//go:build linux && integration

package vm

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// rclient is a resident VM's client the way the tile-sandbox runtime is
// one: every connection through the factory, one ctl whose events are
// dispatched by session, sessions allocated from 2 and never reused.
type rclient struct {
	f      *sandbox.Factory
	ctl    *proto.Conn
	exited <-chan struct{}

	mu   sync.Mutex
	next int
	subs map[int]chan proto.Msg
}

// newRClient opens the ctl and waits for "ready" (the VM boots meanwhile).
func newRClient(t *testing.T, f *sandbox.Factory, exited <-chan struct{}) *rclient {
	t.Helper()
	c := &rclient{f: f, exited: exited, next: 1, subs: map[int]chan proto.Msg{}}
	conn := c.dial(t, proto.Hello{Kind: "ctl"})
	c.ctl = proto.NewConn(conn, nil)
	ready := make(chan error, 1)
	go func() {
		var m proto.Msg
		err := c.ctl.RecvMax(&m, proto.MaxEvent)
		if err == nil && m.Op != "ready" {
			err = io.ErrUnexpectedEOF
		}
		ready <- err
		if err != nil {
			return
		}
		for { // events, by session
			var m proto.Msg
			if err := c.ctl.RecvMax(&m, proto.MaxEvent); err != nil {
				return
			}
			c.mu.Lock()
			ch := c.subs[m.Session]
			c.mu.Unlock()
			if ch != nil {
				ch <- m
			}
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatalf("no ready: %v", err)
		}
	case <-exited:
		t.Fatal("the VM exited while booting")
	case <-time.After(vmTimeout):
		t.Fatalf("no ready within %s", vmTimeout)
	}
	return c
}

// dial makes a connection through the factory and sends its Hello (a full
// factory queue is retried: the shim accepts in a moment).
func (c *rclient) dial(t *testing.T, h proto.Hello) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if conn, err = c.f.Dial(); err == nil || time.Now().After(deadline) {
			break
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	line, _ := json.Marshal(h)
	if _, err := conn.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
	return conn
}

func (c *rclient) send(t *testing.T, m proto.Msg) {
	t.Helper()
	if err := c.ctl.Send(m); err != nil {
		t.Fatal(err)
	}
}

// execution is one session: its streams and its events.
type execution struct {
	id                         int
	events                     chan proto.Msg
	pty, stdin, stdout, stderr net.Conn
	exited                     <-chan struct{}
}

// start dials ex's streams, then sends it.
func (c *rclient) start(t *testing.T, ex proto.Exec) *execution {
	t.Helper()
	c.mu.Lock()
	c.next++
	x := &execution{id: c.next, events: make(chan proto.Msg, 8), exited: c.exited}
	c.subs[x.id] = x.events
	c.mu.Unlock()
	ex.Session = x.id
	stream := func(name string) net.Conn {
		return c.dial(t, proto.Hello{Kind: "stream", Session: x.id, Stream: name})
	}
	if ex.TTY {
		x.pty = stream("pty")
	} else {
		if !ex.NoStdin {
			x.stdin = stream("stdin")
		}
		x.stdout = stream("stdout")
		if !ex.Merge {
			x.stderr = stream("stderr")
		}
	}
	c.send(t, proto.Msg{Op: "exec", Exec: &ex})
	return x
}

// wait returns the session's end: its exit (code, signal) or its error.
func (x *execution) wait(t *testing.T) (code, sig int, err string) {
	t.Helper()
	for {
		select {
		case m := <-x.events:
			switch m.Op {
			case "exited":
				return m.Code, m.Signal, ""
			case "error":
				return -1, 0, m.Error
			}
		case <-x.exited:
			t.Fatalf("session %d: the VM exited", x.id)
		case <-time.After(vmTimeout):
			t.Fatalf("session %d didn't end within %s", x.id, vmTimeout)
		}
	}
}

// expect reads from c until what it read holds want, and returns it.
func (x *execution) expect(t *testing.T, c net.Conn, want string) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(vmTimeout))
	defer c.SetReadDeadline(time.Time{})
	var got []byte
	b := make([]byte, 256)
	for !strings.Contains(string(got), want) {
		n, err := c.Read(b)
		got = append(got, b[:n]...)
		if err != nil {
			t.Fatalf("session %d: read %q, then %v (want %q)", x.id, got, err, want)
		}
	}
	return string(got)
}

type execResult struct {
	code, sig      int
	err            string
	stdout, stderr string
}

// started waits for the session's "started": whatever the agent does
// before it says so (going back to its own oom_score_adj after the
// session's clone, say) is done.
func (x *execution) started(t *testing.T) {
	t.Helper()
	for {
		select {
		case m := <-x.events:
			switch m.Op {
			case "started":
				return
			case "exited", "error":
				t.Fatalf("session %d ended before it started: %+v", x.id, m)
			}
		case <-x.exited:
			t.Fatalf("session %d: the VM exited", x.id)
		case <-time.After(vmTimeout):
			t.Fatalf("session %d didn't start within %s", x.id, vmTimeout)
		}
	}
}

// run runs ex to its end with stdin as its input.
func (c *rclient) run(t *testing.T, ex proto.Exec, stdin string) execResult {
	return c.runSent(t, ex, stdin, false)
}

// runStarted is run with stdin sent once the session has started.
func (c *rclient) runStarted(t *testing.T, ex proto.Exec, stdin string) execResult {
	return c.runSent(t, ex, stdin, true)
}

func (c *rclient) runSent(t *testing.T, ex proto.Exec, stdin string, afterStart bool) execResult {
	t.Helper()
	x := c.start(t, ex)
	if afterStart {
		x.started(t)
	}
	var wg sync.WaitGroup
	var out, errb bytes.Buffer
	for _, s := range []struct {
		c net.Conn
		b *bytes.Buffer
	}{{x.stdout, &out}, {x.stderr, &errb}} {
		if s.c != nil {
			wg.Add(1)
			go func() { defer wg.Done(); _, _ = io.Copy(s.b, s.c) }()
		}
	}
	if x.stdin != nil {
		_, _ = x.stdin.Write([]byte(stdin))
		_ = x.stdin.(*net.UnixConn).CloseWrite()
	}
	var r execResult
	r.code, r.sig, r.err = x.wait(t)
	if r.err != "" {
		for _, s := range []net.Conn{x.stdin, x.stdout, x.stderr} {
			if s != nil {
				s.Close()
			}
		}
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Errorf("session %d: its output streams didn't end", x.id)
	}
	r.stdout, r.stderr = out.String(), errb.String()
	return r
}

// file runs one file operation: data is what a write or tar-put sends; a
// read's or tar-get's data comes back.
func (c *rclient) file(t *testing.T, op proto.FileOp, data []byte) (proto.FileResult, []byte) {
	t.Helper()
	conn := c.dial(t, proto.Hello{Kind: "file", File: &op})
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(vmTimeout))
	pc := proto.NewConn(conn, nil)
	var res proto.FileResult
	switch op.Op {
	case "write", "tar-put":
		fw := proto.NewFrameWriter(conn)
		_, _ = fw.Write(data)
		_ = fw.Close()
		if err := pc.RecvMax(&res, proto.MaxResult); err != nil {
			t.Fatalf("%s %s: %v", op.Op, op.Path, err)
		}
		return res, nil
	case "read", "tar-get":
		if err := pc.RecvMax(&res, proto.MaxResult); err != nil {
			t.Fatalf("%s %s: %v", op.Op, op.Path, err)
		}
		if !res.OK { // a refusal: nothing follows
			return res, nil
		}
		got, err := io.ReadAll(proto.NewFrameReader(pc.Reader()))
		if err != nil {
			t.Fatalf("%s %s: data: %v", op.Op, op.Path, err)
		}
		var end proto.FileResult
		if err := pc.RecvMax(&end, proto.MaxResult); err != nil {
			t.Fatalf("%s %s: the final line: %v", op.Op, op.Path, err)
		}
		if !end.OK {
			return end, got
		}
		return res, got
	default:
		if err := pc.RecvMax(&res, proto.MaxResult); err != nil {
			t.Fatalf("%s %s: %v", op.Op, op.Path, err)
		}
		return res, nil
	}
}
