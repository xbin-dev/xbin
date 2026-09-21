package agent

import (
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
)

// PermissionOption is one way to answer a request (ACP: optionId + kind).
type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"` // allow_once | allow_always | reject_once | reject_always
}

// Permission option kinds.
const (
	AllowOnce    = "allow_once"
	AllowAlways  = "allow_always"
	RejectOnce   = "reject_once"
	RejectAlways = "reject_always"
)

// ToolCallRef is what a permission request is about (ACP ToolCallUpdate,
// the fields worth showing): only ID is guaranteed.
type ToolCallRef struct {
	ID       string          `json:"id"`
	Title    string          `json:"title,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	RawInput json.RawMessage `json:"rawInput,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}

// Pending is a permission request the agent is waiting on.
type Pending struct {
	PID      string             `json:"pid"`
	ToolCall ToolCallRef        `json:"toolCall"`
	Options  []PermissionOption `json:"options"`
	rpcID    json.RawMessage    // the agent's request id, for the reply
}

// Resolution is a settled request: the option chosen and by whom
// ("user:<id>", "auto", "cancel").
type Resolution struct {
	PID      string `json:"pid"`
	OptionID string `json:"optionId,omitempty"`
	By       string `json:"by"`
	RPCID    json.RawMessage
	Cancel   bool // answer the agent with the cancelled outcome, not a selection
}

// Permissions holds a session's pending requests and its "allow for
// session" rules. First answer wins; a second answer is an error.
type Permissions struct {
	mu      sync.Mutex
	next    int
	pending map[string]*Pending
	rules   []rule // auto-allow, in the order they were granted
}

type rule struct{ kind, title string }

func NewPermissions() *Permissions { return &Permissions{pending: map[string]*Pending{}} }

// Request registers a new pending request and returns it. If a rule
// matches, it is resolved at once and the resolution is returned too
// (the caller replies to the agent and logs both events).
func (p *Permissions) Request(tc ToolCallRef, options []PermissionOption, rpcID json.RawMessage) (*Pending, *Resolution) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.next++
	pd := &Pending{PID: "p" + strconv.Itoa(p.next), ToolCall: tc, Options: append([]PermissionOption(nil), options...), rpcID: rpcID}
	for _, r := range p.rules {
		if r.matches(tc) {
			if o := optionOfKind(options, AllowAlways, AllowOnce); o != nil {
				return pd, &Resolution{PID: pd.PID, OptionID: o.OptionID, By: "auto", RPCID: rpcID}
			}
		}
	}
	p.pending[pd.PID] = pd
	return pd, nil
}

// Resolve answers pending request pid with an option id, or with a
// decision (an option kind — the first option of that kind is chosen). An
// allow_always answer records the rule. by names the answerer.
func (p *Permissions) Resolve(pid, optionID, decision, by string) (*Resolution, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pd := p.pending[pid]
	if pd == nil {
		return nil, fmt.Errorf("no pending permission %q (already answered, or never asked)", pid)
	}
	var opt *PermissionOption
	if optionID != "" {
		for i := range pd.Options {
			if pd.Options[i].OptionID == optionID {
				opt = &pd.Options[i]
			}
		}
	} else if decision != "" {
		opt = optionOfKind(pd.Options, decision)
		if opt == nil && decision == AllowAlways {
			opt = optionOfKind(pd.Options, AllowOnce) // the agent offered no "always": allow once, remember anyway
		}
	}
	if opt == nil {
		return nil, fmt.Errorf("permission %s has no option %q", pid, optionID+decision)
	}
	delete(p.pending, pid)
	if opt.Kind == AllowAlways || decision == AllowAlways {
		p.rules = append(p.rules, rule{kind: pd.ToolCall.Kind, title: pd.ToolCall.Title})
	}
	return &Resolution{PID: pid, OptionID: opt.OptionID, By: by, RPCID: pd.rpcID}, nil
}

// CancelAll settles every pending request as cancelled (a turn cancel).
func (p *Permissions) CancelAll() []*Resolution {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []*Resolution{}
	for pid, pd := range p.pending {
		out = append(out, &Resolution{PID: pid, By: "cancel", RPCID: pd.rpcID, Cancel: true})
		delete(p.pending, pid)
	}
	return out
}

// CancelByRPC settles the request the agent itself withdrew ($/cancel_request).
func (p *Permissions) CancelByRPC(rpcID json.RawMessage) *Resolution {
	p.mu.Lock()
	defer p.mu.Unlock()
	for pid, pd := range p.pending {
		if string(pd.rpcID) == string(rpcID) {
			delete(p.pending, pid)
			return &Resolution{PID: pid, By: "cancel", RPCID: pd.rpcID, Cancel: true}
		}
	}
	return nil
}

// List is the pending requests, oldest first.
func (p *Permissions) List() []Pending {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]Pending, 0, len(p.pending))
	for _, pd := range p.pending {
		out = append(out, *pd)
	}
	sortPending(out)
	return out
}

// Count is how many requests wait.
func (p *Permissions) Count() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.pending) }

func (r rule) matches(tc ToolCallRef) bool {
	if r.kind != "" && r.kind != tc.Kind {
		return false
	}
	return r.title == "" || r.title == tc.Title
}

func optionOfKind(opts []PermissionOption, kinds ...string) *PermissionOption {
	for _, k := range kinds {
		for i := range opts {
			if opts[i].Kind == k {
				return &opts[i]
			}
		}
	}
	return nil
}

func sortPending(ps []Pending) {
	num := func(pid string) int { n, _ := strconv.Atoi(pid[1:]); return n }
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && num(ps[j-1].PID) > num(ps[j].PID); j-- {
			ps[j-1], ps[j] = ps[j], ps[j-1]
		}
	}
}
