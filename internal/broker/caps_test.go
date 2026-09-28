package broker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// capEvent is one OnCapChange call.
type capEvent struct {
	tile, capTarget string
	held            bool
}

// capChangeRecorder wires OnCapChange and OnGrantChange to recorders.
func capChangeRecorder(b *Broker) (caps *[]capEvent, restarts *[]string) {
	caps, restarts = &[]capEvent{}, &[]string{}
	b.OnCapChange = func(tile, capTarget string, held bool) {
		*caps = append(*caps, capEvent{tile, capTarget, held})
	}
	b.OnGrantChange = func(comp string) { *restarts = append(*restarts, comp) }
	return caps, restarts
}

// declareSandboxes makes tile declare the manager's use of cap:sandboxes.
func declareSandboxes(t *testing.T, b *Broker, tile string) {
	t.Helper()
	mf := filepath.Join(b.Reg.Root, filepath.FromSlash(tile), "xbin.json")
	if err := os.WriteFile(mf, []byte(`{"uses":[{"target":"cap:sandboxes","role":"writer"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
}

func grantBody(from, target, role string) string {
	return `{"from":"` + from + `","target":"` + target + `","role":"` + role + `"}`
}

// cap:sandboxes (D120, plans/tile-sandbox-runtime.md §8.1): declared, it lands
// pending and unlocks nothing; only a workspace admin approves it; approving
// restarts no backend.
func TestSandboxesCapApproval(t *testing.T) {
	b := testBroker(t)
	caps, restarts := capChangeRecorder(b)
	root := auth.Principal{Owner: true}

	declareSandboxes(t, b, "apps/calendar")
	pending := false
	for _, p := range b.Pending() {
		if p.From == "apps/calendar" && p.Target == SandboxesCap {
			pending = true
		}
	}
	if !pending {
		t.Fatal("a declared cap:sandboxes must land pending")
	}
	if b.SandboxesFor("apps/calendar") {
		t.Fatal("declaring the cap must not grant it (never same-scope auto-granted)")
	}
	if b.SandboxesFor("") || b.SandboxesFor("apps/nope") {
		t.Fatal("no tile, no cap")
	}

	w := call(t, b.apiGrantsAdd, root, "POST", "/grants", grantBody("apps/calendar", SandboxesCap, "writer"), nil)
	if w.Code != 200 {
		t.Fatalf("a workspace admin approves: %d %s", w.Code, w.Body.String())
	}
	if !b.SandboxesFor("apps/calendar") {
		t.Fatal("approved: the tile holds cap:sandboxes")
	}
	for _, p := range b.Pending() {
		if p.From == "apps/calendar" && p.Target == SandboxesCap {
			t.Fatal("approved: no longer pending")
		}
	}
	if len(*restarts) != 0 {
		t.Fatalf("approving cap:sandboxes must restart no backend: %v", *restarts)
	}
	if len(*caps) != 1 || (*caps)[0] != (capEvent{"apps/calendar", SandboxesCap, true}) {
		t.Fatalf("approve reports held=true: %v", *caps)
	}
	// A grant row left behind by a removed tile holds nothing.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/gone", Target: SandboxesCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if b.SandboxesFor("apps/gone") {
		t.Fatal("a removed tile's leftover row must not hold the cap")
	}
}

// No allowance delegates cap:sandboxes, not even cap:* — an org admin (D26)
// and a personal tile's owner (D88) both get 403 where cap:containers under
// the same allowance goes through; the approver hint names only the
// workspace admin.
func TestSandboxesCapNotDelegable(t *testing.T) {
	b, st := orgFixture(t) // carol: admin of sales, which owns apps/email
	carol := principalFor(t, st, "carol")
	grant := func(p auth.Principal, from, target string) int {
		t.Helper()
		return call(t, b.apiGrantsAdd, p, "POST", "/grants", grantBody(from, target, "writer"), nil).Code
	}

	// A literal entry is refused at write, for orgs and permission sets alike.
	if err := st.SetOrgAllow("sales", []string{SandboxesCap}); err == nil {
		t.Fatal("an allowance naming cap:sandboxes must be refused")
	}
	if err := st.UpsertPermissionSet("mgr", users.PermissionSet{Allow: []string{SandboxesCap}}); err == nil {
		t.Fatal("a permission set naming cap:sandboxes must be refused")
	}
	w := call(t, b.apiPermSetPut, auth.Principal{Owner: true}, "PUT", "/permission-sets/mgr",
		`{"allow":["cap:sandboxes"]}`, map[string]string{"name": "mgr"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "never delegable") {
		t.Fatalf("the API refuses it with the reason: %d %s", w.Code, w.Body.String())
	}

	// Org admin with cap:* in the allowance.
	if err := st.SetOrgAllow("sales", []string{"cap:*"}); err != nil {
		t.Fatal(err)
	}
	if c := grant(carol, "apps/email", ContainersCap); c != 200 {
		t.Fatalf("cap:* delegates cap:containers: %d", c)
	}
	if c := grant(carol, "apps/email", SandboxesCap); c != 403 {
		t.Fatalf("an org admin with cap:* must not approve cap:sandboxes: %d", c)
	}
	if b.SandboxesFor("apps/email") {
		t.Fatal("refused approval: nothing held")
	}
	hint := func(from string) string {
		return strings.Join(b.approverHint(registry.Grant{From: from, Target: SandboxesCap, Role: "writer"}), ",")
	}
	if h := hint("apps/email"); h != "workspace-admin" {
		t.Errorf("hint for an org tile %q: only a workspace admin approves", h)
	}

	// Personal owner with cap:* in a personal permission set.
	if err := st.SetOwner("apps/calendar", "user:bob"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	if err := st.UpsertPermissionSet("caps", users.PermissionSet{Allow: []string{"cap:*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{Sets: &[]string{"caps"}}); err != nil {
		t.Fatal(err)
	}
	if c := grant(bob, "apps/calendar", ContainersCap); c != 200 {
		t.Fatalf("the personal allowance delegates cap:containers: %d", c)
	}
	if c := grant(bob, "apps/calendar", SandboxesCap); c != 403 {
		t.Fatalf("a personal owner with cap:* must not approve cap:sandboxes: %d", c)
	}
	if h := hint("apps/calendar"); h != "workspace-admin" {
		t.Errorf("hint for a personal tile %q: only a workspace admin approves", h)
	}

	// The workspace admin still can.
	if c := grant(principalFor(t, st, "root2"), "apps/calendar", SandboxesCap); c != 200 {
		t.Fatalf("a workspace admin approves: %d", c)
	}
	if !b.SandboxesFor("apps/calendar") {
		t.Fatal("approved by the workspace admin: held")
	}
	// Narrowing needs nothing: the owner revokes on their own tile.
	if c := call(t, b.apiGrantsRevoke, bob, "DELETE", "/grants", grantBody("apps/calendar", SandboxesCap, "writer"), nil).Code; c != 200 {
		t.Fatalf("the owner revokes: %d", c)
	}
	if b.SandboxesFor("apps/calendar") {
		t.Fatal("revoked: not held")
	}
}

// The ceiling: an xbin-caps deny strips cap:sandboxes at evaluation (and
// refuses the approval up front, naming the class); a mayCall allow-list
// must not — an unlisted cap would fall into the path matcher and be
// silently stripped (the 2026-07-12 regression class).
func TestSandboxesCapCeiling(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: SandboxesCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.SandboxesFor("apps/email") {
		t.Fatal("granted: held")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if !b.SandboxesFor("apps/email") {
		t.Fatal("a mayCall allow-list must not strip cap:sandboxes")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if b.SandboxesFor("apps/email") {
		t.Fatal("an xbin-caps deny must strip cap:sandboxes")
	}
	if msg := b.ceilingBlockMsg("apps/email", SandboxesCap); !strings.Contains(msg, users.PolicyDenyXbinCaps) {
		t.Fatalf("the block message names the deny class: %q", msg)
	}
	w := call(t, b.apiGrantsAdd, auth.Principal{Owner: true}, "POST", "/grants", grantBody("apps/calendar", SandboxesCap, "writer"), nil)
	if w.Code != 400 || !strings.Contains(w.Body.String(), users.PolicyDenyXbinCaps) {
		t.Fatalf("approving over the ceiling is refused with the row named: %d %s", w.Code, w.Body.String())
	}
}

// Losing the cap reaches OnCapChange: a revoke (held=false, and no backend
// restart), a revoke that leaves another row (still held), and a ceiling
// change that strips it (the usersEvent sweep).
func TestSandboxesCapRevokeFires(t *testing.T) {
	b := testBroker(t)
	caps, restarts := capChangeRecorder(b)
	root := auth.Principal{Owner: true}
	approve := func(role string) {
		t.Helper()
		if w := call(t, b.apiGrantsAdd, root, "POST", "/grants", grantBody("apps/email", SandboxesCap, role), nil); w.Code != 200 {
			t.Fatalf("approve %s: %d %s", role, w.Code, w.Body.String())
		}
	}
	revoke := func(role string) {
		t.Helper()
		if w := call(t, b.apiGrantsRevoke, root, "DELETE", "/grants", grantBody("apps/email", SandboxesCap, role), nil); w.Code != 200 {
			t.Fatalf("revoke %s: %d %s", role, w.Code, w.Body.String())
		}
	}
	last := func() capEvent {
		t.Helper()
		if len(*caps) == 0 {
			t.Fatal("OnCapChange never fired")
		}
		return (*caps)[len(*caps)-1]
	}

	approve("writer")
	revoke("writer")
	if got := last(); got != (capEvent{"apps/email", SandboxesCap, false}) {
		t.Fatalf("a revoke fires OnCapChange(tile, cap, false): %+v", got)
	}
	if len(*restarts) != 0 {
		t.Fatalf("neither approve nor revoke restarts the backend: %v", *restarts)
	}

	// Two rows: revoking one leaves the cap held.
	approve("writer")
	approve("admin")
	revoke("admin")
	if got := last(); got != (capEvent{"apps/email", SandboxesCap, true}) {
		t.Fatalf("revoking one of two rows keeps it held: %+v", got)
	}

	// A ceiling change strips it without touching the grant.
	*caps = nil
	b.usersEvent() // nothing moved: nothing reported
	if len(*caps) != 0 {
		t.Fatalf("a sweep with the cap held reports nothing: %v", *caps)
	}
	w := call(t, b.apiPolicyPut, root, "PUT", "/policy", `{"policy":[{"tiles":"apps/*","deny":["xbin-caps"]}]}`, nil)
	if w.Code != 200 {
		t.Fatalf("policy put: %d %s", w.Code, w.Body.String())
	}
	if got := last(); got != (capEvent{"apps/email", SandboxesCap, false}) {
		t.Fatalf("a ceiling change that strips it fires OnCapChange(tile, cap, false): %+v", got)
	}

	// Other caps keep their spawn restarts and report too.
	*caps, *restarts = nil, nil
	if err := b.Users.SetPolicy(nil); err != nil {
		t.Fatal(err)
	}
	if w := call(t, b.apiGrantsAdd, root, "POST", "/grants", grantBody("apps/calendar", ContainersCap, "writer"), nil); w.Code != 200 {
		t.Fatalf("approve containers: %d", w.Code)
	}
	if len(*restarts) != 1 || (*restarts)[0] != "apps/calendar" {
		t.Fatalf("cap:containers still restarts the backend: %v", *restarts)
	}
	if got := last(); got != (capEvent{"apps/calendar", ContainersCap, true}) {
		t.Fatalf("every cap: change is reported: %+v", got)
	}
}
