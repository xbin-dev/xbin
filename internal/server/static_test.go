package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// Credential-less tile subresource authorization (plans/auth.md §6): a
// sandboxed frame's asset loads arrive with no cookie and no Referer, so
// they're authorized by the opaque-origin Fetch-Metadata fingerprint — a
// signal unsandboxed same-origin JS cannot produce — plus a genuine
// subresource destination. Documents, fetches, HTML, and non-GETs must fail.
func TestTileSubresource(t *testing.T) {
	req := func(method, site, dest string) *http.Request {
		r := httptest.NewRequest(method, "/c/apps/x/app.js", nil)
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if dest != "" {
			r.Header.Set("Sec-Fetch-Dest", dest)
		}
		return r
	}
	cases := []struct {
		name string
		r    *http.Request
		want bool
	}{
		{"module script, opaque origin", req("GET", "cross-site", "script"), true},
		{"stylesheet", req("GET", "cross-site", "style"), true},
		{"image", req("GET", "cross-site", "image"), true},
		{"same-site engine variant", req("GET", "same-site", "script"), true},
		{"HEAD", req("HEAD", "cross-site", "script"), true},
		{"same-origin (unsandboxed JS)", req("GET", "same-origin", "script"), false},
		{"missing site", req("GET", "", "script"), false},
		{"missing dest", req("GET", "cross-site", ""), false},
		{"document dest (HTML nav)", req("GET", "cross-site", "document"), false},
		{"iframe dest (nested doc)", req("GET", "cross-site", "iframe"), false},
		{"empty dest (fetch/XHR)", req("GET", "cross-site", "empty"), false},
		{"embed dest", req("GET", "cross-site", "embed"), false},
		{"non-GET", req("POST", "cross-site", "script"), false},
	}
	for _, c := range cases {
		if got := tileSubresource(c.r); got != c.want {
			t.Errorf("%s: tileSubresource=%v, want %v", c.name, got, c.want)
		}
	}
	// HTML documents never pass the rule even with otherwise-valid signals.
	if tileSubresource(req("GET", "cross-site", "script")) != true {
		t.Fatal("sanity")
	}
	r := httptest.NewRequest("GET", "/c/apps/x/index.html", nil)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	r.Header.Set("Sec-Fetch-Dest", "script")
	if tileSubresource(r) {
		t.Error("index.html passed the subresource rule")
	}
}

// The sandbox decision: everything runs in an opaque origin EXCEPT implicit
// chrome (root, shell — the workspace UI itself) and manifest-flagged trusted
// chrome (e.g. tiles/organisations, which acts as the human by design).
func TestSandboxedFrame(t *testing.T) {
	plain := &registry.Component{Path: "apps/x"}
	chrome := &registry.Component{Path: "tiles/organisations", Manifest: registry.Manifest{Chrome: true}}
	cases := []struct {
		path string
		comp *registry.Component
		want bool
	}{
		{"apps/x", plain, true},
		{"apps/x", nil, true}, // unregistered dir before rescan: sandboxed
		{"root", nil, false},
		{"shell", nil, false},
		{"tiles/organisations", chrome, false},
	}
	for _, c := range cases {
		if got := sandboxedFrame(c.path, c.comp); got != c.want {
			t.Errorf("%s: sandboxedFrame=%v, want %v", c.path, got, c.want)
		}
	}
}

// A code[:<comp>] grant opens the /c/ static plane for element principals
// (the 2026-08-02 clamp made instance tokens self-only even WITH the grant —
// tooling backends couldn't fetch sibling source). Grant-based reads must
// never mint the OTHER tile's frame token into served HTML.
func TestStaticCodeGrant(t *testing.T) {
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
	mk("apps/scanner/xbin.json", `{}`)
	mk("apps/lib/xbin.json", `{}`)
	mk("apps/lib/secret.js", `const key = "hunter2";`)
	mk("apps/lib/index.html", `<!doctype html><html><head><title>lib</title></head><body>lib</body></html>`)

	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a}
	s.CodeReadGrant = func(from, target string) bool {
		return from == "apps/scanner" && target == "apps/lib"
	}

	get := func(url string, p auth.Principal) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.handleComponentStatic(w, r)
		return w
	}
	scanner := auth.Principal{Component: "apps/scanner", Via: "instance"}

	// Granted: source file and HTML doc both read.
	if w := get("/c/apps/lib/secret.js", scanner); w.Code != 200 || !strings.Contains(w.Body.String(), "hunter2") {
		t.Fatalf("code-granted source read: got %d", w.Code)
	}
	w := get("/c/apps/lib/index.html", scanner)
	if w.Code != 200 {
		t.Fatalf("code-granted HTML read: got %d", w.Code)
	}
	// …but the served HTML must carry NO frame token for apps/lib (a
	// code-grant read must not hand the other tile's credential to scanner).
	if !strings.Contains(w.Body.String(), `xbin-frame-token" content=""`) {
		t.Fatal("grant-based read leaked a frame token for the other tile")
	}

	// Ungranted: a different element gets 403.
	if w := get("/c/apps/lib/secret.js", auth.Principal{Component: "apps/other", Via: "instance"}); w.Code != 403 {
		t.Fatalf("ungranted element: want 403, got %d", w.Code)
	}
	// And the grant never widens HUMAN reads (humans use per-tile RBAC).
	if w := get("/c/apps/lib/secret.js", auth.Principal{}); w.Code != 403 {
		t.Fatalf("anonymous human: want 403, got %d", w.Code)
	}
}
