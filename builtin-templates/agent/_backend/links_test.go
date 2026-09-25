package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func children(db *DB, parent int64) []*Run {
	rs, _ := db.queryRuns(`WHERE parent_id=? ORDER BY id`, parent)
	return rs
}

func childByTask(db *DB, parent int64, task string) *Run {
	for _, r := range children(db, parent) {
		msgs, _ := db.messages(r.ID, false)
		for _, m := range msgs {
			if m.Role == "user" && strings.Contains(m.Content, task) {
				return r
			}
		}
	}
	return nil
}

func taskIs(s string) func(LLMRequest) bool {
	return func(r LLMRequest) bool {
		if !isSubagent(r) {
			return false
		}
		for _, m := range r.Msgs {
			if m.Role == "user" {
				return strings.Contains(asString(m.Content), s)
			}
		}
		return false
	}
}

// Two foreground subagents finishing out of order: both results land in their
// placeholders in CALL order, and the parent makes ONE call with both.
func TestForegroundResultsInCallOrderOneParentCall(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("slow job"), say("SLOW")).block("slow")
	f.on(taskIs("fast job"), say("FAST"))
	f.on(lastUser("delegate"), callTools(
		tc("a", "subagent_spawn", `{"task":"slow job"}`), tc("b", "subagent_spawn", `{"task":"fast job"}`))).once()
	f.on(lastIs("tool", ""), say("both back"))
	id := newRun(t, ag, Config{Subagents: true}, "delegate")
	waitStatus(t, db, id, statusAwait)
	fast := childByTask(db, id, "fast job")
	waitFor(t, "the fast child", func() bool { return fast != nil && statusOf(db, fast.ID) == statusIdle })
	if statusOf(db, id) != statusAwait {
		t.Fatalf("the parent resumed with one of two results (%s)", statusOf(db, id))
	}
	f.release("slow")
	waitStatus(t, db, id, statusIdle)
	got := fullText(db, id)
	if !strings.Contains(got, "--- #") || strings.Index(got, "SLOW") > strings.Index(got, "FAST") {
		t.Fatalf("results not in call order: %s", got)
	}
	if n := len(f.callsFor(id)); n != 2 {
		t.Fatalf("the parent made %d calls, want 2 (spawn + one with both results)", n)
	}
	assertTranscriptValid(t, db, id)
}

// A foreground subagent that takes too long moves to the background: the
// parent gets a progress digest and carries on; the answer arrives later as
// one notice, which wakes the (idle) parent.
func TestDemoteOnDeadlineThenNotice(t *testing.T) {
	prev := subagentTimeoutFloor
	subagentTimeoutFloor = 1
	t.Cleanup(func() { subagentTimeoutFloor = prev })
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("research"), say("FINDINGS")).block("child")
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"research","timeout_s":1}`))).once()
	f.on(lastIs("tool", "moved to the background"), say("carrying on"))
	f.on(lastUser("[subagent results"), say("got the findings"))
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the demotion", func() bool { return strings.Contains(transcript(db, id), "carrying on") })
	waitStatus(t, db, id, statusIdle)
	f.release("child")
	waitFor(t, "the notice", func() bool { return strings.Contains(transcript(db, id), "got the findings") })
	if n := strings.Count(fullText(db, id), "FINDINGS"); n != 1 {
		t.Fatalf("the result was delivered %d times: %s", n, fullText(db, id))
	}
	assertTranscriptValid(t, db, id)
}

// The owner speaking while the parent waits on a subagent moves the wait to
// the background at once — the message is answered now, not after the child.
func TestSteerDemotesWaits(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("long"), say("LONG RESULT")).block("child")
	f.on(lastUser("start"), callTools(tc("s", "subagent_spawn", `{"task":"long"}`))).once()
	f.on(lastUser("status?"), say("still on it"))
	f.on(lastUser("[subagent results"), say("done now"))
	id := newRun(t, ag, Config{Subagents: true}, "start")
	waitStatus(t, db, id, statusAwait)
	send(t, ag, id, "status?")
	waitFor(t, "the steer answer", func() bool { return strings.Contains(transcript(db, id), "still on it") })
	if !strings.Contains(fullText(db, id), "moved to the background") {
		t.Fatal(fullText(db, id))
	}
	f.release("child")
	waitFor(t, "the notice", func() bool { return strings.Contains(transcript(db, id), "done now") })
	assertTranscriptValid(t, db, id)
}

func TestWaitAnyThenNotice(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("quick"), say("QUICK"))
	f.on(taskIs("slow"), say("SLOW")).block("slow")
	f.on(lastUser("go"), callTools(
		tc("a", "subagent_spawn", `{"task":"quick","wait":false}`), tc("b", "subagent_spawn", `{"task":"slow","wait":false}`))).once()
	var ids []int64
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		for _, k := range children(db, r.Run) {
			ids = append(ids, k.ID)
		}
		b, _ := json.Marshal(map[string]any{"ids": ids, "mode": "any", "timeout_s": 60})
		return callTools(tc("w", "subagent_wait", string(b)))(r)
	}).once()
	f.on(lastIs("tool", "QUICK"), say("one is back"))
	f.on(lastUser("[subagent results"), say("the other too"))
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the wait to resolve", func() bool { return strings.Contains(fullText(db, id), "one is back") })
	got := fullText(db, id)
	if !strings.Contains(got, "QUICK") || strings.Contains(got, "SLOW") {
		t.Fatalf("wait result: %s", got)
	}
	f.release("slow")
	waitFor(t, "the notice", func() bool { return strings.Contains(transcript(db, id), "the other too") })
	if n := strings.Count(fullText(db, id), "QUICK"); n != 1 {
		t.Fatalf("QUICK delivered %d times", n)
	}
}

func TestMessagingAWorkingAndAFinishedSubagent(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("draft it"), callTools(tc("n", "note", `{"text":"drafting"}`))).block("child").once()
	f.on(lastUser("[message from your parent"), say("REVISED")).once()
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"draft it","wait":false}`))).once()
	var child int64
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		child = children(db, r.Run)[0].ID
		return callTools(tc("m", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"make it shorter"}`, child)))(r)
	}).once()
	f.on(lastIs("tool", "next step"), say("sent"))
	f.on(lastUser("[subagent results"), say("got REVISED"))
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the message to be sent", func() bool { return strings.Contains(transcript(db, id), "A:sent") })
	f.release("child")
	waitFor(t, "the child's answer", func() bool { return strings.Contains(transcript(db, id), "got REVISED") })
	if got := fullText(db, child); !strings.Contains(got, "make it shorter") {
		t.Fatalf("the child never saw the message: %s", got)
	}

	// A finished child set to work again: a new link, so its answer comes back.
	f.on(lastUser("once more"), func(r LLMRequest) (LLMReply, error) {
		return callTools(tc("m2", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"and in French"}`, child)))(r)
	}).once()
	f.on(lastUser("and in French"), say("EN FRANCAIS"))
	f.on(lastIs("tool", "working on it"), say("asked again"))
	send(t, ag, id, "once more")
	waitFor(t, "the second answer", func() bool { return strings.Contains(fullText(db, id), "EN FRANCAIS") })
	assertTranscriptValid(t, db, id)
}

// after:[a]: b starts only when a has finished, with a's result. A dependency
// that already finished counts at once; one that failed is reported.
func TestAfterDependencies(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("step one"), say("ONE")).block("one")
	f.on(taskIs("step two"), say("TWO"))
	f.on(lastUser("chain"), callTools(tc("a", "subagent_spawn", `{"task":"step one","wait":false,"label":"one"}`))).once()
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		a := children(db, r.Run)[0].ID
		return callTools(tc("b", "subagent_spawn", fmt.Sprintf(`{"task":"step two","after":[%d]}`, a)))(r)
	}).once()
	id := newRun(t, ag, Config{Subagents: true}, "chain")
	waitFor(t, "two children", func() bool { return len(children(db, id)) == 2 })
	two := childByTask(db, id, "step two")
	if statusOf(db, two.ID) != statusAwait {
		t.Fatalf("the dependent started before its dependency (%s)", statusOf(db, two.ID))
	}
	f.release("one")
	waitStatus(t, db, two.ID, statusIdle)
	if got := fullText(db, two.ID); !strings.Contains(got, "ONE") {
		t.Fatalf("the dependent never got the result: %s", got)
	}
}

func TestFailedDependencyIsReported(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("will fail"), fail("boom"))
	f.on(taskIs("after it"), say("handled"))
	f.on(lastUser("chain"), callTools(tc("a", "subagent_spawn", `{"task":"will fail","wait":false}`))).once()
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		a := children(db, r.Run)[0].ID
		return callTools(tc("b", "subagent_spawn", fmt.Sprintf(`{"task":"after it","after":[%d]}`, a)))(r)
	}).once()
	id := newRun(t, ag, Config{Subagents: true}, "chain")
	waitFor(t, "two children", func() bool { return len(children(db, id)) == 2 })
	two := childByTask(db, id, "after it")
	waitStatus(t, db, two.ID, statusIdle)
	if got := fullText(db, two.ID); !strings.Contains(got, "failed") {
		t.Fatalf("a failed dependency was not reported: %s", got)
	}
}

// A join waits for the calls of ITS step only — not for an unrelated
// background subagent started earlier (the old engine waited for every edge).
func TestJoinWaitsOnlyForItsOwnCalls(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("forever"), say("never")).block("never")
	f.on(taskIs("now"), say("NOW"))
	f.on(lastUser("go"), callTools(tc("a", "subagent_spawn", `{"task":"forever","wait":false}`))).once()
	f.on(lastIs("tool", "background"), callTools(tc("b", "subagent_spawn", `{"task":"now"}`))).once()
	f.on(lastIs("tool", "NOW"), say("joined"))
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the join", func() bool { return strings.Contains(transcript(db, id), "joined") })
}

// A subagent parked on the owner's approval does not hang its parent: it
// shows up, is approved, and finishes.
func TestSubagentApprovalSurfacesAndResolves(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(taskIs("call out"), callTools(tc("x", "xbin_call", `{"method":"GET","path":"/api/apps/x/y"}`))).once()
	f.on(func(r LLMRequest) bool { return isSubagent(r) && lastIs("tool", "")(r) }, say("CALLED"))
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"call out"}`))).once()
	f.on(lastIs("tool", "CALLED"), say("child done"))
	id := newRun(t, ag, Config{Subagents: true, Approve: true}, "go")
	waitFor(t, "a child", func() bool { return len(children(db, id)) == 1 })
	kid := children(db, id)[0].ID
	waitStatus(t, db, kid, statusWaiting)
	if statusOf(db, id) != statusAwait {
		t.Fatal(statusOf(db, id))
	}
	serve(handleApprove, "POST", "/runs/x/approve", kid, []byte(`{"approve":true}`), "application/json")
	waitFor(t, "the parent to finish", func() bool { return strings.Contains(transcript(db, id), "child done") })
}

// A subagent's turn end stops what it started: nothing outlives the answer
// that was going to consume it.
func TestSubagentTurnEndCancelsGrandchildren(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("grand"), say("x")).block("never")
	f.on(taskIs("mid"), callTools(tc("g", "subagent_spawn", `{"task":"grand","wait":false}`))).once()
	f.on(func(r LLMRequest) bool { return isSubagent(r) && lastIs("tool", "background")(r) }, say("MID DONE"))
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"mid"}`))).once()
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the mid child", func() bool { return len(children(db, id)) == 1 })
	mid := children(db, id)[0].ID
	waitFor(t, "the grandchild", func() bool { return len(children(db, mid)) == 1 })
	grand := children(db, mid)[0].ID
	waitStatus(t, db, grand, statusCanceled)
	waitStatus(t, db, id, statusIdle)
}

func TestSpawnCaps(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("two at once"), callTools(
		tc("a", "subagent_spawn", `{"task":"a","wait":false}`), tc("b", "subagent_spawn", `{"task":"b","wait":false}`))).once()
	id := newRun(t, ag, Config{Subagents: true, MaxSpawnTurn: 1}, "two at once")
	waitStatus(t, db, id, statusIdle)
	if got := fullText(db, id); !strings.Contains(got, "per step") {
		t.Fatalf("the per-step cap was not enforced: %s", got)
	}
	// Depth: a child at the limit cannot delegate even by a hallucinated call.
	f.on(taskIs("deep"), callTools(tc("x", "spawn_subagent", `{"task":"deeper"}`))).once()
	f.on(lastUser("nest"), callTools(tc("n", "subagent_spawn", `{"task":"deep"}`))).once()
	id2 := newRun(t, ag, Config{Subagents: true, MaxDepth: 1}, "nest")
	waitStatus(t, db, id2, statusIdle)
	kid := children(db, id2)[0].ID
	if got := fullText(db, kid); !strings.Contains(got, "depth limit") {
		t.Fatalf("a child at the depth limit delegated: %s", got)
	}
}

func TestLegacyToolNamesStillWork(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(isSubagent, say("LEGACY OK"))
	f.on(lastUser("old style"), callTools(tc("w", "workflow_spawn", `{"task":"x","label":"old"}`))).once()
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		k := children(db, r.Run)[0].ID
		return callTools(tc("st", "workflow_status", `{}`), tc("rs", "workflow_result", fmt.Sprintf(`{"id":%d}`, k)))(r)
	}).once()
	id := newRun(t, ag, Config{Subagents: true}, "old style")
	waitFor(t, "the legacy calls", func() bool { return strings.Contains(transcript(db, id), "[workflow_status][workflow_result]") })
	waitQuiet(t, ag)
	assertTranscriptValid(t, db, id)
}

// The sidebar's source never lists a subagent, whatever its kind or parent.
func TestRootsNeverListSubagents(t *testing.T) {
	db := newTestDB(t)
	top, _ := db.createRun("top", "", 0)
	db.setRunKind(top, "quick")
	_, _ = db.createRun("kid", "", top)
	roots, _ := db.rootRuns()
	if len(roots) != 1 || roots[0].ID != top {
		t.Fatalf("roots = %v", roots)
	}
}

// The race the child/parent column split exists for: a child settling at the
// very instant its parent's deadline fires. Either way the result is
// delivered exactly once.
func TestSettleVersusDeadlineDeliversOnce(t *testing.T) {
	for i := 0; i < 40; i++ {
		db := newTestDB(t)
		ag := newTestAgent(t, db)
		e := ag.eng
		parent, _ := db.createRunStatus("p", "", 0, statusAwait)
		child, _ := db.createRunStatus("c", "", parent, statusRunning)
		addAssistantCalls(t, db, parent, call("s", "subagent_spawn"))
		_, _ = db.addMessage(&Message{RunID: parent, Role: "tool", Name: "subagent_spawn", ToolCallID: "s", Content: "(waiting for subagent #2…)"})
		_, _ = db.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, created) VALUES (?, ?, 's', 'fg', ?, ?)`,
			parent, child, now()-1, now())
		_ = db.setStatus(parent, statusAwait, now()-1, "", `{"kind":"await"}`)
		cr, _ := db.getRun(child)
		pr, _ := db.getRun(parent)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = e.fenced(func(t *DB) error { e.settleOwnLink(t, cr, linkDone, outcomeAnswered, "THE RESULT"); return nil })
		}()
		go func() { defer wg.Done(); e.resolveAwait(pr, false) }()
		wg.Wait()
		// Whatever happened, finish delivery and count.
		pr, _ = db.getRun(parent)
		if pr.Status == statusAwait {
			e.resolveAwait(pr, false)
		}
		_ = e.fenced(func(t *DB) error { e.deliverNotices(t, &turnState{run: pr, root: parent}); return nil })
		msgs, _ := db.messages(parent, false)
		n := 0
		for _, m := range msgs {
			n += strings.Count(m.Content, "THE RESULT")
		}
		if n != 1 {
			t.Fatalf("iteration %d: result delivered %d times", i, n)
		}
	}
}
