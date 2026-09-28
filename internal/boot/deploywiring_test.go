package boot

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// covers D119c PO-7 PO-8 NP-14-2 — boot builds the deployments plane and
// installs its methods as every deployment hook M1 reads: the registry's
// PinnedPrimary, the runner's (CodeFor, Primary, View, Materialize,
// EnvFor), the broker's tile-life hooks and the server's three
// questions, and the terminal manager's has-record hook; the plane gets each
// input it asks the rest of xbind for. Through the installed hooks a
// zero-state workspace answers today's (main runs the work tree with the
// broker's env; the server serves each tile's directory), and boot writes
// no deployment state.
func TestDeploymentsWiring(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	ws := zsWorkspace(t)
	d := zsBoot(t, ws)
	st := d.st

	dp := st.Deployments
	if dp == nil {
		t.Fatal("boot built no deployments plane")
	}
	if dp.Root != st.WS || dp.Reg != st.Reg || dp.Hub != st.Hub || dp.Run != st.Run {
		t.Errorf("plane inputs: root %q reg %p hub %p run %v", dp.Root, dp.Reg, dp.Hub, dp.Run)
	}
	for name, set := range map[string]bool{
		"Run": dp.Run != nil, "OwnerRef": dp.OwnerRef != nil, "IsAdmin": dp.IsAdmin != nil,
		"MayManage": dp.MayManage != nil, "TileEnv": dp.TileEnv != nil, "Provision": dp.Provision != nil,
		"ReconcileIngress": dp.ReconcileIngress != nil,
	} {
		if !set {
			t.Errorf("the plane's input %s is not wired", name)
		}
	}
	if dp.OptInClosed {
		t.Error("the ship-dark switch is off by default; want on (opting in open)")
	}

	for name, set := range map[string]bool{
		"registry.PinnedPrimary":           st.Reg.PinnedPrimary != nil,
		"runner.CodeFor":                   st.Run.CodeFor != nil,
		"runner.Primary":                   st.Run.Primary != nil,
		"runner.View":                      st.Run.View != nil,
		"runner.Materialize":               st.Run.Materialize != nil,
		"runner.EnvFor":                    st.Run.EnvFor != nil,
		"runner.EnvForComponent":           st.Run.EnvForComponent != nil,
		"broker.RewriteDeploymentOwner":    st.Broker.RewriteDeploymentOwner != nil,
		"broker.ResetDeploymentState":      st.Broker.ResetDeploymentState != nil,
		"broker.DeploymentLeftovers":       st.Broker.DeploymentLeftovers != nil,
		"broker.DeploymentCodeRoot":        st.Broker.DeploymentCodeRoot != nil,
		"broker.DeploymentExists":          st.Broker.DeploymentExists != nil,
		"broker.AddressableDeployments":    st.Broker.AddressableDeployments != nil,
		"broker.DeploymentSummary":         st.Broker.DeploymentSummary != nil,
		"broker.RestoreDeploymentState":    st.Broker.RestoreDeploymentState != nil,
		"broker.OnGrantChange":             st.Broker.OnGrantChange != nil,
		"term.Manager.HasDeploymentRecord": st.Term.HasDeploymentRecord != nil,
	} {
		if !set {
			t.Errorf("the hook %s is not installed", name)
		}
	}

	pol := st.Server.Pol
	if pol == nil {
		t.Fatal("the broker installed no server policy")
	}
	comps := st.Reg.Components()
	if len(comps) == 0 {
		t.Fatal("the fixture registered no tile")
	}
	for _, c := range comps {
		code, err := st.Run.CodeFor(c.Path, util.MainDeployment)
		if err != nil || !code.WorkTree {
			t.Errorf("%s: CodeFor(main) = %+v, %v", c.Path, code, err)
		}
		if got := st.Run.Primary(c.Path); got != util.MainDeployment {
			t.Errorf("%s: Primary = %q", c.Path, got)
		}
		env, remap := st.Run.EnvFor(c, util.MainDeployment)
		if want := st.Run.EnvForComponent(c); !reflect.DeepEqual(env, want) || remap != nil {
			t.Errorf("%s: EnvFor(main) = %v, %v; want the broker's %v and no remap", c.Path, env, remap, want)
		}
		if root, pinned, err := pol.CodeRoot(c, ""); root != c.Dir || pinned || err != nil {
			t.Errorf("%s: CodeRoot = %q %v %v; want its directory, unpinned", c.Path, root, pinned, err)
		}
		if pol.HasDeployment(c.Path, "dev") || !pol.HasDeployment(c.Path, util.MainDeployment) {
			t.Errorf("%s: HasDeployment answers a deployment other than main", c.Path)
		}
		if pc, ok := st.Reg.PinnedPrimary(c.Path); pc != nil || ok {
			t.Errorf("%s: PinnedPrimary answers for a zero-state tile", c.Path)
		}
		if st.Term.HasDeploymentRecord(c.Path) {
			t.Errorf("%s: HasDeploymentRecord answers true for a zero-state tile", c.Path)
		}
		if dep, attached := dp.LiveReload(c.Path); dep != util.MainDeployment || !attached {
			t.Errorf("%s: LiveReload = %q %v", c.Path, dep, attached)
		}
	}

	// A grant change restarts the tile, every deployment with a generation:
	// only main, a no-op for a tile that runs nothing.
	st.Broker.OnGrantChange("apps/zs")

	keys := map[string]bool{}
	for _, c := range comps {
		keys[util.CompKey(c.Path)] = true
	}
	if bad := append(zsDeploymentState(t, ws, keys), zsRunDirState(t, st.Run.RunDir, keys)...); len(bad) > 0 {
		t.Fatalf("booting a zero-state workspace created deployment state:\n  %s", strings.Join(bad, "\n  "))
	}
}

// covers NP-14-5 — the ship-dark switch is one Config field on the
// --tile-assets precedent: --tile-deployments / XBIN_TILE_DEPLOYMENTS, on by
// default, on or off and nothing else, and boot hands it to the plane
// (OptInClosed) while building it before the first Provision.
func TestTileDeploymentsSwitch(t *testing.T) {
	var cfg Config
	fs := newFlagSet()
	cfg.RegisterFlags(fs)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.TileDeploys != "on" || cfg.tileDeploysClosed() {
		t.Errorf("default --tile-deployments = %q, want on", cfg.TileDeploys)
	}
	t.Setenv("XBIN_TILE_DEPLOYMENTS", "off")
	var envCfg Config
	fs = newFlagSet()
	envCfg.RegisterFlags(fs)
	if err := fs.Parse(nil); err != nil || envCfg.TileDeploys != "off" || !envCfg.tileDeploysClosed() {
		t.Errorf("XBIN_TILE_DEPLOYMENTS=off gives %q (%v)", envCfg.TileDeploys, err)
	}
	fs = newFlagSet()
	envCfg.RegisterFlags(fs)
	if err := fs.Parse([]string{"--tile-deployments", "on"}); err != nil || envCfg.TileDeploys != "on" {
		t.Errorf("the flag beats the env: %q (%v)", envCfg.TileDeploys, err)
	}
	for _, v := range []string{"", "on", "off"} {
		if _, err := (&Config{Workspace: ".", TileDeploys: v}).Validate(); err != nil {
			t.Errorf("--tile-deployments=%q refused: %v", v, err)
		}
	}
	for _, v := range []string{"yes", "true", "OFF", "0"} {
		if _, err := (&Config{Workspace: ".", TileDeploys: v}).Validate(); err == nil {
			t.Errorf("--tile-deployments=%q accepted", v)
		}
	}

	if testing.Short() {
		t.Skip("boots the first steps of a workspace")
	}
	quiet(t)
	for _, c := range []struct {
		value  string
		closed bool
	}{{"", false}, {"on", false}, {"off", true}} {
		ws := zsWorkspace(t)
		cfg := &Config{Workspace: ws, Listen: "127.0.0.1:0", InsecureVault: true, TileDeploys: c.value,
			Privileges: NoPrivileges{}, Version: "test", LimitMem: "2G"}
		d, err := cfg.Validate()
		if err != nil {
			t.Fatal(err)
		}
		st := &State{Cfg: cfg, WS: d.ws, Started: time.Now(), priv: cfg.Privileges}
		for _, s := range Steps {
			if s.Name == "broker" {
				break
			}
			if err := s.Run(st); err != nil {
				t.Fatalf("boot step %s: %v", s.Name, err)
			}
		}
		if st.Deployments == nil {
			t.Fatalf("--tile-deployments=%q: no plane before the broker step (the first Provision)", c.value)
		}
		if st.Deployments.OptInClosed != c.closed {
			t.Errorf("--tile-deployments=%q: plane OptInClosed = %v, want %v", c.value, st.Deployments.OptInClosed, c.closed)
		}
		if st.Reg.PinnedPrimary == nil {
			t.Errorf("--tile-deployments=%q: the registry hooks are not installed before the first Provision", c.value)
		}
	}
}
