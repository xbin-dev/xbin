// harness_pipe_stdio.go — the pipe's stdio socket (D-harness §5.3), where
// the manager and the sandbox offer it: stdout (binary), stderr offsets,
// gaps and the exit read from one WebSocket, attached again from the
// offsets read so far after a drop; a socket attached again gets the stdin
// no pong acknowledged (harness_pipe.go's flush).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/ws"
)

// stepSocket is one message of the stdio socket, attaching it first when
// there is none (again after a drop, from where reading got).
func (p *harnessPipe) stepSocket() {
	p.mu.Lock()
	c := p.sock
	p.mu.Unlock()
	if c == nil {
		p.connect()
		return
	}
	typ, data, err := c.ReadMessage()
	if err != nil {
		replaced := ws.IsClose(err, xbin.StdioReplaced)
		if replaced && p.t.Guard != nil && p.t.Guard() == nil {
			// this process still owns the session, so whoever attached
			// isn't its successor (that one bumps the epoch first): taken
			// back like a drop — connect checks the Guard again
			logf("harness exec %s: another client attached to its stdio socket — attaching again", p.execID)
			replaced = false
		}
		switch {
		case p.ctx.Err() != nil:
			p.end(errPipeDetached)
		case replaced: // the successor's (or, with no Guard, anyone's): let go
			p.end(errPipeReplaced)
		default: // a drop: attach again (after a while, when it keeps dropping)
			p.dropSock(c)
			if p.drops > 0 {
				sleepCtx(p.ctx, backoff(p.drops, nil))
			}
			p.drops++
		}
		return
	}
	if typ == ws.BinaryMessage {
		p.buf = data
		p.since += int64(len(data))
		p.drops = 0
		return
	}
	var f xbin.StdioFrame
	if json.Unmarshal(data, &f) != nil {
		return
	}
	if f.Op != "hello" && f.Op != "pong" {
		p.drops = 0
	}
	switch f.Op {
	case "gap":
		if f.Stream == "stderr" {
			p.setErrOff(f.To)
		} else if f.To > p.since {
			p.gap += f.To - p.since
			p.since = f.To
		}
	case "stderr": // the log route reads it from the manager; here only where a successor resumes
		p.setErrOff(f.Off + int64(len(f.Data)))
	case "exit":
		state := "exited"
		if f.Code == nil {
			state = "killed"
		}
		p.setState(state, f.Code, f.Signal)
		p.end(nil)
		p.dropSock(c)
	case "pong": // stdin through that unit reached the command
		var n int64
		if json.Unmarshal(f.T, &n) == nil {
			p.mu.Lock()
			p.acked = max(p.acked, n)
			p.mu.Unlock()
		}
	case "error": // a stdin frame it couldn't take (the command ended, stdin closed)
		logf("harness exec %s: stdin: %s %s", p.execID, f.Refusal, f.Error)
	}
}

func (p *harnessPipe) setErrOff(off int64) {
	p.mu.Lock()
	p.errOff = max(p.errOff, off)
	p.mu.Unlock()
}

// open attaches the stdio socket before anyone reads, so a writer needn't
// wait for the reader (which attaches it again after a drop); an exec that
// is gone ends the stream. Nothing for the exec routes.
func (p *harnessPipe) open(ctx context.Context) {
	if !p.Stdio() {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, sbxCallTimeout)
	defer cancel()
	if err := p.dial(ctx); gone(err) {
		p.end(errPipeLost)
	} // any other failure: the reader tries again
}

// connect is the reader attaching the socket (again) — only while this
// process owns the session (Guard): the newest attacher wins, so attaching
// after a handoff would take the command from its new owner. A pipe that no
// longer owns it lets the command go, as Detach does, ending with Guard's
// error.
func (p *harnessPipe) connect() {
	if g := p.t.Guard; g != nil {
		if err := g(); err != nil {
			p.end(err)
			p.cancel()
			return
		}
	}
	ctx, cancel := context.WithTimeout(p.ctx, sbxCallTimeout)
	defer cancel()
	if err := p.dial(ctx); err != nil {
		p.failed(err)
		return
	}
	p.failing, p.tries = time.Time{}, 0
	go p.reflush()
}

// dial attaches the stdio socket from the offsets read so far (the
// reader's, or before it reads). A manager that doesn't have the route
// after all switches the pipe to the exec routes.
func (p *harnessPipe) dial(ctx context.Context) error {
	c, err := xbin.DialManagerStdio(ctx, p.t.Conn.M.URL, p.t.ID, p.execID, xbin.ManagerStdioOptions{
		Since: p.since, ErrSince: p.ErrOff(), User: p.t.Conn.User, Client: sbxClient()})
	if err != nil {
		err = p.sbxErr(err)
		if sbxRefusal(err) == "unsupported" {
			p.mu.Lock()
			p.stdio = false
			p.bump()
			p.mu.Unlock()
			return nil
		}
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.over || p.ctx.Err() != nil {
		go c.Close()
		return nil
	}
	p.sock = c
	p.bump()
	return nil
}

// dropSock forgets a socket that failed (the reader attaches again).
func (p *harnessPipe) dropSock(c *ws.Conn) {
	p.mu.Lock()
	if p.sock == c {
		p.sock = nil
		p.bump()
	}
	p.mu.Unlock()
	go c.Close()
}

// sbxErr is a dial's refusal as the manager's calls report theirs.
func (p *harnessPipe) sbxErr(err error) error {
	var se *xbin.SandboxError
	if errors.As(err, &se) {
		return &sbxError{Provider: p.t.Conn.M.Provider, Status: se.Status, Refusal: se.Refusal, Msg: se.Message, State: se.State,
			RetryAfterMs: int(se.RetryAfter / time.Millisecond)}
	}
	if p.ctx.Err() != nil {
		return p.ctx.Err()
	}
	return &sbxError{Provider: p.t.Conn.M.Provider, Refusal: "unreachable", Msg: err.Error()}
}

// reflush sends a socket the reader attached again what the one before it
// may have swallowed.
func (p *harnessPipe) reflush() {
	p.wmu.Lock()
	defer p.wmu.Unlock()
	p.mu.Lock()
	c := p.sock
	p.mu.Unlock()
	if c != nil && len(p.outbox) > 0 {
		if err := p.flush(c); err != nil {
			p.dropSock(c)
		}
	}
}
