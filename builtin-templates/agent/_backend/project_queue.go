// project_queue.go — the queue and the pump (API.md §Projects and tasks,
// "Tasks at once"): a task's start and every message the coordinator or an
// scm event sends a task wait in project_queue; projectPump moves them,
// oldest first, into their runs' inboxes while fewer than policy.maxTasks
// tasks hold a slot (their run running, awaiting, sleeping or waiting for a
// person). An input marked hold_park waits while its run waits for a person
// (the coordinator never answers a park). A person's own message to a task
// bypasses the queue. Nothing moves while a manager's halt is on, or while
// the project isn't active.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// holdsSlot: a task run in this status holds one of its project's slots.
func holdsSlot(status string) bool {
	switch status {
	case statusRunning, statusQueued, statusBlocked, statusAwait, statusSleep, statusWaiting:
		return true
	}
	return false
}

// slotsUsed is how many of project pid's tasks hold a slot now.
func (d *DB) slotsUsed(pid int64) int {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_tasks k JOIN runs r ON r.id=k.run_id
		WHERE k.project_id=? AND k.run_id<>0 AND r.status IN ('running','queued','blocked','awaiting','sleeping','waiting_input')`, pid).Scan(&n)
	return n
}

// queueTaskInput puts a task's start or a message to it in the queue (in
// the caller's transaction) and runs the pump after the commit. A dedupe
// key already queued answers 0 (nothing added).
func queueTaskInput(t *DB, in taskInput) (int64, error) {
	switch in.Kind {
	case "start", "input":
	default:
		return 0, fmt.Errorf("a queued input is a start or an input")
	}
	if in.Source == "" {
		in.Source = srcHuman
	}
	var id int64
	err := t.q.QueryRow(`INSERT INTO project_queue (project_id, n, kind, text, source, sender, hold_park, dedupe, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING RETURNING id`,
		in.Project, in.N, in.Kind, in.Text, in.Source, in.Sender, b2i(in.HoldPark), in.Dedupe, nowMs()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	pid := in.Project
	t.AfterCommit(func() { projectPump(pid) })
	if k, err := t.taskByN(in.Project, in.N); err == nil {
		if p, err := t.getProject(in.Project); err == nil {
			onTaskChange(t, p, k, "state")
		}
	}
	return id, nil
}

// queuedInputs is project pid's queue, oldest first (n 0: every task's).
func (d *DB) queuedInputs(pid, n int64) []taskInput {
	q := `SELECT id, project_id, n, kind, text, source, sender, hold_park, dedupe, created FROM project_queue WHERE project_id=?`
	args := []any{pid}
	if n > 0 {
		q += ` AND n=?`
		args = append(args, n)
	}
	rows, err := d.q.Query(q+` ORDER BY id`, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []taskInput
	for rows.Next() {
		var in taskInput
		var hold int
		if rows.Scan(&in.ID, &in.Project, &in.N, &in.Kind, &in.Text, &in.Source, &in.Sender, &hold, &in.Dedupe, &in.Created) == nil {
			in.HoldPark = hold != 0
			out = append(out, in)
		}
	}
	return out
}

// projectPumpAsync is the pump, run after the caller's commit (hooks call
// it from t.AfterCommit).
func projectPumpAsync(pid int64) { projectPump(pid) }

// projectPump moves queued inputs of project pid into their runs' inboxes,
// oldest first, while slots last. Runs after a turn ends, whenever a task
// run's status changes, after a create, a queue insert and a policy change.
func projectPump(pid int64) {
	ag := projAg()
	if ag == nil || ag.db.getSetting("halt") == "1" {
		return
	}
	var poke []int64
	err := ag.db.Tx(func(t *DB) error {
		poke = nil
		p, err := t.getProject(pid)
		if err != nil || p.State != projActive {
			return nil
		}
		items := t.queuedInputs(pid, 0)
		if len(items) == 0 {
			return nil
		}
		holders, max := t.slotsUsed(pid), policyOf(p.Policy).MaxTasks
		for _, in := range items {
			k, err := t.taskByN(pid, in.N)
			if err != nil || k.RunID == 0 {
				_, _ = t.q.Exec(`DELETE FROM project_queue WHERE id=?`, in.ID)
				continue
			}
			run, err := t.getRun(k.RunID)
			if err != nil {
				_, _ = t.q.Exec(`DELETE FROM project_queue WHERE id=?`, in.ID)
				continue
			}
			if in.HoldPark && run.Status == statusWaiting {
				continue // the person answers first
			}
			if !holdsSlot(run.Status) {
				if holders >= max {
					continue
				}
				holders++
			}
			if err := deliverTaskInput(t, p, k, run, in); err != nil {
				return err
			}
			poke = append(poke, run.ID)
		}
		return nil
	})
	if err != nil {
		logf("project %d: the pump: %v", pid, err)
		return
	}
	if ag.eng != nil {
		for _, id := range poke {
			ag.eng.Poke(id)
		}
	}
}

// deliverTaskInput writes one queued input into its run's inbox (a coding
// agent's: a prompt) and drops it from the queue. The coordinator's words
// are framed as its own; an event's text comes framed by its sender.
func deliverTaskInput(t *DB, p *Project, k *ProjectTask, run *Run, in taskInput) error {
	text := in.Text
	if in.Source == srcCoordinator {
		text = "[message from the project coordinator]\n" + text
	}
	body := inboxBody{Text: text, Source: in.Source, Sender: in.Sender, OriginID: p.ID, Label: p.Name}
	if in.Source == srcHuman {
		body.OriginID, body.Label = 0, ""
	}
	kind := inboxUser
	if run.Engine == engineHarness {
		kind = inboxHPrompt
	}
	cid := ""
	if in.Kind == "start" {
		cid = startClientID(p.ID, k.N) // a coding agent's start gets its brief once the workspace is ready (jobBind)
	}
	if _, _, err := t.enqueue(run.ID, kind, body, cid); err != nil {
		return err
	}
	if _, err := t.q.Exec(`DELETE FROM project_queue WHERE id=?`, in.ID); err != nil {
		return err
	}
	if k.TurnBy != in.Source {
		k.TurnBy = in.Source
		_ = t.setTask(k.ID, map[string]any{"turn_by": in.Source})
	}
	if projAg() != nil && projAg().eng != nil {
		projAg().eng.emitInbox(t, run.ID, run.ID)
	}
	onTaskChange(t, p, k, "state")
	return nil
}

// dropQueued forgets every queued input of task n (cancel, close).
func (d *DB) dropQueued(pid, n int64) {
	_, _ = d.q.Exec(`DELETE FROM project_queue WHERE project_id=? AND n=?`, pid, n)
}

// queueWords is a short view of what waits in the queue for a task (the
// task view's waitingFor "slot").
func queueWaits(items []taskInput, n int64) bool {
	for _, in := range items {
		if in.N == n {
			return true
		}
	}
	return false
}

// frameIssue is an issue's text for a task's first prompt: untrusted.
func frameIssue(host string, is *scmIssue) string {
	if is == nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "#%d %s\n\n%s", is.Number, is.Title, is.Body)
	return untrusted(host, "the issue this task starts from", b.String())
}
