package player

import (
	"sync"
	"time"

	"github.com/egoist/mygo/audio"
)

// AudioSink abstracts audio playback so automated tests, prototypes, uishots, and smoke
// use NullAudioSink without ever opening a hardware audio device or emitting sound (§6.1).
type AudioSink interface {
	Open(dec audio.Decoder) error
	Play()
	Pause()
	Seek(pos time.Duration) error
	SetVolume(vol float64)
	Position() time.Duration
	IsPlaying() bool
	Close() error
}

// NullAudioSink advances virtual playback position and drains PCM samples silently in memory.
// It never calls audio.NewPlayer and never touches OS audio hardware.
type NullAudioSink struct {
	mu         sync.Mutex
	dec        audio.Decoder
	sampleRate int
	channels   int
	playing    bool
	volume     float64
	basePos    time.Duration
	startedAt  time.Time
	NowFunc    func() time.Time
}

// NewNullAudioSink constructs a silent in-memory AudioSink for tests and quiet mode.
func NewNullAudioSink() *NullAudioSink {
	return &NullAudioSink{
		volume:  1.0,
		NowFunc: time.Now,
	}
}

func (s *NullAudioSink) Open(dec audio.Decoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dec = dec
	if dec != nil {
		s.sampleRate = dec.SampleRate()
		s.channels = dec.Channels()
	}
	if s.sampleRate <= 0 {
		s.sampleRate = 48000
	}
	if s.channels <= 0 {
		s.channels = 2
	}
	s.basePos = 0
	s.playing = false
	return nil
}

func (s *NullAudioSink) Play() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.playing {
		s.startedAt = s.now()
		s.playing = true
	}
}

func (s *NullAudioSink) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.playing {
		s.basePos += s.now().Sub(s.startedAt)
		s.playing = false
	}
}

func (s *NullAudioSink) Seek(pos time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if pos < 0 {
		pos = 0
	}
	s.basePos = pos
	if s.playing {
		s.startedAt = s.now()
	}
	if s.dec != nil && s.sampleRate > 0 {
		framePos := int64(pos.Seconds() * float64(s.sampleRate))
		_ = s.dec.SetPosition(framePos)
	}
	return nil
}

func (s *NullAudioSink) SetVolume(vol float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if vol < 0 {
		vol = 0
	} else if vol > 1 {
		vol = 1
	}
	s.volume = vol
}

func (s *NullAudioSink) Position() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.playing {
		return s.basePos
	}
	return s.basePos + s.now().Sub(s.startedAt)
}

func (s *NullAudioSink) IsPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playing
}

func (s *NullAudioSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.playing = false
	s.dec = nil
	s.basePos = 0
	return nil
}

func (s *NullAudioSink) now() time.Time {
	if s.NowFunc != nil {
		return s.NowFunc()
	}
	return time.Now()
}

// MyGoAudioSink wraps github.com/egoist/mygo/audio.Player for real interactive user playback.
type MyGoAudioSink struct {
	mu         sync.Mutex
	player     *audio.Player
	sampleRate int
}

// NewMyGoAudioSink creates a hardware AudioSink backed by MyGo's audio.Player.
func NewMyGoAudioSink() *MyGoAudioSink {
	return &MyGoAudioSink{}
}

func (s *MyGoAudioSink) Open(dec audio.Decoder) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.Close()
		s.player = nil
	}
	s.player = audio.NewPlayer(dec)
	s.sampleRate = dec.SampleRate()
	if s.sampleRate <= 0 {
		s.sampleRate = 48000
	}
	return nil
}

func (s *MyGoAudioSink) Play() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.Play()
	}
}

func (s *MyGoAudioSink) Pause() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.Pause()
	}
}

func (s *MyGoAudioSink) Seek(pos time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player == nil {
		return nil
	}
	s.player.Seek(pos)
	return nil
}

func (s *MyGoAudioSink) SetVolume(vol float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.SetVolume(vol)
	}
}

func (s *MyGoAudioSink) Position() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player == nil {
		return 0
	}
	return s.player.Position()
}

func (s *MyGoAudioSink) IsPlaying() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.player != nil && s.player.Playing()
}

func (s *MyGoAudioSink) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.player != nil {
		s.player.Close()
		s.player = nil
	}
	return nil
}
