// Package termwire is the /ws/term wire (docs/protocol.md §/ws/term): one
// terminal's output fanned out to its attached WebSockets. Binary frames
// carry terminal bytes both ways; text frames are JSON control — the session
// frame first, then the replayed tail, then the live stream with the echo
// acks and pongs the browser's predictive echo needs (echo.go), and an exit
// frame when — and only when — the process has ended.
//
// It is shared by xbind's terminal sessions (internal/term, /ws/term) and
// the tile sandboxes' TTY execs (plans/tile-sandbox-runtime.md §3.7), so a
// manager relaying a tile TTY byte for byte speaks exactly what
// <bx-terminal> and the xbin app's terminal already speak.
package termwire

import (
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	// queueLen is how many frames one client may fall behind before it is
	// dropped (it reattaches and the tail replays).
	queueLen = 64
	// writeTimeout bounds one write to a client.
	writeTimeout = 10 * time.Second
)

// Terminal is what a socket's input drives: the PTY of a terminal session,
// or a tile TTY exec's stdin. Write gets each binary frame whole; an error
// ends that socket (not the terminal). Resize, when set, gets a client's
// resize — always 1..65535 both ways.
type Terminal struct {
	Write  func(b []byte) error
	Resize func(cols, rows uint16)
}

// Exit is how the process ended, as the exit frame reports it. The zero
// value reports nothing: {"op":"exit"}.
type Exit struct {
	Code   *int   // the exit status; nil when a signal ended it or it is not known
	Signal string // the signal that ended it ("TERM", "KILL", …); the code is then null
}

// ExitCode is an exit with status n: {"op":"exit","code":n}.
func ExitCode(n int) Exit { return Exit{Code: &n} }

// ExitSignal is an exit by a signal: {"op":"exit","code":null,"signal":sig}.
func ExitSignal(sig string) Exit { return Exit{Signal: sig} }

// frame renders the exit frame.
func (e Exit) frame() frame {
	switch {
	case e.Signal != "":
		sig, _ := json.Marshal(e.Signal)
		return frame{text: true, b: []byte(`{"op":"exit","code":null,"signal":` + string(sig) + `}`)}
	case e.Code != nil:
		return frame{text: true, b: []byte(`{"op":"exit","code":` + strconv.Itoa(*e.Code) + `}`)}
	}
	return frame{text: true, b: []byte(`{"op":"exit"}`)}
}

// Hub is one terminal's side of the wire: a bounded tail of its output
// (replayed first to every attach), the attached clients, each with one
// ordered queue for output and control frames, and the exit.
type Hub struct {
	// OnClients, when set, is told the attached count after it changes (an
	// attach, a detach, a drop, the end). Calls are serialized and read the
	// count as they run, so the last one always reports the current count;
	// changes that land during a call coalesce into one more call, and a
	// count may repeat. Never called under the hub's lock; it may call back
	// into the hub.
	OnClients func(n int)

	max int // tail bound in bytes

	cbMu    sync.Mutex // guards cbBusy, cbDirty: one OnClients deliverer at a time
	cbBusy  bool
	cbDirty bool

	mu         sync.Mutex
	tail       []byte
	clients    map[*client]struct{}
	lastActive time.Time
	dead       bool
	exit       frame
}

// client is one attached WebSocket.
type client struct {
	conn *websocket.Conn
	send chan frame
	echo *echoTracker
	// ended: End closed send, so the writer follows the queue with the exit
	// frame. A client dropped for falling behind, or gone on its own, gets
	// none — its terminal did not end. Written under Hub.mu before send is
	// closed; the writer reads it after the close, so the close orders them.
	ended bool
}

// NewHub returns a live hub whose tail keeps the last maxTail bytes.
func NewHub(maxTail int) *Hub {
	return &Hub{max: maxTail, clients: map[*client]struct{}{}, lastActive: time.Now()}
}

// Output appends terminal bytes to the tail and queues them to every client.
// b is copied. A client whose queue is full is dropped (without an exit
// frame): it reattaches and the tail replays. No-op once ended.
func (h *Hub) Output(b []byte) {
	if len(b) == 0 {
		return
	}
	out := append([]byte(nil), b...)
	h.mu.Lock()
	if h.dead {
		h.mu.Unlock()
		return
	}
	if h.max > 0 {
		h.tail = append(h.tail, out...)
		if len(h.tail) > h.max {
			h.tail = h.tail[len(h.tail)-h.max:]
		}
	}
	h.lastActive = time.Now()
	dropped := false
	for c := range h.clients {
		if !h.enqueueLocked(c, frame{b: out}) {
			dropped = true
		}
	}
	h.mu.Unlock()
	if dropped {
		h.notify()
	}
}

// End marks the terminal ended: every attached client gets the exit frame
// after the output already queued to it, then its socket closes; a later
// Attach replays the tail and then the exit. Only the first End counts.
func (h *Hub) End(e Exit) {
	h.mu.Lock()
	if h.dead {
		h.mu.Unlock()
		return
	}
	h.dead, h.exit = true, e.frame()
	n := len(h.clients)
	for c := range h.clients {
		c.ended = true
		close(c.send)
	}
	h.clients = map[*client]struct{}{}
	h.mu.Unlock()
	if n > 0 {
		h.notify()
	}
}

// Clients is the number of sockets attached right now.
func (h *Hub) Clients() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

// LastActive is when output last arrived, a client attached or detached, or
// Touch was called.
func (h *Hub) LastActive() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastActive
}

// Touch counts as activity (an agent session's events, which have no
// terminal output of their own).
func (h *Hub) Touch() {
	h.mu.Lock()
	h.lastActive = time.Now()
	h.mu.Unlock()
}

// Tail returns a copy of the last n bytes of output (n <= 0: all of the tail).
func (h *Hub) Tail(n int) []byte {
	h.mu.Lock()
	defer h.mu.Unlock()
	b := h.tail
	if n > 0 && len(b) > n {
		b = b[len(b)-n:]
	}
	return append([]byte(nil), b...)
}

// Attach serves one upgraded socket (Upgrade) until it or the terminal goes;
// it returns at once. The socket gets the session frame first — hello, with
// "op":"session" and "echoAck":true set here (this hub acks input and
// answers pings) — then the tail, then the live stream. Its binary frames go
// to t.Write, each acked once echoTimeout old; its text frames are resize
// and ping, anything else ignored. On an ended terminal the socket gets the
// session frame, the tail and the exit frame, then closes.
func (h *Hub) Attach(conn *websocket.Conn, hello map[string]any, t Terminal) {
	hl := make(map[string]any, len(hello)+2)
	for k, v := range hello {
		hl[k] = v
	}
	hl["op"], hl["echoAck"] = "session", true
	helloB, _ := json.Marshal(hl)

	c := &client{conn: conn, send: make(chan frame, queueLen)}
	c.echo = newEchoTracker(func(n uint64) {
		h.enqueue(c, textFrame(map[string]any{"op": "ack", "n": n}))
	})

	h.mu.Lock()
	tail := append([]byte(nil), h.tail...)
	if h.dead {
		// It ended before this client attached (a sandbox that fails its init
		// lives ~10ms). The tail holds WHY — the init's stderr goes to the
		// terminal — so replay it before the exit frame instead of discarding
		// it, or the client shows a silently-dead pane.
		exit := h.exit
		h.mu.Unlock()
		defer conn.Close()
		if write(conn, frame{text: true, b: helloB}) != nil {
			return
		}
		if len(tail) > 0 && write(conn, frame{b: tail}) != nil {
			return
		}
		_ = write(conn, exit)
		return
	}
	h.clients[c] = struct{}{}
	h.lastActive = time.Now()
	h.mu.Unlock()
	h.notify()

	// Writer: the session frame (browsers can't read upgrade headers), then
	// the tail (output after it is queued in c.send behind it, preserving
	// order), then the live stream — terminal bytes and control frames in
	// the order they were queued — and the exit frame if the terminal ended.
	go func() {
		defer conn.Close()
		if write(conn, frame{text: true, b: helloB}) != nil {
			return
		}
		if len(tail) > 0 && write(conn, frame{b: tail}) != nil {
			return
		}
		for f := range c.send {
			if write(conn, f) != nil {
				return
			}
		}
		if c.ended {
			_ = write(conn, h.exitFrame())
		}
	}()

	// Reader: input → the terminal; control frames.
	go func() {
		defer func() {
			c.echo.close()
			h.mu.Lock()
			_, attached := h.clients[c]
			if attached {
				delete(h.clients, c)
				close(c.send)
			}
			h.lastActive = time.Now()
			h.mu.Unlock()
			conn.Close()
			if attached {
				h.notify()
			}
		}()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if t.Write == nil || t.Write(data) != nil {
					return
				}
				c.echo.input()
			case websocket.TextMessage:
				var ctl control
				if json.Unmarshal(data, &ctl) != nil {
					continue
				}
				switch ctl.Op {
				case "resize":
					if t.Resize != nil && ctl.Cols > 0 && ctl.Rows > 0 && ctl.Cols <= 0xffff && ctl.Rows <= 0xffff {
						t.Resize(uint16(ctl.Cols), uint16(ctl.Rows))
					}
				case "ping":
					h.enqueue(c, textFrame(map[string]any{"op": "pong", "t": ctl.T}))
				}
			}
		}
	}()
}

// exitFrame is the ended terminal's exit frame.
func (h *Hub) exitFrame() frame {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.exit
}

// enqueueLocked queues f for c; the caller holds h.mu and c is attached. A
// client that cannot keep up is dropped — false — so under h.mu, "c ∈
// h.clients" is exactly "c.send is open". Its socket is closed at once
// (off the lock: a TLS close may block), without an exit frame, and its
// writer drains nothing more to it: the client reattaches and the tail
// replays. The caller notifies after unlocking.
func (h *Hub) enqueueLocked(c *client, f frame) bool {
	select {
	case c.send <- f:
		return true
	default:
		delete(h.clients, c)
		close(c.send)
		go c.conn.Close()
		return false
	}
}

// enqueue is enqueueLocked for callers outside h.mu (the ack timer, the
// reader's pong): a client that has already gone is a no-op, never a send on
// a closed channel.
func (h *Hub) enqueue(c *client, f frame) {
	h.mu.Lock()
	dropped := false
	if _, ok := h.clients[c]; ok {
		dropped = !h.enqueueLocked(c, f)
	}
	h.mu.Unlock()
	if dropped {
		h.notify()
	}
}

// notify tells OnClients the current count (see its doc). A notify while a
// call is running marks it dirty and returns; the running deliverer calls
// again with the count as it is then.
func (h *Hub) notify() {
	if h.OnClients == nil {
		return
	}
	h.cbMu.Lock()
	h.cbDirty = true
	if h.cbBusy {
		h.cbMu.Unlock()
		return
	}
	h.cbBusy = true
	for h.cbDirty {
		h.cbDirty = false
		h.cbMu.Unlock()
		h.OnClients(h.Clients())
		h.cbMu.Lock()
	}
	h.cbBusy = false
	h.cbMu.Unlock()
}

// write sends one frame under the write timeout.
func write(conn *websocket.Conn, f frame) error {
	mt := websocket.BinaryMessage
	if f.text {
		mt = websocket.TextMessage
	}
	_ = conn.SetWriteDeadline(time.Now().Add(writeTimeout))
	return conn.WriteMessage(mt, f.b)
}
