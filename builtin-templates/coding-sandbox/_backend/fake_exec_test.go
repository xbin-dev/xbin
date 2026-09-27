package main

// fake_exec_test.go — the fake backend's commands (TEST ONLY; see
// fake_backend_test.go): host processes in the sandbox's directory, each in
// a process group of its own; a run's output shaped head and tail; an
// exec's one combined stream kept in a ring read by offset.

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// fkExec is a background command.
type fkExec struct {
	info    xbin.ExecInfo
	seq     int
	ring    *fkRing
	cmd     *exec.Cmd
	pid     int // the process group, once started (f.mu); 0 before
	stdin   io.WriteCloser
	eof     bool          // stdin was closed (f.mu)
	done    chan struct{} // closed once the exec has ended (or never started)
	killed  bool
	State   string        // mirrors info.State (f.mu)
	pty     *os.File      // a tty exec's terminal (master), once started (f.mu)
	ptyDone chan struct{} // closed when its output has all reached the ring
}

// --- paths -----------------------------------------------------------------------------

// path resolves an absolute in-sandbox path for an operation on the entry
// itself (stat, remove, move, mkdir): lexically inside the sandbox, with the
// symlinks of its parent resolving inside it too.
func (b *fkBox) path(p string) (string, error) {
	if !strings.HasPrefix(p, "/") {
		return "", fkErr(http.StatusBadRequest, "invalid", "%q is not an absolute path", p)
	}
	c := filepath.Clean(p)
	if !fkWithin(b.dir, c) || !fkWithin(b.dir, fkResolve(filepath.Dir(c))) {
		return "", fkErr(http.StatusBadRequest, "invalid", "%s is outside the sandbox", p)
	}
	return c, nil
}

// pathFollow is path for an operation that follows a final symlink (read,
// write, list, a cwd, a tree): all of it must resolve inside the sandbox.
func (b *fkBox) pathFollow(p string) (string, error) {
	c, err := b.path(p)
	if err == nil && !fkWithin(b.dir, fkResolve(c)) {
		err = fkErr(http.StatusBadRequest, "invalid", "%s is outside the sandbox", p)
	}
	return c, err
}

func fkWithin(dir, p string) bool {
	rel, err := filepath.Rel(dir, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// fkResolve is p with the symlinks of its longest existing prefix resolved.
func fkResolve(p string) string {
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// --- commands ------------------------------------------------------------------------------

type fkCmd struct {
	Cmd  string
	Argv []string
	Cwd  string
	Env  map[string]string
	TTY  bool
}

// command builds a host process for b (f.mu held).
func (b *fkBox) command(q fkCmd) (*exec.Cmd, error) {
	if q.Cmd == "" && len(q.Argv) == 0 {
		return nil, fkErr(http.StatusBadRequest, "invalid", "cmd or argv is required")
	}
	cwd := b.info.Defaults.Cwd
	if q.Cwd != "" {
		p, err := b.pathFollow(q.Cwd)
		if err != nil {
			return nil, err
		}
		cwd = p
	}
	if fi, err := os.Stat(cwd); err != nil || !fi.IsDir() {
		return nil, fkErr(http.StatusBadRequest, "invalid", "cwd %s isn't a directory in the sandbox", cwd)
	}
	var c *exec.Cmd
	if len(q.Argv) > 0 {
		c = exec.Command(q.Argv[0], q.Argv[1:]...) // exec-ok: a test fixture; the "sandbox" is a host directory
	} else {
		c = exec.Command("sh", "-c", q.Cmd) // exec-ok: a test fixture (see above)
	}
	c.Dir = cwd
	env := map[string]string{"PATH": os.Getenv("PATH"), "PWD": cwd, "LANG": "C.UTF-8", "IN_SANDBOX": "1"}
	for k, v := range b.info.Defaults.Env {
		env[k] = v
	}
	if q.TTY {
		env["TERM"] = "xterm-256color"
	}
	for k, v := range q.Env {
		env[k] = v
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c.Env = append(c.Env, k+"="+env[k])
	}
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if q.TTY { // a session of its own (its group too), the terminal its controlling one
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	}
	return c, nil
}

// fkKill signals a started command's process group (never 0: our own).
func fkKill(pgid int, sig syscall.Signal) {
	if pgid > 0 {
		_ = syscall.Kill(-pgid, sig)
	}
}

// fkEnd ends a process group the contract's way: TERM, then KILL once grace
// runs out while any of it is left.
func fkEnd(pgid int, grace time.Duration) {
	if pgid <= 0 {
		return
	}
	fkKill(pgid, syscall.SIGTERM)
	for deadline := time.Now().Add(grace); time.Now().Before(deadline); {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	fkKill(pgid, syscall.SIGKILL)
}

var fkSignalNames = map[syscall.Signal]string{syscall.SIGINT: "INT", syscall.SIGTERM: "TERM", syscall.SIGKILL: "KILL",
	syscall.SIGHUP: "HUP", syscall.SIGQUIT: "QUIT", syscall.SIGABRT: "ABRT", syscall.SIGSEGV: "SEGV", syscall.SIGBUS: "BUS",
	syscall.SIGFPE: "FPE", syscall.SIGILL: "ILL", syscall.SIGPIPE: "PIPE", syscall.SIGALRM: "ALRM", syscall.SIGUSR1: "USR1",
	syscall.SIGUSR2: "USR2", syscall.SIGTRAP: "TRAP", syscall.SIGXCPU: "XCPU", syscall.SIGXFSZ: "XFSZ"}

var fkSignals = map[string]syscall.Signal{"INT": syscall.SIGINT, "TERM": syscall.SIGTERM, "KILL": syscall.SIGKILL, "HUP": syscall.SIGHUP}

func fkSigName(ps *os.ProcessState) (int, string) {
	if ps == nil {
		return -1, ""
	}
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		if n, ok := fkSignalNames[ws.Signal()]; ok {
			return -1, n
		}
		return -1, strconv.Itoa(int(ws.Signal()))
	}
	return ps.ExitCode(), ""
}

// fkHeadTail keeps an output's first quarter and last three quarters of max.
type fkHeadTail struct {
	mu    sync.Mutex
	max   int
	head  []byte
	tail  []byte
	bytes int64
}

func (h *fkHeadTail) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.bytes += int64(len(p))
	q := h.max / 4
	rest := p
	if room := q - len(h.head); room > 0 {
		n := min(room, len(rest))
		h.head = append(h.head, rest[:n]...)
		rest = rest[n:]
	}
	h.tail = append(h.tail, rest...)
	if keep := h.max - q; len(h.tail) > 2*keep+4096 {
		h.tail = append(h.tail[:0:0], h.tail[len(h.tail)-keep:]...)
	}
	return len(p), nil
}

func (h *fkHeadTail) out() *xbin.RunOutput {
	h.mu.Lock()
	defer h.mu.Unlock()
	tail := h.tail
	if keep := h.max - h.max/4; len(tail) > keep {
		tail = tail[len(tail)-keep:]
	}
	kept := int64(len(h.head) + len(tail))
	if kept >= h.bytes { // it all fit: one piece
		return &xbin.RunOutput{Head: fkText(append(append([]byte{}, h.head...), tail...)), Bytes: h.bytes}
	}
	return &xbin.RunOutput{Head: fkText(h.head), Tail: fkText(tail), Elided: h.bytes - kept, Bytes: h.bytes}
}

func fkText(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

func (s *fkSandbox) Run(ctx context.Context, q xbin.RunRequest) (*xbin.RunResult, error) {
	if len(q.Stdin) > 1<<20 {
		return nil, fkErr(http.StatusRequestEntityTooLarge, "too-large", "stdin is over limits.stdinMax")
	}
	b, err := s.use("run")
	if err != nil {
		return nil, err
	}
	c, err := b.command(fkCmd{Cmd: q.Cmd, Argv: q.Argv, Cwd: q.Cwd, Env: q.Env})
	s.f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	limit := int(q.MaxOutput)
	if limit <= 0 {
		limit = 65536
	}
	limit = min(limit, 1<<20)
	timeout := q.TimeoutMs
	if timeout <= 0 {
		timeout = 60000
	}
	timeout = min(timeout, 600000)
	out, errs := &fkHeadTail{max: limit}, &fkHeadTail{max: limit}
	c.Stdout, c.Stderr = out, errs
	if q.Merge {
		c.Stderr = out
	}
	c.Stdin = strings.NewReader(q.Stdin)
	start := time.Now()
	if err := c.Start(); err != nil {
		return nil, fkErr(http.StatusBadRequest, "invalid", "%v", err)
	}
	pgid := c.Process.Pid
	done := make(chan struct{})
	var timedOut atomic.Bool
	go func() {
		timer := time.NewTimer(time.Duration(timeout) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
			timedOut.Store(true)
			fkEnd(pgid, s.f.grace()) // on past the answer if a member outlived the leader
		case <-ctx.Done(): // the caller hung up: the run is its request's
			select {
			case <-done:
			default:
				fkKill(pgid, syscall.SIGKILL)
			}
		}
	}()
	_ = c.Wait()
	close(done)
	code, sig := fkSigName(c.ProcessState)
	res := &xbin.RunResult{Signal: sig, TimedOut: timedOut.Load(), Ms: time.Since(start).Milliseconds()}
	if sig == "" {
		res.ExitCode = &code
	}
	if q.Merge {
		res.Output = out.out()
	} else {
		res.Stdout, res.Stderr = out.out(), errs.out()
	}
	return res, nil
}

// fkRing is an exec's combined output: the newest bytes of a stream, at
// least max of them (up to 2×max between compactions).
type fkRing struct {
	mu     sync.Mutex
	max    int
	buf    []byte
	total  int64
	closed bool
	ch     chan struct{} // closed (and replaced) on every change
}

func newFkRing(size int) *fkRing { return &fkRing{max: size, ch: make(chan struct{})} }

func (r *fkRing) Write(p []byte) (int, error) {
	r.mu.Lock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > 2*r.max {
		r.buf = append(r.buf[:0:0], r.buf[len(r.buf)-r.max:]...)
	}
	r.total += int64(len(p))
	close(r.ch)
	r.ch = make(chan struct{})
	r.mu.Unlock()
	return len(p), nil
}

func (r *fkRing) close() {
	r.mu.Lock()
	r.closed = true
	close(r.ch)
	r.ch = make(chan struct{})
	r.mu.Unlock()
}

// read returns bytes from since (or the oldest kept), at most limit, waiting
// up to wait for some when there are none and the stream is open.
func (r *fkRing) read(ctx context.Context, since int64, limit int, wait time.Duration) (start, end, total, ringStart int64, data []byte) {
	deadline := time.Now().Add(wait)
	for {
		r.mu.Lock()
		ringStart = r.total - int64(len(r.buf))
		if since < r.total || r.closed || time.Now().After(deadline) {
			start = max(since, ringStart)
			if start > r.total {
				start = r.total
			}
			end = min(r.total, start+int64(limit))
			data = append([]byte(nil), r.buf[start-ringStart:end-ringStart]...)
			total = r.total
			r.mu.Unlock()
			return
		}
		ch := r.ch
		r.mu.Unlock()
		select {
		case <-ch:
		case <-time.After(time.Until(deadline)):
		case <-ctx.Done():
			deadline = time.Now()
		}
	}
}

// ended: the stream is over and since has read all of it.
func (r *fkRing) ended(since int64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed && since >= r.total
}

func (r *fkRing) totalNow() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// execView is e's info now (f.mu held).
func (e *fkExec) view() xbin.ExecInfo {
	v := e.info
	v.State = e.State
	v.Total = e.ring.totalNow()
	return v
}

// stopExecs kills b's running commands and returns those that had started
// (f.mu held; the kill doesn't wait — fkAwait them after unlocking).
func (b *fkBox) stopExecs() []*fkExec {
	var out []*fkExec
	for _, e := range b.execs {
		if e.State == "running" {
			e.killed = true
			if e.pid > 0 {
				fkKill(e.pid, syscall.SIGKILL)
				out = append(out, e)
			}
		}
	}
	return out
}

// fkAwait waits for execs to end, up to d in all.
func fkAwait(execs []*fkExec, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	for _, e := range execs {
		select {
		case <-e.done:
		case <-t.C:
			return
		}
	}
}

func (s *fkSandbox) Exec(ctx context.Context, q xbin.ExecRequest) (*xbin.ExecInfo, error) {
	e, err := s.launch(q)
	if err != nil {
		return nil, err
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	v := e.view()
	return &v, nil
}

// launch starts a background exec (a repeated clientId answers the one it
// started).
func (s *fkSandbox) launch(q xbin.ExecRequest) (*fkExec, error) {
	if q.TTY && !s.f.hasCap("tty") {
		return nil, fkErr(http.StatusNotImplemented, "unsupported", "no terminals here")
	}
	b, err := s.use("exec")
	if err != nil {
		return nil, err
	}
	h := fkHash(q)
	if q.ClientID != "" {
		if prev, ok := b.eidem[q.ClientID]; ok {
			if prev.hash != h {
				s.f.mu.Unlock()
				return nil, fkErr(http.StatusConflict, "exists", "clientId %s was used for a different command", q.ClientID)
			}
			if e := b.execs[prev.id]; e != nil {
				s.f.mu.Unlock()
				return e, nil
			}
		}
	}
	cmd, err := b.command(fkCmd{Cmd: q.Cmd, Argv: q.Argv, Cwd: q.Cwd, Env: q.Env, TTY: q.TTY})
	if err != nil {
		s.f.mu.Unlock()
		return nil, err
	}
	b.eseq++
	e := &fkExec{seq: b.eseq, ring: newFkRing(s.f.ringSize()), cmd: cmd, done: make(chan struct{}), State: "running"}
	e.info = xbin.ExecInfo{ID: fmt.Sprintf("e%d", b.eseq), Label: q.Label, Cmd: q.Cmd, Argv: q.Argv, Cwd: cmd.Dir, TTY: q.TTY,
		State: "running", Started: fkNow(), ClientID: q.ClientID, ForUser: q.ForUser, UID: q.UID}
	b.execs[e.info.ID] = e
	if q.ClientID != "" {
		b.eidem[q.ClientID] = fkIdem{id: e.info.ID, hash: h}
	}
	cmd.Stdout, cmd.Stderr = e.ring, e.ring
	s.f.mu.Unlock()
	var in io.WriteCloser
	var master, slave *os.File
	if q.TTY { // the terminal is its stdin, stdout and stderr; its output reaches the ring through us
		if master, slave, err = fkOpenPTY(q.Rows, q.Cols); err != nil {
			s.forgetExec(b, e)
			return nil, fkErr(http.StatusServiceUnavailable, "unavailable", "a terminal: %v", err)
		}
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
		if q.Stdin {
			in = fkTTYIn{master}
		}
	} else if q.Stdin {
		in, _ = cmd.StdinPipe()
	}
	err = cmd.Start()
	if slave != nil {
		slave.Close() // the command's now
	}
	if err != nil {
		if master != nil {
			master.Close()
		}
		s.forgetExec(b, e)
		return nil, fkErr(http.StatusBadRequest, "invalid", "%v", err)
	}
	s.f.mu.Lock()
	e.pid, e.stdin, e.pty = cmd.Process.Pid, in, master
	late := e.killed || s.f.closed || s.f.boxes[s.name] != b || b.execs[e.info.ID] != e
	s.f.mu.Unlock()
	if late { // killed between the check and now: deliver it
		fkKill(e.pid, syscall.SIGKILL)
	}
	if master != nil {
		e.ptyDone = make(chan struct{})
		go func() {
			_, _ = io.Copy(e.ring, master) // until the terminal hangs up (EIO) or reap closes it
			close(e.ptyDone)
		}()
	}
	go s.reap(e)
	if q.TimeoutMs > 0 {
		go func() {
			t := time.NewTimer(time.Duration(q.TimeoutMs) * time.Millisecond)
			defer t.Stop()
			select {
			case <-e.done:
			case <-t.C:
				s.f.mu.Lock()
				running := e.State == "running"
				e.killed = e.killed || running
				s.f.mu.Unlock()
				if running {
					fkEnd(e.pid, s.f.grace())
				}
			}
		}()
	}
	return e, nil
}

// forgetExec drops an exec that never ran.
func (s *fkSandbox) forgetExec(b *fkBox, e *fkExec) {
	s.f.mu.Lock()
	delete(b.execs, e.info.ID)
	e.State, e.info.Ended = "exited", fkNow()
	s.f.mu.Unlock()
	e.ring.close()
	close(e.done)
}

// fkTTYIn is a tty exec's stdin (with stdin: true): end-of-file is ^D.
type fkTTYIn struct{ f *os.File }

func (t fkTTYIn) Write(p []byte) (int, error) { return t.f.Write(p) }
func (t fkTTYIn) Close() error                { _, err := t.f.Write([]byte{4}); return err }

func (s *fkSandbox) reap(e *fkExec) {
	_ = e.cmd.Wait()
	if e.ptyDone != nil {
		select { // the terminal's last output drains, unless something left behind holds it
		case <-e.ptyDone:
		case <-time.After(500 * time.Millisecond):
		}
		_ = e.pty.Close()
		<-e.ptyDone
	}
	code, sig := fkSigName(e.cmd.ProcessState)
	s.f.mu.Lock()
	e.State = "exited"
	if e.killed || sig != "" {
		e.State = "killed"
	}
	e.info.ExitCode, e.info.Signal, e.info.Ended = &code, sig, fkNow()
	if sig != "" {
		e.info.ExitCode = nil
	}
	s.f.mu.Unlock()
	e.ring.close()
	close(e.done)
}

// exec finds exec id (f.mu held on return when ok).
func (s *fkSandbox) exec(op, id string) (*fkBox, *fkExec, error) {
	b, err := s.find(op)
	if err != nil {
		return nil, nil, err
	}
	e := b.execs[id]
	if e == nil {
		s.f.mu.Unlock()
		return nil, nil, fkErr(http.StatusNotFound, "not-found", "no exec %s", id)
	}
	return b, e, nil
}

func (s *fkSandbox) Execs(ctx context.Context) ([]xbin.ExecInfo, error) {
	b, err := s.find("execs")
	if err != nil {
		return nil, err
	}
	defer s.f.mu.Unlock()
	var es []*fkExec
	for _, e := range b.execs {
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].seq < es[j].seq })
	out := []xbin.ExecInfo{}
	for _, e := range es {
		out = append(out, e.view())
	}
	return out, nil
}

func (s *fkSandbox) GetExec(ctx context.Context, id string) (*xbin.ExecInfo, error) {
	_, e, err := s.exec("exec-get", id)
	if err != nil {
		return nil, err
	}
	defer s.f.mu.Unlock()
	v := e.view()
	return &v, nil
}

func (s *fkSandbox) Kill(ctx context.Context, id string) error {
	b, e, err := s.exec("exec-delete", id)
	if err != nil {
		return err
	}
	delete(b.execs, id)
	if e.State == "running" {
		e.killed = true
		fkKill(e.pid, syscall.SIGKILL)
	}
	s.f.mu.Unlock()
	return nil
}

func (s *fkSandbox) Output(ctx context.Context, id string, q xbin.OutputQuery) (*xbin.OutputChunk, error) {
	enc := orStr(q.Encoding, "text")
	if enc != "text" && enc != "base64" {
		return nil, fkErr(http.StatusBadRequest, "invalid", "encoding is text or base64")
	}
	_, e, err := s.exec("output", id)
	if err != nil {
		return nil, err
	}
	s.f.mu.Unlock()
	limit := int(q.Max)
	if limit <= 0 {
		limit = 64 << 10
	}
	limit = min(limit, 1<<20)
	wait := time.Duration(max(0, min(q.WaitMs, 30000))) * time.Millisecond
	start, end, total, ringStart, data := e.ring.read(ctx, max(q.Since, 0), limit, wait)
	s.f.mu.Lock()
	st, code, sig := e.State, e.info.ExitCode, e.info.Signal
	s.f.mu.Unlock()
	c := &xbin.OutputChunk{Start: start, End: end, Total: total, RingStart: ringStart, Encoding: enc, State: st, ExitCode: code, Signal: sig}
	if enc == "base64" {
		c.Data = base64.StdEncoding.EncodeToString(data)
	} else {
		c.Data = fkText(data)
	}
	return c, nil
}

func (s *fkSandbox) Stdin(ctx context.Context, id string, r io.Reader, eof bool) error {
	_, e, err := s.exec("stdin", id)
	if err != nil {
		return err
	}
	in, st, closed := e.stdin, e.State, e.eof
	s.f.mu.Unlock()
	switch {
	case in == nil:
		return fkErr(http.StatusBadRequest, "invalid", "this exec wasn't started with stdin")
	case st != "running":
		return fkState(st, "the exec has ended")
	case closed:
		return fkErr(http.StatusBadRequest, "invalid", "stdin is closed")
	}
	body, err := io.ReadAll(io.LimitReader(r, 1<<20+1))
	if err == nil && len(body) > 1<<20 {
		return fkErr(http.StatusRequestEntityTooLarge, "too-large", "over limits.stdinMax")
	}
	if len(body) > 0 {
		if _, err := in.Write(body); err != nil {
			return fkState("exited", "the exec has stopped reading")
		}
	}
	if eof {
		_ = in.Close()
		s.f.mu.Lock()
		e.eof = true
		s.f.mu.Unlock()
	}
	return nil
}

func (s *fkSandbox) Signal(ctx context.Context, id, sig string, group bool) error {
	n, known := fkSignals[strings.TrimPrefix(strings.ToUpper(sig), "SIG")]
	if !known {
		return fkErr(http.StatusBadRequest, "invalid", "signal is INT, TERM, KILL or HUP")
	}
	_, e, err := s.exec("signal", id)
	if err != nil {
		return err
	}
	defer s.f.mu.Unlock()
	// A finished exec's group is gone (its number may be another's now).
	if e.State == "running" && e.pid > 0 {
		if group {
			fkKill(e.pid, n)
		} else {
			_ = syscall.Kill(e.pid, n)
		}
	}
	return nil
}

func (s *fkSandbox) Resize(ctx context.Context, id string, rows, cols int) error {
	if !s.f.hasCap("tty") {
		return fkErr(http.StatusNotImplemented, "unsupported", "no terminals here")
	}
	if rows <= 0 || cols <= 0 {
		return fkErr(http.StatusBadRequest, "invalid", "rows and cols are positive")
	}
	_, e, err := s.exec("resize", id)
	if err != nil {
		return err
	}
	tty, st, pty := e.info.TTY, e.State, e.pty
	s.f.mu.Unlock()
	switch {
	case !tty:
		return fkErr(http.StatusBadRequest, "invalid", "this exec has no terminal (started without tty)")
	case st != "running" || pty == nil:
		return fkState(st, "the exec has ended")
	case fkSetSize(pty, rows, cols) != nil:
		return fkState("exited", "the exec has ended")
	}
	return nil
}
