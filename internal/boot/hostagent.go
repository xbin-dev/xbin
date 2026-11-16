package boot

import (
	"path/filepath"

	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/server"
)

// hostAgent puts the host's rule for workspace agents in force (D236;
// server/agenthost.go): the deployment file's workspaceAgent key, read by
// the LLM connector with the rest of the file. At boot — the key first
// appearing gives every admin there is an agent under "admins"; a key the
// file no longer has (or no file at all now) is taken back, as far as the
// record (data/hostagent.json) says the host wrote it — on every `users`
// event (an admin made, demoted or made an admin), and when the file
// changes (llm.Options.HostChanged).
func (st *State) hostAgent(srv *server.Server, svc *llm.Service) {
	srv.HostAgentFile = filepath.Join(st.WS, "data", "hostagent.json")
	if st.Cfg.LLMConfig != "" {
		srv.HostAgent = func() server.HostAgentRule {
			h := svc.HostWorkspaceAgent()
			return server.HostAgentRule{Set: h.Set, Enabled: h.Enabled, Who: h.Who}
		}
	}
	srv.ApplyHostAgent()
	if st.Cfg.LLMConfig != "" && st.Hub != nil {
		go st.onUsersEvents(srv.ApplyHostAgent)
	}
}
