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
// comment). p is a person's own principal: a tile's, a delegate's or a
// view-as one previews nothing.
func (b *Broker) PreviewChange(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
	if p.Component != "" || p.Agent != "" || p.Impersonator != "" || p.UserID == "" {
		return delegation.ChangePreview{}, errors.New("an admin change is described for the person who would apply it")
	}
	switch op.Kind {
	case ChangeGrantAdd, ChangeGrantRevoke:
		return b.previewGrant(p, op)
	case ChangeBindingSet, ChangeBindingRemove:
		return b.previewBinding(p, op)
	case ChangeOrgShare:
		return b.previewOrgShare(p, op)
	case ChangeLimitsSet:
		return b.previewLimit(p, op)
	}
	return delegation.ChangePreview{}, fmt.Errorf("no such kind of change (one of %s)", strings.Join(ChangeKinds(), ", "))
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
	g := registry.Grant{From: a.From, Target: a.Target, Role: a.Role}
	revoke := op.Kind == ChangeGrantRevoke
	held := []string{} // the roles from holds on target now, by explicit grant
	for _, e := range b.Reg.Workspace().Grants {
		if e.From == g.From && e.Target == g.Target {
			held = append(held, e.Role)
		}
	}
	slices.Sort(held)
	after := slices.Clone(held)
	has := slices.Contains(held, g.Role)
	pv := delegation.ChangePreview{Before: mustJSON(map[string]any{"roles": held}),
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
	}
	pv.After = mustJSON(map[string]any{"roles": after})
	if strings.HasPrefix(g.Target, "res:") || strings.HasPrefix(g.Target, "gpu:") || strings.HasPrefix(g.Target, "cap:") {
		pv.Effects = append(pv.Effects, g.From+"'s backend may restart")
	}
	if why := b.grantCheck(p, g, revoke); why != nil {
		pv.Blocked = why.msg
	}
	return pv, nil
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

func (b *Broker) previewBinding(p auth.Principal, op delegation.ChangeOp) (delegation.ChangePreview, error) {
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
	slot := body.Component + "'s " + body.Slot
	pv := delegation.ChangePreview{Call: delegation.AdminCall{Method: http.MethodPost, Path: "/bindings", Body: mustJSON(body)}}
	to := strings.Join(append(slices.Clone(body.Providers), body.Provider), ", ")
	to = strings.Trim(to, ", ")
	switch {
	case remove && to != "":
		pv.Line = fmt.Sprintf("Unbind %s from %s", slot, to)
	case remove:
		pv.Line = fmt.Sprintf("Unbind %s", slot)
	case body.Add:
		pv.Line = fmt.Sprintf("Also bind %s to %s", slot, to)
	default:
		pv.Line = fmt.Sprintf("Bind %s to %s", slot, to)
	}
	if remove {
		pv.Call.Method = http.MethodDelete
	}
	cur := b.Reg.Workspace().Bindings[body.Component][body.Slot]
	pv.Before = mustJSON(map[string]any{"binding": refsOf(cur)})
	plan, why := b.bindingCheck(p, body, remove)
	if why != nil {
		pv.Blocked, pv.After = why.msg, pv.Before
		return pv, nil
	}
	pv.After = mustJSON(map[string]any{"binding": refsOf(plan.next)})
	switch {
	case b.isSandboxNetSlot(body.Component, body.Slot):
		pv.Effects = append(pv.Effects, body.Component+"'s sandboxes get the new network; no backend restarts")
	default:
		pv.Effects = append(pv.Effects, body.Component+"'s backend restarts")
	}
	if !plan.del && b.isHTTPSlot(body.Component, body.Slot) {
		for _, e := range plan.delta {
			prov, _ := splitRef(e.Ref)
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
	if a.Org == "" || a.Tile == "" {
		return delegation.ChangePreview{}, errors.New("org.share names an org and a tile")
	}
	pv := delegation.ChangePreview{Call: delegation.AdminCall{Method: http.MethodPut, Path: "/access",
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
	// in the handler's order: who manages the tile's access, then what the
	// store would refuse (SetOrgTile: the level, the org)
	_, tileOK := b.Reg.Component(a.Tile)
	switch why := b.accessCheck(p, a.Tile, a.Org); {
	case why != nil:
		pv.Blocked = why.msg
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
	if a.Tile == "" {
		return delegation.ChangePreview{}, errors.New("limits.set names a tile")
	}
	// a raise an approver decides: never force, never a restart now
	body := limitsPostBody{Tile: a.Tile, Kind: a.Kind, Value: a.Value, Source: limits.SourceRaise}
	pv := delegation.ChangePreview{Call: delegation.AdminCall{Method: http.MethodPost, Path: "/limits",
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
		pv.Blocked = w.refusal()
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
	pv.Effects = append(pv.Effects, fmt.Sprintf("%s may use up to %s of %s (now %s)", a.Tile, limits.Format(k, a.Value), k.Meta().Label, limits.Format(k, cur)))
	if k != limits.DiskGiB {
		pv.Effects = append(pv.Effects, "it applies when "+a.Tile+"'s backend next starts")
	}
	return pv, nil // no inverse: a limit goes back to its default on the limits page (DELETE /limits)
}
