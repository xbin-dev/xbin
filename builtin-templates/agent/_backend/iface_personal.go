// iface_personal.go — a person's own providers (docs/partitions.md §Bind
// types): in a person's partition xbind lists their personal binds in
// XBIN_IFACE_LLM / XBIN_IFACE_MCP after the global rows, each with
// "personal": true (other people's partitions and the global instance never
// see them). The agent offers such a provider's models and tools only in
// conversations that person owns — never in one they don't (a hosted
// conversation someone else shares, a run another tile started there). A
// call's context says whether it may: a turn, a compaction and a title set
// it from their conversation's owner, the model picker from the caller. The
// default is no. An unpartitioned instance never has personal rows.
package main

import (
	"context"
	"net/http"
)

type personalCtxKey struct{}

// withPersonal marks ctx as a call in which the partition's person's own
// providers may be used.
func withPersonal(ctx context.Context, ok bool) context.Context {
	if !ok {
		return ctx
	}
	return context.WithValue(ctx, personalCtxKey{}, true)
}

// personalOK: ctx may use personal providers.
func personalOK(ctx context.Context) bool {
	ok, _ := ctx.Value(personalCtxKey{}).(bool)
	return ok
}

// ownConversation: root is a conversation of this partition's person.
func (ag *Agent) ownConversation(root int64) bool {
	if !userMode() || ag == nil || ag.db == nil {
		return false
	}
	r, err := ag.db.getRun(root)
	return err == nil && r.Owner == runUser
}

// personalCtx is ctx for work on run's conversation.
func (ag *Agent) personalCtx(ctx context.Context, run *Run) context.Context {
	if run == nil || !userMode() {
		return ctx
	}
	return withPersonal(ctx, ag.ownConversation(rootOf(run)))
}

// callerPersonal is ctx for a request by the partition's own person (the
// model picker).
func callerPersonal(r *http.Request) context.Context {
	c := callerOf(r)
	return withPersonal(r.Context(), userMode() && c.kind == whoUser && c.user == runUser && c.viewedBy == "")
}

// llmProvidersIn is the providers a call in ctx may use.
func llmProvidersIn(ctx context.Context) []llmProvider {
	all := llmProviders()
	if personalOK(ctx) {
		return all
	}
	out := all[:0:0]
	for _, p := range all {
		if !p.Personal {
			out = append(out, p)
		}
	}
	if len(out) == 0 { // only personal ones bound: the legacy fallback, as with none
		return []llmProvider{{Path: legacyGateway, URL: "http://xbin/api/" + legacyGateway, Legacy: true}}
	}
	return out
}

// catalogIn is c without personal providers' models unless ctx may use them.
func catalogIn(ctx context.Context, c *catalog) *catalog {
	if personalOK(ctx) {
		return c
	}
	personal := map[string]bool{}
	for _, p := range llmProviders() {
		if p.Personal {
			personal[p.Path] = true
		}
	}
	if len(personal) == 0 {
		return c
	}
	out := &catalog{at: c.at}
	for _, m := range c.Models {
		if !personal[m.Provider] {
			out.Models = append(out.Models, m)
		}
	}
	for _, p := range c.Providers {
		if !personal[p.Path] {
			out.Providers = append(out.Providers, p)
		}
	}
	return out
}

// allMCPServersIn is the MCP servers a call in ctx may use.
func allMCPServersIn(ctx context.Context, cfg Config) []MCPServer {
	all := allMCPServers(cfg)
	if personalOK(ctx) {
		return all
	}
	out := all[:0:0]
	for _, s := range all {
		if !s.personal {
			out = append(out, s)
		}
	}
	return out
}
