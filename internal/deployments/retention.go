package deployments

// retention.go — the checkpoint store's retention, wired (07-runtime §2.8,
// D119e), and the restore put-back of a tile's deployment state (05-model §11,
// 08-data §11.4). The pieces are the store's and the runner's; this file
// decides when they run and what they keep:
//
//   - after each successful deploy of a tile (finish, queue.go): Store.GC
//     with what must stay besides the store's own retention — every
//     checkpoint the record points at, those of the attempts not yet in the
//     deploy log, the trees running generations bind — then
//     Runner.PruneArtifacts, keeping each deployment's current checkpoint
//     and the checkpoints of its previous three successful deploy-log
//     entries (NP-02-3), so a restart or a roll back to them never compiles;
//   - at boot, once a record governs a tile (Boot, plane.go): Store.Sweep,
//     the .tmp-* extractions killed runs left under .xbin/deploy;
//   - RestoreDeploymentState, the broker's restore hook: the store rebuilt
//     from an archive's objects only, the record installed only when the
//     tile has none, the view repository refreshed.
//
// A tile no record governs, or without a store, is never touched, so the
// zero state stays byte for byte (D119c). The store and the runner answer
// through optional interfaces, as the runner's DeploymentStatus does: a
// test fake without them collects nothing.
//
// The pass runs where the deploy finishes, after its deploy-log entry and
// before its waiters are released: in the lane's goroutine for a backend,
// which has swapped and announced its result by then, so the pass is off the
// path to the swap (07-runtime §13.2); inside the operation for a static
// tile, whose answer and reload wait for it. A caller that waits for the
// attempt finds the tile collected, and no pass outlives the daemon.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// collector is the checkpoint store's retention: *checkpoint.Store's GC and
// Sweep, through storeAdapter.
type collector interface {
	// GC collects tile's store and materialized trees, keeping what keep
	// names besides the store's retention (07-runtime §2.8).
	GC(ctx context.Context, tile string, keep func() []string) error
	// Sweep removes every tile's leftover .tmp-* extractions.
	Sweep() error
}

func (a storeAdapter) GC(ctx context.Context, tile string, keep func() []string) error {
	return a.s.GC(ctx, tile, keep)
}

func (a storeAdapter) Sweep() error { return a.s.Sweep() }

// artifactPruner is the runner's PruneArtifacts: *runner.Runner has it.
type artifactPruner interface {
	PruneArtifacts(tile string, keep []string) error
}

// storeRestorer rebuilds a tile's store from a restore's staged objects
// (08-data §11.4): created with xbind's own config when missing, the
// objects imported in confine, each ref of refs created where it is absent
// and none moved, and the checkpoints the archive held made the store's
// again where the store lacks them. It is the checkpoint store's Restore,
// through storeAdapter, once the store has one; until then a restore that
// carries a store puts nothing back.
type storeRestorer interface {
	Restore(ctx context.Context, tile, objects string, refs map[string]string) error
}

const (
	// artifactsKept is how many roll-back targets per deployment keep their
	// artifacts besides its current checkpoint (NP-02-3).
	artifactsKept = 3
	// retainTime bounds one retention pass: GC (its own run is bounded
	// tighter) and the deploy-log reads for the pruning.
	retainTime = 15 * time.Minute
)

// retainAfter runs a retention pass over tile once one of its deploys
// finished ok (07-runtime §2.8: after each successful deploy, for that
// tile). A failed or cancelled attempt moved no deployment's code: the next
// successful one collects.
func (p *Plane) retainAfter(tile, result string) {
	if result != resultOK {
		return
	}
	p.retain(tile)
}

// retain is one retention pass: GC of tile's store, then the pruning of its
// artifacts. A tile no record governs (it opted out meanwhile, or its record
// holds it) or without a store is left alone. A GC failure is logged and
// the pruning still runs: it depends only on the record and the deploy log,
// and a log that can't be read prunes nothing.
func (p *Plane) retain(tile string) {
	col, ok := p.store().(collector)
	if !ok {
		return
	}
	if rec, err := p.record(tile); err != nil || rec == nil || !p.store().Exists(tile) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), retainTime)
	defer cancel()
	if err := col.GC(ctx, tile, func() []string { return p.gcKeep(tile) }); err != nil {
		warn("collecting the checkpoint store", tile, err)
	}
	pr, ok := p.Run.(artifactPruner)
	if !ok {
		return
	}
	keep, err := p.artifactKeep(ctx, tile)
	if err != nil {
		warn("reading the deploy log; no artifact is pruned", tile, err)
		return
	}
	if err := pr.PruneArtifacts(tile, keep); err != nil {
		warn("pruning checkpoint artifacts", tile, err)
	}
}

// gcKeep is what GC keeps of tile besides the store's own retention: the
// trees running generations bind, as the runner holds them (GC ignores
// another tile's), and the checkpoints tile references. GC calls it before
// it takes the store lock.
func (p *Plane) gcKeep(tile string) []string {
	var keep []string
	if p.Run != nil {
		keep = append(keep, p.Run.RootsInUse()...)
	}
	return append(keep, p.referenced(tile)...)
}

// referenced lists the checkpoints tile's record points at, and those of its
// attempts not yet in the deploy log — queued, running, or finished while
// the log couldn't take them — whose code a swap may still put in place.
func (p *Plane) referenced(tile string) []string {
	var out []string
	if rec, _ := p.record(tile); rec != nil {
		for _, name := range sortedKeys(rec.Deployments) {
			if cp := rec.Deployments[name].Checkpoint; cp != nil {
				out = append(out, *cp)
			}
		}
	}
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[tile]; t != nil {
		for _, a := range t.open {
			if a.Tree != "" {
				out = append(out, a.Tree)
			}
		}
	}
	return out
}

// RetainedTrees answers the runner's Retained hook: tile's retained
// checkpoint trees (artifactKeep), which the env-layer GC keeps the layers
// of. A tile no record governs retains none; a record or deploy log that
// can't be read answers ok false, and the GC then keeps every layer.
func (p *Plane) RetainedTrees(tile string) ([]string, bool) {
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return nil, false
	case rec == nil:
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), retainTime)
	defer cancel()
	keep, err := p.artifactKeep(ctx, tile)
	if err != nil {
		warn("reading the deploy log; every env layer is kept", tile, err)
		return nil, false
	}
	return keep, true
}

// artifactKeep is what PruneArtifacts keeps of tile, sorted: every
// checkpoint tile references, and for each of its deployments the
// checkpoints of its newest successful deploy-log entries — the
// artifactsKept most recent distinct ones besides its current checkpoint,
// the roll-back targets (NP-02-3). A failed attempt is no roll-back target.
// Each deployment's log is read on its own, so a busy deployment never
// crowds out another's targets; a log that can't be read is an error.
func (p *Plane) artifactKeep(ctx context.Context, tile string) ([]string, error) {
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return nil, err
	case rec == nil:
		return nil, fmt.Errorf("%s has no deployment record", tile)
	}
	seen := map[string]bool{}
	var keep []string
	add := func(tree string) {
		if fullTreeID(tree) && !seen[tree] {
			seen[tree] = true
			keep = append(keep, tree)
		}
	}
	for _, tree := range p.referenced(tile) {
		add(tree)
	}
	for _, name := range sortedKeys(rec.Deployments) {
		entries, err := p.store().ReadLog(ctx, tile, name)
		if err != nil {
			return nil, err
		}
		mine := map[string]bool{}
		if cp := rec.Deployments[name].Checkpoint; cp != nil {
			mine[*cp] = true
		}
		targets := 0
		for _, e := range entries { // newest first
			if targets == artifactsKept {
				break
			}
			if e.Result != resultOK || e.Tree == "" || mine[e.Tree] {
				continue
			}
			mine[e.Tree] = true
			add(e.Tree)
			targets++
		}
	}
	sort.Strings(keep)
	return keep, nil
}

// sweepAtBoot removes the .tmp-* extractions killed runs left under
// .xbin/deploy (07-runtime §2.6), from Boot once a record governs a tile and
// before anything materializes. A workspace where no record governs a tile
// reads nothing more at boot (D119c); a store without a record has nothing
// materializing, and its next GC sweeps its own. A failure is logged.
func (p *Plane) sweepAtBoot() {
	col, ok := p.store().(collector)
	if !ok {
		return
	}
	if err := col.Sweep(); err != nil {
		slog.Warn("deployments: sweeping leftover materializations", "err", err)
	}
}

// ---- restore ----

// restoredRef is an archived ref as the broker's restore hands it: the
// store's own layout (07-runtime §2.1) under the restore-only namespace.
var restoredRef = regexp.MustCompile(`^refs/xbin/restored/(?:(?:checkpoints|views)/(?:[0-9a-f]{40}|[0-9a-f]{64})|log/([^/]+))$`)

// RestoreDeploymentState puts back the deployment state of tile's backup
// that the broker's restore validated, before the restore writes the tile's
// files: the broker's restore hook (08-data §11.4; 05-model §11; 06-security
// T11.5). record is the archived record (nil: none to install); objects is a
// bare repository the broker staged from the archive — its objects, and as
// loose refs the refs listed in refs, all under refs/xbin/restored/; no
// config, hooks, info or alternates ("" when no store is archived).
//
// Nothing is written until everything handed in checks: a record that isn't
// tile's (another tile's path) or doesn't validate as a load would, a ref
// outside the restore-only namespace, an id that isn't a full object id, or
// objects that aren't a directory refuse the whole restore. Then:
//  1. the store is rebuilt from objects only, in confine (the store's
//     Restore): refs created where absent, none moved;
//  2. the record is installed only when no record file is at the tile's
//     key — an existing one wins, and a restore never removes a deployment —
//     and only when the store holds every checkpoint it names, which a load
//     requires (a record naming a missing one would hold the tile). One made
//     for another owner is installed inert, as a load leaves it, until an
//     admin adopts or clears it (D119i);
//  3. for a record that now governs the tile, the pinned primary's code is
//     prepared, so the rescan after the restore composes the tile from it
//     (D119e), and the view repository is refreshed from the store.
//
// While opting in is closed (--tile-deployments=off) nothing is put back, as
// an xbind without the hook: a restore doesn't open what the switch closed,
// and the rest of the restore goes on.
func (p *Plane) RestoreDeploymentState(ctx context.Context, tile string, record []byte, objects string, refs map[string]string) error {
	var rec *Record
	if record != nil {
		r, err := ParseRecord(record, tile)
		if err != nil {
			return fmt.Errorf("%s: the archived deployment record can't be restored: %w", tile, err)
		}
		rec = r
	}
	if objects == "" {
		refs = nil
	} else if err := checkRestored(objects, refs); err != nil {
		return fmt.Errorf("%s: the archived checkpoint store can't be restored: %w", tile, err)
	}
	if rec == nil && objects == "" {
		return nil
	}
	if p.idx == nil {
		return errors.New("deployments: the plane isn't booted")
	}
	if p.OptInClosed {
		slog.Warn("restore: tile deployments are switched off; the tile's deployment state is left out", "tile", tile)
		return nil
	}
	unlock := p.locks.lock(tile)
	defer unlock()
	if objects != "" {
		sr, ok := p.store().(storeRestorer)
		if !ok {
			slog.Warn("restore: this xbind can't rebuild a checkpoint store; the tile's deployment state is left out", "tile", tile)
			return nil
		}
		if err := sr.Restore(ctx, tile, objects, refs); err != nil {
			return fmt.Errorf("%s: rebuilding the checkpoint store: %w", tile, err)
		}
	}
	if rec == nil {
		return nil
	}
	if missing := p.missingFromStore(ctx, tile, rec); missing != "" {
		slog.Warn("restore: the archived deployment record isn't installed: "+missing, "tile", tile)
		return nil
	}
	installed, err := p.installRecord(tile, rec)
	if err != nil || !installed {
		return err
	}
	f := p.idx.get(tile)
	if f.State != RecordActive {
		slog.Warn("restore: the archived deployment record is installed and doesn't apply", "tile", tile, "why", f.Err)
		return nil
	}
	if d := f.Record.Deployments[f.Record.Primary]; d.Checkpoint != nil {
		_, _ = p.prepare(tile, *d.Checkpoint) // a failure is the tile's alone, answered by PinnedPrimary
	}
	if p.store().Exists(tile) {
		p.syncView(ctx, tile, f.Record)
	}
	return nil
}

// checkRestored checks a restore's staged repository and refs before
// anything is written: objects is a directory (not a symlink), and each ref
// is one of the store's under refs/xbin/restored/ naming a full object id.
func checkRestored(objects string, refs map[string]string) error {
	if fi, err := os.Lstat(objects); err != nil || !fi.IsDir() {
		return fmt.Errorf("the staged objects %s aren't a directory", objects)
	}
	for _, ref := range sortedKeys(refs) {
		m := restoredRef.FindStringSubmatch(ref)
		switch {
		case m == nil, m[1] != "" && !util.DeploymentNameOK(m[1]):
			return fmt.Errorf("%q isn't a restored checkpoint ref", ref)
		case !fullTreeID(refs[ref]):
			return fmt.Errorf("%s names %q, not an object id", ref, refs[ref])
		}
	}
	return nil
}

// missingFromStore names a checkpoint rec points at that tile's store
// doesn't hold, as a load would judge it; "" when it holds them all.
func (p *Plane) missingFromStore(ctx context.Context, tile string, rec *Record) string {
	for _, name := range sortedKeys(rec.Deployments) {
		cp := rec.Deployments[name].Checkpoint
		if cp == nil {
			continue
		}
		if !p.store().Exists(tile) {
			return fmt.Sprintf("%s's checkpoint %s has no store to be in", name, shortTree(*cp))
		}
		if _, err := p.store().Get(ctx, tile, *cp); err != nil {
			return fmt.Sprintf("%s's checkpoint %s isn't in the store: %v", name, shortTree(*cp), err)
		}
	}
	return ""
}

// installRecord writes rec at tile's key when no record file is there, and
// files it in the index as a load would: governing the tile when its owner
// ref is the tile's current one, inert otherwise. It reports whether it
// wrote one.
func (p *Plane) installRecord(tile string, rec *Record) (bool, error) {
	x := p.idx
	unlock := x.lock(tile)
	defer unlock()
	if x.fileExists(tile) {
		slog.Info("restore: the tile has a deployment record; the archived one isn't installed", "tile", tile)
		return false, nil
	}
	data, err := rec.encode()
	if err != nil {
		return false, fmt.Errorf("%s: encoding the restored deployment record: %w", tile, err)
	}
	if err := x.writeIn(recordPath(x.root, tile), data); err != nil {
		return false, fmt.Errorf("%s: writing the restored deployment record: %w", tile, err)
	}
	x.mu.Lock()
	x.admit(util.TileKey(tile), data)
	x.mu.Unlock()
	return true, nil
}
