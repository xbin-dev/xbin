package users

import "testing"

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
