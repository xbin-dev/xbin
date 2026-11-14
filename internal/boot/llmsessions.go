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
	out := term.LLMOffer{Harnesses: o.Harnesses, Terminals: o.Terminals, OpenAIEnv: o.OpenAIEnv, Wires: llmWires, Model: o.Model}
	for _, m := range o.Models {
		out.Models = append(out.Models, sdkacp.ConnectorModel{ID: m.ID, Context: m.Context, Output: m.Output})
	}
	return out, true
}

func (l llmSessions) Mint(user, tile, label string) (string, func(), error) {
	tok, _, revoke, err := l.svc.MintKey(llm.KeyGrant{User: user, Component: tile, Label: label}, 0)
	return tok, revoke, err
}
