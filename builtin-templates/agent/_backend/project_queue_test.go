package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// heldProject is a project whose workspace never gets ready (its setup
// waits for a file nobody makes): its tasks' runs park at the gate —
// holding their slots — so the queue is all that moves.
func heldProject(t *testing.T, fx *projFix, maxTasks int) ProjectView {
	t.Helper()
	script, _ := holdSetup(fx)
	return fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": script}},
		"policy": map[string]any{"maxTasks": maxTasks}})
}

// startDelivered: task n's start reached its run's inbox (or the run took it).
func startDelivered(fx *projFix, pid, n int64) bool {
	for _, in := range fx.ag.db.queuedInputs(pid, n) {
		if in.Kind == "start" {
			return false
		}
	}
	return true
}

// Starts go oldest first, while fewer than maxTasks tasks hold a slot; a
// slot freed lets the next one in.
func TestQueueFIFOAndSlots(t *testing.T) {
	fx := newProjFix(t)
	p := heldProject(t, fx, 2)
	var runs []int64
	for i := 1; i <= 3; i++ {
		_, id := fx.newTask(t, asAlice, p.ID, map[string]any{"text": fmt.Sprintf("task %d", i)})
		runs = append(runs, id)
	}
	waitFor(t, "two tasks holding slots", func() bool { return fx.ag.db.slotsUsed(p.ID) == 2 })
	if !startDelivered(fx, p.ID, 1) || !startDelivered(fx, p.ID, 2) || startDelivered(fx, p.ID, 3) {
		t.Fatalf("starts: 1 %v, 2 %v, 3 %v", startDelivered(fx, p.ID, 1), startDelivered(fx, p.ID, 2), startDelivered(fx, p.ID, 3))
	}
	k3, _ := fx.ag.db.taskByN(p.ID, 3)
	if v := fx.ag.db.projTaskView(nil, k3); v.WaitingFor != "slot" || v.Column != colQueued {
		t.Fatalf("task 3 waits for: %q (%s)", v.WaitingFor, v.Column)
	}
	// the halt: nothing starts
	_ = fx.ag.db.putSetting("halt", "1")
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/1/cancel", p.ID), map[string]any{}); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	waitStatus(t, fx.ag.db, runs[0], statusCanceled)
	time.Sleep(50 * time.Millisecond)
	if startDelivered(fx, p.ID, 3) {
		t.Fatal("the pump started a task while the agent is halted")
	}
	// a person's message lifts the halt: the worker sees it and pumps the
	// queue (nothing here runs the pump by hand)
	if !fx.ag.resumeIfHalted(0) {
		t.Fatal("the halt stayed")
	}
	waitFor(t, "task 3's start", func() bool { return startDelivered(fx, p.ID, 3) })
	waitFor(t, "task 3 holding the freed slot", func() bool { return fx.ag.db.slotsUsed(p.ID) == 2 })
}

// A run waiting for a person holds its slot.
func TestWaitingRunHoldsSlot(t *testing.T) {
	fx := newProjFix(t)
	p := heldProject(t, fx, 1)
	_, r1 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "one"})
	waitStatus(t, fx.ag.db, r1, statusSleep)
	fx.newTask(t, asAlice, p.ID, map[string]any{"text": "two"})
	// task 1 now waits for a person (an approval, say)
	if err := fx.ag.db.setStatus(r1, statusWaiting, 0, "may I?", `{"kind":"approval"}`); err != nil {
		t.Fatal(err)
	}
	projectPump(p.ID)
	time.Sleep(50 * time.Millisecond)
	if startDelivered(fx, p.ID, 2) {
		t.Fatal("a waiting task's slot was given away")
	}
	// it takes its input up and rests: its slot is free, and the status
	// change runs the pump
	_, _ = fx.ag.db.q.Exec(`UPDATE inbox SET delivered_at=? WHERE run_id=?`, now(), r1)
	if err := fx.ag.db.setStatus(r1, statusIdle, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "task 2's start", func() bool { return startDelivered(fx, p.ID, 2) })
}

// An input held for a park (the coordinator's, an event's) waits while its
// run waits for a person, and is delivered once it doesn't.
func TestHoldParkInput(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	_, r1 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "one"})
	waitFor(t, "the first turn", func() bool {
		r, _ := fx.ag.db.getRun(r1)
		return r.Status == statusIdle && len(fakeOf(fx.ag).callsFor(r1)) == 1
	})
	// it asks the person something (ask_user's park)
	if err := fx.ag.db.setStatus(r1, statusWaiting, 0, "which one?", ""); err != nil {
		t.Fatal(err)
	}
	err := fx.ag.db.Tx(func(t2 *DB) error {
		_, err := queueTaskInput(t2, taskInput{Project: p.ID, N: 1, Kind: "input", Text: "the coordinator says hi", Source: srcCoordinator, HoldPark: true})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if ins := fx.ag.db.queuedInputs(p.ID, 1); len(ins) != 1 {
		r, _ := fx.ag.db.getRun(r1)
		var b strings.Builder
		for _, row := range fx.ag.db.inboxRows(`WHERE run_id=? ORDER BY id`, r1) {
			fmt.Fprintf(&b, "\n  inbox %d %s %q delivered=%d", row.ID, row.Kind, clip(row.Body.Text, 60), row.DeliveredAt)
		}
		steps, _ := fx.ag.db.steps(r1)
		for _, st := range steps {
			fmt.Fprintf(&b, "\n  step %s %s", st.Kind, clip(st.Detail, 100))
		}
		t.Fatalf("the held input left the queue while its run waits for a person: %+v; run %s %q%s", ins, r.Status, r.Pending, b.String())
	}
	// a dedupe key queues once
	_ = fx.ag.db.Tx(func(t2 *DB) error {
		for i := 0; i < 2; i++ {
			_, _ = queueTaskInput(t2, taskInput{Project: p.ID, N: 1, Kind: "input", Text: "ci failed", Source: srcEvent, HoldPark: true, Dedupe: "ci:1:abc"})
		}
		return nil
	})
	if ins := fx.ag.db.queuedInputs(p.ID, 1); len(ins) != 2 {
		t.Fatalf("a dedupe key queued twice: %d", len(ins))
	}
	if err := fx.ag.db.setStatus(r1, statusIdle, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the held inputs delivered", func() bool { return len(fx.ag.db.queuedInputs(p.ID, 1)) == 0 })
	var framed bool
	for _, r := range fx.ag.db.inboxRows(`WHERE run_id=?`, r1) {
		framed = framed || (r.Body.Source == srcCoordinator && r.Body.Text == "[message from the project coordinator]\nthe coordinator says hi")
	}
	if !framed {
		t.Fatalf("the coordinator's input, framed: %+v", fx.ag.db.inboxRows(`WHERE run_id=?`, r1))
	}
	k, _ := fx.ag.db.taskByN(p.ID, 1)
	if k.TurnBy != srcEvent {
		t.Fatalf("turn_by: %q", k.TurnBy)
	}
}

// Pumps back to back never give out more than maxTasks slots: a start
// delivered but not yet taken up by its run holds its slot.
func TestPumpsBackToBack(t *testing.T) {
	fx := newProjFix(t)
	p := heldProject(t, fx, 2)
	for i := 1; i <= 6; i++ {
		fx.newTask(t, asAlice, p.ID, map[string]any{"text": fmt.Sprintf("task %d", i)})
	}
	waitFor(t, "two runs parked", func() bool { return fx.ag.db.slotsUsed(p.ID) == 2 })
	delivered := func() int { return 6 - len(fx.ag.db.queuedInputs(p.ID, 0)) }
	if n := delivered(); n != 2 {
		t.Fatalf("delivered with two slots: %d", n)
	}
	// two more slots, written past the route (whose change would pump):
	// three pumps at once fill them, and no more
	if _, err := fx.ag.db.q.Exec(`UPDATE projects SET policy=json_set(policy, '$.maxTasks', 4) WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		projectPump(p.ID)
	}
	if n, used := delivered(), fx.ag.db.slotsUsed(p.ID); n != 4 || used != 4 {
		t.Fatalf("three pumps at once: %d starts delivered, %d slots used (max 4)", n, used)
	}
	waitFor(t, "four runs parked", func() bool {
		n := 0
		ks, _ := fx.ag.db.tasksWhere(`WHERE project_id=?`, p.ID)
		for _, k := range ks {
			if r, err := fx.ag.db.getRun(k.RunID); err == nil && r.Status == statusSleep {
				n++
			}
		}
		return n == 4
	})
	for i := 0; i < 3; i++ {
		projectPump(p.ID)
	}
	if n := delivered(); n != 4 {
		t.Fatalf("after the runs took their starts: %d delivered", n)
	}
}
