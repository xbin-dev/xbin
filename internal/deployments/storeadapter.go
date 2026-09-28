package deployments

// storeadapter.go — the plane's view of *checkpoint.Store (the checkpoints
// interface, plane.go): the store's own entry points, and the deploy log
// converted between the plane's attempts and the store's entries.

import (
	"context"
	"net/http"
	"time"

	"github.com/xbin-dev/xbin/internal/checkpoint"
)

// errNotBuilt marks a store call this build of xbind doesn't have: a
// finished attempt stays in the journal until its log entry is written.
type errNotBuilt string

func (e errNotBuilt) Error() string { return string(e) + " isn't available in this build of xbind" }

// storeAdapter is the plane's view of *checkpoint.Store.
type storeAdapter struct{ s *checkpoint.Store }

func (a storeAdapter) Exists(tile string) bool { return a.s.Exists(tile) }
func (a storeAdapter) Caps() checkpoint.Caps   { return a.s.Caps }

func (a storeAdapter) Estimate(ctx context.Context, src checkpoint.Source) (checkpoint.Estimate, error) {
	return a.s.Estimate(ctx, src)
}

func (a storeAdapter) Capture(ctx context.Context, req checkpoint.CaptureRequest) (checkpoint.Result, error) {
	return a.s.Capture(ctx, req)
}

func (a storeAdapter) Resolve(ctx context.Context, tile, id string) (checkpoint.Checkpoint, error) {
	return a.s.Resolve(ctx, tile, id)
}

func (a storeAdapter) Get(ctx context.Context, tile, tree string) (checkpoint.Checkpoint, error) {
	return a.s.Get(ctx, tile, tree)
}

func (a storeAdapter) Materialize(tile, tree string) (string, error) {
	return a.s.Materialize(tile, tree)
}

// AppendLog writes a finished attempt as its deployment's deploy-log entry
// (11-contract §10.3). by is user:<id> or owner, as 11-contract §0.6 says.
func (a storeAdapter) AppendLog(ctx context.Context, tile string, e attempt) error {
	le := checkpoint.LogEntry{ID: e.ID, Deployment: e.Deployment, How: e.How, From: e.From,
		Checkpoint: e.Tree, Previous: e.Previous, Feed: e.Feed, FollowsWorkTree: e.FollowsWorkTree,
		By: e.By, Via: e.Via, Agent: e.Agent, Session: e.Session, Result: e.Result, Error: e.Error}
	le.RequestedAt, _ = time.Parse(time.RFC3339, e.RequestedAt)
	le.FinishedAt, _ = time.Parse(time.RFC3339, e.FinishedAt)
	if le.Checkpoint == "" {
		le.Feed = "" // no checkpoint was made: the entry names no feed
	}
	return a.s.AppendLog(ctx, tile, le)
}

// ReadLog is tile's deploy log (every deployment's with dep "", or one's),
// newest first, as attempts: at most the store's one-read limit, which is
// more than GC keeps per deployment.
func (a storeAdapter) ReadLog(ctx context.Context, tile, dep string) ([]attempt, error) {
	les, _, err := a.s.Log(ctx, tile, checkpoint.LogQuery{Deployment: dep, Limit: checkpoint.MaxLogLimit})
	if err != nil {
		return nil, err
	}
	out := make([]attempt, 0, len(les))
	for _, le := range les {
		at := attempt{ID: le.ID, Deployment: le.Deployment, How: le.How, From: le.From,
			Tree: le.Checkpoint, Previous: le.Previous, Feed: le.Feed, FollowsWorkTree: le.FollowsWorkTree,
			By: le.By, Via: le.Via, Agent: le.Agent, Session: le.Session, Result: le.Result, Error: le.Error}
		if !le.RequestedAt.IsZero() {
			at.RequestedAt = le.RequestedAt.UTC().Format(time.RFC3339)
		}
		if !le.FinishedAt.IsZero() {
			at.FinishedAt = le.FinishedAt.UTC().Format(time.RFC3339)
		}
		out = append(out, at)
	}
	return out, nil
}

func (a storeAdapter) SyncView(ctx context.Context, tile string, pinned map[string]string, primary string) error {
	return a.s.RefreshView(ctx, tile, checkpoint.Pins{Primary: primary, Trees: pinned})
}

func (a storeAdapter) RemoveView(ctx context.Context, tile string) error {
	return a.s.RemoveView(ctx, tile)
}

func (a storeAdapter) Drift(ctx context.Context, src checkpoint.Source, tree string) (int, error) {
	return a.s.Drift(ctx, src, tree)
}

func (a storeAdapter) Diff(ctx context.Context, req checkpoint.DiffRequest) (checkpoint.DiffResult, error) {
	return a.s.Diff(ctx, req)
}

func (a storeAdapter) ServeFetch(w http.ResponseWriter, r *http.Request, tile, rel string) error {
	return a.s.ServeFetch(w, r, tile, rel)
}

// Checkpoint answers the facts of tile's checkpoint with full tree id tree
// (its short id, when it was made and by whom) for the state's reads. It
// creates no store: a tile without one answers the store's error.
func (p *Plane) Checkpoint(ctx context.Context, tile, tree string) (checkpoint.Checkpoint, error) {
	return p.store().Get(ctx, tile, tree)
}
