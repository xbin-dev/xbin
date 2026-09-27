package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
)

// The boot gate covers .xbin/term only: a tile sandbox (or one of its
// snapshots) pinned to a base that isn't installed fails its own start
// instead, and never keeps xbind from booting (plans/tile-sandbox-runtime.md
// §7 step 3).
func TestCheckBaseImagesIgnoresTileSandboxes(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	m := &Manager{Root: root, Rootfs: cur}
	sb := filepath.Join(root, ".xbin", "sbx", "apps~m-1", "web")
	snap := filepath.Join(sb, "snapshots", "s1")
	if err := os.MkdirAll(snap, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{sb, snap} {
		if err := layers.Stamp(d, layers.Stamps{Base: "gone", Overlay: layers.OverlayFuse}); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.CheckBaseImages(); err != nil {
		t.Fatalf("a tile sandbox's missing base gated the boot: %v", err)
	}
	// A terminal layer on a missing base still does.
	if v := m.ensureLayerBase(filepath.Join(root, ".xbin", "term", "apps~a")); v != "cur1" {
		t.Fatalf("new layer base: %q", v)
	}
	if err := layers.Stamp(filepath.Join(root, ".xbin", "term", "apps~a"), layers.Stamps{Base: "gone"}); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckBaseImages(); err == nil {
		t.Fatal("a terminal layer on a missing base passed the gate")
	}
}
