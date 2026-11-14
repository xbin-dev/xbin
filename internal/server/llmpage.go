package server

// llmpage.go — the LLM connector's page, GET /xbin/llm (D226, docs/llm.md):
// a guaranteed surface shipped with xbind, like /xbin/limits
// (limitspage.go), so a workspace whose admin console predates the
// connector still sets it up — upstreams, default models, quotas — and
// reads usage; the connector's alerts name it. A person who is not an admin
// reads their own usage and quota there.
//
// The same contract as the limits page: web/llm.html served byte for byte
// (no HTML transform, no principal-specific bytes), reading and acting
// through /api/xbin/llm… with the signed-in person's session; top-level
// only, a CSP of 'self' alone, and no copy under /vendor/ (topLevelPage).

import (
	"io/fs"
	"net/http"
)

const (
	// LLMPagePath is where the LLM connector's page is served.
	LLMPagePath = "/xbin/llm"
	llmPageFile = "llm.html" // in the web assets (xbin.WebFS)
)

// handleLLMPage serves the page to any signed-in principal: the page itself
// holds nothing of anyone's.
func (s *Server) handleLLMPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, llmPageFile)
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
