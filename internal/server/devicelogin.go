package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/users"
)

// Device login and the app's sign-in (plans/native.md §5, docs/auth.md
// §Device login, native/spec/device-login.md — the app's contract):
//
//	POST /api/xbin/devices/enroll-code   a signed-in human → one-time code + xbin://enroll link
//	POST /api/xbin/devices/enroll        the app, code in hand → registers its public key
//	POST /login/device/challenge         → a single-use nonce for one device
//	POST /login/device                   the signed nonce → a bearer session
//	POST /api/xbin/login                 the app's password sign-in → a bearer session
//	GET  /login/sso?app=1&challenge=…    the app's SSO sign-in → xbin://sso?ticket=…
//	POST /login/ticket                   ticket + PKCE verifier → a bearer session
//	POST /api/xbin/web-ticket            the app's device session → a one-shot browser sign-in URL (webticket.go)
//
// Every session minted here is the same human session a browser login gets
// (same TTLs, same principal), carried as Authorization: Bearer; it is used
// only by the app's own code — never by tile code (plans/native.md §2).
// Listing and revoking devices is the broker's (devicesapi.go): it needs
// the xbin:users capability for the admin side.

// maxAppLoginBody bounds the JSON bodies of the unauthenticated routes.
const maxAppLoginBody = 16 << 10

// registerDeviceLogin mounts the routes above (from Handler).
func (s *Server) registerDeviceLogin(handleFunc func(string, http.HandlerFunc)) {
	handleFunc("POST /login/device/challenge", s.handleDeviceChallenge)
	handleFunc("POST /login/device", s.handleDeviceLogin)
	handleFunc("POST /login/ticket", s.handleTicketRedeem)
	s.RegisterPublicAPI("POST /login", s.apiAppLogin)
	s.RegisterPublicAPI("POST /devices/enroll", s.apiDeviceEnroll)
	s.RegisterAPI("POST /devices/enroll-code", s.apiEnrollCode)
	s.RegisterAPI("POST /web-ticket", s.apiWebTicket) // signed-in Safari (webticket.go)
}

// RegisterPublicAPI mounts an /api/xbin route that needs NO principal: its
// body is its own credential (an enrollment code, a password). It is part of
// the route inventory like any RegisterAPI route; the /api/ gate lets exactly
// these method+path pairs through unauthenticated (authedAPI). Exact paths
// only — no wildcards.
func (s *Server) RegisterPublicAPI(pattern string, h http.HandlerFunc) {
	s.RegisterAPI(pattern, h)
	if s.publicAPI == nil {
		s.publicAPI = map[string]bool{}
	}
	s.publicAPI[pattern] = true
}

// authedAPI is authed for /api/, except the RegisterPublicAPI routes, which
// run with an empty principal.
func (s *Server) authedAPI(next http.Handler) http.Handler {
	gated := s.authed(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rest, ok := strings.CutPrefix(r.URL.Path, "/api/xbin/"); ok && s.publicAPI[r.Method+" /"+rest] {
			next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{})))
			return
		}
		gated.ServeHTTP(w, r)
	})
}

// decodeAppBody decodes a JSON body leniently (a newer app may send fields
// this xbind doesn't know) and bounded.
func decodeAppBody(w http.ResponseWriter, r *http.Request, v any) bool {
	defer r.Body.Close()
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAppLoginBody)).Decode(v); err != nil {
		WriteError(w, http.StatusBadRequest, "bad JSON body", "/docs/auth.md")
		return false
	}
	return true
}

// appSession is the token response of every app sign-in route.
type appSession struct {
	Token       string  `json:"token"`
	TokenType   string  `json:"tokenType"` // "Bearer"
	User        appUser `json:"user"`
	DeviceID    string  `json:"deviceId,omitempty"`
	ExpiresIdle int64   `json:"expiresIdle"` // unix; slides with use
	ExpiresMax  int64   `json:"expiresMax"`  // unix; hard cap
}

type appUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Role string `json:"role"`
}

func (s *Server) writeAppSession(w http.ResponseWriter, u *users.User, deviceID, ip string, notAfter time.Time) {
	bs := s.Auth.NewBearerSessionUntil(u.ID, deviceID, ip, notAfter)
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, appSession{
		Token: bs.Token, TokenType: "Bearer", DeviceID: deviceID,
		User:        appUser{ID: u.ID, Name: firstNonEmptyStr(u.Name, u.ID), Role: u.Role},
		ExpiresIdle: bs.ExpiresIdle.Unix(), ExpiresMax: bs.ExpiresMax.Unix(),
	})
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// throttled answers 429 when ip is in the login throttle's cooldown (shared
// with password and SSO sign-in).
func (s *Server) throttled(w http.ResponseWriter, ip string) bool {
	if s.loginThrottle.allow(ip) {
		return false
	}
	WriteError(w, http.StatusTooManyRequests, "too many attempts, slow down", "/docs/auth.md")
	return true
}

// loginRefused counts a failed attempt against ip and answers code.
func (s *Server) loginRefused(w http.ResponseWriter, ip string, code int, msg string) {
	s.loginThrottle.fail(ip)
	WriteError(w, code, msg, "/docs/auth.md")
}

func (s *Server) usersEvent() {
	if s.Hub != nil {
		s.Hub.Publish(events.Event{Type: "users"}) // open device lists / admin consoles refresh
	}
}

// EnrollOrigin is the server origin a device enrolled from this request
// signs into its logins: the --external-url origin when configured (the
// operator-vouched public address), else scheme://host of the request —
// which, for an enrollment code, is the signed-in human's own browser (or
// app) talking to this server.
func (s *Server) EnrollOrigin(r *http.Request) string {
	if s.ExternalURL != "" {
		if o, err := auth.NormalizeOrigin(s.ExternalURL); err == nil {
			return o
		}
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	if o, err := auth.NormalizeOrigin(scheme + "://" + r.Host); err == nil {
		return o
	}
	return scheme + "://" + strings.ToLower(r.Host)
}

// apiEnrollCode — POST /api/xbin/devices/enroll-code [{password}]: a
// signed-in human (browser session, or the app's own session) mints a
// one-time code that enrolls one device for THEM. The shell renders url as
// a QR code. A device outlives the session that enrolled it, so this is a
// step-up: the login must be fresh (auth.EnrollFreshLogin), or the body
// carries the account password — a stolen session alone mints nothing.
func (s *Server) apiEnrollCode(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Component != "" || p.User == nil || s.Auth.Users == nil || p.ReadOnly() {
		WriteError(w, http.StatusForbidden, "devices belong to user accounts — sign in with yours (the bootstrap token has none, and tiles can't enroll devices)", "/docs/auth.md")
		return
	}
	if !s.enrollStepUp(w, r, p) {
		return
	}
	origin := s.EnrollOrigin(r)
	code, exp, err := s.Auth.MintEnrollCode(p.User.ID, origin)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, err.Error(), "/docs/auth.md")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"code": code, "origin": origin, "expires": exp.Unix(),
		"url": "xbin://enroll?u=" + url.QueryEscape(origin) + "&c=" + code,
	})
}

// enrollStepUp passes a fresh login, or a correct password in the body;
// otherwise it answers 403 {error, stepUp}: "password" — resend with
// {"password"}; "signin" — sign in again (an SSO account, or password
// sign-in disabled for it) and retry within the window. Wrong passwords
// count against the login throttle.
func (s *Server) enrollStepUp(w http.ResponseWriter, r *http.Request, p auth.Principal) bool {
	if at, ok := s.Auth.LoginTime(p); ok && time.Since(at) <= auth.EnrollFreshLogin {
		return true
	}
	var body struct{ Password string }
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, maxAppLoginBody)); len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	pwOK := p.User.PassHash != "" && (!s.Auth.Users.PasswordLoginDisabled() || p.User.IsAdmin())
	mode := "signin"
	if pwOK {
		mode = "password"
	}
	refuse := func(msg string) bool {
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "stepUp": mode, "docs": "/docs/auth.md"})
		return false
	}
	if !pwOK {
		return refuse(fmt.Sprintf("adding a device needs a recent sign-in — sign in again, then add it within %d minutes", int(auth.EnrollFreshLogin/time.Minute)))
	}
	if body.Password == "" {
		return refuse("confirm your password to add a device")
	}
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return false
	}
	if _, ok := s.Auth.Users.Verify(p.User.ID, body.Password); !ok {
		s.loginThrottle.fail(ip)
		return refuse("wrong password")
	}
	s.loginThrottle.ok(ip)
	return true
}

// apiDeviceEnroll — POST /api/xbin/devices/enroll (no principal: the code is
// the credential): {code, name, platform, publicKey} → {deviceId, user,
// origin, name}. The key is validated before the code is spent.
func (s *Server) apiDeviceEnroll(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return
	}
	var body struct{ Code, Name, Platform, PublicKey string }
	if !decodeAppBody(w, r, &body) {
		return
	}
	if body.Code == "" || body.PublicKey == "" {
		WriteError(w, http.StatusBadRequest, "need {code, name, platform, publicKey}", "/docs/auth.md")
		return
	}
	_, key, err := auth.ParseDeviceKey(body.PublicKey)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), "/docs/auth.md")
		return
	}
	uid, origin, ok := s.Auth.RedeemEnrollCode(body.Code)
	if !ok || s.Auth.Users == nil {
		s.loginRefused(w, ip, http.StatusUnauthorized, "invalid, expired or already used enrollment code — mint a new one (account menu → devices)")
		return
	}
	d, err := s.Auth.Users.AddDevice(uid, users.Device{Name: body.Name, Platform: body.Platform, PublicKey: key, Origin: origin})
	if err != nil {
		WriteError(w, http.StatusConflict, err.Error(), "/docs/auth.md")
		return
	}
	s.loginThrottle.ok(ip)
	slog.Info("audit", "who", "user:"+uid, "method", "POST", "path", "/devices/enroll", "status", 200,
		"device", d.ID, "name", d.Name, "platform", d.Platform, "ip", ip)
	s.usersEvent()
	WriteJSON(w, http.StatusOK, map[string]string{"deviceId": d.ID, "user": uid, "origin": d.Origin, "name": d.Name})
}

// handleDeviceChallenge — POST /login/device/challenge {deviceId} →
// {nonce, expires}. 404 when the device is not enrolled (revoked: the app
// offers to enroll again).
func (s *Server) handleDeviceChallenge(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return
	}
	var body struct {
		DeviceID string `json:"deviceId"`
	}
	if !decodeAppBody(w, r, &body) {
		return
	}
	if s.Auth.Users == nil {
		s.loginRefused(w, ip, http.StatusNotFound, "unknown device")
		return
	}
	if _, _, ok := s.Auth.Users.FindDevice(body.DeviceID); !ok {
		s.loginRefused(w, ip, http.StatusNotFound, "unknown device — it was removed, or never enrolled here; enroll it again")
		return
	}
	nonce, exp, err := s.Auth.NewDeviceChallenge(body.DeviceID)
	if err != nil {
		WriteError(w, http.StatusServiceUnavailable, err.Error(), "/docs/auth.md")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"nonce": nonce, "expires": exp.Unix()})
}

// handleDeviceLogin — POST /login/device {deviceId, nonce, signature} →
// the token response. The nonce is spent first, whatever happens next.
func (s *Server) handleDeviceLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return
	}
	var body struct{ DeviceID, Nonce, Signature string }
	if !decodeAppBody(w, r, &body) {
		return
	}
	if !s.Auth.ConsumeDeviceChallenge(body.DeviceID, body.Nonce) {
		s.loginRefused(w, ip, http.StatusUnauthorized, "challenge expired, already used, or not for this device — ask for a new one")
		return
	}
	if s.Auth.Users == nil {
		s.loginRefused(w, ip, http.StatusUnauthorized, "device login failed")
		return
	}
	uid, d, ok := s.Auth.Users.FindDevice(body.DeviceID)
	if !ok || !auth.VerifyDeviceSignature(d.PublicKey, d.Origin, d.ID, body.Nonce, body.Signature) {
		slog.Warn("device login refused", "device", body.DeviceID, "ip", ip, "known", ok)
		s.loginRefused(w, ip, http.StatusUnauthorized, "device login failed")
		return
	}
	u, ok := s.Auth.Users.Get(uid)
	if !ok || u.Disabled {
		WriteError(w, http.StatusForbidden, "this account is disabled", "/docs/auth.md")
		return
	}
	s.loginThrottle.ok(ip)
	notAfter, ok := s.deviceSSOBound(u)
	if !ok {
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusForbidden, map[string]string{"reauth": "sso", "docs": "/docs/auth.md",
			"error": "this workspace signs in through single sign-on, and yours is too old for this device — sign in with SSO again (the device stays enrolled)"})
		return
	}
	if err := s.Auth.Users.TouchDevice(d.ID, ip); err != nil {
		slog.Warn("device login: last-used stamp failed", "device", d.ID, "err", err)
	}
	s.touchLogin(u.ID, "device")
	slog.Info("audit", "who", "user:"+u.ID, "method", "POST", "path", "/login/device", "status", 200, "device", d.ID, "ip", ip)
	s.writeAppSession(w, u, d.ID, ip, notAfter)
}

// deviceSSOBound: where the IdP is the account's only way in, it stays the
// authority — a device key is a way back in, not a replacement. That is
// every account without a usable password while SSO is configured: in
// SSO-only mode (D53) every non-admin, and in any mode an account with no
// password at all (provisioned by SSO, or an admin granted by an SSO
// group). A device login then needs the user's last SSO sign-in (web or
// app) within the session max TTL, and the session it opens ends when that
// window does, so removing someone at the IdP still bounds their access by
// the session TTL, as before devices. An account that can still sign in
// with its password (outside SSO-only mode, or an admin in it) is not
// bound, nor is anyone once SSO is removed: zero time, true.
func (s *Server) deviceSSOBound(u *users.User) (time.Time, bool) {
	st := s.Auth.Users
	if sso := st.SSO(); sso == nil || !sso.Enabled() {
		return time.Time{}, true
	}
	if u.PassHash != "" && (!st.PasswordLoginDisabled() || u.IsAdmin()) {
		return time.Time{}, true
	}
	until := time.Unix(u.LastSSO, 0).Add(s.Auth.SessionMaxTTL())
	if u.LastSSO == 0 || !time.Now().Before(until) {
		return time.Time{}, false
	}
	return until, true
}

// apiAppLogin — POST /api/xbin/login {username, password} (no principal):
// the app's password sign-in, same rules as the form at POST /login (the
// throttle, disabled accounts, SSO-only mode) → the token response.
func (s *Server) apiAppLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return
	}
	var body struct{ Username, Password string }
	if !decodeAppBody(w, r, &body) {
		return
	}
	if s.Auth.Users == nil || body.Username == "" {
		s.loginRefused(w, ip, http.StatusUnauthorized, "invalid credentials")
		return
	}
	u, ok := s.Auth.Users.Verify(body.Username, body.Password)
	if !ok {
		s.loginRefused(w, ip, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if s.Auth.Users.PasswordLoginDisabled() && !u.IsAdmin() {
		WriteError(w, http.StatusForbidden, passwordLoginRefused, "/docs/auth.md")
		return
	}
	s.loginThrottle.ok(ip)
	s.touchLogin(u.ID, "password")
	s.writeAppSession(w, u, "", ip, time.Time{})
}

// handleTicketRedeem — POST /login/ticket {ticket, verifier}: the second
// half of the app's SSO sign-in → the token response.
func (s *Server) handleTicketRedeem(w http.ResponseWriter, r *http.Request) {
	ip := s.ClientIP(r)
	if s.throttled(w, ip) {
		return
	}
	var body struct{ Ticket, Verifier string }
	if !decodeAppBody(w, r, &body) {
		return
	}
	uid, ok := s.Auth.RedeemAppTicket(body.Ticket, body.Verifier)
	if !ok || s.Auth.Users == nil {
		s.loginRefused(w, ip, http.StatusUnauthorized, "invalid, expired or already used sign-in ticket")
		return
	}
	u, ok := s.Auth.Users.Get(uid)
	if !ok || u.Disabled {
		WriteError(w, http.StatusForbidden, "this account is disabled", "/docs/auth.md")
		return
	}
	s.loginThrottle.ok(ip)
	s.writeAppSession(w, u, "", ip, time.Time{})
}

// --- the app's SSO leg (hooks in sso.go) ---

// appSSORedirect is where an app-started SSO sign-in lands: the custom
// scheme ASWebAuthenticationSession watches for.
const appSSORedirect = "xbin://sso"

// ssoAppStart reads ?app=1&challenge=<S256> on GET /login/sso into the
// login state. false (400 written): an app flow without a valid challenge.
func ssoAppStart(w http.ResponseWriter, r *http.Request, st *ssoState) bool {
	if r.URL.Query().Get("app") != "1" {
		return true
	}
	c := r.URL.Query().Get("challenge")
	if !auth.ValidPKCEChallenge(c) {
		http.Error(w, "app sign-in needs challenge=<base64url(sha256(verifier))>", http.StatusBadRequest)
		return false
	}
	st.App, st.Challenge = true, c
	return true
}

// ssoFailURL is where a failed callback sends the browser: the login page,
// or — for an app flow — back to the app with the same fixed error code.
func ssoFailURL(app bool, code string) string {
	if app {
		return appSSORedirect + "?error=" + url.QueryEscape(code)
	}
	return "/login?sso_err=" + code
}

// ssoAppFinish ends an app-started SSO sign-in: instead of a cookie, a
// one-shot ticket bound to the app's PKCE challenge, handed over by
// redirecting to xbin://sso?ticket=….
func (s *Server) ssoAppFinish(w http.ResponseWriter, r *http.Request, userID, challenge string) {
	t, err := s.Auth.MintAppTicket(userID, challenge)
	if err != nil {
		http.Redirect(w, r, ssoFailURL(true, "failed"), http.StatusFound)
		return
	}
	slog.Info("audit", "who", "user:"+userID, "method", "SSO", "path", "/login/sso/callback", "status", "app-ticket")
	http.Redirect(w, r, appSSORedirect+"?ticket="+t, http.StatusFound)
}

// bearerLogout: POST /logout with an app session's bearer ends that session
// → 204. A device-key session signing out also ends its device's
// per-device state (OnDeviceSignedOut: the push registration) — the device
// stays enrolled and registers again at its next sign-in. false: not an app
// session (the cookie logout runs).
func (s *Server) bearerLogout(w http.ResponseWriter, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	uid, dev, ok := s.Auth.DropBearerSession(strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")))
	if !ok {
		return false
	}
	if dev != "" && s.OnDeviceSignedOut != nil {
		s.OnDeviceSignedOut(uid, dev)
	}
	w.WriteHeader(http.StatusNoContent)
	return true
}
