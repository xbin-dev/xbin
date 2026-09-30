// resume_mode.go — what a process exiting with work leaves behind to start
// the backend again, by mode (API.md "Partitioned instances").
//
//   - Unpartitioned and global: today's rule (owner.go) — any work that
//     needs no human (running, queued, blocked, awaiting, sleeping, an
//     undelivered inbox row) leaves the `resume` job, @every 1m.
//   - A person's partition: every running partition counts against the
//     workspace's caps, so it asks to be started only for work that can move
//     without the person — runs that are running or queued, an undelivered
//     inbox row, a settled subagent whose parent hasn't taken its result —
//     with `resume`; for sleeping runs, one `wake` job at the minute the
//     earliest one wakes (a 5-field cron, UTC); and nothing for runs awaiting
//     a person, who opens the tile anyway. The next owner deletes both at
//     takeover.
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

// userWake looks at d's pending work: runnable work, else the earliest wake
// of a sleeping run (one due within the minute counts as runnable).
func (d *DB) userWake(now time.Time) userWakeAt {
	var n int
	_ = d.q.QueryRow(`SELECT
		(SELECT count(*) FROM runs WHERE status IN ('running','queued'))
		+ (SELECT count(*) FROM inbox WHERE delivered_at=0)
		+ (SELECT count(*) FROM links WHERE state<>'running' AND delivered=0)`).Scan(&n)
	if n > 0 {
		return userWakeAt{runnable: true}
	}
	var wake int64
	_ = d.q.QueryRow(`SELECT COALESCE(min(wake_at), 0) FROM runs WHERE status='sleeping' AND wake_at > 0`).Scan(&wake)
	if wake == 0 {
		return userWakeAt{}
	}
	if wake <= now.Unix()+60 {
		return userWakeAt{runnable: true}
	}
	return userWakeAt{wake: wake}
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
