package term

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D175 PD-22 (LAND) — base auto-update moves a person's layer on a
// partitioned tile (.xbin/term-part/<TileKey>/<pkey>) the way it moves a
// tile's: at the start of that person's next session, never under a
// session that holds it, and nobody else's layer with it — not the tile's
// own, not another person's. The status the terminal window reads is the
// person's layer's. The boot lists a person's layer whose base is gone
// under its layer key. A partition mode switch's wipe after a move deletes
// the fresh layer like any person layer, and the layer the move put aside
// is the remover's alone: the two never touch the same tree.
func TestPartitionLayerBaseAutoUpdate(t *testing.T) {
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	stampBase(t, cur+"-old1", "old1")
	m := &Manager{Root: root, Rootfs: cur, envHeld: map[string]bool{}, sessions: map[string]*Session{}}
	auto := true
	m.BaseAutoUpdate = func() bool { return auto }
	m.rmTree = os.RemoveAll // stands in for the confined removal
	t.Cleanup(m.waitMoved)

	// ana's and bob's layers on apps/p, and the tile's own: all on old1,
	// each with an install in its upper
	layer := func(key string) (dir, marker string) {
		t.Helper()
		dir = m.layerDir(key)
		marker = filepath.Join(dir, "upper", "opt", "installed")
		if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := layers.Stamp(dir, layers.Stamps{Base: "old1"}); err != nil {
			t.Fatal(err)
		}
		return dir, marker
	}
	anaKey, bobKey, tileKey := partLayerKey("apps/p", "k-ana"), partLayerKey("apps/p", "k-bob"), termKey("apps/p")
	anaDir, anaMark := layer(anaKey)
	bobDir, bobMark := layer(bobKey)
	tileDir, tileMark := layer(tileKey)
	if want := filepath.Join(root, ".xbin", "term-part", util.TileKey("apps/p"), "k-ana"); anaDir != want {
		t.Fatalf("ana's layer is at %s, want %s", anaDir, want)
	}
	stampOf := func(dir string) string {
		t.Helper()
		s, err := layers.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		return s.Base
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	if ex, old := m.envStatusOf(anaKey); !ex || !old {
		t.Fatalf("ana's layer before the move: exists %v outdated %v", ex, old)
	}
	// bob's session runs: his layer is held, so a second start of his
	// (another window) runs on an ephemeral upper and moves nothing
	if !m.acquireEnv(bobKey) {
		t.Fatal("acquire bob's layer")
	}
	if _, held, err := m.claimLayer(bobKey); err != nil || !held {
		t.Fatalf("bob's second start: held %v, %v", held, err)
	}
	if stampOf(bobDir) != "old1" || !exists(bobMark) {
		t.Fatal("bob's layer moved under his running session")
	}

	// ana's next start moves hers, and nobody else's
	lc, held, err := m.claimLayer(anaKey)
	if err != nil || held {
		t.Fatalf("ana's start: held %v, %v", held, err)
	}
	if lc.dir != anaDir || lc.moved != "old1" || lc.base != cur || stampOf(anaDir) != "cur1" || exists(anaMark) {
		t.Fatalf("ana's layer didn't move: %+v, stamped %q, install kept %v", lc, stampOf(anaDir), exists(anaMark))
	}
	m.releaseEnv(anaKey)
	if ex, old := m.envStatusOf(anaKey); !ex || old {
		t.Fatalf("ana's layer after the move: exists %v outdated %v", ex, old)
	}
	if stampOf(tileDir) != "old1" || !exists(tileMark) {
		t.Error("ana's move touched the tile's own layer")
	}
	if stampOf(bobDir) != "old1" || !exists(bobMark) {
		t.Error("ana's move touched bob's layer")
	}
	m.waitMoved()
	if ents, _ := os.ReadDir(m.movedRoot()); len(ents) != 0 {
		t.Fatalf("the put-aside layer wasn't removed: %v", ents)
	}

	// bob's session ends; his layer's base disappears: the boot lists it
	// by its layer key, and his next start moves it
	m.releaseEnv(bobKey)
	if err := layers.Stamp(bobDir, layers.Stamps{Base: "gone"}); err != nil {
		t.Fatal(err)
	}
	if missing := m.CheckBaseImages(); len(missing) != 1 || missing[0] != bobKey+"→gone" {
		t.Fatalf("the boot's look: %v, want [%s→gone]", missing, bobKey)
	}
	auto = false
	if _, _, err := m.claimLayer(bobKey); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("off, bob's base gone: %v", err)
	}
	auto = true
	if lc, _, err := m.claimLayer(bobKey); err != nil || lc.moved != "gone" || stampOf(bobDir) != "cur1" {
		t.Fatalf("on, bob's base gone: %+v %v", lc, err)
	}
	m.releaseEnv(bobKey)

	// a switch's wipe: both people's layers go, the tile's stays; the
	// remover had the put-aside layers, the wipe the fresh ones
	got, err := m.WipePartitionTile("apps/p", false)
	if err != nil || got.Layers != 2 {
		t.Fatalf("the wipe: %+v %v", got, err)
	}
	if exists(anaDir) || exists(bobDir) || !exists(tileMark) {
		t.Fatalf("after the wipe: ana's %v, bob's %v, the tile's install %v", exists(anaDir), exists(bobDir), exists(tileMark))
	}
	m.waitMoved()
	if ents, _ := os.ReadDir(m.movedRoot()); len(ents) != 0 {
		t.Fatalf("left in term-moved: %v", ents)
	}
}
