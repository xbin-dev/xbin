package broker

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
)

// builtin_http.go — xbind's own providers of http interface slots (D226). A
// net slot binds to a builtin ("internet", "host", …) or to a provider
// tile; an http slot bound to a builtin reaches a service xbind itself
// serves under /api/xbin/. The binding is the authorization there too: the
// service asks LLMBound-style questions of the binding table, per call.
//
// There is one: the LLM connector (internal/llm, docs/llm.md), which
// provides the "openai" service — a tile binds its llm slot to it the way it
// binds to the llm-gw tile.

// LLMRef is the binding ref of xbind's LLM connector.
const LLMRef = "xbin:llm"

// httpBuiltin is one builtin http provider: the service contract it
// implements and where xbind serves it.
type httpBuiltin struct {
	Service string
	URL     string // what a bound requester calls (under /api/xbin/)
	Label   string
	// Role is what the binding stands for in the edge policy of the tile's
	// non-primary deployments (edgepolicy.go), as a provider tile's provide
	// role does: writer is read-clamped by default, so such a deployment
	// reads (the model list) and doesn't call.
	Role string
}

var httpBuiltins = map[string]httpBuiltin{
	LLMRef: {Service: "openai", URL: "/api/xbin/llm", Label: "LLM by xbin — the workspace's LLM connector", Role: "writer"},
}

// httpBuiltinFor is the builtin ref names when it implements service.
func httpBuiltinFor(ref, service string) (httpBuiltin, bool) {
	hb, ok := httpBuiltins[ref]
	return hb, ok && service == hb.Service
}

// validateHTTPBuiltin is validateBinding's check of a builtin ref on an http
// slot: handled reports that ref is a builtin (err then says why it can't
// bind, nil when it can).
func validateHTTPBuiltin(ref string, def registry.Iface) (handled bool, err error) {
	hb, ok := httpBuiltins[ref]
	if !ok {
		return false, nil
	}
	if def.Service != hb.Service {
		return true, fmt.Errorf("%s provides the %q service; the slot wants %q", ref, hb.Service, def.Service)
	}
	return true, nil
}

// httpBuiltinOptions are the builtins a picker offers for an http slot. The
// LLM connector is offered once it is configured (LLMOffered), so a
// workspace that never set it up sees the options it always saw; binding
// it is a workspace admin's act, greyed for anyone else.
func (b *Broker) httpBuiltinOptions(req registry.Iface, wsAdmin bool) []bindOption {
	hb, ok := httpBuiltinFor(LLMRef, req.Service)
	if !ok || b.LLMOffered == nil || !b.LLMOffered() {
		return nil
	}
	o := bindOption{ID: LLMRef, Label: hb.Label}
	if !wsAdmin {
		o.Label += " — a workspace admin binds it"
		o.Blocked = true
	}
	return []bindOption{o}
}

// LLMBound reports whether comp may call the LLM connector: one of its http
// slots asking for the openai service is bound to it. A window sub-path
// (apps/x/editor) is its tile's. Asked on every call, so an unbind takes
// effect at once.
func (b *Broker) LLMBound(comp string) bool {
	c, ok := b.Reg.Component(comp)
	if !ok {
		if c, _, ok = b.Reg.Resolve(comp); !ok {
			return false
		}
	}
	bound := b.Reg.Workspace().Bindings[c.Path]
	for slot, req := range c.Manifest.Interfaces {
		if _, ok := httpBuiltinFor(LLMRef, req.Service); !ok || req.Kind != "http" {
			continue
		}
		for _, ref := range bound[slot].Refs() {
			if ref == LLMRef {
				return true
			}
		}
	}
	return false
}

// The modes LLMEdge answers (internal/llm's EdgeFull, EdgeRead, EdgeBlock).
const (
	llmEdgeFull  = "full"
	llmEdgeRead  = "read"
	llmEdgeBlock = "block"
)

// LLMEdge says how tile principal p may use its tile's binding to the LLM
// connector (docs/auth.md, the edge policy): in full from the tile's
// primary; from a non-primary deployment by the tile's policy for the edge
// — the slot bound to xbin:llm, an edge like a slot bound to a provider
// tile: read by default (the connector then serves it the model list and
// its status, and refuses its calls, as llm-gw's reader does), or block,
// which a tile manager sets and which wins when several slots are bound.
// why is the refusal's text. Counted for the Deployments panel like every
// edge.
func (b *Broker) LLMEdge(p auth.Principal) (mode, why string) {
	dep, np, err := b.nonPrimary(p)
	switch {
	case err != nil:
		return llmEdgeBlock, err.Error()
	case !np:
		return llmEdgeFull, ""
	}
	c, ok := b.Reg.Component(p.Component)
	if !ok {
		if c, _, ok = b.Reg.Resolve(p.Component); !ok {
			return llmEdgeBlock, fmt.Sprintf("%s's non-primary deployment %q can't call the LLM connector: %s isn't a tile", p.Component, dep, p.Component)
		}
	}
	edges := b.edgesTo(c, LLMRef)
	if len(edges) == 0 {
		return llmEdgeBlock, fmt.Sprintf("%s's non-primary deployment %q can't call the LLM connector: no edge of the tile authorizes it", c.Path, dep)
	}
	stored := b.deploymentEdges(c.Path)
	read := false
	for _, e := range edges {
		switch policy, eff, why, _ := e.effective(stored); eff {
		case deployments.EdgeInherit:
		case deployments.EdgeRead:
			read = true
		default:
			b.tally(c.Path, dep, e.id).refused.Add(1)
			return llmEdgeBlock, blockText(c.Path, dep, LLMRef, e, policy, why)
		}
	}
	if !read {
		return llmEdgeFull, ""
	}
	for _, e := range edges {
		b.tally(c.Path, dep, e.id).clamped.Add(1)
	}
	return llmEdgeRead, fmt.Sprintf("%s's non-primary deployment %q reaches the LLM connector read-only (edge policy \"read\" on %s to %s): it can list models, not run a call. Run the call from the primary; a tile manager can block the edge in the Deployments panel.",
		c.Path, dep, edges[0].id, LLMRef)
}
