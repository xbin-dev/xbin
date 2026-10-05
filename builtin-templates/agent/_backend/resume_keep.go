// resume_keep.go — a person's partition keeps its way back registered while
// it idles (API.md "Partitioned instances" → Resume).
//
// resume_mode.go says what a stopping partition leaves behind: `resume` for
// work that moves without its person, else one `wake` at its earliest timed
// wait (a sleeping run, a subagent's deadline, a coding agent's idle stop).
// leaveWakeUp leaves it at the exit (Engine.Shutdown) — but xbind stops a
// person's partition with its instance token revoked first (the idle reap,
// the person's own stop, a switch: every stop of a person's partition,
// D143), so the exiting process's cron calls are refused and nothing is
// left: a coding agent idle in its sandbox ran on until its person came
// back (the W6 e2e, partitions_agent_harness_test.go, found it).
//
// So in a person's partition the owner registers the same jobs while it
// still can: whenever its engines let the hold go — the moment xbind may
// reap it — and once its takeover has cleared the last owner's jobs, and it
// deletes one no longer wanted. It never looks while an engine holds (work
// runs: the hold's release looks again) nor once it shuts down (the
// successor's takeover owns the jobs then). A job that fires while the
// partition still runs is a pass it would make anyway (POST /tick →
// recover). The exit still leaves them where it can. Unpartitioned and at
// the global instance nothing here runs: the exit leaves them, as ever.
package main

import (
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wakeKeepDelay coalesces a burst of hold changes into one look (a var so
// tests can shorten it).
var wakeKeepDelay = time.Second

// wakeKeeper is what a person's partition registered for its way back.
type wakeKeeper struct {
	mu      sync.Mutex
	ready   bool              // a takeover cleared the jobs; this process keeps them since
	pending bool              // a look is scheduled
	have    map[string]string // job name → the schedule this process registered
	run     sync.Mutex        // one look at a time
}

// wakeKeepReady: a takeover cleared the jobs (clearWakeJobs) — the main
// engine's, or a host engine's, which clears the same names (Engine.keep is
// the partition's agent for both) — so from now on this process keeps them,
// starting from none.
func (ag *Agent) wakeKeepReady() {
	if ag == nil || ag.noGateway || !userMode() { // noGateway first: tests switch the mode under a settling engine
		return
	}
	k := &ag.wakeKeep
	k.mu.Lock()
	k.ready, k.have = true, map[string]string{}
	k.mu.Unlock()
	ag.keepWakeUpSoon()
}

// keepWakeUpSoon schedules one look (coalesced): a person's partition only,
// once its takeover is done.
func (ag *Agent) keepWakeUpSoon() {
	if ag == nil || ag.noGateway || !userMode() { // noGateway first: tests switch the mode under a settling engine
		return
	}
	k := &ag.wakeKeep
	k.mu.Lock()
	if !k.ready || k.pending {
		k.mu.Unlock()
		return
	}
	k.pending = true
	k.mu.Unlock()
	time.AfterFunc(wakeKeepDelay, func() {
		k.mu.Lock()
		k.pending = false
		k.mu.Unlock()
		ag.keepWakeUp(time.Now())
	})
}

// keepWakeUp registers what this partition's exit would leave now and
// deletes what it wouldn't — unless an engine holds the partition or is
// shutting down.
func (ag *Agent) keepWakeUp(now time.Time) {
	if ag.noGateway || !userMode() {
		return
	}
	k := &ag.wakeKeep
	k.run.Lock()
	defer k.run.Unlock()
	if ag.eng == nil || ag.eng.busy() {
		return
	}
	if h := hostEngine.Load(); h != nil && h.busy() {
		return
	}
	want := userWakeJobs(ag.db, now)
	for _, name := range []string{"resume", "wake"} {
		k.mu.Lock()
		had, ready := k.have[name], k.ready
		k.mu.Unlock()
		w := want[name]
		if !ready || w == had {
			continue
		}
		if w == "" {
			ag.cronDelete(name)
		} else if !ag.putWakeJob(name, w) {
			continue // looked at again at the hold's next release
		}
		k.mu.Lock()
		if w == "" {
			delete(k.have, name)
		} else {
			k.have[name] = w
		}
		k.mu.Unlock()
	}
}

// busy: e holds its partition up (updateHoldLocked's rule), hasn't taken
// over yet, or is shutting down.
func (e *Engine) busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closing || !e.owned || len(e.actors) > 0 || len(e.timers) > 0 || e.harnessHoldsLocked() || e.projectsHoldLocked()
}

// userWakeJobs is what a person's partition leaves to be started again, as
// {job name: schedule} (resume_mode.go's rule, and a host's hosted
// conversations': hosted_engine.go hostedWakeAt): `resume` for work a pass
// would take up now, else `wake` at the earliest timed wait; under the brake
// only a coding agent's idle stop.
func userWakeJobs(d *DB, now time.Time) map[string]string {
	out := map[string]string{}
	if brakeIdle() {
		if at := d.harnessIdleWake(now); at > 0 {
			out["wake"] = wakeSchedule(at)
		}
		return out
	}
	at := d.userWake(now)
	if h, ok := hostedWakeAt(d, now); ok {
		switch {
		case h.runnable:
			at.runnable = true
		case h.wake > 0 && (at.wake == 0 || h.wake < at.wake):
			at.wake = h.wake
		}
	}
	switch {
	case at.runnable:
		out["resume"] = resumeSchedule
	case at.wake > 0:
		out["wake"] = wakeSchedule(at.wake)
	}
	return out
}

// hostedWakeAt is what the conversations this partition hosts wait for
// (hosted_engine.go hostedWakeUp's rule; d is the partition's own db, which
// lists them); ok false when it hosts none.
func hostedWakeAt(d *DB, now time.Time) (userWakeAt, bool) {
	if d == nil || hostEngine.Load() == nil {
		return userWakeAt{}, false
	}
	tr := teamRuns()
	if tr == nil {
		return userWakeAt{}, false
	}
	roots := scanIDs(d.q.Query(`SELECT conversation FROM hosted WHERE state='active'`))
	if len(roots) == 0 {
		return userWakeAt{}, false
	}
	return tr.hostedWake(roots, now), true
}

// resumeSchedule is the resume job's (owner.go).
const resumeSchedule = "@every 1m"

// putWakeJob registers job name ("resume" or "wake") at schedule.
func (ag *Agent) putWakeJob(name, schedule string) bool {
	if ag.noGateway {
		return false
	}
	return ag.cronPut(map[string]any{
		"name": name, "resource": "res:" + xbin.Self() + "/beat",
		"schedule": schedule, "path": "/tick", "role": "admin",
	})
}
