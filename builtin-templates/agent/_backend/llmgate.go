// llmgate.go — how many model calls run at once.
//
// The limit (Config.MaxActiveRuns, default 4) is taken per MODEL CALL, not
// per run: a run waiting on a tool, a subagent or a human holds nothing. Two
// classes: top-level runs (a person is usually watching) go first, and
// subagents may hold at most limit-1 slots, so a new chat waits for at most
// one call to finish however wide a fan-out is running. FIFO within a class;
// a waiter whose context ends leaves the queue.
package main

import (
	"container/list"
	"context"
	"sync"
)

type llmGate struct {
	mu     sync.Mutex
	limit  int
	active int
	kids   int // active slots held by subagents
	top    *list.List
	child  *list.List
}

type gateWaiter struct {
	ch  chan struct{}
	top bool
}

func newLLMGate(limit int) *llmGate {
	return &llmGate{limit: clampCfg(limit, defaultMaxActiveRuns, 32), top: list.New(), child: list.New()}
}

func (g *llmGate) kidCap() int {
	if g.limit >= 2 {
		return g.limit - 1
	}
	return g.limit
}

// acquire takes a slot, waiting if needed. The returned release must be
// called exactly once.
func (g *llmGate) acquire(ctx context.Context, top bool) (func(), error) {
	g.mu.Lock()
	if g.canRun(top) && (top && g.top.Len() == 0 || !top && g.child.Len() == 0) {
		g.take(top)
		g.mu.Unlock()
		return g.releaser(top), nil
	}
	w := &gateWaiter{ch: make(chan struct{}), top: top}
	q := g.child
	if top {
		q = g.top
	}
	el := q.PushBack(w)
	g.mu.Unlock()
	select {
	case <-w.ch:
		return g.releaser(top), nil
	case <-ctx.Done():
		g.mu.Lock()
		select {
		case <-w.ch: // granted just as we gave up: hand it back
			g.mu.Unlock()
			g.releaser(top)()
		default:
			q.Remove(el)
			g.mu.Unlock()
		}
		return nil, context.Cause(ctx)
	}
}

func (g *llmGate) canRun(top bool) bool {
	if g.active >= g.limit {
		return false
	}
	return top || g.kids < g.kidCap()
}

func (g *llmGate) take(top bool) {
	g.active++
	if !top {
		g.kids++
	}
}

func (g *llmGate) releaser(top bool) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			g.active--
			if !top {
				g.kids--
			}
			g.grantLocked()
			g.mu.Unlock()
		})
	}
}

// grantLocked hands free slots to waiters: top-level first.
func (g *llmGate) grantLocked() {
	for {
		switch {
		case g.top.Len() > 0 && g.canRun(true):
			w := g.top.Remove(g.top.Front()).(*gateWaiter)
			g.take(true)
			close(w.ch)
		case g.child.Len() > 0 && g.canRun(false):
			w := g.child.Remove(g.child.Front()).(*gateWaiter)
			g.take(false)
			close(w.ch)
		default:
			return
		}
	}
}

// setLimit resizes the gate live (a config write).
func (g *llmGate) setLimit(n int) {
	g.mu.Lock()
	g.limit = clampCfg(n, defaultMaxActiveRuns, 32)
	g.grantLocked()
	g.mu.Unlock()
}

// stats reports active calls, the limit, and how many wait.
func (g *llmGate) stats() (active, limit, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active, g.limit, g.top.Len() + g.child.Len()
}

// tryBackground takes a slot for background work (a conversation's title)
// only when nobody waits and a slot stays free for a person; it never
// queues. nil when it can't — the work is simply tried again later.
func (g *llmGate) tryBackground() func() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.top.Len() > 0 || g.child.Len() > 0 || g.active >= max(g.limit-1, 1) {
		return nil
	}
	g.take(false)
	return g.releaser(false)
}
