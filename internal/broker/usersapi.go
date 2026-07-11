package broker

import (
	"encoding/json"
	"fmt"
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
	srv.RegisterAPI("GET /auth-settings", b.apiAuthSettingsGet)
	srv.RegisterAPI("PATCH /auth-settings", b.apiAuthSettingsUpdate)
	b.registerOrgs(srv)
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
		out["tiles"] = p.User.Tiles // path→level map (D16); direct entries only
		out["canCreate"] = p.User.CanCreate
		out["termApi"] = p.CanTermAPI()
		out["termNet"] = p.CanTermNet()
		if b.Users != nil { // org/team memberships (plans/orgs.md)
			if orgs := b.Users.UserOrgs(p.User.ID); len(orgs) > 0 {
				out["orgs"] = orgs
			}
		}
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
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
	// Tiles is either the current shape (a path→level map, levels
	// read|write|terminal — D16) or the legacy allow-list array; users.ParseTiles
	// normalizes, so pre-tiers clients (an old admin tile) keep working.
	Tiles json.RawMessage `json:"tiles"`
	// Terminal is the legacy global flag. With an array Tiles it picks the
	// migrated level (write vs terminal); alone on PATCH it re-levels the
	// user's existing entries (the old ±term toggle).
	Terminal  *bool    `json:"terminal"`
	CanCreate []string `json:"canCreate"`
	TermAPI   *bool    `json:"termApi"`
	TermNet   *bool    `json:"termNet"`
	Password  string   `json:"password"`
}

// minPasswordLen is the floor for account passwords set through the API. It's
// enforced here (not in users.Store) so the dev-seeded admin and unit tests,
// which set passwords through the store directly, aren't subject to UX policy.
const minPasswordLen = 8

// weakPassword returns a user-facing reason a password is rejected, or "".
func weakPassword(pw string) string {
	if len([]rune(pw)) < minPasswordLen {
		return fmt.Sprintf("password too short (min %d characters)", minPasswordLen)
	}
	return ""
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
	if msg := weakPassword(body.Password); msg != "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
		return
	}
	if _, exists := st.Get(body.ID); exists {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
		return
	}
	tiles, err := users.ParseTiles(body.Tiles, body.Terminal != nil && *body.Terminal)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	u, err := st.Upsert(users.User{
		ID: body.ID, Name: firstNonEmpty(body.Name, body.ID), Role: body.Role,
		Tiles: tiles, CanCreate: body.CanCreate,
		TermAPI: body.TermAPI != nil && *body.TermAPI,
		TermNet: body.TermNet != nil && *body.TermNet,
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
	// Start from current, overlay provided fields (string fields by prefill;
	// tiles/flags by presence — absent keeps the current value, and password
	// only when non-empty).
	body := userBody{ID: cur.ID, Name: cur.Name, Role: cur.Role}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	body.ID = cur.ID         // id is immutable
	if body.Password != "" { // only when actually changing it (empty keeps the old hash)
		if msg := weakPassword(body.Password); msg != "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
	}
	tiles := cur.Tiles
	if body.Tiles != nil {
		var err error
		if tiles, err = users.ParseTiles(body.Tiles, body.Terminal != nil && *body.Terminal); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	} else if body.Terminal != nil {
		// The legacy toggle alone (the old admin tile's ±term button): re-level
		// the user's existing entries — on raises everything to terminal, off
		// caps terminal entries back to write.
		tiles = make(map[string]string, len(cur.Tiles))
		for k, v := range cur.Tiles {
			switch {
			case *body.Terminal:
				tiles[k] = users.LevelTerminal
			case v == users.LevelTerminal:
				tiles[k] = users.LevelWrite
			default:
				tiles[k] = v
			}
		}
	}
	nu := users.User{
		ID: body.ID, Name: body.Name, Role: body.Role, Tiles: tiles,
		CanCreate: cur.CanCreate, TermAPI: cur.TermAPI, TermNet: cur.TermNet,
	}
	if body.CanCreate != nil {
		nu.CanCreate = body.CanCreate
	}
	if body.TermAPI != nil {
		nu.TermAPI = *body.TermAPI
	}
	if body.TermNet != nil {
		nu.TermNet = *body.TermNet
	}
	u, err := st.Upsert(nu, body.Password)
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

// attributedAdminUser reports whether the human behind a request is a
// signed-in admin *user* — directly (session principal, User snapshot set) or
// driving a tile (frame-token principal, where the user id rides along for
// attribution but User is nil by design; resolve it here). The bootstrap owner
// token carries no user id and reports false.
func (b *Broker) attributedAdminUser(p auth.Principal) bool {
	if p.User != nil {
		return p.User.IsAdmin()
	}
	if p.UserID != "" && b.Users != nil {
		if u, ok := b.Users.Get(p.UserID); ok {
			return u.IsAdmin()
		}
	}
	return false
}

// apiAuthSettingsGet reports the owner-token browser-login state, whether an
// admin user exists, and whether THIS caller may disable token login (the
// admin tile renders its toggle from canDisable — one predicate, shared with
// the PATCH guard, so the UI can never disagree with the server).
func (b *Broker) apiAuthSettingsGet(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"tokenLoginDisabled": st.TokenLoginDisabled(),
		"hasAdminUser":       st.HasAdmin(),
		"canDisable":         st.HasAdmin() && b.attributedAdminUser(auth.PrincipalOf(r)),
	})
}

// apiAuthSettingsUpdate toggles owner-token browser login. Disabling it is
// guarded twice against lockout: an admin user must exist (enforced in the
// store), and the caller must be a signed-in admin *user* — not the bootstrap
// owner token itself, whose own session this would invalidate.
func (b *Broker) apiAuthSettingsUpdate(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		TokenLoginDisabled *bool `json:"tokenLoginDisabled"`
	}
	if err := decodeJSON(r, &body); err != nil || body.TokenLoginDisabled == nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tokenLoginDisabled: bool}"})
		return
	}
	if *body.TokenLoginDisabled {
		// Accepts a direct admin-user session AND an admin user driving a tile
		// (frame-token principal, user id attributed); rejects the bootstrap
		// owner token, whose own browser session this would invalidate.
		if !b.attributedAdminUser(auth.PrincipalOf(r)) {
			server.WriteJSON(w, http.StatusConflict, map[string]string{
				"error": "sign in as an admin user (not the bootstrap token) before disabling token login",
			})
			return
		}
	}
	if err := st.SetTokenLoginDisabled(*body.TokenLoginDisabled); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"tokenLoginDisabled": st.TokenLoginDisabled()})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
