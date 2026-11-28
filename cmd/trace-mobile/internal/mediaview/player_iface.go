package mediaview

import (
	"errors"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/videoplayer"
	"github.com/egoist/mygo/ui"
)

// PlaybackHandle abstracts plugins/videoplayer.Player so unit tests can drive
// playback state transitions without requiring an Android device.
type PlaybackHandle interface {
	Play()
	Pause()
	Seek(to time.Duration)
	SetVolume(v float64)
	State() videoplayer.State
	OnState(fn func(videoplayer.State))
	Destroy()
	NativeView() ui.NativeWebView
}

type realPlayer struct {
	p *videoplayer.Player
}

func (r realPlayer) Play()                              { r.p.Play() }
func (r realPlayer) Pause()                             { r.p.Pause() }
func (r realPlayer) Seek(to time.Duration)              { r.p.Seek(to) }
func (r realPlayer) SetVolume(v float64)                { r.p.SetVolume(v) }
func (r realPlayer) State() videoplayer.State           { return r.p.State() }
func (r realPlayer) OnState(fn func(videoplayer.State)) { r.p.OnState(fn) }
func (r realPlayer) Destroy()                           { r.p.Destroy() }
func (r realPlayer) NativeView() ui.NativeWebView       { return r.p }

// PlayerFactory constructs a PlaybackHandle for a window, source URL, and options.
type PlayerFactory func(w *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error)

// LivePreviewTicketIssuer mints a loopback URL + ticket for an active live recording preview.
type LivePreviewTicketIssuer func(proxy *companionclient.MediaProxy, recordingKey string) (string, string, error)

var (
	factoryMu         sync.RWMutex
	customFactory     PlayerFactory
	customOrientation func(mygo.Orientation)
	customLivePreview LivePreviewTicketIssuer

	activeMu      sync.Mutex
	activeClosers = map[any]func(){}
)

// SetLivePreviewTicketIssuerForTest overrides live preview ticket minting for tests (pass nil to restore).
func SetLivePreviewTicketIssuerForTest(fn LivePreviewTicketIssuer) {
	factoryMu.Lock()
	customLivePreview = fn
	factoryMu.Unlock()
}

func issueLivePreviewTicket(proxy *companionclient.MediaProxy, recordingKey string) (string, string, error) {
	factoryMu.RLock()
	fn := customLivePreview
	factoryMu.RUnlock()
	if fn != nil {
		return fn(proxy, recordingKey)
	}
	if proxy == nil || recordingKey == "" {
		return "", "", errors.New("当前无法建立直播实时预览通道")
	}
	if u, tk, err := proxy.IssueLivePreviewHLSTicket(recordingKey); err == nil {
		return u, tk, nil
	}
	return proxy.IssueLivePreviewTicket(recordingKey)
}

// SetPlayerFactoryForTest replaces videoplayer.New for unit testing (pass nil to restore).
func SetPlayerFactoryForTest(fn PlayerFactory) {
	factoryMu.Lock()
	customFactory = fn
	factoryMu.Unlock()
}

// SetOrientationSetterForTest replaces mygo.SetOrientations for unit testing (pass nil to restore).
func SetOrientationSetterForTest(fn func(mygo.Orientation)) {
	factoryMu.Lock()
	customOrientation = fn
	factoryMu.Unlock()
}

func newPlaybackHandle(w *mygo.Window, source string, opts videoplayer.Options) (PlaybackHandle, error) {
	factoryMu.RLock()
	fn := customFactory
	factoryMu.RUnlock()
	if fn != nil {
		return fn(w, source, opts)
	}
	if w == nil {
		return nil, errors.ErrUnsupported
	}
	p, err := videoplayer.New(w, source, opts)
	if err != nil {
		return nil, err
	}
	return realPlayer{p: p}, nil
}

func setOrientations(o mygo.Orientation) {
	factoryMu.RLock()
	fn := customOrientation
	factoryMu.RUnlock()
	if fn != nil {
		fn(o)
		return
	}
	mygo.SetOrientations(o)
}

func registerActive(key any, closeFn func()) {
	activeMu.Lock()
	activeClosers[key] = closeFn
	activeMu.Unlock()
}

func unregisterActive(key any) {
	activeMu.Lock()
	delete(activeClosers, key)
	activeMu.Unlock()
}

// CloseAllActive immediately stops and closes every open image viewer and video
// player, revoking loopback tickets and resetting screen orientation (FR-MOB-006).
func CloseAllActive() {
	activeMu.Lock()
	closers := make([]func(), 0, len(activeClosers))
	for k, fn := range activeClosers {
		closers = append(closers, fn)
		delete(activeClosers, k)
	}
	activeMu.Unlock()
	for _, fn := range closers {
		if fn != nil {
			fn()
		}
	}
}
