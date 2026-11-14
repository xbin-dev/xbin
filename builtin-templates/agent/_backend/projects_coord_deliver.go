// projects_coord_deliver.go — what reaches a coordinator (API.md §The
// coordinator, "Project updates"): its project's events, as one message at
// a step boundary (the profile's Deliver: deliverBoundary writes it and
// marks them), and the wakes — an idle coordinator with an undelivered
// event that asks for one (a task's turn the coordinator asked for ended, a
// task failed, waits for a person, a pull request went green) starts a turn,
// at most once a minute (the profile's Pending; coord_wake.go coalesces
// them, with its own timer for the rest); and its # Project block
// (coordPrompt), stable from turn to turn so the prompt cache stays warm —
// what changes comes as updates and through task_list / task_status.
//
// Every reader of a run's role goes through projectRefOf, so a coordinator
// whose stored config lost `project` (an older build rewrote it) still
// wakes and still gets its updates.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

func init() {
	projectEventHooks = append(projectEventHooks, coordEventWake)
	runStatusHooks = append(runStatusHooks, coordWakeHeld)
	ownerLoops = append(ownerLoops, coordRecover)
}

// coordUpdatesHead opens a coordinator's project updates.
const coordUpdatesHead = "[project updates — tasks and the scm provider reporting, not a person]"

// The delivery's bounds: lines per message, bytes per line, bytes in all.
const (
	coordMaxLines = 40
	coordLineMax  = 400
	coordTextMax  = 8 << 10
)

// coordOf is run's project and person when run is a project's coordinator
// (its role through projectRefOf, its session key agreeing with it and with
// its owner) — ok false otherwise.
func coordOf(d *DB, run *Run) (pid int64, user string, ok bool) {
	if run == nil || run.ParentID != 0 || run.Origin != originProject || hostedID(run.ID) {
		return 0, "", false
	}
	ref := d.projectRefOf(run)
	if !ref.isCoordinator() {
		return 0, "", false
	}
	pid, user, ok = coordKeyOf(run.SessionKey)
	if !ok || pid != ref.ID || user != run.Owner {
		return 0, "", false
	}
	return pid, user, true
}

// --- delivery --------------------------------------------------------------------------

// coordUpdateOf (the profile's Deliver) is coordDeliver's message as the
// kit writes it: headed as the project's updates.
func coordUpdateOf(t *DB, run *Run) *coordUpdate {
	text, mark := coordDeliver(t, run)
	if text == "" || mark == nil {
		return nil
	}
	ref := t.projectRefOf(run)
	if ref == nil {
		return nil
	}
	return &coordUpdate{Text: text, Mark: mark, Meta: msgMeta{Origin: "project", OriginID: ref.ID, Label: "project updates"}}
}

// coordDeliver is the coordinator's undelivered
// project events as one message's text, and the mark that records them
// delivered with that message. At most coordMaxLines lines, each one line
// (#<n> <kind>: <text>), clipped, its text's invisible characters gone and
// the frames' markers defused; older ones beyond say how many. "": none.
func coordDeliver(t *DB, run *Run) (string, func(int64)) {
	pid, user, ok := coordOf(t, run)
	if !ok {
		return "", nil
	}
	var total int
	var top int64
	if t.q.QueryRow(`SELECT count(*), COALESCE(MAX(id), 0) FROM project_events WHERE project_id=? AND coord_user=? AND delivered=0`,
		pid, user).Scan(&total, &top) != nil || total == 0 {
		return "", nil
	}
	rows, err := t.q.Query(`SELECT n, kind, body FROM project_events WHERE project_id=? AND coord_user=? AND delivered=0 AND id<=?
		ORDER BY id DESC LIMIT ?`, pid, user, top, coordMaxLines)
	if err != nil {
		return "", nil
	}
	type ev struct {
		n    int64
		kind string
		body string
	}
	var shown []ev // the newest first
	for rows.Next() {
		var e ev
		if rows.Scan(&e.n, &e.kind, &e.body) == nil {
			shown = append(shown, e)
		}
	}
	rows.Close()
	lines := make([]string, 0, len(shown))
	size := 0
	for _, e := range shown { // the newest first, so the budget keeps them
		l := coordLine(e.n, e.kind, e.body)
		if size+len(l) > coordTextMax {
			break
		}
		size += len(l) + 1
		lines = append(lines, l)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	var b strings.Builder
	b.WriteString(coordUpdatesHead + "\n")
	if left := total - len(lines); left > 0 {
		fmt.Fprintf(&b, "(%d earlier update(s) not shown — task_list and task_status say where each task stands)\n", left)
	}
	b.WriteString(strings.Join(lines, "\n"))
	mark := func(msgID int64) {
		_, _ = t.q.Exec(`UPDATE project_events SET delivered=?, msg_id=? WHERE project_id=? AND coord_user=? AND delivered=0 AND id<=?`,
			nowMs(), msgID, pid, user, top)
	}
	return b.String(), mark
}

// coordLine is one event as a line of the updates.
func coordLine(n int64, kind, body string) string {
	var bv struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(body), &bv)
	who := "project"
	if n > 0 {
		who = fmt.Sprintf("#%d", n)
	}
	return clip(fmt.Sprintf("%s %s: %s", who, coordPlain(kind), coordPlain(bv.Text)), coordLineMax)
}

// coordPlain is text as one line a model reads as data: its invisible
// characters gone, its whitespace one space (no line of its own can pass
// for a header), the frames' markers defused (their bracket a parenthesis).
func coordPlain(s string) string {
	s = invisibles.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	s = frameMarkers.ReplaceAllString(s, "($1")
	return coordMarkers.ReplaceAllString(s, "($1")
}

// coordMarkers are the coordinator's own headers inside text: the updates',
// the starter's (project_starter.go) and the frame a coordinator's message
// to a task wears.
var coordMarkers = regexp.MustCompile(`(?i)[\[\x{FF3B}]((?:` + frameSp + `)*(?:project` + frameSp + `+(?:updates|starter)|message` + frameSp + `+from))`)

// --- the wake ----------------------------------------------------------------------------

// coordPending (the profile's Pending; coordWakes coalesces the wakes): an
// idle (or sleeping) coordinator has an undelivered event that asks to
// wake it. Its project must be active and its owner still take part in
// it: otherwise every project tool would refuse, so a wake would spend a
// model turn on nothing — its events stop asking for one (coordDropWakes).
func coordPending(d *DB, run *Run) bool {
	pid, user, ok := coordOf(d, run)
	if !ok {
		return false
	}
	var n int
	if d.q.QueryRow(`SELECT count(*) FROM project_events WHERE project_id=? AND coord_user=? AND delivered=0 AND wake=1`,
		pid, user).Scan(&n) != nil || n == 0 {
		return false
	}
	if p, err := d.getProject(pid); err != nil || p.State != projActive || d.projectLevel(coordWho(user), pid) < lvParticipant {
		coordDropWakes(d, pid, user)
		return false
	}
	return true
}

// coordDropWakes: user's coordinator of pid can't take a wake now (it waits
// for its person, its turn failed, its project isn't active, its person no
// longer takes part) — its undelivered events stop asking for one. They
// stay undelivered: its next turn, whatever starts it, gets them as
// updates. Left asking, they would count as work for the person's
// partition (userWake, projectsWake) that nothing ever takes up, and the
// partition would never sleep.
func coordDropWakes(d *DB, pid int64, user string) {
	if _, err := d.q.Exec(`UPDATE project_events SET wake=0 WHERE project_id=? AND coord_user=? AND delivered=0 AND wake=1`,
		pid, user); err != nil {
		logf("project %d: %s's coordinator can't take a wake; its events still ask for one: %v", pid, user, err)
	}
}

// coordWakeHeld (runStatusHooks): a coordinator started waiting for its
// person or failed — its events stop asking for a wake (coordDropWakes).
// Matched as projectsWake matches a coordinator: by its session key.
func coordWakeHeld(t *DB, runID int64, status string) {
	if !coordWakeCant(status) {
		return
	}
	// a coordinator first (one row by its key — every chat's approval
	// passes here), then its own events by their index
	var origin, key string
	var parent int64
	if t.q.QueryRow(`SELECT origin, parent_id, session_key FROM runs WHERE id=?`, runID).Scan(&origin, &parent, &key) != nil ||
		origin != originProject || parent != 0 {
		return
	}
	if pid, user, ok := coordKeyOf(key); ok {
		coordDropWakes(t, pid, user)
	}
}

// coordEventWake (projectEventHooks): an event that asks for a wake pokes
// its person's coordinator, if they have one (the pass asks coordWakes) —
// unless it can't take one now (waiting for its person, failed, its
// project not active): then the event stops asking (coordDropWakes).
func coordEventWake(t *DB, p *Project, ev *ProjectEvent) {
	if !ev.Wake || ev.CoordUser == "" {
		return
	}
	if id := t.coordRunOf(p.ID, ev.CoordUser); id != 0 {
		var status string
		_ = t.q.QueryRow(`SELECT status FROM runs WHERE id=?`, id).Scan(&status)
		if p.State != projActive || coordWakeCant(status) {
			coordDropWakes(t, p.ID, ev.CoordUser)
			return
		}
		t.AfterCommit(func() {
			if e := projEng(); e != nil {
				e.Poke(id)
			}
		})
	}
}

// coordRecover (ownerLoops) pokes, once at takeover, every coordinator with
// an undelivered event asking for a wake: the engine's own recovery looks
// at inboxes, links and active runs, never at project events, and a
// person's partition resumed for one would otherwise wait for nothing.
func coordRecover(ctx context.Context, e *Engine) {
	if ctx.Err() != nil {
		return
	}
	ids := scanIDs(e.db.q.Query(`SELECT DISTINCT r.id FROM project_events ev JOIN runs r
		ON r.origin='project' AND r.parent_id=0 AND r.session_key='proj:' || ev.project_id || ':coord:' || ev.coord_user
		WHERE ev.delivered=0 AND ev.wake=1`))
	for _, id := range ids {
		e.Poke(id)
	}
}

// --- the # Project block -----------------------------------------------------------------------

// coordPrompt is a coordinator's # Project block (projectPromptFor), in
// the kit's parts (coordAssemble): who it works for, the project's repos
// and limits, and how it works — its own rules and every coordinator's —
// from the project's row, repos and policy only, so it stays the same from
// turn to turn.
func coordPrompt(p *Project, run *Run) string {
	ag := projAg()
	if ag == nil || p == nil || run == nil {
		return ""
	}
	pol := policyOf(p.Policy)
	var role, facts strings.Builder
	fmt.Fprintf(&role, "You are %s's coordinator of the project %q: you create and steer its tasks. Each task is its own conversation "+
		"with a git worktree of every repo it works in and a branch of its own; a coding agent or the built-in agent answers it.\n",
		run.Owner, p.Name)
	if repos, err := ag.db.projectRepos(p.ID); err == nil && len(repos) > 0 {
		facts.WriteString("Repos (at " + orStr(p.Host, "the scm provider") + "):\n")
		for _, r := range repos {
			if r.State == "removing" {
				continue
			}
			fmt.Fprintf(&facts, "- %s (default branch %s)\n", r.Repo, orStr(r.DefaultBranch, "unknown"))
		}
	}
	fmt.Fprintf(&facts, "Limits: %d task(s) work at once (the others wait in the project's queue); coordinators may have %d open tasks "+
		"and create %d a day; task_create takes at most %d at once.\n", pol.MaxTasks, pol.MaxOpenTasks, pol.MaxCreatesDay, coordMaxCreate)
	rules := []string{
		"Name tasks by their number (#3). task_list and task_status say where they stand; task_result reads one's latest answer in full.",
		"Give each task a self-contained brief: what to change, where, how to check it. Every task pushes its own branch and may open a pull request; a person reviews and merges.",
		"A pull request a task's coding agent opened itself from another branch is linked to the task when the platform finds it; one that isn't, task_link_pr links. When a task's pull request state looks stale (merged, CI finished), task_refresh reads it again.",
		"Keep your goal current with project_goal — your person sees it at the top of the project's page: set it when your aim changes (a new instruction from them, an aim reached), with now for what you are doing this turn.",
		coordRuleNeverAnswers("task"),
		"You can't merge, approve, push or comment: scm_pr and scm_issues only read. Tell the person when a pull request is ready for review.",
		"You don't change the project's policy, sharing or sign-in, and you never delete tasks or the project.",
		coordRuleData(coordDataRule{Updates: "Project updates", Head: "[project updates — …]", From: "tasks and the scm provider",
			Data: "task answers, issues, reviews, CI logs and the project wiki's pages"}),
	}
	know, index := coordWikiRules(ag.db, p, run) // research and the wiki (project_wiki_prompt.go)
	return coordAssemble(coordPromptParts{Heading: "# Project", Role: role.String(), Facts: facts.String(),
		Rules: append(rules, know...), Index: index})
}
