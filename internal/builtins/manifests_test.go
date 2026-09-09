package builtins

import (
	"encoding/json"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin"
	"github.com/xbin-dev/xbin/internal/jsonc"
)

// A shipped tile's manifest must parse with xbind's own JSONC reader (a
// manifest error is a tile that never runs), and every role its backend
// guards with sdk.Role / RoleFunc must be declared under expose.roles —
// a guard on an undeclared role is a 403 nobody can grant their way past.
// `admin` is exempt: it is the self/owner role, never granted by name.
func TestBuiltinManifestsAndRoleGuards(t *testing.T) {
	guard := regexp.MustCompile(`(?:RoleFunc|xbin\.Role)\("([^"]+)"`)
	check := func(fsys fs.FS, kind string) {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			raw, err := fs.ReadFile(fsys, path.Join(e.Name(), "xbin.json"))
			if err != nil {
				continue
			}
			var m struct {
				Expose *struct {
					Roles map[string]any `json:"roles"`
				} `json:"expose"`
			}
			if err := jsonc.Unmarshal(raw, &m); err != nil {
				t.Errorf("%s/%s/xbin.json does not parse: %v", kind, e.Name(), err)
				continue
			}
			if !json.Valid(jsonc.Strip(raw)) {
				t.Errorf("%s/%s/xbin.json: not valid JSON after comment stripping", kind, e.Name())
			}
			declared := map[string]bool{}
			if m.Expose != nil {
				for r := range m.Expose.Roles {
					declared[r] = true
				}
			}
			guarded := map[string]bool{}
			_ = fs.WalkDir(fsys, e.Name(), func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") {
					return err
				}
				b, err := fs.ReadFile(fsys, p)
				if err != nil {
					return err
				}
				for _, mm := range guard.FindAllStringSubmatch(string(b), -1) {
					guarded[mm[1]] = true
				}
				return nil
			})
			var missing []string
			for r := range guarded {
				if r != "admin" && !declared[r] {
					missing = append(missing, r)
				}
			}
			sort.Strings(missing)
			if len(missing) > 0 {
				t.Errorf("%s/%s: backend guards on role(s) %v that xbin.json does not declare under expose.roles — nobody can be granted them", kind, e.Name(), missing)
			}
		}
	}
	check(xbin.BuiltinTilesFS(), "builtin-tiles")
	check(xbin.BuiltinTemplatesFS(), "builtin-templates")
}
