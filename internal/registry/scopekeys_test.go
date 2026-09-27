package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/util"
)

func writeWS(t *testing.T, root string, files map[string]string) {
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

const scopeDB = `{"resources":{"db":{"type":"filesystem"},"q":{"type":"kv"}}}`

// holds: the scope keeps its resources and its tiles carry no error.
func holds(t *testing.T, r *Registry, scope string) {
	t.Helper()
	sm := r.Scopes()[scope]
	if sm == nil || len(sm.Resources) == 0 || sm.Err != "" {
		t.Fatalf("%s should hold its key: %+v", scope, sm)
	}
	if !r.HoldsScopeKey(scope) {
		t.Fatalf("HoldsScopeKey(%s) = false", scope)
	}
	if c, ok := r.Component(scope); ok && c.ManifestErr != "" {
		t.Fatalf("%s: unexpected manifest error %q", scope, c.ManifestErr)
	}
}

// refused: still a scope (its tiles keep their scope), no resources, the
// reason on its tiles.
func refused(t *testing.T, r *Registry, scope, why string) {
	t.Helper()
	sm := r.Scopes()[scope]
	if sm == nil || len(sm.Resources) != 0 || !strings.Contains(sm.Err, why) {
		t.Fatalf("%s should be refused (%q): %+v", scope, why, sm)
	}
	if r.HoldsScopeKey(scope) {
		t.Fatalf("HoldsScopeKey(%s) = true", scope)
	}
	c, ok := r.Component(scope)
	if !ok || c.Scope != scope || !strings.Contains(c.ManifestErr, "scope.json ("+scope+")") {
		t.Fatalf("%s: tile scope %q, manifest error %q", scope, c.Scope, c.ManifestErr)
	}
}

// D118: util.ScopeKey maps apps~x and apps/x to one data key. The scope
// that held the key keeps it — across rescans and restarts — and the
// newcomer is refused; with no history both are refused; nothing is rekeyed.
func TestScopeKeyCollisions(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, map[string]string{
		"apps/x/scope.json": scopeDB,
		"apps/x/xbin.json":  `{}`,
		"apps/y/scope.json": scopeDB,
		"apps/y/xbin.json":  `{}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	holds(t, r, "apps/x")
	if util.ScopeKey("apps/x") != "apps~x" {
		t.Fatal("the on-disk key changed")
	}
	if _, err := os.Stat(filepath.Join(root, "data", scopeKeysFile)); !os.IsNotExist(err) {
		t.Fatal("an uncontested workspace grew a claims file")
	}

	// A newcomer whose key collides is refused; the holder is recorded.
	writeWS(t, root, map[string]string{"apps~x/scope.json": scopeDB, "apps~x/xbin.json": `{}`})
	if err := r.Rescan(); err != nil {
		t.Fatal(err)
	}
	holds(t, r, "apps/x")
	refused(t, r, "apps~x", `held by scope apps/x`)
	if r.ScopeKeyClash("apps~x") != "scope apps/x" || r.ScopeKeyClash("apps/x") != "scope apps~x" || r.ScopeKeyClash("apps/z") != "" {
		t.Fatal("ScopeKeyClash")
	}

	// A restart remembers who held the key.
	r2, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	holds(t, r2, "apps/x")
	refused(t, r2, "apps~x", `held by scope apps/x`)
	// ...even while the holder's scope.json is briefly gone (its directory stays).
	if err := os.Remove(filepath.Join(root, "apps/x/scope.json")); err != nil {
		t.Fatal(err)
	}
	if err := r2.Rescan(); err != nil {
		t.Fatal(err)
	}
	refused(t, r2, "apps~x", `held by scope apps/x`)

	// The holder removed: the key passes to the remaining scope.
	if err := os.RemoveAll(filepath.Join(root, "apps/x")); err != nil {
		t.Fatal(err)
	}
	if err := r2.Rescan(); err != nil {
		t.Fatal(err)
	}
	holds(t, r2, "apps~x")

	// Two claimants with no history (they predate the check): both refused.
	root2 := t.TempDir()
	writeWS(t, root2, map[string]string{
		"a/b/scope.json": scopeDB, "a/b/xbin.json": `{}`,
		"a~b/scope.json": scopeDB, "a~b/xbin.json": `{}`,
	})
	r3, err := Open(root2)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, r3, "a/b", "also claimed by scope a~b")
	refused(t, r3, "a~b", "also claimed by scope a/b")
}

// A scope at "workspace" would share the workspace-level resources' key
// (util.ScopeKey("") == "workspace"): always refused, and not creatable.
func TestScopeKeyWorkspace(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, map[string]string{
		"xbin.json":            `{"schema":1,"resources":{"db":{"type":"filesystem"}}}`,
		"workspace/scope.json": scopeDB,
		"workspace/xbin.json":  `{}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	refused(t, r, "workspace", "workspace-level resources' key")
	if !r.HoldsScopeKey("") || len(r.Workspace().Resources) != 1 {
		t.Fatal("the workspace scope keeps its resources")
	}
	if r.ScopeKeyClash("workspace") == "" {
		t.Fatal("a tile at workspace/ must be refused at creation")
	}
}
