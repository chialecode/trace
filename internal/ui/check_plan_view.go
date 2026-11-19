package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/subscription"
	"github.com/egoist/mygo/ui"
)

// checkPlanRefresh is how long Tasks › 检查计划 shows a plan before reading
// it again (a local read, no request).
const checkPlanRefresh = 20 * time.Second

func (a *App) selectTasksChecks() {
	a.TasksSection = "checks"
	a.checkPlanAt = time.Time{}
	a.closeLivePreview()
}

func (a *App) checkPlanRowsAt(now time.Time) []service.AccountCheckRow {
	if a.backend == nil {
		return nil
	}
	if a.checkPlanAt.IsZero() || now.Sub(a.checkPlanAt) >= checkPlanRefresh {
		a.checkPlan = a.backend.CheckPlan()
		a.checkPlanAt = now
	}
	return a.checkPlan
}

func (a *App) checkPlanRows() []service.AccountCheckRow {
	return a.checkPlanRowsAt(time.Now())
}

// formatPlanTime shows a check time as 今天 15:04, 明天 15:04, 昨天 15:04
// or 11月16日 15:04, using UTC midnight of local calendar dates so 23-hour
// spring-forward / 25-hour fall-back DST boundaries stay exact.
func formatPlanTime(raw string, now time.Time) string {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	t, now = t.Local(), now.Local()
	tDay := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	nowDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch int(tDay.Sub(nowDay) / (24 * time.Hour)) {
	case 0:
		return "今天 " + t.Format("15:04")
	case 1:
		return "明天 " + t.Format("15:04")
	case -1:
		return "昨天 " + t.Format("15:04")
	}
	return fmt.Sprintf("%d月%d日 %s", t.Month(), t.Day(), t.Format("15:04"))
}

func formatEvery(sec int) string {
	switch {
	case sec <= 0:
		return ""
	case sec < 3600:
		return fmt.Sprintf("约每 %d 分钟", max(1, (sec+30)/60))
	case sec%3600 == 0:
		return fmt.Sprintf("约每 %d 小时", sec/3600)
	}
	return fmt.Sprintf("约每 %.1f 小时", float64(sec)/3600)
}

// liveCheckCount is how many auto-record Douyin accounts the plan lists and
// how often (seconds) each is checked for broadcasts.
func liveCheckCount(rows []service.AccountCheckRow) (int, int) {
	seen := map[string]bool{}
	every := 0
	for _, r := range rows {
		if r.Site == "douyin" && r.AutoRecord && !seen[r.UID] {
			seen[r.UID] = true
			every = max(every, r.LiveEverySec)
		}
	}
	return len(seen), every
}

// liveIntervalNote is the line under the live check interval setting: how
// often accounts are actually checked now.
func liveIntervalNote(rows []service.AccountCheckRow) string {
	n, every := liveCheckCount(rows)
	if n == 0 || every <= 0 {
		return "B 站每轮查全部账号；抖音账号轮流查，规则见下"
	}
	return fmt.Sprintf("现在：B 站每轮查全部账号；抖音 %d 个账号，每个%s查一次", n, formatEvery(every))
}

// douyinLiveChecksPerRound is how many Douyin accounts a live check round of
// intervalSec seconds can check at the gap between requests.
func douyinLiveChecksPerRound(intervalSec int) int {
	return max(1, int(time.Duration(max(intervalSec, 60))*time.Second/live.DefaultDouyinLiveGap))
}

// liveCheckRules explains how live checks are scheduled (DEC-060 ⑤), under
// the interval setting.
func liveCheckRules(intervalSec int) []string {
	minutes := max(intervalSec, 60) / 60
	gap := int(live.DefaultDouyinLiveGap / time.Second)
	per := douyinLiveChecksPerRound(intervalSec)
	return []string{
		fmt.Sprintf("B 站：一个请求就能查完全部账号，每 %d 分钟查一轮。", minutes),
		fmt.Sprintf("抖音：没有批量查询，每个账号要单独发一个请求，两个请求之间至少隔 %d 秒，所以 %d 分钟一轮最多查 %d 个账号。账号不超过 %d 个时，每个都每 %d 分钟查一次；超过后轮流查，上次查得早的先查，每多一个账号间隔约多 %d 秒。", gap, minutes, per, per, minutes, gap),
		"开播检查不占每小时自动请求额度（额度留给检查更新和自动缓存）；遇到风控或限流时和其他自动请求一起暂停、自动放慢。",
		"手动刷新开播状态、检查更新时看到正在开播，也会马上开始录制，不用等轮到。",
	}
}

// renderLiveCheckRules shows liveCheckRules as a muted block.
func renderLiveCheckRules(c *ui.Context, intervalSec int) {
	ui.Column(c).FillWidth().Padding(4, 0, 12, 0).Gap(4).ClipX().Children(func() {
		ui.Text(c, "开播检查规则").FontSize(12).FontWeight(600).TextColor(ColorTextMuted).SingleLine()
		for _, line := range liveCheckRules(intervalSec) {
			ui.Text(c, line).FontSize(12).TextColor(ColorTextMuted).MaxLines(4)
		}
	})
}

// checkResultText is the outcome of an account's last update check.
func checkResultText(kind, note string) (string, bool) {
	switch kind {
	case "ok":
		return "正常", true
	case "incomplete":
		return firstNonEmptyStrUI(note, "部分内容没取到"), false
	case "skipped_unauth":
		return "未登录，已跳过", false
	case "risk_control":
		return "被风控拦下", false
	case "rate_limited":
		return "请求过快被限流", false
	case "auth_required":
		return "登录失效", false
	case "network":
		return "网络错误", false
	case "quota":
		return "本小时额度用完，已顺延", false
	case "backoff":
		return "退避中，已顺延", false
	case "":
		return "", true
	}
	return "失败（" + kind + "）", false
}

// checkPlanNextText is the right-hand column of a row: when the account is
// checked next, or why it is not checked on a schedule.
func checkPlanNextText(row service.AccountCheckRow, now time.Time) (string, bool) {
	switch row.Plan {
	case subscription.PlanScheduled:
		if row.NextAt == "" {
			return "暂无计划", false
		}
		if row.AfterBackoff {
			return "退避后 " + formatPlanTime(row.NextAt, now), true
		}
		return "下次 " + formatPlanTime(row.NextAt, now), true
	case subscription.PlanManual:
		return "只手动检查", false
	case subscription.PlanOff:
		return "定时检查已关闭", false
	case subscription.PlanUnauth:
		return "未登录，不检查", false
	}
	return "", false
}

// renderTasksChecksView is Tasks › 检查计划: for every subscribed account,
// when its updates were last checked and with what result, when a scheduled
// round should check it next, and, for subscriptions that record
// broadcasts, its live checks.
func (a *App) renderTasksChecksView(c *ui.Context) {
	now := c.Now()
	rows := a.checkPlanRowsAt(now)
	checking := a.isPending("check-updates")

	st := a.fs()
	if st.checkPlanListState == nil {
		st.checkPlanListState = &ui.ListState{}
	}
	totalItems := 1 + len(rows)
	if len(rows) == 0 {
		totalItems = 2
	}
	st.checkPlanListState.Key = func(i int) any {
		if i == 0 {
			return "header"
		}
		if len(rows) == 0 {
			return "empty"
		}
		idx := i - 1
		if idx < 0 || idx >= len(rows) {
			return i
		}
		return rows[idx].Site + ":" + rows[idx].UID
	}

	ui.List(c, st.checkPlanListState, totalItems, func(i int) {
		if i == 0 {
			ui.Box(c).FillWidth().Padding(16, 16, 12, 16).ClipX().Children(func() {
				ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).ClipX().Children(func() {
					a.renderCheckPlanHead(c, rows, now)
					if checking {
						ui.Text(c, fmt.Sprintf("正在检查 %d / %d", a.CheckDone, a.CheckTotal)).FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine().Shrink(0)
						if OutlinePillButton(c, "取消", "检查计划：取消检查").Clicked() {
							a.backend.CancelSubscriptionCheck()
						}
					} else if len(rows) > 0 && OutlinePillButton(c, "检查全部", "检查计划：检查全部").Clicked() {
						a.checkingAccount = ""
						a.checkAllSubscriptions()
					}
				})
			})
			return
		}
		if len(rows) == 0 {
			ui.Column(c).FillWidth().Padding(32, 16).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "还没有订阅的账号").FontSize(14).TextColor(ColorTextMuted).SingleLine()
			})
			return
		}
		idx := i - 1
		if idx < 0 || idx >= len(rows) {
			return
		}
		padBottom := float32(10)
		if idx == len(rows)-1 {
			padBottom = 16
		}
		ui.Box(c).FillWidth().Padding(0, 16, padBottom, 16).ClipX().Children(func() {
			a.renderCheckPlanRow(c, rows[idx], now, checking)
		})
	}).Label("检查计划列表").Grow(1).FillWidth()

	if checking {
		c.After(time.Second)
		return
	}
	wait := checkPlanRefresh - now.Sub(a.checkPlanAt)
	if wait < time.Second {
		wait = time.Second
	} else if wait > checkPlanRefresh {
		wait = checkPlanRefresh
	}
	c.After(wait)
}

// earliestScheduledNextAt finds the chronologically earliest scheduled NextAt
// using parsed timestamps (handling mixed RFC3339 timezone offsets safely).
func earliestScheduledNextAt(rows []service.AccountCheckRow) string {
	var bestRaw string
	var bestTime time.Time
	for _, r := range rows {
		if r.Plan != subscription.PlanScheduled || strings.TrimSpace(r.NextAt) == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(r.NextAt))
		if err != nil {
			continue
		}
		if bestRaw == "" || t.Before(bestTime) {
			bestRaw = r.NextAt
			bestTime = t
		}
	}
	return bestRaw
}

// renderCheckPlanHead is the page's heading: when the next scheduled round
// starts and how a round picks its accounts.
func (a *App) renderCheckPlanHead(c *ui.Context, rows []service.AccountCheckRow, now time.Time) {
	ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
		next := earliestScheduledNextAt(rows)
		head := "定时检查已关闭或没有定时检查的订阅"
		if next != "" {
			head = "下一轮定时检查：" + formatPlanTime(next, now)
		}
		ui.Text(c, head).FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		ui.Text(c, fmt.Sprintf("更新检查：每轮每个站点最多检查 B 站 %d 个、抖音 %d 个账号，上次检查得早的先查；额度不足或退避时顺延。",
			subscription.DefaultBilibiliRoundAccountCap, subscription.DefaultDouyinRoundAccountCap)).
			FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
		ui.Text(c, fmt.Sprintf("开播检查（只针对开了“开播录制”的订阅）：B 站每轮查全部；抖音每个账号单独查、两次至少隔 %d 秒，一轮最多 %d 个，超过后轮流查；不占每小时额度。完整规则见设置 › 订阅与直播。",
			int(live.DefaultDouyinLiveGap/time.Second), douyinLiveChecksPerRound(a.DraftSettings.Live.PollIntervalSeconds))).
			FontSize(12).TextColor(ColorTextMuted).MaxLines(3)
	})
}

func (a *App) renderCheckPlanRow(c *ui.Context, row service.AccountCheckRow, now time.Time, checking bool) {
	key := row.Site + ":" + row.UID
	ui.Row(c).Label("检查计划行 "+key).FillWidth().Padding(12, 14).Gap(12).Radius(14).Border(1, ColorBorder).Background(ColorBgMain).AlignItems(ui.Center).ClipX().Children(func() {
		var mid int64
		if row.Site == "bilibili" {
			mid, _ = strconv.ParseInt(row.UID, 10, 64)
		}
		a.renderAccountAvatar(c, row.Site, row.Name, row.UID, mid, 36, false, "", nil)
		ui.Column(c).Grow(1).Gap(3).ClipX().Children(func() {
			ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
				ui.Text(c, firstNonEmptyStrUI(row.Name, row.UID)).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine().Shrink(1)
				renderSiteSmallBadgeWithDot(c, row.Site, "")
				if row.SubName != "" && row.SubName != row.Name {
					ui.Text(c, "· 订阅 "+row.SubName).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(1)
				}
			})
			last := "更新检查：还没有检查过"
			good := true
			if row.LastCheckedAt != "" {
				res, ok := checkResultText(row.LastKind, row.Note)
				last, good = "更新检查：上次 "+formatPlanTime(row.LastCheckedAt, now), ok
				if res != "" {
					last += " · " + res
				}
			}
			t := ui.Text(c, last).FontSize(12).MaxLines(2)
			if good {
				t.TextColor(ColorTextMuted)
			} else {
				t.TextColor(ColorError)
			}
			if row.AutoRecord {
				live := "开播检查：本次运行还没有查过"
				if row.LiveCheckedAt != "" {
					live = "开播检查：上次 " + formatPlanTime(row.LiveCheckedAt, now)
				}
				if every := formatEvery(row.LiveEverySec); every != "" {
					live += " · " + every
				}
				ui.Text(c, live).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			}
		})
		text, scheduled := checkPlanNextText(row, now)
		nt := ui.Text(c, text).FontSize(13).SingleLine().Shrink(0)
		if scheduled {
			nt.FontWeight(600).TextColor(ColorTextMain)
		} else {
			nt.TextColor(ColorTextMuted)
		}
		// Checking by hand works whatever the plan, except signed out.
		if checking && a.checkingAccount == key {
			ui.Text(c, "检查中…").FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).SingleLine().Shrink(0)
		} else if OutlinePillButton(c, "检查", "检查计划：立即检查 "+key).Disabled(checking || row.Plan == subscription.PlanUnauth).Clicked() {
			a.checkAccountNow(row.SubID, row.Site, row.UID)
		}
	})
}
