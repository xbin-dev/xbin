package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

// fieldKinds is 05-model §6's split of every Manifest field: tile-level
// fields come from the work tree, the inbound surface from the primary's
// code, deployment-level fields from the deployment's own code.
var fieldKinds = map[string]string{
	"Uses": "tile", "Interfaces": "tile", "Deps": "tile",
	"Template": "inbound", "Exposes": "inbound", "Expose": "inbound", "Provides": "inbound", "Chrome": "inbound",
	"Runtime": "code", "Entry": "code", "Setup": "code", "AlwaysOn": "code", "VM": "code", "Inject": "code", "Native": "code",
}

// pinnedHook answers the PinnedPrimary hook from a fixed map, as the
// deployments plane does from its index.
func pinnedHook(m map[string]*PinnedCode) func(string) (*PinnedCode, bool) {
	return func(rel string) (*PinnedCode, bool) {
		pc, ok := m[rel]
		return pc, ok
	}
}

// readCheckpoint materializes files as a checkpoint tree and reads it.
func readCheckpoint(t *testing.T, files map[string]string) (string, *PinnedCode) {
	t.Helper()
	root := t.TempDir()
	writeTree(t, root, files)
	pc, err := ReadCheckpoint(root)
	if err != nil {
		t.Fatalf("ReadCheckpoint: %v", err)
	}
	return root, pc
}

func exposeNames(m Manifest) []string {
	var out []string
	for k := range m.Exposes {
		out = append(out, k)
	}
	for k := range m.Provides {
		out = append(out, "provides:"+k)
	}
	if m.Expose != nil {
		for k := range m.Expose.Roles {
			out = append(out, "role:"+k)
		}
	}
	slices.Sort(out)
	return out
}

// covers D119e D127f D127n T14 T16 — the three field kinds of 05-model §6. Every
// Manifest field has exactly one kind. While a tile's primary is pinned, the
// registry's component takes the inbound surface (template, exposes,
// expose.roles, provides, chrome) and the deployment-level fields (runtime,
// entry, setup, alwaysOn, vm, inject, native, scope.json's resources and
// importMap) from the primary's checkpoint, for every pinned primary, and
// keeps the tile-level fields (uses, interfaces, deps) and the manifest
// error the work tree's: a work-tree runtime or exposes edit doesn't reach
// the primary, and a new uses entry is still what the broker files as a
// pending grant. A deployment view takes the deployment-level fields from
// its own code; a non-primary deployment's documents are never chrome.
func TestManifestFieldSplit(t *testing.T) {
	t.Run("every field has one kind", func(t *testing.T) {
		mt := reflect.TypeOf(Manifest{})
		for i := range mt.NumField() {
			f := mt.Field(i)
			kind, ok := fieldKinds[f.Name]
			if !ok {
				t.Errorf("Manifest.%s has no kind: classify it in composeManifest and fieldKinds (05-model §6)", f.Name)
				continue
			}
			for _, src := range []string{"tile", "inbound", "code"} {
				in := map[string]*Manifest{"tile": {}, "inbound": {}, "code": {}}
				reflect.ValueOf(in[src]).Elem().Field(i).Set(nonZero(f.Type))
				out := composeManifest(*in["tile"], *in["inbound"], *in["code"])
				if got := !reflect.ValueOf(out).Field(i).IsZero(); got != (src == kind) {
					t.Errorf("Manifest.%s set in the %s source: composed set = %v, want it only from %s", f.Name, src, got, kind)
				}
			}
		}
		if len(fieldKinds) != mt.NumField() {
			t.Errorf("fieldKinds lists %d fields, Manifest has %d", len(fieldKinds), mt.NumField())
		}
	})

	ws := t.TempDir()
	writeTree(t, ws, map[string]string{
		"apps/crm/xbin.json": `{"runtime":"static","entry":"wt","setup":"wt","chrome":true,"inject":true,
			"exposes":{"web":{"kind":"http"}},"expose":{"roles":{"writer":"wt"}},"provides":{"wtnet":{"kind":"net"}},
			"uses":[{"target":"apps/mail","role":"reader"},{"target":"apps/new","role":"reader"}],
			"interfaces":{"llm":{"kind":"http","service":"openai"}},"deps":["apps/mail"]}`,
		"apps/crm/index.html":      `<p>work tree</p>`,
		"apps/crm/native.js":       `export {}`,
		"apps/crm/scope.json":      `{"resources":{"wtonly":{"type":"kv"}},"importMap":{"lib":"/wt/lib.js"}}`,
		"apps/wiki/xbin.json":      `{"expose":{"roles":{"reader":"wt"}}}`,
		"apps/bad/xbin.json":       `{"runtime":"static","exposes":{"w":{"kind":"http","paths":["/*"]}}}`,
		"apps/plain/xbin.json":     `{"runtime":"go","chrome":true}`,
		"apps/suite/scope.json":    `{"resources":{"shared":{"type":"kv"}}}`,
		"apps/suite/app/xbin.json": `{"runtime":"static"}`,
		"apps/mail/xbin.json":      `{}`,
	})
	crmRoot, crmCP := readCheckpoint(t, map[string]string{
		"xbin.json": `{"runtime":"go","entry":"cmd","setup":"apt-get install -y jq","alwaysOn":true,"inject":false,"native":"./m/main.js",
			"exposes":{"api":{"kind":"http","paths":["/cp/*"]}},"expose":{"roles":{"reader":"cp"}},"provides":{"cpnet":{"kind":"net"}},
			"uses":[{"target":"apps/mail","role":"reader"},{"target":"apps/old","role":"writer"}],
			"interfaces":{"old":{"kind":"net"}},"deps":["apps/old"]}`,
		"m/main.js":  `export {}`,
		"scope.json": `{"resources":{"db":{"type":"sqlite"}},"importMap":{"lib":"/cp/lib.js"}}`,
	})
	_, wikiCP := readCheckpoint(t, map[string]string{
		"xbin.json":  `{"chrome":true,"template":{"title":"Wiki"},"exposes":{"pub":{"kind":"http","paths":["/p"]}},"expose":{"roles":{"admin":"cp"}}}`,
		"index.html": `<p>pinned</p>`,
	})
	_, badCP := readCheckpoint(t, map[string]string{"xbin.json": `{"runtime":"go","chrome":true,`, "index.html": `x`})
	if badCP.ManifestErr == "" || !reflect.DeepEqual(badCP.Manifest, Manifest{}) {
		t.Fatalf("a checkpoint xbin.json that doesn't parse: %+v, want the zero manifest and an error", badCP)
	}

	plainRef, err := Open(ws) // the same workspace with no hook: today's scan
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]*PinnedCode{"apps/crm": crmCP, "apps/wiki": wikiCP, "apps/bad": badCP}
	r := &Registry{Root: ws, PinnedPrimary: pinnedHook(pinned)}
	if err := r.Rescan(); err != nil {
		t.Fatal(err)
	}
	crm, _ := r.Component("apps/crm")

	t.Run("rescan composes the pinned primary", func(t *testing.T) {
		m := crm.Manifest
		if m.Runtime != "go" || m.Entry != "cmd" || m.Setup != "apt-get install -y jq" || !m.AlwaysOn ||
			m.Inject == nil || *m.Inject || m.Native == nil || m.Native.Entry != "./m/main.js" {
			t.Errorf("deployment-level fields %+v, want the checkpoint's", m)
		}
		if got, want := exposeNames(m), []string{"api", "provides:cpnet", "role:reader"}; !reflect.DeepEqual(got, want) {
			t.Errorf("inbound surface %q, want the checkpoint's %q", got, want)
		}
		if m.Chrome {
			t.Error("chrome follows the work tree; the pinned checkpoint doesn't ask for it")
		}
		if !reflect.DeepEqual(m.Uses, []Use{{"apps/mail", "reader"}, {"apps/new", "reader"}}) {
			t.Errorf("uses %v, want the work tree's (the new entry files a pending grant)", m.Uses)
		}
		if _, ok := m.Interfaces["llm"]; !ok || len(m.Interfaces) != 1 || !reflect.DeepEqual(m.Deps, []string{"apps/mail"}) {
			t.Errorf("interfaces %v deps %v, want the work tree's", m.Interfaces, m.Deps)
		}
		if crm.HasIndex || crm.Native != "m/main.js" || crm.NativeErr != "" {
			t.Errorf("HasIndex %v Native %q (%s), want the checkpoint's: no index, m/main.js", crm.HasIndex, crm.Native, crm.NativeErr)
		}
		wt := crm.WorkTree
		if wt == nil || wt.Manifest.Runtime != "static" || !wt.HasIndex || wt.Native != "native.js" || crm.Kept {
			t.Fatalf("WorkTree %+v Kept %v, want the work tree's scan, not kept", wt, crm.Kept)
		}
		if got := crm.WorkTreeManifest().Expose.Roles; !reflect.DeepEqual(got, map[string]string{"writer": "wt"}) {
			t.Errorf("WorkTreeManifest roles %v, want the work tree's", got)
		}
		if crm.ManifestErr == "" || crm.ManifestErr != plainErr(t, plainRef, "apps/crm") {
			t.Errorf("manifest error %q, want the work tree's %q", crm.ManifestErr, plainErr(t, plainRef, "apps/crm"))
		}
		if crm.Deployment != "" || crm.CodeRoot != "" {
			t.Errorf("the registry's component has Deployment %q CodeRoot %q, want both empty", crm.Deployment, crm.CodeRoot)
		}
	})

	t.Run("every pinned primary's inbound surface", func(t *testing.T) {
		for rel, pc := range pinned {
			c, ok := r.Component(rel)
			if !ok {
				t.Fatalf("%s isn't registered", rel)
			}
			m, cp := c.Manifest, pc.Manifest
			if !reflect.DeepEqual(m.Template, cp.Template) || !reflect.DeepEqual(m.Exposes, cp.Exposes) ||
				!reflect.DeepEqual(m.Expose, cp.Expose) || !reflect.DeepEqual(m.Provides, cp.Provides) || m.Chrome != cp.Chrome {
				t.Errorf("%s: inbound surface %+v, want its checkpoint's %+v", rel, m, cp)
			}
		}
		wiki, _ := r.Component("apps/wiki")
		if !wiki.Manifest.Chrome || !wiki.IsTemplate() || wiki.WorkTreeManifest().Chrome {
			t.Errorf("apps/wiki: chrome %v template %v, want the checkpoint's (both), the work tree asking for neither", wiki.Manifest.Chrome, wiki.IsTemplate())
		}
		bad, _ := r.Component("apps/bad")
		if bad.HasBackend() || len(bad.Manifest.Exposes) > 0 || bad.Manifest.Chrome || bad.ManifestErr != "" {
			t.Errorf("apps/bad: a checkpoint that doesn't parse composes as the zero manifest, with the work tree's (empty) error: %+v %q", bad.Manifest, bad.ManifestErr)
		}
	})

	t.Run("scopes", func(t *testing.T) {
		sc := r.Scopes()
		if got := sc["apps/crm"].Resources; !reflect.DeepEqual(got, map[string]Resource{"db": {Type: "sqlite"}}) {
			t.Errorf("apps/crm resources %v, want the checkpoint's", got)
		}
		if got := r.ImportMapFor(crm)["lib"]; got != "/cp/lib.js" {
			t.Errorf("apps/crm importMap lib = %q, want the checkpoint's", got)
		}
		if got := sc["apps/suite"].Resources; !reflect.DeepEqual(got, map[string]Resource{"shared": {Type: "kv"}}) {
			t.Errorf("a plain-directory scope %v, want its own scope.json", got)
		}
		if _, ok := sc["apps/wiki"]; ok {
			t.Error("apps/wiki roots a scope though its work tree has no scope.json")
		}
	})

	t.Run("tiles the hook doesn't answer for are today's", func(t *testing.T) {
		for _, rel := range []string{"apps/plain", "apps/suite/app", "apps/mail"} {
			got, _ := r.Component(rel)
			want, _ := plainRef.Component(rel)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %+v, want today's %+v", rel, got, want)
			}
		}
	})

	t.Run("views", func(t *testing.T) {
		wiki, _ := r.Component("apps/wiki")
		devRoot, _ := readCheckpoint(t, map[string]string{
			"xbin.json":  `{"runtime":"node","entry":"dev.js","chrome":true,"exposes":{"dev":{"kind":"http","paths":["/d"]}},"uses":[{"target":"apps/devonly","role":"reader"}]}`,
			"index.html": `<p>dev</p>`,
		})
		for _, c := range []*Component{crm, wiki} {
			v, err := r.View(c, ViewCode{Deployment: "dev", Tree: "t-dev", Root: devRoot})
			if err != nil {
				t.Fatal(err)
			}
			if v.Manifest.Runtime != "node" || v.Manifest.Entry != "dev.js" || !v.HasIndex {
				t.Errorf("%s: dev view %+v, want dev's own code", c.Path, v.Manifest)
			}
			if v.Manifest.Chrome {
				t.Errorf("%s: a non-primary deployment's view is chrome", c.Path)
			}
			if !reflect.DeepEqual(v.Manifest.Exposes, c.Manifest.Exposes) || !reflect.DeepEqual(v.Manifest.Template, c.Manifest.Template) {
				t.Errorf("%s: dev view's inbound surface %+v, want the tile's (the primary's)", c.Path, v.Manifest)
			}
			if !slices.Contains(v.Manifest.Uses, Use{"apps/devonly", "reader"}) || len(v.Manifest.Uses) != len(c.Manifest.Uses)+1 {
				t.Errorf("%s: dev view uses %v, want the work tree's plus its own", c.Path, v.Manifest.Uses)
			}
			if v.Deployment != "dev" || v.CodeRoot != devRoot || v.Path != c.Path || v.Dir != c.Dir || v.Scope != c.Scope {
				t.Errorf("%s: dev view Deployment %q CodeRoot %q Path %q", c.Path, v.Deployment, v.CodeRoot, v.Path)
			}
		}

		// The live reload target while main is pinned runs the work tree.
		v, err := r.View(crm, ViewCode{Deployment: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		if v.Manifest.Runtime != "static" || v.Manifest.Entry != "wt" || !v.HasIndex || v.Native != "native.js" || v.CodeRoot != "" {
			t.Errorf("dev on the work tree: %+v, want the work tree's deployment-level fields", v)
		}
		if v.Manifest.Chrome || !reflect.DeepEqual(v.Manifest.Exposes, crm.Manifest.Exposes) || !reflect.DeepEqual(v.Manifest.Uses, crm.Manifest.Uses) {
			t.Errorf("dev on the work tree: chrome %v exposes %v uses %v", v.Manifest.Chrome, v.Manifest.Exposes, v.Manifest.Uses)
		}

		// The pinned primary's own code: its env takes both uses lists.
		v, err = r.View(crm, ViewCode{Tree: "t-crm", Root: crmRoot})
		if err != nil {
			t.Fatal(err)
		}
		if v.Manifest.Runtime != "go" || !reflect.DeepEqual(v.Manifest.Exposes, crm.Manifest.Exposes) || v.Deployment != "" || v.CodeRoot != crmRoot {
			t.Errorf("the primary's view of its checkpoint: %+v", v)
		}
		want := []Use{{"apps/mail", "reader"}, {"apps/new", "reader"}, {"apps/old", "writer"}}
		if !reflect.DeepEqual(v.Manifest.Uses, want) {
			t.Errorf("pinned primary's uses for env %v, want %v", v.Manifest.Uses, want)
		}

		// The primary back on the work tree (resuming live reload): all of it.
		v, err = r.View(crm, ViewCode{})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v.Manifest, crm.WorkTree.Manifest) || !v.HasIndex || v.CodeRoot != "" {
			t.Errorf("the primary's view of the work tree: %+v, want the work tree's manifest", v.Manifest)
		}

		// The zero state is the registry's own pointer.
		plain, _ := r.Component("apps/plain")
		if v, _ := r.View(plain, ViewCode{}); v != plain {
			t.Error("the zero-state view isn't the registry's own pointer")
		}

		// A checkpoint whose xbin.json doesn't parse can't start.
		badRoot, _ := readCheckpoint(t, map[string]string{"xbin.json": `{"runtime":`})
		if _, err := r.View(crm, ViewCode{Deployment: "dev", Tree: "t-bad", Root: badRoot}); err == nil {
			t.Error("a view of a checkpoint that doesn't parse")
		}
	})

	t.Run("cached by tree and work-tree manifest", func(t *testing.T) {
		devRoot, _ := readCheckpoint(t, map[string]string{"xbin.json": `{"runtime":"node"}`})
		code := ViewCode{Deployment: "dev", Tree: "t-cache", Root: devRoot}
		v1, err := r.View(crm, code)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(devRoot, "xbin.json")); err != nil { // checkpoints are immutable: never read again
			t.Fatal(err)
		}
		if err := r.Rescan(); err != nil { // nothing a view reads changed
			t.Fatal(err)
		}
		crm2, _ := r.Component("apps/crm")
		if v2, _ := r.View(crm2, code); v2 != v1 {
			t.Error("an unchanged rescan made a new view")
		}
		writeTree(t, ws, map[string]string{"apps/crm/xbin.json": `{"runtime":"node","exposes":{"other":{"kind":"http","paths":["/o"]}},"uses":[{"target":"apps/x","role":"reader"}]}`})
		if err := r.Rescan(); err != nil {
			t.Fatal(err)
		}
		crm3, _ := r.Component("apps/crm")
		v3, err := r.View(crm3, code)
		if err != nil || v3 == v1 || v3.Manifest.Runtime != "node" || !slices.Contains(v3.Manifest.Uses, Use{"apps/x", "reader"}) {
			t.Errorf("after a work-tree manifest edit: view %+v, err %v; want a new view from the cached checkpoint", v3, err)
		}
		// ...and the edit didn't reach the pinned primary.
		if crm3.Manifest.Runtime != "go" || !reflect.DeepEqual(exposeNames(crm3.Manifest), exposeNames(crmCP.Manifest)) || crm3.WorkTree.Manifest.Runtime != "node" {
			t.Errorf("a work-tree runtime/exposes edit reached the pinned primary: %+v", crm3.Manifest)
		}
	})

	t.Run("unpinned, the tile is today's again", func(t *testing.T) {
		delete(pinned, "apps/crm")
		if err := r.Rescan(); err != nil {
			t.Fatal(err)
		}
		ref, err := Open(ws)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := r.Component("apps/crm")
		want, _ := ref.Component("apps/crm")
		if !reflect.DeepEqual(got, want) || got.WorkTree != nil {
			t.Errorf("apps/crm after unpinning: %+v, want today's %+v", got, want)
		}
		if res := r.Scopes()["apps/crm"].Resources; !reflect.DeepEqual(res, map[string]Resource{"wtonly": {Type: "kv"}}) {
			t.Errorf("apps/crm resources after unpinning %v, want the work tree's", res)
		}
	})
}

func plainErr(t *testing.T, r *Registry, rel string) string {
	t.Helper()
	c, ok := r.Component(rel)
	if !ok {
		t.Fatalf("%s isn't registered", rel)
	}
	return c.ManifestErr
}

// nonZero is a non-zero value of type t, for the field-kind table.
func nonZero(t reflect.Type) reflect.Value {
	switch t.Kind() {
	case reflect.Bool:
		return reflect.ValueOf(true).Convert(t)
	case reflect.String:
		return reflect.ValueOf("x").Convert(t)
	case reflect.Pointer:
		return reflect.New(t.Elem())
	case reflect.Slice:
		return reflect.MakeSlice(t, 1, 1)
	case reflect.Map:
		m := reflect.MakeMap(t)
		m.SetMapIndex(reflect.ValueOf("x").Convert(t.Key()), reflect.Zero(t.Elem()))
		return m
	}
	panic("nonZero: unhandled kind " + t.Kind().String())
}
