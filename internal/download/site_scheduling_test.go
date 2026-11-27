package download

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/site"
)

type genericFakeSitePlanner struct {
	siteID site.ID
	mu     sync.Mutex
	plans  map[string]func(call int) (WorkDownloadPlan, error)
	calls  map[string]int
}

func newGenericFakeSitePlanner(siteID site.ID) *genericFakeSitePlanner {
	return &genericFakeSitePlanner{
		siteID: siteID,
		plans:  make(map[string]func(call int) (WorkDownloadPlan, error)),
		calls:  make(map[string]int),
	}
}

func (p *genericFakeSitePlanner) SiteID() site.ID                             { return p.siteID }
func (p *genericFakeSitePlanner) RequireAuth(string) error                    { return nil }
func (p *genericFakeSitePlanner) ReportAutomaticError(context.Context, error) {}

func (p *genericFakeSitePlanner) setPlan(targetID string, fn func(call int) (WorkDownloadPlan, error)) {
	p.mu.Lock()
	p.plans[targetID] = fn
	p.mu.Unlock()
}

func (p *genericFakeSitePlanner) PlanWorkDownload(_ context.Context, targetID string, _ WorkPlanOptions) (WorkDownloadPlan, error) {
	p.mu.Lock()
	p.calls[targetID]++
	call := p.calls[targetID]
	fn := p.plans[targetID]
	p.mu.Unlock()
	if fn == nil {
		return WorkDownloadPlan{}, &site.Error{Site: p.siteID, Kind: site.ErrKindUnavailable, Message: "not found"}
	}
	return fn(call)
}

func makeImageWorkPlan(siteID site.ID, targetID, authorUID, imgURL string) WorkDownloadPlan {
	transport := site.DouyinVODTransport("Mozilla/5.0 Test", true)
	if siteID == site.Weibo {
		transport = site.WeiboMediaTransport("Mozilla/5.0 Test", true)
	}
	return WorkDownloadPlan{
		Site:       siteID,
		Kind:       "dynamic",
		TargetID:   targetID,
		Title:      string(siteID) + " " + targetID,
		AuthorUID:  authorUID,
		AuthorName: "Synth_" + string(siteID),
		PubTs:      1720000000,
		Pages: []PlannedNotePage{
			{Index: 1, Kind: "image", ImageURLs: []string{imgURL}},
		},
		Transport: transport,
	}
}

func waitSchedTaskDone(t *testing.T, mgr *Manager, taskID string) Task {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		tk, ok, err := mgr.GetTask(taskID)
		if err != nil {
			t.Fatalf("GetTask(%s): %v", taskID, err)
		}
		if ok && (tk.Status == "completed" || tk.Status == "failed" || tk.Status == "partial" || tk.Status == "canceled") {
			return tk
		}
		time.Sleep(15 * time.Millisecond)
	}
	tk, _, _ := mgr.GetTask(taskID)
	t.Fatalf("timed out waiting for task %s to finish; last=%+v", taskID, tk)
	return Task{}
}

// DEC-088: When Site A is in backoff, claimNextQueuedLocked must skip Site A's
// automatic/queue tasks and immediately dispatch healthy Site B tasks; once
// Site A's backoff expires, the dispatch timer wakes and runs Site A automatically.
func TestDownloadManagerSkipsBackedOffSiteAndDispatchesHealthySites(t *testing.T) {
	jpg := syntheticJPEGMini()
	var wbHits, dyHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/wb/") {
			wbHits.Add(1)
		} else if strings.HasPrefix(r.URL.Path, "/dy/") {
			dyHits.Add(1)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	}))
	t.Cleanup(srv.Close)

	mgr, _, _, _, _ := setupDouyinDownloadTestEnv(t, srv.Config.Handler, 1)
	wbPlanner := newGenericFakeSitePlanner(site.Weibo)
	dyPlanner := newGenericFakeSitePlanner(site.Douyin)
	mgr.RegisterWorkPlanner(wbPlanner)
	mgr.RegisterWorkPlanner(dyPlanner)

	wbPlan := makeImageWorkPlan(site.Weibo, "wb_backoff_1", "51001", srv.URL+"/wb/1.jpg")
	dyPlan := makeImageWorkPlan(site.Douyin, "dy_healthy_1", "sec_dy_1", srv.URL+"/dy/1.jpg")
	wbPlanner.setPlan("wb_backoff_1", func(int) (WorkDownloadPlan, error) { return wbPlan, nil })
	dyPlanner.setPlan("dy_healthy_1", func(int) (WorkDownloadPlan, error) { return dyPlan, nil })

	// Put Weibo into backoff for 300ms BEFORE starting workers.
	backoffUntil := time.Now().UTC().Add(300 * time.Millisecond)
	mgr.SetSiteBackoffChecker(func(siteID site.ID) (bool, time.Time) {
		if siteID == site.Weibo && time.Now().UTC().Before(backoffUntil) {
			return true, backoffUntil
		}
		return false, time.Time{}
	})

	// Enqueue Weibo FIRST (at head of queue), Douyin SECOND.
	ctx := context.Background()
	wbTask, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{
		TargetID: "wb_backoff_1",
		Kind:     "dynamic",
		Source:   "queue",
		Plan:     &wbPlan,
	})
	if err != nil {
		t.Fatalf("EnqueueWeiboWork: %v", err)
	}
	dyTask, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{
		AwemeID: "dy_healthy_1",
		Source:  "queue",
		Plan:    &dyPlan,
	})
	if err != nil {
		t.Fatalf("EnqueueDouyinWork: %v", err)
	}

	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Stop)
	// Start marks pre-start queued rows as paused; re-queue them via Resume so
	// both enter the live dispatch queue while Weibo is in backoff.
	if _, err := mgr.Resume(wbTask.TaskID); err != nil {
		t.Fatalf("Resume wb: %v", err)
	}
	if _, err := mgr.Resume(dyTask.TaskID); err != nil {
		t.Fatalf("Resume dy: %v", err)
	}

	// Healthy Douyin task must complete BEFORE Weibo's backoff expires, while Weibo makes 0 requests.
	dyDone := waitSchedTaskDone(t, mgr, dyTask.TaskID)
	if dyDone.Status != "completed" {
		t.Fatalf("expected healthy Douyin task completed during Weibo backoff, got %+v", dyDone)
	}
	if time.Now().UTC().Before(backoffUntil) && wbHits.Load() != 0 {
		t.Fatalf("backed-off Weibo task ran early during backoff (hits=%d)", wbHits.Load())
	}

	// Once Weibo's backoff expires, dispatchLoop's timer must wake and complete Weibo automatically.
	wbDone := waitSchedTaskDone(t, mgr, wbTask.TaskID)
	if wbDone.Status != "completed" {
		t.Fatalf("expected Weibo task completed after backoff expiry, got %+v", wbDone)
	}
	if dyHits.Load() == 0 || wbHits.Load() == 0 {
		t.Fatalf("expected both sites to fetch media, got dy=%d wb=%d", dyHits.Load(), wbHits.Load())
	}
}

// DEC-088: With MaxConcurrentTasks=2 and Site A holding 1 slot plus more Site A
// tasks queued at the head of the queue, the 2nd concurrency slot must be given
// to Site B (active count 0) instead of starving Site B behind Site A's backlog.
func TestDownloadManagerPerSiteFairnessPreventsStarvation(t *testing.T) {
	jpg := syntheticJPEGMini()
	wb1Started := make(chan struct{})
	wb1Release := make(chan struct{})
	var wb1Once sync.Once
	var wb2Started atomic.Bool

	mux := http.NewServeMux()
	mux.HandleFunc("/wb/1.jpg", func(w http.ResponseWriter, r *http.Request) {
		wb1Once.Do(func() { close(wb1Started) })
		select {
		case <-wb1Release:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	})
	mux.HandleFunc("/wb/2.jpg", func(w http.ResponseWriter, r *http.Request) {
		wb2Started.Store(true)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	})
	mux.HandleFunc("/dy/1.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	})

	srv := httptest.NewServer(mux)
	var releaseOnce sync.Once
	releaseWB1 := func() { releaseOnce.Do(func() { close(wb1Release) }) }
	t.Cleanup(func() {
		releaseWB1()
		srv.Close()
	})

	mgr, _, _, _, _ := setupDouyinDownloadTestEnv(t, mux, 2)
	wbPlanner := newGenericFakeSitePlanner(site.Weibo)
	dyPlanner := newGenericFakeSitePlanner(site.Douyin)
	mgr.RegisterWorkPlanner(wbPlanner)
	mgr.RegisterWorkPlanner(dyPlanner)

	pWB1 := makeImageWorkPlan(site.Weibo, "wb_fair_1", "51001", srv.URL+"/wb/1.jpg")
	pWB2 := makeImageWorkPlan(site.Weibo, "wb_fair_2", "51001", srv.URL+"/wb/2.jpg")
	pDY1 := makeImageWorkPlan(site.Douyin, "dy_fair_1", "sec_dy_1", srv.URL+"/dy/1.jpg")
	wbPlanner.setPlan("wb_fair_1", func(int) (WorkDownloadPlan, error) { return pWB1, nil })
	wbPlanner.setPlan("wb_fair_2", func(int) (WorkDownloadPlan, error) { return pWB2, nil })
	dyPlanner.setPlan("dy_fair_1", func(int) (WorkDownloadPlan, error) { return pDY1, nil })

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Stop)

	// Start wb1 first and wait until it occupies 1 concurrency slot in flight.
	tWB1, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "wb_fair_1", Kind: "dynamic", Source: "manual", Plan: &pWB1})
	if err != nil {
		t.Fatalf("enqueue wb1: %v", err)
	}
	select {
	case <-wb1Started:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for wb1 to start")
	}

	// Pause dispatch briefly by holding m.mu? No — enqueue both wb2 and dy1 while
	// we pause dispatch via MaxConcurrentTasks=1 temporarily, then raise to 2 so both
	// queued candidates compete simultaneously for the 2nd slot!
	mgr.mu.Lock()
	mgr.dlSettings.MaxConcurrentTasks = 1
	mgr.mu.Unlock()

	tWB2, err := mgr.EnqueueWeiboWork(ctx, WeiboWorkEnqueueRequest{TargetID: "wb_fair_2", Kind: "dynamic", Source: "manual", Plan: &pWB2})
	if err != nil {
		t.Fatalf("enqueue wb2: %v", err)
	}
	tDY1, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: "dy_fair_1", Source: "manual", Plan: &pDY1})
	if err != nil {
		t.Fatalf("enqueue dy1: %v", err)
	}

	// Open the 2nd concurrency slot: wb1 is still running on Weibo, so Douyin (0 active)
	// MUST win slot #2 ahead of wb2 (Weibo already has 1 active)!
	mgr.mu.Lock()
	mgr.dlSettings.MaxConcurrentTasks = 2
	mgr.mu.Unlock()
	mgr.notifyWake()

	dyDone := waitSchedTaskDone(t, mgr, tDY1.TaskID)
	if dyDone.Status != "completed" {
		t.Fatalf("expected Douyin task to finish while wb1 blocked, got %+v", dyDone)
	}
	// Release wb1 and confirm both Weibo tasks also complete cleanly.
	releaseWB1()
	if done := waitSchedTaskDone(t, mgr, tWB1.TaskID); done.Status != "completed" {
		t.Fatalf("wb1 failed: %+v", done)
	}
	if done := waitSchedTaskDone(t, mgr, tWB2.TaskID); done.Status != "completed" {
		t.Fatalf("wb2 failed: %+v", done)
	}
	if !wb2Started.Load() {
		t.Fatal("expected wb2 to run after slot freed")
	}
}

// DEC-088: MoveQueuedTask supports top/up/down without changing library.db v2 schema,
// updates ListTasks/ListTasksSummary/ListNonTerminalTasks consistently, and controls
// exact worker dispatch order.
func TestDownloadManagerMoveQueuedTaskReordersDispatchAndList(t *testing.T) {
	jpg := syntheticJPEGMini()
	var (
		orderMu    sync.Mutex
		runOrder   []string
		gateStart  = make(chan struct{})
		gateUnlock = make(chan struct{})
		gateOnce   sync.Once
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/img/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/img/"), ".jpg")
		if id == "gate" {
			gateOnce.Do(func() { close(gateStart) })
			select {
			case <-gateUnlock:
			case <-r.Context().Done():
				return
			}
		} else {
			orderMu.Lock()
			runOrder = append(runOrder, id)
			orderMu.Unlock()
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpg)
	})
	srv := httptest.NewServer(mux)
	var unlockOnce sync.Once
	unlockGate := func() { unlockOnce.Do(func() { close(gateUnlock) }) }
	t.Cleanup(func() {
		unlockGate()
		srv.Close()
	})

	mgr, _, _, _, _ := setupDouyinDownloadTestEnv(t, mux, 1)
	dyPlanner := newGenericFakeSitePlanner(site.Douyin)
	mgr.RegisterWorkPlanner(dyPlanner)

	for _, id := range []string{"gate", "q1", "q2", "q3"} {
		plan := makeImageWorkPlan(site.Douyin, id, "sec_dy_order", srv.URL+"/img/"+id+".jpg")
		dyPlanner.setPlan(id, func(int) (WorkDownloadPlan, error) { return plan, nil })
	}

	ctx := context.Background()
	if err := mgr.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(mgr.Stop)

	// Hold the single worker slot with "gate" while we enqueue q1, q2, q3 and reorder them.
	pGate, _ := dyPlanner.PlanWorkDownload(ctx, "gate", WorkPlanOptions{})
	tGate, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: "gate", Source: "manual", Plan: &pGate})
	if err != nil {
		t.Fatalf("enqueue gate: %v", err)
	}
	select {
	case <-gateStart:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for gate task")
	}

	enqueue := func(id string) Task {
		t.Helper()
		p, _ := dyPlanner.PlanWorkDownload(ctx, id, WorkPlanOptions{})
		tk, err := mgr.EnqueueDouyinWork(ctx, DouyinWorkEnqueueRequest{AwemeID: id, Source: "manual", Plan: &p})
		if err != nil {
			t.Fatalf("enqueue %s: %v", id, err)
		}
		return tk
	}
	t1 := enqueue("q1")
	t2 := enqueue("q2")
	t3 := enqueue("q3")

	queuedTargets := func() []string {
		t.Helper()
		full, err := mgr.ListTasks()
		if err != nil {
			t.Fatalf("ListTasks: %v", err)
		}
		sum, err := mgr.ListTasksSummary()
		if err != nil {
			t.Fatalf("ListTasksSummary: %v", err)
		}
		nonTerm, err := mgr.ListNonTerminalTasks()
		if err != nil {
			t.Fatalf("ListNonTerminalTasks: %v", err)
		}
		extract := func(list []Task) []string {
			var out []string
			for _, tk := range list {
				if tk.Status == "queued" {
					out = append(out, tk.TargetID)
				}
			}
			return out
		}
		a, b, c := extract(full), extract(sum), extract(nonTerm)
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, c) {
			t.Fatalf("queued order mismatch across list APIs: full=%v sum=%v nonTerm=%v", a, b, c)
		}
		return a
	}

	if got := queuedTargets(); !reflect.DeepEqual(got, []string{"q1", "q2", "q3"}) {
		t.Fatalf("initial queued order = %v, want [q1 q2 q3]", got)
	}
	// top: q3 -> [q3, q1, q2]
	if _, err := mgr.MoveQueuedTask(t3.TaskID, "top"); err != nil {
		t.Fatalf("MoveQueuedTask top: %v", err)
	}
	if got := queuedTargets(); !reflect.DeepEqual(got, []string{"q3", "q1", "q2"}) {
		t.Fatalf("after top(q3) = %v, want [q3 q1 q2]", got)
	}
	// up: q2 -> [q3, q2, q1]
	if _, err := mgr.MoveQueuedTask(t2.TaskID, "up"); err != nil {
		t.Fatalf("MoveQueuedTask up: %v", err)
	}
	if got := queuedTargets(); !reflect.DeepEqual(got, []string{"q3", "q2", "q1"}) {
		t.Fatalf("after up(q2) = %v, want [q3 q2 q1]", got)
	}
	// down: q3 -> [q2, q3, q1]
	if _, err := mgr.MoveQueuedTask(t3.TaskID, "down"); err != nil {
		t.Fatalf("MoveQueuedTask down: %v", err)
	}
	if got := queuedTargets(); !reflect.DeepEqual(got, []string{"q2", "q3", "q1"}) {
		t.Fatalf("after down(q3) = %v, want [q2 q3 q1]", got)
	}

	// Non-queued task and bad direction must fail cleanly.
	if _, err := mgr.MoveQueuedTask(tGate.TaskID, "up"); err == nil {
		t.Fatal("expected error reordering running task")
	}
	if _, err := mgr.MoveQueuedTask(t1.TaskID, "sideways"); err == nil {
		t.Fatal("expected error on invalid move direction")
	}

	// Release gate -> remaining tasks MUST execute in exact reordered order [q2, q3, q1]!
	unlockGate()
	for _, id := range []string{tGate.TaskID, t1.TaskID, t2.TaskID, t3.TaskID} {
		if done := waitSchedTaskDone(t, mgr, id); done.Status != "completed" {
			t.Fatalf("task %s failed: %+v", id, done)
		}
	}
	orderMu.Lock()
	gotRun := append([]string(nil), runOrder...)
	orderMu.Unlock()
	if !reflect.DeepEqual(gotRun, []string{"q2", "q3", "q1"}) {
		t.Fatalf("actual dispatch order = %v, want [q2 q3 q1]", gotRun)
	}
}
