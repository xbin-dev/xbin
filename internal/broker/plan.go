package broker

// plan.go — a plan's lines (docs/protocol/delegation.md §Plans;
// plans/wsagent/journey.md §6c). A plan is a change set a person's agent
// proposes for multi-step work: beside the admin kinds (changeset.go) it
// may hold
//
//	tile.create {template, path, owner?}        POST /templates/new
//	agent.start {tile, brief, harness?, mode?}  POST /term/sessions (the server's: it knows sessions)
//
// and a later line may name a tile an earlier tile.create line of the same
// set makes. Such a line can't be judged against a tile that isn't there
// yet, so it is described from the template's manifest — its slots, what
// it provides, whether instances keep each person's data apart — with the
// earlier creations taken as made (ChangePreview.Deferred), and judged for
// real, by the handler's own check, when it is applied: after the tile
// exists. What it then says must be what the person was shown — the same
// line, and no warning they didn't see.
//
// Each line is PERSONAL or an ADMIN's (ChangePreview.Admin). Personal is
// what the person could do with their own hands and no admin:
//
//   - tile.create where their own creation rule lets them (the rule a
//     delegate's ticketed POST /templates/new meets: resolveCreateOwner and
//     canCreateAt, clamped — never an admin's);
//   - binding.set / binding.remove of a tile they own, to providers they
//     own or their own allowance covers (selfMayBind, D88) — never the LLM
//     connector or a named network set, which are a workspace admin's;
//   - agent.start (the server's rule: their terminal level on the tile).
//
// Everything else — a grant, an org's share, a limit, a binding by an
// admin's right — is an admin's line. The server applies a personal line
// only while this file still says it is one (the property
// TestPersonalLinesNeedNoAdmin holds: a personal line applies by hand for
// the same person with no admin right).

import (
	"cmp"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/jsonc"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// The kinds a plan adds.
const (
	ChangeTileCreate = delegation.KindTileCreate
	ChangeAgentStart = delegation.KindAgentStart
)

// PlanKinds lists the kinds a plan may hold: the admin kinds, a tile's
// creation and a tile agent's start.
func PlanKinds() []string {
	return append([]string{ChangeTileCreate, ChangeAgentStart}, ChangeKinds()...)
}

// planClamp stands for the workspace agent in a principal a line is judged
// as "the person, with no admin right": a delegate is never an admin
// (auth.Principal.IsAdmin), whoever its person is.
const planClamp = "plan"

func clamped(p auth.Principal) auth.Principal {
	if p.UserID != "" {
		p.Agent = planClamp
	}
	return p
}

func llmSpendWarning(comp string) string {
	return "SPEND: " + comp + "'s calls to the LLM connector spend the workspace's LLM quota, on the models the connector serves — and send them whatever " + comp + " sends"
}

// plannedTile is the planned tile at path, if an earlier line creates one.
func plannedTile(planned []delegation.PlannedTile, tile string) (delegation.PlannedTile, bool) {
	tile = strings.Trim(tile, "/")
	for _, t := range planned {
		if t.Path == tile && tile != "" {
			return t, true
		}
	}
	return delegation.PlannedTile{}, false
}

// plannedIn are the lines whose planned tiles refs name (a ref may carry
// an #instance), in order, each once.
func plannedIn(planned []delegation.PlannedTile, refs ...string) []int {
	var out []int
	for _, ref := range refs {
		prov, _ := splitRef(ref)
		if t, ok := plannedTile(planned, prov); ok && !slices.Contains(out, t.N) {
			out = append(out, t.N)
		}
	}
	slices.Sort(out)
	return out
}

// plannedComponent is planned tile t as the component it will be: its
// path, and its template's manifest.
func plannedComponent(t delegation.PlannedTile) *registry.Component {
	c := &registry.Component{Path: t.Path}
	_ = jsonc.Unmarshal(t.Manifest, &c.Manifest)
	return c
}

// bindingTiles are the existing tiles a binding line names: the tile whose
// slot it is, and every provider that is a tile. A line is judged by all
// of them — a binding lets the consumer's code, and a tile agent started
// in it, reach the provider.
func (b *Broker) bindingTiles(body bindingBody) []string {
	out := []string{body.Component}
	for _, ref := range append(slices.Clone(body.Providers), body.Provider) {
		prov, _ := splitRef(ref)
		if _, ok := b.Reg.Component(prov); ok && prov != "" && !slices.Contains(out, prov) {
			out = append(out, prov)
		}
	}
	return out
}

// bindPersonal reports whether the binding change plan — which bindingCheck
// let p make — is one the tile's owner makes with no admin right
// (selfMayBind): never the LLM connector or a named set.
func (b *Broker) bindPersonal(p auth.Principal, body bindingBody, plan bindingPlan) bool {
	for _, e := range plan.delta {
		if !plan.del && (e.Ref == LLMRef || strings.HasPrefix(e.Ref, NetRefSet)) {
			return false
		}
	}
	return b.selfMayBind(p, body.Component, body.Slot, plan.delta, plan.del)
}

// createsIn reports whether user is a member of org with its Create knob
// (or its admin): their own right to create there, whatever else they are.
func (b *Broker) createsIn(user, org string) bool {
	if b.Users == nil || user == "" {
		return false
	}
	for _, m := range b.Users.UserOrgs(user) {
		if m.ID == org && !m.Suspended && (m.Create || m.Admin) {
			return true
		}
	}
	return false
}

// previewTileCreate describes tile.create {template, path, owner?}: a new
// tile from a template, its person's — or an org's they may create in.
func (b *Broker) previewTileCreate(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error) {
	var a struct {
		Template string `json:"template"`
		Path     string `json:"path"`
		Owner    string `json:"owner"`
	}
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	a.Path = strings.Trim(a.Path, "/")
	if err := lineNames("template", a.Template, "path", a.Path, "owner", a.Owner); err != nil {
		return delegation.ChangePreview{}, err
	}
	if a.Template == "" || a.Path == "" {
		return delegation.ChangePreview{}, errors.New("tile.create names a template and the new tile's path")
	}
	self := ""
	if p.UserID != "" {
		self = users.OwnerKindUser + ":" + p.UserID
	}
	owner := a.Owner
	switch {
	case owner == "":
		owner = self // the person's, for an admin too
	case owner == self, strings.HasPrefix(owner, users.OwnerKindOrg+":"):
	default:
		return delegation.ChangePreview{}, errors.New("a plan makes a tile its person's, or an org's they may create in (owner: \"org:<id>\") — never another person's")
	}
	body := templateNewBody{Source: a.Template, Path: a.Path, Owner: owner}
	pv := delegation.ChangePreview{Call: delegation.AdminCall{Method: http.MethodPost, Path: "/templates/new", Body: mustJSON(body)},
		Before: mustJSON(map[string]any{"tile": nil}), BeforeText: "nothing at " + a.Path,
		UndoByHand: "xbin doesn't delete tiles: deleting " + a.Path + " is yours to do, from the tile's own menu"}
	whose := "yours"
	switch {
	case strings.HasPrefix(owner, users.OwnerKindOrg+":"):
		whose = "owned by the org " + strings.TrimPrefix(owner, users.OwnerKindOrg+":")
	case owner == "":
		whose = "the workspace's"
	}
	tpl := a.Template
	if b.templates != nil {
		if t, ok := b.templates.Get(a.Template); ok && t.Title != "" {
			tpl = t.Title + " (" + a.Template + ")" // a builtin's title is this release's own text
		}
	}
	pv.Line = fmt.Sprintf("Create the tile %s from the template %s — %s", a.Path, tpl, whose)
	pv.After, pv.AfterText = mustJSON(map[string]any{"tile": a.Path, "owner": owner, "template": a.Template}), "a new tile, "+whose
	for _, t := range planned {
		if t.Path == a.Path || strings.HasPrefix(t.Path, a.Path+"/") || strings.HasPrefix(a.Path, t.Path+"/") {
			pv.Blocked = fmt.Sprintf("line %d of this plan already creates %s: a tile is not made inside another, or twice", t.N, t.Path)
			return pv, nil
		}
	}
	// the person's own creation rule first; an admin's right only when that refuses
	plan, why := b.templateNewCheckFor(clamped(p), body, true)
	if org, isOrg := strings.CutPrefix(owner, users.OwnerKindOrg+":"); why == nil && isOrg && !b.createsIn(p.UserID, org) {
		// an admin's access says yes to every org; creating in one they hold
		// no Create in is an admin's act, not their own
		why = &refused{http.StatusForbidden, "creating tiles owned by org \"" + org + "\" needs the org's Create permission (or org admin)", "/docs/auth.md"}
	}
	if why != nil {
		// (quiet both times: the line is read by the agent's conversation too)
		if full, whyFull := b.templateNewCheckFor(p, body, true); whyFull == nil {
			plan, why, pv.Admin = full, nil, true
		} else {
			why = whyFull
		}
	}
	if why != nil {
		pv.Blocked = why.msg
		return pv, nil
	}
	created := delegation.PlannedTile{Path: plan.target, Template: a.Template, Owner: plan.owner}
	if opts, _ := instanceOpts(plan.def, nil); plan.def != nil && opts.Partition {
		created.Partitioned = slices.Contains(plan.def, "user")
		pv.Effects = append(pv.Effects, "it keeps each person's data apart ("+strings.Join(plan.def, ", ")+")")
	}
	var m registry.Manifest
	tplPath, ownScope := "", false
	switch {
	case plan.builtin:
		created.Manifest, _ = b.templates.Manifest(a.Template)
		_ = jsonc.Unmarshal(created.Manifest, &m)
		if t, ok := b.templates.Get(a.Template); ok {
			tplPath = t.DefaultPath
		}
		ownScope = b.templates.Has(a.Template, "scope.json")
	default:
		m = plan.src.Manifest
		created.Manifest = mustJSON(m)
		tplPath, ownScope = plan.src.Path, plan.src.Scope == plan.src.Path
		pv.Tiles = []string{plan.src.Path} // the template is a tile: copying it reads it
	}
	// what the new tile has from its first second is the template's to say,
	// and a template that is a tile can be edited: xbind reads its manifest
	// now, says what it gives, and pins the line to it
	effects, warnings := b.createEffects(m, tplPath, plan.target, ownScope)
	pv.Effects, pv.Warnings = append(pv.Effects, effects...), warnings
	pv.Pin = delegation.BodyHash(created.Manifest)
	pv.Creates = &created
	return pv, nil
}

// createEffects says what a tile made at target from a template with
// manifest m (authored at tplPath) has from the moment it exists: its own
// resources; what its code asks for, which waits for an approval — except
// a use of something in the scope it lands in, which is its own grant
// (hasGrant's same-scope rule): a warning, since the line then gives
// access by itself; what it offers to tiles bound to it; and endpoints it
// declares for the outside. ownScope: the template brings a scope of its
// own, so the new tile shares one with nothing.
func (b *Broker) createEffects(m registry.Manifest, tplPath, target string, ownScope bool) (effects, warnings []string) {
	scope := ""
	if !ownScope {
		scope = b.scopeAt(target)
	}
	own := func(t string) bool { return t == target || strings.HasPrefix(t, target+"/") }
	// full: a use's target as written (res:<scope>/<name>, a tile's path); bare: the path it names
	inScope := func(full, bare string) bool {
		if scope == "" {
			return false
		}
		if rt, _, ok := b.parseRes(full); ok {
			return rt.Scope == scope
		}
		tc, ok := b.Reg.Component(bare)
		return ok && tc.Scope == scope
	}
	mine, asks := 0, []string{}
	for _, u := range m.Uses {
		t := u.Target
		if tplPath != "" { // the copy's own names follow it to its new place (CopyTree)
			t = strings.ReplaceAll(t, tplPath, target)
		}
		full, _, _ := strings.Cut(t, "#")
		bare := strings.TrimPrefix(strings.TrimPrefix(full, "res:"), "code:")
		switch {
		case own(bare):
			mine++
		case inScope(full, bare):
			warnings = append(warnings, fmt.Sprintf("ACCESS: its code can use %s as %s from the moment it exists: it lands in the scope %s, where declaring a use is the grant", t, u.Role, scope))
		default:
			asks = append(asks, t+" as "+u.Role)
		}
	}
	if mine > 0 {
		effects = append(effects, fmt.Sprintf("it keeps its own data (%d of its own resources)", mine))
	}
	if len(asks) > 0 {
		slices.Sort(asks)
		effects = append(effects, "its code asks for "+strings.Join(asks, ", ")+": that waits for an approval of its own")
	}
	var offers, outside []string
	for name, def := range m.Provides {
		offers = append(offers, cmp.Or(def.Service, name))
	}
	for name := range m.Exposes {
		outside = append(outside, name)
	}
	slices.Sort(offers)
	slices.Sort(outside)
	if len(offers) > 0 {
		effects = append(effects, "it offers "+strings.Join(offers, ", ")+" to tiles bound to it")
	}
	if len(outside) > 0 {
		warnings = append(warnings, "EXPOSES: it declares "+strings.Join(outside, ", ")+" for the outside: nothing reaches it until it is bound, and binding it is a line of its own")
	}
	if len(warnings) == 0 {
		effects = append(effects, "it starts with nothing bound, and with no access to another tile")
	}
	return effects, warnings
}

// previewPlannedBinding describes a binding that names a tile an earlier
// line of the plan creates (uses: those lines), from the templates'
// manifests. Only what a new tile needs wired is planned: an http slot,
// to tiles and the LLM connector, replacing the slot. The handler's own
// check runs when the line is applied, after the tile exists.
func (b *Broker) previewPlannedBinding(p auth.Principal, pv delegation.ChangePreview, body bindingBody, remove bool, planned []delegation.PlannedTile, starts []delegation.PlannedStart, uses []int) (delegation.ChangePreview, error) {
	const later = " — a plan binds a new tile's http slots, to tiles and to the LLM connector; propose this once the tile exists"
	switch {
	case remove:
		return pv, errors.New("binding.remove names a tile this plan only creates: there is nothing bound on it yet")
	case body.Add || body.Host != "" || body.Zone != "" || body.Listen != "":
		return pv, errors.New("a route (host, zone, listen, add) is for an endpoint of a tile that exists" + later)
	}
	pv.Uses, pv.Deferred = uses, true
	for _, t := range b.bindingTiles(body) { // the tiles that exist already: either end
		if _, ok := b.Reg.Component(t); ok {
			pv.Tiles = append(pv.Tiles, t)
		}
	}
	comp, compPlanned := plannedTile(planned, body.Component)
	var c *registry.Component
	if compPlanned {
		c = plannedComponent(comp)
	} else if cc, ok := b.Reg.Component(body.Component); ok {
		c = cc
	} else {
		pv.Blocked = "no such component: " + body.Component
		return pv, nil
	}
	refs := body.Providers
	if len(refs) == 0 {
		refs = []string{body.Provider}
	}
	cur := b.Reg.Workspace().Bindings[body.Component][body.Slot] // a provider that is planned, on a tile that exists
	pv.Before, pv.BeforeText = mustJSON(map[string]any{"binding": refsOf(cur)}), bindingWords(cur)
	next := registry.BindTo(refs...)
	pv.After, pv.AfterText = mustJSON(map[string]any{"binding": refsOf(next)}), bindingWords(next)
	block := func(format string, args ...any) (delegation.ChangePreview, error) {
		pv.Blocked, pv.After, pv.AfterText = fmt.Sprintf(format, args...), pv.Before, pv.BeforeText
		pv.Admin = true // nothing vouches for it: never a personal line
		return pv, nil
	}
	def, ok := c.Manifest.Interfaces[body.Slot]
	switch {
	case !ok:
		return block("%s does not request an interface slot %q (and exposes none by that name)", body.Component, body.Slot)
	case def.Kind != "http":
		return block("%s's %s is a %s slot%s", body.Component, body.Slot, def.Kind, later)
	case len(refs) > 1 && !def.Multi:
		return block("slot %q takes a single binding (multi-input needs {kind:http, multi:true})", body.Slot)
	}
	// personal: the tile is (will be) the person's own, and so is every
	// provider — what selfMayBind lets a personal tile's owner wire (D88)
	self := ""
	if b.Users != nil && p.User != nil && !p.User.Disabled && p.UserID != "" {
		self = users.OwnerKindUser + ":" + p.UserID
	}
	owns := func(tile string) bool {
		if t, ok := plannedTile(planned, tile); ok {
			return self != "" && t.Owner == self
		}
		return self != "" && b.Users.Owner(providerPath(tile)) == self
	}
	personal := owns(body.Component)
	seen := map[string]bool{}
	var wait *delegation.PlanWait
	for _, ref := range refs {
		prov, inst := splitRef(ref)
		switch {
		case seen[ref]:
			return block("duplicate binding %q", ref)
		case prov == body.Component:
			return block("a component can't be its own provider")
		}
		seen[ref] = true
		if handled, err := validateHTTPBuiltin(ref, def); handled { // xbin:llm
			if err != nil {
				return block("%s", err.Error())
			}
			personal = false // the LLM connector is a workspace admin's to bind (D226)
			pv.Effects = append(pv.Effects, body.Component+"'s code, and everyone who can change it, can call the workspace's LLM connector")
			pv.Warnings = append(pv.Warnings, llmSpendWarning(body.Component))
			continue
		}
		var pc *registry.Component
		pt, provPlanned := plannedTile(planned, prov)
		switch {
		case provPlanned:
			pc = plannedComponent(pt)
		default:
			if pc, ok = b.Reg.Component(prov); !ok {
				return block("no such provider: %s", prov)
			}
			if power := b.powerOf(prov); len(power) > 0 {
				pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s holds %s: whatever its API does with that, %s can now ask it to do", prov, strings.Join(power, ", "), body.Component))
			}
		}
		personal = personal && owns(prov)
		pd, ok := httpProvideFor(pc, def.Service)
		if st, built := startIn(starts, prov); !ok && built && def.Service != "" && providesNone(pc, def.Service) {
			// the tile provides nothing of the kind yet, and an earlier line
			// starts a tile agent in it: the line waits for what that agent
			// writes, and is pinned to exactly this
			if len(refs) != 1 {
				return block("a line that waits for %s to provide %s binds that one provider: put the others on a line of their own", prov, def.Service)
			}
			wait = &delegation.PlanWait{Component: body.Component, Slot: body.Slot, Tile: prov, Kind: "http", Service: def.Service, Role: delegation.WaitRole}
			if st.N > 0 && !slices.Contains(pv.Uses, st.N) {
				pv.Uses = append(pv.Uses, st.N)
				slices.Sort(pv.Uses)
			}
			continue
		}
		switch {
		case !ok && def.Service != "":
			return block("%s does not provide the %q service the slot wants", prov, def.Service)
		case !ok:
			return block("%s does not provide an http interface", prov)
		case pd.Instances || inst != "":
			return block("%s is bound by instance (%s#<instance>)%s", prov, prov, later)
		}
		pv.Effects = append(pv.Effects, body.Component+"'s code, and everyone who can change it, can call "+prov)
		// both ends keep each person's data apart: the approval warning of a call grant (partitionGrantWarning)
		fromPart := compPlanned && comp.Partitioned
		if !compPlanned {
			_, fromPart, _ = b.tilePartitioning(body.Component)
		}
		toPart := provPlanned && pt.Partitioned || !provPlanned && b.edgeTile(prov) != ""
		if fromPart && toPart {
			if warn := b.partitionEdgeWarning(body.Component, prov); !slices.Contains(pv.Warnings, warn) {
				pv.Warnings = append(pv.Warnings, warn)
			}
		}
	}
	pv.Effects = append([]string{body.Component + "'s backend restarts"}, pv.Effects...)
	if wait != nil && !personal {
		// only a person's own binding waits: one that takes an admin's
		// rights is approved with a recent sign-in, and that sign-in must
		// not reach 24 hours ahead. It is a line of a later review.
		later := fmt.Sprintf("%s provides no %s yet, and this binding takes an admin's rights — only a binding between your own tiles may wait for a tile to be built. Propose it again, in a plan of its own, once %s provides %s",
			wait.Tile, wait.Service, wait.Tile, wait.Service)
		pv, _ = block("%s", later)
		pv.Later = later
		return pv, nil
	}
	if wait != nil {
		pv.Waits, pv.Deferred = wait, true
		pv.Pin = delegation.BodyHash(mustJSON(wait))
		pv.AfterText = wait.Tile + ", once it provides " + wait.Service
		pv.Effects = append(waitWords(*wait), fmt.Sprintf("once bound: %s's backend restarts, and its code, and everyone who can change it, can call %s as %s", body.Component, wait.Tile, wait.Role))
	}
	pv.Admin = !personal
	if pv.Admin && !b.IsAdmin(p) && !(p.Access != nil && len(p.Access.AdminOrgs()) > 0) {
		// the refusal POST /bindings gives anyone who is neither the owner within their own property nor an admin
		pv.Blocked = "not approvable by you — bindings are wired by a workspace admin, an org admin within their org's allowance (D26), or a personal tile's owner within their own allowance (D88)"
	}
	if len(cur) == 0 {
		pv.Inverse = &delegation.ChangeOp{Kind: ChangeBindingRemove, Args: mustJSON(map[string]string{"component": body.Component, "slot": body.Slot})}
	}
	return pv, nil
}

// readOnlyWords is what a plan's line says of a tile whose agent level is
// "read", so the person knows what they tick.
const readOnlyWords = "this tile's author marked it read-only for agents: this changes how it is wired, not its data"

// readOnlyNote adds readOnlyWords to pv's effects for each tile the line
// names whose agent level in force is "read" (D238: the level governs an
// agent's calls to the tile's API and its reads of the tile's data;
// managing the tile — its bindings, a grant, a start of its own coding
// agent — is the person's right and their approval on xbin's review).
func (b *Broker) readOnlyNote(pv *delegation.ChangePreview) {
	for _, t := range pv.Tiles {
		c, ok := b.Reg.Component(t)
		if !ok {
			continue
		}
		if inForce, _ := b.agentLevel(c); inForce == registry.AgentRead {
			pv.Effects = append(pv.Effects, t+": "+readOnlyWords)
		}
	}
}

// AgentLineAllows is why a plan's line may not name tile for user's agent
// holder (nil: it may). The decision (D238): a tile's "agent" level governs
// an agent's CALLS to the tile's API and its reads of the tile's data —
// "read": GET only; "full": what the person's level allows. MANAGING a
// tile — its bindings, a grant, creating it, starting its own coding agent
// — is governed by the person's rights and their approval on xbin's
// review, and a line says so of a "read" tile (readOnlyNote). "off"
// refuses both, on either side of a line: a tile that turns agents away
// takes no line about it, as consumer or provider, source or target. The
// policy ceiling and the tile's orgs' agent rules must let the agent in
// too, on the model its conversation is on.
func (b *Broker) AgentLineAllows(holder, user, model, tile string) error {
	c, ok := b.Reg.Component(tile)
	if !ok {
		return nil // nothing there: the line's own check says so
	}
	if inForce, _ := b.agentLevel(c); inForce == registry.AgentOff {
		return fmt.Errorf("%s doesn't let a person's agent use it (its agent level is \"off\")", tile)
	}
	if msg := b.ceilingBlockMsg(holder, tile); msg != "" {
		return fmt.Errorf("%s's agent can't change %s: %s", user, tile, msg)
	}
	return b.agentPolicy().TileAllows(delegation.Subject{Holder: holder, User: user, Tile: tile, Model: model})
}

// startIn is the earlier agent.start line of the plan that starts a tile
// agent in tile.
func startIn(starts []delegation.PlannedStart, tile string) (delegation.PlannedStart, bool) {
	for _, st := range starts {
		if st.Tile == tile {
			return st, true
		}
	}
	return delegation.PlannedStart{}, false
}

// providesNone: c declares no provide of service at all — of any kind,
// under any role. (One that declares it differently than a slot wants is
// refused as ever; only nothing-yet waits.)
func providesNone(c *registry.Component, service string) bool {
	for name, def := range c.Manifest.Provides {
		if def.Service == service || name == service {
			return false
		}
	}
	return true
}

// waitsOn: a binding names one existing provider that an earlier line of
// the plan starts a tile agent in and that provides nothing of what the
// slot wants yet.
func (b *Broker) waitsOn(body bindingBody, starts []delegation.PlannedStart) bool {
	if len(starts) == 0 || body.Provider == "" && len(body.Providers) != 1 {
		return false
	}
	c, ok := b.Reg.Component(body.Component)
	if !ok {
		return false
	}
	def, ok := c.Manifest.Interfaces[body.Slot]
	if !ok || def.Kind != "http" || def.Service == "" {
		return false
	}
	prov, _ := splitRef(cmp.Or(body.Provider, strings.Join(body.Providers, "")))
	pc, ok := b.Reg.Component(prov)
	if _, built := startIn(starts, prov); !ok || !built {
		return false
	}
	return providesNone(pc, def.Service)
}

// waitWords are xbind's sentences for a line that waits (w): what it waits
// for, how long, and what does not get bound.
func waitWords(w delegation.PlanWait) []string {
	return []string{
		fmt.Sprintf("applies once %s provides %s; nothing is bound until then", w.Tile, w.Service),
		fmt.Sprintf("xbin applies it by itself, as you, when %s's manifest declares %s as %s interface for the role %s — and only that: if it declares anything else it is not bound, and you are told", w.Tile, w.Service, article(w.Kind), w.Role),
		fmt.Sprintf("it waits at most %d hours after you apply this plan, then lapses", int(delegation.MaxWait/time.Hour)),
	}
}

func article(kind string) string {
	if kind == "http" {
		return "an http"
	}
	return "a " + kind
}

// PreviewWait judges a waiting line (op, pinned to w) as p would apply it
// NOW: pending while the tile provides nothing of the kind; held, with
// xbind's words for why, when it provides something else than was
// approved; gone when a tile of the line is no more; ready — with the
// ordinary preview of the binding, the handler's own check — when it
// provides exactly what was approved.
func (b *Broker) PreviewWait(p auth.Principal, op delegation.ChangeOp, w delegation.PlanWait) (delegation.ChangePreview, delegation.WaitState, string, error) {
	state, why := b.waitState(w)
	if state != delegation.WaitReady {
		return delegation.ChangePreview{}, state, why, nil
	}
	pv, err := b.preview(p, op, nil, nil, true)
	return pv, delegation.WaitReady, "", err
}

// waitState is where the tile a waiting line is pinned to (w) stands now,
// read from one view of the registry: pending, ready, held (with why) or
// gone.
func (b *Broker) waitState(w delegation.PlanWait) (delegation.WaitState, string) {
	return waitStateIn(b.Reg.Component, w)
}

// waitChanged: the tile a waiting line binds to is no longer as the line
// was judged a moment before its bind (apiBindingSet, under the registry's
// lock).
type waitChanged struct{ why string }

func (e *waitChanged) Error() string { return e.why }

// waitStateIn is waitState over component — the registry's lookup, or the
// one MutateWorkspaceWhen hands a write.
func waitStateIn(component func(string) (*registry.Component, bool), w delegation.PlanWait) (delegation.WaitState, string) {
	if _, ok := component(w.Component); !ok {
		return delegation.WaitGone, w.Component + " no longer exists"
	}
	pc, ok := component(w.Tile)
	if !ok {
		return delegation.WaitGone, w.Tile + " no longer exists"
	}
	var found []registry.Iface
	names := make([]string, 0, len(pc.Manifest.Provides))
	for name := range pc.Manifest.Provides {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		if def := pc.Manifest.Provides[name]; def.Service == w.Service || name == w.Service {
			found = append(found, def)
		}
	}
	switch {
	case len(found) == 0:
		return delegation.WaitPending, ""
	case len(found) > 1:
		return delegation.WaitHeld, fmt.Sprintf("%s declares %s more than once: what you approved is one %s interface for the role %s", w.Tile, w.Service, w.Kind, w.Role)
	}
	def := found[0]
	switch {
	case def.Kind != w.Kind || def.Service != w.Service:
		return delegation.WaitHeld, fmt.Sprintf("%s declares %s as %s interface (service %q): what you approved is %s interface of the service %s", w.Tile, w.Service, article(cmp.Or(def.Kind, "unnamed")), def.Service, article(w.Kind), w.Service)
	case def.Role != w.Role:
		return delegation.WaitHeld, fmt.Sprintf("%s declares %s for the role %q: what you approved is the role %s, and a binding grants the role the tile names", w.Tile, w.Service, def.Role, w.Role)
	case def.Instances:
		return delegation.WaitHeld, fmt.Sprintf("%s declares %s as bound by instance: what you approved is the tile itself", w.Tile, w.Service)
	}
	return delegation.WaitReady, ""
}
