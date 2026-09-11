package server

import (
	"log/slog"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
)

// "View as user" (docs/auth.md §Viewing the workspace as a user, D64): a
// workspace admin swaps their browser session for a read-only one that
// authenticates as another user, to see exactly what that user sees. Three
// legs — mint a one-shot ticket (POST /api/xbin/impersonate, from the admin
// console's frame token), redeem it top-level (GET /login?impersonate=,
// which sets the cookie), stop (POST /api/xbin/impersonate/stop, or
// /logout) — plus the read-only gate in the authed middleware.

// readOnlyMsg is the refusal every write gets while viewing as a user.
const readOnlyMsg = "read-only: you are viewing the workspace as another user — exit the view (top banner) to make changes"

// readOnlyAllowed lists what an impersonation session may still call:
// reads, and the way out.
func readOnlyAllowed(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return true
	}
	return r.Method == http.MethodPost && r.URL.Path == "/api/xbin/impersonate/stop"
}

// refuseReadOnly answers a write from an impersonation session: the API's
// JSON error shape on API paths, plain text elsewhere.
func refuseReadOnly(w http.ResponseWriter, r *http.Request) {
	if len(r.URL.Path) >= 5 && r.URL.Path[:5] == "/api/" {
		WriteError(w, http.StatusForbidden, readOnlyMsg, "/docs/auth.md")
		return
	}
	http.Error(w, readOnlyMsg, http.StatusForbidden)
}

// apiImpersonate mints a view-as ticket: {user} → {url, user, expiresIn}.
// Admin only (a signed-in human; the admin tile calls with its frame token,
// which carries the driving admin). The URL must be opened TOP-LEVEL in the
// same browser — it is bound to the minting admin's own session.
func (s *Server) apiImpersonate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		User string `json:"user"`
	}
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.User == "" {
		WriteError(w, http.StatusBadRequest, "user is required")
		return
	}
	tk, err := s.Auth.NewImpersonationTicket(auth.PrincipalOf(r), body.User)
	if err != nil {
		WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"url": "/login?impersonate=" + tk, "user": body.User, "expiresIn": 120,
	})
}

// handleImpersonateRedeem is GET /login?impersonate=<ticket>: swaps the
// cookie for the read-only view. The browser must be signed in as the admin
// who minted the ticket (the ticket is consumed either way).
func (s *Server) handleImpersonateRedeem(w http.ResponseWriter, r *http.Request, ticket string) {
	by, ok := s.Auth.FromRequest(r)
	if !ok {
		http.Error(w, "sign in as the admin who minted this view-as link, then open it again", http.StatusForbidden)
		return
	}
	prev := ""
	if c, err := r.Cookie(auth.CookieName); err == nil {
		prev = c.Value
	}
	sid, err := s.Auth.RedeemImpersonation(ticket, by, prev, s.ClientIP(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	setSessionCookie(w, r, sid)
	slog.Info("audit", "who", by.From(), "method", "GET", "path", "/login?impersonate=", "status", http.StatusFound)
	http.Redirect(w, r, "/", http.StatusFound)
}

// apiImpersonateStop ends the view and hands the browser back to the admin:
// {ok, restored} — restored:false means their own session had expired
// meanwhile (the cookie is cleared; sign in again).
func (s *Server) apiImpersonateStop(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie(auth.CookieName)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "not viewing as a user (no session cookie)")
		return
	}
	restore, owner, ok := s.Auth.StopImpersonation(c.Value)
	if !ok {
		WriteError(w, http.StatusBadRequest, "not viewing as a user")
		return
	}
	restored := s.restoreAdminCookie(w, r, restore, owner)
	WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "restored": restored})
}

// restoreAdminCookie puts the admin's own credential back after a view
// ends: their live session id, or the bootstrap owner cookie when they came
// in on it (and token login is still allowed). Neither → the cookie is
// cleared and the next page load asks them to sign in.
func (s *Server) restoreAdminCookie(w http.ResponseWriter, r *http.Request, restore string, owner bool) bool {
	switch {
	case restore != "":
		setSessionCookie(w, r, restore)
		return true
	case owner && !s.Auth.TokenLoginDisabled():
		setSessionCookie(w, r, s.Auth.OwnerTokenValue())
		return true
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	return false
}
