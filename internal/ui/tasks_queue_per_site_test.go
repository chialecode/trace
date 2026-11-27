package ui

import (
	"reflect"
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

type perSiteReorderStub struct {
	service.BackendAPI
	mu         sync.Mutex
	queue      []service.CacheQueueTask
	tasks      []download.Task
	queueMoves [][2]string
	dlMoves    [][2]string
}

func (s *perSiteReorderStub) HasAnyLogin() bool { return true }

func (s *perSiteReorderStub) ListCacheQueue() []service.CacheQueueTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]service.CacheQueueTask, len(s.queue))
	copy(out, s.queue)
	return out
}

func (s *perSiteReorderStub) MoveCacheQueue(id, direction string) (service.CacheQueueTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queueMoves = append(s.queueMoves, [2]string{id, direction})
	var targetSite string
	for _, q := range s.queue {
		if q.ID == id {
			targetSite = q.Site
			break
		}
	}
	var idxs []int
	pos := -1
	for i, q := range s.queue {
		if q.Site == targetSite && isUnfinishedCacheQueueStatus(q.Status) {
			if q.ID == id {
				pos = len(idxs)
			}
			idxs = append(idxs, i)
		}
	}
	if pos >= 0 {
		switch direction {
		case service.QueueMoveTop:
			if pos > 0 {
				picked := s.queue[idxs[pos]]
				for k := pos; k > 0; k-- {
					s.queue[idxs[k]] = s.queue[idxs[k-1]]
				}
				s.queue[idxs[0]] = picked
			}
		case service.QueueMoveUp:
			if pos > 0 {
				s.queue[idxs[pos-1]], s.queue[idxs[pos]] = s.queue[idxs[pos]], s.queue[idxs[pos-1]]
			}
		case service.QueueMoveDown:
			if pos < len(idxs)-1 {
				s.queue[idxs[pos]], s.queue[idxs[pos+1]] = s.queue[idxs[pos+1]], s.queue[idxs[pos]]
			}
		}
	}
	for _, q := range s.queue {
		if q.ID == id {
			return q, nil
		}
	}
	return service.CacheQueueTask{}, nil
}

func (s *perSiteReorderStub) ListDownloadTasks() ([]download.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]download.Task, len(s.tasks))
	copy(out, s.tasks)
	return out, nil
}

func (s *perSiteReorderStub) MoveDownloadTask(taskID, direction string) (download.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dlMoves = append(s.dlMoves, [2]string{taskID, direction})
	var qIdxs []int
	pos := -1
	for i, tk := range s.tasks {
		if tk.Status == "queued" {
			if tk.TaskID == taskID {
				pos = len(qIdxs)
			}
			qIdxs = append(qIdxs, i)
		}
	}
	if pos >= 0 {
		switch direction {
		case service.QueueMoveTop:
			if pos > 0 {
				picked := s.tasks[qIdxs[pos]]
				for k := pos; k > 0; k-- {
					s.tasks[qIdxs[k]] = s.tasks[qIdxs[k-1]]
				}
				s.tasks[qIdxs[0]] = picked
			}
		case service.QueueMoveUp:
			if pos > 0 {
				s.tasks[qIdxs[pos-1]], s.tasks[qIdxs[pos]] = s.tasks[qIdxs[pos]], s.tasks[qIdxs[pos-1]]
			}
		case service.QueueMoveDown:
			if pos < len(qIdxs)-1 {
				s.tasks[qIdxs[pos]], s.tasks[qIdxs[pos+1]] = s.tasks[qIdxs[pos+1]], s.tasks[qIdxs[pos]]
			}
		}
	}
	for _, tk := range s.tasks {
		if tk.TaskID == taskID {
			return tk, nil
		}
	}
	return download.Task{}, nil
}

// DEC-088: Tasks › 进行中 › 缓存队列 groups tasks by site, clearly surfaces
// per-site independent running/backoff status, supports site filtering and
// manual top/up/down reordering for both cache queue and queued download tasks,
// and keeps zero horizontal overflow across 720 / 1000 / 1280 widths.
func TestTasksQueuePerSiteGroupingReorderAndResponsiveLayout(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	stub := &perSiteReorderStub{
		BackendAPI: app.backend,
		queue: []service.CacheQueueTask{
			{ID: "cq_bili_1", Site: "bilibili", User: "1001", UserName: "UP主甲", List: "videos", ListLabel: "投稿", Status: service.CacheQueueRunning, StatusText: "进行中", CachedCount: 5, ActiveTitle: "视频一"},
			{ID: "cq_bili_2", Site: "bilibili", User: "1002", UserName: "UP主乙", List: "dynamics", ListLabel: "动态", Status: service.CacheQueueQueued, StatusText: "排队中"},
			{ID: "cq_bili_3", Site: "bilibili", User: "1003", UserName: "UP主丙", List: "videos", ListLabel: "投稿", Status: service.CacheQueueQueued, StatusText: "排队中"},
			{ID: "cq_dy_1", Site: "douyin", User: "sec_1", UserName: "抖音甲", List: "works", ListLabel: "作品", Status: service.CacheQueueWaitingBackoff, StatusText: "遇到风控，等待站点退避结束", StopReason: "遇到风控，等待站点退避结束"},
			{ID: "cq_dy_2", Site: "douyin", User: "sec_2", UserName: "抖音乙", List: "works", ListLabel: "作品", Status: service.CacheQueueQueued, StatusText: "排队中"},
			{ID: "cq_wb_1", Site: "weibo", User: "5001", UserName: "微博甲", List: "posts", ListLabel: "微博", Status: service.CacheQueueCompleted, StatusText: "已完成", CachedCount: 8},
			{ID: "cq_x_1", Site: "x", User: "9001", UserName: "X作者甲", List: "posts", ListLabel: "帖子", Status: service.CacheQueueWaiting, StatusText: "等待下一条"},
		},
		tasks: []download.Task{
			{TaskID: "dl_run", Site: "bilibili", Kind: "video", TargetID: "BV1R", Title: "运行中视频", Status: "running", Progress: 0.4},
			{TaskID: "dl_q1", Site: "weibo", Kind: "dynamic", TargetID: "wb1", Title: "排队微博一", Status: "queued"},
			{TaskID: "dl_q2", Site: "douyin", Kind: "video", TargetID: "dy1", Title: "排队抖音一", Status: "queued"},
			{TaskID: "dl_q3", Site: "x", Kind: "dynamic", TargetID: "x1", Title: "排队X一", Status: "queued"},
		},
	}
	app.backend = stub
	app.CacheQueue = stub.ListCacheQueue()
	app.Tasks, _ = stub.ListDownloadTasks()
	app.Navigate(RouteTasks)
	app.selectTasksQueue("")

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	// 1. All 4 sites render independent per-site group headers and clear isolation status.
	for _, label := range []string{
		"缓存队列站点筛选：all",
		"缓存队列站点筛选：bilibili",
		"缓存队列站点筛选：douyin",
		"缓存队列站点筛选：weibo",
		"缓存队列站点筛选：x",
		"缓存队列站点分组：bilibili",
		"缓存队列站点分组：douyin",
		"缓存队列站点分组：weibo",
		"缓存队列站点分组：x",
	} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("missing per-site queue UI element %q", label)
		}
	}
	if !tt.HasText("退避中 · 不影响其他站点") {
		t.Fatal("expected Douyin group header to display 退避中 · 不影响其他站点")
	}
	if !tt.HasText("进行中 · 独立并行") {
		t.Fatal("expected Bilibili group header to display 进行中 · 独立并行")
	}
	// Single unfinished task site (X) and completed-only site (Weibo) must NOT show cluttered reorder buttons.
	if _, ok := tt.Find("置顶缓存队列：cq_x_1"); ok {
		t.Fatal("single-unfinished-task site X should not show reorder buttons")
	}
	if _, ok := tt.Find("置顶缓存队列：cq_wb_1"); ok {
		t.Fatal("completed task cq_wb_1 should not show reorder buttons")
	}

	// 2. Click reorder controls on Bilibili tasks (top / up / down).
	if err := tt.Click("置顶缓存队列：cq_bili_3"); err != nil {
		t.Fatalf("click top cq_bili_3: %v", err)
	}
	tt.Frame()
	if err := tt.Click("上移缓存队列：cq_bili_2"); err != nil {
		t.Fatalf("click up cq_bili_2: %v", err)
	}
	tt.Frame()
	if err := tt.Click("下移缓存队列：cq_bili_3"); err != nil {
		t.Fatalf("click down cq_bili_3: %v", err)
	}
	tt.Frame()

	// Verify Bilibili order changed to [cq_bili_2, cq_bili_3, cq_bili_1] while Douyin stayed untouched.
	var biliOrder, dyOrder []string
	for _, q := range app.CacheQueue {
		if q.Site == "bilibili" {
			biliOrder = append(biliOrder, q.ID)
		} else if q.Site == "douyin" {
			dyOrder = append(dyOrder, q.ID)
		}
	}
	if !reflect.DeepEqual(biliOrder, []string{"cq_bili_2", "cq_bili_3", "cq_bili_1"}) {
		t.Fatalf("bilibili order after top/up/down = %v, want [cq_bili_2 cq_bili_3 cq_bili_1]", biliOrder)
	}
	if !reflect.DeepEqual(dyOrder, []string{"cq_dy_1", "cq_dy_2"}) {
		t.Fatalf("douyin order unexpectedly changed: %v", dyOrder)
	}

	// 3. Site filter pill isolates a single site cleanly and restores on "all".
	if err := tt.Click("缓存队列站点筛选：douyin"); err != nil {
		t.Fatalf("click site filter douyin: %v", err)
	}
	tt.Frame()
	if _, ok := tt.Find("缓存队列站点分组：douyin"); !ok {
		t.Fatal("expected douyin group visible when filtered to douyin")
	}
	if _, ok := tt.Find("缓存队列站点分组：bilibili"); ok {
		t.Fatal("bilibili group should be hidden when filtered to douyin")
	}
	if err := tt.Click("缓存队列站点筛选：all"); err != nil {
		t.Fatalf("click site filter all: %v", err)
	}
	tt.Frame()

	// 4. Verify zero horizontal overflow across 720 / 1000 / 1280 widths on cache queue tab.
	for _, w := range []int{720, 1000, 1280} {
		wf := float32(w)
		tw := ui.NewTester(app.View, w, 860)
		tw.Frame()
		center, ok := tw.Find("中栏内容")
		if !ok || center.X+center.W > wf+0.5 {
			t.Fatalf("cache queue width %d center overflowed: %+v", w, center)
		}
		for _, label := range []string{
			"缓存队列站点筛选：all",
			"缓存队列站点分组：bilibili",
			"置顶缓存队列：cq_bili_3",
			"上移缓存队列：cq_bili_3",
			"下移缓存队列：cq_bili_3",
		} {
			r, found := tw.Find(label)
			if !found {
				t.Fatalf("width %d missing %q", w, label)
			}
			if r.X < -0.5 || r.X+r.W > wf+0.5 || r.W <= 0 || r.H <= 0 {
				t.Fatalf("width %d %q overflowed viewport: %+v", w, label, r)
			}
		}
	}

	// 5. Switch to Download Tasks sub-tab, test queued task reordering + zero overflow at 720/1000/1280.
	app.selectTasksDownloads()
	tt.Frame()
	if _, ok := tt.Find("置顶下载任务：dl_run"); ok {
		t.Fatal("running download task must not show queued reorder buttons")
	}
	if err := tt.Click("置顶下载任务：dl_q3"); err != nil {
		t.Fatalf("click top dl_q3: %v", err)
	}
	tt.Frame()
	if err := tt.Click("上移下载任务：dl_q2"); err != nil {
		t.Fatalf("click up dl_q2: %v", err)
	}
	tt.Frame()
	if err := tt.Click("下移下载任务：dl_q3"); err != nil {
		t.Fatalf("click down dl_q3: %v", err)
	}
	tt.Frame()

	var dlOrder []string
	for _, tk := range app.Tasks {
		if tk.Status == "queued" {
			dlOrder = append(dlOrder, tk.TaskID)
		}
	}
	if !reflect.DeepEqual(dlOrder, []string{"dl_q2", "dl_q3", "dl_q1"}) {
		t.Fatalf("download queued order = %v, want [dl_q2 dl_q3 dl_q1]", dlOrder)
	}

	for _, w := range []int{720, 1000, 1280} {
		wf := float32(w)
		tw := ui.NewTester(app.View, w, 860)
		tw.Frame()
		for _, label := range []string{"置顶下载任务：dl_q3", "上移下载任务：dl_q3", "下移下载任务：dl_q3"} {
			r, found := tw.Find(label)
			if !found {
				t.Fatalf("downloads width %d missing %q", w, label)
			}
			if r.X < -0.5 || r.X+r.W > wf+0.5 || r.W <= 0 || r.H <= 0 {
				t.Fatalf("downloads width %d %q overflowed viewport: %+v", w, label, r)
			}
		}
	}
}
