package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The native-app UI entry (docs/elements.md §Native app UI): native.js by
// convention, or the manifest's "native" path; false opts out; anything
// else is ignored — never a manifest error, since old tiles may carry the
// key for their own reasons and must keep loading exactly as before.
func TestNativeEntryResolution(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/conv/xbin.json":       `{"runtime":"go"}`,
		"apps/conv/native.js":       `export {}`,
		"apps/none/xbin.json":       `{}`,
		"apps/none/index.html":      `<p>web only</p>`,
		"apps/decl/xbin.json":       `{"native": "./mobile/main.js"}`,
		"apps/decl/mobile/main.js":  `export {}`,
		"apps/decl/native.js":       `// not the entry`,
		"apps/missing/xbin.json":    `{"native": "mobile/gone.js"}`,
		"apps/off/xbin.json":        `{"native": false}`,
		"apps/off/native.js":        `export {}`,
		"apps/on/xbin.json":         `{"native": true}`,
		"apps/on/native.js":         `export {}`,
		"apps/weird/xbin.json":      `{"runtime": "go", "native": {"ios": "x.js"}}`,
		"apps/weird/native.js":      `export {}`,
		"apps/escape/xbin.json":     `{"native": "../conv/native.js"}`,
		"apps/nodebe/xbin.json":     `{"runtime": "node", "entry": "./native.js"}`,
		"apps/nodebe/native.js":     `require('http')`,
		"apps/dir/xbin.json":        `{}`,
		"apps/dir/native.js/x":      `a directory named native.js is not a module`,
		"apps/onlynative/xbin.json": `{}`,
		"apps/onlynative/native.js": `export {}`,
	})
	reg, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		comp, want, errHas string
	}{
		{"apps/conv", "native.js", ""},
		{"apps/none", "", ""},
		{"apps/decl", "mobile/main.js", ""},
		{"apps/missing", "", "no such file"},
		{"apps/off", "", ""},
		{"apps/on", "native.js", ""},
		{"apps/weird", "native.js", "ignored"},
		{"apps/escape", "", "not allowed"},
		{"apps/nodebe", "", ""},
		{"apps/dir", "", ""},
		{"apps/onlynative", "native.js", ""},
	}
	for _, c := range cases {
		comp, ok := reg.Component(c.comp)
		if !ok {
			t.Fatalf("%s: not registered", c.comp)
		}
		if comp.ManifestErr != "" {
			t.Errorf("%s: native must never be a manifest error, got %q", c.comp, comp.ManifestErr)
		}
		if comp.Native != c.want {
			t.Errorf("%s: Native = %q, want %q", c.comp, comp.Native, c.want)
		}
		if c.errHas == "" && comp.NativeErr != "" || !strings.Contains(comp.NativeErr, c.errHas) {
			t.Errorf("%s: NativeErr = %q, want it to contain %q", c.comp, comp.NativeErr, c.errHas)
		}
	}
	// The rest of a manifest with an odd "native" still parses.
	if w, _ := reg.Component("apps/weird"); w.Manifest.Runtime != "go" || !w.HasBackend() {
		t.Fatalf("weird native value must not disturb the manifest: %+v", w.Manifest)
	}
}

func TestCleanNativeEntry(t *testing.T) {
	ok := map[string]string{
		"native.js":        "native.js",
		"./native.js":      "native.js",
		"./mobile/main.js": "mobile/main.js",
		"mobile/app.mjs":   "mobile/app.mjs",
		" ./ui/v2-app.js ": "ui/v2-app.js",
		"a/b_c/d~e+f@g.js": "a/b_c/d~e+f@g.js",
	}
	for in, want := range ok {
		got, err := CleanNativeEntry(in)
		if err != nil || got != want {
			t.Errorf("CleanNativeEntry(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"", "./", "/abs/native.js", "../x.js", "a/../../x.js", "a/./x.js",
		".git/x.js", "mobile/.hidden.js", `a\b.js`, "native.ts", "native",
		"a b.js", "a?.js", "a#b.js", `a".js`, "a//b.js", "</script>.js",
	} {
		if got, err := CleanNativeEntry(bad); err == nil {
			t.Errorf("CleanNativeEntry(%q) = %q, want an error", bad, got)
		}
	}
}

// An edit to the native entry alone reloads views but restarts no backend
// — except where a backend might read the file (the undeclared convention
// under node, or under a go package at the tile root), which keeps today's
// restart-on-every-edit.
func TestNativeOnlyChange(t *testing.T) {
	mk := func(js string) *Component {
		c := &Component{Path: "apps/x"}
		if err := jsonc.Unmarshal([]byte(js), &c.Manifest); err != nil {
			t.Fatal(err)
		}
		return c
	}
	cases := []struct {
		manifest, rel string
		want          bool
	}{
		{`{}`, "native.js", true},
		{`{}`, "./native.js", true},
		{`{}`, "index.html", false},
		{`{}`, "model.js", false}, // shared modules may be the backend's too
		{`{"runtime":"go"}`, "native.js", true},
		{`{"runtime":"go","entry":"./backend"}`, "native.js", true},
		{`{"runtime":"go","entry":"."}`, "native.js", false},
		{`{"runtime":"go","entry":"./"}`, "native.js", false},
		{`{"runtime":"go","entry":".","native":"native.js"}`, "native.js", true},
		{`{"runtime":"python"}`, "native.js", true},
		{`{"runtime":"node"}`, "native.js", false},
		{`{"runtime":"node","native":"./native.js"}`, "native.js", true},
		{`{"runtime":"node","native":"./native.js","entry":"native.js"}`, "native.js", false},
		{`{"native":"mobile/main.js"}`, "mobile/main.js", true},
		{`{"native":"mobile/main.js"}`, "native.js", false},
		{`{"native":false}`, "native.js", false},
		{`{"native":"../escape.js"}`, "native.js", false},
		{`{"native":42}`, "native.js", true}, // ignored → the convention
	}
	for _, c := range cases {
		if got := mk(c.manifest).NativeOnlyChange(c.rel); got != c.want {
			t.Errorf("%s: NativeOnlyChange(%q) = %v, want %v", c.manifest, c.rel, got, c.want)
		}
	}
}
