package runner

// deploy.go — tile deployments in the runner: the hooks boot installs, what a
// generation runs, and the per-deployment names beside Ensure, Track, Changed
// and Stop; the deploy worker is deployworker.go's. Every hook is nil-safe:
// nil answers as a tile without a deployment record, whose one deployment is
// main, the primary, following the work tree. Ensure, Track, Changed and Stop
// keep meaning the primary, so every existing caller keeps today's behaviour
// (P7).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// Code is what one generation of a deployment runs.
type Code struct {
	WorkTree bool   // it follows the work tree (the live reload target)
	Tree     string // the pinned checkpoint's full tree hash; "" iff WorkTree
	// Identical is the deployments plane's word, on a Deploy, that the
	// checkpoint holds exactly the files the deployment served until now: a
	// capture of the work tree it followed (pausing live reload, attaching it
	// elsewhere, a deploy of a fresh capture onto the live reload target), or
	// the checkpoint it is already pinned to. Its swap then announces no
	// reload (07-runtime §8.5). It describes a move, not what runs: CodeFor
	// never sets it, and no generation keeps it.
	Identical bool
}

// runs is the code a generation runs, without Identical.
func (c Code) runs() Code { return Code{WorkTree: c.WorkTree, Tree: c.Tree} }

// ResBind is one entry of a deployment's resource remap: what backs a
// canonical resource path (the XBIN_RES_* value, main's mount) in this
// deployment's sandbox. main's remap maps every path to itself, so its binds
// stay today's (Src == Dst); a path-valued resource with no entry is never
// bound for another deployment (it would bind main's data).
type ResBind struct {
	Src  string // the host dir backing the canonical path for this deployment
	RO   bool   // read-only (v1 never sets it)
	Omit bool   // no bind at all: a blocked edge, so the path is absent in the sandbox
}

// DeployProgress receives a deploy's phases and its result. A deploy that
// puts a checkpoint on a deployment reports only through it, never through
// build-* events.
type DeployProgress func(phase, result string, err error)

// DeploymentHooks are installed into the runner by the deployments plane at
// boot (Runner embeds them). The spawn-time hooks (Egress, GPU, NetRoster, …)
// keep their signatures: they learn the spawning deployment from the view's
// Deployment field, empty for the primary.
type DeploymentHooks struct {
	// CodeFor reads the deployment record: what deployment dep of tile runs.
	// Without a record it answers {WorkTree: true} for main and
	// util.ErrNoDeployment for any other name; nil answers the same.
	CodeFor func(tile, dep string) (Code, error)
	// Primary names tile's primary deployment; "main" without a record, and
	// when nil.
	Primary func(tile string) string
	// View is the deployment view a generation of c running code spawns
	// from: the registry's own component for the primary's code, the fields
	// of the deployment's own code otherwise. nil: only the work tree has a
	// view (c itself).
	View func(c *registry.Component, code Code) (*registry.Component, error)
	// Materialize returns the read-only host tree of tile's checkpoint tree,
	// preparing it when missing. nil: no checkpoint can be materialized.
	Materialize func(tile, tree string) (root string, err error)
	// EnvFor is the resource and identity env of deployment dep's generation
	// of c, with its resource remap. nil: EnvForComponent's env and no remap
	// (every path bound at itself, which only main may use).
	EnvFor func(c *registry.Component, dep string) (env []string, remap map[string]ResBind)
	// LimitsFor answers deployment dep's cgroup caps: the tile's unless a
	// tile manager set lower ones, never above the tile's ceilings (P22).
	// nil: the caps installed on Cgroup, today's.
	LimitsFor func(tile, dep string) cgroup.Limits
}

// primary names tile's primary deployment.
func (r *Runner) primary(tile string) string {
	if f := r.Primary; f != nil {
		return f(tile)
	}
	return util.MainDeployment
}

// codeFor is the code a backend generation of deployment dep of tile starts
// from. A checkpoint starts only under isolation (P18): without it the
// deployment is held with the reason, never started from its work tree in
// the checkpoint's place (06-security C7), so every restart path that builds
// from codeFor holds too.
func (r *Runner) codeFor(tile, dep string) (Code, error) {
	code, err := r.recordCode(tile, dep)
	if err != nil {
		return Code{}, err
	}
	if !code.WorkTree && !r.Isolate {
		return Code{}, needsIsolation(tile)
	}
	return code, nil
}

// recordCode is what deployment dep of tile runs, as its record says.
func (r *Runner) recordCode(tile, dep string) (Code, error) {
	if f := r.CodeFor; f != nil {
		return f(tile, dep)
	}
	if dep != util.MainDeployment {
		return Code{}, util.NoDeployment(tile, dep)
	}
	return Code{WorkTree: true}, nil
}

// ErrNeedsIsolation refuses or holds a backend pinned to a checkpoint on an
// xbind without isolation (P18): nothing can show the checkpoint at the
// tile's path there, and the work tree never runs in its place (C7).
var ErrNeedsIsolation = errors.New("pinning a backend to a checkpoint needs isolation (--isolate)")

func needsIsolation(tile string) error {
	return fmt.Errorf("%w — %s is pinned, and this xbind runs backends without a sandbox", ErrNeedsIsolation, tile)
}

// heldWithoutIsolation is the isolation hold alone: a pinned backend on an
// xbind without isolation. Other questions about the record stay Ensure's.
func (r *Runner) heldWithoutIsolation(tile, dep string) error {
	if r.Isolate {
		return nil
	}
	if code, err := r.recordCode(tile, dep); err == nil && !code.WorkTree {
		return needsIsolation(tile)
	}
	return nil
}

// view is the component a generation of c running code spawns from.
func (r *Runner) view(c *registry.Component, code Code) (*registry.Component, error) {
	if f := r.View; f != nil {
		return f(c, code)
	}
	if !code.WorkTree {
		return nil, fmt.Errorf("%s: no view of checkpoint %s without the deployments plane", c.Path, code.Tree)
	}
	return c, nil
}

// materialize returns the host tree of tile's checkpoint tree.
func (r *Runner) materialize(tile, tree string) (string, error) {
	if f := r.Materialize; f != nil {
		return f(tile, tree)
	}
	return "", fmt.Errorf("%s: checkpoint %s can't be materialized without the deployments plane", tile, tree)
}

// envFor is the env and resource remap of deployment dep's generation of c.
func (r *Runner) envFor(c *registry.Component, dep string) ([]string, map[string]ResBind) {
	if f := r.EnvFor; f != nil {
		return f(c, dep)
	}
	if r.EnvForComponent != nil {
		return r.EnvForComponent(c), nil
	}
	return nil, nil
}

// shouldRun gates a spawn of deployment dep of tile: the tile's lifecycle
// and its encryption hold (ShouldRun, per tile).
func (r *Runner) shouldRun(tile, dep string) bool {
	return r.ShouldRun == nil || r.ShouldRun(tile)
}

// sockDir names a deployment's directory under RunDir: CompKey(tile) for
// main, today's; "d-" and 16 hex digits of SHA-256(tile ‖ 0x00 ‖ dep) for any
// other deployment: a name no CompKey takes (a CompKey has its '-' nine
// characters from the end), and short enough for the 108-byte socket limit.
func sockDir(tile, dep string) string {
	if dep == util.MainDeployment {
		return util.CompKey(tile)
	}
	h := sha256.Sum256([]byte(tile + "\x00" + dep))
	return "d-" + hex.EncodeToString(h[:8])
}

// deployActivity is the data of a "deployments" event the runner publishes
// for a non-primary deployment.
type deployActivity struct {
	Op         string `json:"op"` // reload | build
	Deployment string `json:"deployment"`
	Phase      string `json:"phase,omitempty"` // build: start | ok | error
	Text       string `json:"text,omitempty"`  // build: compiler output
}

// emit publishes one runner event of (tile, dep). The primary's keep today's
// types and the bare tile path. A non-primary deployment's never ride an old
// type: its reload and build-* become a "deployments" event (op reload or
// build) with the bare tile path, naming the deployment in data.
func (r *Runner) emit(tile, dep, typ, text string) {
	if dep == r.primary(tile) {
		r.Hub.Publish(events.Event{Type: typ, Component: tile, Text: text})
		return
	}
	a := deployActivity{Deployment: dep}
	switch typ {
	case "reload":
		a.Op = "reload"
	case "build-start", "build-ok", "build-error":
		a.Op, a.Phase, a.Text = "build", strings.TrimPrefix(typ, "build-"), text
	default:
		return // the runner has no other event about a deployment
	}
	r.Hub.Publish(events.Event{Type: "deployments", Component: tile, Data: a})
}

// EnsureDeployment returns the unix socket of a healthy backend for
// deployment dep of c, as Ensure does for the primary. Only the primary runs
// until the runner keys its state by deployment; any other name is refused
// with util.ErrNoDeployment. A pinned backend on an xbind without isolation
// is held with the reason (C7).
func (r *Runner) EnsureDeployment(ctx context.Context, c *registry.Component, dep string) (string, error) {
	if dep != r.primary(c.Path) {
		return "", util.NoDeployment(c.Path, dep)
	}
	if c.HasBackend() {
		if err := r.heldWithoutIsolation(c.Path, dep); err != nil {
			return "", err
		}
	}
	return r.Ensure(ctx, c)
}

// TrackDeployment marks one in-flight connection to deployment dep of tile,
// as Track does for the primary. Another deployment has nothing to track.
func (r *Runner) TrackDeployment(tile, dep string) func() {
	if dep != r.primary(tile) {
		return func() {}
	}
	return r.Track(tile)
}

// ChangedDeployment marks deployment dep of c dirty and clears its crash
// history, as Changed does for the primary. Another deployment has no state.
func (r *Runner) ChangedDeployment(c *registry.Component, dep string) {
	if dep == r.primary(c.Path) {
		r.Changed(c)
	}
}

// ChangedTile is ChangedDeployment for every deployment of c with a running,
// building or failed generation: tile-level restarts (a grant, a binding, a
// transfer) reach them all. Only the primary has one today.
func (r *Runner) ChangedTile(c *registry.Component) {
	r.Changed(c)
}

// StopDeployment stops one deployment of tile (its removal). Stop stops
// every deployment of the tile; only the primary runs today.
func (r *Runner) StopDeployment(tile, dep string) {
	if dep == r.primary(tile) {
		r.Stop(tile)
	}
}
