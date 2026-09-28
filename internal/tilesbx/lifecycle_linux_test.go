//go:build linux

package tilesbx

import (
	"errors"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/layers"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// A start brings the sandbox up through the whole path — the book, the
// lock, the pinned base, the spec, the relay on its TUN, the agent's
// "ready", the registry row — and a stop takes all of it down again.
func TestStartStop(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusOK, "")
	in := fe.info(w)
	if in.State != StateRunning || in.Started == 0 || in.Net.Reach != "none" || in.StateDetail != "" {
		t.Fatalf("started: %+v", in)
	}
	p := fe.l.last()
	sp := p.spec
	if !sp.Restricted || !sp.MountGuard || !sp.NoFollow || !sp.FuseWatch || sp.Net != "relay" || sp.Hostname != "sb-1" ||
		sp.Entry != agentPath || strings.Join(sp.Argv, " ") != "bx __sbx-agent" || sp.Agent == nil || sp.Lock == nil {
		t.Fatalf("the spec: %+v", sp)
	}
	d, _ := fe.m.defs.get(fe.k, "sb-1")
	cur, _ := fe.m.CurDir(fe.k, d)
	if sp.Upper != filepath.Join(cur, "upper") || sp.Work != filepath.Join(cur, "work") || len(sp.Lower) != 1 {
		t.Fatalf("layers: %+v", sp)
	}
	if st, _ := layers.Read(cur); st.Base != "b-test" || d.Base != "b-test" || in.Base.Version != "b-test" {
		t.Fatalf("the pin: stamp %+v, def %q, info %+v", st, d.Base, in.Base)
	}
	if !p.started {
		t.Fatal("xbind's copies of the child-side files weren't closed")
	}
	rows := fe.sbx.List(sbx.Filter{Kind: sbx.Tile})
	if len(rows) != 1 || rows[0].ID != "tile:"+fe.k.CK()+":sb-1" || rows[0].Name != "sb-1" || rows[0].Mode != sbx.Namespace || rows[0].Net != "none" {
		t.Fatalf("registry: %+v", rows)
	}
	if fe.held() != 1 {
		t.Fatalf("books held: %d", fe.held())
	}
	// the minimal exec path
	r := fe.runOf("sb-1")
	if out, ex := execRun(t, r, []string{"sh", "-c", "echo hi; exit 7"}); out != "hi\n" || ex.Code != 7 {
		t.Fatalf("exec: %q %+v", out, ex)
	}
	// a second start of a running sandbox leaves it alone
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	if len(fe.l.procs) != 1 {
		t.Fatalf("a second start launched again: %d", len(fe.l.procs))
	}

	w = fe.do(mgr, "POST", "/sandboxes/sb-1/stop", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.StateDetail != "" || in.Started != 0 {
		t.Fatalf("stopped: %+v", in)
	}
	assertTornDown(t, fe, r)
	// the lock is free again
	lock, err := lockState(filepath.Dir(cur))
	if err != nil {
		t.Fatalf("the lock after the stop: %v", err)
	}
	lock.Close()
	// it starts again on the same state
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)
	if fe.l.last().spec.Upper != sp.Upper {
		t.Fatal("a restart got another upper")
	}
}

// assertTornDown checks nothing of r is left: its book, registry row,
// relay flows, TUN, factory and control connection.
func assertTornDown(t *testing.T, fe *fakeEnv, r *run) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(10 * time.Second):
		t.Fatal("no teardown")
	}
	if fe.held() != 0 {
		t.Errorf("books held after the teardown: %d", fe.held())
	}
	if rows := fe.sbx.List(sbx.Filter{Kind: sbx.Tile}); len(rows) != 0 {
		t.Errorf("registry rows after the teardown: %+v", rows)
	}
	if used, _ := fe.m.FlowBudget(); used != 0 {
		t.Errorf("flows held after the teardown: %d", used)
	}
	if r.tunFD != -1 {
		t.Errorf("the TUN fd %d wasn't closed", r.tunFD)
	}
	if _, err := r.fac.Dial(); err == nil {
		t.Error("the factory is still open")
	}
	select {
	case <-r.client().gone:
	case <-time.After(5 * time.Second):
		t.Error("the control connection's reader is still running")
	}
	p := r.proc.(*fakeProc)
	p.mu.Lock()
	cleaned := p.cleaned
	p.mu.Unlock()
	if !cleaned {
		t.Error("the launch wasn't cleaned up")
	}
}

// A start that fails unwinds everything it did — the book included — and
// leaves the sandbox stopped, why in stateDetail, recorded as a failure.
func TestFailedStartUnwinds(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.l.fail = errors.New("no namespaces here")
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || !strings.Contains(in.StateDetail, "no namespaces here") {
		t.Fatalf("after a failed launch: %+v", in)
	}
	if fe.held() != 0 || fe.books != 1 {
		t.Fatalf("books: held %d, taken %d", fe.held(), fe.books)
	}
	d, _ := fe.m.defs.get(fe.k, "sb-1")
	dir, _ := fe.m.StateDir(fe.k, d)
	lock, err := lockState(dir)
	if err != nil {
		t.Fatalf("the lock after a failed launch: %v", err)
	}
	lock.Close()
	if f := fe.sbx.Failures(sbx.Filter{Kind: sbx.Tile}); len(f) != 1 || f[0].Stage != sbx.Start || !strings.Contains(f[0].Error, "no namespaces") {
		t.Fatalf("failures: %+v", f)
	}

	// An agent that never answers "ready": the run is ended, and torn down.
	fe.l.fail = nil
	fe.l.mod = func(o *agentcore.Options) { o.Configure = func(proto.Config) error { return nil } } // waits for a config
	nsOpsReady := nsOps.readyWait
	nsOps.readyWait = func(*run) time.Duration { return 300 * time.Millisecond }
	defer func() { nsOps.readyWait = nsOpsReady }()
	w = fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || !strings.Contains(in.StateDetail, "wasn't ready within") {
		t.Fatalf("after a start that timed out: %+v", in)
	}
	p := fe.l.last()
	<-p.dead
	if fe.held() != 0 || fe.books != 2 {
		t.Fatalf("books: held %d, taken %d", fe.held(), fe.books)
	}
	if rows := fe.sbx.List(sbx.Filter{Kind: sbx.Tile}); len(rows) != 0 {
		t.Fatalf("a sandbox that never ran is listed: %+v", rows)
	}

	// An agent that exits before it is ready: its exit and its log say why.
	fe.l.mod = func(o *agentcore.Options) { o.Configure = func(proto.Config) error { return nil } }
	nsOps.readyWait = func(*run) time.Duration { return 10 * time.Second }
	done, n := make(chan Info), fe.l.count()
	go func() {
		w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
		done <- fe.info(w)
	}()
	for fe.l.count() == n {
		select {
		case in := <-done:
			t.Fatalf("the start didn't launch: %+v", in)
		case <-time.After(5 * time.Millisecond):
		}
	}
	p = fe.l.last()
	p.log.Write([]byte("sandbox-init: mount the root: permission denied\n"))
	p.die(ExitStatus{Code: 127})
	in := <-done
	if in.State != StateStopped || !strings.Contains(in.StateDetail, "code 127") || !strings.Contains(in.StateDetail, "permission denied") {
		t.Fatalf("an agent that died starting: %+v", in)
	}
	if fe.held() != 0 {
		t.Fatalf("books held: %d", fe.held())
	}
}

// Every way a run ends goes through one teardown, and says why.
func TestTeardownReasons(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	for _, c := range []struct {
		name string
		kill func(p *fakeProc, r *run)
		want string
	}{
		{"agent exit", func(p *fakeProc, _ *run) { p.die(ExitStatus{Code: 2}) }, "the sandbox's agent exited (code 2)"},
		{"root gone", func(p *fakeProc, _ *run) { p.die(ExitStatus{Code: agentcore.ExitRootGone}) }, "the sandbox's root filesystem (fuse-overlayfs) died"},
		{"killed", func(p *fakeProc, _ *run) { p.die(ExitStatus{Code: -1, Signal: syscall.SIGKILL}) }, "the sandbox's agent exited (killed by SIGKILL)"},
		{"ctl lost", func(p *fakeProc, r *run) {
			// a newer control connection takes the agent's events over: ours ends
			c, err := r.fac.Dial()
			if err != nil {
				t.Fatal(err)
			}
			if err := proto.NewConn(c, nil).Send(proto.Hello{Kind: "ctl"}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Close() })
		}, "the sandbox's agent stopped answering"},
		{"runtime stop", func(_ *fakeProc, r *run) { go fe.m.Stop(r.k, "sb-1", "the vault was sealed") }, "the vault was sealed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
			r := fe.runOf("sb-1")
			if r == nil {
				t.Fatalf("not running: %+v", fe.get("sb-1"))
			}
			c.kill(fe.l.last(), r)
			in := fe.waitState("sb-1", StateStopped)
			if !strings.HasPrefix(in.StateDetail, c.want) {
				t.Fatalf("stateDetail %q, want %q", in.StateDetail, c.want)
			}
			assertTornDown(t, fe, r)
		})
	}
	// ends of their own are recorded as the sandbox layer's failures
	f := fe.sbx.Failures(sbx.Filter{Kind: sbx.Tile})
	if len(f) != 4 || f[0].Stage != sbx.Exit {
		t.Fatalf("failures: %+v", f)
	}
	// the OOM killer's reason, from the leaf's count
	r := &run{ops: nsOps, log: &logRing{}, ready: true, exit: ExitStatus{Code: -1, Signal: syscall.SIGKILL}}
	if got := fe.m.exitReason(r, 2); got != "out of memory: 2 processes were killed" {
		t.Fatalf("oom: %q", got)
	}
}

// A stop and the process's own end racing each other tear the run down
// once: one book released, one registry row removed.
func TestTeardownOnce(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	for i := 0; i < 20; i++ {
		fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
		r := fe.runOf("sb-1")
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _ = fe.m.Stop(fe.k, "sb-1", "") }()
		go func() { defer wg.Done(); fe.l.last().die(ExitStatus{Code: 1}) }()
		wg.Wait()
		assertTornDown(t, fe, r)
		fe.waitState("sb-1", StateStopped)
	}
	if fe.books != 20 || fe.held() != 0 {
		t.Fatalf("books: taken %d, held %d", fe.books, fe.held())
	}
}

// A narrowed class stops its running sandboxes (state kept); a widened one
// waits for the next start (egressNext).
func TestEgressChange(t *testing.T) {
	net := fakeNet{"apps/mgr": {{Class: "class:internet", Slot: "internet", Ref: "internet", Reach: "internet",
		Rules: []string{"net:1.1.1.0/24"}}}}
	fe := newFakeEnv(t, func(o *Options) { o.Deps.Net = net })
	fe.create(map[string]any{"name": "sb-1", "mode": "namespace", "net": map[string]any{"egress": "class:internet"}})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	r := fe.runOf("sb-1")
	if !r.pol.IsStrict() || r.pol.Empty() {
		t.Fatalf("the running policy: %+v", r.pol)
	}
	// wider: 1.1.0.0/16 covers 1.1.1.0/24
	net["apps/mgr"][0].Rules = []string{"net:1.1.0.0/16"}
	fe.m.reconcileEgress("apps/mgr")
	in := fe.get("sb-1")
	if in.State != StateRunning || in.Net.EgressNext != "class:internet" || !in.RestartNeeded {
		t.Fatalf("widened: %+v", in)
	}
	// narrower: 8.8.8.8 doesn't cover 1.1.1.0/24
	net["apps/mgr"][0].Rules = []string{"net:8.8.8.8"}
	fe.m.OnSandboxNetChange("apps/mgr")
	in = fe.waitState("sb-1", StateStopped)
	if !strings.Contains(in.StateDetail, "narrowed") {
		t.Fatalf("narrowed: %+v", in)
	}
	assertTornDown(t, fe, r)
	// a restart runs under the new rules
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	if r := fe.runOf("sb-1"); r.pol.Allow(netip.MustParseAddr("1.1.1.1"), 443) || !r.pol.Allow(netip.MustParseAddr("8.8.8.8"), 443) {
		t.Fatalf("the restarted policy: %+v", r.pol)
	}
	// the class gone: stopped too
	net["apps/mgr"] = nil
	fe.m.reconcileEgress("apps/mgr")
	if in := fe.waitState("sb-1", StateStopped); !strings.Contains(in.StateDetail, "is gone") {
		t.Fatalf("gone: %+v", in)
	}
}

// A delete stops the sandbox first; a delete racing a start never leaves
// a run behind a forgotten definition.
func TestDeleteStops(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	r := fe.runOf("sb-1")
	fe.want(fe.do(mgr, "DELETE", "/sandboxes/sb-1", nil), http.StatusNoContent, "")
	assertTornDown(t, fe, r)
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusNotFound, RefNotFound)
}

// The agent client drops what it didn't ask for: an event of a session it
// never opened (or one that ended), a "synced" nobody waits for; a session
// ends once.
func TestAgentClientDropsUnsolicited(t *testing.T) {
	a := &agentClient{sessions: map[int]*agentSession{}, next: firstSession, ready: make(chan struct{}), gone: make(chan struct{}),
		logf: func(string, ...any) {}}
	s := &agentSession{id: 2, started: make(chan struct{}), done: make(chan struct{})}
	a.sessions[2] = s
	a.event(proto.Msg{Op: "synced"})
	a.event(proto.Msg{Op: "exited", Session: 99, Code: 1})
	a.event(proto.Msg{Op: "error", Session: 0, Error: "nothing"})
	a.event(proto.Msg{Op: "started", Session: 2, Pid: 42})
	a.event(proto.Msg{Op: "exited", Session: 2, Code: 3})
	a.event(proto.Msg{Op: "exited", Session: 2, Code: 9}) // after the end: dropped
	<-s.Done()
	if e := s.Exit(); e.Code != 3 || s.Pid() != 42 {
		t.Fatalf("session: %+v pid %d", e, s.Pid())
	}
	if len(a.sessions) != 0 {
		t.Fatalf("an ended session is still routed: %v", a.sessions)
	}
}

// A dial waits out a factory whose queue is full, briefly: a burst, not a
// wedged agent.
func TestDialRetriesAFullQueue(t *testing.T) {
	fac, child, err := sandbox.NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	defer fac.Close()
	defer child.Close()
	var held []interface{ Close() error }
	for i := 0; ; i++ {
		c, err := fac.Dial()
		if err != nil {
			if !errors.Is(err, unix.EAGAIN) {
				t.Fatalf("filling the queue: %v", err)
			}
			break
		}
		held = append(held, c)
		if i > 100000 {
			t.Fatal("the queue never fills")
		}
	}
	defer func() {
		for _, c := range held {
			c.Close()
		}
	}()
	go func() {
		time.Sleep(200 * time.Millisecond)
		if c, err := sandbox.AcceptFrom(child); err == nil {
			c.Close()
		}
	}()
	c, err := dialAgent(fac)
	if err != nil {
		t.Fatalf("a dial after the queue drained: %v", err)
	}
	c.Close()
	// nobody drains it: unavailable, after the retry window
	start := time.Now()
	for {
		c, err := fac.Dial()
		if err != nil {
			break
		}
		held = append(held, c)
	}
	_, err = dialAgent(fac)
	var e *Error
	if !errors.As(err, &e) || e.Refusal != RefUnavailable || time.Since(start) < dialRetry {
		t.Fatalf("a queue that stays full: %v after %s", err, time.Since(start))
	}
}

// The pin refuses a sandbox whose state went missing, and one whose base
// is no longer installed: error, never a blank root.
func TestPinErrors(t *testing.T) {
	fe := newFakeEnv(t)
	fe.create(ns("sb-1"))
	d, _ := fe.m.defs.get(fe.k, "sb-1")
	d.Base = "b-gone"
	if err := fe.m.defs.put(fe.k, d); err != nil {
		t.Fatal(err)
	}
	w := fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil)
	fe.want(w, http.StatusConflict, RefState)
	if in := fe.get("sb-1"); in.State != StateError || !strings.Contains(in.StateDetail, "state is missing") {
		t.Fatalf("missing state: %+v", in)
	}
	if fe.held() != 0 {
		t.Fatalf("books held: %d", fe.held())
	}
	// the next start is refused as it stands
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusConflict, RefState)

	fe.create(ns("sb-2"))
	d2, _ := fe.m.defs.get(fe.k, "sb-2")
	cur, _ := fe.m.CurDir(fe.k, d2)
	os.MkdirAll(cur, 0o700)
	layers.Stamp(cur, layers.Stamps{Base: "b-gone"})
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-2/start", nil), http.StatusConflict, RefState)
	if in := fe.get("sb-2"); in.State != StateError || !strings.Contains(in.StateDetail, "b-gone") {
		t.Fatalf("missing base: %+v", in)
	}
}

// A stop's sync is bounded as a whole, its send included: an agent whose
// control loop is stuck (a syncfs or a spawn on a wedged root) stops
// reading, a large line fills the connection and blocks the next send, and
// the stop must still get to its kill.
func TestSyncBoundedWhenTheAgentStopsReading(t *testing.T) {
	ours, theirs := net.Pipe() // unbuffered: nothing gets through while nobody reads
	defer theirs.Close()
	a := &agentClient{ctl: proto.NewConn(ours, nil), sessions: map[int]*agentSession{}, next: firstSession,
		ready: make(chan struct{}), gone: make(chan struct{}), logf: func(string, ...any) {}}
	defer a.Close()
	go func() { _ = a.send(proto.Msg{Op: "exec", Exec: &proto.Exec{Argv: []string{"true"}}}) }() // holds sendMu, blocked
	time.Sleep(50 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- a.Sync(200 * time.Millisecond) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a sync nobody answered succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Sync blocked past its bound on a send the agent never reads")
	}
}
