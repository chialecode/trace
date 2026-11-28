package mdns

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/companionclient"
)

// DefaultServiceType is the DNS-SD service type advertised by desktop TRACE.
const DefaultServiceType = companionapi.MDNSServiceType + "."

const (
	defaultBrowseTimeout = 3500 * time.Millisecond
	firstMatchSettleWait = 450 * time.Millisecond
)

var cgnatPrefix = netip.MustParsePrefix("100.64.0.0/10")

// DiscoverFunc finds candidate "host:port" endpoints matching (targetNode, targetChannel).
type DiscoverFunc func(ctx context.Context, targetNode, targetChannel string) ([]string, error)

// BrowserFactory builds a Browser; overridden in unit tests.
type BrowserFactory func() (Browser, error)

var (
	hookMu         sync.RWMutex
	customDiscover DiscoverFunc
	customFactory  BrowserFactory
)

// SetDiscoverForTest replaces DiscoverMatchingAddrs for unit tests (pass nil to restore).
func SetDiscoverForTest(fn DiscoverFunc) {
	hookMu.Lock()
	customDiscover = fn
	hookMu.Unlock()
}

// SetBrowserFactoryForTest replaces NewBrowser for unit tests (pass nil to restore).
func SetBrowserFactoryForTest(fn BrowserFactory) {
	hookMu.Lock()
	customFactory = fn
	hookMu.Unlock()
}

// IsAllowedLANHost returns true strictly for local LAN / IPv6 ULA / link-local /
// VPN CGNAT (100.64.0.0/10) unicast IP literals, rejecting public, loopback,
// unspecified, and multicast hosts.
func IsAllowedLANHost(rawHost string) bool {
	h := strings.TrimSpace(rawHost)
	if idx := strings.IndexByte(h, '%'); idx >= 0 {
		h = h[:idx]
	}
	addr, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsValid() || addr.IsUnspecified() || addr.IsMulticast() || addr.IsLoopback() {
		return false
	}
	if addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	return addr.Is4() && cgnatPrefix.Contains(addr)
}

// DiscoverMatchingAddrs browses LAN mDNS (_trace-companion._tcp.) for desktop
// instances whose TXT `node` and `channel` match the paired desktop.
//
// SECURITY: Returned addresses are untrusted network hints only. Callers MUST
// always connect using the original paired TLS 1.3 certificate fingerprint and
// never trust an unpinned certificate.
func DiscoverMatchingAddrs(ctx context.Context, targetNode, targetChannel string) ([]string, error) {
	targetNode = strings.TrimSpace(targetNode)
	normChannel := companionclient.NormalizeChannel(targetChannel)
	if targetNode == "" || normChannel == "" {
		return nil, nil
	}

	hookMu.RLock()
	discoverFn := customDiscover
	factoryFn := customFactory
	hookMu.RUnlock()

	if discoverFn != nil {
		return discoverFn(ctx, targetNode, normChannel)
	}

	var (
		browser Browser
		err     error
	)
	if factoryFn != nil {
		browser, err = factoryFn()
	} else {
		browser, err = NewBrowser()
	}
	if err != nil {
		if errors.Is(err, errors.ErrUnsupported) {
			return nil, nil
		}
		return nil, err
	}
	defer func() {
		browser.Stop()
		_ = browser.Close()
	}()

	browseCtx := ctx
	var cancel context.CancelFunc
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		browseCtx, cancel = context.WithTimeout(ctx, defaultBrowseTimeout)
		defer cancel()
	}

	var (
		mu       sync.Mutex
		ipv4Out  []string
		ipv6Out  []string
		seen     = make(map[string]bool)
		firstHit = make(chan struct{}, 1)
		errCh    = make(chan string, 1)
	)

	browser.Start(DefaultServiceType, func(host string, port int, node, channel string) {
		if strings.TrimSpace(node) != targetNode {
			return
		}
		if companionclient.NormalizeChannel(channel) != normChannel {
			return
		}
		if port <= 0 || port > 65535 {
			return
		}
		if !IsAllowedLANHost(host) {
			return
		}
		cleanHost := strings.TrimSpace(host)
		if idx := strings.IndexByte(cleanHost, '%'); idx >= 0 {
			cleanHost = cleanHost[:idx]
		}
		endpoint := net.JoinHostPort(cleanHost, strconv.Itoa(port))

		mu.Lock()
		if !seen[endpoint] {
			seen[endpoint] = true
			if addr, parseErr := netip.ParseAddr(cleanHost); parseErr == nil && addr.Unmap().Is4() {
				ipv4Out = append(ipv4Out, endpoint)
			} else {
				ipv6Out = append(ipv6Out, endpoint)
			}
			select {
			case firstHit <- struct{}{}:
			default:
			}
		}
		mu.Unlock()
	}, func(msg string) {
		select {
		case errCh <- msg:
		default:
		}
	})

	select {
	case <-browseCtx.Done():
	case <-errCh:
	case <-firstHit:
		// Give multi-NIC desktops a brief settle window to emit all LAN addresses.
		t := time.NewTimer(firstMatchSettleWait)
		select {
		case <-browseCtx.Done():
			t.Stop()
		case <-t.C:
		}
	}

	mu.Lock()
	out := make([]string, 0, len(ipv4Out)+len(ipv6Out))
	out = append(out, ipv4Out...)
	out = append(out, ipv6Out...)
	mu.Unlock()
	return out, nil
}
