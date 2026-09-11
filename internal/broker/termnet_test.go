package broker

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
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

// Named sets as terminal scopes (D65): an admin sees every workspace set on
// any tile; everyone else the owning org's attached sets, only where the org
// scope exists; provider-only sets are skipped; a host rule marks the set.
func TestTermNetSets(t *testing.T) {
	b, st := netSetFixture(t, "")
	admin := auth.Principal{Owner: true}
	names := func(g term.TermNet) string {
		out := ""
		for _, s := range g.Sets {
			out += s.Name + ","
		}
		return out
	}
	if g := b.TermNetFor(admin, "apps/mine"); g.Sets != nil {
		t.Fatalf("no sets in the workspace: %+v", g.Sets)
	}
	for n, rules := range map[string][]string{
		"devs-net":  {"lan:10.0.0.0/8", "internet:*.github.com:443"},
		"infra-net": {"host", "lan:10.0.0.0/8"},
		"vpn-only":  {"provider:apps/vpn"},
	} {
		if err := st.UpsertNetSet(n, users.NetSet{Rules: rules}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	// Admin: every set (sorted), on a personal tile and on the org tile alike;
	// the provider-only set is not a scope.
	g := b.TermNetFor(admin, "apps/mine")
	if names(g) != "devs-net,infra-net," || g.OrgOK {
		t.Fatalf("admin on a personal tile: %s %+v", names(g), g)
	}
	if g := b.TermNetFor(admin, "apps/bot"); names(g) != "devs-net,infra-net," || !g.OrgOK {
		t.Fatalf("admin on the org tile: %s", names(g))
	}
	s := g.Sets[1]
	if !s.Host || s.Label != "net set: infra-net" || !strings.Contains(s.Desc, "⚠ host networking") || len(s.Rules) != 1 {
		t.Fatalf("host set: %+v", s)
	}
	if d := g.Sets[0].Desc; !strings.HasPrefix(d, "network set devs-net:") || !strings.Contains(d, "→ *.github.com:443") {
		t.Fatalf("desc: %q", d)
	}
	// bob (terminal-level on the org tile through the fixture's shares) gets
	// only the attached set there, and nothing on his personal tile.
	bob := principalFor(t, st, "bob")
	g = b.TermNetFor(bob, "apps/bot")
	if names(g) != "devs-net," || !strings.Contains(g.Sets[0].Desc, "(attached to org:sales)") {
		t.Fatalf("member on the org tile: %s %q", names(g), g.Sets[0].Desc)
	}
	if g := b.TermNetFor(bob, "apps/mine"); g.Sets != nil {
		t.Fatalf("member on a personal tile: %+v", g.Sets)
	}
	// deny net strips the member's sets with the org scope; the admin keeps theirs.
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if g := b.TermNetFor(bob, "apps/bot"); g.OrgOK || g.Sets != nil {
		t.Fatalf("deny net: %+v", g)
	}
	if g := b.TermNetFor(admin, "apps/bot"); names(g) != "devs-net,infra-net," {
		t.Fatalf("admin under deny net: %s", names(g))
	}
	// The scope id and the binding ref are spelled the same.
	if term.ScopeSetPrefix != NetRefSet {
		t.Fatalf("prefix drift: %q vs %q", term.ScopeSetPrefix, NetRefSet)
	}
}
