package broker

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Human access requests (D36). The element plane has had a request/approve
// loop since D5; this is the same shape for people: any signed-in user files
// (tile, wanted level, note), and the tile's manager set — its user-owner,
// the owning org's admins, or a ws-admin (mayManageTile, the D24 sharing
// gate) — approves it into an exact ACL entry (authoritative, D31) or
// dismisses it. Requesters can withdraw. Every change publishes a `users`
// event so badges/queues update live.

func (b *Broker) registerRequests(srv *server.Server) {
	srv.RegisterAPI("GET /access-requests", b.apiRequestsList)
	srv.RegisterAPI("POST /access-requests", b.apiRequestCreate)
	srv.RegisterAPI("POST /access-requests/approve", b.apiRequestApprove)
	srv.RegisterAPI("DELETE /access-requests", b.apiRequestDelete)
}

// requestHuman: the signed-in human behind the call — requests are a
// people-plane feature; elements keep their grants queue.
func requestHuman(p auth.Principal) string {
	if p.Component != "" || p.User == nil {
		return ""
	}
	return p.User.ID
}

func (b *Broker) apiRequestCreate(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	uid := requestHuman(p)
	if uid == "" {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "access requests are filed by signed-in users (elements use the grants queue)"})
		return
	}
	var body struct {
		Tile  string `json:"tile"`
		Level string `json:"level"`
		Note  string `json:"note"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tile, level: read|write|terminal, note?}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	if body.Level == "" {
		body.Level = users.LevelRead
	}
	if _, ok := b.Reg.Component(body.Tile); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such tile"})
		return
	}
	if lvlSatisfied(p, body.Tile, body.Level) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "you already have " + body.Level + " on " + body.Tile})
		return
	}
	if err := st.CreateAccessRequest(uid, body.Tile, body.Level, body.Note); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "tile": body.Tile, "level": body.Level})
}

func lvlSatisfied(p auth.Principal, tile, level string) bool {
	switch level {
	case users.LevelWrite:
		return p.CanWriteTile(tile)
	case users.LevelTerminal:
		return p.CanTerminalTile(tile)
	default:
		return p.CanReadTile(tile)
	}
}

// requestView is one row scoped to the viewer: mine = I filed it; manage =
// I may approve/dismiss it.
type requestView struct {
	users.AccessRequest
	Mine   bool `json:"mine,omitempty"`
	Manage bool `json:"manage,omitempty"`
}

func (b *Broker) apiRequestsList(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	uid := requestHuman(p)
	admin := b.canManageUsers(p)
	if uid == "" && !admin {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin or signed-in users only"})
		return
	}
	out := []requestView{}
	for _, req := range st.AccessRequests() {
		v := requestView{AccessRequest: req, Mine: uid != "" && req.User == uid,
			Manage: admin || b.mayManageTile(p, req.Tile)}
		if v.Mine || v.Manage {
			out = append(out, v)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (b *Broker) apiRequestApprove(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		User  string `json:"user"`
		Tile  string `json:"tile"`
		Level string `json:"level"` // optional override; default = as requested
	}
	if err := decodeJSON(r, &body); err != nil || body.User == "" || body.Tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {user, tile, level?}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	p := auth.PrincipalOf(r)
	if !b.mayManageTile(p, body.Tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "approving needs the tile's owner, the owning org's admins, or a workspace admin (D36)", "docs": "/docs/auth.md"})
		return
	}
	level := body.Level
	found := false
	for _, req := range st.AccessRequests() {
		if req.User == body.User && req.Tile == body.Tile {
			found = true
			if level == "" {
				level = req.Level
			}
		}
	}
	if !found {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such request"})
		return
	}
	if err := st.SetUserTile(body.User, body.Tile, level); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if _, err := st.DeleteAccessRequest(body.User, body.Tile, false); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "user": body.User, "tile": body.Tile, "level": level})
}

func (b *Broker) apiRequestDelete(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		User string `json:"user"`
		Tile string `json:"tile"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {user?, tile}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	p := auth.PrincipalOf(r)
	uid := requestHuman(p)
	if body.User == "" {
		body.User = uid // withdraw your own
	}
	if body.User != uid && !b.mayManageTile(p, body.Tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "withdraw your own requests; dismissing others' needs the tile's manager (D36)"})
		return
	}
	// A manager removing someone ELSE's request is a dismissal — it starts
	// the re-file cooldown; withdrawing your own doesn't.
	found, err := st.DeleteAccessRequest(body.User, body.Tile, body.User != uid)
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !found {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such request"})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}
