package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidResourceName(t *testing.T) {
	for name, want := range map[string]bool{
		"db": true, "My.DB_1-2": true, "0": true, strings.Repeat("a", 64): true,
		"": false, ".": false, "..": false, ".hidden": false, "-x": false,
		"a/b": false, "../x": false, `a\b`: false, "a b": false, "a\x00b": false,
		"ü": false, "a:b": false, strings.Repeat("a", 65): false,
	} {
		if got := ValidResourceName(name); got != want {
			t.Errorf("ValidResourceName(%q) = %v, want %v", name, got, want)
		}
	}
}

// D118: a scope.json resource name that isn't a plain segment from the
// conservative set is a manifest error and never reaches provisioning; a
// workspace-level one is left out of Workspace() but kept in the file.
func TestInvalidResourceNames(t *testing.T) {
	root := t.TempDir()
	writeWS(t, root, map[string]string{
		"xbin.json": `{"schema":1,"resources":{"shared":{"type":"kv"},"../../ws-escape":{"type":"filesystem"}}}`,
		"apps/x/scope.json": `{"resources":{
			"db":{"type":"filesystem"},
			"../../../../../tmp/escape":{"type":"filesystem"},
			"x/../..":{"type":"sqlite"},
			"..":{"type":"blob"},
			"q/db":{"type":"kv"}}}`,
		"apps/x/xbin.json":     `{}`,
		"apps/x/sub/xbin.json": `{}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	sm := r.Scopes()["apps/x"]
	if len(sm.Resources) != 1 || sm.Resources["db"].Type != "filesystem" {
		t.Fatalf("resources after validation: %v", sm.Resources)
	}
	for _, p := range []string{"apps/x", "apps/x/sub"} { // every tile of the scope says why
		c, _ := r.Component(p)
		for _, bad := range []string{`"../../../../../tmp/escape"`, `"x/../.."`, `".."`, `"q/db"`} {
			if !strings.Contains(c.ManifestErr, bad) {
				t.Fatalf("%s: manifest error lacks %s: %q", p, bad, c.ManifestErr)
			}
		}
	}
	if !r.HoldsScopeKey("apps/x") {
		t.Fatal("dropping bad names doesn't touch the scope's key")
	}
	ws := r.Workspace()
	if len(ws.Resources) != 1 || ws.Resources["shared"].Type != "kv" {
		t.Fatalf("workspace resources: %v", ws.Resources)
	}
	// A grants write re-serializes the manifest: the admin's declaration stays.
	if err := r.MutateWorkspace(func(w *WorkspaceManifest) {
		w.Grants = append(w.Grants, Grant{From: "apps/x", Target: "res:workspace/shared", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "xbin.json"))
	if err != nil || !strings.Contains(string(b), "ws-escape") {
		t.Fatalf("the workspace manifest lost a declaration: %s %v", b, err)
	}
}
