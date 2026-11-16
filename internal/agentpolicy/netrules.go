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

	// MCPAny (the default): such a conversation may use any MCP tile the
	// workspace's policy and its person allow; an outside server (D240) only
	// with the person's approval of each call — what it is sent leaves the
	// workspace — unless the server is listed with this org.
	MCPAny = "any"
	// MCPListed: as any, and the outside servers an admin listed are trusted
	// with the org's data: their calls go by the ordinary approvals. The one
	// value that loosens the default, as WebAllow does.
	MCPListed = "listed"
	// MCPOrg: only the MCP servers under this org — its MCP tiles, and the
	// outside servers listed with it.
	MCPOrg = "org"
	// MCPNever: no MCP server at all.
	MCPNever = "never"
)

// WebRule is the web rule of a layer: "" (unset: no rule of the layer's
// own), ask, allow or never.
type WebRule string

// MCPRule is the MCP rule of a layer: "" (unset: no rule of its own),
// any, listed, org or never. "any" said explicitly is kept (D240 review):
// a bound or a pin of any holds an org at any, where unset would let its
// own listed through; null is back to unset.
type MCPRule string

func (m MCPRule) rank() int {
	switch m {
	case MCPListed:
		return 1
	case MCPAny:
		return 2
	case MCPOrg:
		return 3
	case MCPNever:
		return 4
	}
	return 0
}

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
		return fmt.Errorf(`mcp: "any", "listed", "org" or "never"`)
	}
	switch {
	case v == nil || *v == "":
		*m = ""
	case *v == MCPAny || *v == MCPListed || *v == MCPOrg || *v == MCPNever:
		*m = MCPRule(*v)
	default:
		return fmt.Errorf(`mcp: "any", "listed", "org" or "never", not %q`, *v)
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
	if r != "" && bound != "" && r.rank() < bound.rank() {
		return fmt.Errorf("mcp: %s is outside what the workspace allows (%s)", r, bound)
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

// MCP is the MCP rule that holds over layers — the strictest any of them
// sets (never, then org, then any, then listed), "" when none sets one (the
// default: any, unless the org keeps its data off the web — the caller's
// to judge, D240 review) — with the reason a person reads when it narrows.
func MCP(layers []Layer) (rule, reason string) {
	best := MCPRule("")
	var by Layer
	for _, l := range layers {
		if l.Rules.MCP.rank() > best.rank() {
			best, by = l.Rules.MCP, l
		}
	}
	switch best {
	case "":
		return "", ""
	case MCPAny:
		return MCPAny, ""
	case MCPNever:
		return MCPNever, by.who() + " keeps its data from every MCP server" + by.note()
	case MCPOrg:
		return MCPOrg, by.who() + " keeps its data from MCP servers outside it" + by.note()
	}
	return string(best), ""
}
