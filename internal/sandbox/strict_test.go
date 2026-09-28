package sandbox

import (
	"net/netip"
	"testing"
)

// Strict is a tile sandbox's "internet": the contract's no private or local
// network, so CGNAT (Tailscale), benchmarking, reserved and NAT64 ranges are
// refused too — by Allow, Reach and Covers. Without Strict, net:internet is
// what it always was.
func TestStrictInternet(t *testing.T) {
	pol, err := Parse([]string{"net:internet"})
	if err != nil {
		t.Fatal(err)
	}
	strict := pol.Strict()
	if pol.IsStrict() || !strict.IsStrict() {
		t.Fatal("Strict returns a strict copy and leaves the policy alone")
	}
	for _, s := range []string{
		"100.64.0.1", "100.100.100.100", "100.127.255.254", // CGNAT, Tailscale's
		"198.18.0.1", "198.19.255.254", // benchmarking
		"240.0.0.1", "255.255.255.254", // reserved
		"64:ff9b::808:808", "64:ff9b::1:2", // NAT64
		"64:ff9b:1::1", "64:ff9b:1:ffff::1", // local-use NAT64
		"::ffff:100.64.0.1", "::ffff:198.18.0.1", // IPv4-mapped
	} {
		ip := netip.MustParseAddr(s)
		if strict.Allow(ip, 443) {
			t.Errorf("strict internet admits %s", s)
		}
		if !pol.Allow(ip, 443) {
			t.Errorf("net:internet (not strict) must still admit %s, as before", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "100.63.255.255", "100.128.0.1", "198.17.255.255", "198.20.0.1",
		"239.255.255.255" /* multicast: refused by both */, "2606:4700:4700::1111", "64:ff9b:2::1"} {
		ip := netip.MustParseAddr(s)
		if strict.Allow(ip, 443) != pol.Allow(ip, 443) {
			t.Errorf("%s: strict %v, not strict %v — they differ only on the five ranges", s, strict.Allow(ip, 443), pol.Allow(ip, 443))
		}
	}
	if !strict.Allow(netip.MustParseAddr("1.1.1.1"), 53) {
		t.Error("strict internet admits a public address")
	}
	// A prefix rule is exact under Strict: it isn't an internet test.
	lan, _ := Parse([]string{"net:100.64.0.0/10"})
	if !lan.Strict().Allow(netip.MustParseAddr("100.64.1.2"), 22) {
		t.Error("a prefix rule for the CGNAT range admits it under Strict")
	}
}

// Reach under Strict: a prefix counts as internet only when it is wholly
// strictly public; not strict, every old answer stands.
func TestStrictReach(t *testing.T) {
	for _, c := range []struct {
		rules          []string
		plain, strictR string
	}{
		{nil, ReachNone, ReachNone},
		{[]string{"net:internet"}, ReachInternet, ReachInternet},
		{[]string{"net:internet:443", "net:api.example.com:443"}, ReachInternet, ReachInternet},
		{[]string{"net:100.64.0.0/10"}, ReachInternet, ReachOpen},
		{[]string{"net:100.100.0.0/16:41641"}, ReachInternet, ReachOpen},
		{[]string{"net:198.18.0.0/15"}, ReachInternet, ReachOpen},
		{[]string{"net:240.0.0.0/4"}, ReachInternet, ReachOpen},
		{[]string{"net:[64:ff9b::]/96"}, ReachInternet, ReachOpen},
		{[]string{"net:[64:ff9b:1::]/48"}, ReachInternet, ReachOpen},
		{[]string{"net:[::ffff:100.64.0.0]/106"}, ReachInternet, ReachOpen},
		{[]string{"net:100.0.0.0/8"}, ReachInternet, ReachOpen}, // holds 100.64/10
		{[]string{"net:8.8.8.0/24", "net:1.1.1.1:53"}, ReachInternet, ReachInternet},
		{[]string{"net:100.63.0.0/16", "net:100.128.0.0/9"}, ReachInternet, ReachInternet}, // around CGNAT
		{[]string{"net:10.0.0.5:5432"}, ReachOpen, ReachOpen},
		{[]string{"net:0.0.0.0/0"}, ReachOpen, ReachOpen},
		{[]string{"net:[::]/0"}, ReachOpen, ReachOpen},
	} {
		pol, err := Parse(c.rules)
		if err != nil {
			t.Fatalf("%v: %v", c.rules, err)
		}
		if got := pol.Reach(); got != c.plain {
			t.Errorf("Reach(%v) = %q, want %q", c.rules, got, c.plain)
		}
		if got := pol.Strict().Reach(); got != c.strictR {
			t.Errorf("Strict().Reach(%v) = %q, want %q", c.rules, got, c.strictR)
		}
	}
}

// Covers between strict and plain policies: a strict internet is narrower
// than a plain one, so it never covers it (nor a plain host rule, whose pins
// may land in the ranges Strict refuses); the other way round it does.
func TestStrictCovers(t *testing.T) {
	parse := func(rules ...string) EgressPolicy {
		pol, err := Parse(rules)
		if err != nil {
			t.Fatal(err)
		}
		return pol
	}
	inet := parse("net:internet")
	for _, c := range []struct {
		name string
		p, q EgressPolicy
		want bool
	}{
		{"strict ⊇ strict", inet.Strict(), inet.Strict(), true},
		{"plain ⊇ strict", inet, inet.Strict(), true},
		{"strict ⊉ plain", inet.Strict(), inet, false},
		{"strict internet ⊉ plain host", inet.Strict(), parse("net:api.example.com:443"), false},
		{"strict internet ⊇ strict host", inet.Strict(), parse("net:api.example.com:443").Strict(), true},
		{"strict host ⊇ strict host", parse("net:*.example.com").Strict(), parse("net:api.example.com").Strict(), true},
		{"strict internet ⊉ CGNAT prefix", inet.Strict(), parse("net:100.64.0.0/10").Strict(), false},
		{"plain internet ⊇ CGNAT prefix", inet, parse("net:100.64.0.0/10"), true},
		{"strict internet ⊇ a public prefix of a plain policy", inet.Strict(), parse("net:8.8.8.0/24"), true},
		{"a prefix rule ⊇ the same prefix, strict or not", parse("net:100.64.0.0/10").Strict(), parse("net:100.64.1.0/24"), true},
	} {
		if got := c.p.Covers(c.q); got != c.want {
			t.Errorf("%s: Covers = %v, want %v", c.name, got, c.want)
		}
	}
}
