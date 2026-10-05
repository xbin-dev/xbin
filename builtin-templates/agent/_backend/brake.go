// brake.go — the halt switch in a person's partition (conf.go; API.md
// "Partitioned instances"). Unpartitioned and at the global instance the
// brake is today's: PUT /halt cancels every live run, a run's pass does
// nothing while it is on, and taking it off (PUT /halt, or a manager's
// message) recovers the runs that wait.
//
// A person's partition reads the switch from conf, which the global
// instance writes — a halt set there (by the owner, or a manager in another
// partition) reaches this process only when it reads conf:
//
//   - A run's next step (or pass) under a halt conf says is on cancels it,
//     as PUT /halt would have (the reason says a manager paused the agent) —
//     within a step and confTTL of the halt. A coding agent's run the same,
//     at its pass (harness_pass.go), its turn's next event (brakeSoon,
//     harness_partition.go) or — a turn that says nothing — the engine's
//     look while one works (brakeLook). Nothing stays running, so a stopped
//     partition asks for no restart while the brake is on (resume_mode.go),
//     but for an idle coding agent's reclaim, which moves no work.
//   - Until conf has been read once (not written yet, a kv error at start)
//     the brake reads as on but nothing is cancelled: the run is parked, a
//     request for work is taken and queued, and the partition looks at conf
//     again — a timer with backoff, armed only while runs are parked on the
//     brake — and recovers them once it is off.
//   - Without conf in xbin.json's uses nothing can be read, ever: requests
//     for work answer 503 saying so, and runs stay parked.
package main

import (
	"net/http"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const haltReason = "halted: a manager paused the agent"

// onBrake is pass()'s halt branch for run (the pass returns after it).
func (e *Engine) onBrake(run *Run) {
	if !userMode() || confIn() == nil {
		return // today's rule: the run waits for the brake to come off
	}
	if v := confIn().view(true); v.State == confKnown && v.Halt == "1" && active(run.Status) && e.ag != nil {
		_ = e.fenced(func(t *DB) error {
			e.ag.cancelRuns(t, run.ID, false, haltReason)
			return nil
		})
		return
	}
	confIn().watchBrake()
}

// brakeInTurn is the brake between two steps of a turn: unpartitioned and
// at the global instance PUT /halt cancels a turn at once, so only a person's
// partition looks (conf, cached for confTTL) — true: the turn ends here.
func (e *Engine) brakeInTurn(run *Run) bool {
	if !userMode() || confIn() == nil || !e.halted() {
		return false
	}
	e.onBrake(run)
	return true
}

// brakeWatchMin/Max bound the gap between two looks at conf while runs are
// parked on the brake.
var (
	brakeWatchMin = 3 * time.Second
	brakeWatchMax = time.Minute
)

// watchBrake arms the look at conf (once; it re-arms itself with backoff
// while the brake stays on and runs are parked).
func (c *confReader) watchBrake() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.watch != nil || c.missing {
		return
	}
	if c.watchGap == 0 {
		c.watchGap = brakeWatchMin
	}
	c.watch = time.AfterFunc(c.watchGap, c.watchTick)
}

func (c *confReader) watchTick() {
	c.mu.Lock()
	c.watch, c.at = nil, time.Time{}
	c.mu.Unlock()
	c.refresh() // the brake off: onHaltOff recovers the parked runs
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.lastHalt != "1" || c.parked == nil || !c.parked() {
		c.watchGap = 0
		return
	}
	if c.watch == nil {
		c.watchGap = min(c.watchGap*2, brakeWatchMax)
		c.watch = time.AfterFunc(c.watchGap, c.watchTick)
	}
}

// brakeParked: d has runs a pass would move but for the brake (recover()'s
// set).
func (d *DB) brakeParked() bool {
	var n int
	_ = d.q.QueryRow(`SELECT
		(SELECT count(*) FROM runs WHERE status IN ('running','queued','blocked','awaiting','sleeping'))
		+ (SELECT count(*) FROM inbox WHERE delivered_at=0)
		+ (SELECT count(*) FROM links WHERE state<>'running' AND delivered=0)`).Scan(&n)
	return n > 0
}

// confBrakeBlocks is haltBlocks' answer while a person's partition doesn't
// know conf: a request for work waits in the queue until conf is read
// (stop=false), or — conf not in uses — is refused, saying why (stop=true).
// handled=false: conf is known, the brake is the manager's (haltBlocks).
func confBrakeBlocks(w http.ResponseWriter) (stop, handled bool) {
	if !userMode() || confIn() == nil {
		return false, false
	}
	switch confIn().view(true).State {
	case confPending:
		return false, true
	case confMissing:
		xbin.WriteError(w, http.StatusServiceUnavailable, "this copy of the agent can't read its shared settings (the conf resource isn't in its xbin.json's uses): "+
			"update it from its template, or ask a manager to")
		return true, true
	}
	return false, false
}

// brakeIdle: in a person's partition, nothing moves while the brake is on
// by a manager's hand (or for good: conf not in uses) — leave no wake-up.
// The cached read: this runs at shutdown.
func brakeIdle() bool {
	if !userMode() || confIn() == nil {
		return false
	}
	v := confIn().view(false)
	return v.State == confMissing || v.State == confKnown && v.Halt == "1"
}
