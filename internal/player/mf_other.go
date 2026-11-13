//go:build !windows

package player

// OpenMediaFile opens pure audio formats registered with mygo/audio on
// non-Windows builds and reports ErrMissingCodec for unsupported containers.
func OpenMediaFile(path string) (MediaDecoder, error) {
	if dec, err := openPureAudioFile(path); err == nil {
		return dec, nil
	}
	return nil, ErrMissingCodec
}
