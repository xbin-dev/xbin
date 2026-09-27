//go:build linux

package host

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// The resident router (resident_linux.go) between a real connection factory
// and a fake guest: the guest's control connection and every connection
// the shim dials to it are socketpairs the test holds the far end of.

type rig struct {
	t       *testing.T
	f       *sandbox.Factory
	guest   *proto.Conn    // the guest's end of the shim's ctl
	gconn   net.Conn       // (raw, for deadlines)
	dialled chan fakeConn  // connections the shim made to the guest
	sigs    chan os.Signal // the shim's signals
	vmm     chan struct{}  // closed: the VMM exited
	done    chan result
	logs    *logBuf
}

type fakeConn struct {
	h proto.Hello
	c net.Conn
	r *bufio.Reader
}

type result struct {
	code int
	err  error
}

type logBuf struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(&l.b, format+"\n", args...)
}

// pair is a connected pair of unix stream sockets (they half-close).
func pair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	conn := func(fd int) net.Conn {
		f := os.NewFile(uintptr(fd), "pair")
		defer f.Close()
		c, err := net.FileConn(f)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	return conn(fds[0]), conn(fds[1])
}

// newRig starts a router; the test's cleanup closes the factory (xbind is
// gone) and waits for the router's end.
func newRig(t *testing.T, syncWait time.Duration) *rig {
	shimEnd, guestEnd := pair(t)
	s := &shim{hs: proto.HostSpec{Resident: true}, ctl: proto.NewConn(shimEnd, nil), in: make(chan ctlMsg), dumps: make(chan string, 1)}
	go s.readCtl()
	f, child, err := sandbox.NewFactory()
	if err != nil {
		t.Fatal(err)
	}
	rg := &rig{t: t, f: f, guest: proto.NewConn(guestEnd, nil), gconn: guestEnd,
		dialled: make(chan fakeConn, 64), sigs: make(chan os.Signal, 1), vmm: make(chan struct{}),
		done: make(chan result, 1), logs: &logBuf{}}
	r := &router{
		guest:  s.ctl,
		events: s.in,
		dial: func(h proto.Hello) (net.Conn, error) {
			a, b := pair(t)
			line, _ := json.Marshal(h) // as dialAgent sends it
			if _, err := a.Write(append(line, '\n')); err != nil {
				return nil, err
			}
			got, br, err := proto.ReadHello(b)
			if err != nil {
				return nil, err
			}
			rg.dialled <- fakeConn{got, b, br}
			return a, nil
		},
		accept:   func() (net.Conn, error) { return sandbox.AcceptFrom(child) },
		vmmDone:  rg.vmm,
		syncWait: syncWait,
		logf:     rg.logs.logf,
	}
	go func() {
		code, err := r.run(rg.sigs)
		rg.done <- result{code, err}
	}()
	t.Cleanup(func() {
		f.Close()
		select {
		case <-rg.done:
		case <-time.After(5 * time.Second):
			t.Error("the router didn't end after the factory closed")
		}
		child.Close()
		shimEnd.Close()
		guestEnd.Close()
	})
	return rg
}

// dial makes one connection through the factory and sends its Hello (plus
// extra bytes in the same write).
func (rg *rig) dial(h proto.Hello, extra string) net.Conn {
	rg.t.Helper()
	c, err := rg.f.Dial()
	if err != nil {
		rg.t.Fatal(err)
	}
	rg.t.Cleanup(func() { c.Close() })
	line, _ := json.Marshal(h)
	if _, err := c.Write(append(append(line, '\n'), extra...)); err != nil {
		rg.t.Fatal(err)
	}
	return c
}

// ctl opens a control connection and reads its "ready".
func (rg *rig) ctl() (*proto.Conn, net.Conn) {
	rg.t.Helper()
	c := rg.dial(proto.Hello{Kind: "ctl"}, "")
	pc := proto.NewConn(c, nil)
	if m := recvMsg(rg.t, pc, c); m.Op != "ready" {
		rg.t.Fatalf("a new ctl got %+v, want ready", m)
	}
	return pc, c
}

func recvMsg(t *testing.T, pc *proto.Conn, c net.Conn) proto.Msg {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	defer c.SetReadDeadline(time.Time{})
	var m proto.Msg
	if err := pc.Recv(&m); err != nil {
		t.Fatalf("recv: %v", err)
	}
	return m
}

// guestRecv is the next line the guest's agent gets from the shim.
func (rg *rig) guestRecv() proto.Msg {
	rg.t.Helper()
	return recvMsg(rg.t, rg.guest, rg.gconn)
}

func (rg *rig) nextDialled() fakeConn {
	rg.t.Helper()
	select {
	case fc := <-rg.dialled:
		rg.t.Cleanup(func() { fc.c.Close() })
		return fc
	case <-time.After(5 * time.Second):
		rg.t.Fatal("the shim dialled nothing to the guest")
	}
	return fakeConn{}
}

// closed reports whether c reads EOF (or an error) within a while.
func closed(c net.Conn) bool {
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var b [1]byte
	_, err := c.Read(b[:])
	return err != nil && !os.IsTimeout(err)
}

func TestResidentControl(t *testing.T) {
	rg := newRig(t, 200*time.Millisecond)
	a, ac := rg.ctl()

	// sessions below 2 are refused and never reach the guest; so are a
	// listen socket and a gateway
	for _, ex := range []proto.Exec{
		{Session: 1, Argv: []string{"sh"}},
		{Session: 0, Argv: []string{"sh"}},
		{Session: 3, Argv: []string{"sh"}, Listen: "/run/x.sock"},
		{Session: 3, Argv: []string{"sh"}, Gateway: "/run/gw.sock"},
	} {
		if err := a.Send(proto.Msg{Op: "exec", Exec: &ex}); err != nil {
			t.Fatal(err)
		}
		if m := recvMsg(t, a, ac); m.Op != "error" || m.Session != ex.Session {
			t.Errorf("exec %+v: got %+v, want an error for its session", ex, m)
		}
	}
	for _, m := range []proto.Msg{
		{Op: "signal", Session: 1, Signal: 15},
		{Op: "resize", Session: 1, Rows: 5, Cols: 5},
		{Op: "sync", Session: 1},
		{Op: "config", Config: &proto.Config{}},
		{Op: "dump"},
	} {
		_ = a.Send(m)
		if got := recvMsg(t, a, ac); got.Op != "error" {
			t.Errorf("%+v: got %+v, want an error", m, got)
		}
	}
	// what passes, passes as the fields its op uses
	_ = a.Send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 2, Argv: []string{"true"}, CwdStrict: true, NoSync: true}})
	if m := rg.guestRecv(); m.Op != "exec" || m.Exec == nil || m.Exec.Session != 2 || !m.Exec.CwdStrict || !m.Exec.NoSync {
		t.Fatalf("the guest got %+v, want session 2's exec (and nothing refused before it)", m)
	}
	_ = a.Send(proto.Msg{Op: "signal", Session: 2, Signal: 15, Group: true, Error: "junk"})
	if m := rg.guestRecv(); m.Op != "signal" || m.Session != 2 || m.Signal != 15 || !m.Group || m.Error != "" {
		t.Errorf("the guest got %+v, want a group signal for session 2", m)
	}
	_ = a.Send(proto.Msg{Op: "resize", Session: 2, Rows: 40, Cols: 100})
	if m := rg.guestRecv(); m.Op != "resize" || m.Rows != 40 || m.Cols != 100 {
		t.Errorf("the guest got %+v, want a resize", m)
	}
	_ = a.Send(proto.Msg{Op: "sync"})
	if m := rg.guestRecv(); m.Op != "sync" || m.Session != 0 {
		t.Errorf("the guest got %+v, want a sync", m)
	}

	// events go up
	for _, ev := range []proto.Msg{{Op: "started", Session: 2, Pid: 42}, {Op: "exited", Session: 2, Code: 143, Signal: 15}, {Op: "synced"}} {
		_ = rg.guest.Send(ev)
		if m := recvMsg(t, a, ac); m.Op != ev.Op || m.Session != ev.Session || m.Pid != ev.Pid || m.Signal != ev.Signal {
			t.Errorf("xbind got %+v, want %+v", m, ev)
		}
	}

	// the newest ctl wins: the one before it is closed, events go to it
	b, bc := rg.ctl()
	if !closed(ac) {
		t.Error("the older ctl is still open")
	}
	_ = rg.guest.Send(proto.Msg{Op: "exited", Session: 3, Code: 1})
	if m := recvMsg(t, b, bc); m.Op != "exited" || m.Session != 3 {
		t.Errorf("the newest ctl got %+v", m)
	}

	// an exec ending ends nothing: the router still runs
	select {
	case r := <-rg.done:
		t.Fatalf("the router ended: %+v", r)
	default:
	}
}

func TestResidentStreams(t *testing.T) {
	rg := newRig(t, 200*time.Millisecond)

	// a stream: the same Hello to the guest, the bytes sent with it too,
	// both ways, half-close passed on
	up := rg.dial(proto.Hello{Kind: "stream", Session: 2, Stream: "stdin"}, "early bytes|")
	g := rg.nextDialled()
	if g.h.Kind != "stream" || g.h.Session != 2 || g.h.Stream != "stdin" {
		t.Fatalf("the guest got hello %+v", g.h)
	}
	if _, err := up.Write([]byte("later")); err != nil {
		t.Fatal(err)
	}
	_ = up.(*net.UnixConn).CloseWrite()
	_ = g.c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(g.r)
	if err != nil || string(got) != "early bytes|later" {
		t.Errorf("the guest read %q, %v", got, err)
	}
	_, _ = g.c.Write([]byte("back"))
	_ = g.c.(*net.UnixConn).CloseWrite()
	_ = up.SetReadDeadline(time.Now().Add(5 * time.Second))
	if got, err := io.ReadAll(up); err != nil || string(got) != "back" {
		t.Errorf("xbind read %q, %v", got, err)
	}

	// a file operation, with its frames
	op := &proto.FileOp{Op: "write", Path: "/x", Mkdirs: true}
	up = rg.dial(proto.Hello{Kind: "file", File: op}, "\x00\x00\x00\x02hi\x00\x00\x00\x00")
	g = rg.nextDialled()
	if g.h.Kind != "file" || g.h.File == nil || g.h.File.Op != "write" || g.h.File.Path != "/x" || !g.h.File.Mkdirs {
		t.Fatalf("the guest got hello %+v", g.h)
	}
	data, err := proto.ReadFrame(g.r, nil)
	if err != nil || string(data) != "hi" {
		t.Errorf("the guest read frame %q, %v", data, err)
	}
	if _, err := proto.ReadFrame(g.r, nil); err != io.EOF {
		t.Errorf("no terminator: %v", err)
	}
	_ = proto.NewConn(g.c, nil).Send(proto.FileResult{OK: true})
	_ = up.SetReadDeadline(time.Now().Add(5 * time.Second))
	var res proto.FileResult
	if err := proto.NewConn(up, nil).Recv(&res); err != nil || !res.OK {
		t.Errorf("xbind got %+v, %v", res, err)
	}

	// refused: a stream of a session below 2, a listen connection, an
	// unknown kind, an oversized or broken Hello — none reaches the guest
	for _, c := range []net.Conn{
		rg.dial(proto.Hello{Kind: "stream", Session: 1, Stream: "stdout"}, ""),
		rg.dial(proto.Hello{Kind: "stream", Stream: "stdout"}, ""),
		rg.dial(proto.Hello{Kind: "listen", Session: 2}, ""),
		rg.dial(proto.Hello{Kind: "mystery"}, ""),
		rg.dial(proto.Hello{Kind: "ctl", Stream: strings.Repeat("x", proto.MaxHello)}, ""),
	} {
		if !closed(c) {
			t.Error("a refused connection stayed open")
		}
	}
	select {
	case fc := <-rg.dialled:
		t.Errorf("a refused connection reached the guest: %+v", fc.h)
	default:
	}
}

func TestResidentBounds(t *testing.T) {
	rg := newRig(t, 200*time.Millisecond)
	a, ac := rg.ctl()
	// an upstream line past MaxCommand drops that connection only
	huge := proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 2, Argv: []string{strings.Repeat("a", proto.MaxCommand)}}}
	_ = a.Send(huge)
	if !closed(ac) {
		t.Error("a ctl sending a line past MaxCommand is still open")
	}
	b, bc := rg.ctl()
	_ = b.Send(proto.Msg{Op: "exec", Exec: &proto.Exec{Session: 2, Argv: []string{"true"}}})
	if m := rg.guestRecv(); m.Op != "exec" || m.Exec.Session != 2 {
		t.Errorf("the guest got %+v", m)
	}
	// a guest event within MaxEvent that re-encodes past it ("<" sent raw
	// becomes "\u003c") is dropped, and xbind's ctl stays up
	raw := `{"op":"exited","session":2,"error":"` + strings.Repeat("<", proto.MaxEvent/2) + "\"}\n"
	if _, err := rg.gconn.Write([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	_ = rg.guest.Send(proto.Msg{Op: "exited", Session: 3, Code: 7})
	if m := recvMsg(t, b, bc); m.Op != "exited" || m.Session != 3 || m.Code != 7 {
		t.Errorf("xbind got %+v (%d bytes of error), want session 3's exit", m.Op, len(m.Error))
	}
	// a guest event past MaxEvent ends the VM (nothing after it can be trusted)
	_ = rg.guest.Send(proto.Msg{Op: "exited", Session: 2, Error: strings.Repeat("e", proto.MaxEvent)})
	select {
	case r := <-rg.done:
		rg.done <- r // for the cleanup
		if r.err == nil {
			t.Errorf("the router ended with %d, want an error", r.code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("an oversized guest event didn't end the router")
	}
}

func TestResidentHangup(t *testing.T) {
	t.Run("factory EOF", func(t *testing.T) {
		rg := newRig(t, 5*time.Second)
		_, _ = rg.ctl()
		start := time.Now()
		rg.f.Close() // xbind is gone
		if m := rg.guestRecv(); m.Op != "sync" || m.Session != 0 {
			t.Fatalf("the guest got %+v, want a flush-only sync", m)
		}
		_ = rg.guest.Send(proto.Msg{Op: "synced"})
		r := <-rg.done
		rg.done <- r
		if r.err != nil || r.code != 129 {
			t.Errorf("the router ended with %+v, want 129", r)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("it waited %s past synced", d)
		}
	})
	t.Run("SIGHUP, xbind's own sync in flight", func(t *testing.T) {
		rg := newRig(t, 5*time.Second)
		a, ac := rg.ctl()
		_ = a.Send(proto.Msg{Op: "sync", Session: 2})
		if m := rg.guestRecv(); m.Op != "sync" || m.Session != 2 {
			t.Fatalf("the guest got %+v", m)
		}
		rg.sigs <- unix.SIGHUP
		if m := rg.guestRecv(); m.Op != "sync" || m.Session != 0 {
			t.Fatalf("the guest got %+v, want the shim's sync", m)
		}
		_ = rg.guest.Send(proto.Msg{Op: "synced"}) // xbind's: passed up, the shim waits on
		if m := recvMsg(t, a, ac); m.Op != "synced" {
			t.Errorf("xbind got %+v, want its synced", m)
		}
		select {
		case r := <-rg.done:
			t.Fatalf("the router ended on xbind's synced: %+v", r)
		case <-time.After(100 * time.Millisecond):
		}
		_ = rg.guest.Send(proto.Msg{Op: "synced"})
		r := <-rg.done
		rg.done <- r
		if r.err != nil || r.code != 129 {
			t.Errorf("the router ended with %+v, want 129", r)
		}
	})
	t.Run("no answer", func(t *testing.T) {
		rg := newRig(t, 200*time.Millisecond)
		rg.sigs <- unix.SIGTERM
		start := time.Now()
		r := <-rg.done
		rg.done <- r
		if r.err != nil || r.code != 128+int(unix.SIGTERM) {
			t.Errorf("the router ended with %+v, want %d", r, 128+int(unix.SIGTERM))
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("a silent guest held the exit for %s", d)
		}
	})
	t.Run("VMM exit", func(t *testing.T) {
		rg := newRig(t, 200*time.Millisecond)
		close(rg.vmm)
		r := <-rg.done
		rg.done <- r
		if r.err == nil {
			t.Errorf("the router ended with %+v, want an error", r)
		}
	})
}
