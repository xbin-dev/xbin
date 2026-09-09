package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/gpu"
	"github.com/xbin-dev/xbin/internal/registry"
)

// registerCoreAPI mounts the always-present /api/xbin/* endpoints. Broker,
// grants, and vault endpoints are added by their packages via RegisterAPI.
func (s *Server) registerCoreAPI() {
	s.RegisterAPI("GET /status", s.apiStatus)
	s.RegisterAPI("POST /auth-rotate-token", s.apiRotateToken)
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

// WriteError writes the built-in API's error shape — {"error": msg}, plus
// "docs": the page to read when one is given (docs/protocol.md "Errors").
// Every non-2xx answer of /api/xbin/* goes through here; the shape is a
// wire contract (docs/compat.md rule 2), so it never grows a field
// silently.
func WriteError(w http.ResponseWriter, code int, msg string, docs ...string) {
	m := map[string]string{"error": msg}
	if len(docs) > 0 && docs[0] != "" {
		m["docs"] = docs[0]
	}
	WriteJSON(w, code, m)
}

// WriteOK writes {"ok":"true"} — the historical success shape of the
// mutating endpoints (a string, not a bool; kept as is for old clients).
func WriteOK(w http.ResponseWriter) {
	WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// DecodeJSON decodes a request body strictly — unknown fields are errors,
// so a misspelled key is a 400 instead of a silently ignored setting — and
// closes it. Handlers that must accept unknown fields (bodies produced by
// older clients) decode by hand.
func DecodeJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func apiErr(w http.ResponseWriter, code int, msg string) {
	WriteError(w, code, msg, "/docs/protocol.md")
}

// admin reports whether the request's principal may use admin-capable
// endpoints (owner, or xbin:admin via the installed Policy).
func (s *Server) admin(r *http.Request) bool {
	p := auth.PrincipalOf(r)
	if p.Owner {
		return true
	}
	return s.policy().IsAdmin(p)
}

// apiRotateToken swaps the owner token (admin). The new token is returned
// exactly once to the caller; the old one — including any historically leaked
// copy (pre-terminal-token agent transcripts) — stops authenticating
// immediately, for bearer calls and owner-token cookies alike.
func (s *Server) apiRotateToken(w http.ResponseWriter, r *http.Request) {
	if !s.admin(r) {
		apiErr(w, http.StatusForbidden, "admin only")
		return
	}
	tok, err := s.Auth.RotateOwnerToken()
	if err != nil {
		apiErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	slog.Warn("owner token rotated", "by", auth.PrincipalOf(r).From())
	WriteJSON(w, http.StatusOK, map[string]string{"token": tok})
}

func (s *Server) apiStatus(w http.ResponseWriter, r *http.Request) {
	if !s.admin(r) {
		apiErr(w, http.StatusForbidden, "admin only")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]any{
		"components": len(s.Reg.Components()),
		"terminals":  s.Term.List(),
		"version":    s.Version, // running xbind build commit
		"host":       hostStats(s.Reg.Root),
		"traffic": map[string]any{
			"reqs": reqCount.Load(), "bytesOut": respBytes.Load(),
			"uptimeSec": int64(time.Since(trafficBoot).Seconds()),
		},
	})
}

type componentInfo struct {
	Path        string   `json:"path"`
	Scope       string   `json:"scope,omitempty"`
	Runtime     string   `json:"runtime,omitempty"`
	HasIndex    bool     `json:"hasIndex"`
	Template    bool     `json:"template,omitempty"` // a blueprint, not a live tile
	State       string   `json:"state,omitempty"`    // lifecycle; "" = enabled (offloaded/disabled hidden by clients)
	Roles       any      `json:"roles,omitempty"`
	Uses        any      `json:"uses,omitempty"`
	Deps        []string `json:"deps,omitempty"`
	ManifestErr string   `json:"manifestError,omitempty"`
	Owner       string   `json:"owner,omitempty"` // "user:<id>" | "org:<id>" | "" (D24)
	// Chrome marks trusted workspace chrome (plans/auth.md §6): bx-frame does
	// NOT sandbox these frames — they act as the signed-in human.
	Chrome bool `json:"chrome,omitempty"`
	// Sandbox lists the `sandbox` tokens this component's grants unlock beyond
	// the base set (ND11: cap:open-links → allow-popups allow-popups-to-
	// escape-sandbox). bx-frame appends them to its iframe attribute; the CSP
	// header of the component's documents carries the same list. Absent for
	// chrome and for tiles holding no such grant.
	Sandbox []string `json:"sandbox,omitempty"`
}

func (s *Server) apiComponents(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	out := []componentInfo{}
	for _, c := range s.Reg.Components() {
		// A user sees only the tiles they may read (plus chrome); admins, all.
		// An element additionally lists what its code[:<comp>] grant covers —
		// the discovery half of source reading (a bare `code` grant is "all
		// components": tooling like linters and stats must be able to
		// enumerate what it may read, not just fetch known paths).
		if !isChrome(c.Path) && !p.CanReadTile(c.Path) && !s.codeGranted(p, c.Path) {
			continue
		}
		ci := componentInfo{
			Path: c.Path, Scope: c.Scope, Runtime: c.Manifest.Runtime,
			HasIndex: c.HasIndex, Template: c.IsTemplate(),
			Deps: c.Manifest.Deps, ManifestErr: c.ManifestErr,
			Chrome: isChrome(c.Path) || c.Manifest.Chrome,
		}
		ci.Owner = s.policy().OwnerOf(c.Path)
		if !ci.Chrome {
			ci.Sandbox = s.sandboxExtras(c.Path)
		}
		if st := s.Reg.LifecycleState(c.Path); st != registry.StateEnabled {
			ci.State = st
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
		Chrome: isChrome(c.Path) || c.Manifest.Chrome,
	}
	if !ci.Chrome {
		ci.Sandbox = s.sandboxExtras(c.Path)
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
// frontend only its own component — including a COOKIE-LESS one, since a
// sandboxed frame holds nothing but its token). The token is re-bound to the
// caller's user.
func (s *Server) apiFrameToken(w http.ResponseWriter, r *http.Request) {
	comp := r.URL.Query().Get("component")
	p := auth.PrincipalOf(r)
	ok := comp != "" && (p.Component == comp || (p.Component == "" && p.CanReadTile(comp)))
	if !ok {
		apiErr(w, http.StatusForbidden, "cannot mint frame token for this tile")
		return
	}
	WriteJSON(w, http.StatusOK, map[string]string{
		"token": s.Auth.MintFrameToken(comp, p.UserID, frameTokenTTL),
	})
}
