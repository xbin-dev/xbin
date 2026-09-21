package host

import (
	"os"
	"os/exec"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// The terminal/* half: a PTY per terminal/create, in the session's
// namespaces (the host is a descendant of the sandbox init, so every guard
// applies), with the output kept in a bounded buffer truncated from the
// start at a character boundary, as the protocol asks.

const defaultOutputLimit = 1 << 20

type terminal struct {
	id  string
	cmd *exec.Cmd
	pty *os.File
	mu  sync.Mutex
	// output is the kept tail; truncated says bytes were dropped from the
	// start; limit is the cap (outputByteLimit or the default)
	output    []byte
	truncated bool
	limit     int
	exit      *acp.ExitStatus
	exited    chan struct{} // closed once the status is in
	drained   chan struct{} // closed once read() has consumed the PTY to EOF
}

// termCreate serves terminal/create.
func (h *Host) termCreate(p acp.TermCreateParams) (any, *acp.Error) {
	if p.Command == "" {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "command is required"}
	}
	cmd := exec.Command(p.Command, p.Args...)
	cmd.Dir = p.Cwd
	if cmd.Dir == "" {
		cmd.Dir = h.cwd
	}
	cmd.Env = os.Environ() // the sandbox env (never the agent's keys: those are in one process only)
	for _, e := range p.Env {
		cmd.Env = append(cmd.Env, e.Name+"="+e.Value)
	}
	cmd.Env = append(cmd.Env, "TERM=dumb")
	t := &terminal{limit: defaultOutputLimit, exited: make(chan struct{}), drained: make(chan struct{})}
	if p.OutputByteLimit != nil && *p.OutputByteLimit > 0 {
		t.limit = int(*p.OutputByteLimit)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: 200, Rows: 50})
	if err != nil {
		return nil, &acp.Error{Code: acp.ErrInternal, Message: "start: " + err.Error()}
	}
	t.cmd, t.pty = cmd, f
	h.mu.Lock()
	h.nextTerm++
	t.id = "t" + strconv.Itoa(h.nextTerm)
	h.terms[t.id] = t
	h.mu.Unlock()
	status := h.reaper.Claim(cmd.Process.Pid)
	go t.read()
	go func() {
		st := <-status
		t.mu.Lock()
		t.exit = &acp.ExitStatus{}
		if st.Signal != "" {
			s := st.Signal
			t.exit.Signal = &s
		} else {
			c := st.Code
			t.exit.ExitCode = &c
		}
		t.mu.Unlock()
		close(t.exited)
	}()
	return acp.TermCreateResult{TerminalID: t.id}, nil
}

// read drains the PTY into the bounded buffer until it closes, then signals
// drained — the PTY master returns EIO once every slave fd (the child and any
// grandchild) is closed, so drained means the child's output is all buffered.
func (t *terminal) read() {
	defer close(t.drained)
	buf := make([]byte, 8192)
	for {
		n, err := t.pty.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.output = append(t.output, buf[:n]...)
			if len(t.output) > t.limit {
				cut := len(t.output) - t.limit
				for cut < len(t.output) && !utf8.RuneStart(t.output[cut]) {
					cut++ // never split a character
				}
				t.output = t.output[cut:]
				t.truncated = true
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (h *Host) term(id string) (*terminal, *acp.Error) {
	h.mu.Lock()
	t := h.terms[id]
	h.mu.Unlock()
	if t == nil {
		return nil, &acp.Error{Code: acp.ErrNoResource, Message: "no terminal " + id}
	}
	return t, nil
}

// termOutput serves terminal/output.
func (h *Host) termOutput(p acp.TermIDParams) (any, *acp.Error) {
	t, rerr := h.term(p.TerminalID)
	if rerr != nil {
		return nil, rerr
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return acp.TermOutputResult{Output: string(t.output), Truncated: t.truncated, ExitStatus: t.exit}, nil
}

// termWait serves terminal/wait_for_exit (blocks on the read loop's
// goroutine, never the frame loop: the handler runs in its own).
func (h *Host) termWait(p acp.TermIDParams) (any, *acp.Error) {
	t, rerr := h.term(p.TerminalID)
	if rerr != nil {
		return nil, rerr
	}
	<-t.exited
	select { // let read() catch up with the child's last bytes (bounded: a
	case <-t.drained: // grandchild holding the PTY open must not stall the wait)
	case <-time.After(2 * time.Second):
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exit, nil
}

// termKill serves terminal/kill: the whole process group (pty.Start makes
// the child a session leader), output stays readable.
func (h *Host) termKill(p acp.TermIDParams) (any, *acp.Error) {
	t, rerr := h.term(p.TerminalID)
	if rerr != nil {
		return nil, rerr
	}
	t.kill()
	return map[string]any{}, nil
}

// termRelease serves terminal/release: kill and forget.
func (h *Host) termRelease(p acp.TermIDParams) (any, *acp.Error) {
	t, rerr := h.term(p.TerminalID)
	if rerr != nil {
		return nil, rerr
	}
	h.mu.Lock()
	delete(h.terms, p.TerminalID)
	h.mu.Unlock()
	t.kill()
	_ = t.pty.Close()
	return map[string]any{}, nil
}

func (t *terminal) kill() {
	if t.cmd.Process != nil {
		_ = unix.Kill(-t.cmd.Process.Pid, unix.SIGKILL)
	}
}

// killTerminals ends every terminal (the host is going away).
func (h *Host) killTerminals() {
	h.mu.Lock()
	ts := make([]*terminal, 0, len(h.terms))
	for _, t := range h.terms {
		ts = append(ts, t)
	}
	h.mu.Unlock()
	for _, t := range ts {
		t.kill()
		_ = t.pty.Close()
	}
}
