package termwire

// What the browser's predictive echo needs from the server (D70,
// docs/protocol.md §/ws/term): an ECHO ACK, "input frame N reached the
// terminal echoTimeout ago, so whatever the application printed in answer is
// already ahead of this frame on the wire", and an app-level ping/pong for
// the round-trip time (a WebSocket-level ping is answered by the browser's
// network stack, which JS cannot time). Both travel in the client's one
// ordered output queue: the writer goroutine is the socket's only writer,
// and an ack must follow the output it vouches for.

import (
	"encoding/json"
	"sync"
	"time"
)

// echoTimeout is mosh's ECHO_TIMEOUT: input this old has been answered by
// the application, if it is going to be.
const echoTimeout = 50 * time.Millisecond

// frame is one queued WebSocket message: a JSON control frame, or raw
// terminal bytes.
type frame struct {
	text bool
	b    []byte
}

func textFrame(v any) frame { b, _ := json.Marshal(v); return frame{text: true, b: b} }

// control is a client → server text frame. Unknown ops are ignored.
type control struct {
	Op   string          `json:"op"` // resize|ping
	Cols int             `json:"cols,omitempty"`
	Rows int             `json:"rows,omitempty"`
	T    json.RawMessage `json:"t,omitempty"` // ping: echoed verbatim in the pong
}

// echoTracker numbers a client's input frames and acks each once it is
// echoTimeout old — mosh's set_echo_ack: one ack for the newest frame that
// old, the older ones implied. The clock and the timer are injectable so
// echo_test.go drives it without waiting.
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

// input records one input frame as it reaches the terminal and returns its
// number.
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
