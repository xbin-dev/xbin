package main

import (
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
	// And the wire shape: results directly follow their call.
	var wire []wireMsg
	for _, m := range msgs {
		if m.Role == "system" {
			continue
		}
		wm := wireMsg{Role: m.Role, ToolCallID: m.ToolCallID}
		if m.ToolCalls != "" {
			_ = json.Unmarshal([]byte(m.ToolCalls), &wm.ToolCalls)
		}
		wire = append(wire, wm)
	}
	if err := validateWire(wire); err != nil {
		t.Fatalf("transcript is not wire-valid: %v", err)
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

func placeholders(t *testing.T, db *DB, runID int64, calls []toolCall) {
	t.Helper()
	for _, c := range calls {
		if _, err := db.addMessage(&Message{RunID: runID, Role: "tool", Name: c.Function.Name, ToolCallID: c.ID, Content: toolRunning}); err != nil {
			t.Fatal(err)
		}
	}
}

func settle(t *testing.T, db *DB, runID int64, c toolCall, content string) {
	t.Helper()
	if ok, err := db.casToolResult(runID, c.ID, content); err != nil || !ok {
		t.Fatalf("settling %s: ok=%v err=%v", c.ID, ok, err)
	}
}

// TestRepairHealsCrashedBatch: a block with no results (a process that died
// between the call and its results) is answered, once.
func TestRepairHealsCrashedBatch(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	addAssistantCalls(t, db, id, call("a", "web_search"), call("b", "web_fetch"))
	if n := ag.eng.repairTranscript(id); n != 2 {
		t.Fatalf("expected 2 repairs, got %d", n)
	}
	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	if !strings.Contains(msgs[len(msgs)-1].Content, "restarted") {
		t.Fatalf("repair should say why the result is missing, got %q", msgs[len(msgs)-1].Content)
	}
	if n := ag.eng.repairTranscript(id); n != 0 {
		t.Fatalf("repair is not idempotent: second pass fixed %d", n)
	}
	assertTranscriptValid(t, db, id)
}

// TestRepairSettlesStalePlaceholders: a placeholder a dead process left
// "running" must not tell the model a tool is still executing.
func TestRepairSettlesStalePlaceholders(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "web_search"), call("b", "web_fetch")}
	addAssistantCalls(t, db, id, calls...)
	placeholders(t, db, id, calls)
	settle(t, db, id, calls[0], "finished fine")

	if n := ag.eng.repairTranscript(id); n != 1 {
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

// TestRepairSplicesInOrder: a missing result goes right after its call, not
// behind whatever the run did next.
func TestRepairSplicesInOrder(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "t1"), call("b", "t2")}
	addAssistantCalls(t, db, id, calls...)
	_, _ = db.addMessage(&Message{RunID: id, Role: "tool", Name: "t1", ToolCallID: "a", Content: "first result"})
	if _, err := db.addMessage(&Message{RunID: id, Role: "user", Content: "carry on"}); err != nil {
		t.Fatal(err)
	}
	if n := ag.eng.repairTranscript(id); n == 0 {
		t.Fatal("expected a repair")
	}
	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	if got := strings.Join(roles, ","); got != "assistant,tool,tool,user" {
		t.Fatalf("repair spliced in the wrong place: %s", got)
	}
}

// TestRepairMovesAWedgedUserMessage is the corruption the old engine could
// write (a message landing between a call and its result) — which made every
// later request fail until the transcript was edited by hand.
func TestRepairMovesAWedgedUserMessage(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)

	calls := []toolCall{call("a", "t1"), call("b", "t2")}
	addAssistantCalls(t, db, id, calls...)
	_, _ = db.addMessage(&Message{RunID: id, Role: "user", Content: "are you there?"})
	_, _ = db.addMessage(&Message{RunID: id, Role: "tool", Name: "t1", ToolCallID: "a", Content: "A"})
	_, _ = db.addMessage(&Message{RunID: id, Role: "tool", Name: "t2", ToolCallID: "b", Content: "B"})
	_, _ = db.addMessage(&Message{RunID: id, Role: "tool", Name: "t2", ToolCallID: "b", Content: "(no result: the backend restarted while this tool was running)"})

	ag.eng.repairTranscript(id)
	assertTranscriptValid(t, db, id)
	if got := transcript(db, id); !strings.HasPrefix(got, "A:[t1][t2] | T:A | T:B | U:are you there?") {
		t.Fatalf("canonical order = %s", got)
	}
	if n := ag.eng.repairTranscript(id); n != 0 {
		t.Fatalf("second repair fixed %d", n)
	}
}

// TestSettleToolResultKeepsCallOrder: tools finish out of order; the
// transcript must not.
func TestSettleToolResultKeepsCallOrder(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("t", "", 0)
	calls := []toolCall{call("a", "slow"), call("b", "fast"), call("c", "mid")}
	addAssistantCalls(t, db, id, calls...)
	placeholders(t, db, id, calls)
	settle(t, db, id, calls[1], "B")
	settle(t, db, id, calls[2], "C")
	settle(t, db, id, calls[0], "A")
	msgs, _ := db.messages(id, true)
	if got := msgs[1].Content + msgs[2].Content + msgs[3].Content; got != "ABC" {
		t.Fatalf("results are in completion order, not call order: %s", got)
	}
	assertTranscriptValid(t, db, id)
}

// TestSettledResultIsNeverOverwritten: a late writer (a tool from an
// interrupted step, a subagent result after its wait moved on) cannot replace
// what the model already saw.
func TestSettledResultIsNeverOverwritten(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("t", "", 0)
	c := call("a", "slow")
	addAssistantCalls(t, db, id, c)
	placeholders(t, db, id, []toolCall{c})
	settle(t, db, id, c, "(interrupted by the owner)")
	if ok, _ := db.casToolResult(id, "a", "late result"); ok {
		t.Fatal("a settled result was overwritten")
	}
}

// TestUpdateToolResultRefreshesSearch: recall must find the real result, not
// the placeholder.
func TestUpdateToolResultRefreshesSearch(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("t", "", 0)
	c := call("a", "web_search")
	addAssistantCalls(t, db, id, c)
	placeholders(t, db, id, []toolCall{c})
	settle(t, db, id, c, "pangolins are nocturnal")
	hits, err := db.searchMessages(id, "pangolins", 5)
	if err != nil || len(hits) == 0 {
		t.Fatalf("the settled result is not searchable (err=%v)", err)
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
	if _, err := db.replPutFile(child, "a.js", "x", 0); err != nil {
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
	_ = db.sql.QueryRow(`SELECT count(*) FROM repl_files WHERE run_id=?`, child).Scan(&n)
	if n != 0 {
		t.Fatalf("child left %d session file(s) behind", n)
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

// --- approval gate (through the engine) -----------------------------------------

// approvalRun starts a run whose first step calls a harmless tool and one that
// reaches outside the agent (xbin_call) with approval on: it parks.
func approvalRun(t *testing.T, ag *Agent) int64 {
	t.Helper()
	note := tc("n1", "note", `{"text":"about to call out"}`)
	out := tc("x1", "xbin_call", `{"method":"GET","path":"/api/apps/nowhere/x"}`)
	f := fakeOf(ag)
	f.on(lastUser("reach out"), callTools(note, out)).once()
	f.on(lastIs("tool", ""), say("done"))
	id := newRun(t, ag, Config{Approve: true}, "reach out")
	waitStatus(t, ag.db, id, statusWaiting)
	return id
}

func TestParkedApprovalKeepsTheTranscriptValid(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id := approvalRun(t, ag)
	assertTranscriptValid(t, db, id)
	if n := ag.eng.repairTranscript(id); n != 0 {
		t.Fatalf("repair rewrote %d call(s) parked for approval", n)
	}
}

func TestApproveRunsEachCallOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id := approvalRun(t, ag)
	if w := serve(handleApprove, "POST", "/runs/x/approve", id, []byte(`{"approve":true}`), "application/json"); w.Code != 200 {
		t.Fatalf("approve = %d", w.Code)
	}
	waitStatus(t, db, id, statusIdle)
	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	for _, m := range msgs {
		if m.Role == "tool" && (isPlaceholder(m.Content) || m.Content == toolLostToRestart) {
			t.Fatalf("an approved call was left with %q", m.Content)
		}
	}
}

func TestDenyAnswersEachCallOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id := approvalRun(t, ag)
	serve(handleApprove, "POST", "/runs/x/approve", id, []byte(`{"approve":false}`), "application/json")
	waitStatus(t, db, id, statusIdle)
	assertTranscriptValid(t, db, id)
	msgs, _ := db.messages(id, true)
	denied := 0
	for _, m := range msgs {
		if m.Role == "tool" && strings.Contains(m.Content, "denied") {
			denied++
		}
	}
	if denied != 2 {
		t.Fatalf("expected both parked calls denied, got %d", denied)
	}
}

// Replying instead of approving denies the parked calls, then the reply is
// answered — each call answered exactly once.
func TestReplyingWhileParkedDeniesThenAnswers(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id := approvalRun(t, ag)
	fakeOf(ag).on(lastUser("never mind"), say("fine"))
	send(t, ag, id, "never mind")
	waitFor(t, "the reply to be answered", func() bool { return strings.Contains(transcript(db, id), "A:fine") })
	assertTranscriptValid(t, db, id)
	if got := transcript(db, id); !strings.Contains(got, "replied instead of") {
		t.Fatalf("parked calls were not denied: %s", got)
	}
}
