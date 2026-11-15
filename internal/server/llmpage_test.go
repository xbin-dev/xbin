package server

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// covers D226 — the LLM connector's page is xbind's own, guaranteed
// surface: GET /xbin/llm serves web/llm.html byte for byte to every
// signed-in principal (no HTML transform), top-level only (frame-ancestors
// 'none', X-Frame-Options DENY, COOP) under a CSP of 'self' alone; a
// signed-out browser goes to sign in and back here; /vendor/ never serves
// the page, while its modules load from /vendor/; an embedded tree without
// the page answers 404.
func TestLLMPage(t *testing.T) {
	page := []byte("<!doctype html><title>LLM</title><script type=\"module\" src=\"/vendor/llm-page.js\"></script>\n")
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			w.s.WebFS = fstest.MapFS{
				llmPageFile:       {Data: page},
				"llm-page.js":     {Data: []byte("export {};\n")},
				"partitions.html": {Data: []byte("<p>partitions</p>")},
			}
			w.do("/healthz") // builds the handler
			if !slices.Contains(w.s.CoreRoutes(), "GET "+LLMPagePath) {
				t.Fatalf("GET %s isn't a core route: %v", LLMPagePath, w.s.CoreRoutes())
			}
			for _, who := range []string{"ana", "bob"} {
				rec := w.do(LLMPagePath, w.session(who), hdr("Accept", "text/html"), hdr("Sec-Fetch-Dest", "document"))
				if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), page) {
					t.Fatalf("%s: %d %q (a transform?)", who, rec.Code, rec.Body.String())
				}
				h := rec.Header()
				csp := h.Get("Content-Security-Policy")
				for _, d := range []string{"frame-ancestors 'none'", "script-src 'self'", "default-src 'self'", "object-src 'none'", "base-uri 'none'"} {
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
			if rec := w.do(LLMPagePath, hdr("Accept", "text/html")); rec.Code != 302 || rec.Header().Get("Location") != "/login?next=%2Fxbin%2Fllm" {
				t.Errorf("signed out: %d %v", rec.Code, rec.Header())
			}
			if rec := w.do(LLMPagePath); rec.Code != 401 {
				t.Errorf("signed out, no browser: %d", rec.Code)
			}
			for _, u := range []string{"/vendor/llm.html", "/vendor/vendor/llm.html"} {
				if rec := w.do(u); rec.Code != 404 {
					t.Errorf("%s: %d, want 404 (the page is served only at %s)", u, rec.Code, LLMPagePath)
				}
			}
			if rec := w.do("/vendor/llm-page.js"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
				t.Errorf("the page's module: %d %q", rec.Code, rec.Header().Get("Content-Type"))
			}
			w.s.WebFS = fstest.MapFS{"partitions.html": {Data: []byte("<p>x</p>")}}
			if rec := w.do(LLMPagePath, w.session("ana"), hdr("Accept", "text/html")); rec.Code != 404 {
				t.Errorf("no page file: %d", rec.Code)
			}
		})
	}
}
