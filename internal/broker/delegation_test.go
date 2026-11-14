package broker

import (
	"errors"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// delegateOf is userID's delegate principal as auth.DelegateFromRequest
// builds it: the person's, Via "delegate", Agent the sealed holder.
func delegateOf(t *testing.T, w *partWS, id, holder string) auth.Principal {
	p := personP(t, w, id)
	p.Via, p.Agent = "delegate", holder
	return p
}

// A delegate's call to a tile's API (routeDelegate): the person's reach —
// read on the tile — at a role clamped to their level, attributed to them
// and to their agent; their own partition of a partitioned tile, with
// their consent while the workspace asks; and every way it is refused.
// The fixture: alice reads every tile, bob is an admin, carol reads apps/q
// and apps/x only, dave is disabled; apps/q stands in for the sealed
// holder, apps/pg and apps/pu are partitioned, apps/x isn't.
func TestRouteDelegate(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	const holder = "apps/q"
	t.Cleanup(auth.TestSealDelegate(holder))
	comp := func(path string) *registry.Component {
		c, ok := b.Reg.Component(path)
		if !ok {
			t.Fatalf("no %s", path)
		}
		return c
	}

	type want struct {
		deny, role, level string
		part              util.Partition
	}
	check := func(name string, p auth.Principal, tile, qualifier string, wnt want) {
		t.Helper()
		d := b.Route(p, comp(tile), qualifier)
		if wnt.deny != "" {
			if d.Deny == nil || !strings.Contains(d.Deny.Error(), wnt.deny) {
				t.Errorf("%s: deny %v, want one containing %q", name, d.Deny, wnt.deny)
			}
			if d.Role != "" || d.Attribute != nil {
				t.Errorf("%s: a refused call carries role %q, attribution %+v", name, d.Role, d.Attribute)
			}
			return
		}
		if d.Deny != nil {
			t.Errorf("%s: refused: %v", name, d.Deny)
			return
		}
		a := d.Attribute
		if a == nil || a.UserID != p.UserID || a.Agent != holder || a.Role != wnt.role || a.Level != wnt.level || d.Role != wnt.role {
			t.Errorf("%s: role %q attribution %+v, want %s/%s by %s for %s", name, d.Role, a, wnt.role, wnt.level, holder, p.UserID)
		}
		if d.Deployment != util.MainDeployment || d.Partition != wnt.part {
			t.Errorf("%s: reaches %s partition %q, want main %q", name, d.Deployment, d.Partition, wnt.part)
		}
	}
	alice, bob, carol := delegateOf(t, w, "alice", holder), delegateOf(t, w, "bob", holder), delegateOf(t, w, "carol", holder)

	// the tile API read, as the person: reader for a person with read
	check("alice's agent, a reader", alice, "apps/x", "", want{role: "reader", level: "read"})
	// an admin's agent holds the admin's level on the tile — writer — never the admin role
	check("bob's agent (bob is an admin)", bob, "apps/x", "", want{role: "writer", level: "terminal"})
	// a partitioned tile: the person's own partition
	check("alice's agent, a partitioned tile", alice, "apps/pg", "", want{role: "reader", level: "read", part: "user:alice"})
	check("bob's agent, a partitioned tile", bob, "apps/pu", "", want{role: "writer", level: "terminal", part: "user:bob"})

	// refused
	check("a tile the person can't read", carol, "apps/pg", "", want{deny: "carol can't read apps/pg"})
	check("a deployment URL", alice, "apps/x", "dev", want{deny: "bare URL"})
	check("the holder itself", alice, holder, "", want{deny: "calls itself with its own credential"})
	disabled := alice
	disabled.UserID = "dave"
	check("a disabled person", disabled, "apps/x", "", want{deny: "disabled"})
	viewAs := alice
	viewAs.Impersonator = "bob"
	check("no view-as delegate exists", viewAs, "apps/x", "", want{deny: "acts for nobody"})
	asTile := alice
	asTile.Component = "apps/x"
	check("no element delegate exists", asTile, "apps/x", "", want{deny: "acts for nobody"})
	unsealed := alice
	unsealed.Agent = "apps/x" // a workspace tile naming itself an agent: not in the table
	check("a holder outside the compiled-in table", unsealed, "apps/pg", "", want{deny: "acts for nobody"})

	// the person themselves, in person, is refused as before: only their
	// frame (as the tile) or their agent (clamped) reaches a tile's API
	if d := b.Route(personP(t, w, "alice"), comp("apps/x"), ""); d.Deny == nil || !strings.Contains(d.Deny.Error(), "user:alice is not granted access") {
		t.Errorf("alice in person: %+v", d)
	}
	// and an admin in person keeps the admin role
	if d := b.Route(personP(t, w, "bob"), comp("apps/x"), ""); d.Deny != nil || d.Role != "admin" || d.Attribute != nil {
		t.Errorf("bob in person: %+v", d)
	}

	// the org's switch (plans/workspace-agent.md §5.6): filtered by the TILE
	withSeam(t, &delegateTileGate, func(_ *Broker, h, user, tile string) error {
		if tile == "apps/x" {
			return errors.New("Finance has switched the workspace agent off for its tiles")
		}
		return nil
	})
	check("the tile's org switched agents off", alice, "apps/x", "", want{deny: "Finance has switched"})
	check("another tile still answers", alice, "apps/pg", "", want{role: "reader", level: "read", part: "user:alice"})

	// consent: with the workspace asking people first, the holder is
	// another tile's code reaching the person's data, like any other
	partRouteConsent(w, true)
	withSeam(t, &partitionConsentHolds, func(_ *Broker, user, from, to string) bool { return user == "bob" && from == holder && to == "apps/pu" })
	check("no consent", alice, "apps/pg", "", want{deny: "alice hasn't let apps/q use their apps/pg data"})
	check("consent given", bob, "apps/pu", "", want{role: "writer", level: "terminal", part: "user:bob"})
	partRouteConsent(w, false)

	// the global instance, addressed explicitly: as the person, clamped
	d := b.RouteGlobal(alice, comp("apps/pg"), "")
	if d.Deny != nil || d.Partition != util.PartitionGlobal || d.Role != "reader" || d.Attribute == nil || d.Attribute.Agent != holder {
		t.Errorf("alice's agent at apps/pg's global instance: %+v (attribute %+v)", d, d.Attribute)
	}
}
