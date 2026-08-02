package broker

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Ownership transfer (plans/transfer.md, D39): create-bound authorization,
// an impact preview before every confirm, and active re-evaluation after —
// a transfer changes which ceilings/allowances govern the tile, so
// enforcement must bite NOW, not at the next restart.

// transferAllowed is the §1 authorization: a GIVE right on the current
// owner side AND a RECEIVE right on the target side. "" = allowed;
// otherwise the refusal names the missing right.
func (b *Broker) transferAllowed(p auth.Principal, st *users.Store, tile, to string) string {
	if b.canManageUsers(p) {
		return ""
	}
	uid := humanID(p)
	if uid == "" {
		return "transfers are made by signed-in humans (or a workspace admin)"
	}
	// GIVE: the tile's user-owner, or an org admin of its owning org.
	owner := st.Owner(tile)
	give := owner == users.OwnerKindUser+":"+uid
	if !give {
		if org, isOrg := strings.CutPrefix(owner, users.OwnerKindOrg+":"); isOrg && p.Access.IsAdminOrg(org) {
			give = true
		}
	}
	if !give {
		return "giving a tile away needs its user-owner, an admin of its owning org, or a workspace admin"
	}
	// RECEIVE (D39): the bound for "may I transfer INTO X" is "may I create
	// in X".
	toKind, toID, err := users.ParseOwner(to)
	if err != nil {
		return err.Error()
	}
	switch toKind {
	case users.OwnerKindOrg:
		if !p.Access.CanCreateAs(toID) {
			return "receiving a tile in org \"" + toID + "\" needs its Create permission — the transfer-in bound is the create bound (D39)"
		}
	case users.OwnerKindUser:
		if toID != uid {
			return "transferring a tile to another user is a workspace-admin act"
		}
	default: // "" — workspace-owned
		return "transferring a tile to workspace-owned is a workspace-admin act"
	}
	return ""
}

// deadItem / deadGrant are the preview's impact rows (spec §2 shape).
type deadItem struct {
	Slot   string `json:"slot"`
	Reason string `json:"reason"`
}
type deadGrant struct {
	Target string `json:"target"`
	Role   string `json:"role"`
	Reason string `json:"reason"`
}

type transferReport struct {
	Tile        string            `json:"tile"`
	From        string            `json:"from"`
	To          string            `json:"to"`
	Allowed     bool              `json:"allowed"`
	Error       string            `json:"error,omitempty"`
	CallerLevel map[string]string `json:"callerLevel,omitempty"` // {before, after}
	DeadBind    []deadItem        `json:"deadBindings"`
	DeadGrants  []deadGrant       `json:"deadGrants"`
	Plane       []string          `json:"planeChanges"`
	Unbound     []string          `json:"unbound,omitempty"` // POST only
}

// transferPreview computes the §2 report for moving tile → to. Pure.
func (b *Broker) transferPreview(p auth.Principal, st *users.Store, tile, to string) transferReport {
	rep := transferReport{
		Tile: tile, From: st.Owner(tile), To: to,
		DeadBind: []deadItem{}, DeadGrants: []deadGrant{}, Plane: []string{},
	}
	if msg := b.transferAllowed(p, st, tile, to); msg != "" {
		rep.Error = msg
	} else {
		rep.Allowed = true
	}
	// The caller's own level, before and after (D31 resolution). Admins are
	// terminal everywhere; elements carry no level.
	switch {
	case p.Access != nil:
		rep.CallerLevel = map[string]string{
			"before": p.Access.TileLevel(tile),
			"after":  p.Access.WithOwner(tile, to).TileLevel(tile),
		}
	case b.IsAdmin(p):
		rep.CallerLevel = map[string]string{"before": users.LevelTerminal, "after": users.LevelTerminal}
	}
	newCeiling := st.CeilingFor(tile, to)
	// Bindings: a slot is DEAD when the new ceiling denies its whole class
	// (net/ingress) or every provider ref is mayCall-blocked. Partial slots
	// survive.
	c, haveComp := b.Reg.Component(tile)
	if haveComp {
		for slot, binding := range b.Reg.Workspace().Bindings[tile] {
			if len(binding) == 0 {
				continue
			}
			if reason := b.deadSlotReason(c, slot, binding, newCeiling); reason != "" {
				rep.DeadBind = append(rep.DeadBind, deadItem{Slot: slot, Reason: reason})
			}
		}
	}
	// Grants FROM the tile that stop resolving under the new ceiling.
	for _, g := range b.Reg.Workspace().Grants {
		if g.From != tile {
			continue
		}
		if msg := b.ceilingBlockWith(newCeiling, tile, g.Target); msg != "" {
			rep.DeadGrants = append(rep.DeadGrants, deadGrant{Target: g.Target, Role: g.Role, Reason: msg})
		}
	}
	// Plane notes (informational).
	if org, isOrg := strings.CutPrefix(to, users.OwnerKindOrg+":"); isOrg {
		rep.Plane = append(rep.Plane,
			"org:"+org+" admins gain full control of this tile (terminal, lifecycle, sharing)")
		intra := false
		for _, g := range b.Reg.Workspace().Grants {
			if g.From == tile && b.targetOwnerOrg(g.Target) == org {
				intra = true
			}
		}
		if intra {
			rep.Plane = append(rep.Plane, "grants from this tile become intra-org for org:"+org)
		}
	}
	return rep
}

// deadSlotReason: why this bound slot cannot exist at all under c's new
// ceiling ("" = it survives, possibly partially).
func (b *Broker) deadSlotReason(c *registry.Component, slot string, binding registry.Binding, ceil users.Ceiling) string {
	if _, isExpose := c.Manifest.Exposes[slot]; isExpose {
		if row, ok := ceil.DenyRow(users.PolicyDenyIngress); ok {
			return "a policy row for tiles matching \"" + row.Tiles + "\" denies ingress under the new owner"
		}
		return ""
	}
	iface, isIface := c.Manifest.Interfaces[slot]
	if !isIface {
		return ""
	}
	if iface.Kind == "net" || iface.Kind == "lan-ingress" {
		if row, ok := ceil.DenyRow(users.PolicyDenyNet); ok {
			return "a policy row for tiles matching \"" + row.Tiles + "\" denies net under the new owner"
		}
		return ""
	}
	// http/stream: every provider ref must be mayCall-blocked for the slot to die.
	reason := ""
	for _, ref := range binding {
		prov := providerPath(ref.Ref)
		if b.sameScope(c, prov) {
			return "" // same-scope refs are ceiling-exempt (ND5)
		}
		if row, ok := ceil.MayCallBlocker(prov); ok {
			reason = "a policy row for tiles matching \"" + row.Tiles + "\" allow-lists call targets and \"" + prov + "\" is not covered under the new owner"
			continue
		}
		return "" // at least one ref survives → partial slot stays
	}
	return reason
}

// GET /owner/preview?tile=&to= — the report both confirm dialogs render.
// Never mutates. Readable by whoever can read the tile.
func (b *Broker) apiOwnerPreview(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	to := r.URL.Query().Get("to")
	p := auth.PrincipalOf(r)
	if tile == "" || !p.CanReadTile(tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no access to this tile"})
		return
	}
	if _, ok := b.Reg.Component(tile); !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such component"})
		return
	}
	if _, _, err := users.ParseOwner(to); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := st.ValidateNewTile(to); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	server.WriteJSON(w, http.StatusOK, b.transferPreview(p, st, tile, to))
}

// executeTransferEffects runs the §3 side effects after a successful
// SetOwner: unbind hard-dead slots, restart what re-materializes at spawn,
// publish events. Returns the unbound slot names.
func (b *Broker) executeTransferEffects(tile string, rep transferReport) []string {
	unbound := []string{}
	if len(rep.DeadBind) > 0 {
		// Old net providers come from the STORED refs — netBinding already
		// applies the NEW owner's ceiling here (that's why the slot is dead),
		// so resolving through it would return "".
		providers := map[string]bool{}
		for _, d := range rep.DeadBind {
			for _, ref := range b.Reg.Workspace().Bindings[tile][d.Slot] {
				prov := providerPath(ref.Ref)
				if p, ok := b.Reg.Component(prov); ok && providesNet(p) {
					providers[prov] = true
				}
			}
		}
		_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
			for _, d := range rep.DeadBind {
				if slots := ws.Bindings[tile]; slots != nil {
					if _, ok := slots[d.Slot]; ok {
						delete(slots, d.Slot)
						unbound = append(unbound, d.Slot)
					}
					if len(slots) == 0 {
						delete(ws.Bindings, tile)
					}
				}
			}
		})
		if b.OnGrantChange != nil {
			for prov := range providers {
				b.OnGrantChange(prov) // its client roster changed
			}
		}
	}
	// The tile's spawn-materialized access (egress, res env, GPU) follows the
	// new owner's ceiling — restart it regardless of unbinds.
	if b.OnGrantChange != nil {
		b.OnGrantChange(tile)
	}
	b.Hub.Publish(events.Event{Type: "grants", Component: tile})
	b.usersEvent()
	return unbound
}
