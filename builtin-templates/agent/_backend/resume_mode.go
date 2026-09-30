// resume_mode.go — what a process exiting with work leaves behind to start
// the backend again, by mode (API.md "Partitioned instances").
//
//   - Unpartitioned and global: today's rule (owner.go) — any work that
//     needs no human (running, queued, blocked, awaiting, sleeping, an
//     undelivered inbox row) leaves the `resume` job, @every 1m.
//   - A person's partition: every running partition counts against the
//     workspace's caps, so it asks to be started only for work that moves
//     without the person, and only as often as that work can move:
//   - `resume` for work a pass would take up now — runs running or
//     queued, an undelivered inbox row, a settled subagent whose parent
//     takes its result (a foreground link to an awaiting parent; a
//     background one to a root that isn't failed — pass() leaves the rest
//     for a person's next message), and a run sleeping on a sandbox job
//     (the job's end is only seen by looking, as legacy's resume does);
//   - else one `wake` job at the minute the earliest timed wait ends — a
//     sleeping run's wake, an awaiting run's subagent deadline — as a
//     5-field cron (UTC);
//   - nothing for runs waiting on a person (who opens the tile anyway),
//     and nothing at all while a manager's halt is on (brake.go: its runs
//     were cancelled; a parked one moves at the next start).
//     The next owner deletes both jobs at takeover.
package main

import (
	"fmt"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// leaveWakeUp registers what brings the backend back for d's pending work.
func (ag *Agent) leaveWakeUp(d *DB) {
	if !userMode() {
		if d.hasWork() {
			ag.registerResumeJob()
		}
		return
	}
	if brakeIdle() {
		return
	}
	switch at := d.userWake(time.Now()); {
	case at.runnable:
		ag.registerResumeJob()
	case at.wake > 0:
		ag.registerWakeJob(at.wake)
	}
}

// userWakeAt is what a person's partition leaves behind: resume now, or wake
// at a time (unix seconds), or nothing.
type userWakeAt struct {
	runnable bool
	wake     int64
}

// userWake looks at d's pending work: runnable work, else the earliest timed
// wait (one due within the minute counts as runnable).
func (d *DB) userWake(now time.Time) userWakeAt {
	var n int
	// a settled link is work only where pass() takes it: a foreground one
	// at an awaiting (or running) parent; a background one at a root that is
	// running, sleeping or resting without an error
	_ = d.q.QueryRow(`SELECT
		(SELECT count(*) FROM runs WHERE status IN ('running','queued'))
		+ (SELECT count(*) FROM inbox WHERE delivered_at=0)
		+ (SELECT count(*) FROM links l JOIN runs p ON p.id = l.parent_id
			WHERE l.state<>'running' AND l.delivered=0 AND (
				(l.mode='fg' AND p.status IN ('running','queued','awaiting'))
				OR (l.mode='bg' AND p.parent_id=0 AND p.status IN ('running','queued','sleeping','idle','done','canceled'))))`).Scan(&n)
	if n > 0 || d.sleepsOnJobs() {
		return userWakeAt{runnable: true}
	}
	var wake int64
	_ = d.q.QueryRow(`SELECT COALESCE(min(wake_at), 0) FROM runs WHERE status IN ('sleeping','awaiting') AND wake_at > 0`).Scan(&wake)
	if wake == 0 {
		return userWakeAt{}
	}
	if wake <= now.Unix()+60 {
		return userWakeAt{runnable: true}
	}
	return userWakeAt{wake: wake}
}

// sleepsOnJobs: a run sleeps on a sandbox job (yield with jobs running, or
// until_job — sandbox_wait.go), which ends when the job does, not at its
// wake time.
func (d *DB) sleepsOnJobs() bool {
	rows, err := d.q.Query(`SELECT pending FROM runs WHERE status='sleeping' AND pending<>''`)
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var p string
		if rows.Scan(&p) == nil && parsePending(p).Kind == "sleep" {
			return true
		}
	}
	return false
}

// wakeSchedule is the 5-field cron (UTC) firing at the first minute at or
// after unix time at.
func wakeSchedule(at int64) string {
	t := time.Unix(at, 0).UTC()
	if t.Second() > 0 {
		t = t.Truncate(time.Minute).Add(time.Minute)
	}
	return fmt.Sprintf("CRON_TZ=UTC %d %d %d %d *", t.Minute(), t.Hour(), t.Day(), int(t.Month()))
}

// registerWakeJob leaves the `wake` job for a sleeping run's time.
func (ag *Agent) registerWakeJob(at int64) {
	if ag.noGateway {
		return
	}
	ag.cronPut(map[string]any{
		"name": "wake", "resource": "res:" + xbin.Self() + "/beat",
		"schedule": wakeSchedule(at), "path": "/tick", "role": "admin",
	})
}
