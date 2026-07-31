// Package server is xbind's front door: route table, auth middleware, login
// flow, static component serving (with the single sanctioned HTML transform),
// and the /api/xbin/* core API. Endpoint reference: docs/protocol.md.
package server

import (
	"bufio"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/term"
)

type Server struct {
	Reg  *registry.Registry
	Auth *auth.Auth
	Hub  *events.Hub
	Term *term.Manager

	WebFS   fs.FS  // core elements + vendored deps, served at /vendor/
	DocsFS  fs.FS  // builder docs, served at /docs/
	Version string // the running xbind build id (commit/describe), for /status

	// ComponentAPI serves /api/<component-path>/… (runner-backed reverse
	// proxy). The request it receives has the caller principal in context.
	ComponentAPI http.Handler

	// BusFilter authorizes bus events per subscriber (installed by the broker).
	BusFilter func(p auth.Principal, e events.Event) bool

	// IsAdmin reports whether a principal may use admin-capable endpoints
	// (owner, or an element granted xbin:admin). Installed by the broker;
	// nil ⇒ owner-only.
	IsAdmin func(p auth.Principal) bool

	// Interfaces resolves a component's http interface slots to {url, service}
	// for injection into its frame (plans/interfaces.md). Installed by the broker.
	Interfaces func(comp string) map[string]any

	apiMux        *http.ServeMux // /api/xbin/… extensions (broker, grants, vault)
	loginThrottle *loginThrottle
}

// RegisterAPI mounts a handler under /api/xbin/. Pattern is a ServeMux
// pattern rooted at "/" (e.g. "GET /kv/{res}/{key...}"). Called before Start.
func (s *Server) RegisterAPI(pattern string, h http.HandlerFunc) {
	if s.apiMux == nil {
		s.apiMux = http.NewServeMux()
	}
	s.apiMux.HandleFunc(pattern, h)
}

func (s *Server) Handler() http.Handler {
	s.loginThrottle = newLoginThrottle()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /login", s.handleLogin)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)

	mux.Handle("GET /{$}", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/c/root/", http.StatusFound)
	})))

	mux.Handle("/c/", s.authed(http.HandlerFunc(s.handleComponentStatic)))
	mux.Handle("GET /vendor/", s.authed(http.HandlerFunc(s.handleVendor)))
	mux.Handle("GET /docs/", s.authed(http.HandlerFunc(s.handleDocs)))

	mux.Handle("/api/", s.authed(http.HandlerFunc(s.handleAPI)))

	mux.Handle("GET /ws/term", s.authedTerminal(http.HandlerFunc(s.Term.ServeWS)))
	mux.Handle("DELETE /ws/term", s.authedTerminal(http.HandlerFunc(s.handleTermKill)))
	mux.Handle("DELETE /ws/term/env", s.authedTerminal(http.HandlerFunc(s.handleTermReset)))
	mux.Handle("GET /ws/events", s.authed(http.HandlerFunc(s.handleEventsWS)))

	s.registerCoreAPI()
	return logRequests(mux)
}

// authed requires any valid principal and stores it in the request context.
// An unauthenticated browser navigation is redirected to the login page; API
// clients get a 401.
func (s *Server) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Auth.FromRequest(r)
		if !ok {
			if r.Method == http.MethodGet && strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			http.Error(w, "unauthorized — sign in at /login", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

// authedTerminal additionally requires terminal (root-shell) permission
// (plans/multi-user.md): the root token, admins, or a user explicitly flagged.
func (s *Server) authedTerminal(next http.Handler) http.Handler {
	return s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.PrincipalOf(r).CanTerminal() {
			http.Error(w, "terminal access is admin-only (root shell)", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// handleTermKill ends a terminal session by id (?session=). The UI uses it to
// restart a session under a new network scope (DELETE /ws/term).
func (s *Server) handleTermKill(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("session")
	if !s.Term.CanTouch(id, auth.PrincipalOf(r)) {
		http.Error(w, "session belongs to another user", http.StatusForbidden)
		return
	}
	if id == "" || !s.Term.Kill(id) {
		http.Error(w, "no such session", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTermReset wipes a component's persistent terminal layer (?cwd=<path>)
// back to the base rootfs, killing any live session on it first
// (DELETE /ws/term/env). The UI's "reset sandbox" action calls this.
func (s *Server) handleTermReset(w http.ResponseWriter, r *http.Request) {
	cwd := r.URL.Query().Get("cwd")
	// Resetting a tile's dev layer is a terminal-plane action (it IS the
	// terminal's overlay), so it needs terminal level on that tile (admins:
	// any); the legacy root layer ("" — root terminals are disabled) is
	// admin-only.
	p := auth.PrincipalOf(r)
	if cwd == "" {
		if !p.IsAdmin() {
			http.Error(w, "resetting the root layer is admin-only", http.StatusForbidden)
			return
		}
	} else if !p.CanTerminalTile(strings.Trim(cwd, "/")) {
		http.Error(w, "your account doesn't have terminal access to this tile", http.StatusForbidden)
		return
	}
	if err := s.Term.ResetEnv(cwd); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, value string) {
	secure := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: value, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
		MaxAge: int((30 * 24 * time.Hour).Seconds()),
	})
}

// handleLogin: GET serves the login page (username/password), and the
// `?token=` bootstrap sets the root/admin cookie. POST authenticates a user.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.Auth.NoAuth() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if r.Method == http.MethodPost {
		s.handleLoginPost(w, r)
		return
	}
	if tok := r.URL.Query().Get("token"); tok != "" {
		if s.Auth.TokenLoginDisabled() {
			http.Error(w, "token login is disabled — sign in with your account", http.StatusForbidden)
			return
		}
		if !s.Auth.IsOwnerToken(tok) {
			http.Error(w, "bad token", http.StatusForbidden)
			return
		}
		setSessionCookie(w, r, tok) // root token as the admin cookie
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(loginPageHTML))
}

func (s *Server) handleLoginPost(w http.ResponseWriter, r *http.Request) {
	if !s.loginThrottle.allow(clientIP(r)) {
		http.Error(w, "too many attempts, slow down", http.StatusTooManyRequests)
		return
	}
	_ = r.ParseForm()
	user, pass := r.FormValue("username"), r.FormValue("password")
	if s.Auth.Users == nil {
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	u, ok := s.Auth.Users.Verify(user, pass)
	if !ok {
		s.loginThrottle.fail(clientIP(r))
		// Generic error — never reveal whether the username exists.
		http.Error(w, "invalid credentials", http.StatusUnauthorized)
		return
	}
	s.loginThrottle.ok(clientIP(r))
	setSessionCookie(w, r, s.Auth.NewSession(u.ID))
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		s.Auth.DropSession(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
	http.Redirect(w, r, "/login", http.StatusFound)
}

// handleAPI routes /api/xbin/* to the core+extension mux and everything else
// to the component reverse proxy.
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/")
	if rest == "xbin" || strings.HasPrefix(rest, "xbin/") {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(rest, "xbin"), "/")
		if auditable(r.Method, r2.URL.Path) {
			aw := &auditWriter{ResponseWriter: w, status: http.StatusOK}
			s.apiMux.ServeHTTP(aw, r2)
			// Who changed workspace governance, and did it take. Data-plane
			// writes (prefs/kv) are excluded as noise; see auditable.
			slog.Info("audit", "who", auth.PrincipalOf(r).From(),
				"method", r.Method, "path", r2.URL.Path, "status", aw.status)
			return
		}
		s.apiMux.ServeHTTP(w, r2)
		return
	}
	if s.ComponentAPI == nil {
		http.Error(w, `{"error":"component backends not enabled"}`, http.StatusNotImplemented)
		return
	}
	s.ComponentAPI.ServeHTTP(w, r)
}

func (s *Server) handleEventsWS(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	filter := func(e events.Event) bool {
		if e.Type != "bus" {
			return true
		}
		if p.IsAdmin() {
			return true
		}
		if s.BusFilter == nil {
			return false
		}
		return s.BusFilter(p, e)
	}
	serveEventsWS(w, r, s.Hub, filter)
}

// Cumulative HTTP traffic counters (all routes), exposed by /status for the
// shell's status footer — the client deltas two polls into req/s + MB/s.
var (
	reqCount    atomic.Int64
	respBytes   atomic.Int64
	trafficBoot = time.Now()
)

// countingWriter tallies response bytes while passing streaming interfaces
// through (Flush for SSE; Hijack for proxied WebSocket upgrades).
type countingWriter struct{ http.ResponseWriter }

func (c *countingWriter) Write(b []byte) (int, error) {
	n, err := c.ResponseWriter.Write(b)
	respBytes.Add(int64(n))
	return n, err
}
func (c *countingWriter) Flush() {
	if f, ok := c.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (c *countingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := c.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// auditable reports whether a core-API call is a workspace-governance mutation
// worth an audit line: any state-changing method, minus the high-frequency
// data plane (prefs, kv) that would just be noise. So user/grant/lifecycle/
// vault/create/token changes are logged with actor + outcome; per-tile state
// writes are not.
func auditable(method, path string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return false
	}
	// The element data plane (prefs/kv/blob/bus) is high-frequency and not
	// governance — exclude it so the audit stream stays signal.
	for _, dp := range []string{"/prefs", "/kv/", "/blob/", "/bus/"} {
		if strings.HasPrefix(path, dp) {
			return false
		}
	}
	return true
}

// auditWriter records the response status for an audit line (bytes/streaming
// don't matter here — audited endpoints return small JSON).
type auditWriter struct {
	http.ResponseWriter
	status int
}

func (a *auditWriter) WriteHeader(code int) {
	a.status = code
	a.ResponseWriter.WriteHeader(code)
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		reqCount.Add(1)
		next.ServeHTTP(&countingWriter{w}, r)
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			return // long-lived; logged at close by their handlers
		}
		slog.Debug("http", "m", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
	})
}
