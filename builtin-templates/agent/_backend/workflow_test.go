package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func ids(t *testing.T, db *DB) []int64 {
	t.Helper()
	got, err := db.readyRuns(dispatchBatch)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func has(list []int64, id int64) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

// TestOrphanChildIsNeverResurrected is the whole point of the orphan guard, and
// the fix for the one thing that genuinely ran in the background before this
// work: a subagent that called yield or ran out of iterations was parked
// sleeping with wake_at=now(), which made it immediately due — so the heartbeat
// picked it up and kept driving it, detached, with no parent listening and no
// consumer for its output. It could even create its own cron-agents.
func TestOrphanChildIsNeverResurrected(t *testing.T) {
	db := newTestDB(t)
	parent, _ := db.createRun("parent", "", 0)
	child, _ := db.createRun("child", "", parent)

	// Exactly the state the old code left behind.
	if err := db.setStatus(child, statusSleep, now(), "", ""); err != nil {
		t.Fatal(err)
	}
	if has(ids(t, db), child) {
		t.Fatal("a non-detached child must never be dispatched — nothing is waiting for its output")
	}

	// The same row, left 'running' by a crash, is equally unreachable.
	_ = db.setStatus(child, statusRunning, 0, "", "")
	if has(ids(t, db), child) {
		t.Fatal("a non-detached child left 'running' must not be resurrected either")
	}

	// But once it is deliberately detached, it IS the dispatcher's business.
	if _, err := db.sql.Exec(`UPDATE runs SET detached=1 WHERE id=?`, child); err != nil {
		t.Fatal(err)
	}
	_ = db.setStatus(child, statusSleep, now(), "", "")
	if !has(ids(t, db), child) {
		t.Fatal("a detached child that is due should be dispatched")
	}
	// ...and a top-level run was always fair game.
	_ = db.setStatus(parent, statusSleep, now(), "", "")
	if !has(ids(t, db), parent) {
		t.Fatal("a due top-level run should be dispatched")
	}
}

func TestReadyRunsSelectsAndExcludes(t *testing.T) {
	db := newTestDB(t)
	mk := func(title, status string, wake int64) int64 {
		id, err := db.createRun(title, "", 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.setStatus(id, status, wake, "", ""); err != nil {
			t.Fatal(err)
		}
		return id
	}
	queued := mk("queued", statusQueued, 0)
	dueSleep := mk("due", statusSleep, now()-5)
	futureSleep := mk("future", statusSleep, now()+3600)
	idle := mk("idle", statusIdle, 0)
	waiting := mk("waiting", statusWaiting, 0)
	done := mk("done", statusDone, 0)
	canceled := mk("canceled", statusCanceled, 0)

	got := ids(t, db)
	for _, c := range []struct {
		id   int64
		want bool
		why  string
	}{
		{queued, true, "queued runs are waiting for a slot, not for time"},
		{dueSleep, true, "a sleeping run past its wake_at is due"},
		{futureSleep, false, "a sleeping run before its wake_at is not due"},
		{idle, false, "idle means awaiting a user message"},
		{waiting, false, "waiting_input means a human owes an answer"},
		{done, false, "terminal"},
		{canceled, false, "terminal"},
	} {
		if has(got, c.id) != c.want {
			t.Fatalf("run %d (%s): want dispatched=%v — %s", c.id, db.mustTitle(t, c.id), c.want, c.why)
		}
	}
}

// TestRunningIsOnlyRecoveredAfterItsLeaseExpires — without this, a blue/green
// swap would have the new generation re-drive runs the draining one still holds.
func TestRunningIsOnlyRecoveredAfterItsLeaseExpires(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("r", "", 0)
	_ = db.setStatus(id, statusRunning, 0, "", "")

	ok, err := db.claimLease(id, "gen-A", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	if has(ids(t, db), id) {
		t.Fatal("a run held under a live lease must not be recovered")
	}
	// Another generation cannot steal it while the lease holds.
	if ok, _ := db.claimLease(id, "gen-B", time.Minute); ok {
		t.Fatal("a second generation claimed a leased run")
	}
	// The owner may always renew/re-take its own.
	if ok, _ := db.claimLease(id, "gen-A", time.Minute); !ok {
		t.Fatal("the lease owner should be able to re-claim")
	}

	// Expire it the way a dead process would.
	if _, err := db.sql.Exec(`UPDATE runs SET lease_until=? WHERE id=?`, now()-1, id); err != nil {
		t.Fatal(err)
	}
	if !has(ids(t, db), id) {
		t.Fatal("an expired lease must make the run recoverable")
	}
	if ok, _ := db.claimLease(id, "gen-B", time.Minute); !ok {
		t.Fatal("an expired lease should be claimable by a new generation")
	}
}

// TestReadyRunsPrioritisesDepth — leaves first, so their parents can unblock.
// Breadth-first would fill the ceiling with runs that cannot finish.
func TestReadyRunsPrioritisesDepth(t *testing.T) {
	db := newTestDB(t)
	root, _ := db.createRun("root", "", 0)
	child, _ := db.createRun("child", "", root)
	grand, _ := db.createRun("grand", "", child)
	for _, id := range []int64{root, child, grand} {
		_, _ = db.sql.Exec(`UPDATE runs SET detached=1 WHERE id=?`, id)
		_ = db.setStatus(id, statusQueued, 0, "", "")
	}
	got := ids(t, db)
	if len(got) != 3 || got[0] != grand || got[2] != root {
		t.Fatalf("expected deepest-first [grand, child, root], got %v (grand=%d root=%d)", got, grand, root)
	}
}

// TestCreateRunPlacesTheGraph — root_id and depth are derived at insert so no
// caller can put a run in an inconsistent position.
func TestCreateRunPlacesTheGraph(t *testing.T) {
	db := newTestDB(t)
	root, _ := db.createRun("root", "", 0)
	child, _ := db.createRun("child", "", root)
	grand, _ := db.createRun("grand", "", child)

	for _, c := range []struct {
		id    int64
		depth int
	}{{root, 0}, {child, 1}, {grand, 2}} {
		r, err := db.getRun(c.id)
		if err != nil {
			t.Fatal(err)
		}
		if r.Depth != c.depth {
			t.Fatalf("run %d: depth %d, want %d", c.id, r.Depth, c.depth)
		}
		if r.RootID != root {
			t.Fatalf("run %d: rootId %d, want %d", c.id, r.RootID, root)
		}
		if r.Detached {
			t.Fatal("a new run must not be detached until something makes it so")
		}
	}
}

// TestBackfillPlacesLegacyRows — existing databases have runs that predate the
// graph columns, and an unplaced row would be invisible to every tree query.
func TestBackfillPlacesLegacyRows(t *testing.T) {
	db := newTestDB(t)
	// Insert the way the pre-workflow code did: no root_id, no depth.
	res, err := db.sql.Exec(
		`INSERT INTO runs (title, status, config, parent_id, created, updated) VALUES ('old', 'idle', '', 0, ?, ?)`,
		now(), now())
	if err != nil {
		t.Fatal(err)
	}
	old, _ := res.LastInsertId()
	res, _ = db.sql.Exec(
		`INSERT INTO runs (title, status, config, parent_id, created, updated) VALUES ('oldkid', 'idle', '', ?, ?, ?)`,
		old, now(), now())
	kid, _ := res.LastInsertId()

	if err := db.init(); err != nil { // re-running init is what a restart does
		t.Fatal(err)
	}
	r, _ := db.getRun(old)
	if r.RootID != old || r.Depth != 0 {
		t.Fatalf("legacy top-level run: root=%d depth=%d", r.RootID, r.Depth)
	}
	k, _ := db.getRun(kid)
	if k.RootID != old || k.Depth != 1 {
		t.Fatalf("legacy child: root=%d depth=%d (want root=%d depth=1)", k.RootID, k.Depth, old)
	}
	if k.Detached {
		t.Fatal("a legacy child must stay non-detached — it was only ever meaningful inside its parent's tool call")
	}
}

func TestHasPendingCountsQueuedNotBlocked(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("r", "", 0)

	_ = db.setStatus(id, statusQueued, 0, "", "")
	if !db.hasPending() {
		t.Fatal("a queued run still needs the heartbeat — it is waiting for a slot")
	}
	// A blocked run waits on OTHER rows that are themselves pending, so the beat
	// stays on transitively; counting it directly would pin the beat forever the
	// first time a dependency became unsatisfiable.
	_ = db.setStatus(id, statusBlocked, 0, "", "")
	if db.hasPending() {
		t.Fatal("blocked must not keep the heartbeat on by itself")
	}
	_ = db.setStatus(id, statusDone, 0, "", "")
	if db.hasPending() {
		t.Fatal("a terminal run needs no heartbeat")
	}
}

func TestAddRunCostAccumulates(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("r", "", 0)
	db.addRunCost(id, 100, 10)
	db.addRunCost(id, 250, 25)
	r, _ := db.getRun(id)
	if r.LLMCalls != 2 || r.PromptTokens != 350 || r.CompletionTokens != 35 {
		t.Fatalf("got calls=%d prompt=%d completion=%d", r.LLMCalls, r.PromptTokens, r.CompletionTokens)
	}
}

func (d *DB) mustTitle(t *testing.T, id int64) string {
	t.Helper()
	r, err := d.getRun(id)
	if err != nil {
		return "?"
	}
	return r.Title
}

// --- ceiling, depth, budget ---------------------------------------------

// TestCeilingQueuesRatherThanSleeping — back-pressure has to be its own state.
// Parking an over-limit run as sleeping+wake_at is exactly what made a
// throttled subagent indistinguishable from one that chose to wait.
func TestCeilingQueuesRatherThanSleeping(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	ag.setLimit(1)

	if !ag.tryAcquire() {
		t.Fatal("the first slot should be free")
	}
	id, _ := db.createRun("over the limit", "", 0)
	ag.dispatchRun(id) // no slot available

	r, _ := db.getRun(id)
	if r.Status != statusQueued {
		t.Fatalf("an over-limit run should be queued, got %q", r.Status)
	}
	if active, limit := ag.activeDrives(); active != 1 || limit != 1 {
		t.Fatalf("slots: active=%d limit=%d", active, limit)
	}
	// A queued run is still the dispatcher's business, and still needs the beat.
	if !has(ids(t, db), id) {
		t.Fatal("a queued run must remain dispatchable")
	}
	if !db.hasPending() {
		t.Fatal("a queued run must keep the heartbeat on")
	}
}

// TestSlotIsReleasedAndResizable — a leaked slot would wedge the agent at
// capacity until a restart, and the ceiling has to be tunable live.
func TestSlotIsReleasedAndResizable(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	ag.setLimit(2)
	if !ag.tryAcquire() || !ag.tryAcquire() {
		t.Fatal("two slots should be available")
	}
	if ag.tryAcquire() {
		t.Fatal("the third acquire should fail at limit 2")
	}
	ag.releaseSlot()
	if !ag.tryAcquire() {
		t.Fatal("a released slot should be reusable")
	}
	ag.setLimit(4)
	if !ag.tryAcquire() {
		t.Fatal("raising the limit should free capacity immediately")
	}
	// Over-releasing must not create slots from nothing.
	for i := 0; i < 10; i++ {
		ag.releaseSlot()
	}
	if active, _ := ag.activeDrives(); active != 0 {
		t.Fatalf("active went to %d on over-release", active)
	}
}

// TestSpawnBudgetIsAtomicAndMonotonic — the budget bounds a tree's LIFETIME
// creations, so a spawn→finish→spawn loop cannot run forever, and two parallel
// spawns must not both pass the same last unit.
func TestSpawnBudgetIsAtomicAndMonotonic(t *testing.T) {
	db := newTestDB(t)
	root, _ := db.createRun("root", "", 0)
	db.ensureTree(root, 3, 2)

	for i := 0; i < 3; i++ {
		if ok, _, _ := db.reserveSpawn(root, 3); !ok {
			t.Fatalf("reservation %d should succeed within a budget of 3", i+1)
		}
	}
	ok, spawned, max := db.reserveSpawn(root, 3)
	if ok {
		t.Fatal("the fourth reservation must fail")
	}
	if spawned != 3 || max != 3 {
		t.Fatalf("budget reports spawned=%d max=%d", spawned, max)
	}
	// Deleting a child must NOT refund the budget — it is a blast-radius
	// ceiling, not a live-children gauge.
	kid, _ := db.createRun("kid", "", root)
	_ = db.deleteRun(kid)
	if ok, _, _ := db.reserveSpawn(root, 3); ok {
		t.Fatal("deleting a run refunded the lifetime budget")
	}
	// An explicit release (the child could not be created) does refund.
	db.releaseSpawn(root)
	if ok, _, _ := db.reserveSpawn(root, 3); !ok {
		t.Fatal("releaseSpawn should hand the reservation back")
	}
}

func TestSpawnBudgetKeepsTheLimitItStartedWith(t *testing.T) {
	db := newTestDB(t)
	root, _ := db.createRun("root", "", 0)
	db.ensureTree(root, 2, 3) // this tree started under a budget of 2
	db.ensureTree(root, 99, 9)
	if ok, _, _ := db.reserveSpawn(root, 99); !ok {
		t.Fatal(err0())
	}
	if ok, _, _ := db.reserveSpawn(root, 99); !ok {
		t.Fatal(err0())
	}
	if ok, _, max := db.reserveSpawn(root, 99); ok || max != 2 {
		t.Fatalf("a running tree must keep its original budget, got ok=%v max=%d", ok, max)
	}
}

func err0() string { return "reservation within budget should succeed" }

// TestDepthGatesDelegationAndScheduling — the spec must VANISH at the limit,
// and a subagent must never be able to create a cron-agent, which would
// outlive the tree that made it and answer to nobody.
func TestDepthGatesDelegationAndScheduling(t *testing.T) {
	names := func(depth int, cfg Config) map[string]bool {
		out := map[string]bool{}
		for _, s := range toolSpecs(cfg, depth, nil) {
			out[s.Function.Name] = true
		}
		return out
	}
	cfg := Config{Subagents: true}
	max := cfg.maxDepth()

	if !names(0, cfg)["spawn_subagent"] {
		t.Fatal("a top-level run should be able to delegate")
	}
	if !names(max-1, cfg)["spawn_subagent"] {
		t.Fatal("one below the limit should still be able to delegate (off-by-one)")
	}
	if names(max, cfg)["spawn_subagent"] {
		t.Fatal("delegation must be absent AT the limit, not present-and-erroring")
	}
	if names(0, Config{Subagents: false})["spawn_subagent"] {
		t.Fatal("subagents:false must hide it entirely")
	}
	if !names(0, cfg)["schedule"] || !names(0, cfg)["unschedule"] {
		t.Fatal("a top-level run should be able to schedule")
	}
	for d := 1; d <= max; d++ {
		if names(d, cfg)["schedule"] {
			t.Fatalf("depth %d: a subagent must not be able to create cron-agents", d)
		}
	}
	// Everything else is unaffected by depth. (ask_user is NOT in this list:
	// it is depth-0 only by design — see TestDetachedRunCannotAskTheHuman.)
	for _, n := range []string{"memory_set", "finish", "yield", "js_eval"} {
		if !names(max, cfg)[n] {
			t.Fatalf("%s should still be offered at max depth", n)
		}
	}
}

func TestWorkflowConfigDefaultsAndClamps(t *testing.T) {
	var c Config
	if c.maxDepth() != defaultMaxDepth || c.maxSpawn() != defaultMaxSpawn ||
		c.maxSpawnTurn() != defaultMaxSpawnTurn || c.maxActiveRuns() != defaultMaxActiveRuns {
		t.Fatal("zero config should yield the documented defaults")
	}
	c = Config{MaxDepth: 999, MaxSpawn: 100000, MaxSpawnTurn: 999, MaxActiveRuns: 999}
	if c.maxDepth() != 8 || c.maxSpawn() != 500 || c.maxSpawnTurn() != 32 || c.maxActiveRuns() != 32 {
		t.Fatalf("limits must be clamped: %d %d %d %d",
			c.maxDepth(), c.maxSpawn(), c.maxSpawnTurn(), c.maxActiveRuns())
	}
}

// --- join ----------------------------------------------------------------

// spawnTurn simulates the model emitting N spawn_subagent calls in one turn.
func spawnTurn(t *testing.T, ag *Agent, parent int64, n int) ([]toolCall, []int64) {
	t.Helper()
	run, err := ag.db.getRun(parent)
	if err != nil {
		t.Fatal(err)
	}
	calls := make([]toolCall, n)
	for i := range calls {
		calls[i] = call(fmt.Sprintf("c%d", i), "spawn_subagent")
		calls[i].Function.Arguments = fmt.Sprintf(`{"task":"job %d"}`, i)
	}
	b, _ := json.Marshal(calls)
	if _, err := ag.db.addMessage(&Message{RunID: parent, Role: "assistant", ToolCalls: string(b)}); err != nil {
		t.Fatal(err)
	}
	parked, term := ag.executeToolCalls(context.Background(), run, Config{Subagents: true}, calls, false)
	if !parked || term {
		t.Fatalf("a spawning turn must park: parked=%v terminal=%v", parked, term)
	}
	kids, err := ag.db.descendants(parent)
	if err != nil {
		t.Fatal(err)
	}
	return calls, kids
}

// TestSpawnParksAndKeepsTheTranscriptValid — the parent must answer its own
// tool_calls block before parking, or the next request it makes is invalid.
func TestSpawnParksAndKeepsTheTranscriptValid(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)

	calls, kids := spawnTurn(t, ag, parent, 3)
	if len(kids) != 3 {
		t.Fatalf("expected 3 children, got %d", len(kids))
	}
	assertTranscriptValid(t, db, parent)

	p, _ := db.getRun(parent)
	if p.Status != statusBlocked {
		t.Fatalf("parent should be blocked, got %q", p.Status)
	}
	if db.openDepCount(parent) != 3 {
		t.Fatalf("expected 3 open deps, got %d", db.openDepCount(parent))
	}
	// The dispatcher must not touch the parent until every child settles...
	if has(ids(t, db), parent) {
		t.Fatal("a parent with pending dependencies must not be dispatched")
	}
	// ...but the children are its business immediately.
	for _, k := range kids {
		kid, _ := db.getRun(k)
		if !kid.Detached {
			t.Fatalf("child %d must be detached to be dispatchable", k)
		}
		if kid.Depth != 1 || kid.RootID != parent {
			t.Fatalf("child %d: depth=%d root=%d", k, kid.Depth, kid.RootID)
		}
		if !has(ids(t, db), k) {
			t.Fatalf("child %d should be dispatchable", k)
		}
	}
	_ = calls
}

// TestChildrenSettleThenParentResumesOnce is the coalescing guarantee: five
// children finishing together must produce ONE parent drive with five results,
// not five drives.
func TestChildrenSettleThenParentResumesOnce(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)
	calls, kids := spawnTurn(t, ag, parent, 3)

	for i, k := range kids {
		if has(ids(t, db), parent) {
			t.Fatalf("parent woke after %d of 3 children settled", i)
		}
		ag.settle(k, outcomeDone, fmt.Sprintf("answer %d", i))
	}
	got := ids(t, db)
	if !has(got, parent) {
		t.Fatal("the parent should be ready once every child has settled")
	}

	// Delivery rewrites the placeholders, in call order.
	if n := ag.deliverSettledDeps(parent); n != 3 {
		t.Fatalf("expected 3 deliveries, got %d", n)
	}
	assertTranscriptValid(t, db, parent)
	msgs, _ := db.messages(parent, true)
	for i, tc := range calls {
		m := msgs[1+i]
		if m.ToolCallID != tc.ID {
			t.Fatalf("result %d is out of call order: %q", i, m.ToolCallID)
		}
		if !strings.Contains(m.Content, fmt.Sprintf("answer %d", i)) {
			t.Fatalf("result %d does not carry its child's answer: %q", i, m.Content)
		}
	}
	// And delivery is exactly-once.
	if n := ag.deliverSettledDeps(parent); n != 0 {
		t.Fatalf("redelivery: %d", n)
	}
	assertTranscriptValid(t, db, parent)
}

// TestDeliveryIsIdempotentAcrossACrash — a result is never "in flight", it is a
// row: delivered=0 is durable, so a crash before delivery replays cleanly.
func TestDeliveryIsIdempotentAcrossACrash(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)
	_, kids := spawnTurn(t, ag, parent, 1)
	ag.settle(kids[0], outcomeDone, "the answer")

	// "Crash" before delivery: nothing was written, the edge is still undelivered.
	deps, _ := db.undeliveredDeps(parent)
	if len(deps) != 1 {
		t.Fatalf("expected 1 undelivered dep, got %d", len(deps))
	}
	if !has(ids(t, db), parent) {
		t.Fatal("a parent with an undelivered settled dep must be re-selected after a restart")
	}
	if n := ag.deliverSettledDeps(parent); n != 1 {
		t.Fatalf("expected 1 delivery after restart, got %d", n)
	}
	assertTranscriptValid(t, db, parent)
}

// TestFailedChildReportsRatherThanHangs — a waiter must learn its dependency
// failed, not wait forever for a result that is never coming.
func TestFailedChildReportsRatherThanHangs(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)
	_, kids := spawnTurn(t, ag, parent, 1)

	ag.fail(kids[0], "llm-gw 503")
	if db.openDepCount(parent) != 0 {
		t.Fatal("a failed child must settle its dependency edge")
	}
	ag.deliverSettledDeps(parent)
	msgs, _ := db.messages(parent, true)
	body := msgs[1].Content
	if !strings.Contains(body, "failed") || !strings.Contains(body, "503") {
		t.Fatalf("the parent should be told what went wrong, got %q", body)
	}
	assertTranscriptValid(t, db, parent)
}

// TestNestedSpawnDoesNotDeadlock — the reason the parent parks instead of
// blocking. A synchronous spawn holds a drive slot while waiting for a child
// that needs a slot, which at limit 1 deadlocks on the first nested spawn.
func TestNestedSpawnDoesNotDeadlock(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	ag.setLimit(1)

	parent, _ := db.createRun("parent", "", 0)
	_, kids := spawnTurn(t, ag, parent, 1)
	child := kids[0]

	// The parent is parked. It must hold NO slot, so the child can run.
	if active, _ := ag.activeDrives(); active != 0 {
		t.Fatalf("a parked parent still holds %d slot(s) — this is the deadlock", active)
	}
	if !ag.tryAcquire() {
		t.Fatal("the child cannot get a slot: the parent is holding it while waiting for the child")
	}
	ag.releaseSlot()

	// And the child can itself spawn without anyone blocking.
	crun, _ := db.getRun(child)
	if _, err := ag.startChild(crun, Config{Subagents: true}, "grandchild work", "", "", nil, ""); err != nil {
		t.Fatalf("nested spawn failed: %v", err)
	}
	if active, _ := ag.activeDrives(); active != 0 {
		t.Fatalf("nested spawning consumed %d slot(s)", active)
	}
}

// TestAfterEdgesCarryOrderAndData — without blocking, "B after A" is just two
// racing agents; without the data, the parent has to pay for A's tokens twice.
func TestAfterEdgesCarryOrderAndData(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	cfg := Config{Subagents: true}

	a, err := ag.startChild(run, cfg, "gather A", "", "A", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := ag.startChild(run, cfg, "synthesize", "", "B", []int64{a}, "")
	if err != nil {
		t.Fatal(err)
	}
	brun, _ := db.getRun(b)
	if brun.Status != statusBlocked {
		t.Fatalf("B should be blocked behind A, got %q", brun.Status)
	}
	if has(ids(t, db), b) {
		t.Fatal("B must not run before A settles")
	}
	if !has(ids(t, db), a) {
		t.Fatal("A should be dispatchable immediately")
	}

	ag.settle(a, outcomeDone, "A's findings")
	if !has(ids(t, db), b) {
		t.Fatal("B should unblock once A settles")
	}
	// The edge carries data: A's result lands in B's transcript.
	if n := ag.deliverSettledDeps(b); n != 1 {
		t.Fatalf("expected A's result delivered to B, got %d", n)
	}
	msgs, _ := db.messages(b, true)
	last := msgs[len(msgs)-1]
	if last.Role != "user" || !strings.Contains(last.Content, "A's findings") {
		t.Fatalf("B did not receive A's result: role=%q %q", last.Role, last.Content)
	}
	// A fire-and-forget delivery must say it is not the owner talking.
	if !strings.Contains(last.Content, "not the owner") {
		t.Fatalf("an injected result must be distinguishable from the owner: %q", last.Content)
	}
}

// TestSubtreeScopingIsTheNewBoundary — before this layer no run could read
// another run's anything, and these tools take a model-supplied integer.
func TestSubtreeScopingIsTheNewBoundary(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	mine, _ := db.createRun("mine", "", 0)
	theirs, _ := db.createRun("theirs", "", 0)
	mineRun, _ := db.getRun(mine)

	kid, err := ag.startChild(mineRun, Config{Subagents: true}, "work", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	kidRun, _ := db.getRun(kid)
	grand, _ := ag.startChild(kidRun, Config{Subagents: true}, "deeper", "", "", nil, "")

	if _, err := ag.workflowNode(mineRun, kid); err != nil {
		t.Fatalf("a direct child should resolve: %v", err)
	}
	if _, err := ag.workflowNode(mineRun, grand); err != nil {
		t.Fatalf("a grandchild should resolve — a parent owns everything it caused: %v", err)
	}
	if _, err := ag.workflowNode(mineRun, theirs); err == nil {
		t.Fatal("another tree's run must be refused")
	}
	if _, err := ag.workflowNode(mineRun, mine); err == nil {
		t.Fatal("the caller's own id must be refused")
	}
	if _, err := ag.workflowNode(mineRun, 99999); err == nil {
		t.Fatal("a nonexistent id must be refused, not panic")
	}
}

func TestWorkflowToolGating(t *testing.T) {
	names := func(depth int, cfg Config) map[string]bool {
		out := map[string]bool{}
		for _, s := range toolSpecs(cfg, depth, nil) {
			out[s.Function.Name] = true
		}
		return out
	}
	cfg := Config{Subagents: true}
	max := cfg.maxDepth()

	// Lane-neutral: it touches only this agent's own rows, so it belongs in both.
	for _, lane := range []string{"", "web"} {
		n := names(0, Config{Subagents: true, Toolset: lane})
		for tool := range workflowToolNames {
			if !n[tool] {
				t.Fatalf("toolset %q: %s missing — the layer widens neither lane", lane, tool)
			}
		}
	}
	// At the limit you can still LOOK, but not delegate.
	if names(max, cfg)["workflow_spawn"] {
		t.Fatal("workflow_spawn must be absent at max depth")
	}
	if !names(max, cfg)["workflow_status"] || !names(max, cfg)["workflow_result"] {
		t.Fatal("a leaf should still be able to inspect its own tree")
	}
	off := names(0, Config{Subagents: true, Features: map[string]bool{"workflow": false}})
	for tool := range workflowToolNames {
		if off[tool] {
			t.Fatalf("workflow:false should hide %s", tool)
		}
	}
}

func TestWorkflowToolsAreNotSideEffecting(t *testing.T) {
	// Spawning risks spend, not effect: a child's world-touching calls hit the
	// approval gate at their own boundary, because Approve is inherited.
	for n := range workflowToolNames {
		if sideEffect(n) {
			t.Fatalf("%s should not require approval", n)
		}
	}
	if sideEffect("spawn_subagent") {
		t.Fatal("spawn_subagent should not require approval")
	}
}

func TestChildInheritsLaneAndCannotWidenIt(t *testing.T) {
	for _, lane := range []string{"private", "web"} {
		parent := Config{Toolset: lane, Subagents: true, Features: map[string]bool{"repl": false}}
		child := childConfig(parent, "a totally different system prompt")
		if child.toolset() != parent.toolset() {
			t.Fatalf("lane %q: child got %q", lane, child.toolset())
		}
		// The features map must be copied, not shared.
		child.Features["repl"] = true
		if parent.Features["repl"] {
			t.Fatal("child and parent share a Features map")
		}
		if !strings.Contains(child.System, "You are a subagent") {
			t.Fatal("the child contract must survive a system override")
		}
	}
}

// --- cancellation --------------------------------------------------------

// TestCancelIsDurable — the old interrupt wrote to a RAM map, so a swap erased
// it and a sleeping descendant was resurrected by the next heartbeat. A stop
// button a restart undoes is not a stop button.
func TestCancelIsDurable(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	kid, err := ag.startChild(run, Config{Subagents: true}, "work", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}

	stopped := ag.requestCancel(kid, true, "not needed")
	if len(stopped) != 1 {
		t.Fatalf("expected 1 run cancelled, got %d", len(stopped))
	}
	k, _ := db.getRun(kid)
	if k.Status != statusCanceled || k.Outcome != outcomeCanceled || k.CancelReq == 0 {
		t.Fatalf("cancel is not durable: status=%q outcome=%q cancel_req=%d", k.Status, k.Outcome, k.CancelReq)
	}
	// A fresh process would see the same thing — and never dispatch it.
	if has(ids(t, db), kid) {
		t.Fatal("a cancelled run must not be dispatchable after a restart")
	}
	// The waiter is released rather than left hanging.
	if db.openDepCount(root) != 0 {
		t.Fatal("cancelling a child must settle its dependency edge")
	}
	ag.deliverSettledDeps(root)
	msgs, _ := db.messages(root, true)
	if !strings.Contains(msgs[len(msgs)-1].Content, "cancelled") {
		t.Fatalf("the waiter should be told the run was cancelled: %q", msgs[len(msgs)-1].Content)
	}
}

// TestCancelCascadesDownTheTree — cancelling 14 runs is pointless if node 15
// is still going.
func TestCancelCascadesDownTheTree(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	cfg := Config{Subagents: true}
	a, _ := ag.startChild(run, cfg, "a", "", "", nil, "")
	arun, _ := db.getRun(a)
	b, _ := ag.startChild(arun, cfg, "b", "", "", nil, "")

	ag.requestCancel(a, true, "stop")
	for _, id := range []int64{a, b} {
		r, _ := db.getRun(id)
		if r.Status != statusCanceled {
			t.Fatalf("run %d survived the cascade: %q", id, r.Status)
		}
	}
	// The root is untouched — cancel goes down, never up.
	r, _ := db.getRun(root)
	if r.Status == statusCanceled {
		t.Fatal("cancelling a child must not cancel its parent")
	}
}

// TestFinishCancelsLiveDescendants — no run outlives the consumer of its
// output.
func TestFinishCancelsLiveDescendants(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	kid, _ := ag.startChild(run, Config{Subagents: true}, "still going", "", "", nil, "")

	ag.cancelDescendants(root, "parent finished")
	k, _ := db.getRun(kid)
	if k.Status != statusCanceled {
		t.Fatalf("a live child should be stopped when its parent ends, got %q", k.Status)
	}
}

// TestDetachedRunCannotAskTheHuman — otherwise the child parks on a human
// while its parent parks on the child, and nothing can answer either.
func TestDetachedRunCannotAskTheHuman(t *testing.T) {
	names := func(depth int) map[string]bool {
		out := map[string]bool{}
		for _, s := range toolSpecs(Config{Subagents: true}, depth, nil) {
			out[s.Function.Name] = true
		}
		return out
	}
	if !names(0)["ask_user"] {
		t.Fatal("a top-level run must still be able to ask")
	}
	if names(1)["ask_user"] {
		t.Fatal("a subagent must not be offered ask_user")
	}

	// And a hallucinated call is converted into a reported blocker, not a park.
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	kid, _ := ag.startChild(run, Config{Subagents: true}, "work", "", "", nil, "")
	krun, _ := db.getRun(kid)

	c := call("q1", "ask_user")
	c.Function.Arguments = `{"question":"which account?"}`
	b, _ := json.Marshal([]toolCall{c})
	_, _ = db.addMessage(&Message{RunID: kid, Role: "assistant", ToolCalls: string(b)})
	parked, term := ag.executeToolCalls(context.Background(), krun, Config{}, []toolCall{c}, false)
	if parked || !term {
		t.Fatalf("a detached ask_user should end the run, not park it: parked=%v terminal=%v", parked, term)
	}
	k, _ := db.getRun(kid)
	if k.Outcome != outcomeIncomplete || !strings.Contains(k.Result, "BLOCKED") {
		t.Fatalf("the blocker should be reported as the result: outcome=%q result=%q", k.Outcome, k.Result)
	}
	assertTranscriptValid(t, db, kid)
}

// --- the dispatch invariant ---------------------------------------------
//
// One rule: a run that has been asked to run must never be left in a state the
// dispatcher cannot select. Before these tests the entire admission path was
// unreachable from the suite — newTestAgent omitted the cancels map, so the
// goroutine panicked on a nil-map write and every test called drive() directly.
// Nine silent drop points shipped behind that gap.

// reachable reports whether the dispatcher can still find this run, which is
// the only thing that decides "retried later" versus "the prompt is gone".
func reachable(t *testing.T, db *DB, id int64) bool {
	t.Helper()
	return has(ids(t, db), id) && db.hasPending()
}

func TestDispatchIsNeverSilentlyDropped(t *testing.T) {
	cases := []struct {
		name  string
		why   string
		setup func(t *testing.T, ag *Agent, db *DB, id int64)
	}{
		{"halted", "the brake is on — a prompt must survive it, not vanish",
			func(t *testing.T, ag *Agent, db *DB, id int64) { _ = db.putSetting("halt", "1") }},
		{"ceiling full", "every slot is busy — the run waits its turn, it does not die",
			func(t *testing.T, ag *Agent, db *DB, id int64) {
				ag.setLimit(1)
				if !ag.tryAcquire() {
					t.Fatal("setup: could not take the only slot")
				}
			}},
		{"stale foreign lease", "a save killed the previous process mid-drive",
			func(t *testing.T, ag *Agent, db *DB, id int64) {
				if _, err := db.sql.Exec(
					`UPDATE runs SET lease_owner='dead-generation', lease_until=? WHERE id=?`,
					now()+int64(leaseTTL.Seconds()), id); err != nil {
					t.Fatal(err)
				}
			}},
		{"claim held", "another drive is already on it in this process",
			func(t *testing.T, ag *Agent, db *DB, id int64) {
				if !ag.claim(id) {
					t.Fatal("setup: could not take the claim")
				}
			}},
	}
	// This is the sequence every human entry point performs — handleAsk
	// (assistant.go), handleMessage and handleResume (main.go) all call exactly
	// these two. Testing it once is honest; the handlers themselves are thin
	// wrappers that the live check covers.
	enter := func(ag *Agent, id int64) { ag.resumeIfHalted(id); ag.driveAsync(id) }

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := newTestDB(t)
			ag := newTestAgent(t, db)
			id, _ := db.createRun("a prompt", "", 0)
			// A fresh run is idle — the state readyRuns deliberately cannot see.
			c.setup(t, ag, db, id)
			enter(ag, id)

			if c.name == "claim held" {
				// A live drive owns it and will kick when it exits; the only
				// thing to assert is that we did not corrupt its state.
				r, _ := db.getRun(id)
				if terminalStatus(r.Status) {
					t.Fatalf("a claimed run was made terminal: %q", r.Status)
				}
				return
			}
			if !reachable(t, db, id) {
				r, _ := db.getRun(id)
				t.Fatalf("%s: run left at %q, unreachable by the dispatcher — %s",
					c.name, r.Status, c.why)
			}
		})
	}
}

// TestIdleIsStillNotSelectable pins the asymmetry the fix relies on: `idle`
// means "waiting for a human" and must NOT be dispatchable, which is exactly
// why a dropped dispatch that leaves a run idle is fatal and why the handlers
// have to move it to `queued` themselves.
func TestIdleIsStillNotSelectable(t *testing.T) {
	db := newTestDB(t)
	id, _ := db.createRun("waiting for you", "", 0)
	if has(ids(t, db), id) {
		t.Fatal("an idle run must not be dispatched — it would spin forever")
	}
	if db.hasPending() {
		t.Fatal("an idle run must not keep the heartbeat registered")
	}
}

func TestStaleLeaseDoesNotStrandARun(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("follow-up", "", 0)
	// Exactly what a save leaves behind: the killed process's lease outlives it.
	if _, err := db.sql.Exec(`UPDATE runs SET lease_owner='dead-generation', lease_until=? WHERE id=?`,
		now()+int64(leaseTTL.Seconds()), id); err != nil {
		t.Fatal(err)
	}
	ag.driveAsync(id)

	r, _ := db.getRun(id)
	if r.Status != statusQueued {
		t.Fatalf("a run blocked by a stale lease should be queued for retry, got %q", r.Status)
	}
	if !reachable(t, db, id) {
		t.Fatal("…and it must be selectable, or the prompt is lost rather than delayed")
	}
	// Once the dead process's lease lapses, the run is ours.
	if _, err := db.sql.Exec(`UPDATE runs SET lease_until=? WHERE id=?`, now()-1, id); err != nil {
		t.Fatal(err)
	}
	ok, err := db.claimLease(id, ag.gen, leaseTTL)
	if err != nil || !ok {
		t.Fatalf("an expired lease should be claimable: %v %v", ok, err)
	}
}

// TestLeaseWindowMatchesTheDrain — the TTL is how long a prompt can stall after
// a save, so it is a user-visible number, not an implementation detail.
func TestLeaseWindowMatchesTheDrain(t *testing.T) {
	if leaseTTL > 30*time.Second {
		t.Fatalf("leaseTTL is %v: a killed process hides its run from the dispatcher for that long", leaseTTL)
	}
}

func TestHumanPromptResumesAHaltedAgent(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("a prompt", "", 0)
	if err := db.putSetting("halt", "1"); err != nil {
		t.Fatal(err)
	}
	if !ag.halted() {
		t.Fatal("setup: halt should be on")
	}

	ag.resumeIfHalted(id)
	if ag.halted() {
		t.Fatal("sending a message must clear the brake — a halt that silently eats prompts is indistinguishable from a broken agent")
	}
	// And it says so, rather than resuming invisibly.
	steps, _ := db.steps(id)
	var told bool
	for _, s := range steps {
		if strings.Contains(s.Detail, "halt cleared") {
			told = true
		}
	}
	if !told {
		t.Fatal("clearing the brake should be journalled")
	}
}

// TestQueuedParkPreservesWakeAndPending — parking must not invent state. The
// old inline write zeroed a sleeping run's wake time and could demote a run
// parked on the human, which then drove with their question unanswered.
func TestQueuedParkPreservesWakeAndPending(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)

	t.Run("sleeping keeps its wake time", func(t *testing.T) {
		id, _ := db.createRun("sleeper", "", 0)
		wake := now() + 3600
		if err := db.setStatus(id, statusSleep, wake, "", ""); err != nil {
			t.Fatal(err)
		}
		ag.parkQueued(id)
		r, _ := db.getRun(id)
		if r.WakeAt != wake {
			t.Fatalf("wake_at was clobbered: %d (want %d)", r.WakeAt, wake)
		}
	})

	t.Run("a parked approval is left alone", func(t *testing.T) {
		id, _ := db.createRun("awaiting approval", "", 0)
		if err := db.setStatus(id, statusWaiting, 0, "approve?", `{"kind":"approval"}`); err != nil {
			t.Fatal(err)
		}
		ag.parkQueued(id)
		r, _ := db.getRun(id)
		if r.Status != statusWaiting {
			t.Fatalf("a run parked on the human was demoted to %q; it would then drive with their question unanswered", r.Status)
		}
		if r.Pending == "" {
			t.Fatal("the parked tool calls were dropped")
		}
	})

	t.Run("blocked keeps its dependency branch", func(t *testing.T) {
		id, _ := db.createRun("awaiting children", "", 0)
		if err := db.setStatus(id, statusBlocked, 0, "", ""); err != nil {
			t.Fatal(err)
		}
		ag.parkQueued(id)
		r, _ := db.getRun(id)
		if r.Status != statusBlocked {
			t.Fatalf("a blocked run became %q, losing the branch that waits for its children", r.Status)
		}
	})

	t.Run("terminal runs are untouched", func(t *testing.T) {
		id, _ := db.createRun("finished", "", 0)
		if err := db.setStatus(id, statusDone, 0, "the answer", ""); err != nil {
			t.Fatal(err)
		}
		ag.parkQueued(id)
		r, _ := db.getRun(id)
		if r.Status != statusDone || r.Result != "the answer" {
			t.Fatalf("a finished run was resurrected as %q", r.Status)
		}
	})
}

// --- Part B: the stranding paths ----------------------------------------

// TestCancelledRunIsAlwaysSettled — readyRuns filters cancel_req=0 and nothing
// ever clears the column, so a cancelled run that nobody is driving would sit
// at `running` forever: invisible to every dispatcher, its waiters blocked
// forever, and the heartbeat pinned on. Delegating the settle to a live drive
// is only safe when a live drive actually exists.
func TestCancelledRunIsAlwaysSettled(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)
	prun, _ := db.getRun(parent)
	kid, err := ag.startChild(prun, Config{Subagents: true}, "work", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	// A row left `running` by a crash or a swap: status says running, but no
	// process holds its lease.
	if err := db.setStatus(kid, statusRunning, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if db.leaseHeld(kid) {
		t.Fatal("setup: no lease should be held")
	}

	ag.requestCancel(kid, true, "halted by the owner")

	k, _ := db.getRun(kid)
	if k.SettledAt == 0 {
		t.Fatalf("a cancelled run with no live drive was left unsettled at %q — nothing would ever pick it up", k.Status)
	}
	if k.Status != statusCanceled {
		t.Fatalf("status %q, want %q", k.Status, statusCanceled)
	}
	if db.openDepCount(parent) != 0 {
		t.Fatal("the waiting parent was left blocked forever")
	}
	if has(ids(t, db), kid) {
		t.Fatal("a settled run must not be dispatched")
	}
}

// TestCancelStillDefersToALiveDrive — the delegation is correct when a drive
// really does hold the run; it should notice at its next iteration rather than
// having the row changed underneath it.
func TestCancelStillDefersToALiveDrive(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("live", "", 0)
	if err := db.setStatus(id, statusRunning, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.claimLease(id, ag.gen, leaseTTL); err != nil || !ok {
		t.Fatalf("setup: %v %v", ok, err)
	}
	ag.requestCancel(id, false, "stop")

	r, _ := db.getRun(id)
	if r.CancelReq == 0 {
		t.Fatal("cancel must be recorded durably regardless")
	}
	if r.SettledAt != 0 {
		t.Fatal("a live drive owns this run; settling under it would race its own writes")
	}
}

// TestHaltDoesNotStrandRunningRuns — halt cancels every live run at once, which
// is the easiest way to reach the stranding bug above.
func TestHaltDoesNotStrandRunningRuns(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	var ids64 []int64
	for i := 0; i < 3; i++ {
		id, _ := db.createRun("in flight", "", 0)
		_ = db.setStatus(id, statusRunning, 0, "", "")
		ids64 = append(ids64, id)
	}
	for _, id := range db.liveRunIDs() {
		ag.requestCancel(id, false, "halted by the owner")
	}
	for _, id := range ids64 {
		r, _ := db.getRun(id)
		if r.SettledAt == 0 {
			t.Fatalf("run %d stranded at %q after halt", id, r.Status)
		}
	}
}

// TestInterruptSurvivesAdmission — release() is now called at admission time
// too, and it used to clear the interrupt flag, so a stop landing in that
// window was silently discarded and the run drove on.
func TestInterruptSurvivesAdmission(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("r", "", 0)

	ag.claim(id)
	ag.requestStop(id) // the human hits interrupt mid-admission
	ag.release(id)     // …and admission finishes
	if !ag.stopped(id) {
		t.Fatal("an interrupt requested during admission was erased before any drive could see it")
	}
	// It is consumed by the drive it was meant for, not by admission.
	ag.clearStop(id)
	if ag.stopped(id) {
		t.Fatal("clearStop should consume the flag")
	}
}

// TestCancelRegistrationIsNotClobbered — two dispatches racing on one run used
// to have the loser's deferred unregister delete the WINNER's entry, disarming
// abort() for the rest of that drive.
func TestCancelRegistrationIsNotClobbered(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	var abortedA, abortedB bool
	tokA := ag.registerCancel(7, func() { abortedA = true })
	tokB := ag.registerCancel(7, func() { abortedB = true })

	ag.unregisterCancel(7, tokA) // the loser cleans up after itself
	ag.abort(7)
	if !abortedB {
		t.Fatal("the live drive's cancel was removed by an earlier registration's cleanup")
	}
	if abortedA {
		t.Fatal("the superseded cancel should not have fired")
	}
	ag.unregisterCancel(7, tokB)
	ag.abort(7) // now a no-op
}

// TestSpawnSubagentAnywhereInATurn — the batch collector stopped only at
// parking tools, so a spawn that was not first got swept into runToolBatch,
// where runTool has no case for it and answers `unknown tool`. Delegation
// silently failed and the model was told the tool did not exist.
func TestSpawnSubagentAnywhereInATurn(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	parent, _ := db.createRun("parent", "", 0)
	run, _ := db.getRun(parent)

	note := call("n1", "note")
	note.Function.Arguments = `{"text":"planning"}`
	spawn := call("s1", "spawn_subagent")
	spawn.Function.Arguments = `{"task":"go and look"}`
	calls := []toolCall{note, spawn}
	b, _ := json.Marshal(calls)
	if _, err := db.addMessage(&Message{RunID: parent, Role: "assistant", ToolCalls: string(b)}); err != nil {
		t.Fatal(err)
	}

	parked, _ := ag.executeToolCalls(context.Background(), run, Config{Subagents: true}, calls, false)
	if !parked {
		t.Fatal("the turn should park on the spawned child")
	}
	kids, _ := db.descendants(parent)
	if len(kids) != 1 {
		t.Fatalf("expected 1 child spawned, got %d", len(kids))
	}
	msgs, _ := db.messages(parent, true)
	for _, m := range msgs {
		if m.ToolCallID == "s1" && strings.Contains(m.Content, "unknown tool") {
			t.Fatalf("spawn_subagent was swept into the tool batch: %q", m.Content)
		}
	}
	assertTranscriptValid(t, db, parent)
}

// TestConfiguredCeilingSurvivesRestart — setLimit was reachable only from a
// config write, so the ceiling silently reverted to the default on every swap.
func TestConfiguredCeilingSurvivesRestart(t *testing.T) {
	db := newTestDB(t)
	cfg := defaultConfig()
	cfg.MaxActiveRuns = 9
	b, _ := json.Marshal(cfg)
	if err := db.putSetting("config", string(b)); err != nil {
		t.Fatal(err)
	}
	// What main() does at boot.
	ag := newTestAgent(t, db)
	ag.setLimit(parseConfig(db.getSetting("config")).maxActiveRuns())
	if _, limit := ag.activeDrives(); limit != 9 {
		t.Fatalf("ceiling is %d after boot, want the configured 9", limit)
	}
}

// TestEveryEntryPointParks — the first version of this fix put the durable park
// in the handlers, and three call sites (new task, learn, cron firing) were
// simply missed, so those prompts kept dying while the fixed ones worked. The
// park now lives in driveAsync, which is the only way anything asks for a run
// to be advanced; this asserts that rather than trusting each caller.
func TestEveryEntryPointParks(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	// Wedge the ceiling so every dispatch is declined.
	ag.setLimit(1)
	if !ag.tryAcquire() {
		t.Fatal("setup: could not take the only slot")
	}
	for _, name := range []string{"quick ask", "new task", "learn", "cron firing"} {
		id, _ := db.createRun(name, "", 0)
		ag.driveAsync(id) // what every one of those handlers calls
		if !reachable(t, db, id) {
			r, _ := db.getRun(id)
			t.Fatalf("%s: left at %q, unreachable by the dispatcher", name, r.Status)
		}
	}
}

// TestRecoverStrandedRuns — rows stranded by the bugs above do not fix
// themselves, because readyRuns cannot see either shape.
func TestRecoverStrandedRuns(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)

	// (1) cancelled but never settled: readyRuns filters cancel_req=0.
	parent, _ := db.createRun("parent", "", 0)
	prun, _ := db.getRun(parent)
	kid, err := ag.startChild(prun, Config{Subagents: true}, "work", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.setStatus(kid, statusRunning, 0, "", "")
	if _, err := db.sql.Exec(`UPDATE runs SET cancel_req=? WHERE id=?`, now(), kid); err != nil {
		t.Fatal(err)
	}

	// (2) idle with the user's message unanswered: a dispatch dropped before
	// the durable park existed.
	dropped, _ := db.createRun("dropped prompt", "", 0)
	if _, err := db.addMessage(&Message{RunID: dropped, Role: "user", Content: "are you there?"}); err != nil {
		t.Fatal(err)
	}
	_ = db.setStatus(dropped, statusIdle, 0, "", "")

	// (3) genuinely idle, waiting on the human — must NOT be touched.
	answered, _ := db.createRun("answered", "", 0)
	_, _ = db.addMessage(&Message{RunID: answered, Role: "user", Content: "hi"})
	_, _ = db.addMessage(&Message{RunID: answered, Role: "assistant", Content: "hello"})
	_ = db.setStatus(answered, statusIdle, 0, "", "")

	for _, id := range []int64{kid, dropped} {
		if has(ids(t, db), id) {
			t.Fatalf("setup: run %d should be unreachable before recovery", id)
		}
	}
	ag.recoverStrandedRuns()

	k, _ := db.getRun(kid)
	if k.SettledAt == 0 {
		t.Fatalf("the stranded cancelled run was not settled (status %q)", k.Status)
	}
	if db.openDepCount(parent) != 0 {
		t.Fatal("its waiter is still blocked")
	}
	if !reachable(t, db, dropped) {
		d, _ := db.getRun(dropped)
		t.Fatalf("the dropped prompt was not requeued (status %q)", d.Status)
	}
	a, _ := db.getRun(answered)
	if a.Status != statusIdle {
		t.Fatalf("a run genuinely waiting on the human was requeued as %q — it would answer itself", a.Status)
	}
}

// TestRecoveryLeavesLiveRunsAlone — a run a drive currently owns must never be
// settled or requeued underneath it.
func TestRecoveryLeavesLiveRunsAlone(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	id, _ := db.createRun("live", "", 0)
	_ = db.setStatus(id, statusRunning, 0, "", "")
	if _, err := db.sql.Exec(`UPDATE runs SET cancel_req=? WHERE id=?`, now(), id); err != nil {
		t.Fatal(err)
	}
	if ok, err := db.claimLease(id, ag.gen, leaseTTL); err != nil || !ok {
		t.Fatalf("setup: %v %v", ok, err)
	}
	ag.recoverStrandedRuns()
	r, _ := db.getRun(id)
	if r.SettledAt != 0 {
		t.Fatal("a run held by a live drive was settled underneath it")
	}
}

// TestDeleteRunTakesItsEdgesAndBudget — deleteOneRun enumerates its tables by
// hand and missed both workflow tables, so deleting a run left dependency edges
// pointing at nothing and a spawn budget for a tree that no longer exists.
func TestDeleteRunTakesItsEdgesAndBudget(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	run, _ := db.getRun(root)
	a, err := ag.startChild(run, Config{Subagents: true}, "a", "", "", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.startChild(run, Config{Subagents: true}, "b", "", "", []int64{a}, ""); err != nil {
		t.Fatal(err)
	}
	count := func(q string, args ...any) int {
		var n int
		if err := db.sql.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(`SELECT count(*) FROM run_deps`) == 0 || count(`SELECT count(*) FROM run_trees WHERE root_id=?`, root) != 1 {
		t.Fatal("setup: expected edges and a tree budget")
	}
	if err := db.deleteRun(root); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM run_deps`); n != 0 {
		t.Fatalf("%d dependency edge(s) survived deleting the tree", n)
	}
	if n := count(`SELECT count(*) FROM run_trees WHERE root_id=?`, root); n != 0 {
		t.Fatal("the tree's spawn budget survived deleting its root")
	}
}

// TestScheduleIsRefusedBelowTheRoot — the tool is hidden from subagents, but a
// hallucinated call must be refused at execution too.
func TestScheduleIsRefusedBelowTheRoot(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	root, _ := db.createRun("root", "", 0)
	kid, _ := db.createRun("kid", "", root)
	krun, _ := db.getRun(kid)
	for _, name := range []string{"schedule", "unschedule"} {
		_, err := ag.runTool(context.Background(), krun, Config{}, name,
			map[string]any{"cron": "@every 1h", "goal": "spend forever", "id": float64(1)})
		if err == nil || !strings.Contains(err.Error(), "top-level") {
			t.Fatalf("%s from a subagent should be refused, got %v", name, err)
		}
	}
}
