package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
)

// Client is the ACP client side for one session: it owns the agent process,
// runs the handshake, turns prompts into turns and the agent's updates into
// agent.Events, and holds permission requests open until answered. It is
// the package's agent.Driver (driver.go).
type Client struct {
	cfg    agent.Config
	proc   *agent.Process
	conn   *Conn
	events chan agent.Event
	// the channel's lifecycle: emit holds emu for reading while it sends;
	// the read loop's end closes stop (unblocking senders), then takes emu
	// for writing and closes events. No send can race the close.
	emu  sync.RWMutex
	stop chan struct{}

	mu        sync.Mutex
	sessionID string
	modes     *SessionModes
	turn      uint64
	busy      bool
	status    string
	usage     *UsageUpdate
	tools     map[string]string // tool call id → last status, this turn
	closed    bool
	done      chan struct{}
}

// New returns an unstarted client.
func New() *Client {
	return &Client{events: make(chan agent.Event, 256), tools: map[string]string{}, done: make(chan struct{}), stop: make(chan struct{})}
}

func (c *Client) Events() <-chan agent.Event { return c.events }

// Start spawns the agent and completes initialize → (authenticate) →
// session/new → (set_mode); the first status event says idle, or error.
func (c *Client) Start(ctx context.Context, cfg agent.Config) error {
	c.cfg = cfg
	if cfg.Spawn == nil {
		return errors.New("acp: no spawner")
	}
	proc, err := cfg.Spawn(ctx, cfg)
	if err != nil {
		return err
	}
	c.proc = proc
	c.conn = NewConn(proc.Stdout, proc.Stdin)
	c.conn.OnRequest = c.onRequest
	c.conn.OnNotify = c.onNotify
	c.conn.OnBad = func(err error) { c.logf("%v", err) }
	if proc.Stderr != nil {
		go c.drainStderr(proc.Stderr)
	}
	go func() {
		err := c.conn.Serve()
		c.mu.Lock()
		c.closed = true
		failed := c.status == agent.StatusError
		c.mu.Unlock()
		if !failed { // an error status keeps its detail; exited says the rest
			detail := "the agent process ended"
			if err != nil {
				detail += ": " + err.Error()
			}
			c.setStatus(agent.StatusExited, detail)
		}
		close(c.stop)
		c.emu.Lock()
		close(c.events)
		c.emu.Unlock()
		close(c.done)
	}()
	if err := c.handshake(); err != nil {
		c.setStatus(agent.StatusError, err.Error())
		c.Close()
		return err
	}
	return nil
}

func (c *Client) handshake() error {
	c.setStatus(agent.StatusStarting, "")
	var init InitializeResult
	if err := c.conn.Call(MInitialize, InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: ClientCapabilities{FS: FSCapabilities{ReadTextFile: true, WriteTextFile: true}, Terminal: true},
		ClientInfo:         &Info{Name: "xbin", Version: c.cfg.Version},
	}, &init); err != nil {
		return fmt.Errorf("initialize: %w", err)
	}
	if init.ProtocolVersion != ProtocolVersion {
		c.logf("agent speaks protocol version %d, we speak %d — continuing", init.ProtocolVersion, ProtocolVersion)
	}
	// An API-key auth method is called when the provider names one and the
	// key is present; every other method is the CLI's own business (its
	// home holds the login). Unknown-method errors are not fatal here.
	if m := c.cfg.Provider.Auth; m != "" && hasKey(c.cfg) {
		for _, am := range init.AuthMethods {
			if am.ID == m {
				if err := c.conn.Call(MAuthenticate, map[string]string{"methodId": m}, nil); err != nil {
					c.logf("authenticate %s: %v", m, err)
				}
			}
		}
	}
	var sess SessionNewResult
	if err := c.conn.Call(MSessionNew, SessionNewParams{Cwd: c.cfg.Cwd, MCPServers: []any{}}, &sess); err != nil {
		return fmt.Errorf("session/new: %w", authHint(err, c.cfg))
	}
	c.mu.Lock()
	c.sessionID = sess.SessionID
	c.modes = sess.Modes
	c.mu.Unlock()
	if want := c.cfg.Mode; want != "" && (sess.Modes == nil || sess.Modes.CurrentModeID != want) {
		if err := c.conn.Call(MSessionSetMode, SetModeParams{SessionID: sess.SessionID, ModeID: want}, nil); err != nil {
			c.logf("set_mode %s: %v", want, err)
		} else if sess.Modes != nil {
			c.mu.Lock()
			c.modes.CurrentModeID = want
			c.mu.Unlock()
		}
	}
	c.setStatus(agent.StatusIdle, "")
	return nil
}

func hasKey(cfg agent.Config) bool {
	for _, e := range cfg.Env {
		for _, k := range cfg.Provider.Keys {
			if strings.HasPrefix(e, k+"=") {
				return true
			}
		}
	}
	return false
}

// authHint turns the agent's -32000 into the operator's next step.
func authHint(err error, cfg agent.Config) error {
	var re *Error
	if errors.As(err, &re) && re.Code == ErrAuthRequired {
		return fmt.Errorf("the agent needs credentials (%s): %s", re.Message, cfg.Provider.KeyHint(tileOf(cfg)))
	}
	return err
}

func tileOf(cfg agent.Config) string {
	if t := cfg.Meta["tile"]; t != "" {
		return t
	}
	return "<tile>"
}

// Send starts a turn. One at a time.
func (c *Client) Send(ctx context.Context, text string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return agent.ErrEnded
	}
	if c.busy {
		c.mu.Unlock()
		return agent.ErrBusy
	}
	c.busy = true
	c.turn++
	turn := c.turn
	c.tools = map[string]string{}
	c.usage = nil
	sid := c.sessionID
	c.mu.Unlock()
	c.emit(agent.New(agent.EvMessageDelta, map[string]any{"role": "user", "text": text}))
	c.setStatus(agent.StatusRunning, "")
	go func() {
		var res PromptResult
		err := c.conn.Call(MSessionPrompt, PromptParams{SessionID: sid, Prompt: []ContentBlock{{Type: "text", Text: text}}}, &res)
		c.mu.Lock()
		c.busy = false
		usage := c.usage
		c.mu.Unlock()
		if err != nil {
			if errors.Is(err, io.ErrClosedPipe) {
				return // the exit status says it
			}
			c.emit(agent.New(agent.EvTurnEnd, map[string]any{"turn": turn, "stopReason": "error", "error": authHint(err, c.cfg).Error()}))
			c.setStatus(agent.StatusError, authHint(err, c.cfg).Error())
			return
		}
		end := map[string]any{"turn": turn, "stopReason": res.StopReason}
		if usage != nil {
			end["usage"] = usage
		}
		c.emit(agent.New(agent.EvTurnEnd, end))
		c.setStatus(agent.StatusIdle, "")
	}()
	return nil
}

// Cancel interrupts the running turn: the agent gets session/cancel, every
// pending permission is answered cancelled, and the turn's unfinished tool
// calls are marked cancelled for the clients (the agent's own updates keep
// flowing; the prompt ends with stopReason cancelled).
func (c *Client) Cancel() error {
	c.mu.Lock()
	sid, busy := c.sessionID, c.busy
	var unfinished []string
	for id, st := range c.tools {
		if st != "completed" && st != "failed" && st != "cancelled" {
			unfinished = append(unfinished, id)
			c.tools[id] = "cancelled"
		}
	}
	c.mu.Unlock()
	if !busy {
		return nil
	}
	// the clients' view first (tool marks, resolutions), then the agent: what
	// the agent does in consequence lands after these in the log
	for _, id := range unfinished {
		c.emit(agent.New(agent.EvToolUpdate, map[string]any{"id": id, "status": "cancelled"}))
	}
	for _, res := range c.cfg.Perms.CancelAll() {
		_ = c.RespondPermission(res)
	}
	return c.conn.Notify(MSessionCancel, SessionIDParams{SessionID: sid})
}

// RespondPermission answers the agent (selected or cancelled) and tells the
// clients.
func (c *Client) RespondPermission(res *agent.Resolution) error {
	out := RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "selected", OptionID: res.OptionID}}
	if res.Cancel {
		out.Outcome = PermissionOutcome{Outcome: "cancelled"}
	}
	// logged before the agent hears it, so the resolution precedes whatever
	// the agent does next in every client's stream
	c.emit(agent.New(agent.EvPermissionResolved, map[string]any{"pid": res.PID, "optionId": res.OptionID, "by": res.By}))
	var err error
	if res.By == "cancel" && res.OptionID == "" && !res.Cancel {
		err = c.conn.Reply(res.RPCID, nil, &Error{Code: ErrCancelled, Message: "request cancelled"})
	} else {
		err = c.conn.Reply(res.RPCID, out, nil)
	}
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if busy && c.cfg.Perms.Count() == 0 {
		c.setStatus(agent.StatusRunning, "")
	}
	return err
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

func (c *Client) Mode() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.modes != nil {
		return c.modes.CurrentModeID
	}
	return c.cfg.Mode
}

// ---- incoming ----

func (c *Client) onNotify(m *Message) {
	switch m.Method {
	case MSessionUpdate:
		var su SessionUpdate
		if json.Unmarshal(m.Params, &su) != nil {
			return
		}
		c.onUpdate(su.Update)
	case MCancelRequest:
		var p CancelRequestParams
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		if res := c.cfg.Perms.CancelByRPC(p.RequestID); res != nil {
			res.Cancel = false // an explicit -32800, as the protocol asks
			_ = c.RespondPermission(res)
		}
	case MXbinLog:
		var p LogParams
		if json.Unmarshal(m.Params, &p) == nil {
			c.logf("%s", p.Text)
		}
	default:
		if !strings.HasPrefix(m.Method, "_") {
			c.logf("ignoring notification %s", m.Method)
		}
	}
}

func (c *Client) onUpdate(raw json.RawMessage) {
	var env UpdateEnvelope
	if json.Unmarshal(raw, &env) != nil {
		return
	}
	switch env.SessionUpdate {
	case UpAgentChunk, UpUserChunk, UpThoughtChunk:
		var u ChunkUpdate
		if json.Unmarshal(raw, &u) != nil || u.Content.Type != "text" {
			return
		}
		if env.SessionUpdate == UpThoughtChunk {
			c.emit(agent.New(agent.EvThoughtDelta, map[string]any{"text": u.Content.Text}))
			return
		}
		role := "agent"
		if env.SessionUpdate == UpUserChunk {
			role = "user"
		}
		d := map[string]any{"role": role, "text": u.Content.Text}
		if u.MessageID != "" {
			d["messageId"] = u.MessageID
		}
		c.emit(agent.New(agent.EvMessageDelta, d))
	case UpToolCall, UpToolCallUpdate:
		var u ToolCallUpdate
		if json.Unmarshal(raw, &u) != nil {
			return
		}
		d := map[string]any{"id": u.ToolCallID}
		if u.Title != nil {
			d["title"] = *u.Title
		}
		if u.Kind != nil {
			d["kind"] = *u.Kind
		}
		if u.Status != nil {
			d["status"] = *u.Status
		} else if env.SessionUpdate == UpToolCall {
			d["status"] = "pending"
		}
		for k, v := range map[string]json.RawMessage{"content": u.Content, "locations": u.Locations, "rawInput": u.RawInput, "rawOutput": u.RawOutput} {
			if len(v) > 0 {
				d[k] = v
			}
		}
		c.mu.Lock()
		if s, ok := d["status"].(string); ok {
			c.tools[u.ToolCallID] = s
		} else if _, seen := c.tools[u.ToolCallID]; !seen {
			c.tools[u.ToolCallID] = "pending"
		}
		c.mu.Unlock()
		typ := agent.EvToolUpdate
		if env.SessionUpdate == UpToolCall {
			typ = agent.EvToolCall
		}
		c.emit(agent.New(typ, d))
	case UpPlan:
		var u PlanUpdate
		if json.Unmarshal(raw, &u) == nil {
			c.emit(agent.New(agent.EvPlan, map[string]any{"entries": u.Entries}))
		}
	case UpUsage:
		var u UsageUpdate
		if json.Unmarshal(raw, &u) == nil {
			c.mu.Lock()
			c.usage = &u
			c.mu.Unlock()
			c.emit(agent.New(agent.EvStatus, map[string]any{"status": c.Status(), "usage": u}))
		}
	case UpCurrentMode:
		var u CurrentModeUpdate
		if json.Unmarshal(raw, &u) == nil {
			c.mu.Lock()
			if c.modes == nil {
				c.modes = &SessionModes{}
			}
			c.modes.CurrentModeID = u.CurrentModeID
			c.mu.Unlock()
			c.emit(agent.New(agent.EvStatus, map[string]any{"status": c.Status(), "currentMode": u.CurrentModeID}))
		}
	default: // available_commands_update, session_info_update, config_option_update, unknown: nothing to show
	}
}

func (c *Client) onRequest(m *Message) (any, *Error) {
	switch m.Method {
	case MRequestPermission:
		var p RequestPermissionParams
		if json.Unmarshal(m.Params, &p) != nil {
			return nil, &Error{Code: ErrInvalidParam, Message: "bad request_permission params"}
		}
		tc := agent.ToolCallRef{ID: p.ToolCall.ToolCallID, RawInput: p.ToolCall.RawInput, Content: p.ToolCall.Content}
		if p.ToolCall.Title != nil {
			tc.Title = *p.ToolCall.Title
		}
		if p.ToolCall.Kind != nil {
			tc.Kind = *p.ToolCall.Kind
		}
		opts := make([]agent.PermissionOption, len(p.Options))
		for i, o := range p.Options {
			opts[i] = agent.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind}
		}
		pd, auto := c.cfg.Perms.Request(tc, opts, m.ID)
		c.emit(agent.New(agent.EvPermissionRequest, map[string]any{"pid": pd.PID, "toolCall": tc, "options": opts}))
		if auto != nil {
			_ = c.RespondPermission(auto)
			return nil, nil
		}
		c.setStatus(agent.StatusWaiting, "")
		return nil, nil // answered by RespondPermission
	case MFsRead, MFsWrite, MTermCreate, MTermOutput, MTermWait, MTermKill, MTermRelease:
		// the in-sandbox host answers these before they reach us; without a
		// host (a bare spawner) they are not served
		return nil, &Error{Code: ErrNotFound, Message: m.Method + " is served by the agent host"}
	default:
		return nil, &Error{Code: ErrNotFound, Message: "method not found: " + m.Method}
	}
}

// ---- outgoing events ----

// emit queues an event for the pump; after the loop's end it is dropped.
func (c *Client) emit(e agent.Event) { c.send(e, true) }

func (c *Client) send(e agent.Event, wait bool) {
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

func (c *Client) setStatus(status, detail string) {
	c.mu.Lock()
	c.status = status
	modes := c.modes
	c.mu.Unlock()
	d := map[string]any{"status": status}
	if detail != "" {
		d["detail"] = detail
	}
	if modes != nil {
		d["currentMode"] = modes.CurrentModeID
		if status == agent.StatusIdle || status == agent.StatusStarting {
			d["modes"] = modes.AvailableModes
		}
	}
	// a terminal status never blocks on a pump that is gone
	c.send(agent.New(agent.EvStatus, d), status != agent.StatusExited && status != agent.StatusError)
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
