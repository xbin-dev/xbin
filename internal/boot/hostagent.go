package boot

import (
	"context"
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/server"
)

// hostAgent puts the host's rule for workspace agents in force (D237;
// server/agenthost.go): the deployment file's workspaceAgent key, read by
// the LLM connector with the rest of the file. At boot — the key first
// appearing gives every admin there is an agent under "admins"; a key the
// file no longer has (or no file at all now) is taken back, as far as the
// record (data/hostagent.json) says the host wrote it — on every `users`
// event (an admin made, demoted or made an admin), and when the file
// changes (llm.Options.HostChanged).
func (st *State) hostAgent(srv *server.Server, svc *llm.Service) {
	srv.HostAgentFile = filepath.Join(st.WS, "data", "hostagent.json")
	srv.AgentModelWindow = func(ctx context.Context) (string, int) { return svc.RoleModelWindow(ctx, "agent") } // the budget's view (D246)
	if st.Cfg.LLMConfig != "" {
		srv.HostAgent = func() server.HostAgentRule {
			h := svc.HostWorkspaceAgent()
			return server.HostAgentRule{Set: h.Set, Enabled: h.Enabled, Who: h.Who}
		}
		// the key's web and mcp: the web and MCP policy's preset and ceiling (D239)
		if srv.Delegation != nil {
			srv.Delegation.SetHost(func() delegation.HostNet {
				h := svc.HostWorkspaceAgent()
				return delegation.HostNet{Web: h.Web, MCP: h.MCP, ContextBudget: h.ContextBudget}
			})
		}
	}
	srv.ApplyHostAgent()
	if st.Cfg.LLMConfig != "" && st.Hub != nil {
		go st.onUsersEvents(srv.ApplyHostAgent)
	}
}
