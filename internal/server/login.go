package server

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// loginThrottle blunts password brute-force: after a few failures from an IP,
// logins from that IP are refused for a cooldown. Success clears it.
type loginThrottle struct {
	mu    sync.Mutex
	fails map[string]*throttleState
}

type throttleState struct {
	count int
	until time.Time
}

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{fails: map[string]*throttleState{}}
}

const (
	throttleMaxFails = 5
	throttleCooldown = 30 * time.Second
)

func (t *loginThrottle) allow(ip string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.fails[ip]
	if s == nil {
		return true
	}
	if s.count >= throttleMaxFails && time.Now().Before(s.until) {
		return false
	}
	return true
}

func (t *loginThrottle) fail(ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.fails[ip]
	if s == nil {
		s = &throttleState{}
		t.fails[ip] = s
	}
	s.count++
	if s.count >= throttleMaxFails {
		s.until = time.Now().Add(throttleCooldown)
	}
}

func (t *loginThrottle) ok(ip string) {
	t.mu.Lock()
	delete(t.fails, ip)
	t.mu.Unlock()
}

// ClientIP resolves the request's client IP for login throttling, session
// IP attribution, and the /c/ warm-IP gate. X-Forwarded-For is honored ONLY
// when the immediate peer is a configured trusted proxy (--trusted-proxies)
// — an untrusted client must never pick its own throttle/attribution
// identity (rotating XFF used to bypass the login throttle entirely).
//
// The chain is walked from the RIGHT, skipping trusted-proxy hops, to the
// first address a trusted proxy actually vouched for: appending proxies
// (nginx $proxy_add_x_forwarded_for, Caddy, HAProxy) put the real client
// there, while everything further left is CLIENT-SUPPLIED — taking the
// leftmost hop would hand the spoof right back to any client behind the
// proxy. A hop that doesn't parse as an IP ends the walk (fall back to the
// peer), which also keeps attacker-chosen strings out of the throttle and
// warm-IP keyspaces.
func (s *Server) ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" && s.trustedProxy(host) {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break // garbage hop — nothing left of it is trustworthy either
			}
			if !s.trustedProxyAddr(a) {
				return a.String()
			}
			// A trusted proxy's own address (a longer proxy chain) — keep
			// walking toward the client.
		}
		// Every hop was a trusted proxy, or the chain was garbage: the peer
		// itself is the closest thing to a client we can attest.
	}
	return host
}

// trustedProxy reports whether ip (the immediate peer) is a configured
// trusted reverse proxy.
func (s *Server) trustedProxy(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	return s.trustedProxyAddr(addr)
}

func (s *Server) trustedProxyAddr(addr netip.Addr) bool {
	for _, p := range s.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// loginPageHTML is the sign-in page: Base Two's Work volume on the product
// tokens (pagetheme.go, D184). brandPage fills TITLE/ICON/LOGO and themed
// THEME; handleLogin fills ERR, SSO and PWNOTE, escaped there.
const loginPageHTML = pageOpen + `{{THEME}}
<title>{{TITLE}}</title>
<link rel="icon" href="{{ICON}}">
<style>` + pageCSS + `
a.sso{box-sizing:border-box;display:flex;align-items:center;justify-content:center;min-height:var(--bx-control-h);
  margin:0 0 12px;padding:4px 11px;background:var(--bx-panel);border:1px solid var(--bx-border-strong);
  border-radius:var(--bx-radius);color:var(--bx-text);font:var(--bx-font);font-weight:600;text-decoration:none}
a.sso:hover{background:var(--bx-hover)}
.or{display:flex;align-items:center;gap:8px;margin:12px 0;color:var(--bx-subtle)}
.or::before,.or::after{content:"";flex:1;border-top:1px solid var(--bx-border)}
.note{margin:16px 0 0;color:var(--bx-muted)}
</style></head><body class="bx">
<form class="plate" method="post" action="/login">
  <div class="logo">{{LOGO}}</div>
  <div class="main">
  {{ERR}}{{SSO}}{{PWNOTE}}<label for="u">Username</label>
  <input id="u" name="username" autocomplete="username" autofocus required>
  <label for="p">Password</label>
  <input id="p" name="password" type="password" autocomplete="current-password" required>
  <button class="primary">Sign in</button>
  <p class="note">Got an invite link? Open it to set your password. Operators can sign in with the owner-token URL from the server logs.</p>
  </div>
</form></body></html>`

// invitePageHTML is the set-your-password page an invite link opens (D22).
// Styled as the sign-in page; {{USER}}/{{TOKEN}}/{{ERR}}/{{WARN}} are
// substituted server-side (HTML-escaped). There is no self-signup — this
// page only ever finishes an admin-created account.
const invitePageHTML = pageOpen + `{{THEME}}
<title>{{TITLE}}</title>
<link rel="icon" href="{{ICON}}">
<style>` + pageCSS + `</style></head><body class="bx">
<form class="plate" method="post" action="/login/invite">
  <div class="logo">{{LOGO}}</div>
  <div class="main">
  <h1>Welcome, {{USER}}</h1>
  <p class="muted">Choose a password to finish setting up your account. This link works once.</p>
  <input type="hidden" name="invite" value="{{TOKEN}}">
  <label for="p">Password (min 8 characters)</label>
  <input id="p" name="password" type="password" autocomplete="new-password" minlength="8" autofocus required>
  <label for="p2">Repeat password</label>
  <input id="p2" name="password2" type="password" autocomplete="new-password" minlength="8" required>
  <button class="primary">Set password &amp; sign in</button>
  {{WARN}}
  {{ERR}}
  </div>
</form></body></html>`

// inviteBadHTML: an invalid/expired/used invite — deliberately generic.
const inviteBadHTML = pageOpen + `{{THEME}}
<title>{{TITLE}}</title>
<link rel="icon" href="{{ICON}}">
<style>` + pageCSS + `</style></head><body class="bx">
<div class="plate">
  <div class="logo">{{LOGO}}</div>
  <div class="main">
  <h1 class="status">` + svgError + `This invite link is invalid, expired, or already used.</h1>
  <p>Ask your workspace admin for a fresh one, or <a href="/login">sign in</a> if you already set a password.</p>
  </div>
</div></body></html>`
