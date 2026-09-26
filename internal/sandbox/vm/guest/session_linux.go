//go:build linux

package guest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// session is one process the host asked for, with its byte streams.
type session struct {
	a    *agent
	ex   proto.Exec
	proc *os.Process
	ptmx *os.File // tty sessions

	mu      sync.Mutex
	streams map[string]io.ReadWriteCloser
	arrived chan struct{} // closed once every expected stream is attached
}

func (s *session) expected() []string {
	if s.ex.TTY {
		return []string{"pty"}
	}
	return []string{"stdin", "stdout", "stderr"}
}

// startSession registers the session; the process starts once its streams
// have connected, so no output is lost.
func (a *agent) startSession(ex proto.Exec) error {
	if len(ex.Argv) == 0 {
		return errors.New("exec: empty argv")
	}
	a.mu.Lock()
	if !a.configured {
		a.mu.Unlock()
		return errors.New("exec before config")
	}
	s := a.sessions[ex.Session]
	if s == nil {
		s = &session{a: a, streams: map[string]io.ReadWriteCloser{}, arrived: make(chan struct{})}
		a.sessions[ex.Session] = s
	} else if s.proc != nil {
		a.mu.Unlock()
		return fmt.Errorf("session %d already running", ex.Session)
	}
	s.ex = ex
	a.mu.Unlock()
	s.checkArrived()
	go func() {
		select {
		case <-s.arrived:
		case <-time.After(15 * time.Second):
			a.send(proto.Msg{Op: "error", Session: ex.Session, Error: "streams did not attach"})
			return
		}
		if err := s.run(); err != nil {
			a.send(proto.Msg{Op: "error", Session: ex.Session, Error: err.Error()})
		}
	}()
	return nil
}

// attachStream hands a stream connection to its session (created on demand:
// streams may connect before or after the exec message).
func (a *agent) attachStream(h proto.Hello, c io.ReadWriteCloser, r *bufio.Reader) {
	a.mu.Lock()
	s := a.sessions[h.Session]
	if s == nil {
		s = &session{a: a, streams: map[string]io.ReadWriteCloser{}, arrived: make(chan struct{})}
		a.sessions[h.Session] = s
	}
	a.mu.Unlock()
	s.mu.Lock()
	if old := s.streams[h.Stream]; old != nil {
		old.Close()
	}
	s.streams[h.Stream] = streamConn{c: c, r: r}
	s.mu.Unlock()
	s.checkArrived()
}

func (s *session) checkArrived() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ex.Session == 0 {
		return // exec not received yet
	}
	for _, name := range s.expected() {
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

func (s *session) run() error {
	ex := s.ex
	env := ex.Env
	prog := ex.Path
	if prog == "" {
		prog = ex.Argv[0]
	}
	argv0, err := lookPath(prog, env)
	if err != nil {
		return err
	}
	cwd := ex.Cwd
	if fi, err := os.Stat(cwd); cwd == "" || err != nil || !fi.IsDir() {
		cwd = "/"
	}
	attr := &os.ProcAttr{Dir: cwd, Env: env}
	var copies sync.WaitGroup
	var closeAfterStart []*os.File
	if ex.TTY {
		ptmx, tty, err := pty.Open()
		if err != nil {
			return fmt.Errorf("pty: %w", err)
		}
		if ex.Rows > 0 && ex.Cols > 0 {
			_ = pty.Setsize(ptmx, &pty.Winsize{Rows: ex.Rows, Cols: ex.Cols})
		}
		s.ptmx = ptmx
		attr.Files = []*os.File{tty, tty, tty}
		attr.Sys = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		closeAfterStart = append(closeAfterStart, tty)
	} else {
		inR, inW, err := os.Pipe()
		if err != nil {
			return err
		}
		outR, outW, err := os.Pipe()
		if err != nil {
			return err
		}
		errR, errW, err := os.Pipe()
		if err != nil {
			return err
		}
		attr.Files = []*os.File{inR, outW, errW}
		attr.Sys = &syscall.SysProcAttr{Setsid: true}
		closeAfterStart = append(closeAfterStart, inR, outW, errW)
		st := s.streams
		go func() { _, _ = io.Copy(inW, st["stdin"]); inW.Close() }()
		copies.Add(2)
		go func() { defer copies.Done(); _, _ = io.Copy(st["stdout"], outR); outR.Close() }()
		go func() { defer copies.Done(); _, _ = io.Copy(st["stderr"], errR); errR.Close() }()
	}
	if ex.Gateway != "" { // up before the backend, which may call it at start
		if err := serveGateway(ex.Gateway); err != nil {
			return fmt.Errorf("gateway %s: %w", ex.Gateway, err)
		}
	}
	proc, done, err := s.a.spawn(argv0, ex.Argv, attr)
	for _, f := range closeAfterStart {
		f.Close()
	}
	if err != nil {
		return fmt.Errorf("exec %s: %w", ex.Argv[0], err)
	}
	s.mu.Lock()
	s.proc = proc
	s.mu.Unlock()
	if ex.TTY {
		c := s.streams["pty"]
		go func() { _, _ = io.Copy(s.ptmx, c) }()
		copies.Add(1)
		// output runs until the last holder of the tty closes it (EIO)
		go func() { defer copies.Done(); _, _ = io.Copy(c, s.ptmx) }()
	}
	s.a.send(proto.Msg{Op: "started", Session: ex.Session})
	stop := make(chan struct{})
	if ex.Listen != "" {
		go s.a.awaitListen(ex.Session, ex.Listen, stop)
	}
	ws := <-done
	close(stop)
	code := ws.ExitStatus()
	if ws.Signaled() {
		code = 128 + int(ws.Signal())
	}
	// drain output (bounded: a daemonized grandchild may keep the tty open)
	drained := make(chan struct{})
	go func() { copies.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
	}
	s.mu.Lock()
	for _, c := range s.streams {
		c.Close()
	}
	s.mu.Unlock()
	if s.ptmx != nil {
		s.ptmx.Close()
	}
	unix.Sync() // the host kills the VM once it hears "exited": the disk must hold everything
	s.a.send(proto.Msg{Op: "exited", Session: ex.Session, Code: code})
	return nil
}

func (s *session) resize(rows, cols uint16) {
	if s.ptmx != nil && rows > 0 && cols > 0 {
		_ = pty.Setsize(s.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
	}
}

func (s *session) signal(sig unix.Signal) {
	s.mu.Lock()
	p := s.proc
	s.mu.Unlock()
	if p != nil {
		_ = p.Signal(sig)
	}
}

// lookPath resolves argv0 against the session's PATH (the agent's own
// environment has none).
func lookPath(argv0 string, env []string) (string, error) {
	if strings.Contains(argv0, "/") {
		return argv0, nil
	}
	path := "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "PATH="); ok {
			path = v
		}
	}
	for _, dir := range filepath.SplitList(path) {
		p := filepath.Join(dir, argv0)
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p, nil
		}
	}
	return "", fmt.Errorf("%s: not found in PATH", argv0)
}
