package term

import (
	"os"
	"path/filepath"
	"testing"
)

func stampBase(t *testing.T, dir, ver string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "etc", "xbin-base-version"), []byte(ver+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBaseVersionAndResolve(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "abc123")
	if v := baseVersion(cur); v != "abc123" {
		t.Fatalf("baseVersion: %q", v)
	}
	// Unstamped rootfs → v0.
	un := filepath.Join(root, "unstamped")
	os.MkdirAll(un, 0o755)
	if v := baseVersion(un); v != "v0" {
		t.Fatalf("unstamped baseVersion: %q", v)
	}
	// Current version resolves to the current rootfs.
	if p, ok := resolveBase(cur, "abc123"); !ok || p != cur {
		t.Fatalf("resolve current: %q %v", p, ok)
	}
	// An old version resolves to the preserved sibling if present.
	sib := cur + "-v0"
	stampBase(t, sib, "v0")
	if p, ok := resolveBase(cur, "v0"); !ok || p != sib {
		t.Fatalf("resolve preserved: %q %v", p, ok)
	}
	// A missing base doesn't resolve.
	if _, ok := resolveBase(cur, "gone"); ok {
		t.Fatal("missing base should not resolve")
	}
}

func TestEnsureLayerBaseAndGate(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	m := &Manager{Root: root, Rootfs: cur}

	// Brand-new layer stamps the current base.
	newLayer := filepath.Join(root, ".xbin", "term", "apps~a")
	if v := m.ensureLayerBase(newLayer); v != "cur1" {
		t.Fatalf("new layer base: %q", v)
	}
	// Pre-existing unstamped layer → v0 (legacy migration).
	legacy := filepath.Join(root, ".xbin", "term", "apps~b")
	os.MkdirAll(filepath.Join(legacy, "upper"), 0o755)
	if v := m.ensureLayerBase(legacy); v != "v0" {
		t.Fatalf("legacy layer base: %q", v)
	}
	// layerOutdated: the legacy (v0) layer is older than cur1.
	if !m.layerOutdated("apps~b") {
		t.Fatal("legacy layer should be outdated")
	}
	if m.layerOutdated("apps~a") {
		t.Fatal("current layer should not be outdated")
	}
	// Safety gate: apps~b pins v0 which isn't installed → error.
	if err := m.CheckBaseImages(); err == nil {
		t.Fatal("gate should fail when a pinned base is missing")
	}
	// Preserve the v0 base → gate passes.
	stampBase(t, cur+"-v0", "v0")
	if err := m.CheckBaseImages(); err != nil {
		t.Fatalf("gate should pass once bases exist: %v", err)
	}
}
