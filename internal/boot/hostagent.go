package boot

import (
	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/server"
)

// hostAgent puts the host's rule for workspace agents in force (D236;
// server/agenthost.go): the deployment file's workspaceAgent key, read by
// the LLM connector with the rest of the file. At boot — the key first
// appearing gives every admin there is an agent under "admins" — on every
// `users` event (an admin made, or made an admin), and when the file
// changes (llm.Options.HostChanged). No host file named: nothing, ever.
func (st *State) hostAgent(srv *server.Server, svc *llm.Service) {
	if st.Cfg.LLMConfig == "" {
		return
	}
	srv.HostAgent = func() server.HostAgentRule {
		h := svc.HostWorkspaceAgent()
		return server.HostAgentRule{Set: h.Set, Enabled: h.Enabled, Who: h.Who}
	}
	srv.ApplyHostAgent()
	if st.Hub != nil {
		go st.onUsersEvents(srv.ApplyHostAgent)
	}
}
