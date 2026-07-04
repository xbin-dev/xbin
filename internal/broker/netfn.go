package broker

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/magik6k/buxon/internal/auth"
	"github.com/magik6k/buxon/internal/events"
	"github.com/magik6k/buxon/internal/registry"
	"github.com/magik6k/buxon/internal/sandbox"
	"github.com/magik6k/buxon/internal/server"
)

// Network-function interface wiring (plans/interfaces.md): a component's `net`
// interface can be bound to a provider tile (one that `provides` a net
// interface). buxond then routes that client's egress through the provider via a
// per-client point-to-point link (splice). Builtins (internet/host/lan) stay on
// the legacy net:* grant path; this file only resolves the tile-provider case.

// providesNet reports whether component p offers a net interface.
func providesNet(p *registry.Component) bool {
	for _, def := range p.Manifest.Provides {
		if def.Kind == "net" {
			return true
		}
	}
	return false
}

// netProvider returns the provider component a comp's net interface is bound to
// (a component path that provides net), or "" if none.
func (b *Broker) netProvider(comp string) string {
	for _, prov := range b.Reg.Workspace().Bindings[comp] {
		if p, ok := b.Reg.Component(prov); ok && providesNet(p) {
			return prov
		}
	}
	return ""
}

// netClientsOf returns the components whose net interface is bound to provider,
// sorted (so link indices are stable for a given membership).
func (b *Broker) netClientsOf(provider string) []string {
	var clients []string
	for comp, slots := range b.Reg.Workspace().Bindings {
		for _, prov := range slots {
			if prov == provider {
				clients = append(clients, comp)
				break
			}
		}
	}
	sort.Strings(clients)
	return clients
}

// link addresses for client index i: a /30 point-to-point, provider .1, client .2.
func linkProviderAddr(i int) string { return fmt.Sprintf("10.42.%d.1/30", i) }
func linkClientAddr(i int) (addr, gw string) {
	return fmt.Sprintf("10.42.%d.2/30", i), fmt.Sprintf("10.42.%d.1", i)
}

// NetProviderRoster returns the per-client links a net-provider tile terminates
// (its bound clients + the provider-side address for each). Empty if c is not a
// provider or has no clients — in which case it spawns as an ordinary backend.
func (b *Broker) NetProviderRoster(c *registry.Component) []sandbox.NetClient {
	if !providesNet(c) {
		return nil
	}
	clients := b.netClientsOf(c.Path)
	out := make([]sandbox.NetClient, len(clients))
	for i, cl := range clients {
		out[i] = sandbox.NetClient{Name: cl, Addr: linkProviderAddr(i)}
	}
	return out
}

// NetClientTarget returns, for a component whose net interface is bound to a
// provider tile, that provider's path and the client's point-to-point link
// addr/gw. ok=false when the component isn't bound to a provider (→ legacy
// net:* egress applies instead).
func (b *Broker) NetClientTarget(c *registry.Component) (provider, addr, gw string, ok bool) {
	provider = b.netProvider(c.Path)
	if provider == "" {
		return "", "", "", false
	}
	for i, cl := range b.netClientsOf(provider) {
		if cl == c.Path {
			a, g := linkClientAddr(i)
			return provider, a, g, true
		}
	}
	return "", "", "", false
}

// --- http/service interfaces (plans/interfaces.md) ----------------------------

// httpProvide returns provider p's first http interface definition, if any.
func httpProvide(p *registry.Component) (registry.Iface, bool) {
	for _, def := range p.Manifest.Provides {
		if def.Kind == "http" {
			return def, true
		}
	}
	return registry.Iface{}, false
}

// httpBindingRole returns the role a from→target http-interface binding grants —
// so the binding is also the call grant (from may call target's API). ok=false
// unless from has an http interface slot bound to target and target provides one.
func (b *Broker) httpBindingRole(from, target string) (string, bool) {
	c, ok := b.Reg.Component(from)
	if !ok {
		return "", false
	}
	for slot, prov := range b.Reg.Workspace().Bindings[from] {
		if prov != target {
			continue
		}
		if req, ok := c.Manifest.Interfaces[slot]; !ok || req.Kind != "http" {
			continue
		}
		if p, ok := b.Reg.Component(target); ok {
			if def, ok := httpProvide(p); ok {
				if def.Role != "" {
					return def.Role, true
				}
				return "reader", true
			}
		}
	}
	return "", false
}

// HTTPInterfaces resolves a component's requested http interface slots to their
// bound provider URL + service (for the frame client + backend env).
func (b *Broker) HTTPInterfaces(comp string) map[string]map[string]string {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return nil
	}
	out := map[string]map[string]string{}
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind != "http" {
			continue
		}
		prov := b.Reg.Workspace().Bindings[comp][slot]
		if prov == "" {
			continue
		}
		if _, ok := b.Reg.Component(prov); !ok {
			continue
		}
		out[slot] = map[string]string{"url": "/api/" + prov, "service": req.Service}
	}
	return out
}

// --- bindings API (plans/interfaces.md) ---------------------------------------

// apiBindingsList returns the binding table plus the interfaces components
// request/provide, for the admin "Interfaces" UX.
func (b *Broker) apiBindingsList(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only"})
		return
	}
	type ifaceInfo struct {
		Component string                    `json:"component"`
		Interface map[string]registry.Iface `json:"interfaces,omitempty"`
		Provides  map[string]registry.Iface `json:"provides,omitempty"`
	}
	var comps []ifaceInfo
	for _, c := range b.Reg.Components() {
		if len(c.Manifest.Interfaces) == 0 && len(c.Manifest.Provides) == 0 {
			continue
		}
		comps = append(comps, ifaceInfo{Component: c.Path, Interface: c.Manifest.Interfaces, Provides: c.Manifest.Provides})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"bindings":   b.Reg.Workspace().Bindings,
		"components": comps,
	})
}

// apiBindingSet handles POST (set) and DELETE (clear) of one binding:
// {component, slot[, provider]}. Restarts the component (its wiring changed) and,
// for a net provider, the provider (its roster changed).
func (b *Broker) apiBindingSet(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only — bindings are the owner's to wire"})
		return
	}
	var body struct{ Component, Slot, Provider string }
	if err := decodeJSON(r, &body); err != nil || body.Component == "" || body.Slot == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component, slot, provider}"})
		return
	}
	del := r.Method == http.MethodDelete
	// Restart both the old and new provider (roster change) + the component.
	oldProvider := b.netProvider(body.Component)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]string{}
		}
		if del {
			delete(ws.Bindings[body.Component], body.Slot)
			return
		}
		if ws.Bindings[body.Component] == nil {
			ws.Bindings[body.Component] = map[string]string{}
		}
		ws.Bindings[body.Component][body.Slot] = body.Provider
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "grants", Component: body.Component})
	if b.OnGrantChange != nil {
		b.OnGrantChange(body.Component)
		for _, p := range []string{oldProvider, body.Provider} {
			if _, ok := b.Reg.Component(p); ok {
				b.OnGrantChange(p)
			}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
