package boot

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers P5 P22 NP-14-2 NP-14-11 — boot installs the plane's answers for
// deployments beyond main: the broker's (primary, deployments, the
// deployment a request reaches, active registrations, edge policy,
// registration files), the server's primary and addressed-deployment
// questions through the broker's policy, and the runner's LimitsFor, whose
// ceiling is the caps the cgroup step installs. Through them a zero-state
// workspace answers today's: main everywhere, the tile's caps.
func TestDeploymentsWiringM2(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	st := zsBoot(t, zsWorkspace(t)).st
	a := st.Broker.DeploymentAnswers
	for name, set := range map[string]bool{
		"broker.PrimaryOf": a.PrimaryOf != nil, "broker.DeploymentsOf": a.DeploymentsOf != nil,
		"broker.AddressedDeployment": a.AddressedDeployment != nil, "broker.RegistrationsActive": a.RegistrationsActive != nil,
		"broker.DeploymentEdges": a.DeploymentEdges != nil, "broker.ReadDeploymentFile": a.ReadDeploymentFile != nil,
		"broker.WriteDeploymentFile": a.WriteDeploymentFile != nil, "broker.RemoveDeploymentFile": a.RemoveDeploymentFile != nil,
		"runner.LimitsFor": st.Run.LimitsFor != nil, "term.TileDeployments": st.Term.TileDeployments != nil,
	} {
		if !set {
			t.Errorf("the hook %s is not installed", name)
		}
	}
	var want cgroup.Limits
	if cg := st.Run.Cgroup; cg != nil && cg.Enabled() {
		if want = st.Deployments.TileLimits; want.MemMax != 2<<30 {
			t.Errorf("the plane's tile ceiling is %+v; want the 2G cap the cgroup step installed", want)
		}
	} else if st.Deployments.TileLimits != (cgroup.Limits{}) {
		t.Errorf("without cgroup delegation the plane holds caps %+v", st.Deployments.TileLimits)
	}
	pp, ok := st.Server.Pol.(server.PrimaryPolicy)
	if !ok {
		t.Fatal("the broker's policy names no primary (server.PrimaryPolicy)")
	}
	ap, ok := st.Server.Pol.(interface {
		Addressed(auth.Principal, string) (string, error)
	})
	if !ok {
		t.Fatal("the broker's policy answers no addressed deployment")
	}
	for _, c := range st.Reg.Components() {
		if got := pp.Primary(c.Path); got != util.MainDeployment {
			t.Errorf("%s: the policy's primary is %q", c.Path, got)
		}
		for _, via := range []string{"frame", "instance", "terminal"} {
			if dep, err := ap.Addressed(auth.Principal{Component: c.Path, Via: via}, c.Path); dep != util.MainDeployment || err != nil {
				t.Errorf("%s: its %s principal reaches %q, %v", c.Path, via, dep, err)
			}
		}
		if got := st.Run.LimitsFor(c.Path, util.MainDeployment); got != want {
			t.Errorf("%s: LimitsFor(main) = %+v; want %+v", c.Path, got, want)
		}
		if fires, routes := a.RegistrationsActive(c.Path, util.MainDeployment); !fires || !routes {
			t.Errorf("%s: main's registrations inactive", c.Path)
		}
	}
}
