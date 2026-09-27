package registry

import (
	"strings"
	"testing"
)

// A workspace may still hold a tile from before D117 declaring runtime
// "cgi". It stays a component that serves its files, but it has no backend
// and says why in its manifest error — the path bx ls, bx doctor and
// /components already surface.
func TestRemovedCGIRuntime(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/old/xbin.json":       `{"runtime": "cgi", "expose": {"roles": {"reader": "Read"}}}`,
		"apps/old/index.html":      `<p>still here</p>`,
		"apps/old/backend/handler": "#!/bin/sh\necho hi\n",
		"apps/py/xbin.json":        `{"runtime": "python"}`,
		"apps/odd/xbin.json":       `{"runtime": "ruby"}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	old, ok := r.Component("apps/old")
	if !ok {
		t.Fatal("a cgi tile must stay a component (its files keep serving)")
	}
	if !old.HasIndex {
		t.Error("the cgi tile's index.html must still be found")
	}
	if old.HasBackend() {
		t.Error("a cgi tile must not count as a backend — nothing may run it")
	}
	for _, want := range []string{`runtime "cgi" was removed`, "outside the sandbox", "go/node/python", "/docs/changes/2026-09-27-cgi-removed.md"} {
		if !strings.Contains(old.ManifestErr, want) {
			t.Errorf("manifest error %q lacks %q", old.ManifestErr, want)
		}
	}
	if err := ValidateRuntime(old.Manifest); err == nil || err.Error() != old.ManifestErr {
		t.Errorf("ValidateRuntime = %v, want the manifest error", err)
	}

	if py, _ := r.Component("apps/py"); py.ManifestErr != "" || !py.HasBackend() {
		t.Errorf("python tile: err %q, backend %v", py.ManifestErr, py.HasBackend())
	}
	// Only the removed runtime is refused; other unknown values stay a
	// backend-less tile, as they always were (compat: unknown tolerated).
	if odd, _ := r.Component("apps/odd"); odd.ManifestErr != "" || odd.HasBackend() {
		t.Errorf("unknown runtime: err %q, backend %v", odd.ManifestErr, odd.HasBackend())
	}
}
