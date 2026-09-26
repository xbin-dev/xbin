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
	"sort"
	"strconv"
	"sync"

	"github.com/xbin-dev/xbin/internal/agent"
)

type elicits struct {
	mu   sync.Mutex
	next int
	pend map[string]pendingElicit // eid → the request
}

// pendingElicit is one question awaiting an answer: the request's rpc id
// (for the reply) and what the clients were shown.
type pendingElicit struct {
	rpcID json.RawMessage
	q     agent.Elicitation
}

// add files a question; q.EID is assigned here.
func (e *elicits) add(rpcID json.RawMessage, q agent.Elicitation) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pend == nil {
		e.pend = map[string]pendingElicit{}
	}
	e.next++
	q.EID = "e" + strconv.Itoa(e.next)
	e.pend[q.EID] = pendingElicit{rpcID: rpcID, q: q}
	return q.EID
}

func (e *elicits) take(eid string) (json.RawMessage, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pend[eid]
	delete(e.pend, eid)
	return p.rpcID, ok
}

// list is the pending questions, oldest first.
func (e *elicits) list() []agent.Elicitation {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]agent.Elicitation, 0, len(e.pend))
	for _, p := range e.pend {
		out = append(out, p.q)
	}
	num := func(eid string) int { n, _ := strconv.Atoi(eid[1:]); return n }
	sort.Slice(out, func(i, j int) bool { return num(out[i].EID) < num(out[j].EID) })
	return out
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
	q := agent.Elicitation{ToolCallID: p.ToolCallID, Message: p.Message, Schema: p.RequestedSchema}
	q.EID = c.elicits.add(m.ID, q)
	c.emit(agent.New(agent.EvElicitRequest, q))
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

// PendingElicitations is the questions still waiting for an answer, oldest
// first (GET /term/sessions/<id> lists them beside the permissions).
func (c *Client) PendingElicitations() []agent.Elicitation { return c.elicits.list() }

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
