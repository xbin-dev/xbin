package main

// The owner's test session of 2026-11-21, replayed (D250; plans/wsagent/
// approvals-ux.md): "turn eui into a tic tac toe game" took five approval
// rounds and started nothing. Each scenario here is one of its frictions,
// now one round: the person's pick on the sheet is what runs, and the
// agent is told so; a follow-up continues the session that is there; a
// fix for a session that acts without asking is one message approval; a
// brief the person edits before approving is one round; a start that
// fails to open is tried again by the person, not filed again.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const tttBrief = `{"tile":"apps/eui","goal":"Turn the eui page into a playable tic tac toe game","done_when":"Opening apps/eui shows a 3x3 board and a reset control; a win or a draw is announced","leave_alone":"No backend: this tile is a page","check_by":"Play it through: X wins a row, a draw, reset"}`

// eui is the empty page tile of the session, and OpenCode among the coding
// agents it allows.
func euiFix(t *testing.T) (*wsFix, *wsSession) {
	t.Helper()
	fx := newWsFix(t)
	fx.x.mu.Lock()
	fx.x.addTile(wsTile{Path: "apps/eui", Title: "eui", Kind: "static", Status: "running", Agent: "full", Level: "terminal"})
	fx.x.harnesses = append(fx.x.harnesses, wsHarness{ID: "opencode", Name: "OpenCode"})
	fx.x.mu.Unlock()
	return fx, fx.main(t)
}

// requestsFiled is how many approval requests the agent filed.
func (f *fakeXbin) requestsFiled() int { return f.count("Ask ") }

// The tic tac toe start, the person switching the coding agent to OpenCode
// on the sheet: ONE approval round, it starts with OpenCode, and the agent
// is told what ran — never "the approval no longer covered the call".
func TestWsEvalApprovalPickIsTheStart(t *testing.T) {
	fx, s := euiFix(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("tic tac toe")), callTools(tc("c1", "tile_agent_brief", tttBrief), tc("c2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("I've written the brief; it starts once you allow it.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("eui's agent has started on the game, on OpenCode.")).once()
	fx.say(t, s, "turn eui into a tic tac toe game")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "tile_agent_start")
	if !strings.Contains(got, "Pending — request 1") || !strings.Contains(got, "an approval doesn't lapse while it waits, so waiting is fine") {
		t.Fatalf("the start's answer: %q", got)
	}
	req := fx.x.pending(t)
	if a := req.startBody(); a.Harness != "" || a.Mode != "" || a.Model != "" || a.Continue != "" {
		t.Fatalf("the agent suggested what the person never named: %s", req.call.Body)
	}
	// the person picks OpenCode on xbin's sheet, and approves: that is the approval
	fx.x.decideWith(req.ID, wsChosen{Provider: "opencode", Mode: "plan"})
	fx.notify(t)
	waitFor(t, "the start's outcome", func() bool { return strings.Contains(fx.lastAssistant(t, s), "on OpenCode") })
	fx.idle(t, s)
	agents := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if fx.x.requestsFiled() != 1 || len(agents) != 1 || agents[0].Harness != "OpenCode" || !agents[0].Asks {
		t.Fatalf("one round, started with OpenCode: %d requests, %+v", fx.x.requestsFiled(), agents)
	}
	up := fx.updates(t, s)
	if len(up) != 1 || !strings.Contains(up[0], "tile agent #1 started in apps/eui: OpenCode, asks before acting.") || strings.Contains(up[0], "no longer covered") {
		t.Fatalf("what the agent was told: %q", up)
	}
	if b, _ := fx.ag.db.wsBrief(s.Run, 1); b.State != "started" || b.Edited {
		t.Fatalf("the brief: %+v (a coding agent picked is no edit)", b)
	}
}

// "add a computer player": the session from before is idle on eui — the
// follow-up continues it, one approval, no second session.
func TestWsEvalApprovalFollowUpContinues(t *testing.T) {
	fx, s := euiFix(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("tic tac toe")), callTools(tc("c1", "tile_agent_brief", tttBrief), tc("c2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("It starts once you allow it.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("Started.")).once()
	fx.say(t, s, "turn eui into a tic tac toe game")
	fx.idle(t, s)
	first := fx.x.pending(t)
	fx.x.decideWith(first.ID, wsChosen{Provider: "opencode", Mode: "plan"})
	fx.notify(t)
	fx.rest(t, s)
	row := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)[0]
	fx.x.setAgent(row.Session, func(a *wsAgent) { a.Status, a.Last = "idle", "The game is in." })
	fx.notify(t)
	fx.rest(t, s)

	f.on(forRun(s.Run, lastUser("computer player")), callTools(tc("c3", "tile_agent_brief", strings.Replace(tttBrief, "playable tic tac toe game", "computer player for the tic tac toe game", 1)),
		tc("c4", "tile_agent_start", `{"brief":2}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("It goes to the session that is there, once you allow it.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("The session took the follow-up.")).once()
	fx.say(t, s, "add a computer player to it")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "tile_agent_start")
	if !strings.Contains(got, "It continues tile agent #1 (OpenCode") || !strings.Contains(got, "no new session opens") {
		t.Fatalf("the follow-up's answer: %q", got)
	}
	second := fx.x.pending(t)
	if a := second.startBody(); a.Continue != row.Session {
		t.Fatalf("the follow-up names no session to continue: %s", second.call.Body)
	}
	// the sheet's primary action: continue (a client that knows it)
	fx.x.decideWith(second.ID, wsChosen{Provider: "opencode", Continue: row.Session})
	fx.notify(t)
	waitFor(t, "the follow-up's outcome", func() bool { return strings.Contains(fx.lastAssistant(t, s), "took the follow-up") })
	fx.idle(t, s)
	agents := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if len(agents) != 1 || fx.x.requestsFiled() != 2 || len(fx.x.said[row.Session]) != 2 || !strings.Contains(fx.x.said[row.Session][1], "computer player") {
		t.Fatalf("one session, two rounds in all: %d agents, %d requests, told %q", len(agents), fx.x.requestsFiled(), fx.x.said[row.Session])
	}
	up := fx.updates(t, s)
	if !strings.Contains(up[len(up)-1], "your person chose to continue tile agent #1 in apps/eui") {
		t.Fatalf("the follow-up's update: %q", up[len(up)-1])
	}
	// new: true, with a reason the person sees, files a new session
	f.on(forRun(s.Run, lastUser("separate")), callTools(tc("c5", "tile_agent_brief", strings.Replace(tttBrief, "playable tic tac toe game", "rules page", 1)),
		tc("c6", "tile_agent_start", `{"brief":3,"new":true,"reason":"a separate piece of work: the rules page"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("A new session, once you allow it.")).once()
	fx.x.setAgent(row.Session, func(a *wsAgent) { a.Status = "idle" })
	fx.notify(t)
	fx.rest(t, s)
	fx.say(t, s, "a separate rules page")
	fx.idle(t, s)
	third := fx.x.pending(t)
	if a := third.startBody(); a.Continue != "" || !strings.HasPrefix(third.summary, "A new session: a separate piece of work") {
		t.Fatalf("a new session asked for: %s / %q", third.call.Body, third.summary)
	}
}

// A five-line fix handed back to a session the person switched to a mode
// that acts without asking: ONE message approval, exactly that text, once
// — no new session.
func TestWsEvalApprovalMessageToNonAskingSession(t *testing.T) {
	fx, s := euiFix(t)
	_, sid := fx.seedAgent(t, s, "apps/eui")
	fx.x.setAgent(sid, func(a *wsAgent) { a.Status = "idle" })
	fx.x.mu.Lock()
	fx.x.modeOff = map[string]bool{sid: true}
	fx.x.mu.Unlock()
	fx.notify(t)
	fx.rest(t, s) // its turn's end is news that wakes the conversation: told before the scenario's own turns
	f := fakeOf(fx.ag)
	const fix = "After a Reset during the computer's pause, a stray O appears: cancel the pending move on Reset."
	in, _ := json.Marshal(map[string]string{"agent": "#1", "text": fix})
	f.on(forRun(s.Run, lastUser("stray O")), callTools(tc("m1", "tile_agent_message", string(in)))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_message")), say("It waits for your approval.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("The fix went to eui's agent.")).once()
	fx.say(t, s, "fix the stray O")
	fx.idle(t, s)
	req := fx.x.pending(t)
	if req.Kind != "message" || fx.x.requestsFiled() != 1 {
		t.Fatalf("the fix's request: %s, %d requests", req.Kind, fx.x.requestsFiled())
	}
	fx.x.decide(req.ID, "approved")
	fx.notify(t)
	waitFor(t, "the message's outcome", func() bool { return strings.Contains(fx.lastAssistant(t, s), "The fix went") })
	fx.idle(t, s)
	if said := fx.x.said[sid]; len(said) != 1 || said[0] != fix || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 1 {
		t.Fatalf("delivered: %q", said)
	}
	if up := fx.updates(t, s); !strings.Contains(up[len(up)-1], "your message went to tile agent #1") {
		t.Fatalf("the message's update: %q", up)
	}
	fx.notify(t)
	if len(fx.x.said[sid]) != 1 {
		t.Fatal("the approved message went twice")
	}
}

// The person edits the brief's text on its card before approving: one
// round — the edited text is what is approved and sent, and the agent is
// told so, with that text.
func TestWsEvalApprovalBriefEditedBeforeApproving(t *testing.T) {
	fx, s := euiFix(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("tic tac toe")), callTools(tc("c1", "tile_agent_brief", tttBrief), tc("c2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("It starts once you allow it.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("Started, with your edit.")).once()
	fx.say(t, s, "turn eui into a tic tac toe game")
	fx.idle(t, s)
	first := fx.x.pending(t)
	base := fmt.Sprintf("/ws/sessions/%d", s.Run)
	fx.json(t, fx.call(t, wsAlice, "PUT", base+"/briefs/1", map[string]any{"goal": "A tic tac toe game with a score line"}), 200, nil)
	second := fx.x.pending(t)
	if second.ID == first.ID || !strings.Contains(second.startBody().Prompt, "with a score line") {
		t.Fatalf("the edited brief's request: %s", second.call.Body)
	}
	fx.x.decide(second.ID, "approved")
	fx.notify(t)
	waitFor(t, "the start's outcome", func() bool { return strings.Contains(fx.lastAssistant(t, s), "with your edit") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	if len(up) != 1 || !strings.Contains(up[0], "Your person edited the brief before approving; what was sent:") || !strings.Contains(up[0], "with a score line") {
		t.Fatalf("what the agent was told: %q", up)
	}
	if n := len(f.callsFor(s.Run)); n != 3 {
		t.Fatalf("%d model calls, want 3: one round", n)
	}
}

// A start the person approved fails to open (the sandbox didn't start):
// the agent is told it is not its doing, files nothing new, and the
// person's Try again starts it.
func TestWsEvalApprovalFailedStartTriedAgain(t *testing.T) {
	fx, s := euiFix(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("tic tac toe")), callTools(tc("c1", "tile_agent_brief", tttBrief), tc("c2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("It starts once you allow it.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("It didn't start; you can try again from the card.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("Started on the second try.")).once()
	fx.say(t, s, "turn eui into a tic tac toe game")
	fx.idle(t, s)
	req := fx.x.pending(t)
	fx.x.mu.Lock()
	fx.x.apx.failNext = true
	fx.x.mu.Unlock()
	fx.x.decide(req.ID, "approved")
	fx.notify(t)
	waitFor(t, "the failure's outcome", func() bool { return strings.Contains(fx.lastAssistant(t, s), "try again from the card") })
	fx.idle(t, s)
	up := fx.updates(t, s)
	if len(up) != 1 || !strings.Contains(up[0], "the tile agent didn't start") || !strings.Contains(up[0], "This is not your doing and not a refusal") || strings.Contains(up[0], "no longer covered") {
		t.Fatalf("the failure's update: %q", up)
	}
	if r, _ := fx.ag.db.wsReq(1); r.State != wsReqRetry {
		t.Fatalf("the request's state: %s", r.State)
	}
	fx.x.retry(req.ID)
	fx.notify(t)
	waitFor(t, "the second try", func() bool { return strings.Contains(fx.lastAssistant(t, s), "second try") })
	fx.idle(t, s)
	if fx.x.requestsFiled() != 1 || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 1 {
		t.Fatalf("tried again: %d requests filed, %d tile agents", fx.x.requestsFiled(), len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)))
	}
}

// What is open on the tile is said at the start: the person's own session
// there — theirs, not the agent's to drive — and the person's own choice
// for the tile, which the agent reads instead of asking.
func TestWsEvalApprovalSaysWhatIsThere(t *testing.T) {
	fx, s := euiFix(t)
	var v wsTileView
	_ = json.Unmarshal([]byte(`{"sessions":[{"id":"p-1","by":"person","status":"idle","provider":"opencode","mode":"build","asks":false}]}`), &v)
	fx.x.mu.Lock()
	fx.x.access.views = map[string]*wsTileView{"apps/eui": &v}
	fx.x.mu.Unlock()
	fx.x.remember("apps/eui", wsChosenNow{Harness: "opencode", Mode: "plan", Asks: true})
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("tic tac toe")), callTools(tc("c1", "tile_agent_brief", tttBrief), tc("c2", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("Filed.")).once()
	fx.say(t, s, "turn eui into a tic tac toe game")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_agent_brief"); !strings.Contains(got, "It would run on OpenCode (your person's own choice for this tile)") {
		t.Fatalf("the brief's answer: %q", got)
	}
	if got := fx.toolResult(t, s, "tile_agent_start"); !strings.Contains(got, "your person's own session (opencode, acts without asking, idle) — not yours to drive") {
		t.Fatalf("the start's answer: %q", got)
	}
}
