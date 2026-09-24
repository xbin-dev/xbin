package broker

// Exposed endpoints (plans/ingress.md, D79): the publish gate — every route of
// a slot validated on its own, exclusivity per hostname, zone and host port —
// and the shapes the bindings API lists them in. Resolution (lookup, route
// table, listeners) is ingressfn.go.

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// validateExposeBinding is the publish gate (plans/ingress.md): an exposed
// endpoint takes any number of routes (D79), each validated on its own —
// source shape, hostname authority (exactly one of host/zone for http),
// listen address for stream, route conflicts with other slots — plus no
// hostname, zone or host port given twice within the slot, and the ingress
// policy ceiling.
func (b *Broker) validateExposeBinding(c *registry.Component, slot string, def registry.ExposeDef, binding registry.Binding) error {
	if err := registry.ValidateExposes(c.Manifest); err != nil {
		return fmt.Errorf("fix the manifest first: %w", err)
	}
	if len(binding) == 0 {
		return fmt.Errorf("an exposed endpoint binds to an ingress source")
	}
	if !c.HasBackend() {
		return fmt.Errorf("%s has no backend — only backend-serving tiles can be exposed", c.Path)
	}
	if b.Users != nil {
		if row, ok := b.Users.Ceiling(c.Path).DenyRow(users.PolicyDenyIngress); ok {
			return fmt.Errorf("a policy row for tiles matching %q denies ingress for %s (workspace/org policy — see /docs/auth.md)", row.Tiles, c.Path)
		}
	}
	seen := map[string]bool{}
	for _, br := range binding {
		key, err := b.validateExposeRoute(c, slot, def, br)
		if err != nil {
			return err
		}
		if seen[key] {
			return fmt.Errorf("%s is given twice on %s.%s", key, c.Path, slot)
		}
		seen[key] = true
	}
	return nil
}

// validateExposeRoute checks one route of an exposed endpoint and returns
// what it claims — the hostname, zone or host port — for the in-slot
// duplicate check.
func (b *Broker) validateExposeRoute(c *registry.Component, slot string, def registry.ExposeDef, br registry.BindRef) (string, error) {
	switch def.Kind {
	case "http":
		if br.Listen != "" {
			return "", fmt.Errorf("listen is for stream exposes")
		}
		if br.Ref != IngressSourceRuntime {
			p, ok := b.Reg.Component(br.Ref)
			if !ok || !providesIngress(p) {
				return "", fmt.Errorf("%s is not an ingress terminator (needs provides {kind:\"ingress\"}) — bind \"runtime\" or a terminator tile", br.Ref)
			}
			if br.Ref == c.Path {
				return "", fmt.Errorf("a component can't be its own ingress source")
			}
		}
		key := ""
		switch {
		case br.Host != "" && br.Zone != "":
			return "", fmt.Errorf("give either an exact --host or a delegated --zone, not both")
		case br.Host != "":
			if !ingress.ValidHost(br.Host) {
				return "", fmt.Errorf("bad hostname %q", br.Host)
			}
			key = "host " + br.Host
		case br.Zone != "":
			if !ingress.ValidZone(br.Zone) {
				return "", fmt.Errorf("bad zone %q (form: *.sites.example.com)", br.Zone)
			}
			key = "zone " + br.Zone
		default:
			return "", fmt.Errorf("an http expose binding needs a hostname authority: --host <exact> or --zone '*.<suffix>'")
		}
		return key, b.exposeRouteConflict(c.Path, slot, br)
	case "stream":
		if br.Host != "" || br.Zone != "" {
			return "", fmt.Errorf("host/zone are for http exposes")
		}
		if br.Ref != IngressSourceRuntime {
			return "", fmt.Errorf("stream exposes bind to \"runtime\" (a host port); reaching one from a sibling tile is that tile's stream interface, and VPN-side ingress is a lan-ingress binding")
		}
		p := listenPort(streamListen(br, def))
		if p < 1 || p > 65535 {
			return "", fmt.Errorf("bad listen address %q (want \":port\" or \"host:port\")", br.Listen)
		}
		// One slot per host port — collide loudly now, not at reconcile.
		for _, other := range b.Reg.Components() {
			for _, oer := range b.exposeRoutesOf(other) {
				if other.Path == c.Path && oer.Slot == slot {
					continue
				}
				if oer.Def.Kind != "stream" || oer.Ref != IngressSourceRuntime {
					continue
				}
				if listenPort(streamListen(oer.BindRef, oer.Def)) == p && oer.Def.StreamProto() == def.StreamProto() {
					return "", fmt.Errorf("host port %d/%s is already taken by %s.%s", p, def.StreamProto(), other.Path, oer.Slot)
				}
			}
		}
		return fmt.Sprintf("host port %d/%s", p, def.StreamProto()), nil
	default:
		return "", fmt.Errorf("exposes.%s: unknown kind %q", slot, def.Kind)
	}
}

// exposeRouteConflict rejects an http binding whose hostname authority
// collides with an existing one: a duplicate exact host, a duplicate zone,
// or an exact host another tile has registered inside its zone.
func (b *Broker) exposeRouteConflict(comp, slot string, br registry.BindRef) error {
	ws := b.Reg.Workspace()
	for _, other := range b.Reg.Components() {
		for _, oer := range b.exposeRoutesOf(other) {
			if other.Path == comp && oer.Slot == slot {
				continue
			}
			if oer.Def.Kind != "http" {
				continue
			}
			if br.Host != "" && oer.Host == br.Host {
				return fmt.Errorf("%s is already bound to %s.%s", br.Host, other.Path, oer.Slot)
			}
			if br.Zone != "" && oer.Zone == br.Zone {
				return fmt.Errorf("zone %s is already delegated to %s.%s", br.Zone, other.Path, oer.Slot)
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

// exposeSlot is one exposed endpoint as GET /bindings lists it: every route
// it has, the sources it can bind, and whether the caller may wire it.
type exposeSlot struct {
	Component  string       `json:"component"`
	Slot       string       `json:"slot"`
	Kind       string       `json:"kind"`
	Paths      []string     `json:"paths,omitempty"`
	Proto      string       `json:"proto,omitempty"`
	Port       int          `json:"port,omitempty"`
	Routes     []routeInfo  `json:"routes"`
	Options    []bindOption `json:"options"`
	Approvable bool         `json:"approvable"`
}

// routeInfo is one route of an exposed endpoint: its source and its host,
// zone or listen address.
type routeInfo struct {
	Source string `json:"source"`
	Host   string `json:"host,omitempty"`
	Zone   string `json:"zone,omitempty"`
	Listen string `json:"listen,omitempty"`
}

func routeInfos(binding registry.Binding) []routeInfo {
	out := []routeInfo{}
	for _, br := range binding {
		if br.Ref != "" {
			out = append(out, routeInfo{Source: br.Ref, Host: br.Host, Zone: br.Zone, Listen: br.Listen})
		}
	}
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

// exposeDef reports whether comp.slot is an exposed endpoint.
func (b *Broker) exposeDef(comp, slot string) (registry.ExposeDef, bool) {
	c, ok := b.Reg.Component(comp)
	if !ok {
		return registry.ExposeDef{}, false
	}
	def, ok := c.Manifest.Exposes[slot]
	return def, ok
}
