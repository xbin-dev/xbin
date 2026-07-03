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
