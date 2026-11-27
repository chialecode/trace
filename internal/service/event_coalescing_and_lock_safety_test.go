package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func rawCacheQueueTasks(b *Backend) []CacheQueueTask {
	b.queue.mu.Lock()
	defer b.queue.mu.Unlock()
	out := make([]CacheQueueTask, 0, len(b.queue.tasks))
	for _, e := range b.queue.tasks {
		out = append(out, b.queue.toDTOLocked(e))
	}
	return out
}

// TestWeiboBackfillTierABatchCoalescesLibraryEvents ensures a large Tier A
// offline inline-link backfill batch publishes ONE coalesced EventLibraryChanged
// instead of flooding the UI event loop with N per-item events, while a 1-item
// batch preserves the exact ItemKey message.
func TestWeiboBackfillTierABatchCoalescesLibraryEvents(t *testing.T) {
	t.Run("batch_many_coalesces_to_single_event", func(t *testing.T) {
		clock := newFakeQueueClock(time.Now().UTC())
		b, err := NewBackend(Options{
			Locations:        testLocations(t),
			Desktop:          NewMockDesktop(),
			DisableBgWorkers: true,
			CacheQueueClock:  clock,
			CacheQueueRand:   func(int) int { return 0 },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = b.Close() }()

		const batchSize = 40
		for i := 0; i < batchSize; i++ {
			mid := fmt.Sprintf("509900000000%04d", i+1)
			seedWeiboStaticItemForBackfill(
				t, b, "100099", mid, "", nil, 2, nil,
				fmt.Sprintf("历史正文 #%d 原文链接 (https://example.com/item/%d)", i+1, i+1),
				nil, nil,
			)
		}

		var evMu sync.Mutex
		var libEvents []Event
		unsub := b.SubscribeEvents(func(ev Event) {
			if ev.Type == EventLibraryChanged {
				evMu.Lock()
				libEvents = append(libEvents, ev)
				evMu.Unlock()
			}
		})
		defer unsub()

		ctx := context.Background()
		b.queue.Start(ctx)
		if _, err := b.StartWeiboBackfill(ctx); err != nil {
			t.Fatalf("StartWeiboBackfill: %v", err)
		}

		deadline := time.Now().Add(6 * time.Second)
		for {
			list := rawCacheQueueTasks(b)
			if len(list) == 1 && list[0].Status == CacheQueueCompleted {
				if list[0].CachedCount != batchSize {
					t.Fatalf("CachedCount = %d, want %d", list[0].CachedCount, batchSize)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for Tier A backfill completion; queues=%+v", list)
			}
			if _, ok := clock.AdvanceToNextTimer(); !ok {
				time.Sleep(15 * time.Millisecond)
			}
		}

		evMu.Lock()
		got := append([]Event(nil), libEvents...)
		evMu.Unlock()
		if len(got) != 1 {
			t.Fatalf("Tier A batch of %d items published %d EventLibraryChanged events (want 1 coalesced event): %+v", batchSize, len(got), got)
		}
		wantMsg := fmt.Sprintf("backfill:offline_links:%d", batchSize)
		if got[0].Site != string(site.Weibo) || got[0].Message != wantMsg {
			t.Fatalf("coalesced event = %+v, want site=weibo message=%q", got[0], wantMsg)
		}
	})

	t.Run("single_item_preserves_item_key", func(t *testing.T) {
		clock := newFakeQueueClock(time.Now().UTC())
		b, err := NewBackend(Options{
			Locations:        testLocations(t),
			Desktop:          NewMockDesktop(),
			DisableBgWorkers: true,
			CacheQueueClock:  clock,
			CacheQueueRand:   func(int) int { return 0 },
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = b.Close() }()

		saved := seedWeiboStaticItemForBackfill(
			t, b, "100099", "5099000000009001", "", nil, 2, nil,
			"单条历史正文 原文链接 (https://example.com/single)", nil, nil,
		)

		var evMu sync.Mutex
		var libEvents []Event
		unsub := b.SubscribeEvents(func(ev Event) {
			if ev.Type == EventLibraryChanged {
				evMu.Lock()
				libEvents = append(libEvents, ev)
				evMu.Unlock()
			}
		})
		defer unsub()

		ctx := context.Background()
		b.queue.Start(ctx)
		if _, err := b.StartWeiboBackfill(ctx); err != nil {
			t.Fatalf("StartWeiboBackfill: %v", err)
		}

		deadline := time.Now().Add(6 * time.Second)
		for {
			list := rawCacheQueueTasks(b)
			if len(list) == 1 && list[0].Status == CacheQueueCompleted {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for single-item Tier A backfill; queues=%+v", list)
			}
			if _, ok := clock.AdvanceToNextTimer(); !ok {
				time.Sleep(15 * time.Millisecond)
			}
		}

		evMu.Lock()
		got := append([]Event(nil), libEvents...)
		evMu.Unlock()
		if len(got) != 1 || got[0].Message != saved.ItemKey {
			t.Fatalf("single-item Tier A events = %+v, want 1 event with message %q", got, saved.ItemKey)
		}
	})
}

// TestBackoffClearAndDownloadQueueConcurrentNoDeadlock exercises the cross-package
// callback graph introduced by per-site backoff recovery (subscription.Manager.mu ->
// onBackoffCleared -> download.Manager / cacheQueueManager / x.Client / eventBus ->
// synchronous subscriber re-entering queue/download/subs reads) under concurrent load
// with a strict fail-fast timeout to lock in acyclic lock ordering.
func TestBackoffClearAndDownloadQueueConcurrentNoDeadlock(t *testing.T) {
	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = b.Close() }()

	var callbackHits atomic.Int64
	unsub := b.SubscribeEvents(func(ev Event) {
		callbackHits.Add(1)
		_ = b.ListCacheQueue()
		_, _ = b.ListDownloadTasks()
		_, _ = b.subs.IsSiteInBackoff(string(site.Weibo))
		_, _ = b.subs.IsSiteInBackoff(string(site.X))
	})
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		const iters = 150
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				b.subs.RecordSiteAutomaticStop(string(site.Weibo), "risk_control")
				b.onManualSiteRequestSuccess(site.Weibo)
				b.subs.RecordSiteAutomaticStop(string(site.X), "rate_limited")
				b.onManualSiteRequestSuccess(site.X)
			}
		}()

		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				b.dl.Wake()
				_, _ = b.ListDownloadTasks()
				_ = b.ListCacheQueue()
				if b.x != nil {
					b.x.ClearRateLimits()
				}
			}
		}()

		go func() {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				_, _ = b.subs.IsSiteInBackoff(string(site.Weibo))
				_, _ = b.subs.IsSiteInBackoff(string(site.X))
				_ = b.SchedulerStatuses()
				if i%10 == 0 && b.events != nil {
					b.events.PublishSite(EventLibraryChanged, string(site.Weibo), fmt.Sprintf("synth:%d", i))
				}
			}
		}()

		wg.Wait()
	}()

	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("deadlock or stall detected in concurrent backoff-clear / download / queue / subscriber stress test")
	}

	if callbackHits.Load() == 0 {
		t.Fatal("expected event callbacks during concurrent stress test")
	}
	statuses := b.SchedulerStatuses()
	if strings.TrimSpace(statuses[string(site.Weibo)].Site) == "" {
		t.Fatalf("expected valid site scheduler statuses after stress test, got %+v", statuses)
	}
}
