package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

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
	// The CSP the sandboxed path serves must carry allow-downloads (ND10 —
	// tiles may trigger file downloads) and must never carry
	// allow-same-origin (that plus allow-scripts would void the sandbox).
	if !strings.Contains(sandboxCSP, "allow-downloads") {
		t.Error("sandboxCSP must allow downloads (ND10)")
	}
	if strings.Contains(sandboxCSP, "allow-same-origin") {
		t.Error("sandboxCSP must never include allow-same-origin")
	}
}

// The /c/ credential-less subresource exception requires a recently-
// authenticated source IP on top of the (spoofable) Fetch-Metadata
// fingerprint: a drive-by scanner that never logged in gets 401 even with
// perfect headers, while a browser — whose tile subresource loads always
// follow an authenticated document load from the same IP — is unaffected.
func TestStaticWarmIPGate(t *testing.T) {
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
	mk("apps/lib/xbin.json", `{}`)
	mk("apps/lib/app.js", `console.log("tile");`)
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
	h := s.authedStatic(http.HandlerFunc(s.handleComponentStatic))

	// httptest.NewRequest's default peer: 192.0.2.1:1234.
	subresource := func(url string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Sec-Fetch-Dest", "script")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// Cold IP (never authenticated): forged fingerprint is not enough.
	if w := subresource("/c/apps/lib/app.js"); w.Code != 401 {
		t.Fatalf("cold IP with forged headers: want 401, got %d", w.Code)
	}

	// A successful login warms the source IP → the exception applies.
	a.NewSession("alice", "192.0.2.1")
	if w := subresource("/c/apps/lib/app.js"); w.Code != 200 || !strings.Contains(w.Body.String(), "tile") {
		t.Fatalf("warm IP subresource: want 200, got %d", w.Code)
	}

	// HTML documents are never subresources — warm IP or not.
	if w := subresource("/c/apps/lib/index.html"); w.Code != 401 {
		t.Fatalf("warm IP .html: want 401, got %d", w.Code)
	}

	// TTL expiry lapses the IP back to cold (2h > the 1h window).
	a.TestAgeWarmIP("192.0.2.1", 2*time.Hour)
	if w := subresource("/c/apps/lib/app.js"); w.Code != 401 {
		t.Fatalf("lapsed warm IP: want 401, got %d", w.Code)
	}
}

// The sandbox is per component (ND11): the base CSP for every non-chrome
// document, the base + what the SandboxExtras hook unlocks for a granted tile
// (at BOTH emission sites — injected HTML and inject:false), mirrored into
// the xbin-sandbox meta and the /components `sandbox` field; chrome gets no
// sandbox and COOP instead. A sandboxed document must NEVER carry COOP: a
// top-level response with a sandboxed origin and a COOP other than
// unsafe-none is a network error per the HTML spec — direct-tab opens of
// /c/<tile>/ would break.
func TestSandboxHeaderPerComponent(t *testing.T) {
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
	page := `<!doctype html><html><head><title>t</title></head><body>t</body></html>`
	mk("apps/plain/xbin.json", `{}`)
	mk("apps/plain/index.html", page)
	mk("apps/linky/xbin.json", `{"uses":[{"target":"cap:open-links","role":"writer"}]}`)
	mk("apps/linky/index.html", page)
	mk("apps/raw/xbin.json", `{"inject":false}`)
	mk("apps/raw/index.html", page)
	mk("tiles/chrome/xbin.json", `{"chrome":true}`)
	mk("tiles/chrome/index.html", page)

	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a}
	extras := []string{"allow-popups", "allow-popups-to-escape-sandbox"}
	s.Pol = testPolicy{sandbox: func(c string) []string {
		if c == "apps/linky" || c == "apps/raw" {
			return extras
		}
		return nil
	}}
	owner := auth.Principal{Owner: true}
	get := func(url string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
		w := httptest.NewRecorder()
		s.handleComponentStatic(w, r)
		return w
	}
	ext := sandboxCSP + " allow-popups allow-popups-to-escape-sandbox"

	w := get("/c/apps/plain/index.html")
	if got := w.Header().Get("Content-Security-Policy"); got != sandboxCSP {
		t.Fatalf("plain CSP: %q", got)
	}
	if w.Header().Get("Cross-Origin-Opener-Policy") != "" {
		t.Fatal("a sandboxed document must not carry COOP")
	}
	if !strings.Contains(w.Body.String(), `name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads"`) {
		t.Fatalf("plain meta: %s", w.Body.String())
	}

	w = get("/c/apps/linky/index.html")
	if got := w.Header().Get("Content-Security-Policy"); got != ext {
		t.Fatalf("granted CSP: %q", got)
	}
	if w.Header().Get("Cross-Origin-Opener-Policy") != "" {
		t.Fatal("a granted (still sandboxed) document must not carry COOP — network error per spec")
	}
	if !strings.Contains(w.Body.String(), `name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatalf("granted meta: %s", w.Body.String())
	}

	w = get("/c/apps/raw/index.html")
	if got := w.Header().Get("Content-Security-Policy"); got != ext {
		t.Fatalf("inject:false granted CSP: %q", got)
	}
	if strings.Contains(w.Body.String(), "xbin-sandbox") {
		t.Fatal("inject:false is byte-exact — no meta")
	}

	w = get("/c/tiles/chrome/index.html")
	if w.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("chrome must not be sandboxed")
	}
	if w.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" {
		t.Fatal("chrome keeps COOP same-origin")
	}
	if strings.Contains(w.Body.String(), "xbin-sandbox") {
		t.Fatal("chrome carries no xbin-sandbox meta")
	}
	if sandboxHeader(nil) != sandboxCSP {
		t.Fatal("no extras = the base header")
	}

	// /components reports the same extras (chrome: none), so bx-frame's
	// attribute and the header stay one list.
	r := httptest.NewRequest("GET", "/api/xbin/components", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
	cw := httptest.NewRecorder()
	s.apiComponents(cw, r)
	var list []struct {
		Path    string   `json:"path"`
		Chrome  bool     `json:"chrome"`
		Sandbox []string `json:"sandbox"`
	}
	if err := json.Unmarshal(cw.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string][]string{}
	for _, c := range list {
		got[c.Path] = c.Sandbox
	}
	if len(got["apps/linky"]) != 2 || got["apps/linky"][1] != "allow-popups-to-escape-sandbox" {
		t.Fatalf("linky sandbox field: %v", got["apps/linky"])
	}
	if got["apps/plain"] != nil || got["tiles/chrome"] != nil {
		t.Fatalf("plain/chrome must report no extras: %v / %v", got["apps/plain"], got["tiles/chrome"])
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
	s.Pol = testPolicy{code: func(from, target string) bool {
		return from == "apps/scanner" && target == "apps/lib"
	}}

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

// The components listing is the discovery half of source reading: an element
// holding a code grant must LIST what the grant covers, not just fetch known
// paths (a bare `code` grant = every component — a code-stats tile that only
// saw itself + chrome was the original report).
func TestComponentsCodeGrant(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"apps/code-stats", "apps/a", "apps/b"} {
		p := filepath.Join(root, filepath.FromSlash(rel), "xbin.json")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(`{}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a}

	list := func(p auth.Principal) []string {
		r := httptest.NewRequest("GET", "/api/xbin/components", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		s.apiComponents(w, r)
		var out []struct{ Path string }
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		paths := []string{}
		for _, c := range out {
			paths = append(paths, c.Path)
		}
		sort.Strings(paths)
		return paths
	}
	el := auth.Principal{Component: "apps/code-stats", Via: "instance"}

	// No grant hook: element lists only itself (self-only clamp).
	if got := list(el); !slices.Contains(got, "apps/code-stats") || slices.Contains(got, "apps/a") {
		t.Fatalf("ungranted element listing = %v", got)
	}
	// Bare-code-style grant (hook says yes to everything): all components list.
	s.Pol = testPolicy{code: func(from, target string) bool { return from == "apps/code-stats" }}
	got := list(el)
	for _, want := range []string{"apps/a", "apps/b", "apps/code-stats"} {
		if !slices.Contains(got, want) {
			t.Fatalf("code-granted element listing = %v, missing %s", got, want)
		}
	}
}

// testPolicy answers one or two of the Policy questions over NoopPolicy.
type testPolicy struct {
	NoopPolicy
	sandbox func(string) []string
	code    func(string, string) bool
}

func (p testPolicy) SandboxExtras(c string) []string {
	if p.sandbox == nil {
		return nil
	}
	return p.sandbox(c)
}
func (p testPolicy) CodeReadGrant(from, target string) bool {
	return p.code != nil && p.code(from, target)
}
