package subscriptions

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

type viewableImage struct {
	Path        string
	NoWatermark string
	GIF         bool
}

type playableSegment struct {
	Label       string
	RelPath     string
	Codec       string
	QualityName string
	DurationSec int
}

func collectFeedImages(item companionapi.FeedItem) []viewableImage {
	var out []viewableImage
	for _, im := range item.Images {
		if strings.TrimSpace(im.Path) == "" {
			continue
		}
		out = append(out, viewableImage{
			Path:        im.Path,
			NoWatermark: im.NoWatermark,
			GIF:         im.GIF,
		})
	}
	if len(out) == 0 {
		for _, pg := range item.NotePages {
			path := firstNonEmpty(pg.Image, pg.Cover)
			if path == "" {
				continue
			}
			out = append(out, viewableImage{
				Path:        path,
				NoWatermark: pg.NoWatermark,
				GIF:         pg.GIF,
			})
		}
	}
	return out
}

func collectPlayableVideos(item companionapi.FeedItem) []playableSegment {
	var out []playableSegment
	if item.Live != nil && len(item.Live.Segments) > 0 {
		for i, seg := range item.Live.Segments {
			if strings.TrimSpace(seg.File) == "" {
				continue
			}
			out = append(out, playableSegment{
				Label:       fmt.Sprintf("第 %d 段", i+1),
				RelPath:     seg.File,
				Codec:       firstNonEmpty(seg.Codec, item.Live.Codec),
				QualityName: firstNonEmpty(seg.QualityName, item.Live.QualityName),
				DurationSec: int(seg.DurationSec),
			})
		}
		return out
	}
	if len(item.Parts) > 0 {
		for i, pt := range item.Parts {
			if strings.TrimSpace(pt.File) == "" {
				continue
			}
			lbl := pt.Title
			if lbl == "" {
				pNum := pt.Page
				if pNum <= 0 {
					pNum = i + 1
				}
				lbl = fmt.Sprintf("P%d", pNum)
			}
			out = append(out, playableSegment{
				Label:       lbl,
				RelPath:     pt.File,
				Codec:       pt.Codecs,
				QualityName: pt.QualityName,
				DurationSec: pt.Duration,
			})
		}
		return out
	}
	if strings.TrimSpace(item.VideoRelPath) != "" {
		out = append(out, playableSegment{
			Label:       firstNonEmpty(item.Title, "视频"),
			RelPath:     item.VideoRelPath,
			QualityName: item.QualitySummary,
			DurationSec: item.Duration,
		})
	}
	return out
}

// openImageViewer pushes a full-screen zoomable image viewer window (FR-MOB-013 / FR-MOB-015).
func (p *Page) openImageViewer(h pagekit.Host, item companionapi.FeedItem, imgs []viewableImage, startIdx int) {
	if len(imgs) == 0 {
		return
	}
	if startIdx < 0 || startIdx >= len(imgs) {
		startIdx = 0
	}
	pageSt := mobile.PageState{Page: startIdx}
	zoomStates := make([]mobile.ZoomState, len(imgs))
	showOriginal := false
	useNoWatermark := false

	var win *mygo.Window
	title := firstNonEmpty(item.Title, "查看图片")
	win = h.PushWindow(title, func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		curIdx := pageSt.Page
		if curIdx < 0 || curIdx >= len(imgs) {
			curIdx = 0
		}
		cur := imgs[curIdx]

		ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
			// Top translucent bar.
			ui.Row(c).
				FillWidth().
				Padding(10, 12).
				Background(ui.RGBA(0, 0, 0, 0.72)).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				Shrink(0).
				ClipX().
				Children(func() {
					closeBtn := ui.ButtonBase(c).Label("关闭看图").Padding(8, 12).MinHeight(40).Radius(999).Background(ui.RGBA(255, 255, 255, 0.14))
					closeBtn.Children(func() {
						ui.Text(c, "关闭").FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
					if closeBtn.Clicked() && win != nil {
						win.Close()
					}

					ui.Text(c, fmt.Sprintf("%d / %d", curIdx+1, len(imgs))).
						FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()

					ui.Row(c).AlignItems(ui.Center).Gap(6).Shrink(0).Children(func() {
						if cur.NoWatermark != "" {
							wmLbl := "无水印"
							if useNoWatermark {
								wmLbl = "官方原图"
							}
							wmBtn := ui.ButtonBase(c).Label("切换原图或无水印").Padding(6, 10).MinHeight(36).Radius(999).Background(ui.RGBA(255, 255, 255, 0.16))
							wmBtn.Children(func() {
								ui.Text(c, wmLbl).FontSize(12).TextColor(theme.ColorWhite).SingleLine()
							})
							if wmBtn.Clicked() {
								useNoWatermark = !useNoWatermark
							}
						}
						origLbl := "查看原图"
						if showOriginal {
							origLbl = "已加载原图"
						}
						origBtn := ui.ButtonBase(c).Label("查看原图").Padding(6, 10).MinHeight(36).Radius(999).Background(ui.RGBA(255, 255, 255, 0.16))
						origBtn.Children(func() {
							ui.Text(c, origLbl).FontSize(12).TextColor(theme.ColorWhite).SingleLine()
						})
						if origBtn.Clicked() {
							showOriginal = true
						}
					})
				})

			// Zoomable swipeable pager.
			mobile.PageView(c, &pageSt, len(imgs), func(i int) {
				im := imgs[i]
				rel := im.Path
				if useNoWatermark && im.NoWatermark != "" {
					rel = im.NoWatermark
				}
				reqPath := companionapi.MediaPath(item.ItemKey, rel)
				maxSide := 2560
				if !showOriginal && !im.GIF {
					reqPath += "?w=2560"
				} else {
					maxSide = 0
				}
				bmp := p.lookupBitmap(h, reqPath, maxSide)
				mobile.Zoomable(c.Key(i), &zoomStates[i], mobile.ZoomOptions{
					Min:       1,
					Max:       4,
					DoubleTap: 2,
				}, func() {
					ui.Box(c).Fill().Center().Children(func() {
						if bmp != nil {
							ui.Image(c, bmp).Fill().Fit(ui.Contain)
						} else {
							ui.Text(c, "正在加载图片…").FontSize(14).TextColor(theme.ColorWhite)
						}
					})
				}).Fill()
			}).Grow(1).Basis(0).FillWidth()
		})
	})
}

// openVideoPlayer pushes a full-screen video player window using plugins/videoplayer + Host.MediaProxy() (FR-MOB-014 / FR-MOB-015).
func (p *Page) openVideoPlayer(h pagekit.Host, item companionapi.FeedItem, segs []playableSegment) {
	if len(segs) == 0 {
		return
	}
	proxy := h.MediaProxy()

	var (
		mu             sync.Mutex
		win            *mygo.Window
		player         *videoplayer.Player
		currentTicket  string
		currentIdx     int
		playerState    videoplayer.State
		playerErr      string
		codecUnsupText string
		closed         bool
	)

	cleanup := func() {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		closed = true
		pl := player
		player = nil
		tk := currentTicket
		currentTicket = ""
		w := win
		mu.Unlock()

		if pl != nil {
			pl.Destroy()
		}
		if proxy != nil && tk != "" {
			proxy.RevokeTicket(tk)
		}
		if w != nil {
			w.Close()
		}
	}
	p.registerMediaPlayerStop(cleanup)

	var startSegment func(idx int)
	startSegment = func(idx int) {
		if idx < 0 || idx >= len(segs) {
			return
		}
		mu.Lock()
		oldPlayer := player
		oldTicket := currentTicket
		player = nil
		currentTicket = ""
		currentIdx = idx
		playerState = videoplayer.State{}
		playerErr = ""
		codecUnsupText = ""
		w := win
		mu.Unlock()

		if oldPlayer != nil {
			oldPlayer.Destroy()
		}
		if proxy != nil && oldTicket != "" {
			proxy.RevokeTicket(oldTicket)
		}

		seg := segs[idx]
		if proxy == nil {
			mu.Lock()
			playerErr = "当前未连接到电脑端，无法播放视频"
			mu.Unlock()
			return
		}
		loopbackURL, ticket, err := proxy.IssueTicket(item.ItemKey, seg.RelPath)
		if err != nil {
			mu.Lock()
			playerErr = "无法建立本地视频转发：" + err.Error()
			mu.Unlock()
			return
		}
		if w == nil {
			proxy.RevokeTicket(ticket)
			mu.Lock()
			playerErr = "当前环境不支持原生视频播放窗口"
			mu.Unlock()
			return
		}

		pl, err := videoplayer.New(w, loopbackURL, videoplayer.Options{AutoPlay: true})
		if err != nil {
			proxy.RevokeTicket(ticket)
			mu.Lock()
			if errors.Is(err, errors.ErrUnsupported) {
				playerErr = "当前环境不支持系统视频播放器（仅在 Android / iOS 真机或模拟器可用）"
			} else {
				playerErr = "无法启动视频播放器：" + err.Error()
			}
			mu.Unlock()
			return
		}
		mu.Lock()
		if closed {
			mu.Unlock()
			pl.Destroy()
			proxy.RevokeTicket(ticket)
			return
		}
		player = pl
		currentTicket = ticket
		mu.Unlock()

		pl.OnState(func(st videoplayer.State) {
			mu.Lock()
			playerState = st
			if st.Error != "" {
				codecUnsupText = formatUnsupportedCodecMessage(seg.Codec, st.Error)
			} else if st.Ready && st.Width == 0 && st.Height == 0 {
				// Android MediaPlayer drops unsupported video tracks silently (design §6 / §9.3).
				codecUnsupText = formatUnsupportedCodecMessage(seg.Codec, "")
			}
			ended := st.Ended && currentIdx+1 < len(segs)
			nextIdx := currentIdx + 1
			mu.Unlock()

			if ended {
				h.Update(func() {
					startSegment(nextIdx)
				})
			} else {
				h.Update(func() {})
			}
		})
	}

	title := firstNonEmpty(item.Title, "视频播放")
	win = h.PushWindow(title, func(c *ui.Context) {
		c.SetTheme(theme.TraceTheme())
		mu.Lock()
		pl := player
		st := playerState
		pErr := playerErr
		unsup := codecUnsupText
		idx := currentIdx
		mu.Unlock()

		ui.Column(c).Fill().Background(ui.Hex("#000000")).Children(func() {
			// Top bar.
			ui.Row(c).
				FillWidth().
				Padding(10, 12).
				Background(ui.RGBA(0, 0, 0, 0.75)).
				AlignItems(ui.Center).
				Justify(ui.SpaceBetween).
				Gap(8).
				Shrink(0).
				ClipX().
				Children(func() {
					closeBtn := ui.ButtonBase(c).Label("关闭视频播放").Padding(8, 12).MinHeight(40).Radius(999).Background(ui.RGBA(255, 255, 255, 0.14))
					closeBtn.Children(func() {
						ui.Text(c, "关闭").FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
					})
					if closeBtn.Clicked() {
						cleanup()
					}
					ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(theme.ColorWhite).SingleLine().Grow(1).Shrink(1)
				})

			// Video surface or honest error / unsupported codec message.
			ui.Box(c).Grow(1).Basis(0).FillWidth().Center().Children(func() {
				switch {
				case unsup != "":
					ui.Column(c).Padding(24).Gap(8).AlignItems(ui.Center).Children(func() {
						ui.Text(c, unsup).FontSize(15).FontWeight(600).TextColor(theme.ColorWhite).TextAlign(ui.Center)
						ui.Text(c, "电脑端不转码，可在电脑上使用 TRACE 观看。").FontSize(13).TextColor(theme.ColorTextFaint).TextAlign(ui.Center)
					})
				case pErr != "":
					ui.Text(c, pErr).FontSize(14).TextColor(theme.ColorWhite).TextAlign(ui.Center).Padding(24)
				case pl != nil:
					ui.WebView(c, pl).Fill()
				default:
					ui.Text(c, "正在准备视频…").FontSize(14).TextColor(theme.ColorWhite)
				}
			})

			// Playback controls + multi-part / multi-segment bar.
			ui.Column(c).FillWidth().Padding(10, 14, 16, 14).Gap(10).Background(ui.RGBA(0, 0, 0, 0.78)).Shrink(0).ClipX().Children(func() {
				if pl != nil && unsup == "" {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(10).Children(func() {
						playLbl := "播放"
						if st.Playing {
							playLbl = "暂停"
						}
						playBtn := ui.ButtonBase(c).Label(playLbl).Padding(8, 16).MinHeight(40).Radius(999).Background(theme.ColorAccent)
						playBtn.Children(func() {
							ui.Text(c, playLbl).FontSize(13).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						if playBtn.Clicked() {
							if st.Playing {
								pl.Pause()
							} else {
								pl.Play()
							}
						}
						timeTxt := fmt.Sprintf("%s / %s", formatTimeDur(st.Position), formatTimeDur(st.Duration))
						ui.Text(c, timeTxt).FontSize(12).TextColor(theme.ColorWhite).SingleLine()
					})
				}

				if len(segs) > 1 {
					ui.Row(c).FillWidth().Gap(6).Wrap().ClipX().Children(func() {
						for i, s := range segs {
							si := i
							bg := ui.RGBA(255, 255, 255, 0.14)
							if si == idx {
								bg = theme.ColorAccent
							}
							pb := ui.ButtonBase(c.Key(si)).Label("切换分段："+s.Label).Padding(6, 12).MinHeight(36).Radius(999).Background(bg)
							pb.Children(func() {
								ui.Text(c, s.Label).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
							})
							if pb.Clicked() && si != idx {
								startSegment(si)
							}
						}
					})
				}
			})
		})
	})

	startSegment(0)
}

func formatUnsupportedCodecMessage(codec, rawErr string) string {
	upper := strings.ToUpper(strings.TrimSpace(codec))
	switch {
	case strings.Contains(upper, "AV1") || strings.Contains(upper, "AV01"):
		return "这台手机不能播放 AV1 视频"
	case strings.Contains(upper, "HEVC") || strings.Contains(upper, "H265") || strings.Contains(upper, "H.265"):
		return "这台手机不能播放 HEVC 视频"
	case strings.Contains(upper, "FLAC"):
		return "这台手机不能播放 FLAC 音轨"
	case upper != "":
		return fmt.Sprintf("这台手机不能播放 %s 视频", upper)
	case rawErr != "":
		return "这台手机无法解码此视频（" + rawErr + "）"
	default:
		return "这台手机不能播放此视频编码"
	}
}

func formatTimeDur(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	return formatDurationSec(int(d.Seconds()))
}
