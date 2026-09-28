package registry

import (
	"reflect"
	"testing"
)

// covers P5 PO-12 SC-ZERO — a scan with no deployment hook installed leaves
// every component's deployment fields zero: it describes the primary (no
// Deployment), its code is its directory (no CodeRoot), nothing is kept for a
// pinned primary, and WorkTreeManifest is its own manifest. A broken or
// missing manifest keeps today's outcome.
func TestDeploymentFieldsZeroState(t *testing.T) {
	root := t.TempDir()
	writeTree(t, root, map[string]string{
		"apps/crm/xbin.json":     `{"runtime":"go","expose":{"roles":{"reader":"r"}}}`,
		"apps/crm/scope.json":    `{"resources":{"db":{"type":"sqlite"}}}`,
		"apps/page/index.html":   `<p>hi</p>`,
		"apps/broken/xbin.json":  `{"runtime":`,
		"apps/plain/notes.txt":   `not a tile`,
		"apps/crm+dev/xbin.json": `{}`,
	})
	r, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if r.PinnedPrimary != nil {
		t.Fatal("a fresh registry has deployment hooks installed")
	}
	want := []string{"apps/broken", "apps/crm", "apps/crm+dev", "apps/page"}
	var got []string
	for _, c := range r.Components() {
		got = append(got, c.Path)
		if c.Deployment != "" || c.CodeRoot != "" || c.WorkTree != nil || c.Kept {
			t.Errorf("%s: deployment fields set: Deployment %q CodeRoot %q WorkTree %v Kept %v", c.Path, c.Deployment, c.CodeRoot, c.WorkTree, c.Kept)
		}
		if !reflect.DeepEqual(c.WorkTreeManifest(), c.Manifest) {
			t.Errorf("%s: WorkTreeManifest differs from Manifest", c.Path)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("components %q, want %q", got, want)
	}
	if c, _ := r.Component("apps/broken"); c.ManifestErr == "" {
		t.Error("apps/broken: the parse error isn't surfaced")
	}
	if res := r.Scopes()["apps/crm"].Resources; !reflect.DeepEqual(res, map[string]Resource{"db": {Type: "sqlite"}}) {
		t.Errorf("apps/crm's scope resources %v, want the work tree's", res)
	}
}

// covers P9 — WorkTreeManifest reads the work tree's scan once the primary
// is pinned: the authors' roles, not the pinned code's.
func TestWorkTreeManifest(t *testing.T) {
	c := &Component{
		Manifest: Manifest{Runtime: "go", Expose: &Expose{Roles: map[string]string{"reader": "pinned"}}},
		WorkTree: &WorkTreeScan{Manifest: Manifest{Runtime: "static", Expose: &Expose{Roles: map[string]string{"writer": "new"}}}},
	}
	if got := c.WorkTreeManifest(); got.Runtime != "static" || got.Expose.Roles["writer"] != "new" {
		t.Errorf("WorkTreeManifest = %+v, want the work tree's", got)
	}
	if c.Manifest.Runtime != "go" {
		t.Error("WorkTreeManifest changed the component's own manifest")
	}
}
