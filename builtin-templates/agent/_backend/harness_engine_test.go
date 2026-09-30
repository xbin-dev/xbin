package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// harnessFixture is the agent with its route table, a fake manager whose
// image has the scripted coding agent (this test binary, re-executed as
// the adapter: TestMain) and alice's sandbox there, reaching out.
func harnessFixture(t *testing.T, stdio bool, flags ...string) (*Agent, *http.ServeMux, *sbxSandbox) {
	t.Helper()
	ag, mux, box, _ := harnessFixtureWith(t, nil, stdio, flags...)
	return ag, mux, box
}

// harnessFixtureWith is harnessFixture with the manager's handler wrapped
// (bindSbxWith), and the manager.
func harnessFixtureWith(t *testing.T, wrap func(http.Handler) http.Handler, stdio bool, flags ...string) (*Agent, *http.ServeMux, *sbxSandbox, *sbxTestManager) {
	t.Helper()
	ag, mux := accessFixture(t)
	m := bindSbxWith(t, wrap, "apps/cs")["apps/cs"]
	if !stdio {
		m.Caps = []string{"exec", "files", "tar"}
	}
	argv := acptest.Command(flags...)
	m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Egress: "internet"})
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(ag.eng) }) // before the manager goes (cleanups run last first)
	return ag, mux, box, m
}

// settleHarnesses stops e (no pass runs after it), lets its adapters go
// and deletes their execs, and waits for their consumers — nothing of a
// test outlives it.
func settleHarnesses(e *Engine) {
	e.Shutdown(5 * time.Second)
	e.mu.Lock()
	var all []*hsess
	for _, s := range e.harness {
		all = append(all, s)
	}
	e.mu.Unlock()
	for _, s := range all {
		if s.pipe == nil {
			continue
		}
		_ = s.pipe.t.Conn.ExecDelete(context.Background(), s.pipe.t.ID, s.pipe.ExecID())
		for _, ch := range []<-chan struct{}{s.pipe.Done(), s.done} {
			select {
			case <-ch:
			case <-time.After(15 * time.Second):
			}
		}
	}
}

// draftText is the run's draft text now.
func draftText(e *Engine, run int64) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := e.drafts[run]; d != nil {
		return d.Text
	}
	return ""
}

// successor is the engine that takes over from ag's after a handoff: it
// ends its adapters, then shuts down, when the test does.
func successor(t *testing.T, ag *Agent) *Engine {
	a := ag.eng
	// the predecessor's consumers stop applying once it let its adapters go:
	// wait for them before newEngine rewrites ag.eng (which they read)
	a.mu.Lock()
	var old []*hsess
	for _, s := range a.harness {
		old = append(old, s)
	}
	a.mu.Unlock()
	for _, s := range old {
		if s.isHalted() {
			select {
			case <-s.done:
			case <-time.After(15 * time.Second):
			}
		}
	}
	b := newEngine(ag.db, ag, a.llm, "")
	t.Cleanup(func() {
		settleHarnesses(b)
		settleHarnesses(a)
	})
	b.takeOver()
	return b
}

// askHarness starts a harness conversation as alice and returns its run.
func askHarness(t *testing.T, mux *http.ServeMux, box *sbxSandbox, text string) *Run {
	t.Helper()
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": text, "class": "coding",
		"harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("POST /ask: %d %s", w.Code, w.Body)
	}
	if run.Engine != engineHarness {
		t.Fatalf("the run's engine: %q", run.Engine)
	}
	return &run
}

// hwait waits (up to 30 s: the adapter is a process to start) for cond.
func hwait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// turnOver: the run rests and its session has no prompt on its way.
func turnOver(ag *Agent, id int64) func() bool {
	return func() bool {
		r, err := ag.db.getRun(id)
		hs, _ := ag.db.harnessSession(id)
		return err == nil && resting(r.Status) && hs != nil && hs.PromptState == ""
	}
}

// viewOf is GET /runs/{id}/view as alice.
func viewOf(t *testing.T, mux *http.ServeMux, id int64) map[string]any {
	t.Helper()
	w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", id), nil)
	var v map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("view: %d %s", w.Code, w.Body)
	}
	return v
}

func msgsOf(v map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range v["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

// hEvents keeps what the hub publishes for one tree.
type hEvents struct {
	mu  sync.Mutex
	evs []*Event
}

func watchTree(t *testing.T, ag *Agent, root int64) *hEvents {
	s, _, _ := ag.eng.hub.subscribe(root, -1, who{kind: whoSystem})
	l := &hEvents{}
	go func() {
		for range s.wake {
			evs := s.drain()
			l.mu.Lock()
			l.evs = append(l.evs, evs...)
			l.mu.Unlock()
		}
	}()
	t.Cleanup(func() { ag.eng.hub.unsubscribe(s) })
	return l
}

func (l *hEvents) of(typ string) []*Event {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*Event
	for _, e := range l.evs {
		if e.Type == typ {
			out = append(out, e)
		}
	}
	return out
}

// An ask with a coding agent: the run is the harness's, its first message
// its first prompt; a turn with one call of every kind lands as one
// assistant row and one tool row per call (§4.3.5) — the `acp` lift, the
// results, the diff — the summary (§4.3.2) says what happened, the stream
// carries the harness event; a follow-up message is the next prompt.
func TestHarnessAskTurn(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		ag, mux, box := harnessFixture(t, stdio)
		run := askHarness(t, mux, box, "cards")
		if run.Status != statusRunning || run.Title != "cards" {
			t.Fatalf("the new run: %+v", run)
		}
		evs := watchTree(t, ag, run.ID)
		hwait(t, "the cards turn", turnOver(ag, run.ID))
		if st := statusOf(ag.db, run.ID); st != statusIdle {
			t.Fatalf("after the turn: %s %s", st, fullText(ag.db, run.ID))
		}
		v := viewOf(t, mux, run.ID)
		msgs := msgsOf(v)
		kinds := []string{"read", "edit", "delete", "move", "search", "execute", "fetch", "think", "other"}
		var calls, tools []map[string]any
		var user, last map[string]any
		for _, m := range msgs {
			switch m["role"] {
			case "user":
				user = m
			case "assistant":
				if m["toolCalls"] != nil {
					calls = append(calls, m)
				} else {
					last = m
				}
			case "tool":
				tools = append(tools, m)
			}
		}
		if user == nil || user["content"] != "cards" || user["sender"] != "alice" {
			t.Fatalf("the user row: %v", user)
		}
		if len(calls) != len(kinds) || len(tools) != len(kinds) {
			t.Fatalf("%d call rows, %d tool rows: %s", len(calls), len(tools), transcript(ag.db, run.ID))
		}
		for i, k := range kinds {
			tc := calls[i]["toolCalls"].([]any)[0].(map[string]any)
			fn := tc["function"].(map[string]any)
			var args map[string]any
			_ = json.Unmarshal([]byte(fn["arguments"].(string)), &args)
			if fn["name"] != "acp:"+k || !strings.HasPrefix(tc["id"].(string), "h1:card-") || args["summary"] == nil {
				t.Fatalf("call %d: %v", i, tc)
			}
			tr := tools[i]
			acp, _ := tr["acp"].(map[string]any)
			if tr["toolCallId"] != tc["id"] || tr["name"] != "acp:"+k || acp["kind"] != k || acp["status"] != "completed" {
				t.Fatalf("tool row %d: %v", i, tr)
			}
		}
		read, edit, exec := tools[0], tools[1], tools[5]
		if read["content"] != "```\nhello\n```" {
			t.Fatalf("the read's result: %q", read["content"])
		}
		ea := edit["acp"].(map[string]any)
		diffs, _ := ea["diffs"].([]any)
		if len(diffs) != 1 || !strings.HasPrefix(edit["content"].(string), "edited ") || !strings.HasSuffix(edit["content"].(string), "hello.txt (+1 −1)") {
			t.Fatalf("the edit: %q %v", edit["content"], ea)
		}
		d := diffs[0].(map[string]any)
		if d["status"] != "modified" || d["add"] != 1.0 || d["del"] != 1.0 || !strings.Contains(d["patch"].(string), "-hello\n+hello, world\n") ||
			!strings.HasPrefix(d["patch"].(string), "--- a/") {
			t.Fatalf("the diff: %v", d)
		}
		if files, _ := ea["files"].([]any); len(files) != 1 {
			t.Fatalf("the edit's files: %v", ea["files"])
		}
		xa := exec["acp"].(map[string]any)
		if xa["exitCode"] != 0.0 || !strings.Contains(xa["output"].(string), "ok  \texample") || exec["content"] != "ok  \texample\t0.01s\n\n[exit 0]" {
			t.Fatalf("the execute: %q %v", exec["content"], xa)
		}
		if last == nil || last["content"] != "cards done" || last["acp"] != nil {
			t.Fatalf("the answer: %v", last)
		}
		sum := v["run"].(map[string]any)
		h, _ := sum["harness"].(map[string]any)
		if sum["engine"] != engineHarness || h == nil || h["provider"] != "fake" || h["name"] != "Fake agent (tests)" || h["state"] != "ready" || h["gen"] != 1.0 {
			t.Fatalf("the summary: %v", sum)
		}
		c := h["counts"].(map[string]any)
		u, _ := h["usage"].(map[string]any)
		sb := h["sandbox"].(map[string]any)
		if c["tools"] != 9.0 || c["files"] != 1.0 || c["add"] != 1.0 || c["del"] != 1.0 || c["paths"] != nil || u["used"] != 42.0 ||
			sb["ref"] != sandboxRef("apps/cs", box.ID) || h["title"] != "fake: cards" || sum["title"] != "fake: cards" {
			t.Fatalf("the summary's counts, usage, sandbox, title: %v", h)
		}
		mode := h["mode"].(map[string]any)
		if mode["current"] != "ask" {
			t.Fatalf("the mode: %v", mode)
		}
		if len(evs.of(evHarness)) == 0 || len(evs.of(evMessage)) < 2*len(kinds) {
			t.Fatalf("events: %d harness, %d message", len(evs.of(evHarness)), len(evs.of(evMessage)))
		}
		var hv map[string]any
		b, _ := json.Marshal(evs.of(evHarness)[len(evs.of(evHarness))-1].Data)
		_ = json.Unmarshal(b, &hv)
		if hv["provider"] != "fake" {
			t.Fatalf("the harness event: %s", b)
		}
		asks, _ := ag.db.asks(run.ID)
		if len(asks) != 1 || asks[0].Text != "cards" {
			t.Fatalf("the ledger: %+v", asks)
		}

		// a follow-up is the next prompt, in the same adapter
		w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo two"})
		if w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		var q []*InboxRow
		for _, r := range ag.db.inboxRows(`WHERE run_id=?`, run.ID) {
			q = append(q, r)
		}
		if len(q) != 2 || q[1].Kind != inboxHPrompt {
			t.Fatalf("the inbox: %+v", q)
		}
		hwait(t, "the second turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
		})
		hs, _ := ag.db.harnessSession(run.ID)
		if hs.Gen != 1 || hs.State != hsLive || hs.ReadOff == 0 || hs.Draft != "" {
			t.Fatalf("the session: %+v", hs)
		}
	})
}

// A handoff mid-turn: the owning engine lets the adapter go (never kills
// it), a successor attaches at the committed offset and the turn ends once
// — its text written once, whole.
func TestHarnessHandoffMidTurn(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		ag, mux, box := harnessFixture(t, stdio)
		run := askHarness(t, mux, box, "slow")
		hwait(t, "a few ticks", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 2") })
		hs, _ := ag.db.harnessSession(run.ID)
		if hs.PromptState != "sent" || hs.PromptRPC == "" {
			t.Fatalf("the prompt on its way: %+v", hs)
		}
		ag.eng.BeginShutdown()
		exec := hs.ExecID
		b := successor(t, ag)
		hwait(t, "the successor to end the turn", turnOver(ag, run.ID))
		if st := statusOf(ag.db, run.ID); st != statusIdle {
			t.Fatalf("after the handoff: %s", st)
		}
		text := fullText(ag.db, run.ID)
		if strings.Count(text, "tick 0 ") != 1 || strings.Count(text, "tick 9 ") != 1 {
			t.Fatalf("the turn's text: %q", text)
		}
		var answers int
		msgs, _ := ag.db.messages(run.ID, false)
		for _, m := range msgs {
			if m.Role == "assistant" {
				answers++
				if !strings.HasPrefix(m.Content, "tick 0 tick 1 tick 2") {
					t.Fatalf("the answer: %q", m.Content)
				}
			}
		}
		hs, _ = ag.db.harnessSession(run.ID)
		if answers != 1 || hs.Gen != 1 || hs.ExecID != exec || b.harnessOf(run.ID) == nil {
			t.Fatalf("%d answers, the session %+v", answers, hs)
		}
		// the successor drives it on
		send := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "after"})
		if send.Code != 200 {
			t.Fatalf("message: %d %s", send.Code, send.Body)
		}
		hwait(t, "the successor's turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: after")
		})
	})
}

// A steer flushes the text before it from the pass, not the consumer; a
// handoff before the turn's next durable event reads on from an offset
// that step stored — the flushed text isn't read (and written) again.
func TestHarnessHandoffAfterSteer(t *testing.T) {
	ag, mux, box := harnessFixture(t, false, "--steer")
	run := askHarness(t, mux, box, "steer")
	hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "use tabs"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the steer's user row", func() bool { return strings.Contains(transcript(ag.db, run.ID), "U:use tabs") })
	ag.eng.BeginShutdown()
	successor(t, ag)
	hwait(t, "the turn", turnOver(ag, run.ID))
	text := fullText(ag.db, run.ID)
	for _, tick := range []string{"tick 0 ", "tick 1 ", "tick 9 "} {
		if strings.Count(text, tick) != 1 {
			t.Fatalf("%q ×%d: %s", tick, strings.Count(text, tick), transcript(ag.db, run.ID))
		}
	}
}

// Parks survive a handoff: the successor answers the permission the
// predecessor parked on (restored from pendingState.harness), and a
// question (from the stored snapshot).
func TestHarnessParkHandoff(t *testing.T) {
	transports(t, func(t *testing.T, stdio bool) {
		ag, mux, box := harnessFixture(t, stdio)
		run := askHarness(t, mux, box, "perm")
		p := parkOf(t, ag, run.ID, "approval")
		q := askHarness(t, mux, box, "ask")
		qp := parkOf(t, ag, q.ID, "question")
		ag.eng.BeginShutdown()
		b := successor(t, ag)
		hwait(t, "the attaches", func() bool { return b.harnessOf(run.ID) != nil && b.harnessOf(q.ID) != nil })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": p.Park}); w.Code != 200 {
			t.Fatalf("approve: %d %s", w.Code, w.Body)
		}
		if _, _, err := ag.queue(q.ID, inboxHAnswer, inboxBody{Park: qp.Park, Action: "accept", Sender: "alice",
			Content: json.RawMessage(`{"question_0":"Postgres"}`)}, ""); err != nil {
			t.Fatal(err)
		}
		hwait(t, "the permission's turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
		hwait(t, "the question's turn", func() bool {
			return turnOver(ag, q.ID)() && strings.Contains(fullText(ag.db, q.ID), `answers: {"question_0":"Postgres"}`)
		})
	})
}

// A handoff while a turn stalls: the successor attaches and an interrupt
// ends the turn (session/cancel → interrupted), once.
func TestHarnessHandoffInterrupt(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "stall")
	hwait(t, "stalling", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && hs.PromptState == "sent" && draftText(ag.eng, run.ID) == "stalling"
	})
	ag.eng.BeginShutdown()
	b := successor(t, ag)
	hwait(t, "the attach", func() bool { return b.harnessOf(run.ID) != nil })
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/interrupt", run.ID), nil); w.Code != 200 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body)
	}
	hwait(t, "the interrupted turn", turnOver(ag, run.ID))
	r, _ := ag.db.getRun(run.ID)
	msgs, _ := ag.db.messages(run.ID, false)
	n := 0
	for _, m := range msgs {
		if m.Role == "assistant" && m.Content == "stalling" {
			n++
		}
	}
	if r.Status != statusIdle || n != 1 {
		t.Fatalf("after the interrupt: %s, %d × stalling: %s", r.Status, n, transcript(ag.db, run.ID))
	}
}

// A restart with the adapter idle: recover() finds the live session, the
// new engine attaches (same generation, same exec) and the next prompt goes
// to it; runs of the agent's own loop and a session row of a run an older
// binary deleted are left alone.
func TestHarnessRecover(t *testing.T) {
	ag, mux, box := harnessFixture(t, true)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the first turn", turnOver(ag, run.ID))
	before, _ := ag.db.harnessSession(run.ID)

	own := newRun(t, ag, Config{}, "hello") // the agent's own loop, beside it
	waitStatus(t, ag.db, own, statusIdle)
	if err := ag.db.putHarnessSession(&harnessSession{RunID: 999, State: hsLive, ExecID: "e1"}); err != nil {
		t.Fatal(err)
	}
	if err := ag.db.putHarnessSession(&harnessSession{RunID: own, State: hsLive, ExecID: "e1"}); err != nil {
		t.Fatal(err)
	}
	if ids := scanIDs(ag.db.q.Query(harnessRecoverSQL)); len(ids) != 1 || ids[0] != run.ID {
		t.Fatalf("recover's harness runs: %v", ids)
	}

	ag.eng.Shutdown(2 * time.Second)
	b := successor(t, ag)
	hwait(t, "the attach after the restart", func() bool { return b.harnessOf(run.ID) != nil })
	if b.harnessOf(own) != nil || b.harnessOf(999) != nil {
		t.Fatal("a run that isn't a coding agent's was taken for one")
	}
	w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo two"})
	if w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn after the restart", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo two")
	})
	after, _ := ag.db.harnessSession(run.ID)
	if after.Gen != before.Gen || after.ExecID != before.ExecID {
		t.Fatalf("respawned: %+v → %+v", before, after)
	}
	ownRun, _ := ag.db.getRun(own)
	sum := runSummary(ownRun)
	if _, ok := sum["harness"]; ok || sum["engine"] != "" {
		t.Fatalf("the built-in run's summary: %v", sum)
	}
	if msgs := fakeOf(ag).callsFor(run.ID); len(msgs) != 0 {
		t.Fatalf("the harness run reached the model: %d calls", len(msgs))
	}
}

// POST /ask's refusals for a coding agent (§4.2.3) and POST /runs with one.
func TestHarnessAskRefusals(t *testing.T) {
	_, mux, box := harnessFixture(t, false)
	ref := sandboxRef("apps/cs", box.ID)
	none := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "offline", Egress: "none"})
	for _, c := range []struct {
		body map[string]any
		code int
		want string
	}{
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "nope"}}, 400, `no coding agent "nope"`},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake"}, "system": "be nice"}, 400, "keeps its own instructions"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake"}, "model": "m"}, 400, "harness.options.model"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake"}, "class": "internal"}, 400, "doesn't allow"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake", "mode": "nope"}}, 400, "harness.mode: one of"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake", "options": map[string]string{"mode": "yolo"}}}, 400, "the mode is harness.mode"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake"}}, 400, "needs a sandbox"},
		{map[string]any{"text": "x", "harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", none.ID)}}, 409, "egress is none"},
	} {
		w := callAs(t, mux, asAlice, "POST", "/ask", c.body)
		var e struct{ Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &e)
		if w.Code != c.code || !strings.Contains(e.Error, c.want) {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body)
		}
	}
	// an explicit mode is a person's own: not an admin's viewing as one (D64)
	viewAs := caller{from: "apps/agent", user: "alice", level: "read", viewedBy: "admin"}
	if w := callAs(t, mux, viewAs, "POST", "/ask", map[string]any{"text": "x", "harness": map[string]any{"provider": "fake", "mode": "yolo"},
		"sandbox": map[string]any{"ref": ref}}); w.Code != 403 || !strings.Contains(w.Body.String(), "only a person can start") {
		t.Fatalf("an explicit mode viewed as alice: %d %s", w.Code, w.Body)
	}
	w := callAs(t, mux, asAlice, "POST", "/runs", map[string]any{"goal": "echo hi", "harness": map[string]any{"provider": "fake", "mode": "yolo"},
		"sandbox": map[string]any{"ref": ref}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil || run.Engine != engineHarness {
		t.Fatalf("POST /runs: %d %s", w.Code, w.Body)
	}
	cfg, _ := agent.db.runConfig(run.ID)
	if cfg.Harness == nil || cfg.Harness.Mode != "yolo" || cfg.Harness.By != "alice" || cfg.Harness.Ref != ref {
		t.Fatalf("its harness: %+v", cfg.Harness)
	}
	hwait(t, "its turn", turnOver(agent, run.ID))
}

// A permission request parks the run (§4.3.4): waiting_input, the card
// data in pendingState.harness, the call's row awaiting approval, the
// summary's pending; an approve answers it and the turn goes on.
func TestHarnessPermissionPark(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	hwait(t, "the park", func() bool { return statusOf(ag.db, run.ID) == statusWaiting })
	v := viewOf(t, mux, run.ID)
	sum := v["run"].(map[string]any)
	ps := sum["pendingState"].(map[string]any)
	h, _ := ps["harness"].(map[string]any)
	if ps["kind"] != "approval" || ps["park"] == "" || h == nil || h["callId"] != "h1:t1" || h["pid"] == nil || h["rpcId"] == nil {
		t.Fatalf("the park: %v", ps)
	}
	if opts := h["options"].([]any); len(opts) != 3 || h["tool"].(map[string]any)["kind"] != "execute" || h["rule"] == nil {
		t.Fatalf("the park's card: %v", h)
	}
	if tcs, _ := ps["toolCalls"].([]any); len(tcs) != 1 {
		t.Fatalf("the park's toolCalls: %v", ps["toolCalls"])
	}
	pend := sum["harness"].(map[string]any)["pending"].(map[string]any)
	if pend["kind"] != "approval" || pend["park"] != ps["park"] || pend["title"] != "run ls" {
		t.Fatalf("the summary's pending: %v", pend)
	}
	var row map[string]any
	for _, m := range msgsOf(v) {
		if m["role"] == "tool" {
			row = m
		}
	}
	if row == nil || row["content"] != toolAwaitingApproval {
		t.Fatalf("the call's row: %v", row)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": ps["park"]}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn after the approval", turnOver(ag, run.ID))
	text := transcript(ag.db, run.ID)
	if statusOf(ag.db, run.ID) != statusIdle || !strings.Contains(text, "T:a.txt b.txt") || !strings.Contains(text, "A:listed") {
		t.Fatalf("after the approval: %s", text)
	}
	if r, _ := ag.db.getRun(run.ID); r.Pending != "" {
		t.Fatalf("the park stays: %s", r.Pending)
	}
}

// The adapter dying mid-turn ends the turn with why; the next message
// starts a new generation.
func TestHarnessAdapterExit(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "crash")
	hwait(t, "the turn's end", func() bool { return statusOf(ag.db, run.ID) == statusError })
	r, _ := ag.db.getRun(run.ID)
	hs, _ := ag.db.harnessSession(run.ID)
	if !strings.Contains(r.Result, "exited 3") || hs.State != hsStopped || hs.PromptState != "" || !strings.Contains(fullText(ag.db, run.ID), "going down") {
		t.Fatalf("after the crash: %q %+v", r.Result, hs)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next generation's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo again")
	})
	if hs, _ = ag.db.harnessSession(run.ID); hs.Gen != 2 || statusOf(ag.db, run.ID) != statusIdle {
		t.Fatalf("the respawn: %+v", hs)
	}
}

// Deleting a coding agent's conversation ends its adapter too.
func TestHarnessDelete(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	hs, _ := ag.db.harnessSession(run.ID)
	s := ag.eng.harnessOf(run.ID)
	if w := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d", run.ID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	select {
	case <-s.pipe.Done():
	case <-time.After(20 * time.Second):
		t.Fatal("the adapter outlived its conversation")
	}
	if ex := s.pipe.Exit(); ex.State == "running" || ex.State == "lost" {
		t.Fatalf("exec %s: %+v", hs.ExecID, ex)
	}
	if ag.eng.harnessOf(run.ID) != nil {
		t.Fatal("still driven")
	}
}
