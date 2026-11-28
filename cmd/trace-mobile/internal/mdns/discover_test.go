package mdns

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeBrowser struct {
	mu      sync.Mutex
	onStart func(onFound func(host string, port int, node, channel string), onError func(msg string))
	stopped bool
	closed  bool
}

func (f *fakeBrowser) Start(_ string, onFound func(host string, port int, node, channel string), onError func(msg string)) {
	f.mu.Lock()
	fn := f.onStart
	f.mu.Unlock()
	if fn != nil {
		go fn(onFound, onError)
	}
}

func (f *fakeBrowser) Stop() {
	f.mu.Lock()
	f.stopped = true
	f.mu.Unlock()
}

func (f *fakeBrowser) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func TestIsAllowedLANHostRejectsPublicLoopbackAndMulticast(t *testing.T) {
	allowed := []string{
		"192.168.1.42",
		"10.0.0.5",
		"172.16.20.3",
		"100.64.12.34",
		"169.254.10.20",
		"fd12:3456:789a::1",
		"fe80::1%wlan0",
	}
	for _, h := range allowed {
		if !IsAllowedLANHost(h) {
			t.Fatalf("expected %q to be allowed LAN host", h)
		}
	}

	rejected := []string{
		"",
		"not-an-ip",
		"8.8.8.8",
		"1.1.1.1",
		"127.0.0.1",
		"::1",
		"0.0.0.0",
		"224.0.0.251",
		"2001:4860:4860::8888",
	}
	for _, h := range rejected {
		if IsAllowedLANHost(h) {
			t.Fatalf("expected %q to be rejected", h)
		}
	}
}

func TestDiscoverMatchingAddrsFiltersNodeChannelAndOrdersIPv4First(t *testing.T) {
	fb := &fakeBrowser{
		onStart: func(onFound func(host string, port int, node, channel string), _ func(string)) {
			// Wrong node -> ignored.
			onFound("192.168.1.99", 47382, "other-node", "dev")
			// Wrong channel -> ignored.
			onFound("192.168.1.98", 47381, "node-pc-1", "stable")
			// Public IP -> rejected.
			onFound("8.8.8.8", 47382, "node-pc-1", "dev")
			// Valid IPv6 ULA and IPv4 private (plus duplicate) -> IPv4 preferred first.
			onFound("fd00::20", 47382, "node-pc-1", "dev")
			onFound("192.168.1.55", 47382, "node-pc-1", "dev")
			onFound("192.168.1.55", 47382, "node-pc-1", "dev")
			onFound("100.88.1.2", 47382, "node-pc-1", "dev")
		},
	}
	SetBrowserFactoryForTest(func() (Browser, error) { return fb, nil })
	defer SetBrowserFactoryForTest(nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	addrs, err := DiscoverMatchingAddrs(ctx, "node-pc-1", "dev")
	if err != nil {
		t.Fatalf("DiscoverMatchingAddrs: %v", err)
	}
	want := []string{"192.168.1.55:47382", "100.88.1.2:47382", "[fd00::20]:47382"}
	if !slices.Equal(addrs, want) {
		t.Fatalf("got %v, want %v", addrs, want)
	}
	fb.mu.Lock()
	stopped, closed := fb.stopped, fb.closed
	fb.mu.Unlock()
	if !stopped || !closed {
		t.Fatalf("expected browser stopped & closed after discovery; stopped=%v closed=%v", stopped, closed)
	}
}
