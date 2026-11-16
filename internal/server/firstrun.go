package server

// firstrun.go — the first-run sheet's two routes (D237; docs/protocol/
// users.md "First run"): what lets the owner's first visit end signed in
// as an account of their own, in one sheet, instead of on the bootstrap
// token.
//
//	GET  /first-run          {bootstrap}: true only to the owner token's
//	                         browser session in a workspace with no accounts —
//	                         the one state xbin's chrome shows the sheet in
//	POST /first-run/session  {id, password}: this browser signs in as the
//	                         account the sheet just made (POST /users, the
//	                         ordinary path: its password rules, its audit
//	                         line) — the session swapped in one step
//
// The sheet's flow is POST /users {role: admin}, then this swap, then (as
// the new account) the vault's setup and the workspace agent's switch.
// The swap is a sign-in, and no wider than POST /login: it takes the
// account's own password, from the token's browser session only, for the
// bootstrap account only — the workspace's ONLY account, an enabled admin
// that has never signed in, made in the last firstRunWindow. So it can't
// open a session for anyone who existed before, or for an account that
// has been used; it is throttled with /login (failed passwords count
// there), refused from anything but this origin's own page, and audited
// (the API's audit line, and one of its own). Only this browser's copy of
// the owner token is replaced; the token stays the bootstrap credential —
// its browser sign-in (the link, its cookie) until an admin turns token
// sign-in off (Admin → Users → sign-in security), the token itself (bx's
// bearer) until it is rotated.

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

const firstRunDocs = "/docs/protocol/users.md#first-run"

// firstRunWindow: how long after it was made the bootstrap account can be
// swapped into. The sheet swaps at once; past this, it is signed in to as
// any account is.
const firstRunWindow = 15 * time.Minute

var firstRunNow = time.Now // tests move it

func (s *Server) registerFirstRunAPI() {
	s.RegisterAPI("GET /first-run", s.apiFirstRun)
	s.RegisterAPI("POST /first-run/session", s.apiFirstRunSession)
}

// tokenSession: p is the owner token's BROWSER session (the bootstrap
// cookie) — not bx's bearer, a tile's frame, a view-as session or the
// implicit owner of --no-auth.
func tokenSession(p auth.Principal) bool {
	return p.Owner && p.Via == "cookie" && p.Component == "" && p.Agent == "" && !p.ReadOnly()
}

// noAccounts: the workspace has no accounts at all (an invited one that
// was never redeemed is one).
func (s *Server) noAccounts() bool {
	return s.Auth != nil && !s.Auth.NoAuth() && s.Auth.Users != nil && len(s.Auth.Users.List()) == 0
}

// apiFirstRun — GET /first-run: whether this caller is in the bootstrap
// state. Anyone may ask; only the owner token's browser session in a
// workspace with no accounts hears true.
func (s *Server) apiFirstRun(w http.ResponseWriter, r *http.Request) {
	WriteJSON(w, http.StatusOK, map[string]any{"bootstrap": tokenSession(auth.PrincipalOf(r)) && s.noAccounts()})
}

// firstAccount is the account the swap may sign this browser in as — the
// workspace's only account, an enabled admin, never signed in, made in the
// last firstRunWindow — or why there is none.
func (s *Server) firstAccount(id string) (users.User, string) {
	all := s.Auth.Users.List()
	switch {
	case len(all) == 0:
		return users.User{}, "there is no account to sign in as yet: make it first (POST /users)"
	case len(all) > 1 || all[0].ID != strings.ToLower(strings.TrimSpace(id)):
		return users.User{}, "this workspace already has accounts: sign in with yours at /login"
	}
	u := all[0]
	switch {
	case !u.IsAdmin() || u.Disabled:
		return users.User{}, "the first account is an enabled admin: sign in at /login"
	case u.LastLogin != 0:
		return users.User{}, "this account has signed in before: sign in with it at /login"
	case firstRunNow().Sub(time.Unix(u.Created, 0)) > firstRunWindow:
		return users.User{}, "this account was made more than 15 minutes ago: sign in with it at /login"
	}
	return u, ""
}

// firstRunSameOrigin: the request is a fetch from this origin's own page.
// With Fetch Metadata the browser says so (an opaque-origin tile frame is
// cross-site, a sibling tile origin same-site); without it (an older
// Safari) the Origin it sends must name this host.
func firstRunSameOrigin(r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" {
		return site == "same-origin"
	}
	o := r.Header.Get("Origin")
	if o == "" || o == "null" {
		return false
	}
	u, err := url.Parse(o)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// apiFirstRunSession — POST /first-run/session {id, password}: the swap
// (file comment). 200 {user: {id, name, role}} with this browser's cookie
// now the account's session.
func (s *Server) apiFirstRunSession(w http.ResponseWriter, r *http.Request) {
	p, ip := auth.PrincipalOf(r), s.ClientIP(r)
	switch {
	case !tokenSession(p):
		WriteError(w, http.StatusForbidden, "only the owner token's browser session signs in to the first account this way; everyone else signs in at /login", firstRunDocs)
		return
	case !firstRunSameOrigin(r):
		WriteError(w, http.StatusForbidden, "a first-run sign-in comes from this workspace's own page", firstRunDocs)
		return
	case s.Auth.Users == nil || s.Auth.NoAuth():
		WriteError(w, http.StatusConflict, "this workspace has no accounts to sign in to", firstRunDocs)
		return
	case !s.loginThrottle.allow(ip):
		w.Header().Set("Retry-After", "30")
		WriteError(w, http.StatusTooManyRequests, "too many attempts, slow down", firstRunDocs)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if err := DecodeJSON(r, &body); err != nil || strings.TrimSpace(body.ID) == "" || body.Password == "" {
		WriteError(w, http.StatusBadRequest, "need {id, password}: the account the first-run sheet just made", firstRunDocs)
		return
	}
	u, why := s.firstAccount(body.ID)
	if why != "" {
		WriteError(w, http.StatusConflict, why, firstRunDocs)
		return
	}
	if _, ok := s.Auth.Users.Verify(u.ID, body.Password); !ok {
		s.loginThrottle.fail(ip)
		WriteError(w, http.StatusUnauthorized, "that isn't this account's password", firstRunDocs)
		return
	}
	s.loginThrottle.ok(ip)
	s.touchLogin(u.ID, "first-run")
	s.setSessionCookie(w, r, s.Auth.NewSession(u.ID, ip)) // replaces this browser's copy of the token; the token itself stays valid
	slog.Info("first run: this browser now signs in as the account it made (the owner token's browser sign-in stays until it is turned off, the token itself until it is rotated)", "user", u.ID, "ip", ip)
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"user": map[string]string{"id": u.ID, "name": u.Name, "role": u.Role}})
}
