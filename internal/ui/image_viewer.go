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

// mediaPanelRectBelow computes the centered floating frame for the image viewer
// and media player, keeping clear of a hidden title bar's window buttons.
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
func mediaBackdrop(c *ui.Context, label string, w, h float32, closeFn func()) {
	if ui.ButtonBase(c).Label(label).Absolute().Top(0).Left(0).Size(w, h).Background(ui.RGBA(15, 20, 25, 0.55)).Clicked() {
		closeFn()
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
		a.ImageViewer.Zoom = 1
		a.ImageViewer.PanX = 0
		a.ImageViewer.PanY = 0
		a.ImageViewer.Fit = true
		if nextIdx < len(a.ImageViewer.InlinePageKinds) && viewerPageAutoplays(a.ImageViewer.InlinePageKinds[nextIdx]) {
			a.startViewerInlinePage(nextIdx)
		}
	}

	ui.Overlay(c, func() {
		modal := ui.Box(c).Absolute().Top(0).Left(0).Size(fullW, fullH).Modal()
		if modal.OverlayShortcut(0, ui.KeySpace) && a.ImageViewer.InlineActive {
			a.wakeImageViewerControls(now)
			a.toggleViewerInlinePlay()
		}
		if modal.OverlayShortcut(0, ui.KeyLeft) {
			a.wakeImageViewerControls(now)
			if a.ImageViewer.Index > 0 {
				switchViewerPage(a.ImageViewer.Index - 1)
			}
		}
		if modal.OverlayShortcut(0, ui.KeyRight) {
			a.wakeImageViewerControls(now)
			if a.ImageViewer.Index+1 < total {
				switchViewerPage(a.ImageViewer.Index + 1)
			}
		}
		if modal.OverlayShortcut(0, ui.KeyEqual) {
			a.wakeImageViewerControls(now)
			a.ImageViewer.Zoom = min(4.0, a.ImageViewer.Zoom+0.25)
		}
		if modal.OverlayShortcut(0, ui.KeyMinus) {
			a.wakeImageViewerControls(now)
			a.ImageViewer.Zoom = max(0.25, a.ImageViewer.Zoom-0.25)
		}
		if modal.OverlayShortcut(0, ui.Key0) {
			a.wakeImageViewerControls(now)
			a.ImageViewer.Fit = true
			a.ImageViewer.Zoom = 1
			a.ImageViewer.PanX = 0
			a.ImageViewer.PanY = 0
		}
		if modal.OverlayShortcut(0, ui.KeyEscape) {
			a.closeImageViewer()
			return
		}

		if now.Sub(a.ImageViewer.LastActiveAt) >= overlayControlsAutoHideDelay {
			a.ImageViewer.ControlsHidden = true
		} else {
			a.ImageViewer.ControlsHidden = false
			c.After(overlayControlsAutoHideDelay - now.Sub(a.ImageViewer.LastActiveAt))
		}

		modal.Children(func() {
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
								if bmpIdx >= 0 && bmpIdx < len(a.ImageViewer.Bitmaps) && a.ImageViewer.Bitmaps[bmpIdx] != nil {
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
										if err := a.backend.OpenItemFolder(a.ImageViewer.ItemKey); err != nil {
											a.showError(err)
										}
									}
								}
							})
					}
				})
		})
	})
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
