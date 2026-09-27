package tilesbx

// activity.go — what a sandbox's commands tell the rest of the lifecycle
// (plans/tile-sandbox-runtime.md §7): when it was last used, the work in
// flight that holds its idle stop off (a run, a non-tty exec), and the TTY
// clients attached — WP-15b's idle timer reads them — and the auto-start a
// command waits on (§3.4).
//
// WP-15b owns the idle stop and the auto-start policy; these are the
// narrow seams the command routes (and WP-18's file routes) call, kept
// minimal so its helpers replace their bodies.

import (
	"errors"
	"fmt"
	"sync/atomic"
)

// activity is a sandbox's use, kept across its runs.
type activity struct {
	last     atomic.Int64 // unix ms: an API call, exec output or input, a TTY attach or frame
	inflight atomic.Int64 // runs and non-tty execs under way: the idle stop waits for them
	clients  atomic.Int64 // TTY clients attached (termwire's OnClients, summed)
}

// touch records activity on b now.
func (m *Manager) touch(b *box) {
	if b != nil && b.act != nil {
		b.act.last.Store(m.now().UnixMilli())
	}
}

// hold marks work in flight on b until the returned func is called (once;
// later calls do nothing).
func (m *Manager) hold(b *box) func() {
	b.act.inflight.Add(1)
	m.touch(b)
	return onceFunc(func() {
		b.act.inflight.Add(-1)
		m.touch(b)
	})
}

// lastActive is when b was last used: its last activity, or its last
// run's end.
func (b *box) lastUsed() int64 {
	if b.act == nil {
		return b.lastActive
	}
	return max(b.lastActive, b.act.last.Load())
}

// ensureRunning is k's sandbox's run, for a command: a running sandbox's
// at once; one starting is waited for; a stopped (or stopping) one with
// autoStart is started — the run's timeout doesn't count the start — and
// one without is 409 state. WP-15b's auto-start helper (bounded by
// waitMaxSec, busy states) replaces it.
func (m *Manager) ensureRunning(k Key, name string) (*box, *run, error) {
	for try := 0; ; try++ {
		m.mu.Lock()
		d, ok := m.defs.get(k, name)
		if !ok {
			m.mu.Unlock()
			return nil, nil, refuse(RefNotFound, "no sandbox %q", name)
		}
		b := m.boxLocked(k, name)
		if b.run != nil && b.state == StateRunning {
			r := b.run
			m.mu.Unlock()
			return b, r, nil
		}
		state, detail := b.state, b.detail
		m.mu.Unlock()
		switch {
		case try > 0 || state == StateCreating || state == StateError:
			msg := fmt.Sprintf("sandbox %q is %s", name, state)
			if detail != "" {
				msg += ": " + detail
			}
			return nil, nil, &Error{Refusal: RefState, State: state, Msg: msg}
		case state == StateStarting && !d.AutoStart:
			b.flight.Lock() // the start in flight holds it
			b.flight.Unlock()
			continue
		case !d.AutoStart:
			return nil, nil, &Error{Refusal: RefState, State: state,
				Msg: fmt.Sprintf("sandbox %q is %s (autoStart is off): start it first", name, state)}
		}
		if err := m.Start(k, name); err != nil {
			var e *Error
			if errors.As(err, &e) {
				return nil, nil, e
			}
			return nil, nil, &Error{Refusal: RefState, State: StateStopped,
				Msg: fmt.Sprintf("sandbox %q didn't start: %v", name, err)}
		}
	}
}
