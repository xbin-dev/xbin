package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// --dev-overlay shadows workspace files on the /c/ plane so `make dev` serves
// the shell and admin tile from workspace-template/ without copying. Files
// only: a manifest is never overlaid (the registry read the real one); a path
// the overlay lacks falls through to the workspace; a file only the overlay
// has is served too (new scaffold files need no re-init).
func TestDevOverlay(t *testing.T) {
	root, overlay := t.TempDir(), t.TempDir()
	mk := func(base, rel, content string) {
		p := filepath.Join(base, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk(root, "apps/x/xbin.json", `{"chrome":true}`)
	mk(root, "apps/x/index.html", `<!doctype html><html><head></head><body>workspace index</body></html>`)
	mk(root, "apps/x/app.js", `// workspace app`)
	mk(root, "apps/x/only-ws.js", `// only in the workspace`)
	mk(overlay, "apps/x/xbin.json", `{"chrome":false}`)
	mk(overlay, "apps/x/index.html", `<!doctype html><html><head></head><body>overlay index</body></html>`)
	mk(overlay, "apps/x/app.js", `// overlay app`)
	mk(overlay, "apps/x/only-overlay.js", `// only in the overlay`)

	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a, Overlay: overlay}
	get := func(url string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		s.handleComponentStatic(w, r)
		return w
	}
	cases := []struct{ url, want string }{
		{"/c/apps/x/app.js", "overlay app"},                  // overlaid file
		{"/c/apps/x/index.html", "overlay index"},            // overlaid document (injected)
		{"/c/apps/x/", "overlay index"},                      // directory index resolves through the overlay too
		{"/c/apps/x/only-ws.js", "only in the workspace"},    // no overlay copy → workspace
		{"/c/apps/x/xbin.json", `{"chrome":true}`},           // manifests are never overlaid
		{"/c/apps/x/only-overlay.js", "only in the overlay"}, // a new scaffold file: served from the overlay alone
		{"/c/apps/x/missing.js", ""},                         // in neither → 404
	}
	for _, c := range cases {
		w := get(c.url)
		if c.want == "" {
			if w.Code != 404 {
				t.Errorf("%s: want 404, got %d", c.url, w.Code)
			}
			continue
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), c.want) {
			t.Errorf("%s: got %d %q, want body containing %q", c.url, w.Code, w.Body.String(), c.want)
		}
	}

	// Off by default: the same server without Overlay serves the workspace.
	s.Overlay = ""
	if w := get("/c/apps/x/app.js"); !strings.Contains(w.Body.String(), "workspace app") {
		t.Errorf("overlay off: got %q", w.Body.String())
	}
}

// A directory index for a component created after the registry's last scan
// is attributed to the DIRECTORY (the component path), never to the
// index.html inside it — the integration suite caught a refactor that
// reassigned the cleaned path while resolving the overlay.
func TestDirIndexAttributesUnscannedComponent(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("apps/old/index.html", `<html><head></head><body>old</body></html>`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a}
	// created after the scan: the registry does not know it yet
	mk("apps/hello/index.html", `<html><head></head><body>hello</body></html>`)
	for _, overlay := range []string{"", t.TempDir()} {
		s.Overlay = overlay
		r := httptest.NewRequest("GET", "/c/apps/hello/", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		s.handleComponentStatic(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `name="xbin-component" content="apps/hello"`) {
			t.Errorf("overlay=%q: got %d %q", overlay, w.Code, w.Body.String())
		}
	}
}
