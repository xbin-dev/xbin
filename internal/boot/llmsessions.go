package boot

import (
	"context"

	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/term"
	sdkacp "github.com/xbin-dev/xbin/sdk/acp"
)

// llmsessions.go — the LLM connector as terminal and agent sessions get it
// (D226, docs/llm.md "Coding agents and terminals"): internal/llm's Service
// behind term's LLMConnector. (Tile sandboxes reach it through the LLM-only
// listener: llmlisten.go.)

// llmSessions is the connector as terminal and agent sessions use it.
type llmSessions struct{ svc *llm.Service }

// llmWires are the wires the connector serves today (an Anthropic-wire
// route is a later package: Claude Code then gets the connector too).
var llmWires = []string{sdkacp.WireChat, sdkacp.WireResponses}

func (l llmSessions) Offer(ctx context.Context, user, tile, kind string) (term.LLMOffer, bool) {
	// only an agent session's start lists the models (its CLI is told them):
	// a terminal's open and a launcher's listing never wait on a provider
	o, ok := l.svc.Offer(ctx, user, tile, kind == term.KindAgent)
	if !ok {
		return term.LLMOffer{}, false
	}
	out := term.LLMOffer{Agents: o.Agents, Terminals: o.Terminals, OpenAIEnv: o.OpenAIEnv, Wires: llmWires, Model: o.Model}
	for _, m := range o.Models {
		out.Models = append(out.Models, sdkacp.ConnectorModel{ID: m.ID, Context: m.Context, Output: m.Output})
	}
	return out, true
}

func (l llmSessions) Mint(user, tile, label string, refused func(status int, code, message string)) (string, func(), error) {
	tok, g, revoke, err := l.svc.MintKey(llm.KeyGrant{User: user, Component: tile, Label: label}, 0)
	if err != nil || refused == nil {
		return tok, revoke, err
	}
	// the session says what the connector refuses its CLI (llm/keywatch.go)
	stop := l.svc.WatchKey(g.ID, func(r llm.KeyRefusal) { refused(r.Status, r.Code, r.Message) })
	return tok, func() { stop(); revoke() }, nil
}

// agentCredentials is the credential rule in force for a person opening an
// agent session of harness on tile (term.Manager.Credentials) — THE one
// place sessions ask the agents policy (D229): today the person's saved
// default for the harness over the workspace's credentials default, which
// when it is "connector" is not theirs to override. The policy package's
// one function for (person, tile, harness) — the three modes, an org's
// allowed models — takes this function's place when it lands.
func (st *State) agentCredentials(userID, tile, harness string) term.CredentialRule {
	saved := ""
	if srv := st.Server; srv != nil && srv.AgentPrefs != nil && userID != "" {
		saved = srv.AgentPrefs.Get(userID).Harnesses[harness].Credentials
	}
	use, forced := st.Broker.AgentCredentials("", saved)
	if forced {
		return term.CredentialRule{Mode: term.CredConnectorOnly}
	}
	return term.CredentialRule{Mode: use} // own-first | own | connector
}
