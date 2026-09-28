package deployments

// reads.go — what the deployments API reads from the plane's checkpoint
// store beyond the state (11-contract §1.11, §1.12): the diff for review and
// the checkpoint remote. The handler judges the caller first (boot's
// deployreads.go); these answer only for a tile an active record governs,
// so an inert store (an opt-out keeps it) is never read, and none is
// created (P5).

import (
	"context"
	"fmt"
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/checkpoint"
)

// DiffSpec is one judged side of a diff: the work tree (captured), a
// checkpoint id the client sent (c:<id>), or a deployment's full tree id.
type DiffSpec struct {
	WorkTree bool
	ID       string
	Tree     string
}

// Diff answers the diff of tile from one side to the other, confined and
// bounded (checkpoint.Store.Diff). A work-tree side is a capture: it spends
// the tile's capture rate and records pr as its author. A tile no active
// record governs has nothing to diff (checkpoint.ErrNothingToDiff); a held
// one answers its hold.
func (p *Plane) Diff(ctx context.Context, pr auth.Principal, tile string, from, to DiffSpec, path string, stat bool) (checkpoint.DiffResult, error) {
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return checkpoint.DiffResult{}, err
	case rec == nil:
		return checkpoint.DiffResult{}, fmt.Errorf("%s: %w", tile, checkpoint.ErrNothingToDiff)
	}
	c, ok := p.component(tile)
	if !ok {
		return checkpoint.DiffResult{}, fmt.Errorf("%s: %w", tile, checkpoint.ErrNothingToDiff)
	}
	req := checkpoint.DiffRequest{Source: p.source(c), By: actor(pr), Path: path, Stat: stat}
	for _, s := range []struct {
		spec DiffSpec
		side *checkpoint.DiffSide
	}{{from, &req.From}, {to, &req.To}} {
		switch {
		case s.spec.WorkTree:
			s.side.WorkTree = true
		case s.spec.Tree != "":
			s.side.Tree = s.spec.Tree
		default:
			cp, err := p.store().Resolve(ctx, tile, s.spec.ID)
			if err != nil {
				return checkpoint.DiffResult{}, err
			}
			s.side.Tree = cp.Hash
		}
	}
	return p.store().Diff(ctx, req)
}

// ServeFetch serves one file of tile's view repository, the checkpoint
// remote's dumb HTTP (checkpoint.Store.ServeFetch). A path off the
// allow-list, and a tile no active record governs, answer
// checkpoint.ErrNotFetchable before anything is written: the handler's 404.
func (p *Plane) ServeFetch(w http.ResponseWriter, r *http.Request, tile, rel string) error {
	if rec, _ := p.record(tile); rec == nil {
		return checkpoint.ErrNotFetchable
	}
	return p.store().ServeFetch(w, r, tile, rel)
}
