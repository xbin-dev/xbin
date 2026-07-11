package broker

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/users"
)

// testUsers wires a fresh user store into a broker.
func testUsers(t *testing.T, b *Broker) *users.Store {
	t.Helper()
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b.Users = st
	return st
}

// The ceiling must cap every grant source in grantedRole: explicit rows,
// same-scope auto-grants, and capability targets (D20) — and lift again when
// the rows go away.
func TestCeilingInGrantedRole(t *testing.T) {
	b := testBroker(t)
	st := testUsers(t, b)

	// Baseline: the fixture grant row and a same-scope auto-grant hold.
	if role, ok := b.grantedRole("apps/email", "apps/calendar"); !ok || role != "reader" {
		t.Fatalf("baseline explicit grant: %q %v", role, ok)
	}
	if _, ok := b.grantedRole("apps/calendar", "res:apps/calendar/events"); !ok {
		t.Fatal("baseline same-scope auto-grant should hold")
	}

	// A mayCall allow-list that covers neither target blocks both sources.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/*", MayCall: []string{"nothing/*"}}}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.grantedRole("apps/email", "apps/calendar"); ok {
		t.Fatal("explicit grant must be capped by mayCall")
	}
	if _, ok := b.grantedRole("apps/calendar", "res:apps/calendar/events"); ok {
		t.Fatal("same-scope auto-grant must be capped by mayCall")
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
	if err := b.validateBinding("apps/o/sales/bot", "net", []string{"internet"}); err != nil {
		t.Fatalf("baseline validateBinding: %v", err)
	}

	// Org policy denies net → existing binding inert, new one unapprovable.
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/o/sales/bot"); nb != "" {
		t.Fatalf("denied binding must resolve to none, got %q", nb)
	}
	err = b.validateBinding("apps/o/sales/bot", "net", []string{"internet"})
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
