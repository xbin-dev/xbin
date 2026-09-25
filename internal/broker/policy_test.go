package broker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// testUsers returns the broker's user store (testBroker always attaches one,
// mirroring production), attaching a fresh one for brokers built directly.
func testUsers(t *testing.T, b *Broker) *users.Store {
	t.Helper()
	if b.Users == nil {
		st, err := users.Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		b.Users = st
	}
	return b.Users
}

// The ceiling must cap every grant source in grantedRole: explicit rows,
// same-scope auto-grants, and capability targets (D20) — and lift again when
// the rows go away.
func TestCeilingInGrantedRole(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)

	// Baseline: the fixture grant row, a same-scope auto-grant, and an
	// explicitly-granted cross-scope resource all hold.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "res:apps/calendar/bus", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if role, ok := b.grantedRole("apps/email", "apps/calendar"); !ok || role != "reader" {
		t.Fatalf("baseline explicit grant: %q %v", role, ok)
	}
	if _, ok := b.grantedRole("apps/calendar", "res:apps/calendar/events"); !ok {
		t.Fatal("baseline same-scope auto-grant should hold")
	}
	if _, ok := b.grantedRole("apps/email", "res:apps/calendar/bus"); !ok {
		t.Fatal("baseline cross-scope resource grant should hold")
	}

	// A mayCall allow-list that covers nothing blocks CROSS-scope reach —
	// but never a tile's own scope (its resources, intra-app calls): mayCall
	// governs external reach only, so an org row can't sever an app's own db.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "apps/calendar"); ok {
		t.Fatal("explicit cross-scope grant must be capped by mayCall")
	}
	if _, ok := b.grantedRole("apps/email", "res:apps/calendar/bus"); ok {
		t.Fatal("cross-scope resource grant must be capped by mayCall")
	}
	if _, ok := b.grantedRole("apps/calendar", "res:apps/calendar/events"); !ok {
		t.Fatal("a tile's own scope is exempt from mayCall")
	}

	// Covering the targets lifts the cap.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", MayCall: []string{"apps/*", "res:apps/*"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "apps/calendar"); !ok {
		t.Fatal("covered target must pass")
	}

	// xbin capability targets fall under xbin-caps, and a denied element also
	// loses broker-adminship (IsAdmin goes through grantedRole).
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.IsAdmin(auth.Principal{Component: "apps/email"}) {
		t.Fatal("xbin admin grant should confer broker admin")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "xbin"); ok {
		t.Fatal("xbin-caps deny must kill capability roles")
	}
	if b.IsAdmin(auth.Principal{Component: "apps/email"}) {
		t.Fatal("xbin-caps deny must neuter element adminship")
	}
	// …but ordinary element→element calls are unaffected by that kind.
	if _, ok := b.grantedRole("apps/email", "apps/calendar"); !ok {
		t.Fatal("xbin-caps deny must not affect component targets")
	}

	// gpu:* targets fall under the gpu kind (GPUFor goes through grantedRole).
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "gpu:0", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "gpu:0"); !ok {
		t.Fatal("gpu grant should hold without a deny")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", Deny: []string{users.PolicyDenyGPU}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "gpu:0"); ok {
		t.Fatal("gpu deny must strip gpu grants")
	}

	// Humans are never subject to the ceiling (it caps elements only).
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}, MayCall: []string{"nothing"}}}); err != nil {
		t.Fatal(err)
	}
	if role, ok := b.Policy(auth.Principal{Owner: true}, mustComponent(t, b, "apps/calendar")); !ok || role != "admin" {
		t.Fatal("the owner must bypass the ceiling")
	}
}

func mustComponent(t *testing.T, b *Broker, path string) *registry.Component {
	t.Helper()
	c, ok := b.Reg.Component(path)
	if !ok {
		t.Fatalf("no component %s", path)
	}
	return c
}

// A net deny row makes the binding unapprovable AND inert if already present.
func TestNetBindingCeiling(t *testing.T) {
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1,"bindings":{"apps/o/sales/bot":{"net":"internet"}}}`)
	write("apps/o/sales/bot/xbin.json", `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	st := testUsers(t, b)
	if _, err := st.Upsert(users.User{ID: "root2", Role: users.RoleAdmin}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	// Org rows key off OWNERSHIP now (D24/D25) — the path carries no org.
	if err := st.SetOwner("apps/o/sales/bot", "org:sales"); err != nil {
		t.Fatal(err)
	}

	if nb := b.netBinding("apps/o/sales/bot"); nb != "internet" {
		t.Fatalf("baseline binding = %q, want internet", nb)
	}
	if err := b.validateBinding("apps/o/sales/bot", "net", registry.BindTo("internet")); err != nil {
		t.Fatalf("baseline validateBinding: %v", err)
	}

	// Org policy denies net → existing binding inert, new one unapprovable.
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/o/sales/bot"); nb != "" {
		t.Fatalf("denied binding must resolve to none, got %q", nb)
	}
	err = b.validateBinding("apps/o/sales/bot", "net", registry.BindTo("internet"))
	if err == nil || !strings.Contains(err.Error(), "denies net") {
		t.Fatalf("validateBinding should refuse with the row named, got %v", err)
	}
}

// resolveCreateOwner (D24/D25): humans default to user-owned; org owner
// needs the Create knob (or org/ws admin); user:<other> is ws-admin only.
func TestResolveCreateOwner(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleUser},
		{ID: "bob", Role: users.RoleUser},
		{ID: "carol", Role: users.RoleUser},
		{ID: "boss", Role: users.RoleAdmin},
	} {
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "carol", Level: users.LevelTerminal, Admin: true},
		{ID: "bob", Level: users.LevelWrite, Create: true},
		{ID: "alice", Level: users.LevelRead},
	}}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name      string
		p         auth.Principal
		requested string
		wantRef   string
		refuse    string // substring of the refusal, "" = allowed
	}{
		{"human default", auth.Principal{UserID: "bob"}, "", "user:bob", ""},
		{"admin default", auth.Principal{Owner: true}, "", "", ""},
		{"org by create-knob", auth.Principal{UserID: "bob"}, "org:sales", "org:sales", ""},
		{"org by org-admin", auth.Principal{UserID: "carol"}, "org:sales", "org:sales", ""},
		{"org refused for viewer", auth.Principal{UserID: "alice"}, "org:sales", "", "Create permission"},
		{"unknown org", auth.Principal{UserID: "bob"}, "org:nope", "", "no such org"},
		{"self user ref", auth.Principal{UserID: "bob"}, "user:bob", "user:bob", ""},
		{"other user ref", auth.Principal{UserID: "bob"}, "user:alice", "", "workspace-admin"},
		{"bad ref", auth.Principal{UserID: "bob"}, "gang:x", "", "owner must be"},
	}
	run := func(cases []struct {
		name      string
		p         auth.Principal
		requested string
		wantRef   string
		refuse    string
	}) {
		t.Helper()
		for _, c := range cases {
			ref, msg := b.resolveCreateOwner(c.p, c.requested)
			if c.refuse == "" && (msg != "" || ref != c.wantRef) {
				t.Errorf("%s: got ref=%q msg=%q, want ref=%q", c.name, ref, msg, c.wantRef)
			}
			if c.refuse != "" && !strings.Contains(msg, c.refuse) {
				t.Errorf("%s: msg %q, want containing %q", c.name, msg, c.refuse)
			}
		}
	}
	run(cases)

	// org-only policy (D52): non-admins can't own tiles personally. "" resolves
	// to the one org where they hold Create; several → must choose; none →
	// refused. Admins and unattributed automation are unaffected.
	if err := st.SetTileCreation(users.TileCreationOrgOnly); err != nil {
		t.Fatal(err)
	}
	run([]struct {
		name      string
		p         auth.Principal
		requested string
		wantRef   string
		refuse    string
	}{
		{"org-only: default → the one create org", auth.Principal{UserID: "bob"}, "", "org:sales", ""},
		{"org-only: org admin default", auth.Principal{UserID: "carol"}, "", "org:sales", ""},
		{"org-only: explicit org still fine", auth.Principal{UserID: "bob"}, "org:sales", "org:sales", ""},
		{"org-only: personal refused", auth.Principal{UserID: "bob"}, "user:bob", "", "must be owned by an organisation"},
		{"org-only: no create org", auth.Principal{UserID: "alice"}, "", "", "hold Create in no organisation"},
		{"org-only: admin still workspace-owned", auth.Principal{Owner: true}, "", "", ""},
		{"org-only: admin personal ref ok", auth.Principal{Owner: true}, "user:bob", "user:bob", ""},
	})
	// Two create orgs → ambiguous, must name one.
	if _, err := st.UpsertOrg(users.Org{ID: "ops", Members: []users.Member{{ID: "bob", Level: users.LevelWrite, Create: true}}}); err != nil {
		t.Fatal(err)
	}
	if ref, msg := b.resolveCreateOwner(auth.Principal{UserID: "bob"}, ""); ref != "" || !strings.Contains(msg, "choose one") || !strings.Contains(msg, "ops") {
		t.Fatalf("ambiguous org-only default: ref=%q msg=%q", ref, msg)
	}
	// Back to "any": bob's default is personal again.
	if err := st.SetTileCreation(users.TileCreationAny); err != nil {
		t.Fatal(err)
	}
	if ref, msg := b.resolveCreateOwner(auth.Principal{UserID: "bob"}, ""); ref != "user:bob" || msg != "" {
		t.Fatalf("policy reset: ref=%q msg=%q", ref, msg)
	}

	// The per-account switch (D88): org-only for one user, the same shape —
	// driven directly or through an element (the manager tile).
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NoPersonalTiles: boolp(true)}); err != nil {
		t.Fatal(err)
	}
	run([]struct {
		name      string
		p         auth.Principal
		requested string
		wantRef   string
		refuse    string
	}{
		{"noPersonalTiles: personal refused", auth.Principal{UserID: "bob"}, "user:bob", "", "turned off for your account"},
		{"noPersonalTiles: via an element too", auth.Principal{Component: "apps/email", UserID: "bob"}, "user:bob", "", "turned off for your account"},
		{"noPersonalTiles: default → must choose an org", auth.Principal{UserID: "bob"}, "", "", "choose one"},
		{"noPersonalTiles: explicit org fine", auth.Principal{UserID: "bob"}, "org:ops", "org:ops", ""},
		{"others unaffected", auth.Principal{UserID: "alice"}, "", "user:alice", ""},
	})
}

func boolp(b bool) *bool { return &b }

// The grants API must refuse approving a grant the ceiling nullifies.
func TestGrantApprovalCeilingReject(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/xbin/grants",
		strings.NewReader(`{"from":"apps/email","target":"apps/llm","role":"reader"}`))
	r = r.WithContext(auth.WithPrincipal(context.Background(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiGrantsAdd(w, r)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "allow-lists call targets") {
		t.Fatalf("expected policy refusal, got %d %s", w.Code, w.Body.String())
	}
	// Revoking an over-ceiling row stays possible (cleanup path).
	r = httptest.NewRequest("DELETE", "/api/xbin/grants",
		strings.NewReader(`{"from":"apps/email","target":"apps/calendar","role":"reader"}`))
	r = r.WithContext(auth.WithPrincipal(context.Background(), auth.Principal{Owner: true}))
	w = httptest.NewRecorder()
	b.apiGrantsRevoke(w, r)
	if w.Code != 200 {
		t.Fatalf("revoke under ceiling should pass, got %d %s", w.Code, w.Body.String())
	}
}

// ValidateNewTile: the owner ref must exist; paths carry no reserved org
// segments anymore (D24 abolished positional naming).
func TestValidateNewTileOwner(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	if _, err := st.UpsertOrg(users.Org{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if err := st.ValidateNewTile("org:sales"); err != nil {
		t.Fatalf("existing org refused: %v", err)
	}
	if err := st.ValidateNewTile("org:nope"); err == nil {
		t.Fatal("unknown org must be refused")
	}
	if err := st.ValidateNewTile(""); err != nil {
		t.Fatal("workspace-owned is always valid")
	}
	_ = b
}

// canCreateAt: the confused-deputy clamp — an element's workspace-management
// grant never lets the attributed human create where they couldn't
// themselves (D82: the ownership path rule applies to them); unattributed
// automation keeps the old capability semantics. Owners are what
// resolveCreateOwner hands a non-admin (user:<self> / an org).
func TestCanCreateAtDeputyClamp(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	for _, u := range []users.User{
		{ID: "admin2", Role: users.RoleAdmin},
		{ID: "plain", Role: users.RoleUser},
	} {
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	// tiles/manager-style element with the capability grant.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "xbin", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	acc := func(id string) *users.Access { a, _ := st.Access(id); return a }
	cases := []struct {
		name  string
		p     auth.Principal
		path  string
		owner string
		want  bool
	}{
		{"owner", auth.Principal{Owner: true}, "tiles/x", "", true},
		{"session plain, personal", auth.Principal{UserID: "plain", Access: acc("plain")}, "apps/x", "user:plain", true},
		{"session plain, reserved", auth.Principal{UserID: "plain", Access: acc("plain")}, "tiles/x", "user:plain", false},
		{"session plain, no owner", auth.Principal{UserID: "plain", Access: acc("plain")}, "apps/x", "", false},
		{"unattributed element w/ grant", auth.Principal{Component: "apps/email"}, "tiles/anything", "", true},
		{"element w/ grant, plain human", auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/anything", "user:plain", true},
		{"element w/ grant, plain human, reserved", auth.Principal{Component: "apps/email", UserID: "plain"}, "tiles/anything", "user:plain", false},
		{"element w/ grant, plain human, foreign scope", auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/calendar/x", "user:plain", false},
		{"element w/ grant, admin human", auth.Principal{Component: "apps/email", UserID: "admin2"}, "tiles/anything", "", true},
		{"element w/o grant", auth.Principal{Component: "apps/calendar", UserID: "plain"}, "apps/x", "user:plain", false},
		// Creating AS an org: the org Create knob (checked upstream in
		// resolveCreateOwner) authorises the owner; the path rule still holds.
		{"session plain, org-owned", auth.Principal{UserID: "plain", Access: acc("plain")}, "apps/x", "org:sales", true},
		{"session plain, org-owned, reserved", auth.Principal{UserID: "plain", Access: acc("plain")}, "tiles/x", "org:sales", false},
		{"element w/ grant, plain human, org-owned", auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/x", "org:sales", true},
		{"element w/o grant, org-owned", auth.Principal{Component: "apps/calendar", UserID: "plain"}, "apps/x", "org:sales", false},
	}
	for _, c := range cases {
		if got, msg := b.canCreateAt(c.p, c.path, c.owner); got != c.want {
			t.Errorf("%s: canCreateAt=%v (%s), want %v", c.name, got, msg, c.want)
		}
	}

	// The read clamp: an attributed human copying a source needs read on it.
	if b.attributedCanRead(auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/calendar") {
		t.Fatal("attributed human without read must be clamped")
	}
	if !b.attributedCanRead(auth.Principal{Component: "apps/email"}, "apps/calendar") {
		t.Fatal("unattributed automation keeps capability semantics")
	}
	if err := st.GrantTile("plain", "apps/calendar", users.LevelRead); err != nil {
		t.Fatal(err)
	}
	if !b.attributedCanRead(auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/calendar") {
		t.Fatal("read grant should satisfy the source clamp")
	}
}

// newTilePathOK (D82): a non-admin creates anywhere free that isn't
// reserved, isn't inside someone else's scope, and carries no leftover
// state from a removed tile.
func TestNewTilePathRule(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	for _, u := range []users.User{
		{ID: "hubert", Role: users.RoleUser},
		{ID: "carol", Role: users.RoleUser},
		{ID: "boss", Role: users.RoleAdmin},
	} {
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "hubert", Level: users.LevelTerminal, Create: true},
	}}); err != nil {
		t.Fatal(err)
	}
	write := func(rel, content string) {
		p := filepath.Join(b.Reg.Root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// Plain-directory scope roots: mine (every tile), carol's (every tile),
	// an owner entry that outranks its tiles, an empty one, an org's.
	for _, sc := range []string{"mine", "hers", "entry", "empty", "orgs"} {
		write(sc+"/scope.json", `{}`)
	}
	for _, tile := range []string{"mine/ui", "hers/ui", "entry/ui", "orgs/ui"} {
		write(tile+"/xbin.json", `{}`)
	}
	must(b.Reg.Rescan())
	must(st.SetOwner("mine/ui", "user:hubert"))
	must(st.SetOwner("hers/ui", "user:carol"))
	must(st.SetOwner("entry/ui", "user:carol"))
	must(st.SetOwner("entry", "user:hubert"))
	must(st.SetOwner("orgs/ui", "org:sales"))

	// Leftovers from removed tiles.
	must(b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants,
			registry.Grant{From: "apps/ghost", Target: "xbin", Role: "admin"},
			registry.Grant{From: "apps/email", Target: "code:apps/ghost2", Role: "reader"},
			registry.Grant{From: "apps/mine-again", Target: "xbin", Role: "writer"})
		ws.Bindings = map[string]map[string]registry.Binding{
			"apps/email": {"store": {{Ref: "apps/ghost3#main"}}},
		}
	}))
	write("data/vault/"+util.CompKey("apps/ghost4")+".json", `{}`)
	must(st.SetUserTile("carol", "apps/ghost5", users.LevelRead))
	must(st.SetUserTile("carol", "apps/excluded", users.LevelNone))
	must(st.SetOrgTile("sales", "apps/ghost6", users.LevelWrite))
	must(st.SetOwner("apps/ghost7", "user:carol"))
	must(st.SetOwner("apps/mine-again", "user:hubert"))
	must(st.SetDefaultTiles(map[string]string{"apps/ghost8": users.LevelRead, "apps/*": users.LevelRead}))

	hubert := func() auth.Principal { a, _ := st.Access("hubert"); return auth.Principal{UserID: "hubert", Access: a} }
	boss := func() auth.Principal {
		a, _ := st.Access("boss")
		u, _ := st.Get("boss")
		return auth.Principal{UserID: "boss", Access: a, User: u}
	}
	cases := []struct {
		path, owner string
		p           auth.Principal
		want        bool
		msg         string // substring of the refusal
	}{
		{"apps/x", "user:hubert", hubert(), true, ""},
		{"brand-new/deep/x", "user:hubert", hubert(), true, ""},
		{"tiles/x", "user:hubert", hubert(), false, "reserved for built-in tiles"},
		{"root", "user:hubert", hubert(), false, "chrome"},
		{"shell", "user:hubert", hubert(), false, "chrome"},
		{"a:b/x", "user:hubert", hubert(), false, "':'"},
		{"apps/user:bob", "user:hubert", hubert(), false, "':'"},
		{"mine/api", "user:hubert", hubert(), true, ""},
		{"hers/api", "user:hubert", hubert(), false, "inside scope hers"},
		{"entry/api", "user:hubert", hubert(), true, ""},
		{"empty/api", "user:hubert", hubert(), false, "inside scope empty"},
		{"orgs/api", "org:sales", hubert(), true, ""},
		{"orgs/api", "user:hubert", hubert(), false, "inside scope orgs"},
		{"apps/calendar/x", "user:hubert", hubert(), false, "inside scope apps/calendar"},
		{"apps/ghost", "user:hubert", hubert(), false, "grant apps/ghost → xbin:admin"},
		{"apps/ghost2", "user:hubert", hubert(), false, "code:apps/ghost2"},
		{"apps/ghost3", "user:hubert", hubert(), false, "binding apps/email store → apps/ghost3#main"},
		{"apps/ghost4", "user:hubert", hubert(), false, "vault secrets"},
		{"apps/ghost5", "user:hubert", hubert(), false, "user carol has read"},
		{"apps/excluded", "user:hubert", hubert(), true, ""},
		{"apps/ghost6", "user:hubert", hubert(), false, "shared to org sales"},
		{"apps/ghost6", "org:sales", hubert(), true, ""}, // the owning org's own share
		{"apps/ghost7", "user:hubert", hubert(), false, "owner entry user:carol"},
		{"apps/mine-again", "user:hubert", hubert(), true, ""}, // re-creating your own
		{"apps/ghost8", "user:hubert", hubert(), false, "visible to every user"},
		{"tiles/x", "", boss(), true, ""},
		{"apps/ghost", "", boss(), true, ""},
		{"hers/api", "", boss(), true, ""},
	}
	for _, c := range cases {
		got, msg := b.canCreateAt(c.p, c.path, c.owner)
		if got != c.want || !strings.Contains(msg, c.msg) {
			t.Errorf("%s as %s: canCreateAt=%v %q, want %v containing %q", c.path, c.owner, got, msg, c.want, c.msg)
		}
	}
}

// guardNewComponentTree: no creating inside a component, over a component, or
// above existing components (the org-container scenario).
func TestGuardNewComponentTree(t *testing.T) {
	b := testBroker(t)
	if err := b.guardNewComponentTree("apps/free"); err != nil {
		t.Fatalf("free path refused: %v", err)
	}
	if err := b.guardNewComponentTree("apps/calendar"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("existing path: %v", err)
	}
	if err := b.guardNewComponentTree("apps/calendar/sub"); err == nil || !strings.Contains(err.Error(), "inside existing") {
		t.Fatalf("inside a component: %v", err)
	}
	if err := b.guardNewComponentTree("apps"); err == nil || !strings.Contains(err.Error(), "would contain") {
		t.Fatalf("above components: %v", err)
	}
}

// Pending annotates ceiling-blocked requests so UIs can grey them out
// instead of offering an approve that would 400.
func TestPendingBlockedAnnotation(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	found := func() (PendingGrant, bool) {
		for _, p := range b.Pending() {
			if p.From == "apps/email" && p.Target == "res:apps/calendar/bus" {
				return p, true
			}
		}
		return PendingGrant{}, false
	}
	p, ok := found()
	if !ok || p.Blocked != "" {
		t.Fatalf("baseline pending should be approvable: %+v %v", p, ok)
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/email", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if p, ok = found(); !ok || p.Blocked == "" {
		t.Fatalf("ceiling-blocked pending must carry the reason: %+v %v", p, ok)
	}
}

// code / code:<comp> are reserved capability targets and must never fall
// into the mayCall path-matcher (the 2026-07-12 code:reader regression: any
// mayCall row silently killed source-read grants). Bare `code` is the
// owner-level whole-workspace read (xbin-caps class); code:<comp> is
// governed like calling that component.
func TestCeilingCodeTargets(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants,
			registry.Grant{From: "apps/email", Target: "code", Role: "reader"},
			registry.Grant{From: "apps/email", Target: "code:apps/calendar", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	tree := func(comp string) int {
		r := httptest.NewRequest("GET", "/code/tree?component="+comp, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Component: "apps/email"}))
		w := httptest.NewRecorder()
		b.apiCodeTree(w, r)
		return w.Code
	}

	// A path allow-list must not strip the code capability…
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "code"); !ok {
		t.Fatal("bare code grant must survive mayCall rows")
	}
	if got := tree("apps/calendar"); got != 200 {
		t.Fatalf("code read under mayCall row: want 200, got %d", got)
	}
	// …but code:<comp> follows the allow-list of the component it reads.
	if _, ok := b.grantedRole("apps/email", "code:apps/calendar"); ok {
		t.Fatal("code:<comp> must be governed like calling the component")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"apps/calendar"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "code:apps/calendar"); !ok {
		t.Fatal("covered component must allow its code:<comp> read")
	}

	// An explicit capability strip DOES kill the blanket read.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "code"); ok {
		t.Fatal("xbin-caps deny must strip the blanket code capability")
	}
	if got := tree("apps/email"); got != 200 {
		t.Fatalf("self-read is never policy-gated: want 200, got %d", got)
	}
}

// cap:net-admin is the admin-granted net-provider capability: NetAdminFor
// resolves it, it's a reserved target (admin-only, never same-scope
// auto-granted), and the policy `net` deny class strips it — never the
// mayCall path-matcher (the code-target regression class).
func TestNetAdminCapGrant(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	router, _ := b.Reg.Component("apps/email") // stand-in provider tile

	if b.NetAdminFor(router) {
		t.Fatal("ungranted tile must not hold net-admin caps")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: NetAdminCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.NetAdminFor(router) {
		t.Fatal("granted tile must hold net-admin caps")
	}

	// A path allow-list (mayCall) must NOT strip a capability grant.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if !b.NetAdminFor(router) {
		t.Fatal("mayCall row must not strip the net-admin capability")
	}
	// …but a `net` deny row does (a tile denied network can't be a provider).
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if b.NetAdminFor(router) {
		t.Fatal("a net deny must strip the net-admin capability")
	}

	// Reserved target: never same-scope auto-granted (a tile can't self-grant
	// it by merely declaring the use).
	b2 := testBroker(t)
	if err := b2.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {}); err != nil {
		t.Fatal(err)
	}
	cal, _ := b2.Reg.Component("apps/calendar")
	if b2.NetAdminFor(cal) {
		t.Fatal("undeclared/ungranted tile must not hold the capability")
	}
}

// cap:containers is the admin-granted container-host capability: ContainersFor
// resolves it, a mayCall row must not strip it, but an xbin-caps deny does.
func TestContainersCapGrant(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	dev, _ := b.Reg.Component("apps/email") // stand-in container-host tile

	if b.ContainersFor(dev) {
		t.Fatal("ungranted tile must not hold container caps")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: ContainersCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.ContainersFor(dev) {
		t.Fatal("granted tile must hold container caps")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if !b.ContainersFor(dev) {
		t.Fatal("a mayCall allow-list must not strip the container capability")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if b.ContainersFor(dev) {
		t.Fatal("an xbin-caps deny must strip the container capability")
	}
}

// cap:open-links (ND11) is the frontend capability: a grant row unlocks
// exactly the two popup sandbox tokens for that tile; the ceiling's xbin-caps
// deny strips it; a mayCall allow-list must not (the 2026-07-12 regression
// class); a manifest that merely DECLARES it lands pending and unlocks nothing.
func TestOpenLinksCapGrant(t *testing.T) {
	b := testBroker(t)
	st := b.Users

	if b.OpenLinksFor("apps/email") || b.SandboxTokensFor("apps/email") != nil {
		t.Fatal("ungranted tile must not unlock popups")
	}
	// Declare-only: pending, still nothing unlocked.
	mf := filepath.Join(b.Reg.Root, "apps", "calendar", "xbin.json")
	if err := os.WriteFile(mf, []byte(`{"uses":[{"target":"cap:open-links","role":"writer"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	pending := false
	for _, p := range b.Pending() {
		if p.From == "apps/calendar" && p.Target == OpenLinksCap {
			pending = true
		}
	}
	if !pending {
		t.Fatal("a declared cap:open-links must land pending (never same-scope auto-granted)")
	}
	if b.OpenLinksFor("apps/calendar") {
		t.Fatal("declaring the cap must not unlock it")
	}

	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: OpenLinksCap, Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if !b.OpenLinksFor("apps/email") {
		t.Fatal("granted tile must hold cap:open-links")
	}
	if got := b.SandboxTokensFor("apps/email"); len(got) != 2 || got[0] != "allow-popups" || got[1] != "allow-popups-to-escape-sandbox" {
		t.Fatalf("sandbox tokens: %v", got)
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if !b.OpenLinksFor("apps/email") {
		t.Fatal("a mayCall allow-list must not strip cap:open-links")
	}
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	if b.OpenLinksFor("apps/email") || b.SandboxTokensFor("apps/email") != nil {
		t.Fatal("an xbin-caps deny must strip cap:open-links")
	}
	if msg := b.ceilingBlockMsg("apps/email", OpenLinksCap); !strings.Contains(msg, users.PolicyDenyXbinCaps) {
		t.Fatalf("the block message must name the deny class: %q", msg)
	}
}
