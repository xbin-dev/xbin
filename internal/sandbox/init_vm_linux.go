//go:build linux

package sandbox

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"unsafe"

	"github.com/vishvananda/netlink"
	"golang.org/x/net/bpf"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// VM sandboxes (plans/vm-sandbox.md): the pieces of the init that differ
// when Spec.VM is set. The namespace sandbox is the jail around Firecracker —
// Firecracker's own jailer needs root — so everything here keeps the
// namespace sandbox's properties and adds only what a VMM needs.

// VMSpecPath is where the init leaves Spec.VM for the shim, and VMDir the
// sandbox-root directory holding the VM's pieces (binds under bin/, boot/,
// img/; the shim's sockets under run/). Neither is ever exported to a guest.
const (
	VMDir      = "/.xbin-vm"
	VMSpecPath = VMDir + "/run/spec.json"
)

// vmRoot makes newroot a bare tmpfs: the shim and Firecracker are static and
// need no userland, and a VMM escape finds no shell or tools.
func vmRoot(newroot string) error {
	if err := unix.Mount("tmpfs", newroot, "tmpfs", unix.MS_NOSUID|unix.MS_NODEV, "mode=0755"); err != nil {
		return must(err, "vm root tmpfs")
	}
	return os.MkdirAll(filepath.Join(newroot, VMDir, "run"), 0o700)
}

// vmDevices binds the device nodes Firecracker opens: /dev/kvm always, and
// /dev/net/tun for the guest NIC's TAP.
func vmDevices(newroot string, s *Spec) error {
	if err := bindNode(newroot, "/dev/kvm"); err != nil {
		return must(err, "bind /dev/kvm (is KVM available and the xbind user in the kvm group?)")
	}
	if s.Net == "relay" {
		if err := bindNode(newroot, "/dev/net/tun"); err != nil {
			return must(err, "bind /dev/net/tun")
		}
	}
	return nil
}

// writeVMSpec leaves Spec.VM where the shim reads it.
func writeVMSpec(newroot string, s *Spec) error {
	b, err := json.Marshal(s.VM)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(newroot, VMSpecPath), b, 0o600)
}

// setupVMEgress is setupEgress for a VM: the egress TUN goes to xbind's relay
// exactly as for a namespace sandbox, but the netns keeps no address of its
// own — it routes. The guest owns 10.0.2.15 on the far side of a TAP
// (persistent and owned by this sandbox's root, so Firecracker opens it with
// no capabilities); permanent neighbour entries replace ARP on both sides and
// strict reverse-path filtering drops anything the guest sends from an
// address it doesn't own. The relay is unchanged: it sees 10.0.2.15 as ever.
func setupVMEgress(newroot string, s *Spec) error {
	tunFD, err := createTUN(tunName)
	if err != nil {
		return fmt.Errorf("create tun: %w", err)
	}
	tun, err := netlink.LinkByName(tunName)
	if err != nil {
		return err
	}
	if err := netlink.LinkSetUp(tun); err != nil {
		return err
	}
	if err := createTap(proto.TapName); err != nil {
		return fmt.Errorf("create tap: %w", err)
	}
	tap, err := netlink.LinkByName(proto.TapName)
	if err != nil {
		return err
	}
	tapMAC, _ := net.ParseMAC(proto.TapMAC)
	guestMAC, _ := net.ParseMAC(proto.GuestMAC)
	if err := netlink.LinkSetHardwareAddr(tap, tapMAC); err != nil {
		return fmt.Errorf("tap mac: %w", err)
	}
	if err := netlink.LinkSetUp(tap); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"ipv4/ip_forward", "1"},
		{"ipv4/conf/all/rp_filter", "1"},
		{"ipv4/conf/default/rp_filter", "1"},
		{"ipv4/conf/" + tunName + "/rp_filter", "1"},
		{"ipv4/conf/" + proto.TapName + "/rp_filter", "1"},
		{"ipv6/conf/" + proto.TapName + "/disable_ipv6", "1"},
	} {
		if err := os.WriteFile("/proc/sys/net/"+kv[0], []byte(kv[1]), 0); err != nil && kv[0] == "ipv4/ip_forward" {
			return fmt.Errorf("sysctl %s: %w", kv[0], err)
		}
	}
	guest := net.ParseIP(proto.GuestAddr).To4()
	if err := netlink.NeighSet(&netlink.Neigh{
		LinkIndex: tap.Attrs().Index, IP: guest, HardwareAddr: guestMAC,
		State: netlink.NUD_PERMANENT, Family: unix.AF_INET,
	}); err != nil {
		return fmt.Errorf("guest neighbour: %w", err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: tap.Attrs().Index,
		Dst:       &net.IPNet{IP: guest, Mask: net.CIDRMask(32, 32)},
		Scope:     netlink.SCOPE_LINK,
	}); err != nil {
		return fmt.Errorf("guest route: %w", err)
	}
	if err := netlink.RouteAdd(&netlink.Route{
		LinkIndex: tun.Attrs().Index,
		Dst:       &net.IPNet{IP: net.IPv4zero, Mask: net.CIDRMask(0, 32)},
		Scope:     netlink.SCOPE_LINK,
	}); err != nil {
		return fmt.Errorf("default route: %w", err)
	}
	if err := sendFD(s.CtrlFD, tunFD); err != nil {
		return fmt.Errorf("hand tun fd to xbind: %w", err)
	}
	unix.Close(tunFD)
	unix.Close(s.CtrlFD)
	return nil
}

// createTap makes a persistent TAP owned by this sandbox's root, with vnet
// headers (Firecracker's virtio-net needs them), and lets go of it.
func createTap(name string) error {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	var ifr [40]byte
	copy(ifr[:15], name)
	flags := uint16(unix.IFF_TAP | unix.IFF_NO_PI | unix.IFF_VNET_HDR)
	ifr[16] = byte(flags)
	ifr[17] = byte(flags >> 8)
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETIFF, uintptr(unsafe.Pointer(&ifr))); e != 0 {
		return e
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETOWNER, 0); e != 0 {
		return e
	}
	if _, _, e := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TUNSETPERSIST, 1); e != 0 {
		return e
	}
	return nil
}

// vmCaps is what the shim keeps (and Firecracker inherits): the file
// capabilities it needs to serve the binds to the guest — files a range-mode
// namespace terminal created under sub-uids included — and nothing that
// mounts, traces, or builds namespaces. Root in the guest is the guest's.
func vmCaps() []int {
	return []int{unix.CAP_CHOWN, unix.CAP_DAC_OVERRIDE, unix.CAP_DAC_READ_SEARCH, unix.CAP_FOWNER, unix.CAP_FSETID}
}

// vmDeny is the backend block-list minus mknodat: the file server creates
// the FIFOs and sockets a guest makes in its binds (device nodes need
// CAP_MKNOD, which is gone).
func vmDeny() []uint32 {
	var out []uint32
	for _, nr := range backendDeny() {
		if nr != uint32(unix.SYS_MKNODAT) {
			out = append(out, nr)
		}
	}
	return out
}

// vmLockdown is a VM sandbox's profile, applied instead of the backend or
// terminal flags: no nested namespaces, the VM caps, the VM block-list, the
// mount guard. Firecracker installs its own per-thread filters on top.
func vmLockdown() error {
	setUserNSLimits()
	if err := dropCapsExcept(vmCaps()); err != nil {
		return must(err, "vm caps")
	}
	if err := installFilter(func(arch uint32) []bpf.Instruction { return denyProgram(arch, vmDeny(), false) }); err != nil {
		return must(err, "vm seccomp")
	}
	if err := installMountGuard(); err != nil {
		return must(err, "vm mount guard")
	}
	return nil
}
