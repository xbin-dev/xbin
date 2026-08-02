package broker

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/scaffold"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// POST /api/xbin/create — the higher-level "create tile" API (same engine
// as `bx new`). Creating components is an editing-plane action, so callers
// are the owner/admins, a user whose create patterns cover the path (D16 —
// "create ≈ own a namespace"), or elements holding the workspace-management
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
	if err := decodeJSON(r, &body); err != nil || body.Path == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"error": "need {path, runtime?, title?, expose?, owner?}", "docs": "/docs/protocol.md",
		})
		return
	}
	o := body.Options
	// No nesting either way: not inside an existing component, and not a
	// subtree that already contains one.
	if err := b.guardNewComponentTree(strings.Trim(o.Path, "/")); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	p := auth.PrincipalOf(r)
	if ok, msg := b.canCreateAt(p, o.Path); !ok {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
		return
	}
	owner, msg := b.resolveCreateOwner(p, body.Owner)
	if msg != "" {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
		return
	}

	files, err := scaffold.Create(b.Reg.Root, o)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.assignOwner(o.Path, owner)
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
func (b *Broker) resolveCreateOwner(p auth.Principal, requested string) (ref, msg string) {
	if b.Users == nil {
		return "", ""
	}
	kind, id, err := users.ParseOwner(requested)
	if err != nil {
		return "", err.Error()
	}
	human := p.UserID // session user, or the user id attributed on a frame principal
	if requested == "" {
		if human == "" {
			return "", "" // automation/root: workspace-owned
		}
		if u, ok := b.Users.Get(human); ok && !u.IsAdmin() {
			return users.OwnerKindUser + ":" + u.ID, ""
		}
		return "", "" // admins default to workspace-owned
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
		if id == human || b.IsAdmin(p) {
			return requested, ""
		}
		return "", "creating tiles owned by another user is a workspace-admin action"
	}
	return requested, ""
}

// assignOwner records ownership after a successful creation (all five entry
// points; plans/ownership.md). Best-effort: a failed write logs, the tile
// stays workspace-owned, and bx doctor lists it.
func (b *Broker) assignOwner(path, ref string) {
	if b.Users == nil || ref == "" {
		return
	}
	if err := b.Users.SetOwner(path, ref); err != nil {
		slog.Warn("assign owner", "tile", path, "owner", ref, "err", err)
	}
}
