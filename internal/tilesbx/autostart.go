package tilesbx

// autostart.go — calls that need a running sandbox, and how long a call
// waits for a transition (plans/tile-sandbox-runtime.md §3.4, §7). An exec,
// a run or a file operation on a stopped sandbox with autoStart starts it
// and waits for it; on a starting one it waits for running; on a stopping
// one it waits for the stop to finish, then starts it again — all within
// waitMaxSec. Without autoStart a stopped or stopping sandbox answers 409
// state. A lifecycle call's ?wait bounds how long it waits for its own
// transition (within); the transition goes on after the answer.

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// minWait is the least a call waits for its transition, ?wait=0 included:
// long enough for one that needn't wait for anything to take its flight
// and say so (starting, stopping), or to be refused.
const minWait = 50 * time.Millisecond

// within runs f off the caller's goroutine and waits at most wait (at
// least minWait) for it. done is false when the wait ran out: f goes on.
func within(wait time.Duration, f func() error) (done bool, err error) {
	ch := make(chan error, 1)
	go func() { ch <- f() }()
	t := time.NewTimer(max(wait, minWait))
	defer t.Stop()
	select {
	case err := <-ch:
		return true, err
	case <-t.C:
		return false, nil
	}
}

// waitOf is a call's ?wait=<seconds>: waitMaxSec when absent, clamped to
// it (a parameter over its limit is clamped, never refused — the
// contract), 400 invalid when it isn't a whole number of seconds.
func waitOf(r *http.Request) (time.Duration, error) {
	v := r.URL.Query().Get("wait")
	if v == "" {
		return waitMaxSec * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, refuse(RefInvalid, "?wait=%q must be a whole number of seconds (≤ %d)", v, waitMaxSec)
	}
	return time.Duration(min(n, waitMaxSec)) * time.Second, nil
}

// waitFlight waits, at most d, until nothing holds b's flight: the start,
// stop, reset or rebase in it has finished.
func waitFlight(b *box, d time.Duration) bool {
	done := make(chan struct{})
	go func() {
		b.flight.Lock()
		b.flight.Unlock() // an empty critical section: only waiting for the holder
		close(done)
	}()
	t := time.NewTimer(max(d, time.Millisecond))
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

// acquire readies k's sandbox for an exec, a run or a file operation — the
// execs, files and TTY routes call it (WP-17, WP-18) — and returns its run
// with a hold on it (idle.go), which the caller releases when its work
// ends: a non-tty exec or a run when it exits, a file, tar or copy
// operation when it is done, a TTY exec once it started (its attached
// clients hold after that: ttyClients). wait bounds the wait for a
// transition (waitMaxSec, normally); the work's own timeout starts after
// it. A sandbox that can't be readied answers 409 state (not-found for
// none; a start's refusal as itself).
func (m *Manager) acquire(k Key, name string, wait time.Duration) (*run, func(), error) {
	deadline := time.Now().Add(wait)
	started := false
	for {
		m.mu.Lock()
		d, ok := m.defs.get(k, name)
		if !ok {
			m.mu.Unlock()
			return nil, nil, refuse(RefNotFound, "no sandbox %q", name)
		}
		b := m.boxLocked(k, name)
		if r := b.run; r != nil && b.state == StateRunning {
			if release, ok := m.holdLocked(r); ok {
				m.mu.Unlock()
				return r, release, nil
			}
		}
		st, detail := b.state, b.detail
		m.mu.Unlock()
		left := time.Until(deadline)
		if left <= 0 {
			return nil, nil, &Error{Refusal: RefState, State: st, RetryAfter: time.Second,
				Msg: fmt.Sprintf("sandbox %q is still %s after %s: try again", name, st, wait.Round(time.Second))}
		}
		switch st {
		case StateError, StateCreating:
			return nil, nil, &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q is %s: %s", name, st, detail)}
		case StateRunning: // its run is ending: the teardown is on its way
			time.Sleep(10 * time.Millisecond)
		case StateStarting:
			waitFlight(b, left)
		case StateStopping:
			if !d.AutoStart {
				return nil, nil, &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q is stopping (autoStart is off): start it again once it stopped", name), RetryAfter: time.Second}
			}
			waitFlight(b, left)
		default: // stopped
			if !d.AutoStart {
				return nil, nil, &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q is stopped (autoStart is off): start it first", name)}
			}
			if started { // it ended as soon as it started
				return nil, nil, &Error{Refusal: RefState, State: st, Msg: fmt.Sprintf("sandbox %q stopped again as it started: %s", name, detail)}
			}
			started = true
			done, err := within(left, func() error { return m.Start(k, name) })
			var e *Error
			switch {
			case !done:
				continue // the deadline answers
			case errors.As(err, &e):
				return nil, nil, e
			case err != nil:
				return nil, nil, &Error{Refusal: RefState, State: StateStopped, Msg: fmt.Sprintf("sandbox %q didn't start: %v", name, err)}
			}
		}
	}
}
