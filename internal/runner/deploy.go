package runner

// deploy.go — tile deployments in the runner: the hooks boot installs, what a
// generation runs, the per-deployment names beside Ensure, Track, Changed and
// Stop, and the deploy worker. Every hook is nil-safe: nil answers as a tile
// without a deployment record, whose one deployment is main, the primary,
// following the work tree. Ensure, Track, Changed and Stop keep meaning the
// primary, so every existing caller keeps today's behaviour (P7).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
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

// ---- the deploy worker (07-runtime §8) ----

// A deploy's phases and results, as a deploy entry spells them (11-contract
// §1.1); the plane adds the checkpoint and materialize phases before Deploy.
const (
	PhaseBuild = "build"
	PhaseStart = "start"
	PhaseSwap  = "swap"

	ResultRunning = "running"
	ResultOK      = "ok"
	ResultFailed  = "failed"
)

// Deploy puts code on deployment dep of c through blue/green (07-runtime
// §8.1), on the build turn Ensure's builds take, so a lazy start, a crash
// restart and a deploy never race, and two deploys of one deployment never
// build at once: build (a checkpoint from its materialized tree, the view's
// CodeRoot), start, health check, swap, then commit(), then the result, then
// the old generation drains. commit belongs to the deployments plane (the
// record's new pointer, the journal); it runs holding the build turn, so it
// must not call back into the runner for this deployment.
//
// A checkpoint deploy reports its phases and result through progress only,
// never through build-* (rule C2). A failed one paints no overlay: the
// previous generation keeps serving, the error stays on the state for the
// status, and the compiler output goes to the deployment's log (§8.4). A
// deploy of the work tree (resume, attach) is today's rebuild, announced by
// build-* for the primary. After a swap that changed what the deployment
// serves, the runner announces one reload: the bare reload for the primary
// (§8.5; Code.Identical tells a same-files move). Besides:
//   - the code a healthy generation already runs: nothing to do (§8.7);
//   - no generation and none failing (never started, reaped): the code is
//     prepared, built so its errors surface now, and the next request
//     starts it (§8.6);
//   - code without a backend: commit; a generation of earlier code drains;
//   - a failing deployment (crash loop, failed start) gets a new generation,
//     which clears its breaker.
//
// Refused before anything is touched, neither callback called: a deployment
// other than the primary (only it has runner state in this release), code no
// view describes, a disabled tile, and a backend pinned to a checkpoint on
// an xbind without isolation (P18, ErrNeedsIsolation).
func (r *Runner) Deploy(ctx context.Context, c *registry.Component, dep string, code Code, commit func() error, progress DeployProgress) error {
	return r.deploy(ctx, c, dep, code, commit, progress, false)
}

// Restart starts a new generation of deployment dep's current code (its
// checkpoint, or the work tree it follows), even while the running one is
// healthy, and clears the crash breaker (11-contract §1.6 restart:true;
// 07-runtime §8.7). It moves no code, so it commits nothing and announces no
// reload; besides progress, build-* report it for the primary, as any restart
// of its current code.
func (r *Runner) Restart(ctx context.Context, c *registry.Component, dep string, progress DeployProgress) error {
	if dep != r.primary(c.Path) {
		return util.NoDeployment(c.Path, dep)
	}
	code, err := r.codeFor(c.Path, dep)
	if err != nil {
		return err
	}
	return r.deploy(ctx, c, dep, code, nil, progress, true)
}

// deployPlan is one deploy, resolved before its turn.
type deployPlan struct {
	dep       string
	code      Code // what the new generation runs
	identical bool // Code.Identical
	restart   bool
	view      *registry.Component // the deployment view of code
	root      string              // the host tree bound at c.Dir; "" for the work tree
}

// loud: the deploy reports build-* besides progress (a work-tree build, a
// restart of current code).
func (p *deployPlan) loud() bool { return p.restart || p.code.WorkTree }

// served is the code a generation runs; a generation started before it
// learned its code (a zero code) runs the work tree.
func (inst *instance) served() Code {
	if inst.code.Tree == "" {
		return Code{WorkTree: true}
	}
	return Code{Tree: inst.code.Tree}
}

func (r *Runner) planDeploy(c *registry.Component, dep string, code Code, restart bool) (*deployPlan, error) {
	if dep != r.primary(c.Path) {
		return nil, util.NoDeployment(c.Path, dep)
	}
	if code.WorkTree == (code.Tree != "") {
		return nil, fmt.Errorf("%s: a generation runs the work tree or one checkpoint", c.Path)
	}
	p := &deployPlan{dep: dep, code: code.runs(), identical: code.Identical, restart: restart}
	view, err := r.view(c, p.code)
	if err != nil {
		return nil, err
	}
	if err := registry.ValidateRuntime(view.Manifest); err != nil {
		return nil, fmt.Errorf("component %s: %w", c.Path, err) // runtime "cgi" (D117): never runs
	}
	p.view = view
	if !view.HasBackend() {
		return p, nil // served by the static plane: needs no isolation, starts nothing
	}
	if !p.code.WorkTree {
		if !r.Isolate {
			return nil, needsIsolation(c.Path)
		}
		if p.root = view.CodeRoot; p.root == "" {
			if p.root, err = r.materialize(c.Path, p.code.Tree); err != nil {
				return nil, err
			}
			v := *view // views are shared: never written
			v.CodeRoot = p.root
			p.view = &v
		}
	}
	if !r.shouldRun(c.Path, dep) {
		return nil, fmt.Errorf("component %s is not enabled", c.Path)
	}
	return p, nil
}

func (r *Runner) deploy(ctx context.Context, c *registry.Component, dep string, code Code, commit func() error, progress DeployProgress, restart bool) error {
	p, err := r.planDeploy(c, dep, code, restart)
	if err != nil {
		return err
	}
	if commit == nil {
		commit = func() error { return nil }
	}
	if progress == nil {
		progress = func(string, string, error) {}
	}
	rep := &deployReport{progress: progress}
	backend := p.view.HasBackend()
	var s *state
	if backend {
		s = r.state(c.Path)
	} else if s = r.existingState(c.Path); s == nil {
		return r.deployStatic(c, nil, p, commit, rep) // no generation to drain
	}
	if err := takeTurn(ctx, s); err != nil {
		return err
	}
	defer releaseTurn(s)
	if !backend {
		return r.deployStatic(c, s, p, commit, rep)
	}
	s.mu.Lock()
	old, failing := s.cur, s.lastErr != nil
	s.mu.Unlock()
	changed := !p.restart && servedChange(old, p)
	switch {
	case !p.restart && old != nil && old.served() == p.code:
		if err := commit(); err != nil {
			return rep.failed(err)
		}
		rep.ok()
		return nil
	case !p.restart && old == nil && !failing:
		return r.deployIdle(c, s, p, commit, rep, changed)
	}
	return r.deploySwap(c, s, p, commit, rep, changed)
}

// deployReport feeds a deploy's progress callback.
type deployReport struct {
	progress DeployProgress
	phase    string
}

func (d *deployReport) step(phase string) { d.phase = phase; d.progress(phase, ResultRunning, nil) }
func (d *deployReport) ok()               { d.progress(d.phase, ResultOK, nil) }
func (d *deployReport) failed(err error) error {
	d.progress(d.phase, ResultFailed, err)
	return err
}

// servedChange reports whether moving onto the plan's code changes what the
// deployment serves, which one reload announces (§8.5). The runner knows
// when the running generation is pinned; otherwise the plane's Identical says
// (a capture of the work tree the deployment followed serves the same files).
func servedChange(old *instance, p *deployPlan) bool {
	if old != nil {
		switch from := old.served(); {
		case !from.WorkTree && !p.code.WorkTree:
			return from.Tree != p.code.Tree
		case from.WorkTree && p.code.WorkTree:
			return false
		case p.code.WorkTree:
			return true // resume: back onto the work tree
		}
	}
	return !p.identical
}

// takeTurn waits for the state's build turn and takes it: the single flight
// Ensure's builds take, so the deploy runs alone on its deployment.
func takeTurn(ctx context.Context, s *state) error {
	for {
		s.mu.Lock()
		if !s.building {
			s.building = true
			s.buildDone = make(chan struct{})
			s.mu.Unlock()
			return nil
		}
		done := s.buildDone
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func releaseTurn(s *state) {
	s.mu.Lock()
	s.building = false
	close(s.buildDone)
	s.mu.Unlock()
}

// existingState is comp's runner state, never creating one.
func (r *Runner) existingState(comp string) *state {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.states[comp]
}

// deployStatic puts code without a backend on the deployment: the static
// plane serves it once the record commits (§8.6); a generation of earlier
// code drains.
func (r *Runner) deployStatic(c *registry.Component, s *state, p *deployPlan, commit func() error, rep *deployReport) error {
	var old *instance
	if s != nil {
		s.mu.Lock()
		old = s.cur
		s.mu.Unlock()
	}
	changed := !p.restart && servedChange(old, p)
	rep.step(PhaseSwap)
	if err := commit(); err != nil {
		return rep.failed(err)
	}
	if s != nil {
		s.mu.Lock()
		if s.cur == old {
			s.cur = nil
		}
		s.lastErr, s.crashes, s.dirty = nil, nil, false
		s.mu.Unlock()
	}
	if old != nil {
		go r.stopGen(old, drainDeadline)
	}
	rep.ok()
	if changed {
		r.emit(c.Path, p.dep, "reload", "")
	}
	return nil
}

// deployIdle prepares code for a deployment with no generation (never
// started, reaped): a checkpoint is built now, so its errors surface, and the
// next request starts it (§8.6); the work tree has nothing to prepare, as a
// save without a process builds nothing.
func (r *Runner) deployIdle(c *registry.Component, s *state, p *deployPlan, commit func() error, rep *deployReport, changed bool) error {
	rep.step(PhaseBuild)
	if !p.code.WorkTree {
		if _, err := r.deployBuild(p); err != nil {
			// Nothing runs, and what the record names still starts on the
			// next request: the failure is the actor's and the log's alone.
			r.logDeployFailure(c.Path, p.dep, p.code, err)
			return rep.failed(err)
		}
	}
	if err := commit(); err != nil {
		return rep.failed(err)
	}
	s.mu.Lock()
	s.dirty, s.crashes = true, nil
	s.mu.Unlock()
	rep.ok()
	if changed {
		r.emit(c.Path, p.dep, "reload", "")
	}
	return nil
}

// deploySwap is a deploy's generation transition (§8.1 steps 1–6).
func (r *Runner) deploySwap(c *registry.Component, s *state, p *deployPlan, commit func() error, rep *deployReport, changed bool) error {
	tile, dep := c.Path, p.dep
	s.mu.Lock()
	s.dirty = false // what a Changed asked for until now, this generation gets
	s.mu.Unlock()
	if p.loud() {
		r.emit(tile, dep, "build-start", "")
	}
	rep.step(PhaseBuild)
	bin, err := r.deployBuild(p)
	if err != nil {
		return r.deployFailed(c, s, p, err, rep)
	}

	s.mu.Lock()
	s.gen++
	gen, old := s.gen, s.cur
	first := old != nil && r.stopFirst(p.view) // vm.go: no two guests on one sqlite
	if first {
		s.cur = nil
	}
	s.mu.Unlock()
	if first {
		r.stopGen(old, drainDeadline)
	}

	rep.step(PhaseStart)
	var inst *instance
	if !r.shouldRun(tile, dep) { // disabled while it built
		err = fmt.Errorf("component %s is not enabled", tile)
	} else {
		inst, err = r.spawn(p.view, bin, gen)
	}
	if err != nil {
		err = r.deployFailed(c, s, p, err, rep)
		if first {
			r.restorePrevious(c, s, dep, old)
		}
		return err
	}
	inst.code, inst.root, inst.artifact = p.code, p.root, bin

	rep.step(PhaseSwap)
	s.mu.Lock()
	s.cur = inst
	s.lastReq = r.now()
	s.mu.Unlock()
	if err := commit(); err != nil {
		// The record didn't take the new code: its previous generation keeps
		// serving what the record names; the new one stops.
		s.mu.Lock()
		if s.cur == inst {
			s.cur = nil
			if old != nil && !first && alive(old) {
				s.cur = old
			} else {
				s.dirty = true // the next request starts what the record names
			}
		}
		s.mu.Unlock()
		r.stopGen(inst, 2*time.Second)
		return r.deployFailed(c, s, p, err, rep)
	}
	s.mu.Lock()
	s.lastErr, s.crashes = nil, nil
	s.mu.Unlock()
	if old != nil && !first {
		go r.stopGen(old, drainDeadline)
	}
	r.watchGen(c, s, dep, inst)
	if p.loud() {
		r.emit(tile, dep, "build-ok", "")
	}
	rep.ok()
	if changed {
		r.emit(tile, dep, "reload", "")
	}
	slog.Info("backend deployed", "component", tile, "deployment", dep, "gen", gen, "code", codeName(p.code))
	return nil
}

// spawn starts generation gen from bin and waits until it answers; one that
// never does is stopped.
func (r *Runner) spawn(view *registry.Component, bin string, gen int) (*instance, error) {
	inst, err := r.startGen(view, bin, gen)
	if err != nil {
		return nil, err
	}
	if err := r.awaitHealthy(view, inst); err != nil {
		if !errors.Is(err, errExited) && r.wantsVM(view) {
			r.sbxFail(view, sbx.Health, fmt.Errorf("the VM backend never listened: %w", err))
		}
		r.stopGen(inst, 2*time.Second)
		return nil, fmt.Errorf("backend did not become healthy: %w", err)
	}
	return inst, nil
}

func alive(inst *instance) bool {
	select {
	case <-inst.waitCh:
		return false
	default:
		return true
	}
}

// deployBuild produces what a generation of the plan's code starts from: the
// work tree's build, or the checkpoint's, from its materialized tree (the
// view's CodeRoot). A node or python checkpoint's entry is looked for in that
// tree, never in the work tree.
func (r *Runner) deployBuild(p *deployPlan) (string, error) {
	if err := checkpointEntry(p.view); err != nil {
		return "", err
	}
	return r.buildGen(p.view)
}

// checkpointEntry checks that an interpreted checkpoint has its entry file,
// opened beneath the materialized tree, so no symlink leads elsewhere.
func checkpointEntry(v *registry.Component) error {
	if v.CodeRoot == "" {
		return nil
	}
	entry := v.Manifest.Entry
	switch v.Manifest.Runtime {
	case "node":
		if entry == "" {
			entry = "backend/server.js"
		}
	case "python":
		if entry == "" {
			entry = "backend/server.py"
		}
	default:
		return nil
	}
	if f, err := fsutil.OpenBeneath(v.CodeRoot, path.Clean(entry)); err == nil {
		fi, serr := f.Stat()
		f.Close()
		if serr == nil && fi.Mode().IsRegular() {
			return nil
		}
	}
	return &BuildError{Output: fmt.Sprintf("entry %s not found in the checkpoint (set \"entry\" in xbin.json)", entry)}
}

// deployFailed ends a deploy that didn't swap. The previous generation keeps
// serving; the error stays on the state, for the status; a checkpoint's
// failure and compiler output go to the deployment's log, and only a
// work-tree build or a restart reports build-error (for the primary, as
// today).
func (r *Runner) deployFailed(c *registry.Component, s *state, p *deployPlan, err error, rep *deployReport) error {
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
	if p.loud() {
		r.emit(c.Path, p.dep, "build-error", err.Error())
	}
	if !p.code.WorkTree {
		r.logDeployFailure(c.Path, p.dep, p.code, err)
	}
	return rep.failed(err)
}
