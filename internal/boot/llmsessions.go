package boot

import (
	"context"
	"errors"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/broker"
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

func (l llmSessions) Mint(user, tile, deployment, label string, refused func(status int, code, message string)) (string, func(), error) {
	// a person's session key: it names the deployment the session targets
	// and is theirs — not held to the edge policy a deployment's own
	// backend is (llm/caller.go)
	tok, g, revoke, err := l.svc.MintKey(llm.KeyGrant{User: user, Component: tile, Deployment: deployment, Label: label}, 0)
	if err != nil || refused == nil {
		return tok, revoke, err
	}
	// the session says what the connector refuses its CLI (llm/keywatch.go)
	stop := l.svc.WatchKey(g.ID, func(r llm.KeyRefusal) { refused(r.Status, r.Code, r.Message) })
	return tok, func() { stop(); revoke() }, nil
}

// agentCredentials is who serves an agent session's model calls
// (term.Manager.Credentials): the agents policy's one answer for (person,
// tile, harness), with the session's own choice applied (D229).
func (st *State) agentCredentials(userID, tile, harness, choice string) term.CredentialRule {
	s := st.Broker.AgentSessionLLM(userID, tile, harness)
	return term.CredentialRule{Use: s.Choose(choice), Forced: s.Forced, Reason: s.Reason}
}

// agentConnector answers the policy's question whether a harness can run on
// the LLM connector (broker.AgentConnector; nil: it can) — from the
// provider table and what the connector serves now. Where a tile's
// sessions must run on the connector, a harness that can't doesn't start.
func (l llmSessions) agentConnector(harness string) error {
	prov, ok := agent.Lookup(harness)
	if !ok {
		return errors.New("it is not a coding agent this xbind knows")
	}
	offer, ok := l.Offer(context.Background(), "", "", "")
	if !ok || !offer.Agents {
		return errors.New("the workspace's LLM connector is not set up for agent sessions (a workspace admin adds an upstream: bx llm upstream add)")
	}
	if why := term.LLMWhyNot(prov, offer); why != "" {
		return errors.New(why)
	}
	return nil
}

// agentModel answers what the connector knows of a model id
// (broker.AgentModels): its upstream and whether that upstream is private —
// what an org's "private models only" asks.
func (l llmSessions) agentModel(id string) (agentpolicy.ModelRef, bool) {
	up, model, private, ok := l.svc.ResolveModel(id)
	return agentpolicy.ModelRef{Upstream: up, Model: model, Private: private}, ok
}

// llmCallPolicy is the connector's CallPolicy: a call made for a tile — a
// bound tile's own, or one with a key minted for a session or a harness on
// it — may use only the models the tile's orgs and the workspace allow
// (the broker's AgentCallAllows). The tile rides the key's grant, so a key
// of a session on a tile that allows private models only can't call
// another, whatever its CLI asks for.
func llmCallPolicy(brk *broker.Broker) llm.CallPolicy {
	return func(c llm.Caller, u llm.UpstreamInfo, model string) error {
		if c.Component == "" {
			return nil
		}
		return brk.AgentCallAllows(c.Component, agentpolicy.ModelRef{Upstream: u.Name, Model: model, Private: u.Private})
	}
}
