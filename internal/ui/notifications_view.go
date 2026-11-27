package ui

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"
)

const (
	notificationEdgeInset      = float32(16)
	notificationMaxWidth       = float32(380)
	notificationPeekStepY      = float32(7)
	notificationPeekStepInsetX = float32(10)
	maxFloatingNotifications   = 50
)

type floatingNoticeKind int

const (
	floatingNoticeInfo floatingNoticeKind = iota
	floatingNoticeError
	floatingNoticeUpdate
)

type floatingNotice struct {
	id        uint64
	kind      floatingNoticeKind
	text      string
	loginSite string
}

// notifyInfo pushes a persistent info notification onto the top of the
// bottom-right floating notification stack while keeping BannerInfo updated.
func (a *App) notifyInfo(msg string) {
	if msg == "" {
		a.clearInfoNotifications()
		return
	}
	a.BannerInfo = msg
	a.bannerInfoTrackedText = msg
	a.pushFloatingNotice(floatingNotice{
		kind: floatingNoticeInfo,
		text: msg,
	})
}

// notifyError pushes a persistent error notification onto the top of the
// bottom-right floating notification stack while keeping BannerError updated.
func (a *App) notifyError(msg, loginSite string) {
	if msg == "" {
		a.clearErrorNotifications()
		return
	}
	a.BannerError = msg
	a.BannerLoginSite = loginSite
	a.bannerErrorTrackedText = msg
	a.bannerErrorTrackedSite = loginSite
	a.pushFloatingNotice(floatingNotice{
		kind:      floatingNoticeError,
		text:      msg,
		loginSite: loginSite,
	})
}

// ClearNotifications clears all active floating info and error notifications.
func (a *App) ClearNotifications() {
	a.clearInfoNotifications()
	a.clearErrorNotifications()
	a.lastAnnouncedNoticeID = 0
}

func (a *App) clearInfoNotifications() {
	a.BannerInfo = ""
	a.bannerInfoTrackedText = ""
	a.removeFloatingNoticesByKind(floatingNoticeInfo)
}

func (a *App) clearErrorNotifications() {
	a.BannerError = ""
	a.BannerLoginSite = ""
	a.bannerErrorTrackedText = ""
	a.bannerErrorTrackedSite = ""
	a.removeFloatingNoticesByKind(floatingNoticeError)
}

func (a *App) pushFloatingNotice(n floatingNotice) {
	// Promote identical notice to top instead of stacking exact duplicates.
	for i := range a.notificationStack {
		cur := a.notificationStack[i]
		if cur.kind == n.kind && cur.text == n.text && cur.loginSite == n.loginSite {
			a.notificationStack = append(a.notificationStack[:i], a.notificationStack[i+1:]...)
			break
		}
	}
	a.notificationNextID++
	n.id = a.notificationNextID
	a.notificationStack = append(a.notificationStack, n)
	if len(a.notificationStack) > maxFloatingNotifications {
		a.notificationStack = a.notificationStack[len(a.notificationStack)-maxFloatingNotifications:]
	}
}

func (a *App) removeFloatingNoticesByKind(kind floatingNoticeKind) {
	kept := a.notificationStack[:0]
	for _, n := range a.notificationStack {
		if n.kind != kind {
			kept = append(kept, n)
		}
	}
	a.notificationStack = kept
}

func (a *App) dismissFloatingNotice(id uint64, notice updateNotice, hasUpdate bool) {
	var removed floatingNotice
	found := false
	for i := range a.notificationStack {
		if a.notificationStack[i].id == id {
			removed = a.notificationStack[i]
			a.notificationStack = append(a.notificationStack[:i], a.notificationStack[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return
	}
	switch removed.kind {
	case floatingNoticeInfo:
		a.syncBannerInfoFromStack()
	case floatingNoticeError:
		a.syncBannerErrorFromStack()
	case floatingNoticeUpdate:
		a.UpdateNotesOpen = false
		a.updateNoticeTrackedKey = ""
		if hasUpdate && notice.dismiss != nil {
			notice.dismiss()
		}
	}
}

func (a *App) syncBannerInfoFromStack() {
	for i := len(a.notificationStack) - 1; i >= 0; i-- {
		if a.notificationStack[i].kind == floatingNoticeInfo {
			a.BannerInfo = a.notificationStack[i].text
			a.bannerInfoTrackedText = a.BannerInfo
			return
		}
	}
	a.BannerInfo = ""
	a.bannerInfoTrackedText = ""
}

func (a *App) syncBannerErrorFromStack() {
	for i := len(a.notificationStack) - 1; i >= 0; i-- {
		if a.notificationStack[i].kind == floatingNoticeError {
			a.BannerError = a.notificationStack[i].text
			a.BannerLoginSite = a.notificationStack[i].loginSite
			a.bannerErrorTrackedText = a.BannerError
			a.bannerErrorTrackedSite = a.BannerLoginSite
			return
		}
	}
	a.BannerError = ""
	a.BannerLoginSite = ""
	a.bannerErrorTrackedText = ""
	a.bannerErrorTrackedSite = ""
}

func (a *App) clearInlineMatchedError(oldErr string) {
	if oldErr == "" {
		return
	}
	removed := false
	kept := a.notificationStack[:0]
	for _, n := range a.notificationStack {
		if n.kind == floatingNoticeError && n.text == oldErr {
			removed = true
			continue
		}
		kept = append(kept, n)
	}
	a.notificationStack = kept
	if removed || a.BannerError == oldErr {
		a.syncBannerErrorFromStack()
	}
}

func (a *App) setLoginError(msg string) {
	if a.LoginError != "" && a.LoginError != msg {
		a.clearInlineMatchedError(a.LoginError)
	}
	a.LoginError = msg
}

func (a *App) setLastParseError(msg string) {
	if a.LastParseError != "" && a.LastParseError != msg {
		a.clearInlineMatchedError(a.LastParseError)
	}
	a.LastParseError = msg
}

func (a *App) isInlineDuplicateError(text string) bool {
	if text == "" {
		return false
	}
	if a.CurrentRoute == RouteParse && text == a.LastParseError {
		return true
	}
	if a.CurrentRoute == RouteLogin && text == a.LoginError {
		return true
	}
	return false
}

// syncNotificationStack reconciles direct field updates on BannerError /
// BannerInfo and live updater state with the ordered LIFO notification stack.
func (a *App) syncNotificationStack() (updateNotice, bool) {
	notice, hasUpdate := a.activeUpdateNotice()
	if !hasUpdate {
		a.removeFloatingNoticesByKind(floatingNoticeUpdate)
		a.updateNoticeTrackedKey = ""
	} else {
		key := fmt.Sprintf("%s|%s|%s", notice.state.Phase, notice.text, notice.primary)
		if key != a.updateNoticeTrackedKey {
			a.removeFloatingNoticesByKind(floatingNoticeUpdate)
			a.pushFloatingNotice(floatingNotice{
				kind: floatingNoticeUpdate,
				text: notice.text,
			})
			a.updateNoticeTrackedKey = key
		} else {
			hasEntry := false
			for _, item := range a.notificationStack {
				if item.kind == floatingNoticeUpdate {
					hasEntry = true
					break
				}
			}
			if !hasEntry {
				a.pushFloatingNotice(floatingNotice{
					kind: floatingNoticeUpdate,
					text: notice.text,
				})
			}
		}
	}

	if a.BannerInfo == "" {
		if a.bannerInfoTrackedText != "" {
			a.clearInfoNotifications()
		}
	} else if a.BannerInfo != a.bannerInfoTrackedText {
		a.notifyInfo(a.BannerInfo)
	}

	if a.BannerError == "" {
		if a.bannerErrorTrackedText != "" {
			a.clearErrorNotifications()
		}
	} else if a.BannerError != a.bannerErrorTrackedText || a.BannerLoginSite != a.bannerErrorTrackedSite {
		a.notifyError(a.BannerError, a.BannerLoginSite)
	}

	return notice, hasUpdate
}

func floatingNoticeStyle(kind floatingNoticeKind) (bg, border ui.Color) {
	switch kind {
	case floatingNoticeError:
		return ColorErrorBg, ui.RGBA(196, 24, 46, 0.28)
	case floatingNoticeUpdate:
		return ColorInfoBlueBg, ui.RGBA(26, 115, 232, 0.24)
	default:
		return ColorBgMain, ColorBorderStrong
	}
}

// renderFloatingNotifications renders all global notifications (update prompt,
// global error, global info, and any MyGo Context.Toast messages) inside a
// single non-modal PassThrough bottom-right overlay deck so notifications never
// shift page layout, never auto-dismiss, visually overlay newer notices on top
// of older ones, and let the user dismiss them one by one (DEC-086).
func (a *App) renderFloatingNotifications(c *ui.Context, w, h float32) {
	bubbleW := min(notificationMaxWidth, max(float32(220), w-notificationEdgeInset*2))
	notice, hasUpdate := a.syncNotificationStack()

	visible := make([]floatingNotice, 0, len(a.notificationStack))
	for _, item := range a.notificationStack {
		if item.kind == floatingNoticeError && a.isInlineDuplicateError(item.text) {
			continue
		}
		if item.kind == floatingNoticeUpdate && !hasUpdate {
			continue
		}
		visible = append(visible, item)
	}

	ui.ToastViewportBase(c, func(viewport ui.Element, toasts []ui.Toast) {
		viewport.Padding(notificationEdgeInset).AlignItems(ui.End).Gap(10)

		total := len(visible) + len(toasts)
		if total == 0 {
			a.lastAnnouncedNoticeID = 0
			return
		}

		// Collect styles of notifications sitting underneath the topmost bubble
		// so stacked deck rims preview the kind of notice waiting below.
		type deckLayer struct {
			bg     ui.Color
			border ui.Color
		}
		var under []deckLayer
		if len(toasts) > 0 {
			for _, item := range visible {
				bg, bd := floatingNoticeStyle(item.kind)
				under = append(under, deckLayer{bg: bg, border: bd})
			}
			for i := 0; i < len(toasts)-1; i++ {
				kind := floatingNoticeInfo
				if strings.EqualFold(toasts[i].Type, "error") {
					kind = floatingNoticeError
				}
				bg, bd := floatingNoticeStyle(kind)
				under = append(under, deckLayer{bg: bg, border: bd})
			}
		} else {
			for i := 0; i < len(visible)-1; i++ {
				bg, bd := floatingNoticeStyle(visible[i].kind)
				under = append(under, deckLayer{bg: bg, border: bd})
			}
		}

		behind := len(under)
		peekCount := min(behind, 2)
		topOffset := float32(peekCount) * notificationPeekStepY

		ui.Box(c).
			Key("trace.notification-stack").
			Label("悬浮通知区").
			Width(bubbleW).
			Padding(topOffset, 0, 0, 0).
			PassThrough().
			Children(func() {
				// Draw back-most stacked rims with negative ZIndex so the topmost card
				// (ZIndex 1) always paints strictly above them and covers their lower half.
				for level := peekCount; level >= 1; level-- {
					layer := under[len(under)-level]
					insetX := float32(level) * notificationPeekStepInsetX
					topY := float32(peekCount-level) * notificationPeekStepY
					ui.Box(c).
						Key(fmt.Sprintf("notify-peek-%d", level)).
						ZIndex(-level).
						Absolute().
						Top(topY).
						Left(insetX).
						Width(bubbleW-insetX*2).
						Height(28).
						Radius(12).
						Background(layer.bg).
						Border(1, layer.border).
						Shadow(0, 4, 12, 0, ui.RGBA(15, 23, 42, 0.08)).
						PassThrough()
				}

				ui.Box(c).ZIndex(1).FillWidth().PassThrough().Children(func() {
					if len(toasts) > 0 {
						topToast := toasts[len(toasts)-1]
						renderToastNotificationBubble(c, topToast, bubbleW, behind)
						return
					}

					top := visible[len(visible)-1]
					if top.id != a.lastAnnouncedNoticeID {
						a.lastAnnouncedNoticeID = top.id
						c.Announce(top.text)
					}
					switch top.kind {
					case floatingNoticeUpdate:
						a.renderUpdateNotificationBubble(c, top.id, notice, bubbleW, h, behind)
					case floatingNoticeError:
						a.renderErrorNotificationBubble(c, top, bubbleW, behind)
					default:
						a.renderInfoNotificationBubble(c, top, bubbleW, behind)
					}
				})
			})
	})
}

func renderStackedCountBadge(c *ui.Context, behind int, bg, fg ui.Color) {
	if behind <= 0 {
		return
	}
	ui.Box(c).
		Label("剩余通知数").
		Tooltip(fmt.Sprintf("下方还有 %d 条通知，关闭当前通知后显示", behind)).
		Padding(2, 7).
		Radius(999).
		Background(bg).
		Shrink(0).
		Children(func() {
			ui.Text(c, fmt.Sprintf("还有 %d 条", behind)).FontSize(11).FontWeight(600).TextColor(fg).SingleLine()
		})
}

func (a *App) renderUpdateNotificationBubble(c *ui.Context, noticeID uint64, notice updateNotice, bubbleW, winH float32, behind int) {
	card := ui.Column(c).
		Key("notify-update").
		Label("更新提示").
		Role(ui.RoleGroup).
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
			renderStackedCountBadge(c, behind, ui.RGBA(26, 115, 232, 0.12), ColorInfoBlue)
			closeBtn := ui.ButtonBase(c).Label("关闭更新提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
			if closeBtn.Hovered() {
				closeBtn.Background(ui.RGBA(26, 115, 232, 0.10))
			}
			closeBtn.Children(func() { DrawIcon(c, "close", 14, ColorTextMuted, 1.8) })
			if closeBtn.Clicked() {
				a.dismissFloatingNotice(noticeID, notice, true)
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

func (a *App) renderErrorNotificationBubble(c *ui.Context, item floatingNotice, bubbleW float32, behind int) {
	card := ui.Column(c).
		Key("notify-error").
		Label("全局错误提示").
		Role(ui.RoleGroup).
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
				ui.Text(c, item.text).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorError).MaxLines(4)
			})
			if behind > 0 {
				ui.Box(c).Padding(3, 0, 0, 0).Shrink(0).Children(func() {
					renderStackedCountBadge(c, behind, ui.RGBA(196, 24, 46, 0.12), ColorError)
				})
			}
			closeBtn := ui.ButtonBase(c).Label("关闭错误提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
			if closeBtn.Hovered() {
				closeBtn.Background(ui.RGBA(196, 24, 46, 0.10))
			}
			closeBtn.Children(func() {
				DrawIcon(c, "close", 14, ColorError, 1.8)
			})
			if closeBtn.Clicked() {
				a.dismissFloatingNotice(item.id, updateNotice{}, false)
			}
		})
		if item.loginSite != "" && a.CurrentRoute != RouteLogin {
			targetSite := item.loginSite
			ui.Row(c).FillWidth().Justify(ui.End).AlignItems(ui.Center).Gap(8).Children(func() {
				if AccentPillButton(c, "去登录", "提示去登录").Clicked() {
					a.dismissFloatingNotice(item.id, updateNotice{}, false)
					a.OpenLoginForSite(targetSite)
				}
			})
		}
	})
}

func (a *App) renderInfoNotificationBubble(c *ui.Context, item floatingNotice, bubbleW float32, behind int) {
	card := ui.Row(c).
		Key("notify-info").
		Label("全局消息提示").
		Role(ui.RoleGroup).
		Width(bubbleW).
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
		ui.Text(c, item.text).FontSize(13).LineHeight(LineHeightCompact).TextColor(ColorTextMain).MaxLines(3).Grow(1).Shrink(1)
		renderStackedCountBadge(c, behind, ColorBgSecondary, ColorTextMuted)
		closeBtn := ui.ButtonBase(c).Label("关闭消息提示").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
		if closeBtn.Hovered() {
			closeBtn.Background(ColorHover)
		}
		closeBtn.Children(func() {
			DrawIcon(c, "close", 14, ColorTextMuted, 1.8)
		})
		if closeBtn.Clicked() {
			a.dismissFloatingNotice(item.id, updateNotice{}, false)
		}
	})
}

func renderToastNotificationBubble(c *ui.Context, t ui.Toast, bubbleW float32, behind int) {
	isErr := strings.EqualFold(t.Type, "error")
	bg := ColorBgMain
	border := ColorBorderStrong
	iconName := "check-circle"
	iconBg := ColorInfoBlueBg
	iconColor := ColorInfoBlue
	textColor := ColorTextMain
	badgeBg := ColorBgSecondary
	badgeFg := ColorTextMuted
	if isErr {
		bg = ColorErrorBg
		border = ui.RGBA(196, 24, 46, 0.28)
		iconName = "alert-circle"
		iconBg = ui.RGBA(196, 24, 46, 0.12)
		iconColor = ColorError
		textColor = ColorError
		badgeBg = ui.RGBA(196, 24, 46, 0.12)
		badgeFg = ColorError
	}

	toast := ui.ToastBase(c, t)
	toast.Root.
		Row().
		Width(bubbleW).
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
		renderStackedCountBadge(c, behind, badgeBg, badgeFg)
		closeBtn := toast.CloseButton().Label("关闭通知").Tooltip("关闭").Size(26, 26).Radius(999).Shrink(0).Center()
		if closeBtn.Hovered() {
			closeBtn.Background(ColorHover)
		}
		closeBtn.Children(func() {
			DrawIcon(c, "close", 14, ColorTextMuted, 1.8)
		})
	})
}
