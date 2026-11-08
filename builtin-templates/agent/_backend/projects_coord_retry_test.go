package main

import (
	"strings"
	"testing"
)

// task_retry retries a failed workspace (its failed jobs queued again, as
// Retry does) or a turn that errored (a wake, as the Resume button), and
// nothing else.
func TestCoordRetryTool(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	d := fx.ag.db
	k1, _ := fx.addTask(t, fx.p, 1, "alice")
	if _, err := fx.tool(t, run, "task_retry", map[string]any{"task": 1}); err == nil || !strings.Contains(err.Error(), "retries only") {
		t.Fatalf("task_retry on a ready task: %v", err)
	}
	// a failed workspace
	k2, _ := fx.addTask(t, fx.p, 2, "alice")
	if _, err := d.queueJob(fx.p.ID, k2.ID, "", pjPrepare, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := d.q.Exec(`UPDATE project_jobs SET state='failed', error='boom' WHERE task_id=?`, k2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := d.q.Exec(`UPDATE project_tasks SET ws='failed', error='boom' WHERE id=?`, k2.ID); err != nil {
		t.Fatal(err)
	}
	out, err := fx.tool(t, run, "task_retry", map[string]any{"task": 2})
	if err != nil || !strings.Contains(out, "failed steps run again") {
		t.Fatalf("task_retry on a failed workspace: %v %s", err, out)
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_jobs WHERE task_id=? AND kind=?`, k2.ID, pjPrepare).Scan(&n)
	if k, _ := d.taskByN(fx.p.ID, 2); n < 2 || k.WS == wsFailed {
		t.Fatalf("the retry queued %d prepare jobs, its workspace %s", n, k.WS)
	}
	// a turn that errored
	_, r3 := fx.addTask(t, fx.p, 3, "alice")
	if err := d.setStatus(r3.ID, statusError, 0, "the model call failed", ""); err != nil {
		t.Fatal(err)
	}
	out, err = fx.tool(t, run, "task_retry", map[string]any{"task": 3})
	if err != nil || !strings.Contains(out, "last turn runs again") {
		t.Fatalf("task_retry on an errored turn: %v %s", err, out)
	}
	hwait(t, "the errored turn woken", func() bool {
		var wakes int
		_ = d.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind=?`, r3.ID, inboxWake).Scan(&wakes)
		r, _ := d.getRun(r3.ID)
		return wakes > 0 || r.Status != statusError
	})
	// a task waiting for a person, and one that is over: refused
	_, r4 := fx.addTask(t, fx.p, 4, "alice")
	if err := d.setStatus(r4.ID, statusWaiting, 0, "which one?", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.tool(t, run, "task_retry", map[string]any{"task": 4}); err == nil || !strings.Contains(err.Error(), "waiting for a person") {
		t.Fatalf("task_retry on a task waiting for a person: %v", err)
	}
	if _, err := d.q.Exec(`UPDATE project_tasks SET phase='closed' WHERE id=?`, k1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.tool(t, run, "task_retry", map[string]any{"task": 1}); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("task_retry on a closed task: %v", err)
	}
	// offered to the coordinator, asked first in approval mode
	cfg, _ := d.runConfig(run.ID)
	if n := toolNamesIn(runToolSpecs(cfg, run, nil)); !n["task_retry"] || !coordWrites["task_retry"] {
		t.Fatalf("task_retry offered %v, a write %v", n["task_retry"], coordWrites["task_retry"])
	}
}
