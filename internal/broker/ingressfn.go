package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/ingress"
	"github.com/magik6k/xbin/internal/registry"
	"github.com/magik6k/xbin/internal/sandbox"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/users"
)

// Ingress resolution (plans/ingress.md): this file is where a manifest's
// `exposes` + the owner's bindings + the policy ceiling become concrete
// routes (host → tile), stream listeners (host port → netns port), forward
// doors (terminator tile → xbind), and lan-ingress legs (service tile →
// provider subnet). Everything here derives from registry state on demand —
// no cached table to invalidate.

// IngressSourceRuntime is the builtin ingress source: xbind itself (the
// --ingress-listen HTTP terminator and the host-port stream relay).
const IngressSourceRuntime = "runtime"

// ingressFwdPort is the virtual-gateway port a terminator tile dials to hand
// decrypted HTTP back to xbind (10.0.2.2:8642 → its forward unix socket).
const ingressFwdPort = 8642

// streamIfacePortBase is where gateway ports for bound stream INTERFACES
// start (10.0.2.2:20000+i → the provider's exposed port, per sorted slot).
const streamIfacePortBase = 20000

// providesIngress reports whether a component offers an ingress-terminator
// provide (an HTTP terminator tile like the Traefik builtin).
func providesIngress(p *registry.Component) bool {
	for _, def := range p.Manifest.Provides {
		if def.Kind == "ingress" {
			return true
		}
	}
	return false
}

// ingressDenied is the policy-ceiling gate (D20 + ING-5): a workspace/org
// `ingress` deny makes a tile unpublishable — enforced at approval AND here,
// at every resolution, so a hand-edited binding is inert.
func (b *Broker) ingressDenied(comp string) bool {
	return b.Users != nil && b.Users.Ceiling(comp).Denies(users.PolicyDenyIngress)
}

// exposeBindingsOf yields a component's BOUND expose slots (ceiling-applied).
func (b *Broker) exposeBindingsOf(c *registry.Component) map[string]registry.BindRef {
	if len(c.Manifest.Exposes) == 0 || b.ingressDenied(c.Path) {
		return nil
	}
	ws := b.Reg.Workspace()
	out := map[string]registry.BindRef{}
	for slot := range c.Manifest.Exposes {
		if br := ws.Bindings[c.Path][slot].FirstRef(); br.Ref != "" {
			out[slot] = br
		}
	}
	return out
}

// liveForIngress: templates and disabled/offloaded components publish nothing.
func (b *Broker) liveForIngress(c *registry.Component) bool {
	return !c.IsTemplate() && b.Reg.LifecycleState(c.Path) == registry.StateEnabled
}

// IngressLookup resolves (source, host) to the one tile route it publishes —
// the function both HTTP terminators consult per request. Exact hosts win
// over zone registrations; a route resolves only for the source its binding
// names (a terminator can't serve hosts bound through another).
func (b *Broker) IngressLookup(source, host string) (ingress.Route, bool) {
	if host == "" {
		return ingress.Route{}, false
	}
	var zoneHit ingress.Route
	var haveZone bool
	ws := b.Reg.Workspace()
	for _, c := range b.Reg.Components() {
		if !b.liveForIngress(c) {
			continue
		}
		for slot, br := range b.exposeBindingsOf(c) {
			def := c.Manifest.Exposes[slot]
			if def.Kind != "http" {
				continue
			}
			rt := ingress.Route{
				Component: c.Path, Slot: slot, Paths: def.Paths,
				Source: br.Ref, Host: host,
			}
			if br.Host == host {
				if br.Ref == source {
					return rt, true // exact host — strongest claim
				}
				continue
			}
			if br.Zone != "" && !haveZone && ingress.HostInZone(host, br.Zone) {
				for _, reg := range ws.IngressHosts[c.Path] {
					if reg == host {
						rt.Zone = br.Zone
						zoneHit, haveZone = rt, true
						break
					}
				}
			}
		}
	}
	if haveZone && zoneHit.Source == source {
		return zoneHit, true
	}
	return ingress.Route{}, false
}

// IngressRoutes lists every resolvable route (concrete hosts only), for the
// admin overview and terminator config generation.
func (b *Broker) IngressRoutes() []ingress.Route {
	seen := map[string]bool{}
	var out []ingress.Route
	ws := b.Reg.Workspace()
	add := func(rt ingress.Route) {
		if rt.Host == "" || seen[rt.Host] {
			return
		}
		seen[rt.Host] = true
		out = append(out, rt)
	}
	// Exact hosts first (they win at lookup), then zone registrations.
	for pass := 0; pass < 2; pass++ {
		for _, c := range b.Reg.Components() {
			if !b.liveForIngress(c) {
				continue
			}
			for slot, br := range b.exposeBindingsOf(c) {
				def := c.Manifest.Exposes[slot]
				if def.Kind != "http" {
					continue
				}
				rt := ingress.Route{Component: c.Path, Slot: slot, Paths: def.Paths, Source: br.Ref}
				switch pass {
				case 0:
					rt.Host = br.Host
					add(rt)
				case 1:
					if br.Zone == "" {
						continue
					}
					for _, reg := range ws.IngressHosts[c.Path] {
						if ingress.HostInZone(reg, br.Zone) {
							r2 := rt
							r2.Host, r2.Zone = reg, br.Zone
							add(r2)
						}
					}
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Host < out[j].Host })
	return out
}

// PublishedHost is the split-horizon predicate (ING-6): does this hostname
// belong to the workspace's published surface? Zone-covered names count even
// unregistered — public wildcard DNS would resolve them too (the route then
// 404s, matching public behavior).
func (b *Broker) PublishedHost(host string) bool {
	if host == "" {
		return false
	}
	for _, c := range b.Reg.Components() {
		if !b.liveForIngress(c) {
			continue
		}
		for slot, br := range b.exposeBindingsOf(c) {
			if c.Manifest.Exposes[slot].Kind != "http" {
				continue
			}
			if br.Host == host || (br.Zone != "" && ingress.HostInZone(host, br.Zone)) {
				return true
			}
		}
	}
	return false
}

// IngressStreamSpecs computes the desired host listeners: every stream
// expose bound to the runtime source (ceiling-applied), for Streams.Reconcile.
func (b *Broker) IngressStreamSpecs() []ingress.StreamSpec {
	var out []ingress.StreamSpec
	for _, c := range b.Reg.Components() {
		if !b.liveForIngress(c) || !c.HasBackend() {
			continue
		}
		for slot, br := range b.exposeBindingsOf(c) {
			def := c.Manifest.Exposes[slot]
			if def.Kind != "stream" || br.Ref != IngressSourceRuntime {
				continue
			}
			listen := br.Listen
			if listen == "" {
				listen = fmt.Sprintf(":%d", def.Port)
			}
			out = append(out, ingress.StreamSpec{
				Component: c.Path, Slot: slot,
				Proto: def.StreamProto(), Listen: listen, Port: def.Port,
			})
		}
	}
	return out
}

// IngressSources lists the terminator tiles that get a forward socket: every
// live component providing kind=ingress (bound or not — an unbound one can
// route nothing, its lookups all miss).
func (b *Broker) IngressSources() []string {
	var out []string
	for _, c := range b.Reg.Components() {
		if b.liveForIngress(c) && c.HasBackend() && providesIngress(c) {
			out = append(out, c.Path)
		}
	}
	sort.Strings(out)
	return out
}

// streamIfaceSlots returns a component's kind=stream interface slots, sorted
// (the order fixes each slot's gateway port: streamIfacePortBase + index).
func streamIfaceSlots(c *registry.Component) []string {
	var slots []string
	for slot, def := range c.Manifest.Interfaces {
		if def.Kind == "stream" {
			slots = append(slots, slot)
		}
	}
	sort.Strings(slots)
	return slots
}

// streamIfaceTarget resolves a bound stream interface slot to its provider
// tile + exposed in-netns port. Binding ref form: "<provider>#<expose-slot>".
func (b *Broker) streamIfaceTarget(c *registry.Component, slot string) (prov string, port int, ok bool) {
	ref := b.Reg.Workspace().Bindings[c.Path][slot].First()
	if ref == "" {
		return "", 0, false
	}
	prov, exSlot := splitRef(ref)
	if !b.ceilingAllows(c.Path, prov) {
		return "", 0, false
	}
	p, found := b.Reg.Component(prov)
	if !found || !b.liveForIngress(p) {
		return "", 0, false
	}
	def, found := p.Manifest.Exposes[exSlot]
	if !found || def.Kind != "stream" || def.StreamProto() != "tcp" {
		return "", 0, false
	}
	return prov, def.Port, true
}

// IngressFwdFor returns a component's policy-exempt virtual-gateway forwards
// (installed into its egress relay): the ingress-forward door for terminator
// tiles, and one port per bound stream interface. Installed as runner.IngressFwd.
func (b *Broker) IngressFwdFor(c *registry.Component) map[int]string {
	out := map[int]string{}
	if providesIngress(c) && b.IngressSocket != nil {
		out[ingressFwdPort] = "unix:" + b.IngressSocket(c.Path)
	}
	for i, slot := range streamIfaceSlots(c) {
		if prov, port, ok := b.streamIfaceTarget(c, slot); ok {
			out[streamIfacePortBase+i] = fmt.Sprintf("stream:%s:%d", prov, port)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// IngressNetFor reports whether a component needs the ingress network
// plumbing even without egress: a bound runtime stream expose (xbind dials
// in), a bound stream interface, or it's dialed by a sibling's stream
// interface. Installed as runner.IngressNet.
func (b *Broker) IngressNetFor(c *registry.Component) bool {
	for slot, br := range b.exposeBindingsOf(c) {
		if c.Manifest.Exposes[slot].Kind == "stream" && br.Ref == IngressSourceRuntime {
			return true
		}
	}
	for _, slot := range streamIfaceSlots(c) {
		if _, _, ok := b.streamIfaceTarget(c, slot); ok {
			return true // it dials out via the gateway forward
		}
	}
	// Dialed BY a sibling's stream interface → its ports must be reachable.
	for _, other := range b.Reg.Components() {
		if other.Path == c.Path {
			continue
		}
		for _, slot := range streamIfaceSlots(other) {
			if prov, _, ok := b.streamIfaceTarget(other, slot); ok && prov == c.Path {
				return true
			}
		}
	}
	return false
}

// --- lan-ingress (ING-6) -------------------------------------------------

// lanIngressClientsOf lists (component, slot) pairs whose kind=lan-ingress
// interface is bound to provider, sorted — link indices are stable for a
// given membership, mirroring netClientsOf.
func (b *Broker) lanIngressClientsOf(provider string) []struct{ Comp, Slot string } {
	var out []struct{ Comp, Slot string }
	ws := b.Reg.Workspace()
	for _, c := range b.Reg.Components() {
		for slot, def := range c.Manifest.Interfaces {
			if def.Kind != "lan-ingress" {
				continue
			}
			if ws.Bindings[c.Path][slot].First() != provider {
				continue
			}
			if b.Users != nil && b.Users.Ceiling(c.Path).Denies(users.PolicyDenyNet) {
				continue // a net-denied tile gets no provider leg either
			}
			out = append(out, struct{ Comp, Slot string }{c.Path, slot})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Comp != out[j].Comp {
			return out[i].Comp < out[j].Comp
		}
		return out[i].Slot < out[j].Slot
	})
	return out
}

// lan-ingress link addresses for client index i: a /30, provider .1, client .2
// — the 10.43/16 twin of the 10.42/16 egress-splice links.
func lanProviderAddr(i int) string { return fmt.Sprintf("10.43.%d.1/30", i) }
func lanClientAddr(i int) string   { return fmt.Sprintf("10.43.%d.2/30", i) }

// lanIngressRoster is the provider-side extension of NetProviderRoster: one
// extra link per lan-ingress client, keyed "<client>#<slot>" in the netmux.
func (b *Broker) lanIngressRoster(c *registry.Component) []sandbox.NetClient {
	if !providesLanIngress(c) {
		return nil
	}
	clients := b.lanIngressClientsOf(c.Path)
	out := make([]sandbox.NetClient, len(clients))
	for i, cl := range clients {
		out[i] = sandbox.NetClient{Name: cl.Comp + "#" + cl.Slot, Addr: lanProviderAddr(i)}
	}
	return out
}

// providesLanIngress reports whether a component offers a lan-ingress provide.
func providesLanIngress(p *registry.Component) bool {
	for _, def := range p.Manifest.Provides {
		if def.Kind == "lan-ingress" {
			return true
		}
	}
	return false
}

// NetLinksFor returns a component's own lan-ingress legs (client side), for
// the sandbox spec. Installed as runner.NetLinks.
func (b *Broker) NetLinksFor(c *registry.Component) []sandbox.NetLink {
	var out []sandbox.NetLink
	ws := b.Reg.Workspace()
	for slot, def := range c.Manifest.Interfaces {
		if def.Kind != "lan-ingress" {
			continue
		}
		provider := ws.Bindings[c.Path][slot].First()
		if provider == "" {
			continue
		}
		if p, ok := b.Reg.Component(provider); !ok || !providesLanIngress(p) {
			continue
		}
		for i, cl := range b.lanIngressClientsOf(provider) {
			if cl.Comp == c.Path && cl.Slot == slot {
				out = append(out, sandbox.NetLink{Provider: provider, Slot: slot, Addr: lanClientAddr(i)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slot < out[j].Slot })
	return out
}

// --- hairpin (ING-6) -----------------------------------------------------

// DialStream is the runner's netns-reach primitive (runner.DialInto), set by
// main; IngressSocket maps a terminator tile to its forward-socket path;
// IngressHTTPAddr is the builtin second listener's local dial address ("" =
// not running); OnIngressChange reconciles listeners after binding/manifest
// changes. All plans/ingress.md wiring.
//
// (Fields live on Broker; declared in this file to keep the subsystem legible.)

// HairpinDial resolves a split-horizon VIP flow to the ingress path the same
// port would take from outside: a bound runtime stream listener on that port
// (the Traefik tile's :80/:443 travel this way), else the builtin HTTP
// terminator if it listens there. Installed as runner.HairpinDial.
func (b *Broker) HairpinDial(port int) (net.Conn, error) {
	for _, spec := range b.IngressStreamSpecs() {
		if listenPort(spec.Listen) == port && spec.Proto == "tcp" && b.DialStream != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			return b.DialStream(ctx, spec.Component, "tcp", spec.Port)
		}
	}
	if b.IngressHTTPAddr != "" && listenPort(b.IngressHTTPAddr) == port {
		return net.Dial("tcp", b.IngressHTTPAddr)
	}
	return nil, fmt.Errorf("no ingress endpoint on port %d", port)
}

func listenPort(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return -1
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return -1
	}
	return n
}

// --- APIs ----------------------------------------------------------------

// apiIngressHosts — PUT /ingress-hosts {component?, hosts: […]}. A tile with
// a delegated-zone binding registers the concrete hostnames it serves
// (ING-2): self-scoped like iface-instances, every host must sit inside one
// of ITS bound zones (a tile can never claim bank.example.com), and exact
// hosts / other tiles' registrations can't be shadowed. Replaces the set.
func (b *Broker) apiIngressHosts(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var body struct {
		Component string   `json:"component"`
		Hosts     []string `json:"hosts"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Hosts == nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {hosts: [\"name.zone.example.com\", …]} (empty list clears)"})
		return
	}
	comp := body.Component
	switch {
	case p.Component != "":
		if comp != "" && comp != p.Component {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "a tile registers only its own ingress hosts"})
			return
		}
		comp = p.Component
	case b.IsAdmin(p):
		if comp == "" {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {component} when called as admin"})
			return
		}
	default:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "tiles or admin only"})
		return
	}
	c, ok := b.Reg.Component(comp)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component: " + comp})
		return
	}
	// The authority boundary: every host must fall inside a zone the OWNER
	// delegated to this tile.
	var zones []string
	for slot, br := range b.exposeBindingsOf(c) {
		if c.Manifest.Exposes[slot].Kind == "http" && br.Zone != "" {
			zones = append(zones, br.Zone)
		}
	}
	if len(zones) == 0 && len(body.Hosts) > 0 {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": comp + " has no delegated-zone ingress binding — the owner binds a zone first (bx expose " + comp + " <slot>=<source> --zone '*.…')"})
		return
	}
	seen := map[string]bool{}
	for i, h := range body.Hosts {
		h = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(h), "."))
		body.Hosts[i] = h
		if !ingress.ValidHost(h) {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad hostname " + h})
			return
		}
		if seen[h] {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "duplicate hostname " + h})
			return
		}
		seen[h] = true
		inZone := false
		for _, z := range zones {
			if ingress.HostInZone(h, z) {
				inZone = true
				break
			}
		}
		if !inZone {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": h + " is outside this tile's delegated zone(s) — registrations are bounded to the authority the owner drew (plans/ingress.md ING-2)"})
			return
		}
		if err := b.ingressHostConflict(comp, h); err != nil {
			server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if len(body.Hosts) == 0 {
			delete(ws.IngressHosts, comp)
			return
		}
		if ws.IngressHosts == nil {
			ws.IngressHosts = map[string][]string{}
		}
		sort.Strings(body.Hosts)
		ws.IngressHosts[comp] = body.Hosts
	}); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.Hub.Publish(events.Event{Type: "grants", Component: comp})
	if b.OnIngressChange != nil {
		b.OnIngressChange()
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"component": comp, "hosts": len(body.Hosts)})
}

// ingressHostConflict rejects a registration colliding with an exact-bound
// host or another tile's registration.
func (b *Broker) ingressHostConflict(comp, host string) error {
	ws := b.Reg.Workspace()
	for _, c := range b.Reg.Components() {
		for slot, br := range b.exposeBindingsOf(c) {
			_ = slot
			if br.Host == host {
				return fmt.Errorf("%s is already bound exactly to %s", host, c.Path)
			}
		}
	}
	for other, hosts := range ws.IngressHosts {
		if other == comp {
			continue
		}
		for _, h := range hosts {
			if h == host {
				return fmt.Errorf("%s is already registered by %s", host, other)
			}
		}
	}
	return nil
}

// apiIngressRoutes — GET /ingress-routes: the concrete host→tile routes. A
// terminator tile reads the routes bound THROUGH IT (to generate its proxy/
// ACME config); admins see everything. Not readable by other principals —
// the workspace's published surface is itself mildly sensitive.
func (b *Broker) apiIngressRoutes(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	routes := b.IngressRoutes()
	switch {
	case b.IsAdmin(p):
	case p.Component != "":
		c, ok := b.Reg.Component(p.Component)
		if !ok || !providesIngress(c) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "only ingress terminator tiles read routes"})
			return
		}
		scoped := routes[:0]
		for _, rt := range routes {
			if rt.Source == p.Component {
				scoped = append(scoped, rt)
			}
		}
		routes = scoped
	default:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "admin or terminator tiles only"})
		return
	}
	if routes == nil {
		routes = []ingress.Route{}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"routes": routes})
}

// IngressOverview is the admin picture: every exposes slot with its binding
// state, the resolved routes, and the registered zone hosts. Runtime listener
// status is merged in by main (which owns the managers).
func (b *Broker) IngressOverview() map[string]any {
	type slotInfo struct {
		Component string   `json:"component"`
		Slot      string   `json:"slot"`
		Kind      string   `json:"kind"`
		Paths     []string `json:"paths,omitempty"`
		Proto     string   `json:"proto,omitempty"`
		Port      int      `json:"port,omitempty"`
		Source    string   `json:"source,omitempty"` // "" = unbound
		Host      string   `json:"host,omitempty"`
		Zone      string   `json:"zone,omitempty"`
		Listen    string   `json:"listen,omitempty"`
		Blocked   string   `json:"blocked,omitempty"` // policy ceiling denial
	}
	var slots []slotInfo
	for _, c := range b.Reg.Components() {
		if len(c.Manifest.Exposes) == 0 {
			continue
		}
		denied := b.ingressDenied(c.Path)
		ws := b.Reg.Workspace()
		for slot, def := range c.Manifest.Exposes {
			si := slotInfo{
				Component: c.Path, Slot: slot, Kind: def.Kind, Paths: def.Paths,
			}
			if def.Kind == "stream" {
				si.Proto, si.Port = def.StreamProto(), def.Port
			}
			if br := ws.Bindings[c.Path][slot].FirstRef(); br.Ref != "" {
				si.Source, si.Host, si.Zone, si.Listen = br.Ref, br.Host, br.Zone, br.Listen
			}
			if denied {
				if row, ok := b.Users.Ceiling(c.Path).DenyRow(users.PolicyDenyIngress); ok {
					si.Blocked = fmt.Sprintf("policy row for tiles matching %q denies ingress", row.Tiles)
				}
			}
			slots = append(slots, si)
		}
	}
	sort.Slice(slots, func(i, j int) bool {
		if slots[i].Component != slots[j].Component {
			return slots[i].Component < slots[j].Component
		}
		return slots[i].Slot < slots[j].Slot
	})
	return map[string]any{
		"exposes":      slots,
		"routes":       b.IngressRoutes(),
		"ingressHosts": b.Reg.Workspace().IngressHosts,
		"terminators":  b.IngressSources(),
	}
}

// lanIngressEnvJSON renders a provider's lan-ingress client map for its env
// (XBIN_LAN_INGRESS) — how a router tile learns which service sits behind
// which link address.
func (b *Broker) lanIngressEnvJSON(c *registry.Component) string {
	clients := b.lanIngressClientsOf(c.Path)
	if len(clients) == 0 {
		return ""
	}
	type entry struct {
		Component    string `json:"component"`
		Slot         string `json:"slot"`
		ProviderAddr string `json:"providerAddr"`
		ClientAddr   string `json:"clientAddr"`
	}
	out := make([]entry, len(clients))
	for i, cl := range clients {
		out[i] = entry{cl.Comp, cl.Slot, lanProviderAddr(i), lanClientAddr(i)}
	}
	j, _ := json.Marshal(out)
	return string(j)
}
