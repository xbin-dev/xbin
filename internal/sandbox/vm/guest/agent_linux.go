//go:build linux

// Package guest is xbin-vmagent, PID 1 inside a VM sandbox
// (plans/vm-sandbox.md). It boots from an initramfs holding only itself,
// mounts the kernel filesystems, and waits on vsock for the host shim. The
// first control message (Config) turns the anonymous guest into this
// sandbox: the root overlay over the read-only rootfs image, the host binds
// (FUSE, served by the shim) at their host paths, the network, the clock. Then it runs sessions
// (the terminal shell, an agent host, a backend) and reports their exits.
//
// Nothing tile-specific exists before Config, which is what lets one booted
// guest be snapshotted as a template and restored for any tile.
package guest

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Main runs the agent; it never returns.
func Main() {
	if os.Getpid() != 1 {
		fmt.Fprintln(os.Stderr, "xbin-vmagent: must run as PID 1 inside a VM sandbox")
		os.Exit(2)
	}
	if err := earlyMounts(); err != nil {
		fatal("early mounts: %v", err)
	}
	a := &agent{sessions: map[int]*session{}, waiting: map[int]chan unix.WaitStatus{}}
	go a.reap()
	ln, err := listenVsock(proto.AgentPort)
	if err != nil {
		fatal("vsock listen: %v", err)
	}
	logf("agent listening on vsock %d", proto.AgentPort)
	for {
		c, err := ln.accept()
		if err != nil {
			logf("accept: %v", err)
			continue
		}
		go a.handle(c)
	}
}

// agent is the process-wide state.
type agent struct {
	mu         sync.Mutex
	configured bool
	sessions   map[int]*session
	ctl        *proto.Conn // the live control connection (events go here)

	// reaper bookkeeping: PID 1 owns every wait. spawn registers a session's
	// pid while holding wmu, so the reaper can't collect it unannounced;
	// statuses of orphans nobody waits for are dropped.
	wmu     sync.Mutex
	waiting map[int]chan unix.WaitStatus
}

func (a *agent) handle(c io.ReadWriteCloser) {
	h, r, err := proto.ReadHello(c)
	if err != nil {
		c.Close()
		return
	}
	switch h.Kind {
	case "ctl":
		a.control(proto.NewConn(c, r))
	case "stream":
		a.attachStream(h, c, r)
	case "listen":
		a.bridgeListen(h, streamConn{c: c, r: r})
	default:
		c.Close()
	}
}

// control serves one control connection until it closes. A later control
// connection takes over event delivery (the shim reconnects after a
// template restore resets vsock).
func (a *agent) control(c *proto.Conn) {
	a.mu.Lock()
	a.ctl = c
	a.mu.Unlock()
	defer c.Close()
	for {
		var m proto.Msg
		if err := c.Recv(&m); err != nil {
			return
		}
		switch m.Op {
		case "config":
			if m.Config == nil {
				a.send(proto.Msg{Op: "error", Error: "config: empty"})
				continue
			}
			if err := a.configure(*m.Config); err != nil {
				a.send(proto.Msg{Op: "error", Error: "config: " + err.Error()})
				continue
			}
			a.send(proto.Msg{Op: "ready"})
		case "exec":
			if m.Exec == nil {
				continue
			}
			if err := a.startSession(*m.Exec); err != nil {
				a.send(proto.Msg{Op: "error", Session: m.Exec.Session, Error: err.Error()})
			}
		case "resize":
			if s := a.session(m.Session); s != nil {
				s.resize(m.Rows, m.Cols)
			}
		case "signal":
			if s := a.session(m.Session); s != nil {
				s.signal(unix.Signal(m.Signal))
			}
		case "sync":
			// the host is ending the VM (a terminal closed): hang up the
			// session, flush every filesystem, say so — it kills us next
			if s := a.session(m.Session); s != nil {
				s.signal(unix.SIGHUP)
			}
			unix.Sync()
			a.send(proto.Msg{Op: "synced"})
		}
	}
}

// send delivers an event on the current control connection.
func (a *agent) send(m proto.Msg) {
	a.mu.Lock()
	c := a.ctl
	a.mu.Unlock()
	if c != nil {
		_ = c.Send(m)
	}
}

func (a *agent) session(id int) *session {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.sessions[id]
}

// reap is PID 1's wait loop: every child and orphan ends here. SIGCHLD wakes
// it; a child that exits between the drain and the receive leaves its
// signal queued, so none is missed.
func (a *agent) reap() {
	sigs := make(chan os.Signal, 64)
	signal.Notify(sigs, unix.SIGCHLD)
	for {
		for {
			var ws unix.WaitStatus
			pid, err := unix.Wait4(-1, &ws, unix.WNOHANG, nil)
			if err != nil || pid <= 0 {
				break
			}
			a.wmu.Lock()
			if ch := a.waiting[pid]; ch != nil {
				delete(a.waiting, pid)
				ch <- ws
			}
			a.wmu.Unlock()
		}
		<-sigs
	}
}

// spawn starts a process and returns the channel its exit status arrives on.
func (a *agent) spawn(argv0 string, argv []string, attr *os.ProcAttr) (*os.Process, chan unix.WaitStatus, error) {
	a.wmu.Lock()
	defer a.wmu.Unlock()
	p, err := os.StartProcess(argv0, argv, attr)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan unix.WaitStatus, 1)
	a.waiting[p.Pid] = ch
	return p, ch, nil
}

// streamConn pairs a stream connection with the reader that consumed its
// Hello (so bytes buffered past the line aren't lost).
type streamConn struct {
	c io.ReadWriteCloser
	r *bufio.Reader
}

func (s streamConn) Read(p []byte) (int, error)  { return s.r.Read(p) }
func (s streamConn) Write(p []byte) (int, error) { return s.c.Write(p) }
func (s streamConn) Close() error                { return s.c.Close() }

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "xbin-vmagent: "+format+"\n", args...)
}

func fatal(format string, args ...any) {
	logf(format, args...)
	// A dead PID 1 panics the kernel, which Firecracker turns into an exit:
	// the shim reports the serial tail.
	os.Exit(1)
}
