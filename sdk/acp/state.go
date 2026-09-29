package acp

// Taking over a live session (ClientOptions.Attach): an embedder that
// keeps an agent running across its own restarts — a process handing off
// to its successor mid-turn — saves State() with the agent's output offset
// (Event.Wire.Off), and the successor's Client rebuilds from it without a
// handshake: the agent never sees a new initialize, its in-flight prompt's
// answer still ends the turn, and the requests it waits on can still be
// answered.

import (
	"encoding/json"
	"io"
	"sort"
	"strconv"
)

// SessionState is what a later process needs to take over a live session:
// JSON-serializable, taken with State, given back as ClientOptions.Attach.
//
// State is the client's state now: it may already reflect frames after the
// offset an embedder commits it with. Everything in it converges when those
// frames are read again (the updates are last-writer-wins, a permission or
// a question already filed is not filed twice) — except a prompt that ended
// meanwhile. An embedder whose own record says a prompt is still in flight
// (it has not seen that turn's end) sets PromptRPC and Turn from that
// record before attaching, so the answer, read again, ends the turn.
type SessionState struct {
	SessionID   string             `json:"sessionId"`
	Loadable    bool               `json:"loadable,omitempty"` // session/load can reopen it
	Steering    bool               `json:"steering,omitempty"` // the agent takes _session/steering (Steer)
	PromptCaps  PromptCapabilities `json:"promptCaps"`
	Modes       *SessionModes      `json:"modes,omitempty"`
	Options     []ConfigOption     `json:"options,omitempty"`
	Commands    []Command          `json:"commands,omitempty"`
	AgentInfo   *Info              `json:"agentInfo,omitempty"`
	AuthMethods []AuthMethod       `json:"authMethods,omitempty"`
	AuthNeeded  bool               `json:"authNeeded,omitempty"` // the agent said it is signed out
	Usage       *UsageUpdate       `json:"usage,omitempty"`
	// Tools is this turn's tool calls, id → last status (a cancel marks the
	// unfinished ones).
	Tools map[string]string `json:"tools,omitempty"`
	Turn  uint64            `json:"turn"` // the last turn's number (turn.end's turn)
	// PromptRPC is the in-flight session/prompt's request id: the turn
	// running (nil: none).
	PromptRPC json.RawMessage `json:"promptRpc,omitempty"`
	// Elicitations is the questions waiting for an answer, and url ones
	// accepted and waiting for the agent's elicitation/complete.
	Elicitations []ElicitationState `json:"elicitations,omitempty"`
	ElicitNext   int                `json:"elicitNext,omitempty"` // the last eid number given
}

// ElicitationState is one question of SessionState: what the clients were
// shown, the agent's request id the answer goes to, and whether it is a
// url one already accepted (waiting for elicitation/complete).
type ElicitationState struct {
	Elicitation
	RPCID    json.RawMessage `json:"rpcId,omitempty"`
	Accepted bool            `json:"accepted,omitempty"`
}

// State is the session's state now (see SessionState).
func (c *Client) State() SessionState {
	c.mu.Lock()
	st := SessionState{SessionID: c.sessionID, Loadable: c.loadable, Steering: c.steering, PromptCaps: c.promptCaps,
		Options: append([]ConfigOption(nil), c.options...), Commands: append([]Command(nil), c.commands...),
		AuthMethods: append([]AuthMethod(nil), c.authMethods...), AuthNeeded: c.authNeeded, Turn: c.turn,
		PromptRPC: append(json.RawMessage(nil), c.promptRPC...)}
	if c.modes != nil {
		m := *c.modes
		m.AvailableModes = append([]ModeEntry(nil), m.AvailableModes...)
		st.Modes = &m
	}
	if c.agentInfo != nil {
		a := *c.agentInfo
		st.AgentInfo = &a
	}
	if c.usage != nil {
		u := *c.usage
		st.Usage = &u
	}
	if len(c.tools) > 0 {
		st.Tools = make(map[string]string, len(c.tools))
		for k, v := range c.tools {
			st.Tools[k] = v
		}
	}
	if len(st.PromptRPC) == 0 {
		st.PromptRPC = nil
	}
	c.mu.Unlock()
	st.Elicitations, st.ElicitNext = c.elicits.state()
	return st
}

// restore rebuilds the client from an earlier process's state and adopts
// its in-flight prompt: the answer, when it comes, ends the turn. Runs in
// Start before the read loop.
func (c *Client) restore(st SessionState) {
	c.mu.Lock()
	c.sessionID, c.loadable, c.steering, c.promptCaps = st.SessionID, st.Loadable, st.Steering, st.PromptCaps
	c.modes, c.options, c.commands, c.agentInfo = st.Modes, st.Options, st.Commands, st.AgentInfo
	c.authMethods, c.authNeeded, c.usage, c.turn = st.AuthMethods, st.AuthNeeded, st.Usage, st.Turn
	c.initialized = true
	c.tools = map[string]string{}
	for k, v := range st.Tools {
		c.tools[k] = v
	}
	var ch <-chan *Message
	id := st.PromptRPC
	if len(id) > 0 {
		c.busy, c.promptRPC = true, id
		ch = c.conn.Expect(id)
	}
	turn := st.Turn
	c.mu.Unlock()
	c.elicits.restore(st.Elicitations, st.ElicitNext)
	if ch != nil {
		go func() {
			resp, ok := <-ch
			var err error
			switch {
			case !ok || resp == nil:
				err = io.ErrClosedPipe
			case resp.Error != nil:
				err = resp.Error
			}
			c.endTurn(turn, id, resp, err)
		}()
	}
}

// attachedStatus is the status a taken-over session starts in.
func (c *Client) attachedStatus() string {
	c.mu.Lock()
	busy := c.busy
	c.mu.Unlock()
	switch {
	case !busy:
		return StatusIdle
	case c.cfg.Perms.Count() > 0 || c.elicits.count() > 0:
		return StatusWaiting
	}
	return StatusRunning
}

// state is the elicitations for SessionState, oldest first.
func (e *elicits) state() ([]ElicitationState, int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []ElicitationState
	for _, p := range e.pend {
		out = append(out, ElicitationState{Elicitation: p.q, RPCID: p.rpcID})
	}
	for _, p := range e.urls {
		out = append(out, ElicitationState{Elicitation: p.q, RPCID: p.rpcID, Accepted: true})
	}
	sort.Slice(out, func(i, j int) bool { return eidNum(out[i].EID) < eidNum(out[j].EID) })
	return out, e.next
}

// restore files an earlier process's elicitations (restore, before the
// read loop).
func (e *elicits) restore(list []ElicitationState, next int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.pend == nil {
		e.pend = map[string]pendingElicit{}
	}
	if e.urls == nil {
		e.urls = map[string]pendingElicit{}
	}
	for _, s := range list {
		p := pendingElicit{rpcID: s.RPCID, q: s.Elicitation}
		if s.Accepted {
			e.urls[s.ElicitationID] = p
		} else {
			e.pend[s.EID] = p
		}
		if n := eidNum(s.EID); n > next {
			next = n
		}
	}
	if next > e.next {
		e.next = next
	}
}

func eidNum(eid string) int {
	if len(eid) < 2 {
		return 0
	}
	n, _ := strconv.Atoi(eid[1:])
	return n
}
