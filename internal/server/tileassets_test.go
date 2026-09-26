package server

import (
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

// assetWS is a workspace for the strict asset-gating tests: two readable
// tiles, one secret one, an inject:false tile, chrome, and a symlink trying
// to leave its tile.
type assetWS struct {
	t    *testing.T
	s    *Server
	h    http.Handler
	a    *auth.Auth
	st   *users.Store
	root string
}

const assetPage = `<!doctype html><html><head><title>a</title></head><body>a</body></html>`

func newAssetWS(t *testing.T, mode string) *assetWS {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"xbin.json":             `{"importMap":{"lit":"/vendor/lit-all.min.js","@lib/":"/c/apps/b/"}}`,
		"apps/a/xbin.json":      `{}`,
		"apps/a/index.html":     assetPage,
		"apps/a/app.js":         `import './dep.js';`,
		"apps/a/dep.js":         `export const x = 1;`,
		"apps/a/style.css":      `body{background:url(img.svg)}`,
		"apps/a/img.svg":        `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`,
		"apps/a/doc.pdf":        `%PDF-1.4`,
		"apps/a/sub/page.html":  assetPage,
		"apps/a/sub/based.html": `<!doctype html><html><head><base href="../assets/"></head><body></body></html>`,
		"apps/a/offsite.html":   `<!doctype html><html><head><base href="https://cdn.example/x/"></head><body></body></html>`,
		"apps/a/abs.html":       `<!doctype html><html><head></head><body><img src="/c/apps/a/img.svg"></body></html>`,
		"apps/b/xbin.json":      `{}`,
		"apps/b/lib.js":         `export const lib = 1;`,
		"apps/b/index.html":     assetPage,
		"apps/secret/xbin.json": `{}`,
		"apps/secret/s.js":      `const key = "hunter2";`,
		"apps/raw/xbin.json":    `{"inject":false}`,
		"apps/raw/index.html":   assetPage,
		"apps/raw/r.js":         `1`,
		"shell/index.html":      assetPage,
		"shell/shell.js":        `1`,
	}
	for rel, c := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("../../.xbin/secret", filepath.Join(root, "apps/a/leak.txt")); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	for _, u := range []users.User{
		{ID: "ana", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelRead, "apps/b": users.LevelRead, "apps/raw": users.LevelRead}},
		{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/b": users.LevelRead}},
	} {
		if _, err := st.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	s := &Server{Reg: reg, Auth: a, TileAssets: mode, ExternalURL: "http://xbin.localhost:9260"}
	if mode == TileAssetsOrigins {
		s.TilesDomain = "xbin.localhost"
	}
	a.SetClientIP(s.ClientIP)
	return &assetWS{t: t, s: s, a: a, st: st, root: root} // h: built on first use
}

type reqOpt func(*http.Request)

func hdr(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func cookie(name, v string) reqOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: name, Value: v}) }
}
func host(h string) reqOpt   { return func(r *http.Request) { r.Host = h } }
func method(m string) reqOpt { return func(r *http.Request) { r.Method = m } }

// subresource: what a sandboxed frame's tag load looks like.
var subresource = []reqOpt{hdr("Sec-Fetch-Site", "cross-site"), hdr("Sec-Fetch-Dest", "script"), hdr("Sec-Fetch-Mode", "no-cors")}

func (w *assetWS) do(url string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", url, nil)
	r.Host = "xbin.localhost:9260"
	for _, o := range opts {
		o(r)
	}
	rec := httptest.NewRecorder()
	if w.h == nil {
		w.h = w.s.Handler()
	}
	w.h.ServeHTTP(rec, r)
	return rec
}

func (w *assetWS) session(uid string) reqOpt {
	return cookie(auth.CookieName, w.a.NewSession(uid, "192.0.2.1"))
}

func (w *assetWS) frame(comp, uid string) reqOpt {
	return hdr(auth.FrameTokenHeader, w.a.MintFrameToken(comp, uid, time.Minute))
}

var baseHref = regexp.MustCompile(`<base href="/c/~([^/"]+)/([^"]*)" data-xbin-assets>`)

// assetTokenFrom returns the asset token and <base> path in a served
// tokens-mode document.
func assetTokenFrom(t *testing.T, body string) (string, string) {
	t.Helper()
	m := baseHref.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no asset <base> in document:\n%s", body)
	}
	return m[1], m[2]
}

// Strict modes: every /c/ request without a credential is refused —
// documents, subresources, every file type — including a perfectly forged
// Fetch-Metadata fingerprint from an IP with a live session (the legacy
// heuristic's exact admission ticket).
func TestStrictNoCredentialLessPath(t *testing.T) {
	for _, mode := range []string{TileAssetsTokens, TileAssetsOrigins} {
		w := newAssetWS(t, mode)
		w.a.NewSession("ana", "192.0.2.1") // warms httptest's peer IP
		for _, p := range []string{"/c/apps/a/app.js", "/c/apps/a/style.css", "/c/apps/a/img.svg", "/c/apps/a/doc.pdf", "/c/apps/a/", "/c/apps/a/index.html", "/c/apps/raw/r.js"} {
			if rec := w.do(p, subresource...); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s forged subresource: %d, want 401", mode, p, rec.Code)
			}
			if rec := w.do(p); rec.Code != http.StatusUnauthorized {
				t.Errorf("%s %s bare: %d, want 401", mode, p, rec.Code)
			}
		}
		if rec := w.do("/c/apps/a/app.js", subresource...); !strings.Contains(rec.Body.String(), "relative URLs") {
			t.Errorf("%s: the refusal should say what to do: %q", mode, rec.Body.String())
		}
	}
	// Legacy keeps today's rule (release N): the warm-IP fingerprint passes.
	w := newAssetWS(t, TileAssetsLegacy)
	w.a.NewSession("ana", "192.0.2.1")
	if rec := w.do("/c/apps/a/app.js", subresource...); rec.Code != 200 {
		t.Fatalf("legacy warm-IP subresource: %d", rec.Code)
	}
}

// Legacy is byte-for-byte today's injection: no <base>, no mode meta, the
// import map first, the self entry absent.
func TestLegacyInjectionUnchanged(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	rec := w.do("/c/apps/a/", w.session("ana"))
	body := rec.Body.String()
	if !strings.Contains(body, "<head>\n<script type=\"importmap\">") {
		t.Fatalf("legacy injection moved: %s", body)
	}
	for _, bad := range []string{"<base", "xbin-tile-assets", "/c/~", `"/c/apps/a/"`} {
		if strings.Contains(body, bad) {
			t.Errorf("legacy document carries %q", bad)
		}
	}
	if rec := w.do("/c/apps/a/img.svg", w.session("ana")); rec.Header().Get("Content-Security-Policy") != "" {
		t.Error("legacy non-document responses gained a CSP")
	}
	if s := (&Server{}); s.tileOriginURL("apps/a") != "" || s.strictAssets() {
		t.Error("the zero Server is legacy")
	}
}

// Tokens mode: the injected <base> and import map carry an asset token;
// relative loads (and module/CSS-relative ones, which resolve under the same
// prefix) go through; the token path never serves HTML, directories, chrome
// or document destinations, and is read-only.
func TestTokensModeInjectionAndPlane(t *testing.T) {
	w := newAssetWS(t, TileAssetsTokens)
	rec := w.do("/c/apps/a/", w.session("ana"))
	body := rec.Body.String()
	tok, base := assetTokenFrom(t, body)
	if base != "apps/a/" {
		t.Fatalf("base %q", base)
	}
	if !strings.Contains(body, `"/c/apps/a/":"/c/~`+tok+`/apps/a/"`) || !strings.Contains(body, `"@lib/":"/c/~`+tok+`/apps/b/"`) {
		t.Fatalf("import map not remapped: %s", body)
	}
	if !strings.Contains(body, `<meta name="xbin-tile-assets" content="tokens">`) {
		t.Fatal("no mode meta")
	}
	if strings.Index(body, "<base") > strings.Index(body, "importmap") {
		t.Fatal("<base> must precede the import map")
	}
	g, ok := w.a.VerifyAssetToken(tok)
	if !ok || g.Tile != "apps/a" || g.UserID != "ana" {
		t.Fatalf("token %+v %v", g, ok)
	}

	at := "/c/~" + tok + "/"
	for _, p := range []string{"apps/a/app.js", "apps/a/dep.js", "apps/a/style.css", "apps/a/img.svg", "apps/a/doc.pdf"} {
		rec := w.do(at+p, subresource...)
		if rec.Code != 200 {
			t.Errorf("%s via token: %d %s", p, rec.Code, rec.Body.String())
			continue
		}
		if rec.Header().Get("Content-Security-Policy") != "sandbox" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers %v", p, rec.Header())
		}
	}
	// Documents, directories, chrome, navigations, writes: never.
	for _, c := range []struct {
		p    string
		opts []reqOpt
		want int
	}{
		{"apps/a/index.html", nil, 403},
		{"apps/a/sub/page.html", nil, 403},
		{"apps/a/", nil, 403},
		{"apps/a/sub", nil, 403},
		{"shell/shell.js", nil, 403},
		{"apps/a/img.svg", []reqOpt{hdr("Sec-Fetch-Dest", "document"), hdr("Sec-Fetch-Mode", "navigate")}, 403},
		{"apps/a/doc.pdf", []reqOpt{hdr("Sec-Fetch-Dest", "iframe")}, 403},
		{"apps/a/app.js", []reqOpt{method("POST")}, 405},
		{"apps/a/leak.txt", nil, 404}, // symlink out of the tile
		{".xbin/secret", nil, 400},
		{"apps/a/../../.xbin/secret", nil, 307}, // the mux cleans, then the above
	} {
		if rec := w.do(at+c.p, c.opts...); rec.Code != c.want {
			t.Errorf("token path %s: %d, want %d", c.p, rec.Code, c.want)
		}
	}
	// The token authenticates nothing but the asset plane.
	if rec := w.do("/api/xbin/frame-token?component=apps/a", hdr(auth.FrameTokenHeader, tok)); rec.Code != 401 {
		t.Errorf("asset token as a frame token on /api: %d", rec.Code)
	}
	if rec := w.do("/c/apps/a/app.js", hdr(auth.FrameTokenHeader, tok)); rec.Code != 401 {
		t.Errorf("asset token as a frame token on /c/: %d", rec.Code)
	}
	if rec := w.do("/c/~garbage/apps/a/app.js"); rec.Code != 401 {
		t.Errorf("garbage token: %d", rec.Code)
	}
	// A document with its own <base>: ours stands for it (token form), or is
	// left out when it points off /c/.
	_, b := assetTokenFrom(t, w.do("/c/apps/a/sub/based.html", w.session("ana")).Body.String())
	if b != "apps/a/assets/" {
		t.Errorf("own relative <base>: ours points at %q", b)
	}
	if body := w.do("/c/apps/a/offsite.html", w.session("ana")).Body.String(); strings.Contains(body, "data-xbin-assets") {
		t.Error("an off-site <base> must not be overridden")
	}
	// Chrome is never gated: the shell's document gets no asset <base>.
	if body := w.do("/c/shell/", w.session("ana")).Body.String(); strings.Contains(body, "<base") || strings.Contains(body, "xbin-tile-assets") {
		t.Errorf("chrome document got the asset injection:\n%s", body)
	}
	// A sub-directory document's base is its own directory.
	if _, b := assetTokenFrom(t, w.do("/c/apps/a/sub/page.html", w.session("ana")).Body.String()); b != "apps/a/sub/" {
		t.Errorf("sub/page.html base %q", b)
	}
}

// Tokens mode, RBAC: cross-tile loads work exactly when the user can read
// the other tile; the user loses access → the next request fails; another
// user's/another tile's token doesn't widen anything; a revoked credential
// generation kills the token.
func TestTokensModeRBAC(t *testing.T) {
	w := newAssetWS(t, TileAssetsTokens)
	tok, _ := assetTokenFrom(t, w.do("/c/apps/a/", w.session("ana")).Body.String())
	at := "/c/~" + tok + "/"
	if rec := w.do(at + "apps/b/lib.js"); rec.Code != 200 {
		t.Fatalf("cross-tile load ana can read: %d", rec.Code)
	}
	if rec := w.do(at + "apps/secret/s.js"); rec.Code != 403 {
		t.Fatalf("cross-tile load ana cannot read: %d", rec.Code)
	}
	// bob's token for apps/b never reaches apps/a (bob can't read it).
	bobTok, _ := assetTokenFrom(t, w.do("/c/apps/b/", w.session("bob")).Body.String())
	if rec := w.do("/c/~" + bobTok + "/apps/a/app.js"); rec.Code != 403 {
		t.Fatalf("another user's token read an unreadable tile: %d", rec.Code)
	}
	// RBAC change → next request.
	u, _ := w.st.Get("ana")
	delete(u.Tiles, "apps/b")
	if _, err := w.st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(at + "apps/b/lib.js"); rec.Code != 403 {
		t.Fatalf("revoked cross-tile read still served: %d", rec.Code)
	}
	delete(u.Tiles, "apps/a")
	if _, err := w.st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(at + "apps/a/app.js"); rec.Code != 403 {
		t.Fatalf("token of a tile the user lost still served: %d", rec.Code)
	}
	// Revoked session generation.
	gen := "g1"
	w.a.SetCredentialGeneration(func(string) string { return gen })
	btok, _ := assetTokenFrom(t, w.do("/c/apps/b/", w.session("bob")).Body.String())
	if rec := w.do("/c/~" + btok + "/apps/b/lib.js"); rec.Code != 200 {
		t.Fatalf("fresh token: %d", rec.Code)
	}
	gen = "g2"
	if rec := w.do("/c/~" + btok + "/apps/b/lib.js"); rec.Code != 401 {
		t.Fatalf("token of a revoked session: %d", rec.Code)
	}
}

// Strict modes re-check the driving user for a tile's own frame principal:
// the element self-pass must not keep a revoked user's tile loading.
func TestStrictFrameSelfReadIsLive(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		w := newAssetWS(t, mode)
		u, _ := w.st.Get("ana")
		delete(u.Tiles, "apps/a")
		if _, err := w.st.Upsert(*u, ""); err != nil {
			t.Fatal(err)
		}
		rec := w.do("/c/apps/a/app.js", w.frame("apps/a", "ana"))
		want := 403
		if mode == TileAssetsLegacy {
			want = 200 // today's element self-pass, unchanged in legacy
		}
		if rec.Code != want {
			t.Errorf("%s: revoked user's frame token on its own tile: %d, want %d", mode, rec.Code, want)
		}
	}
}

// Strict modes on the workspace origin: a sandboxed tile's non-document
// files carry CSP sandbox (an SVG navigated to must not run as the
// workspace origin), PDFs excepted; chrome is untouched; symlinks never
// leave their tile.
func TestStrictWorkspaceOriginHardening(t *testing.T) {
	w := newAssetWS(t, TileAssetsTokens)
	if rec := w.do("/c/apps/a/img.svg", w.session("ana")); rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "sandbox" {
		t.Fatalf("svg: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
	if rec := w.do("/c/apps/a/doc.pdf", w.session("ana")); rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("pdf got CSP sandbox (browsers refuse to render it)")
	}
	if rec := w.do("/c/shell/shell.js", w.session("ana")); rec.Code != 200 || rec.Header().Get("Content-Security-Policy") != "" {
		t.Fatalf("chrome: %d %v", rec.Code, rec.Header())
	}
	if rec := w.do("/c/apps/a/leak.txt", w.session("ana")); rec.Code != 404 || strings.Contains(rec.Body.String(), string(mustRead(t, filepath.Join(w.root, ".xbin/secret")))) {
		t.Fatalf("symlink escape served: %d", rec.Code)
	}
	// inject:false documents are served (sandboxed), never tokenized.
	rec := w.do("/c/apps/raw/", w.session("ana"))
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "<base") || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), "sandbox allow-scripts") {
		t.Fatalf("inject:false: %d %q", rec.Code, rec.Header().Get("Content-Security-Policy"))
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.TrimSpace(string(b)))
}

// The tile asset report (detection): read-filtered, per sandboxed tile,
// with the fix bx fix assets would write; chrome is never listed.
func TestTileAssetsReport(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	get := func(url string, p auth.Principal) (int, map[string]any) {
		r := httptest.NewRequest("GET", url, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		rec := httptest.NewRecorder()
		w.s.apiTileAssets(rec, r)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	acc, _ := w.st.Access("ana")
	ana := auth.Principal{UserID: "ana", Access: acc, Via: "session"}
	code, out := get("/api/xbin/tile-assets", ana)
	if code != 200 || out["mode"] != "legacy" {
		t.Fatalf("%d %v", code, out)
	}
	tiles := map[string]map[string]any{}
	for _, x := range out["tiles"].([]any) {
		m := x.(map[string]any)
		tiles[m["component"].(string)] = m
	}
	a, ok := tiles["apps/a"]
	if !ok {
		t.Fatalf("apps/a missing: %v", out)
	}
	if _, ok := tiles["apps/secret"]; ok {
		t.Fatal("report lists a tile the caller can't read")
	}
	if _, ok := tiles["shell"]; ok {
		t.Fatal("chrome listed")
	}
	if !tiles["apps/raw"]["injectFalse"].(bool) {
		t.Fatal("inject:false not reported")
	}
	var sawFix, sawLink bool
	for _, f := range a["findings"].([]any) {
		m := f.(map[string]any)
		if m["file"] == "abs.html" && m["fix"] == "img.svg" && m["breaks"] == "tokens" {
			sawFix = true
		}
		if m["kind"] == "symlink-escape" && m["file"] == "leak.txt" {
			sawLink = true
		}
	}
	if !sawFix || !sawLink {
		t.Fatalf("apps/a findings: %v", a["findings"])
	}
	if b := a["breaking"].(map[string]any); b["tokens"].(float64) < 2 || b["origins"].(float64) != 1 {
		t.Fatalf("breaking: %v", b)
	}
	if code, _ := get("/api/xbin/tile-assets?component=apps/secret", ana); code != 404 {
		t.Fatalf("unreadable tile by name: %d", code)
	}
	if code, out := get("/api/xbin/tile-assets?component=apps/b", ana); code != 200 || len(out["tiles"].([]any)) != 1 {
		t.Fatalf("a clean tile by name is listed: %d %v", code, out)
	}
}

// The legacy plane too refuses a symlink that resolves to xbind's own
// credentials or outside the workspace (tile directories are written by
// sandboxes); a symlink to another tile's file keeps working there.
func TestLegacySymlinkTargets(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	if err := os.Symlink("../b/lib.js", filepath.Join(w.root, "apps/a/shared.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(w.root, "apps/a/host.txt")); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int{"/c/apps/a/leak.txt": 404, "/c/apps/a/host.txt": 404, "/c/apps/a/shared.js": 200, "/c/apps/a/app.js": 200} {
		if rec := w.do(p, w.session("ana")); rec.Code != want {
			t.Errorf("%s: %d, want %d", p, rec.Code, want)
		}
	}
}
