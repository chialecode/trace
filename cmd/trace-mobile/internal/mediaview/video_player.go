package mediaview

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/codeccheck"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// VideoTrack describes one playable part (分 P) or live recording segment.
type VideoTrack struct {
	Label          string
	RelPath        string
	DurationSec    int
	CodecHint      string
	AudioCodecHint string
	Width          int
	Height         int
	QualitySummary string
	AudioOnly      bool
}

// VideoRequest configures a VideoPlayer session.
type VideoRequest struct {
	ItemKey         string
	Title           string
	IsLiveRecording bool
	Tracks          []VideoTrack
	StartIndex      int
}

// VideoPlayer plays cached videos (with multi-P picker) and multi-segment live
// recordings (sequential auto-advance) via plugins/videoplayer + Host.MediaProxy(),
// with landscape fullscreen and two-layer unsupported-codec detection (FR-MOB-014).
type VideoPlayer struct {
	host    pagekit.Host
	req     VideoRequest
	win     *mygo.Window
	onClose func()

	mu             sync.Mutex
	closed         bool
	currentIdx     int
	player         PlaybackHandle
	ticket         string
	state          videoplayer.State
	unsupportedMsg string
	loadErrMsg     string
	muted          bool
	landscape      bool
	pickerOpen     bool
	seekFraction   float64
	startedInitial bool
	nativeFallback bool
}

// NewVideoPlayer constructs a testable VideoPlayer instance.
func NewVideoPlayer(h pagekit.Host, req VideoRequest, onClose func()) *VideoPlayer {
	idx := req.StartIndex
	if idx < 0 {
		idx = 0
	}
	if len(req.Tracks) > 0 && idx >= len(req.Tracks) {
		idx = len(req.Tracks) - 1
	}
	vp := &VideoPlayer{
		host:       h,
		req:        req,
		onClose:    onClose,
		currentIdx: idx,
	}
	registerActive(vp, vp.Close)
	return vp
}

// OpenVideoPlayer opens a full-screen child window playing req.
func OpenVideoPlayer(h pagekit.Host, req VideoRequest) *VideoPlayer {
	if h == nil || len(req.Tracks) == 0 {
		return nil
	}
	var vp *VideoPlayer
	vp = NewVideoPlayer(h, req, func() {
		if vp != nil && vp.win != nil {
			vp.win.Close()
		}
	})
	title := req.Title
	if strings.TrimSpace(title) == "" {
		title = "播放视频"
	}
	vp.win = h.PushWindow(title, func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		vp.View(c)
	})
	if vp.win != nil {
		vp.win.OnClose(func(*mygo.CloseEvent) {
			vp.cleanup()
		})
	}
	vp.StartTrack(vp.currentIdx)
	return vp
}

// Close stops playback, revokes proxy tickets, restores screen orientation, and closes the window.
func (vp *VideoPlayer) Close() {
	vp.cleanup()
	if vp.onClose != nil {
		vp.onClose()
	}
}

func (vp *VideoPlayer) cleanup() {
	unregisterActive(vp)
	vp.mu.Lock()
	if vp.closed {
		vp.mu.Unlock()
		return
	}
	vp.closed = true
	wasLandscape := vp.landscape
	vp.landscape = false
	vp.mu.Unlock()

	vp.stopCurrentLocked()
	if wasLandscape {
		setOrientations(mygo.OrientationAny)
	}
}

func (vp *VideoPlayer) stopCurrentLocked() {
	vp.mu.Lock()
	p := vp.player
	ticket := vp.ticket
	vp.player = nil
	vp.ticket = ""
	vp.mu.Unlock()

	if p != nil {
		p.Pause()
		p.Destroy()
	}
	if ticket != "" && vp.host != nil && vp.host.MediaProxy() != nil {
		vp.host.MediaProxy().RevokeTicket(ticket)
	}
}

// CurrentIndex returns the active track index.
func (vp *VideoPlayer) CurrentIndex() int {
	vp.mu.Lock()
	defer vp.mu.Unlock()
	return vp.currentIdx
}

// UnsupportedMessage returns the active unsupported-codec explanation, if any.
func (vp *VideoPlayer) UnsupportedMessage() string {
	vp.mu.Lock()
	defer vp.mu.Unlock()
	return vp.unsupportedMsg
}

// StartTrack starts playback of track idx after Layer-1 MediaCodecList pre-check.
func (vp *VideoPlayer) StartTrack(idx int) {
	if idx < 0 || idx >= len(vp.req.Tracks) {
		return
	}
	vp.stopCurrentLocked()

	track := vp.req.Tracks[idx]
	vp.mu.Lock()
	if vp.closed {
		vp.mu.Unlock()
		return
	}
	vp.startedInitial = true
	vp.currentIdx = idx
	vp.state = videoplayer.State{}
	vp.unsupportedMsg = ""
	vp.loadErrMsg = ""
	vp.nativeFallback = false
	vp.seekFraction = 0
	muted := vp.muted
	vp.mu.Unlock()

	// Layer 1: Pre-check codec capabilities via Kotlin MediaCodecList(ALL_CODECS).
	capRes := codeccheck.CheckTrack(track.CodecHint+" "+track.QualitySummary, track.AudioCodecHint, track.Width, track.Height)
	if capRes.Known && !capRes.Supported {
		vp.mu.Lock()
		vp.unsupportedMsg = capRes.Message
		vp.mu.Unlock()
		if vp.host != nil {
			vp.host.Update(nil)
		}
		return
	}

	if vp.host == nil || vp.host.MediaProxy() == nil {
		vp.mu.Lock()
		vp.loadErrMsg = "尚未连接到电脑端，无法读取视频流"
		vp.mu.Unlock()
		return
	}
	loopURL, ticket, err := vp.host.MediaProxy().IssueTicket(vp.req.ItemKey, track.RelPath)
	if err != nil {
		vp.mu.Lock()
		vp.loadErrMsg = "无法准备本机视频播放地址"
		vp.mu.Unlock()
		if vp.host != nil {
			vp.host.Update(nil)
		}
		return
	}

	win := vp.win
	if win == nil {
		win = vp.host.Window()
	}
	p, err := newPlaybackHandle(win, loopURL, videoplayer.Options{
		AutoPlay: true,
		Muted:    muted,
	})
	if err != nil {
		vp.host.MediaProxy().RevokeTicket(ticket)
		vp.mu.Lock()
		if errors.Is(err, errors.ErrUnsupported) {
			vp.nativeFallback = true
		} else {
			vp.loadErrMsg = "无法启动系统播放器：" + err.Error()
		}
		vp.mu.Unlock()
		if vp.host != nil {
			vp.host.Update(nil)
		}
		return
	}

	vp.mu.Lock()
	if vp.closed {
		vp.mu.Unlock()
		p.Destroy()
		vp.host.MediaProxy().RevokeTicket(ticket)
		return
	}
	vp.player = p
	vp.ticket = ticket
	vp.mu.Unlock()

	p.OnState(func(st videoplayer.State) {
		vp.handlePlayerState(p, idx, track, st)
	})
	if vp.host != nil {
		vp.host.Update(nil)
	}
}

func (vp *VideoPlayer) handlePlayerState(p PlaybackHandle, idx int, track VideoTrack, st videoplayer.State) {
	vp.mu.Lock()
	if vp.closed || vp.player != p {
		vp.mu.Unlock()
		return
	}
	vp.state = st
	if st.Duration > 0 {
		frac := float64(st.Position) / float64(st.Duration)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		vp.seekFraction = frac
	}

	// Layer 2a: Runtime silent video-track drop detection (Android MediaPlayer
	// reports Ready=true, Error="", Width=0, Height=0 for unsupported video codecs).
	if st.Ready && !track.AudioOnly && st.Width == 0 && st.Height == 0 {
		_, vLabel, _, _ := codeccheck.ParseCodecHints(track.CodecHint, track.QualitySummary)
		vp.unsupportedMsg = codeccheck.UnsupportedMessage(vLabel, false)
		vp.mu.Unlock()
		vp.stopCurrentLocked()
		if vp.host != nil {
			vp.host.Update(nil)
		}
		return
	}

	// Layer 2b: Runtime MediaPlayer error classification (codec vs network).
	if st.Error != "" {
		if msg, isCodec := classifyRuntimeCodecError(st.Error, track); isCodec {
			vp.unsupportedMsg = msg
		} else {
			vp.loadErrMsg = "视频播放失败：" + st.Error
		}
		vp.mu.Unlock()
		vp.stopCurrentLocked()
		if vp.host != nil {
			vp.host.Update(nil)
		}
		return
	}

	// Sequential multi-part live recording auto-advance.
	shouldAdvance := st.Ended && vp.req.IsLiveRecording && idx+1 < len(vp.req.Tracks)
	vp.mu.Unlock()

	if shouldAdvance {
		vp.StartTrack(idx + 1)
		return
	}
	if vp.host != nil {
		vp.host.Update(nil)
	}
}

func classifyRuntimeCodecError(errText string, track VideoTrack) (string, bool) {
	lower := strings.ToLower(errText)
	codecSignals := []string{
		"codec", "decoder", "unsupported", "format", "av1", "av01", "hevc", "hvc1", "h265", "flac", "media_error_unsupported",
	}
	isCodec := false
	for _, sig := range codecSignals {
		if strings.Contains(lower, sig) {
			isCodec = true
			break
		}
	}
	_, vLabel, _, aLabel := codeccheck.ParseCodecHints(track.CodecHint, track.QualitySummary, track.AudioCodecHint)
	if isCodec {
		if vLabel != "" {
			return codeccheck.UnsupportedMessage(vLabel, false), true
		}
		if aLabel != "" {
			return codeccheck.UnsupportedMessage(aLabel, true), true
		}
		return codeccheck.UnsupportedMessage("", false), true
	}
	// If error is not obviously HTTP/network and track uses a codec prone to phone incompatibility (AV1/HEVC/FLAC).
	if !strings.Contains(lower, "http") && !strings.Contains(lower, "network") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "refused") {
		if vLabel == "AV1" || vLabel == "HEVC" || vLabel == "VP9" {
			return codeccheck.UnsupportedMessage(vLabel, false), true
		}
		if aLabel == "FLAC" {
			return codeccheck.UnsupportedMessage(aLabel, true), true
		}
	}
	return "", false
}

// ToggleLandscape toggles landscape fullscreen via mygo.SetOrientations.
func (vp *VideoPlayer) ToggleLandscape() {
	vp.mu.Lock()
	vp.landscape = !vp.landscape
	land := vp.landscape
	vp.mu.Unlock()
	if land {
		setOrientations(mygo.OrientationLandscape)
	} else {
		setOrientations(mygo.OrientationAny)
	}
}

// View renders the full-screen video player with overlays and multi-P / segment picker sheet.
func (vp *VideoPlayer) View(c *ui.Context) {
	vp.mu.Lock()
	started := vp.startedInitial
	vp.mu.Unlock()
	if !started && len(vp.req.Tracks) > 0 {
		vp.StartTrack(vp.currentIdx)
	}

	c.OnWindowShortcut(0, ui.KeyEscape, func() { vp.Close() })

	vp.mu.Lock()
	idx := vp.currentIdx
	p := vp.player
	st := vp.state
	unsupported := vp.unsupportedMsg
	loadErr := vp.loadErrMsg
	muted := vp.muted
	landscape := vp.landscape
	nativeFallback := vp.nativeFallback
	seekVal := vp.seekFraction
	vp.mu.Unlock()

	var curTrack VideoTrack
	if idx >= 0 && idx < len(vp.req.Tracks) {
		curTrack = vp.req.Tracks[idx]
	}

	ui.Box(c).Fill().Background(ui.Hex("#0d0d0d")).Children(func() {
		// Video surface or state panel.
		switch {
		case unsupported != "":
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(12).Children(func() {
				DrawIcon(c, "alert-circle", 40, theme.ColorDotYellow, 1.8)
				ui.Text(c, unsupported).
					FontSize(16).FontWeight(600).TextColor(theme.ColorWhite).TextAlign(ui.Center)
				if curTrack.QualitySummary != "" {
					ui.Text(c, "文件规格："+curTrack.QualitySummary).
						FontSize(13).TextColor(ui.RGBA(255, 255, 255, 0.7)).TextAlign(ui.Center)
				}
				ui.Text(c, "电脑端不转码，可在电脑端 TRACE 中播放。").
					FontSize(13).LineHeight(theme.LineHeightCompact).TextColor(ui.RGBA(255, 255, 255, 0.65)).TextAlign(ui.Center)
			})
		case loadErr != "":
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(12).Children(func() {
				DrawIcon(c, "alert-circle", 36, theme.ColorError, 1.8)
				ui.Text(c, loadErr).FontSize(15).TextColor(theme.ColorWhite).TextAlign(ui.Center)
				retryBtn := ui.Button(c, "").
					Label("重试播放").
					MinHeight(44).Padding(8, 20).Radius(999).Background(theme.ColorAccent)
				retryBtn.Children(func() {
					ui.Text(c, "重试").FontSize(14).FontWeight(600).TextColor(theme.ColorWhite)
				})
				if retryBtn.Clicked() {
					vp.StartTrack(idx)
				}
			})
		case p != nil && p.NativeView() != nil:
			ui.WebView(c, p.NativeView()).Fill()
		case nativeFallback:
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Padding(32, 24).Gap(10).Children(func() {
				DrawIcon(c, "video", 36, theme.ColorWhite, 1.7)
				ui.Text(c, "系统播放器仅在手机端可用").FontSize(15).FontWeight(600).TextColor(theme.ColorWhite)
				if curTrack.Label != "" {
					ui.Text(c, curTrack.Label).FontSize(13).TextColor(ui.RGBA(255, 255, 255, 0.72))
				}
			})
		default:
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, "正在缓冲视频…").FontSize(14).TextColor(ui.RGBA(255, 255, 255, 0.78))
			})
		}

		// Top translucent bar: close, title, track badge, landscape toggle.
		ui.Row(c).
			Absolute().Top(0).Left(0).Right(0).ZIndex(10).
			Background(ui.RGBA(0, 0, 0, 0.58)).
			Padding(10, 12).
			Gap(8).
			AlignItems(ui.Center).
			Children(func() {
				closeBtn := ui.Button(c, "").
					Label("关闭播放器").
					Size(44, 44).Radius(22).
					Background(ui.RGBA(255, 255, 255, 0.14)).
					Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
				closeBtn.Children(func() {
					DrawIcon(c, "close", 20, theme.ColorWhite, 2)
				})
				if closeBtn.Clicked() {
					vp.Close()
				}

				ui.Column(c).Grow(1).Basis(0).Gap(2).Children(func() {
					title := vp.req.Title
					if title == "" {
						title = curTrack.Label
					}
					ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					if len(vp.req.Tracks) > 1 {
						sub := fmt.Sprintf("P%d / %d", idx+1, len(vp.req.Tracks))
						if vp.req.IsLiveRecording {
							sub = fmt.Sprintf("分段 %d / %d", idx+1, len(vp.req.Tracks))
						}
						if curTrack.Label != "" {
							sub += " · " + curTrack.Label
						}
						ui.Text(c, sub).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.75)).SingleLine()
					}
				})

				landLabel := "横屏全屏"
				landText := "横屏"
				if landscape {
					landLabel = "退出横屏"
					landText = "竖屏"
				}
				landBtn := ui.Button(c, "").
					Label(landLabel).
					MinHeight(38).Padding(6, 12).Radius(999).
					Background(ui.RGBA(255, 255, 255, 0.18)).
					Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
				landBtn.Children(func() {
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						DrawIcon(c, "fullscreen", 15, theme.ColorWhite, 1.8)
						ui.Text(c, landText).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
				})
				if landBtn.Clicked() {
					vp.ToggleLandscape()
				}
			})

		// Bottom translucent control bar.
		ui.Column(c).
			Absolute().Bottom(0).Left(0).Right(0).ZIndex(10).
			Background(ui.RGBA(0, 0, 0, 0.62)).
			Padding(10, 14).
			Gap(8).
			Children(func() {
				// Progress slider & timestamps when playable.
				if unsupported == "" && loadErr == "" {
					durSec := int(st.Duration.Seconds())
					if durSec <= 0 {
						durSec = curTrack.DurationSec
					}
					posSec := int(st.Position.Seconds())
					ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
						posText := FormatDuration(posSec)
						if posText == "" {
							posText = "00:00"
						}
						ui.Text(c, posText).FontSize(12).TextColor(theme.ColorWhite).SingleLine()

						sliderVal := seekVal
						sl := ui.Slider(c, &sliderVal, 0, 1).Label("播放进度").Grow(1).Basis(0)
						if sl.Changed() && p != nil {
							targetDur := st.Duration
							if targetDur <= 0 && curTrack.DurationSec > 0 {
								targetDur = time.Duration(curTrack.DurationSec) * time.Second
							}
							if targetDur > 0 {
								p.Seek(time.Duration(float64(targetDur) * sliderVal))
								vp.mu.Lock()
								vp.seekFraction = sliderVal
								vp.mu.Unlock()
							}
						}

						durText := FormatDuration(durSec)
						if durText == "" {
							durText = "--:--"
						}
						ui.Text(c, durText).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.8)).SingleLine()
					})
				}

				ui.Row(c).FillWidth().Gap(10).AlignItems(ui.Center).Children(func() {
					if unsupported == "" && loadErr == "" && p != nil {
						playLbl := "播放"
						playIcon := "play"
						if st.Playing {
							playLbl = "暂停"
							playIcon = "pause"
						}
						playBtn := ui.Button(c, "").
							Label(playLbl).
							Size(44, 44).Radius(22).
							Background(ui.RGBA(255, 255, 255, 0.16)).
							Justify(ui.Center).AlignItems(ui.Center)
						playBtn.Children(func() {
							DrawIcon(c, playIcon, 18, theme.ColorWhite, 1.8)
						})
						if playBtn.Clicked() {
							if st.Playing {
								p.Pause()
							} else {
								p.Play()
							}
						}

						muteIcon := "volume"
						muteLbl := "静音"
						if muted {
							muteIcon = "volume-mute"
							muteLbl = "取消静音"
						}
						muteBtn := ui.Button(c, "").
							Label(muteLbl).
							Size(44, 44).Radius(22).
							Background(ui.RGBA(255, 255, 255, 0.14)).
							Justify(ui.Center).AlignItems(ui.Center)
						muteBtn.Children(func() {
							DrawIcon(c, muteIcon, 18, theme.ColorWhite, 1.8)
						})
						if muteBtn.Clicked() {
							vp.mu.Lock()
							vp.muted = !vp.muted
							newMuted := vp.muted
							vp.mu.Unlock()
							if newMuted {
								p.SetVolume(0)
							} else {
								p.SetVolume(1)
							}
						}
					}

					ui.Box(c).Grow(1).Basis(0)

					// Multi-P or multi-segment picker button.
					if len(vp.req.Tracks) > 1 {
						btnTitle := fmt.Sprintf("选集 (%d)", len(vp.req.Tracks))
						if vp.req.IsLiveRecording {
							btnTitle = fmt.Sprintf("分段 (%d)", len(vp.req.Tracks))
						}
						pickBtn := ui.Button(c, "").
							Label(btnTitle).
							MinHeight(40).Padding(8, 14).Radius(999).
							Background(ui.RGBA(255, 255, 255, 0.20)).
							Justify(ui.Center).AlignItems(ui.Center)
						pickBtn.Children(func() {
							ui.Text(c, btnTitle).FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						if pickBtn.Clicked() {
							vp.pickerOpen = true
						}
					}
				})
			})

		// Multi-P / live segment picker bottom sheet (mobile.Sheet).
		if len(vp.req.Tracks) > 1 {
			sheetTitle := "选择分 P"
			if vp.req.IsLiveRecording {
				sheetTitle = "选择录像分段"
			}
			bg := theme.ColorBgMain
			mobile.Sheet(c, &vp.pickerOpen, mobile.SheetOptions{
				Detents:    []float32{0.55, 0.85},
				Background: &bg,
				Label:      sheetTitle,
			}, func() {
				ui.Column(c).Fill().Padding(8, 16).Gap(8).Children(func() {
					ui.Text(c, sheetTitle).FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain)
					ui.Scroll(c).Grow(1).Basis(0).FillWidth().Children(func() {
						ui.Column(c).FillWidth().Gap(6).Children(func() {
							for i, tr := range vp.req.Tracks {
								selected := i == idx
								rowBg := theme.ColorBgMain
								borderCol := theme.ColorBorder
								titleCol := theme.ColorTextMain
								if selected {
									rowBg = theme.ColorAccentBg
									borderCol = theme.ColorAccent
									titleCol = theme.ColorAccent
								}
								prefix := fmt.Sprintf("P%d", i+1)
								if vp.req.IsLiveRecording {
									prefix = fmt.Sprintf("分段 %d", i+1)
								}
								label := prefix
								if tr.Label != "" {
									label += " · " + tr.Label
								}
								rowBtn := ui.Button(c.Key(i), "").
									Label(label).
									FillWidth().
									MinHeight(48).
									Padding(10, 12).
									Radius(10).
									Background(rowBg).
									Border(1, borderCol).
									Justify(ui.Start).
									AlignItems(ui.Center)
								rowBtn.Children(func() {
									ui.Column(c).FillWidth().Gap(2).Children(func() {
										ui.Text(c, label).FontSize(14).FontWeight(500).TextColor(titleCol).SingleLine()
										meta := make([]string, 0, 2)
										if d := FormatDuration(tr.DurationSec); d != "" {
											meta = append(meta, d)
										}
										if tr.QualitySummary != "" {
											meta = append(meta, tr.QualitySummary)
										}
										if len(meta) > 0 {
											ui.Text(c, strings.Join(meta, " · ")).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
										}
									})
								})
								if rowBtn.Clicked() {
									vp.pickerOpen = false
									vp.StartTrack(i)
								}
							}
						})
					})
				})
			})
		}
	})
}
