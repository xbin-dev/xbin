package sidecar

import (
	"bufio"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/apptunnel/wire"
)

// Options configures New.
type Options struct {
	// RunDir is xbind's run directory: <RunDir>/iroh/ is where the binary
	// goes when a memfd can't be exec'd.
	RunDir string
	// Workspace is the workspace root: <ws>/.xbin/iroh/ is the last place
	// (a host that mounts /run noexec).
	Workspace string
	// Isolated is --isolate: the sidecar then runs in its sandbox or not
	// at all (D78). Off, it is still sandboxed where user namespaces work,
	// and runs unconfined only where they don't.
	Isolated bool
	// Binary runs this file instead of the embedded sidecar; "" means
	// $XBIN_IROH, then the embedded one.
	Binary string
	// Log receives the supervisor's events and the sidecar's stderr.
	Log *slog.Logger

	// test seams
	noSandbox  bool          // run unconfined even where a sandbox would start
	minBackoff time.Duration // the first restart delay (1 s)
	extraEnv   []string
}

const (
	maxBackoff   = 60 * time.Second
	healthyAfter = 5 * time.Minute // a run this long resets the backoff
	termGrace    = 3 * time.Second // SIGTERM, then SIGKILL
	headerWait   = 5 * time.Second // a stream's header must arrive within
	handoffWait  = 10 * time.Second
	statusEvery  = 30 * time.Second
)

// Supervisor runs the sidecar process and implements Runner.
type Supervisor struct {
	o       Options
	log     *slog.Logger
	streams chan Stream
	stopCh  chan struct{}
	wg      sync.WaitGroup // stream handoffs

	mu        sync.Mutex
	cfg       Config
	allowSeq  uint64 // the version of cfg.Allow
	st        Status
	cancel    context.CancelFunc // the loop's; nil when none runs
	done      chan struct{}      // closed when the loop returns
	restart   chan struct{}      // a second Start: restart now
	proc      *proc              // the live process, nil between runs
	changed   chan struct{}      // closed and replaced on ack, ready, exit
	x         *exe
	unconfine bool // the sandbox proved unusable with isolation off
	stopped   bool

	selftestMu sync.Mutex
}

// New returns a supervisor; nothing runs until Start.
func New(o Options) *Supervisor {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.minBackoff == 0 {
		o.minBackoff = time.Second
	}
	s := &Supervisor{
		o:       o,
		log:     o.Log.With("component", "iroh-sidecar"),
		streams: make(chan Stream, 64),
		stopCh:  make(chan struct{}),
		restart: make(chan struct{}, 1),
		changed: make(chan struct{}),
	}
	s.st.State = StateDisabled
	if !o.available() {
		s.st.State = StateUnavailable
	}
	return s
}

var _ Runner = (*Supervisor)(nil)

// proc is one run of the sidecar.
type proc struct {
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	wmu      sync.Mutex
	initSeq  uint64 // the allow version its init carried
	ready    bool
	readyAt  time.Time
	acked    uint64
	exited   chan struct{}
	selftest chan wire.Event
	tail     []string // stderr's last lines
	tailMu   sync.Mutex
}

func (p *proc) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	p.wmu.Lock()
	defer p.wmu.Unlock()
	_, err = p.stdin.Write(append(b, '\n'))
	return err
}

func (p *proc) stderrTail() string {
	p.tailMu.Lock()
	defer p.tailMu.Unlock()
	return strings.Join(p.tail, "\n")
}

// broadcast wakes everyone waiting on s.changed. Under s.mu.
func (s *Supervisor) broadcast() {
	close(s.changed)
	s.changed = make(chan struct{})
}

// Start implements Runner.
func (s *Supervisor) Start(ctx context.Context, cfg Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return errors.New("app tunnel: the sidecar supervisor is stopped")
	}
	if !s.o.available() {
		s.st.State = StateUnavailable
		return ErrUnavailable
	}
	s.cfg = Config{Key: cfg.Key, Relay: cfg.Relay, Allow: slices.Clone(cfg.Allow), Pairing: cfg.Pairing}
	s.allowSeq++
	if s.cancel != nil {
		select {
		case s.restart <- struct{}{}:
		default:
		}
		return nil
	}
	lctx, cancel := context.WithCancel(ctx)
	s.cancel, s.done = cancel, make(chan struct{})
	s.st.State, s.st.Error = StateStarting, ""
	go s.loop(lctx, s.done)
	return nil
}

func (s *Supervisor) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	backoff := s.o.minBackoff
	from := "" // the extraction kind to start from after an exec failure
	for first := true; ; first = false {
		s.mu.Lock()
		if !first {
			s.st.Restarts++
		}
		s.st.State = StateStarting
		s.mu.Unlock()
		res := s.runOnce(ctx, from)
		from = res.nextExec
		if ctx.Err() != nil {
			s.mu.Lock()
			s.st.State, s.cancel = StateDisabled, nil
			s.mu.Unlock()
			return
		}
		if res.restart {
			backoff = s.o.minBackoff
			continue
		}
		if res.healthy {
			backoff = s.o.minBackoff
		}
		s.mu.Lock()
		s.st.State, s.st.Error = StateFailed, res.err.Error()
		s.mu.Unlock()
		s.log.Warn("iroh sidecar stopped; restarting", "err", res.err, "in", backoff)
		if res.retryNow {
			continue
		}
		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			s.mu.Lock()
			s.st.State, s.cancel = StateDisabled, nil
			s.mu.Unlock()
			return
		case <-s.restart:
			t.Stop()
			backoff = s.o.minBackoff
			continue
		case <-t.C:
		}
		backoff = nextBackoff(backoff)
	}
}

// nextBackoff doubles d up to maxBackoff.
func nextBackoff(d time.Duration) time.Duration { return min(d*2, maxBackoff) }

type runResult struct {
	err      error
	restart  bool   // a second Start asked for it
	healthy  bool   // it ran long enough to reset the backoff
	retryNow bool   // switch something and retry at once (exec fallback, unconfined)
	nextExec string // the extraction kind for the next run
}

func (s *Supervisor) runOnce(ctx context.Context, from string) runResult {
	s.mu.Lock()
	x := s.x
	if x == nil || from != "" {
		x.close()
		var err error
		if x, err = s.o.openExe(from); err != nil {
			s.x = nil
			s.mu.Unlock()
			return runResult{err: err}
		}
		s.x = x
	}
	cfg, seq, unconfine := s.cfg, s.allowSeq, s.unconfine
	s.mu.Unlock()

	l, err := s.launch(x, unconfine)
	if err != nil {
		if !s.o.Isolated && !unconfine && l.sandboxed {
			s.log.Warn("iroh sidecar: the sandbox won't start and isolation is off; running it unconfined", "err", err)
			s.mu.Lock()
			s.unconfine = true
			s.mu.Unlock()
			return runResult{err: err, retryNow: true}
		}
		if l.execFailed && nextKind(x.how) != "" {
			return runResult{err: err, retryNow: true, nextExec: nextKind(x.how)}
		}
		return runResult{err: err}
	}
	p := &proc{cmd: l.cmd, stdin: l.stdin, initSeq: seq, exited: make(chan struct{}), selftest: make(chan wire.Event, 1)}
	s.mu.Lock()
	s.proc = p
	s.st.Confined, s.st.Exec = l.sandboxed, x.how
	s.st.EndpointID, s.st.Ticket, s.st.HomeRelay, s.st.RelayConnected, s.st.Conns = "", "", "", false, nil
	s.mu.Unlock()

	var readers sync.WaitGroup
	readers.Add(3)
	go func() { defer readers.Done(); s.readEvents(p, l.stdout, cfg.Relay.Mode) }()
	go func() { defer readers.Done(); s.readStderr(p, l.stderr) }()
	go func() { defer readers.Done(); s.acceptStreams(l.fac) }()
	go func() { _ = p.cmd.Wait(); close(p.exited) }()

	init := wire.Init{T: "init", V: 1, SecretKey: hex.EncodeToString(cfg.Key.Seed()), Relay: cfg.Relay, Allow: cfg.Allow, Pairing: cfg.Pairing}
	if cfg.Allow == nil {
		init.Allow = []string{}
	}
	_ = p.send(init)

	tick := time.NewTicker(statusEvery)
	defer tick.Stop()
	var res runResult
wait:
	for {
		select {
		case <-p.exited:
			break wait
		case <-tick.C:
			_ = p.send(wire.Command{T: "status"})
		case <-ctx.Done():
			s.terminate(p)
			break wait
		case <-s.restart:
			res.restart = true
			s.terminate(p)
			break wait
		}
	}
	<-p.exited
	l.cleanup()
	p.stdin.Close()
	l.fac.Close() // ends acceptStreams
	readers.Wait()

	s.mu.Lock()
	ready, readyAt := p.ready, p.readyAt
	s.proc = nil
	s.st.Conns, s.st.RelayConnected = nil, false
	s.broadcast()
	s.mu.Unlock()
	res.healthy = ready && time.Since(readyAt) >= healthyAfter
	tail := p.stderrTail()
	res.err = fmt.Errorf("xbin-iroh exited (%v)%s", p.cmd.ProcessState, lastLine(tail))
	if !ready && strings.Contains(tail, "sandbox-init: exec /proc/self/fd/") && nextKind(x.how) != "" {
		// the kernel or an LSM won't exec this copy: the next place
		res.retryNow, res.nextExec = true, nextKind(x.how)
	} else if !ready && l.sandboxed && !s.o.Isolated && strings.Contains(tail, "sandbox-init:") {
		// the namespaces were made, but the sandbox couldn't be set up in
		// them (an LSM's userns restriction): isolation is off, so run it
		// as tiles already run
		s.log.Warn("iroh sidecar: the sandbox won't set up and isolation is off; running it unconfined", "err", lastLine(tail))
		s.mu.Lock()
		s.unconfine = true
		s.mu.Unlock()
		res.retryNow = true
	}
	return res
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return ": " + s
}

// terminate: SIGTERM (the sidecar, PID 1 of its pid namespace, handles
// it), then SIGKILL after termGrace.
func (s *Supervisor) terminate(p *proc) {
	_ = signalTerm(p.cmd)
	select {
	case <-p.exited:
	case <-time.After(termGrace):
		_ = p.cmd.Process.Kill()
	}
}

func (s *Supervisor) readEvents(p *proc, r io.Reader, mode string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var ev wire.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			s.log.Warn("iroh sidecar: a bad control line", "err", err)
			continue
		}
		s.onEvent(p, ev, mode)
	}
}

func (s *Supervisor) onEvent(p *proc, ev wire.Event, mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch ev.T {
	case "ready":
		s.st.EndpointID, s.st.Ticket, s.st.HomeRelay, s.st.Version = ev.EndpointID, ev.Ticket, ev.HomeRelay, ev.Version
		if !p.ready {
			p.ready, p.readyAt = true, time.Now()
			s.st.Error = ""
			s.setRunning(mode)
			s.log.Info("iroh sidecar ready", "endpoint", ev.EndpointID, "relay", ev.HomeRelay, "version", ev.Version, "confined", s.st.Confined, "exec", s.st.Exec)
			// the list and pairing as they are now: they may have moved
			// since the init was written
			allow, seq, pairing := slices.Clone(s.cfg.Allow), s.allowSeq, s.cfg.Pairing
			go func() {
				_ = p.send(wire.Command{T: "allow", Seq: seq, Allow: allow})
				_ = p.send(wire.Command{T: "pairing", Open: pairing})
			}()
			s.broadcast()
		}
	case "ack":
		if ev.Seq > p.acked {
			p.acked = ev.Seq
			s.broadcast()
		}
	case "status":
		s.st.HomeRelay, s.st.RelayConnected = ev.HomeRelay, ev.RelayConnected
		s.st.Conns = slices.Clone(ev.Conns)
		if p.ready {
			s.setRunning(mode)
		}
	case "conn":
		s.st.Conns = slices.DeleteFunc(s.st.Conns, func(c wire.ConnInfo) bool { return c.Conn == ev.Conn })
		if ev.Open {
			s.st.Conns = append(s.st.Conns, wire.ConnInfo{Endpoint: ev.Endpoint, Conn: ev.Conn, Path: ev.Path})
		}
	case "reject":
		s.log.Info("iroh sidecar refused a connection", "endpoint", ev.Endpoint, "alpn", ev.ALPN)
	case "selftest":
		select {
		case p.selftest <- ev:
		default:
		}
	case "error":
		if ev.Fatal {
			s.st.Error = ev.Msg
		}
		s.log.Warn("iroh sidecar", "error", ev.Msg, "fatal", ev.Fatal)
	}
}

// setRunning: running, or degraded while a relay it should have isn't
// connected. Under s.mu.
func (s *Supervisor) setRunning(mode string) {
	if mode != "disabled" && !s.st.RelayConnected {
		s.st.State = StateDegraded
	} else {
		s.st.State = StateRunning
	}
}

func (s *Supervisor) readStderr(p *proc, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		p.tailMu.Lock()
		p.tail = append(p.tail, line)
		if len(p.tail) > 20 {
			p.tail = p.tail[len(p.tail)-20:]
		}
		p.tailMu.Unlock()
		s.log.Info("iroh sidecar", "log", line)
	}
}

// acceptStreams takes each stream the sidecar hands over, checks its
// header and passes it on, until the factory ends.
func (s *Supervisor) acceptStreams(fac interface{ Accept() (net.Conn, error) }) {
	for {
		c, err := fac.Accept()
		if err != nil {
			return
		}
		s.wg.Add(1)
		go s.handoff(c)
	}
}

func (s *Supervisor) handoff(c net.Conn) {
	defer s.wg.Done()
	_ = c.SetReadDeadline(time.Now().Add(headerWait))
	h, err := wire.ReadHeader(c)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		s.log.Warn("iroh sidecar: a stream without a valid header", "err", err)
		c.Close()
		return
	}
	t := time.NewTimer(handoffWait)
	defer t.Stop()
	select {
	case s.streams <- Stream{Header: h, Conn: c}:
	case <-s.stopCh:
		c.Close()
	case <-t.C:
		s.log.Warn("iroh sidecar: nobody took a stream", "endpoint", h.EndpointHex())
		c.Close()
	}
}

// SetAllow implements Runner.
func (s *Supervisor) SetAllow(ctx context.Context, ids []string) error {
	s.mu.Lock()
	s.cfg.Allow = slices.Clone(ids)
	s.allowSeq++
	seq, p := s.allowSeq, s.proc
	s.mu.Unlock()
	if p != nil {
		allow := slices.Clone(ids)
		if allow == nil {
			allow = []string{}
		}
		_ = p.send(wire.Command{T: "allow", Seq: seq, Allow: allow})
	}
	for {
		s.mu.Lock()
		cur := s.proc
		applied := cur == nil || // nothing runs to refuse anyone; the next init carries the list
			cur.acked >= seq || (cur.ready && cur.initSeq >= seq)
		ch := s.changed
		s.mu.Unlock()
		if applied {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// SetPairing implements Runner.
func (s *Supervisor) SetPairing(open bool) {
	s.mu.Lock()
	s.cfg.Pairing = open
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		_ = p.send(wire.Command{T: "pairing", Open: open})
	}
}

// Drop implements Runner.
func (s *Supervisor) Drop(endpoint string) {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		_ = p.send(wire.Command{T: "close", Endpoint: endpoint})
	}
}

// SelfTest implements Runner.
func (s *Supervisor) SelfTest(ctx context.Context) (wire.Event, error) {
	s.selftestMu.Lock()
	defer s.selftestMu.Unlock()
	s.mu.Lock()
	p := s.proc
	ready := p != nil && p.ready
	s.mu.Unlock()
	if !ready {
		return wire.Event{}, errors.New("app tunnel: the iroh sidecar is not running")
	}
	select {
	case <-p.selftest: // a stale answer
	default:
	}
	if err := p.send(wire.Command{T: "selftest"}); err != nil {
		return wire.Event{}, err
	}
	select {
	case ev := <-p.selftest:
		return ev, nil
	case <-p.exited:
		return wire.Event{}, errors.New("app tunnel: the iroh sidecar exited during the selftest")
	case <-ctx.Done():
		return wire.Event{}, ctx.Err()
	}
}

// Streams implements Runner.
func (s *Supervisor) Streams() <-chan Stream { return s.streams }

// Status implements Runner.
func (s *Supervisor) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.st
	st.Conns = slices.Clone(s.st.Conns)
	return st
}

// Stop implements Runner: the process gets SIGTERM (SIGKILL 3 s later),
// streams already handed over stay open, Streams is closed.
func (s *Supervisor) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	close(s.stopCh)
	s.wg.Wait()
	close(s.streams)
	s.mu.Lock()
	s.x.close()
	s.x = nil
	if s.st.State != StateUnavailable {
		s.st.State = StateDisabled
	}
	s.mu.Unlock()
}
