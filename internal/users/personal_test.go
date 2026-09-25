package users

import (
	"strings"
	"testing"
)

func mustDo(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func bptr(b bool) *bool                { return &b }
func sptr(s ...string) *[]string       { return &s }
func has(list []string, e string) bool { return contains(list, e) }

// personalStore: alice/bob/carol plus a permission set, two network sets.
func personalStore(t *testing.T) *Store {
	t.Helper()
	s := newStore(t)
	mustDo(t, s.UpsertPermissionSet("builders", PermissionSet{
		Allow:   []string{"cap:containers", "gpu:*"},
		Policy:  []PolicyRow{{Tiles: "*", Deny: []string{PolicyDenyIngress}}},
		TermAPI: true,
	}))
	mustDo(t, s.UpsertNetSet("web", NetSet{Rules: []string{"internet"}}))
	mustDo(t, s.UpsertNetSet("lab", NetSet{Rules: []string{"lan:10.1.0.0/16"}}))
	return s
}

// The personal plane is defaults ∪ own, and every piece of it resolves:
// sets, net sets, the personal network, the allowance, ceiling rows and
// terminal flags. Admins get none of it (they bypass the gates it feeds).
func TestPersonalPlaneResolution(t *testing.T) {
	s := personalStore(t)
	if p := s.Personal("alice"); len(p.Sets)+len(p.NetSets)+len(p.Allow) != 0 {
		t.Fatalf("no defaults, no sets → empty plane: %+v", p)
	}
	mustDo(t, s.SetPersonalDefaults(PersonalDefaults{NetSets: []string{"web"}}))
	if _, err := s.SetUserPersonal("alice", PersonalPatch{Sets: sptr("builders"), NetSets: sptr("lab", "lab")}); err != nil {
		t.Fatal(err)
	}
	p := s.Personal("alice")
	if strings.Join(p.Sets, ",") != "builders" || strings.Join(p.NetSets, ",") != "lab,web" {
		t.Fatalf("defaults ∪ own: %+v", p)
	}
	if strings.Join(p.NetRules, ",") != "internet,lan:10.1.0.0/16" {
		t.Fatalf("personal network = union of rules: %v", p.NetRules)
	}
	for _, want := range []string{"cap:containers", "gpu:*", "net:internet", "net:lan:10.1.0.0/16"} {
		if !has(p.Allow, want) {
			t.Errorf("allowance misses %s: %v", want, p.Allow)
		}
	}
	if !s.PersonalAllowanceCovers("alice", "gpu:0", "") || !s.PersonalAllowanceCovers("alice", "net:lan:10.1.2.3", "") {
		t.Error("allowance must cover gpu:0 and a lan host inside the set")
	}
	if s.PersonalAllowanceCovers("alice", "net:host", "") || s.PersonalAllowanceCovers("alice", "xbin", "admin") {
		t.Error("host and xbin must stay uncovered")
	}
	// bob only has the defaults
	if p := s.Personal("bob"); strings.Join(p.NetSets, ",") != "web" || len(p.Sets) != 0 {
		t.Fatalf("bob gets exactly the defaults: %+v", p)
	}
	sets, rules := s.PersonalNet("bob")
	if strings.Join(sets, ",") != "web" || strings.Join(rules, ",") != "internet" {
		t.Fatalf("PersonalNet(bob) = %v %v", sets, rules)
	}

	// ceiling: alice's personal sets cap alice's tiles only
	mustDo(t, s.SetOwner("apps/a", "user:alice"))
	mustDo(t, s.SetOwner("apps/b", "user:bob"))
	if !s.Ceiling("apps/a").Denies(PolicyDenyIngress) {
		t.Error("a personal set's policy row must cap the owner's tile")
	}
	if s.Ceiling("apps/b").Denies(PolicyDenyIngress) {
		t.Error("…and nobody else's")
	}
	if s.Ceiling("apps/a").OwnerOrg() != "" || s.Ceiling("apps/a").HasNetSets() {
		t.Error("personal net sets are NOT a ceiling, and the ceiling stays org-only")
	}

	// term flags: a personal set's termApi reaches its user
	if !acc(t, s, "alice").TermAPI() || acc(t, s, "bob").TermAPI() {
		t.Error("termApi from alice's personal set only")
	}

	// admins: nothing personal
	mustDo(t, func() error { _, err := s.Upsert(User{ID: "root2", Role: RoleAdmin}, "password"); return err }())
	if p := s.Personal("root2"); len(p.NetSets) != 0 {
		t.Errorf("admins get no personal plane: %+v", p)
	}
}

// NoTerminal caps every level source at write — owner, org admin, org level,
// exact entry, pattern, default — and Explain agrees with TileLevel.
func TestNoTerminalCaps(t *testing.T) {
	s := personalStore(t)
	mustOrg(t, s, Org{ID: "ops", Members: []Member{{ID: "bob", Level: LevelTerminal}, {ID: "carol", Admin: true, Level: LevelRead}}})
	mustDo(t, s.SetOwner("apps/mine", "user:bob"))
	mustDo(t, s.SetOwner("apps/ops", "org:ops"))
	mustDo(t, s.SetUserTile("bob", "apps/shared", LevelTerminal))
	mustDo(t, s.SetDefaultTiles(map[string]string{"apps/pub": LevelTerminal}))
	paths := []string{"apps/mine", "apps/ops", "apps/shared", "apps/pub"}
	for _, p := range paths {
		if l := acc(t, s, "bob").TileLevel(p); l != LevelTerminal {
			t.Fatalf("before: bob on %s = %q, want terminal", p, l)
		}
	}
	if !acc(t, s, "bob").CanTerminal() {
		t.Fatal("before: bob may open terminals")
	}
	if _, err := s.SetUserPersonal("bob", PersonalPatch{NoTerminal: bptr(true)}); err != nil {
		t.Fatal(err)
	}
	a := acc(t, s, "bob")
	for _, p := range paths {
		if l := a.TileLevel(p); l != LevelWrite {
			t.Errorf("noTerminal: bob on %s = %q, want write", p, l)
		}
		if ex := a.Explain(p); len(ex) == 0 || ex[0].Level != a.TileLevel(p) {
			t.Errorf("Explain(%s) %+v disagrees with TileLevel", p, ex)
		}
	}
	if a.CanTerminal() || a.CanTerminalTile("apps/mine") || !a.CanWriteTile("apps/mine") {
		t.Error("noTerminal: no terminal anywhere, write stays")
	}
	u, _ := s.Get("bob")
	if u.CanTerminal() || u.TileLevel("apps/shared") != LevelWrite {
		t.Error("the User fallback gates cap too")
	}
	// an org admin keeps managing, just without a shell
	mustDo(t, func() error { _, err := s.SetUserPersonal("carol", PersonalPatch{NoTerminal: bptr(true)}); return err }())
	if l := acc(t, s, "carol").TileLevel("apps/ops"); l != LevelWrite {
		t.Errorf("org admin with noTerminal: %q, want write", l)
	}
}

// The personal fields survive a reload, a plain Upsert (store-owned), and
// the seed can only restrict + union.
func TestPersonalPersistenceAndSeed(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	mustDo(t, s.UpsertPermissionSet("builders", PermissionSet{Allow: []string{"gpu:*"}}))
	mustDo(t, s.UpsertNetSet("web", NetSet{Rules: []string{"internet"}}))
	mustDo(t, func() error { _, err := s.Upsert(User{ID: "dana", Role: RoleUser}, "password"); return err }())
	if _, err := s.SetUserPersonal("dana", PersonalPatch{NoPersonalTiles: bptr(true), NoTerminal: bptr(true), Sets: sptr("builders"), NetSets: sptr("web")}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUserPersonal("dana", PersonalPatch{Sets: sptr("nope")}); err == nil {
		t.Fatal("unknown permission set must be refused")
	}
	mustDo(t, s.SetPersonalDefaults(PersonalDefaults{Sets: []string{"builders"}}))
	if err := s.SetPersonalDefaults(PersonalDefaults{NetSets: []string{"nope"}}); err == nil {
		t.Fatal("unknown network set in defaults must be refused")
	}
	// a plain update (the API's PATCH path) must not clear store-owned fields
	mustDo(t, func() error { _, err := s.Upsert(User{ID: "dana", Name: "Dana", Role: RoleUser}, ""); return err }())

	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := s2.Get("dana")
	if !u.NoPersonalTiles || !u.NoTerminal || strings.Join(u.Sets, ",") != "builders" || strings.Join(u.NetSets, ",") != "web" || u.Name != "Dana" {
		t.Fatalf("personal fields lost on update/reload: %+v", u)
	}
	if d := s2.PersonalDefaults(); strings.Join(d.Sets, ",") != "builders" {
		t.Fatalf("personal defaults lost on reload: %+v", d)
	}

	// the seed: switches OR'd, sets unioned, persisted even when it's all it holds
	mustDo(t, s2.SetNewUserDefaults(NewUserDefaults{NoPersonalTiles: true, NoTerminal: true, NetSets: []string{"web"}}))
	s3, _ := Open(dir)
	if d := s3.NewUserDefaults(); !d.NoPersonalTiles || !d.NoTerminal || strings.Join(d.NetSets, ",") != "web" {
		t.Fatalf("a personal-only seed must persist: %+v", d)
	}
	nu, err := s3.Upsert(User{ID: "fresh", Role: RoleUser, Sets: []string{"builders"}}, "password")
	if err != nil {
		t.Fatal(err)
	}
	if !nu.NoPersonalTiles || !nu.NoTerminal || strings.Join(nu.Sets, ",") != "builders" || strings.Join(nu.NetSets, ",") != "web" {
		t.Fatalf("seeded row: %+v", nu)
	}
	if err := s3.SetNewUserDefaults(NewUserDefaults{Sets: []string{"nope"}}); err == nil {
		t.Fatal("unknown set in the seed must be refused")
	}
}

// A set a user, the personal defaults or the seed hold can't be deleted.
func TestPersonalSetDeleteGuards(t *testing.T) {
	s := personalStore(t)
	mustDo(t, func() error { _, err := s.SetUserPersonal("bob", PersonalPatch{Sets: sptr("builders")}); return err }())
	if err := s.DeletePermissionSet("builders"); err == nil || !strings.Contains(err.Error(), "user bob") {
		t.Fatalf("delete of a user-held set: %v", err)
	}
	mustDo(t, s.SetPersonalDefaults(PersonalDefaults{NetSets: []string{"web"}}))
	if err := s.DeleteNetSet("web"); err == nil || !strings.Contains(err.Error(), "personal defaults") {
		t.Fatalf("delete of a defaults-held net set: %v", err)
	}
	mustDo(t, s.SetNewUserDefaults(NewUserDefaults{NetSets: []string{"lab"}}))
	if err := s.DeleteNetSet("lab"); err == nil || !strings.Contains(err.Error(), "new-account") {
		t.Fatalf("delete of a seed-held net set: %v", err)
	}
	if got := strings.Join(s.SetUsers("web", true), ","); got != "personal-defaults" {
		t.Fatalf("SetUsers(web) = %s", got)
	}
	if got := strings.Join(s.SetUsers("builders", false), ","); got != "user:bob" {
		t.Fatalf("SetUsers(builders) = %s", got)
	}
}
