package main

import (
	"context"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/chialecode/trace/internal/transfer"
	"github.com/chialecode/trace/internal/ui"
)

// 设置 › 远程, 导入导出 and 导入订阅 screenshot fixtures: a synthetic remote
// and synthetic scans answered from memory; nothing connects anywhere.

type remoteFixture struct {
	conn    *service.RemoteConnectionInfo
	view    *service.RemoteView
	err     error
	jobs    []service.TransferJob
	plan    *transfer.Plan
	planJob string
}

func (b *screenshotBackend) RemoteConnection() (service.RemoteConnectionInfo, bool) {
	if b.remote.conn != nil {
		return *b.remote.conn, true
	}
	return service.RemoteConnectionInfo{}, false
}

func (b *screenshotBackend) RemoteStatus(context.Context) (service.RemoteView, error) {
	if b.remote.err != nil {
		return service.RemoteView{}, b.remote.err
	}
	if b.remote.view != nil {
		return *b.remote.view, nil
	}
	return service.RemoteView{}, service.ErrNoRemote
}

func (b *screenshotBackend) TransferJobs() []service.TransferJob {
	return append([]service.TransferJob(nil), b.remote.jobs...)
}

func (b *screenshotBackend) ImportPlan(jobID string) (transfer.Plan, bool) {
	if b.remote.plan != nil && jobID == b.remote.planJob {
		return *b.remote.plan, true
	}
	return transfer.Plan{}, false
}

func shotRemoteConn() *service.RemoteConnectionInfo {
	return &service.RemoteConnectionInfo{ID: "remote-shot", Name: "东京 VPS", URL: "https://203.0.113.10:47380",
		Fingerprint: "3f9a1c0e5b7d2468ace013579bdf2468ace013579bdf2468ace013579bdf2468"}
}

func shotRemoteView() *service.RemoteView {
	now := time.Now().UTC()
	at := func(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }
	return &service.RemoteView{
		Connection: *shotRemoteConn(),
		LocalHash:  "a1b2c3", InSync: true, FetchedAt: at(0),
		Status: remote.Status{
			Version: "0.5.0", Protocol: remote.ProtocolVersion,
			Node:      remote.Node{ID: "node-shot", Name: "东京 VPS"},
			StartedAt: at(52 * time.Hour), Now: at(0),
			Disk: remote.DiskStatus{Free: 38 << 30, Total: 80 << 30},
			Sites: []remote.SiteStatus{
				{Site: "bilibili", State: remote.SiteValid, Uname: "TRACE演示账号", CookieSyncedAt: at(26 * time.Hour)},
				{Site: "douyin", State: remote.SiteNone},
			},
			Subscriptions: remote.SubscriptionsInfo{Subscriptions: 4, Accounts: 9, SyncedAt: at(3 * time.Hour), Hash: "a1b2c3"},
			Checks: []remote.CheckRow{
				{Site: "bilibili", UID: "20001", Name: "影视飓风", Subscription: "科技", LastCheckedAt: at(20 * time.Minute), LastResult: "2 条新内容", NextAt: now.Add(40 * time.Minute).Format(time.RFC3339), Scheduled: true},
			},
			Cache: remote.CacheStatus{Running: 1, Queued: 3, Recent: []remote.TaskRow{
				{Site: "bilibili", Title: "合成视频：城市夜景延时", Status: "completed", At: at(30 * time.Minute)},
			}},
			Recordings: []remote.RecordingRow{
				{Site: "bilibili", Name: "数字影像志", Title: "周末直播", StartedAt: at(75 * time.Minute), DurationSec: 4500, Bytes: 1800 << 20, Segments: 3},
			},
			Upload: remote.UploadStatus{Mode: remote.UploadRclone, Target: "gdrive:trace", DeleteAfter: true, QueuedItems: 2, QueuedBytes: 900 << 20,
				Uploading: "bilibili/20001/video/BV1shot", UploadingSize: 420 << 20, TodayBytes: 12 << 30, TodayFiles: 37},
		},
	}
}

func shotImportPlan() *transfer.Plan {
	e := func(cat transfer.Category, title, key string) transfer.Entry {
		return transfer.Entry{Category: cat, ItemKey: key, Site: "bilibili", Kind: "video", Title: title, Author: "影视飓风", CachedBy: "东京 VPS",
			SourceDir: `D:\Downloads\trace\library\bilibili\20001\video\` + key, CopyFiles: 2, CopyBytes: 640 << 20}
	}
	conflict := e(transfer.CategoryConflict, "合成视频：同一条目两边内容不同", "bilibili:video:BV1conf")
	conflict.Conflicts = []string{"P01.mp4"}
	other := e(transfer.CategoryConflict, "合成视频：本地在另一个作者文件夹", "bilibili:video:BV1other")
	other.OtherFolder = true
	partial := e(transfer.CategoryIncomplete, "合成直播：网盘还没传完", "bilibili:live:42:100")
	partial.Unverified = 1
	same := e(transfer.CategorySame, "合成视频：已经在媒体库", "bilibili:video:BV1same")
	same.CopyFiles, same.CopyBytes = 0, 0
	bad := transfer.Entry{Category: transfer.CategoryUnrecognized, SourceDir: `D:\Downloads\trace\misc`, Reason: "没有 item.json"}
	entries := []transfer.Entry{
		e(transfer.CategoryNew, "合成视频：城市夜景延时", "bilibili:video:BV1new1"),
		e(transfer.CategorySupplement, "合成直播：补上新录的段", "bilibili:live:43:200"),
		conflict, other, partial, same, bad,
	}
	counts := map[transfer.Category]int{}
	for _, x := range entries {
		counts[x.Category]++
	}
	return &transfer.Plan{Root: `D:\Downloads\trace`, ScannedAt: time.Now().UTC().Format(time.RFC3339), Entries: entries, Counts: counts}
}

func shotTransferJobs() []service.TransferJob {
	now := time.Now().UTC().Format(time.RFC3339)
	return []service.TransferJob{
		{ID: "import-run", Kind: "import", Status: service.TransferRunning, Source: `D:\Downloads\trace`, Target: `D:\TRACE\cache`, StartedAt: now,
			Progress: transfer.Progress{Phase: "copy", Done: 3, Total: 7, Bytes: 1200 << 20, TotalBytes: 3 << 30, Current: "合成视频：城市夜景延时"}, SpeedBps: 85 << 20},
		{ID: "import-ready", Kind: "import", Status: service.TransferReady, Source: `E:\网盘下载\trace`, StartedAt: now,
			Counts: map[transfer.Category]int{transfer.CategoryNew: 12, transfer.CategoryConflict: 2}},
		{ID: "import-done", Kind: "import", Status: service.TransferDone, Source: `D:\Downloads\trace-1118`, StartedAt: now, FinishedAt: now,
			Summary: &transfer.Summary{Imported: 18, Supplemented: 3, Skipped: 5, Conflicts: 1, Incomplete: 1, Files: 64, Bytes: 9 << 30}},
		{ID: "export-done", Kind: "export", Status: service.TransferDone, Source: `D:\TRACE\cache`, Target: `F:\trace-export`, StartedAt: now, FinishedAt: now,
			Export: &transfer.ExportSummary{Items: 40, Files: 132, Bytes: 21 << 30}},
	}
}

func shotSubPreview() *subscription.ImportPreview {
	return &subscription.ImportPreview{
		New: []subscription.ImportSub{{ID: "s-new", Name: "纪录片", Accounts: []string{"bilibili:30001", "bilibili:30002"}, RuleName: "自动存视频与图文"}},
		Existing: []subscription.ImportExistingAccount{{Site: "bilibili", UID: "20001", Name: "影视飓风", ImportedSubID: "s-tech", ImportedSub: "科技",
			LocalSubID: "s-local", LocalSub: "数码"}},
		Rules: []subscription.ImportRuleConflict{{ID: "rule-auto", LocalName: "自动存视频与图文", LocalSummary: "视频 1080P · 图文",
			ImportedName: "自动存视频与图文", ImportedSummary: "视频最高画质 · 图文 · 直播录制"}},
		NewRules: 1, Unchanged: 2,
	}
}

func remoteShotScenes(app *ui.App, fb *screenshotBackend) []sceneSpec {
	reset := func() {
		fb.remote = remoteFixture{}
		app.PlayerOverlay.Open, app.ImageViewer.Open = false, false
		app.Remote = ui.RemoteUIState{}
		app.Transfer = ui.TransferUIState{}
		app.SubFile = ui.SubFileUIState{}
	}
	settings := func() {
		app.SettingsCategory = "remote"
		app.Navigate(ui.RouteSettings)
	}
	return []sceneSpec{
		{Name: "settings-remote", Setup: func() {
			reset()
			settings()
		}, RequiredLabels: []string{"连接远程", "远程连接码", "切换远程连接方式"}},
		{Name: "settings-remote-connected", Setup: func() {
			reset()
			fb.remote.conn, fb.remote.view = shotRemoteConn(), shotRemoteView()
			app.Remote.View, app.Remote.FetchedAt = fb.remote.view, time.Now()
			settings()
		}, RequiredLabels: []string{"同步订阅"}, ForbiddenLabels: []string{"远程错误"}},
		{Name: "settings-remote-unreachable", Setup: func() {
			reset()
			fb.remote.conn = shotRemoteConn()
			fb.remote.err = &remote.Error{Kind: remote.KindUnreachable, Message: "连接超时，请确认 VPS 正在运行、端口已放行"}
			app.Remote.Err, app.Remote.FetchedAt = fb.remote.err.Error(), time.Now()
			settings()
		}, RequiredLabels: []string{"远程错误"}},
		{Name: "library-import-preview", Setup: func() {
			reset()
			fb.remote.plan, fb.remote.planJob = shotImportPlan(), "import-shot"
			fb.remote.jobs = []service.TransferJob{{ID: "import-shot", Kind: "import", Status: service.TransferReady, Source: `D:\Downloads\trace`, Counts: fb.remote.plan.Counts}}
			app.Navigate(ui.RouteLibrary)
			app.Transfer.ImportPath = `D:\Downloads\trace`
			app.Transfer.JobID = "import-shot"
			app.Transfer.Jobs = fb.remote.jobs
			app.Transfer.Choices = map[string]string{"bilibili:video:BV1conf": library.MergeUnion}
			app.Transfer.ImportPage = true
			p, _ := fb.ImportPlan("import-shot")
			app.Transfer.Plan = &p
		}, RequiredLabels: []string{"返回媒体库", "导入文件夹路径", "全部冲突的处理", "冲突处理：bilibili:video:BV1conf", "导入后删除来源开关", "开始导入"}},
		{Name: "tasks-transfers", Setup: func() {
			reset()
			fb.remote.jobs = shotTransferJobs()
			app.Navigate(ui.RouteTasks)
			app.Transfer.Jobs = fb.remote.jobs
			app.TasksSection = "transfers"
		}, RequiredLabels: []string{"导入导出任务：import-run", "取消导入导出：import-run", "查看导入预览：import-ready", "导入导出任务：export-done"}},
		{Name: "subscriptions-import-preview", Setup: func() {
			reset()
			app.Navigate(ui.RouteSubscriptions)
			app.SubDetailOpen = false
			app.SubFile = ui.SubFileUIState{ImportOpen: true, ImportPath: `D:\backup\trace-subscriptions.json`, Preview: shotSubPreview(), UseImported: map[string]bool{}}
		}, RequiredLabels: []string{"导入订阅对话框", "合并进已有订阅开关", "用导入的规则：rule-auto", "确认导入订阅"}},
	}
}
