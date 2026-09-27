package deployments

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// ---- the fakes: the store's retention and restore, the runner's pruning ----

// retainStore is the fixture's fake store with the checkpoint store's
// retention: it records each GC with the keep set GC was handed (read as GC
// reads it, before its lock), each Sweep, and each materialization, in the
// order they came, beside the runner's prunes.
type retainStore struct {
	*fakeStore
	mu     sync.Mutex
	events []string   // "gc <tile>", "prune <tile>", "sweep", "materialize <tile>"
	keeps  [][]string // each GC's keep set, sorted
	prunes [][]string // each prune's keep set
	gate   chan struct{}
	gcErr  error
}

func (s *retainStore) note(ev string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, ev)
}

func (s *retainStore) GC(ctx context.Context, tile string, keep func() []string) error {
	s.mu.Lock()
	gate := s.gate
	s.mu.Unlock()
	if gate != nil {
		<-gate
	}
	k := keep()
	sort.Strings(k)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, "gc "+tile)
	s.keeps = append(s.keeps, k)
	return s.gcErr
}

func (s *retainStore) Sweep() error {
	s.note("sweep")
	return nil
}

func (s *retainStore) Materialize(tile, tree string) (string, error) {
	s.note("materialize " + tile)
	return s.fakeStore.Materialize(tile, tree)
}

// seen is what the store saw so far.
func (s *retainStore) seen() (events []string, keeps, prunes [][]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.events), slices.Clone(s.keeps), slices.Clone(s.prunes)
}

// count is how many of the events so far are ev.
func (s *retainStore) count(ev string) int {
	events, _, _ := s.seen()
	n := 0
	for _, e := range events {
		if e == ev {
			n++
		}
	}
	return n
}

// retainRunner is the fixture's fake runner with PruneArtifacts: the
// runner's own when real is set, over artifacts build lays out on disk as a
// checkpoint build leaves them.
type retainRunner struct {
	*fakeRunner
	st    *retainStore
	roots []string
	real  *runner.Runner
	built map[string]bool // tree → a Deploy found its artifact already there
}

func (r *retainRunner) RootsInUse() []string { return r.roots }

func (r *retainRunner) PruneArtifacts(tile string, keep []string) error {
	r.st.mu.Lock()
	r.st.events = append(r.st.events, "prune "+tile)
	r.st.prunes = append(r.st.prunes, slices.Clone(keep))
	r.st.mu.Unlock()
	if r.real != nil {
		return r.real.PruneArtifacts(tile, keep)
	}
	return nil
}

// Deploy builds the checkpoint's artifact first when the runner is real and
// the artifact isn't there, as a checkpoint build does, and notes whether
// it was.
func (r *retainRunner) Deploy(ctx context.Context, c *registry.Component, dep string, code runner.Code, commit func() error, progress runner.DeployProgress) error {
	if r.real != nil && code.Tree != "" {
		r.fakeRunner.mu.Lock()
		if r.built == nil {
			r.built = map[string]bool{}
		}
		_, there := r.real.Artifact(c, code.Tree)
		r.built[code.Tree] = there
		r.fakeRunner.mu.Unlock()
		if !there {
			writeArtifact(r.st.fakeStore.root, c.Path, code.Tree, c.Path)
		}
	}
	return r.fakeRunner.Deploy(ctx, c, dep, code, commit, progress)
}

// wasThere reports whether tree's artifact was already there when a Deploy
// of it began: a restart or roll back that compiles nothing.
func (r *retainRunner) wasThere(tree string) bool {
	r.fakeRunner.mu.Lock()
	defer r.fakeRunner.mu.Unlock()
	return r.built[tree]
}

// writeArtifact lays out the artifact of tile's checkpoint tree as a
// checkpoint build leaves it — bin beside a build.json naming the tile
// (stamp) and the tree — under .xbin/build/<CompKey>/c/<tree>.
func writeArtifact(root, tile, tree, stamp string) {
	dir := artifactDir(root, tile, tree)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		panic(err)
	}
	b, _ := json.Marshal(map[string]string{"tile": stamp, "tree": tree})
	for name, body := range map[string][]byte{"bin": []byte("#!bin\n"), "build.json": b} {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o755); err != nil {
			panic(err)
		}
	}
}

func artifactDir(root, tile, tree string) string {
	return filepath.Join(root, ".xbin", "build", util.CompKey(tile), "c", tree)
}

// newRetainFx is newOpsFx (isolated) booted again over the retaining fakes.
func newRetainFx(t *testing.T) (*opsFx, *retainStore, *retainRunner) {
	t.Helper()
	f := newOpsFx(t, true)
	st := &retainStore{fakeStore: f.st}
	run := &retainRunner{fakeRunner: f.run, st: st}
	bootRetaining(f, st, run)
	return f, st, run
}

// bootRetaining builds and boots a plane over f's workspace with the given
// store and runner: xbind restarting.
func bootRetaining(f *opsFx, st checkpoints, run Runner) *Plane {
	f.t.Helper()
	p := &Plane{Root: f.root, Reg: f.reg, Hub: f.hub, Run: run, cps: st, isolated: func() bool { return f.iso }}
	f.reg.PinnedPrimary = p.PinnedPrimary
	if err := p.Boot(); err != nil {
		f.t.Fatalf("Boot: %v", err)
	}
	f.p = p
	return p
}

// current is deployment dep's checkpoint in tile's record.
func (f *opsFx) current(tile, dep string) string {
	f.t.Helper()
	d := f.rec(tile).Deployments[dep]
	if d == nil || d.Checkpoint == nil {
		f.t.Fatalf("%s's %s isn't pinned", tile, dep)
	}
	return *d.Checkpoint
}

// ---- the tests ----

// covers P9 T10 T18 — the store's GC runs after every successful deploy of a
// tile, a static tile's (inside the operation) and a backend's (in the
// deploy's own goroutine, before the attempt's waiters are released), and
// then the artifact pruning: GC is handed the trees running generations
// bind and every checkpoint the tile references — the record's, and those
// of attempts not yet in the deploy log, whose code a swap may still put in
// place. A failed deploy collects nothing; a GC failure still prunes; a tile
// no record governs, or without a store, is never touched.
func TestCheckpointGCAfterDeploy(t *testing.T) {
	f, st, run := newRetainFx(t)
	root := filepath.Join(f.root, ".xbin", "deploy", util.TileKey(opAPI), strings.Repeat("a", 40))
	run.roots = []string{root}

	// A backend's deploy: GC, then the pruning, before Entry answers.
	f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
	c1 := f.current(opAPI, util.MainDeployment)
	events, keeps, _ := st.seen()
	if got := retention(events); !reflect.DeepEqual(got, []string{"gc " + opAPI, "prune " + opAPI}) {
		t.Fatalf("after the pause: %v, want GC then the pruning", got)
	}
	if !slices.Contains(keeps[0], c1) || !slices.Contains(keeps[0], root) {
		t.Errorf("GC kept %v, want the record's checkpoint %s and the running root", keeps[0], c1)
	}

	// A failed deploy collects nothing.
	run.set(func(r *fakeRunner) { r.fail = errors.New("the build broke") })
	f.write(opAPI+"/main.go", "package main // v2\n")
	if e := f.wait(opAPI, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}).Deploy.ID); e.Result != resultFailed {
		t.Fatalf("the failing reload now: %+v", e)
	}
	if n := st.count("gc " + opAPI); n != 1 {
		t.Errorf("a failed deploy ran GC: %d runs", n)
	}
	run.set(func(r *fakeRunner) { r.fail = nil })

	// A GC that fails still prunes: the pruning reads only the record and
	// the log.
	st.mu.Lock()
	st.gcErr = errors.New("git broke")
	st.mu.Unlock()
	f.write(opAPI+"/main.go", "package main // v3\n")
	f.settle(opAPI, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}))
	if g, p := st.count("gc "+opAPI), st.count("prune "+opAPI); g != 2 || p != 2 {
		t.Errorf("after a failing GC: %d GC runs, %d prunes; want 2 and 2", g, p)
	}
	st.mu.Lock()
	st.gcErr = nil
	st.mu.Unlock()

	// An attempt still waiting its turn keeps its checkpoint: GC runs while
	// the next deploy is queued behind the one it follows.
	gate := make(chan struct{})
	st.mu.Lock()
	st.gate = gate
	st.mu.Unlock()
	f.write(opAPI+"/main.go", "package main // v4\n")
	first := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
	waitFor(t, "the reload now's GC to start", func() bool {
		_, running := f.p.laneOf(opAPI, util.MainDeployment)
		return running == nil && f.st.logged(opAPI)[len(f.st.logged(opAPI))-1].ID == first.Deploy.ID
	})
	f.write(opAPI+"/main.go", "package main // v5\n")
	queued := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI})
	if queued.Deploy == nil || queued.Deploy.Result != resultQueued {
		t.Fatalf("the second reload now = %+v, want queued behind the first's GC", queued.Deploy)
	}
	st.mu.Lock()
	st.gate = nil
	st.mu.Unlock()
	close(gate)
	f.wait(opAPI, first.Deploy.ID)
	f.wait(opAPI, queued.Deploy.ID)
	c5 := f.current(opAPI, util.MainDeployment)
	_, keeps, prunes := st.seen()
	if k, p := keeps[len(keeps)-2], prunes[len(prunes)-2]; !slices.Contains(k, c5) || !slices.Contains(p, c5) {
		t.Errorf("GC kept %v and the pruning %v while %s was queued; want it in both", k, p, c5)
	}

	// A static tile's deploy collects inside the operation.
	f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
	if n := st.count("gc " + opSite); n != 1 {
		t.Errorf("a static tile's pause ran GC %d times, want 1 before it answered", n)
	}

	// A tile without a record, and one whose store is gone, are left alone.
	events, _, _ = st.seen()
	before := len(retention(events))
	f.p.retain(opNode)
	if err := os.RemoveAll(storeDir(f.root, opSite)); err != nil {
		t.Fatal(err)
	}
	f.p.retain(opSite)
	if events, _, _ := st.seen(); len(retention(events)) != before {
		t.Errorf("retention touched a tile without a record or a store: %v", retention(events)[before:])
	}
}

// covers P9 T10 — the same through xbind's own checkpoint store (unit-git:
// confined git run directly): a static tile's pause collects before it
// answers, evicting a materialized tree nothing keeps and keeping the one the
// record pins.
func TestCheckpointGCAfterDeployRealStore(t *testing.T) {
	needGit(t)
	root := t.TempDir()
	for rel, body := range map[string]string{opSite + "/xbin.json": "{}", opSite + "/index.html": "<h1>v1</h1>"} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p := &Plane{Root: root, Reg: reg, Hub: events.NewHub(), isolated: func() bool { return false }}
	reg.PinnedPrimary = p.PinnedPrimary
	if err := p.Boot(); err != nil {
		t.Fatal(err)
	}
	unkept := filepath.Join(root, ".xbin", "deploy", util.TileKey(opSite), strings.Repeat("d", 40))
	if err := os.MkdirAll(unkept, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Do(context.Background(), ownerP, OpPause, &PauseRequest{Tile: opSite}); err != nil {
		t.Fatal(err)
	}
	rec, err := p.record(opSite)
	if err != nil || rec == nil {
		t.Fatalf("no record after the pause: %v", err)
	}
	pinned := filepath.Join(root, ".xbin", "deploy", util.TileKey(opSite), *rec.Deployments[util.MainDeployment].Checkpoint)
	if _, err := os.Lstat(unkept); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the materialized tree nothing keeps is still there: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(pinned, "index.html")); err != nil {
		t.Errorf("the pinned tree went: %v", err)
	}
}

// retention is the GC runs and prunes among a store's events.
func retention(events []string) []string {
	var out []string
	for _, e := range events {
		if strings.HasPrefix(e, "gc ") || strings.HasPrefix(e, "prune ") {
			out = append(out, e)
		}
	}
	return out
}

// covers P9 SC-ROLLBACK NP-02-3 T18 — after each successful deploy the
// runner's pruning keeps a deployment's current checkpoint and the
// checkpoints of its previous three successful deploy-log entries, so a
// roll back to one of them compiles nothing; a failed attempt is no roll-back
// target; everything else of the tile's checkpoint artifacts goes, while the
// live reload target's bin and another tile's artifact under the same
// CompKey stay. A deploy log that can't be read prunes nothing.
func TestPruneKeepsRetained(t *testing.T) {
	f, st, run := newRetainFx(t)
	run.real = runner.New(f.root, nil, f.hub, f.reg)
	liveBin := filepath.Join(f.root, ".xbin", "build", util.CompKey(opAPI), "bin")
	if err := os.MkdirAll(filepath.Dir(liveBin), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveBin, []byte("#!live\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale, foreign := strings.Repeat("e", 40), strings.Repeat("f", 40)
	writeArtifact(f.root, opAPI, stale, opAPI)
	writeArtifact(f.root, opAPI, foreign, "apps/other") // a CompKey twin's artifact

	var trees []string // each successful deploy's checkpoint, in order
	deploy := func(v string) string {
		t.Helper()
		f.write(opAPI+"/main.go", "package main // "+v+"\n")
		op := OpReloadNow
		var req any = &ReloadNowRequest{Tile: opAPI}
		if len(trees) == 0 {
			op, req = OpPause, &PauseRequest{Tile: opAPI}
		}
		e := f.wait(opAPI, f.must(ownerP, op, req).Deploy.ID)
		if e.Result != resultOK {
			t.Fatalf("deploy %s: %+v", v, e)
		}
		tree := f.current(opAPI, util.MainDeployment)
		trees = append(trees, tree)
		return tree
	}
	have := func(tree string) bool {
		_, err := os.Lstat(artifactDir(f.root, opAPI, tree))
		return err == nil
	}
	expect := func(when string, kept, gone []string) {
		t.Helper()
		for _, tree := range kept {
			if !have(tree) {
				t.Errorf("%s: %s's artifact was pruned", when, shortTree(tree))
			}
		}
		for _, tree := range gone {
			if have(tree) {
				t.Errorf("%s: %s's artifact is still there", when, shortTree(tree))
			}
		}
		if _, err := os.Lstat(liveBin); err != nil {
			t.Errorf("%s: the live reload target's bin went: %v", when, err)
		}
		if !have(foreign) {
			t.Errorf("%s: another tile's artifact under the same CompKey went", when)
		}
	}

	for _, v := range []string{"v1", "v2", "v3", "v4", "v5"} {
		deploy(v)
	}
	expect("after five deploys", trees[1:5], []string{trees[0], stale})
	if _, _, prunes := st.seen(); !reflect.DeepEqual(prunes[len(prunes)-1], sortedTrees(trees[1:5])) {
		t.Errorf("the last pruning kept %v, want the current and three roll-back targets %v", prunes[len(prunes)-1], sortedTrees(trees[1:5]))
	}

	// A failed attempt is no roll-back target: the next deploy prunes it.
	run.set(func(r *fakeRunner) { r.fail = errors.New("health check failed") })
	f.write(opAPI+"/main.go", "package main // v6\n")
	failed := f.wait(opAPI, f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}).Deploy.ID)
	run.set(func(r *fakeRunner) { r.fail = nil })
	if failed.Result != resultFailed {
		t.Fatalf("the failing deploy: %+v", failed)
	}
	failedTree := f.st.logged(opAPI)[len(f.st.logged(opAPI))-1].Tree
	deploy("v7")
	expect("after a failed deploy and another", []string{trees[5], trees[4], trees[3], trees[2]}, []string{trees[1], failedTree})

	// A roll back reaches a retained target without compiling.
	e := f.wait(opAPI, f.must(ownerP, OpRollback, &RollbackRequest{Tile: opAPI}).Deploy.ID)
	back := f.current(opAPI, util.MainDeployment)
	if e.Result != resultOK || !slices.Contains(trees[2:5], back) {
		t.Fatalf("the roll back: %+v onto %s, want one of the retained targets", e, shortTree(back))
	}
	if !run.wasThere(back) {
		t.Errorf("the roll back onto %s had to build its artifact", shortTree(back))
	}

	// A deploy log that can't be read prunes nothing.
	writeArtifact(f.root, opAPI, stale, opAPI)
	f.st.set(func(s *fakeStore) { s.noLog = true })
	deploy("v8")
	if !have(stale) {
		t.Error("an unreadable deploy log pruned an artifact")
	}
}

// sortedTrees is trees sorted, as the pruning's keep set is.
func sortedTrees(trees []string) []string {
	out := slices.Clone(trees)
	sort.Strings(out)
	return out
}

// covers P5 P16 T10 — at boot, once a record governs a tile, the store's
// sweep removes the .tmp-* extractions killed runs left under .xbin/deploy
// before anything materializes (the pinned primary's preparation comes
// after it), leaving materialized trees and per-deployment state; a
// workspace where no record governs a tile is swept by nothing, and its
// leftovers stay as they are (the zero state reads nothing more at boot).
func TestSweepAtBoot(t *testing.T) {
	t.Run("through the plane", func(t *testing.T) {
		f, st, run := newRetainFx(t)
		if n := st.count("sweep"); n != 0 {
			t.Fatalf("a workspace without a record was swept %d times at boot", n)
		}
		f.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		st.mu.Lock()
		st.events = nil
		st.mu.Unlock()
		bootRetaining(f, st, run)
		events, _, _ := st.seen()
		if len(events) < 2 || events[0] != "sweep" || !slices.Contains(events, "materialize "+opSite) {
			t.Errorf("the boot's store calls = %v, want the sweep before the primary's materialization", events)
		}
	})

	t.Run("the checkpoint store's sweep", func(t *testing.T) {
		for _, withRecord := range []bool{false, true} {
			root := t.TempDir()
			const tile = "apps/site"
			trees := filepath.Join(root, ".xbin", "deploy", util.TileKey(tile))
			leftover := filepath.Join(trees, ".tmp-1234")
			tree := filepath.Join(trees, strings.Repeat("b", 40))
			derived := filepath.Join(trees, "d", "dev")
			for _, d := range []string{leftover, tree, derived} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(d, "index.html"), []byte("x"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if withRecord {
				rec := ZeroRecord(tile)
				rec.Schema, rec.Seq, rec.Created = RecordSchema, 2, "2026-09-01T10:00:00Z"
				data, err := rec.encode()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(recordDir(root), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(recordPath(root, tile), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			p := &Plane{Root: root} // xbind's own store, built from Root
			if err := p.Boot(); err != nil {
				t.Fatalf("Boot: %v", err)
			}
			if f := p.Lookup(tile); withRecord && f.State != RecordActive {
				t.Fatalf("the hand-written record = %v, %v", f.State, f.Err)
			}
			_, err := os.Lstat(leftover)
			if gone := errors.Is(err, os.ErrNotExist); gone != withRecord {
				t.Errorf("with a record %v: the leftover extraction gone = %v", withRecord, gone)
			}
			for _, keep := range []string{tree, derived} {
				if _, err := os.Lstat(filepath.Join(keep, "index.html")); err != nil {
					t.Errorf("with a record %v: %s went: %v", withRecord, keep, err)
				}
			}
		}
	})
}

// restoreStore is the fixture's fake store with the checkpoint store's
// Restore: it creates the store, and makes each restored retention root's
// tree a checkpoint of the tile, holding files.
type restoreStore struct {
	*fakeStore
	mu    sync.Mutex
	calls []string // "<tile> <objects> <refs, sorted>"
	files map[string]string
	err   error
}

func (s *restoreStore) Restore(ctx context.Context, tile, objects string, refs map[string]string) error {
	s.mu.Lock()
	s.calls = append(s.calls, tile+" "+objects+" "+strings.Join(sortedKeys(refs), ","))
	err := s.err
	s.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(storeDir(s.fakeStore.root, tile), 0o755); err != nil {
		return err
	}
	for ref := range refs {
		tree, ok := strings.CutPrefix(ref, "refs/xbin/restored/checkpoints/")
		if !ok {
			continue
		}
		s.fakeStore.set(func(fs *fakeStore) {
			fs.files[tree] = s.files
			if fs.cps[tile] == nil {
				fs.cps[tile] = map[string]checkpoint.Checkpoint{}
			}
			fs.cps[tile][tree] = checkpoint.Checkpoint{ID: "c:" + tree[:7], Hash: tree}
		})
	}
	return nil
}

func (s *restoreStore) restores() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// covers T11 P29 P9 P5 — the broker's restore hook puts a tile's deployment
// state back: an archive naming another tile (its record's path), a ref
// outside the restore-only namespace or an id that isn't one is refused
// before anything is written; otherwise the store is rebuilt from the staged
// objects, the record is installed only when the tile has none — the tile's
// own record wins, one made for another owner is installed inert, one
// naming a checkpoint the store doesn't hold isn't installed — and a record
// that governs the tile has its pinned primary prepared and its view
// repository refreshed. With opting in closed, or a store that can't
// restore, nothing is put back.
func TestRestorePutsBackDeploymentState(t *testing.T) {
	files := map[string]string{"xbin.json": "{}", "index.html": "<h1>restored</h1>"}
	tree := treeOf(files)
	commit := strings.Repeat("c", 40)
	archived := func(tile, owner string, pinned bool) []byte {
		rec := &Record{Schema: RecordSchema, Tile: tile, Owner: owner, Created: "2026-09-01T10:00:00Z", Seq: 4,
			LastLiveReload: util.MainDeployment, Primary: util.MainDeployment, NextDeploy: 7,
			Deployments: map[string]*DeploymentRecord{util.MainDeployment: {}}}
		if pinned {
			tr := tree
			rec.Deployments[util.MainDeployment].Checkpoint = &tr
		} else {
			rec.LiveReload = util.MainDeployment
		}
		data, err := rec.encode()
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	refs := map[string]string{"refs/xbin/restored/checkpoints/" + tree: commit, "refs/xbin/restored/log/main": commit}
	type fx struct {
		*opsFx
		st     *restoreStore
		owners map[string]string
		stage  string
	}
	newFx := func(t *testing.T) *fx {
		f := newOpsFx(t, true)
		x := &fx{opsFx: f, st: &restoreStore{fakeStore: f.st, files: files}, owners: map[string]string{}, stage: t.TempDir()}
		p := &Plane{Root: f.root, Reg: f.reg, Hub: f.hub, Run: f.run, cps: x.st, isolated: func() bool { return true },
			OwnerRef: func(tile string) string { return x.owners[tile] }}
		f.reg.PinnedPrimary = p.PinnedPrimary
		if err := p.Boot(); err != nil {
			t.Fatalf("Boot: %v", err)
		}
		f.p = p
		return x
	}
	restore := func(x *fx, tile string, record []byte, refs map[string]string) error {
		return x.p.RestoreDeploymentState(context.Background(), tile, record, x.stage, refs)
	}
	wroteNothing := func(t *testing.T, x *fx) {
		t.Helper()
		for _, p := range []string{recordDir(x.root), filepath.Join(x.root, "data", "checkpoints")} {
			if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s was written: %v", p, err)
			}
		}
		if n := len(x.st.restores()); n != 0 {
			t.Errorf("the store was rebuilt %d times", n)
		}
	}

	t.Run("another tile's archive is refused before anything is written", func(t *testing.T) {
		x := newFx(t)
		for name, c := range map[string]struct {
			record []byte
			refs   map[string]string
		}{
			"another tile's record":       {archived(opAPI, "", true), refs},
			"a record that doesn't parse": {[]byte(`{"tile":"` + opSite + `","schema":1`), refs},
			"a live checkpoint ref":       {archived(opSite, "", true), map[string]string{"refs/xbin/checkpoints/" + tree: commit}},
			"a ref naming no object id":   {archived(opSite, "", true), map[string]string{"refs/xbin/restored/log/main": "HEAD"}},
			"a log ref of no deployment":  {nil, map[string]string{"refs/xbin/restored/log/Main!": commit}},
			"a stage that isn't a stage":  {archived(opSite, "", true), nil},
		} {
			stage := x.stage
			if c.refs == nil {
				x.stage = filepath.Join(x.root, "no-such-stage")
			}
			if err := restore(x, opSite, c.record, c.refs); err == nil {
				t.Errorf("%s: restored", name)
			}
			x.stage = stage
			wroteNothing(t, x)
		}
		if pc, ok := x.p.PinnedPrimary(opSite); ok || pc != nil {
			t.Errorf("PinnedPrimary = %v, %v after refused restores", pc, ok)
		}
	})

	t.Run("a tile without a record gets its record, store and view back", func(t *testing.T) {
		x := newFx(t)
		data := archived(opSite, "", true)
		if err := restore(x, opSite, data, refs); err != nil {
			t.Fatal(err)
		}
		if got := x.st.restores(); len(got) != 1 || got[0] != opSite+" "+x.stage+" "+strings.Join(sortedKeys(refs), ",") {
			t.Errorf("the store's restores = %v", got)
		}
		b, err := os.ReadFile(recordPath(x.root, opSite))
		if err != nil || string(b) != string(data) {
			t.Errorf("the installed record = %q, %v; want the archived one", b, err)
		}
		fd := x.p.Lookup(opSite)
		if fd.State != RecordActive || fd.Record.Seq != 4 || fd.Record.NextDeploy != 7 || x.current(opSite, util.MainDeployment) != tree {
			t.Errorf("the restored record = %v %+v", fd.State, fd.Record)
		}
		if pc, ok := x.p.PinnedPrimary(opSite); !ok || pc == nil || pc.ManifestErr != "" {
			t.Errorf("the pinned primary isn't prepared: %+v, %v", pc, ok)
		}
		x.st.fakeStore.mu.Lock()
		view := x.st.fakeStore.views[opSite]
		x.st.fakeStore.mu.Unlock()
		if !reflect.DeepEqual(view, map[string]string{util.MainDeployment: tree}) {
			t.Errorf("the view repository holds %v", view)
		}
	})

	t.Run("the tile's own record wins", func(t *testing.T) {
		x := newFx(t)
		x.must(ownerP, OpPause, &PauseRequest{Tile: opSite})
		mine, _ := os.ReadFile(recordPath(x.root, opSite))
		if err := restore(x, opSite, archived(opSite, "", true), refs); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(recordPath(x.root, opSite)); string(b) != string(mine) {
			t.Errorf("the restore replaced the tile's record:\n%s", b)
		}
		if n := len(x.st.restores()); n != 1 {
			t.Errorf("the store was rebuilt %d times, want once beside the tile's record", n)
		}
	})

	t.Run("a record made for another owner is installed inert", func(t *testing.T) {
		x := newFx(t)
		x.owners[opSite] = "user:bob"
		if err := restore(x, opSite, archived(opSite, "user:ana", true), refs); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(recordPath(x.root, opSite)); err != nil {
			t.Errorf("the record isn't there: %v", err)
		}
		if fd := x.p.Lookup(opSite); fd.State != RecordInert {
			t.Errorf("the record for another owner = %v, want inert", fd.State)
		}
		if pc, ok := x.p.PinnedPrimary(opSite); ok || pc != nil {
			t.Errorf("an inert record pins the primary: %+v", pc)
		}
	})

	t.Run("a record naming a checkpoint the store lacks isn't installed", func(t *testing.T) {
		x := newFx(t)
		logOnly := map[string]string{"refs/xbin/restored/log/main": commit}
		if err := restore(x, opSite, archived(opSite, "", true), logOnly); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(recordPath(x.root, opSite)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the record was installed: %v", err)
		}
		if fd := x.p.Lookup(opSite); fd.State != RecordNone {
			t.Errorf("the tile = %v, want the zero state", fd.State)
		}
	})

	t.Run("a record that pins nothing needs no store", func(t *testing.T) {
		x := newFx(t)
		if err := x.p.RestoreDeploymentState(context.Background(), opSite, archived(opSite, "", false), "", nil); err != nil {
			t.Fatal(err)
		}
		if fd := x.p.Lookup(opSite); fd.State != RecordActive || fd.Record.LiveReload != util.MainDeployment {
			t.Errorf("the restored record = %v %+v", fd.State, fd.Record)
		}
		if n := len(x.st.restores()); n != 0 {
			t.Errorf("the store was rebuilt %d times without an archived store", n)
		}
	})

	t.Run("switched off, nothing is put back", func(t *testing.T) {
		x := newFx(t)
		x.p.OptInClosed = true
		if err := restore(x, opSite, archived(opSite, "", true), refs); err != nil {
			t.Fatal(err)
		}
		wroteNothing(t, x)
	})

	t.Run("a store that can't restore puts nothing back", func(t *testing.T) {
		f := newOpsFx(t, true) // the plain fake: no Restore
		if err := f.p.RestoreDeploymentState(context.Background(), opSite, archived(opSite, "", true), t.TempDir(), refs); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(recordDir(f.root)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a record was written: %v", err)
		}
	})

	t.Run("a failed rebuild refuses the restore", func(t *testing.T) {
		x := newFx(t)
		x.st.err = errors.New("fsck failed")
		if err := restore(x, opSite, archived(opSite, "", true), refs); err == nil {
			t.Error("restored over a failed rebuild")
		}
		if _, err := os.Lstat(recordDir(x.root)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a record was written: %v", err)
		}
	})
}

// waitFor polls cond until it holds, failing the test after a while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 1000; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
