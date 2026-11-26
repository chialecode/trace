package ui

import (
	"context"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/download"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func TestTasksSectionLegacyNormalization(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	cases := []struct {
		raw       string
		wantSec   string
		wantSub   string
		activeSub string
	}{
		{"", TasksSectionActive, "downloads", ""},
		{"active", TasksSectionActive, "downloads", ""},
		{"active", TasksSectionActive, "queue", "queue"},
		{"downloads", TasksSectionActive, "downloads", "queue"},
		{"queue", TasksSectionActive, "queue", "downloads"},
		{"live", TasksSectionLive, "downloads", ""},
		{"attention", TasksSectionAttention, "downloads", ""},
		{"conflicts", TasksSectionAttention, "downloads", ""},
		{"incidents", TasksSectionAttention, "downloads", ""},
		{"records", TasksSectionRecords, "downloads", ""},
		{"transfers", TasksSectionRecords, "downloads", ""},
		{"checks", TasksSectionActive, "downloads", ""},
		{"unknown-legacy-key", TasksSectionActive, "downloads", ""},
	}
	for _, tc := range cases {
		app.TasksSection = tc.raw
		app.TasksActiveTab = tc.activeSub
		if got := app.effectiveTasksSection(); got != tc.wantSec {
			t.Errorf("effectiveTasksSection(%q) = %q, want %q", tc.raw, got, tc.wantSec)
		}
		if tc.wantSec == TasksSectionActive {
			if got := app.effectiveTasksActiveTab(); got != tc.wantSub {
				t.Errorf("effectiveTasksActiveTab(raw=%q, sub=%q) = %q, want %q", tc.raw, tc.activeSub, got, tc.wantSub)
			}
		}
	}
}

func TestTasksFourSectionsCountsAttentionAggregationAndResponsiveOverflow(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	rawBackend := app.backend.(*service.Backend)
	_, _ = rawBackend.ImportCookie(context.Background(), "SESSDATA=synth; bili_jct=csrf; DedeUserID=12345", "")
	saved := seedSyntheticLocalConflictItem(t, rawBackend)

	now := time.Now().UTC()
	recentStart := now.Add(-1 * time.Hour).Format(time.RFC3339)
	recentEnd := now.Add(-30 * time.Minute).Format(time.RFC3339)
	oldStart := now.Add(-9 * 24 * time.Hour).Format(time.RFC3339)
	oldEnd := now.Add(-9*24*time.Hour + 20*time.Minute).Format(time.RFC3339)

	endedLive := seedLibraryControlItem(t, app, library.ItemMetadata{
		Version:  1,
		ID:       "88101",
		Site:     "bilibili",
		Kind:     "live",
		RawID:    "88101",
		Status:   "partial",
		Title:    "测试近期录制异常场次",
		URL:      "https://live.bilibili.com/88101",
		PubTs:    now.Add(-1 * time.Hour).Unix(),
		CachedAt: recentEnd,
		Duration: 600,
		Author:   library.ItemAuthor{Mid: 20001, UID: "20001", Name: "主播甲"},
		Media:    []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 600}},
		Live: &library.LiveRecordingInfo{
			RoomID:    88101,
			StartedAt: recentStart,
			EndedAt:   recentEnd,
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentReconnect, At: recentStart, DurationSec: 12, Count: 1},
			},
		},
	})
	_ = seedLibraryControlItem(t, app, library.ItemMetadata{
		Version:  1,
		ID:       "88102-old",
		Site:     "bilibili",
		Kind:     "live",
		RawID:    "88102-old",
		Status:   "partial",
		Title:    "超期录制异常",
		PubTs:    now.Add(-9 * 24 * time.Hour).Unix(),
		CachedAt: oldEnd,
		Author:   library.ItemAuthor{Mid: 20002, UID: "20002", Name: "主播乙"},
		Live: &library.LiveRecordingInfo{
			RoomID:    88102,
			StartedAt: oldStart,
			EndedAt:   oldEnd,
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentDisk, At: oldEnd, Reason: "空间不足", Count: 1},
			},
		},
	})

	stub := &conflictsBackendStub{
		BackendAPI: rawBackend,
		raw:        rawBackend,
		tasks: []download.Task{
			{TaskID: "t_run", Site: "bilibili", Kind: "video", TargetID: "BV1R1", Title: "进行中下载", Status: "running", Progress: 0.5, TotalBytes: 4096},
			{TaskID: "t_fail", Site: "bilibili", Kind: "video", TargetID: "BV1F1", Title: "失败下载任务", Status: "failed", ErrorKind: "network", LastError: "分段超时"},
			{TaskID: "t_pause_net", Site: "douyin", Kind: "video", TargetID: "73099", Title: "网络暂停任务", Status: "paused", PauseReason: "network"},
			// User-paused must NOT count toward 待处理!
			{TaskID: "t_pause_user", Site: "bilibili", Kind: "video", TargetID: "BV1U1", Title: "用户手动暂停", Status: "paused", PauseReason: "user"},
		},
		conflicts: sampleConflictFixtures(saved.ItemKey)[:1], // 1 conflict
	}
	app.backend = stub
	app.Tasks = append([]download.Task(nil), stub.tasks...)
	app.CacheQueue = []service.CacheQueueTask{
		{ID: "cq_1", Site: "bilibili", User: "20001", UserName: "测试UP", List: "videos", ListLabel: "投稿", Status: service.CacheQueueRunning, StatusText: "进行中"},
		{ID: "cq_done", Site: "bilibili", User: "20002", UserName: "已完成UP", List: "videos", ListLabel: "投稿", Status: service.CacheQueueCompleted, StatusText: "已完成"},
	}
	app.LiveRooms = []live.RoomState{
		{
			RoomID:      9201,
			UID:         20201,
			Uname:       "正在录制主播",
			Title:       "实时录播",
			LiveStatus:  1,
			Recording:   true,
			QualityName: "原画",
			Codec:       "avc",
			Incidents: []library.LiveIncident{
				{Kind: library.LiveIncidentLostSegments, At: recentStart, LostSegments: 2, DurationSec: 4, Count: 1},
			},
		},
	}
	app.Transfer.Jobs = []service.TransferJob{
		{ID: "tr_run", Kind: "import", Status: service.TransferRunning, Source: `D:\import\a`},
		{ID: "tr_done", Kind: "export", Status: service.TransferDone, Target: `D:\export\b`},
	}
	app.liveLibItemsCacheRev = 0
	app.Navigate(RouteTasks)
	app.refreshCacheConflicts()

	// Verify DEC-072: rail "任务" badge counts ONLY queued/running downloads (1),
	// NOT cache queues, live recordings, conflicts, failed tasks, incidents, or transfers.
	if got := app.activeWorkCount(); got != 1 {
		t.Fatalf("activeWorkCount() = %d, want 1", got)
	}
	// 进行中 = 1 active download + 1 unfinished queue = 2.
	if got := app.activeTasksSectionCount(); got != 2 {
		t.Fatalf("activeTasksSectionCount() = %d, want 2", got)
	}
	// 直播 = 1 active recording.
	if got := app.activeLiveRecordingCount(); got != 1 {
		t.Fatalf("activeLiveRecordingCount() = %d, want 1", got)
	}
	// 待处理 = 1 conflict + 2 retryable tasks (failed + network-paused, excluding user-paused) + 2 recent live incidents (1 active + 1 <7d ended, excluding >7d) = 5.
	if got := app.attentionItemCount(); got != 5 {
		t.Fatalf("attentionItemCount() = %d, want 5 (1 conflict + 2 retryable + 2 recent incidents)", got)
	}
	// 记录 = 1 active transfer.
	if got := app.activeTransferCount(); got != 1 {
		t.Fatalf("activeTransferCount() = %d, want 1", got)
	}

	tt := ui.NewTester(app.View, 1280, 960)
	tt.Frame()

	for _, wantText := range []string{"进行中（2）", "直播（1）", "待处理（5）", "记录（1）"} {
		if !tt.HasText(wantText) {
			t.Fatalf("missing sidebar section %q", wantText)
		}
	}
	// Legacy 7-section sidebar items must not exist.
	for _, gone := range []string{"任务侧栏：下载任务", "任务侧栏：缓存队列", "任务侧栏：内容冲突", "任务侧栏：录制异常", "任务侧栏：检查计划", "任务侧栏：导入导出"} {
		if _, ok := tt.Find(gone); ok {
			t.Fatalf("legacy sidebar item %q must not exist", gone)
		}
	}

	// Header action "检查画质更新" appears on 进行中, hidden on other sections.
	if _, ok := tt.Find("检查画质升级"); !ok {
		t.Fatal("expected 检查画质更新 header action on 进行中")
	}

	// Switch to 待处理: verifies all 3 aggregated groups render together.
	if err := tt.Click("任务侧栏：待处理"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("检查画质升级"); ok {
		t.Fatal("检查画质更新 header action must be hidden on 待处理")
	}
	for _, wantLabel := range []string{
		"待处理分节：内容冲突",
		"内容冲突卡片：c_bili",
		"待处理分节：失败下载",
		"待处理：批量重试",
		"待处理下载卡片：t_fail",
		"待处理下载卡片：t_pause_net",
		"待处理分节：录制异常",
		"录制异常场次：bilibili:live:9201",
		"录制异常场次：" + endedLive.ItemKey,
	} {
		if _, ok := tt.Find(wantLabel); !ok {
			t.Fatalf("待处理 missing %q", wantLabel)
		}
	}
	if _, ok := tt.Find("待处理下载卡片：t_pause_user"); ok {
		t.Fatal("user-paused task must NOT appear in 待处理")
	}
	if tt.HasText("超期录制异常") {
		t.Fatal(">7d recording incident must NOT appear in 待处理")
	}
	if !tt.HasText("因网络中断暂停，可继续下载") {
		t.Fatal("error-paused task should explain pause reason")
	}

	// Click inline remove on failed task inside 待处理 -> shows inline confirm.
	if err := tt.Click("移除任务：t_fail"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("确认移除任务：t_fail"); !ok {
		t.Fatal("expected inline remove confirm for failed task in 待处理")
	}
	if err := tt.Click("取消移除任务：t_fail"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Clicking ended incident session header opens /library/item, and Back returns cleanly to 待处理!
	if err := tt.Click("点击打开异常录像：" + endedLive.ItemKey); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLibraryItem {
		t.Fatalf("expected click on ended incident session to open /library/item, got %q", app.CurrentRoute)
	}
	if err := tt.Click("返回"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteTasks || app.effectiveTasksSection() != TasksSectionAttention {
		t.Fatalf("expected Back from /library/item to return to Tasks attention, got route=%q sec=%q", app.CurrentRoute, app.effectiveTasksSection())
	}

	// Clicking "查看正在录制" on active incident session jumps to "直播".
	if err := tt.Click("查看异常正在录制：bilibili:live:9201"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.effectiveTasksSection() != TasksSectionLive {
		t.Fatalf("expected 查看正在录制 to jump to live, got %q", app.effectiveTasksSection())
	}

	// selectTasksTransfers routes cleanly to "记录" ("records").
	app.selectTasksTransfers()
	app.Transfer.Jobs = []service.TransferJob{
		{ID: "tr_run", Kind: "import", Status: service.TransferRunning, Source: `D:\import\a`},
		{ID: "tr_done", Kind: "export", Status: service.TransferDone, Target: `D:\export\b`},
	}
	tt.Frame()
	if app.effectiveTasksSection() != TasksSectionRecords {
		t.Fatalf("selectTasksTransfers() sec = %q, want records", app.effectiveTasksSection())
	}
	if _, ok := tt.Find("导入导出任务：tr_run"); !ok {
		t.Fatal("expected transfer job card in 记录 section")
	}

	// Verify 1280 / 1000 / 720 zero horizontal overflow across all 4 sections.
	for _, sec := range []string{TasksSectionActive, TasksSectionLive, TasksSectionAttention, TasksSectionRecords} {
		app.TasksSection = sec
		for _, w := range []int{1280, 1000, 720} {
			tw := ui.NewTester(app.View, w, 860)
			tw.Frame()
			if w < 1000 {
				for _, seg := range []string{"任务分段：进行中", "任务分段：直播", "任务分段：待处理", "任务分段：记录"} {
					r, ok := tw.Find(seg)
					if !ok {
						t.Fatalf("sec=%s width=%d missing %q", sec, w, seg)
					}
					if r.X < -0.5 || r.X+r.W > float32(w)+0.5 || r.W <= 0 || r.H <= 0 {
						t.Fatalf("sec=%s width=%d %q overflowed: %+v", sec, w, seg, r)
					}
				}
			} else {
				for _, item := range []string{"任务侧栏：进行中", "任务侧栏：直播", "任务侧栏：待处理", "任务侧栏：记录"} {
					r, ok := tw.Find(item)
					if !ok {
						t.Fatalf("sec=%s width=%d missing %q", sec, w, item)
					}
					if r.X < -0.5 || r.X+r.W > float32(w)+0.5 || r.W <= 0 || r.H <= 0 {
						t.Fatalf("sec=%s width=%d %q overflowed: %+v", sec, w, item, r)
					}
				}
			}
			center, ok := tw.Find("中栏内容")
			if !ok || center.X < -0.5 || center.X+center.W > float32(w)+0.5 {
				t.Fatalf("sec=%s width=%d center column overflowed: ok=%v rect=%+v", sec, w, ok, center)
			}
		}
	}
}

func TestSubscriptionsSidebarAndNarrowCheckPlanNavigation(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	sub, err := app.backend.CreateSubscription("测试订阅", "", []subscription.AccountRef{
		{Site: "bilibili", UID: "6001", Name: "检查计划测试UP"},
	}, subscription.SubRules{ScheduledCheck: true})
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()

	// 1280px sidebar: 全部订阅 -> 全部账号 -> 检查计划 -> 订阅详情 (mutually exclusive).
	app.Navigate(RouteSubscriptions)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	for _, lbl := range []string{"订阅侧栏：全部订阅", "订阅侧栏：全部账号", "订阅侧栏：检查计划"} {
		if _, ok := tt.Find(lbl); !ok {
			t.Fatalf("missing Subscriptions sidebar item %q", lbl)
		}
	}
	if err := tt.Click("订阅侧栏：检查计划"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubChecksOpen || app.SubAccountsOpen || app.SubDetailOpen {
		t.Fatalf("expected only SubChecksOpen=true, got checks=%v accounts=%v detail=%v", app.SubChecksOpen, app.SubAccountsOpen, app.SubDetailOpen)
	}
	if app.routeTitle() != "检查计划" {
		t.Fatalf("routeTitle() = %q, want 检查计划", app.routeTitle())
	}
	if _, ok := tt.Find("检查计划行 bilibili:6001"); !ok {
		t.Fatal("expected check plan row for subscribed account")
	}

	// Clicking a subscription in sidebar clears SubChecksOpen and opens detail.
	if err := tt.Click("订阅侧栏：" + sub.ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.SubDetailOpen || app.SubChecksOpen || app.SubAccountsOpen {
		t.Fatalf("expected only SubDetailOpen=true, got detail=%v checks=%v accounts=%v", app.SubDetailOpen, app.SubChecksOpen, app.SubAccountsOpen)
	}

	// Narrow 720px switcher moves cleanly between 全部订阅 / 全部账号 / 检查计划 with zero overflow.
	app.SubDetailOpen = false
	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	if err := narrow.Click("订阅页检查计划"); err != nil {
		t.Fatal(err)
	}
	narrow.Frame()
	if !app.SubChecksOpen {
		t.Fatal("narrow 订阅页检查计划 click did not open Check Plan")
	}
	for _, lbl := range []string{"订阅页全部订阅", "订阅页全部账号", "订阅页检查计划", "检查计划列表"} {
		r, ok := narrow.Find(lbl)
		if !ok {
			t.Fatalf("720px Check Plan missing %q", lbl)
		}
		if r.X < -0.5 || r.X+r.W > 720.5 || r.W <= 0 || r.H <= 0 {
			t.Fatalf("720px Check Plan %q overflowed: %+v", lbl, r)
		}
	}
	if err := narrow.Click("订阅页全部账号"); err != nil {
		t.Fatal(err)
	}
	narrow.Frame()
	if !app.SubAccountsOpen || app.SubChecksOpen {
		t.Fatalf("switching narrow 全部账号 failed: accounts=%v checks=%v", app.SubAccountsOpen, app.SubChecksOpen)
	}
	if err := narrow.Click("订阅页全部订阅"); err != nil {
		t.Fatal(err)
	}
	narrow.Frame()
	if app.SubAccountsOpen || app.SubChecksOpen || app.SubDetailOpen {
		t.Fatalf("switching narrow 全部订阅 failed: detail=%v accounts=%v checks=%v", app.SubDetailOpen, app.SubAccountsOpen, app.SubChecksOpen)
	}
}
