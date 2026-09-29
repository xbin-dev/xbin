// sandbox_wait.go — a sleeping run wakes when its sandbox jobs end (D134).
//
// yield parks a run until its wake time (the timer); a run that started
// sandbox jobs also sleeps on them: it wakes as soon as one of the jobs it
// started ends — or, with until_job, the one job it names. What it sleeps on
// is its pending state ({kind: sleep, since, job}), so the wake is durable:
// every pass over a sleeping run (the poke a recorded end gives it, a
// restart's recovery) asks the jobs table whether a job it sleeps on has
// ended, and nothing else decides.
//
// The table learns of an end when something reads the job — bash_output,
// bash_kill, jobs — or, while nothing does, from the watcher a sleeping run
// has: a long-poll per job at its manager (its output's end, never the
// output), which records the end and pokes the run. No ticker: a watcher
// waits on the manager, and only while the run sleeps.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	yieldJobMax     = 3600             // seconds a yield {until_job} sleeps when it names none
	jobWatchPoll    = 25 * time.Second // one long-poll at the manager
	jobWatchPace    = 2 * time.Second  // between polls of a job that keeps writing
	jobWatchBackoff = 10 * time.Second // after a manager that didn't answer
)

// yieldSpec is the yield tool: seconds always; until_job where a sandbox
// is bound (its jobs are the conversation's).
func yieldSpec(cfg Config) toolSpec {
	props := map[string]any{"seconds": intProp("how long to sleep (with until_job: the most, default 3600)")}
	desc := "Sleep for a while, then resume automatically (durable: it survives restarts). Use when you should wait before continuing."
	if sandboxToolsOn(cfg) {
		desc = "Sleep for a while, then resume automatically (durable: it survives restarts) — early when a sandbox job you started ends, or, with until_job, when that job ends. " +
			"Use when you should wait before continuing, e.g. for a background job (instead of polling bash_output)."
		props["until_job"] = intProp("sleep until this sandbox job ends (a job that has already ended returns at once)")
	}
	return toolSpec{Type: "function", Function: funcDef{Name: "yield", Description: desc, Parameters: obj(nil, props)}}
}

// sleepPending is the pending state of a run yielding with sandbox jobs:
// since (unix ms) is when it went to sleep; job, when named, the one job
// it waits for.
func sleepPending(job int) pendingState {
	return pendingState{Kind: "sleep", Job: job, Since: nowMs()}
}

// runningJobsOf is the jobs run started that still run, as the table has them.
func (d *DB) runningJobsOf(run int64) []*sbxJob {
	rows, err := d.q.Query(`SELECT `+jobCols+` FROM sandbox_jobs WHERE run_id=? AND state IN ('starting','running') ORDER BY job`, run)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*sbxJob
	for rows.Next() {
		if j, err := scanJob(rows); err == nil {
			out = append(out, j)
		}
	}
	return out
}

// yieldPlan is what a yield call does: how long it sleeps, its pending
// state ("" = the plain timer, as before) and its answer.
func (e *Engine) yieldPlan(run *Run, root int64, args map[string]any) (secs int, pend, text string) {
	secs = max(toInt(args["seconds"]), 0)
	if n := toInt(args["until_job"]); n > 0 {
		j, err := e.db.job(root, n)
		switch {
		case err != nil:
			return 0, "", fmt.Sprintf("(didn't sleep: %v)", err)
		case !j.running():
			return 0, "", fmt.Sprintf("(didn't sleep: job %d has already ended — %s; bash_output {\"job\": %d} reads it)", n, endWords(j.State, j.Exit, j.Signal), n)
		}
		if secs == 0 {
			secs = yieldJobMax
		}
		return secs, pendJSON(sleepPending(n)), fmt.Sprintf("(sleeping until job %d ends, %ds at most)", n, secs)
	}
	jobs := e.db.runningJobsOf(run.ID)
	if len(jobs) == 0 {
		return secs, "", fmt.Sprintf("(yielded %ds)", secs)
	}
	nums := make([]string, len(jobs))
	for i, j := range jobs {
		nums[i] = fmt.Sprint(j.Job)
	}
	return secs, pendJSON(sleepPending(0)), fmt.Sprintf("(yielded %ds — waking early when job %s ends)", secs, strings.Join(nums, " or "))
}

func pendJSON(p pendingState) string {
	b, _ := json.Marshal(p)
	return string(b)
}

// jobWake: a sleeping run's job ended — the one it named, or any it started
// since it went to sleep.
func (e *Engine) jobWake(run *Run) bool {
	p := parsePending(run.Pending)
	if p.Kind != "sleep" {
		return false
	}
	if p.Job > 0 {
		j, err := e.db.job(rootOf(run), p.Job)
		return err != nil || !j.running()
	}
	var one int
	err := e.db.q.QueryRow(`SELECT 1 FROM sandbox_jobs WHERE run_id=? AND ended_ms>=? AND state NOT IN ('starting','running') LIMIT 1`,
		run.ID, p.Since).Scan(&one)
	return err == nil
}

// --- the watcher ------------------------------------------------------------------

// jobWatch is a sleeping run's watcher: cancel stops it.
type jobWatch struct{ cancel context.CancelFunc }

// watchJobs follows the jobs a sleeping run sleeps on until one ends (once
// per run; a second call while it watches does nothing).
func (e *Engine) watchJobs(run *Run) {
	p := parsePending(run.Pending)
	if p.Kind != "sleep" || e.ag == nil {
		return
	}
	root := rootOf(run)
	var jobs []*sbxJob
	if p.Job > 0 {
		if j, err := e.db.job(root, p.Job); err == nil && j.running() {
			jobs = append(jobs, j)
		}
	} else {
		jobs = e.db.runningJobsOf(run.ID)
	}
	if len(jobs) == 0 {
		return
	}
	cfg, err := e.db.runConfig(root) // the conversation's bindings, as they are now
	if err != nil {
		return
	}
	e.mu.Lock()
	if e.closing || !e.owned || e.jobWatch[run.ID] != nil {
		e.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(e.base)
	w := &jobWatch{cancel: cancel}
	e.jobWatch[run.ID] = w
	e.mu.Unlock()

	id := run.ID
	sleeping := func() bool {
		r, err := e.db.getRun(id)
		return err == nil && r.Status == statusSleep
	}
	var wg sync.WaitGroup
	for _, j := range jobs {
		wg.Add(1)
		go func(j *sbxJob) {
			defer wg.Done()
			if e.ag.followJobEnd(ctx, cfg, j, sleeping) && ctx.Err() == nil {
				e.Poke(id) // the job may be another run's: its end pokes that one
			}
		}(j)
	}
	go func() {
		wg.Wait()
		e.stopJobWatch(id, w)
	}()
}

// stopJobWatch stops a run's watcher (w: only that one; nil: whichever).
func (e *Engine) stopJobWatch(id int64, w *jobWatch) {
	e.mu.Lock()
	cur := e.jobWatch[id]
	if cur != nil && (w == nil || cur == w) {
		delete(e.jobWatch, id)
	}
	e.mu.Unlock()
	if cur != nil && (w == nil || cur == w) {
		cur.cancel()
	}
}

// followJobEnd waits at its manager for a job to end, and records how it
// did (true) — until ctx ends, or still says the wait is over. It asks
// only where the job stands, as the person who bound its sandbox.
func (ag *Agent) followJobEnd(ctx context.Context, cfg Config, j *sbxJob, still func() bool) bool {
	b, ok := cfg.sandboxBinding(j.Ref)
	if !ok {
		return false // detached: the detach recorded it (stopDetachedJobs)
	}
	conn, id, err := sbxDialRef(j.Ref, sbxUserOf(binderWho(b.By)))
	if err != nil {
		return false
	}
	pause := func(d time.Duration) bool {
		t := time.NewTimer(d)
		defer t.Stop()
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
			return true
		}
	}
	var total int64
	for ctx.Err() == nil && still() {
		if j.Exec == "" { // its start was cut short: find it by its clientId
			execs, err := conn.ExecList(ctx, id)
			if err != nil {
				if gone(err) {
					ag.jobEnded(j, "lost", nil, "")
					return true
				}
				if !pause(jobWatchBackoff) {
					return false
				}
				continue
			}
			for _, ex := range execs {
				if ex.ClientID == j.clientID() {
					ag.db.jobStarted(j, ex.ID)
				}
			}
			if j.Exec == "" {
				if time.Since(time.UnixMilli(j.Created)) > sbxCallTimeout {
					ag.jobEnded(j, "lost", nil, "")
					return true
				}
				if !pause(jobWatchPace) {
					return false
				}
				continue
			}
		}
		ch, err := conn.ExecOutput(ctx, id, j.Exec, total, 1, int(jobWatchPoll/time.Millisecond), false)
		switch {
		case ctx.Err() != nil:
			return false
		case gone(err):
			ag.jobEnded(j, "lost", nil, "")
			return true
		case err != nil:
			if !pause(jobWatchBackoff) {
				return false
			}
			continue
		case ch.State != "running":
			ag.jobEnded(j, ch.State, ch.ExitCode, ch.Signal)
			return true
		}
		if ch.Total > total { // it keeps writing: wait for more, at a pace
			moved := total > 0
			total = ch.Total
			if moved && !pause(jobWatchPace) {
				return false
			}
		}
	}
	return false
}
