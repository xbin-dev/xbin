package users

import (
	"testing"
)

func ssoStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// SSO config round-trips through the store, normalizes domains, keeps the
// stored secret on empty updates (write-only API semantics), and validates.
func TestSSOConfig(t *testing.T) {
	s := ssoStore(t)
	if s.SSO().Enabled() {
		t.Fatal("fresh store must have SSO off")
	}
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "http://insecure", ClientID: "x"}); err == nil {
		t.Fatal("http issuer must be refused")
	}
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "https://accounts.google.com/", ClientID: "cid",
		ClientSecret: "sec1", AllowedDomains: []string{" @Corp.com ", ""}}); err != nil {
		t.Fatal(err)
	}
	c := s.SSO()
	if !c.Enabled() || c.Issuer != "https://accounts.google.com" || len(c.AllowedDomains) != 1 || c.AllowedDomains[0] != "corp.com" {
		t.Fatalf("normalized config: %+v", c)
	}
	// Empty secret on update keeps the stored one.
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "https://accounts.google.com", ClientID: "cid2"}); err != nil {
		t.Fatal(err)
	}
	if got := s.SSO(); got.ClientSecret != "sec1" || got.ClientID != "cid2" {
		t.Fatalf("secret must survive an empty update: %+v", got)
	}
	// Domain list is replace-semantics: an update without domains clears the
	// allow-rule (the API layer always sends the full object).
	if s.SSO().DomainAllowed("alice@corp.com") {
		t.Fatal("update without domains must clear the allow-rule")
	}
}

// Email binding: unique across the store, case-folded, PATCH-preserved by
// the store layer (Upsert), and resolvable via FindByEmail.
func TestEmailBinding(t *testing.T) {
	s := ssoStore(t)
	if _, err := s.Upsert(User{ID: "alice", Email: "Alice@Corp.com"}, "password123"); err != nil {
		t.Fatal(err)
	}
	u, ok := s.FindByEmail("ALICE@corp.com")
	if !ok || u.ID != "alice" || u.Email != "alice@corp.com" {
		t.Fatalf("find by email: %+v %v", u, ok)
	}
	if _, err := s.Upsert(User{ID: "bob", Email: "alice@corp.com"}, "password123"); err == nil {
		t.Fatal("duplicate email must be refused")
	}
	if _, err := s.UpsertInvited(User{ID: "carol", Email: "alice@corp.com"}); err == nil {
		t.Fatal("duplicate email via invite must be refused")
	}
}

// An email binding must survive a daemon restart: User.UnmarshalJSON is
// hand-rolled and silently dropped `email` before 2026-09-05 — every SSO
// binding vanished on reload and the next save persisted the loss.
func TestEmailBindingPersistsAcrossReload(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(User{ID: "alice", Email: "Alice@Corp.com"}, "password123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertInvited(User{ID: "bob", Email: "bob@corp.com"}); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for id, email := range map[string]string{"alice": "alice@corp.com", "bob": "bob@corp.com"} {
		u, ok := s2.FindByEmail(email)
		if !ok || u.ID != id {
			t.Fatalf("binding %s → %s lost on reload: %+v %v", email, id, u, ok)
		}
	}
	// A save after reload must still carry the emails.
	if err := s2.SetTileCreation(TileCreationOrgOnly); err != nil {
		t.Fatal(err)
	}
	s3, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s3.FindByEmail("alice@corp.com"); !ok {
		t.Fatal("binding lost by a persist after reload")
	}
}

// JIT provisioning: domain rule enforced, id derived from the local part
// (folded to the id charset, collision-suffixed), defaultTiles-only access,
// credential-less, idempotent per email.
func TestProvisionSSO(t *testing.T) {
	s := ssoStore(t)
	if _, err := s.ProvisionSSO("eve@corp.com", ""); err == nil {
		t.Fatal("no SSO config: provisioning must refuse")
	}
	if err := s.SetSSO(&SSOConfig{Kind: "oidc", Issuer: "https://idp.corp.com", ClientID: "cid",
		AllowedDomains: []string{"corp.com"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ProvisionSSO("mallory@evil.com", ""); err == nil {
		t.Fatal("off-domain email must be refused")
	}
	u, err := s.ProvisionSSO("Jane.Doe+ops@corp.com", "Jane Doe")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != "jane.doe-ops" || u.Email != "jane.doe+ops@corp.com" || u.Role != RoleUser || u.Name != "Jane Doe" {
		t.Fatalf("provisioned: %+v", u)
	}
	got, _ := s.Get(u.ID)
	if got.PassHash != "" {
		t.Fatal("SSO users are credential-less (PassHash empty)")
	}
	// Same email again → same row, no duplicate.
	again, err := s.ProvisionSSO("jane.doe+ops@corp.com", "ignored")
	if err != nil || again.ID != u.ID {
		t.Fatalf("re-provision must return the existing row: %+v %v", again, err)
	}
	// Collision: a different email deriving the SAME id ("jane.doe#ops" also
	// folds to jane.doe-ops) gets a numeric suffix.
	u2, err := s.ProvisionSSO("jane.doe#ops@corp.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if u2.ID != "jane.doe-ops-2" {
		t.Fatalf("collision handling: got %q, want jane.doe-ops-2", u2.ID)
	}
	// Binding beats JIT: an admin-bound email resolves to that row even
	// though the domain rule would also match.
	if _, err := s.Upsert(User{ID: "boss", Email: "boss@corp.com"}, "password123"); err != nil {
		t.Fatal(err)
	}
	b, err := s.ProvisionSSO("boss@corp.com", "")
	if err != nil || b.ID != "boss" {
		t.Fatalf("existing binding must win: %+v %v", b, err)
	}
}

// deriveSSOID folds arbitrary local parts into the permanent id charset.
func TestDeriveSSOID(t *testing.T) {
	cases := map[string]string{
		"alice@x.com":                          "alice",
		"Jane.Doe@x.com":                       "jane.doe",
		"weird++name@x.com":                    "weird-name",
		"++@x.com":                             "user",
		"UPPER_case-ok@x.com":                  "upper_case-ok",
		".lead.trail.@x.com":                   "lead.trail",
		"very.long.local.part.that.goes@x.com": "very.long.local.part.tha",
	}
	for in, want := range cases {
		if got := deriveSSOID(in); got != want {
			t.Errorf("deriveSSOID(%q) = %q, want %q", in, got, want)
		}
	}
}
