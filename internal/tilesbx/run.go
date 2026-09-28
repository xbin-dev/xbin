package tilesbx

// run.go — POST …/run (plans/tile-sandbox-runtime.md §3.5): a command run
// to its end in the request, answered with the contract's result. Output
// past maxOutput keeps its first quarter in head and its last three
// quarters in tail, elided bytes between; the text is UTF-8 with invalid
// bytes replaced. At timeoutMs the process group gets TERM, then KILL 5 s
// later (even when its leader ended in between); a caller that hangs up
// has the group killed. A run keeps nothing past its answer and isn't
// listed as an exec, but counts against execsRunning and holds the idle
// stop off while it runs.

import (
	"context"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

const (
	runTimeoutDefaultMs = 60000
	hangupWait          = 10 * time.Second // after a hang-up, the run's end is waited for this long
)

// RunRequest is POST …/run's body: the contract's, plus uid, gid and
// forUser.
type RunRequest struct {
	Cmd       string            `json:"cmd,omitempty"`
	Argv      []string          `json:"argv,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	Stdin     string            `json:"stdin,omitempty"`
	TimeoutMs int64             `json:"timeoutMs,omitempty"`
	MaxOutput int64             `json:"maxOutput,omitempty"`
	Merge     bool              `json:"merge,omitempty"`
	UID       *uint32           `json:"uid,omitempty"`
	GID       *uint32           `json:"gid,omitempty"`
	ForUser   string            `json:"forUser,omitempty"`
}

// RunResult is a run's answer (the contract's): exitCode null when a
// signal ended it (signal names it); stdout and stderr, or output with
// merge.
type RunResult struct {
	ExitCode *int       `json:"exitCode"`
	Signal   string     `json:"signal"`
	TimedOut bool       `json:"timedOut"`
	Ms       int64      `json:"ms"`
	Stdout   *RunOutput `json:"stdout,omitempty"`
	Stderr   *RunOutput `json:"stderr,omitempty"`
	Output   *RunOutput `json:"output,omitempty"`
}

// RunOutput is one stream of a run.
type RunOutput struct {
	Head   string `json:"head"`
	Tail   string `json:"tail"`
	Elided int64  `json:"elided"`
	Bytes  int64  `json:"bytes"`
}

// headTail keeps a stream's output as a run answers it: everything while
// it fits in max; past it, the first quarter and a ring of the last three
// quarters.
type headTail struct {
	max   int
	head  []byte
	over  bool   // past max: head is the first quarter, tail the rest
	tail  []byte // circular, max - max/4 bytes
	pos   int    // the next write in tail
	full  bool   // tail has wrapped
	total int64
}

func newHeadTail(max int) *headTail { return &headTail{max: max} }

// Write takes the stream's next bytes.
func (h *headTail) Write(p []byte) (int, error) {
	n := len(p)
	h.total += int64(n)
	if !h.over {
		if len(h.head)+len(p) <= h.max {
			h.head = append(h.head, p...)
			return n, nil
		}
		q := h.max / 4
		h.over, h.tail = true, make([]byte, h.max-q)
		if k := q - len(h.head); k > 0 { // the head fills from p
			h.head, p = append(h.head, p[:k]...), p[k:]
		} else {
			rest := h.head[q:]
			h.head = h.head[:q:q]
			h.put(rest)
		}
	}
	h.put(p)
	return n, nil
}

// put appends p to the tail ring.
func (h *headTail) put(p []byte) {
	c := len(h.tail)
	if c == 0 {
		return
	}
	if len(p) >= c {
		copy(h.tail, p[len(p)-c:])
		h.pos, h.full = 0, true
		return
	}
	k := copy(h.tail[h.pos:], p)
	copy(h.tail, p[k:])
	if h.pos+len(p) >= c {
		h.full = true
	}
	h.pos = (h.pos + len(p)) % c
}

// result is the stream as answered.
func (h *headTail) result() *RunOutput {
	out := &RunOutput{Head: utf8Text(h.head), Bytes: h.total}
	if !h.over {
		return out
	}
	var tail []byte
	if h.full {
		tail = append(append(tail, h.tail[h.pos:]...), h.tail[:h.pos]...)
	} else {
		tail = h.tail[:h.pos]
	}
	out.Tail = utf8Text(tail)
	out.Elided = h.total - int64(len(h.head)) - int64(len(tail))
	return out
}

// utf8Text is b as UTF-8, invalid bytes replaced.
func utf8Text(b []byte) string { return strings.ToValidUTF8(string(b), "\uFFFD") }

// runCommand runs req in k's sandbox to its end (§3.5).
func (m *Manager) runCommand(ctx context.Context, k Key, d *Def, req *RunRequest) (*RunResult, error) {
	switch {
	case req.TimeoutMs < 0:
		return nil, refuse(RefInvalid, "timeoutMs must not be negative")
	case req.MaxOutput < 0:
		return nil, refuse(RefInvalid, "maxOutput must not be negative")
	case len(req.Stdin) > stdinMax:
		return nil, refuse(RefTooLarge, "stdin is %d bytes, over %d (runtime limits.stdinMax)", len(req.Stdin), stdinMax)
	}
	ex, err := m.execOf(d, command{Cmd: req.Cmd, Argv: req.Argv, Cwd: req.Cwd, Env: req.Env, UID: req.UID, GID: req.GID, ForUser: req.ForUser})
	if err != nil {
		return nil, err
	}
	ex.Merge, ex.NoStdin = req.Merge, req.Stdin == ""
	timeout := req.TimeoutMs
	if timeout == 0 {
		timeout = runTimeoutDefaultMs
	}
	timeout = min(timeout, runTimeoutMaxMs)
	maxOut := req.MaxOutput
	if maxOut == 0 {
		maxOut = runOutputMax
	}
	maxOut = min(maxOut, runOutputMax)

	b, err := m.boxFor(k, d.Name)
	if err != nil {
		return nil, err
	}
	if err := b.execs.admit(); err != nil {
		return nil, err
	}
	defer b.execs.release()
	r, hold, err := m.acquire(k, d.Name, waitMaxSec*time.Second) // its hold is the run's, to its end
	if err != nil {
		return nil, err
	}
	defer hold()
	if r.b != b {
		return nil, refuse(RefNotFound, "sandbox %q was deleted", d.Name)
	}
	c := r.client()
	sess, streams, err := c.Exec(ex)
	if err != nil {
		return nil, err
	}
	t0 := time.Now()
	defer func() {
		for _, s := range streams {
			s.Close()
		}
	}()
	outs := map[string]*headTail{"stdout": newHeadTail(int(maxOut))}
	if !req.Merge {
		outs["stderr"] = newHeadTail(int(maxOut))
	}
	var pumps sync.WaitGroup
	for name, h := range outs {
		s := streams[name]
		pumps.Add(1)
		go func() {
			defer pumps.Done()
			_, _ = io.Copy(touchWriter{m, r, h}, s)
		}()
	}
	if in := streams["stdin"]; in != nil {
		go func() {
			if _, err := io.WriteString(in, req.Stdin); err == nil {
				if cw, ok := in.(interface{ CloseWrite() error }); ok {
					_ = cw.CloseWrite()
				}
			}
		}()
	}
	if err := waitStarted(c, sess); err != nil {
		return nil, err
	}

	var timedOut atomic.Bool
	group := func(sig syscall.Signal) { _ = c.Signal(sess.ID(), int(sig), true) }
	// a timeout's KILL stays armed past the leader's end: a member that
	// ignored the TERM and outlived it goes after the grace too (the agent
	// still signals an ended session's group, agentcore)
	term := time.AfterFunc(time.Duration(timeout)*time.Millisecond, func() {
		timedOut.Store(true)
		time.AfterFunc(termGrace, func() { group(syscall.SIGKILL) })
		group(syscall.SIGTERM)
	})
	select {
	case <-sess.Done():
	case <-ctx.Done(): // the caller hung up: the group goes
		group(syscall.SIGKILL)
		select {
		case <-sess.Done():
		case <-time.After(hangupWait):
		}
	}
	term.Stop()
	ms := time.Since(t0).Milliseconds()
	x := sess.Exit()
	drainStreams(streams, &pumps, x.Killed)
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	res := &RunResult{TimedOut: timedOut.Load(), Ms: ms}
	switch {
	case x.Killed:
		res.Signal = "KILL"
	case x.Signal > 0:
		res.Signal = signalWord(x.Signal)
	case x.Code >= 0:
		code := x.Code
		res.ExitCode = &code
	}
	if req.Merge {
		res.Output = outs["stdout"].result()
	} else {
		res.Stdout, res.Stderr = outs["stdout"].result(), outs["stderr"].result()
	}
	return res, nil
}

// drainStreams waits for a run's output to be read to its end — briefly
// when its sandbox stopped under it — then cuts what is left.
func drainStreams(streams map[string]net.Conn, pumps *sync.WaitGroup, killed bool) {
	done := make(chan struct{})
	go func() { pumps.Wait(); close(done) }()
	grace := drainWait
	if killed {
		grace = killedDrain
	}
	t := time.NewTimer(grace)
	defer t.Stop()
	select {
	case <-done:
		return
	case <-t.C:
	}
	for _, s := range streams {
		s.Close()
	}
	<-done
}

// touchWriter records activity on r's sandbox on every write.
type touchWriter struct {
	m *Manager
	r *run
	w io.Writer
}

func (t touchWriter) Write(p []byte) (int, error) {
	t.m.touch(t.r)
	return t.w.Write(p)
}
