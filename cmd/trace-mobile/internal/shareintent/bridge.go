package shareintent

import (
	"errors"
	"strings"
	"sync"
)

const maxPayloadLen = 16384

// ReceiverFactory creates a Receiver; replaced in tests on non-Android platforms.
type ReceiverFactory func() (Receiver, error)

var (
	factoryMu     sync.RWMutex
	customFactory ReceiverFactory
)

// SetReceiverFactoryForTest overrides NewReceiver for unit testing (pass nil to restore).
func SetReceiverFactoryForTest(fn ReceiverFactory) {
	factoryMu.Lock()
	customFactory = fn
	factoryMu.Unlock()
}

// Open starts listening for Android ACTION_SEND text/plain share intents.
// Returns (nil, errors.ErrUnsupported) on desktop/non-Android when no test hook is installed.
func Open(onShared func(text string)) (Receiver, error) {
	factoryMu.RLock()
	fn := customFactory
	factoryMu.RUnlock()

	var (
		rec Receiver
		err error
	)
	if fn != nil {
		rec, err = fn()
	} else {
		rec, err = NewReceiver()
	}
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return nil, err
		}
		return nil, err
	}
	rec.Start(func(raw string) {
		cleaned := NormalizeSharedText(raw)
		if cleaned != "" && onShared != nil {
			onShared(cleaned)
		}
	})
	return rec, nil
}

// NormalizeSharedText trims whitespace, strips NUL bytes, and caps oversized share payloads.
func NormalizeSharedText(raw string) string {
	s := strings.ReplaceAll(raw, "\x00", "")
	s = strings.TrimSpace(s)
	if len(s) > maxPayloadLen {
		s = s[:maxPayloadLen]
	}
	return s
}
