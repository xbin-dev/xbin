package server

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
)

// agentllm.go — what GET /agent/providers says about the workspace's LLM
// connector (D226 part 2, term/llmhook.go): per provider, whether a session
// of the caller's would run on the connector, on their own sign-in, or
// needs one — so a launcher can say it before the session opens.

// providerRow is a catalog row with its `llm` member. Without one it
// marshals exactly as the catalog's (the route's golden).
type providerRow struct {
	agent.Provider
	LLM *term.ProviderLLM `json:"llm,omitempty"`
}

// withAgentLLM is provs as the route answers them: the catalog itself while
// agent sessions get nothing of a connector, else each row with its `llm`.
// ?cwd=<tile> names the tile the session would open on (the connector's
// answer may depend on it); a tile the caller can't read counts as none.
func (s *Server) withAgentLLM(r *http.Request, provs []agent.Provider) any {
	if s.Term == nil {
		return provs
	}
	p := auth.PrincipalOf(r)
	cwd := strings.Trim(r.URL.Query().Get("cwd"), "/")
	if cwd != "" && !p.CanReadTile(cwd) {
		cwd = ""
	}
	llm := s.Term.AgentLLM(p.UserID, term.HomeKey(p), cwd, provs)
	if llm == nil {
		return provs
	}
	rows := make([]providerRow, len(provs))
	for i, pr := range provs {
		rows[i] = providerRow{Provider: pr, LLM: llm[pr.ID]}
	}
	return rows
}
