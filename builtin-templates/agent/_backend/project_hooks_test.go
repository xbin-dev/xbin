package main

import (
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"
)

// hookLog records what a hook list saw (installed for one test).
type hookLog struct {
	mu   sync.Mutex
	seen []string
}

func (h *hookLog) add(s string) {
	h.mu.Lock()
	h.seen = append(h.seen, s)
	h.mu.Unlock()
}

func (h *hookLog) all() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.seen...)
}

func (h *hookLog) count(s string) int {
	n := 0
	for _, x := range h.all() {
		if x == s {
			n++
		}
	}
	return n
}

func recordTurnEnds(t *testing.T) *hookLog {
	h := &hookLog{}
	old := turnEndHooks
	turnEndHooks = append(append([]func(*DB, *Run, string, string, string){}, old...), func(_ *DB, run *Run, why, _, _ string) {
		h.add(fmt.Sprintf("%d %s", run.ID, why))
	})
	t.Cleanup(func() { turnEndHooks = old })
	return h
}

func recordStatuses(t *testing.T) *hookLog {
	h := &hookLog{}
	old := runStatusHooks
	runStatusHooks = append(append([]func(*DB, int64, string){}, old...), func(_ *DB, id int64, status string) {
		h.add(fmt.Sprintf("%d %s", id, status))
	})
	t.Cleanup(func() { runStatusHooks = old })
	return h
}

// turnEndHooks run once at every turn end, with the seam's words: a
// top-level built-in run's, a subagent's, an interrupt's (stopRun), a
// coding agent's, and a coding agent's cancel.
func TestTurnEndHooksEverySite(t *testing.T) {
	t.Run("built-in", func(t *testing.T) {
		h := recordTurnEnds(t) // before the fixture: restored after its engine stops
		ag, _ := accessFixture(t)
		f := fakeOf(ag)
		conv := createConv(t, ag, alicePrivate, nil)
		send(t, ag, conv.ID, "hello")
		waitFor(t, "the turn's end hook", func() bool { return h.count(fmt.Sprintf("%d answered", conv.ID)) == 1 })
		// a subagent's turn
		kid, err := ag.db.createRunStatus("kid", `{}`, conv.ID, statusIdle)
		if err != nil {
			t.Fatal(err)
		}
		send(t, ag, kid, "work")
		waitFor(t, "the subagent's end hook", func() bool { return h.count(fmt.Sprintf("%d answered", kid)) == 1 })
		// an interrupt mid-turn (stopRun)
		gate := "turn-ends-interrupt"
		f.on(lastUser("slowly"), say("never")).block(gate)
		send(t, ag, conv.ID, "slowly")
		waitFor(t, "the call in flight", func() bool { return f.inFlight() > 0 })
		_, _, _ = ag.queue(conv.ID, inboxInterrupt, inboxBody{Reason: "stop"}, "")
		ag.eng.Signal(conv.ID, errInterrupt)
		waitFor(t, "the interrupt's end hook", func() bool { return h.count(fmt.Sprintf("%d interrupted", conv.ID)) == 1 })
		f.release(gate)
		time.Sleep(50 * time.Millisecond)
		if n := h.count(fmt.Sprintf("%d answered", conv.ID)); n != 1 {
			t.Fatalf("the first turn's hook ran %d times: %v", n, h.all())
		}
	})
	t.Run("coding agent", func(t *testing.T) {
		h := recordTurnEnds(t)
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "echo one")
		hwait(t, "the turn", turnOver(ag, run.ID))
		hwait(t, "the coding agent's end hook", func() bool { return h.count(fmt.Sprintf("%d answered", run.ID)) == 1 })
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "slow"}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		hwait(t, "the slow turn running", func() bool {
			hs, _ := ag.db.harnessSession(run.ID)
			return hs != nil && hs.PromptState != ""
		})
		time.Sleep(300 * time.Millisecond)
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/cancel", run.ID), map[string]any{}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		hwait(t, "the cancel's end hook", func() bool { return h.count(fmt.Sprintf("%d canceled", run.ID)) >= 1 })
		time.Sleep(300 * time.Millisecond)
		if n := h.count(fmt.Sprintf("%d canceled", run.ID)); n != 1 {
			t.Fatalf("the cancel's hook ran %d times: %v", n, h.all())
		}
	})
}

// runStatusHooks see every move to waiting_input that ends no turn — an
// approval park, ask_user, a coding agent's question — and never a hosted
// run's, nor the legacy rows migrate() rewrites before the feature tables.
func TestRunStatusHooksOnPark(t *testing.T) {
	t.Run("built-in", func(t *testing.T) {
		h := recordStatuses(t)
		ag, _, id := approveConv(t)
		f := fakeOf(ag)
		f.on(lastUser("go"), callTools(tc("b1", "bash", `{"command":"echo hi"}`))).once()
		send(t, ag, id, "go")
		waitFor(t, "the approval park's hook", func() bool { return h.count(fmt.Sprintf("%d waiting_input", id)) == 1 })
		if p := parsePending(mustRun(t, ag, id).Pending); p.Kind != "approval" {
			t.Fatalf("parked on %+v", p)
		}
		conv := createConv(t, ag, alicePrivate, nil)
		f.on(lastUser("ask me"), callTools(tc("q1", "ask_user", `{"question":"which?"}`))).once()
		send(t, ag, conv.ID, "ask me")
		waitFor(t, "ask_user's hook", func() bool { return h.count(fmt.Sprintf("%d waiting_input", conv.ID)) == 1 })
	})
	t.Run("coding agent", func(t *testing.T) {
		h := recordStatuses(t)
		ag, mux, box := harnessFixture(t, false)
		run := askHarness(t, mux, box, "ask")
		parkOf(t, ag, run.ID, "question")
		hwait(t, "the question's hook", func() bool { return h.count(fmt.Sprintf("%d waiting_input", run.ID)) >= 1 })
	})
	t.Run("hosted and legacy", func(t *testing.T) {
		h := recordStatuses(t)
		turns := recordTurnEnds(t)
		// migrate() rewrites a legacy database's runs (a queued one to
		// running) before any feature table exists: no hook sees it
		path := seedOldDB(t, oldHarnessRows+`
INSERT INTO runs (id, title, status, config, created, updated, root_id, owner, visibility, team_role, origin)
 VALUES (9, 'legacy', 'queued', '{}', 1790000000, 1790000000, 9, 'alice', 'private', 'viewer', 'chat');`)
		db, err := openDB(path)
		if err != nil {
			t.Fatal(err)
		}
		defer db.sql.Close()
		if r, _ := db.getRun(9); r.Status != statusRunning {
			t.Fatalf("migrate() didn't rewrite the legacy run: %s", r.Status)
		}
		if got := h.all(); len(got) != 0 {
			t.Fatalf("the legacy rewrite reached the hook: %v", got)
		}
		_ = db.setStatus(9, statusIdle, 0, "", "")
		if got := h.all(); len(got) != 1 || got[0] != "9 idle" {
			t.Fatalf("after the features are in, a status write reaches the hook once: %v", got)
		}
		noFeatures := &DB{sql: db.sql, q: db.q}
		_ = noFeatures.setStatus(9, statusIdle, 0, "", "")
		hosted := teamIDBase + 5
		runStatusChanged(db, hosted, statusWaiting)
		runTurnEnd(db, &Run{ID: hosted, Origin: originProject}, turnAnswered, outcomeAnswered, "")
		if got := h.all(); len(got) != 1 {
			t.Fatalf("a handle without the feature tables, or a hosted run, reached the hook: %v", got)
		}
		if got := turns.all(); len(got) != 0 {
			t.Fatalf("a hosted run's turn end reached the hook: %v", got)
		}
	})
}

// A hosted conversation (a run in team, driven by a person's partition)
// reaches no hook — turn end, status, view or delete — and nothing of a
// project is written to team.
func TestHostedRunReachesNoHook(t *testing.T) {
	turns, statuses := recordTurnEnds(t), recordStatuses(t)
	views, deletes := &hookLog{}, &hookLog{}
	oldV, oldD := runViewHooks, runDeletedHooks
	runViewHooks = append(append([]func(*DB, who, *Run, map[string]any){}, oldV...), func(_ *DB, _ who, run *Run, _ map[string]any) { views.add(fmt.Sprint(run.ID)) })
	runDeletedHooks = append(append([]func(*DB, int64) error{}, oldD...), func(_ *DB, id int64) error { deletes.add(fmt.Sprint(id)); return nil })
	t.Cleanup(func() { runViewHooks, runDeletedHooks = oldV, oldD })
	ag, _ := accessFixture(t)

	team := newTestTeamDB(t)
	hosted := &Run{ID: teamIDBase + 1, Origin: originProject, OriginID: 1}
	for _, d := range []*DB{ag.db, team} {
		runTurnEnd(d, hosted, turnAnswered, outcomeAnswered, "")
		runStatusChanged(d, hosted.ID, statusWaiting)
		runViewExtras(d, who{kind: whoSystem}, hosted, map[string]any{})
		if err := runDeleted(d, hosted.ID); err != nil {
			t.Fatal(err)
		}
	}
	// team's own handle has no feature tables: an ordinary id there reaches none either
	plain := &Run{ID: 7, Origin: originProject}
	runTurnEnd(team, plain, turnAnswered, outcomeAnswered, "")
	runStatusChanged(team, 7, statusIdle)
	runViewExtras(team, who{kind: whoSystem}, plain, map[string]any{})
	_ = runDeleted(team, 7)
	for name, h := range map[string]*hookLog{"turn end": turns, "status": statuses, "view": views, "delete": deletes} {
		if got := h.all(); len(got) != 0 {
			t.Errorf("%s hooks ran: %v", name, got)
		}
	}
	for _, tbl := range projectTables {
		var n int
		if err := team.q.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err == nil {
			t.Errorf("team has %s", tbl)
		}
	}
}

// newTestTeamDB is a team database as migrateTeamRuns makes it: migrate()
// alone, no feature tables.
func newTestTeamDB(t *testing.T) *DB {
	t.Helper()
	sq, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	sq.SetMaxOpenConns(1)
	t.Cleanup(func() { sq.Close() })
	if err := migrateTeamRuns(sq); err != nil {
		t.Fatal(err)
	}
	return &DB{sql: sq, q: sq}
}

// The `project` stream event reaches the list subscribers who may see the
// project — coalesced, never anyone else — and a task's own stream.
func TestProjectStreamEvent(t *testing.T) {
	fx := newProjFix(t)
	hub := fx.ag.eng.hub
	alice, _, _ := hub.subscribe(0, hub.now(), who{kind: whoUser, user: "alice", level: "read"})
	bob, _, _ := hub.subscribe(0, hub.now(), who{kind: whoUser, user: "bob", level: "read"})
	defer hub.unsubscribe(alice)
	defer hub.unsubscribe(bob)
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "go"})
	taskSub, _, _ := hub.subscribe(runID, hub.now(), who{kind: whoUser, user: "alice", level: "read"})
	defer hub.unsubscribe(taskSub)
	projEvents := func(s *subscriber) []map[string]any {
		s.mu.Lock()
		defer s.mu.Unlock()
		var out []map[string]any
		for _, ev := range s.q {
			if ev.Type == evProject {
				out = append(out, ev.Data.(map[string]any))
			}
		}
		return out
	}
	waitFor(t, "alice's project events", func() bool {
		var proj, task bool
		for _, d := range projEvents(alice) {
			proj = proj || (d["change"] == "project" && d["id"] == p.ID)
			task = task || (d["change"] == "task" && d["n"] == int64(1))
		}
		return proj && task
	})
	waitFor(t, "the task's own stream", func() bool { return len(projEvents(taskSub)) > 0 })
	time.Sleep(50 * time.Millisecond)
	if got := projEvents(bob); len(got) != 0 {
		t.Fatalf("bob got the project's events: %v", got)
	}
	// coalesced: a burst of the same change is one event
	projStreamDelay.Store(int64(250 * time.Millisecond)) // as in production: the burst is well within it
	before := len(projEvents(alice))
	for i := 0; i < 20; i++ {
		projStream.post(p.ID, "repo", 0)
	}
	waitFor(t, "the coalesced event", func() bool { return len(projEvents(alice)) > before })
	time.Sleep(300 * time.Millisecond)
	n := 0
	for _, d := range projEvents(alice)[before:] {
		if d["change"] == "repo" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("20 repo changes in a burst made %d events", n)
	}
}
