package runner

import (
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-48 — PartitionRunning, the data plane's idle-unmount seam
// (plans/partitions/03 §B.6): true for a person's partition from its start
// on, in its tile's scope and any scope above it, never for another person
// or an unrelated scope; still true while a stop drains (its process may
// still bind the volumes), false once it has.
func TestPartitionRunning(t *testing.T) {
	w := newPartWorld(t, userOnly, registry.Manifest{})
	pk := fakePkey("user:alice")
	if w.r.PartitionRunning("apps/x", pk) {
		t.Error("alice's partition runs before any start")
	}
	w.ensure("user:alice")
	for scope, want := range map[string]bool{"apps/x": true, "apps": true, "apps/y": false, "apps/xy": false} {
		if got := w.r.PartitionRunning(scope, pk); got != want {
			t.Errorf("PartitionRunning(%s) = %v, want %v", scope, got, want)
		}
	}
	if w.r.PartitionRunning("apps/x", fakePkey("user:bob")) {
		t.Error("bob's partition runs")
	}

	e := w.r.engine
	stop := e.stop
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.stop = func(inst *instance, d time.Duration) {
		once.Do(func() { close(held) })
		<-release
		stop(inst, d)
	}
	done := make(chan struct{})
	go func() {
		w.r.StopPartition("apps/x", "main", "user:alice")
		close(done)
	}()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the stop never reached the process")
	}
	if w.state("user:alice") != nil || !w.r.PartitionRunning("apps/x", pk) {
		t.Error("a draining stop: the state must be gone and PartitionRunning still true")
	}
	close(release)
	<-done
	if w.r.PartitionRunning("apps/x", pk) {
		t.Error("PartitionRunning after the drain")
	}
}
