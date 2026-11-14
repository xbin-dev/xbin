package server

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/term"
)

// agentllm.go — what GET /agent/providers says about the workspace's LLM
// connector (D226 part 2, term/llmhook.go): per provider, whether a session
// of the caller's would run on the connector, on their own sign-in, or
// needs one — so a launcher can say it before the session opens.

// providerRow is a catalog row with what the route adds to it: why the
// harness can't start on the tile asked about (D229: ?all=1), and its `llm`
// member. With neither it marshals exactly as the catalog's (the route's
// golden).
type providerRow struct {
	agent.Provider
	Unavailable string            `json:"unavailable,omitempty"`
	LLM         *term.ProviderLLM `json:"llm,omitempty"`
}

// withAgentLLM is rows with each one's `llm`, while the workspace's LLM
// connector is offered to agent sessions; rows as they are otherwise. tile
// is the tile the session would open on ("" none: the caller named none,
// or one they can't read).
func (s *Server) withAgentLLM(r *http.Request, tile string, rows []providerRow) []providerRow {
	if s.Term == nil {
		return rows
	}
	p := auth.PrincipalOf(r)
	provs := make([]agent.Provider, len(rows))
	for i, row := range rows {
		provs[i] = row.Provider
	}
	llm := s.Term.AgentLLM(p.UserID, term.HomeKey(p), tile, provs)
	for i := range rows {
		rows[i].LLM = llm[rows[i].ID]
	}
	return rows
}
