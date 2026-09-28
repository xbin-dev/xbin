package broker

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// GET /access/{user} — a tile's backend asks what a person it names may do
// on ITSELF (docs/protocol.md; the sandbox-terminal tile checks it at every
// SSH login, D121): {user, level: none|read|write|terminal, active}.
//
// A tile learns a person's level from X-XBin-User-Level only while that
// person is calling it; a credential of its own that outlives the call — an
// SSH key registered on its page — needs to ask again later, when the
// person may have been removed from the workspace or from the tile. The
// level is the one the proxy would put in X-XBin-User-Level now (the same
// users.Access resolution: ownership, org membership and shares, the
// person's own entries, the workspace defaults, the personal plane — D24,
// D31, D88), and none for a disabled or unknown account, whose frame tokens
// and sessions stop working the moment it is disabled (D34).
//
// Only the calling tile, and only its backend (the instance token): the
// answer never names another tile, and a frontend or a shell in the tile
// (acting for the person using it) can't list who else uses it. Nothing
// 404s — an unknown id is {level: none, active: false}.
func (b *Broker) apiAccessOf(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component == "" || p.Via != "instance" {
		server.WriteError(w, http.StatusForbidden, "a tile's backend asks (its instance token): a person's level on the calling tile", "/docs/protocol.md")
		return
	}
	id := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(r.PathValue("user"), "user:")))
	if id == "" || len(id) > 64 {
		server.WriteError(w, http.StatusBadRequest, "name a person: GET /access/<user id>")
		return
	}
	level, active := users.LevelNone, false
	if b.Users != nil {
		if u, ok := b.Users.Get(id); ok && !u.Disabled {
			active = true
			if acc, ok := b.Users.Access(id); ok {
				if l := acc.TileLevel(p.Component); l != "" {
					level = l
				}
			}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"user": id, "level": level, "active": active})
}
