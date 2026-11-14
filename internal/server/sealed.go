package server

// sealed.go — sealed components on the HTTP planes (D227). A sealed
// component lives at xbin/<name>, inside the namespace of xbind's own API
// (/api/xbin/…) and outside what /c/ serves (pathAllowed refuses the
// reserved top-level names). Both planes ask the registry: a path owned by
// a registered sealed component is that component's, served from xbind's
// materialized tree (codeRoot) and proxied to its backend (handleAPI).

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/util"
)

// sealedOwned reports whether urlPath ("xbin/agent/x.js") lies at or below a
// registered sealed component.
func (s *Server) sealedOwned(urlPath string) bool {
	if s.Reg == nil || !util.SealedPath(urlPath) {
		return false
	}
	c, _, ok := s.Reg.Resolve(urlPath)
	return ok && c.Sealed
}

// WantsSealed reports whether a listing's caller asked for sealed
// components too (?sealed=1). Listings leave them out otherwise, so a shell,
// an admin console or an app that predates them never shows a tile it
// can't open a terminal, the code or a deployment of (docs/compat.md).
func WantsSealed(r *http.Request) bool {
	switch r.URL.Query().Get("sealed") {
	case "1", "true":
		return true
	}
	return false
}

// SealedNameClash names what of xbind's own a sealed component called name
// would shadow ("" = nothing): an /api/xbin route whose first path segment
// is the name (its API prefix is /api/xbin/<name>/…), or an xbind page
// /xbin/<name>. apiPatterns are RegisterAPI patterns, corePatterns the
// top-level mux's.
func SealedNameClash(name string, apiPatterns, corePatterns []string) string {
	for _, pat := range apiPatterns {
		seg, _, _ := strings.Cut(strings.TrimPrefix(patternPath(pat), "/"), "/")
		if seg == name || strings.HasPrefix(seg, "{") && !strings.HasPrefix(seg, "{$") {
			return fmt.Sprintf("xbind's API route %q", pat)
		}
	}
	for _, pat := range corePatterns {
		if p := patternPath(pat); p == "/xbin/"+name || strings.HasPrefix(p, "/xbin/"+name+"/") {
			return fmt.Sprintf("xbind's page %q", pat)
		}
	}
	return ""
}

// patternPath is a ServeMux pattern without its method.
func patternPath(pat string) string {
	if i := strings.IndexByte(pat, ' '); i >= 0 {
		return pat[i+1:]
	}
	return pat
}

// SealedAPIClash names the first registered sealed component whose name
// shadows something of xbind's own (SealedNameClash), "" when none does.
// Boot refuses to serve with one, after every route is registered: a
// sealed component can never take a route of the API over, and a new route
// can never be added under a sealed name unnoticed.
func (s *Server) SealedAPIClash() string {
	if s.Reg == nil {
		return ""
	}
	for _, c := range s.Reg.SealedComponents() {
		if what := SealedNameClash(strings.TrimPrefix(c.Path, util.SealedPrefix), s.apiPatterns, s.corePatterns); what != "" {
			return fmt.Sprintf("sealed component %s would shadow %s", c.Path, what)
		}
	}
	return ""
}
