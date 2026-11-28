package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/site/douyin"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/transfer"
)

func seedBiliLogin(t *testing.T, b *Backend) {
	t.Helper()
	if _, err := b.cred.SaveManual("SESSDATA=s; bili_jct=j; DedeUserID=1", ""); err != nil {
		t.Fatal(err)
	}
}

func seedDouyinLogin(t *testing.T, b *Backend) {
	t.Helper()
	if _, err := b.douyinCred.SaveDouyinManual("sessionid=s; ttwid=t"); err != nil {
		t.Fatal(err)
	}
}

func TestExternalURLValidationRejectsUnsafeSchemes(t *testing.T) {
	desk := NewMockDesktop()
	rejected := []string{
		"",
		"   ",
		"file:///C:/Windows/System32/cmd.exe",
		`\\evil-host\share\payload.exe`,
		"javascript:alert(1)",
		"ms-msdt:/id",
		"https://user:pass@example.com/path",
		"https://example.com/has space",
		"https://example.com/line\nbreak",
		`https://example.com/back\slash`,
		"http://:8080/path",
		"http:///missing-host",
	}
	for _, raw := range rejected {
		if err := desk.OpenExternalURL(raw); err == nil {
			t.Errorf("OpenExternalURL(%q) succeeded, want rejection", raw)
		}
	}
	allowed := []string{
		"https://live.bilibili.com/12345",
		"https://live.douyin.com/998877",
		"https://example.com/docs?a=1&copy=2#sec-1",
		"http://127.0.0.1:8080/ok",
	}
	for _, raw := range allowed {
		if err := desk.OpenExternalURL(raw); err != nil {
			t.Errorf("OpenExternalURL(%q) failed: %v", raw, err)
		}
	}
	if len(desk.Actions()) != len(allowed) {
		t.Fatalf("expected %d recorded external URL actions, got %d", len(allowed), len(desk.Actions()))
	}
}

func TestAvatarFetchRespectsPerSiteAuthGate(t *testing.T) {
	var hits atomic.Int32
	pngBytes := historyThumbnailPNG(t, 16, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngBytes)
	}))
	defer srv.Close()

	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		BilibiliOpts:     bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1},
		DouyinOpts:       douyin.ClientOptions{AllowLocalhost: true, MinInterval: -1, JitterMax: -1},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Only Bilibili is logged in; Douyin is logged out.
	seedBiliLogin(t, b)
	if _, err := b.CreateSubscription("dy-creator", "", []subscription.AccountRef{
		{Site: "douyin", UID: "sec_dy_1", Name: "DY", Face: srv.URL + "/avatar.png"},
	}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}
	b.rememberAccountAvatar("douyin", "sec_dy_1", srv.URL+"/avatar.png")
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("expected 0 avatar requests for logged-out Douyin site, got %d", got)
	}
}

func TestEventPublishDoesNotHoldServiceOrQueueLocks(t *testing.T) {
	clk := newFakeQueueClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clk)
	defer b.Close()

	var settingsEvents atomic.Int32
	var queueEvents atomic.Int32
	var lockHeldDuringSettings atomic.Bool

	unsub := b.SubscribeEvents(func(ev Event) {
		switch ev.Type {
		case EventSettingsChanged:
			settingsEvents.Add(1)
			if !b.settingsMu.TryLock() {
				lockHeldDuringSettings.Store(true)
			} else {
				b.settingsMu.Unlock()
			}
			_ = b.GetSettings()
		case EventCacheQueueUpdated:
			queueEvents.Add(1)
			// Re-entering queue APIs synchronously inside the event callback must not deadlock.
			_ = b.ListCacheQueue()
			_, _ = b.FindCacheQueueTask("bilibili", "1001", "videos")
		}
	})
	defer unsub()

	done := make(chan struct{})
	go func() {
		defer close(done)
		cur := b.GetSettings()
		cur.Download.MaxConcurrentTasks = 2
		if _, err := b.UpdateSettings(cur); err != nil {
			t.Errorf("UpdateSettings: %v", err)
			return
		}
		pager := func(context.Context, string) ([]queueCandidate, string, bool, error) {
			return nil, "", false, nil
		}
		task, err := b.enqueueCacheQueueWithPager(site.Bilibili, "videos", "1001", CacheQueueRequest{
			Site: "bilibili", List: "videos", User: "1001", UserName: "UP",
		}, []queueCandidate{{key: "bilibili:video:BV1TEST0001", uncacheable: true}}, pager)
		if err != nil {
			t.Errorf("enqueue: %v", err)
			return
		}
		if _, err := b.PauseCacheQueue(task.ID); err != nil {
			t.Errorf("PauseCacheQueue: %v", err)
			return
		}
		if _, err := b.ResumeCacheQueue(task.ID); err != nil {
			t.Errorf("ResumeCacheQueue: %v", err)
			return
		}
		if _, err := b.CancelCacheQueue(task.ID); err != nil {
			t.Errorf("CancelCacheQueue: %v", err)
			return
		}
		if err := b.RemoveCacheQueue(task.ID); err != nil {
			t.Errorf("RemoveCacheQueue: %v", err)
			return
		}
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock detected while publishing settings or cache-queue events")
	}
	if lockHeldDuringSettings.Load() {
		t.Fatal("settingsMu was still held while publishing EventSettingsChanged")
	}
	if settingsEvents.Load() == 0 || queueEvents.Load() < 5 {
		t.Fatalf("expected events observed, got settings=%d queue=%d", settingsEvents.Load(), queueEvents.Load())
	}
}

func TestDouyinUncertainEmptyTriggersBackoffAndEmptyHasMoreAdvances(t *testing.T) {
	// Part 1: Douyin UncertainEmpty from page 1 must return ErrKindRiskControl -> waiting_backoff, NOT completed.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status_code":0,"aweme_list":[],"has_more":0,"max_cursor":0}`))
	}))
	defer srv.Close()

	clk := newFakeQueueClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	b, err := NewBackend(Options{
		Locations: testLocations(t),
		DouyinOpts: douyin.ClientOptions{
			AllowLocalhost: true,
			MinInterval:    -1,
			JitterMax:      -1,
			Endpoints: douyin.Endpoints{
				AwemePost: srv.URL + "/aweme/post",
			},
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
		CacheQueueClock:  clk,
		CacheQueueRand:   func(int) int { return 0 },
		CacheQueuePacing: &CacheQueuePacing{
			ItemMinDelay:        time.Second,
			ItemMaxDelay:        time.Second,
			LongRestMinItems:    50,
			LongRestMaxItems:    50,
			LongRestMinDelay:    time.Second,
			LongRestMaxDelay:    time.Second,
			BackoffCoolMinDelay: time.Minute,
			BackoffCoolMaxDelay: time.Minute,
			StartupMinDelay:     time.Second,
			StartupMaxDelay:     time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedDouyinLogin(t, b)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.queue.Start(ctx)

	task, err := b.EnqueueCacheQueue(ctx, CacheQueueRequest{
		Site: "douyin", List: "works", User: "MS4wLjABAAAA_synth_uncertain_01", UserName: "Creator",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := waitQueuePredicate(t, b, task.ID, "waiting_backoff on UncertainEmpty", func(q CacheQueueTask) bool {
		return q.Status == CacheQueueWaitingBackoff || q.Status == CacheQueueCompleted
	})
	if got.Status != CacheQueueWaitingBackoff {
		t.Fatalf("expected UncertainEmpty page to trigger waiting_backoff, got %q (%s)", got.Status, got.StopReason)
	}

	// Part 2: Non-empty next page behind an empty filtered page with HasMore=true must advance cursor, not complete.
	var pageCalls atomic.Int32
	var cachedPage2 atomic.Bool
	pager := func(_ context.Context, cursor string) ([]queueCandidate, string, bool, error) {
		n := pageCalls.Add(1)
		if n == 1 && cursor == "" {
			return nil, "cursor_page_2", true, nil
		}
		if cursor == "cursor_page_2" {
			return []queueCandidate{{
				key:   "bilibili:video:BV1PAGE20001",
				title: "Page 2 video",
				enqueue: func(context.Context) (queueEnqueued, error) {
					cachedPage2.Store(true)
					return queueEnqueued{itemKey: "bilibili:video:BV1PAGE20001", title: "Page 2 video"}, nil
				},
			}}, "", false, nil
		}
		return nil, "", false, nil
	}
	seedBiliLogin(t, b)
	t2, err := b.enqueueCacheQueueWithPager(site.Bilibili, "videos", "2002", CacheQueueRequest{
		Site: "bilibili", List: "videos", User: "2002", UserName: "BiliUP",
	}, nil, pager)
	if err != nil {
		t.Fatal(err)
	}
	got2 := waitQueuePredicate(t, b, t2.ID, "complete after advancing empty page", func(q CacheQueueTask) bool {
		return q.Status == CacheQueueCompleted
	})
	if !cachedPage2.Load() || got2.CachedCount != 1 {
		t.Fatalf("expected queue to advance past empty page and cache page 2, got cached=%d pageCalls=%d", got2.CachedCount, pageCalls.Load())
	}
}

func TestDouyinLiveIncidentsPreservedAndStoppedPartNotProbed(t *testing.T) {
	itemDir := t.TempDir()
	partFile := filepath.Join(itemDir, "part-001.flv.part")
	if err := os.WriteFile(partFile, []byte("synthetic-flv-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	incident := library.LiveIncident{Kind: library.LiveIncidentReconnect, At: "2026-11-20T00:00:00Z", Count: 2}

	stopped := mapRoomToAccountState(live.RoomState{
		Site:          "douyin",
		AccountUID:    "sec_1",
		RoomIDStr:     "777",
		Recording:     false,
		RecordItemDir: itemDir,
		Incidents:     []library.LiveIncident{incident},
	})
	if len(stopped.Incidents) != 1 || stopped.Incidents[0].Count != 2 {
		t.Fatalf("expected Douyin incidents copied, got %+v", stopped.Incidents)
	}
	if stopped.RecordPartPath != "" {
		t.Fatalf("stopped room must not probe disk for .part, got %q", stopped.RecordPartPath)
	}

	recording := mapRoomToAccountState(live.RoomState{
		Site:          "douyin",
		AccountUID:    "sec_1",
		RoomIDStr:     "777",
		Recording:     true,
		RecordItemDir: itemDir,
		Incidents:     []library.LiveIncident{incident},
	})
	if recording.RecordPartPath != partFile {
		t.Fatalf("active recording expected RecordPartPath=%q, got %q", partFile, recording.RecordPartPath)
	}
}

func TestCacheQueueSurvivesAppClosedDownloadWithoutExitingLoop(t *testing.T) {
	clk := newFakeQueueClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clk)
	defer b.Close()

	now := clk.Now().UTC().Format(time.RFC3339)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.queue.Start(ctx)

	// First task enqueues a download that immediately becomes paused with app_closed.
	t1, err := b.enqueueCacheQueueWithPager(site.Bilibili, "videos", "3001", CacheQueueRequest{
		Site: "bilibili", List: "videos", User: "3001", UserName: "UP1",
	}, []queueCandidate{{
		key: "bilibili:video:BV1CLOSED01",
		enqueue: func(context.Context) (queueEnqueued, error) {
			if _, err := b.db.Exec(
				`INSERT INTO download_tasks (task_id, site, kind, target_id, item_key, status, pause_reason, created_at, updated_at)
				 VALUES ('dl-app-closed', 'bilibili', 'video', 'BV1CLOSED01', 'bilibili:video:BV1CLOSED01', 'paused', 'app_closed', ?, ?)`,
				now, now,
			); err != nil {
				return queueEnqueued{}, err
			}
			return queueEnqueued{taskIDs: []string{"dl-app-closed"}, itemKey: "bilibili:video:BV1CLOSED01", title: "Closed"}, nil
		},
	}}, func(context.Context, string) ([]queueCandidate, string, bool, error) { return nil, "", false, nil })
	if err != nil {
		t.Fatal(err)
	}
	got1 := waitQueuePredicate(t, b, t1.ID, "paused with app_closed", func(q CacheQueueTask) bool {
		return q.Status == CacheQueuePaused && q.PauseReason == "app_closed"
	})
	if got1.Status != CacheQueuePaused {
		t.Fatalf("t1 status = %q, want paused", got1.Status)
	}

	// Second task on the SAME site must still execute — proving runSiteLoop did not exit.
	t2, err := b.enqueueCacheQueueWithPager(site.Bilibili, "videos", "3002", CacheQueueRequest{
		Site: "bilibili", List: "videos", User: "3002", UserName: "UP2",
	}, []queueCandidate{{
		key: "bilibili:video:BV1NEXT0002",
		enqueue: func(context.Context) (queueEnqueued, error) {
			return queueEnqueued{itemKey: "bilibili:video:BV1NEXT0002", title: "Next"}, nil
		},
	}}, func(context.Context, string) ([]queueCandidate, string, bool, error) { return nil, "", false, nil })
	if err != nil {
		t.Fatal(err)
	}
	got2 := waitQueuePredicate(t, b, t2.ID, "completed while loop stayed alive", func(q CacheQueueTask) bool {
		return q.Status == CacheQueueCompleted
	})
	if got2.CachedCount != 1 {
		t.Fatalf("expected second queue task to complete with CachedCount=1, got %+v", got2)
	}
}

func TestMultiPartVideoPersistsPendingPagesAcrossRestart(t *testing.T) {
	loc := testLocations(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"isLogin": true,
				"mid":     1,
				"uname":   "bili",
				"wbi_img": map[string]any{
					"img_url": "https://i0.hdslb.com/bfs/wbi/7cd084941338484aae1ad9425b84077c.png",
					"sub_url": "https://i0.hdslb.com/bfs/wbi/4932caff0ff746eab6f01bf08b70ac45.png",
				},
			},
		})
	})
	viewHandler := func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid":  "BV1MULTI0001",
				"aid":   1001,
				"title": "Two Part Video",
				"owner": map[string]any{"mid": 77, "name": "UP77"},
				"pages": []map[string]any{
					{"cid": 101, "page": 1, "part": "P1", "duration": 10},
					{"cid": 102, "page": 2, "part": "P2", "duration": 10},
				},
			},
		})
	}
	mux.HandleFunc("/x/web-interface/view", viewHandler)
	mux.HandleFunc("/x/web-interface/wbi/view", viewHandler)
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"code": -404, "message": "not used"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	clk := newFakeQueueClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	pacing := &CacheQueuePacing{
		ItemMinDelay:        time.Second,
		ItemMaxDelay:        time.Second,
		LongRestMinItems:    50,
		LongRestMaxItems:    50,
		LongRestMinDelay:    time.Second,
		LongRestMaxDelay:    time.Second,
		BackoffCoolMinDelay: time.Minute,
		BackoffCoolMaxDelay: time.Minute,
		StartupMinDelay:     time.Second,
		StartupMaxDelay:     time.Second,
	}
	openB := func() *Backend {
		b, err := NewBackend(Options{
			Locations: loc,
			BilibiliOpts: bilibili.ClientOptions{
				AllowLocalhost: true,
				PacingInterval: -1,
				Endpoints: bilibili.Endpoints{
					APIBase: srv.URL,
					WWWBase: srv.URL,
				},
			},
			Desktop:          NewMockDesktop(),
			DisableBgWorkers: true,
			CacheQueueClock:  clk,
			CacheQueueRand:   func(int) int { return 0 },
			CacheQueuePacing: pacing,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	b1 := openB()
	seedBiliLogin(t, b1)
	// Seed a download task representing P1 completed, and persist a queue record with ActivePendingPages=[2].
	now := clk.Now().UTC().Format(time.RFC3339)
	if _, err := b1.db.Exec(
		`INSERT INTO download_tasks (task_id, site, kind, target_id, item_key, status, created_at, updated_at)
		 VALUES ('dl-p1', 'bilibili', 'video', 'BV1MULTI0001', 'bilibili:video:BV1MULTI0001', 'completed', ?, ?)`,
		now, now,
	); err != nil {
		t.Fatal(err)
	}

	b1.queue.mu.Lock()
	b1.queue.tasks = append(b1.queue.tasks, &cacheQueueEntry{
		rec: cacheQueueRecord{
			ID:                 "cq_multipart_restart",
			Site:               "bilibili",
			User:               "77",
			UserName:           "UP77",
			List:               "videos",
			HasMore:            false,
			ActiveDownloadIDs:  []string{"dl-p1"},
			ActivePendingPages: []int{2},
			ActiveItemKey:      "bilibili:video:BV1MULTI0001",
			ActiveTitle:        "Two Part Video",
			Status:             CacheQueueRunning,
			CreatedAt:          now,
			UpdatedAt:          now,
		},
		rt: cacheQueueRuntime{seenKeys: map[string]bool{}},
	})
	if err := b1.queue.saveLocked(); err != nil {
		t.Fatal(err)
	}
	b1.queue.mu.Unlock()
	if err := b1.Close(); err != nil {
		t.Fatal(err)
	}

	// Verify activePendingPages survived serialization to cache-queue.json.
	raw, err := os.ReadFile(filepath.Join(loc.DataDir, cacheQueueFileName))
	if err != nil {
		t.Fatal(err)
	}
	var persisted cacheQueueFile
	if err := json.Unmarshal(raw, &persisted); err != nil || len(persisted.Tasks) != 1 || len(persisted.Tasks[0].ActivePendingPages) != 1 || persisted.Tasks[0].ActivePendingPages[0] != 2 {
		t.Fatalf("expected activePendingPages=[2] persisted in cache-queue.json, got err=%v tasks=%+v", err, persisted.Tasks)
	}

	// Restart into a brand-new Backend (in-memory nextPartEnqueuers is empty).
	b2 := openB()
	defer b2.Close()

	b2.queue.mu.Lock()
	if len(b2.queue.tasks) != 1 || len(b2.queue.tasks[0].rec.ActivePendingPages) != 1 || b2.queue.tasks[0].rec.ActivePendingPages[0] != 2 {
		t.Fatalf("expected ActivePendingPages=[2] after restart, got %+v", b2.queue.tasks)
	}
	b2.queue.mu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b2.queue.Start(ctx)
	clk.WaitForPendingTimer(t, 2*time.Second)
	clk.Advance(2 * time.Second)

	// Wait until b2 processes P1 completion and enqueues P2 (creating a new download task for BV1MULTI0001).
	deadline := time.Now().Add(3 * time.Second)
	var sawP2Task bool
	for time.Now().Before(deadline) {
		tasks, _ := b2.ListDownloadTasks()
		for _, dt := range tasks {
			if dt.TaskID != "dl-p1" && dt.TargetID == "BV1MULTI0001" {
				sawP2Task = true
				break
			}
		}
		if sawP2Task {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if !sawP2Task {
		t.Fatal("expected restarted queue worker to reconstruct and enqueue page 2")
	}
}

func TestRemoteProfileIsolationAndAllowMovedFiles(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Profile:          ProfileRemote,
		BilibiliOpts:     bilibili.ClientOptions{AllowLocalhost: true, PacingInterval: -1},
		DouyinOpts:       douyin.ClientOptions{AllowLocalhost: true, MinInterval: -1, JitterMax: -1, Endpoints: douyin.Endpoints{ProfileOther: srv.URL + "/profile"}},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if !b.LibraryStore().AllowMovedFiles() {
		t.Fatal("expected ProfileRemote to enable libStore.AllowMovedFiles()")
	}
	b.queue.mu.Lock()
	queueStarted := b.queue.started
	b.queue.mu.Unlock()
	if queueStarted {
		t.Fatal("ProfileRemote must never start cacheQueueManager background workers")
	}

	seedDouyinLogin(t, b)
	if _, err := b.subs.Create("creator", "", []subscription.AccountRef{{Site: "douyin", UID: "sec_fc"}}, subscription.SubRules{FullCache: true}); err != nil {
		t.Fatal(err)
	}
	if ids := b.startDueFullCacheSweeps(context.Background(), time.Now()); len(ids) != 0 {
		t.Fatalf("ProfileRemote startDueFullCacheSweeps started %v, want nil", ids)
	}
	if hits.Load() != 0 {
		t.Fatalf("ProfileRemote full cache sweep sent %d network requests, want 0", hits.Load())
	}
}

func TestPauseAllActiveWorkPreservesAuthenticatedSite(t *testing.T) {
	clk := newFakeQueueClock(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC))
	b := newBatchTestBackend(t, clk)
	defer b.Close()
	seedDouyinLogin(t, b)

	now := clk.Now().UTC().Format(time.RFC3339)
	for _, row := range []struct{ id, site, target string }{
		{"dl-bili", "bilibili", "BV1KEEP0001"},
		{"dl-dy", "douyin", "735000001"},
	} {
		if _, err := b.db.Exec(
			`INSERT INTO download_tasks (task_id, site, kind, target_id, status, created_at, updated_at)
			 VALUES (?, ?, 'video', ?, 'queued', ?, ?)`,
			row.id, row.site, row.target, now, now,
		); err != nil {
			t.Fatal(err)
		}
	}

	pager := func(context.Context, string) ([]queueCandidate, string, bool, error) { return nil, "", false, nil }
	qBili, err := b.enqueueCacheQueueWithPager(site.Bilibili, "videos", "100", CacheQueueRequest{
		Site: "bilibili", List: "videos", User: "100",
	}, nil, pager)
	if err != nil {
		t.Fatal(err)
	}
	qDy, err := b.enqueueCacheQueueWithPager(site.Douyin, "works", "sec_100", CacheQueueRequest{
		Site: "douyin", List: "works", User: "sec_100",
	}, nil, pager)
	if err != nil {
		t.Fatal(err)
	}

	// Expire ONLY Douyin; Bilibili remains logged in.
	if _, err := b.douyinCred.MarkState("expired", "expired"); err != nil {
		t.Fatal(err)
	}
	b.PauseAllActiveWork()

	biliDL, ok, err := b.TaskDetail("dl-bili")
	if err != nil || !ok || biliDL.Task.Status != "queued" {
		t.Fatalf("expected Bilibili download untouched (queued), got %+v ok=%v err=%v", biliDL.Task, ok, err)
	}
	dyDL, ok, err := b.TaskDetail("dl-dy")
	if err != nil || !ok || dyDL.Task.Status != "paused" || dyDL.Task.PauseReason != "auth_required" {
		t.Fatalf("expected Douyin download paused (auth_required), got %+v ok=%v err=%v", dyDL.Task, ok, err)
	}
	qList := b.ListCacheQueue()
	for _, q := range qList {
		if q.ID == qBili.ID && q.Status != CacheQueueQueued {
			t.Fatalf("expected Bilibili cache queue untouched (queued), got %+v", q)
		}
		if q.ID == qDy.ID && (q.Status != CacheQueuePaused || q.PauseReason != "auth_required") {
			t.Fatalf("expected Douyin cache queue paused (auth_required), got %+v", q)
		}
	}
}

func TestRemoteSyncStateConcurrencyAndServerStatusWhenLoggedOut(t *testing.T) {
	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Profile:          ProfileRemote,
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	if _, err := b.subs.Create("CreatorA", "", []subscription.AccountRef{{Site: "bilibili", UID: "42", Name: "UP42"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}
	// Neither site is logged in: ServerStatus must STILL populate Checks from local subscriptions.
	st := b.ServerStatus()
	if len(st.Checks) != 1 || st.Checks[0].UID != "42" {
		t.Fatalf("expected ServerStatus().Checks populated while logged out, got %+v", st.Checks)
	}

	// Concurrent syncState updates must not clobber each other's fields.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = b.mutateSyncState(func(s *syncState) {
				s.Hash = fmt.Sprintf("hash-%d", i)
			})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			_, _ = b.mutateSyncState(func(s *syncState) {
				if s.CookieSyncedAt == nil {
					s.CookieSyncedAt = map[string]string{}
				}
				s.CookieSyncedAt["bilibili"] = fmt.Sprintf("2026-11-20T00:00:%02dZ", i)
			})
		}
	}()
	wg.Wait()

	final := b.loadSyncState()
	if final.Hash != "hash-19" || final.CookieSyncedAt["bilibili"] != "2026-11-20T00:00:19Z" {
		t.Fatalf("lost concurrent syncState update: %+v", final)
	}
}

func TestConnectRemotePreservesOldTokenOnFailureAndCloseClosesClient(t *testing.T) {
	const goodToken = "SYNTH_GOOD_TOKEN_AAA_1234567890_EXTRA"
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+goodToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(remote.Status{Version: "0.1.0", Protocol: remote.ProtocolVersion})
	}))
	defer srv.Close()
	fp := remote.Fingerprint(srv.Certificate().Raw)

	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx := context.Background()
	if _, err := b.ConnectRemote(ctx, RemoteConnectInput{URL: srv.URL, Token: goodToken, Fingerprint: fp}); err != nil {
		t.Fatalf("initial ConnectRemote: %v", err)
	}
	// Attempting to connect to a broken address must fail AND leave the previous connection/token intact.
	if _, err := b.ConnectRemote(ctx, RemoteConnectInput{URL: "https://127.0.0.1:1", Token: "SYNTH_BAD_TOKEN_1234567890_EXTRA_99", Fingerprint: fp}); err == nil {
		t.Fatal("expected ConnectRemote to invalid address to fail")
	}
	if _, err := b.RemoteStatus(ctx); err != nil {
		t.Fatalf("previous remote connection broken after failed ConnectRemote: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	b.remote.mu.Lock()
	c := b.remote.client
	b.remote.mu.Unlock()
	if c != nil {
		t.Fatal("expected Backend.Close to close and clear remote client")
	}
}

func TestLogRedactionCoversRemoteTokensCSRFAndQRCodeKey(t *testing.T) {
	b := &Backend{
		logPath: filepath.Join(t.TempDir(), "trace.log"),
		remote:  &remoteLink{token: "SYNTH_ACTIVE_RAW_REMOTE_TOKEN_999"},
	}
	sentinels := []struct{ line, secret string }{
		{"api err csrf=SYNTH_CSRF_SECRET_1", "SYNTH_CSRF_SECRET_1"},
		{"qr poll qrcode_key=SYNTH_QR_SECRET_2", "SYNTH_QR_SECRET_2"},
		{"auth token=SYNTH_TOK_SECRET_3", "SYNTH_TOK_SECRET_3"},
		{"header Bearer SYNTH_BEARER_SECRET_4", "SYNTH_BEARER_SECRET_4"},
		{"remote trace-remote:1:SYNTH_REMOTE_SECRET_5 failed", "SYNTH_REMOTE_SECRET_5"},
		{"raw leak SYNTH_ACTIVE_RAW_REMOTE_TOKEN_999 here", "SYNTH_ACTIVE_RAW_REMOTE_TOKEN_999"},
	}
	for _, tc := range sentinels {
		b.logInfo(tc.line)
	}
	raw, err := os.ReadFile(b.logPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, tc := range sentinels {
		if strings.Contains(text, tc.secret) {
			t.Fatalf("trace.log leaked secret %q from line %q", tc.secret, tc.line)
		}
	}
	lines, err := b.RecentLogs(500)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(lines, "\n")
	for _, tc := range sentinels {
		if strings.Contains(joined, tc.secret) {
			t.Fatalf("RecentLogs leaked secret %q", tc.secret)
		}
	}
}

func TestTransferJobsConcurrencyCleanupAndCloseSafety(t *testing.T) {
	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	srcDir := t.TempDir()
	job, err := b.StartImportScan(srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Status != TransferScanning {
		t.Fatalf("unexpected initial scan job: %+v", job)
	}

	// Poll concurrently while scan finishes to exercise transfer mutex & cloning.
	deadline := time.Now().Add(3 * time.Second)
	var ready bool
	for time.Now().Before(deadline) {
		for _, j := range b.TransferJobs() {
			if j.ID == job.ID && j.Status == TransferReady {
				ready = true
				break
			}
		}
		if ready {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !ready {
		t.Fatal("scan job did not reach TransferReady")
	}

	runJob, err := b.StartImport(job.ID, transfer.Choices{})
	if err != nil {
		t.Fatal(err)
	}
	if runJob.FinishedAt != "" {
		t.Fatalf("StartImport must clear previous scan FinishedAt, got %q", runJob.FinishedAt)
	}

	deadline = time.Now().Add(3 * time.Second)
	var done bool
	for time.Now().Before(deadline) {
		for _, j := range b.TransferJobs() {
			if j.ID == job.ID && j.Status == TransferDone {
				done = true
				break
			}
		}
		if done {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !done {
		t.Fatal("import job did not finish")
	}
	if _, ok := b.ImportPlan(job.ID); ok {
		t.Fatal("expected scanned plan to be cleared after import finished")
	}

	// Closing backend must safely reject new background transfer jobs without panicking on WaitGroup.
	if err := b.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartImportScan(srcDir); err == nil {
		t.Fatal("expected StartImportScan after Close to return an error")
	}
}

func TestLiveStatusesAndStartRecordingFilterRequestedBilibiliUID(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		// Return room for uid 100 ONLY; uid 200 has no live room.
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"100": map[string]any{
					"uid": 100, "room_id": 1001, "live_status": 1, "uname": "Streamer100", "title": "Live100",
				},
			},
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	b, err := NewBackend(Options{
		Locations: testLocations(t),
		BilibiliOpts: bilibili.ClientOptions{
			AllowLocalhost: true,
			PacingInterval: -1,
			Endpoints: bilibili.Endpoints{
				LiveAPIBase: srv.URL,
			},
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedBiliLogin(t, b)
	ctx := context.Background()
	// Warm cache with uid 100 (room 1001).
	if _, err := b.LiveStatus(ctx, []int64{100}); err != nil {
		t.Fatal(err)
	}
	// Requesting status for uid 200 must NOT return uid 100.
	got, err := b.LiveStatuses(ctx, []string{"bilibili:200"})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range got {
		if st.UID != "200" {
			t.Fatalf("LiveStatuses(bilibili:200) leaked unrelated room: %+v", st)
		}
	}
	// Starting recording for uid 200 (no room) must fail with ErrKindUnavailable, NOT hijack room 1001.
	_, err = b.StartLiveRecordingByAccount(ctx, "bilibili:200")
	if !site.IsKind(err, site.ErrKindUnavailable) {
		t.Fatalf("StartLiveRecordingByAccount(bilibili:200) err = %v, want ErrKindUnavailable", err)
	}
}

func TestAccountReplacementAndAuthExpiryDoNotDeadlockWithActiveLiveRecording(t *testing.T) {
	// Custom fake Bilibili server whose /nav echoes DedeUserID from Cookie so we can switch between two accounts.
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/nav", func(w http.ResponseWriter, r *http.Request) {
		mid := int64(10001)
		uname := "AcctA"
		if strings.Contains(r.Header.Get("Cookie"), "DedeUserID=10002") {
			mid = 10002
			uname = "AcctB"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"isLogin": true, "mid": mid, "uname": uname},
		})
	})
	var srvURL string
	mux.HandleFunc("/room/v1/Room/room_init", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 223344, "short_id": 223, "uid": 8888, "live_status": 1, "live_time": 1710000000},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_status_info_by_uids", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"8888": map[string]any{"uid": 8888, "room_id": 223344, "uname": "Streamer", "title": "LiveRoom", "live_status": 1, "live_time": 1710000000},
			},
		})
	})
	mux.HandleFunc("/room/v1/Room/get_info", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{"room_id": 223344, "uid": 8888, "live_status": 1, "title": "LiveRoom"},
		})
	})
	mux.HandleFunc("/xlive/web-room/v2/index/getRoomPlayInfo", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"room_id":     223344,
				"uid":         8888,
				"live_status": 1,
				"playurl_info": map[string]any{
					"playurl": map[string]any{
						"g_qn_desc": []map[string]any{{"qn": 10000, "desc": "原画"}},
						"stream": []map[string]any{{
							"protocol_name": "http_hls",
							"format": []map[string]any{{
								"format_name": "fmp4",
								"codec": []map[string]any{{
									"codec_name": "avc",
									"current_qn": 10000,
									"accept_qn":  []int{10000},
									"base_url":   "/live/bili.m3u8",
									"url_info":   []map[string]any{{"host": srvURL, "extra": ""}},
								}},
							}},
						}},
					},
				},
			},
		})
	})
	mux.HandleFunc("/live/bili.m3u8", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
	})
	srv := httptest.NewServer(mux)
	srvURL = srv.URL
	defer srv.Close()

	b, err := NewBackend(Options{
		Locations: testLocations(t),
		BilibiliOpts: bilibili.ClientOptions{
			AllowLocalhost: true,
			PacingInterval: -1,
			Endpoints: bilibili.Endpoints{
				APIBase:     srv.URL,
				LiveAPIBase: srv.URL,
				LiveWebBase: srv.URL,
			},
		},
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	ctx := context.Background()
	if _, err := b.ImportCookie(ctx, "SESSDATA=SYNTH_SESS_A; bili_jct=SYNTH_JCT_A; DedeUserID=10001", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.StartLiveRecording(ctx, 223344); err != nil {
		t.Fatal(err)
	}

	// Replace with another Bilibili account while recording is active: SetAccountReplacingHandler
	// runs while bilibili.Client holds sequenceMu.Lock() and must not deadlock waiting on StopForSite.
	done := make(chan error, 1)
	go func() {
		_, err := b.ImportCookie(ctx, "SESSDATA=SYNTH_SESS_B; bili_jct=SYNTH_JCT_B; DedeUserID=10002", "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ImportCookie account replacement failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ImportCookie deadlocked with active live recording")
	}

	// Wait briefly for the async live stop to finish.
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, recs := b.ActiveWork()
		if recs == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expected active recording stopped after account replacement, still %d", recs)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPrunePreviewDirEvictsOldestFilesAndKeepsLatest(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	var paths []string
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, fmt.Sprintf("preview_%02d.mp4", i))
		if err := os.WriteFile(p, make([]byte, 100), 0o600); err != nil {
			t.Fatal(err)
		}
		mt := base.Add(time.Duration(i) * time.Minute)
		_ = os.Chtimes(p, mt, mt)
		paths = append(paths, p)
	}
	prunePreviewDir(dir, paths[5], 350, 3)

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 preview files remaining, got %d", len(entries))
	}
	if _, err := os.Stat(paths[5]); err != nil {
		t.Fatalf("newest keepPath was removed: %v", err)
	}
	if _, err := os.Stat(paths[0]); !os.IsNotExist(err) {
		t.Fatalf("oldest preview file was not pruned")
	}
}

func TestCheckAccountNormalizesSiteID(t *testing.T) {
	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	seedBiliLogin(t, b)

	_, err = b.CheckAccount(context.Background(), "sub-1", "  DOUYIN ", "sec_1")
	assertSiteAuthRequired(t, err, site.Douyin)
}

func TestMigrateCacheDirRejectsWhenLiveRecordingActive(t *testing.T) {
	h := newDualSiteTestHarness(t)
	ctx := context.Background()
	if _, err := h.backend.ImportCookie(ctx, "SESSDATA=SYNTH_SESS_A; bili_jct=SYNTH_JCT_A; DedeUserID=778899", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := h.backend.StartLiveRecording(ctx, 223344); err != nil {
		t.Fatal(err)
	}
	_, err := h.backend.MigrateCacheDir(filepath.Join(t.TempDir(), "new-cache"), true)
	if !site.IsKind(err, site.ErrKindBadRequest) {
		t.Fatalf("expected ErrKindBadRequest migrating cache during active live recording, got %v", err)
	}
}

func TestDiagnosticsReportsMultiSiteBackoff(t *testing.T) {
	b, err := NewBackend(Options{
		Locations:        testLocations(t),
		Desktop:          NewMockDesktop(),
		DisableBgWorkers: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	b.subs.RecordSiteAutomaticStop(string(site.Douyin), site.ErrKindRiskControl)
	diag := b.DiagnosticsSnapshot()
	if !strings.Contains(diag.SubscriptionCheckStatus, "风控退避中") || !strings.Contains(diag.SubscriptionCheckStatus, "抖音") {
		t.Fatalf("expected DiagnosticsSnapshot to surface Douyin backoff, got %q", diag.SubscriptionCheckStatus)
	}
}
