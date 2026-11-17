package broker

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/signins"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers D230 (the rule of 2026-11-17) — the real answer to "where does
// this tile's code come from, for alice": who owns it (condition 1) and
// who else can change it (condition 2), each by every source of access, on
// the tile a path resolves to, for partitioned tiles as for any other —
// and so whether her trust is asked before her saved sign-in is used.
func TestSigninSource(t *testing.T) {
	f := newOpsFx(t) // apps/pg (partitioned: user+global; owner mona), apps/pu (partitioned), apps/q, apps/x; wendy writes apps/pg, zed reads apps/*
	b := f.b
	add := func(u users.User) {
		t.Helper()
		if _, err := b.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	add(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "exact", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "writer", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelWrite}})
	add(users.User{ID: "reader", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}})
	add(users.User{ID: "pattern", Role: users.RoleUser, Tiles: map[string]string{"apps/p*": users.LevelTerminal}})
	add(users.User{ID: "gone", Role: users.RoleUser, Disabled: true, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "noterm", Role: users.RoleUser, NoTerminal: true, Tiles: map[string]string{"apps/x": users.LevelTerminal}})
	add(users.User{ID: "root2", Name: "Root Two", Role: users.RoleAdmin})
	add(users.User{ID: "orgmember", Role: users.RoleUser})
	add(users.User{ID: "olga", Name: "Olga P", Role: users.RoleUser})
	if _, err := b.Users.UpsertOrg(users.Org{ID: "crew", Name: "Crew", Members: []users.Member{{ID: "orgmember", Level: users.LevelWrite}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetOrgTile("crew", "apps/q", users.LevelWrite); err != nil { // shared to the org
		t.Fatal(err)
	}
	owner := func(tile, ref string) {
		t.Helper()
		if err := b.Users.SetOwner(tile, ref); err != nil {
			t.Fatal(err)
		}
	}
	owner("apps/pu", "user:olga")
	type want struct {
		what, as, path, tile string
		kind, name           string
		notOwn               bool
		editors              []string
		trust, ok            bool
	}
	check := func(c want) signins.Source {
		t.Helper()
		src, ok := b.SigninSource(c.as, c.path)
		if src.Tile != c.tile || ok != c.ok || src.OwnerKind != c.kind || src.OwnerName != c.name || src.NotOwn != c.notOwn ||
			!slices.Equal(src.Editors, c.editors) || src.NeedsTrust() != c.trust || ok && src.ID == "" {
			t.Errorf("%s (%s as %s): %+v ok %v needs trust %v, want %+v", c.what, c.path, c.as, src, ok, src.NeedsTrust(), c)
		}
		return src
	}
	xEditors := []string{"exact", "noterm", "writer"}
	for _, c := range []want{
		{"the workspace's tile; exact entries, write and terminal — never read, disabled, an admin or alice", "alice", "apps/x", "apps/x", "workspace", "", true, xEditors, true, true},
		{"a directory inside the tile is the tile", "alice", "apps/x/backend", "apps/x", "workspace", "", true, xEditors, true, true},
		{"deeper", "alice", "apps/x/backend/sub/dir", "apps/x", "workspace", "", true, xEditors, true, true},
		{"an org's share, on the workspace's tile", "alice", "apps/q", "apps/q", "workspace", "", true, []string{"orgmember"}, true, true},
		{"another person's partitioned tile: shared code — its owner, a writer and a pattern can change it", "alice", "apps/pg", "apps/pg", "person", "mona", true, []string{"mona", "pattern", "wendy"}, true, true},
		{"another person's tile, named as people know them", "alice", "apps/pu", "apps/pu", "person", "Olga P", true, []string{"olga", "pattern"}, true, true},
		{"her own partitioned tile, with a co-editor by a pattern", "olga", "apps/pu", "apps/pu", "you", "olga", false, []string{"pattern"}, true, true},
		{"a deployment-qualified path is no tile", "alice", "apps/x+dev", "", "", "", false, nil, false, false},
		{"no tile", "alice", "apps/nowhere", "", "", "", false, nil, false, false},
		{"xbind's own trees", "alice", "data", "", "", "", false, nil, false, false},
	} {
		check(c)
	}
	// condition 1 alone, condition 2 alone, neither
	owner("apps/q", "org:crew")
	check(want{"an org's tile", "alice", "apps/q", "apps/q", "org", "Crew", true, []string{"orgmember"}, true, true})
	owner("apps/q", "user:orgmember")
	q := check(want{"her own tile, nobody else able to change it: nothing to trust", "orgmember", "apps/q", "apps/q", "you", "orgmember", false, nil, false, true})
	owner("apps/x", "user:alice")
	check(want{"her own tile, three co-editors", "alice", "apps/x", "apps/x", "you", "alice", false, xEditors, true, true})
	owner("apps/x", "user:root2")
	check(want{"an admin's own tile is another person's", "alice", "apps/x", "apps/x", "person", "Root Two", true, xEditors, true, true})
	// asked as someone else, alice herself is one of the editors
	if src, _ := b.SigninSource("exact", "apps/x"); !slices.Contains(src.Editors, "alice") || slices.Contains(src.Editors, "exact") {
		t.Errorf("asked as exact: %v", src.Editors)
	}
	// a tile about to be made: no identity, the same two conditions
	if src := b.SigninSourcePlanned("alice", "apps/new", "user:alice"); src.ID != "" || src.NeedsTrust() || src.OwnerKind != "you" {
		t.Errorf("a planned tile of her own: %+v", src)
	}
	if src := b.SigninSourcePlanned("alice", "apps/pnew", "user:alice"); !src.NeedsTrust() || !slices.Equal(src.Editors, []string{"pattern"}) {
		t.Errorf("a planned tile someone's pattern covers: %+v", src)
	}
	if src := b.SigninSourcePlanned("alice", "apps/new", "org:crew"); !src.NeedsTrust() || src.OwnerKind != "org" {
		t.Errorf("a planned tile of an org: %+v", src)
	}
	// its identity is the tile's, not its path's: made again at the same
	// path, it is another tile
	dir := filepath.Join(b.Reg.Root, "apps", "q")
	if err := os.Rename(dir, dir+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.CopyFS(dir, os.DirFS(dir+".old")); err != nil {
		t.Fatal(err)
	}
	if again, ok := b.SigninSource("orgmember", "apps/q"); !ok || again.ID == "" || again.ID == q.ID {
		t.Errorf("the tile made again at its path has the identity %q (was %q)", again.ID, q.ID)
	}
}
