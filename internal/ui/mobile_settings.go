package ui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/companion"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/egoist/mygo/ui"
)

// CompanionAPI is what Settings › 移动端 reads and asks of the desktop companion
// service (implemented by *companion.Manager in the app, or a fake in tests/uishots).
type CompanionAPI interface {
	Status() companion.StatusSnapshot
	ApplySettings(enabled bool, port int) error
	StartPairing() (companion.PairingSessionInfo, error)
	CancelPairing()
	ApprovePairing(pairID string) (companion.DeviceSummary, error)
	RejectPairing(pairID string) error
	RenameDevice(deviceID, newName string) error
	RevokeDevice(deviceID string) error
	ResetIdentity() error
}

// MobileUIState holds ephemeral UI state for Settings › 移动端 (docs/ui.md §3.12).
type MobileUIState struct {
	PortInput        string
	PortLoaded       bool
	PortComposing    bool
	Err              string
	Info             string
	PairDialogOpen   bool
	PairQRPayload    string
	PairQRBitmap     *ui.Bitmap
	RenamingDeviceID string
	RenameInput      string
	RenameComposing  bool
	ConfirmRevokeID  string
	ConfirmResetOpen bool
	DeviceMenuOpenID string
}

// BindCompanion connects the desktop UI to the companion service manager.
func (a *App) BindCompanion(c CompanionAPI) {
	a.companion = c
}

func (a *App) companionStatus() (companion.StatusSnapshot, bool) {
	if a.companion == nil {
		return companion.StatusSnapshot{}, false
	}
	return a.companion.Status(), true
}

func (a *App) renderMobileSettings(c *ui.Context) {
	snap, ok := a.companionStatus()
	if !ok {
		ui.Column(c).FillWidth().Gap(10).ClipX().Children(func() {
			ui.Text(c, "移动端服务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "当前运行模式未启用移动端服务。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		})
		return
	}
	if !snap.Supported {
		ui.Column(c).FillWidth().Gap(10).ClipX().Children(func() {
			ui.Text(c, "移动端服务").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			ui.Text(c, "测试演练身份不提供移动端服务，请在正式版或开发版中使用。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		})
		return
	}

	if !a.Mobile.PortLoaded {
		p := snap.Port
		if p <= 0 {
			p = snap.DefaultPort
		}
		a.Mobile.PortInput = strconv.Itoa(p)
		a.Mobile.PortLoaded = true
	}

	// If a phone initiated pairing while the user is viewing Settings › 移动端,
	// ensure the pairing dialog is visible so the user can approve or reject.
	if len(snap.PendingPairs) > 0 && !a.Mobile.PairDialogOpen {
		a.Mobile.PairDialogOpen = true
	}

	ui.Column(c).Label("设置移动端面板").FillWidth().Gap(14).ClipX().Children(func() {
		// 1. Master switch: 允许手机连接 (docs/ui.md §3.12)
		enabledVal := snap.Enabled
		desc := "手机只能在同一局域网（或你自己的 VPN）里连接这台电脑；关闭窗口时选了“退出”的话，窗口关掉后手机就连不上了。"
		if snap.CloseAction == "quit" {
			desc += "（当前“关闭窗口时”已设为退出，关窗后手机将断开）"
		}
		if settingSwitchRowDisabled(c, "允许手机连接", desc, "允许手机连接开关", &enabledVal, false) {
			a.toggleCompanionEnabled(enabledVal, snap)
			snap, _ = a.companionStatus()
		}

		// 2. Listening status / firewall hint / error banner
		if snap.Running && len(snap.ListenAddrs) > 0 {
			formatted := make([]string, 0, len(snap.ListenAddrs))
			for _, addr := range snap.ListenAddrs {
				if strings.Contains(addr, ":") {
					formatted = append(formatted, fmt.Sprintf("[%s]:%d", addr, snap.Port))
				} else {
					formatted = append(formatted, fmt.Sprintf("%s:%d", addr, snap.Port))
				}
			}
			ui.Column(c).Label("移动端监听状态").FillWidth().Padding(10, 12).Radius(10).Background(ColorBgSecondary).Gap(4).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
					ui.Box(c).Size(8, 8).Radius(4).Background(ColorDotGreen).Shrink(0)
					ui.Text(c, "正在监听 "+strings.Join(formatted, " · ")).
						FontSize(13).FontWeight(500).TextColor(ColorTextMain).MaxLines(3)
				})
				ui.Text(c, "第一次打开时 Windows 可能弹出防火墙提示，需要允许“专用网络”。").
					FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			})
		}

		if errText := firstNonEmptyStrUI(a.Mobile.Err, snap.LastError); errText != "" {
			renderErrorBanner(c, "移动端服务错误", errText, nil)
		}
		if a.Mobile.Info != "" {
			ui.Text(c, a.Mobile.Info).Label("移动端提示").FontSize(12).TextColor(ColorInfoBlue).MaxLines(2)
		}

		// 3. Port configuration row
		ui.Row(c).FillWidth().Padding(8, 0).BorderWidth(0, 0, 1, 0).BorderColor(ColorBorder).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Wrap().ClipX().Children(func() {
			ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
				ui.Text(c, "监听端口").FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
				ui.Text(c, fmt.Sprintf("默认 %d（端口被占用时不会自动换端口）", snap.DefaultPort)).
					FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
			})
			ui.Row(c).AlignItems(ui.Center).Gap(8).Shrink(0).Children(func() {
				inp := ui.TextInput(c, &a.Mobile.PortInput).
					Label("移动端监听端口").
					Placeholder(strconv.Itoa(snap.DefaultPort)).
					Width(110)
				wasComp := a.Mobile.PortComposing
				a.Mobile.PortComposing = inp.Composing()
				if inp.Submitted() && !inp.Composing() && !wasComp {
					a.applyCompanionPort(snap)
				}
				if OutlinePillButton(c, "应用端口", "应用移动端端口").Clicked() {
					a.applyCompanionPort(snap)
				}
			})
		})

		// 4. Paired devices section: 手机 (max 5)
		ui.Column(c).Label("已配对手机列表").FillWidth().Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
				ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
					ui.Text(c, "手机").FontSize(15).FontWeight(600).TextColor(ColorTextMain).SingleLine()
					ui.Text(c, fmt.Sprintf("%d / %d", len(snap.Devices), companionapi.MaxDevices)).
						FontSize(12).TextColor(ColorTextMuted).SingleLine()
				})
				canAdd := snap.Running && len(snap.Devices) < companionapi.MaxDevices
				if AccentPillButton(c, "添加手机", "添加手机").Disabled(!canAdd).Clicked() && canAdd {
					a.openCompanionPairingDialog()
				}
			})

			if len(snap.Devices) == 0 {
				emptyMsg := "还没有已配对的手机。打开“允许手机连接”后，点右上角“添加手机”扫码配对。"
				if snap.Running {
					emptyMsg = "还没有已配对的手机。点击右上角“添加手机”，用手机上的 TRACE 扫码即可连接。"
				}
				ui.Text(c, emptyMsg).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
			} else {
				for _, dev := range snap.Devices {
					a.renderCompanionDeviceRow(c, dev)
				}
			}
		})

		// 5. Bottom danger action: 重置移动端身份
		ui.Column(c).FillWidth().Padding(12, 0, 0, 0).BorderWidth(1, 0, 0, 0).BorderColor(ColorBorder).Gap(8).ClipX().Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(12).Wrap().ClipX().Children(func() {
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					ui.Text(c, "移动端身份").FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
					if snap.IdentityCode != "" {
						ui.Text(c, "电脑身份码："+snap.IdentityCode).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					} else {
						ui.Text(c, "打开“允许手机连接”后自动生成本机证书与身份码。").FontSize(12).TextColor(ColorTextMuted).MaxLines(2)
					}
				})
				if !a.Mobile.ConfirmResetOpen {
					if DangerOutlinePillButton(c, "重置移动端身份", "重置移动端身份").Clicked() {
						a.Mobile.ConfirmResetOpen = true
					}
				}
			})
			if a.Mobile.ConfirmResetOpen {
				ui.Row(c).Label("确认重置移动端身份栏").FillWidth().Padding(10, 12).Radius(10).Background(ColorErrorBg).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
					ui.Text(c, "重置后会生成新的电脑身份证书，所有手机都要重新配对。").
						FontSize(12).TextColor(ColorError).MaxLines(2)
					ui.Row(c).Gap(8).Shrink(0).Children(func() {
						if OutlinePillButton(c, "取消", "取消重置移动端身份").Clicked() {
							a.Mobile.ConfirmResetOpen = false
						}
						if DangerOutlinePillButton(c, "确认重置", "确认重置移动端身份").Clicked() {
							a.Mobile.ConfirmResetOpen = false
							if err := a.companion.ResetIdentity(); err != nil {
								a.Mobile.Err = userErrorText(err)
							} else {
								a.Mobile.Err = ""
								a.Mobile.Info = "已重置移动端身份，所有手机需要重新扫码配对。"
								a.Mobile.PairDialogOpen = false
								a.Mobile.PairQRPayload = ""
								a.Mobile.PairQRBitmap = nil
							}
						}
					})
				})
			}
		})
	})
}

func (a *App) renderCompanionDeviceRow(c *ui.Context, dev companion.DeviceSummary) {
	devID := dev.ID
	ui.Column(c).Label("已配对手机："+dev.Name).FillWidth().Padding(10, 12).Radius(12).Background(ColorBgMain).Border(1, ColorBorder).Gap(8).ClipX().Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).ClipX().Children(func() {
			ui.Row(c).Grow(1).AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				DrawIcon(c, "smartphone", 20, ColorTextMuted, 1.7)
				ui.Column(c).Grow(1).Gap(2).ClipX().Children(func() {
					title := dev.Name
					if dev.Model != "" && !strings.EqualFold(dev.Model, dev.Name) {
						title = dev.Name + " · " + dev.Model
					}
					ui.Text(c, title).FontSize(14).FontWeight(500).TextColor(ColorTextMain).SingleLine()
					ui.Row(c).AlignItems(ui.Center).Gap(6).ClipX().Children(func() {
						if dev.Online {
							ui.Box(c).Size(8, 8).Radius(4).Background(ColorDotGreen).Shrink(0)
							statusLine := "在线"
							if dev.LastAddr != "" {
								statusLine += " · " + dev.LastAddr
							}
							ui.Text(c, statusLine).FontSize(12).TextColor(ColorDotGreen).SingleLine()
						} else {
							last := "尚未连接"
							if dev.LastSeenAt != "" {
								last = "上次连接 " + formatCompanionTimestamp(dev.LastSeenAt)
								if dev.LastAddr != "" {
									last += " · " + dev.LastAddr
								}
							}
							ui.Text(c, last).FontSize(12).TextColor(ColorTextMuted).SingleLine()
						}
					})
				})
			})
			ui.Row(c).Gap(6).Shrink(0).Children(func() {
				if OutlinePillButton(c, "改名", "改名手机："+dev.Name).Clicked() {
					a.Mobile.RenamingDeviceID = devID
					a.Mobile.RenameInput = dev.Name
					a.Mobile.ConfirmRevokeID = ""
				}
				if DangerOutlinePillButton(c, "移除", "移除手机："+dev.Name).Clicked() {
					a.Mobile.ConfirmRevokeID = devID
					a.Mobile.RenamingDeviceID = ""
				}
			})
		})

		if a.Mobile.RenamingDeviceID == devID {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
				inp := ui.TextInput(c, &a.Mobile.RenameInput).
					Label("设备新名称").
					Placeholder("输入手机名称").
					FillWidth()
				wasComp := a.Mobile.RenameComposing
				a.Mobile.RenameComposing = inp.Composing()
				if inp.Submitted() && !inp.Composing() && !wasComp {
					a.submitCompanionRename(devID)
				}
				if AccentPillButton(c, "保存", "保存手机名称").Clicked() {
					a.submitCompanionRename(devID)
				}
				if OutlinePillButton(c, "取消", "取消改名").Clicked() {
					a.Mobile.RenamingDeviceID = ""
				}
			})
		}

		if a.Mobile.ConfirmRevokeID == devID {
			ui.Row(c).Label("确认移除手机栏").FillWidth().Padding(8, 10).Radius(8).Background(ColorErrorBg).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Wrap().ClipX().Children(func() {
				ui.Text(c, "移除后这台手机要重新扫码才能连接。").FontSize(12).TextColor(ColorError).MaxLines(2)
				ui.Row(c).Gap(8).Shrink(0).Children(func() {
					if OutlinePillButton(c, "取消", "取消移除手机").Clicked() {
						a.Mobile.ConfirmRevokeID = ""
					}
					if DangerOutlinePillButton(c, "确认移除", "确认移除手机").Clicked() {
						a.Mobile.ConfirmRevokeID = ""
						if err := a.companion.RevokeDevice(devID); err != nil {
							a.Mobile.Err = userErrorText(err)
						} else {
							a.Mobile.Err = ""
							a.Mobile.Info = fmt.Sprintf("已移除手机“%s”。", dev.Name)
						}
					}
				})
			})
		}
	})
}

func (a *App) renderCompanionPairingDialog(c *ui.Context, snap companion.StatusSnapshot) {
	if !a.Mobile.PairDialogOpen {
		return
	}
	winW, _ := c.Size()
	w := min(float32(460), max(float32(320), winW-40))
	renderDialog(c, &a.Mobile.PairDialogOpen, "添加手机遮罩", "添加手机对话框", w, 0, func() {
		if a.companion != nil {
			a.companion.CancelPairing()
		}
		a.Mobile.PairQRPayload = ""
		a.Mobile.PairQRBitmap = nil
	}, func() {
		// Priority 1: desktop confirmation when a phone has submitted the pairing code (docs/ui.md §3.12).
		if len(snap.PendingPairs) > 0 {
			req := snap.PendingPairs[0]
			ui.Text(c, fmt.Sprintf("允许『%s』连接这台电脑？", req.DeviceName)).
				FontSize(18).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
			metaParts := nonEmptyUI(req.Model, req.Platform, req.RemoteAddr)
			if len(metaParts) > 0 {
				ui.Text(c, strings.Join(metaParts, " · ")).FontSize(13).TextColor(ColorTextMuted).SingleLine()
			}
			ui.Text(c, "允许后，这台手机可以在同一局域网内查看媒体库、订阅与任务，并控制下载和录制。").
				FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
			ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
				if OutlinePillButton(c, "拒绝", "拒绝手机连接").Clicked() {
					_ = a.companion.RejectPairing(req.PairID)
					a.Mobile.PairDialogOpen = false
					a.Mobile.Info = fmt.Sprintf("已拒绝“%s”的连接请求。", req.DeviceName)
				}
				if AccentPillButton(c, "允许", "允许手机连接").Clicked() {
					if dev, err := a.companion.ApprovePairing(req.PairID); err != nil {
						a.Mobile.Err = userErrorText(err)
					} else {
						a.Mobile.Err = ""
						a.Mobile.Info = fmt.Sprintf("已允许“%s”连接。", dev.Name)
						a.Mobile.PairDialogOpen = false
						a.Mobile.PairQRPayload = ""
						a.Mobile.PairQRBitmap = nil
					}
				}
			})
			return
		}

		// Priority 2: QR code + 5-minute countdown + manual short code + 64-bit identity code.
		ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
			ui.Text(c, "添加手机").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
			if OutlinePillButton(c, "刷新", "刷新配对二维码").Clicked() {
				a.refreshCompanionPairingCode()
			}
		})

		session := snap.ActivePairing
		if session == nil {
			ui.Text(c, "配对码已过期，请点击“刷新”重新生成。").FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		} else {
			if session.Payload != a.Mobile.PairQRPayload {
				a.Mobile.PairQRPayload = session.Payload
				a.Mobile.PairQRBitmap = ui.NewBitmap(bilibili.RenderQRRGBA(session.Payload, 6))
			}
			// Countdown text (refresh every second when live)
			countdown := "5 分钟内有效，仅可使用一次"
			if exp, err := time.Parse(time.RFC3339, session.ExpiresAt); err == nil {
				rem := exp.Sub(c.Now())
				if rem <= 0 {
					countdown = "配对码已过期，请点击“刷新”"
				} else {
					mins := int(rem.Minutes())
					secs := int(rem.Seconds()) % 60
					countdown = fmt.Sprintf("剩余 %d:%02d · 仅可使用一次", mins, secs)
					if a.dispatch != nil {
						c.After(time.Second)
					}
				}
			}

			ui.Column(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				if a.Mobile.PairQRBitmap != nil {
					ui.Image(c, a.Mobile.PairQRBitmap).Label("移动端配对二维码").Size(240, 240).Fit(ui.Contain)
				}
				ui.Text(c, countdown).FontSize(12).TextColor(ColorTextMuted).SingleLine()
			})

			primaryAddr := ""
			if len(session.Addrs) > 0 {
				primaryAddr = fmt.Sprintf("%s:%d", session.Addrs[0], session.Port)
			}
			manualHint := fmt.Sprintf("用手机上的 TRACE 扫码；扫不了时输入 %s + 配对码 %s", primaryAddr, session.ShortCode)
			if session.ShortCode == "" {
				manualHint = "手动配对码输入错误过多已作废，请点击“刷新”重新生成。"
			}
			ui.Text(c, manualHint).FontSize(13).TextColor(ColorTextMain).MaxLines(3)
			ui.Text(c, fmt.Sprintf("电脑身份码 %s（手动输入时请在手机上核对）", session.IdentityCode)).
				FontSize(13).FontWeight(600).TextColor(ColorInfoBlue).MaxLines(2)
		}

		ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
			if OutlinePillButton(c, "关闭", "关闭添加手机对话框").Clicked() {
				a.Mobile.PairDialogOpen = false
				a.companion.CancelPairing()
				a.Mobile.PairQRPayload = ""
				a.Mobile.PairQRBitmap = nil
			}
		})
	})
}

func (a *App) toggleCompanionEnabled(enable bool, snap companion.StatusSnapshot) {
	a.Mobile.Err = ""
	a.Mobile.Info = ""
	port := snap.Port
	if parsed, err := strconv.Atoi(strings.TrimSpace(a.Mobile.PortInput)); err == nil && parsed >= 1 && parsed <= 65535 {
		port = parsed
	}
	if port <= 0 {
		port = snap.DefaultPort
	}
	if err := a.companion.ApplySettings(enable, port); err != nil {
		a.Mobile.Err = userErrorText(err)
		// Keep settings disabled when starting fails (design §3: port busy stays off).
		a.DraftSettings.Companion.Enabled = false
		a.DraftSettings.Companion.Port = port
		a.saveSettings()
		return
	}
	a.DraftSettings.Companion.Enabled = enable
	a.DraftSettings.Companion.Port = port
	a.saveSettings()
	if !enable {
		a.Mobile.PairDialogOpen = false
		a.Mobile.PairQRPayload = ""
		a.Mobile.PairQRBitmap = nil
	}
}

func (a *App) applyCompanionPort(snap companion.StatusSnapshot) {
	a.Mobile.Err = ""
	a.Mobile.Info = ""
	port, err := strconv.Atoi(strings.TrimSpace(a.Mobile.PortInput))
	if err != nil || port < 1 || port > 65535 {
		a.Mobile.Err = "端口必须是 1～65535 之间的数字"
		return
	}
	if err := a.companion.ApplySettings(snap.Enabled, port); err != nil {
		a.Mobile.Err = userErrorText(err)
		if snap.Enabled {
			a.DraftSettings.Companion.Enabled = false
		}
		a.DraftSettings.Companion.Port = port
		a.saveSettings()
		return
	}
	a.DraftSettings.Companion.Port = port
	a.saveSettings()
	a.Mobile.Info = fmt.Sprintf("端口已更新为 %d。", port)
}

func (a *App) openCompanionPairingDialog() {
	a.Mobile.Err = ""
	a.Mobile.Info = ""
	if _, err := a.companion.StartPairing(); err != nil {
		a.Mobile.Err = userErrorText(err)
		return
	}
	a.Mobile.PairDialogOpen = true
}

func (a *App) refreshCompanionPairingCode() {
	a.Mobile.Err = ""
	if _, err := a.companion.StartPairing(); err != nil {
		a.Mobile.Err = userErrorText(err)
	}
}

func (a *App) submitCompanionRename(deviceID string) {
	if err := a.companion.RenameDevice(deviceID, a.Mobile.RenameInput); err != nil {
		a.Mobile.Err = userErrorText(err)
		return
	}
	a.Mobile.Err = ""
	a.Mobile.RenamingDeviceID = ""
	a.Mobile.RenameInput = ""
}

func formatCompanionTimestamp(raw string) string {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return raw
	}
	return t.Local().Format("1 月 2 日 15:04")
}

func nonEmptyUI(vals ...string) []string {
	var out []string
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			out = append(out, s)
		}
	}
	return out
}
