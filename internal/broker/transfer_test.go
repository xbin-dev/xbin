package broker

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// D39 §1 RECEIVE matrix: {ws-admin, org-admin, member+create, member,
// non-member} × {org, self, other-user, workspace}. GIVE is arranged per
// caller (ws-admin: any tile; others: a tile they user-own), so each cell
// isolates the RECEIVE rule.
func TestTransferReceiveMatrix(t *testing.T) {
	b, st := orgFixture(t) // sales: carol admin, bob member+create, alice member(read); dave no org
	cases := []struct {
		name  string
		who   string // "" = ws-admin root
		to    string
		want  bool
		owner string // tile owner arranged for GIVE
	}{
		{"ws-admin→org", "", "org:sales", true, "user:bob"},
		{"ws-admin→self", "", "user:bob", true, "user:carol"},
		{"ws-admin→other", "", "user:alice", true, "user:bob"},
		{"ws-admin→workspace", "", "", true, "user:bob"},

		{"org-admin→org (admin implies create)", "carol", "org:sales", true, "org:sales"},
		{"org-admin→self", "carol", "user:carol", true, "org:sales"},
		{"org-admin→other member", "carol", "user:bob", false, "org:sales"},
		{"org-admin→workspace", "carol", "", false, "org:sales"},

		{"member+create→org", "bob", "org:sales", true, "user:bob"},
		{"member+create→self (no-op)", "bob", "user:bob", true, "user:bob"},
		{"member+create→other", "bob", "user:alice", false, "user:bob"},
		{"member+create→workspace", "bob", "", false, "user:bob"},

		{"member(no create)→org", "alice", "org:sales", false, "user:alice"},
		{"member→self", "alice", "user:alice", true, "user:alice"},
		{"member→other", "alice", "user:bob", false, "user:alice"},
		{"member→workspace", "alice", "", false, "user:alice"},

		{"non-member→org", "dave", "org:sales", false, "user:dave"},
		{"non-member→self", "dave", "user:dave", true, "user:dave"},
		{"non-member→other", "dave", "user:alice", false, "user:dave"},
		{"non-member→workspace", "dave", "", false, "user:dave"},
	}
	for _, c := range cases {
		if err := st.SetOwner("apps/email", c.owner); err != nil {
			t.Fatal(err)
		}
		p := auth.Principal{Owner: true}
		if c.who != "" {
			p = principalFor(t, st, c.who)
		}
		msg := b.transferAllowed(p, st, "apps/email", c.to)
		if (msg == "") != c.want {
			t.Errorf("%s: allowed=%v (msg %q), want %v", c.name, msg == "", msg, c.want)
		}
	}
	// GIVE still gates: bob cannot move a tile he doesn't own even to an org
	// he could create in.
	if err := st.SetOwner("apps/email", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if msg := b.transferAllowed(principalFor(t, st, "bob"), st, "apps/email", "org:sales"); msg == "" ||
		!strings.Contains(msg, "user-owner") {
		t.Errorf("GIVE must gate first: %q", msg)
	}
}

// transferFixture: apps/email owned by bob with a net binding + an iface
// binding + grants; org "locked" denies net and allow-lists calls.
func transferFixture(t *testing.T) (*Broker, *users.Store) {
	t.Helper()
	b, st := orgFixture(t)
	if _, err := st.UpsertOrg(users.Org{ID: "locked", Members: []users.Member{
		{ID: "bob", Level: users.LevelWrite, Create: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgPolicy("locked", []users.PolicyRow{
		{Tiles: "*", Deny: []string{users.PolicyDenyNet}, MayCall: []string{"apps/allowed*"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/email", "user:bob"); err != nil {
		t.Fatal(err)
	}
	c, ok := b.Reg.Component("apps/email")
	if !ok {
		t.Fatal("fixture component missing")
	}
	c.Manifest.Interfaces = map[string]registry.Iface{
		"net": {Kind: "net"},
		"api": {Kind: "http", Service: "api", Multi: true},
	}
	if cal, ok := b.Reg.Component("apps/calendar"); ok {
		cal.Manifest.Provides = map[string]registry.Iface{"net": {Kind: "net"}}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{
			"apps/email": {
				"net": registry.BindTo("apps/calendar"), // provider-tile net ref
				"api": registry.BindTo("apps/calendar", "apps/allowed-x"),
			},
		}
	}); err != nil {
		t.Fatal(err)
	}
	return b, st
}

// D39 §2: preview reports each impact class without mutating anything.
func TestTransferPreview(t *testing.T) {
	b, st := transferFixture(t)
	bob := principalFor(t, st, "bob")
	rep := b.transferPreview(bob, st, "apps/email", "org:locked")
	if !rep.Allowed {
		t.Fatalf("bob holds Create in locked: %+v", rep)
	}
	// callerLevel: owner terminal → member write.
	if rep.CallerLevel["before"] != "terminal" || rep.CallerLevel["after"] != "write" {
		t.Errorf("callerLevel = %v", rep.CallerLevel)
	}
	// net slot dead (org denies net); api slot PARTIAL (apps/allowed-x
	// covered by mayCall, apps/calendar not) → stays.
	if len(rep.DeadBind) != 1 || rep.DeadBind[0].Slot != "net" ||
		!strings.Contains(rep.DeadBind[0].Reason, "denies net") {
		t.Errorf("deadBindings = %+v", rep.DeadBind)
	}
	// The pre-existing grant apps/email→apps/calendar dies under mayCall.
	found := false
	for _, g := range rep.DeadGrants {
		if g.Target == "apps/calendar" && strings.Contains(g.Reason, "allow-lists") {
			found = true
		}
	}
	if !found {
		t.Errorf("deadGrants = %+v", rep.DeadGrants)
	}
	if len(rep.Plane) == 0 || !strings.Contains(rep.Plane[0], "org:locked admins gain full control") {
		t.Errorf("planeChanges = %v", rep.Plane)
	}
	// Preview mutated nothing.
	if st.Owner("apps/email") != "user:bob" {
		t.Fatal("preview must not mutate ownership")
	}
	if len(b.Reg.Workspace().Bindings["apps/email"]) != 2 {
		t.Fatal("preview must not mutate bindings")
	}
	// The API shape carries the JSON field names the spec pins.
	w := call(t, b.apiOwnerPreview, bob, "GET", "/owner/preview?tile=apps/email&to=org:locked", "", nil)
	if w.Code != 200 {
		t.Fatalf("preview API: %d %s", w.Code, w.Body.String())
	}
	var shape map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &shape); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tile", "from", "to", "allowed", "callerLevel", "deadBindings", "deadGrants", "planeChanges"} {
		if _, ok := shape[k]; !ok {
			t.Errorf("preview missing field %q", k)
		}
	}
}

// D39 §3: the transfer unbinds hard-dead slots, keeps partial slots and
// grants, restarts the tile AND the old net provider, and reports it all.
func TestTransferSideEffects(t *testing.T) {
	b, st := transferFixture(t)
	var restarted []string
	b.OnGrantChange = func(comp string) { restarted = append(restarted, comp) }

	bob := principalFor(t, st, "bob")
	w := call(t, b.apiOwnerTransfer, bob, "POST", "/owner",
		`{"tile":"apps/email","to":"org:locked"}`, nil)
	if w.Code != 200 {
		t.Fatalf("transfer: %d %s", w.Code, w.Body.String())
	}
	var rep transferReport
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatal(err)
	}
	if st.Owner("apps/email") != "org:locked" {
		t.Fatal("ownership must move")
	}
	if len(rep.Unbound) != 1 || rep.Unbound[0] != "net" {
		t.Errorf("unbound = %v", rep.Unbound)
	}
	slots := b.Reg.Workspace().Bindings["apps/email"]
	if _, dead := slots["net"]; dead {
		t.Error("dead net slot must be unbound")
	}
	if _, alive := slots["api"]; !alive {
		t.Error("partial api slot must survive")
	}
	// Grants stay stored (inert, human's call to revoke).
	kept := false
	for _, g := range b.Reg.Workspace().Grants {
		if g.From == "apps/email" && g.Target == "apps/calendar" {
			kept = true
		}
	}
	if !kept {
		t.Error("ceiling-dead grants must stay stored")
	}
	// Restarts: the tile and the old net provider.
	tile, prov := false, false
	for _, c := range restarted {
		if c == "apps/email" {
			tile = true
		}
		if c == "apps/calendar" {
			prov = true
		}
	}
	if !tile || !prov {
		t.Errorf("restarts = %v (want tile + old net provider)", restarted)
	}
	// Reverse transfer restores nothing silently.
	carol := principalFor(t, st, "carol")
	_ = carol
	root := auth.Principal{Owner: true}
	w = call(t, b.apiOwnerTransfer, root, "POST", "/owner", `{"tile":"apps/email","to":"user:bob"}`, nil)
	if w.Code != 200 {
		t.Fatalf("reverse transfer: %d %s", w.Code, w.Body.String())
	}
	if _, back := b.Reg.Workspace().Bindings["apps/email"]["net"]; back {
		t.Error("unbound slots must not resurrect on reverse transfer")
	}
}

// users-side parity: CeilingFor(path, currentOwner) == Ceiling(path), and
// the org override picks the org's rows on an unowned path; WithOwner flips
// the caller's resolved level.
func TestCeilingForAndWithOwner(t *testing.T) {
	_, st := transferFixture(t)
	if err := st.SetOwner("apps/email", "org:locked"); err != nil {
		t.Fatal(err)
	}
	same := st.Ceiling("apps/email").Denies(users.PolicyDenyNet)
	viaFor := st.CeilingFor("apps/email", "org:locked").Denies(users.PolicyDenyNet)
	if !same || same != viaFor {
		t.Fatalf("parity: Ceiling=%v CeilingFor=%v", same, viaFor)
	}
	if st.CeilingFor("apps/email", "user:bob").Denies(users.PolicyDenyNet) {
		t.Fatal("user-owner override must drop the org rows")
	}
	if !st.CeilingFor("apps/other", "org:locked").Denies(users.PolicyDenyNet) {
		t.Fatal("org override must pick the org rows for any path")
	}
	acc, _ := st.Access("bob")
	if l := acc.TileLevel("apps/email"); l != "write" {
		t.Fatalf("member level = %q", l)
	}
	if l := acc.WithOwner("apps/email", "user:bob").TileLevel("apps/email"); l != "terminal" {
		t.Fatalf("WithOwner(user:bob) = %q, want terminal", l)
	}
	if l := acc.WithOwner("apps/email", "").TileLevel("apps/email"); l == "terminal" {
		t.Fatalf("workspace override must not confer ownership: %q", l)
	}
}
