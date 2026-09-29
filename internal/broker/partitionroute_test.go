package broker

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// partRouteWS is the routing fixture: apps/pg (user + global) and apps/pu
// (user only), recorded at once (no data); apps/q, a partitioned caller;
// apps/x, an unpartitioned one; users/alice/mcp, alice's personal tile.
// People: alice reads everything, bob is an admin, carol reads only
// apps/q and apps/x, dave (disabled) reads everything. A second
// deployment, dev, exists for every tile.
func partRouteWS(t *testing.T) *partWS {
	t.Helper()
	grants := `[{"from":"apps/x","target":"apps/pg","role":"reader"},{"from":"apps/x","target":"apps/pu","role":"reader"},
		{"from":"apps/q","target":"apps/pg","role":"reader"},{"from":"apps/q","target":"apps/pu","role":"reader"},
		{"from":"apps/q","target":"apps/x","role":"reader"}]`
	w := newPartWS(t, map[string]string{
		"xbin.json":                  `{"schema":1,"grants":` + grants + `}`,
		"apps/pg/xbin.json":          `{"runtime":"go","partition":["user","global"]}`,
		"apps/pu/xbin.json":          `{"runtime":"go","partition":["user"]}`,
		"apps/q/xbin.json":           `{"runtime":"go","partition":["user"],"uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"reader"},{"target":"apps/x","role":"reader"}]}`,
		"apps/x/xbin.json":           `{"runtime":"go","uses":[{"target":"apps/pg","role":"reader"},{"target":"apps/pu","role":"reader"}]}`,
		"users/alice/mcp/xbin.json":  `{"runtime":"go"}`,
		"apps/pg/backend/main.go":    "package main\n",
		"apps/pu/backend/main.go":    "package main\n",
		"apps/q/backend/main.go":     "package main\n",
		"apps/x/backend/main.go":     "package main\n",
		"users/alice/mcp/backend/.k": "",
	}, nil)
	w.rescan() // the store is installed: auto-record the partitioned tiles
	for _, tile := range []string{"apps/pg", "apps/pu", "apps/q"} {
		if st, _, _ := w.state(tile); st != registry.PartitionPartitioned {
			t.Fatalf("%s: %v, want partitioned", tile, st)
		}
	}
	st, err := users.Open(filepath.Join(w.root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]string{"apps/*": users.LevelRead, "users/*": users.LevelRead}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleUser, Tiles: all},
		{ID: "bob", Role: users.RoleAdmin},
		{ID: "carol", Role: users.RoleUser, Tiles: map[string]string{"apps/q": users.LevelRead, "apps/x": users.LevelRead}},
		{ID: "dave", Role: users.RoleUser, Tiles: all},
	} {
		if _, err := st.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	d, _ := st.Get("dave")
	d.Disabled = true
	if _, err := st.Upsert(*d, ""); err != nil {
		t.Fatal(err)
	}
	w.b.Users = st
	w.b.PrimaryOf = func(string) string { return util.MainDeployment }
	w.b.DeploymentExists = func(_, name string) bool { return name == util.MainDeployment || name == "dev" }
	w.b.AddressedDeployment = func(p auth.Principal, tile string) (string, error) {
		if p.Component == tile && p.Deployment != "" {
			return p.Deployment, nil
		}
		return util.MainDeployment, nil
	}
	return w
}

// partRouteConsent writes the workspace policy partitionConsent (a plain
// function, not a partWS method: F4's fixture has its own).
func partRouteConsent(w *partWS, on bool) {
	w.t.Helper()
	body := fmt.Sprintf(`{"schema":1,"partitionConsent":%v,"credentialResetConfirm":false}`, on)
	p := filepath.Join(w.root, "data", "workspace-policies.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		w.t.Fatal(err)
	}
	later := time.Now().Add(time.Duration(len(body)) * time.Second)
	_ = os.Chtimes(p, later, later) // the policies cache keys on size and mtime
	if got := w.b.Policies().PartitionConsent; got != on {
		w.t.Fatalf("the policy reads %v after writing %v", got, on)
	}
}

// withSeam sets a package seam for one test.
func withSeam[T any](t *testing.T, seam *T, v T) {
	was := *seam
	*seam = v
	t.Cleanup(func() { *seam = was })
}

// pkeyOf is the partition id xbind gives id's person now.
func (w *partWS) pkeyOf(id string) string {
	w.t.Helper()
	u, ok := w.b.Users.Get(id)
	if !ok || u.UID == "" {
		w.t.Fatalf("%s has no uid", id)
	}
	return util.PartitionKey(id, u.UID)
}

func frameOf(tile, user string) auth.Principal {
	return auth.Principal{Component: tile, UserID: user, Via: "frame"}
}

func instanceOf(tile string, part util.Partition) auth.Principal {
	return auth.Principal{Component: tile, Via: "instance", Partition: part}
}

func personP(t *testing.T, w *partWS, id string) auth.Principal {
	u, ok := w.b.Users.Get(id)
	if !ok {
		t.Fatalf("no %s", id)
	}
	acc, _ := w.b.Users.Access(id)
	return auth.Principal{UserID: id, User: u, Access: acc, Via: "session"}
}

// covers PD-08 PD-10 PD-11 PD-12 PD-20 — TestAddressedPartition, the whole
// table of plans/partitions/02 §3: deliveries by their registration, a
// tile's instance by its registration, its frames, tickets, terminals and
// agent sessions by their person (live, reading the tile), the root
// token's by global or nothing, view-as never, people (admins too) their
// own, other tiles by the edge matrix, a non-primary deployment global,
// and every principal of an unpartitioned tile "" — while a person's
// partition credential never reaches a tile that is no longer partitioned.
func TestAddressedPartition(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	alice, bob, carol := personP(t, w, "alice"), personP(t, w, "bob"), personP(t, w, "carol")
	cron := func(part util.Partition, dep string) auth.Principal {
		return auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer", Deployment: dep, Partition: part}
	}
	for _, c := range []struct {
		name, tile string
		p          auth.Principal
		want       util.Partition
		refused    string // a substring of the refusal; "" = allowed
	}{
		// deliveries: the registration's partition, live
		{"alice's cron", "apps/pg", cron("user:alice", ""), "user:alice", ""},
		{"global's cron", "apps/pg", cron("", ""), "global", ""},
		{"a cron on a tile without global", "apps/pu", cron("", ""), "", "has no global instance"},
		{"carol's cron, she can't read it", "apps/pg", cron("user:carol", ""), "", "carol can't read apps/pg"},
		{"dave's cron, disabled", "apps/pg", cron("user:dave", ""), "", "disabled"},
		{"erin's cron, deleted", "apps/pg", cron("user:erin", ""), "", "erin no longer exists"},
		{"a bus delivery", "apps/pg", auth.Principal{Component: BusPrincipal, Via: "bus", Partition: "user:alice"}, "user:alice", ""},
		{"a mail doorbell", "apps/pg", auth.Principal{Component: partitionMailPrincipal, Via: "mail", Partition: "user:alice"}, "user:alice", ""},
		{"dev's cron", "apps/pg", cron("", "dev"), "global", ""},
		// the tile's instance tokens
		{"alice's instance", "apps/pg", instanceOf("apps/pg", "user:alice"), "user:alice", ""},
		{"the global instance", "apps/pg", instanceOf("apps/pg", ""), "global", ""},
		{"an instance of no global", "apps/pu", instanceOf("apps/pu", ""), "", "has no global instance"},
		{"dave's instance", "apps/pg", instanceOf("apps/pg", "user:dave"), "", "disabled"},
		// frames, path tickets, terminals, agent sessions
		{"alice's frame", "apps/pg", frameOf("apps/pg", "alice"), "user:alice", ""},
		{"alice's terminal", "apps/pu", auth.Principal{Component: "apps/pu", UserID: "alice", Via: "terminal"}, "user:alice", ""},
		{"carol's frame, no read", "apps/pg", frameOf("apps/pg", "carol"), "", "carol can't read apps/pg"},
		{"a deleted person's frame", "apps/pg", frameOf("apps/pg", "erin"), "", "erin no longer exists"},
		{"the owner's frame", "apps/pg", frameOf("apps/pg", ""), "global", ""},
		{"the owner's frame, no global", "apps/pu", frameOf("apps/pu", ""), "", "sign in as a person: apps/pu keeps each person's data apart"},
		{"the owner's terminal, no global", "apps/pu", auth.Principal{Component: "apps/pu", Via: "terminal"}, "", "sign in as a person"},
		{"view-as alice", "apps/pg", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame", Impersonator: "bob"}, "",
			"apps/pg keeps alice's data private: view-as can't open it"},
		{"a view-as session", "apps/pg", auth.Principal{UserID: "alice", Via: "session", Impersonator: "owner"}, "", "view-as can't open it"},
		{"dev's frame", "apps/pg", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame", Deployment: "dev"}, "global", ""},
		// people and the root token
		{"alice in person", "apps/pg", alice, "user:alice", ""},
		{"an admin: their own", "apps/pu", bob, "user:bob", ""},
		{"carol, no read", "apps/pg", carol, "", "carol can't read apps/pg"},
		{"the root token", "apps/pg", auth.Principal{Owner: true, Via: "bearer"}, "global", ""},
		{"the root token, no global", "apps/pu", auth.Principal{Owner: true, Via: "bearer"}, "", "sign in as a person"},
		{"--no-auth", "apps/pg", auth.Principal{Owner: true, Via: "dev"}, "global", ""},
		{"an anonymous subrequest", "apps/pg", auth.Principal{}, "", "sign in as a person: apps/pg"},
		{"a person-less session", "apps/pg", auth.Principal{Via: "session"}, "", "sign in as a person: apps/pg"},
		// other tiles: the edge matrix (05 §1)
		{"apps/q as alice", "apps/pg", instanceOf("apps/q", "user:alice"), "user:alice", ""},
		{"apps/q's frame of alice", "apps/pu", frameOf("apps/q", "alice"), "user:alice", ""},
		{"apps/q as carol, no read on pg", "apps/pg", frameOf("apps/q", "carol"), "", "carol can't read apps/pg"},
		{"an unpartitioned tile", "apps/pg", instanceOf("apps/x", ""), "global", ""},
		{"an unpartitioned tile, no global", "apps/pu", frameOf("apps/x", "alice"), "", "apps/pu is partitioned: only partitioned tiles reach its people's data, and it has no global instance"},
		{"apps/q's global-less owner frame", "apps/pg", frameOf("apps/q", ""), "", "sign in as a person: apps/q"},
		// an unpartitioned target: "" for everyone
		{"alice on apps/x", "apps/x", alice, "", ""},
		{"apps/x's instance", "apps/x", instanceOf("apps/x", ""), "", ""},
		{"apps/q's alice on apps/x", "apps/x", instanceOf("apps/q", "user:alice"), "", ""},
		{"view-as on apps/x", "apps/x", auth.Principal{UserID: "alice", Via: "session", Impersonator: "owner"}, "", ""},
		{"the root token on apps/x", "apps/x", auth.Principal{Owner: true, Via: "bearer"}, "", ""},
		// a person's partition credential on a tile no longer partitioned
		{"a stale partition token", "apps/x", instanceOf("apps/x", "user:alice"), "", "no longer keeps each person's data apart"},
		{"a stale partition's cron", "apps/x", cron("user:alice", ""), "", "no longer keeps each person's data apart"},
	} {
		got, err := b.addressedPartition(c.p, c.tile)
		switch {
		case c.refused == "" && (err != nil || got != c.want):
			t.Errorf("%s: addressedPartition(%s) = %q, %v; want %q", c.name, c.tile, got, err, c.want)
		case c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused) || got != ""):
			t.Errorf("%s: addressedPartition(%s) = %q, %v; want a refusal saying %q", c.name, c.tile, got, err, c.refused)
		}
	}
	for _, p := range []auth.Principal{instanceOf("apps/q", "user:alice"), frameOf("apps/x", "alice"), alice, bob} {
		if got, err := b.callerPartition(p); p.Component != "" && p.Component != "apps/x" && got != "user:alice" || err != nil ||
			(p.Component == "" || p.Component == "apps/x") && got != "" {
			t.Errorf("callerPartition(%s) = %q, %v", describe(p), got, err)
		}
	}
	// the consent policy (PD-13): with it on, another partitioned tile
	// reaches alice's partition only with her consent (F10's records)
	partRouteConsent(w, true)
	if _, err := b.addressedPartition(instanceOf("apps/q", "user:alice"), "apps/pg"); err == nil ||
		err.Error() != "alice hasn't let apps/q use their apps/pg data" {
		t.Errorf("consent policy on, no consent: %v", err)
	}
	withSeam(t, &partitionConsentHolds, func(_ *Broker, id, from, to string) bool {
		return id == "alice" && from == "apps/q" && to == "apps/pg"
	})
	if got, err := b.addressedPartition(instanceOf("apps/q", "user:alice"), "apps/pg"); got != "user:alice" || err != nil {
		t.Errorf("consent policy on, consented: %q, %v", got, err)
	}
	if got, err := b.addressedPartition(frameOf("apps/pg", "alice"), "apps/pg"); got != "user:alice" || err != nil {
		t.Errorf("the consent policy touched the tile's own frame: %q, %v", got, err)
	}
}

// covers PD-12 PD-13 PD-16 PD-20 PD-17 — TestRoutePartitions: Route's rules
// 1-4 against partitioned and unpartitioned targets, with and without a
// global instance, and the consent policy off, on without and on with
// consent; plus a personal bind from its owner's partition, another
// partition and global. Deliveries are background; the caller's partition
// and its id ride along, including to an unpartitioned target; the id is
// the person's pkey, minted at their first partition; allowed cross-tile
// partition calls are counted; and nobody whose partition can't be told
// reaches anything.
func TestRoutePartitions(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	comp := func(tile string) *registry.Component {
		c, ok := b.Reg.Component(tile)
		if !ok {
			t.Fatalf("no %s", tile)
		}
		return c
	}
	if u, _ := b.Users.Get("carol"); u.UID != "" {
		t.Fatal("carol has a uid before any partition")
	}
	var counted []string
	withSeam(t, &partitionEdgeCounted, func(_ *Broker, id, from, to string) {
		counted = append(counted, from+"/user:"+id+"→"+to)
	})
	type want struct {
		dep, role, part, caller string
		id                      bool   // CallerPartitionID set (and the person's pkey)
		delivery                string // Delivery
		deny                    string // a substring of the refusal
	}
	check := func(name string, p auth.Principal, target, qualifier string, wt want) {
		t.Helper()
		d := b.Route(p, comp(target), qualifier)
		if wt.deny != "" {
			if d.Deny == nil || !strings.Contains(d.Deny.Error(), wt.deny) {
				t.Errorf("%s: %+v; want a refusal saying %q", name, d, wt.deny)
			}
			return
		}
		if d.Deny != nil {
			t.Errorf("%s: refused: %v", name, d.Deny)
			return
		}
		gotID := d.CallerPartitionID != ""
		if d.Deployment != wt.dep || d.Role != wt.role || string(d.Partition) != wt.part || string(d.CallerPartition) != wt.caller ||
			gotID != wt.id || d.Delivery != wt.delivery {
			t.Errorf("%s: %+v; want %+v", name, d, wt)
		}
		if id, ok := d.CallerPartition.User(); ok && d.CallerPartitionID != w.pkeyOf(id) {
			t.Errorf("%s: partition id %q, want %s's pkey", name, d.CallerPartitionID, id)
		}
	}
	main := util.MainDeployment
	cron := auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer", Partition: "user:alice"}
	// rule 1: deliveries
	check("alice's cron", cron, "apps/pg", "", want{dep: main, role: "writer", part: "user:alice", caller: "user:alice", id: true, delivery: "cron"})
	check("global's cron", auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}, "apps/pg", "",
		want{dep: main, role: "writer", part: "global", caller: "global", delivery: "cron"})
	check("alice's bus delivery", auth.Principal{Component: BusPrincipal, Via: "bus", Role: "reader", Partition: "user:alice"}, "apps/pg", "",
		want{dep: main, role: "reader", part: "user:alice", caller: "user:alice", id: true, delivery: "bus"})
	check("alice's mail doorbell", auth.Principal{Component: partitionMailPrincipal, Via: "mail", Role: "writer", Partition: "user:alice"}, "apps/pg", "",
		want{dep: main, role: "writer", part: "user:alice", caller: "user:alice", id: true, delivery: "mail"})
	check("a cron without global", auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}, "apps/pu", "",
		want{deny: "has no global instance"})
	check("a disabled person's bus delivery", auth.Principal{Component: BusPrincipal, Via: "bus", Role: "reader", Partition: "user:dave"},
		"apps/pg", "", want{deny: "disabled"})
	check("an unpartitioned tile's cron", auth.Principal{Component: CronPrincipal, Via: "cron", Role: "writer"}, "apps/x", "",
		want{dep: main, role: "writer"})
	// rule 2: the tile's own principals stay in their partition
	check("alice's instance, self", instanceOf("apps/pg", "user:alice"), "apps/pg", "",
		want{dep: main, role: "admin", part: "user:alice", caller: "user:alice", id: true})
	check("alice's frame, self", frameOf("apps/pu", "alice"), "apps/pu", "", want{dep: main, role: "admin", part: "user:alice", caller: "user:alice", id: true})
	check("the global instance, self", instanceOf("apps/pg", ""), "apps/pg", "", want{dep: main, role: "admin", part: "global", caller: "global"})
	check("dev's frame, self", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame", Deployment: "dev", Access: personP(t, w, "alice").Access},
		"apps/pg", "", want{deny: "deployments of apps/pg need write access"})
	check("a view-as frame", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame", Impersonator: "bob"}, "apps/pg", "",
		want{deny: "view-as can't open it"})
	// rule 3: admins reach their own partition; the root token global
	check("bob, an admin", personP(t, w, "bob"), "apps/pu", "", want{dep: main, role: "admin", part: "user:bob", caller: "user:bob", id: true})
	check("bob at dev", personP(t, w, "bob"), "apps/pg", "dev", want{dep: "dev", role: "admin", part: "global", caller: "global"})
	check("the root token", auth.Principal{Owner: true, Via: "bearer"}, "apps/pg", "", want{dep: main, role: "admin", part: "global", caller: "global"})
	check("the root token, no global", auth.Principal{Owner: true, Via: "bearer"}, "apps/pu", "",
		want{deny: "sign in as a person: apps/pu keeps each person's data apart"})
	check("the root token on apps/x", auth.Principal{Owner: true, Via: "bearer"}, "apps/x", "", want{dep: main, role: "admin"})
	// rule 4: the edge matrix, the consent policy off
	check("apps/q as alice → pg", instanceOf("apps/q", "user:alice"), "apps/pg", "",
		want{dep: main, role: "reader", part: "user:alice", caller: "user:alice", id: true})
	check("apps/q as carol → pg", frameOf("apps/q", "carol"), "apps/pg", "", want{deny: "carol can't read apps/pg"})
	check("apps/q's instance of no global → pg", instanceOf("apps/q", ""), "apps/pg", "", want{deny: "apps/q has no global instance"})
	check("apps/x → pg", instanceOf("apps/x", ""), "apps/pg", "", want{dep: main, role: "reader", part: "global"})
	check("apps/x → pu", instanceOf("apps/x", ""), "apps/pu", "", want{deny: "it has no global instance"})
	check("apps/q as alice → apps/x", instanceOf("apps/q", "user:alice"), "apps/x", "",
		want{dep: main, role: "reader", caller: "user:alice", id: true})
	check("apps/q as alice, no grant", instanceOf("apps/q", "user:alice"), "users/alice/mcp", "",
		want{deny: "apps/q is not granted access to users/alice/mcp"})
	check("apps/x, not granted", instanceOf("apps/pg", "user:alice"), "apps/x", "", want{deny: "apps/pg is not granted access to apps/x"})
	if u, _ := b.Users.Get("carol"); u.UID != "" {
		t.Error("carol got a uid though no partition of hers was ever reached")
	}
	if strings.Join(counted, ",") != "apps/q/user:alice→apps/pg,apps/q/user:alice→apps/x" {
		t.Errorf("the ledger counted %v", counted)
	}
	// the consent policy on: without consent refused, with it allowed
	partRouteConsent(w, true)
	check("consent on, none", instanceOf("apps/q", "user:alice"), "apps/pg", "", want{deny: "alice hasn't let apps/q use their apps/pg data"})
	check("consent on, apps/x → pg", instanceOf("apps/x", ""), "apps/pg", "", want{dep: main, role: "reader", part: "global"})
	withSeam(t, &partitionConsentHolds, func(_ *Broker, id, from, to string) bool { return id == "alice" && from == "apps/q" })
	check("consent on, consented", instanceOf("apps/q", "user:alice"), "apps/pg", "",
		want{dep: main, role: "reader", part: "user:alice", caller: "user:alice", id: true})
	partRouteConsent(w, false)
	// a personal bind (F15's seam): only its owner's partition
	withSeam(t, &personalBindGrant, func(_ *Broker, caller string, part util.Partition, target string) (string, bool) {
		return "reader", target == "users/alice/mcp" && part == "user:alice"
	})
	check("alice's personal bind", instanceOf("apps/q", "user:alice"), "users/alice/mcp", "",
		want{dep: main, role: "reader", caller: "user:alice", id: true})
	check("bob's partition, alice's bind", frameOf("apps/q", "bob"), "users/alice/mcp", "",
		want{deny: "apps/q is not granted access to users/alice/mcp"})
	check("the root's frame, alice's bind", frameOf("apps/q", ""), "users/alice/mcp", "", want{deny: "sign in as a person"})
	// a stale partition credential never reaches main
	check("a stale partition token", instanceOf("apps/x", "user:alice"), "apps/x", "", want{deny: "no longer keeps each person's data apart"})
	// a users store that can't be written: no partition id, no call
	if os.Geteuid() != 0 {
		if err := os.Chmod(filepath.Join(w.root, "data"), 0o500); err != nil {
			t.Fatal(err)
		}
		d := b.Route(frameOf("apps/q", "carol"), comp("apps/x"), "")
		_ = os.Chmod(filepath.Join(w.root, "data"), 0o700)
		if d.Deny == nil || !strings.Contains(d.Deny.Error(), "carol's partition identity can't be recorded") {
			t.Errorf("carol's first partition with a read-only users store: %+v", d)
		}
	}
}
