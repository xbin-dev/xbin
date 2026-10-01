package deployments

import (
	"testing"
	"time"
)

// covers D174 — settle, what SettledCodeFor waits on: it returns at once for
// a tile no operation marks (another tile's mark included), and otherwise
// only once every mark on the tile is released, however many overlap.
func TestOverlaySettle(t *testing.T) {
	var o overlay
	settled := func(tile string) chan struct{} {
		done := make(chan struct{})
		go func() { o.settle(tile); close(done) }()
		return done
	}
	must := func(done chan struct{}, what string) {
		t.Helper()
		select {
		case <-done:
		case <-time.After(time.Minute): // a hang guard
			t.Fatalf("settle never returned %s", what)
		}
	}
	mustNot := func(done chan struct{}, what string) {
		t.Helper()
		select {
		case <-done:
			t.Fatalf("settle returned %s", what)
		default:
		}
	}

	must(settled("apps/x"), "with no mark at all")
	other := o.on("apps/y")
	must(settled("apps/x"), "for a tile another tile's mark doesn't cover")

	first, second := o.on("apps/x"), o.on("apps/x")
	done := settled("apps/x")
	for !o.waiting("apps/x") { // the waiter has its channel: what release closes
		time.Sleep(time.Millisecond)
	}
	first()
	first() // a release runs once
	mustNot(done, "while a second operation still marks the tile")
	second()
	must(done, "once every mark on the tile is released")
	other()
	if o.has("apps/x") || o.has("apps/y") || o.n.Load() != 0 {
		t.Error("marks left after every release")
	}
}

// waiting says whether a settle on tile holds its channel.
func (o *overlay) waiting(tile string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.idle[tile] != nil
}
