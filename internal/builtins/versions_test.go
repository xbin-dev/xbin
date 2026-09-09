package builtins

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin"
)

// A builtin tile that changes must bump its tile.json version: that number
// is what `bx builtin updates` compares against a workspace's recorded
// copy, so an edit without a bump is an update nobody is offered.
// hack/tile-versions.txt records each tile's version and a rollup hash of
// its files (tile.json excluded); this test recomputes the hash and, when
// it differs, requires a higher version — then the baseline moves:
//
//	UPDATE_TILE_VERSIONS=1 go test ./internal/builtins -run TestTileVersions
func TestTileVersions(t *testing.T) {
	base := filepath.Join("..", "..", "hack", "tile-versions.txt")
	prev := map[string][2]string{} // name → {version, hash}
	if b, err := os.ReadFile(base); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 3 && !strings.HasPrefix(f[0], "#") {
				prev[f[0]] = [2]string{f[1], f[2]}
			}
		}
	}
	set, err := Load(xbin.BuiltinTilesFS())
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, m := range set.List() {
		hashes := map[string]string{}
		err := fs.WalkDir(xbin.BuiltinTilesFS(), m.Name, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || inTile(p) == "tile.json" {
				return err
			}
			b, err := fs.ReadFile(xbin.BuiltinTilesFS(), p)
			if err != nil {
				return err
			}
			hashes[p] = sha(b)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		h := rollup(hashes)
		v := fmt.Sprint(m.Version)
		if p, ok := prev[m.Name]; ok && p[1] != h && p[0] >= v {
			t.Errorf("builtin tile %s changed (its files no longer match hack/tile-versions.txt) but tile.json still says version %s — bump the version, note the change in its changelog, then UPDATE_TILE_VERSIONS=1 go test ./internal/builtins -run TestTileVersions", m.Name, v)
		}
		lines = append(lines, fmt.Sprintf("%s %s %s", m.Name, v, h))
	}
	sort.Strings(lines)
	want := "# hack/tile-versions.txt — every builtin tile's tile.json version and a rollup hash of\n# its files (tile.json excluded). internal/builtins TestTileVersions fails when a tile's\n# files change without a version bump; regenerate with UPDATE_TILE_VERSIONS=1 (see the test).\n" + strings.Join(lines, "\n") + "\n"
	got, _ := os.ReadFile(base)
	if string(got) != want {
		if os.Getenv("UPDATE_TILE_VERSIONS") != "" {
			if err := os.WriteFile(base, []byte(want), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		if !t.Failed() {
			t.Errorf("hack/tile-versions.txt is stale (a tile's version or files changed) — regenerate: UPDATE_TILE_VERSIONS=1 go test ./internal/builtins -run TestTileVersions")
		}
	}
}

// inTile is the file's name within its tile dir ("tile.json" for the marker).
func inTile(p string) string {
	if i := strings.Index(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
