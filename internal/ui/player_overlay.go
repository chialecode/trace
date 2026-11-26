package ui

import (
	"context"
	"fmt"
	"time"

	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func (a *App) wakePlayerControls(now time.Time) {
	a.PlayerOverlay.LastActiveAt = now
	a.PlayerOverlay.ControlsHidden = false
}

func (a *App) previewCacheInFlight() bool {
	return a.PlayerOverlay.PreviewPendingKey != "" && a.isPending(a.PlayerOverlay.PreviewPendingKey)
}

func (a *App) previewCacheButtonState() (text string, disabled bool) {
	if a.previewCacheInFlight() {
		return "正在加入缓存…", true
	}
	if a.PlayerOverlay.PreviewCacheQueued {
		return "已加入缓存", true
	}
	return "缓存到媒体库", false
}

func (a *App) triggerPreviewCache() {
	if !a.PlayerOverlay.PreviewMode || a.PlayerOverlay.PreviewOnCache == nil || a.previewCacheInFlight() || a.PlayerOverlay.PreviewCacheQueued {
		return
	}
	seq := a.playerOpenSeq
	reqKey := a.PlayerOverlay.PreviewPendingKey
	if reqKey == "" {
		reqKey = fmt.Sprintf("preview-cache-%d", seq)
		a.PlayerOverlay.PreviewPendingKey = reqKey
	}
	a.PlayerOverlay.PreviewCacheError = ""
	enqueue := a.PlayerOverlay.PreviewOnCache
	requestGuarded(a, reqKey, nil, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, enqueue(ctx)
	}, func(struct{}) {
		a.BannerInfo = "已加入缓存任务"
		if a.PlayerOverlay.Open && a.PlayerOverlay.PreviewMode && a.playerOpenSeq == seq {
			a.PlayerOverlay.PreviewCacheQueued = true
			a.PlayerOverlay.PreviewCacheError = ""
			a.PlayerOverlay.PreviewCacheToast = "已加入缓存任务"
		}
		a.RefreshData()
	}, func(err error) {
		a.showError(err)
		if a.PlayerOverlay.Open && a.PlayerOverlay.PreviewMode && a.playerOpenSeq == seq {
			a.PlayerOverlay.PreviewCacheQueued = false
			a.PlayerOverlay.PreviewCacheError = "加入缓存失败：" + userErrorText(err)
			a.PlayerOverlay.PreviewCacheToast = a.PlayerOverlay.PreviewCacheError
			a.PlayerOverlay.ControlsHidden = false
			a.PlayerOverlay.LastActiveAt = time.Time{}
		}
		a.RefreshData()
	})
}

func (a *App) renderPlayerOverlay(c *ui.Context, w, h float32) {
	// A floating panel over the app; full screen fills the window.
	fullW, fullH := w, h
	panelX, panelY, radius := float32(0), float32(0), float32(0)
	if !a.PlayerOverlay.Fullscreen {
		panelX, panelY, w, h = mediaPanelRectBelow(fullW, fullH, a.titleBar.Height)
		radius = 16
	}
	now := c.Now()
	if a.PlayerOverlay.PreviewCacheToast != "" {
		c.Toast(a.PlayerOverlay.PreviewCacheToast)
		a.PlayerOverlay.PreviewCacheToast = ""
	}
	if a.PlayerOverlay.LastActiveAt.IsZero() {
		a.PlayerOverlay.LastActiveAt = now
	}
	if len(c.KeysDown()) > 0 {
		a.wakePlayerControls(now)
	}

	var snap player.StateSnapshot
	if a.player != nil {
		snap = a.player.Snapshot(w, h)
		if snap.DurationSec > 0 && !a.player.Scrubbing() {
			a.PlayerOverlay.SeekNormalized = snap.PositionSec / snap.DurationSec
		}
	}

	ready := a.player != nil && !a.PlayerOverlay.Loading && a.PlayerOverlay.LoadError == "" && !a.PlayerOverlay.DecoderMissing
	ui.Overlay(c, func() {
		modal := ui.Box(c).Absolute().Top(0).Left(0).Size(fullW, fullH).Modal()
		if ready && modal.OverlayShortcut(0, ui.KeySpace) {
			a.wakePlayerControls(now)
			a.PlayerOverlay.LoopPaused = snap.Playing
			a.player.TogglePlay()
		}
		if ready && snap.DurationSec > 0 && modal.OverlayShortcut(0, ui.KeyLeft) {
			a.wakePlayerControls(now)
			_ = a.player.Seek(max(0, snap.PositionSec-5))
		}
		if ready && snap.DurationSec > 0 && modal.OverlayShortcut(0, ui.KeyRight) {
			a.wakePlayerControls(now)
			_ = a.player.Seek(min(snap.DurationSec, snap.PositionSec+5))
		}
		if modal.OverlayShortcut(0, ui.KeyEscape) {
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

		modal.Children(func() {
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
								if !a.PlayerOverlay.PreviewMode && a.PlayerOverlay.MediaPath != "" {
									if AccentPillButton(c, "立即用系统播放器打开", "缺失解码器时用系统播放器打开").Clicked() {
										if err := a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath); err != nil {
											a.showError(err)
										}
									}
								} else if a.PlayerOverlay.PreviewMode && a.PlayerOverlay.PreviewOnCache != nil {
									btnText, disabled := a.previewCacheButtonState()
									if AccentPillButton(c, btnText, "缺失解码器时缓存到媒体库").Disabled(disabled).Clicked() && !disabled {
										a.triggerPreviewCache()
									}
									if a.PlayerOverlay.PreviewCacheError != "" {
										ui.Text(c, a.PlayerOverlay.PreviewCacheError).Label("临时预览缓存失败提示").FontSize(12).LineHeight(LineHeightCompact).MaxLines(2).TextColor(ui.RGBA(252, 165, 165, 1))
									}
								}
							})
						})
					} else if a.PlayerOverlay.LoadError != "" {
						ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Padding(32).Children(func() {
							if a.PlayerOverlay.PreviewMode {
								ui.Column(c).Width(min(float32(520), w-48)).Padding(24).Radius(16).Background(ui.RGBA(30, 34, 42, 0.94)).Border(1, ui.RGBA(255, 255, 255, 0.16)).AlignItems(ui.Center).Gap(14).Children(func() {
									ui.Text(c, a.PlayerOverlay.LoadError).Label("临时预览错误说明").FontSize(14).LineHeight(LineHeightCompact).FontWeight(600).MaxLines(4).TextColor(ColorWhite)
									if a.PlayerOverlay.PreviewOnCache != nil {
										btnText, disabled := a.previewCacheButtonState()
										if AccentPillButton(c, btnText, "预览失败缓存到媒体库").Disabled(disabled).Clicked() && !disabled {
											a.triggerPreviewCache()
										}
									}
									if a.PlayerOverlay.PreviewCacheError != "" {
										ui.Text(c, a.PlayerOverlay.PreviewCacheError).Label("临时预览缓存失败提示").FontSize(12).LineHeight(LineHeightCompact).MaxLines(2).TextColor(ui.RGBA(252, 165, 165, 1))
									}
								})
							} else {
								ui.Text(c, a.PlayerOverlay.LoadError).FontSize(14).FontWeight(600).MaxLines(3).TextColor(ColorError)
							}
						})
					} else {
						ui.Box(c).Absolute().Top(0).Left(0).Size(w, h).Background(ColorBlack).Clip().Children(func() {
							if a.PlayerOverlay.Loading {
								ui.Column(c).Absolute().Top(0).Left(0).Size(w, h).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
									ui.Spinner(c).Label("播放器加载中").Size(28, 28)
								})
							} else if a.player != nil {
								if bmp := a.player.Bitmap(); bmp != nil {
									ui.Image(c, bmp).Size(w, h).Fit(ui.Contain)
								}
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
								subBottom := float32(88)
								if !a.PlayerOverlay.ControlsHidden && len(a.PlayerOverlay.Chapters) > 0 {
									subBottom = 122
								}
								ui.Row(c).FillWidth().Justify(ui.Center).Absolute().Bottom(subBottom).Left(0).Children(func() {
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
						// Top floating bar: Title (+ "临时预览" badge in PreviewMode) on left,
						// optional "缓存到媒体库" CTA + "⋯" menu & Close circular floating buttons on right.
						ui.Row(c).
							Absolute().
							Top(20).
							Left(24).
							Right(24).
							AlignItems(ui.Center).
							Justify(ui.SpaceBetween).
							Gap(12).
							Children(func() {
								ui.Row(c).Shrink(1).AlignItems(ui.Center).Gap(8).ClipX().Children(func() {
									ui.Box(c).Shrink(1).ClipX().Padding(6, 14).Radius(999).Background(ui.RGBA(0, 0, 0, 0.48)).Children(func() {
										ui.Text(c, a.PlayerOverlay.Title).FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
									})
									if a.PlayerOverlay.PreviewMode {
										ui.Box(c).
											Label("临时预览标记").
											Shrink(0).
											Padding(5, 10).
											Radius(999).
											Background(ui.RGBA(14, 34, 68, 0.76)).
											Border(1, ui.RGBA(138, 180, 248, 0.65)).
											Children(func() {
												ui.Text(c, "临时预览").FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()
											})
									}
								})
								ui.Row(c).Shrink(0).Gap(10).AlignItems(ui.Center).Children(func() {
									if a.PlayerOverlay.PreviewMode && a.PlayerOverlay.PreviewOnCache != nil {
										btnText, disabled := a.previewCacheButtonState()
										bg := ColorAccent
										border := ui.RGBA(255, 255, 255, 0.26)
										if disabled {
											bg = ui.RGBA(255, 255, 255, 0.16)
										}
										cacheBtn := ui.Button(c, "").
											Label("临时预览缓存到媒体库").
											Shrink(0).
											Radius(999).
											Padding(7, 14).
											Background(bg).
											Border(1, border).
											Justify(ui.Center).
											AlignItems(ui.Center).
											Disabled(disabled)
										if !disabled && cacheBtn.Hovered() {
											cacheBtn.Border(1, ColorWhite)
										}
										cacheBtn.Children(func() {
											ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
												DrawIcon(c, "download", 14, ColorWhite, 1.9)
												ui.Text(c, btnText).FontSize(12).FontWeight(600).TextColor(ColorWhite).SingleLine()
											})
										})
										if cacheBtn.Clicked() && !disabled {
											a.wakePlayerControls(now)
											a.triggerPreviewCache()
										}
									}
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

						if a.PlayerOverlay.PreviewMode && a.PlayerOverlay.PreviewCacheError != "" && a.PlayerOverlay.LoadError == "" && !a.PlayerOverlay.DecoderMissing {
							ui.Row(c).Absolute().Top(70).Left(24).Right(24).Justify(ui.Center).Children(func() {
								ui.Box(c).
									Label("临时预览缓存失败提示").
									MaxWidth(min(float32(520), w-48)).
									Padding(8, 14).
									Radius(10).
									Background(ui.RGBA(127, 29, 29, 0.92)).
									Border(1, ui.RGBA(252, 165, 165, 0.55)).
									Children(func() {
										ui.Text(c, a.PlayerOverlay.PreviewCacheError).FontSize(12).LineHeight(LineHeightCompact).FontWeight(600).TextColor(ColorWhite).MaxLines(2)
									})
							})
						}

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
										opSlider := ui.Slider(c, &a.PlayerOverlay.DanmakuOpacity, 0.2, 1.0).Label("弹幕透明度").Width(140).Disabled(danmakuMissing)
										_, opHeld := opSlider.PressedButton()
										switch {
										case opSlider.Changed() && opHeld:
											a.PlayerOverlay.opacityScrubbing = true
											a.wakePlayerControls(now)
											a.applyPlayerPrefs()
										case opSlider.Changed() || (a.PlayerOverlay.opacityScrubbing && !opHeld):
											a.PlayerOverlay.opacityScrubbing = false
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
														item, ok, err := a.backend.GetLibraryItem(a.PlayerOverlay.ItemKey)
														if err != nil {
															a.showError(err)
														} else if ok {
															a.openPlayerSegment(item, next)
														}
													}
												}
											})
										})
									}

									canOpenLibraryFile := !a.PlayerOverlay.PreviewMode && a.PlayerOverlay.MediaPath != ""
									canRevealLibrary := !a.PlayerOverlay.PreviewMode && a.PlayerOverlay.ItemKey != ""
									if canRevealLibrary || canOpenLibraryFile {
										ui.Row(c).FillWidth().Padding(4, 0, 0, 0).Gap(8).Wrap().Children(func() {
											if canRevealLibrary {
												if FloatingPillButton(c, "folder", "在文件夹中显示", "播放层打开文件夹").Clicked() {
													a.wakePlayerControls(now)
													if err := a.backend.OpenItemFolder(a.PlayerOverlay.ItemKey); err != nil {
														a.showError(err)
													}
												}
											}
											if canOpenLibraryFile {
												if FloatingPillButton(c, "external-link", "用系统播放器打开", "用系统播放器打开").Clicked() {
													a.wakePlayerControls(now)
													if err := a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath); err != nil {
														a.showError(err)
													}
												}
											}
										})
									}
								})
						}
						if a.PlayerOverlay.VolumeMenuOpen {
							ui.Column(c).Label("播放器音量面板").Absolute().Bottom(86).Right(72).Width(240).Padding(16).Radius(16).
								Background(ui.RGBA(20, 24, 30, 0.94)).Border(1, ui.RGBA(255, 255, 255, 0.18)).Gap(12).Children(func() {
								ui.Text(c, fmt.Sprintf("音量 %.0f%%", a.PlayerOverlay.Volume*100)).FontSize(13).FontWeight(600).TextColor(ColorWhite).SingleLine()
								volSlider := ui.Slider(c, &a.PlayerOverlay.Volume, 0, 1).Label("播放器音量调节").FillWidth()
								_, volHeld := volSlider.PressedButton()
								switch {
								case volSlider.Changed() && volHeld:
									a.PlayerOverlay.volumeScrubbing = true
									a.wakePlayerControls(now)
									a.applyPlayerVolume(a.PlayerOverlay.Volume, false)
								case volSlider.Changed() || (a.PlayerOverlay.volumeScrubbing && !volHeld):
									a.PlayerOverlay.volumeScrubbing = false
									a.wakePlayerControls(now)
									a.applyPlayerVolume(a.PlayerOverlay.Volume, false)
									a.persistPlayerVolume()
								}
								muted := snap.Muted
								ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Children(func() {
									ui.Text(c, "静音").FontSize(12).TextColor(ColorWhite).SingleLine()
									if ui.Switch(c, &muted).Label("播放器静音").Changed() {
										a.wakePlayerControls(now)
										a.applyPlayerVolume(a.PlayerOverlay.Volume, muted)
										a.persistPlayerVolume()
									}
								})
							})
						}

						if len(a.PlayerOverlay.Chapters) > 0 {
							ui.Row(c).
								Label("播放器章节栏").
								Absolute().
								Bottom(78).
								Left(24).
								Right(24).
								AlignItems(ui.Center).
								Gap(6).
								ClipX().
								Children(func() {
									for i, ch := range a.PlayerOverlay.Chapters {
										chapter := ch
										active := snap.PositionSec >= float64(chapter.FromSec) && snap.PositionSec < float64(chapter.ToSec)
										bg := ui.RGBA(20, 24, 30, 0.76)
										border := ui.RGBA(255, 255, 255, 0.18)
										if active {
											bg = ColorAccent
											border = ColorAccent
										}
										btn := ui.ButtonBase(c).
											Label(fmt.Sprintf("章节跳转：%d", i+1)).
											Shrink(0).
											Padding(5, 10).
											Radius(999).
											Background(bg).
											Border(1, border).
											Disabled(!ready)
										btn.Children(func() {
											ui.Text(c, fmt.Sprintf("%s %s", FormatDuration(chapter.FromSec), chapter.Title)).
												FontSize(12).
												FontWeight(600).
												TextColor(ColorWhite).
												SingleLine()
										})
										if btn.Clicked() && ready {
											a.wakePlayerControls(now)
											target := float64(chapter.FromSec)
											if snap.DurationSec > 0 && target > snap.DurationSec {
												target = snap.DurationSec
											}
											_ = a.player.Seek(target)
											if snap.DurationSec > 0 {
												a.PlayerOverlay.SeekNormalized = target / snap.DurationSec
											}
										}
									}
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
								if FloatingCircleButton(c, playIcon, "播放器播放暂停", 38).Disabled(!ready).Clicked() && a.player != nil {
									a.wakePlayerControls(now)
									a.PlayerOverlay.LoopPaused = snap.Playing
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
								case seekBar.Changed() && held && a.player != nil:
									a.wakePlayerControls(now)
									a.player.Preview(target)
								case a.player != nil && (seekBar.Changed() || a.player.Scrubbing() && !held):
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
	})
}

func (a *App) applyPlayerPrefs() (areaFrac float64, density, lang string) {
	areaFrac = 0.5
	switch a.PlayerOverlay.DanmakuArea {
	case "quarter":
		areaFrac = 0.25
	case "full":
		areaFrac = 1.0
	}
	density = playerDensityValue(a.PlayerOverlay.DanmakuDensity)
	lang = a.PlayerOverlay.SubtitleLang
	if lang == "关闭" {
		lang = ""
	}
	if a.player == nil {
		return areaFrac, density, lang
	}
	a.player.SetDanmakuVisible(a.PlayerOverlay.DanmakuEnabled)
	a.player.SetDanmakuOpacity(a.PlayerOverlay.DanmakuOpacity)
	a.player.SetDanmakuArea(areaFrac)
	a.player.SetDanmakuDensity(density)
	a.player.SetSubtitleLang(lang)
	return areaFrac, density, lang
}

func (a *App) applyAndPersistPlayerPrefs() {
	areaFrac, density, lang := a.applyPlayerPrefs()
	visible, opacity := a.PlayerOverlay.DanmakuEnabled, a.PlayerOverlay.DanmakuOpacity
	if _, err := a.backend.MutateUIState(func(uiSt *uistate.State) {
		uiSt.Player.DanmakuVisible = visible
		uiSt.Player.DanmakuOpacity = opacity
		uiSt.Player.DanmakuArea = areaFrac
		uiSt.Player.DanmakuDensity = density
		uiSt.Player.SubtitleLang = lang
	}); err != nil {
		a.showError(err)
	}
}
