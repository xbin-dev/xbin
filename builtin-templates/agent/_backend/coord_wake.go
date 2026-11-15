// coord_wake.go — what reaches a coordinator, whatever its profile
// (coord_profile.go): the wake — an idle coordinator starts a turn when
// something it started changed state and its profile says that asks for
// one (Pending), at most once per coordWakeEvery, a timer poking it when
// the minute is up — and the updates: what changed, as one message at a
// step boundary (the profile's Deliver; deliverBoundary writes it beside
// the notices and has it marked delivered with the message).
package main

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

func init() { runDeletedHooks = append(runDeletedHooks, coordRunDeleted) }

// coordWakeEvery is how often a coordinator may be woken for its updates (a
// var so tests can shorten it).
var coordWakeEvery atomic.Int64

func init() { coordWakeEvery.Store(int64(60 * time.Second)) }

// coordWaker remembers each coordinator's last wake, and the timer that
// pokes it once its minute is up.
var coordWaker = struct {
	sync.Mutex
	last  map[int64]time.Time
	timer map[int64]*time.Timer
}{last: map[int64]time.Time{}, timer: map[int64]*time.Timer{}}

// coordWakes: run is an idle (or sleeping) coordinator and something it
// started asks to wake it (its profile's Pending) — at most one wake per
// coordWakeEvery; within it, a timer pokes it when the minute is up (the
// idle-wake checks in actor.go ask).
func coordWakes(d *DB, run *Run) bool {
	p := coordOriginOf(run)
	if p == nil || p.Pending == nil || !hooksOn(d, run.ID) {
		return false
	}
	return p.Pending(d, run) && coordWakeAdmit(run.ID)
}

// coordWakeCant: a coordinator in this status never takes a wake — one
// waiting for its person goes on when they answer, one in error when they
// write (the actor wakes neither).
func coordWakeCant(status string) bool { return status == statusWaiting || status == statusError }

// coordWakeAdmit says whether coordinator id may be woken now, and arms its
// timer when not.
func coordWakeAdmit(id int64) bool {
	every := time.Duration(coordWakeEvery.Load())
	now := time.Now()
	coordWaker.Lock()
	defer coordWaker.Unlock()
	if last, ok := coordWaker.last[id]; ok && now.Sub(last) < every {
		if coordWaker.timer[id] == nil {
			coordWaker.timer[id] = time.AfterFunc(last.Add(every).Sub(now), func() {
				coordWaker.Lock()
				delete(coordWaker.timer, id)
				coordWaker.Unlock()
				if e := projEng(); e != nil {
					e.Poke(id)
				}
			})
		}
		return false
	}
	coordWaker.last[id] = now
	return true
}

// coordForget drops a coordinator's wake state (its run deleted).
func coordForget(id int64) {
	coordWaker.Lock()
	if tm := coordWaker.timer[id]; tm != nil {
		tm.Stop()
	}
	delete(coordWaker.timer, id)
	delete(coordWaker.last, id)
	coordWaker.Unlock()
}

// coordRunDeleted (runDeletedHooks): a coordinator's conversation is gone,
// and its wake state with it.
func coordRunDeleted(_ *DB, runID int64) error {
	coordForget(runID)
	return nil
}

// deliverCoordUpdates writes a coordinator's undelivered updates as one
// message at a step boundary (deliverBoundary, beside the notices) and has
// them marked delivered with it. True when it wrote one.
func (e *Engine) deliverCoordUpdates(t *DB, ts *turnState) bool {
	run := ts.run
	p := coordOriginOf(run)
	if p == nil || p.Deliver == nil || !hooksOn(t, run.ID) {
		return false
	}
	u := p.Deliver(t, run)
	if u == nil || u.Text == "" || u.Mark == nil {
		return false
	}
	m := &Message{RunID: run.ID, Role: "user", Content: u.Text}
	m.Meta, _ = json.Marshal(u.Meta)
	if _, err := t.addMessage(m); err != nil {
		return false
	}
	u.Mark(m.ID)
	e.emitMessage(t, ts.root, m)
	return true
}
