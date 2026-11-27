package companion

import (
	"net"
	"net/netip"
	"sort"
	"strings"
)

// NetworkInterface describes one OS network interface and its addresses,
// abstracted so interface selection and CGNAT/VPN rules can be tested deterministically.
type NetworkInterface struct {
	Name    string
	Flags   net.Flags
	Virtual bool // true for tunnel/VPN adapters (e.g. Tailscale, WireGuard, utun, tun/tap)
	Addrs   []netip.Addr
}

// ListenCandidate is a validated local interface address eligible for listening.
type ListenCandidate struct {
	Addr    netip.Addr
	Virtual bool
	Iface   string
}

var (
	prefix10    = netip.MustParsePrefix("10.0.0.0/8")
	prefix172   = netip.MustParsePrefix("172.16.0.0/12")
	prefix192   = netip.MustParsePrefix("192.168.0.0/16")
	prefix169   = netip.MustParsePrefix("169.254.0.0/16")
	prefixCGNAT = netip.MustParsePrefix("100.64.0.0/10")
	prefixULA   = netip.MustParsePrefix("fc00::/7")
	prefixLL6   = netip.MustParsePrefix("fe80::/10")
)

// IsPrivateLANAddr reports whether ip is a private RFC1918 IPv4 or IPv6 ULA address.
func IsPrivateLANAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return prefix10.Contains(ip) || prefix172.Contains(ip) || prefix192.Contains(ip) || prefixULA.Contains(ip)
}

// IsCGNATAddr reports whether ip falls in 100.64.0.0/10 (RFC 6598 / Tailscale).
func IsCGNATAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return prefixCGNAT.Contains(ip)
}

// IsLinkLocalAddr reports whether ip is IPv4 link-local (169.254.0.0/16) or IPv6 link-local (fe80::/10).
func IsLinkLocalAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return prefix169.Contains(ip) || prefixLL6.Contains(ip)
}

// SelectListenCandidates chooses eligible local addresses from OS interfaces
// strictly following docs/design/mobile.md §3 and NFR-MOB-SEC-002:
//   - RFC 1918 private IPv4 (10/8, 172.16/12, 192.168/16)
//   - IPv6 ULA (fc00::/7) and link-local (fe80::/10)
//   - 100.64.0.0/10 ONLY when assigned to a virtual/tunnel interface (e.g. Tailscale);
//     physical NIC 100.64.0.0/10 is carrier-grade NAT and treated as public (excluded).
//   - Public addresses and loopback are excluded from LAN listener enumeration.
func SelectListenCandidates(ifaces []NetworkInterface) []ListenCandidate {
	seen := make(map[string]bool)
	var out []ListenCandidate
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		isVirtual := iface.Virtual || isVirtualInterfaceName(iface.Name, iface.Flags)
		for _, raw := range iface.Addrs {
			if !raw.IsValid() {
				continue
			}
			addr := raw.Unmap()
			if addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() {
				continue
			}
			eligible := false
			switch {
			case IsPrivateLANAddr(addr):
				eligible = true
			case prefixLL6.Contains(addr):
				// IPv6 link-local requires a zone when listening.
				if addr.Zone() != "" || iface.Name != "" {
					if addr.Zone() == "" {
						addr = addr.WithZone(iface.Name)
					}
					eligible = true
				}
			case IsCGNATAddr(addr):
				// Crucial rule (design §3, §12): only virtual/tunnel interfaces!
				eligible = isVirtual
			}
			if !eligible {
				continue
			}
			key := addr.String()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, ListenCandidate{
				Addr:    addr,
				Virtual: isVirtual,
				Iface:   iface.Name,
			})
		}
	}
	// Deterministic order: IPv4 private first, then virtual 100.64/10, then IPv6 ULA, then IPv6 link-local.
	sort.Slice(out, func(i, j int) bool {
		pi, pj := candidatePriority(out[i].Addr), candidatePriority(out[j].Addr)
		if pi != pj {
			return pi < pj
		}
		return out[i].Addr.String() < out[j].Addr.String()
	})
	return out
}

func candidatePriority(a netip.Addr) int {
	u := a.Unmap()
	switch {
	case prefix192.Contains(u):
		return 0
	case prefix10.Contains(u):
		return 1
	case prefix172.Contains(u):
		return 2
	case prefixCGNAT.Contains(u):
		return 3
	case prefixULA.Contains(u):
		return 4
	default:
		return 5
	}
}

// IsAllowedSource checks whether a request's remote IP is allowed according to
// docs/design/mobile.md §5.1 and NFR-MOB-SEC-002:
//   - 10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16, 169.254.0.0/16
//   - fc00::/7, fe80::/10
//   - loopback (127.0.0.0/8, ::1)
//   - 100.64.0.0/10 ONLY when received on a virtual/tunnel interface (arrivedOnVirtual == true)
//
// Any public IP or CGNAT IP on a physical interface is rejected with 403 forbidden_source.
func IsAllowedSource(remoteIP netip.Addr, arrivedOnVirtual bool) bool {
	if !remoteIP.IsValid() {
		return false
	}
	ip := remoteIP.Unmap()
	if ip.IsLoopback() {
		return true
	}
	if IsPrivateLANAddr(ip) || IsLinkLocalAddr(ip) {
		return true
	}
	if IsCGNATAddr(ip) {
		return arrivedOnVirtual
	}
	return false
}

// ParseRemoteAddr extracts a netip.Addr from http.Request.RemoteAddr.
func ParseRemoteAddr(remoteAddr string) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = strings.Trim(strings.TrimSpace(remoteAddr), "[]")
	}
	if host == "" {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}

// isVirtualInterfaceName provides a portable fallback heuristic for tunnel/VPN adapters.
// OS-specific enumeration (iface_windows.go) additionally inspects MIB interface type (IF_TYPE_TUNNEL).
func isVirtualInterfaceName(name string, flags net.Flags) bool {
	if flags&net.FlagPointToPoint != 0 {
		return true
	}
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, kw := range []string{
		"tailscale", "wireguard", "wg", "wintun", "tun", "tap", "utun",
		"zerotier", "zt", "vpn", "ppp", "nordlynx", "clash", "mihomo",
	} {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

// AdvertisedAddrs returns clean string addresses suitable for QR pairing payloads
// (excluding IPv6 link-local addresses with host-specific zone IDs that a remote phone cannot dial).
func AdvertisedAddrs(candidates []ListenCandidate) []string {
	out := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		u := c.Addr.Unmap()
		if prefixLL6.Contains(u) {
			continue
		}
		s := u.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
