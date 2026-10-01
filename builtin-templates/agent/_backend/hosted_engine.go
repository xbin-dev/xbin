// hosted_engine.go — the host's engine: a person's partition driving the
// non-secure conversations it hosts (hosted.go), with the same engine code as
// its own (engine.go, actor.go) over `team` (team_runs.go):
//
//   - its own lock, "<team>.engine.<partition id>", and its own epoch in
//     team's settings ("engine_epoch.<partition id>"): the fencing of D81,
//     per host — a blue/green successor of the same partition takes over
//     from its predecessor exactly as the main engine does, and no host's
//     takeover touches another's;
//   - its scope: a run it may take up is one whose conversation this
//     partition's own hosted table lists as active and whose audience is
//     within what the host confirmed (hostDrives, at every pass). A row in
//     team — any host column, any inbox row, any status — can't make it
//     drive anything else;
//   - its live events go to the global instance (the forwarder below): one
//     batched POST /hosted/events at a time, drafts coalesced, which global
//     fans out to the members' streams (90 §I4). A batch global doesn't take
//     becomes partition mail hosted/changed (durable: global re-reads team).
//
// It shares the partition's model-call gate, tools and blob store; it is
// started when the partition first hosts something (and at every start of
// a partition that hosts), and stops with the main engine (main.go).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// hostEngine is this partition's host engine (nil: it hosts nothing, or
// isn't a person's partition).
var (
	hostEngine atomic.Pointer[Engine]
	hostMu     sync.Mutex // one start at a time
)

// hostKey names this partition in team's host locks and epochs: its
// partition id (learned from the first call, kept in settings — hosting
// starts from a call), else a digest of its person.
func hostKey() string {
	if id := partitionID(); id != "" {
		return id
	}
	h := sha256.Sum256([]byte("xbin-agent-host\x00" + runUser))
	return "h-" + hex.EncodeToString(h[:12])
}

// ensureHostEngine starts the host engine if it isn't running (nil: team
// isn't usable here, or this isn't a person's partition).
func ensureHostEngine() *Engine {
	if e := hostEngine.Load(); e != nil {
		return e
	}
	if !userMode() || agent == nil || agent.eng == nil {
		return nil
	}
	hostMu.Lock()
	defer hostMu.Unlock()
	if e := hostEngine.Load(); e != nil {
		return e
	}
	t := teamStore.Load()
	tr := teamRuns()
	if tr == nil {
		return nil
	}
	main := agent.eng
	hostAg := &Agent{db: tr, repl: agent.repl, toolSem: agent.toolSem, blobs: agent.blobs, blobCache: agent.blobCache,
		noGateway: agent.noGateway}
	lock := ""
	if t.path != "" && t.path != ":memory:" {
		lock = t.path + ".engine." + hostKey()
	}
	e := newEngine(tr, hostAg, main.llm, lock)
	e.epochKey = "engine_epoch." + hostKey()
	e.scope = func(id int64) bool { return hostDrives(tr, id) }
	e.wake = func() { hostedWakeUp(hostAg, tr) }
	e.keep = agent     // the partition's way back is its main agent's to keep (resume_keep.go)
	e.gate = main.gate // one model-call gate per partition (PD-36)
	e.hold.open = main.hold.open
	e.hub.tap = hostedFwd.add
	hostEngine.Store(e)
	expireHostPauses()
	e.Start()
	armHostPauses()
	return e
}

// startHosting (main.go, after the main engine starts), once team is open:
// a person's partition takes up the moves it asked for before a stop
// (hosted_move.go) and starts its host engine if it hosts something; the
// global instance settles moves that never finished.
func startHosting() {
	if agent == nil {
		return
	}
	global := globalMode() && partitioned()
	if !global && (!userMode() || (!agent.db.hostsAny() && !agent.db.hostingMovesWait())) {
		return
	}
	deadline := time.Now().Add(teamWait + 5*time.Second) // team opens in the background (partition_start.go)
	for teamRuns() == nil {
		if time.Now().After(deadline) {
			if !global {
				logf("hosting: the team database isn't usable — hosted conversations wait for the next start")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	if global {
		settleHostedMoves(teamView())
		return
	}
	reconcileHostingMoves()
	if agent.db.hostsAny() {
		ensureHostEngine()
	}
}

// stopHosting stops the host engine with the main one (main.go): at once
// (wait 0), or waiting up to wait for its actors and leaving its wake-up —
// and what its forwarder still holds goes to the global instance (or, if
// that fails, as hosted/changed mail).
func stopHosting(wait time.Duration) {
	e := hostEngine.Load()
	if e == nil {
		return
	}
	if wait == 0 {
		e.BeginShutdown()
		return
	}
	e.Shutdown(wait)
	hostedFwd.flush()
}

// haltHosted abandons what the host's engine is doing in a conversation
// (paused or dropped) as a handoff abandons it: a model call in flight writes
// nothing (the run stays running — made again once confirmed), a tool call
// in flight ends as after a restart; the pass that follows finds it out of
// scope.
func haltHosted(root int64) {
	e := hostEngine.Load()
	if e == nil {
		return
	}
	ids := []int64{root}
	if kids, err := e.db.descendants(root); err == nil {
		ids = append(ids, kids...)
	}
	for _, id := range ids {
		e.Signal(id, errHandoff)
	}
}

// hostedWakeUp is the host engine's exit (Engine.wake): what the
// conversations it drives (active in its own table) wait for leaves the
// partition's wake-up, by resume_mode.go's rule (08 §9 C4) — `resume` for
// work a pass would take up now, else the `wake` job at the earliest timed
// wait (the earlier of its own and the main engine's: they share the name);
// a paused one waits for its host, who opens the tile anyway.
func hostedWakeUp(ag *Agent, tr *DB) {
	if brakeIdle() {
		return
	}
	roots := scanIDs(agent.db.q.Query(`SELECT conversation FROM hosted WHERE state='active'`))
	if len(roots) == 0 {
		return
	}
	switch at := tr.hostedWake(roots, time.Now()); {
	case at.runnable:
		ag.registerResumeJob()
	case at.wake > 0:
		if own := agent.db.userWake(time.Now()); !own.runnable && own.wake > 0 && own.wake < at.wake {
			at.wake = own.wake
		}
		ag.registerWakeJob(at.wake)
	}
}

// hostedWake is DB.userWake (resume_mode.go) over the trees of roots only.
func (d *DB) hostedWake(roots []int64, now time.Time) userWakeAt {
	in := strings.TrimSuffix(strings.Repeat("?,", len(roots)), ",")
	args := make([]any, 0, 2*len(roots))
	for _, id := range roots {
		args = append(args, id)
	}
	for _, id := range roots {
		args = append(args, id)
	}
	tree := func(alias string) string {
		return "(" + alias + ".id IN (" + in + ") OR " + alias + ".root_id IN (" + in + "))"
	}
	var n int
	_ = d.q.QueryRow(`SELECT
		(SELECT count(*) FROM runs r WHERE r.status IN ('running','queued') AND `+tree("r")+`)
		+ (SELECT count(*) FROM inbox i JOIN runs r ON r.id = i.run_id WHERE i.delivered_at=0 AND `+tree("r")+`)
		+ (SELECT count(*) FROM links l JOIN runs p ON p.id = l.parent_id
			WHERE l.state<>'running' AND l.delivered=0 AND `+tree("p")+` AND (
				(l.mode='fg' AND p.status IN ('running','queued','awaiting'))
				OR (l.mode='bg' AND p.parent_id=0 AND p.status IN ('running','queued','sleeping','idle','done','canceled'))))`,
		append(append(append([]any{}, args...), args...), args...)...).Scan(&n)
	if n > 0 {
		return userWakeAt{runnable: true}
	}
	rows, err := d.q.Query(`SELECT r.pending FROM runs r WHERE r.status='sleeping' AND r.pending<>'' AND `+tree("r"), args...)
	if err == nil {
		sleeps := false
		for rows.Next() {
			var p string
			if rows.Scan(&p) == nil && parsePending(p).Kind == "sleep" {
				sleeps = true
			}
		}
		rows.Close()
		if sleeps {
			return userWakeAt{runnable: true} // a run sleeping on a sandbox job: seen only by looking
		}
	}
	var wake int64
	_ = d.q.QueryRow(`SELECT COALESCE(min(r.wake_at), 0) FROM runs r WHERE r.status IN ('sleeping','awaiting') AND r.wake_at > 0 AND `+tree("r"), args...).Scan(&wake)
	switch {
	case wake == 0:
		return userWakeAt{}
	case wake <= now.Unix()+60:
		return userWakeAt{runnable: true}
	}
	return userWakeAt{wake: wake}
}

// armHostPauses times the drop of every paused conversation (hostPauseTTL
// after its pause) while the partition runs.
func armHostPauses() {
	for _, row := range agent.db.hostedRows() {
		if row.State != hostPaused || row.PausedAt == 0 {
			continue
		}
		d := time.Until(time.Unix(row.PausedAt, 0).Add(hostPauseTTL))
		time.AfterFunc(max(d, 0)+time.Second, expireHostPauses)
	}
}

// --- the forwarder: the host's run, live, to the global instance ------------------

// fwdEvent is one event as the host posts it (POST /hosted/events).
type fwdEvent struct {
	Type string `json:"type"`
	Run  int64  `json:"run"`
	Root int64  `json:"root"`
	Key  string `json:"key,omitempty"`
	Data any    `json:"data,omitempty"`
}

// hostedForwarder batches the host engine's events for the global instance.
type hostedForwarder struct {
	mu      sync.Mutex
	q       []fwdEvent
	pos     map[string]int // a draft's key → its place in q (coalesced: the latest wins)
	kick    chan struct{}
	running bool
	post    func(ctx context.Context, body []byte) error // nil: callGlobal
}

var hostedFwd = &hostedForwarder{pos: map[string]int{}, kick: make(chan struct{}, 1)}

// fwdWindow is how long the forwarder gathers events before a post.
var fwdWindow = 20 * time.Millisecond

// add is the host hub's tap.
func (f *hostedForwarder) add(ev *Event) {
	switch ev.Type {
	case evReset, evBye, evRevoked, evUState:
		return
	}
	fe := fwdEvent{Type: ev.Type, Run: ev.Run, Root: ev.Root, Key: ev.key, Data: ev.Data}
	f.mu.Lock()
	if i, ok := f.pos[ev.key]; ok && ev.key != "" {
		f.q[i] = fe
	} else {
		if ev.key != "" {
			f.pos[ev.key] = len(f.q)
		} else {
			clear(f.pos) // a draft after a commit (a draft's end, a message) goes after it, never before
		}
		f.q = append(f.q, fe)
	}
	start := !f.running
	f.running = true
	f.mu.Unlock()
	if start {
		go f.loop()
	}
	select {
	case f.kick <- struct{}{}:
	default:
	}
}

// changed says a hosted conversation's state changed (paused, confirmed,
// dropped): global re-reads it from team.
func hostedChangedAtGlobal(root int64) {
	hostedFwd.add(&Event{Type: "hosted", Run: root, Root: root})
}

// flush sends what is queued now, once (the process is stopping): what
// doesn't get through is mailed.
func (f *hostedForwarder) flush() {
	f.mu.Lock()
	batch := f.q
	f.q, f.pos = nil, map[string]int{}
	f.mu.Unlock()
	if len(batch) > 0 {
		f.sendWith(batch, []time.Duration{0}, 5*time.Second)
	}
}

func (f *hostedForwarder) loop() {
	for range f.kick {
		time.Sleep(fwdWindow)
		f.mu.Lock()
		batch := f.q
		f.q, f.pos = nil, map[string]int{}
		f.mu.Unlock()
		if len(batch) > 0 {
			f.send(batch)
		}
	}
}

// send posts one batch, a few times; what global never took is mailed as
// hosted/changed per conversation (global drops its drafts and has the
// members' streams re-read it: they missed what the batch carried).
func (f *hostedForwarder) send(batch []fwdEvent) {
	f.sendWith(batch, []time.Duration{0, 200 * time.Millisecond, time.Second, 3 * time.Second}, 30*time.Second)
}

func (f *hostedForwarder) sendWith(batch []fwdEvent, tries []time.Duration, mailTimeout time.Duration) {
	body, err := json.Marshal(map[string]any{"events": batch})
	if err != nil {
		logf("hosted events: %v", err)
		return
	}
	f.mu.Lock()
	post := f.post
	f.mu.Unlock()
	if post == nil {
		post = postHostedEvents
	}
	for i, wait := range tries {
		if wait > 0 {
			time.Sleep(wait)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = post(ctx, body)
		cancel()
		if err == nil {
			return
		}
		logf("hosted events to the shared instance (try %d): %v", i+1, err)
	}
	roots := map[int64]bool{}
	for _, ev := range batch {
		roots[ev.Root] = true
	}
	for root := range roots {
		ctx, cancel := context.WithTimeout(context.Background(), mailTimeout)
		if _, err := sendMail(ctx, "global", topicHostedChanged, map[string]any{"conversation": root}, ""); err != nil {
			logf("hosted/changed for #%d: %v", root, err)
		}
		cancel()
	}
}

// postHostedEvents is the production post (F5: attributed to this
// partition's person at the global instance).
func postHostedEvents(ctx context.Context, body []byte) error {
	res, err := callGlobal(ctx, http.MethodPost, "/hosted/events", body, "application/json")
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK {
		return gwErr("POST /hosted/events", res)
	}
	return nil
}
