package site

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
)

var (
	prefixCGNAT   = netip.MustParsePrefix("100.64.0.0/10")
	prefixThisNet = netip.MustParsePrefix("0.0.0.0/8")
	prefixNAT64   = netip.MustParsePrefix("64:ff9b::/96")
)

// GuardedMediaDialer holds the Proxy and DialContext pair for a media CDN
// transport. Media hosts must not resolve to loopback, private, link-local,
// CGNAT, multicast, NAT64, or unspecified addresses. The proxy currently chosen
// by the user (Settings → 网络) is trusted and dialed as is; requests sent
// through it still have their media host checked against local DNS first, and
// switching proxy settings invalidates previously trusted proxy addresses.
type GuardedMediaDialer struct {
	site       ID
	allowLocal bool
	dialer     *net.Dialer
	message    string
	proxyFunc  func(*http.Request) (*url.URL, error)
	resolver   func(ctx context.Context, host string) ([]net.IPAddr, error)

	mu         sync.RWMutex
	proxyGen   uint64
	proxyAddrs map[string]struct{}
}

// NewGuardedMediaDialer returns a guard for site using the proxy chosen in
// Settings → 网络 (Proxy).
// message is the user-facing text when an address is refused.
func NewGuardedMediaDialer(siteID ID, allowLocalhost bool, dialer *net.Dialer, message string) *GuardedMediaDialer {
	if dialer == nil {
		dialer = &net.Dialer{}
	}
	return &GuardedMediaDialer{
		site:       siteID,
		allowLocal: allowLocalhost,
		dialer:     dialer,
		message:    message,
		proxyFunc:  Proxy,
		resolver:   net.DefaultResolver.LookupIPAddr,
		proxyGen:   proxyGen.Load(),
	}
}

func (g *GuardedMediaDialer) isTrustedProxyAddr(addr string) bool {
	key := strings.ToLower(addr)
	curGen := proxyGen.Load()
	g.mu.RLock()
	if g.proxyGen == curGen {
		_, ok := g.proxyAddrs[key]
		g.mu.RUnlock()
		return ok
	}
	g.mu.RUnlock()

	g.mu.Lock()
	if g.proxyGen != curGen {
		g.proxyAddrs = nil
		g.proxyGen = curGen
	}
	_, ok := g.proxyAddrs[key]
	g.mu.Unlock()
	return ok
}

func (g *GuardedMediaDialer) rememberProxyAddr(addr string) {
	key := strings.ToLower(addr)
	curGen := proxyGen.Load()
	g.mu.Lock()
	if g.proxyGen != curGen {
		g.proxyAddrs = nil
		g.proxyGen = curGen
	}
	if g.proxyAddrs == nil {
		g.proxyAddrs = make(map[string]struct{})
	}
	g.proxyAddrs[key] = struct{}{}
	g.mu.Unlock()
}

// Proxy is the http.Transport Proxy hook.
func (g *GuardedMediaDialer) Proxy(req *http.Request) (*url.URL, error) {
	proxyURL, err := g.proxyFunc(req)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		if !g.allowLocal && req != nil && req.URL != nil && g.isTrustedProxyAddr(canonicalProxyAddr(req.URL)) {
			// A direct request targeting the same host:port as an active proxy
			// (e.g. via NO_PROXY) must still pass the media host guard.
			if ips, lerr := g.resolver(req.Context(), req.URL.Hostname()); lerr != nil || len(ips) == 0 || blockedAddress(ips) {
				return nil, g.blocked()
			}
		}
		return nil, nil
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
	g.rememberProxyAddr(canonicalProxyAddr(proxyURL))
	return proxyURL, nil
}

// DialContext is the http.Transport DialContext hook.
func (g *GuardedMediaDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if g.allowLocal {
		return g.dialer.DialContext(ctx, network, addr)
	}
	if g.isTrustedProxyAddr(addr) {
		return g.dialer.DialContext(ctx, network, addr)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	var ips []net.IPAddr
	if lit := net.ParseIP(host); lit != nil {
		ips = []net.IPAddr{{IP: lit}}
	} else {
		ips, err = g.resolver(ctx, host)
		if err != nil {
			return nil, err
		}
	}
	if len(ips) == 0 {
		return nil, &net.DNSError{Err: "no addresses", Name: host, IsNotFound: true}
	}
	if blockedAddress(ips) {
		return nil, g.blocked()
	}
	var lastErr error
	for _, ipAddr := range ips {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		conn, dErr := g.dialer.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
		if dErr == nil {
			return conn, nil
		}
		lastErr = dErr
	}
	return nil, lastErr
}

func (g *GuardedMediaDialer) blocked() error {
	return &Error{Site: g.site, Kind: ErrKindBlocked, Message: g.message}
}

func blockedAddress(ips []net.IPAddr) bool {
	for _, ipAddr := range ips {
		addr, ok := netip.AddrFromSlice(ipAddr.IP)
		if !ok {
			return true
		}
		addr = addr.Unmap()
		if addr.IsLoopback() ||
			addr.IsPrivate() ||
			addr.IsLinkLocalUnicast() ||
			addr.IsLinkLocalMulticast() ||
			addr.IsMulticast() ||
			addr.IsUnspecified() ||
			prefixCGNAT.Contains(addr) ||
			prefixThisNet.Contains(addr) ||
			prefixNAT64.Contains(addr) {
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
