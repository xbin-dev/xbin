package broker

// edgepolicy.go — the outbound edge policy of a tile's non-primary
// deployments (09-fabric §5) (D127a) (D127f) (D127o) (D127s): the tile's edges with
// their kinds, values and defaults (EdgesOf) and the check a write passes
// (ValidateEdgePolicy); the verdict resolveTarget asks for a non-primary
// caller (the read clamp, block, inherit; block wins), each refusal naming
// its edge and counted for the panel; the principal-aware checks the
// broker's gates call (Policy's self rule, allowRes's clamp, governance,
// approving an xbin grant); and the edges applied at spawn (the net edge,
// capability grants, stream dials). The primary never reaches any of it:
// resolveTarget answers it, and every tile without a record, grantedRole's
// role first (F1) (D119c).

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/deployments"
	"github.com/xbin-dev/xbin/internal/registry"
)

func init() {
	edgeVerdict = func(b *Broker, d Decision, caller, callerDep, target, role string) Decision {
		return b.applyEdgePolicy(d, caller, callerDep, target, role)
	}
}

// The kinds of edge (09-fabric §5.1), as Edge.kind reports them.
const (
	edgeHTTP       = "http"        // an http interface slot, single or multi
	edgeStream     = "stream"      // a stream interface slot
	edgeLAN        = "lan-ingress" // a lan-ingress interface slot
	edgeNet        = "net"         // the tile's net slot
	edgeGrant      = "grant"       // a call grant on another tile: an explicit row or a same-scope uses entry
	edgeResource   = "resource"    // a resource grant in another scope, or workspace-level
	edgeCode       = "code"        // grant:code, grant:code:<tile>
	edgeCapability = "capability"  // gpu:*, cap:*
)

// The value sets a kind takes (09-fabric §5.1): read|block where the role
// can be read-clamped, inherit|block for the role-less net slot and
// capability grants, block alone where nothing narrows the edge (D127o).
var (
	clampValues   = []string{deployments.EdgeRead, deployments.EdgeBlock}
	inheritValues = []string{deployments.EdgeInherit, deployments.EdgeBlock}
	blockValues   = []string{deployments.EdgeBlock}
)

// edgeLeg is one provider an edge reaches, with the role the tile holds
// through it.
type edgeLeg struct {
	to   string // the provider tile, or the grant's target (res:…, code, gpu:0, …)
	role string // "" for a role-less edge (stream, lan-ingress, net, capability)
	why  string // why the read clamp can't narrow this leg (D127o); "" when it can
}

// outEdge is one outbound edge of a tile (09-fabric §5.1).
type outEdge struct {
	id, kind string
	legs     []edgeLeg
	values   []string // what a tile manager may store; block always among them
	def      string   // what an absent override reads as
	why      string   // why the edge takes block alone (D127o), or is forced to it
	forced   bool     // block whatever is stored: a net that shares the host's (§5.8)
	// custom names the custom role of a block-alone http or grant edge, whose
	// refusal asks the provider to declare what it implies (§5.5).
	custom string
}

// ---- the tile's edges ----

// tileEdges is every outbound edge c has now, sorted by id: its bound http,
// stream and lan-ingress slots, its net slot, its explicit grant rows (call,
// resource, code and capability grants) and its same-scope uses of sibling
// tiles. Governance grants are no edge: non-primary principals never hold
// them (D127k). Own-scope resources are the deployment's own data (D127i), and
// net:* rows no longer grant anything.
func (b *Broker) tileEdges(c *registry.Component) []outEdge {
	var out []outEdge
	bindings := b.Reg.Workspace().Bindings[c.Path]
	for slot, req := range c.Manifest.Interfaces {
		refs := bindings[slot].Refs()
		switch req.Kind {
		case "http":
			if e, ok := b.httpSlotEdge(c, slot, req, ""); ok {
				out = append(out, e)
			}
		case "stream", "lan-ingress":
			if len(refs) > 0 {
				out = append(out, blockedSlotEdge(slot, req.Kind, refs))
			}
		case "net":
			out = append(out, b.netEdge(c, slot))
		}
	}
	targets := b.grantRoles(c, "")
	for _, target := range slices.Sorted(maps.Keys(targets)) {
		if e, ok := b.grantEdge(c, target, targets[target]); ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// edgesTo is the edges of c that authorize a call to target, each with its
// legs to target alone, sorted by id: the grant (an explicit row, or a
// same-scope uses entry) and every http slot bound to it. These are what
// grantedRole merges into one role (09-fabric §5.4).
func (b *Broker) edgesTo(c *registry.Component, target string) []outEdge {
	var out []outEdge
	if target == "" {
		return nil
	}
	if role, ok := b.grantRoles(c, target)[target]; ok {
		if e, ok := b.grantEdge(c, target, role); ok {
			out = append(out, e)
		}
	}
	for slot, req := range c.Manifest.Interfaces {
		if req.Kind != "http" {
			continue
		}
		if e, ok := b.httpSlotEdge(c, slot, req, target); ok {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// grantRoles is the role c holds through each grant edge, as grantedRole
// reads it: the best explicit row per target, else the first same-scope
// uses entry naming a sibling tile. only, when set, limits it to one target.
func (b *Broker) grantRoles(c *registry.Component, only string) map[string]string {
	out := map[string]string{}
	for _, g := range b.Reg.Workspace().Grants {
		if g.From != c.Path || only != "" && g.Target != only {
			continue
		}
		if best, ok := out[g.Target]; !ok || roleSatisfies(g.Role, best, nil) {
			out[g.Target] = g.Role
		}
	}
	for _, u := range c.Manifest.Uses {
		if only != "" && u.Target != only || strings.HasPrefix(u.Target, "res:") {
			continue
		}
		if _, ok := out[u.Target]; !ok && b.sameScope(c, u.Target) {
			out[u.Target] = u.Role
		}
	}
	return out
}

// grantEdge is the edge grant:<target> at role, by what target is; false for
// a grant that is no edge (governance, net:*, an own-scope resource).
func (b *Broker) grantEdge(c *registry.Component, target, role string) (outEdge, bool) {
	e := outEdge{id: "grant:" + target}
	switch {
	case target == "xbin", strings.HasPrefix(target, "xbin:"), strings.HasPrefix(target, "net:"):
		return outEdge{}, false
	case strings.HasPrefix(target, "gpu:"): // a shared device cgroups don't cover (§5.9)
		e.kind, e.values, e.def = edgeCapability, inheritValues, deployments.EdgeBlock
		e.legs = []edgeLeg{{to: target}}
		return e, true
	case strings.HasPrefix(target, "cap:"):
		e.kind, e.values, e.def = edgeCapability, inheritValues, deployments.EdgeInherit
		e.legs = []edgeLeg{{to: target}}
		return e, true
	case target == "code", strings.HasPrefix(target, "code:"):
		e.kind = edgeCode
	case strings.HasPrefix(target, "res:"):
		if b.sameScope(c, target) {
			return outEdge{}, false
		}
		e.kind = edgeResource
		if rt, res, ok := b.parseRes(target); ok && res != nil && rt.Scope == "" && c.Scope == "" &&
			(res.Type == "filesystem" || res.Type == "sqlite") {
			// the path env and a rw bind: a read-only bind is not a safe read (§5.1)
			e.legs = []edgeLeg{{to: target, role: role}}
			e.values, e.def = blockValues, deployments.EdgeBlock
			e.why = fmt.Sprintf("a workspace-level %s resource can't be read-clamped: a read-only bind is not a safe read", res.Type)
			return e, true
		}
	default:
		e.kind = edgeGrant
	}
	e.legs = []edgeLeg{b.clampLeg(target, role)}
	return clampEdge(e), true
}

// httpSlotEdge is the edge slot:<slot> of c's http slot req, over its bound
// providers (only, when set, alone), each at the role its provide grants;
// false when no bound provider provides the slot's service.
func (b *Broker) httpSlotEdge(c *registry.Component, slot string, req registry.Iface, only string) (outEdge, bool) {
	e := outEdge{id: "slot:" + slot, kind: edgeHTTP}
	seen := map[string]bool{}
	for _, ref := range b.Reg.Workspace().Bindings[c.Path][slot].Refs() {
		prov, _ := splitRef(ref)
		if only != "" && prov != only || seen[prov] {
			continue
		}
		p, ok := b.Reg.Component(prov)
		if !ok {
			continue
		}
		def, ok := httpProvideFor(p, req.Service)
		if !ok {
			continue
		}
		seen[prov] = true
		e.legs = append(e.legs, b.clampLeg(prov, provideRole(def)))
	}
	if len(e.legs) == 0 {
		return outEdge{}, false
	}
	return clampEdge(e), true
}

// httpSlotsTo lists from's http slots bound to target, sorted by slot, with
// the role each grants: httpBindingRole answers the first's, and each is an
// edge the clamp reads (09-fabric §5.3).
func (b *Broker) httpSlotsTo(from, target string) []edgeLeg {
	c, ok := b.Reg.Component(from)
	if !ok || target == "" {
		return nil
	}
	var out []edgeLeg
	slots := slices.Sorted(maps.Keys(c.Manifest.Interfaces))
	for _, slot := range slots {
		if req := c.Manifest.Interfaces[slot]; req.Kind == "http" {
			if e, ok := b.httpSlotEdge(c, slot, req, target); ok {
				out = append(out, e.legs[0])
			}
		}
	}
	return out
}

// clampLeg is a leg to target at role, with why the read clamp can't narrow
// it: a role that doesn't imply reader through the blessed order, the bus
// aliases or the provider primary's expose.implies (F8) (D127o). A provider
// merely declaring a reader role doesn't make another role clampable.
func (b *Broker) clampLeg(target, role string) edgeLeg {
	l := edgeLeg{to: target, role: role}
	if !roleSatisfies(role, "reader", b.exposeOf(target)) {
		l.why = fmt.Sprintf("it grants the custom role %q on %s, which implies no reader the read clamp could narrow it to", role, target)
	}
	return l
}

// exposeOf is target's expose, the provider primary's (the registry's
// component describes the primary, F8); nil for a resource or a target that
// isn't a tile.
func (b *Broker) exposeOf(target string) *registry.Expose {
	if p, ok := b.Reg.Component(target); ok {
		return p.Manifest.Expose
	}
	return nil
}

// clampEdge sets a read-clamp edge's values: read|block while any leg can be
// clamped, else block alone, naming the first leg's reason.
func clampEdge(e outEdge) outEdge {
	e.values, e.def = clampValues, deployments.EdgeRead
	for _, l := range e.legs {
		if l.why == "" {
			return e
		}
	}
	e.values, e.def, e.why, e.custom = blockValues, deployments.EdgeBlock, e.legs[0].why, e.legs[0].role
	return e
}

// blockedSlotEdge is a stream or lan-ingress slot: raw L4/L3 into the
// provider's primary, which no clamp narrows (§5.7) (D127o).
func blockedSlotEdge(slot, kind string, refs []string) outEdge {
	e := outEdge{id: "slot:" + slot, kind: kind, values: blockValues, def: deployments.EdgeBlock}
	for _, ref := range refs {
		prov, _ := splitRef(ref)
		e.legs = append(e.legs, edgeLeg{to: prov})
	}
	if kind == edgeStream {
		e.why = "stream edges can't be read-clamped"
	} else {
		e.why = "lan-ingress links can't be read-clamped"
	}
	return e
}

// netEdge is c's net slot (§5.8): inherit, the tile's relay policy, by
// default. A net bound to a provider tile is a splice, block alone; a net
// that resolves to host sharing is block whatever is stored, since that can
// change with a network set and no edit of the tile (11-contract §1.7).
func (b *Broker) netEdge(c *registry.Component, slot string) outEdge {
	e := outEdge{id: "slot:" + slot, kind: edgeNet, values: inheritValues, def: deployments.EdgeInherit}
	switch nb := b.netBinding(c.Path); {
	case b.netProvider(c.Path) != "":
		e.legs = []edgeLeg{{to: nb}}
		e.values, e.def = blockValues, deployments.EdgeBlock
		e.why = "net provider splices serve the tile's primary only"
	case b.NetHostShare(c):
		e.forced = true
		e.why = "host networking serves the tile's primary only; non-primary deployments get no egress"
	}
	return e
}

// ---- what an edge reads as ----

// effective is what e is for the tile's non-primary deployments under the
// stored overrides: policy is the stored value, or the default when none is
// (set says which); eff is what applies, with why when it differs from
// policy: a forced edge is block, and so is a value this xbind doesn't know
// or one e doesn't take now (D127s).
func (e outEdge) effective(stored map[string]string) (policy, eff, why string, set bool) {
	v, set := stored[e.id]
	policy = e.def
	if set {
		policy = v
	}
	switch {
	case e.forced:
		return policy, deployments.EdgeBlock, e.why, set
	case slices.Contains(e.values, policy):
		return policy, policy, "", set
	case policy != deployments.EdgeRead && policy != deployments.EdgeBlock && policy != deployments.EdgeInherit:
		return policy, deployments.EdgeBlock, fmt.Sprintf("its stored value %q isn't one this xbind knows, so it reads as block", policy), set
	}
	return policy, deployments.EdgeBlock, fmt.Sprintf("it takes %s now, so its stored %q reads as block", strings.Join(e.values, " or "), policy), set
}

// ---- the verdict ----

// applyEdgePolicy is the verdict on one call that deployment callerDep, not
// caller's primary, makes to target, where caller holds role (09-fabric
// §5.4–§5.9). An own-scope resource is the deployment's own data and no
// edge. Otherwise every edge authorizing the call is read: any block among
// them refuses it, naming that edge (D127s); a role-less inherit passes role;
// read passes reader, the clamp, where every leg's role and the tile's own
// imply reader, and refuses any leg that doesn't (D127f) (D127o).
func (b *Broker) applyEdgePolicy(d Decision, caller, callerDep, target, role string) Decision {
	c, ok := b.Reg.Component(caller)
	if !ok {
		d.Deny = fmt.Errorf("%s's non-primary deployment %q can't call %s: %s isn't a tile", caller, callerDep, target, caller)
		return d
	}
	if strings.HasPrefix(target, "res:") && b.sameScope(c, target) {
		d.Role = role // the deployment's own namespace (08-data §4.2)
		return d
	}
	edges := b.edgesTo(c, target)
	if len(edges) == 0 { // grantedRole found a role no edge explains: fail closed (F6)
		d.Deny = fmt.Errorf("%s's non-primary deployment %q can't call %s: no edge of the tile authorizes it", caller, callerDep, target)
		return d
	}
	stored := b.deploymentEdges(caller)
	for _, e := range edges {
		d.Edges = append(d.Edges, e.id)
	}
	read := false
	for _, e := range edges {
		policy, eff, why, _ := e.effective(stored)
		switch eff { // read only where the leg to target can be clamped (clampEdge)
		case deployments.EdgeInherit:
		case deployments.EdgeRead:
			read = true
		default:
			return b.refuse(d, caller, callerDep, e, blockText(caller, callerDep, target, e, policy, why))
		}
	}
	if !read {
		d.Role = role
		return d
	}
	if l := b.clampLeg(target, role); l.why != "" { // the tile's own role must imply reader too (D127f)
		return b.refuse(d, caller, callerDep, edges[0], blockedText(caller, callerDep, edges[0], l.why, role))
	}
	d.Role, d.Clamped = clampedRole(role), clampedRole(role) != role
	for _, e := range edges {
		if clampedRole(e.legs[0].role) != e.legs[0].role {
			b.tally(caller, callerDep, e.id).clamped.Add(1)
		}
	}
	return d
}

// clampedRole is min(role, reader): the bus aliases stay aliases, so
// publisher clamps to subscriber (08-data §7).
func clampedRole(role string) string {
	if role == "subscriber" || role == "publisher" {
		return "subscriber"
	}
	return "reader"
}

// refuse is d refused by edge e, counted for the panel (NP-09-10).
func (b *Broker) refuse(d Decision, caller, callerDep string, e outEdge, msg string) Decision {
	b.tally(caller, callerDep, e.id).refused.Add(1)
	d.Role, d.Clamped, d.Deny = "", false, errors.New(msg)
	return d
}

// blockText is a refusal by e's value (§5.6): stored, its default, or read
// as block. An edge nothing unblocks says so instead of sending the reader
// to the panel (D127o).
func blockText(caller, dep, target string, e outEdge, policy, why string) string {
	if slices.Equal(e.values, blockValues) || e.forced {
		return blockedText(caller, dep, e, e.why, e.custom)
	}
	edge := e.id
	if e.id != "grant:"+target {
		edge += " to " + target
	}
	if why != "" {
		return fmt.Sprintf("%s's non-primary deployment %q may not use edge %s: %s. A tile manager can change it in the Deployments panel.",
			caller, dep, edge, why)
	}
	return fmt.Sprintf("%s's non-primary deployment %q may not use edge %s: the tile's edge policy for it is %q. A tile manager can change it in the Deployments panel.",
		caller, dep, edge, policy)
}

// blockedText is a refusal by an edge nothing unblocks in this release
// (§5.5, §5.6) (D127o): a custom role the provider could make clampable by
// declaring what it implies, or anything else that works from the primary.
func blockedText(caller, dep string, e outEdge, why, custom string) string {
	if custom != "" && e.kind != edgeCapability {
		return fmt.Sprintf("%s's non-primary deployment %q can't use edge %s: %s, so non-primary deployments are blocked from it. "+
			"Test this from the primary, or ask the provider to declare what %s implies.", caller, dep, e.id, why, custom)
	}
	return fmt.Sprintf("%s's non-primary deployment %q can't use edge %s: %s, so non-primary deployments are blocked from it, "+
		"and no edge-policy value unblocks it in this release. The call works from the primary.", caller, dep, e.id, why)
}

// ---- principals ----

// nonPrimary reports whether p is a non-primary principal (09-fabric §0): a
// tile principal, from an instance, frame or terminal token, whose addressed
// deployment isn't its tile's primary, named by dep. A bound deployment that
// no longer exists, or a session that may target none, is an error: that
// credential acts nowhere. Anyone else (people, deliveries, the broker's own
// checks of a tile's authority) is no non-primary principal.
func (b *Broker) nonPrimary(p auth.Principal) (dep string, yes bool, err error) {
	switch {
	case p.Component == "":
		return "", false, nil
	case p.Via != "instance" && p.Via != "frame" && p.Via != "terminal":
		return "", false, nil
	}
	if dep, err = b.addressed(p, p.Component); err != nil {
		return "", false, err
	}
	return dep, !b.isPrimary(p.Component, dep), nil
}

// actsInPrimary is Policy's self rule (D127g): Policy reaches the tile's
// primary, so a principal of the tile is admin there unless it is a
// non-primary one, whose self-calls are Route's.
func (b *Broker) actsInPrimary(p auth.Principal) bool {
	_, np, err := b.nonPrimary(p)
	return err == nil && !np
}

// policyRole is Policy's answer for another tile or a person: resolveTarget's
// role, grantedRole's for all but a non-primary principal, whose calls take
// the edge policy's verdict.
func (b *Broker) policyRole(p auth.Principal, target string) (string, bool) {
	dep, np, err := b.nonPrimary(p)
	if err != nil {
		return "", false
	} else if !np {
		dep = b.primaryOf(p.Component)
	}
	d := b.resolveTarget(p.Component, dep, target)
	return d.Role, d.Deny == nil
}

// resEdge is allowRes's edge check on a resource p's tile holds a role on
// (08-data §7): nothing for anyone but a non-primary principal; for one, the
// verdict, and want against the clamped role, so a write to another scope
// is refused naming the policy.
func (b *Broker) resEdge(p auth.Principal, res, want string) error {
	dep, np, err := b.nonPrimary(p)
	if err != nil || !np {
		return err
	}
	d := b.resolveTarget(p.Component, dep, res)
	switch {
	case d.Deny != nil:
		return d.Deny
	case !roleSatisfies(d.Role, want, nil):
		return fmt.Errorf("%s+%s may not write %s: non-primary deployments reach other scopes read-only (edge policy %q)",
			p.Component, dep, res, deployments.EdgeRead)
	}
	return nil
}

// allowResUnclamped is allowRes without the read clamp, for registering on
// a foreign cron resource, which only ever schedules the deployment's own
// handler (09-fabric §6; NP-09-18): a non-primary principal is checked at
// its tile's authority, and the edge's block is depEdge's.
func (b *Broker) allowResUnclamped(p auth.Principal, target, want string) error {
	if _, np, err := b.nonPrimary(p); err != nil {
		return err
	} else if np {
		p = auth.Principal{Component: p.Component}
	}
	return b.allowRes(p, target, want)
}

// codeReadAllowed is codeGrantAllows for principal p (09-fabric §5.1's code
// edges): today's answer for anyone but a non-primary principal; for one,
// grant:code and grant:code:<target> through the edge policy, where either
// at block refuses the read though the other would allow it (D127s).
func (b *Broker) codeReadAllowed(p auth.Principal, target string) bool {
	dep, np, err := b.nonPrimary(p)
	switch {
	case err != nil:
		return false
	case !np:
		return p.Component != "" && b.codeGrantAllows(p.Component, target)
	}
	ok := false
	for _, t := range []string{"code", "code:" + target} {
		d := b.resolveTarget(p.Component, dep, t)
		var ng *NotGrantedError
		switch {
		case errors.As(d.Deny, &ng): // no such grant: not an edge of this read
		case d.Deny != nil:
			return false
		case roleSatisfies(d.Role, "reader", nil):
			ok = true
		}
	}
	return ok
}

// governanceRole is the role p's tile holds on a governance target (xbin,
// xbin:*) for the gates that read one: none for a non-primary principal, or
// one whose deployment is gone, whatever the grant table says (06-security
// T14) (D127k).
func (b *Broker) governanceRole(p auth.Principal, target string) (string, bool) {
	if _, np, err := b.nonPrimary(p); err != nil || np {
		return "", false
	}
	return b.grantedRole(p.Component, target)
}

// xbinGrantRefusal refuses approving a governance grant for a tile that has
// non-primary deployments (06-security T14 item 2) (D127k): such a tile stays
// single-deployment. A tile with only main, live reload paused or not, may
// hold one.
func (b *Broker) xbinGrantRefusal(g registry.Grant) error {
	if g.Target != "xbin" && !strings.HasPrefix(g.Target, "xbin:") {
		return nil
	}
	if _, names := b.deploymentsOf(g.From); len(names) > 1 {
		return fmt.Errorf("%s has non-primary deployments: remove them before granting it %s", g.From, g.Target)
	}
	return nil
}

// ---- edges applied at spawn ----

// viewGrant is grantedRole for the generation the runner view c describes
// (c.Deployment "" is the primary's): the primary's exactly as today; a
// non-primary deployment's through the edge policy, so a capability edge at
// block, gpu:* by default, withholds the grant from its sandbox (§5.9).
func (b *Broker) viewGrant(c *registry.Component, target string) (string, bool) {
	if c.Deployment == "" {
		return b.grantedRole(c.Path, target)
	}
	d := b.resolveTarget(c.Path, c.Deployment, target)
	return d.Role, d.Deny == nil
}

// viewNetInherits reports whether the generation view c describes takes
// the tile's relay policy: the primary always; a non-primary deployment
// while NetEdge is inherit. Otherwise it spawns with no egress, as a none
// binding does (§5.8).
func (b *Broker) viewNetInherits(c *registry.Component) bool {
	if c.Deployment == "" {
		return true
	}
	v, _ := b.NetEdge(c.Path)
	return v == deployments.EdgeInherit
}

// NetEdge is the net edge's effective value for tile's non-primary
// deployments (09-fabric §5.8): inherit (the tile's relay policy, in the
// deployment's own network namespace) or block, with why when that isn't
// the stored or default value: a net that shares the host's, a provider
// splice, a value this xbind doesn't know. A tile without a net slot has
// nothing to inherit and answers inherit.
func (b *Broker) NetEdge(tile string) (value, why string) {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return deployments.EdgeBlock, tile + " isn't a tile"
	}
	slot, has := netIfaceSlot(c)
	if !has {
		return deployments.EdgeInherit, ""
	}
	e := b.netEdge(c, slot)
	_, eff, why, _ := e.effective(b.deploymentEdges(tile))
	if why == "" && eff == deployments.EdgeBlock {
		why = e.why
	}
	return eff, why
}

// StreamDialAllowed is the check at each dial through a stream slot's
// gateway forward, for the generation view c describes (§5.7): nil for the
// primary; a non-primary deployment is refused, counted, naming the edge,
// since stream edges are blocked with no override (D127o). The runner closes
// the dial and writes the error to that deployment's log.
func (b *Broker) StreamDialAllowed(c *registry.Component, slot string) error {
	if c.Deployment == "" {
		return nil
	}
	b.tally(c.Path, c.Deployment, "slot:"+slot).refused.Add(1)
	return fmt.Errorf("stream slot %s blocked by edge policy (edge slot:%s)", slot, slot)
}

// EdgeChangeRestarts reports whether a change of edge's policy takes effect
// only at spawn, so the tile's non-primary deployments restart for it (the
// net slot, capability grants; NP-09-6). The primary never restarts for an
// edge-policy change.
func (b *Broker) EdgeChangeRestarts(tile, edge string) bool {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return false
	}
	for _, e := range b.tileEdges(c) {
		if e.id == edge {
			return e.kind == edgeNet || e.kind == edgeCapability
		}
	}
	return false
}

// ---- the panel's view, and writes ----

// edgeTally is one (tile, deployment, edge)'s refused calls, and the calls
// it passed with the role clamped to reader, since xbind started.
type edgeTally struct{ refused, clamped atomic.Int64 }

func (b *Broker) tally(tile, dep, edge string) *edgeTally {
	k := tile + "\x00" + dep + "\x00" + edge
	if v, ok := b.edgeTallies.Load(k); ok {
		return v.(*edgeTally)
	}
	v, _ := b.edgeTallies.LoadOrStore(k, &edgeTally{})
	return v.(*edgeTally)
}

// edgeCounts sums an edge's tallies over the tile's deployments.
func (b *Broker) edgeCounts(tile, edge string) (refused, clamped int64) {
	pre := tile + "\x00"
	b.edgeTallies.Range(func(k, v any) bool {
		s := k.(string)
		if strings.HasPrefix(s, pre) && strings.HasSuffix(s, "\x00"+edge) && strings.Count(s, "\x00") == 2 {
			refused += v.(*edgeTally).refused.Load()
			clamped += v.(*edgeTally).clamped.Load()
		}
		return true
	})
	return refused, clamped
}

// EdgesOf is tile's outbound edges with their policy for its non-primary
// deployments, for the deployments state (11-contract §1.1 Edge; NP-11-32),
// sorted by id. A stored override whose edge is no longer present is listed
// too, inert, with no values: it applies again when the edge returns
// (09-fabric §5.2).
func (b *Broker) EdgesOf(tile string) []deployments.Edge {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return []deployments.Edge{}
	}
	stored := b.deploymentEdges(tile)
	out := []deployments.Edge{}
	present := map[string]bool{}
	for _, e := range b.tileEdges(c) {
		present[e.id] = true
		policy, eff, why, set := e.effective(stored)
		ed := deployments.Edge{ID: e.id, Kind: e.kind, Policy: policy, Default: e.def, Values: slices.Clone(e.values), Set: set}
		ed.To, ed.Role = legsOf(e.legs)
		switch {
		case eff != policy:
			ed.Effective, ed.Why = eff, why
		case slices.Equal(e.values, blockValues):
			ed.Why = e.why
		}
		ed.Refused, ed.Clamped = b.edgeCounts(tile, e.id)
		out = append(out, ed)
	}
	for _, id := range slices.Sorted(maps.Keys(stored)) {
		if !present[id] {
			ed := deployments.Edge{ID: id, Policy: stored[id], Values: []string{}, Set: true,
				Why: "no longer present on the tile: the override is kept and applies again if the edge returns"}
			ed.Refused, ed.Clamped = b.edgeCounts(tile, id)
			out = append(out, ed)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// legsOf is an edge's providers and their roles, as Edge.to and Edge.role
// list them.
func legsOf(legs []edgeLeg) (to, role string) {
	var tos, roles []string
	for _, l := range legs {
		tos = append(tos, l.to)
		if l.role != "" && !slices.Contains(roles, l.role) {
			roles = append(roles, l.role)
		}
	}
	return strings.Join(tos, ", "), strings.Join(roles, ", ")
}

// ValidateEdgePolicy is the check a write of policy to tile's edge passes
// (09-fabric §5.2; 11-contract §1.7, §1.14): the edge must be present on the
// tile now, and the value one it takes; default removes an override, also
// one whose edge is gone. It is a *deployments.Error: 404 for an edge the
// tile doesn't have, 400 for a value the edge doesn't take (an edge nothing
// narrows takes block alone: no override, D127o). Who may write is the
// deployments plane's (a tile manager in a human session).
func (b *Broker) ValidateEdgePolicy(tile, edge, policy string) error {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return &deployments.Error{Status: http.StatusNotFound, Msg: "no such tile: " + tile}
	}
	var e *outEdge
	for _, x := range b.tileEdges(c) {
		if x.id == edge {
			e = &x
			break
		}
	}
	if policy == deployments.EdgeDefault {
		if _, stored := b.deploymentEdges(tile)[edge]; e != nil || stored {
			return nil
		}
	}
	if e == nil {
		return &deployments.Error{Status: http.StatusNotFound, Msg: fmt.Sprintf("%s has no edge %s", tile, edge)}
	}
	if slices.Contains(e.values, policy) {
		return nil
	}
	var reason string
	switch {
	case slices.Equal(e.values, blockValues) && e.custom != "":
		reason = fmt.Sprintf("a custom role with no path to reader can't be read-clamped — %s, so non-primary deployments are blocked from it with no override", e.why)
	case slices.Equal(e.values, blockValues):
		reason = e.why + ", so non-primary deployments are blocked from it with no override"
	case policy == "match":
		reason = "match isn't accepted by this xbind"
	case policy == deployments.EdgeInherit:
		reason = "inherit is for the net slot and capability grants, which carry no role"
	case policy == deployments.EdgeRead:
		reason = "read clamps a role, and this edge carries none"
	default:
		reason = fmt.Sprintf("%q is not an edge-policy value", policy)
	}
	return &deployments.Error{Status: http.StatusBadRequest,
		Msg: fmt.Sprintf("%s takes %s: %s", edge, strings.Join(e.values, " or "), reason)}
}
