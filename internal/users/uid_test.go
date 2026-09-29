package users

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covers PD-43 — a person's uid: none until their first partition (a new
// account's users.json is today's), then minted, persisted and stable;
// kept by an API update that doesn't carry it (or carries another), never
// on the wire; adopted instead of re-minted when an older xbind's rewrite
// dropped it; and gone with the record, so a recreated id starts over.
func TestUID(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if _, err := s.Upsert(User{ID: "alice", Role: RoleUser, UID: "0123456789abcdef0123456789abcdef"}, "password1"); err != nil {
		t.Fatal(err)
	}
	raw := func() string {
		b, err := os.ReadFile(filepath.Join(dir, "users.json"))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	if u, _ := s.Get("alice"); u.UID != "" || strings.Contains(raw(), `"uid"`) {
		t.Fatalf("a new account has a uid before any partition: %q\n%s", u.UID, raw())
	}
	uid, err := s.EnsureUID("alice", "")
	if err != nil || !UIDOK(uid) {
		t.Fatalf("EnsureUID = %q, %v", uid, err)
	}
	if again, _ := s.EnsureUID("Alice", "ffffffffffffffffffffffffffffffff"); again != uid {
		t.Errorf("a second EnsureUID changed the uid: %q → %q", uid, again)
	}
	s2, _ := Open(dir)
	if u, _ := s2.Get("alice"); u.UID != uid {
		t.Errorf("the uid didn't persist: %q, want %q", u.UID, uid)
	}
	if _, err := s2.Upsert(User{ID: "alice", Name: "Alice", Role: RoleUser, UID: "ffffffffffffffffffffffffffffffff"}, ""); err != nil {
		t.Fatal(err)
	}
	if u, _ := s2.Get("alice"); u.UID != uid {
		t.Errorf("an API update replaced the uid: %q, want %q", u.UID, uid)
	}
	for _, u := range s2.List() {
		if u.UID != "" {
			t.Errorf("GET /users lists %s's uid", u.ID)
		}
	}
	if b, _ := json.Marshal(func() User { u, _ := s2.Get("alice"); return u.Public() }()); strings.Contains(string(b), "uid") {
		t.Errorf("Public() carries the uid: %s", b)
	}

	// An older xbind rewrote the store and dropped the field: the caller
	// passes the uid its partition records carry, and it is adopted.
	var doc map[string]any
	_ = json.Unmarshal([]byte(raw()), &doc)
	for _, u := range doc["users"].([]any) {
		delete(u.(map[string]any), "uid")
	}
	b, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(dir, "users.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	s3, _ := Open(dir)
	if got, _ := s3.EnsureUID("alice", uid); got != uid {
		t.Errorf("the dropped uid wasn't adopted: %q, want %q", got, uid)
	}
	if got, _ := s3.EnsureUID("nobody", ""); got != "" {
		t.Errorf("EnsureUID minted for a missing user: %q", got)
	}

	// Deleted and recreated: a new incarnation, a new uid.
	if _, err := s3.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := s3.Upsert(User{ID: "alice", Role: RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	if u, _ := s3.Get("alice"); u.UID != "" {
		t.Errorf("a recreated alice inherited uid %q", u.UID)
	}
	if got, _ := s3.EnsureUID("alice", "not-a-uid"); got == uid || !UIDOK(got) {
		t.Errorf("the recreated alice's uid = %q (the old one was %q)", got, uid)
	}
}
