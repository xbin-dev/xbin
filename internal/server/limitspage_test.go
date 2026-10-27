package server

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

// covers D203 — the tile limits page is xbind's own, guaranteed surface:
// GET /xbin/limits[?tile=] serves web/limits.html byte for byte to every
// signed-in principal (no HTML transform), top-level only (frame-ancestors
// 'none', X-Frame-Options DENY, COOP) under a CSP of 'self' alone; a
// signed-out browser goes to sign in and back here, query included; /vendor/
// never serves the page, while its modules load from /vendor/.
func TestLimitsPage(t *testing.T) {
	page := []byte("<!doctype html><title>Limits</title><script type=\"module\" src=\"/vendor/limits-page.js\"></script>\n")
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			w.s.WebFS = fstest.MapFS{
				limitsPageFile:      {Data: page},
				"limits-page.js":    {Data: []byte("export {};\n")},
				"partitions.html":   {Data: []byte("<p>partitions</p>")},
				"vendor/lit-all.js": {Data: []byte("export {};\n")},
			}
			w.do("/healthz") // builds the handler
			if !slices.Contains(w.s.CoreRoutes(), "GET "+LimitsPagePath) {
				t.Fatalf("GET %s isn't a core route: %v", LimitsPagePath, w.s.CoreRoutes())
			}
			for _, who := range []string{"ana", "bob"} {
				for _, u := range []string{LimitsPagePath, LimitsPagePath + "?tile=apps/x"} {
					rec := w.do(u, w.session(who), hdr("Accept", "text/html"), hdr("Sec-Fetch-Dest", "document"))
					if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), page) {
						t.Fatalf("%s %s: %d %q (a transform?)", who, u, rec.Code, rec.Body.String())
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
			}
			// signed out: a browser signs in and lands back here; an API client gets 401
			if rec := w.do(LimitsPagePath+"?tile=apps/x", hdr("Accept", "text/html")); rec.Code != 302 || rec.Header().Get("Location") != "/login?next=%2Fxbin%2Flimits%3Ftile%3Dapps%2Fx" {
				t.Errorf("signed out: %d %v", rec.Code, rec.Header())
			}
			if rec := w.do(LimitsPagePath); rec.Code != 401 {
				t.Errorf("signed out, no browser: %d", rec.Code)
			}
			for _, u := range []string{"/vendor/limits.html", "/vendor/vendor/limits.html"} {
				if rec := w.do(u); rec.Code != 404 {
					t.Errorf("%s: %d, want 404 (the page is served only at %s)", u, rec.Code, LimitsPagePath)
				}
			}
			if rec := w.do("/vendor/limits-page.js"); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
				t.Errorf("the page's module: %d %q", rec.Code, rec.Header().Get("Content-Type"))
			}
		})
	}
}

// covers D203 — before web/limits.html ships (an embedded tree without it),
// the route answers 404 rather than anything else.
func TestLimitsPageMissing(t *testing.T) {
	w := newAssetWS(t, TileAssetsTokens)
	w.s.WebFS = fstest.MapFS{"partitions.html": {Data: []byte("<p>x</p>")}}
	if rec := w.do(LimitsPagePath, w.session("ana"), hdr("Accept", "text/html")); rec.Code != 404 {
		t.Errorf("no page file: %d", rec.Code)
	}
}
