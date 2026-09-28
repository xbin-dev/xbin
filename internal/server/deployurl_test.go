package server

// deployurl_test.go — deployment URLs on the /c/ static and asset-token
// planes (11-contract §2.2–§2.7, §7.2; 07-runtime §4.3–§4.6).

import (
	"context"
	"fmt"
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
	"github.com/xbin-dev/xbin/internal/util"
)

// urlPolicy is testPolicy plus the answers a deployments plane gives for
// the tiles with a record: each deployment's code (its materialized root,
// or "" while it follows the work tree), the primary, protection, and the
// deployment a principal is bound to, as Plane.Addressed answers it.
type urlPolicy struct {
	testPolicy
	recs map[string]*urlRecord
}

type urlRecord struct {
	primary   string
	roots     map[string]string // deployment → materialized root; "" follows the work tree
	protected bool
}

var (
	_ AddressedPolicy      = (*urlPolicy)(nil)
	_ PrimaryPolicy        = (*urlPolicy)(nil)
	_ PrimarySummaryPolicy = (*urlPolicy)(nil)
)

func (p *urlPolicy) CodeRoot(c *registry.Component, dep string) (string, bool, error) {
	r := p.recs[c.Path]
	if r == nil {
		return NoopPolicy{}.CodeRoot(c, dep)
	}
	if dep == "" {
		dep = r.primary
	}
	root, ok := r.roots[dep]
	switch {
	case !ok:
		return "", false, util.NoDeployment(c.Path, dep)
	case root == "":
		return c.Dir, false, nil
	}
	return root, true, nil
}

func (p *urlPolicy) HasDeployment(tile, name string) bool {
	if r := p.recs[tile]; r != nil {
		_, ok := r.roots[name]
		return ok
	}
	return name == util.MainDeployment
}

func (p *urlPolicy) PrimarySummary(tile string) (string, bool, bool, bool) {
	r := p.recs[tile]
	if r == nil {
		return "", false, false, false
	}
	return r.primary, r.roots[r.primary] != "", r.protected, true
}

func (p *urlPolicy) Primary(tile string) string {
	if r := p.recs[tile]; r != nil {
		return r.primary
	}
	return util.MainDeployment
}

// Addressed is Plane.Addressed over the records.
func (p *urlPolicy) Addressed(pr auth.Principal, tile string) (string, error) {
	primary, protected := p.Primary(tile), false
	if r := p.recs[tile]; r != nil {
		protected = r.protected
	}
	if pr.Component != tile {
		return primary, nil
	}
	session, dep := pr.Via == "terminal", pr.Deployment
	switch {
	case dep == "" && !session:
		return util.MainDeployment, nil
	case dep == "":
		dep = primary
	case !p.HasDeployment(tile, dep):
		return "", util.NoDeployment(tile, dep)
	}
	if session && protected && dep == primary {
		return "", fmt.Errorf("the primary of %s is protected: terminal and agent sessions can't target it", tile)
	}
	return dep, nil
}

// depWS is assetWS with deployment records: apps/a has main on its work
// tree and dev pinned to devFiles; apps/c has dev too, while a tile of its
// own lives at apps/c+dev; tiles/organisations, shipped chrome, has a dev
// whose code asks for chrome. wes writes apps/a, tia has terminal level on
// it, ada is an admin; ana reads it, as in assetWS.
type depWS struct {
	*assetWS
	pol *urlPolicy
}

// the code apps/a's dev deployment is pinned to.
var devFiles = map[string]string{
	"xbin.json":     `{}`,
	"index.html":    `<!doctype html><html><head><title>dev</title></head><body>dev page</body></html>`,
	"app.js":        `export const dev = true;`,
	"native.js":     `export default function tile() {}`,
	"sub/page.html": `<!doctype html><html><head></head><body>dev sub page</body></html>`,
	"deps/.keep":    ``,
}

func newDepWS(t *testing.T, mode string) *depWS {
	t.Helper()
	w := &depWS{assetWS: newAssetWS(t, mode), pol: &urlPolicy{recs: map[string]*urlRecord{}}}
	writeWS(t, w.root, map[string]string{
		"apps/a/nested/xbin.json":        `{}`,
		"apps/a/nested/n.js":             `nested`,
		"apps/c/xbin.json":               `{}`,
		"apps/c/index.html":              assetPage,
		"apps/c+dev/xbin.json":           `{}`,
		"apps/c+dev/index.html":          `<!doctype html><html><head></head><body>a tile named apps/c+dev</body></html>`,
		"tiles/organisations/xbin.json":  `{"chrome":true}`,
		"tiles/organisations/index.html": assetPage,
		"root/index.html":                assetPage,
	})
	for _, u := range []users.User{
		{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelWrite, "apps/b": users.LevelRead}},
		{ID: "tia", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelTerminal}},
		{ID: "ada", Role: users.RoleAdmin},
	} {
		if _, err := w.st.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	w.s.Pol = w.pol
	w.s.Reg.PinnedPrimary = func(rel string) (*registry.PinnedCode, bool) {
		r := w.pol.recs[rel]
		if r == nil || r.roots[r.primary] == "" {
			return nil, false
		}
		pc, err := registry.ReadCheckpoint(r.roots[r.primary])
		return pc, err == nil
	}
	devRoot := w.checkpoint("apps/a", "dev", devFiles, map[string]string{"deps/b": "../../b", "deps/self": "../../a"})
	w.pol.recs["apps/a"] = &urlRecord{primary: util.MainDeployment, roots: map[string]string{util.MainDeployment: "", "dev": devRoot}}
	w.pol.recs["apps/c"] = &urlRecord{primary: util.MainDeployment, roots: map[string]string{util.MainDeployment: "",
		"dev": w.checkpoint("apps/c", "dev", map[string]string{"index.html": "c dev"}, nil)}}
	w.pol.recs["tiles/organisations"] = &urlRecord{primary: util.MainDeployment, roots: map[string]string{util.MainDeployment: "",
		"dev": w.checkpoint("tiles/organisations", "dev", map[string]string{"xbin.json": `{"chrome":true}`,
			"index.html": `<!doctype html><html><head></head><body>org dev</body></html>`, "raw.html": `<p>raw</p>`,
			"native.js": `export default 1`}, nil)}}
	w.rescan()
	return w
}

// checkpoint materializes files and links as a checkpoint of tile under
// .xbin/deploy, its directory named as a tree, and returns its root.
func (w *depWS) checkpoint(tile, name string, files, links map[string]string) string {
	w.t.Helper()
	root := filepath.Join(w.root, ".xbin", "deploy", strings.ReplaceAll(tile, "/", "_"), name+"-tree")
	if err := os.RemoveAll(root); err != nil {
		w.t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		w.t.Fatal(err)
	}
	writeWS(w.t, root, files)
	for l, text := range links {
		p := filepath.Join(root, filepath.FromSlash(l))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			w.t.Fatal(err)
		}
		if err := os.Symlink(text, p); err != nil {
			w.t.Fatal(err)
		}
	}
	return root
}

func (w *depWS) rescan() {
	w.t.Helper()
	if err := w.s.Reg.Rescan(); err != nil {
		w.t.Fatal(err)
	}
}

// today answers url as this server does without any deployment record.
func (w *depWS) today(url string, opts ...reqOpt) *httptest.ResponseRecorder {
	w.s.Pol = w.pol.testPolicy
	defer func() { w.s.Pol = w.pol }()
	return w.do(url, opts...)
}

func (w *depWS) devFrame(uid string) reqOpt {
	return hdr(auth.FrameTokenHeader, w.a.MintFrameTokenDeployment("apps/a", uid, "dev", time.Minute))
}

func (w *depWS) terminal(uid, target string) reqOpt {
	return hdr("Authorization", "Bearer "+w.a.MintTerminalTarget("apps/a", uid, target))
}

var frameTokenMeta = regexp.MustCompile(`<meta name="xbin-frame-token" content="([^"]*)">`)

// frameTokenIn is the frame token injected into a served document ("" when
// none was minted).
func frameTokenIn(t *testing.T, body string) string {
	t.Helper()
	m := frameTokenMeta.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no frame-token meta in:\n%s", body)
	}
	return m[1]
}

// same reports whether two answers agree on status, body (frame tokens
// masked) and Location.
func same(a, b *httptest.ResponseRecorder) bool {
	mask := func(s string) string { return frameTokenMeta.ReplaceAllString(s, "<frame-token>") }
	return a.Code == b.Code && mask(a.Body.String()) == mask(b.Body.String()) && a.Header().Get("Location") == b.Header().Get("Location")
}

// covers D127d D127j D127k — /c/<tile>+<name>/ serves that deployment's code:
// pinned (a checkpoint, its deps/ links re-dispatched to the other tile's
// primary) or following the work tree; /c/<tile>/ and the alias
// /c/<tile>+<primary>/ serve the primary, which may be another deployment
// than main. A qualified URL resolves only for a tile with a record and
// only after today's resolution fails (a tile at apps/c+dev wins); an
// unknown deployment is a 404, a bad name or a zero-state tile answers as
// today, /c/root+dev/ is a 404, a path into a nested tile is a 404, and a
// missing slash redirects as today. The document carries its deployment and
// keeps its self-imports in it, in legacy and tokens mode, whose asset
// token serves the deployment's files to a user who writes the tile only;
// ?native=1 is generated from the deployment's code; origins mode sends a
// document navigation to the deployment's own origin
// (TestOriginLabelPerDeployment).
func TestQualifiedURLRouting(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	wes := w.session("wes")

	dev := w.do("/c/apps/a+dev/", wes)
	body := dev.Body.String()
	if dev.Code != 200 || !strings.Contains(body, "dev page") {
		t.Fatalf("/c/apps/a+dev/: %d %s", dev.Code, body)
	}
	for _, want := range []string{`<meta name="xbin-component" content="apps/a">`, `<meta name="xbin-deployment" content="dev">`, `"/c/apps/a/":"/c/apps/a+dev/"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dev document lacks %s:\n%s", want, body)
		}
	}
	if rec := w.do("/c/apps/a+dev/app.js", wes); rec.Code != 200 || rec.Body.String() != devFiles["app.js"] {
		t.Errorf("/c/apps/a+dev/app.js: %d %q", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/apps/a+dev/sub/page.html", wes); rec.Code != 200 || !strings.Contains(rec.Body.String(), "dev sub page") {
		t.Errorf("/c/apps/a+dev/sub/page.html: %d %q", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/apps/a+dev/fresh.js", wes); rec.Code != 404 {
		t.Errorf("a file only the work tree has, at dev: %d", rec.Code)
	}

	// The bare URL and the alias serve the primary, main on its work tree.
	bare, alias := w.do("/c/apps/a/", wes), w.do("/c/apps/a+main/", wes)
	if bare.Code != 200 || strings.Contains(bare.Body.String(), "dev page") || strings.Contains(bare.Body.String(), "xbin-deployment") {
		t.Fatalf("/c/apps/a/: %d %s", bare.Code, bare.Body.String())
	}
	if !same(bare, alias) {
		t.Errorf("the alias answers otherwise than the bare URL:\n%s\nvs\n%s", alias.Body.String(), bare.Body.String())
	}
	if rec := w.do("/c/apps/a+main/app.js", wes); rec.Code != 200 || rec.Body.String() != `import './dep.js';` {
		t.Errorf("/c/apps/a+main/app.js: %d %q", rec.Code, rec.Body.String())
	}

	// deps/ in dev's checkpoint: the other tile's primary answers, as a
	// direct request would; deps/self goes back to apps/a's primary.
	for via, direct := range map[string]string{"/c/apps/a+dev/deps/b/lib.js": "/c/apps/b/lib.js", "/c/apps/a+dev/deps/self/app.js": "/c/apps/a/app.js"} {
		if got, want := w.do(via, wes), w.do(direct, wes); !same(got, want) || got.Code != 200 {
			t.Errorf("%s: %d %q, want %s's %d %q", via, got.Code, got.Body.String(), direct, want.Code, want.Body.String())
		}
	}

	// Today's resolution wins: a tile at apps/c+dev, a zero-state tile, a
	// name outside the grammar.
	if rec := w.do("/c/apps/c+dev/", w.zsOwner()); rec.Code != 200 || !strings.Contains(rec.Body.String(), "a tile named apps/c+dev") ||
		!strings.Contains(rec.Body.String(), `<meta name="xbin-component" content="apps/c+dev">`) {
		t.Errorf("the tile at apps/c+dev: %d %s", rec.Code, rec.Body.String())
	}
	for _, url := range []string{"/c/apps/b+dev/", "/c/apps/b+dev/lib.js", "/c/apps/a+Dev/", "/c/apps/a+9x/app.js", "/c/apps/a+/", "/c/apps/a/x+dev/"} {
		for _, who := range []reqOpt{w.zsOwner(), wes} {
			if got, want := w.do(url, who), w.today(url, who); !same(got, want) {
				t.Errorf("%s: %d %q, want today's %d %q", url, got.Code, got.Body.String(), want.Code, want.Body.String())
			}
		}
	}

	// Unknown names, chrome, nested tiles: 404.
	if rec := w.do("/c/apps/a+nope/", wes); rec.Code != 404 || !strings.Contains(rec.Body.String(), `apps/a has no deployment "nope"`) {
		t.Errorf("an unknown deployment: %d %q", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/root+dev/", w.zsOwner()); rec.Code != 404 {
		t.Errorf("/c/root+dev/ without a record: %d", rec.Code)
	}
	w.pol.recs["root"] = &urlRecord{primary: util.MainDeployment, roots: map[string]string{util.MainDeployment: "", "dev": ""}}
	if rec := w.do("/c/root+dev/", w.zsOwner()); rec.Code != 404 {
		t.Errorf("/c/root+dev/ with a record: %d", rec.Code)
	}
	delete(w.pol.recs, "root")
	if rec := w.do("/c/apps/a+dev/nested/n.js", wes); rec.Code != 404 || !strings.Contains(rec.Body.String(), "apps/a/nested is a tile of its own") {
		t.Errorf("a path into a nested tile: %d %q", rec.Code, rec.Body.String())
	}

	// A missing slash redirects as today.
	if rec := w.do("/c/apps/a+dev", wes); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/c/apps/a+dev/" {
		t.Errorf("/c/apps/a+dev: %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// ?native=1 from each deployment's code: dev has native.js, main's
	// work tree has none.
	if rec := w.do("/c/apps/a+dev/?native=1", wes); rec.Code != 200 || !strings.Contains(rec.Body.String(), `await boot("./native.js")`) ||
		!strings.Contains(rec.Body.String(), `<meta name="xbin-deployment" content="dev">`) {
		t.Errorf("dev's native document: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/apps/a+dev?native=1", wes); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/c/apps/a+dev/?native=1" {
		t.Errorf("dev's native document without its slash: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := w.do("/c/apps/a/?native=1", wes); rec.Code != 404 {
		t.Errorf("the primary's native document (none in the work tree): %d", rec.Code)
	}

	// dev follows the work tree while main is pinned: each serves its own.
	mainRoot := w.checkpoint("apps/a", "main", map[string]string{"index.html": `<!doctype html><html><head></head><body>main checkpoint</body></html>`}, nil)
	w.pol.recs["apps/a"].roots = map[string]string{util.MainDeployment: mainRoot, "dev": ""}
	w.rescan()
	if rec := w.do("/c/apps/a/", wes); !strings.Contains(rec.Body.String(), "main checkpoint") {
		t.Errorf("/c/apps/a/ while main is pinned: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/apps/a+dev/", wes); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<body>a</body>") ||
		!strings.Contains(rec.Body.String(), `<meta name="xbin-deployment" content="dev">`) {
		t.Errorf("/c/apps/a+dev/ on the work tree: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/apps/a+dev/app.js", wes); rec.Body.String() != `import './dep.js';` {
		t.Errorf("/c/apps/a+dev/app.js on the work tree: %q", rec.Body.String())
	}

	// The primary reassigned to dev: the bare URL and its alias serve dev,
	// without the deployment meta; main is a deployment URL of its own.
	w.pol.recs["apps/a"] = &urlRecord{primary: "dev", roots: map[string]string{util.MainDeployment: "", "dev": w.checkpoint("apps/a", "dev-primary", devFiles, nil)}}
	w.rescan()
	for _, url := range []string{"/c/apps/a/", "/c/apps/a+dev/"} {
		if rec := w.do(url, wes); rec.Code != 200 || !strings.Contains(rec.Body.String(), "dev page") || strings.Contains(rec.Body.String(), "xbin-deployment") {
			t.Errorf("%s with dev the primary: %d %s", url, rec.Code, rec.Body.String())
		}
	}
	if rec := w.do("/c/apps/a+main/", wes); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<body>a</body>") ||
		!strings.Contains(rec.Body.String(), `<meta name="xbin-deployment" content="main">`) || !strings.Contains(rec.Body.String(), `"/c/apps/a/":"/c/apps/a+main/"`) {
		t.Errorf("/c/apps/a+main/ with dev the primary: %d %s", rec.Code, rec.Body.String())
	}

	t.Run("tokens", func(t *testing.T) {
		w := newDepWS(t, TileAssetsTokens)
		wes := w.session("wes")
		doc := w.do("/c/apps/a+dev/", wes)
		tok, base := assetTokenFrom(t, doc.Body.String())
		if base != "apps/a+dev/" || !strings.Contains(doc.Body.String(), `"/c/apps/a/":"/c/~`+tok+`/apps/a+dev/"`) ||
			!strings.Contains(doc.Body.String(), `<meta name="xbin-deployment" content="dev">`) {
			t.Fatalf("tokens-mode dev document: %s", doc.Body.String())
		}
		if rec := w.do("/c/~"+tok+"/apps/a+dev/app.js", subresource...); rec.Code != 200 || rec.Body.String() != devFiles["app.js"] {
			t.Errorf("dev's file under its asset token: %d %q", rec.Code, rec.Body.String())
		}
		if rec := w.do("/c/~"+tok+"/apps/a+dev/deps/b/lib.js", subresource...); rec.Code != 200 || rec.Body.String() != "export const lib = 1;" {
			t.Errorf("dev's deps/b under its asset token: %d %q", rec.Code, rec.Body.String())
		}
		if rec := w.do("/c/~"+tok+"/apps/a+nope/app.js", subresource...); rec.Code != 404 {
			t.Errorf("an unknown deployment under an asset token: %d", rec.Code)
		}
		if rec := w.do("/c/~"+tok+"/apps/a+dev/", subresource...); rec.Code != 403 {
			t.Errorf("dev's document under an asset token: %d", rec.Code)
		}
		// A reader's asset token (from the primary's document) never opens
		// dev, whether or not the name exists; wes's stops at his demotion.
		atok, _ := assetTokenFrom(t, w.do("/c/apps/a/", w.session("ana")).Body.String())
		for _, url := range []string{"/c/~" + atok + "/apps/a+dev/app.js", "/c/~" + atok + "/apps/a+nope/app.js"} {
			if rec := w.do(url, subresource...); rec.Code != 403 || !strings.Contains(rec.Body.String(), "deployment URLs need write access on apps/a") {
				t.Errorf("%s as a reader: %d %q", url, rec.Code, rec.Body.String())
			}
		}
		if rec := w.do("/c/~"+atok+"/apps/a+main/app.js", subresource...); rec.Code != 200 || rec.Body.String() != `import './dep.js';` {
			t.Errorf("the alias under a reader's asset token: %d %q", rec.Code, rec.Body.String())
		}
		if _, err := w.st.Upsert(users.User{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelRead}}, ""); err != nil {
			t.Fatal(err)
		}
		if rec := w.do("/c/~"+tok+"/apps/a+dev/app.js", subresource...); rec.Code != 403 {
			t.Errorf("dev under a demoted writer's asset token: %d", rec.Code)
		}
	})

	t.Run("origins", func(t *testing.T) {
		w := newDepWS(t, TileAssetsOrigins)
		if rec := w.do("/c/apps/a+dev/", append(shellNav, w.session("wes"))...); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "http://"+w.a.TileHostIDDeployment("apps/a", "dev")+".xbin.localhost:9260/c/apps/a+dev/?") {
			t.Errorf("a deployment URL in origins mode goes to its own origin: %d %q", rec.Code, rec.Header().Get("Location"))
		}
		if rec := w.do("/c/apps/a+dev/", w.session("ana")); rec.Code != 403 {
			t.Errorf("a deployment URL in origins mode, as a reader: %d", rec.Code)
		}
		if rec := w.do("/c/apps/c+dev/", w.zsOwner(), hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Mode", "navigate")); rec.Code == 404 {
			t.Errorf("the tile at apps/c+dev in origins mode: %d %q", rec.Code, rec.Body.String())
		}
	})
}

// covers D127d D127l T9 — who opens a non-primary deployment's URL: people with
// at least write on the tile (terminal level and admins included), judged
// by their current level on every request, so a writer demoted to read is
// refused on the next request although his dev frame token still verifies;
// a reader gets 403 whether or not the name exists, and so does a reader's
// frame of the tile (the gate never uses p.Component == tile); the tile's
// own tokens (frame, terminal, instance) only for their own deployment,
// except legacy's code files; a credential-less subresource by legacy's
// rule; other tiles' principals never, code grants and the alias included
// (TestDeploymentURLRefusesOtherTiles).
func TestDeploymentURLGate(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	const doc, file = "/c/apps/a+dev/", "/c/apps/a+dev/app.js"
	needsWrite := "deployment URLs need write access on apps/a"
	expect := func(label, url string, who reqOpt, code int, text string) {
		t.Helper()
		opts := []reqOpt{}
		if who != nil {
			opts = append(opts, who)
		}
		rec := w.do(url, opts...)
		if rec.Code != code || (text != "" && !strings.Contains(rec.Body.String(), text)) {
			t.Errorf("%s %s: %d %q, want %d %q", label, url, rec.Code, rec.Body.String(), code, text)
		}
	}

	for label, who := range map[string]reqOpt{"writer": w.session("wes"), "terminal level": w.session("tia"),
		"admin": w.session("ada"), "owner": w.zsOwner()} {
		expect(label, doc, who, 200, "dev page")
		expect(label, file, who, 200, devFiles["app.js"])
		expect(label, "/c/apps/a+nope/", who, 404, `apps/a has no deployment "nope"`)
	}
	ana := w.session("ana")
	for _, url := range []string{doc, file, "/c/apps/a+nope/", "/c/apps/a+nope/app.js", "/c/apps/a+dev/?native=1"} {
		expect("reader", url, ana, 403, needsWrite)
	}
	expect("reader", "/c/apps/a+main/", ana, 200, "<body>a</body>")

	// A reader's frame of the tile is one of its own principals, bound to
	// main: never dev's documents.
	anaFrame := w.frame("apps/a", "ana")
	expect("reader's frame", doc, anaFrame, 403, "a tile's own credentials act only on their own deployment (main)")

	// The tile's own tokens: their own deployment only.
	expect("dev frame", doc, w.devFrame("wes"), 200, "dev page")
	expect("dev frame", "/c/apps/a+dev/?native=1", w.devFrame("wes"), 200, `await boot("./native.js")`)
	wesMain := w.frame("apps/a", "wes")
	expect("main frame", doc, wesMain, 403, "a tile's own credentials act only on their own deployment (main)")
	expect("main frame", "/c/apps/a+dev/sub/page.html", wesMain, 403, "")
	expect("main frame", file, wesMain, 200, devFiles["app.js"]) // legacy: the tile's code
	expect("dev terminal", doc, w.terminal("wes", "dev"), 200, "dev page")
	expect("following terminal", doc, w.terminal("wes", ""), 403, "(main)")
	instance := util.RandomToken(24)
	w.a.RegisterInstanceDeployment(instance, "apps/a", "dev")
	expect("dev instance", file, hdr("Authorization", "Bearer "+instance), 200, devFiles["app.js"])
	w.a.RegisterInstanceDeployment(instance, "apps/a", "")
	expect("main instance", doc, hdr("Authorization", "Bearer "+instance), 403, "(main)")
	w.pol.recs["apps/a"].protected = true
	expect("terminal following a protected primary", doc, w.terminal("wes", ""), 403, "is protected")
	w.pol.recs["apps/a"].protected = false

	// A credential-less subresource load passes by legacy's rule; a
	// credential-less document never.
	w.a.NewSession("ana", "192.0.2.1") // warms httptest's peer IP
	if rec := w.do(file, subresource...); rec.Code != 200 || rec.Body.String() != devFiles["app.js"] {
		t.Errorf("credential-less subresource: %d %q", rec.Code, rec.Body.String())
	}
	if rec := w.do(doc); rec.Code == 200 {
		t.Errorf("credential-less document: %d", rec.Code)
	}

	// Current level, on every request: wes demoted to read loses dev at
	// once, his cookie and his still-valid dev frame token alike.
	wes, wesDev := w.session("wes"), w.devFrame("wes")
	expect("writer", doc, wes, 200, "dev page")
	if _, err := w.st.Upsert(users.User{ID: "wes", Role: users.RoleUser, Tiles: map[string]string{"apps/a": users.LevelRead, "apps/b": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	expect("demoted writer", doc, wes, 403, needsWrite)
	r := httptest.NewRequest("GET", doc, nil)
	wesDev(r)
	if p, ok := w.a.FramePrincipal(r.Header.Get(auth.FrameTokenHeader)); !ok || p.Deployment != "dev" {
		t.Fatalf("the demoted writer's dev frame token no longer verifies: %+v %v", p, ok)
	}
	expect("demoted writer's dev frame", doc, wesDev, 403, needsWrite)
	expect("demoted writer's dev frame", file, wesDev, 403, needsWrite)
	expect("demoted writer", "/c/apps/a/", wes, 200, "<body>a</body>")

	t.Run("strict", func(t *testing.T) {
		w := newDepWS(t, TileAssetsTokens)
		mainFrame := w.frame("apps/a", "wes")
		for _, url := range []string{doc, file} {
			if rec := w.do(url, mainFrame); rec.Code != 403 {
				t.Errorf("main frame %s in tokens mode: %d", url, rec.Code)
			}
		}
		if rec := w.do(doc, w.devFrame("wes")); rec.Code != 200 {
			t.Errorf("dev frame in tokens mode: %d", rec.Code)
		}
	})

	t.Run("TestDeploymentURLRefusesOtherTiles", func(t *testing.T) {
		w := newDepWS(t, TileAssetsLegacy)
		// wes writes apps/a, yet apps/b's frame of his is another tile.
		other := w.frame("apps/b", "wes")
		w.s.Pol.(*urlPolicy).testPolicy.code = func(from, target string) bool { return from == "apps/b" }
		for _, url := range []string{doc, file, "/c/apps/a+main/", "/c/apps/a+main/app.js", "/c/apps/a+nope/", "/c/apps/a+dev/?native=1"} {
			rec := w.do(url, other)
			if rec.Code != 403 || !strings.Contains(rec.Body.String(), "apps/b opens apps/a by its bare URL") {
				t.Errorf("another tile's frame %s: %d %q", url, rec.Code, rec.Body.String())
			}
		}
		instance := util.RandomToken(24)
		w.a.RegisterInstance(instance, "apps/b")
		if rec := w.do(file, hdr("Authorization", "Bearer "+instance)); rec.Code != 403 {
			t.Errorf("another tile's backend: %d", rec.Code)
		}
		if rec := w.do("/c/apps/a/app.js", other); rec.Code != 200 {
			t.Errorf("another tile's frame, the bare URL: %d", rec.Code)
		}
	})
}

// covers D127g D127j — the frame token injected into a non-primary document
// carries the deployment claim, main's none: a dev document (a page, a
// sub-page, the native runtime document) mints dev's six-field token; the
// bare URL and the alias mint main's five fields; with dev the primary, the
// bare URL mints dev's, for a reader too and for a terminal session that
// follows the primary, and main's deployment URL mints main's claim-less
// token.
func TestFrameTokenClaimInjected(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	claim := func(url string, who reqOpt) (string, int) {
		t.Helper()
		rec := w.do(url, who)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		tok := frameTokenIn(t, rec.Body.String())
		p, ok := w.a.FramePrincipal(tok)
		if !ok || p.Component != "apps/a" {
			t.Fatalf("%s: the injected token %q doesn't verify for apps/a: %+v", url, tok, p)
		}
		return p.Deployment, strings.Count(tok, "|") + 1
	}
	wes := w.session("wes")
	for _, url := range []string{"/c/apps/a+dev/", "/c/apps/a+dev/sub/page.html", "/c/apps/a+dev/?native=1"} {
		if dep, n := claim(url, wes); dep != "dev" || n != 6 {
			t.Errorf("%s: claim %q, %d fields; want dev, 6", url, dep, n)
		}
	}
	if dep, n := claim("/c/apps/a+dev/", w.devFrame("wes")); dep != "dev" || n != 6 {
		t.Errorf("dev's frame renewing through its document: claim %q, %d fields", dep, n)
	}
	for _, url := range []string{"/c/apps/a/", "/c/apps/a+main/"} {
		if dep, n := claim(url, wes); dep != "" || n != 5 {
			t.Errorf("%s: claim %q, %d fields; want none, 5", url, dep, n)
		}
	}

	w.pol.recs["apps/a"].primary = "dev"
	w.rescan()
	for label, who := range map[string]reqOpt{"writer": wes, "reader": w.session("ana"), "following terminal": w.terminal("wes", "")} {
		if dep, n := claim("/c/apps/a/", who); dep != "dev" || n != 6 {
			t.Errorf("%s, the bare URL with dev the primary: claim %q, %d fields", label, dep, n)
		}
	}
	if dep, n := claim("/c/apps/a+main/", wes); dep != "" || n != 5 {
		t.Errorf("main's deployment URL: claim %q, %d fields; want none, 5", dep, n)
	}
}

// covers D127g T3 — mayMintFrameToken never crosses deployments, in both
// directions (TestMintRefusesCrossDeployment): a dev frame, a dev-targeted
// terminal and a dev instance fetching the bare /c/<tile>/ get the primary's
// files with content=""; a main frame or a terminal that follows the
// primary never gets a dev token, nor does a reader; the navigation rule
// between one tree's tiles holds only between primaries, from a frame bound
// to its tile's primary; a terminal that follows a protected primary mints
// nothing.
func TestMayMintFrameTokenNeverCrossesDeployments(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	for label, who := range map[string]reqOpt{"dev frame": w.devFrame("wes"), "dev terminal": w.terminal("wes", "dev")} {
		rec := w.do("/c/apps/a/", who)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<body>a</body>") || frameTokenIn(t, rec.Body.String()) != "" {
			t.Errorf("%s fetching /c/apps/a/: %d, token %q", label, rec.Code, frameTokenIn(t, rec.Body.String()))
		}
	}
	for label, who := range map[string]reqOpt{"main frame": w.frame("apps/a", "wes"), "following terminal": w.terminal("wes", "")} {
		if rec := w.do("/c/apps/a+dev/", who); rec.Code != 403 || strings.Contains(rec.Body.String(), "xbin-frame-token") {
			t.Errorf("%s fetching /c/apps/a+dev/: %d %q", label, rec.Code, rec.Body.String())
		}
	}

	// The rule itself, for a document of each deployment.
	at := func(url string) *http.Request {
		r := httptest.NewRequest("GET", url, nil)
		if q, ok := w.s.resolveQualified(strings.TrimSuffix(strings.TrimPrefix(url, "/c/"), "/")); ok {
			r = withServed(r, &served{tile: q.c.Path, dep: q.dep, primary: q.primary})
		}
		return r
	}
	principal := func(opt reqOpt) auth.Principal {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "192.0.2.1:1"
		opt(r)
		p, ok := w.a.FromRequest(r)
		if !ok {
			t.Fatal("principal refused")
		}
		return p
	}
	instance := util.RandomToken(24)
	w.a.RegisterInstanceDeployment(instance, "apps/a", "dev")
	cases := []struct {
		who       string
		p         auth.Principal
		bare, dev bool
	}{
		{"writer", principal(w.session("wes")), true, true},
		{"reader", principal(w.session("ana")), true, false},
		{"owner", principal(w.zsOwner()), true, true},
		{"main frame", principal(w.frame("apps/a", "wes")), true, false},
		{"reader's frame", principal(w.frame("apps/a", "ana")), true, false},
		{"dev frame", principal(w.devFrame("wes")), false, true},
		{"dev frame of a reader", principal(w.devFrame("ana")), false, false},
		{"following terminal", principal(w.terminal("wes", "")), true, false},
		{"main terminal", principal(w.terminal("wes", "main")), true, false},
		{"dev terminal", principal(w.terminal("wes", "dev")), false, true},
		{"dev instance", principal(hdr("Authorization", "Bearer "+instance)), false, false},
		{"another tile's frame", principal(w.frame("apps/b", "wes")), false, false},
	}
	for _, c := range cases {
		if got := w.s.mayMintFrameToken(at("/c/apps/a/"), c.p, "apps/a"); got != c.bare {
			t.Errorf("%s, the primary's document: mint %v, want %v", c.who, got, c.bare)
		}
		if got := w.s.mayMintFrameToken(at("/c/apps/a+dev/"), c.p, "apps/a"); got != c.dev {
			t.Errorf("%s, dev's document: mint %v, want %v", c.who, got, c.dev)
		}
	}

	// The navigation rule: apps/a's frame may take the nested
	// apps/a/nested's token only between primaries.
	nav := func(url string) *http.Request {
		r := at(url)
		r.Header.Set("Sec-Fetch-Mode", "navigate")
		return r
	}
	mainFrame, devFrame := principal(w.frame("apps/a", "")), principal(w.devFrame(""))
	if !w.s.mayMintFrameToken(nav("/c/apps/a/nested/"), mainFrame, "apps/a/nested") {
		t.Error("the navigation rule no longer holds between primaries")
	}
	if w.s.mayMintFrameToken(nav("/c/apps/a/nested/"), devFrame, "apps/a/nested") {
		t.Error("a dev frame took a nested tile's primary token by navigating")
	}
	w.pol.recs["apps/a/nested"] = &urlRecord{primary: util.MainDeployment, roots: map[string]string{util.MainDeployment: "", "dev": ""}}
	if w.s.mayMintFrameToken(nav("/c/apps/a/nested+dev/"), mainFrame, "apps/a/nested") {
		t.Error("the navigation rule reached a nested tile's non-primary deployment")
	}

	// A session that follows a protected primary is bound to nothing.
	w.pol.recs["apps/a"].protected = true
	if w.s.mayMintFrameToken(at("/c/apps/a/"), principal(w.terminal("wes", "")), "apps/a") {
		t.Error("a terminal following a protected primary minted its token")
	}
	if rec := w.do("/c/apps/a/", w.terminal("wes", "")); frameTokenIn(t, rec.Body.String()) != "" {
		t.Errorf("a terminal following a protected primary got a token in the document: %d", rec.Code)
	}
}

// withServed is r carrying sv, as serveQualified sets it.
func withServed(r *http.Request, sv *served) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), servedKey{}, sv))
}

// covers T14 D127k — a non-primary deployment's documents are always
// sandboxed, whatever its code declares: the shipped chrome tile's bare URL
// runs unsandboxed, its dev's page, inject:false file and native runtime
// document get the CSP sandbox (and the sandbox meta), never COOP.
func TestNonPrimaryDocumentAlwaysSandboxed(t *testing.T) {
	w := newDepWS(t, TileAssetsLegacy)
	owner := w.zsOwner()
	bare := w.do("/c/tiles/organisations/", owner)
	if bare.Code != 200 || strings.HasPrefix(bare.Header().Get("Content-Security-Policy"), "sandbox") ||
		bare.Header().Get("Cross-Origin-Opener-Policy") != "same-origin" || strings.Contains(bare.Body.String(), "xbin-sandbox") {
		t.Fatalf("the chrome tile's bare URL should run unsandboxed: %d %v", bare.Code, bare.Header())
	}
	for _, url := range []string{"/c/tiles/organisations+dev/", "/c/tiles/organisations+dev/?native=1"} {
		rec := w.do(url, owner)
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), sandboxCSP) ||
			rec.Header().Get("Cross-Origin-Opener-Policy") != "" || !strings.Contains(rec.Body.String(), `<meta name="xbin-sandbox"`) {
			t.Errorf("%s: %d %v\n%s", url, rec.Code, rec.Header(), rec.Body.String())
		}
	}
	// inject:false in dev's code: served byte-exact, and sandboxed.
	w.pol.recs["tiles/organisations"].roots["dev"] = w.checkpoint("tiles/organisations", "dev-raw",
		map[string]string{"xbin.json": `{"chrome":true,"inject":false}`, "index.html": `<p>raw dev</p>`}, nil)
	rec := w.do("/c/tiles/organisations+dev/", owner)
	if rec.Code != 200 || rec.Body.String() != `<p>raw dev</p>` || !strings.HasPrefix(rec.Header().Get("Content-Security-Policy"), sandboxCSP) {
		t.Errorf("an inject:false dev document: %d %q %v", rec.Code, rec.Body.String(), rec.Header())
	}
	// The registry never gives a view chrome.
	c, _ := w.s.Reg.Component("tiles/organisations")
	v, err := w.s.deploymentView(c, "dev", w.pol.recs["tiles/organisations"].roots["dev"], true)
	if err != nil || w.s.trustedChrome(c.Path, v) || !w.s.trustedChrome(c.Path, c) {
		t.Errorf("chrome: the view %v (%v), the tile %v", w.s.trustedChrome(c.Path, v), err, w.s.trustedChrome(c.Path, c))
	}
}
