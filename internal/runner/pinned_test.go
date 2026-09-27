package runner

// covers P9 P8 P5 T11 T18 C7 P18 SC-ROLLBACK — seam rows 16–22 and 30 of
// 15-test-plan §2.5, and TestArtifactPerCheckpoint (§3.2): every restart
// path of a pinned primary starts the checkpoint its record names, from the
// artifact kept for it, never the work tree; a zero-state tile asks no
// checkpoint question at all; artifacts are kept per checkpoint while
// referenced, and the trees generations bind are reported to GC.
//
// The rows drive the real runner through the fake engine of
// fake_engine_test.go with the deployments plane's runner hooks faked: a
// record (main's code), a materializer and the registry's own View. The
// fake build of a checkpoint leaves what the checkpoint build leaves: its
// artifact directory, .xbin/build/<CompKey>/c/<tree>/{bin,build.json}.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// ckX is apps/x's CompKey, hand-maintained (TestZeroStateKeys' literal).
const ckX = "apps~x-ebdae547"

// ckTree is a checkpoint label's full tree id: the label, padded with zeros
// to 40 hex digits ("c1" → "c100…0").
func ckTree(label string) string { return label + strings.Repeat("0", 40-len(label)) }

// pinWorld is the deployments plane as the runner sees it, over a fake
// engine: one record answering main's code, a materializer, the registry's
// View, and counts of every hook call.
type pinWorld struct {
	t       *testing.T
	f       *fakeEngine
	c       *registry.Component
	reg     *registry.Registry // the plane's registry, which makes the views
	r       *Runner
	isolate bool

	mu     sync.Mutex
	pinned string // main's checkpoint tree; "" = it follows the work tree
	held   error  // CodeFor's answer while the record holds the tile
	calls  map[string]int
	labels map[string]string // tree → label
	views  []*registry.Component
}

func newPinWorld(t *testing.T, man registry.Manifest) (*pinWorld, *tape) {
	t.Helper()
	c := &registry.Component{Path: "apps/x", Manifest: man}
	_, f, tp := newSeamRunner(t, c)
	reg, err := registry.Open(f.root)
	if err != nil {
		t.Fatal(err)
	}
	w := &pinWorld{t: t, f: f, c: c, reg: reg, isolate: true, calls: map[string]int{}, labels: map[string]string{}}
	w.r = w.runner()
	return w, tp
}

// runner is a fresh Runner over the world: what an xbind restart looks like.
func (w *pinWorld) runner() *Runner {
	r := w.f.runner()
	r.Isolate = w.isolate
	r.engine = &engine{build: w.build, start: w.start, healthy: w.f.healthy, stop: w.f.stop, now: w.f.now}
	r.DeploymentHooks = DeploymentHooks{CodeFor: w.codeFor, Materialize: w.materialize, View: w.view}
	return r
}

func (w *pinWorld) count(hook string) {
	w.mu.Lock()
	w.calls[hook]++
	w.mu.Unlock()
}

func (w *pinWorld) callsOf(hook string) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls[hook]
}

// pin points main's record at the checkpoint labelled label.
func (w *pinWorld) pin(label string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pinned = ckTree(label)
	w.labels[w.pinned] = label
}

func (w *pinWorld) label(tree string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if l, ok := w.labels[tree]; ok {
		return l
	}
	return "?" + tree
}

// rootOf is where the plane materializes tile's tree (07-runtime §9).
func (w *pinWorld) rootOf(tile, tree string) string {
	return filepath.Join(w.f.root, ".xbin", "deploy", util.TileKey(tile), tree)
}

func (w *pinWorld) codeFor(tile, dep string) (Code, error) {
	w.count("codeFor")
	w.mu.Lock()
	defer w.mu.Unlock()
	switch {
	case w.held != nil:
		return Code{}, w.held
	case dep != util.MainDeployment:
		return Code{}, util.NoDeployment(tile, dep)
	case w.pinned != "":
		return Code{Tree: w.pinned}, nil
	}
	return Code{WorkTree: true}, nil
}

// materialize (re)creates the checkpoint's tree: its xbin.json is apps/x's
// manifest.
func (w *pinWorld) materialize(tile, tree string) (string, error) {
	w.count("materialize")
	root := w.rootOf(tile, tree)
	b, err := json.Marshal(w.c.Manifest)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	return root, os.WriteFile(filepath.Join(root, "xbin.json"), b, 0o644)
}

// view is the plane's View hook: the registry's view of the checkpoint at
// its materialized tree (WP-18's registry.View, the deployment named).
func (w *pinWorld) view(c *registry.Component, code Code) (*registry.Component, error) {
	w.count("view")
	if code.WorkTree {
		return c, nil
	}
	return w.reg.View(c, registry.ViewCode{Tree: code.Tree, Root: w.rootOf(c.Path, code.Tree)})
}

// artifactDirOf is a checkpoint artifact's directory: the layout
// .xbin/build/<CompKey>/c/<tree>, hand-maintained.
func (w *pinWorld) artifactDirOf(tree string) string {
	return filepath.Join(w.f.root, ".xbin", "build", ckX, "c", tree)
}

// build is the fake engine's build, plus checkpoints: a view with a CodeRoot
// logs "build apps/x main@c1" and leaves the checkpoint's artifact.
func (w *pinWorld) build(c *registry.Component) (string, error) {
	if c.CodeRoot == "" {
		return w.f.build(c) // "build apps/x main@worktree"
	}
	tree := filepath.Base(c.CodeRoot)
	entry := "build " + c.Path + " main@" + w.label(tree)
	f := w.f
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failing("build " + c.Path + " main") {
		f.log = append(f.log, entry+": fail")
		return "", &BuildError{Output: "fake compile error"}
	}
	if _, err := os.Stat(filepath.Join(c.CodeRoot, "xbin.json")); err != nil {
		w.t.Errorf("%s: the checkpoint isn't materialized: %v", entry, err)
	}
	f.log = append(f.log, entry)
	dir := w.artifactDirOf(tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("fake binary"), 0o755); err != nil {
		return "", err
	}
	stamp, _ := json.Marshal(map[string]string{"tile": c.Path, "tree": tree})
	return filepath.Join(dir, "bin"), os.WriteFile(filepath.Join(dir, "build.json"), stamp, 0o644)
}

// start is the fake engine's start, logging what the generation runs:
// "start apps/x main g2 @c1" from c1's artifact, "@worktree" from the work
// tree's build. A checkpoint view with any other entry logs "@MISMATCH".
func (w *pinWorld) start(c *registry.Component, bin string, gen int) (*instance, error) {
	from := "@worktree"
	if c.CodeRoot != "" {
		tree := filepath.Base(c.CodeRoot)
		from = "@" + w.label(tree)
		if bin != filepath.Join(w.artifactDirOf(tree), "bin") {
			from = "@MISMATCH " + bin
		}
		w.mu.Lock()
		w.views = append(w.views, c)
		w.mu.Unlock()
	} else if bin != "fake-bin/"+c.Path {
		from = "@MISMATCH " + bin
	}
	f := w.f
	f.mu.Lock()
	defer f.mu.Unlock()
	inst := &instance{
		gen: gen, sock: fmt.Sprintf("fake-run/%s/g%d.sock", c.Path, gen),
		cmd: &exec.Cmd{}, started: f.clock, waitCh: make(chan struct{}),
	}
	f.gens[inst] = &fakeGen{tile: c.Path, gen: gen}
	f.log = append(f.log, fmt.Sprintf("start %s main g%d %s", c.Path, gen, from))
	return inst, nil
}

// roots renders RootsInUse as labels, waiting (at most 2 s) for it to equal
// want: a generation's hold drops when the crash watch sees it exit.
func (w *pinWorld) roots(want []string) []string {
	deadline := time.Now().Add(2 * time.Second)
	for {
		var got []string
		for _, p := range w.r.RootsInUse() {
			if filepath.Dir(p) == filepath.Dir(w.rootOf(w.c.Path, "x")) {
				got = append(got, w.label(filepath.Base(p)))
			} else {
				got = append(got, "?"+p)
			}
		}
		if equalStrings(got, want) || time.Now().After(deadline) {
			return got
		}
		time.Sleep(time.Millisecond)
	}
}

// step runs one step token on the world; answers collects ensure's.
func (w *pinWorld) step(step string, answers *[]string) {
	t, c := w.t, w.c
	switch {
	case step == "ensure":
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		sock, err := w.r.Ensure(ctx, c)
		cancel()
		*answers = append(*answers, answer(sock, err))
	case strings.HasPrefix(step, "pause "):
		// Pausing live reload pins main to the checkpoint it takes, then
		// moves the running generation onto it (WP-16b's deploy of the same
		// checkpoint; here the restart that reads the record).
		w.pin(strings.TrimPrefix(step, "pause "))
		w.r.ChangedDeployment(c, util.MainDeployment)
	case step == "save":
		// The watcher after live-reload gating (07-runtime §6): only the
		// live reload target rebuilds, and a paused tile has none.
		w.mu.Lock()
		follows := w.pinned == ""
		w.mu.Unlock()
		if follows {
			w.r.ChangedDeployment(c, util.MainDeployment)
		}
	case step == "grant":
		w.r.ChangedTile(c) // OnGrantChange (internal/boot/boot.go)
	case step == "crash":
		w.f.crash(c.Path, util.MainDeployment)
	case strings.HasPrefix(step, "reap+"):
		d, err := time.ParseDuration(step[len("reap+"):])
		if err != nil {
			t.Fatalf("step %q: %v", step, err)
		}
		w.f.advance(d)
		w.r.reapOnce()
	case step == "seal":
		w.f.seal(c.Path, true)
		w.r.Stop(c.Path)
	case step == "unseal":
		w.f.seal(c.Path, false)
	case step == "restart", step == "restart-without-isolate":
		w.r.StopAll()
		w.isolate = step == "restart"
		w.r = w.runner()
	case step == "drop-xbin": // the loss of .xbin/: artifacts and materialized trees
		if err := os.RemoveAll(filepath.Join(w.f.root, ".xbin")); err != nil {
			t.Fatal(err)
		}
	case step == "hold-record":
		w.mu.Lock()
		w.held = errors.New("apps/x: the deployment record can't be used")
		w.mu.Unlock()
	case step == "fail-next-build":
		w.f.failNext("build", c.Path, util.MainDeployment, 1)
	default:
		t.Fatalf("unknown step %q", step)
	}
	settle(t, w.r, w.f)
}

type pinRow struct {
	id     string
	plain  bool // no "ensure; pause c1" prefix: a zero-state tile
	steps  []string
	log    []string // the fake log after the prefix
	tape   []string // hub events after the prefix
	ensure []string // each ensure step's answer after the prefix
	status string
	roots  []string // RootsInUse after the last step, as labels
}

const (
	bldWT = "build apps/x main@worktree"
	bldC1 = "build apps/x main@c1"
)

func stc(g int, from string) string { return fmt.Sprintf("start apps/x main g%d @%s", g, from) }

// pinnedCrashLoop is the crash-loop breaker's text for pinned code.
const pinnedCrashLoop = "backend crash-looping (3 exits); deploy a fixed checkpoint or restart it — see .xbin/log/" + ckX + ".log"

// pinRows are 15-test-plan §2.5's rows 16–22 and 30, and the fail-closed
// cases of the same restart paths. Unless plain, every row starts with
// "ensure; pause c1": g1 builds the work tree, then g2 runs c1, built once
// from its materialized tree. Step tokens are pinWorld.step's.
var pinRows = []pinRow{
	{id: "16 a save while live reload is paused builds nothing and says nothing",
		steps:  []string{"save", "ensure"},
		ensure: []string{"g2"}, status: "healthy g2", roots: []string{"c1"}},
	{id: "17 a crash restarts the checkpoint from its artifact, with no build",
		steps: []string{"crash", "ensure"},
		log:   []string{stc(3, "c1")}, tape: []string{bs, bo},
		ensure: []string{"g3"}, status: "healthy g3", roots: []string{"c1"}},
	{id: "18 a reaped backend restarts the checkpoint from its artifact",
		steps: []string{"reap+31m", "ensure"},
		log:   []string{sp(2), stc(3, "c1")}, tape: []string{bs, bo},
		ensure: []string{"g3"}, status: "healthy g3", roots: []string{"c1"}},
	{id: "19 a grant change restarts the checkpoint, never a build of the work tree",
		steps: []string{"grant"},
		log:   []string{stc(3, "c1"), sp(2)}, tape: []string{bs, bo},
		status: "healthy g3", roots: []string{"c1"}},
	{id: "20 after an xbind restart the first request starts the checkpoint's artifact",
		steps: []string{"restart", "ensure"},
		log:   []string{sp(2), stc(1, "c1")}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1", roots: []string{"c1"}},
	{id: "21 after the loss of .xbin/ the checkpoint is materialized again and rebuilt",
		steps: []string{"drop-xbin", "restart", "ensure"},
		log:   []string{sp(2), bldC1, stc(1, "c1")}, tape: []string{bs, bo},
		ensure: []string{"g1"}, status: "healthy g1", roots: []string{"c1"}},
	{id: "22 unsealing restarts the checkpoint from its artifact",
		steps: []string{"seal", "unseal", "ensure"},
		log:   []string{sp(2), stc(3, "c1")}, tape: []string{bs, bo},
		ensure: []string{"g3"}, status: "healthy g3", roots: []string{"c1"}},
	{id: "22s a sealed pinned tile stays down until unsealed",
		steps: []string{"seal", "ensure"},
		log:   []string{sp(2)},
		// Stop leaves Status the spent generation, as today (row 12s)
		ensure: []string{"error: component apps/x is not enabled"}, status: "idle g2"},
	{id: "3p a pinned crash loop says to deploy or restart; a save doesn't clear it, a grant does",
		steps:  []string{"crash", "ensure", "crash", "ensure", "crash", "ensure", "save", "ensure", "grant"},
		log:    []string{stc(3, "c1"), stc(4, "c1"), stc(5, "c1")},
		tape:   []string{bs, bo, bs, bo, "build-error apps/x: " + pinnedCrashLoop, bs, bo},
		ensure: []string{"g3", "g4", "error: " + pinnedCrashLoop, "error: " + pinnedCrashLoop},
		status: "healthy g5", roots: []string{"c1"}},
	{id: "C7 a record that holds its tile starts nothing, not the work tree",
		steps:  []string{"hold-record", "crash", "ensure"},
		tape:   []string{bs, "build-error apps/x: apps/x: the deployment record can't be used"},
		ensure: []string{"error: apps/x: the deployment record can't be used"},
		status: "failed g2 · apps/x: the deployment record can't be used"},
	{id: "P18 without isolation a pinned backend is refused, never run as the work tree",
		steps:  []string{"restart-without-isolate", "ensure"},
		log:    []string{sp(2)},
		tape:   []string{bs, "build-error apps/x: " + noIsolate},
		ensure: []string{"error: " + noIsolate}, status: "failed g0 · " + noIsolate},
	{id: "30 a zero-state tile asks no checkpoint question on any restart path",
		plain: true,
		steps: []string{"ensure", "save", "grant", "crash", "ensure", "reap+31m", "ensure", "restart", "ensure"},
		log: []string{bldWT, stc(1, "worktree"), bldWT, stc(2, "worktree"), sp(1), bldWT, stc(3, "worktree"), sp(2),
			bldWT, stc(4, "worktree"), sp(4), bldWT, stc(5, "worktree"), sp(5), bldWT, stc(1, "worktree")},
		tape:   []string{bs, bo, bs, bo, bs, bo, bs, bo, bs, bo, bs, bo},
		ensure: []string{"g1", "g4", "g5", "g1"}, status: "healthy g1"},
}

const noIsolate = "apps/x is pinned to checkpoint c:c100000, and a pinned backend runs only with --isolate: it isn't started, and its work tree never runs in its place"

// covers P9 P8 P5 T11 C7 P18 — 15-test-plan §2.5 rows 16–22 and 30: every
// restart path runs the record's checkpoint, from its kept artifact.
func TestPinnedSeamRows(t *testing.T) {
	for _, row := range pinRows {
		t.Run(row.id, func(t *testing.T) { runPinRow(t, row) })
	}
}

func runPinRow(t *testing.T, row pinRow) {
	w, tp := newPinWorld(t, registry.Manifest{Runtime: "go"})
	var answers []string
	if !row.plain {
		w.step("ensure", &answers)
		w.step("pause c1", &answers)
		checkPaused(t, w, tp, answers)
		answers = nil
	}
	for _, step := range row.steps {
		w.step(step, &answers)
	}

	if got := w.f.takeLog(); !equalStrings(got, row.log) {
		t.Errorf("fake log:\n got %q\nwant %q", got, row.log)
	}
	if got := tp.take(); !equalStrings(got, row.tape) {
		t.Errorf("tape:\n got %q\nwant %q", got, row.tape)
	}
	if !equalStrings(answers, row.ensure) {
		t.Errorf("ensure answers:\n got %q\nwant %q", answers, row.ensure)
	}
	if got := statusOf(w.r, w.c.Path); got != row.status {
		t.Errorf("status:\n got %q\nwant %q", got, row.status)
	}
	if got := w.roots(row.roots); !equalStrings(got, row.roots) {
		t.Errorf("RootsInUse = %q, want %q", got, row.roots)
	}
	if row.plain {
		checkZeroStateAsksNothing(t, w)
	}
	if strings.HasPrefix(row.id, "21 ") && w.callsOf("materialize") != 2 {
		t.Errorf("the checkpoint was materialized %d times, want twice (again after the loss of .xbin/)", w.callsOf("materialize"))
	}
}

// checkPaused checks the "ensure; pause c1" prefix: the work tree's g1, then
// c1 built once from its materialized tree and started as g2, through the
// registry's shared view of it; Inspect names the checkpoint g2 runs.
func checkPaused(t *testing.T, w *pinWorld, tp *tape, answers []string) {
	t.Helper()
	want := []string{bldWT, stc(1, "worktree"), bldC1, stc(2, "c1"), sp(1)}
	if got := w.f.takeLog(); !equalStrings(got, want) {
		t.Fatalf("ensure; pause c1: fake log\n got %q\nwant %q", got, want)
	}
	if got := tp.take(); !equalStrings(got, []string{bs, bo, bs, bo}) {
		t.Fatalf("ensure; pause c1: tape %q", got)
	}
	if !equalStrings(answers, []string{"g1"}) {
		t.Fatalf("ensure; pause c1: answers %q", answers)
	}
	w.mu.Lock()
	views := append([]*registry.Component(nil), w.views...)
	w.mu.Unlock()
	root := w.rootOf(w.c.Path, ckTree("c1"))
	again, err := w.reg.View(w.c, registry.ViewCode{Tree: ckTree("c1"), Root: root})
	switch {
	case len(views) != 1 || err != nil:
		t.Fatalf("started %d views of c1 (%v), want 1", len(views), err)
	case views[0] == w.c || views[0].CodeRoot != root || views[0].Deployment != "":
		t.Errorf("g2 spawned from %+v, want the primary's view of c1 at %s", views[0], root)
	case views[0] != again:
		t.Error("g2's view isn't the registry's shared view of c1")
	case w.c.CodeRoot != "" || w.c.Manifest.Runtime != "go":
		t.Errorf("the registry's component was mutated: %+v", w.c)
	}
	if b := w.r.Inspect(); len(b) != 1 || b[0].Checkpoint != ckTree("c1") {
		t.Errorf("Inspect = %+v, want apps/x running checkpoint %s", b, ckTree("c1"))
	}
}

// checkZeroStateAsksNothing: a zero-state tile's restarts asked the record
// (an in-memory lookup) and nothing else — no materialization, no checkpoint
// view, no artifact directory, no tree held — and its /runtime row has no
// checkpoint key.
func checkZeroStateAsksNothing(t *testing.T, w *pinWorld) {
	t.Helper()
	if n, v := w.callsOf("materialize"), w.callsOf("view"); n != 0 || v != 0 || w.callsOf("codeFor") == 0 {
		t.Errorf("hook calls: materialize %d, view %d, codeFor %d; want only codeFor", n, v, w.callsOf("codeFor"))
	}
	for _, p := range []string{".xbin/deploy", ".xbin/build/" + ckX + "/c"} {
		if _, err := os.Lstat(filepath.Join(w.f.root, p)); err == nil {
			t.Errorf("%s exists after zero-state restarts", p)
		}
	}
	b, err := json.Marshal(w.r.Inspect())
	if err != nil || strings.Contains(string(b), `"checkpoint"`) {
		t.Errorf("Inspect of the work tree carries a checkpoint: %s (%v)", b, err)
	}
}

// covers P9 SC-ROLLBACK T18 — artifacts are kept per checkpoint while
// referenced: Artifact finds a checkpoint's retained binary only where its
// build left it for this tile; PruneArtifacts keeps what the plane names (a
// deployment's current checkpoint and its previous three deploy-log entries,
// NP-02-3) and what a generation runs, and never the live reload target's
// .xbin/build/<CompKey>/bin.
func TestArtifactPerCheckpoint(t *testing.T) {
	root := t.TempDir()
	r := &Runner{Root: root}
	x := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	build := filepath.Join(root, ".xbin", "build", ckX)
	arts := filepath.Join(build, "c")
	put := func(name, tile string) string {
		t.Helper()
		dir := filepath.Join(arts, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if tile != "" {
			tree, _, _ := strings.Cut(name, ".tmp-")
			stamp, _ := json.Marshal(map[string]string{"tile": tile, "tree": tree, "toolchain": "go1.x"})
			if err := os.WriteFile(filepath.Join(dir, "build.json"), stamp, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	exists := func(p string) bool { _, err := os.Lstat(p); return err == nil }

	if err := r.PruneArtifacts("apps/x", nil); err != nil {
		t.Errorf("PruneArtifacts with nothing built: %v", err)
	}
	// the live reload target's own bin, today's path
	if err := os.MkdirAll(build, 0o755); err != nil {
		t.Fatal(err)
	}
	wtBin := filepath.Join(build, "bin")
	if err := os.WriteFile(wtBin, []byte("wt"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("found where the checkpoint build left it", func(t *testing.T) {
		put(ckTree("c1"), "apps/x")
		bin, ok := r.Artifact(x, ckTree("c1"))
		if want := filepath.Join(root, ".xbin/build/"+ckX+"/c/"+ckTree("c1")+"/bin"); !ok || bin != want {
			t.Errorf("Artifact(c1) = %q, %v; want %q", bin, ok, want)
		}
		if _, ok := r.Artifact(x, ckTree("c9")); ok {
			t.Error("Artifact of a checkpoint never built: found")
		}
	})

	t.Run("refused unless it is this tile's, complete and contained", func(t *testing.T) {
		put(ckTree("d1"), "")       // no build.json
		put(ckTree("d2"), "apps/y") // another tile under the same CompKey
		put(ckTree("d3"), "apps/x")
		if err := os.Remove(filepath.Join(arts, ckTree("d3"), "bin")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/bin/sh", filepath.Join(arts, ckTree("d3"), "bin")); err != nil {
			t.Fatal(err)
		}
		d4 := put(ckTree("d4"), "apps/x")
		if err := os.Remove(filepath.Join(d4, "bin")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(d4, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(ckTree("c1"), filepath.Join(arts, ckTree("d5"))); err != nil { // a directory symlinked to a valid one
			t.Fatal(err)
		}
		put(ckTree("d6")+".tmp-1234", "apps/x") // a build in flight
		d7 := put(ckTree("d7"), "apps/x")       // its record names another tree
		stamp, _ := json.Marshal(map[string]string{"tile": "apps/x", "tree": ckTree("c1")})
		if err := os.WriteFile(filepath.Join(d7, "build.json"), stamp, 0o644); err != nil {
			t.Fatal(err)
		}
		for _, tree := range []string{ckTree("d1"), ckTree("d2"), ckTree("d3"), ckTree("d4"), ckTree("d5"), ckTree("d7"),
			ckTree("d6") + ".tmp-1234", "../" + ckX + "/c/" + ckTree("c1"), strings.ToUpper(ckTree("c1")), "c1", ""} {
			if bin, ok := r.Artifact(x, tree); ok {
				t.Errorf("Artifact(%q) = %q, want none", tree, bin)
			}
		}
		for _, name := range []string{"d1", "d3", "d4", "d5", "d7"} {
			os.RemoveAll(filepath.Join(arts, ckTree(name)))
		}
	})

	t.Run("pruning keeps the current checkpoint, the previous three and what runs", func(t *testing.T) {
		for _, l := range []string{"c0", "c2", "c3", "c4", "c5"} {
			put(ckTree(l), "apps/x")
		}
		put(ckTree("e1"), "") // kept by name, but no artifact
		stale := put(ckTree("c6")+".tmp-old", "apps/x")
		old := time.Now().Add(-2 * time.Hour)
		if err := os.Chtimes(stale, old, old); err != nil {
			t.Fatal(err)
		}
		// c0 runs in a generation (held from its preparation to its exit)
		release := r.inUse.hold(&r.inUse.arts, filepath.Join(arts, ckTree("c0")))
		keep := []string{ckTree("c5"), ckTree("c4"), ckTree("c3"), ckTree("c2"), ckTree("e1")}
		if err := r.PruneArtifacts("apps/x", keep); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			want bool
		}{
			{ckTree("c5"), true}, {ckTree("c4"), true}, {ckTree("c3"), true}, {ckTree("c2"), true},
			{ckTree("c0"), true},  // running
			{ckTree("c1"), false}, // neither kept nor running
			{ckTree("d2"), true},  // another tile's
			{ckTree("d6") + ".tmp-1234", true}, {ckTree("c6") + ".tmp-old", false},
			{ckTree("e1"), false},
		} {
			if got := exists(filepath.Join(arts, tc.name)); got != tc.want {
				t.Errorf("after pruning, %s exists = %v, want %v", tc.name, got, tc.want)
			}
		}
		if !exists(wtBin) {
			t.Error("pruning removed the live reload target's bin")
		}
		if _, ok := r.Artifact(x, ckTree("c5")); !ok {
			t.Error("the current checkpoint's artifact is gone")
		}
		release()
		release() // idempotent
		if err := r.PruneArtifacts("apps/x", keep); err != nil {
			t.Fatal(err)
		}
		if exists(filepath.Join(arts, ckTree("c0"))) {
			t.Error("c0's artifact outlived its last generation")
		}
	})

	t.Run("roots in use", func(t *testing.T) {
		if got := r.RootsInUse(); got != nil {
			t.Errorf("RootsInUse with nothing running = %q, want nil", got)
		}
		a, b := filepath.Join(root, ".xbin/deploy/k/t2"), filepath.Join(root, ".xbin/deploy/k/t1")
		ra, rb, rb2 := r.inUse.hold(&r.inUse.roots, a), r.inUse.hold(&r.inUse.roots, b), r.inUse.hold(&r.inUse.roots, b)
		if got := r.RootsInUse(); !equalStrings(got, []string{b, a}) {
			t.Errorf("RootsInUse = %q, want both, sorted", got)
		}
		ra()
		rb()
		if got := r.RootsInUse(); !equalStrings(got, []string{b}) {
			t.Errorf("RootsInUse = %q, want t1 (a second generation binds it)", got)
		}
		rb2()
		if got := r.RootsInUse(); got != nil {
			t.Errorf("RootsInUse = %q after every generation exited, want nil", got)
		}
	})
}
