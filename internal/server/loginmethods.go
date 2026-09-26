package server

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/users"
)

// The app's sign-in discovery and invite redemption (docs/auth.md §Device
// login, native/spec/device-login.md §8 and §9):
//
//	GET  /api/xbin/login/methods   which sign-in methods this workspace offers
//	POST /api/xbin/invite/check    an invite link's account, without spending it
//	POST /api/xbin/invite/redeem   set the password → the app token response
//
// All three need no principal (RegisterPublicAPI). The methods answer says
// nothing the login page doesn't already show (the title, the SSO button
// and its label, the SSO-only note), so it is cheap and not throttled; the
// invite routes are the form at GET /login?invite= + POST /login/invite in
// JSON, under the login throttle.

// registerLoginMethods mounts the routes above (from registerDeviceLogin).
func (s *Server) registerLoginMethods() {
	s.RegisterPublicAPI("GET /login/methods", s.apiLoginMethods)
	s.RegisterPublicAPI("POST /invite/check", s.apiInviteCheck)
	s.RegisterPublicAPI("POST /invite/redeem", s.apiInviteRedeem)
}

// loginMethods is GET /login/methods' answer. api versions the shape
// (additive fields don't bump it).
type loginMethods struct {
	API      int       `json:"api"`
	Title    string    `json:"title"`
	Auth     bool      `json:"auth"`
	Password pwMethod  `json:"password"`
	SSO      ssoMethod `json:"sso"`
	Invites  bool      `json:"invites"`
}

type pwMethod struct {
	Enabled   bool `json:"enabled"`
	AdminOnly bool `json:"adminOnly"`
}

type ssoMethod struct {
	Enabled bool   `json:"enabled"`
	Label   string `json:"label"`
}

// brandTitle is the workspace's title as the sign-in pages show it: the
// branding title, else "xbin".
func (s *Server) brandTitle() string {
	if t := s.brand().Title; t != "" {
		return t
	}
	return "xbin"
}

// apiLoginMethods — GET /api/xbin/login/methods: what the login page offers,
// as data, so the app shows the password form and/or the SSO button the way
// the page does. password.enabled: a password can sign someone in (the
// workspace has accounts); adminOnly: SSO-only mode (D53) — the form stays
// for admins. sso.enabled: the page's SSO button is shown (configured, and
// --external-url set). invites: invite links can be redeemed here.
func (s *Server) apiLoginMethods(w http.ResponseWriter, r *http.Request) {
	m := loginMethods{API: 1, Title: s.brandTitle(), Auth: !s.Auth.NoAuth()}
	if m.Auth && s.Auth.Users != nil {
		hasUsers := s.Auth.Users.Count() > 0
		m.Password = pwMethod{Enabled: hasUsers, AdminOnly: hasUsers && s.Auth.Users.PasswordLoginDisabled()}
		if s.SSOReady() {
			m.SSO = ssoMethod{Enabled: true, Label: SSOButtonLabel(s.ssoConfig())}
		}
		m.Invites = hasUsers
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, m)
}

// inviteRefused is the one answer for an unknown, used or expired invite
// (and a disabled account's) — counted against the login throttle.
func (s *Server) inviteRefused(w http.ResponseWriter, ip string) {
	s.loginRefused(w, ip, http.StatusForbidden, users.ErrInvalidInvite.Error())
}

// inviteUser answers the invite routes' common part: the throttle, the
// body, the no-auth refusal, then the token — looked up, not spent.
// ok=false: already answered.
func (s *Server) inviteUser(w http.ResponseWriter, r *http.Request, body *inviteBody) (u *users.User, ip string, ok bool) {
	ip = s.ClientIP(r)
	if s.throttled(w, ip) || !decodeAppBody(w, r, body) {
		return nil, ip, false
	}
	if s.Auth.NoAuth() {
		WriteError(w, http.StatusForbidden, "this workspace runs without sign-in — open it directly", "/docs/auth.md")
		return nil, ip, false
	}
	if s.Auth.Users != nil && body.Invite != "" {
		u, ok = s.Auth.Users.InviteUser(body.Invite)
	}
	if !ok {
		s.inviteRefused(w, ip)
	}
	return u, ip, ok
}

type inviteBody struct {
	Invite   string `json:"invite"`
	Password string `json:"password"`
}

// apiInviteCheck — POST /api/xbin/invite/check {invite} → {user:{id,name},
// expires, title}: whom an invite link is for, so the app can say "you're
// invited to <title> as <name>" before asking for a password. Doesn't spend
// the invite.
func (s *Server) apiInviteCheck(w http.ResponseWriter, r *http.Request) {
	var body inviteBody
	u, _, ok := s.inviteUser(w, r, &body)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"user":    map[string]string{"id": u.ID, "name": firstNonEmptyStr(u.Name, u.ID)},
		"expires": u.InviteExpires,
		"title":   s.brandTitle(),
	})
}

// apiInviteRedeem — POST /api/xbin/invite/redeem {invite, password}: the
// invite form (POST /login/invite) for the app — sets the invitee's first
// password, spends the invite, and answers the token response of POST
// /api/xbin/login instead of a cookie. A password the policy refuses is a
// 400 and leaves the invite unspent.
func (s *Server) apiInviteRedeem(w http.ResponseWriter, r *http.Request) {
	var body inviteBody
	if _, ip, ok := s.inviteUser(w, r, &body); ok {
		s.redeemInvite(w, ip, body.Invite, body.Password)
	}
}

func (s *Server) redeemInvite(w http.ResponseWriter, ip, tok, password string) {
	if err := users.CheckNewPassword(password); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), "/docs/auth.md")
		return
	}
	u, err := s.Auth.Users.RedeemInvite(tok, password)
	switch {
	case errors.Is(err, users.ErrInvalidInvite): // spent by someone else meanwhile
		s.inviteRefused(w, ip)
		return
	case err != nil:
		WriteError(w, http.StatusInternalServerError, "could not set the password: "+err.Error(), "/docs/auth.md")
		return
	}
	s.loginThrottle.ok(ip)
	s.touchLogin(u.ID, "invite")
	slog.Info("audit", "who", "user:"+u.ID, "method", "POST", "path", "/invite/redeem", "status", 200, "login", "invite", "ip", ip)
	s.usersEvent() // open admin consoles: the invite is no longer pending
	s.writeAppSession(w, u, "", ip, time.Time{})
}
