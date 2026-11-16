package broker

// changeset.go — the kinds of an admin change set and their previews
// (docs/protocol/delegation.md §Change sets; D228; the server's
// changeset.go runs the sets). A person's workspace agent proposes admin
// operations; it never performs one. Each kind here is one admin API call
// xbind knows how to describe:
//
//	grant.add      {from, target, role}                  POST   /grants
//	grant.revoke   {from, target, role}                  DELETE /grants
//	binding.set    {component, slot, provider | providers, host?, zone?, listen?, add?}
//	                                                     POST   /bindings
//	binding.remove {component, slot, provider?, host?, zone?, listen?}
//	                                                     DELETE /bindings
//	org.share      {org, tile, level}  ("" stops it)     PUT    /access
//	limits.set     {tile, kind, value}                   POST   /limits
//
// The list is closed: there is no kind that names a route, so nothing
// outside it — users, passwords, devices, SSO, the vault, view-as, policy
// rows, ownership — can be in a change set.
//
// And each kind is closed to what it was meant for (the 2026-11 security
// review: "what is the most powerful thing this argument set can
// express"). A change set is for wiring tiles to each other and to the
// network, sharing them and raising their limits. It is never for power
// over the workspace itself, and never for the agent that proposes it:
//
//   - no operation is ON a workspace agent (a holder of delegation): not a
//     grant from or to it, a binding of or to it, a share of it, its limits
//     — an agent that could propose its own powers needs one tired click;
//   - a grant's target is a tile, one tile's code (code:<tile>) or a
//     resource (res:…). xbin and xbin:* (the admin API), cap:* (host
//     capabilities), gpu:*, the bare code (every tile's source) and net:*
//     are given on the Grants page, by a person who went there to do it —
//     and taken away there too;
//   - a binding is of a tile's own declared slot: not the @ pseudo-slots
//     (where backups go), not the workspace default (*), and never the
//     host's network (host, or a network set that says host);
//   - a tile that holds xbin, cap:*, gpu:* or the bare code is not shared
//     with an org by a change set: its members would hold that power.
//
// These are refusals of the PROPOSAL (PreviewChange's error: 400), checked
// again when the set is applied — not lines a person might click through.
//
// PreviewChange describes one operation as the person would apply it NOW,
// and changes nothing: it runs the same checks the handler runs before it
// writes (grantCheck, bindingCheck, accessCheck, limitsCheck and decide's
// dry run — the handlers call the very same functions), reads the state
// the operation would replace, and names the admin call that applies it.
// The line a person reads is written here from the kind and its arguments,
// never from the agent's text.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// Change-set kinds.
const (
	ChangeGrantAdd      = "grant.add"
	ChangeGrantRevoke   = "grant.revoke"
	ChangeBindingSet    = "binding.set"
	ChangeBindingRemove = "binding.remove"
	ChangeOrgShare      = "org.share"
	ChangeLimitsSet     = "limits.set"
)

// ChangeKinds lists the kinds a change set may hold.
func ChangeKinds() []string {
	return []string{ChangeGrantAdd, ChangeGrantRevoke, ChangeBindingSet, ChangeBindingRemove, ChangeOrgShare, ChangeLimitsSet}
}

// strictArgs decodes a kind's arguments: unknown keys are an error, so a
// misspelt or smuggled field never rides into the admin call.
func strictArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("its arguments don't parse: %w", err)
	}
	return nil
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// captured is a ResponseWriter that keeps a check's refusal.
type captured struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (c *captured) Header() http.Header         { return c.header }
func (c *captured) WriteHeader(s int)           { c.status = s }
func (c *captured) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *captured) refusal() string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(c.body.Bytes(), &e) != nil || e.Error == "" {
		return fmt.Sprintf("refused (%d)", c.status)
	}
	return e.Error
}

// PreviewChange describes op as person p would apply it now (the file
// comment). p is a person's own principal — or the owner's, who approves
// under --no-auth, where nobody signs in (the server decides when that
// counts): a tile's, a delegate's or a view-as one previews nothing.
func (b *Broker) PreviewChange(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
	return b.preview(p, op, nil, nil, false)
}

// PreviewPlanned is PreviewChange for a line of a plan (plan.go): planned
// are the tiles earlier tile.create lines of the same set make, taken as
// made — a line that names one is described from its template's manifest
// (ChangePreview.Deferred) and checked against the tile once it exists.
func (b *Broker) PreviewPlanned(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile) (delegation.ChangePreview, error) {
	return b.preview(p, op, planned, nil, true)
}

// PreviewPlan is PreviewPlanned for a line that comes after starts — the
// set's earlier agent.start lines. A binding to a tile one of them starts
// a tile agent in, which doesn't provide what the slot wants yet, is a
// line that waits (ChangePreview.Waits, plan.go) instead of one that can't
// be applied.
func (b *Broker) PreviewPlan(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile, starts []delegation.PlannedStart) (delegation.ChangePreview, error) {
	return b.preview(p, op, planned, starts, true)
}

func (b *Broker) preview(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile, starts []delegation.PlannedStart, plan bool) (delegation.ChangePreview, error) {
	if p.Component != "" || p.Agent != "" || p.Impersonator != "" || p.UserID == "" && !p.Owner {
		return delegation.ChangePreview{}, errors.New("an admin change is described for the person who would apply it")
	}
	pv, err := b.previewChange(p, op, planned, starts)
	if err == nil && plan && pv.Blocked == "" {
		b.readOnlyNote(&pv)
	}
	if err == nil {
		slices.Sort(pv.Warnings) // the basis of "what the person saw" (the server's changeBasis)
	}
	return pv, err
}

// lineNames checks the names an operation's arguments carry before any of
// them is written into the line a person reads: xbind's sentence is built
// from them, so each must be a name and nothing else — 1 to 200 bytes of
// letters, digits and the punctuation paths, refs, hostnames and
// addresses are made of. No space, quote, line break, control or
// non-ASCII character (a bidi override reads as another sentence): a
// "name" that carries text would put the agent's words in xbind's mouth.
// Pairs are (what, value); an empty value is an argument left out.
func lineNames(pairs ...string) error {
	for i := 0; i+1 < len(pairs); i += 2 {
		what, v := pairs[i], pairs[i+1]
		if len(v) > 200 {
			return fmt.Errorf("%s is %d bytes long: a name is at most 200", what, len(v))
		}
		for _, c := range []byte(v) {
			switch {
			case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			case strings.IndexByte("._~:@#/*+=,[]-", c) >= 0:
			default:
				return fmt.Errorf("%s is not a name: it holds %q", what, string(rune(c)))
			}
		}
	}
	return nil
}

// notOnAgent refuses an operation on a workspace agent (the file comment).
func notOnAgent(what, tile string) error {
	if auth.HoldsDelegation(strings.Trim(tile, "/")) {
		return fmt.Errorf("%s %s: it is a workspace agent, and no change set changes what an agent may do or who may reach it", what, tile)
	}
	return nil
}

// grantTargetRefusal is why a change set never grants (or revokes) target:
// "" for a tile, one tile's code or a resource.
func grantTargetRefusal(target string) string {
	const page = " is given and taken away on the Grants page, by an admin — never by a change set"
	switch {
	case target == "xbin" || strings.HasPrefix(target, "xbin:"):
		return target + " (the admin API)" + page
	case strings.HasPrefix(target, "cap:"):
		return target + " (a capability over the host)" + page
	case strings.HasPrefix(target, "gpu:"):
		return target + " (a device)" + page
	case target == "code":
		return "code (every tile's source)" + page
	case strings.HasPrefix(target, "net:"):
		return "network egress is not a grant"
	case strings.ContainsAny(target, "*?[ \t\n"):
		return "a grant names one target"
	}
	return ""
}

// powerOf lists the workspace-level grants tile holds — what makes it more
// than a tile: the admin API, host capabilities, devices, all source.
func (b *Broker) powerOf(tile string) []string {
	var out []string
	for _, g := range b.Reg.Workspace().Grants {
		if g.From == tile && grantTargetRefusal(g.Target) != "" && !strings.HasPrefix(g.Target, "net:") && !slices.Contains(out, g.Target) {
			out = append(out, g.Target)
		}
	}
	slices.Sort(out)
	return out
}

// previewChange describes op. Every kind but the three a person may do with
// their own hands is an admin's (ChangePreview.Admin): a grant, an org's
// share and a limit always; a binding unless its tile's owner could wire it
// themselves (bindPersonal); a creation unless the person's own creation
// rule lets it (plan.go).
func (b *Broker) previewChange(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile, starts []delegation.PlannedStart) (pv delegation.ChangePreview, err error) {
	switch op.Kind {
	case ChangeGrantAdd, ChangeGrantRevoke:
		pv, err = b.previewGrant(p, op)
		pv.Admin = true
	case ChangeBindingSet, ChangeBindingRemove:
		pv, err = b.previewBinding(p, op, planned, starts)
	case ChangeOrgShare:
		pv, err = b.previewOrgShare(p, op)
		pv.Admin = true
	case ChangeLimitsSet:
		pv, err = b.previewLimit(p, op)
		pv.Admin = true
	case ChangeTileCreate:
		pv, err = b.previewTileCreate(p, op, planned)
	default:
		return delegation.ChangePreview{}, fmt.Errorf("no such kind of change (one of %s)", strings.Join(ChangeKinds(), ", "))
	}
	if err == nil && pv.Forbidden {
		// not theirs to change, so not theirs to read through a proposal:
		// the line and the refusal, nothing of what is there now
		pv = delegation.ChangePreview{Line: pv.Line, Call: pv.Call, Blocked: pv.Blocked, Forbidden: true, Admin: true, Tiles: pv.Tiles}
	}
	return pv, err
}

func (b *Broker) previewGrant(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
	var a struct {
		From   string `json:"from"`
		Target string `json:"target"`
		Role   string `json:"role"`
	}
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	if err := lineNames("from", a.From, "target", a.Target, "role", a.Role); err != nil {
		return delegation.ChangePreview{}, err
	}
	g := registry.Grant{From: a.From, Target: a.Target, Role: a.Role}
	revoke := op.Kind == ChangeGrantRevoke
	if err := notOnAgent("a grant of", g.From); err != nil {
		return delegation.ChangePreview{}, err
	}
	if why := grantTargetRefusal(g.Target); why != "" {
		return delegation.ChangePreview{}, errors.New(why)
	}
	callee, _, _ := strings.Cut(strings.TrimPrefix(g.Target, "code:"), "#") // the tile a grant reaches ("" for a resource)
	if strings.HasPrefix(g.Target, "res:") {
		callee = ""
	}
	if err := notOnAgent("a grant on", callee); err != nil {
		return delegation.ChangePreview{}, err
	}
	held := []string{} // the roles from holds on target now, by explicit grant
	for _, e := range b.Reg.Workspace().Grants {
		if e.From == g.From && e.Target == g.Target {
			held = append(held, e.Role)
		}
	}
	slices.Sort(held)
	after := slices.Clone(held)
	has := slices.Contains(held, g.Role)
	// the tiles the line is judged by: both ends — a grant on a tile (or on
	// a resource of one's scope) reaches into it
	tiles := []string{g.From}
	if callee != "" {
		tiles = append(tiles, callee)
	} else if rt, _, ok := b.parseRes(g.Target); ok && rt.Scope != "" {
		tiles = append(tiles, rt.Scope)
	}
	pv := delegation.ChangePreview{Before: mustJSON(map[string]any{"roles": held}), Tiles: tiles,
		Call: delegation.AdminCall{Method: http.MethodPost, Path: "/grants", Body: mustJSON(a)}}
	if revoke {
		pv.Call.Method = http.MethodDelete
		pv.Line = fmt.Sprintf("Take away %s's %s access to %s", g.From, g.Role, g.Target)
		after = slices.DeleteFunc(after, func(r string) bool { return r == g.Role })
		pv.Effects = append(pv.Effects, g.From+" can no longer use "+g.Target+" as "+g.Role)
		if has {
			pv.Inverse = &delegation.ChangeOp{Kind: ChangeGrantAdd, Args: mustJSON(a)}
		}
	} else {
		pv.Line = fmt.Sprintf("Let %s use %s as %s", g.From, g.Target, g.Role)
		if !has {
			after = append(after, g.Role)
			slices.Sort(after)
			pv.Inverse = &delegation.ChangeOp{Kind: ChangeGrantRevoke, Args: mustJSON(a)}
		}
		pv.Effects = append(pv.Effects, g.From+"'s code, and everyone who can change it, can use "+g.Target+" as "+g.Role)
		if warn := b.partitionGrantWarning(g.From, g.Target); warn != "" {
			pv.Warnings = append(pv.Warnings, warn)
		}
		if power := b.powerOf(callee); callee != "" && len(power) > 0 && g.Target == callee {
			pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s holds %s: whatever its API does with that, %s can now ask it to do", callee, strings.Join(power, ", "), g.From))
		}
	}
	pv.After = mustJSON(map[string]any{"roles": after})
	pv.BeforeText, pv.AfterText = rolesWords(held), rolesWords(after)
	if strings.HasPrefix(g.Target, "res:") {
		pv.Effects = append(pv.Effects, g.From+"'s backend may restart")
	}
	if why := b.grantCheck(p, g, revoke); why != nil {
		pv.Blocked, pv.Forbidden = why.msg, why.status == http.StatusForbidden
	}
	return pv, nil
}

// rolesWords, bindingWords and shareWords say a change's before and after
// in words (ChangePreview.BeforeText): what a review draws, so that no
// client has to make a sentence of the data.
func rolesWords(roles []string) string {
	if len(roles) == 0 {
		return "no access"
	}
	return strings.Join(roles, ", ")
}

func bindingWords(bd registry.Binding) string {
	if len(bd) == 0 {
		return "not bound"
	}
	parts := make([]string, 0, len(bd))
	for _, e := range bd {
		w := e.Ref
		if e.Ref == LLMRef {
			w = "the workspace's LLM connector (" + LLMRef + ")"
		}
		for _, kv := range [][2]string{{"the public hostname", e.Host}, {"every hostname matching", e.Zone}, {"the host's port", e.Listen}} {
			if kv[1] != "" {
				w += ", at " + kv[0] + " " + kv[1]
			}
		}
		parts = append(parts, w)
	}
	return strings.Join(parts, "; ")
}

func shareWords(level string) string {
	if level == "" {
		return "not shared"
	}
	return "shared at " + level
}

func refsOf(bd registry.Binding) []map[string]string {
	out := []map[string]string{}
	for _, e := range bd {
		m := map[string]string{"provider": e.Ref}
		for k, v := range map[string]string{"host": e.Host, "zone": e.Zone, "listen": e.Listen} {
			if v != "" {
				m[k] = v
			}
		}
		out = append(out, m)
	}
	return out
}

func (b *Broker) previewBinding(p auth.Principal, op delegation.ChangeOp, planned []delegation.PlannedTile, starts []delegation.PlannedStart) (delegation.ChangePreview, error) {
	var body bindingBody
	if err := strictArgs(op.Args, &body); err != nil {
		return delegation.ChangePreview{}, err
	}
	remove := op.Kind == ChangeBindingRemove
	if remove && (len(body.Providers) > 0 || body.Add) {
		return delegation.ChangePreview{}, errors.New("binding.remove names a slot, and optionally one provider or route")
	}
	if !remove && body.Provider == "" && len(body.Providers) == 0 {
		return delegation.ChangePreview{}, errors.New("binding.set names a provider (to unbind, use binding.remove)")
	}
	names := []string{"component", body.Component, "slot", body.Slot, "provider", body.Provider, "host", strings.TrimSpace(body.Host), "zone", strings.TrimSpace(body.Zone), "listen", strings.TrimSpace(body.Listen)}
	for _, ref := range body.Providers {
		names = append(names, "provider", ref)
	}
	if err := lineNames(names...); err != nil {
		return delegation.ChangePreview{}, err
	}
	if len(body.Providers) > 32 {
		return delegation.ChangePreview{}, errors.New("a binding names at most 32 providers")
	}
	if body.Provider != "" && len(body.Providers) > 0 {
		return delegation.ChangePreview{}, errors.New("a binding names provider or providers, not both")
	}
	switch {
	case body.Component == "*" || strings.ContainsAny(body.Component, "*?["):
		return delegation.ChangePreview{}, errors.New("a change set binds one tile's slot, never the workspace default")
	case strings.HasPrefix(body.Slot, "@"):
		return delegation.ChangePreview{}, fmt.Errorf("%s is no slot of a tile (where backups go is set on the Backups page, by an admin)", body.Slot)
	}
	if err := notOnAgent("a binding of", body.Component); err != nil {
		return delegation.ChangePreview{}, err
	}
	for _, ref := range append(slices.Clone(body.Providers), body.Provider) {
		prov, _ := splitRef(ref)
		if err := notOnAgent("a binding to", prov); err != nil {
			return delegation.ChangePreview{}, err
		}
		if !remove && b.refIsHostNet(body.Component, prov) {
			return delegation.ChangePreview{}, fmt.Errorf("%s is the host's own network: a tile gets it on the Network page, by an admin — never by a change set", prov)
		}
	}
	slot := body.Component + "'s " + body.Slot
	pv := delegation.ChangePreview{Call: delegation.AdminCall{Method: http.MethodPost, Path: "/bindings", Body: mustJSON(body)}}
	to := strings.Join(append(slices.Clone(body.Providers), body.Provider), ", ")
	to = strings.Trim(to, ", ")
	// the route is part of the line: "bind web to runtime" and "publish web
	// to the internet at shop.example.com" are not the same request
	route := ""
	for _, kv := range [][2]string{{"the public hostname", strings.ToLower(strings.TrimSpace(body.Host))}, {"every hostname matching", strings.ToLower(strings.TrimSpace(body.Zone))}, {"the host's port", strings.TrimSpace(body.Listen)}} {
		if kv[1] != "" {
			route += ", at " + kv[0] + " " + kv[1]
		}
	}
	switch {
	case remove && to != "":
		pv.Line = fmt.Sprintf("Unbind %s from %s%s", slot, to, route)
	case remove && route != "":
		pv.Line = fmt.Sprintf("Unbind %s%s", slot, strings.TrimPrefix(route, ","))
	case remove:
		pv.Line = fmt.Sprintf("Unbind %s", slot)
	case body.Add:
		pv.Line = fmt.Sprintf("Also bind %s to %s%s", slot, to, route)
	default:
		pv.Line = fmt.Sprintf("Bind %s to %s%s", slot, to, route)
	}
	if remove {
		pv.Call.Method = http.MethodDelete
	}
	if uses := plannedIn(planned, append(slices.Clone(body.Providers), body.Provider, body.Component)...); len(uses) > 0 {
		return b.previewPlannedBinding(p, pv, body, remove, planned, starts, uses) // a tile an earlier line creates (plan.go)
	}
	if !remove && b.waitsOn(body, starts) {
		return b.previewPlannedBinding(p, pv, body, remove, planned, starts, nil) // a provide a tile agent of this plan is still to write
	}
	pv.Tiles = b.bindingTiles(body)
	cur := b.Reg.Workspace().Bindings[body.Component][body.Slot]
	pv.Before, pv.BeforeText = mustJSON(map[string]any{"binding": refsOf(cur)}), bindingWords(cur)
	plan, why := b.bindingCheck(p, body, remove)
	pv.Admin = why != nil || !b.bindPersonal(p, body, plan)
	if why != nil {
		pv.Blocked, pv.After, pv.AfterText = why.msg, pv.Before, pv.BeforeText
		pv.Forbidden = why.status == http.StatusForbidden
		return pv, nil
	}
	pv.After, pv.AfterText = mustJSON(map[string]any{"binding": refsOf(plan.next)}), bindingWords(plan.next)
	if !plan.del {
		b.bindingOpens(&pv, body.Component, body.Slot, plan.delta)
	}
	switch {
	case b.isSandboxNetSlot(body.Component, body.Slot):
		pv.Effects = append(pv.Effects, body.Component+"'s sandboxes get the new network; no backend restarts")
	default:
		pv.Effects = append(pv.Effects, body.Component+"'s backend restarts")
	}
	if !plan.del && b.isHTTPSlot(body.Component, body.Slot) {
		for _, e := range plan.delta {
			if e.Ref == LLMRef { // xbind's own LLM connector, not a tile
				pv.Effects = append(pv.Effects, body.Component+"'s code, and everyone who can change it, can call the workspace's LLM connector")
				pv.Warnings = append(pv.Warnings, llmSpendWarning(body.Component))
				continue
			}
			prov, _ := splitRef(e.Ref)
			if power := b.powerOf(prov); len(power) > 0 {
				pv.Warnings = append(pv.Warnings, fmt.Sprintf("%s holds %s: whatever its API does with that, %s can now ask it to do", prov, strings.Join(power, ", "), body.Component))
			}
			pv.Effects = append(pv.Effects, body.Component+"'s code, and everyone who can change it, can call "+prov)
			if warn := b.partitionGrantWarning(body.Component, prov); warn != "" && !slices.Contains(pv.Warnings, warn) {
				pv.Warnings = append(pv.Warnings, warn)
			}
		}
	}
	// the inverse: put the slot back as it was — when that is one call
	simple := true
	for _, e := range cur {
		simple = simple && e.Host == "" && e.Zone == "" && e.Listen == ""
	}
	switch {
	case len(cur) == 0 && len(plan.next) > 0:
		pv.Inverse = &delegation.ChangeOp{Kind: ChangeBindingRemove, Args: mustJSON(map[string]string{"component": body.Component, "slot": body.Slot})}
	case len(cur) > 0 && simple:
		pv.Inverse = &delegation.ChangeOp{Kind: ChangeBindingSet, Args: mustJSON(map[string]any{"component": body.Component, "slot": body.Slot, "providers": cur.Refs()})}
	}
	return pv, nil
}

func (b *Broker) previewOrgShare(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
	var a struct {
		Org   string `json:"org"`
		Tile  string `json:"tile"`
		Level string `json:"level"`
	}
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	a.Tile = strings.Trim(a.Tile, "/")
	if err := lineNames("org", a.Org, "tile", a.Tile, "level", a.Level); err != nil {
		return delegation.ChangePreview{}, err
	}
	if a.Org == "" || a.Tile == "" {
		return delegation.ChangePreview{}, errors.New("org.share names an org and a tile")
	}
	if err := notOnAgent("a share of", a.Tile); err != nil {
		return delegation.ChangePreview{}, err
	}
	if power := b.powerOf(a.Tile); len(power) > 0 && a.Level != "" {
		return delegation.ChangePreview{}, fmt.Errorf("%s holds %s: who may use a tile with that power is decided on its Access page, by an admin — never by a change set", a.Tile, strings.Join(power, ", "))
	}
	pv := delegation.ChangePreview{Tiles: []string{a.Tile}, Call: delegation.AdminCall{Method: http.MethodPut, Path: "/access",
		Body: mustJSON(map[string]string{"tile": a.Tile, "kind": "org", "id": a.Org, "level": a.Level})}}
	if a.Level == "" {
		pv.Line = fmt.Sprintf("Stop sharing %s with the org %s", a.Tile, a.Org)
	} else {
		pv.Line = fmt.Sprintf("Share %s with the org %s at %s", a.Tile, a.Org, a.Level)
	}
	before := ""
	var org *users.Org
	if b.Users != nil {
		if o, ok := b.Users.Org(a.Org); ok {
			org, before = o, o.Tiles[a.Tile]
		}
	}
	pv.Before, pv.After = mustJSON(map[string]string{"level": before}), mustJSON(map[string]string{"level": a.Level})
	pv.BeforeText, pv.AfterText = shareWords(before), shareWords(a.Level)
	// in the handler's order: who manages the tile's access, then what the
	// store would refuse (SetOrgTile: the level, the org)
	_, tileOK := b.Reg.Component(a.Tile)
	switch why := b.accessCheck(p, a.Tile, a.Org); {
	case why != nil:
		pv.Blocked, pv.Forbidden = why.msg, why.status == http.StatusForbidden
	case a.Level != "" && a.Level != users.LevelRead && a.Level != users.LevelWrite && a.Level != users.LevelTerminal:
		pv.Blocked = fmt.Sprintf("unknown level %q (want read|write|terminal, or empty to remove)", a.Level)
	case b.Users == nil || org == nil:
		pv.Blocked = fmt.Sprintf("no such org %q", a.Org)
	case !tileOK:
		pv.Blocked = "no such tile: " + a.Tile // a share of nothing: the handler would take it, a change set doesn't
	}
	if org != nil {
		n := len(org.Members)
		switch {
		case a.Level == "":
			pv.Effects = append(pv.Effects, fmt.Sprintf("the %d member(s) of %s lose the access this share gave them to %s", n, a.Org, a.Tile))
		default:
			pv.Effects = append(pv.Effects, fmt.Sprintf("the %d member(s) of %s get %s on %s", n, a.Org, a.Level, a.Tile))
		}
		if a.Level == users.LevelTerminal {
			pv.Warnings = append(pv.Warnings, "terminal level lets them run code in "+a.Tile+" and change it")
		}
	}
	pv.Inverse = &delegation.ChangeOp{Kind: ChangeOrgShare, Args: mustJSON(map[string]string{"org": a.Org, "tile": a.Tile, "level": before})}
	return pv, nil
}

func (b *Broker) previewLimit(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
	var a struct {
		Tile  string `json:"tile"`
		Kind  string `json:"kind"`
		Value int64  `json:"value"`
	}
	if err := strictArgs(op.Args, &a); err != nil {
		return delegation.ChangePreview{}, err
	}
	if err := lineNames("tile", a.Tile, "kind", a.Kind); err != nil {
		return delegation.ChangePreview{}, err
	}
	if a.Tile == "" {
		return delegation.ChangePreview{}, errors.New("limits.set names a tile")
	}
	if err := notOnAgent("the limits of", a.Tile); err != nil {
		return delegation.ChangePreview{}, err
	}
	// a raise an approver decides: never force, never a restart now
	body := limitsPostBody{Tile: a.Tile, Kind: a.Kind, Value: a.Value, Source: limits.SourceRaise}
	pv := delegation.ChangePreview{Tiles: []string{a.Tile}, Call: delegation.AdminCall{Method: http.MethodPost, Path: "/limits",
		Body: mustJSON(map[string]any{"tile": a.Tile, "kind": a.Kind, "value": a.Value, "source": limits.SourceRaise})}}
	k, known := limits.Parse(a.Kind)
	if !known {
		pv.Line = fmt.Sprintf("Raise %s's %s limit to %d", a.Tile, a.Kind, a.Value)
	} else {
		pv.Line = fmt.Sprintf("Raise %s's %s limit to %s", a.Tile, k.Meta().Label, limits.Format(k, a.Value))
	}
	w := &captured{header: http.Header{}, status: http.StatusOK}
	v, t, ci, ok := b.limitsCheck(w, p, body)
	if !ok {
		pv.Blocked, pv.Forbidden = w.refusal(), w.status == http.StatusForbidden
		pv.Before, pv.After = mustJSON(map[string]any{}), mustJSON(map[string]any{"value": a.Value})
		return pv, nil
	}
	if why := b.refusal(v, t, body); why.code != 0 {
		pv.Blocked = why.msg
	}
	status, msg, cur := b.decide(v, t, body, ci, true)
	if status != http.StatusOK && pv.Blocked == "" {
		pv.Blocked = msg
	}
	pv.Before = mustJSON(map[string]any{"value": cur, "default": t.kf.def})
	pv.After = mustJSON(map[string]any{"value": a.Value})
	pv.BeforeText, pv.AfterText = limits.Format(k, cur), limits.Format(k, a.Value)
	if cur == t.kf.def {
		pv.BeforeText += " (the default)"
	}
	pv.Effects = append(pv.Effects, fmt.Sprintf("%s may use up to %s of %s (now %s)", a.Tile, limits.Format(k, a.Value), k.Meta().Label, limits.Format(k, cur)))
	if k != limits.DiskGiB {
		pv.Effects = append(pv.Effects, "it applies when "+a.Tile+"'s backend next starts")
	}
	return pv, nil // no inverse: a limit goes back to its default on the limits page (DELETE /limits)
}

// refIsHostNet reports a net ref that gives comp the host's own network:
// host, a network set whose rules say host, or — org and personal, which
// follow their owner's sets live — sets that say host now.
func (b *Broker) refIsHostNet(comp, prov string) bool {
	switch {
	case prov == "host":
		return true
	case b.Users == nil:
		return false
	case strings.HasPrefix(prov, NetRefSet):
		_, host, ok := b.netSetRuleTargets(prov)
		return ok && host
	case prov == NetRefOrg:
		_, host := netRuleTargets(b.Users.Ceiling(comp).NetRules())
		return host
	case prov == NetRefPersonal:
		_, _, rules := b.personalNet(comp)
		_, host := netRuleTargets(rules)
		return host
	}
	return false
}

// bindingOpens writes what the routes a binding adds OPEN — xbind's words
// for the reach a person is approving: an endpoint the internet or the
// host's network can reach, the networks a tile's code can reach.
func (b *Broker) bindingOpens(pv *delegation.ChangePreview, comp, slot string, delta registry.Binding) {
	warn := func(format string, a ...any) {
		if w := fmt.Sprintf(format, a...); !slices.Contains(pv.Warnings, w) {
			pv.Warnings = append(pv.Warnings, w)
		}
	}
	c, ok := b.Reg.Component(comp)
	if !ok {
		return
	}
	if def, expose := c.Manifest.Exposes[slot]; expose {
		what := fmt.Sprintf("%s's %s endpoint", comp, slot)
		if len(def.Paths) > 0 {
			what += " (its paths " + strings.Join(def.Paths, ", ") + ")"
		}
		for _, e := range delta {
			via := "xbind's own listener"
			if e.Ref != IngressSourceRuntime {
				via = e.Ref
			}
			switch {
			case e.Host != "":
				warn("PUBLIC: anyone who can reach %s reaches %s, through %s — without signing in to this workspace", e.Host, what, via)
			case e.Zone != "":
				warn("PUBLIC: anyone who can reach any hostname matching %s reaches %s, through %s — without signing in to this workspace", e.Zone, what, via)
			case e.Listen != "":
				warn("OPEN PORT: %s on this machine goes straight to %s — anyone who can reach that port reaches it, with no sign-in", e.Listen, what)
			default:
				warn("%s becomes reachable through %s", what, via)
			}
		}
		return
	}
	def := c.Manifest.Interfaces[slot]
	if !netKind(def.Kind) {
		return
	}
	who := comp + "'s code"
	if def.Kind == registry.KindSandboxNet {
		who = "the sandboxes " + comp + " starts"
	}
	for _, e := range delta {
		prov, _ := splitRef(e.Ref)
		switch {
		case prov == NetRefNone:
		case prov == "internet":
			warn("EGRESS: %s can reach the whole internet — and send anything it can read there", who)
		case strings.HasPrefix(prov, "internet:"):
			warn("EGRESS: %s can reach %s on the internet — and send anything it can read there", who, strings.TrimPrefix(prov, "internet:"))
		case strings.HasPrefix(prov, "lan:"):
			warn("EGRESS: %s can reach %s on this machine's local network", who, strings.TrimPrefix(prov, "lan:"))
		case prov == NetRefOrg || prov == NetRefPersonal || strings.HasPrefix(prov, NetRefSet):
			warn("EGRESS: %s can reach every network in %s, as that is set now and as it is changed later", who, map[bool]string{true: "the network set " + strings.TrimPrefix(prov, NetRefSet), false: "its owner's network sets (" + prov + ")"}[strings.HasPrefix(prov, NetRefSet)])
		default:
			warn("EGRESS: %s's traffic goes out through %s, wherever that tile sends it", who, prov)
		}
	}
}
