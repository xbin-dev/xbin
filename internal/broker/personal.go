package broker

import (
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// Self-approval on personal tiles (plans/DECISIONS.md D88). The D26 caller
// edge, for a tile's user-owner: a HUMAN who owns the requesting tile
// personally may approve its grants and bindings when the target is their
// own property (another tile or scope they own) or covered by their
// personal allowance (users.Store.Personal: their permission sets' Allow ∪
// their network sets' rules, workspace defaults included). Narrowing —
// revoke, unbind, `none`, the `personal` network — needs nothing. The xbin
// family is floored in users.allowCovers, a named set (`set:`) stays a
// workspace-admin act (apiBindingSet), and the policy ceiling still judges
// the result on the normal approval path.

// approverSelf returns the caller's id when they are the human user-owner
// of tile ("" otherwise). Element principals never approve (D21).
func (b *Broker) approverSelf(p auth.Principal, tile string) string {
	if b.Users == nil || p.Component != "" || p.User == nil || p.User.Disabled {
		return ""
	}
	if b.Users.Owner(tile) != users.OwnerKindUser+":"+p.User.ID {
		return ""
	}
	return p.User.ID
}

// ownedBy reports whether path (a tile, a res: scope's dir, a provider ref)
// is owned by user uid.
func (b *Broker) ownedBy(uid, path string) bool {
	return b.Users != nil && path != "" && b.Users.Owner(providerPath(path)) == users.OwnerKindUser+":"+uid
}

// intraUserTarget is intraOrgTarget for a person: a grant target that is the
// same user's property — a tile they own, or a res: in a scope they own.
func (b *Broker) intraUserTarget(uid, target string) bool {
	if strings.HasPrefix(target, "res:") {
		rt, _, ok := b.parseRes(target)
		return ok && rt.Scope != "" && b.ownedBy(uid, rt.Scope)
	}
	return !strings.Contains(target, ":") && b.ownedBy(uid, target)
}

// selfMayGrant: the owner revokes anything on their tile, and approves its
// own-property or allowance-covered targets.
func (b *Broker) selfMayGrant(p auth.Principal, g registry.Grant, revoke bool) bool {
	uid := b.approverSelf(p, g.From)
	if uid == "" {
		return false
	}
	return revoke || b.intraUserTarget(uid, g.Target) || b.Users.PersonalAllowanceCovers(uid, g.Target, g.Role)
}

// selfMayBind: the owner unbinds anything on their tile; a binding passes
// when every normalized target is a narrowing builtin (none, personal), a
// provider/terminator tile they own, or covered by their allowance.
func (b *Broker) selfMayBind(p auth.Principal, comp, slot string, binding registry.Binding, unbind bool) bool {
	uid := b.approverSelf(p, comp)
	if uid == "" {
		return false
	}
	if unbind {
		return true
	}
	pts, ok := b.bindingTargetsPaired(comp, slot, binding)
	if !ok || len(pts) == 0 {
		return false
	}
	for _, pt := range pts {
		switch {
		case pt.target == "net:"+NetRefNone || pt.target == "net:"+NetRefPersonal:
		case pt.ref != "" && b.ownedBy(uid, pt.ref) && !strings.HasPrefix(pt.target, "ingress:listen:"):
			// their own provider or terminator tile (host ports stay
			// workspace infrastructure: allowance or admin)
		case pt.target != "" && b.Users.PersonalAllowanceCovers(uid, pt.target, ""):
		default:
			return false
		}
	}
	return true
}

// selfApprovable is the set of tiles the caller may wire as their owner — the
// bindings list marks them approvable (D88).
func (b *Broker) selfApprovable(p auth.Principal) map[string]bool {
	out := map[string]bool{}
	if b.Users == nil || p.Component != "" || p.User == nil || p.User.Disabled {
		return out
	}
	for _, t := range b.Users.OwnedBy(users.OwnerKindUser + ":" + p.User.ID) {
		out[t] = true
	}
	return out
}

// markSelfBlocked greys out the net options an owner could not bind
// themselves (outside their allowance), so the picker never offers a click
// POST /bindings would refuse. Options already blocked stay blocked.
func (b *Broker) markSelfBlocked(uid string, opts []bindOption) []bindOption {
	for i, o := range opts {
		if o.Blocked {
			continue
		}
		var target string
		switch {
		case o.ID == NetRefNone || o.ID == NetRefPersonal:
			continue
		case o.ID == "internet" || o.ID == "host":
			target = "net:" + o.ID
		case strings.HasPrefix(o.ID, NetRefSet) || o.ID == NetRefOrg:
			continue // decided elsewhere (admin-only / refused on personal tiles)
		case b.ownedBy(uid, o.ID):
			continue
		default:
			target = "net:provider:" + providerPath(o.ID)
		}
		if !b.Users.PersonalAllowanceCovers(uid, target, "") {
			opts[i].Blocked = true
			opts[i].Label += " — outside your network allowance (ask a workspace admin)"
		}
	}
	return opts
}
