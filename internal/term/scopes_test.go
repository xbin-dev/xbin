package term

import (
	"strings"
	"testing"
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
