package agentpolicy

import (
	"encoding/json"
	"strings"
	"testing"
)

// covers D236 — an org's web and MCP rules: parsed, strictest over the
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
