package broker

import (
	"errors"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// covers PD-16 PD-17 PD-20 S3 — TestRouteGlobal, F5's routing
// (plans/partitions/05 §6, 02 §4 rule 2's exception): a partitioned tile's
// own frames, terminals, agent sessions and user-partition instance tokens
// reach its global instance attributed to their person — X-XBin-User, the
// person's live level and a role clamped to it (reader for read, writer for
// write or terminal), never the self-call's admin — with X-XBin-Partition
// naming user:<id> and its id; a view-as frame too (the viewed person); a
// person calling directly keeps their own role; the global instance, the
// owner token's credentials and the root token reach global as without the
// parameter; a non-primary deployment stays in its one instance. Refused:
// other tiles, cron/bus/mail deliveries, a person who can't read the tile
// or is disabled, nobody signed in; a tile without a global instance is
// ErrNoGlobalInstance (404) for everyone who could use F5. Route itself is
// unchanged, and an unpartitioned target answers as Route.
func TestRouteGlobal(t *testing.T) {
	if GlobalAddressFeature != "global-address/1" { // the wire word (06 §6): changing it is a compat change
		t.Fatalf("GlobalAddressFeature = %q", GlobalAddressFeature)
	}
	w := partRouteWS(t)
	b := w.b
	if _, err := b.Users.Upsert(users.User{ID: "wendy", Role: users.RoleUser, Tiles: map[string]string{"apps/pg": users.LevelWrite}}, "password1"); err != nil {
		t.Fatal(err)
	}
	comp := func(tile string) *registry.Component {
		c, ok := b.Reg.Component(tile)
		if !ok {
			t.Fatalf("no %s", tile)
		}
		return c
	}
	var counted []string
	withSeam(t, &partitionEdgeSeam, func(_ *Broker, id, from, to string) { counted = append(counted, from+"→"+to) })
	type want struct {
		dep, role, caller string
		attr              string // "user/level/role" of the attribution; "" = none
		id                bool   // CallerPartitionID is the caller's pkey
		deny              string // a substring of the refusal
		notFound          bool   // the refusal is ErrNoGlobalInstance
	}
	main := util.MainDeployment
	check := func(name string, p auth.Principal, target, qualifier string, wt want) {
		t.Helper()
		d := b.RouteGlobal(p, comp(target), qualifier)
		if wt.deny != "" || wt.notFound {
			switch {
			case d.Deny == nil:
				t.Errorf("%s: %+v; want a refusal", name, d)
			case !strings.Contains(d.Deny.Error(), wt.deny):
				t.Errorf("%s: %v; want a refusal saying %q", name, d.Deny, wt.deny)
			case wt.notFound != errors.Is(d.Deny, util.ErrNoGlobalInstance):
				t.Errorf("%s: %v; ErrNoGlobalInstance %v, want %v", name, d.Deny, !wt.notFound, wt.notFound)
			}
			return
		}
		if d.Deny != nil {
			t.Errorf("%s: refused: %v", name, d.Deny)
			return
		}
		attr := ""
		if a := d.Attribute; a != nil {
			attr = a.UserID + "/" + a.Level + "/" + a.Role
		}
		if d.Deployment != wt.dep || d.Role != wt.role || d.Partition != util.PartitionGlobal || string(d.CallerPartition) != wt.caller ||
			attr != wt.attr || (d.CallerPartitionID != "") != wt.id || d.Delivery != "" {
			t.Errorf("%s: %+v (attribution %q); want global and %+v", name, d, attr, wt)
		}
		if id, ok := d.CallerPartition.User(); ok && d.CallerPartitionID != w.pkeyOf(id) {
			t.Errorf("%s: partition id %q, want %s's pkey", name, d.CallerPartitionID, id)
		}
	}
	alice := want{dep: main, role: "reader", caller: "user:alice", attr: "alice/read/reader", id: true}
	// the tile's own credentials in a user partition: global, as the person
	check("alice's frame", frameOf("apps/pg", "alice"), "apps/pg", "", alice)
	check("alice's partition's instance token", instanceOf("apps/pg", "user:alice"), "apps/pg", "", alice)
	check("alice's terminal", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "terminal"}, "apps/pg", "", alice)
	check("alice's frame, the primary named", frameOf("apps/pg", "alice"), "apps/pg", main, alice)
	check("a writer's frame", frameOf("apps/pg", "wendy"), "apps/pg", "",
		want{dep: main, role: "writer", caller: "user:wendy", attr: "wendy/write/writer", id: true})
	check("an admin's frame: terminal level, never admin", frameOf("apps/pg", "bob"), "apps/pg", "",
		want{dep: main, role: "writer", caller: "user:bob", attr: "bob/terminal/writer", id: true})
	check("view-as alice's frame", auth.Principal{Component: "apps/pg", UserID: "alice", Via: "frame", Impersonator: "bob"}, "apps/pg", "", alice)
	// a person calling directly: their own role, the partition names them
	check("bob in person", personP(t, w, "bob"), "apps/pg", "", want{dep: main, role: "admin", caller: "user:bob", id: true})
	// already in global: as without the parameter
	check("the global instance", instanceOf("apps/pg", ""), "apps/pg", "", want{dep: main, role: "admin", caller: "global"})
	check("the owner token's frame", frameOf("apps/pg", ""), "apps/pg", "", want{dep: main, role: "admin", caller: "global"})
	check("the root token", auth.Principal{Owner: true, Via: "bearer"}, "apps/pg", "", want{dep: main, role: "admin", caller: "global"})
	check("--no-auth", auth.Principal{Owner: true, Via: "dev"}, "apps/pg", "", want{dep: main, role: "admin", caller: "global"})
	// a non-primary deployment's one instance is global already (PD-17)
	bobDev := auth.Principal{Component: "apps/pg", UserID: "bob", Via: "frame", Deployment: "dev", Access: personP(t, w, "bob").Access}
	check("bob's frame at dev", bobDev, "apps/pg", "", want{dep: "dev", role: "admin", caller: "global"})
	// no global instance: 404 for everyone who could use F5
	for name, p := range map[string]auth.Principal{
		"alice's frame": frameOf("apps/pu", "alice"), "alice's instance": instanceOf("apps/pu", "user:alice"),
		"the owner token's frame": frameOf("apps/pu", ""), "the root token": {Owner: true, Via: "bearer"},
		"bob in person": personP(t, w, "bob"),
	} {
		check(name+", no global", p, "apps/pu", "", want{deny: "apps/pu has no global instance", notFound: true})
	}
	// refused
	check("carol's frame, no read", frameOf("apps/pg", "carol"), "apps/pg", "", want{deny: "carol can't read apps/pg"})
	check("carol's frame, no read, no global", frameOf("apps/pu", "carol"), "apps/pu", "", want{deny: "carol can't read apps/pu"})
	check("dave's instance, disabled", instanceOf("apps/pg", "user:dave"), "apps/pg", "", want{deny: "disabled"})
	check("a deleted person's frame", frameOf("apps/pg", "erin"), "apps/pg", "", want{deny: "erin no longer exists"})
	check("another partitioned tile", instanceOf("apps/q", "user:alice"), "apps/pg", "",
		want{deny: "?xbin-partition=global addresses a tile's own global instance: apps/q can't use it on apps/pg"})
	check("an unpartitioned tile", instanceOf("apps/x", ""), "apps/pg", "", want{deny: "apps/x can't use it on apps/pg"})
	check("another tile's frame of alice", frameOf("apps/q", "alice"), "apps/pu", "", want{deny: "apps/q can't use it on apps/pu"})
	for _, p := range []auth.Principal{
		{Component: CronPrincipal, Via: "cron", Role: "writer", Partition: "user:alice"},
		{Component: CronPrincipal, Via: "cron", Role: "writer"},
		{Component: BusPrincipal, Via: "bus", Role: "reader", Partition: "user:alice"},
		{Component: partitionMailPrincipal, Via: "mail", Role: "writer", Partition: "user:alice"},
	} {
		check(describe(p), p, "apps/pg", "", want{deny: "delivery acts in the partition it was registered for: ?xbin-partition=global is for apps/pg's own"})
	}
	check("nobody signed in", auth.Principal{}, "apps/pg", "", want{deny: "not granted"})
	check("a qualifier naming another deployment", frameOf("apps/pg", "alice"), "apps/pg", "dev", want{deny: "can only call itself"})
	if len(counted) != 0 {
		t.Errorf("F5 isn't a cross-tile edge, yet the ledger counted %v", counted)
	}
	// Route is unchanged: alice's frame stays in her partition
	if d := b.Route(frameOf("apps/pg", "alice"), comp("apps/pg"), ""); d.Partition != "user:alice" || d.Attribute != nil || d.Role != "admin" {
		t.Errorf("Route for alice's frame: %+v", d)
	}
	// an unpartitioned target: Route's answer, whatever the caller
	for _, p := range []auth.Principal{frameOf("apps/x", "alice"), instanceOf("apps/x", "user:alice"), {Component: CronPrincipal, Via: "cron", Role: "writer"}} {
		got, want := b.RouteGlobal(p, comp("apps/x"), ""), b.Route(p, comp("apps/x"), "")
		if got.Partition != want.Partition || got.Role != want.Role || got.Attribute != nil || (got.Deny == nil) != (want.Deny == nil) {
			t.Errorf("%s on apps/x: RouteGlobal %+v, Route %+v", describe(p), got, want)
		}
	}
}

// covers S3 — the attribution follows the person's live level: a level
// change applies to the next call, and losing read refuses it.
func TestRouteGlobalLiveLevel(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	c, _ := b.Reg.Component("apps/pg")
	if _, err := b.Users.Upsert(users.User{ID: "wendy", Role: users.RoleUser, Tiles: map[string]string{"apps/pg": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	inst := instanceOf("apps/pg", "user:wendy")
	if d := b.RouteGlobal(inst, c, ""); d.Deny != nil || d.Attribute == nil || d.Attribute.Level != "read" || d.Role != "reader" {
		t.Fatalf("wendy at read: %+v", d)
	}
	for _, step := range []struct{ level, wantLevel, wantRole, deny string }{
		{users.LevelWrite, "write", "writer", ""},
		{users.LevelTerminal, "terminal", "writer", ""},
		{users.LevelNone, "", "", "wendy can't read apps/pg"},
	} {
		u, _ := b.Users.Get("wendy")
		u.Tiles = map[string]string{"apps/pg": step.level}
		if _, err := b.Users.Upsert(*u, ""); err != nil {
			t.Fatal(err)
		}
		d := b.RouteGlobal(inst, c, "")
		switch {
		case step.deny != "" && (d.Deny == nil || !strings.Contains(d.Deny.Error(), step.deny)):
			t.Errorf("wendy at %s: %+v, want a refusal saying %q", step.level, d, step.deny)
		case step.deny == "" && (d.Deny != nil || d.Attribute == nil || d.Attribute.Level != step.wantLevel || d.Role != step.wantRole || d.Attribute.Role != step.wantRole):
			t.Errorf("wendy at %s: %+v (%+v)", step.level, d, d.Attribute)
		}
	}
}
