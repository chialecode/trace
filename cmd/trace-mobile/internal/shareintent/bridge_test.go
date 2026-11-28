package shareintent

import (
	"errors"
	"strings"
	"sync"
	"testing"
)

type fakeReceiver struct {
	mu       sync.Mutex
	onShared func(string)
	stopped  bool
	closed   bool
}

func (f *fakeReceiver) Start(onShared func(string)) {
	f.mu.Lock()
	f.onShared = onShared
	f.mu.Unlock()
}

func (f *fakeReceiver) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.onShared = nil
	f.mu.Unlock()
}

func (f *fakeReceiver) Close() error {
	f.mu.Lock()
	f.closed = true
	f.onShared = nil
	f.mu.Unlock()
	return nil
}

func (f *fakeReceiver) emit(s string) {
	f.mu.Lock()
	cb := f.onShared
	f.mu.Unlock()
	if cb != nil {
		cb(s)
	}
}

func TestOpenDeliversNormalizedShareTextAndIgnoresEmpty(t *testing.T) {
	fake := &fakeReceiver{}
	SetReceiverFactoryForTest(func() (Receiver, error) { return fake, nil })
	defer SetReceiverFactoryForTest(nil)

	var got []string
	rec, err := Open(func(text string) {
		got = append(got, text)
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rec.Close() }()

	fake.emit("   \n\t  ")
	fake.emit("  https://www.bilibili.com/video/BV1TEST \x00 ")
	if len(got) != 1 || got[0] != "https://www.bilibili.com/video/BV1TEST" {
		t.Fatalf("unexpected delivered share payloads: %v", got)
	}

	huge := strings.Repeat("a", maxPayloadLen+500)
	if n := len(NormalizeSharedText(huge)); n != maxPayloadLen {
		t.Fatalf("expected truncated length %d, got %d", maxPayloadLen, n)
	}
}

func TestOpenDesktopReturnsErrUnsupported(t *testing.T) {
	SetReceiverFactoryForTest(nil)
	rec, err := Open(func(string) {})
	if !errors.Is(err, errors.ErrUnsupported) || rec != nil {
		t.Fatalf("expected (nil, ErrUnsupported) on non-Android, got (%v, %v)", rec, err)
	}
}
