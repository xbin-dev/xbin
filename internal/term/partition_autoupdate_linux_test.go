//go:build linux && integration

// Run with: go test -tags=integration -run '^TestPartitionLayer' ./internal/term/
// Needs user namespaces and an unpacked rootfs (XBIN_TEST_ROOTFS, or the
// repo's .rootfs); skips otherwise.
package term

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D175 PD-22 (LAND) — in real sandboxes: a person's layer on a
// partitioned tile, built on an older base, stays on it while base
// auto-update is off; with it on, that person's next terminal moves it —
// the grey line first, their install gone, the layer stamped with the
// rootfs's base, the old layer removed by the confined remover — while
// another person's terminal that is still running keeps its layer and base
// (a second window of theirs runs on an ephemeral upper), until their next
// start moves theirs too. The tile's own layer is never touched. A
// switch's stop and wipe afterwards delete both people's (fresh) layers.
// The "older base" is the rootfs under a second name, as in
// TestConfinedBaseAutoUpdate.
func TestPartitionLayerBaseMoveIsolated(t *testing.T) {
	real := layerRootfs(t)
	cur := layers.BaseVersion(real)
	if cur == "old1" || cur == layers.Legacy {
		t.Skip("the rootfs is unstamped, or stamped with the test's name")
	}
	root, bases := t.TempDir(), t.TempDir()
	rootfs := filepath.Join(bases, "rootfs")
	for _, l := range []string{rootfs, rootfs + "-old1"} {
		if err := os.Symlink(real, l); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "apps", "p"), 0o755); err != nil {
		t.Fatal(err)
	}
	confine.Configure(real)
	t.Cleanup(func() {
		_ = confine.RemoveAll(context.Background(), root)
		confine.Configure("")
	})
	m := NewManager(root, nil)
	m.Isolate, m.Rootfs = true, rootfs
	(&partHooks{}).install(m)
	var auto atomic.Bool
	m.BaseAutoUpdate = auto.Load
	t.Cleanup(m.waitMoved)

	tk := util.TileKey("apps/p")
	anaLayer := filepath.Join(root, ".xbin", "term-part", tk, "k-ana")
	bobLayer := filepath.Join(root, ".xbin", "term-part", tk, "k-bob")
	tileLayer := filepath.Join(root, ".xbin", "term", termKey("apps/p"))
	for _, l := range []string{anaLayer, bobLayer, tileLayer} {
		if err := os.MkdirAll(l, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := layers.Stamp(l, layers.Stamps{Base: "old1"}); err != nil {
			t.Fatal(err)
		}
	}
	stampOf := func(dir string) string {
		t.Helper()
		s, err := layers.Read(dir)
		if err != nil {
			t.Fatal(err)
		}
		return s.Base
	}
	stop := func() {
		t.Helper()
		if err := m.StopTileSessions("apps/p"); err != nil {
			t.Fatal(err)
		}
	}
	const install = "mkdir -p /opt/mark && echo x > /opt/mark/f"
	const probe = "test -e /opt/mark/f && echo LAYER-''KEPT || echo LAYER-''FRESH"
	const line = "terminal moved to the new base image"
	ana, bob := termAdmin("ana"), termAdmin("bob")

	// off: both install on old1, and keep their layers
	runIn(t, m, ana, "apps/p", install)
	runIn(t, m, bob, "apps/p", install)
	stop()
	if out := runIn(t, m, ana, "apps/p", probe); !strings.Contains(out, "LAYER-KEPT") || strings.Contains(out, line) {
		t.Fatalf("off: ana's layer moved: %q", out)
	}
	stop()
	if stampOf(anaLayer) != "old1" || stampOf(bobLayer) != "old1" {
		t.Fatal("off: a person's layer was restamped")
	}

	// bob's terminal runs (runIn leaves it attached-less, alive) when the
	// setting turns on
	runIn(t, m, bob, "apps/p", "true")
	auto.Store(true)
	if out := runIn(t, m, bob, "apps/p", probe); strings.Contains(out, line) {
		t.Fatalf("bob's second window moved the layer his running terminal holds: %q", out)
	}
	if stampOf(bobLayer) != "old1" {
		t.Fatal("bob's layer moved under his running terminal")
	}

	// ana's next terminal moves hers: the line first, the install gone
	out := runIn(t, m, ana, "apps/p", probe)
	if !strings.Contains(out, line) || !strings.Contains(out, "LAYER-FRESH") || strings.Index(out, line) > strings.Index(out, "LAYER-FRESH") {
		t.Fatalf("on: ana's layer didn't move first: %q", out)
	}
	if stampOf(anaLayer) != cur || stampOf(bobLayer) != "old1" || stampOf(tileLayer) != "old1" {
		t.Fatalf("on: stamps ana %q bob %q tile %q", stampOf(anaLayer), stampOf(bobLayer), stampOf(tileLayer))
	}
	stop()
	m.waitMoved() // the confined remover's run on ana's put-aside layer
	if ents, err := os.ReadDir(m.movedRoot()); err != nil || len(ents) != 0 {
		t.Fatalf("ana's old layer wasn't removed: %v %v", ents, err)
	}

	// bob's next start, his terminals ended, moves his
	if out := runIn(t, m, bob, "apps/p", probe); !strings.Contains(out, line) || !strings.Contains(out, "LAYER-FRESH") {
		t.Fatalf("bob's next start didn't move his layer: %q", out)
	}
	if stampOf(bobLayer) != cur || stampOf(tileLayer) != "old1" {
		t.Fatalf("stamps bob %q tile %q", stampOf(bobLayer), stampOf(tileLayer))
	}

	// a switch: stop, then wipe — the people's fresh layers go, the tile's stays
	stop()
	got, err := m.WipePartitionTile("apps/p", false)
	if err != nil || got.Layers != 2 {
		t.Fatalf("wipe: %+v %v", got, err)
	}
	for _, l := range []string{anaLayer, bobLayer} {
		if _, err := os.Lstat(l); !os.IsNotExist(err) {
			t.Errorf("%s survived the wipe: %v", l, err)
		}
	}
	if stampOf(tileLayer) != "old1" {
		t.Error("the wipe touched the tile's own layer")
	}
	m.waitMoved()
	if ents, _ := os.ReadDir(m.movedRoot()); len(ents) != 0 {
		t.Fatalf("left in term-moved: %v", ents)
	}
}
