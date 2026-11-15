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

// What a person's workspace agent may do through a shipped tile is a
// decision someone made ("agent": off | read | full; docs/auth.md
// §Delegation), in one of two ways:
//
//   - infrastructure — a tile whose API is a control surface or hands out
//     secrets (the gateways, sandboxes, ingress, the consoles) — and the
//     agent template say a value, with the reason beside it;
//   - what people build apps from — the chat tile, the starter template —
//     says NOTHING: an app is what a workspace agent is for, and an
//     unspecified tile is fully usable under its person's tickets. The
//     starter's AGENTS.md tells its builder when to narrow that.
//
// A new builtin fails here until it is put in one of the two.
func TestBuiltinManifestsSayAgent(t *testing.T) {
	apps := map[string]bool{"builtin-tiles/chat": true, "builtin-templates/starter": true}
	agentOf := func(fsys fs.FS, file string) (string, bool) {
		raw, err := fs.ReadFile(fsys, file)
		if err != nil {
			return "", false
		}
		var m struct {
			Agent string `json:"agent"`
		}
		if err := jsonc.Unmarshal(raw, &m); err != nil {
			t.Errorf("%s does not parse: %v", file, err)
			return "", false
		}
		return m.Agent, true
	}
	check := func(fsys fs.FS, kind string) {
		entries, err := fs.ReadDir(fsys, ".")
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			got, ok := agentOf(fsys, path.Join(e.Name(), "xbin.json"))
			if !e.IsDir() || !ok {
				continue
			}
			name := kind + "/" + e.Name()
			switch {
			case apps[name] && got != "":
				t.Errorf("%s/xbin.json says \"agent\": %q — an app people build on stays unspecified (its AGENTS.md says when a builder narrows it)", name, got)
			case !apps[name] && got != "off" && got != "read" && got != "full":
				t.Errorf("%s/xbin.json: \"agent\" is %q — say off, read or full, with the reason in a comment (docs/elements.md \"A person's agent\"), or list it as an app in this test", name, got)
			}
		}
	}
	check(xbin.BuiltinTilesFS(), "builtin-tiles")
	check(xbin.BuiltinTemplatesFS(), "builtin-templates")
	// the consoles of a new workspace
	for _, tile := range []string{"admin", "organisations", "manager"} {
		if got, _ := agentOf(xbin.TemplateFS(), path.Join("tiles", tile, "xbin.json")); got != "off" {
			t.Errorf("workspace-template/tiles/%s/xbin.json: \"agent\" is %q, want off", tile, got)
		}
	}
	// and the starter says how to narrow it, where a tile's coding agent reads
	if md, err := fs.ReadFile(xbin.BuiltinTemplatesFS(), "starter/AGENTS.md"); err != nil || !strings.Contains(string(md), `"agent": "read"`) || !strings.Contains(string(md), "ByAgent()") {
		t.Errorf("builtin-templates/starter/AGENTS.md doesn't say when to declare \"agent\" and how to check the caller (%v)", err)
	}
}
