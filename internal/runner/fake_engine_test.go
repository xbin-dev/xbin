package runner

// covers SC-ZERO — the fake engine the state-machine rows drive the runner
// through (15-test-plan §2.3): fake builds, fake generations, a fake clock
// and a tape of hub events. It exercises the real Ensure, Changed, Track,
// crash watch, reaper, Stop and StopAll; only the effects are fake.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// fakeGen is what the fake knows about a generation it started.
type fakeGen struct {
	tile   string
	gen    int
	closed bool // its waitCh is closed: stopped, or crashed
}

// fakeEngine records every effect the runner asks for, in order:
// "build apps/x main@worktree", "start apps/x main g2", "stop apps/x main g1".
// A build that fails, by fail-next-build, logs with a ": fail" suffix.
type fakeEngine struct {
	t       *testing.T
	root    string
	hub     *events.Hub
	mu      sync.Mutex
	log     []string
	fail    map[string]int         // "build apps/x main" or "health apps/x main" → fail the next n
	gens    map[*instance]*fakeGen // every generation the fake started
	cur     map[string]*instance   // per tile: the last generation that passed health
	clock   time.Time
	sealed  map[string]bool // ShouldRun is false for these tiles
	hold    chan struct{}   // armed: the next build parks until releaseBuild
	release chan struct{}   // what the parked build waits on
	parked  string          // the tile whose build is parked ("" = none)
}

// tape records hub events as "<type> <component>", with the text of a
// build-error after a colon.
type tape struct {
	ch <-chan events.Event
}

func (tp *tape) take() []string {
	var out []string
	for {
		select {
		case e, ok := <-tp.ch:
			if !ok {
				return append(out, "(tape evicted)")
			}
			s := e.Type + " " + e.Component
			if e.Type == "build-error" {
				s += ": " + e.Text
			}
			out = append(out, s)
		default:
			return out
		}
	}
}

// seamEpoch is where the fake clock starts.
var seamEpoch = time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

// newSeamRunner builds a runner over a temporary workspace holding comps,
// without New: no real reaper starts. Each component's manifest is written
// to disk, so the registry (read by the reaper's alwaysOn check) agrees with
// the component the test passes to Ensure.
func newSeamRunner(t *testing.T, comps ...*registry.Component) (*Runner, *fakeEngine, *tape) {
	t.Helper()
	root := t.TempDir()
	for _, c := range comps {
		c.Dir = filepath.Join(root, filepath.FromSlash(c.Path))
		b, err := json.Marshal(c.Manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(c.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.Dir, "xbin.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := &fakeEngine{
		t: t, root: root, hub: events.NewHub(), clock: seamEpoch,
		fail: map[string]int{}, gens: map[*instance]*fakeGen{}, cur: map[string]*instance{},
		sealed: map[string]bool{},
	}
	ch, cancel := f.hub.Subscribe(nil)
	t.Cleanup(cancel)
	return f.runner(), f, &tape{ch: ch}
}

// runner is a fresh Runner over the fake's workspace, hub and world: what
// an xbind restart looks like to the runner.
func (f *fakeEngine) runner() *Runner {
	reg, err := registry.Open(f.root)
	if err != nil {
		f.t.Fatal(err)
	}
	return &Runner{
		Root: f.root, Hub: f.hub, Reg: reg,
		ShouldRun: func(comp string) bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return !f.sealed[comp]
		},
		states: map[string]*state{}, netmux: newNetMux(), engine: f.engine(),
	}
}

func (f *fakeEngine) engine() *engine {
	return &engine{build: f.build, start: f.start, healthy: f.healthy, stop: f.stop, now: f.now}
}

// failNext makes the next n calls of kind ("build" or "health") for the
// tile's deployment (only main in M0) fail.
func (f *fakeEngine) failNext(kind, tile, dep string, n int) {
	f.mu.Lock()
	f.fail[kind+" "+tile+" "+dep] += n
	f.mu.Unlock()
}

// failing consumes one pending failure of key. Callers hold f.mu.
func (f *fakeEngine) failing(key string) bool {
	if f.fail[key] > 0 {
		f.fail[key]--
		return true
	}
	return false
}

func (f *fakeEngine) build(c *registry.Component) (string, error) {
	f.mu.Lock()
	entry := "build " + c.Path + " main@worktree"
	if h := f.hold; h != nil {
		f.hold, f.release, f.parked = nil, h, c.Path
		f.log = append(f.log, entry)
		f.mu.Unlock()
		<-h // releaseBuild clears parked before it closes h
		return "fake-bin/" + c.Path, nil
	}
	defer f.mu.Unlock()
	if f.failing("build " + c.Path + " main") {
		f.log = append(f.log, entry+": fail")
		return "", &BuildError{Output: "fake compile error"}
	}
	f.log = append(f.log, entry)
	return "fake-bin/" + c.Path, nil
}

func (f *fakeEngine) start(c *registry.Component, bin string, gen int) (*instance, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := &instance{
		gen: gen, sock: fmt.Sprintf("fake-run/%s/g%d.sock", c.Path, gen),
		cmd: &exec.Cmd{}, started: f.clock, waitCh: make(chan struct{}),
	}
	f.gens[inst] = &fakeGen{tile: c.Path, gen: gen}
	f.log = append(f.log, fmt.Sprintf("start %s main g%d", c.Path, gen))
	return inst, nil
}

func (f *fakeEngine) healthy(c *registry.Component, inst *instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing("health " + c.Path + " main") {
		return fmt.Errorf("fake: never answered")
	}
	f.cur[c.Path] = inst
	return nil
}

// stop closes a generation's waitCh, as a real process exit would; stopping
// an exited generation is a no-op, like SIGTERM to a reaped process.
func (f *fakeEngine) stop(inst *instance, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.gens[inst]
	if g == nil {
		f.t.Errorf("stop of a generation the fake never started")
		return
	}
	f.log = append(f.log, fmt.Sprintf("stop %s main g%d", g.tile, g.gen))
	if !g.closed {
		g.closed = true
		close(inst.waitCh)
	}
}

// crash ends the tile's current generation without the runner asking: the
// real crash watch sees the exit.
func (f *fakeEngine) crash(tile, dep string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if dep != "main" {
		f.t.Fatalf("crash %s: no deployment %q in M0", tile, dep)
	}
	inst := f.cur[tile]
	g := f.gens[inst]
	if g == nil || g.closed {
		f.t.Fatalf("crash %s: no running generation", tile)
	}
	g.closed = true
	close(inst.waitCh)
}

// advance moves the fake clock.
func (f *fakeEngine) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func (f *fakeEngine) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

func (f *fakeEngine) seal(tile string, on bool) {
	f.mu.Lock()
	f.sealed[tile] = on
	f.mu.Unlock()
}

// holdNextBuild parks the next build until releaseBuild: a slow build.
func (f *fakeEngine) holdNextBuild() {
	f.mu.Lock()
	f.hold = make(chan struct{})
	f.mu.Unlock()
}

func (f *fakeEngine) releaseBuild() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hold != nil || f.release == nil {
		f.t.Fatal("release-build: no build is parked")
	}
	close(f.release)
	f.release, f.parked = nil, ""
}

func (f *fakeEngine) takeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.log
	f.log = nil
	return out
}

// settle waits (at most 2 s) until the runner is quiescent: no build in
// flight unless it is parked on a hold, no Ensure pending from a Changed, no
// crash the watch has yet to see, and every generation that is not a
// state's current one stopped.
func settle(t *testing.T, r *Runner, f *fakeEngine) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		why := unsettled(r, f)
		if why == "" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("runner never settled: %s", why)
		}
		time.Sleep(time.Millisecond)
	}
}

func unsettled(r *Runner, f *fakeEngine) string {
	type snap struct {
		comp                          string
		cur                           *instance
		building, dirty, hasErr, runs bool
	}
	r.mu.Lock()
	var snaps []snap
	for comp, s := range r.states {
		s.mu.Lock()
		snaps = append(snaps, snap{comp: comp, cur: s.cur, building: s.building, dirty: s.dirty, hasErr: s.lastErr != nil})
		s.mu.Unlock()
	}
	r.mu.Unlock()
	curs := map[*instance]bool{}
	for i := range snaps {
		snaps[i].runs = r.ShouldRun == nil || r.ShouldRun(snaps[i].comp)
		if snaps[i].cur != nil {
			curs[snaps[i].cur] = true
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range snaps {
		switch {
		case s.building && f.parked != s.comp:
			return s.comp + " is building"
		case s.building:
			// parked on a hold: saves meanwhile are expected to wait on it
		case s.dirty && s.runs && (s.cur != nil || s.hasErr):
			return s.comp + " has a rebuild pending (Changed)"
		case s.cur != nil && f.gens[s.cur] != nil && f.gens[s.cur].closed:
			return s.comp + "'s generation exited unseen by the crash watch"
		}
	}
	for inst, g := range f.gens {
		if !g.closed && !curs[inst] {
			return fmt.Sprintf("%s g%d is neither current nor stopped", g.tile, g.gen)
		}
	}
	return ""
}

// statusOf renders a tile's Status() entry: "healthy g2", "failed g3 · <error>",
// "-" when the runner has no state for it.
func statusOf(r *Runner, tile string) string {
	e, ok := r.Status()[tile].(map[string]any)
	if !ok {
		return "-"
	}
	s := fmt.Sprintf("%s g%d", e["state"], e["gen"])
	if msg, ok := e["error"].(string); ok {
		s += " · " + msg
	}
	return s
}

// answer renders what Ensure returned: the generation it serves ("g1"), or
// "error: <text>".
func answer(sock string, err error) string {
	if err != nil {
		return "error: " + err.Error()
	}
	return strings.TrimSuffix(filepath.Base(sock), ".sock")
}
