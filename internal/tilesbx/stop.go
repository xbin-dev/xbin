package tilesbx

// stop.go — how runs end when asked (plans/tile-sandbox-runtime.md §7): a
// stop of one sandbox, stops over a set (StopWhere, StopTile, StopAll: the
// hooks' — a revoke, a seal, a shutdown, the kill switch), and the egress
// reconcile a sandbox-net change triggers (§4; a rescan and a users event
// run it too, reconcile.go). Each goes through the sandbox's flight and
// ends in the one teardown (lifecycle.go).

import (
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"
)

// Stop ends k's sandbox's run, keeping its state (§7): its agent syncs
// (at most stopSync), then it is killed, and Stop returns once the
// teardown finished — at most endWait. why is the stateDetail it leaves
// ("" for a stop the manager asked for). A stopped sandbox is left alone.
func (m *Manager) Stop(k Key, name, why string) error {
	m.mu.Lock()
	b := m.live[k][name]
	m.mu.Unlock()
	if b == nil {
		return nil // never ran since xbind started
	}
	b.flight.Lock()
	defer b.flight.Unlock()
	return m.stopLocked(b, why)
}

// stopLocked is Stop inside the flight.
func (m *Manager) stopLocked(b *box, why string) error {
	m.mu.Lock()
	r := b.run
	if r != nil {
		b.state = StateStopping
	}
	m.mu.Unlock()
	if r == nil {
		return nil
	}
	return m.stopRun(r, why)
}

// stopRun ends r the way its mode stops (namespace: the agent syncs, at
// most stopSync, then the sandbox is killed) and waits for its teardown.
func (m *Manager) stopRun(r *run, why string) error {
	if r.ops.stop != nil {
		r.ops.stop(m, r, why)
	} else {
		m.syncThenEnd(r, why)
	}
	select {
	case <-r.done:
		return nil
	case <-time.After(m.endWait):
		return &Error{Refusal: RefUnavailable, Msg: fmt.Sprintf("sandbox %q didn't end within %s: it is still stopping", r.def.Name, m.endWait), RetryAfter: 5 * time.Second}
	}
}

// syncThenEnd is namespace mode's stop: a sync the agent answers (at most
// stopSync — a stopped fuse-overlayfs can wedge it), then the kill. A run
// already ending isn't synced again.
func (m *Manager) syncThenEnd(r *run, why string) {
	r.mu.Lock()
	asked, agent := r.asked, r.agent
	r.mu.Unlock()
	if !asked && agent != nil {
		if err := agent.Sync(stopSync); err != nil {
			slog.Info("tile sandbox: stopping without a sync", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
		}
	}
	m.end(r, why)
}

// running is every run up now, with its key, name and definition as
// launched, pred picks (nil: all).
func (m *Manager) running(pred func(Key, *Def) bool) []*run {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*run
	for _, boxes := range m.live {
		for _, b := range boxes {
			if r := b.run; r != nil && (pred == nil || pred(r.k, r.def.clone())) {
				out = append(out, r)
			}
		}
	}
	return out
}

// StopWhere stops, in parallel, every sandbox pred picks (given its key
// and definition — as launched, or as defined for one not up yet) that
// runs or is starting, and returns once they all ended. A start is seen
// from the moment it is starting (startLocked sets it under m.mu, before
// its gates: the tile's reach, admission, the disk, the mounts): it is
// marked (box.stopAsk) and ends before it can come up — at once when its
// process runs — so a revoke, a disable, a removal, a seal or a low disk
// never misses one that passed its checks just before. Each stop is
// idempotent; why is the stateDetail they are left with.
func (m *Manager) StopWhere(pred func(Key, *Def) bool, why string) {
	type target struct {
		k    Key
		name string
	}
	var targets []target
	var starting []*run
	m.mu.Lock()
	for k, boxes := range m.live {
		for name, b := range boxes {
			d := (*Def)(nil)
			switch {
			case b.run != nil:
				d = b.run.def
			case b.state == StateStarting:
				d, _ = m.defs.get(k, name)
			}
			if d == nil || (pred != nil && !pred(k, d.clone())) {
				continue
			}
			targets = append(targets, target{k, name})
			if b.state == StateStarting {
				if b.stopAsk == "" {
					b.stopAsk = why
				}
				if b.run != nil {
					starting = append(starting, b.run)
				}
			}
		}
	}
	m.mu.Unlock()
	for _, r := range starting { // its process runs: its start fails at once
		m.end(r, why)
	}
	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := m.Stop(t.k, t.name, why); err != nil {
				slog.Warn("tile sandbox: stop", "tile", t.k.Tile, "sandbox", t.name, "err", err)
			}
		}()
	}
	wg.Wait()
}

// stopRuns stops the sandboxes of runs in parallel, and returns once they
// all ended.
func (m *Manager) stopRuns(runs []*run, why string) {
	var wg sync.WaitGroup
	for _, r := range runs {
		wg.Add(1)
		go func(r *run) {
			defer wg.Done()
			if err := m.Stop(r.k, r.def.Name, why); err != nil {
				slog.Warn("tile sandbox: stop", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
			}
		}(r)
	}
	wg.Wait()
}

// StopTile stops every sandbox of tile (all its deployments) that runs or
// is starting, and returns once they ended — at once when none does. Safe
// from any hook.
func (m *Manager) StopTile(tile, why string) {
	m.StopWhere(func(k Key, _ *Def) bool { return k.Tile == tile }, why)
}

// StopAll stops every tile sandbox, synced, within endWait in all: xbind is
// shutting down (its exit would kill them anyway, unsynced).
func (m *Manager) StopAll(why string) {
	m.closeUsage() // no confined du starts while xbind exits
	done := make(chan struct{})
	go func() { m.StopWhere(nil, why); close(done) }()
	select {
	case <-done:
	case <-time.After(m.endWait):
		slog.Warn("tile sandboxes: some didn't stop in time; xbind's exit ends them")
	}
}

// OnSandboxNetChange is the broker's hook (§4): a tile's sandbox-net
// classes may resolve differently now. It returns at once; each running
// sandbox of the tile is re-resolved off the caller's goroutine.
func (m *Manager) OnSandboxNetChange(tile string) { go m.reconcileEgress(tile) }

// reconcileEgress re-resolves each running sandbox of tile with the class
// it runs: rules that no longer cover the running ones (the relay can't cut
// flows it admitted) stop it, state kept, with why; wider rules wait for
// the next start (egressNext).
func (m *Manager) reconcileEgress(tile string) {
	for _, r := range m.running(func(k Key, _ *Def) bool { return k.Tile == tile }) {
		_, pol, err := m.egress(r.k, r.def.Net.Egress)
		switch {
		case err != nil || !pol.Covers(r.pol):
			why := fmt.Sprintf("its network (%s) narrowed: stopped, state kept — start it again to run under the new rules", r.def.Net.Egress)
			if err != nil {
				why = fmt.Sprintf("its network (%s) is gone: stopped, state kept — %v", r.def.Net.Egress, err)
			}
			go func(r *run) { _ = m.Stop(r.k, r.def.Name, why) }(r)
		case !r.pol.Covers(pol):
			m.mu.Lock()
			if r.b.run == r {
				r.b.egressNext = true
			}
			m.mu.Unlock()
		}
	}
}

// switchedOff stops every tile sandbox running or starting, state kept:
// the policy's kill switch went off (§7 "Policy flips"). It returns at
// once.
func (m *Manager) switchedOff(why string) {
	if len(m.activeTiles()) > 0 {
		go m.StopWhere(nil, why)
	}
}

// activeTiles are the tiles with a sandbox running or starting, sorted:
// what a reconcile walks (a start is seen from the moment it is starting;
// StopWhere).
func (m *Manager) activeTiles() []string {
	m.mu.Lock()
	seen := map[string]bool{}
	for k, boxes := range m.live {
		for _, b := range boxes {
			if b.run != nil || b.state == StateStarting {
				seen[k.Tile] = true
			}
		}
	}
	m.mu.Unlock()
	out := make([]string, 0, len(seen))
	for t := range seen {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}
