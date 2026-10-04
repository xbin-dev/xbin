// project_events.go — what Projects hears from the engine and tells
// others (API.md §Projects and tasks): the call sites' helpers of the hook
// lists (turnEndHooks at every turn end, runStatusHooks at every status
// write, runViewHooks in a run's answers, runDeletedHooks in deleteOneRun —
// each skipping hosted conversations and a handle without the feature
// tables), Projects' own entries in them, project_events
// (addProjectEvent), the `project` stream event (coalesced per project,
// change and task for 250 ms; never replayed), and onTaskChange, which
// every change of a task's state goes through.
package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	turnEndHooks = append(turnEndHooks, projectTurnEnd)
	runStatusHooks = append(runStatusHooks, projectStatusChanged)
	runViewHooks = append(runViewHooks, projectRunView)
	runDeletedHooks = append(runDeletedHooks, projectRunDeleted)
	projectTaskChanged = func(t *DB, pid, n int64, what string) {
		p, err := t.getProject(pid)
		if err != nil {
			return
		}
		if k, err := t.taskByN(pid, n); err == nil {
			onTaskChange(t, p, k, what)
		}
	}
	projectRefsCheck = func(t *DB, pid, n int64) {
		if k, err := t.taskByN(pid, n); err == nil && k.RunID != 0 {
			if j, err := t.queueJob(pid, k.ID, "", pjRefs, "", 0); err == nil {
				// a PR event: its pull requests are read whatever prs holds
				_, _ = t.q.Exec(`UPDATE project_jobs SET client_id=? WHERE id=?`, refsReadPulls, j.ID)
			}
		}
	}
}

// The words turnEndHooks get for why a turn ended (projects_seams.go).
const (
	turnAnswered    = "answered"
	turnFinished    = "finished"
	turnIncomplete  = "incomplete"
	turnError       = "error"
	turnCanceled    = "canceled"
	turnInterrupted = "interrupted"
)

// turnWhy maps a built-in turn's end (actor.go's end* words) to the hooks'.
func turnWhy(why string) string {
	switch why {
	case endFinished:
		return turnFinished
	case endError:
		return turnError
	case endCap:
		return turnIncomplete
	}
	return turnAnswered
}

// harnessTurnWhy maps a coding agent's stop reason (endHarnessTurnTx) to
// the hooks' words.
func harnessTurnWhy(why string) string {
	switch why {
	case "end_turn", "":
		return turnAnswered
	case "cancelled":
		return turnInterrupted
	case "max_tokens", "max_turn_requests", "refusal":
		return turnIncomplete
	}
	return turnError
}

// --- the call sites' helpers ---------------------------------------------------------

// hooksOn: a run the hooks may see, in a handle that has the feature tables.
func hooksOn(t *DB, id int64) bool { return t != nil && t.features && !hostedID(id) }

// runTurnEnd runs turnEndHooks for run's turn end, inside its transaction.
func runTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if run == nil || !hooksOn(t, run.ID) {
		return
	}
	for _, h := range turnEndHooks {
		h(t, run, why, outcome, result)
	}
}

// runStatusChanged runs runStatusHooks after a run's status was written.
func runStatusChanged(t *DB, id int64, status string) {
	if !hooksOn(t, id) {
		return
	}
	for _, h := range runStatusHooks {
		h(t, id, status)
	}
}

// runViewExtras adds the hooks' keys to a run's answer (GET /runs/{id},
// GET /runs/{id}/view).
func runViewExtras(t *DB, w who, run *Run, v map[string]any) {
	if run == nil || !hooksOn(t, run.ID) {
		return
	}
	for _, h := range runViewHooks {
		h(t, w, run, v)
	}
}

// runDeleted runs runDeletedHooks inside deleteOneRun's transaction.
func runDeleted(t *DB, id int64) error {
	if !hooksOn(t, id) {
		return nil
	}
	for _, h := range runDeletedHooks {
		if err := h(t, id); err != nil {
			return err
		}
	}
	return nil
}

// --- redaction ----------------------------------------------------------------------

// projRedact masks secrets in text a project row keeps: the shapes the
// harness redactor knows, and the scm tokens (shapes and live ones).
func projRedact(s string) string { return scmRedact(redactText(s)) }

// untrusted frames scm text for a model: clipped (8 KiB), redacted, said to
// be data from host. The text's invisible format characters (zero-width
// spaces and joiners, soft hyphens, direction marks) go — a model reads
// past them, so they would hide a marker inside a word — and then the
// frame's own markers inside the text are defused (their bracket, plain or
// full-width, made a parenthesis), so the text can't close the frame early
// and pass what follows off as the agent's own words.
func untrusted(host, what, s string) string {
	s = invisibles.ReplaceAllString(clip(projRedact(s), 8<<10), "")
	return fmt.Sprintf("[untrusted — from %s: %s]\n%s\n[end of untrusted text]", orStr(host, "the scm provider"), what,
		frameMarkers.ReplaceAllString(s, "($1"))
}

// invisibles are the Unicode format characters (Cf), which render as
// nothing.
var invisibles = regexp.MustCompile(`\p{Cf}+`)

// frameMarkers finds untrusted's markers: any case, any spacing (Unicode
// spaces too), and a full-width bracket as well as a plain one.
var frameMarkers = regexp.MustCompile(`(?i)[\[\x{FF3B}]((?:` + frameSp + `)*(?:end(?:` + frameSp + `)+of(?:` + frameSp + `)+)?untrusted)`)

// frameSp is a run of spacing between a marker's words.
const frameSp = `[\s\p{Z}]`

// --- project events ------------------------------------------------------------------

// addProjectEvent writes a project event — the coordinator's feed and the
// project page's — and runs projectEventHooks. body is JSON (text at least);
// it is redacted as stored. A dedupe key already used answers nil.
func addProjectEvent(t *DB, pid, n int64, kind string, body map[string]any, wake bool, dedupe string) *ProjectEvent {
	p, err := t.getProject(pid)
	if err != nil {
		return nil
	}
	coord := p.Owner
	if n > 0 {
		if k, err := t.taskByN(pid, n); err == nil {
			coord = k.CreatedBy
		}
	}
	raw, _ := json.Marshal(body)
	ev := &ProjectEvent{Project: pid, N: n, Kind: kind, Body: json.RawMessage(projRedact(string(raw))), Wake: wake,
		CoordUser: coord, Dedupe: dedupe, Created: nowMs()}
	err = t.q.QueryRow(`INSERT INTO project_events (project_id, n, kind, body, wake, coord_user, dedupe, created)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING RETURNING id`,
		pid, n, kind, string(ev.Body), b2i(wake), coord, dedupe, ev.Created).Scan(&ev.ID)
	if err != nil {
		return nil // a repeat (dedupe), or the table isn't there
	}
	for _, h := range projectEventHooks {
		h(t, p, ev)
	}
	emitProject(t, pid, "event", n)
	return ev
}

// projectEvents is a project's feed after since, oldest first.
func (d *DB) projectEvents(pid, since int64, limit int) []ProjectEvent {
	out := []ProjectEvent{}
	rows, err := d.q.Query(`SELECT id, project_id, n, kind, body, wake, coord_user, delivered, msg_id, created
		FROM project_events WHERE project_id=? AND id>? ORDER BY id LIMIT ?`, pid, since, limit)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ev ProjectEvent
		var body string
		var wake int
		if rows.Scan(&ev.ID, &ev.Project, &ev.N, &ev.Kind, &body, &wake, &ev.CoordUser, &ev.Delivered, &ev.MsgID, &ev.Created) == nil {
			ev.Body, ev.Wake = json.RawMessage(orStr(body, "{}")), wake != 0
			out = append(out, ev)
		}
	}
	return out
}

// --- the stream event ------------------------------------------------------------------

// emitProject sends the `project` stream event after the commit.
func emitProject(t *DB, pid int64, change string, n int64) {
	t.AfterCommit(func() { projStream.post(pid, change, n) })
}

// projStream coalesces project events per (project, change, task) for
// projStreamDelay: a client re-reads what changed, so one event says it.
var projStream = &projCoalescer{pending: map[string]bool{}}

var projStreamDelay atomic.Int64 // ns; 250 ms (tests shorten it)

func init() { projStreamDelay.Store(int64(250 * time.Millisecond)) }

type projCoalescer struct {
	mu      sync.Mutex
	pending map[string]bool
}

func (c *projCoalescer) post(pid int64, change string, n int64) {
	key := fmt.Sprintf("%d|%s|%d", pid, change, n)
	c.mu.Lock()
	if c.pending[key] {
		c.mu.Unlock()
		return
	}
	c.pending[key] = true
	c.mu.Unlock()
	time.AfterFunc(time.Duration(projStreamDelay.Load()), func() {
		c.mu.Lock()
		delete(c.pending, key)
		c.mu.Unlock()
		publishProject(pid, change, n, nil)
	})
}

// publishProject sends {id, change, n?} to the list subscribers who may see
// the project and, for a task's change, its conversation's stream. acl nil:
// the project's own (a deleted project's is passed in).
func publishProject(pid int64, change string, n int64, acl *rootACL) {
	ag := projAg()
	e := projEng()
	if ag == nil || e == nil {
		return
	}
	if acl == nil {
		var err error
		if acl, err = ag.db.loadProjectACL(pid); err != nil {
			return
		}
	}
	var run int64
	if n > 0 {
		if k, err := ag.db.taskByN(pid, n); err == nil {
			run = k.RunID
		}
	}
	data := map[string]any{"id": pid, "change": change}
	if n > 0 {
		data["n"] = n
	}
	e.hub.publishTo(func(s *subscriber) bool {
		return (s.root == 0 && acl.level(s.w) >= lvViewer) || (run != 0 && s.root == run && change == "task")
	}, &Event{Type: evProject, Data: data})
}

// --- a task changed --------------------------------------------------------------------

// onTaskChange is every change of a task's state: the stream event (change
// "task") and taskChangedHooks (what: created | state | ws | phase | prs |
// ci | deleted).
func onTaskChange(t *DB, p *Project, k *ProjectTask, what string) {
	if p == nil || k == nil {
		return
	}
	emitProject(t, p.ID, "task", k.N)
	for _, h := range taskChangedHooks {
		h(t, p, k, what)
	}
}

// setWS moves a task's workspace state (and tells).
func setWS(t *DB, p *Project, k *ProjectTask, ws, errText string) {
	if k.WS == ws && k.Error == errText {
		return
	}
	k.WS, k.Error = ws, errText
	_ = t.setTask(k.ID, map[string]any{"ws": ws, "error": errText})
	onTaskChange(t, p, k, "ws")
	if e, run := projEng(), k.RunID; run != 0 && e != nil {
		t.AfterCommit(func() { e.Poke(run) }) // the gate looks again: a park follows its workspace
	}
}

// --- Projects' own hook entries --------------------------------------------------------------

// projectTurnEnd (turnEndHooks): a task's turn ended — its latest answer,
// a task.state event (waking the coordinator when the turn was the
// coordinator's, failed, or waits for a person), the refs check (what it
// pushed), auto-PR (projectRested) and the pump.
func projectTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if run.ParentID != 0 || run.Origin != originProject {
		return
	}
	k := t.taskByRun(run.ID)
	if k == nil {
		return // a coordinator: its own part's
	}
	p, err := t.getProject(k.ProjectID)
	if err != nil {
		return
	}
	cur, err := t.getRun(run.ID)
	if err != nil {
		return
	}
	last := strings.TrimSpace(result)
	if last == "" && why == turnAnswered {
		last = t.lastAssistant(run.ID)
	}
	last = clip(projRedact(last), inlineBudget(1))
	if last != "" {
		k.Last = last
		_ = t.setTask(k.ID, map[string]any{"last": last})
	}
	wake := k.TurnBy == srcCoordinator || why == turnError || cur.Status == statusWaiting
	addProjectEvent(t, p.ID, k.N, pevTaskState, map[string]any{
		"text": fmt.Sprintf("%s (%s)", turnWords(why, cur.Status), clip(firstLine(last), 200)), "why": why,
		"outcome": outcome, "status": cur.Status, "by": k.TurnBy}, wake, "")
	if k.WS == wsReady && p.State == projActive {
		_, _ = t.queueJob(p.ID, k.ID, "", pjRefs, "", 0) // what it pushed this turn (deduped while one is live)
	}
	if resting(cur.Status) {
		projectRested(t, p, k)
	}
	onTaskChange(t, p, k, "state")
	pid := p.ID
	t.AfterCommit(func() { projectPump(pid) })
}

// turnWords says how a task's turn ended, for the event feed.
func turnWords(why, status string) string {
	switch {
	case status == statusWaiting:
		return "waits for a person"
	case why == turnError:
		return "failed"
	case why == turnCanceled:
		return "was cancelled"
	case why == turnInterrupted:
		return "was interrupted"
	case why == turnIncomplete:
		return "stopped before it was done"
	case why == turnFinished:
		return "finished"
	}
	return "answered"
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// projectStatusChanged (runStatusHooks): a task run's status moved — a park
// waiting for a person outside a turn's end (an approval, ask_user, a coding
// agent's question or sign-in) wakes the coordinator; any move may free or
// take a slot (the pump) and moves the board.
func projectStatusChanged(t *DB, runID int64, status string) {
	k := t.taskByRun(runID)
	if k == nil {
		return
	}
	p, err := t.getProject(k.ProjectID)
	if err != nil {
		return
	}
	if status == statusWaiting {
		if run, err := t.getRun(runID); err == nil && run.ParentID == 0 {
			if ps := parsePending(run.Pending); ps.Kind != pendKindProject { // the gate's own parks are the workspace's events
				addProjectEvent(t, p.ID, k.N, pevTaskState, map[string]any{
					"text": "waits for a person: " + clip(firstLine(run.Result), 200), "status": status}, true, "")
			}
		}
	}
	onTaskChange(t, p, k, "state")
	pid := p.ID
	t.AfterCommit(func() { projectPump(pid) })
}

// projectRunDeleted (runDeletedHooks): a task's conversation is deleted —
// the task is marked deleted, its queued inputs dropped, and its workspace
// cleaned up by the worker (a cleanup job; unpushed work blocks it).
func projectRunDeleted(t *DB, runID int64) error {
	k := t.taskByRun(runID)
	if k == nil {
		return nil
	}
	if _, err := t.q.Exec(`UPDATE project_tasks SET run_id=0, phase=?, updated_ms=? WHERE id=?`, phaseDeleted, nowMs(), k.ID); err != nil {
		return err
	}
	_, _ = t.q.Exec(`DELETE FROM project_queue WHERE project_id=? AND n=?`, k.ProjectID, k.N)
	p, err := t.getProject(k.ProjectID)
	if err != nil {
		return nil
	}
	k.RunID, k.Phase = 0, phaseDeleted
	if k.WS != wsCleaned && k.WS != wsPending {
		_, _ = t.queueJob(p.ID, k.ID, "", pjCleanup, "", 0)
	}
	onTaskChange(t, p, k, "deleted")
	return nil
}

// projectHumanMessage is POST /runs/{id}/message's hook: a person wrote to
// a task directly (bypassing the queue) — a quiet task.human event, and the
// turn is theirs.
func projectHumanMessage(run *Run, c who) {
	if run == nil || run.ParentID != 0 || run.Origin != originProject || hostedID(run.ID) || projAg() == nil {
		return
	}
	_ = projAg().db.Tx(func(t *DB) error {
		k := t.taskByRun(run.ID)
		if k == nil {
			return nil
		}
		p, err := t.getProject(k.ProjectID)
		if err != nil {
			return nil
		}
		k.TurnBy = srcHuman
		_ = t.setTask(k.ID, map[string]any{"turn_by": srcHuman})
		addProjectEvent(t, p.ID, k.N, pevTaskHuman, map[string]any{"text": orStr(c.user, c.tag()) + " wrote to the task", "by": c.tag()}, false, "")
		onTaskChange(t, p, k, "state")
		return nil
	})
}

// runStopEnd is runTurnEnd for a run stopped mid-turn (stopRun: to is
// canceled or idle).
func runStopEnd(t *DB, run *Run, to, reason string) {
	if to == statusCanceled {
		runTurnEnd(t, run, turnCanceled, outcomeCanceled, reason)
		return
	}
	runTurnEnd(t, run, turnInterrupted, outcomeInterrupted, reason)
}
