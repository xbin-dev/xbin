package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// pushLog records what the needs pusher sent (in place of POST /api/xbin/notify).
type pushLog struct {
	mu   sync.Mutex
	sent []xbin.UserNotification
	err  error
}

func (l *pushLog) send(_ context.Context, n xbin.UserNotification) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sent = append(l.sent, n)
	return l.err
}

func (l *pushLog) all() []xbin.UserNotification {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]xbin.UserNotification(nil), l.sent...)
}

// to is who got a push, sorted, as "user:kind".
func (l *pushLog) to() string {
	var out []string
	for _, n := range l.all() {
		out = append(out, n.User+":"+n.Kind)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func withPushes(ag *Agent, grace time.Duration) *pushLog {
	l := &pushLog{}
	ag.needs = newNeedsPusher(l.send)
	ag.needs.grace = grace
	return l
}

// A question reaches the people who may answer it — the owner and
// participant members, not viewers — once, linking to the conversation.
func TestNeedsPushQuestion(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	log := withPushes(ag, 0)
	f := fakeOf(ag)
	f.on(lastUser("book it"), callTools(tc("q1", "ask_user", `{"question":"**Which** day?"}`))).once()
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, true)
	send(t, ag, id, "book it")
	waitStatus(t, db, id, statusWaiting)
	waitFor(t, "two pushes", func() bool { return len(log.all()) == 2 })
	if got := log.to(); got != "alice:question carol:question" {
		t.Fatalf("pushed to %s (dave is only a viewer)", got)
	}
	n := log.all()[0]
	if n.Title != "t" || n.Body != "Which day?" || n.Link != fmt.Sprintf("#c=%d", id) || n.CollapseID != fmt.Sprintf("needs:%d", id) {
		t.Fatalf("push: %+v", n)
	}
	// the same moment again (a re-park, a second process's notice) is not news
	ag.needs.check(ag, id)
	if len(log.all()) != 2 {
		t.Fatalf("deduped per run+state: %s", log.to())
	}
}

// An approval names what it wants to run; a subagent's is linked to the
// subagent (where the approval card is) and goes to its root's people.
func TestNeedsPushSubagentApproval(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	log := withPushes(ag, 0)
	f := fakeOf(ag)
	f.on(taskIs("call out"), callTools(tc("x", "xbin_call", `{"method":"GET","path":"/api/apps/x/y"}`))).once()
	f.on(func(r LLMRequest) bool { return isSubagent(r) && lastIs("tool", "")(r) }, say("CALLED"))
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"call out"}`))).once()
	f.on(lastIs("tool", "CALLED"), say("child done"))
	cfg := defaultConfig()
	cfg.Subagents, cfg.Approve = true, true
	cfg.Features = mergeFeatures(cfg.Features, map[string]bool{"streaming": false})
	r, err := ag.startRunOpts(runOpts{Title: "deploy", Cfg: cfg, Text: "go", Sender: "bob",
		Stamp: runStamp{Owner: "bob", Visibility: visTeam, TeamRole: roleParticipant, Origin: "chat"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a child", func() bool { return len(children(db, r.ID)) == 1 })
	kid := children(db, r.ID)[0].ID
	waitStatus(t, db, kid, statusWaiting)
	waitFor(t, "a push", func() bool { return len(log.all()) == 1 })
	n := log.all()[0]
	if n.User != "bob" || n.Kind != needApproval || n.Title != "deploy" || n.Link != fmt.Sprintf("#c=%d", kid) ||
		!strings.Contains(n.Body, "xbin_call") {
		t.Fatalf("push: %+v (a team-wide participant role is nobody in particular)", n)
	}
	serve(handleApprove, "POST", "/runs/x/approve", kid, []byte(`{"approve":true}`), "application/json")
	waitFor(t, "the parent to finish", func() bool { return strings.Contains(transcript(db, r.ID), "child done") })
	if len(log.all()) != 1 {
		t.Fatalf("pushes: %s", log.to())
	}
}

// Answered within the grace (someone was looking): nothing is sent.
func TestNeedsPushGrace(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	log := withPushes(ag, 150*time.Millisecond)
	f := fakeOf(ag)
	f.on(lastUser("ask me"), callTools(tc("q1", "ask_user", `{"question":"sure?"}`))).once()
	f.on(lastUser("yes"), say("ok"))
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	send(t, ag, id, "ask me")
	waitStatus(t, db, id, statusWaiting)
	send(t, ag, id, "yes")
	waitStatus(t, db, id, statusIdle)
	time.Sleep(300 * time.Millisecond)
	if len(log.all()) != 0 {
		t.Fatalf("pushed after the answer: %s", log.to())
	}
}

// A failed automation run reaches its owner; a failed chat does not (the
// person saw it fail), nor does an unowned automation's.
func TestNeedsPushFailedAutomation(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	log := withPushes(ag, 0)
	f := fakeOf(ag)
	f.on(lastUser("digest"), fail("upstream 500"))
	f.on(lastUser("chat"), fail("upstream 500"))
	sched := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "schedule", OriginID: 3}, true)
	legacy := runAs(t, ag, runStamp{Visibility: visTeam, TeamRole: roleParticipant, Origin: "schedule", OriginID: 4}, false)
	chat := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	send(t, ag, sched, "digest")
	send(t, ag, legacy, "digest")
	send(t, ag, chat, "chat")
	for _, id := range []int64{sched, legacy, chat} {
		waitStatus(t, db, id, statusError)
	}
	waitFor(t, "a push", func() bool { return len(log.all()) >= 1 })
	time.Sleep(50 * time.Millisecond)
	if got := log.to(); got != "alice:failed" {
		t.Fatalf("pushed to %s (only the owner, only the automation)", got)
	}
	if n := log.all()[0]; n.Link != fmt.Sprintf("#c=%d", sched) || !strings.Contains(n.Body, "upstream 500") {
		t.Fatalf("push: %+v", n)
	}
}

// The per-person budget drops what is over it; a refusal from xbind is
// logged, not retried.
func TestNeedsPushBudget(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	log := withPushes(ag, 0)
	log.err = errors.New("403 that user cannot read this tile")
	p := ag.needs
	clock := time.Unix(1000, 0)
	p.now = func() time.Time { return clock }
	p.userBurst, p.userEvery = 2, time.Minute
	var ids []int64
	for i := 0; i < 4; i++ {
		id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
		if err := db.setStatus(id, statusWaiting, 0, fmt.Sprintf("question %d", i), ""); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	for _, id := range ids[:3] {
		p.check(ag, id)
	}
	if len(log.all()) != 2 {
		t.Fatalf("over the burst: %d sent", len(log.all()))
	}
	clock = clock.Add(time.Minute) // one refilled
	p.check(ag, ids[3])
	p.check(ag, ids[0]) // already sent: a duplicate costs nothing
	if len(log.all()) != 3 {
		t.Fatalf("after a refill: %d sent", len(log.all()))
	}
	clock = clock.Add(7 * time.Hour) // the dedupe window passed
	p.check(ag, ids[0])
	if len(log.all()) != 4 {
		t.Fatalf("after the dedupe window: %d sent", len(log.all()))
	}
	// a new question in the same run is news again
	clock = clock.Add(time.Hour)
	_ = db.setStatus(ids[1], statusWaiting, 0, "another question", "")
	p.check(ag, ids[1])
	if len(log.all()) != 5 {
		t.Fatalf("a new question: %d sent", len(log.all()))
	}
}

// No pusher (tests, an instance that removed it): the engine carries on.
func TestNeedsPushOff(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("ask"), callTools(tc("q1", "ask_user", `{"question":"?"}`))).once()
	id := newRun(t, ag, Config{}, "ask")
	waitStatus(t, db, id, statusWaiting)
	var nilAgent *Agent
	nilAgent.needsMoment(id)
	if plainText("## **Pick** `one`\n  please") != "Pick one please" {
		t.Fatal(plainText("## **Pick** `one`\n  please"))
	}
}

// A run that keeps failing (a watcher, a session) is one push per window,
// whatever the error says.
func TestNeedsPushFailingRunOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	log := withPushes(ag, 0)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "watcher", OriginID: 2}, false)
	for i := 0; i < 3; i++ {
		if err := db.setStatus(id, statusError, 0, fmt.Sprintf("round %d: upstream 500", i), ""); err != nil {
			t.Fatal(err)
		}
		ag.needs.check(ag, id)
	}
	if got := log.to(); got != "alice:failed" {
		t.Fatalf("pushed %s", got)
	}
}
