package broker

import (
	"net/http"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/scaffold"
	"github.com/magik6k/buxon/internal/server"
)

// POST /api/buxon/create — the higher-level "create tile" API (same engine
// as `bx new`). Creating components is an editing-plane action, so callers
// are the owner or elements holding the workspace-management capability:
// an explicit grant on the reserved target "buxon" at role writer
// (the template ships tiles/manager with that grant; revoke it and the tile
// request shows up in the grants panel like any other).
func (b *Broker) apiCreate(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !b.IsAdmin(p) {
		role, ok := b.grantedRole(p.Component, "buxon")
		if p.Component == "" || !ok || !roleSatisfies(role, "writer", nil) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error": "creating components needs the workspace-management grant — declare {\"target\":\"buxon\",\"role\":\"writer\"} in \"uses\" and have the owner approve it",
				"docs":  "/docs/auth.md",
			})
			return
		}
	}

	var o scaffold.Options
	if err := decodeJSON(r, &o); err != nil || o.Path == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"error": "need {path, runtime?, title?, expose?}", "docs": "/docs/protocol.md",
		})
		return
	}
	files, err := scaffold.Create(b.Reg.Root, o)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	_ = b.Reg.Rescan() // visible immediately; the watcher event follows anyway
	server.WriteJSON(w, http.StatusOK, map[string]any{"path": o.Path, "files": files})
}
