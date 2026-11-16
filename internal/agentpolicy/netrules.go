package agentpolicy

// The org rules on the web and on MCP servers (D236): what an org says of
// its data in a workspace agent's conversation that holds it.
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
