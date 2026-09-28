package runner

// alwayson.go — backends that stay up ("alwaysOn": true in xbin.json). An
// ordinary backend starts on its first proxied request and is reaped when
// idle; a tile that holds a connection open (a chat adapter's socket) has no
// inbound requests to start it or keep it alive. So: start it at boot and
// whenever it becomes runnable (enable, vault unseal, the flag appearing on a
// rescan), skip it in the idle reaper, and after an exit restart it once a
// backoff has passed — 1 s doubling to 5 min, back to 1 s after 10 healthy
// minutes. The crash-loop breaker still wins: a backend that keeps dying
// stays down until a save (a pinned one: until a deploy or restart). Every
// start is event-driven (a one-shot timer after an exit); nothing polls.
// The flag is the deployment's own code's (its view): a pinned primary's
// checkpoint decides, never a work-tree edit.
//
// alwaysOn is per deployment (07-runtime §11; 09-fabric §7). The primary's
// is its own code's flag. A non-primary deployment's is never implied: its
// own code's flag and its alwaysOn switch, which a tile manager sets
// (AlwaysOnSwitched), both; off by default, because alwaysOn tiles hold
// exclusive external connections. The wakes, the backoff and the reaper's
// exemption are keyed per deployment, and the limit of starts at once is
// shared, so a boot doesn't storm.

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

const (
	aoBackoffMin  = time.Second
	aoBackoffMax  = 5 * time.Minute
	aoHealthyAge  = 10 * time.Minute
	aoConcurrency = 2 // boot builds at once (a rescan of many tiles must not storm)
)

// alwaysOn is the restart bookkeeping, keyed by stateKey(tile, dep).
type alwaysOn struct {
	mu      sync.Mutex
	backoff map[string]time.Duration
	upSince map[string]time.Time
	pending map[string]bool // a restart is scheduled
	sem     chan struct{}
}

// isAlwaysOn: comp's primary stays up (the reaper's exemption of a primary).
func (r *Runner) isAlwaysOn(comp string) bool { return r.keptUp(comp, r.primary(comp)) }

// keptUp: deployment dep of tile has effective alwaysOn, so the idle reaper
// spares it: the primary by its own code's flag, any other deployment only
// while its switch is on too (07-runtime §11).
func (r *Runner) keptUp(tile, dep string) bool {
	c, ok := r.Reg.Component(tile)
	return ok && r.alwaysOnView(c, dep) != nil
}

// switched names tile's non-primary deployments whose alwaysOn switch is on.
func (r *Runner) switched(tile string) []string {
	if f := r.AlwaysOnSwitched; f != nil {
		return f(tile)
	}
	return nil
}

// alwaysOnView is the deployment view of deployment dep of c when dep has
// effective alwaysOn, nil otherwise. Its own code says "alwaysOn" (a pinned
// deployment's checkpoint manifest, the work tree's while it follows it;
// 07-runtime §11), and a non-primary deployment's switch is on as well,
// which is asked before any view is. A deployment that can't start — no
// such name, a held record, a pinned backend without isolation (C7), a
// non-primary one without isolation (D119h) — isn't kept up either.
func (r *Runner) alwaysOnView(c *registry.Component, dep string) *registry.Component {
	primary := dep == r.primary(c.Path)
	if !primary && (!r.Isolate || !slices.Contains(r.switched(c.Path), dep)) {
		return nil
	}
	code, err := r.codeFor(c.Path, dep)
	if err != nil {
		return nil
	}
	v := c // the work tree's view is c itself: a zero-state tile asks no checkpoint question (D119c)
	switch {
	case !primary:
		v, err = r.viewOf(c, dep, code, "")
	case !code.WorkTree:
		v, err = r.view(c, code)
	}
	if err != nil || !v.Manifest.AlwaysOn {
		return nil
	}
	return v
}

// WakeAlwaysOn starts every alwaysOn backend that may run and isn't running:
// each tile's primary, and its non-primary deployments whose switch is on.
// Idempotent; call it whenever something may have made one runnable.
func (r *Runner) WakeAlwaysOn() {
	for _, c := range r.Reg.Components() {
		primary := r.primary(c.Path)
		r.wake(c, primary)
		for _, dep := range r.switched(c.Path) {
			if dep != primary {
				r.wake(c, dep)
			}
		}
	}
}

// wake starts deployment dep of c in the background when it has effective
// alwaysOn, may run, and has no generation, build or sticky error.
func (r *Runner) wake(c *registry.Component, dep string) {
	if v := r.alwaysOnView(c, dep); v == nil || !v.HasBackend() {
		return
	}
	if !r.shouldRun(c.Path, dep) {
		return
	}
	var s *state
	if dep == r.primary(c.Path) {
		s = r.stateOf(c.Path, dep) // as Ensure makes it
	} else {
		s = r.existingStateOf(c.Path, dep) // none yet: EnsureDeployment decides whether it may exist
	}
	down := true
	if s != nil {
		s.mu.Lock()
		down = s.cur == nil && !s.building && s.lastErr == nil
		s.mu.Unlock()
	}
	if down {
		go r.aoStart(c, dep)
	}
}

func (r *Runner) aoStart(c *registry.Component, dep string) {
	r.ao.mu.Lock()
	if r.ao.sem == nil {
		r.ao.sem = make(chan struct{}, aoConcurrency)
	}
	sem := r.ao.sem
	r.ao.mu.Unlock()
	sem <- struct{}{}
	defer func() { <-sem }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var err error
	if dep == r.primary(c.Path) {
		_, err = r.Ensure(ctx, c)
	} else {
		_, err = r.EnsureDeployment(ctx, c, dep) // deployment: a non-primary one whose alwaysOn switch is on (07-runtime §11)
	}
	if err != nil {
		slog.Warn("always-on backend did not start", "component", c.Path, "deployment", dep, "err", err)
		return
	}
	r.ao.mu.Lock()
	if r.ao.upSince == nil {
		r.ao.upSince = map[string]time.Time{}
	}
	r.ao.upSince[stateKey(c.Path, dep)] = time.Now()
	r.ao.mu.Unlock()
}

// nextBackoff is the wait before restarting a backend that exited: doubled
// on each exit, back to the minimum once it had run healthily for a while.
func nextBackoff(prev time.Duration, upFor time.Duration) time.Duration {
	if prev == 0 || upFor >= aoHealthyAge {
		return aoBackoffMin
	}
	if prev*2 > aoBackoffMax {
		return aoBackoffMax
	}
	return prev * 2
}

// afterExitOf is the crash watch's hook: a generation of deployment dep of c
// with effective alwaysOn that exited (not replaced, not stopped, not
// crash-looping) comes back after its deployment's backoff.
func (r *Runner) afterExitOf(c *registry.Component, dep string) {
	if r.alwaysOnView(c, dep) == nil {
		return
	}
	k := stateKey(c.Path, dep)
	r.ao.mu.Lock()
	if r.ao.backoff == nil {
		r.ao.backoff, r.ao.pending = map[string]time.Duration{}, map[string]bool{}
	}
	if r.ao.pending[k] {
		r.ao.mu.Unlock()
		return
	}
	var upFor time.Duration
	if t, ok := r.ao.upSince[k]; ok {
		upFor = time.Since(t)
	}
	wait := nextBackoff(r.ao.backoff[k], upFor)
	r.ao.backoff[k] = wait
	r.ao.pending[k] = true
	r.ao.mu.Unlock()
	slog.Info("always-on backend exited; restarting", "component", c.Path, "deployment", dep, "in", wait)
	time.AfterFunc(wait, func() {
		r.ao.mu.Lock()
		delete(r.ao.pending, k)
		r.ao.mu.Unlock()
		if cur, ok := r.Reg.Component(c.Path); ok {
			r.wake(cur, dep)
		}
	})
}
