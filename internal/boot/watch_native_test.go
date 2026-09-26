package boot

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// An edit to a tile's native UI entry reloads the tile (its frames and its
// native runtime) without restarting its backend; any other change in the
// same batch still restarts it, as every edit always has.
func TestChangedComponentsNativeEntry(t *testing.T) {
	root := t.TempDir()
	for rel, content := range map[string]string{
		"apps/counter/xbin.json":       `{"runtime":"go"}`,
		"apps/counter/native.js":       `export {}`,
		"apps/counter/index.html":      `<p>web</p>`,
		"apps/cal/xbin.json":           `{"runtime":"go","native":"./mobile/main.js"}`,
		"apps/cal/mobile/main.js":      `export {}`,
		"apps/nodey/xbin.json":         `{"runtime":"node"}`,
		"apps/nodey/native.js":         `module.exports = {}`,
		"apps/counter/backend/main.go": `package main`,
		"apps/cal/native.js":           `// not cal's entry`,
		"apps/static/index.html":       `<p>static</p>`,
		"apps/static/native.js":        `export {}`,
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
		{[]string{"apps/counter/native.js"}, "apps/counter", ""},
		{[]string{"apps/cal/mobile/main.js"}, "apps/cal", ""},
		{[]string{"apps/cal/native.js"}, "apps/cal", "apps/cal"}, // not cal's entry
		{[]string{"apps/counter/native.js", "apps/counter/backend/main.go"}, "apps/counter", "apps/counter"},
		{[]string{"apps/counter/index.html"}, "apps/counter", "apps/counter"},
		{[]string{"apps/nodey/native.js"}, "apps/nodey", "apps/nodey"}, // a node backend may import it
		{[]string{"apps/counter/native.js", "apps/cal/xbin.json"}, "apps/cal,apps/counter", "apps/cal"},
		{[]string{"apps/static/native.js"}, "apps/static", ""},
		{[]string{"outside/file.txt"}, "", ""},
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
