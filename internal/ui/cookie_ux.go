package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/service"
	"github.com/chialecode/trace/internal/site"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func siteDisplayNameUI(siteID string) string {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		return "抖音"
	case "weibo":
		return "微博"
	case "x":
		return "X"
	default:
		return "哔哩哔哩"
	}
}

func cookieCopySteps(siteID string) []string {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		return []string{
			"在桌面浏览器打开 douyin.com 并登录，点开「我的 → 喜欢」（如有滑块先完成验证）",
			"按 F12 打开开发者工具，切换到「网络（Network）」，筛选「Fetch/XHR」",
			"刷新页面，点开发往 www.douyin.com 的任意请求，查看「请求头（Request Headers）」",
			"复制整段 Cookie 值粘贴到下方（包含 sessionid 及 s_v_web_id、ttwid、UIFID 等辅助字段）",
		}
	case "weibo":
		return []string{
			"在桌面浏览器打开 PC 版 weibo.com 并登录（勿从手机版 m.weibo.cn 复制）",
			"按 F12 打开开发者工具，切换到「网络（Network）」，筛选「Fetch/XHR」",
			"刷新页面，点开 weibo.com/ajax/ 请求，查看「请求头（Request Headers）」",
			"复制整段 Cookie 值粘贴到下方（区分大小写，包含 SUB 及 SUBP、XSRF-TOKEN、WBPSESS）",
		}
	case "x":
		return []string{
			"在桌面浏览器打开已登录的 x.com/home",
			"按 F12 打开开发者工具，切换到「网络（Network）」，筛选「Fetch/XHR」",
			"刷新页面，点开发往 x.com 的请求（勿点 twimg.com 图片），查看「请求头（Request Headers）」",
			"复制整段 Cookie 值粘贴到下方（包含 auth_token、ct0，建议同时保留 twid 与 guest_id）",
		}
	default:
		return []string{
			"日常优先推荐「扫码登录」（支持自动续期）；如需粘贴，请先在浏览器登录 bilibili.com",
			"按 F12 打开开发者工具，切换到「网络（Network）」，筛选「Fetch/XHR」",
			"刷新页面，点开发往 api.bilibili.com 的请求（如 nav），查看「请求头（Request Headers）」",
			"复制整段 Cookie 值粘贴到下方（包含 SESSDATA，建议同时保留 bili_jct、DedeUserID）",
		}
	}
}

func cookieInputPlaceholder(siteID string) string {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		return "粘贴 Cookie（至少包含 sessionid）"
	case "weibo":
		return "粘贴 Cookie（至少包含 SUB）"
	case "x":
		return "粘贴 Cookie（至少包含 auth_token 与 ct0）"
	default:
		return "粘贴 Cookie（至少包含 SESSDATA）"
	}
}

// renderCookieCopyGuide draws a compact, text-only step-by-step guide for copying
// complete browser Cookie headers (no external images, zero real credentials).
func renderCookieCopyGuide(c *ui.Context, siteID string) {
	norm := normalizeSiteIDUI(siteID)
	steps := cookieCopySteps(norm)
	ui.Column(c).
		Label("Cookie获取步骤："+norm).
		FillWidth().
		Padding(9, 11).
		Radius(10).
		Background(ColorBgSecondary).
		Gap(3).
		ClipX().
		Children(func() {
			ui.Text(c, "如何复制完整 Cookie").FontSize(12).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			for i, step := range steps {
				num := fmt.Sprintf("%d.", i+1)
				ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(6).ClipX().Children(func() {
					ui.Text(c, num).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine().Shrink(0)
					ui.Text(c, step).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3).Grow(1).Shrink(1)
				})
			}
		})
}

// renderCookiePrecheckBanner renders live, non-blocking local pre-check feedback for
// a pasted Cookie string. It never echoes raw cookie values and never disables submission.
func renderCookiePrecheckBanner(c *ui.Context, siteID, rawInput string) {
	norm := normalizeSiteIDUI(siteID)
	res := service.PrecheckCookieInput(norm, rawInput)
	if res.Severity == service.CookiePrecheckEmpty {
		return
	}

	var (
		label      string
		icon       string
		bgColor    ui.Color
		borderCol  ui.Color
		titleColor ui.Color
		iconColor  ui.Color
	)
	switch res.Severity {
	case service.CookiePrecheckOK:
		label = "Cookie预检状态：" + norm
		icon = "check-circle"
		bgColor = ui.RGBA(42, 174, 91, 0.10)
		borderCol = ui.RGBA(42, 174, 91, 0.30)
		titleColor = ColorTextMain
		iconColor = ColorDotGreen
	case service.CookiePrecheckWarning:
		label = "Cookie预检提示：" + norm
		icon = "alert-circle"
		bgColor = ColorWarnBg
		borderCol = ColorWarnBorder
		titleColor = ColorWarnText
		iconColor = ColorWarnText
	default:
		label = "Cookie预检错误：" + norm
		icon = "alert-circle"
		bgColor = ColorErrorBg
		borderCol = ui.RGBA(180, 35, 24, 0.22)
		titleColor = ColorError
		iconColor = ColorError
	}

	ui.Column(c).
		Label(label).
		FillWidth().
		Padding(9, 11).
		Radius(10).
		Background(bgColor).
		Border(1, borderCol).
		Gap(4).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(6).ClipX().Children(func() {
				DrawIcon(c, icon, 14, iconColor, 1.8)
				ui.Text(c, res.Summary).FontSize(12).LineHeight(LineHeightCompact).FontWeight(600).TextColor(titleColor).MaxLines(3).Grow(1).Shrink(1)
			})
			for _, f := range res.MissingRequired {
				ui.Text(c, fmt.Sprintf("• 缺少 %s：%s", f.Key, f.Reason)).
					FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorError).MaxLines(2)
			}
			for _, f := range res.MissingOptional {
				ui.Text(c, fmt.Sprintf("• 缺少 %s：%s", f.Key, f.Reason)).
					FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorWarnText).MaxLines(2)
			}
			for _, notice := range res.Notices {
				ui.Text(c, "• "+notice).
					FontSize(12).LineHeight(LineHeightCompact).TextColor(titleColor).MaxLines(3)
			}
			if res.Guidance != "" {
				ui.Text(c, "建议："+res.Guidance).
					FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(4)
			}
		})
}

func renderXRiskDisclosureBox(c *ui.Context, label string) {
	ui.Column(c).
		Label(label).
		FillWidth().
		Padding(10, 12).
		Radius(10).
		Background(ColorWarnBg).
		Border(1, ColorDotYellow).
		Gap(4).
		ClipX().
		Children(func() {
			ui.Text(c, "使用前请注意").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "X 网页端接口不是官方开放 API，X 服务条款与自动化规则限制未经授权的自动化访问。用网页 Cookie 请求 X 存在触发限流、要求验证、临时锁号甚至封号的风险，强烈建议使用专门账号或可承受风控的小号（本工具不会阻止你粘贴自己的账号）。").
				FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(6)
			ui.Text(c, "TRACE 只读不写，不保存账号密码，也不对粘贴的 Cookie 自动续期；粘贴的 Cookie 与浏览器共用会话，请避免本机与远程 VPS 对同一账号并发高频请求，若遇到验证或锁号会立即停止 X 的全部自动请求。").
				FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(5)
		})
}

// siteLastVerifiedAt resolves the RFC3339 timestamp when siteID was last verified
// valid, preferring uistate.AuthVerifiedAt (preserved across expired/unknown checks)
// and falling back to credential.Status.LastCheckedAt/UpdatedAt for active pre-existing logins.
func siteLastVerifiedAt(siteID string, st credential.Status, uiSt uistate.State) string {
	norm := normalizeSiteIDUI(siteID)
	if uiSt.AuthVerifiedAt != nil {
		if ts := strings.TrimSpace(uiSt.AuthVerifiedAt[norm]); ts != "" {
			return ts
		}
	}
	if st.LoggedIn && st.State == "logged_in" {
		return firstNonEmptyStrUI(st.LastCheckedAt, st.UpdatedAt)
	}
	return ""
}

// formatAccountVerifiedTime formats a UTC RFC3339 verified-at timestamp in the local timezone.
func formatAccountVerifiedTime(raw string, now time.Time) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "尚未验证"
	}
	t, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return "尚未验证"
	}
	loc := now.Location()
	if loc == nil {
		loc = time.Local
	}
	localNow := now.In(loc)
	localT := t.In(loc)
	ny, nm, nd := localNow.Date()
	ty, tm, td := localT.Date()
	if ny == ty && nm == tm && nd == td {
		return "今天 " + localT.Format("15:04")
	}
	yest := localNow.AddDate(0, 0, -1)
	yy, ym, yd := yest.Date()
	if yy == ty && ym == tm && yd == td {
		return "昨天 " + localT.Format("15:04")
	}
	if ny == ty {
		return localT.Format("1月2日 15:04")
	}
	return localT.Format("2006年1月2日 15:04")
}

func accountStateBadgeStyle(st credential.Status) (text string, fg, bg, border ui.Color) {
	switch {
	case st.State == "expired":
		return "登录已失效", ColorError, ColorErrorBg, ui.RGBA(180, 35, 24, 0.25)
	case st.State == "decrypt_failed":
		return "凭证损坏", ColorError, ColorErrorBg, ui.RGBA(180, 35, 24, 0.25)
	case st.State == "refresh_failed":
		return "续期失败", ColorWarnText, ColorWarnBg, ColorWarnBorder
	case st.State == "unknown":
		return "状态待确认", ColorWarnText, ColorWarnBg, ColorWarnBorder
	case st.LoggedIn:
		return "已登录", ColorDotGreen, ui.RGBA(42, 174, 91, 0.10), ui.RGBA(42, 174, 91, 0.28)
	default:
		return "未登录", ColorTextMuted, ColorBgSecondary, ColorBorder
	}
}

func renderAccountStatusBadge(c *ui.Context, siteID string, st credential.Status) {
	text, fg, bg, border := accountStateBadgeStyle(st)
	ui.Box(c).
		Label("账号状态标签："+normalizeSiteIDUI(siteID)).
		Padding(2, 8).
		Radius(999).
		Background(bg).
		Border(1, border).
		Shrink(0).
		Children(func() {
			ui.Text(c, text).FontSize(11).FontWeight(600).TextColor(fg).SingleLine()
		})
}

func (a *App) expiredAccountSites() []string {
	if a == nil || a.backend == nil {
		return nil
	}
	order := []string{"bilibili", "douyin", "weibo", "x"}
	var expired []string
	for _, s := range order {
		st := a.backend.StatusOf(s)
		if st.State == "expired" || st.State == "decrypt_failed" {
			expired = append(expired, s)
		}
	}
	return expired
}

func accountExpiryTooltip(expired []string) string {
	if len(expired) == 0 {
		return ""
	}
	names := make([]string, 0, len(expired))
	for _, s := range expired {
		names = append(names, siteDisplayNameUI(s))
	}
	return strings.Join(names, "、") + "登录已失效，点击前往设置 › 账号重新登录"
}

func (a *App) clearSettingsPasteState() {
	a.SettingsPasteSite = ""
	a.SettingsPasteInput = ""
	a.SettingsPasteComposing = false
	a.SettingsPasteError = ""
}

func (a *App) toggleSettingsInlinePaste(siteID string) {
	norm := normalizeSiteIDUI(siteID)
	if a.SettingsPasteSite == norm {
		a.clearSettingsPasteState()
		return
	}
	a.SettingsPasteSite = norm
	a.SettingsPasteInput = ""
	a.SettingsPasteComposing = false
	a.SettingsPasteError = ""
}

func (a *App) renderSettingsInlinePaste(c *ui.Context, siteID string) {
	norm := normalizeSiteIDUI(siteID)
	busyKey := "settings-cookie-login-" + norm
	busy := a.isPending(busyKey)

	ui.Column(c).
		Label("设置内联粘贴区："+norm).
		FillWidth().
		Padding(14).
		Radius(12).
		Background(ColorBgMain).
		Border(1, ColorBorderStrong).
		Gap(10).
		ClipX().
		Children(func() {
			ui.Text(c, fmt.Sprintf("重新粘贴 %s Cookie", siteDisplayNameUI(norm))).
				FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()

			renderCookieCopyGuide(c, norm)

			inp := ui.TextInput(c, &a.SettingsPasteInput).
				Password().
				Placeholder(cookieInputPlaceholder(norm)).
				Label("设置Cookie输入框：" + norm).
				FillWidth()
			wasComposing := a.SettingsPasteComposing
			a.SettingsPasteComposing = inp.Composing()
			if inp.Submitted() && !inp.Composing() && !wasComposing && !busy {
				a.submitSettingsInlineCookie(norm)
			}

			if norm == "x" {
				renderXRiskDisclosureBox(c, "设置X登录风险提示")
			}

			if a.SettingsPasteError != "" {
				renderErrorBanner(c, "设置Cookie错误提示", a.SettingsPasteError, nil)
			}

			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
				ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
					FontSize(12).TextColor(ColorTextMuted).MaxLines(2).Grow(1).Shrink(1)
				ui.Row(c).Gap(8).Shrink(0).AlignItems(ui.Center).Children(func() {
					if OutlinePillButton(c, "取消", "取消设置粘贴："+norm).Disabled(busy).Clicked() && !busy {
						a.clearSettingsPasteState()
					}
					btnText := "验证并更新"
					if busy {
						btnText = "正在验证…"
					}
					if AccentPillButton(c, btnText, "验证并更新Cookie："+norm).Disabled(busy).Clicked() && !busy {
						a.submitSettingsInlineCookie(norm)
					}
				})
			})

			renderCookiePrecheckBanner(c, norm, a.SettingsPasteInput)
		})
}

func (a *App) submitSettingsInlineCookie(siteID string) {
	norm := normalizeSiteIDUI(siteID)
	rawCookie := a.SettingsPasteInput
	// Clear input immediately upon submit before async network validation finishes.
	a.SettingsPasteInput = ""
	a.SettingsPasteError = ""
	if strings.TrimSpace(rawCookie) == "" {
		switch norm {
		case "douyin":
			a.SettingsPasteError = "缺少 sessionid"
		case "weibo":
			a.SettingsPasteError = "缺少 SUB"
		case "x":
			a.SettingsPasteError = "缺少 auth_token 或 ct0（请复制包含 auth_token 与 ct0 的完整 X Cookie）"
		default:
			a.SettingsPasteError = "缺少 SESSDATA"
		}
		return
	}
	busyKey := "settings-cookie-login-" + norm
	if a.isPending(busyKey) {
		rawCookie = ""
		return
	}
	var targetSite site.ID
	switch norm {
	case "douyin":
		targetSite = site.Douyin
	case "weibo":
		targetSite = site.Weibo
	case "x":
		targetSite = site.X
	default:
		targetSite = site.Bilibili
	}
	requestGuarded(a, busyKey, nil, func(ctx context.Context) (credential.Status, error) {
		defer func() { rawCookie = "" }()
		return a.backend.LoginWithCookie(ctx, string(targetSite), rawCookie, "")
	}, func(status credential.Status) {
		if status.LoggedIn {
			a.clearSettingsPasteState()
			if a.expiredNoticeShown != nil {
				delete(a.expiredNoticeShown, norm)
			}
			a.clearErrorNotifications()
			a.notifyInfo(settingsCookieUpdatedMessage(norm, status))
			a.RefreshData()
		}
	}, func(err error) {
		switch norm {
		case "douyin":
			a.SettingsPasteError = douyinLoginErrorMessage(err)
		case "weibo":
			a.SettingsPasteError = weiboLoginErrorMessage(err)
		case "x":
			a.SettingsPasteError = xLoginErrorMessage(err)
		default:
			a.SettingsPasteError = userErrorText(err)
		}
	})
}

func settingsCookieUpdatedMessage(siteID string, status credential.Status) string {
	switch normalizeSiteIDUI(siteID) {
	case "douyin":
		handle := firstNonEmptyStrUI(status.UniqueID, status.UID)
		if status.Uname != "" && handle != "" {
			return fmt.Sprintf("已更新抖音 Cookie：%s · 抖音号：%s", status.Uname, handle)
		}
		if status.Uname != "" {
			return "已更新抖音 Cookie：" + status.Uname
		}
		return "已更新抖音 Cookie"
	case "weibo":
		uid := strings.TrimSpace(status.UID)
		if status.Uname != "" && uid != "" {
			return fmt.Sprintf("已更新微博 Cookie：%s · UID %s", status.Uname, uid)
		}
		if status.Uname != "" {
			return "已更新微博 Cookie：" + status.Uname
		}
		return "已更新微博 Cookie"
	case "x":
		handle := strings.TrimPrefix(strings.TrimSpace(status.UniqueID), "@")
		if status.Uname != "" && handle != "" {
			return fmt.Sprintf("已更新 X Cookie：%s (@%s)", status.Uname, handle)
		}
		if handle != "" {
			return "已更新 X Cookie：@" + handle
		}
		return "已更新 X Cookie"
	default:
		return "已更新哔哩哔哩 Cookie"
	}
}
