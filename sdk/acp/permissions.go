package acp

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	Name     string          `json:"name,omitempty"` // the programmatic tool name, when the adapter says (ExitPlanMode, Bash, …)
	Title    string          `json:"title,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	RawInput json.RawMessage `json:"rawInput,omitempty"`
	Content  json.RawMessage `json:"content,omitempty"`
}

// KindSwitchMode is the ACP tool kind of a mode switch — Claude's and
// Codex's plan approval. Its "allow_always" options mean "approve AND raise
// the permission mode" (the ACP spec's own example), never "remember this
// answer": a switch_mode request is never scoped to a session rule, and a
// rule never answers one (otherwise the next plan is approved unseen — with
// whichever allow_always option comes first, e.g. "clear context").
const KindSwitchMode = "switch_mode"

// Pending is a permission request the agent is waiting on.
type Pending struct {
	PID      string             `json:"pid"`
	ToolCall ToolCallRef        `json:"toolCall"`
	Options  []PermissionOption `json:"options"`
	rpcID    json.RawMessage    // the agent's request id, for the reply
}

// RPCID is the agent's request id the answer goes to — what an embedder
// keeps beside a Pending to Restore it in a later process.
func (p Pending) RPCID() json.RawMessage { return p.rpcID }

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
// session" rules. First answer wins; a second answer is an error. One per
// agent process: a request id names a request only within it.
type Permissions struct {
	mu      sync.Mutex
	next    int
	pending map[string]*Pending
	rules   []Rule // auto-allow, in the order they were granted
}

// Rule is an "allow for the session" answer: a later request whose call
// matches (the same kind, and title when the rule has one) is allowed
// without asking. Rules and SetRules carry them across processes.
type Rule struct {
	Kind  string `json:"kind,omitempty"`
	Title string `json:"title,omitempty"`
}

func NewPermissions() *Permissions { return &Permissions{pending: map[string]*Pending{}} }

// Request registers a new pending request and returns it. If a rule
// matches, it is resolved at once and the resolution is returned too
// (the caller replies to the agent and logs both events). Idempotent by
// rpcID: a request still pending under the same (non-empty) id is
// returned as it is — a frame read again after a handoff files nothing
// twice.
func (p *Permissions) Request(tc ToolCallRef, options []PermissionOption, rpcID json.RawMessage) (*Pending, *Resolution) {
	pd, res, _ := p.file(tc, options, rpcID)
	return pd, res
}

// file is Request, and whether the request was already pending (dup).
func (p *Permissions) file(tc ToolCallRef, options []PermissionOption, rpcID json.RawMessage) (*Pending, *Resolution, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pd := p.byRPC(rpcID); pd != nil {
		return pd, nil, true
	}
	p.next++
	pd := &Pending{PID: "p" + strconv.Itoa(p.next), ToolCall: tc, Options: append([]PermissionOption(nil), options...), rpcID: rpcID}
	for _, r := range p.rules {
		if tc.Kind != KindSwitchMode && r.matches(tc) {
			if o := optionOfKind(options, AllowAlways, AllowOnce); o != nil {
				return pd, &Resolution{PID: pd.PID, OptionID: o.OptionID, By: "auto", RPCID: rpcID}, false
			}
		}
	}
	p.pending[pd.PID] = pd
	return pd, nil, false
}

// byRPC is the pending request under a non-empty rpc id (caller holds mu).
func (p *Permissions) byRPC(rpcID json.RawMessage) *Pending {
	if len(rpcID) == 0 {
		return nil
	}
	key := idKey(rpcID)
	for _, pd := range p.pending {
		if len(pd.rpcID) > 0 && idKey(pd.rpcID) == key {
			return pd
		}
	}
	return nil
}

// Restore files a request an earlier process held (as List gave it, with
// its RPCID) under its own pid, so it can be answered here; later pids
// never reuse it. A request already pending under that pid or rpc id is
// left as it is.
func (p *Permissions) Restore(pd Pending, rpcID json.RawMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pending[pd.PID] != nil || p.byRPC(rpcID) != nil {
		return
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(pd.PID, "p")); err == nil && n > p.next {
		p.next = n
	}
	pd.Options = append([]PermissionOption(nil), pd.Options...)
	pd.rpcID = append(json.RawMessage(nil), rpcID...)
	p.pending[pd.PID] = &pd
}

// Rules is the session's "allow for the session" rules, oldest first.
func (p *Permissions) Rules() []Rule {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Rule(nil), p.rules...)
}

// SetRules replaces them (a later process continuing the session). A rule
// with neither kind nor title would allow everything: it is dropped.
func (p *Permissions) SetRules(rules []Rule) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rules = nil
	for _, r := range rules {
		if r.Kind != "" || r.Title != "" {
			p.rules = append(p.rules, r)
		}
	}
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
	// A session rule is recorded only when the agent's allow_always option
	// was actually chosen (a fallback to allow_once must not remember
	// anything the agent did not grant) and only when the call has a kind or
	// a title — with neither, rule.matches would be a wildcard: allow
	// everything for the session.
	if opt.Kind == AllowAlways && pd.ToolCall.Rule() {
		p.rules = append(p.rules, Rule{Kind: pd.ToolCall.Kind, Title: pd.ToolCall.Title})
	}
	return &Resolution{PID: pid, OptionID: opt.OptionID, By: by, RPCID: pd.rpcID}, nil
}

// Rule reports whether "allow for the session" can be scoped to this call
// (it has a kind or a title to match on). The clients hide the option
// otherwise. Never for a mode switch (KindSwitchMode).
func (t ToolCallRef) Rule() bool { return t.Kind != KindSwitchMode && (t.Kind != "" || t.Title != "") }

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
		if idKey(pd.rpcID) == idKey(rpcID) {
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

func (r Rule) matches(tc ToolCallRef) bool {
	if r.Kind != "" && r.Kind != tc.Kind {
		return false
	}
	return r.Title == "" || r.Title == tc.Title
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
