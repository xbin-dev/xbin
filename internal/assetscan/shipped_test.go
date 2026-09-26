package assetscan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/jsonc"
)

// Every tile xbin ships — the workspace scaffold, builtin tiles and
// templates, the examples — loads under strict tile asset gating: no
// absolute /c/ reference a strict mode refuses, no inject:false, no escaping
// symlink. (Chrome — root, shell, chrome:true — runs on the workspace origin
// with the session cookie and is not gated.)
func TestShippedTilesPassStrictGating(t *testing.T) {
	repo := filepath.Join("..", "..")
	var dirs []string
	for _, tree := range []string{"workspace-template", "builtin-tiles", "builtin-templates", "examples"} {
		_ = filepath.WalkDir(filepath.Join(repo, tree), func(p string, d os.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return nil
			}
			if n := d.Name(); n == "node_modules" || n == "deps" || strings.HasPrefix(n, ".") {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(p, "xbin.json")); err == nil && p != filepath.Join(repo, "workspace-template") {
				dirs = append(dirs, p)
			}
			return nil
		})
	}
	if len(dirs) < 10 {
		t.Fatalf("found only %d shipped tiles", len(dirs))
	}
	for _, d := range dirs {
		rel, _ := filepath.Rel(repo, d)
		rel = filepath.ToSlash(rel)
		tile := rel[strings.IndexByte(rel, '/')+1:] // workspace-template/tiles/admin → tiles/admin
		if tile == "root" || tile == "shell" {
			continue
		}
		b, _ := os.ReadFile(filepath.Join(d, "xbin.json"))
		var m struct {
			Chrome bool `json:"chrome"`
		}
		_ = jsonc.Unmarshal(b, &m)
		if m.Chrome {
			continue
		}
		rep, err := Scan(d, tile, Options{Owner: func(p string) string {
			if p == tile || strings.HasPrefix(p, tile+"/") {
				return tile
			}
			return strings.SplitN(p, "/", 2)[0]
		}})
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range rep.Findings {
			if f.Breaks != "" {
				t.Errorf("%s: %s:%d %s %s — %s", rel, f.File, f.Line, f.Kind, f.Ref, f.Note)
			}
		}
	}
}
