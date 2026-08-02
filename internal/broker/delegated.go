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

// targetOwnerOrg resolves the org that OWNS a grant target's property: the
// target component itself, a res:<scope>/… scope's component, or the
// component of a code:<comp> read. "" when the target is not org-owned
// (capability classes never are).
func (b *Broker) targetOwnerOrg(target string) string {
	if b.Users == nil {
		return ""
	}
	if strings.HasPrefix(target, "res:") {
		if rt, _, ok := b.parseRes(target); ok && rt.Scope != "" {
			if o, isOrg := b.Users.OwnerOrg(rt.Scope); isOrg {
				return o
			}
		}
		return ""
	}
	if c, ok := strings.CutPrefix(target, "code:"); ok {
		if o, isOrg := b.Users.OwnerOrg(c); isOrg {
			return o
		}
		return ""
	}
	if strings.Contains(target, ":") {
		return "" // capability classes have no owner
	}
	if o, isOrg := b.Users.OwnerOrg(target); isOrg {
		return o
	}
	return ""
}

// providerOrg returns the org id when p is a human admin of the org that owns
// the grant TARGET (D33: sharing your own property is an ownership right —
// the provider org consents to its consumers, no allowance needed).
func (b *Broker) providerOrg(p auth.Principal, target string) string {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return ""
	}
	org := b.targetOwnerOrg(target)
	if org == "" || !p.Access.IsAdminOrg(org) {
		return ""
	}
	return org
}

// orgAdminMayGrant is the D26/D33 gate for POST/DELETE /grants when the
// caller is not a workspace admin. Two independent rights:
//
//   - CALLER side (D26): an admin of the org owning the requesting tile —
//     revoke always (narrowing is safe); approve when the target is
//     intra-org or covered by the org's allowance at the requested role.
//   - PROVIDER side (D33): an admin of the org owning the TARGET — they may
//     approve (consent to sharing their property) and revoke (withdraw it).
func (b *Broker) orgAdminMayGrant(p auth.Principal, g registry.Grant, revoke bool) bool {
	if org := b.approverOrg(p, g.From); org != "" {
		if revoke || b.intraOrgTarget(org, g.Target) {
			return true
		}
		if b.Users.AllowanceCovers(org, g.Target, g.Role) {
			return true
		}
	}
	return b.providerOrg(p, g.Target) != ""
}

// pairedTarget is one normalized approval target with the binding REF it
// came from ("" for builtin sources — net classes, the runtime listener).
// Explicit pairing lets consent rules reason per-ref (an ingress host is
// consented by the org that owns its terminator, D41) without index-guessing
// across the flattened target list.
type pairedTarget struct {
	target string
	ref    string
}

// bindingTargetsPaired normalizes one binding request into (target, ref)
// pairs. ok=false when the slot can't be resolved.
func (b *Broker) bindingTargetsPaired(comp, slot string, binding registry.Binding) ([]pairedTarget, bool) {
	c, found := b.Reg.Component(comp)
	if !found {
		return nil, false
	}
	var out []pairedTarget
	if _, isExpose := c.Manifest.Exposes[slot]; isExpose {
		for _, ref := range binding {
			src := ""
			if ref.Ref != "" && ref.Ref != "runtime" {
				src = providerPath(ref.Ref)
			}
			if ref.Host != "" {
				out = append(out, pairedTarget{"ingress:host:" + ref.Host, src})
			}
			if ref.Zone != "" {
				out = append(out, pairedTarget{"ingress:zone:" + ref.Zone, src})
			}
			if ref.Listen != "" {
				port := ref.Listen
				if i := strings.LastIndexByte(port, ':'); i >= 0 {
					port = port[i+1:]
				}
				out = append(out, pairedTarget{"ingress:listen:" + port, src})
			}
			if src != "" && ref.Host == "" && ref.Zone == "" && ref.Listen == "" {
				out = append(out, pairedTarget{"", src}) // bare terminator ref
			}
		}
		return out, true
	}
	iface, isIface := c.Manifest.Interfaces[slot]
	if !isIface {
		return nil, false
	}
	for _, ref := range binding {
		v := ref.Ref
		switch {
		case iface.Kind == "net" && (v == "internet" || v == "host"):
			out = append(out, pairedTarget{"net:" + v, ""})
		case iface.Kind == "net" && strings.HasPrefix(v, "internet:"):
			// Filtered internet (D35): every spec must be covered, so an
			// allowance can carve "these hosts / this subnet only".
			for _, spec := range strings.Split(strings.TrimPrefix(v, "internet:"), ",") {
				if spec = strings.TrimSpace(spec); spec != "" {
					out = append(out, pairedTarget{"net:internet:" + spec, ""})
				}
			}
		case iface.Kind == "net" && strings.HasPrefix(v, "lan:"):
			out = append(out, pairedTarget{"net:" + v, ""})
		case iface.Kind == "net":
			// A provider tile: same-org providers are intra-org wiring;
			// otherwise the allowance must name net:provider:<tile>.
			out = append(out, pairedTarget{"net:provider:" + providerPath(v), providerPath(v)})
		default:
			// http/stream interface to a provider tile. The normalized target
			// pins the provider (and instance when the ref names one) so
			// allowances can scope to "this service, from that tile, dev
			// instance only" (D32).
			svc := iface.Service
			if svc == "" {
				svc = iface.Kind
			}
			t := "iface:" + svc + "@" + providerPath(v)
			if i := strings.IndexByte(v, '#'); i >= 0 && i+1 < len(v) {
				t += "#" + v[i+1:]
			}
			out = append(out, pairedTarget{t, providerPath(v)})
		}
	}
	return out, true
}

// bindingTargets is the flattened legacy view (targets + provider refs) —
// transfer previews consume it; the approval gates use the paired form.
func (b *Broker) bindingTargets(comp, slot string, binding registry.Binding) (targets []string, intra []string, ok bool) {
	pts, ok := b.bindingTargetsPaired(comp, slot, binding)
	if !ok {
		return nil, nil, false
	}
	seen := map[string]bool{}
	for _, pt := range pts {
		if pt.target != "" {
			targets = append(targets, pt.target)
		}
		if pt.ref != "" && !seen[pt.ref] {
			seen[pt.ref] = true
			intra = append(intra, pt.ref)
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

// providerRefOrg returns the org owning a provider tile ref ("" when not
// org-owned or no store).
func (b *Broker) providerRefOrg(ref string) string {
	if b.Users == nil {
		return ""
	}
	if o, isOrg := b.Users.OwnerOrg(providerPath(ref)); isOrg {
		return o
	}
	return ""
}

// orgAdminMayBind is the D26/D33 gate for POST/DELETE /bindings when the
// caller is not a workspace admin. Two independent rights:
//
//   - CALLER side (D26): the component is org-owned by an org p administers;
//     unbinding is always fine; binding requires every normalized target to
//     be intra-org (a same-org provider) or allowance-covered.
//   - PROVIDER side (D33): every ref in the binding is a provider tile owned
//     by an org p administers — the providing org consents to (or withdraws
//     from) serving this consumer. Net-class (internet/host/lan) and ingress
//     targets have no provider and can never be consented this way.
func (b *Broker) orgAdminMayBind(p auth.Principal, comp, slot string, binding registry.Binding, unbind bool) bool {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return false
	}
	// adminOwnsRef: the ref is a tile owned by an org p administers — the
	// basis for provider consent (D33) and terminator-domain consent (D41).
	adminOwnsRef := func(ref string) bool {
		if ref == "" {
			return false
		}
		o, isOrg := b.Users.OwnerOrg(ref)
		return isOrg && p.Access.IsAdminOrg(o)
	}
	if org := b.approverOrg(p, comp); org != "" {
		if unbind {
			return true
		}
		pts, ok := b.bindingTargetsPaired(comp, slot, binding)
		if ok {
			pass := len(pts) > 0
			for _, pt := range pts {
				if pt.target == "" { // bare terminator/provider ref
					if o, isOrg := b.Users.OwnerOrg(pt.ref); isOrg && o == org {
						continue
					}
					pass = false
					break
				}
				if b.Users.AllowanceCovers(org, pt.target, "") {
					continue
				}
				// Provider-shaped targets pass when the provider ref is same-org
				// wiring (net providers, http/stream ifaces).
				if pt.ref != "" && (strings.HasPrefix(pt.target, "net:provider:") || strings.HasPrefix(pt.target, "iface:")) {
					if o, isOrg := b.Users.OwnerOrg(pt.ref); isOrg && o == org {
						continue
					}
				}
				// Terminator-domain consent (D41): an ingress HOST/ZONE routed
				// through a terminator tile owned by an org p administers is
				// org property flowing through org property — the terminator's
				// owner controls those domains. Host PORTS (ingress:listen:)
				// and the builtin "runtime" listener stay workspace
				// infrastructure: allowance or ws-admin only.
				if strings.HasPrefix(pt.target, "ingress:host:") || strings.HasPrefix(pt.target, "ingress:zone:") {
					if adminOwnsRef(pt.ref) {
						continue
					}
				}
				pass = false
				break
			}
			if pass {
				return true
			}
		}
	}
	// Provider side (D33/D41): every ref must be a tile owned by orgs p
	// administers — the providing (or terminating) org consents to serving
	// this consumer, wherever the consumer lives. For an UNBIND the request
	// carries no refs — the provider is withdrawing service, so the refs that
	// matter are the ones currently STORED for (comp, slot). Host/zone routed
	// through the consented terminator ride along; ingress:listen: (host
	// ports) never does — that stays workspace infrastructure.
	refs := binding
	if unbind && len(refs) == 0 {
		refs = b.Reg.Workspace().Bindings[comp][slot]
	}
	if len(refs) == 0 {
		return false
	}
	for _, ref := range refs {
		if ref.Ref == "" || ref.Ref == "runtime" || ref.Ref == "internet" || ref.Ref == "host" ||
			strings.HasPrefix(ref.Ref, "lan:") || strings.HasPrefix(ref.Ref, "internet:") {
			return false
		}
		if !adminOwnsRef(providerPath(ref.Ref)) {
			return false
		}
	}
	if pts, ok := b.bindingTargetsPaired(comp, slot, refs); ok {
		for _, pt := range pts {
			if strings.HasPrefix(pt.target, "ingress:listen:") {
				return false
			}
		}
	}
	return true
}

// grantRow is one grant in the org-scoped view, marked with the viewer's
// relation to it: "consumer" (their org owns the requesting tile),
// "provider" (their org owns the target property, D33), "both", or "mine"
// (a tile the viewer can write — the requester's own view).
type grantRow struct {
	registry.Grant
	Direction string `json:"direction,omitempty"`
}

// orgFilterGrants returns the grants/pending subset a non-ws-admin session
// human may see (D26/D33 + requester visibility):
//
//   - CONSUMER rows: From is owned by an org they administer;
//   - PROVIDER rows: the target's property is owned by an org they
//     administer — the consumption of their tiles, visible and revocable;
//   - MINE rows: From is a tile they can write — a requester sees their own
//     tile's grants and pending requests (with who-can-approve hints)
//     instead of a silent dead tile.
//
// ok is false only when the principal has no view at all (elements, or no
// user store).
func (b *Broker) orgFilterGrants(p auth.Principal) (grants []grantRow, pending []PendingGrant, ok bool) {
	if b.Users == nil || p.Component != "" || p.User == nil {
		return nil, nil, false
	}
	admin := map[string]bool{}
	for _, a := range p.Access.AdminOrgs() {
		admin[a] = true
	}
	direction := func(g registry.Grant) string {
		consumer := false
		if org, isOrg := b.Users.OwnerOrg(g.From); isOrg && admin[org] {
			consumer = true
		}
		provider := false
		if torg := b.targetOwnerOrg(g.Target); torg != "" && admin[torg] {
			provider = true
		}
		switch {
		case consumer && provider:
			return "both"
		case consumer:
			return "consumer"
		case provider:
			return "provider"
		case p.CanWriteTile(g.From):
			return "mine"
		}
		return ""
	}
	grants = []grantRow{}
	for _, g := range b.Reg.Workspace().Grants {
		if d := direction(g); d != "" {
			grants = append(grants, grantRow{Grant: g, Direction: d})
		}
	}
	pending = []PendingGrant{}
	for _, pg := range b.Pending() {
		d := direction(pg.Grant)
		if d == "" {
			continue
		}
		pg.Direction = d
		pg.Approvable = pg.Blocked == "" && b.orgAdminMayGrant(p, pg.Grant, false)
		if !pg.Approvable {
			pg.Approvers = b.approverHint(pg.Grant)
		}
		pending = append(pending, pg)
	}
	return grants, pending, true
}

// approverHint names who could approve a pending grant — rendered to the
// requester ("pending — ask …") and next to non-approvable rows.
func (b *Broker) approverHint(g registry.Grant) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	if b.Users != nil {
		if org, isOrg := b.Users.OwnerOrg(g.From); isOrg {
			if b.intraOrgTarget(org, g.Target) || b.Users.AllowanceCovers(org, g.Target, g.Role) {
				add("org:" + org)
			}
		}
		if torg := b.targetOwnerOrg(g.Target); torg != "" {
			add("org:" + torg)
		}
		// A USER-owned requesting tile is otherwise ws-admin-only — but when
		// the owner belongs to an org whose allowance would cover the target,
		// the self-serve escape is transferring the tile there. Hint it so
		// the requester learns the detour at the moment of frustration.
		if owner := b.Users.Owner(g.From); strings.HasPrefix(owner, "user:") {
			uid := strings.TrimPrefix(owner, "user:")
			for _, m := range b.Users.UserOrgs(uid) {
				if b.Users.AllowanceCovers(m.ID, g.Target, g.Role) {
					add("transfer:org:" + m.ID)
				}
			}
		}
	}
	add("workspace-admin")
	return out
}
