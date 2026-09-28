package boot

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers D119c PO-12 — today's mapping of a change batch to components with
// nested tiles: a path belongs to its nearest enclosing component only, so an
// edit in a nested tile reloads and restarts that tile and never its parent;
// a batch spanning tiles maps to each; paths under xbind's stores and
// reserved directories map to no component.
func TestChangedComponentsNesting(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"apps/a/xbin.json":           `{"runtime":"go"}`,
		"apps/a/backend/main.go":     `package main`,
		"apps/a/lib/util.js":         `export {}`, // a plain subdirectory of a
		"apps/a/b/xbin.json":         `{"runtime":"node"}`,
		"apps/a/b/x.js":              `module.exports = {}`,
		"apps/a/b/c/deep.js":         `module.exports = {}`, // a plain subdirectory of b
		"apps/a/b/n/xbin.json":       `{"runtime":"go","native":"./native.js"}`,
		"apps/a/b/n/native.js":       `export {}`,
		"apps/a/docs/index.html":     `<p>docs</p>`, // a static tile nested in a go tile
		"apps/cal/xbin.json":         `{"runtime":"go"}`,
		"apps/cal/index.html":        `<p>cal</p>`,
		"notes/index.html":           `<p>notes</p>`,
		"data/deployments/k.json":    `{}`,
		".xbin/deploy/k/h/xbin.json": `{"runtime":"go"}`, // a materialized tree: never a component
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
	keys := func(m map[string]bool) string {
		var out []string
		for k, v := range m {
			if v {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return strings.Join(out, ",")
	}
	cases := []struct {
		paths               []string
		wantReload, restart string
	}{
		// The nearest enclosing component only.
		{[]string{"apps/a/b/x.js"}, "apps/a/b", "apps/a/b"},
		{[]string{"apps/a/b/c/deep.js"}, "apps/a/b", "apps/a/b"},
		{[]string{"apps/a/b/xbin.json"}, "apps/a/b", "apps/a/b"},
		{[]string{"apps/a/b"}, "apps/a/b", "apps/a/b"}, // a directory event names the tile itself
		{[]string{"apps/a/backend/main.go"}, "apps/a", "apps/a"},
		{[]string{"apps/a/lib/util.js"}, "apps/a", "apps/a"},
		{[]string{"apps/a/b/n/native.js"}, "apps/a/b/n", ""}, // a native-only edit, two levels down
		{[]string{"apps/a/docs/index.html"}, "apps/a/docs", "apps/a/docs"},
		// Batches spanning tiles map to each, parent and child independently.
		{[]string{"apps/a/b/x.js", "apps/cal/index.html"}, "apps/a/b,apps/cal", "apps/a/b,apps/cal"},
		{[]string{"apps/a/backend/main.go", "apps/a/b/x.js"}, "apps/a,apps/a/b", "apps/a,apps/a/b"},
		{[]string{"apps/a/b/n/native.js", "apps/a/b/x.js"}, "apps/a/b,apps/a/b/n", "apps/a/b"},
		{[]string{"apps/a/b/x.js", "apps/a/b/c/deep.js", "apps/a/b/xbin.json"}, "apps/a/b", "apps/a/b"},
		{[]string{"notes/index.html", "apps/a/docs/index.html", "apps/cal/xbin.json"},
			"apps/a/docs,apps/cal,notes", "apps/a/docs,apps/cal,notes"},
		// xbind's stores, reserved directories and unknown paths: nothing.
		{[]string{"data", ".xbin", "data/deployments/k.json", ".xbin/deploy/k/h/xbin.json"}, "", ""},
		{[]string{"data/checkpoints/k.git/HEAD", "data/checkpoints/k.view.git/info/refs"}, "", ""},
		{[]string{"apps", "apps/README.md", "outside/file.txt"}, "", ""},
	}
	for _, c := range cases {
		reload, restart := changedComponents(reg, c.paths)
		rl := map[string]bool{}
		for p, comp := range reload {
			if comp.Path != p {
				t.Fatalf("reload key %q holds %q", p, comp.Path)
			}
			rl[p] = true
		}
		if got := keys(rl); got != c.wantReload {
			t.Errorf("%v: reload %q, want %q", c.paths, got, c.wantReload)
		}
		if got := keys(restart); got != c.restart {
			t.Errorf("%v: restart %q, want %q", c.paths, got, c.restart)
		}
	}
}
