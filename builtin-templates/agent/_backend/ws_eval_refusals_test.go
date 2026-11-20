package main

import (
	"errors"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// The refusals xbind gives what the agent tells tile agents (the
// conversation's taint, a session switched to a mode that doesn't ask, a
// harness command): each reaches the model once, as a tool result that
// says what happened and what to do instead, and the person's card says
// the same in plain words. Nothing is retried — by the backend or, in the
// scripted run, by the model, which stops and explains.

// Scenario R1 — a start the conversation's taint keeps from the tile
// agent's model: no request is filed, the model is told the way forward
// (the connector on an allowed model, or a clean conversation), and stops.
func TestWsEvalStartRefusedByTaint(t *testing.T) {
	fx := newWsFix(t)
	s := fx.session(t, map[string]any{"name": "Books", "model": "local-llama"})
	fx.tool(t, s, "workspace_map", nil) // the map holds Finance's row: the conversation holds Finance's data
	fx.x.mu.Lock()
	fx.x.briefTaint = "finance"
	fx.x.mu.Unlock()
	f := fakeOf(fx.ag)
	brief := `{"tile":"apps/orders","goal":"Export orders as CSV","done_when":"The Export button downloads a CSV"}`
	f.on(forRun(s.Run, lastUser("csv export")), callTools(tc("b1", "tile_agent_brief", brief), tc("b2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("I can't brief the Orders agent from this conversation: it has read Finance's data. Start it on the workspace connector on a model Finance allows, or ask me in a new session.")).once()
	calls := len(f.callsFor(s.Run))
	fx.say(t, s, "add a csv export to Orders")
	// the turn is over when the model has explained: "at rest" alone was once read between the refusal and
	// the explanation (a two-core run under load, 1 in 40) — the count below was then 1
	waitFor(t, "the explanation after the refusal", func() bool {
		if n := len(f.callsFor(s.Run)) - calls; n >= 2 {
			return true
		}
		if st := statusOf(fx.ag.db, s.Run); st == statusError {
			r, _ := fx.ag.db.getRun(s.Run)
			t.Fatalf("the turn stopped on an error after %d model call(s): %s", len(f.callsFor(s.Run))-calls, r.Result)
		}
		return false
	})
	fx.idle(t, s)
	got := fx.toolResult(t, s, "tile_agent_start")
	for _, want := range []string{"is refused", "holds data of finance", "telling it less, rewording it or asking again changes nothing", "workspace's LLM connector, on a model those orgs allow", "a clean conversation", "Stop here and tell your person"} {
		if !strings.Contains(got, want) {
			t.Errorf("the start's answer lacks %q: %s", want, got)
		}
	}
	if n := fx.x.count("Ask "); n != 0 || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 0 {
		t.Fatalf("a start refused whatever approves it was filed or run: %v", fx.x.calls)
	}
	if n := len(f.callsFor(s.Run)) - calls; n != 2 {
		t.Fatalf("%d model calls: one refusal, then the explanation (2)", n)
	}
	if r := fx.ag.db.wsRefusals(s.Run); len(r) != 1 || r[0].Kind != "brief-taint" || !strings.Contains(r[0].Reason, "holds data of finance") {
		t.Fatalf("the card: %+v", r)
	}
}

// Scenario R2 — a message to a tile agent its person switched to a mode
// that doesn't ask: no longer a dead end (D250) — the message is filed for
// their approval, the whole text, and nothing reaches the session until
// they allow it. One that begins with "/" is refused, said once.
func TestWsEvalMessageRefusedByModeAndCommand(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	row, sess := fx.seedAgent(t, s, "apps/orders")
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.modeOff = map[string]bool{sess: true}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("tell it to add tests")), callTools(tc("m1", "tile_agent_message", `{"agent":"#1","text":"Please add tests for the export."}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_message")), say("Orders' agent acts without asking now, so the message waits for your approval.")).once()
	calls := len(f.callsFor(s.Run))
	fx.say(t, s, "tell it to add tests")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_agent_message"); !strings.Contains(got, "acts without asking now, so your person approves this message first") || !strings.Contains(got, "Pending — request 1") {
		t.Fatalf("the message's answer: %s", got)
	}
	if n := len(f.callsFor(s.Run)) - calls; n != 2 || fx.x.count("AgentSay refused") != 1 || len(fx.x.said[sess]) != 0 {
		t.Fatalf("after a mode refusal: %d model calls, %v", n, fx.x.calls)
	}
	if req := fx.x.pending(t); req.Kind != "message" || string(req.call.Body) != `{"text":"Please add tests for the export."}` {
		t.Fatalf("the message's request: %s %s", req.Kind, req.call.Body)
	}
	_ = row
	fx.x.mu.Lock()
	fx.x.modeOff = nil
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("switch its model")), callTools(tc("m2", "tile_agent_message", `{"agent":"#1","text":"/model opus"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_message")), say("I can't give it commands: a message starting with / is yours to give. Tell me what you want changed and I'll say it in words.")).once()
	calls = len(f.callsFor(s.Run))
	fx.say(t, s, "switch its model to opus")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_agent_message"); !strings.Contains(got, `can't begin with "/"`) || !strings.Contains(got, "Never begin a brief or a message with") {
		t.Fatalf("the command's answer: %s", got)
	}
	// (refused here, in xbind's words, before anything is sent: xbind's own refusal stays for whatever reaches it)
	if n := len(f.callsFor(s.Run)) - calls; n != 2 || fx.x.count("AgentSay refused") != 1 || len(fx.x.said[sess]) != 0 {
		t.Fatalf("after a command refusal: %d model calls, %v", n, fx.x.calls)
	}
	kinds := []string{}
	for _, r := range fx.ag.db.wsRefusals(s.Run) {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, " ") != "command" { // (a session that acts without asking is no refusal any more: its message is an approval, D250)
		t.Fatalf("the cards: %v", kinds)
	}
	_ = row
}

// The real-model run (2026-11-17) — a command for a tile agent that waits
// for its person. "/model opus" was held behind the tile agent's permission
// request like any message, the model told its person "it gets it once
// you've answered", and xbind refused it when it would have gone in. A
// message that begins with "/" is refused whatever the tile agent is
// doing, so it is refused at once: nothing held, nothing sent, the same
// words and the same card as xbind's refusal.
func TestWsEvalCommandRefusedThoughTheTileAgentWaits(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	row, sess := fx.seedAgent(t, s, "apps/orders")
	fx.x.setAgent(sess, func(a *wsAgent) { a.Pending, a.Waiting = 1, "permission: run the export tests" })
	fx.notify(t)
	fx.rest(t, s)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("/model opus")), callTools(tc("c1", "tile_agent_message", `{"agent":"#1","text":"  /model opus"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_message")), say("I can't give it commands: those are yours. Tell me what you want changed and I'll say it in words.")).once()
	fx.say(t, s, "Tell the Orders agent /model opus.")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "tile_agent_message")
	if !strings.Contains(got, `can't begin with "/"`) || !strings.Contains(got, "those are your person's to give") || strings.Contains(got, "Held") {
		t.Fatalf("a command for a tile agent that waits: %s", got)
	}
	if ids, _ := fx.ag.db.wsHeld(row.ID); len(ids) != 0 || fx.x.count("AgentSay") != 0 || len(fx.x.said[sess]) != 0 {
		t.Fatalf("the command was held or sent: held %v, xbind saw %v", ids, fx.x.calls)
	}
	kinds := []string{}
	for _, r := range fx.ag.db.wsRefusals(s.Run) {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, " ") != "command" {
		t.Fatalf("the refusal's card: %v, want one command refusal", kinds)
	}
	// a message in words is still held behind the question, as before
	f.on(forRun(s.Run, lastUser("say it in words")), callTools(tc("c2", "tile_agent_message", `{"agent":"#1","text":"Please use the opus model for this."}`))).once()
	fx.say(t, s, "say it in words then")
	fx.idle(t, s)
	if ids, _ := fx.ag.db.wsHeld(row.ID); len(ids) != 1 || !strings.Contains(fx.toolResult(t, s, "tile_agent_message"), "Held, not delivered") {
		t.Fatalf("a message in words to a tile agent that waits: held %v", ids)
	}
}

// Scenario R3 — messages held behind a tile agent's question, refused when
// they would go in (its person switched its mode meanwhile): dropped, not
// tried at every pass; the conversation is told once.
func TestWsEvalHeldMessagesRefusedOnce(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	row, sess := fx.seedAgent(t, s, "apps/orders")
	fx.x.setAgent(sess, func(a *wsAgent) { a.Pending = 1 })
	fx.notify(t)
	// (a permission request wakes nobody: the conversation is at rest)
	fx.rest(t, s)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("also add a header row")), callTools(tc("h1", "tile_agent_message", `{"agent":"#1","text":"Also add a header row."}`))).once()
	fx.say(t, s, "also add a header row")
	fx.idle(t, s)
	if ids, _ := fx.ag.db.wsHeld(row.ID); len(ids) != 1 {
		t.Fatalf("the message isn't held behind the question: %v", ids)
	}
	// alice answers, and switches the session to a mode that doesn't ask
	fx.x.mu.Lock()
	fx.x.modeOff = map[string]bool{sess: true}
	fx.x.mu.Unlock()
	fx.x.setAgent(sess, func(a *wsAgent) { a.Pending = 0 })
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("Your message to the Orders agent wasn't delivered: you switched it to a mode that doesn't ask.")).once()
	fx.notify(t)
	fx.rest(t, s)
	fx.notify(t)
	fx.notify(t)
	if n := fx.x.count("AgentSay refused"); n != 1 {
		t.Fatalf("the held message was tried %d times", n)
	}
	if ids, _ := fx.ag.db.wsHeld(row.ID); len(ids) != 0 {
		t.Fatalf("a refused held message stays held: %v", ids)
	}
	up := fx.updates(t, s)
	if len(up) == 0 || !strings.Contains(up[len(up)-1], "were not delivered") || !strings.Contains(up[len(up)-1], "only they can drive it now") {
		t.Fatalf("the update: %q", up)
	}
}

// The words and the kinds of the refusals the switches give a tool call.
func TestWsOffRefusalWords(t *testing.T) {
	for _, c := range []struct{ refusal, kind, words string }{
		{"no-agent", "no-agent", "hasn't been given a workspace agent"},
		{"held", "held", "held on this xbin"},
		{"taint", "brief-taint", "a clean conversation"},
		{"mode", "mode", "only they can drive it now"},
		{"command", "command", `can't begin with "/"`},
	} {
		err := error(&xbin.DelegateError{Status: 403, Refusal: c.refusal, Message: "xbind says so"})
		if kind, _ := wsRefusalKind(err); kind != c.kind {
			t.Errorf("%s: kind %q, want %q", c.refusal, kind, c.kind)
		}
		if w := wsRefusal(err); !strings.Contains(w, c.words) {
			t.Errorf("%s: %q lacks %q", c.refusal, w, c.words)
		}
	}
	if !wsSendRefused(&xbin.DelegateError{Refusal: "mode"}) || wsSendRefused(errors.New("xbind unreachable")) {
		t.Fatal("wsSendRefused")
	}
}

// The real-model run (2026-11-17) — continuing a tile agent by its brief.
// Asked to continue a tile agent an xbin restart had cut off, a real model
// named the brief it had started it with; the refusal said "write a new
// brief for new work", and it briefed the same work anew. The refusal now
// names the way to continue: resume.
func TestWsEvalStartedBriefPointsAtResume(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	if got := fx.tool(t, s, "tile_agent_brief", map[string]any{"tile": "apps/orders", "goal": "an export", "done_when": "the export downloads"}); !strings.Contains(got, "Brief 1") {
		t.Fatalf("the brief: %s", got)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE ws_briefs SET state='started' WHERE run_id=? AND id=1`, s.Run); err != nil {
		t.Fatal(err)
	}
	got := fx.tool(t, s, "tile_agent_start", map[string]any{"brief": 1})
	for _, want := range []string{"brief 1 was started already", `tile_agent_start {"resume": its number}`, "a restart cut it off", "write a new brief only for new work"} {
		if !strings.Contains(got, want) {
			t.Errorf("starting a brief that was started lacks %q: %s", want, got)
		}
	}
	if fx.x.count("Ask") != 0 {
		t.Fatalf("something was filed: %v", fx.x.calls)
	}
}
