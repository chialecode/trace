package ui

import (
	"github.com/egoist/mygo/ui"
)

// renderParseRoute draws the centered search-style link & UID parser route (docs/ui.md §3.2).
func (a *App) renderParseRoute(c *ui.Context, width float32) {
	acct, acctSite, acctUID, _, _ := a.primaryAccountSummary()
	acctName := firstNonEmptyStrUI(acct.Uname, "TRACE")

	barW := min(float32(640), max(float32(220), width-32))
	ui.Column(c).
		Grow(1).
		FillWidth().
		Padding(0, 16).
		AlignItems(ui.Center).
		ClipX().
		Children(func() {
			// Place the search-style input slightly above the vertical center.
			// A narrow window has no context panel, so the recent parses fall back under
			// the input (docs/ui.md §1) instead of becoming unreachable.
			narrow := !a.panelShown
			if narrow {
				ui.Box(c).Height(24).Shrink(0)
			} else {
				ui.Box(c).Grow(2)
			}
			ui.Column(c).
				Width(barW).
				Gap(10).
				ClipX().
				Children(func() {
					ui.Row(c).
						Label("解析大输入框容器").
						FillWidth().
						Height(52).
						Padding(6, 6, 6, 10).
						Radius(999).
						Background(ColorBgMain).
						Border(1, ColorBorderStrong).
						Gap(10).
						AlignItems(ui.Center).
						ClipX().
						Children(func() {
							a.renderAccountAvatar(c, acctSite, acctName, acctUID, acct.Mid, 34, false, "", nil, acct.Face)
							inp := ui.TextInputBase(c, &a.ParsePageInput).
								Placeholder("粘贴哔哩哔哩或抖音链接、分享文本、主页或 UID").
								Label("链接解析输入框").
								FontSize(14).
								Padding(6, 4).
								Grow(1)
							wasComposing := a.ParseInputComposing
							a.ParseInputComposing = inp.Composing()
							if inp.Submitted() && !inp.Composing() && !wasComposing && !a.parseBusy() {
								a.SubmitParse(a.ParsePageInput)
							}
							btnLabel := "立即解析"
							if a.parseBusy() {
								btnLabel = "解析中…"
							}
							if AccentPillButton(c, btnLabel, "立即解析").Disabled(a.parseBusy()).Clicked() {
								a.SubmitParse(a.ParsePageInput)
							}
						})

					if a.LastParseError != "" {
						ui.Row(c).
							FillWidth().
							Padding(4, 12).
							AlignItems(ui.Center).
							Justify(ui.SpaceBetween).
							Gap(8).
							ClipX().
							Children(func() {
								ui.Row(c).AlignItems(ui.Center).Gap(6).Grow(1).ClipX().Children(func() {
									DrawIcon(c, "alert-circle", 15, ColorError, 1.8)
									ui.Text(c, a.LastParseError).FontSize(13).TextColor(ColorError).MaxLines(2).Grow(1)
								})
								if a.BannerLoginSite != "" {
									targetSite := a.BannerLoginSite
									if AccentPillButton(c, "去登录", "解析提示去登录").Clicked() {
										a.LastParseError = ""
										a.BannerError = ""
										a.BannerLoginSite = ""
										a.OpenLoginForSite(targetSite)
									}
								}
							})
					}
				})
			if narrow {
				ui.Column(c).
					Label("解析页最近解析").
					Width(barW).
					Grow(1).
					Margin(12, 0, 12, 0).
					Radius(12).
					Border(1, ColorBorder).
					Clip().
					Children(func() {
						a.renderHistoryPanel(c)
					})
			} else {
				ui.Box(c).Grow(3)
			}
		})
}
