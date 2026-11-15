package runner

// live.go — every generation the runner has started and not seen end, and
// the end of starting at shutdown.
//
// StopAll once stopped each state's current generation and returned. A
// generation is current only after it was built, spawned and found healthy
// (install), so one that was still starting when xbind shut down — a boot's
// eager start, a restart after a binding changed, a request's first spawn —
// was installed after StopAll had looked, and nobody stopped it: xbind
// exited and left the backend running (in a test process, one orphan per
// run; under systemd the unit's cgroup hid it). So the runner now keeps the
// set of generations it started, from the spawn itself until the process
// ends, whatever state they are in; and StopAll first ends starting — no
// spawn begins after it, and it waits for the ones under way to return —
// and then stops every generation of the set.

import (
	"errors"
	"sync"
	"time"
)

// errStopping is a start's answer once xbind is shutting down.
var errStopping = errors.New("xbind is shutting down: nothing starts")

// liveSet is the generations started and not ended (the zero value is ready).
type liveSet struct {
	mu       sync.Mutex
	stopping bool
	spawning sync.WaitGroup // spawns under way: Add only under mu while !stopping
	insts    map[*instance]struct{}
}

// beginSpawn admits one spawn, or reports that xbind is shutting down. A
// true answer is paired with endSpawn.
func (l *liveSet) beginSpawn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.stopping {
		return false
	}
	l.spawning.Add(1)
	return true
}

// endSpawn ends the spawn beginSpawn admitted: inst (nil when it failed)
// is live from here until forget.
func (l *liveSet) endSpawn(inst *instance) {
	l.mu.Lock()
	if inst != nil {
		if l.insts == nil {
			l.insts = map[*instance]struct{}{}
		}
		l.insts[inst] = struct{}{}
	}
	l.mu.Unlock()
	l.spawning.Done()
}

func (l *liveSet) forget(inst *instance) {
	l.mu.Lock()
	delete(l.insts, inst)
	l.mu.Unlock()
}

// stop ends starting — no spawn is admitted afterwards, and the ones under
// way have returned when it does — and answers every live generation.
func (l *liveSet) stop() []*instance {
	l.mu.Lock()
	l.stopping = true
	l.mu.Unlock()
	l.spawning.Wait()
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]*instance, 0, len(l.insts))
	for inst := range l.insts {
		out = append(out, inst)
	}
	return out
}

// stopLive stops one generation at shutdown and waits for it to be gone. A
// generation something else is already stopping (a swap draining the old
// one) is waited for the same, and killed at the deadline: that stopper's
// own deadline ends with xbind.
func (r *Runner) stopLive(inst *instance, deadline time.Duration) {
	if !inst.retired.Load() {
		r.stopGen(inst, deadline)
		return
	}
	if inst.cmd == nil || inst.cmd.Process == nil || inst.waitCh == nil {
		return // an engine's generation (tests): its stopper is all there is
	}
	select {
	case <-inst.waitCh:
	case <-time.After(deadline):
		_ = inst.cmd.Process.Kill()
		<-inst.waitCh
	}
}
