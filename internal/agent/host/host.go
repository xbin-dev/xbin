// Package host is the AGENT HOST: the process that replaces the shell in an
// agent session's sandbox (`bx __agent-host`, D74). It is PID 1 of the
// sandbox, spawns the coding-agent CLI as its child from the daemon's first
// frame (_xbin/spawn {argv, env, cwd} — the provider keys travel here, so
// they sit in the agent's environ only, not in the sandbox spec and not in
// any terminal it opens), and is a transparent proxy for the ACP frames
// between the daemon (its own stdio) and the agent — except the agent's
// fs/* and terminal/* requests, which it serves itself inside the sandbox
// (fs.go, terminal.go) where the kernel's mount view and guards are the
// authority. reaper.go is the PID-1 duty. The agent's stderr passes through
// to the host's, which the daemon keeps as the session's text log.
package host

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/agent/acp"
)

// Version is what _xbin/hello reports (the daemon's own bx is bound in, so
// host and daemon are one build; the version is informational).
var Version = "dev"

// Host is one agent host: the daemon on one side, the agent on the other.
type Host struct {
	in     io.Reader // the daemon → us
	daemon *acp.Conn // us → the daemon (writes only; the read loop is ours)
	agent  *acp.Conn // us → the agent
	agentR io.ReadCloser
	agentW io.WriteCloser
	cmd    *exec.Cmd
	stderr io.Writer
	reaper *Reaper
	cwd    string

	mu       sync.Mutex
	terms    map[string]*terminal
	nextTerm int
	spawned  chan struct{}
	exit     <-chan Status
}

// New builds a host over the daemon's stdio. stderr receives the agent's
// stderr and the host's own notes.
func New(in io.Reader, out io.Writer, stderr io.Writer) *Host {
	return &Host{in: in, daemon: acp.NewConn(nil, out), stderr: stderr, terms: map[string]*terminal{}, spawned: make(chan struct{})}
}

// Run serves until the agent exits (its exit code is returned) or the
// daemon closes stdin / sends SIGTERM (0). The reaper is installed here:
// the host owns SIGCHLD from now on.
func (h *Host) Run() int {
	h.reaper = StartReaper()
	term := make(chan os.Signal, 1)
	signal.Notify(term, unix.SIGTERM, unix.SIGINT)
	daemonGone := make(chan struct{})
	go func() {
		h.daemonLoop()
		close(daemonGone)
	}()
	select {
	case <-h.spawned:
	case <-daemonGone:
		return 0
	case <-term:
		return 0
	}
	agentGone := make(chan struct{})
	go func() {
		h.agentLoop()
		close(agentGone)
	}()
	code := 0
	select {
	case st := <-h.exit:
		code = st.Code
		if st.Signal != "" {
			h.note("the agent was killed by " + st.Signal)
			code = 128
		}
		// its last frames may still be in the pipe (a helper holding the
		// agent's stdout keeps it open past the exit: bounded)
		select {
		case <-agentGone:
		case <-time.After(2 * time.Second):
		}
	case <-agentGone: // stdout closed: the agent is going; its status follows
		select {
		case st := <-h.exit:
			code = st.Code
		case <-time.After(5 * time.Second):
			h.killAgent()
		}
	case <-daemonGone:
	case <-term:
	}
	h.killTerminals()
	h.killAgent()
	return code
}

// daemonLoop reads the daemon's frames: the first must be _xbin/spawn;
// the rest go to the agent as they are.
func (h *Host) daemonLoop() {
	dec := acp.NewDecoder(h.in)
	for {
		m, err := dec.Next()
		if err != nil {
			if errors.Is(err, acp.ErrBadLine) {
				continue
			}
			return
		}
		if m.Method == acp.MXbinSpawn {
			var p acp.SpawnParams
			if err := unmarshal(m.Params, &p); err != nil || len(p.Argv) == 0 {
				h.note("bad spawn frame")
				return
			}
			if err := h.spawn(p); err != nil {
				_ = h.daemon.Notify(acp.MXbinLog, acp.LogParams{Text: "spawn: " + err.Error()})
				return
			}
			continue
		}
		select {
		case <-h.spawned:
		default:
			h.note("frame before spawn: " + m.Method)
			continue
		}
		_ = h.agent.Send(m)
	}
}

// spawn starts the agent with exactly the env the daemon gave.
func (h *Host) spawn(p acp.SpawnParams) error {
	cmd := exec.Command(p.Argv[0], p.Argv[1:]...)
	cmd.Env = p.Env
	cmd.Dir = p.Cwd
	cmd.Stderr = h.stderr
	// OS pipes (not io.Pipe): the read end sees EOF when the agent's stdout
	// closes, with no Wait() involved — the reaper owns the status
	inW, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	outR, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	h.cmd, h.cwd = cmd, p.Cwd
	h.agentR, h.agentW = outR, inW
	h.agent = acp.NewConn(nil, inW)
	h.exit = h.reaper.Claim(cmd.Process.Pid)
	close(h.spawned)
	_ = h.daemon.Notify(acp.MXbinHello, acp.HelloParams{Version: Version})
	return nil
}

// agentLoop reads the agent's frames: fs/* and terminal/* requests are
// answered here; everything else goes to the daemon as it is. A line that
// is not a frame is reported to the daemon as a log line.
func (h *Host) agentLoop() {
	dec := acp.NewDecoder(h.agentR)
	for {
		m, err := dec.Next()
		if err != nil {
			if errors.Is(err, acp.ErrBadLine) {
				_ = h.daemon.Notify(acp.MXbinLog, acp.LogParams{Text: "agent stdout: " + strings.TrimPrefix(err.Error(), acp.ErrBadLine.Error()+": ")})
				continue
			}
			return
		}
		if m.IsRequest() && h.serves(m.Method) {
			go func(m *acp.Message) {
				res, rerr := h.serve(m)
				_ = h.agent.Reply(m.ID, res, rerr)
			}(m)
			continue
		}
		_ = h.daemon.Send(m)
	}
}

func (h *Host) serves(method string) bool {
	switch method {
	case acp.MFsRead, acp.MFsWrite, acp.MTermCreate, acp.MTermOutput, acp.MTermWait, acp.MTermKill, acp.MTermRelease:
		return true
	}
	return false
}

// serve answers one client-capability request.
func (h *Host) serve(m *acp.Message) (any, *acp.Error) {
	bad := func(err error) (any, *acp.Error) {
		return nil, &acp.Error{Code: acp.ErrInvalidParam, Message: "bad params for " + m.Method + ": " + err.Error()}
	}
	switch m.Method {
	case acp.MFsRead:
		var p acp.FsReadParams
		if err := unmarshal(m.Params, &p); err != nil {
			return bad(err)
		}
		return h.readTextFile(p)
	case acp.MFsWrite:
		var p acp.FsWriteParams
		if err := unmarshal(m.Params, &p); err != nil {
			return bad(err)
		}
		return h.writeTextFile(p)
	case acp.MTermCreate:
		var p acp.TermCreateParams
		if err := unmarshal(m.Params, &p); err != nil {
			return bad(err)
		}
		return h.termCreate(p)
	default:
		var p acp.TermIDParams
		if err := unmarshal(m.Params, &p); err != nil {
			return bad(err)
		}
		switch m.Method {
		case acp.MTermOutput:
			return h.termOutput(p)
		case acp.MTermWait:
			return h.termWait(p)
		case acp.MTermKill:
			return h.termKill(p)
		default:
			return h.termRelease(p)
		}
	}
}

func (h *Host) killAgent() {
	if h.cmd != nil && h.cmd.Process != nil {
		_ = h.cmd.Process.Kill()
	}
	if h.agentW != nil {
		_ = h.agentW.Close()
	}
}

func (h *Host) note(s string) {
	fmt.Fprintln(h.stderr, "[agent-host] "+s)
}
