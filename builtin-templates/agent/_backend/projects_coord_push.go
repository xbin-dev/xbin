// projects_coord_push.go — a project's runs among what needs a person
// (API.md §The coordinator, "Needs and pushes"): GET /needs items of a
// project's run carry project {id, name, n} (n 0: a coordinator); GET
// /projects/{pid}/needs is the same list for one project; a push about a
// task (a question, an approval, a failure) is titled "<project> · <task>";
// and the project's digest pushes — pr-ready (a task's pull request went
// green), task-failed (a task's turn or workspace failed), ci-stuck (CI
// kept failing past the day's fixes), all-done (every task of the project
// finished) — go to the person whose task it is, through the same pusher
// (its per-person budget and dedupe), collapsed per project and kind on the
// device (project:<id>:<kind>).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { projectEventHooks = append(projectEventHooks, coordDigest) }

// handleProjectNeeds is GET /projects/{pid}/needs: /needs, for the
// project's runs (its tasks and the caller's coordinator).
func handleProjectNeeds(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	items, err := needsItems(callerOf(r), "r.origin=? AND r.origin_id=?", originProject, p.ID)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items})
}

// withNeedsProject adds project {id, name, n} to a /needs item of a
// project's run x (a task's number; 0 for a coordinator).
func withNeedsProject(d *DB, item map[string]any, x *Run) map[string]any {
	if x == nil || x.Origin != originProject || !d.features {
		return item
	}
	ref := d.projectRefOf(x)
	if ref == nil {
		return item
	}
	p, err := d.getProject(ref.ID)
	if err != nil {
		return item
	}
	item["project"] = map[string]any{"id": p.ID, "name": p.Name, "n": ref.N}
	return item
}

// projectPushTitle is a push's title for root's conversation: a project's
// run's starts with the project's name.
func projectPushTitle(d *DB, root *Run, title string) string {
	if root == nil || root.Origin != originProject || !d.features {
		return title
	}
	p, err := d.getProject(root.OriginID)
	if err != nil {
		return title
	}
	return clip(p.Name, 60) + " · " + title
}

// Digest pushes' kinds (the notification's kind; CollapseID
// project:<id>:<kind>).
const (
	pushPRReady    = "pr-ready"
	pushTaskFailed = "task-failed"
	pushCIStuck    = "ci-stuck"
	pushAllDone    = "all-done"
)

// coordDigest (projectEventHooks): an event worth a push sends one, after
// the commit, to the person whose task it is — while they still take part
// in the project.
func coordDigest(t *DB, p *Project, ev *ProjectEvent) {
	user := ev.CoordUser
	if !person(user) || projAg() == nil || projAg().needs == nil {
		return
	}
	var k *ProjectTask
	if ev.N > 0 {
		k, _ = t.taskByN(p.ID, ev.N)
	}
	var body struct {
		Text string `json:"text"`
		Why  string `json:"why"`
		SHA  string `json:"sha"`
	}
	_ = json.Unmarshal(ev.Body, &body)
	text := clip(plainText(body.Text), 200)
	n := needsPush{user: user, title: clip(p.Name, 60), link: fmt.Sprintf("#proj=%d", p.ID)}
	if k != nil {
		n.title += " · " + clip(plainText(k.Title), 80)
		if k.RunID != 0 {
			n.run, n.link = k.RunID, fmt.Sprintf("#c=%d", k.RunID)
		}
	}
	switch {
	case ev.Kind == pevPRReady && k != nil:
		n.state, n.body, n.fingerprint = pushPRReady, "Its pull request's checks passed: ready for review.", fmt.Sprintf("%d:%s", k.N, body.SHA)
	case ev.Kind == pevCIStuck && k != nil:
		n.state, n.body, n.fingerprint = pushCIStuck, orStr(text, "Its CI keeps failing."), fmt.Sprintf("%d:%s", k.N, body.SHA)
	case k != nil && (ev.Kind == pevTaskState && body.Why == turnError && coordHarnessRun(t, k) || ev.Kind == pevWorkspace && ev.Wake && k.WS == wsFailed):
		// a built-in task's failed turn has its push already (the run's
		// "failed", needs_push.go); a coding agent's and a workspace's don't
		n.state, n.body, n.fingerprint = pushTaskFailed, "It failed"+orStr(": "+text, "."), fmt.Sprintf("%d:%d", k.N, ev.ID)
	case k != nil && (ev.Kind == pevMerged || ev.Kind == pevClosed || ev.Kind == pevTaskCancel || ev.Kind == pevTaskState):
		done, total, last := projectAllDone(t, p)
		if !done {
			return
		}
		n.state, n.title, n.link, n.run = pushAllDone, clip(p.Name, 60), fmt.Sprintf("#proj=%d", p.ID), 0
		n.body, n.fingerprint = fmt.Sprintf("All %d of its tasks are finished.", total), fmt.Sprintf("%d:%d", total, last)
	default:
		return
	}
	if t.projectLevel(coordWho(user), p.ID) < lvParticipant {
		return
	}
	collapse := fmt.Sprintf("project:%d:%s", p.ID, n.state)
	t.AfterCommit(func() { go sendDigest(n, collapse) })
}

// coordHarnessRun: task k's conversation is answered by a coding agent.
func coordHarnessRun(t *DB, k *ProjectTask) bool {
	if k.RunID == 0 {
		return false
	}
	run, err := t.getRun(k.RunID)
	return err == nil && run.Engine == engineHarness
}

// projectAllDone: every task of p (one at least, a deleted one aside) is
// finished — its board column done: merged, closed, done, or its run
// cancelled with its workspace not waiting for a person (taskDerived) —
// with how many and the newest's number (the push's fingerprint: a new
// task makes it news again). One query: it runs at every task's turn end.
func projectAllDone(t *DB, p *Project) (bool, int, int64) {
	var total, open int
	var last int64
	if t.q.QueryRow(`SELECT count(*), COALESCE(MAX(k.n), 0), COALESCE(SUM(CASE WHEN k.phase IN ('merged','closed','done')
		OR (k.ws NOT IN ('pending','queued','preparing','signin','failed','blocked') AND r.status='canceled') THEN 0 ELSE 1 END), 0)
		FROM project_tasks k LEFT JOIN runs r ON r.id=k.run_id WHERE k.project_id=? AND k.phase<>'deleted'`, p.ID).
		Scan(&total, &last, &open) != nil || total == 0 || open > 0 {
		return false, 0, 0
	}
	return true, total, last
}

// sendDigest sends one digest push through the pusher's budget and dedupe.
func sendDigest(n needsPush, collapse string) {
	ag := projAg()
	if ag == nil || ag.needs == nil {
		return
	}
	pu := ag.needs
	if !pu.admit(n) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pu.send(ctx, xbin.UserNotification{User: n.user, Title: n.title, Body: n.body, Link: n.link, Kind: n.state,
		CollapseID: collapse}); err != nil {
		log.Printf("project push: %s to %s: %v", n.state, n.user, err)
	}
}
