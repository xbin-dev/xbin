package server

// personpage.go — the partitions page, GET /xbin/partitions
// (plans/partitions/06 §12.1; PD-47; docs/partitions.md §Your partitions
// page): a guaranteed surface, shipped with xbind like /docs/, so a
// person's consents, credential confirmations and a manager's switch
// decisions never wait for a scaffold update.
//
// It is a static page — web/partitions.html and the /vendor/ modules it
// loads — served byte for byte as the binary (or --dev's web/) holds it: no
// HTML transform (the D4 injection is for tile documents only), no
// principal-specific bytes. Everything it shows it reads from the
// partitions API with the signed-in person's own session, and xbind judges
// every act there as for bx.
//
// It opens top-level only: frame-ancestors 'none' and X-Frame-Options DENY
// (a tile framing it could otherwise overlay a consent or a credential
// confirmation — clickjacking), its own browsing-context group (COOP, as
// /docs/), and a CSP of 'self' alone (no inline script). /vendor/ doesn't
// serve the page (topLevelPage), so it has no copy without those headers.

import (
	"io/fs"
	"net/http"
)

const (
	// PersonPagePath is where the partitions page is served (the pushes
	// link it as "xbin/partitions", workspace-relative).
	PersonPagePath = "/xbin/partitions"
	personPageFile = "partitions.html" // in the web assets (xbin.WebFS)
	personPageCSP  = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// handlePersonPage serves the partitions page to any signed-in principal
// (authed): the page itself holds nothing of anyone's.
func (s *Server) handlePersonPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, personPageFile)
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

// topLevelPage reports whether name (a /vendor/ path) is a page xbind
// serves only at its own route, with its framing headers.
func topLevelPage(name string) bool { return name == personPageFile }
