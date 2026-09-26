package main

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseNativeArgs(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "")
	a, err := parseNativeArgs("preview", []string{"--native", "/c/apps/x/", "--dark", "--size=430x932", "--large-text", "--data", "d.json", "-o", "s.png", "--full", "--timeout", "45"})
	if err != nil {
		t.Fatal(err)
	}
	want := nativeArgs{cmd: "preview", native: true, tiles: []string{"apps/x"}, dark: true, width: 430, height: 932,
		largeText: true, data: "d.json", out: "s.png", full: true, timeout: 45 * time.Second}
	if !reflect.DeepEqual(a, want) {
		t.Fatalf("got %+v\nwant %+v", a, want)
	}
	a, err = parseNativeArgs("lint", []string{"--native", "apps/a", "apps/b", "--static", "--json", "--timeout", "2m"})
	if err != nil || !a.static || !a.json || len(a.tiles) != 2 || a.timeout != 2*time.Minute {
		t.Fatalf("lint: %+v %v", a, err)
	}
	if a, err := parseNativeArgs("tree", []string{"apps/x"}); err != nil || a.tiles[0] != "apps/x" || a.width != 390 || a.height != 844 {
		t.Fatalf("tree defaults: %+v %v", a, err)
	}

	bad := []struct {
		cmd  string
		args []string
		want string
	}{
		{"lint", []string{"apps/x"}, "only --native"},
		{"preview", []string{"apps/x"}, "only --native"},
		{"lint", []string{"--native", "--dark"}, "unknown flag --dark"},
		{"tree", []string{"apps/x", "--out", "f.png"}, "unknown flag --out"},
		{"preview", []string{"--native", "apps/x", "--size", "390"}, "--size wants"},
		{"preview", []string{"--native", "apps/x", "--size", "10x10"}, "--size wants"},
		{"preview", []string{"--native", "apps/x", "--timeout", "soon"}, "--timeout wants"},
		{"preview", []string{"--native", "apps/x", "--out"}, "needs a value"},
		{"preview", []string{"--native=1", "apps/x"}, "takes no value"},
		{"preview", []string{"--native", "apps/x", "apps/y"}, "one tile at a time"},
		{"tree", []string{}, "which tile"},
		{"tree", []string{"apps/../x"}, "bad tile path"},
		{"tree", []string{"/"}, "bad tile path"},
	}
	for _, b := range bad {
		if _, err := parseNativeArgs(b.cmd, b.args); err == nil || !strings.Contains(err.Error(), b.want) {
			t.Errorf("%s %v: err %v, want %q", b.cmd, b.args, err, b.want)
		}
	}

	t.Setenv("XBIN_COMPONENT", "apps/mine")
	if a, err := parseNativeArgs("preview", []string{"--native"}); err != nil || a.tiles[0] != "apps/mine" {
		t.Fatalf("terminal default tile: %+v %v", a, err)
	}
}

func TestLoadFixture(t *testing.T) {
	dir := t.TempDir()
	must := func(p, s string) {
		if err := os.WriteFile(filepath.Join(dir, p), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("data.json", `{"now": 1, "routes": {}}`)
	must("steps.json", `[{"tap": "r.0"}]`)
	d, s, err := loadFixture(dir, "")
	if err != nil || !strings.Contains(string(d), `"now": 1`) || string(s) != `[{"tap": "r.0"}]` {
		t.Fatalf("dir: %s %s %v", d, s, err)
	}
	must("inline.json", `{"routes": {}, "steps": [{"wait": 5}]}`)
	if _, s, err := loadFixture(filepath.Join(dir, "inline.json"), ""); err != nil || string(s) != `[{"wait": 5}]` {
		t.Fatalf("inline steps: %s %v", s, err)
	}
	if _, s, err := loadFixture(filepath.Join(dir, "inline.json"), filepath.Join(dir, "steps.json")); err != nil || !strings.Contains(string(s), "tap") {
		t.Fatalf("--steps wins: %s %v", s, err)
	}
	must("arr.json", `[1]`)
	if _, _, err := loadFixture(filepath.Join(dir, "arr.json"), ""); err == nil {
		t.Fatal("a non-object data file must fail")
	}
	if _, _, err := loadFixture("", filepath.Join(dir, "data.json")); err == nil {
		t.Fatal("a non-array steps file must fail")
	}
	if _, _, err := loadFixture(filepath.Join(dir, "nope.json"), ""); err == nil {
		t.Fatal("a missing data file must fail")
	}
}

func TestJSImports(t *testing.T) {
	src := "// import './commented.js';\n" +
		"/* import './block.js' */\n" +
		"import { html, render } from '/vendor/xb-native.js';\n" +
		"import {\n  a,\n  b,\n} from \"./model.js\";\n" +
		"import './side.js';\n" +
		"import * as fmt from './fmt.js'\n" +
		"export { x } from './re.js';\n" +
		"export * from './all.js';\n" +
		"const re = /['\"]import '.\\/regex.js'/g;\n" +
		"const s = 'http://x // not a comment';\n" +
		"const t = html`<text>import './in-template.js'</text>${await import('./lazy.js')}`;\n" +
		"obj.import('./method.js');\n" +
		"const u = import.meta.url;\n" +
		"import def, { y } from 'lit';\n" +
		"import './model.js';\n"
	var got []string
	lines := map[string]int{}
	for _, imp := range jsImports(jsScan(src)) {
		got = append(got, imp.Spec)
		lines[imp.Spec] = imp.Line
	}
	want := []string{"/vendor/xb-native.js", "./model.js", "./side.js", "./fmt.js", "./re.js", "./all.js", "./lazy.js", "lit"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("imports\n got %q\nwant %q", got, want)
	}
	if lines["./model.js"] != 7 || lines["./lazy.js"] != 14 || lines["lit"] != 17 { // the specifier's line
		t.Fatalf("lines: %v", lines)
	}
}

func TestRawColours(t *testing.T) {
	src := "const RED = '#ff3b30';\n" +
		"const label = '#1 item';\n" +
		"const css = `color: #fff`;\n" +
		"render(html`<row title=\"#1 item\" tone=\"#f00\"\n" +
		"  detail=${x} subtitle='rgb(1, 2, 3)'/><text tone=accent>ok</text><badge text=\"#bad\"/>`);\n"
	type hit struct {
		line  int
		attr  string
		value string
	}
	var got []hit
	for _, h := range rawColours(jsScan(src), true) {
		got = append(got, hit{h.Line, h.Attr, h.Value})
	}
	want := []hit{{4, "tone", "#f00"}, {5, "subtitle", "rgb(1, 2, 3)"}, {5, "text", "#bad"}, {1, "", "#ff3b30"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("colours\n got %+v\nwant %+v", got, want)
	}
	if n := len(rawColours(jsScan(src), false)); n != 3 {
		t.Fatalf("a non-entry module's string literals are not checked: %d hits", n)
	}
}

func TestImportMapResolve(t *testing.T) {
	m := importMap{Imports: map[string]string{"lit": "/vendor/lit.js", "lit/": "/vendor/lit/", "lit/directives/": "/vendor/lit-dir/"}}
	for spec, want := range map[string]string{"lit": "/vendor/lit.js", "lit/x.js": "/vendor/lit/x.js", "lit/directives/r.js": "/vendor/lit-dir/r.js"} {
		if got, ok := m.resolve(spec); !ok || got != want {
			t.Errorf("%s → %q %v, want %q", spec, got, ok, want)
		}
	}
	if _, ok := m.resolve("left-pad"); ok {
		t.Error("left-pad resolved")
	}
}

// fakeXbind answers a static lint's GETs from a map (path → status, body).
type fakeXbind map[string]struct {
	st   int
	body string
}

func (f fakeXbind) get(p string) (int, []byte, error) {
	if r, ok := f[p]; ok {
		return r.st, []byte(r.body), nil
	}
	return 404, []byte("404 page not found"), nil
}

const fakeDoc = `<!doctype html><html><head>
<script type="importmap">{"imports":{"lit":"/vendor/lit.js"}}</script>
<meta name="xbin-native" content="1"></head></html>`

func levels(fs []lintFinding) string {
	var b []string
	for _, f := range fs {
		b = append(b, f.Level+":"+f.Message)
	}
	return strings.Join(b, "\n")
}

func has(fs []lintFinding, level, substr string) bool {
	for _, f := range fs {
		if f.Level == level && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

func TestStackWhere(t *testing.T) {
	for stack, want := range map[string]string{
		"Error: x\n    at /c/apps/x/late.js:1:7":                     "/c/apps/x/late.js:1:7",
		"Error: x\n    at load (/c/apps/x/native.js:12:3)\n    at y": "/c/apps/x/native.js:12:3",
		"Error: x":                     "",
		"Error: x\n    at <anonymous>": "",
	} {
		if got := stackWhere(stack); got != want {
			t.Errorf("%q → %q, want %q", stack, got, want)
		}
	}
}

func TestParseNodeCheck(t *testing.T) {
	out := "/tmp/bx-check-1.mjs:2\nrender(html`<screen>`;\n                     ^\n\nSyntaxError: missing ) after argument list\n    at wrapSafe (node:internal/modules/cjs/loader:1)\n\nNode.js v26.7.0\n"
	if line, msg := parseNodeCheck(out, "/tmp/bx-check-1.mjs"); line != 2 || msg != "missing ) after argument list" {
		t.Fatalf("got %d %q", line, msg)
	}
}

func TestStaticLint(t *testing.T) {
	jsSyntax = func(src []byte) (int, string) {
		if strings.Contains(string(src), "SYNTAX") {
			return 3, "Unexpected identifier 'SYNTAX'"
		}
		return 0, ""
	}
	defer func() { jsSyntax = nodeSyntax }()
	native := func(e string) *struct {
		Entry string `json:"entry"`
	} {
		return &struct {
			Entry string `json:"entry"`
		}{e}
	}
	good := fakeXbind{
		"/c/apps/x/?native=1":   {200, fakeDoc},
		"/c/apps/x/native.js":   {200, "import { html, render } from '/vendor/xb-native.js';\nimport { f } from './lib/fmt.js';\nimport 'lit';\n"},
		"/c/apps/x/lib/fmt.js":  {200, "export { g } from '../model.js';\nexport const f = 1;"},
		"/c/apps/x/model.js":    {200, "export const g = 2;"},
		"/vendor/xb-native.js":  {200, "export const html = 1"},
		"/vendor/lit.js":        {200, "export const x = 1"},
		"/c/apps/y/?native=1":   {404, "this tile has no native app UI (no native.js, and no \"native\" in its xbin.json)"},
		"/c/apps/bad/?native=1": {404, "native entry \"./mobile.js\": no such file"},
	}
	fs := staticLint(good.get, nativeComp{Path: "apps/x", Native: native("native.js")})
	if len(fs) != 1 || !has(fs, "ok", "3 module(s), 4 import(s) resolve") {
		t.Fatalf("good tile:\n%s", levels(fs))
	}
	if fs := staticLint(good.get, nativeComp{Path: "apps/y"}); len(fs) != 1 || !has(fs, "info", "no native UI") {
		t.Fatalf("web-only tile:\n%s", levels(fs))
	}
	if fs := staticLint(good.get, nativeComp{Path: "apps/bad", ManifestErr: "bad json"}); !has(fs, "error", "bad json") || !has(fs, "error", "no such file") {
		t.Fatalf("bad declaration:\n%s", levels(fs))
	}

	broken := fakeXbind{
		"/c/apps/x/?native=1": {200, fakeDoc},
		"/c/apps/x/native.js": {200, "import { m } from './missing.js';\nimport 'left-pad';\nimport '/vendor/nope.js';\nimport '../other/x.js';\nimport 'https://cdn.example/x.js';\nrender(html`<row tone=\"#abcdef\"/>`);\n"},
		"/c/apps/other/x.js":  {200, ""},
	}
	fs = staticLint(broken.get, nativeComp{Path: "apps/x", Native: native("native.js")})
	for _, w := range []struct{ level, substr string }{
		{"error", "import missing.js does not resolve (HTTP 404)"},
		{"error", `bare import "left-pad"`},
		{"error", "import /vendor/nope.js does not resolve"},
		{"warn", "reaches into another tile"},
		{"warn", "remote import https://cdn.example/x.js"},
		{"warn", `raw colour tone="#abcdef"`},
		{"warn", "nothing imports /vendor/xb-native.js"},
	} {
		if !has(fs, w.level, w.substr) {
			t.Errorf("broken tile: no %s %q in\n%s", w.level, w.substr, levels(fs))
		}
	}
	if has(fs, "ok", "") {
		t.Errorf("a tile with errors has no ok line:\n%s", levels(fs))
	}
	syntax := fakeXbind{"/c/apps/x/?native=1": {200, fakeDoc}, "/c/apps/x/native.js": {200, "import './m.js';"}, "/c/apps/x/m.js": {200, "a\nb\nSYNTAX here"}}
	if fs := staticLint(syntax.get, nativeComp{Path: "apps/x", Native: native("native.js")}); !has(fs, "error", "syntax: Unexpected identifier") || fs[0].Where != "m.js:3" {
		t.Fatalf("syntax error:\n%s", levels(fs))
	}
	missing := fakeXbind{"/c/apps/x/?native=1": {200, fakeDoc}}
	if fs := staticLint(missing.get, nativeComp{Path: "apps/x", Native: native("native.js")}); !has(fs, "error", "native entry native.js: HTTP 404") {
		t.Fatalf("missing entry:\n%s", levels(fs))
	}
	forbidden := fakeXbind{"/c/apps/x/?native=1": {403, "not permitted to use this tile"}}
	if fs := staticLint(forbidden.get, nativeComp{Path: "apps/x", Native: native("native.js")}); !has(fs, "error", "HTTP 403 not permitted") {
		t.Fatalf("forbidden:\n%s", levels(fs))
	}
}

func TestLendsCredential(t *testing.T) {
	tiles := []string{"apps/x"}
	known := []string{"apps/x", "apps/x/inner", "apps/xy", "apps/other"}
	for _, c := range []struct {
		method, path string
		want         bool
	}{
		{"GET", "/c/apps/x/", true},
		{"GET", "/c/apps/x", true},
		{"HEAD", "/c/apps/x/native.js", true},
		{"GET", "/c/apps/x/lib/model.js", true},
		{"POST", "/c/apps/x/native.js", false},
		{"GET", "/c/apps/x/inner/secret.json", false}, // a nested component is not the tile
		{"GET", "/c/apps/xy/index.html", false},       // a sibling sharing the prefix
		{"GET", "/c/apps/other/?native=1", false},
		{"GET", "/c/apps/x/../other/native.js", false},
		{"GET", "/c/apps/x//native.js", false},
		{"GET", "/api/apps/x/count", false},
		{"GET", "/api/xbin/whoami", false},
		{"GET", "/vendor/xb-native.js", false},
		{"GET", "/ws/events", false},
	} {
		if got := lendsCredential(c.method, c.path, tiles, known); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestNativeProxy: the proxy forwards everything to xbind (bx's transport),
// adds bx's token only where lendsCredential says, and never forwards cookies.
func TestNativeProxy(t *testing.T) {
	var mu sync.Mutex
	seen := map[string][2]string{} // path → [authorization, cookie]
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen[r.URL.Path] = [2]string{r.Header.Get("Authorization"), r.Header.Get("Cookie")}
		mu.Unlock()
		_, _ = w.Write([]byte("ok " + r.Host))
	}))
	defer up.Close()
	t.Setenv("XBIN_URL", up.URL)
	t.Setenv("XBIN_TOKEN", "sekrit")
	t.Setenv("XBIN_GATEWAY", "")
	base, stop, err := startNativeProxy([]string{"apps/x"}, []string{"apps/x", "apps/y"})
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	do := func(method, p, auth string) {
		rq, _ := http.NewRequest(method, base+p, nil)
		rq.Header.Set("Cookie", "xbin_session=abc")
		if auth != "" {
			rq.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(rq)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	do("GET", "/c/apps/x/", "")
	do("GET", "/c/apps/y/", "")
	do("GET", "/api/apps/x/count", "")
	do("POST", "/api/apps/y/count", "Bearer tile-own")
	mu.Lock()
	defer mu.Unlock()
	want := map[string][2]string{
		"/c/apps/x/":        {"Bearer sekrit", ""},
		"/c/apps/y/":        {"", ""},
		"/api/apps/x/count": {"", ""},
		"/api/apps/y/count": {"Bearer tile-own", ""}, // the page's own header passes untouched
	}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("upstream saw %v\nwant %v", seen, want)
	}
}

func TestRuntimeFindings(t *testing.T) {
	ms := 6200.0
	r := &probeResult{Tile: "apps/x", Tree: []byte(`{"v":1,"root":{"k":"r","t":"screen"}}`), FirstTreeMs: &ms,
		Unknown: []string{"blink", "marquee"}, Needs: map[string]int{"screen": 1, "row": 1, "chart": 2},
		Features: []string{"chart.area"}, Warnings: []string{"still waiting on GET /api/apps/x/slow"},
		Errors:      []probeMsg{{Kind: "exception", Message: "boom", Where: "native.js:3"}},
		Diagnostics: []probeMsg{{Level: "error", Code: "unknown-tag", Message: "<blink> is not a primitive of the vocabulary"}, {Level: "warn", Code: "bad-token", Message: "<row> tone: #f00"}, {Level: "info", Code: "unvalidated", Message: "x"}}}
	r.Stats.Nodes, r.Stats.Depth, r.Stats.Bytes = 1, 1, 2048
	fs := runtimeFindings(r)
	for _, w := range []struct{ level, substr string }{
		{"error", "exception: boom"},
		{"error", "unknown-tag"},
		{"warn", "bad-token"},
		{"info", "unvalidated"},
		{"error", "doesn't know: marquee"},
		{"warn", "still waiting"},
		{"warn", "first tree after 6200 ms"},
		{"ok", "1 nodes, depth 1, 2.0 KiB"},
		{"info", "rev 1: row screen; rev 2: chart"},
		{"info", "features: chart.area"},
	} {
		if !has(fs, w.level, w.substr) {
			t.Errorf("no %s %q in\n%s", w.level, w.substr, levels(fs))
		}
	}
	if has(fs, "error", "doesn't know: blink") {
		t.Errorf("blink is reported twice:\n%s", levels(fs))
	}
	if !runtimeFailed(r) {
		t.Error("runtimeFailed = false")
	}
	gone := &probeResult{Tile: "apps/x", Status: 404, LoadError: "this tile has no native app UI"}
	if fs := runtimeFindings(gone); len(fs) != 1 || !has(fs, "error", "HTTP 404") {
		t.Fatalf("load error:\n%s", levels(fs))
	}
	empty := &probeResult{Tile: "apps/x"}
	if fs := runtimeFindings(empty); !has(fs, "error", "rendered nothing") {
		t.Fatalf("no tree:\n%s", levels(fs))
	}
}

func TestCoverage(t *testing.T) {
	reps := []tileLint{
		{Tile: "apps/a", Entry: "native.js", Findings: []lintFinding{{Level: "ok"}}},
		{Tile: "apps/b", Entry: "native.js", Findings: []lintFinding{{Level: "warn"}, {Level: "ok"}}},
		{Tile: "apps/c", Entry: "native.js", Findings: []lintFinding{{Level: "error"}}},
		{Tile: "apps/d", Findings: []lintFinding{{Level: "info"}}},
	}
	c := coverage(reps, true)
	if c.Tiles != 4 || len(c.Native) != 3 || !reflect.DeepEqual(c.Clean, []string{"apps/a"}) ||
		!reflect.DeepEqual(c.Warn, []string{"apps/b"}) || !reflect.DeepEqual(c.Errors, []string{"apps/c"}) ||
		!reflect.DeepEqual(c.WebOnly, []string{"apps/d"}) {
		t.Fatalf("%+v", c)
	}
	if e := coverage(nil, true); e.Native == nil || e.WebOnly == nil || e.Warn == nil { // JSON [] not null
		t.Fatalf("empty coverage has nil lists: %+v", e)
	}
}

// Inside a component sandbox bx reaches xbind over the XBIN_GATEWAY unix
// socket; Chromium can't, so the proxy carries it.
func TestNativeProxyGateway(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix sockets: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(r.Host + " " + r.URL.Path + " " + r.Header.Get("Authorization")))
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	t.Setenv("XBIN_URL", "")
	t.Setenv("XBIN_GATEWAY", sock)
	t.Setenv("XBIN_TOKEN", "inst")
	base, stop, err := startNativeProxy([]string{"apps/x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	resp, err := http.Get(base + "/c/apps/x/native.js")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "xbin /c/apps/x/native.js Bearer inst" {
		t.Fatalf("through the gateway: %q", b)
	}
}

func TestRunProbeWithoutNode(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := runProbe(probeConfig{Tiles: []string{"apps/x"}}, nil)
	var nb *errNoBrowser
	if !errors.As(err, &nb) || !strings.Contains(err.Error(), "node was not found") {
		t.Fatalf("err = %v", err)
	}
}

// The embedded probe is the file on disk (go:embed), and it parses.
func TestProbeEmbedded(t *testing.T) {
	b, err := os.ReadFile("native-probe.mjs")
	if err != nil || string(b) != string(nativeProbeJS) {
		t.Fatalf("embed mismatch: %v", err)
	}
}
