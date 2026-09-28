package tilesbx

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An unreadable definitions file (or one from a newer xbind) is never
// written over: reads see nothing, writes are unavailable.
func TestUnreadableDefs(t *testing.T) {
	for _, content := range []string{"{not json", `{"version": 2, "tiles": {}}`} {
		root := t.TempDir()
		path := filepath.Join(root, "data", "sandboxes.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		e := newEnv(t, func(o *Options) { o.Root = root })
		if e.m.Health() == nil {
			t.Fatalf("%q: no health error", content)
		}
		e.want(e.do(mgr, "POST", "/sandboxes", ns("sb-1")), http.StatusServiceUnavailable, RefUnavailable)
		if b, _ := os.ReadFile(path); string(b) != content {
			t.Fatalf("%q was rewritten: %s", content, b)
		}
	}
}

// Entries no create could have made are dropped on load.
func TestDefsLoadDropsForgeries(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data", "sandboxes.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, []byte(`{"version":1,"tiles":{"apps/mgr":{"sandboxes":{
		"ok":{"name":"ok","mode":"namespace","net":{"egress":"none"}},
		"../x":{"name":"../x","mode":"namespace"},
		"runtime":{"name":"runtime","mode":"namespace"},
		"mismatch":{"name":"other","mode":"namespace"}}},
		"apps/empty":{"sandboxes":{}}}}`), 0o600)
	e := newEnv(t, func(o *Options) { o.Root = root })
	if e.m.Health() != nil {
		t.Fatal(e.m.Health())
	}
	rows := e.m.AdminList("")
	if len(rows) != 1 || rows[0].Name != "ok" {
		t.Fatalf("loaded %+v", rows)
	}
}

// Deleting a sandbox puts its state aside — a rename into its key's
// .trash — and answers at once; the confined remover empties it later, one
// entry at a time. The same name created again gets another uid and dir.
func TestDeleteTrashesState(t *testing.T) {
	e := newEnv(t)
	gate, removed := make(chan struct{}), make(chan string, 4)
	e.m.trash.remove = func(_ context.Context, dir string) error {
		<-gate
		removed <- dir
		return os.RemoveAll(dir)
	}
	k := Key{Tile: "apps/mgr"}
	e.create(ns("sb-1"))
	d, _ := e.m.defs.get(k, "sb-1")
	dir, _ := e.m.StateDir(k, d)
	if err := os.MkdirAll(filepath.Join(dir, "cur", "upper"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.want(e.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "") // the remover is blocked
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1", nil), http.StatusNotFound, RefNotFound)
	trash, _ := e.m.TrashDir(k)
	if _, err := os.Stat(filepath.Join(trash, d.UID, "cur", "upper")); err != nil {
		t.Fatalf("the state isn't in .trash/<uid>: %v", err)
	}
	if _, err := os.Lstat(dir); !os.IsNotExist(err) {
		t.Fatalf("the state dir is still there: %v", err)
	}
	in := e.create(ns("sb-1"))
	nd, _ := e.m.defs.get(k, "sb-1")
	ndir, _ := e.m.StateDir(k, nd)
	if in.UID == d.UID || ndir == dir {
		t.Fatalf("the re-created sandbox has the old identity: %s %s", in.UID, ndir)
	}
	close(gate)
	if got := <-removed; got != filepath.Join(trash, d.UID) {
		t.Fatalf("removed %s", got)
	}
	e.m.trash.wait()
	if _, err := os.Lstat(filepath.Join(trash, d.UID)); !os.IsNotExist(err) {
		t.Fatalf("the trash entry is still there: %v", err)
	}
	// A sandbox with no state on disk deletes the same way.
	e.want(e.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "")
}

// The boot queues what .trash holds again, and every staging dir (tmp/)
// goes there first.
func TestBootRequeuesTrash(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, ".xbin", "sbx", "apps~mgr-1")
	for _, d := range []string{filepath.Join(base, ".trash", "0123456789ab", "cur"),
		filepath.Join(base, "sb-1.aaaaaaaaaaaa", "tmp", "x1", "upper")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	e := newEnv(t, func(o *Options) { o.Root = root })
	e.m.trash.wait()
	ents, _ := os.ReadDir(filepath.Join(base, ".trash"))
	if len(ents) != 0 {
		t.Fatalf(".trash after the boot: %v", ents)
	}
	if ents, _ := os.ReadDir(filepath.Join(base, "sb-1.aaaaaaaaaaaa", "tmp")); len(ents) != 0 {
		t.Fatalf("tmp/ after the boot: %v", ents)
	}
}

// DefBases is every definition's base, for the boot's GC — and an error
// for a file it can't read, so the GC releases nothing.
func TestDefBases(t *testing.T) {
	ws := t.TempDir()
	if b, err := DefBases(ws); err != nil || b != nil {
		t.Fatalf("no file: %v %v", b, err)
	}
	os.MkdirAll(filepath.Join(ws, "data"), 0o700)
	os.WriteFile(filepath.Join(ws, "data", "sandboxes.json"), []byte(`{"version":1,"tiles":{
		"apps/a":{"sandboxes":{"x":{"name":"x","base":"b-old"},"y":{"name":"y"}}},
		"apps/b":{"sandboxes":{"z":{"name":"z","base":"b-new"},"w":{"name":"w","base":"b-old"}}}}}`), 0o600)
	if b, err := DefBases(ws); err != nil || strings.Join(b, ",") != "b-new,b-old" {
		t.Fatalf("bases %v %v", b, err)
	}
	os.WriteFile(filepath.Join(ws, "data", "sandboxes.json"), []byte(`{"version":1,"til`), 0o600)
	if _, err := DefBases(ws); err == nil {
		t.Fatal("an unreadable file must be an error")
	}
}

// A sandbox's uid is its identity: set at create, kept by every change and
// restart, and new when the name is deleted and created again.
func TestUIDs(t *testing.T) {
	e := newEnv(t)
	in := e.create(map[string]any{"name": "sb-1", "mode": "namespace", "clientId": "c-1"})
	if !validUID(in.UID) {
		t.Fatalf("created uid %q", in.UID)
	}
	// A clientId repeat answers the same sandbox, the same uid.
	w := e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-1", "mode": "namespace", "clientId": "c-1"})
	e.want(w, http.StatusOK, "")
	if got := e.info(w).UID; got != in.UID {
		t.Fatalf("clientId repeat uid %q, want %q", got, in.UID)
	}
	w = e.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"idleStopMin": 60})
	e.want(w, http.StatusOK, "")
	if got := e.info(w).UID; got != in.UID {
		t.Fatalf("patched uid %q, want %q", got, in.UID)
	}
	b, _ := os.ReadFile(e.m.defsPath())
	if !strings.Contains(string(b), `"uid": "`+in.UID+`"`) {
		t.Fatalf("the file lacks the uid: %s", b)
	}
	e2 := &testEnv{t: t, m: New(Options{Root: e.m.root, Isolated: true, UIDRange: true, Deps: testDeps()})}
	e2.mux = routes(e2.m)
	if got := e2.info(e2.do(mgr, "GET", "/sandboxes/sb-1", nil)).UID; got != in.UID {
		t.Fatalf("uid after a restart %q, want %q", got, in.UID)
	}
	// Deleted and created again: another sandbox, another uid (so another
	// state dir, trash entry and archive key).
	e2.want(e2.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "")
	again := e2.create(ns("sb-1"))
	if !validUID(again.UID) || again.UID == in.UID {
		t.Fatalf("re-created uid %q, the old one %q", again.UID, in.UID)
	}
	k := Key{Tile: "apps/mgr"}
	oldDir, _ := e2.m.StateDir(k, &Def{Name: "sb-1", UID: in.UID})
	d, _ := e2.m.defs.get(k, "sb-1")
	if newDir, err := e2.m.StateDir(k, d); err != nil || newDir == oldDir {
		t.Fatalf("state dir %q (%v), the old one %q", newDir, err, oldDir)
	}
}

// Definitions written before uids existed load and gain one each, written
// back at once so they survive the next restart; a malformed uid is a
// forgery (dropped), a duplicated one is replaced.
func TestOldShapeGainsUIDs(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "data", "sandboxes.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	if err := os.WriteFile(path, []byte(`{"version":1,"tiles":{"apps/mgr":{"sandboxes":{
		"a":{"name":"a","mode":"namespace","net":{"egress":"none"},"version":3},
		"b":{"name":"b","mode":"namespace","net":{"egress":"none"}},
		"c":{"name":"c","mode":"namespace","uid":"0123456789ab"},
		"d":{"name":"d","mode":"namespace","uid":"0123456789ab"},
		"e":{"name":"e","mode":"namespace","uid":"../../../x"}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(Options{Root: root, Isolated: true, Deps: testDeps()})
	if m.Health() != nil {
		t.Fatal(m.Health())
	}
	k := Key{Tile: "apps/mgr"}
	uids := map[string]string{}
	for _, d := range m.defs.list(k) {
		if !validUID(d.UID) {
			t.Fatalf("%s: uid %q", d.Name, d.UID)
		}
		uids[d.Name] = d.UID
	}
	if len(uids) != 4 || uids["c"] != "0123456789ab" || uids["d"] == uids["c"] || uids["a"] == uids["b"] {
		t.Fatalf("uids %v", uids)
	}
	if a, _ := m.defs.get(k, "a"); a.Version != 3 {
		t.Fatalf("a changed: %+v", a)
	}
	// Written back: a restart finds the same uids.
	m2 := New(Options{Root: root, Isolated: true, Deps: testDeps()})
	for _, d := range m2.defs.list(k) {
		if d.UID != uids[d.Name] {
			t.Fatalf("%s: uid %q after a restart, want %q", d.Name, d.UID, uids[d.Name])
		}
	}
	// A file this xbind may not write is left alone, the uids given in memory.
	newer := filepath.Join(t.TempDir(), "data", "sandboxes.json")
	_ = os.MkdirAll(filepath.Dir(newer), 0o700)
	content := `{"version":2,"tiles":{"apps/mgr":{"sandboxes":{"a":{"name":"a","mode":"namespace"}}}}}`
	_ = os.WriteFile(newer, []byte(content), 0o600)
	m3 := New(Options{Root: filepath.Dir(filepath.Dir(newer)), Isolated: true, Deps: testDeps()})
	if b, _ := os.ReadFile(newer); string(b) != content {
		t.Fatalf("a newer file was rewritten: %s", b)
	}
	if a, ok := m3.defs.get(k, "a"); !ok || !validUID(a.UID) {
		t.Fatalf("a newer file's definition: %+v", a)
	}
}
