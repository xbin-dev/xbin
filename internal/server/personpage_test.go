package server

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-47 06§12.1 — the partitions page is xbind's own, guaranteed
// surface: GET /xbin/partitions serves web/partitions.html byte for byte to
// every signed-in principal (no HTML transform: no import map, no token, no
// client injected), top-level only (frame-ancestors 'none', X-Frame-Options
// DENY, COOP) under a CSP of 'self' alone; a signed-out browser goes to the
// login page; /vendor/ never serves the page (no copy without the framing
// headers), while the page's modules load from /vendor/ as every core
// element does.
func TestPersonPage(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			w.s.WebFS = os.DirFS(filepath.Join("..", "..", "web"))
			want, err := os.ReadFile(filepath.Join("..", "..", "web", "partitions.html"))
			if err != nil {
				t.Fatal(err)
			}
			w.do("/healthz") // builds the handler
			if !slices.Contains(w.s.CoreRoutes(), "GET "+PersonPagePath) {
				t.Fatalf("GET %s isn't a core route: %v", PersonPagePath, w.s.CoreRoutes())
			}
			for _, who := range []string{"ana", "bob"} {
				rec := w.do(PersonPagePath, w.session(who), hdr("Accept", "text/html"), hdr("Sec-Fetch-Dest", "document"))
				if rec.Code != 200 {
					t.Fatalf("%s: %d %s", who, rec.Code, rec.Body.String())
				}
				if !bytes.Equal(rec.Body.Bytes(), want) {
					t.Errorf("%s: the page isn't served as it is (a transform?):\n%s", who, rec.Body.String())
				}
				h := rec.Header()
				csp := h.Get("Content-Security-Policy")
				for _, d := range []string{"frame-ancestors 'none'", "script-src 'self'", "default-src 'self'", "object-src 'none'", "base-uri 'none'"} {
					if !strings.Contains(csp, d) {
						t.Errorf("%s: CSP %q lacks %q", who, csp, d)
					}
				}
				if strings.Contains(csp, "unsafe-eval") || regexp.MustCompile(`script-src[^;]*unsafe-inline`).MatchString(csp) {
					t.Errorf("%s: CSP %q allows inline or eval'd script", who, csp)
				}
				if h.Get("X-Frame-Options") != "DENY" || h.Get("Cross-Origin-Opener-Policy") != "same-origin" ||
					h.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(h.Get("Content-Type"), "text/html") {
					t.Errorf("%s: headers %v", who, h)
				}
			}
			// signed out: a browser is sent to sign in; an API client gets 401
			if rec := w.do(PersonPagePath, hdr("Accept", "text/html")); rec.Code != 302 || rec.Header().Get("Location") != "/login" {
				t.Errorf("signed out: %d %v", rec.Code, rec.Header())
			}
			if rec := w.do(PersonPagePath); rec.Code != 401 {
				t.Errorf("signed out, no browser: %d", rec.Code)
			}
			// the page has no copy under /vendor/; its modules do
			for _, u := range []string{"/vendor/partitions.html", "/vendor/vendor/partitions.html"} {
				if rec := w.do(u); rec.Code != 404 {
					t.Errorf("%s: %d, want 404 (the page is served only at %s)", u, rec.Code, PersonPagePath)
				}
			}
			for _, u := range []string{"/vendor/partitions-page.js", "/vendor/partitions-kit.js", "/vendor/partitions-sections.js", "/vendor/partitions-more.js", "/vendor/partitions-css.js"} {
				if rec := w.do(u); rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "javascript") {
					t.Errorf("%s: %d %q", u, rec.Code, rec.Header().Get("Content-Type"))
				}
			}
		})
	}
}

// covers PD-47 — the page stays within its CSP: no inline script, no
// import map, no reference off the workspace's own origin, and its modules
// import lit from /vendor/ directly (a bare "lit" needs an import map, which
// would be an inline script).
func TestPersonPageSelfContained(t *testing.T) {
	web := filepath.Join("..", "..", "web")
	page, err := os.ReadFile(filepath.Join(web, "partitions.html"))
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`).FindAllStringSubmatch(string(page), -1) {
		if !strings.Contains(m[1], "src=") || strings.TrimSpace(m[2]) != "" || strings.Contains(m[1], "importmap") {
			t.Errorf("partitions.html: an inline script or import map (%q): its CSP runs 'self' alone", m[0])
		}
	}
	if u := regexp.MustCompile(`(?i)(src|href)="(https?:)?//`).FindString(string(page)); u != "" {
		t.Errorf("partitions.html references another origin: %q", u)
	}
	mods, _ := filepath.Glob(filepath.Join(web, "partitions-*.js"))
	if len(mods) < 5 {
		t.Fatalf("the page's modules: %v", mods)
	}
	for _, f := range mods {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range regexp.MustCompile(`(?m)^import\b[^;]*?from '([^']+)';`).FindAllStringSubmatch(string(src), -1) {
			if !strings.HasPrefix(m[1], "/vendor/") {
				t.Errorf("%s imports %q: the page has no import map — import from /vendor/", filepath.Base(f), m[1])
			}
		}
		if strings.Contains(string(src), "innerHTML") || strings.Contains(string(src), "unsafeHTML") {
			t.Errorf("%s writes HTML: the page binds every value as text", filepath.Base(f))
		}
	}
}

// covers PD-44 H1 — the page says what a switch deletes in xbind's words:
// web/partitions-kit.js holds registry.SwitchDeletes' texts
// (hack/partitions-page.test.mjs checks the kit picks them per switch).
func TestPersonPageSwitchWords(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "partitions-kit.js"))
	if err != nil {
		t.Fatal(err)
	}
	kit := strings.ReplaceAll(string(src), `\'`, "'")
	u, ug, none := registry.PartitionSpec{User: true}, registry.PartitionSpec{User: true, Global: true}, registry.PartitionSpec{}
	for _, c := range [][2]registry.PartitionSpec{{none, u}, {ug, u}, {u, ug}} {
		if w := registry.SwitchDeletes(c[0], c[1]); !strings.Contains(kit, "'"+w+"'") {
			t.Errorf("web/partitions-kit.js lacks xbind's words for %s → %s: %q", c[0], c[1], w)
		}
	}
}
