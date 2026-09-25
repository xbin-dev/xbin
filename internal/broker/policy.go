package broker

import (
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// Policy ceiling (plans/orgs.md, D20). The rows and their evaluation live in
// internal/users (Store.Ceiling); this file only dispatches a grant target to
// the right capability class and phrases the refusal. It is asked at every
// evaluation chokepoint — grantedRole (covering explicit grants, interface
// bindings, and same-scope auto-grants alike) and netBinding — so a ceiling
// holds even against a hand-edited xbin.json; the approval APIs additionally
// reject up front with the blocking row named.

// ceilingBlockMsg reports why the policy ceiling blocks `from` reaching
// `target` ("" = allowed). from=="" (not a tile) is never blocked.
//
// Target classification matters: reserved CAPABILITY targets must never fall
// into the mayCall path-matcher (a path allow-list can't cover the string
// "code", so it would silently strip the capability — the 2026-07-12
// code:reader regression). xbin/xbin:* and bare `code` (whole-workspace
// source read — owner-level, see code.go) are the xbin-caps class;
// `code:<comp>` reads ONE component's source, so it is governed exactly like
// calling that component.
func (b *Broker) ceilingBlockMsg(from, target string) string {
	if b.Users == nil || from == "" {
		return ""
	}
	return b.ceilingBlockWith(b.Users.Ceiling(from), from, target)
}

// ceilingBlockWith is ceilingBlockMsg against an EXPLICIT ceiling — the
// transfer preview evaluates a tile's rows/targets under the ceiling it
// WOULD have after a move (users.CeilingFor, D39).
func (b *Broker) ceilingBlockWith(c users.Ceiling, from, target string) string {
	deny := func(kind string) string {
		if row, ok := c.DenyRow(kind); ok {
			return fmt.Sprintf("a policy row for tiles matching %q denies %s (workspace/org policy — see /docs/auth.md)", row.Tiles, kind)
		}
		return ""
	}
	// callBlock evaluates a component/res call target. Same-scope targets are
	// exempt from mayCall: a scope is one trust unit (ND5), so the allow-list
	// governs a tile's EXTERNAL reach — it must never sever an app from its
	// own resources (res:<scope>/db) or intra-app calls.
	callBlock := func(display, t string) string {
		if caller, ok := b.Reg.Component(from); ok && b.sameScope(caller, t) {
			return ""
		}
		if row, ok := c.MayCallBlocker(t); ok {
			return fmt.Sprintf("a policy row for tiles matching %q allow-lists call targets and %q is not covered (workspace/org policy — see /docs/auth.md)", row.Tiles, display)
		}
		return ""
	}
	switch {
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		return deny(users.PolicyDenyXbinCaps)
	case target == "code": // blanket workspace source read — owner-level capability
		return deny(users.PolicyDenyXbinCaps)
	case strings.HasPrefix(target, "code:"): // one component's source — like calling it
		return callBlock(target, strings.TrimPrefix(target, "code:"))
	case strings.HasPrefix(target, "gpu:"):
		return deny(users.PolicyDenyGPU)
	case target == NetAdminCap: // net-provider capability — a `net` deny covers it
		return deny(users.PolicyDenyNet)
	case target == ContainersCap: // container-host capability — a system cap; xbin-caps deny covers it
		return deny(users.PolicyDenyXbinCaps)
	case target == OpenLinksCap: // frontend popup capability (ND11) — xbin-caps deny covers it
		return deny(users.PolicyDenyXbinCaps)
	case strings.HasPrefix(target, "net:"): // legacy net grants (pre-bindings)
		return deny(users.PolicyDenyNet)
	default: // component paths and res:… targets
		return callBlock(target, target)
	}
}

// ceilingAllows is the boolean form used on the evaluation hot paths.
func (b *Broker) ceilingAllows(from, target string) bool {
	return b.ceilingBlockMsg(from, target) == ""
}

// canCreateAt is the shared tile-creation authority for the same five entry
// points: workspace admins; humans creating a tile they will own (personally,
// or as an org where they hold Create — resolveCreateOwner has already
// decided and authorised the owner) at a path newTilePathOK accepts; or an
// element holding the workspace-management capability (xbin:writer).
//
// Creation follows OWNERSHIP, not path patterns (D82): a non-admin may take
// any free path that isn't reserved, isn't inside someone else's scope, and
// carries no leftover state from a removed tile. The deprecated per-user
// canCreate patterns are not consulted.
//
// When a HUMAN is attributed on an element call (frame or terminal
// principal), the same path rule applies to them — the confused-deputy
// clamp: a manager-style tile can never be driven to create where its
// driver couldn't create themselves. Unattributed automation (instance
// tokens, the bootstrap owner) keeps plain capability semantics.
func (b *Broker) canCreateAt(p auth.Principal, path, ownerRef string) (bool, string) {
	if b.IsAdmin(p) {
		return true, ""
	}
	if p.Component != "" {
		// Element callers (frame/terminal/instance) need the
		// workspace-management capability regardless of owner.
		if role, ok := b.grantedRole(p.Component, "xbin"); !ok || !roleSatisfies(role, "writer", nil) {
			return false, "creating components from a tile needs the workspace-management grant — declare {\"target\":\"xbin\",\"role\":\"writer\"} in \"uses\" and have the owner approve it"
		}
		if p.UserID == "" {
			return true, "" // unattributed automation holding the capability
		}
		if b.Users != nil {
			if u, ok := b.Users.Get(p.UserID); ok && u.IsAdmin() {
				return true, "" // an admin driving the manager tile
			}
		}
	}
	if ownerRef == "" {
		// resolveCreateOwner gives every non-admin human a user:/org: owner;
		// an empty one here means the account vanished mid-request.
		return false, "creating workspace-owned tiles is a workspace-admin action"
	}
	return b.newTilePathOK(strings.Trim(path, "/"), ownerRef)
}

// reservedCreateTop are first path segments a non-admin may not create
// under (D82): tiles/ is where the built-in tiles live — the shell, the
// default screen and boot backfill point at fixed paths there — and root /
// shell are the implicitly-trusted workspace chrome (isChrome).
var reservedCreateTop = map[string]string{
	"tiles": "tiles/ is reserved for built-in tiles",
	"root":  "root is the workspace chrome",
	"shell": "shell is the workspace chrome",
}

// newTilePathOK is the ownership-based path rule for a non-admin creating a
// tile owned by ownerRef at path (D82). In order:
//
//  1. reserved: a first segment in reservedCreateTop, or any segment with
//     ':' (the grant-target / identity separator — code, cap:x, user:bob);
//  2. scope: the nearest scope root at or above path must be absent ("top
//     level") or owned by ownerRef — its owner entry, or, with none, every
//     tile already in it. A new tile in a scope joins it, and same-scope
//     grants are auto-approved, so this is a trust boundary;
//  3. leftovers: state keyed by the path that would otherwise pass silently
//     to the new tile (pathLeftovers).
//
// Nesting and path syntax stay with guardNewComponentTree and
// util.ComponentPathOK.
func (b *Broker) newTilePathOK(path, ownerRef string) (bool, string) {
	const hint = " — pick another path, or ask a workspace admin"
	parts := strings.Split(path, "/")
	if why, ok := reservedCreateTop[parts[0]]; ok {
		return false, "can't create " + path + ": " + why + hint
	}
	for _, part := range parts {
		if strings.Contains(part, ":") {
			return false, "can't create " + path + ": ':' isn't allowed in tile names (it separates grant targets and identities)" + hint
		}
	}
	if s := b.scopeAt(path); s != "" && !b.scopeOwnedBy(s, ownerRef) {
		return false, "can't create " + path + ": it's inside scope " + s + ", which " + ownerRef + " doesn't own" + hint
	}
	if left := b.pathLeftovers(path, ownerRef); len(left) > 0 {
		return false, "can't create " + path + ": the path still carries state from a removed tile (" + strings.Join(left, "; ") + ") — pick another path, or ask a workspace admin to clear it first"
	}
	return true, ""
}

// scopeAt returns the nearest scope root at or above path ("" = none).
func (b *Broker) scopeAt(p string) string {
	scopes := b.Reg.Scopes()
	for p != "." && p != "" {
		if _, ok := scopes[p]; ok {
			return p
		}
		p = pathpkg.Dir(p)
	}
	return ""
}

// scopeOwnedBy: the scope root's own owner entry decides; without one, the
// scope belongs to ownerRef only if it holds at least one tile and ownerRef
// owns every tile in it.
func (b *Broker) scopeOwnedBy(scope, ownerRef string) bool {
	if b.Users == nil {
		return false
	}
	if o := b.Users.Owner(scope); o != "" {
		return o == ownerRef
	}
	n := 0
	for _, c := range b.Reg.Components() {
		if c.Scope != scope {
			continue
		}
		if b.Users.Owner(c.Path) != ownerRef {
			return false
		}
		n++
	}
	return n > 0
}

// pathLeftovers names the state still keyed by path (or a path under it)
// that a new tile there would inherit: workspace grant rows naming it on
// either side, interface bindings / instances / ingress hosts, its vault,
// and the identity store's entries (Store.PathLeftovers). Nothing prunes
// these when a tile's directory disappears. A path whose owner entry is
// already ownerRef is the owner re-creating their own tile — nothing to
// take over.
func (b *Broker) pathLeftovers(path, ownerRef string) []string {
	if b.Users != nil && b.Users.Owner(path) == ownerRef {
		return nil
	}
	under := func(p string) bool { return p == path || strings.HasPrefix(p, path+"/") }
	var out []string
	ws := b.Reg.Workspace()
	for _, g := range ws.Grants {
		if under(g.From) || under(strings.TrimPrefix(g.Target, "code:")) {
			out = append(out, "grant "+g.From+" → "+g.Target+":"+g.Role)
		}
	}
	for comp, slots := range ws.Bindings {
		for slot, bind := range slots {
			for _, r := range bind {
				prov, _, _ := strings.Cut(r.Ref, "#")
				if under(comp) || under(prov) {
					out = append(out, "binding "+comp+" "+slot+" → "+r.Ref)
				}
			}
		}
	}
	for comp := range ws.IfaceInstances {
		if under(comp) {
			out = append(out, "interface instances of "+comp)
		}
	}
	for comp := range ws.IngressHosts {
		if under(comp) {
			out = append(out, "ingress hosts of "+comp)
		}
	}
	if _, err := os.Stat(b.vaultPath(path)); err == nil {
		out = append(out, "vault secrets")
	}
	if b.Users != nil {
		out = append(out, b.Users.PathLeftovers(path, ownerRef)...)
	}
	sort.Strings(out)
	return out
}

// attributedCanRead is the matching source-side clamp for copy-shaped
// creation (clone, workspace-template instantiate): a human must be able to
// READ what they are copying, directly (session principal) or attributed on
// an element call — otherwise a manager-style tile would be a source-code
// exfiltration route into the caller's own namespace.
func (b *Broker) attributedCanRead(p auth.Principal, path string) bool {
	if b.IsAdmin(p) {
		return true
	}
	if p.Component == "" {
		return p.CanReadTile(path)
	}
	if p.UserID == "" { // unattributed automation with the capability grant
		return true
	}
	return b.attributedAccess(p.UserID).CanReadTile(path)
}

// attributedAccess resolves the org/team-aware access of the human riding an
// element principal (nil-safe: unknown user / no store ⇒ a nil Access, which
// answers no to everything).
func (b *Broker) attributedAccess(userID string) *users.Access {
	if b.Users == nil {
		return nil
	}
	acc, _ := b.Users.Access(userID)
	return acc
}

// guardNewComponentTree rejects creation paths that nest with existing
// components either way (the same rule clone always had): not inside one,
// and not a subtree that already contains one — e.g. a tile AT an org
// container (apps/o/sales) above existing org tiles.
func (b *Broker) guardNewComponentTree(path string) error {
	if owner, _, ok := b.Reg.Resolve(path); ok && owner != nil {
		if owner.Path == path {
			return fmt.Errorf("%s already exists", path)
		}
		return fmt.Errorf("%s is inside existing component %s", path, owner.Path)
	}
	for _, c := range b.Reg.Components() {
		if strings.HasPrefix(c.Path, path+"/") {
			return fmt.Errorf("%s would contain existing component %s", path, c.Path)
		}
	}
	return nil
}
