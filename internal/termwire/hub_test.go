package termwire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// rig serves one hub over httptest: every connection is Upgraded (with
// limit) and attached with a fixed hello; the fake terminal records input
// and resizes, and with echo on writes its input back as output, as a PTY
// in cooked mode does.
type rig struct {
	h       *Hub
	srv     *httptest.Server
	in      chan []byte
	resizes chan [2]uint16
	echo    atomic.Bool
}

func newRig(t *testing.T, tail int, limit int64) *rig {
	t.Helper()
	r := &rig{h: NewHub(tail), in: make(chan []byte, 256), resizes: make(chan [2]uint16, 16)}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := Upgrade(w, req, http.Header{"X-XBin-Session": {"s1"}}, limit)
		if err != nil {
			return
		}
		r.h.Attach(conn, map[string]any{"id": "s1", "sandbox": "box", "op": "not-this", "echoAck": false}, Terminal{
			Write: func(b []byte) error {
				r.in <- b
				if r.echo.Load() {
					r.h.Output(b)
				}
				return nil
			},
			Resize: func(cols, rows uint16) { r.resizes <- [2]uint16{cols, rows} },
		})
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// maxQueued is the longest client queue right now.
func (h *Hub) maxQueued() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for c := range h.clients {
		n = max(n, len(c.send))
	}
	return n
}

func (r *rig) dialWith(t *testing.T, d *websocket.Dialer) *websocket.Conn {
	t.Helper()
	c, resp, err := d.Dial("ws"+strings.TrimPrefix(r.srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Header.Get("X-XBin-Session") != "s1" {
		t.Fatalf("upgrade header: %v", resp.Header)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (r *rig) dial(t *testing.T) *websocket.Conn { return r.dialWith(t, websocket.DefaultDialer) }

// next reads one message (5 s at most).
func next(t *testing.T, c *websocket.Conn) (int, []byte) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	mt, b, err := c.ReadMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return mt, b
}

func nextText(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	mt, b := next(t, c)
	if mt != websocket.TextMessage {
		t.Fatalf("got binary %q, want a control frame", b)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("control frame %q: %v", b, err)
	}
	return m
}

func nextBinary(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	mt, b := next(t, c)
	if mt != websocket.BinaryMessage {
		t.Fatalf("got control %s, want terminal bytes", b)
	}
	return string(b)
}

func wantHello(t *testing.T, c *websocket.Conn) {
	t.Helper()
	m := nextText(t, c)
	if m["op"] != "session" || m["id"] != "s1" || m["sandbox"] != "box" || m["echoAck"] != true {
		t.Fatalf("first frame = %v, want the session frame (op and echoAck the hub's)", m)
	}
}

// wantClosed reads until the socket ends and fails on an exit frame on the
// way: a socket the terminal outlived must not say it ended.
func wantClosedWithoutExit(t *testing.T, c *websocket.Conn) {
	t.Helper()
	for {
		_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
		mt, b, err := c.ReadMessage()
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				t.Fatal("the socket was not closed")
			}
			return
		}
		if mt == websocket.TextMessage && bytes.Contains(b, []byte(`"exit"`)) {
			t.Fatalf("a socket whose terminal lives on got %s", b)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !cond(); time.Sleep(time.Millisecond) {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestHubHelloFirstThenReplayThenLive(t *testing.T) {
	r := newRig(t, 1<<10, 0)
	r.h.Output([]byte("one "))
	r.h.Output([]byte("two "))
	c := r.dial(t)
	wantHello(t, c)
	if got := nextBinary(t, c); got != "one two " {
		t.Fatalf("replay = %q", got)
	}
	r.h.Output([]byte("three"))
	if got := nextBinary(t, c); got != "three" {
		t.Fatalf("live = %q", got)
	}
	if r.h.Clients() != 1 {
		t.Fatalf("clients = %d", r.h.Clients())
	}
	// the tail is bounded, and Tail reads its end
	r.h.Output(bytes.Repeat([]byte("x"), 2000))
	if tl := r.h.Tail(0); len(tl) != 1<<10 {
		t.Fatalf("tail holds %d bytes, want %d", len(tl), 1<<10)
	}
	if got := string(r.h.Tail(3)); got != "xxx" {
		t.Fatalf("Tail(3) = %q", got)
	}
}

// Output racing an attach: the replay and the live stream meet with no gap
// and no repeat.
func TestHubReplayMeetsLiveExactly(t *testing.T) {
	r := newRig(t, 1<<20, 0)
	const n = 2000
	halfway := make(chan struct{})
	go func() {
		for i := 0; i < n; i++ {
			if i == n/4 {
				close(halfway)
			}
			// never let a client fall a whole queue behind: this test is
			// about ordering, not drops
			for r.h.maxQueued() > queueLen/2 {
				time.Sleep(time.Millisecond)
			}
			r.h.Output(fmt.Appendf(nil, "%05d\n", i))
		}
	}()
	<-halfway
	c := r.dial(t)
	wantHello(t, c)
	var got strings.Builder
	last := fmt.Sprintf("%05d\n", n-1)
	for !strings.HasSuffix(got.String(), last) {
		got.WriteString(nextBinary(t, c))
	}
	var want strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&want, "%05d\n", i)
	}
	if got.String() != want.String() {
		t.Fatalf("stream has a gap or a repeat at the replay boundary (%d bytes, want %d)", got.Len(), want.Len())
	}
}

func TestHubAckFollowsTheEcho(t *testing.T) {
	r := newRig(t, 1<<10, 0)
	r.echo.Store(true)
	c := r.dial(t)
	wantHello(t, c)
	for i, key := range []string{"a", "b"} {
		sent := time.Now()
		if err := c.WriteMessage(websocket.BinaryMessage, []byte(key)); err != nil {
			t.Fatal(err)
		}
		if got := nextBinary(t, c); got != key {
			t.Fatalf("echo = %q, want %q (the output precedes its ack)", got, key)
		}
		m := nextText(t, c)
		if m["op"] != "ack" || m["n"] != float64(i+1) {
			t.Fatalf("after the echo: %v, want ack %d", m, i+1)
		}
		if d := time.Since(sent); d < echoTimeout {
			t.Fatalf("acked after %v, before %v", d, echoTimeout)
		}
	}
	if got := string(<-r.in) + string(<-r.in); got != "ab" {
		t.Fatalf("terminal got %q", got)
	}
}

func TestHubPongEchoesTAndResizeIsBounded(t *testing.T) {
	r := newRig(t, 1<<10, 0)
	c := r.dial(t)
	wantHello(t, c)
	for _, junk := range []string{`not json`, `{"op":"future","x":1}`,
		`{"op":"resize","cols":0,"rows":30}`, `{"op":"resize","cols":70000,"rows":30}`,
		`{"op":"resize","cols":100,"rows":30}`, `{"op":"ping","t":{"a":[1,"b"],"n":1234.5}}`} {
		if err := c.WriteMessage(websocket.TextMessage, []byte(junk)); err != nil {
			t.Fatal(err)
		}
	}
	mt, b := next(t, c)
	if mt != websocket.TextMessage || string(b) != `{"op":"pong","t":{"a":[1,"b"],"n":1234.5}}` {
		t.Fatalf("pong = %s, want t echoed verbatim", b)
	}
	if got := <-r.resizes; got != [2]uint16{100, 30} {
		t.Fatalf("resize = %v", got)
	}
	select {
	case got := <-r.resizes:
		t.Fatalf("an out-of-range resize reached the terminal: %v", got)
	default:
	}
}

func TestHubExitOnlyWhenEnded(t *testing.T) {
	r := newRig(t, 1<<10, 0)
	var mu sync.Mutex
	var counts []int
	r.h.OnClients = func(n int) { mu.Lock(); counts = append(counts, n); mu.Unlock() }
	a, b := r.dial(t), r.dial(t)
	wantHello(t, a)
	wantHello(t, b)
	waitFor(t, "two clients", func() bool { return r.h.Clients() == 2 })

	// one client leaving ends nothing
	a.Close()
	waitFor(t, "the detach", func() bool { return r.h.Clients() == 1 })

	r.h.Output([]byte("bye"))
	r.h.End(ExitCode(3))
	r.h.End(ExitSignal("KILL")) // only the first End counts
	r.h.Output([]byte("late"))  // nor is output after it kept
	if got := nextBinary(t, b); got != "bye" {
		t.Fatalf("before the exit: %q", got)
	}
	if mt, m := next(t, b); mt != websocket.TextMessage || string(m) != `{"op":"exit","code":3}` {
		t.Fatalf("exit frame = %s", m)
	}
	if _, _, err := b.ReadMessage(); err == nil {
		t.Fatal("the socket stayed open after the exit")
	}

	// attaching to an ended terminal: the session frame, the tail, the exit
	c := r.dial(t)
	wantHello(t, c)
	if got := nextBinary(t, c); got != "bye" {
		t.Fatalf("replay after the end = %q", got)
	}
	if m := nextText(t, c); m["op"] != "exit" || m["code"] != float64(3) {
		t.Fatalf("exit after the replay = %v", m)
	}
	if r.h.Clients() != 0 {
		t.Fatalf("an ended hub holds %d clients", r.h.Clients())
	}
	waitFor(t, "OnClients to settle at 0", func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(counts) > 0 && counts[len(counts)-1] == 0
	})
}

// Attach returns at once on an ended terminal too: the replay to a client
// that doesn't read must not hold the caller (an HTTP handler, a tile TTY
// holding its exec) for the write timeouts.
func TestHubAttachToEndedReturnsAtOnce(t *testing.T) {
	h := NewHub(32 << 20) // more than a loopback socket buffers
	h.Output(bytes.Repeat([]byte("z"), 32<<20))
	h.End(ExitCode(0))
	took := make(chan time.Duration, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		conn, err := Upgrade(w, req, nil, 0)
		if err != nil {
			return
		}
		start := time.Now()
		h.Attach(conn, map[string]any{"id": "s1"}, Terminal{})
		took <- time.Since(start)
	}))
	defer srv.Close()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	select { // nobody reads c
	case d := <-took:
		if d > time.Second {
			t.Fatalf("Attach took %v on an ended terminal", d)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Attach blocked on the replay to a client that doesn't read")
	}
	// and the replay still arrives whole, then the exit
	if m := nextText(t, c); m["op"] != "session" {
		t.Fatalf("first frame = %v", m)
	}
	if got := nextBinary(t, c); len(got) != 32<<20 {
		t.Fatalf("replay = %d bytes", len(got))
	}
	if m := nextText(t, c); m["op"] != "exit" || m["code"] != float64(0) {
		t.Fatalf("after the replay: %v", m)
	}
}

func TestExitFrames(t *testing.T) {
	for _, tc := range []struct {
		e    Exit
		want string
	}{
		{Exit{}, `{"op":"exit"}`},
		{ExitCode(0), `{"op":"exit","code":0}`},
		{ExitCode(137), `{"op":"exit","code":137}`},
		{ExitSignal("TERM"), `{"op":"exit","code":null,"signal":"TERM"}`},
	} {
		if f := tc.e.frame(); !f.text || string(f.b) != tc.want {
			t.Fatalf("%+v → %s, want %s", tc.e, f.b, tc.want)
		}
	}
}

// A client that can't keep up is dropped WITHOUT an exit frame — it was
// read as "shell ended" and the terminal closed, though the shell lived on —
// and reattaching replays.
func TestHubSlowClientIsDroppedWithoutExit(t *testing.T) {
	r := newRig(t, 1<<10, 0)
	counts := make(chan int, 16)
	r.h.OnClients = func(n int) { counts <- n }
	// nobody reads: the socket buffers fill, then the queue
	c := r.dial(t)
	if n := <-counts; n != 1 {
		t.Fatalf("OnClients after the attach = %d", n)
	}
	chunk := bytes.Repeat([]byte("y"), 64<<10)
	for i := 0; r.h.Clients() > 0; i++ {
		if i > 4096 {
			t.Fatal("256 MiB queued to a reader that never reads, and it was not dropped")
		}
		r.h.Output(chunk)
	}
	if n := <-counts; n != 0 {
		t.Fatalf("OnClients after the drop = %d", n)
	}
	wantClosedWithoutExit(t, c)

	// the terminal lives on: a reattach gets the session frame and the tail
	r.h.Output([]byte("still here"))
	c2 := r.dial(t)
	wantHello(t, c2)
	if got := nextBinary(t, c2); !strings.HasSuffix(got, "still here") || len(got) != 1<<10 {
		t.Fatalf("replay after the drop: %d bytes ending %q", len(got), got[max(0, len(got)-12):])
	}
}

func TestReadLimit(t *testing.T) {
	r := newRig(t, 1<<10, ReadLimit)
	c := r.dial(t)
	wantHello(t, c)
	if err := c.WriteMessage(websocket.BinaryMessage, make([]byte, ReadLimit)); err != nil {
		t.Fatal(err)
	}
	if got := <-r.in; len(got) != ReadLimit {
		t.Fatalf("a message at the limit arrived as %d bytes", len(got))
	}
	if err := c.WriteMessage(websocket.BinaryMessage, make([]byte, ReadLimit+1)); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var err error
	for err == nil { // an echo ack for the first message may come first
		_, _, err = c.ReadMessage()
	}
	if !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("past the limit: %v, want close 1009", err)
	}
	select {
	case got := <-r.in:
		t.Fatalf("a message past the limit reached the terminal (%d bytes)", len(got))
	default:
	}
	waitFor(t, "the detach", func() bool { return r.h.Clients() == 0 })

	// no limit (the browser's /ws/term): the same message goes through
	r0 := newRig(t, 1<<10, 0)
	c0 := r0.dial(t)
	wantHello(t, c0)
	if err := c0.WriteMessage(websocket.BinaryMessage, make([]byte, ReadLimit+1)); err != nil {
		t.Fatal(err)
	}
	if got := <-r0.in; len(got) != ReadLimit+1 {
		t.Fatalf("unlimited: %d bytes", len(got))
	}
}
