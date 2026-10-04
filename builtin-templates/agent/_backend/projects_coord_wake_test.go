package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// A coordinator that can't take a wake — waiting for its person, failed,
// its project archived, its person removed — leaves no event asking for
// one: otherwise the person's partition counts it as work (projectsWake)
// that nothing ever takes up, and never sleeps. The events stay
// undelivered, for its next turn.
func TestCoordWakeHeld(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	fx.addTask(t, fx.p, 1, "alice")
	rawEvent := func(user string) int64 { // an undelivered event asking for a wake, no hooks run
		t.Helper()
		r, err := fx.ag.db.q.Exec(`INSERT INTO project_events (project_id, n, kind, body, wake, coord_user, created)
			VALUES (?, 1, 'task.state', '{"text":"failed: boom"}', 1, ?, ?)`, fx.p.ID, user, nowMs())
		if err != nil {
			t.Fatal(err)
		}
		id, _ := r.LastInsertId()
		return id
	}
	wakeOf := func(id int64) (wake, delivered int64) {
		_ = fx.ag.db.q.QueryRow(`SELECT wake, delivered FROM project_events WHERE id=?`, id).Scan(&wake, &delivered)
		return
	}
	sleeps := func(what string) {
		t.Helper()
		if runnable, _ := fx.ag.db.projectsWake(time.Now()); runnable {
			t.Fatalf("%s: the person's partition still has a coordinator's wake to take up", what)
		}
	}

	// the check means something: an idle coordinator's event is work
	id := rawEvent("alice")
	if runnable, _ := fx.ag.db.projectsWake(time.Now()); !runnable {
		t.Fatal("an idle coordinator's undelivered wake isn't work for the partition")
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE project_events SET delivered=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}

	// waiting for its person: an event already there, and one written after
	id = rawEvent("alice")
	if err := fx.ag.db.setStatusOnly(run.ID, statusWaiting); err != nil {
		t.Fatal(err)
	}
	ev := addProjectEventTx(t, fx.ag.db, fx.p.ID, 1, pevTaskState, "waiting for a person", true)
	for _, e := range []int64{id, ev.ID} {
		if w, d := wakeOf(e); w != 0 || d != 0 {
			t.Fatalf("waiting: event %d wake=%d delivered=%d", e, w, d)
		}
	}
	sleeps("waiting for its person")

	// in error
	if err := fx.ag.db.setStatusOnly(run.ID, statusIdle); err != nil {
		t.Fatal(err)
	}
	id = rawEvent("alice")
	if err := fx.ag.db.setStatusOnly(run.ID, statusError); err != nil {
		t.Fatal(err)
	}
	if w, _ := wakeOf(id); w != 0 {
		t.Fatal("error: the event still asks for a wake")
	}
	ev = addProjectEventTx(t, fx.ag.db, fx.p.ID, 1, pevTaskState, "failed again", true)
	if w, _ := wakeOf(ev.ID); w != 0 {
		t.Fatal("error: an event written after still asks for a wake")
	}
	sleeps("in error")

	// the project archived: no wake (every project tool would refuse)
	if err := fx.ag.db.setStatusOnly(run.ID, statusIdle); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE projects SET state=? WHERE id=?`, projArchived, fx.p.ID); err != nil {
		t.Fatal(err)
	}
	id = rawEvent("alice")
	cur, _ := fx.ag.db.getRun(run.ID)
	if coordWakes(fx.ag.db, cur) {
		t.Fatal("a coordinator of an archived project was woken")
	}
	if w, d := wakeOf(id); w != 0 || d != 0 {
		t.Fatalf("archived: wake=%d delivered=%d", w, d)
	}
	ev = addProjectEventTx(t, fx.ag.db, fx.p.ID, 1, pevTaskState, "shelved", true)
	if w, _ := wakeOf(ev.ID); w != 0 {
		t.Fatal("archived: a new event still asks for a wake")
	}
	sleeps("archived")
	if _, err := fx.ag.db.q.Exec(`UPDATE projects SET state=? WHERE id=?`, projActive, fx.p.ID); err != nil {
		t.Fatal(err)
	}

	// its person removed
	if _, err := fx.ag.db.q.Exec(`INSERT INTO project_members (project_id, user, role) VALUES (?, 'carol', ?)`, fx.p.ID, roleParticipant); err != nil {
		t.Fatal(err)
	}
	projACL.flush(fx.p.ID)
	carol := fx.coordinator(t, asCarol, fx.p.ID)
	if _, err := fx.ag.db.q.Exec(`DELETE FROM project_members WHERE project_id=? AND user='carol'`, fx.p.ID); err != nil {
		t.Fatal(err)
	}
	projACL.flush(fx.p.ID)
	id = rawEvent("carol")
	cur, _ = fx.ag.db.getRun(carol.ID)
	if coordWakes(fx.ag.db, cur) {
		t.Fatal("a removed person's coordinator was woken")
	}
	if w, _ := wakeOf(id); w != 0 {
		t.Fatalf("removed: event %d still asks for a wake", id)
	}
	sleeps("its person removed")
}

// task_create and task_cancel write nothing once another engine owns the
// database: the fence is checked before P1's helpers open their
// transactions.
func TestCoordWritesFenced(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	fx.addTask(t, fx.p, 1, "alice")
	fx.addTask(t, fx.p, 2, "alice")
	fx.addTask(t, fx.p, 3, "alice")
	// before: task_cancel's reason, and none (no dangling colon)
	if out, err := fx.tool(t, run, "task_cancel", map[string]any{"tasks": []int64{2}}); err != nil || !strings.Contains(out, "#2: stopped") {
		t.Fatalf("task_cancel #2: %v %s", err, out)
	}
	if out, err := fx.tool(t, run, "task_cancel", map[string]any{"tasks": []int64{3}, "reason": " a duplicate of #2 "}); err != nil || !strings.Contains(out, "#3: stopped") {
		t.Fatalf("task_cancel #3: %v %s", err, out)
	}
	texts := map[int64]string{}
	for _, ev := range fx.ag.db.projectEvents(fx.p.ID, 0, 100) {
		if ev.Kind == pevTaskCancel {
			var b struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(ev.Body, &b)
			texts[ev.N] = b.Text
		}
	}
	if !strings.HasSuffix(texts[2], "cancelled by the project coordinator") || !strings.HasSuffix(texts[3], "cancelled by the project coordinator: a duplicate of #2") {
		t.Fatalf("the cancel events: %v", texts)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE settings SET v=CAST(v AS INTEGER)+1 WHERE k=?`, fx.ag.eng.epochName()); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.tool(t, run, "task_create", map[string]any{"tasks": []map[string]any{{"brief": "fix login"}}}); err != errFenced {
		t.Fatalf("task_create after a takeover: %v", err)
	}
	if n := countTasks(fx.ag.db, fx.p.ID); n != 3 {
		t.Fatalf("%d tasks after a fenced task_create", n)
	}
	if _, err := fx.tool(t, run, "task_cancel", map[string]any{"tasks": []int64{1}}); err != errFenced {
		t.Fatalf("task_cancel after a takeover: %v", err)
	}
	if k, _ := fx.ag.db.taskByN(fx.p.ID, 1); k.Phase != phaseOpen {
		t.Fatalf("a fenced task_cancel stopped the task: %s", k.Phase)
	}
}
