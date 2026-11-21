package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// Tasks › 检查计划 lists each subscribed account with its last update check
// and when it is checked next; an account whose rule checks only by hand
// says so, and a subscription that records broadcasts shows its live
// checks too (dev feedback 2026-11-18).
func TestTasksCheckPlanSection(t *testing.T) {
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	if _, err := app.backend.ImportCookie(context.Background(), "SESSDATA=fixture; DedeUserID=12345", ""); err != nil {
		t.Fatal(err)
	}
	app.CompleteLogin()
	if _, err := app.backend.CreateSubscription("定时", "", []subscription.AccountRef{{Site: "bilibili", UID: "5001", Name: "scheduled author"}}, subscription.SubRules{ScheduledCheck: true, AutoRecord: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.backend.CreateSubscription("手动", "", []subscription.AccountRef{{Site: "bilibili", UID: "5002", Name: "manual author"}}, subscription.SubRules{}); err != nil {
		t.Fatal(err)
	}
	app.Navigate(RouteTasks)
	tt := ui.NewTester(app.View, 1280, 860)
	tt.Frame()
	if err := tt.Click("任务侧栏：检查计划"); err != nil {
		t.Fatal(err)
	}
	settle(tt, 2)
	if app.effectiveTasksSection() != "checks" {
		t.Fatalf("section %q", app.effectiveTasksSection())
	}
	for _, want := range []string{"scheduled author", "manual author", "只手动检查", "更新检查：还没有检查过"} {
		if !tt.HasText(want) {
			t.Errorf("missing %q in %v", want, tt.Texts())
		}
	}
	if !tt.HasText("开播检查：本次运行还没有查过 · 约每 3 分钟") {
		t.Error("the recording subscription's live checks are not shown")
	}
	rows := app.checkPlanRows()
	for _, r := range rows {
		switch r.UID {
		case "5001":
			if r.Plan != subscription.PlanScheduled && r.Plan != subscription.PlanOff || !r.AutoRecord {
				t.Errorf("scheduled account: %+v", r)
			}
			if text, _ := checkPlanNextText(r, time.Now()); !tt.HasText(text) {
				t.Errorf("next check %q not shown", text)
			}
		case "5002":
			if r.Plan != subscription.PlanManual || r.AutoRecord || r.LiveEverySec != 0 {
				t.Errorf("manual account: %+v", r)
			}
		}
	}
	if len(rows) != 2 {
		t.Fatalf("rows: %+v", rows)
	}

	// 检查 on a row checks that account at once, whatever its plan; the
	// other account stays unchecked.
	if _, ok := tt.Find("检查计划：检查全部"); !ok {
		t.Error("no 检查全部 button")
	}
	if err := tt.Click("检查计划：立即检查 bilibili:5002"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && app.isPending("check-updates") {
		settle(tt, 1)
		time.Sleep(10 * time.Millisecond)
	}
	settle(tt, 2)
	for _, r := range app.checkPlanRows() {
		if checked := r.LastCheckedAt != ""; checked != (r.UID == "5002") {
			t.Errorf("after checking 5002 by hand: %+v", r)
		}
	}

	narrow := ui.NewTester(app.View, 720, 860)
	narrow.Frame()
	if _, ok := narrow.Find("任务分段：检查计划"); !ok {
		t.Fatal("narrow window has no 检查计划 segment")
	}
}

func TestFormatPlanTime(t *testing.T) {
	now := time.Date(2026, 11, 18, 20, 0, 0, 0, time.Local)
	for raw, want := range map[string]string{
		now.Add(time.Hour).UTC().Format(time.RFC3339):       "今天 21:00",
		now.Add(5 * time.Hour).UTC().Format(time.RFC3339):   "明天 01:00",
		now.Add(-21 * time.Hour).UTC().Format(time.RFC3339): "昨天 23:00",
		now.Add(-72 * time.Hour).UTC().Format(time.RFC3339): "11月15日 20:00",
	} {
		if got := formatPlanTime(raw, now); got != want {
			t.Errorf("formatPlanTime(%s) = %q, want %q", raw, got, want)
		}
	}
	if got := formatEvery(18 * 60); got != "约每 18 分钟" {
		t.Errorf("formatEvery = %q", got)
	}
}

// The live check rules are spelled out under the interval setting, with
// the numbers of the chosen interval: a 3-minute round fits 30 Douyin
// accounts at the 6-second gap; a 10-minute one, 100.
func TestLiveCheckRulesInSettings(t *testing.T) {
	if got := douyinLiveChecksPerRound(180); got != 30 {
		t.Fatalf("3-minute round: %d accounts", got)
	}
	if got := douyinLiveChecksPerRound(600); got != 100 {
		t.Fatalf("10-minute round: %d accounts", got)
	}
	app, _, cleanup := newTestUIApp(t)
	defer cleanup()
	app.DraftSettings.Live.PollIntervalSeconds = 180
	app.SettingsCategory = "subscription"
	app.Navigate(RouteSettings)
	tt := ui.NewTester(app.View, 1280, 1600)
	settle(tt, 2)
	for _, want := range append([]string{"开播检查规则"}, liveCheckRules(180)...) {
		if !tt.HasText(want) {
			t.Errorf("settings miss %q", want)
		}
	}
	if !strings.Contains(liveCheckRules(180)[1], "3 分钟一轮最多查 30 个账号") {
		t.Errorf("Douyin rule: %s", liveCheckRules(180)[1])
	}
}
