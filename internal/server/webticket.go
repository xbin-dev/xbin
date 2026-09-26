package server

import (
	"crypto/subtle"
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
	"github.com/xbin-dev/xbin/internal/users"
)

// Signed-in Safari (docs/auth.md §Device login, native/spec/device-login.md
// §6): the xbin app opens the workspace in a browser already signed in.
//
//	POST /api/xbin/web-ticket {next}   the app's DEVICE session → {url, expires, expiresIn}
//	GET  /login?ticket=<t>&next=<path> top level, in the browser → "Continue as <name>"
//	POST /login/web-ticket {confirm}   that page's button → a cookie session, 303 next
//
// The ticket is one-shot, lives WebTicketTTL (60 s) and is bound to the
// device session that minted it (internal/auth/webticket.go). The redeem is
// the D64 view-as pattern — a URL minted by a credential the browser can't
// hold, opened top-level — with login-CSRF defences of its own. Anyone can
// mint a ticket for THEIR account and hand the link over (a chat message, a
// QR code, a redirector): a link another app opens is a navigation "nobody
// started" (Sec-Fetch-Site: none) exactly like the xbin app's own open, so
// Fetch Metadata can't tell them apart. So a GET never signs a browser in:
// a browser signed in as the same person just lands on next, one signed in
// as somebody else is refused, and a signed-out one gets a page naming the
// account, whose Continue posts a one-shot nonce back — bound to this
// browser by a cookie only that page's response set, and accepted only from
// this origin's own page (Sec-Fetch-Site: same-origin, or a matching Origin;
// neither: refused). The person sees whose account they are entering.

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
// browser half. The ticket is spent first, whatever happens next; a GET
// never opens a session (the package comment above).
func (s *Server) handleWebTicketRedeem(w http.ResponseWriter, r *http.Request, ticket string) {
	webTicketHeaders(w)
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
	if !ok || s.Auth.Users == nil {
		s.loginThrottle.fail(ip)
		webTicketRefuse(w, t, ip, "this sign-in link is invalid, expired or already used — open the workspace from the xbin app again, or sign in here")
		return
	}
	// Defence in depth (the confirmation below is the defence): a link, a
	// redirect or a form from a page — this workspace's included — or a
	// subresource never gets as far as the page. What the app (or anything
	// outside a browser page) opens says none, or nothing at all.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "none" {
		webTicketRefuse(w, t, ip, "this sign-in link only works opened by the xbin app, not followed from a page")
		return
	}
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" && mode != "navigate" {
		webTicketRefuse(w, t, ip, "this sign-in link only works as a page the xbin app opens")
		return
	}
	if q := r.URL.Query().Get("next"); q != "" && q != t.Next {
		webTicketRefuse(w, t, ip, "this sign-in link was altered — open the workspace from the xbin app again")
		return
	}
	u, _, same, why := s.webTicketCheck(r, t)
	switch {
	case why != "":
		webTicketRefuse(w, t, ip, why)
		return
	case same: // already signed in as this person: nothing to open
		http.Redirect(w, r, t.Next, http.StatusFound)
		return
	}
	nonce, _, err := s.Auth.HoldWebTicket(t)
	if err != nil {
		webTicketRefuse(w, t, ip, err.Error())
		return
	}
	secure := s.Auth.SessionCookieSecure(r)
	http.SetCookie(w, &http.Cookie{Name: webConfirmCookie(secure), Value: nonce, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: secure, MaxAge: int(auth.WebConfirmTTL / time.Second)})
	name := u.Name
	if strings.TrimSpace(name) == "" {
		name = u.ID
	}
	who := u.ID
	if u.Email != "" {
		who += " · " + u.Email
	}
	page := s.brandPage(webConfirmPageHTML, " — continue")
	page = strings.NewReplacer("{{NAME}}", htmlEscape(name), "{{WHO}}", htmlEscape(who),
		"{{NEXT}}", htmlEscape(t.Next), "{{NONCE}}", htmlEscape(nonce)).Replace(page)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Not no-referrer here: under it a browser sends the form post's Origin
	// as "null", which a browser without Fetch Metadata then can't prove
	// anything with. strict-origin: the origin, never the (spent) ticket.
	w.Header().Set("Referrer-Policy", "strict-origin")
	w.Header().Set("Content-Security-Policy", webConfirmCSP)
	w.Header().Set("X-Frame-Options", "DENY")
	_, _ = w.Write([]byte(page))
}

// handleWebTicketConfirm is POST /login/web-ticket {confirm}: the
// confirmation page's Continue. Only from this origin's own page, only
// from the browser that page was served to, once.
func (s *Server) handleWebTicketConfirm(w http.ResponseWriter, r *http.Request) {
	webTicketHeaders(w)
	ip := s.ClientIP(r)
	if !s.loginThrottle.allow(ip) {
		http.Error(w, "too many attempts, slow down", http.StatusTooManyRequests)
		return
	}
	if !webConfirmSameOrigin(r) { // before anything is spent: a forged post burns nothing
		webTicketRefuse(w, auth.WebTicket{}, ip, "confirm this sign-in on the page that asked — a post from anywhere else is refused")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	_ = r.ParseForm()
	nonce := r.PostFormValue("confirm")
	secure := s.Auth.SessionCookieSecure(r)
	c, err := auth.OnlyCookie(r, webConfirmCookie(secure))
	http.SetCookie(w, &http.Cookie{Name: webConfirmCookie(secure), Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		SameSite: http.SameSiteStrictMode, Secure: secure})
	if err != nil || nonce == "" || subtle.ConstantTimeCompare([]byte(c.Value), []byte(nonce)) != 1 {
		s.loginThrottle.fail(ip)
		webTicketRefuse(w, auth.WebTicket{}, ip, "this confirmation isn't from this browser's own page — open the workspace from the xbin app again")
		return
	}
	t, ok := s.Auth.ConsumeWebConfirm(nonce)
	if !ok || s.Auth.Users == nil {
		s.loginThrottle.fail(ip)
		webTicketRefuse(w, t, ip, "this confirmation expired or was already used — open the workspace from the xbin app again")
		return
	}
	_, notAfter, same, why := s.webTicketCheck(r, t)
	switch {
	case why != "":
		webTicketRefuse(w, t, ip, why)
		return
	case same:
		http.Redirect(w, r, t.Next, http.StatusSeeOther)
		return
	}
	sid, err := s.Auth.OpenWebSession(t, ip, notAfter)
	if err != nil {
		webTicketRefuse(w, t, ip, err.Error())
		return
	}
	s.setSessionCookie(w, r, sid)
	slog.Info("audit", "who", "user:"+t.UserID, "method", "POST", "path", "/login/web-ticket", "status", http.StatusSeeOther,
		"device", t.DeviceID, "ip", ip)
	http.Redirect(w, r, t.Next, http.StatusSeeOther)
}

// webTicketCheck is what both halves re-check: the account and the device
// still exist and may sign in (and the SSO window, D93, whose end caps the
// session: notAfter), and the browser isn't signed in as somebody else —
// the owner token and view-as sessions included; same: it already is this
// person. why is the refusal, "" when none.
func (s *Server) webTicketCheck(r *http.Request, t auth.WebTicket) (u *users.User, notAfter time.Time, same bool, why string) {
	u, found := s.Auth.Users.Get(t.UserID)
	if !found || u.Disabled {
		return nil, notAfter, false, "this account is disabled"
	}
	if uid, _, found := s.Auth.Users.FindDevice(t.DeviceID); !found || uid != t.UserID {
		return nil, notAfter, false, "the device that opened this link was removed"
	}
	notAfter, ok := s.deviceSSOBound(u)
	if !ok {
		return nil, notAfter, false, "this workspace signs in through single sign-on, and yours is too old — sign in with SSO again"
	}
	if p, ok := s.Auth.FromRequest(r); ok {
		if p.Owner || p.Component != "" || p.ReadOnly() || p.UserID != t.UserID {
			return nil, notAfter, false, "this browser is signed in as someone else — sign out here first, then open the workspace from the app again"
		}
		return u, notAfter, true, ""
	}
	return u, notAfter, false, ""
}

// webConfirmSameOrigin: the post came from a page of this origin. With
// Fetch Metadata the browser says so (Sec-Fetch-Site same-origin — an
// opaque-origin sandbox is cross-site — and a navigation: a form, not a
// fetch); an Origin it sends must then name this host, or be "null" (what
// a no-referrer policy a privacy setting forces makes of a same-origin
// post's Origin). Without Fetch Metadata (Safari before 16.4) the Origin
// must name this host. A request proving neither is refused.
func webConfirmSameOrigin(r *http.Request) bool {
	site, origin := r.Header.Get("Sec-Fetch-Site"), r.Header.Get("Origin")
	if mode := r.Header.Get("Sec-Fetch-Mode"); mode != "" && mode != "navigate" {
		return false
	}
	switch {
	case site != "" && site != "same-origin":
		return false
	case site == "" && (origin == "" || origin == "null"):
		return false
	case origin == "" || origin == "null":
		return true
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host != "" && strings.EqualFold(u.Host, r.Host)
}

// webConfirmCookie binds a confirmation to the browser its page was served
// to: __Host- when secure, so a sibling (tile) origin can't toss one in.
func webConfirmCookie(secure bool) string {
	if secure {
		return "__Host-xbin_webconfirm"
	}
	return "xbin_webconfirm"
}

func webTicketHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// webTicketRefuse answers 403 with why (a fixed text, never request input)
// and logs it.
func webTicketRefuse(w http.ResponseWriter, t auth.WebTicket, ip, why string) {
	slog.Warn("web ticket refused", "why", why, "ip", ip, "user", t.UserID, "device", t.DeviceID)
	http.Error(w, why, http.StatusForbidden)
}

// webConfirmCSP: the confirmation page runs no script, loads nothing but
// its inline style and data: icons, is never framed (no clickjacking of
// Continue) and posts only here.
const webConfirmCSP = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'"

// webConfirmPageHTML is the signed-out browser's "Continue as <name>" page
// (the sign-in pages' styling; brandPage fills TITLE/ICON/LOGO, the rest is
// HTML-escaped server-side).
const webConfirmPageHTML = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{TITLE}}</title>
<link rel="icon" href="{{ICON}}">
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
  background:#1b1e24;color:#d4d9e0;font:14px/1.5 -apple-system,"Segoe UI",system-ui,sans-serif}
.card{background:#23272e;border:1px solid #363c45;border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,.45);
  padding:26px 28px;width:320px;max-width:calc(100vw - 24px);box-sizing:border-box}
.logo{display:flex;align-items:center;gap:9px;font-weight:800;font-size:16px;letter-spacing:.04em;margin-bottom:16px}
.logo svg,.logo img.mark{flex:none}
.logo img.mark{width:22px;height:22px;object-fit:contain;border-radius:4px}
h1{font-size:16px;margin:0 0 2px;overflow-wrap:anywhere}
.who{font-size:12.5px;color:#868f9a;margin:0 0 4px;overflow-wrap:anywhere}
.next{font-size:12px;color:#868f9a;margin:0;overflow-wrap:anywhere}
.next code{color:#d4d9e0}
button{width:100%;margin-top:16px;background:#f5a623;color:#23272e;border:0;border-radius:6px;
  padding:9px;font:700 14px inherit;cursor:pointer;overflow-wrap:anywhere}
button:hover{background:#e0912a}
.warn{margin-top:14px;background:#3a2d12;border:1px solid #8a6d1a;color:#e3c878;border-radius:6px;
  padding:8px 10px;font-size:12px}
.alt{margin-top:12px;font-size:12px;color:#868f9a}
.alt a{color:#f5a623}
</style></head><body>
<form class="card" method="post" action="/login/web-ticket">
  <div class="logo">{{LOGO}}</div>
  <h1>Continue as {{NAME}}</h1>
  <p class="who">{{WHO}}</p>
  <p class="next">opens <code>{{NEXT}}</code></p>
  <input type="hidden" name="confirm" value="{{NONCE}}">
  <button autofocus>Continue as {{NAME}}</button>
  <div class="warn">Only continue if you just opened this from the xbin app on your own phone or tablet. If someone sent you this link, close this page: it would sign this browser into <b>their</b> account.</div>
  <div class="alt">Not you? <a href="/login">Sign in with your own account</a></div>
</form></body></html>`
