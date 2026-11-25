package ui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// renderDialog displays a modal dialog over the window while *open is true.
// Clicking the backdrop or pressing Escape sets *open to false and calls
// onDismiss (if non-nil). When height > 0 the panel uses fixed Size(width,
// height) with Clip(); otherwise it uses Width(width) with ClipX().
func renderDialog(c *ui.Context, open *bool, backdropLabel, panelLabel string, width, height float32, onDismiss func(), body func()) {
	if open == nil || !*open {
		return
	}
	dlg := ui.DialogBase(c, open, func(backdrop, panel ui.Element) {
		if backdropLabel != "" {
			backdrop.Label(backdropLabel)
		}
		backdrop.Background(ui.RGBA(15, 20, 25, 0.55))
		panel.
			Column().
			Padding(22).
			Radius(16).
			Background(ColorBgMain).
			Border(1, ColorBorder).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.24)).
			Gap(12)
		if panelLabel != "" {
			panel.Label(panelLabel)
		}
		if height > 0 {
			panel.Size(width, height).Clip()
		} else {
			panel.Width(width).ClipX()
		}
		panel.Children(body)
	})
	if dlg.Dismissed() && onDismiss != nil {
		onDismiss()
	}
}

// renderConfirmDialog displays a standard two-button confirmation dialog.
// When danger is true the confirm button uses DangerOutlinePillButton;
// otherwise it uses AccentPillButton.
func renderConfirmDialog(c *ui.Context, open *bool, backdropLabel, panelLabel, title, body, cancelText, cancelLabel, confirmText, confirmLabel string, danger bool, onConfirm func()) {
	winW, _ := c.Size()
	w := min(float32(460), max(float32(300), winW-40))
	renderDialog(c, open, backdropLabel, panelLabel, w, 0, nil, func() {
		ui.Text(c, title).FontSize(18).FontWeight(600).TextColor(ColorTextMain)
		if body != "" {
			ui.Text(c, body).FontSize(13).TextColor(ColorTextMuted).MaxLines(4)
		}
		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			if OutlinePillButton(c, cancelText, cancelLabel).Clicked() {
				*open = false
			}
			var confirmBtn ui.Element
			if danger {
				confirmBtn = DangerOutlinePillButton(c, confirmText, confirmLabel)
			} else {
				confirmBtn = AccentPillButton(c, confirmText, confirmLabel)
			}
			if confirmBtn.Clicked() {
				*open = false
				if onConfirm != nil {
					onConfirm()
				}
			}
		})
	})
}

func (a *App) renderRuleBatchOverlay(c *ui.Context, w, h float32) {
	if !a.RuleBatch.Open {
		return
	}
	cardW := min(float32(540), max(float32(320), w-40))
	cardH := min(float32(580), max(float32(320), h-80))
	renderDialog(c, &a.RuleBatch.Open, "关闭选择订阅对话框（点击空白处）", "选择订阅对话框", cardW, cardH, a.closeRuleBatchDialog, func() {
		a.renderRuleBatchDialogBody(c)
	})
}

func (a *App) renderMultiTagBatchOverlay(c *ui.Context, w, h float32) {
	if !a.MultiTagBatch.Open {
		return
	}
	cardW := min(float32(580), max(float32(320), w-40))
	cardH := min(float32(640), max(float32(360), h-60))
	renderDialog(c, &a.MultiTagBatch.Open, "关闭批量设置缓存标签对话框（点击空白处）", "批量设置缓存标签对话框", cardW, cardH, a.closeMultiTagBatchDialog, func() {
		a.renderMultiTagBatchDialogBody(c)
	})
}

func (a *App) renderLogoutConfirmOverlay(c *ui.Context, w, h float32) {
	targetSite := normalizeSiteIDUI(a.ConfirmLogoutSite)
	titleText := "退出登录？"
	bodyText := "退出后会暂停哔哩哔哩的缓存任务并停止录制，抖音、微博与 X 不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	switch targetSite {
	case "douyin":
		titleText = "退出抖音登录？"
		bodyText = "退出后会暂停抖音的缓存任务并停止抖音录制，哔哩哔哩、微博与 X 不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	case "weibo":
		titleText = "退出微博登录？"
		bodyText = "退出后会暂停微博的缓存任务，哔哩哔哩、抖音与 X 不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	case "x":
		titleText = "退出 X 登录？"
		bodyText = "退出后会暂停 X 的缓存任务，哔哩哔哩、抖音与微博不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	}
	renderConfirmDialog(
		c,
		&a.ConfirmLogoutOpen,
		"关闭退出登录对话框（点击空白处）",
		"退出登录确认对话框",
		titleText,
		bodyText,
		"取消",
		"取消退出登录",
		"退出登录",
		"确认退出登录",
		true,
		func() {
			a.logoutSiteAccount(targetSite)
		},
	)
}

func (a *App) renderRemotePushXConfirmOverlay(c *ui.Context, w, h float32) {
	renderConfirmDialog(
		c,
		&a.Remote.ConfirmPushXOpen,
		"关闭同步XCookie确认对话框（点击空白处）",
		"同步XCookie风险确认对话框",
		"确认同步 X Cookie 到远程？",
		"在云服务器的网络上用网页 Cookie 访问 X，被要求验证或锁号的可能明显更高，不建议本地和远程同时用同一个账号。本次确认仅对这一次同步生效。",
		"取消",
		"取消同步XCookie到远程",
		"仍然同步",
		"确认同步XCookie到远程",
		true,
		func() {
			a.pushRemoteCookie("x")
		},
	)
}

func (a *App) renderDeleteConfirmOverlay(c *ui.Context, w, h float32) {
	cardW := min(float32(480), max(float32(300), w-40))
	renderDialog(c, &a.DeleteConfirm.Open, "关闭删除确认对话框（点击空白处）", "删除确认对话框", cardW, 0, nil, func() {
		ui.Text(c, "删除缓存确认").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
		ui.Text(c, fmt.Sprintf("%s：%s", itemKindLabel(a.DeleteConfirm.Item.Kind), deleteConfirmTitle(a.DeleteConfirm.Item))).
			FontSize(14).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
		ui.Text(c, fmt.Sprintf("预计清理已登记缓存文件：%d 个（共 %s）",
			a.DeleteConfirm.FileCount,
			FormatBytes(a.DeleteConfirm.TotalBytes))).
			FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		if n := len(a.DeleteConfirm.Plan.ItemKeys) - 1; n > 0 {
			ui.Text(c, fmt.Sprintf("同时删除这条动态引用的 %d 个视频（没有单独缓存，也没有别的动态引用）", n)).
				FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
		}
		if len(a.DeleteConfirm.Plan.PreservedSharedVideos) > 0 {
			ui.Box(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorInfoBlueBg).Children(func() {
				ui.Text(c, fmt.Sprintf("这条动态引用的 %d 个视频单独缓存过或还被别的动态引用，将保留", len(a.DeleteConfirm.Plan.PreservedSharedVideos))).
					FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).MaxLines(2)
			})
		}
		if len(a.DeleteConfirm.Plan.PreservedUserFiles) > 0 {
			ui.Box(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorInfoBlueBg).Children(func() {
				ui.Text(c, fmt.Sprintf("安全保留：目录内 %d 个不是 TRACE 创建的文件将原样保留", len(a.DeleteConfirm.Plan.PreservedUserFiles))).
					FontSize(12).FontWeight(600).TextColor(ColorInfoBlue).MaxLines(2)
			})
		}

		ui.Row(c).
			FillWidth().
			Padding(10, 12).
			Radius(10).
			Background(ColorBgMain).
			Border(1, ColorBorder).
			AlignItems(ui.Center).
			Justify(ui.SpaceBetween).
			Children(func() {
				ui.Text(c, "同时删除磁盘上的 TRACE 缓存文件").FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine()
				ui.Switch(c, &a.DeleteConfirm.DeleteFiles).Label("同时删除磁盘文件开关")
			})

		ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
			if OutlinePillButton(c, "取消", "取消删除").Clicked() {
				a.DeleteConfirm.Open = false
			}
			if AccentPillButton(c, "确认删除", "确认删除").Clicked() {
				deletedKey := a.DeleteConfirm.Item.ItemKey
				err := a.backend.DeleteLibraryItem(deletedKey, a.DeleteConfirm.DeleteFiles)
				if err != nil {
					a.showError(err)
				} else {
					if len(a.DeleteConfirm.Plan.PreservedUserFiles) > 0 {
						a.BannerInfo = fmt.Sprintf("已删除缓存记录，并保留了 %d 个不是 TRACE 创建的文件", len(a.DeleteConfirm.Plan.PreservedUserFiles))
					} else {
						a.BannerInfo = "已删除：" + deleteConfirmTitle(a.DeleteConfirm.Item)
					}
					if a.CurrentRoute == RouteLibraryItem && a.CurrentLibraryDetail != nil && a.CurrentLibraryDetail.ItemKey == deletedKey {
						a.CurrentLibraryDetail = nil
						a.Navigate(RouteLibrary)
					}
				}
				a.DeleteConfirm.Open = false
				a.RefreshData()
			}
		})
	})
}
