package runner

// changed.go — what a change asks of the runner (Changed, ChangedSaved):
// mark the primary dirty and rebuild it in the background, unless the
// generation building or running already read the saves (moved out of
// runner.go: its size budget).

import (
	"context"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// Changed marks a component dirty and kicks a background rebuild so build
// errors surface on save, not on next request.
func (r *Runner) Changed(c *registry.Component) { r.changedAt(c, time.Time{}) }

// ChangedSaved is ChangedDeployment for a batch of saves whose last landed
// at saved (the watcher's, watch.Event.Last). A build of c's primary that
// began after it read those files already, so it is left to finish, or to
// keep running, instead of being followed by a rebuild of the same files:
// a backend started on demand while the batch was still being handled
// (its rescan made the tile visible before the batch reached the runner —
// a new tile's first request, a bus push starting it) once ran twice, the
// second generation replacing the first a moment after it had answered.
// A tile with people's partitions is always rebuilt (their shared build
// follows every change), as is a deployment other than the primary.
func (r *Runner) ChangedSaved(c *registry.Component, dep string, saved time.Time) {
	if dep != r.primary(c.Path) {
		r.ChangedDeployment(c, dep)
		return
	}
	r.changedAt(c, saved)
}

// changedAt is Changed for saves made before saved (zero: unknown, always
// rebuilt).
func (r *Runner) changedAt(c *registry.Component, saved time.Time) {
	if !c.HasBackend() {
		return
	}
	if !saved.IsZero() && r.hasRead(c, saved) {
		return
	}
	r.nextBuild(c.Path) // a partitioned tile's shared build is a new one, before the primary's (partadmit.go)
	s := r.state(c.Path)
	s.mu.Lock()
	s.dirty = true
	s.crashes = nil
	resetRetryLocked(s) // retry.go: a save starts once, now
	hadProcess := s.cur != nil || s.building || s.lastErr != nil
	s.mu.Unlock()
	// Don't respawn a disabled/offloaded component on a file change (Ensure would
	// refuse anyway; this just skips the pointless goroutine + build).
	restart := hadProcess && (r.ShouldRun == nil || r.ShouldRun(c.Path))
	done := r.changedPartitions(c, restart) // people's partitions follow, onto the same build (partstop.go)
	if restart {
		go func() {
			defer done()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, _ = r.Ensure(ctx, c)
		}()
	}
}

// hasRead reports whether c's primary already builds or runs the work tree
// as it stood at saved: its latest build began after it, nothing marked it
// dirty since, and the tile has no partitions (whose builds follow every
// change).
func (r *Runner) hasRead(c *registry.Component, saved time.Time) bool {
	if _, ok := r.partitionSpec(c.Path); ok || len(r.partStates(c.Path)) > 0 {
		return false
	}
	s := r.existingStateOf(c.Path, r.primary(c.Path))
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.dirty && (s.building || s.cur != nil) && !s.readAt.IsZero() && s.readAt.After(saved)
}
