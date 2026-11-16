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
// that doesn't ask, and one that begins with "/": refused, said once, and
// the model stops.
func TestWsEvalMessageRefusedByModeAndCommand(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	row, sess := fx.seedAgent(t, s, "apps/orders")
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.modeOff = map[string]bool{sess: true}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("tell it to add tests")), callTools(tc("m1", "tile_agent_message", `{"agent":"#1","text":"Please add tests for the export."}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_message")), say("The Orders agent takes no more messages from me: you switched it to a mode that doesn't ask. Switch it back, or tell it yourself.")).once()
	calls := len(f.callsFor(s.Run))
	fx.say(t, s, "tell it to add tests")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_agent_message"); !strings.Contains(got, "doesn't ask before it acts") || !strings.Contains(got, "only they can drive it now") || !strings.Contains(got, "Don't send it again") {
		t.Fatalf("the message's answer: %s", got)
	}
	if n := len(f.callsFor(s.Run)) - calls; n != 2 || fx.x.count("AgentSay refused") != 1 || len(fx.x.said[sess]) != 0 {
		t.Fatalf("after a mode refusal: %d model calls, %v", n, fx.x.calls)
	}

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
	if n := len(f.callsFor(s.Run)) - calls; n != 2 || fx.x.count("AgentSay refused") != 2 {
		t.Fatalf("after a command refusal: %d model calls, %v", n, fx.x.calls)
	}
	kinds := []string{}
	for _, r := range fx.ag.db.wsRefusals(s.Run) {
		kinds = append(kinds, r.Kind)
	}
	if strings.Join(kinds, " ") != "command mode" {
		t.Fatalf("the cards: %v", kinds)
	}
	_ = row
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
	// the update that it waits wakes the conversation: it reaches it first,
	// so the person's message below is the last thing the model reads (a
	// message sent before the update is delivered is read before it)
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
