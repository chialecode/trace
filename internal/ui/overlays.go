package ui

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"
)

const overlayControlsAutoHideDelay = 2 * time.Second

func (a *App) wakeImageViewerControls(now time.Time) {
	a.ImageViewer.LastActiveAt = now
	a.ImageViewer.ControlsHidden = false
}

func (a *App) wakePlayerControls(now time.Time) {
	a.PlayerOverlay.LastActiveAt = now
	a.PlayerOverlay.ControlsHidden = false
}

func (a *App) renderLogoutConfirmOverlay(c *ui.Context, w, h float32) {
	if c.Shortcut(0, ui.KeyEscape) {
		a.ConfirmLogoutOpen = false
		return
	}
	targetSite := normalizeSiteIDUI(a.ConfirmLogoutSite)
	titleText := "退出登录？"
	bodyText := "退出后会暂停哔哩哔哩的缓存任务并停止录制，抖音不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	if targetSite == "douyin" {
		titleText = "退出抖音登录？"
		bodyText = "退出后会暂停抖音的缓存任务并停止抖音录制，哔哩哔哩不受影响。已缓存的文件会保留，重新登录后可以继续任务。"
	}
	ui.Overlay(c, func() {
		ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Background(ui.RGBA(15, 20, 25, 0.55)).Center().Children(func() {
			ui.Column(c).Label("退出登录确认对话框").Width(min(float32(460), w-40)).Padding(22).Radius(16).Background(ColorBgMain).Gap(12).Children(func() {
				ui.Text(c, titleText).FontSize(18).FontWeight(600).TextColor(ColorTextMain)
				ui.Text(c, bodyText).FontSize(13).TextColor(ColorTextMuted).MaxLines(3)
				ui.Row(c).FillWidth().Justify(ui.End).Gap(8).Children(func() {
					if OutlinePillButton(c, "取消", "取消退出登录").Clicked() {
						a.ConfirmLogoutOpen = false
					}
					if DangerOutlinePillButton(c, "退出登录", "确认退出登录").Clicked() {
						a.ConfirmLogoutOpen = false
						a.logoutSiteAccount(targetSite)
					}
				})
			})
		})
	})
}

func (a *App) renderDeleteConfirmOverlay(c *ui.Context, w, h float32) {
	if c.Shortcut(0, ui.KeyEscape) {
		a.DeleteConfirm.Open = false
		return
	}
	ui.Overlay(c, func() {
		cardW := float32(480)
		if w-40 < cardW {
			cardW = w - 40
		}
		ui.Column(c).
			Absolute().
			Top(0).
			Left(0).
			Size(w, h).
			Background(ui.RGBA(15, 20, 25, 0.55)).
			AlignItems(ui.Center).
			Justify(ui.Center).
			Children(func() {
				ui.Column(c).
					Label("删除确认对话框").
					Width(cardW).
					Padding(22).
					Radius(16).
					Background(ColorBgMain).
					Border(1, ColorBorder).
					Gap(12).
					ClipX().
					Children(func() {
						ui.Text(c, "删除缓存确认").FontSize(18).FontWeight(600).TextColor(ColorTextMain).SingleLine()
						ui.Text(c, fmt.Sprintf("标题：%s (%s)", a.DeleteConfirm.Item.Title, a.DeleteConfirm.Item.ItemKey)).
							FontSize(14).FontWeight(600).TextColor(ColorTextMain).MaxLines(2)
						ui.Text(c, fmt.Sprintf("预计清理已登记缓存文件：%d 个（共 %s）",
							a.DeleteConfirm.FileCount,
							FormatBytes(a.DeleteConfirm.TotalBytes))).
							FontSize(13).TextColor(ColorTextMuted).MaxLines(2)
						if len(a.DeleteConfirm.Plan.PreservedSharedVideos) > 0 {
							ui.Box(c).FillWidth().Padding(8, 12).Radius(8).Background(ColorInfoBlueBg).Children(func() {
								ui.Text(c, fmt.Sprintf("这个视频还被 %d 条动态引用，将保留视频文件", len(a.DeleteConfirm.Plan.PreservedSharedVideos))).
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
								err := a.backend.DeleteLibraryItem(a.DeleteConfirm.Item.ItemKey, a.DeleteConfirm.DeleteFiles)
								if err != nil {
									a.BannerError = err.Error()
								} else {
									if len(a.DeleteConfirm.Plan.PreservedUserFiles) > 0 {
										a.BannerInfo = fmt.Sprintf("已删除缓存记录，并保留了 %d 个不是 TRACE 创建的文件", len(a.DeleteConfirm.Plan.PreservedUserFiles))
									} else {
										a.BannerInfo = "已删除：" + a.DeleteConfirm.Item.Title
									}
								}
								a.DeleteConfirm.Open = false
								a.RefreshData()
							}
						})
					})
			})
	})
}

// ============================================================================
// Overlay: In-App Immersive Image & Mixed-Page Viewer (docs/ui.md §3.5)
// ============================================================================

// mediaPanelRect is the floating frame the image viewer and the player open
// in (DEC-040): centred over the app, which stays visible around it.
func mediaPanelRect(w, h float32) (x, y, pw, ph float32) {
	return mediaPanelRectBelow(w, h, 0)
}

// mediaPanelRectBelow keeps the panel clear of a hidden title bar's window
// buttons (DEC-046 ④): its top margin is at least the bar's height plus 8.
func mediaPanelRectBelow(w, h, titleBarH float32) (x, y, pw, ph float32) {
	margin := max(24, min(w, h)*0.06)
	if titleBarH > 0 {
		margin = max(margin, titleBarH+8)
	}
	pw, ph = max(320, min(w-2*margin, 1280)), max(240, min(h-2*margin, 860))
	return (w - pw) / 2, (h - ph) / 2, pw, ph
}

// mediaBackdrop dims the app behind a floating media panel; clicking it
// closes the panel.
func mediaBackdrop(c *ui.Context, label string, w, h float32, close func()) {
	if ui.ButtonBase(c).Label(label).Absolute().Top(0).Left(0).Size(w, h).Background(ui.RGBA(15, 20, 25, 0.55)).Clicked() {
		close()
	}
}

func (a *App) renderImageViewerOverlay(c *ui.Context, w, h float32) {
	fullW, fullH := w, h
	panelX, panelY, w, h := mediaPanelRectBelow(fullW, fullH, a.titleBar.Height)
	now := c.Now()
	if a.ImageViewer.LastActiveAt.IsZero() {
		a.ImageViewer.LastActiveAt = now
	}
	if len(c.KeysDown()) > 0 {
		a.wakeImageViewerControls(now)
	}

	total := len(a.ImageViewer.URLs)
	if len(a.ImageViewer.LocalPaths) > total {
		total = len(a.ImageViewer.LocalPaths)
	}
	if total == 0 {
		total = 1
	}
	if a.ImageViewer.Index < 0 {
		a.ImageViewer.Index = 0
	}
	if a.ImageViewer.Index >= total {
		a.ImageViewer.Index = total - 1
	}

	switchViewerPage := func(nextIdx int) {
		if nextIdx == a.ImageViewer.Index || nextIdx < 0 || nextIdx >= total {
			return
		}
		a.stopViewerInlinePlayback()
		a.ImageViewer.Index = nextIdx
		if nextIdx < len(a.ImageViewer.InlinePageKinds) && a.ImageViewer.InlinePageKinds[nextIdx] == "video" {
			a.startViewerInlinePage(nextIdx)
		}
	}

	if c.Shortcut(0, ui.KeySpace) && a.ImageViewer.InlineActive {
		a.wakeImageViewerControls(now)
		a.toggleViewerInlinePlay()
	}
	if c.Shortcut(0, ui.KeyLeft) {
		a.wakeImageViewerControls(now)
		if a.ImageViewer.Index > 0 {
			switchViewerPage(a.ImageViewer.Index - 1)
		}
	}
	if c.Shortcut(0, ui.KeyRight) {
		a.wakeImageViewerControls(now)
		if a.ImageViewer.Index+1 < total {
			switchViewerPage(a.ImageViewer.Index + 1)
		}
	}
	if c.Shortcut(0, ui.KeyEqual) {
		a.wakeImageViewerControls(now)
		a.ImageViewer.Zoom = min(4.0, a.ImageViewer.Zoom+0.25)
	}
	if c.Shortcut(0, ui.KeyMinus) {
		a.wakeImageViewerControls(now)
		a.ImageViewer.Zoom = max(0.25, a.ImageViewer.Zoom-0.25)
	}
	if c.Shortcut(0, ui.Key0) {
		a.wakeImageViewerControls(now)
		a.ImageViewer.Fit = true
		a.ImageViewer.Zoom = 1
		a.ImageViewer.PanX = 0
		a.ImageViewer.PanY = 0
	}
	if c.Shortcut(0, ui.KeyEscape) {
		a.closeImageViewer()
		return
	}

	if now.Sub(a.ImageViewer.LastActiveAt) >= overlayControlsAutoHideDelay {
		a.ImageViewer.ControlsHidden = true
	} else {
		a.ImageViewer.ControlsHidden = false
		c.After(overlayControlsAutoHideDelay - now.Sub(a.ImageViewer.LastActiveAt))
	}

	ui.Overlay(c, func() {
		mediaBackdrop(c, "关闭看图（点击空白处）", fullW, fullH, a.closeImageViewer)
		ui.Box(c).
			Label("看图层").
			Absolute().
			Top(panelY).
			Left(panelX).
			Size(w, h).
			Radius(16).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.35)).
			Background(ui.RGBA(18, 20, 24, 1)).
			Clip().
			Children(func() {
				// Full-bleed dark viewport
				ui.Box(c).
					Label("原图视口").
					HandleInput(func(ev ui.InputEvent) bool {
						if ev.Kind == ui.InputPointerMove || ev.Kind == ui.InputPointerDown || ev.Kind == ui.InputScroll || ev.Kind == ui.InputZoom {
							a.wakeImageViewerControls(c.Now())
						}
						return a.imageInput(ev)
					}).
					Absolute().
					Top(0).
					Left(0).
					Size(w, h).
					Clip().
					Children(func() {
						inlineShowing := a.ImageViewer.InlineActive && a.ImageViewer.InlinePageIndex == a.ImageViewer.Index
						if inlineShowing && playerLoaded(a.viewerPlayer) {
							if vBmp := a.viewerPlayer.Bitmap(); vBmp != nil {
								ui.Image(c, vBmp).Size(w, h).Fit(ui.Contain).Absolute().Left(0).Top(0)
							}
						} else {
							bmpIdx := a.ImageViewer.Index
							if bmpIdx < 0 || bmpIdx >= len(a.ImageViewer.Bitmaps) {
								bmpIdx = 0
							}
							if len(a.ImageViewer.Bitmaps) > 0 && a.ImageViewer.Bitmaps[bmpIdx] != nil {
								bmp := a.ImageViewer.Bitmaps[bmpIdx]
								bw, bh := bmp.Size()
								scale := a.ImageViewer.Zoom
								if a.ImageViewer.Fit && bw > 0 && bh > 0 {
									scale *= min((w-64)/float32(bw), (h-64)/float32(bh))
								}
								dispW, dispH := float32(bw)*scale, float32(bh)*scale
								ui.Image(c, bmp).
									Size(dispW, dispH).
									Fit(ui.Contain).
									Absolute().
									Left((w-dispW)/2 + a.ImageViewer.PanX).
									Top((h-dispH)/2 + a.ImageViewer.PanY)
							}
						}
						if inlineShowing && a.ImageViewer.InlineLoading {
							ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c).Label("看图媒体加载中").Size(26, 26)
							})
						}
						errText := ""
						if inlineShowing && a.ImageViewer.InlineError != "" {
							errText = a.ImageViewer.InlineError
						} else if a.ImageViewer.Index < len(a.ImageViewer.Errors) && a.ImageViewer.Errors[a.ImageViewer.Index] != "" {
							errText = a.ImageViewer.Errors[a.ImageViewer.Index]
						}
						if errText != "" {
							ui.Row(c).Absolute().Top(72).Left(24).Right(24).Justify(ui.Center).Children(func() {
								ui.Box(c).Padding(8, 14).Radius(10).Background(ui.RGBA(180, 35, 24, 0.85)).Children(func() {
									ui.Text(c, errText).FontSize(13).FontWeight(600).TextColor(ColorWhite).MaxLines(2)
								})
							})
						}
						// Thin inline progress bar + duration at the bottom of the viewer when playing motion/video page (docs/ui.md §3.5)
						if inlineShowing && playerLoaded(a.viewerPlayer) {
							vSnap := a.viewerPlayer.Snapshot(w, h)
							dur := vSnap.DurationSec
							if dur <= 0 && a.ImageViewer.Index < len(a.ImageViewer.InlineDurations) {
								dur = float64(a.ImageViewer.InlineDurations[a.ImageViewer.Index])
							}
							frac := float32(0)
							if dur > 0 {
								frac = float32(max(0.0, min(1.0, vSnap.PositionSec/dur)))
							}
							ui.Row(c).
								Label("看图内嵌播放进度栏").
								Absolute().
								Bottom(78).
								Left(36).
								Right(36).
								Padding(6, 12).
								Radius(999).
								Background(ui.RGBA(0, 0, 0, 0.56)).
								AlignItems(ui.Center).
								Gap(10).
								Children(func() {
									ui.Box(c).Grow(1).Height(4).Radius(2).Background(ui.RGBA(255, 255, 255, 0.28)).Clip().Draw(func(p *ui.Painter, r ui.Rect) {
										if frac > 0 {
											p.Fill(ui.Rect{X: r.X, Y: r.Y, W: r.W * frac, H: r.H}, ColorWhite, 2)
										}
									})
									ui.Text(c, fmt.Sprintf("%s / %s", FormatDuration(int(vSnap.PositionSec)), FormatDuration(int(dur)))).
										FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
								})
						}
					})

				// Minimal floating circular controls (fade out after 2s of inactivity)
				if !a.ImageViewer.ControlsHidden {
					// Top floating bar: index/zoom badge on left + Close circular button on right
					ui.Row(c).
						Absolute().
						Top(20).
						Left(24).
						Right(24).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Children(func() {
							ui.Box(c).Padding(6, 14).Radius(999).Background(ui.RGBA(255, 255, 255, 0.14)).Border(1, ui.RGBA(255, 255, 255, 0.2)).Children(func() {
								ui.Text(c, fmt.Sprintf("%d / %d · %.0f%%", a.ImageViewer.Index+1, total, a.ImageViewer.Zoom*100)).
									FontSize(13).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
							if FloatingCircleButton(c, "close", "关闭看图", 42).Clicked() {
								a.closeImageViewer()
							}
						})

					// Left & Right floating circular navigation buttons: 上一张 / 下一张
					ui.Row(c).
						Absolute().
						Top(h/2 - 22).
						Left(24).
						Children(func() {
							if FloatingCircleButton(c, "chevron-left", "看图上一张", 44).Disabled(a.ImageViewer.Index <= 0).Clicked() {
								a.wakeImageViewerControls(now)
								if a.ImageViewer.Index > 0 {
									switchViewerPage(a.ImageViewer.Index - 1)
								}
							}
						})
					ui.Row(c).
						Absolute().
						Top(h/2 - 22).
						Right(24).
						Children(func() {
							if FloatingCircleButton(c, "chevron-right", "看图下一张", 44).Disabled(a.ImageViewer.Index+1 >= total).Clicked() {
								a.wakeImageViewerControls(now)
								if a.ImageViewer.Index+1 < total {
									switchViewerPage(a.ImageViewer.Index + 1)
								}
							}
						})

					// Bottom-center floating circular buttons: 适应 / 原始大小 (1:1) / 实况或视频内嵌播放
					ui.Row(c).
						Absolute().
						Bottom(24).
						Left(0).
						Right(0).
						Justify(ui.Center).
						Gap(12).
						Children(func() {
							if FloatingCircleButton(c, "maximize", "看图适应", 42).Clicked() {
								a.wakeImageViewerControls(now)
								a.ImageViewer.Fit = true
								a.ImageViewer.Zoom = 1
								a.ImageViewer.PanX = 0
								a.ImageViewer.PanY = 0
							}
							if FloatingCircleButton(c, "text:1:1", "看图1:1", 42).Clicked() {
								a.wakeImageViewerControls(now)
								a.ImageViewer.Zoom = 1.0
								a.ImageViewer.Fit = false
								a.ImageViewer.PanX = 0
								a.ImageViewer.PanY = 0
							}
							i := a.ImageViewer.Index
							hasInlineMedia := (i >= 0 && i < len(a.ImageViewer.InlineMediaPaths) && a.ImageViewer.InlineMediaPaths[i] != "") ||
								(i >= 0 && i < len(a.ImageViewer.InlineMediaURLs) && a.ImageViewer.InlineMediaURLs[i] != "")
							if hasInlineMedia {
								inlinePlaying := a.ImageViewer.InlineActive && a.ImageViewer.InlinePageIndex == i && playerPlaying(a.viewerPlayer)
								icon := "play"
								if inlinePlaying {
									icon = "pause"
								}
								if FloatingCircleButton(c, icon, "看图播放实况", 42).Clicked() {
									a.wakeImageViewerControls(now)
									if a.ImageViewer.InlineActive && a.ImageViewer.InlinePageIndex == i && playerLoaded(a.viewerPlayer) {
										a.toggleViewerInlinePlay()
									} else {
										a.startViewerInlinePage(i)
									}
									return
								}
							}
							if a.ImageViewer.ItemKey != "" {
								if FloatingCircleButton(c, "folder", "看图打开文件夹", 42).Clicked() {
									a.wakeImageViewerControls(now)
									_ = a.backend.OpenItemFolder(a.ImageViewer.ItemKey)
								}
							}
						})
				}
			})
	})
}

// ============================================================================
// Overlay: In-App Immersive Media Player + Danmaku + Subtitles
// ============================================================================

func (a *App) renderPlayerOverlay(c *ui.Context, w, h float32) {
	// A floating panel over the app (DEC-040); full screen fills the window.
	fullW, fullH := w, h
	panelX, panelY, radius := float32(0), float32(0), float32(0)
	if !a.PlayerOverlay.Fullscreen {
		panelX, panelY, w, h = mediaPanelRectBelow(fullW, fullH, a.titleBar.Height)
		radius = 16
	}
	now := c.Now()
	if a.PlayerOverlay.LastActiveAt.IsZero() {
		a.PlayerOverlay.LastActiveAt = now
	}
	if len(c.KeysDown()) > 0 {
		a.wakePlayerControls(now)
	}

	snap := a.player.Snapshot(w, h)
	if snap.DurationSec > 0 && !a.player.Scrubbing() {
		a.PlayerOverlay.SeekNormalized = snap.PositionSec / snap.DurationSec
	}

	if c.Shortcut(0, ui.KeySpace) {
		a.wakePlayerControls(now)
		a.player.TogglePlay()
	}
	if c.Shortcut(0, ui.KeyLeft) {
		a.wakePlayerControls(now)
		_ = a.player.Seek(max(0, snap.PositionSec-5))
	}
	if c.Shortcut(0, ui.KeyRight) {
		a.wakePlayerControls(now)
		_ = a.player.Seek(min(max(1.0, snap.DurationSec), snap.PositionSec+5))
	}
	if c.Shortcut(0, ui.KeyEscape) {
		if a.PlayerOverlay.MoreMenuOpen || a.PlayerOverlay.VolumeMenuOpen {
			a.PlayerOverlay.MoreMenuOpen = false
			a.PlayerOverlay.VolumeMenuOpen = false
			a.wakePlayerControls(now)
		} else {
			a.closePlayerOverlay()
			return
		}
	}

	if !a.PlayerOverlay.MoreMenuOpen && !a.PlayerOverlay.VolumeMenuOpen && now.Sub(a.PlayerOverlay.LastActiveAt) >= overlayControlsAutoHideDelay {
		a.PlayerOverlay.ControlsHidden = true
	} else {
		a.PlayerOverlay.ControlsHidden = false
		if !a.PlayerOverlay.MoreMenuOpen && !a.PlayerOverlay.VolumeMenuOpen {
			c.After(overlayControlsAutoHideDelay - now.Sub(a.PlayerOverlay.LastActiveAt))
		}
	}

	ui.Overlay(c, func() {
		if !a.PlayerOverlay.Fullscreen {
			mediaBackdrop(c, "关闭播放（点击空白处）", fullW, fullH, a.closePlayerOverlay)
		}
		ui.Box(c).
			Label("播放层").
			HandleInput(func(ev ui.InputEvent) bool {
				if ev.Kind == ui.InputPointerMove || ev.Kind == ui.InputPointerDown {
					a.wakePlayerControls(c.Now())
				}
				return false
			}).
			Absolute().
			Top(panelY).
			Left(panelX).
			Size(w, h).
			Radius(radius).
			Shadow(0, 12, 40, 0, ui.RGBA(0, 0, 0, 0.35)).
			Background(ColorBlack).
			Clip().
			Children(func() {
				// Full-window dark video surface or decoder fallback
				if a.PlayerOverlay.DecoderMissing {
					ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Padding(32).Children(func() {
						ui.Column(c).Width(min(float32(520), w-48)).Padding(24).Radius(16).Background(ui.RGBA(30, 34, 42, 0.94)).Border(1, ColorDotYellow).AlignItems(ui.Center).Gap(12).Children(func() {
							reason := a.PlayerOverlay.MissingReason
							if reason == "" {
								reason = "当前系统缺少 AV1 / HEVC 硬件或系统解码扩展，无法在内置播放器解码该视频流。"
							}
							ui.Text(c, "解码器缺失提示："+reason).FontSize(14).FontWeight(600).TextColor(ColorWhite).MaxLines(3)
							if AccentPillButton(c, "立即用系统播放器打开", "缺失解码器时用系统播放器打开").Clicked() {
								_ = a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath)
							}
						})
					})
				} else if a.PlayerOverlay.LoadError != "" {
					ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Padding(32).Children(func() {
						ui.Text(c, a.PlayerOverlay.LoadError).FontSize(14).FontWeight(600).MaxLines(3).TextColor(ColorError)
					})
				} else {
					ui.Box(c).Absolute().Top(0).Left(0).Size(w, h).Background(ColorBlack).Clip().Children(func() {
						if a.PlayerOverlay.Loading {
							ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
								ui.Spinner(c).Label("播放器加载中").Size(28, 28)
							})
						} else if bmp := a.player.Bitmap(); bmp != nil {
							ui.Image(c, bmp).Size(w, h).Fit(ui.Contain)
						}
						dmAlpha := float32(max(0.2, min(1.0, a.PlayerOverlay.DanmakuOpacity)))
						for _, dm := range snap.VisibleDanmaku {
							ui.Text(c, dm.Text).
								FontSize(dm.FontSize).
								Bold().
								TextColor(ui.RGBA(255, 255, 255, dmAlpha)).
								SingleLine().
								Absolute().
								Left(dm.X).
								Top(dm.Y)
						}
						if snap.ActiveSubtitle != "" {
							ui.Row(c).FillWidth().Justify(ui.Center).Absolute().Bottom(88).Left(0).Children(func() {
								ui.Text(c, snap.ActiveSubtitle).
									FontSize(15).
									Bold().
									TextColor(ColorWhite).
									Background(ui.RGBA(0, 0, 0, 0.72)).
									Padding(5, 12).
									Radius(6).
									SingleLine()
							})
						}
					})
				}

				// Floating controls (fade out after 2s of mouse/key inactivity)
				if !a.PlayerOverlay.ControlsHidden {
					// Top floating bar: Title on left + "⋯" menu & Close circular floating buttons on right
					ui.Row(c).
						Absolute().
						Top(20).
						Left(24).
						Right(24).
						AlignItems(ui.Center).
						Justify(ui.SpaceBetween).
						Gap(12).
						Children(func() {
							ui.Box(c).Padding(6, 14).Radius(999).Background(ui.RGBA(0, 0, 0, 0.48)).Children(func() {
								ui.Text(c, a.PlayerOverlay.Title).FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
							ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
								if FloatingCircleButton(c, "more", "播放器更多菜单", 40).Clicked() {
									a.wakePlayerControls(now)
									a.PlayerOverlay.MoreMenuOpen = !a.PlayerOverlay.MoreMenuOpen
									a.PlayerOverlay.VolumeMenuOpen = false
								}
								if FloatingCircleButton(c, "close", "关闭播放", 40).Clicked() {
									a.closePlayerOverlay()
								}
							})
						})

					// Popover "⋯" Menu: Danmaku, Subtitles, Segments, Reveal in folder, Open in external player
					if a.PlayerOverlay.MoreMenuOpen {
						ui.Column(c).
							Label("播放器更多面板").
							Absolute().
							Top(70).
							Right(24).
							Width(340).
							Padding(16).
							Radius(16).
							Background(ui.RGBA(20, 24, 30, 0.94)).
							Border(1, ui.RGBA(255, 255, 255, 0.18)).
							Gap(10).
							ClipX().
							Children(func() {
								danmakuMissing := snap.DanmakuCount == 0
								subtitlesMissing := len(a.PlayerOverlay.SubtitleOptions) <= 1
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
									ui.Text(c, "弹幕").FontSize(13).FontWeight(600).TextColor(ColorWhite).SingleLine()
									if ui.Switch(c, &a.PlayerOverlay.DanmakuEnabled).Label("弹幕开关").Disabled(danmakuMissing).Changed() {
										a.wakePlayerControls(now)
										a.applyAndPersistPlayerPrefs()
									}
								})
								if danmakuMissing {
									ui.Text(c, "此文件没有可用的弹幕").Label("弹幕不可用说明").FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.65))
								}
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "弹幕透明度").FontSize(12).TextColor(ColorWhite).SingleLine()
									if ui.Slider(c, &a.PlayerOverlay.DanmakuOpacity, 0.2, 1.0).Label("弹幕透明度").Width(140).Disabled(danmakuMissing).Changed() {
										a.wakePlayerControls(now)
										a.applyAndPersistPlayerPrefs()
									}
								})
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "显示区域").FontSize(12).TextColor(ColorWhite).SingleLine()
									areaLabel := playerAreaLabel(a.PlayerOverlay.DanmakuArea)
									if ui.Select(c, &areaLabel, playerAreaLabels).Label("弹幕显示区域").Disabled(danmakuMissing).Changed() {
										a.PlayerOverlay.DanmakuArea = playerAreaValue(areaLabel)
										a.wakePlayerControls(now)
										a.applyAndPersistPlayerPrefs()
									}
								})
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "弹幕密度").FontSize(12).TextColor(ColorWhite).SingleLine()
									if ui.Select(c, &a.PlayerOverlay.DanmakuDensity, []string{"低", "中", "高", "不限"}).Label("弹幕密度选择").Disabled(danmakuMissing).Changed() {
										a.wakePlayerControls(now)
										a.applyAndPersistPlayerPrefs()
									}
								})
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
									ui.Text(c, "字幕").FontSize(12).TextColor(ColorWhite).SingleLine()
									if ui.Select(c, &a.PlayerOverlay.SubtitleLang, a.PlayerOverlay.SubtitleOptions).Label("字幕语言选择").Disabled(subtitlesMissing).Changed() {
										a.wakePlayerControls(now)
										a.applyAndPersistPlayerPrefs()
									}
								})
								if subtitlesMissing {
									ui.Text(c, "此文件没有可用的字幕").Label("字幕不可用说明").FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.65))
								}

								if len(a.PlayerOverlay.Segments) > 1 {
									ui.Row(c).FillWidth().Padding(4, 0).AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(6).Children(func() {
										ui.Text(c, fmt.Sprintf("第 %d / %d 段", a.PlayerOverlay.Segment+1, len(a.PlayerOverlay.Segments))).
											FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()
										ui.Row(c).Gap(6).Children(func() {
											for _, step := range []int{-1, 1} {
												label := "上一段"
												if step == 1 {
													label = "下一段"
												}
												next := a.PlayerOverlay.Segment + step
												if FloatingPillButton(c, "", label, label).Disabled(next < 0 || next >= len(a.PlayerOverlay.Segments)).Clicked() {
													a.wakePlayerControls(now)
													if item, ok, _ := a.backend.GetLibraryItem(a.PlayerOverlay.ItemKey); ok {
														a.openPlayerSegment(item, next)
													}
												}
											}
										})
									})
								}

								ui.Row(c).FillWidth().Padding(4, 0, 0, 0).Gap(8).Wrap().Children(func() {
									if a.PlayerOverlay.ItemKey != "" {
										if FloatingPillButton(c, "folder", "在文件夹中显示", "播放层打开文件夹").Clicked() {
											a.wakePlayerControls(now)
											_ = a.backend.OpenItemFolder(a.PlayerOverlay.ItemKey)
										}
									}
									if FloatingPillButton(c, "external-link", "用系统播放器打开", "用系统播放器打开").Clicked() {
										a.wakePlayerControls(now)
										_ = a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath)
									}
								})
							})
					}
					if a.PlayerOverlay.VolumeMenuOpen {
						ui.Column(c).Label("播放器音量面板").Absolute().Bottom(86).Right(72).Width(240).Padding(16).Radius(16).
							Background(ui.RGBA(20, 24, 30, 0.94)).Border(1, ui.RGBA(255, 255, 255, 0.18)).Gap(12).Children(func() {
							ui.Text(c, fmt.Sprintf("音量 %.0f%%", a.PlayerOverlay.Volume*100)).FontSize(13).FontWeight(600).TextColor(ColorWhite).SingleLine()
							if ui.Slider(c, &a.PlayerOverlay.Volume, 0, 1).Label("播放器音量调节").FillWidth().Changed() {
								a.wakePlayerControls(now)
								a.player.SetVolume(a.PlayerOverlay.Volume)
								a.player.SetMuted(false)
								a.persistPlayerVolume()
							}
							muted := snap.Muted
							ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
								ui.Text(c, "静音").FontSize(12).TextColor(ColorWhite).SingleLine()
								if ui.Switch(c, &muted).Label("播放器静音").Changed() {
									a.wakePlayerControls(now)
									a.player.SetMuted(muted)
									a.persistPlayerVolume()
								}
							})
						})
					}

					// Bottom floating transport bar: Play/Pause, Seek bar, Volume, Fullscreen
					ui.Row(c).
						Absolute().
						Bottom(20).
						Left(24).
						Right(24).
						Padding(10, 16).
						Radius(999).
						Background(ui.RGBA(20, 24, 30, 0.76)).
						Border(1, ui.RGBA(255, 255, 255, 0.16)).
						AlignItems(ui.Center).
						Gap(12).
						Children(func() {
							playIcon := "play"
							if snap.Playing {
								playIcon = "pause"
							}
							if FloatingCircleButton(c, playIcon, "播放器播放暂停", 38).Disabled(a.PlayerOverlay.LoadError != "").Clicked() {
								a.wakePlayerControls(now)
								a.player.TogglePlay()
							}

							ui.Text(c, fmt.Sprintf("%s / %s", FormatDuration(int(snap.PositionSec)), FormatDuration(int(snap.DurationSec)))).
								FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()

							// Dragging shows key-frame previews with the audio held;
							// the position is sought once, on release. Arrow keys on the bar seek at once.
							seekBar := ui.Slider(c, &a.PlayerOverlay.SeekNormalized, 0, 1).Label("播放进度条").Grow(1)
							durSec := snap.DurationSec
							if durSec <= 0 {
								durSec = 10.0
							}
							target := durSec * a.PlayerOverlay.SeekNormalized
							_, held := seekBar.PressedButton() // held even off the bar
							switch {
							case seekBar.Changed() && held:
								a.wakePlayerControls(now)
								a.player.Preview(target)
							case seekBar.Changed() || a.player.Scrubbing() && !held:
								a.wakePlayerControls(now)
								_ = a.player.Seek(target)
							}

							volIcon := "volume"
							if snap.Muted || snap.Volume <= 0.01 {
								volIcon = "volume-mute"
							}
							if FloatingCircleButton(c, volIcon, "播放器音量", 38).Clicked() {
								a.wakePlayerControls(now)
								a.PlayerOverlay.VolumeMenuOpen = !a.PlayerOverlay.VolumeMenuOpen
								a.PlayerOverlay.MoreMenuOpen = false
							}

							if FloatingCircleButton(c, "fullscreen", "播放器全屏", 38).Clicked() {
								a.wakePlayerControls(now)
								a.setPlayerFullscreen(!a.PlayerOverlay.Fullscreen)
							}
						})
				}
			})
	})
}

func (a *App) applyAndPersistPlayerPrefs() {
	areaFrac := 0.5
	switch a.PlayerOverlay.DanmakuArea {
	case "quarter":
		areaFrac = 0.25
	case "full":
		areaFrac = 1.0
	}
	a.player.SetDanmakuVisible(a.PlayerOverlay.DanmakuEnabled)
	a.player.SetDanmakuOpacity(a.PlayerOverlay.DanmakuOpacity)
	a.player.SetDanmakuArea(areaFrac)
	density := playerDensityValue(a.PlayerOverlay.DanmakuDensity)
	a.player.SetDanmakuDensity(density)

	lang := a.PlayerOverlay.SubtitleLang
	if lang == "关闭" {
		lang = ""
	}
	a.player.SetSubtitleLang(lang)

	uiSt := a.backend.GetUIState()
	uiSt.Player.DanmakuVisible = a.PlayerOverlay.DanmakuEnabled
	uiSt.Player.DanmakuOpacity = a.PlayerOverlay.DanmakuOpacity
	uiSt.Player.DanmakuArea = areaFrac
	uiSt.Player.DanmakuDensity = density
	uiSt.Player.SubtitleLang = lang
	if _, err := a.backend.UpdateUIState(uiSt); err != nil {
		a.showError(err)
	}
}

func (a *App) imageInput(ev ui.InputEvent) bool {
	switch ev.Kind {
	case ui.InputScroll:
		if ev.DY < 0 {
			a.ImageViewer.Zoom *= 1.12
		} else {
			a.ImageViewer.Zoom /= 1.12
		}
		a.ImageViewer.Zoom = max(0.15, min(8, a.ImageViewer.Zoom))
		return true
	case ui.InputZoom:
		a.ImageViewer.Zoom = max(0.15, min(8, a.ImageViewer.Zoom*ev.Scale))
		return true
	case ui.InputPointerDown:
		if ev.Button != 0 {
			return false
		}
		if a.ImageViewer.InlineActive && a.ImageViewer.InlinePageIndex == a.ImageViewer.Index && ev.Clicks == 1 {
			a.toggleViewerInlinePlay()
			return true
		}
		if ev.Clicks == 2 {
			a.ImageViewer.Fit = !a.ImageViewer.Fit
			a.ImageViewer.Zoom = 1
			a.ImageViewer.PanX = 0
			a.ImageViewer.PanY = 0
		}
		a.ImageViewer.Dragging = true
		a.ImageViewer.PointerX = ev.X
		a.ImageViewer.PointerY = ev.Y
		return true
	case ui.InputPointerMove:
		if !a.ImageViewer.Dragging {
			return false
		}
		a.ImageViewer.PanX += ev.X - a.ImageViewer.PointerX
		a.ImageViewer.PanY += ev.Y - a.ImageViewer.PointerY
		a.ImageViewer.PointerX = ev.X
		a.ImageViewer.PointerY = ev.Y
		return true
	case ui.InputPointerUp:
		a.ImageViewer.Dragging = false
		return true
	}
	return false
}
