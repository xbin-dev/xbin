package broker

import (
	"fmt"

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
}

var httpBuiltins = map[string]httpBuiltin{
	LLMRef: {Service: "openai", URL: "/api/xbin/llm", Label: "LLM by xbin — the workspace's LLM connector"},
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
