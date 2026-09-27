package runner

// deployworker.go — the deploy worker (07-runtime §8): Deploy and Restart,
// on the build turn Ensure's builds take; moved verbatim out of deploy.go.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

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
// serves, the runner announces one reload: the bare reload for the primary,
// a deployments reload for any other deployment (§8.5; Code.Identical tells
// a same-files move). Besides:
//   - the code a healthy generation already runs: nothing to do (§8.7);
//   - no generation and none failing (never started, reaped): the code is
//     prepared, built so its errors surface now, and the next request
//     starts it (§8.6);
//   - code without a backend: commit; a generation of earlier code drains;
//   - a failing deployment (crash loop, failed start) gets a new generation,
//     which clears its breaker.
//
// Refused before anything is touched, neither callback called: a name the
// record doesn't hold (util.ErrNoDeployment), code no view describes, a
// disabled tile, a backend pinned to a checkpoint on an xbind without
// isolation (P18, ErrNeedsIsolation), and there, too, a backend of any
// deployment but main as the primary.
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
	primary := dep == r.primary(c.Path)
	if !primary {
		if _, err := r.recordCode(c.Path, dep); err != nil {
			return nil, err
		}
	}
	if code.WorkTree == (code.Tree != "") {
		return nil, fmt.Errorf("%s: a generation runs the work tree or one checkpoint", c.Path)
	}
	p := &deployPlan{dep: dep, code: code.runs(), identical: code.Identical, restart: restart}
	view, err := r.viewOf(c, dep, p.code, "")
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
	if !r.Isolate && p.code.WorkTree && (!primary || dep != util.MainDeployment) { // P18
		return nil, fmt.Errorf("%s: deployment %s runs only in a sandbox (--isolate), and this xbind runs backends without one", c.Path, dep)
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
		s = r.stateOf(c.Path, dep)
	} else if s = r.existingStateOf(c.Path, dep); s == nil {
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
		g, err := r.deployBuild(c, p)
		if err != nil {
			// Nothing runs, and what the record names still starts on the
			// next request: the failure is the actor's and the log's alone.
			r.logDeployFailure(c.Path, p.dep, p.code, err)
			return rep.failed(err)
		}
		g.release() // prepared, not run: the next request's start holds it again
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
	g, err := r.deployBuild(c, p)
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
		inst, err = r.spawnFor(g.view, dep, g.bin, gen)
	}
	if err != nil {
		g.release()
		err = r.deployFailed(c, s, p, err, rep)
		if first {
			r.restorePrevious(c, s, dep, old)
		}
		return err
	}
	inst.code, inst.root, inst.artifact = p.code, g.root, g.artifact

	rep.step(PhaseSwap)
	if !r.install(s, inst, true) {
		g.release()
		return rep.failed(util.NoDeployment(tile, dep)) // removed while it deployed
	}
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
		g.release()
		return r.deployFailed(c, s, p, err, rep)
	}
	s.mu.Lock()
	s.lastErr, s.crashes = nil, nil
	s.mu.Unlock()
	if old != nil && !first {
		go r.stopGen(old, drainDeadline)
	}
	r.watchGen(c, s, dep, inst, g.release)
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

// spawn is spawnFor the deployment view names.
func (r *Runner) spawn(view *registry.Component, bin string, gen int) (*instance, error) {
	return r.spawnFor(view, r.viewDeployment(view), bin, gen)
}

// spawnFor starts generation gen of deployment dep from bin and waits until
// it answers; one that never does is stopped.
func (r *Runner) spawnFor(view *registry.Component, dep, bin string, gen int) (*instance, error) {
	inst, err := r.startFor(view, dep, bin, gen)
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

// deployBuild produces what a generation of the plan's code starts from,
// through resolveGen (inspect.go) as every restart does: the work tree's
// build, or the checkpoint's kept artifact, built from its materialized tree
// (the view's CodeRoot) only when none is kept (§8.7), its tree and artifact
// held from here until the generation exits (RootsInUse). A node or python
// checkpoint's entry is looked for in that tree, never in the work tree.
func (r *Runner) deployBuild(c *registry.Component, p *deployPlan) (genPlan, error) {
	if err := checkpointEntry(p.view); err != nil {
		return genPlan{}, err
	}
	return r.resolveGenFor(c, p.dep, p.code)
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
