package broker

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// Delegated approval (plans/ownership.md, D26): a HUMAN org admin may approve
// grants and bindings for tiles OWNED by their org when the target is either
// intra-org (the org wiring its own property) or covered by the org's
// resolved allowance (permission sets ∪ extras) — always under the policy
// ceilings, which the normal approval path still evaluates. The xbin
// capability family is floored in users.AllowanceCovers and again here.
//
// Everything below answers one question — "may p approve (tile, target)?" —
// plus the target-normalization that turns a binding into allowance-grammar
// strings (net:…, iface:…, ingress:…).

// approverOrg returns the org id when p is a human org admin of the org that
// owns tile ("" otherwise). Element principals never approve (D21 rule kept:
// tiles request, humans approve).
func (b *Broker) approverOrg(p auth.Principal, tile string) string {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return ""
	}
	org, ok := b.Users.OwnerOrg(tile)
	if !ok || !p.Access.IsAdminOrg(org) {
		return ""
	}
	return org
}

// intraOrgTarget reports whether a GRANT target is the same org's property:
// a component path owned by the org, or a res:<scope>/<name> whose scope
// path is an org-owned component dir.
func (b *Broker) intraOrgTarget(org, target string) bool {
	if strings.HasPrefix(target, "res:") {
		if rt, _, ok := b.parseRes(target); ok && rt.Scope != "" {
			o, isOrg := b.Users.OwnerOrg(rt.Scope)
			return isOrg && o == org
		}
		return false
	}
	if strings.Contains(target, ":") {
		return false // capability classes are never intra-org
	}
	o, isOrg := b.Users.OwnerOrg(target)
	return isOrg && o == org
}

// orgAdminMayGrant is the D26 gate for POST/DELETE /grants when the caller
// is not a workspace admin. DELETE (revoke) is allowed for any org-owned
// tile's grant — narrowing is safe; POST needs intra-org or allowance cover.
func (b *Broker) orgAdminMayGrant(p auth.Principal, g registry.Grant, revoke bool) bool {
	org := b.approverOrg(p, g.From)
	if org == "" {
		return false
	}
	if revoke {
		return true
	}
	if b.intraOrgTarget(org, g.Target) {
		return true
	}
	return b.Users.AllowanceCovers(org, g.Target)
}

// bindingTargets normalizes one binding request into allowance-grammar
// targets that must ALL be covered (or intra-org). Returns nil, false when
// the slot can't be resolved (unknown component/slot) — the caller falls
// back to the ws-admin-only refusal.
func (b *Broker) bindingTargets(comp, slot string, binding registry.Binding) (targets []string, intra []string, ok bool) {
	c, found := b.Reg.Component(comp)
	if !found {
		return nil, nil, false
	}
	if def, isExpose := c.Manifest.Exposes[slot]; isExpose {
		_ = def
		for _, ref := range binding {
			if ref.Host != "" {
				targets = append(targets, "ingress:host:"+ref.Host)
			}
			if ref.Zone != "" {
				targets = append(targets, "ingress:zone:"+ref.Zone)
			}
			if ref.Listen != "" {
				port := ref.Listen
				if i := strings.LastIndexByte(port, ':'); i >= 0 {
					port = port[i+1:]
				}
				targets = append(targets, "ingress:listen:"+port)
			}
			// The ingress SOURCE (builtin listener or a terminator tile) rides
			// along: a same-org terminator is intra-org; the builtin listener
			// ("runtime") is covered by the host/zone/listen entries above.
			if ref.Ref != "" && ref.Ref != "runtime" {
				intra = append(intra, providerPath(ref.Ref))
			}
		}
		return targets, intra, true
	}
	iface, isIface := c.Manifest.Interfaces[slot]
	if !isIface {
		return nil, nil, false
	}
	for _, ref := range binding {
		v := ref.Ref
		switch {
		case iface.Kind == "net" && (v == "internet" || v == "host"):
			targets = append(targets, "net:"+v)
		case iface.Kind == "net" && strings.HasPrefix(v, "lan:"):
			targets = append(targets, "net:"+v)
		case iface.Kind == "net":
			// A provider tile: same-org providers are intra-org wiring;
			// otherwise the allowance must name net:provider:<tile>.
			intra = append(intra, providerPath(v))
			targets = append(targets, "net:provider:"+providerPath(v))
		default:
			// http/stream interface to a provider tile.
			intra = append(intra, providerPath(v))
			svc := iface.Service
			if svc == "" {
				svc = iface.Kind
			}
			targets = append(targets, "iface:"+svc)
		}
	}
	return targets, intra, true
}

// providerPath strips a "#instance" suffix off a provider ref.
func providerPath(ref string) string {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i]
	}
	return ref
}

// orgAdminMayBind is the D26 gate for POST/DELETE /bindings when the caller
// is not a workspace admin: the component must be org-owned by an org p
// administers; unbinding is always fine; binding requires every normalized
// target to be intra-org (a same-org provider) or allowance-covered. For
// net/iface targets paired with an intra-org provider, the provider being
// same-org satisfies that ref without an allowance entry.
func (b *Broker) orgAdminMayBind(p auth.Principal, comp, slot string, binding registry.Binding, unbind bool) bool {
	org := b.approverOrg(p, comp)
	if org == "" {
		return false
	}
	if unbind {
		return true
	}
	targets, intraRefs, ok := b.bindingTargets(comp, slot, binding)
	if !ok {
		return false
	}
	intraOK := map[string]bool{}
	for _, ref := range intraRefs {
		if o, isOrg := b.Users.OwnerOrg(ref); isOrg && o == org {
			intraOK[ref] = true
		}
	}
	for i, t := range targets {
		if b.Users.AllowanceCovers(org, t) {
			continue
		}
		// A provider-shaped target passes when its provider ref is same-org.
		if strings.HasPrefix(t, "net:provider:") && intraOK[strings.TrimPrefix(t, "net:provider:")] {
			continue
		}
		if strings.HasPrefix(t, "iface:") && i < len(binding) && intraOK[providerPath(binding[i].Ref)] {
			continue
		}
		return false
	}
	return len(targets) > 0 || len(intraRefs) > 0 || unbind
}

// orgFilterGrants returns the grants/pending subset an org admin may see:
// rows whose From is owned by one of their admin orgs, with approvable
// pending marked (the organisations tile renders these).
func (b *Broker) orgFilterGrants(p auth.Principal) (grants []registry.Grant, pending []PendingGrant, any bool) {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return nil, nil, false
	}
	admin := p.Access.AdminOrgs()
	if len(admin) == 0 {
		return nil, nil, false
	}
	isMine := func(from string) bool {
		org, ok := b.Users.OwnerOrg(from)
		if !ok {
			return false
		}
		for _, a := range admin {
			if a == org {
				return true
			}
		}
		return false
	}
	for _, g := range b.Reg.Workspace().Grants {
		if isMine(g.From) {
			grants = append(grants, g)
		}
	}
	for _, pg := range b.Pending() {
		if isMine(pg.From) {
			pg.Approvable = b.orgAdminMayGrant(p, pg.Grant, false)
			pending = append(pending, pg)
		}
	}
	return grants, pending, true
}
