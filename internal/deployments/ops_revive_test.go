package deployments

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/runner"
)

// statusRunner is the fake runner reporting one state for every deployment,
// as *runner.Runner's DeploymentStatus does.
type statusRunner struct {
	*fakeRunner
	state string
}

func (r *statusRunner) DeploymentStatus(tile, dep string) runner.DeploymentState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return runner.DeploymentState{State: r.state}
}

// covers SC-PINNED — deploying the checkpoint a deployment already runs
// (11-contract §1.6, 07-runtime §8.7): unchanged while its generation is
// healthy or being built; a new generation from the kept artifact (how
// "restart") while the runner reports it crash-looping, failed or down — a
// pinned primary a save can't revive. Reload now of an identical work tree
// stays unchanged whatever the runner says (§1.5), and a static tile has no
// generation to be down.
func TestDeployOwnCheckpointRevivesDownGeneration(t *testing.T) {
	for _, tc := range []struct {
		state   string
		restart bool
	}{
		{"healthy", false},
		{"building", false},
		{"failed", true},
		{"idle", true},
	} {
		t.Run(tc.state, func(t *testing.T) {
			f := newOpsFx(t, true)
			sr := &statusRunner{fakeRunner: f.run, state: "healthy"}
			f.p.Run = sr
			f.settle(opAPI, f.must(ownerP, OpPause, &PauseRequest{Tile: opAPI}))
			tree := *f.rec(opAPI).Deployments["main"].Checkpoint
			deploys, _ := sr.counts()
			sr.set(func(*fakeRunner) { sr.state = tc.state })

			if ans := f.must(ownerP, OpReloadNow, &ReloadNowRequest{Tile: opAPI}); !ans.Unchanged || ans.Deploy != nil {
				t.Errorf("reload now of an identical work tree while %s = %+v, want unchanged", tc.state, ans)
			}
			res, err := f.do(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Checkpoint: "c:" + tree, DryRun: true})
			dry, ok := res.(DryRunAnswer)
			if err != nil || !ok {
				t.Fatalf("dry run: %T %v", res, err)
			}
			if got := len(dry.Impact.Reloads) == 1; got != tc.restart {
				t.Errorf("dry run while %s: impact %+v, want reloads %v", tc.state, dry.Impact, tc.restart)
			}
			ans := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opAPI, Checkpoint: "c:" + tree})
			if !tc.restart {
				if !ans.Unchanged || ans.Deploy != nil {
					t.Errorf("deploy of its own checkpoint while %s = %+v, want unchanged", tc.state, ans)
				}
				if n, _ := sr.counts(); n != deploys {
					t.Errorf("deploys %d → %d, want none", deploys, n)
				}
				return
			}
			if ans.Unchanged || ans.Deploy == nil {
				t.Fatalf("deploy of its own checkpoint while %s = %+v, want a restart", tc.state, ans)
			}
			if e := f.wait(opAPI, ans.Deploy.ID); e.How != "restart" || e.Result != resultOK {
				t.Errorf("the restart = %+v", e)
			}
			if n, _ := sr.counts(); n != deploys+1 {
				t.Errorf("deploys %d → %d, want one from the kept artifact", deploys, n)
			}
			if got := *f.rec(opAPI).Deployments["main"].Checkpoint; got != tree {
				t.Errorf("the pointer moved: %s → %s", tree, got)
			}
		})
	}

	t.Run("static", func(t *testing.T) {
		f := newOpsFx(t, true)
		f.p.Run = &statusRunner{fakeRunner: f.run, state: "idle"}
		f.settle(opSite, f.must(ownerP, OpPause, &PauseRequest{Tile: opSite}))
		tree := *f.rec(opSite).Deployments["main"].Checkpoint
		if ans := f.must(ownerP, OpDeploy, &DeployRequest{Tile: opSite, Checkpoint: "c:" + tree}); !ans.Unchanged {
			t.Errorf("deploy of a static tile's own checkpoint = %+v, want unchanged", ans)
		}
	})
}
