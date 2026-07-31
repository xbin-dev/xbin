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

// Create-in-team: membership/org rules, and the team + creator auto-grants.
func TestResolveCreateTeam(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	for _, id := range []string{"alice", "bob", "carol"} {
		if _, err := st.Upsert(users.User{ID: id, Role: users.RoleUser}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTeam("sales", users.Team{ID: "backend", Members: []string{"bob"}, NewTiles: users.LevelTerminal}); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name string
		p    auth.Principal
		ref  string
		path string
		want string // "" = allowed, else substring of the refusal
	}{
		{"member", auth.Principal{UserID: "bob"}, "sales/backend", "apps/o/sales/crm", ""},
		{"org admin", auth.Principal{UserID: "carol"}, "sales/backend", "apps/o/sales/crm", ""},
		{"ws admin", auth.Principal{Owner: true}, "sales/backend", "apps/o/sales/crm", ""},
		{"non-member", auth.Principal{UserID: "alice"}, "sales/backend", "apps/o/sales/crm", "membership"},
		{"path outside org", auth.Principal{UserID: "bob"}, "sales/backend", "apps/crm", "not inside org"},
		{"bad ref", auth.Principal{UserID: "bob"}, "backend", "apps/o/sales/crm", "<org>/<team>"},
		{"unknown team", auth.Principal{UserID: "bob"}, "sales/frontend", "apps/o/sales/crm", "no such team"},
	}
	for _, c := range cases {
		team, org, msg := b.resolveCreateTeam(c.p, c.ref, c.path)
		if c.want == "" && (msg != "" || team == nil || org != "sales") {
			t.Errorf("%s: unexpected refusal %q", c.name, msg)
		}
		if c.want != "" && !strings.Contains(msg, c.want) {
			t.Errorf("%s: msg %q, want containing %q", c.name, msg, c.want)
		}
	}
	if team, _, _ := b.resolveCreateTeam(auth.Principal{UserID: "bob"}, "sales/backend", "apps/o/sales/crm"); team.NewTiles != users.LevelTerminal {
		t.Fatal("resolved team must carry its NewTiles level")
	}
}

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

// Reserved-segment validation runs on every creating entry point.
func TestValidateNewPathGate(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	if _, err := st.UpsertOrg(users.Org{ID: "sales"}); err != nil {
		t.Fatal(err)
	}
	if err := b.validateNewPath("apps/o/sales/x"); err != nil {
		t.Fatalf("valid org path refused: %v", err)
	}
	if err := b.validateNewPath("apps/o/nope/x"); err == nil {
		t.Fatal("unknown org must be refused")
	}
	if err := b.validateNewPath("apps/u/bob/x"); err == nil {
		t.Fatal("u segment must be reserved")
	}
	// Nil store (single-user mode): no restriction.
	b.Users = nil
	if err := b.validateNewPath("apps/u/bob/x"); err != nil {
		t.Fatal("nil store must not restrict")
	}
}

// canCreateAt: the confused-deputy clamp — an element's workspace-management
// grant never extends the attributed human's own create rights; unattributed
// automation keeps the old capability semantics.
func TestCanCreateAtDeputyClamp(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)
	for _, u := range []users.User{
		{ID: "admin2", Role: users.RoleAdmin},
		{ID: "maker", Role: users.RoleUser, CanCreate: []string{"apps/mk/*"}},
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
		name string
		p    auth.Principal
		path string
		want bool
	}{
		{"owner", auth.Principal{Owner: true}, "apps/x", true},
		{"session maker in-pattern", auth.Principal{UserID: "maker", Access: acc("maker")}, "apps/mk/x", true},
		{"session maker out-of-pattern", auth.Principal{UserID: "maker", Access: acc("maker")}, "apps/x", false},
		{"unattributed element w/ grant", auth.Principal{Component: "apps/email"}, "apps/anything", true},
		{"element w/ grant, plain human", auth.Principal{Component: "apps/email", UserID: "plain"}, "apps/anything", false},
		{"element w/ grant, maker human in-pattern", auth.Principal{Component: "apps/email", UserID: "maker"}, "apps/mk/x", true},
		{"element w/ grant, maker human out-of-pattern", auth.Principal{Component: "apps/email", UserID: "maker"}, "apps/x", false},
		{"element w/ grant, admin human", auth.Principal{Component: "apps/email", UserID: "admin2"}, "apps/anything", true},
		{"element w/o grant", auth.Principal{Component: "apps/calendar", UserID: "maker"}, "apps/mk/x", false},
		{"session plain", auth.Principal{UserID: "plain", Access: acc("plain")}, "apps/x", false},
	}
	for _, c := range cases {
		if got, msg := b.canCreateAt(c.p, c.path); got != c.want {
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
