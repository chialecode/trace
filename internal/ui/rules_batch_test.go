package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

type ruleBatchSpyBackend struct {
	service.BackendAPI
	batchCalls int
	lastRuleID string
	lastSubIDs []string
	forceErr   error
}

func (s *ruleBatchSpyBackend) SetSubscriptionsRule(ruleID string, subIDs []string) error {
	s.batchCalls++
	s.lastRuleID = ruleID
	s.lastSubIDs = append([]string(nil), subIDs...)
	if s.forceErr != nil {
		return s.forceErr
	}
	return s.BackendAPI.SetSubscriptionsRule(ruleID, subIDs)
}

func seedFiveSubscriptionsForRuleBatch(t *testing.T, app *App) (ruleVideo, ruleLive subscription.CacheRule, subs [5]subscription.Subscription) {
	t.Helper()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()

	var err error
	ruleVideo, err = app.backend.SaveCacheRule(subscription.CacheRule{
		Name:           "只存视频",
		ScheduledCheck: true,
		AutoCache:      true,
		Types:          []string{"video"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ruleLive, err = app.backend.SaveCacheRule(subscription.CacheRule{
		Name:           "自动录播",
		ScheduledCheck: true,
		AutoRecord:     true,
	})
	if err != nil {
		t.Fatal(err)
	}

	specs := []struct {
		name     string
		note     string
		uid      string
		acctName string
		ruleID   string
	}{
		{"阿尔法", "备注日常", "5001", "AlphaOfficial", subscription.DefaultRuleID},
		{"贝塔", "备注二创", "5002", "BetaChannel", ruleVideo.ID},
		{"伽马", "备注评测", "5003", "GammaLab", ruleVideo.ID},
		{"德尔塔", "备注歌回", "5004", "DeltaSinger", ruleLive.ID},
		{"艾普西隆", "备注杂谈", "5005", "EpsilonTalk", ruleLive.ID},
	}
	for i, sp := range specs {
		created, err := app.backend.CreateSubscription(
			sp.name,
			sp.note,
			[]subscription.AccountRef{{Site: "bilibili", UID: sp.uid, Name: sp.acctName}},
			subscription.DefaultSubRules(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if sp.ruleID != subscription.DefaultRuleID {
			created, err = app.backend.SetSubscriptionRule(created.ID, sp.ruleID)
			if err != nil {
				t.Fatal(err)
			}
		}
		subs[i] = created
	}
	app.RefreshData()
	return ruleVideo, ruleLive, subs
}

// In Settings › 缓存规则, expanding a rule offers "选择订阅…", supports
// instant search (by name, note, account nickname with IME composition guard)
// and select-all / unselect-all, applies the selection in 1 atomic write to
// subscriptions.json, reverts unchecked former members to "默认", keeps other
// rules untouched, and syncs both rule counts and subscription detail's rule
// button immediately (DEC-051 ⑧).
func TestCacheRuleBatchSelectSubscriptions(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	spy := &ruleBatchSpyBackend{BackendAPI: app.backend}
	app.backend = spy

	if err := app.backend.SetSubscriptionsRule(subscription.DefaultRuleID, nil); err == nil {
		t.Fatal("expected SetSubscriptionsRule before login to require authentication")
	}

	var ruleUpdatedEvents int
	unsub := app.backend.SubscribeEvents(func(ev service.Event) {
		if ev.Type == service.EventSubscriptionChecked && ev.Message == "rules_updated" {
			ruleUpdatedEvents++
		}
	})
	defer unsub()

	ruleVideo, ruleLive, subs := seedFiveSubscriptionsForRuleBatch(t, app)

	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if err := tt.Click("缓存规则：只存视频"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("使用这条规则的订阅（2）") {
		t.Fatalf("expected editor to show 2 subscriptions using 只存视频, texts=%v", tt.Texts())
	}

	if err := tt.Click("选择订阅…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.RuleBatch.Open || !tt.HasText("选择使用「只存视频」的订阅") {
		t.Fatal("batch dialog did not open with expected title")
	}
	if !tt.HasText("已选 2 个") || !tt.HasText("应用到 2 个订阅") {
		t.Fatalf("expected 2 pre-selected subscriptions, texts=%v", tt.Texts())
	}
	if !tt.HasText("当前：默认") || !tt.HasText("当前：自动录播") {
		t.Fatalf("expected current rule labels for subscriptions on other rules, texts=%v", tt.Texts())
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

	// Clear all, then pick 3 subscriptions: 阿尔法 (was 默认), 贝塔 (kept 只存视频), 德尔塔 (was 自动录播).
	// 伽马 (was 只存视频) stays unchecked -> reverts to 默认; 艾普西隆 (was 自动录播) stays 自动录播.
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
	spy.batchCalls = 0
	ruleUpdatedEvents = 0

	if err := tt.Click("应用到 3 个订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()

	if app.RuleBatch.Open {
		t.Fatal("expected batch dialog to close after successful apply")
	}
	if spy.batchCalls != 1 || ruleUpdatedEvents != 1 {
		t.Fatalf("expected SetSubscriptionsRule once and 1 rules_updated event, got calls=%d events=%d", spy.batchCalls, ruleUpdatedEvents)
	}
	bakAfter, err := os.ReadFile(subsPath + ".bak")
	if err != nil || !bytes.Equal(bakAfter, rawBefore) {
		t.Fatalf("expected subscriptions.json.bak to match pre-batch file (1 atomic write), err=%v", err)
	}
	if !tt.HasText("使用这条规则的订阅（3）") {
		t.Fatalf("expected rule editor count to refresh to 3 immediately, texts=%v", tt.Texts())
	}

	// Verify persisted rules across all 5 subscriptions.
	gotRule := make(map[string]string, 5)
	for _, s := range app.Subscriptions {
		gotRule[s.ID] = s.RuleID
	}
	wantRule := map[string]string{
		subs[0].ID: ruleVideo.ID,               // 阿尔法 -> 只存视频
		subs[1].ID: ruleVideo.ID,               // 贝塔 -> 只存视频
		subs[2].ID: subscription.DefaultRuleID, // 伽马 -> reverted to 默认
		subs[3].ID: ruleVideo.ID,               // 德尔塔 -> 只存视频
		subs[4].ID: ruleLive.ID,                // 艾普西隆 -> unchanged 自动录播
	}
	for id, want := range wantRule {
		if gotRule[id] != want {
			t.Fatalf("subscription %s rule=%q, want %q", id, gotRule[id], want)
		}
	}

	// Jump to subscription detail page via UI navigation and verify "规则：<规则名>" button is synced.
	app.Navigate(RouteSubscriptions)
	tt.Frame()
	if err := tt.Click("选择订阅 " + subs[0].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅缓存规则：只存视频"); !ok || !tt.HasText("规则：只存视频") {
		t.Fatalf("subscription detail for 阿尔法 did not sync rule button, texts=%v", tt.Texts())
	}

	if err := tt.Click("订阅侧栏：" + subs[2].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅缓存规则：默认"); !ok || !tt.HasText("规则：默认") {
		t.Fatalf("subscription detail for 伽马 did not sync fallback 默认 rule button, texts=%v", tt.Texts())
	}

	if err := tt.Click("订阅侧栏：" + subs[4].ID); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if _, ok := tt.Find("订阅缓存规则：自动录播"); !ok || !tt.HasText("规则：自动录播") {
		t.Fatalf("subscription detail for 艾普西隆 did not keep 自动录播 rule button, texts=%v", tt.Texts())
	}

	// DefaultRuleID batch pull-back: open "默认" rule, pull 艾普西隆 back to 默认 while 伽马 stays 默认.
	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt.Frame()
	if err := tt.Click("缓存规则：默认"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("选择订阅…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("勾选规则订阅：艾普西隆"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("应用到 2 个订阅"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !tt.HasText("使用这条规则的订阅（2）") {
		t.Fatalf("expected 默认 rule count to become 2, texts=%v", tt.Texts())
	}
}

func TestCacheRuleBatchDialogErrorRetainsSelectionAndDismissShortcuts(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()

	spy := &ruleBatchSpyBackend{BackendAPI: app.backend}
	app.backend = spy

	_, _, _ = seedFiveSubscriptionsForRuleBatch(t, app)

	app.SettingsCategory = "rules"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()

	if err := tt.Click("缓存规则：只存视频"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if err := tt.Click("选择订阅…"); err != nil {
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
		t.Fatalf("expected dialog to stay open, show error in red, and keep 5 selected; open=%v texts=%v", app.RuleBatch.Open, tt.Texts())
	}
	spy.forceErr = nil

	// Escape closes the modal without applying.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if app.RuleBatch.Open {
		t.Fatal("expected Escape to close the batch dialog")
	}
	if !tt.HasText("使用这条规则的订阅（2）") {
		t.Fatal("dismissing dialog should not alter rule membership")
	}

	// Backdrop click outside the centered dialog closes the modal.
	if err := tt.Click("选择订阅…"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if !app.RuleBatch.Open {
		t.Fatal("expected batch dialog to reopen")
	}
	tt.ClickAt(12, 12)
	tt.Frame()
	if app.RuleBatch.Open {
		t.Fatal("expected clicking backdrop outside dialog to close it")
	}
}
