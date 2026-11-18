package delegation

// hostfile.go — the host's web and MCP policy for people's workspace agents
// (D239): the "web" and "mcp" members of the "workspaceAgent" key of the
// host's one deployment file, XBIN_LLM_CONFIG — read, validated and
// reloaded there with the rest of the file (internal/llm/hostagent.go;
// docs/workspace-agent.md "Set by your host") and handed to the store as a
// provider (Store.SetHost).
//
// What it sets is the host's, in the shape of the host's other settings:
// Locks names the settings it holds by their names in PUT
// /delegate/workspace ({locked, reason, ceiling?}), and Refuses the ones of
// a write it refuses (409 {settings}). Unlike the switch and who, which
// lock outright, a mode is a preset and a CEILING: in force is the admin's
// value when they set one, never past the host's; the admin may always
// narrow it (turning a capability off is never a risk a host must
// prevent). The host's lists hold beside the admin's and lock nothing.

// HostNet is the host's web and MCP policy as its file reads (nil: unset).
type HostNet struct {
	Web *WebPolicy
	MCP *MCPPolicy
	// ContextBudget: the host's preset and ceiling for the workspace
	// agent's context budget (tokens; 0 unset — budget.go, D246)
	ContextBudget int
}

// Set reports whether the host's file sets any of it.
func (h HostNet) Set() bool { return h.Web != nil || h.MCP != nil || h.ContextBudget > 0 }

// NetSettings are the web and MCP settings an admin writes, by their names
// in PUT /delegate/workspace.
var NetSettings = []string{"web.mode", "web.allow", "web.deny", "web.search", "mcp.mode", "mcp.servers"}

// HostLock is one setting the host's file holds: a write past its ceiling
// is refused.
type HostLock struct {
	Locked bool   `json:"locked"`
	Reason string `json:"reason"`
	// Ceiling: the most an admin may set — they may set less.
	Ceiling string `json:"ceiling,omitempty"`
}

// HostLockReason is what a held control says — the host's other
// settings' words.
const HostLockReason = "set by your host"

// Locks names the settings the host's file holds: a mode it sets (a
// ceiling), search where it keeps it off.
func (h HostNet) Locks() map[string]HostLock {
	out := map[string]HostLock{}
	if h.Web != nil && h.Web.Mode != "" {
		out["web.mode"] = HostLock{Locked: true, Reason: HostLockReason, Ceiling: h.Web.Mode}
	}
	if h.Web != nil && h.Web.Search != nil && !*h.Web.Search {
		out["web.search"] = HostLock{Locked: true, Reason: HostLockReason, Ceiling: "false"}
	}
	if h.MCP != nil && h.MCP.Mode != "" {
		out["mcp.mode"] = HostLock{Locked: true, Reason: HostLockReason, Ceiling: h.MCP.Mode}
	}
	if h.MCP != nil && h.MCP.Servers != nil { // a list of the host's is the list: locked outright (D240)
		out["mcp.servers"] = HostLock{Locked: true, Reason: HostLockReason}
	}
	if l, ok := budgetLock(h); ok { // a preset and a ceiling (D246)
		out["contextBudget"] = l
	}
	return out
}

// Refuses names the settings of p the host's file refuses: a mode past the
// host's, search where it keeps it off.
func (h HostNet) Refuses(p NetPatch) []string {
	var out []string
	l := h.Locks()
	if c, held := l["web.mode"]; held && p.WebMode != nil && webRank(*p.WebMode) > webRank(c.Ceiling) {
		out = append(out, "web.mode")
	}
	if _, held := l["web.search"]; held && p.WebSearch != nil && *p.WebSearch {
		out = append(out, "web.search")
	}
	if c, held := l["mcp.mode"]; held && p.MCPMode != nil && mcpRank(*p.MCPMode) > mcpRank(c.Ceiling) {
		out = append(out, "mcp.mode")
	}
	if _, held := l["mcp.servers"]; held && p.MCPServers != nil {
		out = append(out, "mcp.servers")
	}
	return out
}
