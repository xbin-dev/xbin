package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// User-management API (plans/multi-user.md). Gated by the xbin:users
// capability (distinct from xbin:admin so a dedicated user-admin tile can be
// granted only this) — admin implies it. Password hashes never leave here.

func (b *Broker) registerUsers(srv *server.Server) {
	srv.RegisterAPI("GET /whoami", b.apiWhoami)
	srv.RegisterAPI("GET /users", b.apiUsersList)
	srv.RegisterAPI("GET /sessions", func(w http.ResponseWriter, r *http.Request) { b.apiSessions(srv, w, r) })
	srv.RegisterAPI("POST /users", b.apiUsersCreate)
	srv.RegisterAPI("PATCH /users/{id}", func(w http.ResponseWriter, r *http.Request) { b.apiUsersUpdate(srv, w, r) })
	srv.RegisterAPI("POST /users/{id}/invite", b.apiUsersInvite)
	srv.RegisterAPI("DELETE /users/{id}", func(w http.ResponseWriter, r *http.Request) { b.apiUsersDelete(srv, w, r) })
	srv.RegisterAPI("DELETE /users/{id}/sessions", func(w http.ResponseWriter, r *http.Request) { b.apiUsersSignout(srv, w, r) })
	srv.RegisterAPI("POST /account/password", b.apiAccountPassword)
	srv.RegisterAPI("GET /auth-settings", b.apiAuthSettingsGet)
	srv.RegisterAPI("PATCH /auth-settings", b.apiAuthSettingsUpdate)
	srv.RegisterAPI("POST /auth-settings/sso/test", func(w http.ResponseWriter, r *http.Request) { b.apiSSOTest(srv, w, r) })
	b.registerOrgs(srv)
	b.registerRequests(srv)
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
	if b.Users != nil { // workspace tile-creation policy (D52) — owner pickers adapt to it
		out["tileCreation"] = b.Users.TileCreation()
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
		if b.Users != nil { // tiles this user OWNS (D24) — the self-service view
			if owned := b.Users.OwnedBy(users.OwnerKindUser + ":" + p.User.ID); len(owned) > 0 {
				out["owned"] = owned
			}
		}
		if orgs := b.userOrgsView(p.User); len(orgs) > 0 { // plans/orgs.md
			out["orgs"] = orgs
		}
	case p.Owner:
		out["kind"] = "root"
		out["id"] = "root"
		out["name"] = "root (token)"
		out["role"] = users.RoleAdmin
	case p.Component != "":
		out["kind"] = "element"
		out["id"] = p.Component
		if du := b.driverView(p); du != nil {
			out["user"] = du
		}
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// driverView is whoami's `user` object on element principals — the human
// driving the tile (frame/terminal attribution), FILTERED by the tile's
// trust so a low-trust or compromised tile can't harvest memberships:
//
//   - every tile: {id, name} only (attribution the tile can already infer
//     from per-user prefs; the element's own privilege is unchanged);
//   - the tile's own org: plus that ONE org's membership slice (its teams,
//     org-admin flag) — org-internal tiles may adapt to "your team here";
//   - workspace-management tiles (an xbin or xbin:users capability grant,
//     where a compromise already means workspace control): plus the admin
//     flag and the full org list — this is what the manager's
//     create-in-team picker runs on. A policy row denying xbin-caps
//     downgrades the tile's view along with its capability.
func (b *Broker) driverView(p auth.Principal) map[string]any {
	if p.UserID == "" || b.Users == nil {
		return nil
	}
	u, ok := b.Users.Get(p.UserID)
	if !ok {
		return nil
	}
	du := map[string]any{"id": u.ID, "name": u.Name}
	switch {
	case b.elementXbinCapable(p.Component):
		du["admin"] = u.IsAdmin()
		if orgs := b.userOrgsView(u); len(orgs) > 0 {
			du["orgs"] = orgs
		}
	default:
		// An ordinary element learns the driving user's membership only for
		// the org that OWNS it (D24) — never the full membership map.
		if org, inOrg := b.Users.OwnerOrg(p.Component); inOrg {
			for _, m := range b.userOrgsView(u) {
				if m.ID == org {
					du["orgs"] = []users.OrgMembership{m}
					break
				}
			}
		}
	}
	return du
}

// elementXbinCapable reports whether an element holds any workspace-
// management capability (target "xbin" at any role, or "xbin:users") —
// grantedRole applies the policy ceiling, so an xbin-caps deny strips this
// trust tier too.
func (b *Broker) elementXbinCapable(comp string) bool {
	if comp == "" {
		return false
	}
	if _, ok := b.grantedRole(comp, "xbin"); ok {
		return true
	}
	_, ok := b.grantedRole(comp, "xbin:users")
	return ok
}

// userOrgsView is whoami's orgs field: memberships for a regular user; for a
// workspace admin every org, presented as an admin membership (their
// effective reality — they may act in all of them, e.g. create-as-org).
func (b *Broker) userOrgsView(u *users.User) []users.OrgMembership {
	if b.Users == nil {
		return nil
	}
	if !u.IsAdmin() {
		return b.Users.UserOrgs(u.ID)
	}
	var out []users.OrgMembership
	for _, o := range b.Users.Orgs() {
		out = append(out, users.OrgMembership{
			ID: o.ID, Name: o.Name, Level: users.LevelTerminal, Create: true, Admin: true,
		})
	}
	return out
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
	type userOut struct {
		users.User
		InvitePending bool `json:"invitePending,omitempty"`
	}
	list := st.List()
	out := make([]userOut, 0, len(list))
	for _, u := range list {
		full, _ := st.Get(u.ID)
		out = append(out, userOut{User: u, InvitePending: full != nil && full.InvitePending()})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"users": out})
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
	// Disabled suspends/restores the account (D34): everything refuses while
	// set, but rows/memberships/ownership stay for re-enable.
	Disabled *bool  `json:"disabled"`
	Password string `json:"password"`
	// Email binds an SSO identity to this account (docs/auth.md §SSO).
	// Pointer for PATCH presence: absent keeps the current value, "" clears.
	Email *string `json:"email"`
	// SSO (POST only) pre-provisions an SSO-only account: credential-less
	// like the invite flow but WITHOUT minting an invite — the bound email's
	// IdP sign-in is the credential (D52). Requires email.
	SSO bool `json:"sso"`
	// Orgs (POST only) joins the new account to orgs at creation (D53) —
	// each entry overlays a seed-joined default org.
	Orgs []struct {
		Org    string `json:"org"`
		Level  string `json:"level"`
		Create bool   `json:"create"`
		Admin  bool   `json:"admin"`
	} `json:"orgs"`
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
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, name?, role?, tiles?, canCreate?, termApi?, termNet?, email?, password? | sso:true}"})
		return
	}
	if body.SSO {
		if body.Email == nil || strings.TrimSpace(*body.Email) == "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "an SSO account needs an email to bind the IdP identity to"})
			return
		}
		if body.Password != "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "an SSO account takes no password (sign-in is the IdP); drop sso or the password"})
			return
		}
	}
	if body.Password != "" {
		if msg := weakPassword(body.Password); msg != "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": msg})
			return
		}
	}
	if _, exists := st.Get(body.ID); exists {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
		return
	}
	// SSO-only mode (D53): a non-admin can't be given a password path — the
	// invite link would mint one.
	if st.PasswordLoginDisabled() && body.Role != users.RoleAdmin && !body.SSO && body.Password == "" {
		server.WriteJSON(w, http.StatusConflict, map[string]string{
			"error": "password sign-in is disabled for non-admins — create the account with sso:true and an email"})
		return
	}
	// Orgs are validated BEFORE the account exists so a typo never leaves a
	// half-provisioned user behind.
	for _, o := range body.Orgs {
		if _, ok := st.Org(o.Org); !ok {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "no such org " + o.Org})
			return
		}
	}
	tiles, err := users.ParseTiles(body.Tiles, body.Terminal != nil && *body.Terminal)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	nu := users.User{
		ID: body.ID, Name: firstNonEmpty(body.Name, body.ID), Role: body.Role,
		Tiles: tiles, CanCreate: body.CanCreate,
		TermAPI: body.TermAPI != nil && *body.TermAPI,
		TermNet: body.TermNet != nil && *body.TermNet,
	}
	if body.Email != nil {
		nu.Email = *body.Email
	}
	// No password → invite flow (D22): create the account credential-less and
	// mint a single-use set-your-password link the admin delivers. There is no
	// self-signup — accounts only ever come from here. sso:true is the same
	// credential-less row with NO invite: the bound email signs in through
	// the IdP (pre-provisioning, D52). Either way the new-account defaults
	// (defaults.go) seed what the request left out.
	var u *users.User
	var invite string
	switch {
	case body.SSO:
		u, err = st.UpsertInvited(nu)
	case body.Password == "":
		if u, err = st.UpsertInvited(nu); err == nil {
			invite, err = st.CreateInvite(u.ID, 0)
		}
	default:
		u, err = st.Upsert(nu, body.Password)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	for _, o := range body.Orgs {
		level, create, admin := o.Level, o.Create, o.Admin
		if level == "" {
			level = users.LevelRead
		}
		if _, err := st.SetOrgMember(o.Org, u.ID, users.MemberPatch{Level: &level, Create: &create, Admin: &admin}); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "join " + o.Org + ": " + err.Error(), "user": u.ID})
			return
		}
	}
	b.usersEvent() // refresh open user/admin panels (matches org/team/access mutations)
	out := map[string]any{"user": u.Public()}
	if orgs := st.UserOrgs(u.ID); len(orgs) > 0 {
		out["orgs"] = orgs
	}
	if invite != "" {
		out["invite"] = invite
		out["inviteUrl"] = "/login?invite=" + invite
		if l := b.inviteLink(r, invite); l != "" {
			out["inviteLink"] = l // absolute — what a curl-driven admin pastes
		}
		out["inviteExpires"] = time.Now().Add(users.InviteTTL).Unix()
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// inviteLink builds the absolute invite URL. The configured --external-url
// wins (a stable public identity the operator vouched for); else it falls
// back to the request's own host/scheme (X-Forwarded-Proto honored — invites
// travel through reverse proxies). bx and the admin tile prepend their known
// origin themselves; this saves the raw-curl admin from pasting a relative
// path into a chat.
func (b *Broker) inviteLink(r *http.Request, tok string) string {
	if b.ExternalURL != "" {
		return b.ExternalURL + "/login?invite=" + tok
	}
	if r.Host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/login?invite=" + tok
}

// apiAccountPassword is the self-service password change (D38): a signed-in
// user rotates their OWN credential after proving the current one. Not
// admin-gated, not usable by elements.
func (b *Broker) apiAccountPassword(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	if p.Component != "" || p.User == nil {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "password change is for signed-in users (the bootstrap token has no password)"})
		return
	}
	var body struct{ Current, New string }
	if err := decodeJSON(r, &body); err != nil || body.New == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {current, new}"})
		return
	}
	if err := st.ChangePassword(p.User.ID, body.Current, body.New); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// apiUsersInvite (re)mints a single-use invite link for an existing user —
// credential delivery and "reset by link" in one (D22). The current password,
// if any, keeps working until the invite is redeemed; re-minting invalidates
// any previous link. Admin/xbin:users — plus, as delegated reset-by-link
// (D38), a signed-in ORG ADMIN for a NON-ADMIN member of their org ("the kid
// forgot their password" shouldn't need the workspace admin; resetting an
// admin user always does).
func (b *Broker) apiUsersInvite(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	if !b.canManageUsers(p) {
		allowed := false
		if p.Component == "" && p.User != nil {
			if target, ok := st.Get(r.PathValue("id")); ok && !target.IsAdmin() {
				for _, om := range p.Access.AdminOrgs() {
					if o, found := st.Org(om); found {
						if m, isMember := o.Member(target.ID); isMember && !m.Suspended {
							allowed = true
						}
					}
				}
			}
		}
		if !allowed {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "invites are minted by admins — or by an org admin for a non-admin member of their org (reset-by-link, D38)"})
			return
		}
	}
	if st.PasswordLoginDisabled() {
		if target, ok := st.Get(r.PathValue("id")); ok && !target.IsAdmin() {
			server.WriteJSON(w, http.StatusConflict, map[string]string{
				"error": "password sign-in is disabled for non-admins — this account signs in through the IdP (bind its email instead)"})
			return
		}
	}
	tok, err := st.CreateInvite(r.PathValue("id"), 0)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	out := map[string]any{
		"invite": tok, "inviteUrl": "/login?invite=" + tok,
		"inviteExpires": time.Now().Add(users.InviteTTL).Unix(),
	}
	if l := b.inviteLink(r, tok); l != "" {
		out["inviteLink"] = l
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func (b *Broker) apiUsersUpdate(srv *server.Server, w http.ResponseWriter, r *http.Request) {
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
		Disabled: cur.Disabled, Email: cur.Email, RoleVia: cur.RoleVia, // (Upsert drops RoleVia on a role change)
	}
	if body.Email != nil {
		nu.Email = *body.Email
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
	if body.Disabled != nil {
		// Lockout guards (D34): you can't disable yourself, and the last
		// enabled admin USER can't be disabled (the bootstrap token may
		// already be login-disabled — a workspace with zero usable admins is
		// unrecoverable from the UI).
		if *body.Disabled && !cur.Disabled {
			p := auth.PrincipalOf(r)
			self := p.UserID
			if self == "" && p.User != nil {
				self = p.User.ID
			}
			if self == cur.ID {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "you can't disable your own account"})
				return
			}
			if cur.IsAdmin() {
				others := false
				for _, u := range st.List() {
					if u.IsAdmin() && !u.Disabled && u.ID != cur.ID {
						others = true
						break
					}
				}
				if !others {
					server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "refusing to disable the last enabled admin user"})
					return
				}
			}
		}
		nu.Disabled = *body.Disabled
	}
	u, err := st.Upsert(nu, body.Password)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if nu.Disabled && !cur.Disabled && srv != nil && srv.Auth != nil {
		srv.Auth.DropUserSessions(u.ID) // the snapshot rule already refuses them; this clears the list
	}
	b.usersEvent() // refresh open user/admin panels (matches org/team/access mutations)
	server.WriteJSON(w, http.StatusOK, u.Public())
}

func (b *Broker) apiUsersDelete(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	orphaned, err := st.Delete(r.PathValue("id"))
	if err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if srv != nil && srv.Auth != nil {
		srv.Auth.DropUserSessions(r.PathValue("id"))
	}
	b.usersEvent() // a deleted user's sessions/frame/terminal principals are gone — refresh panels
	// orphanedTiles: what just fell to workspace-owned, so the handover is
	// explicit rather than silent (re-assign with bx owner).
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "orphanedTiles": orphaned})
}

// apiUsersSignout — DELETE /users/{id}/sessions: "sign out everywhere"
// (D53). Ends every browser session and terminal token of the user; they
// can sign in again (disable the account to stop that).
func (b *Broker) apiUsersSignout(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	u, ok := st.Get(r.PathValue("id"))
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such user"})
		return
	}
	n := 0
	if srv != nil && srv.Auth != nil {
		n = srv.Auth.DropUserSessions(u.ID)
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "dropped": n})
}

// apiSSOTest — POST /auth-settings/sso/test: probe the provider (discovery +
// JWKS, or GitHub reachability) for the stored config or the admin's unsaved
// draft {sso:{…}}. Always 200 — the body is a report, ok:false included.
func (b *Broker) apiSSOTest(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	if st := b.usersStore(w); st == nil {
		return
	}
	var body struct {
		SSO *users.SSOConfig `json:"sso"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &body); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {} or {sso: {kind, preset?, issuer?, clientId, …}}"})
			return
		}
	}
	server.WriteJSON(w, http.StatusOK, srv.SSOTest(r.Context(), body.SSO))
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
		"tokenLoginDisabled":    st.TokenLoginDisabled(),
		"hasAdminUser":          st.HasAdmin(),
		"canDisable":            st.HasAdmin() && b.attributedAdminUser(auth.PrincipalOf(r)),
		"sso":                   b.ssoView(st),
		"passwordLoginDisabled": st.PasswordLoginDisabled(),
		"canDisablePassword":    b.ssoReady(st), // same predicate as the PATCH guard
	})
}

// ssoReady: a provider is configured AND the daemon knows its public URL —
// the login button exists, so SSO-only mode can't strand non-admins.
func (b *Broker) ssoReady(st *users.Store) bool {
	return st.SSO().Enabled() && b.ExternalURL != ""
}

// ssoView is auth-settings' SSO block: the stored config with the client
// secret reduced to a set/unset bool (write-only — it never leaves the
// store), plus the daemon-side readiness facts the admin UI explains.
func (b *Broker) ssoView(st *users.Store) map[string]any {
	c := st.SSO()
	out := map[string]any{
		"enabled":     c.Enabled(),
		"externalUrl": b.ExternalURL,
		"ready":       c.Enabled() && b.ExternalURL != "",
	}
	if c != nil {
		out["kind"], out["preset"] = c.Kind, c.Preset
		out["issuer"], out["clientId"] = c.Issuer, c.ClientID
		out["allowedDomains"] = c.AllowedDomains
		out["buttonLabel"] = c.ButtonLabel
		out["clientSecretSet"] = c.ClientSecret != ""
		out["groupsClaim"], out["groupsScope"] = c.GroupsClaim, c.GroupsScope
		out["adminGroups"] = c.AdminGroups
	}
	// Group sync status (D53): whether any rule is active, every group the
	// IdP has been seen sending (the console's datalist), and the newest
	// recorded fetch failure — derived from user rows, nothing extra stored.
	sync := map[string]any{"rulesActive": st.SSOGroupRulesExist(), "knownGroups": st.KnownSSOGroups()}
	var last *users.User
	for _, u := range st.List() {
		if u.SSOSyncError != "" && (last == nil || u.LastLogin > last.LastLogin) {
			uu := u
			last = &uu
		}
	}
	if last != nil {
		sync["lastError"] = map[string]any{"user": last.ID, "at": last.LastLogin, "error": last.SSOSyncError}
	}
	out["groupSync"] = sync
	return out
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
		TokenLoginDisabled    *bool           `json:"tokenLoginDisabled"`
		SSO                   json.RawMessage `json:"sso"`
		PasswordLoginDisabled *bool           `json:"passwordLoginDisabled"`
	}
	if err := decodeJSON(r, &body); err != nil || (body.TokenLoginDisabled == nil && body.SSO == nil && body.PasswordLoginDisabled == nil) {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tokenLoginDisabled: bool} and/or {sso: {...}|null} and/or {passwordLoginDisabled: bool}"})
		return
	}
	// SSO config: an object replaces (empty clientSecret keeps the stored
	// one), an explicit null clears SSO entirely. Same capability gate as the
	// rest of sign-in policy.
	if body.SSO != nil {
		if string(body.SSO) == "null" {
			if err := st.SetSSO(nil); err != nil {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		} else {
			var c users.SSOConfig
			if err := json.Unmarshal(body.SSO, &c); err != nil {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad sso object: " + err.Error()})
				return
			}
			if err := st.SetSSO(&c); err != nil {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
		}
	}
	// SSO-only mode (D53): non-admins sign in through the IdP only. Enabling
	// needs a READY provider (configured + --external-url) — the same
	// predicate GET reports as canDisablePassword.
	if body.PasswordLoginDisabled != nil {
		if *body.PasswordLoginDisabled && !b.ssoReady(st) {
			server.WriteJSON(w, http.StatusConflict, map[string]string{
				"error": "SSO must be configured and --external-url set before disabling password sign-in"})
			return
		}
		if err := st.SetPasswordLoginDisabled(*body.PasswordLoginDisabled); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if body.TokenLoginDisabled == nil {
		server.WriteJSON(w, http.StatusOK, map[string]any{
			"tokenLoginDisabled": st.TokenLoginDisabled(), "sso": b.ssoView(st),
			"passwordLoginDisabled": st.PasswordLoginDisabled(),
		})
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
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"tokenLoginDisabled": st.TokenLoginDisabled(), "sso": b.ssoView(st),
		"passwordLoginDisabled": st.PasswordLoginDisabled(),
	})
}

// apiSessions lists live browser sessions with their client IPs — the
// admin console's sessions tab, and the attribution view for the /c/
// warm-IP gate (an asset request from one of these IPs within an hour of
// its last activity is what "recently authenticated" means there). Session
// ids are credentials and are NEVER serialized; the caller's own row is
// marked server-side. Note the bootstrap owner-token "session" is stateless
// (the cookie holds the token itself), so token logins don't appear here.
func (b *Broker) apiSessions(srv *server.Server, w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	// current is matched by session cookie, so it marks only cookie-
	// authenticated callers (a direct browser fetch, curl with the cookie).
	// The admin TILE drives this endpoint via a frame token with no cookie
	// (ND8), so no row reads current there — cosmetic, documented as such.
	current := ""
	if c, err := r.Cookie(auth.CookieName); err == nil {
		current = c.Value
	}
	out := []map[string]any{}
	for _, si := range srv.Auth.Sessions() {
		name := si.UserID
		if b.Users != nil {
			if u, ok := b.Users.Get(si.UserID); ok && u.Name != "" {
				name = u.Name
			}
		}
		out = append(out, map[string]any{
			"user":       si.UserID,
			"name":       name,
			"created":    si.Created.Unix(),
			"lastActive": si.LastActive.Unix(),
			"ip":         si.IP,
			"lastIP":     si.LastIP,
			"current":    current != "" && si.ID == current,
		})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
