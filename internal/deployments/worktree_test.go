package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// wtClock is a manual clock: timers fire when advance passes them.
type wtClock struct {
	now    time.Time
	timers []wtTimer
}

type wtTimer struct {
	at time.Time
	f  func()
}

func (c *wtClock) after(d time.Duration, f func()) {
	c.timers = append(c.timers, wtTimer{at: c.now.Add(d), f: f})
}

// advance moves the clock by d, firing every timer due by then in order.
func (c *wtClock) advance(d time.Duration) {
	end := c.now.Add(d)
	for {
		slices.SortStableFunc(c.timers, func(a, b wtTimer) int { return a.at.Compare(b.at) })
		if len(c.timers) == 0 || c.timers[0].at.After(end) {
			break
		}
		next := c.timers[0]
		c.timers = c.timers[1:]
		c.now = next.at
		next.f()
	}
	c.now = end
}

// wtCounter is a drift count computed from the work tree, as the store's is:
// the files under src.WorkTree, nested components left out, that differ from
// the baseline the pinned checkpoint holds (added, changed or removed).
type wtCounter struct {
	t        *testing.T
	baseline map[string]map[string]string // tree → rel → content
	calls    []checkpoint.Source
	during   func() // runs inside the next count, once
}

func (w *wtCounter) count(_ context.Context, src checkpoint.Source, tree string) (int, error) {
	w.calls = append(w.calls, src)
	if f := w.during; f != nil {
		w.during = nil
		f()
	}
	base, ok := w.baseline[tree]
	if !ok {
		return 0, errors.New("no such checkpoint")
	}
	now := wtFiles(w.t, src.WorkTree, src.Nested)
	n := 0
	for rel, body := range now {
		if b, ok := base[rel]; !ok || b != body {
			n++
		}
	}
	for rel := range base {
		if _, ok := now[rel]; !ok {
			n++
		}
	}
	return n, nil
}

// wtFiles reads every file under dir, but those under nested.
func wtFiles(t *testing.T, dir string, nested []string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if slices.Contains(nested, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// wtFixture is a booted plane over a workspace with a tile whose live
// reload is paused (apps/reloady, main pinned to treeA, a nested component
// beside its files), a zero-state tile, a tile whose record holds it and one
// whose live reload is attached, with the drift count, the clock and the
// goroutine replaced so that every count runs inline.
type wtFixture struct {
	t       *testing.T
	ws      string
	p       *Plane
	w       *workTrees
	clock   *wtClock
	counter *wtCounter
	events  <-chan events.Event
}

const wtTile = "apps/reloady"

func newWTFixture(t *testing.T) *wtFixture {
	t.Helper()
	ws := t.TempDir()
	for rel, body := range map[string]string{
		"apps/reloady/index.html":        `<p>v1</p>`,
		"apps/reloady/app.js":            `export {}`,
		"apps/reloady/nested/xbin.json":  `{"runtime":"static"}`,
		"apps/reloady/nested/index.html": `<p>nested</p>`,
		"apps/free/index.html":           `<p>free</p>`,
		"apps/held/index.html":           `<p>held</p>`,
		"apps/attached/index.html":       `<p>attached</p>`,
	} {
		wtWrite(t, ws, rel, body)
	}
	writeRecordFile(t, ws, "apps/held", []byte("not a record"))
	reg, err := registry.Open(ws)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	p := &Plane{Root: ws, Reg: reg, Hub: hub}
	if err := p.Boot(); err != nil {
		t.Fatal(err)
	}
	f := &wtFixture{t: t, ws: ws, p: p, clock: &wtClock{now: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}}
	f.pin(wtTile, treeA)
	if _, err := p.idx.commit("apps/attached", -1, func(*Record) error { return nil }); err != nil {
		t.Fatal(err)
	}
	f.counter = &wtCounter{t: t, baseline: map[string]map[string]string{
		treeA: wtFiles(t, filepath.Join(ws, wtTile), []string{"nested"}),
	}}
	f.w = p.workTrees()
	f.w.count, f.w.now, f.w.after = f.counter.count, func() time.Time { return f.clock.now }, f.clock.after
	f.w.spawn = func(run func()) { run() }
	ch, cancel := hub.Subscribe(nil)
	t.Cleanup(cancel)
	f.events = ch
	t.Cleanup(func() { planeWorkTrees.Delete(p) })
	return f
}

func wtWrite(t *testing.T, ws, rel, body string) {
	t.Helper()
	p := filepath.Join(ws, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pin commits tile's record with live reload paused and main pinned to tree:
// what pausing live reload, or a reload now, leaves.
func (f *wtFixture) pin(tile, tree string) {
	f.t.Helper()
	if _, err := f.p.idx.commit(tile, -1, func(r *Record) error {
		r.LiveReload, r.LastLiveReload = "", util.MainDeployment
		r.Deployments[util.MainDeployment].Checkpoint = &tree
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

// resume commits tile's record with live reload attached to main again.
func (f *wtFixture) resume(tile string) {
	f.t.Helper()
	if _, err := f.p.idx.commit(tile, -1, func(r *Record) error {
		r.LiveReload = util.MainDeployment
		r.Deployments[util.MainDeployment].Checkpoint = nil
		return nil
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *wtFixture) save(rel, body string) { f.t.Helper(); wtWrite(f.t, f.ws, wtTile+"/"+rel, body) }

func (f *wtFixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.ws, filepath.FromSlash(wtTile+"/"+rel))); err != nil {
		f.t.Fatal(err)
	}
}

// announced drains the work-tree counts announced since the last call; any
// other event, or other bytes, fail the test.
func (f *wtFixture) announced() []int {
	f.t.Helper()
	var out []int
	for {
		select {
		case e := <-f.events:
			b, _ := json.Marshal(e)
			var d struct {
				Data struct {
					Op      string `json:"op"`
					Changed int    `json:"changed"`
				} `json:"data"`
			}
			_ = json.Unmarshal(b, &d)
			want := fmt.Sprintf(`{"type":"deployments","component":%q,"data":{"op":"work-tree","changed":%d}}`, wtTile, d.Data.Changed)
			if string(b) != want {
				f.t.Errorf("announced %s, want only %s", b, want)
			}
			out = append(out, d.Data.Changed)
		default:
			return out
		}
	}
}

// drift is the plane's State.workTree answer for the paused tile.
func (f *wtFixture) drift() WorkTreeDrift {
	f.t.Helper()
	d, ok := f.p.WorkTreeDrift(wtTile)
	if !ok {
		f.t.Fatalf("%s: no work-tree drift while its live reload is paused", wtTile)
	}
	return d
}

// covers NP-13-12 D119d — the watcher drops a batch when its consumer is slow
// (internal/watch/watch.go), so the files changed since the checkpoint are
// recounted from the work tree, never summed from notices: of three saves,
// the second's batch is dropped (no notice reaches the plane), and the count
// still says three. Removing the files brings it back to zero, announced as
// a move too.
func TestPendingCountAfterDroppedBatch(t *testing.T) {
	f := newWTFixture(t)

	f.save("a.js", "a")
	f.p.WorkTreeMoved(wtTile) // counts at once: none ran in the last 2 s
	if got := f.announced(); !reflect.DeepEqual(got, []int{1}) {
		t.Fatalf("after the first save: announced %v, want [1]", got)
	}

	f.save("b.js", "b") // its batch is dropped: no notice

	f.clock.advance(500 * time.Millisecond)
	f.save("index.html", "<p>v2</p>")
	f.p.WorkTreeMoved(wtTile) // within 2 s of the last count: scheduled
	if got := f.announced(); len(got) != 0 {
		t.Fatalf("a notice inside the debounce counted at once: announced %v", got)
	}
	f.clock.advance(2 * time.Second)
	if got := f.announced(); !reflect.DeepEqual(got, []int{3}) {
		t.Fatalf("after the dropped batch: announced %v, want [3] (recounted, not summed)", got)
	}
	if d := f.drift(); d != (WorkTreeDrift{Since: treeA, Changed: 3, Counted: true}) {
		t.Errorf("State.workTree %+v, want 3 files since %s", d, treeA)
	}

	f.remove("a.js")
	f.remove("b.js")
	f.save("index.html", `<p>v1</p>`)
	f.clock.advance(3 * time.Second)
	f.p.WorkTreeMoved(wtTile)
	if got := f.announced(); !reflect.DeepEqual(got, []int{0}) {
		t.Errorf("after undoing every change: announced %v, want [0]", got)
	}
	if n := len(f.counter.calls); n != 3 {
		t.Errorf("%d counts, want 3 (one per debounce window with a notice)", n)
	}
	want := checkpoint.Source{Tile: wtTile, WorkTree: filepath.Join(f.ws, wtTile), Nested: []string{"nested"}}
	for _, src := range f.counter.calls {
		if !reflect.DeepEqual(src, want) {
			t.Errorf("counted %+v, want %+v", src, want)
		}
	}
}

// covers NP-13-12 D119d — the drift count is debounced per tile: ten notices
// inside two seconds make one count at once and one when the two seconds
// have passed, not ten; a notice that comes while a count runs, which may
// have missed its change, gets one more count after it; a count that didn't
// move announces nothing; and each tile is debounced on its own.
func TestWorkTreeCountDebounced(t *testing.T) {
	f := newWTFixture(t)
	for i := range 10 {
		f.save("burst.js", strings.Repeat("x", i+1))
		f.p.WorkTreeMoved(wtTile)
		f.clock.advance(100 * time.Millisecond)
	}
	if n := len(f.counter.calls); n != 1 {
		t.Fatalf("%d counts inside the first 2 s, want 1", n)
	}
	f.clock.advance(time.Second)
	if n := len(f.counter.calls); n != 2 {
		t.Fatalf("%d counts once 2 s passed, want 2 (the trailing one)", n)
	}
	if got := f.announced(); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("announced %v, want [1]: the trailing count didn't move", got)
	}

	// A notice during a count: the count may not have seen that save.
	f.clock.advance(5 * time.Second)
	f.counter.during = func() {
		f.save("late.js", "late")
		f.p.WorkTreeMoved(wtTile)
	}
	f.save("early.js", "early")
	f.p.WorkTreeMoved(wtTile)
	f.clock.advance(2 * time.Second)
	if n := len(f.counter.calls); n != 4 {
		t.Errorf("%d counts, want 4: one more after the notice that came during a count", n)
	}
	if got := f.announced(); len(got) == 0 || got[len(got)-1] != 3 {
		t.Errorf("announced %v, want a last count of 3 (burst, early and late)", got)
	}

	// Another tile has a debounce of its own.
	other := "apps/other"
	wtWrite(t, f.ws, other+"/index.html", "<p>o</p>")
	if err := f.p.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	f.pin(other, treeB)
	f.counter.baseline[treeB] = map[string]string{}
	calls := len(f.counter.calls)
	f.p.WorkTreeMoved(wtTile)
	f.p.WorkTreeMoved(other)
	if n := len(f.counter.calls) - calls; n != 1 {
		t.Errorf("%d counts for a new tile's first notice beside a debounced one, want 1", n)
	}
}

// covers NP-13-12 D119c D119d C7 — only a paused tile is ever counted: a tile
// without a record, one whose record holds it (it answers LiveReload like a
// paused one) and one whose live reload is attached get no count, no state
// and no State.workTree. A count already scheduled when live reload resumes
// counts nothing and drops the tile. After a reload now re-pins the last
// target, the state read reports no count against the new checkpoint until
// one runs, and that count moves from zero. An unwired drift count counts
// nothing.
func TestWorkTreeCountOnlyWhilePaused(t *testing.T) {
	f := newWTFixture(t)
	for _, tile := range []string{"apps/free", "apps/held", "apps/attached", "apps/none"} {
		f.p.WorkTreeMoved(tile)
		if d, ok := f.p.WorkTreeDrift(tile); ok {
			t.Errorf("%s: State.workTree %+v, want none", tile, d)
		}
	}
	if st := f.p.Lookup("apps/held").State; st != RecordHeld {
		t.Fatalf("apps/held's record state %v, want held", st)
	}
	if n := len(f.counter.calls); n != 0 || len(f.w.tiles) != 0 {
		t.Errorf("%d counts and state for %d tiles, want none", n, len(f.w.tiles))
	}

	if d := f.drift(); d != (WorkTreeDrift{Since: treeA}) {
		t.Errorf("before any count: State.workTree %+v, want no count since %s", d, treeA)
	}
	f.save("a.js", "a")
	f.p.WorkTreeMoved(wtTile)
	f.save("b.js", "b")
	f.p.WorkTreeMoved(wtTile) // scheduled for 2 s on
	f.resume(wtTile)
	f.clock.advance(2 * time.Second)
	if n := len(f.counter.calls); n != 1 {
		t.Errorf("%d counts, want 1: the count scheduled before the resume counts nothing", n)
	}
	if _, ok := f.p.WorkTreeDrift(wtTile); ok || len(f.w.tiles) != 0 {
		t.Errorf("after resume: State.workTree reported, or state kept for %d tiles", len(f.w.tiles))
	}
	f.announced()

	// A reload now: a new checkpoint of the work tree, pinned.
	f.pin(wtTile, treeB)
	f.counter.baseline[treeB] = wtFiles(t, filepath.Join(f.ws, wtTile), []string{"nested"})
	if d := f.drift(); d != (WorkTreeDrift{Since: treeB}) {
		t.Errorf("after the re-pin: State.workTree %+v, want no count since %s", d, treeB)
	}
	f.clock.advance(3 * time.Second)
	f.save("c.js", "c")
	f.p.WorkTreeMoved(wtTile)
	if got := f.announced(); !reflect.DeepEqual(got, []int{1}) {
		t.Errorf("announced %v, want [1] against the new checkpoint", got)
	}

	// In xbind the count is the plane's checkpoint store's.
	if (&Plane{Root: f.ws}).driftCounter() == nil {
		t.Error("the plane has no drift count")
	}
}

// covers NP-13-12 D119d — notices and state reads from many goroutines, with
// counts running on goroutines of their own, as in xbind: the debounce
// holds (one count starts at once, the rest wait for the timer) and nothing
// races (go test -race).
func TestWorkTreeCountConcurrent(t *testing.T) {
	f := newWTFixture(t)
	var mu sync.Mutex
	counts := 0
	f.w.count = func(context.Context, checkpoint.Source, string) (int, error) {
		mu.Lock()
		counts++
		mu.Unlock()
		return 2, nil
	}
	var timers atomic.Int32
	f.w.now, f.w.after = time.Now, func(time.Duration, func()) { timers.Add(1) }
	var wg sync.WaitGroup
	f.w.spawn = func(run func()) { wg.Go(run) }
	var readers sync.WaitGroup
	for range 8 {
		readers.Go(func() {
			for range 100 {
				f.p.WorkTreeMoved(wtTile)
				f.p.WorkTreeDrift(wtTile)
			}
		})
	}
	readers.Wait()
	wg.Wait()
	if counts != 1 || timers.Load() > 1 {
		t.Errorf("%d counts started at once and %d timers set, want 1 and at most 1", counts, timers.Load())
	}
	if d := f.drift(); d != (WorkTreeDrift{Since: treeA, Changed: 2, Counted: true}) {
		t.Errorf("State.workTree %+v, want 2 files since %s", d, treeA)
	}
}
