package registry

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// covers D119e T16 — a tile whose primary is pinned stays registered while its
// directory exists, even when the work tree has neither xbin.json nor
// index.html, or an xbin.json that doesn't parse: it is Kept, its tile-level
// fields (uses, interfaces, deps, and whether it roots a scope) fall back to
// the primary's checkpoint, and the work tree's error is surfaced. A work
// tree with only index.html is valid and keeps its own tile-level fields.
// Only the directory disappearing takes it down. A tile in the zero state
// disappears as today, and so does a tile once nothing is pinned.
func TestPinnedPrimarySurvivesMissingManifest(t *testing.T) {
	ws := t.TempDir()
	writeTree(t, ws, map[string]string{
		"apps/crm/xbin.json":  `{"runtime":"go","uses":[{"target":"apps/new","role":"reader"}]}`,
		"apps/crm/scope.json": `{"resources":{"db":{"type":"sqlite"}}}`,
		"apps/crm/notes.txt":  `a file that makes no tile`,
		"apps/free/xbin.json": `{"runtime":"static"}`,
	})
	_, cp := readCheckpoint(t, map[string]string{
		"xbin.json": `{"runtime":"go","exposes":{"api":{"kind":"http","paths":["/*"]}},
			"uses":[{"target":"apps/mail","role":"reader"}],"interfaces":{"llm":{"kind":"http"}},"deps":["apps/mail"]}`,
		"index.html": `<p>pinned</p>`,
		"scope.json": `{"resources":{"db":{"type":"sqlite"},"jobs":{"type":"cron"}}}`,
	})
	pinned := map[string]*PinnedCode{"apps/crm": cp}
	r := &Registry{Root: ws, PinnedPrimary: pinnedHook(pinned)}
	rescan := func() {
		t.Helper()
		if err := r.Rescan(); err != nil {
			t.Fatal(err)
		}
	}
	rm := func(rel string) {
		t.Helper()
		if err := os.RemoveAll(filepath.Join(ws, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	checkKept := func(what, wantErr string) *Component {
		t.Helper()
		c, ok := r.Component("apps/crm")
		if !ok {
			t.Fatalf("%s: the pinned tile is gone", what)
		}
		if !c.Kept || c.WorkTree == nil || !reflect.DeepEqual(c.WorkTree.Manifest, Manifest{}) {
			t.Errorf("%s: Kept %v WorkTree %+v, want kept with no valid work-tree manifest", what, c.Kept, c.WorkTree)
		}
		m := cp.Manifest
		if !reflect.DeepEqual(c.Manifest.Uses, m.Uses) || !reflect.DeepEqual(c.Manifest.Interfaces, m.Interfaces) || !reflect.DeepEqual(c.Manifest.Deps, m.Deps) {
			t.Errorf("%s: tile-level fields %+v, want the checkpoint's", what, c.Manifest)
		}
		if !c.HasBackend() || !c.HasIndex || len(c.Manifest.Exposes) != 1 {
			t.Errorf("%s: the pinned primary no longer serves: %+v HasIndex %v", what, c.Manifest, c.HasIndex)
		}
		if !strings.Contains(c.ManifestErr, keptNotice) || !strings.Contains(c.ManifestErr, wantErr) {
			t.Errorf("%s: manifest error %q, want %q and the notice", what, c.ManifestErr, wantErr)
		}
		if res := r.Scopes()["apps/crm"].Resources; len(res) != 2 {
			t.Errorf("%s: scope resources %v, want the checkpoint's", what, res)
		}
		return c
	}

	rescan()
	if c, _ := r.Component("apps/crm"); c.Kept || !reflect.DeepEqual(c.Manifest.Uses, []Use{{"apps/new", "reader"}}) {
		t.Fatalf("a valid work tree: Kept %v uses %v, want the work tree's uses", c.Kept, c.Manifest.Uses)
	}

	writeTree(t, ws, map[string]string{"apps/crm/xbin.json": `{"runtime":`, "apps/free/xbin.json": `{"runtime":`})
	rescan()
	checkKept("a broken xbin.json", "jsonc:")
	if c, ok := r.Component("apps/free"); !ok || c.Kept || c.ManifestErr == "" || strings.Contains(c.ManifestErr, keptNotice) {
		t.Errorf("a zero-state tile with a broken xbin.json: %+v, want today's (registered, its parse error)", c)
	}

	rm("apps/crm/xbin.json")
	rm("apps/free/xbin.json")
	rescan()
	c := checkKept("neither xbin.json nor index.html", keptNotice)
	if c.ManifestErr != keptNotice {
		t.Errorf("manifest error %q, want just the notice", c.ManifestErr)
	}
	if _, ok := r.Component("apps/free"); ok {
		t.Error("a zero-state tile without xbin.json or index.html is still registered")
	}
	if _, err := r.View(c, ViewCode{Deployment: "dev"}); err == nil {
		t.Error("a Kept tile's broken work tree has a view")
	}

	rm("apps/crm/scope.json") // an old branch checked out, the whole tree gone
	rescan()
	checkKept("no scope.json either", keptNotice)

	writeTree(t, ws, map[string]string{"apps/crm/index.html": `<p>work tree</p>`})
	rescan()
	if c, _ := r.Component("apps/crm"); c.Kept || c.Manifest.Uses != nil || c.ManifestErr != "" {
		t.Errorf("a work tree with index.html only: Kept %v uses %v err %q, want valid, its own (no) uses", c.Kept, c.Manifest.Uses, c.ManifestErr)
	}
	if _, ok := r.Scopes()["apps/crm"]; ok {
		t.Error("a valid work tree without scope.json still roots a scope")
	}

	rm("apps/crm/index.html")
	delete(pinned, "apps/crm") // live reload resumed: nothing is kept
	rescan()
	if _, ok := r.Component("apps/crm"); ok {
		t.Error("with the primary following the work tree, a tile without xbin.json or index.html is still registered")
	}

	pinned["apps/crm"] = cp
	rm("apps/crm")
	rescan()
	if _, ok := r.Component("apps/crm"); ok {
		t.Error("a pinned tile whose directory is gone is still registered")
	}
}
