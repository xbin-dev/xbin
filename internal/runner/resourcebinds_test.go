package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResourceBinds(t *testing.T) {
	root := t.TempDir()
	scopeDir := filepath.Join(root, "data", "resources", "apps~sql-ui")
	if err := os.MkdirAll(scopeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(scopeDir, "db.sqlite") // fresh sqlite: the file does NOT exist yet
	fsDir := filepath.Join(scopeDir, "store")      // a filesystem resource: an existing DIR
	if err := os.MkdirAll(fsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	env := []string{
		"XBIN_RES_DB=" + dbPath,                      // sqlite (file) → bind its parent dir
		"XBIN_RES_DB2=" + scopeDir + "/other.sqlite", // another sqlite in the same dir → dedup
		"XBIN_RES_STORE=" + fsDir,                    // filesystem (dir) → bind the dir itself
		"XBIN_RES_KVX=res:apps/sql-ui/kvx",           // kv → not a path, skip
		"XBIN_SOCKET=/tmp/x/g0.sock",                 // outside root → skip
		"PATH=/usr/bin",                               // not a resource
	}

	bound := map[string]bool{}
	for _, b := range resourceBinds(env, root) {
		if b.RO || b.Src != b.Dst {
			t.Errorf("resource bind must be rw + same src/dst: %+v", b)
		}
		bound[b.Src] = true
	}
	// sqlite files → their parent (the scope dir); the filesystem resource → itself.
	if !bound[scopeDir] {
		t.Errorf("sqlite resources should bind the scope dir %q; bound=%v", scopeDir, bound)
	}
	if !bound[fsDir] {
		t.Errorf("filesystem resource should bind its own dir %q; bound=%v", fsDir, bound)
	}
	// The fresh sqlite file doesn't exist yet — the dir bind lets the backend
	// create it (and the WAL sidecars) on the persistent store.
	if _, err := os.Stat(dbPath); err == nil {
		t.Errorf("test setup wrong: db should not exist yet")
	}
}
