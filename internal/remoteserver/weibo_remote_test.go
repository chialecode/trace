package remoteserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site/weibo"
	"github.com/chialecode/trace/internal/subscription"
)

const (
	remoteSentinelWeiboSUB     = "SYNTH_REM_WB_SUB_4f9e2a1b"
	remoteSentinelWeiboSUBP    = "SYNTH_REM_WB_SUBP_8c7d6e5f"
	remoteSentinelWeiboXSRF    = "SYNTH_REM_WB_XSRF_0a1b2c3d"
	remoteSentinelWeiboWBPSESS = "SYNTH_REM_WB_SESS_9e8f7a6b"
	remoteWeiboSelfUID         = "5200000001"
	remoteWeiboSelfName        = "合成远程微博用户"
	remoteWeiboCreatorUID      = "5200000002"
	remoteWeiboCreatorName     = "合成远程微博创作者"
	remoteWeiboMidBase         = "5091000000000001"
	remoteWeiboMidNew          = "5091000000000002"
)

func remoteWeiboCookie() string {
	return "SUB=" + remoteSentinelWeiboSUB + "; SUBP=" + remoteSentinelWeiboSUBP + "; XSRF-TOKEN=" + remoteSentinelWeiboXSRF + "; WBPSESS=" + remoteSentinelWeiboWBPSESS
}

type fakeWeiboRemote struct {
	srv        *httptest.Server
	reqs       atomic.Int64
	expired    atomic.Bool
	publishNew atomic.Bool
}

func newFakeWeiboRemote(t *testing.T) *fakeWeiboRemote {
	t.Helper()
	f := &fakeWeiboRemote{}
	// Minimal 1x1 PNG payload so media download validates cleanly.
	pngBytes := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xff, 0xff, 0x3f,
		0x00, 0x05, 0xfe, 0x02, 0xfe, 0xdc, 0xcc, 0x59,
		0xe7, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}

	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/cdn/") {
			if r.Header.Get("Cookie") != "" {
				t.Errorf("remote weibo CDN request leaked Cookie header on %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(pngBytes)
			return
		}
		f.reqs.Add(1)
		w.Header().Set("Content-Type", "application/json")
		ck := r.Header.Get("Cookie")
		if f.expired.Load() || !strings.Contains(ck, "SUB=") || strings.Contains(ck, "SUB=expired") {
			_, _ = fmt.Fprint(w, `{"ok":-100,"url":"https://passport.weibo.com/sso/signin"}`)
			return
		}
		cdn := f.srv.URL + "/cdn"
		switch r.URL.Path {
		case "/ajax/setting/getBasicInfo":
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"screen_name":%q,"domain":"remself"}}`, remoteWeiboSelfName)
		case "/ajax/profile/info":
			q := r.URL.Query()
			if q.Get("screen_name") == remoteWeiboSelfName {
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q,"statuses_count":5}}}`,
					remoteWeiboSelfUID, remoteWeiboSelfName, cdn+"/self.png")
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q,"statuses_count":10}}}`,
				remoteWeiboCreatorUID, remoteWeiboCreatorName, cdn+"/creator.png")
		case "/ajax/statuses/mymblog":
			basePost := fmt.Sprintf(`{"idstr":%q,"mid":%q,"mblogid":"RemBase01","created_at":"Mon Nov 18 09:00:00 +0800 2026","text_raw":"基线微博","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"pic_ids":["p0"],"pic_infos":{"p0":{"pic_id":"p0","type":"pic","largest":{"url":%q,"width":1,"height":1}}}}`,
				remoteWeiboMidBase, remoteWeiboMidBase, remoteWeiboCreatorUID, remoteWeiboCreatorName, cdn+"/creator.png", cdn+"/p0.png")
			if f.publishNew.Load() {
				newPost := fmt.Sprintf(`{"idstr":%q,"mid":%q,"mblogid":"RemNew002","created_at":"Mon Nov 18 10:00:00 +0800 2026","text_raw":"远程自动缓存新图文微博","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"pic_ids":["p1"],"pic_infos":{"p1":{"pic_id":"p1","type":"pic","largest":{"url":%q,"width":1,"height":1}}}}`,
					remoteWeiboMidNew, remoteWeiboMidNew, remoteWeiboCreatorUID, remoteWeiboCreatorName, cdn+"/creator.png", cdn+"/p1.png")
				_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"since_id":"","total":2,"list":[%s,%s]}}`, newPost, basePost)
				return
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"data":{"since_id":"","total":1,"list":[%s]}}`, basePost)
		case "/ajax/statuses/show":
			id := r.URL.Query().Get("id")
			mid := id
			if decoded, err := weibo.MblogIDToMid(id); err == nil {
				mid = decoded
			}
			_, _ = fmt.Fprintf(w, `{"ok":1,"idstr":%q,"mid":%q,"mblogid":%q,"created_at":"Mon Nov 18 10:00:00 +0800 2026","text_raw":"远程自动缓存新图文微博","user":{"idstr":%q,"screen_name":%q,"avatar_hd":%q},"pic_ids":["p1"],"pic_infos":{"p1":{"pic_id":"p1","type":"pic","largest":{"url":%q,"width":1,"height":1}}}}`,
				mid, mid, id, remoteWeiboCreatorUID, remoteWeiboCreatorName, cdn+"/creator.png", cdn+"/p1.png")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeWeiboRemote) options(bo *service.Options) {
	u := f.srv.URL
	bo.WeiboOpts = weibo.ClientOptions{
		Endpoints: weibo.Endpoints{
			PCBasicInfo:     u + "/ajax/setting/getBasicInfo",
			ProfileInfo:     u + "/ajax/profile/info",
			StatusesShow:    u + "/ajax/statuses/show",
			StatusesLong:    u + "/ajax/statuses/longtext",
			StatusesMyMblog: u + "/ajax/statuses/mymblog",
			TVComponent:     u + "/tv/api/component",
			Friends:         u + "/ajax/friendships/friends",
			AllGroups:       u + "/ajax/feed/allGroups",
			AllFavorites:    u + "/ajax/favorites/all_fav",
			LikeList:        u + "/ajax/statuses/likelist",
		},
		MinInterval:    -1,
		JitterMax:      -1,
		AllowLocalhost: true,
	}
}

func TestRemoteWeiboCredentialPushRejectDeleteAndThreeSiteStatus(t *testing.T) {
	dataDir, res := initServer(t)
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	wb := newFakeWeiboRemote(t)
	var logBuf syncBuffer
	bo := service.Options{
		Locations:        service.RemoteLocations(dataDir, cfg.ExportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         cfg.Name,
		DisableBgWorkers: true,
		LogMirror:        &logBuf,
	}
	wb.options(&bo)
	b, err := service.NewBackend(bo)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })

	s := New(Options{DataDir: dataDir, TokenHash: cfg.TokenHash, Service: b})
	h := httptest.NewServer(s)
	t.Cleanup(h.Close)

	doReq := func(method, path string, body []byte) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest(method, h.URL+path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+res.Conn.Token)
		req.Header.Set(remote.HeaderProtocol, remote.ProtocolVersion)
		resp, err := h.Client().Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	// Unknown site must return 404 (never silently fall back to Douyin or Bilibili).
	badBody, _ := json.Marshal(remote.CredentialPush{Cookie: remoteWeiboCookie()})
	if code, _ := doReq("PUT", "/api/v1/credentials/unknown", badBody); code != http.StatusNotFound {
		t.Fatalf("PUT unknown site = %d, want 404", code)
	}

	// Valid Weibo Cookie push validates via PC 2-step flow before saving and echoes zero secrets.
	pushBody, _ := json.Marshal(remote.CredentialPush{Cookie: remoteWeiboCookie()})
	code, raw := doReq("PUT", "/api/v1/credentials/weibo", pushBody)
	if code != http.StatusOK {
		t.Fatalf("PUT weibo valid = %d %s", code, raw)
	}
	var pushed remote.SiteStatus
	if err := json.Unmarshal(raw, &pushed); err != nil {
		t.Fatalf("unmarshal pushed SiteStatus: %v", err)
	}
	if pushed.Site != "weibo" || pushed.State != remote.SiteValid || pushed.Uname != remoteWeiboSelfName || pushed.CookieSyncedAt == "" {
		t.Fatalf("pushed weibo status = %+v", pushed)
	}
	if st := b.StatusOf("weibo"); !st.LoggedIn || st.Source != service.CredentialSourceRemotePush || st.UID != remoteWeiboSelfUID {
		t.Fatalf("stored weibo status = %+v", st)
	}

	// Rejected Weibo push returns 422, echoes no secret, and leaves existing Weibo login untouched.
	rejBody, _ := json.Marshal(remote.CredentialPush{Cookie: "SUB=expired_secret_token_999"})
	code, raw = doReq("PUT", "/api/v1/credentials/weibo", rejBody)
	var eb remote.ErrorBody
	_ = json.Unmarshal(raw, &eb)
	if code != http.StatusUnprocessableEntity || eb.Error.Code != remote.CodeCredentialRejected || bytes.Contains(raw, []byte("expired_secret_token_999")) {
		t.Fatalf("rejected weibo push = %d %s", code, raw)
	}
	if st := b.StatusOf("weibo"); !st.LoggedIn || st.UID != remoteWeiboSelfUID {
		t.Fatalf("rejected push overwrote stored weibo login: %+v", st)
	}

	// GET /api/v1/status lists all 3 sites (bilibili, douyin, weibo).
	code, statusRaw := doReq("GET", "/api/v1/status", nil)
	if code != http.StatusOK {
		t.Fatalf("GET status = %d %s", code, statusRaw)
	}
	var st remote.Status
	if err := json.Unmarshal(statusRaw, &st); err != nil {
		t.Fatalf("unmarshal status: %v", err)
	}
	seenSites := make(map[string]remote.SiteStatus, len(st.Sites))
	for _, row := range st.Sites {
		seenSites[row.Site] = row
	}
	for _, want := range []string{"bilibili", "douyin", "weibo"} {
		if _, ok := seenSites[want]; !ok {
			t.Fatalf("GET /api/v1/status missing site %q: %+v", want, st.Sites)
		}
	}
	if seenSites["weibo"].State != remote.SiteValid {
		t.Fatalf("status weibo row = %+v, want valid", seenSites["weibo"])
	}

	// DELETE /api/v1/credentials/weibo clears Weibo login cleanly.
	if code, raw = doReq("DELETE", "/api/v1/credentials/weibo", nil); code != http.StatusOK {
		t.Fatalf("DELETE weibo credential = %d %s, want 200", code, raw)
	}
	var cleared remote.SiteStatus
	if err := json.Unmarshal(raw, &cleared); err != nil || cleared.Site != "weibo" || cleared.State != remote.SiteNone {
		t.Fatalf("DELETE weibo response = %s (%v), want site=weibo state=none", raw, err)
	}
	if b.StatusOf("weibo").LoggedIn {
		t.Fatal("Weibo still logged in after DELETE credential")
	}

	// Zero Weibo cookie sentinels in responses, logs, or any file on disk.
	sentinels := []string{res.Conn.Token, remoteSentinelWeiboSUB, remoteSentinelWeiboSUBP, remoteSentinelWeiboXSRF, remoteSentinelWeiboWBPSESS}
	for _, sVal := range sentinels {
		if bytes.Contains(statusRaw, []byte(sVal)) || strings.Contains(logBuf.String(), sVal) {
			t.Fatalf("sentinel %q leaked into status or log mirror", sVal[:8])
		}
	}
	root := filepath.Dir(dataDir)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		for _, sVal := range sentinels {
			if bytes.Contains(data, []byte(sVal)) {
				t.Errorf("sentinel %q leaked into file %s", sVal[:8], p)
			}
		}
		return nil
	})
}

func TestRemoteWeiboSubscriptionSyncCheckAutoCacheAndNoFullCache(t *testing.T) {
	dataDir, _ := initServer(t)
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	wb := newFakeWeiboRemote(t)
	var logBuf syncBuffer
	bo := service.Options{
		Locations:        service.RemoteLocations(dataDir, cfg.ExportDir),
		Desktop:          service.NewMockDesktop(),
		Profile:          service.ProfileRemote,
		NodeName:         cfg.Name,
		DisableBgWorkers: false,
		LogMirror:        &logBuf,
	}
	wb.options(&bo)
	b, err := service.NewBackend(bo)
	if err != nil {
		t.Fatalf("NewBackend: %v", err)
	}
	t.Cleanup(func() { _ = b.Close() })
	ctx := context.Background()

	// Push valid Weibo cookie via remote server API method.
	if _, err := b.AcceptPushedCredential(ctx, "weibo", remoteWeiboCookie()); err != nil {
		t.Fatalf("AcceptPushedCredential(weibo): %v", err)
	}

	// Sync a subscription snapshot containing a Weibo account with FullCache=true + AutoCache=true.
	snap := subscription.Snapshot{
		Format:  subscription.SnapshotFormat,
		Version: subscription.SnapshotVersion,
		Subscriptions: []subscription.SnapshotSubscription{{
			ID:   "sub_wb",
			Name: "合成微博订阅",
			Tags: []string{
				subscription.TagScheduledCheck,
				subscription.TagCacheVideo,
				subscription.TagCacheDraw,
				subscription.TagCacheWord,
				subscription.TagFullCache,
			},
			AutoCacheSince: "2026-11-18T00:00:00Z",
			Accounts: []subscription.SnapshotAccount{{
				Site: "weibo",
				UID:  remoteWeiboCreatorUID,
				Name: remoteWeiboCreatorName,
			}},
		}},
	}
	snapRaw, _ := json.Marshal(snap)
	info, err := b.ApplyRemoteSnapshot(snapRaw)
	if err != nil || info.Subscriptions != 1 || info.Accounts != 1 {
		t.Fatalf("ApplyRemoteSnapshot = %+v err=%v", info, err)
	}

	// Remote must note that full_cache does NOT run on remote and must not enqueue any full-cache queue.
	st := b.ServerStatus()
	if len(st.Notes) == 0 {
		t.Fatal("expected ServerStatus.Notes to mention full_cache not running on remote")
	}
	if len(b.ListCacheQueue()) != 0 {
		t.Fatalf("remote unexpectedly started cache queues: %+v", b.ListCacheQueue())
	}

	// First check establishes baseline (sees remoteWeiboMidBase, 0 new items).
	sum1, err := b.CheckSubscription(ctx, "sub_wb")
	if err != nil {
		t.Fatalf("first CheckSubscription(weibo): %v", err)
	}
	if sum1.NewVideos+sum1.NewDynamics != 0 {
		t.Fatalf("baseline check reported %+v, want 0 new items", sum1)
	}

	// Re-applying the same snapshot preserves the Weibo account and its established baseline.
	if _, err := b.ApplyRemoteSnapshot(snapRaw); err != nil {
		t.Fatalf("re-apply snapshot: %v", err)
	}

	// Publish a new Weibo post; second check detects it and auto-caches it into remote ExportDir.
	wb.publishNew.Store(true)
	sum2, err := b.CheckSubscription(ctx, "sub_wb")
	if err != nil {
		t.Fatalf("second CheckSubscription(weibo): %v", err)
	}
	if sum2.NewVideos+sum2.NewDynamics != 1 {
		t.Fatalf("second check summary = %+v, want 1 new item", sum2)
	}

	// Wait for auto-cached Weibo item to finish downloading into ExportDir.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, ok, _ := b.LibraryStore().GetItem("weibo:dynamic:" + remoteWeiboMidNew); ok {
			break
		}
		tasks, _ := b.ListDownloadTasks()
		allDone := true
		for _, tk := range tasks {
			if tk.Status == "failed" {
				t.Fatalf("remote auto-cache task failed: %+v", tk)
			}
			if tk.Status != "completed" {
				allDone = false
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("auto-cached Weibo post did not finish; sum2=%+v tasks=%+v logs=%s", sum2, tasks, logBuf.String())
		}
		if allDone && len(tasks) > 0 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	cachedItem, ok, err := b.LibraryStore().GetItem("weibo:dynamic:" + remoteWeiboMidNew)
	if err != nil || !ok || !strings.HasPrefix(filepath.Clean(cachedItem.ItemDir), filepath.Clean(cfg.ExportDir)) {
		t.Fatalf("auto-cached item = %+v ok=%v err=%v (want inside %s)", cachedItem, ok, err, cfg.ExportDir)
	}

	// When Weibo cookie expires on remote, remote stops Weibo requests and status reports SiteResync.
	wb.expired.Store(true)
	_, _ = b.CheckSubscription(ctx, "sub_wb")
	stAfter := b.ServerStatus()
	var wbRow remote.SiteStatus
	for _, r := range stAfter.Sites {
		if r.Site == "weibo" {
			wbRow = r
		}
	}
	if wbRow.State != remote.SiteResync || wbRow.Message == "" {
		t.Fatalf("expired remote weibo status = %+v, want resync", wbRow)
	}
	reqsAfterExpire := wb.reqs.Load()
	// Subsequent check while expired must make 0 Weibo HTTP requests!
	_, _ = b.CheckSubscription(ctx, "sub_wb")
	if wb.reqs.Load() != reqsAfterExpire {
		t.Fatalf("expired remote Weibo still sent %d HTTP requests, want 0", wb.reqs.Load()-reqsAfterExpire)
	}

	// Full sentinel check across status, log mirror, and disk.
	stRaw, _ := json.Marshal(stAfter)
	sentinels := []string{remoteSentinelWeiboSUB, remoteSentinelWeiboSUBP, remoteSentinelWeiboXSRF, remoteSentinelWeiboWBPSESS}
	for _, sVal := range sentinels {
		if bytes.Contains(stRaw, []byte(sVal)) || strings.Contains(logBuf.String(), sVal) {
			t.Fatalf("sentinel %q leaked into status or log", sVal[:8])
		}
	}
	root := filepath.Dir(dataDir)
	_ = filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(p)
		for _, sVal := range sentinels {
			if bytes.Contains(data, []byte(sVal)) {
				t.Errorf("sentinel %q leaked into file %s", sVal[:8], p)
			}
		}
		return nil
	})
}
