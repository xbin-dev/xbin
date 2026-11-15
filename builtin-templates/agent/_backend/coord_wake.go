// coord_wake.go — what reaches a coordinator, whatever its profile
// (coord_profile.go): the wake — an idle coordinator starts a turn when
// something it started changed state and its profile says that asks for
// one (Pending) — and the updates: what changed, as one message at a step
// boundary (the profile's Deliver; deliverBoundary writes it beside the
// notices and has it marked delivered with the message).
//
// Wakes are coalesced, never held back for their own sake. What changed
// together is one wake: a wake delivers everything owed in one message,
// and what changes while the turn runs is read at its next step. The first
// change after a quiet time wakes at once. What is bounded is a storm: a
// coordinator takes at most its profile's Limits.Wakes wakes (one, when
// the profile names none) within any coordWakeEvery; the next one waits
// for the oldest of them to leave the window, a timer poking it then.
package main

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"
)

func init() { runDeletedHooks = append(runDeletedHooks, coordRunDeleted) }

// coordWakeEvery is the window a coordinator's wakes are counted in (a var
// so tests can shorten it).
var coordWakeEvery atomic.Int64

func init() { coordWakeEvery.Store(int64(60 * time.Second)) }

// coordWaker remembers each coordinator's wakes of the last window, oldest
// first, and the timer that pokes it once the oldest leaves it.
var coordWaker = struct {
	sync.Mutex
	recent map[int64][]time.Time
	timer  map[int64]*time.Timer
}{recent: map[int64][]time.Time{}, timer: map[int64]*time.Timer{}}

// coordWakes: run is an idle (or sleeping) coordinator and something it
// started asks to wake it (its profile's Pending) — at once, unless it was
// woken its profile's Limits.Wakes times within coordWakeEvery already;
// then a timer pokes it when the oldest of those leaves the window (the
// idle-wake checks in actor.go ask).
func coordWakes(d *DB, run *Run) bool {
	p := coordOriginOf(run)
	if p == nil || p.Pending == nil || !hooksOn(d, run.ID) {
		return false
	}
	return p.Pending(d, run) && coordWakeAdmit(run.ID, p.Limits.Wakes)
}

// coordWakeCant: a coordinator in this status never takes a wake — one
// waiting for its person goes on when they answer, one in error when they
// write (the actor wakes neither).
func coordWakeCant(status string) bool { return status == statusWaiting || status == statusError }

// coordWakeAdmit says whether coordinator id may be woken now — it was
// woken fewer than burst times (at least one) within the window — and
// arms its timer when not.
func coordWakeAdmit(id int64, burst int) bool {
	every := time.Duration(coordWakeEvery.Load())
	now := time.Now()
	coordWaker.Lock()
	defer coordWaker.Unlock()
	recent := coordWaker.recent[id]
	for len(recent) > 0 && now.Sub(recent[0]) >= every {
		recent = recent[1:]
	}
	if len(recent) >= max(burst, 1) {
		coordWaker.recent[id] = recent
		if coordWaker.timer[id] == nil {
			coordWaker.timer[id] = time.AfterFunc(recent[0].Add(every).Sub(now), func() {
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
	coordWaker.recent[id] = append(recent[:len(recent):len(recent)], now)
	return true
}

// coordForget drops a coordinator's wake state (its run deleted).
func coordForget(id int64) {
	coordWaker.Lock()
	if tm := coordWaker.timer[id]; tm != nil {
		tm.Stop()
	}
	delete(coordWaker.timer, id)
	delete(coordWaker.recent, id)
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
