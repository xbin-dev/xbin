package acp

import (
	"encoding/json"
	"strings"
)

// The agent's side of the conversation: its notifications (session
// updates, withdrawn requests, the sign-in status, extensions), its
// requests (permissions, questions), and the answers the client sends back.

func (c *Client) onNotify(m *Message) {
	w := c.wireOf(m)
	switch m.Method {
	case MSessionUpdate:
		var su SessionUpdate
		if json.Unmarshal(m.Params, &su) != nil {
			return
		}
		c.onUpdate(w, su.Update)
	case MCancelRequest:
		var p CancelRequestParams
		if json.Unmarshal(m.Params, &p) != nil {
			return
		}
		if res := c.cfg.Perms.CancelByRPC(p.RequestID); res != nil {
			res.Cancel = false // an explicit -32800, as the protocol asks
			w.RPCID = p.RequestID
			_ = c.respondPermission(w, res)
		}
	case MAuthStatus:
		var p struct {
			AuthStatus struct{ Kind, Label string } `json:"authStatus"`
		}
		if json.Unmarshal(m.Params, &p) == nil {
			c.setAuthNeededW(w, p.AuthStatus.Kind == "none")
		}
	case MElicitComplete:
		if !c.elicitURL() { // not advertised: an extension like any other
			c.onExt(m)
			return
		}
		c.onElicitComplete(w, m)
	default:
		c.onExt(m)
	}
}

// onExt is a notification the client does not handle itself.
func (c *Client) onExt(m *Message) {
	if c.opts.OnExt != nil && c.opts.OnExt(c.cfg, m) {
		return
	}
	if !strings.HasPrefix(m.Method, "_") {
		c.logf("ignoring notification %s", m.Method)
	}
}

func (c *Client) onUpdate(w *Wire, raw json.RawMessage) {
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
			c.emitW(w, NewEvent(EvThoughtDelta, withParent(map[string]any{"text": text}, raw)))
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
		c.emitW(w, NewEvent(EvMessageDelta, withParent(d, raw)))
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
		typ := EvToolUpdate
		if env.SessionUpdate == UpToolCall {
			typ = EvToolCall
		}
		c.emitW(w, NewEvent(typ, d))
	case UpPlan:
		var u PlanUpdate
		if json.Unmarshal(raw, &u) == nil {
			c.emitW(w, NewEvent(EvPlan, map[string]any{"entries": u.Entries}))
		}
	case UpUsage:
		var u UsageUpdate
		if json.Unmarshal(raw, &u) == nil {
			c.mu.Lock()
			c.usage = &u
			c.mu.Unlock()
			c.emitW(w, c.partialStatus(map[string]any{"usage": u}))
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
			c.emitW(w, c.partialStatus(map[string]any{"currentMode": u.CurrentModeID}))
		}
	case UpConfigOption:
		var u ConfigOptionUpdate
		if json.Unmarshal(raw, &u) == nil && len(u.ConfigOptions) > 0 {
			c.mu.Lock()
			c.options = u.ConfigOptions
			c.mu.Unlock()
			c.emitW(w, c.optionsEvent())
		}
	case UpSessionInfo:
		var u SessionInfoUpdate
		if json.Unmarshal(raw, &u) == nil && u.Title != "" {
			c.emitW(w, c.partialStatus(map[string]any{"title": u.Title}))
		}
	case UpAvailableCmds:
		c.onCommands(w, raw)
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
			return nil, &Error{Code: CodeInvalidParams, Message: "bad request_permission params"}
		}
		tc := toolRef(p.ToolCall)
		opts := make([]PermissionOption, len(p.Options))
		for i, o := range p.Options {
			opts[i] = PermissionOption{OptionID: o.OptionID, Name: o.Name, Kind: o.Kind}
		}
		w := c.wireOf(m)
		pd, auto, dup := c.cfg.Perms.file(tc, opts, m.ID)
		if dup { // read again after a handoff: it is already waiting
			return nil, nil
		}
		// rule: what "allow for the session" would remember (nothing when the
		// call has neither kind nor title — the clients hide the option then)
		c.emitW(w, NewEvent(EvPermissionRequest, map[string]any{"pid": pd.PID, "toolCall": tc, "options": opts,
			"rule": map[string]any{"kind": tc.Kind, "title": tc.Title, "scoped": tc.Rule()}, "meta": permissionMeta(m.Params)}))
		if auto != nil {
			_ = c.respondPermission(w, auto)
			return nil, nil
		}
		c.setStatusW(w, StatusWaiting, "")
		return nil, nil // answered by RespondPermission
	case MElicitCreate:
		return c.onElicit(m)
	case MFsRead, MFsWrite, MTermCreate, MTermOutput, MTermWait, MTermKill, MTermRelease:
		// a proxy between us and the agent (xbind's in-sandbox host) answers
		// these before they reach us; without one they are not served
		return nil, &Error{Code: CodeMethodNotFound, Message: m.Method + " is served by the agent host"}
	default:
		return nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + m.Method}
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

// Cancel interrupts the running turn: the agent gets session/cancel, every
// pending permission is answered cancelled, and the turn's unfinished tool
// calls are marked cancelled for the clients (the agent's own updates keep
// flowing; the prompt ends with stopReason cancelled). A prompt still
// handing its files over (ClientOptions.Drop) is aborted instead: it
// returns ErrCancelled and no turn starts.
func (c *Client) Cancel() error {
	c.mu.Lock()
	if c.prepCancel != nil { // a prompt still handing its files over: it never becomes a turn
		c.prepCancel(ErrCancelled)
		c.mu.Unlock()
		return nil
	}
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
		c.emit(NewEvent(EvToolUpdate, map[string]any{"id": id, "status": "cancelled"}))
	}
	for _, res := range c.cfg.Perms.CancelAll() {
		_ = c.RespondPermission(res)
	}
	c.cancelElicits()
	c.setStatus(StatusCancelling, "")
	return c.conn.Notify(MSessionCancel, SessionIDParams{SessionID: sid})
}

// RespondPermission answers the agent (selected or cancelled) and tells the
// clients.
func (c *Client) RespondPermission(res *Resolution) error {
	return c.respondPermission(&Wire{RPCID: res.RPCID}, res)
}

func (c *Client) respondPermission(w *Wire, res *Resolution) error {
	out := RequestPermissionResult{Outcome: PermissionOutcome{Outcome: "selected", OptionID: res.OptionID}}
	if res.Cancel {
		out.Outcome = PermissionOutcome{Outcome: "cancelled"}
	}
	// logged before the agent hears it, so the resolution precedes whatever
	// the agent does next in every client's stream
	c.emitW(w, NewEvent(EvPermissionResolved, map[string]any{"pid": res.PID, "optionId": res.OptionID, "by": res.By}))
	// running again before the agent hears it: said after the reply, it
	// could land after a turn the answer let end (idle, then a stale running)
	c.afterAnswer(w)
	if res.By == "cancel" && res.OptionID == "" && !res.Cancel {
		return c.conn.Reply(res.RPCID, nil, &Error{Code: CodeRequestCancelled, Message: "request cancelled"})
	}
	return c.conn.Reply(res.RPCID, out, nil)
}
