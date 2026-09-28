package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119c D127i D127n PO-2 PO-3 PO-8 — boot installs the hooks wave 2.2's work
// packages reach xbind through: the runner's alwaysOn switch, per-deployment
// env and spawn hold, the plane's data namespace and vault acts, and the
// proxy's Route and deployment lookup. Through them a zero-state workspace
// answers today's: no switch is on, each tile's main spawns under the tile's
// own gate with today's env and no remap, main's data is original, the
// primary has no placeholders, and a tile's self-call reaches main.
func TestDeploymentsWiringWave22(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	st := zsBoot(t, zsWorkspace(t)).st
	run, dp := st.Run, st.Deployments
	for name, set := range map[string]bool{
		"runner.AlwaysOnSwitched": run.AlwaysOnSwitched != nil, "runner.EnvFor": run.EnvFor != nil,
		"runner.ShouldRunDeployment": run.ShouldRunDeployment != nil,
		"plane.ResetData":            dp.ResetData != nil, "plane.DropData": dp.DropData != nil,
		"plane.DataOf": dp.DataOf != nil, "plane.JoinData": dp.JoinData != nil,
		"plane.VaultCopy": dp.VaultCopy != nil, "plane.VaultPlaceholders": dp.VaultPlaceholders != nil,
		"proxy.Route": st.Proxy.Route != nil, "proxy.Deployments": st.Proxy.Deployments != nil,
	} {
		if !set {
			t.Errorf("the hook %s is not installed", name)
		}
	}
	for _, c := range st.Reg.Components() {
		if got := run.AlwaysOnSwitched(c.Path); got != nil {
			t.Errorf("%s: switched on %v", c.Path, got)
		}
		if got, want := run.ShouldRunDeployment(c.Path, util.MainDeployment), run.ShouldRun(c.Path); got != want {
			t.Errorf("%s: main's spawn gate %v; the tile's %v", c.Path, got, want)
		}
		env, remap := run.EnvFor(c, util.MainDeployment)
		if remap != nil {
			t.Errorf("%s: main's remap %v", c.Path, remap)
		}
		if want := st.Broker.EnvFor(c); len(env) != len(want) {
			t.Errorf("%s: main's env %v; today's %v", c.Path, env, want)
		}
		if d := dp.DataOf(c.Path, util.MainDeployment); d == nil || d.State != "original" || d.Busy != "" {
			t.Errorf("%s: main's data %+v; want original", c.Path, d)
		}
		if ph, err := dp.VaultPlaceholders(c.Path, util.MainDeployment); len(ph) != 0 || err != nil {
			t.Errorf("%s: the primary's placeholders %v, %v", c.Path, ph, err)
		}
		if d := st.Proxy.Route(auth.Principal{Component: c.Path, Via: "instance"}, c, ""); d.Deny != nil || d.Deployment != util.MainDeployment {
			t.Errorf("%s: a self-call routes to %+v; want main", c.Path, d)
		}
	}
}
