package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/gpu"
)

// registerCoreAPI mounts the always-present /api/buxon/* endpoints. Broker,
// grants, and vault endpoints are added by their packages via RegisterAPI.
func (s *Server) registerCoreAPI() {
	s.RegisterAPI("GET /status", s.apiStatus)
	s.RegisterAPI("GET /components", s.apiComponents)
	s.RegisterAPI("GET /components/{path...}", s.apiComponent)
	s.RegisterAPI("GET /gpus", s.apiGPUs)
	s.RegisterAPI("GET /frame-token", s.apiFrameToken)
	s.RegisterAPI("GET /openapi.json", s.apiOpenAPI)
}

// apiGPUs lists the host GPUs available for gpu:* grants / the terminal picker.
func (s *Server) apiGPUs(w http.ResponseWriter, r *http.Request) {
	if !s.admin(r) {
		apiErr(w, http.StatusForbidden, "admin only")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{"gpus": gpu.Inventory()})
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
	Template    bool     `json:"template,omitempty"` // a blueprint, not a live tile
	Roles       any      `json:"roles,omitempty"`
	Uses        any      `json:"uses,omitempty"`
	Deps        []string `json:"deps,omitempty"`
	ManifestErr string   `json:"manifestError,omitempty"`
}

func (s *Server) apiComponents(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	out := []componentInfo{}
	for _, c := range s.Reg.Components() {
		// A user sees only the tiles they may use (plus chrome); admins, all.
		if !isChrome(c.Path) && !p.CanUseTile(c.Path) {
			continue
		}
		ci := componentInfo{
			Path: c.Path, Scope: c.Scope, Runtime: c.Manifest.Runtime,
			HasIndex: c.HasIndex, Template: c.IsTemplate(),
			Deps: c.Manifest.Deps, ManifestErr: c.ManifestErr,
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

// apiFrameToken refreshes a frame token. Allowed for a principal that may use
// the tile (admin/owner any; a user only tiles on their allow-list; a tile
// frontend only its own component). The token is re-bound to the caller's user.
func (s *Server) apiFrameToken(w http.ResponseWriter, r *http.Request) {
	comp := r.URL.Query().Get("component")
	p := auth.PrincipalOf(r)
	ok := comp != "" && (p.Component == comp || (p.Component == "" && p.CanUseTile(comp)))
	if !ok {
		apiErr(w, http.StatusForbidden, "cannot mint frame token for this tile")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"token": s.Auth.MintFrameToken(comp, p.UserID, frameTokenTTL),
	})
}
