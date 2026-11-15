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
		cfg := e.m.relayConfig(7, pol, true) // asked for, but this xbind has no LLM listener: still a dead end
		if cfg.TunFD != 7 || cfg.CloseTUN {  // the runtime closes it, after Relay.Close
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

// covers D226 — the LLM door: a sandbox created with net.llm gets exactly
// one gateway port forwarded, to xbind's LLM-only listener, whatever its
// egress (none included) and with every other part of §4's relay as it
// was; one that didn't ask gets none; and an xbind without the listener
// gives none to anyone.
func TestRelayConfigLLMDoor(t *testing.T) {
	sock := ""
	e := newEnv(t, func(o *Options) { o.Deps.LLMSock = func() string { return sock } })
	k := Key{Tile: "apps/mgr"}
	_, pol, err := e.m.egress(k, "none")
	if err != nil {
		t.Fatal(err)
	}
	if cfg := e.m.relayConfig(7, pol, true); cfg.Doors != nil {
		t.Fatalf("no listener, yet a door: %v", cfg.Doors)
	}
	sock = "/run/xbin/llm.sock"
	if cfg := e.m.relayConfig(7, pol, false); cfg.Doors != nil {
		t.Fatalf("a sandbox that didn't ask has a door: %v", cfg.Doors)
	}
	cfg := e.m.relayConfig(7, pol, true)
	if len(cfg.Doors) != 1 || cfg.Doors[LLMGatewayPort] != "unix:/run/xbin/llm.sock" {
		t.Fatalf("the door: %v", cfg.Doors)
	}
	// the rest is none's relay: nothing allowed, no DNS, the gateway's network denied, no other forward
	if cfg.HostFwd != nil || cfg.HostDial != nil || cfg.Published != nil || cfg.HairpinDial != nil || !cfg.DNSRefuse || cfg.Resolver != "" ||
		cfg.Allow(netip.MustParseAddr("1.1.1.1"), 443) || !cfg.Deny(netip.MustParseAddr(sandbox.GatewayIP)) {
		t.Errorf("the door changed the rest of the relay: %+v", cfg)
	}
	if LLMURL != "http://10.0.2.2:4180/v1" {
		t.Errorf("LLMURL %q", LLMURL)
	}
}

// covers D226 — net.llm is set at create, answered as the connector's
// address in the sandbox while xbind has the listener, and kept by a PATCH
// of the egress (a manager that never heard of it changes the egress alone).
func TestNetLLMDefinition(t *testing.T) {
	sock := "/run/xbin/llm.sock"
	e := newEnv(t, func(o *Options) { o.Deps.LLMSock = func() string { return sock } })
	plain := e.create(ns("plain"))
	if plain.Net.LLM != "" {
		t.Fatalf("a sandbox that didn't ask: %+v", plain.Net)
	}
	body := ns("coder")
	body["net"] = map[string]any{"llm": true} // no egress named: none
	in := e.create(body)
	if in.Net.LLM != LLMURL || in.Net.Egress != "none" {
		t.Fatalf("created with net.llm: %+v", in.Net)
	}
	w := e.do(mgr, "PATCH", "/sandboxes/coder", map[string]any{"net": map[string]any{"egress": "none"}, "labels": map[string]string{"a": "b"}})
	e.want(w, 200, "")
	if in = e.info(w); in.Net.LLM != LLMURL {
		t.Fatalf("a PATCH of the egress dropped net.llm: %+v", in.Net)
	}
	w = e.do(mgr, "PATCH", "/sandboxes/plain", map[string]any{"net": map[string]any{"egress": "none", "llm": true}})
	e.want(w, 200, "")
	if in = e.info(w); in.Net.LLM != "" {
		t.Fatalf("a PATCH turned net.llm on: %+v", in.Net)
	}
	sock = "" // this xbind forwards none: the caller learns it from the row
	if in = e.info(e.do(mgr, "GET", "/sandboxes/coder", nil)); in.Net.LLM != "" {
		t.Fatalf("no listener, yet the row names an address: %+v", in.Net)
	}
}
