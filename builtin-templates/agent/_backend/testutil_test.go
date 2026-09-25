package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// newTestAgent is an Agent with a live engine that already owns its (in-memory)
// database, driven by a scripted fake model. Tests act through the same entry
// points production uses — handlers, the inbox, Poke — and wait for state.
func newTestAgent(t *testing.T, db *DB) *Agent {
	t.Helper()
	ag := &Agent{db: db, repl: newReplRegistry(), toolSem: make(chan struct{}, maxToolsGlobal),
		blobs: newMemBlobs(), blobCache: newBlobCache(8 << 20), noGateway: true}
	f := &fakeLLM{t: t, gates: map[string]chan struct{}{}}
	e := newEngine(db, ag, f, "")
	e.takeOver()
	t.Cleanup(func() { e.Shutdown(2 * time.Second) })
	return ag
}

func fakeOf(ag *Agent) *fakeLLM { return ag.eng.llm.(*fakeLLM) }

// --- the scripted model ------------------------------------------------------

type fakeRule struct {
	match  func(req LLMRequest) bool
	reply  func(req LLMRequest) (LLMReply, error)
	events []LLMEvent
	gate   string // block until released (or the call's context ends)
	times  int    // 0 = unlimited
	used   int
}

type fakeLLM struct {
	t     *testing.T
	mu    sync.Mutex
	rules []*fakeRule
	calls []LLMRequest
	gates map[string]chan struct{}
	// inflight counts calls currently inside Chat (for the gate tests).
	inflight int
}

// on adds a rule; the first matching, unexhausted rule answers a call. With
// no match the model says "ok".
func (f *fakeLLM) on(match func(LLMRequest) bool, reply func(LLMRequest) (LLMReply, error)) *fakeRule {
	r := &fakeRule{match: match, reply: reply}
	f.mu.Lock()
	f.rules = append(f.rules, r)
	f.mu.Unlock()
	return r
}

func (r *fakeRule) once() *fakeRule             { r.times = 1; return r }
func (r *fakeRule) block(gate string) *fakeRule { r.gate = gate; return r }
func (r *fakeRule) emit(evs ...LLMEvent) *fakeRule {
	r.events = append(r.events, evs...)
	return r
}

func (f *fakeLLM) gate(name string) chan struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.gates[name]
	if g == nil {
		g = make(chan struct{})
		f.gates[name] = g
	}
	return g
}

func (f *fakeLLM) release(name string) { close(f.gate(name)) }

func (f *fakeLLM) Chat(ctx context.Context, req LLMRequest, onEvent func(LLMEvent)) (LLMReply, error) {
	if err := validateWire(req.Msgs); err != nil {
		f.t.Errorf("run #%d sent an invalid transcript: %v", req.Run, err)
	}
	f.mu.Lock()
	f.calls = append(f.calls, req)
	var rule *fakeRule
	for _, r := range f.rules {
		if (r.times == 0 || r.used < r.times) && (r.match == nil || r.match(req)) {
			r.used++
			rule = r
			break
		}
	}
	f.inflight++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	if rule == nil {
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: "ok"}, Model: req.Model, Wire: "chat"}, nil
	}
	for _, ev := range rule.events {
		if onEvent != nil {
			onEvent(ev)
		}
	}
	if rule.gate != "" {
		select {
		case <-f.gate(rule.gate):
		case <-ctx.Done():
			return LLMReply{}, context.Cause(ctx)
		}
	}
	if rule.reply == nil {
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: "ok"}, Model: req.Model, Wire: "chat"}, nil
	}
	rep, err := rule.reply(req)
	if rep.Model == "" {
		rep.Model = req.Model
	}
	return rep, err
}

func (f *fakeLLM) callsFor(run int64) []LLMRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []LLMRequest
	for _, c := range f.calls {
		if c.Run == run {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeLLM) inFlight() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.inflight
}

// --- matchers and replies ---------------------------------------------------------

// lastUser matches a turn whose last user message contains s.
func lastUser(s string) func(LLMRequest) bool {
	return func(r LLMRequest) bool {
		for i := len(r.Msgs) - 1; i >= 0; i-- {
			if r.Msgs[i].Role == "user" {
				return strings.Contains(asString(r.Msgs[i].Content), s)
			}
		}
		return false
	}
}

// lastIs matches a turn whose last message has this role (and contains s).
func lastIs(role, s string) func(LLMRequest) bool {
	return func(r LLMRequest) bool {
		if len(r.Msgs) == 0 {
			return false
		}
		m := r.Msgs[len(r.Msgs)-1]
		return m.Role == role && strings.Contains(asString(m.Content), s)
	}
}

func forRun(id int64, m func(LLMRequest) bool) func(LLMRequest) bool {
	return func(r LLMRequest) bool { return r.Run == id && (m == nil || m(r)) }
}

func isSubagent(r LLMRequest) bool {
	return len(r.Msgs) > 0 && strings.Contains(asString(r.Msgs[0].Content), "You are a subagent")
}

func say(text string) func(LLMRequest) (LLMReply, error) {
	return func(LLMRequest) (LLMReply, error) {
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: text}, Wire: "chat"}, nil
	}
}

func fail(msg string) func(LLMRequest) (LLMReply, error) {
	return func(LLMRequest) (LLMReply, error) { return LLMReply{}, fmt.Errorf("%s", msg) }
}

// tc builds a tool call; args is JSON.
func tc(id, name, args string) toolCall {
	c := toolCall{ID: id, Type: "function"}
	c.Function.Name = name
	c.Function.Arguments = args
	return c
}

func callTools(calls ...toolCall) func(LLMRequest) (LLMReply, error) {
	return func(LLMRequest) (LLMReply, error) {
		return LLMReply{Msg: wireMsg{Role: "assistant", ToolCalls: calls}, Wire: "chat"}, nil
	}
}

// --- driving and waiting ---------------------------------------------------------

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func statusOf(db *DB, id int64) string {
	r, err := db.getRun(id)
	if err != nil {
		return "gone"
	}
	return r.Status
}

func waitStatus(t *testing.T, db *DB, id int64, want string) {
	t.Helper()
	waitFor(t, fmt.Sprintf("run #%d to be %s (is %s)", id, want, statusOf(db, id)), func() bool { return statusOf(db, id) == want })
}

// idleWithEngine waits until no actor is working on anything.
func waitQuiet(t *testing.T, ag *Agent) {
	t.Helper()
	waitFor(t, "the engine to go quiet", func() bool {
		ag.eng.mu.Lock()
		defer ag.eng.mu.Unlock()
		return len(ag.eng.actors) == 0
	})
}

// newRun starts a top-level run with a first message, like POST /runs.
func newRun(t *testing.T, ag *Agent, cfg Config, text string) int64 {
	t.Helper()
	if cfg.System == "" {
		cfg.System = "test agent"
	}
	cfg.Features = mergeFeatures(cfg.Features, map[string]bool{"streaming": false})
	r, err := ag.startRun(clip(text, 40), "", cfg, text, false, "")
	if err != nil {
		t.Fatal(err)
	}
	return r.ID
}

func mergeFeatures(a, b map[string]bool) map[string]bool {
	out := map[string]bool{}
	for k, v := range b {
		out[k] = v
	}
	for k, v := range a {
		out[k] = v
	}
	return out
}

// send queues a human message, like POST /runs/{id}/message.
func send(t *testing.T, ag *Agent, id int64, text string) int64 {
	t.Helper()
	iid, _, err := ag.queue(id, inboxUser, inboxBody{Text: text, Source: "human"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return iid
}

func transcript(db *DB, id int64) string {
	msgs, _ := db.messages(id, true)
	var b strings.Builder
	for _, m := range msgs {
		switch m.Role {
		case "assistant":
			fmt.Fprintf(&b, "A:%s", m.Content)
			if m.ToolCalls != "" {
				var calls []toolCall
				_ = json.Unmarshal([]byte(m.ToolCalls), &calls)
				for _, c := range calls {
					fmt.Fprintf(&b, "[%s]", c.Function.Name)
				}
			}
		case "tool":
			fmt.Fprintf(&b, "T:%s", clip(m.Content, 40))
		case "user":
			fmt.Fprintf(&b, "U:%s", clip(m.Content, 40))
		default:
			continue
		}
		b.WriteString(" | ")
	}
	return b.String()
}

// fullText is every message's content, unclipped.
func fullText(db *DB, id int64) string {
	msgs, _ := db.messages(id, false)
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}
