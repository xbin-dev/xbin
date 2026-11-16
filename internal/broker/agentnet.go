package broker

// agentnet.go — the org rules on the web and on MCP servers (D239;
// internal/agentpolicy netrules.go) as the server asks them
// (server.agentNetRules): for one org whose data a workspace agent's
// conversation holds, the rule over every layer that holds for its tiles —
// the workspace's bounds, the org's pin and its own rules — and, when it
// narrows, the reason a person reads.

import "github.com/xbin-dev/xbin/internal/agentpolicy"

// AgentWebIn is org's web rule: ask (the default), allow or never.
func (b *Broker) AgentWebIn(org string) (rule, reason string) {
	if b.Users == nil {
		return agentpolicy.WebAsk, ""
	}
	return agentpolicy.Web(b.Users.OrgAgentLayers(org))
}

// AgentMCPIn is org's MCP rule: any (the default) or org.
func (b *Broker) AgentMCPIn(org string) (rule, reason string) {
	if b.Users == nil {
		return agentpolicy.MCPAny, ""
	}
	return agentpolicy.MCP(b.Users.OrgAgentLayers(org))
}
