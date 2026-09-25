package broker

import (
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/scaffold"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// POST /api/xbin/create — the higher-level "create tile" API (same engine
// as `bx new`). Creating components is an editing-plane action, so callers
// are the owner/admins, a user creating a tile they will own at a path the
// ownership rule accepts (canCreateAt / newTilePathOK, D82), or elements
// holding the workspace-management
// capability: an explicit grant on the reserved target "xbin" at role writer
// (the template ships tiles/manager with that grant; revoke it and the tile
// request shows up in the grants panel like any other).
//
// An optional `owner: "org:<id>"` creates the tile OWNED BY that org
// (plans/ownership.md D24/D25): the attributed human must be a member with
// the Create knob (or an org/workspace admin). Without it, a human creator
// becomes the user-owner; admin/automation creations are workspace-owned.
func (b *Broker) apiCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		scaffold.Options
		Owner string `json:"owner"`
	}
	if err := server.DecodeJSON(r, &body); err != nil || body.Path == "" {
		server.WriteError(w, http.StatusBadRequest, "need {path, runtime?, title?, expose?, owner?}", "/docs/protocol.md")
		return
	}
	o := body.Options
	// No nesting either way: not inside an existing component, and not a
	// subtree that already contains one.
	if err := b.guardNewComponentTree(strings.Trim(o.Path, "/")); err != nil {
		server.WriteError(w, http.StatusConflict, err.Error())
		return
	}
	p := auth.PrincipalOf(r)
	// Owner resolves FIRST: creating AS an org is gated by the org's Create
	// knob (checked inside resolveCreateOwner), not by personal path patterns
	// — the path no longer encodes the org (D24/D25), so canCreateAt must
	// know which authority applies.
	owner, msg := b.resolveCreateOwner(p, body.Owner)
	if msg != "" {
		server.WriteError(w, http.StatusForbidden, msg, "/docs/auth.md")
		return
	}
	if ok, msg := b.canCreateAt(p, o.Path, owner); !ok {
		server.WriteError(w, http.StatusForbidden, msg, "/docs/auth.md")
		return
	}

	files, err := scaffold.Create(b.Reg.Root, o)
	if err != nil {
		server.WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	b.assignOwner(o.Path, owner)
	// its own git repo from the start, like an imported or instantiated tile
	// (the Code panel's history, an agent's changed-files snapshots) — until
	// now a created tile got one only at the next daemon start
	if err := gitInitComponent(filepath.Join(b.Reg.Root, filepath.FromSlash(o.Path))); err != nil {
		slog.Warn("git init new component", "path", o.Path, "err", err)
	}
	_ = b.Reg.Rescan() // visible immediately; the watcher event follows anyway
	out := map[string]any{"path": o.Path, "files": files}
	if owner != "" {
		out["owner"] = owner
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// resolveCreateOwner decides the owner ref for a new tile (D24). requested ""
// defaults to the attributed human (user-owned) — workspace-owned for
// admin/automation callers. "org:<id>" needs the human to hold the org's
// Create knob (or be an org/workspace admin); "user:<other>" is admin-only.
//
// Under the org-only tile-creation policy (D52), or when their account's
// personal tiles are off (noPersonalTiles, D88), a non-admin human may not
// own tiles personally: "user:<self>" is refused and "" resolves to the ONE
// org where they hold Create (several → they must name it; none → refused).
// Shared by all five creation entry points, so the policy holds for clone,
// template, builtin and git imports too.
func (b *Broker) resolveCreateOwner(p auth.Principal, requested string) (ref, msg string) {
	if b.Users == nil {
		return "", ""
	}
	kind, id, err := users.ParseOwner(requested)
	if err != nil {
		return "", err.Error()
	}
	human := p.UserID // session user, or the user id attributed on a frame principal
	why := ""         // why this human may not own tiles personally ("" = they may)
	if human != "" && !b.IsAdmin(p) {
		why = b.personalRefusal(human)
	}
	if requested == "" {
		if human == "" {
			return "", "" // automation/root: workspace-owned
		}
		u, ok := b.Users.Get(human)
		if !ok || u.IsAdmin() {
			return "", "" // admins default to workspace-owned
		}
		if why != "" {
			return b.defaultOrgOwner(u.ID, why)
		}
		return users.OwnerKindUser + ":" + u.ID, ""
	}
	if err := b.Users.ValidateNewTile(requested); err != nil {
		return "", err.Error()
	}
	if b.IsAdmin(p) && human == "" {
		return requested, ""
	}
	switch kind {
	case users.OwnerKindOrg:
		acc, _ := b.Users.Access(human)
		if !b.IsAdmin(p) && !acc.CanCreateAs(id) {
			return "", "creating tiles owned by org \"" + id + "\" needs the org's Create permission (or org admin)"
		}
		return requested, ""
	case users.OwnerKindUser:
		if why != "" {
			return "", why + " — create it with owner org:<id> where you hold Create"
		}
		if id == human || b.IsAdmin(p) {
			return requested, ""
		}
		return "", "creating tiles owned by another user is a workspace-admin action"
	}
	return requested, ""
}

// personalRefusal says why a non-admin human may not own a tile personally
// ("" = they may): the workspace org-only policy (D52) or their account's
// noPersonalTiles switch (D88). Admins are never refused. Creation and
// transfer-in ask it alike — receiving a tile is creating one (D39).
func (b *Broker) personalRefusal(uid string) string {
	if b.Users == nil || uid == "" {
		return ""
	}
	u, ok := b.Users.Get(uid)
	switch {
	case ok && u.IsAdmin():
		return ""
	case b.Users.TileCreation() == users.TileCreationOrgOnly:
		return "workspace policy: tiles must be owned by an organisation"
	case ok && u.NoPersonalTiles:
		return "personal tiles are turned off for your account (ask a workspace admin)"
	}
	return ""
}

// defaultOrgOwner picks the owner for a creation with no explicit owner by a
// human who may not own personally (why): the single org where they hold
// Create (or org admin).
func (b *Broker) defaultOrgOwner(userID, why string) (ref, msg string) {
	var can []string
	for _, m := range b.Users.UserOrgs(userID) {
		if !m.Suspended && (m.Create || m.Admin) {
			can = append(can, m.ID)
		}
	}
	switch len(can) {
	case 0:
		return "", why + ", and you hold Create in no organisation — ask an org admin to add you"
	case 1:
		return users.OwnerKindOrg + ":" + can[0], ""
	}
	return "", why + " — choose one with owner: org:<id> (you hold Create in " + strings.Join(can, ", ") + ")"
}

// assignOwner records ownership after a successful creation (all five entry
// points; plans/ownership.md). Best-effort: a failed write logs, the tile
// stays workspace-owned, and bx doctor lists it.
//
// It first drops the path's leftover cron jobs and bus subscriptions: a
// removed tile's delivery registrations, not access decisions (grants and
// bindings still refuse the path, D82), so the new tile starts with none and
// registers its own (D85).
func (b *Broker) assignOwner(path, ref string) {
	if n := b.cron.forget(path) + b.bus.forget(path); n > 0 {
		slog.Info("dropped a removed tile's cron jobs and bus subscriptions", "tile", path, "count", n)
	}
	if b.Users == nil || ref == "" {
		return
	}
	if err := b.Users.SetOwner(path, ref); err != nil {
		slog.Warn("assign owner", "tile", path, "owner", ref, "err", err)
	}
}
