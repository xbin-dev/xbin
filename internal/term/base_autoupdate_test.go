package term

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/termwire"
)

// The decision a session's start makes for the layer it holds (D175).
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
		{true, "old1", layers.Legacy, false, "on, an unstamped rootfs: nothing to move to"},
		{false, "old1", "cur1", false, "off, outdated: stays pinned"},
		{false, "cur1", "cur1", false, "off, current: stays"},
	} {
		if got := moveBase(c.auto, c.layer, c.cur); got != c.want {
			t.Errorf("%s: moveBase(%v, %q, %q) = %v", c.what, c.auto, c.layer, c.cur, got)
		}
	}
}

// autoRig is a workspace with a current base cur1, an old base old1
// preserved beside it, and a terminal layer for apps/x stamped with a base
// and a file in its upper (an apt install). Its rmTree stands in for the
// confined removal and records what it was asked to remove.
type autoRig struct {
	m      *Manager
	key    string
	layer  string
	marker string
	auto   bool

	mu    sync.Mutex
	rmErr error
	rmd   []string
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
	if layerBase != "" {
		if err := layers.Stamp(r.layer, layers.Stamps{Base: layerBase}); err != nil {
			t.Fatal(err)
		}
	}
	r.m.BaseAutoUpdate = func() bool { return r.auto }
	r.m.rmTree = func(dir string) error { // the remover runs it in the background
		r.mu.Lock()
		defer r.mu.Unlock()
		r.rmd = append(r.rmd, dir)
		if r.rmErr != nil {
			return r.rmErr
		}
		return os.RemoveAll(dir)
	}
	t.Cleanup(r.m.waitMoved) // no remover outlives its TempDir
	return r
}

func (r *autoRig) setRmErr(err error) {
	r.mu.Lock()
	r.rmErr = err
	r.mu.Unlock()
}

// removed is what rmTree was asked to remove, once the remover is idle.
func (r *autoRig) removed() []string {
	r.m.waitMoved()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.rmd...)
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

// left is what .xbin/term-moved holds.
func (r *autoRig) left(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(r.m.movedRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		out = append(out, e.Name())
	}
	return out
}

// free reports the layer isn't held (a failed start let it go).
func (r *autoRig) free(t *testing.T) {
	t.Helper()
	if !r.m.acquireEnv(r.key) {
		t.Fatal("the layer is still held")
	}
	r.m.releaseEnv(r.key)
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
			rmd := r.removed()
			if c.wantMoved == "" && len(rmd) != 0 {
				t.Fatalf("removed %v", rmd)
			}
			if c.wantMoved != "" {
				// put aside, then removed in the background — not in place
				if len(rmd) != 1 || filepath.Dir(rmd[0]) != r.m.movedRoot() || !strings.HasPrefix(filepath.Base(rmd[0]), r.key+".") {
					t.Fatalf("the remover removed %v, want one dir in %s", rmd, r.m.movedRoot())
				}
				if left := r.left(t); len(left) != 0 {
					t.Fatalf("left in term-moved: %v", left)
				}
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
// other; off, its start fails as it always has. Either way the boot only
// lists it (D175: it no longer refuses to boot — turning the setting off
// with such a layer around must not keep xbind down).
func TestClaimLayerOldBaseGone(t *testing.T) {
	r := newAutoRig(t, "gone")
	r.auto = false
	if missing := r.m.CheckBaseImages(); len(missing) != 1 || missing[0] != r.key+"→gone" {
		t.Fatalf("off: the boot's look: %v", missing)
	}
	if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("off: %v", err)
	}
	if !r.kept(t) || r.stamp(t) != "gone" {
		t.Fatal("off: the layer changed")
	}
	r.free(t)

	r.auto = true
	if missing := r.m.CheckBaseImages(); len(missing) != 1 {
		t.Fatalf("on: the boot's look: %v", missing)
	}
	lc, held, err := r.m.claimLayer(r.key)
	if err != nil || held {
		t.Fatalf("on: %v held=%v", err, held)
	}
	if lc.moved != "gone" || lc.base != r.m.Rootfs || r.stamp(t) != "cur1" || r.kept(t) {
		t.Fatalf("on: %+v stamp %q kept %v", lc, r.stamp(t), r.kept(t))
	}
	r.m.releaseEnv(r.key)
	if missing := r.m.CheckBaseImages(); len(missing) != 0 {
		t.Fatalf("after the move: %v", missing)
	}
	// Nothing pins the old base any more: the boot's GC releases it.
	r.m.waitMoved()
	pins, err := layers.Pinned(r.m.Root, nil)
	if err != nil || pins["gone"] || pins["old1"] || !pins["cur1"] {
		t.Fatalf("pins after the move: %v %v", pins, err)
	}
	if gone := layers.GC(r.m.Rootfs, pins); len(gone) != 1 || gone[0] != r.m.Rootfs+"-old1" {
		t.Fatalf("GC released %v", gone)
	}
}

// Where the old layer can't be put aside (here: .xbin/term-moved is a file),
// it is removed in place, confined, before the session starts; a removal
// that fails then fails the start — what it left may be half a layer, and
// nothing mounts that. The claim is released; the next start tries again.
func TestClaimLayerMoveInPlace(t *testing.T) {
	r := newAutoRig(t, "old1")
	r.auto = true
	if err := os.WriteFile(r.m.movedRoot(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	r.setRmErr(errors.New("confined run failed"))
	if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "couldn't be moved") {
		t.Fatalf("a failed removal: %v", err)
	}
	r.free(t)
	r.setRmErr(nil)
	lc, _, err := r.m.claimLayer(r.key)
	if err != nil || lc.moved != "old1" || r.stamp(t) != "cur1" || r.kept(t) {
		t.Fatalf("the retry: %+v %v", lc, err)
	}
	if rmd := r.removed(); len(rmd) != 2 || rmd[0] != r.layer || rmd[1] != r.layer {
		t.Fatalf("removed %v, want the layer in place twice", rmd)
	}
}

// The remover failing in the background doesn't fail the start (the layer
// was already put aside): what it couldn't remove stays in term-moved, and
// the next boot's sweep has it removed.
func TestMovedLayerRemoverRetriesAtBoot(t *testing.T) {
	r := newAutoRig(t, "old1")
	r.auto = true
	r.setRmErr(errors.New("confined run failed"))
	lc, _, err := r.m.claimLayer(r.key)
	if err != nil || lc.moved != "old1" {
		t.Fatalf("%+v %v", lc, err)
	}
	r.m.releaseEnv(r.key)
	if rmd := r.removed(); len(rmd) != 1 {
		t.Fatalf("removed %v", rmd)
	}
	if left := r.left(t); len(left) != 1 {
		t.Fatalf("left %v, want the old layer", left)
	}
	r.setRmErr(nil)
	r.m.CheckBaseImages() // the boot
	if rmd := r.removed(); len(rmd) != 2 || rmd[1] != rmd[0] {
		t.Fatalf("removed %v", rmd)
	}
	if left := r.left(t); len(left) != 0 {
		t.Fatalf("left after the sweep: %v", left)
	}
}

// unreadable makes p unreadable two ways: mode 000 (not for root, who reads
// it anyway), or replaced by something readStamp/ReadFile refuse — a
// symlink to a copy (a stamp is opened O_NOFOLLOW) or a directory.
func unreadable(t *testing.T, p, how string) (restore func()) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	switch how {
	case "mode000":
		if os.Geteuid() == 0 {
			t.Skip("root reads a mode-000 file")
		}
		if err := os.Chmod(p, 0); err != nil {
			t.Fatal(err)
		}
		return func() { _ = os.Chmod(p, 0o644) }
	case "symlink":
		cp := p + ".copy"
		if err := os.WriteFile(cp, b, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(cp, p); err != nil {
			t.Fatal(err)
		}
		return func() { _ = os.Remove(p); _ = os.Rename(cp, p) }
	case "dir":
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return func() { _ = os.Remove(p); _ = os.WriteFile(p, b, 0o644) }
	}
	t.Fatalf("how %q", how)
	return nil
}

// A layer stamp that is there but can't be read (EACCES, EIO, a link) is
// not a missing one: the start fails and the layer is left as it is —
// never re-stamped "v0" and then discarded as outdated while it is on the
// current base, auto-update on.
func TestClaimLayerUnreadableStamp(t *testing.T) {
	for _, how := range []string{"mode000", "symlink"} {
		t.Run(how, func(t *testing.T) {
			r := newAutoRig(t, "cur1")
			r.auto = true
			restore := unreadable(t, filepath.Join(r.layer, layers.BaseFile), how)
			if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "base stamp can't be read") {
				t.Fatalf("an unreadable stamp: %v", err)
			}
			r.free(t)
			restore()
			if rmd := r.removed(); len(rmd) != 0 {
				t.Fatalf("removed %v", rmd)
			}
			if !r.kept(t) || r.stamp(t) != "cur1" {
				t.Fatalf("the layer changed: kept %v stamp %q", r.kept(t), r.stamp(t))
			}
			// readable again: it was on the current base all along
			if lc, _, err := r.m.claimLayer(r.key); err != nil || lc.moved != "" || !r.kept(t) {
				t.Fatalf("after: %+v %v", lc, err)
			}
		})
	}
}

// The rootfs's version file there but unreadable is not an unstamped base
// ("v0"): every start fails — nothing moved, stamped or made — until it
// reads again; a read that worked is kept (the rootfs can't change under a
// running xbind), and a failed one isn't.
func TestClaimLayerUnreadableRootfsVersion(t *testing.T) {
	for _, how := range []string{"mode000", "dir"} {
		t.Run(how, func(t *testing.T) {
			r := newAutoRig(t, "cur1")
			r.auto = true
			restore := unreadable(t, filepath.Join(r.m.Rootfs, layers.VersionFile), how)
			if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "version") {
				t.Fatalf("an unreadable base version: %v", err)
			}
			r.free(t)
			fresh := termKey("apps/new")
			if _, _, err := r.m.claimLayer(fresh); err == nil {
				t.Fatal("a brand-new layer started on an unknown base")
			}
			if _, err := os.Stat(filepath.Join(r.m.Root, ".xbin", "term", fresh)); !os.IsNotExist(err) {
				t.Fatalf("a brand-new layer was made: %v", err)
			}
			if r.m.layerOutdated(r.key) {
				t.Fatal("outdated on a guess")
			}
			restore()
			if rmd := r.removed(); len(rmd) != 0 {
				t.Fatalf("removed %v", rmd)
			}
			if !r.kept(t) || r.stamp(t) != "cur1" {
				t.Fatalf("the layer changed: kept %v stamp %q", r.kept(t), r.stamp(t))
			}
			lc, _, err := r.m.claimLayer(r.key)
			if err != nil || lc.moved != "" || !r.kept(t) {
				t.Fatalf("readable again: %+v %v", lc, err)
			}
			r.m.releaseEnv(r.key)
			// Read once: the file changing under a running xbind (the
			// installer stops it first) moves nothing.
			stampBase(t, r.m.Rootfs, "cur2")
			if lc, _, err := r.m.claimLayer(r.key); err != nil || lc.moved != "" || !r.kept(t) {
				t.Fatalf("after the file changed: %+v %v", lc, err)
			}
		})
	}
}

// The fresh layer's stamp failing after the move fails the start: the empty
// dir goes again, so the next start makes a new layer on the current base
// (never one read as legacy and moved — or refused — once more).
func TestClaimLayerStampFailsAfterMove(t *testing.T) {
	r := newAutoRig(t, "old1")
	r.auto = true
	fail := true
	r.m.stampHook = func(dir string, s layers.Stamps) error {
		if fail {
			return errors.New("EIO")
		}
		return layers.Stamp(dir, s)
	}
	if _, _, err := r.m.claimLayer(r.key); err == nil || !strings.Contains(err.Error(), "couldn't be moved") {
		t.Fatalf("a failed stamp: %v", err)
	}
	r.free(t)
	if _, err := os.Lstat(r.layer); !os.IsNotExist(err) {
		t.Fatalf("an unstamped layer dir was left: %v", err)
	}
	if rmd := r.removed(); len(rmd) != 1 {
		t.Fatalf("removed %v", rmd)
	}
	fail = false
	lc, _, err := r.m.claimLayer(r.key)
	if err != nil || lc.base != r.m.Rootfs || r.stamp(t) != "cur1" || r.kept(t) {
		t.Fatalf("the next start: %+v %v", lc, err)
	}
}

// A brand-new layer whose stamp can't be written fails the start and isn't
// left unstamped (it would read as legacy next time).
func TestClaimLayerNewStampFails(t *testing.T) {
	r := newAutoRig(t, "cur1")
	r.m.stampHook = func(string, layers.Stamps) error { return errors.New("EIO") }
	key := termKey("apps/new")
	if _, _, err := r.m.claimLayer(key); err == nil || !strings.Contains(err.Error(), "stamp its base") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Lstat(filepath.Join(r.m.Root, ".xbin", "term", key)); !os.IsNotExist(err) {
		t.Fatalf("an unstamped layer dir was left: %v", err)
	}
}

// An unstamped rootfs (a dev one) is no base to move to: a legacy layer
// stays and runs on it, auto-update on.
func TestClaimLayerUnstampedRootfs(t *testing.T) {
	r := newAutoRig(t, "")
	r.auto = true
	if err := os.Remove(filepath.Join(r.m.Rootfs, layers.VersionFile)); err != nil {
		t.Fatal(err)
	}
	lc, _, err := r.m.claimLayer(r.key)
	if err != nil || lc.moved != "" || lc.base != r.m.Rootfs || !r.kept(t) || r.stamp(t) != layers.Legacy {
		t.Fatalf("%+v %v kept %v stamp %q", lc, err, r.kept(t), r.stamp(t))
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

// A VM session's launch keeps what the setup learnt before applyVM: the
// base move sandboxShell recorded (it once replaced the whole struct, and a
// VM terminal's move — its whole disk — went unsaid).
func TestLaunchSetVMKeepsBaseMoved(t *testing.T) {
	l := &sbxLaunch{baseMoved: "old1"}
	l.setVM(2048, 2, true, "/ws/.xbin/term/apps~x/vm/disk.img")
	if l.baseMoved != "old1" || l.memMiB != 2048 || l.vcpus != 2 || !l.emulated || l.disk == "" {
		t.Fatalf("%+v", *l)
	}
	var none *sbxLaunch
	none.setVM(1, 1, false, "") // nil-safe
}

// What the tile's shells say: their own move's line; an agent session's
// move once, to the next shell; nothing after.
func TestMoveNotes(t *testing.T) {
	m := &Manager{}
	k := termKey("apps/x")
	if n := m.takeMoveNote(k, false); n != "" {
		t.Fatalf("no move: %q", n)
	}
	if n := m.takeMoveNote(k, true); n != baseMovedLine {
		t.Fatalf("own move: %q", n)
	}
	m.noteMove(k)
	if n := m.takeMoveNote(termKey("apps/y"), false); n != "" {
		t.Fatalf("another tile: %q", n)
	}
	if n := m.takeMoveNote(k, false); n != baseMovedByAgentLine {
		t.Fatalf("after an agent's move: %q", n)
	}
	if n := m.takeMoveNote(k, false); n != "" {
		t.Fatalf("told twice: %q", n)
	}
	m.noteMove(k)
	if n := m.takeMoveNote(k, true); n != baseMovedLine {
		t.Fatalf("own move after an agent's: %q", n)
	}
	if n := m.takeMoveNote(k, false); n != "" {
		t.Fatalf("the agent's told after the shell's own: %q", n)
	}
	for _, line := range []string{baseMovedLine, baseMovedByAgentLine} {
		for _, what := range []string{"$HOME", "installed packages", "/etc", "/var", "/opt", "VM terminal's whole disk"} {
			if !strings.Contains(line, what) {
				t.Errorf("the line doesn't say %q: %q", what, line)
			}
		}
	}
}

// An agent session whose start moved the layer says so where its user
// looks: a notice in its log (the Agent tab; published like every event),
// and the tile's next shell.
func TestAgentSaysBaseMoved(t *testing.T) {
	var got []SessionEvent
	m := &Manager{OnEvent: func(cwd string, ev SessionEvent) { got = append(got, ev) }}
	s := &Session{ID: "s1", Cwd: "apps/x", kind: KindAgent, hub: termwire.NewHub(0), agent: &agentState{log: agent.NewLog(0, 0)}}
	s.sayBaseMoved(m, termKey("apps/x"))
	evs, _ := s.agent.log.Since(0)
	if len(evs) != 1 || evs[0].Type != EvNotice || len(got) != 1 || got[0].Type != EvNotice {
		t.Fatalf("log %+v, published %+v", evs, got)
	}
	var d noticeData
	if err := json.Unmarshal(evs[0].Data, &d); err != nil || d.Text != baseMovedNote {
		t.Fatalf("notice %s: %v", evs[0].Data, err)
	}
	if !strings.Contains(string(s.agent.text), baseMovedNote) {
		t.Fatal("not in the host's text log")
	}
	if n := m.takeMoveNote(termKey("apps/x"), false); n != baseMovedByAgentLine {
		t.Fatalf("the next shell: %q", n)
	}
}
