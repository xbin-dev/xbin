// project_starter.go — a new project's starter work (API.md §Projects and
// tasks, "Starter work"): POST /projects {starter: {build, wiki}} asks for
// task #1 — clone, build and run the tests — once the project's workspace
// is ready, and for the creator's coordinator to build the initial wiki.
// With both, the wiki waits for task #1: its first turn ends, it gets a
// dream round (project_dream.go; policy dream) to keep what it verified in
// the wiki, and once that round's pages are in — a built-in task's dream
// turn ended, a coding agent's ingest over — the coordinator is asked. No
// dream (policy off, a turn that failed) or one that never ends
// (starterDreamWait): the coordinator is asked at once, or then.
//
// The backend owns it, not the page: a small record per project (the
// setting proj_starter:<pid>) says what is still to do, and a `starter` job
// does it — queued in the transaction that makes it due (the workspace's
// last job done, task #1's turn ended or its dream over), so a restart
// loses nothing; each step marks the record in the transaction that does
// it (the task's creation, the coordinator's message), so nothing happens
// twice.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() {
	projectJobKinds[pjStarter] = jobStarter
	rerunMarks[pjStarter] = starterAgain // a kick while it runs: it runs once more (finish)
	turnEndHooks = append(turnEndHooks, starterTurnEnd)
	taskChangedHooks = append(taskChangedHooks, starterTaskChanged)
}

// pjStarter is the job that does a project's starter work; starterAgain the
// mark (its client id) a kick leaves on it while it is live.
const (
	pjStarter    = "starter"
	starterAgain = "again"
)

// srcStarter is the coordinator's starter message's source: the platform,
// never a person (the chat shows it as a notice, model/fold.js).
const srcStarter = "starter"

// A starter step's states: wait (for its moment), dream (the wiki: task
// #1's dream round runs first), due (the job does it now), done, failed.
// "" is a step nobody asked for. Task #1's dream (starterRec.Dream): ""
// (its first turn hasn't ended), asked, over, or none (it got no round).
const (
	stWait   = "wait"
	stDream  = "dream"
	stDue    = "due"
	stDone   = "done"
	stFailed = "failed"
)

// starterDreamWait is how long the coordinator's message waits for task
// #1's dream round at most; starterPoll how often the job looks meanwhile
// (vars: tests).
var (
	starterDreamWait = 30 * time.Minute
	starterPoll      = 30 * time.Second
)

// starterTitle and starterBrief are task #1's.
const (
	starterTitle = "Clone, build and run the tests"
	starterBrief = "For each of the project's repos: find how it is built and tested (its README, Makefile, CI config, package files); " +
		"if a repo has no build docs, say so and work it out from its manifests (go.mod, package.json, …). Build it and run its tests " +
		"in your workspace. Change no code and open no pull request. Answer with, per repo: the exact commands that worked, the " +
		"toolchain they need, how long they took, what passed and what failed (the failing tests' names and first error lines), and " +
		"what you had to install or work around."
)

// starterHead opens the coordinator's starter message.
const starterHead = "[project starter — from the platform, not a person]"

// starterReq is POST /projects' starter.
type starterReq struct {
	Build bool `json:"build"`
	Wiki  bool `json:"wiki"`
}

func (s *starterReq) asked() bool { return s != nil && (s.Build || s.Wiki) }

// starterRec is a project's starter record (the setting starterKey).
type starterRec struct {
	By      string `json:"by"`                // the creator (who.tag): task #1 is theirs, the coordinator theirs
	Build   string `json:"build,omitempty"`   // "" | wait | done | failed
	Wiki    string `json:"wiki,omitempty"`    // "" | wait | dream | due | done | failed
	Task    int64  `json:"task,omitempty"`    // the build's task number
	Dream   string `json:"dream,omitempty"`   // task #1's dream round: "" | asked | over | none
	DreamMs int64  `json:"dreamMs,omitempty"` // when task #1's dream round was queued
	Partial bool   `json:"partial,omitempty"` // task #1's first turn stopped part-way (its limit)
	Error   string `json:"error,omitempty"`   // why a step failed
}

// Task #1's dream round (starterRec.Dream).
const (
	drAsked = "asked"
	drOver  = "over"
	drNone  = "none"
)

func starterKey(pid int64) string { return "proj_starter:" + strconv.FormatInt(pid, 10) }

// starterOf is pid's starter record (ok false: none).
func (d *DB) starterOf(pid int64) (starterRec, bool) {
	var r starterRec
	raw := d.getSetting(starterKey(pid))
	if raw == "" || json.Unmarshal([]byte(raw), &r) != nil {
		return starterRec{}, false
	}
	return r, true
}

func (d *DB) putStarter(pid int64, r starterRec) error {
	b, _ := json.Marshal(r)
	return d.putSetting(starterKey(pid), string(b))
}

// wikiFree: the wiki step waits for nothing but the workspace — it was
// asked alone, or the build couldn't be made.
func (r starterRec) wikiFree() bool {
	return r.Wiki == stWait && (r.Build == "" || r.Build == stFailed)
}

// --- asking for it (POST /projects) -------------------------------------------------------------

// checkStarter: may c ask for s on a new project of this kind? A team
// project's definition has no tasks and no coordinator; the work runs in
// the project's sandbox; task #1 is made later in the background as c
// without a manager's rights, so its class must be one c may use so; the
// wiki is c's coordinator's work, a person's own.
func checkStarter(c who, kind string, hasSandbox bool, pol ProjectPolicy, probe *Project, s *starterReq) error {
	if !s.asked() {
		return nil
	}
	switch {
	case kind == projTeam:
		return &projErr{code: 409, refusal: "starter", msg: "a team project's definition has no tasks and no coordinator: it takes no starter"}
	case !hasSandbox:
		return &projErr{code: 409, refusal: "starter", msg: "the starter works in the project's sandbox: give it one"}
	}
	if s.Build {
		if _, err := taskClassFor(binderWho(c.tag()), probe, pol.TaskClass); err != nil {
			return err
		}
	}
	if s.Wiki {
		if c.kind != whoUser || c.viewedBy != "" {
			return &projErr{code: 409, refusal: "starter", msg: "the initial wiki is your coordinator's work, and a coordinator is a person's own: ask from your own account"}
		}
		if err := coordClassCheck(coordWho(c.user)); err != nil {
			return err
		}
	}
	return nil
}

// coordClassCheck: w's coordinator could be made — its class (the built-in
// web class) is there, without internal reach, and w may use it.
func coordClassCheck(w who) error {
	cls, ok := currentClasses().find(classWeb)
	if !ok || cls.has(tsInternal) {
		return &projErr{code: 409, refusal: refusalClassInternal, msg: errCoordClassInternal.Error()}
	}
	if !cls.usableBy(w) {
		return perr(403, "the coordinator's class (%s) is for the agent's managers only: the initial wiki needs a coordinator", orStr(cls.Name, cls.ID))
	}
	return nil
}

// newStarter is the record a project starts with (in its creation's transaction).
func newStarter(t *DB, pid int64, c who, s *starterReq) error {
	if !s.asked() {
		return nil
	}
	r := starterRec{By: c.tag()}
	if s.Build {
		r.Build = stWait
	}
	if s.Wiki {
		r.Wiki = stWait
	}
	return t.putStarter(pid, r)
}

// starterView is the record as POST /projects answers it (nil: none asked).
func starterView(r starterRec, ok bool) map[string]any {
	if !ok {
		return nil
	}
	return map[string]any{"build": r.Build != "", "wiki": r.Wiki != ""}
}

// --- its moments ----------------------------------------------------------------------------------

// workspaceReady: p's sandbox is laid out and every repo of it is cloned.
func workspaceReady(d *DB, p *Project) bool {
	if p.Dir == "" || p.SandboxRef == "" {
		return false
	}
	repos, err := d.projectRepos(p.ID)
	if err != nil {
		return false
	}
	for _, r := range repos {
		if r.State != "ready" && r.State != "removing" {
			return false
		}
	}
	return true
}

// starterKick queues pid's starter job; a live one is marked (starterAgain)
// — a waiting one looks now, a running one runs once more when it ends. An
// archived project's job waits, held (claimableSQL), until it is active.
func starterKick(t *DB, pid int64, by string) {
	if j, err := t.queueJob(pid, 0, "", pjStarter, by, 0); err == nil && j.State != pjQueued {
		_, _ = t.q.Exec(`UPDATE project_jobs SET client_id=?, next_ms=CASE WHEN state='waiting' THEN 0 ELSE next_ms END
			WHERE id=? AND state IN ('waiting','running')`, starterAgain, j.ID)
	}
}

// starterToDo: what the record says the job has to do now — task #1 or
// the wiki step once the workspace is ready, the wiki step due or waiting
// for task #1's dream round.
func (r starterRec) toDo(ready bool) bool {
	return (r.Build == stWait || r.wikiFree()) && ready || r.Wiki == stDue || r.Wiki == stDream
}

// starterJobDone (finish, in its transaction): a project's sandbox or repo
// job is done — once the workspace is ready, the starter's work waiting on
// it is queued; and anything the record still says is due, whatever became
// of its job (one failed: Warm or a task's Retry runs the sandbox job, and
// this queues it again).
func starterJobDone(t *DB, pid int64) {
	r, ok := t.starterOf(pid)
	if !ok {
		return
	}
	p, err := t.getProject(pid)
	if err == nil && r.toDo(workspaceReady(t, p)) {
		starterKick(t, pid, r.By)
	}
}

// starterJobFailed (finish, in its transaction): a project's own job failed
// — a sandbox or repo job the starter waits for, or the starter's own. Said
// once per job, with what goes on: Warm (or a task's Retry) runs them again.
func starterJobFailed(t *DB, pid int64, j *ProjectJob, errText string) {
	r, ok := t.starterOf(pid)
	if !ok {
		return
	}
	var text string
	switch {
	case j.Kind == pjStarter && (r.Build == stWait || r.Wiki == stWait || r.Wiki == stDue || r.Wiki == stDream):
		text = "starter: its work failed (" + clip(errText, 300) + ") — Warm, or Retry on a task, tries again"
	case (j.Kind == pjSandbox || j.Kind == pjRepo) && (r.Build == stWait || r.wikiFree()):
		text = fmt.Sprintf("starter: waits for the project's workspace (its %s job failed) — Warm, or Retry on a task, goes on", j.Kind)
	default:
		return
	}
	addProjectEvent(t, pid, 0, pevNote, map[string]any{"text": text, "by": "starter"}, false, fmt.Sprintf("starter:failed:%d", j.ID))
}

// starterTurnEnd (turnEndHooks): task #1's first turn ended — it dreams
// first where it can (a wiki step waits for the round), else the wiki step
// is due; a turn a person stopped (interrupted) isn't the builds' end: the
// next one's is. A built-in task's dream turn ended — its pages are in. (A
// coding agent's dream is over when its ingest is: starterDreamOver.)
func starterTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if run.ParentID != 0 || run.Origin != originProject {
		return
	}
	k := t.taskByRun(run.ID)
	if k == nil {
		return
	}
	r, ok := t.starterOf(k.ProjectID)
	if !ok || r.Build != stDone || r.Task != k.N {
		return
	}
	switch r.Dream {
	case drAsked:
		// its dream turn ends: its row says active (before projectTurnEnd's
		// bookkeeping) or that the last turn was the dream's (after it)
		if dr := t.dreamOf(k.ID); run.Engine != engineHarness && (dr.Active || dr.LastDream) {
			starterDreamEnded(t, k.ProjectID, r)
		}
		return
	case "":
	default:
		return
	}
	if why == turnInterrupted {
		return
	}
	cur, err := t.getRun(run.ID)
	if err != nil {
		return
	}
	p, err := t.getProject(k.ProjectID)
	if err != nil {
		return
	}
	r.Partial = why == turnIncomplete
	if starterDream(t, p, k, cur, why, r.Partial) {
		r.Dream, r.DreamMs = drAsked, nowMs()
		if r.Wiki == stWait {
			r.Wiki = stDream
		}
		if t.putStarter(p.ID, r) == nil && r.Wiki == stDream {
			starterKick(t, p.ID, r.By) // it waits for the round, at most starterDreamWait
		}
		return
	}
	r.Dream = drNone
	if r.Wiki == stWait {
		starterWikiDue(t, p.ID, r)
		return
	}
	_ = t.putStarter(p.ID, r)
}

// starterDreamEnded: task #1's dream round is over — the wiki step that
// waited for it is due.
func starterDreamEnded(t *DB, pid int64, r starterRec) {
	r.Dream = drOver
	if r.Wiki == stDream {
		starterWikiDue(t, pid, r)
		return
	}
	_ = t.putStarter(pid, r)
}

// starterDreamOver (jobWikiIngest, once no follow-up round goes out): task
// n's dream round's pages are in.
func starterDreamOver(t *DB, pid, n int64) {
	if r, ok := t.starterOf(pid); ok && r.Dream == drAsked && r.Task == n {
		starterDreamEnded(t, pid, r)
	}
}

// starterTaskChanged (taskChangedHooks): task #1 failed or ended before its
// turn or its dream did (its workspace failed, it was cancelled or
// deleted) — the wiki step is due all the same, and can say what failed.
func starterTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if what != "deleted" && k.WS != wsFailed && !taskOver(k) {
		return
	}
	if r, ok := t.starterOf(p.ID); ok && r.Build == stDone && r.Task == k.N && (r.Wiki == stWait || r.Wiki == stDream) {
		starterWikiDue(t, p.ID, r)
	}
}

func starterWikiDue(t *DB, pid int64, r starterRec) {
	r.Wiki = stDue
	if t.putStarter(pid, r) == nil {
		starterKick(t, pid, r.By)
	}
}

// starterDream queues task #1's dream round after its first turn — the
// project's dream policy, and the dream's own rules (a run at rest whose
// turn didn't fail, its session still there), as dreamMaybe's: it keeps
// what it verified in the wiki before the coordinator builds on it. A turn
// that hit its limit (partial) dreams too, told it stopped part-way.
func starterDream(t *DB, p *Project, k *ProjectTask, run *Run, why string, partial bool) bool {
	if !t.features || !policyOf(p.Policy).Dream || p.State != projActive || k.WS != wsReady || why == turnError || why == turnCanceled ||
		!resting(run.Status) || run.Status == statusCanceled || run.Status == statusError || !dreamSessionLives(t, run) {
		return false
	}
	opening := "Your builds and tests are done. "
	if partial {
		opening = "Your builds and tests stopped part-way (your turn hit its limit): keep what you did verify. "
	}
	text := dreamPromptFor(p, k, run.Engine == engineHarness, true)
	text = strings.Replace(text, "This task is over. ", opening, 1)
	if _, err := queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcDream, HoldPark: true,
		Dedupe: fmt.Sprintf("dream:%d:starter", k.N)}); err != nil {
		logf("project %d task %d: queueing its starter dream round: %v", p.ID, k.N, err)
		return false
	}
	r := t.dreamOf(k.ID)
	r.Dreams++
	r.Round = 0
	t.putDream(k, r)
	addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": "its dream round: it adds what it verified to the project wiki"}, false, "")
	return true
}

// --- the job --------------------------------------------------------------------------------------

// jobStarter does what the record says is due: task #1 once the workspace
// is ready, then the coordinator's message when its moment came. A refusal
// fails the step (an event says why; a wiki asked too goes on without the
// task), anything else is tried again as any job's step is.
func jobStarter(ctx context.Context, p *Project, _ *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	d := projAg().db
	if j.ClientID == starterAgain { // the kick is taken up now: a later one marks it again (rerunMarks)
		j.ClientID = ""
		_, _ = d.q.Exec(`UPDATE project_jobs SET client_id='' WHERE id=? AND client_id=?`, j.ID, starterAgain)
	}
	r, ok := d.starterOf(p.ID)
	if !ok {
		return doneJob("no starter")
	}
	switch p.State {
	case projActive:
	case projArchived:
		return waitJob(60000, "the project is archived") // held until it is active again (claimableSQL)
	default:
		return doneJob("the project is " + p.State)
	}
	if r.Build == stWait {
		if !workspaceReady(d, p) {
			return doneJob("the workspace isn't ready yet") // its last job's end queues this again
		}
		if err := starterBuild(ctx, p); err != nil {
			return jobOutcome{}, err
		}
		r, _ = d.starterOf(p.ID)
	}
	if r.Wiki == stDream {
		left := r.DreamMs + starterDreamWait.Milliseconds() - nowMs()
		if left > 0 {
			return waitJob(min(left, starterPoll.Milliseconds()), "task #1's dream round goes first")
		}
	}
	if r.Wiki == stDue || r.Wiki == stDream || r.wikiFree() && workspaceReady(d, p) {
		if err := starterWiki(p); err != nil {
			return jobOutcome{}, err
		}
		return doneJob("the coordinator builds the initial wiki")
	}
	return doneJob("task #1 is made")
}

// starterBuild makes task #1 as the creator, its record marked in the
// task's own transaction — once, however often this runs. A refusal marks
// it failed instead (the wiki asked too is free to go).
func starterBuild(ctx context.Context, p *Project) error {
	ag := projAg()
	r, _ := ag.db.starterOf(p.ID)
	_, _, err := ag.createTask(ctx, binderWho(r.By), p, TaskSpec{Title: starterTitle, Text: starterBrief,
		InTx: func(t *DB, k *ProjectTask) error {
			cur, ok := t.starterOf(p.ID)
			if !ok || cur.Build != stWait {
				return errStarterDone
			}
			cur.Build, cur.Task = stDone, k.N
			if err := t.putStarter(p.ID, cur); err != nil {
				return err
			}
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": fmt.Sprintf("starter: task #%d created", k.N), "by": "starter"},
				false, "starter:build")
			return nil
		}})
	var pe *projErr
	switch {
	case err == nil || errors.Is(err, errStarterDone):
		return nil
	case !errors.As(err, &pe) || pe.code >= 500:
		return err
	}
	if cur, gerr := ag.db.getProject(p.ID); gerr == nil && cur.State != projActive {
		return err // archived meanwhile: tried again once it is active (its job is held till then)
	}
	return ag.db.Tx(func(t *DB) error {
		cur, ok := t.starterOf(p.ID)
		if !ok || cur.Build != stWait {
			return nil
		}
		cur.Build, cur.Error = stFailed, clip(pe.msg, 400)
		if err := t.putStarter(p.ID, cur); err != nil {
			return err
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "starter: task #1 couldn't be made: " + clip(pe.msg, 300), "by": "starter"},
			true, "starter:build")
		return nil
	})
}

// errStarterDone: task #1 was made already (a second run of the job rolls its task back).
var errStarterDone = errors.New("the starter's task is made already")

// starterWiki sends the creator's coordinator (made now when they have
// none) the one starter message, the record marked in the same
// transaction. A coordinator that can't be made fails the step, said.
func starterWiki(p *Project) error {
	ag := projAg()
	r, _ := ag.db.starterOf(p.ID)
	fail := func(why string) error {
		return ag.db.Tx(func(t *DB) error {
			cur, ok := t.starterOf(p.ID)
			if !ok || cur.Wiki == stDone || cur.Wiki == stFailed {
				return nil
			}
			cur.Wiki, cur.Error = stFailed, clip(why, 400)
			if err := t.putStarter(p.ID, cur); err != nil {
				return err
			}
			addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "starter: the coordinator can't build the initial wiki: " + clip(why, 300), "by": "starter"},
				false, "starter:wiki")
			return nil
		})
	}
	if strings.HasPrefix(r.By, "el:") || r.By == "" {
		return fail("a coordinator is a person's own")
	}
	run, _, err := ag.ensureCoordinator(coordWho(r.By), p)
	if err != nil {
		var pe *projErr
		if errors.As(err, &pe) && pe.code < 500 {
			return fail(pe.msg)
		}
		return err
	}
	sent := false
	err = ag.db.Tx(func(t *DB) error {
		cur, ok := t.starterOf(p.ID)
		if !ok || cur.Wiki == stDone || cur.Wiki == stFailed || cur.Wiki == stWait && !cur.wikiFree() {
			return nil
		}
		if _, _, err := t.enqueue(run.ID, inboxUser, inboxBody{Text: starterWikiText(t, p, cur), Source: srcStarter},
			fmt.Sprintf("proj-starter:%d", p.ID)); err != nil {
			return err
		}
		cur.Wiki = stDone
		if err := t.putStarter(p.ID, cur); err != nil {
			return err
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "starter: the coordinator builds the initial wiki", "by": "starter"},
			false, "starter:wiki")
		if e := projEng(); e != nil {
			e.emitInbox(t, run.ID, run.ID)
		}
		sent = true
		return nil
	})
	if err == nil && sent {
		ag.db.bumpActivity(run.ID)
		if e := projEng(); e != nil {
			e.Poke(run.ID)
		}
	}
	return err
}

// starterWikiText is the coordinator's starter message: the goal, task #1's
// part when it was asked (or what became of it).
func starterWikiText(t *DB, p *Project, r starterRec) string {
	task, verified := "", ""
	switch {
	case r.Build == stDone:
		n := r.Task
		task = fmt.Sprintf("Task #%d ran the builds and tests: read its answer (task_result %d). ", n, n)
		if r.DreamMs > 0 {
			task = fmt.Sprintf("Task #%d ran the builds and tests and kept what it verified in the wiki: read its answer (task_result %d) and those pages, "+
				"and build on them rather than duplicate them. ", n, n)
		}
		if r.Partial {
			task = strings.Replace(task, "ran the builds and tests", "ran the builds and tests but stopped part-way (its turn hit its limit)", 1) +
				"Find out through research what it didn't get to. "
		}
		if k, err := t.taskByN(p.ID, n); err == nil && strings.TrimSpace(k.Last) == "" {
			task = fmt.Sprintf("Task #%d was to run the builds and tests, and ended without an answer: task_status %d and task_result %d say what failed — say it too. ", n, n, n)
		}
		verified = fmt.Sprintf(" (as task #%d verified)", n)
	case r.Build == stFailed:
		task = "The task that was to run the builds and tests couldn't be made (" + strings.TrimSuffix(r.Error, ".") + "): find how through research. "
	}
	return starterHead + " Build this project's initial wiki. " + task +
		"Then use research subagents — one per repo or area, in parallel — to map what the next agent needs: where things live, " +
		"how to build, test and run it" + verified + ", the architecture and main flows, conventions (style, commits, branches, CI) and traps. " +
		"Write a page per topic and the index page (slug `index`), a short map of at most 60 lines; quote paths, commands and errors " +
		"verbatim and say how each fact is known. Then tell your person in two lines what the wiki now covers."
}
