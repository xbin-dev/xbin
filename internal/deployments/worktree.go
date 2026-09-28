package deployments

// worktree.go — the work tree's drift while live reload is paused
// (07-runtime §2.9, §6; NP-13-12). A save in a paused tile drives no
// deployment; the watcher tells the plane instead (WorkTreeMoved), and the
// plane recounts the files the work tree differs in from the last target's
// checkpoint: one confined run that changes nothing durable, off the watcher
// loop, at most one per tile every 2 s. A count that moved is announced as
// the deployments op work-tree, and it is what State.workTree.changed
// reports (11-contract §1.1, §3.3). The count is always recomputed from the
// work tree, never summed from batches: the watcher drops batches under
// load. A tile without a record, or whose live reload is attached, or whose
// record holds it, costs nothing here (D119d): no count, no state, no timer.

import (
	"cmp"
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
)

const (
	// driftEvery is the debounce: one count per tile every 2 s.
	driftEvery = 2 * time.Second
	// driftTimeout bounds one count (its target is 150 ms, 07-runtime §13.2).
	driftTimeout = time.Minute
)

// driftFunc counts the files src's work tree differs in from the checkpoint
// with full tree id tree: the checkpoint store's drift count, one confined
// run against a scratch copy of the index and a scratch quarantine, both
// discarded (06-security L16).
type driftFunc func(ctx context.Context, src checkpoint.Source, tree string) (int, error)

// driftCounter is the plane's drift count: the checkpoint store's Drift on
// the plane's store.
func (p *Plane) driftCounter() driftFunc { return p.store().Drift }

// WorkTreeDrift is State.workTree (11-contract §1.1) for a tile whose live
// reload is paused.
type WorkTreeDrift struct {
	// Since is the full tree id of the last target's checkpoint, which the
	// count is against.
	Since string
	// Changed is the number of files the work tree differs in from Since,
	// as of the last count.
	Changed int
	// Counted is false while no count against Since has finished; Changed
	// is then 0, as it is right after the pause that captured the work tree.
	Counted bool
}

// workTreeOp is the data of the deployments op work-tree.
type workTreeOp struct {
	Op      string `json:"op"`
	Changed int    `json:"changed"`
}

// WorkTreeMoved is the watcher's notice that a batch touched tile while its
// live reload is paused: the plane recounts how far the work tree moved from
// the last target's checkpoint, debounced per tile, and announces a moved
// count. A state read that wants a fresh count (a client asking, 07-runtime
// §2.9) may call it too. A tile whose record holds it answers LiveReload
// ("", false) like a paused one and is told apart here: it, a tile without a
// record and one whose live reload is attached are never counted.
func (p *Plane) WorkTreeMoved(tile string) {
	if _, ok := p.pausedSince(tile); !ok {
		return
	}
	if aware, _ := p.Branches(tile, ""); aware {
		p.NoteBranch(tile) // a switch back offers to resume (D131)
	}
	p.workTrees().notice(tile)
}

// WorkTreeDrift answers State.workTree for tile: ok only while its live
// reload is paused. It reads memory only and never starts a count.
func (p *Plane) WorkTreeDrift(tile string) (d WorkTreeDrift, ok bool) {
	if d.Since, ok = p.pausedSince(tile); !ok {
		return WorkTreeDrift{}, false
	}
	if w, found := planeWorkTrees.Load(p); found {
		d.Changed, d.Counted = w.(*workTrees).counted(tile, d.Since)
	}
	return d, true
}

// pausedSince answers, for a tile governed by an active record whose live
// reload is paused, the full tree id of the checkpoint its last target runs.
// An in-memory lookup that allocates nothing.
func (p *Plane) pausedSince(tile string) (string, bool) {
	rec, err := p.record(tile)
	if err != nil || rec == nil || rec.LiveReload != "" {
		return "", false
	}
	d := rec.Deployments[cmp.Or(rec.LastLiveReload, rec.Primary)]
	if d == nil || d.Checkpoint == nil {
		return "", false
	}
	return *d.Checkpoint, true
}

// planeWorkTrees holds each plane's drift counts, made on the plane's first
// notice for a paused tile: a Plane is a literal whose fields are plane.go's.
var planeWorkTrees sync.Map // *Plane → *workTrees

// workTrees returns the plane's drift counts, making them on first use.
func (p *Plane) workTrees() *workTrees {
	if w, ok := planeWorkTrees.Load(p); ok {
		return w.(*workTrees)
	}
	w, _ := planeWorkTrees.LoadOrStore(p, newWorkTrees(p))
	return w.(*workTrees)
}

// workTrees is one plane's drift counts, by tile.
type workTrees struct {
	p     *Plane
	count driftFunc
	now   func() time.Time
	after func(time.Duration, func()) // runs f once d has passed: time.AfterFunc
	spawn func(func())                // runs f off the caller: a goroutine

	mu    sync.Mutex
	tiles map[string]*tileDrift
}

// tileDrift is one paused tile's count and its debounce.
type tileDrift struct {
	since   string    // the checkpoint the last count was against
	changed int       // what it counted
	counted bool      // a count against since finished
	last    time.Time // when the last count started
	running bool      // a count is in flight
	due     bool      // a trailing count is scheduled
	again   bool      // a notice came while a count ran, which may have missed its change
}

func newWorkTrees(p *Plane) *workTrees {
	return &workTrees{p: p, count: p.driftCounter(), now: time.Now,
		after: func(d time.Duration, f func()) { time.AfterFunc(d, f) },
		spawn: func(f func()) { go f() },
		tiles: map[string]*tileDrift{}}
}

// counted is tile's last count against since.
func (w *workTrees) counted(tile, since string) (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t := w.tiles[tile]; t != nil && t.counted && t.since == since {
		return t.changed, true
	}
	return 0, false
}

// notice debounces a count of tile: at once when none started in the last
// driftEvery, otherwise once, when driftEvery has passed since the last one
// began. A notice during a count asks for one more after it, so the last
// change is always counted.
func (w *workTrees) notice(tile string) {
	if w.count == nil {
		return
	}
	w.mu.Lock()
	t := w.tiles[tile]
	if t == nil {
		t = &tileDrift{}
		w.tiles[tile] = t
	}
	now := false
	switch {
	case t.running:
		t.again = true
	case !t.due:
		now = w.scheduleLocked(tile, t)
	}
	w.mu.Unlock()
	if now {
		w.spawn(func() { w.run(tile) })
	}
}

// scheduleLocked plans tile's next count: it reports true when the count
// starts now (the caller runs it once the lock is released), or sets a
// timer for when the debounce allows it.
func (w *workTrees) scheduleLocked(tile string, t *tileDrift) bool {
	if wait := t.last.Add(driftEvery).Sub(w.now()); !t.last.IsZero() && wait > 0 {
		t.due = true
		w.after(wait, func() { w.start(tile) })
		return false
	}
	t.running, t.last = true, w.now()
	return true
}

// start is a trailing count's timer.
func (w *workTrees) start(tile string) {
	w.mu.Lock()
	t := w.tiles[tile]
	if t == nil || !t.due {
		w.mu.Unlock()
		return
	}
	t.due, t.running, t.last = false, true, w.now()
	w.mu.Unlock()
	w.run(tile)
}

// run counts tile once, keeps the count, and announces it when it moved:
// the first count against a checkpoint moves from 0, the drift right after
// the pause that captured it.
func (w *workTrees) run(tile string) {
	since, n, ok, err := w.measure(tile)
	w.mu.Lock()
	t := w.tiles[tile]
	t.running = false
	moved := false
	switch {
	case !ok: // no longer paused, or gone: nothing to keep
		t.since, t.changed, t.counted = "", 0, false
	case err != nil: // the last count stands
		slog.Warn("deployments: counting the work tree's drift", "tile", tile, "err", err)
	default:
		prev := 0
		if t.since == since {
			prev = t.changed
		}
		moved = n != prev
		t.since, t.changed, t.counted = since, n, true
	}
	again := false
	if t.again {
		t.again = false
		again = w.scheduleLocked(tile, t)
	}
	if !ok && !t.running && !t.due {
		delete(w.tiles, tile)
	}
	w.mu.Unlock()
	if moved && w.p.Hub != nil {
		w.p.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: workTreeOp{Op: "work-tree", Changed: n}})
	}
	if again {
		w.spawn(func() { w.run(tile) })
	}
}

// measure runs one count of tile against its last target's checkpoint, as
// the record says now; ok is false when the tile is no longer paused or its
// component is gone.
func (w *workTrees) measure(tile string) (since string, n int, ok bool, err error) {
	if since, ok = w.p.pausedSince(tile); !ok || w.p.Reg == nil {
		return "", 0, false, nil
	}
	c, found := w.p.Reg.Component(tile)
	if !found {
		return "", 0, false, nil
	}
	src := checkpoint.Source{Tile: tile, WorkTree: c.Dir}
	for _, o := range w.p.Reg.Components() {
		if rel, in := strings.CutPrefix(o.Path, tile+"/"); in {
			src.Nested = append(src.Nested, rel)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), driftTimeout)
	defer cancel()
	n, err = w.count(ctx, src, since)
	return since, n, true, err
}
