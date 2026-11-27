package download

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

func minimalDec089PNGBytes() []byte {
	return []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x02, 0x00, 0x00, 0x00, 0x90, 0x77, 0x53,
		0xde, 0x00, 0x00, 0x00, 0x0c, 0x49, 0x44, 0x41,
		0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
		0x00, 0x03, 0x01, 0x01, 0x00, 0x18, 0xdd, 0x8d,
		0xb0, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e,
		0x44, 0xae, 0x42, 0x60, 0x82,
	}
}

func TestWeiboOptionalNoWMCompanion418DoesNotTripBackoffOrFailPrimary(t *testing.T) {
	var primaryHits atomic.Int32
	var nowmHits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/oslarge/") {
			nowmHits.Add(1)
			w.WriteHeader(http.StatusTeapot) // HTTP 418 on optional /oslarge/ companion!
			return
		}
		primaryHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(minimalDec089PNGBytes())
	})

	mgr, lib, srv, _, _ := setupDouyinDownloadTestEnv(t, handler, 1)
	wbPlanner := newFakeWeiboSitePlanner()
	mgr.RegisterSitePlanner(wbPlanner)

	const mid = "5000000000000901"
	wbPlanner.setPlan(mid, func(_ int) (site.DownloadPlan, error) {
		return site.DownloadPlan{
			Site:       site.Weibo,
			ItemKey:    "weibo:dynamic:" + mid,
			TargetID:   mid,
			Kind:       "image_note",
			AuthorUID:  "1000000001",
			AuthorName: "微博合成作者",
			Title:      "测试主图成功但oslarge遇418",
			PubTs:      1763700100,
			Media:      site.WeiboMediaTransport("TRACE-Test-UA", true),
			Images: []site.MediaImageAsset{{
				Index:            1,
				MediaRole:        "image",
				ImageURLs:        []string{srv.URL + "/large/001SynThPic418.png"},
				WatermarkFreeURL: srv.URL + "/oslarge/001SynThPic418.png",
			}},
		}, nil
	})

	task, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: mid, Source: "manual"})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork: %v", err)
	}
	done := waitForTaskTerminal(t, mgr, task.TaskID, 10*time.Second)
	if done.Status != "completed" {
		t.Fatalf("primary image should succeed despite /oslarge/ 418; got status=%s kind=%s err=%s", done.Status, done.ErrorKind, done.ErrorMessage)
	}
	if primaryHits.Load() < 1 || nowmHits.Load() < 1 {
		t.Fatalf("expected both primary (%d) and /oslarge/ (%d) requests", primaryHits.Load(), nowmHits.Load())
	}
	if wbPlanner.reportedCount() != 0 {
		t.Fatalf("optional /oslarge/ 418 must NOT trigger site-wide backoff/stop; reported=%d", wbPlanner.reportedCount())
	}
	item, ok, err := lib.GetItem("weibo:dynamic:" + mid)
	if err != nil || !ok || item.Status != "complete" {
		t.Fatalf("expected complete library item, got ok=%v status=%q err=%v", ok, item.Status, err)
	}
	if len(item.NotePages) != 1 || item.NotePages[0].Image == "" || item.NotePages[0].NoWatermark != "" {
		t.Fatalf("expected primary saved and companion skipped, got NotePages=%+v", item.NotePages)
	}
}

func TestDownloadManualProbeBypassAndBatchCap(t *testing.T) {
	var cdnHits atomic.Int32
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnHits.Add(1)
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(minimalDec089PNGBytes())
	})
	mgr, _, srv, _, _ := setupDouyinDownloadTestEnv(t, handler, 2)

	var wbBackoff atomic.Bool
	wbBackoff.Store(true)
	var failPlan atomic.Bool

	wbPlanner := newFakeWeiboSitePlanner()
	for _, id := range []string{"5000000000000911", "5000000000000912"} {
		targetID := id
		wbPlanner.setPlan(targetID, func(_ int) (site.DownloadPlan, error) {
			if failPlan.Load() {
				return site.DownloadPlan{}, &site.Error{Site: site.Weibo, Kind: site.ErrKindRiskControl, Message: "still 418"}
			}
			return site.DownloadPlan{
				Site:       site.Weibo,
				ItemKey:    "weibo:dynamic:" + targetID,
				TargetID:   targetID,
				Kind:       "image_note",
				AuthorUID:  "1000000002",
				AuthorName: "微博探测作者",
				Title:      "探测任务 " + targetID,
				PubTs:      1763700200,
				Media:      site.WeiboMediaTransport("TRACE-Test-UA", true),
				Images:     []site.MediaImageAsset{{Index: 1, MediaRole: "image", ImageURLs: []string{srv.URL + "/large/" + targetID + ".png"}}},
			}, nil
		})
	}
	mgr.RegisterSitePlanner(wbPlanner)
	mgr.SetSiteBackoffChecker(func(sid site.ID) (bool, time.Time) {
		if sid == site.Weibo && wbBackoff.Load() {
			return true, time.Now().UTC().Add(15 * time.Minute)
		}
		return false, time.Time{}
	})

	var probeMu sync.Mutex
	var probeResults []error
	mgr.SetManualProbeResultHandler(func(sid site.ID, probeErr error) {
		probeMu.Lock()
		probeResults = append(probeResults, probeErr)
		probeMu.Unlock()
		if sid == site.Weibo && probeErr == nil {
			wbBackoff.Store(false)
			mgr.Wake()
		}
	})

	// Enqueue two queue-sourced Weibo tasks: while Weibo is in backoff, NEITHER downloads automatically.
	t1, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: "5000000000000911", Source: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := mgr.EnqueueWeiboWork(context.Background(), WeiboWorkEnqueueRequest{TargetID: "5000000000000912", Source: "queue"})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if cdnHits.Load() != 0 {
		t.Fatalf("backed-off queue tasks must not execute automatically: cdnHits=%d", cdnHits.Load())
	}

	// Pause both for backoff and simulate ongoing 418 on fresh plan; RetryActionable re-queues both
	// but marks at most 1 task per backed-off site as a manual probe so it never bursts concurrent probes!
	failPlan.Store(true)
	mgr.PauseQueuedForBackoff(t1.TaskID, "退避中")
	mgr.PauseQueuedForBackoff(t2.TaskID, "退避中")
	callsBeforeRetry := wbPlanner.callCount("5000000000000911") + wbPlanner.callCount("5000000000000912")
	res, err := mgr.RetryActionable(map[string]bool{"weibo": true})
	if err != nil {
		t.Fatalf("RetryActionable: %v", err)
	}
	if res.Retried != 2 {
		t.Fatalf("RetryActionable should re-queue both paused tasks, got %+v", res)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		probeMu.Lock()
		n := len(probeResults)
		probeMu.Unlock()
		if n >= 1 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	probeMu.Lock()
	if len(probeResults) != 1 || !site.IsKind(probeResults[0], site.ErrKindRiskControl) {
		probeMu.Unlock()
		t.Fatalf("expected exactly 1 failed risk_control probe result (capped to 1 per backed-off site), got %v", probeResults)
	}
	probeMu.Unlock()
	if got := wbPlanner.callCount("5000000000000911") + wbPlanner.callCount("5000000000000912") - callsBeforeRetry; got != 1 {
		t.Fatalf("expected exactly 1 planner probe call during backoff, got %d", got)
	}
	if !wbBackoff.Load() {
		t.Fatal("failed probe must keep Weibo backoff active")
	}

	// Now Weibo recovers: explicit ResumeManualProbe on the probed task succeeds, fires onManualProbeResult(nil)
	// -> clears backoff -> probeTarget completes and normal Resume on the other task succeeds too!
	failPlan.Store(false)
	probeTarget, otherTarget := t1.TaskID, t2.TaskID
	if gotT2, ok, err := mgr.GetTask(t2.TaskID); err == nil && ok && gotT2.Status == "failed" {
		probeTarget, otherTarget = t2.TaskID, t1.TaskID
	}
	if _, err := mgr.ResumeManualProbe(probeTarget); err != nil {
		t.Fatalf("ResumeManualProbe(%s): %v", probeTarget, err)
	}
	doneProbe := waitForTaskTerminal(t, mgr, probeTarget, 10*time.Second)
	if doneProbe.Status != "completed" {
		t.Fatalf("expected probed task %s completed, got %+v", probeTarget, doneProbe)
	}
	if wbBackoff.Load() {
		t.Fatal("successful manual probe must clear Weibo backoff")
	}
	if _, err := mgr.Resume(otherTarget); err != nil {
		t.Fatalf("Resume(%s) after backoff cleared: %v", otherTarget, err)
	}
	doneOther := waitForTaskTerminal(t, mgr, otherTarget, 10*time.Second)
	if doneOther.Status != "completed" {
		t.Fatalf("expected other task %s completed after backoff cleared, got %+v", otherTarget, doneOther)
	}
}
