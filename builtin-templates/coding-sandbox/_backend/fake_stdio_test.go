package main

// fake_stdio_test.go — the fake backend's stdio sockets (TEST ONLY; see
// fake_backend_test.go): a non-tty exec's streams on one WebSocket, served
// with the SDK's sdk/ws on the wire the runtime's stdio route speaks
// (docs/sandbox-manager.md §stdio), so the manager's relay is the same call
// either way.

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// fkStdio is one attached stdio socket.
type fkStdio struct {
	cancel   context.CancelFunc
	replaced atomic.Bool
}

func (s *fkSandbox) RelayStdio(w http.ResponseWriter, r *http.Request, execID string, since, errSince int64) {
	if !s.f.hasCap("stdio") {
		xbin.WriteSandboxError(w, fkErr(http.StatusNotImplemented, "unsupported", "no stdio sockets here"))
		return
	}
	if !ws.IsUpgrade(r) {
		xbin.WriteSandboxError(w, fkErr(http.StatusBadRequest, "invalid", "the stdio route is a WebSocket upgrade"))
		return
	}
	_, e, err := s.exec("stdio", execID)
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	tty := e.info.TTY
	s.f.mu.Unlock()
	total, errTotal := e.ring.totalNow(), int64(0)
	if e.errRing != nil {
		errTotal = e.errRing.totalNow()
	}
	switch {
	case tty:
		err = fkErr(http.StatusBadRequest, "invalid", "exec %s has a terminal: attach to it with …/tty", execID)
	case since < 0 || errSince < 0:
		err = fkErr(http.StatusBadRequest, "invalid", "since and errSince are non-negative numbers")
	case since > total:
		err = fkErr(http.StatusBadRequest, "invalid", "since %d is past the output's end (%d)", since, total)
	case errSince > errTotal:
		err = fkErr(http.StatusBadRequest, "invalid", "errSince %d is past stderr's end (%d)", errSince, errTotal)
	}
	if err != nil {
		xbin.WriteSandboxError(w, err)
		return
	}
	c, err := ws.Upgrade(w, r, &ws.UpgradeOptions{MaxMessageSize: 1 << 20,
		Error: func(w http.ResponseWriter, _ *http.Request, status int, reason string) {
			xbin.WriteSandboxError(w, fkErr(status, "invalid", "%s", reason))
		}})
	if err != nil {
		return
	}
	s.serveStdio(c, e, since, errSince)
}

// serveStdio runs one attached socket: hello, the streams from their
// offsets (a gap frame where the ring dropped bytes), stdin, eof and ping
// from the client, the exit once the exec ended and all its output is out,
// then a normal close. The socket attached last holds stdin; attaching
// closes the one before with 4001.
func (s *fkSandbox) serveStdio(c *ws.Conn, e *fkExec, since, errSince int64) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	me := &fkStdio{cancel: cancel}
	s.f.mu.Lock()
	prev := e.stdio
	e.stdio = me
	hello := map[string]any{"op": "hello", "id": e.info.ID, "total": e.ring.totalNow(), "errTotal": int64(0), "state": e.State,
		"stdin": e.wantStdin, "split": e.errRing != nil}
	if e.errRing != nil {
		hello["errTotal"] = e.errRing.totalNow()
	}
	s.f.mu.Unlock()
	if prev != nil {
		prev.replaced.Store(true)
		prev.cancel()
	}
	defer func() {
		s.f.mu.Lock()
		if e.stdio == me {
			e.stdio = nil
		}
		s.f.mu.Unlock()
	}()
	send := func(v any) error {
		b, _ := json.Marshal(v)
		err := c.WriteMessage(ws.TextMessage, b)
		if err != nil {
			cancel()
		}
		return err
	}
	if send(hello) != nil {
		c.Close()
		return
	}
	go func() { // the client's frames
		defer cancel()
		for {
			typ, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if typ == ws.BinaryMessage {
				s.stdioIn(e, me, data, send)
				continue
			}
			var ctl struct {
				Op string          `json:"op"`
				T  json.RawMessage `json:"t"`
			}
			if json.Unmarshal(data, &ctl) != nil {
				continue
			}
			switch ctl.Op { // anything else is ignored
			case "ping":
				_ = send(map[string]any{"op": "pong", "t": ctl.T})
			case "eof":
				s.stdioIn(e, me, nil, send)
			}
		}
	}()
	stream := func(ring *fkRing, off int64, name string) {
		for !ring.ended(off) {
			start, end, _, _, data := ring.read(ctx, off, 64<<10, 30*time.Second)
			if ctx.Err() != nil {
				return
			}
			if start > off && send(map[string]any{"op": "gap", "stream": name, "from": off, "to": start}) != nil {
				return
			}
			if len(data) > 0 {
				var err error
				if name == "stdout" {
					if err = c.WriteMessage(ws.BinaryMessage, data); err != nil {
						cancel()
					}
				} else {
					err = send(map[string]any{"op": "stderr", "off": start, "data": data}) // base64
				}
				if err != nil {
					return
				}
			}
			off = end
		}
	}
	var wg sync.WaitGroup
	if e.errRing != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stream(e.errRing, errSince, "stderr")
		}()
	}
	stream(e.ring, since, "stdout")
	wg.Wait()
	if ctx.Err() == nil {
		select {
		case <-e.done:
		case <-ctx.Done():
		}
	}
	switch {
	case me.replaced.Load():
		_ = c.CloseWith(xbin.StdioReplaced, "replaced: another client attached")
	case ctx.Err() != nil: // the client left
		c.Close()
	default:
		s.f.mu.Lock()
		exit := map[string]any{"op": "exit", "code": e.info.ExitCode, "signal": e.info.Signal, "total": e.ring.totalNow(), "errTotal": int64(0)}
		if e.errRing != nil {
			exit["errTotal"] = e.errRing.totalNow()
		}
		s.f.mu.Unlock()
		_ = send(exit)
		c.Close()
	}
}

// stdioIn writes a frame to the exec's stdin (nil: eof) — the socket
// attached last only. A pipe the command doesn't read holds the socket;
// what can't go is an {"op":"error"} frame.
func (s *fkSandbox) stdioIn(e *fkExec, me *fkStdio, data []byte, send func(any) error) {
	s.f.mu.Lock()
	in, st, eof, holds, want := e.stdin, e.State, e.eof, e.stdio == me, e.wantStdin
	if data == nil && want && holds && in != nil && !eof && st == "running" {
		e.eof = true
	}
	s.f.mu.Unlock()
	fail := func(refusal, msg string) { _ = send(map[string]any{"op": "error", "refusal": refusal, "error": msg}) }
	switch {
	case !want:
		fail("invalid", "this exec wasn't started with stdin")
	case !holds:
	case st != "running":
		fail("state", "the exec has ended")
	case eof:
		fail("invalid", "stdin is closed")
	case in == nil:
		fail("unavailable", "the exec hasn't started yet")
	case data == nil:
		_ = in.Close()
	default:
		if _, err := in.Write(data); err != nil {
			fail("state", "the exec has stopped reading")
		}
	}
}
