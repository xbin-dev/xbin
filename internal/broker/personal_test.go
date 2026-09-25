package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// Self-approval of GRANTS on a personal tile (D88): the owner approves what
// their personal allowance covers or what they own themselves, revokes
// anything, never xbin; nobody else gains anything; the pending list tells
// the owner which rows are theirs to approve.
func TestPersonalGrantSelfApproval(t *testing.T) {
	b, st := orgFixture(t) // bob: sales member; apps/email org-owned by sales
	if err := st.SetOwner("apps/calendar", "user:bob"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	dave := principalFor(t, st, "dave")
	grant := func(p auth.Principal, method, from, target, role string) int {
		t.Helper()
		body := `{"from":"` + from + `","target":"` + target + `","role":"` + role + `"}`
		h := b.apiGrantsAdd
		if method == "DELETE" {
			h = b.apiGrantsRevoke
		}
		return call(t, h, p, method, "/grants", body, nil).Code
	}
	// no allowance yet: a capability is not his to approve…
	if c := grant(bob, "POST", "apps/calendar", "gpu:0", "egress"); c != 403 {
		t.Fatalf("gpu without allowance: %d", c)
	}
	// …his own property is (bob owns apps/crm too)
	if err := st.SetOwner("apps/crm", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if c := grant(bob, "POST", "apps/calendar", "apps/crm", "reader"); c != 200 {
		t.Fatalf("intra-user target: %d", c)
	}
	// a permission set on bob: the allowance now covers gpu
	if err := st.UpsertPermissionSet("gpu", users.PermissionSet{Allow: []string{"gpu:*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{Sets: &[]string{"gpu"}}); err != nil {
		t.Fatal(err)
	}
	if c := grant(bob, "POST", "apps/calendar", "gpu:0", "egress"); c != 200 {
		t.Fatalf("allowance-covered gpu: %d", c)
	}
	if c := grant(bob, "POST", "apps/calendar", "xbin", "writer"); c != 403 {
		t.Fatalf("xbin must never be self-approved: %d", c)
	}
	// revoke: always, even what an admin approved
	if c := grant(auth.Principal{Owner: true}, "POST", "apps/calendar", "apps/email", "reader"); c != 200 {
		t.Fatalf("admin approve: %d", c)
	}
	if c := grant(bob, "DELETE", "apps/calendar", "apps/email", "reader"); c != 200 {
		t.Fatalf("owner revokes on his own tile: %d", c)
	}
	// not his tile: nothing
	if c := grant(dave, "POST", "apps/calendar", "apps/crm", "reader"); c != 403 {
		t.Fatalf("a stranger approves on bob's tile: %d", c)
	}
	if c := grant(bob, "DELETE", "apps/email", "apps/calendar", "reader"); c != 403 {
		t.Fatalf("bob revokes on an org tile he doesn't admin: %d", c)
	}

	// Who-can-approve hints name the owner exactly when his allowance covers.
	hint := func(target string) string {
		return strings.Join(b.approverHint(registry.Grant{From: "apps/calendar", Target: target, Role: "egress"}), ",")
	}
	if h := hint("gpu:0"); !strings.Contains(h, "owner") {
		t.Errorf("covered: hint %q must name the owner", h)
	}
	if h := hint("cap:containers"); strings.Contains(h, "owner") || !strings.Contains(h, "workspace-admin") {
		t.Errorf("uncovered: hint %q must not name the owner", h)
	}
}
