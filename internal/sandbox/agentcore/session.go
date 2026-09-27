//go:build linux

package agentcore

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// session is one process the peer asked for, with its byte streams. Its
// streams may connect before or after its exec; the process starts once
// both have arrived, so no output is lost.
type session struct {
	c  *Core
	id int

	// execReceived is written under c.mu and s.mu, so either lock reads it.
	execReceived bool
	timer        *time.Timer // a phantom's pruning (c.mu)

	mu      sync.Mutex
	ex      proto.Exec  // set once, with execReceived
	started bool        // run took the streams: no more attach
	proc    *os.Process // until it exited
	pid     int         // its process (group), until the session ends
	ptmx    *os.File    // tty sessions
	streams map[string]io.ReadWriteCloser
	arrived chan struct{} // closed once the exec and every stream it expects are here
}

// streamsOf names the streams a session's process needs attached.
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

func validStream(name string) bool {
	switch name {
	case "pty", "stdin", "stdout", "stderr":
		return true
	}
	return false
}

// newSessionLocked registers an empty session (c.mu held).
func (c *Core) newSessionLocked(id int) *session {
	s := &session{c: c, id: id, streams: map[string]io.ReadWriteCloser{}, arrived: make(chan struct{})}
	c.sessions[id] = s
	return s
}

// startSession takes an exec: the session starts once its streams are in.
func (c *Core) startSession(ex proto.Exec) error {
	switch {
	case len(ex.Argv) == 0:
		return errors.New("exec: empty argv")
	case ex.Session <= 0:
		return errors.New("exec: no session number")
	case ex.Gateway != "" && c.o.Gateway == nil:
		return errors.New("exec: this sandbox has no gateway")
	}
	c.mu.Lock()
	if !c.configured {
		c.mu.Unlock()
		return errors.New("exec before config")
	}
	if c.retired[ex.Session] {
		c.mu.Unlock()
		return fmt.Errorf("session %d has already run", ex.Session)
	}
	s := c.sessions[ex.Session]
	if s != nil && s.execReceived {
		c.mu.Unlock()
		return fmt.Errorf("session %d is already running", ex.Session)
	}
	if live, _ := c.countsLocked(); live >= c.o.MaxSessions {
		if s != nil { // its streams came for nothing
			c.retireLocked(ex.Session, s)
		}
		c.mu.Unlock()
		if s != nil {
			s.closeStreams()
		}
		return fmt.Errorf("too many sessions (%d running)", c.o.MaxSessions)
	}
	if s == nil {
		s = c.newSessionLocked(ex.Session)
	}
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	s.mu.Lock()
	s.ex = ex
	s.execReceived = true
	s.mu.Unlock()
	c.mu.Unlock()
	go s.await()
	return nil
}

// attachStream hands a stream connection to its session, creating the
// session when the stream is first (a phantom, pruned unless its exec
// follows within StreamWait).
func (c *Core) attachStream(h proto.Hello, conn streamConn) {
	if h.Session <= 0 || !validStream(h.Stream) {
		conn.Close()
		return
	}
	c.mu.Lock()
	if c.retired[h.Session] {
		c.mu.Unlock()
		conn.Close()
		return
	}
	s := c.sessions[h.Session]
	if s == nil {
		if _, phantoms := c.countsLocked(); phantoms >= c.o.MaxSessions {
			c.mu.Unlock()
			c.o.Logf("session %d: a stream with no exec, and %d such sessions already: dropped", h.Session, phantoms)
			conn.Close()
			return
		}
		s = c.newSessionLocked(h.Session)
		s.timer = time.AfterFunc(c.o.StreamWait, func() { c.prunePhantom(s) })
	} else if s.timer != nil {
		s.timer.Reset(c.o.StreamWait)
	}
	c.mu.Unlock()
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		conn.Close() // too late: the process has its streams
		return
	}
	if old := s.streams[h.Stream]; old != nil {
		old.Close()
	}
	s.streams[h.Stream] = conn
	s.mu.Unlock()
	s.checkArrived()
}

// prunePhantom drops a session whose exec never came.
func (c *Core) prunePhantom(s *session) {
	c.mu.Lock()
	if c.sessions[s.id] != s || s.execReceived {
		c.mu.Unlock()
		return
	}
	c.retireLocked(s.id, s)
	c.mu.Unlock()
	s.closeStreams()
	c.o.Logf("session %d: streams but no exec within %s: pruned", s.id, c.o.StreamWait)
}

func (s *session) checkArrived() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.execReceived {
		return
	}
	for _, name := range streamsOf(s.ex) {
		if s.streams[name] == nil {
			return
		}
	}
	select {
	case <-s.arrived:
	default:
		close(s.arrived)
	}
}

// await runs the session once its streams are in, or gives up on them.
func (s *session) await() {
	s.checkArrived()
	t := time.NewTimer(s.c.o.StreamWait)
	defer t.Stop()
	select {
	case <-s.arrived:
	case <-t.C:
		s.fail(errors.New("streams did not attach"))
		return
	}
	if err := s.run(); err != nil {
		s.fail(err)
	}
}

// fail ends a session that never ran: it is forgotten, its streams closed,
// and the peer told.
func (s *session) fail(err error) {
	s.c.mu.Lock()
	s.c.retireLocked(s.id, s)
	s.c.mu.Unlock()
	s.closeStreams()
	s.c.send(proto.Msg{Op: "error", Session: s.id, Error: err.Error()})
}

func (s *session) closeStreams() {
	s.mu.Lock()
	s.started = true
	for _, c := range s.streams {
		c.Close()
	}
	s.mu.Unlock()
}

// run starts the process, reports it, waits for it, and reports its end.
func (s *session) run() error {
	ex := s.ex // set before await started
	prog := ex.Path
	if prog == "" {
		prog = ex.Argv[0]
	}
	env := sessionEnv(ex.Env)
	argv0, err := lookPath(prog, env)
	if err != nil {
		return err
	}
	cwd, err := sessionCwd(ex)
	if err != nil {
		return err
	}
	cred, err := credential(ex)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.started = true
	st := map[string]io.ReadWriteCloser{}
	for name, c := range s.streams {
		st[name] = c
	}
	s.mu.Unlock()
	want := map[string]bool{}
	for _, name := range streamsOf(ex) {
		want[name] = true
	}
	for name, c := range st {
		if !want[name] {
			c.Close() // a stream this exec doesn't use (stderr with Merge)
		}
	}
	sys := &syscall.SysProcAttr{Setsid: true, Credential: cred} // pgid = pid: Group signals
	attr := &os.ProcAttr{Dir: cwd, Env: env, Sys: sys}
	pipes, err := s.stdio(ex, attr, st)
	if err != nil {
		return err
	}
	if ex.Gateway != "" { // up before the backend, which may call it at start
		if err := s.c.o.Gateway(ex.Gateway); err != nil {
			s.closePipes(pipes)
			return fmt.Errorf("gateway %s: %w", ex.Gateway, err)
		}
	}
	proc, done, err := s.c.o.Spawn.Start(argv0, ex.Argv, attr)
	pipes.started()
	if err != nil {
		s.closePipes(pipes)
		return fmt.Errorf("exec %s: %w", ex.Argv[0], err)
	}
	s.mu.Lock()
	s.proc, s.pid = proc, proc.Pid
	s.mu.Unlock()
	pipes.copy()
	s.c.send(proto.Msg{Op: "started", Session: s.id, Pid: proc.Pid})
	stop := make(chan struct{})
	if ex.Listen != "" {
		go s.c.awaitListen(s.id, ex.Listen, stop)
	}
	ws, ok := <-done
	close(stop)
	s.mu.Lock()
	s.proc = nil // (the group may live on, and a Group signal still reaches it)
	_ = proc.Release()
	s.mu.Unlock()
	code, sig := 255, 0
	if ok {
		code = ws.ExitStatus()
		if ws.Signaled() {
			sig = int(ws.Signal())
			code = 128 + sig
		}
	}
	// drain output (bounded: a daemonized grandchild may keep the pipes or
	// the tty open), then cut what is left
	pipes.drain(2 * time.Second)
	s.closePipes(pipes)
	for _, c := range st {
		c.Close()
	}
	if !ex.NoSync && s.c.o.Sync != nil {
		s.c.o.Sync() // a VM is killed once the host hears "exited": the disk must hold everything
	}
	s.c.mu.Lock()
	s.c.retireLocked(s.id, s)
	s.c.mu.Unlock()
	s.c.send(proto.Msg{Op: "exited", Session: s.id, Code: code, Signal: sig})
	return nil
}

// stdio is a session's plumbing: the ends its process gets, the ends the
// agent copies through, and those copies.
type stdio struct {
	child, parent []*os.File
	copies        []func(*sync.WaitGroup)
	wg            sync.WaitGroup
}

// started closes the child's ends (it holds its own copies now, or failed).
func (p *stdio) started() {
	for _, f := range p.child {
		f.Close()
	}
	p.child = nil
}

// copy starts the copies between the streams and the process.
func (p *stdio) copy() {
	for _, c := range p.copies {
		c(&p.wg)
	}
}

// drain waits up to d for the output copies to end.
func (p *stdio) drain(d time.Duration) {
	done := make(chan struct{})
	go func() { p.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(d):
	}
}

// close closes every end still open (a copy blocked on one returns).
func (p *stdio) close() {
	p.started()
	for _, f := range p.parent {
		f.Close()
	}
}

// closePipes closes the session's ends (resize stops at the PTY first).
func (s *session) closePipes(p *stdio) {
	s.mu.Lock()
	s.ptmx = nil
	s.mu.Unlock()
	p.close()
}

// stdio wires the process to its streams: one PTY, or pipes — stdin (or
// /dev/null), stdout, and stderr (or stdout again, merged).
func (s *session) stdio(ex proto.Exec, attr *os.ProcAttr, st map[string]io.ReadWriteCloser) (*stdio, error) {
	p := &stdio{}
	output := func(dst io.ReadWriteCloser, src *os.File) func(*sync.WaitGroup) {
		return func(wg *sync.WaitGroup) {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = io.Copy(dst, src); halfClose(dst) }()
		}
	}
	if ex.TTY {
		ptmx, tty, err := pty.Open()
		if err != nil {
			return nil, fmt.Errorf("pty: %w", err)
		}
		if ex.Rows > 0 && ex.Cols > 0 {
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: ex.Rows, Cols: ex.Cols})
		}
		if cred := attr.Sys.Credential; cred != nil {
			_ = tty.Chown(int(cred.Uid), -1)
		}
		s.mu.Lock()
		s.ptmx = ptmx
		s.mu.Unlock()
		attr.Files = []*os.File{tty, tty, tty}
		attr.Sys.Setctty, attr.Sys.Ctty = true, 0
		p.child, p.parent = []*os.File{tty}, []*os.File{ptmx}
		c := st["pty"]
		p.copies = append(p.copies, func(*sync.WaitGroup) { go func() { _, _ = io.Copy(ptmx, c) }() },
			output(c, ptmx)) // output runs until the last holder of the tty closes it (EIO)
		return p, nil
	}
	var stdin *os.File
	if ex.NoStdin {
		f, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		stdin = f
	} else {
		inR, inW, err := os.Pipe()
		if err != nil {
			return nil, err
		}
		stdin = inR
		p.parent = append(p.parent, inW)
		in := st["stdin"]
		p.copies = append(p.copies, func(*sync.WaitGroup) {
			go func() { _, _ = io.Copy(inW, in); inW.Close() }()
		})
	}
	p.child = append(p.child, stdin)
	outR, outW, err := os.Pipe()
	if err != nil {
		p.close()
		return nil, err
	}
	p.child, p.parent = append(p.child, outW), append(p.parent, outR)
	p.copies = append(p.copies, output(st["stdout"], outR))
	errW := outW
	if !ex.Merge {
		errR, w, err := os.Pipe()
		if err != nil {
			p.close()
			return nil, err
		}
		errW = w
		p.child, p.parent = append(p.child, errW), append(p.parent, errR)
		p.copies = append(p.copies, output(st["stderr"], errR))
	}
	attr.Files = []*os.File{stdin, outW, errW}
	return p, nil
}

// halfClose tells the peer an output stream has ended, where the transport
// can (the stream is closed for good after the exit anyway).
func halfClose(c io.ReadWriteCloser) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

func (s *session) resize(rows, cols uint16) {
	s.mu.Lock() // held: run closes the PTY under it
	defer s.mu.Unlock()
	if s.ptmx != nil && rows > 0 && cols > 0 {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	}
}

// signal sends sig to the session's process, or with group to its whole
// process group (it leads one: setsid).
func (s *session) signal(sig int, group bool) {
	if sig <= 0 || sig > 64 {
		return
	}
	s.mu.Lock() // held: run releases the process under it
	defer s.mu.Unlock()
	switch {
	case group && s.pid > 0:
		_ = unix.Kill(-s.pid, unix.Signal(sig))
	case s.proc != nil:
		_ = s.proc.Signal(unix.Signal(sig))
	}
}
