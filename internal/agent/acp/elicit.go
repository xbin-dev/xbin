package acp

// Questions the agent asks the user (D77): ACP's elicitation/create in form
// mode — Claude's AskUserQuestion (a single- or multi-select per question,
// each with an "Other" box), an MCP server's form. Like a permission request
// it is held until a client answers: an elicitation.request event carries
// the message and the requested JSON schema, the session waits, and the
// first answer (accept with the form's values, decline, cancel) goes back
// as the JSON-RPC reply and an elicitation.resolved event. Cancelling the
// turn answers cancel. Only form mode is advertised; a url-mode request is
// declined.

import (
	"encoding/json"
	"strconv"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
)

type elicits struct {
	mu   sync.Mutex
	next int
	pend map[string]json.RawMessage // eid → the request's rpc id
}

func (e *elicits) add(rpcID json.RawMessage) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pend == nil {
		e.pend = map[string]json.RawMessage{}
	}
	e.next++
	eid := "e" + strconv.Itoa(e.next)
	e.pend[eid] = rpcID
	return eid
}

func (e *elicits) take(eid string) (json.RawMessage, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	id, ok := e.pend[eid]
	delete(e.pend, eid)
	return id, ok
}

func (e *elicits) count() int { e.mu.Lock(); defer e.mu.Unlock(); return len(e.pend) }

func (e *elicits) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.pend))
	for eid := range e.pend {
		out = append(out, eid)
	}
	return out
}

func (c *Client) onElicit(m *Message) (any, *Error) {
	var p struct {
		Mode            string          `json:"mode"`
		ToolCallID      string          `json:"toolCallId"`
		Message         string          `json:"message"`
		RequestedSchema json.RawMessage `json:"requestedSchema"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return nil, &Error{Code: ErrInvalidParam, Message: "bad elicitation/create params"}
	}
	if p.Mode != "" && p.Mode != "form" {
		return map[string]string{"action": "decline"}, nil // we advertise form only
	}
	eid := c.elicits.add(m.ID)
	d := map[string]any{"eid": eid, "message": p.Message, "schema": p.RequestedSchema}
	if p.ToolCallID != "" {
		d["toolCallId"] = p.ToolCallID
	}
	c.emit(agent.New(agent.EvElicitRequest, d))
	c.setStatus(agent.StatusWaiting, "")
	return nil, nil // answered by RespondElicitation
}

// RespondElicitation answers a pending question: action accept (content =
// the form's values), decline or cancel.
func (c *Client) RespondElicitation(eid, action string, content json.RawMessage, by string) error {
	switch action {
	case "accept", "decline", "cancel":
	default:
		return errBadAction
	}
	rpcID, ok := c.elicits.take(eid)
	if !ok {
		return agent.ErrNoElicitation
	}
	out := map[string]any{"action": action}
	res := map[string]any{"eid": eid, "action": action, "by": by}
	if action == "accept" {
		if len(content) == 0 || string(content) == "null" {
			content = json.RawMessage(`{}`)
		}
		out["content"], res["content"] = content, content // the transcript shows what was answered
	}
	// logged before the agent hears it, like a permission's resolution
	c.emit(agent.New(agent.EvElicitResolved, res))
	err := c.conn.Reply(rpcID, out, nil)
	c.mu.Lock()
	busy, st := c.busy, c.status
	c.mu.Unlock()
	if busy && st != agent.StatusCancelling && c.cfg.Perms.Count() == 0 && c.elicits.count() == 0 {
		c.setStatus(agent.StatusRunning, "")
	}
	return err
}

// cancelElicits answers every pending question "cancel" (the turn is being
// cancelled).
func (c *Client) cancelElicits() {
	for _, eid := range c.elicits.all() {
		_ = c.RespondElicitation(eid, "cancel", nil, "cancel")
	}
}

type badAction struct{}

func (badAction) Error() string { return "action must be accept, decline or cancel" }

var errBadAction error = badAction{}
