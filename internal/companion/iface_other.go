//go:build !windows

package companion

import (
	"net"
	"net/netip"
)

// EnumerateNetworkInterfaces lists OS network interfaces and identifies
// virtual/tunnel adapters via PointToPoint flag and common tunnel interface names.
func EnumerateNetworkInterfaces() ([]NetworkInterface, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]NetworkInterface, 0, len(ifaces))
	for _, iface := range ifaces {
		rawAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		var addrs []netip.Addr
		for _, a := range rawAddrs {
			ipn, ok := a.(*net.IPNet)
			if !ok || ipn.IP == nil {
				continue
			}
			if parsed, ok := netip.AddrFromSlice(ipn.IP); ok {
				addrs = append(addrs, parsed.Unmap())
			}
		}
		out = append(out, NetworkInterface{
			Name:    iface.Name,
			Flags:   iface.Flags,
			Virtual: isVirtualInterfaceName(iface.Name, iface.Flags),
			Addrs:   addrs,
		})
	}
	return out, nil
}
