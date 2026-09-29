//go:build linux

package host

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// A resident VM is a tile sandbox's (plans/tile-sandbox-runtime.md §2.5):
// it runs no session 1. Once the guest is configured, the shim routes the
// connections xbind makes through the connection factory (HostSpec.AgentFD,
// sandbox.Factory) to the guest's agent, which runs the sandbox's execs as
// sessions 2 and up:
//
//   - "ctl" is xbind's control connection. The newest wins and the one
//     before it is closed; it gets "ready" at once (the guest is configured
//     already). Its exec, resize, signal and sync lines (≤ MaxCommand) go to
//     the guest over the shim's own control connection — the guest's only
//     one — and every event the guest sends (≤ MaxEvent, read by readCtl)
//     comes back up on it.
//   - "stream", "file" and "port" (D135: a TCP port on the guest's own
//     loopback, the ports capability) are dialled to the guest with the
//     same Hello, then spliced both ways with half-close passed on. The
//     shim never parses what flows through them.
//   - anything else ("listen": there is no backend socket or gateway in a
//     tile sandbox) is closed, and so is anything naming a session below 2
//     (backends' and terminals').
//
// An exec ending ends nothing else: the VM lives until xbind hangs up. The
// factory's EOF (xbind is gone) or SIGHUP (xbind stopping it; SIGTERM and
// SIGINT alike) makes the shim ask the guest to flush its disks, wait for
// that at most 2 s (6× emulated) and exit 128+signal — 129 for SIGHUP and
// for the EOF. A VMM that exits makes it exit 125 with the console tail.

// firstSession is the lowest session a resident VM runs.
const firstSession = 2

// Bounds the shim keeps with xbind.
const (
	helloWait = 30 * time.Second // an accepted connection's Hello must arrive within
	upWrite   = 10 * time.Second // an event line upstream must be taken within, or xbind's ctl is dropped
)

// adoptFactory takes the factory the shim inherited on fd: it moves it to a
// close-on-exec descriptor (the VMM must never hold it) and checks that it
// is one.
func adoptFactory(fd int) (*os.File, error) {
	if fd <= 2 {
		return nil, errors.New("a resident VM needs its factory (no agentFd in the spec)")
	}
	nfd, err := unix.FcntlInt(uintptr(fd), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return nil, fmt.Errorf("fd %d: %w", fd, err)
	}
	unix.Close(fd)
	if typ, err := unix.GetsockoptInt(nfd, unix.SOL_SOCKET, unix.SO_TYPE); err != nil || typ != unix.SOCK_SEQPACKET {
		unix.Close(nfd)
		return nil, fmt.Errorf("fd %d is not a connection factory", fd)
	}
	return os.NewFile(uintptr(nfd), "sbx-factory"), nil
}

// resident routes xbind's connections until xbind hangs up or the VM dies,
// and returns the shim's exit code.
func (s *shim) resident() int {
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, unix.SIGHUP, unix.SIGTERM, unix.SIGINT)
	r := &router{
		guest:  s.ctl,
		events: s.in,
		dial: func(h proto.Hello) (net.Conn, error) {
			return s.dialAgent(h, s.slow(5*time.Second))
		},
		accept:   func() (net.Conn, error) { return sandbox.AcceptFrom(s.factory) },
		vmmDone:  s.vmmDone,
		syncWait: s.slow(2 * time.Second),
		logf: func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "vm sandbox: "+format+"\n", args...)
		},
	}
	code, err := r.run(sigs)
	if err != nil {
		return fail(s, "%v", err)
	}
	return code
}

// router is a resident VM's routing between xbind and the guest; its
// pieces are the shim's (a test's fakes in resident_linux_test.go).
type router struct {
	guest    *proto.Conn                         // the guest's only control connection
	events   <-chan ctlMsg                       // the guest's events (readCtl)
	dial     func(proto.Hello) (net.Conn, error) // a new connection to the guest's agent, Hello sent
	accept   func() (net.Conn, error)            // the factory's next connection; an error: xbind is gone
	vmmDone  <-chan struct{}
	syncWait time.Duration
	logf     func(string, ...any)

	gmu sync.Mutex // one line at a time to the guest

	umu sync.Mutex // guards up; one line at a time upstream
	up  *upstream  // xbind's newest control connection (nil: none)

	syncs atomic.Int64 // syncs sent to the guest and not answered yet
}

type upstream struct {
	c  net.Conn
	pc *proto.Conn
}

// run serves until xbind hangs up (the exit code) or the VM fails (the
// error).
func (r *router) run(sigs <-chan os.Signal) (int, error) {
	gone := make(chan error, 1)
	go func() {
		for {
			c, err := r.accept()
			if err != nil {
				gone <- err
				return
			}
			go r.route(c)
		}
	}()
	for {
		select {
		case m := <-r.events:
			if m.err != nil {
				return 0, fmt.Errorf("guest agent: %w", m.err)
			}
			r.event(m.m)
		case <-r.vmmDone:
			return 0, errors.New("the VM exited")
		case sig := <-sigs:
			return r.shutdown(128 + int(sig.(unix.Signal))), nil
		case err := <-gone:
			if !errors.Is(err, io.EOF) {
				r.logf("connection factory: %v", err)
			}
			return r.shutdown(128 + int(unix.SIGHUP)), nil
		}
	}
}

// shutdown asks the guest to flush its disks — session 0: every session
// runs on until the VM is killed — and waits for "synced", briefly. Events
// still go upstream meanwhile.
func (r *router) shutdown(code int) int {
	r.syncs.Add(1)
	sent := make(chan error, 1)
	go func() { sent <- r.toGuest(proto.Msg{Op: "sync"}) }() // a guest that doesn't read can't hold the exit
	deadline := time.NewTimer(r.syncWait)
	defer deadline.Stop()
	for {
		select {
		case err := <-sent:
			if err != nil {
				return code
			}
		case m := <-r.events:
			switch {
			case m.err != nil:
				return code
			case m.m.Op == "synced":
				if r.answered() == 0 {
					return code // ours: the guest answers its syncs in order
				}
				r.sendUp(m.m)
			default:
				r.event(m.m)
			}
		case <-r.vmmDone:
			return code
		case <-deadline.C:
			return code
		}
	}
}

// answered counts one sync off as answered and returns how many are left.
func (r *router) answered() int64 {
	for {
		n := r.syncs.Load()
		if n <= 0 {
			return 0
		}
		if r.syncs.CompareAndSwap(n, n-1) {
			return n - 1
		}
	}
}

// event passes a guest event up to xbind (none connected: dropped). Its
// line stays within the MaxEvent xbind reads with: re-encoding can grow what
// the guest sent raw ("<" becomes "\u003c"), and past the bound it is
// dropped here rather than cutting xbind's ctl.
func (r *router) event(m proto.Msg) {
	if m.Op == "synced" {
		r.answered()
	}
	if b, err := json.Marshal(m); err != nil || len(b) >= proto.MaxEvent {
		r.logf("a guest event over %d bytes re-encoded: dropped", proto.MaxEvent)
		return
	}
	r.sendUp(m)
}

func (r *router) sendUp(m proto.Msg) {
	r.umu.Lock()
	defer r.umu.Unlock()
	u := r.up
	if u == nil {
		return
	}
	_ = u.c.SetWriteDeadline(time.Now().Add(upWrite))
	if err := u.pc.Send(m); err != nil {
		r.logf("xbind's control connection: %v (dropped)", err)
		u.c.Close()
		r.up = nil
	}
}

func (r *router) toGuest(m proto.Msg) error {
	r.gmu.Lock()
	defer r.gmu.Unlock()
	return r.guest.Send(m)
}

// route serves one connection from the factory by the kind its Hello names.
func (r *router) route(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(helloWait))
	h, br, err := proto.ReadHelloMax(c, proto.MaxHello)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	switch {
	case h.Kind == "ctl":
		r.control(c, br)
	case h.Kind == "stream" && h.Session >= firstSession, h.Kind == "file", h.Kind == "port" && h.Port > 0 && h.Port <= 65535:
		// the same Hello, as the shim read it (unknown fields don't pass)
		g, err := r.dial(proto.Hello{Kind: h.Kind, Session: h.Session, Stream: h.Stream, File: h.File, Port: h.Port})
		if err != nil {
			r.logf("%s connection to the guest: %v", h.Kind, err)
			c.Close()
			return
		}
		splice(bufConn{Conn: c, r: br}, g)
	default:
		c.Close()
	}
}

// control serves xbind's control connection until it closes or a newer one
// replaces it.
func (r *router) control(c net.Conn, br *bufio.Reader) {
	u := &upstream{c: c, pc: proto.NewConn(c, br)}
	r.umu.Lock()
	old := r.up
	r.up = u
	_ = c.SetWriteDeadline(time.Now().Add(upWrite))
	err := u.pc.Send(proto.Msg{Op: "ready"}) // before any event reaches it
	r.umu.Unlock()
	if old != nil {
		old.c.Close()
	}
	defer func() {
		r.umu.Lock()
		if r.up == u {
			r.up = nil
		}
		r.umu.Unlock()
		c.Close()
	}()
	if err != nil {
		return
	}
	for {
		var m proto.Msg
		if err := u.pc.RecvMax(&m, proto.MaxCommand); err != nil {
			if err == proto.ErrLineTooLong {
				r.logf("a control line from xbind over %d bytes: connection dropped", proto.MaxCommand)
			}
			return
		}
		r.command(m)
	}
}

// command passes one of xbind's control lines to the guest — rebuilt from
// the fields its op uses — or refuses it with an "error" event.
func (r *router) command(m proto.Msg) {
	switch m.Op {
	case "exec":
		ex := m.Exec
		switch {
		case ex == nil:
			r.refuse(0, "exec: no exec")
		case ex.Session < firstSession:
			r.refuse(ex.Session, fmt.Sprintf("exec: a resident VM's sessions start at %d", firstSession))
		case ex.Listen != "" || ex.Gateway != "":
			r.refuse(ex.Session, "exec: a resident VM has no listen socket or gateway")
		default:
			_ = r.toGuest(proto.Msg{Op: "exec", Exec: ex})
		}
	case "resize", "signal":
		if m.Session < firstSession {
			r.refuse(m.Session, fmt.Sprintf("%s: a resident VM's sessions start at %d", m.Op, firstSession))
			return
		}
		_ = r.toGuest(proto.Msg{Op: m.Op, Session: m.Session, Rows: m.Rows, Cols: m.Cols, Signal: m.Signal, Group: m.Group})
	case "sync": // session 0 flushes only; a session hangs it up first
		if m.Session != 0 && m.Session < firstSession {
			r.refuse(0, fmt.Sprintf("sync: a resident VM's sessions start at %d", firstSession))
			return
		}
		r.syncs.Add(1)
		if err := r.toGuest(proto.Msg{Op: "sync", Session: m.Session}); err != nil {
			r.answered()
		}
	default:
		op := m.Op
		if len(op) > 32 {
			op = op[:32]
		}
		r.refuse(0, "a resident VM takes no "+strconv.Quote(op))
	}
}

// refuse tells xbind a command was not passed on ("error" for session, 0
// for none).
func (r *router) refuse(session int, msg string) {
	r.sendUp(proto.Msg{Op: "error", Session: session, Error: msg})
}

// bufConn is an accepted connection read through the reader that took its
// Hello, so what xbind sent past that line isn't lost; it half-closes.
type bufConn struct {
	net.Conn
	r *bufio.Reader
}

func (b bufConn) Read(p []byte) (int, error) { return b.r.Read(p) }

func (b bufConn) CloseWrite() error {
	if cw, ok := b.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}
