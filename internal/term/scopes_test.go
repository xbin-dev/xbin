package term

import (
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// normalizeNet: the four builtins and a well-formed set: id pass; anything
// else (a typo, a path, a shell-ish name) is "" — the tile's default — so
// nothing but a charset-checked name can reach the clamp note (it is
// written into the PTY).
func TestNormalizeNet(t *testing.T) {
	for in, want := range map[string]string{
		"internet": NetInternet, "host": NetHost, "none": NetNone, "org": NetOrg,
		"set:devs-net": "set:devs-net", "set:a.b_c-9": "set:a.b_c-9",
		"":                               "",
		"bogus":                          "",
		"set:":                           "",
		"set:../x":                       "",
		"set:Devs":                       "",
		"set:a b":                        "",
		"set:\x1b[2J":                    "",
		"set:" + strings.Repeat("a", 33): "",
	} {
		if got := normalizeNet(in); got != want {
			t.Errorf("normalizeNet(%q) = %q, want %q", in, got, want)
		}
	}
}

// resolveNet turns a clamped scope into the spawn's network + its label.
func TestResolveNet(t *testing.T) {
	g := TermNet{OrgOK: true, OrgHost: false, Rules: []string{"net:10.0.0.0/8"}, OrgLabel: "org network (devs-net)",
		Sets: []NetSetScope{{Name: "lan", Rules: []string{"net:10.42.0.0/16"}, Label: "net set: lan"}, {Name: "infra", Host: true, Label: "net set: infra"}}}
	for _, tc := range []struct {
		net   string
		host  bool
		rules string
		label string
	}{
		{NetHost, true, "", "host net"},
		{NetNone, false, "", "offline"},
		{NetInternet, false, "net:internet", "internet"},
		{NetOrg, false, "net:10.0.0.0/8", "org network (devs-net)"},
		{"set:lan", false, "net:10.42.0.0/16", "net set: lan"},
		{"set:infra", true, "", "net set: infra"},
		{"set:gone", false, "net:internet", "internet"}, // never reached after the clamp; fails safe to internet-only
	} {
		host, rules, label := resolveNet(tc.net, g)
		if host != tc.host || strings.Join(rules, ",") != tc.rules || label != tc.label {
			t.Errorf("resolveNet(%s) = (%v, %v, %q), want (%v, %q, %q)", tc.net, host, rules, label, tc.host, tc.rules, tc.label)
		}
	}
	// The org scope under a host-carrying union is host networking.
	if host, _, _ := resolveNet(NetOrg, TermNet{OrgOK: true, OrgHost: true}); !host {
		t.Fatal("org with a host rule must be host networking")
	}
}

// The owner scope on a personal tile is "personal" (D88): it is the default,
// org is not honoured there (and personal isn't on an org tile), a refused
// host lands on it, and it resolves to the owner's rules.
func TestPersonalOwnerScope(t *testing.T) {
	if normalizeNet("personal") != NetPersonal {
		t.Fatal("personal must normalize")
	}
	g := TermNet{OrgOK: true, OwnerScope: NetPersonal, Rules: []string{"net:10.1.0.0/16"}, OrgLabel: "personal network (lab)", InternetOK: true}
	p := auth.Principal{UserID: "bob", Via: "session", User: &users.User{ID: "bob", Role: users.RoleUser}}
	if _, def := ScopesFor(p, g); def != NetPersonal {
		t.Fatalf("default = %q, want personal", def)
	}
	for asked, want := range map[string]string{
		NetPersonal: NetPersonal, NetOrg: NetInternet, NetHost: NetPersonal, "": NetPersonal, NetInternet: NetInternet,
	} {
		if _, got := clampTermScopes(p, false, asked, g); got != want {
			t.Errorf("asked %q on a personal tile → %q, want %q", asked, got, want)
		}
	}
	host, rules, label := resolveNet(NetPersonal, g)
	if host || strings.Join(rules, ",") != "net:10.1.0.0/16" || label != "personal network (lab)" {
		t.Fatalf("resolve personal: %v %v %q", host, rules, label)
	}
	// personal asked on an org tile → not honoured
	org := TermNet{OrgOK: true, Rules: []string{"net:internet"}}
	if _, got := clampTermScopes(p, false, NetPersonal, org); got != NetNone {
		t.Fatalf("personal on an org tile → %q, want none (no termNet)", got)
	}
	if n := clampNote(NetPersonal, NetNone, TermNet{}); !strings.Contains(n, "no personal network") {
		t.Fatalf("note: %q", n)
	}
}
