package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/qrscanner"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo/ui"
)

type pairingStep int

const (
	pairStepGuide pairingStep = iota
	pairStepQRScan
	pairStepManualForm
	pairStepIdentityConfirm
	pairStepWaitingApproval
)

type pairingState struct {
	step pairingStep

	// QR scan / paste fallback
	qrInput          string
	qrInputComposing bool
	qrActive         bool
	qrUnsupported    bool
	qrNotice         string

	// Manual form
	manualAddr          string
	manualAddrComposing bool
	manualCode          string
	manualCodeComposing bool

	// Probed identity (step 3 -> 4)
	probedAddr         string
	probedFP           string
	probedIdentityCode string
	pendingShortCode   string

	// In-flight state
	busy       bool
	waitingMsg string
	errMsg     string
	cancel     context.CancelFunc

	// Whether user entered pairing via "重新扫码" while already paired
	rescanning bool
}

func (a *App) renderPairingGate(c *ui.Context) {
	ui.Column(c).Fill().Background(theme.ColorBgMain).Children(func() {
		// Top bar for sub-steps
		if a.pair.step != pairStepGuide || a.pair.rescanning {
			ui.Row(c).
				FillWidth().
				Padding(12, 16).
				Gap(10).
				AlignItems(ui.Center).
				BorderWidth(0, 0, 1, 0).
				BorderColor(theme.ColorBorder).
				Children(func() {
					backBtn := ui.ButtonBase(c).
						Label("返回").
						Size(44, 44).
						Radius(22).
						Center()
					if backBtn.Hovered() {
						backBtn.Background(theme.ColorHover)
					}
					backBtn.Children(func() {
						theme.DrawIcon(c, "arrow-left", 20, theme.ColorTextMain, 1.9)
					})
					if backBtn.Clicked() {
						a.handlePairingBack()
					}
					ui.Text(c, a.pairingStepTitle()).FontSize(17).FontWeight(600).TextColor(theme.ColorTextMain)
				})
		}

		ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
			ui.Column(c).
				FillWidth().
				Padding(24, 20, 32, 20).
				Gap(20).
				AlignItems(ui.Center).
				Children(func() {
					ui.Column(c).FillWidth().MaxWidth(440).Gap(18).Children(func() {
						a.renderPendingShareBanner(c, true)
						switch a.pair.step {
						case pairStepGuide:
							a.renderPairingGuide(c)
						case pairStepQRScan:
							a.renderPairingQR(c)
						case pairStepManualForm:
							a.renderPairingManualForm(c)
						case pairStepIdentityConfirm:
							a.renderPairingIdentityConfirm(c)
						case pairStepWaitingApproval:
							a.renderPairingWaiting(c)
						}

						if a.pair.errMsg != "" {
							ui.Row(c).
								FillWidth().
								Padding(12, 14).
								Radius(10).
								Background(theme.ColorErrorBg).
								Border(1, ui.RGBA(196, 24, 46, 0.25)).
								Gap(8).
								AlignItems(ui.Center).
								Children(func() {
									theme.DrawIcon(c, "alert-circle", 18, theme.ColorError, 1.8)
									ui.Text(c, a.pair.errMsg).
										FontSize(13).
										LineHeight(theme.LineHeightCompact).
										TextColor(theme.ColorError).
										Grow(1).Shrink(1)
								})
						}
					})
				})
		})
	})
}

func (a *App) pairingStepTitle() string {
	switch a.pair.step {
	case pairStepQRScan:
		return "扫码连接"
	case pairStepManualForm:
		return "手动输入配对"
	case pairStepIdentityConfirm:
		return "核对电脑身份码"
	case pairStepWaitingApproval:
		return "等待电脑确认"
	default:
		return "连接你的电脑"
	}
}

func (a *App) renderPairingGuide(c *ui.Context) {
	ui.Column(c).FillWidth().Gap(8).AlignItems(ui.Center).Padding(16, 0, 4, 0).Children(func() {
		ui.Box(c).
			Size(64, 64).
			Radius(18).
			Background(theme.ColorAccentBg).
			Center().
			Children(func() {
				theme.DrawIcon(c, "qr", 30, theme.ColorAccent, 1.9)
			})
		ui.Text(c, "连接你的电脑").FontSize(22).FontWeight(700).TextColor(theme.ColorTextMain)
		ui.Text(c, "手机是电脑端 TRACE 的查看器与遥控器，数据与站点登录均保留在电脑上。").
			FontSize(13).
			LineHeight(theme.LineHeightCompact).
			TextColor(theme.ColorTextMuted).
			TextAlign(ui.Center)
	})

	ui.Column(c).
		FillWidth().
		Padding(16, 16).
		Gap(12).
		Radius(12).
		Background(theme.ColorBgPanel).
		Border(1, theme.ColorBorder).
		Children(func() {
			renderGuideStep(c, "1", "在电脑上打开 TRACE")
			renderGuideStep(c, "2", "进入「设置 › 移动端」，开启「允许手机连接」")
			renderGuideStep(c, "3", "点击「添加手机」显示二维码或 8 位配对码")
		})

	ui.Column(c).FillWidth().Gap(10).Children(func() {
		if theme.PrimaryPillButton(c, "扫码连接", "扫码连接").FillWidth().Clicked() {
			a.pair.errMsg = ""
			a.enterQRScan()
		}
		if theme.SecondaryPillButton(c, "手动输入", "手动输入").FillWidth().Clicked() {
			a.stopQRScanner()
			a.pair.errMsg = ""
			a.pair.step = pairStepManualForm
		}
	})
}

func renderGuideStep(c *ui.Context, num, text string) {
	ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
		ui.Box(c).Size(24, 24).Radius(12).Background(theme.ColorAccent).Center().Children(func() {
			ui.Text(c, num).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
		})
		ui.Text(c, text).FontSize(14).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMain).Grow(1).Shrink(1)
	})
}

func (a *App) renderPairingQR(c *ui.Context) {
	ui.Column(c).
		FillWidth().
		Padding(16).
		Gap(12).
		Radius(12).
		Background(theme.ColorBgPanel).
		Border(1, theme.ColorBorder).
		AlignItems(ui.Center).
		Children(func() {
			ui.Box(c).
				Size(200, 200).
				Radius(16).
				Background(theme.ColorBgMain).
				Border(1, theme.ColorBorderStrong).
				Center().
				Children(func() {
					if a.qrScanner != nil && !a.pair.qrUnsupported {
						ui.NativeView(c, a.qrScanner).Fill().Radius(16)
					} else {
						theme.DrawIcon(c, "qr", 56, theme.ColorAccent, 1.6)
					}
				})
			ui.Text(c, "请扫描电脑端「添加手机」显示的二维码").
				FontSize(14).FontWeight(500).TextColor(theme.ColorTextMain).TextAlign(ui.Center)
			if a.pair.qrNotice != "" {
				ui.Text(c, a.pair.qrNotice).
					FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			} else {
				ui.Text(c, "如当前设备无相机或处于测试环境，可在下方粘贴二维码文本（trace-pair:…）或切换手动输入。").
					FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			}
		})

	ui.Column(c).FillWidth().Gap(8).Children(func() {
		ui.Text(c, "二维码文本").FontSize(13).FontWeight(500).TextColor(theme.ColorTextMuted)
		inp := ui.TextInput(c, &a.pair.qrInput).
			Label("二维码文本输入框").
			Placeholder("trace-pair:...").
			Capitalize(ui.CapitalizeNone).
			Autocorrect(false).
			ReturnKey(ui.ReturnGo).
			MinHeight(44)
		wasComposing := a.pair.qrInputComposing
		a.pair.qrInputComposing = inp.Composing()
		if inp.Submitted() && !inp.Composing() && !wasComposing && !a.pair.busy {
			a.submitQRText(a.pair.qrInput)
		}
		if theme.PrimaryPillButton(c, "解析并连接", "解析二维码并连接").FillWidth().Clicked() && !a.pair.busy {
			a.submitQRText(a.pair.qrInput)
		}
		if theme.SecondaryPillButton(c, "改用手动输入", "改用手动输入").FillWidth().Clicked() {
			a.stopQRScanner()
			a.pair.errMsg = ""
			a.pair.step = pairStepManualForm
		}
	})
}

func (a *App) renderPairingManualForm(c *ui.Context) {
	defaultHint := fmt.Sprintf("例如 192.168.1.20:%d", companionclient.DefaultPortForChannel(a.channel))
	ui.Column(c).FillWidth().Gap(14).Children(func() {
		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, "电脑地址").FontSize(13).FontWeight(500).TextColor(theme.ColorTextMuted)
			addrInp := ui.TextInput(c, &a.pair.manualAddr).
				Label("电脑地址输入框").
				Placeholder(defaultHint).
				Keyboard(ui.KeyboardURL).
				Capitalize(ui.CapitalizeNone).
				Autocorrect(false).
				ReturnKey(ui.ReturnNext).
				MinHeight(44)
			wasAddrComp := a.pair.manualAddrComposing
			a.pair.manualAddrComposing = addrInp.Composing()
			_ = wasAddrComp
		})

		ui.Column(c).FillWidth().Gap(6).Children(func() {
			ui.Text(c, "8 位配对码").FontSize(13).FontWeight(500).TextColor(theme.ColorTextMuted)
			codeInp := ui.TextInput(c, &a.pair.manualCode).
				Label("配对码输入框").
				Placeholder("例如 3F7K-9Q2M").
				Capitalize(ui.CapitalizeCharacters).
				Autocorrect(false).
				ReturnKey(ui.ReturnGo).
				MinHeight(44)
			wasCodeComp := a.pair.manualCodeComposing
			a.pair.manualCodeComposing = codeInp.Composing()
			if codeInp.Submitted() && !codeInp.Composing() && !wasCodeComp && !a.pair.busy {
				a.startManualProbe()
			}
		})

		ui.Text(c, "提交配对码前，手机会先建立安全连接并显示电脑身份码供你逐组核对。").
			FontSize(12).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted)

		btnLabel := "下一步"
		if a.pair.busy {
			btnLabel = "正在核对连接…"
		}
		if theme.PrimaryPillButton(c, btnLabel, "下一步核对身份码").FillWidth().Clicked() && !a.pair.busy {
			a.startManualProbe()
		}
	})
}

func (a *App) renderPairingIdentityConfirm(c *ui.Context) {
	ui.Column(c).
		FillWidth().
		Padding(20, 16).
		Gap(14).
		Radius(12).
		Background(theme.ColorBgPanel).
		Border(1, theme.ColorBorder).
		AlignItems(ui.Center).
		Children(func() {
			theme.DrawIcon(c, "shield-check", 34, theme.ColorAccent, 1.8)
			ui.Text(c, "电脑身份码").FontSize(15).FontWeight(600).TextColor(theme.ColorTextMain)
			ui.Box(c).
				FillWidth().
				Padding(14, 12).
				Radius(10).
				Background(theme.ColorBgMain).
				Border(1, theme.ColorBorderStrong).
				Center().
				Children(func() {
					ui.Text(c, a.pair.probedIdentityCode).
						Font(theme.MonoFont).
						FontSize(20).
						FontWeight(700).
						TextColor(theme.ColorTextMain).
						SingleLine()
				})
			ui.Text(c, "请与电脑上显示的逐组核对。若不一致，可能存在中间人拦截，请立即取消。").
				FontSize(13).
				LineHeight(theme.LineHeightBody).
				TextColor(theme.ColorTextMuted).
				TextAlign(ui.Center)
		})

	ui.Column(c).FillWidth().Gap(10).Children(func() {
		if theme.PrimaryPillButton(c, "一致，继续", "一致，继续配对").FillWidth().Clicked() && !a.pair.busy {
			a.confirmIdentityAndPair()
		}
		if theme.SecondaryPillButton(c, "不一致，取消", "不一致，取消配对").FillWidth().Clicked() {
			a.abortManualIdentity()
		}
	})
}

func (a *App) renderPairingWaiting(c *ui.Context) {
	ui.Column(c).
		FillWidth().
		Padding(24, 16).
		Gap(14).
		Radius(12).
		Background(theme.ColorBgPanel).
		Border(1, theme.ColorBorder).
		AlignItems(ui.Center).
		Children(func() {
			theme.DrawIcon(c, "computer", 36, theme.ColorAccent, 1.8)
			ui.Text(c, "等待电脑上确认…").Label("等待电脑确认").FontSize(18).FontWeight(600).TextColor(theme.ColorTextMain)
			if a.pair.waitingMsg != "" {
				ui.Text(c, a.pair.waitingMsg).FontSize(13).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
			}
			ui.Text(c, "请在电脑端弹窗中点击“允许”完成配对。").
				FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(theme.ColorTextMuted).TextAlign(ui.Center)
		})

	if theme.SecondaryPillButton(c, "取消", "取消等待确认").FillWidth().Clicked() {
		a.cancelPairing()
	}
}

func (a *App) enterQRScan() {
	a.pair.step = pairStepQRScan
	a.ensureQRScanner()
}

func (a *App) ensureQRScanner() {
	if a.pair.qrUnsupported {
		return
	}
	if a.qrScanner == nil {
		sc, err := qrscanner.NewScanner()
		if err != nil {
			a.pair.qrUnsupported = true
			if !errors.Is(err, errors.ErrUnsupported) {
				a.pair.qrNotice = err.Error()
			}
			return
		}
		a.qrScanner = sc
	}
	if !a.pair.qrActive {
		a.pair.qrActive = true
		a.pair.qrNotice = ""
		a.qrScanner.Start(func(payload string) {
			a.Update(func() {
				if !a.pair.qrActive || a.pair.step != pairStepQRScan || a.pair.busy {
					return
				}
				a.submitQRText(payload)
			})
		}, func(msg string) {
			a.Update(func() {
				if !a.pair.qrActive {
					return
				}
				a.pair.qrNotice = msg
				a.pair.qrUnsupported = true
				a.stopQRScanner()
			})
		})
	}
}

func (a *App) stopQRScanner() {
	a.pair.qrActive = false
	if a.qrScanner != nil {
		a.qrScanner.Stop()
		_ = a.qrScanner.Close()
		a.qrScanner = nil
	}
}

func (a *App) handlePairingBack() {
	a.cancelPairing()
	a.stopQRScanner()
	a.pair.errMsg = ""
	switch a.pair.step {
	case pairStepIdentityConfirm:
		a.abortManualIdentity()
	case pairStepWaitingApproval, pairStepQRScan, pairStepManualForm:
		a.mu.Lock()
		rescan := a.pair.rescanning
		a.mu.Unlock()
		if rescan {
			if _, ok := a.store.LoadPairing(); ok {
				a.mu.Lock()
				a.pair.rescanning = false
				a.mu.Unlock()
				a.pair.step = pairStepGuide
				a.syncGateVisibility()
				return
			}
		}
		a.pair.step = pairStepGuide
	default:
		a.mu.Lock()
		wasRescan := a.pair.rescanning
		a.pair.rescanning = false
		a.mu.Unlock()
		if wasRescan {
			a.syncGateVisibility()
		}
	}
}

func (a *App) cancelPairing() {
	if a.pair.cancel != nil {
		a.pair.cancel()
		a.pair.cancel = nil
	}
	a.pair.busy = false
}

func (a *App) abortManualIdentity() {
	a.cancelPairing()
	a.pair.probedAddr = ""
	a.pair.probedFP = ""
	a.pair.probedIdentityCode = ""
	a.pair.pendingShortCode = ""
	a.pair.errMsg = ""
	a.pair.step = pairStepManualForm
}

func (a *App) submitQRText(raw string) {
	qr, err := companionclient.ParseQRCode(raw)
	if err != nil {
		a.pair.errMsg = err.Error()
		return
	}
	if qr.Channel != a.channel {
		a.pair.errMsg = (&companionclient.Error{Kind: companionclient.KindChannelMismatch}).UserMessage()
		return
	}

	// Design §4.7: Re-scanning QR of same node + same fingerprint sends POST /pair
	// with existing Bearer token to invalidate the pairing code and update addresses
	// without showing desktop confirmation.
	a.stopQRScanner()
	if existing, ok := a.store.LoadPairing(); ok && existing.SameIdentity(qr) {
		if tok, err := a.store.LoadToken(); err == nil && tok != "" {
			a.beginRescanUpdateFlow(existing, tok, qr)
			return
		}
	}

	a.beginPairFlow(qr.Addrs, qr.Fingerprint, qr.Code, qr.Node, qr.Name)
}

func (a *App) startManualProbe() {
	defaultPort := companionclient.DefaultPortForChannel(a.channel)
	addr, err := companionclient.NormalizeEndpoint(a.pair.manualAddr, defaultPort)
	if err != nil {
		a.pair.errMsg = err.Error()
		return
	}
	shortCode, err := companionclient.NormalizeShortCode(a.pair.manualCode)
	if err != nil {
		a.pair.errMsg = err.Error()
		return
	}
	a.pair.errMsg = ""
	a.pair.busy = true

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	a.pair.cancel = cancel

	go func() {
		fp, idCode, err := a.probeFn(ctx, addr)
		a.Update(func() {
			a.pair.busy = false
			a.pair.cancel = nil
			if err != nil {
				if companionclient.KindOf(err) == companionclient.KindCanceled {
					return
				}
				a.pair.errMsg = err.Error()
				return
			}
			a.pair.probedAddr = addr
			a.pair.probedFP = fp
			a.pair.probedIdentityCode = idCode
			a.pair.pendingShortCode = shortCode
			a.pair.step = pairStepIdentityConfirm
		})
	}()
}

func (a *App) confirmIdentityAndPair() {
	if a.pair.probedAddr == "" || a.pair.probedFP == "" || a.pair.pendingShortCode == "" {
		a.pair.errMsg = "请重新核对电脑地址与配对码"
		a.pair.step = pairStepManualForm
		return
	}
	a.beginPairFlow([]string{a.pair.probedAddr}, a.pair.probedFP, a.pair.pendingShortCode, "", "")
}

func (a *App) beginRescanUpdateFlow(existing companionclient.PairingRecord, tok string, qr companionapi.PairPayload) {
	a.cancelPairing()
	a.pair.errMsg = ""
	a.pair.busy = true
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	a.pair.cancel = cancel

	req := companionapi.PairRequest{
		Code:       qr.Code,
		DeviceName: a.deviceName,
		Model:      a.deviceModel,
		Platform:   "android",
		Channel:    a.channel,
		AppVersion: a.appVersion,
	}
	go func() {
		freshAddrs, winAddr, err := companionclient.RescanUpdateAddrs(ctx, qr.Addrs, qr.Fingerprint, tok, req)
		a.Update(func() {
			a.pair.busy = false
			a.pair.cancel = nil
			if err != nil {
				if companionclient.KindOf(err) == companionclient.KindCanceled {
					return
				}
				if companionclient.KindOf(err) == companionclient.KindUnauthorized {
					// Desktop removed this device: fall back to full pairing.
					a.beginPairFlow(qr.Addrs, qr.Fingerprint, qr.Code, qr.Node, qr.Name)
					return
				}
				a.pair.errMsg = err.Error()
				return
			}
			existing.Addrs = freshAddrs
			existing.Name = qr.Name
			existing.LastAddr = winAddr
			_ = a.store.SavePairing(existing)
			a.mu.Lock()
			a.pair.rescanning = false
			a.mu.Unlock()
			a.pair.step = pairStepGuide
			a.pair.errMsg = ""
			a.syncGateVisibility()
			a.RetryNow()
		})
	}()
}

func (a *App) beginPairFlow(addrs []string, fp, code, expectedNode, expectedName string) {
	a.cancelPairing()
	a.pair.errMsg = ""
	a.pair.busy = true
	a.pair.step = pairStepWaitingApproval
	a.pair.waitingMsg = strings.Join(addrs, " / ")

	ctx, cancel := context.WithTimeout(context.Background(), 140*time.Second)
	a.pair.cancel = cancel

	req := companionapi.PairRequest{
		Code:       code,
		DeviceName: a.deviceName,
		Model:      a.deviceModel,
		Platform:   "android",
		Channel:    a.channel,
		AppVersion: a.appVersion,
	}

	go func() {
		tok, winAddr, meta, err := a.pairFn(ctx, addrs, fp, req, func(addr string) {
			a.Update(func() {
				a.pair.waitingMsg = "正在等待 " + addr + " 确认…"
			})
		})
		a.Update(func() {
			a.pair.busy = false
			a.pair.cancel = nil
			if err != nil {
				if companionclient.KindOf(err) == companionclient.KindCanceled {
					return
				}
				a.pair.errMsg = err.Error()
				a.pair.step = pairStepGuide
				return
			}
			node := meta.Node
			if node == "" {
				node = expectedNode
			}
			name := meta.Name
			if name == "" {
				name = expectedName
			}
			rec := companionclient.PairingRecord{
				Node:     node,
				Name:     name,
				Channel:  a.channel,
				FP:       fp,
				Addrs:    addrs,
				LastAddr: winAddr,
				PairedAt: time.Now(),
			}
			if err := a.store.SaveToken(tok); err != nil {
				a.pair.errMsg = "保存设备令牌失败：" + err.Error()
				a.pair.step = pairStepGuide
				return
			}
			if err := a.store.SavePairing(rec); err != nil {
				_ = a.store.ClearAll()
				a.pair.errMsg = "保存配对记录失败：" + err.Error()
				a.pair.step = pairStepGuide
				return
			}
			a.mu.Lock()
			a.pair = pairingState{}
			a.mu.Unlock()
			a.syncGateVisibility()
			a.RetryNow()
		})
	}()
}
