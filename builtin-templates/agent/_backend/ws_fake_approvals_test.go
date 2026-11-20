package main

// The fake xbind's share of D250 (one approval, one place): the person's
// pick on a start's approval is what opens — the coding agent, model and
// mode, or the session it continues; a start can fail to open, and its
// person tries it again; a message to a session that acts without asking
// is a request of its own, sent under its ticket.

import (
	"context"
	"encoding/json"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type fakeApprovals struct {
	failNext bool                    // the next ticketed start fails to open, retryable (the sandbox didn't start)
	tiles    map[string]*wsChosenNow // the person's own choice per tile, as start-defaults' chosen says it
}

// chosenFor is start-defaults' chosen for tile: the person's own choice
// remembered there, else the default.
func (a *fakeApprovals) chosenFor(tile, def string) *wsChosenNow {
	if c := a.tiles[tile]; c != nil {
		cc := *c
		return &cc
	}
	return &wsChosenNow{Harness: def, Mode: "default", Asks: true, From: map[string]string{"harness": "default", "mode": "default", "model": "default"}}
}

// remember is the person's "Remember for this tile".
func (f *fakeXbin) remember(tile string, c wsChosenNow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.apx.tiles == nil {
		f.apx.tiles = map[string]*wsChosenNow{}
	}
	c.From = map[string]string{"harness": "tile", "mode": "tile", "model": "tile"}
	f.apx.tiles[tile] = &c
}

// decideWith is the person's approval of a start with their pick on the
// sheet (D250): what opens.
func (f *fakeXbin) decideWith(id string, c wsChosen) {
	f.decide(id, "approved")
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.requests[id]; r != nil {
		cc := c
		r.Chosen = &cc
	}
}

// retry is the person's Try again on a start that failed to open.
func (f *fakeXbin) retry(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r := f.requests[id]; r != nil && r.State == "failed" {
		r.State, r.used, r.Failure = "approved", false, nil
	}
}

// ticketedStart is AgentStart under a ticket whose approval picked
// something, or that fails: done reports it answered. (f.mu held.)
func (f *fakeXbin) ticketedStart(sid string, a wsAgentStart, ticket string) (wsAgent, error, bool) {
	r := f.requests[ticket]
	if ticket == "" || r == nil || r.State != "used" { // (f.call spent it just before)
		return wsAgent{}, nil, false
	}
	if f.apx.failNext {
		f.apx.failNext = false
		r.State, r.Failure = "failed", &wsFailure{Why: "the tile's sandbox didn't start", Retry: true}
		return wsAgent{}, &xbin.DelegateError{Status: 503, Refusal: "start-failed", Retry: true,
			Message: "approved by the person, but the tile agent didn't start: the tile's sandbox didn't start — it is not the agent's doing: the person can try it again from the card"}, true
	}
	c := r.Chosen
	if c == nil {
		return wsAgent{}, nil, false
	}
	if c.Continue != "" {
		ag := f.agents[c.Continue]
		if ag == nil {
			return wsAgent{}, &xbin.DelegateError{Status: 403, Refusal: "ticket", Reason: "session", Message: "the session your person chose to continue ended"}, true
		}
		ag.Status, ag.Continued = "running", true
		f.said[ag.ID] = append(f.said[ag.ID], a.Prompt)
		f.note("AgentContinue %s", ag.ID)
		out := *ag
		ag.Continued = false
		return out, nil, true
	}
	name := c.Provider
	for _, h := range f.harnesses {
		if h.ID == c.Provider {
			name = h.Name
		}
	}
	asks := c.Mode == "default" || c.Mode == "plan" || c.Mode == "read-only"
	ag := &wsAgent{ID: f.id("s"), Tile: a.Tile, Provider: c.Provider, Name: name, Mode: c.Mode, Asks: asks, Model: c.Model, Status: "running", Origin: "agent", Mine: true, Turn: 1}
	f.agents[ag.ID] = ag
	f.said[ag.ID] = append(f.said[ag.ID], a.Prompt)
	f.note("AgentStart %s picked=%s/%s/%s", ag.ID, c.Provider, c.Mode, c.Model)
	return *ag, nil, true
}

func (f *fakeXbin) AskMessage(_ context.Context, sid, id, text, summary string) (wsRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := f.agents[id]
	if a == nil || !a.Mine {
		return wsRequest{}, &xbin.DelegateError{Status: 403, Message: "a workspace agent drives the sessions it started"}
	}
	r, err := f.ask(sid, messageCall(id, text), "message", summary)
	if err == nil {
		f.requests[r.ID].Tile = a.Tile
		f.requests[r.ID].Preview = map[string]any{"message": map[string]any{"session": id, "provider": a.Provider, "mode": a.Mode, "text": text}}
	}
	return r, err
}

func (f *fakeXbin) AgentSayTicket(_ context.Context, sid, id, text, ticket string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests[ticket]
	c := messageCall(id, text)
	switch {
	case r == nil || r.State != "approved" || r.used:
		return "", &xbin.DelegateError{Status: 403, Refusal: "ticket", Message: "the ticket isn't approved, or was used"}
	case r.call.Path != c.Path || string(r.call.Body) != string(c.Body):
		return "", &xbin.DelegateError{Status: 403, Refusal: "ticket", Reason: "bound", Message: "the message's text changed after approval"}
	}
	r.used, r.State, r.Decided = true, "used", time.Now().Unix()
	f.note("AgentSayTicket %s", id)
	f.said[id] = append(f.said[id], text)
	if a := f.agents[id]; a != nil {
		a.Status = "running"
	}
	return "prompted", nil
}

// startBody is the start a request was filed for, as the backend sent it.
func (r *fakeRequest) startBody() wsAgentStart {
	var a wsAgentStart
	_ = json.Unmarshal(r.call.Body, &a)
	return a
}
