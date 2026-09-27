//go:build linux

package tilesbx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// wsFakes are the Deps a WP-19 test changes while the runtime reads them:
// the tiles (exists → enabled), who holds cap:sandboxes, the resources.
type wsFakes struct {
	mu     sync.Mutex
	tiles  map[string]bool
	caps   map[string]bool
	mounts map[string]MountSource
	sealed bool
}

func newWSFakes() *wsFakes {
	return &wsFakes{
		tiles: map[string]bool{"apps/mgr": true, "apps/mgr2": true, "apps/mgr3": true},
		caps:  map[string]bool{"apps/mgr": true, "apps/mgr2": true, "apps/mgr3": true},
		mounts: map[string]MountSource{
			"apps/mgr res:apps/mgr/work": {Src: "/x/work", Role: "writer", Kind: "filesystem", Encrypted: true, Ready: true},
			"apps/mgr res:apps/mgr/ro":   {Src: "/x/ro", Role: "reader", Kind: "filesystem", Encrypted: true, Ready: true},
		},
	}
}

func (f *wsFakes) set(fn func(*wsFakes)) { f.mu.Lock(); defer f.mu.Unlock(); fn(f) }

func (f *wsFakes) Exists(t string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.tiles[t]
	return ok
}
func (f *wsFakes) Enabled(t string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tiles[t]
}
func (f *wsFakes) SandboxesFor(t string) bool { f.mu.Lock(); defer f.mu.Unlock(); return f.caps[t] }
func (f *wsFakes) Sealed() bool               { f.mu.Lock(); defer f.mu.Unlock(); return f.sealed }

func (f *wsFakes) ResourceMount(tile, res string) (MountSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ms, ok := f.mounts[tile+" "+res]; ok {
		return ms, nil
	}
	return MountSource{}, errors.New("the tile doesn't hold " + res + ": declare it in uses")
}

// deps wires them in.
func (f *wsFakes) deps(o *Options) {
	o.Deps.Tiles, o.Deps.Caps, o.Deps.Mounts, o.Deps.Vault = f, f, f, f
}

// fakeDU is the confined du: bytes by sandbox name (the state dir's
// <name>.<uid>), and the dirs it measured.
type fakeDU struct {
	mu    sync.Mutex
	bytes map[string]int64
	dirs  []string
}

func (f *fakeDU) du(_ context.Context, dir string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dirs = append(f.dirs, dir)
	name, _, _ := strings.Cut(filepath.Base(dir), ".")
	return f.bytes[name], nil
}

func (f *fakeDU) measured(dir string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.dirs {
		if d == dir {
			n++
		}
	}
	return n
}

func withDU(f *fakeDU) func(*Options) { return func(o *Options) { o.DiskUsage = f.du } }

// as is a manager principal of another tile.
func as(tile string) auth.Principal { return auth.Principal{Component: tile, Via: "instance"} }

// startAs creates and starts name as tile's manager, and waits until it runs.
func (fe *fakeEnv) startAs(tile string, body map[string]any) {
	fe.t.Helper()
	fe.want(fe.do(as(tile), "POST", "/sandboxes", body), http.StatusCreated, "")
	w := fe.do(as(tile), "POST", "/sandboxes/"+body["name"].(string)+"/start", nil)
	if fe.want(w, http.StatusOK, ""); fe.info(w).State != StateRunning {
		fe.t.Fatalf("%s: %s", body["name"], w.Body)
	}
}

// statusOf is k's sandbox's state and detail.
func (fe *fakeEnv) statusOf(k Key, name string) (string, string) {
	in, ok := fe.m.infoOf(k, name)
	if !ok {
		fe.t.Fatalf("no sandbox %s of %s", name, k.Tile)
	}
	return in.State, in.StateDetail
}

// waitStopped waits until k's sandbox stopped, with a detail holding want.
func (fe *fakeEnv) waitStopped(k Key, name, want string) {
	fe.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		st, detail := fe.statusOf(k, name)
		if st == StateStopped {
			if !strings.Contains(detail, want) {
				fe.t.Fatalf("%s of %s stopped with %q, want %q", name, k.Tile, detail, want)
			}
			return
		}
		if time.Now().After(deadline) {
			fe.t.Fatalf("%s of %s is %s, want stopped (%q)", name, k.Tile, st, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// running asserts k's sandbox is running.
func (fe *fakeEnv) running(k Key, name string) {
	fe.t.Helper()
	if st, detail := fe.statusOf(k, name); st != StateRunning {
		fe.t.Fatalf("%s of %s is %s (%s), want running", name, k.Tile, st, detail)
	}
}

var (
	kMgr  = Key{Tile: "apps/mgr"}
	kMgr2 = Key{Tile: "apps/mgr2"}
	kMgr3 = Key{Tile: "apps/mgr3"}
)

// A reconcile (a rescan, a users event) stops the sandboxes of a tile that
// vanished, was disabled, or lost cap:sandboxes by a hand edit that fired
// no hook — state kept — and leaves the others running.
func TestReconcileTiles(t *testing.T) {
	f := newWSFakes()
	fe := newFakeEnv(t, f.deps)
	fe.startAs("apps/mgr", ns("a"))
	fe.startAs("apps/mgr2", ns("b"))
	fe.m.Reconcile()
	fe.m.waitReconcile()
	fe.running(kMgr, "a")
	fe.running(kMgr2, "b")

	f.set(func(f *wsFakes) { delete(f.tiles, "apps/mgr2") }) // rm -rf apps/mgr2, then a rescan
	fe.m.Reconcile()
	fe.waitStopped(kMgr2, "b", "its tile was removed")
	fe.running(kMgr, "a")
	if st, _ := fe.statusOf(kMgr2, "b"); st != StateStopped || !exists(fe.stateDir(kMgr2, "b")) {
		t.Fatal("a removed tile's sandbox keeps its state")
	}

	f.set(func(f *wsFakes) { f.tiles["apps/mgr"] = false }) // disabled by a hand edit
	fe.m.Reconcile()
	fe.waitStopped(kMgr, "a", "its tile is disabled")

	f.set(func(f *wsFakes) { f.tiles["apps/mgr"] = true })
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	f.set(func(f *wsFakes) { f.caps["apps/mgr"] = false }) // the grant row edited out of xbin.json: no hook
	fe.m.OnUsersChange()
	fe.waitStopped(kMgr, "a", "no longer holds cap:sandboxes")
	fe.assertBookEmpty()
}

// A revoke fires StopTile, and capSweep repeats it on every users event:
// the stops are idempotent — the sandbox ends once, with the first reason.
func TestStopTileRepeated(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("a"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); fe.m.StopTile("apps/mgr", "cap:sandboxes was revoked: stopped, state kept") }()
	}
	wg.Wait()
	fe.waitStopped(kMgr, "a", "cap:sandboxes was revoked")
	fe.m.StopTile("apps/mgr", "again")
	if _, detail := fe.statusOf(kMgr, "a"); !strings.Contains(detail, "revoked") {
		t.Fatalf("a second StopTile rewrote the reason: %q", detail)
	}
	if n := fe.l.count(); n != 1 {
		t.Fatalf("%d launches", n)
	}
}

// §5: a mount the tile no longer holds, or a read-write one whose role
// dropped to reader, stops its sandbox with the mount named; a read-only
// mount's sandbox, and one whose role widened, run on.
func TestResourceReconcile(t *testing.T) {
	f := newWSFakes()
	fe := newFakeEnv(t, f.deps)
	mount := func(name, res string, ro bool) map[string]any {
		b := ns(name)
		b["mounts"] = []map[string]any{{"res": res, "at": "/mnt/m", "ro": ro}}
		return b
	}
	fe.startAs("apps/mgr", mount("rw", "res:apps/mgr/work", false))
	fe.startAs("apps/mgr", mount("rom", "res:apps/mgr/work", true))
	fe.startAs("apps/mgr", mount("rd", "res:apps/mgr/ro", false)) // a reader's: bound read-only
	fe.startAs("apps/mgr", ns("plain"))

	// writer → reader
	f.set(func(f *wsFakes) {
		ms := f.mounts["apps/mgr res:apps/mgr/work"]
		ms.Role = "reader"
		f.mounts["apps/mgr res:apps/mgr/work"] = ms
	})
	fe.m.OnResourceChange("apps/mgr")
	fe.waitStopped(kMgr, "rw", "read-write mount at /mnt/m (res:apps/mgr/work): the tile now holds it only as a reader")
	// reader → writer: widened, it waits for the next start
	f.set(func(f *wsFakes) {
		ms := f.mounts["apps/mgr res:apps/mgr/ro"]
		ms.Role = "writer"
		f.mounts["apps/mgr res:apps/mgr/ro"] = ms
	})
	fe.m.OnResourceChange("apps/mgr")
	time.Sleep(50 * time.Millisecond)
	fe.running(kMgr, "rom")
	fe.running(kMgr, "rd")

	// the resource dropped from the manifest (a rescan reconciles)
	f.set(func(f *wsFakes) { delete(f.mounts, "apps/mgr res:apps/mgr/work") })
	fe.m.Reconcile()
	fe.waitStopped(kMgr, "rom", "its mount at /mnt/m (res:apps/mgr/work) is no longer held")
	fe.m.waitReconcile()
	time.Sleep(50 * time.Millisecond)
	fe.running(kMgr, "rd")
	fe.running(kMgr, "plain")
	// its next start fails with the mount named, as before
	w := fe.do(mgr, "POST", "/sandboxes/rom/start", nil)
	fe.want(w, http.StatusBadRequest, RefInvalid)
}

// The seal's stop (the broker's StopWhere over sandboxes with a resource
// mounted): they stop, state kept, "the vault was sealed"; the others run
// on; a start answers 503 until the vault is unsealed.
func TestSealStopsMounted(t *testing.T) {
	f := newWSFakes()
	fe := newFakeEnv(t, f.deps)
	b := ns("m")
	b["mounts"] = []map[string]any{{"res": "res:apps/mgr/work", "at": "/mnt/w"}}
	fe.startAs("apps/mgr", b)
	fe.startAs("apps/mgr", ns("plain"))
	hasRes := func(_ Key, d *Def) bool {
		for _, mt := range d.Mounts {
			if mt.Res != "" {
				return true
			}
		}
		return false
	}
	fe.m.StopWhere(hasRes, "the vault was sealed: stopped, state kept — start it again once the vault is unsealed")
	// StopWhere returned: the stop is done (the seal unmounts only now)
	if st, detail := fe.statusOf(kMgr, "m"); st != StateStopped || !strings.HasPrefix(detail, "the vault was sealed") {
		t.Fatalf("after the seal's stop: %s %q", st, detail)
	}
	fe.running(kMgr, "plain")
	f.set(func(f *wsFakes) {
		f.sealed = true
		ms := f.mounts["apps/mgr res:apps/mgr/work"]
		ms.Ready = false
		f.mounts["apps/mgr res:apps/mgr/work"] = ms
	})
	w := fe.do(mgr, "POST", "/sandboxes/m/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !strings.Contains(w.Body.String(), "vault is sealed") {
		t.Fatalf("a sealed start: %s", w.Body)
	}
}

// §6.3: a stop measures the sandbox's state dir (a confined du), and a
// running one is measured by the worker; a tile measured over
// perTile.diskGiB while running has its largest running sandbox stopped.
func TestDiskCapWhileRunning(t *testing.T) {
	du := &fakeDU{bytes: map[string]int64{"s1": 700 << 20, "s2": 600 << 20}}
	fe := newFakeEnv(t, withDU(du))
	fe.putPolicy(`{"perTile":{"diskGiB":1}}`, http.StatusOK)
	fe.startAs("apps/mgr", ns("s1"))
	fe.m.waitUsage()
	if in := fe.get("s1"); in.DiskBytes != 700<<20 {
		t.Fatalf("s1 measured %d", in.DiskBytes)
	}
	fe.startAs("apps/mgr", ns("s2")) // admitted: 700 MiB are under 1 GiB
	fe.waitStopped(kMgr, "s1", "the tile's sandboxes use 1.3 GiB, over its 1 GiB (sandboxes policy: perTile.diskGiB)")
	fe.m.waitUsage()
	fe.running(kMgr, "s2") // its own stop's measurement stops nothing more
	if du.measured(fe.stateDir(kMgr, "s1")) < 2 {
		t.Fatal("s1 wasn't measured at its stop")
	}
	// and the tile's bytes hold further starts (429)
	fe.want(fe.do(mgr, "POST", "/sandboxes/s1/start", nil), http.StatusTooManyRequests, RefLimit)
}

// §6.3: the workspace disk below the reserve (the 5 s statfs watch) stops
// the running namespace sandboxes of every tile above the fair share,
// largest tile first; a tile below it runs on; starts answer 503 until
// the space is back.
func TestLowDisk(t *testing.T) {
	f := newWSFakes()
	disk := &fakeDisk{fair: 10 << 30}
	du := &fakeDU{bytes: map[string]int64{"big1": 20 << 30, "big2": 10 << 30, "mid": 20 << 30, "small": 5 << 30}}
	var low atomic.Bool
	clk := newFakeClock()
	fe := newFakeEnv(t, f.deps, withDU(du), withClock(clk), func(o *Options) {
		o.Deps.Disk = disk
		o.Statfs = func(string) (int64, int64) {
			if low.Load() {
				return 5 << 30, 100 << 30
			}
			return 50 << 30, 100 << 30
		}
	})
	fe.m.afterFunc = clk.AfterFunc
	fe.putPolicy(`{"perTile":{"diskGiB":1000},"total":{"memMiB":65536}}`, http.StatusOK)
	fe.startAs("apps/mgr", ns("big1"))
	fe.startAs("apps/mgr", ns("big2"))
	fe.startAs("apps/mgr2", ns("mid"))
	fe.startAs("apps/mgr3", ns("small"))
	fe.m.waitUsage()
	if tiles, _ := fe.m.lowDiskTiles(10 << 30); strings.Join(tiles, ",") != "apps/mgr,apps/mgr2" {
		t.Fatalf("stop order %v, want the largest tile first and none under the fair share", tiles)
	}
	clk.advance(diskEvery) // a tick, the disk fine
	fe.running(kMgr, "big1")

	low.Store(true)
	fe.want(fe.do(as("apps/mgr3"), "POST", "/sandboxes", ns("more")), http.StatusCreated, "")
	w := fe.do(as("apps/mgr3"), "POST", "/sandboxes/more/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	clk.advance(diskEvery)
	for _, s := range []struct {
		k    Key
		name string
	}{{kMgr, "big1"}, {kMgr, "big2"}, {kMgr2, "mid"}} {
		fe.waitStopped(s.k, s.name, "the workspace disk is low")
	}
	fe.running(kMgr3, "small")

	low.Store(false)
	fe.want(fe.do(mgr, "POST", "/sandboxes/big1/start", nil), http.StatusOK, "")
	fe.running(kMgr, "big1")
}

// Offload's check and a removed tile's leftovers: a sandbox holds state
// once it has a cur/ (or a snapshot); a definition that never started
// holds none. Leftovers count every definition at and under a path.
func TestStateAndLeftovers(t *testing.T) {
	du := &fakeDU{bytes: map[string]int64{"a": 3 << 30}}
	fe := newFakeEnv(t, withDU(du))
	fe.create(ns("a"))
	fe.create(ns("b"))
	if n, _ := fe.m.HasState("apps/mgr"); n != 0 {
		t.Fatalf("nothing started: %d with state", n)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/stop", nil), http.StatusOK, "")
	fe.m.waitUsage()
	if n, bytes := fe.m.HasState("apps/mgr"); n != 1 || bytes != 3<<30 {
		t.Fatalf("HasState %d, %d", n, bytes)
	}
	// a snapshot alone is state too
	must(t, os.MkdirAll(filepath.Join(fe.stateDir(kMgr, "b"), "snapshots", "s-1"), 0o700))
	if n, _ := fe.m.HasState("apps/mgr"); n != 2 {
		t.Fatalf("a snapshot: HasState %d", n)
	}
	for path, want := range map[string]int{"apps/mgr": 2, "apps": 2, "apps/mg": 0, "apps/mgr/x": 0} {
		if n, _ := fe.m.Leftovers(path); n != want {
			t.Fatalf("Leftovers(%s) = %d, want %d", path, n, want)
		}
	}
	if _, bytes := fe.m.Leftovers("apps"); bytes != 3<<30 {
		t.Fatalf("leftover bytes %d", bytes)
	}
	if u := fe.m.Usage(); u["apps/mgr"] != 3<<30 {
		t.Fatalf("Usage %v", u)
	}
}

// §9: a backup carries the definitions; a restore merges them by uid —
// the same sandbox replaced, its state kept; a name another uid holds
// skipped and reported; a definition whose state is gone back in error;
// one that never ran back stopped; a malformed one skipped.
func TestRestoreDefs(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("a"))
	fe.create(ns("b"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/stop", nil), http.StatusOK, "")
	backup := fe.m.Defs("apps/mgr")
	if len(backup) != 2 || fe.m.Defs("apps/nothing") != nil {
		t.Fatalf("Defs: %d", len(backup))
	}
	a0 := fe.get("a")
	if a0.Base.Version == "" {
		t.Fatal("a pinned no base")
	}

	// the same sandbox: replaced, state kept, stopped
	if skipped := fe.m.RestoreDefs("apps/mgr", backup); len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	a1 := fe.get("a")
	if a1.UID != a0.UID || a1.State != StateStopped || a1.Version <= a0.Version || !exists(filepath.Join(fe.stateDir(kMgr, "a"), "cur")) {
		t.Fatalf("restored over itself: %+v", a1)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/stop", nil), http.StatusOK, "")

	// delete + re-create the name: the old definition is skipped
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/a", nil), http.StatusNoContent, "")
	fe.create(ns("a"))
	a2 := fe.get("a")
	skipped := fe.m.RestoreDefs("apps/mgr", backup)
	if len(skipped) != 1 || !strings.Contains(skipped[0], "the name is taken by another sandbox (uid "+a2.UID+")") {
		t.Fatalf("skipped %v", skipped)
	}
	if fe.get("a").UID != a2.UID {
		t.Fatal("the backup displaced the live sandbox")
	}

	// a fresh workspace (the state gone): a is error, b (never ran) stopped
	fe2 := newFakeEnv(t)
	if skipped := fe2.m.RestoreDefs("apps/mgr", backup); len(skipped) != 0 {
		t.Fatalf("skipped %v", skipped)
	}
	if in := fe2.get("a"); in.State != StateError || !strings.Contains(in.StateDetail, "restored without state — reset it") {
		t.Fatalf("a without state: %+v", in)
	}
	if in := fe2.get("b"); in.State != StateStopped {
		t.Fatalf("b: %+v", in)
	}
	fe2.want(fe2.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusConflict, RefState)
	fe2.want(fe2.do(mgr, "POST", "/sandboxes/a/reset", nil), http.StatusOK, "")
	fe2.want(fe2.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")

	// malformed: an XBIN_ variable, no uid, a mount at /proc
	var d Def
	must(t, json.Unmarshal(backup[1], &d))
	bad := func(mut func(*Def)) json.RawMessage {
		c := *d.clone()
		c.Name = "bad"
		mut(&c)
		b, _ := json.Marshal(c)
		return b
	}
	skipped = newFakeEnv(t).m.RestoreDefs("apps/mgr", []json.RawMessage{
		bad(func(d *Def) { d.Defaults.Env = map[string]string{"XBIN_TOKEN": "x"} }),
		bad(func(d *Def) { d.UID = "" }),
		bad(func(d *Def) { d.Mounts = []Mount{{Source: true, At: "/proc/x"}} }),
		json.RawMessage(`{"name":`),
	})
	if len(skipped) != 4 {
		t.Fatalf("skipped %v", skipped)
	}
}

// The .trash backlog: a deleted sandbox's state waits for the confined
// remover with its bytes as last measured (the admin's health).
func TestTrashBacklog(t *testing.T) {
	du := &fakeDU{bytes: map[string]int64{"a": 2 << 30}}
	fe := newFakeEnv(t, withDU(du))
	block := make(chan struct{})
	fe.m.trash.remove = func(context.Context, string) error { <-block; return nil }
	fe.create(ns("a"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/stop", nil), http.StatusOK, "")
	fe.m.waitUsage()
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/a", nil), http.StatusNoContent, "")
	if n, bytes := fe.m.TrashBacklog(); n != 1 || bytes != 2<<30 {
		t.Fatalf("backlog %d, %d", n, bytes)
	}
	close(block)
	fe.m.trash.wait()
	if n, _ := fe.m.TrashBacklog(); n != 0 {
		t.Fatalf("backlog %d after the removal", n)
	}
}

// stateDir is k's sandbox's state dir.
func (fe *fakeEnv) stateDir(k Key, name string) string {
	fe.t.Helper()
	fe.m.mu.Lock()
	d, ok := fe.m.defs.get(k, name)
	fe.m.mu.Unlock()
	if !ok {
		fe.t.Fatalf("no sandbox %s", name)
	}
	dir, err := fe.m.StateDir(k, d)
	if err != nil {
		fe.t.Fatal(err)
	}
	return dir
}
