// Package deployments is the deployments plane: pausing live reload, tile
// deployments and promotion. It owns the deployment record, the operations
// that change what a tile's deployments run, and the answers the rest of
// xbind asks about them: which code a deployment runs, where it serves from,
// what the registry composes for a pinned primary, which deployment live
// reload drives.
//
// A plane in the D63 style: a struct whose fields are the exact answers it
// needs from the rest of xbind (the runner as a facade, the broker's gates
// and actions as funcs), built by boot, which installs its methods as the
// registry's, runner's, broker's and terminal manager's deployment hooks.
// Tests build it from a literal of those fields, with no broker.
//
// A tile without a deployment record is in the zero state (D119c): every method
// answers exactly as xbind did before tile deployments, with no file read or
// written; its one deployment is main, the primary, following the work tree.
// A tile with one answers from it (record.go), through the in-memory index
// Boot fills (index.go); a record that can't be used holds its tile, which
// then fails closed (06-security C7): no backend, no inbound surface, never
// its work tree in place of a pinned checkpoint.
//
// The files: plane.go holds the Plane, its hooks and the checkpoint store it
// builds for itself; record.go and index.go the record; authz.go and
// dispatch.go authority and the operation registry; ops.go the operations
// (their requests, answers and runs); queue.go the deploys (one in flight
// per deployment, the journal that keeps attempts through a crash, the
// deploy log's reads); events.go what clients see (the deployments event,
// the State); worktree.go the drift count while live reload is paused;
// m2types.go the declarations for deployments beyond main and the answers
// to their hooks.
package deployments

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// Runner is what the plane asks of the runner: *runner.Runner in xbind, a
// fake in tests. Ensure, Track, Changed and Stop keep meaning the primary;
// the plane names the deployment it acts on (D127d).
type Runner interface {
	// Deploy puts code on deployment dep of c through blue/green; commit runs
	// after the swap and before the result is reported.
	Deploy(ctx context.Context, c *registry.Component, dep string, code runner.Code, commit func() error, progress runner.DeployProgress) error
	// ChangedDeployment rebuilds one deployment from what its record says.
	ChangedDeployment(c *registry.Component, dep string)
	// ChangedTile restarts every deployment of c with a generation.
	ChangedTile(c *registry.Component)
	// StopDeployment stops one deployment (its removal).
	StopDeployment(tile, dep string)
	// RootsInUse lists the materialized trees running generations bind.
	RootsInUse() []string
}

// statuser is the runner's view of one deployment's generation, which
// *runner.Runner reports; a runner without it (a test fake) counts every
// deployment as up.
type statuser interface {
	DeploymentStatus(tile, dep string) runner.DeploymentState
}

// down answers whether dep of c has no healthy generation — crash-looping,
// failed or not running — so deploying the checkpoint it already runs starts
// a new generation from the kept artifact instead of answering unchanged
// (11-contract §1.6; 07-runtime §8.7). A generation being built is on its
// way up; code without a backend has no generation to be down.
func (p *Plane) down(c *registry.Component, dep string) bool {
	s, ok := p.Run.(statuser)
	if !ok || !c.HasBackend() {
		return false
	}
	st := s.DeploymentStatus(c.Path, dep).State
	return st == "failed" || st == "idle"
}

// Plane is the deployments plane. Boot fills the fields step by step, before
// the daemon serves; nothing changes them afterwards.
type Plane struct {
	Root string             // the workspace root: data/deployments, data/checkpoints, .xbin/deploy
	Reg  *registry.Registry // the tiles; Rescan after a commit that changes the primary's code
	Hub  *events.Hub        // the deployments event (rule C2)
	Run  Runner             // generations: deploy, restart, stop

	// OwnerRef is a tile's current owner ref ("" = workspace-owned), which a
	// record must match to apply to the tile (D119i).
	OwnerRef func(tile string) string
	// IsAdmin answers the broker's admin question (an element principal whose
	// tile holds xbin admin passes it; the manager gate never relies on it).
	IsAdmin func(auth.Principal) bool
	// MayManage is the manager gate: a person in their own session who is a
	// workspace admin or manages the tile. No element principal passes.
	MayManage func(p auth.Principal, tile string) bool
	// AdminFrameDriver is the person behind the frame of a tile holding xbin
	// admin (the admin tile), minted under their own login (D127m, extended by
	// the owner 2026-09-28): the broker's. That frame does the acts marked
	// frame in the authority table when the person passes MayManage; nil,
	// or false, and no frame does any manager act.
	AdminFrameDriver func(p auth.Principal) (auth.Principal, bool)
	// TileEnv is the resource and identity env the broker derives for a
	// tile's backend: what the primary's generation gets, today's env.
	TileEnv func(c *registry.Component) []string
	// Provision provisions the resources the registry's scopes declare.
	Provision func()
	// ReconcileIngress re-derives the ingress listeners and doors from the
	// registry and the bindings.
	ReconcileIngress func()
	// RunNow delivers one cron job of a deployment once, whatever its
	// deliveries switch says; DropRegistrations removes a deployment's
	// cron jobs and bus subscriptions. Both are the broker's.
	RunNow            func(ctx context.Context, tile, dep, job string) (Delivery, error)
	DropRegistrations func(tile, dep string) error
	// DropDeploymentFiles deletes a deployment beyond main's own tile-keyed
	// files (vault, prefs, registration files, derived state; 08-data §3.3,
	// §9.2): the broker's. Call it outside index.dmu, since it removes the
	// registration files through RemoveDeploymentFile, whose prune takes it.
	DropDeploymentFiles func(tile, dep string) error
	DataHooks           // the broker's data namespace acts (datahooks.go)
	// TileLimits are a tile's cgroup caps, today's per-component ones: the
	// ceiling of every deployment's limits (D127n), in LimitsFor. Zero without
	// cgroup delegation.
	TileLimits cgroup.Limits

	// OptInClosed is the ship-dark switch turned off (--tile-deployments=off):
	// operations that create or extend deployment state are refused, those
	// that return a tile to the zero state stay allowed, and existing records
	// keep governing what runs. Only the authorize function reads it; the
	// zero-state answers below never do.
	OptInClosed bool

	// idx holds the records, from Boot on; nil (a literal that never booted)
	// answers the zero state for every tile.
	idx *index

	// cps is the checkpoint store: built from Root on first use (store), a
	// fake in tests. Building it creates nothing on disk (D119c).
	cps     checkpoints
	cpsOnce sync.Once
	// isolated answers whether tools and backends run sandboxed:
	// confine.Isolated in xbind (D119h); tests set it.
	isolated func() bool
	// now is the clock stamps read; nil is time.Now.
	now func() time.Time

	locks   tileLocks    // one operation per tile at a time (ops.go)
	pausing overlay      // tiles whose live reload an operation is detaching
	prep    preparations // pinned primaries' prepared code (PinnedPrimary)
	q       queue        // deploys, the journal, finished attempts (queue.go)
	guard   branchGuard  // saves reaching a target with an assigned branch (branch.go)
}

// Boot runs once, from boot's registry step, after the registry hooks are
// installed and before the first Provision and any backend start. It loads
// the deployment records into memory (PO-8: it reads them and never
// rewrites one). A record that can't be used holds its own tile, which fails
// closed; an error — data/deployments can't be read, so no tile's record
// can be judged — stops the daemon. Without data/deployments it reads
// nothing more and writes nothing; a literal without a Root reads nothing.
//
// For each tile a record governs it then reconciles the deploy journal into
// the deploy log (an attempt in flight at a crash is logged, never lost:
// queue.go) and prepares the pinned primary's code — its checkpoint
// materialized and read (registry.ReadCheckpoint) — so the registry's
// rescan composes the tile from the code it runs before anything serves or
// starts (D119e). A checkpoint that can't be prepared fails its tile closed,
// alone.
func (p *Plane) Boot() error {
	if p.Root == "" {
		return nil
	}
	idx := newIndex(p.Root, p.OwnerRef)
	if err := idx.load(); err != nil {
		return err
	}
	p.idx = idx
	tiles := p.boundTiles()
	if len(tiles) == 0 {
		return nil // the zero state: nothing more is read or written
	}
	p.sweepAtBoot() // leftover .tmp-* extractions, before anything materializes (retention.go)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, tile := range tiles {
		f := idx.get(tile)
		if f.State != RecordActive {
			continue
		}
		p.reconcileJournal(ctx, tile, f.Record)
		if d := f.Record.Deployments[f.Record.Primary]; d.Checkpoint != nil {
			_, _ = p.prepare(tile, *d.Checkpoint) // a failure is the tile's alone, answered by PinnedPrimary
		}
	}
	if p.Reg != nil {
		if err := p.Reg.Rescan(); err != nil {
			slog.Warn("deployments: rescan after loading the records", "err", err)
		}
	}
	return nil
}

// boundTiles lists the tiles a record file names at their own key, sorted.
func (p *Plane) boundTiles() []string {
	if p.idx == nil {
		return nil
	}
	p.idx.mu.RLock()
	defer p.idx.mu.RUnlock()
	return sortedKeys(p.idx.tiles)
}

// Lookup answers what tile's record means for it (the synthesized zero state
// for a tile it doesn't govern). An in-memory lookup.
func (p *Plane) Lookup(tile string) Found {
	if p.idx == nil {
		return Found{State: RecordNone, Record: ZeroRecord(tile)}
	}
	return p.idx.lookup(tile)
}

// record is tile's governing record: (rec, nil) while an active record
// governs it, (nil, err) while one holds it, (nil, nil) in the zero state (no
// record, or one that isn't the tile's). It allocates nothing.
func (p *Plane) record(tile string) (*Record, error) {
	if p.idx == nil {
		return nil, nil
	}
	switch f := p.idx.get(tile); f.State {
	case RecordActive:
		return f.Record, nil
	case RecordHeld:
		return nil, f.Err
	}
	return nil, nil
}

// ---- the checkpoint store ----

// checkpoints is what the plane asks of the checkpoint store: xbind's
// *checkpoint.Store through storeAdapter, a fake in tests.
type checkpoints interface {
	Exists(tile string) bool
	Estimate(ctx context.Context, src checkpoint.Source) (checkpoint.Estimate, error)
	Capture(ctx context.Context, req checkpoint.CaptureRequest) (checkpoint.Result, error)
	Resolve(ctx context.Context, tile, id string) (checkpoint.Checkpoint, error)
	Get(ctx context.Context, tile, tree string) (checkpoint.Checkpoint, error)
	Caps() checkpoint.Caps
	// Materialize returns the read-only host tree of tile's checkpoint tree,
	// preparing it when missing.
	Materialize(tile, tree string) (string, error)
	// AppendLog appends one finished attempt to its deployment's deploy log.
	AppendLog(ctx context.Context, tile string, a attempt) error
	// ReadLog is tile's deploy log, every deployment's entries ("" dep) or
	// one's, newest first.
	ReadLog(ctx context.Context, tile, dep string) ([]attempt, error)
	// SyncView makes the view repository hold exactly the git views of the
	// pinned deployments (name → full tree id), with HEAD naming primary's.
	SyncView(ctx context.Context, tile string, pinned map[string]string, primary string) error
	// RemoveView removes tile's view repository, a crashed refresh's
	// leftovers included, under the store's lock.
	RemoveView(ctx context.Context, tile string) error
	// Drift counts the files src's work tree differs in from checkpoint
	// tree, changing nothing durable (worktree.go).
	Drift(ctx context.Context, src checkpoint.Source, tree string) (int, error)
	// Diff and ServeFetch are the API's diff and checkpoint remote (reads.go).
	Diff(ctx context.Context, req checkpoint.DiffRequest) (checkpoint.DiffResult, error)
	ServeFetch(w http.ResponseWriter, r *http.Request, tile, rel string) error
}

// store is the plane's checkpoint store, built from Root on first use.
func (p *Plane) store() checkpoints {
	p.cpsOnce.Do(func() {
		if p.cps == nil {
			p.cps = storeAdapter{checkpoint.New(p.Root)}
		}
	})
	return p.cps
}

// isIsolated reports whether backends and tools run sandboxed (D119h).
func (p *Plane) isIsolated() bool {
	if p.isolated != nil {
		return p.isolated()
	}
	return confine.Isolated()
}

// clock is the time stamps read.
func (p *Plane) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// stamp is the clock as the wire spells times: RFC 3339, UTC.
func (p *Plane) stamp() string { return p.clock().UTC().Format(time.RFC3339) }

// ---- one operation per tile ----

// tileLocks serializes the operations on each tile: one reads the record,
// captures and commits before the next reads it.
type tileLocks struct {
	mu sync.Mutex
	m  map[string]*sync.Mutex
}

func (l *tileLocks) lock(tile string) func() {
	l.mu.Lock()
	if l.m == nil {
		l.m = map[string]*sync.Mutex{}
	}
	m := l.m[tile]
	if m == nil {
		m = &sync.Mutex{}
		l.m[tile] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// ---- pinned primaries' code ----

// preparations is the primaries' code, prepared: materialized and read, per
// tile and checkpoint — the one the record pins, and those a queued deploy
// will swap to. PinnedPrimary answers from it with two map lookups.
type preparations struct {
	mu sync.RWMutex
	m  map[string]map[string]preparation // tile → tree
}

// preparation is one checkpoint prepared for a tile's primary: its code, or
// why it can't be prepared.
type preparation struct {
	tree string
	pc   *registry.PinnedCode
	err  string
}

func (ps *preparations) get(tile, tree string) (preparation, bool) {
	ps.mu.RLock()
	defer ps.mu.RUnlock()
	pr, ok := ps.m[tile][tree]
	return pr, ok
}

// has reports whether tree is prepared for tile's primary, readably.
func (ps *preparations) has(tile, tree string) bool {
	pr, ok := ps.get(tile, tree)
	return ok && pr.pc != nil
}

func (ps *preparations) put(tile string, pr preparation) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.m == nil {
		ps.m = map[string]map[string]preparation{}
	}
	if ps.m[tile] == nil {
		ps.m[tile] = map[string]preparation{}
	}
	ps.m[tile][pr.tree] = pr
}

// keep drops every preparation of tile but tree's: the primary moved there
// (a queued deploy re-prepares its own at its swap).
func (ps *preparations) keep(tile, tree string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	for t := range ps.m[tile] {
		if t != tree {
			delete(ps.m[tile], t)
		}
	}
}

func (ps *preparations) drop(tile string) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	delete(ps.m, tile)
}

// prepare materializes tile's checkpoint tree and reads what it declares,
// for the primary, and keeps the answer for PinnedPrimary. A checkpoint that
// can't be read is an error; one whose xbin.json doesn't parse answers its
// code with ManifestErr set: it can't start, and no operation pins a primary
// to it (ops.go).
func (p *Plane) prepare(tile, tree string) (*registry.PinnedCode, error) {
	pc, err := p.readCode(tile, tree)
	if err != nil {
		p.prep.put(tile, preparation{tree: tree, err: err.Error()})
		slog.Warn("deployments: the primary's checkpoint can't be prepared; the tile fails closed", "tile", tile, "checkpoint", tree, "err", err)
		return nil, err
	}
	p.prep.put(tile, preparation{tree: tree, pc: pc})
	return pc, nil
}

// readCode materializes and reads a checkpoint without keeping the answer.
func (p *Plane) readCode(tile, tree string) (*registry.PinnedCode, error) {
	if !p.store().Exists(tile) {
		return nil, fmt.Errorf("%s: its checkpoint store is missing", tile)
	}
	root, err := p.store().Materialize(tile, tree)
	if err != nil {
		return nil, err
	}
	return registry.ReadCheckpoint(root)
}

// ---- live reload being detached ----

// overlay marks the tiles whose live reload an operation is detaching:
// between the request and its commit, saves drive nothing (07-runtime §8.6),
// so the checkpoint the operation takes is the code that ships. n makes the
// per-save question one atomic load while no operation is detaching (D119d).
type overlay struct {
	n     atomic.Int32
	mu    sync.Mutex
	tiles map[string]int
}

// on marks tile until the returned func runs.
func (o *overlay) on(tile string) func() {
	o.mu.Lock()
	if o.tiles == nil {
		o.tiles = map[string]int{}
	}
	o.tiles[tile]++
	o.n.Add(1)
	o.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			o.mu.Lock()
			if o.tiles[tile]--; o.tiles[tile] <= 0 {
				delete(o.tiles, tile)
			}
			o.n.Add(-1)
			o.mu.Unlock()
		})
	}
}

func (o *overlay) has(tile string) bool {
	if o.n.Load() == 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.tiles[tile] > 0
}

// ---- the runner's hooks (runner.DeploymentHooks) ----

// CodeFor answers what deployment dep of tile runs: its record's checkpoint,
// or the work tree while live reload drives it (D119e); util.ErrNoDeployment for
// a name the tile doesn't have; a *HeldError while its record holds it, so
// nothing starts. Without a record: the work tree for main.
func (p *Plane) CodeFor(tile, dep string) (runner.Code, error) {
	rec, err := p.record(tile)
	if err != nil {
		return runner.Code{}, err
	}
	if rec == nil {
		if dep != util.MainDeployment {
			return runner.Code{}, util.NoDeployment(tile, dep)
		}
		return runner.Code{WorkTree: true}, nil
	}
	d := rec.Deployments[dep]
	switch {
	case d == nil:
		return runner.Code{}, util.NoDeployment(tile, dep)
	case d.Checkpoint == nil:
		return runner.Code{WorkTree: true}, nil
	}
	return runner.Code{Tree: *d.Checkpoint}, nil
}

// Primary names tile's primary deployment: its record's, else main.
func (p *Plane) Primary(tile string) string {
	if rec, _ := p.record(tile); rec != nil {
		return rec.Primary
	}
	return util.MainDeployment
}

// View is the component a generation running code spawns from: c itself for
// the work tree of a primary that follows it (the registry's own pointer),
// the registry's view of the checkpoint's materialized tree otherwise. main
// is every tile's only deployment, and its primary, until deployments beyond
// main land, so the view is the primary's (registry.ViewCode's "").
func (p *Plane) View(c *registry.Component, code runner.Code) (*registry.Component, error) {
	if code.WorkTree {
		if p.Reg == nil {
			return c, nil
		}
		return p.Reg.View(c, registry.ViewCode{})
	}
	root, err := p.Materialize(c.Path, code.Tree)
	if err != nil {
		return nil, err
	}
	if p.Reg == nil {
		return nil, fmt.Errorf("%s: checkpoint %s has no view without the registry", c.Path, code.Tree)
	}
	return p.Reg.View(c, registry.ViewCode{Tree: code.Tree, Root: root})
}

// Materialize returns the read-only host tree of tile's checkpoint tree,
// preparing it when missing. Only a tile a record governs runs or serves a
// checkpoint: any other tile's answer is an error, and nothing falls back to
// the work tree.
func (p *Plane) Materialize(tile, tree string) (string, error) {
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return "", err
	case rec == nil:
		return "", fmt.Errorf("%s: checkpoint %s can't be materialized: the tile has no deployment record", tile, tree)
	case !fullTreeID(tree):
		return "", fmt.Errorf("%s: %q isn't a full checkpoint tree id", tile, tree)
	}
	return p.store().Materialize(tile, tree)
}

// EnvFor is deployment dep's spawn env and resource remap. The primary gets
// the tile's env with no remap: every resource path bound at itself, as
// today. Any other name gets no env and an empty remap, so nothing of the
// primary's data is bound.
func (p *Plane) EnvFor(c *registry.Component, dep string) ([]string, map[string]runner.ResBind) {
	if dep != p.Primary(c.Path) {
		return nil, map[string]runner.ResBind{}
	}
	if p.TileEnv == nil {
		return nil, nil
	}
	return p.TileEnv(c), nil
}

// ---- the registry's hooks ----

// PinnedPrimary answers for a tile whose primary is pinned to a checkpoint:
// what that checkpoint declares, which Rescan composes into the tile's
// component. It answers the zero manifest with the reason — no backend, no
// inbound surface (07-runtime §5.1) — for a tile its record holds, and for a
// pinned primary whose code isn't prepared or can't be: never the work
// tree's surface in its place (C7). No answer for a primary that follows the
// work tree, or a tile without a record.
func (p *Plane) PinnedPrimary(rel string) (*registry.PinnedCode, bool) {
	rec, err := p.record(rel)
	if err != nil {
		return &registry.PinnedCode{ManifestErr: err.Error()}, true
	}
	if rec == nil {
		return nil, false
	}
	d := rec.Deployments[rec.Primary]
	if d.Checkpoint == nil {
		return nil, false
	}
	if pr, ok := p.prep.get(rel, *d.Checkpoint); ok {
		if pr.pc != nil {
			return pr.pc, true
		}
		return &registry.PinnedCode{ManifestErr: fmt.Sprintf("%s: the primary (%s) is pinned to checkpoint %s, which can't be prepared: %s",
			rel, rec.Primary, *d.Checkpoint, pr.err)}, true
	}
	return &registry.PinnedCode{ManifestErr: fmt.Sprintf("%s: the primary (%s) is pinned to checkpoint %s, which isn't prepared",
		rel, rec.Primary, *d.Checkpoint)}, true
}

// ---- the broker's hooks (broker.DeploymentHooks) ----

// RewriteDeploymentOwner rewrites the owner ref of tile's record in the same
// step as a transfer (D119i), and must run before the owner store moves: until
// the store reports ownerRef, the record keeps answering to the former owner,
// so the tile never reads as inert in between. Only a record bound to the
// tile follows it. No record: nothing to rewrite.
func (p *Plane) RewriteDeploymentOwner(tile, ownerRef string) error {
	if p.idx == nil {
		return nil
	}
	return p.idx.rewriteOwner(tile, ownerRef)
}

// ResetDeploymentState drops path's record, whatever it holds, its journal
// and its view repository before a creation path assigns the new tile's
// owner (D119i), so the new tile starts in the zero state. The checkpoint
// store stays, a leftover. No record: nothing to reset.
func (p *Plane) ResetDeploymentState(path string) error {
	if p.idx == nil {
		return nil
	}
	if err := p.idx.remove(path, -1); err != nil {
		return err
	}
	return p.dropDerived(path)
}

// dropDerived removes what a record carries with it once the record is gone
// — its journal, its view repository, its prepared code — and forgets the
// tile's attempts. The checkpoint store and its deploy log stay. Its callers
// hold a booted index (a record was there).
func (p *Plane) dropDerived(tile string) error {
	p.prep.drop(tile)
	p.forget(tile)
	if err := os.Remove(filepath.Join(journalDir(p.Root, tile), journalFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: removing the deploy journal: %w", tile, err)
	}
	p.idx.prune(journalDir(p.Root, tile), recordDir(p.Root)) // each only when empty, never under a write
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := p.store().RemoveView(ctx, tile); err != nil {
		return fmt.Errorf("%s: removing the view repository: %w", tile, err)
	}
	return nil
}

// DeploymentLeftovers lists the deployment state still keyed by path that a
// new tile there would find: a record file, a checkpoint store. None: nil.
func (p *Plane) DeploymentLeftovers(path string) []string {
	if p.idx == nil {
		return nil
	}
	var out []string
	if p.idx.fileExists(path) {
		out = append(out, "deployment record")
	}
	if _, err := os.Lstat(storeDir(p.Root, path)); err == nil {
		out = append(out, "checkpoint store")
	}
	return out
}

// ---- the server's questions (server.Policy, through the broker) ----

// CodeRoot answers where deployment dep of c serves its files from ("" names
// the primary): its directory, unpinned, while it follows the work tree; its
// materialized checkpoint, pinned, otherwise; util.ErrNoDeployment for a name
// the tile doesn't have. A held record, or a checkpoint that can't be
// materialized, is an error: never a fallback to the work tree.
func (p *Plane) CodeRoot(c *registry.Component, dep string) (string, bool, error) {
	rec, err := p.record(c.Path)
	if err != nil {
		return "", false, err
	}
	if rec == nil {
		if dep != "" && dep != util.MainDeployment {
			return "", false, util.NoDeployment(c.Path, dep)
		}
		return c.Dir, false, nil
	}
	if dep == "" {
		dep = rec.Primary
	}
	d := rec.Deployments[dep]
	switch {
	case d == nil:
		return "", false, util.NoDeployment(c.Path, dep)
	case d.Checkpoint == nil:
		return c.Dir, false, nil
	}
	root, err := p.Materialize(c.Path, *d.Checkpoint)
	if err != nil {
		return "", false, err
	}
	return root, true, nil
}

// HasDeployment reports whether tile has a deployment called name: its
// record's; main alone without one, or while its record holds it.
func (p *Plane) HasDeployment(tile, name string) bool {
	if rec, _ := p.record(tile); rec != nil {
		return rec.Deployments[name] != nil
	}
	return name == util.MainDeployment
}

// Addressable lists the deployments of tile that pr may name: main, which
// the bare URL serves exactly as today.
func (p *Plane) Addressable(pr auth.Principal, tile string) []string {
	return []string{util.MainDeployment}
}

// ---- the terminal manager's hook ----

// HasRecord reports whether a deployment record governs tile, which gives
// its terminal and agent sessions the checkpoint fetch remote: a valid one,
// bound to the tile. One that holds the tile, or isn't its, doesn't count.
func (p *Plane) HasRecord(tile string) bool {
	rec, _ := p.record(tile)
	return rec != nil
}

// ---- the watcher's questions (boot's watchLoop) ----

// LiveReload answers which deployment a save in tile's work tree drives:
// (dep, true) while live reload is attached to dep, ("", false) while it is
// paused, while an operation is detaching it (the checkpoint that operation
// takes is what ships), and while the tile's record holds it (nothing may
// follow its work tree then). In-memory lookups, called on every save (D119d);
// without a record: main, attached.
func (p *Plane) LiveReload(tile string) (dep string, attached bool) {
	if p.pausing.has(tile) {
		return "", false
	}
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return "", false
	case rec == nil:
		return util.MainDeployment, true
	}
	return rec.LiveReload, rec.LiveReload != ""
}

// ---- small helpers ----

// pinnedSet is rec's pinned deployments, name → full tree id: what the view
// repository holds.
func pinnedSet(rec *Record) map[string]string {
	out := map[string]string{}
	for _, name := range sortedKeys(rec.Deployments) {
		if cp := rec.Deployments[name].Checkpoint; cp != nil {
			out[name] = *cp
		}
	}
	return out
}

// nested lists the registered components beneath tile, tile-relative: never
// in its checkpoints.
func (p *Plane) nested(tile string) []string {
	if p.Reg == nil {
		return nil
	}
	var out []string
	for _, c := range p.Reg.Components() {
		if rel, ok := cutPrefixDir(c.Path, tile); ok {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

// cutPrefixDir is path relative to dir when it lies strictly beneath it.
func cutPrefixDir(path, dir string) (string, bool) {
	if len(path) <= len(dir)+1 || path[:len(dir)] != dir || path[len(dir)] != '/' {
		return "", false
	}
	return path[len(dir)+1:], true
}
