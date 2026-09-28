package tilesbx

// netcfg.go — a tile sandbox's egress (plans/tile-sandbox-runtime.md §4):
// the class it names, resolved at every start into a strict policy, and the
// relay that enforces it inside xbind. The relay is always there, in both
// modes; a sandbox reaches nothing of the host and nothing of xbind.

import (
	"net/netip"
	"sync"
	"syscall"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
)

// flowBudgetMax caps the flows of every tile-sandbox relay together (§4).
const flowBudgetMax = 16384

// flowBudgetSize is the shared budget: min(16384, RLIMIT_NOFILE/4) — a
// quarter of xbind's descriptors at most, whatever the sandboxes do.
func flowBudgetSize() int {
	var rl syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rl); err != nil || rl.Cur == 0 {
		return 1024
	}
	return int(min(uint64(flowBudgetMax), uint64(rl.Cur)/4))
}

// netState is the runtime's share of the relays: the flow budget every
// tile-sandbox relay draws on, and the one host-locality Deny they share
// (one netlink socket in xbind's network namespace, made on first use).
type netState struct {
	budget *relay.Budget
	listen []netip.AddrPort

	denyOnce sync.Once
	deny     relay.Deny
}

func (n *netState) hostDeny() relay.Deny {
	n.denyOnce.Do(func() { n.deny = relay.HostDeny(n.listen...) })
	return n.deny
}

// FlowBudget is the shared flow budget: flows held now, of its cap (the
// admin's health view).
func (m *Manager) FlowBudget() (used, cap int) { return m.net.budget.Used(), m.net.budget.Cap() }

// relayConfig is §4's relay for a sandbox under pol, exactly: the strict
// policy's Allow (never nil), the host resolver — or none, and every query
// REFUSED, when the policy is empty (none) —, Deny with xbind's listen
// addresses (no route to the host or to xbind, decided per flow), the
// strict public predicate for DNS pins, the per-sandbox flow caps and the
// shared budget, one packet processor, the gateway a dead end (no
// HostFwd, HostDial, Published or HairpinDial). The TUN stays the
// caller's: it closes it after Relay.Close returns.
func (m *Manager) relayConfig(tunFD int, pol sandbox.EgressPolicy) relay.Config {
	pol = pol.Strict()
	cfg := relay.Config{
		TunFD:        tunFD,
		Allow:        pol.Allow,
		Deny:         m.net.hostDeny(),
		DNSRefuse:    pol.Empty(),
		StrictPublic: pol.IsStrict(),
		MaxTCP:       flowsTCP,
		MaxUDP:       flowsUDP,
		Budget:       m.net.budget,
		Processors:   1,
		Gateway:      netip.MustParseAddr(sandbox.GatewayIP),
	}
	if !pol.Empty() {
		cfg.Resolver = sandbox.HostResolver()
	}
	if pol.HasHostRules() {
		cfg.AllowHost = pol.AllowsHost // DNS-pinned hostname rules (D35)
	}
	return cfg
}

// egress resolves a sandbox's egress selector for k's tile now: its class
// and the strict policy it runs under. "none" (or "") needs nothing; a
// class the tile no longer declares is an error.
func (m *Manager) egress(k Key, selector string) (EgressClass, sandbox.EgressPolicy, error) {
	if selector == "" || selector == "none" {
		return EgressClass{Class: "none", Reach: "none"}, sandbox.EgressPolicy{}.Strict(), nil
	}
	if m.deps.Net == nil {
		return EgressClass{}, sandbox.EgressPolicy{}, refuse(RefInvalid, "egress %s: this xbind has no sandbox network classes", selector)
	}
	c, pol, err := m.deps.Net.Egress(k.Tile, selector)
	if err != nil {
		return EgressClass{}, sandbox.EgressPolicy{}, refuse(RefInvalid, "egress %s: %v", selector, err)
	}
	return c, pol.Strict(), nil
}
