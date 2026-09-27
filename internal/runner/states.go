package runner

// states.go — stopping backends (one, all, idle ones), reporting each
// component's backend state, and how a generation a deploy started ends: its
// crash watch, the breaker's words, the deployment's log (07-runtime §7, §8.4).

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

func (r *Runner) stop(inst *instance, deadline time.Duration) {
	if inst.cmd.Process == nil {
		return
	}
	_ = inst.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-inst.waitCh:
	case <-time.After(deadline):
		_ = inst.cmd.Process.Kill()
		<-inst.waitCh
	}
	_ = os.Remove(inst.sock)
}

// Stop terminates a single component's running backend, if any (e.g. when the
// owner disables/offloads it). A subsequent request re-spawns it (unless the
// caller has since gated it). No-op if it isn't running.
func (r *Runner) Stop(comp string) {
	r.mu.Lock()
	s := r.states[comp]
	r.mu.Unlock()
	if s == nil {
		return
	}
	s.mu.Lock()
	inst := s.cur
	s.cur = nil
	s.mu.Unlock()
	if inst != nil {
		r.stopGen(inst, 5*time.Second)
	}
}

// StopAll terminates all backends (xbind shutdown).
func (r *Runner) StopAll() {
	r.mu.Lock()
	states := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		states = append(states, s)
	}
	r.mu.Unlock()
	var wg sync.WaitGroup
	for _, s := range states {
		s.mu.Lock()
		inst := s.cur
		s.cur = nil
		s.mu.Unlock()
		if inst != nil {
			wg.Add(1)
			go func() { defer wg.Done(); r.stopGen(inst, 5*time.Second) }()
		}
	}
	wg.Wait()
}

// Status reports per-component backend state for /api/xbin/status.
func (r *Runner) Status() map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]any{}
	for path, s := range r.states {
		s.mu.Lock()
		st := "idle"
		switch {
		case s.building:
			st = "building"
		case s.cur != nil:
			st = "healthy"
		case s.lastErr != nil:
			st = "failed"
		}
		e := map[string]any{"state": st, "gen": s.gen}
		if s.lastErr != nil {
			e["error"] = s.lastErr.Error()
		}
		out[path] = e
		s.mu.Unlock()
	}
	return out
}

// DeploymentState is one deployment's backend as the runner sees it: beside
// the record's pointer, what its current generation serves and the last
// error, so a status can say "pinned to c:3f2a1c9 — build failed; serving an
// earlier generation" (07-runtime §8.2).
type DeploymentState struct {
	State   string // idle | building | healthy | failed
	Gen     int    // the current generation's number; without one, the last spent
	Serving string // the current generation's code: "work-tree", or the checkpoint's full tree hash; "" without one
	Error   string
}

// DeploymentStatus is deployment dep of tile's DeploymentState.
func (r *Runner) DeploymentStatus(tile, dep string) DeploymentState {
	s := r.existingState(tile)
	if s == nil || dep != r.primary(tile) {
		return DeploymentState{State: "idle"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d := DeploymentState{State: stateName(s), Gen: s.gen}
	if s.cur != nil {
		d.Gen, d.Serving = s.cur.gen, "work-tree"
		if code := s.cur.served(); !code.WorkTree {
			d.Serving = code.Tree
		}
	}
	if s.lastErr != nil {
		d.Error = s.lastErr.Error()
	}
	return d
}

func (r *Runner) reaper() {
	for range time.Tick(time.Minute) {
		r.reapOnce()
	}
}

// reapOnce stops every backend that has served nothing for idleReap: no
// in-flight connection, not alwaysOn (its own code's, alwayson.go). The next
// request restarts it lazily. The alwaysOn question may resolve a deployment
// view, so it is asked outside the state's lock, and the idleness checked
// again under it.
func (r *Runner) reapOnce() {
	r.mu.Lock()
	states := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		states = append(states, s)
	}
	r.mu.Unlock()
	for _, s := range states {
		s.mu.Lock()
		comp, idle := s.comp, r.idle(s)
		s.mu.Unlock()
		if !idle || r.isAlwaysOn(comp) {
			continue
		}
		s.mu.Lock()
		if !r.idle(s) {
			s.mu.Unlock()
			continue
		}
		inst := s.cur
		s.cur = nil
		s.dirty = true // next request restarts lazily
		s.mu.Unlock()
		slog.Info("reaping idle backend", "component", comp)
		go r.stopGen(inst, 5*time.Second)
	}
}

// idle: a running generation that has served nothing for idleReap; callers
// hold s.mu.
func (r *Runner) idle(s *state) bool {
	return s.cur != nil && s.active == 0 && r.now().Sub(s.lastReq) > idleReap
}

// watchGen is the crash watch of a generation a deploy started, as
// buildAndStart's is of its own: a generation that exits without being
// replaced leaves the state dirty, so the next request restarts the code the
// record names; crashLimit exits inside the window trip the breaker, which
// a deploy or restart clears (§7 row 3). release drops what the generation
// holds in RootsInUse once it exits.
func (r *Runner) watchGen(c *registry.Component, s *state, dep string, inst *instance, release func()) {
	go func() {
		<-inst.waitCh
		release()
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.cur != inst {
			return // replaced, or stopped
		}
		s.cur = nil
		s.crashes = append(s.crashes, r.now())
		recent := 0
		for _, t := range s.crashes {
			if r.now().Sub(t) < crashWindow*time.Duration(crashLimit) {
				recent++
			}
		}
		if recent >= crashLimit {
			s.lastErr = crashLoopError(c.Path, dep, inst.served(), recent)
			r.emit(c.Path, dep, "build-error", s.lastErr.Error())
		} else {
			s.dirty = true // transparent restart on the next request
			go r.afterExit(c)
		}
	}()
}

// crashLoopError is the breaker's error for deployment dep running code,
// naming what clears it and the deployment's log.
func crashLoopError(tile, dep string, code Code, n int) error {
	if code.WorkTree {
		return fmt.Errorf("backend crash-looping (%d exits); fix the code and save to retry — see %s", n, deploymentLog(tile, dep))
	}
	return fmt.Errorf("backend crash-looping (%d exits); deploy a fixed checkpoint or restart it — see %s", n, deploymentLog(tile, dep))
}

// deploymentLog is deployment dep's backend log, workspace-relative: main
// keeps today's .xbin/log/<CompKey>.log; any other deployment's lives under
// the tile's deploy state (07-runtime §9).
func deploymentLog(tile, dep string) string {
	if dep == util.MainDeployment {
		return ".xbin/log/" + util.CompKey(tile) + ".log"
	}
	return ".xbin/deploy/" + util.TileKey(tile) + "/d/" + dep + "/backend.log"
}

// logDeployFailure appends a failed checkpoint deploy, with the compiler
// output of a failed build, to the deployment's log (§8.4); the deploy log
// gets its first 500 bytes from the plane.
func (r *Runner) logDeployFailure(tile, dep string, code Code, err error) {
	text := err.Error()
	var be *BuildError
	if errors.As(err, &be) {
		text = be.Output
	}
	p := filepath.Join(r.Root, filepath.FromSlash(deploymentLog(tile, dep)))
	if os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	f, ferr := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "--- deploy of %s failed %s ---\n%s\n", codeName(code), r.now().Format(time.RFC3339), strings.TrimRight(text, "\n"))
}

// codeName is code as the deployments wire names it: "work-tree", or c: and
// the tree hash's first 7 digits.
func codeName(code Code) string {
	if code.WorkTree || code.Tree == "" {
		return "work-tree"
	}
	if len(code.Tree) > 7 {
		return "c:" + code.Tree[:7]
	}
	return "c:" + code.Tree
}
