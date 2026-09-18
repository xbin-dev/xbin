package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// assertTranscriptValid enforces the provider's contract: every assistant
// tool_calls block is answered, exactly once per call id, by tool messages that
// follow it and nothing else. This is the invariant the whole placeholder
// design exists to preserve, so it gets one helper used by every test here.
func assertTranscriptValid(t *testing.T, db *DB, runID int64) {
	t.Helper()
	msgs, err := db.messages(runID, true)
	if err != nil {
		t.Fatal(err)
	}
	answered := map[string]int{}
	declared := map[string]bool{}
	for i, m := range msgs {
		if m.Role == "tool" {
			if m.ToolCallID == "" {
				t.Fatalf("msg %d: tool result with no tool_call_id", i)
			}
			answered[m.ToolCallID]++
			continue
		}
		if m.Role != "assistant" || m.ToolCalls == "" {
			continue
		}
		var calls []toolCall
		if err := json.Unmarshal([]byte(m.ToolCalls), &calls); err != nil {
			t.Fatalf("msg %d: bad tool_calls json: %v", i, err)
		}
		for _, c := range calls {
			declared[c.ID] = true
		}
	}
	for id := range declared {
		switch answered[id] {
		case 1: // good
		case 0:
			t.Fatalf("tool call %q was never answered — this request would be rejected by the provider", id)
		default:
			t.Fatalf("tool call %q answered %d times", id, answered[id])
		}
	}
	for id := range answered {
		if !declared[id] {
			t.Fatalf("orphaned tool result for %q (no assistant call declares it)", id)
		}
	}
	// Seq must be strictly increasing, or the repair's splice corrupted order.
	for i := 1; i < len(msgs); i++ {
		if msgs[i].Seq <= msgs[i-1].Seq {
			t.Fatalf("seq not increasing at %d: %d after %d", i, msgs[i].Seq, msgs[i-1].Seq)
		}
	}
}

func call(id, name string) toolCall {
	tc := toolCall{ID: id, Type: "function"}
	tc.Function.Name = name
	tc.Function.Arguments = "{}"
	return tc
}

func addAssistantCalls(t *testing.T, db *DB, runID int64, calls ...toolCall) {
	t.Helper()
	b, _ := json.Marshal(calls)
	if _, err := db.addMessage(&Message{RunID: runID, Role: "assistant", ToolCalls: string(b)}); err != nil {
		t.Fatal(err)
	}
}

// TestRepairHealsCrashedBatch is the core of phase 1. The loop persists the
// assistant tool_calls message before running the tools, so a save/swap between
// those two writes leaves an unanswered block in the LIVE database — which a
// write-ordering fix alone can never clean up.
func TestRepairHealsCrashedBatch(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	addAssistantCalls(t, db, id, call("a", "web_search"), call("b", "web_fetch"))
	// ...process dies here: no results were ever written.

	if n := ag.repairTranscript(id); n != 2 {
		t.Fatalf("expected 2 repairs, got %d", n)
	}
	assertTranscriptValid(t, db, id)

	msgs, _ := db.messages(id, true)
	if !strings.Contains(msgs[len(msgs)-1].Content, "restarted") {
		t.Fatalf("repair should say why the result is missing, got %q", msgs[len(msgs)-1].Content)
	}
	// Idempotent: a second pass must not add duplicates.
	if n := ag.repairTranscript(id); n != 0 {
		t.Fatalf("repair is not idempotent: second pass fixed %d", n)
	}
	assertTranscriptValid(t, db, id)
}

// TestRepairSettlesStalePlaceholders covers the other half: the process died
// AFTER the placeholders were written but before the tools finished. Leaving
// "(running…)" in context would tell the model a tool is still executing.
func TestRepairSettlesStalePlaceholders(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "web_search"), call("b", "web_fetch")}
	addAssistantCalls(t, db, id, calls...)
	ag.placeholderResults(id, calls)
	ag.settleToolResult(id, calls[0], "finished fine") // only the first completed

	if n := ag.repairTranscript(id); n != 1 {
		t.Fatalf("expected 1 stale placeholder settled, got %d", n)
	}
	assertTranscriptValid(t, db, id)

	msgs, _ := db.messages(id, true)
	for _, m := range msgs {
		if m.Content == toolRunning {
			t.Fatal("a placeholder survived the repair")
		}
	}
	if msgs[1].Content != "finished fine" {
		t.Fatalf("a completed result was clobbered: %q", msgs[1].Content)
	}
}

// TestRepairSplicesInOrder — appending repairs at the end would leave them
// behind whatever the run did next, which is its own protocol violation.
func TestRepairSplicesInOrder(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "t1"), call("b", "t2")}
	addAssistantCalls(t, db, id, calls...)
	ag.addToolResult(id, calls[0], "first result") // only "a" answered
	// The run carried on regardless — now "b" is missing in the MIDDLE.
	if _, err := db.addMessage(&Message{RunID: id, Role: "user", Content: "carry on"}); err != nil {
		t.Fatal(err)
	}

	if n := ag.repairTranscript(id); n != 1 {
		t.Fatalf("expected 1 repair, got %d", n)
	}
	assertTranscriptValid(t, db, id)

	msgs, _ := db.messages(id, true)
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	want := "assistant,tool,tool,user"
	if got := strings.Join(roles, ","); got != want {
		t.Fatalf("repair spliced in the wrong place: %s (want %s)", got, want)
	}
}

// TestSettleToolResultKeepsCallOrder — tools finish out of order; the
// transcript must not.
func TestSettleToolResultKeepsCallOrder(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "slow"), call("b", "fast"), call("c", "mid")}
	addAssistantCalls(t, db, id, calls...)
	ag.placeholderResults(id, calls)
	ag.settleToolResult(id, calls[1], "B")
	ag.settleToolResult(id, calls[2], "C")
	ag.settleToolResult(id, calls[0], "A")

	msgs, _ := db.messages(id, true)
	got := []string{msgs[1].Content, msgs[2].Content, msgs[3].Content}
	if strings.Join(got, "") != "ABC" {
		t.Fatalf("results are in completion order, not call order: %v", got)
	}
	assertTranscriptValid(t, db, id)
}

// TestUpdateToolResultRefreshesSearch — recall searches messages_fts, so a
// placeholder left in the index would be findable while the real result is not.
func TestUpdateToolResultRefreshesSearch(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	c := call("a", "web_search")
	addAssistantCalls(t, db, id, c)
	ag.placeholderResults(id, []toolCall{c})
	ag.settleToolResult(id, c, "pangolins are nocturnal")

	hits, err := db.searchMessages(id, "pangolins", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("the settled result is not searchable — the FTS row still holds the placeholder")
	}
}

// TestDenyPendingKeepsTranscriptValid — handleMessage clears `pending`, so a
// user who replies instead of approving would otherwise orphan the parked calls.
func TestDenyPendingKeepsTranscriptValid(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "xbin_call")}
	addAssistantCalls(t, db, id, calls...)
	pend, _ := json.Marshal(pending{Kind: "approval", ToolCalls: calls})
	if err := db.setStatus(id, statusWaiting, 0, "approve?", string(pend)); err != nil {
		t.Fatal(err)
	}

	ag.denyPending(id)                          // the user typed a reply
	_ = db.setStatus(id, statusIdle, 0, "", "") // …and handleMessage clears pending
	assertTranscriptValid(t, db, id)

	// And it is a no-op when nothing is parked.
	before, _ := db.messages(id, true)
	ag.denyPending(id)
	after, _ := db.messages(id, true)
	if len(before) != len(after) {
		t.Fatalf("denyPending should be a no-op with no pending: %d → %d", len(before), len(after))
	}
}

// TestDeleteRunCascadesToSubagents — a subagent run is only meaningful as part
// of its parent's work, and deleteRun enumerates its tables by hand.
func TestDeleteRunCascadesToSubagents(t *testing.T) {
	db := newTestDB(t)
	parent, _ := db.createRun("parent", "", 0)
	child, _ := db.createRun("child", "", parent)
	grand, _ := db.createRun("grandchild", "", child)
	other, _ := db.createRun("unrelated", "", 0)

	if _, err := db.addMessage(&Message{RunID: grand, Role: "user", Content: "hi"}); err != nil {
		t.Fatal(err)
	}

	if err := db.deleteRun(parent); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{parent, child, grand} {
		if _, err := db.getRun(id); err == nil {
			t.Fatalf("run %d survived the cascade", id)
		}
	}
	var n int
	_ = db.sql.QueryRow(`SELECT count(*) FROM messages WHERE run_id=?`, grand).Scan(&n)
	if n != 0 {
		t.Fatalf("grandchild left %d message(s) behind", n)
	}
	if _, err := db.getRun(other); err != nil {
		t.Fatal("an unrelated run was deleted")
	}
}

// TestSweepRemovesPreCascadeOrphans — deleting a run did not cascade before the
// cascade shipped, so existing databases hold subagent rows pointing at a
// parent that is gone. Nothing will ever reach them: no parent lists them and
// no cascade will. They are unreachable rows left by a deletion the owner
// already asked for.
func TestSweepRemovesPreCascadeOrphans(t *testing.T) {
	db := newTestDB(t)
	parent, _ := db.createRun("parent", "", 0)
	child, _ := db.createRun("child", "", parent)
	grand, _ := db.createRun("grand", "", child)
	keep, _ := db.createRun("unrelated top-level", "", 0)
	liveParent, _ := db.createRun("live parent", "", 0)
	liveKid, _ := db.createRun("live kid", "", liveParent)

	// Simulate the old bug: remove ONLY the parent row, leaving its subtree.
	if err := db.deleteOneRun(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := db.getRun(child); err != nil {
		t.Fatal("setup: the child should still be there before the sweep")
	}

	if n := db.sweepOrphanRuns(); n != 1 {
		t.Fatalf("expected 1 orphaned subtree swept, got %d", n)
	}
	for _, id := range []int64{child, grand} {
		if _, err := db.getRun(id); err == nil {
			t.Fatalf("run %d survived the sweep", id)
		}
	}
	// Nothing reachable is touched.
	for _, id := range []int64{keep, liveParent, liveKid} {
		if _, err := db.getRun(id); err != nil {
			t.Fatalf("run %d was swept but its parent still exists", id)
		}
	}
	// Idempotent.
	if n := db.sweepOrphanRuns(); n != 0 {
		t.Fatalf("second sweep removed %d more", n)
	}
}

// TestDeleteCascadeIsWhatTheOwnerAsksFor — the sweep is cleanup for old rows;
// this is the behaviour going forward.
func TestDeleteCascadeIsWhatTheOwnerAsksFor(t *testing.T) {
	db := newTestDB(t)
	parent, _ := db.createRun("parent", "", 0)
	a, _ := db.createRun("a", "", parent)
	b, _ := db.createRun("b", "", parent)
	deep, _ := db.createRun("deep", "", a)

	if err := db.deleteRun(parent); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{parent, a, b, deep} {
		if _, err := db.getRun(id); err == nil {
			t.Fatalf("run %d survived deleting its root", id)
		}
	}
	// And no orphan is left for the sweep to find.
	if n := db.sweepOrphanRuns(); n != 0 {
		t.Fatalf("the cascade left %d orphan(s) behind", n)
	}
}

// --- approval gate ------------------------------------------------------
//
// The approval gate parks a turn before running anything. Before this was
// fixed it parked WITHOUT answering the calls, so: the transcript was invalid
// for as long as the run waited, and on approve, drive's repairTranscript ran
// before the resume path, filled every parked call with "backend restarted",
// and then the resume path added its own placeholders on top — every approved
// call ended up answered twice, which the provider rejects.

// parkForApproval sets up a turn the gate parks: a harmless call plus one that
// reaches outside the agent (xbin_call), which is what triggers the gate.
func parkForApproval(t *testing.T, ag *Agent, db *DB) (int64, []toolCall) {
	t.Helper()
	id, _ := db.createRun("needs approval", "", 0)
	note := call("n1", "note")
	note.Function.Arguments = `{"text":"about to call out"}`
	out := call("x1", "xbin_call")
	out.Function.Arguments = `{"method":"GET","path":"/api/apps/nowhere/x"}`
	calls := []toolCall{note, out}
	addAssistantCalls(t, db, id, calls...)
	run, _ := db.getRun(id)
	parked, _ := ag.executeToolCalls(context.Background(), run, Config{Approve: true}, calls, false)
	if !parked {
		t.Fatal("setup: a side-effecting call with approval on should park the turn")
	}
	return id, calls
}

func TestParkedApprovalKeepsTheTranscriptValid(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := parkForApproval(t, ag, db)
	// Anything that assembles context while the run waits — an interjection, a
	// /resume, a restart — would otherwise ship an unanswered block.
	assertTranscriptValid(t, db, id)
}

func TestApproveThenResumeAnswersEachCallOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := parkForApproval(t, ag, db)
	run, _ := db.getRun(id)

	// What handleApprove does, then what drive does before its resume path.
	if err := db.setStatus(id, statusRunning, 0, "", run.Pending); err != nil {
		t.Fatal(err)
	}
	ag.repairTranscript(id)
	var pend pending
	if err := json.Unmarshal([]byte(run.Pending), &pend); err != nil {
		t.Fatal(err)
	}
	run, _ = db.getRun(id)
	ag.executeToolCalls(context.Background(), run, Config{Approve: true}, pend.ToolCalls, true)

	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	for _, m := range msgs {
		if m.Role == "tool" && (m.Content == toolAwaitingApproval || m.Content == toolLostToRestart) {
			t.Fatalf("an approved call was left with %q instead of its result", m.Content)
		}
	}
}

func TestResumeWhileParkedThenApprove(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := parkForApproval(t, ag, db)
	// A /resume on a run parked for approval drives it: drive repairs first,
	// then returns on the waiting status. That repair must not touch the
	// parked calls, or approving afterwards doubles them up.
	if n := ag.repairTranscript(id); n != 0 {
		t.Fatalf("repair rewrote %d parked call(s) that were waiting for approval, not lost", n)
	}
	run, _ := db.getRun(id)
	var pend pending
	_ = json.Unmarshal([]byte(run.Pending), &pend)
	_ = db.setStatus(id, statusRunning, 0, "", run.Pending)
	run, _ = db.getRun(id)
	ag.executeToolCalls(context.Background(), run, Config{Approve: true}, pend.ToolCalls, true)
	assertTranscriptValid(t, db, id)
}

func TestDenyApprovalAnswersEachCallOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := parkForApproval(t, ag, db)
	run, _ := db.getRun(id)
	var pend pending
	_ = json.Unmarshal([]byte(run.Pending), &pend)

	ag.denyApproval(id, pend)
	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	var denied int
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "denied") {
			denied++
		}
	}
	if denied != 2 {
		t.Fatalf("expected both parked calls denied, got %d", denied)
	}
}

func TestInterjectingWhileParkedAnswersEachCallOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := parkForApproval(t, ag, db)
	ag.denyPending(id) // the user typed a reply instead of approving
	_ = db.setStatus(id, statusIdle, 0, "", "")
	assertTranscriptValid(t, db, id)
}
