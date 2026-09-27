//go:build linux

package relay

import (
	"net/netip"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// hostLocalPrefixes is what the host delivers to itself: the local routing
// table's local and anycast routes — every interface address, plus any AnyIP
// range (`ip route add local <cidr> dev lo`). If the table can't be read, the
// interface addresses stand in.
func hostLocalPrefixes() ([]netip.Prefix, error) {
	routes, err := netlink.RouteListFiltered(netlink.FAMILY_ALL,
		&netlink.Route{Table: unix.RT_TABLE_LOCAL}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return interfacePrefixes()
	}
	var out []netip.Prefix
	for _, rt := range routes {
		if (rt.Type != unix.RTN_LOCAL && rt.Type != unix.RTN_ANYCAST) || rt.Dst == nil {
			continue
		}
		a, ok := netip.AddrFromSlice(rt.Dst.IP)
		ones, bits := rt.Dst.Mask.Size()
		if !ok || bits != a.BitLen() {
			continue
		}
		out = append(out, netip.PrefixFrom(a, ones)) // refreshLocked unmaps
	}
	if len(out) == 0 {
		return interfacePrefixes()
	}
	return out, nil
}
