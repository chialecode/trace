package ui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/chialecode/trace/internal/service"
	"github.com/egoist/mygo/ui"
)

// douyinNoteRenderPage normalizes an online or locally cached Douyin note page for grid and viewer rendering.
type douyinNoteRenderPage struct {
	Index             int
	Kind              string // "image", "live_photo", "video", "unknown"
	IsGIF             bool
	HLSOnly           bool
	HLSOnlyNotice     string
	ImageURL          string
	FallbackImageURLs []string
	ImageRel          string
	MotionURL         string
	MotionRel         string
	VideoURL          string
	VideoRel          string
	Width             int
	Height            int
	DurationSec       int
	UnknownReason     string
}

// ============================================================================
// Douyin Note BGM Bar & Companion Viewer Inline Playback (docs/ui.md §3.4, §3.5)
// ============================================================================

func playerLoaded(ctrl *player.Controller) bool {
	return ctrl != nil && ctrl.Snapshot(1, 1).Open
}

func playerPlaying(ctrl *player.Controller) bool {
	if ctrl == nil {
		return false
	}
	snap := ctrl.Snapshot(1, 1)
	return snap.Open && snap.Playing
}

func (a *App) pauseBGM() {
	if playerPlaying(a.bgmPlayer) {
		a.bgmPlayer.Pause()
	}
}

func (a *App) stopBGM() {
	a.bgmOpenSeq++
	if a.bgmPlayer != nil {
		_ = a.bgmPlayer.Close()
	}
	a.BGMActiveKey = ""
	a.BGMTitle = ""
	a.BGMAuthor = ""
	a.BGMPath = ""
	a.BGMDurationSec = 0
	a.BGMLoading = false
	a.BGMError = ""
	a.BGMSeekNormalized = 0
}

func (a *App) toggleBGM(key, title, author string, durationSec int, localItemKey, localRelFile, onlinePlayURL string) {
	if key == "" {
		return
	}
	if a.BGMActiveKey == key && playerLoaded(a.bgmPlayer) {
		if !playerPlaying(a.bgmPlayer) {
			if a.ImageViewer.Open && a.ImageViewer.InlineActive {
				a.stopViewerInlinePlayback()
			}
			if a.PlayerOverlay.Open && playerPlaying(a.player) {
				a.player.Pause()
			}
			a.bgmPlayer.Play()
		} else {
			a.bgmPlayer.Pause()
		}
		return
	}

	a.stopBGM() // already increments a.bgmOpenSeq
	if a.ImageViewer.Open && a.ImageViewer.InlineActive {
		a.stopViewerInlinePlayback()
	}
	if a.PlayerOverlay.Open && playerPlaying(a.player) {
		a.player.Pause()
	}

	seq := a.bgmOpenSeq
	a.BGMActiveKey = key
	a.BGMTitle = title
	a.BGMAuthor = author
	a.BGMDurationSec = durationSec
	a.BGMLoading = true
	a.BGMError = ""
	a.BGMSeekNormalized = 0

	backend := a.backend
	isCurrent := func() bool {
		return a.bgmOpenSeq == seq && a.BGMActiveKey == key
	}

	type bgmOpened struct {
		path string
		dec  player.MediaDecoder
	}
	requestGuardedCleanup(a, fmt.Sprintf("bgm-open-%d", seq), isCurrent, func(ctx context.Context) (bgmOpened, error) {
		var mediaPath string
		if localItemKey != "" && localRelFile != "" {
			p, err := backend.MediaPath(localItemKey, localRelFile)
			if err != nil {
				return bgmOpened{}, fmt.Errorf("配乐文件缺失或不可访问")
			}
			mediaPath = p
		} else if strings.TrimSpace(onlinePlayURL) != "" {
			ext := strings.ToLower(filepath.Ext(strings.Split(onlinePlayURL, "?")[0]))
			if ext == "" || len(ext) > 6 {
				ext = ".mp3"
			}
			p, err := backend.StagePreviewMedia(ctx, "douyin", onlinePlayURL, ext)
			if err != nil {
				return bgmOpened{}, err
			}
			mediaPath = p
		} else {
			return bgmOpened{}, fmt.Errorf("当前作品没有可播放的配乐")
		}
		if ctx.Err() != nil {
			return bgmOpened{}, ctx.Err()
		}
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return bgmOpened{}, err
		}
		if ctx.Err() != nil {
			_ = dec.Close()
			return bgmOpened{}, ctx.Err()
		}
		return bgmOpened{path: mediaPath, dec: dec}, nil
	}, func(res bgmOpened) {
		a.BGMLoading = false
		if a.bgmPlayer == nil {
			if res.dec != nil {
				_ = res.dec.Close()
			}
			return
		}
		a.BGMPath = res.path
		if err := a.bgmPlayer.LoadDecoder(res.path, res.dec); err != nil {
			a.BGMError = "无法播放配乐：" + userErrorText(err)
			return
		}
		a.bgmPlayer.SetVolume(a.PlayerOverlay.Volume)
		a.bgmPlayer.Play()
	}, func(err error) {
		a.BGMLoading = false
		a.BGMError = userErrorText(err)
	}, func(res bgmOpened) {
		if res.dec != nil {
			_ = res.dec.Close()
		}
	})
}

func formatMusicLine(title, author string, original bool) string {
	t := strings.TrimSpace(title)
	au := strings.TrimSpace(author)
	if t == "" && original {
		t = "原声"
	}
	if t == "" {
		t = "配乐"
	}
	if au != "" && !strings.Contains(t, au) {
		return fmt.Sprintf("♪ %s · %s", t, au)
	}
	return "♪ " + t
}

func (a *App) renderBGMBar(c *ui.Context, key, title, author string, original bool, durationSec int, localItemKey, localRelFile, onlinePlayURL, unavailableReason string) {
	musicLabel := formatMusicLine(title, author, original)
	active := a.BGMActiveKey == key
	loaded := active && playerLoaded(a.bgmPlayer)
	playing := loaded && playerPlaying(a.bgmPlayer)
	hasSource := (localItemKey != "" && localRelFile != "") || strings.TrimSpace(onlinePlayURL) != ""

	var posSec, durSec float64
	durSec = float64(max(0, durationSec))
	if loaded {
		snap := a.bgmPlayer.Snapshot(1, 1)
		posSec = snap.PositionSec
		if snap.DurationSec > 0 {
			durSec = snap.DurationSec
		}
		if durSec > 0 {
			a.BGMSeekNormalized = max(0.0, min(1.0, posSec/durSec))
		}
	}

	ui.Column(c).
		Label("配乐栏："+key).
		FillWidth().
		Padding(10, 12).
		Radius(12).
		Background(ColorBgMain).
		Border(1, ColorBorder).
		Gap(6).
		ClipX().
		Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).ClipX().Children(func() {
				btnIcon := "play"
				if playing {
					btnIcon = "pause"
				}
				playBtn := ui.ButtonBase(c).
					Label("配乐播放暂停："+key).
					Size(32, 32).
					Radius(16).
					Shrink(0).
					Justify(ui.Center).
					AlignItems(ui.Center).
					Disabled(!hasSource || (active && a.BGMLoading))
				if hasSource {
					playBtn.Background(ColorAccent)
				} else {
					playBtn.Background(ColorBorder)
				}
				playBtn.Children(func() {
					if active && a.BGMLoading {
						ui.Spinner(c).Size(14, 14)
					} else {
						DrawIcon(c, btnIcon, 15, ColorWhite, 2.0)
					}
				})
				if playBtn.Clicked() && hasSource {
					a.toggleBGM(key, title, author, durationSec, localItemKey, localRelFile, onlinePlayURL)
				}

				ui.Column(c).Grow(1).Gap(4).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).ClipX().Children(func() {
						ui.Text(c, musicLabel).FontSize(13).FontWeight(600).TextColor(ColorTextMain).SingleLine().Grow(1).Shrink(1)
						if hasSource {
							timeText := fmt.Sprintf("%s / %s", FormatDuration(int(posSec)), FormatDuration(int(durSec)))
							ui.Text(c, timeText).FontSize(12).TextColor(ColorTextMuted).SingleLine().Shrink(0)
						}
					})
					if hasSource {
						seekVal := a.BGMSeekNormalized
						if !active {
							seekVal = 0
						}
						if ui.Slider(c, &seekVal, 0.0, 1.0).Label("配乐进度条：" + key).FillWidth().Disabled(!loaded || durSec <= 0).Changed() {
							a.BGMSeekNormalized = seekVal
							if loaded && durSec > 0 {
								_ = a.bgmPlayer.Seek(seekVal * durSec)
							}
						}
					} else {
						reason := firstNonEmptyStrUI(unavailableReason, "该作品配乐不可用")
						ui.Text(c, reason).FontSize(12).TextColor(ColorTextMuted).SingleLine()
					}
				})
			})
			if active && a.BGMError != "" {
				ui.Text(c, a.BGMError).FontSize(12).TextColor(ColorError).MaxLines(2)
			}
		})
}

func (a *App) viewerInlinePageIsLoopMuted(idx int) bool {
	return idx >= 0 && idx < len(a.ImageViewer.InlineLoopMuted) && a.ImageViewer.InlineLoopMuted[idx]
}

// stopViewerInlinePlayback stops any inline live-photo motion or video page playing inside ImageViewer.
// By design (docs/ui.md §3.4), stopping inline playback does NOT auto-resume the note's BGM bar.
func (a *App) stopViewerInlinePlayback() {
	a.viewerOpenSeq++
	if a.viewerPlayer != nil {
		_ = a.viewerPlayer.Close()
	}
	a.ImageViewer.InlineActive = false
	a.ImageViewer.InlinePageIndex = -1
	a.ImageViewer.InlineLoading = false
	a.ImageViewer.InlineError = ""
	a.ImageViewer.InlineMediaPath = ""
	a.ImageViewer.InlineLoopPaused = false
}

// startViewerInlinePage starts playing a live-photo motion clip or mixed-note video page inline inside ImageViewer.
// Starting inline playback immediately pauses any active note BGM bar.
func (a *App) startViewerInlinePage(pageIndex int) {
	if !a.ImageViewer.Open || pageIndex < 0 {
		return
	}
	var localPath, remoteURL string
	if pageIndex < len(a.ImageViewer.InlineMediaPaths) {
		localPath = a.ImageViewer.InlineMediaPaths[pageIndex]
	}
	if pageIndex < len(a.ImageViewer.InlineMediaURLs) {
		remoteURL = a.ImageViewer.InlineMediaURLs[pageIndex]
	}
	if localPath == "" && remoteURL == "" {
		return
	}

	// Pause note BGM immediately; never auto-resume when inline clip finishes or closes.
	a.pauseBGM()
	a.stopViewerInlinePlayback() // already increments a.viewerOpenSeq

	seq := a.viewerOpenSeq
	a.ImageViewer.InlineActive = true
	a.ImageViewer.InlinePageIndex = pageIndex
	a.ImageViewer.InlineLoading = true
	a.ImageViewer.InlineError = ""

	backend := a.backend
	inlineSite := a.ImageViewer.InlineSite
	if inlineSite == "" {
		inlineSite = "douyin"
	}
	isCurrent := func() bool {
		return a.ImageViewer.Open && a.viewerOpenSeq == seq && a.ImageViewer.Index == pageIndex && a.ImageViewer.InlineActive
	}

	type inlineOpened struct {
		path string
		dec  player.MediaDecoder
	}
	requestGuardedCleanup(a, fmt.Sprintf("viewer-inline-%d", seq), isCurrent, func(ctx context.Context) (inlineOpened, error) {
		mediaPath := localPath
		if mediaPath == "" {
			p, err := backend.StagePreviewMedia(ctx, inlineSite, remoteURL, ".mp4")
			if err != nil {
				return inlineOpened{}, err
			}
			mediaPath = p
		}
		if ctx.Err() != nil {
			return inlineOpened{}, ctx.Err()
		}
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return inlineOpened{}, err
		}
		if ctx.Err() != nil {
			_ = dec.Close()
			return inlineOpened{}, ctx.Err()
		}
		return inlineOpened{path: mediaPath, dec: dec}, nil
	}, func(res inlineOpened) {
		a.ImageViewer.InlineLoading = false
		if a.viewerPlayer == nil {
			if res.dec != nil {
				_ = res.dec.Close()
			}
			return
		}
		a.ImageViewer.InlineMediaPath = res.path
		if err := a.viewerPlayer.LoadDecoder(res.path, res.dec); err != nil {
			a.ImageViewer.InlineError = "无法播放此页媒体：" + userErrorText(err)
			return
		}
		a.viewerPlayer.SetVolume(a.PlayerOverlay.Volume)
		if a.viewerInlinePageIsLoopMuted(pageIndex) {
			a.viewerPlayer.SetMuted(true)
		} else if a.backend != nil {
			a.viewerPlayer.SetMuted(a.backend.GetUIState().Player.Muted)
		}
		a.ImageViewer.InlineLoopPaused = false
		a.viewerPlayer.Play()
	}, func(err error) {
		a.ImageViewer.InlineLoading = false
		a.ImageViewer.InlineError = userErrorText(err)
	}, func(res inlineOpened) {
		if res.dec != nil {
			_ = res.dec.Close()
		}
	})
}

func (a *App) toggleViewerInlinePlay() {
	if !a.ImageViewer.Open || !a.ImageViewer.InlineActive || !playerLoaded(a.viewerPlayer) {
		return
	}
	wasPlaying := playerPlaying(a.viewerPlayer)
	if !wasPlaying {
		a.pauseBGM()
	}
	a.ImageViewer.InlineLoopPaused = wasPlaying
	a.viewerPlayer.TogglePlay()
}

func (a *App) openDouyinNoteViewer(itemKey string, pages []douyinNoteRenderPage, index int) {
	a.openNotePagesViewer(itemKey, "douyin", pages, index)
}

func (a *App) openNotePagesViewer(itemKey, siteID string, pages []douyinNoteRenderPage, index int) {
	if len(pages) == 0 {
		return
	}
	urls := make([]string, len(pages))
	fallbackURLs := make([][]string, len(pages))
	localStills := make([]string, len(pages))
	rels := make([]string, len(pages))
	kinds := make([]string, len(pages))
	inlinePaths := make([]string, len(pages))
	inlineURLs := make([]string, len(pages))
	inlineDurs := make([]int, len(pages))
	inlineLoopMuted := make([]bool, len(pages))

	offline := a.CurrentRoute == RouteLibrary || a.CurrentRoute == RouteLibraryItem
	for i, pg := range pages {
		if !offline {
			urls[i] = pg.ImageURL
			if len(pg.FallbackImageURLs) > 0 {
				fallbackURLs[i] = append([]string(nil), pg.FallbackImageURLs...)
			}
		}
		rels[i] = pg.ImageRel
		if itemKey != "" && pg.ImageRel != "" && a.backend != nil {
			if p, err := a.backend.MediaPath(itemKey, pg.ImageRel); err == nil {
				localStills[i] = p
			}
		}
		normKind := pg.Kind
		if normKind == "motion" {
			normKind = "live_photo"
		}
		kinds[i] = normKind
		inlineDurs[i] = pg.DurationSec
		inlineLoopMuted[i] = pg.IsGIF && normKind == "video"
		switch normKind {
		case "live_photo":
			if itemKey != "" && pg.MotionRel != "" && a.backend != nil {
				if p, err := a.backend.MediaPath(itemKey, pg.MotionRel); err == nil {
					inlinePaths[i] = p
				}
			}
			if !offline {
				inlineURLs[i] = pg.MotionURL
			}
		case "video":
			if itemKey != "" && pg.VideoRel != "" && a.backend != nil {
				if p, err := a.backend.MediaPath(itemKey, pg.VideoRel); err == nil {
					inlinePaths[i] = p
				}
			}
			if !offline {
				inlineURLs[i] = pg.VideoURL
			}
		}
	}

	a.OpenImageViewerWithFallbacks(urls, fallbackURLs, localStills, index)
	a.ImageViewer.ItemKey = itemKey
	a.ImageViewer.RelPaths = rels
	a.ImageViewer.InlinePageKinds = kinds
	a.ImageViewer.InlineMediaPaths = inlinePaths
	a.ImageViewer.InlineMediaURLs = inlineURLs
	a.ImageViewer.InlineDurations = inlineDurs
	a.ImageViewer.InlineLoopMuted = inlineLoopMuted
	a.ImageViewer.InlineLoopPaused = false
	a.ImageViewer.InlineSite = firstNonEmptyStrUI(siteID, "douyin")

	idx := a.ImageViewer.Index
	if idx >= 0 && idx < len(kinds) && viewerPageAutoplays(kinds[idx]) {
		a.startViewerInlinePage(idx)
	}
}

// OpenDouyinNoteViewer opens the note pages of an online or cached Douyin note.
func (a *App) OpenDouyinNoteViewer(work *service.DouyinWorkView, cached *library.ItemMetadata, index int) {
	pages := buildDouyinNoteRenderPages(work, cached)
	itemKey := ""
	if cached != nil {
		itemKey = cached.ItemKey
	} else if work != nil {
		itemKey = work.CacheKey
	}
	a.openDouyinNoteViewer(itemKey, pages, index)
}

// viewerPageAutoplays reports whether a viewer page plays as soon as it is
// shown: a video page, and a live photo's motion clip, which plays once and
// stays on its last frame; the play button replays it.
func viewerPageAutoplays(kind string) bool {
	return kind == "video" || kind == "live_photo"
}

func buildDouyinNoteRenderPages(work *service.DouyinWorkView, cached *library.ItemMetadata) []douyinNoteRenderPage {
	var out []douyinNoteRenderPage
	if cached != nil && len(cached.NotePages) > 0 {
		var onlinePages []service.DouyinMediaPageDTO
		if work != nil {
			onlinePages = work.Pages
		}
		for i, rec := range cached.NotePages {
			kind := rec.Kind
			if kind == "motion" || (kind == "image" && rec.Motion != "") {
				kind = "live_photo"
			}
			pg := douyinNoteRenderPage{
				Index:       rec.Index,
				Kind:        kind,
				IsGIF:       rec.GIF,
				ImageRel:    firstNonEmptyStrUI(rec.Image, rec.Cover),
				MotionRel:   rec.Motion,
				VideoRel:    rec.Video,
				Width:       rec.Width,
				Height:      rec.Height,
				DurationSec: rec.Duration,
			}
			if rec.UnknownKind != "" {
				pg.UnknownReason = "暂不支持的页面类型：" + rec.UnknownKind
			}
			if i < len(onlinePages) {
				op := onlinePages[i]
				pg.ImageURL = op.ImageURL
				pg.MotionURL = op.MotionURL
				pg.VideoURL = op.VideoURL
				if pg.Width <= 0 {
					pg.Width = op.Width
				}
				if pg.Height <= 0 {
					pg.Height = op.Height
				}
				if pg.DurationSec <= 0 {
					pg.DurationSec = op.DurationSec
				}
				if pg.UnknownReason == "" {
					pg.UnknownReason = op.UnknownReason
				}
			}
			out = append(out, pg)
		}
		return out
	}

	if work != nil && len(work.Pages) > 0 {
		for _, op := range work.Pages {
			kind := op.Kind
			if kind == "motion" {
				kind = "live_photo"
			}
			out = append(out, douyinNoteRenderPage{
				Index:         op.Index,
				Kind:          kind,
				ImageURL:      op.ImageURL,
				MotionURL:     op.MotionURL,
				VideoURL:      op.VideoURL,
				Width:         op.Width,
				Height:        op.Height,
				DurationSec:   op.DurationSec,
				UnknownReason: op.UnknownReason,
			})
		}
		return out
	}

	if cached != nil {
		idx := 0
		for _, m := range cached.Media {
			if m.Role != "image" {
				continue
			}
			idx++
			kind := "image"
			if m.Motion != "" {
				kind = "live_photo"
			}
			out = append(out, douyinNoteRenderPage{
				Index:     idx,
				Kind:      kind,
				ImageRel:  m.Path,
				MotionRel: m.Motion,
				Width:     m.Width,
				Height:    m.Height,
			})
		}
	}
	return out
}

func (a *App) renderDouyinNotePagesGrid(c *ui.Context, itemKey string, pages []douyinNoteRenderPage) {
	a.renderNotePagesGrid(c, itemKey, "douyin", pages, false)
}

func (a *App) renderNotePagesGrid(c *ui.Context, itemKey, siteID string, pages []douyinNoteRenderPage, showAll bool) {
	n := len(pages)
	if n == 0 {
		return
	}
	pageBitmap := func(pg douyinNoteRenderPage) *ui.Bitmap {
		if bmp := a.lookupLocalBitmap(itemKey, pg.ImageRel); bmp != nil {
			return bmp
		}
		return a.lookupRemoteBitmapWithFallbacks(pg.ImageURL, pg.FallbackImageURLs)
	}
	renderCell := func(bmp *ui.Bitmap, pg douyinNoteRenderPage, pageNum int) {
		ui.Box(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ColorBgSecondary).Children(func() {
			if bmp != nil {
				ui.Image(c, bmp).Absolute().Left(0).Top(0).Right(0).Bottom(0).Fit(ui.Cover)
			}
		})
		switch pg.Kind {
		case "live_photo":
			ui.Box(c).Label(fmt.Sprintf("实况标记 %d", pageNum)).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.58)).Children(func() {
				ui.Text(c, "实况").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
			})
		case "video":
			if pg.IsGIF {
				ui.Box(c).Label(fmt.Sprintf("动图标记 %d", pageNum)).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.58)).Children(func() {
					ui.Text(c, "GIF").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			} else {
				ui.Row(c).Label(fmt.Sprintf("视频页标记 %d", pageNum)).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(38, 38).Radius(19).Background(ui.RGBA(0, 0, 0, 0.58)).Center().Children(func() {
						DrawIcon(c, "play", 18, ColorWhite, 2.0)
					})
				})
				if pg.DurationSec > 0 {
					ui.Box(c).Absolute().Bottom(6).Right(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
						ui.Text(c, FormatDuration(pg.DurationSec)).FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
				}
			}
			if pg.HLSOnly {
				ui.Box(c).Label(fmt.Sprintf("分段流标记 %d", pageNum)).Absolute().Bottom(6).Left(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.72)).Children(func() {
					ui.Text(c, "分段流").FontSize(10).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			}
		case "unknown":
			ui.Box(c).Absolute().Bottom(6).Left(6).Right(6).Padding(2, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.65)).Children(func() {
				ui.Text(c, firstNonEmptyStrUI(pg.UnknownReason, "暂不支持此页")).FontSize(11).TextColor(ColorWhite).SingleLine()
			})
		default:
			if pg.IsGIF {
				ui.Box(c).Label(fmt.Sprintf("动图标记 %d", pageNum)).Absolute().Left(6).Top(6).Padding(1, 6).Radius(4).Background(ui.RGBA(0, 0, 0, 0.58)).Children(func() {
					ui.Text(c, "GIF").FontSize(11).FontWeight(600).TextColor(ColorWhite).SingleLine()
				})
			}
		}
	}

	if n == 1 {
		pg := pages[0]
		bmp := pageBitmap(pg)
		ratio := imageRatio(PostImage{Width: pg.Width, Height: pg.Height}, bmp)
		cell := ui.ButtonBase(c).Label("查看图文页 1").Radius(8).Clip().Shrink(0)
		if ratio < 1 {
			cell.Size(singleImageHeight*ratio, singleImageHeight)
		} else {
			cell.FillWidth().MaxWidth(singleImageMaxWidth).AspectRatio(ratio)
		}
		cell.Children(func() { renderCell(bmp, pg, 1) })
		if cell.Clicked() {
			a.openNotePagesViewer(itemKey, siteID, pages, 0)
		}
		return
	}

	cols, maxW := 3, float32(gridMaxWidth3)
	if n == 2 || n == 4 {
		cols, maxW = 2, float32(gridMaxWidth2)
	}
	shown := min(n, 9)
	if showAll {
		shown = n
	}
	extra := n - shown
	ui.Grid(c).Columns(cols).Gap(4).FillWidth().MaxWidth(maxW).Children(func() {
		for idx := range shown {
			pageIdx := idx
			pg := pages[pageIdx]
			cell := ui.ButtonBase(c).Label(fmt.Sprintf("查看图文页 %d", pageIdx+1)).AspectRatio(1).Radius(6).Clip()
			cell.Children(func() {
				renderCell(pageBitmap(pg), pg, pageIdx+1)
				if extra > 0 && pageIdx == shown-1 {
					ui.Row(c).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ui.RGBA(0, 0, 0, 0.55)).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
						ui.Text(c, fmt.Sprintf("+%d", extra)).FontSize(18).FontWeight(600).TextColor(ColorWhite).SingleLine()
					})
				}
			})
			if cell.Clicked() {
				a.openNotePagesViewer(itemKey, siteID, pages, pageIdx)
			}
		}
	})
}
