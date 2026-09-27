package runner

// states.go — stopping backends (one, all, idle ones) and reporting each
// component's backend state.

import (
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
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
		r.stop(inst, 5*time.Second)
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
			go func() { defer wg.Done(); r.stop(inst, 5*time.Second) }()
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

func (r *Runner) reaper() {
	for range time.Tick(time.Minute) {
		r.mu.Lock()
		states := make([]*state, 0, len(r.states))
		for _, s := range r.states {
			states = append(states, s)
		}
		r.mu.Unlock()
		for _, s := range states {
			s.mu.Lock()
			if s.cur != nil && s.active == 0 && time.Since(s.lastReq) > idleReap && !r.isAlwaysOn(s.comp) {
				inst := s.cur
				s.cur = nil
				s.dirty = true // next request restarts lazily
				s.mu.Unlock()
				slog.Info("reaping idle backend", "component", s.comp)
				go r.stop(inst, 5*time.Second)
				continue
			}
			s.mu.Unlock()
		}
	}
}
