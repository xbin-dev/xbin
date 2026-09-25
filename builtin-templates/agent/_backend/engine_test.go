package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestTurnAnswersAndGoesIdle(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	fakeOf(ag).on(lastUser("hello"), say("hi there"))
	id := newRun(t, ag, Config{}, "hello")
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:hello | A:hi there | " {
		t.Fatalf("transcript = %q", got)
	}
}

// A message sent while a step runs is delivered after that step's tool
// results and before the next model call — the steer.
func TestSteerLandsAfterToolsBeforeNextCall(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("start"), callTools(tc("n1", "note", `{"text":"working"}`))).block("step1").once()
	f.on(lastUser("steer!"), say("steered"))
	id := newRun(t, ag, Config{}, "start")
	waitFor(t, "the first call", func() bool { return f.inFlight() == 1 })
	send(t, ag, id, "steer!")
	f.release("step1")
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:start | A:[note] | T:noted | U:steer! | A:steered | " {
		t.Fatalf("transcript = %q", got)
	}
	assertTranscriptValid(t, db, id)
}

// A message that arrives while the model is writing its final answer is
// answered too — the old engine set the run idle over it and it was lost.
func TestSteerDuringPlainAnswerIsAnswered(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("first"), say("answer one")).block("g").once()
	f.on(lastUser("follow up"), say("answer two"))
	id := newRun(t, ag, Config{}, "first")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	send(t, ag, id, "follow up")
	f.release("g")
	waitFor(t, "the follow-up answer", func() bool { return strings.Contains(transcript(db, id), "answer two") })
	waitStatus(t, db, id, statusIdle)
	if got := transcript(db, id); got != "U:first | A:answer one | U:follow up | A:answer two | " {
		t.Fatalf("transcript = %q", got)
	}
}

func TestQueuedMessageCanBeTakenBack(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(lastUser("start"), say("done")).block("g").once()
	id := newRun(t, ag, Config{}, "start")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	iid := send(t, ag, id, "oops, not this")
	w := serveInbox(id, iid)
	if w.Code != 200 {
		t.Fatalf("remove queued = %d %s", w.Code, w.Body)
	}
	f.release("g")
	waitStatus(t, db, id, statusIdle)
	if strings.Contains(transcript(db, id), "oops") {
		t.Fatal("a removed message was delivered")
	}
	if w := serveInbox(id, iid); w.Code != 404 {
		t.Fatalf("removing it again = %d, want 404", w.Code)
	}
	// Once delivered it cannot be taken back.
	iid2 := send(t, ag, id, "keep this")
	waitFor(t, "delivery", func() bool { return strings.Contains(transcript(db, id), "keep this") })
	if w := serveInbox(id, iid2); w.Code != 409 {
		t.Fatalf("removing a delivered message = %d, want 409", w.Code)
	}
}

func serveInbox(runID, iid int64) *httptest.ResponseRecorder {
	r := httptest.NewRequest("DELETE", "/runs/x/inbox/y", nil)
	r.SetPathValue("id", fmt.Sprint(runID))
	r.SetPathValue("iid", fmt.Sprint(iid))
	w := httptest.NewRecorder()
	handleRemoveQueued(w, r)
	return w
}

// post is a human message through the real handler (it also clears a halt).
func post(t *testing.T, id int64, text string) {
	t.Helper()
	if w := serve(handleMessage, "POST", "/runs/x/message", id, []byte(`{"text":"`+text+`"}`), "application/json"); w.Code != 200 {
		t.Fatalf("message = %d %s", w.Code, w.Body)
	}
}

func TestClientIDMakesAPostIdempotent(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("t", "", 0)
	a, dup1, _ := ag.queue(id, inboxUser, inboxBody{Text: "hi"}, "c-1")
	b, dup2, _ := ag.queue(id, inboxUser, inboxBody{Text: "hi"}, "c-1")
	if a != b || dup1 || !dup2 {
		t.Fatalf("a retried post made a second row: %d %d dup=%v/%v", a, b, dup1, dup2)
	}
}

// The bug that made finished chats unusable: once a run had called finish
// (or failed once), the old dispatcher never looked at it again, so its next
// spawn parked forever.
func TestFinishedChatResumesAndDelegatesAgain(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("do it"), callTools(tc("f1", "finish", `{"result":"all done"}`))).once()
	id := newRun(t, ag, Config{Subagents: true}, "do it")
	waitStatus(t, db, id, statusDone)

	f.on(isSubagent, say("child says 42"))
	f.on(lastUser("ask a helper"), callTools(tc("s1", "spawn_subagent", `{"task":"compute"}`))).once()
	f.on(lastIs("tool", "child says 42"), say("the helper said 42"))
	send(t, ag, id, "ask a helper")
	waitFor(t, "the parent to use the child's answer", func() bool { return strings.Contains(transcript(db, id), "the helper said 42") })
	waitStatus(t, db, id, statusIdle)
	assertTranscriptValid(t, db, id)
}

func TestErroredChatIsResumable(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("break"), fail("upstream 400")).once()
	f.on(lastUser("again"), say("fine now"))
	id := newRun(t, ag, Config{}, "break")
	waitStatus(t, db, id, statusError)
	send(t, ag, id, "again")
	waitStatus(t, db, id, statusIdle)
	if !strings.Contains(transcript(db, id), "fine now") {
		t.Fatal(transcript(db, id))
	}
}

// Interrupting a model call stops the turn without calling it an error, and
// messages still queued come back to the owner instead of being sent.
func TestInterruptIsNotAnErrorAndReturnsTheQueue(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(lastUser("long"), say("never")).block("g")
	id := newRun(t, ag, Config{}, "long")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	send(t, ag, id, "queued thought")
	w := serve(handleInterrupt, "POST", "/runs/x/interrupt", id, nil, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "queued thought") {
		t.Fatalf("interrupt = %d %s", w.Code, w.Body)
	}
	waitStatus(t, db, id, statusIdle)
	waitQuiet(t, ag)
	if got := transcript(db, id); strings.Contains(got, "queued thought") || strings.Contains(got, "never") {
		t.Fatalf("transcript = %s", got)
	}
	if len(db.undelivered(id)) != 0 {
		t.Fatal("rows left in the inbox")
	}
}

func TestStaleInterruptIsIgnored(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	id := newRun(t, ag, Config{}, "hi")
	waitStatus(t, db, id, statusIdle)
	serve(handleInterrupt, "POST", "/runs/x/interrupt", id, nil, "") // nothing to stop
	fakeOf(ag).on(lastUser("next"), say("answered"))
	send(t, ag, id, "next")
	waitFor(t, "the next answer", func() bool { return strings.Contains(transcript(db, id), "answered") })
}

func TestYieldWakesOnItsTimerAndOnAMessage(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("nap"), callTools(tc("y1", "yield", `{"seconds":1}`))).once()
	f.on(lastIs("tool", "yielded"), say("awake"))
	id := newRun(t, ag, Config{}, "nap")
	waitStatus(t, db, id, statusSleep)
	waitFor(t, "the timer wake", func() bool { return strings.Contains(transcript(db, id), "awake") })

	f.on(lastUser("long nap"), callTools(tc("y2", "yield", `{"seconds":3600}`))).once()
	f.on(lastUser("wake up"), say("up"))
	send(t, ag, id, "long nap")
	waitStatus(t, db, id, statusSleep)
	send(t, ag, id, "wake up")
	waitFor(t, "the message wake", func() bool { return strings.Contains(transcript(db, id), "A:up") })
}

func TestTurnStepCap(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	fakeOf(ag).on(nil, callTools(tc("", "note", `{"text":"again"}`)))
	id := newRun(t, ag, Config{MaxTurnSteps: 3}, "loop")
	waitStatus(t, db, id, statusIdle)
	if n := len(fakeOf(ag).callsFor(id)); n != 3 {
		t.Fatalf("%d model calls, want 3", n)
	}
	assertTranscriptValid(t, db, id)
}

func TestHaltStopsAndAMessageResumes(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(lastUser("work"), say("x")).block("g")
	id := newRun(t, ag, Config{}, "work")
	waitFor(t, "the call", func() bool { return f.inFlight() == 1 })
	serve(handleHaltPut, "PUT", "/halt", 0, []byte(`{"on":true}`), "application/json")
	waitStatus(t, db, id, statusCanceled)
	f.on(lastUser("go on"), say("resumed"))
	post(t, id, "go on")
	waitFor(t, "the answer", func() bool { return strings.Contains(transcript(db, id), "resumed") })
	if ag.eng.halted() {
		t.Fatal("a human message should clear the halt")
	}
}

func TestCompactionRunsThroughTheEngine(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	f := fakeOf(ag)
	f.on(func(r LLMRequest) bool { return r.Purpose == "compact" }, say("SUMMARY"))
	id := newRun(t, ag, Config{}, "one")
	waitStatus(t, db, id, statusIdle)
	for i, m := range []string{"two", "three", "four"} {
		send(t, ag, id, m)
		want := i + 2
		waitFor(t, "an answer to "+m, func() bool { return strings.Count(transcript(db, id), "A:ok") >= want })
		waitQuiet(t, ag)
	}
	if w := serve(handleCompact, "POST", "/runs/x/compact", id, nil, ""); w.Code != 200 {
		t.Fatalf("compact = %d %s", w.Code, w.Body)
	}
	if r, _ := db.getRun(id); r.Summary != "SUMMARY" {
		t.Fatalf("summary = %q", r.Summary)
	}
}

func TestWatcherRoundsRollBackWhenNothingChanged(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	s := &Schedule{Name: "w", Cron: "@every 1h", Goal: "watch the thing", Watcher: true}
	sid, _ := db.createSchedule(s)
	s.ID = sid
	f.on(lastUser("Check now"), say("no change")).once()
	ag.fireSchedule(s)
	waitFor(t, "the watcher run", func() bool { s2, _ := db.getSchedule(sid); return s2.RunID != 0 })
	s, _ = db.getSchedule(sid)
	waitFor(t, "round 1 to end", func() bool {
		return statusOf(db, s.RunID) == statusIdle && len(db.undelivered(s.RunID)) == 0
	})
	waitQuiet(t, ag)
	if got := transcript(db, s.RunID); got != "" {
		t.Fatalf("a no-change round was kept: %q", got)
	}
	f.on(lastUser("Check now"), callTools(tc("c1", "state_changed", `{"summary":"it moved"}`))).once()
	f.on(lastIs("tool", "change recorded"), say("reported"))
	ag.fireSchedule(s)
	waitFor(t, "round 2", func() bool { return strings.Contains(transcript(db, s.RunID), "reported") })
	waitQuiet(t, ag)
	if got := transcript(db, s.RunID); !strings.Contains(got, "state_changed") {
		t.Fatalf("a changed round was discarded: %q", got)
	}
}

// A wide fan-out never makes a person wait more than one call for their chat.
func TestInteractiveCallsAreNotStarved(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	ag.eng.gate.setLimit(2)
	f := fakeOf(ag)
	f.on(isSubagent, say("kid")).block("kids")
	f.on(lastUser("fan out"), callTools(
		tc("a", "subagent_spawn", `{"task":"1","wait":false}`), tc("b", "subagent_spawn", `{"task":"2","wait":false}`),
		tc("c", "subagent_spawn", `{"task":"3","wait":false}`), tc("d", "subagent_spawn", `{"task":"4","wait":false}`))).once()
	f.on(lastIs("tool", "background"), say("started them"))
	busy := newRun(t, ag, Config{Subagents: true}, "fan out")
	waitFor(t, "a subagent call in flight", func() bool { return f.inFlight() >= 1 })
	_ = busy
	f.on(lastUser("quick question"), say("quick answer"))
	chat := newRun(t, ag, Config{}, "quick question")
	waitStatus(t, db, chat, statusIdle)
	f.release("kids")
}

// --- ownership and handoff ----------------------------------------------------------

func fileAgent(t *testing.T, path string) *Agent {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	ag := &Agent{db: db, repl: newReplRegistry(), toolSem: make(chan struct{}, maxToolsGlobal),
		blobs: newMemBlobs(), blobCache: newBlobCache(8 << 20), noGateway: true}
	newEngine(db, ag, &fakeLLM{t: t, gates: map[string]chan struct{}{}}, path+".engine")
	return ag
}

// The successor boots while the old process still holds the engine lock; it
// takes over the instant the old one lets go, and finishes the turn the old
// one was in the middle of — once.
func TestHandoffBetweenProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	a := fileAgent(t, path)
	fa := fakeOf(a)
	fa.on(lastUser("work"), say("from A")).block("never")
	a.eng.Start()
	waitFor(t, "A to own", func() bool { a.eng.mu.Lock(); defer a.eng.mu.Unlock(); return a.eng.owned })
	id := newRun(t, a, Config{}, "work")
	waitFor(t, "A's call", func() bool { return fa.inFlight() == 1 })

	b := fileAgent(t, path)
	fakeOf(b).on(lastUser("work"), say("from B"))
	var took atomic.Bool
	b.eng.onTakeup = func() { took.Store(true) }
	b.eng.Start()
	time.Sleep(100 * time.Millisecond)
	if took.Load() {
		t.Fatal("B took over while A still held the lock")
	}
	start := time.Now()
	a.eng.Shutdown(time.Second)
	waitFor(t, "B to take over", took.Load)
	waitStatus(t, b.db, id, statusIdle)
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("handoff took %v", d)
	}
	if got := transcript(b.db, id); got != "U:work | A:from B | " {
		t.Fatalf("transcript after the handoff = %q", got)
	}
	b.eng.Shutdown(time.Second)
}

// Two engines that both believe they own the file (a lock that does not
// work): the later takeover fences the earlier one out.
func TestEpochFencesAStaleEngine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	a := fileAgent(t, path)
	a.eng.lockPath = ""
	a.eng.takeOver()
	b := fileAgent(t, path)
	b.eng.lockPath = ""
	b.eng.takeOver()
	err := a.eng.fenced(func(t *DB) error { return nil })
	if err != errFenced {
		t.Fatalf("the stale engine could still write: %v", err)
	}
	a.eng.mu.Lock()
	owned := a.eng.owned
	a.eng.mu.Unlock()
	if owned {
		t.Fatal("the stale engine still thinks it owns the database")
	}
	a.eng.Shutdown(time.Second)
	b.eng.Shutdown(time.Second)
}

func TestTimersSurviveARestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	a := fileAgent(t, path)
	a.eng.Start()
	waitFor(t, "A to own", func() bool { a.eng.mu.Lock(); defer a.eng.mu.Unlock(); return a.eng.owned })
	fakeOf(a).on(lastUser("nap"), callTools(tc("y", "yield", `{"seconds":2}`)))
	id := newRun(t, a, Config{}, "nap")
	waitStatus(t, a.db, id, statusSleep)
	a.eng.Shutdown(time.Second)

	b := fileAgent(t, path)
	fakeOf(b).on(lastIs("tool", "yielded"), say("woke in B"))
	b.eng.Start()
	waitFor(t, "the wake in the new process", func() bool { return strings.Contains(transcript(b.db, id), "woke in B") })
	b.eng.Shutdown(time.Second)
}

// No goroutine in the backend runs on a clock: everything is an event or a
// one-shot timer at a known instant.
func TestNoTickers(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// repl.go's watchdog samples the heap while ONE sandboxed statement
		// runs (bounded by its time budget) — goja has no allocation hook. It
		// never schedules anything.
		if f == "repl.go" {
			continue
		}
		b, _ := os.ReadFile(f)
		for _, bad := range []string{"time.NewTicker", "time.Tick("} {
			if strings.Contains(string(b), bad) {
				t.Errorf("%s uses %s", f, bad)
			}
		}
	}
}

func TestHoldFollowsWork(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	var open atomic.Int32
	ag.eng.hold.mu.Lock()
	ag.eng.hold.open = func(ctx context.Context) error {
		open.Add(1)
		<-ctx.Done()
		open.Add(-1)
		return nil
	}
	ag.eng.hold.mu.Unlock()
	f := fakeOf(ag)
	f.on(lastUser("busy"), say("done")).block("g")
	id := newRun(t, ag, Config{}, "busy")
	waitFor(t, "the hold while working", func() bool { return open.Load() == 1 })
	f.release("g")
	waitStatus(t, db, id, statusIdle)
	waitFor(t, "the hold to be released", func() bool { return open.Load() == 0 })
}
