package player

import (
	"errors"
	"image"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo/ui"
)

// ErrMissingCodec indicates the local machine lacks a Media Foundation decoder for the video/audio stream (C22).
var ErrMissingCodec = errors.New("player: missing local media codec (HEVC/AV1 extension required)")

// IsMissingCodecError reports whether err represents a missing hardware/software codec.
func IsMissingCodecError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrMissingCodec) {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "missing") && strings.Contains(lower, "codec") ||
		strings.Contains(lower, "c00d5212") || strings.Contains(lower, "c00d36b4")
}

// Options configures a video/audio/danmaku/subtitle Controller.
type Options struct {
	AudioSink      AudioSink
	DanmakuVisible bool
	DanmakuOpacity float64
	DanmakuArea    float64
	DanmakuDensity string
	Volume         float64
	Muted          bool
	SubtitleLang   string
}

// StateSnapshot exposes the current player state for native UI rendering and tests.
type StateSnapshot struct {
	Open            bool                `json:"open"`
	Playing         bool                `json:"playing"`
	FilePath        string              `json:"filePath,omitempty"`
	Backend         string              `json:"backend"`
	Width           int                 `json:"width"`
	Height          int                 `json:"height"`
	PositionSec     float64             `json:"positionSec"`
	DurationSec     float64             `json:"durationSec"`
	Volume          float64             `json:"volume"`
	Muted           bool                `json:"muted"`
	DanmakuVisible  bool                `json:"danmakuVisible"`
	DanmakuOpacity  float64             `json:"danmakuOpacity"`
	DanmakuArea     float64             `json:"danmakuArea"`
	DanmakuDensity  string              `json:"danmakuDensity"`
	DanmakuCount    int                 `json:"danmakuCount"`
	VisibleDanmaku  []PositionedDanmaku `json:"-"`
	SubtitleLang    string              `json:"subtitleLang"`
	SubtitleOptions []string            `json:"subtitleOptions,omitempty"`
	ActiveSubtitle  string              `json:"activeSubtitle"`
}

// Controller manages local video playback, A/V clock synchronization, danmaku overlay, and subtitle tracks.
type Controller struct {
	mu             sync.Mutex
	dec            MediaDecoder
	sink           AudioSink
	configuredSink AudioSink
	filePath       string
	bitmap         *ui.Bitmap
	bmpW           int
	bmpH           int
	playing        bool
	position       time.Duration
	volume         float64
	muted          bool
	danmakuVisible bool
	danmakuOpacity float64
	danmakuArea    float64
	danmakuDensity string
	danmakuEngine  *DanmakuEngine
	subtitles      map[string]SubtitleTrack
	subOrder       []string
	activeSubLang  string

	// Video frames are decoded by worker off the UI thread (F-043); seekGen
	// discards frames decoded for a position the user has left.
	worker  *videoWorker
	seekGen uint64
	onFrame func()

	// scrubbing: the progress bar is being dragged. Audio stays paused and
	// the worker shows the nearest key frame, which decodes at once, until
	// Seek commits the position (F-059).
	scrubbing      bool
	resumeAfterScr bool

	// holdGen: playback resumed after a seek waits, audio held, for the
	// first frame at the new position (or holdUntil), so picture and sound
	// start together instead of the sound running ahead (F-059).
	holdGen   uint64
	holdUntil time.Time
}

const seekHoldLimit = 1500 * time.Millisecond

// audioSeeker moves only a decoder's audio. The audio sink seeks its
// decoder on a goroutine of its own; seeking video there as well would
// pull the video reader back while the video worker reads it.
type audioSeeker interface {
	SeekAudio(pos time.Duration) error
}

// keyFramePreviewer decodes the key frame at or before pos, without
// decoding on to pos: a preview that costs one frame.
type keyFramePreviewer interface {
	PreviewFrameAt(pos time.Duration, dst *image.RGBA) (*image.RGBA, time.Duration, error)
}

// SetFrameNotify registers fn, called from the decode goroutine when a frame
// decoded while paused (after a seek) is ready to show. It should only
// schedule a redraw on the UI thread.
func (c *Controller) SetFrameNotify(fn func()) {
	c.mu.Lock()
	c.onFrame = fn
	c.mu.Unlock()
}

// NewController creates a player Controller. If opts.AudioSink is nil, it uses NullAudioSink when TRACE_QUIET is set,
// or MyGoAudioSink in interactive production mode.
func NewController(opts Options) *Controller {
	sink := opts.AudioSink
	if sink == nil {
		if os.Getenv("TRACE_QUIET") != "" {
			sink = NewNullAudioSink()
		} else {
			sink = NewMyGoAudioSink()
		}
	}
	vol := opts.Volume
	if vol < 0 || vol > 1 {
		vol = 1.0
	}
	op := opts.DanmakuOpacity
	if op <= 0 || op > 1 {
		op = 0.85
	}
	area := opts.DanmakuArea
	if area <= 0 || area > 1 {
		area = 0.5
	}
	return &Controller{
		sink:           sink,
		configuredSink: sink,
		volume:         vol,
		muted:          opts.Muted,
		danmakuVisible: opts.DanmakuVisible,
		danmakuOpacity: op,
		danmakuArea:    area,
		danmakuDensity: NormalizeDanmakuDensity(opts.DanmakuDensity),
		subtitles:      make(map[string]SubtitleTrack),
		activeSubLang:  opts.SubtitleLang,
	}
}

// LoadDecoder attaches an explicit MediaDecoder (used by unit tests and prototypes).
func (c *Controller) LoadDecoder(label string, dec MediaDecoder) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closeLocked()
	c.dec = dec
	c.filePath = label
	c.position = 0
	c.playing = false
	c.scrubbing, c.resumeAfterScr = false, false

	c.sink = c.configuredSink
	if dec.SampleRate() <= 0 {
		c.sink = NewNullAudioSink()
	}
	if err := c.sink.Open(&decoderAudioAdapter{dec: dec}); err != nil {
		c.closeLocked()
		return err
	}
	if c.muted {
		c.sink.SetVolume(0)
	} else {
		c.sink.SetVolume(c.volume)
	}

	// Decode initial frame at t=0.
	rgba, _, err := dec.DecodeVideoFrameAt(0, nil)
	if err != nil {
		c.closeLocked()
		return err
	}
	if rgba != nil {
		c.bitmap = ui.NewBitmap(rgba) // copies: rgba is free again
		c.bmpW = rgba.Bounds().Dx()
		c.bmpH = rgba.Bounds().Dy()
	}
	w := newVideoWorker(dec)
	w.putFree(rgba)
	c.worker = w
	go c.runVideoWorker(w, c.seekGen)
	return nil
}

// LoadDanmakuXML loads a Bilibili XML danmaku file or byte payload into the player.
func (c *Controller) LoadDanmakuXML(xmlBytes []byte) error {
	eng, err := ParseDanmakuXML(xmlBytes)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.danmakuEngine = eng
	c.mu.Unlock()
	return nil
}

// SetDanmakuEngine attaches danmaku parsed elsewhere (ParseDanmakuXML), so a
// large file is not parsed on the UI thread.
func (c *Controller) SetDanmakuEngine(eng *DanmakuEngine) {
	c.mu.Lock()
	c.danmakuEngine = eng
	c.mu.Unlock()
}

// SetSubtitles loads WebVTT/SRT subtitle tracks into the player.
func (c *Controller) SetSubtitles(tracks []SubtitleTrack) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.subtitles = make(map[string]SubtitleTrack, len(tracks))
	c.subOrder = make([]string, 0, len(tracks))
	for _, t := range tracks {
		c.subtitles[t.Lang] = t
		c.subOrder = append(c.subOrder, t.Lang)
	}

}

// Play starts or resumes playback.
func (c *Controller) Play() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dec == nil {
		return
	}
	if c.scrubbing {
		c.resumeAfterScr = true
		return
	}
	c.holdGen = 0
	c.playing = true
	c.sink.Play()
	c.worker.signal()
}

// Pause pauses playback.
func (c *Controller) Pause() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.playing = false
	c.resumeAfterScr = false
	c.holdGen = 0
	c.sink.Pause()
	if c.worker != nil {
		c.worker.signal()
	}
}

// TogglePlay toggles between Play and Pause.
func (c *Controller) TogglePlay() bool {
	c.mu.Lock()
	playing := c.playing || c.scrubbing && c.resumeAfterScr
	c.mu.Unlock()
	if playing {
		c.Pause()
		return false
	}
	c.Play()
	return true
}

// Seek jumps to sec seconds in the media timeline and updates the video
// frame and overlays (C22). It never touches the decoder on the calling (UI)
// thread: the audio sink seeks the audio on its own goroutine, and the video
// worker seeks the video when it sees the new position (F-059). A drag of
// the progress bar (Preview) ends here, resuming playback if it was playing.
func (c *Controller) Seek(sec float64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dec == nil {
		return nil
	}
	pos := c.clampLocked(sec)
	c.position = pos
	_ = c.sink.Seek(pos)
	resume := c.playing
	if c.scrubbing {
		c.scrubbing = false
		resume = c.resumeAfterScr
		c.resumeAfterScr = false
	}
	c.requestFrameLocked()
	if resume {
		c.playing = true
		c.sink.Pause()
		c.holdGen, c.holdUntil = c.seekGen, time.Now().Add(seekHoldLimit)
	}
	return nil
}

// holdingLocked reports whether playback waits for the frame of the latest
// seek before its audio runs.
func (c *Controller) holdingLocked() bool {
	return c.playing && c.holdGen != 0 && c.holdGen == c.seekGen
}

// releaseHold lets the audio run once the frame of seek gen is ready, or the
// wait ran out.
func (c *Controller) releaseHold(gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.holdingLocked() && c.holdGen == gen {
		c.holdGen = 0
		c.sink.Play()
	}
}

// Preview shows the frame near sec while the progress bar is dragged,
// without seeking the audio: playback pauses until Seek commits a position.
func (c *Controller) Preview(sec float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dec == nil {
		return
	}
	if !c.scrubbing {
		c.scrubbing = true
		c.resumeAfterScr = c.playing
		if c.playing {
			c.playing = false
			c.sink.Pause()
		}
	}
	c.position = c.clampLocked(sec)
	c.requestFrameLocked()
}

// Scrubbing reports whether a Preview awaits its Seek.
func (c *Controller) Scrubbing() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scrubbing
}

func (c *Controller) clampLocked(sec float64) time.Duration {
	pos := time.Duration(sec * float64(time.Second))
	if pos < 0 {
		pos = 0
	}
	if dur := c.dec.Duration(); dur > 0 && pos > dur {
		pos = dur
	}
	return pos
}

// requestFrameLocked has the worker decode the frame at c.position, dropping
// any frame decoded for an earlier position.
func (c *Controller) requestFrameLocked() {
	c.seekGen++
	if c.worker != nil {
		c.worker.signal()
	}
}

// Advance steps the player forward by delta (or syncs with the AudioSink
// clock) and shows the newest frame the decode worker has ready. It never
// decodes: call it from the view, on the UI thread.
func (c *Controller) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dec == nil {
		return
	}
	if delta > 0 {
		c.position += delta
		_ = c.sink.Seek(c.position)
		c.requestFrameLocked()
	} else if c.playing {
		c.position = c.sink.Position()
	}
	if dur := c.dec.Duration(); dur > 0 && c.position >= dur {
		c.position = dur
		c.playing = false
		c.sink.Pause()
	}
	c.showReadyFrameLocked()
}

func (c *Controller) showReadyFrameLocked() {
	w := c.worker
	if w == nil {
		return
	}
	rgba, gen := w.take()
	if rgba == nil {
		return
	}
	if gen != c.seekGen {
		w.putFree(rgba) // decoded for a position the user has left
		return
	}
	fw, fh := rgba.Bounds().Dx(), rgba.Bounds().Dy()
	if c.bitmap == nil || c.bmpW != fw || c.bmpH != fh {
		c.bitmap = ui.NewBitmap(rgba)
		c.bmpW, c.bmpH = fw, fh
	} else {
		c.bitmap.Update(rgba) // keeps rgba until the next Update
	}
	w.showing(rgba)
}

// SetVolume updates audio volume [0.0..1.0].
func (c *Controller) SetVolume(v float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if v < 0 {
		v = 0
	} else if v > 1 {
		v = 1
	}
	c.volume = v
	if !c.muted {
		c.sink.SetVolume(v)
	}
}

// SetMuted toggles audio mute.
func (c *Controller) SetMuted(muted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.muted = muted
	if muted {
		c.sink.SetVolume(0)
	} else {
		c.sink.SetVolume(c.volume)
	}
}

// SetDanmakuVisible toggles danmaku overlay visibility.
func (c *Controller) SetDanmakuVisible(vis bool) {
	c.mu.Lock()
	c.danmakuVisible = vis
	c.mu.Unlock()
}

// SetDanmakuOpacity sets danmaku opacity [0.1..1.0].
func (c *Controller) SetDanmakuOpacity(op float64) {
	c.mu.Lock()
	if op >= 0.1 && op <= 1.0 {
		c.danmakuOpacity = op
	}
	c.mu.Unlock()
}

// SetDanmakuArea sets danmaku screen area ratio (0.25, 0.5, 0.75, 1.0).
func (c *Controller) SetDanmakuArea(area float64) {
	c.mu.Lock()
	if area >= 0.25 && area <= 1.0 {
		c.danmakuArea = area
	}
	c.mu.Unlock()
}

func (c *Controller) SetDanmakuDensity(density string) {
	c.mu.Lock()
	c.danmakuDensity = NormalizeDanmakuDensity(density)
	c.mu.Unlock()
}

// SetSubtitleLang selects the active subtitle language ("" disables subtitles).
func (c *Controller) SetSubtitleLang(lang string) {
	c.mu.Lock()
	c.activeSubLang = lang
	c.mu.Unlock()
}

// Bitmap returns the current video frame *ui.Bitmap for rendering in ui.Image.
func (c *Controller) Bitmap() *ui.Bitmap {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bitmap
}

// Snapshot returns the current playback state, visible danmaku layout, and active subtitle cue.
func (c *Controller) Snapshot(viewportW, viewportH float32) StateSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.dec == nil {
		return StateSnapshot{
			Volume:         c.volume,
			Muted:          c.muted,
			DanmakuVisible: c.danmakuVisible,
			DanmakuOpacity: c.danmakuOpacity,
			DanmakuArea:    c.danmakuArea,
			DanmakuDensity: c.danmakuDensity,
		}
	}

	posSec := c.position.Seconds()
	var visibleDm []PositionedDanmaku
	dmCount := 0
	if c.danmakuEngine != nil {
		dmCount = c.danmakuEngine.Count()
		if c.danmakuVisible {
			visibleDm = c.danmakuEngine.LayoutFrame(posSec, viewportW, viewportH, c.danmakuArea, c.danmakuOpacity, c.danmakuDensity)
		}
	}

	activeSub := ""
	if c.activeSubLang != "" {
		if track, ok := c.subtitles[c.activeSubLang]; ok {
			activeSub = ActiveCueAt(track.Cues, posSec)
		}
	}

	subOpts := append([]string(nil), c.subOrder...)
	return StateSnapshot{
		Open:            true,
		Playing:         c.playing,
		FilePath:        c.filePath,
		Backend:         c.dec.BackendName(),
		Width:           c.dec.Width(),
		Height:          c.dec.Height(),
		PositionSec:     posSec,
		DurationSec:     c.dec.Duration().Seconds(),
		Volume:          c.volume,
		Muted:           c.muted,
		DanmakuVisible:  c.danmakuVisible,
		DanmakuOpacity:  c.danmakuOpacity,
		DanmakuArea:     c.danmakuArea,
		DanmakuDensity:  c.danmakuDensity,
		DanmakuCount:    dmCount,
		VisibleDanmaku:  visibleDm,
		SubtitleLang:    c.activeSubLang,
		SubtitleOptions: subOpts,
		ActiveSubtitle:  activeSub,
	}
}

// Close stops playback, closes the decoder and audio sink, and releases all frame buffers (C22).
func (c *Controller) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeLocked()
	return nil
}

func (c *Controller) closeLocked() {
	c.playing = false
	c.scrubbing, c.resumeAfterScr = false, false
	c.holdGen = 0
	if c.worker != nil {
		// The worker exits at its next check; Close below waits for a
		// decode in progress through the decoder's own lock.
		close(c.worker.stop)
		c.worker = nil
	}
	if c.sink != nil {
		_ = c.sink.Close()
	}
	if c.dec != nil {
		_ = c.dec.Close()
		c.dec = nil
	}
	c.bitmap = nil
	c.bmpW = 0
	c.bmpH = 0
	c.danmakuEngine = nil
	c.subtitles = make(map[string]SubtitleTrack)
	c.subOrder = nil
	c.filePath = ""
	c.position = 0
}

type decoderAudioAdapter struct {
	dec MediaDecoder
}

func (a *decoderAudioAdapter) SampleRate() int {
	if a.dec == nil {
		return 48000
	}
	return a.dec.SampleRate()
}

func (a *decoderAudioAdapter) Channels() int {
	if a.dec == nil {
		return 2
	}
	return a.dec.Channels()
}

func (a *decoderAudioAdapter) Len() int64 {
	if a.dec == nil {
		return 0
	}
	return int64(a.dec.Duration().Seconds() * float64(a.dec.SampleRate()))
}

func (a *decoderAudioAdapter) SetPosition(frame int64) error {
	if a.dec == nil || a.dec.SampleRate() <= 0 {
		return nil
	}
	pos := time.Duration(float64(frame) / float64(a.dec.SampleRate()) * float64(time.Second))
	if s, ok := a.dec.(audioSeeker); ok {
		return s.SeekAudio(pos)
	}
	return a.dec.Seek(pos)
}

func (a *decoderAudioAdapter) Read(buf []float32) (int, error) {
	if a.dec == nil {
		return 0, nil
	}
	return a.dec.ReadAudioSamples(buf)
}
