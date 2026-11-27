package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

var loginTabs = []TabOption{
	{Key: "qr", Text: "扫码登录（推荐）", AccessLabel: "登录标签：扫码"},
	{Key: "cookie", Text: "粘贴凭证", AccessLabel: "登录标签：粘贴凭证"},
}

// renderLoginRoute draws /login: centered login card with two-segment site
// switcher, QR login for Bilibili, and masked paste-cookie for Bilibili/Douyin
// (docs/ui.md §3.8).
func (a *App) renderLoginRoute(c *ui.Context) {
	loginSite := normalizeSiteIDUI(a.LoginSite)
	siteStatus := a.backend.StatusOf(loginSite)

	ui.Scroll(c).Grow(1).FillWidth().ClipX().Children(func() {
		ui.Column(c).FillWidth().Padding(20, 18).AlignItems(ui.Center).Gap(14).ClipX().Children(func() {
			if siteStatus.State == "expired" {
				ui.Row(c).FillWidth().Padding(10, 14).Radius(12).Background(ColorWarnBg).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					DrawIcon(c, "alert-circle", 16, ColorDotYellow, 1.8)
					ui.Text(c, "登录已失效，请重新登录").
						FontSize(13).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
				})
			}
			if a.SavedRoute != "" {
				ui.Row(c).FillWidth().Padding(10, 14).Radius(12).Background(ColorInfoBlueBg).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					ui.Text(c, "需要重新登录，登录完成后将回到之前的页面并恢复已加载内容").
						FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).MaxLines(2)
				})
			}
			if a.LoginError != "" {
				renderErrorBanner(c, "", a.LoginError, nil)
			}

			// Centered Main Login Card (docs/ui.md §3.8)
			ui.Column(c).
				FillWidth().
				Padding(20).
				Radius(16).
				Background(ColorBgMain).
				Border(1, ColorBorder).
				Gap(16).
				ClipX().
				Children(func() {
					renderTwoSegmentLoginSiteSwitcher(c, loginSite, func(siteID string) {
						if siteID != loginSite {
							a.cancelQRLogin()
							a.LoginSite = siteID
							a.setLoginError("")
						}
					})

					if loginSite == "douyin" {
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "登录抖音").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, "TRACE 需要登录后才能解析和缓存").
								FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						})

						if siteStatus.LoggedIn {
							handleLine := firstNonEmptyStrUI(formatDouyinHandle(siteStatus.UniqueID, "", siteStatus.UID), "抖音号：—")
							dyUID := firstNonEmptyStrUI(siteStatus.SecUID, siteStatus.UID)
							ui.Row(c).
								Label("抖音已登录摘要").
								FillWidth().
								Padding(12, 14).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								AlignItems(ui.Center).
								Gap(10).
								ClipX().
								Children(func() {
									a.renderAccountAvatar(c, "douyin", firstNonEmptyStrUI(siteStatus.Uname, "抖音用户"), dyUID, 0, 40, false, "", nil, siteStatus.Face)
									ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
										ui.Text(c, firstNonEmptyStrUI(siteStatus.Uname, "抖音用户")).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										ui.Text(c, handleLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									})
								})
						}

						// Douyin has Paste-Cookie ONLY (docs/ui.md §3.8)
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "1. 在桌面浏览器打开 douyin.com 登录，进入「我的 → 喜欢」页面\n2. 按 F12 打开开发者工具 → 网络 (Network)，刷新页面并点开任意请求\n3. 复制请求头 (Request Headers) 中的整段完整 Cookie 粘贴到下方").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(5)

								dyBusy := a.isPending("douyin-cookie-login")
								inp := ui.TextInput(c, &a.DouyinCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 sessionid）").
									Label("抖音Cookie输入框").
									FillWidth()
								wasComposing := a.DouyinCookieComposing
								a.DouyinCookieComposing = inp.Composing()
								if inp.Submitted() && !inp.Composing() && !wasComposing && !dyBusy {
									a.importDouyinCookie()
								}

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if dyBusy {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并登录抖音").Disabled(dyBusy).Clicked() && !dyBusy {
										a.importDouyinCookie()
									}
								})
							})
						return
					}

					if loginSite == "weibo" {
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "登录微博").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, "TRACE 需要登录后才能解析和缓存").
								FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						})

						if siteStatus.LoggedIn {
							wbUID := strings.TrimSpace(siteStatus.UID)
							uidLine := "UID：—"
							if wbUID != "" {
								uidLine = "UID：" + wbUID
							}
							ui.Row(c).
								Label("微博已登录摘要").
								FillWidth().
								Padding(12, 14).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								AlignItems(ui.Center).
								Gap(10).
								ClipX().
								Children(func() {
									a.renderAccountAvatar(c, "weibo", firstNonEmptyStrUI(siteStatus.Uname, "微博用户"), wbUID, 0, 40, false, "", nil, siteStatus.Face)
									ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
										ui.Text(c, firstNonEmptyStrUI(siteStatus.Uname, "微博用户")).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										ui.Text(c, uidLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									})
								})
						}

						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "1. 在桌面浏览器打开 weibo.com 并登录账号\n2. 按 F12 打开开发者工具 → 网络 (Network)，刷新页面并点开任意请求\n3. 复制请求头 (Request Headers) 中的整段 Cookie 粘贴到下方（至少包含 SUB）").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(5)

								wbBusy := a.isPending("weibo-cookie-login")
								inp := ui.TextInput(c, &a.WeiboCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 SUB）").
									Label("微博Cookie输入框").
									FillWidth()
								wasComposing := a.WeiboCookieComposing
								a.WeiboCookieComposing = inp.Composing()
								if inp.Submitted() && !inp.Composing() && !wasComposing && !wbBusy {
									a.importWeiboCookie()
								}

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if wbBusy {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并登录微博").Disabled(wbBusy).Clicked() && !wbBusy {
										a.importWeiboCookie()
									}
								})
							})
						return
					}

					if loginSite == "x" {
						ui.Column(c).Gap(4).Children(func() {
							ui.Text(c, "登录 X（原 Twitter）").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
							ui.Text(c, "TRACE 需要登录后才能解析和缓存 X（原 Twitter）内容").
								FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						})

						if siteStatus.LoggedIn {
							xHandle := strings.TrimPrefix(strings.TrimSpace(siteStatus.UniqueID), "@")
							xUID := strings.TrimSpace(siteStatus.UID)
							identLine := "ID：—"
							if xHandle != "" && xUID != "" {
								identLine = fmt.Sprintf("@%s · ID %s", xHandle, xUID)
							} else if xHandle != "" {
								identLine = "@" + xHandle
							} else if xUID != "" {
								identLine = "ID " + xUID
							}
							ui.Row(c).
								Label("X已登录摘要").
								FillWidth().
								Padding(12, 14).
								Radius(12).
								Background(ColorBgMain).
								Border(1, ColorBorder).
								AlignItems(ui.Center).
								Gap(10).
								ClipX().
								Children(func() {
									a.renderAccountAvatar(c, "x", firstNonEmptyStrUI(siteStatus.Uname, xHandle, "X 用户"), xUID, 0, 40, false, "", nil, siteStatus.Face)
									ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
										ui.Text(c, firstNonEmptyStrUI(siteStatus.Uname, xHandle, "X 用户")).FontSize(14).FontWeight(600).TextColor(ColorTextMain).SingleLine()
										ui.Text(c, identLine).FontSize(12).TextColor(ColorTextMuted).SingleLine()
									})
								})
						}

						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "1. 在桌面浏览器打开 x.com 并登录账号\n2. 按 F12 打开开发者工具 → 网络 (Network)，刷新页面并点开发往 x.com 的任意请求\n3. 复制请求头 (Request Headers) 中的整段 Cookie 粘贴到下方（至少包含 auth_token 与 ct0）").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(5)

								xBusy := a.isPending("x-cookie-login")
								inp := ui.TextInput(c, &a.XCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 auth_token 与 ct0）").
									Label("XCookie输入框").
									FillWidth()
								wasComposing := a.XCookieComposing
								a.XCookieComposing = inp.Composing()
								if inp.Submitted() && !inp.Composing() && !wasComposing && !xBusy {
									a.importXCookie()
								}

								// Mandatory neutral factual risk disclosure ABOVE the submit button (x.md §12.3 #1 & §15.1 #19).
								ui.Column(c).
									Label("X登录风险提示").
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
											FontSize(12).TextColor(ColorTextMain).MaxLines(6)
										ui.Text(c, "TRACE 只读不写，不保存账号密码，也不对粘贴的 Cookie 自动续期；若遇到验证或锁号会立即停止 X 的全部自动请求。").
											FontSize(12).TextColor(ColorTextMuted).MaxLines(4)
									})

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if xBusy {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并登录X").Disabled(xBusy).Clicked() && !xBusy {
										a.importXCookie()
									}
								})
							})
						return
					}

					ui.Column(c).Gap(4).Children(func() {
						ui.Text(c, "登录哔哩哔哩").FontSize(20).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, "TRACE 需要登录后才能解析和缓存").
							FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
					})

					UnderlineTabs(c, a.LoginTab, loginTabs, func(key string) {
						if key == "cookie" && a.LoginTab != key {
							a.cancelQRLogin()
						}
						a.LoginTab = key
					})

					// 1. QR Code Login Section
					if a.LoginTab == "qr" {
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
									ui.Text(c, "方式一：哔哩哔哩手机客户端扫码登录").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
									ui.Box(c).Padding(2, 8).Radius(999).Background(ColorAccentBg).Children(func() {
										ui.Text(c, "支持自动续期").FontSize(11).FontWeight(600).TextColor(ColorAccent).SingleLine()
									})
								})

								if a.QRBitmap != nil {
									ui.Row(c).FillWidth().Justify(ui.Center).Padding(6, 0).Children(func() {
										ui.Box(c).Padding(8).Radius(12).Background(ColorWhite).Border(1, ColorBorder).Children(func() {
											ui.Image(c, a.QRBitmap).Size(168, 168).Fit(ui.Contain)
										})
									})
								}
								if a.QRStatusText != "" {
									ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
										ui.Text(c, "扫码状态："+a.QRStatusText).FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).SingleLine()
									})
								}

								qrStarting := a.isPending(fmt.Sprintf("qr-start-%d", a.qrRevision))
								qrPolling := a.QRSession.FlowID != "" && a.isPending("qr-poll-"+a.QRSession.FlowID)
								ui.Row(c).FillWidth().Justify(ui.Center).Gap(8).Wrap().Children(func() {
									if AccentPillButton(c, "获取登录二维码", "获取登录二维码").Disabled(qrStarting).Clicked() && !qrStarting {
										a.startQRLogin()
									}
									if a.QRSession.FlowID != "" {
										if OutlinePillButton(c, "检查扫码状态", "检查扫码状态").Disabled(qrPolling).Clicked() && !qrPolling {
											a.pollQRLogin()
										}
										if OutlinePillButton(c, "取消扫码", "取消扫码").Clicked() {
											a.cancelQRLogin()
											a.QRStatusText = "已取消"
										}
									}
								})
							})
					}

					// 2. Manual Cookie Paste Section (Masked input, cleared immediately on submit, docs/ui.md §3.8)
					if a.LoginTab == "cookie" {
						ui.Column(c).
							FillWidth().
							Padding(16).
							Radius(12).
							Background(ColorBgMain).
							Border(1, ColorBorder).
							Gap(10).
							ClipX().
							Children(func() {
								ui.Text(c, "方式二：粘贴凭证（浏览器 Cookie）").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
								ui.Text(c, "在浏览器打开 bilibili.com 并登录 → 按 F12 打开开发者工具 → 网络 (Network) → 复制请求头中的 Cookie（至少包含 SESSDATA）。").
									FontSize(12).TextColor(ColorTextMuted).MaxLines(2)

								manualBusy := a.isPending("manual-login")
								cookieInp := ui.TextInput(c, &a.ManualCookieInput).
									Password().
									Placeholder("粘贴 Cookie（至少包含 SESSDATA）").
									Label("手动Cookie输入框").
									FillWidth()

								refreshInp := ui.TextInput(c, &a.ManualRefreshInput).
									Password().
									Placeholder("可选：ac_time_value / refresh_token").
									Label("手动RefreshToken输入框").
									FillWidth()

								wasComposing := a.ManualCookieComposing
								a.ManualCookieComposing = cookieInp.Composing() || refreshInp.Composing()
								if (cookieInp.Submitted() || refreshInp.Submitted()) && !a.ManualCookieComposing && !wasComposing && !manualBusy {
									a.importManualCookie()
								}

								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "凭证只保存在这台电脑上，并按当前 Windows 用户加密").
										FontSize(12).TextColor(ColorTextMuted).SingleLine()
									label := "验证并登录"
									if manualBusy {
										label = "正在验证…"
									}
									if AccentPillButton(c, label, "验证并保存凭证").Disabled(manualBusy).Clicked() && !manualBusy {
										a.importManualCookie()
									}
								})
							})
					}
				})
		})
	})
}
