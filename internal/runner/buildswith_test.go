package runner

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers D246 — BuildsWith names the tiles whose modules a tile's build
// takes, transitively (a library user's library too), as the build's own
// go.work has them; a tile that doesn't build from Go, and one nothing
// chooses, take none.
func TestBuildsWith(t *testing.T) {
	root := t.TempDir()
	write := func(rel, s string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("apps/lib/xbin.json", `{"runtime":"go"}`)
	write("apps/lib/go.mod", "module lib\n\ngo 1.24\n")
	write("apps/lib/lib.go", "package lib\n")
	write("apps/user/xbin.json", `{"runtime":"go"}`)
	write("apps/user/go.mod", "module user\n\ngo 1.24\n\nrequire lib v0.0.0\n")
	write("apps/user/backend/main.go", "package main\n\nimport _ \"lib\"\n\nfunc main() {}\n")
	write("apps/top/xbin.json", `{"runtime":"go"}`)
	write("apps/top/go.mod", "module top\n\ngo 1.24\n\nrequire user v0.0.0\n")
	write("apps/top/backend/main.go", "package main\n\nimport _ \"user/x\"\n\nfunc main() {}\n")
	write("apps/top/x.go", "package top\n")
	write("apps/alone/xbin.json", `{"runtime":"go"}`)
	write("apps/alone/go.mod", "module alone\n\ngo 1.24\n")
	write("apps/alone/backend/main.go", "package main\n\nfunc main() {}\n")
	write("apps/py/xbin.json", `{"runtime":"python","deps":["apps/lib"]}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	r := &Runner{Root: root, Reg: reg}
	of := func(tile string) []string {
		c, ok := reg.Component(tile)
		if !ok {
			t.Fatalf("%s isn't registered", tile)
		}
		return r.BuildsWith(c)
	}
	if got := of("apps/user"); !slices.Equal(got, []string{"apps/lib"}) {
		t.Errorf("apps/user builds with %v", got)
	}
	if got := of("apps/top"); !slices.Equal(got, []string{"apps/lib", "apps/user"}) {
		t.Errorf("apps/top builds with %v (its library's library too)", got)
	}
	for _, tile := range []string{"apps/alone", "apps/py", "apps/lib"} {
		if got := of(tile); len(got) != 0 {
			t.Errorf("%s builds with %v", tile, got)
		}
	}
}
