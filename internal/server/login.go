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
<title>xbin — sign in</title>
<link rel="icon" type="image/svg+xml" href="data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCA2NCA2NCIgcm9sZT0iaW1nIiBhcmlhLWxhYmVsPSJYL0JJTiI+CiAgPHBhdGggZD0iTTE4IDRINTZhNCA0IDAgMCAxIDQgNHYzOEw0NiA2MEg4YTQgNCAwIDAgMS00LTRWMTh6IiBmaWxsPSIjZjVhNjIzIi8+CiAgPHBhdGggZD0iTTIxIDIxIDQzIDQzTTQzIDIxIDIxIDQzIiBzdHJva2U9IiMyMzI3MmUiIHN0cm9rZS13aWR0aD0iOSIgc3Ryb2tlLWxpbmVjYXA9ImJ1dHQiLz4KICA8Y2lyY2xlIGN4PSI1MyIgY3k9IjExIiByPSIyLjYiIGZpbGw9IiMyMzI3MmUiIG9wYWNpdHk9Ii40Ii8+CiAgPGNpcmNsZSBjeD0iMTEiIGN5PSI1MyIgcj0iMi42IiBmaWxsPSIjMjMyNzJlIiBvcGFjaXR5PSIuNCIvPgo8L3N2Zz4K">
<style>
:root{color-scheme:dark}
body{margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;
  background:#1b1e24;color:#d4d9e0;font:14px/1.5 -apple-system,"Segoe UI",system-ui,sans-serif}
.card{background:#23272e;border:1px solid #363c45;border-radius:10px;box-shadow:0 12px 32px rgba(0,0,0,.45);
  padding:26px 28px;width:300px}
.logo{display:flex;align-items:center;gap:9px;font-weight:800;font-size:16px;letter-spacing:.04em;margin-bottom:18px}
.logo svg{flex:none}
label{display:block;font-size:10.5px;font-weight:600;letter-spacing:.06em;text-transform:uppercase;
  color:#868f9a;margin:10px 0 3px}
input{width:100%;box-sizing:border-box;border:1px solid #363c45;border-radius:6px;padding:7px 9px;
  font:14px inherit;color:#d4d9e0;background:#2b3038}
input:focus{outline:2px solid rgba(245,166,35,.45)}
button{width:100%;margin-top:16px;background:#f5a623;color:#23272e;border:0;border-radius:6px;
  padding:8px;font:700 14px inherit;cursor:pointer}
button:hover{background:#e0912a}
.note{margin-top:14px;font-size:11.5px;color:#868f9a}
</style></head><body>
<form class="card" method="post" action="/login">
  <div class="logo"><svg viewBox="0 0 64 64" width="22" height="22" aria-hidden="true"><path d="M18 4H56a4 4 0 0 1 4 4v38L46 60H8a4 4 0 0 1-4-4V18z" fill="#f5a623"/><path d="M21 21 43 43M43 21 21 43" stroke="#23272e" stroke-width="9" stroke-linecap="butt"/></svg>X/BIN</div>
  <label for="u">Username</label>
  <input id="u" name="username" autocomplete="username" autofocus required>
  <label for="p">Password</label>
  <input id="p" name="password" type="password" autocomplete="current-password" required>
  <button>Sign in</button>
  <div class="note">Admins can also use the one-time token URL from the server logs.</div>
</form></body></html>`
