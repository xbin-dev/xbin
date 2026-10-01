package term

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/layers"
)

// The decision a session's start makes for the layer it holds (D173).
func TestMoveBaseDecision(t *testing.T) {
	for _, c := range []struct {
		auto       bool
		layer, cur string
		want       bool
		what       string
	}{
		{true, "old1", "cur1", true, "on, outdated: moves"},
		{true, "cur1", "cur1", false, "on, current: stays"},
		{true, layers.Legacy, "cur1", true, "on, a legacy (unstamped) layer: moves"},
		{false, "old1", "cur1", false, "off, outdated: stays pinned"},
		{false, "cur1", "cur1", false, "off, current: stays"},
	} {
		if got := moveBase(c.auto, c.layer, c.cur); got != c.want {
			t.Errorf("%s: moveBase(%v, %q, %q) = %v", c.what, c.auto, c.layer, c.cur, got)
		}
	}
}

// autoRig is a workspace with a current base cur1, an old base old1
// preserved beside it, and a terminal layer for apps/x stamped old1 with a
// file in its upper (an apt install).
type autoRig struct {
	m      *Manager
	key    string
	layer  string
	marker string
	auto   bool
	rmErr  error
	rmd    []string
}

func newAutoRig(t *testing.T, layerBase string) *autoRig {
	t.Helper()
	root := t.TempDir()
	cur := filepath.Join(root, "rootfs")
	stampBase(t, cur, "cur1")
	stampBase(t, cur+"-old1", "old1")
	r := &autoRig{m: &Manager{Root: root, Rootfs: cur, envHeld: map[string]bool{}, sessions: map[string]*Session{}}, key: termKey("apps/x")}
	r.layer = filepath.Join(root, ".xbin", "term", r.key)
	r.marker = filepath.Join(r.layer, "upper", "usr", "bin", "installed")
	if err := os.MkdirAll(filepath.Dir(r.marker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.marker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := layers.Stamp(r.layer, layers.Stamps{Base: layerBase}); err != nil {
		t.Fatal(err)
	}
	r.m.BaseAutoUpdate = func() bool { return r.auto }
	r.m.rmTree = func(dir string) error { // stands in for the confined removal
		r.rmd = append(r.rmd, dir)
		if r.rmErr != nil {
			return r.rmErr
		}
		return os.RemoveAll(dir)
	}
	return r
}

func (r *autoRig) stamp(t *testing.T) string {
	t.Helper()
	s, err := layers.Read(r.layer)
	if err != nil {
		t.Fatal(err)
	}
	return s.Base
}

func (r *autoRig) kept(t *testing.T) bool {
	t.Helper()
	_, err := os.Stat(r.marker)
	return err == nil
}

// The start of a session on a layer built on an older base, with auto-update
// on and off, with the layer free and held by a running session.
func TestClaimLayerBaseAutoUpdate(t *testing.T) {
	cases := []struct {
		auto, running bool
		layer         string // the layer's base
		wantMoved     string
		wantBase      string // the stamp after
		wantKept      bool   // the upper's file is still there
		wantOn        string // which rootfs it stacks on: "cur" | "old" | "" (none: held)
	}{
		{auto: true, layer: "old1", wantMoved: "old1", wantBase: "cur1", wantOn: "cur"},
		{auto: true, layer: "cur1", wantBase: "cur1", wantKept: true, wantOn: "cur"},
		{auto: true, running: true, layer: "old1", wantBase: "old1", wantKept: true},
		{auto: true, running: true, layer: "cur1", wantBase: "cur1", wantKept: true},
		{auto: false, layer: "old1", wantBase: "old1", wantKept: true, wantOn: "old"},
		{auto: false, layer: "cur1", wantBase: "cur1", wantKept: true, wantOn: "cur"},
		{auto: false, running: true, layer: "old1", wantBase: "old1", wantKept: true},
		{auto: false, running: true, layer: "cur1", wantBase: "cur1", wantKept: true},
	}
	for _, c := range cases {
		name := map[bool]string{true: "on", false: "off"}[c.auto] + "/" + c.layer + map[bool]string{true: "/running", false: ""}[c.running]
		t.Run(name, func(t *testing.T) {
			r := newAutoRig(t, c.layer)
			r.auto = c.auto
			if c.running && !r.m.acquireEnv(r.key) { // a live session holds the layer
				t.Fatal("acquire")
			}
			lc, held, err := r.m.claimLayer(r.key)
			if err != nil {
				t.Fatal(err)
			}
			if held != c.running {
				t.Fatalf("held=%v", held)
			}
			if lc.moved != c.wantMoved {
				t.Fatalf("moved off %q, want %q", lc.moved, c.wantMoved)
			}
			if got := r.stamp(t); got != c.wantBase {
				t.Fatalf("the layer is stamped %q, want %q", got, c.wantBase)
			}
			if got := r.kept(t); got != c.wantKept {
				t.Fatalf("the upper's file kept=%v, want %v", got, c.wantKept)
			}
			on := map[string]string{"cur": r.m.Rootfs, "old": r.m.Rootfs + "-old1"}[c.wantOn]
			if lc.base != on {
				t.Fatalf("stacks on %q, want %q", lc.base, on)
			}
			if c.running && len(r.rmd) != 0 {
				t.Fatalf("removed %v under a running session", r.rmd)
			}
			if !c.running {
				if r.m.acquireEnv(r.key) {
					t.Fatal("the claim doesn't hold the layer")
				}
				r.m.releaseEnv(r.key)
			}
		})
	}
}

// A layer whose old base is no longer installed (layers.GC released it, or
// it was deleted): with auto-update on it moves to the current base like any
// other; off, the start fails as it always has. The boot gate lets it
// through only when on — its next session discards it rather than stack it.
func TestClaimLayerOldBaseGone(t *testing.T) {
	r := newAutoRig(t, "gone")
	r.auto = false
	if err := r.m.CheckBaseImages(); err == nil {
		t.Fatal("off: the boot gate passed a layer on a missing base")
	}
	if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("off: %v", err)
	}
	if !r.kept(t) || r.stamp(t) != "gone" {
		t.Fatal("off: the layer changed")
	}
	if !r.m.acquireEnv(r.key) {
		t.Fatal("a failed start kept the layer held")
	}
	r.m.releaseEnv(r.key)

	r.auto = true
	if err := r.m.CheckBaseImages(); err != nil {
		t.Fatalf("on: the boot gate refused a layer its next session moves: %v", err)
	}
	lc, held, err := r.m.claimLayer(r.key)
	if err != nil || held {
		t.Fatalf("on: %v held=%v", err, held)
	}
	if lc.moved != "gone" || lc.base != r.m.Rootfs || r.stamp(t) != "cur1" || r.kept(t) {
		t.Fatalf("on: %+v stamp %q kept %v", lc, r.stamp(t), r.kept(t))
	}
	r.m.releaseEnv(r.key)
	// Nothing pins the old base any more: the boot's GC releases it.
	pins, err := layers.Pinned(r.m.Root, nil)
	if err != nil || pins["gone"] || pins["old1"] || !pins["cur1"] {
		t.Fatalf("pins after the move: %v %v", pins, err)
	}
	if gone := layers.GC(r.m.Rootfs, pins); len(gone) != 1 || gone[0] != r.m.Rootfs+"-old1" {
		t.Fatalf("GC released %v", gone)
	}
}

// A removal that fails fails the start: what it left may be half a layer,
// and nothing mounts that. The claim is released; the next start tries
// again.
func TestClaimLayerMoveFails(t *testing.T) {
	r := newAutoRig(t, "old1")
	r.auto = true
	r.rmErr = errors.New("confined run failed")
	if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "couldn't be moved") {
		t.Fatalf("a failed removal: %v", err)
	}
	if !r.m.acquireEnv(r.key) {
		t.Fatal("a failed move kept the layer held")
	}
	r.m.releaseEnv(r.key)
	r.rmErr = nil
	if lc, _, err := r.m.claimLayer(r.key); err != nil || lc.moved != "old1" || r.stamp(t) != "cur1" {
		t.Fatalf("the retry: %+v %v", lc, err)
	}
}

// Not wired (nil hook): off — today's pinning.
func TestBaseAutoUpdateUnwiredIsOff(t *testing.T) {
	r := newAutoRig(t, "old1")
	r.m.BaseAutoUpdate = nil
	if r.m.BaseAutoUpdateOn() {
		t.Fatal("an unwired hook reads on")
	}
	lc, _, err := r.m.claimLayer(r.key)
	if err != nil || lc.moved != "" || !r.kept(t) {
		t.Fatalf("%+v %v", lc, err)
	}
}
