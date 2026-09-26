package assetscan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, c := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

const page = `<!doctype html>
<html><head>
<link rel="stylesheet" href="/c/apps/t/style.css">
<script type="module" src="/c/apps/t/app.js"></script>
<script type="importmap">{"imports":{"ui/":"/c/apps/t/ui/"}}</script>
<style>body{background:url('/c/apps/t/img/bg.png')}</style>
</head><body>
<img src="/c/apps/t/img/logo.png?v=2" srcset="/c/apps/t/img/a.png 1x, /c/apps/t/img/b.png 2x">
<a href="/c/apps/t/help.html#top">help</a>
<div style="background:url(/c/apps/t/img/x.png)"></div>
<img src="/c/lib/ui/icon.svg">
<img src="/c/shell/logo.svg">
<img src="img/relative.png"><a href="#frag">x</a>
<script type="module">
import { a } from '/c/apps/t/mod.js';
const u = "/c/apps/t/data.json";
</script>
</body></html>`

func fixture(t *testing.T) (string, string) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"apps/t/xbin.json":         `{}`,
		"apps/t/index.html":        page,
		"apps/t/sub/deep.html":     `<html><head></head><body><img src="/c/apps/t/img/logo.png"><a href="/c/apps/t/">up</a></body></html>`,
		"apps/t/style.css":         `@import "/c/apps/t/base.css"; .x{background:url("/c/apps/t/img/x.png")} .y{background:url(img/ok.png)}`,
		"apps/t/app.js":            "import './dep.js';\nimport { m } from \"/c/apps/t/mod.js\";\nimport '/c/lib/ui/el.js';\nconst p = import(`/c/apps/t/lazy.js`);\nimg.src = '/c/apps/t/img/x.png';\n",
		"apps/t/based.html":        `<html><head><base href="/c/apps/t/assets/"></head><body><img src="/c/apps/t/x.png"></body></html>`,
		"apps/t/node_modules/x.js": `import '/c/apps/t/never.js'`,
		"apps/t/nested/xbin.json":  `{}`,
		"apps/t/nested/n.html":     `<img src="/c/apps/t/never.png">`,
		"lib/ui/xbin.json":         `{}`,
	})
	if err := os.Symlink("../../lib/ui/el.js", filepath.Join(root, "apps/t/escape.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("style.css", filepath.Join(root, "apps/t/alias.css")); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "apps/t")
}

func TestScan(t *testing.T) {
	root, dir := fixture(t)
	rep, err := Scan(dir, "apps/t", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	type key struct{ file, kind, ref string }
	got := map[key]Finding{}
	for _, f := range rep.Findings {
		got[key{f.File, f.Kind, f.Ref}] = f
		if strings.Contains(f.Ref, "never") {
			t.Errorf("scanned a skipped tree: %+v", f)
		}
	}
	want := []struct {
		k      key
		breaks string
		fix    string
	}{
		{key{"index.html", KindHTMLAttr, "/c/apps/t/style.css"}, BreaksTokens, "style.css"},
		{key{"index.html", KindHTMLAttr, "/c/apps/t/app.js"}, BreaksTokens, "app.js"},
		{key{"index.html", KindImportMap, "/c/apps/t/ui/"}, BreaksTokens, "./ui/"}, // a bare "ui/" is an invalid import-map address
		{key{"index.html", KindCSSURL, "/c/apps/t/img/bg.png"}, BreaksTokens, "img/bg.png"},
		{key{"index.html", KindHTMLAttr, "/c/apps/t/img/logo.png?v=2"}, BreaksTokens, "img/logo.png?v=2"},
		{key{"index.html", KindHTMLAttr, "/c/apps/t/img/a.png"}, BreaksTokens, "img/a.png"},
		{key{"index.html", KindHTMLAttr, "/c/apps/t/img/b.png"}, BreaksTokens, "img/b.png"},
		{key{"index.html", KindHTMLAttr, "/c/apps/t/help.html#top"}, BreaksTokens, "help.html#top"},
		{key{"index.html", KindCSSURL, "/c/apps/t/img/x.png"}, BreaksTokens, "img/x.png"},
		{key{"index.html", KindHTMLAttr, "/c/lib/ui/icon.svg"}, BreaksTokens, "../../lib/ui/icon.svg"},
		{key{"index.html", KindHTMLAttr, "/c/shell/logo.svg"}, BreaksStrict, ""},
		{key{"index.html", KindJSImport, "/c/apps/t/mod.js"}, "", "./mod.js"},
		{key{"index.html", KindJSString, "/c/apps/t/data.json"}, BreaksTokens, ""},
		{key{"sub/deep.html", KindHTMLAttr, "/c/apps/t/img/logo.png"}, BreaksTokens, "../img/logo.png"},
		{key{"sub/deep.html", KindHTMLAttr, "/c/apps/t/"}, BreaksTokens, "../"},
		{key{"style.css", KindCSSURL, "/c/apps/t/base.css"}, BreaksTokens, "base.css"},
		{key{"style.css", KindCSSURL, "/c/apps/t/img/x.png"}, BreaksTokens, "img/x.png"},
		{key{"app.js", KindJSImport, "/c/apps/t/mod.js"}, "", "./mod.js"},
		{key{"app.js", KindJSImport, "/c/lib/ui/el.js"}, BreaksTokens, "../../lib/ui/el.js"},
		{key{"app.js", KindJSImport, "/c/apps/t/lazy.js"}, "", "./lazy.js"},
		{key{"app.js", KindJSString, "/c/apps/t/img/x.png"}, BreaksTokens, ""},
		{key{"based.html", KindHTMLAttr, "/c/apps/t/x.png"}, BreaksTokens, ""}, // own <base>: manual
		{key{"escape.js", KindSymlinkEscape, ""}, BreaksStrict, ""},
	}
	for _, w := range want {
		f, ok := got[w.k]
		if !ok {
			t.Errorf("missing %+v", w.k)
			continue
		}
		if f.Breaks != w.breaks || f.Fix != w.fix {
			t.Errorf("%+v: breaks %q fix %q, want %q %q", w.k, f.Breaks, f.Fix, w.breaks, w.fix)
		}
		if f.Line == 0 && f.Kind != KindSymlinkEscape {
			t.Errorf("%+v: no line", w.k)
		}
	}
	if _, ok := got[key{"based.html", KindBaseTag, ""}]; !ok {
		t.Error("own <base> not reported")
	}
	// The in-tree symlink alias.css is scanned like style.css.
	if _, ok := got[key{"alias.css", KindCSSURL, "/c/apps/t/base.css"}]; !ok {
		t.Error("in-tree symlinked file not scanned")
	}
	for k := range got {
		if strings.Contains(k.ref, "relative.png") || k.ref == "#frag" {
			t.Errorf("relative reference reported: %+v", k)
		}
	}
	if n := rep.Breaking("tokens"); n < 15 {
		t.Errorf("breaking(tokens) = %d", n)
	}
	if n := rep.Breaking("origins"); n != 2 { // chrome ref + the escaping symlink
		t.Errorf("breaking(origins) = %d", n)
	}
}

func TestInjectFalseReported(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"apps/r/xbin.json": "{\n // raw\n \"inject\": false\n}", "apps/r/index.html": "<p>hi"})
	rep, err := Scan(filepath.Join(root, "apps/r"), "apps/r", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.InjectFalse || len(rep.Findings) != 1 || rep.Findings[0].Kind != KindInjectFalse || rep.Breaking("tokens") != 1 || rep.Breaking("origins") != 0 {
		t.Fatalf("%+v", rep)
	}
}

// The codemod rewrites exactly the fixable references and nothing else,
// and a second run finds nothing left to fix.
func TestPlanApply(t *testing.T) {
	root, dir := fixture(t)
	changes, _, err := Plan(dir, "apps/t", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	byFile := map[string]Change{}
	for _, c := range changes {
		byFile[c.File] = c
	}
	if _, ok := byFile["based.html"]; ok {
		t.Error("a document with its own <base> was rewritten")
	}
	idx := string(byFile["index.html"].After)
	for _, want := range []string{
		`href="style.css"`, `src="app.js"`, `"ui/":"./ui/"`, `url('img/bg.png')`, `src="img/logo.png?v=2"`,
		`srcset="img/a.png 1x, img/b.png 2x"`, `href="help.html#top"`, `url(img/x.png)`,
		`src="../../lib/ui/icon.svg"`, `src="/c/shell/logo.svg"`, `from './mod.js'`, `"/c/apps/t/data.json"`,
	} {
		if !strings.Contains(idx, want) {
			t.Errorf("index.html lacks %q:\n%s", want, idx)
		}
	}
	js := string(byFile["app.js"].After)
	if !strings.Contains(js, `from "./mod.js"`) || !strings.Contains(js, `import '../../lib/ui/el.js'`) ||
		!strings.Contains(js, "import(`./lazy.js`)") || !strings.Contains(js, `'/c/apps/t/img/x.png'`) {
		t.Errorf("app.js:\n%s", js)
	}
	if err := Apply(dir, changes); err != nil {
		t.Fatal(err)
	}
	again, _, err := Plan(dir, "apps/t", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(again) != 0 {
		t.Fatalf("second run still has fixes: %+v", again)
	}
	// A file edited between plan and apply is refused.
	write(t, root, map[string]string{"apps/t/x.html": `<img src="/c/apps/t/a.png">`})
	ch, _, _ := Plan(dir, "apps/t", Options{Root: root})
	write(t, root, map[string]string{"apps/t/x.html": `<img src="/c/apps/t/b.png">`})
	if err := Apply(dir, ch); err == nil {
		t.Fatal("applied over a concurrent edit")
	}
}

func TestRelURL(t *testing.T) {
	for _, c := range []struct{ dir, ref, want string }{
		{"/c/apps/t/", "/c/apps/t/x.js", "x.js"},
		{"/c/apps/t/", "/c/apps/t/", "./"},
		{"/c/apps/t/sub/", "/c/apps/t/", "../"},
		{"/c/apps/t/", "/c/apps/t/a/../b.js", "b.js"},
		{"/c/apps/t/", "/c/other/y.css?x#y", "../../other/y.css?x#y"},
		{"/c/apps/t/", "/c/apps/t/a:b.png", "./a:b.png"},
	} {
		if got := relURL(c.dir, c.ref, false); got != c.want {
			t.Errorf("relURL(%q,%q) = %q, want %q", c.dir, c.ref, got, c.want)
		}
	}
	if got := relURL("/c/apps/t/", "/c/apps/t/m.js", true); got != "./m.js" {
		t.Errorf("import: %q", got)
	}
}

// Import-map addresses the codemod writes stay valid: absolute, or
// starting with /, ./ or ../ (browsers null a bare "ui/" entry, so the
// rewrite would break the tile in every mode — review finding).
func TestImportMapRewriteStaysValid(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"apps/t/xbin.json":      `{}`,
		"apps/t/index.html":     `<script type="importmap">{"imports":{"@ui/":"/c/apps/t/ui/","x":"/c/apps/t/x.js","up/":"/c/lib/ui/"}}</script>`,
		"apps/t/sub/index.html": `<script type="importmap">{"imports":{"@ui/":"/c/apps/t/sub/ui/"}}</script>`,
		"lib/ui/xbin.json":      `{}`,
	})
	changes, _, err := Plan(filepath.Join(root, "apps/t"), "apps/t", Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, c := range changes {
		for _, e := range c.Edits {
			if e.Kind != KindImportMap {
				continue
			}
			got[e.Fix] = true
			if !strings.HasPrefix(e.Fix, "./") && !strings.HasPrefix(e.Fix, "../") && !strings.HasPrefix(e.Fix, "/") {
				t.Errorf("%s: invalid import-map address %q", c.File, e.Fix)
			}
		}
	}
	for _, want := range []string{"./ui/", "./x.js", "../../lib/ui/"} {
		if !got[want] {
			t.Errorf("missing rewrite to %q (got %v)", want, got)
		}
	}
}
