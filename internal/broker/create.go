package broker

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/scaffold"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// POST /api/xbin/create — the higher-level "create tile" API (same engine
// as `bx new`). Creating components is an editing-plane action, so callers
// are the owner/admins, a user whose (org/team-unioned) create patterns cover
// the path (D16 — "create ≈ own a namespace"), or elements holding the
// workspace-management capability: an explicit grant on the reserved target
// "xbin" at role writer (the template ships tiles/manager with that grant;
// revoke it and the tile request shows up in the grants panel like any other).
//
// An optional `team: "<org>/<team>"` creates the tile IN that team (plans/
// orgs.md): the path must be inside the team's org, the attributed human must
// be a team member (or an org/workspace admin), and the team is auto-granted
// its NewTiles level on the result.
func (b *Broker) apiCreate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		scaffold.Options
		Team string `json:"team"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Path == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{
			"error": "need {path, runtime?, title?, expose?, team?}", "docs": "/docs/protocol.md",
		})
		return
	}
	o := body.Options
	// Reserved-segment gate (plans/orgs.md): `o` must name an existing org,
	// `u` is reserved — applies to every caller, admins included.
	if err := b.validateNewPath(o.Path); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error(), "docs": "/docs/auth.md"})
		return
	}
	// No nesting either way: not inside an existing component, and not a
	// subtree that already contains one (an org container, say).
	if err := b.guardNewComponentTree(strings.Trim(o.Path, "/")); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	p := auth.PrincipalOf(r)
	if ok, msg := b.canCreateAt(p, o.Path); !ok {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
		return
	}

	// Validate create-in-team before any side effect.
	var team *users.Team
	var teamOrg string
	if body.Team != "" {
		var msg string
		team, teamOrg, msg = b.resolveCreateTeam(p, body.Team, o.Path)
		if msg != "" {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": msg, "docs": "/docs/auth.md"})
			return
		}
	}

	files, err := scaffold.Create(b.Reg.Root, o)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	// The creator owns their new namespace: a non-admin user who creates a
	// tile gets terminal on it (D16 auto-grant) — whether directly or by
	// driving a manager-style tile (frame principal; the user id rides along
	// for attribution). Admins have everything already.
	if p.UserID != "" && b.Users != nil {
		if u, ok := b.Users.Get(p.UserID); ok && !u.IsAdmin() {
			if err := b.Users.GrantTile(u.ID, o.Path, users.LevelTerminal); err != nil {
				slog.Warn("create auto-grant", "user", u.ID, "tile", o.Path, "err", err)
			}
		}
	}
	// Create-in-team: the team gets its configured NewTiles level (D19).
	if team != nil {
		if err := b.Users.GrantTileTeam(teamOrg, team.ID, o.Path, team.NewTiles); err != nil {
			slog.Warn("create team auto-grant", "team", body.Team, "tile", o.Path, "err", err)
		}
	}
	_ = b.Reg.Rescan() // visible immediately; the watcher event follows anyway
	out := map[string]any{"path": o.Path, "files": files}
	if team != nil {
		out["team"] = teamOrg + "/" + team.ID
		out["teamLevel"] = team.NewTiles
	}
	server.WriteJSON(w, http.StatusOK, out)
}

// resolveCreateTeam validates a create-in-team request: the ref parses, the
// tile path is inside the team's org, and the attributed human is a team
// member or an org admin (workspace admins pass outright). Returns the team
// and its org, or a user-facing refusal.
func (b *Broker) resolveCreateTeam(p auth.Principal, ref, tilePath string) (*users.Team, string, string) {
	if b.Users == nil {
		return nil, "", "no user store — teams are unavailable"
	}
	orgID, teamID, err := users.ParseTeamRef(ref)
	if err != nil {
		return nil, "", err.Error()
	}
	tileOrg, ok := users.OrgOf(tilePath)
	if !ok || tileOrg != orgID {
		return nil, "", "the tile path is not inside org \"" + orgID + "\" — create-in-team paths look like apps/o/" + orgID + "/<name>"
	}
	org, ok := b.Users.Org(orgID)
	if !ok {
		return nil, "", "no such org \"" + orgID + "\""
	}
	team := org.Team(teamID)
	if team == nil {
		return nil, "", "no such team \"" + ref + "\""
	}
	if !b.IsAdmin(p) {
		// The human behind the call (session principal, or the user id riding
		// a frame principal) must be a team member or an org admin — an
		// element's own xbin-writer grant is not enough to act "in a team".
		acc := (*users.Access)(nil)
		if p.UserID != "" {
			acc, _ = b.Users.Access(p.UserID)
		}
		if !acc.IsAdminOrg(orgID) && !slices.Contains(team.Members, p.UserID) {
			return nil, "", "create-in-team needs membership of " + ref + " (or org admin)"
		}
	}
	return team, orgID, ""
}
