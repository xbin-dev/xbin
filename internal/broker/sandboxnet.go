package broker

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/users"
)

// Sandbox network classes (plans/tile-sandbox-runtime.md §4, D120). A
// sandbox manager declares request-side interface slots of kind
// `sandbox-net`; an approver binds each one like a `net` slot — D20's deny
// row, D26 org admins within the allowance, D54 org-set coverage, D65
// (a named set is a workspace-admin act), D88 owner self-approval and the
// inert notes all apply, because bindingTargetsPaired and validateBinding
// treat the kind like `net` — to a builtin network: none, internet,
// internet:<spec,…>, lan:<cidr>, org, personal or set:<name>. Never host,
// a provider tile or a set that says host: a sandbox has no route to the
// host and never splices through a tile.
//
// A class is not the tile's own egress: the manager's `net` slot is
// separate, and no class change restarts its backend. A sandbox names a
// class as "class:<slot>" (or "none"), and the runtime resolves it at
// every start (SandboxEgress). An unbound class is none — there is no
// D54/D88 auto-default, so a class never silently gets the org network.

// The egress selectors a tile sandbox takes.
const (
	SandboxClassNone   = "none"   // no network: an empty policy, DNS refused
	SandboxClassPrefix = "class:" // "class:<slot>", one of the tile's sandbox-net slots
)

// SandboxNet is one class resolved now: what its binding gives a sandbox.
type SandboxNet struct {
	Class string   `json:"class"`          // "none" | "class:<slot>"
	Slot  string   `json:"slot,omitempty"` // the sandbox-net slot ("" for none)
	Ref   string   `json:"ref,omitempty"`  // the stored binding ref ("" = unbound)
	Reach string   `json:"reach"`          // none | internet | open (EgressPolicy.Reach)
	Rules []string `json:"rules,omitempty"`
	// Note says why the class gives less than its binding names: inert (the
	// binding resolves to nothing — a set narrowed, a transfer, a deny row)
	// or narrowed (host networking left out).
	Note string `json:"note,omitempty"`
	// Policy is the relay policy a sandbox of this class runs under.
	Policy sandbox.EgressPolicy `json:"-"`
	inert  bool                 // bound to something, resolving to nothing
}

// netKind reports whether an interface kind takes network refs — the kinds
// D20, D26, D54 and D88 judge alike.
func netKind(kind string) bool { return kind == "net" || kind == registry.KindSandboxNet }

// sandboxNetSlots lists a component's sandbox-net classes, sorted. A slot
// outside the name grammar is a manifest error and no class.
func sandboxNetSlots(c *registry.Component) []string {
	var out []string
	for slot, def := range c.Manifest.Interfaces {
		if def.Kind == registry.KindSandboxNet && registry.ValidSandboxNetSlot(slot) {
			out = append(out, slot)
		}
	}
	sort.Strings(out)
	return out
}

// isSandboxNetSlot reports whether comp's slot is of kind sandbox-net.
func (b *Broker) isSandboxNetSlot(comp, slot string) bool {
	c, ok := b.Reg.Component(comp)
	return ok && c.Manifest.Interfaces[slot].Kind == registry.KindSandboxNet
}

// hasSandboxNet reports whether comp declares any sandbox-net slot.
func (b *Broker) hasSandboxNet(comp string) bool {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return false
	}
	for _, def := range c.Manifest.Interfaces {
		if def.Kind == registry.KindSandboxNet {
			return true
		}
	}
	return false
}

// SandboxNetClasses lists the classes a tile's sandboxes may name, resolved
// now: none first, then each sandbox-net slot by name. GET
// /sandboxes/runtime answers it, and a manager builds its contract's
// hello.egress from it.
func (b *Broker) SandboxNetClasses(tile string) []SandboxNet {
	out := []SandboxNet{{Class: SandboxClassNone, Reach: sandbox.ReachNone}}
	if c, ok := b.Reg.Component(tile); ok {
		for _, slot := range sandboxNetSlots(c) {
			out = append(out, b.resolveSandboxNet(tile, slot))
		}
	}
	return out
}

// SandboxEgress resolves a sandbox's egress selector — "" or "none", or
// "class:<slot>" naming one of the tile's sandbox-net slots — into the
// policy it runs under. The runtime calls it at every start, so a binding
// changed since the sandbox was defined applies; an error means the
// selector names no class of this tile (any longer).
func (b *Broker) SandboxEgress(tile, class string) (SandboxNet, error) {
	if class == "" || class == SandboxClassNone {
		return SandboxNet{Class: SandboxClassNone, Reach: sandbox.ReachNone}, nil
	}
	slot, ok := strings.CutPrefix(class, SandboxClassPrefix)
	if ok {
		if c, found := b.Reg.Component(tile); found && slices.Contains(sandboxNetSlots(c), slot) {
			return b.resolveSandboxNet(tile, slot), nil
		}
	}
	return SandboxNet{}, fmt.Errorf("egress %q names no network class of %s: it takes none or class:<slot>, one of the tile's sandbox-net slots", class, tile)
}

// resolveSandboxNet is one class's resolution — netBinding + EgressFor for
// a sandbox-net slot, with no auto-default and no host networking.
func (b *Broker) resolveSandboxNet(tile, slot string) SandboxNet {
	out := SandboxNet{Class: SandboxClassPrefix + slot, Slot: slot, Reach: sandbox.ReachNone}
	binding := b.Reg.Workspace().Bindings[tile][slot]
	ref := binding.First()
	out.Ref = ref
	if ref == "" || ref == NetRefNone {
		return out
	}
	inert := func(why string) SandboxNet {
		out.Note, out.inert = why, true
		return out
	}
	var ceil users.Ceiling
	if b.Users != nil {
		ceil = b.Users.Ceiling(tile)
	}
	if row, ok := ceil.DenyRow(users.PolicyDenyNet); ok {
		return inert(fmt.Sprintf("a policy row for tiles matching %q denies net — no network for these sandboxes", row.Tiles))
	}
	hasSets := ceil.OwnerOrg() != "" && ceil.HasNetSets()
	var rules []string // in the network-set spelling: internet[:<spec>], lan:<cidr>, host
	switch {
	case ref == "internet", strings.HasPrefix(ref, "lan:"):
		rules = []string{ref}
	case strings.HasPrefix(ref, "internet:"):
		for _, spec := range strings.Split(strings.TrimPrefix(ref, "internet:"), ",") {
			if spec = strings.TrimSpace(spec); spec != "" {
				rules = append(rules, "internet:"+spec)
			}
		}
	case ref == NetRefOrg:
		if !hasSets {
			return inert("bound to org network but its owner has no network sets attached — no network until a workspace admin attaches one")
		}
		rules = ceil.NetRules()
	case ref == NetRefPersonal:
		uid, _, r := b.personalNet(tile)
		if uid == "" {
			return inert("bound to the personal network but its owner has none (no personal network sets)")
		}
		rules = r
	case strings.HasPrefix(ref, NetRefSet):
		var ns users.NetSet
		found := false
		if name, _ := netSetName(ref); b.Users != nil {
			ns, found = b.Users.NetSet(name)
		}
		if !found {
			return inert("bound to network " + ref + ", which no longer exists — rebind (GET /net-sets lists the sets)")
		}
		rules = ns.Rules
	default: // hand-edited: host or a provider tile
		return inert(ref + " is not a sandbox network (never host networking or a provider tile) — rebind the class")
	}
	if hasSets && ref != NetRefOrg { // D54: an explicit ref outside the org's sets
		if why := b.netUncovered(tile, slot, binding, ceil); why != "" {
			return inert(why)
		}
	}
	targets, host := netRuleTargets(rules)
	pol, err := sandbox.Parse(targets)
	if err != nil {
		return inert("its network has a rule the relay can't use: " + err.Error())
	}
	if pol.Empty() {
		return inert("its network has no relay rules (host networking or provider tiles only) — no network for these sandboxes")
	}
	out.Policy, out.Reach, out.Rules = pol, pol.Reach(), pol.Strings()
	if host {
		out.Note = "its network says host, which a sandbox never gets: the class has the other rules"
	}
	return out
}

// validateNetRef checks one ref bound to a net or sandbox-net slot. The
// builtins the kinds share (none, internet[:<spec>], lan:, org, personal,
// set:) are judged alike; host networking and provider tiles are net-only,
// and a sandbox-net lan: must name an address or CIDR.
func (b *Broker) validateNetRef(comp, slot string, def registry.Iface, ref string) error {
	prov, inst := splitRef(ref)
	sbx := def.Kind == registry.KindSandboxNet
	switch {
	case sbx && !registry.ValidSandboxNetSlot(slot):
		return fmt.Errorf("%q is not a valid sandbox-net slot name ([a-z0-9][a-z0-9_-]{0,31}) — fix the manifest", slot)
	case inst != "":
		return fmt.Errorf("%s bindings take no #instance", def.Kind)
	case prov == NetRefNone, prov == "internet":
		return nil
	case strings.HasPrefix(prov, "internet:"): // filtered internet (D35)
		return validateFilteredInternet(prov)
	case strings.HasPrefix(prov, "lan:"):
		if r, err := sandbox.ParseRule("net:" + strings.TrimPrefix(prov, "lan:")); sbx && (err != nil || !r.Net.IsValid()) {
			return fmt.Errorf("%q: lan: takes an address or CIDR, optionally :port", prov)
		}
		return nil
	case prov == NetRefOrg: // the owning org's network sets (D54)
		if b.Users == nil {
			return fmt.Errorf("org egress needs the user store")
		}
		if _, isOrg := b.Users.OwnerOrg(comp); !isOrg {
			return fmt.Errorf("org egress is for org-owned tiles; %s is %s — bind a concrete provider", comp, ownerLabel(b.Users.Owner(comp)))
		}
		return nil
	case prov == NetRefPersonal: // the owner's personal network sets (D88)
		if b.Users == nil || !strings.HasPrefix(b.Users.Owner(comp), users.OwnerKindUser+":") {
			return fmt.Errorf("personal egress is for personal (user-owned) tiles; %s is %s", comp, ownerLabel(b.Users.Owner(comp)))
		}
		return nil
	case strings.HasPrefix(prov, NetRefSet): // a named network set (D65; ws-admin only — apiBindingSet)
		name, _ := netSetName(prov)
		if b.Users == nil {
			return fmt.Errorf("network-set bindings need the user store")
		}
		ns, ok := b.Users.NetSet(name)
		if !ok {
			return fmt.Errorf("no such network set %q (GET /net-sets lists them)", name)
		}
		m, host := netSetMaterial(ns.Rules)
		if !host && len(m) == 0 {
			return fmt.Errorf("network set %q has no relay reach (provider rules only) — bind the provider tile, or none", name)
		}
		if sbx && host {
			return fmt.Errorf("network set %q says host — a sandbox never gets host networking; bind a set without it", name)
		}
		return nil
	case sbx && prov == "host":
		return fmt.Errorf("a sandbox never shares the host's network — bind internet, lan:<cidr>, a network set or none")
	case sbx:
		return fmt.Errorf("%s is not a sandbox network: sandbox-net classes bind none, internet[:<spec>,…], lan:<cidr>, org, personal or set:<name> (never a provider tile)", prov)
	case prov == "host":
		return nil
	}
	if p, ok := b.Reg.Component(prov); !ok || !providesNet(p) {
		return fmt.Errorf("%s does not provide net", prov)
	}
	return nil
}

// sandboxNetBuiltinOptions is a sandbox-net slot's picker: the net builtins
// without host and provider tiles; a set that says host is blocked, and an
// org or personal network that says host is labelled as losing it.
func (b *Broker) sandboxNetBuiltinOptions(comp string, wsAdmin bool) []bindOption {
	var ceil users.Ceiling
	if b.Users != nil {
		ceil = b.Users.Ceiling(comp)
	}
	var out []bindOption
	for _, o := range b.netBuiltinOptions(comp, wsAdmin) {
		switch {
		case o.ID == "host":
			continue
		case o.ID == NetRefNone:
			o.Label = "none — no network for these sandboxes (the same as unbound)"
		case o.ID == NetRefOrg && ceil.NetHost():
			o.Label += " — without host: a sandbox never gets host networking"
		case o.ID == NetRefPersonal:
			if _, _, rules := b.personalNet(comp); slices.Contains(rules, "host") {
				o.Label += " — without host: a sandbox never gets host networking"
			}
		case strings.HasPrefix(o.ID, NetRefSet) && !o.Blocked:
			if name, _ := netSetName(o.ID); b.Users != nil {
				if ns, ok := b.Users.NetSet(name); ok {
					if _, host := netSetMaterial(ns.Rules); host {
						o.Label += " — says host: never a sandbox network"
						o.Blocked = true
					}
				}
			}
		}
		out = append(out, o)
	}
	return out
}

// addSandboxNetView fills the bindings answer's sandbox-net parts for one
// component in view: its classes' option list (sandboxNetOptions, keyed by
// component like netOptions — every class of a tile offers the same) and
// the classes whose stored binding resolves to no network (inert, by slot).
func (b *Broker) addSandboxNetView(comp string, wsAdmin, self bool, p auth.Principal, opts map[string][]bindOption, inert map[string]map[string]string) {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return
	}
	slots := sandboxNetSlots(c)
	if len(slots) == 0 {
		return
	}
	o := b.sandboxNetBuiltinOptions(comp, wsAdmin)
	if self && p.User != nil {
		o = b.markSelfBlocked(p.User.ID, o)
	}
	opts[comp] = o
	for _, slot := range slots {
		if sn := b.resolveSandboxNet(comp, slot); sn.inert {
			if inert[comp] == nil {
				inert[comp] = map[string]string{}
			}
			inert[comp][slot] = sn.Note
		}
	}
}

// sandboxNetChanged tells the runtime that a tile's classes may resolve
// differently now (a bind, an unbind, a network-set edit or attachment, a
// transfer): it re-resolves each running sandbox's class. The manager's
// backend is not restarted.
func (b *Broker) sandboxNetChanged(tile string) {
	if b.OnSandboxNetChange != nil {
		b.OnSandboxNetChange(tile)
	}
}
