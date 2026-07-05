package broker

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/sandbox"
	"github.com/magik6k/xbin/internal/server"
)

// Network-function interface wiring (plans/interfaces.md): a component's `net`
// interface is bound either to a builtin (internet/host/lan:<cidr>, resolved in
// egress.go / NetHostShare) or to a provider tile (one that `provides` a net
// interface). For a provider tile xbind routes that client's egress through it
// via a per-client point-to-point link (splice); this file resolves both cases
// and serves the owner's bindings API.

// providesNet reports whether component p offers a net interface.
func providesNet(p *registry.Component) bool {
	for _, def := range p.Manifest.Provides {
		if def.Kind == "net" {
			return true
		}
	}
	return false
}

// netBinding returns the provider a component's `net` interface is bound to —
// a builtin ("internet"/"host"/"lan:<cidr>") or a provider-tile path — or "" if
// the component has no net interface or it's unbound. Owner-set; there is no
// implicit default, so egress stays owner-authorized (like a net:internet grant).
func (b *Broker) netBinding(comp string) string {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return ""
	}
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind == "net" {
			return b.Reg.Workspace().Bindings[comp][slot]
		}
	}
	return ""
}

// netProvider returns the provider *tile* a comp's net interface is bound to
// (a component path that provides net), or "" (builtin / unbound / none).
func (b *Broker) netProvider(comp string) string {
	nb := b.netBinding(comp)
	if p, ok := b.Reg.Component(nb); ok && providesNet(p) {
		return nb
	}
	return ""
}

// netClientsOf returns the components whose net interface is bound to provider,
// sorted (so link indices are stable for a given membership).
func (b *Broker) netClientsOf(provider string) []string {
	var clients []string
	for _, c := range b.Reg.Components() {
		if b.netBinding(c.Path) == provider {
			clients = append(clients, c.Path)
		}
	}
	sort.Strings(clients)
	return clients
}

// NetHostShare reports whether a component's net interface is bound to the
// "host" builtin (share the host network — a powerful, owner-only binding).
func (b *Broker) NetHostShare(c *registry.Component) bool {
	return b.netBinding(c.Path) == "host"
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
				return provideRole(def), true
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
		"pending":    b.pendingBindings(),
	})
}

// bindOption is one provider the owner can pick for a requested interface slot.
type bindOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// pendingBind is a requested interface slot that is not yet bound, with the
// providers that could satisfy it — the data behind the owner's bind-on-install
// prompt (mirrors a pending `uses` grant request).
type pendingBind struct {
	Component string       `json:"component"`
	Slot      string       `json:"slot"`
	Kind      string       `json:"kind"`
	Service   string       `json:"service,omitempty"`
	Options   []bindOption `json:"options"`
}

// pendingBindings lists every requested interface slot with no binding yet.
func (b *Broker) pendingBindings() []pendingBind {
	var out []pendingBind
	for _, c := range b.Reg.Components() {
		for slot, req := range c.Manifest.Interfaces {
			if b.Reg.Workspace().Bindings[c.Path][slot] != "" {
				continue // already bound
			}
			out = append(out, pendingBind{
				Component: c.Path, Slot: slot, Kind: req.Kind, Service: req.Service,
				Options: b.bindOptions(c.Path, req),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Component != out[j].Component {
			return out[i].Component < out[j].Component
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

// bindOptions returns the providers that can satisfy a requested interface: the
// builtins for its kind plus every tile that `provides` a matching interface
// (excluding the requester itself — a component can't be its own provider).
func (b *Broker) bindOptions(comp string, req registry.Iface) []bindOption {
	var builtins, tiles []bindOption
	switch req.Kind {
	case "net":
		builtins = []bindOption{
			{ID: "internet", Label: "internet — public internet (gVisor relay, no LAN)"},
			{ID: "host", Label: "host — share the host's network (powerful)"},
		}
		for _, p := range b.Reg.Components() {
			if p.Path != comp && providesNet(p) {
				tiles = append(tiles, bindOption{ID: p.Path, Label: p.Path + " — net provider tile"})
			}
		}
	case "http":
		for _, p := range b.Reg.Components() {
			if p.Path == comp {
				continue
			}
			if def, ok := httpProvide(p); ok && (req.Service == "" || def.Service == req.Service) {
				tiles = append(tiles, bindOption{ID: p.Path, Label: p.Path + " — " + def.Service + " (grants " + provideRole(def) + ")"})
			}
		}
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].ID < tiles[j].ID })
	return append(builtins, tiles...)
}

// provideRole is the role an http provider grants a bound requester (default reader).
func provideRole(def registry.Iface) string {
	if def.Role != "" {
		return def.Role
	}
	return "reader"
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
