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
// traffic to xbind's userspace relay (plans/isolation.md §3).
const (
	tunName  = "bx0"
	tunAddr  = "10.0.2.15/24"
	tunGw    = GatewayIP  // 10.0.2.2 — xbind may host-forward here
	relayDNS = "10.0.2.3" // the relay answers DNS here
)

// setupEgress creates this netns's TUN(s), configures address/route/DNS, and
// hands the fd(s) back to xbind over the control socket. The egress TUN is sent
// first (xbind runs the relay on it, or splices it to a provider); then one TUN
// per NetClients entry (a provider tile's client links, which xbind splices).
// Runs as netns-root, before pivot_root (needs the host's /dev/net/tun).
func setupEgress(newroot string, s *Spec) error {
	addr, gw := s.NetAddr, s.NetGw
	if addr == "" {
		addr = tunAddr
	}
	if gw == "" {
		gw = tunGw
	}
	tunFD, err := createTUN(tunName)
	if err != nil {
		return fmt.Errorf("create tun: %w", err)
	}
	if err := configTUN(tunName, addr, gw); err != nil {
		return fmt.Errorf("config tun: %w", err)
	}
	// A spliced client reaches DNS through the provider chain to the terminal
	// relay (which pins :53 to the host resolver), so any public resolver works;
	// a direct relay client uses the relay's own DNS address.
	ns := relayDNS
	if s.Net == "splice" {
		ns = "1.1.1.1"
	}
	_ = os.MkdirAll(filepath.Join(newroot, "etc"), 0o755)
	_ = os.WriteFile(filepath.Join(newroot, "etc", "resolv.conf"),
		[]byte("nameserver "+ns+"\noptions single-request\n"), 0o644)

	if err := sendFD(s.CtrlFD, tunFD); err != nil {
		return fmt.Errorf("hand tun fd to xbind: %w", err)
	}
	unix.Close(tunFD) // xbind holds it via SCM_RIGHTS

	// Provider tile: one extra TUN per client link (addr only, no default route —
	// the provider routes among them and its egress).
	for i, c := range s.NetClients {
		name := fmt.Sprintf("bxc%d", i)
		fd, err := createTUN(name)
		if err != nil {
			return fmt.Errorf("create client tun %s: %w", c.Name, err)
		}
		if err := configAddr(name, c.Addr); err != nil {
			return fmt.Errorf("config client tun %s: %w", c.Name, err)
		}
		if err := sendFD(s.CtrlFD, fd); err != nil {
			return fmt.Errorf("hand client tun %s: %w", c.Name, err)
		}
		unix.Close(fd)
	}
	unix.Close(s.CtrlFD)
	return nil
}

// configAddr assigns an address and brings a link up, without adding a route
// (used for a provider's client links — the provider owns routing between them).
func configAddr(name, cidr string) error {
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
	return netlink.LinkSetUp(link)
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
