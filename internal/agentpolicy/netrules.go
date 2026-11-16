package agentpolicy

import (
	"encoding/json"
	"fmt"
)

// The org rules on the web and on MCP servers (D239): what an org says of
// its data in a workspace agent's conversation that holds it. Unlike the
// other rules they don't decide whether the agent reaches the org's tiles:
// they decide what a conversation that HAS read them may do next — the
// server asks them for every org in a handle's taint.
const (
	// WebAsk (the default, an unset rule): a conversation that holds the
	// org's data reads only a URL its person approved — an address can carry
	// the data out.
	WebAsk = "ask"
	// WebAllow: such a conversation reads the web freely, within the
	// workspace's policy.
	WebAllow = "allow"
	// WebNever: such a conversation doesn't read the web at all.
	WebNever = "never"

	// MCPAny (the default): such a conversation may use any MCP server the
	// workspace's policy and its person allow.
	MCPAny = "any"
	// MCPOrg: only the MCP tiles under this org.
	MCPOrg = "org"
)

// WebRule is the web rule of a layer: "" (unset: no rule of the layer's
// own), ask, allow or never.
type WebRule string

// MCPRule is the MCP rule of a layer: "" (unset) or org; "any" reads as
// unset.
type MCPRule string

func (w WebRule) rank() int {
	switch w {
	case WebAllow:
		return 1
	case WebAsk:
		return 2
	case WebNever:
		return 3
	}
	return 0
}

func (w *WebRule) UnmarshalJSON(b []byte) error {
	var v *string
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf(`web: "ask", "allow" or "never"`)
	}
	switch {
	case v == nil || *v == "":
		*w = ""
	case *v == WebAsk || *v == WebAllow || *v == WebNever:
		*w = WebRule(*v)
	default:
		return fmt.Errorf(`web: "ask", "allow" or "never", not %q`, *v)
	}
	return nil
}

func (m *MCPRule) UnmarshalJSON(b []byte) error {
	var v *string
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf(`mcp: "any" or "org"`)
	}
	switch {
	case v == nil || *v == "" || *v == MCPAny:
		*m = ""
	case *v == MCPOrg:
		*m = MCPOrg
	default:
		return fmt.Errorf(`mcp: "any" or "org", not %q`, *v)
	}
	return nil
}

// webWithin: an org's web rule is at least as strict as the bound's.
func webWithin(r, bound WebRule) error {
	if r != "" && bound != "" && r.rank() < bound.rank() {
		return fmt.Errorf("web: %s is outside what the workspace allows (%s)", r, bound)
	}
	return nil
}

func mcpWithin(r, bound MCPRule) error {
	if bound == MCPOrg && r != MCPOrg && r != "" {
		return fmt.Errorf("mcp: %s is outside what the workspace allows (org)", r)
	}
	return nil
}

// Web is the web rule that holds over layers — the strictest any of them
// sets (never, then ask, then allow), ask when none sets one — with the
// reason a person reads when it is never.
func Web(layers []Layer) (rule, reason string) {
	best := WebRule("")
	var by Layer
	for _, l := range layers {
		if l.Rules.Web.rank() > best.rank() {
			best, by = l.Rules.Web, l
		}
	}
	switch best {
	case "":
		return WebAsk, ""
	case WebNever:
		return WebNever, by.who() + " keeps its data off the web" + by.note()
	}
	return string(best), ""
}

// MCP is the MCP rule that holds over layers: org when any of them sets
// it, with the reason.
func MCP(layers []Layer) (rule, reason string) {
	for _, l := range layers {
		if l.Rules.MCP == MCPOrg {
			return MCPOrg, l.who() + " keeps its data from MCP servers outside it" + l.note()
		}
	}
	return MCPAny, ""
}
