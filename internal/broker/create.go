package broker

import (
	"log/slog"
	"net/http"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/scaffold"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/users"
)

// POST /api/xbin/create — the higher-level "create tile" API (same engine
// as `bx new`). Creating components is an editing-plane action, so callers
// are the owner/admins, a user whose CanCreate patterns cover the path
// (D16 — "create ≈ own a namespace"), or elements holding the
// workspace-management capability: an explicit grant on the reserved target
// "xbin" at role writer (the template ships tiles/manager with that grant;
// revoke it and the tile request shows up in the grants panel like any other).
func (b *Broker) apiCreate(w http.ResponseWriter, r *http.Request) {
	var o scaffold.Options
	if err := decodeJSON(r, &o); err != nil || o.Path == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"error": "need {path, runtime?, title?, expose?}", "docs": "/docs/protocol.md",
		})
		return
	}
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) && !p.CanCreateTile(o.Path) {
		role, ok := b.grantedRole(p.Component, "xbin")
		if p.Component == "" || !ok || !roleSatisfies(role, "writer", nil) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error": "creating components needs a create permission on this path (ask an admin), or the workspace-management grant — declare {\"target\":\"xbin\",\"role\":\"writer\"} in \"uses\" and have the owner approve it",
				"docs":  "/docs/auth.md",
			})
			return
		}
	}

	files, err := scaffold.Create(b.Reg.Root, o)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// The creator owns their new namespace: a non-admin user who creates a
	// tile gets terminal on it (D16 auto-grant) — whether directly or by
	// driving a manager-style tile (frame principal; the user id rides along
	// for attribution). Admins have everything already.
	if p.UserID != "" && b.Users != nil {
		if u, ok := b.Users.Get(p.UserID); ok && !u.IsAdmin() {
			if err := b.Users.GrantTile(u.ID, o.Path, users.LevelTerminal); err != nil {
				slog.Warn("create auto-grant", "user", u.ID, "tile", o.Path, "err", err)
			}
		}
	}
	_ = b.Reg.Rescan() // visible immediately; the watcher event follows anyway
	server.WriteJSON(w, http.StatusOK, map[string]any{"path": o.Path, "files": files})
}
