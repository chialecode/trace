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
