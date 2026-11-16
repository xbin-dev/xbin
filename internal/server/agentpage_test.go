package server

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// covers D232 — the workspace agent's page is xbind's own, guaranteed
// surface: GET /xbin/agent serves web/agent.html byte for byte to every
// signed-in principal (no HTML transform), top-level only (frame-ancestors
// 'none', X-Frame-Options DENY, COOP); its CSP keeps script and connect to
// 'self' and — unlike the other pages — lets it frame the agent's own page;
// a signed-out browser goes to sign in and back here; /vendor/ never serves
// the page; an embedded tree without the page answers 404.
func TestAgentPage(t *testing.T) {
	page := []byte("<!doctype html><title>Agent</title><script type=\"module\" src=\"/vendor/agent-page.js\"></script>\n")
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			w.s.WebFS = fstest.MapFS{
				agentPageFile:     {Data: page},
				"agent-page.js":   {Data: []byte("export {};\n")},
				"partitions.html": {Data: []byte("<p>partitions</p>")},
			}
			w.do("/healthz") // builds the handler
			if !slices.Contains(w.s.CoreRoutes(), "GET "+AgentPagePath) {
				t.Fatalf("GET %s isn't a core route: %v", AgentPagePath, w.s.CoreRoutes())
			}
			for _, who := range []string{"ana", "bob"} {
				rec := w.do(AgentPagePath, w.session(who), hdr("Accept", "text/html"), hdr("Sec-Fetch-Dest", "document"))
				if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), page) {
					t.Fatalf("%s: %d %q (a transform?)", who, rec.Code, rec.Body.String())
				}
				h := rec.Header()
				csp := h.Get("Content-Security-Policy")
				for _, d := range []string{"frame-ancestors 'none'", "script-src 'self'", "default-src 'self'", "object-src 'none'", "base-uri 'none'", "connect-src 'self'", "frame-src 'self'"} {
					if !strings.Contains(csp, d) {
						t.Errorf("%s: CSP %q lacks %q", who, csp, d)
					}
				}
				if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
					t.Errorf("%s: CSP %q allows inline or eval'd script", who, csp)
				}
				if h.Get("X-Frame-Options") != "DENY" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" ||
					h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
					t.Errorf("%s: headers %v", who, h)
				}
			}
			if rec := w.do(AgentPagePath, hdr("Accept", "text/html")); rec.Code != 302 || rec.Header().Get("Location") != "/login?next=%2Fxbin%2Fagent" {
				t.Errorf("signed out: %d %v", rec.Code, rec.Header())
			}
			if rec := w.do(AgentPagePath); rec.Code != 401 {
				t.Errorf("signed out, no browser: %d", rec.Code)
			}
			for _, u := range []string{"/vendor/agent.html", "/vendor/vendor/agent.html"} {
				if rec := w.do(u); rec.Code != 404 {
					t.Errorf("%s: %d, want 404 (the page is served only at %s)", u, rec.Code, AgentPagePath)
				}
			}
			if rec := w.do("/vendor/agent-page.js"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
				t.Errorf("the page's module: %d %q", rec.Code, rec.Header().Get("Content-Type"))
			}
			w.s.WebFS = fstest.MapFS{"partitions.html": {Data: []byte("<p>x</p>")}}
			if rec := w.do(AgentPagePath, w.session("ana"), hdr("Accept", "text/html")); rec.Code != 404 {
				t.Errorf("no page file: %d", rec.Code)
			}
		})
	}
}
