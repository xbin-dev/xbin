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

	apiMux *http.ServeMux // /api/buxon/… extensions (broker, grants, vault)
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
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /login", s.handleLogin)

	mux.Handle("GET /{$}", s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/c/root/", http.StatusFound)
	})))

	mux.Handle("/c/", s.authed(http.HandlerFunc(s.handleComponentStatic)))
	mux.Handle("GET /vendor/", s.authed(http.HandlerFunc(s.handleVendor)))
	mux.Handle("GET /docs/", s.authed(http.HandlerFunc(s.handleDocs)))

	mux.Handle("/api/", s.authed(http.HandlerFunc(s.handleAPI)))

	mux.Handle("GET /ws/term", s.authedOwner(http.HandlerFunc(s.Term.ServeWS)))
	mux.Handle("GET /ws/events", s.authed(http.HandlerFunc(s.handleEventsWS)))

	s.registerCoreAPI()
	return logRequests(mux)
}

// authed requires any valid principal and stores it in the request context.
func (s *Server) authed(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Auth.FromRequest(r)
		if !ok {
			http.Error(w, "unauthorized — open the login URL printed by buxond", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithPrincipal(r.Context(), p)))
	})
}

// authedOwner additionally requires the owner principal (terminals are the
// editing plane; elements never get shells).
func (s *Server) authedOwner(next http.Handler) http.Handler {
	return s.authed(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !auth.PrincipalOf(r).Owner {
			http.Error(w, "owner only", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	}))
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	tok := r.URL.Query().Get("token")
	if s.Auth.NoAuth() {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	if tok != s.Auth.OwnerToken {
		http.Error(w, "bad token", http.StatusForbidden)
		return
	}
	secure := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: tok, Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: secure,
		MaxAge: int((365 * 24 * time.Hour).Seconds()),
	})
	http.Redirect(w, r, "/", http.StatusFound)
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
		if p.Owner {
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
