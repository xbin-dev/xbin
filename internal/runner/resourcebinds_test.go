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
	dbPath := filepath.Join(scopeDir, "db.sqlite") // fresh: the file does NOT exist yet

	env := []string{
		"BUXON_RES_DB=" + dbPath,                      // sqlite → bind its DIR (even though the file is absent)
		"BUXON_RES_DB2=" + scopeDir + "/other.sqlite", // second sqlite in the same dir → dedup
		"BUXON_RES_KVX=res:apps/sql-ui/kvx",           // kv → not a path, skip
		"BUXON_SOCKET=/tmp/x/g0.sock",                 // outside root → skip
		"PATH=/usr/bin",                               // not a resource
	}

	binds := resourceBinds(env, root)
	if len(binds) != 1 {
		t.Fatalf("want exactly one bind (the deduped resource dir), got %d: %+v", len(binds), binds)
	}
	b := binds[0]
	if b.Src != scopeDir || b.Dst != scopeDir {
		t.Errorf("bind src/dst = %q/%q, want the scope resource dir %q", b.Src, b.Dst, scopeDir)
	}
	if b.RO {
		t.Errorf("resource dir must be read-write, got RO")
	}
	// The fresh db file itself doesn't exist, but its dir is bound so the backend
	// can create it there (and the WAL sidecars) on the persistent store.
	if _, err := os.Stat(dbPath); err == nil {
		t.Errorf("test setup wrong: db should not exist yet")
	}
}
