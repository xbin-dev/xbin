package broker

import (
	"fmt"
	"strings"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/users"
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
func (b *Broker) ceilingBlockMsg(from, target string) string {
	if b.Users == nil || from == "" {
		return ""
	}
	c := b.Users.Ceiling(from)
	deny := func(kind string) string {
		if row, ok := c.DenyRow(kind); ok {
			return fmt.Sprintf("a policy row for tiles matching %q denies %s (workspace/org policy — see /docs/auth.md)", row.Tiles, kind)
		}
		return ""
	}
	switch {
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		return deny(users.PolicyDenyXbinCaps)
	case strings.HasPrefix(target, "gpu:"):
		return deny(users.PolicyDenyGPU)
	case strings.HasPrefix(target, "net:"): // legacy net grants (pre-bindings)
		return deny(users.PolicyDenyNet)
	default: // component paths and res:… targets
		// Same-scope targets are exempt from mayCall: a scope is one trust
		// unit (ND5), so the allow-list governs a tile's EXTERNAL reach — it
		// must never sever an app from its own resources (res:<scope>/db) or
		// intra-app calls. Deny kinds above still apply regardless.
		if caller, ok := b.Reg.Component(from); ok && b.sameScope(caller, target) {
			return ""
		}
		if row, ok := c.MayCallBlocker(target); ok {
			return fmt.Sprintf("a policy row for tiles matching %q allow-lists call targets and %q is not covered (workspace/org policy — see /docs/auth.md)", row.Tiles, target)
		}
		return ""
	}
}

// ceilingAllows is the boolean form used on the evaluation hot paths.
func (b *Broker) ceilingAllows(from, target string) bool {
	return b.ceilingBlockMsg(from, target) == ""
}

// validateNewPath is the shared reserved-segment gate on every tile-creating
// entry point (create / clone / git import / builtin import / template
// instantiate): the `o` org marker must name an existing org, `u` is reserved
// (plans/orgs.md). Existing on-disk paths are never rejected — bx doctor
// warns instead.
func (b *Broker) validateNewPath(path string) error {
	if b.Users == nil {
		return nil
	}
	return b.Users.ValidateNewTilePath(path)
}

// canCreateAt is the shared tile-creation authority for the same five entry
// points: workspace admins; humans whose (org/team-unioned) create patterns
// cover the path; or an element holding the workspace-management capability
// (xbin:writer). When a HUMAN is attributed on an element call (frame or
// terminal principal), the human's own rights must cover the path too — the
// confused-deputy clamp: a manager-style tile can never be driven to create
// beyond what its driver may create themselves. Unattributed automation
// (instance tokens, the bootstrap owner) is unaffected.
func (b *Broker) canCreateAt(p auth.Principal, path string) (bool, string) {
	if b.IsAdmin(p) {
		return true, ""
	}
	if p.CanCreateTile(path) { // session humans: their own union rights
		return true, ""
	}
	if p.Component == "" {
		return false, "creating components needs a create permission on this path (ask an admin for a create pattern or a team)"
	}
	if role, ok := b.grantedRole(p.Component, "xbin"); !ok || !roleSatisfies(role, "writer", nil) {
		return false, "creating components needs a create permission on this path (ask an admin), or the workspace-management grant — declare {\"target\":\"xbin\",\"role\":\"writer\"} in \"uses\" and have the owner approve it"
	}
	if p.UserID != "" && !b.attributedAccess(p.UserID).CanCreateTile(path) {
		return false, "your account has no create permission on " + path + " — the tile's workspace-management grant doesn't extend your own rights (ask an admin for a create pattern or a team)"
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
