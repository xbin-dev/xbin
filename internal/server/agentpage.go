package server

// agentpage.go — the workspace agent's page, GET /xbin/agent (D232,
// docs/protocol/delegation.md §The chrome): xbind's own chrome for a
// person's workspace agent — the dock (web/bx-agent-dock.js) full width —
// for a workspace whose scaffold predates it (no chip in its top bar) and
// for direct links: a push's, the sign-in step of a change set
// (?changes=<id>), an alert's.
//
// The same contract as the LLM connector's page: web/agent.html served
// byte for byte (no HTML transform, no principal-specific bytes), reading
// and acting through /api/xbin/delegate/… with the signed-in person's
// session; top-level only (frame-ancestors 'none', X-Frame-Options DENY,
// COOP), and no copy under /vendor/ (topLevelPage). Its CSP differs in one
// directive: it may FRAME — the agent's own page is a tile document in a
// sandboxed frame below the chrome (another origin in origins mode) — and
// nothing else: script, style and connect stay 'self'.

import (
	"io/fs"
	"net/http"
)

const (
	// AgentPagePath is where the workspace agent's page is served.
	AgentPagePath = "/xbin/agent"
	agentPageFile = "agent.html" // in the web assets (xbin.WebFS)
	agentPageCSP  = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
		"connect-src 'self'; frame-src 'self' https: http://*.localhost:*; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
)

// handleAgentPage serves the page to any signed-in principal (authed): the
// page itself holds nothing of anyone's.
func (s *Server) handleAgentPage(w http.ResponseWriter, r *http.Request) {
	b, err := fs.ReadFile(s.WebFS, agentPageFile)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-cache") // revalidate: it changes on upgrade
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Content-Security-Policy", agentPageCSP)
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Referrer-Policy", "same-origin")
	_, _ = w.Write(b)
}
