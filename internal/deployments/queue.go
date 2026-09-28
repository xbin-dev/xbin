package deployments

// queue.go — deploys (07-runtime §8.3, §8.4). Operations on one deployment
// serialize: one deploy in flight, a FIFO of at most eight behind it, a
// request whose checkpoint equals the queue's tail merged into it. Each
// accepted request is an attempt with a per-tile id, kept in the tile's
// deploy journal, data/deployments/<TileKey>/pending.json, from acceptance
// until its entry is in the deploy log (16-open-questions Q3): at boot every
// attempt still there is logged — queued as cancelled, swapped as ok,
// running as failed with the reason "interrupted", naming the code the
// record now runs — so no attempt is lost to a crash (SC-AUDIT). Finished
// attempts are also kept in memory, for the log's readers while the store's
// log can't answer.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	maxWaiting    = 8   // queued deploys behind the one in flight, per deployment
	recentKept    = 50  // finished attempts kept in memory, per tile
	journalMax    = 200 // attempts the journal keeps, per tile, while the log can't take them
	maxErrorBytes = 500 // an attempt's error, as the log and the answers carry it
	journalSchema = 1
)

// The results of an attempt (11-contract §1.1).
const (
	resultQueued    = "queued"
	resultRunning   = "running"
	resultOK        = "ok"
	resultFailed    = "failed"
	resultCancelled = "cancelled"
)

// pointerRequest marks an attempt whose record pointer was written at
// request time: a move off the work tree (07-runtime §8.2).
const pointerRequest = "request"

// attempt is one deploy attempt: queued, running or finished. The journal
// and the deploy log keep it in this shape; DeployEntry is its answer.
// Every field but the immutable ones (ID, Tile, Deployment, How, From,
// Tree, Previous, Pointer, g) is read and written under queue.mu.
type attempt struct {
	ID              int64  `json:"id"`
	Deployment      string `json:"deployment"`
	How             string `json:"how"`
	From            string `json:"from,omitempty"`
	Tree            string `json:"checkpoint,omitempty"` // full tree id
	Previous        string `json:"previous,omitempty"`   // full tree id; "" = the work tree
	Feed            string `json:"feed,omitempty"`
	FollowsWorkTree bool   `json:"followsWorkTree,omitempty"`
	By              string `json:"by"`
	Via             string `json:"via,omitempty"`
	Agent           bool   `json:"agent,omitempty"`
	Session         string `json:"session,omitempty"`
	RequestedAt     string `json:"requestedAt"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	Result          string `json:"result"`
	Phase           string `json:"phase,omitempty"`
	Error           string `json:"error,omitempty"`
	// Pointer is pointerRequest when the record's pointer was written at
	// request time; Swapped, when the swap committed it.
	Pointer string `json:"pointer,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`

	tile   string
	g      Grant
	forced bool          // restart:true: a new generation of the current code (Restart)
	done   chan struct{} // closed when it finishes
}

// restarter is the runner's Restart where the runner has one: a new
// generation of a deployment's current code, committing nothing.
type restarter interface {
	Restart(ctx context.Context, c *registry.Component, dep string, progress runner.DeployProgress) error
}

func (a *attempt) finished() bool {
	return a.Result != resultQueued && a.Result != resultRunning
}

// identical reports whether a ships the files its deployment already served,
// so its swap announces no reload (07-runtime §8.5; 11-contract §3.5): a
// pause, or a pin in place, attach's (pinAttempt: the old live reload
// target) or protect's (a primary that followed the work tree), each a
// capture of the work tree the deployment followed. A forced protect
// rebuild restarts the code already running.
func (a *attempt) identical() bool {
	return a.How == "pause" || a.How == "attach" || a.How == "protect"
}

// queue is the plane's deploys. The zero value is ready.
type queue struct {
	mu    sync.Mutex
	lanes map[string]*lane         // by laneKey(tile, dep)
	tiles map[string]*tileAttempts // by tile
}

// lane is one deployment's deploys: the one in flight and those waiting.
type lane struct {
	running *attempt
	waiting []*attempt
}

// tileAttempts is one tile's attempts: the next id, the journal, the most
// recent finished ones, and each deployment's last failure.
type tileAttempts struct {
	next    int64
	open    []*attempt        // the journal: accepted and not yet in the deploy log
	recent  []*attempt        // finished, oldest first
	lastErr map[string]string // by deployment: the last failed attempt's error
	serving map[string]string // by deployment: the checkpoint its last swap put there
}

func laneKey(tile, dep string) string { return tile + "\x00" + dep }

// journal is the file's shape.
type journal struct {
	Schema   int        `json:"schema"`
	Tile     string     `json:"tile"`
	Next     int64      `json:"next"`
	Attempts []*attempt `json:"attempts"`
}

// journalDir holds a tile's journal beside its record (11-contract §10.2's
// per-tile directory).
func journalDir(root, tile string) string {
	return filepath.Join(recordDir(root), util.TileKey(tile))
}

const journalFile = "pending.json"

// attemptsLocked is tile's attempts, loaded on first use: the journal's
// next id, never below the record's nextDeploy or past the deploy log's
// newest id (a tile opting in again keeps counting).
func (p *Plane) attemptsLocked(ctx context.Context, tile string, rec *Record) *tileAttempts {
	if t := p.q.tiles[tile]; t != nil {
		return t
	}
	t := &tileAttempts{next: 1, lastErr: map[string]string{}, serving: map[string]string{}}
	if j, err := p.readJournal(tile); err == nil && j != nil {
		t.next = max(t.next, j.Next)
		t.open = j.Attempts
		for _, a := range t.open {
			a.tile = tile
			if a.done == nil {
				a.done = make(chan struct{})
				if a.finished() {
					close(a.done)
				}
			}
		}
	}
	if rec != nil {
		t.next = max(t.next, rec.NextDeploy)
	}
	if logged, err := p.store().ReadLog(ctx, tile, ""); err == nil {
		for _, a := range logged {
			t.next = max(t.next, a.ID+1)
		}
	}
	for _, a := range t.open {
		t.next = max(t.next, a.ID+1)
	}
	if p.q.tiles == nil {
		p.q.tiles = map[string]*tileAttempts{}
	}
	p.q.tiles[tile] = t
	return t
}

// readJournal reads tile's journal: nil without one. The file is xbind's
// own, opened beneath data/deployments.
func (p *Plane) readJournal(tile string) (*journal, error) {
	data, err := readRecordFile(journalDir(p.Root, tile), journalFile)
	if err != nil {
		return nil, err
	}
	var j journal
	if err := json.Unmarshal(data, &j); err != nil {
		return nil, fmt.Errorf("%s: the deploy journal doesn't parse: %w", tile, err)
	}
	if j.Tile != tile {
		return nil, fmt.Errorf("%s: the deploy journal names %q", tile, j.Tile)
	}
	return &j, nil
}

// writeJournalLocked writes tile's journal while a record governs the tile;
// a tile without one (an opt-in before its commit, a tile that opted out)
// has no journal.
func (p *Plane) writeJournalLocked(tile string, t *tileAttempts) error {
	if rec, _ := p.record(tile); rec == nil {
		return nil
	}
	if len(t.open) > journalMax {
		slog.Warn("deployments: the deploy journal is full; its oldest attempts are dropped unlogged", "tile", tile, "dropped", len(t.open)-journalMax)
		t.open = t.open[len(t.open)-journalMax:]
	}
	data, err := json.MarshalIndent(journal{Schema: journalSchema, Tile: tile, Next: t.next, Attempts: t.open}, "", "  ")
	if err != nil {
		return err
	}
	if err := p.idx.writeIn(filepath.Join(journalDir(p.Root, tile), journalFile), append(data, '\n')); err != nil {
		return fmt.Errorf("%s: writing the deploy journal: %w", tile, err)
	}
	return nil
}

// syncJournal writes tile's journal as it stands (after an opt-in's commit).
func (p *Plane) syncJournal(tile string) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[tile]; t != nil {
		if err := p.writeJournalLocked(tile, t); err != nil {
			slog.Warn("deployments: "+err.Error(), "tile", tile)
		}
	}
}

// accept gives a its id and journals it as queued, before the operation
// commits anything or answers.
func (p *Plane) accept(ctx context.Context, tile string, rec *Record, a *attempt) error {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	t := p.attemptsLocked(ctx, tile, rec)
	a.tile, a.ID = tile, t.next
	a.Result, a.RequestedAt, a.done = resultQueued, p.stamp(), make(chan struct{})
	t.next++
	t.open = append(t.open, a)
	if err := p.writeJournalLocked(tile, t); err != nil {
		t.open = t.open[:len(t.open)-1]
		return err
	}
	return nil
}

// discard drops an accepted attempt whose operation didn't commit: it never
// happened (its id stays used).
func (p *Plane) discard(a *attempt) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	t := p.q.tiles[a.tile]
	if t == nil {
		return
	}
	t.open = slicesDelete(t.open, a)
	if err := p.writeJournalLocked(a.tile, t); err != nil {
		slog.Warn("deployments: "+err.Error(), "tile", a.tile)
	}
}

func slicesDelete(s []*attempt, a *attempt) []*attempt {
	out := s[:0]
	for _, x := range s {
		if x != a {
			out = append(out, x)
		}
	}
	return out
}

// tail is the attempt a new request for (tile, dep) would merge into: the
// last one waiting, else the one in flight; nil when the lane is idle.
func (p *Plane) tail(tile, dep string) *attempt {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	l := p.q.lanes[laneKey(tile, dep)]
	switch {
	case l == nil:
		return nil
	case len(l.waiting) > 0:
		return l.waiting[len(l.waiting)-1]
	case l.running != nil && !l.running.finished():
		return l.running
	}
	return nil
}

// full reports whether (tile, dep) already has maxWaiting deploys waiting.
func (p *Plane) full(tile, dep string) bool {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	l := p.q.lanes[laneKey(tile, dep)]
	return l != nil && len(l.waiting) >= maxWaiting
}

// enqueue puts an accepted attempt on its deployment's lane and starts the
// lane's worker when it is idle.
func (p *Plane) enqueue(a *attempt) {
	p.q.mu.Lock()
	if p.q.lanes == nil {
		p.q.lanes = map[string]*lane{}
	}
	key := laneKey(a.tile, a.Deployment)
	l := p.q.lanes[key]
	idle := l == nil
	if idle {
		l = &lane{}
		p.q.lanes[key] = l
	}
	l.waiting = append(l.waiting, a)
	p.q.mu.Unlock()
	if idle {
		go p.drain(key)
	}
}

// drain runs a lane's attempts one at a time until it is empty.
func (p *Plane) drain(key string) {
	for {
		p.q.mu.Lock()
		l := p.q.lanes[key]
		if len(l.waiting) == 0 {
			delete(p.q.lanes, key)
			p.q.mu.Unlock()
			return
		}
		a := l.waiting[0]
		l.waiting = l.waiting[1:]
		l.running = a
		a.Result, a.Phase = resultRunning, "build"
		if t := p.q.tiles[a.tile]; t != nil {
			if err := p.writeJournalLocked(a.tile, t); err != nil {
				slog.Warn("deployments: "+err.Error(), "tile", a.tile)
			}
		}
		p.q.mu.Unlock()
		p.publishDeploy(a)
		p.runAttempt(a)
		p.q.mu.Lock()
		l.running = nil
		p.q.mu.Unlock()
	}
}

// runAttempt deploys a's checkpoint through the runner: build, start, health
// check, swap, commitSwap, drain. Its result is the runner's.
func (p *Plane) runAttempt(a *attempt) {
	c, ok := p.component(a.tile)
	switch {
	case !ok:
		p.finish(a, resultCancelled, fmt.Errorf("%s is no longer a tile", a.tile))
		return
	case p.Run == nil:
		p.finish(a, resultFailed, errors.New("no runner to deploy with"))
		return
	}
	commit := func() error { return p.commitSwap(a) }
	progress := func(phase, result string, err error) { p.progress(a, phase, result) }
	var err error
	if rs, ok := p.Run.(restarter); ok && a.forced {
		err = rs.Restart(context.Background(), c, a.Deployment, progress)
	} else {
		code := runner.Code{Tree: a.Tree, Identical: a.identical()}
		err = p.Run.Deploy(context.Background(), c, a.Deployment, code, commit, progress)
	}
	if err != nil {
		p.finish(a, resultFailed, err)
		return
	}
	p.finish(a, resultOK, nil)
}

// progress records a runner phase of a running attempt and announces it.
func (p *Plane) progress(a *attempt, phase, result string) {
	if phase == "" || (result != "" && result != resultRunning) {
		return // the result is Deploy's to report
	}
	p.q.mu.Lock()
	if a.finished() || a.Phase == phase {
		p.q.mu.Unlock()
		return
	}
	a.Phase = phase
	p.q.mu.Unlock()
	p.publishDeploy(a)
}

// finish ends an attempt: its result in the journal, the event, the waiters
// released, then its entry in the deploy log (off the critical path), after
// which the journal drops it. A failed move off the work tree marks the
// record's pointer failed (D119e): restarts retry that checkpoint.
func (p *Plane) finish(a *attempt, result string, err error) {
	p.q.mu.Lock()
	done := a.finished()
	p.q.mu.Unlock()
	if done {
		return
	}
	if result == resultFailed && a.Pointer == pointerRequest {
		p.markFailed(a) // before anyone waiting on the attempt reads the record
	}
	p.q.mu.Lock()
	if a.finished() {
		p.q.mu.Unlock()
		return
	}
	a.Result, a.FinishedAt = result, p.stamp()
	if err != nil {
		a.Error = clip(err.Error(), maxErrorBytes)
	}
	t := p.q.tiles[a.tile]
	if t != nil {
		t.recent = append(t.recent, a)
		if len(t.recent) > recentKept {
			t.recent = t.recent[len(t.recent)-recentKept:]
		}
		switch result {
		case resultFailed:
			t.lastErr[a.Deployment] = a.Error
		case resultOK:
			delete(t.lastErr, a.Deployment)
		}
		if werr := p.writeJournalLocked(a.tile, t); werr != nil {
			slog.Warn("deployments: "+werr.Error(), "tile", a.tile)
		}
	}
	p.q.mu.Unlock()
	p.publishDeploy(a)
	p.logAttempt(a)
	p.retainAfter(a.tile, result) // GC and artifact pruning (retention.go)
	p.q.mu.Lock()
	close(a.done)
	p.q.mu.Unlock()
}

// markFailed sets the failed state on the record's pointer when it still
// names a's checkpoint: the attempted code of a failed move off the work
// tree, which every restart runs (D119e).
func (p *Plane) markFailed(a *attempt) {
	if p.idx == nil {
		return
	}
	_, err := p.idx.commit(a.tile, -1, func(r *Record) error {
		d := r.Deployments[a.Deployment]
		if d == nil || d.Checkpoint == nil || *d.Checkpoint != a.Tree || d.State == "failed" {
			return errNoChange
		}
		d.State = "failed"
		return nil
	})
	if err != nil && !errors.Is(err, errNoChange) && !errors.Is(err, ErrRecordInert) {
		slog.Warn("deployments: marking a failed move off the work tree", "tile", a.tile, "err", err)
	}
}

// errNoChange ends a record change that has nothing to change.
var errNoChange = errors.New("no change")

// logAttempt writes a finished attempt's deploy-log entry, then drops it
// from the journal. While the log can't take it, the journal keeps it and
// the next boot writes it.
func (p *Plane) logAttempt(a *attempt) {
	p.q.mu.Lock()
	entry := *a
	p.q.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := p.store().AppendLog(ctx, a.tile, entry); err != nil {
		if !isNotBuilt(err) {
			slog.Warn("deployments: writing a deploy-log entry; the journal keeps it", "tile", a.tile, "id", a.ID, "err", err)
		}
		return
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[a.tile]; t != nil {
		t.open = slicesDelete(t.open, a)
		if err := p.writeJournalLocked(a.tile, t); err != nil {
			slog.Warn("deployments: "+err.Error(), "tile", a.tile)
		}
	}
}

func isNotBuilt(err error) bool {
	var nb errNotBuilt
	return errors.As(err, &nb)
}

// cancelWaiting ends (tile, dep)'s waiting deploys cancelled: live reload
// drives the deployment now, so no checkpoint may land on it.
func (p *Plane) cancelWaiting(tile, dep, why string) {
	p.q.mu.Lock()
	var gone []*attempt
	if l := p.q.lanes[laneKey(tile, dep)]; l != nil {
		gone, l.waiting = l.waiting, nil
	}
	p.q.mu.Unlock()
	for _, a := range gone {
		p.finish(a, resultCancelled, errors.New(why))
	}
}

// forget drops everything the plane keeps in memory for tile's attempts:
// the tile opted out, and its journal is gone with the record. An attempt
// in flight finishes on its own and journals nothing.
func (p *Plane) forget(tile string) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	delete(p.q.tiles, tile)
}

// startAttempt runs an accepted attempt: queued behind dep's deploys for a
// backend, committed at once for a static tile, which has no process to
// swap (static tiles announce their reload after the record commits).
func (p *Plane) startAttempt(o *op, a *attempt) {
	if o.c.HasBackend() {
		p.enqueue(a)
		return
	}
	p.q.mu.Lock()
	a.Result, a.Phase = resultRunning, "swap"
	p.q.mu.Unlock()
	if err := p.commitSwap(a); err != nil {
		p.finish(a, resultFailed, err)
		return
	}
	p.finish(a, resultOK, nil)
	if !a.identical() && a.Previous != a.Tree { // a pause or a pin ships the code already served
		p.publishReload(o.tile, a.Deployment)
	}
}

// commitSwap writes a's pointer once the runner swapped dep onto a's code
// (a static tile: at once), with the grant judged again against the record
// it changes (T9): a pinned → pinned move sets the checkpoint; a move off
// the work tree, whose pointer the request wrote, only clears a failed
// state. A deployment that moved meanwhile (live reload attached to it, the
// record gone) refuses the commit.
func (p *Plane) commitSwap(a *attempt) error {
	if p.idx == nil {
		return errors.New("deployments: the plane isn't booted")
	}
	// The primary's new code is prepared before its pointer moves, so the
	// registry composes the tile from it the moment it does.
	if a.Deployment == p.Primary(a.tile) && !p.prep.has(a.tile, a.Tree) {
		if _, err := p.prepare(a.tile, a.Tree); err != nil {
			return err
		}
	}
	primary := false
	rec, err := p.idx.commit(a.tile, -1, func(r *Record) error {
		if err := p.Recheck(a.g, subjectFrom(a.tile, a.Deployment, r)); err != nil {
			return err
		}
		d := r.Deployments[a.Deployment]
		switch {
		case d == nil:
			return util.NoDeployment(a.tile, a.Deployment)
		case d.Checkpoint == nil:
			return moved(a.tile)
		case a.Pointer == pointerRequest && *d.Checkpoint != a.Tree:
			return moved(a.tile)
		}
		primary = a.Deployment == r.Primary
		if *d.Checkpoint == a.Tree && d.State == "" {
			return errNoChange
		}
		d.Checkpoint, d.State = &a.Tree, ""
		r.NextDeploy = max(r.NextDeploy, a.ID+1)
		return nil
	})
	switch {
	case errors.Is(err, errNoChange): // the pointer the request wrote, already clear
	case err != nil:
		return err
	default:
		p.publishRecord(a.tile, rec, a.By, []string{"deployments"})
		if primary {
			p.prep.keep(a.tile, a.Tree)
			p.primaryCodeMoved()
		}
		p.syncView(context.Background(), a.tile, rec)
	}
	p.q.mu.Lock()
	a.Swapped, a.Phase = true, "swap"
	if t := p.q.tiles[a.tile]; t != nil {
		if werr := p.writeJournalLocked(a.tile, t); werr != nil {
			warn("journaling a swap", a.tile, werr)
		}
	}
	p.q.mu.Unlock()
	p.setServing(a.tile, a.Deployment, a.Tree)
	p.publishDeploy(a)
	return nil
}

// ---- boot: the journal into the deploy log ----

// reconcileJournal logs every attempt tile's journal still holds, as a crash
// left it, then drops the logged ones from the journal: queued as cancelled,
// swapped as ok, running as failed ("interrupted") naming the checkpoint the
// record runs now — the attempted one when the pointer was written at
// request time, the previous one otherwise. It never touches the record
// (PO-8).
func (p *Plane) reconcileJournal(ctx context.Context, tile string, rec *Record) {
	p.q.mu.Lock()
	t := p.attemptsLocked(ctx, tile, rec)
	var pending []*attempt
	for _, a := range t.open {
		if !a.finished() {
			switch {
			case a.Result == resultQueued:
				a.Result, a.Error = resultCancelled, "cancelled: xbind restarted before its turn"
			case a.Swapped:
				a.Result = resultOK
			default:
				now := "the work tree"
				if d := rec.Deployments[a.Deployment]; d != nil && d.Checkpoint != nil {
					now = "checkpoint " + shortTree(*d.Checkpoint)
				}
				a.Result, a.Error = resultFailed, fmt.Sprintf("interrupted: xbind restarted mid-deploy; %s runs %s", a.Deployment, now)
			}
			a.FinishedAt = p.stamp()
			close(a.done)
		}
		t.recent = append(t.recent, a)
		pending = append(pending, a)
	}
	if len(t.recent) > recentKept {
		t.recent = t.recent[len(t.recent)-recentKept:]
	}
	if len(pending) > 0 {
		if err := p.writeJournalLocked(tile, t); err != nil {
			slog.Warn("deployments: "+err.Error(), "tile", tile)
		}
	}
	p.q.mu.Unlock()
	for _, a := range pending {
		p.logAttempt(a)
	}
}

// laneOf is (tile, dep)'s attempt in flight and those waiting, copied.
func (p *Plane) laneOf(tile, dep string) (running *attempt, waiting []attempt) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	l := p.q.lanes[laneKey(tile, dep)]
	if l == nil {
		return nil, nil
	}
	if l.running != nil && !l.running.finished() {
		c := *l.running
		running = &c
	}
	for _, a := range l.waiting {
		waiting = append(waiting, *a)
	}
	return running, waiting
}

// runtimeOf is dep's last failure and the checkpoint its last swap put
// there, as far as this xbind has seen since it started.
func (p *Plane) runtimeOf(tile, dep string) (lastErr, serving string) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[tile]; t != nil {
		return t.lastErr[dep], t.serving[dep]
	}
	return "", ""
}

// setServing records the checkpoint a swap put on dep.
func (p *Plane) setServing(tile, dep, tree string) {
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[tile]; t != nil {
		t.serving[dep] = tree
	}
}

// component is tile's registry component.
func (p *Plane) component(tile string) (*registry.Component, bool) {
	if p.Reg == nil {
		return nil, false
	}
	return p.Reg.Component(tile)
}

// clip cuts s to at most n bytes, on a rune boundary, at its first line.
func clip(s string, n int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xc0 == 0x80 {
		n--
	}
	return s[:n]
}

// shortTree is a full tree id as answers abbreviate it without the store.
func shortTree(tree string) string {
	if len(tree) > 7 {
		tree = tree[:7]
	}
	return "c:" + tree
}

// restart starts a new generation of dep's current code (its checkpoint, or
// the work tree's build while live reload drives it) and clears its crash
// breaker: what a crash restart does, so it moves no code (D119e). A runner
// with Restart does it blue/green on dep's lane (07-runtime §8.7); one
// without marks dep changed, which rebuilds it from its record's code.
func (p *Plane) restart(ctx context.Context, o *op, dry bool, dep string) (any, error) {
	if dry {
		return p.answer(ctx, true, nil, Impact{Data: "none", Affects: affects(o.rec, dep)}, false)
	}
	d := o.rec.Deployments[dep]
	tree := ""
	if d.Checkpoint != nil {
		tree = *d.Checkpoint
	}
	a := o.newAttempt("restart", dep, tree)
	a.FollowsWorkTree, a.forced = d.Checkpoint == nil, true
	_, blue := p.Run.(restarter)
	if blue && o.c.HasBackend() && p.full(o.tile, dep) {
		return nil, queueFull(dep)
	}
	if err := p.accept(ctx, o.tile, o.rec, a); err != nil {
		return nil, opError(o.tile, err)
	}
	if blue && o.c.HasBackend() {
		p.enqueue(a) // a new generation through the runner's blue/green, on dep's lane
		return p.answer(ctx, false, a, Impact{}, false)
	}
	if p.Run != nil {
		p.Run.ChangedDeployment(o.c, dep) // what a crash restart does
	}
	p.finish(a, resultOK, nil)
	return p.answer(ctx, false, a, Impact{}, false)
}
