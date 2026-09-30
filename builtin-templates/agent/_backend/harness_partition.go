// harness_partition.go — coding agents (harnesses, D147) in a partitioned
// agent (the owner's ruling, plans/partitions/90-decisions.md §I15; API.md
// "Partitioned instances" → "Coding agents"): **only in a person's own
// conversations**. A sign-in lives in the sandbox's $HOME, so a coding agent
// started anywhere else would put a person's credentials where others reach
// them, and "the global instance holds no person's credentials" would fail.
//
//   - The global instance never starts or drives one: POST /ask and POST
//     /runs with `harness` answer 409, its runs' subagent_spawn offers no
//     `harness` (and refuses one), a sign-in there (POST
//     /runs/{id}/harness/authenticate, the run terminal's login=1) answers
//     409, the catalog says none is available there, and its engine refuses
//     to start or attach one (harnessUse) — so no channel's, trigger's or
//     schedule's run there reaches a coding agent by any road.
//   - A hosted (non-secure) conversation has none: one a coding agent answers
//     or still works in can't be hosted (POST /hosted: 409), a hosted run's
//     subagent_spawn offers none, and the host's engine refuses one.
//   - A conversation doesn't move between homes while a coding agent is in
//     it: one a coding agent answers, or a tree where one is still at work
//     (its run active, or its adapter up), is refused by every copy between
//     homes — publish, copy, the move an un-share starts, hosting and
//     continuing without the host (exportConv) — and un-sharing one at the
//     global instance answers 409 rather than moving it (moveIfUnshared).
//
// In a person's partition coding agents work as unpartitioned, with three
// differences: an idle adapter doesn't keep the partition up (the hold,
// owner.go: only one at work does) — the `wake` job at its reclaim's minute
// brings the partition back to stop it (resume_mode.go) and the reclaim then
// counts from the adapter's last activity (armIdleFrom); a halt read from
// conf reaches a coding agent's turn at its next event (brakeSoon) and its
// pass (brake.go: cancelled when conf is known, parked with a look again
// while it isn't); and its sessions count in the usage totals (usage.go).
// Unpartitioned nothing here changes a thing.
package main

import (
	"errors"
	"time"
)

// Why a coding agent isn't here, for people.
const (
	harnessNotAtGlobal = "coding agents work only in a person's own conversations — not in the shared space, " +
		"where a sign-in would sit in a sandbox others use: start one in your own space"
	harnessNotHosted = "coding agents work only in a person's own conversations — not in a non-secure (hosted) one, " +
		"whose members would drive it with its host's sign-in"
	harnessCantMove = "a coding agent's conversation stays in the space it was started in — it can't be published, " +
		"copied, hosted or moved to another (its sign-in and sandbox are its person's)"
	harnessStillWorks = "a coding agent is still at work in this conversation — stop it (or wait until it finishes), then try again: " +
		"a conversation doesn't move to another space while one is in it"
)

// harnessBarred says why a coding agent may not start or work in run here
// ("" = it may): nowhere at the global instance, not in a hosted
// conversation (every run in team is one: its id says so).
func harnessBarred(run *Run) string {
	switch {
	case globalMode():
		return harnessNotAtGlobal
	case run != nil && partitioned() && hostedID(run.ID):
		return harnessNotHosted
	}
	return ""
}

// harnessMoveErr refuses a copy or move of a conversation a coding agent
// is in (409).
type harnessMoveErr struct{ msg string }

func (e *harnessMoveErr) Error() string { return e.msg }

// isHarnessMoveErr: err is harnessStays'.
func isHarnessMoveErr(err error) bool {
	var h *harnessMoveErr
	return errors.As(err, &h)
}

// harnessStays is why conversation root can't leave this home (nil: it
// may): a coding agent answers it, or one is still at work below it — its
// run active, or its adapter running (starting, live, or waiting for a
// sign-in). Harness children that finished don't hold it: their results
// are the root's transcript, which is what a copy carries.
func (d *DB) harnessStays(root int64) error {
	var eng string
	if err := d.q.QueryRow(`SELECT engine FROM runs WHERE id=?`, root).Scan(&eng); err != nil {
		return nil // the caller reads the run and answers its own 404
	}
	if eng == engineHarness {
		return &harnessMoveErr{harnessCantMove}
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM runs r WHERE r.root_id=?1 AND r.id<>?1 AND r.engine=?2 AND (
		r.status IN ('running','queued','blocked','awaiting','sleeping','waiting_input')
		OR EXISTS(SELECT 1 FROM harness_sessions h WHERE h.run_id=r.id AND h.exec_id<>'' AND h.state IN ('starting','live','login')))`,
		root, engineHarness).Scan(&n)
	if n > 0 {
		return &harnessMoveErr{harnessStillWorks}
	}
	return nil
}

// --- a person's partition: the wake-up, the hold, the brake ------------------------

// harnessIdleNow is the idle reclaim's time as a stopping process reads it
// (the cached conf, never waiting on it: this runs at shutdown).
func (d *DB) harnessIdleNow() time.Duration {
	if harnessIdleTest != 0 {
		return harnessIdleTest
	}
	raw, ok := confSetting("config", true)
	if !ok {
		raw = d.getSetting("config")
	}
	return parseConfig(raw).harnessIdle()
}

// harnessSendingSQL counts the prompts on their way to a coding agent (a
// successor settles each: harness_engine.go attachHarness) — work that moves
// without the person.
const harnessSendingSQL = `(SELECT count(*) FROM harness_sessions h JOIN runs r ON r.id=h.run_id
	WHERE r.engine='harness' AND h.prompt_state='sending')`

// harnessIdleWake is when a stopped partition must come back to stop the
// adapters that rest (up with no turn, not waiting on a person): the
// earliest last activity plus the idle reclaim's time (unix seconds; 0:
// none rests, or the reclaim is off). Never within the minute: a reclaim is
// housekeeping, which the `wake` job does — never the `resume` job's
// @every 1m.
func (d *DB) harnessIdleWake(now time.Time) int64 {
	var last int64
	_ = d.q.QueryRow(`SELECT COALESCE(min(h.last_active_ms), 0) FROM harness_sessions h JOIN runs r ON r.id=h.run_id
		WHERE r.engine=? AND h.exec_id<>'' AND h.state IN ('starting','live') AND h.prompt_state='' AND r.status NOT IN (?, ?)`,
		engineHarness, statusWaiting, statusRunning).Scan(&last)
	idle := d.harnessIdleNow()
	if last <= 0 || idle <= 0 {
		return 0
	}
	return max(time.UnixMilli(last).Add(idle).Unix(), now.Unix()+61)
}

// setRest marks s resting — no turn at work: idle, or parked on a person —
// or working. In a person's partition only a working one keeps the backend
// up (owner.go updateHoldLocked); unpartitioned the hold is today's.
func (s *hsess) setRest(rest bool) {
	if s.rest.Swap(rest) != rest && userMode() {
		s.e.updateHold()
	}
}

// harnessHoldsLocked (e.mu held): a coding agent this process drives keeps
// the hold — any, unpartitioned and at the global instance; in a person's
// partition only one at work (a stopped partition's `wake` brings it back
// for a resting one's reclaim: resume_mode.go).
func (e *Engine) harnessHoldsLocked() bool {
	if !userMode() {
		return len(e.harness) > 0
	}
	for _, s := range e.harness {
		if !s.rest.Load() {
			return true
		}
	}
	return false
}

// updateHold re-reads what the hold wants.
func (e *Engine) updateHold() {
	e.mu.Lock()
	e.updateHoldLocked()
	e.mu.Unlock()
}

// brakeSoon: in a person's partition a halt reaches this process only when
// it reads conf (brake.go) — a coding agent's turn looks at each of its
// events (the cached read, at most one refresh at a time beside it, as a
// built-in turn looks between its steps), and a halt conf says is on pokes
// the run once: its pass cancels it, as PUT /halt cancels a run
// unpartitioned (harnessPass → onBrake). A turn that says nothing (a long
// command) is reached at its next event.
func (s *hsess) brakeSoon() {
	if !userMode() || confIn == nil {
		return
	}
	v := confIn.view(false)
	on := v.State == confKnown && v.Halt == "1"
	s.mu.Lock()
	poke := on && !s.braked && !s.halted
	s.braked = on
	s.mu.Unlock()
	if poke {
		s.e.Poke(s.run)
	}
}

// harnessUsageTx counts a coding agent's session started today (UTC) in a
// person's partition's usage totals (usage.go): a number, no content.
func (d *DB) harnessUsageTx() {
	if !userMode() {
		return
	}
	_, _ = d.q.Exec(`INSERT INTO usage_daily (day, harness_sessions) VALUES (?, 1)
		ON CONFLICT(day) DO UPDATE SET harness_sessions=harness_sessions+1`, time.Now().UTC().Format("2006-01-02"))
}
