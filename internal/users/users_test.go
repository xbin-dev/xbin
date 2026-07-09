package users

import (
	"strings"
	"testing"
)

func TestPasswordHashing(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Upsert(User{ID: "alice", Role: RoleUser}, "s3cret"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("alice", "s3cret"); !ok {
		t.Fatal("correct password rejected")
	}
	if _, ok := s.Verify("alice", "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, ok := s.Verify("nobody", "x"); ok {
		t.Fatal("unknown user accepted")
	}
	// Persistence across reopen.
	s2, err := Open(s.path[:len(s.path)-len("/users.json")])
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Verify("alice", "s3cret"); !ok {
		t.Fatal("password did not persist across reopen")
	}
}

func TestCanUseTile(t *testing.T) {
	admin := &User{Role: RoleAdmin}
	if !admin.CanUseTile("anything/at/all") || !admin.CanTerminal() {
		t.Fatal("admin should use all tiles + terminal")
	}
	u := &User{Role: RoleUser, Tiles: []string{"apps/chat", "lib/*"}}
	cases := map[string]bool{
		"apps/chat":     true,
		"apps/chatX":    false, // not a prefix, exact only
		"apps/other":    false,
		"lib":           true, // prefix root
		"lib/ui/button": true, // under prefix
		"libs/x":        false,
	}
	for path, want := range cases {
		if got := u.CanUseTile(path); got != want {
			t.Errorf("CanUseTile(%q)=%v want %v", path, got, want)
		}
	}
	if u.CanTerminal() {
		t.Fatal("regular user without flag must not have terminal")
	}
	u.Terminal = true
	if !u.CanTerminal() {
		t.Fatal("terminal flag ignored")
	}
	star := &User{Role: RoleUser, Tiles: []string{"*"}}
	if !star.CanUseTile("apps/anything") {
		t.Fatal("* should allow all")
	}
}

func TestPublicStripsHash(t *testing.T) {
	u := User{ID: "x", PassHash: "argon2id$...$..."}
	if u.Public().PassHash != "" {
		t.Fatal("Public must strip the password hash")
	}
}

func TestTokenLoginDisabledGuardAndPersist(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)

	// Refuse to disable token login while there's no admin (avoids lockout).
	if err := s.SetTokenLoginDisabled(true); err == nil {
		t.Fatal("disabled token login with no admin user present")
	}
	if s.TokenLoginDisabled() {
		t.Fatal("flag set despite the guard erroring")
	}

	if _, err := s.Upsert(User{ID: "root", Role: RoleAdmin}, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTokenLoginDisabled(true); err != nil {
		t.Fatalf("disable failed with an admin present: %v", err)
	}
	if !s.TokenLoginDisabled() || !s.HasAdmin() {
		t.Fatal("expected tokenLoginDisabled + HasAdmin")
	}

	// Survives a reload (persisted in users.json).
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !s2.TokenLoginDisabled() {
		t.Fatal("tokenLoginDisabled not persisted across Open")
	}
}

// A user id is a permanent key (homes/<user>, prefs bucket, user:<id>
// attribution) — validated at creation, never renamable. Locks the boundary
// before GA (DECISIONS D15).
func TestUserIDValidation(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bad := []string{
		"owner",                 // collides with the token principal's homes/owner
		"a/b",                   // path separator → homes/ + attribution ambiguity
		"foo:bar",               // attribution separator
		"Alice Doe",             // space (would normalize but still invalid charset)
		"..",                    // dots-only
		"-lead",                 // must start alphanumeric
		"",                      // empty
		strings.Repeat("x", 33), // too long
	}
	for _, id := range bad {
		if _, err := s.Upsert(User{ID: id, Role: RoleUser}, "pw"); err == nil {
			t.Errorf("id %q should be rejected", id)
		}
	}
	good := []string{"alice", "bob.smith", "carol-99", "d_e_v", "admin"}
	for _, id := range good {
		if _, err := s.Upsert(User{ID: id, Role: RoleUser}, "pw"); err != nil {
			t.Errorf("id %q should be accepted: %v", id, err)
		}
	}
	// A legacy/odd id already on disk stays editable (load bypasses Upsert;
	// only *new* ids are gated) — plant the realistic on-disk form (already
	// lowercased by the old normalizeID) with a char new validID rejects.
	s.byID["legacy/id"] = &User{ID: "legacy/id", Role: RoleUser, PassHash: "x"}
	if _, err := s.Upsert(User{ID: "legacy/id", Role: RoleAdmin}, ""); err != nil {
		t.Errorf("editing a pre-existing odd id must still work: %v", err)
	}
}
