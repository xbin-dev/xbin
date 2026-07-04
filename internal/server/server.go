// Package server is buxond's front door: route table, auth middleware, login
// flow, static component serving (with the single sanctioned HTML transform),
// and the /api/buxon/* core API. Endpoint reference: docs/protocol.md.
package server

import (
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/term"
)

type Server struct {
	Reg  *registry.Registry
	Auth *auth.Auth
	Hub  *events.Hub
	Term *term.Manager

	WebFS  fs.FS // core elements + vendored deps, served at /vendor/
	DocsFS fs.FS // builder docs, served at /docs/

	// ComponentAPI serves /api/<component-path>/… (runner-backed reverse
	// proxy). The request it receives has the caller principal in context.
	ComponentAPI http.Handler

	// BusFilter authorizes bus events per subscriber (installed by the broker).
	BusFilter func(p auth.Principal, e events.Event) bool

	// IsAdmin reports whether a principal may use admin-capable endpoints
	// (owner, or an element granted buxon:admin). Installed by the broker;
	// nil ⇒ owner-only.
	IsAdmin func(p auth.Principal) bool

	apiMux        *http.ServeMux // /api/buxon/… extensions (broker, grants, vault)
	loginThrottle *loginThrottle
}

// RegisterAPI mounts a handler under /api/buxon/. Pattern is a ServeMux
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
	if id == "" || !s.Term.Kill(id) {
		http.Error(w, "no such session", http.StatusNotFound)
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
		if tok != s.Auth.OwnerToken {
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

// handleAPI routes /api/buxon/* to the core+extension mux and everything else
// to the component reverse proxy.
func (s *Server) handleAPI(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/")
	if rest == "buxon" || strings.HasPrefix(rest, "buxon/") {
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/" + strings.TrimPrefix(strings.TrimPrefix(rest, "buxon"), "/")
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

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		if strings.HasPrefix(r.URL.Path, "/ws/") {
			return // long-lived; logged at close by their handlers
		}
		slog.Debug("http", "m", r.Method, "path", r.URL.Path, "dur", time.Since(start).Round(time.Millisecond))
	})
}
