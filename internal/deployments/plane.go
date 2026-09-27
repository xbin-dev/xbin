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
// A tile without a deployment record is in the zero state (P5): every method
// answers exactly as xbind did before tile deployments, with no file read or
// written; its one deployment is main, the primary, following the work tree.
// A tile with one answers from it (record.go), through the in-memory index
// Boot fills (index.go); a record that can't be used holds its tile, which
// then fails closed (06-security C7): no backend, no inbound surface, never
// its work tree in place of a pinned checkpoint.
package deployments

import (
	"context"
	"fmt"
	"os"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/util"
)

// Runner is what the plane asks of the runner: *runner.Runner in xbind, a
// fake in tests. Ensure, Track, Changed and Stop keep meaning the primary;
// the plane names the deployment it acts on (P7).
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

// Plane is the deployments plane. Boot fills the fields step by step, before
// the daemon serves; nothing changes them afterwards.
type Plane struct {
	Root string             // the workspace root: data/deployments, data/checkpoints, .xbin/deploy
	Reg  *registry.Registry // the tiles; Rescan after a commit that changes the primary's code
	Hub  *events.Hub        // the deployments event (rule C2)
	Run  Runner             // generations: deploy, restart, stop

	// OwnerRef is a tile's current owner ref ("" = workspace-owned), which a
	// record must match to apply to the tile (P29).
	OwnerRef func(tile string) string
	// IsAdmin answers the broker's admin question (an element principal whose
	// tile holds xbin admin passes it; the manager gate never relies on it).
	IsAdmin func(auth.Principal) bool
	// MayManage is the manager gate: a person in their own session who is a
	// workspace admin or manages the tile. No element principal passes.
	MayManage func(p auth.Principal, tile string) bool
	// TileEnv is the resource and identity env the broker derives for a
	// tile's backend: what the primary's generation gets, today's env.
	TileEnv func(c *registry.Component) []string
	// Provision provisions the resources the registry's scopes declare.
	Provision func()
	// ReconcileIngress re-derives the ingress listeners and doors from the
	// registry and the bindings.
	ReconcileIngress func()

	// OptInClosed is the ship-dark switch turned off (--tile-deployments=off):
	// operations that create or extend deployment state are refused, those
	// that return a tile to the zero state stay allowed, and existing records
	// keep governing what runs. Only the authorize function reads it; the
	// zero-state answers below never do.
	OptInClosed bool

	// idx holds the records, from Boot on; nil (a literal that never booted)
	// answers the zero state for every tile.
	idx *index
}

// Boot runs once, from boot's registry step, after the registry hooks are
// installed and before the first Provision and any backend start. It loads
// the deployment records into memory (PO-8: it reads them and never
// rewrites one). A record that can't be used holds its own tile, which fails
// closed; an error — data/deployments can't be read, so no tile's record
// can be judged — stops the daemon. Without data/deployments it reads
// nothing more and writes nothing; a literal without a Root reads nothing.
func (p *Plane) Boot() error {
	if p.Root == "" {
		return nil
	}
	idx := newIndex(p.Root, p.OwnerRef)
	if err := idx.load(); err != nil {
		return err
	}
	p.idx = idx
	return nil
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

// ---- the runner's hooks (runner.DeploymentHooks) ----

// CodeFor answers what deployment dep of tile runs: its record's checkpoint,
// or the work tree while live reload drives it (P9); util.ErrNoDeployment for
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
// the work tree, the registry's own pointer. No checkpoint has a view yet, so
// nothing starts from one.
func (p *Plane) View(c *registry.Component, code runner.Code) (*registry.Component, error) {
	if !code.WorkTree {
		return nil, fmt.Errorf("%s: checkpoint %s has no view: this xbind can't run checkpoints yet", c.Path, code.Tree)
	}
	return c, nil
}

// Materialize returns the read-only host tree of tile's checkpoint tree. No
// checkpoint can be materialized yet, so nothing serves or runs one, and
// nothing falls back to the work tree.
func (p *Plane) Materialize(tile, tree string) (string, error) {
	return "", fmt.Errorf("%s: checkpoint %s can't be materialized: this xbind can't run checkpoints yet", tile, tree)
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
// pinned primary whose code isn't prepared: never the work tree's surface in
// its place (C7). No answer for a primary that follows the work tree, or a
// tile without a record.
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
	return &registry.PinnedCode{ManifestErr: fmt.Sprintf("%s: the primary (%s) is pinned to checkpoint %s, which isn't prepared",
		rel, rec.Primary, *d.Checkpoint)}, true
}

// ScopeResources answers the resources a scope root declares when they don't
// come from the work tree's scope.json (a pinned primary's checkpoint, P22).
// Every scope declares what its work tree does.
func (p *Plane) ScopeResources(scope string) (map[string]registry.Resource, bool) {
	return nil, false
}

// ---- the broker's hooks (broker.DeploymentHooks) ----

// RewriteDeploymentOwner rewrites the owner ref of tile's record in the same
// step as a transfer (P29), and must run before the owner store moves: until
// the store reports ownerRef, the record keeps answering to the former owner,
// so the tile never reads as inert in between. Only a record bound to the
// tile follows it. No record: nothing to rewrite.
func (p *Plane) RewriteDeploymentOwner(tile, ownerRef string) error {
	if p.idx == nil {
		return nil
	}
	return p.idx.rewriteOwner(tile, ownerRef)
}

// ResetDeploymentState drops path's record, whatever it holds, and its view
// repository before a creation path assigns the new tile's owner (P29), so
// the new tile starts in the zero state. The checkpoint store stays, a
// leftover. No record: nothing to reset.
func (p *Plane) ResetDeploymentState(path string) error {
	if p.idx == nil {
		return nil
	}
	if err := p.idx.remove(path, -1); err != nil {
		return err
	}
	if err := os.RemoveAll(viewDir(p.Root, path)); err != nil {
		return fmt.Errorf("%s: removing the view repository: %w", path, err)
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
// paused, and while the tile's record holds it (nothing may follow its work
// tree then). An in-memory lookup, called on every save (P8); without a
// record: main, attached.
func (p *Plane) LiveReload(tile string) (dep string, attached bool) {
	rec, err := p.record(tile)
	switch {
	case err != nil:
		return "", false
	case rec == nil:
		return util.MainDeployment, true
	}
	return rec.LiveReload, rec.LiveReload != ""
}

// WorkTreeMoved is the watcher's notice that a batch touched tile while its
// live reload is paused: the plane recounts how far the work tree moved from
// the pinned checkpoint, debounced per tile, and announces a moved count. It
// is never called for a tile whose live reload is attached.
func (p *Plane) WorkTreeMoved(tile string) {}
