package ui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// M4-K33 & M4-K35:
// 1. Settings "订阅与直播" does not render "同时录制数".
// 2. When N == 0 recent incident sessions exist, Tasks sidebar & narrow < 1000 segmented bar show "录制异常" (no "(0)") and empty state "最近没有录制异常".
// 3. When active + recent (< 7 days) + older (> 7 days) sessions have incidents:
//   - Sidebar shows "录制异常（2）" (counts active + <7d, excludes >7d); icon rail "任务" badge remains 0 (only counts download tasks).
//   - "任务 › 直播录制" shows clickable "⚠ N 处异常" on both active and recently finished recording rows.
//   - Leaving "直播录制" for "录制异常" immediately calls closeLivePreview() and releases the .part file handle on Windows.
//   - "录制异常" page renders per-session streamer/room, site badge, start time, incident occurrence time, Chinese type explanation, and affected duration/segments.
//   - Finished recordings open offline local detail (/library/item) with 0 network requests.
//   - Narrow window (< 1000) top segmented bar exposes "录制异常（2）" and switches cleanly.
func TestSettingsNoRecordingLimitAndTasksLiveIncidentsUI_M4K33_M4K35(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	posted := make(chan func(), 64)
	app.dispatch = func(fn func()) { posted <- fn }
	seedUIFeed(t, app, 0)

	rawBackend := app.backend.(*service.Backend)
	spy := &spyOfflineLibraryBackend{BackendAPI: rawBackend}
	app.backend = spy

	// --- 1. M4-K33: Settings "订阅与直播" has NO "同时录制数" ---
	app.Navigate(RouteSettings)
	app.SettingsCategory = "subscription"
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if !tt.HasText("定时检查更新") {
		t.Fatal("expected Settings subscription category to render 定时检查更新")
	}
	if tt.HasText("同时录制数") {
		t.Fatal("M4-K33: Settings '订阅与直播' must NOT render '同时录制数'")
	}

	// --- 2. M4-K35: N == 0 -> "录制异常" without "(0)" + clear empty state ---
	app.Navigate(RouteTasks)
	tt.Frame()

	if !tt.HasText("录制异常") {
		t.Fatal("expected Tasks sidebar to render '录制异常'")
	}
	if tt.HasText("录制异常（0）") || tt.HasText("录制异常(0)") {
		t.Fatal("M4-K35: when N == 0, sidebar must show '录制异常' WITHOUT '(0)'")
	}
	if err := tt.Click("任务侧栏：录制异常"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("录制异常空状态"); !ok || !tt.HasText("最近没有录制异常") {
		t.Fatal("expected empty state 最近没有录制异常 when no sessions have incidents")
	}

	// --- 3. Seed active recording with live preview .part + recent finished (<7d) + old finished (>7d) ---
	now := time.Now().UTC()
	recentStart := now.Add(-2 * time.Hour).Format(time.RFC3339)
	recentEnd := now.Add(-90 * time.Minute).Format(time.RFC3339)
	oldStart := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)
	oldEnd := now.Add(-10*24*time.Hour + 30*time.Minute).Format(time.RFC3339)

	// Finished session #1 (within 7 days, Douyin, 2 incidents)
	app.backend = rawBackend
	recentItem := seedLibraryControlItem(t, app, library.ItemMetadata{
		Version:  1,
		ID:       "79001",
		Site:     "douyin",
		Kind:     "live",
		RawID:    "79001",
		Status:   "partial",
		Title:    "周末户外歌会",
		URL:      "https://live.douyin.com/79001",
		PubTs:    now.Add(-2 * time.Hour).Unix(),
		CachedAt: recentEnd,
		Duration: 1800,
		Author: library.ItemAuthor{
			Site: "douyin",
			UID:  "sec_dy_inc",
			Name: "抖音歌手B",
		},
		Media: []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 1800}},
		Live: &library.LiveRecordingInfo{
			RoomIDStr:   "79001",
			WebRID:      "79001",
			StartedAt:   recentStart,
			EndedAt:     recentEnd,
			Source:      "manual",
			Codec:       "avc",
			QualityName: "原画",
			Incidents: []library.LiveIncident{
				{
					Kind:        library.LiveIncidentReconnect,
					At:          recentStart,
					EndedAt:     recentStart,
					DurationSec: 20,
					Count:       1,
					Reason:      "断流后已重新取流",
				},
				{
					Kind:    library.LiveIncidentConvertFailed,
					At:      recentEnd,
					EndedAt: recentEnd,
					Count:   1,
					Part:    "part-002.mp4.part",
					Reason:  "直播录像转换失败：缺少初始化分片",
				},
			},
		},
	})

	// Finished session #2 (10 days ago > 7 days -> must NOT be counted in sidebar badge N!)
	_ = seedLibraryControlItem(t, app, library.ItemMetadata{
		Version:  1,
		ID:       "9999-old",
		Site:     "bilibili",
		Kind:     "live",
		RawID:    "9999-old",
		Status:   "partial",
		Title:    "十天前旧直播",
		URL:      "https://live.bilibili.com/9999",
		PubTs:    now.Add(-10 * 24 * time.Hour).Unix(),
		CachedAt: oldEnd,
		Author:   library.ItemAuthor{Mid: 10999, UID: "10999", Name: "旧直播UP"},
		Media:    []library.MediaFile{{Role: "live_segment", Path: "part-001.mp4", Duration: 60}},
		Live: &library.LiveRecordingInfo{
			RoomID:    9999,
			StartedAt: oldStart,
			EndedAt:   oldEnd,
			Source:    "manual",
			Incidents: []library.LiveIncident{
				{
					Kind:    library.LiveIncidentDisk,
					At:      oldEnd,
					EndedAt: oldEnd,
					Count:   1,
					Reason:  "磁盘空间不足 1 GiB，已停止录制",
				},
			},
		},
	})
	app.backend = spy
	_ = recentItem

	// Active recording with real .part fMP4 file + 2 incidents (lost_segments 3 segs + slow 35s)
	activeRecDir := t.TempDir()
	partPath := filepath.Join(activeRecDir, "part-001.mp4.part")
	initAndFirst, _ := buildGrowingVideoPartChunks(t, 1)
	writerFile, err := os.OpenFile(partPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = writerFile.Close() }()
	if _, err := writerFile.Write(initAndFirst); err != nil {
		t.Fatal(err)
	}
	_ = writerFile.Sync()

	activeStart := now.Add(-15 * time.Minute).Format(time.RFC3339)
	app.LiveRooms = []live.RoomState{
		{
			RoomID:           9101,
			UID:              20101,
			Uname:            "技术主播_Alpha",
			Title:            "高码率直播测试",
			LiveStatus:       1,
			Quality:          10000,
			QualityName:      "原画",
			Codec:            "avc",
			Transport:        "hls_fmp4",
			Recording:        true,
			RecordSegments:   1,
			RecordPartIndex:  1,
			RecordPartPath:   partPath,
			RecordPartReady:  true,
			RecordItemDir:    activeRecDir,
			PreviewSupported: true,
			Incidents: []library.LiveIncident{
				{
					Kind:         library.LiveIncidentLostSegments,
					At:           activeStart,
					EndedAt:      activeStart,
					DurationSec:  6,
					LostSegments: 3,
					Count:        1,
				},
				{
					Kind:        library.LiveIncidentSlow,
					At:          activeStart,
					EndedAt:     now.Format(time.RFC3339),
					DurationSec: 35,
					Count:       1,
				},
			},
		},
	}
	app.liveLibItemsCacheRev = 0
	app.selectTasksLive()
	tt.Frame()

	// Sidebar shows "录制异常（2）" (1 active + 1 <7d finished; excludes 10d-old session!)
	if !tt.HasText("录制异常（2）") {
		t.Fatal("expected Tasks sidebar to show '录制异常（2）' (active + <7d finished, excluding >7d)")
	}
	// Icon rail "任务" badge only counts queued/running downloads (0 here!)
	if got := app.activeWorkCount(); got != 0 {
		t.Fatalf("expected icon rail activeWorkCount() == 0 when only live recordings/incidents exist, got %d", got)
	}

	// Both active row and recently finished row on "任务 › 直播录制" show "⚠ 2 处异常"!
	if _, ok := tt.Find("录制行异常：bilibili:live:9101"); !ok {
		t.Fatal("expected active recording row to show '录制行异常：bilibili:live:9101'")
	}
	if _, ok := tt.Find("录制行异常：douyin:live:79001"); !ok {
		t.Fatal("expected recently finished recording row to show '录制行异常：douyin:live:79001'")
	}
	if !tt.HasText("⚠ 2 处异常") {
		t.Fatal("expected '⚠ 2 处异常' pill text on recording rows")
	}

	// --- 4. Open live preview on active .part, then switch to "录制异常" -> immediate handle release! ---
	if err := tt.Click("预览直播录制：bilibili:live:9101"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		drainPosted(posted)
		tt.Frame()
		if !app.LivePreviewLoading && app.livePreviewPlayer.Bitmap() != nil {
			break
		}
		if app.LivePreviewError != "" {
			t.Fatalf("live preview failed: %s", app.LivePreviewError)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if app.livePreviewPlayer.Bitmap() == nil {
		t.Fatal("expected live preview bitmap before switching to incidents view")
	}

	// Click "⚠ 2 处异常" on the active row -> switches to "录制异常" and immediately closes live preview!
	if err := tt.Click("录制行异常：bilibili:live:9101"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.effectiveTasksSection() != "incidents" {
		t.Fatalf("expected TasksSection == 'incidents', got %q", app.effectiveTasksSection())
	}
	if app.LivePreviewKey != "" {
		t.Fatalf("expected LivePreviewKey cleared immediately when switching to 录制异常, got %q", app.LivePreviewKey)
	}
	_ = writerFile.Close()
	renamedCheck := filepath.Join(activeRecDir, "part-001.unlocked.mp4")
	if err := os.Rename(partPath, renamedCheck); err != nil {
		t.Fatalf("switching to '录制异常' leaked .part file handle on Windows: %v", err)
	}
	if err := os.Rename(renamedCheck, partPath); err != nil {
		t.Fatal(err)
	}

	// Also verify switching via the context sidebar item "任务侧栏：录制异常" releases the live preview .part handle!
	app.selectTasksLive()
	tt.Frame()
	if err := tt.Click("预览直播录制：bilibili:live:9101"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		drainPosted(posted)
		tt.Frame()
		if !app.LivePreviewLoading && app.livePreviewPlayer.Bitmap() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := tt.Click("任务侧栏：录制异常"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.LivePreviewKey != "" {
		t.Fatalf("expected LivePreviewKey cleared when clicking 任务侧栏：录制异常, got %q", app.LivePreviewKey)
	}
	if err := os.Rename(partPath, renamedCheck); err != nil {
		t.Fatalf("clicking '任务侧栏：录制异常' leaked .part file handle on Windows: %v", err)
	}
	if err := os.Rename(renamedCheck, partPath); err != nil {
		t.Fatal(err)
	}

	// --- 5. Verify "录制异常" page content & zero-network offline录像详情 navigation ---
	for _, wantText := range []string{
		"技术主播_Alpha · 高码率直播测试",
		"抖音歌手B · 周末户外歌会",
		"开始时间 ",
		"分片丢失",
		"媒体序号跳号丢失 3 段，估算影响约 6 秒",
		"下载过慢",
		"下载速度低于流码率，持续 35 秒",
		"断流重连",
		"断流后重新取流，恢复用时 20 秒",
		"收尾转换失败",
		"part-002.mp4.part：直播录像转换失败：缺少初始化分片",
	} {
		if !tt.HasText(wantText) {
			t.Fatalf("expected '录制异常' view to render %q", wantText)
		}
	}

	spy.siteCalls.Store(0)
	if err := tt.Click("打开异常录像详情：douyin:live:79001"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.CurrentRoute != RouteLibraryItem {
		t.Fatalf("expected finished incident recording to navigate to %q (/library/item), got %q", RouteLibraryItem, app.CurrentRoute)
	}
	if got := spy.siteCalls.Load(); got != 0 {
		t.Fatalf("expected 0 site network calls when opening offline incident recording detail, got %d", got)
	}

	// --- 6. Narrow window (< 1000) top segmented selector reaches "录制异常（2）" ---
	app.Navigate(RouteTasks)
	app.selectTasksDownloads()
	narrowTT := ui.NewTester(app.View, 820, 860)
	narrowTT.Frame()
	if _, ok := narrowTT.Find("任务分段：录制异常"); !ok || !narrowTT.HasText("录制异常（2）") {
		t.Fatal("expected narrow <1000 top segmented selector to expose '录制异常（2）'")
	}
	if err := narrowTT.Click("任务分段：录制异常"); err != nil {
		t.Fatal(err)
	}
	narrowTT.Frame()
	if app.effectiveTasksSection() != "incidents" || !narrowTT.HasText("技术主播_Alpha · 高码率直播测试") {
		t.Fatal("expected narrow segmented selector click to open 录制异常 view")
	}

	// --- 7. DEC-057: every entry can be removed. A finished recording leaves
	// "最近结束的录制" (kept in ui-state, the recording stays); a finished
	// session's incident records can be deleted, an active one's cannot. ---
	app.backend = rawBackend
	app.selectTasksLive()
	tt.Frame()
	if err := tt.Click("移除最近结束的录制：douyin:live:79001"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("最近结束录制行：douyin:live:79001"); ok {
		t.Fatal("the removed recording is still under 最近结束的录制")
	}
	if _, ok := rawBackend.GetUIState().DismissedLiveSessions["douyin:live:79001"]; !ok {
		t.Fatal("the removal was not saved to ui-state.json")
	}
	if _, ok, _ := rawBackend.LibraryStore().GetItem("douyin:live:79001"); !ok {
		t.Fatal("removing it from the list deleted the recording")
	}

	app.selectTasksIncidents("")
	tt.Frame()
	if _, ok := tt.Find("删除录制异常记录：bilibili:live:9101"); ok {
		t.Fatal("a session still recording offers to delete its incident records")
	}
	if err := tt.Click("删除录制异常记录：douyin:live:79001"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("录制异常场次：douyin:live:79001"); ok {
		t.Fatal("the session whose records were deleted is still listed")
	}
	item, ok, _ := rawBackend.LibraryStore().GetItem("douyin:live:79001")
	if !ok || item.Live == nil || len(item.Live.Incidents) != 0 || len(item.Media) == 0 {
		t.Fatalf("after deleting the records: ok=%v live=%+v media=%d", ok, item.Live, len(item.Media))
	}
}
