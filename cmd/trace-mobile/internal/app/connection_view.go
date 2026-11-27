package app

import (
	"fmt"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
)

const githubReleasesURL = "https://github.com/chialecode/trace/releases"

// renderConnectionGate renders the full-screen connection status/error page
// covering every reason in docs/design/mobile.md §8. No stale content is shown.
func (a *App) renderConnectionGate(c *ui.Context) {
	rec, hasPairing := a.store.LoadPairing()
	computerName := rec.Name
	if computerName == "" {
		computerName = "我的电脑"
	}
	connState, tryingAddr, lastErr := a.snapshotGateError()

	ui.Column(c).
		Fill().
		Background(theme.ColorBgMain).
		Padding(28, 24).
		Justify(ui.Center).
		AlignItems(ui.Center).
		Children(func() {
			ui.Column(c).
				FillWidth().
				MaxWidth(420).
				Gap(18).
				AlignItems(ui.Center).
				Children(func() {
					// Computer icon badge
					ui.Box(c).
						Size(68, 68).
						Radius(20).
						Background(theme.ColorBgPanel).
						Border(1, theme.ColorBorder).
						Center().
						Children(func() {
							col := theme.ColorAccent
							if connState == pagekit.ConnError && lastErr != nil && lastErr.Kind != companionclient.KindDesktopLoggedOut {
								col = theme.ColorError
							}
							theme.DrawIcon(c, "computer", 32, col, 1.8)
						})

					if hasPairing {
						ui.Text(c, computerName).FontSize(20).FontWeight(600).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
					}

					if connState == pagekit.ConnConnecting {
						addr := tryingAddr
						if addr == "" && len(rec.Addrs) > 0 {
							addr = rec.Addrs[0]
						}
						msg := "正在连接电脑…"
						if addr != "" {
							msg = fmt.Sprintf("正在连接 %s…", addr)
						}
						ui.Text(c, msg).
							FontSize(14).
							LineHeight(theme.LineHeightBody).
							TextColor(theme.ColorTextMuted).
							TextAlign(ui.Center)
						if theme.SecondaryPillButton(c, "取消", "取消连接").MinWidth(140).Clicked() {
							a.cancelConnect()
						}
						return
					}

					// Error state (every reason in design §8)
					errMsg := "无法连接到电脑"
					var actions []companionclient.ErrorAction
					if lastErr != nil {
						errMsg = lastErr.UserMessage()
						actions = lastErr.Actions()
					} else {
						actions = []companionclient.ErrorAction{companionclient.ActionRetry, companionclient.ActionRescan}
					}

					ui.Column(c).
						FillWidth().
						Padding(14, 16).
						Radius(12).
						Background(theme.ColorErrorBg).
						Border(1, ui.RGBA(196, 24, 46, 0.22)).
						Gap(8).
						AlignItems(ui.Center).
						Children(func() {
							theme.DrawIcon(c, "alert-circle", 20, theme.ColorError, 1.8)
							ui.Text(c, errMsg).
								FontSize(14).
								LineHeight(theme.LineHeightBody).
								TextColor(theme.ColorError).
								TextAlign(ui.Center)
						})

					if len(actions) > 0 {
						ui.Column(c).FillWidth().Gap(10).Padding(4, 0, 0, 0).Children(func() {
							for _, act := range actions {
								switch act {
								case companionclient.ActionRetry:
									if theme.PrimaryPillButton(c, "重试", "重试连接").FillWidth().Clicked() {
										a.RetryNow()
									}
								case companionclient.ActionRescan:
									btn := func() ui.Element {
										if len(actions) == 1 {
											return theme.PrimaryPillButton(c, "重新扫码", "重新扫码")
										}
										return theme.SecondaryPillButton(c, "重新扫码", "重新扫码")
									}()
									if btn.FillWidth().Clicked() {
										a.StartRescan()
									}
								case companionclient.ActionOpenDownload:
									if theme.PrimaryPillButton(c, "打开下载页", "打开下载页").FillWidth().Clicked() {
										_ = a.OpenExternal(githubReleasesURL)
									}
								}
							}
						})
					}
				})
		})
}
