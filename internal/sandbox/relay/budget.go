package relay

import "sync"

// Budget caps the flows of many relays together: every tile sandbox's relay
// shares one (plans/tile-sandbox-runtime.md §4), so however many sandboxes
// run, their flows — each a host fd and buffers in xbind — stay under one
// bound. A flow holds a slot from before it dials until it closes; a closed
// relay gives back every slot its flows held at once. Safe for concurrent
// use. A nil *Budget admits everything (Config.Budget unset).
type Budget struct {
	mu   sync.Mutex
	used int
	cap  int
}

// NewBudget returns a budget of n concurrent flows; n ≤ 0 admits none.
func NewBudget(n int) *Budget { return &Budget{cap: max(n, 0)} }

// Used is how many flows hold a slot now.
func (b *Budget) Used() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.used
}

// Cap is the budget's size (NewBudget's n).
func (b *Budget) Cap() int {
	if b == nil {
		return 0
	}
	return b.cap
}

// take claims a slot; false when the budget is spent.
func (b *Budget) take() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used >= b.cap {
		return false
	}
	b.used++
	return true
}

// give returns a slot take claimed.
func (b *Budget) give() {
	if b == nil {
		return
	}
	b.mu.Lock()
	if b.used > 0 {
		b.used--
	}
	b.mu.Unlock()
}
