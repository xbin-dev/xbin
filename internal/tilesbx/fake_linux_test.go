//go:build linux

package tilesbx

import (
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
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

// fakeLauncher stands in for sandbox.Launch: its "sandbox" is an agentcore
// Core in the test process, served over the real factory the runtime made,
// with a socketpair for a TUN (the relay runs on it) — so the runtime's
// agent client, relay, registry and teardown all run for real.
type fakeLauncher struct {
	t *testing.T

	mu    sync.Mutex
	procs []*fakeProc
	fail  error                    // Start fails with this
	mod   func(*agentcore.Options) // tweaks the next Core
	// onSignal is how its processes take a signal other than SIGKILL (nil:
	// they ignore it) — a VM's shim exits 129 on SIGHUP.
	onSignal func(p *fakeProc, s os.Signal)
}

func (f *fakeLauncher) Start(spec *sandbox.Spec, o LaunchOpts) (Proc, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	dup := func(fl *os.File) *os.File {
		fd, err := unix.FcntlInt(fl.Fd(), unix.F_DUPFD_CLOEXEC, 3)
		if err != nil {
			f.t.Fatal(err)
		}
		return os.NewFile(uintptr(fd), fl.Name())
	}
	p := &fakeProc{spec: spec, log: o.Log, agent: dup(spec.Agent), lock: dup(spec.Lock), tunFD: -1, onSignal: f.onSignal,
		exit: make(chan ExitStatus, 1), dead: make(chan struct{}), pid: 100000 + len(f.procs)}
	opts := agentcore.Options{Spawn: agentcore.ProcSpawner(), Root: f.t.TempDir(), StreamWait: 3 * time.Second}
	if f.mod != nil {
		f.mod(&opts)
	}
	p.core = agentcore.New(opts)
	go func() {
		_ = p.core.Serve(func() (io.ReadWriteCloser, error) { return sandbox.AcceptFrom(p.agent) })
		p.die(ExitStatus{Code: 0}) // the factory's EOF: the agent syncs and exits 0
	}()
	f.procs = append(f.procs, p)
	return p, nil
}

// count is how many processes it started.
func (f *fakeLauncher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.procs)
}

// last is the newest process.
func (f *fakeLauncher) last() *fakeProc {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.procs[len(f.procs)-1]
}

type fakeProc struct {
	spec  *sandbox.Spec
	log   io.Writer
	core  *agentcore.Core
	agent *os.File // the "sandbox's" copy of the factory's end
	lock  *os.File // its copy of the lock: the flock lives while it is open
	pid   int

	mu       sync.Mutex
	tunFD    int      // what RecvTUN handed out
	tunPeer  *os.File // the sandbox's side of the TUN
	started  bool
	cleaned  bool
	signals  []os.Signal // what it was sent besides SIGKILL
	onSignal func(p *fakeProc, s os.Signal)

	once sync.Once
	exit chan ExitStatus
	dead chan struct{}

	ignoreKills atomic.Int32 // kills that don't take
}

func (p *fakeProc) Pid() int           { return p.pid }
func (p *fakeProc) InCgroup() bool     { return true }
func (p *fakeProc) SetupUserns() error { return nil }

func (p *fakeProc) RecvTUN() (int, error) {
	if p.spec.Net != "relay" {
		return -1, nil
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	p.mu.Lock()
	p.tunFD, p.tunPeer = fds[0], os.NewFile(uintptr(fds[1]), "tun-peer")
	p.mu.Unlock()
	return fds[0], nil
}

// Started closes the caller's copies, as Handle.Started does.
func (p *fakeProc) Started() {
	p.mu.Lock()
	p.started = true
	p.mu.Unlock()
	p.spec.Agent.Close()
	p.spec.Lock.Close()
}

func (p *fakeProc) Wait() ExitStatus { return <-p.exit }

// Kill ends it — unless it was told to shrug kills off (ignoreKills): a
// PID 1 stuck in the kernel.
func (p *fakeProc) Kill() error {
	if p.ignoreKills.Add(-1) >= 0 {
		return nil
	}
	p.die(ExitStatus{Code: -1, Signal: syscall.SIGKILL})
	return nil
}

func (p *fakeProc) Signal(s os.Signal) error {
	if s == syscall.SIGKILL {
		return p.Kill()
	}
	p.mu.Lock()
	p.signals = append(p.signals, s)
	h := p.onSignal
	p.mu.Unlock()
	if h != nil {
		h(p, s)
	}
	return nil
}

func (p *fakeProc) Cleanup() {
	p.mu.Lock()
	p.cleaned = true
	p.mu.Unlock()
}

// die ends the "sandbox" with st: its agent's copies of the factory and
// the lock go, and so does its side of the TUN — and, as when a pid
// namespace's init dies, every session's process group.
func (p *fakeProc) die(st ExitStatus) {
	p.once.Do(func() {
		for _, s := range p.core.Sessions() {
			if s.Pid > 0 {
				_ = syscall.Kill(-s.Pid, syscall.SIGKILL)
			}
		}
		p.agent.Close()
		p.lock.Close()
		p.mu.Lock()
		if p.tunPeer != nil {
			p.tunPeer.Close()
		}
		p.mu.Unlock()
		close(p.dead)
		p.exit <- st
	})
}

// fakeEnv is a runtime whose sandboxes the fake launcher runs.
type fakeEnv struct {
	*testEnv
	l   *fakeLauncher
	sbx *sbx.Registry
	k   Key

	bookMu   sync.Mutex
	reserved int // books held now
	books    int // books taken in all
}

// newFakeEnv: a runtime over a fake base rootfs (a stamped dir), sandboxes
// run by the fake launcher.
func newFakeEnv(t *testing.T, mut ...func(*Options)) *fakeEnv {
	t.Helper()
	fe := &fakeEnv{l: &fakeLauncher{t: t}, sbx: sbx.New(), k: Key{Tile: "apps/mgr"}}
	rootfs := filepath.Join(t.TempDir(), "rootfs")
	if err := os.MkdirAll(filepath.Join(rootfs, "etc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootfs, layers.VersionFile), []byte("b-test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fe.testEnv = newEnv(t, append([]func(*Options){func(o *Options) {
		o.Rootfs, o.BxPath = rootfs, "/nonexistent/bx"
		o.Deps.Launcher, o.Deps.Sbx = fe.l, fe.sbx
	}}, mut...)...)
	admit := fe.m.reserve // the real admission, counted
	fe.m.reserve = func(k Key, d *Def) (func(), error) {
		release, err := admit(k, d)
		if err != nil {
			return nil, err
		}
		fe.bookMu.Lock()
		fe.reserved++
		fe.books++
		fe.bookMu.Unlock()
		return func() {
			release()
			fe.bookMu.Lock()
			fe.reserved--
			fe.bookMu.Unlock()
		}, nil
	}
	t.Cleanup(func() { fe.m.StopAll("the test ended") })
	return fe
}

// held is how many books are held now.
func (fe *fakeEnv) held() int {
	fe.bookMu.Lock()
	defer fe.bookMu.Unlock()
	return fe.reserved
}

// runOf is the sandbox's current run (nil: none).
func (fe *fakeEnv) runOf(name string) *run {
	fe.m.mu.Lock()
	defer fe.m.mu.Unlock()
	if b := fe.m.live[fe.k][name]; b != nil {
		return b.run
	}
	return nil
}

// get is the sandbox's SandboxInfo.
func (fe *fakeEnv) get(name string) Info {
	fe.t.Helper()
	in, ok := fe.m.infoOf(fe.k, name)
	if !ok {
		fe.t.Fatalf("no sandbox %q", name)
	}
	return in
}

// waitState waits until the sandbox is in state.
func (fe *fakeEnv) waitState(name, state string) Info {
	fe.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		in := fe.get(name)
		if in.State == state {
			return in
		}
		if time.Now().After(deadline) {
			fe.t.Fatalf("sandbox %q is %s (%s), want %s", name, in.State, in.StateDetail, state)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// execRun is the minimal exec path: argv run to its end through the run's
// agent client, its combined output and how it ended.
func execRun(t *testing.T, r *run, argv []string) (string, SessionExit) {
	t.Helper()
	s, streams, err := r.client().Exec(proto.Exec{Argv: argv, Env: sessionEnv(r.def, nil, nil), Merge: true, NoStdin: true, CwdStrict: false})
	if err != nil {
		t.Fatalf("exec %q: %v", argv, err)
	}
	out := streams["stdout"]
	defer out.Close()
	_ = out.SetReadDeadline(time.Now().Add(30 * time.Second))
	b, _ := io.ReadAll(out)
	select {
	case <-s.Done():
	case <-time.After(30 * time.Second):
		t.Fatalf("exec %q never ended", argv)
	}
	return string(b), s.Exit()
}

// fakeClock is the runtime's time in the idle tests: Now moves only when
// advance says so, and advance fires the timers that fell due, in order —
// on the caller's goroutine, so it returns once what they did is done.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	c    *fakeClock
	at   time.Time
	f    func()
	done bool // fired or stopped
}

func (t *fakeTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := !t.done
	t.done = true
	return was
}

func newFakeClock() *fakeClock { return &fakeClock{now: time.UnixMilli(1790000000000)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{c: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

// armed is how many timers wait to fire.
func (c *fakeClock) armed() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.timers {
		if !t.done {
			n++
		}
	}
	return n
}

// advance moves the clock on by d, firing every timer due by then (and
// those the fired ones arm, when they fall due too).
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var next *fakeTimer
		for _, t := range c.timers {
			if !t.done && !t.at.After(end) && (next == nil || t.at.Before(next.at)) {
				next = t
			}
		}
		if next == nil {
			c.now = end
			c.mu.Unlock()
			return
		}
		next.done = true
		if next.at.After(c.now) {
			c.now = next.at
		}
		c.mu.Unlock()
		next.f()
	}
}

// withClock runs the runtime on c.
func withClock(c *fakeClock) func(*Options) { return func(o *Options) { o.Now = c.Now } }
