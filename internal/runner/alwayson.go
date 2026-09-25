package runner

// alwayson.go — backends that stay up ("alwaysOn": true in xbin.json). An
// ordinary backend starts on its first proxied request and is reaped when
// idle; a tile that holds a connection open (a chat adapter's socket) has no
// inbound requests to start it or keep it alive. So: start it at boot and
// whenever it becomes runnable (enable, vault unseal, the flag appearing on a
// rescan), skip it in the idle reaper, and after an exit restart it once a
// backoff has passed — 1 s doubling to 5 min, back to 1 s after 10 healthy
// minutes. The crash-loop breaker still wins: a backend that keeps dying
// stays down until a save. Every start is event-driven (a one-shot timer
// after an exit); nothing polls.

import (
	"context"
	"log/slog"
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

type alwaysOn struct {
	mu      sync.Mutex
	backoff map[string]time.Duration
	upSince map[string]time.Time
	pending map[string]bool // a restart is scheduled
	sem     chan struct{}
}

func (r *Runner) isAlwaysOn(comp string) bool {
	c, ok := r.Reg.Component(comp)
	return ok && c.Manifest.AlwaysOn
}

// WakeAlwaysOn starts every alwaysOn backend that may run and isn't running.
// Idempotent; call it whenever something may have made one runnable.
func (r *Runner) WakeAlwaysOn() {
	for _, c := range r.Reg.Components() {
		if !c.Manifest.AlwaysOn || !c.HasBackend() || c.Manifest.Runtime == "cgi" {
			continue
		}
		if r.ShouldRun != nil && !r.ShouldRun(c.Path) {
			continue
		}
		s := r.state(c.Path)
		s.mu.Lock()
		down := s.cur == nil && !s.building && s.lastErr == nil
		s.mu.Unlock()
		if down {
			go r.aoStart(c)
		}
	}
}

func (r *Runner) aoStart(c *registry.Component) {
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
	if _, err := r.Ensure(ctx, c); err != nil {
		slog.Warn("always-on backend did not start", "component", c.Path, "err", err)
		return
	}
	r.ao.mu.Lock()
	if r.ao.upSince == nil {
		r.ao.upSince = map[string]time.Time{}
	}
	r.ao.upSince[c.Path] = time.Now()
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

// afterExit is the crash watch's hook: an alwaysOn backend that exited (not
// replaced, not stopped, not crash-looping) comes back after its backoff.
func (r *Runner) afterExit(c *registry.Component) {
	if !c.Manifest.AlwaysOn {
		return
	}
	r.ao.mu.Lock()
	if r.ao.backoff == nil {
		r.ao.backoff, r.ao.pending = map[string]time.Duration{}, map[string]bool{}
	}
	if r.ao.pending[c.Path] {
		r.ao.mu.Unlock()
		return
	}
	var upFor time.Duration
	if t, ok := r.ao.upSince[c.Path]; ok {
		upFor = time.Since(t)
	}
	wait := nextBackoff(r.ao.backoff[c.Path], upFor)
	r.ao.backoff[c.Path] = wait
	r.ao.pending[c.Path] = true
	r.ao.mu.Unlock()
	slog.Info("always-on backend exited; restarting", "component", c.Path, "in", wait)
	time.AfterFunc(wait, func() {
		r.ao.mu.Lock()
		delete(r.ao.pending, c.Path)
		r.ao.mu.Unlock()
		cur, ok := r.Reg.Component(c.Path)
		if !ok || !cur.Manifest.AlwaysOn || (r.ShouldRun != nil && !r.ShouldRun(c.Path)) {
			return
		}
		s := r.state(c.Path)
		s.mu.Lock()
		down := s.cur == nil && !s.building && s.lastErr == nil
		s.mu.Unlock()
		if down {
			r.aoStart(cur)
		}
	})
}
