package runner

// partstop.go — how people's partitions stop and follow the tile
// (plans/partitions/03 §A.7-§A.8, 01 §6, 02 §2): the stops, which revoke
// every instance token of a partition before they return — the installed
// generation's and one still spawning (partstart.go) — the mode transitions,
// and the restarts after a change of the tile's code.

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// partitionWakeDelay is how long after a mode change an alwaysOn global
// instance is woken: past the publish of the scan that changed it
// (PartitionsChanged runs before it).
const partitionWakeDelay = aoBackoffMin

// stopPart stops partition state s and forgets it: it is marked gone, so
// no build of it spawns again (partBegin) and a spawn in flight stops what
// it spawned (partSpawned); every instance token it holds — the installed
// generation's, a spawned one's not yet installed, one registered mid-spawn
// — is revoked before this returns, so none authenticates from then on.
// The processes drain in the background, or, with wait, in this call, which
// then returns only once no process of the state runs (a spawn in flight
// included) and its run dir is gone. Data is never touched.
func (r *Runner) stopPart(s *state, wait bool) {
	r.mu.Lock()
	if k := partStateKey(s.comp, s.dep, s.pt.pkey); r.parts.states[k] == s {
		delete(r.parts.states, k)
	}
	r.mu.Unlock()
	s.mu.Lock()
	s.gone = true
	var insts []*instance
	for _, inst := range []*instance{s.cur, s.pt.starting} {
		if inst != nil && alive(inst) {
			insts = append(insts, inst)
		}
	}
	s.cur, s.pt.starting = nil, nil
	tok, spawning := s.pt.token, s.pt.spawning
	s.pt.token = ""
	s.mu.Unlock()
	r.revoke(tok)
	for _, inst := range insts {
		r.revoke(inst.token)
	}
	drain := func() {
		for _, inst := range insts {
			r.stopGen(inst, 5*time.Second)
		}
		if spawning != nil {
			<-spawning // the spawn in flight stops its own generation (partSpawned)
		}
		r.removePartDir(s)
	}
	if wait {
		drain()
	} else {
		go drain()
	}
}

// revoke revokes an instance token, if any.
func (r *Runner) revoke(token string) {
	if token != "" && r.Auth != nil {
		r.Auth.RevokeInstance(token)
	}
}

// removePartDir removes stopped partition state s's run dir, unless a new
// state of the same partition exists by now: a spawn makes the dir only
// after its state is in the map, so the check under r.mu can't race one.
// Its log stays: that is the person's (F13a's wipe and the people hooks
// remove .xbin/partition/<TileKey>/<dep>/<pkey>/).
func (r *Runner) removePartDir(s *state) {
	if r.RunDir == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, again := r.parts.states[partStateKey(s.comp, s.dep, s.pt.pkey)]; !again {
		_ = os.RemoveAll(filepath.Join(r.RunDir, partSockDir(s.comp, s.dep, s.pt.pkey)))
	}
}

// stopParts stops every partition state the filter keeps, revoking all
// their tokens before it returns, and waits for their processes when wait.
func (r *Runner) stopParts(keep func(*state) bool, wait bool) int {
	var wg sync.WaitGroup
	n := 0
	for _, s := range r.partStates("") {
		if !keep(s) {
			continue
		}
		n++
		if !wait {
			r.stopPart(s, false)
			continue
		}
		wg.Add(1)
		go func() { defer wg.Done(); r.stopPart(s, true) }()
	}
	wg.Wait()
	return n
}

// stopStale stops every state of partition part of deployment dep of tile
// that belongs to another incarnation of its person than pkey — the person
// was deleted and recreated: its uid, and so its pkey, is new (PD-20, S13).
func (r *Runner) stopStale(tile, dep, part, pkey string) {
	for _, s := range r.partStates(tile) {
		if s.dep == dep && s.pt.part == part && s.pt.pkey != pkey {
			slog.Info("partition of an earlier incarnation stopped", "component", tile, "deployment", dep)
			r.stopPart(s, false)
		}
	}
}

// StopPartition stops partition part of deployment dep of tile — every
// incarnation of it (a recreated person's included) — revoking its tokens
// first; it returns once none of its processes runs, a spawn in flight
// included. Its data stays (06 §6 stop). A request starts it again.
func (r *Runner) StopPartition(tile, dep, part string) {
	r.stopParts(func(s *state) bool { return s.comp == tile && s.dep == dep && s.pt.part == part }, true)
}

// StopPartitionsOf stops every partition of person userID on every tile —
// their deletion, disabling or loss of access (06 §9) — revoking the tokens
// first; it returns once none of their processes runs.
func (r *Runner) StopPartitionsOf(userID string) {
	part := "user:" + userID
	if n := r.stopParts(func(s *state) bool { return s.pt.part == part }, true); n > 0 {
		slog.Info("partitions stopped", "user", userID, "instances", n)
	}
}

// StopPartitions stops every user partition of tile — a switch's stop step
// before its wipe (01 §2.5), a hold, a disable — revoking their tokens
// first; it returns once none of their processes runs, so nothing writes
// into a partition's namespaces after it.
func (r *Runner) StopPartitions(tile string) {
	r.stopParts(func(s *state) bool { return s.comp == tile }, true)
}

// PartitionsChanged is told a tile's running partition spec changed (01 §6):
// old and new have User only while the tile runs user partitions (its
// state is Partitioned). Boot calls it from the registry's
// OnPartitionChange, before the scan that changed it is published and
// under the scan lock, so the runner answers from the new spec at once
// (partitionSpec) and nothing here waits for a drain. Without user
// partitions every partition instance stops, its tokens revoked here. The
// primary's running generation retires (retirePrimary) when the new spec
// has no global instance, and when the tile turned partitioned or
// unpartitioned: the next request, or alwaysOn's wake, starts it again
// with XBIN_PARTITION saying what it is now. Nothing starts here — c may
// be a tile the scan dropped.
func (r *Runner) PartitionsChanged(c *registry.Component, old, new registry.PartitionSpec) {
	r.mu.Lock()
	if r.parts.mode == nil {
		r.parts.mode = map[string]registry.PartitionSpec{}
	}
	r.parts.mode[c.Path] = new
	r.mu.Unlock()
	if !new.User {
		r.stopParts(func(s *state) bool { return s.comp == c.Path }, false)
	}
	if old != new && (old.User != new.User || new.User && !new.Global) {
		r.retirePrimary(c.Path)
	}
	if !new.User || new.Global {
		time.AfterFunc(partitionWakeDelay, func() { r.wakeTile(c.Path) })
	}
}

// retirePrimary takes tile's primary generation out of service: its token
// is revoked now (01 §6: none the new state doesn't cover stays valid past
// the publish), and it drains in the background. A start in flight starts
// again once it lands (dirty), with what the tile is now.
func (r *Runner) retirePrimary(tile string) {
	s := r.existingStateOf(tile, r.primary(tile))
	if s == nil {
		return
	}
	s.mu.Lock()
	inst := s.cur
	s.cur = nil
	if s.building {
		s.dirty = true
	}
	s.mu.Unlock()
	if inst != nil {
		r.revoke(inst.token)
		go r.stopGen(inst, 5*time.Second)
	}
}

// wakeTile wakes tile's alwaysOn primary, if it has one that may run now
// (after a mode change, whichever rescan made it: a mode act or an unseal's
// resettle never reaches the watcher's WakeAlwaysOn).
func (r *Runner) wakeTile(tile string) {
	if r.Reg == nil {
		return
	}
	if c, ok := r.Reg.Component(tile); ok {
		r.wake(c, r.primary(tile))
	}
}

// changedPartitions is Changed's part for c's user partitions (03 §A.7),
// after nextBuild: every partition state is dirty and forgets its crashes,
// and the live ones restart onto the change's one build (restartParts).
// primary: Changed restarts the primary too, which counts in the change's
// wave (partadmit.go) until the answer is called.
func (r *Runner) changedPartitions(c *registry.Component, primary bool) (done func()) {
	done = func() {}
	if _, ok := r.partitionSpec(c.Path); ok && primary {
		r.waveAdd(c.Path, 1)
		var once sync.Once
		done = func() { once.Do(func() { r.waveAdd(c.Path, -1) }) }
	}
	r.restartParts(c, nil)
	return done
}

// restartParts marks every user partition state of c dirty and restarts
// the live ones, at most partitionSwapsPerTile at once, on the build the
// change's wave shares (the others build on their next request); keep
// (optional) leaves alone a state whose running generation it names — one
// that already runs what a deploy moved to. A partition that may no longer
// run — its person changed, was disabled or lost the tile, the tile's mode
// or lifecycle moved — stops instead.
func (r *Runner) restartParts(c *registry.Component, keep func(*instance) bool) {
	var live []*state
	for _, s := range r.partStates(c.Path) {
		s.mu.Lock()
		if keep != nil && s.cur != nil && !s.building && keep(s.cur) {
			s.mu.Unlock()
			continue
		}
		s.dirty, s.crashes = true, nil
		l := s.cur != nil || s.building
		s.mu.Unlock()
		if l {
			live = append(live, s)
		}
	}
	if len(live) == 0 {
		return
	}
	r.waveAdd(c.Path, len(live))
	sem := make(chan struct{}, partitionSwapsPerTile)
	for _, s := range live {
		go func() {
			defer r.waveAdd(c.Path, -1)
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := r.partitionCurrent(s); err != nil {
				slog.Info("partition stopped, not restarted", "component", c.Path, "err", err)
				r.stopPart(s, false)
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			_, _ = r.ensureState(ctx, c, s)
		}()
	}
}

// partCrashLoop is a user partition's crash-loop error (crashLoop).
type partCrashLoop struct{ error }

func (e partCrashLoop) Unwrap() error { return e.error }

// sticky: a user partition's failure that keeps it down until the tile's
// code changes — its build's, or its crash loop (07-runtime §7). Any other
// failure (the spawn, the health check, a gate that closed meanwhile, a
// record that didn't answer) is this person's alone, and the next request
// tries again through admission.
func sticky(err error) bool {
	var be *BuildError
	var cl partCrashLoop
	return errors.As(err, &be) || errors.As(err, &cl)
}
