package runner

// covers D119e D127h T5 — the lifecycle half of the runner's deployment edges
// (07-runtime §11, §12; 09-fabric §7): seam rows 35 and 36 of 15-test-plan
// §2.5 (TestDeploymentEdgeSeamRows) and 09-fabric §10's
// TestNonPrimaryAlwaysOn, over depFake (deployments_test.go).

import (
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
)

// alwaysOnDev is apps/x with main on the work tree, whose code isn't
// alwaysOn, and dev pinned to c1, whose checkpoint's manifest is; dev's
// alwaysOn switch is on while the returned flag is set.
func alwaysOnDev(t *testing.T) (*depFake, *atomic.Bool) {
	t.Helper()
	f, _ := newDepFake(t, "apps/x")
	f.set("apps/x", "dev", "c1")
	f.r.Materialize = func(tile, tree string) (string, error) {
		root, err := f.materialize(tile, tree)
		if err != nil {
			return "", err
		}
		return root, os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"runtime":"go","alwaysOn":true}`), 0o644)
	}
	on := &atomic.Bool{}
	f.r.AlwaysOnSwitched = func(tile string) []string {
		if on.Load() && tile == "apps/x" {
			return []string{"dev"}
		}
		return nil
	}
	return f, on
}

func (f *depFake) advance(d time.Duration) {
	f.mu.Lock()
	f.clock = f.clock.Add(d)
	f.mu.Unlock()
}

func healthyGen(f *depFake, dep string, gen int) func() bool {
	return func() bool {
		st := f.r.DeploymentStatus("apps/x", dep)
		return st.State == "healthy" && st.Gen == gen
	}
}

// rawLog is the fake's effects since the last take, in the order they
// happened (takeLog sorts the stops after the rest).
func (f *depFake) rawLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.log
	f.log = nil
	return out
}

// covers D127h T5 D119e — 15-test-plan §2.5 rows 35 and 36: alwaysOn in dev's
// checkpoint with its switch off, then on, is not started, then started; the
// reaper exemption covers the primary's and switched-on deployments'
// alwaysOn only. A VM tile with file resources stops the old generation of
// the same deployment first, never another deployment's, and a deployment
// whose own namespace holds no file resource doesn't stop first.
func TestDeploymentEdgeSeamRows(t *testing.T) {
	t.Run("35 alwaysOn in dev's checkpoint, switch off then on", func(t *testing.T) {
		f, on := alwaysOnDev(t)
		f.r.WakeAlwaysOn()
		f.settle()
		if got := f.takeLog(); len(got) != 0 || f.r.existingStateOf("apps/x", "dev") != nil {
			t.Fatalf("switch off: %q, want nothing started", got)
		}
		on.Store(true)
		f.r.WakeAlwaysOn()
		waitUntil(t, "dev started", healthyGen(f, "dev", 1))
		f.settle()
		if got, want := f.takeLog(), []string{"build apps/x dev@c1", "start apps/x dev g1 @c1"}; !equalStrings(got, want) {
			t.Fatalf("switch on: %q, want %q", got, want)
		}

		f.ensure("apps/x", "main")
		f.settle()
		f.takeLog()
		if f.r.keptUp("apps/x", "main") || !f.r.keptUp("apps/x", "dev") {
			t.Errorf("exemptions: main %v, dev %v; want only dev's (switched on, alwaysOn code)", f.r.keptUp("apps/x", "main"), f.r.keptUp("apps/x", "dev"))
		}
		f.advance(31 * time.Minute)
		t.Run("the reaper spares the switched-on deployment", func(t *testing.T) {
			f.r.reapOnce()
			f.settle()
			if got, want := f.takeLog(), []string{"stop apps/x main g1"}; !equalStrings(got, want) {
				t.Errorf("reap: %q, want main's alone", got)
			}
		})
		on.Store(false)
		if f.r.keptUp("apps/x", "dev") {
			t.Error("dev is exempt with its switch off")
		}
		f.r.reapOnce()
		f.settle()
		if got := f.takeLog(); !slices.Contains(got, "stop apps/x dev g1") {
			t.Errorf("reap with the switch off: %q, want dev's generation stopped", got)
		}
	})

	t.Run("36 VM tile with file resources stops first per deployment", func(t *testing.T) {
		f, _ := newDepFake(t, "apps/x")
		f.set("apps/x", "dev", "worktree")
		c := f.comps["apps/x"]
		c.Manifest.VM = &registry.VMOpt{On: true}
		canon := filepath.Join(f.root, ".xbin", "resenc", "apps~x", "db")
		devNS := filepath.Join(f.root, ".xbin", "resenc", ".deployments", "dev", "db")
		for _, d := range []string{canon, devNS} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		omit := false
		f.r.EnvFor = func(c *registry.Component, dep string) ([]string, map[string]ResBind) {
			env := []string{"XBIN_RES_DB=" + filepath.Join(canon, "app.sqlite")}
			if dep == "main" {
				return env, nil
			}
			return env, map[string]ResBind{canon: {Src: devNS, Omit: omit}}
		}
		f.ensure("apps/x", "main")
		f.ensure("apps/x", "dev")
		f.settle()
		f.rawLog()

		f.r.ChangedDeployment(c, "dev")
		f.settle()
		if got, want := f.rawLog(), []string{"build apps/x dev@worktree", "stop apps/x dev g1", "start apps/x dev g2 @worktree"}; !equalStrings(got, want) {
			t.Errorf("dev's rebuild:\n got %q\nwant %q", got, want)
		}
		f.r.Changed(c)
		f.settle()
		if got, want := f.rawLog(), []string{"build apps/x main@worktree", "stop apps/x main g1", "start apps/x main g2 @worktree"}; !equalStrings(got, want) {
			t.Errorf("main's rebuild:\n got %q\nwant %q", got, want)
		}

		omit = true // dev's namespace binds no file resource: blue/green as usual
		f.r.ChangedDeployment(c, "dev")
		f.settle()
		if got, want := f.rawLog(), []string{"build apps/x dev@worktree", "start apps/x dev g3 @worktree", "stop apps/x dev g2"}; !equalStrings(got, want) {
			t.Errorf("dev's rebuild without file resources:\n got %q\nwant %q", got, want)
		}
	})
}

// covers D127h T5 — 09-fabric §10's TestNonPrimaryAlwaysOn (§7): a non-primary
// deployment is kept up only by its own code's alwaysOn and its switch
// together — never by the switch alone, and never without isolation; it is
// woken at the wake points, restarted after an exit on its own backoff,
// keyed per deployment; once reassigned primary it is kept up by its code
// alone, and the old primary needs a switch like any other.
func TestNonPrimaryAlwaysOn(t *testing.T) {
	t.Run("the switch without alwaysOn in dev's code keeps nothing up", func(t *testing.T) {
		f, on := alwaysOnDev(t)
		f.set("apps/x", "dev", "worktree") // the work tree's manifest has no alwaysOn
		on.Store(true)
		f.r.WakeAlwaysOn()
		f.settle()
		if got := f.takeLog(); len(got) != 0 || f.r.keptUp("apps/x", "dev") {
			t.Errorf("woken without alwaysOn in its code: %q", got)
		}
	})

	t.Run("woken, restarted after an exit on its own backoff", func(t *testing.T) {
		f, on := alwaysOnDev(t)
		on.Store(true)
		f.r.WakeAlwaysOn()
		waitUntil(t, "dev started", healthyGen(f, "dev", 1))
		f.settle()
		f.takeLog()
		f.crash("apps/x", "dev")
		waitUntil(t, "dev restarted after its backoff", healthyGen(f, "dev", 2))
		f.settle()
		if got, want := f.takeLog(), []string{"start apps/x dev g2 @c1"}; !equalStrings(got, want) {
			t.Errorf("the restart: %q, want %q (its kept artifact)", got, want)
		}
		f.r.ao.mu.Lock()
		backoff := map[string]time.Duration{}
		for k, v := range f.r.ao.backoff {
			backoff[k] = v
		}
		f.r.ao.mu.Unlock()
		if len(backoff) != 1 || backoff[stateKey("apps/x", "dev")] != aoBackoffMin {
			t.Errorf("backoff %v, want dev's alone, keyed (tile, dev)", backoff)
		}
		if st := f.r.existingStateOf("apps/x", "main"); st != nil {
			t.Error("dev's wake and restart touched main")
		}
	})

	t.Run("never without isolation", func(t *testing.T) {
		f, on := alwaysOnDev(t)
		on.Store(true)
		f.r.Isolate = false
		if f.r.keptUp("apps/x", "dev") {
			t.Error("a non-primary deployment is kept up on an xbind without isolation")
		}
	})

	t.Run("reassigned primary", func(t *testing.T) {
		f, _ := alwaysOnDev(t) // the switch stays off
		// The fake's View reads the checkpoint where Materialize leaves it.
		if _, err := f.r.Materialize("apps/x", pinTree("c1")); err != nil {
			t.Fatal(err)
		}
		f.setPrimary("apps/x", "dev")
		f.set("apps/x", "main", "worktree")
		if !f.r.keptUp("apps/x", "dev") || f.r.keptUp("apps/x", "main") {
			t.Errorf("after the reassignment: dev %v (its code), main %v (no switch)", f.r.keptUp("apps/x", "dev"), f.r.keptUp("apps/x", "main"))
		}
	})
}
