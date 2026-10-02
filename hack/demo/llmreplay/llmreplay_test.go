package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Chat Completions, streamed: recorded through the proxy (the upstream gets
// the proxy's key, never the caller's, and no lane), the caller gets the
// upstream's bytes as they come; the cassette has every event with its
// pause and no credential; a replay with the upstream gone, whose request
// carries another timestamp, gets the very same bytes.
func TestChatStreamRecordReplay(t *testing.T) {
	f := newFakeAPI(t, 30*time.Millisecond)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	hdr := map[string]string{"Authorization": "Bearer llm-gw-placeholder"}
	req := chat("gpt-5.1", true, "You are Northwind's launch assistant.", user("Plan the launch review for 2026-10-02T09:30:00Z"))
	got := send(t, rp.URL+"/lane/agent/v1/chat/completions", hdr, req)
	if got.status != 200 || !strings.Contains(got.body, `"content":"Answer `) || !strings.HasSuffix(got.body, "data: [DONE]\n\n") {
		t.Fatalf("recorded call: %d %q", got.status, got.body)
	}
	if ct := got.header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	if got.header.Get("Set-Cookie") != "" {
		t.Fatalf("the upstream's cookie reached the caller")
	}
	up := f.seen()[0]
	if up.path != "/v1/chat/completions" || up.header.Get("Authorization") != "Bearer "+openaiKey || up.header.Get(laneHeader) != "" {
		t.Fatalf("the upstream got %s auth=%q lane=%q", up.path, up.header.Get("Authorization"), up.header.Get(laneHeader))
	}
	text := finish(t, rp, cas)
	for _, secret := range []string{openaiKey, "llm-gw-placeholder", "COOKIE-SECRET", "acme-org-SECRET"} {
		if strings.Contains(text, secret) {
			t.Fatalf("the cassette holds %q:\n%s", secret, text)
		}
	}
	c, err := loadCassette(cas)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Exchanges) != 1 {
		t.Fatalf("%d exchanges", len(c.Exchanges))
	}
	ex := c.Exchanges[0]
	if ex.Lane != "agent" || ex.Seq != 0 || ex.Path != "/v1/chat/completions" || ex.Status != 200 || len(ex.Chunks) < 6 {
		t.Fatalf("exchange: lane %s seq %d path %s status %d, %d chunks", ex.Lane, ex.Seq, ex.Path, ex.Status, len(ex.Chunks))
	}
	var joined strings.Builder
	for i, ch := range ex.Chunks {
		joined.Write(ch.bytes())
		if i > 0 && ch.Ms < 20 { // the fake pauses 30 ms between events
			t.Fatalf("event %d recorded %.1f ms after the one before", i, ch.Ms)
		}
	}
	if joined.String() != got.body {
		t.Fatalf("recorded events %q\nwhat the caller got %q", joined.String(), got.body)
	}
	f.Close()

	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	req2 := chat("gpt-5.1", true, "You are Northwind's launch assistant.", user("Plan the launch review for 2026-10-03T14:05:12Z"))
	again := send(t, pp.URL+"/lane/agent/v1/chat/completions", hdr, req2)
	if again.status != 200 || again.body != got.body || again.header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("replay: %d %q\nwant %q", again.status, again.body, got.body)
	}
	if again.header.Get("X-Request-Id") != "req_1" {
		t.Fatalf("replayed headers: %v", again.header)
	}
	if strings.Contains(pp.log.String(), "DRIFT") || !strings.Contains(pp.log.String(), "thread") {
		t.Fatalf("a timestamp alone should match by thread, quietly:\n%s", pp.log.String())
	}
}

// The Anthropic Messages API as Claude Code speaks it: lane by header, the
// proxy's key as x-api-key in place of the caller's subscription token
// (and its oauth beta flag dropped), the query kept; a replay of the next
// session (new session id in metadata and billing line) gets the same
// stream.
func TestAnthropicStreamRecordReplay(t *testing.T) {
	f := newFakeAPI(t, 20*time.Millisecond)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	hdr := map[string]string{laneHeader: "claude", "Authorization": "Bearer " + callerSecret, "Anthropic-Version": "2023-06-01",
		"Anthropic-Beta": "oauth-2025-04-20,interleaved-thinking-2025-05-14", "User-Agent": "claude-cli/2.1.280 (external, cli)"}
	first := claude("claude-sonnet-4-5", "9b2c1f0e-1111-4c2d-9e8f-0a1b2c3d4e5f", userBlocks("<system-reminder>Today's date is 2026-10-02.</system-reminder>", "Add a CSV export to the invoices page"))
	got := send(t, rp.URL+"/v1/messages?beta=true", hdr, first)
	if got.status != 200 || !strings.Contains(got.body, "event: message_stop") {
		t.Fatalf("recorded call: %d %q", got.status, got.body)
	}
	up := f.seen()[0]
	if up.path != "/v1/messages" || up.query != "beta=true" || up.header.Get("X-Api-Key") != anthropicKey || up.header.Get("Authorization") != "" ||
		up.header.Get("Anthropic-Version") != "2023-06-01" || up.header.Get("Anthropic-Beta") != "interleaved-thinking-2025-05-14" {
		t.Fatalf("the upstream got %s?%s headers %v", up.path, up.query, up.header)
	}
	text := finish(t, rp, cas)
	for _, secret := range []string{anthropicKey, callerSecret} {
		if strings.Contains(text, secret) {
			t.Fatalf("the cassette holds %q", secret)
		}
	}
	f.Close()

	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	next := claude("claude-sonnet-4-5", "77aa0c3d-2222-4e5f-8a9b-1c2d3e4f5a6b", userBlocks("<system-reminder>Today's date is 2026-10-05.</system-reminder>", "Add a CSV export to the invoices page"))
	again := send(t, pp.URL+"/lane/claude/v1/messages?beta=true", hdr, next)
	if again.status != 200 || again.body != got.body {
		t.Fatalf("replay: %d %q\nwant %q", again.status, again.body, got.body)
	}
	if lg := pp.log.String(); strings.Contains(lg, "DRIFT") {
		t.Fatalf("a new session and date are no drift:\n%s", lg)
	}
	// the default lane recorded nothing: an error in the Messages API's shape
	noLane := map[string]string{"Anthropic-Version": "2023-06-01"}
	miss := send(t, pp.URL+"/v1/messages", noLane, next)
	var e struct {
		Type  string `json:"type"`
		Error struct {
			Type, Message string
		} `json:"error"`
	}
	if miss.status != 400 || json.Unmarshal([]byte(miss.body), &e) != nil || e.Type != "error" || !strings.Contains(e.Error.Message, `lane "default"`) {
		t.Fatalf("a miss: %d %s", miss.status, miss.body)
	}
}

// The Responses API, both ways: a streamed call and a plain one.
func TestResponsesRecordReplay(t *testing.T) {
	f := newFakeAPI(t, 10*time.Millisecond)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	body := func(stream bool, ask string) obj {
		return obj{"model": "gpt-5.1", "stream": stream, "store": false, "instructions": "You are the ops agent of Northwind.",
			"input": []any{obj{"role": "user", "content": []any{obj{"type": "input_text", "text": ask}}}}, "prompt_cache_key": "conv-81f2"}
	}
	s1 := send(t, rp.URL+"/lane/agent/v1/responses", nil, body(true, "Summarize yesterday's incidents"))
	s2 := send(t, rp.URL+"/lane/agent/v1/responses", nil, body(false, "Draft the status page note"))
	if s1.status != 200 || !strings.Contains(s1.body, "event: response.completed") || s2.status != 200 || !strings.Contains(s2.body, `"output_text"`) {
		t.Fatalf("recorded: %q / %q", s1.body, s2.body)
	}
	finish(t, rp, cas)
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	// another cache key (a volatile field): the exact hash still holds
	b2 := body(false, "Draft the status page note")
	b2["prompt_cache_key"] = "conv-0c11"
	r2 := send(t, pp.URL+"/lane/agent/v1/responses", nil, b2)
	r1 := send(t, pp.URL+"/lane/agent/v1/responses", nil, body(true, "Summarize yesterday's incidents"))
	if r1.body != s1.body || r2.body != s2.body || r2.header.Get("Content-Length") == "" {
		t.Fatalf("replay: %q / %q", r1.body, r2.body)
	}
	if lg := pp.log.String(); strings.Count(lg, "exact)") != 2 {
		t.Fatalf("both should match exactly:\n%s", lg)
	}
}

// The heart of it: an agent's lane with side calls and parallel subagents,
// and a coding agent's lane, recorded in one order and replayed in another
// with every volatile detail changed — each request gets the response its
// own counterpart got, the lanes never cross, and nothing is reported as
// drift.
func TestSequenceMatchingAcrossThreadsAndLanes(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	const main = "You are Northwind's operations agent. Current time: 09:12."
	const sub = "You are a subagent of Northwind's operations agent."
	tasks := []string{"Audit the pricing page copy", "Check the onboarding emails", "Review the changelog draft"}
	type step struct {
		name string
		url  string
		hdr  map[string]string
		body func(variant bool) obj
	}
	agent := rp.URL + "/lane/agent/v1/chat/completions"
	ts := func(v bool, a, b string) string {
		if v {
			return b
		}
		return a
	}
	steps := []step{
		{"main 1", agent, nil, func(v bool) obj {
			return chat("gpt-5.1", true, main, user("Prepare the launch checklist for Thursday"))
		}},
		{"title", agent, nil, func(v bool) obj {
			return chat("gpt-5.1", false, "Name this conversation in three words.", user("First message:\nPrepare the launch checklist for Thursday"))
		}},
	}
	for i, task := range tasks {
		steps = append(steps, step{"sub " + tasks[i] + " 1", agent, nil, func(v bool) obj { return chat("gpt-5.1", true, sub, user(task)) }})
	}
	for i, task := range tasks {
		id := []string{"call_Ab12Cd34Ef", "call_Gh56Ij78Kl", "call_Mn90Op12Qr"}[i]
		steps = append(steps, step{"sub " + tasks[i] + " 2", agent, nil, func(v bool) obj {
			return chat("gpt-5.1", true, sub, user(task), calls(id, "read_file", `{"path":"notes.md"}`),
				result(id, "notes.md (modified "+ts(v, "2026-10-02 09:14:07", "2026-10-03 16:40:51")+", request "+
					ts(v, "3f6a1c2e-0b1d-4e8f-9a7b-6c5d4e3f2a1b", "c0ffee00-1234-4abc-9def-001122334455")+"): "+task))
		}})
	}
	steps = append(steps, step{"main 2", agent, nil, func(v bool) obj {
		return chat("gpt-5.1", true, main, user("Prepare the launch checklist for Thursday"), said("I'll ask three helpers."),
			user("Helpers finished in "+ts(v, "41.2s", "38.9s")+"."))
	}})
	claudeURL := rp.URL + "/lane/claude/v1/messages?beta=true"
	ch := map[string]string{"Anthropic-Version": "2023-06-01"}
	sess := func(v bool) string {
		return ts(v, "11111111-2222-4333-8444-555555555555", "99999999-8888-4777-8666-555555555555")
	}
	steps = append(steps,
		step{"claude 1", claudeURL, ch, func(v bool) obj {
			return claude("claude-sonnet-4-5", sess(v), userBlocks("Add a CSV export to the invoices page"))
		}},
		step{"claude 2", claudeURL, ch, func(v bool) obj {
			return claude("claude-sonnet-4-5", sess(v), userBlocks("Add a CSV export to the invoices page"),
				toolUse("toolu_01AbCdEfGhIjKlMn", "Bash", obj{"command": "go test ./invoices/..."}),
				toolResult("toolu_01AbCdEfGhIjKlMn", "ok  \tnorthwind/invoices\t"+ts(v, "0.412s", "0.388s")))
		}},
	)
	recorded := map[string]string{}
	for _, s := range steps {
		r := send(t, strings.Replace(s.url, rp.URL, rp.URL, 1), s.hdr, s.body(false))
		if r.status != 200 {
			t.Fatalf("%s: %d %s", s.name, r.status, r.body)
		}
		recorded[s.name] = r.body
	}
	finish(t, rp, cas)
	f.Close()

	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	// the take: the title call first, the subagents' calls interleaved
	// otherwise, Claude Code's calls in between
	order := []string{"title", "main 1", "claude 1", "sub Review the changelog draft 1", "sub Audit the pricing page copy 1",
		"sub Check the onboarding emails 1", "sub Audit the pricing page copy 2", "claude 2", "sub Review the changelog draft 2",
		"sub Check the onboarding emails 2", "main 2"}
	byName := map[string]step{}
	for _, s := range steps {
		byName[s.name] = s
	}
	for _, name := range order {
		s := byName[name]
		url := strings.Replace(s.url, rp.URL, pp.URL, 1)
		got := send(t, url, s.hdr, s.body(true))
		if got.body != recorded[name] {
			t.Fatalf("%s: replayed %q\nwant %q", name, got.body, recorded[name])
		}
	}
	lg := pp.log.String()
	if strings.Contains(lg, "DRIFT") || strings.Contains(lg, "MISS") {
		t.Fatalf("a clean take reported drift:\n%s", lg)
	}
	st := pp.s.status()
	if st.Lanes["agent"].Left != 0 || st.Lanes["claude"].Left != 0 || st.Lanes["agent"].Served != 9 {
		t.Fatalf("status: %+v", st.Lanes)
	}
	// a reset starts the take over
	pp.s.play.reset("")
	if again := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, byName["main 1"].body(true)); again.body != recorded["main 1"] {
		t.Fatalf("after a reset: %q", again.body)
	}
}

// Drift: a tool result unlike the recorded one still gets the recorded
// answer (the take goes on) but is reported, with where the two part; a
// retyped first prompt finds its conversation by similarity and the rest of
// the conversation follows it.
func TestDriftWarnings(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	url := rp.URL + "/lane/agent/v1/chat/completions"
	sys := "You are Northwind's release agent."
	turn1 := func(ask string) obj { return chat("gpt-5.1", true, sys, user(ask)) }
	turn2 := func(ask, out string) obj {
		return chat("gpt-5.1", true, sys, user(ask), calls("call_Run0Tests1", "bash", `{"command":"go test ./billing/..."}`), result("call_Run0Tests1", out))
	}
	const ask = "Run the billing test suite and tell me if we can ship"
	const passed = "ok  \tnorthwind/billing\t0.412s\nok  \tnorthwind/billing/invoice\t1.031s"
	r1 := send(t, url, nil, turn1(ask))
	r2 := send(t, url, nil, turn2(ask, passed))
	finish(t, rp, cas)

	// the same take, but the tests failed this time
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	purl := pp.URL + "/lane/agent/v1/chat/completions"
	if got := send(t, purl, nil, turn1(ask)); got.body != r1.body {
		t.Fatalf("turn 1: %q", got.body)
	}
	failed := "--- FAIL: TestRoundTotals (0.00s)\n    invoice_test.go:41: total = 9.99, want 10.00\nFAIL\tnorthwind/billing/invoice\t0.977s"
	if got := send(t, purl, nil, turn2(ask, failed)); got.body != r2.body {
		t.Fatalf("turn 2 should still get its recorded answer: %q", got.body)
	}
	lg := pp.log.String()
	if !strings.Contains(lg, "DRIFT agent request 2") || !strings.Contains(lg, "message 3 (tool) differs") {
		t.Fatalf("no drift reported:\n%s", lg)
	}
	if d := pp.s.status().Lanes["agent"].Drifts; len(d) != 1 || !strings.Contains(d[0], "FAIL") {
		t.Fatalf("status drifts: %q", d)
	}

	// the first prompt retyped: found by similarity (and reported), then the
	// conversation follows it
	pp2 := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	purl2 := pp2.URL + "/lane/agent/v1/chat/completions"
	const retyped = "Run the billing test suite and tell me whether we can ship"
	if got := send(t, purl2, nil, turn1(retyped)); got.body != r1.body {
		t.Fatalf("retyped turn 1: %q", got.body)
	}
	if got := send(t, purl2, nil, turn2(retyped, passed)); got.body != r2.body {
		t.Fatalf("retyped turn 2: %q", got.body)
	}
	lg2 := pp2.log.String()
	if !strings.Contains(lg2, "nearest") || !strings.Contains(lg2, "DRIFT agent request 1") || !strings.Contains(lg2, "step 2, thread)") {
		t.Fatalf("a retyped prompt:\n%s", lg2)
	}
	// and past the end of the recording: a miss, in the OpenAI shape
	miss := send(t, purl2, nil, turn1("Something else entirely"))
	var e struct {
		Error struct{ Message, Type string } `json:"error"`
	}
	if miss.status != 400 || json.Unmarshal([]byte(miss.body), &e) != nil || e.Error.Type != "llmreplay_error" {
		t.Fatalf("a miss: %d %s", miss.status, miss.body)
	}
	if pp2.s.status().Lanes["agent"].Misses != 1 {
		t.Fatalf("misses: %+v", pp2.s.status().Lanes["agent"])
	}
}

// The nearest-match window counts model calls, not lookups: a dozen model
// listings recorded before the first call don't push it out of reach.
func TestNearestPastLookups(t *testing.T) {
	cas := filepath.Join(t.TempDir(), "t.jsonl")
	var exs []Exchange
	for i := range 12 {
		exs = append(exs, Exchange{Lane: "agent", Seq: i, Method: "GET", Path: "/v1/models", Status: 200,
			Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: `{"object":"list","data":[]}`})
	}
	exs = append(exs, Exchange{Lane: "agent", Seq: 12, Method: "POST", Path: "/v1/chat/completions",
		ReqBody: mustJSON(chat("gpt-5.1", false, "You are Northwind's travel agent.", user("Plan the team offsite in Lisbon for October"))),
		Status:  200, Headers: map[string][]string{"Content-Type": {"application/json"}}, Body: `{"choices":[]}`})
	writeCassette(t, cas, exs...)
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	got := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", false, "You are Northwind's travel agent.", user("Plan a team offsite in Lisbon for October")))
	if got.status != 200 || got.body != `{"choices":[]}` || !strings.Contains(pp.log.String(), "nearest") {
		t.Fatalf("a retyped first call after lookups: %d %s\n%s", got.status, got.body, pp.log.String())
	}
}

// Replay timing: headers at the recorded latency (counted from the
// request's arrival), each event after its recorded pause — as recorded,
// scaled, capped, or not at all.
func TestReplayTiming(t *testing.T) {
	cas := filepath.Join(t.TempDir(), "t.jsonl")
	ex := Exchange{Lane: "agent", Seq: 0, Method: "POST", Path: "/v1/chat/completions", ReqBody: json.RawMessage(`{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
		Status: 200, Headers: map[string][]string{"Content-Type": {"text/event-stream"}}, HeadMs: 400, BodyMs: 50,
		Chunks: []Chunk{{Ms: 10, Data: "data: {\"a\":1}\n\n"}, {Ms: 200, Data: "data: {\"a\":2}\n\n"}, {Ms: 300, Data: "data: [DONE]\n\n"}}}
	writeCassette(t, cas, ex)
	for _, tc := range []struct {
		name   string
		timing float64
		cap    time.Duration
		want   []time.Duration // after the headers' wait
		head   time.Duration
	}{
		{"original", 1, 0, ms3(10, 200, 300, 50), 400 * time.Millisecond},
		{"half", 0.5, 0, ms3(5, 100, 150, 25), 200 * time.Millisecond},
		{"capped", 1, 120 * time.Millisecond, ms3(10, 120, 120, 50), 120 * time.Millisecond},
		{"instant", 0, 0, ms3(0, 0, 0, 0), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opt := replayOpts()
			opt.timing, opt.maxWait = tc.timing, tc.cap
			sl := &sleepLog{}
			pp := replayProxy(t, cas, opt, upstreams{}, sl)
			got := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, obj{"model": "gpt-5.1", "stream": true, "messages": []any{user("hi")}})
			if got.body != "data: {\"a\":1}\n\ndata: {\"a\":2}\n\ndata: [DONE]\n\n" {
				t.Fatalf("body %q", got.body)
			}
			d := sl.all()
			if len(d) != 5 {
				t.Fatalf("waits %v", d)
			}
			if d[0] > tc.head || d[0] < tc.head-time.Second {
				t.Fatalf("the headers' wait %v, want %v less what matching took", d[0], tc.head)
			}
			for i, w := range tc.want {
				if d[i+1] != w {
					t.Fatalf("waits %v, want %v after the headers'", d[1:], tc.want)
				}
			}
		})
	}
}

func ms3(v ...int) []time.Duration {
	var out []time.Duration
	for _, n := range v {
		out = append(out, time.Duration(n)*time.Millisecond)
	}
	return out
}

func writeCassette(t *testing.T, path string, exs ...Exchange) {
	t.Helper()
	rec, _, err := openRecorder(path, false, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := range exs {
		if err := rec.add(&exs[i]); err != nil {
			t.Fatal(err)
		}
	}
	_ = rec.close()
}

// On the real clock, events arrive spread out as recorded (each flushed on
// its own), not all at the end.
func TestReplayStreamsOnTheClock(t *testing.T) {
	cas := filepath.Join(t.TempDir(), "t.jsonl")
	var chunks []Chunk
	for i := range 4 {
		chunks = append(chunks, Chunk{Ms: 80, Data: "data: {\"n\":" + string(rune('0'+i)) + "}\n\n"})
	}
	writeCassette(t, cas, Exchange{Lane: "default", Method: "POST", Path: "/v1/chat/completions",
		ReqBody: json.RawMessage(`{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`),
		Status:  200, Headers: map[string][]string{"Content-Type": {"text/event-stream"}}, Chunks: chunks})
	c, err := loadCassette(cas)
	if err != nil {
		t.Fatal(err)
	}
	pp := startProxy(t, replayOpts(), upstreams{}, func(s *server) { s.play = newPlayer(c, replayOpts()) })
	req, _ := http.NewRequest("POST", pp.URL+"/v1/chat/completions", strings.NewReader(`{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`))
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	br := bufio.NewReader(r.Body)
	var at []time.Time
	for {
		line, err := br.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			at = append(at, time.Now())
		}
		if err != nil {
			break
		}
	}
	if len(at) != 4 {
		t.Fatalf("%d events", len(at))
	}
	if gap := at[3].Sub(at[0]); gap < 200*time.Millisecond {
		t.Fatalf("the events came %v apart, first to last; recorded 240 ms", gap)
	}
}

// Lookups (GET /v1/models, embeddings) answer from the cassette any number
// of times without moving the sequence; a model list nobody recorded is
// made up from the models the calls used.
func TestLookupsAndModelList(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	models := send(t, rp.URL+"/lane/agent/v1/models", nil, nil)
	turn := send(t, rp.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", false, "s", user("hello")))
	finish(t, rp, cas)
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	for range 3 {
		if got := send(t, pp.URL+"/lane/agent/v1/models", nil, nil); got.body != models.body {
			t.Fatalf("models: %q", got.body)
		}
	}
	if got := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", false, "s", user("hello"))); got.body != turn.body {
		t.Fatalf("the call after lookups: %q", got.body)
	}

	// a cassette without a model list
	cas2 := cassettePath(t)
	rp2 := recordProxy(t, fakeUps(f), cas2)
	send(t, rp2.URL+"/lane/claude/v1/messages", map[string]string{"Anthropic-Version": "2023-06-01"}, claude("claude-sonnet-4-5", "11111111-2222-4333-8444-555555555555", userBlocks("hi")))
	finish(t, rp2, cas2)
	pp2 := replayProxy(t, cas2, replayOpts(), upstreams{}, nil)
	var list struct {
		Data []struct{ ID, Type string } `json:"data"`
	}
	got := send(t, pp2.URL+"/lane/claude/v1/models", map[string]string{"Anthropic-Version": "2023-06-01"}, nil)
	if json.Unmarshal([]byte(got.body), &list) != nil || len(list.Data) != 1 || list.Data[0].ID != "claude-sonnet-4-5" || list.Data[0].Type != "model" {
		t.Fatalf("made-up model list: %s", got.body)
	}
}

// A 429 the client retried past is left out of the replay (the first try
// gets the real answer); -keep-errors replays it too.
func TestTransientErrorsLeftOut(t *testing.T) {
	f := newFakeAPI(t, 0)
	f.fail429 = 1
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	req := chat("gpt-5.1", false, "s", user("Send the weekly digest"))
	limited := send(t, rp.URL+"/lane/agent/v1/chat/completions", nil, req)
	ok := send(t, rp.URL+"/lane/agent/v1/chat/completions", nil, req)
	if limited.status != 429 || ok.status != 200 || limited.header.Get("Retry-After") != "1" {
		t.Fatalf("recorded %d then %d", limited.status, ok.status)
	}
	finish(t, rp, cas)
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	if got := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, req); got.status != 200 || got.body != ok.body {
		t.Fatalf("replay: %d %q", got.status, got.body)
	}
	opt := replayOpts()
	opt.keepErrors = true
	pk := replayProxy(t, cas, opt, upstreams{}, nil)
	first := send(t, pk.URL+"/lane/agent/v1/chat/completions", nil, req)
	second := send(t, pk.URL+"/lane/agent/v1/chat/completions", nil, req)
	if first.status != 429 || first.header.Get("Retry-After") != "1" || second.body != ok.body {
		t.Fatalf("-keep-errors: %d then %d", first.status, second.status)
	}
}

// A caller that hangs up mid-replay (a timeout) gets the same exchange on
// its retry.
func TestHungUpReplayIsPutBack(t *testing.T) {
	cas := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`
	writeCassette(t, cas, Exchange{Lane: "default", Method: "POST", Path: "/v1/chat/completions", ReqBody: json.RawMessage(body),
		Status: 200, Headers: map[string][]string{"Content-Type": {"text/event-stream"}},
		Chunks: []Chunk{{Data: "data: 1\n\n"}, {Ms: 5000, Data: "data: 2\n\n"}}})
	c, err := loadCassette(cas)
	if err != nil {
		t.Fatal(err)
	}
	pp := startProxy(t, replayOpts(), upstreams{}, func(s *server) { s.play = newPlayer(c, replayOpts()) })
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, "POST", pp.URL+"/v1/chat/completions", strings.NewReader(body))
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(r.Body).ReadString('\n')
	if line != "data: 1\n" {
		t.Fatalf("first event %q", line)
	}
	cancel()
	r.Body.Close()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(pp.log.String(), "unused again") {
		if time.Now().After(deadline) {
			t.Fatalf("not put back:\n%s", pp.log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	pp.s.sleep = (*sleepLog)(nil).sleep // the retry plays at once
	if got := send(t, pp.URL+"/v1/chat/completions", nil, json.RawMessage(body)); got.body != "data: 1\n\ndata: 2\n\n" {
		t.Fatalf("the retry: %q", got.body)
	}
}

// LLMREPLAY_TOKEN: a caller without it gets 401; with it (as a bearer token
// or x-api-key) it is let in, and the token never goes upstream.
func TestProxyToken(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rec, next, err := openRecorder(cas, false, false)
	if err != nil {
		t.Fatal(err)
	}
	defer rec.close()
	ups := fakeUps(f)
	ups.anthropic.key = "" // no key: with a token, nothing of the caller's goes up either
	p := startProxy(t, options{mode: "record", token: "film-crew-token"}, ups, func(s *server) { s.rec, s.seq = rec, next })
	req := chat("gpt-5.1", false, "s", user("hi"))
	if r := send(t, p.URL+"/lane/agent/v1/chat/completions", nil, req); r.status != 401 {
		t.Fatalf("no token: %d", r.status)
	}
	if r := send(t, p.URL+"/lane/agent/v1/chat/completions", map[string]string{"Authorization": "Bearer wrong"}, req); r.status != 401 {
		t.Fatalf("a wrong token: %d", r.status)
	}
	if r := send(t, p.URL+"/_llmreplay/status", nil, nil); r.status != 401 {
		t.Fatalf("status without a token: %d", r.status)
	}
	if r := send(t, p.URL+"/lane/agent/v1/chat/completions", map[string]string{"Authorization": "Bearer film-crew-token"}, req); r.status != 200 {
		t.Fatalf("with the token: %d %s", r.status, r.body)
	}
	send(t, p.URL+"/lane/claude/v1/messages", map[string]string{"X-Api-Key": "film-crew-token", "Anthropic-Version": "2023-06-01"},
		claude("claude-sonnet-4-5", "11111111-2222-4333-8444-555555555555", userBlocks("hi")))
	for _, r := range f.seen() {
		if strings.Contains(r.header.Get("Authorization")+r.header.Get("X-Api-Key"), "film-crew-token") {
			t.Fatalf("the token went upstream: %v", r.header)
		}
	}
	if s := f.seen(); len(s) != 2 || s[0].header.Get("Authorization") != "Bearer "+openaiKey || s[1].header.Get("X-Api-Key") != "" {
		t.Fatalf("upstream auth: %v / %v", s[0].header, s[1].header)
	}
}

// With no key configured and no proxy token, the caller's own credentials
// go upstream (a signed-in Claude Code) — and still never into the cassette.
func TestCallerCredentialsPassThrough(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	ups := fakeUps(f)
	ups.anthropic.key = ""
	rp := recordProxy(t, ups, cas)
	r := send(t, rp.URL+"/lane/claude/v1/messages", map[string]string{"Authorization": "Bearer " + callerSecret, "Anthropic-Version": "2023-06-01",
		"Anthropic-Beta": "oauth-2025-04-20"}, claude("claude-sonnet-4-5", "11111111-2222-4333-8444-555555555555", userBlocks("hi")))
	if r.status != 200 {
		t.Fatalf("%d %s", r.status, r.body)
	}
	up := f.seen()[0]
	if up.header.Get("Authorization") != "Bearer "+callerSecret || up.header.Get("Anthropic-Beta") != "oauth-2025-04-20" {
		t.Fatalf("upstream headers: %v", up.header)
	}
	if text := finish(t, rp, cas); strings.Contains(text, callerSecret) {
		t.Fatalf("the cassette holds the caller's token")
	}
}

// passthrough relays and records nothing; replay's -on-miss passthrough
// sends what the cassette can't answer upstream.
func TestPassthrough(t *testing.T) {
	f := newFakeAPI(t, 5*time.Millisecond)
	p := startProxy(t, options{mode: "passthrough"}, fakeUps(f), nil)
	r := send(t, p.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", true, "s", user("hi")))
	if r.status != 200 || !strings.HasSuffix(r.body, "data: [DONE]\n\n") || len(f.seen()) != 1 {
		t.Fatalf("passthrough: %d %q", r.status, r.body)
	}
	cas := filepath.Join(t.TempDir(), "empty.jsonl")
	writeCassette(t, cas)
	opt := replayOpts()
	opt.onMiss = "passthrough"
	pp := replayProxy(t, cas, opt, fakeUps(f), nil)
	if r := send(t, pp.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", false, "s", user("hi"))); r.status != 200 || len(f.seen()) != 2 {
		t.Fatalf("a miss passed through: %d %s", r.status, r.body)
	}
}

// Replay under concurrency: four conversations at once, each served its own
// (go test -race covers the player's locking).
func TestConcurrentConversations(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	conv := func(i, step int) obj {
		msgs := []obj{user("Write the release note for service " + string(rune('A'+i)))}
		if step == 2 {
			msgs = append(msgs, said("Here is a draft."), user("Shorter, please."))
		}
		return chat("gpt-5.1", true, "You are a subagent.", msgs...)
	}
	want := map[[2]int]string{}
	for step := 1; step <= 2; step++ {
		for i := range 4 {
			want[[2]int{i, step}] = send(t, rp.URL+"/lane/agent/v1/chat/completions", nil, conv(i, step)).body
		}
	}
	finish(t, rp, cas)
	pp := replayProxy(t, cas, replayOpts(), upstreams{}, nil)
	var wg sync.WaitGroup
	errs := make(chan string, 8)
	for i := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for step := 1; step <= 2; step++ {
				b, _ := json.Marshal(conv(i, step))
				r, err := http.Post(pp.URL+"/lane/agent/v1/chat/completions", "application/json", strings.NewReader(string(b)))
				if err != nil {
					errs <- err.Error()
					return
				}
				got, _ := io.ReadAll(r.Body)
				r.Body.Close()
				if string(got) != want[[2]int{i, step}] {
					errs <- string(got)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Errorf("a conversation got another's answer: %q", e)
	}
}

// A cassette: refused when it exists (unless -append or -overwrite),
// appended with its sequences going on, read back sorted, a torn last line
// skipped.
func TestCassetteFile(t *testing.T) {
	cas := cassettePath(t)
	writeCassette(t, cas, Exchange{Lane: "agent", Seq: 0, Method: "POST", Path: "/v1/chat/completions", Status: 200})
	if _, _, err := openRecorder(cas, false, false); err == nil || !strings.Contains(err.Error(), "-append") {
		t.Fatalf("an existing cassette: %v", err)
	}
	rec, next, err := openRecorder(cas, true, false)
	if err != nil || next["agent"] != 1 {
		t.Fatalf("append: %v %v", err, next)
	}
	_ = rec.add(&Exchange{Lane: "agent", Seq: 2, Method: "POST", Path: "/v1/chat/completions", Status: 200})
	_ = rec.add(&Exchange{Lane: "agent", Seq: 1, Method: "POST", Path: "/v1/chat/completions", Status: 200})
	_ = rec.add(&Exchange{Lane: "claude", Seq: 0, Method: "POST", Path: "/v1/messages", Status: 200})
	_ = rec.close()
	fh, _ := os.OpenFile(cas, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = fh.WriteString(`{"lane":"agent","seq":3,"meth`)
	fh.Close()
	st, _ := os.Stat(cas)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("cassette mode %v", st.Mode().Perm())
	}
	c, err := loadCassette(cas)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, ex := range c.Exchanges {
		got = append(got, ex.Lane+string(rune('0'+ex.Seq)))
	}
	if strings.Join(got, ",") != "agent0,agent1,agent2,claude0" || c.Skipped != 1 {
		t.Fatalf("loaded %v (skipped %d)", got, c.Skipped)
	}
	if _, err := loadCassette(filepath.Join(t.TempDir(), "x")); err == nil {
		t.Fatal("a missing cassette loaded")
	}
}

// inspect lists a cassette lane by lane, numbered as replay numbers it.
func TestInspect(t *testing.T) {
	f := newFakeAPI(t, 0)
	cas := cassettePath(t)
	rp := recordProxy(t, fakeUps(f), cas)
	send(t, rp.URL+"/lane/agent/v1/chat/completions", nil, chat("gpt-5.1", true, "s", user("Plan the offsite")))
	send(t, rp.URL+"/lane/agent/v1/models", nil, nil)
	finish(t, rp, cas)
	var out, errb strings.Builder
	if code := inspectCmd([]string{"-v", cas}, &out, &errb); code != 0 {
		t.Fatalf("inspect: %d %s", code, errb.String())
	}
	for _, want := range []string{"lane agent: 1 model calls in 1 threads", "seq 0", "openai-chat", "gpt-5.1", "stream", "Plan the offsite", "GET /v1/models"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("inspect output lacks %q:\n%s", want, out.String())
		}
	}
}

// Recording refuses a path git would track.
func TestGitGuard(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	if err := gitGuard(filepath.Join(repo, "takes", "a.jsonl")); err == nil {
		t.Fatal("a tracked path was allowed")
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("/.film-media/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := gitGuard(filepath.Join(repo, ".film-media", "replay", "a.jsonl")); err != nil {
		t.Fatalf("an ignored path: %v", err)
	}
	if err := gitGuard(filepath.Join(t.TempDir(), "a.jsonl")); err != nil {
		t.Fatalf("outside any repository: %v", err)
	}
}

func TestOptionsAndEnv(t *testing.T) {
	for in, want := range map[string]float64{"original": 1, "instant": 0, "0.5": 0.5, "0.5x": 0.5, "2": 2} {
		if got, err := parseTiming(in); err != nil || got != want {
			t.Fatalf("-timing %s: %v %v", in, got, err)
		}
	}
	if _, err := parseTiming("fast"); err == nil {
		t.Fatal("-timing fast parsed")
	}
	env := map[string]string{"OPENAI_API_KEY": "sk-a", "LLMREPLAY_OPENAI_BASE_URL": "https://openrouter.ai/api/v1", "ANTHROPIC_AUTH_TOKEN": "tok",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:9398/lane/claude"}
	u := upstreamsFromEnv(func(k string) string { return env[k] })
	if u.openai.base != "https://openrouter.ai/api/v1" || u.openai.key != "sk-a" || u.anthropic.base != "https://api.anthropic.com" ||
		u.anthropic.key != "tok" || u.anthropic.header != "Authorization" {
		t.Fatalf("upstreams %+v", u)
	}
	if checkLoop("127.0.0.1:9398", u) != nil {
		t.Fatal("a real upstream is no loop")
	}
	u.anthropic.base = "http://localhost:9398/lane/claude"
	if checkLoop("127.0.0.1:9398", u) == nil {
		t.Fatal("the proxy as its own upstream")
	}
	if joinURL("https://api.openai.com/v1", "/v1/chat/completions", "") != "https://api.openai.com/v1/chat/completions" ||
		joinURL("https://api.anthropic.com/", "/v1/messages", "beta=true") != "https://api.anthropic.com/v1/messages?beta=true" {
		t.Fatal("joinURL")
	}
	r, _ := http.NewRequest("GET", "/lane/claude/v1/messages", nil)
	if lane, path, err := laneOf(r); lane != "claude" || path != "/v1/messages" || err != nil {
		t.Fatalf("laneOf: %s %s %v", lane, path, err)
	}
	r, _ = http.NewRequest("GET", "/v1/messages", nil)
	r.Header.Set(laneHeader, "agent")
	if lane, path, _ := laneOf(r); lane != "agent" || path != "/v1/messages" {
		t.Fatalf("laneOf by header: %s %s", lane, path)
	}
	r, _ = http.NewRequest("GET", "/lane/../v1", nil)
	if _, _, err := laneOf(r); err == nil {
		t.Fatal("a bad lane passed")
	}
}
