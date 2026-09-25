package auth

import (
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

func TestCanTerminalTileVia(t *testing.T) {
	u := &users.User{ID: "alice", Role: "user", Tiles: map[string]string{"apps/x": users.LevelTerminal, "apps/y": users.LevelTerminal}}
	// a human session: the plain gate
	if p := (Principal{UserID: "alice", Via: "session", User: u}); !p.CanTerminalTileVia("apps/x") || p.CanTerminalTileVia("apps/z") {
		t.Fatal("session principal")
	}
	// a terminal token: its own tile only, while the user's level holds
	tok := Principal{Component: "apps/x", UserID: "alice", Via: "terminal", User: u}
	if !tok.CanTerminalTileVia("apps/x") {
		t.Fatal("own tile refused")
	}
	if tok.CanTerminalTileVia("apps/y") {
		t.Fatal("another tile (terminal-level for the user) must be refused from a terminal token")
	}
	revoked := tok
	revoked.User = &users.User{ID: "alice", Role: "user", Tiles: map[string]string{"apps/x": users.LevelWrite}}
	if revoked.CanTerminalTileVia("apps/x") {
		t.Fatal("a withdrawn level must close the door")
	}
	// an owner-driven terminal (no user id): its own tile
	if p := (Principal{Component: "apps/x", Via: "terminal"}); !p.CanTerminalTileVia("apps/x") || p.CanTerminalTileVia("apps/y") {
		t.Fatal("owner terminal")
	}
	// other elements: never (as CanTerminalTile)
	if p := (Principal{Component: "apps/x", Via: "instance"}); p.CanTerminalTileVia("apps/x") {
		t.Fatal("an instance token is not a terminal")
	}
	if p := (Principal{Component: "apps/x", UserID: "alice", Via: "frame", User: u}); p.CanTerminalTileVia("apps/x") {
		t.Fatal("a frame token is not a terminal")
	}
}

// noTerminal (D88) closes every terminal door for the account — the session
// gates, a terminal token on their own tile, the coarse pre-gate — while
// write stays, and whatever the level source (here: ownership).
func TestNoTerminalClosesTerminals(t *testing.T) {
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/x", "user:alice"); err != nil {
		t.Fatal(err)
	}
	principals := func() (Principal, Principal) {
		a, _ := st.Access("alice")
		u, _ := st.Get("alice")
		return Principal{UserID: "alice", Via: "session", User: u, Access: a},
			Principal{Component: "apps/x", UserID: "alice", Via: "terminal", User: u, Access: a}
	}
	sess, tok := principals()
	if !sess.CanTerminal() || !sess.CanTerminalTile("apps/x") || !tok.CanTerminalTileVia("apps/x") {
		t.Fatal("baseline: the owner has a terminal on their tile")
	}
	if _, err := st.SetUserPersonal("alice", users.PersonalPatch{NoTerminal: func(b bool) *bool { return &b }(true)}); err != nil {
		t.Fatal(err)
	}
	sess, tok = principals()
	if sess.CanTerminal() || sess.CanTerminalTile("apps/x") || tok.CanTerminalTileVia("apps/x") {
		t.Fatal("noTerminal: no terminal door may stay open")
	}
	if !sess.CanWriteTile("apps/x") {
		t.Fatal("noTerminal keeps write")
	}
}
