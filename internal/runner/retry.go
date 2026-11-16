package runner

// retry.go — a failed start that can clear by itself is tried again (D242).
//
// A start failure was sticky until the next save: a backend that missed its
// health deadline once — a slow disk under its first write, a slow answer —
// stayed failed for days. Now a start that failed for a reason that can
// clear by itself (retryable) is started again after 10 s, 1 min and 5 min,
// then left failed with its last reason, as before. Each retry is a new
// generation through runCurrent, exactly as a request after a save starts
// one: the code the record names, its build, its launch spec. The timer is
// the alwaysOn backoff's mechanism (a one-shot AfterFunc per deployment,
// event-driven, nothing polls), on the engine's seam so the tests fire it.
// A save, a grant, a deploy or restart and a healthy start end the series
// (resetRetry); a stop and xbind's shutdown cancel the pending one. People's
// partitions are not timed: their next request already tries again
// (sticky, partstop.go).

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

const startRetries = 3

// startRetryDelays is the wait before each retry of a failed start.
var startRetryDelays = [startRetries]time.Duration{10 * time.Second, time.Minute, 5 * time.Minute}

// startRetry is a deployment's retries of its failed start.
type startRetry struct {
	n      int         // retries scheduled since the series began
	exited bool        // the last failed start's process exited by itself
	due    time.Time   // when the pending retry starts; zero: none pending
	stop   func() bool // the pending retry's timer
	epoch  int         // bumped by every reset: an older timer does nothing
}

// StartRetry is a pending retry of a failed start, as the status reports it
// (the row's state stays "failed").
type StartRetry struct {
	Attempt int    `json:"attempt"` // which retry is pending, 1…Of
	Of      int    `json:"of"`
	At      string `json:"at"`    // when it starts, RFC 3339
	InSec   int64  `json:"inSec"` // seconds until then
}

// transientErrnos are launch failures a moment later may not meet: a
// resource briefly short (processes, memory, file descriptors) or the
// binary still open for writing.
var transientErrnos = []error{syscall.EAGAIN, syscall.ENOMEM, syscall.EMFILE, syscall.ENFILE, syscall.ETXTBSY}

// retryable says whether a start that failed with err can clear by itself:
// the process was alive at the health deadline (it never listened, or never
// accepted — also when it was waiting on xbind), it exited before it
// listened and the start before it in this series didn't (exited), or the
// launch met a transient errno. A build error, a manifest error, a refusal
// by policy, a hold or a limit, and anything else a person must fix, are
// not.
func retryable(err error, exited bool) bool {
	var be *BuildError
	switch {
	case err == nil || errors.As(err, &be):
		return false
	case errors.Is(err, errNeverListened), errors.Is(err, errNeverAccepted):
		return true
	case errors.Is(err, errExited):
		return !exited
	}
	for _, e := range transientErrnos {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}

// startEnded is ensureState's, after the start it took ended in err: a
// healthy start ends the series; a failure that can clear by itself, while
// retries remain, schedules the next one, writes one line into the
// deployment's log and announces it. epoch is the series' when that start
// began: after a reset since (a save, a deploy, a stop) the failure is left
// to whatever reset it.
func (r *Runner) startEnded(s *state, err error, epoch int) {
	if s.pt != nil {
		return
	}
	s.mu.Lock()
	if err == nil {
		resetRetryLocked(s)
		s.mu.Unlock()
		return
	}
	if s.retry.epoch != epoch || s.dirty || s.building || s.gone {
		s.mu.Unlock()
		return // something moved on since: a save, a deploy, a stop
	}
	rt := &s.retry
	if !retryable(err, rt.exited) || rt.n >= startRetries {
		last := rt.n >= startRetries && retryable(err, rt.exited)
		resetRetryLocked(s)
		s.mu.Unlock()
		if last {
			r.retryLog(s, fmt.Sprintf("the start failed again after %d retries: it stays failed until a save, a restart or a deploy", startRetries))
		}
		return
	}
	d := startRetryDelays[rt.n]
	rt.n++
	rt.exited = errors.Is(err, errExited)
	rt.due = r.now().Add(d)
	n := rt.n
	rt.stop = r.afterFunc(d, func() { r.retryStart(s, epoch) })
	info := retryInfoLocked(s, r.now())
	s.mu.Unlock()
	r.retryLog(s, fmt.Sprintf("the start failed; retrying in %s (%d of %d)", d, n, startRetries))
	if s.dep == r.primary(s.comp) && r.Hub != nil {
		r.Hub.Publish(events.Event{Type: "backend-retry", Component: s.comp, Data: info})
	}
}

// retryStart is a pending retry come due: unless the series was reset
// meanwhile, or the deployment builds, went, may not run (disabled, held,
// sealed) or xbind is shutting down, it starts a new generation as a
// request after a save would.
func (r *Runner) retryStart(s *state, epoch int) {
	s.mu.Lock()
	if s.retry.epoch != epoch || s.gone || s.building || s.lastErr == nil {
		s.mu.Unlock()
		return
	}
	s.retry.due, s.retry.stop = time.Time{}, nil
	s.mu.Unlock()
	c, ok := r.Reg.Component(s.comp)
	if !ok || !c.HasBackend() || !r.shouldRun(s.comp, s.dep) || r.live.isStopping() {
		r.resetRetry(s)
		return
	}
	s.mu.Lock()
	if s.retry.epoch != epoch || s.building {
		s.mu.Unlock()
		return
	}
	s.dirty = true // the start a request would take now
	s.mu.Unlock()
	r.ensureRetry(c, s.dep)
}

// ensureRetry starts deployment dep of c through the path a request takes.
func (r *Runner) ensureRetry(c *registry.Component, dep string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	_, _ = r.EnsureDeployment(ctx, c, dep) // deployment: the one whose start failed (the primary's included)
}

// resetRetry ends s's series: the pending retry, if any, never starts, and
// the next failure begins a new one.
func (r *Runner) resetRetry(s *state) {
	s.mu.Lock()
	resetRetryLocked(s)
	s.mu.Unlock()
}

func resetRetryLocked(s *state) {
	if s.retry.stop != nil {
		s.retry.stop()
	}
	s.retry = startRetry{epoch: s.retry.epoch + 1}
}

// retryInfoLocked is s's pending retry for a status row; nil without one.
func retryInfoLocked(s *state, now time.Time) *StartRetry {
	rt := s.retry
	if rt.due.IsZero() {
		return nil
	}
	in := rt.due.Sub(now)
	if in < 0 {
		in = 0
	}
	return &StartRetry{Attempt: rt.n, Of: startRetries, At: rt.due.UTC().Format(time.RFC3339), InSec: int64((in + time.Second - 1) / time.Second)}
}

// retryLog appends one line about s's retries to its deployment's log.
func (r *Runner) retryLog(s *state, line string) {
	if r.Root == "" {
		return
	}
	p := filepath.Join(r.Root, filepath.FromSlash(deploymentLog(s.comp, s.dep)))
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644) // a start wrote it: never created here
	if err != nil {
		return
	}
	fmt.Fprintf(f, "--- %s %s ---\n", line, r.now().UTC().Format(time.RFC3339))
	f.Close()
}
