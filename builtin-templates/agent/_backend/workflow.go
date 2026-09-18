// workflow.go — the run graph's scheduler.
//
// Governing principle: **in-RAM is latency; sqlite + /tick is correctness.**
// The kick channel below is an optimization whose loss costs only latency —
// every dispatch it would have made is reachable from a cold process running
// `readyRuns` once. That is what lets this survive a save, a blue/green swap
// and the ~30-minute idle reap without any recovery logic of its own.
//
// There is exactly ONE place where database state becomes a running drive
// (dispatchOnce), reached from three entry points: boot (startupResume), the
// cron heartbeat (/tick), and an in-process kick. They all run the same query.
package main

import (
	"context"
	"fmt"
	"log"
	"time"
)

const (
	// leaseTTL bounds how long a dead process can keep a run to itself. claim()
	// is a RAM map and therefore per-process, but a blue/green swap briefly runs
	// two generations against the same sqlite file — without a durable lease the
	// new one re-drives runs the old one is still driving.
	// Matches the documented blue/green drain. It used to be 90s, which meant a
	// process killed by a save left a lease that hid its run from the dispatcher
	// for a minute and a half — the tile showed "thinking" the whole time.
	leaseTTL = 30 * time.Second
	// kickDebounce coalesces a burst of settles into roughly one dispatch pass.
	kickDebounce = 50 * time.Millisecond
	// dispatchBatch bounds one pass. handleTick used to drive EVERY due run at
	// once, which with real fan-out is a thundering herd.
	dispatchBatch = 32
	// maxToolsGlobal bounds tool goroutines across every concurrent drive.
	maxToolsGlobal = 12
)

// --- lease ---------------------------------------------------------------

// claimLease is claim() made durable across process generations: a conditional
// UPDATE that succeeds only when the run is unowned, already ours, or the
// previous owner's lease has expired.
func (d *DB) claimLease(runID int64, owner string, ttl time.Duration) (bool, error) {
	res, err := d.sql.Exec(
		`UPDATE runs SET lease_owner=?, lease_until=? WHERE id=? AND (lease_owner='' OR lease_owner=? OR lease_until<=?)`,
		owner, now()+int64(ttl.Seconds()), runID, owner, now())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// renewLease extends our hold. Called on the per-iteration write the loop
// already performs, so it costs nothing extra.
func (d *DB) renewLease(runID int64, owner string, ttl time.Duration) {
	_, _ = d.sql.Exec(`UPDATE runs SET lease_until=? WHERE id=? AND lease_owner=?`,
		now()+int64(ttl.Seconds()), runID, owner)
}

// leaseHeld reports whether some live drive currently owns this run. Used to
// decide whether a cancel can be delegated to that drive or has to settle the
// run itself.
func (d *DB) leaseHeld(runID int64) bool {
	var until int64
	if d.sql.QueryRow(`SELECT lease_until FROM runs WHERE id=?`, runID).Scan(&until) != nil {
		return false
	}
	return until > now()
}

func (d *DB) releaseLease(runID int64, owner string) {
	_, _ = d.sql.Exec(`UPDATE runs SET lease_owner='', lease_until=0 WHERE id=? AND lease_owner=?`, runID, owner)
}

// --- readiness -----------------------------------------------------------

// readyRuns is the single predicate that decides what may run, in priority
// order. It replaces dueRuns, and the two clauses that matter are the orphan
// guard and the ordering:
//
//   - `NOT (parent_id<>0 AND detached=0)` makes a non-detached child
//     structurally unreachable by the heartbeat. A child that is not detached
//     exists only inside its parent's tool call, so resurrecting it runs work
//     nobody is waiting for — which is precisely how a subagent that yielded or
//     ran out of iterations used to escape and keep spending on its own.
//
//   - `depth DESC` drains leaves first so their parents can unblock;
//     breadth-first would fill the ceiling with runs that cannot finish.
func (d *DB) readyRuns(limit int) ([]int64, error) {
	rows, err := d.sql.Query(`
WITH cand(id, prio) AS (
  -- 0: has a settled dependency whose result has not been handed over yet.
  --    Short work that unblocks a whole subtree, so it goes first.
  -- NOT 'blocked': a blocked run is parked on ALL of its dependencies, and
  -- branch 1 admits it once the last one settles. Waking it here on the first
  -- would burn a dispatch, a claim and a lease on a drive that just returns.
  SELECT r.id, 0 FROM runs r
   WHERE r.status IN ('idle','sleeping','queued')
     AND EXISTS (SELECT 1 FROM run_deps d WHERE d.run_id=r.id AND d.delivered=0 AND d.state<>'pending')
  UNION ALL
  -- 1: blocked, and every await dependency has settled.
  SELECT r.id, 1 FROM runs r
   WHERE r.status='blocked'
     AND NOT EXISTS (SELECT 1 FROM run_deps d WHERE d.run_id=r.id AND d.kind='await' AND d.state='pending')
  UNION ALL
  SELECT id, 2 FROM runs WHERE status='queued'
  UNION ALL
  SELECT id, 3 FROM runs WHERE status='sleeping' AND wake_at<=?
  UNION ALL
  -- 4: left 'running' by a dead process, and its lease has expired.
  SELECT id, 4 FROM runs WHERE status='running' AND lease_until<=?
)
SELECT c.id FROM cand c JOIN runs r ON r.id=c.id
 WHERE r.cancel_req=0 AND r.settled_at=0
   AND NOT (r.parent_id<>0 AND r.detached=0)
 GROUP BY c.id
 ORDER BY MIN(c.prio), r.depth DESC, c.id
 LIMIT ?`, now(), now(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- spawn budget --------------------------------------------------------

// ensureTree creates a root's budget row, snapshotting the limits in force when
// the workflow started so changing the default later cannot move the goalposts
// under a tree that is already running.
func (d *DB) ensureTree(rootID int64, maxSpawn, maxDepth int) {
	_, _ = d.sql.Exec(
		`INSERT OR IGNORE INTO run_trees (root_id, spawned, max_spawn, max_depth, created) VALUES (?, 0, ?, ?, ?)`,
		rootID, maxSpawn, maxDepth, now())
}

// reserveSpawn takes one unit of a tree's lifetime budget, or reports that it
// is spent. A single-statement compare-and-swap: a count(*) over runs would be
// a gauge (it drops when a run is deleted) and would not be atomic with the
// child INSERT, so two parallel spawns could both pass the check.
func (d *DB) reserveSpawn(rootID int64, fallbackMax int) (ok bool, spawned, max int) {
	d.ensureTree(rootID, fallbackMax, 0)
	res, err := d.sql.Exec(
		`UPDATE run_trees SET spawned = spawned + 1 WHERE root_id=? AND spawned < max_spawn`, rootID)
	_ = d.sql.QueryRow(`SELECT spawned, max_spawn FROM run_trees WHERE root_id=?`, rootID).Scan(&spawned, &max)
	if err != nil {
		return false, spawned, max
	}
	n, _ := res.RowsAffected()
	return n == 1, spawned, max
}

// releaseSpawn hands a reservation back when the child could not be created.
// The counter is otherwise monotonic on purpose.
func (d *DB) releaseSpawn(rootID int64) {
	_, _ = d.sql.Exec(`UPDATE run_trees SET spawned = spawned - 1 WHERE root_id=? AND spawned > 0`, rootID)
}

// --- concurrency ceiling -------------------------------------------------

// tryAcquire takes a drive slot, or reports that the process is at capacity.
// Non-blocking on purpose: back-pressure is expressed as durable state (a run
// parked `queued`), never as a blocked goroutine — a goroutine waiting for a
// slot while holding one is how a spawn-and-wait design deadlocks.
func (ag *Agent) tryAcquire() bool {
	ag.slotMu.Lock()
	defer ag.slotMu.Unlock()
	if ag.limit <= 0 {
		ag.limit = defaultMaxActiveRuns
	}
	if ag.active >= ag.limit {
		return false
	}
	ag.active++
	return true
}

func (ag *Agent) releaseSlot() {
	ag.slotMu.Lock()
	if ag.active > 0 {
		ag.active--
	}
	ag.slotMu.Unlock()
	ag.kick() // a freed slot may admit a queued run
}

// setLimit resizes the ceiling live. A mutex+counter rather than a channel
// semaphore precisely so this is possible without rebuilding anything.
func (ag *Agent) setLimit(n int) {
	ag.slotMu.Lock()
	ag.limit = clampCfg(n, defaultMaxActiveRuns, 32)
	ag.slotMu.Unlock()
	ag.kick()
}

func (ag *Agent) activeDrives() (active, limit int) {
	ag.slotMu.Lock()
	defer ag.slotMu.Unlock()
	if ag.limit <= 0 {
		ag.limit = defaultMaxActiveRuns
	}
	return ag.active, ag.limit
}

// --- asking for a drive --------------------------------------------------

// parkQueued moves a run into the retryable `queued` state, preserving the
// fields a park must not invent: wake_at (a sleeping run has a time it means
// to wake) and pending (a parked approval's tool calls). It refuses to touch a
// terminal run, a running one, or one that is blocked on dependencies — those
// already have a dispatcher branch of their own.
func (ag *Agent) parkQueued(runID int64) {
	r, err := ag.db.getRun(runID)
	if err != nil || terminalStatus(r.Status) {
		return
	}
	switch r.Status {
	case statusRunning, statusQueued, statusBlocked, statusWaiting:
		// running/queued: already dispatchable. blocked: has its own branch and
		// must keep it. waiting_input: a human owes an answer, and queueing it
		// would drive the run with their question still unanswered.
		return
	}
	_, _ = ag.db.sql.Exec(`UPDATE runs SET status=?, updated=? WHERE id=?`, statusQueued, now(), runID)
	// Asynchronously: registering the heartbeat is a gateway round-trip that
	// retries over several seconds, and this is called from the dispatch path —
	// including from inside a drive slot.
	go ag.reconcileBeat()
}

// resumeIfHalted clears the global brake when a human explicitly asks for work.
// A prompt is an unambiguous "go", and a halt that silently swallows prompts is
// indistinguishable from a broken agent.
func (ag *Agent) resumeIfHalted(runID int64) {
	if !ag.halted() {
		return
	}
	_ = ag.db.putSetting("halt", "")
	if runID != 0 {
		ag.db.journal(runID, "note", map[string]string{
			"text": "halt cleared: you sent a message, which resumes the agent"})
	}
	ag.kick()
}

// --- dispatch ------------------------------------------------------------

// dispatchOnce admits every ready run it can. Idempotent and cheap: the
// predicate is durable state, never an edge, so calling it redundantly is free
// and missing a call costs only latency.
func (ag *Agent) dispatchOnce() {
	ids, err := ag.db.readyRuns(dispatchBatch)
	if err != nil {
		return
	}
	for _, id := range ids {
		ag.dispatchRun(id)
	}
}

// dispatchRun admits one run: claim FIRST, then take a slot. Claiming is free
// and rejects a duplicate immediately, whereas admitting first would let that
// duplicate burn a slot it is about to give back.
//
// At the ceiling the run is parked `queued` — durable, ordered, and visible —
// rather than slept: reusing wake_at for throttling is exactly what made an
// over-budget subagent indistinguishable from one that chose to wait.
func (ag *Agent) dispatchRun(runID int64) {
	// The brake is checked at admission, so a halt stops the tree from starting
	// anything new even while its already-running drives wind down.
	if ag.halted() {
		return
	}
	if !ag.claim(runID) {
		return
	}
	if !ag.tryAcquire() {
		ag.release(runID)
		// parkQueued preserves wake_at and pending and leaves blocked/waiting
		// runs alone — the old inline write zeroed a sleeping run's wake time
		// and could demote a run parked on the human to `queued`, which then
		// drove with their question unanswered. It also registers the beat,
		// which this branch previously skipped: the one path that most needs
		// the heartbeat was the one that never asked for it.
		ag.parkQueued(runID)
		return
	}
	// ONE claim for the whole admission. Releasing here and re-claiming inside
	// drive left a window where a second dispatch could claim the same run,
	// take a second slot, and — on losing the race — run its deferred
	// unregisterCancel over the WINNER's entry, disarming abort() for the rest
	// of that drive.
	go func() {
		defer ag.releaseSlot()
		defer ag.release(runID)
		ctx, cancel := context.WithTimeout(context.Background(), driveTimeout)
		defer cancel()
		tok := ag.registerCancel(runID, cancel)
		defer ag.unregisterCancel(runID, tok)
		ag.driveClaimed(ctx, runID)
		// Off the slot: reconcileBeat makes a retrying gateway call with no
		// client timeout, so running it inline held a drive slot for seconds
		// after every drive and indefinitely if the gateway hung — the one way
		// the concurrency ceiling could drift shut and stay shut.
		go ag.reconcileBeat()
	}()
}

func terminalStatus(s string) bool {
	switch s {
	case statusDone, statusError, statusCanceled:
		return true
	}
	return false
}

// kick asks for a dispatch pass without blocking the caller. A 1-deep buffered
// channel drained by one goroutine, so N simultaneous completions collapse into
// roughly one pass rather than N.
func (ag *Agent) kick() {
	select {
	case ag.kickCh <- struct{}{}:
	default: // a pass is already pending; it will see our state too
	}
}

func (ag *Agent) kickLoop() {
	for range ag.kickCh {
		time.Sleep(kickDebounce)
		ag.dispatchOnce()
	}
}

// recoverStrandedRuns un-sticks rows that no dispatcher can reach, at boot and
// on every heartbeat. Two shapes exist, both created by bugs this file has
// since fixed, and neither self-heals because readyRuns cannot see them:
//
//   - cancel_req set but never settled: requestCancel used to delegate the
//     settle to a live drive even when none existed, and readyRuns filters
//     cancel_req=0, so the row sat at `running` forever with its waiters
//     blocked behind it.
//   - a run left `idle` by a dispatch that was declined before the durable
//     park existed. `idle` means "waiting for a human", so nothing selects it —
//     but a trailing user message means someone IS waiting for a reply.
//
// Both checks require no live lease, so a run a drive currently owns is never
// touched.
func (ag *Agent) recoverStrandedRuns() {
	rows, err := ag.db.sql.Query(
		`SELECT id FROM runs WHERE cancel_req<>0 AND settled_at=0 AND lease_until<=?`, now())
	if err == nil {
		var ids []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
		for _, id := range ids {
			_ = ag.db.setStatus(id, statusCanceled, 0, "cancelled", "")
			ag.settle(id, outcomeCanceled, "cancelled")
		}
		if len(ids) > 0 {
			log.Printf("agent: settled %d cancelled run(s) that no drive had finished", len(ids))
		}
	}

	// An idle run whose last message is from the user was asked a question that
	// was never answered — the signature of a dispatch dropped before the park.
	rows, err = ag.db.sql.Query(`
		SELECT r.id FROM runs r
		 WHERE r.status='idle' AND r.settled_at=0 AND r.cancel_req=0 AND r.lease_until<=?
		   AND (SELECT m.role FROM messages m WHERE m.run_id=r.id ORDER BY m.seq DESC LIMIT 1) = 'user'`, now())
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		ag.parkQueued(id)
	}
	if len(ids) > 0 {
		log.Printf("agent: requeued %d run(s) left waiting on a dropped dispatch", len(ids))
		ag.kick()
	}
}

// generationID labels this process for the lease. Runs claimed by an older
// generation stay untouched until their lease expires.
func generationID() string {
	return fmt.Sprintf("%d-%d", now(), time.Now().UnixNano()%100000)
}

// --- halt ----------------------------------------------------------------

// halted reports the owner's global brake. Read on every admission, so a
// runaway fan-out stops starting new work the moment the button is pressed —
// and stays stopped across the restart it may well have caused.
func (ag *Agent) halted() bool { return ag.db.getSetting("halt") == "1" }

// liveRunIDs lists everything currently running or about to.
func (d *DB) liveRunIDs() []int64 {
	rows, err := d.sql.Query(
		`SELECT id FROM runs WHERE status IN ('running','queued','blocked','sleeping') AND settled_at=0`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// blockersByRun maps each run in a tree to the runs it is waiting on — one
// query for the whole tree rather than one per node.
func (d *DB) blockersByRun(rootID int64) map[int64][]int64 {
	out := map[int64][]int64{}
	rows, err := d.sql.Query(
		`SELECT d.run_id, d.dep_id FROM run_deps d JOIN runs r ON r.id = d.run_id
		 WHERE r.root_id=? AND d.kind='await' AND d.state='pending'`, rootID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var w, dep int64
		if rows.Scan(&w, &dep) == nil {
			out[w] = append(out[w], dep)
		}
	}
	return out
}

// lastStepByRun returns each node's most recent journal line — "what is it
// doing right now". One query for the tree, the lastAssistantByRun pattern.
func (d *DB) lastStepByRun(rootID int64) map[int64]string {
	out := map[int64]string{}
	rows, err := d.sql.Query(`
		SELECT s.run_id, s.kind, s.detail FROM steps s
		 WHERE s.id IN (SELECT MAX(id) FROM steps
		                 WHERE run_id IN (SELECT id FROM runs WHERE root_id=?) GROUP BY run_id)`, rootID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, detail string
		if rows.Scan(&id, &kind, &detail) == nil {
			out[id] = kind + " " + clip(detail, 90)
		}
	}
	return out
}
