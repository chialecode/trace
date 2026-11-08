//go:build !windows

package player

// Production media loading reports unsupported platforms explicitly. Synthetic
// decoders are only selected by tests through LoadDecoder.
func OpenMediaFile(path string) (MediaDecoder, error) { return nil, ErrMissingCodec }
