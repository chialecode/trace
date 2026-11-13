package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/appdirs"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

type fakeQueueClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeQueueTimer
	history []time.Duration
	added   chan struct{}
}

type fakeQueueTimer struct {
	clock    *fakeQueueClock
	deadline time.Time
	dur      time.Duration
	ch       chan time.Time
	fired    bool
	stopped  bool
}

func newFakeQueueClock(start time.Time) *fakeQueueClock {
	return &fakeQueueClock{
		now:   start.UTC(),
		added: make(chan struct{}, 16),
	}
}

func (c *fakeQueueClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeQueueClock) NewTimer(d time.Duration) CacheQueueTimer {
	c.mu.Lock()
	tm := &fakeQueueTimer{
		clock:    c,
		deadline: c.now.Add(d),
		dur:      d,
		ch:       make(chan time.Time, 1),
	}
	c.history = append(c.history, d)
	if d <= 0 {
		tm.fired = true
		tm.ch <- c.now
		c.mu.Unlock()
		return tm
	}
	c.timers = append(c.timers, tm)
	c.mu.Unlock()
	select {
	case c.added <- struct{}{}:
	default:
	}
	return tm
}

func (t *fakeQueueTimer) C() <-chan time.Time {
	return t.ch
}

func (t *fakeQueueTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.fired || t.stopped {
		return false
	}
	t.stopped = true
	return true
}

func (c *fakeQueueClock) ActiveTimerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, tm := range c.timers {
		if !tm.fired && !tm.stopped {
			n++
		}
	}
	return n
}

func (c *fakeQueueClock) History() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]time.Duration, len(c.history))
	copy(out, c.history)
	return out
}

func (c *fakeQueueClock) WaitForPendingTimer(t *testing.T, timeout time.Duration) (time.Duration, time.Time) {
	t.Helper()
	return c.WaitForPendingTimerMax(t, 0, timeout)
}

func (c *fakeQueueClock) WaitForPendingTimerMax(t *testing.T, maxDur, timeout time.Duration) (time.Duration, time.Time) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		var earliest *fakeQueueTimer
		for _, tm := range c.timers {
			if !tm.fired && !tm.stopped {
				if maxDur > 0 && tm.dur > maxDur {
					continue
				}
				if earliest == nil || tm.deadline.Before(earliest.deadline) {
					earliest = tm
				}
			}
		}
		if earliest != nil {
			d, dl := earliest.dur, earliest.deadline
			c.mu.Unlock()
			return d, dl
		}
		c.mu.Unlock()

		rem := time.Until(deadline)
		if rem <= 0 {
			t.Fatalf("timed out after %v waiting for cache queue timer (maxDur=%v)", timeout, maxDur)
		}
		wait := min(rem, 15*time.Millisecond)
		select {
		case <-c.added:
		case <-time.After(wait):
		}
	}
}

func (c *fakeQueueClock) Advance(d time.Duration) int {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	var toFire []*fakeQueueTimer
	for _, tm := range c.timers {
		if !tm.fired && !tm.stopped && !now.Before(tm.deadline) {
			tm.fired = true
			toFire = append(toFire, tm)
		}
	}
	c.mu.Unlock()
	for _, tm := range toFire {
		tm.ch <- now
	}
	return len(toFire)
}

func (c *fakeQueueClock) AdvanceToNextTimer() (time.Duration, bool) {
	c.mu.Lock()
	var earliest *fakeQueueTimer
	for _, tm := range c.timers {
		if !tm.fired && !tm.stopped {
			if earliest == nil || tm.deadline.Before(earliest.deadline) {
				earliest = tm
			}
		}
	}
	if earliest == nil {
		c.mu.Unlock()
		return 0, false
	}
	step := earliest.deadline.Sub(c.now)
	if step < 0 {
		step = 0
	}
	c.mu.Unlock()
	c.Advance(step)
	return step, true
}

func newBatchTestBackend(t *testing.T, clock *fakeQueueClock) *Backend {
	t.Helper()
	srv := newTestBiliServer(t)
	t.Cleanup(srv.Close)
	tmp := t.TempDir()
	loc, err := appdirs.ResolveWithOptions(appdirs.Options{
		Channel:   "dev",
		ForceMode: "portable",
		DataDir:   filepath.Join(tmp, "trace-data"),
		CacheDir:  filepath.Join(tmp, "trace-cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(Options{
		Locations:        loc,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clock,
		CacheQueueRand:   func(int) int { return 0 },
		BilibiliOpts: bilibili.ClientOptions{
			Endpoints: bilibili.Endpoints{
				APIBase:      srv.URL,
				PassportBase: srv.URL,
				WWWBase:      srv.URL,
				SpaceBase:    srv.URL,
				DynamicBase:  srv.URL,
				LiveAPIBase:  srv.URL,
				LiveWebBase:  srv.URL,
			},
			PacingInterval: time.Millisecond,
			AllowLocalhost: true,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if _, err := b.ImportCookie(context.Background(), "SESSDATA=s; bili_jct=j; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	return b
}

// Migrated from DEC-046 ⑥ -> DEC-050 ①: the cache queue walks loaded items and older pages,
// skipping already-cached, repeated, and uncacheable items without waiting 40-150s (no request is
// sent for them), while pacing uncached items one by one until list exhaustion. An ordinary failure
// already sent site requests, so it is counted as skipped and still waits the normal gap.
func TestCacheQueueSkipsCachedDuplicatesAndOrdinaryItemErrors(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)

	dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1", "dynamics", "d2")
	_ = os.MkdirAll(dir, 0o700)
	if _, err := b.lib.SaveItem(dir, library.ItemMetadata{
		Version: 1, ID: "d2", RawID: "d2", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: library.ItemAuthor{Site: "bilibili", UID: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var queued []string
	cand := func(id string, uncacheable bool, fail error) queueCandidate {
		key := library.CanonicalItemKey("bilibili", "dynamic", id)
		return queueCandidate{
			key:         key,
			title:       "动态 " + id,
			uncacheable: uncacheable,
			enqueue: func(context.Context) (queueEnqueued, error) {
				if fail != nil {
					return queueEnqueued{}, fail
				}
				mu.Lock()
				queued = append(queued, id)
				mu.Unlock()
				return queueEnqueued{itemKey: key, title: "动态 " + id}, nil
			},
		}
	}

	pages := 0
	pager := func(_ context.Context, cursor string) ([]queueCandidate, string, bool, error) {
		pages++
		if cursor != "c0" {
			return nil, "", false, fmt.Errorf("unexpected cursor %q", cursor)
		}
		return []queueCandidate{
			cand("p1-a", false, nil),
			cand("p1-b", false, nil),
		}, "c1", false, nil
	}

	loaded := []queueCandidate{
		cand("d1", false, nil),
		cand("d2", false, nil),                   // already cached in library -> skip without sleep
		cand("d1", false, nil),                   // duplicate -> ignored without double-counting skip
		cand("d3", true, nil),                    // uncacheable -> skip without sleep
		cand("d4", false, fmt.Errorf("deleted")), // ordinary error -> skipped, but keeps the normal gap
		cand("d5", false, nil),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.queue.Start(ctx)

	task, err := b.enqueueCacheQueueWithPager(site.Bilibili, "dynamics", "1", CacheQueueRequest{
		Site:     "bilibili",
		List:     "dynamics",
		User:     "1",
		UserName: "测试UP",
		Cursor:   "c0",
		HasMore:  true,
	}, loaded, pager)
	if err != nil {
		t.Fatalf("enqueueCacheQueueWithPager: %v", err)
	}

	// Drive 4 inter-item waits (after d1, after the failed d4, after d5, after p1-a); p1-b is the
	// last item so the task completes immediately.
	for step := 0; step < 4; step++ {
		dur, _ := clock.WaitForPendingTimer(t, 2*time.Second)
		if dur != 40*time.Second {
			t.Fatalf("step %d timer = %v, want 40s", step, dur)
		}
		clock.Advance(dur)
	}

	var final CacheQueueTask
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		list := b.ListCacheQueue()
		if len(list) == 1 && list[0].Status == CacheQueueCompleted {
			final = list[0]
			break
		}
	}
	if final.Status != CacheQueueCompleted {
		t.Fatalf("task %s did not complete: %+v", task.ID, b.ListCacheQueue())
	}

	mu.Lock()
	gotQueued := append([]string(nil), queued...)
	mu.Unlock()
	want := []string{"d1", "d5", "p1-a", "p1-b"}
	if fmt.Sprint(gotQueued) != fmt.Sprint(want) {
		t.Fatalf("queued = %v, want %v", gotQueued, want)
	}
	if final.CachedCount != 4 || final.SkippedCount != 3 || pages != 1 || final.Cursor != "c1" || final.HasMore {
		t.Fatalf("unexpected final task summary: %+v (pages=%d)", final, pages)
	}
	if len(clock.History()) != 4 {
		t.Fatalf("expected exactly 4 inter-item timers (local skips must not sleep, failed requests must), got %d: %v", len(clock.History()), clock.History())
	}
}

func TestEnqueueCacheQueueValidatesRequestAndLoadedList(t *testing.T) {
	clock := newFakeQueueClock(time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clock)
	ctx := context.Background()

	for _, bad := range []CacheQueueRequest{
		{Site: "bilibili", List: "dynamics", User: "x"},
		{Site: "bilibili", List: "dynamics", User: "0"},
		{Site: "bilibili", List: "favorites", User: "1"},
		{Site: "douyin", List: "works", User: ""},
		{Site: "douyin", List: "works", User: "bad?sig=1"},
		{Site: "douyin", List: "videos", User: "MS4wLjABAAAA_ok"},
		{Site: "unknown", List: "videos", User: "1"},
	} {
		if _, err := b.EnqueueCacheQueue(ctx, bad); err == nil {
			t.Fatalf("expected error for invalid request %+v", bad)
		}
	}

	// Douyin is not logged in on newBatchTestBackend -> refused with auth_required.
	if _, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "douyin",
		List: "works",
		User: "MS4wLjABAAAA_synth_creator",
	}); !site.IsKind(err, site.ErrKindAuthRequired) {
		t.Fatalf("expected auth_required for logged-out douyin, got %v", err)
	}

	// Pre-saving h1 in library and passing it as the sole loaded item with HasMore=false completes with 0 network calls.
	dir := filepath.Join(b.lib.CacheDir(), "bilibili", "1", "dynamics", "h1")
	_ = os.MkdirAll(dir, 0o700)
	if _, err := b.lib.SaveItem(dir, library.ItemMetadata{
		Version: 1, ID: "h1", RawID: "h1", Site: "bilibili", Kind: "dynamic", Status: "complete",
		Author: library.ItemAuthor{Site: "bilibili", UID: "1"},
	}); err != nil {
		t.Fatal(err)
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	b.queue.Start(runCtx)

	created, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site:     "bilibili",
		List:     "dynamics",
		User:     "1",
		Dynamics: []bilibili.DynamicItem{{ID: "h1", Author: bilibili.AuthorInfo{Mid: 1}}},
		HasMore:  false,
	})
	if err != nil {
		t.Fatalf("EnqueueCacheQueue loaded-only: %v", err)
	}

	var done CacheQueueTask
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		list := b.ListCacheQueue()
		if len(list) == 1 && list[0].Status == CacheQueueCompleted {
			done = list[0]
			break
		}
	}
	if done.ID != created.ID || done.Status != CacheQueueCompleted || done.SkippedCount != 1 || done.CachedCount != 0 {
		t.Fatalf("loaded-only completion mismatch: %+v", done)
	}
}
