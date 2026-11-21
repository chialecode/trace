package download

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/site/bilibili"
)

// Live-photo motion clips must stay under a temporary *.part name until
// EnsureSingleMP4Context succeeds; a broken clip must never appear as final
// images/01_live.mp4 on disk, and a valid trailing-moov clip is relocated
// before atomic rename.
func TestLivePhotoMotionStagedBeforeFinalRename(t *testing.T) {
	validTrailingMoov := buildProgressiveWithoutElst(t, true, false, 0)
	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("j"), 256)...)

	var serveBroken atomic.Bool
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/polymer/web-dynamic/v1/detail", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"item": map[string]any{
					"id_str": id,
					"type":   "DYNAMIC_TYPE_DRAW",
					"modules": map[string]any{
						"module_author": map[string]any{"mid": 777, "name": "LivePhotoAuthor", "pub_ts": 1700000001},
						"module_dynamic": map[string]any{
							"desc": map[string]any{"text": "实况动态测试"},
							"major": map[string]any{
								"type": "MAJOR_TYPE_OPUS",
								"opus": map[string]any{
									"title": "实况动态",
									"pics": []map[string]any{
										{"url": srvURL + "/media/pic.jpg", "live_url": srvURL + "/media/clip.mp4", "width": 16, "height": 16},
									},
								},
							},
						},
					},
				},
			},
		})
	})
	mux.HandleFunc("/media/pic.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
		_, _ = w.Write(jpegBytes)
	})
	mux.HandleFunc("/media/clip.mp4", func(w http.ResponseWriter, r *http.Request) {
		body := validTrailingMoov
		if serveBroken.Load() {
			body = []byte("not-a-valid-mp4-motion-clip")
		}
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = w.Write(body)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// 1. Broken motion clip must never materialize as final images/01_live.mp4.
	serveBroken.Store(true)
	checkNames := watchNames(t, lib.CacheDir())
	tBroken, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{DynamicID: "900000001"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tBroken.TaskID, "partial", 5*time.Second)
	checkNames()
	if meta, ok, err := lib.GetItem("dynamic:900000001"); err != nil || !ok {
		t.Fatalf("GetItem broken dynamic: ok=%v err=%v", ok, err)
	} else if _, statErr := os.Stat(filepath.Join(meta.ItemDir, "images", "01_live.mp4")); !os.IsNotExist(statErr) {
		t.Fatalf("broken motion clip left final file on disk: %v", statErr)
	}

	// 2. Valid trailing-moov motion clip is validated/relocated before final rename.
	serveBroken.Store(false)
	checkValidNames := watchNames(t, lib.CacheDir())
	tOK, err := mgr.EnqueueDynamic(ctx, DynamicEnqueueRequest{DynamicID: "900000002"})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tOK.TaskID, "completed", 5*time.Second)
	checkValidNames()
	meta, ok, err := lib.GetItem("dynamic:900000002")
	if err != nil || !ok || meta.Status != "complete" {
		t.Fatalf("expected complete live-photo dynamic, got ok=%v err=%v meta=%+v", ok, err, meta)
	}
	summary, err := InspectMP4File(filepath.Join(meta.ItemDir, "images", "01_live.mp4"))
	if err != nil || !summary.MoovBeforeMdat {
		t.Fatalf("expected relocated moov-before-mdat motion clip, got summary=%+v err=%v", summary, err)
	}
}

// When Douyin OriginalURLs exhausts its 5 network retries and falls back to
// VideoURLs, Task.Status must NOT be poisoned to "paused" in SQLite; the task
// must finish as "completed" and deliver the completion notification.
func TestOriginalURLsNetworkFailureDoesNotPoisonTaskStatus(t *testing.T) {
	videoBytes := buildProgressiveWithoutElst(t, true, false, 0)
	var origHits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/orig-500.mp4", func(w http.ResponseWriter, r *http.Request) {
		origHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("/cdn/transcoded.mp4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", strconv.Itoa(len(videoBytes)))
		_, _ = w.Write(videoBytes)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	var notifyMu sync.Mutex
	var statuses []string
	mgr.onTaskUpdate = func(tk Task) {
		notifyMu.Lock()
		statuses = append(statuses, tk.Status)
		notifyMu.Unlock()
	}

	const (
		secUID  = "MS4wLjABAAAA_orig_net_fallback_0001"
		awemeID = "7399000000000000101"
	)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)
	planner := newFakeDouyinWorkPlanner()
	planner.setPlan(awemeID, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site:         site.Douyin,
			Kind:         "video",
			TargetID:     awemeID,
			Title:        "原画网络失败回退测试",
			DurationSec:  2,
			AuthorUID:    secUID,
			AuthorName:   "测试创作者",
			OriginalURLs: []string{srv.URL + "/cdn/orig-500.mp4?sign=secret_orig"},
			VideoURLs:    []string{srv.URL + "/cdn/transcoded.mp4?sign=secret_vid"},
			VideoGear:    "normal_1080_0",
			QualityName:  "1080P",
			Codec:        "avc",
			Transport:    transport,
		}, nil
	})
	mgr.RegisterWorkPlanner(planner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	task, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: awemeID})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForTaskStatus(t, mgr, task.TaskID, "completed", 8*time.Second)
	if origHits.Load() < 5 {
		t.Fatalf("expected OriginalURLs to exhaust 5 retries, got %d hits", origHits.Load())
	}
	if done.PauseReason != "" || done.ErrorKind != "" {
		t.Fatalf("completed task kept stale pause/error fields: %+v", done)
	}
	meta, ok, err := lib.GetItem("douyin:video:" + awemeID)
	if err != nil || !ok || meta.Status != "complete" {
		t.Fatalf("expected complete library item, got ok=%v err=%v meta=%+v", ok, err, meta)
	}
	notifyMu.Lock()
	sawCompleted := false
	for _, st := range statuses {
		if st == "completed" {
			sawCompleted = true
		}
	}
	notifyMu.Unlock()
	if !sawCompleted {
		t.Fatalf("onTaskUpdate never received completed status: %v", statuses)
	}
}

// When a 403 media stream refreshes PlayURL and receives -101 (AuthRequired),
// downloadStreamWithRefresh must propagate ErrKindAuthRequired (not the stale
// 403), pause both the triggering task and other running/queued same-site
// tasks, and emit onTaskUpdate for the paused sibling tasks.
func TestDownloadStreamRefreshPropagatesAuthRequiredAndPausesConcurrentSiteTasks(t *testing.T) {
	var playHits atomic.Int32
	var srvURL string
	siblingStarted := make(chan struct{})
	siblingRelease := make(chan struct{})
	var siblingOnce sync.Once

	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": bvid, "aid": 200, "title": "Auth " + bvid,
				"owner": map[string]any{"mid": 200, "name": "UP"},
				"pages": []map[string]any{{"cid": 2001, "page": 1, "part": "P1", "duration": 1}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		bvid := r.URL.Query().Get("bvid")
		if bvid == "BV1authSib01" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"code": 0,
				"data": map[string]any{
					"quality": 80, "accept_format": "dash", "accept_quality": []int{80},
					"dash": map[string]any{
						"duration": 1,
						"video":    []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/sibling.m4s", "codecid": 7, "codecs": "avc1"}},
					},
				},
			})
			return
		}
		if playHits.Add(1) >= 2 {
			_ = json.NewEncoder(w).Encode(map[string]any{"code": -101, "message": "账号未登录"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality": 80, "accept_format": "dash", "accept_quality": []int{80},
				"dash": map[string]any{
					"duration": 1,
					"video":    []map[string]any{{"id": 80, "baseUrl": srvURL + "/media/expired.m4s?sign=secret_tok", "codecid": 7, "codecs": "avc1"}},
				},
			},
		})
	})
	mux.HandleFunc("/media/sibling.m4s", func(w http.ResponseWriter, r *http.Request) {
		siblingOnce.Do(func() { close(siblingStarted) })
		w.Header().Set("Content-Range", "bytes 0-1023/1024")
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("0123456789abcdef"))
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		select {
		case <-r.Context().Done():
		case <-siblingRelease:
		}
	})
	mux.HandleFunc("/media/expired.m4s", func(w http.ResponseWriter, r *http.Request) {
		// Wait until sibling worker is also actively running so we verify running-sibling cancellation.
		select {
		case <-siblingStarted:
		case <-time.After(3 * time.Second):
		}
		w.WriteHeader(http.StatusForbidden)
	})

	mgr, _, srv := setupDownloadEnv(t, mux, 2)
	srvURL = srv.URL
	cfg := mgr.dlSettings
	cfg.SaveCover, cfg.SaveDanmaku, cfg.SaveSubtitles = false, false, false
	mgr.UpdateSettings(cfg)

	var notifyMu sync.Mutex
	pausedEvents := make(map[string]string)
	mgr.onTaskUpdate = func(tk Task) {
		if tk.Status == "paused" {
			notifyMu.Lock()
			pausedEvents[tk.TaskID] = tk.PauseReason
			notifyMu.Unlock()
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer close(siblingRelease)
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	detail := func(bvid string) *bilibili.VideoDetail {
		return &bilibili.VideoDetail{
			BVID: bvid, Title: "Auth " + bvid,
			Author: bilibili.AuthorInfo{Mid: 200, Name: "UP"},
			Pages:  []bilibili.VideoPage{{CID: 2001, Page: 1, Title: "P1", Duration: 1}},
		}
	}
	t1, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1authSib01", Detail: detail("BV1authSib01")})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := mgr.EnqueueVideo(ctx, VideoEnqueueRequest{BVID: "BV1authExp02", Detail: detail("BV1authExp02")})
	if err != nil {
		t.Fatal(err)
	}

	done2 := waitForTaskStatus(t, mgr, t2[0].TaskID, "paused", 6*time.Second)
	if done2.PauseReason != "auth_required" || done2.ErrorKind != site.ErrKindAuthRequired {
		t.Fatalf("expected triggering task paused(auth_required), got %+v", done2)
	}
	done1 := waitForTaskStatus(t, mgr, t1[0].TaskID, "paused", 6*time.Second)
	if done1.PauseReason != "auth_required" {
		t.Fatalf("expected concurrent running sibling task paused(auth_required), got %+v", done1)
	}
	notifyMu.Lock()
	defer notifyMu.Unlock()
	if pausedEvents[t1[0].TaskID] != "auth_required" || pausedEvents[t2[0].TaskID] != "auth_required" {
		t.Fatalf("expected onTaskUpdate paused(auth_required) for both tasks, got %+v", pausedEvents)
	}
}

// Douyin note tasks must: (a) stop immediately on 412 risk control during cover
// download, (b) mark the item partial when cover fails with a non-stop error,
// and (c) share one PlanWorkDownload refresh across all expired pages in a note.
func TestDouyinNoteCoverErrorsNotSwallowedAndSharedPlanRefresh(t *testing.T) {
	jpgBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("j"), 128)...)
	var pageHitsAfterRisk atomic.Int32

	mux := http.NewServeMux()
	mux.HandleFunc("/cdn/cover-412.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	})
	mux.HandleFunc("/cdn/cover-404.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/cdn/risk-page.jpg", func(w http.ResponseWriter, r *http.Request) {
		pageHitsAfterRisk.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(jpgBytes)))
		_, _ = w.Write(jpgBytes)
	})
	mux.HandleFunc("/cdn/expired/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	mux.HandleFunc("/cdn/fresh/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(jpgBytes)))
		_, _ = w.Write(jpgBytes)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)
	const secUID = "MS4wLjABAAAA_note_cover_refresh_001"
	planner := newFakeDouyinWorkPlanner()

	// Case A: Cover 412 stops immediately without fetching page 1.
	const noteRisk = "7399000000000000201"
	planner.setPlan(noteRisk, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "dynamic", TargetID: noteRisk, Title: "封面风控图文",
			AuthorUID: secUID, AuthorName: "创作者",
			CoverURLs: []string{srv.URL + "/cdn/cover-412.jpg"},
			Pages:     []PlannedNotePage{{Index: 1, Kind: "image", ImageURLs: []string{srv.URL + "/cdn/risk-page.jpg"}}},
			Transport: transport,
		}, nil
	})

	// Case B: Cover 404 records missing cover -> item is partial (not falsely complete).
	const noteMissingCover = "7399000000000000202"
	planner.setPlan(noteMissingCover, func(_ int) (WorkDownloadPlan, error) {
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "dynamic", TargetID: noteMissingCover, Title: "封面缺失图文",
			AuthorUID: secUID, AuthorName: "创作者",
			CoverURLs: []string{srv.URL + "/cdn/cover-404.jpg"},
			Pages:     []PlannedNotePage{{Index: 1, Kind: "image", ImageURLs: []string{srv.URL + "/cdn/fresh/p1.jpg"}}},
			Transport: transport,
		}, nil
	})

	// Case C: 3-page note with expired URLs calls PlanWorkDownload at most once on refresh.
	const noteSharedRefresh = "7399000000000000203"
	planner.setPlan(noteSharedRefresh, func(call int) (WorkDownloadPlan, error) {
		prefix := "/cdn/expired/"
		if call >= 2 {
			prefix = "/cdn/fresh/"
		}
		return WorkDownloadPlan{
			Site: site.Douyin, Kind: "dynamic", TargetID: noteSharedRefresh, Title: "共享刷新三图",
			AuthorUID: secUID, AuthorName: "创作者",
			Pages: []PlannedNotePage{
				{Index: 1, Kind: "image", ImageURLs: []string{srv.URL + prefix + "1.jpg"}},
				{Index: 2, Kind: "image", ImageURLs: []string{srv.URL + prefix + "2.jpg"}},
				{Index: 3, Kind: "image", ImageURLs: []string{srv.URL + prefix + "3.jpg"}},
			},
			Transport: transport,
		}, nil
	})
	mgr.RegisterWorkPlanner(planner)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	// Run Case A.
	tRisk, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: noteRisk})
	if err != nil {
		t.Fatal(err)
	}
	stRisk := waitForTaskStatus(t, mgr, tRisk.TaskID, "paused", 5*time.Second)
	if stRisk.PauseReason != "risk_control" || pageHitsAfterRisk.Load() != 0 {
		t.Fatalf("cover 412 did not stop note immediately: status=%+v pageHits=%d", stRisk, pageHitsAfterRisk.Load())
	}

	// Run Case B.
	tCov, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: noteMissingCover})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tCov.TaskID, "partial", 5*time.Second)
	metaCov, ok, err := lib.GetItem("douyin:dynamic:" + noteMissingCover)
	if err != nil || !ok || metaCov.Status != "partial" {
		t.Fatalf("expected partial item when cover missing, got ok=%v err=%v meta=%+v", ok, err, metaCov)
	}
	hasCoverMissing := false
	for _, m := range metaCov.Missing {
		if m.Item == "cover" {
			hasCoverMissing = true
		}
	}
	if !hasCoverMissing {
		t.Fatalf("expected missing cover record, got %+v", metaCov.Missing)
	}

	// Run Case C.
	tRef, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: noteSharedRefresh})
	if err != nil {
		t.Fatal(err)
	}
	waitForTaskStatus(t, mgr, tRef.TaskID, "completed", 5*time.Second)
	if got := planner.callCount(noteSharedRefresh); got != 2 {
		t.Fatalf("expected 1 enqueue + 1 shared refresh (total 2 calls), got %d", got)
	}
}

// lockItem must honor context cancellation while waiting and evict keys once
// all waiters/holders release so the lock map never grows unbounded.
func TestLockItemIsContextCancellableAndEvictsUnusedKeys(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NewServeMux(), 1)

	unlock1, err := mgr.lockItem(context.Background(), "BV1lock0001")
	if err != nil {
		t.Fatal(err)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		_, lErr := mgr.lockItem(cancelCtx, "BV1lock0001")
		errCh <- lErr
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case gotErr := <-errCh:
		if !errors.Is(gotErr, context.Canceled) {
			t.Fatalf("expected context.Canceled, got %v", gotErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lockItem did not unblock on context cancellation")
	}

	unlock1()

	mgr.itemLockMu.Lock()
	remaining := len(mgr.itemLocks)
	mgr.itemLockMu.Unlock()
	if remaining != 0 {
		t.Fatalf("expected itemLocks map to be empty after unlock, got %d entries", remaining)
	}
}

// ensureTrackElst must select elst Version 1 for 64-bit values without 32-bit
// truncation and must never overwrite an existing non-trivial edit list.
func TestEnsureTrackElstVersion1AndPreservesExistingElst(t *testing.T) {
	moov := &mp4.MoovBox{Mvhd: &mp4.MvhdBox{Timescale: 1000}}
	trak := &mp4.TrakBox{
		Tkhd: &mp4.TkhdBox{TrackID: 1},
		Mdia: &mp4.MdiaBox{Mdhd: &mp4.MdhdBox{Timescale: 90000}},
	}
	trak.Children = []mp4.Box{trak.Tkhd, trak.Mdia}

	largeMediaTime := int64(math.MaxInt32) + 5000
	largeDur := uint64(math.MaxUint32) * 100
	if !ensureTrackElst(moov, trak, largeDur, largeMediaTime) {
		t.Fatal("expected ensureTrackElst to synthesize elst on track without one")
	}
	if trak.Edts == nil || len(trak.Edts.Elst) != 1 {
		t.Fatalf("expected 1 elst box, got %+v", trak.Edts)
	}
	elst := trak.Edts.Elst[0]
	if elst.Version != 1 {
		t.Fatalf("expected elst Version 1 for >32-bit values, got %d", elst.Version)
	}
	if elst.Entries[0].MediaTime != largeMediaTime {
		t.Fatalf("MediaTime = %d, want %d", elst.Entries[0].MediaTime, largeMediaTime)
	}
	if elst.Entries[0].SegmentDuration <= math.MaxUint32 {
		t.Fatalf("SegmentDuration truncated to %d", elst.Entries[0].SegmentDuration)
	}

	// Calling again on a track that already has an elst must NOT clobber MediaTime.
	if ensureTrackElst(moov, trak, 90000, 12345) {
		t.Fatal("expected ensureTrackElst to return false when elst already exists")
	}
	if elst.Entries[0].MediaTime != largeMediaTime {
		t.Fatalf("existing MediaTime was clobbered: got %d, want %d", elst.Entries[0].MediaTime, largeMediaTime)
	}
}

// cleanTaskPartFiles must delete owned temporary staging files (.trace-*.m4s,
// *.stage.part) even if they reached "completed" before cancel/failure, while
// never deleting completed final outputs.
func TestCleanTaskPartFilesRemovesCompletedTempStagingOnly(t *testing.T) {
	dir := t.TempDir()
	tempStream := filepath.Join(dir, ".trace-P01.video-80-7.m4s")
	finalMP4 := filepath.Join(dir, "P01.mp4")
	if err := os.WriteFile(tempStream, []byte("temp-m4s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalMP4, []byte("final-mp4"), 0o600); err != nil {
		t.Fatal(err)
	}

	cleanTaskPartFiles(Task{
		ItemDir: dir,
		Files: []TaskFileProgress{
			{Owned: true, Role: "video_stream", Path: ".trace-P01.video-80-7.m4s", Status: "completed"},
			{Owned: true, Role: "video", Path: "P01.mp4", Status: "completed"},
		},
	})

	if _, err := os.Stat(tempStream); !os.IsNotExist(err) {
		t.Fatalf("expected completed temp staging stream to be removed, stat err=%v", err)
	}
	if _, err := os.Stat(finalMP4); err != nil {
		t.Fatalf("completed final output should not be removed: %v", err)
	}
}

// PauseForSite and PauseAll must emit onTaskUpdate notifications for every
// affected task so service/UI listeners observe the state change.
func TestPauseForSiteAndPauseAllEmitTaskUpdates(t *testing.T) {
	mgr, _, _ := setupDownloadEnv(t, http.NewServeMux(), 1)
	var mu sync.Mutex
	seen := make(map[string]Task)
	mgr.onTaskUpdate = func(tk Task) {
		mu.Lock()
		seen[tk.TaskID] = tk
		mu.Unlock()
	}

	bTask, err := mgr.enqueueSingleTask(Task{Site: "bilibili", Kind: "video", TargetID: "BV1emit0001", Page: 1})
	if err != nil {
		t.Fatal(err)
	}
	dTask, err := mgr.enqueueSingleTask(Task{Site: "douyin", Kind: "video", TargetID: "7399000000000000301", Page: 1})
	if err != nil {
		t.Fatal(err)
	}

	mgr.PauseForSite(site.Bilibili, "auth_required", "已暂停")
	mu.Lock()
	bEvt, bOK := seen[bTask.TaskID]
	_, dOK := seen[dTask.TaskID]
	mu.Unlock()
	if !bOK || bEvt.Status != "paused" {
		t.Fatalf("expected PauseForSite to emit paused update for bilibili task, got ok=%v task=%+v", bOK, bEvt)
	}
	if dOK {
		t.Fatalf("PauseForSite(bilibili) should not emit pause for douyin task")
	}

	mgr.PauseAll("user", "全部暂停")
	mu.Lock()
	dEvt, dOK := seen[dTask.TaskID]
	mu.Unlock()
	if !dOK || dEvt.Status != "paused" {
		t.Fatalf("expected PauseAll to emit paused update for douyin task, got ok=%v task=%+v", dOK, dEvt)
	}
}

// Signed CDN query strings must be stripped from SiteError.Endpoint, and
// sniffAudioExtension must reject unknown binary/error payloads instead of
// blindly defaulting to .m4a.
func TestSanitizeMediaEndpointAndRejectUnknownAudio(t *testing.T) {
	rawURL := "https://cdn.example.com/media/clip.m4s?sign=super_secret_token&expires=1999999999"
	se := mapMediaHTTPStatus(site.Bilibili, http.StatusForbidden, rawURL)
	if strings.Contains(se.Endpoint, "super_secret_token") || strings.Contains(se.Endpoint, "?") {
		t.Fatalf("SiteError.Endpoint leaked signed query: %q", se.Endpoint)
	}
	if se.Endpoint != "https://cdn.example.com/media/clip.m4s" {
		t.Fatalf("unexpected sanitized endpoint: %q", se.Endpoint)
	}

	badPath := filepath.Join(t.TempDir(), "unknown.bin")
	if err := os.WriteFile(badPath, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, 0o600); err != nil {
		t.Fatal(err)
	}
	if ext, err := sniffAudioExtension(badPath, "application/octet-stream"); err == nil {
		t.Fatalf("expected unknown binary audio payload to be rejected, got ext=%q", ext)
	}
}

// In remote allow-moved-files mode, an already-completed cover moved away
// between multi-part downloads must not be re-downloaded on P2.
func TestMovedCoverNotRedownloadedWhenAllowMovedFilesEnabled(t *testing.T) {
	var coverHits atomic.Int32
	jpegBytes := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, bytes.Repeat([]byte("c"), 128)...)
	mux := http.NewServeMux()
	mux.HandleFunc("/media/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		coverHits.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Length", strconv.Itoa(len(jpegBytes)))
		_, _ = w.Write(jpegBytes)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	lib.SetAllowMovedFiles(true)

	itemDir := filepath.Join(lib.CacheDir(), "bilibili", "999", "videos", "BV1CovMv001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "video:BV1CovMv001",
		Status: "partial", Title: "Moved Cover", ItemDir: itemDir,
		Author: library.ItemAuthor{Mid: 999, Name: "UP"},
	}
	cfg := mgr.dlSettings
	cfg.SaveCover = true
	task := Task{TaskID: "t-cov", Site: "bilibili", Kind: "video", TargetID: "BV1CovMv001", ItemDir: itemDir, Status: "running"}

	// P1 downloads cover.jpg once.
	if err := mgr.saveVideoCover(context.Background(), &task, &meta, srv.URL+"/media/cover.jpg", cfg); err != nil {
		t.Fatalf("P1 saveVideoCover: %v", err)
	}
	if coverHits.Load() != 1 {
		t.Fatalf("coverHits after P1 = %d, want 1", coverHits.Load())
	}

	// Simulate remote rclone move removing cover.jpg from local disk.
	if err := os.Remove(filepath.Join(itemDir, "cover.jpg")); err != nil {
		t.Fatal(err)
	}

	// P2 must recognize cover.jpg was already completed and moved away.
	if err := mgr.saveVideoCover(context.Background(), &task, &meta, srv.URL+"/media/cover.jpg", cfg); err != nil {
		t.Fatalf("P2 saveVideoCover: %v", err)
	}
	if coverHits.Load() != 1 {
		t.Fatalf("moved cover was re-downloaded on P2: hits=%d, want 1", coverHits.Load())
	}
}

// Verify that oversized image responses are rejected before creating a .part file.
func TestOversizedImageRejectedBeforeCreatingPartFile(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/media/huge.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-1023/%d", MaxImageFileBytes+1024))
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(bytes.Repeat([]byte("x"), 1024))
	})

	mgr, _, srv := setupDownloadEnv(t, mux, 1)
	dir := t.TempDir()
	dest := filepath.Join(dir, "huge.jpg")
	task := Task{
		TaskID:  "t-huge",
		ItemDir: dir,
		Files:   []TaskFileProgress{{Owned: true, Role: "image", Path: "huge.jpg", Status: "downloading"}},
	}
	_, _, err := mgr.downloadFileWithRange(context.Background(), []string{srv.URL + "/media/huge.jpg"}, dest, &task)
	if !site.IsKind(err, site.ErrKindSchemaChanged) {
		t.Fatalf("expected schema_changed for >50MiB image, got %v", err)
	}
	if _, statErr := os.Stat(dest + ".part"); !os.IsNotExist(statErr) {
		t.Fatalf("expected no .part file created for oversized image, stat err=%v", statErr)
	}
}

// executeUpgradeTask must propagate network pause exhaustion as paused(network)
// (not failed) and preserve child attempt records on the parent task.
func TestUpgradeNetworkExhaustionPausesAndRecordsAttempts(t *testing.T) {
	var srvURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/x/web-interface/wbi/view", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"bvid": "BV1upgNet001", "aid": 300, "title": "Upgrade Net Pause",
				"owner": map[string]any{"mid": 300, "name": "UP"},
				"pages": []map[string]any{{"cid": 3001, "page": 1, "part": "P1", "duration": 1}},
			},
		})
	})
	mux.HandleFunc("/x/player/wbi/playurl", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"data": map[string]any{
				"quality": 112, "accept_format": "dash", "accept_quality": []int{112, 80},
				"dash": map[string]any{
					"duration": 1,
					"video":    []map[string]any{{"id": 112, "baseUrl": srvURL + "/media/always500.m4s", "codecid": 7, "codecs": "avc1"}},
				},
			},
		})
	})
	mux.HandleFunc("/media/always500.m4s", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mgr, lib, srv := setupDownloadEnv(t, mux, 1)
	srvURL = srv.URL
	cfg := mgr.dlSettings
	cfg.SaveCover, cfg.SaveDanmaku, cfg.SaveSubtitles = false, false, false
	mgr.UpdateSettings(cfg)

	itemDir := filepath.Join(lib.CacheDir(), "bilibili", "300", "videos", "BV1upgNet001")
	if err := os.MkdirAll(itemDir, 0o700); err != nil {
		t.Fatal(err)
	}
	vBytes, err := BuildSyntheticVideoM4S(30, 30)
	if err != nil {
		t.Fatal(err)
	}
	p1Path := filepath.Join(itemDir, "P01.mp4")
	sum, err := DefragmentFMP4Bytes(vBytes, p1Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lib.SaveItem(itemDir, library.ItemMetadata{
		Version: 1, Site: "bilibili", Kind: "video", ID: "video:BV1upgNet001", Status: "complete",
		Title: "Upgrade Net Pause", ItemDir: itemDir, Author: library.ItemAuthor{Mid: 300, Name: "UP"},
		ExpectedPages: []int{1},
		Media:         []library.MediaFile{{Role: "video", Path: "P01.mp4", Bytes: sum.FileBytes, SHA256: sum.SHA256}},
		Parts:         []library.VideoPartRecord{{CID: 3001, Page: 1, File: "P01.mp4", Video: library.PartVideoInfo{Quality: 80}}},
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer mgr.Stop()

	upgTask, err := mgr.Upgrade(ctx, "BV1upgNet001")
	if err != nil {
		t.Fatal(err)
	}
	paused := waitForTaskStatus(t, mgr, upgTask.TaskID, "paused", 6*time.Second)
	if paused.PauseReason != "network" {
		t.Fatalf("expected upgrade task paused(network), got %+v", paused)
	}
	if len(paused.Attempts) < 5 {
		t.Fatalf("expected child retry attempts propagated to parent task, got %d", len(paused.Attempts))
	}
	var dbAttempts int
	if err := mgr.db.QueryRow(`SELECT COUNT(*) FROM task_attempts WHERE task_id = ?`, upgTask.TaskID).Scan(&dbAttempts); err != nil {
		t.Fatal(err)
	}
	if dbAttempts < 5 {
		t.Fatalf("expected >=5 rows in task_attempts for parent upgrade task, got %d", dbAttempts)
	}
}
