//go:build linux

package host

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
	"golang.org/x/term"

	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
)

// ptyRig is a terminal session's input as the shim has it: a host PTY whose
// master is xbind's (it resizes, then writes keys) and whose slave is the
// shim's stdin, forwarded by a ptyInput to a "guest" that logs, in order,
// the resizes it is sent, its answers and the keys it gets.
type ptyRig struct {
	t       *testing.T
	master  *os.File
	resizes *proto.Resizes
	end     chan struct{}
	sent    chan proto.Msg

	mu  sync.Mutex
	log []string
}

func newPtyRig(t *testing.T, acks bool) *ptyRig {
	t.Helper()
	master, slave, err := pty.Open()
	if err != nil {
		t.Skipf("no PTY: %v", err)
	}
	t.Cleanup(func() { master.Close(); slave.Close() })
	if _, err := term.MakeRaw(int(slave.Fd())); err != nil { // as the shim's stdin is
		t.Fatal(err)
	}
	if err := pty.Setsize(master, &pty.Winsize{Rows: 10, Cols: 20}); err != nil {
		t.Fatal(err)
	}
	r := &ptyRig{t: t, master: master, resizes: &proto.Resizes{}, end: make(chan struct{}), sent: make(chan proto.Msg, 16)}
	if acks {
		r.resizes.Enable()
	}
	in := &ptyInput{
		size: func() (unix.Winsize, bool) {
			ws, err := unix.IoctlGetWinsize(int(slave.Fd()), unix.TIOCGWINSZ)
			if err != nil {
				return unix.Winsize{}, false
			}
			return *ws, true
		},
		send: func(m proto.Msg) error {
			r.note(fmt.Sprintf("resize %dx%d", m.Rows, m.Cols))
			r.sent <- m
			return nil
		},
		resizes: r.resizes, end: r.end, last: unix.Winsize{Row: 10, Col: 20},
	}
	go in.copy(writerFunc(func(p []byte) (int, error) { r.note("keys " + string(p)); return len(p), nil }), slave)
	return r
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func (r *ptyRig) note(s string) {
	r.mu.Lock()
	r.log = append(r.log, s)
	r.mu.Unlock()
}

func (r *ptyRig) logged() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.log...)
}

// xbindTypes is xbind's side: resize the host PTY, then write the keys.
func (r *ptyRig) xbindTypes(rows, cols uint16, keys string) {
	r.t.Helper()
	if err := pty.Setsize(r.master, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		r.t.Fatal(err)
	}
	if _, err := r.master.Write([]byte(keys)); err != nil {
		r.t.Fatal(err)
	}
}

// resize is the next resize the guest was sent.
func (r *ptyRig) resize() proto.Msg {
	r.t.Helper()
	select {
	case m := <-r.sent:
		return m
	case <-time.After(10 * time.Second):
		r.t.Fatalf("no resize sent; the guest's log %q", r.logged())
	}
	return proto.Msg{}
}

func (r *ptyRig) waitLog(n int) []string {
	r.t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if l := r.logged(); len(l) >= n {
			return l
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("the guest's log %q, want %d entries", r.logged(), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A VM terminal's resize reaches the guest before the keys typed after it,
// though the shim hears of it by a SIGWINCH that may come late (none comes
// here at all) and the guest answers slowly: the keys wait for the answer.
func TestPtyInputResizeBeforeKeys(t *testing.T) {
	r := newPtyRig(t, true)
	r.xbindTypes(40, 100, "hi")
	m := r.resize()
	if m.Op != "resize" || m.Session != 1 || m.Rows != 40 || m.Cols != 100 || m.Seq == 0 {
		t.Fatalf("sent %+v, want session 1's numbered resize to 40x100", m)
	}
	time.Sleep(100 * time.Millisecond) // the guest is slow to answer: the keys wait meanwhile
	r.note("answered")
	r.resizes.End(m.Seq)
	if l := r.waitLog(3); l[0] != "resize 40x100" || l[1] != "answered" || l[2] != "keys hi" {
		t.Fatalf("the guest's log %q: the keys overtook the resize", l)
	}
	// the same size again: nothing sent, nothing waited for
	if _, err := r.master.Write([]byte("yo")); err != nil {
		t.Fatal(err)
	}
	if l := r.waitLog(4); l[3] != "keys yo" {
		t.Fatalf("the guest's log %q", l)
	}
}

// A guest from before ResizeAcks gets its resizes unnumbered, still before
// the keys, and nothing waits; a session that ends ends a wait.
func TestPtyInputOlderGuestAndEnd(t *testing.T) {
	r := newPtyRig(t, false)
	r.xbindTypes(40, 100, "hi")
	if m := r.resize(); m.Seq != 0 {
		t.Fatalf("an older guest's resize numbered: %+v", m)
	}
	if l := r.waitLog(2); l[0] != "resize 40x100" || l[1] != "keys hi" {
		t.Fatalf("the guest's log %q", l)
	}

	e := newPtyRig(t, true)
	e.xbindTypes(50, 120, "x")
	e.resize() // never answered
	close(e.end)
	if l := e.waitLog(2); l[1] != "keys x" {
		t.Fatalf("the guest's log %q", l)
	}
}

// Resizes go to the guest in the order the terminal took them: a sync
// that read the size (the SIGWINCH handler) and was held up before it sent
// it can't send that size after a newer one another sync sent meanwhile
// (the input's, for the keys after the newer size) — the guest would be
// left at the stale size with the keys.
func TestPtyInputResizesInOrder(t *testing.T) {
	var mu sync.Mutex
	cur := unix.Winsize{Row: 40, Col: 100}
	inSize, release := make(chan struct{}), make(chan struct{})
	first := true
	var sent []proto.Msg
	p := &ptyInput{
		size: func() (unix.Winsize, bool) {
			mu.Lock()
			ws, hold := cur, first
			first = false
			mu.Unlock()
			if hold { // the handler read 40x100, and is held up here
				close(inSize)
				<-release
			}
			return ws, true
		},
		send: func(m proto.Msg) error {
			mu.Lock()
			sent = append(sent, m)
			mu.Unlock()
			return nil
		},
		resizes: &proto.Resizes{}, end: make(chan struct{}), last: unix.Winsize{Row: 10, Col: 20},
	}
	handler := make(chan struct{})
	go func() { p.sync(); close(handler) }()
	<-inSize
	mu.Lock()
	cur = unix.Winsize{Row: 50, Col: 120} // xbind resizes again, and types
	mu.Unlock()
	input := make(chan struct{})
	go func() { p.sync(); close(input) }()
	if p.mu.TryLock() { // the handler doesn't hold the lock while it reads: the input's sync runs through
		p.mu.Unlock()
		<-input
	}
	close(release)
	<-handler
	<-input
	mu.Lock()
	defer mu.Unlock()
	if len(sent) == 0 || sent[len(sent)-1].Rows != 50 || sent[len(sent)-1].Cols != 120 {
		t.Fatalf("sent %+v: the guest is left at a stale size, want 50x120 last", sent)
	}
}
