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

// A users file edited by hand can hold what the store refuses to write: an
// entry above read on a sealed path. The readers that look at the stored
// map itself — "may this person open any terminal", and what "who am I"
// shows — go through the cap, as every level resolution does.
func TestSealedEntriesOfAHandEditedFile(t *testing.T) {
	const sealed = "xbin/wsagent"
	only := &User{ID: "eve", Role: RoleUser, Tiles: map[string]string{sealed: LevelTerminal, "xbin/*": LevelTerminal, "/" + sealed + "/": LevelWrite}}
	if only.CanTerminal() {
		t.Error("terminal entries on sealed paths alone open the terminal pre-gate")
	}
	if only.TileLevel(sealed) != LevelRead || only.CanTerminalTile(sealed) || only.CanWriteTile(sealed) {
		t.Errorf("its level there is %q", only.TileLevel(sealed))
	}
	shown := only.ShownTiles()
	if shown[sealed] != LevelRead || shown["xbin/*"] != LevelRead || shown["/"+sealed+"/"] != LevelRead {
		t.Errorf("ShownTiles: %v, want read on every sealed entry", shown)
	}
	if only.Tiles[sealed] != LevelTerminal {
		t.Error("ShownTiles changed the stored map")
	}
	both := &User{ID: "dan", Role: RoleUser, Tiles: map[string]string{sealed: LevelTerminal, "apps/a": LevelTerminal}}
	if !both.CanTerminal() || both.ShownTiles()["apps/a"] != LevelTerminal {
		t.Error("a tile's terminal entry no longer counts")
	}
	if (&User{ID: "x", Role: RoleUser, NoTerminal: true, Tiles: map[string]string{"apps/a": LevelTerminal}}).CanTerminal() {
		t.Error("NoTerminal no longer holds")
	}
	if (&User{ID: "n"}).ShownTiles() != nil {
		t.Error("ShownTiles of a user without entries")
	}
	// an Access built over such a user answers the same
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Upsert(User{ID: "eve", Role: RoleUser, Tiles: map[string]string{"xbin/*": LevelTerminal}}, "pw-eve-pw-pw"); err != nil {
		t.Fatal(err)
	}
	a, ok := s.Access("eve")
	if !ok {
		t.Fatal("no Access for a stored user")
	}
	// (a pattern gives nothing on a sealed component: only an entry that names it does)
	if a.CanTerminal() || a.CanTerminalTile(sealed) || a.TileLevel(sealed) != "" {
		t.Errorf("Access: terminal pre-gate %v, level %q", a.CanTerminal(), a.TileLevel(sealed))
	}
}

// Nobody has a sealed component until it is given to them: "*" and every
// other pattern were written about tiles and give nothing there — in a
// person's entries, an org's shares, the workspace defaults. An entry that
// names it does, wherever it is; an admin reads it without one. The store
// says who was given it, and gives it to everyone (and takes that back)
// without touching anyone's own entry.
func TestSealedIsReachedOnlyByName(t *testing.T) {
	const sealed = "xbin/wsagent"
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetDefaultTiles(map[string]string{"*": LevelRead, "xbin/*": LevelRead, "apps/*": LevelWrite}); err != nil {
		t.Fatal(err)
	}
	for id, tiles := range map[string]map[string]string{
		"star": {"*": LevelTerminal}, "glob": {"xbin/*": LevelRead, "xbin/ws*": LevelRead}, "named": {sealed: LevelRead}, "none": nil,
	} {
		if _, err := s.Upsert(User{ID: id, Role: RoleUser, Tiles: tiles}, "pw-"+id+"-pw-pw"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Upsert(User{ID: "boss", Role: RoleAdmin}, "pw-boss-pw-pw"); err != nil {
		t.Fatal(err)
	}
	level := func(id string) string { a, _ := s.Access(id); return a.TileLevel(sealed) }
	for id, want := range map[string]string{"star": "", "glob": "", "none": "", "named": LevelRead, "boss": LevelRead} {
		if got := level(id); got != want {
			t.Errorf("%s's level on %s: %q, want %q", id, sealed, got, want)
		}
		if u, _ := s.Get(id); u.TileLevel(sealed) != want {
			t.Errorf("%s's own entries give %q on %s, want %q", id, u.TileLevel(sealed), sealed, want)
		}
	}
	if a, _ := s.Access("star"); a.TileLevel("apps/a") != LevelTerminal {
		t.Error("a pattern no longer reaches a tile")
	}
	if all, n := s.SealedGrants(sealed); all || n != 1 {
		t.Errorf("who was given it: everyone %v, entries %d; want one person", all, n)
	}
	// everyone, by one entry in the workspace defaults
	if err := s.SetSealedEveryone(sealed, true); err != nil {
		t.Fatal(err)
	}
	if all, _ := s.SealedGrants(sealed); !all || level("none") != LevelRead || level("star") != LevelRead {
		t.Errorf("after giving it to everyone: everyone %v, none %q, star %q", all, level("none"), level("star"))
	}
	if d := s.DefaultTiles(); d["*"] != LevelRead || d["apps/*"] != LevelWrite || d[sealed] != LevelRead {
		t.Errorf("the defaults after: %v", d)
	}
	if err := s.SetSealedEveryone(sealed, false); err != nil {
		t.Fatal(err)
	}
	if all, n := s.SealedGrants(sealed); all || n != 1 || level("none") != "" || level("named") != LevelRead {
		t.Errorf("after taking it back: everyone %v, entries %d, none %q, named %q", all, n, level("none"), level("named"))
	}
	for _, bad := range []string{"apps/a", "xbin/*", "*"} {
		if err := s.SetSealedEveryone(bad, true); err == nil {
			t.Errorf("SetSealedEveryone(%q) passed", bad)
		}
	}
	// an org's share names it too
	if _, err := s.UpsertOrg(Org{ID: "sales", Members: []Member{{ID: "none", Level: LevelRead}}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgTile("sales", sealed, LevelRead); err != nil {
		t.Fatal(err)
	}
	if _, n := s.SealedGrants(sealed); n != 2 || level("none") != LevelRead {
		t.Errorf("an org's share: entries %d, its member's level %q", n, level("none"))
	}
}
