package broker

import (
	"errors"
	"reflect"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers D127f D127k T9 — MayManageDeployments is the manager gate: a person in
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

// covers D127m T9 — AdminFrameDriver (D127m extended by the owner 2026-09-28):
// a frame of a tile holding xbin admin, minted under a person's own login,
// stands in for that person at the manager gate, and the person is judged —
// a workspace admin or the owning org's admin passes on their tiles, the
// root token's login everywhere, a non-manager nowhere. A frame minted by a
// terminal or agent session, a view-as frame, a frame of a tile without
// xbin admin, the admin tile's terminal, agent and instance tokens, and a
// disabled account's frame have no driver.
func TestAdminFrameDriver(t *testing.T) {
	b, st := orgFixture(t) // apps/email is owned by org:sales, carol its admin
	if err := st.SetOwner("apps/calendar", "user:dave"); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/calendar", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "gone", Role: users.RoleAdmin, Disabled: true}, "password"); err != nil {
		t.Fatal(err)
	}
	const admin = "apps/calendar" // the tile granted xbin admin: the admin tile here
	frame := func(uid, gen string) auth.Principal {
		return auth.Principal{Component: admin, UserID: uid, Via: "frame", Gen: gen}
	}
	viewAs := frame("bob", "s.v")
	viewAs.Impersonator = "root2"
	for _, tc := range []struct {
		name   string
		p      auth.Principal
		driver bool
		// manages: the tiles the driver passes MayManageDeployments on
		manages map[string]bool
	}{
		{"a workspace admin's login frame", frame("root2", "s.a"), true, map[string]bool{"apps/email": true, admin: true}},
		{"the owning org's admin's login frame", frame("carol", "s.c"), true, map[string]bool{"apps/email": true, admin: false}},
		{"the tile's user-owner's login frame", frame("dave", "s.d"), true, map[string]bool{"apps/email": false, admin: true}},
		{"a non-manager's login frame", frame("bob", "s.b"), true, map[string]bool{"apps/email": false, admin: false}},
		{"the root token's login frame", frame("", "o.x"), true, map[string]bool{"apps/email": true, admin: true}},
		{"a frame a user's terminal minted", frame("root2", "u.e.0"), false, nil},
		{"a frame an owner-driven terminal minted", frame("", "t.x"), false, nil},
		{"a legacy frame (no generation)", frame("root2", ""), false, nil},
		{"a view-as frame", viewAs, false, nil},
		{"a disabled account's frame", frame("gone", "s.g"), false, nil},
		{"a frame of a tile without xbin admin", auth.Principal{Component: "apps/email", UserID: "root2", Via: "frame", Gen: "s.a"}, false, nil},
		{"the admin tile's terminal token", auth.Principal{Component: admin, UserID: "root2", Via: "terminal"}, false, nil},
		{"the admin tile's agent token", auth.Principal{Component: admin, UserID: "root2", Via: "agent", Gen: "s.a"}, false, nil},
		{"the admin tile's instance token", auth.Principal{Component: admin, Via: "instance"}, false, nil},
		{"a person in their own session", principalFor(t, st, "root2"), false, nil},
	} {
		d, ok := b.AdminFrameDriver(tc.p)
		if ok != tc.driver {
			t.Errorf("%s: driver %v, want %v", tc.name, ok, tc.driver)
			continue
		}
		if !ok {
			continue
		}
		if d.Component != "" || d.ReadOnly() || (d.UserID != tc.p.UserID) {
			t.Errorf("%s: driver %+v isn't the person", tc.name, d)
		}
		for tile, want := range tc.manages {
			if got := b.MayManageDeployments(d, tile); got != want {
				t.Errorf("%s: manages %s %v, want %v", tc.name, tile, got, want)
			}
		}
	}
	// The frame itself never passes the human-session gate.
	if b.MayManageDeployments(frame("root2", "s.a"), "apps/email") {
		t.Error("the admin tile's frame passed MayManageDeployments itself")
	}
}

// covers D119c D119i SC-ZERO — with no hook installed the broker's deployment
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
