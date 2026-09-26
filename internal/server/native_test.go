package server

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// ifacePolicy adds bound interfaces to testPolicy.
type ifacePolicy struct {
	testPolicy
	ifaces map[string]map[string]any
}

func (p ifacePolicy) Interfaces(c string) map[string]any { return p.ifaces[c] }

// nativeWorkspace: tiles with and without a native entry, plus the edge
// cases (declared path, chrome, inject:false, a granted sandbox, interfaces).
func nativeWorkspace(t *testing.T) (*Server, *auth.Auth) {
	t.Helper()
	root := t.TempDir()
	page := `<!doctype html><html><head><title>t</title></head><body>t</body></html>`
	for rel, content := range map[string]string{
		"apps/conv/xbin.json":      `{"runtime":"go"}`,
		"apps/conv/index.html":     page,
		"apps/conv/native.js":      `export const tile = 'conv';`,
		"apps/conv/assets/a.svg":   `<svg/>`,
		"apps/decl/xbin.json":      `{"native": "./mobile/main.js"}`,
		"apps/decl/mobile/main.js": `export {}`,
		"apps/web/xbin.json":       `{}`,
		"apps/web/index.html":      page,
		"apps/gone/xbin.json":      `{"native": "mobile/gone.js"}`,
		"apps/gone/index.html":     page,
		"apps/linky/xbin.json":     `{"uses":[{"target":"cap:open-links","role":"writer"}]}`,
		"apps/linky/native.js":     `export {}`,
		"apps/raw/xbin.json":       `{"inject": false}`,
		"apps/raw/index.html":      page,
		"apps/raw/native.js":       `export {}`,
		"apps/ifc/xbin.json":       `{}`,
		"apps/ifc/native.js":       `export {}`,
		"tiles/chrome/xbin.json":   `{"chrome": true}`,
		"tiles/chrome/index.html":  page,
		"tiles/chrome/native.js":   `export {}`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
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
	s.Pol = ifacePolicy{
		testPolicy: testPolicy{sandbox: func(c string) []string {
			if c == "apps/linky" {
				return []string{"allow-popups", "allow-popups-to-escape-sandbox"}
			}
			return nil
		}},
		ifaces: map[string]map[string]any{"apps/ifc": {"llm": map[string]any{"url": "/api/apps/llm", "service": "openai"}}},
	}
	return s, a
}

func serveAs(s *Server, method, url string, p auth.Principal, hdr map[string]string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, nil)
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	s.handleComponentStatic(w, r)
	return w
}

// The native runtime document (docs/elements.md §Native app UI): an
// xbind-generated page — not a tile file — with the tile page's full D4
// injection, the xbin-native marker, and one module script loading the
// template layer, then the entry; with the tile's own authorization, CSP
// sandbox and headers.
func TestNativeRuntimeDocument(t *testing.T) {
	s, a := nativeWorkspace(t)
	owner := auth.Principal{Owner: true}
	get := func(url string) *httptest.ResponseRecorder { return serveAs(s, "GET", url, owner, nil) }

	w := get("/c/apps/conv/?native=1")
	if w.Code != 200 {
		t.Fatalf("runtime document: %d %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	h := w.Header()
	if h.Get("Content-Type") != "text/html; charset=utf-8" || h.Get("Cache-Control") != "no-store" ||
		h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("headers: %v", h)
	}
	if h.Get("Content-Security-Policy") != sandboxCSP || h.Get("Cross-Origin-Opener-Policy") != "" {
		t.Fatalf("sandbox: CSP %q COOP %q", h.Get("Content-Security-Policy"), h.Get("Cross-Origin-Opener-Policy"))
	}
	for _, want := range []string{
		`<script type="importmap">`,
		`<meta name="xbin-component" content="apps/conv">`,
		`<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">`,
		`<script type="module" src="/vendor/xbin-client.js"></script>`,
		`<meta name="xbin-native" content="1">`,
		`import { boot } from '/vendor/xb-native.js';`,
		`await boot("./native.js");`,
		`<meta name="viewport"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("runtime document lacks %s:\n%s", want, body)
		}
	}
	for _, not := range []string{"xbin-native-preview", "preview-host", "xbin-ws-origin", "export const tile"} {
		if strings.Contains(body, not) {
			t.Errorf("runtime document must not carry %q:\n%s", not, body)
		}
	}
	// Order: the client (window.xbin) runs first, then the template layer,
	// then the entry, booted by it (a load failure reports kind "module") —
	// module scripts execute in document order.
	if i, j, k := strings.Index(body, "xbin-client.js"), strings.Index(body, "xb-native.js"), strings.Index(body, `boot("./native.js")`); !(i < j && j < k) {
		t.Fatalf("script order client=%d xb-native=%d entry=%d", i, j, k)
	}
	// A real frame token for this tile.
	m := regexp.MustCompile(`name="xbin-frame-token" content="([^"]+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no frame token:\n%s", body)
	}
	if comp, _, ok := a.VerifyFrameToken(m[1]); !ok || comp != "apps/conv" {
		t.Fatalf("frame token: %q %v", comp, ok)
	}

	// preview=1: the preview marker, and the (optional) preview host imported
	// before the entry, its absence tolerated.
	body = get("/c/apps/conv/?native=1&preview=1").Body.String()
	if !strings.Contains(body, `<meta name="xbin-native-preview" content="1">`) {
		t.Fatalf("preview meta missing:\n%s", body)
	}
	pi := strings.Index(body, "try { await import('/vendor/xb/preview-host.js'); } catch")
	if pi < 0 || pi > strings.Index(body, `boot("./native.js")`) || pi < strings.Index(body, "xb-native.js") {
		t.Fatalf("preview host must load (tolerantly) between xb-native and the entry:\n%s", body)
	}

	// A declared entry, on a tile with no index.html at all.
	w = get("/c/apps/decl/?native=1")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `await boot("./mobile/main.js");`) {
		t.Fatalf("declared entry: %d\n%s", w.Code, w.Body.String())
	}

	// The tile's sandbox extras and interfaces ride along, as on its page.
	w = get("/c/apps/linky/?native=1")
	if w.Header().Get("Content-Security-Policy") != sandboxCSP+" allow-popups allow-popups-to-escape-sandbox" ||
		!strings.Contains(w.Body.String(), `content="allow-scripts allow-forms allow-modals allow-downloads allow-popups allow-popups-to-escape-sandbox"`) {
		t.Fatalf("granted sandbox: %q\n%s", w.Header().Get("Content-Security-Policy"), w.Body.String())
	}
	if body := get("/c/apps/ifc/?native=1").Body.String(); !strings.Contains(body, `<meta name="xbin-interfaces" content="{&quot;llm&quot;:{&quot;service&quot;:&quot;openai&quot;,&quot;url&quot;:&quot;/api/apps/llm&quot;}}">`) {
		t.Fatalf("interfaces meta:\n%s", body)
	}
	// inject:false keeps index.html byte-exact, but the runtime document is
	// xbind's own — without the injection it could do nothing.
	if w := get("/c/apps/raw/?native=1"); w.Code != 200 || !strings.Contains(w.Body.String(), "xbin-client.js") {
		t.Fatalf("inject:false tile's runtime document: %d\n%s", w.Code, w.Body.String())
	}
	if body := get("/c/apps/raw/").Body.String(); strings.Contains(body, "xbin-client.js") {
		t.Fatal("inject:false index.html must stay byte-exact")
	}

	// No native entry → 404, saying why.
	for url, why := range map[string]string{
		"/c/apps/web/?native=1":         "no native app UI",
		"/c/apps/gone/?native=1":        "no such file",
		"/c/tiles/chrome/?native=1":     "trusted chrome",
		"/c/apps/nope/?native=1":        "",
		"/c/apps/conv/assets/?native=1": "no such tile",
	} {
		w := get(url)
		if w.Code != 404 || !strings.Contains(w.Body.String(), why) {
			t.Errorf("%s: want 404 (%s), got %d %q", url, why, w.Code, w.Body.String())
		}
	}

	// The directory URL without its slash redirects, keeping the query (the
	// entry is imported relative to the directory).
	if w := get("/c/apps/conv?native=1&preview=1"); w.Code != 301 || w.Header().Get("Location") != "/c/apps/conv/?native=1&preview=1" {
		t.Fatalf("slashless: %d %q", w.Code, w.Header().Get("Location"))
	}
	// ?native=1 on anything but a tile's directory is an ordinary request.
	if w := get("/c/apps/conv/native.js?native=1"); w.Code != 200 || !strings.Contains(w.Body.String(), "export const tile") {
		t.Fatalf("file with ?native=1: %d %q", w.Code, w.Body.String())
	}
	if body := get("/c/apps/conv/index.html?native=1").Body.String(); strings.Contains(body, "xbin-native") || !strings.Contains(body, "<title>t</title>") {
		t.Fatalf("index.html with ?native=1 is the page:\n%s", body)
	}
	// Without the query the directory is still the web page.
	if body := get("/c/apps/conv/").Body.String(); strings.Contains(body, "xbin-native") {
		t.Fatalf("plain directory load must be index.html:\n%s", body)
	}
	// HEAD: headers only.
	if w := serveAs(s, "HEAD", "/c/apps/conv/?native=1", owner, nil); w.Code != 200 || w.Body.Len() != 0 ||
		w.Header().Get("Content-Security-Policy") != sandboxCSP {
		t.Fatalf("HEAD: %d len=%d", w.Code, w.Body.Len())
	}
}

// The runtime document is authorized exactly like the tile's index.html:
// a user who may read the tile gets it (with a token bound to them); one
// who may not is refused the same way; an element reading via a code grant
// gets the document WITHOUT the other tile's frame token.
func TestNativeRuntimeAuthorization(t *testing.T) {
	s, a := nativeWorkspace(t)
	alice := auth.Principal{UserID: "alice", Via: "session", User: &users.User{ID: "alice", Role: "user", Tiles: map[string]string{"apps/conv": "read"}}}
	bob := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: "user"}}

	w := serveAs(s, "GET", "/c/apps/conv/?native=1", alice, nil)
	m := regexp.MustCompile(`name="xbin-frame-token" content="([^"]+)"`).FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil {
		t.Fatalf("reader: %d\n%s", w.Code, w.Body.String())
	}
	if comp, uid, ok := a.VerifyFrameToken(m[1]); !ok || comp != "apps/conv" || uid != "alice" {
		t.Fatalf("token: %q %q %v", comp, uid, ok)
	}
	for _, url := range []string{"/c/apps/conv/?native=1", "/c/apps/conv/"} {
		if w := serveAs(s, "GET", url, bob, nil); w.Code != 403 {
			t.Fatalf("%s for a user without the tile: want 403, got %d", url, w.Code)
		}
	}
	s.Pol = testPolicy{code: func(from, target string) bool { return from == "apps/scanner" }}
	w = serveAs(s, "GET", "/c/apps/conv/?native=1", auth.Principal{Component: "apps/scanner", Via: "instance"}, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `xbin-frame-token" content=""`) {
		t.Fatalf("code-grant read must carry no frame token: %d\n%s", w.Code, w.Body.String())
	}
}

// Another tile's frontend never gets a frame token out of a runtime document
// (or a tile page): ?native=1 exists even for inject:false tiles and tiles
// with no index.html, so without the rule a tile could xbin.fetch
// '/c/<other>/?native=1' and lift a credential that tile never published.
// Only a human or the tile itself (its own frame, or an xbin.window
// sub-path of it) gets one.
func TestNativeRuntimeNeverMintsAnotherTilesToken(t *testing.T) {
	s, a := nativeWorkspace(t)
	tokRe := regexp.MustCompile(`name="xbin-frame-token" content="([^"]*)"`)
	tokenIn := func(w *httptest.ResponseRecorder) string {
		t.Helper()
		m := tokRe.FindStringSubmatch(w.Body.String())
		if w.Code != 200 || m == nil {
			t.Fatalf("%d, no frame-token meta:\n%s", w.Code, w.Body.String())
		}
		return m[1]
	}
	targets := []string{
		"/c/apps/raw/?native=1",  // inject:false: its page carries no token
		"/c/apps/decl/?native=1", // no index.html at all
		"/c/apps/conv/?native=1",
		"/c/apps/conv/",
	}
	boss := &users.User{ID: "boss", Role: users.RoleAdmin}
	for _, p := range []auth.Principal{
		{Component: "apps/web", Via: "frame"},                             // owner-driven frame: owner reach
		{Component: "apps/web", Via: "frame", UserID: "boss", User: boss}, // an admin's frame
		{Component: "apps/web/sub", Via: "frame"},                         // a window of another tile
	} {
		for _, url := range targets {
			if tok := tokenIn(serveAs(s, "GET", url, p, nil)); tok != "" {
				c, u, _ := a.VerifyFrameToken(tok)
				t.Errorf("%s as %+v: minted %s's token (uid %q)", url, p, c, u)
			}
		}
	}

	// Through the real middleware, as the review's scenario: apps/web's
	// owner-driven frame token fetching apps/raw's runtime document.
	h := s.authedStatic(http.HandlerFunc(s.handleComponentStatic))
	fetch := func(url, frameComp string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set(auth.FrameTokenHeader, a.MintFrameToken(frameComp, "", time.Minute))
		r.Header.Set("Sec-Fetch-Site", "cross-site") // xbin.fetch out of an opaque origin
		r.Header.Set("Sec-Fetch-Mode", "cors")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, url := range targets {
		if tok := tokenIn(fetch(url, "apps/web")); tok != "" {
			t.Errorf("%s via apps/web's frame token: got a token", url)
		}
	}

	// The tile itself still gets its own (the app loads its runtime document
	// with that tile's token), and so does a sub-path window of it.
	for _, c := range []struct{ url, frame, want string }{
		{"/c/apps/raw/?native=1", "apps/raw", "apps/raw"},
		{"/c/apps/decl/?native=1", "apps/decl", "apps/decl"},
		{"/c/apps/conv/?native=1", "apps/conv", "apps/conv"},
		{"/c/apps/conv/?native=1", "apps/conv/editor", "apps/conv"},
	} {
		comp, uid, ok := a.VerifyFrameToken(tokenIn(fetch(c.url, c.frame)))
		if !ok || comp != c.want || uid != "" {
			t.Errorf("%s as %s: token for %q uid %q ok %v, want %s", c.url, c.frame, comp, uid, ok, c.want)
		}
	}
	// A human (cookie/bearer principal) keeps getting one, as before.
	if tokenIn(serveAs(s, "GET", "/c/apps/raw/?native=1", auth.Principal{Owner: true, Via: "bearer"}, nil)) == "" {
		t.Fatal("the owner lost the runtime document's token")
	}
}

// A tile's backend never gets a frame token (review): its instance token
// names no user, so a token minted for it would verify as an owner-driven
// frame — owner reach on every tile — while the instance itself is
// self-only. Neither the <head> injection (a tile page, the runtime
// document, a tile with no index.html) nor /api/xbin/frame-token mints one
// for it, through the real middleware.
func TestBackendNeverGetsAFrameToken(t *testing.T) {
	s, a := nativeWorkspace(t)
	a.RegisterInstance("conv-instance", "apps/conv")
	a.RegisterInstance("decl-instance", "apps/decl")
	tokRe := regexp.MustCompile(`name="xbin-frame-token" content="([^"]*)"`)
	static := s.authedStatic(http.HandlerFunc(s.handleComponentStatic))
	api := s.authed(http.HandlerFunc(s.apiFrameToken))
	do := func(h http.Handler, url, bearer string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set("Authorization", "Bearer "+bearer)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := do(static, "/c/apps/web/", "conv-instance"); w.Code != http.StatusForbidden {
		t.Fatalf("an instance token read another tile's page: %d", w.Code)
	}
	for _, c := range []struct{ url, tok string }{
		{"/c/apps/conv/", "conv-instance"},
		{"/c/apps/conv/?native=1", "conv-instance"},
		{"/c/apps/decl/?native=1", "decl-instance"}, // no index.html
	} {
		w := do(static, c.url, c.tok)
		m := tokRe.FindStringSubmatch(w.Body.String())
		if w.Code != http.StatusOK || m == nil {
			t.Fatalf("%s: %d, no frame-token meta", c.url, w.Code)
		}
		if m[1] != "" {
			comp, uid, _ := a.VerifyFrameToken(m[1])
			t.Errorf("%s: the backend got a frame token (%s, uid %q)", c.url, comp, uid)
		}
	}
	for _, c := range []string{"apps/conv", "apps/decl"} {
		tok := strings.SplitN(c, "/", 2)[1] + "-instance"
		if w := do(api, "/api/xbin/frame-token?component="+c, tok); w.Code != http.StatusForbidden {
			t.Errorf("GET /frame-token?component=%s as its backend: %d %s", c, w.Code, w.Body.String())
		}
	}
	// The tile's own frame and a human keep theirs.
	if w := serveAs(s, "GET", "/c/apps/conv/", auth.Principal{Component: "apps/conv", Via: "frame"}, nil); tokRe.FindStringSubmatch(w.Body.String())[1] == "" {
		t.Error("the tile's own frame lost its token")
	}
	if w := serveAs(s, "GET", "/c/apps/conv/", auth.Principal{Component: "apps/conv", Via: "terminal"}, nil); tokRe.FindStringSubmatch(w.Body.String())[1] == "" {
		t.Error("the tile's own terminal lost its token")
	}
	if w := do(api, "/api/xbin/frame-token?component=apps/conv", a.OwnerTokenValue()); w.Code != http.StatusOK {
		t.Errorf("the owner's renewal: %d", w.Code)
	}
}

// Documents requested by the xbin app (X-XBin-Client: app/<v>) carry the
// WebSocket origin of the host the app reached; browsers' documents are
// unchanged. Both tile pages and runtime documents get it.
func TestAppWSOriginMeta(t *testing.T) {
	s, _ := nativeWorkspace(t)
	owner := auth.Principal{Owner: true}
	req := func(url string, hdr map[string]string, tlsOn bool, host string) string {
		r := httptest.NewRequest("GET", url, nil)
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		if tlsOn {
			r.TLS = &tls.ConnectionState{}
		}
		if host != "" {
			r.Host = host
		}
		r = r.WithContext(auth.WithPrincipal(r.Context(), owner))
		w := httptest.NewRecorder()
		s.handleComponentStatic(w, r)
		return w.Body.String()
	}
	app := map[string]string{AppClientHeader: "app/1.0.3"}
	cases := []struct {
		name, url string
		hdr       map[string]string
		tls       bool
		host      string
		want      string // "" = no meta
	}{
		{"browser page", "/c/apps/conv/", nil, false, "", ""},
		{"browser runtime doc", "/c/apps/conv/?native=1", nil, false, "", ""},
		{"app page, http", "/c/apps/conv/", app, false, "", "ws://example.com"},
		{"app runtime doc, tls", "/c/apps/conv/?native=1", app, true, "xbin.example.org", "wss://xbin.example.org"},
		{"app behind a tls proxy", "/c/apps/web/", map[string]string{AppClientHeader: "app/2", "X-Forwarded-Proto": "https"}, false, "ws.example:8443", "wss://ws.example:8443"},
		{"app, ipv6", "/c/apps/web/", app, false, "[::1]:9988", "ws://[::1]:9988"},
		{"not the app", "/c/apps/web/", map[string]string{AppClientHeader: "cli/1"}, false, "", ""},
		{"hostile host", "/c/apps/web/", app, false, `x"><script>alert(1)</script>`, ""},
	}
	for _, c := range cases {
		body := req(c.url, c.hdr, c.tls, c.host)
		m := regexp.MustCompile(`<meta name="xbin-ws-origin" content="([^"]*)">`).FindStringSubmatch(body)
		switch {
		case c.want == "" && m != nil:
			t.Errorf("%s: unexpected ws origin %q", c.name, m[1])
		case c.want != "" && (m == nil || m[1] != c.want):
			t.Errorf("%s: ws origin %v, want %q\n%s", c.name, m, c.want, body)
		}
	}
	// A browser's page is byte-for-byte what it always was: the injection
	// ends with xbin-client right after the sandbox meta.
	body := req("/c/apps/web/", nil, false, "")
	if !strings.Contains(body, "allow-downloads\">\n<script type=\"module\" src=\"/vendor/xbin-client.js\"></script>\n<title>t</title>") {
		t.Fatalf("browser injection changed:\n%s", body)
	}
}

// /components (and /components/<path>) advertise a native entry per tile,
// so the app knows before opening one; tiles without one — and trusted
// chrome — carry no `native`.
func TestComponentsNative(t *testing.T) {
	s, _ := nativeWorkspace(t)
	r := httptest.NewRequest("GET", "/api/xbin/components", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	s.apiComponents(w, r)
	var list []struct {
		Path   string          `json:"path"`
		Native json.RawMessage `json:"native"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, c := range list {
		got[c.Path] = string(c.Native)
	}
	want := map[string]string{
		"apps/conv":    `{"entry":"native.js"}`,
		"apps/decl":    `{"entry":"mobile/main.js"}`,
		"apps/linky":   `{"entry":"native.js"}`,
		"apps/raw":     `{"entry":"native.js"}`,
		"apps/web":     "",
		"apps/gone":    "",
		"tiles/chrome": "",
	}
	for p, wv := range want {
		if got[p] != wv {
			t.Errorf("%s: native = %s, want %s", p, got[p], wv)
		}
	}

	r = httptest.NewRequest("GET", "/api/xbin/components/apps/decl", nil)
	r.SetPathValue("path", "apps/decl")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w = httptest.NewRecorder()
	s.apiComponent(w, r)
	var one struct {
		Component struct {
			Native *nativeInfo `json:"native"`
		} `json:"component"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil || one.Component.Native == nil || one.Component.Native.Entry != "mobile/main.js" {
		t.Fatalf("component detail: %v %s", err, w.Body.String())
	}
}

func TestNativeRuntimeRequest(t *testing.T) {
	for _, c := range []struct {
		method, url string
		want        bool
	}{
		{"GET", "/c/x/?native=1", true},
		{"HEAD", "/c/x/?native=1", true},
		{"GET", "/c/x/?preview=1&native=1", true},
		{"POST", "/c/x/?native=1", false},
		{"GET", "/c/x/?native=true", false},
		{"GET", "/c/x/", false},
	} {
		if got := nativeRuntimeRequest(httptest.NewRequest(c.method, c.url, nil)); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.url, got, c.want)
		}
	}
}
