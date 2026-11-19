package ui

import (
	"fmt"
	"time"

	"github.com/chialecode/trace/internal/uistate"
	"github.com/egoist/mygo/ui"
)

func (a *App) wakePlayerControls(now time.Time) {
	a.PlayerOverlay.LastActiveAt = now
	a.PlayerOverlay.ControlsHidden = false
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

	ready := !a.PlayerOverlay.Loading && a.PlayerOverlay.LoadError == "" && !a.PlayerOverlay.DecoderMissing
	if ready && c.Shortcut(0, ui.KeySpace) {
		a.wakePlayerControls(now)
		a.player.TogglePlay()
	}
	if ready && snap.DurationSec > 0 && c.Shortcut(0, ui.KeyLeft) {
		a.wakePlayerControls(now)
		_ = a.player.Seek(max(0, snap.PositionSec-5))
	}
	if ready && snap.DurationSec > 0 && c.Shortcut(0, ui.KeyRight) {
		a.wakePlayerControls(now)
		_ = a.player.Seek(min(snap.DurationSec, snap.PositionSec+5))
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
								if err := a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath); err != nil {
									a.showError(err)
								}
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
							ui.Box(c).Shrink(1).ClipX().Padding(6, 14).Radius(999).Background(ui.RGBA(0, 0, 0, 0.48)).Children(func() {
								ui.Text(c, a.PlayerOverlay.Title).FontSize(14).FontWeight(600).TextColor(ColorWhite).SingleLine()
							})
							ui.Row(c).Shrink(0).Gap(10).AlignItems(ui.Center).Children(func() {
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

								ui.Row(c).FillWidth().Padding(4, 0, 0, 0).Gap(8).Wrap().Children(func() {
									if a.PlayerOverlay.ItemKey != "" {
										if FloatingPillButton(c, "folder", "在文件夹中显示", "播放层打开文件夹").Clicked() {
											a.wakePlayerControls(now)
											if err := a.backend.OpenItemFolder(a.PlayerOverlay.ItemKey); err != nil {
												a.showError(err)
											}
										}
									}
									if FloatingPillButton(c, "external-link", "用系统播放器打开", "用系统播放器打开").Clicked() {
										a.wakePlayerControls(now)
										if err := a.backend.Desktop().OpenPath(a.PlayerOverlay.MediaPath); err != nil {
											a.showError(err)
										}
									}
								})
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
							if FloatingCircleButton(c, playIcon, "播放器播放暂停", 38).Disabled(!ready).Clicked() {
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

func (a *App) applyPlayerPrefs() (areaFrac float64, density, lang string) {
	areaFrac = 0.5
	switch a.PlayerOverlay.DanmakuArea {
	case "quarter":
		areaFrac = 0.25
	case "full":
		areaFrac = 1.0
	}
	a.player.SetDanmakuVisible(a.PlayerOverlay.DanmakuEnabled)
	a.player.SetDanmakuOpacity(a.PlayerOverlay.DanmakuOpacity)
	a.player.SetDanmakuArea(areaFrac)
	density = playerDensityValue(a.PlayerOverlay.DanmakuDensity)
	a.player.SetDanmakuDensity(density)

	lang = a.PlayerOverlay.SubtitleLang
	if lang == "关闭" {
		lang = ""
	}
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
