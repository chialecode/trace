//go:build windows

package companion

import (
	"net"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	ifTypeTunnel = 131 // IF_TYPE_TUNNEL
	ifTypeProp   = 53  // IF_TYPE_PROP_VIRTUAL
	ifTypePPP    = 23  // IF_TYPE_PPP
)

// EnumerateNetworkInterfaces lists OS network interfaces with accurate Windows
// tunnel/virtual adapter detection via GetAdaptersAddresses (IF_TYPE_TUNNEL / IF_TYPE_PROP_VIRTUAL).
func EnumerateNetworkInterfaces() ([]NetworkInterface, error) {
	tunnelNames := windowsVirtualAdapterNames()
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
			Virtual: tunnelNames[iface.Name] || isVirtualInterfaceName(iface.Name, iface.Flags),
			Addrs:   addrs,
		})
	}
	return out, nil
}

func windowsVirtualAdapterNames() map[string]bool {
	res := make(map[string]bool)
	var size uint32 = 16384
	for attempt := 0; attempt < 3; attempt++ {
		buf := make([]byte, size)
		aa := (*windows.IpAdapterAddresses)(unsafe.Pointer(&buf[0]))
		err := windows.GetAdaptersAddresses(windows.AF_UNSPEC, windows.GAA_FLAG_INCLUDE_PREFIX, 0, aa, &size)
		if err == windows.ERROR_BUFFER_OVERFLOW {
			continue
		}
		if err != nil {
			return res
		}
		for cur := aa; cur != nil; cur = cur.Next {
			if cur.IfType == ifTypeTunnel || cur.IfType == ifTypeProp || cur.IfType == ifTypePPP {
				name := windows.UTF16PtrToString(cur.FriendlyName)
				if name != "" {
					res[name] = true
				}
			}
		}
		return res
	}
	return res
}
