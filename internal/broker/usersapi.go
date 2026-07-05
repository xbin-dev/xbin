package broker

import (
	"net/http"
	"strings"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/users"
)

// User-management API (plans/multi-user.md). Gated by the xbin:users
// capability (distinct from xbin:admin so a dedicated user-admin tile can be
// granted only this) — admin implies it. Password hashes never leave here.

func (b *Broker) registerUsers(srv *server.Server) {
	srv.RegisterAPI("GET /whoami", b.apiWhoami)
	srv.RegisterAPI("GET /users", b.apiUsersList)
	srv.RegisterAPI("POST /users", b.apiUsersCreate)
	srv.RegisterAPI("PATCH /users/{id}", b.apiUsersUpdate)
	srv.RegisterAPI("DELETE /users/{id}", b.apiUsersDelete)
}

// canManageUsers: root/admin, or an element granted xbin:users (or xbin:admin).
func (b *Broker) canManageUsers(p auth.Principal) bool {
	if b.IsAdmin(p) {
		return true
	}
	if p.Component == "" {
		return false
	}
	if role, ok := b.grantedRole(p.Component, "xbin"); ok && roleSatisfies(role, "admin", nil) {
		return true
	}
	role, ok := b.grantedRole(p.Component, "xbin:users")
	return ok && roleSatisfies(role, "writer", nil)
}

func (b *Broker) requireUsersCap(w http.ResponseWriter, r *http.Request) bool {
	if b.canManageUsers(auth.PrincipalOf(r)) {
		return true
	}
	server.WriteJSON(w, http.StatusForbidden, map[string]string{
		"error": "user management needs admin or the xbin:users capability", "docs": "/docs/auth.md",
	})
	return false
}

// apiWhoami reports the caller's identity + permissions (any authenticated
// principal — a tile uses it to adapt its UI to the current user).
func (b *Broker) apiWhoami(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	out := map[string]any{
		"admin":    b.IsAdmin(p),
		"terminal": p.CanTerminal(),
	}
	switch {
	case p.User != nil:
		out["kind"] = "user"
		out["id"] = p.User.ID
		out["name"] = p.User.Name
		out["role"] = p.User.Role
		out["tiles"] = p.User.Tiles
	case p.Owner:
		out["kind"] = "root"
		out["id"] = "root"
		out["name"] = "root (token)"
		out["role"] = users.RoleAdmin
	case p.Component != "":
		out["kind"] = "element"
		out["id"] = p.Component
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func (b *Broker) usersStore(w http.ResponseWriter) *users.Store {
	if b.Users == nil {
		server.WriteJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "user store unavailable"})
		return nil
	}
	return b.Users
}

func (b *Broker) apiUsersList(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"users": st.List()})
}

type userBody struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Role     string   `json:"role"`
	Tiles    []string `json:"tiles"`
	Terminal bool     `json:"terminal"`
	Password string   `json:"password"`
}

func (b *Broker) apiUsersCreate(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body userBody
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.ID) == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, name?, role?, tiles?, terminal?, password}"})
		return
	}
	if body.Password == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "password required for a new user"})
		return
	}
	if _, exists := st.Get(body.ID); exists {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
		return
	}
	u, err := st.Upsert(users.User{
		ID: body.ID, Name: firstNonEmpty(body.Name, body.ID), Role: body.Role,
		Tiles: body.Tiles, Terminal: body.Terminal,
	}, body.Password)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, u.Public())
}

func (b *Broker) apiUsersUpdate(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	id := r.PathValue("id")
	cur, ok := st.Get(id)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such user"})
		return
	}
	// Start from current, overlay provided fields (only password if non-empty).
	body := userBody{ID: cur.ID, Name: cur.Name, Role: cur.Role, Tiles: cur.Tiles, Terminal: cur.Terminal}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	body.ID = cur.ID // id is immutable
	u, err := st.Upsert(users.User{
		ID: body.ID, Name: body.Name, Role: body.Role, Tiles: body.Tiles, Terminal: body.Terminal,
	}, body.Password)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, u.Public())
}

func (b *Broker) apiUsersDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	if err := st.Delete(r.PathValue("id")); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
