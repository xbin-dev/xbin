package runner

// partstart.go — the spawn window of a person's partition
// (plans/partitions/03 §A.2, §A.8; 02 §2; 01 §6). A build can take minutes,
// and a stop (a mode change, a switch before its wipe, a person's deletion,
// an eviction) may land meanwhile. So a partition's generation passes its
// gates again right before it spawns (partBegin) — the state isn't stopped,
// the tile still runs people's partitions, the person is who the state was
// made for and may run it — and every step after is visible to stopPart
// under the state's lock: the token registered mid-spawn (registerGen), the
// generation spawned but not yet healthy (partSpawned), and the install.
// A stop thus revokes every token the partition could authenticate with
// before it returns, and a waiting stop returns only once the spawn that
// was in flight has stopped what it spawned.

import (
	"fmt"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// partitionCurrent is what partition state s must still pass to spawn or to
// restart after a change: the tile runs people's partitions and may run,
// this xbind isolates backends and has the planes' hooks, and its person is
// still the one the state was made for (PartitionIdent answers its pkey)
// and may run it (ShouldRunPartition, with that uid).
func (r *Runner) partitionCurrent(s *state) error {
	refuse := func(why string) error {
		return fmt.Errorf("%w: %s's partition of %s %s", ErrPartitionRefused, s.pt.part, s.comp, why)
	}
	h := r.PartitionHooks
	switch {
	case !r.Isolate:
		return refuse("runs only in a sandbox (--isolate)")
	case h.PartitionIdent == nil || h.ShouldRunPartition == nil || h.PartitionEnv == nil || h.RegisterPartitionInstance == nil:
		return refuse("can't get its own identity and data on this xbind yet")
	case !r.shouldRun(s.comp, s.dep):
		return refuse("can't run: the tile is not enabled")
	}
	if _, ok := r.partitionSpec(s.comp); !ok {
		return refuse("can't run: the tile doesn't run people's partitions now")
	}
	if pkey, _, err := h.PartitionIdent(s.comp, s.pt.part); err != nil || pkey != s.pt.pkey {
		return refuse("is no longer its person's (they were removed, or removed and made again)")
	}
	if !h.ShouldRunPartition(s.comp, s.dep, s.pt.part, s.pt.uid) {
		return refuse("may not run now")
	}
	return nil
}

// stoppedErr is the error of a start whose partition was stopped while it
// built or spawned.
func stoppedErr(s *state) error {
	return fmt.Errorf("%w: %s's partition of %s was stopped while it started", ErrPartitionRefused, s.pt.part, s.comp)
}

// partBegin opens the spawn window of state s for a generation spawning
// from view v: v itself for every state but a person's partition's, which
// gets its own copy of the view (partitionView) once it passed its gates
// again, and is refused, spawning nothing, when it doesn't or s was stopped.
// Every partBegin that answers a view is closed by partSpawned.
func (r *Runner) partBegin(s *state, v *registry.Component) (*registry.Component, error) {
	if s.pt == nil {
		return v, nil
	}
	if err := r.partitionCurrent(s); err != nil {
		return nil, err
	}
	pv := partitionView(v, s.pt.part, s.pt.pkey)
	s.mu.Lock()
	if s.gone {
		s.mu.Unlock()
		return nil, stoppedErr(s)
	}
	s.pt.spawning = make(chan struct{})
	s.mu.Unlock()
	r.mu.Lock()
	if r.parts.spawns == nil {
		r.parts.spawns = map[*registry.Component]*state{}
	}
	r.parts.spawns[pv] = s
	r.mu.Unlock()
	return pv, nil
}

// partSpawned closes the spawn window partBegin opened for view v: the
// generation startFor answered (inst, err) becomes the state's starting
// one, or, when a stop landed meanwhile, is stopped here — its token
// revoked first — before the window closes, so a waiting stop returns only
// once it is gone. Any other state's answer passes through.
func (r *Runner) partSpawned(s *state, v *registry.Component, inst *instance, err error) (*instance, error) {
	if s.pt == nil {
		return inst, err
	}
	r.mu.Lock()
	delete(r.parts.spawns, v)
	r.mu.Unlock()
	s.mu.Lock()
	gone, window := s.gone, s.pt.spawning
	s.pt.spawning, s.pt.token = nil, ""
	if !gone && inst != nil {
		s.pt.starting = inst
	}
	s.mu.Unlock()
	if gone && inst != nil {
		r.revoke(inst.token)
		r.stopGen(inst, 2*time.Second)
	}
	if window != nil {
		close(window)
	}
	switch {
	case err != nil:
		return nil, err
	case gone:
		return nil, stoppedErr(s)
	}
	return inst, nil
}

// registerGen registers a generation's instance token from the runner's
// own state: any but a person's partition's through registerInstance; a
// person's partition's through RegisterPartitionInstance with its person's
// uid, under the state's lock and only while the state isn't stopped, and
// kept where a stop finds it (a spawn partBegin didn't open registers
// nothing: its generation can't authenticate).
func (r *Runner) registerGen(token string, c *registry.Component, dep string) {
	if c.Partition == "" {
		r.registerInstance(token, c.Path, dep)
		return
	}
	r.mu.Lock()
	s := r.parts.spawns[c]
	r.mu.Unlock()
	f := r.RegisterPartitionInstance
	if s == nil || f == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gone {
		return
	}
	f(token, c.Path, dep, c.Partition, s.pt.uid)
	s.pt.token = token
}

// notInstalled is buildAndStart's error for a generation install refused:
// a person's partition stopped while it started, the primary of a tile
// that runs no global instance now (a mode change landed meanwhile), or a
// deployment removed while it built.
func (r *Runner) notInstalled(s *state, tile, dep string) error {
	switch {
	case s.pt != nil:
		return stoppedErr(s)
	case r.noGlobal(tile, dep):
		return globalRefusal(tile)
	}
	return util.NoDeployment(tile, dep)
}
