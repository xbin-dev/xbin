package runner

// covers D119e D119h T10 T12 SC-SAFE-DEPLOY SC-LIVE-RELOAD-PAUSE — the deploy
// worker (07-runtime §8, §11, §12) driven through the seam's fake engine
// (fake_engine_test.go) and a fake deployments plane: seam rows 23–29 and 38
// of 15-test-plan §2.5, the deploy queue at the runner, isolation, restart as
// a deploy of the same checkpoint, and the alwaysOn, VM and entry checks
// reading the deployment view.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// tree is checkpoint "cN"'s full tree hash: forty times the digit N.
func tree(name string) string { return strings.Repeat(name[1:], 40) }

// ckName is tree's checkpoint name ("c1").
func ckName(tree string) string { return "c" + tree[:1] }

// deployRig is the seam runner with a fake deployments plane installed: the
// record's code for apps/x main, each checkpoint's manifest and materialized
// root. Its ops do what the plane does around Deploy (07-runtime §8.2): a
// move off the work tree writes the pointer at request time, a pinned →
// pinned deploy in its commit.
type deployRig struct {
	t  *testing.T
	r  *Runner
	f  *fakeEngine
	tp *tape
	c  *registry.Component

	mu          sync.Mutex
	code        Code                         // the record's pointer for apps/x main
	mans        map[string]registry.Manifest // a checkpoint's manifest, by name; default: the work tree's
	inflight    int                          // builds running now
	maxInflight int
	progress    []string // "phase result[: error]", every deploy's
	commits     []string // at each commit: the generation then current
}

func newDeployRig(t *testing.T, man registry.Manifest) *deployRig {
	t.Helper()
	g := &deployRig{t: t, c: &registry.Component{Path: "apps/x", Manifest: man}, code: Code{WorkTree: true},
		mans: map[string]registry.Manifest{}}
	g.r, g.f, g.tp = newSeamRunner(t, g.c)
	g.install()
	return g
}

// install puts the fake plane and the checkpoint-aware engine on g.r.
func (g *deployRig) install() {
	g.r.Isolate = true
	g.r.DeploymentHooks = DeploymentHooks{
		CodeFor: func(tile, dep string) (Code, error) {
			if dep != util.MainDeployment {
				return Code{}, util.NoDeployment(tile, dep)
			}
			g.mu.Lock()
			defer g.mu.Unlock()
			return g.code, nil
		},
		View: func(c *registry.Component, code Code) (*registry.Component, error) {
			if code.WorkTree {
				return c, nil
			}
			g.mu.Lock()
			man, ok := g.mans[ckName(code.Tree)]
			g.mu.Unlock()
			if !ok {
				man = c.Manifest
			}
			return &registry.Component{Path: c.Path, Dir: c.Dir, Manifest: man, CodeRoot: g.root(code.Tree)}, nil
		},
		Materialize: func(tile, tree string) (string, error) { return g.root(tree), nil },
	}
	e := g.f.engine()
	worktree := e.build
	e.build = func(c *registry.Component) (string, error) {
		g.mu.Lock()
		if g.inflight++; g.inflight > g.maxInflight {
			g.maxInflight = g.inflight
		}
		g.mu.Unlock()
		defer func() { g.mu.Lock(); g.inflight--; g.mu.Unlock() }()
		if c.CodeRoot == "" {
			return worktree(c)
		}
		return g.buildCheckpoint(c)
	}
	g.r.engine = e
}

// buildCheckpoint is the fake's build of a checkpoint's view: logged as
// "build apps/x main@c1", with the fake's hold and failure knobs.
func (g *deployRig) buildCheckpoint(c *registry.Component) (string, error) {
	f := g.f
	name := ckName(filepath.Base(c.CodeRoot))
	entry := "build " + c.Path + " main@" + name
	bin := "fake-bin/" + c.Path + "@" + name
	f.mu.Lock()
	if h := f.hold; h != nil {
		f.hold, f.release, f.parked = nil, h, c.Path
		f.log = append(f.log, entry)
		f.mu.Unlock()
		<-h
		return bin, g.leaveArtifact(c)
	}
	defer f.mu.Unlock()
	if f.failing("build " + c.Path + " main") {
		f.log = append(f.log, entry+": fail")
		return "", &BuildError{Output: "fake compile error in " + name}
	}
	f.log = append(f.log, entry)
	return bin, g.leaveArtifact(c)
}

// leaveArtifact leaves the artifact a checkpoint build keeps, in the
// layout Artifact reads (.xbin/build/<CompKey>/c/<tree>/{bin,build.json}),
// so the next start of that checkpoint reuses it (07-runtime §8.7).
func (g *deployRig) leaveArtifact(c *registry.Component) error {
	tree := filepath.Base(c.CodeRoot)
	dir := filepath.Join(g.f.root, ".xbin", "build", util.CompKey(c.Path), "c", tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "bin"), []byte("fake binary"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "build.json"), []byte(fmt.Sprintf(`{"tile":%q,"tree":%q}`, c.Path, tree)), 0o644)
}

// root is where a checkpoint is materialized, as the plane would put it.
func (g *deployRig) root(tree string) string {
	return filepath.Join(g.f.root, ".xbin", "deploy", "k", tree)
}

func (g *deployRig) setCode(code Code) {
	g.mu.Lock()
	g.code = code
	g.mu.Unlock()
}

func (g *deployRig) record() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.code.WorkTree {
		return "work-tree"
	}
	return ckName(g.code.Tree)
}

func (g *deployRig) report(phase, result string, err error) {
	s := phase + " " + result
	if err != nil {
		s += ": " + strings.SplitN(err.Error(), "\n", 2)[0]
	}
	g.mu.Lock()
	g.progress = append(g.progress, s)
	g.mu.Unlock()
}

// commitPointer is a pinned → pinned commit: the record takes code, after
// the swap; it notes which generation was current by then.
func (g *deployRig) commitPointer(code Code) func() error {
	return func() error {
		cur := "-"
		if s := g.r.existingState(g.c.Path); s != nil {
			s.mu.Lock()
			if s.cur != nil {
				cur = fmt.Sprintf("g%d", s.cur.gen)
			}
			s.mu.Unlock()
		}
		g.mu.Lock()
		g.commits = append(g.commits, cur)
		g.code = code
		g.mu.Unlock()
		return nil
	}
}

func (g *deployRig) deploy(code Code, commit func() error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return g.r.Deploy(ctx, g.c, util.MainDeployment, code, commit, g.report)
}

// pause is pausing live reload: the pointer is the capture from the request
// on, and the deploy moves the deployment onto the same files.
func (g *deployRig) pause(name string) error {
	g.setCode(Code{Tree: tree(name)})
	return g.deploy(Code{Tree: tree(name), Identical: true}, nil)
}

// deployPinned is a pinned → pinned deploy (reload now, deploy, promote,
// roll back while paused): the pointer moves at commit.
func (g *deployRig) deployPinned(name string) error {
	code := Code{Tree: tree(name)}
	return g.deploy(code, g.commitPointer(code))
}

// redeploy deploys the checkpoint the deployment is already pinned to: the
// plane knows the files are the same.
func (g *deployRig) redeploy(name string) error {
	code := Code{Tree: tree(name), Identical: true}
	return g.deploy(code, g.commitPointer(code.runs()))
}

// deployOntoLive deploys a checkpoint onto the live reload target: a move off
// the work tree, pointer at request time, not the work tree's files.
func (g *deployRig) deployOntoLive(name string) error {
	g.setCode(Code{Tree: tree(name)})
	return g.deploy(Code{Tree: tree(name)}, nil)
}

// resume: the pointer back to the work tree, then the work tree's build.
func (g *deployRig) resume() error {
	g.setCode(Code{WorkTree: true})
	return g.deploy(Code{WorkTree: true}, nil)
}

func (g *deployRig) ensure() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return answer(g.r.Ensure(ctx, g.c))
}

// served renders what the deployment's generation runs, as the runner
// reports it: "healthy g2 c1", "healthy g1 work-tree", "idle g0".
func (g *deployRig) served() string {
	d := g.r.DeploymentStatus(g.c.Path, util.MainDeployment)
	s := fmt.Sprintf("%s g%d", d.State, d.Gen)
	switch d.Serving {
	case "":
	case "work-tree":
		s += " work-tree"
	default:
		s += " " + ckName(d.Serving)
	}
	return s
}

func (g *deployRig) settle() { g.t.Helper(); settle(g.t, g.r, g.f) }

// takeAll drops what the fake and the tape recorded so far, and the progress.
func (g *deployRig) takeAll() {
	g.f.takeLog()
	g.tp.take()
	g.mu.Lock()
	g.progress, g.commits = nil, nil
	g.mu.Unlock()
}

func (g *deployRig) expect(log, tape, progress []string) {
	g.t.Helper()
	g.expectLog(log, false)
	g.expectTape(tape)
	g.mu.Lock()
	got := g.progress
	g.progress = nil
	g.mu.Unlock()
	if !equalStrings(got, progress) {
		g.t.Errorf("progress:\n got %q\nwant %q", got, progress)
	}
}

func (g *deployRig) expectLog(want []string, sorted bool) {
	g.t.Helper()
	got := g.f.takeLog()
	if sorted {
		got, want = sortedCopy(got), sortedCopy(want)
	}
	if !equalStrings(got, want) {
		g.t.Errorf("fake log:\n got %q\nwant %q", got, want)
	}
}

func (g *deployRig) expectTape(want []string) {
	g.t.Helper()
	if got := g.tp.take(); !equalStrings(got, want) {
		g.t.Errorf("tape:\n got %q\nwant %q", got, want)
	}
}

func (g *deployRig) deploymentLog() string {
	b, _ := os.ReadFile(filepath.Join(g.f.root, ".xbin", "log", util.CompKey(g.c.Path)+".log"))
	return string(b)
}

// waitParked waits until a build is parked on the fake's hold.
func (g *deployRig) waitParked() {
	g.t.Helper()
	waitFor(g.t, "a parked build", func() bool {
		g.f.mu.Lock()
		defer g.f.mu.Unlock()
		return g.f.parked != ""
	})
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("never saw %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// needsRestartPathsFromCodeFor skips a joint check of this worker and the
// restart paths (Ensure's lazy start, the crash restart) until those build
// from codeFor (D119e, 07-runtime §7): probed on a pinned rig, never assumed.
func needsRestartPathsFromCodeFor(t *testing.T) {
	t.Helper()
	g := newDeployRig(t, goMan)
	g.setCode(Code{Tree: tree("c1")})
	g.ensure()
	if !strings.Contains(strings.Join(g.f.takeLog(), " "), "main@c1") {
		t.Skip("Ensure doesn't build from codeFor in this tree yet: the restart paths' half of D119e checks here once it does")
	}
}

const (
	c1b = "build apps/x main@c1"
	c2b = "build apps/x main@c2"
	c3b = "build apps/x main@c3"
	rl  = "reload apps/x"
)

// deployOK is a successful swap's progress.
var deployOK = []string{"build running", "start running", "swap running", "swap ok"}

var goMan = registry.Manifest{Runtime: "go"}

// covers D119e D127h SC-LIVE-RELOAD-PAUSE SC-SAFE-DEPLOY — seam rows 23–27, 29
// and 38 (15-test-plan §2.5): pausing live reload, reload now, resume and a
// deploy onto the live reload target, through Deploy. A checkpoint deploy
// reports only through progress (never build-*), announces one bare reload
// only when the served code changed, and a failed one leaves the previous
// generation serving (05-model §5, §8).
func TestDeploySeamRows(t *testing.T) {
	t.Run("23 pausing an unchanged work tree is exactly one swap onto c1, never a build of the work tree", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		if a := g.ensure(); a != "g1" {
			t.Fatalf("ensure = %s", a)
		}
		g.takeAll()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		// the same files: no reload, and no build-* for a checkpoint deploy
		g.expect([]string{c1b, st(2), sp(1)}, nil, deployOK)
		if got := g.served(); got != "healthy g2 c1" {
			t.Errorf("served %s, want healthy g2 c1", got)
		}
		if a := g.ensure(); a != "g2" {
			t.Errorf("ensure after the pause = %s, want g2", a)
		}
	})

	t.Run("24 an edit whose rebuild is in flight: that build swaps, then the pause ships c1", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		g.takeAll()
		g.f.holdNextBuild()
		g.r.Changed(g.c) // the save's rebuild, parked
		g.waitParked()
		done := make(chan error, 1)
		go func() { done <- g.pause("c1") }()
		time.Sleep(20 * time.Millisecond) // the deploy waits on the build turn
		if got := g.f.takeLog(); !equalStrings(got, []string{bld}) {
			t.Fatalf("while the save's build is parked: %q, want only it", got)
		}
		g.f.releaseBuild()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expectLog([]string{st(2), sp(1), c1b, st(3), sp(2)}, true)
		g.expectTape([]string{bs, bo}) // the save's build only
		if got := g.served(); got != "healthy g3 c1" {
			t.Errorf("served %s, want healthy g3 c1", got)
		}
		if g.maxInflight != 1 {
			t.Errorf("%d builds in flight at once, want 1", g.maxInflight)
		}
	})

	t.Run("25 a broken edit, then pause: the build of c1 fails, the old generation serves, nothing on old types", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		g.takeAll()
		g.f.failNext("build", g.c.Path, "main", 1)
		err := g.pause("c1")
		var be *BuildError
		if !errors.As(err, &be) {
			t.Fatalf("pause = %v, want the build error", err)
		}
		g.settle()
		g.expect([]string{c1b + ": fail"}, nil, []string{"build running", "build failed: build failed:"})
		// the record keeps the attempted checkpoint (the plane's, state failed)
		if got := g.record(); got != "c1" {
			t.Errorf("record %s, want c1", got)
		}
		if a := g.ensure(); a != "g1" {
			t.Errorf("ensure = %s, want the old generation g1", a)
		}
		d := g.r.DeploymentStatus(g.c.Path, util.MainDeployment)
		if d.Serving != "work-tree" || !strings.Contains(d.Error, "fake compile error in c1") {
			t.Errorf("status %+v: want the work tree serving and the build error", d)
		}
		if l := g.deploymentLog(); !strings.Contains(l, "deploy of c:1111111 failed") || !strings.Contains(l, "fake compile error in c1") {
			t.Errorf("the deployment's log lacks the failure and compiler output:\n%s", l)
		}
		g.expectTape(nil)
	})

	t.Run("25r after the failed pause, any restart runs c1 and fails visibly, never the work tree", func(t *testing.T) {
		needsRestartPathsFromCodeFor(t)
		g := newDeployRig(t, goMan)
		g.ensure()
		g.f.failNext("build", g.c.Path, "main", 2)
		if err := g.pause("c1"); err == nil {
			t.Fatal("the pause's build didn't fail")
		}
		g.settle()
		g.takeAll()
		g.f.crash(g.c.Path, "main")
		// Not settle: the crash leaves the state dirty with the failed
		// pause's error on it, which the next request (below) rebuilds.
		waitFor(t, "the crash watch", func() bool {
			s := g.r.existingState(g.c.Path)
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.cur == nil && s.dirty
		})
		if a := g.ensure(); !strings.HasPrefix(a, "error: ") {
			t.Errorf("ensure after the crash = %s, want c1's build error", a)
		}
		g.expectLog([]string{c1b + ": fail"}, false)
	})

	t.Run("26 pause, edit, reload now: build c2, swap, one bare reload, committed after the swap", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		if err := g.deployPinned("c2"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect([]string{c2b, st(3), sp(2)}, []string{rl}, deployOK)
		if !equalStrings(g.commits, []string{"g3"}) {
			t.Errorf("commits saw %q, want [g3]: the pointer moves after the swap", g.commits)
		}
		if got, rec := g.served(), g.record(); got != "healthy g3 c2" || rec != "c2" {
			t.Errorf("served %s, record %s; want healthy g3 c2, record c2", got, rec)
		}
	})

	t.Run("27 pause, resume: the work tree's build, a swap, one reload, and saves reach it again", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		if err := g.resume(); err != nil {
			t.Fatal(err)
		}
		g.settle()
		// a work-tree build is today's build, bare build-* for the primary
		g.expect([]string{bld, st(3), sp(2)}, []string{bs, bo, rl}, deployOK)
		if got := g.served(); got != "healthy g3 work-tree" {
			t.Errorf("served %s, want healthy g3 work-tree", got)
		}
		g.r.Changed(g.c) // a save
		g.settle()
		g.expect([]string{bld, st(4), sp(3)}, []string{bs, bo}, nil)
	})

	t.Run("27c resume by ChangedDeployment (07-runtime §8.2) rebuilds the work tree", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		g.setCode(Code{WorkTree: true})
		g.r.ChangedDeployment(g.c, util.MainDeployment)
		g.settle()
		g.expect([]string{bld, st(3), sp(2)}, []string{bs, bo}, nil)
	})

	t.Run("29 a deploy onto the live reload target pins it: c2 swaps in and one reload announces it", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		g.takeAll()
		if err := g.deployOntoLive("c2"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect([]string{c2b, st(2), sp(1)}, []string{rl}, deployOK)
		if got := g.served(); got != "healthy g2 c2" {
			t.Errorf("served %s, want healthy g2 c2", got)
		}
	})

	for _, tc := range []struct {
		name, fail string
		log        []string
		progress   []string
	}{
		{"a build that fails", "build", []string{c2b + ": fail"}, []string{"build running", "build failed: build failed:"}},
		{"a start that never answers", "health", []string{c2b, st(3), sp(3)},
			[]string{"build running", "start running", "start failed: backend did not become healthy: fake: never answered"}},
	} {
		t.Run("38 pause, broken edit, reload now: "+tc.name+" keeps c1 serving, nothing on old types", func(t *testing.T) {
			g := newDeployRig(t, goMan)
			g.ensure()
			if err := g.pause("c1"); err != nil {
				t.Fatal(err)
			}
			g.settle()
			g.takeAll()
			g.f.failNext(tc.fail, g.c.Path, "main", 1)
			if err := g.deployPinned("c2"); err == nil {
				t.Fatal("the failed deploy answered no error")
			}
			g.settle()
			g.expect(tc.log, nil, tc.progress)
			if len(g.commits) != 0 || g.record() != "c1" {
				t.Errorf("commits %q, record %s: a failed pinned → pinned deploy keeps the pointer", g.commits, g.record())
			}
			if a, got := g.ensure(), g.served(); a != "g2" || got != "healthy g2 c1" {
				t.Errorf("ensure %s, served %s; want c1 still serving (g2)", a, got)
			}
			if d := g.r.DeploymentStatus(g.c.Path, util.MainDeployment); d.Error == "" {
				t.Error("the failure isn't on the deployment's status")
			}
			if l := g.deploymentLog(); !strings.Contains(l, "deploy of c:2222222 failed") {
				t.Errorf("the deployment's log lacks the failure:\n%s", l)
			}
			if tc.fail == "build" && !strings.Contains(g.deploymentLog(), "fake compile error in c2") {
				t.Error("the compiler output isn't in the deployment's log")
			}
		})
	}
}

// covers T10 — seam row 28 (= TestDeployQueueCoalesces, 06-security T10):
// deploys of one deployment serialize on its build turn. Deploying c2, then
// c3 while c2 builds, never has two builds in flight, and ends on c3.
func TestDeployQueueCoalesces(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.ensure()
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	g.takeAll()

	g.f.holdNextBuild()
	d2 := make(chan error, 1)
	go func() { d2 <- g.deployPinned("c2") }()
	g.waitParked()
	d3 := make(chan error, 1)
	go func() { d3 <- g.deployPinned("c3") }()
	// the ensure a request makes meanwhile waits on the turn too
	ens := make(chan string, 1)
	go func() { ens <- g.ensure() }()
	time.Sleep(20 * time.Millisecond)
	if got := g.f.takeLog(); !equalStrings(got, []string{c2b}) {
		t.Fatalf("while c2 builds: %q, want only c2's build", got)
	}
	g.f.releaseBuild()
	for _, ch := range []chan error{d2, d3} {
		if err := <-ch; err != nil {
			t.Fatal(err)
		}
	}
	<-ens
	g.settle()
	g.expectLog([]string{st(3), sp(2), c3b, st(4), sp(3)}, true)
	g.expectTape([]string{rl, rl})
	if g.maxInflight != 1 {
		t.Errorf("%d builds in flight at once, want 1", g.maxInflight)
	}
	if got, rec := g.served(), g.record(); got != "healthy g4 c3" || rec != "c3" {
		t.Errorf("served %s, record %s; want healthy g4 c3, record c3", got, rec)
	}
}

// covers D119e T18 — a generation a deploy starts holds its materialized tree
// in RootsInUse (checkpoint GC's keep set) until it exits, as one Ensure
// starts does: deployed c1 is held, c2's swap moves the hold, a stop drops it.
func TestDeployHoldsRootsInUse(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.ensure()
	if got := g.r.RootsInUse(); len(got) != 0 {
		t.Fatalf("the work tree holds %q", got)
	}
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	if got := g.r.RootsInUse(); !equalStrings(got, []string{g.root(tree("c1"))}) {
		t.Fatalf("after the pause RootsInUse = %q, want c1's tree", got)
	}
	if err := g.deployPinned("c2"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	waitFor(t, "c1's generation to exit", func() bool {
		return equalStrings(g.r.RootsInUse(), []string{g.root(tree("c2"))})
	})
	g.r.Stop(g.c.Path)
	waitFor(t, "the stop", func() bool { return len(g.r.RootsInUse()) == 0 })
}

// covers D119e NP-07-2 — restart is a deploy of the same checkpoint (07-runtime
// §8.7, 11-contract §1.6): a no-op while healthy; a new generation, clearing
// the breaker, while crash-looping; Restart forces one either way, reported
// by build-* like any restart of current code, with no reload. The crash
// watch of a deployed generation names what clears its breaker.
func TestDeployRestartIsSameCheckpoint(t *testing.T) {
	t.Run("healthy: nothing to do", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		if err := g.redeploy("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect(nil, nil, []string{" ok"})
		if got := g.served(); got != "healthy g2 c1" {
			t.Errorf("served %s", got)
		}
	})

	t.Run("crash-looping: a new generation, and the breaker clears", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		s := g.r.existingState(g.c.Path)
		s.mu.Lock()
		now := g.f.now()
		s.crashes = []time.Time{now, now} // two recent exits already
		s.mu.Unlock()
		g.f.crash(g.c.Path, "main")
		g.settle()
		const loop = "backend crash-looping (3 exits); deploy a fixed checkpoint or restart it — see .xbin/log/apps~x-ebdae547.log"
		g.expect(nil, []string{"build-error apps/x: " + loop}, nil)
		if a := g.ensure(); a != "error: "+loop {
			t.Fatalf("ensure while crash-looping = %s", a)
		}
		if err := g.redeploy("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect([]string{st(3)}, nil, deployOK) // from c1's kept artifact: no compile (§8.7)
		if got, a := g.served(), g.ensure(); got != "healthy g3 c1" || a != "g3" {
			t.Errorf("served %s, ensure %s; want the breaker cleared, g3 on c1", got, a)
		}
	})

	t.Run("Restart forces a new generation of the pinned checkpoint", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		if err := g.r.Restart(context.Background(), g.c, util.MainDeployment, g.report); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect([]string{st(3), sp(2)}, []string{bs, bo}, deployOK) // c1's kept artifact (§8.7)
	})

	t.Run("Restart of the work tree rebuilds it", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.ensure()
		g.takeAll()
		if err := g.r.Restart(context.Background(), g.c, util.MainDeployment, g.report); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expect([]string{bld, st(2), sp(1)}, []string{bs, bo}, deployOK)
	})
}

// covers D119e — pausing live reload on a deployment with no process (never
// started, reaped) pins without starting one: the checkpoint is built now so
// its errors surface, and the next request starts it (07-runtime §8.6).
func TestDeployIdlePreparesWithoutStarting(t *testing.T) {
	g := newDeployRig(t, goMan)
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	g.expect([]string{c1b}, nil, []string{"build running", "build ok"})
	if got := g.served(); got != "idle g0" {
		t.Errorf("served %s, want idle g0: nothing started", got)
	}

	g.f.failNext("build", g.c.Path, "main", 1)
	if err := g.deployPinned("c2"); err == nil {
		t.Fatal("a failed preparation answered no error")
	}
	g.settle()
	g.expect([]string{c2b + ": fail"}, nil, []string{"build running", "build failed: build failed:"})
	if g.record() != "c1" {
		t.Errorf("record %s, want c1 kept", g.record())
	}
}

// covers D119h — a tile without a backend is served by the static plane: a
// deploy commits and starts nothing, needs no isolation, keeps no runner
// state, and reloads only when the files change (07-runtime §8.6, §12).
func TestDeployStaticTile(t *testing.T) {
	g := newDeployRig(t, registry.Manifest{Runtime: "static"})
	g.r.Isolate = false
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.expect(nil, nil, []string{"swap running", "swap ok"})
	if err := g.deployPinned("c2"); err != nil {
		t.Fatal(err)
	}
	g.expect(nil, []string{rl}, []string{"swap running", "swap ok"})
	if g.record() != "c2" {
		t.Errorf("record %s, want c2", g.record())
	}
	if _, ok := g.r.Status()[g.c.Path]; ok {
		t.Error("a static tile's deploy made a runner state (it would list in /status)")
	}
}

// covers D119e — a checkpoint whose code has no backend replaces a running one:
// the old generation drains and the deployment serves statically.
func TestDeployToStaticCodeDrains(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.mans["c1"] = registry.Manifest{Runtime: "static"}
	g.ensure()
	g.takeAll()
	if err := g.deployOntoLive("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	g.expect([]string{sp(1)}, []string{rl}, []string{"swap running", "swap ok"})
	if got := g.served(); got != "idle g1" {
		t.Errorf("served %s, want idle g1", got)
	}
}

// covers D119h T12 SC-FAIL-CLOSED — TestNonIsolatedRefusesBackendDeployments,
// the runner half: without isolation, pausing live reload on, deploying to
// or restarting a pinned go, node or python backend is refused, naming
// --isolate, before anything is built or either callback called; a static
// tile is allowed, and so is today's work-tree build.
func TestNonIsolatedRefusesBackendDeployments(t *testing.T) {
	for _, rt := range []string{"go", "node", "python"} {
		t.Run(rt, func(t *testing.T) {
			g := newDeployRig(t, registry.Manifest{Runtime: rt})
			g.r.Isolate = false
			called := false
			commit := func() error { called = true; return nil }
			progress := func(string, string, error) { called = true }
			for _, code := range []Code{{Tree: tree("c1"), Identical: true}, {Tree: tree("c2")}} {
				err := g.r.Deploy(context.Background(), g.c, util.MainDeployment, code, commit, progress)
				if !errors.Is(err, ErrNeedsIsolation) || !strings.Contains(err.Error(), "--isolate") {
					t.Errorf("deploy %s = %v, want the isolation refusal", ckName(code.Tree), err)
				}
			}
			g.setCode(Code{Tree: tree("c1")})
			if err := g.r.Restart(context.Background(), g.c, util.MainDeployment, progress); !errors.Is(err, ErrNeedsIsolation) {
				t.Errorf("restart of a pinned backend = %v, want the isolation refusal", err)
			}
			if called {
				t.Error("a refused deploy called commit or progress")
			}
			g.expect(nil, nil, nil)
			if _, ok := g.r.Status()[g.c.Path]; ok {
				t.Error("a refused deploy made a runner state")
			}
		})
	}
	t.Run("static", func(t *testing.T) {
		g := newDeployRig(t, registry.Manifest{Runtime: "static"})
		g.r.Isolate = false
		if err := g.pause("c1"); err != nil {
			t.Errorf("pausing live reload on a static tile without isolation: %v", err)
		}
	})
	t.Run("the work tree", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.r.Isolate = false
		g.ensure()
		g.takeAll()
		if err := g.r.Restart(context.Background(), g.c, util.MainDeployment, nil); err != nil {
			t.Errorf("restarting the work tree without isolation: %v", err)
		}
		g.settle()
		g.expectLog([]string{bld, st(2), sp(1)}, false)
	})
}

// covers D119h T12 C7 — TestIsolationLossHoldsPinnedBackends: an xbind
// restarted without isolation over a record that pins the backend holds it,
// with the reason, and never runs the work tree in its place, on every path
// the deploy worker owns: codeFor (the code every restart path builds from),
// EnsureDeployment, alwaysOn wake-ups, Deploy and Restart; and the inbound
// Ensure path, which holds through codeFor once buildAndStart builds from it
// (skipped, saying so, until then).
func TestIsolationLossHoldsPinnedBackends(t *testing.T) {
	g := newDeployRig(t, registry.Manifest{Runtime: "go", AlwaysOn: true})
	g.r.Isolate = false
	g.setCode(Code{Tree: tree("c1")})
	ctx := context.Background()

	if _, err := g.r.codeFor(g.c.Path, util.MainDeployment); !errors.Is(err, ErrNeedsIsolation) {
		t.Errorf("codeFor = %v, want the isolation hold", err)
	}
	if _, err := g.r.EnsureDeployment(ctx, g.c, util.MainDeployment); !errors.Is(err, ErrNeedsIsolation) {
		t.Errorf("EnsureDeployment = %v, want the isolation hold", err)
	}
	g.r.WakeAlwaysOn()
	if g.r.isAlwaysOn(g.c.Path) {
		t.Error("a held alwaysOn backend counts as alwaysOn")
	}
	if err := g.r.Restart(ctx, g.c, util.MainDeployment, nil); !errors.Is(err, ErrNeedsIsolation) {
		t.Errorf("Restart = %v, want the isolation hold", err)
	}
	if err := g.deployPinned("c2"); !errors.Is(err, ErrNeedsIsolation) {
		t.Errorf("Deploy = %v, want the isolation hold", err)
	}
	g.settle()
	g.expect(nil, nil, nil)

	t.Run("the inbound Ensure path", func(t *testing.T) {
		needsRestartPathsFromCodeFor(t)
		h := newDeployRig(t, goMan)
		h.r.Isolate = false
		h.setCode(Code{Tree: tree("c1")})
		if a := h.ensure(); !strings.Contains(a, "--isolate") {
			t.Errorf("Ensure of a pinned backend without isolation = %s, want the hold naming --isolate", a)
		}
		h.settle()
		h.expectLog(nil, false) // nothing built, the work tree least of all
	})

	// isolation back: the same record starts again
	g.r.Isolate = true
	if _, err := g.r.codeFor(g.c.Path, util.MainDeployment); err != nil {
		t.Errorf("codeFor under isolation: %v", err)
	}
	// a record that follows the work tree was never held
	g.r.Isolate = false
	g.setCode(Code{WorkTree: true})
	if a := answer(g.r.EnsureDeployment(ctx, g.c, util.MainDeployment)); a != "g1" {
		t.Errorf("EnsureDeployment of the work tree without isolation = %s, want g1", a)
	}
}

// covers D119e D127h — alwaysOn is the deployment's own code's (07-runtime §11):
// with live reload paused, the pinned checkpoint's manifest decides the
// reaper's exemption and the wake-up, never the work tree's.
func TestDeployAlwaysOnReadsView(t *testing.T) {
	t.Run("the checkpoint says alwaysOn, the work tree doesn't: never reaped", func(t *testing.T) {
		g := newDeployRig(t, goMan)
		g.mans["c1"] = registry.Manifest{Runtime: "go", AlwaysOn: true}
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		g.f.advance(31 * time.Minute)
		g.r.reapOnce()
		g.settle()
		g.expectLog(nil, false)
	})
	t.Run("the work tree says alwaysOn, the checkpoint doesn't: reaped, and not woken", func(t *testing.T) {
		g := newDeployRig(t, registry.Manifest{Runtime: "go", AlwaysOn: true})
		g.mans["c1"] = goMan
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		g.f.advance(31 * time.Minute)
		g.r.reapOnce()
		g.settle()
		g.expectLog([]string{sp(2)}, false)
		g.r.WakeAlwaysOn()
		g.settle()
		g.expectLog(nil, false)
	})
}

// covers D119e — a VM backend's checks read the deployment view (07-runtime
// §8.1 step 2, §8.4): the new code's "vm" decides whether the old generation
// stops first; after such a stop, a failed pinned → pinned deploy restarts
// the previous checkpoint from its kept artifact, and a failed move off the
// work tree stays down with the error.
func TestDeployStopFirstReadsView(t *testing.T) {
	vmMan := registry.Manifest{Runtime: "go", VM: &registry.VMOpt{On: true}}
	rig := func(t *testing.T, work registry.Manifest) *deployRig {
		g := newDeployRig(t, work)
		db := filepath.Join(g.f.root, "data", "apps", "x", "db.sqlite")
		if err := os.MkdirAll(filepath.Dir(db), 0o755); err != nil {
			t.Fatal(err)
		}
		g.r.EnvForComponent = func(*registry.Component) []string { return []string{"XBIN_RES_DB=" + db} }
		return g
	}

	t.Run("the checkpoint asks for a VM: the old generation stops first", func(t *testing.T) {
		g := rig(t, goMan)
		g.mans["c1"], g.mans["c2"] = vmMan, vmMan
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expectLog([]string{bld, st(1), c1b, sp(1), st(2)}, false)
		if err := g.deployPinned("c2"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expectLog([]string{c2b, sp(2), st(3)}, false)
	})

	t.Run("only the work tree asks for a VM: blue/green as usual", func(t *testing.T) {
		g := rig(t, vmMan)
		g.mans["c1"] = goMan
		g.ensure()
		g.takeAll()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.expectLog([]string{c1b, st(2), sp(1)}, false)
	})

	t.Run("a failed pinned → pinned deploy after the stop restarts the previous checkpoint", func(t *testing.T) {
		g := rig(t, goMan)
		g.mans["c1"], g.mans["c2"] = vmMan, vmMan
		g.ensure()
		if err := g.pause("c1"); err != nil {
			t.Fatal(err)
		}
		g.settle()
		g.takeAll()
		g.f.failNext("health", g.c.Path, "main", 1)
		if err := g.deployPinned("c2"); err == nil {
			t.Fatal("the failed deploy answered no error")
		}
		g.settle()
		// c1 again from its kept artifact: no build
		g.expect([]string{c2b, sp(2), st(3), sp(3), st(4)}, []string{bs, bo},
			[]string{"build running", "start running", "start failed: backend did not become healthy: fake: never answered"})
		if got, rec := g.served(), g.record(); got != "healthy g4 c1" || rec != "c1" {
			t.Errorf("served %s, record %s; want c1 restarted as g4", got, rec)
		}
	})

	t.Run("a failed move off the work tree after the stop stays down with the error", func(t *testing.T) {
		g := rig(t, goMan)
		g.mans["c1"] = vmMan
		g.ensure()
		g.takeAll()
		g.f.failNext("health", g.c.Path, "main", 1)
		if err := g.pause("c1"); err == nil {
			t.Fatal("the failed pause answered no error")
		}
		g.settle()
		g.expectLog([]string{c1b, sp(1), st(2), sp(2)}, false)
		if a := g.ensure(); !strings.HasPrefix(a, "error: backend did not become healthy") {
			t.Errorf("ensure = %s, want the deploy's error held", a)
		}
	})
}

// covers D119e D119g — an interpreted checkpoint's entry is looked for in its
// materialized tree, opened beneath it (07-runtime §8.1 step 1), never in the
// work tree: missing there, or behind a symlink out of it, the build fails.
func TestDeployEntryReadsCheckpoint(t *testing.T) {
	g := newDeployRig(t, registry.Manifest{Runtime: "node"})
	// the work tree has the entry; the checkpoints decide for themselves
	if err := os.MkdirAll(filepath.Join(g.c.Dir, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(g.c.Dir, "backend", "server.js"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	for name, setup := range map[string]func(root string) error{
		"c1": func(root string) error { // has it
			if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(root, "backend", "server.js"), nil, 0o644)
		},
		"c2": func(root string) error { return os.MkdirAll(root, 0o755) }, // lacks it
		"c3": func(root string) error { // a symlink out of the tree
			if err := os.MkdirAll(root, 0o755); err != nil {
				return err
			}
			return os.Symlink(g.c.Dir+"/backend", filepath.Join(root, "backend"))
		},
	} {
		if err := setup(g.root(tree(name))); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.pause("c1"); err != nil {
		t.Errorf("c1 has its entry: %v", err)
	}
	for _, name := range []string{"c2", "c3"} {
		err := g.deployPinned(name)
		var be *BuildError
		if !errors.As(err, &be) || !strings.Contains(be.Output, "entry backend/server.js not found in the checkpoint") {
			t.Errorf("%s: %v, want the missing entry", name, err)
		}
	}
	g.expectLog([]string{c1b}, false) // c2 and c3 never reached the build
}

// covers D127d D119e — Deploy refuses before touching anything: another deployment
// than the primary, code no view describes, a disabled tile, a checkpoint
// whose runtime can't run, and a Code that names nothing.
func TestDeployRefusals(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.mans["c3"] = registry.Manifest{Runtime: "cgi"}
	g.ensure()
	g.takeAll()
	called := false
	commit := func() error { called = true; return nil }
	progress := func(string, string, error) { called = true }
	ctx := context.Background()
	try := func(what string, dep string, code Code, want func(error) bool) {
		t.Helper()
		if err := g.r.Deploy(ctx, g.c, dep, code, commit, progress); !want(err) {
			t.Errorf("%s: %v", what, err)
		}
	}
	try("another deployment", "dev", Code{Tree: tree("c1")}, func(err error) bool { return errors.Is(err, util.ErrNoDeployment) })
	try("no code", util.MainDeployment, Code{}, func(err error) bool { return err != nil })
	try("both codes", util.MainDeployment, Code{WorkTree: true, Tree: tree("c1")}, func(err error) bool { return err != nil })
	try("cgi", util.MainDeployment, Code{Tree: tree("c3")}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "cgi") })
	g.r.View = func(c *registry.Component, code Code) (*registry.Component, error) {
		return nil, errors.New("checkpoint unreadable")
	}
	try("no view", util.MainDeployment, Code{Tree: tree("c1")}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "unreadable") })
	g.install()
	g.f.seal(g.c.Path, true)
	try("disabled", util.MainDeployment, Code{Tree: tree("c1")}, func(err error) bool { return err != nil && strings.Contains(err.Error(), "not enabled") })
	if called {
		t.Error("a refused deploy called commit or progress")
	}
	g.settle()
	g.expect(nil, nil, nil)
	if a := g.ensure(); a != "error: component apps/x is not enabled" {
		t.Errorf("ensure = %s", a)
	}
}

// covers D119e — a commit that fails leaves the record's code serving: the new
// generation stops and the previous one keeps the deployment.
func TestDeployCommitFailureKeepsPrevious(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.ensure()
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	g.takeAll()
	err := g.deploy(Code{Tree: tree("c2")}, func() error { return errors.New("record write failed") })
	if err == nil || !strings.Contains(err.Error(), "record write failed") {
		t.Fatalf("deploy = %v", err)
	}
	g.settle()
	g.expect([]string{c2b, st(3), sp(3)}, nil,
		[]string{"build running", "start running", "swap running", "swap failed: record write failed"})
	if got := g.served(); got != "healthy g2 c1" {
		t.Errorf("served %s, want c1 kept on g2", got)
	}
	if a := g.ensure(); a != "g2" {
		t.Errorf("ensure = %s, want g2", a)
	}
}
