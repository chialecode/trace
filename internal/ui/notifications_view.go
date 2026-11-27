package ui

import (
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
)

const (
	notificationInfoAutoDismiss = 6 * time.Second
	notificationEdgeInset       = float32(16)
	notificationMaxWidth        = float32(380)
	notificationMinWidth        = float32(220)
)

// notifyInfo shows a transient bottom-right floating info bubble while keeping
// BannerInfo synchronized for callers and tests. Re-sending the same text
// refreshes the auto-dismiss countdown via bannerInfoSeq.
func (a *App) notifyInfo(msg string) {
	a.BannerInfo = msg
	a.bannerInfoSeq++
}

func (a *App) clearBannerInfo() {
	a.BannerInfo = ""
	a.bannerInfoTrackedText = ""
	a.bannerInfoTrackedSeq = a.bannerInfoSeq
	a.bannerInfoShownAt = time.Time{}
	a.bannerInfoSpent = 0
	a.bannerInfoPaused = false
}

func (a *App) shouldShowGlobalErrorBubble() bool {
	if a.BannerError == "" {
		return false
	}
	if a.CurrentRoute == RouteParse && a.BannerError == a.LastParseError {
		return false
	}
	if a.CurrentRoute == RouteLogin && a.BannerError == a.LoginError {
		return false
	}
	return true
}

func (a *App) syncBannerInfoTimer(c *ui.Context, now time.Time, paused bool) {
	if a.BannerInfo == "" {
		a.clearBannerInfo()
		return
	}
	if a.BannerInfo != a.bannerInfoTrackedText || a.bannerInfoSeq != a.bannerInfoTrackedSeq || a.bannerInfoShownAt.IsZero() {
		a.bannerInfoTrackedText = a.BannerInfo
		a.bannerInfoTrackedSeq = a.bannerInfoSeq
		a.bannerInfoShownAt = now
		a.bannerInfoSpent = 0
		a.bannerInfoPaused = paused
		c.Announce(a.BannerInfo)
	} else if paused != a.bannerInfoPaused {
		if paused {
			if d := now.Sub(a.bannerInfoShownAt); d > 0 {
				a.bannerInfoSpent += d
			}
		} else {
			a.bannerInfoShownAt = now
		}
		a.bannerInfoPaused = paused
	}

	elapsed := a.bannerInfoSpent
	if !a.bannerInfoPaused {
		if d := now.Sub(a.bannerInfoShownAt); d > 0 {
			elapsed += d
		}
	}
	if elapsed >= notificationInfoAutoDismiss {
		a.clearBannerInfo()
		return
	}
	if !a.bannerInfoPaused {
		c.After(notificationInfoAutoDismiss - elapsed)
	}
}

// renderFloatingNotifications renders all global notifications (update prompt,
// global error, global info, and any MyGo Context.Toast messages) inside a
// single non-modal PassThrough bottom-right floating stack so notifications
// never shift or occupy page layout space (DEC-086).
func (a *App) renderFloatingNotifications(c *ui.Context, w, h float32) {
	bubbleW := min(notificationMaxWidth, max(float32(200), w-notificationEdgeInset*2))
	now := c.Now()

	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(notificationEdgeInset).AlignItems(ui.End).Gap(10)

		paused := viewport.Hovered() || viewport.FocusWithin()
		a.syncBannerInfoTimer(c, now, paused)

		notice, hasUpdate := a.activeUpdateNotice()
		showError := a.shouldShowGlobalErrorBubble()
		if showError {
			if a.BannerError != a.bannerErrorTrackedText {
				a.bannerErrorTrackedText = a.BannerError
				c.Announce(a.BannerError)
			}
		} else {
			a.bannerErrorTrackedText = ""
		}
		showInfo := a.BannerInfo != ""

		if !hasUpdate && !showError && !showInfo && len(toasts) == 0 {
			return
		}

		ui.Column(c).
			Key("trace.notification-stack").
			Label("悬浮通知区").
			AlignItems(ui.End).
			Gap(10).
			PassThrough().
			Children(func() {
				if hasUpdate {
					a.renderUpdateNotificationBubble(c, notice, bubbleW, h)
				}
				if showError {
					a.renderErrorNotificationBubble(c, bubbleW)
				}
				if showInfo {
					a.renderInfoNotificationBubble(c, bubbleW)
				}
				for _, t := range toasts {
					renderToastNotificationBubble(c, t, bubbleW)
				}
			})
	})
}

func (a *App) renderUpdateNotificationBubble(c *ui.Context, notice updateNotice, bubbleW, winH float32) {
	card := ui.Column(c).
		Key("notify-update").
		Label("更新提示").
		Role(ui.RoleStatus).
		Width(bubbleW).
		Padding(12, 14).
		Radius(12).
		Background(ColorInfoBlueBg).
		Border(1, ui.RGBA(26, 115, 232, 0.24)).
		Shadow(0, 8, 24, 0, ui.RGBA(15, 23, 42, 0.14)).
		Gap(8).
		ClipX()
	_ = card.Hovered()
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
			DrawIcon(c, "check-circle", 16, ColorInfoBlue, 1.8)
			ui.Text(c, notice.text).FontSize(13).FontWeight(600).LineHeight(LineHeightCompact).TextColor(ColorInfoBlue).MaxLines(2).Grow(1).Shrink(1)
			closeBtn := ui.ButtonBase(c).Label("关闭更新提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
			if closeBtn.Hovered() {
				closeBtn.Background(ui.RGBA(26, 115, 232, 0.10))
			}
			closeBtn.Children(func() { DrawIcon(c, "close", 14, ColorTextMuted, 1.8) })
			if closeBtn.Clicked() {
				a.UpdateNotesOpen = false
				notice.dismiss()
			}
		})

		hasNotes := strings.TrimSpace(notice.notes) != ""
		if a.UpdateNotesOpen && hasNotes {
			maxNotesH := min(float32(220), max(float32(100), winH*0.32))
			ui.Scroll(c).
				Label("更新内容").
				FillWidth().
				MaxHeight(maxNotesH).
				Radius(8).
				Background(ColorBgMain).
				Border(1, ui.RGBA(26, 115, 232, 0.16)).
				Padding(8, 10).
				ClipX().
				Children(func() {
					ui.Column(c).FillWidth().Gap(3).ClipX().Children(func() {
						for _, line := range releaseNoteLines(notice.notes) {
							ui.Text(c, line).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(4)
						}
					})
				})
		}

		if hasNotes || notice.primary != "" {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
				if hasNotes {
					label := "更新内容"
					if a.UpdateNotesOpen {
						label = "收起更新内容"
					}
					link := ui.ButtonBase(c).Label(label).Padding(4, 6).Radius(6)
					if link.Hovered() {
						link.Background(ui.RGBA(26, 115, 232, 0.10))
					}
					link.Children(func() {
						ui.Text(c, label).FontSize(13).FontWeight(500).TextColor(ColorAccent).SingleLine()
					})
					if link.Clicked() {
						a.UpdateNotesOpen = !a.UpdateNotesOpen
					}
				} else {
					ui.Box(c).Grow(1)
				}
				if notice.primary != "" && AccentOutlinePillButton(c, notice.primary, notice.primaryLabel).Clicked() {
					a.onUpdatePrimary(notice.state)
				}
			})
		}
	})
}

func (a *App) renderErrorNotificationBubble(c *ui.Context, bubbleW float32) {
	card := ui.Column(c).
		Key("notify-error").
		Label("全局错误提示").
		Role(ui.RoleStatus).
		Width(bubbleW).
		Padding(12, 14).
		Radius(12).
		Background(ColorErrorBg).
		Border(1, ui.RGBA(196, 24, 46, 0.28)).
		Shadow(0, 8, 24, 0, ui.RGBA(15, 23, 42, 0.14)).
		Gap(8).
		ClipX()
	_ = card.Hovered()
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Start).Gap(8).ClipX().Children(func() {
			ui.Box(c).Padding(4, 0, 0, 0).Shrink(0).Children(func() {
				DrawIcon(c, "alert-circle", 16, ColorError, 1.8)
			})
			ui.Box(c).Padding(3, 0, 0, 0).Grow(1).Shrink(1).ClipX().Children(func() {
				ui.Text(c, a.BannerError).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorError).MaxLines(4)
			})
			closeBtn := ui.ButtonBase(c).Label("关闭错误提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
			if closeBtn.Hovered() {
				closeBtn.Background(ui.RGBA(196, 24, 46, 0.10))
			}
			closeBtn.Children(func() {
				DrawIcon(c, "close", 14, ColorError, 1.8)
			})
			if closeBtn.Clicked() {
				a.BannerError = ""
				a.BannerLoginSite = ""
			}
		})
		if a.BannerLoginSite != "" && a.CurrentRoute != RouteLogin {
			targetSite := a.BannerLoginSite
			ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(8).Children(func() {
				if AccentPillButton(c, "去登录", "提示去登录").Clicked() {
					a.BannerError = ""
					a.BannerLoginSite = ""
					a.OpenLoginForSite(targetSite)
				}
			})
		}
	})
}

func (a *App) renderInfoNotificationBubble(c *ui.Context, bubbleW float32) {
	card := ui.Row(c).
		Key("notify-info").
		Label("全局消息提示").
		Role(ui.RoleStatus).
		MinWidth(min(notificationMinWidth, bubbleW)).
		MaxWidth(bubbleW).
		Padding(10, 12).
		Radius(12).
		Background(ColorBgMain).
		Border(1, ColorBorderStrong).
		Shadow(0, 8, 24, 0, ui.RGBA(15, 23, 42, 0.14)).
		AlignItems(ui.Center).
		Gap(10).
		ClipX()
	_ = card.Hovered()
	card.Children(func() {
		ui.Box(c).Size(24, 24).Radius(999).Background(ColorInfoBlueBg).Center().Shrink(0).Children(func() {
			DrawIcon(c, "check-circle", 14, ColorInfoBlue, 1.8)
		})
		ui.Text(c, a.BannerInfo).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(3).Grow(1).Shrink(1)
		closeBtn := ui.ButtonBase(c).Label("关闭消息提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
		if closeBtn.Hovered() {
			closeBtn.Background(ColorHover)
		}
		closeBtn.Children(func() {
			DrawIcon(c, "close", 14, ColorTextMuted, 1.8)
		})
		if closeBtn.Clicked() {
			a.clearBannerInfo()
		}
	})
}

func renderToastNotificationBubble(c *ui.Context, t ui.Toast, bubbleW float32) {
	isErr := strings.EqualFold(t.Type, "error")
	bg := ColorBgMain
	border := ColorBorderStrong
	iconName := "check-circle"
	iconBg := ColorInfoBlueBg
	iconColor := ColorInfoBlue
	textColor := ColorTextMain
	if isErr {
		bg = ColorErrorBg
		border = ui.RGBA(196, 24, 46, 0.28)
		iconName = "alert-circle"
		iconBg = ui.RGBA(196, 24, 46, 0.12)
		iconColor = ColorError
		textColor = ColorError
	}

	toast := ui.ToastBase(c, t)
	toast.Root.
		Row().
		MinWidth(min(notificationMinWidth, bubbleW)).
		MaxWidth(bubbleW).
		Padding(10, 12).
		Radius(12).
		Background(bg).
		Border(1, border).
		Shadow(0, 8, 24, 0, ui.RGBA(15, 23, 42, 0.14)).
		AlignItems(ui.Center).
		Gap(10).
		ClipX()
	toast.Root.Children(func() {
		ui.Box(c).Size(24, 24).Radius(999).Background(iconBg).Center().Shrink(0).Children(func() {
			DrawIcon(c, iconName, 14, iconColor, 1.8)
		})
		if t.Description == "" {
			ui.Text(c, t.Title).FontSize(13).LineHeight(LineHeightCompact).TextColor(textColor).MaxLines(3).Grow(1).Shrink(1)
		} else {
			ui.Column(c).Grow(1).Shrink(1).Gap(2).ClipX().Children(func() {
				ui.Text(c, t.Title).FontSize(13).FontWeight(600).LineHeight(LineHeightCompact).TextColor(textColor).MaxLines(2)
				ui.Text(c, t.Description).FontSize(12).LineHeight(LineHeightCompact).TextColor(ColorTextMuted).MaxLines(3)
			})
		}
		if t.Action != "" {
			act := toast.ActionButton().Padding(4, 10).Radius(999).Border(1, ColorAccent).Background(ColorBgMain).Shrink(0)
			if act.Hovered() {
				act.Background(ColorAccentBg)
			}
			act.Children(func() {
				ui.Text(c, t.Action).FontSize(12).FontWeight(600).TextColor(ColorAccent).SingleLine()
			})
		}
		closeBtn := toast.CloseButton().Label("关闭通知").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
		if closeBtn.Hovered() {
			closeBtn.Background(ColorHover)
		}
		closeBtn.Children(func() {
			DrawIcon(c, "close", 14, ColorTextMuted, 1.8)
		})
	})
}
