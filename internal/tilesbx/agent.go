package tilesbx

// agent.go — the agent client: xbind's side of the wire to a running tile
// sandbox's agent (internal/sandbox/vm/proto; plans/tile-sandbox-runtime.md
// §2). One control connection per run, read by one goroutine; one more
// connection per session stream and per file operation, each dialled
// through the run's connection factory and opened by its Hello.
//
// Nothing the agent says is trusted (§2.6): every event line is bounded
// (proto.MaxEvent), an event for a session this client didn't open — or
// one that already ended — is dropped, and so is a "synced" nobody asked
// for. Session ids are allocated here, from 2, and never reused within a
// run. A control connection that ends while the client is open is an
// incident: the run is ended (lost).

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// dialRetry bounds how long a dial waits out a full factory queue: a burst
// of dials past net.unix.max_dgram_qlen (512 under systemd, 10 on a bare
// kernel) is EAGAIN, not a wedged agent (WP-4's note).
const dialRetry = 2 * time.Second

// firstSession is the first session a tile sandbox's execs get: 1 is a
// backend's or a terminal's (§2.6).
const firstSession = 2

// agentClient is one run's connection to its agent.
type agentClient struct {
	fac    *sandbox.Factory
	ctl    *proto.Conn
	sendMu sync.Mutex // one control line at a time
	logf   func(string, ...any)

	mu       sync.Mutex
	next     int // the next session id
	sessions map[int]*agentSession
	syncs    []chan struct{} // "sync"s waiting for "synced", oldest first
	closed   bool

	readyOnce sync.Once
	ready     chan struct{} // closed at the first "ready"
	gone      chan struct{} // closed when the control reader has ended
}

// dialAgent opens one connection over a factory, waiting out a full queue
// for at most dialRetry. Any other failure — the agent gone, the host's
// in-flight fd limit (ETOOMANYREFS, §2.6) — is unavailable at once.
func dialAgent(fac *sandbox.Factory) (net.Conn, error) {
	deadline := time.Now().Add(dialRetry)
	for delay := time.Millisecond; ; delay = min(2*delay, 50*time.Millisecond) {
		c, err := fac.Dial()
		if err == nil {
			return c, nil
		}
		if !errors.Is(err, syscall.EAGAIN) || time.Now().After(deadline) {
			return nil, &Error{Refusal: RefUnavailable, Msg: "the sandbox's agent can't be reached: " + err.Error(), RetryAfter: time.Second}
		}
		time.Sleep(delay)
	}
}

// open dials one connection and sends its Hello.
func (a *agentClient) open(h proto.Hello) (net.Conn, error) {
	c, err := dialAgent(a.fac)
	if err != nil {
		return nil, err
	}
	if err := proto.NewConn(c, nil).Send(h); err != nil {
		c.Close()
		return nil, &Error{Refusal: RefUnavailable, Msg: "the sandbox's agent can't be reached: " + err.Error(), RetryAfter: time.Second}
	}
	return c, nil
}

// connectAgent opens the run's control connection and starts reading it.
// lost is called once, from the reader, when the connection ends while the
// client is still open.
func connectAgent(fac *sandbox.Factory, logf func(string, ...any), lost func(error)) (*agentClient, error) {
	a := &agentClient{fac: fac, logf: logf, next: firstSession, sessions: map[int]*agentSession{},
		ready: make(chan struct{}), gone: make(chan struct{})}
	c, err := a.open(proto.Hello{Kind: "ctl"})
	if err != nil {
		return nil, err
	}
	a.ctl = proto.NewConn(c, nil)
	go a.read(lost)
	return a, nil
}

func (a *agentClient) read(lost func(error)) {
	defer close(a.gone)
	for {
		var m proto.Msg
		if err := a.ctl.RecvMax(&m, proto.MaxEvent); err != nil {
			a.mu.Lock()
			closed := a.closed
			a.mu.Unlock()
			if !closed && lost != nil {
				lost(err)
			}
			return
		}
		a.event(m)
	}
}

// event routes one line from the agent.
func (a *agentClient) event(m proto.Msg) {
	switch m.Op {
	case "ready":
		a.readyOnce.Do(func() { close(a.ready) })
	case "synced":
		a.mu.Lock()
		if len(a.syncs) > 0 {
			close(a.syncs[0])
			a.syncs = a.syncs[1:]
		}
		a.mu.Unlock()
	case "started", "exited", "error":
		if m.Session == 0 {
			if m.Op == "error" {
				a.logf("the agent: %s", m.Error)
			}
			return
		}
		a.mu.Lock()
		s := a.sessions[m.Session]
		a.mu.Unlock()
		if s != nil { // a session this run opened and that hasn't ended
			s.event(a, m)
		}
	}
}

// WaitReady waits for the agent's "ready": at most d, and not past the
// run's end (ended closes when its process is gone).
func (a *agentClient) WaitReady(d time.Duration, ended <-chan struct{}) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-a.ready:
		return nil
	case <-a.gone:
		return errors.New("its agent closed the control connection before it was ready")
	case <-ended:
		return errors.New("it exited before its agent was ready")
	case <-t.C:
		return fmt.Errorf("its agent wasn't ready within %s", d)
	}
}

// send writes one control line.
func (a *agentClient) send(m proto.Msg) error {
	a.sendMu.Lock()
	defer a.sendMu.Unlock()
	if err := a.ctl.Send(m); err != nil {
		return &Error{Refusal: RefUnavailable, Msg: "the sandbox's agent can't be reached: " + err.Error(), RetryAfter: time.Second}
	}
	return nil
}

// ctlSendWait bounds a control line's send (an exec, a signal, a resize);
// a variable for the tests.
var ctlSendWait = 10 * time.Second

// sendWithin is send, bounded by d: the agent runs a sync inline in its
// control loop, so over a wedged root it stops reading, and a line past the
// socket's buffer (an exec's argv and env) would block the request for
// good. Past d it is unavailable; the send left behind ends when the
// teardown closes the connection.
func (a *agentClient) sendWithin(m proto.Msg, d time.Duration) error {
	sent := make(chan error, 1)
	go func() { sent <- a.send(m) }()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case err := <-sent:
		return err
	case <-a.gone:
		return &Error{Refusal: RefUnavailable, Msg: "the sandbox's agent closed its control connection", RetryAfter: time.Second}
	case <-t.C:
		return &Error{Refusal: RefUnavailable, Msg: fmt.Sprintf("the sandbox's agent isn't reading its control connection (%s)", d), RetryAfter: 5 * time.Second}
	}
}

// Sync asks the agent to flush the sandbox's filesystem and waits for its
// "synced", at most d. A fuse-overlayfs a session stopped can wedge the
// flush (WP-3's note): a stop gives up after d and kills the sandbox anyway.
// The send is inside the bound too: an agent whose control loop is stuck (a
// syncfs or a spawn on a wedged root) stops reading, and a send already
// blocked on the full connection holds sendMu. A send left behind ends when
// the teardown closes the connection.
func (a *agentClient) Sync(d time.Duration) error {
	w := make(chan struct{})
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return errors.New("the agent is gone")
	}
	a.syncs = append(a.syncs, w)
	a.mu.Unlock()
	t := time.NewTimer(d)
	defer t.Stop()
	sent := make(chan error, 1)
	go func() { sent <- a.send(proto.Msg{Op: "sync"}) }()
	for {
		select {
		case err := <-sent:
			if err != nil {
				return err
			}
			sent = nil
		case <-w:
			return nil
		case <-a.gone:
			return errors.New("the agent closed its control connection")
		case <-t.C:
			return fmt.Errorf("no synced within %s", d)
		}
	}
}

// Close ends the client: the control connection is closed (no incident),
// and every session still open ends as killed — the sandbox stopped under
// it (§3.6). Idempotent.
func (a *agentClient) Close() {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return
	}
	a.closed = true
	open := make([]*agentSession, 0, len(a.sessions))
	for _, s := range a.sessions {
		open = append(open, s)
	}
	a.mu.Unlock()
	if a.ctl != nil {
		a.ctl.Close()
	}
	for _, s := range open {
		s.finish(a, SessionExit{Killed: true, Code: -1})
	}
}

// streamsOf is the streams an exec expects, in the order they are dialled
// (agentcore's rule: a pty; or stdin unless NoStdin, stdout, and stderr
// unless Merge).
func streamsOf(ex proto.Exec) []string {
	if ex.TTY {
		return []string{"pty"}
	}
	var s []string
	if !ex.NoStdin {
		s = append(s, "stdin")
	}
	s = append(s, "stdout")
	if !ex.Merge {
		s = append(s, "stderr")
	}
	return s
}

// Exec starts one session: its id is allocated here, its streams are
// dialled first (the agent waits for them), then the exec is sent. The
// streams are the caller's to close.
func (a *agentClient) Exec(ex proto.Exec) (*agentSession, map[string]net.Conn, error) {
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil, nil, refuse(RefState, "the sandbox stopped")
	}
	ex.Session = a.next
	a.next++
	s := &agentSession{id: ex.Session, started: make(chan struct{}), done: make(chan struct{})}
	a.sessions[s.id] = s
	a.mu.Unlock()
	streams := map[string]net.Conn{}
	fail := func(err error) (*agentSession, map[string]net.Conn, error) {
		for _, c := range streams {
			c.Close()
		}
		s.finish(a, SessionExit{Error: err.Error(), Code: -1})
		return nil, nil, err
	}
	for _, name := range streamsOf(ex) {
		c, err := a.open(proto.Hello{Kind: "stream", Session: s.id, Stream: name})
		if err != nil {
			return fail(err)
		}
		streams[name] = c
	}
	if err := a.sendWithin(proto.Msg{Op: "exec", Exec: &ex}, ctlSendWait); err != nil {
		return fail(err)
	}
	return s, streams, nil
}

// abandon gives a session up (its "started" never came): it ends with why,
// and whatever the agent says of it later is dropped.
func (a *agentClient) abandon(s *agentSession, why string) {
	s.finish(a, SessionExit{Error: why, Code: -1})
}

// Signal signals a session (group: its whole process group). Bounded, as
// every control line but a sync is (sendWithin).
func (a *agentClient) Signal(session, sig int, group bool) error {
	return a.sendWithin(proto.Msg{Op: "signal", Session: session, Signal: sig, Group: group}, ctlSendWait)
}

// Resize resizes a tty session's terminal (bounded).
func (a *agentClient) Resize(session int, rows, cols uint16) error {
	return a.sendWithin(proto.Msg{Op: "resize", Session: session, Rows: rows, Cols: cols}, ctlSendWait)
}

// File opens one file operation's connection (proto/file.go's framing
// follows its Hello). The connection is the caller's to close.
func (a *agentClient) File(op proto.FileOp) (*proto.Conn, error) {
	c, err := a.open(proto.Hello{Kind: "file", File: &op})
	if err != nil {
		return nil, err
	}
	return proto.NewConn(c, nil), nil
}

// SessionExit is how a session ended.
type SessionExit struct {
	Code   int    // its exit status (128+signal when a signal ended it); -1 when it never ran or was cut off
	Signal int    // > 0: the signal that ended it
	Error  string // the agent refused or failed to start it
	Killed bool   // the sandbox stopped under it (§3.6: "killed", signal KILL)
}

// agentSession is one exec's session, as the agent reports it.
type agentSession struct {
	id      int
	started chan struct{} // closed at "started" (or at the end, if it never started)
	done    chan struct{} // closed at the end

	mu   sync.Mutex
	pid  int
	exit SessionExit
	fin  bool
}

func (s *agentSession) event(a *agentClient, m proto.Msg) {
	switch m.Op {
	case "started":
		s.mu.Lock()
		if !s.fin && s.pid == 0 {
			s.pid = max(m.Pid, 1)
			close(s.started)
		}
		s.mu.Unlock()
	case "exited":
		s.finish(a, SessionExit{Code: m.Code, Signal: max(m.Signal, 0)})
	case "error":
		s.finish(a, SessionExit{Error: m.Error, Code: -1})
	}
}

// finish records the end once and forgets the session: later events for its
// id are dropped.
func (s *agentSession) finish(a *agentClient, e SessionExit) {
	s.mu.Lock()
	if s.fin {
		s.mu.Unlock()
		return
	}
	s.fin, s.exit = true, e
	if s.pid == 0 {
		close(s.started)
	}
	s.mu.Unlock()
	a.mu.Lock()
	if a.sessions[s.id] == s {
		delete(a.sessions, s.id)
	}
	a.mu.Unlock()
	close(s.done)
}

// ID is the session's number.
func (s *agentSession) ID() int { return s.id }

// Started is closed once it runs (or ended without running).
func (s *agentSession) Started() <-chan struct{} { return s.started }

// Done is closed once it ended.
func (s *agentSession) Done() <-chan struct{} { return s.done }

// Pid is its pid in the sandbox (0: not started).
func (s *agentSession) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.fin || s.pid > 0 {
		return s.pid
	}
	return 0
}

// Exit is how it ended (valid once Done is closed).
func (s *agentSession) Exit() SessionExit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.exit
}
