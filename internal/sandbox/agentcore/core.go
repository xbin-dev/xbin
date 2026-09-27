//go:build linux

// Package agentcore is the exec core that runs inside a sandbox: the VM
// guest's agent (xbin-vmagent) and, for namespace-mode tile sandboxes, `bx
// __sbx-agent` (plans/tile-sandbox-runtime.md §2). It serves the wire in
// internal/sandbox/vm/proto: control connections (config, exec, resize,
// signal, sync), the byte streams of the sessions it runs, and file
// operations, each on a connection of its own that the transport — vsock in
// a VM, the connection factory in a namespace sandbox — hands to Handle.
//
// What its peer sends is bounded (proto.MaxHello, proto.MaxCommand, frames),
// sessions are numbered by the peer and never run twice, and file operations
// resolve every path inside Options.Root (§2.6).
package agentcore

import (
	"errors"
	"io"
	"sort"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// Options configures a Core.
type Options struct {
	// Spawn starts sessions: PID1Spawner in a sandbox, ProcSpawner in tests.
	Spawn Spawner
	// Configure applies the first "config" (a VM guest: the root, the
	// network, the mounts). nil: the sandbox is ready as it is — every ctl
	// connection gets "ready" at once, and a "config" is an error.
	Configure func(proto.Config) error
	// Sync flushes the sandbox's filesystems: on "sync", and after every
	// session but a NoSync one. A VM guest: unix.Sync; a namespace sandbox:
	// syncfs("/"), never sync(2), which would flush the whole host.
	Sync func()
	// Root is where file operations resolve their paths (openat2
	// RESOLVE_IN_ROOT): "/" in a sandbox (the default), a directory in tests.
	Root string
	// MaxSessions bounds the live sessions, and separately the sessions
	// whose streams arrived before their exec (default 256).
	MaxSessions int
	// StreamWait is how long a session's streams may take to attach after
	// its exec, and an exec after its first stream (default 15 s).
	StreamWait time.Duration
	// Gateway serves Exec.Gateway (a VM backend's xbind gateway); nil
	// refuses an exec that asks for one.
	Gateway func(path string) error
	// Dump answers "dump" (a VM guest's report); nil ignores it.
	Dump func() string
	Logf func(format string, args ...any)
}

// Core is the state of one agent: its sessions and its control connection.
type Core struct {
	o Options

	cfgMu sync.Mutex // one Configure at a time

	mu         sync.Mutex
	configured bool
	ctl        *proto.Conn // the newest control connection: events go there
	sessions   map[int]*session
	retired    map[int]bool // ids that ran (or were pruned): never run again
	retiredQ   []int

	sendMu sync.Mutex // one event line at a time
	wmu    sync.Mutex // file writes: a precondition and its rename are one step
}

// New returns a core; it serves nothing until connections are handed to it.
func New(o Options) *Core {
	if o.Spawn == nil {
		o.Spawn = ProcSpawner()
	}
	if o.Root == "" {
		o.Root = "/"
	}
	if o.MaxSessions <= 0 {
		o.MaxSessions = 256
	}
	if o.StreamWait <= 0 {
		o.StreamWait = 15 * time.Second
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	return &Core{
		o:          o,
		configured: o.Configure == nil,
		sessions:   map[int]*session{},
		retired:    map[int]bool{},
	}
}

// Serve hands every connection accept returns to Handle, until accept fails
// (io.EOF: the transport is gone), and returns that error.
func (c *Core) Serve(accept func() (io.ReadWriteCloser, error)) error {
	for {
		conn, err := accept()
		if err != nil {
			return err
		}
		go c.Handle(conn)
	}
}

// Handle serves one accepted connection, by the kind its Hello names, and
// closes it when done.
func (c *Core) Handle(conn io.ReadWriteCloser) {
	h, r, err := proto.ReadHelloMax(conn, proto.MaxHello)
	if err != nil {
		if err == proto.ErrLineTooLong {
			c.o.Logf("a connection's hello is over %d bytes: dropped", proto.MaxHello)
		}
		conn.Close()
		return
	}
	switch h.Kind {
	case "ctl":
		c.control(proto.NewConn(conn, r))
	case "stream":
		c.attachStream(h, streamConn{c: conn, r: r})
	case "listen":
		c.bridgeListen(h, streamConn{c: conn, r: r})
	case "file":
		c.fileOp(h.File, proto.NewConn(conn, r)) // files_linux.go
	default:
		conn.Close()
	}
}

// control serves one control connection until it closes. The newest one
// takes over event delivery and the one before it is closed (a VM's shim
// reconnects after a template restore resets vsock).
func (c *Core) control(conn *proto.Conn) {
	c.mu.Lock()
	old := c.ctl
	c.ctl = conn
	ready := c.o.Configure == nil
	c.mu.Unlock()
	if old != nil {
		old.Close()
	}
	defer func() {
		c.mu.Lock()
		if c.ctl == conn {
			c.ctl = nil
		}
		c.mu.Unlock()
		conn.Close()
	}()
	if ready { // on this connection, whichever is newest by now
		c.sendMu.Lock()
		_ = conn.Send(proto.Msg{Op: "ready"})
		c.sendMu.Unlock()
	}
	for {
		var m proto.Msg
		if err := conn.RecvMax(&m, proto.MaxCommand); err != nil {
			if err == proto.ErrLineTooLong {
				c.o.Logf("a control line over %d bytes: connection dropped", proto.MaxCommand)
			}
			return
		}
		c.command(m)
	}
}

func (c *Core) command(m proto.Msg) {
	switch m.Op {
	case "config":
		if err := c.configure(m.Config); err != nil {
			c.send(proto.Msg{Op: "error", Error: "config: " + err.Error()})
			return
		}
		c.send(proto.Msg{Op: "ready"})
	case "exec":
		if m.Exec == nil {
			return
		}
		if err := c.startSession(*m.Exec); err != nil { // session.go
			c.send(proto.Msg{Op: "error", Session: m.Exec.Session, Error: err.Error()})
		}
	case "resize":
		if s := c.session(m.Session); s != nil {
			s.resize(m.Rows, m.Cols)
		}
	case "signal":
		if s := c.session(m.Session); s != nil {
			s.signal(m.Signal, m.Group)
		}
	case "sync":
		// the host is ending the sandbox (a terminal closed, xbind is
		// stopping it): hang up the session, flush, say so — it kills us next
		if s := c.session(m.Session); s != nil {
			s.signal(int(unix.SIGHUP), false)
		}
		if c.o.Sync != nil {
			c.o.Sync()
		}
		c.send(proto.Msg{Op: "synced"})
	case "dump":
		if c.o.Dump != nil {
			go func() { c.send(proto.Msg{Op: "dump", Dump: c.o.Dump()}) }()
		}
	}
}

func (c *Core) configure(cfg *proto.Config) error {
	if c.o.Configure == nil {
		return errors.New("this sandbox takes no config")
	}
	if cfg == nil {
		return errors.New("empty")
	}
	c.cfgMu.Lock()
	defer c.cfgMu.Unlock()
	if c.isConfigured() {
		return errors.New("already configured")
	}
	if err := c.o.Configure(*cfg); err != nil {
		return err
	}
	c.mu.Lock()
	c.configured = true
	c.mu.Unlock()
	return nil
}

func (c *Core) isConfigured() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.configured
}

// maxError bounds an event's error text (it may quote an argv).
const maxError = 4 << 10

// send delivers an event on the current control connection (none: dropped).
func (c *Core) send(m proto.Msg) {
	if len(m.Error) > maxError {
		m.Error = m.Error[:maxError] + "…"
	}
	c.mu.Lock()
	conn := c.ctl
	c.mu.Unlock()
	if conn == nil {
		return
	}
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	_ = conn.Send(m)
}

func (c *Core) session(id int) *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id]
}

// retiredMax bounds the ids remembered as used. The peer allocates ids from
// a counter and never reuses one (§2.6): this catches a stray late message,
// not a peer that wraps around.
const retiredMax = 4096

// retireLocked forgets session s (if id still names it) and marks id used.
// Callers hold c.mu.
func (c *Core) retireLocked(id int, s *session) {
	if c.sessions[id] == s {
		delete(c.sessions, id)
	}
	if !c.retired[id] {
		c.retired[id] = true
		c.retiredQ = append(c.retiredQ, id)
		if len(c.retiredQ) > retiredMax {
			delete(c.retired, c.retiredQ[0])
			c.retiredQ = c.retiredQ[1:]
		}
	}
}

// countsLocked reports the live sessions (their exec arrived) and the
// phantoms (streams only). Callers hold c.mu.
func (c *Core) countsLocked() (live, phantoms int) {
	for _, s := range c.sessions {
		if s.execReceived {
			live++
		} else {
			phantoms++
		}
	}
	return
}

// SessionInfo describes one session, for a dump.
type SessionInfo struct {
	ID       int
	Pid      int // 0: not started
	Argv     []string
	Listen   string
	Exec     bool // its exec arrived
	Attached int  // streams attached
	Expected int  // streams its exec expects
}

// Sessions lists the sessions, by id.
func (c *Core) Sessions() []SessionInfo {
	c.mu.Lock()
	all := make([]*session, 0, len(c.sessions))
	for _, s := range c.sessions {
		all = append(all, s)
	}
	c.mu.Unlock()
	out := make([]SessionInfo, 0, len(all))
	for _, s := range all {
		s.mu.Lock()
		si := SessionInfo{ID: s.id, Argv: s.ex.Argv, Listen: s.ex.Listen, Exec: s.execReceived, Attached: len(s.streams)}
		if s.execReceived {
			si.Expected = len(streamsOf(s.ex))
		}
		si.Pid = s.pid
		s.mu.Unlock()
		out = append(out, si)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
