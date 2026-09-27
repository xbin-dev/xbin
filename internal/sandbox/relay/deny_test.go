package relay

import (
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestHostDeny(t *testing.T) {
	host := []netip.Prefix{
		netip.MustParsePrefix("192.168.1.5/32"),
		netip.MustParsePrefix("203.0.113.9/32"), // a public address on a host interface
		netip.MustParsePrefix("2001:db8::5/128"),
		netip.MustParsePrefix("198.51.100.0/28"), // an AnyIP range
		netip.MustParsePrefix("::ffff:192.0.2.77/128"),
	}
	set := newAddrSet(func() ([]netip.Prefix, error) { return host, nil }, time.Hour)
	d := newHostDeny(set.contains,
		netip.MustParseAddrPort("192.0.2.80:8080"), netip.MustParseAddrPort("[::]:8080"))

	for _, c := range []struct {
		ip   string
		deny bool
	}{
		{"127.0.0.1", true},
		{"127.9.9.9", true},
		{"::1", true},
		{"::ffff:127.0.0.1", true},
		{"0.0.0.0", true},
		{"::", true},
		{"::ffff:0.0.0.0", true}, // IPv4-mapped unspecified dials the host
		{"169.254.169.254", true},
		{"fe80::1", true},
		{"224.0.0.1", true},
		{"239.1.2.3", true},
		{"ff02::1", true},
		{"10.0.2.2", true}, // the relay's virtual network
		{"10.0.2.3", true},
		{"10.0.2.4", true},
		{"10.0.2.200", true},
		{"192.168.1.5", true},           // host interface
		{"::ffff:192.168.1.5", true},    // …IPv4-mapped
		{"203.0.113.9", true},           // the host's public address
		{"2001:db8::5", true},           // host v6
		{"198.51.100.7", true},          // inside the AnyIP range
		{"192.0.2.77", true},            // a mapped host address, unmapped
		{"192.0.2.80", true},            // a listen address (any port)
		{"8.8.8.8", false},              // the internet
		{"192.168.1.6", false},          // the LAN, not the host
		{"10.0.3.1", false},             // outside 10.0.2.0/24
		{"198.51.100.16", false},        // just past the AnyIP range
		{"2001:db8::6", false},          // v6 neighbour
		{"2606:4700:4700::1111", false}, // v6 internet
	} {
		if got := d.denied(netip.MustParseAddr(c.ip)); got != c.deny {
			t.Errorf("denied(%s) = %v, want %v", c.ip, got, c.deny)
		}
	}
	if !d.denied(netip.Addr{}) {
		t.Error("an invalid address must be denied")
	}
}

// Off Linux, the host's addresses are re-read when stale; until one read
// succeeds, everything is denied, and a failed re-read keeps the last good
// view.
func TestHostDenyRefresh(t *testing.T) {
	var host []netip.Prefix
	var fail bool
	read := func() ([]netip.Prefix, error) {
		if fail {
			return nil, errors.New("netlink down")
		}
		return host, nil
	}
	pub := netip.MustParseAddr("8.8.8.8")
	vpn := netip.MustParseAddr("100.64.1.2")

	fail = true
	set := newAddrSet(read, time.Nanosecond)
	d := newHostDeny(set.contains)
	if !d.denied(pub) {
		t.Fatal("with no view of the host yet, everything must be denied")
	}
	fail = false
	set.next = time.Time{} // skip the retry pacing
	if d.denied(pub) || d.denied(vpn) {
		t.Fatal("after a good read, the internet passes")
	}
	host = []netip.Prefix{netip.PrefixFrom(vpn, 32)} // an interface comes up
	time.Sleep(time.Millisecond)
	if !d.denied(vpn) {
		t.Fatal("an address that appeared later must be denied once re-read")
	}
	fail = true
	time.Sleep(time.Millisecond)
	if !d.denied(vpn) || d.denied(pub) {
		t.Fatal("a failed re-read must keep the last good view")
	}
}

// HostDeny against this machine: every interface address is denied.
func TestHostDenyThisHost(t *testing.T) {
	deny := HostDeny()
	as, err := net.InterfaceAddrs()
	if err != nil {
		t.Skip("interface addrs:", err)
	}
	for _, a := range as {
		n, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip, ok := netip.AddrFromSlice(n.IP)
		if !ok {
			continue
		}
		if !deny(ip) {
			t.Errorf("host address %s is not denied", ip)
		}
	}
	pfx, err := interfacePrefixes()
	if err != nil || len(pfx) == 0 {
		t.Fatalf("interfacePrefixes = %v, %v", pfx, err)
	}
	if deny(netip.MustParseAddr("192.0.2.1")) { // TEST-NET-1: never a host's
		t.Error("an address the host doesn't own must pass")
	}
}
