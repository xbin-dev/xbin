package server

// sealed.go — sealed components on the HTTP planes (spike S1, issue #51).
// A sealed component lives at xbin/<name>, inside the namespace of xbind's
// own API (/api/xbin/…) and outside what /c/ serves (pathAllowed refuses the
// reserved top-level names). Both planes ask the registry: a path owned by
// a registered sealed component is that component's, served from xbind's
// materialized tree (codeRoot) and proxied to its backend (handleAPI).

import (
	"fmt"
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

// SealedAPIClash names an /api/xbin route of xbind's own that a registered
// sealed component's API prefix would shadow ("" = none): a route whose
// first path segment is the component's name. Boot refuses to serve with
// one, so a sealed component can never take a route of the API over, and a
// new route can never be added under a sealed name unnoticed.
func (s *Server) SealedAPIClash() string {
	if s.Reg == nil {
		return ""
	}
	for _, c := range s.Reg.SealedComponents() {
		name := strings.TrimPrefix(c.Path, util.SealedPrefix)
		for _, pat := range s.apiPatterns {
			p := pat
			if i := strings.IndexByte(p, ' '); i >= 0 {
				p = p[i+1:]
			}
			seg, _, _ := strings.Cut(strings.TrimPrefix(p, "/"), "/")
			if seg == name || strings.HasPrefix(seg, "{") {
				return fmt.Sprintf("sealed component %s would shadow xbind's API route %q", c.Path, pat)
			}
		}
	}
	return ""
}
