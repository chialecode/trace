package mediaview

import (
	"fmt"
	"sync"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// ImageItem describes one image (or live photo / GIF) displayed in ImageViewer.
type ImageItem struct {
	ItemKey            string
	RelPath            string
	NoWatermarkRelPath string // Weibo image_nowm companion file (DEC-078)
	MotionRelPath      string // Live photo MP4 clip
	GIF                bool
	Width              int
	Height             int
}

// ImageViewer renders a full-screen zoomable, swipeable image gallery with
// ?w=2560 default loading, "查看原图" full file toggle, Weibo "原图 / 无水印"
// companion toggle, and automatic live photo clip playback (FR-MOB-013).
type ImageViewer struct {
	host       pagekit.Host
	loader     *ImageLoader
	items      []ImageItem
	win        *mygo.Window
	onClose    func()
	pageState  mobile.PageState
	zoomStates []mobile.ZoomState

	// Per-image display options (UI thread).
	wantOriginal    []bool
	wantNoWatermark []bool
	lastMotionPage  int

	// Live photo clip playback state.
	mu           sync.Mutex
	closed       bool
	motionPlayer PlaybackHandle
	motionTicket string
	motionActive bool
	motionEnded  bool
	motionErr    string
}

// NewImageViewer constructs a testable ImageViewer instance.
func NewImageViewer(h pagekit.Host, items []ImageItem, startIndex int, onClose func()) *ImageViewer {
	if startIndex < 0 {
		startIndex = 0
	}
	if len(items) > 0 && startIndex >= len(items) {
		startIndex = len(items) - 1
	}
	v := &ImageViewer{
		host:            h,
		loader:          NewImageLoader(24),
		items:           append([]ImageItem(nil), items...),
		onClose:         onClose,
		pageState:       mobile.PageState{Page: startIndex},
		zoomStates:      make([]mobile.ZoomState, len(items)),
		wantOriginal:    make([]bool, len(items)),
		wantNoWatermark: make([]bool, len(items)),
		lastMotionPage:  -1,
	}
	registerActive(v, v.Close)
	return v
}

// OpenImageViewer opens a full-screen child window displaying items starting at startIndex.
func OpenImageViewer(h pagekit.Host, items []ImageItem, startIndex int) *ImageViewer {
	if h == nil || len(items) == 0 {
		return nil
	}
	var v *ImageViewer
	v = NewImageViewer(h, items, startIndex, func() {
		if v != nil && v.win != nil {
			v.win.Close()
		}
	})
	v.win = h.PushWindow("查看图片", func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		v.View(c)
	})
	if v.win != nil {
		v.win.OnClose(func(*mygo.CloseEvent) {
			v.cleanup()
		})
	}
	return v
}

// Close releases all playback resources and closes the viewer.
func (v *ImageViewer) Close() {
	v.cleanup()
	if v.onClose != nil {
		v.onClose()
	}
}

func (v *ImageViewer) cleanup() {
	unregisterActive(v)
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		return
	}
	v.closed = true
	v.mu.Unlock()
	v.stopMotionClip()
	v.loader.Clear()
}

// CurrentIndex returns the currently displayed image index.
func (v *ImageViewer) CurrentIndex() int {
	idx := v.pageState.Page
	if idx < 0 {
		return 0
	}
	if len(v.items) > 0 && idx >= len(v.items) {
		return len(v.items) - 1
	}
	return idx
}

// ActivePath returns the currently requested media path for index i.
func (v *ImageViewer) ActivePath(i int) string {
	if i < 0 || i >= len(v.items) {
		return ""
	}
	item := v.items[i]
	rel := item.RelPath
	if v.wantNoWatermark[i] && item.NoWatermarkRelPath != "" {
		rel = item.NoWatermarkRelPath
	}
	return MediaViewerPath(item.ItemKey, rel, v.wantOriginal[i], item.GIF)
}

// IsMotionPlaying reports whether a live photo clip is currently active.
func (v *ImageViewer) IsMotionPlaying() bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.motionActive && v.motionPlayer != nil
}

func (v *ImageViewer) stopMotionClip() {
	v.mu.Lock()
	p := v.motionPlayer
	ticket := v.motionTicket
	v.motionPlayer = nil
	v.motionTicket = ""
	v.motionActive = false
	v.motionEnded = false
	v.motionErr = ""
	v.mu.Unlock()

	if p != nil {
		p.Pause()
		p.Destroy()
	}
	if ticket != "" && v.host != nil && v.host.MediaProxy() != nil {
		v.host.MediaProxy().RevokeTicket(ticket)
	}
}

func (v *ImageViewer) startMotionClip(idx int) {
	v.stopMotionClip()
	if idx < 0 || idx >= len(v.items) {
		return
	}
	item := v.items[idx]
	if item.MotionRelPath == "" || v.host == nil {
		return
	}
	proxy := v.host.MediaProxy()
	if proxy == nil {
		return
	}
	loopURL, ticket, err := proxy.IssueTicket(item.ItemKey, item.MotionRelPath)
	if err != nil {
		return
	}
	win := v.win
	if win == nil {
		win = v.host.Window()
	}
	p, err := newPlaybackHandle(win, loopURL, videoplayer.Options{AutoPlay: true})
	if err != nil {
		// Desktop / ui.Tester where system videoplayer is unsupported: revoke ticket cleanly.
		proxy.RevokeTicket(ticket)
		return
	}
	v.mu.Lock()
	if v.closed {
		v.mu.Unlock()
		p.Destroy()
		proxy.RevokeTicket(ticket)
		return
	}
	v.motionPlayer = p
	v.motionTicket = ticket
	v.motionActive = true
	v.motionEnded = false
	v.motionErr = ""
	v.mu.Unlock()

	p.OnState(func(st videoplayer.State) {
		v.mu.Lock()
		if v.motionPlayer != p {
			v.mu.Unlock()
			return
		}
		if st.Error != "" {
			v.motionErr = st.Error
			v.motionActive = false
		} else if st.Ended {
			v.motionEnded = true
			v.motionActive = false
		}
		v.mu.Unlock()
		if v.host != nil {
			v.host.Update(nil)
		}
	})
}

// View renders the full-screen image viewer.
func (v *ImageViewer) View(c *ui.Context) {
	if len(v.items) == 0 {
		ui.Column(c).Fill().Background(ui.Hex("#000000")).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "没有可查看的图片").FontSize(15).TextColor(theme.ColorWhite)
		})
		return
	}

	// Keyboard shortcuts for desktop/accessibility/testing.
	c.OnWindowShortcut(0, ui.KeyEscape, func() { v.Close() })
	c.OnWindowShortcut(0, ui.KeyLeft, func() {
		if v.pageState.Page > 0 {
			v.pageState.Page--
		}
	})
	c.OnWindowShortcut(0, ui.KeyRight, func() {
		if v.pageState.Page < len(v.items)-1 {
			v.pageState.Page++
		}
	})

	curIdx := v.CurrentIndex()
	if curIdx != v.lastMotionPage {
		v.lastMotionPage = curIdx
		if v.items[curIdx].MotionRelPath != "" {
			v.startMotionClip(curIdx)
		} else {
			v.stopMotionClip()
		}
	}

	cur := v.items[curIdx]
	v.mu.Lock()
	motionPlayer := v.motionPlayer
	motionActive := v.motionActive
	v.mu.Unlock()

	ui.Box(c).Fill().Background(ui.Hex("#000000")).Children(func() {
		// Full-screen swipeable pages.
		mobile.PageView(c, &v.pageState, len(v.items), func(i int) {
			v.renderPage(c, i, i == curIdx && motionActive && motionPlayer != nil, motionPlayer)
		}).Fill()

		// Top translucent bar: close, N / M, Weibo 原图/无水印 toggle, 查看原图.
		ui.Row(c).
			Absolute().Top(0).Left(0).Right(0).ZIndex(10).
			Background(ui.RGBA(0, 0, 0, 0.58)).
			Padding(10, 12).
			Gap(8).
			AlignItems(ui.Center).
			Children(func() {
				closeBtn := ui.Button(c, "").
					Label("关闭看图").
					Size(44, 44).
					Radius(22).
					Background(ui.RGBA(255, 255, 255, 0.14)).
					Justify(ui.Center).
					AlignItems(ui.Center).
					Shrink(0)
				closeBtn.Children(func() {
					DrawIcon(c, "close", 20, theme.ColorWhite, 2)
				})
				if closeBtn.Clicked() {
					v.Close()
				}

				ui.Text(c, fmt.Sprintf("%d / %d", curIdx+1, len(v.items))).
					FontSize(14).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()

				ui.Box(c).Grow(1).Basis(0)

				// Weibo 原图 / 无水印 segmented toggle (DEC-078), only when image_nowm exists.
				if cur.NoWatermarkRelPath != "" {
					v.renderWatermarkToggle(c, curIdx)
				}

				// "查看原图" button (hidden for GIFs which already fetch original).
				if !cur.GIF {
					if v.wantOriginal[curIdx] {
						ui.Box(c).
							Padding(6, 12).
							Radius(999).
							Background(ui.RGBA(255, 255, 255, 0.16)).
							Children(func() {
								ui.Text(c, "已显示原图").FontSize(12).FontWeight(500).TextColor(ui.RGBA(255, 255, 255, 0.85)).SingleLine()
							})
					} else {
						origBtn := ui.Button(c, "").
							Label("查看原图").
							MinHeight(38).
							Padding(6, 12).
							Radius(999).
							Background(ui.RGBA(255, 255, 255, 0.22)).
							Justify(ui.Center).
							AlignItems(ui.Center).
							Shrink(0)
						origBtn.Children(func() {
							ui.Text(c, "查看原图").FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						if origBtn.Clicked() {
							v.wantOriginal[curIdx] = true
						}
					}
				}
			})

		// Top-left live photo badge under top bar.
		if cur.MotionRelPath != "" {
			ui.Row(c).
				Absolute().Top(68).Left(14).ZIndex(10).
				Padding(4, 10).
				Radius(999).
				Background(ui.RGBA(0, 0, 0, 0.62)).
				Border(1, ui.RGBA(255, 255, 255, 0.24)).
				Gap(5).
				AlignItems(ui.Center).
				Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(theme.ColorLive)
					ui.Text(c, "实况").FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
				})
		}

		// Bottom navigation / live replay bar.
		ui.Row(c).
			Absolute().Bottom(16).Left(16).Right(16).ZIndex(10).
			Justify(ui.Center).
			AlignItems(ui.Center).
			Gap(12).
			Children(func() {
				if len(v.items) > 1 {
					prevBtn := ui.Button(c, "").
						Label("上一张").
						Size(44, 44).
						Radius(22).
						Background(ui.RGBA(0, 0, 0, 0.56)).
						Justify(ui.Center).
						AlignItems(ui.Center).
						Disabled(curIdx == 0)
					prevBtn.Children(func() {
						DrawIcon(c, "chevron-left", 20, theme.ColorWhite, 2)
					})
					if prevBtn.Clicked() && curIdx > 0 {
						v.pageState.Page = curIdx - 1
					}
				}

				if cur.MotionRelPath != "" {
					replayLabel := "重播实况"
					if !motionActive {
						replayLabel = "播放实况"
					}
					replayBtn := ui.Button(c, "").
						Label(replayLabel).
						MinHeight(44).
						Padding(8, 16).
						Radius(999).
						Background(ui.RGBA(0, 0, 0, 0.62)).
						Border(1, ui.RGBA(255, 255, 255, 0.25)).
						Justify(ui.Center).
						AlignItems(ui.Center)
					replayBtn.Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							DrawIcon(c, "play", 14, theme.ColorWhite, 1.8)
							ui.Text(c, replayLabel).FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
					})
					if replayBtn.Clicked() {
						v.startMotionClip(curIdx)
					}
				}

				if len(v.items) > 1 {
					nextBtn := ui.Button(c, "").
						Label("下一张").
						Size(44, 44).
						Radius(22).
						Background(ui.RGBA(0, 0, 0, 0.56)).
						Justify(ui.Center).
						AlignItems(ui.Center).
						Disabled(curIdx >= len(v.items)-1)
					nextBtn.Children(func() {
						DrawIcon(c, "chevron-right", 20, theme.ColorWhite, 2)
					})
					if nextBtn.Clicked() && curIdx < len(v.items)-1 {
						v.pageState.Page = curIdx + 1
					}
				}
			})
	})
}

func (v *ImageViewer) renderWatermarkToggle(c *ui.Context, idx int) {
	noWM := v.wantNoWatermark[idx]
	ui.Row(c).
		Radius(999).
		Background(ui.RGBA(255, 255, 255, 0.16)).
		Padding(2, 2).
		Gap(2).
		Shrink(0).
		Children(func() {
			origBg := ui.Color{}
			if !noWM {
				origBg = theme.ColorAccent
			}
			origBtn := ui.Button(c, "").
				Label("切换原图").
				MinHeight(36).
				Padding(4, 10).
				Radius(999).
				Background(origBg).
				Justify(ui.Center).
				AlignItems(ui.Center)
			origBtn.Children(func() {
				ui.Text(c, "原图").FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			})
			if origBtn.Clicked() {
				v.wantNoWatermark[idx] = false
			}

			nowmBg := ui.Color{}
			if noWM {
				nowmBg = theme.ColorAccent
			}
			nowmBtn := ui.Button(c, "").
				Label("切换无水印").
				MinHeight(36).
				Padding(4, 10).
				Radius(999).
				Background(nowmBg).
				Justify(ui.Center).
				AlignItems(ui.Center)
			nowmBtn.Children(func() {
				ui.Text(c, "无水印").FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
			})
			if nowmBtn.Clicked() {
				v.wantNoWatermark[idx] = true
			}
		})
}

func (v *ImageViewer) renderPage(c *ui.Context, i int, showMotion bool, motionPlayer PlaybackHandle) {
	if showMotion && motionPlayer != nil && motionPlayer.NativeView() != nil {
		ui.Box(c).Fill().Background(ui.Hex("#000000")).Children(func() {
			ui.WebView(c, motionPlayer.NativeView()).Fill()
		})
		return
	}

	path := v.ActivePath(i)
	src, loading, failed := v.loader.GetOrLoad(v.host, path)

	mobile.Zoomable(c, &v.zoomStates[i], mobile.ZoomOptions{
		Min:       1,
		Max:       4,
		DoubleTap: 2,
	}, func() {
		ui.Box(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Children(func() {
			switch {
			case src != nil:
				ui.Image(c, src).Fit(ui.Contain).Fill()
			case failed:
				ui.Column(c).Gap(10).AlignItems(ui.Center).Padding(20, 20).Children(func() {
					ui.Text(c, "图片加载失败").FontSize(14).TextColor(theme.ColorWhite)
					retryBtn := ui.Button(c, "").
						Label("重试加载图片").
						MinHeight(44).
						Padding(8, 18).
						Radius(999).
						Background(ui.RGBA(255, 255, 255, 0.2))
					retryBtn.Children(func() {
						ui.Text(c, "重试").FontSize(14).FontWeight(600).TextColor(theme.ColorWhite)
					})
					if retryBtn.Clicked() {
						v.loader.Retry(v.host, path)
					}
				})
			case loading:
				ui.Text(c, "正在加载…").FontSize(14).TextColor(ui.RGBA(255, 255, 255, 0.72))
			default:
				ui.Box(c).Size(120, 120).Radius(12).Background(ui.RGBA(255, 255, 255, 0.08))
			}
		})
	}).Fill()
}
