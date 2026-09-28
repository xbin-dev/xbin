package broker

import (
	"errors"
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers P11 P19 T9 — MayManageDeployments is the manager gate: a person in
// their own session who is a workspace admin or manages the tile (its
// user-owner, an admin of its owning org). Tile credentials never pass —
// terminal and agent tokens of managers, frame and instance tokens, and an
// element whose tile holds xbin:admin and xbin:users grants, which IsAdmin
// and the user-management gate do admit.
func TestMayManageDeployments(t *testing.T) {
	b, st := orgFixture(t) // apps/email is owned by org:sales, carol its admin
	if err := st.SetOwner("apps/calendar", "user:dave"); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants,
			registry.Grant{From: "apps/calendar", Target: "xbin", Role: "admin"},
			registry.Grant{From: "apps/calendar", Target: "xbin:users", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	carol := principalFor(t, st, "carol")
	carolTerm := carol
	carolTerm.Component, carolTerm.Via = "apps/email", "terminal"
	carolAgent := carolTerm
	carolAgent.Via = "agent"
	granted := auth.Principal{Component: "apps/calendar", Via: "instance"}
	if !b.IsAdmin(granted) || !b.canManageUsers(granted) {
		t.Fatal("fixture: the xbin-granted element should pass IsAdmin and the user-management gate")
	}

	for _, tc := range []struct {
		name string
		p    auth.Principal
		tile string
		want bool
	}{
		{"the root token", auth.Principal{Owner: true, Via: "bearer"}, "apps/email", true},
		{"a workspace admin", principalFor(t, st, "root2"), "apps/email", true},
		{"the owning org's admin", carol, "apps/email", true},
		{"the tile's user-owner", principalFor(t, st, "dave"), "apps/calendar", true},
		{"an org developer (terminal level)", principalFor(t, st, "bob"), "apps/email", false},
		{"an org viewer", principalFor(t, st, "alice"), "apps/email", false},
		{"an outsider", principalFor(t, st, "dave"), "apps/email", false},
		{"an org admin on a tile her org doesn't own", carol, "apps/calendar", false},
		{"the org admin's terminal token", carolTerm, "apps/email", false},
		{"the org admin's agent token", carolAgent, "apps/email", false},
		{"the tile's frame", auth.Principal{Component: "apps/email", UserID: "carol", Via: "frame"}, "apps/email", false},
		{"the tile's instance", auth.Principal{Component: "apps/email", Via: "instance"}, "apps/email", false},
		{"an element holding xbin and xbin:users", granted, "apps/calendar", false},
		{"an element holding xbin and xbin:users, on another tile", granted, "apps/email", false},
		{"cron", auth.Principal{Component: CronPrincipal, Via: "cron"}, "apps/email", false},
		{"nobody", auth.Principal{}, "apps/email", false},
	} {
		if got := b.MayManageDeployments(tc.p, tc.tile); got != tc.want {
			t.Errorf("%s on %s: %v, want %v", tc.name, tc.tile, got, tc.want)
		}
	}
}

// covers P5 P29 SC-ZERO — with no hook installed the broker's deployment
// seams answer today: nothing to rewrite or reset, no leftovers, and the
// server's deployment questions answered exactly as server.NoopPolicy does.
// Installed hooks are what those seams consult.
func TestBrokerDeploymentHooksZeroState(t *testing.T) {
	b := testBroker(t)
	pol := brokerPolicy{b}
	c, _ := b.Reg.Component("apps/calendar")
	p := auth.Principal{Owner: true}

	if err := b.rewriteDeploymentOwner("apps/calendar", "user:ana"); err != nil {
		t.Errorf("rewriteDeploymentOwner: %v", err)
	}
	if err := b.resetDeploymentState("apps/calendar"); err != nil {
		t.Errorf("resetDeploymentState: %v", err)
	}
	if got := b.deploymentLeftovers("apps/calendar"); got != nil {
		t.Errorf("deploymentLeftovers = %q, want none", got)
	}
	noop := server.NoopPolicy{}
	for _, dep := range []string{"", "main", "dev"} {
		root, pinned, err := pol.CodeRoot(c, dep)
		nr, np, nerr := noop.CodeRoot(c, dep)
		if root != nr || pinned != np || (err == nil) != (nerr == nil) {
			t.Errorf("CodeRoot(%q) = (%q, %v, %v), NoopPolicy says (%q, %v, %v)", dep, root, pinned, err, nr, np, nerr)
		}
		if got, want := pol.HasDeployment(c.Path, dep), noop.HasDeployment(c.Path, dep); got != want {
			t.Errorf("HasDeployment(%q) = %v, NoopPolicy says %v", dep, got, want)
		}
	}
	if root, pinned, err := pol.CodeRoot(c, ""); root != c.Dir || pinned || err != nil {
		t.Errorf("CodeRoot(primary) = (%q, %v, %v), want the work tree", root, pinned, err)
	}
	if got := pol.Addressable(p, c.Path); !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("Addressable = %q, want [main]", got)
	}
	if _, _, _, ok := pol.PrimarySummary(c.Path); ok {
		t.Error("PrimarySummary without the hook gives the entry a summary")
	}

	// Installed, the hooks answer.
	var calls []string
	errReset := errors.New("reset failed")
	b.RewriteDeploymentOwner = func(tile, ref string) error { calls = append(calls, "owner "+tile+" "+ref); return nil }
	b.ResetDeploymentState = func(path string) error { calls = append(calls, "reset "+path); return errReset }
	b.DeploymentLeftovers = func(path string) []string { return []string{"deployment record"} }
	b.DeploymentCodeRoot = func(c *registry.Component, dep string) (string, bool, error) {
		return "/ws/.xbin/deploy/" + util.TileKey(c.Path) + "/t", true, nil
	}
	b.DeploymentExists = func(tile, name string) bool { return name == "dev" }
	b.AddressableDeployments = func(auth.Principal, string) []string { return []string{"main", "dev"} }
	b.DeploymentSummary = func(tile string) (string, bool, bool, bool) { return "main", true, false, tile == "apps/calendar" }

	if err := b.rewriteDeploymentOwner("apps/calendar", "org:sales"); err != nil {
		t.Errorf("rewriteDeploymentOwner: %v", err)
	}
	if err := b.resetDeploymentState("apps/new"); !errors.Is(err, errReset) {
		t.Errorf("resetDeploymentState: %v, want the hook's error", err)
	}
	if want := []string{"owner apps/calendar org:sales", "reset apps/new"}; !reflect.DeepEqual(calls, want) {
		t.Errorf("hook calls %q, want %q", calls, want)
	}
	if got := b.deploymentLeftovers("apps/calendar"); !reflect.DeepEqual(got, []string{"deployment record"}) {
		t.Errorf("deploymentLeftovers = %q", got)
	}
	if root, pinned, err := pol.CodeRoot(c, "dev"); !pinned || err != nil || root == c.Dir {
		t.Errorf("CodeRoot through the hook = (%q, %v, %v)", root, pinned, err)
	}
	if !pol.HasDeployment(c.Path, "dev") || pol.HasDeployment(c.Path, "main") {
		t.Error("HasDeployment doesn't follow the hook")
	}
	if got := pol.Addressable(p, c.Path); !reflect.DeepEqual(got, []string{"main", "dev"}) {
		t.Errorf("Addressable through the hook = %q", got)
	}
	if primary, pinned, protected, ok := pol.PrimarySummary(c.Path); primary != "main" || !pinned || protected || !ok {
		t.Errorf("PrimarySummary through the hook = (%q, %v, %v, %v)", primary, pinned, protected, ok)
	}
}
