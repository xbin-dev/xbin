package runner

// oom.go — what a backend leaf's OOM kills leave behind (D203,
// plans/tile-limits.md §6): the kernel's oom_kill counter goes with the
// leaf, so an exiting generation reads it before its leaf may go
// (noteExit); its backend log says so; the crash watch's breaker names the
// cap and how to raise it; and the limits plane hears of it (OnOOM) as a
// hit at the tile's memory cap. A live leaf's kills (a child the killer
// took while the backend kept serving) are read by the limit alerts
// through AtLimitTile, against the same count, so each kill is told once.

import (
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
)

// LimitHooks are the tile-limits plane's, installed at boot
// (stepTileLimits); each nil-safe (Runner embeds them).
type LimitHooks struct {
	// OnOOM is told of an exited generation whose leaf the OOM killer took
	// processes in since the runner last looked. It runs on the exit's
	// goroutine before the crash watch sees the exit: it must not block.
	OnOOM func(OOMEvent)
	// VMGrant is what decisions raise tile's VM backend to (vmMemMiB,
	// vmVCPUs; 0 = nothing raised): a guest's ceiling is the larger of the
	// policy's and the grant, which the manifest's "vm" sizes lower as
	// before (guestSize).
	VMGrant func(tile string) (memMiB, vcpus int)
}

// OOMEvent is one exit after the OOM killer took processes in the leaf of
// a backend generation.
type OOMEvent struct {
	Tile       string
	Deployment string // its deployment's name ("main" included)
	Partition  bool   // a person's partition's: who stays out of every alert (03 §A.9)
	Gen        int
	Kills      int64 // processes killed since the runner last looked
	LimitMiB   int64 // the leaf's memory.max; 0 when it had none or it couldn't be read
}

// oomState is the runner's view of its leaves' oom_kill counters.
type oomState struct {
	mu sync.Mutex
	// seen is each leaf's counter as last read: an exit and the alerts'
	// poll take their deltas from it, so a kill is told once. A leaf the
	// runner made fresh starts at its own count; one it found (blue/green's
	// second generation, a leftover of an earlier xbind) keeps the count it
	// had or starts at the count it has, never reporting the past.
	seen map[string]int64
	// marks are each leaf's latest kill, which the crash watch's breaker
	// names; they outlive the leaf.
	marks map[string]oomMark
}

type oomMark struct {
	at       time.Time
	limitMiB int64
}

// oomWindow is how recent a kill must be for a crash loop to name it: the
// breaker's own window.
const oomWindow = crashWindow * time.Duration(crashLimit)

// joined records a generation joining leaf, whose counter reads kills now:
// a fresh leaf, or one the runner hasn't tracked, starts there.
func (o *oomState) joined(leaf string, kills int64, fresh bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = map[string]int64{}
	}
	if _, ok := o.seen[leaf]; fresh || !ok {
		o.seen[leaf] = kills
	}
}

// delta is leaf's kills since last read, its counter reading kills now. A
// leaf the runner never saw joined counts from zero when past is set (an
// exit: what it holds is this run's) and from now otherwise (a poll: a
// leftover's past isn't news). A counter below the last read is a new
// leaf's.
func (o *oomState) delta(leaf string, kills int64, past bool) int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.seen == nil {
		o.seen = map[string]int64{}
	}
	prev, ok := o.seen[leaf]
	o.seen[leaf] = kills
	switch {
	case !ok && !past:
		return 0
	case kills < prev:
		return kills
	}
	return kills - prev
}

// forget drops leaf's counter: the leaf is gone, and its count with it.
func (o *oomState) forget(leaf string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	delete(o.seen, leaf)
}

// mark notes a kill in leaf at now, dropping marks past the window.
func (o *oomState) mark(leaf string, now time.Time, limitMiB int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.marks == nil {
		o.marks = map[string]oomMark{}
	}
	for l, m := range o.marks {
		if now.Sub(m.at) >= oomWindow {
			delete(o.marks, l)
		}
	}
	o.marks[leaf] = oomMark{at: now, limitMiB: limitMiB}
}

// recent is leaf's kill within the window before now, if any.
func (o *oomState) recent(leaf string, now time.Time) (oomMark, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	m, ok := o.marks[leaf]
	return m, ok && now.Sub(m.at) < oomWindow
}

// noteExit is an exited generation inst's leaf bookkeeping, of deployment
// dep of view c, whose backend log is log: the leaf's OOM kills read first
// — the counter goes with the leaf — then the leaf removed as leaveLeaf
// does. A kill since the runner last looked is written to the log, marked
// for the crash watch and, but for a VM's leaf (the guest's memory is the
// VM's size, a kind of its own), told to OnOOM. It runs before the exit
// closes waitCh, so the crash watch sees the mark.
func (r *Runner) noteExit(c *registry.Component, dep string, inst *instance, log io.Writer) {
	cg := r.cgroups()
	if cg == nil || inst.leaf == "" {
		return
	}
	u, _ := cg.Usage(inst.leaf)
	if kills := r.oom.delta(inst.leaf, cg.OOMKills(inst.leaf), true); kills > 0 {
		limitMiB := max(u.MemMax, 0) >> 20
		fmt.Fprintf(log, "--- gen %d: OOM-killed at memory.max %s ---\n", inst.gen, memWords(limitMiB))
		if _, isVM := r.vmLeafBytes(inst.sock); !isVM {
			r.oom.mark(inst.leaf, r.now(), limitMiB)
			if f := r.OnOOM; f != nil {
				f(OOMEvent{Tile: c.Path, Deployment: dep, Partition: c.Partition != "", Gen: inst.gen, Kills: kills, LimitMiB: limitMiB})
			}
		}
	}
	cg.Remove(inst.leaf)
	if _, live := cg.Usage(inst.leaf); !live {
		r.oom.forget(inst.leaf)
	}
}

// memWords is a memory cap in MiB for people: "2 GiB"; "its limit" when
// it isn't known.
func memWords(mib int64) string {
	if mib <= 0 {
		return "its limit"
	}
	return limits.Format(limits.MemMiB, mib)
}

// loopError is the crash watch's breaker error: the backend's or a
// person's partition's (Unwrap), with what the OOM killer did, if it did.
type loopError struct {
	error
	oom string
}

func (e *loopError) Error() string { return e.error.Error() + e.oom }
func (e *loopError) Unwrap() error { return e.error }

// looped is the breaker's error err for generation inst of tile: when the
// OOM killer took processes in its leaf inside the breaker's window, it
// says so — the cap, and the three ways to raise it (the tile's ⚙, the
// xbind page, bx).
func (r *Runner) looped(err error, tile string, inst *instance) error {
	e := &loopError{error: err}
	if m, ok := r.oom.recent(inst.leaf, r.now()); ok {
		at := "its memory limit"
		if m.limitMiB > 0 {
			at = "its " + limits.Format(limits.MemMiB, m.limitMiB) + " memory limit"
		}
		e.oom = fmt.Sprintf("; it was OOM-killed at %s — raise it (the tile's ⚙ → Limits, /xbin/limits, `bx limits raise %s memory`)", at, tile)
	}
	return e
}

// crashLooped reports whether err is the breaker's.
func crashLooped(err error) bool {
	var le *loopError
	return errors.As(err, &le)
}

// oomOf is a live leaf's OOM kills since the runner last looked: the
// alerts' poll (AtLimitTile). A leaf the runner never saw joined counts
// from what it has now. A kill marks the leaf for the crash watch.
func (r *Runner) oomOf(cg cgroupOps, leaf string) (kills, limitMiB int64) {
	u, _ := cg.Usage(leaf)
	limitMiB = max(u.MemMax, 0) >> 20
	if kills = r.oom.delta(leaf, cg.OOMKills(leaf), false); kills > 0 {
		r.oom.mark(leaf, r.now(), limitMiB)
	}
	return kills, limitMiB
}
