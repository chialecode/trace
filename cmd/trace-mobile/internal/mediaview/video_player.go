package mediaview

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/cmd/trace-mobile/internal/codeccheck"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/pagekit"
	"github.com/chialecode/trace/cmd/trace-mobile/internal/theme"
	"github.com/chialecode/trace/internal/companionapi"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/mobile"
)

// SubtitleSource identifies one downloadable WebVTT/SRT subtitle track on desktop.
type SubtitleSource struct {
	Lang    string
	Label   string
	AI      bool
	RelPath string
}

// DisplayLabel returns a user-friendly Chinese/language track title.
func (s SubtitleSource) DisplayLabel() string {
	lbl := strings.TrimSpace(s.Label)
	if lbl == "" {
		lbl = strings.TrimSpace(s.Lang)
	}
	if lbl == "" {
		lbl = "字幕"
	}
	if s.AI && !strings.Contains(strings.ToUpper(lbl), "AI") {
		lbl += " (AI)"
	}
	return lbl
}

// TrackKey returns a stable map key for caching this subtitle track in RAM.
func (s SubtitleSource) TrackKey() string {
	if k := strings.TrimSpace(s.Lang); k != "" {
		return k
	}
	return strings.TrimSpace(s.RelPath)
}

// VideoChapter marks one jumpable video chapter segment.
type VideoChapter struct {
	FromSec int
	ToSec   int
	Title   string
}

// SubtitlesFromDTO converts companionapi SubtitleTrackDTO entries into player SubtitleSource entries.
func SubtitlesFromDTO(tracks []companionapi.SubtitleTrackDTO) []SubtitleSource {
	if len(tracks) == 0 {
		return nil
	}
	out := make([]SubtitleSource, 0, len(tracks))
	for _, tr := range tracks {
		rel := strings.TrimSpace(tr.RelPath)
		if rel == "" {
			rel = strings.TrimSpace(tr.VTT)
		}
		if rel == "" {
			rel = strings.TrimSpace(tr.SRT)
		}
		if rel == "" {
			continue
		}
		out = append(out, SubtitleSource{
			Lang:    strings.TrimSpace(tr.Lang),
			Label:   strings.TrimSpace(tr.Label),
			AI:      tr.AI,
			RelPath: rel,
		})
	}
	return out
}

// ChaptersFromDTO converts companionapi ChapterDTO entries into player VideoChapter entries.
func ChaptersFromDTO(chapters []companionapi.ChapterDTO) []VideoChapter {
	if len(chapters) == 0 {
		return nil
	}
	out := make([]VideoChapter, 0, len(chapters))
	for _, ch := range chapters {
		out = append(out, VideoChapter{
			FromSec: ch.FromSec,
			ToSec:   ch.ToSec,
			Title:   strings.TrimSpace(ch.Title),
		})
	}
	return out
}

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
	DanmakuRelPath string
	Subtitles      []SubtitleSource
	Chapters       []VideoChapter
}

// VideoRequest configures a VideoPlayer session.
type VideoRequest struct {
	ItemKey          string
	Title            string
	IsLiveRecording  bool
	IsLivePreview    bool   // active live recording real-time preview (.part / HLS)
	LiveRecordingKey string // companionapi live recording key for preview streaming
	Tracks           []VideoTrack
	StartIndex       int
}

const (
	defaultDanmakuOpacity = 0.85
	defaultDanmakuArea    = 0.5
	defaultDanmakuDensity = "medium"
)

// VideoPlayer plays cached videos (with multi-P picker, in-memory danmaku
// overlay, WebVTT/SRT subtitles, and chapter jumps), multi-segment live
// recordings (sequential auto-advance), and active live recording real-time
// previews via plugins/videoplayer + Host.MediaProxy(), with landscape
// fullscreen and two-layer unsupported-codec detection (FR-MOB-014, FR-MOB-030).
type VideoPlayer struct {
	host    pagekit.Host
	req     VideoRequest
	win     *mygo.Window
	onClose func()

	mu             sync.Mutex
	closed         bool
	trackGen       uint64
	currentIdx     int
	player         PlaybackHandle
	ticket         string
	state          videoplayer.State
	posAnchorUI    time.Time
	unsupportedMsg string
	loadErrMsg     string
	muted          bool
	landscape      bool
	pickerOpen     bool
	optionsOpen    bool
	seekFraction   float64
	startedInitial bool
	nativeFallback bool

	// In-memory only danmaku & subtitle buffers (zero disk writes, wiped on close/switch).
	danmaku        *DanmakuEngine
	danmakuLoading bool
	danmakuErr     string
	danmakuEnabled bool
	danmakuOpacity float64
	danmakuDensity string
	danmakuArea    float64

	subtitleTracks  map[string]*SubtitleTrackData
	subtitleKey     string // "" = closed
	subtitleLoading bool
	subtitleErr     string
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
		host:           h,
		req:            req,
		onClose:        onClose,
		currentIdx:     idx,
		danmakuEnabled: true,
		danmakuOpacity: defaultDanmakuOpacity,
		danmakuDensity: defaultDanmakuDensity,
		danmakuArea:    defaultDanmakuArea,
		subtitleTracks: make(map[string]*SubtitleTrackData),
	}
	registerActive(vp, vp.Close)
	return vp
}

// OpenVideoPlayer opens a full-screen child window playing req.
func OpenVideoPlayer(h pagekit.Host, req VideoRequest) *VideoPlayer {
	if h == nil || (len(req.Tracks) == 0 && !req.IsLivePreview) {
		return nil
	}
	if req.IsLivePreview && len(req.Tracks) == 0 {
		req.Tracks = []VideoTrack{{
			Label: firstNonEmptyStr(req.Title, "直播实时预览"),
		}}
	}
	var vp *VideoPlayer
	vp = NewVideoPlayer(h, req, func() {
		if vp != nil && vp.win != nil {
			vp.win.Close()
		}
	})
	title := req.Title
	if strings.TrimSpace(title) == "" {
		if req.IsLivePreview {
			title = "直播实时预览"
		} else {
			title = "播放视频"
		}
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

// Close stops playback, revokes proxy tickets, wipes in-memory danmaku/subtitles,
// restores screen orientation, and closes the window.
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
	vp.trackGen++
	p := vp.player
	ticket := vp.ticket
	vp.player = nil
	vp.ticket = ""
	vp.posAnchorUI = time.Time{}
	vp.danmaku = nil
	vp.danmakuLoading = false
	vp.danmakuErr = ""
	clear(vp.subtitleTracks)
	vp.subtitleLoading = false
	vp.subtitleErr = ""
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

// DanmakuCount returns the count of parsed in-memory danmaku entries for the active track.
func (vp *VideoPlayer) DanmakuCount() int {
	vp.mu.Lock()
	defer vp.mu.Unlock()
	if vp.danmaku == nil {
		return 0
	}
	return vp.danmaku.Count()
}

// VisibleDanmaku computes currently visible danmaku entries for the active playback position.
func (vp *VideoPlayer) VisibleDanmaku(viewportW, viewportH float32) []PositionedDanmaku {
	vp.mu.Lock()
	if !vp.danmakuEnabled || vp.danmaku == nil || vp.req.IsLivePreview {
		vp.mu.Unlock()
		return nil
	}
	eng := vp.danmaku
	posSec := vp.state.Position.Seconds()
	area := vp.danmakuArea
	opacity := vp.danmakuOpacity
	density := vp.danmakuDensity
	vp.mu.Unlock()
	return eng.LayoutFrame(posSec, viewportW, viewportH, area, opacity, density)
}

// ActiveSubtitle returns the active subtitle cue text at the current playback position.
func (vp *VideoPlayer) ActiveSubtitle() string {
	vp.mu.Lock()
	defer vp.mu.Unlock()
	if vp.subtitleKey == "" || vp.req.IsLivePreview {
		return ""
	}
	tr := vp.subtitleTracks[vp.subtitleKey]
	if tr == nil {
		return ""
	}
	return tr.ActiveCueAt(vp.state.Position.Seconds())
}

// SetDanmakuEnabled toggles danmaku overlay visibility.
func (vp *VideoPlayer) SetDanmakuEnabled(enabled bool) {
	vp.mu.Lock()
	vp.danmakuEnabled = enabled
	vp.mu.Unlock()
}

// SetDanmakuOpacity sets danmaku opacity in [0.2, 1.0].
func (vp *VideoPlayer) SetDanmakuOpacity(opacity float64) {
	if math.IsNaN(opacity) || math.IsInf(opacity, 0) {
		return
	}
	if opacity < 0.2 {
		opacity = 0.2
	}
	if opacity > 1.0 {
		opacity = 1.0
	}
	vp.mu.Lock()
	vp.danmakuOpacity = opacity
	vp.mu.Unlock()
}

// SetDanmakuDensity sets danmaku density ("low", "medium", "high", "unlimited").
func (vp *VideoPlayer) SetDanmakuDensity(density string) {
	vp.mu.Lock()
	vp.danmakuDensity = NormalizeDanmakuDensity(density)
	vp.mu.Unlock()
}

// SetDanmakuArea sets vertical screen area ratio (0.25, 0.5, 1.0).
func (vp *VideoPlayer) SetDanmakuArea(area float64) {
	if area <= 0 || area > 1.0 {
		area = defaultDanmakuArea
	}
	vp.mu.Lock()
	vp.danmakuArea = area
	vp.mu.Unlock()
}

// SelectSubtitle selects a subtitle track key ("" disables subtitles) and fetches
// it into memory if not yet loaded for the active track.
func (vp *VideoPlayer) SelectSubtitle(trackKey string) {
	vp.mu.Lock()
	vp.subtitleKey = strings.TrimSpace(trackKey)
	vp.subtitleErr = ""
	if vp.subtitleKey == "" {
		vp.subtitleLoading = false
		vp.mu.Unlock()
		return
	}
	if vp.subtitleTracks[vp.subtitleKey] != nil {
		vp.subtitleLoading = false
		vp.mu.Unlock()
		return
	}
	idx := vp.currentIdx
	gen := vp.trackGen
	closed := vp.closed
	vp.mu.Unlock()

	if closed || idx < 0 || idx >= len(vp.req.Tracks) {
		return
	}
	for _, src := range vp.req.Tracks[idx].Subtitles {
		if src.TrackKey() == trackKey {
			vp.fetchSubtitleAsync(gen, src)
			return
		}
	}
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
	gen := vp.trackGen
	vp.state = videoplayer.State{}
	vp.posAnchorUI = time.Time{}
	vp.unsupportedMsg = ""
	vp.loadErrMsg = ""
	vp.nativeFallback = false
	vp.seekFraction = 0
	muted := vp.muted
	wantSubKey := vp.subtitleKey
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

	loopURL, ticket, err := vp.issueTrackTicket(track)
	if err != nil {
		vp.mu.Lock()
		if vp.req.IsLivePreview {
			vp.loadErrMsg = "无法准备直播实时预览地址：" + err.Error()
		} else {
			vp.loadErrMsg = "无法准备本机视频播放地址"
		}
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

	// Load in-memory danmaku & selected subtitle asynchronously (never persisted to phone disk).
	if !vp.req.IsLivePreview {
		if strings.TrimSpace(track.DanmakuRelPath) != "" {
			vp.fetchDanmakuAsync(gen, track.DanmakuRelPath)
		}
		if wantSubKey != "" {
			matched := false
			for _, src := range track.Subtitles {
				if src.TrackKey() == wantSubKey {
					matched = true
					vp.fetchSubtitleAsync(gen, src)
					break
				}
			}
			if !matched {
				vp.mu.Lock()
				if vp.trackGen == gen {
					vp.subtitleKey = ""
				}
				vp.mu.Unlock()
			}
		}
	}

	if vp.host != nil {
		vp.host.Update(nil)
	}
}

func (vp *VideoPlayer) issueTrackTicket(track VideoTrack) (string, string, error) {
	if vp.req.IsLivePreview {
		key := strings.TrimSpace(vp.req.LiveRecordingKey)
		if key == "" {
			key = strings.TrimSpace(vp.req.ItemKey)
		}
		return issueLivePreviewTicket(vp.host.MediaProxy(), key)
	}
	return vp.host.MediaProxy().IssueTicket(vp.req.ItemKey, track.RelPath)
}

func (vp *VideoPlayer) fetchDanmakuAsync(gen uint64, relPath string) {
	cli := vp.host.Client()
	if cli == nil || strings.TrimSpace(vp.req.ItemKey) == "" || strings.TrimSpace(relPath) == "" {
		return
	}
	vp.mu.Lock()
	if vp.closed || vp.trackGen != gen {
		vp.mu.Unlock()
		return
	}
	vp.danmakuLoading = true
	vp.danmakuErr = ""
	vp.mu.Unlock()

	itemKey := vp.req.ItemKey
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, err := cli.FetchDanmaku(ctx, itemKey, relPath)
		var eng *DanmakuEngine
		if err == nil {
			eng, err = ParseDanmakuXML(raw)
		}
		vp.mu.Lock()
		if vp.closed || vp.trackGen != gen {
			vp.mu.Unlock()
			return
		}
		vp.danmakuLoading = false
		if err != nil {
			vp.danmaku = nil
			vp.danmakuErr = "弹幕加载失败"
		} else {
			vp.danmaku = eng
			vp.danmakuErr = ""
		}
		vp.mu.Unlock()
		if vp.host != nil {
			vp.host.Update(nil)
		}
	}()
}

func (vp *VideoPlayer) fetchSubtitleAsync(gen uint64, src SubtitleSource) {
	cli := vp.host.Client()
	if cli == nil || strings.TrimSpace(vp.req.ItemKey) == "" || strings.TrimSpace(src.RelPath) == "" {
		return
	}
	key := src.TrackKey()
	vp.mu.Lock()
	if vp.closed || vp.trackGen != gen {
		vp.mu.Unlock()
		return
	}
	vp.subtitleLoading = true
	vp.subtitleErr = ""
	vp.mu.Unlock()

	itemKey := vp.req.ItemKey
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		raw, err := cli.FetchSubtitle(ctx, itemKey, src.RelPath)
		vp.mu.Lock()
		if vp.closed || vp.trackGen != gen {
			vp.mu.Unlock()
			return
		}
		vp.subtitleLoading = false
		if err != nil {
			vp.subtitleErr = "字幕加载失败"
		} else {
			parsed := NewSubtitleTrack(src.Lang, src.DisplayLabel(), raw)
			vp.subtitleTracks[key] = &parsed
			vp.subtitleErr = ""
		}
		vp.mu.Unlock()
		if vp.host != nil {
			vp.host.Update(nil)
		}
	}()
}

func (vp *VideoPlayer) handlePlayerState(p PlaybackHandle, idx int, track VideoTrack, st videoplayer.State) {
	vp.mu.Lock()
	if vp.closed || vp.player != p {
		vp.mu.Unlock()
		return
	}
	vp.state = st
	vp.posAnchorUI = time.Time{}
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
		} else if vp.req.IsLivePreview {
			vp.loadErrMsg = "直播实时预览中断：" + st.Error
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

	// Sequential multi-part live recording auto-advance (never in live preview mode).
	shouldAdvance := st.Ended && vp.req.IsLiveRecording && !vp.req.IsLivePreview && idx+1 < len(vp.req.Tracks)
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

func (vp *VideoPlayer) seekTo(p PlaybackHandle, curTrack VideoTrack, target time.Duration) {
	if p == nil {
		return
	}
	if target < 0 {
		target = 0
	}
	p.Seek(target)
	vp.mu.Lock()
	vp.state.Position = target
	vp.posAnchorUI = time.Time{}
	dur := vp.state.Duration
	if dur <= 0 && curTrack.DurationSec > 0 {
		dur = time.Duration(curTrack.DurationSec) * time.Second
	}
	if dur > 0 {
		frac := float64(target) / float64(dur)
		if frac < 0 {
			frac = 0
		}
		if frac > 1 {
			frac = 1
		}
		vp.seekFraction = frac
	}
	vp.mu.Unlock()
}

// View renders the full-screen video player with in-memory danmaku overlay,
// synchronized subtitle captions, chapter jump strip, options sheet, and
// multi-P / segment picker sheet.
func (vp *VideoPlayer) View(c *ui.Context) {
	vp.mu.Lock()
	started := vp.startedInitial
	vp.mu.Unlock()
	if !started && len(vp.req.Tracks) > 0 {
		vp.StartTrack(vp.currentIdx)
	}

	c.OnWindowShortcut(0, ui.KeyEscape, func() { vp.Close() })

	now := c.Now()
	vw, vh := c.Size()
	if vw <= 0 {
		vw = 412
	}
	if vh <= 0 {
		vh = 915
	}

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

	dmEngine := vp.danmaku
	dmEnabled := vp.danmakuEnabled
	dmOpacity := vp.danmakuOpacity
	dmDensity := vp.danmakuDensity
	dmArea := vp.danmakuArea
	dmLoading := vp.danmakuLoading
	dmErr := vp.danmakuErr

	subKey := vp.subtitleKey
	var activeSubTrack *SubtitleTrackData
	if subKey != "" {
		activeSubTrack = vp.subtitleTracks[subKey]
	}
	subLoading := vp.subtitleLoading
	subErr := vp.subtitleErr

	// Smoothly interpolate playback position between MediaPlayer state ticks while playing.
	posSec := st.Position.Seconds()
	if st.Playing && !vp.req.IsLivePreview {
		if vp.posAnchorUI.IsZero() {
			vp.posAnchorUI = now
		} else {
			delta := now.Sub(vp.posAnchorUI)
			if delta > 0 {
				if delta > 2*time.Second {
					delta = 2 * time.Second
				}
				posSec += delta.Seconds()
				if st.Duration > 0 && posSec > st.Duration.Seconds() {
					posSec = st.Duration.Seconds()
				}
			}
		}
	} else {
		vp.posAnchorUI = time.Time{}
	}
	vp.mu.Unlock()

	var curTrack VideoTrack
	if idx >= 0 && idx < len(vp.req.Tracks) {
		curTrack = vp.req.Tracks[idx]
	}

	hasDanmaku := !vp.req.IsLivePreview && (curTrack.DanmakuRelPath != "" || dmEngine != nil)
	hasSubtitles := !vp.req.IsLivePreview && len(curTrack.Subtitles) > 0
	hasOptions := hasDanmaku || hasSubtitles
	hasChapters := !vp.req.IsLivePreview && len(curTrack.Chapters) > 0 && unsupported == "" && loadErr == ""

	// Schedule smooth overlay animation frames while playing with active danmaku or subtitles.
	if st.Playing && unsupported == "" && loadErr == "" && !vp.req.IsLivePreview &&
		((dmEnabled && dmEngine != nil && dmEngine.Count() > 0) || activeSubTrack != nil) {
		c.After(33 * time.Millisecond)
	}

	var visibleDanmaku []PositionedDanmaku
	dmTop := float32(64)
	dmHeight := vh - 210
	if dmHeight < 120 {
		dmHeight = 120
	}
	if !vp.req.IsLivePreview && unsupported == "" && loadErr == "" && dmEnabled && dmEngine != nil && dmEngine.Count() > 0 {
		visibleDanmaku = dmEngine.LayoutFrame(posSec, vw, dmHeight, dmArea, dmOpacity, dmDensity)
	}

	activeCaption := ""
	if !vp.req.IsLivePreview && unsupported == "" && loadErr == "" && activeSubTrack != nil {
		activeCaption = activeSubTrack.ActiveCueAt(posSec)
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
			loadingText := "正在缓冲视频…"
			if vp.req.IsLivePreview {
				loadingText = "正在连接电脑端录制实时画面…"
			}
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, loadingText).FontSize(14).TextColor(ui.RGBA(255, 255, 255, 0.78))
			})
		}

		// In-memory danmaku overlay (painted directly, pass-through touch, zero overflow).
		if len(visibleDanmaku) > 0 {
			alphaF := float32(dmOpacity)
			if alphaF < 0.2 {
				alphaF = 0.2
			}
			if alphaF > 1 {
				alphaF = 1
			}
			shadowCol := ui.RGBA(0, 0, 0, alphaF*0.8)
			ui.Box(c).
				Absolute().Top(dmTop).Left(0).Right(0).Height(dmHeight).ZIndex(5).
				IgnoreInput(true).
				AccessHidden(true).
				Draw(func(painter *ui.Painter, r ui.Rect) {
					painter.Clip(r, 0, func() {
						for _, dm := range visibleDanmaku {
							x := r.X + dm.X
							y := r.Y + dm.Y
							col := ui.RGBA(dm.Color.R, dm.Color.G, dm.Color.B, alphaF)
							painter.Text(x+1, y+1, dm.Text, dm.FontSize, shadowCol)
							painter.Text(x, y, dm.Text, dm.FontSize, col)
						}
					})
				})
		}

		// Synchronized subtitle caption overlay (centered above bottom controls).
		if activeCaption != "" {
			subBottom := float32(126)
			if hasChapters {
				subBottom = 172
			}
			ui.Row(c).
				Absolute().Bottom(subBottom).Left(16).Right(16).ZIndex(6).
				Justify(ui.Center).
				IgnoreInput(true).
				Children(func() {
					ui.Box(c).
						Label("当前字幕").
						MaxWidth(vw-32).
						Padding(6, 12).
						Radius(8).
						Background(ui.RGBA(0, 0, 0, 0.76)).
						Children(func() {
							ui.Text(c, activeCaption).
								FontSize(15).FontWeight(600).
								LineHeight(theme.LineHeightCompact).
								TextColor(theme.ColorWhite).
								TextAlign(ui.Center).
								MaxLines(3)
						})
				})
		}

		// Top translucent bar: close, title, live preview / track badge, landscape toggle.
		ui.Row(c).
			Absolute().Top(0).Left(0).Right(0).ZIndex(10).
			Background(ui.RGBA(0, 0, 0, 0.58)).
			Padding(10, 12).
			Gap(8).
			AlignItems(ui.Center).
			ClipX().
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

				ui.Column(c).Grow(1).Basis(0).Gap(2).ClipX().Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).ClipX().Children(func() {
						if vp.req.IsLivePreview {
							ui.Box(c).Padding(2, 8).Radius(999).Background(theme.ColorLive).Shrink(0).Children(func() {
								ui.Text(c, "实时预览").FontSize(11).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
							})
						}
						title := vp.req.Title
						if title == "" {
							title = curTrack.Label
						}
						ui.Text(c, title).FontSize(14).FontWeight(600).TextColor(theme.ColorWhite).SingleLine().Grow(1).Shrink(1)
					})
					if len(vp.req.Tracks) > 1 && !vp.req.IsLivePreview {
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
			Background(ui.RGBA(0, 0, 0, 0.66)).
			Padding(10, 14).
			Gap(8).
			ClipX().
			Children(func() {
				// Chapter jump strip (horizontal scroll, highlighted current chapter).
				if hasChapters {
					ui.ScrollHorizontal(c).FillWidth().ClipX().Scrollbars(false).Children(func() {
						ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
							for ci, ch := range curTrack.Chapters {
								chapter := ch
								active := posSec >= float64(chapter.FromSec) &&
									(chapter.ToSec <= chapter.FromSec || posSec < float64(chapter.ToSec))
								bg := ui.RGBA(255, 255, 255, 0.16)
								if active {
									bg = theme.ColorAccent
								}
								ts := FormatDuration(chapter.FromSec)
								if ts == "" {
									ts = "00:00"
								}
								chText := ts
								if strings.TrimSpace(chapter.Title) != "" {
									chText += " " + strings.TrimSpace(chapter.Title)
								}
								chBtn := ui.Button(c.Key("ch-"+fmt.Sprint(ci)), "").
									Label(fmt.Sprintf("章节跳转：%s", chText)).
									MinHeight(34).Padding(5, 10).Radius(999).
									Background(bg).
									Shrink(0).
									Justify(ui.Center).AlignItems(ui.Center)
								chBtn.Children(func() {
									ui.Text(c, chText).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
								})
								if chBtn.Clicked() && p != nil {
									vp.seekTo(p, curTrack, time.Duration(chapter.FromSec)*time.Second)
								}
							}
						})
					})
				}

				// Progress slider (hidden in live preview mode) OR live stream status line.
				if unsupported == "" && loadErr == "" {
					if vp.req.IsLivePreview {
						liveHint := "实时同步电脑端正在录制的直播画面"
						if sec := int(st.Position.Seconds()); sec > 0 {
							if d := FormatDuration(sec); d != "" {
								liveHint = "实时预览 · 已播放 " + d
							}
						}
						ui.Text(c, liveHint).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.82)).SingleLine()
					} else {
						durSec := int(st.Duration.Seconds())
						if durSec <= 0 {
							durSec = curTrack.DurationSec
						}
						posInt := int(posSec)
						ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
							posText := FormatDuration(posInt)
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
									vp.seekTo(p, curTrack, time.Duration(float64(targetDur)*sliderVal))
								}
							}

							durText := FormatDuration(durSec)
							if durText == "" {
								durText = "--:--"
							}
							ui.Text(c, durText).FontSize(12).TextColor(ui.RGBA(255, 255, 255, 0.8)).SingleLine()
						})
					}
				}

				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).ClipX().Children(func() {
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
							Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
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
							Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
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

					// Danmaku & Subtitle options sheet button.
					if hasOptions && unsupported == "" && loadErr == "" {
						optText := "弹幕/字幕"
						if hasDanmaku && !hasSubtitles {
							if dmEnabled {
								optText = "弹幕 开"
							} else {
								optText = "弹幕 关"
							}
						} else if !hasDanmaku && hasSubtitles {
							if subKey != "" {
								optText = "字幕 开"
							} else {
								optText = "字幕"
							}
						}
						optBg := ui.RGBA(255, 255, 255, 0.20)
						if (hasDanmaku && dmEnabled) || subKey != "" {
							optBg = ui.RGBA(26, 115, 232, 0.80)
						}
						optBtn := ui.Button(c, "").
							Label("弹幕与字幕设置").
							MinHeight(40).Padding(8, 12).Radius(999).
							Background(optBg).
							Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
						optBtn.Children(func() {
							ui.Text(c, optText).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						if optBtn.Clicked() {
							vp.optionsOpen = true
						}
					}

					// Multi-P or multi-segment picker button.
					if len(vp.req.Tracks) > 1 && !vp.req.IsLivePreview {
						btnTitle := fmt.Sprintf("选集 (%d)", len(vp.req.Tracks))
						if vp.req.IsLiveRecording {
							btnTitle = fmt.Sprintf("分段 (%d)", len(vp.req.Tracks))
						}
						pickBtn := ui.Button(c, "").
							Label(btnTitle).
							MinHeight(40).Padding(8, 12).Radius(999).
							Background(ui.RGBA(255, 255, 255, 0.20)).
							Justify(ui.Center).AlignItems(ui.Center).Shrink(0)
						pickBtn.Children(func() {
							ui.Text(c, btnTitle).FontSize(12).FontWeight(600).TextColor(theme.ColorWhite).SingleLine()
						})
						if pickBtn.Clicked() {
							vp.pickerOpen = true
						}
					}
				})
			})

		// Danmaku & subtitle options bottom sheet (mobile.Sheet).
		if hasOptions {
			vp.renderOptionsSheet(c, curTrack, dmEngine, dmEnabled, dmOpacity, dmDensity, dmArea, dmLoading, dmErr, subKey, subLoading, subErr)
		}

		// Multi-P / live segment picker bottom sheet (mobile.Sheet).
		if len(vp.req.Tracks) > 1 && !vp.req.IsLivePreview {
			vp.renderTrackPickerSheet(c, idx)
		}
	})
}

func (vp *VideoPlayer) renderOptionsSheet(
	c *ui.Context,
	curTrack VideoTrack,
	dmEngine *DanmakuEngine,
	dmEnabled bool,
	dmOpacity float64,
	dmDensity string,
	dmArea float64,
	dmLoading bool,
	dmErr string,
	subKey string,
	subLoading bool,
	subErr string,
) {
	bg := theme.ColorBgMain
	mobile.Sheet(c, &vp.optionsOpen, mobile.SheetOptions{
		Detents:    []float32{0.62, 0.88},
		Background: &bg,
		Label:      "弹幕与字幕设置面板",
	}, func() {
		ui.Scroll(c).Fill().Children(func() {
			ui.Column(c).FillWidth().Padding(10, 16, 24, 16).Gap(14).ClipX().Children(func() {
				ui.Text(c, "弹幕与字幕").FontSize(16).FontWeight(600).TextColor(theme.ColorTextMain)

				// 1. Danmaku section.
				ui.Column(c).FillWidth().Padding(12, 14).Radius(12).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(10).ClipX().Children(func() {
					ui.Row(c).FillWidth().AlignItems(ui.Center).Justify(ui.SpaceBetween).Gap(8).Children(func() {
						ui.Column(c).Grow(1).Shrink(1).Gap(2).Children(func() {
							ui.Text(c, "显示弹幕").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain).SingleLine()
							statusTxt := "此视频暂无弹幕"
							switch {
							case dmLoading:
								statusTxt = "正在加载弹幕…"
							case dmErr != "":
								statusTxt = dmErr
							case dmEngine != nil && dmEngine.Count() > 0:
								statusTxt = fmt.Sprintf("已加载 %d 条弹幕（仅内存，不落盘）", dmEngine.Count())
							}
							ui.Text(c, statusTxt).FontSize(12).TextColor(theme.ColorTextMuted).SingleLine()
						})
						enabled := dmEnabled
						canToggle := curTrack.DanmakuRelPath != "" || dmEngine != nil
						if ui.Switch(c, &enabled).Label("弹幕开关").Disabled(!canToggle).Changed() {
							vp.SetDanmakuEnabled(enabled)
						}
					})

					if curTrack.DanmakuRelPath != "" || dmEngine != nil {
						// Opacity presets.
						ui.Column(c).FillWidth().Gap(6).Children(func() {
							ui.Text(c, "透明度").FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted)
							ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
								for _, opt := range []struct {
									label string
									val   float64
								}{
									{"40%", 0.40},
									{"60%", 0.60},
									{"85%", 0.85},
									{"100%", 1.00},
								} {
									active := math.Abs(dmOpacity-opt.val) < 0.06
									if optionChipButton(c.Key("op-"+opt.label), opt.label, "弹幕透明度："+opt.label, active).Clicked() {
										vp.SetDanmakuOpacity(opt.val)
									}
								}
							})
						})

						// Density presets.
						ui.Column(c).FillWidth().Gap(6).Children(func() {
							ui.Text(c, "密度").FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted)
							ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
								for _, opt := range []struct {
									key   string
									label string
								}{
									{"low", "低"},
									{"medium", "中"},
									{"high", "高"},
									{"unlimited", "不限"},
								} {
									active := NormalizeDanmakuDensity(dmDensity) == opt.key
									if optionChipButton(c.Key("den-"+opt.key), opt.label, "弹幕密度："+opt.label, active).Clicked() {
										vp.SetDanmakuDensity(opt.key)
									}
								}
							})
						})

						// Display area presets.
						ui.Column(c).FillWidth().Gap(6).Children(func() {
							ui.Text(c, "显示区域").FontSize(12).FontWeight(600).TextColor(theme.ColorTextMuted)
							ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
								for _, opt := range []struct {
									label string
									val   float64
								}{
									{"1/4 屏", 0.25},
									{"半屏", 0.50},
									{"全屏", 1.00},
								} {
									active := math.Abs(dmArea-opt.val) < 0.08
									if optionChipButton(c.Key("area-"+opt.label), opt.label, "弹幕区域："+opt.label, active).Clicked() {
										vp.SetDanmakuArea(opt.val)
									}
								}
							})
						})
					}
				})

				// 2. Subtitle section.
				ui.Column(c).FillWidth().Padding(12, 14).Radius(12).Background(theme.ColorBgPanel).Border(1, theme.ColorBorder).Gap(10).ClipX().Children(func() {
					ui.Text(c, "字幕轨").FontSize(14).FontWeight(600).TextColor(theme.ColorTextMain)
					if len(curTrack.Subtitles) == 0 {
						ui.Text(c, "此视频暂无可用字幕").FontSize(12).TextColor(theme.ColorTextMuted)
					} else {
						if subLoading {
							ui.Text(c, "正在加载字幕…").FontSize(12).TextColor(theme.ColorAccent)
						} else if subErr != "" {
							ui.Text(c, subErr).FontSize(12).TextColor(theme.ColorError)
						}
						ui.Row(c).FillWidth().Gap(6).Wrap().Children(func() {
							if optionChipButton(c.Key("sub-off"), "关闭", "关闭字幕", subKey == "").Clicked() {
								vp.SelectSubtitle("")
							}
							for si, src := range curTrack.Subtitles {
								s := src
								k := s.TrackKey()
								lbl := s.DisplayLabel()
								if optionChipButton(c.Key("sub-"+fmt.Sprint(si)), lbl, "选择字幕："+lbl, subKey == k).Clicked() {
									vp.SelectSubtitle(k)
								}
							}
						})
					}
				})
			})
		})
	})
}

func (vp *VideoPlayer) renderTrackPickerSheet(c *ui.Context, idx int) {
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

func optionChipButton(c *ui.Context, text, label string, active bool) ui.Element {
	bg := theme.ColorBgMain
	borderCol := theme.ColorBorderStrong
	txtCol := theme.ColorTextMain
	if active {
		bg = theme.ColorAccentBg
		borderCol = theme.ColorAccent
		txtCol = theme.ColorAccent
	}
	btn := ui.Button(c, "").
		Label(label).
		MinHeight(36).
		Padding(6, 12).
		Radius(999).
		Background(bg).
		Border(1, borderCol).
		Justify(ui.Center).
		AlignItems(ui.Center)
	btn.Children(func() {
		ui.Text(c, text).FontSize(12).FontWeight(600).TextColor(txtCol).SingleLine()
	})
	return btn
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
