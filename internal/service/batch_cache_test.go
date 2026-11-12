package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

func newBatchTestBackend(t *testing.T) *Backend {
	t.Helper()
	srv := newTestBiliServer(t)
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{Channel: "dev", ForceMode: "portable",
		DataDir: filepath.Join(tmp, "trace-data"), CacheDir: filepath.Join(tmp, "trace-cache")})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(Options{Locations: loc, Desktop: NewMockDesktop(), DisableBgWorkers: true,
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints:      bilibili.Endpoints{APIBase: srv.URL, PassportBase: srv.URL, WWWBase: srv.URL, SpaceBase: srv.URL, DynamicBase: srv.URL, LiveAPIBase: srv.URL, LiveWebBase: srv.URL},
			PacingInterval: time.Millisecond, AllowLocalhost: true,
		}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if _, err := b.ImportCookie(context.Background(), "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	return b
}

// DEC-046 ⑥: a batch queues only uncached posts, from the loaded list on and
// then older pages, stops at its size, and ends at risk control.
func TestCacheOlderQueuesOnlyUncachedUpToTheLimit(t *testing.T) {
	b := newBatchTestBackend(t)
	dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1", "dynamics", "d2")
	_ = os.MkdirAll(dir, 0o700)
	if _, err := b.lib.SaveItem(dir, library.ItemMetadata{Version: 1, ID: "d2", RawID: "d2", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: library.ItemAuthor{Site: "bilibili", UID: "1"}}); err != nil {
		t.Fatal(err)
	}
	var queued []string
	cand := func(id string, fail error) batchCandidate {
		return batchCandidate{key: library.CanonicalItemKey("bilibili", "dynamic", id), enqueue: func(context.Context) error {
			if fail != nil {
				return fail
			}
			queued = append(queued, id)
			return nil
		}}
	}
	pages := 0
	pager := func(_ context.Context, cursor string) ([]batchCandidate, string, bool, error) {
		pages++
		n := pages
		return []batchCandidate{cand(fmt.Sprintf("p%d-a", n), nil), cand(fmt.Sprintf("p%d-b", n), nil)}, fmt.Sprintf("c%d", n), true, nil
	}
	loaded := []batchCandidate{cand("d1", nil), cand("d2", nil), cand("d1", nil), cand("d3", nil)}
	res := b.runCacheOlder(context.Background(), 4, loaded, "c0", true, pager)
	if want := []string{"d1", "d3", "p1-a", "p1-b"}; fmt.Sprint(queued) != fmt.Sprint(want) {
		t.Fatalf("queued %v, want %v (cached d2 and the repeat skipped)", queued, want)
	}
	if res.Enqueued != 4 || res.Skipped != 1 || res.Pages != 1 || res.Stopped != "" || res.NextCursor != "c1" {
		t.Fatalf("result %+v", res)
	}

	// Risk control while paging ends the batch with what was queued.
	queued, pages = nil, 0
	riskPager := func(context.Context, string) ([]batchCandidate, string, bool, error) {
		return nil, "", false, &site.Error{Site: site.Bilibili, Kind: site.ErrKindRiskControl, Message: "-352"}
	}
	res = b.runCacheOlder(context.Background(), 20, []batchCandidate{cand("e1", nil)}, "c0", true, riskPager)
	if res.Enqueued != 1 || res.Stopped == "" || res.Exhausted {
		t.Fatalf("risk control while paging: %+v", res)
	}

	// Risk control on an enqueue stops the rest at once.
	queued = nil
	risk := &site.Error{Site: site.Douyin, Kind: site.ErrKindRateLimited}
	res = b.runCacheOlder(context.Background(), 20, []batchCandidate{cand("f1", nil), cand("f2", risk), cand("f3", nil)}, "", false, pager)
	if fmt.Sprint(queued) != "[f1]" || res.Stopped == "" {
		t.Fatalf("rate limit on enqueue: queued %v, %+v", queued, res)
	}

	// The end of the list is reported; an ordinary failure skips one post.
	queued = nil
	res = b.runCacheOlder(context.Background(), 20, []batchCandidate{cand("g1", fmt.Errorf("gone")), cand("g2", nil)}, "", false, pager)
	if fmt.Sprint(queued) != "[g2]" || !res.Exhausted || res.Skipped != 1 {
		t.Fatalf("end of list: queued %v, %+v", queued, res)
	}
}

// Sizes other than 10, 20 and 50 fall back to 20; a bad user is refused
// before any request.
func TestCacheOlderValidatesItsRequest(t *testing.T) {
	b := newBatchTestBackend(t)
	if _, err := b.CacheOlderUserItems(context.Background(), CacheOlderRequest{Site: "bilibili", List: "dynamics", User: "x"}); err == nil {
		t.Fatal("a bad UID was accepted")
	}
	res, err := b.CacheOlderUserItems(context.Background(), CacheOlderRequest{Site: "bilibili", List: "dynamics", User: "1", Limit: 999,
		Dynamics: []bilibili.DynamicItem{{ID: "h1", Author: bilibili.AuthorInfo{Mid: 1}}}})
	if err != nil || res.Enqueued != 1 || !res.Exhausted {
		t.Fatalf("loaded-only batch: %+v %v", res, err)
	}
}
