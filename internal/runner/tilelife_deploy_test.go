package runner

// covers P29 P9 C2 — the runtime half of TestDeploymentsAcrossTileLife
// (05-model §11; 07-runtime §7 rows 5, 7 and 13, §11): the tile's lifecycle
// and a transfer reach every deployment it has, over depFake
// (deployments_test.go). The broker's half, which drives these through
// StopBackend, WakeBackends and OnGrantChange as boot wires them, is
// internal/broker's TestDeploymentsAcrossTileLifeM2.

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// lifeWorld is apps/x with four deployments: main, the primary, following
// the work tree, whose code is alwaysOn; dev pinned to c1, alwaysOn code with
// its switch off; qa pinned to c2, alwaysOn code with its switch on; exp
// pinned to c1, never started. enabled is the tile's lifecycle, which boot's
// ShouldRun and ShouldRunDeployment both read.
func lifeWorld(t *testing.T) (*depFake, *tape, *atomic.Bool) {
	t.Helper()
	f, tp := newDepFake(t, "apps/x")
	if err := os.WriteFile(filepath.Join(f.root, "apps", "x", "xbin.json"), []byte(`{"runtime":"go","alwaysOn":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	c, ok := f.r.Reg.Component("apps/x")
	if !ok || !c.Manifest.AlwaysOn {
		t.Fatalf("fixture: apps/x isn't registered with alwaysOn code: %+v", c)
	}
	f.comps["apps/x"] = c // requests, like the proxy's, carry the registry's component
	f.set("apps/x", "dev", "c1")
	f.set("apps/x", "qa", "c2")
	f.set("apps/x", "exp", "c1")
	f.r.Materialize = func(tile, tree string) (string, error) {
		root, err := f.materialize(tile, tree)
		if err != nil {
			return "", err
		}
		return root, os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"runtime":"go","alwaysOn":true}`), 0o644)
	}
	f.r.AlwaysOnSwitched = func(tile string) []string {
		if tile == "apps/x" {
			return []string{"qa"}
		}
		return nil
	}
	enabled := &atomic.Bool{}
	enabled.Store(true)
	f.r.ShouldRun = func(tile string) bool { return enabled.Load() }
	f.r.ShouldRunDeployment = func(tile, dep string) bool { return enabled.Load() }
	return f, tp, enabled
}

// lifeStates reports each deployment's DeploymentStatus as "dep state gN".
func (f *depFake) lifeStates(deps ...string) []string {
	var out []string
	for _, dep := range deps {
		st := f.r.DeploymentStatus("apps/x", dep)
		out = append(out, dep+" "+st.State+" g"+strconv.Itoa(st.Gen))
	}
	return out
}

// covers P29 P9 C2 — lifecycle is the tile's (05-model §11): disabling stops
// every deployment (Stop, the broker's StopBackend) and none of them starts
// again while the tile isn't enabled — not by a request, a save, a grant or
// transfer restart, nor alwaysOn; enabling starts the primary (alwaysOn), a
// deployment beyond it only by its own alwaysOn switch, and the rest on
// demand, each onto the code its record names (a pinned one from its kept
// artifact, never compiled again). A transfer (OnGrantChange: ChangedTile)
// restarts every running deployment the same way, and starts none that
// wasn't running; each restart's events ride the primary's bare types or op
// build (C2).
func TestDeploymentsAcrossTileLifeRuntime(t *testing.T) {
	f, tp, enabled := lifeWorld(t)
	deps := []string{"main", "dev", "qa", "exp"}

	// boot: alwaysOn wakes the primary and the switched-on qa; dev and exp
	// wait for a request, and dev gets one.
	f.r.WakeAlwaysOn()
	waitUntil(t, "main and qa started", func() bool {
		return f.r.DeploymentStatus("apps/x", "main").State == "healthy" && f.r.DeploymentStatus("apps/x", "qa").State == "healthy"
	})
	f.settle()
	if got := f.ensure("apps/x", "dev"); got != "g1" {
		t.Fatalf("dev on demand: %s", got)
	}
	f.settle()
	if got, want := sortedCopy(f.takeLog()), sortedCopy([]string{
		"build apps/x main@worktree", "start apps/x main g1 @worktree",
		"build apps/x qa@c2", "start apps/x qa g1 @c2",
		"build apps/x dev@c1", "start apps/x dev g1 @c1",
	}); !equalStrings(got, want) {
		t.Fatalf("setup effects:\n got %q\nwant %q", got, want)
	}
	f.events(tp)

	t.Run("disabling stops every deployment", func(t *testing.T) {
		enabled.Store(false) // the manifest's lifecycle moves first (lifecycle.go)
		f.r.Stop("apps/x")
		f.settle()
		if got, want := f.takeLog(), []string{"stop apps/x dev g1", "stop apps/x main g1", "stop apps/x qa g1"}; !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if got, want := f.lifeStates(deps...), []string{"main idle g1", "dev idle g1", "qa idle g1", "exp idle g0"}; !equalStrings(got, want) {
			t.Errorf("states %q, want %q", got, want)
		}
	})

	t.Run("nothing starts while the tile isn't enabled", func(t *testing.T) {
		for _, dep := range deps {
			if got := f.ensure("apps/x", dep); !strings.Contains(got, "is not enabled") {
				t.Errorf("a request for %s: %s, want refused", dep, got)
			}
		}
		c := f.comps["apps/x"]
		f.r.ChangedTile(c)               // a grant or a transfer
		f.r.ChangedDeployment(c, "dev")  // a save, live reload elsewhere
		f.r.ChangedDeployment(c, "main") // a save on the primary
		f.r.WakeAlwaysOn()               // an unseal, a watcher batch
		f.settle()
		if got := f.takeLog(); len(got) != 0 {
			t.Errorf("effects while disabled: %q, want none", got)
		}
		if got := f.events(tp); len(got) != 0 {
			t.Errorf("events while disabled: %q, want none", got)
		}
	})

	t.Run("enabling starts the primary; the rest by their switch or on demand", func(t *testing.T) {
		enabled.Store(true)
		f.r.WakeAlwaysOn() // lifecycle.go's wakeBackends
		waitUntil(t, "main and qa started again", func() bool {
			return f.r.DeploymentStatus("apps/x", "main").State == "healthy" && f.r.DeploymentStatus("apps/x", "qa").State == "healthy"
		})
		f.settle()
		if got, want := sortedCopy(f.takeLog()), sortedCopy([]string{
			"build apps/x main@worktree", "start apps/x main g2 @worktree",
			"start apps/x qa g2 @c2", // its kept artifact: a checkpoint is never compiled twice
		}); !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if got, want := f.lifeStates("dev", "exp"), []string{"dev idle g1", "exp idle g0"}; !equalStrings(got, want) {
			t.Errorf("before a request: %q, want %q", got, want)
		}
		if got := f.ensure("apps/x", "dev"); got != "g2" {
			t.Errorf("dev on demand: %s, want g2", got)
		}
		f.settle()
		if got, want := f.takeLog(), []string{"start apps/x dev g2 @c1"}; !equalStrings(got, want) {
			t.Errorf("dev's effects:\n got %q\nwant %q", got, want)
		}
		if got, want := sortedCopy(f.events(tp)), sortedCopy([]string{bsX, boX,
			dbuild("qa", "start"), dbuild("qa", "ok"), dbuild("dev", "start"), dbuild("dev", "ok")}); !equalStrings(got, want) {
			t.Errorf("events:\n got %q\nwant %q", got, want)
		}
	})

	t.Run("a transfer restarts every running deployment", func(t *testing.T) {
		f.r.ChangedTile(f.comps["apps/x"]) // OnGrantChange, as executeTransferEffects fires it
		waitUntil(t, "every running deployment restarted", func() bool {
			return slices.Equal(f.lifeStates("main", "dev", "qa"), []string{"main healthy g3", "dev healthy g3", "qa healthy g3"})
		})
		f.settle()
		if got, want := sortedCopy(f.takeLog()), sortedCopy([]string{
			"build apps/x main@worktree", "start apps/x main g3 @worktree", "stop apps/x main g2",
			"start apps/x dev g3 @c1", "stop apps/x dev g2", // the same checkpoint and artifact, a new spawn-time env
			"start apps/x qa g3 @c2", "stop apps/x qa g2",
		}); !equalStrings(got, want) {
			t.Errorf("effects:\n got %q\nwant %q", got, want)
		}
		if f.r.existingStateOf("apps/x", "exp") != nil {
			t.Error("the restart made exp a state: a deployment without a generation starts on its next request")
		}
		if got, want := sortedCopy(f.events(tp)), sortedCopy([]string{bsX, boX,
			dbuild("dev", "start"), dbuild("dev", "ok"), dbuild("qa", "start"), dbuild("qa", "ok")}); !equalStrings(got, want) {
			t.Errorf("events:\n got %q\nwant %q", got, want)
		}
	})
}
