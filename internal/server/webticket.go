package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Signed-in Safari (docs/auth.md §Device login, native/spec/device-login.md
// §6): the xbin app opens the workspace in a browser already signed in.
//
//	POST /api/xbin/web-ticket {next}   the app's DEVICE session → {url, expires, expiresIn}
//	GET  /login?ticket=<t>&next=<path> top level, in the browser → a cookie session, 302 next
//
// The ticket is one-shot, lives WebTicketTTL (60 s) and is bound to the
// device session that minted it (internal/auth/webticket.go). The redeem is
// the D64 view-as pattern — a URL minted by a credential the browser can't
// hold, opened top-level — with login-CSRF defences of its own: it must be
// a navigation nobody else started (Fetch Metadata `Sec-Fetch-Site: none`,
// or no metadata at all), and it never switches a browser that is signed in
// as somebody else.

// maxNextLen bounds the landing path a ticket carries.
const maxNextLen = 2048

// safeNext validates a landing path: "" is "/"; otherwise a same-origin
// absolute path — printable ASCII only (percent-encode the rest), starting
// with exactly one "/", no backslash, and no scheme or host however a
// browser might read it (browsers drop tabs and newlines and treat "\" as
// "/", so "/\t/evil" and "/\evil" would leave the origin), and not the
// sign-in routes. false: refuse.
func safeNext(raw string) (string, bool) {
	if raw == "" {
		return "/", true
	}
	if len(raw) > maxNextLen || raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return "", false
	}
	for i := 0; i < len(raw); i++ {
		if c := raw[i]; c <= 0x20 || c >= 0x7f || c == '\\' {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Opaque != "" {
		return "", false
	}
	// the decoded path too: "/%2F/evil" is harmless to a browser, but a
	// landing page that re-uses its path as a link must not become one
	if p := u.Path; strings.HasPrefix(p, "//") || strings.ContainsAny(p, "\\") {
		return "", false
	}
	// nor the sign-in routes themselves: a landing page is a page, and a
	// ticket must not chain into another redeem (or a sign-out)
	if p := u.Path; p == "/login" || p == "/logout" || strings.HasPrefix(p, "/login/") {
		return "", false
	}
	return raw, true
}

// apiWebTicket — POST /api/xbin/web-ticket [{next}]: the app's device-key
// session mints a one-shot browser sign-in URL for its user. Anything else
// — a browser cookie, the app's password/SSO session before a device is
// enrolled, a tile, a terminal, the owner token — gets 403: the handoff
// trades a Face ID sign-in for a browser one, nothing weaker.
func (s *Server) apiWebTicket(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Via != "device" || p.DeviceID == "" || p.Component != "" || p.User == nil || p.ReadOnly() || s.Auth.Users == nil {
		WriteError(w, http.StatusForbidden, "only the xbin app's device sign-in can open the workspace in a browser — sign in there (a browser session, a tile or a token can't mint this)", "/docs/auth.md")
		return
	}
	var body struct {
		Next string `json:"next"`
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxAppLoginBody+1))
	if err != nil || len(raw) > maxAppLoginBody {
		WriteError(w, http.StatusBadRequest, "body too large", "/docs/auth.md")
		return
	}
	if len(strings.TrimSpace(string(raw))) > 0 && json.Unmarshal(raw, &body) != nil {
		WriteError(w, http.StatusBadRequest, "bad JSON body: want {next?: \"/path\"}", "/docs/auth.md")
		return
	}
	next, ok := safeNext(body.Next)
	if !ok {
		WriteError(w, http.StatusBadRequest, "next must be a path on this workspace (\"/c/apps/x/\"): one leading slash, printable ASCII (percent-encode the rest), no backslashes, at most 2048 characters", "/docs/auth.md")
		return
	}
	uid, d, found := s.Auth.Users.FindDevice(p.DeviceID)
	if !found || uid != p.UserID {
		WriteError(w, http.StatusForbidden, "this device was removed — enroll it again", "/docs/auth.md")
		return
	}
	if _, ok := s.deviceSSOBound(p.User); !ok {
		w.Header().Set("Cache-Control", "no-store")
		WriteJSON(w, http.StatusForbidden, map[string]string{"reauth": "sso", "docs": "/docs/auth.md",
			"error": "this workspace signs in through single sign-on, and yours is too old — sign in with SSO again"})
		return
	}
	tk, exp, err := s.Auth.MintWebTicket(p, next)
	if err != nil {
		var rate *auth.WebTicketRateError
		switch {
		case errors.As(err, &rate):
			w.Header().Set("Retry-After", strconv.Itoa(int(rate.RetryAfter/time.Second)+1))
			WriteError(w, http.StatusTooManyRequests, err.Error(), "/docs/auth.md")
		case errors.Is(err, auth.ErrWebTicketNotDevice):
			WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		default: // every pending slot taken
			WriteError(w, http.StatusServiceUnavailable, err.Error(), "/docs/auth.md")
		}
		return
	}
	origin := d.Origin // the address this device talks to and signs
	if origin == "" {
		origin = s.EnrollOrigin(r)
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{
		"url":       origin + "/login?ticket=" + url.QueryEscape(tk) + "&next=" + url.QueryEscape(next),
		"expires":   exp.Unix(),
		"expiresIn": int(auth.WebTicketTTL / time.Second),
	})
}

// handleWebTicketRedeem is GET /login?ticket=<t>[&next=<path>]: the
// browser half. The ticket is spent first, whatever happens next.
func (s *Server) handleWebTicketRedeem(w http.ResponseWriter, r *http.Request, ticket string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method != http.MethodGet { // a HEAD (a link checker, a preview) must not spend it
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "open this link in a browser", http.StatusMethodNotAllowed)
		return
	}
	ip := s.ClientIP(r)
	if !s.loginThrottle.allow(ip) {
		http.Error(w, "too many attempts, slow down", http.StatusTooManyRequests)
		return
	}
	t, ok := s.Auth.ConsumeWebTicket(ticket)
	refuse := func(why string) {
		slog.Warn("web ticket refused", "why", why, "ip", ip, "user", t.UserID, "device", t.DeviceID)
		http.Error(w, why, http.StatusForbidden)
	}
	if !ok || s.Auth.Users == nil {
		s.loginThrottle.fail(ip)
		refuse("this sign-in link is invalid, expired or already used — open the workspace from the xbin app again, or sign in here")
		return
	}
	// Login CSRF: someone else's ticket, planted by a link, a redirect or a
	// form from any site (this one included), would sign the victim's
	// browser into their account. The app opens the URL itself — a
	// navigation no page started.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" {
		refuse("this sign-in link only works opened by the xbin app, not followed from a page")
		return
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" && mode != "navigate" {
		refuse("this sign-in link only works as a page the xbin app opens")
		return
	}
	if q := r.URL.Query().Get("next"); q != "" && q != t.Next {
		refuse("this sign-in link was altered — open the workspace from the xbin app again")
		return
	}
	u, found := s.Auth.Users.Get(t.UserID)
	if !found || u.Disabled {
		refuse("this account is disabled")
		return
	}
	if uid, _, found := s.Auth.Users.FindDevice(t.DeviceID); !found || uid != t.UserID {
		refuse("the device that opened this link was removed")
		return
	}
	notAfter, ok := s.deviceSSOBound(u)
	if !ok {
		refuse("this workspace signs in through single sign-on, and yours is too old — sign in with SSO again")
		return
	}
	// Never switch a browser signed in as somebody else (the owner token and
	// view-as sessions included); the same person is simply let through.
	if p, ok := s.Auth.FromRequest(r); ok {
		if p.Owner || p.Component != "" || p.ReadOnly() || p.UserID != t.UserID {
			refuse("this browser is signed in as someone else — sign out here first, then open the workspace from the app again")
			return
		}
		http.Redirect(w, r, t.Next, http.StatusFound)
		return
	}
	sid, err := s.Auth.OpenWebSession(t, ip, notAfter)
	if err != nil {
		refuse(err.Error())
		return
	}
	s.setSessionCookie(w, r, sid)
	slog.Info("audit", "who", "user:"+t.UserID, "method", "GET", "path", "/login?ticket=", "status", http.StatusFound,
		"device", t.DeviceID, "ip", ip)
	http.Redirect(w, r, t.Next, http.StatusFound)
}
