package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// starterRows are the starter messages in run's inbox (delivered or not).
func starterRows(d *DB, runID int64) []*InboxRow {
	var out []*InboxRow
	for _, r := range d.inboxRows(`WHERE run_id=? AND kind='user'`, runID) {
		if r.Body.Source == srcStarter {
			out = append(out, r)
		}
	}
	return out
}

// starterEvents is pid's events whose text starts "starter:".
func starterEvents(d *DB, pid int64) []string {
	var out []string
	for _, ev := range d.projectEvents(pid, 0, 500) {
		var b struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Body, &b)
		if strings.HasPrefix(b.Text, "starter:") {
			out = append(out, b.Text)
		}
	}
	return out
}

func newStarterProject(t *testing.T, fx *projFix, starter map[string]any) (ProjectView, map[string]any) {
	t.Helper()
	body := fx.projBody(map[string]any{})
	if starter != nil {
		body["starter"] = starter
	}
	w := callAs(t, fx.mux, asAlice, "POST", "/projects", body)
	if w.Code != 201 {
		t.Fatalf("POST /projects: %d %s", w.Code, w.Body)
	}
	var out struct {
		Project ProjectView    `json:"project"`
		Starter map[string]any `json:"starter"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Project, out.Starter
}

// Both: once the workspace is ready, task #1 — the builds and tests, its
// brief built in, as the project's creator; its first turn ends, it dreams
// (policy dream on), and once that round is over the creator's coordinator
// gets exactly one starter message, from the platform; the triggers run
// again (a restart) neither make a second task nor send it again.
func TestProjectStarterBuildThenWiki(t *testing.T) {
	fx := newProjFix(t)
	coordWakerReset()
	t.Cleanup(coordWakerReset)
	d := fx.ag.db
	p, st := newStarterProject(t, fx, map[string]any{"build": true, "wiki": true})
	if st["build"] != true || st["wiki"] != true {
		t.Fatalf("the answer's starter: %v", st)
	}
	if r, ok := d.starterOf(p.ID); !ok || r.Build != stWait || r.Wiki != stWait || r.By != "alice" {
		t.Fatalf("the record: %+v %v", r, ok)
	}
	var k *ProjectTask
	hwait(t, "task #1", func() bool { k, _ = d.taskByN(p.ID, 1); return k != nil })
	if k.Title != starterTitle || k.CreatedBy != "alice" || k.TurnBy != srcHuman {
		t.Fatalf("task #1: %+v", k)
	}
	if !d.workspaceReadyFor(p.ID) {
		t.Fatalf("task #1 made before the workspace was ready (jobs %s)", jobsDump(d, p.ID))
	}
	waitFor(t, "task #1's first turn", func() bool { return len(fakeOf(fx.ag).callsFor(k.RunID)) > 0 })
	first := userTexts(fakeOf(fx.ag).callsFor(k.RunID)[0])
	if got := first[len(first)-1]; !strings.HasSuffix(got, starterBrief) || !strings.Contains(starterBrief, "work it out from its manifests") {
		t.Fatalf("task #1's prompt doesn't end with the brief:\n%s", got)
	}
	var coord int64
	hwait(t, "the coordinator's starter message", func() bool {
		coord = d.coordRunOf(p.ID, "alice")
		return coord != 0 && len(starterRows(d, coord)) == 1
	})
	// its dream round went first, and was over
	dreams := dreamsIn(d, k.RunID)
	if len(dreams) != 1 || dreams[0].DeliveredAt == 0 || !strings.Contains(dreams[0].Body.Text, "Your builds and tests are done.") {
		t.Fatalf("task #1's dream round before the message: %+v", dreams)
	}
	if dr := d.dreamOf(k.ID); dr.Active || !dr.LastDream || dr.Dreams != 1 {
		t.Fatalf("the dream row when the message went: %+v", dr)
	}
	row := starterRows(d, coord)[0]
	for _, want := range []string{starterHead, "task_result 1", "kept what it verified in the wiki", "research subagents", "(slug `index`)", "(as task #1 verified)"} {
		if !strings.Contains(row.Body.Text, want) {
			t.Errorf("the starter message lacks %q:\n%s", want, row.Body.Text)
		}
	}
	if row.Body.Sender != "" || row.Body.Source != srcStarter {
		t.Fatalf("the message is a person's: %+v", row.Body)
	}
	waitFor(t, "the coordinator's turn with it", func() bool {
		for _, c := range fakeOf(fx.ag).callsFor(coord) {
			for _, s := range userTexts(c) {
				if strings.Contains(s, starterHead) {
					return true
				}
			}
		}
		return false
	})
	if r, _ := d.starterOf(p.ID); r.Build != stDone || r.Wiki != stDone || r.Task != 1 || r.DreamMs == 0 {
		t.Fatalf("the record at the end: %+v", r)
	}
	if evs := starterEvents(d, p.ID); strings.Join(evs, "|") != "starter: task #1 created|starter: the coordinator builds the initial wiki" {
		t.Fatalf("the starter's events: %q", evs)
	}

	// a restart: every trigger runs again, the job too — nothing twice
	run, _ := d.getRun(k.RunID)
	_ = d.Tx(func(tx *DB) error {
		starterJobDone(tx, p.ID)
		starterTurnEnd(tx, run, turnAnswered, "", "")
		starterDreamOver(tx, p.ID, 1)
		starterKick(tx, p.ID, "alice")
		return nil
	})
	successor(t, fx.ag)
	waitJobsDone(t, fx, p.ID)
	time.Sleep(100 * time.Millisecond)
	var tasks int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=?`, p.ID).Scan(&tasks)
	if n := len(starterRows(d, coord)); tasks != 1 || n != 1 {
		t.Fatalf("after a restart: %d task(s), %d starter message(s)", tasks, n)
	}
	if evs := starterEvents(d, p.ID); len(evs) != 2 {
		t.Fatalf("the starter's events after a restart: %q", evs)
	}
}

// workspaceReadyFor: project pid's workspace is ready (the test's view).
func (d *DB) workspaceReadyFor(pid int64) bool {
	p, err := d.getProject(pid)
	return err == nil && workspaceReady(d, p)
}

// The wiki alone: no task; once the workspace is ready the coordinator is
// asked at once, without the task's part. Dream off with both: no round,
// the message right after task #1's turn.
func TestProjectStarterWikiOnly(t *testing.T) {
	fx := newProjFix(t)
	coordWakerReset()
	t.Cleanup(coordWakerReset)
	d := fx.ag.db
	p, st := newStarterProject(t, fx, map[string]any{"wiki": true})
	if st["build"] != false || st["wiki"] != true {
		t.Fatalf("the answer's starter: %v", st)
	}
	var coord int64
	hwait(t, "the coordinator's starter message", func() bool {
		coord = d.coordRunOf(p.ID, "alice")
		return coord != 0 && len(starterRows(d, coord)) == 1
	})
	if !d.workspaceReadyFor(p.ID) {
		t.Fatal("the message went before the workspace was ready")
	}
	text := starterRows(d, coord)[0].Body.Text
	if strings.Contains(text, "task_result") || strings.Contains(text, "task #1") || !strings.Contains(text, "Build this project's initial wiki.") {
		t.Fatalf("the wiki-only message:\n%s", text)
	}
	waitJobsDone(t, fx, p.ID)
	if _, err := d.taskByN(p.ID, 1); err == nil {
		t.Fatal("a task was made with the wiki alone")
	}

	// both, dream off: task #1's turn, then the message at once
	body := fx.projBody(map[string]any{"name": "Web two", "starter": map[string]any{"build": true, "wiki": true}, "policy": map[string]any{"dream": false}})
	w := callAs(t, fx.mux, asAlice, "POST", "/projects", body)
	if w.Code != 201 {
		t.Fatalf("POST /projects: %d %s", w.Code, w.Body)
	}
	var out struct{ Project ProjectView }
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	pid := out.Project.ID
	var k *ProjectTask
	hwait(t, "task #1", func() bool { k, _ = d.taskByN(pid, 1); return k != nil })
	hwait(t, "the coordinator's starter message", func() bool {
		coord = d.coordRunOf(pid, "alice")
		return coord != 0 && len(starterRows(d, coord)) == 1
	})
	if n := len(dreamsIn(d, k.RunID)); n != 0 {
		t.Fatalf("a dream round with policy dream off: %d", n)
	}
	if r, _ := d.starterOf(pid); r.DreamMs != 0 || r.Wiki != stDone {
		t.Fatalf("the record: %+v", r)
	}
	if text := starterRows(d, coord)[0].Body.Text; !strings.Contains(text, "Task #1 ran the builds and tests: read its answer (task_result 1).") {
		t.Fatalf("the message after a turn without a dream:\n%s", text)
	}
}

// Refused: a team project's definition (no tasks, no coordinator), no
// sandbox, the wiki for a component or where the coordinator's class is
// the managers' — each before anything is made.
func TestProjectStarterRefused(t *testing.T) {
	t.Run("team definition", func(t *testing.T) {
		setMode(t, modeGlobal, "")
		fx := newProjFix(t)
		body := fx.projBody(map[string]any{"kind": "team", "sandbox": nil, "share": map[string]any{"visibility": "team", "teamRole": "participant"},
			"starter": map[string]any{"build": true}})
		w := callAs(t, fx.mux, asAlice, "POST", "/projects", body)
		if w.Code != 409 || !strings.Contains(w.Body.String(), `"refusal":"starter"`) {
			t.Fatalf("a team definition with a starter: %d %s", w.Code, w.Body)
		}
		var n int
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
		if n != 0 {
			t.Fatalf("%d project(s) made", n)
		}
		// without it, as ever
		body["starter"] = map[string]any{"build": false, "wiki": false}
		if w := callAs(t, fx.mux, asAlice, "POST", "/projects", body); w.Code != 201 {
			t.Fatalf("a team definition with nothing asked: %d %s", w.Code, w.Body)
		}
	})
	t.Run("no sandbox", func(t *testing.T) {
		pol := defaultProjectPolicy()
		err := checkStarter(who{kind: whoUser, user: "alice", level: "read"}, projPersonal, false, pol, nil, &starterReq{Build: true})
		if pe, ok := err.(*projErr); !ok || pe.code != 409 || pe.refusal != "starter" {
			t.Fatalf("no sandbox: %v", err)
		}
	})
	t.Run("a component's wiki", func(t *testing.T) {
		err := checkStarter(who{kind: whoElement, el: "apps/other"}, projPersonal, true, defaultProjectPolicy(), nil, &starterReq{Wiki: true})
		if pe, ok := err.(*projErr); !ok || pe.code != 409 || !strings.Contains(pe.msg, "a person's own") {
			t.Fatalf("a component's wiki: %v", err)
		}
		if err := checkStarter(who{kind: whoElement, el: "apps/other"}, projPersonal, true, defaultProjectPolicy(), nil, &starterReq{Build: true}); err != nil {
			t.Fatalf("a component's build: %v", err)
		}
	})
	t.Run("the coordinator's class kept for managers", func(t *testing.T) {
		fx := newProjFix(t)
		st := currentClasses()
		var list []agentClass
		for _, c := range st.list {
			if c.ID == classWeb {
				c.Who = "managers"
			}
			list = append(list, c)
		}
		old := classStore.Load()
		t.Cleanup(func() { classStore.Store(old) })
		classStore.Store(&classState{list: list, stored: st.stored, def: st.def})
		w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(map[string]any{"starter": map[string]any{"wiki": true}}))
		if w.Code != 403 || !strings.Contains(w.Body.String(), "for the agent's managers only") {
			t.Fatalf("the wiki without a coordinator: %d %s", w.Code, w.Body)
		}
	})
}

// No starter, or nothing asked: as before — no record, no task, no
// coordinator, no `starter` in the answer.
func TestProjectStarterOptional(t *testing.T) {
	fx := newProjFix(t)
	d := fx.ag.db
	for i, starter := range []map[string]any{nil, {"build": false, "wiki": false}} {
		p, st := newStarterProject(t, fx, starter)
		if st != nil {
			t.Fatalf("%d: the answer's starter: %v", i, st)
		}
		fx.waitRepoReady(t, p.ID)
		waitJobsDone(t, fx, p.ID)
		if _, ok := d.starterOf(p.ID); ok {
			t.Fatalf("%d: a starter record", i)
		}
		if _, err := d.taskByN(p.ID, 1); err == nil {
			t.Fatalf("%d: a task", i)
		}
		if id := d.coordRunOf(p.ID, "alice"); id != 0 {
			t.Fatalf("%d: a coordinator", i)
		}
		if evs := starterEvents(d, p.ID); len(evs) != 0 {
			t.Fatalf("%d: starter events %q", i, evs)
		}
	}
}

// The message never waits for good: a dream round that doesn't end is
// waited for starterDreamWait at most; a coding agent's round is over when
// its ingest is (starterDreamOver), never at its dream turn's end.
func TestProjectStarterDreamWaits(t *testing.T) {
	oldWait, oldPoll := starterDreamWait, starterPoll
	starterDreamWait, starterPoll = 600*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { starterDreamWait, starterPoll = oldWait, oldPoll })
	fx := newProjFix(t)
	coordWakerReset()
	t.Cleanup(coordWakerReset)
	d := fx.ag.db
	fake := fakeOf(fx.ag)
	fake.on(func(r LLMRequest) bool {
		u := userTexts(r)
		return len(u) > 0 && strings.HasPrefix(u[len(u)-1], "[dream round")
	}, nil).block("dream")
	t.Cleanup(func() { fake.release("dream") })
	p, _ := newStarterProject(t, fx, map[string]any{"build": true, "wiki": true})
	var k *ProjectTask
	hwait(t, "task #1", func() bool { k, _ = d.taskByN(p.ID, 1); return k != nil })
	hwait(t, "its dream round to start", func() bool { return d.dreamOf(k.ID).Active })
	r, _ := d.starterOf(p.ID)
	if r.Wiki != stDream {
		t.Fatalf("the record while it dreams: %+v", r)
	}
	var coord int64
	hwait(t, "the coordinator's starter message", func() bool {
		coord = d.coordRunOf(p.ID, "alice")
		return coord != 0 && len(starterRows(d, coord)) == 1
	})
	if since := nowMs() - r.DreamMs; since < starterDreamWait.Milliseconds() {
		t.Fatalf("the message went %d ms after the round was queued, before its wait was up", since)
	}
	if !d.dreamOf(k.ID).Active {
		t.Fatal("the round ended: the wait wasn't what sent it")
	}

	// a coding agent's dream turn ending isn't its pages being in
	_ = d.Tx(func(tx *DB) error {
		return tx.putStarter(p.ID, starterRec{By: "alice", Build: stDone, Wiki: stDream, Task: 1, DreamMs: nowMs()})
	})
	run, _ := d.getRun(k.RunID)
	run.Engine = engineHarness
	_ = d.Tx(func(tx *DB) error { starterTurnEnd(tx, run, turnAnswered, "", ""); return nil })
	if r, _ := d.starterOf(p.ID); r.Wiki != stDream {
		t.Fatalf("a coding agent's dream turn moved it: %+v", r)
	}
	_ = d.Tx(func(tx *DB) error { starterDreamOver(tx, p.ID, 2); return nil })
	if r, _ := d.starterOf(p.ID); r.Wiki != stDream {
		t.Fatalf("another task's ingest moved it: %+v", r)
	}
	_ = d.Tx(func(tx *DB) error { starterDreamOver(tx, p.ID, 1); return nil })
	if r, _ := d.starterOf(p.ID); r.Wiki != stDue {
		t.Fatalf("its ingest over: %+v", r)
	}
	hwait(t, "the record's message sent", func() bool { r, _ := d.starterOf(p.ID); return r.Wiki == stDone })
	if n := len(starterRows(d, coord)); n != 1 {
		t.Fatalf("%d starter messages: one coordinator inbox row per project, whatever the record says", n)
	}
}
