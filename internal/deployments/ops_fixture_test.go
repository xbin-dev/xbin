package deployments

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// The fixture's tiles: a static one and backends of each sandboxed runtime.
const (
	opSite = "apps/site" // static
	opAPI  = "apps/api"  // go
	opNode = "apps/node" // node
	opPy   = "apps/py"   // python
)

// opsFx is a workspace with a booted plane over a fake checkpoint store and
// a fake runner, as the D63 pattern builds planes in tests: a literal of the
// plane's inputs, no broker.
type opsFx struct {
	t    *testing.T
	root string
	reg  *registry.Registry
	hub  *events.Hub
	run  *fakeRunner
	st   *fakeStore
	p    *Plane
	iso  bool
	evs  *eventTape
}

// newOpsFx builds the workspace and boots a plane over it; isolated says
// whether backends may be pinned (P18).
func newOpsFx(t *testing.T, isolated bool) *opsFx {
	t.Helper()
	root := t.TempDir()
	f := &opsFx{t: t, root: root, iso: isolated, run: &fakeRunner{}, st: newFakeStore(root)}
	f.write(opSite+"/xbin.json", `{}`)
	f.write(opSite+"/index.html", "<h1>v1</h1>")
	f.write(opAPI+"/xbin.json", `{"runtime":"go"}`)
	f.write(opAPI+"/main.go", "package main\n")
	f.write(opNode+"/xbin.json", `{"runtime":"node"}`)
	f.write(opPy+"/xbin.json", `{"runtime":"python"}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f.reg, f.hub = reg, events.NewHub()
	f.boot()
	return f
}

// boot builds and boots a new plane over the same workspace, store and
// runner: xbind restarting.
func (f *opsFx) boot() *Plane {
	f.t.Helper()
	p := &Plane{Root: f.root, Reg: f.reg, Hub: f.hub, Run: f.run, cps: f.st,
		isolated: func() bool { return f.iso }}
	f.reg.PinnedPrimary = p.PinnedPrimary
	f.evs = tapeOf(f.hub)
	if err := p.Boot(); err != nil {
		f.t.Fatalf("Boot: %v", err)
	}
	f.p = p
	return p
}

func (f *opsFx) write(rel, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// do dispatches one operation as the handler would.
func (f *opsFx) do(pr auth.Principal, op Op, req any) (any, error) {
	f.t.Helper()
	return f.p.Do(context.Background(), pr, op, req)
}

// must dispatches one operation that must succeed, and returns its answer.
func (f *opsFx) must(pr auth.Principal, op Op, req any) Answer {
	f.t.Helper()
	res, err := f.do(pr, op, req)
	if err != nil {
		f.t.Fatalf("%s %+v: %v", op, req, err)
	}
	a, ok := res.(Answer)
	if !ok {
		f.t.Fatalf("%s answered %T, want Answer", op, res)
	}
	return a
}

// settle waits for an answer's deploy when it is still queued or running.
func (f *opsFx) settle(tile string, a Answer) {
	f.t.Helper()
	if a.Deploy != nil && (a.Deploy.Result == resultQueued || a.Deploy.Result == resultRunning) {
		f.wait(tile, a.Deploy.ID)
	}
}

// wait waits for attempt id of tile to finish and returns its entry.
func (f *opsFx) wait(tile string, id int64) DeployEntry {
	f.t.Helper()
	e, err := f.p.Entry(context.Background(), tile, id, 10*time.Second)
	if err != nil {
		f.t.Fatalf("Entry(%s, %d): %v", tile, id, err)
	}
	if e.Result == resultQueued || e.Result == resultRunning {
		f.t.Fatalf("deploy %d of %s still %s", id, tile, e.Result)
	}
	return e
}

// rec is tile's record as the index holds it; nil in the zero state.
func (f *opsFx) rec(tile string) *Record {
	r, err := f.p.record(tile)
	if err != nil {
		f.t.Fatalf("record(%s): %v", tile, err)
	}
	return r
}

// errStatus is err's HTTP status (0 for nil) and text.
func errStatus(err error) (int, string) {
	var e *Error
	if errors.As(err, &e) {
		return e.Status, e.Msg
	}
	if err == nil {
		return 0, ""
	}
	return -1, err.Error()
}

// ---- principals ----

var ownerP = auth.Principal{Owner: true, Via: "bearer"}

// userP is a person in their own session holding level on tile.
func userP(id, tile, level string) auth.Principal {
	u := &users.User{ID: id, Role: users.RoleUser, Tiles: map[string]string{tile: level}}
	return auth.Principal{UserID: id, User: u, Via: "session"}
}

// terminalP is tile's terminal token driven by a person holding level.
func terminalP(id, tile, level string) auth.Principal {
	u := &users.User{ID: id, Role: users.RoleUser, Tiles: map[string]string{tile: level}}
	return auth.Principal{Component: tile, UserID: id, User: u, Via: "terminal"}
}

// ---- the event tape ----

// eventTape records every event the hub publishes.
type eventTape struct {
	mu  sync.Mutex
	evs []events.Event
}

func tapeOf(h *events.Hub) *eventTape {
	t := &eventTape{}
	ch, _ := h.Subscribe(nil)
	go func() {
		for e := range ch {
			t.mu.Lock()
			t.evs = append(t.evs, e)
			t.mu.Unlock()
		}
	}()
	return t
}

// settle waits for the hub to deliver what was published so far.
func (t *eventTape) settle() { time.Sleep(20 * time.Millisecond) }

func (t *eventTape) take() []events.Event {
	t.settle()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.evs
	t.evs = nil
	return out
}

// ---- the fake runner ----

// fakeRunner deploys by calling commit, unless told to fail or to hold.
type fakeRunner struct {
	mu      sync.Mutex
	deploys []string // "tile/dep@tree"
	changed []string // "tile/dep"
	fail    error    // Deploy fails before its swap
	// before and after hold a Deploy before its swap and after its commit
	// until closed (a crash test never closes them).
	before, after chan struct{}
	started       chan string
	commitErrs    []error
	identical     []bool // each Deploy's Code.Identical
}

func (r *fakeRunner) Deploy(ctx context.Context, c *registry.Component, dep string, code runner.Code, commit func() error, progress runner.DeployProgress) error {
	r.mu.Lock()
	r.deploys = append(r.deploys, c.Path+"/"+dep+"@"+code.Tree)
	r.identical = append(r.identical, code.Identical)
	fail, before, after, started := r.fail, r.before, r.after, r.started
	r.mu.Unlock()
	progress("build", "running", nil)
	if started != nil {
		started <- code.Tree
	}
	if before != nil {
		<-before
	}
	if fail != nil {
		return fail
	}
	progress("start", "running", nil)
	err := commit()
	r.mu.Lock()
	r.commitErrs = append(r.commitErrs, err)
	r.mu.Unlock()
	if after != nil {
		<-after
	}
	return err
}

func (r *fakeRunner) ChangedDeployment(c *registry.Component, dep string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.changed = append(r.changed, c.Path+"/"+dep)
}

func (r *fakeRunner) ChangedTile(c *registry.Component) {}
func (r *fakeRunner) StopDeployment(tile, dep string)   {}
func (r *fakeRunner) RootsInUse() []string              { return nil }

func (r *fakeRunner) set(fn func(r *fakeRunner)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	fn(r)
}

func (r *fakeRunner) counts() (deploys, changed int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.deploys), len(r.changed)
}

// ---- the fake checkpoint store ----

// fakeStore is the checkpoint store the plane sees in tests: captures hash
// the work tree (or go through a real store, for the unit-git tests), trees
// materialize from the snapshot taken at capture, and the deploy log and the
// view repository live in memory. The on-disk marks the plane's leftovers
// read (the store and view directories) are made where the real store makes
// them.
type fakeStore struct {
	mu    sync.Mutex
	root  string
	real  *checkpoint.Store
	files map[string]map[string]string                // tree → path → content
	cps   map[string]map[string]checkpoint.Checkpoint // tile → tree → checkpoint
	logs  map[string][]attempt                        // tile → entries, oldest first
	views map[string]map[string]string                // tile → the pinned set
	// hook runs inside every committed capture, after the tree is taken.
	hook            func(tile string)
	failCapture     error
	failMaterialize error
	noLog           bool
	captures        int
}

func newFakeStore(root string) *fakeStore {
	return &fakeStore{root: root, files: map[string]map[string]string{},
		cps: map[string]map[string]checkpoint.Checkpoint{}, logs: map[string][]attempt{}, views: map[string]map[string]string{}}
}

func (s *fakeStore) storeDir(tile string) string { return storeDir(s.root, tile) }

func (s *fakeStore) Exists(tile string) bool {
	if s.real != nil {
		return s.real.Exists(tile)
	}
	_, err := os.Lstat(s.storeDir(tile))
	return err == nil
}

func (s *fakeStore) Caps() checkpoint.Caps {
	if s.real != nil {
		return s.real.Caps
	}
	return checkpoint.DefaultCaps()
}

func (s *fakeStore) Estimate(ctx context.Context, src checkpoint.Source) (checkpoint.Estimate, error) {
	if s.real != nil {
		return s.real.Estimate(ctx, src)
	}
	files, err := workTreeFiles(src)
	if err != nil {
		return checkpoint.Estimate{}, err
	}
	var e checkpoint.Estimate
	e.Entries = len(files)
	for _, b := range files {
		e.Bytes += int64(len(b))
	}
	return e, nil
}

func (s *fakeStore) Capture(ctx context.Context, req checkpoint.CaptureRequest) (checkpoint.Result, error) {
	s.mu.Lock()
	fail := s.failCapture
	s.mu.Unlock()
	if fail != nil {
		return checkpoint.Result{}, fail
	}
	files, err := workTreeFiles(req.Source)
	if err != nil {
		return checkpoint.Result{}, err
	}
	var res checkpoint.Result
	if s.real != nil {
		if res, err = s.real.Capture(ctx, req); err != nil {
			return res, err
		}
	} else {
		if !req.Create && !s.Exists(req.Tile) {
			return res, fmt.Errorf("%s: %w", req.Tile, checkpoint.ErrNoStore)
		}
		if err := os.MkdirAll(s.storeDir(req.Tile), 0o755); err != nil {
			return res, err
		}
		tree := treeOf(files)
		res.Checkpoint = checkpoint.Checkpoint{ID: "c:" + tree[:7], Hash: tree, Feed: checkpoint.FeedWorkTree,
			At: time.Now().UTC().Truncate(time.Second), By: req.By}
	}
	s.mu.Lock()
	s.captures++
	if s.cps[req.Tile] == nil {
		s.cps[req.Tile] = map[string]checkpoint.Checkpoint{}
	}
	if old, ok := s.cps[req.Tile][res.Hash]; ok {
		res.Checkpoint = old
	} else {
		res.New = true
		s.cps[req.Tile][res.Hash] = res.Checkpoint
	}
	s.files[res.Hash] = files
	hook := s.hook
	s.mu.Unlock()
	if hook != nil && req.Create {
		hook(req.Tile)
	}
	return res, nil
}

func (s *fakeStore) Resolve(ctx context.Context, tile, id string) (checkpoint.Checkpoint, error) {
	if s.real != nil {
		return s.real.Resolve(ctx, tile, id)
	}
	prefix, err := checkpoint.ParseID(id)
	if err != nil {
		return checkpoint.Checkpoint{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var hits []checkpoint.Checkpoint
	for tree, cp := range s.cps[tile] {
		if strings.HasPrefix(tree, prefix) {
			hits = append(hits, cp)
		}
	}
	switch len(hits) {
	case 0:
		return checkpoint.Checkpoint{}, fmt.Errorf("%s has no checkpoint %s: %w", tile, id, checkpoint.ErrUnknownCheckpoint)
	case 1:
		return hits[0], nil
	}
	return checkpoint.Checkpoint{}, fmt.Errorf("checkpoint id %s is ambiguous in %s; use more digits: %w", id, tile, checkpoint.ErrAmbiguousID)
}

func (s *fakeStore) Get(ctx context.Context, tile, tree string) (checkpoint.Checkpoint, error) {
	if s.real != nil {
		return s.real.Get(ctx, tile, tree)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cp, ok := s.cps[tile][tree]; ok {
		return cp, nil
	}
	return checkpoint.Checkpoint{}, fmt.Errorf("%s has no checkpoint %s: %w", tile, tree, checkpoint.ErrUnknownCheckpoint)
}

func (s *fakeStore) Materialize(tile, tree string) (string, error) {
	s.mu.Lock()
	fail, files := s.failMaterialize, s.files[tree]
	s.mu.Unlock()
	if fail != nil {
		return "", fail
	}
	if files == nil {
		return "", fmt.Errorf("%s: no checkpoint %s to materialize", tile, tree)
	}
	dir := filepath.Join(s.root, ".xbin", "deploy", util.TileKey(tile), tree)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(body), 0o444); err != nil && !errors.Is(err, fs.ErrPermission) {
			return "", err
		}
	}
	return dir, nil
}

func (s *fakeStore) AppendLog(ctx context.Context, tile string, a attempt) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noLog {
		return errNotBuilt("the deploy log")
	}
	a.done, a.g = nil, Grant{}
	s.logs[tile] = append(s.logs[tile], a)
	return nil
}

func (s *fakeStore) ReadLog(ctx context.Context, tile, dep string) ([]attempt, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.noLog {
		return nil, errNotBuilt("the deploy log")
	}
	var out []attempt
	for i := len(s.logs[tile]) - 1; i >= 0; i-- {
		if a := s.logs[tile][i]; dep == "" || a.Deployment == dep {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeStore) SyncView(ctx context.Context, tile string, pinned map[string]string, primary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.views[tile] = pinned
	return os.MkdirAll(viewDir(s.root, tile), 0o755)
}

func (s *fakeStore) RemoveView(ctx context.Context, tile string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.views, tile)
	return os.RemoveAll(viewDir(s.root, tile))
}

// Drift counts nothing: the drift count's own tests fake it (worktree_test.go).
func (s *fakeStore) Drift(ctx context.Context, src checkpoint.Source, tree string) (int, error) {
	return 0, nil
}

// logged is tile's deploy log, oldest first.
func (s *fakeStore) logged(tile string) []attempt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]attempt(nil), s.logs[tile]...)
}

func (s *fakeStore) set(fn func(s *fakeStore)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// workTreeFiles reads a work tree's files, .git directories and nested
// components left out, as a checkpoint holds them.
func workTreeFiles(src checkpoint.Source) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(src.WorkTree, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src.WorkTree, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" || nestedIn(src.Nested, rel) {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			target, err := os.Readlink(p)
			out[rel] = "symlink → " + target
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	return out, err
}

func nestedIn(nested []string, rel string) bool {
	for _, n := range nested {
		if rel == n {
			return true
		}
	}
	return false
}

// treeOf is a stable 40-digit id of a file set.
func treeOf(files map[string]string) string {
	keys := make([]string, 0, len(files))
	for k := range files {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha1.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%d\x00%s\x00", k, len(files[k]), files[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// needGit skips a unit-git test on a host without git or GNU find (a store
// in direct mode runs the host's), and refuses one run with confinement on.
func needGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if out, err := exec.Command("find", "--version").CombinedOutput(); err != nil || !strings.Contains(string(out), "GNU") {
		t.Skip("no GNU find on this host")
	}
	if confine.Isolated() {
		t.Fatal("confinement is on in a direct-mode test")
	}
}

// newGitOpsFx is newOpsFx over a real checkpoint store (confined git, run
// directly as every confined run is without isolation); materializing, the
// deploy log and the view repository stay the fake's.
func newGitOpsFx(t *testing.T, isolated bool) *opsFx {
	t.Helper()
	needGit(t)
	f := newOpsFx(t, isolated)
	f.st.real = checkpoint.New(f.root)
	return f
}

// restartingRunner is a fakeRunner with the runner's Restart.
type restartingRunner struct {
	*fakeRunner
	restarts []string
}

func (r *restartingRunner) Restart(ctx context.Context, c *registry.Component, dep string, progress runner.DeployProgress) error {
	r.mu.Lock()
	r.restarts = append(r.restarts, c.Path+"/"+dep)
	fail := r.fail
	r.mu.Unlock()
	progress("build", "running", nil)
	return fail
}
