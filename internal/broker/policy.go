package broker

import (
	"fmt"
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
// points: workspace admins; humans whose create rights cover the request; or
// an element holding the workspace-management capability (xbin:writer).
//
// Which create right applies depends on the requested OWNER (D24/D25): a
// personal/workspace tile is gated by the user's own path patterns, but a
// tile created AS AN ORG (`ownerRef == "org:<id>"`) is gated by the org's
// Create knob — already verified by resolveCreateOwner, so the personal
// pattern check is skipped for it (the path no longer encodes the org, so
// path patterns are the wrong authority).
//
// When a HUMAN is attributed on an element call (frame or terminal
// principal), the human's own rights must cover the request too — the
// confused-deputy clamp: a manager-style tile can never be driven to create
// beyond what its driver may create themselves. Unattributed automation
// (instance tokens, the bootstrap owner) is unaffected.
func (b *Broker) canCreateAt(p auth.Principal, path, ownerRef string) (bool, string) {
	if b.IsAdmin(p) {
		return true, ""
	}
	orgOwned := strings.HasPrefix(ownerRef, "org:")
	if p.Component == "" {
		if orgOwned || p.CanCreateTile(path) { // org: resolveCreateOwner checked CanCreateAs
			return true, ""
		}
		return false, "creating components needs a create permission on this path (ask an admin for a create pattern, or create it owned by an org where you hold Create)"
	}
	// Element callers (frame/terminal/instance) need the workspace-management
	// capability regardless of owner.
	if role, ok := b.grantedRole(p.Component, "xbin"); !ok || !roleSatisfies(role, "writer", nil) {
		return false, "creating components needs a create permission on this path (ask an admin), or the workspace-management grant — declare {\"target\":\"xbin\",\"role\":\"writer\"} in \"uses\" and have the owner approve it"
	}
	if p.UserID != "" && !orgOwned && !b.attributedAccess(p.UserID).CanCreateTile(path) {
		return false, "your account has no create permission on " + path + " — the tile's workspace-management grant doesn't extend your own rights (ask an admin for a create pattern, or create it owned by an org where you hold Create)"
	}
	return true, ""
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
