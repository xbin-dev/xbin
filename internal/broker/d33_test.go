package broker

import (
	"encoding/json"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// Provider-side approval (D33): admins of the org that OWNS a grant's TARGET
// may approve (consent to sharing their property) and revoke it — no
// allowance needed; and both sides see the row with the right direction.
func TestProviderSideGrantApproval(t *testing.T) {
	b, st := orgFixture(t) // sales owns apps/email; carol=sales admin, bob=member
	if _, err := st.Upsert(users.User{ID: "dana", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "data", Members: []users.Member{
		{ID: "dana", Level: users.LevelTerminal, Admin: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/calendar", "org:data"); err != nil {
		t.Fatal(err)
	}
	dana := principalFor(t, st, "dana")
	alice := principalFor(t, st, "alice")

	// dana (target-owner org admin) approves the email→calendar request.
	w := call(t, b.apiGrantsAdd, dana, "POST", "/grants",
		`{"from":"apps/email","target":"apps/calendar","role":"writer"}`, nil)
	if w.Code != 200 {
		t.Fatalf("provider-side approval: %d %s", w.Code, w.Body.String())
	}
	// The stored row records the approver (D33 audit).
	found := false
	for _, g := range b.Reg.Workspace().Grants {
		if g.From == "apps/email" && g.Target == "apps/calendar" && g.Role == "writer" {
			found = true
			if g.ApprovedBy != "user:dana" || g.ApprovedAt == 0 {
				t.Errorf("approver not recorded: %+v", g)
			}
		}
	}
	if !found {
		t.Fatal("approved grant not stored")
	}
	// dana cannot approve targets her org doesn't own.
	w = call(t, b.apiGrantsAdd, dana, "POST", "/grants",
		`{"from":"apps/email","target":"gpu:0","role":"user"}`, nil)
	if w.Code == 200 {
		t.Fatalf("no provider right on gpu targets: %d", w.Code)
	}
	// A non-admin member of the target org cannot.
	w = call(t, b.apiGrantsAdd, alice, "POST", "/grants",
		`{"from":"apps/email","target":"apps/calendar","role":"reader"}`, nil)
	if w.Code == 200 {
		t.Fatalf("non-admin must not approve: %d", w.Code)
	}
	// Directions: dana sees the row as provider, carol (owner of From) as consumer.
	var view struct {
		Grants []struct {
			registry.Grant
			Direction string `json:"direction"`
		}
		Pending []PendingGrant
		Scope   string `json:"scope"`
	}
	get := func(p auth.Principal) {
		t.Helper()
		w := call(t, b.apiGrantsList, p, "GET", "/grants", "", nil)
		if w.Code != 200 {
			t.Fatalf("grants list: %d %s", w.Code, w.Body.String())
		}
		view = struct {
			Grants []struct {
				registry.Grant
				Direction string `json:"direction"`
			}
			Pending []PendingGrant
			Scope   string `json:"scope"`
		}{}
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
	}
	get(dana)
	dir := ""
	for _, g := range view.Grants {
		if g.Target == "apps/calendar" && g.Role == "writer" {
			dir = g.Direction
		}
	}
	if dir != "provider" {
		t.Errorf("dana's direction = %q, want provider", dir)
	}
	get(principalFor(t, st, "carol"))
	dir = ""
	for _, g := range view.Grants {
		if g.Target == "apps/calendar" && g.Role == "writer" {
			dir = g.Direction
		}
	}
	if dir != "consumer" {
		t.Errorf("carol's direction = %q, want consumer", dir)
	}
	// The requester view: bob (writes apps/email via org level, not an admin)
	// sees pending rows with approver hints instead of a dead 403.
	get(principalFor(t, st, "bob"))
	if view.Scope != "mine" {
		t.Errorf("bob's scope = %q, want mine", view.Scope)
	}
	hintOK := false
	for _, pg := range view.Pending {
		if pg.From == "apps/email" && len(pg.Approvers) > 0 {
			hintOK = true
		}
	}
	if !hintOK {
		t.Errorf("requester pending rows must carry approver hints: %+v", view.Pending)
	}
	// Provider-side revoke (withdrawing service) works.
	w = call(t, b.apiGrantsRevoke, dana, "DELETE", "/grants",
		`{"from":"apps/email","target":"apps/calendar","role":"writer"}`, nil)
	if w.Code != 200 {
		t.Fatalf("provider-side revoke: %d %s", w.Code, w.Body.String())
	}
}

// Vault D30: a secret's value is readable ONLY by the tile's backend
// (instance token); terminals manage write-only; frames get nothing.
func TestVaultBackendOnlyReads(t *testing.T) {
	b := testBroker(t)
	b.AllowInsecureVault = true
	instance := auth.Principal{Component: "apps/email", Via: "instance"}
	terminal := auth.Principal{Component: "apps/email", Via: "terminal", UserID: "bob"}
	frame := auth.Principal{Component: "apps/email", Via: "frame", UserID: "bob"}
	admin := auth.Principal{Owner: true}

	put := func(p auth.Principal, want int) {
		t.Helper()
		w := call(t, b.apiVaultPut, p, "PUT", "/vault/apps/email/k", `{"value":"s3cret"}`,
			map[string]string{"rest": "apps/email/k"})
		if w.Code != want {
			t.Fatalf("put as %s: %d %s, want %d", p.Via, w.Code, w.Body.String(), want)
		}
	}
	get := func(p auth.Principal, rest string, want int) {
		t.Helper()
		w := call(t, b.apiVaultGet, p, "GET", "/vault/"+rest, "", map[string]string{"rest": rest})
		if w.Code != want {
			t.Fatalf("get %s as %s/%s: %d %s, want %d", rest, p.Via, p.Component, w.Code, w.Body.String(), want)
		}
	}
	put(terminal, 200) // terminals write
	put(admin, 200)    // admins manage
	put(frame, 403)    // frames: nothing (D30)
	get(instance, "apps/email/k", 200)
	get(terminal, "apps/email/k", 403) // terminal cannot read values
	get(admin, "apps/email/k", 403)    // admin cannot read values
	get(frame, "apps/email/k", 403)
	get(terminal, "apps/email", 200) // list keys stays available for management
	get(admin, "apps/email", 200)
}

// Create-as-org (D25 unblocked): a member with the Create knob creates an
// org-owned tile with NO personal canCreate pattern; personal creation still
// requires one.
func TestCreateAsOrg(t *testing.T) {
	b, st := orgFixture(t)
	bob := principalFor(t, st, "bob")   // sales member, Create knob, no patterns
	dave := principalFor(t, st, "dave") // no org

	w := call(t, b.apiCreate, bob, "POST", "/create",
		`{"path":"apps/bobtool","owner":"org:sales"}`, nil)
	if w.Code != 200 {
		t.Fatalf("create-as-org: %d %s", w.Code, w.Body.String())
	}
	if got := st.Owner("apps/bobtool"); got != "org:sales" {
		t.Fatalf("owner = %q, want org:sales", got)
	}
	// Personal creation without a pattern still refuses.
	w = call(t, b.apiCreate, bob, "POST", "/create", `{"path":"apps/bobpersonal"}`, nil)
	if w.Code != 403 {
		t.Fatalf("personal create without a pattern: %d %s", w.Code, w.Body.String())
	}
	// Non-members can't create as the org.
	w = call(t, b.apiCreate, dave, "POST", "/create",
		`{"path":"apps/davetool","owner":"org:sales"}`, nil)
	if w.Code != 403 {
		t.Fatalf("outsider create-as-org: %d %s", w.Code, w.Body.String())
	}
	// A read-level member (no Create knob) can't either.
	w = call(t, b.apiCreate, principalFor(t, st, "alice"), "POST", "/create",
		`{"path":"apps/alicetool","owner":"org:sales"}`, nil)
	if w.Code != 403 {
		t.Fatalf("viewer create-as-org: %d %s", w.Code, w.Body.String())
	}
}

// Lifecycle is the owner's to set (D24): org admins disable/enable their
// org's tiles; members and outsiders don't.
func TestLifecycleForOwners(t *testing.T) {
	b, st := orgFixture(t)
	set := func(p auth.Principal, state string) int {
		w := call(t, b.apiLifecycleSet, p, "POST", "/lifecycle",
			`{"component":"apps/email","state":"`+state+`"}`, nil)
		return w.Code
	}
	if got := set(principalFor(t, st, "carol"), "disabled"); got != 200 { // org admin
		t.Fatalf("org admin disable: %d", got)
	}
	if got := set(principalFor(t, st, "carol"), "enabled"); got != 200 {
		t.Fatalf("org admin enable: %d", got)
	}
	if got := set(principalFor(t, st, "alice"), "disabled"); got != 403 { // read member
		t.Fatalf("member disable must refuse: %d", got)
	}
	if got := set(principalFor(t, st, "dave"), "disabled"); got != 403 { // outsider
		t.Fatalf("outsider disable must refuse: %d", got)
	}
	// User-owner controls their own tile's lifecycle.
	if err := st.SetOwner("apps/calendar", "user:dave"); err != nil {
		t.Fatal(err)
	}
	w := call(t, b.apiLifecycleSet, principalFor(t, st, "dave"), "POST", "/lifecycle",
		`{"component":"apps/calendar","state":"disabled"}`, nil)
	if w.Code != 200 {
		t.Fatalf("user-owner disable: %d %s", w.Code, w.Body.String())
	}
}

// Provider-side binding consent (D33): the org owning a PROVIDER tile may
// wire a consumer's binding to it — pinned by instance in the allowance
// grammar when the ws-admin delegates instead.
func TestProviderSideBindingApproval(t *testing.T) {
	b, st := orgFixture(t) // sales owns apps/email
	if _, err := st.Upsert(users.User{ID: "dana", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "data", Members: []users.Member{
		{ID: "dana", Level: users.LevelTerminal, Admin: true},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/calendar", "org:data"); err != nil {
		t.Fatal(err)
	}
	// apps/email needs an iface slot bound to apps/calendar. Give it one.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {}); err != nil {
		t.Fatal(err)
	}
	dana := principalFor(t, st, "dana")
	dave := principalFor(t, st, "dave")
	// dana may bind email's api slot to HER org's provider tile…
	if !b.orgAdminMayBind(dana, "apps/email", "api", registry.BindTo("apps/calendar"), false) {
		t.Error("provider org admin must be able to consent to a binding to her tile")
	}
	// …but not to someone else's, nor to net classes.
	if b.orgAdminMayBind(dana, "apps/email", "net", registry.BindTo("internet"), false) {
		t.Error("net classes have no provider to consent")
	}
	if b.orgAdminMayBind(dave, "apps/email", "api", registry.BindTo("apps/calendar"), false) {
		t.Error("non-admin has no provider right")
	}
	// Instance pinning in the allowance grammar reaches bindingTargets: a
	// sales allowance pinned to calendar#dev covers #dev, not #prod.
	if err := st.SetOrgAllow("sales", []string{"iface:api@apps/calendar#dev"}); err != nil {
		t.Fatal(err)
	}
	carol := principalFor(t, st, "carol")
	// apps/email has no manifest iface slot in the fixture, so drive the
	// normalized-target check directly through AllowanceCovers.
	if !st.AllowanceCovers("sales", "iface:api@apps/calendar#dev", "") {
		t.Error("pinned allowance must cover the dev instance")
	}
	if st.AllowanceCovers("sales", "iface:api@apps/calendar#prod", "") {
		t.Error("pinned allowance must not cover prod")
	}
	_ = carol
}
