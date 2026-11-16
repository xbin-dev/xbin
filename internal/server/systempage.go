package server

// systempage.go — the system components page, GET /xbin/system (D227,
// docs/sealed.md): a guaranteed surface shipped with xbind, like
// /xbin/limits (limitspage.go), so a workspace whose shell and admin
// console predate sealed components — which their listings leave out for
// that reason — still shows an admin each one, its state and why it is
// held, its logs, who has access, its limits and the bindings of its
// declared slots.
//
// The same contract as the other xbind pages: web/system.html served byte
// for byte (no HTML transform, no principal-specific bytes), reading
// through /api/xbin/sealed and acting through the routes a tile has, with
// the signed-in person's session; top-level only (frame-ancestors 'none',
// X-Frame-Options DENY, COOP), a CSP of 'self' alone, and no copy under
// /vendor/ (topLevelPage).

import (
	"io/fs"
	"net/http"
)

const (
	// SystemPagePath is where the system components page is served.
	SystemPagePath = "/xbin/system"
	systemPageFile = "system.html" // in the web assets (xbin.WebFS)
)

// handleSystemPage serves the page to any signed-in principal (authed):
// the page itself holds nothing of anyone's, and what it reads is the
// admins' (GET /sealed).
func (s *Server) handleSystemPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, systemPageFile)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache") // revalidate: it changes on upgrade
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", personPageCSP)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Referrer-Policy", "same-origin")
	_, _ = w.Write(b)
}
