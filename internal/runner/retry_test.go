package runner

// A failed start that can clear by itself is tried again (retry.go): driven
// through the fake engine, with the retry timers on the seam — the test
// fires each one; nothing waits for real time.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// fakeTimer is one timer the runner set through the seam.
type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
	fired   bool
}

// fakeTimers is the seam's after: timers the test fires.
type fakeTimers struct {
	mu  sync.Mutex
	all []*fakeTimer
}

func (ft *fakeTimers) after(d time.Duration, f func()) func() bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	t := &fakeTimer{d: d, f: f}
	ft.all = append(ft.all, t)
	return func() bool {
		ft.mu.Lock()
		defer ft.mu.Unlock()
		was := !t.stopped && !t.fired
		t.stopped = true
		return was
	}
}

// live is the timers neither stopped nor fired.
func (ft *fakeTimers) live() []*fakeTimer {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	var out []*fakeTimer
	for _, t := range ft.all {
		if !t.stopped && !t.fired {
			out = append(out, t)
		}
	}
	return out
}

// fire runs timer t as its time came: the fake clock moves by its delay and
// its function runs here, so the retry's start has ended when fire returns.
func (ft *fakeTimers) fire(f *fakeEngine, t *fakeTimer) {
	ft.mu.Lock()
	t.fired = true
	ft.mu.Unlock()
	f.advance(t.d)
	t.f()
}

// retryRig is a seam runner over apps/x whose health checks answer what
// health says (nil: healthy), with the retry timers on the seam and the
// deployment's log present, as a start leaves it.
type retryRig struct {
	t      *testing.T
	r      *Runner
	f      *fakeEngine
	tp     *tape
	c      *registry.Component
	timers *fakeTimers
	mu     sync.Mutex
	health []error // the next health answers, in order; empty: healthy
	starts int
	log    string
}

func newRetryRig(t *testing.T, health ...error) *retryRig {
	t.Helper()
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, tp := newSeamRunner(t, c)
	rg := &retryRig{t: t, r: r, f: f, tp: tp, c: c, timers: &fakeTimers{}, health: health}
	rg.log = filepath.Join(r.Root, filepath.FromSlash(deploymentLog(c.Path, "main")))
	if err := os.MkdirAll(filepath.Dir(rg.log), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rg.log, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	rg.wire(r)
	return rg
}

// wire puts the rig's health and timers on runner r's engine.
func (rg *retryRig) wire(r *Runner) {
	r.engine.after = rg.timers.after
	r.engine.healthy = func(c *registry.Component, inst *instance) error {
		rg.mu.Lock()
		rg.starts++
		var err error
		if len(rg.health) > 0 {
			err, rg.health = rg.health[0], rg.health[1:]
		}
		rg.mu.Unlock()
		if err != nil {
			return err
		}
		return rg.f.healthy(c, inst)
	}
}

func (rg *retryRig) ensure() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := rg.r.Ensure(ctx, rg.c)
	return err
}

// pending is the one live retry timer, with the delay it must have.
func (rg *retryRig) pending(d time.Duration) *fakeTimer {
	rg.t.Helper()
	live := rg.timers.live()
	if len(live) != 1 || live[0].d != d {
		ds := []time.Duration{}
		for _, t := range live {
			ds = append(ds, t.d)
		}
		rg.t.Fatalf("live retry timers %v, want one of %v", ds, d)
	}
	return live[0]
}

func (rg *retryRig) noPending() {
	rg.t.Helper()
	if live := rg.timers.live(); len(live) != 0 {
		rg.t.Fatalf("%d retry timer(s) still live", len(live))
	}
}

func (rg *retryRig) startCount() int {
	rg.mu.Lock()
	defer rg.mu.Unlock()
	return rg.starts
}

// row is apps/x's Status() row.
func (rg *retryRig) row() map[string]any {
	e, _ := rg.r.Status()["apps/x"].(map[string]any)
	return e
}

func (rg *retryRig) retryOf() *StartRetry {
	rt, _ := rg.row()["retry"].(*StartRetry)
	return rt
}

func (rg *retryRig) logText() string {
	b, _ := os.ReadFile(rg.log)
	return string(b)
}

// A backend whose first start is slow — it is still in startup when the
// deadline passes — becomes healthy by itself, with no save: the retry is
// a new generation of the same code, 10 s later. The first start blocks on
// a channel the test closes, as a backend waiting on a slow disk would.
func TestStartRetrySlowOnceThenHealthy(t *testing.T) {
	rg := newRetryRig(t)
	slow := make(chan struct{})
	first := true
	inner := rg.r.engine.healthy
	rg.r.engine.healthy = func(c *registry.Component, inst *instance) error {
		if first {
			first = false
			<-slow // the deadline passes while it is still starting
			return errNeverListened
		}
		return inner(c, inst)
	}
	done := make(chan error, 1)
	go func() { done <- rg.ensure() }()
	close(slow)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "never started listening") {
		t.Fatalf("the slow start: %v", err)
	}
	settle(t, rg.r, rg.f)

	row := rg.row()
	rt := rg.retryOf()
	if row["state"] != "failed" || rt == nil || rt.Attempt != 1 || rt.Of != 3 || rt.InSec != 10 ||
		rt.At != seamEpoch.Add(10*time.Second).Format(time.RFC3339) {
		t.Fatalf("waiting: row %v retry %+v", row, rt)
	}
	if b := rg.r.Inspect(); len(b) != 1 || b[0].State != "failed" || b[0].Retry == nil || b[0].Retry.Attempt != 1 {
		t.Fatalf("inspect while waiting: %+v", b)
	}
	if !strings.Contains(rg.logText(), "--- the start failed; retrying in 10s (1 of 3) ") {
		t.Fatalf("the log: %q", rg.logText())
	}

	rg.timers.fire(rg.f, rg.pending(10*time.Second))
	settle(t, rg.r, rg.f)
	if row := rg.row(); row["state"] != "healthy" || row["gen"] != 2 || row["retry"] != nil {
		t.Fatalf("after the retry: %v", row)
	}
	rg.noPending()
	if got, want := rg.f.takeLog(), []string{bld, st(1), sp(1), bld, st(2)}; !equalStrings(got, want) {
		t.Fatalf("fake log %q, want %q", got, want)
	}
	tape := rg.tp.take()
	if len(tape) != 5 || tape[0] != bs || !strings.HasPrefix(tape[1], "build-error apps/x: backend did not become healthy") ||
		tape[2] != "backend-retry apps/x" || tape[3] != bs || tape[4] != bo {
		t.Fatalf("tape %q", tape)
	}
}

// A start that keeps failing is retried three times — 10 s, 1 min, 5 min —
// and then stays failed with its last reason, the row without a retry.
func TestStartRetryStopsAfterTheCap(t *testing.T) {
	rg := newRetryRig(t, errNeverListened, errNeverListened, errNeverListened, errNeverListened, errNeverListened)
	if err := rg.ensure(); err == nil {
		t.Fatal("the first start passed")
	}
	for i, d := range []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute} {
		if rt := rg.retryOf(); rt == nil || rt.Attempt != i+1 {
			t.Fatalf("before retry %d: %+v", i+1, rt)
		}
		rg.timers.fire(rg.f, rg.pending(d))
		settle(t, rg.r, rg.f)
	}
	rg.noPending()
	if row := rg.row(); row["state"] != "failed" || row["retry"] != nil ||
		!strings.Contains(fmt.Sprint(row["error"]), "never started listening") {
		t.Fatalf("after the cap: %v", row)
	}
	if n := rg.startCount(); n != 4 {
		t.Fatalf("%d starts, want 4", n)
	}
	if err := rg.ensure(); err == nil || rg.startCount() != 4 {
		t.Fatalf("a request after the cap: %v, %d starts", err, rg.startCount())
	}
	if !strings.Contains(rg.logText(), "--- the start failed again after 3 retries: it stays failed until a save, a restart or a deploy ") {
		t.Fatalf("the log: %q", rg.logText())
	}
}

// A save while a retry is pending cancels it: the save's start is the one
// start, and a retry that fires anyway starts nothing.
func TestStartRetrySaveCancels(t *testing.T) {
	rg := newRetryRig(t, errNeverListened)
	_ = rg.ensure()
	tm := rg.pending(10 * time.Second)
	rg.r.Changed(rg.c)
	settle(t, rg.r, rg.f)
	if row := rg.row(); row["state"] != "healthy" || row["retry"] != nil {
		t.Fatalf("after the save: %v", row)
	}
	rg.noPending()
	tm.f() // a timer that fires anyway (it lost the race with the save)
	settle(t, rg.r, rg.f)
	if n := rg.startCount(); n != 2 {
		t.Fatalf("%d starts, want 2 (the failed one and the save's)", n)
	}
}

// A restart (a deploy's path) while a retry is pending cancels it, as a save does.
func TestStartRetryRestartCancels(t *testing.T) {
	rg := newRetryRig(t, errNeverListened)
	_ = rg.ensure()
	tm := rg.pending(10 * time.Second)
	if err := rg.r.Restart(context.Background(), rg.c, "main", nil); err != nil {
		t.Fatal(err)
	}
	settle(t, rg.r, rg.f)
	rg.noPending()
	tm.f()
	settle(t, rg.r, rg.f)
	if n := rg.startCount(); n != 2 {
		t.Fatalf("%d starts, want 2", n)
	}
}

// A stop by a person, a hold, and xbind's shutdown cancel a pending retry.
func TestStartRetryStopHoldShutdownCancel(t *testing.T) {
	for _, how := range []string{"stop", "hold", "shutdown"} {
		t.Run(how, func(t *testing.T) {
			rg := newRetryRig(t, errNeverListened)
			_ = rg.ensure()
			tm := rg.pending(10 * time.Second)
			switch how {
			case "stop":
				rg.r.Stop(rg.c.Path)
				rg.noPending()
			case "hold": // ShouldRun turns false; the timer finds it so
				rg.f.seal(rg.c.Path, true)
			case "shutdown":
				rg.r.StopAll()
				rg.noPending()
			}
			if how != "hold" {
				tm.f() // fires anyway
			} else {
				rg.timers.fire(rg.f, tm)
			}
			settle(t, rg.r, rg.f)
			if n := rg.startCount(); n != 1 {
				t.Fatalf("%d starts, want 1", n)
			}
			if rg.retryOf() != nil {
				t.Fatalf("a retry is still shown: %+v", rg.retryOf())
			}
		})
	}
}

// A compile error is never retried.
func TestStartRetryNeverForABuildError(t *testing.T) {
	rg := newRetryRig(t)
	rg.f.failNext("build", rg.c.Path, "main", 1)
	if err := rg.ensure(); err == nil {
		t.Fatal("the build passed")
	}
	rg.noPending()
	if row := rg.row(); row["state"] != "failed" || row["retry"] != nil {
		t.Fatalf("%v", row)
	}
}

// A backend that exits during its start is retried once; exiting again is
// a crash loop: no more retries.
func TestStartRetryExitedOnce(t *testing.T) {
	rg := newRetryRig(t, errExited, errExited)
	_ = rg.ensure()
	rg.timers.fire(rg.f, rg.pending(10*time.Second))
	settle(t, rg.r, rg.f)
	rg.noPending()
	if row := rg.row(); row["state"] != "failed" || row["retry"] != nil || rg.startCount() != 2 {
		t.Fatalf("%v after %d starts", row, rg.startCount())
	}
}

// An on-demand tile reaped while idle starts on its next request as before;
// when that start fails it is retried by the timer, with no request.
func TestStartRetryOnDemandTile(t *testing.T) {
	rg := newRetryRig(t)
	if err := rg.ensure(); err != nil {
		t.Fatal(err)
	}
	rg.f.advance(31 * time.Minute)
	rg.r.reapOnce()
	settle(t, rg.r, rg.f)
	rg.mu.Lock()
	rg.health = []error{errNeverListened}
	rg.mu.Unlock()
	if err := rg.ensure(); err == nil {
		t.Fatal("the on-demand start passed")
	}
	rg.timers.fire(rg.f, rg.pending(10*time.Second))
	settle(t, rg.r, rg.f)
	if row := rg.row(); row["state"] != "healthy" || row["gen"] != 3 {
		t.Fatalf("%v", row)
	}
}

// Which failures retry (docs/elements.md §Runtimes & backend lifecycle has
// the table).
func TestRetryableFailures(t *testing.T) {
	wrapped := func(err error) error { return fmt.Errorf("backend did not become healthy: %w", err) }
	for _, c := range []struct {
		name   string
		err    error
		exited bool
		want   bool
	}{
		{"never listened", wrapped(fmt.Errorf("%w within 5s: it is waiting on xbind: PUT /api/xbin/cron/jobs", errNeverListened)), false, true},
		{"listens, never accepted", wrapped(errNeverAccepted), false, true},
		{"exited during start", wrapped(errExited), false, true},
		{"exited again", wrapped(errExited), true, false},
		{"fork: resource temporarily unavailable", fmt.Errorf("start backend: %w", &os.SyscallError{Syscall: "fork", Err: syscall.EAGAIN}), false, true},
		{"text file busy", fmt.Errorf("start backend: %w", &os.PathError{Op: "fork/exec", Path: "bin", Err: syscall.ETXTBSY}), false, true},
		{"too many open files", fmt.Errorf("sandbox: %w", syscall.EMFILE), false, true},
		{"compile error", &BuildError{Output: "x.go:1: syntax error"}, false, false},
		{"no such binary", fmt.Errorf("start backend: %w", &os.PathError{Op: "fork/exec", Path: "bin", Err: syscall.ENOENT}), false, false},
		{"manifest", errors.New("component apps/x: runtime \"cgi\" was removed"), false, false},
		{"held", errors.New("component apps/x is not enabled"), false, false},
		{"shutting down", errStopping, false, false},
		{"another failure", errors.New("fake: never answered"), false, false},
	} {
		if got := retryable(c.err, c.exited); got != c.want {
			t.Errorf("%s: retryable %v, want %v", c.name, got, c.want)
		}
	}
}
