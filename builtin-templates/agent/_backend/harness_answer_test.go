package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// parkOf waits for run's harness park of kind and returns it.
func parkOf(t *testing.T, ag *Agent, id int64, kind string) pendingState {
	t.Helper()
	var p pendingState
	hwait(t, "a "+kind+" park", func() bool {
		r, err := ag.db.getRun(id)
		if err != nil || r.Status != statusWaiting {
			return false
		}
		p = parsePending(r.Pending)
		return p.Kind == kind && p.Harness != nil
	})
	return p
}

// A permission is answered with one of the adapter's own options: an
// "always" one is remembered as a rule (stored for a successor), and the
// next matching request is allowed without a park; an edit's permission
// parks with its diff in ask mode and isn't asked at all in auto mode.
func TestHarnessPermissionOptions(t *testing.T) {
	ag, mux, box := harnessFixture(t, false, "--auto-mode")
	run := askHarness(t, mux, box, "perm")
	p := parkOf(t, ag, run.ID, "approval")
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"option": "nope"}); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "option: one of once, always, no") {
		t.Fatalf("an unknown option: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "feedback": "x"}); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "feedback goes with a rejection") {
		t.Fatalf("feedback with an allow: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"option": "always", "park": p.Park}); w.Code != 200 {
		t.Fatalf("approve always: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn after it", turnOver(ag, run.ID))
	hs, _ := ag.db.harnessSession(run.ID)
	var rules []acp.Rule
	if json.Unmarshal([]byte(hs.Rules), &rules) != nil || len(rules) != 1 || rules[0].Kind != "execute" || rules[0].Title != "run ls" {
		t.Fatalf("the rule: %q", hs.Rules)
	}
	evs := watchTree(t, ag, run.ID)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "perm again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the ruled turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Count(transcript(ag.db, run.ID), "A:listed") == 2
	})
	for _, ev := range evs.of(evRun) {
		if b, _ := json.Marshal(ev.Data); strings.Contains(string(b), `"status":"waiting_input"`) {
			t.Fatalf("a rule's request parked the run: %s", b)
		}
	}

	// perm-edit: an edit asks in ask mode (the diff on the card) …
	edit := askHarness(t, mux, box, "perm-edit")
	pe := parkOf(t, ag, edit.ID, "approval")
	if pe.Harness.Tool == nil || pe.Harness.Tool.Kind != "edit" || !strings.Contains(string(pe.Harness.Tool.Content), "hello.txt") {
		t.Fatalf("the edit's card: %+v", pe.Harness.Tool)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", edit.ID), map[string]any{"approve": true}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	hwait(t, "the edit", func() bool {
		return turnOver(ag, edit.ID)() && strings.Contains(fullText(ag.db, edit.ID), "edited hello.txt")
	})
	// … and not at all in auto mode (alice's setting)
	if w := callAs(t, mux, asAlice, "PUT", "/prefs/harness-mode/fake", map[string]any{"mode": "auto"}); w.Code != 200 {
		t.Fatalf("the setting: %d %s", w.Code, w.Body)
	}
	auto := askHarness(t, mux, box, "perm-edit")
	hwait(t, "the auto edit", func() bool {
		return turnOver(ag, auto.ID)() && strings.Contains(fullText(ag.db, auto.ID), "edited hello.txt")
	})
	if cfg, _ := ag.db.runConfig(auto.ID); cfg.Harness.Mode != "auto" {
		t.Fatalf("the auto conversation's mode: %q", cfg.Harness.Mode)
	}
}

// A rejection with feedback: the park answered reject_once, then the words
// are the caller's next message — the next prompt.
func TestHarnessRejectFeedback(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	p := parkOf(t, ag, run.ID, "approval")
	w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": false, "park": p.Park, "feedback": "use find instead"})
	if w.Code != 200 {
		t.Fatalf("reject: %d %s", w.Code, w.Body)
	}
	hwait(t, "the feedback's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: use find instead")
	})
	text := transcript(ag.db, run.ID)
	if !strings.Contains(text, "A:denied") || !strings.Contains(text, "U:use find instead") || strings.Index(text, "A:denied") > strings.Index(text, "U:use find instead") {
		t.Fatalf("the rejection, then the feedback: %s", text)
	}
}

// An option that raises the session to an explicit mode is the owner's:
// a participant gets 403, and approve:true never picks one (400 when every
// allow is explicit); the owner may.
func TestHarnessExplicitOption(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	p := parkOf(t, ag, run.ID, "approval")
	if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, 'carol', ?, 1)`, run.ID, roleParticipant); err != nil {
		t.Fatal(err)
	}
	ag.acl.flush(run.ID)
	for i := range p.Harness.Options {
		if strings.HasPrefix(p.Harness.Options[i].Kind, "allow") {
			p.Harness.Options[i].Explicit = true
		}
	}
	raw, _ := json.Marshal(p)
	if _, err := ag.db.q.Exec(`UPDATE runs SET pending=? WHERE id=?`, string(raw), run.ID); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/runs/%d/approve", run.ID)
	if w := callAs(t, mux, asCarol, "POST", path, map[string]any{"option": "once"}); w.Code != 403 || !strings.Contains(w.Body.String(), "only alice can allow Allow once") {
		t.Fatalf("carol's explicit option: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asCarol, "POST", path, map[string]any{"approve": true}); w.Code != 400 || !strings.Contains(w.Body.String(), "option: name one") {
		t.Fatalf("approve with only explicit allows: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"option": "once", "park": p.Park}); w.Code != 200 {
		t.Fatalf("alice's explicit option: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
}

// At most one park: a second request while one is parked waits in the
// queue and becomes the park once the first is answered.
func TestHarnessQueuedPark(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	first := parkOf(t, ag, run.ID, "approval")
	s := ag.eng.harnessOf(run.ID)
	// a second request the adapter sends meanwhile (the fake asks one at a time)
	second := acp.Pending{PID: "p99", ToolCall: acp.ToolCallRef{ID: "t9", Title: "rm -rf build", Kind: "delete"},
		Options: []acp.PermissionOption{{OptionID: "y", Name: "Yes", Kind: acp.AllowOnce}, {OptionID: "n", Name: "No", Kind: acp.RejectOnce}}}
	s.perms.Restore(second, json.RawMessage(`"x9"`))
	ev := acp.NewEvent(acp.EvPermissionRequest, map[string]any{"pid": "p99", "toolCall": second.ToolCall, "options": second.Options,
		"rule": map[string]any{"kind": "delete", "title": "rm -rf build"}})
	ev.Wire = &acp.Wire{RPCID: json.RawMessage(`"x9"`)}
	s.apply(ev)
	if p := parsePending(mustRun(t, ag, run.ID).Pending); p.Park != first.Park {
		t.Fatalf("the second request took the park: %+v", p)
	}
	if hs, _ := ag.db.harnessSession(run.ID); !strings.Contains(hs.Queue, "p99") {
		t.Fatalf("the queue: %q", hs.Queue)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": true, "park": first.Park}); w.Code != 200 {
		t.Fatalf("approve: %d %s", w.Code, w.Body)
	}
	var next pendingState
	hwait(t, "the queued request's park", func() bool {
		next = parsePending(mustRun(t, ag, run.ID).Pending)
		return next.Harness != nil && next.Park != first.Park
	})
	if next.Kind != "approval" || next.Park == first.Park || next.Harness.PID != "p99" || next.Harness.Tool.Title != "rm -rf build" {
		t.Fatalf("the queued park: %+v", next.Harness)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/approve", run.ID), map[string]any{"approve": false, "park": next.Park}); w.Code != 200 {
		t.Fatalf("deny: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "listed") })
	if hs, _ := ag.db.harnessSession(run.ID); hs.Queue != "" {
		t.Fatalf("the queue after: %q", hs.Queue)
	}
}

// A form question parks as question (its schema on the card); an hanswer
// row answers it. A message while a question waits declines it first.
func TestHarnessQuestion(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "ask")
	p := parkOf(t, ag, run.ID, "question")
	if p.Harness.EID == "" || !strings.Contains(string(p.Harness.Schema), "question_0") || p.Harness.CallID != "h1:ask1" {
		t.Fatalf("the question: %+v", p.Harness)
	}
	if _, _, err := ag.queue(run.ID, inboxHAnswer, inboxBody{Park: p.Park, Action: "accept", Sender: "alice",
		Content: json.RawMessage(`{"question_0":"Postgres"}`)}, ""); err != nil {
		t.Fatal(err)
	}
	hwait(t, "the answer", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), `answers: {"question_0":"Postgres"}`)
	})

	// a message instead of an answer: declined, then the next prompt
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "ask again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	parkOf(t, ag, run.ID, "question")
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo skip it"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "declined, then the message", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo skip it")
	})
	if text := fullText(ag.db, run.ID); !strings.Contains(text, "skipped") {
		t.Fatalf("the question wasn't declined: %s", transcript(ag.db, run.ID))
	}
}

// A message while a permission waits rejects it (reject_once) first, then
// goes as the next prompt (this adapter doesn't steer).
func TestHarnessMessageWhileParked(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	parkOf(t, ag, run.ID, "approval")
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo never mind"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the message's turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo never mind")
	})
	text := transcript(ag.db, run.ID)
	if !strings.Contains(text, "A:denied") || strings.Index(text, "A:denied") > strings.Index(text, "U:echo never mind") {
		t.Fatalf("rejected first: %s", text)
	}
}

// Steering: with an adapter that steers, a message during the turn joins
// it (its user row at once, the turn reporting it); promptRequired (the
// turn ended as it was sent) leaves it for the next prompt. Without
// steering it waits for the turn's end and is the next prompt.
func TestHarnessSteer(t *testing.T) {
	t.Run("injected", func(t *testing.T) {
		ag, mux, box := harnessFixture(t, false, "--steer")
		run := askHarness(t, mux, box, "steer")
		hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "use tabs"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		hwait(t, "the steer's user row", func() bool { return strings.Contains(transcript(ag.db, run.ID), "U:use tabs") })
		if statusOf(ag.db, run.ID) != statusRunning {
			t.Fatal("the steer ended the turn")
		}
		hwait(t, "the turn", turnOver(ag, run.ID))
		text := fullText(ag.db, run.ID)
		if !strings.Contains(text, "steers: use tabs") || strings.Contains(text, "echo: use tabs") {
			t.Fatalf("the steered turn: %s", transcript(ag.db, run.ID))
		}
		if n := len(ag.db.undelivered(run.ID)); n != 0 {
			t.Fatalf("%d rows left", n)
		}
		if asks, _ := ag.db.asks(run.ID); len(asks) != 2 {
			t.Fatalf("the ledger: %+v", asks)
		}
		// promptRequired: the client has no turn although the record says one
		// is on its way — the message waits, then goes as a prompt
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET prompt_state='sent' WHERE run_id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo late"}); w.Code != 200 {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		time.Sleep(300 * time.Millisecond)
		if n := len(ag.db.undelivered(run.ID)); n != 1 || strings.Contains(fullText(ag.db, run.ID), "echo late") {
			t.Fatalf("promptRequired delivered it: %d rows, %s", n, transcript(ag.db, run.ID))
		}
		if _, err := ag.db.q.Exec(`UPDATE harness_sessions SET prompt_state='' WHERE run_id=?`, run.ID); err != nil {
			t.Fatal(err)
		}
		ag.eng.Poke(run.ID)
		hwait(t, "the prompt", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo late")
		})
	})
	t.Run("queued", func(t *testing.T) {
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "slow")
		hwait(t, "ticking", func() bool { return strings.Contains(draftText(ag.eng, run.ID), "tick 1") })
		w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo later"})
		var ans struct{ Queued bool }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &ans) != nil || !ans.Queued {
			t.Fatalf("message: %d %s", w.Code, w.Body)
		}
		time.Sleep(200 * time.Millisecond)
		if strings.Contains(transcript(ag.db, run.ID), "U:echo later") {
			t.Fatal("delivered mid-turn without steering")
		}
		hwait(t, "the next turn", func() bool {
			return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo later")
		})
		text := transcript(ag.db, run.ID)
		if strings.Index(text, "tick 9") > strings.Index(text, "U:echo later") {
			t.Fatalf("it went before the turn's end: %s", text)
		}
	})
}

// An interrupt while a permission waits: the park settles "(interrupted)",
// session/cancel ends the turn (interrupted), the run rests.
func TestHarnessInterruptParked(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "perm")
	parkOf(t, ag, run.ID, "approval")
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/interrupt", run.ID), nil); w.Code != 200 {
		t.Fatalf("interrupt: %d %s", w.Code, w.Body)
	}
	hwait(t, "the interrupted turn", turnOver(ag, run.ID))
	r := mustRun(t, ag, run.ID)
	if r.Status != statusIdle || r.Pending != "" || !strings.Contains(transcript(ag.db, run.ID), "T:(interrupted)") {
		t.Fatalf("after the interrupt: %s %q %s", r.Status, r.Pending, transcript(ag.db, run.ID))
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo on"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo on")
	})
}

// /compact is sent to an adapter that advertises it (as a prompt); a
// schedule's message isn't a coding agent's to take — consumed with a note.
func TestHarnessCompactAndStray(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	hwait(t, "the commands", func() bool {
		hs, _ := ag.db.harnessSession(run.ID)
		return hs != nil && strings.Contains(hs.Snapshot, `"compact"`)
	})
	if _, _, err := ag.queue(run.ID, inboxCompact, inboxBody{}, ""); err != nil {
		t.Fatal(err)
	}
	hwait(t, "/compact", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: /compact")
	})
	if _, _, err := ag.queue(run.ID, inboxUser, inboxBody{Text: "daily report", Source: "schedule"}, ""); err != nil {
		t.Fatal(err)
	}
	hwait(t, "the stray row consumed", func() bool { return len(ag.db.undelivered(run.ID)) == 0 })
	if strings.Contains(fullText(ag.db, run.ID), "daily report") {
		t.Fatal("a schedule drove the coding agent")
	}
}

// codex's detached turn (a steer answered startedNewTurn, whose end no
// session/prompt reports): the run follows it as working and ends it once
// the adapter goes quiet, its text the answer.
func TestHarnessDetachedTurn(t *testing.T) {
	old := hDetachedQuiet
	hDetachedQuiet = 300 * time.Millisecond
	t.Cleanup(func() { hDetachedQuiet = old })
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo one")
	hwait(t, "the turn", turnOver(ag, run.ID))
	s := ag.eng.harnessOf(run.ID)
	if _, err := ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, run.ID); err != nil {
		t.Fatal(err)
	}
	s.followDetached()
	s.apply(acp.NewEvent(acp.EvMessageDelta, map[string]any{"role": "agent", "text": "on my own"}))
	if statusOf(ag.db, run.ID) != statusRunning {
		t.Fatal("the detached turn isn't followed")
	}
	hwait(t, "the quiet end", func() bool { return statusOf(ag.db, run.ID) == statusIdle })
	if !strings.Contains(transcript(ag.db, run.ID), "A:on my own") || s.isDetached() {
		t.Fatalf("the detached turn's answer: %s", transcript(ag.db, run.ID))
	}
}
