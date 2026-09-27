package builtins

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/deps"
	"github.com/xbin-dev/xbin/internal/registry"
)

// Every shipped builtin tile imports (`bx tile import <name>`) into a
// workspace whose registry reads it as a component with a clean manifest —
// its exposes included — and a Go backend gets its go.mod back, with a `go`
// line the workspace's generated go.work covers (a module above it fails
// the build: "module . listed in go.work file requires go >= …").
func TestBuiltinTilesImport(t *testing.T) {
	set, err := Load(xbin.BuiltinTilesFS())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	paths := map[string]string{}
	for _, m := range set.List() {
		p, written, err := set.Import(root, m.Name, "")
		if err != nil || len(written) == 0 {
			t.Fatalf("import %s: %v (%d files)", m.Name, err, len(written))
		}
		paths[m.Name] = p
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	for name, p := range paths {
		c, ok := reg.Component(p)
		if !ok {
			t.Errorf("%s: no component at %s after the import", name, p)
			continue
		}
		if c.ManifestErr != "" {
			t.Errorf("%s: %s", name, c.ManifestErr)
		}
		if _, err := os.Stat(filepath.Join(root, p, "backend")); err == nil {
			if _, err := os.Stat(filepath.Join(root, p, "go.mod")); err != nil {
				t.Errorf("%s: a Go backend without its go.mod: %v", name, err)
			}
		}
	}
	if c, _ := reg.Component(paths["sandbox-terminal"]); c == nil || c.Manifest.Exposes["ssh"].Port != 2222 {
		t.Errorf("sandbox-terminal's ssh expose: %+v", c)
	}
	goLine := regexp.MustCompile(`(?m)^go ([0-9.]+)\s*$`)
	work := goLine.FindStringSubmatch(deps.GoWorkFor(reg, "", func(string) bool { return true }))
	if work == nil {
		t.Fatal("the generated go.work has no go line")
	}
	for name, p := range paths {
		b, err := os.ReadFile(filepath.Join(root, p, "go.mod"))
		if err != nil {
			continue
		}
		if m := goLine.FindSubmatch(b); m != nil && goVersionLess(work[1], string(m[1])) {
			t.Errorf("%s: go.mod says go %s, above the generated go.work's go %s — the backend won't build in a workspace", name, m[1], work[1])
		}
	}
}

// goVersionLess orders go lines as the go command does: numerically by
// part, and a language version ("1.24") before its releases ("1.24.0").
func goVersionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return len(pa) < len(pb)
}
