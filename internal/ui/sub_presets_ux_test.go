package ui

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

func findSubByName(t *testing.T, app *App, name string) subscription.Subscription {
	t.Helper()
	subs, err := app.backend.ListSubscriptions()
	if err != nil {
		t.Fatalf("ListSubscriptions: %v", err)
	}
	for _, s := range subs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("subscription %q not found in %+v", name, subs)
	return subscription.Subscription{}
}

func hasLabel(tt *ui.Tester, label string) bool {
	_, ok := tt.Find(label)
	return ok
}

func assertNoHorizontalOverflow(t *testing.T, tt *ui.Tester, width int, labels []string) {
	t.Helper()
	for _, lbl := range labels {
		r, ok := tt.Find(lbl)
		if !ok {
			t.Fatalf("width=%d: missing expected label %q", width, lbl)
		}
		if r.W <= 0 || r.H <= 0 {
			t.Fatalf("width=%d: label %q has zero bounds %+v", width, lbl, r)
		}
		if r.X < -1 || r.X+r.W > float32(width)+1 {
			t.Fatalf("width=%d: label %q overflows horizontally: %+v", width, lbl, r)
		}
	}
}

func TestNewSubscriptionEntryPointsDefaultFullArchiveAndPresetSelection(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// Seed an existing empty-tag subscription to verify existing subscriptions are NEVER altered retroactively.
	existingManual, err := app.backend.CreateSubscription(
		"既有手动订阅",
		"",
		[]subscription.AccountRef{{Site: "bilibili", UID: "80001", Name: "既有UP"}},
		subscription.EmptySubRules(),
	)
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()

	tt := ui.NewTester(app.View, 1280, 860)

	// 1. 添加订阅 dialog: defaults to 完整存档 and supports preset switch, 录直播 overlay, and custom warning.
	app.Navigate(RouteSubscriptions)
	app.AddSubDialogOpen = true
	app.AddSubName = "默认完整存档"
	tt.Frame()
	if !hasLabel(tt, "新订阅追踪模式选择区") || !tt.HasText("完整存档（默认）") {
		t.Fatalf("add sub dialog missing default full-archive preset UI; texts=%v", tt.Texts())
	}
	if err := tt.Click("确认添加订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	createdDefault := findSubByName(t, app, "默认完整存档")
	if !slices.Equal(createdDefault.Tags, subscription.DefaultNewSubTags()) || !createdDefault.Rules.FullCache || createdDefault.Rules.AutoCache.Since == "" || createdDefault.LiveAutoRecord {
		t.Fatalf("add sub default did not create full_archive: tags=%v rules=%+v", createdDefault.Tags, createdDefault.Rules)
	}

	// Open again: choose 追更并保存 + 录直播.
	app.AddSubDialogOpen = true
	app.AddSubName = "追更录播"
	tt.Frame()
	if err := tt.Click("新订阅预设：" + subscription.PresetFollowCache); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("新订阅录直播"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认添加订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	createdFollow := findSubByName(t, app, "追更录播")
	wantFollow := []string{subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagCacheDraw, subscription.TagAutoRecord}
	if !slices.Equal(createdFollow.Tags, wantFollow) || !createdFollow.LiveAutoRecord {
		t.Fatalf("add sub follow+live mismatch: got=%v want=%v", createdFollow.Tags, wantFollow)
	}

	// Open again: expand 自定义 and toggle off scheduled_check -> amber dependency warning must appear.
	app.AddSubDialogOpen = true
	app.AddSubName = "仅手动缓存测试"
	tt.Frame()
	if err := tt.Click("新订阅自定义"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasLabel(tt, "新订阅自定义原子标签区") {
		t.Fatal("expected custom atomic tags panel to expand")
	}
	if err := tt.Click("新订阅原子标签：" + subscription.TagScheduledCheck); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasLabel(tt, "新订阅自动缓存缺少定时检查提示") || !hasLabel(tt, "新订阅一键开启定时检查") || !tt.HasText(autoCacheWithoutScheduledCheckHint) {
		t.Fatalf("expected dependency gap warning + one-click action in add-sub modal; texts=%v", tt.Texts())
	}
	if err := tt.Click("新订阅一键开启定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if hasLabel(tt, "新订阅自动缓存缺少定时检查提示") || !slices.Contains(app.AddSubPreset.SelectedTags(), subscription.TagScheduledCheck) {
		t.Fatalf("one-click enable in add-sub modal did not restore scheduled_check: tags=%v", app.AddSubPreset.SelectedTags())
	}
	app.closeAddSubOverlay()
	tt.Frame()

	// 2. 加入订阅 dialog (OpenSubscribePicker): defaults to 完整存档.
	app.OpenSubscribePicker(subscription.AccountRef{Site: "bilibili", UID: "80002", Name: "新发现UP"})
	tt.Frame()
	if !hasLabel(tt, "加入订阅追踪模式选择区") || !tt.HasText("完整存档（默认）") {
		t.Fatalf("subscribe picker missing preset UI; texts=%v", tt.Texts())
	}
	if err := tt.Click("新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	createdFromPicker := findSubByName(t, app, "新发现UP")
	if !slices.Equal(createdFromPicker.Tags, subscription.DefaultNewSubTags()) {
		t.Fatalf("subscribe picker new sub tags=%v want default full_archive", createdFromPicker.Tags)
	}

	// 3. 更改关联订阅 dialog (OpenMoveAccountPicker): selecting check_only creates target with [scheduled_check].
	app.OpenMoveAccountPicker(createdFromPicker, subscription.AccountRef{Site: "bilibili", UID: "80002", Name: "新发现UP"})
	tt.Frame()
	if err := tt.Click("加入订阅预设：" + subscription.PresetCheckOnly); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("移到新建订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	subsAfterMove, err := app.backend.ListSubscriptions()
	if err != nil {
		t.Fatal(err)
	}
	foundMoved := false
	for _, s := range subsAfterMove {
		if s.ID != createdFromPicker.ID && len(s.Accounts) == 1 && s.Accounts[0].UID == "80002" {
			foundMoved = true
			if !slices.Equal(s.Tags, []string{subscription.TagScheduledCheck}) {
				t.Fatalf("moved-to-new sub tags=%v want [scheduled_check]", s.Tags)
			}
		}
	}
	if !foundMoved {
		t.Fatal("expected account 80002 to be moved into a new subscription")
	}

	// 4. 从关注列表导入 modal: respects selected preset AND defaults to 完整存档.
	app.ImportSubModalOpen = true
	app.ImportSite = "bilibili"
	app.ImportPreset.ResetDefault()
	app.ImportCreators = []service.FollowingCreatorDTO{
		{Site: "bilibili", UID: "80003", Name: "导入手动UP"},
		{Site: "bilibili", UID: "80004", Name: "导入默认UP"},
	}
	app.ImportSelectedKeys = map[string]bool{"bilibili:80003": true}
	app.ImportSelectedMids = map[int64]bool{80003: true}
	tt.Frame()
	if !hasLabel(tt, "关注导入追踪模式选择区") {
		t.Fatal("import followings modal missing tracking preset picker")
	}
	if err := tt.Click("关注导入预设：" + subscription.PresetManual); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("确认导入关注"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	importedManual := findSubByName(t, app, "导入手动UP")
	if len(importedManual.Tags) != 0 {
		t.Fatalf("imported manual sub tags=%v want empty", importedManual.Tags)
	}

	// Reset to default and import second creator -> full_archive.
	app.ImportSubModalOpen = true
	app.ImportPreset.ResetDefault()
	app.ImportSelectedKeys = map[string]bool{"bilibili:80004": true}
	app.ImportSelectedMids = map[int64]bool{80004: true}
	tt.Frame()
	if err := tt.Click("确认导入关注"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	importedDefault := findSubByName(t, app, "导入默认UP")
	if !slices.Equal(importedDefault.Tags, subscription.DefaultNewSubTags()) {
		t.Fatalf("imported default sub tags=%v want default full_archive", importedDefault.Tags)
	}

	// Existing manual subscription must remain untouched (empty tags).
	reloadedExisting := findSubByName(t, app, existingManual.Name)
	if len(reloadedExisting.Tags) != 0 {
		t.Fatalf("existing saved subscription was altered retroactively: tags=%v", reloadedExisting.Tags)
	}
}

func TestSubscriptionDetailPresetDropdownDependencyWarningAndEmptyStates(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// Verify all 6 rule-aware empty-state copy variants directly.
	cases := []struct {
		name string
		sub  subscription.Subscription
		want string
	}{
		{
			name: "no_accounts",
			sub:  subscription.Subscription{Name: "无账号"},
			want: "还没有关联站点账号",
		},
		{
			name: "full_cache",
			sub: subscription.Subscription{
				Name:     "完整存档",
				Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "101"}},
				Tags:     subscription.DefaultNewSubTags(),
			},
			want: "已开启「全部缓存」",
		},
		{
			name: "follow_cache",
			sub: subscription.Subscription{
				Name:     "追更并保存",
				Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "102"}},
				Tags:     []string{subscription.TagScheduledCheck, subscription.TagCacheVideo, subscription.TagCacheDraw},
			},
			want: "已开启自动缓存，之后新发布的内容会自动追踪并缓存到这里",
		},
		{
			name: "auto_cache_without_sched",
			sub: subscription.Subscription{
				Name:     "无定时自动缓存",
				Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "103"}},
				Tags:     []string{subscription.TagCacheVideo},
			},
			want: "已开启自动缓存但未开启「定时检查」",
		},
		{
			name: "check_only",
			sub: subscription.Subscription{
				Name:     "只看新动态",
				Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "104"}},
				Tags:     []string{subscription.TagScheduledCheck},
			},
			want: "当前仅检查新动态、未开启自动缓存",
		},
		{
			name: "manual",
			sub: subscription.Subscription{
				Name:     "仅手动",
				Accounts: []subscription.AccountRef{{Site: "bilibili", UID: "105"}},
				Tags:     []string{},
			},
			want: "当前未开启自动缓存",
		},
	}
	for _, tc := range cases {
		if got := subscriptionEmptyStateHint(tc.sub); !strings.Contains(got, tc.want) {
			t.Fatalf("empty state case %s: got %q, want substring %q", tc.name, got, tc.want)
		}
	}

	sub, err := app.backend.CreateSubscription(
		"测试详情预设",
		"",
		[]subscription.AccountRef{{Site: "bilibili", UID: "90011", Name: "测试UP"}},
		subscription.DefaultSubRules(),
	)
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	app.OpenSubscriptionDetail(sub)

	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if !tt.HasText("追踪：完整存档") || !tt.HasText("已开启「全部缓存」") {
		t.Fatalf("detail header should show 追踪：完整存档 and full_cache empty state; texts=%v", tt.Texts())
	}
	if hasLabel(tt, "自动缓存缺少定时检查提示") {
		t.Fatal("dependency warning should NOT show when scheduled_check is enabled")
	}

	// Open preset menu, switch to 追更并保存, toggle 录直播, then expand atomic tags and turn off scheduled_check.
	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("追更并保存"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("追踪：追更并保存") {
		t.Fatalf("expected 追踪：追更并保存 after preset switch; texts=%v", tt.Texts())
	}

	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("录直播"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasLabel(tt, "订阅缓存标签：开播录制") {
		t.Fatal("expected 录直播 chip after toggling auto_record on")
	}

	if err := tt.Click("订阅缓存标签菜单"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("自定义原子标签"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasLabel(tt, "订阅详情自定义原子标签栏") {
		t.Fatal("expected inline custom atomic tag panel")
	}
	if err := tt.Click("订阅原子标签：定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("追踪：自定义") || !hasLabel(tt, "自动缓存缺少定时检查提示") || !hasLabel(tt, "一键开启定时检查") || !tt.HasText(autoCacheWithoutScheduledCheckHint) {
		t.Fatalf("expected custom mode + dependency warning + one-click action after disabling scheduled_check; texts=%v", tt.Texts())
	}
	if !tt.HasText("已开启自动缓存但未开启「定时检查」") {
		t.Fatalf("expected empty-state copy to update for auto-cache without scheduled_check; texts=%v", tt.Texts())
	}

	// Collapse custom atomic tags and verify user can still fix the dependency gap in 1 click.
	app.SubCustomTagsSubID = ""
	tt.Frame()
	if !hasLabel(tt, "一键开启定时检查") {
		t.Fatal("one-click enable scheduled_check button should stay visible when custom tags are collapsed")
	}
	if err := tt.Click("一键开启定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if hasLabel(tt, "自动缓存缺少定时检查提示") || !tt.HasText("追踪：追更并保存") {
		t.Fatalf("one-click enable in detail header should restore scheduled_check and clear warning; texts=%v", tt.Texts())
	}
}

func TestFirstCheckBaselineFeedbackMessages(t *testing.T) {
	// 1. Pure first-check baseline initialization: NEVER say "新增 0 条动态".
	pure := checkSummaryMessage(subscription.CheckSummary{
		TotalCreators:       3,
		CheckedCreators:     3,
		BaselineInitialized: 3,
		NewDynamics:         0,
	})
	if !strings.Contains(pure, "已为 3 个账号记录检查起点，之后发布的新内容会被追踪") || strings.Contains(pure, "新增 0 条动态") {
		t.Fatalf("pure baseline message misleading: %q", pure)
	}

	// 2. Mixed round: some incremental + some baseline + auto-cache queued.
	mixed := checkSummaryMessage(subscription.CheckSummary{
		TotalCreators:       5,
		CheckedCreators:     5,
		BaselineInitialized: 2,
		NewDynamics:         4,
		AutoCacheTasks:      2,
	})
	if !strings.Contains(mixed, "已为 2 个账号记录检查起点") || !strings.Contains(mixed, "另检查 3 个账号") || !strings.Contains(mixed, "新增 4 条动态") || !strings.Contains(mixed, "已自动入队 2 条") {
		t.Fatalf("mixed baseline+dynamics message unexpected: %q", mixed)
	}

	// 3. Normal incremental check with auto-cache queued.
	normalAuto := checkSummaryMessage(subscription.CheckSummary{
		TotalCreators:   2,
		CheckedCreators: 2,
		NewDynamics:     3,
		AutoCacheTasks:  2,
	})
	if !strings.Contains(normalAuto, "检查 2 个账号，新增 3 条动态，已自动入队 2 条") {
		t.Fatalf("normal auto check message unexpected: %q", normalAuto)
	}

	// 4. Normal incremental check with 0 new dynamics.
	normal := checkSummaryMessage(subscription.CheckSummary{
		TotalCreators:   2,
		CheckedCreators: 2,
		NewDynamics:     0,
	})
	if !strings.Contains(normal, "检查 2 个账号，新增 0 条动态，失败 0 个") {
		t.Fatalf("normal check message unexpected: %q", normal)
	}

	// 5. Stopped, canceled, backoff, and error states.
	if got := checkSummaryMessage(subscription.CheckSummary{DeferredByQuota: true}); !strings.Contains(got, "本小时自动请求额度已用尽") {
		t.Fatalf("quota message unexpected: %q", got)
	}
	if got := checkSummaryMessage(subscription.CheckSummary{DeferredByBackoff: true}); !strings.Contains(got, "自动请求仍在退避期") {
		t.Fatalf("backoff message unexpected: %q", got)
	}
	if got := checkSummaryMessage(subscription.CheckSummary{Canceled: true, CheckedCreators: 1, TotalCreators: 4}); !strings.Contains(got, "已取消检查（1/4 个账号）") {
		t.Fatalf("canceled message unexpected: %q", got)
	}
	if got := checkSummaryMessage(subscription.CheckSummary{CheckedCreators: 2, TotalCreators: 2, FailedCreators: 1, Errors: []string{"boom"}}); !strings.Contains(got, "boom") {
		t.Fatalf("error message unexpected: %q", got)
	}
}

func TestSettingsSubscriptionRemovedGlobalToggleQuickPresetsAndResponsiveWidths(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	// Simulate legacy v1 settings.json with Enabled=false.
	app.DraftSettings.Subscription.Enabled = false
	app.DraftSettings.ScheduledCheck = false

	// Start with 0 scheduled subscriptions -> Settings › 订阅与直播 shows empty summary, no global toggle.
	app.SettingsCategory = "subscription"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if hasLabel(tt, "开关定时订阅") {
		t.Fatal("global scheduled-check toggle should be removed")
	}
	if !hasLabel(tt, "选择订阅检查间隔") || !hasLabel(tt, "定时检查状态摘要") || !tt.HasText("当前没有订阅开启定时检查") {
		t.Fatalf("settings subscription missing interval or 0-sub summary; texts=%v", tt.Texts())
	}

	// Create 1 default (full_archive) subscription -> summary updates immediately (no stale 20s cache!)
	// to show 1 sub (1 account) + "下一轮定时检查：" and NEVER falsely claims "对应站点尚未登录".
	sub, err := app.backend.CreateSubscription(
		"定时摘要测试",
		"",
		[]subscription.AccountRef{{Site: "bilibili", UID: "90021", Name: "定时UP"}},
		subscription.DefaultSubRules(),
	)
	if err != nil {
		t.Fatal(err)
	}
	app.RefreshData()
	tt.Frame()
	if !tt.HasText("已开启定时检查：1 个订阅（1 个账号）") {
		t.Fatalf("expected 1 scheduled sub summary; texts=%v", tt.Texts())
	}
	if tt.HasText("对应站点尚未登录，登录后将按间隔自动检查") {
		t.Fatalf("stale checkPlan cache falsely reported site unauthenticated; texts=%v", tt.Texts())
	}
	hasNextLine := func(texts []string) string {
		for _, s := range texts {
			if strings.HasPrefix(s, "下一轮定时检查：") {
				return s
			}
		}
		return ""
	}
	next60 := hasNextLine(tt.Texts())
	if next60 == "" {
		t.Fatalf("expected immediate 下一轮定时检查 line after creating scheduled sub; texts=%v", tt.Texts())
	}

	// Changing interval via saveSettings immediately clears checkPlanAt and updates next check time.
	app.DraftSettings.Subscription.IntervalMinutes = 30
	app.DraftSettings.CheckIntervalMin = 30
	app.saveSettings()
	tt.Frame()
	next30 := hasNextLine(tt.Texts())
	if next30 == "" || next30 == next60 {
		t.Fatalf("expected 下一轮定时检查 to update immediately on interval change: before=%q after=%q", next60, next30)
	}

	// Explicit manual schedule mode (PlanOff) reports manual pause, NOT unauthenticated.
	app.DraftSettings.Subscription.Mode = "manual"
	app.saveSettings()
	tt.Frame()
	if !tt.HasText("定时检查当前处于手动模式，已暂停自动运行") || tt.HasText("对应站点尚未登录，登录后将按间隔自动检查") {
		t.Fatalf("PlanOff should distinguish manual mode from unauthenticated; texts=%v", tt.Texts())
	}
	app.DraftSettings.Subscription.Mode = "interval"
	app.saveSettings()
	tt.Frame()

	// Settings › 缓存标签 quick presets open MultiTagBatch pre-filled & dependency warning + one-click fix work.
	app.SettingsCategory = "rules"
	tt.Frame()
	if !hasLabel(tt, "缓存标签快速预设栏") || !hasLabel(tt, "快速应用预设："+subscription.PresetFollowCache) {
		t.Fatal("Settings › 缓存标签 missing quick preset strip")
	}
	if err := tt.Click("快速应用预设：" + subscription.PresetFollowCache); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.MultiTagBatch.Open || app.MultiTagBatch.Mode != subscription.TagBatchReplace {
		t.Fatalf("quick preset did not open MultiTagBatch in replace mode: open=%v mode=%q", app.MultiTagBatch.Open, app.MultiTagBatch.Mode)
	}
	if !app.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] || !app.MultiTagBatch.SelectedTags[subscription.TagCacheVideo] || !app.MultiTagBatch.SelectedTags[subscription.TagCacheDraw] {
		t.Fatalf("quick preset did not preselect follow_cache tags: %+v", app.MultiTagBatch.SelectedTags)
	}
	if err := tt.Click("批量选择标签：定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !hasLabel(tt, "批量设置自动缓存缺少定时检查提示") || !hasLabel(tt, "批量一键开启定时检查") || !tt.HasText(autoCacheWithoutScheduledCheckHint) {
		t.Fatalf("batch dialog missing dependency gap warning or one-click fix; texts=%v", tt.Texts())
	}
	if err := tt.Click("批量一键开启定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if hasLabel(tt, "批量设置自动缓存缺少定时检查提示") || !app.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] {
		t.Fatalf("one-click fix in batch replace mode did not re-enable scheduled_check")
	}

	// Remove mode: removing scheduled_check from a subscription with auto-cache shows warning + 保留定时检查.
	app.MultiTagBatch.Mode = subscription.TagBatchRemove
	app.MultiTagBatch.SelectedTags = map[string]bool{subscription.TagScheduledCheck: true}
	app.MultiTagBatch.SelectedSubs = map[string]bool{ruleBatchSubKey(sub): true}
	tt.Frame()
	if !hasLabel(tt, "批量设置自动缓存缺少定时检查提示") || !hasLabel(tt, "批量保留定时检查") {
		t.Fatalf("batch remove mode missing dependency warning or 保留定时检查 action; texts=%v", tt.Texts())
	}
	if err := tt.Click("批量保留定时检查"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if hasLabel(tt, "批量设置自动缓存缺少定时检查提示") || app.MultiTagBatch.SelectedTags[subscription.TagScheduledCheck] {
		t.Fatalf("clicking 批量保留定时检查 should uncheck scheduled_check and clear warning")
	}
	app.closeMultiTagBatchDialog()

	// Responsive width overflow checks at 1280, 1000, and 720.
	for _, w := range []int{1280, 1000, 720} {
		wt := ui.NewTester(app.View, w, 860)

		// Settings › 订阅与直播
		app.SettingsCategory = "subscription"
		app.Navigate(RouteSettings)
		wt.Frame()
		assertNoHorizontalOverflow(t, wt, w, []string{"选择订阅检查间隔", "定时检查状态摘要"})

		// Settings › 缓存标签
		app.SettingsCategory = "rules"
		wt.Frame()
		assertNoHorizontalOverflow(t, wt, w, []string{"打开批量设置订阅标签", "缓存标签快速预设栏", "快速应用预设：full_archive"})

		// Add Sub modal with expanded custom tags + dependency warning button
		app.Navigate(RouteSubscriptions)
		app.AddSubDialogOpen = true
		app.AddSubPreset.ResetDefault()
		app.AddSubPreset.CustomExpanded = true
		app.AddSubPreset.ToggleCustomTag(subscription.TagScheduledCheck)
		wt.Frame()
		assertNoHorizontalOverflow(t, wt, w, []string{"添加订阅对话框", "新订阅追踪模式选择区", "新订阅自定义原子标签区", "新订阅自动缓存缺少定时检查提示", "新订阅一键开启定时检查", "确认添加订阅"})
		app.closeAddSubOverlay()

		// Subscription detail with dependency warning + one-click button
		_, _ = app.backend.SetSubscriptionTags(sub.ID, []string{subscription.TagCacheVideo})
		app.RefreshData()
		updated := findSubByName(t, app, sub.Name)
		app.OpenSubscriptionDetail(updated)
		app.SubCustomTagsSubID = updated.ID
		wt.Frame()
		assertNoHorizontalOverflow(t, wt, w, []string{"订阅详情缓存标签栏", "订阅缓存标签菜单", "订阅详情自定义原子标签栏", "自动缓存缺少定时检查提示", "一键开启定时检查"})
	}
}
