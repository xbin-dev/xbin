package broker

import (
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers D230 (review 2, 8) — the real answer to "who else could change
// what alice's coding agent runs on this tile": by every source of access,
// on the tile a path resolves to, for partitioned tiles as for any other.
func TestSigninOthers(t *testing.T) {
	f := newOpsFx(t) // apps/pg (partitioned: user+global; owner mona), apps/pu (partitioned), apps/q, apps/x; wendy writes apps/pg, zed reads apps/*
	b := f.b
	add := func(u users.User) {
		t.Helper()
		if _, err := b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	add(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelTerminal}})
	add(users.User{ID: "exact", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "writer", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelWrite}})
	add(users.User{ID: "reader", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}})
	add(users.User{ID: "pattern", Role: users.RoleUser, Tiles: map[string]string{"apps/p*": users.LevelTerminal}})
	add(users.User{ID: "gone", Role: users.RoleUser, Disabled: true, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "noterm", Role: users.RoleUser, NoTerminal: true, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "root2", Role: users.RoleAdmin})
	add(users.User{ID: "orgmember", Role: users.RoleUser})
	add(users.User{ID: "olga", Role: users.RoleUser})
	if _, err := b.Users.UpsertOrg(users.Org{ID: "crew", Name: "Crew", Members: []users.Member{{ID: "orgmember", Level: users.LevelWrite}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetOrgTile("crew", "apps/q", users.LevelWrite); err != nil { // shared to the org
		t.Fatal(err)
	}
	if err := b.Users.SetOwner("apps/pu", users.OwnerKindUser+":olga"); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		what, path, tile string
		others           []string
		ok               bool
	}{
		{"exact entries, write and terminal; never read, disabled, an admin or alice", "apps/x", "apps/x", []string{"exact", "noterm", "writer"}, true},
		{"a directory inside the tile is the tile", "apps/x/backend", "apps/x", []string{"exact", "noterm", "writer"}, true},
		{"deeper", "apps/x/backend/sub/dir", "apps/x", []string{"exact", "noterm", "writer"}, true},
		{"an org's share", "apps/q", "apps/q", []string{"orgmember"}, true},
		{"a partitioned tile is shared code: its owner, a writer and a pattern count", "apps/pg", "apps/pg", []string{"mona", "pattern", "wendy"}, true},
		{"a partitioned tile's owner counts", "apps/pu", "apps/pu", []string{"olga", "pattern"}, true},
		{"a deployment-qualified path is no tile", "apps/x+dev", "", nil, false},
		{"no tile", "apps/nowhere", "", nil, false},
		{"xbind's own trees", "data", "", nil, false},
	} {
		tile, others, ok := b.SigninOthers("alice", c.path)
		if tile != c.tile || ok != c.ok || !slices.Equal(others, c.others) {
			t.Errorf("%s (%s): tile %q others %v ok %v, want %q %v %v", c.what, c.path, tile, others, ok, c.tile, c.others, c.ok)
		}
	}
	// asked as someone else, alice herself is one of the others
	if _, others, _ := b.SigninOthers("exact", "apps/x"); !slices.Contains(others, "alice") || slices.Contains(others, "exact") {
		t.Errorf("asked as exact: %v", others)
	}
}
