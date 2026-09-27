package tilesbx

// exec.go — background execs (plans/tile-sandbox-runtime.md §3.6). Each is
// one session of the sandbox's agent with one output stream — a non-tty
// exec's stdout and stderr merged, a tty exec's terminal — kept in a ring
// (ring.go) that is read by byte offset, plus its stdin, its signals, and
// its end. A sandbox's execs outlive its runs: a stop, whatever caused it,
// ends the ones running as killed (signal KILL, exitCode null), and their
// records and rings stay, so a reader still gets their output to its end.
// Only an id from another xbind start is lost (410). What a command runs
// with is command.go's; the table of a sandbox's execs is exectable.go's.

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
	"github.com/xbin-dev/xbin/internal/termwire"
)

const (
	execRetain     = time.Hour // a finished exec is kept at least this long …
	execRetainLast = 50        // … or, past it, while among its sandbox's last 50 finished
	execRetainMax  = 1000      // finished records a sandbox keeps within the hour, at most
	startWait      = 30 * time.Second
	drainWait      = 3 * time.Second        // after its exit, its output reads to the end within this
	killedDrain    = 500 * time.Millisecond // … after its sandbox stopped under it
	execEndWait    = 3 * time.Second        // a teardown waits this long for its execs' records
	termGrace      = 5 * time.Second        // a timeout's TERM, then KILL this much later
	inWait         = 30 * time.Second       // one write to a command's input waits for it to read
	argvEnvMax     = 256 << 10              // a command's argv and env, in bytes
	execBodyMax    = argvEnvMax + 64<<10    // POST …/execs
	ttyReplay      = 256 << 10              // a TTY attach replays at most this much
	maxLabel       = 128
	maxTimeoutMs   = 365 * 24 * 3600 * 1000
)

// The states of an exec (the contract's).
const (
	ExecRunning = "running"
	ExecExited  = "exited"
	ExecKilled  = "killed"
)

// Exec is one background exec as the runtime answers it: the contract's,
// plus forUser and uid.
type Exec struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Cmd      string   `json:"cmd,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	Cwd      string   `json:"cwd"`
	TTY      bool     `json:"tty"`
	State    string   `json:"state"`
	ExitCode *int     `json:"exitCode"`
	Signal   string   `json:"signal"`
	Started  int64    `json:"started"`
	Ended    *int64   `json:"ended"`
	Total    int64    `json:"total"`
	ClientID string   `json:"clientId,omitempty"`
	ForUser  string   `json:"forUser,omitempty"`
	UID      *uint32  `json:"uid,omitempty"`
}

// ExecRequest is POST …/execs's body: the contract's, plus uid, gid and
// forUser.
type ExecRequest struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	TTY       bool              `json:"tty,omitempty"`
	Rows      int               `json:"rows,omitempty"`
	Cols      int               `json:"cols,omitempty"`
	Stdin     bool              `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
	Label     string            `json:"label,omitempty"`
	ClientID  string            `json:"clientId,omitempty"`
	UID       *uint32           `json:"uid,omitempty"`
	GID       *uint32           `json:"gid,omitempty"`
	ForUser   string            `json:"forUser,omitempty"`
}

// execRec is one exec. What its start sets is read-only once it is listed.
type execRec struct {
	id, label, cmd, cwd string
	clientID, reqHash   string
	forUser             string
	argv                []string
	tty, stdin          bool
	uid                 *uint32
	started             int64
	b                   *box
	r                   *run
	sess                *agentSession
	ring                *ring
	in, out             net.Conn      // its input (stdin: true, or the pty; nil: none) and its output stream
	pumped, ready, done chan struct{} // output read to its end; the start resolved; the end recorded
	failed              bool          // the start failed (set before ready closes)
	inMu                sync.Mutex    // one writer to in at a time
	inClosed            bool          // stdin's eof was sent
	mu                  sync.Mutex    // what follows
	hub                 *termwire.Hub // a tty's, while it runs
	state, signal       string
	exitCode            *int
	ended               int64
	timedOut, deleted   bool
	clients             int64           // TTY clients attached now
	users               map[string]bool // who attached (forUser): noTerminal ends it for them too
	timers              []*time.Timer
}

// endedAt is when it ended (0: running).
func (e *execRec) endedAt() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ended
}

// info is the exec as answered.
func (e *execRec) info() Exec {
	e.mu.Lock()
	defer e.mu.Unlock()
	x := Exec{ID: e.id, Label: e.label, Cmd: e.cmd, Argv: e.argv, Cwd: e.cwd, TTY: e.tty, State: e.state,
		ExitCode: e.exitCode, Signal: e.signal, Started: e.started, Total: e.ring.Total(),
		ClientID: e.clientID, ForUser: e.forUser, UID: e.uid}
	if e.ended > 0 {
		ended := e.ended
		x.Ended = &ended
	}
	return x
}

// running reports that it hasn't ended.
func (e *execRec) running() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state == ExecRunning
}

// ttyExit is the exit frame its end gives a TTY.
func (e *execRec) ttyExit() termwire.Exit {
	e.mu.Lock()
	defer e.mu.Unlock()
	switch {
	case e.exitCode != nil:
		return termwire.ExitCode(*e.exitCode)
	case e.signal != "":
		return termwire.ExitSignal(e.signal)
	}
	return termwire.Exit{}
}

// signalGroup sends sig to its session (group: its whole process group).
// Bounded: a sandbox that stopped reading its control connection answers
// unavailable.
func (e *execRec) signalGroup(sig syscall.Signal, group bool) error {
	c := e.r.client()
	if c == nil {
		return refuse(RefState, "the sandbox stopped")
	}
	return c.Signal(e.sess.ID(), int(sig), group)
}

// writeIn writes p to its input: stdin, or a tty's terminal. A command that
// doesn't read it for inWait answers unavailable.
func (e *execRec) writeIn(p []byte) error {
	e.inMu.Lock()
	defer e.inMu.Unlock()
	if e.inClosed {
		return refuse(RefInvalid, "exec %s's stdin was closed (eof)", e.id)
	}
	for len(p) > 0 {
		_ = e.in.SetWriteDeadline(time.Now().Add(inWait))
		n, err := e.in.Write(p)
		p = p[n:]
		if err != nil {
			if !e.running() {
				return refuse(RefState, "exec %s has ended", e.id)
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				return &Error{Refusal: RefUnavailable, Msg: fmt.Sprintf("exec %s isn't reading its input", e.id), RetryAfter: time.Second}
			}
			return refuse(RefState, "exec %s's input is closed: %v", e.id, err)
		}
	}
	return nil
}

// closeIn sends its stdin's eof.
func (e *execRec) closeIn() error {
	e.inMu.Lock()
	defer e.inMu.Unlock()
	if e.inClosed {
		return nil
	}
	e.inClosed = true
	if cw, ok := e.in.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return e.in.Close()
}

// resize resizes a tty's terminal.
func (e *execRec) resize(rows, cols uint16) error {
	c := e.r.client()
	if c == nil {
		return refuse(RefState, "the sandbox stopped")
	}
	return c.Resize(e.sess.ID(), rows, cols)
}

// startExec starts a background exec: 201's exec, or — repeat — the one a
// clientId already names for the same request (200). An exec the agent
// can't start (a missing cwd, a program that isn't there) is invalid, and
// nothing is kept of it.
func (m *Manager) startExec(k Key, d *Def, req *ExecRequest, label string) (e *execRec, b *box, repeat bool, err error) {
	ex, err := m.checkExec(d, req)
	if err != nil {
		return nil, nil, false, err
	}
	if req.TTY && m.noTerminal(req.ForUser) {
		return nil, nil, false, refuse(RefNotAllowed, "%s may not use terminals (noTerminal)", req.ForUser)
	}
	if label == "" {
		label = req.Label
	}
	hb, _ := json.Marshal(req)
	e = &execRec{label: label, cmd: req.Cmd, argv: req.Argv, cwd: ex.Cwd, clientID: req.ClientID, reqHash: string(hb),
		forUser: req.ForUser, tty: req.TTY, stdin: req.Stdin && !req.TTY, uid: ex.UID,
		ready: make(chan struct{}), done: make(chan struct{}), pumped: make(chan struct{}), users: map[string]bool{}}
	if b, err = m.boxFor(k, d.Name); err != nil {
		return nil, nil, false, err
	}
	t := b.execs
	for {
		t.mu.Lock()
		if o := t.byClient[req.ClientID]; req.ClientID != "" && o != nil {
			t.mu.Unlock()
			if o.reqHash != e.reqHash {
				return nil, nil, false, refuse(RefExists, "clientId %q was used for a different exec", req.ClientID)
			}
			<-o.ready
			if o.failed {
				continue
			}
			return o, b, true, nil
		}
		err := t.admitLocked()
		if err == nil && req.ClientID != "" {
			t.byClient[req.ClientID] = e
		}
		t.mu.Unlock()
		if err != nil {
			return nil, nil, false, err
		}
		break
	}
	if err := m.launchExec(k, d, b, e, ex, req.TimeoutMs); err != nil {
		t.mu.Lock()
		t.running--
		if t.byClient[req.ClientID] == e {
			delete(t.byClient, req.ClientID)
		}
		t.mu.Unlock()
		e.failed = true
		close(e.ready)
		return nil, nil, false, err
	}
	return e, b, false, nil
}

// checkExec validates an exec request and builds its session.
func (m *Manager) checkExec(d *Def, req *ExecRequest) (proto.Exec, error) {
	switch {
	case req.TimeoutMs < 0:
		return proto.Exec{}, refuse(RefInvalid, "timeoutMs must not be negative")
	case len(req.Label) > maxLabel || hasControl(req.Label):
		return proto.Exec{}, refuse(RefInvalid, "label must be at most %d printable characters", maxLabel)
	case req.TTY && (req.Rows < 0 || req.Cols < 0 || req.Rows > 0xffff || req.Cols > 0xffff):
		return proto.Exec{}, refuse(RefInvalid, "rows and cols are 1…65535")
	}
	if err := checkClientID(req.ClientID); err != nil {
		return proto.Exec{}, err
	}
	ex, err := m.execOf(d, command{Cmd: req.Cmd, Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, UID: req.UID, GID: req.GID, ForUser: req.ForUser})
	if err != nil {
		return ex, err
	}
	if req.TTY {
		ex.TTY, ex.Rows, ex.Cols = true, 24, 80
		if req.Rows > 0 && req.Cols > 0 {
			ex.Rows, ex.Cols = uint16(req.Rows), uint16(req.Cols)
		}
	} else {
		ex.Merge, ex.NoStdin = true, !req.Stdin
	}
	return ex, nil
}

// launchExec runs e's session in k's sandbox (started first when it may
// be) and lists it once the agent says it runs.
func (m *Manager) launchExec(k Key, d *Def, b *box, e *execRec, ex proto.Exec, timeoutMs int64) error {
	m.touch(b)
	rb, r, err := m.ensureRunning(k, d.Name)
	if err != nil {
		return err
	}
	if rb != b {
		return refuse(RefNotFound, "sandbox %q was deleted", d.Name)
	}
	c := r.client()
	sess, streams, err := c.Exec(ex)
	if err != nil {
		return err
	}
	lim := m.limitsFor(k.Tile)
	e.b, e.r, e.sess = b, r, sess
	e.ring = newRing(m.ringBudgetFor(k.Tile, lim), lim.OutputRingMiB<<20)
	e.state, e.started = ExecRunning, m.now().UnixMilli()
	if ex.TTY {
		e.in, e.out = streams["pty"], streams["pty"]
		e.hub = termwire.NewHub(ttyReplay)
		e.hub.OnClients = func(n int) { m.ttyClients(b, e, n) }
	} else {
		e.in, e.out = streams["stdin"], streams["stdout"]
	}
	go m.pump(e)
	if err := waitStarted(c, sess); err != nil {
		for _, s := range streams {
			s.Close()
		}
		<-e.pumped
		e.ring.Drop()
		return err
	}
	release := func() {}
	if !ex.TTY { // a TTY's hold is its clients (OnClients)
		release = m.hold(b)
	}
	t := b.execs
	e.id = fmt.Sprintf("%s-%d", m.boot, m.execSeq.Add(1))
	t.mu.Lock()
	t.execs[e.id] = e
	t.order = append(t.order, e)
	t.pruneLocked(m.now().UnixMilli())
	t.mu.Unlock()
	close(e.ready)
	if timeoutMs > 0 {
		e.mu.Lock()
		e.timers = append(e.timers, time.AfterFunc(time.Duration(min(timeoutMs, maxTimeoutMs))*time.Millisecond, func() { e.timeout() }))
		e.mu.Unlock()
	}
	go m.watchExec(t, e, release)
	return nil
}

// waitStarted waits for the agent's "started": an "error" instead (a cwd
// that isn't there, a program that can't start) is invalid; a sandbox that
// stopped meanwhile is state; no answer within startWait is unavailable,
// and the session is given up.
func waitStarted(c *agentClient, sess *agentSession) error {
	t := time.NewTimer(startWait)
	defer t.Stop()
	select {
	case <-sess.Started():
	case <-t.C:
		c.abandon(sess, "no answer")
		return &Error{Refusal: RefUnavailable, Msg: fmt.Sprintf("the sandbox's agent didn't start the command within %s", startWait), RetryAfter: 5 * time.Second}
	}
	if sess.Pid() > 0 {
		return nil
	}
	x := sess.Exit()
	if x.Killed {
		return refuse(RefState, "the sandbox stopped")
	}
	return refuse(RefInvalid, "%s", x.Error)
}

// timeout is its timeoutMs passing: TERM to its group, then KILL after
// termGrace. It ends killed.
func (e *execRec) timeout() {
	e.mu.Lock()
	if e.state != ExecRunning {
		e.mu.Unlock()
		return
	}
	e.timedOut = true
	e.timers = append(e.timers, time.AfterFunc(termGrace, func() { _ = e.signalGroup(syscall.SIGKILL, true) }))
	e.mu.Unlock()
	_ = e.signalGroup(syscall.SIGTERM, true)
}

// pump reads its output stream into its ring (and a tty's hub) to the end.
func (m *Manager) pump(e *execRec) {
	defer close(e.pumped)
	buf := make([]byte, 32<<10)
	for {
		n, err := e.out.Read(buf)
		if n > 0 {
			e.ring.Write(buf[:n])
			if e.hub != nil {
				e.hub.Output(buf[:n])
			}
			m.touch(e.b)
		}
		if err != nil {
			return
		}
	}
}

// watchExec waits for its end and records it, once its output has been
// read to the end (bounded: a sandbox that stopped under it has nothing
// more to say).
func (m *Manager) watchExec(t *execTable, e *execRec, release func()) {
	<-e.sess.Done()
	x := e.sess.Exit()
	grace := drainWait
	if x.Killed {
		grace = killedDrain
	}
	g := time.NewTimer(grace)
	select {
	case <-e.pumped:
	case <-g.C:
	}
	g.Stop()
	e.out.Close()
	<-e.pumped
	if e.in != nil {
		e.in.Close()
	}
	e.finish(m.now().UnixMilli(), x)
	t.release()
	release()
	m.touch(e.b)
}

// finish records how it ended (§3.6): killed with its signal when a signal
// ended it — KILL when its sandbox stopped under it — and killed too when
// its timeout or a DELETE did; exited with its code otherwise.
func (e *execRec) finish(now int64, x SessionExit) {
	e.mu.Lock()
	for _, t := range e.timers {
		t.Stop()
	}
	e.timers = nil
	e.ended = now
	switch {
	case x.Killed:
		e.state, e.signal = ExecKilled, "KILL"
	case x.Signal > 0:
		e.state, e.signal = ExecKilled, signalWord(x.Signal)
	case x.Code < 0 || x.Error != "":
		e.state = ExecKilled
	default:
		code := x.Code
		e.exitCode, e.state = &code, ExecExited
		if e.timedOut || e.deleted {
			e.state = ExecKilled
		}
	}
	hub := e.hub
	e.hub = nil // a later attach replays the ring
	e.mu.Unlock()
	e.ring.End()
	if hub != nil {
		hub.End(e.ttyExit())
	}
	close(e.done)
}

// kill ends it: KILL to its process group. delete also forgets it now.
func (m *Manager) killExec(t *execTable, e *execRec, forget bool) error {
	e.mu.Lock()
	running := e.state == ExecRunning
	if running && forget {
		e.deleted = true
	}
	e.mu.Unlock()
	var err error
	if running {
		err = e.signalGroup(syscall.SIGKILL, true)
	}
	if forget {
		t.forget(e)
	}
	return err
}

// ringBudgetFor is the tile's ring budget, set to its policy now.
func (m *Manager) ringBudgetFor(tile string, lim Limits) *ringBudget {
	m.ringsMu.Lock()
	b := m.rings[tile]
	if b == nil {
		b = newRingBudget(int64(lim.OutputBudgetMiB) << 20)
		m.rings[tile] = b
	}
	m.ringsMu.Unlock()
	b.setMax(int64(lim.OutputBudgetMiB) << 20)
	return b
}

// noTerminal reports D88's noTerminal for a forUser claim.
func (m *Manager) noTerminal(user string) bool {
	return user != "" && m.deps.Users != nil && m.deps.Users.NoTerminal(user)
}
