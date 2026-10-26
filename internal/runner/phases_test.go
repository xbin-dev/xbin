package runner

import (
	"context"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
)

// D196: a build announces its phases — build-start (today's bytes), then
// backend-starting (a new type: older shells and the app ignore it) once it
// is built and its backend spawned and waited for, then build-ok — and
// PrimaryState says building, then starting, then healthy; a build that
// fails never reaches starting.
func TestBuildPhases(t *testing.T) {
	c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
	r, f, _ := newSeamRunner(t, c)
	ch, cancel := f.hub.Subscribe(nil)
	defer cancel()
	raw := func() []events.Event {
		var out []events.Event
		for {
			select {
			case e := <-ch:
				out = append(out, e)
			case <-time.After(50 * time.Millisecond):
				return out
			}
		}
	}

	if got := r.PrimaryState("apps/x"); got != "" {
		t.Fatalf("never run: %q", got)
	}
	f.holdNextBuild()
	done := make(chan error, 1)
	go func() { _, err := r.Ensure(context.Background(), c); done <- err }()
	deadline := time.Now().Add(2 * time.Second)
	for r.PrimaryState("apps/x") != "building" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := r.PrimaryState("apps/x"); got != "building" {
		t.Fatalf("while it compiles: %q", got)
	}
	for f.parkedTile() == "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	f.releaseBuild()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	evs := raw()
	var types []string
	for _, e := range evs {
		types = append(types, e.Type)
	}
	if !equalStrings(types, []string{"build-start", "backend-starting", "build-ok"}) {
		t.Fatalf("events %q", types)
	}
	if evs[0].Data != nil || evs[0].Text != "" {
		t.Fatalf("build-start keeps today's bytes: data %#v text %q", evs[0].Data, evs[0].Text)
	}
	if got := r.PrimaryState("apps/x"); got != "healthy" {
		t.Fatalf("served: %q", got)
	}

	// starting is its own state while building
	s := r.existingState("apps/x")
	s.mu.Lock()
	s.building, s.phase = true, "starting"
	got := stateName(s)
	s.building, s.phase = false, ""
	s.mu.Unlock()
	if got != "starting" {
		t.Fatalf("spawned, not yet healthy: %q", got)
	}

	// a build that fails never starts a backend
	f.failNext("build", c.Path, "main", 1)
	r.Changed(c)
	settle(t, r, f)
	for _, e := range raw() {
		if e.Type == "backend-starting" {
			t.Fatal("a failed build announced its backend starting")
		}
	}
}

// parkedTile is the tile whose build is parked now ("" = none).
func (f *fakeEngine) parkedTile() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.parked
}
