//go:build linux

package guest

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// configNet sets up eth0 as the guest end of the routed link into the
// sandbox's netns: a /32 address, the gateway on-link with a permanent
// neighbour entry (no ARP needed for the relay's addresses), and the default
// route through it. Egress policy lives in xbind's relay, not here.
func configNet(n *proto.Net) error {
	link, err := netlink.LinkByName("eth0")
	if err != nil {
		return err
	}
	if n.MTU > 0 {
		_ = netlink.LinkSetMTU(link, n.MTU)
	}
	addr, err := netlink.ParseAddr(n.Addr + "/32")
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return fmt.Errorf("address: %w", err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	gw := net.ParseIP(n.Gw)
	if mac, err := net.ParseMAC(n.GwMAC); err == nil {
		_ = netlink.NeighSet(&netlink.Neigh{
			LinkIndex:    link.Attrs().Index,
			IP:           gw,
			HardwareAddr: mac,
			State:        netlink.NUD_PERMANENT,
			Family:       unix.AF_INET,
		})
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst:       &net.IPNet{IP: gw, Mask: net.CIDRMask(32, 32)},
		Scope:     netlink.SCOPE_LINK,
	}); err != nil {
		return fmt.Errorf("gateway route: %w", err)
	}
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: link.Attrs().Index, Gw: gw}); err != nil {
		return fmt.Errorf("default route: %w", err)
	}
	return nil
}

func upLoopback() {
	if lo, err := netlink.LinkByName("lo"); err == nil {
		_ = netlink.LinkSetUp(lo)
	}
}
