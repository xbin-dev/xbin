package server

// limitspage.go — the tile limits page, GET /xbin/limits[?tile=<t>]
// (D203, docs/isolation.md §Resource limits): a guaranteed surface shipped
// with xbind, like /xbin/partitions (personpage.go), so a workspace whose
// scaffold predates tile limits — an old shell, an old admin console —
// still approves a tile's ask and raises a cap; alert messages name it.
//
// The same contract as the partitions page: web/limits.html served byte
// for byte (no HTML transform, no principal-specific bytes), reading and
// acting through /api/xbin/limits with the signed-in person's session;
// top-level only (frame-ancestors 'none', X-Frame-Options DENY, COOP), a
// CSP of 'self' alone, and no copy under /vendor/ (topLevelPage).

import (
	"io/fs"
	"net/http"
)

const (
	// LimitsPagePath is where the tile limits page is served.
	LimitsPagePath = "/xbin/limits"
	limitsPageFile = "limits.html" // in the web assets (xbin.WebFS)
)

// handleLimitsPage serves the limits page to any signed-in principal
// (authed): the page itself holds nothing of anyone's.
func (s *Server) handleLimitsPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, limitsPageFile)
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
