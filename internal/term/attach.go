package term

// The per-client half of a session — the WebSocket attach with its writer
// and reader goroutines — and what the browser's predictive echo needs from
// the server (D70, docs/protocol.md §/ws/term): an ECHO ACK, "input frame N
// reached the PTY echoTimeout ago, so whatever the application printed in
// answer is already ahead of this frame on the wire", and an app-level
// ping/pong for the round-trip time (a WebSocket-level ping is answered by
// the browser's network stack, which JS cannot time). Both travel in the
// client's one ordered output queue: the writer goroutine is the socket's
// only writer, and an ack must follow the output it vouches for.

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

// echoTimeout is mosh's ECHO_TIMEOUT: input this old has been answered by
// the application, if it is going to be.
const echoTimeout = 50 * time.Millisecond

// frame is one queued WebSocket message: a JSON control frame, or raw PTY bytes.
type frame struct {
	text bool
	b    []byte
}

func textFrame(v any) frame { b, _ := json.Marshal(v); return frame{text: true, b: b} }

// client is one attached WebSocket.
type client struct {
	conn *websocket.Conn
	send chan frame
	echo *echoTracker
}

// control is a client → server text frame.
type control struct {
	Op   string          `json:"op"` // resize|ping
	Cols int             `json:"cols,omitempty"`
	Rows int             `json:"rows,omitempty"`
	T    json.RawMessage `json:"t,omitempty"` // ping: echoed verbatim in the pong
}

// enqueueLocked queues f for c; the caller holds s.mu. A client that cannot
// keep up is dropped (it can reattach) — so under s.mu, "c ∈ s.clients" is
// exactly "c.send is open".
func (s *Session) enqueueLocked(c *client, f frame) {
	select {
	case c.send <- f:
	default:
		delete(s.clients, c)
		close(c.send)
	}
}

// enqueue is enqueueLocked for callers outside s.mu (the ack timer): a
// client that has already gone is a no-op, never a send on a closed channel.
func (s *Session) enqueue(c *client, f frame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.clients[c]; ok {
		s.enqueueLocked(c, f)
	}
}

// echoTracker numbers a client's input frames and acks each once it is
// echoTimeout old — mosh's set_echo_ack: one ack for the newest frame that
// old, the older ones implied. The clock and the timer are injectable so
// attach_test.go drives it without waiting.
type echoTracker struct {
	mu     sync.Mutex
	now    func() time.Time
	after  func(time.Duration, func()) stopper
	emit   func(n uint64)
	n      uint64
	hist   []inputAt // frames not yet acked, oldest first
	timer  stopper
	closed bool
}

type stopper interface{ Stop() bool }

type inputAt struct {
	n  uint64
	at time.Time
}

func newEchoTracker(emit func(n uint64)) *echoTracker {
	return &echoTracker{
		now:   time.Now,
		after: func(d time.Duration, f func()) stopper { return time.AfterFunc(d, f) },
		emit:  emit,
	}
}

// input records one input frame as it reaches the PTY and returns its number.
func (e *echoTracker) input() uint64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.n++
	e.hist = append(e.hist, inputAt{n: e.n, at: e.now()})
	e.armLocked()
	return e.n
}

// armLocked schedules fire for when the oldest unacked frame turns echoTimeout old.
func (e *echoTracker) armLocked() {
	if e.closed || e.timer != nil || len(e.hist) == 0 {
		return
	}
	d := echoTimeout - e.now().Sub(e.hist[0].at)
	if d < 0 {
		d = 0
	}
	e.timer = e.after(d, e.fire)
}

// fire acks the newest frame at least echoTimeout old, forgets it and the
// older ones, and re-arms for the rest.
func (e *echoTracker) fire() {
	e.mu.Lock()
	e.timer = nil
	if e.closed {
		e.mu.Unlock()
		return
	}
	cut := e.now().Add(-echoTimeout)
	var ack uint64
	i := 0
	for ; i < len(e.hist) && !e.hist[i].at.After(cut); i++ {
		ack = e.hist[i].n
	}
	e.hist = e.hist[i:]
	e.armLocked()
	e.mu.Unlock()
	if ack > 0 {
		e.emit(ack)
	}
}

// close stops the timer; nothing is emitted afterwards.
func (e *echoTracker) close() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
}

func (s *Session) attach(conn *websocket.Conn) {
	c := &client{conn: conn, send: make(chan frame, 64)}
	c.echo = newEchoTracker(func(n uint64) {
		s.enqueue(c, textFrame(map[string]any{"op": "ack", "n": n}))
	})

	s.mu.Lock()
	if s.dead {
		// The session died before this client attached (a sandbox that fails
		// its init lives ~10ms). Its scrollback holds WHY — the init's stderr
		// goes to the PTY — so replay it before the exit frame instead of
		// discarding it, or the browser shows a silently-dead pane.
		sb := make([]byte, len(s.scrollback))
		copy(sb, s.scrollback)
		s.mu.Unlock()
		if len(sb) > 0 {
			_ = conn.WriteMessage(websocket.BinaryMessage, sb)
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"exit"}`))
		conn.Close()
		return
	}
	sb := make([]byte, len(s.scrollback))
	copy(sb, s.scrollback)
	s.clients[c] = struct{}{}
	s.lastActive = time.Now()
	s.mu.Unlock()

	// Writer: session id first (browsers can't read upgrade headers), then
	// scrollback replay (new output is queued in c.send behind it, preserving
	// order), then the live stream — PTY bytes and control frames in the
	// order they were queued.
	go func() {
		hello, _ := json.Marshal(map[string]any{
			"op": "session", "id": s.ID, "net": s.Net, "baseOutdated": s.baseOld,
			"label": s.Label, "scopes": s.Scopes, "netNote": s.NetNote,
			"vm":      s.vm,
			"echoAck": true, // this xbind acks input and answers pings (D70)
		})
		_ = conn.WriteMessage(websocket.TextMessage, hello)
		if len(sb) > 0 {
			if err := conn.WriteMessage(websocket.BinaryMessage, sb); err != nil {
				return
			}
		}
		for f := range c.send {
			mt := websocket.BinaryMessage
			if f.text {
				mt = websocket.TextMessage
			}
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(mt, f.b); err != nil {
				return
			}
		}
		_ = conn.WriteMessage(websocket.TextMessage, []byte(`{"op":"exit"}`))
		conn.Close()
	}()

	// Reader: browser input → PTY; control frames.
	go func() {
		defer func() {
			c.echo.close()
			s.mu.Lock()
			if _, ok := s.clients[c]; ok {
				delete(s.clients, c)
				close(c.send)
			}
			s.lastActive = time.Now()
			s.mu.Unlock()
			conn.Close()
		}()
		for {
			mt, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			switch mt {
			case websocket.BinaryMessage:
				if _, err := s.pty.Write(data); err != nil {
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
					if ctl.Cols > 0 && ctl.Rows > 0 {
						_ = pty.Setsize(s.pty, &pty.Winsize{
							Cols: uint16(ctl.Cols), Rows: uint16(ctl.Rows),
						})
					}
				case "ping":
					s.enqueue(c, textFrame(map[string]any{"op": "pong", "t": ctl.T}))
				}
			}
		}
	}()
}
