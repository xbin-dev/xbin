package users

import "testing"

func defaultsStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(User{ID: "boss", Role: RoleAdmin}, "password123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertOrg(Org{ID: "corp", Members: []Member{{ID: "boss", Level: LevelTerminal, Admin: true}}}); err != nil {
		t.Fatal(err)
	}
	return s, dir
}

// New-account defaults (D52): validated on set, persisted, and applied as a
// UNION onto every creation path — password, invite, SSO JIT — never onto
// updates. Default orgs are joined unless already a member.
func TestNewUserDefaults(t *testing.T) {
	s, _ := defaultsStore(t)

	bad := []NewUserDefaults{
		{Tiles: map[string]string{"apps/*": "root"}},
		{Tiles: map[string]string{" ": LevelRead}},
		{Orgs: []OrgDefault{{Org: "nope"}}},
		{Orgs: []OrgDefault{{Org: "corp", Level: "owner"}}},
	}
	for i, d := range bad {
		if err := s.SetNewUserDefaults(d); err == nil {
			t.Errorf("bad defaults #%d accepted", i)
		}
	}
	if err := s.SetNewUserDefaults(NewUserDefaults{
		Tiles:     map[string]string{"apps/shared/*": LevelRead},
		CanCreate: []string{"apps/sandbox/*", "apps/sandbox/*"},
		TermNet:   true,
		Orgs:      []OrgDefault{{Org: "corp", Create: true}}, // level "" → read
	}); err != nil {
		t.Fatal(err)
	}
	got := s.NewUserDefaults()
	if len(got.CanCreate) != 1 || got.Orgs[0].Level != LevelRead || !got.Orgs[0].Create || !got.TermNet || got.TermAPI {
		t.Fatalf("normalized defaults: %+v", got)
	}

	// Password creation: request tiles/patterns ADD to the defaults, the
	// request wins on a shared path; flags OR; org joined with the default
	// knobs.
	u, err := s.Upsert(User{ID: "alice", Tiles: map[string]string{"apps/shared/*": LevelWrite, "apps/mine": LevelTerminal},
		CanCreate: []string{"apps/alice/*"}}, "password123")
	if err != nil {
		t.Fatal(err)
	}
	if u.Tiles["apps/shared/*"] != LevelWrite || u.Tiles["apps/mine"] != LevelTerminal || len(u.Tiles) != 2 {
		t.Fatalf("tiles union: %v", u.Tiles)
	}
	if len(u.CanCreate) != 2 || !u.TermNet || u.TermAPI {
		t.Fatalf("patterns/flags: %+v", u)
	}
	org, _ := s.Org("corp")
	m, ok := org.Member("alice")
	if !ok || m.Level != LevelRead || !m.Create || m.Admin {
		t.Fatalf("default org join: %+v %v", m, ok)
	}

	// An UPDATE of an existing row is not re-seeded (an admin who removed
	// the seeded entries must not see them come back).
	nu := *u
	nu.Tiles = map[string]string{}
	nu.CanCreate = nil
	nu.TermNet = false
	if u2, err := s.Upsert(nu, ""); err != nil || len(u2.Tiles) != 0 || len(u2.CanCreate) != 0 || u2.TermNet {
		t.Fatalf("update re-seeded: %+v %v", u2, err)
	}

	// Invite creation seeds too; an existing membership stays as it is.
	if _, err := s.UpsertOrg(Org{ID: "corp", Members: []Member{
		{ID: "boss", Level: LevelTerminal, Admin: true}, {ID: "bob", Level: LevelTerminal, Admin: true},
	}}); err == nil {
		t.Fatal("bob does not exist yet — UpsertOrg must refuse")
	}
	inv, err := s.UpsertInvited(User{ID: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Tiles["apps/shared/*"] != LevelRead || len(inv.CanCreate) != 1 || !inv.TermNet {
		t.Fatalf("invite seed: %+v", inv)
	}
	// The seed joined bob as read+create; a later AddOrgMember for an
	// existing member is a no-op (existing knobs win, no error).
	if err := s.AddOrgMember("corp", Member{ID: "bob", Level: LevelTerminal, Admin: true}); err != nil {
		t.Fatal(err)
	}
	org, _ = s.Org("corp")
	if m, ok := org.Member("bob"); !ok || m.Level != LevelRead || !m.Create || m.Admin {
		t.Fatalf("bob's seeded membership must stay: %+v %v", m, ok)
	}
}

// AddOrgMember: single-row join, idempotent, validated.
func TestAddOrgMember(t *testing.T) {
	s, _ := defaultsStore(t)
	if _, err := s.Upsert(User{ID: "carol"}, "password123"); err != nil {
		t.Fatal(err)
	}
	if err := s.AddOrgMember("nope", Member{ID: "carol"}); err == nil {
		t.Fatal("unknown org accepted")
	}
	if err := s.AddOrgMember("corp", Member{ID: "ghost"}); err == nil {
		t.Fatal("unknown user accepted")
	}
	if err := s.AddOrgMember("corp", Member{ID: "carol", Level: "owner"}); err == nil {
		t.Fatal("bad level accepted")
	}
	if err := s.AddOrgMember("corp", Member{ID: "carol", Create: true}); err != nil {
		t.Fatal(err)
	}
	// Already a member: no change, no error.
	if err := s.AddOrgMember("corp", Member{ID: "carol", Level: LevelTerminal, Admin: true}); err != nil {
		t.Fatal(err)
	}
	org, _ := s.Org("corp")
	m, _ := org.Member("carol")
	if m.Level != LevelRead || !m.Create || m.Admin || len(org.Members) != 2 {
		t.Fatalf("membership: %+v (%d members)", m, len(org.Members))
	}
	// Admin (boss) membership untouched.
	if bm, ok := org.Member("boss"); !ok || !bm.Admin {
		t.Fatalf("boss: %+v", bm)
	}
}

// SSO JIT provisioning lands with the seed + default orgs; the role is
// always user.
func TestProvisionSSOSeeded(t *testing.T) {
	s, dir := defaultsStore(t)
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "https://idp.corp.com", ClientID: "cid",
		AllowedDomains: []string{"corp.com"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetNewUserDefaults(NewUserDefaults{
		Tiles: map[string]string{"apps/shared/*": LevelWrite}, TermAPI: true,
		Orgs: []OrgDefault{{Org: "corp", Level: LevelWrite, Create: true}},
	}); err != nil {
		t.Fatal(err)
	}
	u, err := s.ProvisionSSO("dave@corp.com", "Dave")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != RoleUser || u.Tiles["apps/shared/*"] != LevelWrite || !u.TermAPI || u.TermNet {
		t.Fatalf("seeded JIT user: %+v", u)
	}
	acc, _ := s.Access("dave")
	if !acc.CanCreateAs("corp") {
		t.Fatal("default org membership must confer Create")
	}
	// Persisted: reopen and check both the seed and the membership.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if d := s2.NewUserDefaults(); len(d.Orgs) != 1 || d.Orgs[0].Org != "corp" || !d.TermAPI {
		t.Fatalf("reloaded defaults: %+v", d)
	}
	org, _ := s2.Org("corp")
	if m, ok := org.Member("dave"); !ok || m.Level != LevelWrite || !m.Create {
		t.Fatalf("reloaded membership: %+v %v", m, ok)
	}
	// A default org deleted later is skipped, not fatal.
	if _, err := s2.UpsertOrg(Org{ID: "old"}); err != nil {
		t.Fatal(err)
	}
	if err := s2.SetNewUserDefaults(NewUserDefaults{Orgs: []OrgDefault{{Org: "old"}, {Org: "corp"}}}); err != nil {
		t.Fatal(err)
	}
	if err := s2.DeleteOrg("old"); err != nil {
		t.Fatal(err)
	}
	e, err := s2.ProvisionSSO("erin@corp.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.UserOrgs(e.ID)) != 1 {
		t.Fatalf("stale default org must be skipped: %+v", s2.UserOrgs(e.ID))
	}
}

// Tile-creation policy: any|org-only, persisted, invalid refused.
func TestTileCreationPolicy(t *testing.T) {
	s, dir := defaultsStore(t)
	if s.TileCreation() != TileCreationAny {
		t.Fatalf("default policy: %q", s.TileCreation())
	}
	if err := s.SetTileCreation("nobody"); err == nil {
		t.Fatal("bad policy accepted")
	}
	if err := s.SetTileCreation(TileCreationOrgOnly); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s2.TileCreation() != TileCreationOrgOnly {
		t.Fatalf("reloaded policy: %q", s2.TileCreation())
	}
	if err := s2.SetTileCreation(""); err != nil || s2.TileCreation() != TileCreationAny {
		t.Fatalf("reset: %v %q", err, s2.TileCreation())
	}
}
