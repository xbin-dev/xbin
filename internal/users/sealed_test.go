package users

import (
	"strings"
	"testing"
)

// On a sealed component's path (D227) access is read or none: an entry that
// names the path above read is refused wherever entries are written, and a
// pattern that reaches it gives read.
func TestSealedPathLevels(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const sealed = "xbin/wsagent"
	refused := func(what string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "is a system component, managed by xbin") {
			t.Errorf("%s: %v, want the system-component refusal", what, err)
		}
	}
	if _, err := s.Upsert(User{ID: "ann", Role: RoleUser, Tiles: map[string]string{"*": LevelTerminal, "xbin/*": LevelWrite, sealed: LevelRead, "apps/a": LevelTerminal}}, "pw-ann-pw-pw"); err != nil {
		t.Fatalf("patterns above read and an entry at read: %v", err)
	}
	for _, level := range []string{LevelWrite, LevelTerminal} {
		_, err := s.Upsert(User{ID: "bob", Role: RoleUser, Tiles: map[string]string{sealed: level}}, "pw-bob-pw-pw")
		refused("a new user's map at "+level, err)
		_, err = s.Upsert(User{ID: "ann", Role: RoleUser, Tiles: map[string]string{"/" + sealed + "/": level}}, "")
		refused("a patched map at "+level, err)
		refused("SetUserTile "+level, s.SetUserTile("ann", sealed, level))
		refused("GrantTile "+level, s.GrantTile("ann", sealed, level))
		refused("SetDefaultTiles "+level, s.SetDefaultTiles(map[string]string{sealed: level}))
		refused("SetNewUserDefaults "+level, s.SetNewUserDefaults(NewUserDefaults{Tiles: map[string]string{sealed: level}}))
		refused("CreateAccessRequest "+level, s.CreateAccessRequest("ann", sealed, level, ""))
	}
	refused("SetOwner", s.SetOwner(sealed, "user:ann"))
	if err := s.SetDefaultTiles(map[string]string{"xbin/*": LevelTerminal, sealed: LevelRead}); err != nil {
		t.Fatalf("a default pattern above read: %v", err)
	}
	if err := s.SetUserTile("ann", sealed, LevelNone); err != nil {
		t.Fatalf("an explicit exclusion: %v", err)
	}
	if err := s.SetUserTile("ann", sealed, LevelRead); err != nil {
		t.Fatal(err)
	}
	acc, ok := s.Access("ann")
	if !ok {
		t.Fatal("no access for ann")
	}
	if got := acc.TileLevel(sealed); got != LevelRead {
		t.Errorf("ann's level on %s is %q: patterns at terminal and write give read", sealed, got)
	}
	if got := acc.TileLevel("apps/a"); got != LevelTerminal {
		t.Errorf("ann's level on a tile is %q", got)
	}
	if acc.CanWriteTile(sealed) || acc.CanTerminalTile(sealed) || !acc.CanReadTile(sealed) {
		t.Error("the levels above read exist on a sealed path")
	}
	// an admin too
	if _, err := s.Upsert(User{ID: "root", Role: RoleAdmin}, "pw-root-pw-pw"); err != nil {
		t.Fatal(err)
	}
	adm, _ := s.Access("root")
	if got := adm.TileLevel(sealed); got != LevelRead {
		t.Errorf("an admin's level on %s is %q", sealed, got)
	}
	if got := adm.TileLevel("apps/a"); got != LevelTerminal {
		t.Errorf("an admin's level on a tile is %q", got)
	}
}
