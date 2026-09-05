package users

import (
	"strings"
	"testing"
)

// Rule grammar: the net: allowance forms without the prefix, plus the
// concrete checks the relay needs.
func TestNetSetGrammar(t *testing.T) {
	good := []string{
		"internet", "host", "provider:apps/vpn", "provider:apps/*",
		"internet:api.stripe.com", "internet:api.stripe.com:443", "Internet:*.GitHub.com:443",
		"internet:203.0.113.0/24", "internet:203.0.113.7:8443", "internet:[2001:db8::]/32",
		"lan:10.0.0.0/8", "lan:10.42.0.1", "lan:10.42.0.0/16:5432", "lan:[2001:db8::]/32", "lan:[fd00::1]:22",
	}
	if err := ValidateNetRules(good); err != nil {
		t.Fatalf("good rules refused: %v", err)
	}
	bad := map[string]string{
		"":                        "empty",
		"tailscale":               "rules are",
		"net:internet":            "rules are",
		"internet:":               "rules are",
		"internet:*":              "dotted remainder",
		"internet:*.":             "dotted remainder",
		"internet:*com":           "dotted remainder",
		"internet:a*b*.com":       "single",
		"internet:10.0.0.1":       "lan: rule",
		"internet:192.168.0.0/16": "lan: rule",
		"internet:127.0.0.1":      "lan: rule",
		"internet:internet":       "plain internet",
		"internet:a.com,b.com":    "one destination",
		"internet:a.com:70000":    "port",
		"lan:*.corp":              "no globs",
		"lan:db.internal":         "addresses or CIDRs",
		"lan:10.0.0.0/99":         "CIDR",
		"provider:a b":            "tile path",
	}
	for r, want := range bad {
		err := ValidateNetRules([]string{r})
		if err == nil {
			t.Errorf("bad rule %q accepted", r)
			continue
		}
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Errorf("rule %q: error %q lacks %q", r, err, want)
		}
	}
	if err := ValidateNetRules([]string{"internet", " Internet "}); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate must be refused: %v", err)
	}
	if got := NormalizeNetRules([]string{" Internet:*.GitHub.com:443 ", "HOST"}); got[0] != "internet:*.github.com:443" || got[1] != "host" {
		t.Fatalf("normalize: %v", got)
	}
}

// Store: sets are named objects attached to orgs by reference; the org's
// reach is the union; deletion is refused while attached; the whole-org
// update path keeps the attachment; everything persists.
func TestNetSetsStore(t *testing.T) {
	s, dir := defaultsStore(t)
	if err := s.UpsertNetSet("Bad Name", NetSet{Rules: []string{"internet"}}); err == nil {
		t.Fatal("bad name accepted")
	}
	if err := s.UpsertNetSet("devs-net", NetSet{Rules: []string{"lan:bogus"}}); err == nil {
		t.Fatal("bad rule accepted")
	}
	if err := s.UpsertNetSet("devs-net", NetSet{Rules: []string{"lan:10.42.0.0/16", "internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertNetSet("infra-net", NetSet{Rules: []string{"lan:10.0.0.0/8", "host", "internet"}}); err != nil {
		t.Fatal(err)
	}
	first, _ := s.NetSet("devs-net")
	if err := s.UpsertNetSet("devs-net", NetSet{Rules: []string{"lan:10.42.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.NetSet("devs-net"); again.Created != first.Created || len(again.Rules) != 1 {
		t.Fatalf("replace must keep Created and take the new rules: %+v", again)
	}
	if err := s.SetOrgNetSets("corp", []string{"nope"}); err == nil {
		t.Fatal("unknown set attached")
	}
	if err := s.SetOrgNetSets("nope", []string{"devs-net"}); err == nil {
		t.Fatal("unknown org accepted")
	}
	if err := s.SetOrgNetSets("corp", []string{"infra-net", "devs-net", "devs-net"}); err != nil {
		t.Fatal(err)
	}
	if got := s.OrgNetSets("corp"); len(got) != 2 || got[0] != "devs-net" || got[1] != "infra-net" {
		t.Fatalf("attached: %v", got)
	}
	rules, host := s.OrgNetRules("corp")
	if !host || len(rules) != 4 || rules[0] != "host" || rules[3] != "lan:10.42.0.0/16" {
		t.Fatalf("union: %v host=%v", rules, host)
	}
	if err := s.DeleteNetSet("devs-net"); err == nil || !strings.Contains(err.Error(), "attached") {
		t.Fatalf("delete while attached: %v", err)
	}
	if got := s.NetSetAttachedTo("devs-net"); len(got) != 1 || got[0] != "corp" {
		t.Fatalf("attachedTo: %v", got)
	}
	// A whole-org update (the PATCH /orgs path) keeps the attachment.
	if _, err := s.UpsertOrg(Org{ID: "corp", Name: "Corp Inc"}); err != nil {
		t.Fatal(err)
	}
	if got := s.OrgNetSets("corp"); len(got) != 2 {
		t.Fatalf("UpsertOrg dropped net sets: %v", got)
	}
	// Persisted and reloaded.
	s2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ns, ok := s2.NetSet("infra-net"); !ok || len(ns.Rules) != 3 || ns.Created == 0 {
		t.Fatalf("reloaded set: %+v %v", ns, ok)
	}
	if got := s2.OrgNetSets("corp"); len(got) != 2 {
		t.Fatalf("reloaded attachment: %v", got)
	}
	if err := s2.SetOrgNetSets("corp", nil); err != nil {
		t.Fatal(err)
	}
	if err := s2.DeleteNetSet("devs-net"); err != nil {
		t.Fatal(err)
	}
	if err := s2.DeleteNetSet("devs-net"); err == nil {
		t.Fatal("double delete accepted")
	}
}

// Ceiling: without sets everything is covered (today's behaviour); with sets
// the union is the ceiling with the allowance's containment/glob semantics;
// CeilingFor answers for another owner; ResolvedAllow folds the rules in.
func TestCeilingNetCovers(t *testing.T) {
	s, _ := defaultsStore(t)
	if err := s.SetOwner("apps/crm", "org:corp"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOwner("apps/mine", "user:boss"); err != nil {
		t.Fatal(err)
	}
	c := s.Ceiling("apps/crm")
	if c.HasNetSets() || !c.NetCovers("net:host") || !c.NetCovers("net:internet") || c.OwnerOrg() != "corp" {
		t.Fatalf("no sets: %+v", c)
	}
	if err := s.UpsertNetSet("devs-net", NetSet{Rules: []string{"lan:10.0.0.0/8", "internet:*.github.com:443", "provider:apps/vpn*"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgNetSets("corp", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	c = s.Ceiling("apps/crm")
	if !c.HasNetSets() || c.NetHost() || len(c.NetSets()) != 1 || len(c.NetRules()) != 3 {
		t.Fatalf("with sets: %+v", c)
	}
	cases := map[string]bool{
		"net:lan:10.42.0.0/16":            true,
		"net:lan:10.1.2.3":                true,
		"net:lan:10.0.0.0/8":              true,
		"net:lan:10.0.0.0/7":              false,
		"net:lan:192.168.1.0/24":          false,
		"net:internet:api.github.com:443": true,
		"net:internet:github.com:443":     true,  // apex carve-out
		"net:internet:api.github.com":     false, // port pinned
		"net:internet:api.github.com:80":  false,
		"net:internet":                    false,
		"net:host":                        false,
		"net:provider:apps/vpn":           true,
		"net:provider:apps/vpn-eu":        true,
		"net:provider:apps/other":         false,
		"net:internet:evil.com:443":       false,
		"gpu:0":                           false,
	}
	for target, want := range cases {
		if got := c.NetCovers(target); got != want {
			t.Errorf("NetCovers(%q) = %v, want %v", target, got, want)
		}
	}
	// Host rule flips NetHost and covers net:host.
	if err := s.UpsertNetSet("infra-net", NetSet{Rules: []string{"host", "internet"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOrgNetSets("corp", []string{"devs-net", "infra-net"}); err != nil {
		t.Fatal(err)
	}
	c = s.Ceiling("apps/crm")
	if !c.NetHost() || !c.NetCovers("net:host") || !c.NetCovers("net:internet") || !c.NetCovers("net:internet:anything.example:80") {
		t.Fatalf("host/internet union: %+v", c)
	}
	// A user-owned tile has no sets; CeilingFor asks "as if org-owned".
	if u := s.Ceiling("apps/mine"); u.HasNetSets() || u.OwnerOrg() != "" {
		t.Fatalf("user tile: %+v", u)
	}
	if f := s.CeilingFor("apps/mine", "org:corp"); !f.HasNetSets() || !f.NetHost() {
		t.Fatalf("CeilingFor: %+v", f)
	}
	// ResolvedAllow folds the rules in → org admins may self-approve inside.
	if !s.AllowanceCovers("corp", "net:lan:10.42.0.0/16", "") || !s.AllowanceCovers("corp", "net:host", "") {
		t.Fatal("allowance must cover the set's reach")
	}
	if s.AllowanceCovers("corp", "net:lan:172.16.0.0/12", "") {
		t.Fatal("allowance must not exceed the sets")
	}
	if !contains(s.ResolvedAllow("corp"), "net:lan:10.0.0.0/8") {
		t.Fatalf("resolvedAllow: %v", s.ResolvedAllow("corp"))
	}
}
