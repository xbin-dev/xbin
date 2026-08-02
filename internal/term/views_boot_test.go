package term

import (
	"os"
	"path/filepath"
	"testing"
)

// D40 view dirs under .xbin/term/ are NOT env layers: CheckBaseImages must
// sweep orphaned view-* dirs instead of treating them as layers pinned to a
// phantom base — that fatal crash-looped a production boot (2026-08-02).
func TestCheckBaseImagesSweepsOrphanedViews(t *testing.T) {
	root := t.TempDir()
	rootfs := filepath.Join(root, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		t.Fatal(err)
	}
	view := filepath.Join(root, ".xbin", "term", "view-deadbeef")
	if err := os.MkdirAll(view, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(view, "xbin.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Root: root, Rootfs: rootfs}
	if err := m.CheckBaseImages(); err != nil {
		t.Fatalf("orphaned views must not fail the boot gate: %v", err)
	}
	if _, err := os.Stat(view); !os.IsNotExist(err) {
		t.Fatal("orphaned view dir must be swept at boot")
	}
}
