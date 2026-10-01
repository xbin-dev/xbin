package term

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
)

// The boot's look at missing bases covers .xbin/term only: a tile sandbox
// (or one of its snapshots) pinned to a base that isn't installed fails its
// own start (plans/tile-sandbox-runtime.md §7 step 3). Neither keeps xbind
// from booting any more (D175): a terminal layer's start refuses it too.
func TestCheckBaseImagesIgnoresTileSandboxes(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	m := &Manager{Root: root, Rootfs: cur}
	sb := filepath.Join(root, ".xbin", "sbx", "apps~m-1", "web.0123456789ab")
	state, snap := filepath.Join(sb, layers.CurDir), filepath.Join(sb, "snapshots", "s1")
	for _, d := range []string{state, snap} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := layers.Stamp(d, layers.Stamps{Base: "gone", Overlay: layers.OverlayFuse}); err != nil {
			t.Fatal(err)
		}
	}
	if ls, _ := layers.Check(root, cur); len(ls) != 2 || !ls[0].Missing || !ls[1].Missing {
		t.Fatalf("the sandbox and its snapshot aren't layers on a missing base: %+v", ls)
	}
	if missing := m.CheckBaseImages(); len(missing) != 0 {
		t.Fatalf("a tile sandbox's missing base is listed as a terminal's: %v", missing)
	}
	// A terminal layer on a missing base is.
	if v, err := m.layerBase(filepath.Join(root, ".xbin", "term", "apps~a"), "cur1"); err != nil || v != "cur1" {
		t.Fatalf("new layer base: %q %v", v, err)
	}
	if err := layers.Stamp(filepath.Join(root, ".xbin", "term", "apps~a"), layers.Stamps{Base: "gone"}); err != nil {
		t.Fatal(err)
	}
	if missing := m.CheckBaseImages(); len(missing) != 1 || missing[0] != "apps~a→gone" {
		t.Fatalf("a terminal layer on a missing base: %v", missing)
	}
}

// A terminal layer's readable base stamp is kept when the overlay stamp
// beside it can't be read (a symlink): layerBase neither discards it nor
// re-stamps the layer with another base.
func TestEnsureLayerBaseKeepsBaseOverBadOverlay(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	m := &Manager{Root: root, Rootfs: cur}
	layer := filepath.Join(root, ".xbin", "term", "apps~a")
	if err := os.MkdirAll(layer, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := layers.Stamp(layer, layers.Stamps{Base: "old1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hostname", filepath.Join(layer, layers.OverlayFile)); err != nil {
		t.Fatal(err)
	}
	if v, err := m.layerBase(layer, "cur1"); err != nil || v != "old1" {
		t.Fatalf("base: %q %v, want the stamped old1", v, err)
	}
	if s, _ := layers.Read(layer); s.Base != "old1" {
		t.Fatalf("the base stamp was rewritten: %+v", s)
	}
}
