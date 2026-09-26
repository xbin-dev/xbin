//go:build linux

package guest

import (
	"fmt"
	"net"
	"os"
	"syscall"

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

// formatExt4 makes the persistent disk's filesystem with the rootfs image's
// own mkfs.ext4 (the initramfs holds only the agent). PID 1's reaper owns
// every wait, so the child goes through spawn.
func (a *agent) formatExt4(dev string) error {
	if err := unix.Mount("/dev", "/lower/dev", "", unix.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind /dev for mkfs: %w", err)
	}
	defer unix.Unmount("/lower/dev", unix.MNT_DETACH)
	argv := []string{"mkfs.ext4", "-F", "-q", "-E", "lazy_itable_init=1,lazy_journal_init=1,discard", "-L", "xbin-vm", dev}
	_, done, err := a.spawn("/sbin/mkfs.ext4", argv, &os.ProcAttr{
		Env:   []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin"},
		Files: []*os.File{nil, os.Stderr, os.Stderr},
		Sys:   &syscall.SysProcAttr{Chroot: "/lower"},
	})
	if err != nil {
		return err
	}
	if ws := <-done; ws.ExitStatus() != 0 {
		return fmt.Errorf("mkfs.ext4 exited %d", ws.ExitStatus())
	}
	return nil
}
