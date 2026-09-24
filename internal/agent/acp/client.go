package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

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

	mu         sync.Mutex
	sessionID  string
	modes      *SessionModes
	options    []ConfigOption // the agent's session settings (model, effort, …)
	commands   []Command      // its slash commands (commands.go)
	agentInfo  *Info          // what initialize said the agent is
	authNeeded bool           // the agent reported it is not signed in (login required)
	loadable   bool           // the agent advertised loadSession: its session id can be reopened later
	turn       uint64
	busy       bool
	status     string
	usage      *UsageUpdate
	tools      map[string]string // tool call id → last status, this turn
	closed     bool
	done       chan struct{}
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

// handshakeTimeout bounds initialize and session/new: an adapter that never
// answers (a broken install, a hung login) becomes a status error instead of
// a session stuck at "starting".
const handshakeTimeout = 90 * time.Second

func (c *Client) handshake() error {
	c.setStatus(agent.StatusStarting, "")
	ctx, cancel := context.WithTimeout(context.Background(), handshakeTimeout)
	defer cancel()
	var init InitializeResult
	if err := c.conn.CallCtx(ctx, MInitialize, InitializeParams{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: ClientCapabilities{FS: FSCapabilities{ReadTextFile: true, WriteTextFile: true}, Terminal: true, Meta: clientMeta()},
		ClientInfo:         &Info{Name: "xbin", Version: c.cfg.Version},
	}, &init); err != nil {
		return fmt.Errorf("initialize: %w", deadlineHint(err, "answer initialize"))
	}
	c.mu.Lock()
	c.agentInfo = init.AgentInfo
	c.loadable = init.AgentCapabilities != nil && init.AgentCapabilities.LoadSession
	c.mu.Unlock()
	if init.ProtocolVersion != ProtocolVersion {
		c.logf("agent speaks protocol version %d, we speak %d — continuing", init.ProtocolVersion, ProtocolVersion)
	}
	// No authenticate call: the CLI authenticates itself from its own $HOME
	// (the same per-user home a shell terminal gets), so a login done once in
	// a terminal serves every agent session. If the home holds no login,
	// session/new returns -32000 and we surface how to sign in.
	var sess SessionNewResult
	if c.cfg.ResumeID != "" {
		// resume: reopen the agent's earlier session — it streams the prior
		// turns back as session/update (they land in the log like live ones),
		// then the session continues. Only an agent that said loadSession.
		if !c.loadable {
			return agent.ErrResumeUnsupported
		}
		var ld SessionLoadResult
		if err := c.conn.CallCtx(ctx, MSessionLoad, SessionLoadParams{SessionID: c.cfg.ResumeID, Cwd: c.cfg.Cwd, MCPServers: []any{}, Meta: c.cfg.Provider.SessionMeta}, &ld); err != nil {
			return fmt.Errorf("session/load: %w", authHint(deadlineHint(err, "reopen the session"), c.cfg))
		}
		sess = SessionNewResult{SessionID: c.cfg.ResumeID, Modes: ld.Modes, ConfigOptions: ld.ConfigOptions}
	} else if err := c.conn.CallCtx(ctx, MSessionNew, SessionNewParams{Cwd: c.cfg.Cwd, MCPServers: []any{}, Meta: c.cfg.Provider.SessionMeta}, &sess); err != nil {
		return fmt.Errorf("session/new: %w", authHint(deadlineHint(err, "open a session"), c.cfg))
	}
	c.mu.Lock()
	c.sessionID = sess.SessionID
	c.modes = sess.Modes
	c.options = sess.ConfigOptions
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
	// requested settings (a model, an effort) — applied after the CLI has
	// loaded its own config, so they win over the home's defaults
	for _, id := range sortedOptionIDs(c.cfg.Options) {
		if cur, ok := c.option(id); !ok || cur.CurrentValue == c.cfg.Options[id] {
			if !ok {
				c.logf("option %s: the agent does not offer it", id)
			}
			continue
		}
		if err := c.setOption(id, c.cfg.Options[id]); err != nil {
			c.logf("set option %s=%s: %v", id, c.cfg.Options[id], err)
		}
	}
	c.setStatus(agent.StatusIdle, "")
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

// option finds one of the agent's config options by id.
func (c *Client) option(id string) (ConfigOption, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, o := range c.options {
		if o.ID == id {
			return o, true
		}
	}
	return ConfigOption{}, false
}

// setOption is the wire call; the response carries the refreshed list.
func (c *Client) setOption(id, value string) error {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	var res SetConfigResult
	if err := c.conn.Call(MSessionSetConfig, SetConfigParams{SessionID: sid, ConfigID: id, Value: value}, &res); err != nil {
		return err
	}
	c.mu.Lock()
	if len(res.ConfigOptions) > 0 {
		c.options = res.ConfigOptions
	} else { // an agent that answers {} — apply locally
		for i := range c.options {
			if c.options[i].ID == id {
				c.options[i].CurrentValue = value
			}
		}
	}
	c.mu.Unlock()
	return nil
}

// SetOption changes a session setting for the clients: the refreshed
// options ride a status event.
func (c *Client) SetOption(ctx context.Context, id, value string) error {
	if _, ok := c.option(id); !ok {
		if id == "mode" && c.hasMode(value) { // no "mode" config option: the older session/set_mode
			return c.setModeLive(value)
		}
		return fmt.Errorf("the agent offers no option %q", id)
	}
	if err := c.setOption(id, value); err != nil {
		return err
	}
	c.emitOptions()
	return nil
}

func (c *Client) hasMode(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.modes == nil {
		return false
	}
	for _, m := range c.modes.AvailableModes {
		if m.ID == id {
			return true
		}
	}
	return false
}

// setModeLive switches the permission mode mid-session (session/set_mode)
// and tells the clients through a status {currentMode}.
func (c *Client) setModeLive(mode string) error {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()
	if err := c.conn.Call(MSessionSetMode, SetModeParams{SessionID: sid, ModeID: mode}, nil); err != nil {
		return err
	}
	c.mu.Lock()
	if c.modes != nil {
		c.modes.CurrentModeID = mode
	}
	c.mu.Unlock()
	c.emit(agent.New(agent.EvStatus, map[string]any{"status": c.Status(), "currentMode": mode}))
	return nil
}

// emitOptions publishes the current options on a status event.
func (c *Client) emitOptions() {
	c.mu.Lock()
	opts := append([]ConfigOption(nil), c.options...)
	c.mu.Unlock()
	c.emit(agent.New(agent.EvStatus, map[string]any{"status": c.Status(), "options": opts}))
}

func sortedOptionIDs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// deadlineHint names a handshake timeout for the operator.
func deadlineHint(err error, what string) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("the agent did not %s within %s — its log (GET …/log) has the adapter's output", what, handshakeTimeout)
	}
	return err
}

// isAuthError reports whether a status detail is the "sign in" message
// authHint produced (the ACP -32000 is not visible at this layer).
func isAuthError(detail string) bool {
	return strings.Contains(detail, "Authentication required") || strings.Contains(detail, "isn't signed in")
}

// authHint turns the agent's -32000 into the operator's next step: sign the
// CLI in from a terminal, whose $HOME the agent shares.
func authHint(err error, cfg agent.Config) error {
	var re *Error
	if errors.As(err, &re) && re.Code == ErrAuthRequired {
		return fmt.Errorf("%s: %s", re.Message, cfg.Provider.LoginHint(tileOf(cfg)))
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
			var re *Error
			if errors.As(err, &re) && re.Code == ErrAuthRequired {
				c.mu.Lock()
				c.authNeeded = true
				c.mu.Unlock()
			}
			c.emit(agent.New(agent.EvTurnEnd, map[string]any{"turn": turn, "stopReason": "error", "error": authHint(err, c.cfg).Error()}))
			c.setStatus(agent.StatusError, authHint(err, c.cfg).Error())
			return
		}
		c.setAuthNeeded(false)
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
	c.setStatus(agent.StatusCancelling, "")
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
	busy, st := c.busy, c.status
	c.mu.Unlock()
	if busy && st != agent.StatusCancelling && c.cfg.Perms.Count() == 0 {
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
	case MAuthStatus:
		var p struct {
			AuthStatus struct{ Kind, Label string } `json:"authStatus"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			c.setAuthNeeded(p.AuthStatus.Kind == "none")
		}
	default:
		if !strings.HasPrefix(m.Method, "_") {
			c.logf("ignoring notification %s", m.Method)
		}
	}
}

// setAuthNeeded records whether the agent says it is signed out and, on a
// change, re-emits the current status so the clients show (or clear) the
// sign-in prompt. The status carries the login command (Provider.Login).
func (c *Client) setAuthNeeded(need bool) {
	c.mu.Lock()
	changed := c.authNeeded != need
	c.authNeeded = need
	st := c.status
	c.mu.Unlock()
	if changed && st != "" {
		c.setStatus(st, "")
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
		if json.Unmarshal(raw, &u) != nil {
			return
		}
		text := u.Content.Text
		if u.Content.Type != "text" { // an image, a resource, a link: say so rather than drop it
			text = contentPlaceholder(raw)
		}
		if env.SessionUpdate == UpThoughtChunk {
			c.emit(agent.New(agent.EvThoughtDelta, withParent(map[string]any{"text": text}, raw)))
			return
		}
		role := "agent"
		if env.SessionUpdate == UpUserChunk {
			role = "user"
		}
		d := map[string]any{"role": role, "text": text}
		if u.MessageID != "" {
			d["messageId"] = u.MessageID
		}
		c.emit(agent.New(agent.EvMessageDelta, withParent(d, raw)))
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
		addToolExtras(d, u) // name, label, subagent parent, terminal output (toolmeta.go)
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
	case UpConfigOption:
		var u ConfigOptionUpdate
		if json.Unmarshal(raw, &u) == nil && len(u.ConfigOptions) > 0 {
			c.mu.Lock()
			c.options = u.ConfigOptions
			c.mu.Unlock()
			c.emitOptions()
		}
	case UpSessionInfo:
		var u SessionInfoUpdate
		if json.Unmarshal(raw, &u) == nil && u.Title != "" {
			c.emit(agent.New(agent.EvStatus, map[string]any{"status": c.Status(), "title": u.Title}))
		}
	case UpAvailableCmds:
		c.onCommands(raw)
	default:
		c.logf("ignoring session update %s", env.SessionUpdate)
	}
}

// contentPlaceholder names a non-text content block in a chunk so the
// transcript shows that something was said: [image], [audio],
// [link: name], [resource: uri].
func contentPlaceholder(raw json.RawMessage) string {
	var u struct {
		Content struct {
			Type     string `json:"type"`
			Name     string `json:"name"`
			URI      string `json:"uri"`
			Resource struct {
				URI string `json:"uri"`
			} `json:"resource"`
		} `json:"content"`
	}
	_ = json.Unmarshal(raw, &u)
	cb := u.Content
	switch cb.Type {
	case "resource_link":
		if cb.Name != "" {
			return "[link: " + cb.Name + "]"
		}
		return "[link: " + cb.URI + "]"
	case "resource":
		return "[resource: " + cb.Resource.URI + "]"
	case "":
		return "[content]"
	}
	return "[" + cb.Type + "]"
}

func (c *Client) onRequest(m *Message) (any, *Error) {
	switch m.Method {
	case MRequestPermission:
		var p RequestPermissionParams
		if json.Unmarshal(m.Params, &p) != nil {
			return nil, &Error{Code: ErrInvalidParam, Message: "bad request_permission params"}
		}
		tc := toolRef(p.ToolCall)
		opts := make([]agent.PermissionOption, len(p.Options))
		for i, o := range p.Options {
			opts[i] = agent.PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind}
		}
		pd, auto := c.cfg.Perms.Request(tc, opts, m.ID)
		// rule: what "allow for the session" would remember (nothing when the
		// call has neither kind nor title — the clients hide the option then)
		c.emit(agent.New(agent.EvPermissionRequest, map[string]any{"pid": pd.PID, "toolCall": tc, "options": opts,
			"rule": map[string]any{"kind": tc.Kind, "title": tc.Title, "scoped": tc.Rule()}, "meta": permissionMeta(m.Params)}))
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

// permissionMeta lifts the adapter's presentation hints (_meta.permission:
// title, description, defaultToNo — the claude-agent-acp extension) so the
// clients can honour them; nil when absent.
func permissionMeta(params json.RawMessage) map[string]any {
	var p struct {
		Meta struct {
			Permission map[string]any `json:"permission"`
		} `json:"_meta"`
	}
	if json.Unmarshal(params, &p) != nil || len(p.Meta.Permission) == 0 {
		return nil
	}
	return p.Meta.Permission
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
	opts := append([]ConfigOption(nil), c.options...)
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
	if len(opts) > 0 && status == agent.StatusIdle {
		d["options"] = opts
	}
	if status == agent.StatusIdle && c.agentInfo != nil {
		d["agent"] = c.agentInfo
	}
	if status == agent.StatusIdle {
		c.withCommands(d)
	}
	c.mu.Lock()
	need := c.authNeeded
	c.mu.Unlock()
	if need || isAuthError(detail) {
		p := c.cfg.Provider
		d["login"] = map[string]any{"needed": true, "provider": p.Name, "command": p.Login}
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
