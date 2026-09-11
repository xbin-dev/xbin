package broker

import (
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/users"
)

// Named network sets beyond the org union (plans/DECISIONS.md D65): a set
// is a binding ref — "set:<name>", a WORKSPACE-ADMIN act (apiBindingSet
// refuses it from org admins; they bind `org`, the union of their org's
// sets) — and the set half of a net slot's picker. On an org-owned tile
// with sets the ref must still be inside the union (D54's ceiling, judged
// by the set's material rules — bindingTargetsPaired); provider-only sets
// are not bindable (they would mean "offline", and `none` says that
// honestly). The terminal side (a set as a scope) is termNetSets.

// NetRefSet prefixes a set binding ref. Same spelling as term.ScopeSetPrefix.
const NetRefSet = "set:"

func netSetName(ref string) (string, bool) { return strings.CutPrefix(ref, NetRefSet) }

// netSetMaterial splits a set's rules into what a binding or a terminal can
// materialize — the relay/host rules, in the allowance spelling — and
// whether host networking is among them. Provider rules are allowance-only
// (a set can't splice to several provider tiles) and confer nothing here.
func netSetMaterial(rules []string) (material []string, host bool) {
	for _, r := range rules {
		switch {
		case r == "host":
			host = true
			material = append(material, r)
		case strings.HasPrefix(r, "provider:"):
		default:
			material = append(material, r)
		}
	}
	return material, host
}

// netSetRuleTargets resolves a set: ref to the relay's grant targets and
// whether the set says host. ok=false when the set is gone (the binding is
// inert; netBinding says why) or there is no user store.
func (b *Broker) netSetRuleTargets(ref string) (targets []string, host, ok bool) {
	name, isSet := netSetName(ref)
	if !isSet || b.Users == nil {
		return nil, false, false
	}
	ns, found := b.Users.NetSet(name)
	if !found {
		return nil, false, false
	}
	targets, host = netRuleTargets(ns.Rules)
	return targets, host, true
}

// netSetBoundTiles lists the components whose net slot is STORED as
// set:<name>, anywhere — personal and workspace tiles included: the delete
// guard (409 while bound) and the restart fan-out after an edit.
func (b *Broker) netSetBoundTiles(name string) []string {
	var out []string
	for comp, slots := range b.Reg.Workspace().Bindings {
		c, ok := b.Reg.Component(comp)
		if !ok {
			continue
		}
		for slot, req := range c.Manifest.Interfaces {
			if req.Kind == "net" && slots[slot].First() == NetRefSet+name {
				out = append(out, comp)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// orgNetDefault reports whether an org-owned component's unbound net slot
// resolves to "org" (sets attached, net not denied) — D54's default binding.
func (b *Broker) orgNetDefault(comp string) bool {
	if b.Users == nil {
		return false
	}
	ceil := b.Users.Ceiling(comp)
	return ceil.OwnerOrg() != "" && ceil.HasNetSets() && !ceil.Denies(users.PolicyDenyNet)
}

// netBuiltinOptions is the builtin half of a net slot's picker: org (on
// org-owned tiles, labelled with the live reach), internet, host, none, and
// the named sets (D65) — with "not covered" suffixes where the org's network
// sets refuse a choice, and "workspace admins only" on the sets for anyone
// else (Blocked either way: pickers grey them out).
func (b *Broker) netBuiltinOptions(comp string, wsAdmin bool) []bindOption {
	var ceil users.Ceiling
	if b.Users != nil {
		ceil = b.Users.Ceiling(comp)
	}
	var out []bindOption
	if org := ceil.OwnerOrg(); org != "" {
		label := "org — org:" + org + "'s network sets"
		switch {
		case !ceil.HasNetSets():
			label += " (none attached: no egress until a workspace admin attaches one)"
		default:
			label += " (" + strings.Join(ceil.NetSets(), ", ") + "): " + strings.Join(ceil.NetRules(), ", ")
			if t, host := netRuleTargets(ceil.NetRules()); len(t) == 0 && !host {
				label += " (provider-only set: no relay egress)"
			}
		}
		out = append(out, bindOption{ID: NetRefOrg, Label: label})
	}
	uncovered := func(target string) bool { return ceil.HasNetSets() && !ceil.NetCovers(target) }
	builtin := func(id, label, target string) bindOption {
		o := bindOption{ID: id, Label: label}
		if uncovered(target) {
			o.Label += " — not covered by the org's network sets"
			o.Blocked = true
		}
		return o
	}
	out = append(out,
		builtin("internet", "internet — public internet (gVisor relay, no LAN; internet:<host|cidr>[:port][,…] filters to named destinations, D35)", "net:internet"),
		builtin("host", "host — share the host's network (powerful)", "net:host"),
		bindOption{ID: NetRefNone, Label: "none — no egress, explicitly (deny-all)"},
	)
	return append(out, b.netSetOptions(ceil, wsAdmin)...)
}

// netSetOptions: one option per workspace network set, server-labelled with
// its rules. Blocked when the caller is not a workspace admin (binding a set
// is their act), when the set is provider-only (not bindable), or when an
// org tile's union doesn't cover the set's material rules.
func (b *Broker) netSetOptions(ceil users.Ceiling, wsAdmin bool) []bindOption {
	if b.Users == nil {
		return nil
	}
	sets := b.Users.NetSets()
	names := make([]string, 0, len(sets))
	for n := range sets {
		names = append(names, n)
	}
	sort.Strings(names)
	var out []bindOption
	for _, n := range names {
		o := bindOption{ID: NetRefSet + n, Label: "set:" + n + " — network set (" + strings.Join(sets[n].Rules, ", ") + ")"}
		material, host := netSetMaterial(sets[n].Rules)
		switch {
		case len(material) == 0 && !host:
			o.Label += " — provider-only, not bindable"
			o.Blocked = true
		case !wsAdmin:
			o.Label += " — workspace admins only"
			o.Blocked = true
		case ceil.HasNetSets() && !netSetCovered(ceil, material):
			o.Label += " — not covered by the org's network sets"
			o.Blocked = true
		}
		out = append(out, o)
	}
	return out
}

// netSetCovered: every material rule of a set is inside the org's union.
func netSetCovered(ceil users.Ceiling, material []string) bool {
	for _, r := range material {
		if !ceil.NetCovers("net:" + r) {
			return false
		}
	}
	return true
}
