package agentpolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

// covers D239 — an org's web and MCP rules: parsed, strictest over the
// layers, never past the bounds or a pin, and unset in a file from before
func TestNetRules(t *testing.T) {
	var old Rules
	if err := json.Unmarshal([]byte(`{"workspaceAgent":"on","harnesses":"any","models":"private"}`), &old); err != nil || old.Web != "" || old.MCP != "" {
		t.Fatalf("an org's rules of before: %+v %v", old, err)
	}
	if b, _ := json.Marshal(Rules{}); strings.Contains(string(b), "web") || strings.Contains(string(b), "mcp") {
		t.Errorf("unset rules are written: %s", b)
	}
	p, err := ParsePatch(map[string]json.RawMessage{"web": json.RawMessage(`"never"`), "mcp": json.RawMessage(`"org"`)})
	if err != nil {
		t.Fatal(err)
	}
	r := Rules{}.Apply(p)
	if r.Web != WebNever || r.MCP != MCPOrg || r.IsZero() {
		t.Errorf("applied: %+v", r)
	}
	for _, bad := range []string{`{"web":"sometimes"}`, `{"mcp":"all"}`, `{"web":1}`} {
		var m map[string]json.RawMessage
		_ = json.Unmarshal([]byte(bad), &m)
		if _, err := ParsePatch(m); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
	p, _ = ParsePatch(map[string]json.RawMessage{"web": json.RawMessage(`null`), "mcp": json.RawMessage(`"any"`)})
	if r = r.Apply(p); !r.IsZero() {
		t.Errorf("back to the defaults: %+v", r)
	}
	// strictest wins; nothing set is ask, any
	fin := Layer{ID: "fin", Name: "Finance", Rules: Rules{Web: WebNever}}
	open := Layer{ID: "ops", Name: "Ops", Rules: Rules{Web: WebAllow}}
	if rule, why := Web([]Layer{{}, open}); rule != WebAllow || why != "" {
		t.Errorf("allow alone: %s %q", rule, why)
	}
	if rule, why := Web([]Layer{{}, open, fin}); rule != WebNever || !strings.Contains(why, "Finance keeps its data off the web") {
		t.Errorf("allow and never: %s %q", rule, why)
	}
	if rule, _ := Web([]Layer{{Rules: Rules{Web: WebAsk}}, open}); rule != WebAsk {
		t.Errorf("the bounds' ask over an org's allow: %s", rule)
	}
	if rule, _ := Web(nil); rule != WebAsk {
		t.Errorf("no rule: %s", rule)
	}
	if rule, why := MCP([]Layer{{}, {ID: "fin", Name: "Finance", Rules: Rules{MCP: MCPOrg}, Pinned: true}}); rule != MCPOrg || !strings.Contains(why, "pinned") {
		t.Errorf("mcp org: %s %q", rule, why)
	}
	// an org narrows the bounds, never widens
	if err := (Rules{Web: WebAllow}).Within(Rules{Web: WebAsk}); err == nil {
		t.Error("allow within ask")
	}
	if err := (Rules{Web: WebNever}).Within(Rules{Web: WebAsk}); err != nil {
		t.Errorf("never within ask: %v", err)
	}
	if err := (Rules{}).Within(Rules{Web: WebNever, MCP: MCPOrg}); err != nil {
		t.Errorf("unset within anything: %v", err)
	}
}

// covers D240 — the MCP rule's outside-server values: listed (trusts the
// admin's list) and never (no MCP server at all); strictest wins — never
// over org over any over listed — and an org can't loosen a bound
func TestMCPRuleListedAndNever(t *testing.T) {
	for in, want := range map[string]MCPRule{`"listed"`: MCPListed, `"never"`: MCPNever, `"org"`: MCPOrg, `"any"`: "", `null`: ""} {
		var m MCPRule
		if err := json.Unmarshal([]byte(in), &m); err != nil || m != want {
			t.Errorf("%s: %q %v", in, m, err)
		}
	}
	trust := Layer{ID: "ops", Name: "Ops", Rules: Rules{MCP: MCPListed}}
	if rule, _ := MCP([]Layer{{}, trust}); rule != MCPListed {
		t.Errorf("listed alone: %s", rule)
	}
	if rule, why := MCP([]Layer{trust, {ID: "fin", Name: "Finance", Rules: Rules{MCP: MCPNever}}}); rule != MCPNever || !strings.Contains(why, "every MCP server") {
		t.Errorf("never over listed: %s %q", rule, why)
	}
	if rule, _ := MCP([]Layer{trust, {Rules: Rules{MCP: MCPOrg}}}); rule != MCPOrg {
		t.Errorf("org over listed: %s", rule)
	}
	if rule, _ := MCP(nil); rule != MCPAny {
		t.Errorf("no rule: %s", rule)
	}
	if err := (Rules{MCP: MCPListed}).Within(Rules{MCP: MCPOrg}); err == nil {
		t.Error("listed within org")
	}
	if err := (Rules{MCP: MCPOrg}).Within(Rules{MCP: MCPNever}); err == nil {
		t.Error("org within never")
	}
	if err := (Rules{MCP: MCPNever}).Within(Rules{MCP: MCPListed}); err != nil {
		t.Errorf("never within listed: %v", err)
	}
}
