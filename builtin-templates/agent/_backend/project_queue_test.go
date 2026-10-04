package main

import (
	"fmt"
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
	_ = fx.ag.db.putSetting("halt", "")
	projectPump(p.ID)
	waitFor(t, "task 3's start", func() bool { return startDelivered(fx, p.ID, 3) })
	waitFor(t, "task 3 holding the freed slot", func() bool { return fx.ag.db.slotsUsed(p.ID) == 2 })
}

// A run waiting for a person holds its slot (V5).
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
	// it rests: its slot is free, and the status change runs the pump
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
		t.Fatalf("the held input left the queue while its run waits for a person: %+v", ins)
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
