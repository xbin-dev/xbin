package term

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/termwire"
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

// covers D175 PD-09 PD-22 (LAND) — what a shell says after an agent
// session's start moved a layer is keyed by the layer that moved: on a
// partitioned tile the person's own (openOpts.layerKey, which both the
// agent session's start and the shell's ask). Ana's agent moving her layer
// is said once to her next shell — never to bob's, whose layer stayed, nor
// to a shell on the tile's own layer, which would learn that someone's
// agent ran there.
func TestPartitionMoveNotes(t *testing.T) {
	m := &Manager{}
	o := func(user string) openOpts {
		if user == "" {
			return openOpts{part: sessionPart{tile: "apps/p"}}
		}
		return openOpts{part: sessionPart{on: true, tile: "apps/p", part: "user:" + user, key: "k-" + user}}
	}
	ana, bob, tile := o("ana").layerKey("apps/p"), o("bob").layerKey("apps/p"), o("").layerKey("apps/p")
	if ana != partLayerKey("apps/p", "k-ana") || tile != termKey("apps/p") || ana == bob {
		t.Fatalf("layer keys: ana %q bob %q tile %q", ana, bob, tile)
	}
	m.noteMove(ana, o("ana").movedLayer()) // ana's agent session's start moved her layer
	if n := m.takeMoveNote(bob, o("bob").movedLayer(), false); n != "" {
		t.Errorf("bob's shell was told of ana's move: %q", n)
	}
	if n := m.takeMoveNote(tile, o("").movedLayer(), false); n != "" {
		t.Errorf("a shell on the tile's own layer was told of ana's move: %q", n)
	}
	if n := m.takeMoveNote(ana, o("ana").movedLayer(), false); n != "\x1b[90mxbin: an agent session's start moved your terminal on apps/p to the new base image — "+baseMovedWhat+"\x1b[0m\r\n" {
		t.Errorf("ana's next shell: %q", n)
	}
}

// covers D175 PD-22 (D177) — the base-move lines name the layer that moved
// as its session's person knows it: a person's own layer on a partitioned
// tile is "your terminal on <tile>" — in her shell's line, the line an
// agent's move leaves her next shell, and her agent session's notice —
// while the tile's own layer (an unpartitioned tile, or a session there
// without a person) stays "this tile's terminal".
func TestPartitionMoveLines(t *testing.T) {
	anaO := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "user:ana", key: "k-ana"}}
	tileO := openOpts{part: sessionPart{tile: "apps/p"}}
	globalO := openOpts{part: sessionPart{on: true, tile: "apps/p", part: "global"}}
	const yours, tiles = "your terminal on apps/p", "this tile's terminal"
	for _, c := range []struct {
		name string
		o    openOpts
		want string
	}{{"ana's own layer", anaO, yours}, {"the tile's layer", tileO, tiles}, {"a session in global without a person", globalO, tiles}} {
		layer := c.o.movedLayer()
		if layer != c.want {
			t.Errorf("%s: named %q, want %q", c.name, layer, c.want)
			continue
		}
		m := &Manager{}
		key := c.o.layerKey("apps/p")
		if n, want := m.takeMoveNote(key, layer, true), "\x1b[90mxbin: "+c.want+" moved to the new base image — "+baseMovedWhat+"\x1b[0m\r\n"; n != want {
			t.Errorf("%s: own move %q, want %q", c.name, n, want)
		}
		var got []SessionEvent
		m.OnEvent = func(cwd string, ev SessionEvent) { got = append(got, ev) }
		s := &Session{ID: "s1", Cwd: "apps/p", kind: KindAgent, hub: termwire.NewHub(0), agent: &agentState{log: agent.NewLog(0, 0)}}
		s.sayBaseMoved(m, key, layer)
		evs, _ := s.agent.log.Since(0)
		var d noticeData
		if len(evs) != 1 || json.Unmarshal(evs[0].Data, &d) != nil || d.Text != "xbin: "+c.want+" moved to the new base image — "+baseMovedWhat {
			t.Errorf("%s: the agent's notice %+v", c.name, evs)
		}
		if n, want := m.takeMoveNote(key, layer, false), "\x1b[90mxbin: an agent session's start moved "+c.want+" to the new base image — "+baseMovedWhat+"\x1b[0m\r\n"; n != want {
			t.Errorf("%s: the next shell after the agent's move %q, want %q", c.name, n, want)
		}
	}
}
