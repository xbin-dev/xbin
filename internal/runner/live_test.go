package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// A generation that is still being built or started when xbind shuts down
// is not left running. StopAll once stopped each state's current
// generation only, and one becomes current after its build, spawn and
// health check: a build that finished after StopAll spawned a backend
// nobody stopped — the orphaned backends in-process boot tests (and a
// stopped xbind) left behind.
func TestStopAllLeavesNothingStarting(t *testing.T) {
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, _ := newSeamRunner(t, c)
	running := func() []string {
		f.mu.Lock()
		defer f.mu.Unlock()
		var out []string
		for _, g := range f.gens {
			if !g.closed {
				out = append(out, g.tile)
			}
		}
		return out
	}

	// a build under way when xbind shuts down…
	f.holdNextBuild()
	done := make(chan error, 1)
	go func() { _, err := r.Ensure(context.Background(), c); done <- err }()
	deadline := time.Now().Add(5 * time.Second)
	for f.parkedTile() == "" && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if f.parkedTile() == "" {
		t.Fatal("the build never parked")
	}
	r.StopAll()
	// …finishes afterwards: nothing is spawned for it
	f.releaseBuild()
	if err := <-done; !errors.Is(err, errStopping) {
		t.Errorf("a start after StopAll: %v, want errStopping", err)
	}
	if left := running(); len(left) != 0 {
		t.Errorf("after StopAll a generation runs: %v (log %v)", left, f.takeLog())
	}
	// and nothing starts on a stopped runner
	if _, err := r.Ensure(context.Background(), c); err == nil {
		t.Error("a stopped runner started a backend")
	}
	if left := running(); len(left) != 0 {
		t.Errorf("a stopped runner runs: %v", left)
	}
}

// StopAll stops every generation the runner started, the ones that are no
// state's current generation included: here one that was spawned and never
// installed (its state dropped meanwhile is stopped by install; one whose
// health check is still pending at shutdown is this case).
func TestStopAllStopsWhatWasSpawned(t *testing.T) {
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, _ := newSeamRunner(t, c)
	inst, err := r.startFor(c, "main", "bin", 1) // spawned; nothing made it current
	if err != nil {
		t.Fatal(err)
	}
	r.StopAll()
	f.mu.Lock()
	g := f.gens[inst]
	f.mu.Unlock()
	if g == nil || !g.closed {
		t.Errorf("a spawned generation that is no state's current one survived StopAll: %+v", g)
	}
}
