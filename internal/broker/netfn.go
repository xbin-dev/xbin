package broker

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
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
// The policy ceiling (D20) is applied here — the one resolution point every
// consumer (spawn env, relay, provider roster) goes through — so a deny row
// makes even a pre-existing binding inert.
func (b *Broker) netBinding(comp string) string {
	if b.Users != nil && b.Users.Ceiling(comp).Denies(users.PolicyDenyNet) {
		return ""
	}
	c, ok := b.Reg.Component(comp)
	if !ok {
		return ""
	}
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind == "net" {
			return b.Reg.Workspace().Bindings[comp][slot].First()
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
// (its bound clients + the provider-side address for each), plus its
// lan-ingress client links (plans/ingress.md ING-6, keyed "<client>#<slot>",
// 10.43/16 addresses). Empty if c is not a provider or has no clients — in
// which case it spawns as an ordinary backend.
func (b *Broker) NetProviderRoster(c *registry.Component) []sandbox.NetClient {
	var out []sandbox.NetClient
	if providesNet(c) {
		clients := b.netClientsOf(c.Path)
		out = make([]sandbox.NetClient, len(clients))
		for i, cl := range clients {
			out[i] = sandbox.NetClient{Name: cl, Addr: linkProviderAddr(i)}
		}
	}
	return append(out, b.lanIngressRoster(c)...)
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

// httpProvideFor returns the http provide of p that a binding to `service`
// grants — selected BY SERVICE, deterministically. A provider may declare
// several http provides (e.g. llm-gw: openai=writer + metrics=reader); the map
// they live in has no stable "first", so picking one arbitrarily made binding
// roles/endpoints flap between provides. With an empty service (a slot that
// names none) it returns the name-sorted-first http provide, still stable.
func httpProvideFor(p *registry.Component, service string) (registry.Iface, bool) {
	if service != "" {
		for _, def := range p.Manifest.Provides {
			if def.Kind == "http" && def.Service == service {
				return def, true
			}
		}
		return registry.Iface{}, false
	}
	names := make([]string, 0, len(p.Manifest.Provides))
	for name := range p.Manifest.Provides {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if def := p.Manifest.Provides[name]; def.Kind == "http" {
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
	for slot, refs := range b.Reg.Workspace().Bindings[from] {
		for _, ref := range refs.Refs() {
			if prov, _ := splitRef(ref); prov != target {
				continue
			}
			req, ok := c.Manifest.Interfaces[slot]
			if !ok || req.Kind != "http" {
				continue
			}
			if p, ok := b.Reg.Component(target); ok {
				// Role comes from the provide matching THIS slot's service, not
				// an arbitrary one — otherwise a multi-provide target (openai +
				// metrics) flaps the granted role between writer and reader.
				if def, ok := httpProvideFor(p, req.Service); ok {
					return provideRole(def), true
				}
			}
		}
	}
	return "", false
}

// splitRef splits a binding ref "<provider>[#<instance>]" (plans/interfaces.md
// §Multiplicity — the # syntax is used consistently everywhere).
func splitRef(ref string) (provider, instance string) {
	if i := strings.IndexByte(ref, '#'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return ref, ""
}

// IfaceEndpoint is one resolved http-interface binding: the provider (and
// instance, when bound as provider#instance) plus the URL the requester calls.
type IfaceEndpoint struct {
	Provider string `json:"provider"`
	Instance string `json:"instance,omitempty"`
	URL      string `json:"url"`
}

// ResolvedIface is one requested http slot with its resolved endpoints.
type ResolvedIface struct {
	Def       registry.Iface
	Endpoints []IfaceEndpoint
}

// HTTPSlots resolves a component's http interface slots: each binding ref to
// an endpoint URL. Instance refs resolve through the provider's registered
// instance table (the instance's API path prefix); refs that no longer
// resolve (vanished provider / unregistered instance) are skipped here and
// surface as broken in the bindings UI — never silently rewired.
func (b *Broker) HTTPSlots(comp string) map[string]ResolvedIface {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return nil
	}
	ws := b.Reg.Workspace()
	out := map[string]ResolvedIface{}
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind != "http" {
			continue
		}
		ri := ResolvedIface{Def: req}
		for _, ref := range ws.Bindings[comp][slot].Refs() {
			prov, inst := splitRef(ref)
			p, ok := b.Reg.Component(prov)
			if !ok {
				continue
			}
			def, ok := httpProvideFor(p, req.Service)
			if !ok {
				continue
			}
			url := "/api/" + prov
			switch {
			case inst != "":
				path, ok := ws.IfaceInstances[prov][inst]
				if !ok || !def.Instances {
					continue
				}
				url += path
			case def.Instances:
				continue // an instances-provide must be bound as provider#instance
			}
			ri.Endpoints = append(ri.Endpoints, IfaceEndpoint{Provider: prov, Instance: inst, URL: url})
		}
		if len(ri.Endpoints) > 0 || req.Multi {
			out[slot] = ri
		}
	}
	return out
}

// HTTPInterfaces renders a component's http slots for the xbin-interfaces
// meta (frontends: xbin.iface(slot)). Single slots keep the original
// {url, service} shape (+instance when bound to one) so existing tiles work
// unchanged; multi slots carry {service, multi, endpoints}.
func (b *Broker) HTTPInterfaces(comp string) map[string]any {
	out := map[string]any{}
	for slot, ri := range b.HTTPSlots(comp) {
		if ri.Def.Multi {
			eps := ri.Endpoints
			if eps == nil {
				eps = []IfaceEndpoint{}
			}
			out[slot] = map[string]any{"service": ri.Def.Service, "multi": true, "endpoints": eps}
			continue
		}
		if len(ri.Endpoints) == 0 {
			continue
		}
		m := map[string]string{"url": ri.Endpoints[0].URL, "service": ri.Def.Service}
		if ri.Endpoints[0].Instance != "" {
			m["instance"] = ri.Endpoints[0].Instance
		}
		out[slot] = m
	}
	return out
}

// --- bindings API (plans/interfaces.md) ---------------------------------------

// apiBindingsList returns the binding table plus the interfaces components
// request/provide, for the admin "Interfaces" UX.
func (b *Broker) apiBindingsList(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	orgScope := map[string]bool{} // org-admin view: only their orgs' tiles
	if !b.IsAdmin(p) {
		if b.Users != nil && p.Component == "" && p.User != nil {
			for _, o := range p.Access.AdminOrgs() {
				for _, t := range b.Users.OwnedBy("org:" + o) {
					orgScope[t] = true
				}
			}
		}
		if len(orgScope) == 0 {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin only"})
			return
		}
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
		if len(orgScope) > 0 && !orgScope[c.Path] {
			continue
		}
		comps = append(comps, ifaceInfo{Component: c.Path, Interface: c.Manifest.Interfaces, Provides: c.Manifest.Provides})
	}
	bindings := b.Reg.Workspace().Bindings
	pending := b.pendingBindings()
	if len(orgScope) > 0 { // org-admin view: scope every table to their tiles
		fb := map[string]map[string]registry.Binding{}
		for comp, slots := range bindings {
			if orgScope[comp] {
				fb[comp] = slots
			}
		}
		bindings = fb
		fp := pending[:0]
		for _, pb := range pending {
			if orgScope[pb.Component] {
				fp = append(fp, pb)
			}
		}
		pending = fp
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"bindings":   bindings,
		"instances":  b.Reg.Workspace().IfaceInstances,
		"components": comps,
		"pending":    pending,
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
	Component string `json:"component"`
	Slot      string `json:"slot"`
	Kind      string `json:"kind"`
	Service   string `json:"service,omitempty"`
	Multi     bool   `json:"multi,omitempty"`
	// Expose marks an unbound EXPOSED endpoint (plans/ingress.md): binding it
	// publishes the tile, and the bind call carries route config (host/zone/
	// listen), so UIs render it with the route editor, not a plain picker.
	Expose  bool         `json:"expose,omitempty"`
	Options []bindOption `json:"options"`
}

// pendingBindings lists every requested interface slot with no binding yet.
func (b *Broker) pendingBindings() []pendingBind {
	var out []pendingBind
	for _, c := range b.Reg.Components() {
		if registry.IsOffloaded(b.Reg.LifecycleState(c.Path)) {
			continue // offloaded components make no live bind requests
		}
		for slot, req := range c.Manifest.Interfaces {
			if len(b.Reg.Workspace().Bindings[c.Path][slot]) > 0 {
				continue // already bound (multi slots grow via the bindings UI)
			}
			out = append(out, pendingBind{
				Component: c.Path, Slot: slot, Kind: req.Kind, Service: req.Service,
				Multi:   req.Multi,
				Options: b.bindOptions(c.Path, req),
			})
		}
		for slot, def := range c.Manifest.Exposes {
			if len(b.Reg.Workspace().Bindings[c.Path][slot]) > 0 {
				continue
			}
			out = append(out, pendingBind{
				Component: c.Path, Slot: slot, Kind: def.Kind, Expose: true,
				Options: b.exposeBindOptions(c.Path, def),
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

// exposeBindOptions lists the ingress sources an exposed endpoint can bind
// to: the runtime builtin, plus (for http) every terminator tile.
func (b *Broker) exposeBindOptions(comp string, def registry.ExposeDef) []bindOption {
	switch def.Kind {
	case "http":
		opts := []bindOption{{ID: IngressSourceRuntime, Label: "runtime — xbind's built-in ingress listener (BYO/no TLS)"}}
		for _, p := range b.Reg.Components() {
			if p.Path != comp && providesIngress(p) {
				opts = append(opts, bindOption{ID: p.Path, Label: p.Path + " — ingress terminator tile (public TLS)"})
			}
		}
		return opts
	default:
		return []bindOption{{ID: IngressSourceRuntime, Label: "runtime — a host port relayed into the tile"}}
	}
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
			def, ok := httpProvideFor(p, req.Service)
			if !ok {
				continue
			}
			if def.Instances {
				// Each registered instance is a first-class bind option — a
				// non-instance-aware requester connects to one like any provider.
				ids := make([]string, 0)
				for id := range b.Reg.Workspace().IfaceInstances[p.Path] {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					tiles = append(tiles, bindOption{ID: p.Path + "#" + id, Label: p.Path + "#" + id + " — " + def.Service + " (grants " + provideRole(def) + ")"})
				}
				continue
			}
			tiles = append(tiles, bindOption{ID: p.Path, Label: p.Path + " — " + def.Service + " (grants " + provideRole(def) + ")"})
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
// {component, slot[, provider|providers][, host|zone|listen]}. The route
// fields carry an exposed endpoint's config (plans/ingress.md ING-1/ING-2) —
// binding IS the publish action, so they ride the same owner-gated call.
// Restarts the component (its wiring changed) and, for a net provider, the
// provider (its roster changed).
func (b *Broker) apiBindingSet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Component string   `json:"component"`
		Slot      string   `json:"slot"`
		Provider  string   `json:"provider"`  // single ref (back-compat)
		Providers []string `json:"providers"` // full set for multi slots (replaces)
		Host      string   `json:"host"`      // exposes http: exact public hostname
		Zone      string   `json:"zone"`      // exposes http: delegated wildcard zone
		Listen    string   `json:"listen"`    // exposes stream: host listen address
	}
	if err := decodeJSON(r, &body); err != nil || body.Component == "" || body.Slot == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component, slot, provider|providers}"})
		return
	}
	refs := body.Providers
	if len(refs) == 0 && body.Provider != "" {
		refs = []string{body.Provider}
	}
	del := r.Method == http.MethodDelete || len(refs) == 0
	binding := registry.BindTo(refs...)
	if len(binding) == 1 {
		binding[0].Host = strings.ToLower(strings.TrimSpace(body.Host))
		binding[0].Zone = strings.ToLower(strings.TrimSpace(body.Zone))
		binding[0].Listen = strings.TrimSpace(body.Listen)
	}
	if !b.IsAdmin(p) {
		// D26: an org admin may wire bindings for tiles their org OWNS when
		// every normalized target is intra-org or allowance-covered (unbind
		// always). Everyone else: workspace admin only.
		if !b.orgAdminMayBind(p, body.Component, body.Slot, binding, del) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "not approvable by you — bindings are wired by a workspace admin, or an org admin within their org's allowance (D26)", "docs": "/docs/auth.md"})
			return
		}
	}
	if !del {
		if err := b.validateBinding(body.Component, body.Slot, binding); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	// Restart both the old and new provider (roster change) + the component.
	oldProvider := b.netProvider(body.Component)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		if del {
			delete(ws.Bindings[body.Component], body.Slot)
			return
		}
		if ws.Bindings[body.Component] == nil {
			ws.Bindings[body.Component] = map[string]registry.Binding{}
		}
		ws.Bindings[body.Component][body.Slot] = binding
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "grants", Component: body.Component})
	if b.OnGrantChange != nil {
		b.OnGrantChange(body.Component)
		notify := []string{oldProvider}
		for _, ref := range refs {
			prov, _ := splitRef(ref)
			notify = append(notify, prov)
		}
		for _, p := range notify {
			if _, ok := b.Reg.Component(p); ok {
				b.OnGrantChange(p)
			}
		}
	}
	if b.OnIngressChange != nil {
		b.OnIngressChange() // stream listeners / forward sockets may have changed
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// validateBinding checks a binding set before it lands: slot exists (except
// "@" pseudo-slots like @archive), multi only on multi:true http slots,
// instance refs only against instances-provides with that instance
// registered, bare refs rejected where the provide exposes instances.
// Exposed-endpoint slots (plans/ingress.md) additionally validate their route
// config and the ingress policy ceiling.
func (b *Broker) validateBinding(comp, slot string, binding registry.Binding) error {
	refs := binding.Refs()
	if len(refs) > 1 {
		seen := map[string]bool{}
		for _, ref := range refs {
			if seen[ref] {
				return fmt.Errorf("duplicate binding %q", ref)
			}
			seen[ref] = true
		}
	}
	routeCfg := binding.FirstRef().Host != "" || binding.FirstRef().Zone != "" || binding.FirstRef().Listen != ""
	if strings.HasPrefix(slot, "@") { // pseudo-slots are single-valued
		if len(refs) > 1 {
			return fmt.Errorf("%s takes a single provider", slot)
		}
		if routeCfg {
			return fmt.Errorf("host/zone/listen are for exposed endpoints")
		}
		return nil
	}
	c, ok := b.Reg.Component(comp)
	if !ok {
		return fmt.Errorf("no such component: %s", comp)
	}
	if exDef, isExpose := c.Manifest.Exposes[slot]; isExpose {
		return b.validateExposeBinding(c, slot, exDef, binding)
	}
	def, ok := c.Manifest.Interfaces[slot]
	if !ok {
		return fmt.Errorf("%s does not request an interface slot %q (and exposes none by that name)", comp, slot)
	}
	if routeCfg {
		return fmt.Errorf("host/zone/listen are for exposed endpoints; %q is an interface slot", slot)
	}
	if len(refs) > 1 && !(def.Kind == "http" && def.Multi) {
		return fmt.Errorf("slot %q takes a single binding (multi-input needs {kind:http, multi:true})", slot)
	}
	// Policy ceiling (D20): refuse binding net when a workspace/org row denies
	// it (netBinding also enforces at resolution, so this is the friendly half).
	if (def.Kind == "net" || def.Kind == "lan-ingress") && b.Users != nil {
		if row, ok := b.Users.Ceiling(comp).DenyRow(users.PolicyDenyNet); ok {
			return fmt.Errorf("a policy row for tiles matching %q denies net for %s (workspace/org policy — see /docs/auth.md)", row.Tiles, comp)
		}
	}
	for _, ref := range refs {
		prov, inst := splitRef(ref)
		if prov == comp {
			return fmt.Errorf("a component can't be its own provider")
		}
		switch def.Kind {
		case "net":
			if inst != "" {
				return fmt.Errorf("net bindings take no #instance")
			}
			if prov == "internet" || prov == "host" || strings.HasPrefix(prov, "lan:") {
				continue
			}
			if p, ok := b.Reg.Component(prov); !ok || !providesNet(p) {
				return fmt.Errorf("%s does not provide net", prov)
			}
		case "lan-ingress":
			// A service tile's leg into a router tile's subnet (ING-6).
			if inst != "" {
				return fmt.Errorf("lan-ingress bindings take no #instance")
			}
			if p, ok := b.Reg.Component(prov); !ok || !providesLanIngress(p) {
				return fmt.Errorf("%s does not provide lan-ingress", prov)
			}
		case "stream":
			// A direct tile→tile stream binding: "<provider>#<expose-slot>"
			// names a sibling's exposed tcp port (plans/ingress.md source 3).
			p, ok := b.Reg.Component(prov)
			if !ok {
				return fmt.Errorf("no such provider: %s", prov)
			}
			if inst == "" {
				return fmt.Errorf("bind a specific exposed port: %s#<expose-slot>", prov)
			}
			exp, ok := p.Manifest.Exposes[inst]
			if !ok || exp.Kind != "stream" {
				return fmt.Errorf("%s does not expose a stream slot %q", prov, inst)
			}
			if exp.StreamProto() != "tcp" {
				return fmt.Errorf("stream interfaces are tcp-only (%s#%s is %s)", prov, inst, exp.StreamProto())
			}
			if msg := b.ceilingBlockMsg(comp, prov); msg != "" {
				return fmt.Errorf("%s", msg)
			}
		case "http":
			p, ok := b.Reg.Component(prov)
			if !ok {
				return fmt.Errorf("no such provider: %s", prov)
			}
			pd, ok := httpProvideFor(p, def.Service)
			if !ok {
				if def.Service != "" {
					return fmt.Errorf("%s does not provide the %q service the slot wants", prov, def.Service)
				}
				return fmt.Errorf("%s does not provide an http interface", prov)
			}
			switch {
			case pd.Instances && inst == "":
				return fmt.Errorf("%s exposes instances — bind a specific one (%s#<instance>)", prov, prov)
			case pd.Instances:
				if _, ok := b.Reg.Workspace().IfaceInstances[prov][inst]; !ok {
					return fmt.Errorf("unknown instance %s", ref)
				}
			case inst != "":
				return fmt.Errorf("%s has no instances (bind it plain)", prov)
			}
		}
	}
	return nil
}

// validateExposeBinding is the publish gate (plans/ingress.md): source shape,
// hostname authority (exactly one of host/zone for http), listen address for
// stream, route conflicts, and the ingress policy ceiling.
func (b *Broker) validateExposeBinding(c *registry.Component, slot string, def registry.ExposeDef, binding registry.Binding) error {
	if err := registry.ValidateExposes(c.Manifest); err != nil {
		return fmt.Errorf("fix the manifest first: %w", err)
	}
	if len(binding) != 1 {
		return fmt.Errorf("an exposed endpoint binds to a single ingress source")
	}
	br := binding[0]
	if !c.HasBackend() {
		return fmt.Errorf("%s has no backend — only backend-serving tiles can be exposed", c.Path)
	}
	if b.Users != nil {
		if row, ok := b.Users.Ceiling(c.Path).DenyRow(users.PolicyDenyIngress); ok {
			return fmt.Errorf("a policy row for tiles matching %q denies ingress for %s (workspace/org policy — see /docs/auth.md)", row.Tiles, c.Path)
		}
	}
	switch def.Kind {
	case "http":
		if br.Listen != "" {
			return fmt.Errorf("listen is for stream exposes")
		}
		if br.Ref != IngressSourceRuntime {
			p, ok := b.Reg.Component(br.Ref)
			if !ok || !providesIngress(p) {
				return fmt.Errorf("%s is not an ingress terminator (needs provides {kind:\"ingress\"}) — bind \"runtime\" or a terminator tile", br.Ref)
			}
			if br.Ref == c.Path {
				return fmt.Errorf("a component can't be its own ingress source")
			}
		}
		switch {
		case br.Host != "" && br.Zone != "":
			return fmt.Errorf("give either an exact --host or a delegated --zone, not both")
		case br.Host != "":
			if !ingress.ValidHost(br.Host) {
				return fmt.Errorf("bad hostname %q", br.Host)
			}
		case br.Zone != "":
			if !ingress.ValidZone(br.Zone) {
				return fmt.Errorf("bad zone %q (form: *.sites.example.com)", br.Zone)
			}
		default:
			return fmt.Errorf("an http expose binding needs a hostname authority: --host <exact> or --zone '*.<suffix>'")
		}
		return b.exposeRouteConflict(c.Path, slot, br)
	case "stream":
		if br.Host != "" || br.Zone != "" {
			return fmt.Errorf("host/zone are for http exposes")
		}
		if br.Ref != IngressSourceRuntime {
			return fmt.Errorf("stream exposes bind to \"runtime\" (a host port); reaching one from a sibling tile is that tile's stream interface, and VPN-side ingress is a lan-ingress binding")
		}
		listen := br.Listen
		if listen == "" {
			listen = fmt.Sprintf(":%d", def.Port)
		}
		p := listenPort(listen)
		if p < 1 || p > 65535 {
			return fmt.Errorf("bad listen address %q (want \":port\" or \"host:port\")", br.Listen)
		}
		// One host port per binding — collide loudly now, not at reconcile.
		for _, other := range b.Reg.Components() {
			for oslot, obr := range b.exposeBindingsOf(other) {
				if other.Path == c.Path && oslot == slot {
					continue
				}
				odef := other.Manifest.Exposes[oslot]
				if odef.Kind != "stream" || obr.Ref != IngressSourceRuntime {
					continue
				}
				ol := obr.Listen
				if ol == "" {
					ol = fmt.Sprintf(":%d", odef.Port)
				}
				if listenPort(ol) == p && odef.StreamProto() == def.StreamProto() {
					return fmt.Errorf("host port %d/%s is already taken by %s.%s", p, def.StreamProto(), other.Path, oslot)
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("exposes.%s: unknown kind %q", slot, def.Kind)
	}
}

// exposeRouteConflict rejects an http binding whose hostname authority
// collides with an existing one: a duplicate exact host, a duplicate zone,
// or an exact host another tile has registered inside its zone.
func (b *Broker) exposeRouteConflict(comp, slot string, br registry.BindRef) error {
	ws := b.Reg.Workspace()
	for _, other := range b.Reg.Components() {
		for oslot, obr := range b.exposeBindingsOf(other) {
			if other.Path == comp && oslot == slot {
				continue
			}
			if other.Manifest.Exposes[oslot].Kind != "http" {
				continue
			}
			if br.Host != "" && obr.Host == br.Host {
				return fmt.Errorf("%s is already bound to %s.%s", br.Host, other.Path, oslot)
			}
			if br.Zone != "" && obr.Zone == br.Zone {
				return fmt.Errorf("zone %s is already delegated to %s.%s", br.Zone, other.Path, oslot)
			}
		}
	}
	if br.Host != "" {
		for other, hosts := range ws.IngressHosts {
			if other == comp {
				continue
			}
			for _, h := range hosts {
				if h == br.Host {
					return fmt.Errorf("%s is registered by %s inside its delegated zone", br.Host, other)
				}
			}
		}
	}
	return nil
}

// apiIfaceInstancesSet — PUT /iface-instances {component?, instances:{id:path}}.
// A provider whose provide declares {instances:true} registers its concrete
// instances (they're runtime config — accounts, profiles — not manifest data).
// Elements may only set their OWN instances; admin may set any component's.
// Replaces the provider's whole map; requesters bound to it are re-wired.
func (b *Broker) apiIfaceInstancesSet(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Component string            `json:"component"`
		Instances map[string]string `json:"instances"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Instances == nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {instances: {\"<id>\": \"/provider-relative/prefix\"}} (path may be \"\")"})
		return
	}
	comp := body.Component
	switch {
	case p.Component != "":
		if comp != "" && comp != p.Component {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "a provider registers only its own instances"})
			return
		}
		comp = p.Component
	case b.IsAdmin(p):
		if comp == "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component} when called as admin"})
			return
		}
	default:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "providers or admin only"})
		return
	}
	c, ok := b.Reg.Component(comp)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component: " + comp})
		return
	}
	hasInstances := false
	for _, def := range c.Manifest.Provides {
		if def.Kind == "http" && def.Instances {
			hasInstances = true
			break
		}
	}
	if !hasInstances {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": comp + " does not declare provides {kind:http, instances:true}"})
		return
	}
	for id, path := range body.Instances {
		if id == "" || strings.ContainsAny(id, "#/ \t") {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad instance id " + id + " (no #, /, or whitespace)"})
			return
		}
		if path != "" && !strings.HasPrefix(path, "/") {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "instance path for " + id + " must start with / (or be empty)"})
			return
		}
		// Paths are PROVIDER-RELATIVE: xbind injects /api/<provider><path>.
		// A workspace-absolute registration ("/api/<self>/…") would double the
		// prefix and 404 in the *consumer's* logs — reject it here, at the
		// provider, where the mistake is fixable (and don't let install paths
		// leak into persisted state: they'd go stale on rename/clone).
		if path == "/api" || strings.HasPrefix(path, "/api/") {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{
				"error": "instance path for " + id + " must be provider-relative (e.g. \"/m/1\") — xbind composes /api/<provider>+path; do not register \"/api/" + comp + "/…\"",
			})
			return
		}
		if trimmed := strings.TrimRight(path, "/"); trimmed != path {
			body.Instances[id] = trimmed // consumers append "/sub" — avoid "//"
		}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if len(body.Instances) == 0 {
			delete(ws.IfaceInstances, comp)
			return
		}
		if ws.IfaceInstances == nil {
			ws.IfaceInstances = map[string]map[string]string{}
		}
		ws.IfaceInstances[comp] = body.Instances
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Requesters bound to this provider get their URLs re-injected.
	b.Hub.Publish(events.Event{Type: "grants", Component: comp})
	if b.OnGrantChange != nil {
		for rc, slots := range b.Reg.Workspace().Bindings {
			for _, refs := range slots {
				for _, ref := range refs.Refs() {
					if prov, _ := splitRef(ref); prov == comp {
						if _, ok := b.Reg.Component(rc); ok {
							b.OnGrantChange(rc)
						}
					}
				}
			}
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"component": comp, "instances": len(body.Instances)})
}
