package ui

import (
	"sync"
	"testing"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/egoist/mygo/ui"
)

type batchRetryBackendStub struct {
	service.BackendAPI

	mu           sync.Mutex
	tasks        []download.Task
	nextRes      download.BatchRetryResult
	nextErr      error
	retryCalls   int
	listCalls    int
	loggedErrors []error
}

func (b *batchRetryBackendStub) HasAnyLogin() bool {
	return true
}

func (b *batchRetryBackendStub) ListDownloadTasks() ([]download.Task, error) {
	b.mu.Lock()
	b.listCalls++
	out := append([]download.Task(nil), b.tasks...)
	b.mu.Unlock()
	return out, nil
}

func (b *batchRetryBackendStub) RetryAllTasks() (download.BatchRetryResult, error) {
	b.mu.Lock()
	b.retryCalls++
	res := b.nextRes
	err := b.nextErr
	if err == nil && res.Retried > 0 {
		for i := range b.tasks {
			if download.IsRetryableTask(b.tasks[i]) {
				b.tasks[i].Status = "queued"
				b.tasks[i].PauseReason = ""
				b.tasks[i].ErrorKind = ""
				b.tasks[i].ErrorMessage = ""
				b.tasks[i].LastError = ""
			}
		}
	}
	b.mu.Unlock()
	return res, err
}

func (b *batchRetryBackendStub) LogUserError(err error) {
	b.mu.Lock()
	b.loggedErrors = append(b.loggedErrors, err)
	b.mu.Unlock()
}

func TestTasksBatchRetryCountVisibilityAndNarrowLayout(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	stub := &batchRetryBackendStub{
		BackendAPI: app.backend,
		tasks: []download.Task{
			{TaskID: "tk-failed", Site: "bilibili", Kind: "video", TargetID: "BV1F", Title: "失败任务", Status: "failed"},
			{TaskID: "tk-partial", Site: "bilibili", Kind: "dynamic", TargetID: "dyn1", Title: "部分完成任务", Status: "partial"},
			{TaskID: "tk-paused-net", Site: "douyin", Kind: "video", TargetID: "7301", Title: "网络暂停任务", Status: "paused", PauseReason: "network"},
			{TaskID: "tk-paused-closed", Site: "weibo", Kind: "dynamic", TargetID: "5001", Title: "关闭暂停任务", Status: "paused", PauseReason: "app_closed"},
			// Excluded from retryable:
			{TaskID: "tk-paused-user", Site: "bilibili", Kind: "video", TargetID: "BV1U", Title: "手动暂停任务", Status: "paused", PauseReason: "user"},
			{TaskID: "tk-queued", Site: "bilibili", Kind: "video", TargetID: "BV1Q", Title: "排队中任务", Status: "queued"},
			{TaskID: "tk-running", Site: "bilibili", Kind: "video", TargetID: "BV1R", Title: "运行中任务", Status: "running"},
			{TaskID: "tk-completed", Site: "bilibili", Kind: "video", TargetID: "BV1C", Title: "已完成任务", Status: "completed"},
			{TaskID: "tk-canceled", Site: "bilibili", Kind: "video", TargetID: "BV1X", Title: "已取消任务", Status: "canceled"},
		},
	}
	app.backend = stub
	app.Tasks = append([]download.Task(nil), stub.tasks...)
	app.Navigate(RouteTasks)
	app.TasksSection = "downloads"

	// Wide (1280x860): count must match download.IsRetryableTask (4), finished = 2.
	ttWide := ui.NewTester(app.View, 1280, 860)
	ttWide.Frame()
	if _, ok := ttWide.Find("批量重试失败或已暂停的任务"); !ok {
		t.Fatal("missing batch retry button when 4 retryable tasks exist")
	}
	if !ttWide.HasText("批量重试（4）") {
		t.Fatal("expected batch retry button text 批量重试（4）")
	}
	if _, ok := ttWide.Find("清除已结束的任务"); !ok || !ttWide.HasText("清除已结束（2）") {
		t.Fatal("expected finished tasks clear button 清除已结束（2） alongside batch retry")
	}

	// Narrow (720x860): zero horizontal overflow for both action buttons.
	ttNarrow := ui.NewTester(app.View, 720, 860)
	ttNarrow.Frame()
	for _, label := range []string{"批量重试失败或已暂停的任务", "清除已结束的任务"} {
		r, ok := ttNarrow.Find(label)
		if !ok {
			t.Fatalf("narrow 720px missing %q", label)
		}
		if r.X < -0.5 || r.X+r.W > 720.5 || r.W <= 0 || r.H <= 0 {
			t.Fatalf("narrow 720px %q overflowed viewport: rect=%+v", label, r)
		}
	}

	// Hidden when retryable == 0 (only user-paused, queued, running, completed, canceled).
	app.Tasks = []download.Task{
		{TaskID: "tk-paused-user", Site: "bilibili", Kind: "video", TargetID: "BV1U", Title: "手动暂停", Status: "paused", PauseReason: "user"},
		{TaskID: "tk-queued", Site: "bilibili", Kind: "video", TargetID: "BV1Q", Title: "排队中", Status: "queued"},
		{TaskID: "tk-running", Site: "bilibili", Kind: "video", TargetID: "BV1R", Title: "运行中", Status: "running"},
		{TaskID: "tk-completed", Site: "bilibili", Kind: "video", TargetID: "BV1C", Title: "已完成", Status: "completed"},
		{TaskID: "tk-canceled", Site: "bilibili", Kind: "video", TargetID: "BV1X", Title: "已取消", Status: "canceled"},
	}
	ttWide.Frame()
	if _, ok := ttWide.Find("批量重试失败或已暂停的任务"); ok {
		t.Fatal("batch retry button must be hidden when retryable == 0")
	}
	if _, ok := ttWide.Find("清除已结束的任务"); !ok {
		t.Fatal("clear finished button should remain visible when finished > 0")
	}
}

func TestTasksBatchRetryClickFeedbackAndErrors(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	baseTasks := func() []download.Task {
		return []download.Task{
			{TaskID: "tk-1", Site: "bilibili", Kind: "video", TargetID: "BV1A", Title: "任务1", Status: "failed"},
			{TaskID: "tk-2", Site: "douyin", Kind: "video", TargetID: "7302", Title: "任务2", Status: "paused", PauseReason: "network"},
			{TaskID: "tk-3", Site: "weibo", Kind: "dynamic", TargetID: "5002", Title: "任务3", Status: "partial"},
		}
	}

	stub := &batchRetryBackendStub{
		BackendAPI: app.backend,
	}
	app.backend = stub
	app.Navigate(RouteTasks)
	app.TasksSection = "downloads"
	tt := ui.NewTester(app.View, 1280, 860)

	cases := []struct {
		name           string
		res            download.BatchRetryResult
		err            error
		wantBannerInfo string
		wantTaskErr    string
		wantLoggedErr  bool
	}{
		{
			name:           "all_retried",
			res:            download.BatchRetryResult{Retried: 3},
			wantBannerInfo: "已重新排队 3 个任务",
		},
		{
			name: "partial_auth_multi_site",
			res: download.BatchRetryResult{
				Retried:          1,
				SkippedAuth:      2,
				MissingAuthSites: []string{"douyin", "weibo"},
			},
			wantBannerInfo: "已重新排队 1 个任务；另有 2 个任务需先登录抖音、微博",
		},
		{
			name: "retried_with_duplicates",
			res: download.BatchRetryResult{
				Retried:          2,
				SkippedDuplicate: 1,
			},
			wantBannerInfo: "已重新排队 2 个任务（跳过 1 个已在队列中的重复任务）",
		},
		{
			name: "only_duplicates",
			res: download.BatchRetryResult{
				Retried:          0,
				SkippedDuplicate: 2,
			},
			wantBannerInfo: "对应作品已在队列中（跳过 2 个重复任务）",
		},
		{
			name: "auth_error",
			err: &site.Error{
				Kind:    site.ErrKindAuthRequired,
				Message: "请先登录抖音、微博后再重试",
			},
			wantBannerInfo: "",
			wantTaskErr:    "操作没有完成：请先登录抖音、微博后再重试",
			wantLoggedErr:  true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub.mu.Lock()
			stub.tasks = baseTasks()
			stub.nextRes = tc.res
			stub.nextErr = tc.err
			stub.retryCalls = 0
			stub.listCalls = 0
			stub.loggedErrors = nil
			stub.mu.Unlock()

			app.Tasks = baseTasks()
			app.TaskRemoveConfirmID = "tk-1"
			app.BannerInfo = "旧提示"
			app.TaskActionError = "旧错误"

			tt.Frame()
			if err := tt.Click("批量重试失败或已暂停的任务"); err != nil {
				t.Fatalf("click batch retry: %v", err)
			}
			tt.Frame()

			stub.mu.Lock()
			retryCalls := stub.retryCalls
			listCalls := stub.listCalls
			loggedN := len(stub.loggedErrors)
			stub.mu.Unlock()

			if retryCalls != 1 {
				t.Fatalf("RetryAllTasks calls = %d, want 1", retryCalls)
			}
			if listCalls == 0 {
				t.Fatal("expected RefreshData to reload download tasks via ListDownloadTasks")
			}
			if app.TaskRemoveConfirmID != "" {
				t.Fatalf("TaskRemoveConfirmID = %q, want cleared", app.TaskRemoveConfirmID)
			}
			if app.BannerInfo != tc.wantBannerInfo {
				t.Fatalf("BannerInfo = %q, want %q", app.BannerInfo, tc.wantBannerInfo)
			}
			if app.TaskActionError != tc.wantTaskErr {
				t.Fatalf("TaskActionError = %q, want %q", app.TaskActionError, tc.wantTaskErr)
			}
			if tc.wantBannerInfo != "" && !tt.HasText(tc.wantBannerInfo) {
				t.Fatalf("expected BannerInfo %q to be visibly rendered on RouteTasks", tc.wantBannerInfo)
			}
			if tc.wantTaskErr != "" && !tt.HasText(tc.wantTaskErr) {
				t.Fatalf("expected TaskActionError %q to be visibly rendered on RouteTasks", tc.wantTaskErr)
			}
			if tc.wantLoggedErr && loggedN == 0 {
				t.Fatal("expected LogUserError to be called on RetryAllTasks error")
			}
		})
	}
}
