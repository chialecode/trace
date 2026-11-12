package site

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"testing"
)

func withSystemProxy(t *testing.T, fn func(*http.Request) (*url.URL, error)) {
	t.Helper()
	prev := systemProxy
	systemProxy = fn
	t.Cleanup(func() {
		systemProxy = prev
		_ = SetProxy(ProxySystem, "")
	})
}

// DEC-046 ⑤: follow the system (default), no proxy, or a custom address;
// loopback targets always go direct; a switch applies to the next request.
func TestProxyModes(t *testing.T) {
	sys, _ := url.Parse("http://10.1.2.3:8080")
	withSystemProxy(t, func(*http.Request) (*url.URL, error) { return sys, nil })
	req, _ := http.NewRequest(http.MethodGet, "https://api.bilibili.com/x", nil)
	local, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:9000/x", nil)

	if ProxyMode() != ProxySystem {
		t.Fatalf("default mode %q, want system", ProxyMode())
	}
	if got, _ := Proxy(req); got == nil || got.Host != "10.1.2.3:8080" {
		t.Fatalf("system mode proxy = %v", got)
	}
	if got, _ := Proxy(local); got != nil {
		t.Fatalf("loopback target proxied: %v", got)
	}
	if err := SetProxy(ProxyNone, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := Proxy(req); got != nil {
		t.Fatalf("none mode proxy = %v", got)
	}
	if err := SetProxy(ProxyCustom, "socks5://127.0.0.1:1080"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Proxy(req); got == nil || got.String() != "socks5://127.0.0.1:1080" {
		t.Fatalf("custom proxy = %v", got)
	}
	if err := SetProxy(ProxyCustom, "127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Proxy(req); got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("custom proxy without scheme = %v", got)
	}
	for _, bad := range []string{"", "ftp://h:1", "http://host", "http://h:1/path", "socks4://h:1", "http://user:pass@h:1", "socks5://user@h:1"} {
		if err := SetProxy(ProxyCustom, bad); err == nil {
			t.Fatalf("custom proxy %q accepted", bad)
		}
	}
	if got, _ := Proxy(req); got == nil || got.Port() != "7890" {
		t.Fatalf("a rejected address changed the proxy in use: %v", got)
	}
	if err := SetProxy("bogus", ""); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

// The media guard dials a custom local proxy as the user configured it and
// still refuses media hosts that resolve to private addresses.
func TestGuardedMediaDialerTrustsCustomProxy(t *testing.T) {
	withSystemProxy(t, func(*http.Request) (*url.URL, error) { return nil, nil })
	if err := SetProxy(ProxyCustom, "http://127.0.0.1:7890"); err != nil {
		t.Fatal(err)
	}
	g := NewGuardedMediaDialer(Douyin, false, &net.Dialer{}, "blocked")
	g.resolver = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "inner.example" {
			return []net.IPAddr{{IP: net.ParseIP("192.168.1.5")}}, nil
		}
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.9")}}, nil
	}
	req, _ := http.NewRequest(http.MethodGet, "https://cdn.example/v.mp4", nil)
	p, err := g.Proxy(req)
	if err != nil || p == nil || p.Host != "127.0.0.1:7890" {
		t.Fatalf("guard proxy = %v, %v", p, err)
	}
	if _, ok := g.proxyAddrs.Load("127.0.0.1:7890"); !ok {
		t.Fatal("the configured proxy is not trusted for dialing")
	}
	inner, _ := http.NewRequest(http.MethodGet, "https://inner.example/v.mp4", nil)
	if _, err := g.Proxy(inner); err == nil {
		t.Fatal("a private media host passed through the proxy")
	}
}
