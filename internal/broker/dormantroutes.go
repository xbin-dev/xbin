package broker

// dormantroutes.go — the interface instances and ingress hosts of a tile's
// deployments (D127h) (09-fabric §6, §8; 11-contract §8, §10.2; NP-09-12,
// NP-09-13).
//
// Where they live. main's stay in the root xbin.json maps ifaceInstances and
// ingressHosts, whether or not main is the primary; another deployment's in
// its own files, data/deployments/<TileKey>/<name>/iface-instances.json and
// ingress-hosts.json, read and written only through the plane's hooks. An
// older xbind never loads those, so it can't route them as main's
// (12-compat PO-9).
//
// The active set. Only the primary's route: HTTPSlots resolves prov#inst
// against the provider primary's table, the ingress lookup and route list
// read each tile's primary's hosts, and a conflict check meets only other
// tiles' active hosts. A registration by any other deployment is stored and
// dormant: it sends no grants event, restarts no consumer, triggers no
// ingress reconcile and takes no part in conflict checks; the deliveries
// switch never touches it (unlike cron jobs and bus subscriptions, active
// for their own deployment, dormant.go). Nothing is cached: a reassignment of the primary applies at
// the next lookup, and a removed deployment's files are gone with it.
//
// Zero state: every tile's primary is main, so each answer below is the root
// map's, the same map, and each handler takes today's path.

import (
	"cmp"
	"errors"
	"maps"
	"net/http"
	"slices"
	"sort"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// The registration files this file keeps (11-contract §10.2).
const (
	depIfaceFile   = "iface-instances.json"
	depIngressFile = "ingress-hosts.json"
)

// depIfaceDoc and depIngressDoc are those files: today's values without the
// component, since the file names the tile and deployment.
type depIfaceDoc struct {
	Schema    int               `json:"schema"`
	Instances map[string]string `json:"instances"`
}

type depIngressDoc struct {
	Schema int      `json:"schema"`
	Hosts  []string `json:"hosts"`
}

// routeTarget is the deployment of comp a PUT /iface-instances or
// /ingress-hosts by p writes (11-contract §0.4, §8), and whether what it
// stores is dormant, not routed now: the tile's own principal writes its
// bound deployment's (DR1); an admin naming the tile, its primary's (DR2).
// ok false: the refusal is answered.
func (b *Broker) routeTarget(w http.ResponseWriter, p auth.Principal, comp string) (dep string, dormant, ok bool) {
	dep = b.primaryOf(comp)
	if p.Component == comp {
		d, err := b.addressed(p, comp)
		switch {
		case errors.Is(err, util.ErrNoDeployment):
			server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/protocol.md")
			return "", false, false
		case err != nil:
			server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
			return "", false, false
		}
		dep = d
	}
	_, routes := b.registrationsActive(comp, dep)
	return dep, !routes, true
}

// writeRouteOK answers a registration: today's body, plus dormant when what
// it stored doesn't route now (11-contract §8).
func writeRouteOK(w http.ResponseWriter, body map[string]any, dormant bool) {
	if dormant {
		body["dormant"] = true
	}
	server.WriteJSON(w, http.StatusOK, body)
}

// ---- interface instances ----

// depInstances is deployment dep (not main) of tile's registered instances;
// none when it has no file, or one this xbind can't read.
func (b *Broker) depInstances(tile, dep string) map[string]string {
	var doc depIfaceDoc
	if b.readDepFile(tile, dep, depIfaceFile, &doc) != nil {
		return nil
	}
	return doc.Instances
}

// activeInstances is provider prov's active instance table: its primary's
// (09-fabric §8): the root map's entry (root is ws.IfaceInstances) while main
// is the primary, the primary's file otherwise.
func (b *Broker) activeInstances(root map[string]map[string]string, prov string) map[string]string {
	if dep := b.primaryOf(prov); dep != util.MainDeployment {
		return b.depInstances(prov, dep)
	}
	return root[prov]
}

// activeInstanceMap is every provider's active instance table, for the
// bindings listing: root itself while every tile's primary is main.
func (b *Broker) activeInstanceMap(root map[string]map[string]string) map[string]map[string]string {
	var out map[string]map[string]string
	for _, c := range b.Reg.Components() {
		dep := b.primaryOf(c.Path)
		if dep == util.MainDeployment {
			continue
		}
		if out == nil {
			out = maps.Clone(root)
			if out == nil {
				out = map[string]map[string]string{}
			}
		}
		if inst := b.depInstances(c.Path, dep); len(inst) > 0 {
			out[c.Path] = inst
		} else {
			delete(out, c.Path)
		}
	}
	if out == nil {
		return root
	}
	return out
}

// storeInstances writes comp's instances as deployment dep's (D127h): main's
// into the root xbin.json map, as today, any other's into its own file; an
// empty map clears them.
func (b *Broker) storeInstances(comp, dep string, inst map[string]string) error {
	if dep != util.MainDeployment {
		return b.writeDepFile(comp, dep, depIfaceFile, depIfaceDoc{Schema: depFileSchema, Instances: inst}, len(inst) == 0)
	}
	return b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if len(inst) == 0 {
			delete(ws.IfaceInstances, comp)
			return
		}
		if ws.IfaceInstances == nil {
			ws.IfaceInstances = map[string]map[string]string{}
		}
		ws.IfaceInstances[comp] = inst
	})
}

// ---- ingress hosts ----

// depIngressHosts is deployment dep (not main) of tile's registered hosts;
// none when it has no file, or one this xbind can't read.
func (b *Broker) depIngressHosts(tile, dep string) []string {
	var doc depIngressDoc
	if b.readDepFile(tile, dep, depIngressFile, &doc) != nil {
		return nil
	}
	return doc.Hosts
}

// activeHosts is tile's active ingress hosts: its primary's (NP-09-13): the
// root map's entry (root is ws.IngressHosts) while main is the primary, the
// primary's file otherwise.
func (b *Broker) activeHosts(root map[string][]string, tile string) []string {
	if dep := b.primaryOf(tile); dep != util.MainDeployment {
		return b.depIngressHosts(tile, dep)
	}
	return root[tile]
}

// activeHostMap is every tile's active ingress hosts, for conflict checks
// and the admin overview: root itself while every tile's primary is main.
func (b *Broker) activeHostMap(root map[string][]string) map[string][]string {
	var out map[string][]string
	for _, c := range b.Reg.Components() {
		dep := b.primaryOf(c.Path)
		if dep == util.MainDeployment {
			continue
		}
		if out == nil {
			out = maps.Clone(root)
			if out == nil {
				out = map[string][]string{}
			}
		}
		if hosts := b.depIngressHosts(c.Path, dep); len(hosts) > 0 {
			out[c.Path] = hosts
		} else {
			delete(out, c.Path)
		}
	}
	if out == nil {
		return root
	}
	return out
}

// storeIngressHosts writes comp's hosts, sorted, as deployment dep's: main's
// into the root xbin.json map, as today, any other's into its own file; an
// empty list clears them.
func (b *Broker) storeIngressHosts(comp, dep string, hosts []string) error {
	sort.Strings(hosts)
	if dep != util.MainDeployment {
		return b.writeDepFile(comp, dep, depIngressFile, depIngressDoc{Schema: depFileSchema, Hosts: hosts}, len(hosts) == 0)
	}
	return b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if len(hosts) == 0 {
			delete(ws.IngressHosts, comp)
			return
		}
		if ws.IngressHosts == nil {
			ws.IngressHosts = map[string][]string{}
		}
		ws.IngressHosts[comp] = hosts
	})
}

// routesHidden reports whether a terminator's principal p reads no routes
// (NP-09-12): it acts in a deployment that isn't its tile's primary, so its
// traefik renders no ACME for the primary's hostnames. err: p's deployment
// is gone (util.ErrNoDeployment) or refused.
func (b *Broker) routesHidden(p auth.Principal) (bool, error) {
	dep, err := b.addressed(p, p.Component)
	if err != nil {
		return false, err
	}
	return !b.isPrimary(p.Component, dep), nil
}

// writeRoutesHidden answers GET /ingress-routes for a terminator whose
// routes are hidden: an empty list, or why its deployment can't be told.
func writeRoutesHidden(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, util.ErrNoDeployment):
		server.WriteError(w, http.StatusNotFound, err.Error(), "/docs/protocol.md")
	case err != nil:
		server.WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
	default:
		server.WriteJSON(w, http.StatusOK, map[string]any{"routes": []any{}})
	}
}

// ---- what the panel lists ----

// DeploymentRouteRegistrations lists deployment dep of tile's interface
// instances and ingress hosts, for the state's Deployment.registrations
// (11-contract §1.1), beside DeploymentRegistrations' cron jobs and bus
// subscriptions: main's from the root xbin.json maps, another's from its
// files, each dormant unless the deployment is the primary.
func (b *Broker) DeploymentRouteRegistrations(tile, dep string) []deployments.Registration {
	dep = cmp.Or(dep, util.MainDeployment)
	var inst map[string]string
	var hosts []string
	if dep == util.MainDeployment {
		ws := b.Reg.Workspace()
		inst, hosts = ws.IfaceInstances[tile], ws.IngressHosts[tile]
	} else {
		inst, hosts = b.depInstances(tile, dep), b.depIngressHosts(tile, dep)
	}
	_, routes := b.registrationsActive(tile, dep)
	out := make([]deployments.Registration, 0, len(inst)+len(hosts))
	for _, id := range slices.Sorted(maps.Keys(inst)) {
		out = append(out, deployments.Registration{Kind: deployments.RegIfaceInstance, Name: id, Prefix: inst[id], Dormant: !routes})
	}
	for _, h := range slices.Sorted(slices.Values(hosts)) {
		out = append(out, deployments.Registration{Kind: deployments.RegIngressHost, Name: h, Dormant: !routes})
	}
	return out
}
