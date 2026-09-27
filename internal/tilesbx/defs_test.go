package tilesbx

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeys(t *testing.T) {
	m := New(Options{Root: "/ws"})
	k := Key{Tile: "apps/coding-sandbox"}
	ck := k.CK()
	if !strings.HasPrefix(ck, "apps~coding-sandbox-") {
		t.Fatalf("CK %q", ck)
	}
	if dir, err := m.StateDir(k, "sb-1"); err != nil || dir != filepath.Join("/ws/.xbin/sbx", ck, "sb-1") {
		t.Fatalf("StateDir %q %v", dir, err)
	}
	if got := RegistryID(k, "sb-1"); got != "tile:"+ck+":sb-1" {
		t.Fatalf("RegistryID %q", got)
	}
	if got := Leaf(k, "sb-1"); got != "sbx-"+ck+"-sb-1" {
		t.Fatalf("Leaf %q", got)
	}
	if got := ArchiveKey(k, "sb-1"); got != ck+".sbx.sb-1" {
		t.Fatalf("ArchiveKey %q", got)
	}
	if root, err := m.CodeRoot(k); err != nil || root != "/ws/apps/coding-sandbox" {
		t.Fatalf("CodeRoot %q %v", root, err)
	}
	// A deployment's key: its ids and leaf now, its paths once they exist.
	d := Key{Tile: "apps/coding-sandbox", Deployment: "blue"}
	if RegistryID(d, "sb-1") != "tile+blue:"+ck+":sb-1" || Leaf(d, "sb-1") != "sbx-"+ck+"+blue-sb-1" || ArchiveKey(d, "sb-1") != "" {
		t.Fatalf("deployment ids %q %q", RegistryID(d, "sb-1"), Leaf(d, "sb-1"))
	}
	if _, err := m.StateDir(d, "sb-1"); err == nil {
		t.Fatal("a deployment's state dir before deployments exist")
	}
	if _, err := m.CodeRoot(d); err == nil {
		t.Fatal("a deployment's code root before deployments exist")
	}
	if _, err := m.ResourceMount(d, "res:x/y"); err == nil {
		t.Fatal("a deployment's resource mount before deployments exist")
	}
	// An admin naming a deployment: unsupported, never main's sandbox.
	e := newEnv(t)
	e.create(ns("sb-1"))
	e.want(e.do(admin, "DELETE", "/sandboxes/sb-1?tile=apps/mgr&deployment=blue", nil), http.StatusNotImplemented, RefUnsupported)
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1", nil), http.StatusOK, "")
}

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

// Deleting a sandbox whose state exists on disk is refused until the
// runtime can remove it confined — and the definition stays.
func TestDeleteKeepsDefinitionWithState(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))
	dir, _ := e.m.StateDir(Key{Tile: "apps/mgr"}, "sb-1")
	if err := os.MkdirAll(filepath.Join(dir, "upper"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.want(e.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNotImplemented, RefUnsupported)
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1", nil), http.StatusOK, "")
	if _, err := os.Stat(filepath.Join(dir, "upper")); err != nil {
		t.Fatalf("state touched: %v", err)
	}
}
