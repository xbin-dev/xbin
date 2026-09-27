package runner

// covers P7 P8 P12 P13 P17 P25 SC-INBOUND T3 T18 — runner state keyed by
// (tile, deployment) (07-runtime §1.2, §8.5, §8.8, §9, §10.3): seam rows
// 31–34 and 37 of 15-test-plan §2.5, TestBackendEnvPerDeployment,
// TestInstanceTokenRegisteredWithDeployment, TestLogPathsPerDeployment,
// TestRunDirPerDeployment, TestEnsureCallSitesPassPrimary, 09-fabric §10's
// TestDrainAuthority, and the admission caps (TestNonPrimaryAdmissionCaps).
//
// The seam rows drive the real runner through a fake engine and a fake
// deployments plane (depFake): a record per tile naming each deployment's
// code and the primary, a materializer, and the registry's own views. The
// storage names are hand-maintained literals (sha256sum), never computed by
// the code under test.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	tkX     = "628e34e47e8e631c2fde937e0fbf1205" // TileKey("apps/x"): sha256("xbin-tile-key-v1\x00apps/x")[:16]
	sockDev = "d-2a102cf9fefcda61"               // sockDir("apps/x", "dev"): sha256("apps/x\x00dev")[:8]
)

// ---- the fake plane and engine ----

// depFake is the deployments plane and the runner's engine, faked over one
// workspace: records (each deployment's code, the primary), a materializer,
// the registry's views, and a log of every effect: "build apps/x dev@worktree",
// "start apps/x dev g2 @c1", "stop apps/x main g1".
type depFake struct {
	t     *testing.T
	root  string
	hub   *events.Hub
	r     *Runner
	comps map[string]*registry.Component

	mu      sync.Mutex
	primary map[string]string // tile → its primary; absent: main
	code    map[string]Code   // "tile dep" → the record's code
	labels  map[string]string // tree → label
	log     []string
	gens    map[*instance]*depGen
	cur     map[string]*instance // "tile dep" → the last generation that passed health
	views   map[string]*registry.Component
	clock   time.Time
	holdFor string        // "tile dep": its next build parks until release
	hold    chan struct{} // what a parked build waits on
}

type depGen struct {
	tile, dep string
	gen       int
	closed    bool
}

func dk(tile, dep string) string { return tile + " " + dep }

// newDepFake builds an isolated runner (the fake engine stands in for the
// sandbox) over a workspace holding a Go tile at each path.
func newDepFake(t *testing.T, tiles ...string) (*depFake, *tape) {
	t.Helper()
	f := &depFake{t: t, root: t.TempDir(), hub: events.NewHub(), comps: map[string]*registry.Component{},
		primary: map[string]string{}, code: map[string]Code{}, labels: map[string]string{},
		gens: map[*instance]*depGen{}, cur: map[string]*instance{}, views: map[string]*registry.Component{}, clock: seamEpoch}
	for _, tile := range tiles {
		c := &registry.Component{Path: tile, Dir: filepath.Join(f.root, filepath.FromSlash(tile)), Manifest: registry.Manifest{Runtime: "go"}}
		if err := os.MkdirAll(c.Dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(c.Dir, "xbin.json"), []byte(`{"runtime":"go"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		f.comps[tile] = c
		f.code[dk(tile, "main")] = Code{WorkTree: true}
	}
	reg, err := registry.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	f.r = &Runner{Root: f.root, Hub: f.hub, Reg: reg, Isolate: true, Sandboxes: sbx.New(),
		states: map[string]*state{}, netmux: newNetMux(),
		engine: &engine{build: f.build, start: f.start, healthy: f.healthy, stop: f.stop, now: f.now}}
	f.r.DeploymentHooks = DeploymentHooks{CodeFor: f.codeFor, Primary: f.primaryOf, View: f.view, Materialize: f.materialize}
	ch, cancel := f.hub.Subscribe(nil)
	t.Cleanup(cancel)
	t.Cleanup(f.r.StopAll)
	return f, &tape{ch: ch}
}

// set points deployment dep of tile at code: "worktree" or a checkpoint's
// label ("c1" → its tree).
func (f *depFake) set(tile, dep, code string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if code == "worktree" {
		f.code[dk(tile, dep)] = Code{WorkTree: true}
		return
	}
	f.code[dk(tile, dep)] = Code{Tree: pinTree(code)}
	f.labels[pinTree(code)] = code
}

func (f *depFake) setPrimary(tile, dep string) {
	f.mu.Lock()
	f.primary[tile] = dep
	f.mu.Unlock()
}

func (f *depFake) codeFor(tile, dep string) (Code, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.code[dk(tile, dep)]; ok {
		return c, nil
	}
	return Code{}, util.NoDeployment(tile, dep)
}

func (f *depFake) primaryOf(tile string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p := f.primary[tile]; p != "" {
		return p
	}
	return util.MainDeployment
}

func (f *depFake) rootOf(tile, tree string) string {
	return filepath.Join(f.root, ".xbin", "deploy", util.TileKey(tile), tree)
}

func (f *depFake) materialize(tile, tree string) (string, error) {
	root := f.rootOf(tile, tree)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"runtime":"go"}`), 0o644)
}

// view is the plane's View: the primary's views, as WP-18's registry makes
// them.
func (f *depFake) view(c *registry.Component, code Code) (*registry.Component, error) {
	if code.WorkTree {
		return c, nil
	}
	return f.r.Reg.View(c, registry.ViewCode{Tree: code.Tree, Root: f.rootOf(c.Path, code.Tree)})
}

// depOf names the deployment a view spawns: the one it names, else the
// primary (the fake has no other way to know the primary's name).
func (f *depFake) depOf(c *registry.Component) string {
	if c.Deployment != "" {
		return c.Deployment
	}
	return f.primaryOf(c.Path)
}

func (f *depFake) label(tree string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if l, ok := f.labels[tree]; ok {
		return l
	}
	return "?" + tree
}

func (f *depFake) build(c *registry.Component) (string, error) {
	dep := f.depOf(c)
	if c.CodeRoot == "" {
		f.mu.Lock()
		f.log = append(f.log, fmt.Sprintf("build %s %s@worktree", c.Path, dep))
		var hold chan struct{}
		if f.holdFor == dk(c.Path, dep) {
			f.holdFor, hold = "", f.hold
		}
		f.mu.Unlock()
		if hold != nil {
			<-hold
		}
		return "fake-bin/" + c.Path, nil
	}
	tree := filepath.Base(c.CodeRoot)
	f.mu.Lock()
	f.log = append(f.log, fmt.Sprintf("build %s %s@%s", c.Path, dep, f.labels[tree]))
	f.mu.Unlock()
	dir := filepath.Join(f.root, ".xbin", "build", util.CompKey(c.Path), "c", tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("fake"), 0o755); err != nil {
		return "", err
	}
	stamp, _ := json.Marshal(map[string]string{"tile": c.Path, "tree": tree})
	return filepath.Join(dir, "bin"), os.WriteFile(filepath.Join(dir, "build.json"), stamp, 0o644)
}

func (f *depFake) start(c *registry.Component, bin string, gen int) (*instance, error) {
	dep, from := f.depOf(c), "@worktree"
	if c.CodeRoot != "" {
		from = "@" + f.label(filepath.Base(c.CodeRoot))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := &instance{gen: gen, sock: fmt.Sprintf("fake-run/%s/%s/g%d.sock", c.Path, dep, gen),
		cmd: &exec.Cmd{}, started: f.clock, waitCh: make(chan struct{})}
	f.gens[inst] = &depGen{tile: c.Path, dep: dep, gen: gen}
	f.views[fmt.Sprintf("%s %s g%d", c.Path, dep, gen)] = c
	f.log = append(f.log, fmt.Sprintf("start %s %s g%d %s", c.Path, dep, gen, from))
	return inst, nil
}

func (f *depFake) healthy(c *registry.Component, inst *instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.gens[inst]
	f.cur[dk(g.tile, g.dep)] = inst
	return nil
}

func (f *depFake) stop(inst *instance, _ time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	g := f.gens[inst]
	if g == nil {
		f.t.Errorf("stop of a generation the fake never started")
		return
	}
	f.log = append(f.log, fmt.Sprintf("stop %s %s g%d", g.tile, g.dep, g.gen))
	if !g.closed {
		g.closed = true
		close(inst.waitCh)
	}
}

func (f *depFake) now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.clock
}

// crash ends deployment dep of tile's current generation unasked.
func (f *depFake) crash(tile, dep string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := f.cur[dk(tile, dep)]
	g := f.gens[inst]
	if g == nil || g.closed {
		f.t.Fatalf("crash %s %s: no running generation", tile, dep)
	}
	g.closed = true
	close(inst.waitCh)
}

// takeLog returns the effects since the last take; stops, which drains run
// in the background, are sorted after the rest.
func (f *depFake) takeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out, stops []string
	for _, l := range f.log {
		if strings.HasPrefix(l, "stop ") {
			stops = append(stops, l)
		} else {
			out = append(out, l)
		}
	}
	f.log = nil
	sort.Strings(stops)
	return append(out, stops...)
}

func (f *depFake) viewOf(tile, dep string, gen int) *registry.Component {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.views[fmt.Sprintf("%s %s g%d", tile, dep, gen)]
}

// ensure is a request for deployment dep of tile, as the proxy makes it.
func (f *depFake) ensure(tile, dep string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return answer(f.r.EnsureDeployment(ctx, f.comps[tile], dep))
}

// settle waits until the runner is quiescent: nothing building, no rebuild
// pending, no exit unseen by a crash watch, every generation that isn't a
// state's current one stopped.
func (f *depFake) settle() {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		why := f.unsettled()
		if why == "" {
			return
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("runner never settled: %s", why)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *depFake) unsettled() string {
	curs := map[*instance]bool{}
	for _, s := range f.r.allStates("") {
		s.mu.Lock()
		cur, building, pending := s.cur, s.building, s.dirty && (s.cur != nil || s.lastErr != nil)
		s.mu.Unlock()
		name := dk(s.comp, s.dep)
		switch {
		case building:
			return name + " is building"
		case pending:
			return name + " has a rebuild pending"
		}
		if cur != nil {
			curs[cur] = true
			f.mu.Lock()
			closed := f.gens[cur] != nil && f.gens[cur].closed
			f.mu.Unlock()
			if closed {
				return name + "'s generation exited unseen"
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for inst, g := range f.gens {
		if !g.closed && !curs[inst] {
			return fmt.Sprintf("%s %s g%d is neither current nor stopped", g.tile, g.dep, g.gen)
		}
	}
	return ""
}

// events renders the hub's events since the last take: "reload apps/x",
// "build-start apps/x", "deployments apps/x build dev start". An event of
// today's types that carries a qualified component fails the test (C2).
func (f *depFake) events(tp *tape) []string {
	f.t.Helper()
	var out []string
	for {
		select {
		case e, ok := <-tp.ch:
			if !ok {
				return append(out, "(tape evicted)")
			}
			if e.Type != "deployments" && strings.Contains(e.Component, "+") {
				f.t.Errorf("%s event with a qualified component %q (rule C2)", e.Type, e.Component)
			}
			s := e.Type + " " + e.Component
			if a, ok := e.Data.(deployActivity); ok {
				s += " " + a.Op + " " + a.Deployment
				if a.Phase != "" {
					s += " " + a.Phase
				}
			}
			out = append(out, s)
		default:
			return out
		}
	}
}

// ---- the seam rows ----

const (
	bsX = "build-start apps/x"
	boX = "build-ok apps/x"
)

func dbuild(dep, phase string) string { return "deployments apps/x build " + dep + " " + phase }

// withDev is rows 31 and 32's world: main pinned to c1 (the primary), live
// reload on dev, both running.
func withDev(t *testing.T) (*depFake, *tape) {
	f, tp := newDepFake(t, "apps/x")
	f.set("apps/x", "main", "c1")
	f.set("apps/x", "dev", "worktree")
	if got := []string{f.ensure("apps/x", "main"), f.ensure("apps/x", "dev")}; !equalStrings(got, []string{"g1", "g1"}) {
		t.Fatalf("ensure main, dev: %q", got)
	}
	f.settle()
	wantLog := []string{"build apps/x main@c1", "start apps/x main g1 @c1", "build apps/x dev@worktree", "start apps/x dev g1 @worktree"}
	if got := f.takeLog(); !equalStrings(got, wantLog) {
		t.Fatalf("setup effects:\n got %q\nwant %q", got, wantLog)
	}
	if got, want := f.events(tp), []string{bsX, boX, dbuild("dev", "start"), dbuild("dev", "ok")}; !equalStrings(got, want) {
		t.Fatalf("setup events:\n got %q\nwant %q", got, want)
	}
	return f, tp
}

// covers P8 P13 P7 P17 SC-INBOUND — 15-test-plan §2.5 rows 31–34 and 37: a
// save or a crash reaches only its own deployment, and its activity rides
// only the deployments type (rule C2); Ensure means the primary; Stop stops
// every deployment; a reassignment restarts the new primary first, then the
// old one as non-primary, from their kept artifacts.
func TestDeploymentSeamRows(t *testing.T) {
	t.Run("31 a save with live reload on dev rebuilds only dev, announced only as deployments events", func(t *testing.T) {
		f, tp := withDev(t)
		f.r.ChangedDeployment(f.comps["apps/x"], "dev") // the watcher, live reload on dev (07-runtime §6)
		f.settle()
		if got, want := f.takeLog(), []string{"build apps/x dev@worktree", "start apps/x dev g2 @worktree", "stop apps/x dev g1"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if got, want := f.events(tp), []string{dbuild("dev", "start"), dbuild("dev", "ok")}; !equalStrings(got, want) {
			t.Errorf("events:\n got %q\nwant %q", got, want)
		}
		if got := f.ensure("apps/x", "main"); got != "g1" {
			t.Errorf("main after dev's save: %s, want g1 untouched", got)
		}
	})

	t.Run("32 a crash of dev restarts only dev", func(t *testing.T) {
		f, tp := withDev(t)
		f.crash("apps/x", "dev")
		f.settle()
		if got := f.ensure("apps/x", "dev"); got != "g2" {
			t.Errorf("dev after its crash: %s, want g2", got)
		}
		f.settle()
		if got, want := f.takeLog(), []string{"build apps/x dev@worktree", "start apps/x dev g2 @worktree"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if got, want := f.events(tp), []string{dbuild("dev", "start"), dbuild("dev", "ok")}; !equalStrings(got, want) {
			t.Errorf("events:\n got %q\nwant %q", got, want)
		}
		if got := f.ensure("apps/x", "main"); got != "g1" {
			t.Errorf("main after dev's crash: %s, want g1", got)
		}
		if st := f.r.DeploymentStatus("apps/x", "dev"); st.State != "healthy" || st.Gen != 2 || st.Serving != "work-tree" {
			t.Errorf("DeploymentStatus(dev) = %+v, want healthy g2 on the work tree", st)
		}
	})

	t.Run("33 Ensure always reaches the primary", func(t *testing.T) {
		f, _ := withDev(t)
		c := f.comps["apps/x"]
		ctx := context.Background()
		if sock, err := f.r.Ensure(ctx, c); err != nil || !strings.Contains(sock, "/main/") {
			t.Errorf("Ensure with main primary = %q, %v; want main's generation", sock, err)
		}
		f.setPrimary("apps/x", "dev") // the plane reassigned; Primary answers at once
		if sock, err := f.r.Ensure(ctx, c); err != nil || !strings.Contains(sock, "/dev/") {
			t.Errorf("Ensure with dev primary = %q, %v; want dev's generation", sock, err)
		}
		release := f.r.Track(c.Path)
		s := f.r.existingStateOf(c.Path, "dev")
		s.mu.Lock()
		active := s.active
		s.mu.Unlock()
		release()
		if active != 1 {
			t.Errorf("Track with dev primary counted %d on dev, want 1", active)
		}
		if f.r.state(c.Path) != s {
			t.Error("the primary's state isn't dev's")
		}
	})

	t.Run("34 Stop stops every deployment", func(t *testing.T) {
		f, _ := withDev(t)
		f.r.Stop("apps/x")
		f.settle()
		if got, want := f.takeLog(), []string{"stop apps/x dev g1", "stop apps/x main g1"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		for _, dep := range []string{"main", "dev"} {
			if st := f.r.DeploymentStatus("apps/x", dep); st.State != "idle" {
				t.Errorf("%s after Stop: %+v, want idle", dep, st)
			}
		}
	})

	t.Run("34r StopDeployment removes one deployment, even mid-build", func(t *testing.T) {
		f, tp := withDev(t)
		f.mu.Lock()
		f.holdFor, f.hold = dk("apps/x", "dev"), make(chan struct{})
		f.mu.Unlock()
		f.r.ChangedDeployment(f.comps["apps/x"], "dev") // a save: dev builds, parked
		waitUntil(t, "dev's build parked", func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			return f.holdFor == ""
		})
		f.mu.Lock()
		delete(f.code, dk("apps/x", "dev")) // the plane's removal commits first
		f.mu.Unlock()
		f.r.StopDeployment("apps/x", "dev") // then stops it, while it builds
		close(f.hold)
		waitUntil(t, "the generation built for the removed dev stopped", func() bool {
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, g := range f.gens {
				if g.dep == "dev" && g.gen == 2 && g.closed {
					return true
				}
			}
			return false
		})
		f.settle()
		if got, want := f.takeLog(), []string{"build apps/x dev@worktree", "start apps/x dev g2 @worktree", "stop apps/x dev g1", "stop apps/x dev g2"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if s := f.r.existingStateOf("apps/x", "dev"); s != nil {
			t.Error("the removed deployment keeps runner state")
		}
		if got := f.ensure("apps/x", "main"); got != "g1" {
			t.Errorf("main after dev's removal: %s, want g1", got)
		}
		f.events(tp)
	})

	t.Run("37 a reassignment restarts dev as the primary first, then main, from their artifacts", func(t *testing.T) {
		f, tp := newDepFake(t, "apps/x")
		f.set("apps/x", "main", "c1")
		f.set("apps/x", "dev", "c2")
		f.ensure("apps/x", "main")
		f.ensure("apps/x", "dev")
		f.settle()
		f.takeLog()
		f.events(tp)

		f.setPrimary("apps/x", "dev") // the plane's commit
		if err := f.r.Reassign(context.Background(), f.comps["apps/x"], "main", "dev"); err != nil {
			t.Fatalf("Reassign: %v", err)
		}
		f.settle()
		want := []string{"start apps/x dev g2 @c2", "start apps/x main g2 @c1", "stop apps/x dev g1", "stop apps/x main g1"}
		if got := f.takeLog(); !equalStrings(got, want) {
			t.Errorf("effects (no build: both from their artifacts):\n got %q\nwant %q", got, want)
		}
		wantEv := []string{bsX, boX, dbuild("main", "start"), dbuild("main", "ok"), "reload apps/x", "deployments apps/x reload main"}
		if got := f.events(tp); !equalStrings(got, wantEv) {
			t.Errorf("events:\n got %q\nwant %q", got, wantEv)
		}
		if v := f.viewOf("apps/x", "dev", 2); v == nil || v.Deployment != "" {
			t.Errorf("dev's g2 spawned from %+v, want the primary's view", v)
		}
		if v := f.viewOf("apps/x", "main", 2); v == nil || v.Deployment != "main" {
			t.Errorf("main's g2 spawned from %+v, want a view naming main (non-primary)", v)
		}
		if b := f.r.Inspect(); len(b) != 1 || b[0].Path != "apps/x" || b[0].Gen != 2 || b[0].Checkpoint != pinTree("c2") || b[0].Deployment != "" {
			t.Errorf("Inspect = %+v, want dev's row as apps/x's", b)
		}
		if b := f.r.InspectDeployments(); len(b) != 1 || b[0].Deployment != "main" || b[0].Checkpoint != pinTree("c1") {
			t.Errorf("InspectDeployments = %+v, want main's row", b)
		}
		if st := statusOf(f.r, "apps/x"); st != "healthy g2" {
			t.Errorf("Status row %q, want dev's healthy g2", st)
		}
		if got := f.r.StatusDeployments(); len(got) != 1 || len(got["apps/x"]) != 1 || got["apps/x"]["main"] == nil {
			t.Errorf("StatusDeployments = %v, want main alone", got)
		}
	})

	t.Run("a deploy onto dev announces a deployments reload, never build-* or a bare reload", func(t *testing.T) {
		f, tp := withDev(t)
		f.set("apps/x", "dev", "c3") // the plane pins dev at request time (07-runtime §8.2)
		var progress []string
		err := f.r.Deploy(context.Background(), f.comps["apps/x"], "dev", Code{Tree: pinTree("c3")}, nil,
			func(phase, result string, err error) { progress = append(progress, phase+" "+result) })
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		f.settle()
		if got, want := f.takeLog(), []string{"build apps/x dev@c3", "start apps/x dev g2 @c3", "stop apps/x dev g1"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if got, want := f.events(tp), []string{"deployments apps/x reload dev"}; !equalStrings(got, want) {
			t.Errorf("events:\n got %q\nwant %q", got, want)
		}
		if want := []string{"build running", "start running", "swap running", "swap ok"}; !equalStrings(progress, want) {
			t.Errorf("progress %q, want %q", progress, want)
		}
	})

	t.Run("names the record doesn't hold are refused and create no state", func(t *testing.T) {
		f, _ := newDepFake(t, "apps/x")
		if got := f.ensure("apps/x", "nope"); !strings.Contains(got, `has no deployment "nope"`) {
			t.Errorf("EnsureDeployment(nope) = %s", got)
		}
		f.r.TrackDeployment("apps/x", "nope")()
		f.r.ChangedDeployment(f.comps["apps/x"], "nope")
		f.r.StopDeployment("apps/x", "nope")
		if err := f.r.Deploy(context.Background(), f.comps["apps/x"], "nope", Code{WorkTree: true}, nil, nil); !errors.Is(err, util.ErrNoDeployment) {
			t.Errorf("Deploy(nope) = %v, want ErrNoDeployment", err)
		}
		if n := len(f.r.allStates("")); n != 0 {
			t.Errorf("%d runner states after unknown names", n)
		}
	})
}

// ---- the admission caps ----

// covers P25 T10 — TestNonPrimaryAdmissionCaps (07-runtime §10.3,
// NP-07-7): a non-primary deployment's start is refused, as an sbx refusal
// reported on its deployments build event and in the failure ring, past 3
// running per tile, 12 running per workspace, or 24 running or building per
// workspace; primaries and a deployment's own blue/green never count.
func TestNonPrimaryAdmissionCaps(t *testing.T) {
	t.Run("3 per tile", func(t *testing.T) {
		f, tp := newDepFake(t, "apps/x")
		for _, d := range []string{"a", "b", "c", "d"} {
			f.set("apps/x", d, "worktree")
		}
		for _, d := range []string{"main", "a", "b", "c"} {
			if got := f.ensure("apps/x", d); got != "g1" {
				t.Fatalf("ensure %s: %s", d, got)
			}
		}
		f.events(tp)
		got := f.ensure("apps/x", "d")
		if !strings.Contains(got, "apps/x already runs 3 non-primary deployments") {
			t.Errorf("a 4th non-primary deployment: %s", got)
		}
		if ev := f.events(tp); len(ev) != 2 || ev[1] != dbuild("d", "error") {
			t.Errorf("events %q, want d's build start and error", ev)
		}
		fl := f.r.Sandboxes.Failures(sbx.Filter{})
		if len(fl) != 1 || fl[0].Stage != sbx.Refused || fl[0].Tile != "apps/x" {
			t.Errorf("failure ring %+v, want one refusal for apps/x", fl)
		}
		// blue/green over its own generation is always admitted
		f.r.ChangedDeployment(f.comps["apps/x"], "c")
		f.settle()
		if got := f.ensure("apps/x", "c"); got != "g2" {
			t.Errorf("c's next generation: %s, want g2", got)
		}
	})

	t.Run("12 running per workspace", func(t *testing.T) {
		var tiles []string
		for i := 0; i < 5; i++ {
			tiles = append(tiles, fmt.Sprintf("apps/t%d", i))
		}
		f, _ := newDepFake(t, tiles...)
		n := 0
		for _, tile := range tiles {
			for _, d := range []string{"a", "b", "c"} {
				f.set(tile, d, "worktree")
				got := f.ensure(tile, d)
				if n++; n <= MaxNonPrimaryRunning && got != "g1" {
					t.Fatalf("start %d (%s %s): %s", n, tile, d, got)
				} else if n > MaxNonPrimaryRunning && !strings.Contains(got, "12 non-primary backends already run") {
					t.Errorf("start %d (%s %s) past the running cap: %s", n, tile, d, got)
				}
			}
		}
		if got := f.ensure("apps/t4", "main"); got != "g1" {
			t.Errorf("a primary past the non-primary cap: %s, want it started", got)
		}
	})

	t.Run("24 running or building per workspace", func(t *testing.T) {
		f, _ := newDepFake(t, "apps/x")
		for i := 0; i < MaxNonPrimaryPerWorkspace; i++ { // builds in flight count too
			tile := fmt.Sprintf("apps/w%d", i/3)
			f.r.stateOf(tile, fmt.Sprintf("d%d", i%3)).building = true
		}
		v := &registry.Component{Path: "apps/x", Deployment: "dev"}
		err := f.r.admit(v, "dev")
		if !errors.Is(err, ErrAdmission) || !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "workspace already runs 24") {
			t.Errorf("admit past 24: %v", err)
		}
		if err := f.r.admit(&registry.Component{Path: "apps/x"}, "main"); err != nil {
			t.Errorf("the primary's admission: %v", err)
		}
	})
}

// ---- spawn setup: env, logs, run dirs, instance tokens ----

// spawnRunner is a runner for spawnSetup alone: no engine, no process.
func spawnRunner(t *testing.T, iso bool, primary string) (*Runner, *registry.Component) {
	t.Helper()
	root := t.TempDir()
	c := &registry.Component{Path: "apps/x", Dir: filepath.Join(root, "apps", "x"), Manifest: registry.Manifest{Runtime: "go"}}
	r := &Runner{Root: root, RunDir: filepath.Join(t.TempDir(), "run"), Isolate: iso, Rootfs: "/nonexistent-rootfs",
		states: map[string]*state{}}
	r.Primary = func(string) string { return primary }
	r.EnvFor = func(c *registry.Component, dep string) ([]string, map[string]ResBind) {
		return []string{"XBIN_RES_DB=" + c.Dir + "/data/" + dep}, nil
	}
	return r, c
}

func nonPrimary(c *registry.Component, dep string) *registry.Component {
	v := *c
	v.Deployment = dep
	return &v
}

// covers P17 — TestBackendEnvPerDeployment (15-test-plan §3.2):
// XBIN_DEPLOYMENT is set only for a backend that isn't the primary when it
// spawns (the role rule, 11-contract §5), with its name; XBIN_COMPONENT stays
// the bare tile path; the host-env allow-list and every other variable are
// today's; EnvFor's env is the deployment's own. Through a real generation
// on the host, main's backend sees no XBIN_DEPLOYMENT, and no other
// deployment runs without a sandbox.
func TestBackendEnvPerDeployment(t *testing.T) {
	for _, iso := range []bool{false, true} {
		for _, tc := range []struct {
			primary, dep string
			view         func(*registry.Component) *registry.Component
			marker       string // XBIN_DEPLOYMENT's value; "" = absent
		}{
			{"main", "main", func(c *registry.Component) *registry.Component { return c }, ""},
			{"main", "dev", func(c *registry.Component) *registry.Component { return nonPrimary(c, "dev") }, "dev"},
			{"dev", "dev", func(c *registry.Component) *registry.Component { return c }, ""},
			{"dev", "main", func(c *registry.Component) *registry.Component { return nonPrimary(c, "main") }, "main"},
		} {
			name := fmt.Sprintf("isolate=%v primary=%s spawning=%s", iso, tc.primary, tc.dep)
			r, c := spawnRunner(t, iso, tc.primary)
			sp, err := r.spawnSetup(tc.view(c), tc.dep, 3)
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			want := append(backendEnv(iso),
				"XBIN_SOCKET="+sp.sock,
				"XBIN_COMPONENT=apps/x",
				"XBIN_GATEWAY="+filepath.Join(r.RunDir, "gateway.sock"),
				"XBIN_TOKEN="+sp.token)
			if tc.marker != "" {
				want = append(want, "XBIN_DEPLOYMENT="+tc.marker)
			}
			want = append(want, "XBIN_RES_DB="+c.Dir+"/data/"+tc.dep)
			if !equalStrings(sp.env, want) {
				t.Errorf("%s: env\n got %q\nwant %q", name, sp.env, want)
			}
			if len(sp.token) != 48 {
				t.Errorf("%s: token %q, want 24 random bytes in hex", name, sp.token)
			}
		}
	}
	r, c := spawnRunner(t, true, "main")
	if _, err := r.spawnSetup(nonPrimary(c, "dev"), "qa", 1); err == nil {
		t.Error("a view of dev spawned deployment qa")
	}

	t.Run("a real generation on the host", func(t *testing.T) {
		h := newHostWorld(t, 0)
		if got := h.ensure(); got != "g1" {
			t.Fatalf("ensure main: %s", got)
		}
		env := h.env(1)
		if env["XBIN_COMPONENT"] != "apps/x" || env["XBIN_TOKEN"] == "" {
			t.Errorf("main's backend env %v", env)
		}
		if v, ok := env["XBIN_DEPLOYMENT"]; ok {
			t.Errorf("main, the primary, sees XBIN_DEPLOYMENT=%q", v)
		}
		h.mu.Lock()
		h.code["dev"] = Code{WorkTree: true}
		h.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := h.r.EnsureDeployment(ctx, h.c, "dev"); err == nil || !strings.Contains(err.Error(), "runs only in a sandbox (--isolate)") {
			t.Errorf("dev without isolation: %v", err)
		}
		if s := h.r.existingStateOf("apps/x", "dev"); s != nil {
			t.Error("the refused deployment got runner state")
		}
	})
}

// covers P6 P17 — TestLogPathsPerDeployment (15-test-plan §3.2): main keeps
// .xbin/log/<CompKey>.log; any other deployment writes
// .xbin/deploy/<TileKey>/d/<name>/backend.log, created for it; the crash
// loop's words and a failed deploy's output name and reach the deployment's
// own log.
func TestLogPathsPerDeployment(t *testing.T) {
	r, c := spawnRunner(t, true, "main")
	sp, err := r.spawnSetup(c, "main", 1)
	if err != nil || sp.log != filepath.Join(r.Root, ".xbin/log/"+ckX+".log") {
		t.Errorf("main's log %q (%v)", sp.log, err)
	}
	if _, err := os.Lstat(filepath.Join(r.Root, ".xbin/deploy")); err == nil {
		t.Error("main's spawn made .xbin/deploy")
	}
	devLog := filepath.Join(r.Root, ".xbin/deploy/"+tkX+"/d/dev/backend.log")
	sp, err = r.spawnSetup(nonPrimary(c, "dev"), "dev", 1)
	if err != nil || sp.log != devLog {
		t.Errorf("dev's log %q (%v), want %q", sp.log, err, devLog)
	}
	if fi, err := os.Stat(filepath.Dir(devLog)); err != nil || !fi.IsDir() {
		t.Errorf("dev's log directory: %v", err)
	}
	if got := crashLoopError("apps/x", "dev", Code{Tree: pinTree("c1")}, 3).Error(); !strings.HasSuffix(got, "see .xbin/deploy/"+tkX+"/d/dev/backend.log") {
		t.Errorf("dev's crash loop: %q", got)
	}
	r.logDeployFailure("apps/x", "dev", Code{Tree: pinTree("c1")}, &BuildError{Output: "x.go:1: nope"})
	if b, err := os.ReadFile(devLog); err != nil || !strings.Contains(string(b), "x.go:1: nope") {
		t.Errorf("dev's log after a failed deploy: %q %v", b, err)
	}
	if _, err := os.Lstat(filepath.Join(r.Root, ".xbin/log/"+ckX+".log")); err == nil {
		t.Error("dev's failure reached main's log")
	}

	t.Run("a real generation on the host", func(t *testing.T) {
		h := newHostWorld(t, 0)
		h.ensure()
		if b, err := os.ReadFile(filepath.Join(h.root, ".xbin/log/"+ckX+".log")); err != nil || !strings.Contains(string(b), "--- gen 1 start ") {
			t.Errorf("main's log: %q %v", b, err)
		}
		if _, err := os.Lstat(filepath.Join(h.root, ".xbin/deploy")); err == nil {
			t.Error("a zero-state generation made .xbin/deploy")
		}
	})
}

// covers T18 P6 — TestRunDirPerDeployment (06-security T18): a non-primary
// deployment's socket dir is its own sibling of main's, never inside it, so
// its sandbox binds neither main's dir nor main's socket; main's is today's
// <RunDir>/<CompKey>; removing the deployment removes its dir alone.
func TestRunDirPerDeployment(t *testing.T) {
	r, c := spawnRunner(t, true, "main")
	spMain, err := r.spawnSetup(c, "main", 4)
	if err != nil {
		t.Fatal(err)
	}
	spDev, err := r.spawnSetup(nonPrimary(c, "dev"), "dev", 4)
	if err != nil {
		t.Fatal(err)
	}
	if spMain.dir != filepath.Join(r.RunDir, ckX) || spMain.sock != filepath.Join(r.RunDir, ckX, "g4.sock") {
		t.Errorf("main's run dir %q, socket %q", spMain.dir, spMain.sock)
	}
	if spDev.dir != filepath.Join(r.RunDir, sockDev) || spDev.sock != filepath.Join(r.RunDir, sockDev, "g4.sock") {
		t.Errorf("dev's run dir %q, socket %q", spDev.dir, spDev.sock)
	}
	within := func(p, dir string) bool { return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator)) }
	spec := r.launchSpec(nonPrimary(c, "dev"), "/bin", spDev.dir, spDev.env, sandbox.EgressPolicy{}, "")
	sawOwn := false
	for _, b := range spec.Binds {
		if within(b.Src, spMain.dir) || within(b.Dst, spMain.dir) || within(spMain.dir, b.Src) && b.Src != "/" {
			t.Errorf("dev's sandbox binds %+v, which holds or is main's run dir", b)
		}
		sawOwn = sawOwn || b.Src == spDev.dir && b.Dst == spDev.dir && !b.RO
	}
	if !sawOwn {
		t.Errorf("dev's sandbox doesn't bind its own run dir read-write: %+v", spec.Binds)
	}
	r.stateOf("apps/x", "dev")
	r.stateOf("apps/x", "main")
	r.StopDeployment("apps/x", "dev")
	if _, err := os.Lstat(spDev.dir); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("dev's run dir after its removal: %v", err)
	}
	if _, err := os.Lstat(spMain.dir); err != nil {
		t.Errorf("main's run dir after dev's removal: %v", err)
	}
}

// covers P12 T3 — TestInstanceTokenRegisteredWithDeployment (15-test-plan
// §3.2, 06-security T3c): a generation's instance token is registered with
// its tile and deployment, from the runner's state, and authenticates as
// that tile's principal bound to it (main is ""); until auth can bind a
// token to a deployment, no deployment but main starts.
func TestInstanceTokenRegisteredWithDeployment(t *testing.T) {
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	who := func(tok string) (auth.Principal, bool) {
		req := httptest.NewRequest("GET", "/x", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		return a.FromRequest(req)
	}
	r, c := spawnRunner(t, true, "main")
	r.Auth = a
	r.registerInstance("tok-main", "apps/x", "main")
	if p, ok := who("tok-main"); !ok || p.Component != "apps/x" || p.Via != "instance" || p.Deployment != "" {
		t.Errorf("main's token: %+v %v", p, ok)
	}
	if _, can := any(a).(deploymentRegistrar); can {
		r.registerInstance("tok-dev", "apps/x", "dev")
		if p, ok := who("tok-dev"); !ok || p.Component != "apps/x" || p.Via != "instance" || p.Deployment != "dev" {
			t.Errorf("dev's token: %+v %v", p, ok)
		}
		a.RevokeInstance("tok-dev")
		if _, ok := who("tok-dev"); ok {
			t.Error("dev's revoked token still authenticates")
		}
		if _, err := r.spawnSetup(nonPrimary(c, "dev"), "dev", 1); err != nil {
			t.Errorf("dev's spawn: %v", err)
		}
	} else {
		// auth before per-deployment instance tokens (WP-32): dev never starts
		if _, err := r.spawnSetup(nonPrimary(c, "dev"), "dev", 1); err == nil || !strings.Contains(err.Error(), "can't be bound") {
			t.Errorf("dev's spawn without a deployment-bound token: %v", err)
		}
		r.registerInstance("tok-dev", "apps/x", "dev")
		if p, ok := who("tok-dev"); ok {
			t.Errorf("dev's token authenticates as %+v", p)
		}
	}

	t.Run("a real generation's token", func(t *testing.T) {
		h := newHostWorld(t, 0)
		h.ensure()
		tok := h.token("main")
		if p, ok := h.who(tok); !ok || p.Component != "apps/x" || p.Via != "instance" || p.Deployment != "" {
			t.Errorf("main's generation authenticates as %+v %v", p, ok)
		}
		if env := h.env(1); env["XBIN_TOKEN"] != tok {
			t.Errorf("the backend's XBIN_TOKEN %q isn't its registered token", env["XBIN_TOKEN"])
		}
		h.r.StopAll()
		if _, ok := h.who(tok); ok {
			t.Error("the token outlived its process")
		}
	})
}

// covers P12 T3 — 09-fabric §10's TestDrainAuthority (§3.7): a draining
// generation acts only as its own deployment. Its instance token stays bound
// to the deployment it was spawned for through a blue/green drain and
// through a reassignment of the primary, until its process exits, and a
// reassignment never registers it as another deployment: the role, and so
// the clamp on its next call, is looked up per request (09-fabric §3.6). On
// an xbind without isolation the old primary can't restart as non-primary,
// so it stops rather than keep the primary's wiring.
func TestDrainAuthority(t *testing.T) {
	h := newHostWorld(t, 2*time.Second)
	if got := h.ensure(); got != "g1" {
		t.Fatalf("ensure: %s", got)
	}
	tok1 := h.token("main")

	h.r.Changed(h.c) // a save: g2 replaces g1, which drains
	waitUntil(t, "g2 healthy", func() bool { return statusOf(h.r, "apps/x") == "healthy g2" })
	if p, ok := h.who(tok1); !ok || p.Component != "apps/x" || p.Deployment != "" {
		t.Errorf("draining g1 authenticates as %+v %v, want main of apps/x", p, ok)
	}
	waitUntil(t, "g1's token revoked at its exit", func() bool { _, ok := h.who(tok1); return !ok })

	tok2 := h.token("main")
	h.mu.Lock()
	h.primary, h.code["dev"] = "dev", Code{WorkTree: true} // the plane reassigned
	h.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- h.r.Reassign(context.Background(), h.c, "main", "dev") }()
	waitUntil(t, "g2 stopping", func() bool {
		s := h.r.existingStateOf("apps/x", "main")
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.cur == nil
	})
	if p, ok := h.who(tok2); !ok || p.Deployment != "" || p.Component != "apps/x" {
		t.Errorf("g2 after the reassignment authenticates as %+v %v, want main of apps/x until it exits", p, ok)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "runs only in a sandbox") {
		t.Errorf("Reassign on the host: %v, want main's restart refused", err)
	}
	if _, ok := h.who(tok2); ok {
		t.Error("g2's token outlived its process")
	}
}

// hostWorld runs apps/x's Go backend on the host through the shipped start
// (no engine): a fake `go` on PATH emits a wrapper that runs this test
// binary as the backend (TestDeploymentsHelperProcess), which writes its env
// to a file per generation.
type hostWorld struct {
	t       *testing.T
	root    string
	envDir  string
	a       *auth.Auth
	r       *Runner
	c       *registry.Component
	mu      sync.Mutex
	primary string
	code    map[string]Code
}

func newHostWorld(t *testing.T, drain time.Duration) *hostWorld {
	t.Helper()
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh for the fake go")
	}
	testBin, err := os.Executable()
	if err != nil {
		t.Skip("no test binary path:", err)
	}
	h := &hostWorld{t: t, root: t.TempDir(), envDir: t.TempDir(), primary: "main", code: map[string]Code{"main": {WorkTree: true}}}
	tile := filepath.Join(h.root, "apps", "x")
	if err := os.MkdirAll(tile, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tile, "xbin.json"), []byte(`{"runtime":"go"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := t.TempDir()
	wrapper := filepath.Join(fake, "backend.sh")
	writeExec(t, filepath.Join(fake, "go"), "#!/bin/sh\ncp \"$DEPLOY_FAKE_BACKEND\" \"$3\"\n")
	writeExec(t, wrapper, "#!/bin/sh\n"+
		`export GORACE="${GORACE:+$GORACE }atexit_sleep_ms=0"`+"\n"+
		fmt.Sprintf("exec '%s' '-test.run=^TestDeploymentsHelperProcess$'\n", testBin))
	t.Setenv("PATH", fake+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("DEPLOY_FAKE_BACKEND", wrapper)
	t.Setenv(depHelperEnv, h.envDir)
	t.Setenv(depHelperDrain, strconv.Itoa(int(drain/time.Millisecond)))
	if h.a, err = auth.Load(h.root, false); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(h.root)
	if err != nil {
		t.Fatal(err)
	}
	h.r = New(h.root, h.a, events.NewHub(), reg)
	h.r.DeploymentHooks = DeploymentHooks{
		CodeFor: func(tile, dep string) (Code, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			if c, ok := h.code[dep]; ok {
				return c, nil
			}
			return Code{}, util.NoDeployment(tile, dep)
		},
		Primary: func(string) string { h.mu.Lock(); defer h.mu.Unlock(); return h.primary },
	}
	t.Cleanup(h.r.StopAll)
	if rd := h.r.RunDir; !strings.HasPrefix(rd, h.root) {
		t.Cleanup(func() { os.RemoveAll(filepath.Dir(rd)) })
	}
	var ok bool
	if h.c, ok = reg.Component("apps/x"); !ok {
		t.Fatal("apps/x not scanned")
	}
	return h
}

func (h *hostWorld) ensure() string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return answer(h.r.Ensure(ctx, h.c))
}

// token is deployment dep's current generation's instance token.
func (h *hostWorld) token(dep string) string {
	s := h.r.existingStateOf("apps/x", dep)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		h.t.Fatalf("%s has no generation", dep)
	}
	return s.cur.token
}

func (h *hostWorld) who(tok string) (auth.Principal, bool) {
	req := httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	return h.a.FromRequest(req)
}

// env is generation gen's backend env, as it wrote it.
func (h *hostWorld) env(gen int) map[string]string {
	h.t.Helper()
	var b []byte
	waitUntil(h.t, fmt.Sprintf("g%d's env", gen), func() bool {
		var err error
		b, err = os.ReadFile(filepath.Join(h.envDir, fmt.Sprintf("g%d.env", gen)))
		return err == nil && strings.HasSuffix(string(b), "\n.\n")
	})
	out := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.HasPrefix(k, "XBIN_") {
			out[k] = v
		}
	}
	return out
}

const (
	depHelperEnv   = "RUNNER_DEPLOYMENTS_HELPER" // the directory it writes its env to
	depHelperDrain = "RUNNER_DEPLOYMENTS_DRAIN"  // milliseconds it lingers after SIGTERM
)

// covers P12 — no test of its own: the backend's main when hostWorld runs
// this test binary as apps/x's Go backend. It writes its env to
// g<gen>.env, listens on XBIN_SOCKET, and exits a drain delay after SIGTERM
// (a minute at most).
func TestDeploymentsHelperProcess(t *testing.T) {
	dir, sock := os.Getenv(depHelperEnv), os.Getenv("XBIN_SOCKET")
	if dir == "" || sock == "" {
		return
	}
	gen := strings.TrimSuffix(filepath.Base(sock), ".sock")
	body := strings.Join(os.Environ(), "\n") + "\n.\n"
	if err := os.WriteFile(filepath.Join(dir, gen+".env"), []byte(body), 0o644); err != nil {
		os.Exit(3)
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		os.Exit(3)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, os.Interrupt)
	select {
	case <-sig:
		ms, _ := strconv.Atoi(os.Getenv(depHelperDrain))
		time.Sleep(time.Duration(ms) * time.Millisecond)
	case <-time.After(time.Minute):
	}
	os.Exit(0)
}

// ---- the static guard ----

// ensureCallers are the functions that may call Runner.Ensure, which means
// the primary (15-test-plan §2.5 row 33): every inbound edge, and Changed's
// own rebuild. A caller that acts on a named deployment calls
// EnsureDeployment instead, annotated.
var ensureCallers = map[string]bool{
	"internal/proxy/proxy.go ServeHTTP":        true, // /api/ proxy: a bare URL (WP-37 moves qualified ones)
	"internal/proxy/ingress.go ForwardIngress": true, // public HTTP ingress
	"internal/runner/ingress.go DialInto":      true, // L4 streams, hairpin, stream interfaces
	"internal/runner/alwayson.go aoStart":      true, // alwaysOn: the primary's (07-runtime §11)
	"internal/runner/netmux.go ensureProvider": true, // net-provider bring-up
	"internal/runner/runner.go Changed":        true, // Changed's background rebuild of the primary
}

// covers P7 SC-INBOUND — TestEnsureCallSitesPassPrimary (15-test-plan
// §3.13): every call of Runner.Ensure (a method Ensure with two arguments)
// in xbind's code is one of ensureCallers, which mean the primary, and
// every EnsureDeployment call carries a "deployment:" comment on its line or
// the line above, saying which deployment and why.
func TestEnsureCallSitesPassPrimary(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	found := 0
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(repo, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && d.Name() == "testdata" {
				return filepath.SkipDir
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			f, err := parser.ParseFile(fset, p, nil, parser.ParseComments)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(repo, p)
			rel = filepath.ToSlash(rel)
			comments := map[int]string{}
			for _, cg := range f.Comments {
				for _, cm := range cg.List {
					comments[fset.Position(cm.Pos()).Line] += cm.Text
				}
			}
			for _, decl := range f.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					line := fset.Position(call.Pos()).Line
					switch {
					case sel.Sel.Name == "Ensure" && len(call.Args) == 2:
						found++
						if site := rel + " " + fn.Name.Name; !ensureCallers[site] {
							t.Errorf("%s:%d: %s calls Runner.Ensure, which means the primary; name the deployment with EnsureDeployment (annotated), or add the site to ensureCallers if it is an inbound edge", rel, line, fn.Name.Name)
						}
					case sel.Sel.Name == "EnsureDeployment" && len(call.Args) == 3:
						if !strings.Contains(comments[line]+comments[line-1], "deployment:") {
							t.Errorf("%s:%d: an EnsureDeployment call without a \"deployment:\" comment saying which deployment and why", rel, line)
						}
					}
					return true
				})
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if found == 0 {
		t.Fatal("no Runner.Ensure call found: the scan is broken")
	}
}
