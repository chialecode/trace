package site

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"
)

func newTestGuard(t *testing.T, proxy string, hostIPs map[string]string) *GuardedMediaDialer {
	t.Helper()
	g := NewGuardedMediaDialer(Douyin, false, &net.Dialer{Timeout: 2 * time.Second}, "blocked")
	g.proxyFunc = func(*http.Request) (*url.URL, error) {
		if proxy == "" {
			return nil, nil
		}
		return url.Parse(proxy)
	}
	g.resolver = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if lit := net.ParseIP(host); lit != nil {
			return []net.IPAddr{{IP: lit}}, nil
		}
		ip, ok := hostIPs[host]
		if !ok {
			return nil, &net.DNSError{Err: "not found", Name: host, IsNotFound: true}
		}
		return []net.IPAddr{{IP: net.ParseIP(ip)}}, nil
	}
	return g
}

// A local proxy client (HTTP_PROXY=http://127.0.0.1:<port>) must be dialable;
// before the fix every Douyin media download through it failed as "blocked".
func TestGuardedMediaDialerTrustsConfiguredLocalProxy(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		if c, err := ln.Accept(); err == nil {
			_ = c.Close()
		}
	}()
	g := newTestGuard(t, "http://"+ln.Addr().String(), map[string]string{"v26-web.douyinvod.com": "198.18.0.183"})
	req, _ := http.NewRequest(http.MethodGet, "https://v26-web.douyinvod.com/v.mp4", nil)
	if _, err := g.Proxy(req); err != nil {
		t.Fatalf("Proxy() for a public media host: %v", err)
	}
	conn, err := g.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dialing the configured proxy: %v", err)
	}
	_ = conn.Close()
}

func TestGuardedMediaDialerBlocksPrivateHostBehindProxy(t *testing.T) {
	g := newTestGuard(t, "http://127.0.0.1:7897", map[string]string{"evil.douyinvod.com": "10.0.0.5"})
	req, _ := http.NewRequest(http.MethodGet, "https://evil.douyinvod.com/v.mp4", nil)
	if _, err := g.Proxy(req); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("Proxy() for a host resolving to 10.0.0.5 = %v, want blocked", err)
	}
	// The proxy was not recorded, so a direct dial to it stays refused.
	if _, err := g.DialContext(context.Background(), "tcp", "127.0.0.1:7897"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("dial to an unrecorded loopback address = %v, want blocked", err)
	}
}

func TestGuardedMediaDialerBlocksDirectLoopback(t *testing.T) {
	g := newTestGuard(t, "", map[string]string{"cdn.douyinvod.com": "127.0.0.1"})
	if _, err := g.DialContext(context.Background(), "tcp", "cdn.douyinvod.com:443"); !IsKind(err, ErrKindBlocked) {
		t.Fatalf("direct dial to a loopback media host = %v, want blocked", err)
	}
}

func TestCanonicalProxyAddr(t *testing.T) {
	for raw, want := range map[string]string{
		"http://127.0.0.1:7897": "127.0.0.1:7897",
		"http://LocalHost":      "localhost:80",
		"https://proxy.example": "proxy.example:443",
		"socks5://127.0.0.1":    "127.0.0.1:1080",
		"http://[::1]:7890/":    "[::1]:7890",
	} {
		u, _ := url.Parse(raw)
		if got := canonicalProxyAddr(u); got != want {
			t.Errorf("canonicalProxyAddr(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestBlockedAddressExtendedRangesAndFakeIPAllowed(t *testing.T) {
	for _, blocked := range []string{
		"127.0.0.1",
		"10.0.0.1",
		"169.254.169.254",
		"100.64.0.1",
		"100.127.255.254",
		"0.1.2.3",
		"224.0.0.1",
		"64:ff9b::127.0.0.1",
		"::ffff:127.0.0.1",
		"ff02::1",
	} {
		if !blockedAddress([]net.IPAddr{{IP: net.ParseIP(blocked)}}) {
			t.Errorf("expected %s to be blocked", blocked)
		}
	}
	for _, allowed := range []string{"198.18.0.183", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if blockedAddress([]net.IPAddr{{IP: net.ParseIP(allowed)}}) {
			t.Errorf("expected %s to be allowed", allowed)
		}
	}
}

func TestGuardedMediaDialerBlocksPrivateXMediaHosts(t *testing.T) {
	g := NewGuardedMediaDialer(X, false, &net.Dialer{Timeout: 2 * time.Second}, "blocked")
	g.proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	g.resolver = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "pbs.twimg.com":
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		case "video.twimg.com":
			return []net.IPAddr{{IP: net.ParseIP("10.0.0.8")}}, nil
		default:
			return nil, &net.DNSError{Err: "not found", Name: host, IsNotFound: true}
		}
	}
	for _, addr := range []string{"pbs.twimg.com:443", "video.twimg.com:443"} {
		_, err := g.DialContext(context.Background(), "tcp", addr)
		if !IsKind(err, ErrKindBlocked) || SiteOf(err) != X {
			t.Fatalf("DialContext(%q) err = %v, want site=x kind=blocked", addr, err)
		}
	}
}

func TestGuardedMediaDialerCachesAllowedProxyDNSAndInvalidatesOnProxyChange(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var lookups int
	var resolveIP = "93.184.216.34"

	g := NewGuardedMediaDialer(Bilibili, false, &net.Dialer{Timeout: 2 * time.Second}, "blocked")
	g.nowFunc = func() time.Time { return now }
	g.proxyFunc = func(*http.Request) (*url.URL, error) {
		return url.Parse("http://127.0.0.1:7897")
	}
	g.resolver = func(_ context.Context, host string) ([]net.IPAddr, error) {
		lookups++
		return []net.IPAddr{{IP: net.ParseIP(resolveIP)}}, nil
	}

	req, _ := http.NewRequest(http.MethodGet, "https://cn-edge.bilivideo.com/live/seg1.m4s", nil)
	if _, err := g.Proxy(req); err != nil {
		t.Fatalf("first Proxy() failed: %v", err)
	}
	if lookups != 1 {
		t.Fatalf("lookups after first Proxy() = %d, want 1", lookups)
	}

	// Within 30s TTL: cache hit, no extra DNS lookup.
	now = now.Add(15 * time.Second)
	if _, err := g.Proxy(req); err != nil {
		t.Fatalf("cached Proxy() failed: %v", err)
	}
	if lookups != 1 {
		t.Fatalf("lookups within TTL = %d, want 1", lookups)
	}

	// After 30s TTL: cache expires and re-resolves.
	now = now.Add(20 * time.Second)
	if _, err := g.Proxy(req); err != nil {
		t.Fatalf("post-TTL Proxy() failed: %v", err)
	}
	if lookups != 2 {
		t.Fatalf("lookups after TTL = %d, want 2", lookups)
	}

	// Changing proxyGen invalidates the cache immediately even inside TTL.
	proxyGen.Add(1)
	if _, err := g.Proxy(req); err != nil {
		t.Fatalf("post-proxyGen Proxy() failed: %v", err)
	}
	if lookups != 3 {
		t.Fatalf("lookups after proxyGen bump = %d, want 3", lookups)
	}

	// Blocked hosts are never cached.
	resolveIP = "10.0.0.8"
	reqBlocked, _ := http.NewRequest(http.MethodGet, "https://blocked.bilivideo.com/live/seg1.m4s", nil)
	for i := 1; i <= 2; i++ {
		if _, err := g.Proxy(reqBlocked); !IsKind(err, ErrKindBlocked) {
			t.Fatalf("blocked Proxy() call %d = %v, want blocked", i, err)
		}
	}
	if lookups != 5 {
		t.Fatalf("blocked host should never be cached: lookups = %d, want 5", lookups)
	}
}

func TestGuardedMediaDialerCapsPerIPTimeoutOnMultiIPHosts(t *testing.T) {
	// Verify that when a host resolves to multiple IPs and the first IP stalls,
	// per-IP deadline capping leaves budget to dial subsequent IPs rather than
	// consuming the entire context deadline on the first dead address.
	g := NewGuardedMediaDialer(Bilibili, false, &net.Dialer{Timeout: 5 * time.Second}, "blocked")
	g.proxyFunc = func(*http.Request) (*url.URL, error) { return nil, nil }
	// Use two TEST-NET-1 addresses (192.0.2.x, public non-private in netip) so
	// blockedAddress allows them while DialContext enforces per-IP splitting.
	g.resolver = func(_ context.Context, _ string) ([]net.IPAddr, error) {
		return []net.IPAddr{
			{IP: net.ParseIP("192.0.2.1")},
			{IP: net.ParseIP("192.0.2.2")},
		}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := g.DialContext(ctx, "tcp", "multi-ip.example:443")
	if err == nil {
		t.Fatal("expected dial error against TEST-NET addresses")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("multi-IP dial took %v, expected bounded completion under context deadline", elapsed)
	}
}
