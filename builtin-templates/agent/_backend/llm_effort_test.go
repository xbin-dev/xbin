package main

// llm_effort_test.go — D249 in the agent: reasoning sent back to the model
// that produced it (against a provider that requires it, and one that
// refuses it), the level and when it is sent, the transcript's lines.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// newWireAgent is newTestAgent over the real model wire: the gateway LLM
// pinned to srv.
func newWireAgent(t *testing.T, db *DB, srv *httptest.Server) *Agent {
	t.Helper()
	fastWires(t)
	ag := &Agent{db: db, repl: newReplRegistry(), toolSem: make(chan struct{}, maxToolsGlobal),
		blobs: newMemBlobs(), blobCache: newBlobCache(8 << 20), noGateway: true}
	e := newEngine(db, ag, testGW(srv), "")
	e.takeOver()
	t.Cleanup(func() { e.Shutdown(2 * time.Second) })
	return ag
}

// thinkingProvider is a provider of a thinking model on the chat wire: every
// answer carries reasoning_content; the first answer to a request calls the
// note tool, the answer after a tool's result says so. Its rules, as a
// switch: requireReplay — DeepSeek's documented 400 when tools are sent and
// an earlier assistant message lacks its reasoning_content; refuseReplay —
// a strict server's 400 on the unknown member; overflow — a context error.
type thinkingProvider struct {
	mu            sync.Mutex
	requireReplay bool
	refuseReplay  bool
	refuseQuiet   bool // refuses the field in words that don't name it
	overflow      bool
	always400     bool
	bodies        []map[string]any
	raws          []string
}

func (p *thinkingProvider) set(f func(p *thinkingProvider)) { p.mu.Lock(); f(p); p.mu.Unlock() }

func (p *thinkingProvider) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		p.mu.Lock()
		p.bodies = append(p.bodies, body)
		p.raws = append(p.raws, string(raw))
		require, refuse, quiet, overflow, always := p.requireReplay, p.refuseReplay, p.refuseQuiet, p.overflow, p.always400
		p.mu.Unlock()
		msgs, _ := body["messages"].([]any)
		refusal := func(msg string) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
		}
		if overflow {
			refusal("This model's maximum context length is 1000 tokens.")
			return
		}
		if always {
			refusal("tools[0]: invalid schema")
			return
		}
		for i, m := range msgs {
			mm := m.(map[string]any)
			if mm["role"] != "assistant" {
				continue
			}
			_, has := mm["reasoning_content"]
			if require && body["tools"] != nil && !has {
				refusal(fmt.Sprintf("Missing `reasoning_content` field in the assistant message at message index %d. The reasoning_content in the thinking mode must be passed back to the API.", i))
				return
			}
			if refuse && has {
				refusal(fmt.Sprintf("messages.%d: property 'reasoning_content' is unsupported", i))
				return
			}
			if quiet && has {
				refusal("invalid request")
				return
			}
		}
		last := msgs[len(msgs)-1].(map[string]any)
		msg := map[string]any{"role": "assistant", "reasoning_content": "I will note it.", "content": nil,
			"tool_calls": []any{map[string]any{"id": fmt.Sprintf("call_%d", len(p.raws)), "type": "function", "function": map[string]any{"name": "note", "arguments": `{"text":"x"}`}}}}
		if last["role"] == "tool" {
			msg = map[string]any{"role": "assistant", "reasoning_content": "It is noted.", "content": "Noted it."}
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-XBin-LLM-Effort", "high")
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": len(raw) / 4, "completion_tokens": 10, "total_tokens": len(raw)/4 + 10}})
	})
}

func (p *thinkingProvider) calls() int { p.mu.Lock(); defer p.mu.Unlock(); return len(p.bodies) }

func (p *thinkingProvider) body(i int) map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.bodies[i]
}

func assistantsOf(body map[string]any) []map[string]any {
	var out []map[string]any
	msgs, _ := body["messages"].([]any)
	for _, m := range msgs {
		if mm := m.(map[string]any); mm["role"] == "assistant" {
			out = append(out, mm)
		}
	}
	return out
}

func runNotes(db *DB, id int64) string {
	var out []string
	rows, err := db.q.Query(`SELECT detail FROM steps WHERE run_id=? AND kind='note' ORDER BY seq`, id)
	if err != nil {
		return ""
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		_ = rows.Scan(&d)
		out = append(out, d)
	}
	return strings.Join(out, "\n")
}

// covers D249 (stage 0) — a provider that requires a thinking model's
// reasoning back once tools are in play (DeepSeek's documented 400) gets it,
// on every earlier answer, and a tool-using conversation goes on over turns.
// Without the replay its second call is refused.
func TestReasoningReplayWithToolsAsDeepSeekRequires(t *testing.T) {
	db := newTestDB(t)
	p := &thinkingProvider{requireReplay: true}
	srv := httptest.NewServer(p.handler(t))
	defer srv.Close()
	ag := newWireAgent(t, db, srv)
	id := newRun(t, ag, Config{Model: "deepseek-v4.1-flash", Wire: "chat"}, "use a tool")
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:use a tool | A:[note] | T:noted | A:Noted it. | " {
		t.Fatalf("transcript = %q (calls %d)", got, p.calls())
	}
	send(t, ag, id, "once more")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(db, id), "Noted it.") == 2 })
	last := p.body(p.calls() - 1)
	as := assistantsOf(last)
	if len(as) != 3 || as[0]["reasoning_content"] != "I will note it." || as[1]["reasoning_content"] != "It is noted." || as[2]["reasoning_content"] != "I will note it." {
		t.Fatalf("the last call's answers: %v", as)
	}
	// it costs input: the provider's prompt count (which carries it) is what compaction reads
	run, _ := db.getRun(id)
	if want := len(p.raws[len(p.raws)-1]) / 4; run.LastPromptTokens != want || !strings.Contains(p.raws[len(p.raws)-1], "It is noted.") {
		t.Errorf("the prompt count compaction reads: %d, want the provider's %d (reasoning included)", run.LastPromptTokens, want)
	}
}

// covers D249 (compat) — a provider that refuses the field (400 naming it)
// gets the call once more without it, the conversation says so in one
// line, and that conversation stops sending it — that conversation only:
// another learns for itself; a 400 about the context's size is not that,
// and strips nothing.
func TestReasoningReplayRefusedOnce(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	db := newTestDB(t)
	p := &thinkingProvider{refuseReplay: true}
	srv := httptest.NewServer(p.handler(t))
	defer srv.Close()
	ag := newWireAgent(t, db, srv)
	id := newRun(t, ag, Config{Model: "strict-thinker", Wire: "chat"}, "use a tool")
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:use a tool | A:[note] | T:noted | A:Noted it. | " {
		t.Fatalf("transcript = %q", got)
	}
	if p.calls() != 3 {
		t.Fatalf("calls: %d, want 3 (the answer, the refused call, the call without reasoning)", p.calls())
	}
	// the call made again is the refused one without the reasoning, and nothing else
	refused, again := p.body(1), p.body(2)
	for _, m := range refused["messages"].([]any) {
		delete(m.(map[string]any), "reasoning_content")
	}
	if fmt.Sprint(refused) != fmt.Sprint(again) {
		t.Errorf("the call made again differs by more than the reasoning:\n%v\n%v", refused, again)
	}
	if n := runNotes(db, id); !strings.Contains(n, "refused the reasoning sent back") || !strings.Contains(n, "strict-thinker") || !strings.Contains(n, "reasoning_content") ||
		strings.Contains(n, "I will note") {
		t.Errorf("the conversation's line: %q", n)
	}
	if !strings.Contains(logged.String(), "refused the reasoning sent back") || strings.Contains(logged.String(), "I will note") || strings.Contains(logged.String(), "It is noted") {
		t.Errorf("the log: %q", logged.String())
	}
	// remembered: the next turn's calls carry none, and are made once each
	before := p.calls()
	send(t, ag, id, "once more")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(db, id), "Noted it.") == 2 })
	if p.calls()-before != 2 {
		t.Errorf("the next turn made %d calls, want 2", p.calls()-before)
	}
	for i := before; i < p.calls(); i++ {
		if strings.Contains(p.raws[i], "reasoning_content") {
			t.Errorf("call %d carried reasoning after the refusal", i)
		}
	}
	// nothing global: another conversation on the same model sends its reasoning, and learns for itself
	before = p.calls()
	id3 := newRun(t, ag, Config{Model: "strict-thinker", Wire: "chat"}, "use a tool too")
	waitStatus(t, db, id3, statusIdle)
	if p.calls()-before != 3 || !strings.Contains(p.raws[before+1], "reasoning_content") {
		t.Errorf("another conversation made %d calls (want 3: it sends its reasoning, is refused, goes on without)", p.calls()-before)
	}

	// a context overflow is not a refusal of the field: one call, nothing remembered, the turn says it
	db2 := newTestDB(t)
	p2 := &thinkingProvider{requireReplay: true}
	srv2 := httptest.NewServer(p2.handler(t))
	defer srv2.Close()
	ag2 := newWireAgent(t, db2, srv2)
	id2 := newRun(t, ag2, Config{Model: "big-thinker", Wire: "chat"}, "use a tool")
	waitStatus(t, db2, id2, statusIdle)
	p2.mu.Lock()
	p2.overflow = true
	p2.mu.Unlock()
	n := p2.calls()
	send(t, ag2, id2, "once more")
	waitFor(t, "the failed turn", func() bool { return p2.calls() > n && statusOf(db2, id2) != statusRunning })
	time.Sleep(50 * time.Millisecond)
	if p2.calls()-n != 1 {
		t.Errorf("an overflow was sent %d times", p2.calls()-n)
	}
	if off := plainOffOf(ag2, id2); off != "" {
		t.Errorf("an overflow switched replay off: %q", off)
	}
	// a 400 for something else: made once more without the reasoning, which doesn't help — the turn fails on
	// the first refusal, and nothing is remembered (the field wasn't the trouble)
	p2.mu.Lock()
	p2.overflow, p2.always400 = false, true
	p2.mu.Unlock()
	n = p2.calls()
	send(t, ag2, id2, "and again")
	waitFor(t, "the failed turn", func() bool { return p2.calls() >= n+2 && statusOf(db2, id2) != statusRunning })
	time.Sleep(50 * time.Millisecond)
	if p2.calls()-n != 2 {
		t.Errorf("a refusal for something else was sent %d times, want 2", p2.calls()-n)
	}
	if off := plainOffOf(ag2, id2); off != "" {
		t.Errorf("a refusal for something else switched replay off: %q", off)
	}
}

// plainOffOf is what run id remembers of its provider refusing reasoning.
func plainOffOf(ag *Agent, id int64) string {
	req := LLMRequest{Run: id}
	ag.eng.replayOf(&req)
	return req.PlainOff
}

// covers D249 (H2) — a refusal that names no field it was sent, the call
// without the reasoning then working, is not remembered: the next turn
// sends it again (and is made again without it). A run whose replay is off
// gets it back once when a call without it is refused, the call with it
// then working; never twice.
func TestReplayMemoryIsTheRunsAndNamed(t *testing.T) {
	db := newTestDB(t)
	p := &thinkingProvider{refuseQuiet: true}
	srv := httptest.NewServer(p.handler(t))
	defer srv.Close()
	ag := newWireAgent(t, db, srv)
	id := newRun(t, ag, Config{Model: "quiet-thinker", Wire: "chat"}, "use a tool")
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:use a tool | A:[note] | T:noted | A:Noted it. | " || p.calls() != 3 {
		t.Fatalf("transcript = %q, calls %d", got, p.calls())
	}
	if off := plainOffOf(ag, id); off != "" || strings.Contains(runNotes(db, id), "refused") {
		t.Fatalf("a refusal naming no field was remembered: %q, notes %q", off, runNotes(db, id))
	}
	before := p.calls()
	send(t, ag, id, "once more")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(db, id), "Noted it.") == 2 })
	if !strings.Contains(p.raws[before], "reasoning_content") {
		t.Error("the next turn sent no reasoning: an unnamed refusal was remembered")
	}

	// remembered (the refusal names the field), then the provider requires it: the run brings it back, once
	db2 := newTestDB(t)
	p2 := &thinkingProvider{refuseReplay: true}
	srv2 := httptest.NewServer(p2.handler(t))
	defer srv2.Close()
	ag2 := newWireAgent(t, db2, srv2)
	id2 := newRun(t, ag2, Config{Model: "fickle", Wire: "chat"}, "use a tool")
	waitStatus(t, db2, id2, statusIdle)
	if off := plainOffOf(ag2, id2); off != "reasoning_content" {
		t.Fatalf("remembered %q", off)
	}
	p2.set(func(p *thinkingProvider) { p.refuseReplay, p.requireReplay = false, true })
	n := p2.calls()
	send(t, ag2, id2, "once more")
	waitFor(t, "the second turn", func() bool { return strings.Count(transcript(db2, id2), "Noted it.") == 2 })
	if off := plainOffOf(ag2, id2); off != "" || !strings.Contains(runNotes(db2, id2), "sent its earlier reasoning again") {
		t.Fatalf("not brought back: %q, notes %q", off, runNotes(db2, id2))
	}
	if !strings.Contains(p2.raws[p2.calls()-1], "reasoning_content") || p2.calls()-n != 3 {
		t.Errorf("calls %d, the last without its reasoning", p2.calls()-n)
	}
	// refused again: remembered again; and now a refusal without it is the turn's failure — no third flip
	p2.set(func(p *thinkingProvider) { p.refuseReplay, p.requireReplay = true, false })
	send(t, ag2, id2, "and again")
	waitFor(t, "the third turn", func() bool { return strings.Count(transcript(db2, id2), "Noted it.") == 3 })
	if off := plainOffOf(ag2, id2); off != "reasoning_content" {
		t.Fatalf("not remembered again: %q", off)
	}
	p2.set(func(p *thinkingProvider) { p.refuseReplay, p.requireReplay = false, true })
	n = p2.calls()
	send(t, ag2, id2, "and once more")
	waitFor(t, "the failed turn", func() bool { return p2.calls() > n && statusOf(db2, id2) != statusRunning })
	time.Sleep(50 * time.Millisecond)
	if p2.calls()-n != 1 || plainOffOf(ag2, id2) != "reasoning_content" {
		t.Errorf("brought back twice: %d calls, off %q", p2.calls()-n, plainOffOf(ag2, id2))
	}
}

// covers D249 (H2) — with the run's plain reasoning off, opaque blocks
// (OpenRouter's reasoning_details) still go back; a field the run doesn't
// remember still goes; namedField reads a field only as a word of its own.
func TestPlainOffKeepsOpaqueBlocks(t *testing.T) {
	via := "|m"
	msgs := []wireMsg{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", Replay: &msgMeta{Wire: "chat", Model: "m", Via: via, ReasoningRaw: json.RawMessage(`[{"type":"reasoning.encrypted","data":"x"}]`)}},
		{Role: "assistant", Content: "b", Replay: &msgMeta{Wire: "chat", Model: "m", Via: via, Reasoning: "plain", ReplayField: "reasoning_content"}},
		{Role: "assistant", Content: "c", Replay: &msgMeta{Wire: "chat", Model: "m", Via: via, Reasoning: "other", ReplayField: "reasoning"}},
	}
	for _, c := range []struct {
		off              string
		details, rc, rsn bool
	}{{"", true, true, true}, {"*", true, false, false}, {"reasoning_content", true, false, true}} {
		b, _ := chatCodec{}.encode(LLMRequest{Model: "m", Via: via, Msgs: msgs, PlainOff: c.off})
		s := string(b)
		if strings.Contains(s, "reasoning_details") != c.details || strings.Contains(s, `"reasoning_content":"plain"`) != c.rc || strings.Contains(s, `"reasoning":"other"`) != c.rsn {
			t.Errorf("off %q: %s", c.off, s)
		}
	}
	f := func(msg string) error { return &llmFailed{err: errors.New(msg), Status: 400} }
	for msg, want := range map[string]string{
		"messages.2: property 'reasoning_content' is unsupported": "reasoning_content",
		"Unknown field: reasoning":                                "reasoning",
		"reasoning_effort is not supported":                       "",
		"invalid request":                                         "",
		"xreasoning_contenty":                                     "",
	} {
		if got := namedField(f(msg), []string{"reasoning_content", "reasoning"}); got != want {
			t.Errorf("%q: %q, want %q", msg, got, want)
		}
	}
	if got := namedField(f("unknown field reasoning"), []string{"reasoning_content"}); got != "" {
		t.Errorf("a field it wasn't sent: %q", got)
	}
}

// replayRefused: a 400 the provider gave, and not about the context's size.
func TestReplayRefusedIsOnlyA400(t *testing.T) {
	f := func(status int, msg string) error {
		return &llmFailed{err: errors.New("llm-gw " + msg), Status: status}
	}
	for _, c := range []struct {
		err  error
		want bool
	}{
		{f(400, "unknown field reasoning_content"), true},
		{f(400, "This model's maximum context length is 8192"), false},
		{f(400, `{"code":"context_length_exceeded"}`), false},
		{f(422, "unknown field"), false},
		{f(500, "boom"), false},
		{f(429, "slow down"), false},
		{errors.New("400 but not a provider's"), false},
		{context.Canceled, false},
	} {
		if got := replayRefused(c.err); got != c.want {
			t.Errorf("%v: %v, want %v", c.err, got, c.want)
		}
	}
}

// covers D249 — the level a turn asks for, and that it goes only to a model
// that reasons: what the model's record says, else as before.
func TestEffortLevelAndWhenItIsSent(t *testing.T) {
	yes, no := &effortRec{Levels: []string{"low", "high", "max"}}, &effortRec{Levels: []string{}}
	for _, c := range []struct {
		name string
		cfg  Config
		want string
	}{
		{"the tile's default", Config{ReasoningEffort: "high"}, "high"},
		{"the conversation's pick", Config{ReasoningEffort: "high", Effort: "low"}, "low"},
		{"default: the model's own, over the tile's", Config{ReasoningEffort: "high", Effort: "default"}, ""},
		{"nothing", Config{}, ""},
		{"a workspace agent asks for none", Config{ReasoningEffort: "high", Effort: "max", Class: classWorkspace}, ""},
	} {
		if got := effortFor(c.cfg); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
	e := &Engine{effortFn: func(m string) *effortRec {
		return map[string]*effortRec{"ds": yes, "plain": no, "budget": {Levels: []string{}, Partial: true}}[m]
	}}
	for _, c := range []struct {
		model   string
		reasons int8
		chat    bool // reasoning_effort on the chat wire
		resp    bool // a reasoning block on the Responses wire
	}{
		{"ds", reasonsYes, true, true},
		{"plain", reasonsNo, false, false},
		{"budget", reasonsUnknown, true, true}, // a record it can't use yet: as before
		{"unknown", reasonsUnknown, true, true},
		{"gpt-5", reasonsUnknown, true, true},
	} {
		req := LLMRequest{Model: c.model}
		e.withEffort(context.Background(), &req, Config{ReasoningEffort: "high"})
		if req.Reasons != c.reasons || req.ReasoningEffort != "high" {
			t.Errorf("%s: reasons %d effort %q", c.model, req.Reasons, req.ReasoningEffort)
		}
		cb, _ := chatCodec{}.encode(req)
		rb, _ := responsesCodec{}.encode(req)
		if strings.Contains(string(cb), `"reasoning_effort":"high"`) != c.chat || strings.Contains(string(rb), `"reasoning":`) != c.resp {
			t.Errorf("%s: chat %s\nresponses %s", c.model, cb, rb)
		}
	}
	// no level, a model whose record says it reasons, on the Responses wire: no block it didn't get before D249
	// (l6: an older conversation's calls don't change); OpenAI's names get theirs, as before
	req := LLMRequest{Model: "ds", Reasons: reasonsYes}
	if rb, _ := (responsesCodec{}).encode(req); strings.Contains(string(rb), `"reasoning"`) || strings.Contains(string(rb), `"include"`) {
		t.Errorf("a described reasoning model with no level: %s", rb)
	}
	if rb, _ := (responsesCodec{}).encode(LLMRequest{Model: "o3", Reasons: reasonsYes}); !strings.Contains(string(rb), `"summary":"auto"`) {
		t.Errorf("an OpenAI reasoning name with no level: %s", rb)
	}
	// a record that says never: nothing goes back
	e.effortFn = func(string) *effortRec { return &effortRec{Levels: []string{"high"}, Replay: "never"} }
	req = LLMRequest{Model: "x"}
	e.withEffort(context.Background(), &req, Config{})
	if !req.NoReplay {
		t.Error("replay never: replay still on")
	}
	e.effortFn = func(string) *effortRec { return &effortRec{Replay: "never"} } // replay-only: nothing said of levels
	req = LLMRequest{Model: "x"}
	e.withEffort(context.Background(), &req, Config{ReasoningEffort: "high"})
	if !req.NoReplay || req.Reasons != reasonsUnknown || req.ReasoningEffort != "high" {
		t.Errorf("a replay-only record: %+v", req)
	}
}

// covers D249 — the provider's list is untrusted: only ladder words and
// booleans are read from an effort member.
func TestParseEffortUntrusted(t *testing.T) {
	r := parseEffort(json.RawMessage(`{"supported_levels":["max","turbo","low",3,"low"],"default_level":"ultra","can_disable":"yes","send":{"x":1},"source":"` + strings.Repeat("s", 40) + `"}`))
	if r == nil || strings.Join(r.Levels, ",") != "low,max" || r.Default != "" || r.CanDisable != nil || r.Source != "" {
		t.Fatalf("parsed %+v", r)
	}
	for _, raw := range []string{`"high"`, `{"default_level":"high"}`, `{"supported_levels":["turbo"]}`, `{"supported_levels":"low"}`,
		`{"supported_levels":[` + strings.Repeat(`"low",`, 20) + `"low"]}`} {
		if parseEffort(json.RawMessage(raw)) != nil {
			t.Errorf("%s: read as a record", raw)
		}
	}
	// m2: only the connector's own record says "no reasoning"; a provider's list saying [] is unknown
	for _, src := range []string{"admin", "host", "xbin"} {
		if r := parseEffort(json.RawMessage(`{"supported_levels":[],"source":"` + src + `"}`)); r == nil || len(r.Levels) != 0 {
			t.Errorf("no reasoning from %s: not read", src)
		}
	}
	for _, raw := range []string{`{"supported_levels":[]}`, `{"supported_levels":[],"source":"upstream"}`, `{"supported_levels":["none"],"source":"admin"}`} {
		if r := parseEffort(json.RawMessage(raw)); r != nil {
			t.Errorf("%s: read as no reasoning: %+v", raw, r)
		}
	}
	// m4: an entry that says only "replay": "never" — replay off, levels unknown (nothing said of reasoning)
	if r := parseEffort(json.RawMessage(`{"replay":"never","source":"admin"}`)); r == nil || r.Replay != "never" || r.Levels != nil {
		t.Errorf("a replay-only record: %+v", r)
	} else if known, _ := r.reasoning(); known {
		t.Error("a replay-only record says whether the model reasons")
	}
	if r := parseEffort(json.RawMessage(`{"replay":"sometimes"}`)); r != nil {
		t.Errorf("no levels, no replay never: %+v", r)
	}
	// m1: aliases onto its own levels are carried (the chip maps as the connector does); others are not
	if r := parseEffort(json.RawMessage(`{"supported_levels":["low","high"],"aliases":{"medium":"high","xhigh":"max","none":"low","turbo":"low","minimal":7}}`)); r == nil ||
		fmt.Sprint(r.Aliases) != "map[medium:high]" {
		t.Errorf("aliases: %+v", r)
	}
	var m catalogModel
	_ = json.Unmarshal([]byte(`{"id":"x","effort":{"supported_levels":["low","high"],"default_level":"high"}}`), &m)
	if m.Effort == nil || m.Effort.Default != "high" {
		t.Errorf("the catalog's model: %+v", m.Effort)
	}
}

// covers D249 — a model switch the level doesn't survive as it was gets one
// line; the same model, or a level the new model has, gets none.
func TestEffortModelSwitchLine(t *testing.T) {
	for header, want := range map[string]string{
		"high; asked=max":         "model → m2 · effort Max → High (its highest at or below)",
		"high; asked=medium":      "model → m2 · effort Medium → High (its own mapping)",
		"low; asked=none":         "model → m2 · effort Off → Low (it always thinks)",
		"none; asked=high; can't": "model → m2 · effort High → none (it has no reasoning)",
		"high":                    "",
		"ultra; asked=x":          "",
	} {
		if got := effortSwitchNote("prov|m2", header); got != want {
			t.Errorf("%q: %q, want %q", header, got, want)
		}
	}
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("first"), say("one"))
	f.on(lastUser("second"), func(LLMRequest) (LLMReply, error) {
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: "two"}, Effort: "high; asked=max"}, nil
	})
	id := newRun(t, ag, Config{Pick: "m1", Effort: "max"}, "first")
	waitStatus(t, db, id, statusIdle)
	cfg, _ := db.runConfig(id)
	cfg.Pick = "m2"
	raw, _ := json.Marshal(cfg)
	if _, err := db.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), id); err != nil {
		t.Fatal(err)
	}
	send(t, ag, id, "second")
	waitFor(t, "the second answer", func() bool { return strings.Contains(transcript(db, id), "two") })
	waitStatus(t, db, id, statusIdle)
	if n := runNotes(db, id); !strings.Contains(n, "model → m2 · effort Max → High") {
		t.Errorf("notes: %q", n)
	}
	var detail string
	_ = db.q.QueryRow(`SELECT detail FROM steps WHERE run_id=? AND kind='llm_call' ORDER BY seq DESC LIMIT 1`, id).Scan(&detail)
	if !strings.Contains(detail, `"effort":"high; asked=max"`) {
		t.Errorf("the call's journal row: %s", detail)
	}
	if calls := f.callsFor(id); calls[len(calls)-1].ReasoningEffort != "max" {
		t.Errorf("the level is the intent, kept: %q", calls[len(calls)-1].ReasoningEffort)
	}
}

// covers D249 — a compacted conversation sends no reasoning without its
// answer: the reasoning rides on its own message, and goes with it.
func TestCompactedReasoningGoesWithItsAnswer(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	think := func(text string) func(LLMRequest) (LLMReply, error) {
		return func(req LLMRequest) (LLMReply, error) {
			return LLMReply{Msg: wireMsg{Role: "assistant", Content: text}, Reasoning: "thought for " + text, ReplayField: "reasoning_content", Via: "p|m", Wire: "chat", Model: "m"}, nil
		}
	}
	f.on(lastUser("a"), think("A")).once()
	f.on(lastUser("b"), think("B")).once()
	id := newRun(t, ag, Config{}, "a")
	waitStatus(t, db, id, statusIdle)
	send(t, ag, id, "b")
	waitFor(t, "B", func() bool { return strings.Contains(transcript(db, id), "A:B") })
	waitStatus(t, db, id, statusIdle)
	live, _ := db.messages(id, true)
	var firstAnswer int64
	for _, m := range live {
		if m.Role == "assistant" {
			firstAnswer = m.ID
			break
		}
	}
	if err := db.markCompacted([]int64{live[0].ID, firstAnswer}); err != nil {
		t.Fatal(err)
	}
	run, _ := db.getRun(id)
	cfg, _ := db.runConfig(id)
	msgs, err := ag.assembleContext(context.Background(), run, cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := chatCodec{}.encode(LLMRequest{Model: "m", Via: "p|m", Msgs: msgs})
	if strings.Contains(string(b), "thought for A") || !strings.Contains(string(b), `"reasoning_content":"thought for B"`) {
		t.Errorf("after compaction: %s", b)
	}
}

// covers D249 (l4) — compaction weighs the reasoning that goes back with
// the answers: by the same estimator as their text, when the provider's
// count is unknown or out of date (just after a compaction).
func TestCompactionCountsReplayedReasoning(t *testing.T) {
	plain := &Message{Role: "assistant", Content: "answer", Tokens: estimateTokens("answer")}
	meta, _ := json.Marshal(msgMeta{Reasoning: strings.Repeat("r", 4000), ReplayField: "reasoning_content", Via: "p|m", Wire: "chat", Model: "m"})
	thought := &Message{Role: "assistant", Content: "answer", Tokens: plain.Tokens, Meta: meta}
	raw, _ := json.Marshal(msgMeta{ReasoningRaw: json.RawMessage(`[{"type":"reasoning.encrypted","data":"` + strings.Repeat("e", 2000) + `"}]`), Wire: "chat", Model: "m"})
	blocks := &Message{Role: "assistant", Content: "answer", Tokens: plain.Tokens, Meta: raw}
	shown, _ := json.Marshal(msgMeta{Reasoning: strings.Repeat("s", 4000)}) // a summary shown, never sent back
	summary := &Message{Role: "assistant", Content: "answer", Tokens: plain.Tokens, Meta: shown}
	if sentTokens(thought, nil) != plain.Tokens+1000 || sentTokens(blocks, nil) <= plain.Tokens+500 || sentTokens(summary, nil) != plain.Tokens {
		t.Errorf("sentTokens: %d %d %d (plain %d)", sentTokens(thought, nil), sentTokens(blocks, nil), sentTokens(summary, nil), plain.Tokens)
	}
	cfg := Config{System: "sys"}
	for _, c := range []struct {
		last int
		want int
	}{{0, estimateTokens("sys") + 2*plain.Tokens + 1000}, {promptStale, estimateTokens("sys") + 2*plain.Tokens + 1000}, {7000, 7000}} {
		total, sent := promptSize(cfg, &Run{LastPromptTokens: c.last}, []*Message{plain, thought}, nil)
		if total != c.want || sent != 2*plain.Tokens+1000 {
			t.Errorf("last %d: total %d sent %d, want %d", c.last, total, sent, c.want)
		}
	}
}

// covers D249 — PATCH /runs/{id} {effort} and POST /ask {effort}: a ladder
// word, default, or "" (the class's or the tile's); refused otherwise, and
// from someone who may only read.
func TestPatchRunEffort(t *testing.T) {
	setMode(t, modeLegacy, "")
	ag, h := partAgent(t)
	hdr := func(level string) map[string]string {
		return map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-User": "alice", "X-XBin-User-Level": level}
	}
	do := func(r *http.Request) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec
	}
	rec := do(as("POST", "/ask", `{"text":"hello","effort":"high"}`, hdr("write")))
	if rec.Code != 200 {
		t.Fatalf("POST /ask {effort}: %d %s", rec.Code, rec.Body)
	}
	var run struct {
		ID int64 `json:"id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Effort != "high" {
		t.Fatalf("the ask's effort: %+v", cfg.Effort)
	}
	if rec := do(as("POST", "/ask", `{"text":"x","effort":"turbo"}`, hdr("write"))); rec.Code != 400 {
		t.Errorf("POST /ask with effort turbo: %d", rec.Code)
	}
	path := "/runs/" + itoa(run.ID)
	for body, want := range map[string]int{`{"effort":"max"}`: 200, `{"effort":"default"}`: 200, `{"effort":"turbo"}`: 400, `{"effort":""}`: 200, `{"effort":"low"}`: 200} {
		if rec := do(as("PATCH", path, body, hdr("write"))); rec.Code != want {
			t.Errorf("%s: %d %s, want %d", body, rec.Code, rec.Body, want)
		}
	}
	if rec := do(as("PATCH", path, `{"effort":"low"}`, hdr("write"))); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Effort != "low" {
		t.Errorf("stored %q", cfg.Effort)
	}
	bob := map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-User": "bob", "X-XBin-User-Level": "read"}
	if rec := do(as("PATCH", path, `{"effort":"max"}`, bob)); rec.Code == 200 {
		t.Errorf("someone who may not talk in it picked its effort: %d", rec.Code)
	}
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Effort != "low" {
		t.Errorf("after a refused change: %q", cfg.Effort)
	}
	// an older config without the field reads as unset
	if c := parseConfig(`{"model":"m","reasoningEffort":"high"}`); c.Effort != "" || effortFor(c) != "high" {
		t.Errorf("an older config: %+v", c)
	}
}

// covers D249 (compat) — a newer template on an xbind (or llm-gw) whose
// model list has no effort: nothing describes the model, the chip has
// nothing to draw (model/effort.js), and the requests are the ones an
// older template sent (the golden file's request/* cases, which these
// fields leave untouched). With a listing that has one, the record is read
// — an alias's from its target.
func TestEffortFromTheCatalog(t *testing.T) {
	old := catalogCache
	t.Cleanup(func() { catalogMu.Lock(); catalogCache = old; catalogMu.Unlock() })
	var list struct {
		Data []catalogModel `json:"data"`
	}
	if err := json.Unmarshal([]byte(`{"data":[{"id":"old-xbind-model","context_length":1000},
		{"id":"house/deepseek-v4.1-flash","effort":{"supported_levels":["low","high","max"],"default_level":"high","source":"xbin"}},
		{"id":"xbin/default","alias_of":"house/deepseek-v4.1-flash","owned_by":"alias"}]}`), &list); err != nil {
		t.Fatal(err)
	}
	for i := range list.Data {
		list.Data[i].Ref = list.Data[i].ID
	}
	catalogMu.Lock()
	catalogCache = &catalog{Models: list.Data, at: time.Now()}
	catalogMu.Unlock()
	ctx := context.Background()
	if r := modelEffort(ctx, "old-xbind-model"); r != nil {
		t.Errorf("a list without effort: %+v", r)
	}
	if r := modelEffort(ctx, "xbin/default"); r == nil || r.Default != "high" {
		t.Errorf("an alias's record: %+v", r)
	}
	// the request for a model nothing describes is the one before D249
	req := LLMRequest{Model: "old-xbind-model", ReasoningEffort: "high", Msgs: []wireMsg{{Role: "user", Content: "q"}}}
	before, _ := chatCodec{}.encode(req)
	(&Engine{effortFn: func(string) *effortRec { return nil }}).withEffort(ctx, &req, Config{ReasoningEffort: "high"})
	after, _ := chatCodec{}.encode(req)
	if string(before) != string(after) || !strings.Contains(string(after), `"reasoning_effort":"high"`) {
		t.Errorf("an undescribed model's request changed:\n%s\n%s", before, after)
	}
}

// covers D249 (l5) — reasoning goes back only to the model that produced
// it, as the connector names the one that answered (X-XBin-LLM-Model): a
// request that sends it back says whose it is (X-XBin-LLM-Expect-Model),
// and when the alias's target changed under this agent's cached model list
// the connector refuses it — the call goes once more without any reasoning
// sent back, and the next is sent back only what the new model produced.
func TestReplayFollowsTheServedModel(t *testing.T) {
	fastWires(t)
	var mu sync.Mutex
	target := "house/a"
	var expects []string
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/models") {
			_, _ = io.WriteString(w, `{"data":[{"id":"house/a"},{"id":"house/b"},{"id":"xbin/default","alias_of":"`+target+`"}]}`)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body struct{ Model string }
		_ = json.Unmarshal(raw, &body)
		served := body.Model
		if served == "xbin/default" || served == "anything" { // the alias, and a name a catch-all routes
			served = target
		}
		exp := r.Header.Get("X-XBin-LLM-Expect-Model")
		expects, bodies = append(expects, exp), append(bodies, string(raw))
		if exp != "" && exp != served {
			w.WriteHeader(409)
			_, _ = io.WriteString(w, `{"error":{"message":"xbin/default is served by `+served+` now","type":"invalid_request_error","code":"xbin_llm_model_moved"}}`)
			return
		}
		w.Header().Set("X-XBin-LLM-Model", served)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok","reasoning_content":"thought by `+served+`"},"finish_reason":"stop"}]}`)
	}))
	defer srv.Close()
	t.Setenv("XBIN_IFACE_LLM", `[{"provider":"xbin:llm","url":"`+srv.URL+`","service":"openai"}]`)
	old := llmClient
	llmClient = func() *http.Client { return http.DefaultClient }
	dropCatalog()
	t.Cleanup(func() { llmClient = old; dropCatalog() })
	g := &gatewayLLM{client: http.DefaultClient}
	ctx := context.Background()
	turn := func(model string, history []wireMsg) (LLMReply, []wireMsg) {
		t.Helper()
		msgs := append(slices.Clone(history), wireMsg{Role: "user", Content: "q"})
		rep, err := g.Chat(ctx, LLMRequest{Model: model, Wire: "chat", Msgs: msgs}, nil)
		if err != nil {
			t.Fatal(err)
		}
		m := rep.Msg
		m.Replay = &msgMeta{Reasoning: rep.Reasoning, ReplayField: rep.ReplayField, Via: rep.Via, Wire: rep.Wire, Model: rep.Model}
		return rep, append(msgs, m)
	}
	last := func() (string, string) {
		mu.Lock()
		defer mu.Unlock()
		return expects[len(expects)-1], bodies[len(bodies)-1]
	}
	rep, h := turn("xbin/default", nil)
	if rep.Via != "xbin:llm|house/a" {
		t.Fatalf("the answer's Via: %q", rep.Via)
	}
	_, h = turn("xbin/default", h)
	if exp, body := last(); exp != "house/a" || !strings.Contains(body, "thought by house/a") {
		t.Fatalf("sent back to its model: expect %q, %s", exp, body)
	}
	// the alias moves; this agent's list is cached (60 s): the connector refuses, the call goes without
	mu.Lock()
	target = "house/b"
	n := len(bodies)
	mu.Unlock()
	rep, h = turn("xbin/default", h)
	mu.Lock()
	if len(bodies)-n != 2 || expects[n] != "house/a" || expects[n+1] != "" || strings.Contains(bodies[n+1], "reasoning_content") {
		t.Errorf("after the move: %d calls, expects %q, last %s", len(bodies)-n, expects[n:], bodies[len(bodies)-1])
	}
	mu.Unlock()
	if rep.Via != "xbin:llm|house/b" {
		t.Errorf("the answer after the move: Via %q", rep.Via)
	}
	// the next call sends back only what house/b produced
	_, _ = turn("xbin/default", h)
	if exp, body := last(); exp != "house/b" || !strings.Contains(body, "thought by house/b") || strings.Contains(body, "thought by house/a") {
		t.Errorf("after the move, next: expect %q, %s", exp, body)
	}
	// a name the list doesn't know (a catch-all routes it): the model that answered it, as the connector said
	rep, h = turn("anything", nil)
	_, _ = turn("anything", h)
	if exp, body := last(); rep.Via != "xbin:llm|house/b" || exp != "house/b" || !strings.Contains(body, "thought by house/b") {
		t.Errorf("a catch-all's name: Via %q, expect %q, %s", rep.Via, exp, body)
	}
}
