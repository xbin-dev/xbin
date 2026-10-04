// project_worker.go — the project worker (API.md §The workspace, "Jobs"):
// in the engine owner only, started at takeover (beside the ownerLoops it
// starts with a context the shutdown cancels) and stopped with the engine.
// It claims due jobs of project_jobs under the engine's fence (writing its
// epoch: at takeover a running job of an older epoch is queued again, and
// finds its exec again by its clientId), runs at most projWorkerSlots at
// once and one git step per sandbox at a time, and records each outcome: done,
// waiting (look again at next_ms), or failed — retried with backoff (10 s
// doubling to 10 min) up to five attempts, then the task's workspace fails.
// A kind nobody registered fails "not in this build". Once a minute it
// prunes old rows and queues the fetches of busy projects.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// projWorkerSlots: jobs one worker runs at once.
const projWorkerSlots = 4

// projJobMaxAttempts: a job's tries before it fails.
const projJobMaxAttempts = 5

// projJobMaxAge: a job not done this long after it was queued fails (a
// stuck wait never keeps the engine up for ever).
var projJobMaxAge = 3 * time.Hour

type projWorker struct {
	e      *Engine
	ctx    context.Context
	cancel context.CancelFunc
	kick   chan struct{}

	mu      sync.Mutex
	busy    map[int64]bool  // jobs running here
	locked  map[string]bool // sandboxes a git step is running in
	pending bool            // live jobs other than fetches exist (the hold)
	swept   time.Time
}

var projWorkers = struct {
	sync.Mutex
	m map[*Engine]*projWorker
}{m: map[*Engine]*projWorker{}}

// startProjects (takeover): the worker, and every ownerLoops entry with the
// same context. Not for a host's engine over team.
func (e *Engine) startProjects() {
	if e.scope != nil || e.ag == nil || !e.db.features {
		return
	}
	e.mu.Lock()
	epoch, closing := e.epoch, e.closing
	e.mu.Unlock()
	if closing {
		return
	}
	ctx, cancel := context.WithCancel(e.base)
	w := &projWorker{e: e, ctx: ctx, cancel: cancel, kick: make(chan struct{}, 1), busy: map[int64]bool{}, locked: map[string]bool{}}
	projWorkers.Lock()
	if old := projWorkers.m[e]; old != nil {
		old.cancel()
	}
	projWorkers.m[e] = w
	projWorkers.Unlock()
	_ = e.fenced(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE project_jobs SET state='queued', updated_ms=? WHERE state='running' AND epoch<>?`, nowMs(), epoch)
		return err
	})
	go w.loop()
	for _, l := range ownerLoops {
		go l(ctx, e)
	}
}

// stopProjects (BeginShutdown): the worker and the owner loops stop; a job
// in flight writes nothing more (the next owner takes it up again).
func (e *Engine) stopProjects() {
	projWorkers.Lock()
	w := projWorkers.m[e]
	delete(projWorkers.m, e)
	projWorkers.Unlock()
	if w != nil {
		w.cancel()
	}
}

// kickProjectWorker makes the worker look at the jobs now.
func kickProjectWorker() {
	projWorkers.Lock()
	defer projWorkers.Unlock()
	for _, w := range projWorkers.m {
		select {
		case w.kick <- struct{}{}:
		default:
		}
	}
}

// projectsHoldLocked (e.mu held; owner.go): the worker has work in flight
// or due — the engine keeps the process. Memory only: no database under
// the engine's lock.
func (e *Engine) projectsHoldLocked() bool {
	projWorkers.Lock()
	w := projWorkers.m[e]
	projWorkers.Unlock()
	if w == nil {
		return false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.busy) > 0 || w.pending
}

// projectsWork (hasWork): jobs other than fetches are live — the engine
// has work no person needs to start. A fetch alone never brings a stopped
// process back.
func (d *DB) projectsWork() bool {
	if !d.features {
		return false
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_jobs WHERE state IN ('queued','running','waiting') AND kind<>'fetch'`).Scan(&n)
	return n > 0
}

// projectsWake (userWake, a person's partition): due project jobs count as
// work to take up now; a later one is a time to come back; so is an
// undelivered event that should wake one of the person's coordinators.
func (d *DB) projectsWake(now time.Time) (runnable bool, wake int64) {
	if !d.features {
		return false, 0
	}
	var due, at int64
	_ = d.q.QueryRow(`SELECT count(*), COALESCE(MIN(next_ms), 0) FROM project_jobs WHERE state IN ('queued','running','waiting')
		AND kind<>'fetch'`).Scan(&due, &at)
	if due > 0 && at <= now.Add(time.Minute).UnixMilli() {
		return true, 0
	}
	var ev int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_events e WHERE e.wake=1 AND e.delivered=0 AND EXISTS
		(SELECT 1 FROM runs r WHERE r.origin='project' AND r.parent_id=0 AND r.session_key='proj:' || e.project_id || ':coord:' || e.coord_user)`).Scan(&ev)
	if ev > 0 {
		return true, 0
	}
	if due > 0 {
		return false, at / 1000
	}
	return false, 0
}

func (w *projWorker) updateHold() {
	w.e.mu.Lock()
	w.e.updateHoldLocked()
	w.e.mu.Unlock()
}

func (w *projWorker) loop() {
	defer func() {
		projWorkers.Lock()
		if projWorkers.m[w.e] == w {
			delete(projWorkers.m, w.e)
		}
		projWorkers.Unlock()
	}()
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-w.ctx.Done():
			return
		case <-w.kick:
		case <-t.C:
		}
		next := w.pass()
		if time.Since(w.swept) > time.Minute {
			w.swept = time.Now()
			w.sweep()
		}
		if !t.Stop() {
			select {
			case <-t.C:
			default:
			}
		}
		t.Reset(next)
	}
}

// pass claims and starts the due jobs it has room for; it answers when to
// look again.
func (w *projWorker) pass() time.Duration {
	next := 30 * time.Second
	if w.e.halted() {
		return next
	}
	d := w.e.db
	jobs := d.jobsWhere(`WHERE state IN ('queued','waiting') AND (kind IN ('cleanup','scrub') OR project_id IN
		(SELECT id FROM projects WHERE state='active')) ORDER BY next_ms, id LIMIT 100`)
	var live int
	_ = d.q.QueryRow(`SELECT count(*) FROM project_jobs WHERE state IN ('queued','running','waiting') AND kind<>'fetch'`).Scan(&live)
	w.mu.Lock()
	changed := w.pending != (live > 0)
	w.pending = live > 0
	w.mu.Unlock()
	if changed {
		w.updateHold()
	}
	now := nowMs()
	for _, j := range jobs {
		w.mu.Lock()
		full, running := len(w.busy) >= projWorkerSlots, w.busy[j.ID]
		w.mu.Unlock()
		if running {
			continue
		}
		if j.NextMs > now {
			next = min(next, time.Duration(j.NextMs-now)*time.Millisecond)
			continue
		}
		if full {
			next = min(next, 200*time.Millisecond)
			break
		}
		key := w.lockKey(j)
		w.mu.Lock()
		if key != "" && w.locked[key] {
			w.mu.Unlock()
			next = min(next, 200*time.Millisecond)
			continue
		}
		w.mu.Unlock()
		if !w.claim(j) {
			continue
		}
		w.mu.Lock()
		w.busy[j.ID] = true
		if key != "" {
			w.locked[key] = true
		}
		w.mu.Unlock()
		w.updateHold()
		go w.run(j, key)
	}
	return max(next, 50*time.Millisecond)
}

// lockKey is the sandbox a git step of j works in ("" for a job that takes
// no lock): one such step at a time per sandbox.
func (w *projWorker) lockKey(j *ProjectJob) string {
	switch j.Kind {
	case pjRepo, pjFetch, pjPrepare, pjCleanup, pjRefs:
	default:
		return ""
	}
	p, err := w.e.db.getProject(j.Project)
	if err != nil {
		return ""
	}
	if j.Task != 0 {
		if k, err := w.e.db.taskByID(j.Task); err == nil {
			return taskRef(p, k)
		}
	}
	return p.SandboxRef
}

// claim marks j running under this engine's epoch (false: another took it,
// or this engine no longer owns the database).
func (w *projWorker) claim(j *ProjectJob) bool {
	ok := false
	w.e.mu.Lock()
	epoch := w.e.epoch
	w.e.mu.Unlock()
	_ = w.e.fenced(func(t *DB) error {
		res, err := t.q.Exec(`UPDATE project_jobs SET state='running', epoch=?, updated_ms=? WHERE id=? AND state IN ('queued','waiting')`,
			epoch, nowMs(), j.ID)
		ok = err == nil && rowsAffected(res) == 1
		return err
	})
	j.Epoch = epoch
	return ok
}

// run runs one step of j and records what it said.
func (w *projWorker) run(j *ProjectJob, key string) {
	defer func() {
		w.mu.Lock()
		delete(w.busy, j.ID)
		if key != "" {
			delete(w.locked, key)
		}
		w.mu.Unlock()
		w.updateHold()
		kickProjectWorker()
	}()
	d := w.e.db
	p, err := d.getProject(j.Project)
	if err != nil {
		_, _ = d.q.Exec(`DELETE FROM project_jobs WHERE id=?`, j.ID)
		return
	}
	var k *ProjectTask
	if j.Task != 0 {
		if k, err = d.taskByID(j.Task); err != nil {
			k = nil
		}
	}
	var out jobOutcome
	switch fn := projectJobKinds[j.Kind]; {
	case fn == nil:
		err = jobFail("a %s job is not in this build", j.Kind)
	case nowMs()-j.Created > projJobMaxAge.Milliseconds():
		err = jobFail("gave up: not done %s after it was queued", projJobMaxAge)
	case j.Task != 0 && k == nil:
		err = jobFail("the task is gone")
	default:
		ctx, cancel := context.WithTimeout(w.ctx, 35*time.Minute)
		out, err = safeJob(fn, ctx, p, k, j)
		cancel()
	}
	if w.ctx.Err() != nil {
		return // shutting down or fenced out: the next owner takes it up again
	}
	w.finish(p, k, j, out, err)
}

// safeJob runs fn, a panic being its error.
func safeJob(fn projectJobFunc, ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (out jobOutcome, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the %s job panicked: %v", j.Kind, r)
		}
	}()
	return fn(ctx, p, k, j)
}

// jobBackoff is the wait before a failed attempt's retry.
func jobBackoff(attempts int) time.Duration {
	d := 10 * time.Second
	for i := 1; i < attempts && d < 10*time.Minute; i++ {
		d *= 2
	}
	return min(d, 10*time.Minute)
}

// finish records a step's outcome (fenced by the epoch it was claimed in).
func (w *projWorker) finish(p *Project, k *ProjectTask, j *ProjectJob, out jobOutcome, err error) {
	now := nowMs()
	state, next, errText := pjDone, int64(0), ""
	var fail *jobFailErr
	switch {
	case err == nil && out.Done:
	case err == nil && out.WaitMs > 0:
		state, next = pjWaiting, now+out.WaitMs
	case err == nil:
		state, next = pjQueued, now
	case errors.As(err, &fail) || j.Attempts+1 >= projJobMaxAttempts:
		state, errText = pjFailed, clip(projRedact(err.Error()), 4000)
		j.Attempts++
	default:
		j.Attempts++
		state, next, errText = pjQueued, now+jobBackoff(j.Attempts).Milliseconds(), clip(projRedact(err.Error()), 4000)
		j.ExecID = "" // the next attempt starts its exec afresh (a new clientId)
	}
	step := orStr(out.Step, j.Step)
	if state == pjFailed {
		step = "failed"
	}
	_ = w.e.fenced(func(t *DB) error {
		if state == pjDone && j.Kind == pjRefs && j.ClientID != refsReadPulls {
			// an scm event asked for the pull requests while this one ran: once more
			var cid string
			if t.q.QueryRow(`SELECT client_id FROM project_jobs WHERE id=?`, j.ID).Scan(&cid) == nil && cid == refsReadPulls {
				state, next, j.ClientID = pjQueued, now, cid
			}
		}
		res, err := t.q.Exec(`UPDATE project_jobs SET state=?, step=?, attempts=?, next_ms=?, exec_ref=?, exec_id=?, client_id=?,
			out=?, error=?, updated_ms=? WHERE id=? AND epoch=?`, state, step, j.Attempts, next, j.ExecRef, j.ExecID, j.ClientID,
			clip(projRedact(j.Out), 8<<10), errText, now, j.ID, j.Epoch)
		if err != nil || rowsAffected(res) != 1 {
			return err
		}
		if j.Kind == pjCleanup && j.Task == 0 { // a project's deletion: its own row goes with the rest
			_, _ = t.q.Exec(`DELETE FROM project_jobs WHERE id=? AND NOT EXISTS (SELECT 1 FROM projects WHERE id=?)`, j.ID, p.ID)
		}
		if state == pjFailed {
			jobFailed(t, p, k, j, errText)
		}
		if state == pjDone || state == pjFailed {
			// what waits on this one looks again now (prepare on the sandbox
			// and the repos, bind on the setups)
			_, _ = t.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND state='waiting' AND kind IN (?, ?)`, p.ID, pjPrepare, pjBind)
		}
		emitProject(t, p.ID, "job", 0)
		return nil
	})
}

// jobFailed tells what a failed job means: a task's workspace fails; the
// project's own (its sandbox, a repo) is an event the owner's coordinator
// wakes for (the tasks waiting on it fail at their next look).
func jobFailed(t *DB, p *Project, k *ProjectTask, j *ProjectJob, errText string) {
	if k != nil {
		if cur, err := t.taskByID(k.ID); err == nil {
			k = cur
		}
		if j.Kind == pjCleanup || j.Kind == pjRefs || k.Phase == phaseDeleted {
			addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": fmt.Sprintf("its %s job failed: %s", j.Kind, clip(errText, 400))}, false, "")
			return
		}
		setWS(t, p, k, wsFailed, errText)
		addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its workspace failed: " + clip(errText, 400)}, true, "")
		return
	}
	if j.Kind == pjSandbox || j.Kind == pjRepo {
		addProjectEvent(t, p.ID, 0, pevWorkspace, map[string]any{"text": fmt.Sprintf("the project's %s job failed: %s", j.Kind, clip(errText, 400))}, true, "")
	}
}

// sweep prunes finished jobs (7 days) and events (30 days), and queues the
// fetches of projects someone works in or looks at.
func (w *projWorker) sweep() {
	d := w.e.db
	_, _ = d.q.Exec(`DELETE FROM project_jobs WHERE state IN ('done','failed') AND updated_ms<?`, nowMs()-7*24*3600*1000)
	_, _ = d.q.Exec(`DELETE FROM project_events WHERE created<?`, nowMs()-30*24*3600*1000)
	ps, _ := d.projectsWhere(`WHERE state='active' AND dir<>'' AND sandbox_ref<>''`)
	for _, p := range ps {
		busy := nowMs()-statusReadAt(p.ID) < 5*60*1000
		if !busy {
			var n int
			_ = d.q.QueryRow(`SELECT count(*) FROM project_tasks k JOIN runs r ON r.id=k.run_id WHERE k.project_id=?
				AND r.status IN ('running','queued','awaiting','sleeping')`, p.ID).Scan(&n)
			busy = n > 0
		}
		if !busy {
			continue
		}
		every := int64(policyOf(p.Policy).FetchEveryMin) * 60 * 1000
		repos, _ := d.projectRepos(p.ID)
		for _, r := range repos {
			if r.State == "ready" && r.Mode == repoBare && nowMs()-r.FetchedMs > every {
				_, _ = d.queueJob(p.ID, 0, r.Slug, pjFetch, "", 0)
			}
		}
	}
}
