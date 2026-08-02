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

func TestTileLevels(t *testing.T) {
	admin := &User{Role: RoleAdmin}
	if admin.TileLevel("anything/at/all") != LevelTerminal || !admin.CanTerminal() {
		t.Fatal("admin should have terminal everywhere")
	}
	u := &User{Role: RoleUser, Tiles: map[string]string{"apps/chat": LevelWrite, "lib/*": LevelRead}}
	cases := map[string]string{
		"apps/chat":     LevelWrite,
		"apps/chatX":    "", // not a prefix, exact only
		"apps/other":    "",
		"lib":           LevelRead, // prefix root
		"lib/ui/button": LevelRead, // under prefix
		"libs/x":        "",
	}
	for path, want := range cases {
		if got := u.TileLevel(path); got != want {
			t.Errorf("TileLevel(%q)=%q want %q", path, got, want)
		}
	}
	// Monotone gates: write ⊇ read, terminal ⊇ write.
	if !u.CanReadTile("apps/chat") || !u.CanWriteTile("apps/chat") || u.CanTerminalTile("apps/chat") {
		t.Fatal("write level must grant read+write, not terminal")
	}
	if !u.CanReadTile("lib/ui") || u.CanWriteTile("lib/ui") {
		t.Fatal("read level must grant read only")
	}
	if u.CanTerminal() {
		t.Fatal("no terminal-level entry → no terminal pre-gate")
	}
	u.Tiles["apps/chat"] = LevelTerminal
	if !u.CanTerminal() || !u.CanTerminalTile("apps/chat") {
		t.Fatal("terminal level ignored")
	}
	// An EXACT entry is authoritative (D31): it overrides matching patterns —
	// down as well as up — and `none` excludes outright.
	both := &User{Role: RoleUser, Tiles: map[string]string{"apps/*": LevelTerminal, "apps/chat": LevelRead}}
	if got := both.TileLevel("apps/chat"); got != LevelRead {
		t.Fatalf("exact entry must override the pattern: got %q, want read", got)
	}
	if !both.CanTerminalTile("apps/other") {
		t.Fatal("patterns still union where no exact entry exists")
	}
	excl := &User{Role: RoleUser, Tiles: map[string]string{"apps/*": LevelWrite, "apps/chat": LevelNone}}
	if excl.CanReadTile("apps/chat") {
		t.Fatal("exact none must exclude")
	}
	star := &User{Role: RoleUser, Tiles: map[string]string{"*": LevelWrite}}
	if !star.CanWriteTile("apps/anything") {
		t.Fatal("* should allow all")
	}
}

// Legacy users.json shape (tiles array + global terminal flag) loads as the
// power it had: write on each entry, terminal when the flag was set.
func TestLegacyUserShape(t *testing.T) {
	var u User
	if err := u.UnmarshalJSON([]byte(`{"id":"bob","role":"user","tiles":["apps/a","lib/*"],"terminal":true}`)); err != nil {
		t.Fatal(err)
	}
	if u.TileLevel("apps/a") != LevelTerminal || u.TileLevel("lib/x") != LevelTerminal {
		t.Fatalf("legacy terminal user: tiles = %v, want terminal on both", u.Tiles)
	}
	var v User
	if err := v.UnmarshalJSON([]byte(`{"id":"eve","role":"user","tiles":["apps/a"]}`)); err != nil {
		t.Fatal(err)
	}
	if v.TileLevel("apps/a") != LevelWrite || v.CanTerminal() {
		t.Fatalf("legacy non-terminal user: tiles = %v, want write, no terminal", v.Tiles)
	}
	// New shape passes through; unknown levels are rejected, not misread.
	var w User
	if err := w.UnmarshalJSON([]byte(`{"id":"kim","tiles":{"apps/a":"read"}}`)); err != nil || w.TileLevel("apps/a") != LevelRead {
		t.Fatalf("map shape: %v / %v", err, w.Tiles)
	}
	if err := w.UnmarshalJSON([]byte(`{"id":"kim","tiles":{"apps/a":"rw"}}`)); err == nil {
		t.Fatal("unknown level must be rejected")
	}
}

func TestCanCreateAndGrantTile(t *testing.T) {
	s, _ := Open(t.TempDir())
	if _, err := s.Upsert(User{ID: "dev", Role: RoleUser, CanCreate: []string{"sales/*"}}, "pw"); err != nil {
		t.Fatal(err)
	}
	u, _ := s.Get("dev")
	if !u.CanCreateTile("sales/leads") || u.CanCreateTile("apps/x") {
		t.Fatal("CanCreate must be pattern-scoped")
	}
	// The create auto-grant: terminal on the new tile, raise-only.
	if err := s.GrantTile("dev", "sales/leads", LevelTerminal); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Get("dev")
	if !u.CanTerminalTile("sales/leads") {
		t.Fatal("auto-grant missing")
	}
	if err := s.GrantTile("dev", "sales/leads", LevelRead); err != nil {
		t.Fatal(err)
	}
	u, _ = s.Get("dev")
	if !u.CanTerminalTile("sales/leads") {
		t.Fatal("GrantTile must never lower a level")
	}
	// Persisted (reload sees the grant).
	s2, err := Open(s.path[:len(s.path)-len("/users.json")])
	if err != nil {
		t.Fatal(err)
	}
	u2, _ := s2.Get("dev")
	if !u2.CanTerminalTile("sales/leads") {
		t.Fatal("grant not persisted")
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
