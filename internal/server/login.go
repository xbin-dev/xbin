package server

import (
	"net"
	"net/http"
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

func clientIP(r *http.Request) string {
	// Trust a fronting proxy's X-Forwarded-For first hop, else the peer.
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

const loginPageHTML = `<!doctype html><html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>buxon — sign in</title>
<style>
:root{color-scheme:light}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
  background:#f0f2f5;color:#33414e;font:14px/1.5 -apple-system,"Segoe UI",system-ui,sans-serif}
.card{background:#fff;border:1px solid #e4e8ed;border-radius:10px;box-shadow:0 8px 28px rgba(16,24,40,.10);
  padding:26px 28px;width:300px}
.logo{display:flex;align-items:center;gap:8px;font-weight:700;font-size:16px;margin-bottom:18px}
.logo .d{width:22px;height:22px;border-radius:7px;background:#1e88e5;display:inline-flex;
  align-items:center;justify-content:center;color:#fff;font-weight:800;font-size:13px}
label{display:block;font-size:10.5px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
  color:#8794a1;margin:10px 0 3px}
input{width:100%;box-sizing:border-box;border:1px solid #e4e8ed;border-radius:6px;padding:7px 9px;
  font:14px inherit;color:#33414e;background:#fff}
input:focus{outline:2px solid rgba(30,136,229,.3)}
button{width:100%;margin-top:16px;background:#1e88e5;color:#fff;border:0;border-radius:6px;
  padding:8px;font:600 14px inherit;cursor:pointer}
button:hover{background:#1a76c9}
.note{margin-top:14px;font-size:11.5px;color:#8794a1}
</style></head><body>
<form class="card" method="post" action="/login">
  <div class="logo"><span class="d">b</span>buxon</div>
  <label for="u">Username</label>
  <input id="u" name="username" autocomplete="username" autofocus required>
  <label for="p">Password</label>
  <input id="p" name="password" type="password" autocomplete="current-password" required>
  <button>Sign in</button>
  <div class="note">Admins can also use the one-time token URL from the server logs.</div>
</form></body></html>`
