package server

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// pinnedPolicy is testPolicy plus the deployment answers a deployments
// plane gives: CodeRoot for the tiles whose primary is pinned (roots) or
// can't be served (broken), and the /components summary of the tiles with
// a record (summary).
type pinnedPolicy struct {
	testPolicy
	roots   map[string]string
	broken  map[string]bool
	summary map[string]deploymentsSummary
}

func (p *pinnedPolicy) CodeRoot(c *registry.Component, dep string) (string, bool, error) {
	if p.broken[c.Path] {
		return "", false, errors.New(c.Path + ": the primary's checkpoint isn't prepared")
	}
	if root, ok := p.roots[c.Path]; ok && (dep == "" || dep == util.MainDeployment) {
		return root, true, nil
	}
	return NoopPolicy{}.CodeRoot(c, dep)
}

func (p *pinnedPolicy) PrimarySummary(tile string) (string, bool, bool, bool) {
	s, ok := p.summary[tile]
	return s.Primary, s.Pinned, s.Protected, ok
}

// pinned is a workspace whose tiles' primaries can be pinned to
// checkpoints: the registry's PinnedPrimary hook and the server's Policy
// answer from the same maps, as the deployments plane does.
type pinned struct {
	*assetWS
	pol  *pinnedPolicy
	code map[string]*registry.PinnedCode
}

// newPinned installs a pinnedPolicy and the registry hook on w.
func newPinned(w *assetWS) *pinned {
	p := &pinned{assetWS: w, pol: &pinnedPolicy{roots: map[string]string{}, broken: map[string]bool{},
		summary: map[string]deploymentsSummary{}}, code: map[string]*registry.PinnedCode{}}
	w.s.Pol = p.pol
	w.s.Reg.PinnedPrimary = func(rel string) (*registry.PinnedCode, bool) {
		pc, ok := p.code[rel]
		return pc, ok
	}
	return p
}

// pin materializes files and links (name → link text) as a checkpoint of
// tile under .xbin/deploy, as materialization lays one out, and pins the
// tile's primary to it: the registry composes the tile from it, CodeRoot
// answers it, and /components gets the tile's summary.
func (p *pinned) pin(tile string, files, links map[string]string) string {
	p.t.Helper()
	root := filepath.Join(p.root, ".xbin", "deploy", strings.ReplaceAll(tile, "/", "_"), "tree")
	if err := os.RemoveAll(root); err != nil {
		p.t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		p.t.Fatal(err)
	}
	writeWS(p.t, root, files)
	for name, text := range links {
		l := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(l), 0o755); err != nil {
			p.t.Fatal(err)
		}
		if err := os.Symlink(text, l); err != nil {
			p.t.Fatal(err)
		}
	}
	pc, err := registry.ReadCheckpoint(root)
	if err != nil {
		p.t.Fatalf("ReadCheckpoint(%s): %v", tile, err)
	}
	p.code[tile], p.pol.roots[tile] = pc, root
	p.pol.summary[tile] = deploymentsSummary{Primary: util.MainDeployment, Pinned: true}
	p.rescan()
	return root
}

func (p *pinned) rescan() {
	p.t.Helper()
	if err := p.s.Reg.Rescan(); err != nil {
		p.t.Fatal(err)
	}
}

// edit writes files into the work tree and rescans, as a save does.
func (p *pinned) edit(files map[string]string) {
	p.t.Helper()
	writeWS(p.t, p.root, files)
	p.rescan()
}

func writeWS(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		f := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// the checkpoint apps/a's primary is pinned to in the serving tests: the
// tile's files as they were at the pause, and the links a checkpoint may
// hold.
var pinnedFiles = map[string]string{
	"xbin.json":        `{}`,
	"index.html":       `<!doctype html><html><head><title>pinned</title></head><body>pinned page</body></html>`,
	"app.js":           `export const pinned = true;`,
	"dist/a.css":       `body{color:red}`,
	"sub/page.html":    `<!doctype html><html><head></head><body>pinned sub page</body></html>`,
	"sub/data.json":    `{"pinned":1}`,
	"loop/readme.txt":  `in-tree`,
	"deps/.keep":       ``,
	"notes/unused.txt": `x`,
}

// pinnedApps is newAssetWS with apps/a's primary pinned to pinnedFiles, the
// checkpoint also holding every kind of link: in-tree ones, ones leaving
// the tree (absolute, into the workspace, into xbind's own secret, a chain,
// a directory link) and deps/ links, relative and absolute.
func pinnedApps(t *testing.T, mode string) (*pinned, string) {
	w := newPinned(newAssetWS(t, mode))
	secret := filepath.Join(w.root, "apps/secret/s.js")
	root := w.pin("apps/a", pinnedFiles, map[string]string{
		"alias.js":      "app.js",                       // in-tree file link
		"assets":        "dist",                         // in-tree directory link
		"passwd":        "/etc/passwd",                  // absolute, off the workspace
		"leak.js":       "../../../../apps/secret/s.js", // into another tile, if followed on the host
		"key.txt":       "../../../secret",              // xbind's HMAC secret, if followed on the host
		"abs-secret.js": secret,                         // absolute, into another tile
		"chain1.js":     "chain2.js",                    // an in-tree link to one that leaves
		"chain2.js":     secret,
		"etc":           "/etc",             // a directory link off the host
		"up":            "../../../../apps", // a directory link into the workspace
		"deps/b":        "../../b",          // apps/a/deps/../../b = apps/b
		"deps/abs":      filepath.Join(w.root, "apps/b"),
		"deps/secret":   "../../secret",   // apps/secret: ana may not read it
		"deps/shell":    "../../../shell", // chrome
		"deps/self":     "../../a",        // apps/a itself
		"deps/nowhere":  "../../nothing",  // no component
		"deps/sub":      "../../b/sub",    // inside a component, not one
		"deps/host":     "/etc",
		"deps/raw":      "../../raw", // apps/raw (inject:false)
	})
	// Work-tree edits after the pause: none reaches what the pinned
	// primary serves, a re-pointed deps/ link included.
	w.edit(map[string]string{
		"apps/a/app.js":        `export const workTree = true;`,
		"apps/a/index.html":    `<!doctype html><html><head></head><body>work tree page</body></html>`,
		"apps/a/fresh.js":      `export const onlyInTheWorkTree = 1;`,
		"apps/a/sub/page.html": `<!doctype html><html><head></head><body>work tree sub page</body></html>`,
	})
	if err := os.MkdirAll(filepath.Join(w.root, "apps/a/deps"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../secret", filepath.Join(w.root, "apps/a/deps/b")); err != nil {
		t.Fatal(err)
	}
	return w, root
}

// covers P9 — while main is pinned, the bare URL serves the checkpoint in
// every asset mode: its documents (with today's injection, for the tile the
// URL names), files and directory indexes; work-tree edits — changed,
// added or removed files, index.html and inject included — don't show
// there; Cache-Control: no-store is kept. A nested component under the
// tile serves its own primary. A primary whose code can't be served
// answers 404, never its work tree, for ?native=1 too.
func TestPinnedPrimaryServesCheckpoint(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w, _ := pinnedApps(t, mode)
			ana := w.session("ana")
			get := func(url string) *httptest.ResponseRecorder { return w.do(url, ana) }

			doc := get("/c/apps/a/")
			if doc.Code != 200 || !strings.Contains(doc.Body.String(), "pinned page") || strings.Contains(doc.Body.String(), "work tree") {
				t.Fatalf("/c/apps/a/: %d %s", doc.Code, doc.Body.String())
			}
			if !strings.Contains(doc.Body.String(), `<meta name="xbin-component" content="apps/a">`) ||
				!strings.Contains(doc.Body.String(), `<script type="module" src="/vendor/xbin-client.js">`) {
				t.Errorf("a pinned document lost the injection: %s", doc.Body.String())
			}
			if strings.Contains(doc.Body.String(), "xbin-deployment") {
				t.Errorf("a pinned main primary's document names a deployment: %s", doc.Body.String())
			}
			for url, want := range map[string]string{
				"/c/apps/a/app.js":        "export const pinned = true;",
				"/c/apps/a/sub/data.json": `{"pinned":1}`,
			} {
				rec := get(url)
				if rec.Code != 200 || rec.Body.String() != want {
					t.Errorf("%s: %d %q, want the checkpoint's %q", url, rec.Code, rec.Body.String(), want)
				}
				if rec.Header().Get("Cache-Control") != "no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("%s headers: %v", url, rec.Header())
				}
			}
			if rec := get("/c/apps/a/sub/page.html"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "pinned sub page") {
				t.Errorf("sub/page.html: %d %s", rec.Code, rec.Body.String())
			}
			if rec := get("/c/apps/a/fresh.js"); rec.Code != 404 {
				t.Errorf("a file only the work tree has: %d %q, want 404", rec.Code, rec.Body.String())
			}
			if rec := get("/c/apps/a/sub"); rec.Code != 301 || rec.Header().Get("Location") != "/c/apps/a/sub/" {
				t.Errorf("a directory without its slash: %d %v", rec.Code, rec.Header())
			}
			if rec := get("/c/apps/b/lib.js"); rec.Code != 200 || rec.Body.String() != "export const lib = 1;" {
				t.Errorf("a zero-state tile beside it: %d %q", rec.Code, rec.Body.String())
			}

			// The work tree loses index.html: the pinned primary keeps its own.
			if err := os.Remove(filepath.Join(w.root, "apps/a/index.html")); err != nil {
				t.Fatal(err)
			}
			w.rescan()
			if rec := get("/c/apps/a/"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "pinned page") {
				t.Errorf("after the work tree lost index.html: %d %s", rec.Code, rec.Body.String())
			}

			// inject is deployment-level: the checkpoint's, whatever the work
			// tree now says.
			w.edit(map[string]string{"apps/a/xbin.json": `{"inject":false}`})
			if rec := get("/c/apps/a/"); !strings.Contains(rec.Body.String(), "xbin-client.js") {
				t.Errorf("a work-tree inject:false reached the pinned primary: %s", rec.Body.String())
			}
			files := map[string]string{}
			for k, v := range pinnedFiles {
				files[k] = v
			}
			files["xbin.json"] = `{"inject":false}`
			w.pin("apps/a", files, nil)
			w.edit(map[string]string{"apps/a/xbin.json": `{}`})
			rec := get("/c/apps/a/")
			if rec.Code != 200 || rec.Body.String() != pinnedFiles["index.html"] {
				t.Errorf("the checkpoint's inject:false: %d %q, want its bytes unchanged", rec.Code, rec.Body.String())
			}
			if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
				t.Errorf("an inject:false pinned document isn't sandboxed: %q", csp)
			}

			// A nested component registered under the tile serves its own
			// primary, its work tree (a checkpoint never holds one).
			w.edit(map[string]string{"apps/a/nested/xbin.json": `{}`, "apps/a/nested/n.js": "nested work tree"})
			if rec := w.do("/c/apps/a/nested/n.js", w.zsOwner()); rec.Code != 200 || rec.Body.String() != "nested work tree" {
				t.Errorf("a nested component under a pinned tile: %d %q", rec.Code, rec.Body.String())
			}

			// Nothing to serve: 404, never the work tree.
			w.pol.broken["apps/a"] = true
			for _, url := range []string{"/c/apps/a/", "/c/apps/a/app.js", "/c/apps/a/fresh.js", "/c/apps/a/?native=1"} {
				if rec := get(url); rec.Code != 404 {
					t.Errorf("%s while the primary can't be served: %d %q, want 404", url, rec.Code, rec.Body.String())
				}
			}
			if rec := get("/c/apps/b/lib.js"); rec.Code != 200 {
				t.Errorf("another tile, meanwhile: %d", rec.Code)
			}
		})
	}
}

// covers P16 T2 — containment in all three asset modes: a pinned
// checkpoint's files open beneath its materialized tree. An in-tree link
// works; every other link leaving the tree (/etc/passwd, into another
// tile, xbind's secret, a chain, a directory link) answers 404 and is never
// followed on the host; the dev overlay never applies. A deps/<name>/…
// path is re-dispatched to the tile the checkpoint's own link names, and
// answers exactly as a direct request for /c/<target>/… by the same
// principal would — its gate included (another tile ana may not read,
// chrome refusing a credential-less load); a link naming no component, or
// leaving the workspace, is a 404. A work-tree deps edit doesn't re-point
// the pinned primary. Re-dispatches chain only so far.
func TestPinnedServingUsesOpenBeneath(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w, _ := pinnedApps(t, mode)
			w.a.NewSession("ana", "192.0.2.1") // warms httptest's peer IP (legacy's subresource rule)
			secret := strings.TrimSpace(w.secretOf())

			// The plane under test, as ana: legacy's /c/ with her session;
			// the asset-token plane (tokens); apps/a's own origin (origins).
			var at func(url string, opts ...reqOpt) *httptest.ResponseRecorder
			switch mode {
			case TileAssetsLegacy:
				ana := w.session("ana")
				at = func(url string, opts ...reqOpt) *httptest.ResponseRecorder {
					return w.do(url, append([]reqOpt{ana}, opts...)...)
				}
			case TileAssetsTokens:
				tok, _ := assetTokenFrom(t, w.do("/c/apps/a/", w.session("ana")).Body.String())
				at = func(url string, opts ...reqOpt) *httptest.ResponseRecorder {
					return w.do("/c/~"+tok+strings.TrimPrefix(url, "/c"), append(append([]reqOpt{}, subresource...), opts...)...)
				}
			case TileAssetsOrigins:
				c, rec := w.exchange("apps/a", "ana", "/c/apps/a/")
				if c == nil {
					t.Fatalf("exchange: %d %v", rec.Code, rec.Header())
				}
				on := []reqOpt{host(w.originHost("apps/a")), cookie(c.Name, c.Value), hdr("Sec-Fetch-Site", "same-origin"), hdr("Sec-Fetch-Dest", "script")}
				at = func(url string, opts ...reqOpt) *httptest.ResponseRecorder {
					return w.do(url, append(append([]reqOpt{}, on...), opts...)...)
				}
			}

			for url, want := range map[string]string{
				"/c/apps/a/alias.js":        "export const pinned = true;",
				"/c/apps/a/assets/a.css":    "body{color:red}",
				"/c/apps/a/loop/readme.txt": "in-tree",
			} {
				if rec := at(url); rec.Code != 200 || rec.Body.String() != want {
					t.Errorf("in-tree link %s: %d %q, want %q", url, rec.Code, rec.Body.String(), want)
				}
			}
			for _, url := range []string{"/c/apps/a/passwd", "/c/apps/a/leak.js", "/c/apps/a/key.txt", "/c/apps/a/abs-secret.js",
				"/c/apps/a/chain1.js", "/c/apps/a/chain2.js", "/c/apps/a/etc/passwd", "/c/apps/a/up/secret/s.js"} {
				rec := at(url)
				if rec.Code != 404 {
					t.Errorf("escaping link %s: %d, want 404", url, rec.Code)
				}
				if b := rec.Body.String(); strings.Contains(b, "hunter2") || strings.Contains(b, "root:") || (secret != "" && strings.Contains(b, secret)) {
					t.Errorf("escaping link %s was followed on the host: %q", url, b)
				}
			}

			// deps/: served as the direct request for the target would be.
			for _, c := range []struct{ via, direct string }{
				{"/c/apps/a/deps/b/lib.js", "/c/apps/b/lib.js"},       // the checkpoint's link, not the work tree's
				{"/c/apps/a/deps/abs/lib.js", "/c/apps/b/lib.js"},     // an absolute link inside the workspace
				{"/c/apps/a/deps/secret/s.js", "/c/apps/secret/s.js"}, // ana may not read apps/secret
				{"/c/apps/a/deps/raw/r.js", "/c/apps/raw/r.js"},
				{"/c/apps/a/deps/shell/shell.js", "/c/shell/shell.js"}, // chrome
				{"/c/apps/a/deps/self/app.js", "/c/apps/a/app.js"},     // back into the pinned primary
				{"/c/apps/a/deps/b", "/c/apps/b"},                      // the directory, without its slash
			} {
				got, want := at(c.via), at(c.direct)
				if got.Code != want.Code || got.Body.String() != want.Body.String() || got.Header().Get("Location") != want.Header().Get("Location") {
					t.Errorf("%s: %d %q (%v), want %s's %d %q (%v)", c.via, got.Code, got.Body.String(), got.Header(),
						c.direct, want.Code, want.Body.String(), want.Header())
				}
				for _, h := range []string{"Content-Type", "Content-Security-Policy", "Cache-Control", "X-Content-Type-Options", "Referrer-Policy"} {
					if got.Header().Get(h) != want.Header().Get(h) {
						t.Errorf("%s %s: %q, want %q", c.via, h, got.Header().Get(h), want.Header().Get(h))
					}
				}
			}
			if rec := at("/c/apps/a/deps/b/lib.js"); rec.Code != 200 || rec.Body.String() != "export const lib = 1;" {
				t.Errorf("deps/b: %d %q", rec.Code, rec.Body.String())
			}
			if rec := at("/c/apps/a/deps/secret/s.js"); rec.Code != 403 || strings.Contains(rec.Body.String(), "hunter2") {
				t.Errorf("deps/secret as ana: %d %q, want 403", rec.Code, rec.Body.String())
			}
			for _, url := range []string{"/c/apps/a/deps/nowhere/x.js", "/c/apps/a/deps/sub/x.js", "/c/apps/a/deps/host/passwd", "/c/apps/a/deps/none/x.js"} {
				if rec := at(url); rec.Code != 404 || strings.Contains(rec.Body.String(), "root:") {
					t.Errorf("%s: %d %q, want 404", url, rec.Code, rec.Body.String())
				}
			}
			self := "/c/apps/a" + strings.Repeat("/deps/self", 3) + "/app.js"
			if rec := at(self); rec.Code != 200 || rec.Body.String() != "export const pinned = true;" {
				t.Errorf("a short chain of re-dispatches: %d %q", rec.Code, rec.Body.String())
			}
			long := "/c/apps/a" + strings.Repeat("/deps/self", depsHopsMax+1) + "/app.js"
			if rec := at(long); rec.Code != 404 {
				t.Errorf("a chain longer than %d re-dispatches: %d, want 404", depsHopsMax, rec.Code)
			}

			if mode == TileAssetsLegacy {
				// Legacy's credential-less subresource rule, re-dispatched:
				// a tile's file passes, chrome refuses, as when direct.
				if rec := w.do("/c/apps/a/deps/b/lib.js", subresource...); rec.Code != 200 {
					t.Errorf("credential-less deps/b: %d", rec.Code)
				}
				got, want := w.do("/c/apps/a/deps/shell/shell.js", subresource...), w.do("/c/shell/shell.js", subresource...)
				if got.Code != want.Code || want.Code == 200 {
					t.Errorf("credential-less chrome through deps/: %d, direct %d", got.Code, want.Code)
				}
				// The owner reads apps/secret directly, and so through deps/.
				if rec := w.do("/c/apps/a/deps/secret/s.js", w.zsOwner()); rec.Code != 200 || !strings.Contains(rec.Body.String(), "hunter2") {
					t.Errorf("deps/secret as the owner: %d %q", rec.Code, rec.Body.String())
				}
				// The dev overlay shadows work trees, never a checkpoint.
				overlay := t.TempDir()
				writeWS(t, overlay, map[string]string{"apps/a/app.js": "overlay", "apps/b/lib.js": "overlay"})
				w.s.Overlay = overlay
				if rec := at("/c/apps/a/app.js"); rec.Body.String() != "export const pinned = true;" {
					t.Errorf("the dev overlay reached a checkpoint: %q", rec.Body.String())
				}
				if rec := at("/c/apps/b/lib.js"); rec.Body.String() != "overlay" {
					t.Errorf("the dev overlay no longer shadows a work tree: %q", rec.Body.String())
				}
			}
		})
	}
}

// covers P7 E15 — ?native=1 is generated from the primary's code, its
// checkpoint while pinned: the entry the checkpoint declares or holds,
// whatever the work tree now says, loaded from the checkpoint; no native UI
// when the checkpoint has none, though the work tree added one; an entry
// that leaves the checkpoint through a symlink is refused with the reason
// (never stat'ed on the host). /components' native agrees each time.
func TestNativeDocumentFollowsPrimary(t *testing.T) {
	w := newPinned(newAssetWS(t, TileAssetsLegacy))
	page := `<!doctype html><html><head></head><body>p</body></html>`
	w.edit(map[string]string{
		"apps/n/xbin.json": `{}`, "apps/n/index.html": page, "apps/n/native.js": `export const workTree = 1;`,
		"apps/w/xbin.json": `{}`, "apps/w/index.html": page,
		"apps/k/xbin.json": `{}`, "apps/k/index.html": page,
		"apps/e/xbin.json": `{}`, "apps/e/index.html": page, "apps/e/native.js": `export {}`,
	})
	w.pin("apps/n", map[string]string{"xbin.json": `{"native":"mobile/main.js"}`, "index.html": page,
		"mobile/main.js": `export const pinnedEntry = 1;`}, nil)
	w.pin("apps/w", map[string]string{"xbin.json": `{}`, "index.html": page}, nil)
	w.pin("apps/k", map[string]string{"xbin.json": `{}`, "index.html": page, "native.js": `export const pinned = 1;`}, nil)
	w.pin("apps/e", map[string]string{"xbin.json": `{}`, "index.html": page}, map[string]string{"native.js": "../../../../apps/e/native.js"})
	w.edit(map[string]string{"apps/w/native.js": `export const added = 1;`}) // the work tree grows a native UI
	if err := os.Remove(filepath.Join(w.root, "apps/k/xbin.json")); err != nil {
		t.Fatal(err)
	}
	w.rescan()

	owner := w.zsOwner()
	comps := componentsView(t, w.s)
	for _, c := range []struct{ tile, entry, module, body string }{
		{"apps/n", "mobile/main.js", "/c/apps/n/mobile/main.js", "export const pinnedEntry = 1;"},
		{"apps/k", "native.js", "/c/apps/k/native.js", "export const pinned = 1;"},
	} {
		rec := w.do("/c/"+c.tile+"/?native=1", owner)
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `await boot("./`+c.entry+`");`) {
			t.Errorf("%s ?native=1: %d %s", c.tile, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `<meta name="xbin-component" content="`+c.tile+`">`) {
			t.Errorf("%s: the runtime document lost its injection: %s", c.tile, rec.Body.String())
		}
		if got := comps[c.tile].Native; got == nil || got.Entry != c.entry {
			t.Errorf("%s /components native = %+v, want %s", c.tile, got, c.entry)
		}
		if rec := w.do(c.module, owner); rec.Code != 200 || rec.Body.String() != c.body {
			t.Errorf("%s entry %s: %d %q, want the checkpoint's", c.tile, c.module, rec.Code, rec.Body.String())
		}
	}
	for tile, why := range map[string]string{"apps/w": "no native app UI", "apps/e": "native.js"} {
		rec := w.do("/c/"+tile+"/?native=1", owner)
		if rec.Code != 404 || !strings.Contains(rec.Body.String(), why) {
			t.Errorf("%s ?native=1: %d %q, want 404 naming %q", tile, rec.Code, rec.Body.String(), why)
		}
		if comps[tile].Native != nil {
			t.Errorf("%s /components native = %+v, want none", tile, comps[tile].Native)
		}
	}
	if rec := w.do("/c/apps/w/native.js", owner); rec.Code != 404 {
		t.Errorf("apps/w's work-tree native.js is served from the pinned primary: %d", rec.Code)
	}
}

// covers P5 C7 — which opener a /c/ request of a tile uses: today's for a
// zero-state tile and for a path no component owns; the checkpoint's root
// for a pinned primary; none (a 404) for one that can't be served. Without
// a PrimarySummaryPolicy no /components entry gains a summary.
func TestPinnedPrimaryRootAnswers(t *testing.T) {
	w := newPinned(newAssetWS(t, TileAssetsLegacy))
	if root, pinned, ok := w.s.primaryRoot("apps/a"); root != "" || pinned || !ok {
		t.Errorf("a zero-state tile: %q %v %v, want today's opener", root, pinned, ok)
	}
	if root, pinned, ok := w.s.primaryRoot("apps/unregistered"); root != "" || pinned || !ok {
		t.Errorf("an unregistered path: %q %v %v, want today's opener", root, pinned, ok)
	}
	want := w.pin("apps/a", pinnedFiles, nil)
	if root, pinned, ok := w.s.primaryRoot("apps/a"); root != want || !pinned || !ok {
		t.Errorf("a pinned tile: %q %v %v, want %q", root, pinned, ok, want)
	}
	w.pol.broken["apps/a"] = true
	if _, _, ok := w.s.primaryRoot("apps/a"); ok {
		t.Error("a primary that can't be served answers ok")
	}
	if got := (&Server{Reg: w.s.Reg}).primarySummary("apps/a"); got != nil {
		t.Errorf("NoopPolicy gives a summary: %+v", got)
	}
}
