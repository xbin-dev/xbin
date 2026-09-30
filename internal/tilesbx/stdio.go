package tilesbx

// stdio.go — the stdio WebSocket (the sandbox-manager contract's optional
// `stdio`, docs/sandbox-manager.md §stdio): a non-tty exec's streams on one
// socket, for a consumer that drives a program over its stdin and stdout
// (a coding agent speaking ACP) without polling. Reached only by the
// manager's instance token, which relays it to its consumer unchanged.
//
// Server → client: {"op":"hello",…} first; binary frames are stdout from
// ?since= (the ring's bytes, then live), {"op":"gap"} where the ring
// dropped some; a split exec's stderr from ?errSince= as {"op":"stderr",
// "off","data"} (base64); {"op":"exit"} once the exec has ended and all its
// output is out, then a normal close. Client → server: binary frames are
// stdin (the command's own reading paces them: no 30 s / 503 rule here),
// {"op":"eof"} closes it, {"op":"ping","t"} is answered with a pong.
// Unknown ops are ignored. The socket attached last holds stdin: a new
// attach closes the one before it with 4001 (replaced).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/xbin-dev/xbin/internal/termwire"
)

const (
	stdioChunk    = 64 << 10         // one output frame, at most
	stdioWriteMax = 30 * time.Second // a client that reads nothing this long is dropped
	stdioInTry    = time.Second      // one try at the command's stdin; the socket waits between tries
	stdioLinger   = 2 * time.Second  // after our close frame, the client's answer
	stdioReplaced = 4001             // the close code of a socket a newer attach replaced
)

// ServeExecStdio answers GET /sandboxes/{name}/execs/{id}/stdio?since=&errSince=:
// the stdio WebSocket of a non-tty exec. Refusals come before the upgrade,
// as JSON: not a WebSocket, a tty exec (its route is …/tty), an offset past
// its stream's end.
func (m *Manager) ServeExecStdio(w http.ResponseWriter, r *http.Request) {
	k, d, ok := m.managed(w, r)
	if !ok {
		return
	}
	if !websocket.IsWebSocketUpgrade(r) {
		writeErr(w, refuse(RefInvalid, "the stdio route is a WebSocket upgrade"))
		return
	}
	q := r.URL.Query()
	since, err1 := intQuery(q.Get("since"), 0)
	errSince, err2 := intQuery(q.Get("errSince"), 0)
	if errors.Join(err1, err2) != nil {
		writeErr(w, refuse(RefInvalid, "since and errSince are non-negative numbers"))
		return
	}
	_, e, ok := m.execFor(w, k, d, r.PathValue("id"))
	if !ok {
		return
	}
	x := e.info()
	switch {
	case e.tty:
		writeErr(w, refuse(RefInvalid, "exec %s has a terminal: attach to it with …/tty", e.id))
		return
	case since > x.Total:
		writeErr(w, refuse(RefInvalid, "since %d is past the output's end (%d)", since, x.Total))
		return
	case errSince > x.ErrTotal:
		writeErr(w, refuse(RefInvalid, "errSince %d is past stderr's end (%d)", errSince, x.ErrTotal))
		return
	}
	conn, err := termwire.Upgrade(w, r, nil, stdinMax)
	if err != nil {
		return // answered
	}
	m.serveStdio(conn, e, since, errSince)
}

// stdioSock is one attached stdio socket.
type stdioSock struct {
	e        *execRec
	conn     *websocket.Conn
	ctx      context.Context // ends when the client left, a write failed, or it was replaced
	cancel   context.CancelFunc
	wmu      sync.Mutex // one writer of data frames
	replaced atomic.Bool
	readDone chan struct{} // the read loop ended (the client's close frame, or its loss)
}

// takeStdio makes s the socket that holds e's stdin and returns the one
// that held it (nil: none).
func (e *execRec) takeStdio(s *stdioSock) *stdioSock {
	e.stdioMu.Lock()
	defer e.stdioMu.Unlock()
	prev := e.stdioAt
	e.stdioAt = s
	return prev
}

// dropStdio lets go of e's stdin when s still holds it.
func (e *execRec) dropStdio(s *stdioSock) {
	e.stdioMu.Lock()
	defer e.stdioMu.Unlock()
	if e.stdioAt == s {
		e.stdioAt = nil
	}
}

// holds reports that s is the socket attached last.
func (s *stdioSock) holds() bool {
	s.e.stdioMu.Lock()
	defer s.e.stdioMu.Unlock()
	return s.e.stdioAt == s
}

// serveStdio runs one attached socket to its end.
func (m *Manager) serveStdio(conn *websocket.Conn, e *execRec, since, errSince int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &stdioSock{e: e, conn: conn, ctx: ctx, cancel: cancel, readDone: make(chan struct{})}
	if prev := e.takeStdio(s); prev != nil {
		prev.replaced.Store(true)
		prev.cancel() // its own goroutine closes it (4001)
	}
	defer e.dropStdio(s)
	x := e.info()
	if s.send(map[string]any{"op": "hello", "id": e.id, "total": x.Total, "errTotal": x.ErrTotal,
		"state": x.State, "stdin": e.stdin, "split": e.split}) != nil {
		conn.Close()
		return
	}
	go s.read(m)
	var wg sync.WaitGroup
	if e.errRing != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.stream(e.errRing, errSince, "stderr")
		}()
	}
	s.stream(e.ring, since, "stdout")
	wg.Wait()
	if ctx.Err() == nil { // all its output is out: the exit, once it is recorded
		select {
		case <-e.done:
		case <-ctx.Done():
		}
	}
	switch {
	case s.replaced.Load():
		s.close(stdioReplaced, "replaced: another client attached")
	case ctx.Err() != nil: // the client left, or stopped reading
		conn.Close()
	default:
		x = e.info()
		if s.send(map[string]any{"op": "exit", "code": x.ExitCode, "signal": x.Signal, "total": x.Total, "errTotal": x.ErrTotal}) != nil {
			conn.Close()
			return
		}
		s.close(websocket.CloseNormalClosure, "")
	}
}

// stream sends rg from off on: stdout as binary frames, stderr as its op,
// each after the gap the ring dropped (if any) — until the stream ended and
// all of it is out, or the socket ends.
func (s *stdioSock) stream(rg *ring, off int64, name string) {
	for s.ctx.Err() == nil {
		c := rg.Read(off, stdioChunk)
		if c.start > off {
			if s.send(map[string]any{"op": "gap", "stream": name, "from": off, "to": c.start}) != nil {
				return
			}
			off = c.start
		}
		if len(c.data) > 0 {
			var err error
			if name == "stdout" {
				err = s.write(websocket.BinaryMessage, c.data)
			} else {
				err = s.send(map[string]any{"op": "stderr", "off": c.start, "data": c.data}) // []byte: base64
			}
			if err != nil {
				return
			}
			off = c.end
			continue
		}
		if c.ended {
			return
		}
		rg.Wait(s.ctx, off, time.Minute)
	}
}

// read takes the client's frames: stdin, eof, ping. It ends the socket's
// context when the client closes or is lost.
func (s *stdioSock) read(m *Manager) {
	defer close(s.readDone)
	defer s.cancel()
	for {
		typ, b, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		if typ == websocket.BinaryMessage {
			s.stdin(m, b)
			continue
		}
		var ctl struct {
			Op string          `json:"op"`
			T  json.RawMessage `json:"t"`
		}
		if json.Unmarshal(b, &ctl) != nil {
			continue
		}
		switch ctl.Op { // anything else is ignored
		case "ping":
			_ = s.send(map[string]any{"op": "pong", "t": ctl.T})
		case "eof":
			s.eof()
		}
	}
}

// stdin writes p to the command's stdin, waiting for it to read as long as
// the socket lasts (the client's frames wait meanwhile). What can't go is
// answered with an error op; a replaced socket's frames are dropped.
func (s *stdioSock) stdin(m *Manager, p []byte) {
	e := s.e
	switch {
	case !e.stdin:
		s.fail(refuse(RefInvalid, "exec %s was started without stdin: true", e.id))
		return
	case !s.holds():
		return
	}
	m.touch(e.r) // input is activity
	for len(p) > 0 && s.ctx.Err() == nil {
		n, err := e.writeInN(p, stdioInTry)
		p = p[n:]
		var re *Error
		switch {
		case err == nil:
		case errors.As(err, &re) && re.Refusal == RefUnavailable: // not reading yet
		default:
			s.fail(err)
			return
		}
	}
}

// eof closes the command's stdin (the socket attached last only).
func (s *stdioSock) eof() {
	e := s.e
	switch {
	case !e.stdin:
		s.fail(refuse(RefInvalid, "exec %s was started without stdin: true", e.id))
	case !s.holds():
	default:
		if err := e.closeIn(); err != nil && e.running() {
			s.fail(refuse(RefState, "closing exec %s's stdin: %v", e.id, err))
		}
	}
}

// fail tells the client what its frame couldn't do: {"op":"error",
// "refusal","error"}. The socket goes on.
func (s *stdioSock) fail(err error) {
	var re *Error
	if !errors.As(err, &re) {
		re = &Error{Refusal: RefState, Msg: err.Error()}
	}
	_ = s.send(map[string]any{"op": "error", "refusal": re.Refusal, "error": re.Msg})
}

// send writes a JSON control frame.
func (s *stdioSock) send(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.write(websocket.TextMessage, b)
}

// write writes one frame; a client that doesn't take it within
// stdioWriteMax ends the socket.
func (s *stdioSock) write(typ int, b []byte) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_ = s.conn.SetWriteDeadline(time.Now().Add(stdioWriteMax))
	err := s.conn.WriteMessage(typ, b)
	if err != nil {
		s.cancel()
	}
	return err
}

// close sends a close frame with code, waits a little for the client's,
// and drops the connection.
func (s *stdioSock) close(code int, why string) {
	_ = s.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, why), time.Now().Add(time.Second))
	t := time.NewTimer(stdioLinger)
	select {
	case <-s.readDone:
	case <-t.C:
	}
	t.Stop()
	s.conn.Close()
}
