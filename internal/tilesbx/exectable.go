package tilesbx

// exectable.go — a sandbox's execs (exec.go), kept across its runs: listed
// by id and in start order, found by clientId, counted against
// execsRunning, and pruned past their retention (§3.6).

import (
	"sort"
	"sync"
	"time"
)

// execTable is a sandbox's execs, across its runs.
type execTable struct {
	mu       sync.Mutex
	execs    map[string]*execRec // listed, by id
	order    []*execRec          // listed, oldest first
	byClient map[string]*execRec // by clientId, listed or still starting
	running  int                 // execs running, starts under way and runs: execsRunning
}

func newExecTable() *execTable {
	return &execTable{execs: map[string]*execRec{}, byClient: map[string]*execRec{}}
}

// runningCount is execsRunning.
func (t *execTable) runningCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.running
}

// admit takes one of the sandbox's execsRunning.
func (t *execTable) admit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.admitLocked()
}

func (t *execTable) admitLocked() error {
	if t.running >= execsRunningMax {
		return refuse(RefLimit, "the sandbox runs %d commands, its limit (runtime limits.execsRunning)", t.running)
	}
	t.running++
	return nil
}

// release gives one back.
func (t *execTable) release() {
	t.mu.Lock()
	t.running--
	t.mu.Unlock()
}

// get is a listed exec (nil: none).
func (t *execTable) get(id string) *execRec {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.execs[id]
}

// list is every exec kept, oldest first; the retention is applied first.
func (t *execTable) list(now int64) []*execRec {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pruneLocked(now)
	return append([]*execRec(nil), t.order...)
}

// pruneLocked forgets the finished execs past the retention: kept an hour
// after its end, or while among the sandbox's last execRetainLast
// finished; and never more than execRetainMax finished ones.
func (t *execTable) pruneLocked(now int64) {
	var fin []*execRec
	for _, e := range t.order {
		if ended := e.endedAt(); ended > 0 {
			fin = append(fin, e)
		}
	}
	if len(fin) <= execRetainLast {
		return
	}
	sort.SliceStable(fin, func(i, j int) bool { return fin[i].endedAt() > fin[j].endedAt() })
	for i, e := range fin[execRetainLast:] {
		if i+execRetainLast >= execRetainMax || e.endedAt() < now-execRetain.Milliseconds() {
			t.forgetLocked(e)
		}
	}
}

// forget removes an exec: it is no longer listed or found, its ring's bytes
// go back to the tile's budget, and its clientId may be used again.
func (t *execTable) forget(e *execRec) {
	t.mu.Lock()
	t.forgetLocked(e)
	t.mu.Unlock()
}

func (t *execTable) forgetLocked(e *execRec) {
	if t.execs[e.id] != e {
		return
	}
	delete(t.execs, e.id)
	for i, o := range t.order {
		if o == e {
			t.order = append(t.order[:i], t.order[i+1:]...)
			break
		}
	}
	if e.clientID != "" && t.byClient[e.clientID] == e {
		delete(t.byClient, e.clientID)
	}
	e.ring.Drop()
}

// forgetAll forgets every exec (the sandbox was deleted).
func (t *execTable) forgetAll() {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, e := range append([]*execRec(nil), t.order...) {
		t.forgetLocked(e)
	}
}

// awaitRun waits, at most d in all, until every exec of run r has recorded
// its end — a teardown's, so a stop answers with them killed.
func (t *execTable) awaitRun(r *run, d time.Duration) {
	t.mu.Lock()
	var waits []chan struct{}
	for _, e := range t.order {
		if e.r == r {
			waits = append(waits, e.done)
		}
	}
	t.mu.Unlock()
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for _, w := range waits {
		select {
		case <-w:
		case <-deadline.C:
			return
		}
	}
}
