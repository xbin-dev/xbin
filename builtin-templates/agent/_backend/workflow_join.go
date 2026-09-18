// workflow_join.go — starting background runs and collecting their results.
//
// The invariant everything here rests on: **a child never writes into its
// parent's transcript.** A settling child only marks its dependency rows
// settled and kicks the dispatcher; the parent delivers, from its own drive, in
// one pass. That is what makes five children finishing together produce ONE
// parent drive with five results instead of five drives, makes delivery
// transactional and idempotent, and keeps two goroutines from racing on
// nextSeq (which is SELECT MAX(seq) then INSERT, and is not atomic).
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// Dependency edge states.
const (
	depPending   = "pending"
	depSatisfied = "satisfied"
	depFailed    = "failed"
	depCanceled  = "canceled"
	depTimeout   = "timeout"
)

// Run outcomes, distinct from status: a run can be idle after answering, and a
// caller needs to tell "answered" from "was killed".
const (
	outcomeDone       = "done"
	outcomeAnswered   = "answered"
	outcomeError      = "error"
	outcomeCanceled   = "canceled"
	outcomeIncomplete = "incomplete"
)

// A delivered child result is clipped harder than a normal tool result: eight
// children at the 16 KiB tool cap would be a compaction storm in a 12k budget.
// The full text stays available through workflow_result.
const maxChildResult = 4 << 10

// --- edges ---------------------------------------------------------------

func (d *DB) addDep(waiterID, depID int64, kind, toolCallID, onError string) error {
	_, err := d.sql.Exec(
		`INSERT OR REPLACE INTO run_deps (run_id, dep_id, kind, state, delivered, tool_call_id, on_error, created, settled)
		 VALUES (?, ?, ?, ?, 0, ?, ?, ?, 0)`,
		waiterID, depID, kind, depPending, toolCallID, onError, now())
	return err
}

type runDep struct {
	RunID, DepID int64
	Kind, State  string
	ToolCallID   string
	OnError      string
	Delivered    bool
}

// undeliveredDeps returns settled dependencies whose result the waiter has not
// been handed yet, oldest first.
func (d *DB) undeliveredDeps(waiterID int64) ([]runDep, error) {
	rows, err := d.sql.Query(
		`SELECT run_id, dep_id, kind, state, tool_call_id, on_error FROM run_deps
		 WHERE run_id=? AND delivered=0 AND state<>? ORDER BY created, dep_id`, waiterID, depPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []runDep
	for rows.Next() {
		var e runDep
		if err := rows.Scan(&e.RunID, &e.DepID, &e.Kind, &e.State, &e.ToolCallID, &e.OnError); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) openDepCount(waiterID int64) int {
	var n int
	_ = d.sql.QueryRow(`SELECT count(*) FROM run_deps WHERE run_id=? AND kind='await' AND state=?`,
		waiterID, depPending).Scan(&n)
	return n
}

// waitersOf lists the runs waiting on this one — the reverse lookup the edge
// table exists for.
func (d *DB) waitersOf(depID int64) []int64 {
	rows, err := d.sql.Query(`SELECT run_id FROM run_deps WHERE dep_id=?`, depID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// deliverDep writes a dependency's result into the waiter's transcript and
// marks it delivered IN ONE TRANSACTION. Two statements would let a crash
// between them deliver the same result twice; `delivered=0` being durable is
// the whole idempotency story.
func (d *DB) deliverDep(waiterID, depID int64, toolCallID, content string) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	if toolCallID != "" {
		// Rewrite the placeholder written when the parent parked, so the result
		// lands in CALL order rather than completion order.
		var id int64
		err = tx.QueryRow(
			`SELECT id FROM messages WHERE run_id=? AND role='tool' AND tool_call_id=? ORDER BY seq DESC LIMIT 1`,
			waiterID, toolCallID).Scan(&id)
		if err == nil {
			_, err = tx.Exec(`UPDATE messages SET content=?, tokens=? WHERE id=?`, content, estimateTokens(content), id)
			if err == nil {
				if _, e := tx.Exec(`UPDATE messages_fts SET content=? WHERE msg_id=?`, content, id); e != nil {
					_, _ = tx.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`, content, waiterID, id)
				}
			}
		} else if err == sql.ErrNoRows {
			err = nil
			toolCallID = "" // no placeholder: fall through to a plain message
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if toolCallID == "" {
		// Fire-and-forget delivery. role:"user" because assembleContext skips
		// system messages and a tool result with no matching call is invalid.
		seq := 0
		var n sql.NullInt64
		_ = tx.QueryRow(`SELECT MAX(seq) FROM messages WHERE run_id=?`, waiterID).Scan(&n)
		if n.Valid {
			seq = int(n.Int64) + 1
		}
		res, err := tx.Exec(
			`INSERT INTO messages (run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created)
			 VALUES (?, ?, 'user', ?, '', '', '', ?, 0, ?)`,
			waiterID, seq, content, estimateTokens(content), now())
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		id, _ := res.LastInsertId()
		if _, err := tx.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`, content, waiterID, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE run_deps SET delivered=1 WHERE run_id=? AND dep_id=?`, waiterID, depID); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// --- settling ------------------------------------------------------------

// settle records a run's terminal verdict exactly once and releases whoever was
// waiting on it. Idempotent via the settled_at=0 guard, because it is reachable
// from the finish branch, the failure path, cancellation and the legacy
// end-of-drive path.
func (ag *Agent) settle(runID int64, outcome, result string) {
	res, err := ag.db.sql.Exec(
		`UPDATE runs SET outcome=?, settled_at=?, result=CASE WHEN ?<>'' THEN ? ELSE result END, updated=?
		 WHERE id=? AND settled_at=0`,
		outcome, now(), result, result, now(), runID)
	if err != nil {
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return // already settled
	}
	state := depSatisfied
	switch outcome {
	case outcomeError, outcomeIncomplete:
		state = depFailed
	case outcomeCanceled:
		state = depCanceled
	}
	_, _ = ag.db.sql.Exec(
		`UPDATE run_deps SET state=?, settled=? WHERE dep_id=? AND state=?`, state, now(), runID, depPending)
	for _, w := range ag.db.waitersOf(runID) {
		ag.db.journal(w, "child_settled", map[string]any{"runId": runID, "outcome": outcome})
	}
	ag.kick()
}

// deliverSettledDeps hands over every settled-but-undelivered dependency
// result. Called at the top of each loop iteration, so a child that settles
// while its parent is already running is usually absorbed by the drive in
// flight rather than needing another one.
func (ag *Agent) deliverSettledDeps(runID int64) int {
	deps, err := ag.db.undeliveredDeps(runID)
	if err != nil || len(deps) == 0 {
		return 0
	}
	budget := inlineBudget(len(deps))
	n := 0
	for _, e := range deps {
		child, cerr := ag.db.getRun(e.DepID)
		var content string
		if cerr != nil {
			content = fmt.Sprintf("run #%d is gone (deleted before its result was collected)", e.DepID)
		} else {
			content = formatChildResult(child, e.State, budget, e.ToolCallID == "")
		}
		if ag.db.deliverDep(runID, e.DepID, e.ToolCallID, content) == nil {
			n++
		}
	}
	return n
}

// inlineBudget splits one report between the runs being reported, so N children
// cost one report rather than N × the tool-result cap.
func inlineBudget(n int) int {
	if n < 1 {
		n = 1
	}
	b := (8 << 10) / n
	if b < 512 {
		b = 512
	}
	if b > maxChildResult {
		b = maxChildResult
	}
	return b
}

// formatChildResult renders one settled run for its waiter. standalone marks a
// fire-and-forget delivery, which arrives as a user message and therefore has
// to say plainly that it is not the owner speaking.
func formatChildResult(child *Run, state string, budget int, standalone bool) string {
	body := strings.TrimSpace(child.Result)
	if body == "" {
		body = "(no result)"
	}
	if len(body) > budget {
		body = strings.ToValidUTF8(body[:budget], "") +
			fmt.Sprintf("\n…[%d bytes elided — workflow_result %d for the whole thing]", len(body)-budget, child.ID)
	}
	label := child.Title
	if label == "" {
		label = fmt.Sprintf("run #%d", child.ID)
	}
	head := fmt.Sprintf("--- #%d %s (%s) ---", child.ID, label, outcomeWord(child, state))
	if standalone {
		return "workflow: a background run you started has finished. " +
			"This is your workflow layer reporting, not the owner.\n" + head + "\n" + body
	}
	return head + "\n" + body
}

func outcomeWord(child *Run, state string) string {
	switch state {
	case depFailed:
		return "failed"
	case depCanceled:
		return "cancelled"
	case depTimeout:
		return "timed out"
	}
	if child.Outcome != "" {
		return child.Outcome
	}
	return "done"
}

// --- starting children ---------------------------------------------------

// childConfig derives a node's config from its parent's. The capability lane is
// re-normalized FROM THE PARENT and never from arguments — this is the one line
// where a lane-widening bug could live, so there is exactly one of it.
func childConfig(parent Config, system string) Config {
	c := parent
	c.Toolset = parent.toolset()
	c.System = parent.System
	if strings.TrimSpace(system) != "" {
		c.System = system
	}
	c.System += subagentContract
	// Config holds a map and a slice, so a value copy shares them with the
	// parent. Nothing mutates them today; closing it anyway.
	if parent.Features != nil {
		c.Features = make(map[string]bool, len(parent.Features))
		for k, v := range parent.Features {
			c.Features[k] = v
		}
	}
	if parent.MCP != nil {
		c.MCP = append([]MCPServer(nil), parent.MCP...)
	}
	return c
}

const subagentContract = "\n\nYou are a subagent. Your parent cannot see your work — only the result you " +
	"finish() with — so make it self-contained: the answer, the sources you used (ids/URLs/paths), and " +
	"anything you could not determine. Keep it under about 400 words unless the task asks for more. " +
	"You cannot ask the human anything; if you are blocked, finish() saying exactly what is missing."

// startChild creates a detached child run. awaitTC non-empty means the parent
// is parking on it and its result will rewrite that placeholder; empty means
// fire-and-forget. after lists runs that must settle first — those edges carry
// data, so the dependency's result is prepended to this child's task.
func (ag *Agent) startChild(parent *Run, cfg Config, task, system, label string, after []int64, awaitTC string) (int64, error) {
	if strings.TrimSpace(task) == "" {
		return 0, fmt.Errorf("task is required")
	}
	if ag.halted() {
		return 0, fmt.Errorf("the owner has halted this agent — no new background runs can start until they resume it")
	}
	rootID := parent.RootID
	if rootID == 0 {
		rootID = parent.ID
	}
	ok, spawned, max := ag.db.reserveSpawn(rootID, cfg.maxSpawn())
	if !ok {
		return 0, fmt.Errorf("this workflow has already created %d runs (its lifetime budget of %d). "+
			"Finish with what you have, or ask the owner to raise maxSpawn", spawned, max)
	}
	child := childConfig(cfg, system)
	cfgJSON, _ := json.Marshal(child)
	title := strings.TrimSpace(label)
	if title == "" {
		title = "subagent: " + clip(task, 60)
	}
	childID, err := ag.db.createRun(title, string(cfgJSON), parent.ID)
	if err != nil {
		ag.db.releaseSpawn(rootID)
		return 0, err
	}
	if _, err := ag.db.sql.Exec(`UPDATE runs SET detached=1 WHERE id=?`, childID); err != nil {
		return 0, err
	}
	_, _ = ag.db.addMessage(&Message{RunID: childID, Role: "system", Content: child.System})
	_, _ = ag.db.addMessage(&Message{RunID: childID, Role: "user", Content: task})

	// The parent waits on the child.
	if err := ag.db.addDep(parent.ID, childID, "await", awaitTC, "report"); err != nil {
		return 0, err
	}
	// ...and the child waits on whatever it was told to follow. These edges
	// carry data: deliverSettledDeps prepends each dependency's result to the
	// child's transcript before it runs, so "B after A" needs no parent turn.
	status := statusQueued
	for _, dep := range after {
		if dep == childID || dep == 0 {
			continue
		}
		if err := ag.db.addDep(childID, dep, "await", "", "report"); err == nil {
			status = statusBlocked
		}
	}
	if err := ag.db.setStatus(childID, status, 0, "", ""); err != nil {
		return 0, err
	}
	ag.db.journal(parent.ID, "spawn", map[string]any{
		"runId": childID, "task": clip(task, 200), "after": after, "detached": true,
	})
	ag.kick()
	return childID, nil
}

// parkForAwait puts the parent to sleep on its children. It holds NO drive slot
// while parked, which is what makes nested spawning impossible to deadlock.
func (ag *Agent) parkForAwait(run *Run, calls []toolCall) {
	pend, _ := json.Marshal(pending{Kind: "await", ToolCalls: calls})
	_ = ag.db.setStatus(run.ID, statusBlocked, 0, run.Result, string(pend))
	ag.db.journal(run.ID, "await", map[string]any{"calls": len(calls)})
}

// --- cancellation --------------------------------------------------------

// requestCancel stops a run and, optionally, everything below it. Durable by
// design: the old interrupt wrote to a RAM map, so a swap erased it and a
// sleeping descendant was resurrected by the next heartbeat. A stop button a
// restart undoes is not a stop button.
//
// Targets that are not currently running settle immediately; a running one is
// aborted through its context and confirmed by the loop's per-iteration check,
// with lease expiry as the backstop if the process died mid-cancel.
func (ag *Agent) requestCancel(runID int64, cascade bool, reason string) []int64 {
	targets := []int64{runID}
	if cascade {
		if kids, err := ag.db.descendants(runID); err == nil {
			targets = append(targets, kids...)
		}
	}
	msg := strings.TrimSpace(reason)
	if msg == "" {
		msg = "cancelled"
	}
	var stopped []int64
	for _, id := range targets {
		r, err := ag.db.getRun(id)
		if err != nil || r.SettledAt != 0 || terminalStatus(r.Status) {
			continue
		}
		_, _ = ag.db.sql.Exec(`UPDATE runs SET cancel_req=? WHERE id=? AND cancel_req=0`, now(), id)
		ag.abort(id) // aborts an in-flight LLM call immediately
		// Only leave the settle to a live drive if there IS one. readyRuns
		// filters cancel_req=0 and nothing ever clears the column, so a
		// cancelled run with nobody driving it would sit at `running` forever:
		// invisible to every dispatcher, its waiters blocked forever, and the
		// heartbeat pinned on by hasPending. Pressing halt is the easiest way
		// to reach that, since it cancels every live run at once.
		if r.Status != statusRunning || !ag.db.leaseHeld(id) {
			_ = ag.db.setStatus(id, statusCanceled, 0, msg, "")
			ag.settle(id, outcomeCanceled, msg)
		}
		ag.db.journal(id, "cancel", map[string]any{"reason": msg})
		stopped = append(stopped, id)
	}
	ag.kick()
	return stopped
}

// cancelDescendants is the rule that stops work outliving its consumer: a run
// reaching a terminal state takes its live children with it. A plain-answer
// idle is NOT terminal, so children there keep running and will deliver.
func (ag *Agent) cancelDescendants(runID int64, reason string) {
	kids, err := ag.db.descendants(runID)
	if err != nil || len(kids) == 0 {
		return
	}
	for _, id := range kids {
		r, err := ag.db.getRun(id)
		if err != nil || r.SettledAt != 0 || terminalStatus(r.Status) {
			continue
		}
		_, _ = ag.db.sql.Exec(`UPDATE runs SET cancel_req=? WHERE id=? AND cancel_req=0`, now(), id)
		ag.abort(id)
		// Same rule as requestCancel: delegate the settle to a live drive only
		// when one actually holds the run.
		if r.Status != statusRunning || !ag.db.leaseHeld(id) {
			_ = ag.db.setStatus(id, statusCanceled, 0, reason, "")
			ag.settle(id, outcomeCanceled, reason)
		}
	}
	ag.kick()
}

// cancelDescendantsCounted is cancelDescendants with a count, for handlers that
// report what they stopped.
func (ag *Agent) cancelDescendantsCounted(runID int64, reason string) int {
	before, _ := ag.db.descendants(runID)
	live := 0
	for _, id := range before {
		if r, err := ag.db.getRun(id); err == nil && r.SettledAt == 0 && !terminalStatus(r.Status) {
			live++
		}
	}
	ag.cancelDescendants(runID, reason)
	return live
}
