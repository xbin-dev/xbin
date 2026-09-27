package deployments

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// The runner facade is the runner itself in xbind.
var _ Runner = (*runner.Runner)(nil)

// zsRunner is a runner facade that fails the test on any call: nothing in
// the zero state asks the runner for anything.
type zsRunner struct{ t *testing.T }

func (r zsRunner) Deploy(context.Context, *registry.Component, string, runner.Code, func() error, runner.DeployProgress) error {
	r.t.Error("Deploy called in the zero state")
	return nil
}

func (r zsRunner) ChangedDeployment(*registry.Component, string) {
	r.t.Error("ChangedDeployment called in the zero state")
}

func (r zsRunner) ChangedTile(*registry.Component) {
	r.t.Error("ChangedTile called in the zero state")
}

func (r zsRunner) StopDeployment(string, string) {
	r.t.Error("StopDeployment called in the zero state")
}

func (r zsRunner) RootsInUse() []string {
	r.t.Error("RootsInUse called in the zero state")
	return nil
}

// covers P5 PO-7 PO-12 NP-14-5 — every hook the plane answers for the
// registry, the runner, the broker, the server, the terminal manager and the
// watcher gives today's answer for a tile without a record, the same whether
// the ship-dark switch is on or off (the zero-state path never reads it); the
// server's three answers equal server.NoopPolicy's; the plane calls no input
// but TileEnv, and the workspace gains no file.
func TestPlaneZeroState(t *testing.T) {
	root := t.TempDir()
	c := &registry.Component{Path: "apps/crm", Dir: filepath.Join(root, "apps", "crm")}
	env := []string{"XBIN_RES_DB=/r/db"}
	fail := func(what string) func() { return func() { t.Errorf("%s called in the zero state", what) } }

	var answers []any
	for _, closed := range []bool{false, true} {
		p := &Plane{Root: root, Run: zsRunner{t}, OptInClosed: closed,
			OwnerRef:         func(string) string { t.Error("OwnerRef called in the zero state"); return "" },
			IsAdmin:          func(auth.Principal) bool { t.Error("IsAdmin called in the zero state"); return false },
			MayManage:        func(auth.Principal, string) bool { t.Error("MayManage called in the zero state"); return false },
			TileEnv:          func(got *registry.Component) []string { return env },
			Provision:        fail("Provision"),
			ReconcileIngress: fail("ReconcileIngress"),
		}
		if err := p.Boot(); err != nil {
			t.Fatalf("Boot: %v", err)
		}

		// The runner's hooks: main runs the work tree with today's env.
		code, err := p.CodeFor(c.Path, util.MainDeployment)
		if err != nil || code != (runner.Code{WorkTree: true}) {
			t.Errorf("CodeFor(main) = %+v, %v; want the work tree", code, err)
		}
		if _, err := p.CodeFor(c.Path, "dev"); !errors.Is(err, util.ErrNoDeployment) {
			t.Errorf("CodeFor(dev) = %v, want util.ErrNoDeployment", err)
		}
		if got := p.Primary(c.Path); got != util.MainDeployment {
			t.Errorf("Primary = %q, want main", got)
		}
		if v, err := p.View(c, runner.Code{WorkTree: true}); v != c || err != nil {
			t.Errorf("View(work tree) = %p, %v; want the registry's own pointer", v, err)
		}
		if v, err := p.View(c, runner.Code{Tree: "3f2a"}); v != nil || err == nil {
			t.Errorf("View(checkpoint) = %p, %v; want an error", v, err)
		}
		if r, err := p.Materialize(c.Path, "3f2a"); r != "" || err == nil {
			t.Errorf("Materialize = %q, %v; want an error", r, err)
		}
		if e, remap := p.EnvFor(c, util.MainDeployment); !reflect.DeepEqual(e, env) || remap != nil {
			t.Errorf("EnvFor(main) = %v, %v; want the tile's env and no remap", e, remap)
		}
		if e, remap := p.EnvFor(c, "dev"); e != nil || remap == nil || len(remap) != 0 {
			t.Errorf("EnvFor(dev) = %v, %v; want no env and an empty remap (nothing of main's bound)", e, remap)
		}

		// The registry's hooks: nothing is pinned, every scope is the work tree's.
		if pc, ok := p.PinnedPrimary(c.Path); pc != nil || ok {
			t.Errorf("PinnedPrimary = %v, %v; want nothing", pc, ok)
		}
		if res, ok := p.ScopeResources(c.Path); res != nil || ok {
			t.Errorf("ScopeResources = %v, %v; want the work tree's", res, ok)
		}

		// The broker's tile-life hooks: nothing to rewrite, reset or list.
		if err := p.RewriteDeploymentOwner(c.Path, "user:ana"); err != nil {
			t.Errorf("RewriteDeploymentOwner: %v", err)
		}
		if err := p.ResetDeploymentState(c.Path); err != nil {
			t.Errorf("ResetDeploymentState: %v", err)
		}
		if l := p.DeploymentLeftovers(c.Path); l != nil {
			t.Errorf("DeploymentLeftovers = %v", l)
		}

		// The server's questions, exactly as server.NoopPolicy answers them.
		noop := server.NoopPolicy{}
		ana := auth.Principal{UserID: "ana"}
		for _, dep := range []string{"", util.MainDeployment, "dev", "Main"} {
			r1, p1, e1 := p.CodeRoot(c, dep)
			r2, p2, e2 := noop.CodeRoot(c, dep)
			if r1 != r2 || p1 != p2 || (e1 == nil) != (e2 == nil) || (e1 != nil && e1.Error() != e2.Error()) {
				t.Errorf("CodeRoot(%q) = %q %v %v; NoopPolicy answers %q %v %v", dep, r1, p1, e1, r2, p2, e2)
			}
			if got, want := p.HasDeployment(c.Path, dep), noop.HasDeployment(c.Path, dep); got != want {
				t.Errorf("HasDeployment(%q) = %v, NoopPolicy answers %v", dep, got, want)
			}
		}
		if got, want := p.Addressable(ana, c.Path), noop.Addressable(ana, c.Path); !reflect.DeepEqual(got, want) {
			t.Errorf("Addressable = %v, NoopPolicy answers %v", got, want)
		}

		// The terminal manager's and the watcher's questions.
		if p.HasRecord(c.Path) {
			t.Error("HasRecord: a tile without a record has one")
		}
		dep, attached := p.LiveReload(c.Path)
		if dep != util.MainDeployment || !attached {
			t.Errorf("LiveReload = %q, %v; want main, attached", dep, attached)
		}
		p.WorkTreeMoved(c.Path)

		answers = append(answers, [...]any{code, dep, attached, p.Primary(c.Path), p.HasDeployment(c.Path, "dev")})
	}
	if !reflect.DeepEqual(answers[0], answers[1]) {
		t.Errorf("the ship-dark switch changed a zero-state answer: on %v, off %v", answers[0], answers[1])
	}

	if ents, err := os.ReadDir(root); err != nil || len(ents) != 0 {
		t.Errorf("the zero-state plane wrote into the workspace: %v %v", ents, err)
	}
}

// covers P5 — a plane with no inputs (a literal, as registerDeploymentsAPI's
// route test builds it) answers the primary's env as the runner's own
// fallback does without EnvForComponent: none, and no remap.
func TestPlaneZeroStateNoInputs(t *testing.T) {
	var p Plane
	c := &registry.Component{Path: "apps/x", Dir: "/w/apps/x"}
	if e, remap := p.EnvFor(c, util.MainDeployment); e != nil || remap != nil {
		t.Errorf("EnvFor(main) with no TileEnv = %v, %v", e, remap)
	}
	if err := p.Boot(); err != nil {
		t.Errorf("Boot: %v", err)
	}
}
