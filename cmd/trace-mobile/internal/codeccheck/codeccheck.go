// Package codeccheck queries Android's MediaCodecList for video/audio decoder
// capabilities before starting playback (FR-MOB-014, docs/design/mobile.md §6 & §9).
// On non-Android platforms (desktop/tests), NewChecker returns errors.ErrUnsupported
// and CheckTrack gracefully reports Known=false so desktop builds need no cgo.
package codeccheck

// Checker queries the platform media codec registry for decoder support.
//
//mygo:native
type Checker interface {
	// CanDecode reports whether the device has a decoder supporting the given
	// MIME type (e.g. "video/av01", "video/hevc", "video/avc", "audio/flac").
	// When width > 0 and height > 0 for video MIME types, the decoder's
	// supported frame size range is also checked.
	CanDecode(mime string, width, height int) (bool, error)
	// Close releases resources held by the checker.
	Close() error
}
