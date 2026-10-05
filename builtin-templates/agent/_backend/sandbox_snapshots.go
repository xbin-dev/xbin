// sandbox_snapshots.go — a project's fork-base snapshot (API.md §Big tasks,
// upgrades and pull requests): the snapshot job, which takes one of the
// project's sandbox only while it is quiet — no task of any project
// working there, no command or coding agent busy in it, no workspace job
// running in it — because a snapshot may stop the sandbox; POST
// /projects/{pid}/fork-base, a person's ask for one (now: even while it
// works, which the UI confirms first); and the owner loop that asks for
// one after the project's first warm-up and again when it is older than a
// day and the project was worked in since, and that deletes the forks the
// cleanup left (project_sandbox.go). The credentials of every project in
// the sandbox are scrubbed before it is taken, so a snapshot — and every
// fork made of it — holds no live token.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	projectJobKinds[pjSnapshot] = jobSnapshot
	routeTables = append(routeTables, snapshotRoutes)
	ownerLoops = append(ownerLoops, forkBaseLoop)
}

func snapshotRoutes() []routeDef {
	return []routeDef{
		{"POST /projects/{pid}/fork-base", needAny, projectNeed(lvOwner, handleForkBase)},
	}
}

// forkBaseNow marks a snapshot job a person asked to take now (its
// client id): it doesn't wait for the sandbox to be quiet.
const forkBaseNow = "now"

// forkBaseWait is how long a snapshot a person asked for waits for the
// sandbox to be quiet; the loop's own never waits (a waiting job would keep
// the process up).
var forkBaseWait = 30 * time.Minute

// forkBaseMaxAge: a fork base older than this is taken again (once the
// project was worked in since).
var forkBaseMaxAge = 24 * time.Hour

// forkBaseBackoff: the loop asks for no fork base this soon after the
// last snapshot job of the project ended (failed, not taken, or taken).
var forkBaseBackoff = time.Hour

// forkBaseFirst and forkBaseEvery are the owner loop's first look after a
// takeover, and its period (ns; tests shorten them).
var forkBaseFirst, forkBaseEvery atomic.Int64

func init() {
	forkBaseFirst.Store(int64(2 * time.Minute))
	forkBaseEvery.Store(int64(time.Minute))
}

// snapCaps: what a sandbox manager needs to offer for a fork base — taking
// a snapshot and cloning a sandbox from it.
func snapCaps(h *sbxHello) bool { return h != nil && h.has("snapshots") && h.has("clone") }

// managerSnapCaps: the manager of sandbox ref offers snapshots and clones.
func managerSnapCaps(ctx context.Context, ref string) bool {
	provider, _, ok := splitSandboxRef(ref)
	if !ok {
		return false
	}
	m, ok := boundManager(provider)
	if !ok {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, sbxHelloTimeout)
	defer cancel()
	h, err := managerHello(cctx, m)
	return err == nil && snapCaps(h)
}

// sandboxBusyWhy says what keeps sandbox ref from being quiet ("": it is):
// a task of any project working in it, a command a conversation runs
// there, a busy coding agent, a conversation at work bound to it, a
// workspace job running in it. A store that can't say: busy.
func (d *DB) sandboxBusyWhy(ref string) string {
	count := func(q string, args ...any) int {
		n := 1
		_ = d.q.QueryRow(q, args...).Scan(&n)
		return n
	}
	switch {
	case count(`SELECT count(*) FROM project_tasks k JOIN projects p ON p.id=k.project_id JOIN runs r ON r.id=k.run_id
		WHERE k.run_id<>0 AND (k.sandbox_ref=? OR (k.sandbox_ref IN ('', p.sandbox_ref) AND p.sandbox_ref=?))
		AND r.status IN ('running','queued','awaiting','sleeping')`, ref, ref) > 0:
		return "a task is at work in it"
	case count(`SELECT count(*) FROM sandbox_jobs WHERE ref=? AND state IN ('starting','running')`, ref) > 0:
		return "a command runs in it"
	case count(`SELECT count(*) FROM harness_sessions WHERE ref=? AND (prompt_state<>'' OR state IN ('starting','login'))`, ref) > 0:
		return "a coding agent is busy in it"
	case count(`SELECT count(*) FROM runs WHERE parent_id=0 AND status IN ('running','awaiting')
		AND (json_extract(config, '$.sandbox.ref')=? OR json_extract(config, '$.harness.ref')=?)`, ref, ref) > 0:
		return "a conversation is at work in it"
	case count(`SELECT count(*) FROM project_jobs j JOIN projects p ON p.id=j.project_id
		LEFT JOIN project_tasks k ON k.id=j.task_id
		WHERE j.kind NOT IN ('snapshot','fork') AND (j.state='running' OR (j.state='waiting' AND j.exec_id<>''))
		AND COALESCE(NULLIF(k.sandbox_ref, ''), p.sandbox_ref)=?`, ref) > 0:
		return "a workspace job runs in it"
	}
	return ""
}

// reposReady: every repo of p is ready (and it has one) — its first
// warm-up is over.
func reposReady(d *DB, pid int64) bool {
	rs, err := d.projectRepos(pid)
	if err != nil || len(rs) == 0 {
		return false
	}
	for _, r := range rs {
		if r.State != "ready" {
			return false
		}
	}
	return true
}

// jobSnapshot takes p's fork base: once its repos are ready, while its
// sandbox is quiet (a person's ask waits up to forkBaseWait for that; one
// marked now doesn't), its credentials out of it first. A copy still
// running is looked at again.
func jobSnapshot(ctx context.Context, p *Project, _ *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	d := projAg().db
	// a team definition's is its seed's (API.md §Team projects): what a bot
	// membership's {new} sandbox is cloned from (it holds no credential;
	// the scrub below finds none)
	if p.State != projActive || p.SandboxRef == "" || p.Dir == "" {
		return doneJob("no workspace to snapshot")
	}
	conn, id, err := sbxDialRef(p.SandboxRef, sbxUserOf(binderWho(p.Owner)))
	if err != nil {
		return jobOutcome{}, err
	}
	if j.ExecID != "" { // a copy that was still running
		snaps, err := conn.Snapshots(ctx, id)
		if err != nil {
			return jobOutcome{}, err
		}
		for _, s := range snaps {
			if s.ID == j.ExecID {
				if s.Pending {
					return waitJob(5000, "taking the snapshot")
				}
				return recordForkBase(ctx, conn, id, p, &s)
			}
		}
		j.ExecID = "" // gone: taken again
	}
	if !managerSnapCaps(ctx, p.SandboxRef) {
		return doneJob("the sandbox's manager takes no snapshots or clones: big tasks get fresh sandboxes")
	}
	if !reposReady(d, p.ID) {
		return doneJob("not taken: the project's repos aren't ready yet")
	}
	// From the quiet check to the manager's answer nothing else goes into
	// the sandbox: no git step starts there (the worker's lock for it) and
	// no credential is written there (its credential lock, which every
	// write takes) — the scrub below stays true for the snapshot. A copy
	// still pending holds the sandbox at its manager (it answers state),
	// so nothing is written meanwhile either.
	releaseGit, ok := holdGitSteps(p.SandboxRef)
	if !ok {
		return snapBusy(j, "a workspace job runs in it")
	}
	defer releaseGit()
	defer scmHoldSandbox(p.SandboxRef)()
	if j.ClientID != forkBaseNow {
		if why, err := quietWhy(ctx, d, conn, id, p.SandboxRef); err != nil {
			return jobOutcome{}, err
		} else if why != "" {
			return snapBusy(j, why)
		}
	}
	// no live token goes into the snapshot (nor into a fork made of it):
	// every credential there, of any project, is scrubbed first, and
	// written again by the workspace gate when a task next needs it
	if err := scmScrubSandbox(ctx, p.SandboxRef, scrubStop); err != nil {
		return jobOutcome{}, fmt.Errorf("emptying the credentials before the snapshot: %w", err)
	}
	if live := scmLiveIn(p.SandboxRef); len(live) > 0 {
		return jobOutcome{}, fmt.Errorf("emptying the credentials before the snapshot: %d still there", len(live))
	}
	// one snapshot per job: a retry of the job asks the same (its
	// clientId, its name the project's directory, which never changes)
	cid := fmt.Sprintf("agent:proj:%d:snap:%d:%d", p.ID, j.ID, j.Created)
	snap, err := conn.Snapshot(ctx, id, clip("fork base of "+p.Slug, 64), cid)
	if err != nil {
		if r := sbxRefusal(err); r == "unsupported" || r == "invalid" || r == "exists" {
			return jobOutcome{}, jobFail("taking the snapshot: %v", err)
		}
		return jobOutcome{}, err
	}
	if snap.Pending {
		j.ExecRef, j.ExecID = p.SandboxRef, snap.ID
		return waitJob(5000, "taking the snapshot")
	}
	return recordForkBase(ctx, conn, id, p, snap)
}

// snapBusy is a snapshot job's answer to a sandbox that isn't quiet: a
// person's ask waits (up to forkBaseWait), the loop's own gives up.
func snapBusy(j *ProjectJob, why string) (jobOutcome, error) {
	if j.By != "" && nowMs()-j.Created < forkBaseWait.Milliseconds() {
		return waitJob(30000, "waiting for the sandbox to be quiet: "+why)
	}
	return doneJob("not taken: " + why)
}

// quietWhy says what keeps sandbox ref (id at conn) from being quiet ("":
// nothing): what the agent knows (sandboxBusyWhy), then what only its
// manager knows — a person's terminal, a command started by hand.
func quietWhy(ctx context.Context, d *DB, conn *sbxConn, id, ref string) (string, error) {
	if why := d.sandboxBusyWhy(ref); why != "" {
		return why, nil
	}
	execs, err := conn.ExecList(ctx, id)
	if err != nil {
		return "", err
	}
	for _, x := range execs {
		if x.State == "running" {
			return "a command runs in it (" + clip(orStr(x.Label, "a terminal"), 80) + ")", nil
		}
	}
	return "", nil
}

// holdGitSteps takes the project worker's lock on sandbox ref — the one a
// git step (repo, fetch, prepare, cleanup, refs) holds while it runs there
// — so that none starts there until the release; false: one runs now, or
// is about to. Without a worker (not the engine owner): nothing to hold.
//
// The worker's look at the lock and its claim of a step are two moves (the
// claim in the database between them), so a step it let through just
// before the lock was taken may still be on its way: due (queued or
// waiting, its time come) or claimed. Such a step makes this give way, and
// the release then never clears a lock that a step of this sandbox the
// worker runs holds (the worker sets it with the step as running: its own
// end clears it).
func holdGitSteps(ref string) (func(), bool) {
	projWorkers.Lock()
	w := projWorkers.m[projEng()]
	projWorkers.Unlock()
	if w == nil {
		return func() {}, true
	}
	w.mu.Lock()
	if w.locked[ref] {
		w.mu.Unlock()
		return nil, false
	}
	w.locked[ref] = true
	w.mu.Unlock()
	release := func() {
		releaseGitSteps(w, ref)
		kickProjectWorker() // the steps it held back
	}
	if len(gitStepsIn(projAg().db, ref, true)) > 0 {
		release()
		return nil, false
	}
	return release, true
}

// releaseGitSteps clears the lock on sandbox ref unless a git step of it
// the worker runs holds it now (its ids read first: the worker marks a
// step running in the database before it marks it busy, under w.mu).
func releaseGitSteps(w *projWorker, ref string) {
	ids := gitStepsIn(projAg().db, ref, false)
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, id := range ids {
		if w.busy[id] {
			return
		}
	}
	delete(w.locked, ref)
}

// gitStepsIn lists the git steps (the kinds the worker locks a sandbox
// for) of sandbox ref that are running or live; due: running, or queued
// or waiting with their time come.
func gitStepsIn(d *DB, ref string, due bool) []int64 {
	cond := `j.state IN ('queued','waiting','running')`
	if due {
		// due: one the worker would claim (claimableSQL — not an archived
		// project's held step, which would hold the base off for good)
		cond = `(j.state='running' OR (j.state IN ('queued','waiting') AND j.next_ms<=? AND (j.kind IN ('cleanup','scrub') OR p.state='active')))`
	}
	// a big task's first prepare works in its own new sandbox, not this
	// one (the worker's lockKey)
	cond += ` AND NOT (j.kind='prepare' AND COALESCE(k.size, '')='big' AND COALESCE(k.fork_made, 0)=0)`
	args := []any{pjRepo, pjFetch, pjPrepare, pjCleanup, pjRefs}
	if due {
		args = append(args, nowMs())
	}
	args = append(args, ref)
	rows, err := d.q.Query(`SELECT j.id FROM project_jobs j JOIN projects p ON p.id=j.project_id
		LEFT JOIN project_tasks k ON k.id=j.task_id
		WHERE j.kind IN (?,?,?,?,?) AND `+cond+` AND COALESCE(NULLIF(k.sandbox_ref, ''), p.sandbox_ref)=?`, args...)
	if err != nil {
		return []int64{-1} // unknown: as if one were there
	}
	return scanIDs(rows, nil)
}

// recordForkBase makes snap p's fork base and deletes the one before it.
func recordForkBase(ctx context.Context, conn *sbxConn, id string, p *Project, snap *sbxSnapshot) (jobOutcome, error) {
	old := ""
	err := projAg().db.Tx(func(t *DB) error {
		_ = t.q.QueryRow(`SELECT fork_snap FROM projects WHERE id=?`, p.ID).Scan(&old)
		if _, err := t.q.Exec(`UPDATE projects SET fork_snap=?, fork_snap_ms=? WHERE id=?`, snap.ID, nowMs(), p.ID); err != nil {
			return err
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "took a snapshot of the project's sandbox for big tasks to fork from"}, false, "")
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	if old != "" && old != snap.ID {
		if err := conn.DeleteSnapshot(ctx, id, old); err != nil && sbxRefusal(err) != "not-found" {
			logf("project %d: deleting its old fork base %s: %v", p.ID, old, err)
		}
	}
	return doneJob("took the fork base")
}

// handleForkBase: POST /projects/{pid}/fork-base {now?} — a fork base taken
// when the sandbox is next quiet, or now (it may stop the sandbox: the UI
// confirms first).
func handleForkBase(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var body struct {
		Now bool `json:"now"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch {
	case p.State != projActive:
		xbin.WriteError(w, 409, "this project is "+p.State)
		return
	case p.Kind == projTeam && p.SandboxRef == "":
		xbin.WriteError(w, 409, "this team project has no seed sandbox yet")
		return
	case p.SandboxRef == "" || p.Dir == "":
		xbin.WriteError(w, 409, "the project's sandbox isn't ready yet")
		return
	case !managerSnapCaps(r.Context(), p.SandboxRef):
		writeProjErr(w, &projErr{code: 409, refusal: "unsupported", msg: "the project's sandbox manager takes no snapshots or clones: big tasks get a fresh sandbox"})
		return
	}
	var job *ProjectJob
	by := callerOf(r).tag()
	err := projAg().db.Tx(func(t *DB) error {
		var err error
		if job, err = t.queueJob(p.ID, 0, "", pjSnapshot, by, 0); err != nil {
			return err
		}
		switch {
		case job.State == pjRunning && (job.By == "" || (body.Now && job.ClientID != forkBaseNow)):
			// the loop's own (which waits for nobody), or one not taken now,
			// is at work: what it does was decided — ask again once it ends
			return &projErr{code: 409, refusal: refusalBusy, msg: "a snapshot job of this project is at work: ask again once it ends"}
		case job.State == pjRunning:
		case job.By == "":
			// the loop's job, not started yet: it becomes this person's ask
			// (it waits for quiet, from now)
			job.By, job.Created, job.NextMs = by, nowMs(), 0
			_, err = t.q.Exec(`UPDATE project_jobs SET by_user=?, created_ms=?, next_ms=0 WHERE id=? AND state<>'running'`, job.By, job.Created, job.ID)
			t.AfterCommit(kickProjectWorker)
		}
		if err == nil && body.Now && job.State != pjRunning {
			_, err = t.q.Exec(`UPDATE project_jobs SET client_id=? WHERE id=? AND state<>'running'`, forkBaseNow, job.ID)
			job.ClientID = forkBaseNow
		}
		emitProject(t, p.ID, "job", 0)
		return err
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, 202, map[string]any{"job": job})
}

// --- the owner loop -------------------------------------------------------------------

// forkBaseLoop (ownerLoops): every forkBaseEvery while the engine runs, a
// fork base for each project that needs one and whose sandbox is quiet,
// and the forks a cleanup left behind deleted (forkSweep).
func forkBaseLoop(ctx context.Context, e *Engine) {
	t := time.NewTimer(time.Duration(forkBaseFirst.Load()))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if !e.halted() {
			forkBaseSweep(ctx)
			forkSweep(ctx)
		}
		t.Reset(time.Duration(forkBaseEvery.Load()))
	}
}

// forkBaseSweep queues the snapshot of each active project whose big tasks
// fork (policy bigTasks.mode fork) and that has none yet — once its repos
// are ready — or one older than forkBaseMaxAge with a task changed since;
// only while its sandbox is quiet, and not again within forkBaseBackoff of
// the last one's end (whatever it said).
func forkBaseSweep(ctx context.Context) {
	d := projAg().db
	ps, err := d.projectsWhere(`WHERE state='active' AND sandbox_ref<>'' AND dir<>''`)
	if err != nil {
		return
	}
	for _, p := range ps {
		if ctx.Err() != nil {
			return
		}
		if policyOf(p.Policy).BigTasks.Mode != "fork" {
			if p.ForkSnap != "" {
				dropForkBase(ctx, p) // forks turned off: its base isn't used again
			}
			continue
		}
		if !reposReady(d, p.ID) {
			continue
		}
		if p.ForkSnap != "" {
			var since int
			_ = d.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND updated_ms>?`, p.ID, p.ForkSnapMs).Scan(&since)
			// a team definition (a seed) has no tasks: its base is renewed
			// by age alone
			if nowMs()-p.ForkSnapMs < forkBaseMaxAge.Milliseconds() || (since == 0 && p.Kind != projTeam) {
				continue
			}
		}
		// one at a time, and none within an hour of the last one's end —
		// failed, or done without a snapshot (the sandbox wasn't quiet: an
		// idle coding agent's adapter, a person's terminal may run for hours)
		recent := 1
		_ = d.q.QueryRow(`SELECT count(*) FROM project_jobs WHERE project_id=? AND kind=? AND (state IN ('queued','running','waiting')
			OR (state IN ('done','failed') AND updated_ms>?))`, p.ID, pjSnapshot, nowMs()-forkBaseBackoff.Milliseconds()).Scan(&recent)
		if recent > 0 || d.sandboxBusyWhy(p.SandboxRef) != "" || !managerSnapCaps(ctx, p.SandboxRef) {
			continue
		}
		if _, err := d.queueJob(p.ID, 0, "", pjSnapshot, "", 0); err != nil {
			logf("project %d: queueing its fork base: %v", p.ID, err)
		}
	}
}

// forkKeys is every fork the registry holds (settings proj_fork:<pid>:<n>).
func forkKeys(d *DB) []string {
	rows, err := d.q.Query(`SELECT k FROM settings WHERE k LIKE 'proj\_fork:%' ESCAPE '\'`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if rows.Scan(&k) == nil && strings.HasPrefix(k, forkKeyPrefix) {
			out = append(out, k)
		}
	}
	return out
}

// forkEntryAt reads a registry entry by its key (nil: none).
func forkEntryAt(d *DB, key string) *forkEntry {
	raw := d.getSetting(key)
	if raw == "" {
		return nil
	}
	var e forkEntry
	if json.Unmarshal([]byte(raw), &e) != nil {
		return nil
	}
	return &e
}

// dropForkBase deletes p's fork base at its manager and forgets it (gone
// already: forgotten too; another error: left for the next sweep).
func dropForkBase(ctx context.Context, p *Project) {
	conn, id, err := sbxDialRef(p.SandboxRef, sbxUserOf(binderWho(p.Owner)))
	if err != nil {
		return
	}
	if err := conn.DeleteSnapshot(ctx, id, p.ForkSnap); err != nil && sbxRefusal(err) != "not-found" {
		logf("project %d: deleting its fork base: %v", p.ID, err)
		return
	}
	_, _ = projAg().db.q.Exec(`UPDATE projects SET fork_snap='', fork_snap_ms=0 WHERE id=? AND fork_snap=?`, p.ID, p.ForkSnap)
}
