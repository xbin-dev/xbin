package broker

import (
	"net/http"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// GET /whoami — who the caller is and what they may do (docs/protocol.md).

// apiWhoami reports the caller's identity + permissions (any authenticated
// principal — a tile uses it to adapt its UI to the current user).
func (b *Broker) apiWhoami(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	out := map[string]any{
		"admin":    b.IsAdmin(p),
		"terminal": p.CanTerminal(),
	}
	if b.Users != nil { // workspace tile-creation policy (D52) — owner pickers adapt to it
		out["tileCreation"] = b.Users.TileCreation()
		// personalTiles (D88): may THIS caller (the human behind a tile call)
		// own tiles personally — org-only and the account switch folded in,
		// so pickers need not know why.
		out["personalTiles"] = b.IsAdmin(p) || b.personalRefusal(p.UserID) == ""
	}
	if p.Impersonator != "" { // an admin's read-only view of this user (D64) — the shell shows a banner
		out["impersonatedBy"] = p.Impersonator
		out["readOnly"] = true
	}
	switch {
	case p.User != nil:
		out["kind"] = "user"
		out["id"] = p.User.ID
		out["name"] = p.User.Name
		out["role"] = p.User.Role
		out["tiles"] = p.User.Tiles // path→level map (D16); direct entries only
		out["canCreate"] = p.User.CanCreate
		out["termApi"] = p.CanTermAPI()
		out["termNet"] = p.CanTermNet()
		if b.Users != nil { // tiles this user OWNS (D24) — the self-service view
			if owned := b.Users.OwnedBy(users.OwnerKindUser + ":" + p.User.ID); len(owned) > 0 {
				out["owned"] = owned
			}
		}
		if orgs := b.userOrgsView(p.User); len(orgs) > 0 { // plans/orgs.md
			out["orgs"] = orgs
		}
		if b.Users != nil && !p.User.IsAdmin() { // the personal plane (D88)
			out["personal"] = b.Users.Personal(p.User.ID)
		}
	case p.Owner:
		out["kind"] = "root"
		out["id"] = "root"
		out["name"] = "root (token)"
		out["role"] = users.RoleAdmin
	case p.Component != "":
		out["kind"] = "element"
		out["id"] = p.Component
		if du := b.driverView(p); du != nil {
			out["user"] = du
		}
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// driverView is whoami's `user` object on element principals — the human
// driving the tile (frame/terminal attribution), FILTERED by the tile's
// trust so a low-trust or compromised tile can't harvest memberships:
//
//   - every tile: {id, name} only (attribution the tile can already infer
//     from per-user prefs; the element's own privilege is unchanged);
//   - the tile's own org: plus that ONE org's membership slice (its teams,
//     org-admin flag) — org-internal tiles may adapt to "your team here";
//   - workspace-management tiles (an xbin or xbin:users capability grant,
//     where a compromise already means workspace control): plus the admin
//     flag and the full org list — this is what the manager's
//     create-in-team picker runs on. A policy row denying xbin-caps
//     downgrades the tile's view along with its capability.
func (b *Broker) driverView(p auth.Principal) map[string]any {
	if p.UserID == "" || b.Users == nil {
		return nil
	}
	u, ok := b.Users.Get(p.UserID)
	if !ok {
		return nil
	}
	du := map[string]any{"id": u.ID, "name": u.Name, "personalTiles": b.personalRefusal(u.ID) == ""}
	switch {
	case b.elementXbinCapable(p.Component):
		du["admin"] = u.IsAdmin()
		if orgs := b.userOrgsView(u); len(orgs) > 0 {
			du["orgs"] = orgs
		}
	default:
		// An ordinary element learns the driving user's membership only for
		// the org that OWNS it (D24) — never the full membership map.
		if org, inOrg := b.Users.OwnerOrg(p.Component); inOrg {
			for _, m := range b.userOrgsView(u) {
				if m.ID == org {
					du["orgs"] = []users.OrgMembership{m}
					break
				}
			}
		}
	}
	return du
}

// elementXbinCapable reports whether an element holds any workspace-
// management capability (target "xbin" at any role, or "xbin:users") —
// grantedRole applies the policy ceiling, so an xbin-caps deny strips this
// trust tier too.
func (b *Broker) elementXbinCapable(comp string) bool {
	if comp == "" {
		return false
	}
	if _, ok := b.grantedRole(comp, "xbin"); ok {
		return true
	}
	_, ok := b.grantedRole(comp, "xbin:users")
	return ok
}

// userOrgsView is whoami's orgs field: memberships for a regular user; for a
// workspace admin every org, presented as an admin membership (their
// effective reality — they may act in all of them, e.g. create-as-org).
func (b *Broker) userOrgsView(u *users.User) []users.OrgMembership {
	if b.Users == nil {
		return nil
	}
	if !u.IsAdmin() {
		return b.Users.UserOrgs(u.ID)
	}
	var out []users.OrgMembership
	for _, o := range b.Users.Orgs() {
		out = append(out, users.OrgMembership{
			ID: o.ID, Name: o.Name, Level: users.LevelTerminal, Create: true, Admin: true,
		})
	}
	return out
}
