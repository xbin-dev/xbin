package tilesbx

// idle.go — the idle stop (plans/tile-sandbox-runtime.md §7). Every
// activity on a running sandbox records a time: an API call on it, exec
// output or input, a TTY attach, detach or frame, a file operation. One
// time.AfterFunc per run, armed when it reaches running, fires after the
// sandbox's idleStopMin and re-arms itself for whatever remains of the
// interval since the last activity (no tickers). When it fires on a quiet
// sandbox, the sandbox stops (state kept) — unless work is in flight: a
// non-tty exec, a run, a file, tar or copy operation (a quiet `run` longer
// than idleStopMin, or a long tar stream, is work, not idleness), or a TTY
// client attached. Each of those takes a hold for its duration (hold);
// the timer then re-arms for a full interval. A detached TTY exec with no
// traffic holds nothing: it is idle.
//
// The execs, files and TTY routes (WP-17, WP-18) take their holds through
// acquire (autostart.go), hold and ttyClients, and record activity with
// touch.

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// stopper is a timer that can be stopped (time.Timer; a test clock's).
type stopper interface{ Stop() bool }

// idleState is a run's idle bookkeeping. holds, timer and off are guarded
// by m.mu; last is read and written without it (output may touch it often).
type idleState struct {
	last  atomic.Int64 // unix ms of the last activity
	holds int          // work in flight: while any, the sandbox isn't idle
	timer stopper
	off   bool // the run ended (or is ending): the timer is never armed again
}

// idleMinutes is how long a sandbox may sit idle: its definition's
// idleStopMin (≤ 1440), or the policy's when it has none (0) — the
// policy's is the default, as WP-13 stored and answered it.
func idleMinutes(d *Def, lim Limits) int {
	if d.IdleStopMin <= 0 {
		return lim.IdleStopMin
	}
	return d.IdleStopMin
}

// idleForLocked is r's idle interval now: its definition as it stands (a
// PATCH applies to a running sandbox) under the tile's limits now.
// Callers hold m.mu.
func (m *Manager) idleForLocked(r *run) time.Duration {
	d := r.def
	if cd, ok := m.defs.get(r.k, r.def.Name); ok && cd.UID == r.def.UID {
		d = cd
	}
	return time.Duration(idleMinutes(d, m.limitsFor(r.k.Tile))) * time.Minute
}

// touch records activity on r now.
func (m *Manager) touch(r *run) { r.idle.last.Store(m.now().UnixMilli()) }

// touchBox records activity on k's sandbox, if it runs: an API call on it.
func (m *Manager) touchBox(k Key, name string) {
	m.mu.Lock()
	var r *run
	if b := m.live[k][name]; b != nil {
		r = b.run
	}
	m.mu.Unlock()
	if r != nil {
		m.touch(r)
	}
}

// hold marks work in flight on r's sandbox — a non-tty exec, a run, a
// file, tar or copy operation, an attached TTY client — and returns its
// idempotent release; taking and releasing it are activity. The idle stop
// never stops a sandbox a hold is on. ok is false when r isn't running
// (any more): it is stopping, and the caller waits for it or starts it
// again (acquire).
func (m *Manager) hold(r *run) (release func(), ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.holdLocked(r)
}

// holdLocked is hold. Callers hold m.mu.
func (m *Manager) holdLocked(r *run) (func(), bool) {
	if r == nil || r.b.run != r || r.b.state != StateRunning || r.idle.off {
		return nil, false
	}
	r.idle.holds++
	m.touch(r)
	return onceFunc(func() {
		m.mu.Lock()
		r.idle.holds--
		m.mu.Unlock()
		m.touch(r)
	}), true
}

// ttyClients is a TTY exec's hub's OnClients callback (termwire): while
// any client is attached the sandbox holds (hold); an attach and a detach
// are activity.
func (m *Manager) ttyClients(r *run) func(n int) {
	var mu sync.Mutex
	var release func()
	return func(n int) {
		mu.Lock()
		defer mu.Unlock()
		m.touch(r)
		switch {
		case n > 0 && release == nil:
			if rel, ok := m.hold(r); ok {
				release = rel
			}
		case n <= 0 && release != nil:
			release()
			release = nil
		}
	}
}

// armIdleLocked arms r's idle timer for d (a full interval when d is 0).
// Callers hold m.mu.
func (m *Manager) armIdleLocked(r *run, d time.Duration) {
	if r.idle.off {
		return
	}
	if r.idle.timer != nil {
		r.idle.timer.Stop()
	}
	if d <= 0 {
		d = m.idleForLocked(r)
	}
	r.idle.timer = m.afterFunc(d, func() { m.idleFired(r) })
}

// disarmIdle ends r's idle timer for good: its teardown.
func (m *Manager) disarmIdle(r *run) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.idle.off = true
	if r.idle.timer != nil {
		r.idle.timer.Stop()
		r.idle.timer = nil
	}
}

// idleLeftLocked is how long r has left before it is idle: 0 when it is,
// a full interval while work is in flight. Callers hold m.mu.
func (m *Manager) idleLeftLocked(r *run) time.Duration {
	interval := m.idleForLocked(r)
	if r.idle.holds > 0 {
		return interval
	}
	left := time.UnixMilli(r.idle.last.Load()).Add(interval).Sub(m.now())
	return max(left, 0)
}

// idleFired is r's timer: re-armed for what remains, or — the sandbox
// idle — a stop, state kept.
func (m *Manager) idleFired(r *run) {
	m.mu.Lock()
	if r.idle.off || r.b.run != r {
		m.mu.Unlock()
		return
	}
	if left := m.idleLeftLocked(r); left > 0 {
		m.armIdleLocked(r, left)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	m.idleStop(r)
}

// idleStop stops r for being idle, in its sandbox's flight, after looking
// again: an exec may have come in while the flight was busy. Holds are
// taken under m.mu while the sandbox is running, and the state turns
// stopping under m.mu here, so none is taken once the stop is decided — a
// call arriving now waits for the stop and starts it again (acquire).
func (m *Manager) idleStop(r *run) {
	b := r.b
	b.flight.Lock()
	defer b.flight.Unlock()
	m.mu.Lock()
	if r.idle.off || b.run != r || b.state != StateRunning {
		m.mu.Unlock()
		return
	}
	if left := m.idleLeftLocked(r); left > 0 {
		m.armIdleLocked(r, left)
		m.mu.Unlock()
		return
	}
	mins := int(m.idleForLocked(r) / time.Minute)
	b.state = StateStopping
	m.mu.Unlock()
	why := fmt.Sprintf("idle for %d minutes: stopped, state kept (idleStopMin)", mins)
	if err := m.stopLocked(b, why); err != nil {
		slog.Warn("tile sandbox: the idle stop", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
	}
}

// rearmIdle re-arms the idle timer of every running sandbox pred picks
// (nil: all) for what remains under its idleStopMin now: a PATCH or a
// policy change applies to running sandboxes at once, not after the
// interval they were armed with.
func (m *Manager) rearmIdle(pred func(Key, *Def) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rearmIdleLocked(pred)
}

// rearmIdleLocked is rearmIdle. Callers hold m.mu.
func (m *Manager) rearmIdleLocked(pred func(Key, *Def) bool) {
	for _, boxes := range m.live {
		for _, b := range boxes {
			r := b.run
			if r == nil || r.idle.off || r.idle.timer == nil || (pred != nil && !pred(r.k, r.def)) {
				continue
			}
			m.armIdleLocked(r, max(m.idleLeftLocked(r), time.Millisecond))
		}
	}
}

// lastActiveLocked is when k's sandbox was last active (unix ms): its
// run's last activity while it runs. Callers hold m.mu.
func lastActiveLocked(b box) int64 {
	if b.run != nil {
		if t := b.run.idle.last.Load(); t > 0 {
			return t
		}
	}
	return b.lastActive
}
