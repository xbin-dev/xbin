package tilesbx

// lifecycle.go — a sandbox's runs (plans/tile-sandbox-runtime.md §7): start,
// stop, and the one teardown every run ends through, whatever ended it — a
// stop the manager or the runtime asked for, the agent exiting (it crashed;
// exit 3: its root filesystem died), an OOM kill, the control connection
// lost. Each transition runs in its sandbox's single flight (box.flight),
// never under the definitions mutex m.mu: the flight is taken first, m.mu
// only for moments.

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
	"github.com/xbin-dev/xbin/internal/sandbox/relay"
	"github.com/xbin-dev/xbin/internal/sbx"
)

const (
	nsReadyWait = 30 * time.Second // a namespace sandbox's agent answers "ready" within
	stopSync    = 5 * time.Second  // a stop waits this long for "synced", then kills
	ctlGrace    = time.Second      // a lost ctl waits this long for the process's own exit (its reason wins)

	endWait  = 15 * time.Second // a stop waits this long for the teardown (Manager.endWait: tests shorten it)
	lockWait = 5 * time.Second  // a start (a reset, a rebase) waits this long for an earlier run's lock and leaf (Manager.lockWait)
)

// run is one run of a sandbox: from its launch to its teardown.
type run struct {
	k        Key
	def      *Def // as launched (sizes clamped)
	b        *box
	proc     Proc
	fac      *sandbox.Factory
	agent    *agentClient
	relay    *relay.Relay
	tunFD    int
	leaf     string
	accel    string // a VM's: kvm | emulate (specAccel; "" in namespace mode)
	class    EgressClass
	pol      sandbox.EgressPolicy
	ops      *modeOps
	mounts   []runMount // its res mounts as bound: re-checked when the tile's hold on them changes (§5)
	release  func()     // the book (reserve)
	modeUndo func()     // what the mode's spec took (a VM reservation)
	unlist   func()     // the registry row
	log      *logRing
	logMark  int64 // where its own output starts in log
	started  time.Time

	exited chan struct{} // closed once the process ended (exit is set)
	exit   ExitStatus
	done   chan struct{} // closed once the teardown finished
	once   sync.Once

	idle    idleState      // activity, holds and the idle timer (idle.go)
	killers sync.WaitGroup // leaf killers in flight: the teardown waits for them before removing the leaf

	// mu guards what the start attaches while the watcher may already be
	// tearing the run down (tunFD, relay, agent, unlist, ready), and why it
	// ended.
	mu     sync.Mutex
	why    string // the first reason an end was asked with
	asked  bool   // someone asked it to end (a stop, an unwind, an incident)
	ready  bool   // it reached running
	closed bool   // the teardown began: nothing more may be attached
}

// client is the run's agent client (nil before the start reached it).
func (r *run) client() *agentClient {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.agent
}

// errEnded is a start that lost the race with its own teardown.
var errEnded = errors.New("it ended while starting")

// attach runs set under r.mu unless the teardown began (false: the caller
// closes what it would have attached).
func (r *run) attach(set func()) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	set()
	return true
}

// end asks the run to end with why (the first reason wins) and kills its
// process; the watcher then runs the teardown. Every ask kills again, so a
// second stop retries a kill that didn't take. It reports whether this
// call was the first to ask.
func (m *Manager) end(r *run, why string) bool {
	first := m.ask(r, why)
	m.kill(r)
	return first
}

// ask records that r is to end, and why (the first reason wins), without
// ending it: a mode's stop that lets the process exit on its own first (a
// VM's SIGHUP) kills it itself if it doesn't. It reports whether this call
// was the first to ask.
func (m *Manager) ask(r *run, why string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	first := !r.asked
	if first {
		r.asked, r.why = true, why
	}
	return first
}

// kill SIGKILLs the sandbox's first process — PID 1 of its pid namespace,
// so the kernel ends the rest — and every process of the sandbox besides
// (Proc.Kill: its descendants; cgroup.kill: its leaf). A PID 1 stuck on a
// FUSE request of a wedged fuse-overlayfs (WP-3's note; the root-dir create
// deadlock) can't die before its server does (killtree.go). The leaf is
// killed off the caller's goroutine (cgroup.Kill waits for it to empty),
// and only while the teardown hasn't begun, which waits for it before it
// removes the leaf: without cgroup.kill (before 5.14) the killer SIGKILLs
// the leaf's pids on every round, and a leaf removed and made again by the
// next start (a reset's restart) must never get those rounds.
func (m *Manager) kill(r *run) {
	_ = r.proc.Kill()
	if r.leaf == "" {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.killers.Add(1)
	r.mu.Unlock()
	go func() {
		defer r.killers.Done()
		_ = m.cg.Kill(r.leaf)
	}()
}

// watch waits for the process and tears the run down: every way a
// sandbox ends comes through here.
func (m *Manager) watch(r *run) {
	r.exit = r.proc.Wait()
	close(r.exited)
	m.teardown(r)
}

// teardown is §7's, once per run: the relay closed and then its TUN, the
// leaf killed, its OOM kills read and the leaf removed, the book released,
// the registry row, the factory and the control connection gone (every
// exec still running ends killed, its record and ring kept), and the
// sandbox stopped with why it ended in stateDetail.
func (m *Manager) teardown(r *run) {
	r.once.Do(func() {
		r.mu.Lock()
		r.closed = true // what the start still attaches, it closes itself (attach)
		rl, tunFD, agent, unlist := r.relay, r.tunFD, r.agent, r.unlist
		r.tunFD = -1
		r.mu.Unlock()
		m.disarmIdle(r)
		if rl != nil {
			rl.Close() // its readers have stopped when it returns
		}
		if tunFD >= 0 {
			_ = syscall.Close(tunFD) // after Close: nothing reads it any more
		}
		var oom int64
		if r.leaf != "" {
			r.killers.Wait() // no killer of this run outlives its leaf
			if err := m.cg.Kill(r.leaf); err != nil {
				slog.Warn("tile sandbox: emptying its cgroup", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
			}
			oom = m.cg.OOMKills(r.leaf)
			m.cg.Remove(r.leaf)
		}
		r.release()
		if unlist != nil {
			unlist()
		}
		if agent != nil {
			agent.Close() // every session still open ends killed
		}
		r.b.execs.awaitRun(r, execEndWait) // their records say so before a stop answers
		r.fac.Close()
		if r.modeUndo != nil {
			r.modeUndo()
		}
		r.proc.Cleanup()

		r.mu.Lock()
		asked, why, ready := r.asked, r.why, r.ready
		r.mu.Unlock()
		detail := why
		if !asked {
			detail = m.exitReason(r, oom)
			if ready { // a start that fails records its own failure
				m.failed(r.k, r.def, sbx.Exit, errors.New(detail))
			}
		}
		m.measureSoon(r.k, r.def) // §6.3: measured at each stop (queued before it reads stopped)
		m.mu.Lock()
		if r.b.run == r {
			b := r.b
			b.run, b.launched, b.reach, b.accel, b.started, b.egressNext = nil, nil, "", "", 0, false
			b.state, b.detail = StateStopped, detail
			b.lastActive = r.idle.last.Load() // its last activity (a stop the manager asked for is one): an idle stop's is idleStopMin before it
			if b.lastActive == 0 {
				b.lastActive = m.now().UnixMilli()
			}
		}
		m.mu.Unlock()
		if !ready || !asked || why != "" {
			slog.Info("tile sandbox ended", "tile", r.k.Tile, "sandbox", r.def.Name, "why", detail)
		}
		close(r.done)
	})
}

// exitReason is why a run ended on its own (§7): its mode's reason (a VM's
// console), the OOM killer, its root filesystem, or its agent's exit —
// with the log's last line when it never got as far as running.
func (m *Manager) exitReason(r *run, oom int64) string {
	var why string
	var quoted bool
	if r.ops.exitReason != nil {
		why, quoted = r.ops.exitReason(r, oom)
	}
	switch {
	case why != "":
	case oom > 0 && r.exit.Code != agentcore.ExitRootGone:
		why = fmt.Sprintf("out of memory: %d processes were killed", oom)
	case r.exit.Code == agentcore.ExitRootGone:
		why = "the sandbox's root filesystem (fuse-overlayfs) died"
	case r.exit.Signal > 0:
		why = "the sandbox's agent exited (killed by " + sigName(r.exit.Signal) + ")"
	case r.exit.Err != nil && r.exit.Code < 0:
		why = "the sandbox's agent exited (" + r.exit.Err.Error() + ")"
	default:
		why = fmt.Sprintf("the sandbox's agent exited (code %d)", r.exit.Code)
	}
	r.mu.Lock()
	ready := r.ready
	r.mu.Unlock()
	if !ready {
		why = "the sandbox didn't start: " + why
		if t := r.log.TailSince(r.logMark, 1); t != "" && !quoted {
			why += ": " + t
		}
	}
	return why
}

func sigName(s syscall.Signal) string {
	switch s {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGSEGV:
		return "SIGSEGV"
	case syscall.SIGABRT:
		return "SIGABRT"
	}
	return fmt.Sprintf("signal %d", int(s))
}

// lost is the control connection ending while the run is up: an incident.
// The process's own exit, when it follows at once, says why better (its
// code); otherwise the sandbox is ended for it.
func (m *Manager) lost(r *run, err error) {
	select {
	case <-r.exited:
		return
	case <-time.After(ctlGrace):
	}
	why := "the sandbox's agent stopped answering (its control connection closed)"
	if m.end(r, why) {
		slog.Warn("tile sandbox: its agent's control connection closed; ending it", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
		m.failed(r.k, r.def, sbx.Exit, errors.New(why))
	}
}

// box gets k's sandbox's live state, making it: the definition must exist.
func (m *Manager) boxFor(k Key, name string) (*box, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.defs.get(k, name); !ok {
		return nil, refuse(RefNotFound, "no sandbox %q", name)
	}
	return m.boxLocked(k, name), nil
}

// boxLocked is k's sandbox's live state, made when missing. Callers hold m.mu.
func (m *Manager) boxLocked(k Key, name string) *box {
	b := m.live[k][name]
	if b == nil {
		b = newBox()
		if m.live[k] == nil {
			m.live[k] = map[string]*box{}
		}
		m.live[k][name] = b
	}
	return b
}

// Start brings k's sandbox up and returns once it runs — or didn't: a
// refusal (*Error: its state, a limit, the vault sealed, a mount it no
// longer holds) or a failed launch, which leaves it stopped with why in
// stateDetail. A sandbox already running is left alone.
func (m *Manager) Start(k Key, name string) error {
	b, err := m.boxFor(k, name)
	if err != nil {
		return err
	}
	b.flight.Lock()
	defer b.flight.Unlock()
	return m.startLocked(k, name, b)
}

// startLocked is Start inside the flight.
func (m *Manager) startLocked(k Key, name string, b *box) error {
	m.mu.Lock()
	d, ok := m.defs.get(k, name)
	switch {
	case !ok || m.live[k][name] != b:
		m.mu.Unlock()
		return refuse(RefNotFound, "no sandbox %q", name)
	case b.run != nil:
		m.mu.Unlock()
		return nil // running (a stop in the flight would have ended it)
	case b.state == StateError || b.state == StateCreating:
		st, detail := b.state, b.detail
		m.mu.Unlock()
		return &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q is %s: %s", name, st, detail)}
	}
	if on, why := m.policy.on(); !on {
		m.mu.Unlock()
		return &Error{Refusal: RefUnavailable, Msg: why, RetryAfter: time.Minute}
	}
	lim := m.limitsFor(k.Tile)
	d.MemMiB, d.VCPUs, d.DiskGiB = clamp(d, lim)
	ops := m.modes[d.Mode]
	if ops == nil {
		m.mu.Unlock()
		return refuse(RefUnsupported, "%s mode can't run tile sandboxes in this xbind", d.Mode)
	}
	b.state, b.detail, b.stopAsk = StateStarting, "", "" // from here a stop over a set sees it (StopWhere)
	m.mu.Unlock()

	err := m.launch(k, d, b, lim, ops)
	if err == nil {
		return nil
	}
	m.mu.Lock()
	if b.run == nil && b.state == StateStarting { // failed before its process ran
		b.state, b.detail = StateStopped, err.Error()
		var e *Error
		if errors.As(err, &e) && e.State == StateError {
			b.state = StateError
		}
	}
	m.mu.Unlock()
	m.failed(k, d, sbx.Start, err)
	return err
}

// launch is §7's start, steps 1–8. What fails before the process started
// is undone here, in reverse; after that the run owns it all, and a
// failure ends the run (its teardown undoes the rest).
func (m *Manager) launch(k Key, d *Def, b *box, lim Limits, ops *modeOps) (err error) {
	var undo []func()
	defer func() {
		if err != nil {
			for i := len(undo) - 1; i >= 0; i-- {
				undo[i]()
			}
		}
	}()
	// 0. the mode can run now (a VM: the VM policy's switches)
	if ops.check != nil {
		if err := ops.check(m); err != nil {
			return err
		}
	}
	// 0. the tile may run sandboxes (removed, disabled, cap:sandboxes
	// revoked?) — asked once the sandbox is starting, so a stop over the
	// tile either finds this start (StopWhere marks it) or is seen here
	if _, refusal := m.tileReach(k.Tile); refusal != nil {
		return refusal
	}
	// 1. admission: the book
	release, err := m.reserve(k, d)
	if err != nil {
		return err
	}
	release = onceFunc(release)
	undo = append(undo, release)
	// 2. the lock (and an orphan's leaf, emptied)
	dir, err := m.StateDir(k, d)
	if err != nil {
		return err
	}
	lock, err := m.lockRun(k, d, dir)
	if err != nil {
		return err
	}
	undo = append(undo, func() { lock.Close() }) // xbind was its only holder
	// 3. the pinned base
	cur := filepath.Join(dir, "cur")
	lower, err := m.pin(k, d, cur)
	if err != nil {
		return err
	}
	// 4. mounts and egress, against the tile's reach now
	binds, err := m.binds(k, d)
	if err != nil {
		return err
	}
	class, pol, err := m.egress(k, d.Net.Egress)
	if err != nil {
		return err
	}
	// 5. the spec
	fac, child, err := sandbox.NewFactory()
	if err != nil {
		return err
	}
	undo = append(undo, func() { fac.Close(); child.Close() })
	spec, modeUndo, err := ops.spec(m, k, d, specInput{Lower: lower, Cur: cur, Binds: binds, Agent: child, Lock: lock})
	if err != nil {
		return err
	}
	modeUndo = onceFunc(modeUndo)
	undo = append(undo, modeUndo)
	accel := specAccel(spec)
	// 6. the leaf, the launch
	ll := ops.leaf(d, lim, accel)
	leafDir, leaf, err := m.prepareLeaf(k, d, ll)
	if err != nil {
		return fmt.Errorf("its cgroup: %w", err)
	}
	if leaf != "" {
		undo = append(undo, func() { m.cg.Remove(leaf) })
	}
	b.log.Write([]byte(fmt.Sprintf("--- start %s ---\n", m.now().UTC().Format(time.RFC3339))))
	logMark := b.log.Mark()
	proc, err := m.launcher.Start(spec, LaunchOpts{Log: b.log, CgroupFD: leafDir})
	if leafDir != nil {
		leafDir.Close()
	}
	if err != nil {
		return fmt.Errorf("the sandbox didn't start: %w", err)
	}
	undo = nil // the run owns it all from here: its teardown undoes it
	proc.Started()
	r := &run{k: k, def: d, b: b, proc: proc, fac: fac, tunFD: -1, leaf: leaf, accel: accel, class: class, pol: pol, ops: ops, mounts: resMounts(d, binds),
		release: release, modeUndo: modeUndo, log: b.log, logMark: logMark, started: m.now(), exited: make(chan struct{}), done: make(chan struct{})}
	m.mu.Lock()
	b.run = r
	ask := b.stopAsk // a stop over a set picked it before it had a process
	m.mu.Unlock()
	go m.watch(r)
	fail := func(what string, err error) error {
		select {
		case <-r.exited: // it ended on its own: its exit (and its log) say why
		default:
			msg := "the sandbox didn't start: " + what + ": " + err.Error()
			if t := r.log.TailSince(r.logMark, 1); t != "" {
				msg += " (" + t + ")"
			}
			m.end(r, msg)
		}
		<-r.done
		m.mu.Lock()
		detail, ask := b.detail, b.stopAsk
		m.mu.Unlock()
		if ask != "" {
			return stoppedStarting(d.Name, ask)
		}
		return errors.New(detail)
	}
	if ask != "" {
		m.end(r, ask)
		return fail("it was stopped", errEnded)
	}
	if leaf != "" && !proc.InCgroup() { // the pre-5.7 fallback: join before the maps release it
		if _, err := m.cg.AddWith(leaf, proc.Pid(), ll); err != nil {
			return fail("its cgroup", err)
		}
	}
	if err := proc.SetupUserns(); err != nil {
		return fail("its uid maps", err)
	}
	fd, err := proc.RecvTUN()
	if err != nil {
		return fail("its network", err)
	}
	if !r.attach(func() { r.tunFD = fd }) {
		_ = syscall.Close(fd)
		return fail("its network", errEnded)
	}
	if fd >= 0 {
		rl, err := relay.Start(m.relayConfig(fd, pol))
		if err != nil {
			return fail("its relay", err)
		}
		if !r.attach(func() { r.relay = rl }) {
			rl.Close()
			return fail("its relay", errEnded)
		}
	}
	// 7. the agent
	a, err := connectAgent(fac, func(f string, args ...any) {
		slog.Debug("tile sandbox agent: "+fmt.Sprintf(f, args...), "tile", k.Tile, "sandbox", d.Name)
	}, func(err error) { m.lost(r, err) })
	if err != nil {
		return fail("its agent", err)
	}
	if !r.attach(func() { r.agent = a }) {
		a.Close()
		return fail("its agent", errEnded)
	}
	if err := a.WaitReady(ops.readyWait(r), r.exited); err != nil {
		return fail("its agent", err)
	}
	// 8. running — unless tile sandboxes were switched off since the start's
	// check: the switch stopped what ran then, and b.run was nil. (Switched
	// off after this check, it finds b.run and stops it after this flight.)
	m.mu.Lock()
	on, why := m.policy.on()
	m.mu.Unlock()
	if !on {
		m.end(r, why+": stopped, state kept")
		<-r.done
		return &Error{Refusal: RefUnavailable, Msg: why, RetryAfter: time.Minute}
	}
	// Nor if the vault was sealed since its mounts were resolved: the seal
	// stopped what ran then (§5), and b.run was nil — its bind would keep a
	// decrypted view alive past the seal. (Sealed after b.run was set, the
	// seal found it, and stops it after this flight.)
	if len(r.mounts) > 0 && m.deps.Vault != nil && m.deps.Vault.Sealed() {
		m.end(r, "the vault was sealed: stopped, state kept — start it again once the vault is unsealed")
		<-r.done
		return &Error{Refusal: RefUnavailable, Msg: "the vault was sealed while it started: its resources can't be mounted until it is unsealed", RetryAfter: 30 * time.Second}
	}
	// Nor if a mount was narrowed since it was resolved (a res: grant
	// revoked, a role dropped, the resource gone): the reconcile of the
	// tile's mounts (OnResourceChange, a rescan) looked at what ran, and
	// b.run was nil. (Narrowed after b.run was set, it found it, and
	// stops it after this flight.) Refused as step 4 would have.
	if len(r.mounts) > 0 {
		if why := m.mountsNarrowed(r); why != "" {
			m.end(r, why)
			<-r.done
			return refuse(RefInvalid, "a mount changed while it started: %s", why)
		}
	}
	// Nor if its mode was switched off meanwhile (the VM policy): a stop
	// over the running ones (OnVMPolicy) found it once it had b.run, and
	// this finds a switch turned before that — refused as the first check
	// would have.
	if ops.check != nil {
		if err := ops.check(m); err != nil {
			m.end(r, err.Error()+": stopped, state kept")
			<-r.done
			return err
		}
	}
	if !r.attach(func() { r.ready = true; r.unlist = m.register(r) }) {
		return fail("its agent", errEnded)
	}
	m.mu.Lock()
	ask = b.stopAsk // (a stop over a set that picked it has ended it too)
	if b.run == r && b.state == StateStarting && ask == "" {
		b.state, b.detail = StateRunning, ""
		b.launched, b.reach, b.accel, b.started = d, class.Reach, r.accel, r.started.UnixMilli()
		b.egressNext = false
		m.touch(r)
		m.armIdleLocked(r, 0) // the idle stop (idle.go)
	}
	m.mu.Unlock()
	if ask != "" {
		m.end(r, ask)
		return fail("it was stopped", errEnded)
	}
	m.kickUsage() // measured while it runs (usage.go)
	if d.Mode == ModeNamespace {
		m.watchDisk() // an upper has no cap: the partition is watched while one runs
	}
	return nil
}

// stoppedStarting is the answer to a start a stop over a set ended before
// it came up (StopWhere: a revoke, a disable, a seal, low disk, the kill
// switch): unavailable, as the same reason answers a start after it; the
// sandbox is stopped, why in its stateDetail.
func stoppedStarting(name, why string) error {
	return &Error{Refusal: RefUnavailable, RetryAfter: 30 * time.Second, Msg: fmt.Sprintf("sandbox %q was stopped while it started: %s", name, why)}
}

// onceFunc makes f idempotent.
func onceFunc(f func()) func() {
	if f == nil {
		return func() {}
	}
	var once sync.Once
	return func() { once.Do(f) }
}
