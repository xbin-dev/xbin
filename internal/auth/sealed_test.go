package auth

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// On a sealed component (D227) the levels above read don't exist for any
// principal: the root token, an admin, a person whose patterns or entries
// say otherwise, a tile, its own page, its own backend, a terminal token.
// Reading it is as for a tile.
func TestSealedPathHasNoLevelAboveRead(t *testing.T) {
	const sealed, tile = "xbin/wsagent", "apps/a"
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id, role string, tiles map[string]string) Principal {
		t.Helper()
		u, err := st.Upsert(users.User{ID: id, Role: role, Tiles: tiles}, "pw-"+id+"-pw-pw")
		if err != nil {
			t.Fatal(err)
		}
		a, _ := st.Access(id)
		return Principal{UserID: id, User: u, Access: a, Via: "session"}
	}
	admin := mk("adm", users.RoleAdmin, nil)
	patterned := mk("pat", users.RoleUser, map[string]string{"*": users.LevelTerminal, "xbin/*": users.LevelTerminal})
	reader := mk("rdr", users.RoleUser, map[string]string{sealed: users.LevelRead})
	nobody := mk("nob", users.RoleUser, nil)
	// what a hand-edited users file could hold: built without the store
	edited := Principal{UserID: "eve", Via: "session", User: &users.User{ID: "eve", Role: users.RoleUser,
		Tiles: map[string]string{sealed: users.LevelTerminal}}}

	cases := []struct {
		what string
		p    Principal
		read bool
	}{
		{"the root token", Principal{Owner: true, Via: "bearer"}, true},
		{"an admin", admin, true},
		{"a person with terminal patterns", patterned, false}, // a pattern gives nothing there: only an entry that names it
		{"a reader of it", reader, true},
		{"a person without access", nobody, false},
		{"a hand-edited terminal entry", edited, true},
		{"another tile's backend", Principal{Component: tile, Via: "instance"}, false},
		{"its own backend", Principal{Component: sealed, Via: "instance"}, true},
		{"its own page, opened by the root token", Principal{Component: sealed, Via: "frame"}, true},
		{"a terminal token naming it", Principal{Component: sealed, Via: "terminal"}, true},
		{"the root token's terminal on a tile", Principal{Component: tile, Via: "terminal"}, true},
	}
	for _, c := range cases {
		if c.p.CanTerminalTile(sealed) || c.p.CanTerminalTileVia(sealed) {
			t.Errorf("%s: the terminal level on a sealed component", c.what)
		}
		if c.p.CanWriteTile(sealed) {
			t.Errorf("%s: the write level on a sealed component", c.what)
		}
		if got := c.p.CanReadTile(sealed); got != c.read {
			t.Errorf("%s: CanReadTile = %v, want %v", c.what, got, c.read)
		}
	}
	// the coarse terminal pre-gate: entries on sealed paths open nothing
	if edited.CanTerminal() {
		t.Error("a terminal entry on a sealed path alone opens the terminal pre-gate")
	}
	// and a tile's levels are as they were
	if !admin.CanTerminalTile(tile) || !admin.CanWriteTile(tile) || !patterned.CanTerminalTile(tile) || !patterned.CanTerminal() ||
		reader.CanReadTile(tile) || !(Principal{Owner: true}).CanTerminalTile(tile) {
		t.Error("a tile's levels changed")
	}
}
