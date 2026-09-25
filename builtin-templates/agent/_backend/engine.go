// engine.go — what moves runs forward.
//
// One in-process ACTOR per run that has work: a goroutine that loops "pass"
// (actor.go) until the run parks, then exits. Everything that could give a
// run work — an HTTP input written to the inbox, a subagent settling, a timer
// — commits its row and then calls Poke. There is no dispatcher query on a
// clock and no ticker anywhere: a run that needs nothing costs nothing, and a
// run that needs something is started by the event that made it so.
//
// Ownership. Exactly one process drives runs: the one holding an exclusive
// flock on "<db>.engine" (owner.go). A blue/green swap boots the new process
// while the old still serves; the new one answers HTTP at once (handlers only
// write inbox rows) and its engine blocks on the lock — blocking, not polling —
// until the old process exits, then takes over within milliseconds. A crash
// hands over the same way: the kernel drops the lock with the process. Every
// actor transaction also checks the engine epoch the owner bumped at takeover,
// so even a lock that does not work cannot let two engines write.
package main

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"
)

// Cancellation causes. A step cancelled for a handoff writes nothing — the
// successor re-issues it; interrupt and cancel are recorded by the pass that
// consumes their inbox row.
var (
	errHandoff   = errors.New("handoff: the backend is being replaced")
	errFenced    = errors.New("another engine owns this database")
	errInterrupt = errors.New("interrupted by the owner")
	errCancel    = errors.New("cancelled")
)

type actor struct {
	id    int64
	dirty bool
	// cancel aborts the step in flight (a model call, a tool batch).
	cancel context.CancelCauseFunc
}

type Engine struct {
	db  *DB
	ag  *Agent
	llm LLM

	gate *llmGate
	hub  *eventHub
	gen  string // this process: event cursors, the row marker old binaries respect

	lockPath string
	lockFile *os.File

	base       context.Context
	cancelBase context.CancelCauseFunc
	closingCh  chan struct{}

	mu       sync.Mutex
	owned    bool
	closing  bool
	epoch    int64
	actors   map[int64]*actor
	timers   map[int64]*time.Timer
	delivery map[int64][]chan struct{} // inbox id → closed when consumed
	drafts   map[int64]*draft
	idleCh   chan struct{} // closed when the last actor exits during shutdown

	legacyTimer *time.Timer
	hold        holder

	// Test seams.
	now      func() time.Time
	onTakeup func() // called after takeover (tests)
}

func newEngine(db *DB, ag *Agent, llm LLM, lockPath string) *Engine {
	base, cancel := context.WithCancelCause(context.Background())
	e := &Engine{
		db: db, ag: ag, llm: llm, lockPath: lockPath,
		gen:  generationID(),
		base: base, cancelBase: cancel, closingCh: make(chan struct{}),
		actors: map[int64]*actor{}, timers: map[int64]*time.Timer{},
		delivery: map[int64][]chan struct{}{}, drafts: map[int64]*draft{},
		now: time.Now,
	}
	e.gate = newLLMGate(parseConfig(db.getSetting("config")).maxActiveRuns())
	e.hub = newEventHub(e.gen)
	if ag != nil {
		ag.eng = e
	}
	return e
}

func (e *Engine) unix() int64 { return e.now().Unix() }

// Start acquires ownership in the background and takes over when it has it.
func (e *Engine) Start() {
	go func() {
		if err := e.acquireLock(); err != nil {
			logf("engine lock unavailable (%v) — relying on the epoch fence alone", err)
		}
		e.takeOver()
	}()
}

// takeOver makes this process the owner: bump the epoch (fencing any other
// engine), then find every run with something to do. Recovery is one pass,
// not a loop: after this, only events move runs.
func (e *Engine) takeOver() {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()
	var epoch int64
	err := e.db.Tx(func(t *DB) error {
		_ = t.q.QueryRow(`SELECT CAST(v AS INTEGER) FROM settings WHERE k='engine_epoch'`).Scan(&epoch)
		epoch++
		return t.putSetting("engine_epoch", itoa(epoch))
	})
	if err != nil {
		logf("engine takeover failed: %v", err)
		return
	}
	e.mu.Lock()
	e.owned, e.epoch = true, epoch
	e.mu.Unlock()
	if e.ag != nil {
		go e.ag.clearWakeJobs()
	}
	e.recover()
	if e.onTakeup != nil {
		e.onTakeup()
	}
}

// recover pokes every run that may have something to do. The pass decides;
// a run that turns out to need nothing (a future wake) arms its timer and
// its actor exits at once.
func (e *Engine) recover() {
	e.mu.Lock()
	owned, closing := e.owned, e.closing
	e.mu.Unlock()
	if !owned || closing {
		return
	}
	// Runs an old (pre-engine) process still drives: leave them until its
	// leases lapse, then look once more (one timer, first upgrade only).
	busy, until := e.db.legacyLeaseLive(e.leaseMark())
	skip := map[int64]bool{}
	for _, id := range busy {
		skip[id] = true
	}
	if until > 0 {
		d := time.Until(time.Unix(until+1, 0))
		e.mu.Lock()
		if e.legacyTimer == nil && !e.closing {
			e.legacyTimer = time.AfterFunc(d, func() {
				e.mu.Lock()
				e.legacyTimer = nil
				e.mu.Unlock()
				e.db.adoptLegacy(false)
				e.recover()
			})
		}
		e.mu.Unlock()
	}
	ids := scanIDs(e.db.q.Query(`
		SELECT id FROM runs WHERE status IN ('running','queued','blocked','awaiting','sleeping')
		UNION SELECT DISTINCT run_id FROM inbox WHERE delivered_at=0
		UNION SELECT DISTINCT parent_id FROM links WHERE state<>'running' AND delivered=0`))
	for _, id := range ids {
		if !skip[id] {
			e.Poke(id)
		}
	}
}

// Poke asks for a pass over a run. Cheap and idempotent; call it AFTER the
// write that gave the run work has committed (DB.AfterCommit).
func (e *Engine) Poke(id int64) {
	if id == 0 {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing || !e.owned {
		return // the owner (this process later, or the successor) recovers it
	}
	if a := e.actors[id]; a != nil {
		a.dirty = true
		return
	}
	a := &actor{id: id, dirty: true}
	e.actors[id] = a
	e.updateHoldLocked()
	go e.runActor(a)
}

// runActor loops passes while the run keeps getting work. The exit check and
// the map delete happen under the same lock Poke takes, so a poke can never
// fall between "nothing to do" and "gone" and be lost.
func (e *Engine) runActor(a *actor) {
	defer func() {
		if r := recover(); r != nil {
			logf("run #%d: engine panic: %v", a.id, r)
			e.mu.Lock()
			delete(e.actors, a.id)
			e.afterActorExitLocked()
			e.mu.Unlock()
		}
	}()
	e.markDriving(a.id)
	for {
		e.mu.Lock()
		if !a.dirty || e.closing {
			delete(e.actors, a.id)
			e.afterActorExitLocked()
			e.mu.Unlock()
			return
		}
		a.dirty = false
		e.mu.Unlock()
		e.pass(a)
	}
}

func (e *Engine) afterActorExitLocked() {
	if e.closing && len(e.actors) == 0 && e.idleCh != nil {
		close(e.idleCh)
		e.idleCh = nil
	}
	e.updateHoldLocked()
}

func (e *Engine) isClosing() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closing
}

// setStepCancel records (or clears) the cancel func of the step in flight.
func (e *Engine) setStepCancel(id int64, c context.CancelCauseFunc) {
	e.mu.Lock()
	if a := e.actors[id]; a != nil {
		a.cancel = c
	}
	e.mu.Unlock()
}

// Signal aborts a run's step in flight (interrupt, cancel). The row that says
// why is already committed; the pass that follows consumes it.
func (e *Engine) Signal(id int64, cause error) {
	e.mu.Lock()
	var c context.CancelCauseFunc
	if a := e.actors[id]; a != nil {
		c = a.cancel
		a.dirty = true
	}
	e.mu.Unlock()
	if c != nil {
		c(cause)
	}
	e.Poke(id)
}

// fenced runs fn in one transaction that first proves this engine still owns
// the database. With _txlock=immediate the check and the writes are atomic
// against any other process.
func (e *Engine) fenced(fn func(t *DB) error) error {
	return e.db.Tx(func(t *DB) error {
		var ep int64
		_ = t.q.QueryRow(`SELECT CAST(v AS INTEGER) FROM settings WHERE k='engine_epoch'`).Scan(&ep)
		e.mu.Lock()
		mine := e.epoch
		e.mu.Unlock()
		if ep != mine {
			e.lostOwnership()
			return errFenced
		}
		return fn(t)
	})
}

// lostOwnership stops this engine: another one bumped the epoch, so the lock
// did not keep us apart and nothing we write can be trusted any more.
func (e *Engine) lostOwnership() {
	e.mu.Lock()
	if e.closing || !e.owned {
		e.mu.Unlock()
		return
	}
	e.owned = false
	e.mu.Unlock()
	logf("another engine took over this database — this one stops driving runs")
	e.cancelBase(errFenced)
}

// --- timers ---------------------------------------------------------------

// armTimer pokes a run at a unix time (a yield's wake, the earliest subagent
// deadline). One timer per run; re-arming replaces it. A time already past
// pokes at once.
func (e *Engine) armTimer(id int64, at int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing {
		return
	}
	if t := e.timers[id]; t != nil {
		t.Stop()
	}
	d := time.Until(time.Unix(at, 0))
	if at <= e.unix() {
		d = 0
	}
	e.timers[id] = time.AfterFunc(d, func() {
		e.mu.Lock()
		delete(e.timers, id)
		e.updateHoldLocked()
		e.mu.Unlock()
		e.Poke(id)
	})
	e.updateHoldLocked()
}

func (e *Engine) disarmTimer(id int64) {
	e.mu.Lock()
	if t := e.timers[id]; t != nil {
		t.Stop()
		delete(e.timers, id)
		e.updateHoldLocked()
	}
	e.mu.Unlock()
}

// --- inbox delivery waiters (compact) ------------------------------------

// waitDelivered blocks until an inbox row this process queued is consumed, or
// the timeout. Event-driven: the consuming pass closes the channel.
func (e *Engine) waitDelivered(inboxID int64, timeout time.Duration) bool {
	ch := make(chan struct{})
	e.mu.Lock()
	e.delivery[inboxID] = append(e.delivery[inboxID], ch)
	e.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
		return true
	case <-t.C:
		return false
	case <-e.closingCh:
		return false
	}
}

func (e *Engine) delivered(inboxID int64) {
	e.mu.Lock()
	chs := e.delivery[inboxID]
	delete(e.delivery, inboxID)
	e.mu.Unlock()
	for _, c := range chs {
		close(c)
	}
}

// --- shutdown ---------------------------------------------------------------

// BeginShutdown stops everything at once: no new passes, every step in
// flight cancelled with cause handoff (so nothing is written for it), the
// live streams told to reconnect. Idempotent.
func (e *Engine) BeginShutdown() {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	e.closing = true
	close(e.closingCh)
	for id, t := range e.timers {
		t.Stop()
		delete(e.timers, id)
	}
	if e.legacyTimer != nil {
		e.legacyTimer.Stop()
	}
	if len(e.actors) > 0 {
		e.idleCh = make(chan struct{})
	}
	e.mu.Unlock()
	e.cancelBase(errHandoff)
	e.hub.closeAll()
	e.hold.stop()
}

// Shutdown is BeginShutdown plus: wait (bounded) for the actors to unwind,
// leave a wake-up behind if work is pending with no successor to take it,
// and drop the lock so a successor takes over the moment we exit.
func (e *Engine) Shutdown(wait time.Duration) {
	e.BeginShutdown()
	e.mu.Lock()
	idle := e.idleCh
	owned := e.owned
	e.mu.Unlock()
	if idle != nil {
		t := time.NewTimer(wait)
		select {
		case <-idle:
		case <-t.C:
		}
		t.Stop()
	}
	if owned && e.ag != nil && e.db.hasWork() {
		e.ag.registerResumeJob()
	}
	e.releaseLock()
}

// hasWork reports runs that need the engine without a human doing anything.
func (d *DB) hasWork() bool {
	var n int
	_ = d.q.QueryRow(`SELECT
		(SELECT count(*) FROM runs WHERE status IN ('running','queued','blocked','awaiting','sleeping'))
		+ (SELECT count(*) FROM inbox WHERE delivered_at=0)`).Scan(&n)
	return n > 0
}

// --- the row marker old binaries respect -----------------------------------

// leaseMark is written into runs.lease_owner when an actor starts on a run.
// The engine does not use leases; a PRE-engine binary sharing the file during
// the first upgrade does, and a live-looking lease is what stops it driving a
// run this engine owns. One write per actor, never renewed, never cleared (it
// lapses; engines ignore "engine:" marks).
func (e *Engine) leaseMark() string { return "engine:" + e.gen }

func (e *Engine) markDriving(id int64) {
	_, _ = e.db.q.Exec(`UPDATE runs SET lease_owner=?, lease_until=? WHERE id=?`, e.leaseMark(), e.unix()+3600, id)
}
