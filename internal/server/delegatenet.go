package server

// delegatenet.go — the web and MCP settings around a person's workspace
// agent (D236): what the person's own Preferences show and may turn on
// (GET/PUT /delegate/state's web, mcp, mcpReadOnly and net), and what an
// admin's settings read (GET/PUT /delegate/workspace's web, mcp, policy,
// host, locks).

import (
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// netForPerson is what the workspace policy lets a person turn on, for
// their Preferences: each capability's policy and, when it is off, why.
func (s *Server) netForPerson() map[string]any {
	pol := s.Delegation.NetPolicy()
	web := map[string]any{"policy": pol.Web.Mode}
	if pol.Web.Mode == delegation.WebOff {
		web["why"] = policyOffWhy("Reading the web", pol.Web.ByHost)
	}
	if lists := len(pol.Web.Allow) + len(pol.Web.HostAllow); lists > 0 {
		web["only"] = append(append([]string{}, pol.Web.HostAllow...), pol.Web.Allow...) // the hosts it may read, when a list names them
	}
	mcp := map[string]any{"policy": pol.MCP.Mode}
	if pol.MCP.Mode == delegation.MCPOff {
		mcp["why"] = policyOffWhy("Using MCP tools", pol.MCP.ByHost)
	}
	return map[string]any{"web": web, "mcp": mcp, "search": pol.Web.Search}
}

// netSettingRefusal is why a person may not turn on what they ask ("" when
// they may): turning one on while the workspace policy has it off would
// read as on and do nothing. Turning off is never refused.
func (s *Server) netSettingRefusal(web, mcp, mcpReadOnly *bool) (refusal, msg string) {
	pol := s.Delegation.NetPolicy()
	if web != nil && *web && pol.Web.Mode == delegation.WebOff {
		return "policy", policyOffWhy("Reading the web", pol.Web.ByHost)
	}
	if (mcp != nil && *mcp || mcpReadOnly != nil && *mcpReadOnly) && pol.MCP.Mode == delegation.MCPOff {
		return "policy", policyOffWhy("Using MCP tools", pol.MCP.ByHost)
	}
	return "", ""
}

// netPolicyChanged tells every person's agent, and their own clients, that
// what it may do on the web or with MCP servers changed (a `state`
// event): the backend reads its sessions' reach again, the chrome its
// Preferences.
func (s *Server) netPolicyChanged() {
	for _, u := range s.Delegation.People() {
		s.delegateEvent(u, "state", "")
	}
}

// netWorkspaceView is the web and MCP part of GET /delegate/workspace: the
// admin's own settings, what is in force under the host's file, the
// host's file and what of it holds.
func (s *Server) netWorkspaceView(out map[string]any) {
	ws := s.Delegation.Workspace()
	web := delegation.WebPolicy{}
	if ws.Web != nil {
		web = *ws.Web
	}
	search := web.Search != nil && *web.Search
	out["web"] = map[string]any{"mode": orStr(web.Mode, delegation.WebOff), "allow": orNone(web.Allow), "deny": orNone(web.Deny), "search": search}
	mcp := delegation.MCPPolicy{}
	if ws.MCP != nil {
		mcp = *ws.MCP
	}
	out["mcp"] = map[string]any{"mode": orStr(mcp.Mode, delegation.MCPOff)}
	if ws.NetBy != "" {
		out["netBy"], out["netAt"] = ws.NetBy, ws.NetAt
	}
	pol := s.Delegation.NetPolicy()
	out["policy"] = pol
	host, hostErr := s.Delegation.Host()
	hv := map[string]any{"active": s.Delegation.HostActive(), "lockAll": host.LockAll}
	if hostErr != "" {
		hv["error"] = hostErr
	}
	locks := map[string]any{}
	lock := func(name string, ceiling any) {
		l := map[string]any{"locked": true, "reason": "set by your host"}
		if ceiling != nil {
			l["ceiling"] = ceiling
		}
		locks[name] = l
	}
	if host.Web != nil {
		hv["web"] = host.Web
		if host.Web.Mode != "" {
			lock("web.mode", host.Web.Mode)
		}
		if len(host.Web.Allow) > 0 {
			lock("web.allow", nil)
		}
		if len(host.Web.Deny) > 0 {
			lock("web.deny", nil)
		}
		if host.Web.Search != nil {
			lock("web.search", *host.Web.Search)
		}
	}
	if host.MCP != nil {
		hv["mcp"] = host.MCP
		if host.MCP.Mode != "" {
			lock("mcp.mode", host.MCP.Mode)
		}
	}
	if host.LockAll {
		locks["*"] = map[string]any{"locked": true, "reason": "set by your host"}
	}
	out["host"], out["locks"] = hv, locks
}

func orNone(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func orStr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// sessionNet is what a session's answers carry of the web (D236): whether
// the conversation has read it, and its reach now.
func (s *Server) sessionNet(p auth.Principal, h delegation.AgentSession, out map[string]any) {
	out["web"] = h.Web
	out["reach"] = s.reachView(p, h)
}
