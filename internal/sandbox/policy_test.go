package sandbox

import (
	"net/netip"
	"testing"
)

func TestParseAndAllow(t *testing.T) {
	pol, err := Parse([]string{
		"apps/other:reader", // ignored (not net:)
		"net:internet:443",
		"net:10.0.0.0/24:5432",
		"net:192.168.1.5",
		"net:db.internal",
		"net:[2001:db8::]/32",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(pol.Rules) != 5 {
		t.Fatalf("want 5 rules, got %d", len(pol.Rules))
	}

	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	cases := []struct {
		ip   string
		port int
		want bool
	}{
		{"93.184.216.34", 443, true}, // public :443 → internet:443
		{"93.184.216.34", 80, false}, // public but wrong port
		{"10.0.0.9", 5432, true},     // cidr + port
		{"10.0.0.9", 22, false},      // cidr, wrong port
		{"192.168.1.5", 22, true},    // host addr, any port
		{"192.168.1.6", 22, false},   // not granted
		{"127.0.0.1", 443, false},    // loopback not "internet"
		{"172.16.0.1", 443, false},   // RFC1918 not "internet"
		{"2001:db8::1", 80, true},    // v6 cidr, any port
		{"2001:dbff::1", 80, false},  // outside v6 cidr
	}
	for _, c := range cases {
		if got := pol.Allow(ip(c.ip), c.port); got != c.want {
			t.Errorf("Allow(%s,%d)=%v want %v", c.ip, c.port, got, c.want)
		}
	}

	if !pol.AllowsHost("db.internal", 5432) {
		t.Error("host rule should allow db.internal")
	}
	if pol.AllowsHost("evil.example", 443) {
		t.Error("host rule should not allow evil.example")
	}
	if pol.Empty() {
		t.Error("policy is not empty")
	}
	if !(EgressPolicy{}).Empty() {
		t.Error("zero policy is empty")
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"apps/x:reader", "net:", "net:10.0.0.0/99", "net:internet:0", "net:internet:70000"} {
		if bad == "apps/x:reader" {
			continue // handled by Parse skip, not ParseRule
		}
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("ParseRule(%q) should error", bad)
		}
	}
}

// Host rules may carry one '*' (org network sets, D54): any depth below the
// apex matches, the apex itself matches (allowance carve-out), and unrelated
// names that merely end in the suffix do not.
func TestAllowsHostGlob(t *testing.T) {
	pol, err := Parse([]string{"net:*.github.com:443", "net:api.stripe.com", "net:cdn-*.example.net"})
	if err != nil {
		t.Fatal(err)
	}
	if !pol.HasHostRules() {
		t.Fatal("host rules expected")
	}
	cases := []struct {
		name string
		port int
		want bool
	}{
		{"api.github.com", 443, true},
		{"a.b.github.com", 443, true}, // one '*' spans labels
		{"github.com", 443, true},     // apex carve-out
		{"GITHUB.COM.", 443, true},    // case/trailing dot normalized
		{"api.github.com", 80, false}, // port pinned
		{"evilgithub.com", 443, false},
		{"github.com.evil.example", 443, false},
		{"api.stripe.com", 80, true},   // exact rule, any port
		{"www.stripe.com", 443, false}, // exact rule doesn't glob
		{"cdn-7.example.net", 443, true},
		{"cdn.example.net", 443, false},
	}
	for _, c := range cases {
		if got := pol.AllowsHost(c.name, c.port); got != c.want {
			t.Errorf("AllowsHost(%q, %d) = %v, want %v", c.name, c.port, got, c.want)
		}
	}
}

// Reach speaks the contract's vocabulary and never claims less than Allow
// admits.
func TestReach(t *testing.T) {
	for _, c := range []struct {
		rules []string
		want  string
	}{
		{nil, ReachNone},
		{[]string{"apps/x:reader"}, ReachNone}, // not a net: grant
		{[]string{"net:internet"}, ReachInternet},
		{[]string{"net:internet:443", "net:api.example.com:443"}, ReachInternet},
		{[]string{"net:*.github.com:443"}, ReachInternet},
		{[]string{"net:8.8.8.0/24", "net:1.1.1.1:53"}, ReachInternet},
		{[]string{"net:[2606:4700::]/32"}, ReachInternet},
		{[]string{"net:[::ffff:8.8.8.8]"}, ReachInternet},
		{[]string{"net:internet", "net:192.168.1.0/24"}, ReachOpen},
		{[]string{"net:10.0.0.5:5432"}, ReachOpen},
		{[]string{"net:0.0.0.0/0"}, ReachOpen},   // holds every private range
		{[]string{"net:8.0.0.0/5"}, ReachOpen},   // 8.0.0.0–15.255.255.255 holds 10/8
		{[]string{"net:172.0.0.0/8"}, ReachOpen}, // holds 172.16/12
		{[]string{"net:127.0.0.1"}, ReachOpen},   // loopback
		{[]string{"net:169.254.169.254"}, ReachOpen},
		{[]string{"net:224.0.0.0/4"}, ReachOpen}, // multicast
		{[]string{"net:0.0.0.0"}, ReachOpen},     // unspecified
		{[]string{"net:[fd00::]/8"}, ReachOpen},  // ULA
		{[]string{"net:[fe80::1]"}, ReachOpen},   // link-local
		{[]string{"net:[::]/0"}, ReachOpen},
		{[]string{"net:[::ffff:0:0]/96"}, ReachOpen},   // every IPv4-mapped address
		{[]string{"net:[::ffff:10.0.0.1]"}, ReachOpen}, // mapped private
		{[]string{"net:[::ffff:0.0.0.0]"}, ReachOpen},  // mapped unspecified
	} {
		pol, err := Parse(c.rules)
		if err != nil {
			t.Fatalf("%v: %v", c.rules, err)
		}
		if got := pol.Reach(); got != c.want {
			t.Errorf("Reach(%v) = %q, want %q", c.rules, got, c.want)
		}
	}
}

// publicPrefix agrees with isPublic (and, strict, with isPublicStrict),
// address by address: every /16 of IPv4 (the ranges either test refuses are
// /16-aligned or wider, bar 0.0.0.0/32 at the start of 0.0.0.0/16), and the
// same IPv4-mapped.
func TestPublicPrefixMatchesIsPublic(t *testing.T) {
	for _, strict := range []bool{false, true} {
		pub := isPublic
		if strict {
			pub = isPublicStrict
		}
		for i := 0; i < 1<<16; i++ {
			first := netip.AddrFrom4([4]byte{byte(i >> 8), byte(i), 0, 0})
			last := netip.AddrFrom4([4]byte{byte(i >> 8), byte(i), 255, 255})
			want := pub(first) && pub(last)
			if got := publicPrefix(netip.PrefixFrom(first, 16), strict); got != want {
				t.Fatalf("strict=%v: publicPrefix(%s/16) = %v, isPublic says %v", strict, first, got, want)
			}
			mapped := netip.AddrFrom16(first.As16())
			if got := publicPrefix(netip.PrefixFrom(mapped, 112), strict); got != want {
				t.Fatalf("strict=%v: publicPrefix(%s/112) = %v, isPublic says %v", strict, mapped, got, want)
			}
		}
		for _, s := range []string{"::", "::1", "fc00::1", "fdff::1", "fe80::1", "febf::1", "ff00::1", "ff02::1"} {
			a := netip.MustParseAddr(s)
			if pub(a) || publicPrefix(netip.PrefixFrom(a, 128), strict) {
				t.Errorf("strict=%v: %s: isPublic=%v publicPrefix=%v, want both false", strict, s, pub(a), publicPrefix(netip.PrefixFrom(a, 128), strict))
			}
		}
		for _, s := range []string{"2001:db8::1", "2606:4700::1", "fbff::1", "fec0::1", "64:ff9b:2::1"} {
			a := netip.MustParseAddr(s)
			if !pub(a) || !publicPrefix(netip.PrefixFrom(a, 128), strict) {
				t.Errorf("strict=%v: %s: isPublic=%v publicPrefix=%v, want both true", strict, s, pub(a), publicPrefix(netip.PrefixFrom(a, 128), strict))
			}
		}
		for _, s := range []string{"64:ff9b::808:808", "64:ff9b:1::1"} { // NAT64: public unless strict
			a := netip.MustParseAddr(s)
			if pub(a) == strict || publicPrefix(netip.PrefixFrom(a, 128), strict) == strict {
				t.Errorf("strict=%v: %s: isPublic=%v publicPrefix=%v", strict, s, pub(a), publicPrefix(netip.PrefixFrom(a, 128), strict))
			}
		}
	}
}

// net:internet judges an IPv4-mapped address as the IPv4 address it names:
// ::ffff:0.0.0.0 would dial the host.
func TestInternetRefusesMappedSpecials(t *testing.T) {
	pol, _ := Parse([]string{"net:internet"})
	for _, s := range []string{"::ffff:0.0.0.0", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:169.254.169.254"} {
		if pol.Allow(netip.MustParseAddr(s), 80) {
			t.Errorf("net:internet admits %s", s)
		}
	}
	if !pol.Allow(netip.MustParseAddr("::ffff:8.8.8.8"), 80) {
		t.Error("net:internet refuses a mapped public address")
	}
}
