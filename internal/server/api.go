package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/magik6k/buxon/internal/auth"
)

// registerCoreAPI mounts the always-present /api/buxon/* endpoints. Broker,
// grants, and vault endpoints are added by their packages via RegisterAPI.
func (s *Server) registerCoreAPI() {
	s.RegisterAPI("GET /status", s.apiStatus)
	s.RegisterAPI("GET /components", s.apiComponents)
	s.RegisterAPI("GET /components/{path...}", s.apiComponent)
	s.RegisterAPI("GET /frame-token", s.apiFrameToken)
}

func WriteJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	WriteJSON(w, code, map[string]string{"error": msg, "docs": "/docs/protocol.md"})
}

// admin reports whether the request's principal may use admin-capable
// endpoints (owner, or buxon:admin via the broker-installed hook).
func (s *Server) admin(r *http.Request) bool {
	p := auth.PrincipalOf(r)
	if p.Owner {
		return true
	}
	return s.IsAdmin != nil && s.IsAdmin(p)
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	if !s.admin(r) {
		apiErr(w, http.StatusForbidden, "admin only")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"components": len(s.Reg.Components()),
		"terminals":  s.Term.List(),
	})
}

type componentInfo struct {
	Path        string   `json:"path"`
	Scope       string   `json:"scope,omitempty"`
	Runtime     string   `json:"runtime,omitempty"`
	HasIndex    bool     `json:"hasIndex"`
	Roles       any      `json:"roles,omitempty"`
	Uses        any      `json:"uses,omitempty"`
	Deps        []string `json:"deps,omitempty"`
	ManifestErr string   `json:"manifestError,omitempty"`
}

func (s *Server) apiComponents(w http.ResponseWriter, r *http.Request) {
	out := []componentInfo{}
	for _, c := range s.Reg.Components() {
		ci := componentInfo{
			Path: c.Path, Scope: c.Scope, Runtime: c.Manifest.Runtime,
			HasIndex: c.HasIndex, Deps: c.Manifest.Deps, ManifestErr: c.ManifestErr,
		}
		if c.Manifest.Expose != nil {
			ci.Roles = c.Manifest.Expose.Roles
		}
		if len(c.Manifest.Uses) > 0 {
			ci.Uses = c.Manifest.Uses
		}
		out = append(out, ci)
	}
	WriteJSON(w, http.StatusOK, out)
}

// apiComponent returns one component's metadata plus its API.md if present —
// the machine end of the docs standard (docs/elements.md §API contract).
func (s *Server) apiComponent(w http.ResponseWriter, r *http.Request) {
	p := r.PathValue("path")
	c, ok := s.Reg.Component(p)
	if !ok {
		apiErr(w, http.StatusNotFound, "no such component")
		return
	}
	ci := componentInfo{
		Path: c.Path, Scope: c.Scope, Runtime: c.Manifest.Runtime,
		HasIndex: c.HasIndex, Deps: c.Manifest.Deps, ManifestErr: c.ManifestErr,
	}
	if c.Manifest.Expose != nil {
		ci.Roles = c.Manifest.Expose.Roles
	}
	if len(c.Manifest.Uses) > 0 {
		ci.Uses = c.Manifest.Uses
	}
	apiMD := ""
	if b, err := os.ReadFile(filepath.Join(c.Dir, "API.md")); err == nil {
		apiMD = string(b)
	}
	WriteJSON(w, http.StatusOK, map[string]any{"component": ci, "apiDoc": apiMD})
}

// apiFrameToken refreshes a frame token. Allowed for the owner (any
// component) or an element frontend refreshing its own token.
func (s *Server) apiFrameToken(w http.ResponseWriter, r *http.Request) {
	comp := r.URL.Query().Get("component")
	p := auth.PrincipalOf(r)
	if comp == "" || (!p.Owner && p.Component != comp) {
		apiErr(w, http.StatusForbidden, "cannot mint frame token for other components")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"token": s.Auth.MintFrameToken(comp, frameTokenTTL),
	})
}
