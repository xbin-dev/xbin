//go:build linux && integration

package relay

import (
	"errors"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// localityChild marks the re-exec'd test binary that runs inside its own
// user + network namespace.
const localityChild = "XBIN_RELAY_LOCALITY_CHILD"

// HostDeny decides locality per flow (plans/tile-sandbox-runtime.md §4,
// WP-10b): in a network namespace of its own — the test binary re-execs
// itself into a new user + net namespace, where it may add addresses and
// routes — an address added to an interface after the relay started is
// refused on its first flow, an AnyIP route is refused, and a routable
// address that isn't local passes on to Allow.
func TestHostDenyLocality(t *testing.T) {
	if os.Getenv(localityChild) == "" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestHostDenyLocality$", "-test.v", "-test.count=1")
		cmd.Env = append(os.Environ(), localityChild+"=1")
		cmd.SysProcAttr = &syscall.SysProcAttr{
			Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNET,
			UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
			GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
		}
		out, err := cmd.CombinedOutput()
		var ee *exec.ExitError
		switch {
		case err != nil && !errors.As(err, &ee):
			t.Skipf("no user + network namespace for the helper: %v", err)
		case err != nil:
			t.Fatalf("helper: %v\n%s", err, out)
		case strings.Contains(string(out), "--- SKIP"):
			t.Skipf("helper skipped:\n%s", out)
		}
		t.Logf("helper:\n%s", out)
		return
	}
	localityChecks(t)
}

func localityChecks(t *testing.T) {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Skip("lo up:", err)
	}
	// A dummy interface carries the addresses; where one can't be made, the
	// loopback does (no broadcast route then).
	link := netlink.Link(&netlink.Dummy{LinkAttrs: netlink.LinkAttrs{Name: "dum0"}})
	dummy := true
	if err := netlink.LinkAdd(link); err != nil {
		t.Logf("no dummy interface (%v): addresses go on lo", err)
		link, dummy = lo, false
	} else if err := netlink.LinkSetUp(link); err != nil {
		t.Fatal(err)
	}
	addAddr := func(cidr string) {
		t.Helper()
		a, err := netlink.ParseAddr(cidr)
		if err != nil {
			t.Fatal(err)
		}
		a.Flags = unix.IFA_F_NODAD
		if err := netlink.AddrAdd(link, a); err != nil {
			t.Fatalf("add %s: %v", cidr, err)
		}
	}
	// 192.0.2.0/24 is routed through the link: 192.0.2.77 is reachable and
	// not the host's.
	if dummy {
		addAddr("192.0.2.1/24")
	} else if err := netlink.RouteAdd(&netlink.Route{LinkIndex: lo.Attrs().Index,
		Dst: mustCIDR(t, "192.0.2.0/24"), Scope: netlink.SCOPE_LINK}); err != nil {
		t.Fatal(err)
	}

	deny := HostDeny()
	h := newHarness(t, Config{Allow: allowAll, Deny: deny})
	passes := func(ip string) {
		t.Helper()
		a := netip.MustParseAddr(ip)
		if deny(a) {
			t.Fatalf("%s: denied, but it isn't this namespace's", ip)
		}
		if a.Is4() {
			if _, dial := h.synTo(a, 80); dial != "tcp "+ip+":80" {
				t.Fatalf("%s: the flow must pass on to Allow and be dialed, got %q", ip, dial)
			}
		}
	}
	refused := func(ip string) {
		t.Helper()
		a := netip.MustParseAddr(ip)
		if !deny(a) {
			t.Fatalf("%s: not denied", ip)
		}
		if a.Is4() {
			if reset, dial := h.synTo(a, 80); !reset || dial != "" {
				t.Fatalf("%s: reset=%v dial=%q, want a RST and no dial", ip, reset, dial)
			}
		}
	}

	passes("192.0.2.77")  // routable, not local
	passes("203.0.113.9") // no route at all: not local either (its dial fails)
	refused("192.0.2.1")  // the link's own address

	// Added after the relay started: refused on its very first flow.
	addAddr("203.0.113.9/32")
	refused("203.0.113.9")
	addAddr("2001:db8::9/128")
	waitLocal(t, "2001:db8::9") // IPv6 installs an address's local route from a work queue
	refused("2001:db8::9")

	// An AnyIP range: `ip route add local 198.51.100.0/24 dev lo`.
	if err := netlink.RouteAdd(&netlink.Route{LinkIndex: lo.Attrs().Index, Dst: mustCIDR(t, "198.51.100.0/24"),
		Type: unix.RTN_LOCAL, Table: unix.RT_TABLE_LOCAL, Scope: netlink.SCOPE_HOST}); err != nil {
		t.Fatal(err)
	}
	refused("198.51.100.7")
	refused("198.51.100.200")
	passes("198.51.101.7")

	if dummy { // the link's broadcast address is delivered locally too
		refused("192.0.2.255")
	}

	// Removed again: no longer the host's, so it passes on.
	a, _ := netlink.ParseAddr("203.0.113.9/32")
	if err := netlink.AddrDel(link, a); err != nil {
		t.Fatal(err)
	}
	passes("203.0.113.9")
}

// waitLocal waits until the kernel routes ip as local: an IPv6 address's
// local route comes a moment after the address, from addrconf's work queue
// (even with IFA_F_NODAD), and until then nothing can call it the host's.
func waitLocal(t *testing.T, ip string) {
	t.Helper()
	waitUntil(t, ip+": the kernel routes it as local", func() bool {
		rs, err := netlink.RouteGet(net.ParseIP(ip))
		return err == nil && len(rs) > 0 && rs[0].Type == unix.RTN_LOCAL
	})
}

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, n, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
