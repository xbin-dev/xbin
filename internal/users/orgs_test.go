package users

import (
	"strings"
	"testing"
)

func TestOrgOf(t *testing.T) {
	cases := []struct {
		path, org string
		ok        bool
	}{
		{"o/sales/crm", "sales", true},
		{"o/sales", "sales", true},
		{"apps/o/sales/crm", "sales", true},
		{"tiles/o/eng/dash", "eng", true},
		{"apps/o/sales", "sales", true},
		{"apps/chat", "", false},
		{"lib/ui/button", "", false},
		{"apps/foo/o/sales/x", "", false}, // marker too deep — not org-owned
		{"o", "", false},
		{"apps/o", "", false},
		{"o/", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		org, ok := OrgOf(c.path)
		if org != c.org || ok != c.ok {
			t.Errorf("OrgOf(%q) = %q,%v want %q,%v", c.path, org, ok, c.org, c.ok)
		}
	}
}

// fixture builds a store with users alice/bob/carol/dave, org "sales"
// (admins: carol; members: alice, bob) with team "backend" (member bob).
func fixture(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"alice", "bob", "carol", "dave"} {
		if _, err := s.Upsert(User{ID: id, Role: RoleUser}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpsertOrg(Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTeam("sales", Team{
		ID: "backend", Members: []string{"bob"},
		// The lib/* entry escapes the org: team patterns only ever apply to
		// paths INSIDE the team's org (pattern ∩ org clamp), so it is inert.
		Tiles:     map[string]string{"apps/o/sales/*": LevelWrite, "lib/*": LevelTerminal},
		CanCreate: []string{"apps/o/sales/*"},
		TermNet:   true,
	}); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestValidateNewTilePath(t *testing.T) {
	s := fixture(t)
	ok := []string{"apps/o/sales/crm", "o/sales/crm", "apps/chat", "lib/ui", "apps/uno", "docs/oak/x"}
	for _, p := range ok {
		if err := s.ValidateNewTilePath(p); err != nil {
			t.Errorf("ValidateNewTilePath(%q) = %v, want nil", p, err)
		}
	}
	bad := map[string]string{
		"apps/o/nope/x":    "no such org",
		"apps/u/alice/x":   "reserved",
		"u/alice":          "reserved",
		"a/b/o/sales/x":    "only valid",
		"apps/o":           "followed by an org id",
		"o":                "followed by an org id",
		"apps/o/sales/o/x": "only valid",
	}
	for p, want := range bad {
		err := s.ValidateNewTilePath(p)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ValidateNewTilePath(%q) = %v, want error containing %q", p, err, want)
		}
	}
}

func TestAccessUnion(t *testing.T) {
	s := fixture(t)

	// bob: team member — team grants apply inside the org only.
	bob, ok := s.Access("bob")
	if !ok {
		t.Fatal("no access for bob")
	}
	if got := bob.TileLevel("apps/o/sales/crm"); got != LevelWrite {
		t.Fatalf("bob on org tile = %q, want write", got)
	}
	if got := bob.TileLevel("lib/ui"); got != "" {
		t.Fatalf("escaping team pattern must be inert outside the org, got %q", got)
	}
	if !bob.CanCreateTile("apps/o/sales/new") || bob.CanCreateTile("apps/new") {
		t.Fatal("team canCreate must be org-clamped")
	}
	if !bob.TermNet() || bob.TermAPI() {
		t.Fatal("term flags must union from teams")
	}
	// The escaping terminal entry still counts for the coarse pre-gate…
	if !bob.CanTerminal() {
		t.Fatal("terminal-level team entry should satisfy the coarse pre-gate")
	}
	// …but confers no terminal on any actual path outside the org.
	if bob.CanTerminalTile("lib/ui") || bob.CanTerminalTile("apps/o/sales/crm") {
		t.Fatal("no terminal on paths: outside org inert, inside org only write is granted")
	}

	// alice: plain member, no team, no base permission yet.
	alice, _ := s.Access("alice")
	if got := alice.TileLevel("apps/o/sales/crm"); got != "" {
		t.Fatalf("member without base permission = %q, want none", got)
	}
	// Base permission floors every member on org tiles.
	if _, err := s.UpsertOrg(Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}, BasePermission: LevelRead}); err != nil {
		t.Fatal(err)
	}
	alice, _ = s.Access("alice")
	if got := alice.TileLevel("apps/o/sales/crm"); got != LevelRead {
		t.Fatalf("base permission = %q, want read", got)
	}
	if alice.TileLevel("apps/chat") != "" {
		t.Fatal("base permission must not leak outside the org")
	}

	// carol: org admin — implicit terminal + create inside the org, nothing outside.
	carol, _ := s.Access("carol")
	if !carol.CanTerminalTile("apps/o/sales/crm") || !carol.CanCreateTile("apps/o/sales/x") || !carol.CanTerminal() {
		t.Fatal("org admin should have implicit terminal+create on org tiles")
	}
	if carol.TileLevel("apps/chat") != "" || carol.CanCreateTile("apps/chat") {
		t.Fatal("org admin power must not leak outside the org")
	}
	if !carol.IsAdminOrg("sales") || carol.IsAdminOrg("eng") || len(carol.AdminOrgs()) != 1 {
		t.Fatal("AdminOrgs bookkeeping wrong")
	}
	if carol.TermAPI() || carol.TermNet() {
		t.Fatal("org admin must not confer term flags")
	}

	// dave: outsider — nothing.
	dave, _ := s.Access("dave")
	if dave.TileLevel("apps/o/sales/crm") != "" || dave.CanTerminal() {
		t.Fatal("non-member must get nothing")
	}

	// direct user entries still union in.
	if err := s.GrantTile("dave", "apps/o/sales/crm", LevelTerminal); err != nil {
		t.Fatal(err)
	}
	dave, _ = s.Access("dave")
	if !dave.CanTerminalTile("apps/o/sales/crm") {
		t.Fatal("direct grant must apply on org tiles too")
	}

	// nil-safety.
	var nilA *Access
	if nilA.TileLevel("x") != "" || nilA.CanTerminal() || nilA.CanCreateTile("x") || nilA.TermAPI() || nilA.TermNet() || nilA.IsAdminOrg("sales") {
		t.Fatal("nil Access must answer no to everything")
	}
}

func TestWorkspaceAdminAccess(t *testing.T) {
	s := fixture(t)
	if _, err := s.Upsert(User{ID: "root2", Role: RoleAdmin}, "password"); err != nil {
		t.Fatal(err)
	}
	a, _ := s.Access("root2")
	if a.TileLevel("anything") != LevelTerminal || !a.CanCreateTile("x") || !a.TermAPI() || !a.TermNet() {
		t.Fatal("workspace admin must have everything")
	}
}

func TestCeiling(t *testing.T) {
	s := fixture(t)
	if err := s.SetPolicy([]PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgPolicy("sales", []PolicyRow{
		{Tiles: "apps/o/sales/*", Deny: []string{PolicyDenyGPU}, MayCall: []string{"apps/o/sales/*", "res:apps/o/sales/*"}},
	}); err != nil {
		t.Fatal(err)
	}

	// Org tile: both layers compose.
	c := s.Ceiling("apps/o/sales/crm")
	if !c.Denies(PolicyDenyNet) || !c.Denies(PolicyDenyGPU) || c.Denies(PolicyDenyXbinCaps) {
		t.Fatal("deny composition wrong")
	}
	if !c.MayCall("apps/o/sales/db") || !c.MayCall("res:apps/o/sales/db") {
		t.Fatal("in-org targets must pass mayCall")
	}
	if c.MayCall("apps/chat") {
		t.Fatal("out-of-list target must be blocked")
	}
	if row, blocked := c.MayCallBlocker("apps/chat"); !blocked || row.Tiles != "apps/o/sales/*" {
		t.Fatal("MayCallBlocker should name the blocking row")
	}

	// Non-org tile: only the workspace row applies.
	c = s.Ceiling("apps/chat")
	if !c.Denies(PolicyDenyNet) || c.Denies(PolicyDenyGPU) || !c.MayCall("anything/at/all") {
		t.Fatal("workspace-only composition wrong")
	}

	// Unmatched tile pattern ⇒ no ceiling.
	if err := s.SetPolicy([]PolicyRow{{Tiles: "sales-only/*", Deny: []string{PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if s.Ceiling("apps/chat").Denies(PolicyDenyNet) {
		t.Fatal("non-matching row must not apply")
	}

	// Validation.
	if err := s.SetPolicy([]PolicyRow{{Tiles: "", Deny: []string{PolicyDenyNet}}}); err == nil {
		t.Fatal("empty tiles pattern must be rejected")
	}
	if err := s.SetPolicy([]PolicyRow{{Tiles: "*", Deny: []string{"nope"}}}); err == nil {
		t.Fatal("unknown deny kind must be rejected")
	}
}

func TestOrgTeamCRUD(t *testing.T) {
	s := fixture(t)

	// Team members must be org members.
	if _, err := s.UpsertTeam("sales", Team{ID: "t2", Members: []string{"dave"}}); err == nil {
		t.Fatal("non-member in team must be rejected")
	}
	// Unknown users rejected everywhere.
	if _, err := s.UpsertOrg(Org{ID: "eng", Members: []string{"ghost"}}); err == nil {
		t.Fatal("unknown member must be rejected")
	}
	// Reserved org ids.
	for _, id := range []string{"o", "u", "workspace"} {
		if _, err := s.UpsertOrg(Org{ID: id}); err == nil {
			t.Fatalf("org id %q must be reserved", id)
		}
	}
	// Unknown levels rejected.
	if _, err := s.UpsertTeam("sales", Team{ID: "t3", Tiles: map[string]string{"x": "boss"}}); err == nil {
		t.Fatal("unknown tile level must be rejected")
	}
	if _, err := s.UpsertTeam("sales", Team{ID: "t3", NewTiles: "boss"}); err == nil {
		t.Fatal("unknown newTiles level must be rejected")
	}
	// NewTiles defaults to write.
	tm, err := s.UpsertTeam("sales", Team{ID: "t4"})
	if err != nil || tm.NewTiles != LevelWrite {
		t.Fatalf("newTiles default = %q err=%v, want write", tm.NewTiles, err)
	}

	// GrantTileTeam is monotone.
	if err := s.GrantTileTeam("sales", "backend", "apps/o/sales/newtile", LevelWrite); err != nil {
		t.Fatal(err)
	}
	if err := s.GrantTileTeam("sales", "backend", "apps/o/sales/newtile", LevelRead); err != nil {
		t.Fatal(err)
	}
	o, _ := s.Org("sales")
	if o.Team("backend").Tiles["apps/o/sales/newtile"] != LevelWrite {
		t.Fatal("GrantTileTeam must never lower")
	}

	// Org update preserves teams+policy and strips removed members from teams.
	if err := s.SetOrgPolicy("sales", []PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertOrg(Org{ID: "sales", Name: "Sales!", Admins: []string{"carol"}, Members: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Org("sales")
	if o.Name != "Sales!" || len(o.Policy) != 1 || o.Team("backend") == nil {
		t.Fatal("org update must preserve teams and policy")
	}
	if contains(o.Team("backend").Members, "bob") {
		t.Fatal("member removed from org must leave its teams")
	}

	// Deleting a user strips org/team membership.
	if _, err := s.UpsertOrg(Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertTeam("sales", Team{ID: "backend", Members: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("bob"); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Org("sales")
	if contains(o.Members, "bob") || contains(o.Team("backend").Members, "bob") {
		t.Fatal("deleted user must be stripped from orgs and teams")
	}
	if err := s.Delete("carol"); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Org("sales")
	if contains(o.Admins, "carol") {
		t.Fatal("deleted user must be stripped from org admins")
	}

	// Team + org deletion.
	if err := s.DeleteTeam("sales", "backend"); err != nil {
		t.Fatal(err)
	}
	o, _ = s.Org("sales")
	if o.Team("backend") != nil {
		t.Fatal("team not deleted")
	}
	if err := s.DeleteOrg("sales"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Org("sales"); ok {
		t.Fatal("org not deleted")
	}
}

func TestOrgPersistence(t *testing.T) {
	s := fixture(t)
	if err := s.SetPolicy([]PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(strings.TrimSuffix(s.path, "/users.json"))
	if err != nil {
		t.Fatal(err)
	}
	o, ok := s2.Org("sales")
	if !ok || o.Team("backend") == nil || !contains(o.Admins, "carol") {
		t.Fatal("org did not survive reopen")
	}
	if !s2.Ceiling("anything").Denies(PolicyDenyNet) {
		t.Fatal("workspace policy did not survive reopen")
	}
	bob, ok := s2.Access("bob")
	if !ok || bob.TileLevel("apps/o/sales/crm") != LevelWrite {
		t.Fatal("access resolution wrong after reopen")
	}
}

func TestParseTeamRef(t *testing.T) {
	org, team, err := ParseTeamRef("Sales/Backend")
	if err != nil || org != "sales" || team != "backend" {
		t.Fatalf("got %q %q %v", org, team, err)
	}
	for _, bad := range []string{"", "sales", "sales/", "/backend", "a/b/c"} {
		if _, _, err := ParseTeamRef(bad); err == nil {
			t.Errorf("ParseTeamRef(%q) should fail", bad)
		}
	}
}

// Creating inside an org requires membership (D19 amendment) — broad personal
// patterns must not reach into org trees; and the org container itself is not
// a valid tile path.
func TestOrgCreateMembershipAndContainer(t *testing.T) {
	s := fixture(t)
	// dave: outsider with a broad personal create pattern.
	if _, err := s.Upsert(User{ID: "dave", Role: RoleUser, CanCreate: []string{"apps/*"}}, "password"); err != nil {
		t.Fatal(err)
	}
	dave, _ := s.Access("dave")
	if !dave.CanCreateTile("apps/newthing") {
		t.Fatal("personal pattern must still work outside orgs")
	}
	if dave.CanCreateTile("apps/o/sales/newthing") {
		t.Fatal("non-member must not create inside an org, whatever their patterns")
	}
	// alice: member with the same pattern — allowed inside her org.
	if _, err := s.Upsert(User{ID: "alice", Role: RoleUser, CanCreate: []string{"apps/*"}}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertOrg(Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	alice, _ := s.Access("alice")
	if !alice.CanCreateTile("apps/o/sales/newthing") {
		t.Fatal("member's matching pattern must work inside their org")
	}

	// Org containers are not tile paths.
	for _, p := range []string{"o/sales", "apps/o/sales", "tiles/o/sales"} {
		if err := s.ValidateNewTilePath(p); err == nil || !strings.Contains(err.Error(), "container") {
			t.Errorf("ValidateNewTilePath(%q) = %v, want container error", p, err)
		}
	}
	if err := s.ValidateNewTilePath("apps/o/sales/crm"); err != nil {
		t.Errorf("below the container must stay valid: %v", err)
	}
}
