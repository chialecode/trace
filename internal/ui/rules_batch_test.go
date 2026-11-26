package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type tagBatchSpyBackend struct {
	service.BackendAPI
	perTagCalls   int
	lastTagID     string
	lastTagSubIDs []string
	multiCalls    int
	lastMultiMode string
	lastMultiTags []string
	lastMultiSubs []string
	forceErr      error
}

func (s *tagBatchSpyBackend) SetTagSubscriptions(tagID string, subIDs []string) error {
	s.perTagCalls++
	s.lastTagID = tagID
	s.lastTagSubIDs = append([]string(nil), subIDs...)
	if s.forceErr != nil {
		return s.forceErr
	}
	return s.BackendAPI.SetTagSubscriptions(tagID, subIDs)
}

func (s *tagBatchSpyBackend) BatchUpdateSubscriptionsTags(subIDs []string, mode string, tags []string) error {
	s.multiCalls++
	s.lastMultiSubs = append([]string(nil), subIDs...)
	s.lastMultiMode = mode
	s.lastMultiTags = append([]string(nil), tags...)
	if s.forceErr != nil {
		return s.forceErr
	}
	return s.BackendAPI.BatchUpdateSubscriptionsTags(subIDs, mode, tags)
}

func seedFiveSubscriptionsForTagBatch(t *testing.T, app *App) [5]subscription.Subscription {
	t.Helper()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	specs := []struct {
		name     string
		note     string
		uid      string
		acctName string
		tags     []string
	}{
		{"阿尔法", "备注日常", "5001", "AlphaOfficial", nil},
		{"贝塔", "备注二创", "5002", "BetaChannel", []string{subscription.TagScheduledCheck, subscription.TagCacheVideo}},
		{"伽马", "备注评测", "5003", "GammaLab", []string{subscription.TagScheduledCheck, subscription.TagCacheVideo}},
		{"德尔塔", "备注歌回", "5004", "DeltaSinger", []string{subscription.TagScheduledCheck, subscription.TagAutoRecord}},
		{"艾普西隆", "备注杂谈", "5005", "EpsilonTalk", []string{subscription.TagScheduledCheck, subscription.TagAutoRecord}},
	}
	var subs [5]subscription.Subscription
	for i, sp := range specs {
		created, err := app.backend.CreateSubscription(
			sp.name,
			sp.note,
			[]subscription.AccountRef{{Site: "bilibili", UID: sp.uid, Name: sp.acctName}},
			subscription.EmptySubRules(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(sp.tags) > 0 {
			created, err = app.backend.SetSubscriptionTags(created.ID, sp.tags)
			if err != nil {
				t.Fatal(err)
			}
		}
		subs[i] = created
	}
	app.RefreshData()
	return subs
}

// In Settings › 缓存标签, clicking "管理订阅…" on a tag opens a searchable
// checklist (with IME composition guard,全选/全不选), applies the tag membership
// in 1 atomic write while preserving each subscription's other tags, and syncs
// tag counts + subscription detail immediately (DEC-051 ⑧, DEC-070).
func TestCapabilityTagBatchManageSubscriptions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	spy := &tagBatchSpyBackend{BackendAPI: app.backend}
	app.backend = spy

	if err := app.backend.SetTagSubscriptions(subscription.TagCacheVideo, nil); err == nil {
		t.Fatal("expected SetTagSubscriptions before login to require authentication")
	}

	var ruleUpdatedEvents int
	unsub := app.backend.SubscribeEvents(func(ev service.Event) {
		if ev.Type == service.EventSubscriptionChecked && ev.Message == "rules_updated" {
			ruleUpdatedEvents++
		}
	})
	defer unsub()

	subs := seedFiveSubscriptionsForTagBatch(t, app)

	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if app.tagUseCount(subscription.TagCacheVideo) != 2 {
		t.Fatalf("expected 2 subscriptions with cache_video initially, got %d", app.tagUseCount(subscription.TagCacheVideo))
	}

	if err := tt.Click("管理标签订阅：" + subscription.TagCacheVideo); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.RuleBatch.Open || !tt.HasText("管理「自动缓存视频」的订阅") {
		t.Fatalf("per-tag batch dialog did not open with expected title; texts=%v", tt.Texts())
	}
	if !tt.HasText("已选 2 个") || !tt.HasText("应用到 2 个订阅") {
		t.Fatalf("expected 2 pre-selected subscriptions, texts=%v", tt.Texts())
	}
	if !tt.HasText("无缓存标签") || !tt.HasText("定时检查 · 开播录制") {
		t.Fatalf("expected current tag summaries in checklist rows, texts=%v", tt.Texts())
	}

	// Search box: IME preedit must not prematurely filter out Chinese rows,
	// while committed queries filter by note and account nickname.
	if err := tt.Click("规则订阅搜索框"); err != nil {
		t.Fatal(err)
	}
	tt.Compose("gehui", 5)
	tt.Frame()
	if !tt.HasText("阿尔法") || !tt.HasText("艾普西隆") {
		t.Fatal("IME composition preedit should not filter out subscriptions before commit")
	}
	tt.Compose("", 0)
	tt.Type("备注歌回")
	tt.Frame()
	if !tt.HasText("德尔塔") || tt.HasText("阿尔法") {
		t.Fatalf("expected note search to filter to 德尔塔, texts=%v", tt.Texts())
	}
	if err := tt.Click("清空规则订阅搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if err := tt.Click("规则订阅搜索框"); err != nil {
		t.Fatal(err)
	}
	tt.Type("AlphaOfficial")
	tt.Frame()
	if !tt.HasText("阿尔法") || tt.HasText("贝塔") {
		t.Fatalf("expected account nickname search to filter to 阿尔法, texts=%v", tt.Texts())
	}
	if err := tt.Click("清空规则订阅搜索"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Clear all, then select 阿尔法 (had []), 贝塔 (had [sched, video]), 德尔塔 (had [sched, record]).
	// 伽马 (had [sched, video]) loses ONLY cache_video while keeping scheduled_check!
	if err := tt.Click("全不选"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("应用到 0 个订阅") {
		t.Fatalf("expected 0 selected after 全不选, texts=%v", tt.Texts())
	}
	for _, name := range []string{"阿尔法", "贝塔", "德尔塔"} {
		if err := tt.Click("勾选规则订阅：" + name); err != nil {
			t.Fatalf("click %s: %v", name, err)
		}
		tt.Frame()
	}
	if !tt.HasText("应用到 3 个订阅") {
		t.Fatalf("expected 3 selected, texts=%v", tt.Texts())
	}

	subsPath := filepath.Join(app.backend.Locations().DataDir, "subscriptions.json")
	rawBefore, err := os.ReadFile(subsPath)
	if err != nil {
		t.Fatal(err)
	}
	spy.perTagCalls = 0
	ruleUpdatedEvents = 0

	if err := tt.Click("应用到 3 个订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if app.RuleBatch.Open {
		t.Fatal("expected per-tag batch dialog to close after apply")
	}
	if spy.perTagCalls != 1 || ruleUpdatedEvents != 1 {
		t.Fatalf("expected SetTagSubscriptions once and 1 rules_updated event, got calls=%d events=%d", spy.perTagCalls, ruleUpdatedEvents)
	}
	bakAfter, err := os.ReadFile(subsPath + ".bak")
	if err != nil || !bytes.Equal(bakAfter, rawBefore) {
		t.Fatalf("expected subscriptions.json.bak to match pre-batch file (1 atomic write), err=%v", err)
	}
	if app.tagUseCount(subscription.TagCacheVideo) != 3 {
		t.Fatalf("expected cache_video count to refresh to 3 immediately, got %d", app.tagUseCount(subscription.TagCacheVideo))
	}

	gotTags := make(map[string][]string, 5)
	for _, s := range app.Subscriptions {
		gotTags[s.ID] = s.Tags
	}
	wantTags := map[string][]string{
		subs[0].ID: {subscription.TagCacheVideo},                                                             // 阿尔法 -> gained cache_video
		subs[1].ID: {subscription.TagScheduledCheck, subscription.TagCacheVideo},                             // 贝塔 -> kept both
		subs[2].ID: {subscription.TagScheduledCheck},                                                         // 伽马 -> lost cache_video, kept scheduled_check
		subs[3].ID: {subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagAutoRecord}, // 德尔塔 -> gained cache_video, kept sched+record
		subs[4].ID: {subscription.TagScheduledCheck, subscription.TagAutoRecord},                             // 艾普西隆 -> untouched
	}
	for id, want := range wantTags {
		if !slices.Equal(gotTags[id], want) {
			t.Fatalf("subscription %s tags=%v, want %v", id, gotTags[id], want)
		}
	}

	// Verify Subscription Detail reflects synced capability tags as active chips.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if err := tt.Click("选择订阅 " + subs[0].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅缓存标签：自动缓存视频"); !ok {
		t.Fatalf("subscription detail for 阿尔法 did not sync active tag chip, texts=%v", tt.Texts())
	}
	if err := tt.Click("订阅侧栏：" + subs[3].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, label := range []string{"订阅缓存标签：定时检查", "订阅缓存标签：自动缓存视频", "订阅缓存标签：开播录制"} {
		if _, ok := tt.Find(label); !ok {
			t.Fatalf("subscription detail for 德尔塔 missing active tag chip %s, texts=%v", label, tt.Texts())
		}
	}
}

// Cross-subscription multi-tag batch setup ("批量设置缓存标签") works from both
// Settings › 缓存标签 and Subscriptions › ⋯ menu, supporting add, remove, and
// replace (including clearing all tags) in 1 atomic write per operation.
func TestCapabilityTagMultiBatchAddRemoveReplaceFromSettingsAndSubscriptionsMenu(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	spy := &tagBatchSpyBackend{BackendAPI: app.backend}
	app.backend = spy

	if err := app.backend.BatchUpdateSubscriptionsTags([]string{"x"}, subscription.TagBatchAdd, []string{subscription.TagCacheVideo}); err == nil {
		t.Fatal("expected BatchUpdateSubscriptionsTags before login to require authentication")
	}

	subs := seedFiveSubscriptionsForTagBatch(t, app)

	// 1. Open from Settings › 缓存标签 ("打开批量设置订阅标签") and batch-add [自动缓存图文, 全部缓存] to 阿尔法 & 贝塔.
	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if err := tt.Click("打开批量设置订阅标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.MultiTagBatch.Open || !tt.HasText("批量设置缓存标签") {
		t.Fatalf("expected multi-tag batch dialog to open, texts=%v", tt.Texts())
	}

	if err := tt.Click("批量选择标签：自动缓存图文"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("批量选择标签：全部缓存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	for _, name := range []string{"阿尔法", "贝塔"} {
		if err := tt.Click("勾选批量订阅：" + name); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	if !tt.HasText("为 2 个订阅添加标签") {
		t.Fatalf("expected add button summary for 2 subs, texts=%v", tt.Texts())
	}
	spy.multiCalls = 0
	if err := tt.Click("确认批量设置标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if app.MultiTagBatch.Open || spy.multiCalls != 1 || spy.lastMultiMode != subscription.TagBatchAdd {
		t.Fatalf("unexpected multi-batch add result: open=%v calls=%d mode=%q", app.MultiTagBatch.Open, spy.multiCalls, spy.lastMultiMode)
	}

	// 2. Open from Subscriptions › ⋯ ("订阅更多菜单" -> "批量设置缓存标签…") and batch-remove [自动缓存图文], then batch-replace to clear 阿尔法.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if err := tt.Click("订阅更多菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.ChooseMenuItem("批量设置缓存标签…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.MultiTagBatch.Open {
		t.Fatal("expected 订阅更多菜单 item to open multi-tag batch dialog")
	}
	if err := tt.Click("批量模式：remove"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("批量选择标签：自动缓存图文"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("勾选批量订阅：贝塔"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认批量设置标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	// Reopen and use replace with 0 tags selected to clear all tags on 阿尔法 while leaving 贝塔 untouched.
	app.openMultiTagBatchDialog()
	tt.Frame()
	if err := tt.Click("批量模式：replace"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("勾选批量订阅：阿尔法"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("清空 1 个订阅的标签") {
		t.Fatalf("expected replace-empty button text 清空 1 个订阅的标签, texts=%v", tt.Texts())
	}
	if err := tt.Click("确认批量设置标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	gotTags := make(map[string][]string, 5)
	for _, s := range app.Subscriptions {
		gotTags[s.ID] = s.Tags
	}
	if len(gotTags[subs[0].ID]) != 0 {
		t.Fatalf("expected 阿尔法 cleared to empty tags, got %v", gotTags[subs[0].ID])
	}
	wantBeta := []string{subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagFullCache}
	if !slices.Equal(gotTags[subs[1].ID], wantBeta) {
		t.Fatalf("贝塔 tags = %v, want %v", gotTags[subs[1].ID], wantBeta)
	}
}

func TestCapabilityTagBatchDialogsErrorRetentionAndDismissShortcuts(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	spy := &tagBatchSpyBackend{BackendAPI: app.backend}
	app.backend = spy

	_ = seedFiveSubscriptionsForTagBatch(t, app)

	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if err := tt.Click("管理标签订阅：" + subscription.TagCacheVideo); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("全选"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("应用到 5 个订阅") {
		t.Fatalf("expected 全选 to select all 5 subscriptions, texts=%v", tt.Texts())
	}

	spy.forceErr = errors.New("模拟写入失败：磁盘空间不足")
	if err := tt.Click("应用到 5 个订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.RuleBatch.Open || !tt.HasText("模拟写入失败：磁盘空间不足") || !tt.HasText("应用到 5 个订阅") {
		t.Fatalf("expected dialog to stay open, show error, and keep 5 selected; open=%v texts=%v", app.RuleBatch.Open, tt.Texts())
	}
	spy.forceErr = nil

	// Escape closes per-tag modal without applying.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.RuleBatch.Open {
		t.Fatal("expected Escape to close the per-tag batch dialog")
	}
	if app.tagUseCount(subscription.TagCacheVideo) != 2 {
		t.Fatal("dismissing dialog should not alter tag membership")
	}

	// Backdrop click outside the centered multi-tag dialog closes it cleanly.
	if err := tt.Click("打开批量设置订阅标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.MultiTagBatch.Open {
		t.Fatal("expected multi-tag batch dialog to open")
	}
	tt.ClickAt(12, 12)
	tt.Frame()
	if app.MultiTagBatch.Open {
		t.Fatal("expected clicking backdrop to dismiss multi-tag batch dialog")
	}
}

// DEC-038 ⑤ & Issue #101: Settings › 缓存标签 omits the top explanatory paragraph
// and all 7 per-tag description subtitles while keeping the right-aligned
// "批量设置标签…" button, all 7 tag names, subscription counts, and "管理订阅…"
// actions functional across desktop and narrow widths.
func TestSettingsCacheTagsOmitsExplanatorySubtitles(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	_ = seedFiveSubscriptionsForTagBatch(t, app)

	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)

	allTags := subscription.AllCapabilityTags()
	if len(allTags) != 7 {
		t.Fatalf("expected 7 capability tags, got %d", len(allTags))
	}

	forbiddenTop := []string{
		"一个标签对应一项缓存能力",
		"新增订阅默认没有任何标签",
		"可在订阅详情单独开关",
	}

	wantCounts := map[string]int{
		subscription.TagScheduledCheck: 4,
		subscription.TagCacheVideo:     2,
		subscription.TagCacheDraw:      0,
		subscription.TagCacheWord:      0,
		subscription.TagCacheForward:   0,
		subscription.TagFullCache:      0,
		subscription.TagAutoRecord:     2,
	}

	for _, width := range []int{1280, 720} {
		tt := ui.NewTester(app.View, width, 860)
		tt.Frame()

		for _, phrase := range forbiddenTop {
			if tt.HasText(phrase) {
				t.Fatalf("width=%d: unexpected top explanatory subtitle %q in texts=%v", width, phrase, tt.Texts())
			}
		}

		batchRect, ok := tt.Find("打开批量设置订阅标签")
		if !ok || batchRect.W <= 0 || batchRect.H <= 0 || batchRect.X+batchRect.W > float32(width)+0.5 {
			t.Fatalf("width=%d: top batch button not visible/in-bounds: ok=%v rect=%+v", width, ok, batchRect)
		}
		if !tt.HasText("批量设置标签…") {
			t.Fatalf("width=%d: missing 批量设置标签… button text", width)
		}

		for _, tag := range allTags {
			if tt.HasText(tag.Description) {
				t.Fatalf("width=%d: tag %s description subtitle should not render on Settings › 缓存标签: %q", width, tag.ID, tag.Description)
			}
			rowRect, ok := tt.Find("缓存标签行：" + tag.ID)
			if !ok || rowRect.W <= 0 || rowRect.H <= 0 || rowRect.X+rowRect.W > float32(width)+0.5 {
				t.Fatalf("width=%d: tag row %s missing or out of bounds: ok=%v rect=%+v", width, tag.ID, ok, rowRect)
			}
			if !tt.HasText(tag.Name) {
				t.Fatalf("width=%d: missing tag name %q", width, tag.Name)
			}
			countRect, ok := tt.Find("标签订阅数：" + tag.ID)
			if !ok || countRect.W <= 0 || countRect.H <= 0 || countRect.Y < rowRect.Y-1 || countRect.Y+countRect.H > rowRect.Y+rowRect.H+1 {
				t.Fatalf("width=%d: missing or misplaced count label for %s: ok=%v rect=%+v row=%+v", width, tag.ID, ok, countRect, rowRect)
			}
			if got := app.tagUseCount(tag.ID); got != wantCounts[tag.ID] {
				t.Fatalf("width=%d: tag %s count=%d, want %d", width, tag.ID, got, wantCounts[tag.ID])
			}
			manageRect, ok := tt.Find("管理标签订阅：" + tag.ID)
			if !ok || manageRect.W <= 0 || manageRect.H <= 0 || manageRect.X+manageRect.W > float32(width)+0.5 {
				t.Fatalf("width=%d: manage button for %s missing or out of bounds: ok=%v rect=%+v", width, tag.ID, ok, manageRect)
			}
			diffRight := (batchRect.X + batchRect.W) - (manageRect.X + manageRect.W)
			if diffRight < -2 || diffRight > 2 {
				t.Fatalf("width=%d: top batch button right edge (%.1f) not aligned with %s manage button right edge (%.1f)",
					width, batchRect.X+batchRect.W, tag.ID, manageRect.X+manageRect.W)
			}
		}

		// Verify both interactive entry points still work cleanly.
		if err := tt.Click("打开批量设置订阅标签"); err != nil {
			t.Fatalf("width=%d: click batch button: %v", width, err)
		}
		tt.Frame()
		if !app.MultiTagBatch.Open {
			t.Fatalf("width=%d: expected multi-tag batch dialog to open", width)
		}
		app.closeMultiTagBatchDialog()
		tt.Frame()

		if err := tt.Click("管理标签订阅：" + subscription.TagCacheVideo); err != nil {
			t.Fatalf("width=%d: click manage button: %v", width, err)
		}
		tt.Frame()
		if !app.RuleBatch.Open || app.RuleBatch.TagID != subscription.TagCacheVideo {
			t.Fatalf("width=%d: expected per-tag dialog for cache_video to open", width)
		}
		app.closeRuleBatchDialog()
		tt.Frame()
	}
}
