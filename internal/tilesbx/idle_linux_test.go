//go:build linux

package tilesbx

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// newIdleEnv is a fake-launcher runtime on a fake clock.
func newIdleEnv(t *testing.T) (*fakeEnv, *fakeClock) {
	clk := newFakeClock()
	fe := newFakeEnv(t, withClock(clk))
	fe.m.afterFunc = clk.AfterFunc
	return fe, clk
}

// The idle timer re-arms for what remains after activity, and stops a
// quiet sandbox once its idleStopMin has passed — state kept.
func TestIdleStop(t *testing.T) {
	fe, clk := newIdleEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	clk.advance(20 * time.Minute)
	fe.do(mgr, "GET", "/sandboxes/sb-1/execs", nil) // an API call on it: activity
	clk.advance(20 * time.Minute)                   // 40 min since the start, 20 since the call
	if in := fe.get("sb-1"); in.State != StateRunning || in.LastActive != clk.Now().Add(-20*time.Minute).UnixMilli() {
		t.Fatalf("stopped though active 20 min ago: %+v", in)
	}
	// reading its record isn't activity
	fe.do(mgr, "GET", "/sandboxes/sb-1", nil)
	clk.advance(10 * time.Minute)
	in := fe.waitState("sb-1", StateStopped)
	if in.StateDetail != "idle for 30 minutes: stopped, state kept (idleStopMin)" {
		t.Fatalf("stateDetail %q", in.StateDetail)
	}
	fe.assertBookEmpty()
	if clk.armed() != 0 {
		t.Fatalf("%d timers left armed", clk.armed())
	}
	// its own idleStopMin, and a PATCH applies to it running
	fe.want(fe.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"idleStopMin": 60}), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	clk.advance(45 * time.Minute)
	if in := fe.get("sb-1"); in.State != StateRunning {
		t.Fatalf("idleStopMin 60 stopped after 45: %+v", in)
	}
	fe.want(fe.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"idleStopMin": 10}), http.StatusOK, "")
	clk.advance(time.Second)
	fe.waitState("sb-1", StateStopped)
}

// Work in flight holds the idle stop off, however long it runs: a non-tty
// exec, a run, a tar stream, an attached TTY client. A detached TTY exec
// with no traffic holds nothing.
func TestIdleHolds(t *testing.T) {
	fe, clk := newIdleEnv(t)
	fe.create(ns("sb-1"))
	for _, what := range []string{"a non-tty exec", "a run longer than idleStopMin", "a tar stream", "an attached TTY client"} {
		t.Run(what, func(t *testing.T) {
			fe.t = t
			fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
			r := fe.runOf("sb-1")
			var release func()
			if what == "an attached TTY client" {
				clients := fe.m.ttyClients(r)
				clients(1)
				release = func() { clients(0) }
			} else {
				rel, ok := fe.m.hold(r)
				if !ok {
					t.Fatal("no hold on a running sandbox")
				}
				release = rel
			}
			clk.advance(3 * time.Hour)
			if in := fe.get("sb-1"); in.State != StateRunning {
				t.Fatalf("stopped under %s: %+v", what, in)
			}
			release()
			release() // idempotent
			clk.advance(29 * time.Minute)
			if in := fe.get("sb-1"); in.State != StateRunning {
				t.Fatalf("stopped before idleStopMin after %s ended: %+v", what, in)
			}
			clk.advance(time.Minute)
			fe.waitState("sb-1", StateStopped)
		})
	}
	t.Run("a detached TTY exec", func(t *testing.T) {
		fe.t = t
		fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
		clients := fe.m.ttyClients(fe.runOf("sb-1"))
		clients(1)
		clients(2)
		clients(0) // detached: its exec runs on, quiet
		clk.advance(30 * time.Minute)
		fe.waitState("sb-1", StateStopped)
	})
	fe.t = t
	// a hold on a sandbox that isn't running is refused
	if _, ok := fe.m.hold(&run{b: newBox()}); ok {
		t.Fatal("a hold on a run that isn't up")
	}
}

// acquire (an exec's or a file operation's way in) starts a stopped
// sandbox with autoStart and waits for it; waits out a stop, then starts
// it again; waits for a start; and answers 409 state without autoStart,
// for error, and when the wait runs out.
func TestAutoStart(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	r, release, err := fe.m.acquire(fe.k, "sb-1", 10*time.Second)
	if err != nil || r == nil || fe.get("sb-1").State != StateRunning {
		t.Fatalf("an exec on a stopped sandbox: %v %+v", err, fe.get("sb-1"))
	}
	if out, ex := execRun(t, r, []string{"echo", "hi"}); out != "hi\n" || ex.Code != 0 {
		t.Fatalf("exec: %q %+v", out, ex)
	}
	release()

	// while stopping: it waits for the stop, then starts it again
	b := fe.m.live[fe.k]["sb-1"]
	b.flight.Lock()
	fe.m.mu.Lock()
	b.state = StateStopping
	fe.m.mu.Unlock()
	type got struct {
		r   *run
		err error
	}
	ch := make(chan got, 1)
	go func() {
		r, release, err := fe.m.acquire(fe.k, "sb-1", 10*time.Second)
		if release != nil {
			release()
		}
		ch <- got{r, err}
	}()
	select {
	case g := <-ch:
		t.Fatalf("it didn't wait for the stop: %+v", g)
	case <-time.After(200 * time.Millisecond):
	}
	if err := fe.m.stopLocked(b, ""); err != nil {
		t.Fatal(err)
	}
	b.flight.Unlock()
	g := <-ch
	if g.err != nil || g.r == nil || g.r == r || fe.l.count() != 2 {
		t.Fatalf("after the stop: %+v, %d launches", g, fe.l.count())
	}

	// the wait runs out: 409 state, the state named
	b.flight.Lock()
	fe.m.mu.Lock()
	b.state = StateStopping
	fe.m.mu.Unlock()
	_, _, err = fe.m.acquire(fe.k, "sb-1", 200*time.Millisecond)
	var e *Error
	if !errors.As(err, &e) || e.Refusal != RefState || e.State != StateStopping {
		t.Fatalf("a wait that ran out: %v", err)
	}
	_ = fe.m.stopLocked(b, "")
	b.flight.Unlock()

	// without autoStart: 409, stopped
	fe.want(fe.do(mgr, "PATCH", "/sandboxes/sb-1", map[string]any{"autoStart": false}), http.StatusOK, "")
	if _, _, err = fe.m.acquire(fe.k, "sb-1", time.Second); !errors.As(err, &e) || e.State != StateStopped {
		t.Fatalf("without autoStart: %v", err)
	}
	// error: 409, error
	fe.m.mu.Lock()
	b.state, b.detail = StateError, "its state is missing"
	fe.m.mu.Unlock()
	if _, _, err = fe.m.acquire(fe.k, "sb-1", time.Second); !errors.As(err, &e) || e.State != StateError {
		t.Fatalf("in error: %v", err)
	}
	if _, _, err = fe.m.acquire(fe.k, "sb-9", time.Second); !errors.As(err, &e) || e.Refusal != RefNotFound {
		t.Fatalf("no such sandbox: %v", err)
	}
}

// ?wait bounds how long a lifecycle call waits: absent is waitMaxSec, 0
// answers the sandbox as it stands, and the transition goes on after it.
func TestLifecycleWait(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start?wait=x", nil), http.StatusBadRequest, RefInvalid)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start?wait=-1", nil), http.StatusBadRequest, RefInvalid)
	b, _ := fe.m.boxFor(fe.k, "sb-1")
	b.flight.Lock() // a transition in the flight
	start := time.Now()
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start?wait=0", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || time.Since(start) > 2*time.Second {
		t.Fatalf("?wait=0 answered %+v after %s", in, time.Since(start))
	}
	b.flight.Unlock() // the start goes on
	fe.waitState("sb-1", StateRunning)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop?wait=999", nil), http.StatusOK, "") // clamped
	if in := fe.get("sb-1"); in.State != StateStopped {
		t.Fatalf("after a waited stop: %+v", in)
	}
}

// A start while an earlier run (an orphan of a crashed xbind) still holds
// the sandbox's lock waits for it, then fails with 409 state; one that
// lets go within the wait lets the start through.
func TestOrphanLock(t *testing.T) {
	fe := newFakeEnv(t)
	fe.m.lockWait = 400 * time.Millisecond
	fe.create(ns("sb-1"))
	d, _ := fe.m.defs.get(fe.k, "sb-1")
	dir, _ := fe.m.StateDir(fe.k, d)
	orphan, err := lockState(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusConflict, RefState)
	if waited := time.Since(start); waited < 400*time.Millisecond || !strings.Contains(w.Body.String(), "still ending") {
		t.Fatalf("after %s: %s", waited, w.Body)
	}
	if in := fe.get("sb-1"); in.State != StateStopped {
		t.Fatalf("after a refused start: %+v", in)
	}
	fe.assertBookEmpty()
	go func() { time.Sleep(150 * time.Millisecond); orphan.Close() }()
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)
}

// stateOf is the sandbox's state dir, cur/ and a snapshot dir in it.
func (fe *fakeEnv) stateOf(name string) (dir, cur, snap string) {
	d, _ := fe.m.defs.get(fe.k, name)
	dir, _ = fe.m.StateDir(fe.k, d)
	return dir, filepath.Join(dir, "cur"), filepath.Join(dir, "snapshots", "s-1")
}

// Reset of a running sandbox leaves it running on a fresh cur/ pinned to
// the current base, its old state in .trash for the confined remover and
// its snapshots untouched; rebase keeps the state and re-pins it. Both
// repair error; a stopped sandbox stays stopped.
func TestResetRebase(t *testing.T) {
	fe := newFakeEnv(t)
	var removed []string
	fe.m.trash.remove = func(_ context.Context, dir string) error {
		removed = append(removed, dir)
		return os.RemoveAll(dir)
	}
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	_, cur, snap := fe.stateOf("sb-1")
	must(t, os.WriteFile(filepath.Join(cur, "upper", "kept"), []byte("x"), 0o644))
	must(t, os.MkdirAll(snap, 0o700))
	must(t, os.WriteFile(filepath.Join(snap, "meta.json"), []byte("{}"), 0o600))
	old := fe.runOf("sb-1")

	w := fe.do(mgr, "POST", "/sandboxes/sb-1/reset", nil)
	fe.want(w, http.StatusOK, "")
	in := fe.info(w)
	if in.State != StateRunning || in.Base.Version != "b-test" || fe.runOf("sb-1") == old {
		t.Fatalf("after a reset: %+v", in)
	}
	select {
	case <-old.done: // the old run is gone; one book and one row: the new run's
	default:
		t.Fatal("the old run wasn't torn down")
	}
	if run, _, _ := fe.m.booked(fe.k.Tile); run != 1 || fe.held() != 1 || len(fe.sbx.List(sbx.Filter{Kind: sbx.Tile})) != 1 {
		t.Fatalf("after a reset: %d booked, %d held, rows %+v", run, fe.held(), fe.sbx.List(sbx.Filter{Kind: sbx.Tile}))
	}
	if _, err := os.Stat(filepath.Join(cur, "upper", "kept")); !os.IsNotExist(err) {
		t.Fatalf("the upper survived a reset: %v", err)
	}
	if st, _ := layers.Read(cur); st.Base != "b-test" {
		t.Fatalf("the fresh cur's stamps: %+v", st)
	}
	fe.m.trash.wait()
	if len(removed) != 1 || !strings.Contains(removed[0], "/.trash/") {
		t.Fatalf("the old state went to %v", removed)
	}
	if _, err := os.Stat(filepath.Join(snap, "meta.json")); err != nil {
		t.Fatalf("its snapshot: %v", err)
	}

	// rebase: the base moved on; the state is kept and re-pinned, running again
	must(t, os.WriteFile(filepath.Join(cur, "upper", "kept"), []byte("x"), 0o644))
	must(t, os.WriteFile(filepath.Join(fe.m.rootfs, layers.VersionFile), []byte("b-new\n"), 0o644))
	old = fe.runOf("sb-1")
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/rebase?wait=30", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateRunning || in.Base.Version != "b-new" || fe.runOf("sb-1") == old {
		t.Fatalf("after a rebase: %+v", in)
	}
	if _, err := os.Stat(filepath.Join(cur, "upper", "kept")); err != nil {
		t.Fatalf("a rebase lost the upper: %v", err)
	}
	if st, _ := layers.Read(cur); st.Base != "b-new" {
		t.Fatalf("the re-pinned stamps: %+v", st)
	}
	if d, _ := fe.m.defs.get(fe.k, "sb-1"); d.Base != "b-new" {
		t.Fatalf("Def.base: %q", d.Base)
	}
	if _, err := os.Stat(filepath.Join(snap, "meta.json")); err != nil {
		t.Fatalf("its snapshot: %v", err)
	}

	// a stopped sandbox stays stopped; error is repaired
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	must(t, os.WriteFile(filepath.Join(fe.m.rootfs, layers.VersionFile), []byte("b-newer\n"), 0o644))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusConflict, RefState) // b-new isn't installed
	if in := fe.get("sb-1"); in.State != StateError {
		t.Fatalf("a missing base: %+v", in)
	}
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/rebase", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.StateDetail != "" || in.Base.Version != "b-newer" {
		t.Fatalf("a rebase of an error: %+v", in)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)

	// a missing state: rebase can't repair it, reset can
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil), http.StatusOK, "")
	must(t, os.Rename(cur, cur+".gone"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusConflict, RefState)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/rebase", nil), http.StatusConflict, RefState)
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/reset", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.Base.Version != "" {
		t.Fatalf("a reset of an error: %+v", in)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/reset", nil), http.StatusOK, "") // idempotent
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	if in := fe.waitState("sb-1", StateRunning); in.Base.Version != "b-newer" {
		t.Fatalf("started after a reset: %+v", in)
	}
	// a sandbox that never ran resets and rebases to nothing
	fe.create(ns("sb-2"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/reset", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/rebase", nil), http.StatusOK, "")
	if dir, _, _ := fe.stateOf("sb-2"); exists(dir) {
		t.Fatal("a reset made state for a sandbox that never ran")
	}
}

// An exec id of another boot answers 410 lost — on every exec route, and
// across a restart of the runtime.
func TestExecLost(t *testing.T) {
	root := t.TempDir()
	e := newEnv(t, func(o *Options) { o.Root = root })
	e.create(ns("sb-1"))
	other := "abcdef"
	if other == e.m.bootID {
		other = "fedcba"
	}
	for _, p := range []string{"", "/output", "/tty"} {
		e.want(e.do(mgr, "GET", "/sandboxes/sb-1/execs/"+other+"-1"+p, nil), http.StatusGone, RefLost)
	}
	e.want(e.do(mgr, "POST", "/sandboxes/sb-1/execs/"+other+"-1/signal", "{}"), http.StatusGone, RefLost)
	first := e.m.bootID + "-7"
	e2 := newEnv(t, func(o *Options) { o.Root = root }) // xbind restarted
	if e2.m.bootID == e.m.bootID {
		t.Skip("the two boots drew the same id")
	}
	e2.want(e2.do(mgr, "GET", "/sandboxes/sb-1/execs/"+first, nil), http.StatusGone, RefLost)
}

// A users event resolves the running sandboxes' egress again: a class
// that narrowed (a D20 policy row, which fires no sandbox-net hook) stops.
func TestUsersEventReconcile(t *testing.T) {
	net := fakeNet{"apps/mgr": {{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet",
		Rules: []string{"net:1.1.1.0/24"}}}}
	fe := newFakeEnv(t, func(o *Options) { o.Deps.Net = net })
	fe.create(map[string]any{"name": "sb-1", "mode": "namespace", "net": map[string]any{"egress": "class:internet"}})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.m.OnUsersChange() // nothing changed: it runs on
	for busy := true; busy; time.Sleep(5 * time.Millisecond) {
		fe.m.recon.mu.Lock()
		busy = fe.m.recon.running
		fe.m.recon.mu.Unlock()
	}
	if in := fe.get("sb-1"); in.State != StateRunning {
		t.Fatalf("an unchanged class: %+v", in)
	}
	net["apps/mgr"][0].Rules = []string{"net:8.8.8.8"}
	fe.m.OnUsersChange()
	fe.m.OnUsersChange() // coalesced
	if in := fe.waitState("sb-1", StateStopped); !strings.Contains(in.StateDetail, "narrowed") {
		t.Fatalf("narrowed: %+v", in)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return !errors.Is(err, syscall.ENOENT)
}
