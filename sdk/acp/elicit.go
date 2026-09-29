package acp

// Questions the agent asks the user (D77): ACP's elicitation/create in form
// mode — Claude's AskUserQuestion (a single- or multi-select per question,
// each with an "Other" box), an MCP server's form. Like a permission request
// it is held until a client answers: an elicitation.request event carries
// the message and the requested JSON schema, the session waits, and the
// first answer (accept with the form's values, decline, cancel) goes back
// as the JSON-RPC reply and an elicitation.resolved event. Cancelling the
// turn answers cancel.
//
// URL mode (only with ElicitationCaps.URL advertised; declined otherwise):
// the agent asks the person to open a URL — codex-acp's device-code
// sign-in, during authenticate. The elicitation.request carries mode "url",
// the url, the message (with the code) and the agent's elicitationId; an
// accept is answered without content and stays remembered until the
// agent's elicitation/complete, which becomes an elicitation.resolved with
// action "complete" (by "agent") — a second one after the accept's, or the
// only one when the agent completes first (its request is then answered
// cancel).

import (
	"encoding/json"
	"sort"
	"strconv"
	"sync"
)

type elicits struct {
	mu   sync.Mutex
	next int
	pend map[string]pendingElicit // eid → the request
	urls map[string]pendingElicit // elicitationId → a url request accepted, until elicitation/complete
}

// pendingElicit is one question awaiting an answer: the request's rpc id
// (for the reply) and what the clients were shown.
type pendingElicit struct {
	rpcID json.RawMessage
	q     Elicitation
}

// add files a question; q.EID is assigned here. Idempotent by rpcID: a
// question still pending under the same (non-empty) request id keeps its
// eid (dup) — read again after a handoff, it is filed once.
func (e *elicits) add(rpcID json.RawMessage, q Elicitation) (eid string, dup bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pend == nil {
		e.pend = map[string]pendingElicit{}
	}
	if len(rpcID) > 0 {
		for _, m := range []map[string]pendingElicit{e.pend, e.urls} {
			for _, p := range m {
				if idKey(p.rpcID) == idKey(rpcID) {
					return p.q.EID, true
				}
			}
		}
	}
	e.next++
	q.EID = "e" + strconv.Itoa(e.next)
	e.pend[q.EID] = pendingElicit{rpcID: rpcID, q: q}
	return q.EID, false
}

func (e *elicits) take(eid string) (pendingElicit, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p, ok := e.pend[eid]
	delete(e.pend, eid)
	return p, ok
}

// accepted remembers an accepted url question until its completion.
func (e *elicits) accepted(p pendingElicit) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.urls == nil {
		e.urls = map[string]pendingElicit{}
	}
	e.urls[p.q.ElicitationID] = p
}

// complete takes the url question an elicitation/complete names: still
// unanswered (open), or accepted.
func (e *elicits) complete(elicitationID string) (p pendingElicit, open, ok bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, ok := e.urls[elicitationID]; ok {
		delete(e.urls, elicitationID)
		return p, false, true
	}
	for eid, p := range e.pend {
		if p.q.Mode == "url" && p.q.ElicitationID == elicitationID {
			delete(e.pend, eid)
			return p, true, true
		}
	}
	return pendingElicit{}, false, false
}

// list is the pending questions, oldest first.
func (e *elicits) list() []Elicitation {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Elicitation, 0, len(e.pend))
	for _, p := range e.pend {
		out = append(out, p.q)
	}
	sort.Slice(out, func(i, j int) bool { return eidNum(out[i].EID) < eidNum(out[j].EID) })
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
		URL             string          `json:"url"`
		ElicitationID   string          `json:"elicitationId"`
	}
	if json.Unmarshal(m.Params, &p) != nil {
		return nil, &Error{Code: CodeInvalidParams, Message: "bad elicitation/create params"}
	}
	url := p.Mode == "url" && c.elicitURL()
	if p.Mode != "" && p.Mode != "form" && !url {
		return map[string]string{"action": "decline"}, nil // a mode we did not advertise
	}
	q := Elicitation{ToolCallID: p.ToolCallID, Message: p.Message, Schema: p.RequestedSchema}
	if url {
		q = Elicitation{Message: p.Message, Mode: "url", URL: p.URL, ElicitationID: p.ElicitationID}
	}
	w := c.wireOf(m)
	eid, dup := c.elicits.add(m.ID, q)
	if dup { // read again after a handoff: it is already waiting
		return nil, nil
	}
	q.EID = eid
	c.emitW(w, NewEvent(EvElicitRequest, q))
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	if !url || busy { // a url one outside a turn (a sign-in) holds no turn up
		c.setStatusW(w, StatusWaiting, "")
	}
	return nil, nil // answered by RespondElicitation
}

// elicitURL: url-mode questions were advertised (ClientOptions.Caps).
func (c *Client) elicitURL() bool {
	caps := c.opts.Caps
	return caps != nil && caps.Elicitation != nil && caps.Elicitation.URL != nil
}

// onElicitComplete is the agent's elicitation/complete: a url question's
// out-of-band step is done.
func (c *Client) onElicitComplete(w *Wire, m *Message) {
	var p ElicitCompleteParams
	if json.Unmarshal(m.Params, &p) != nil || p.ElicitationID == "" {
		return
	}
	pe, open, ok := c.elicits.complete(p.ElicitationID)
	if !ok {
		return
	}
	c.emitW(w, NewEvent(EvElicitResolved, map[string]any{"eid": pe.q.EID, "action": "complete", "by": "agent"}))
	if open { // completed before anyone answered: the request is moot
		_ = c.conn.Reply(pe.rpcID, map[string]any{"action": "cancel"}, nil)
	}
	c.afterAnswer(w)
}

// RespondElicitation answers a pending question: action accept (content =
// the form's values), decline or cancel.
func (c *Client) RespondElicitation(eid, action string, content json.RawMessage, by string) error {
	switch action {
	case "accept", "decline", "cancel":
	default:
		return errBadAction
	}
	pe, ok := c.elicits.take(eid)
	if !ok {
		return ErrNoElicitation
	}
	out := map[string]any{"action": action}
	res := map[string]any{"eid": eid, "action": action, "by": by}
	switch {
	case action == "accept" && pe.q.Mode == "url": // no content: the person opens the URL
		if pe.q.ElicitationID != "" {
			c.elicits.accepted(pe)
		}
	case action == "accept":
		if len(content) == 0 || string(content) == "null" {
			content = json.RawMessage(`{}`)
		}
		out["content"], res["content"] = content, content // the transcript shows what was answered
	}
	w := &Wire{RPCID: pe.rpcID}
	// logged before the agent hears it, like a permission's resolution
	c.emitW(w, NewEvent(EvElicitResolved, res))
	err := c.conn.Reply(pe.rpcID, out, nil)
	c.afterAnswer(w)
	return err
}

// afterAnswer is the status once nothing waits for an answer any more: a
// turn runs on.
func (c *Client) afterAnswer(w *Wire) {
	c.mu.Lock()
	busy, st := c.busy, c.status
	c.mu.Unlock()
	if busy && st != StatusCancelling && c.cfg.Perms.Count() == 0 && c.elicits.count() == 0 {
		c.setStatusW(w, StatusRunning, "")
	}
}

// PendingElicitations is the questions still waiting for an answer, oldest
// first (GET /term/sessions/<id> lists them beside the permissions).
func (c *Client) PendingElicitations() []Elicitation { return c.elicits.list() }

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
