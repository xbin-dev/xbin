package broker

// reassignroutes.go — the broker's half of a reassignment of a tile's
// primary (09-fabric §8 steps 3-4; D127d, D127h). The lookups need nothing: each
// reads the primary's table at its next call (dormantroutes.go). What is
// captured at spawn does: a consumer bound to <tile>#<inst> holds the old
// primary's instance URL in its env until it restarts, and the ingress
// listeners were reconciled against the old primary's hosts.

import (
	"maps"
	"slices"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// PrimaryReassigned runs after the plane moved tile's primary from from to
// to, outside its locks: when the active interface-instance table changed,
// the grants event and a restart of every requester bound to tile, as a
// routed PUT /iface-instances does; when the active hosts changed, the
// ingress reconcile, as a routed PUT /ingress-hosts does. Equal tables do
// nothing, so a tile that registers neither restarts no one.
func (b *Broker) PrimaryReassigned(tile, from, to string) {
	ws := b.Reg.Workspace()
	if !maps.Equal(b.instancesOf(&ws, tile, from), b.instancesOf(&ws, tile, to)) {
		b.rebindConsumers(&ws, tile)
	}
	if !slices.Equal(b.hostsOf(&ws, tile, from), b.hostsOf(&ws, tile, to)) && b.OnIngressChange != nil {
		b.OnIngressChange()
	}
}

// instancesOf is deployment dep of tile's registered instances: main's in
// the root map, another's in its file.
func (b *Broker) instancesOf(ws *registry.WorkspaceManifest, tile, dep string) map[string]string {
	if dep == util.MainDeployment {
		return ws.IfaceInstances[tile]
	}
	return b.depInstances(tile, dep)
}

// hostsOf is deployment dep of tile's registered ingress hosts, sorted as
// they are stored.
func (b *Broker) hostsOf(ws *registry.WorkspaceManifest, tile, dep string) []string {
	if dep == util.MainDeployment {
		return ws.IngressHosts[tile]
	}
	return b.depIngressHosts(tile, dep)
}

// rebindConsumers publishes the grants event for prov and restarts every
// requester bound to it, so their instance URLs are injected again.
func (b *Broker) rebindConsumers(ws *registry.WorkspaceManifest, prov string) {
	b.Hub.Publish(events.Event{Type: "grants", Component: prov})
	if b.OnGrantChange == nil {
		return
	}
	for _, rc := range slices.Sorted(maps.Keys(ws.Bindings)) {
		bound := false
		for _, refs := range ws.Bindings[rc] {
			for _, ref := range refs.Refs() {
				if p, _ := splitRef(ref); p == prov {
					bound = true
				}
			}
		}
		if _, ok := b.Reg.Component(rc); ok && bound {
			b.OnGrantChange(rc)
		}
	}
}
