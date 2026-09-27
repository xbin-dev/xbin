package deployments

// ops_purge.go — purging a checkpoint (05-model §2, §10; 06-security T20,
// NP-06-16; 16-open-questions Q11): a tile manager, in a person's own
// session, removes a checkpoint no deployment runs from every deploy log of
// the tile, and its objects are pruned at once — the store's Purge, one
// confined run under the store lock (06-security L7). The view repository is
// rebuilt, the tile's attempts in memory stop naming the checkpoint, the
// build products only it kept go, and the purge is logged: the audit line
// every POST under /deployments gets names who and the route; this file's
// line names the checkpoint. Archives made earlier are out of reach.
//
// Authority is the dispatcher's, from the act's row in the authority table
// (authz.go): the manager gate, never passed by a terminal or agent token or
// an element principal. The act is on the tile as a whole; it is accepted on
// a tile without a record, since the store and its deploy log outlive an
// opt-out (05-model §2), and the ship-dark switch never closes it: it only
// removes. The request names the checkpoint, and is refused (409) while any
// deployment runs it — the record points at it, a deploy of it is queued,
// running or not yet in the deploy log, a swap put it on a deployment, or a
// running generation binds its tree (purgeKeep, the store's own check). The
// operation holds the tile's lock throughout, so no operation can deploy it
// meanwhile. A dry run is judged and refused exactly as for real, and
// changes nothing.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/xbin-dev/xbin/internal/checkpoint"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	register(OpPurge, Handler[PurgeRequest]{Subject: purgeSubject, Run: runPurge})
}

// PurgeRequest is POST /deployments/purge's body.
type PurgeRequest struct {
	Tile       string `json:"tile"`
	Checkpoint string `json:"checkpoint"` // c:<id>: the checkpoint to purge
	Seq        *int64 `json:"seq,omitempty"`
	DryRun     bool   `json:"dryRun,omitempty"`
}

// PurgeAnswer is a committed purge's answer; the handler adds the state.
type PurgeAnswer struct {
	// Purged is the checkpoint's id as it read before the purge.
	Purged string `json:"purged"`
	// Entries counts the deploy-log entries that deployed it, which now name
	// no checkpoint.
	Entries int `json:"entries"`
}

// purger is the checkpoint store's Purge: *checkpoint.Store's, through
// storeAdapter; a test fake without it answers 501.
type purger interface {
	Purge(ctx context.Context, tile, tree string, keep func() []string) (checkpoint.PurgeResult, error)
}

func (a storeAdapter) Purge(ctx context.Context, tile, tree string, keep func() []string) (checkpoint.PurgeResult, error) {
	return a.s.Purge(ctx, tile, tree, keep)
}

// purgeSubject is the tile as a whole: a qualified ref names its tile. The
// checkpoint must be an id (400); whether the tile has it is Run's to
// answer, after authority, so a caller who may not purge learns nothing of
// its checkpoints.
func purgeSubject(p *Plane, r *PurgeRequest) (Op, Subject, error) {
	tile, _, err := p.resolveTile(r.Tile)
	if err != nil {
		return "", Subject{}, err
	}
	if r.Checkpoint == "" {
		return "", Subject{}, badRequest("bad request body: checkpoint is required: the c:<id> to purge")
	}
	if _, err := checkpoint.ParseID(r.Checkpoint); err != nil {
		return "", Subject{}, badRequest(err.Error())
	}
	return "", p.subjectOf(tile, ""), nil
}

// runPurge purges the checkpoint r names from g's tile.
func runPurge(ctx context.Context, p *Plane, g Grant, r *PurgeRequest) (any, error) {
	o, err := p.begin(g, r.DryRun)
	if err != nil {
		return nil, err
	}
	defer o.done()
	if err := o.checkSeq(r.Seq); err != nil {
		return nil, err
	}
	cp, err := p.store().Resolve(ctx, o.tile, r.Checkpoint)
	if err != nil {
		return nil, opError(o.tile, err)
	}
	if e := p.purgeRefusal(o, cp); e != nil {
		return nil, e
	}
	if r.DryRun {
		return p.answer(ctx, true, nil, Impact{}, false)
	}
	pg, ok := p.store().(purger)
	if !ok {
		return nil, &Error{Status: http.StatusNotImplemented, Msg: "purging a checkpoint isn't available in this build of xbind"}
	}
	if err := p.Recheck(g, p.subjectOf(o.tile, "")); err != nil { // authority at the act itself (T9)
		return nil, err
	}
	res, err := pg.Purge(ctx, o.tile, cp.Hash, func() []string { return p.purgeKeep(o.tile) })
	switch {
	case errors.Is(err, checkpoint.ErrInUse):
		return nil, inUse(o.tile, cp.ID, "a running generation or a deploy still uses it")
	case err != nil && res.Checkpoint.Hash == "":
		return nil, opError(o.tile, err)
	case err != nil: // purged; what is left is derived
		warn("purging a checkpoint", o.tile, err)
	}
	p.forgetPurged(o.tile, cp.Hash)
	if rec := p.current(o.tile); o.state == RecordActive && rec != nil && rec.Seq > 0 {
		p.syncView(ctx, o.tile, rec)
		p.pruneAfterPurge(ctx, o.tile)
	} else if err := p.store().RemoveView(ctx, o.tile); err != nil && !isNotBuilt(err) {
		warn("removing a view repository no record keeps", o.tile, err) // it exists only with a record
	}
	slog.Info("deployments: checkpoint purged", "tile", o.tile, "checkpoint", cp.Hash, "by", o.by, "via", g.P.Via,
		"entries", res.Entries, "logs", strings.Join(res.Logs, ","))
	return PurgeAnswer{Purged: cp.ID, Entries: res.Entries}, nil
}

// purgeRefusal refuses purging cp from o's tile while any deployment runs
// it: the record points at it; a deploy of it is queued, running or not yet
// in the deploy log; a swap put it on a deployment; a running generation
// binds its tree. nil when nothing uses it.
func (p *Plane) purgeRefusal(o *op, cp checkpoint.Checkpoint) *Error {
	tree := cp.Hash
	for _, name := range sortedKeys(o.rec.Deployments) {
		if c := o.rec.Deployments[name].Checkpoint; c != nil && *c == tree {
			return inUse(o.tile, cp.ID, name+" runs it")
		}
	}
	why := ""
	p.q.mu.Lock()
	if t := p.q.tiles[o.tile]; t != nil {
		for _, a := range t.open {
			switch {
			case a.Tree != tree:
			case a.finished():
				why = fmt.Sprintf("deploy %d of it to %s isn't in the deploy log yet", a.ID, a.Deployment)
			default:
				why = fmt.Sprintf("deploy %d of it to %s is %s", a.ID, a.Deployment, a.Result)
			}
			if why != "" {
				break
			}
		}
		for _, dep := range sortedKeys(t.serving) {
			if why == "" && t.serving[dep] == tree {
				why = dep + " runs it"
			}
		}
	}
	p.q.mu.Unlock()
	if why != "" {
		return inUse(o.tile, cp.ID, why)
	}
	if p.Run != nil {
		root := filepath.Join(treesOf(p.Root, o.tile), tree)
		for _, r := range p.Run.RootsInUse() {
			if filepath.Clean(r) == root {
				return inUse(o.tile, cp.ID, "a running generation of "+o.tile+" still runs it")
			}
		}
	}
	return nil
}

func inUse(tile, id, why string) *Error {
	return &Error{Status: http.StatusConflict, Kind: KindState,
		Msg: fmt.Sprintf("%s: checkpoint %s is in use — %s; only a checkpoint no deployment runs can be purged", tile, id, why)}
}

// purgeKeep is what the store's purge must never remove, which it checks
// again before it takes the store lock: what GC keeps for tile (the trees
// running generations bind, the checkpoints the record and the unlogged
// attempts name) and what the last swaps put on its deployments.
func (p *Plane) purgeKeep(tile string) []string {
	keep := p.gcKeep(tile)
	p.q.mu.Lock()
	defer p.q.mu.Unlock()
	if t := p.q.tiles[tile]; t != nil {
		for _, dep := range sortedKeys(t.serving) {
			if tree := t.serving[dep]; tree != "" {
				keep = append(keep, tree)
			}
		}
	}
	return keep
}

// forgetPurged makes tile's finished attempts in memory name no checkpoint
// where they named tree, as the deploy log's entries now do — each a copy,
// since a reader may hold the attempt — and drops tree's prepared code.
func (p *Plane) forgetPurged(tile, tree string) {
	p.q.mu.Lock()
	if t := p.q.tiles[tile]; t != nil {
		for i, a := range t.recent {
			if a.Tree == tree {
				c := *a
				c.Tree, c.Feed = "", ""
				t.recent[i] = &c
			}
		}
	}
	p.q.mu.Unlock()
	p.prep.mu.Lock()
	delete(p.prep.m[tile], tree)
	p.prep.mu.Unlock()
}

// pruneAfterPurge prunes tile's checkpoint artifacts as a retention pass
// does (retention.go): the purged checkpoint is no longer referenced or a
// roll-back target, so its build products go with it.
func (p *Plane) pruneAfterPurge(ctx context.Context, tile string) {
	pr, ok := p.Run.(artifactPruner)
	if !ok {
		return
	}
	keep, err := p.artifactKeep(ctx, tile)
	if err != nil {
		warn("reading the deploy log after a purge; no artifact is pruned", tile, err)
		return
	}
	if err := pr.PruneArtifacts(tile, keep); err != nil {
		warn("pruning checkpoint artifacts after a purge", tile, err)
	}
}

// treesOf is tile's materialized checkpoints, .xbin/deploy/<TileKey>, as
// the store places them (checkpoint.Store.TreesDir).
func treesOf(root, tile string) string {
	return filepath.Join(root, ".xbin", "deploy", util.TileKey(tile))
}
