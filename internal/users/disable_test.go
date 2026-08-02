package users

import "testing"

// D34: account disable — everything refuses, nothing is lost.
func TestUserDisable(t *testing.T) {
	s := newStore(t)
	if _, ok := s.Verify("bob", "password"); !ok {
		t.Fatal("precondition: bob logs in")
	}
	u, _ := s.Get("bob")
	nu := *u
	nu.Disabled = true
	if _, err := s.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("bob", "password"); ok {
		t.Fatal("disabled user must not verify")
	}
	// Invites refuse for disabled users too.
	tok, err := s.CreateInvite("bob", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.InviteUser(tok); ok {
		t.Fatal("disabled user's invite must not verify")
	}
	if _, err := s.RedeemInvite(tok, "new-password-1"); err == nil {
		t.Fatal("disabled user's invite must not redeem")
	}
	// Rows survive: re-enable restores the exact account.
	u2, _ := s.Get("bob")
	nu2 := *u2
	nu2.Disabled = false
	if _, err := s.Upsert(nu2, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("bob", "password"); !ok {
		t.Fatal("re-enabled user must log in with the same password")
	}
}

// A plain PATCH-style Upsert must not burn a pending invite (D22/D34 fix).
func TestUpsertKeepsPendingInvite(t *testing.T) {
	s := newStore(t)
	tok, err := s.CreateInvite("carol", 0)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := s.Get("carol")
	nu := *u
	nu.Name = "Carol Renamed"
	nu.InviteHash, nu.InviteExpires = "", 0 // what an API overlay naturally passes
	if _, err := s.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.InviteUser(tok); !ok {
		t.Fatal("rename must not invalidate the pending invite")
	}
}

// D34: org member suspension — the membership confers nothing while set.
func TestMemberSuspension(t *testing.T) {
	s := newStore(t)
	mustOrg(t, s, Org{ID: "crew", Members: []Member{
		{ID: "alice", Level: LevelTerminal, Admin: true},
		{ID: "bob", Level: LevelWrite, Create: true},
	}})
	if err := s.SetOwner("apps/mc", "org:crew"); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPermissionSet("net", PermissionSet{TermNet: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgSets("crew", []string{"net"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgTile("crew", "apps/media", LevelRead); err != nil {
		t.Fatal(err)
	}
	// Baseline: bob has it all.
	b := acc(t, s, "bob")
	if b.TileLevel("apps/mc") != LevelWrite || !b.CanCreateAs("crew") ||
		!b.TermNet() || b.TileLevel("apps/media") != LevelRead {
		t.Fatal("precondition: active membership confers")
	}
	// Suspend bob.
	o, _ := s.Org("crew")
	up := *o
	for i, m := range up.Members {
		if m.ID == "bob" {
			up.Members[i].Suspended = true
		}
	}
	if _, err := s.UpsertOrg(up); err != nil {
		t.Fatal(err)
	}
	b = acc(t, s, "bob")
	if b.TileLevel("apps/mc") != "" {
		t.Error("suspended member must lose org-tile access")
	}
	if b.CanCreateAs("crew") {
		t.Error("suspended member must lose create")
	}
	if b.TermNet() {
		t.Error("suspended member must lose set-conferred flags")
	}
	if b.TileLevel("apps/media") != "" {
		t.Error("suspended member must lose org shares")
	}
	// Suspended admins hold no adminship.
	o, _ = s.Org("crew")
	up = *o
	for i, m := range up.Members {
		up.Members[i].Suspended = m.ID == "alice"
	}
	if _, err := s.UpsertOrg(up); err != nil {
		t.Fatal(err)
	}
	if acc(t, s, "alice").IsAdminOrg("crew") {
		t.Error("suspended admin must not administer")
	}
	// whoami still shows the membership, flagged.
	found := false
	for _, m := range s.UserOrgs("alice") {
		if m.ID == "crew" {
			found = true
			if !m.Suspended {
				t.Error("UserOrgs must flag suspension")
			}
		}
	}
	if !found {
		t.Error("suspended membership must stay listed")
	}
}
