package runner

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// liveWorld is a record for the seam runner's apps/x: main follows the work
// tree until pin, and a pause in progress (detaching) makes SettledCodeFor
// wait until it ends.
type liveWorld struct {
	mu        sync.Mutex
	pinned    bool
	detaching chan struct{} // non-nil while a pause is between its request and its end
	settled   chan struct{} // receives once SettledCodeFor is waiting
}

func (w *liveWorld) codeFor(tile, dep string) (Code, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pinned {
		return Code{Tree: strings.Repeat("ab", 20)}, nil
	}
	return Code{WorkTree: true}, nil
}

func (w *liveWorld) settledCodeFor(tile, dep string) (Code, error) {
	w.mu.Lock()
	d := w.detaching
	w.mu.Unlock()
	if d != nil {
		w.settled <- struct{}{}
		<-d
	}
	return w.codeFor(tile, dep)
}

// pause starts a pause: SettledCodeFor waits until end, which commits it
// (pin) or not (a failed pause, before its commit).
func (w *liveWorld) pause() (end func(commit bool)) {
	d := make(chan struct{})
	w.mu.Lock()
	w.detaching = d
	w.mu.Unlock()
	return func(commit bool) {
		w.mu.Lock()
		w.pinned = commit
		w.detaching = nil
		w.mu.Unlock()
		close(d)
	}
}

// covers D174 SC-LIVE-RELOAD-PAUSE — a generation built from the work tree
// serves only if live reload still drives its deployment once any pause in
// progress has ended: one built while a pause took its checkpoint read the
// work tree possibly after that checkpoint, so when the pause commits it is
// stopped unserved and the current generation serves on (the pause's own
// deploy then swaps the checkpoint in); when the pause fails before its
// commit, the generation serves as any save's would.
func TestWorkTreeGenerationAfterPause(t *testing.T) {
	for _, commit := range []bool{true, false} {
		name := map[bool]string{true: "the pause commits", false: "the pause fails"}[commit]
		t.Run(name, func(t *testing.T) {
			c := &registry.Component{Path: "apps/x", Manifest: registry.Manifest{Runtime: "go"}}
			r, f, _ := newSeamRunner(t, c)
			w := &liveWorld{settled: make(chan struct{}, 1)}
			r.DeploymentHooks = DeploymentHooks{CodeFor: w.codeFor, SettledCodeFor: w.settledCodeFor}
			ensure := func() string {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				sock, err := r.Ensure(ctx, c)
				if err != nil {
					t.Fatal(err)
				}
				return sock
			}
			g1 := ensure()
			settle(t, r, f)
			f.takeLog()

			f.holdNextBuild()
			r.Changed(c) // a save: its work-tree build parks
			waitParked(t, f, c.Path)
			end := w.pause() // the pause begins while the save builds
			f.releaseBuild()
			select { // the built generation waits for the pause to end
			case <-w.settled:
			case <-time.After(time.Minute): // a hang guard
				t.Fatal("the work tree's generation never asked whether live reload still drives it")
			}
			end(commit)
			settle(t, r, f)

			got := ensure()
			log := f.takeLog()
			if commit {
				if got != g1 {
					t.Errorf("after the pause committed, the deployment answers %s, want the generation before it, %s", got, g1)
				}
				if want := []string{bld, st(2), sp(2)}; !equalStrings(log, want) {
					t.Errorf("fake log %q, want %q: the work tree's generation started and stopped unserved", log, want)
				}
			} else {
				if got == g1 || !strings.HasSuffix(got, "/g2.sock") {
					t.Errorf("after the pause failed, the deployment answers %s, want the save's generation g2", got)
				}
				if want := []string{bld, st(2), sp(1)}; !equalStrings(log, want) {
					t.Errorf("fake log %q, want %q: the save's generation swapped in", log, want)
				}
			}
		})
	}
}

// waitParked waits until tile's build is parked on the fake's hold.
func waitParked(t *testing.T, f *fakeEngine, tile string) {
	t.Helper()
	for deadline := time.Now().Add(time.Minute); ; {
		f.mu.Lock()
		parked := f.parked
		f.mu.Unlock()
		if parked == tile {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s's build never parked", tile)
		}
		time.Sleep(time.Millisecond)
	}
}
