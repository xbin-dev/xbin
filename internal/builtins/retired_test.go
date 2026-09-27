package builtins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
)

// A retired name must not also be a shipped tile: the retired message would
// shadow it in the import endpoint.
func TestRetiredNotEmbedded(t *testing.T) {
	set, err := Load(os.DirFS(filepath.Join("..", "..", "builtin-tiles")))
	if err != nil {
		t.Fatal(err)
	}
	if len(set.List()) == 0 {
		t.Fatal("no builtin tiles loaded — wrong path?")
	}
	for name := range retiredTiles {
		if _, ok := set.Get(name); ok {
			t.Errorf("builtin-tiles/%s ships but %q is in retiredTiles — drop one of them", name, name)
		}
	}
}

// Importing a retired tile fails with what replaces it and writes nothing;
// an unknown name keeps its plain error.
func TestRetiredTileImport(t *testing.T) {
	set, err := Load(fstest.MapFS{
		"hello/tile.json": {Data: []byte(`{"name":"hello","version":1}`)},
		"hello/xbin.json": {Data: []byte(`{}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_, written, err := set.Import(root, "devbox", "")
	if err == nil {
		t.Fatal("importing devbox must fail")
	}
	for _, want := range []string{"retired", "coding-sandbox", "sandbox-terminal", "/docs/sandbox-manager.md", "keeps its copy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if len(written) != 0 {
		t.Errorf("wrote %v", written)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "devbox")); !os.IsNotExist(err) {
		t.Errorf("apps/devbox must not exist: %v", err)
	}
	if _, _, err := set.Import(root, "nope", ""); err == nil || err.Error() != `no builtin tile "nope"` {
		t.Errorf("unknown tile: %v", err)
	}
}

// A workspace that imported devbox before it was retired keeps its copy:
// after the upgrade no update is offered, the check does not fail, and the
// update actions refuse with the retirement instead of touching the copy.
func TestRetiredTileImportedCopy(t *testing.T) {
	root := t.TempDir()
	hello := fstest.MapFS{
		"hello/tile.json":  {Data: []byte(`{"name":"hello","version":1}`)},
		"hello/index.html": {Data: []byte(`<html>hello</html>`)},
	}
	old := fstest.MapFS{
		"devbox/tile.json":  {Data: []byte(`{"name":"devbox","version":4}`)},
		"devbox/xbin.json":  {Data: []byte(`{"backend":{"runtime":"go"}}`)},
		"devbox/index.html": {Data: []byte(`<html>devbox</html>`)},
	}
	for k, v := range hello {
		old[k] = v
	}
	// Before: the older xbind imports devbox and records its provenance.
	oldSet, err := Load(old)
	if err != nil {
		t.Fatal(err)
	}
	path, _, err := oldSet.Import(root, "devbox", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewUpdater(root, oldSet, nil).RecordTile("devbox", path); err != nil {
		t.Fatal(err)
	}
	// The workspace edits its copy, as workspaces do.
	page := filepath.Join(root, "apps", "devbox", "index.html")
	if err := os.WriteFile(page, []byte("<html>mine</html>"), 0o644); err != nil {
		t.Fatal(err)
	}

	// After: the embed no longer has devbox.
	newSet, err := Load(hello)
	if err != nil {
		t.Fatal(err)
	}
	u := NewUpdater(root, newSet, nil)
	ups, err := u.Updates()
	if err != nil {
		t.Fatalf("update check must not fail on a retired tile's copy: %v", err)
	}
	for _, uu := range ups {
		if strings.Contains(uu.ID, "devbox") {
			t.Errorf("a retired tile's copy must not be offered an update: %+v", uu)
		}
	}
	for _, id := range []string{"tile:devbox", "devbox"} {
		id = u.ResolveID(id)
		if _, err := u.ApplyReplace(id); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Errorf("replace %s: %v", id, err)
		}
		if _, err := u.ApplyMerge(id); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Errorf("merge %s: %v", id, err)
		}
		if _, err := u.Propose(id); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Errorf("propose %s: %v", id, err)
		}
	}
	if b, err := os.ReadFile(page); err != nil || string(b) != "<html>mine</html>" {
		t.Errorf("the copy must be untouched: %q %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(root, "apps", "devbox", "xbin.json")); err != nil {
		t.Errorf("the copy must be untouched: %v", err)
	}
}
