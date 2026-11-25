package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/player"
	"github.com/egoist/mygo/ui"
	xdraw "golang.org/x/image/draw"
)

// thumbMaxEdge bounds decoded thumbnails: a single post image or a card at
// most 480 px wide on a 200% display. gridThumbEdge is a grid cell's;
// avatarThumbEdge bounds local avatars and recent-parse sidebar thumbs.
const (
	thumbMaxEdge    = 960
	gridThumbEdge   = 480
	avatarThumbEdge = 256
)

// lookupLocalBitmap returns a registered local image as a thumbnail. The
// file is read, decoded and scaled down off the UI thread; until it is ready
// (or when it cannot be read) the caller draws its placeholder. Scrolling
// therefore never waits for a large original to decode.
func (a *App) lookupLocalBitmap(itemKey, relPath string) *ui.Bitmap {
	return a.lookupLocalBitmapEdge(itemKey, relPath, thumbMaxEdge)
}

// lookupLocalBitmapEdge is lookupLocalBitmap decoded to fit maxEdge pixels:
// the size it is drawn at, not the original's (DEC-057).
func (a *App) lookupLocalBitmapEdge(itemKey, relPath string, maxEdge int) *ui.Bitmap {
	if a.backend == nil || itemKey == "" || relPath == "" {
		return nil
	}
	key := itemKey + "::" + relPath
	if maxEdge != thumbMaxEdge {
		key += "@" + strconv.Itoa(maxEdge)
	}
	a.mu.Lock()
	if bmp, ok := a.images.get(key); ok {
		a.mu.Unlock()
		return bmp
	}
	a.images.reserve(key)
	a.mu.Unlock()
	backend := a.backend
	requestGuarded(a, "thumb:"+key, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		fullPath, err := backend.MediaPath(itemKey, relPath)
		if err != nil || fullPath == "" {
			return nil, nil
		}
		return loadThumbBitmap(fullPath, maxEdge), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		if !a.closed {
			a.images.fill(key, bmp)
		}
		a.mu.Unlock()
	}, func(error) {})
	a.mu.Lock()
	bmp, _ := a.images.get(key)
	a.mu.Unlock()
	return bmp
}

// lookupHistoryThumbBitmap loads and caches a local parse-history thumbnail from
// <DataDir>/thumbs/. File decoding and resizing run off the UI thread via
// App.imageQueue when dispatch is bound; misses are cached so steady frames
// never re-hit disk until new thumbnails arrive or library/account events fire.
func (a *App) lookupHistoryThumbBitmap(thumbName string) *ui.Bitmap {
	if a.backend == nil || thumbName == "" {
		return nil
	}
	cacheKey := "hist-thumb::" + thumbName
	a.mu.Lock()
	if bmp, ok := a.images.get(cacheKey); ok {
		a.mu.Unlock()
		return bmp
	}
	a.mu.Unlock()

	fullPath, err := a.backend.HistoryThumbPath(thumbName)
	if err != nil || fullPath == "" {
		a.mu.Lock()
		if !a.closed {
			a.images.put(cacheKey, nil)
		}
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.images.reserve(cacheKey)
	a.mu.Unlock()

	requestGuarded(a, cacheKey, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		return loadThumbBitmap(fullPath, avatarThumbEdge), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		if !a.closed {
			a.images.fill(cacheKey, bmp)
		}
		a.mu.Unlock()
	}, func(error) {})
	a.mu.Lock()
	bmp, _ := a.images.get(cacheKey)
	a.mu.Unlock()
	return bmp
}

// lookupAccountAvatarBitmap loads and caches the local avatar of a subscribed
// account. Cache hits and misses never re-call AccountAvatarPath on steady
// frames, and existing local files are decoded and resized off the UI thread
// via App.imageQueue (setting pendingLocalAvatar while in-flight so
// renderAccountAvatar never falls back to a remote FetchImage for a local file).
func (a *App) lookupAccountAvatarBitmap(siteID, uid string) *ui.Bitmap {
	a.pendingLocalAvatar = false
	if a.backend == nil || uid == "" {
		return nil
	}
	normSite := normalizeSiteIDUI(siteID)
	cacheKey := "avatar::" + normSite + "::" + uid
	a.mu.Lock()
	if bmp, ok := a.images.get(cacheKey); ok {
		if bmp == nil && a.pending[cacheKey] {
			a.pendingLocalAvatar = true
		}
		a.mu.Unlock()
		return bmp
	}
	a.mu.Unlock()

	path := a.backend.AccountAvatarPath(normSite, uid)
	if path == "" {
		a.mu.Lock()
		if !a.closed {
			a.images.put(cacheKey, nil)
		}
		a.mu.Unlock()
		return nil
	}
	a.mu.Lock()
	a.images.reserve(cacheKey)
	a.mu.Unlock()

	requestGuarded(a, cacheKey, nil, func(ctx context.Context) (*ui.Bitmap, error) {
		select {
		case a.imageQueue <- struct{}{}:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		defer func() { <-a.imageQueue }()
		return loadThumbBitmap(path, avatarThumbEdge), nil
	}, func(bmp *ui.Bitmap) {
		a.mu.Lock()
		if !a.closed {
			a.images.fill(cacheKey, bmp)
		}
		a.mu.Unlock()
	}, func(error) {})
	a.mu.Lock()
	bmp, _ := a.images.get(cacheKey)
	if bmp == nil && a.pending[cacheKey] {
		a.pendingLocalAvatar = true
	}
	a.mu.Unlock()
	return bmp
}

// OpenItemImageViewer resolves registered relative media paths via Backend.MediaPath (INV-08) and opens the image viewer.
// A live photo's cached motion clip plays inside the viewer (DEC-046 ①).
func (a *App) OpenItemImageViewer(itemKey string, urls []string, relPaths []string, index int) {
	a.openPostImageViewer(itemKey, "", urls, relPaths, nil, index)
}

// openPostImageViewer opens a post's images. liveURLs holds each image's
// online motion clip (实况) when the post is not cached; a cached clip is
// looked up in the library item and preferred. Live photos get the viewer's
// play button and play in place, cached or not (DEC-046 ①).
func (a *App) openPostImageViewer(itemKey, siteID string, urls, relPaths, liveURLs []string, index int) {
	n := maxInt(len(urls), len(relPaths))
	if n == 0 {
		return
	}
	resolved := make([]string, len(relPaths))
	for i, rel := range relPaths {
		if rel == "" {
			continue
		}
		if a.backend != nil && itemKey != "" {
			if p, err := a.backend.MediaPath(itemKey, rel); err == nil {
				resolved[i] = p
			}
		}
	}
	kinds := make([]string, n)
	inlinePaths := make([]string, n)
	inlineURLs := make([]string, n)
	durations := make([]int, n)
	hasLive := false
	if a.backend != nil && itemKey != "" {
		if item, ok, err := a.backend.GetLibraryItem(itemKey); err == nil && ok {
			if siteID == "" {
				siteID = item.Site
			}
			for i := 0; i < n && i < len(relPaths); i++ {
				rel := relPaths[i]
				if rel == "" {
					continue
				}
				for _, m := range item.Media {
					if m.Path != rel || m.Motion == "" {
						continue
					}
					if p, err := a.backend.MediaPath(itemKey, m.Motion); err == nil {
						inlinePaths[i] = p
						hasLive = true
					}
					for _, clip := range item.Media {
						if clip.Path == m.Motion {
							durations[i] = clip.Duration
						}
					}
				}
			}
		}
	}
	for i := 0; i < n && i < len(liveURLs); i++ {
		if liveURLs[i] != "" {
			inlineURLs[i] = liveURLs[i]
			hasLive = true
		}
	}
	a.OpenImageViewer(urls, resolved, index)
	if !a.ImageViewer.Open {
		return
	}
	a.ImageViewer.ItemKey = itemKey
	a.ImageViewer.RelPaths = relPaths
	if hasLive {
		for i := range kinds {
			kinds[i] = "image"
			if inlinePaths[i] != "" || inlineURLs[i] != "" {
				kinds[i] = "live_photo"
			}
		}
		a.ImageViewer.InlinePageKinds = kinds
		a.ImageViewer.InlineMediaPaths = inlinePaths
		a.ImageViewer.InlineMediaURLs = inlineURLs
		a.ImageViewer.InlineDurations = durations
		a.ImageViewer.InlineSite = normalizeSiteIDUI(siteID)
		if idx := a.ImageViewer.Index; idx >= 0 && idx < len(kinds) && viewerPageAutoplays(kinds[idx]) {
			a.startViewerInlinePage(idx)
		}
	}
}

// OpenImageViewer opens the in-app image viewer overlay, decoding local files or fetched image bytes.
func (a *App) OpenImageViewer(urls []string, localPaths []string, index int) {
	a.OpenImageViewerWithFallbacks(urls, nil, localPaths, index)
}

// OpenImageViewerWithFallbacks opens the in-app image viewer overlay with optional per-image fallback URLs
// (e.g. falling back from Weibo /oslarge/ to the original largest URL on 404, HTML, or corrupt image).
func (a *App) OpenImageViewerWithFallbacks(urls []string, fallbackURLs [][]string, localPaths []string, index int) {
	if len(urls) == 0 && len(localPaths) == 0 {
		return
	}
	a.closeImageViewer()
	revision := a.imageViewerRevision
	ctx, cancel := context.WithCancel(a.ctx)
	a.imageViewerCancel = cancel
	sanitizeViewerURL := func(u string) string {
		u = strings.TrimSpace(u)
		if strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "http://127.0.0.1") && !strings.HasPrefix(u, "http://localhost") {
			u = "https://" + strings.TrimPrefix(u, "http://")
		}
		return u
	}
	// Ensure URLs use https and never leak external Referer.
	cleanURLs := make([]string, len(urls))
	localPaths = append([]string(nil), localPaths...)
	for i, u := range urls {
		cleanURLs[i] = sanitizeViewerURL(u)
	}
	var cleanFallbacks [][]string
	if len(fallbackURLs) > 0 {
		cleanFallbacks = make([][]string, len(fallbackURLs))
		for i, list := range fallbackURLs {
			if len(list) == 0 {
				continue
			}
			cands := make([]string, 0, len(list))
			for _, u := range list {
				if cu := sanitizeViewerURL(u); cu != "" {
					cands = append(cands, cu)
				}
			}
			cleanFallbacks[i] = cands
		}
	}
	if index < 0 {
		index = 0
	}
	total := maxInt(len(cleanURLs), len(localPaths))
	index = min(index, total-1)
	a.ImageViewer = ImageViewerState{Open: true, URLs: cleanURLs, FallbackURLs: cleanFallbacks, LocalPaths: localPaths, Index: index, Zoom: 1, Fit: true, InlinePageIndex: -1}
	backend := a.backend
	requestGuarded(a, fmt.Sprintf("image-%d", revision), func() bool {
		return a.ImageViewer.Open && a.imageViewerRevision == revision && backend.HasAnyLogin()
	}, func(context.Context) (ImageViewerState, error) {
		defer cancel()
		bitmaps := make([]*ui.Bitmap, total)
		errors := make([]string, total)
		for i := 0; i < total; i++ {
			if ctx.Err() != nil {
				break
			}
			if i < len(localPaths) && localPaths[i] != "" {
				bitmaps[i] = loadBitmapFromFile(localPaths[i])
				if bitmaps[i] == nil {
					errors[i] = "本机图片无法读取或格式不支持"
				}
			} else {
				var cands []string
				seen := make(map[string]bool)
				addCand := func(u string) {
					if u != "" && !seen[u] {
						seen[u] = true
						cands = append(cands, u)
					}
				}
				if i < len(cleanURLs) {
					addCand(cleanURLs[i])
				}
				if i < len(cleanFallbacks) {
					for _, fb := range cleanFallbacks[i] {
						addCand(fb)
					}
				}
				if len(cands) == 0 {
					errors[i] = "图片未登记或路径不可访问"
					continue
				}
				lastErr := ""
				for _, candURL := range cands {
					if ctx.Err() != nil {
						break
					}
					raw, err := backend.FetchImage(ctx, candURL)
					if err != nil {
						lastErr = userErrorText(err)
						continue
					}
					if bmp := decodeImageBytesToBitmap(raw); bmp != nil {
						bitmaps[i] = bmp
						lastErr = ""
						break
					}
					lastErr = "图片格式不支持或文件损坏"
				}
				if bitmaps[i] == nil && lastErr != "" {
					errors[i] = lastErr
				}
			}
		}
		return ImageViewerState{
			Open:         true,
			URLs:         cleanURLs,
			FallbackURLs: cleanFallbacks,
			LocalPaths:   localPaths,
			Index:        index,
			Zoom:         1,
			Fit:          true,
			Bitmaps:      bitmaps,
			Errors:       errors,
		}, nil
	}, func(state ImageViewerState) {
		a.ImageViewer.Bitmaps = state.Bitmaps
		a.ImageViewer.Errors = state.Errors
		a.imageViewerCancel = nil
	}, nil)
}

func loadBitmapFromFile(path string) *ui.Bitmap {
	raw := readBoundedImageFile(path)
	if len(raw) == 0 {
		return nil
	}
	return decodeImageBytesToBitmap(raw)
}

func decodeImageBytesToBitmap(raw []byte) *ui.Bitmap {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxImageDecodePixels {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil || img == nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if w > viewerMaxBitmapEdge {
		h, w = max(1, int(int64(h)*viewerMaxBitmapEdge/int64(w))), viewerMaxBitmapEdge
	}
	if h > viewerMaxBitmapEdge {
		w, h = max(1, int(int64(w)*viewerMaxBitmapEdge/int64(h))), viewerMaxBitmapEdge
	}
	if int64(w)*int64(h) > viewerMaxBitmapPixels {
		ratio := math.Sqrt(float64(viewerMaxBitmapPixels) / float64(int64(w)*int64(h)))
		w = max(1, int(float64(w)*ratio))
		h = max(1, int(float64(h)*ratio))
		for int64(w)*int64(h) > viewerMaxBitmapPixels {
			if w >= h && w > 1 {
				w--
			} else if h > 1 {
				h--
			} else {
				break
			}
		}
	}
	if w == b.Dx() && h == b.Dy() {
		if rgba, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) {
			return ui.NewBitmap(rgba)
		}
		rgba := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(rgba, rgba.Bounds(), img, b.Min, draw.Src)
		return ui.NewBitmap(rgba)
	}
	rgba := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.ApproxBiLinear.Scale(rgba, rgba.Bounds(), img, b, xdraw.Src, nil)
	return ui.NewBitmap(rgba)
}

// OpenPlayerOverlay opens the in-app media player overlay for an item, loading its registered MP4, danmaku XML, and subtitles via Backend.MediaPath.
func (a *App) OpenPlayerOverlay(item library.ItemMetadata) {
	a.openPlayerFile(item, "")
}

// openPlayerFile selects a registered file before opening the decoder once.
func (a *App) openPlayerFile(item library.ItemMetadata, selected string) {
	a.pauseBGM()
	a.setPlayerFullscreen(false)
	var segments []string
	seen := make(map[string]bool)
	add := func(path string) {
		if path != "" && !seen[path] {
			segments = append(segments, path)
			seen[path] = true
		}
	}
	for _, part := range item.Parts {
		add(part.File)
	}
	if item.DouyinVideo != nil {
		add(item.DouyinVideo.File)
	}
	for _, m := range item.Media {
		if m.Role == "video" || m.Role == "live_segment" {
			add(m.Path)
		}
		if m.Role == "motion" && m.Path == selected {
			// A live photo's motion clip plays on its own.
			segments, seen = nil, map[string]bool{}
			add(m.Path)
			break
		}
	}
	a.PlayerOverlay.Segments = segments
	index := 0
	if selected != "" {
		index = -1
		for i, path := range segments {
			if path == selected {
				index = i
				break
			}
		}
	}
	a.openPlayerSegment(item, index)
}

func isGIFLibrarySegment(item library.ItemMetadata, relPath string) bool {
	if relPath == "" {
		return false
	}
	for _, m := range item.Media {
		if m.Path == relPath && m.GIF {
			return true
		}
	}
	for _, np := range item.NotePages {
		if (np.Video == relPath || np.Image == relPath) && np.GIF {
			return true
		}
	}
	return false
}

func (a *App) openPlayerSegment(item library.ItemMetadata, index int) {
	a.closeLivePreview()
	_ = a.player.Close()
	a.PlayerOverlay.Open = true
	a.PlayerOverlay.LastActiveAt = time.Time{}
	a.PlayerOverlay.ControlsHidden = false
	a.PlayerOverlay.MoreMenuOpen = false
	a.PlayerOverlay.VolumeMenuOpen = false
	a.PlayerOverlay.volumeScrubbing = false
	a.PlayerOverlay.opacityScrubbing = false
	a.PlayerOverlay.ItemKey = item.ItemKey
	a.PlayerOverlay.Title = item.Title
	a.PlayerOverlay.Segment = index
	a.PlayerOverlay.DecoderMissing = false
	a.PlayerOverlay.LoadError = ""
	a.PlayerOverlay.Loading = false
	a.PlayerOverlay.MediaPath = ""
	a.PlayerOverlay.SeekNormalized = 0
	a.PlayerOverlay.SubtitleOptions = []string{"关闭"}
	a.PlayerOverlay.LoopMuted = false
	a.PlayerOverlay.LoopPaused = false
	if index < 0 || index >= len(a.PlayerOverlay.Segments) {
		a.PlayerOverlay.LoadError = "没有已登记的可播放视频，请补全缓存或检查文件。"
		return
	}
	a.PlayerOverlay.LoopMuted = isGIFLibrarySegment(item, a.PlayerOverlay.Segments[index])
	mediaPath, err := a.backend.MediaPath(item.ItemKey, a.PlayerOverlay.Segments[index])
	if err != nil {
		a.PlayerOverlay.LoadError = "视频文件不可访问：" + err.Error()
		return
	}
	a.PlayerOverlay.MediaPath = mediaPath
	var part library.VideoPartRecord
	for _, candidate := range item.Parts {
		if candidate.File == a.PlayerOverlay.Segments[index] {
			part = candidate
			break
		}
	}
	// Opening a file (Media Foundation, D3D11) and parsing its danmaku take
	// up to a few hundred milliseconds: they run off the UI thread, which
	// shows the dark layer and a spinner meanwhile.
	a.playerOpenSeq++
	seq := a.playerOpenSeq
	a.PlayerOverlay.Loading = true
	itemKey, backend := item.ItemKey, a.backend
	var danmakuPath string
	if part.Danmaku != "" {
		if path, err := backend.MediaPath(itemKey, part.Danmaku); err == nil {
			danmakuPath = path
		}
	}
	type subtitleFile struct{ lang, path string }
	var subtitleFiles []subtitleFile
	for _, rel := range part.Subtitles {
		if strings.ToLower(filepath.Ext(rel)) != ".vtt" {
			continue
		}
		path, err := backend.MediaPath(itemKey, rel)
		if err != nil {
			continue
		}
		lang := strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
		if i := strings.IndexByte(lang, '.'); i >= 0 {
			lang = lang[i+1:]
		}
		subtitleFiles = append(subtitleFiles, subtitleFile{lang, path})
	}
	type opened struct {
		dec     player.MediaDecoder
		danmaku *player.DanmakuEngine
		tracks  []player.SubtitleTrack
	}
	current := func() bool {
		return a.playerOpenSeq == seq && a.PlayerOverlay.Open && a.PlayerOverlay.MediaPath == mediaPath
	}
	requestGuardedCleanup(a, fmt.Sprintf("player-open-%d", seq), current, func(ctx context.Context) (opened, error) {
		if err := ctx.Err(); err != nil {
			return opened{}, err
		}
		dec, err := player.OpenMediaFile(mediaPath)
		if err != nil {
			return opened{}, err
		}
		if err := ctx.Err(); err != nil {
			_ = dec.Close()
			return opened{}, err
		}
		out := opened{dec: dec}
		if danmakuPath != "" {
			if raw, err := os.ReadFile(danmakuPath); err == nil {
				if eng, err := player.ParseDanmakuXML(raw); err == nil {
					out.danmaku = eng
				}
			}
		}
		for _, f := range subtitleFiles {
			raw, err := os.ReadFile(f.path)
			if err != nil {
				continue
			}
			if cues := player.ParseWebVTT(raw); len(cues) > 0 {
				out.tracks = append(out.tracks, player.SubtitleTrack{Lang: f.lang, Label: f.lang, Cues: cues})
			}
		}
		return out, nil
	}, func(res opened) {
		a.PlayerOverlay.Loading = false
		if err := a.player.LoadDecoder(mediaPath, res.dec); err != nil {
			a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
			a.PlayerOverlay.MissingReason = err.Error()
			a.PlayerOverlay.LoadError = "无法播放此文件：" + err.Error()
			return
		}
		if res.danmaku != nil {
			a.player.SetDanmakuEngine(res.danmaku)
		}
		for _, t := range res.tracks {
			a.PlayerOverlay.SubtitleOptions = append(a.PlayerOverlay.SubtitleOptions, t.Lang)
		}
		a.player.SetSubtitles(res.tracks)
		a.applyPlayerPrefs()
		if a.PlayerOverlay.LoopMuted {
			a.player.SetMuted(true)
		}
		a.player.Play() // videos start playing as soon as they open
	}, func(err error) {
		a.PlayerOverlay.Loading = false
		a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
		a.PlayerOverlay.MissingReason = err.Error()
		a.PlayerOverlay.LoadError = "无法播放此文件：" + err.Error()
	}, func(res opened) {
		if res.dec != nil {
			_ = res.dec.Close()
		}
	})
}

// openRemoteVideoPreview stages an uncached online video via Backend.StagePreviewMedia
// (allowlisted CDN, zero Cookie) and plays it in PlayerOverlay.
func (a *App) openRemoteVideoPreview(siteID, title, rawURL string) {
	a.openRemoteVideoPreviewOpts(siteID, title, rawURL, false)
}

func (a *App) openRemoteVideoPreviewOpts(siteID, title, rawURL string, loopMuted bool) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" || a.backend == nil {
		return
	}
	a.pauseBGM()
	a.closeLivePreview()
	a.setPlayerFullscreen(false)
	_ = a.player.Close()

	a.PlayerOverlay.Open = true
	a.PlayerOverlay.LastActiveAt = time.Time{}
	a.PlayerOverlay.ControlsHidden = false
	a.PlayerOverlay.MoreMenuOpen = false
	a.PlayerOverlay.VolumeMenuOpen = false
	a.PlayerOverlay.volumeScrubbing = false
	a.PlayerOverlay.opacityScrubbing = false
	a.PlayerOverlay.ItemKey = ""
	a.PlayerOverlay.Title = firstNonEmptyStrUI(title, "在线视频预览")
	a.PlayerOverlay.Segments = nil
	a.PlayerOverlay.Segment = 0
	a.PlayerOverlay.DecoderMissing = false
	a.PlayerOverlay.MissingReason = ""
	a.PlayerOverlay.LoadError = ""
	a.PlayerOverlay.Loading = true
	a.PlayerOverlay.MediaPath = ""
	a.PlayerOverlay.SeekNormalized = 0
	a.PlayerOverlay.SubtitleOptions = []string{"关闭"}
	a.PlayerOverlay.LoopMuted = loopMuted
	a.PlayerOverlay.LoopPaused = false

	a.playerOpenSeq++
	seq := a.playerOpenSeq
	backend := a.backend
	type previewOpened struct {
		path string
		dec  player.MediaDecoder
	}
	current := func() bool {
		return a.playerOpenSeq == seq && a.PlayerOverlay.Open
	}
	requestGuardedCleanup(a, fmt.Sprintf("player-preview-%d", seq), current, func(ctx context.Context) (previewOpened, error) {
		stagedPath, err := backend.StagePreviewMedia(ctx, siteID, rawURL, ".mp4")
		if err != nil {
			return previewOpened{}, err
		}
		if err := ctx.Err(); err != nil {
			return previewOpened{}, err
		}
		dec, err := player.OpenMediaFile(stagedPath)
		if err != nil {
			return previewOpened{}, err
		}
		if err := ctx.Err(); err != nil {
			_ = dec.Close()
			return previewOpened{}, err
		}
		return previewOpened{path: stagedPath, dec: dec}, nil
	}, func(res previewOpened) {
		a.PlayerOverlay.Loading = false
		a.PlayerOverlay.MediaPath = res.path
		if err := a.player.LoadDecoder(res.path, res.dec); err != nil {
			a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
			a.PlayerOverlay.MissingReason = err.Error()
			a.PlayerOverlay.LoadError = "无法预览此视频：" + err.Error()
			return
		}
		a.applyPlayerPrefs()
		if a.PlayerOverlay.LoopMuted {
			a.player.SetMuted(true)
		}
		a.player.Play()
	}, func(err error) {
		a.PlayerOverlay.Loading = false
		a.PlayerOverlay.DecoderMissing = player.IsMissingCodecError(err)
		a.PlayerOverlay.MissingReason = err.Error()
		a.PlayerOverlay.LoadError = "无法预览此视频：" + userErrorText(err)
	}, func(res previewOpened) {
		if res.dec != nil {
			_ = res.dec.Close()
		}
	})
}
