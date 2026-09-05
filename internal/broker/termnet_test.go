package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// TermNetFor: the terminal grant on org tiles follows the org's network
// sets (members need no termNet; internet is replaced; host only via a host
// rule); personal tiles and deny-net orgs keep the legacy rules.
func TestTermNetFor(t *testing.T) {
	b, st := netSetFixture(t, "")
	bob := principalFor(t, st, "bob")
	admin := auth.Principal{Owner: true}
	if g := b.TermNetFor(bob, "apps/bot"); g.OrgOK || g.InternetOK || g.HostOK {
		t.Fatalf("no sets, no termNet: %+v", g)
	}
	if g := b.TermNetFor(admin, "apps/bot"); g.OrgOK || !g.InternetOK || !g.HostOK {
		t.Fatalf("admin without sets: %+v", g)
	}
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8", "internet:*.github.com:443", "provider:apps/vpn"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	g := b.TermNetFor(bob, "apps/bot")
	if !g.OrgOK || g.OrgHost || g.InternetOK || g.HostOK || len(g.Rules) != 2 {
		t.Fatalf("member on an org tile: %+v", g)
	}
	if g.OrgLabel != "org network (devs-net)" || !strings.Contains(g.OrgDesc, "🖧 LAN 10.0.0.0/8") || !strings.Contains(g.OrgDesc, "→ *.github.com:443") {
		t.Fatalf("labels: %q / %q", g.OrgLabel, g.OrgDesc)
	}
	// termNet grants nothing extra on an org tile (the set replaces internet)…
	u, _ := st.Get("bob")
	u.TermNet = true
	if _, err := st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	bob = principalFor(t, st, "bob")
	if g := b.TermNetFor(bob, "apps/bot"); g.InternetOK {
		t.Fatalf("termNet must not add internet on an org tile with sets: %+v", g)
	}
	// …but still governs the member's personal tile.
	if g := b.TermNetFor(bob, "apps/mine"); g.OrgOK || !g.InternetOK || g.HostOK {
		t.Fatalf("personal tile: %+v", g)
	}
	// Admins keep every scope, with the org scope as the default.
	if g := b.TermNetFor(admin, "apps/bot"); !g.OrgOK || !g.InternetOK || !g.HostOK {
		t.Fatalf("admin on an org tile: %+v", g)
	}
	// A host rule grants host networking to members.
	if err := st.UpsertNetSet("infra-net", users.NetSet{Rules: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net", "infra-net"}); err != nil {
		t.Fatal(err)
	}
	if g := b.TermNetFor(bob, "apps/bot"); !g.OrgHost || !g.HostOK || !strings.Contains(g.OrgLabel, "host networking") {
		t.Fatalf("host rule: %+v", g)
	}
	// A provider-only set reaches nothing for terminals.
	if err := st.UpsertNetSet("vpn-only", users.NetSet{Rules: []string{"provider:apps/vpn"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"vpn-only"}); err != nil {
		t.Fatal(err)
	}
	if g := b.TermNetFor(bob, "apps/bot"); g.OrgOK {
		t.Fatalf("provider-only set: %+v", g)
	}
	// deny net beats the org scope.
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if g := b.TermNetFor(bob, "apps/bot"); g.OrgOK {
		t.Fatalf("deny net: %+v", g)
	}
}
