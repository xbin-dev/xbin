package users

import (
	"strings"
	"testing"
)

// newStore builds a store with two users (alice, bob) and one org (sales:
// alice admin, bob developer-ish per test).
func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "bob", "carol"} {
		if _, err := s.Upsert(User{ID: id, Role: RoleUser}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

func mustOrg(t *testing.T, s *Store, o Org) {
	t.Helper()
	if _, err := s.UpsertOrg(o); err != nil {
		t.Fatal(err)
	}
}

func acc(t *testing.T, s *Store, id string) *Access {
	t.Helper()
	a, ok := s.Access(id)
	if !ok {
		t.Fatalf("no access for %q", id)
	}
	return a
}

// --- resolver precedence (D24/D25/D27) --------------------------------------

func TestTileLevelPrecedence(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "sales", Members: []Member{
		{ID: "alice", Level: LevelTerminal, Create: true, Admin: true},
		{ID: "bob", Level: LevelWrite},
	}})
	if err := s.SetOwner("apps/crm", "org:sales"); err != nil { // org-owned
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/mine", "user:bob"); err != nil { // user-owned
		t.Fatal(err)
	}
	if err := s.SetOrgTile("sales", "apps/shared", LevelRead); err != nil { // shared TO org
		t.Fatal(err)
	}
	if err := s.SetDefaultTiles(map[string]string{"apps/welcome": LevelRead}); err != nil {
		t.Fatal(err)
	}

	bob := acc(t, s, "bob")
	cases := []struct{ path, want string }{
		{"apps/mine", LevelTerminal}, // your own tile is yours (D24)
		{"apps/crm", LevelWrite},     // org member level on org-owned
		{"apps/shared", LevelRead},   // shared-to-org entry
		{"apps/welcome", LevelRead},  // workspace default (D27)
		{"apps/other", ""},           // nothing reaches it
	}
	for _, c := range cases {
		if got := bob.TileLevel(c.path); got != c.want {
			t.Errorf("bob level(%s) = %q, want %q", c.path, got, c.want)
		}
	}
	// Org ADMIN gets terminal on org-owned tiles regardless of level knob.
	alice := acc(t, s, "alice")
	if got := alice.TileLevel("apps/crm"); got != LevelTerminal {
		t.Errorf("org admin level = %q, want terminal", got)
	}
	// carol: no membership — org-owned tile unreachable even with the share
	// belonging to sales.
	carol := acc(t, s, "carol")
	if carol.CanReadTile("apps/crm") || carol.CanReadTile("apps/shared") {
		t.Error("non-member must not reach org tiles or org shares")
	}
	if !carol.CanReadTile("apps/welcome") {
		t.Error("defaults apply to every user")
	}
	// Explain agrees with TileLevel on the top contribution.
	for _, path := range []string{"apps/mine", "apps/crm", "apps/shared", "apps/welcome"} {
		ex := bob.Explain(path)
		if lvl := bob.TileLevel(path); lvl != "" && (len(ex) == 0 || ex[0].Level != lvl) {
			t.Errorf("Explain(%s) top %v != TileLevel %q", path, ex, lvl)
		}
	}
}

func TestCreateAs(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "sales", Members: []Member{
		{ID: "alice", Level: LevelTerminal, Admin: true},
		{ID: "bob", Level: LevelTerminal, Create: true},
		{ID: "carol", Level: LevelRead},
	}})
	if !acc(t, s, "alice").CanCreateAs("sales") { // admin implies create
		t.Error("org admin must create")
	}
	if !acc(t, s, "bob").CanCreateAs("sales") {
		t.Error("Create knob must allow")
	}
	if acc(t, s, "carol").CanCreateAs("sales") {
		t.Error("viewer must not create")
	}
	if acc(t, s, "bob").CanCreateAs("other") {
		t.Error("non-member must not create")
	}
	// Personal creation still runs on user patterns only.
	if acc(t, s, "bob").CanCreateTile("apps/x") {
		t.Error("no personal pattern → no personal create")
	}
}

// --- ownership lifecycle ------------------------------------------------------

func TestOwnershipLifecycle(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "sales", Members: []Member{{ID: "alice", Level: LevelRead, Admin: true}}})
	if err := s.SetOwner("apps/a", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/b", "org:sales"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/x", "user:ghost"); err == nil {
		t.Fatal("unknown user must be rejected")
	}
	if err := s.SetOwner("apps/x", "org:ghost"); err == nil {
		t.Fatal("unknown org must be rejected")
	}
	// Transfer: storage-level move; authz is the broker's.
	if err := s.SetOwner("apps/a", "org:sales"); err != nil {
		t.Fatal(err)
	}
	if got := s.Owner("apps/a"); got != "org:sales" {
		t.Fatalf("owner after transfer = %q", got)
	}
	// Org delete refused while owning tiles.
	if err := s.DeleteOrg("sales"); err == nil || !strings.Contains(err.Error(), "owns") {
		t.Fatalf("org delete must refuse while owning tiles, got %v", err)
	}
	if err := s.SetOwner("apps/a", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/b", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOrg("sales"); err != nil {
		t.Fatalf("org delete after transfer: %v", err)
	}
	// Deleting a user orphans their tiles to workspace-owned.
	if err := s.SetOwner("apps/c", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("bob"); err != nil {
		t.Fatal(err)
	}
	if got := s.Owner("apps/c"); got != "" {
		t.Fatalf("deleted user's tile must fall to workspace-owned, got %q", got)
	}
}

// --- allowances (D26) ---------------------------------------------------------

func TestAllowanceGrammarAndFloor(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "dev", Members: []Member{{ID: "alice", Level: LevelTerminal, Admin: true}}})

	// Write-time: grammar enforced, xbin family refused.
	bad := [][]string{
		{"xbin"}, {"xbin:users"}, {"xbin:*"},
		{"whatever"}, {""}, {"res:"},
	}
	for _, entries := range bad {
		if err := s.SetOrgAllow("dev", entries); err == nil {
			t.Errorf("allow %v must be rejected", entries)
		}
	}
	good := []string{
		"res:*", "gpu:0", "cap:containers", "net:internet", "net:host",
		"net:lan:10.0.0.0/8", "net:lan:192.168.*", "iface:llm", "ingress:host:*.dev.example.com",
		"ingress:zone:*.z.example.com", "ingress:listen:20000-20999", "tile:apps/*",
	}
	if err := s.SetOrgAllow("dev", good); err != nil {
		t.Fatalf("valid allow rejected: %v", err)
	}

	cover := []struct {
		target string
		want   bool
	}{
		{"res:apps/x/db", true},
		{"gpu:0", true},
		{"gpu:1", false},
		{"cap:containers", true},
		{"cap:net-admin", false},
		{"net:internet", true},
		{"net:host", true},
		{"net:lan:10.0.0.0/8", true},     // exact binding value
		{"net:lan:10.1.2.0/24", false},   // no CIDR-contains semantics — use a glob
		{"net:lan:192.168.1.0/24", true}, // glob entry
		{"iface:llm", true},
		{"iface:mcp", false},
		{"ingress:host:api.dev.example.com", true},
		{"ingress:host:api.prod.example.com", false},
		{"ingress:listen:20500", true},
		{"ingress:listen:21000", false},
		{"apps/other", true}, // tile:apps/*
		{"tiles/x", false},
		// The floor: never coverable, no matter the entries.
		{"xbin", false},
		{"xbin:users", false},
	}
	for _, c := range cover {
		if got := s.AllowanceCovers("dev", c.target); got != c.want {
			t.Errorf("AllowanceCovers(%q) = %v, want %v", c.target, got, c.want)
		}
	}
}

// --- permission sets (D28) ----------------------------------------------------

func TestPermissionSets(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "dev", Members: []Member{{ID: "bob", Level: LevelWrite}}})

	if err := s.UpsertPermissionSet("hightrust", PermissionSet{
		Allow:   []string{"cap:containers", "net:internet"},
		Policy:  []PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyGPU}}},
		TermNet: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPermissionSet("bad", PermissionSet{Allow: []string{"xbin"}}); err == nil {
		t.Fatal("set with xbin allow must be rejected")
	}
	if err := s.SetOrgSets("dev", []string{"hightrust"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgSets("dev", []string{"missing"}); err == nil {
		t.Fatal("attaching an unknown set must fail")
	}
	if err := s.SetOrgAllow("dev", []string{"gpu:*"}); err != nil {
		t.Fatal(err)
	}

	// ResolvedAllow = set ∪ extras.
	ra := s.ResolvedAllow("dev")
	for _, want := range []string{"cap:containers", "net:internet", "gpu:*"} {
		found := false
		for _, e := range ra {
			if e == want {
				found = true
			}
		}
		if !found {
			t.Errorf("ResolvedAllow missing %q: %v", want, ra)
		}
	}
	if !s.AllowanceCovers("dev", "cap:containers") || !s.AllowanceCovers("dev", "gpu:3") {
		t.Error("union must cover set + extra entries")
	}

	// Set-conferred term flags reach members.
	if !acc(t, s, "bob").TermNet() {
		t.Error("set TermNet must reach org members")
	}
	if acc(t, s, "bob").TermAPI() {
		t.Error("TermAPI not conferred")
	}
	if acc(t, s, "carol").TermNet() {
		t.Error("non-members unaffected")
	}

	// Set ceiling rows compose restrictively for org-OWNED tiles.
	if err := s.SetOwner("apps/devtile", "org:dev"); err != nil {
		t.Fatal(err)
	}
	if !s.Ceiling("apps/devtile").Denies(PolicyDenyGPU) {
		t.Error("attached set's deny row must apply to org-owned tiles")
	}
	if s.Ceiling("apps/elsewhere").Denies(PolicyDenyGPU) {
		t.Error("set rows must NOT apply outside the org's owned tiles")
	}

	// Delete refused while attached; ok after detach.
	if err := s.DeletePermissionSet("hightrust"); err == nil {
		t.Fatal("delete of attached set must refuse")
	}
	if err := s.SetOrgSets("dev", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePermissionSet("hightrust"); err != nil {
		t.Fatalf("delete after detach: %v", err)
	}
}

// --- ceilings by ownership ----------------------------------------------------

func TestCeilingByOwnership(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "sales", Members: []Member{{ID: "alice", Level: LevelRead}}})
	if err := s.SetPolicy([]PolicyRow{{Tiles: "apps/*", Deny: []string{PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgPolicy("sales", []PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyIngress}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/crm", "org:sales"); err != nil {
		t.Fatal(err)
	}
	c := s.Ceiling("apps/crm")
	if !c.Denies(PolicyDenyNet) || !c.Denies(PolicyDenyIngress) {
		t.Error("workspace + owner-org rows must both apply")
	}
	// A workspace-plane tile only gets workspace rows.
	c2 := s.Ceiling("apps/other")
	if !c2.Denies(PolicyDenyNet) || c2.Denies(PolicyDenyIngress) {
		t.Error("org rows must key off ownership, not paths")
	}
}
