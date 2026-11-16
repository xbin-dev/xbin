package broker

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
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
	b.AgentPolicy = delegation.OpenPolicy{}
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

	// every allowed call is counted in the person's partition's egress
	// ledger, as any partitioned tile's code's calls are; a refused one isn't
	var counted []string
	withSeam(t, &partitionEdgeSeam, func(_ *Broker, user, from, to string) { counted = append(counted, user+" "+from+"→"+to) })
	defer func() {
		want := "alice apps/q→apps/x,bob apps/q→apps/x,alice apps/q→apps/pg,bob apps/q→apps/pu"
		if got := strings.Join(counted[:min(4, len(counted))], ","); got != want {
			t.Errorf("the ledger counted %s, want first %s", got, want)
		}
		for _, c := range counted {
			if strings.HasPrefix(c, "carol") || strings.HasPrefix(c, "dave") {
				t.Errorf("a refused call was counted: %s", c)
			}
		}
	}()

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

	// the org's rules (the agent-policy package's answer): filtered by the
	// TILE, on the model the agent says it is on
	var asked []delegation.Subject
	b.AgentPolicy = policyFunc(func(s delegation.Subject) error {
		asked = append(asked, s)
		if s.Tile == "apps/x" && s.Model != "private-1" {
			return errors.New("Finance allows private models only")
		}
		return nil
	})
	check("the tile's org refuses the model", alice, "apps/x", "", want{deny: "Finance allows private models only"})
	onPrivate := alice
	onPrivate.AgentModel = "private-1"
	check("…and allows a private one", onPrivate, "apps/x", "", want{role: "reader", level: "read"})
	check("another tile still answers", alice, "apps/pg", "", want{role: "reader", level: "read", part: "user:alice"})
	if n := len(asked); n < 3 || asked[n-1] != (delegation.Subject{Holder: holder, User: "alice", Tile: "apps/pg"}) {
		t.Errorf("the policy was asked %+v", asked)
	}
	b.AgentPolicy = nil // no policy installed: nothing is reached
	check("no agent policy", alice, "apps/pg", "", want{deny: "rules aren't set up"})
	b.AgentPolicy = delegation.OpenPolicy{}

	// consent: with the workspace asking people first, the holder is
	// another tile's code reaching the person's data, like any other
	partRouteConsent(w, true)
	withSeam(t, &partitionConsentHolds, func(_ *Broker, user, from, to string) bool { return user == "bob" && from == holder && to == "apps/pu" })
	check("no consent", alice, "apps/pg", "", want{deny: "alice hasn't let apps/q use their apps/pg data"})
	check("consent given", bob, "apps/pu", "", want{role: "writer", level: "terminal", part: "user:bob"})
	partRouteConsent(w, false)

	// the global instance, addressed explicitly: as the person, clamped —
	// and counted in their ledger, as what left the agent for that tile
	var edges []string
	withSeam(t, &partitionEdgeSeam, func(_ *Broker, user, from, to string) { edges = append(edges, user+" "+from+"→"+to) })
	d := b.RouteGlobal(alice, comp("apps/pg"), "")
	if d.Deny != nil || d.Partition != util.PartitionGlobal || d.Role != "reader" || d.Attribute == nil || d.Attribute.Agent != holder {
		t.Errorf("alice's agent at apps/pg's global instance: %+v (attribute %+v)", d, d.Attribute)
	}
	if want := []string{"alice " + holder + "→apps/pg+main"}; !slices.Equal(edges, want) {
		t.Errorf("the ledger after a call to the global instance: %v, want %v", edges, want)
	}
	// ?xbin-partition=global is no way round the person's consent (the
	// security review's finding 4: it skipped both the consent check and
	// the ledger)
	partRouteConsent(w, true)
	edges = nil
	d = b.RouteGlobal(alice, comp("apps/pg"), "")
	if d.Deny == nil || !strings.Contains(d.Deny.Error(), "alice hasn't let apps/q use their apps/pg data") || len(edges) != 0 {
		t.Errorf("alice's agent at apps/pg's global instance, without her consent: %+v, ledger %v", d, edges)
	}
	if d := b.Route(alice, comp("apps/pg"), ""); d.Deny == nil {
		t.Errorf("…and at her own partition: %+v", d)
	}
	withSeam(t, &partitionConsentHolds, func(_ *Broker, user, from, to string) bool {
		return user == "alice" && from == holder && to == "apps/pg"
	})
	d = b.RouteGlobal(alice, comp("apps/pg"), "")
	if d.Deny != nil || d.Partition != util.PartitionGlobal || len(edges) != 1 {
		t.Errorf("…with her consent: %+v, ledger %v", d, edges)
	}
	// a person in person is not their agent: no consent asked of them
	if d := b.RouteGlobal(personP(t, w, "bob"), comp("apps/pg"), ""); d.Deny != nil || len(edges) != 1 {
		t.Errorf("bob in person at the global instance: %+v, ledger %v", d, edges)
	}
	partRouteConsent(w, false)
}

// policyFunc is a delegation.Policy from one function (tile and session
// rules alike; nothing starts unasked).
type policyFunc func(delegation.Subject) error

func (f policyFunc) TileAllows(s delegation.Subject) error              { return f(s) }
func (f policyFunc) SessionAllows(s delegation.Subject, _ string) error { return f(s) }
func (policyFunc) StartPrefs(string, string, string) (delegation.StartPrefs, bool) {
	return delegation.StartPrefs{}, false
}

// A tile's manifest says how much of its API a person's agent may use
// ("agent": off | read | full; absent is full, an unknown word off), read
// from the code the tile's primary runs.
func TestDelegateManifestKnob(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	const holder = "apps/q"
	t.Cleanup(auth.TestSealDelegate(holder))
	b.AgentPolicy = delegation.OpenPolicy{}
	alice := delegateOf(t, w, "alice", holder)
	for _, c := range []struct {
		manifest          string
		read, write, only bool
	}{
		{`{"runtime":"go"}`, true, true, false},
		{`{"runtime":"go","agent":"full"}`, true, true, false},
		{`{"runtime":"go","agent":"read"}`, true, false, true},
		{`{"runtime":"go","agent":"off"}`, false, false, false},
		{`{"runtime":"go","agent":"everything"}`, false, false, false}, // a word this xbind doesn't know
	} {
		w.write(map[string]string{"apps/x/xbin.json": c.manifest})
		w.rescan()
		_, _, only, rerr := b.DelegateTile(alice, "apps/x", false)
		_, _, _, werr := b.DelegateTile(alice, "apps/x", true)
		if (rerr == nil) != c.read || (werr == nil) != c.write || only != c.only {
			t.Errorf("%s: read %v write %v readOnly %v", c.manifest, rerr, werr, only)
		}
		comp, _ := b.Reg.Component("apps/x")
		d := b.Route(alice, comp, "")
		if (d.Deny == nil) != c.read || d.Deny == nil && d.Attribute.ReadOnly != c.only {
			t.Errorf("%s: Route %+v (attribute %+v)", c.manifest, d, d.Attribute)
		}
	}
}

// Nothing reaches a sealed holder but its own principals and people: no
// tile's call — whatever xbin.json grants — and no new grant or binding.
func TestSealedHolderIsNoTarget(t *testing.T) {
	w := partRouteWS(t) // the fixture grants apps/q → apps/x, and nothing on apps/x itself
	b := w.b
	t.Cleanup(auth.TestSealDelegate("apps/pg"))
	comp, _ := b.Reg.Component("apps/pg")
	// apps/x and apps/q hold approved grants on apps/pg: refused all the same
	for _, p := range []auth.Principal{instanceOf("apps/x", ""), instanceOf("apps/q", "user:alice"), frameOf("apps/x", "alice")} {
		if d := b.Route(p, comp, ""); d.Deny == nil || !strings.Contains(d.Deny.Error(), "is a system component that acts for people") || strings.Contains(d.Deny.Error(), "sealed") {
			t.Errorf("%s reached the holder: %+v", p.Component, d)
		}
	}
	// its own frame and an admin in person still do
	if d := b.Route(frameOf("apps/pg", "alice"), comp, ""); d.Deny != nil {
		t.Errorf("the holder's own frame: %v", d.Deny)
	}
	if d := b.Route(personP(t, w, "bob"), comp, ""); d.Deny != nil {
		t.Errorf("an admin in person: %v", d.Deny)
	}
	// another holder's delegate doesn't either
	t.Cleanup(auth.TestSealDelegate("apps/q"))
	b.AgentPolicy = delegation.OpenPolicy{}
	if d := b.Route(delegateOf(t, w, "alice", "apps/q"), comp, ""); d.Deny == nil || !strings.Contains(d.Deny.Error(), "is a system component") || strings.Contains(d.Deny.Error(), "sealed") {
		t.Errorf("a delegate reached another holder: %+v", d)
	}
	if holderRefusal("apps/x", "apps/pg") == "" || holderRefusal("apps/pg", "apps/pg") != "" || holderRefusal("", "apps/pg") != "" || holderRefusal("apps/x", "apps/pu") != "" {
		t.Error("holderRefusal")
	}
	// a person is asked for the holder's edges like any approved edge's
	if !b.edgeGranted("apps/q", "apps/pu") || b.edgeGranted("apps/q", "apps/q") {
		t.Error("edgeGranted for a holder")
	}
}
