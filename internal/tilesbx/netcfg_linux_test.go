//go:build linux

package tilesbx

import (
	"net/netip"
	"testing"

	"github.com/xbin-dev/xbin/internal/sandbox"
)

// laxNet answers a class's policy as parsed, not made strict: the runtime
// must make it strict itself.
type laxNet struct{ fakeNet }

func (l laxNet) Egress(tile, class string) (EgressClass, sandbox.EgressPolicy, error) {
	c, _, err := l.fakeNet.Egress(tile, class)
	pol, _ := sandbox.Parse(c.Rules)
	return c, pol, err
}

// A tile sandbox's relay is §4's, exactly: the strict policy's Allow, the
// host Deny carrying xbind's listen addresses, DNS refused (and no resolver)
// under none, the per-sandbox flow caps and the shared budget, one packet
// processor, the gateway a dead end — nothing of a backend's forwarding.
func TestRelayConfig(t *testing.T) {
	// not an address of this host: only its being xbind's listener denies it
	listen := netip.MustParseAddrPort("192.0.2.10:8642")
	net := fakeNet{"apps/mgr": {
		{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet", Rules: []string{"net:internet"}},
		{Class: "class:docs", Slot: "docs", Ref: "internet:docs.example.com", Reach: "internet", Rules: []string{"net:docs.example.com:443"}},
	}}
	e := newEnv(t, func(o *Options) { o.Deps.Listen, o.Deps.Net = []netip.AddrPort{listen}, laxNet{net} })
	k := Key{Tile: "apps/mgr"}
	a := func(s string) netip.Addr { return netip.MustParseAddr(s) }

	for _, class := range []string{"none", "class:internet", "class:docs"} {
		_, pol, err := e.m.egress(k, class)
		if err != nil {
			t.Fatalf("%s: %v", class, err)
		}
		cfg := e.m.relayConfig(7, pol)
		if cfg.TunFD != 7 || cfg.CloseTUN { // the runtime closes it, after Relay.Close
			t.Errorf("%s: the TUN: %d close=%v", class, cfg.TunFD, cfg.CloseTUN)
		}
		if cfg.Allow == nil || cfg.Deny == nil || !cfg.StrictPublic {
			t.Fatalf("%s: allow %v, deny %v, strict %v", class, cfg.Allow != nil, cfg.Deny != nil, cfg.StrictPublic)
		}
		for _, ip := range []string{listen.Addr().String(), "127.0.0.1", "10.0.2.2", "169.254.169.254"} {
			if !cfg.Deny(a(ip)) {
				t.Errorf("%s: %s isn't denied", class, ip)
			}
		}
		if cfg.MaxTCP != 1024 || cfg.MaxUDP != 256 || cfg.Budget != e.m.net.budget || cfg.Budget.Cap() < 1 || cfg.Processors != 1 {
			t.Errorf("%s: caps tcp %d udp %d, budget %p (shared %p), processors %d", class, cfg.MaxTCP, cfg.MaxUDP, cfg.Budget, e.m.net.budget, cfg.Processors)
		}
		if cfg.Gateway != a(sandbox.GatewayIP) || cfg.HostFwd != nil || cfg.HostDial != nil || cfg.Published != nil ||
			cfg.HairpinDial != nil || cfg.TileIP.IsValid() {
			t.Errorf("%s: the gateway isn't a dead end: %+v", class, cfg)
		}
		switch class {
		case "none":
			if !cfg.DNSRefuse || cfg.Resolver != "" || cfg.AllowHost != nil || cfg.Allow(a("1.1.1.1"), 443) {
				t.Errorf("none: dnsRefuse %v, resolver %q, allowHost %v, allows 1.1.1.1", cfg.DNSRefuse, cfg.Resolver, cfg.AllowHost != nil)
			}
		case "class:internet":
			if cfg.DNSRefuse || cfg.Resolver != sandbox.HostResolver() || cfg.AllowHost != nil {
				t.Errorf("internet: dnsRefuse %v, resolver %q, allowHost %v", cfg.DNSRefuse, cfg.Resolver, cfg.AllowHost != nil)
			}
			if !cfg.Allow(a("1.1.1.1"), 443) || cfg.Allow(a("100.64.0.1"), 443) || cfg.Allow(a("10.1.2.3"), 443) {
				t.Error("internet isn't the strict one: public allowed, CGNAT and private refused")
			}
		case "class:docs":
			if cfg.AllowHost == nil || !cfg.AllowHost("docs.example.com", 443) || cfg.AllowHost("docs.example.com", 80) {
				t.Error("a host rule's class pins its names (AllowHost)")
			}
		}
	}
}
