package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

// countingBili answers nav for any SESSDATA and counts the requests by
// path; everything else is 404.
func countingBili(t *testing.T) (*httptest.Server, *siteCounts) {
	t.Helper()
	c := &siteCounts{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.total.Add(1)
		if strings.Contains(r.URL.Path, "cookie") || strings.Contains(r.URL.Path, "refresh") || strings.Contains(r.URL.Path, "correspond") {
			c.renewal.Add(1)
		}
		if r.URL.Path == "/x/web-interface/nav" && strings.Contains(r.Header.Get("Cookie"), "SESSDATA=") {
			_, _ = w.Write([]byte(`{"code":0,"data":{"isLogin":true,"mid":778899,"uname":"合成远程用户","wbi_img":{"img_url":"https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png","sub_url":"https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

type siteCounts struct{ total, renewal atomic.Int64 }

func remoteTestBackend(t *testing.T, srvURL string) (*Backend, string) {
	t.Helper()
	root := t.TempDir()
	export := filepath.Join(root, "export")
	b, err := NewBackend(Options{
		Locations: RemoteLocations(filepath.Join(root, "data"), export),
		Desktop:   NewMockDesktop(),
		Profile:   ProfileRemote,
		NodeName:  "测试节点",
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints:      bilibili.Endpoints{APIBase: srvURL, PassportBase: srvURL, WWWBase: srvURL, SpaceBase: srvURL, DynamicBase: srvURL, LiveAPIBase: srvURL, LiveWebBase: srvURL},
			PacingInterval: -1, AllowLocalhost: true,
		},
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b, export
}

// On the remote there is no renewal, 全部缓存 never builds a queue,
// manual caching returns a clear error, scheduled checking stays on and the
// save location is the export folder.
func TestRemoteProfileHasNoRenewalNoFullCacheAndChecksOn(t *testing.T) {
	srv, counts := countingBili(t)
	b, export := remoteTestBackend(t, srv.URL)
	ctx := context.Background()

	s := b.GetSettings()
	if !s.ScheduledCheck || !s.Subscription.Enabled || filepath.Clean(s.CacheDir) != filepath.Clean(export) {
		t.Fatalf("remote settings = check %v/%v cache %q", s.ScheduledCheck, s.Subscription.Enabled, s.CacheDir)
	}
	s.ScheduledCheck, s.Subscription.Enabled, s.CacheDir = false, false, filepath.Join(t.TempDir(), "elsewhere")
	if got, err := b.UpdateSettings(s); err != nil || !got.ScheduledCheck || !got.Subscription.Enabled || filepath.Clean(got.CacheDir) != filepath.Clean(export) {
		t.Fatalf("settings after trying to turn checks off = %+v %v", got.ScheduledCheck, err)
	}

	if _, err := b.AcceptPushedCredential(ctx, "bilibili", "SESSDATA=synthetic; bili_jct=x"); err != nil {
		t.Fatalf("AcceptPushedCredential: %v", err)
	}
	snap := subscription.Snapshot{Format: subscription.SnapshotFormat, Version: subscription.SnapshotVersion,
		Subscriptions: []subscription.SnapshotSubscription{{ID: "s1", Name: "合成", Tags: []string{subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagFullCache}, Accounts: []subscription.SnapshotAccount{{Site: "bilibili", UID: "1001"}}}}}
	raw, _ := json.Marshal(snap)
	if _, err := b.ApplyRemoteSnapshot(raw); err != nil {
		t.Fatal(err)
	}

	before := counts.total.Load()
	if r := b.Renewal("bilibili"); r.Available || r.Reason != "由本地续期" {
		t.Fatalf("Renewal = %+v", r)
	}
	if _, err := b.RefreshCookie(ctx); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("RefreshCookie = %v, want ErrRemoteUnsupported", err)
	}
	if ids := b.startDueFullCacheSweeps(ctx, b.StartedAt().AddDate(1, 0, 0)); len(ids) != 0 {
		t.Fatalf("full-cache sweep started %v on the remote", ids)
	}
	if q := b.ListCacheQueue(); len(q) != 0 {
		t.Fatalf("cache queue on the remote = %+v", q)
	}
	if _, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{Site: "bilibili", List: "videos", User: "1001"}); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("EnqueueCacheQueue = %v, want ErrRemoteUnsupported", err)
	}
	if _, err := b.EnqueueVideoCache(ctx, download.VideoEnqueueRequest{BVID: "BV1xx411c7mD"}); !errors.Is(err, ErrRemoteUnsupported) {
		t.Fatalf("EnqueueVideoCache = %v, want ErrRemoteUnsupported", err)
	}
	if n := counts.total.Load() - before; n != 0 {
		t.Fatalf("%d site requests for renewal or 全部缓存 on the remote", n)
	}
	if counts.renewal.Load() != 0 {
		t.Fatal("the remote sent a renewal request")
	}
	st := b.ServerStatus()
	if len(st.Notes) == 0 || !strings.Contains(st.Notes[0], "全部缓存") {
		t.Fatalf("status notes = %v", st.Notes)
	}
	if acc := b.AccountStatus(); acc.Source != CredentialSourceRemotePush || acc.HasRefreshToken {
		t.Fatalf("pushed credential = %+v", acc)
	}
}

// Items cached here carry cachedBy with this installation's node,
// which stays the same after a restart; old items without it still load.
func TestCachedByNodeIsStableAndOldItemsStillLoad(t *testing.T) {
	loc := testLocations(t)
	open := func() *Backend {
		b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true, NodeName: "台式机"})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := open()
	node := b.LibraryStore().Node()
	if len(node.Node) != 32 || node.Name != "台式机" {
		t.Fatalf("node = %+v", node)
	}
	itemDir := filepath.Join(b.GetSettings().CacheDir, "bilibili", "1001", "videos", "BV1node")
	_ = os.MkdirAll(itemDir, 0o700)
	_ = os.WriteFile(filepath.Join(itemDir, "P01.mp4"), []byte("v"), 0o600)
	meta, err := b.LibraryStore().SaveItem(itemDir, library.ItemMetadata{ID: "video:BV1node", Site: "bilibili", Kind: "video", Title: "t", Media: []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if meta.CachedBy == nil || meta.CachedBy.Node != node.Node {
		t.Fatalf("cachedBy = %+v, want %s", meta.CachedBy, node.Node)
	}
	// An old item.json without cachedBy.
	oldDir := filepath.Join(b.GetSettings().CacheDir, "bilibili", "1001", "videos", "BV1old")
	_ = os.MkdirAll(oldDir, 0o700)
	_ = os.WriteFile(filepath.Join(oldDir, "P01.mp4"), []byte("v"), 0o600)
	_ = os.WriteFile(filepath.Join(oldDir, "item.json"), []byte(`{"version":1,"id":"video:BV1old","site":"bilibili","kind":"video","title":"旧","status":"complete","media":[{"role":"video","path":"P01.mp4","bytes":1}]}`), 0o600)
	_ = b.Close()

	b = open()
	defer b.Close()
	if again := b.LibraryStore().Node(); again.Node != node.Node {
		t.Fatalf("node after restart = %s, want %s", again.Node, node.Node)
	}
	if _, err := b.LibraryStore().RefreshIndex(); err != nil {
		t.Fatal(err)
	}
	old, ok, err := b.LibraryStore().GetItem("BV1old")
	if err != nil || !ok || old.Title != "旧" {
		t.Fatalf("old item = %+v %v %v", old, ok, err)
	}
	raw, _ := os.ReadFile(filepath.Join(oldDir, "item.json"))
	if strings.Contains(string(raw), "cachedBy") {
		t.Fatal("reading an old item rewrote it with cachedBy")
	}
}
