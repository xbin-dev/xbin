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
package deployments

import (
	"context"
	"fmt"

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
}

// Boot runs once, from boot's registry step, after the registry hooks are
// installed and before the first Provision and any backend start. It loads
// the deployment records into memory, reconciles the journal of an
// operation a crash interrupted into the deploy log, prepares each pinned
// primary's code (materializing a missing tree) and re-runs Rescan when a
// primary is pinned, so the primary's code is what the registry composes,
// provisions and serves from the first request (P9). A tile whose
// preparation fails composes with no inbound surface and no backend; an
// error stops the daemon. Boot never rewrites a record. With no record it
// does nothing.
func (p *Plane) Boot() error { return nil }

// ---- the runner's hooks (runner.DeploymentHooks) ----

// CodeFor answers what deployment dep of tile runs: the work tree for main,
// util.ErrNoDeployment for any other name.
func (p *Plane) CodeFor(tile, dep string) (runner.Code, error) {
	if dep != util.MainDeployment {
		return runner.Code{}, util.NoDeployment(tile, dep)
	}
	return runner.Code{WorkTree: true}, nil
}

// Primary names tile's primary deployment: main.
func (p *Plane) Primary(tile string) string { return util.MainDeployment }

// View is the component a generation running code spawns from: c itself for
// the work tree, the registry's own pointer. No checkpoint has a view.
func (p *Plane) View(c *registry.Component, code runner.Code) (*registry.Component, error) {
	if !code.WorkTree {
		return nil, fmt.Errorf("%s: checkpoint %s has no view: the tile has no deployment record", c.Path, code.Tree)
	}
	return c, nil
}

// Materialize returns the read-only host tree of tile's checkpoint tree.
// Without a record there is no checkpoint to materialize.
func (p *Plane) Materialize(tile, tree string) (string, error) {
	return "", fmt.Errorf("%s: no checkpoint %s to materialize: the tile has no deployment record", tile, tree)
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
// component. No primary is pinned.
func (p *Plane) PinnedPrimary(rel string) (*registry.PinnedCode, bool) { return nil, false }

// ScopeResources answers the resources a scope root declares when they don't
// come from the work tree's scope.json (a pinned primary's checkpoint, P22).
// Every scope declares what its work tree does.
func (p *Plane) ScopeResources(scope string) (map[string]registry.Resource, bool) {
	return nil, false
}

// ---- the broker's hooks (broker.DeploymentHooks) ----

// RewriteDeploymentOwner rewrites the owner ref of tile's record in the same
// step as a transfer (P29). No record: nothing to rewrite.
func (p *Plane) RewriteDeploymentOwner(tile, ownerRef string) error { return nil }

// ResetDeploymentState drops path's record and view repository before a
// creation path assigns the new tile's owner (P29). No record: nothing to
// reset.
func (p *Plane) ResetDeploymentState(path string) error { return nil }

// DeploymentLeftovers lists the deployment state still keyed by path that a
// new tile there would find. None.
func (p *Plane) DeploymentLeftovers(path string) []string { return nil }

// ---- the server's questions (server.Policy, through the broker) ----

// CodeRoot answers where deployment dep of c serves its files from ("" names
// the primary): its directory, unpinned, for the primary; util.ErrNoDeployment
// for any other name, never a fallback to the work tree.
func (p *Plane) CodeRoot(c *registry.Component, dep string) (string, bool, error) {
	if dep != "" && dep != p.Primary(c.Path) {
		return "", false, util.NoDeployment(c.Path, dep)
	}
	return c.Dir, false, nil
}

// HasDeployment reports whether tile has a deployment called name: main only.
func (p *Plane) HasDeployment(tile, name string) bool { return name == util.MainDeployment }

// Addressable lists the deployments of tile that pr may name: main, which
// the bare URL serves exactly as today.
func (p *Plane) Addressable(pr auth.Principal, tile string) []string {
	return []string{util.MainDeployment}
}

// ---- the terminal manager's hook ----

// HasRecord reports whether tile has a deployment record, which gives its
// terminal and agent sessions the checkpoint fetch remote. No tile has one.
func (p *Plane) HasRecord(tile string) bool { return false }

// ---- the watcher's questions (boot's watchLoop) ----

// LiveReload answers which deployment a save in tile's work tree drives:
// (dep, true) while live reload is attached to dep, ("", false) while it is
// paused. An in-memory lookup, called on every save (P8): main, attached.
func (p *Plane) LiveReload(tile string) (dep string, attached bool) {
	return util.MainDeployment, true
}

// WorkTreeMoved is the watcher's notice that a batch touched tile while its
// live reload is paused: the plane recounts how far the work tree moved from
// the pinned checkpoint, debounced per tile, and announces a moved count. It
// is never called for a tile whose live reload is attached.
func (p *Plane) WorkTreeMoved(tile string) {}
