//go:build linux

package sandbox

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Virtual egress network inside the component netns: the TUN carries all IP
// traffic to buxond's userspace relay (plans/isolation.md §3).
const (
	tunName  = "bx0"
	tunAddr  = "10.0.2.15/24"
	tunGw    = GatewayIP  // 10.0.2.2 — buxond may host-forward here
	relayDNS = "10.0.2.3" // the relay answers DNS here
)

// setupEgress creates the TUN in this (the component's) netns, configures its
// address/route/DNS, and hands the TUN fd back to buxond over the control
// socket. buxond runs the userspace stack on it. Runs as netns-root, before
// pivot_root (needs the host's /dev/net/tun). newroot is where /etc/resolv.conf
// is written.
func setupEgress(newroot string, ctrlFD int) error {
	tunFD, err := createTUN(tunName)
	if err != nil {
		return fmt.Errorf("create tun: %w", err)
	}
	if err := configTUN(tunName, tunAddr, tunGw); err != nil {
		return fmt.Errorf("config tun: %w", err)
	}
	_ = os.MkdirAll(filepath.Join(newroot, "etc"), 0o755)
	_ = os.WriteFile(filepath.Join(newroot, "etc", "resolv.conf"),
		[]byte("nameserver "+relayDNS+"\noptions single-request\n"), 0o644)

	if err := sendFD(ctrlFD, tunFD); err != nil {
		return fmt.Errorf("hand tun fd to buxond: %w", err)
	}
	// buxond now holds the TUN (via SCM_RIGHTS), keeping it alive; drop our copy.
	unix.Close(tunFD)
	unix.Close(ctrlFD)
	return nil
}

func createTUN(name string) (int, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR, 0)
	if err != nil {
		return -1, err
	}
	var ifr [40]byte
	copy(ifr[:15], name)
	flags := uint16(unix.IFF_TUN | unix.IFF_NO_PI)
	ifr[16] = byte(flags)
	ifr[17] = byte(flags >> 8)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETIFF, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		unix.Close(fd)
		return -1, e
	}
	return fd, nil
}

func configTUN(name, cidr, gw string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	addr, err := netlink.ParseAddr(cidr)
	if err != nil {
		return err
	}
	if err := netlink.AddrAdd(link, addr); err != nil {
		return err
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return err
	}
	return netlink.RouteAdd(&netlink.Route{
		LinkIndex: link.Attrs().Index,
		Gw:        net.ParseIP(gw),
	})
}

// sendFD passes fd over the control socket using SCM_RIGHTS.
func sendFD(sock, fd int) error {
	rights := unix.UnixRights(fd)
	return unix.Sendmsg(sock, []byte{'t'}, rights, nil, 0)
}
