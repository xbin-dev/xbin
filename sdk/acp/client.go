package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
)

// Client is the ACP client side for one session: it owns the agent process,
// runs the handshake, turns prompts into turns and the agent's updates into
// Events, and holds permission requests and questions open until answered.
type Client struct {
	opts   ClientOptions
	cfg    Config
	proc   *Process
	conn   *Conn
	events chan Event
	// the channel's lifecycle: emit holds emu for reading while it sends;
	// the read loop's end closes stop (unblocking senders), then takes emu
	// for writing and closes events. No send can race the close.
	emu  sync.RWMutex
	stop chan struct{}

	mu          sync.Mutex
	sessionID   string
	modes       *SessionModes
	options     []ConfigOption // the agent's session settings (model, effort, …)
	commands    []Command      // its slash commands (commands.go)
	elicits     elicits        // questions awaiting an answer (elicit.go)
	agentInfo   *Info          // what initialize said the agent is
	authNeeded  bool           // the agent reported it is not signed in (login required)
	loadable    bool           // the agent advertised loadSession: its session id can be reopened later
	steering    bool           // the agent advertised _session/steering (steer.go)
	authMethods []AuthMethod   // how it signs in (auth.go)
	initialized bool           // initialize answered: Authenticate may open the session
	promptCaps  PromptCapabilities
	turn        uint64
	busy        bool
	promptRPC   json.RawMessage         // the in-flight session/prompt's request id (State)
	preparing   bool                    // a prompt's files are on their way to the agent (prompt.go): the next prompt is busy
	prepCancel  context.CancelCauseFunc // aborts that hand-off (Cancel)
	status      string
	usage       *UsageUpdate
	tools       map[string]string // tool call id → last status, this turn
	closed      bool
	done        chan struct{}
	// replaying: a session/load is streaming earlier turns back (Wire.Replay);
	// set before the load is sent, cleared on the read loop by its response
	replaying atomic.Bool
}

// New returns an unstarted client with the default options.
func New() *Client { return NewWith(ClientOptions{}) }

// NewWith returns an unstarted client with the embedder's seams.
func NewWith(o ClientOptions) *Client {
	return &Client{opts: o, events: make(chan Event, 256), tools: map[string]string{}, done: make(chan struct{}), stop: make(chan struct{})}
}

// Events is the session's typed stream, closed once the agent is gone (its
// last event a status exited or error). Read it steadily: the client waits
// for room to send.
func (c *Client) Events() <-chan Event { return c.events }

// Start spawns the agent and completes initialize → session/new (or
// session/load for cfg.ResumeID) → the requested mode and options; the
// first status event says starting, then idle or error. A Start that fails
// still ends the event stream (with the error status). Once per Client.
func (c *Client) Start(ctx context.Context, cfg Config) error {
	c.cfg = cfg
	switch {
	case cfg.Perms == nil:
		return c.abort(errors.New("acp: no Perms"))
	case cfg.Spawn == nil:
		return c.abort(errors.New("acp: no spawner"))
	}
	proc, err := cfg.Spawn(ctx, cfg)
	if err != nil {
		return c.abort(err) // no read loop will close the events: abort.go
	}
	c.proc = proc
	c.conn = NewConnWith(proc.Stdout, proc.Stdin, ConnOptions{IDPrefix: c.opts.IDPrefix, Offset: proc.Off})
	c.conn.OnRequest = c.onRequest
	c.conn.OnNotify = c.onNotify
	c.conn.OnBad = func(err error) { c.logf("%v", err) }
	c.conn.onResponse = func(method string, m *Message) {
		if method == MSessionLoad {
			c.replaying.Store(false) // what follows the load's answer is live
		}
	}
	if c.opts.Attach != nil {
		c.restore(*c.opts.Attach) // before the loop: the prompt's answer may be next (state.go)
	}
	if proc.Stderr != nil {
		go c.drainStderr(proc.Stderr)
	}
	if c.opts.Attach != nil { // first, before anything the loop reads
		c.setStatus(c.attachedStatus(), "")
	}
	go func() {
		err := c.conn.Serve()
		c.mu.Lock()
		c.closed = true
		failed := c.status == StatusError
		c.mu.Unlock()
		if !failed { // an error status keeps its detail; exited says the rest
			detail := "the agent process ended"
			if err != nil {
				detail += ": " + err.Error()
			}
			c.setStatus(StatusExited, detail)
		}
		close(c.stop)
		c.emu.Lock()
		close(c.events)
		c.emu.Unlock()
		close(c.done)
	}()
	if c.opts.Attach != nil {
		return nil
	}
	if err := c.handshake(); err != nil {
		c.setStatus(StatusError, err.Error())
		if !(c.opts.AwaitLogin && c.signedOut()) {
			c.Close()
		}
		return err
	}
	return nil
}

// Session is the agent's own session id and whether it advertised
// loadSession — persisted with the transcript so the id can reopen the
// conversation later (resume).
func (c *Client) Session() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID, c.loadable
}

// Close ends the agent process; the read loop's end reports exited.
func (c *Client) Close() error {
	c.mu.Lock()
	if c.proc == nil {
		c.mu.Unlock()
		return nil
	}
	proc := c.proc
	c.mu.Unlock()
	if proc.Stdin != nil {
		_ = proc.Stdin.Close()
	}
	if proc.Kill != nil {
		proc.Kill()
	}
	return nil
}

// Done is closed once the read loop has ended.
func (c *Client) Done() <-chan struct{} { return c.done }

// Status is the last status set; Modes what the agent advertised.
func (c *Client) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Mode is the session's current mode (the requested one until the agent
// reports its own).
func (c *Client) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.modes != nil {
		return c.modes.CurrentModeID
	}
	return c.cfg.Mode
}

// ---- outgoing events ----

// emit queues an event for the pump; after the loop's end it is dropped.
func (c *Client) emit(e Event) { c.send(e, true) }

// emitW is emit with the event's place on the wire (nil: none).
func (c *Client) emitW(w *Wire, e Event) {
	e.Wire = w
	c.send(e, true)
}

// wireOf is where a frame the agent sent puts the events it causes: the
// offset after it, the request's id when it asks something, and whether a
// session/load is replaying earlier turns.
func (c *Client) wireOf(m *Message) *Wire {
	w := &Wire{Off: m.off, Replay: c.replaying.Load()}
	if m.IsRequest() {
		w.RPCID = m.ID
	}
	return w
}

func (c *Client) send(e Event, wait bool) {
	c.emu.RLock()
	defer c.emu.RUnlock()
	select {
	case <-c.stop:
		return
	default:
	}
	if !wait {
		select {
		case c.events <- e:
		default:
		}
		return
	}
	select {
	case c.events <- e:
	case <-c.stop:
	}
}

func (c *Client) logf(format string, a ...any) {
	if c.cfg.Log != nil {
		c.cfg.Log(fmt.Sprintf(format, a...))
	}
}

func (c *Client) drainStderr(r io.Reader) {
	buf := make([]byte, 4096)
	var line []byte
	for {
		n, err := r.Read(buf)
		for _, b := range buf[:n] {
			if b == '\n' {
				if s := strings.TrimRight(string(line), "\r"); s != "" {
					c.logf("%s", s)
				}
				line = line[:0]
			} else {
				line = append(line, b)
			}
		}
		if err != nil {
			if s := strings.TrimRight(string(line), "\r"); s != "" {
				c.logf("%s", s)
			}
			return
		}
	}
}
