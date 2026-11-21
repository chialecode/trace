package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
)

// A cache queue (全部缓存) caches items of the list that are cached but
// incomplete: partial, complete with missing records, or with expected pages
// lacking a file. It skips complete items, items that only have a better
// quality available and paused ones (dev 2026-11-20: partial notes were
// never retried by 全部缓存).
func TestCacheQueueCompletesIncompleteItems(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)

	save := func(id string, meta library.ItemMetadata) {
		t.Helper()
		dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1", "dynamics", id)
		_ = os.MkdirAll(dir, 0o700)
		meta.Version, meta.ID, meta.RawID, meta.Site, meta.Kind = 1, id, id, "bilibili", "dynamic"
		meta.Author = library.ItemAuthor{Site: "bilibili", UID: "1", Mid: 1}
		if _, err := b.lib.SaveItem(dir, meta); err != nil {
			t.Fatal(err)
		}
	}
	save("BVcomplete", library.ItemMetadata{Status: "complete"})
	save("BVpartial", library.ItemMetadata{Status: "partial", Missing: []library.MissingRecord{{Item: "P02", Reason: "网络中断"}}})
	save("BVmissing", library.ItemMetadata{Status: "complete", Missing: []library.MissingRecord{{Item: "danmaku", Reason: "弹幕下载失败"}}})
	save("BVpages", library.ItemMetadata{Status: "complete", ExpectedPages: []int{1, 2}, Parts: []library.VideoPartRecord{{Page: 1, File: "P01.mp4"}}})
	save("BVupgrade", library.ItemMetadata{Status: "complete", UpgradeAvailable: true})
	save("BVpaused", library.ItemMetadata{Status: "paused"})

	var mu sync.Mutex
	var queued []string
	cand := func(id string) queueCandidate {
		key := library.CanonicalItemKey("bilibili", "dynamic", id)
		return queueCandidate{key: key, title: id, enqueue: func(context.Context) (queueEnqueued, error) {
			mu.Lock()
			queued = append(queued, id)
			mu.Unlock()
			return queueEnqueued{itemKey: key, title: id}, nil
		}}
	}
	ids := []string{"BVcomplete", "BVpartial", "BVmissing", "BVpages", "BVupgrade", "BVpaused", "BVnew"}
	var loaded []queueCandidate
	for _, id := range ids {
		loaded = append(loaded, cand(id))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.queue.Start(ctx)
	if _, err := b.enqueueCacheQueueWithPager(site.Bilibili, "dynamics", "1", CacheQueueRequest{Site: "bilibili", List: "dynamics", User: "1", UserName: "测试UP"}, loaded,
		func(context.Context, string) ([]queueCandidate, string, bool, error) { return nil, "", false, nil }); err != nil {
		t.Fatal(err)
	}
	// One gap after each cached item but the last.
	for step := 0; step < 3; step++ {
		dur, _ := clock.WaitForPendingTimer(t, 2*time.Second)
		clock.Advance(dur)
	}
	var final CacheQueueTask
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if list := b.ListCacheQueue(); len(list) == 1 && list[0].Status == CacheQueueCompleted {
			final = list[0]
			break
		}
	}
	mu.Lock()
	got := fmt.Sprint(queued)
	mu.Unlock()
	if want := fmt.Sprint([]string{"BVpartial", "BVmissing", "BVpages", "BVnew"}); got != want {
		t.Fatalf("queued = %s, want %s", got, want)
	}
	if final.CachedCount != 4 || final.SkippedCount != 3 {
		t.Fatalf("summary = cached %d skipped %d (status %q)", final.CachedCount, final.SkippedCount, final.Status)
	}
}
