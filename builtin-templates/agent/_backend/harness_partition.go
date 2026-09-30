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
// In a person's partition coding agents work as unpartitioned, with these
// differences: only one at work — a turn, or a sign-in AgTT is waiting on —
// keeps the partition up (the hold, owner.go), never one idle or waiting on
// a person; the `wake` job at an idle one's reclaim minute brings the
// partition back to stop it (resume_mode.go), the reclaim then counting from
// the adapter's last activity (armIdleFrom), and a halt doesn't stop that
// reclaim (stopping an idle adapter moves no work); a halt read from conf
// reaches a coding agent's turn at its next event or within about confTTL
// of a silent one (brakeSoon, brakeLook) and its pass (brake.go: cancelled
// when conf is known, parked with a look again while it isn't); and its
// sessions count in the usage totals (usage.go). Unpartitioned nothing here
// changes a thing.
package main

import (
	"context"
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

// harnessSendingSQL counts the prompts on their way to a coding agent whose
// adapter a successor attaches (harness_engine.go attachHarness settles each)
// — work that moves without the person. One left on an adapter that is gone
// isn't: resumeHarness ends its turn.
const harnessSendingSQL = `(SELECT count(*) FROM harness_sessions h JOIN runs r ON r.id=h.run_id
	WHERE r.engine='harness' AND h.prompt_state='sending' AND h.exec_id<>'' AND h.state IN ('starting','live','login'))`

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

// The rest flag: in a person's partition only a session at work keeps the
// backend up. Changed under s.mu (so a rest after a turn's end and the next
// prompt's work can't land in the wrong order: workMark), the hold
// re-read after it (holdMoved: never e.mu under s.mu — adopt takes s.mu
// under e.mu).

// restLocked (s.mu held) marks s resting or not; true when that changed.
func (s *hsess) restLocked(rest bool) bool { return s.rest.Swap(rest) != rest }

// holdMoved re-reads what the hold wants when s's part in it changed (a
// person's partition only; unpartitioned the hold is today's).
func (s *hsess) holdMoved(moved bool) {
	if moved && userMode() {
		s.e.updateHold()
	}
}

// workMark is how many times s went to work so far — taken before a commit
// whose poke may send the next prompt, so the rest that follows it doesn't
// land after that prompt's work (armIdleFrom).
func (s *hsess) workMark() *uint64 {
	s.mu.Lock()
	n := s.work
	s.mu.Unlock()
	return &n
}

// toWork: s is at work — a prompt sent (disarm: its idle reclaim off), a
// turn of the adapter's own followed, a park cleared with its turn going
// on. In a person's partition it keeps the backend up, and the halt is
// looked at while it works (brakeLook).
func (s *hsess) toWork(disarm bool) {
	s.mu.Lock()
	s.work++
	if disarm {
		s.disarmIdleLocked()
	}
	moved := s.restLocked(false)
	s.mu.Unlock()
	s.holdMoved(moved)
	if userMode() {
		s.e.brakeLook()
	}
}

// toRest: s waits on a person — a question, its sign-in. No reclaim (the
// caller disarmed it, or none was armed), and in a person's partition it
// doesn't keep the backend up: the person's answer brings it back.
func (s *hsess) toRest() {
	s.mu.Lock()
	moved := s.restLocked(true)
	s.mu.Unlock()
	s.holdMoved(moved)
}

// harnessHoldsLocked (e.mu held): a coding agent this process drives keeps
// the hold — any, unpartitioned and at the global instance; in a person's
// partition only one at work, or one AgTT is signing in (its authenticate
// awaits the adapter's answer here — at most hDeviceFor). A stopped
// partition's `wake` brings it back for a resting one's reclaim
// (resume_mode.go).
func (e *Engine) harnessHoldsLocked() bool {
	if !userMode() {
		return len(e.harness) > 0
	}
	for _, s := range e.harness {
		if !s.rest.Load() || s.signing.Load() {
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
// command) is looked at by brakeLook.
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

// hBrakeLookMin floors brakeLook's period (a test's confTTL of 0).
var hBrakeLookMin = 250 * time.Millisecond

// brakeLook arms, in a person's partition, the engine's one look at the
// halt while any coding agent works: every confTTL each working session
// looks (brakeSoon), so a turn that says nothing — a long build — is
// reached within about two confTTL of the halt, as PUT /halt reaches one
// unpartitioned. It re-arms while one works and holds nothing (a turn at
// work holds the partition already).
func (e *Engine) brakeLook() {
	if confIn == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hbrake != nil || e.closing {
		return
	}
	e.hbrake = time.AfterFunc(max(confTTL, hBrakeLookMin), func() {
		e.mu.Lock()
		e.hbrake = nil
		var working []*hsess
		if !e.closing {
			for _, s := range e.harness {
				if !s.rest.Load() {
					working = append(working, s)
				}
			}
		}
		e.mu.Unlock()
		for _, s := range working {
			s.brakeSoon()
		}
		if len(working) > 0 {
			e.brakeLook()
		}
	})
}

// harnessIdleUnderBrake is harnessPass under the brake in a person's
// partition, after onBrake: an idle coding agent's reclaim goes on — the
// adapter stopped at its idle time, and one a stopped partition left (the
// `wake` it left came back for it) taken over for that — since stopping an
// idle adapter moves no work. A turn, a park or a sign-in waits for the
// brake as ever.
func (e *Engine) harnessIdleUnderBrake(ctx context.Context, runID int64) {
	run, err := e.db.getRun(runID)
	if err != nil || run.Status == statusRunning || run.Status == statusWaiting {
		return
	}
	hs, _ := e.db.harnessSession(runID)
	if hs == nil || hs.PromptState != "" || hs.ExecID == "" || (hs.State != hsLive && hs.State != hsStarting) {
		return
	}
	switch s := e.harnessOf(runID); {
	case s != nil && s.reclaimDue():
		e.harnessReclaim(run, s, false)
	case s == nil:
		e.resumeHarness(ctx, run, hs)
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
