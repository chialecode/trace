package site

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// GuardedMediaDialer holds the Proxy and DialContext pair for a media CDN
// transport. Media hosts must not resolve to loopback, private, link-local or
// unspecified addresses. The proxy the user chose (Settings → 网络: the
// system's, HTTP(S)_PROXY or a custom address, usually a local 127.0.0.1
// client) is trusted and dialed as is; requests sent through it still have their media host checked against
// local DNS first.
type GuardedMediaDialer struct {
	site       ID
	allowLocal bool
	dialer     *net.Dialer
	message    string
	proxyFunc  func(*http.Request) (*url.URL, error)
	resolver   func(ctx context.Context, host string) ([]net.IPAddr, error)
	proxyAddrs sync.Map // canonical host:port of proxies returned by proxyFunc
}

// NewGuardedMediaDialer returns a guard for site using the proxy chosen in
// Settings → 网络 (Proxy).
// message is the user-facing text when an address is refused.
func NewGuardedMediaDialer(siteID ID, allowLocalhost bool, dialer *net.Dialer, message string) *GuardedMediaDialer {
	return &GuardedMediaDialer{
		site:       siteID,
		allowLocal: allowLocalhost,
		dialer:     dialer,
		message:    message,
		proxyFunc:  Proxy,
		resolver:   net.DefaultResolver.LookupIPAddr,
	}
}

// Proxy is the http.Transport Proxy hook.
func (g *GuardedMediaDialer) Proxy(req *http.Request) (*url.URL, error) {
	proxyURL, err := g.proxyFunc(req)
	if err != nil || proxyURL == nil {
		return proxyURL, err
	}
	if !g.allowLocal {
		// The proxy resolves the media host; check what it resolves to here so a
		// proxy does not become a way around the private-address guard.
		if ips, lerr := g.resolver(req.Context(), req.URL.Hostname()); lerr == nil {
			if blockedAddress(ips) {
				return nil, g.blocked()
			}
		}
	}
	g.proxyAddrs.Store(canonicalProxyAddr(proxyURL), true)
	return proxyURL, nil
}

// DialContext is the http.Transport DialContext hook.
func (g *GuardedMediaDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.allowLocal {
		return g.dialer.DialContext(ctx, network, addr)
	}
	if _, ok := g.proxyAddrs.Load(strings.ToLower(addr)); ok {
		return g.dialer.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := g.resolver(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	if blockedAddress(ips) {
		return nil, g.blocked()
	}
	return g.dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
}

func (g *GuardedMediaDialer) blocked() error {
	return &Error{Site: g.site, Kind: ErrKindBlocked, Message: g.message}
}

func blockedAddress(ips []net.IPAddr) bool {
	for _, ipAddr := range ips {
		ip := ipAddr.IP
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

// canonicalProxyAddr mirrors how net/http picks the address it dials for a proxy.
func canonicalProxyAddr(u *url.URL) string {
	host, port := u.Hostname(), u.Port()
	if port == "" {
		switch strings.ToLower(u.Scheme) {
		case "https":
			port = "443"
		case "socks5", "socks5h":
			port = "1080"
		default:
			port = "80"
		}
	}
	return strings.ToLower(net.JoinHostPort(host, port))
}
