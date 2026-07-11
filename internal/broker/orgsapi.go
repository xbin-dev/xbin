package broker

import (
	"net/http"
	"sort"
	"strings"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/events"
	"github.com/magik6k/xbin/internal/server"
	"github.com/magik6k/xbin/internal/users"
)

// Org/team management API (plans/orgs.md). Pure translation: decode → gate →
// users.Store method → encode; every rule lives in internal/users. Gates:
//
//   - workspace admin / xbin:users element (canManageUsers) — everything;
//   - a HUMAN org admin (session principal) — their org, minus the
//     workspace-security knobs (term flags, policy rows, org create/delete,
//     D21). Frame principals act as their element and never inherit the
//     driving user's org-adminship (plans/auth.md).
//
// GET /orgs is the MANAGEMENT view (orgs the caller may manage); a member's
// self-service view of their own orgs/teams is whoami's `orgs` field.

func (b *Broker) registerOrgs(srv *server.Server) {
	srv.RegisterAPI("GET /orgs", b.apiOrgsList)
	srv.RegisterAPI("POST /orgs", b.apiOrgCreate)
	srv.RegisterAPI("PATCH /orgs/{org}", b.apiOrgUpdate)
	srv.RegisterAPI("DELETE /orgs/{org}", b.apiOrgDelete)
	srv.RegisterAPI("POST /orgs/{org}/teams", b.apiTeamUpsert)
	srv.RegisterAPI("PATCH /orgs/{org}/teams/{team}", b.apiTeamUpsert)
	srv.RegisterAPI("DELETE /orgs/{org}/teams/{team}", b.apiTeamDelete)
	srv.RegisterAPI("GET /access", b.apiAccessGet)
	srv.RegisterAPI("PUT /access", b.apiAccessPut)
	srv.RegisterAPI("GET /access-matrix", b.apiAccessMatrix)
	srv.RegisterAPI("GET /users-directory", b.apiUsersDirectory)
	srv.RegisterAPI("GET /policy", b.apiPolicyGet)
	srv.RegisterAPI("PUT /policy", b.apiPolicyPut)
	srv.RegisterAPI("GET /orgs/{org}/policy", b.apiPolicyGet)
	srv.RegisterAPI("PUT /orgs/{org}/policy", b.apiPolicyPut)
}

// canManageOrg: full user-management capability, or a signed-in human who is
// an org admin of THIS org. Element principals (Component != "") only pass
// via their own grants — a tile never inherits the driving user's
// org-adminship (the frame-token rule).
func (b *Broker) canManageOrg(p auth.Principal, org string) bool {
	if b.canManageUsers(p) {
		return true
	}
	if p.Component != "" {
		return false
	}
	return p.Access.IsAdminOrg(org)
}

func (b *Broker) usersEvent() { b.Hub.Publish(events.Event{Type: "users"}) }

func (b *Broker) apiOrgsList(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	var out []users.Org
	switch {
	case b.canManageUsers(p):
		out = st.Orgs()
	case p.Component == "": // human: the orgs they administer
		out = []users.Org{}
		for _, o := range st.Orgs() {
			if p.Access.IsAdminOrg(o.ID) {
				out = append(out, o)
			}
		}
	default:
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "org management needs admin, org admin, or the xbin:users capability", "docs": "/docs/auth.md"})
		return
	}
	if out == nil {
		out = []users.Org{}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"orgs": out})
}

func (b *Broker) apiOrgCreate(w http.ResponseWriter, r *http.Request) {
	if !b.canManageUsers(auth.PrincipalOf(r)) { // org create/delete is workspace-level (D21)
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "creating orgs needs admin or the xbin:users capability"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		ID, Name       string
		Admins         []string
		Members        []string
		BasePermission string `json:"basePermission"`
	}
	if err := decodeJSON(r, &body); err != nil || strings.TrimSpace(body.ID) == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, name?, admins?, members?, basePermission?}"})
		return
	}
	if _, exists := st.Org(body.ID); exists {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "org already exists"})
		return
	}
	o, err := st.UpsertOrg(users.Org{ID: body.ID, Name: body.Name, Admins: body.Admins, Members: body.Members, BasePermission: body.BasePermission})
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, o)
}

func (b *Broker) apiOrgUpdate(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	id := r.PathValue("org")
	if !b.canManageOrg(auth.PrincipalOf(r), id) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "managing this org needs admin, its org admin, or the xbin:users capability"})
		return
	}
	cur, ok := st.Org(id)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
		return
	}
	var body struct {
		Name           *string   `json:"name"`
		Admins         *[]string `json:"admins"`
		Members        *[]string `json:"members"`
		BasePermission *string   `json:"basePermission"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	next := users.Org{ID: cur.ID, Name: cur.Name, Admins: cur.Admins, Members: cur.Members, BasePermission: cur.BasePermission}
	if body.Name != nil {
		next.Name = *body.Name
	}
	if body.Admins != nil {
		next.Admins = *body.Admins
	}
	if body.Members != nil {
		next.Members = *body.Members
	}
	if body.BasePermission != nil {
		next.BasePermission = *body.BasePermission
	}
	o, err := st.UpsertOrg(next)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, o)
}

func (b *Broker) apiOrgDelete(w http.ResponseWriter, r *http.Request) {
	if !b.canManageUsers(auth.PrincipalOf(r)) { // workspace-level (D21)
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "deleting orgs needs admin or the xbin:users capability"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	if err := st.DeleteOrg(r.PathValue("org")); err != nil {
		server.WriteJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// apiTeamUpsert serves both POST (create) and PATCH (update-by-overlay).
func (b *Broker) apiTeamUpsert(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	orgID := r.PathValue("org")
	if !b.canManageOrg(p, orgID) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "managing this org's teams needs admin, its org admin, or the xbin:users capability"})
		return
	}
	org, ok := st.Org(orgID)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
		return
	}
	var body struct {
		ID        string            `json:"id"`
		Name      *string           `json:"name"`
		Members   *[]string         `json:"members"`
		Tiles     map[string]string `json:"tiles"`
		CanCreate *[]string         `json:"canCreate"`
		NewTiles  *string           `json:"newTiles"`
		TermAPI   *bool             `json:"termApi"`
		TermNet   *bool             `json:"termNet"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	// Term flags are terminal-plane security — workspace-admin/xbin:users only (D21).
	if (body.TermAPI != nil || body.TermNet != nil) && !b.canManageUsers(p) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "termApi/termNet are workspace-admin settings (D21) — ask a workspace admin"})
		return
	}
	var cur *users.Team
	if r.Method == http.MethodPatch {
		if cur = org.Team(strings.ToLower(strings.TrimSpace(r.PathValue("team")))); cur == nil {
			server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such team"})
			return
		}
	} else if strings.TrimSpace(body.ID) == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, name?, members?, tiles?, canCreate?, newTiles?}"})
		return
	} else if org.Team(strings.ToLower(strings.TrimSpace(body.ID))) != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": "team already exists"})
		return
	}
	next := users.Team{ID: body.ID}
	if cur != nil { // PATCH: start from current, overlay provided fields
		next = *cur
	}
	if body.Name != nil {
		next.Name = *body.Name
	}
	if body.Members != nil {
		next.Members = *body.Members
	}
	if body.Tiles != nil {
		next.Tiles = body.Tiles
	}
	if body.CanCreate != nil {
		next.CanCreate = *body.CanCreate
	}
	if body.NewTiles != nil {
		next.NewTiles = *body.NewTiles
	}
	if body.TermAPI != nil {
		next.TermAPI = *body.TermAPI
	}
	if body.TermNet != nil {
		next.TermNet = *body.TermNet
	}
	t, err := st.UpsertTeam(orgID, next)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, t)
}

func (b *Broker) apiTeamDelete(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	orgID := r.PathValue("org")
	if !b.canManageOrg(auth.PrincipalOf(r), orgID) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "managing this org's teams needs admin, its org admin, or the xbin:users capability"})
		return
	}
	if err := st.DeleteTeam(orgID, r.PathValue("team")); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- per-tile access (the GitHub-style repo-access panel) -------------------

// accessEntry is one resolved row of a tile's ACL view.
type accessEntry struct {
	Kind   string `json:"kind"` // user | team | org
	ID     string `json:"id"`   // user id, <org>/<team>, or org id (base permission)
	Level  string `json:"level"`
	Source string `json:"source"` // exact | pattern:<pat> | base
}

// requireTileAccessMgmt gates the /access API for one tile: full user
// management, or a human org admin of the tile's org.
func (b *Broker) requireTileAccessMgmt(w http.ResponseWriter, p auth.Principal, tile string) bool {
	if b.canManageUsers(p) {
		return true
	}
	if org, ok := users.OrgOf(tile); ok && p.Component == "" && p.Access.IsAdminOrg(org) {
		return true
	}
	server.WriteJSON(w, http.StatusForbidden, map[string]string{
		"error": "tile access management needs admin, the tile's org admin, or the xbin:users capability", "docs": "/docs/auth.md",
	})
	return false
}

func (b *Broker) apiAccessGet(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need ?tile=<component path>"})
		return
	}
	if !b.requireTileAccessMgmt(w, auth.PrincipalOf(r), tile) {
		return
	}
	entries := []accessEntry{}
	// Users: every user's entries covering this tile.
	for _, u := range st.List() {
		if u.IsAdmin() {
			continue // admins hold everything implicitly; not ACL rows
		}
		for pat, level := range u.Tiles {
			switch {
			case pat == tile:
				entries = append(entries, accessEntry{Kind: "user", ID: u.ID, Level: level, Source: "exact"})
			case matchesTile(pat, tile):
				entries = append(entries, accessEntry{Kind: "user", ID: u.ID, Level: level, Source: "pattern:" + pat})
			}
		}
	}
	// Teams + base permission: only the tile's org can confer anything.
	org, inOrg := users.OrgOf(tile)
	var orgAdmins []string
	if inOrg {
		if o, ok := st.Org(org); ok {
			orgAdmins = o.Admins
			if o.BasePermission != "" {
				entries = append(entries, accessEntry{Kind: "org", ID: o.ID, Level: o.BasePermission, Source: "base"})
			}
			for _, t := range o.Teams {
				for pat, level := range t.Tiles {
					switch {
					case pat == tile:
						entries = append(entries, accessEntry{Kind: "team", ID: o.ID + "/" + t.ID, Level: level, Source: "exact"})
					case matchesTile(pat, tile):
						entries = append(entries, accessEntry{Kind: "team", ID: o.ID + "/" + t.ID, Level: level, Source: "pattern:" + pat})
					}
				}
			}
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].ID < entries[j].ID
	})
	out := map[string]any{"tile": tile, "entries": entries}
	if inOrg {
		out["org"] = org
		out["orgAdmins"] = orgAdmins
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func (b *Broker) apiAccessPut(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Tile  string `json:"tile"`
		Kind  string `json:"kind"` // user | team
		ID    string `json:"id"`
		Level string `json:"level"` // read|write|terminal, "" = remove
	}
	if err := decodeJSON(r, &body); err != nil || body.Tile == "" || body.ID == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tile, kind: user|team, id, level: read|write|terminal|\"\"}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	if !b.requireTileAccessMgmt(w, auth.PrincipalOf(r), body.Tile) {
		return
	}
	var err error
	switch body.Kind {
	case "user":
		err = st.SetUserTile(body.ID, body.Tile, body.Level)
	case "team":
		var org, team string
		if org, team, err = users.ParseTeamRef(body.ID); err == nil {
			// A team entry only makes sense on its own org's tiles — anywhere
			// else the evaluation clamp would make it inert.
			if tileOrg, ok := users.OrgOf(body.Tile); !ok || tileOrg != org {
				server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "team " + body.ID + " cannot be assigned outside org " + org})
				return
			}
			err = st.SetTeamTile(org, team, body.Tile, body.Level)
		}
	default:
		err = errBadKind
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

var errBadKind = jsonError("kind must be user or team")

type jsonError string

func (e jsonError) Error() string { return string(e) }

// apiAccessMatrix resolves the whole users × tiles effective-access matrix
// with full provenance (users.Access.Explain) — the admin tile's access-map
// view. Small workspaces make this cheap (users × tiles evaluations); cells
// exist only where the effective level is non-empty. Chrome (root/shell —
// always viewable, outside the level model) and template blueprints are
// skipped.
func (b *Broker) apiAccessMatrix(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var tiles []string
	for _, c := range b.Reg.Components() {
		if c.Path == "root" || c.Path == "shell" || c.IsTemplate() {
			continue
		}
		tiles = append(tiles, c.Path)
	}
	sort.Strings(tiles)
	type cell struct {
		Level string               `json:"level"`
		Via   []users.Contribution `json:"via"`
	}
	type userRow struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	rows := []userRow{}
	cells := map[string]map[string]cell{}
	for _, u := range st.List() {
		rows = append(rows, userRow{ID: u.ID, Name: u.Name, Role: u.Role})
		acc, ok := st.Access(u.ID)
		if !ok {
			continue
		}
		uc := map[string]cell{}
		for _, tile := range tiles {
			if via := acc.Explain(tile); len(via) > 0 {
				uc[tile] = cell{Level: via[0].Level, Via: via}
			}
		}
		if len(uc) > 0 {
			cells[u.ID] = uc
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"users": rows, "tiles": tiles, "cells": cells})
}

// apiUsersDirectory is the minimal people list ({id, name} — no roles, no
// grants, no hashes) that click-through pickers run on. Gated wider than
// /users on purpose: org admins add existing accounts to their org/teams, so
// they may enumerate ids — but nothing else about them.
func (b *Broker) apiUsersDirectory(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !b.canManageUsers(p) && !(p.Component == "" && len(p.Access.AdminOrgs()) > 0) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "the user directory needs admin, an org admin, or the xbin:users capability"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	type entry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	out := []entry{}
	for _, u := range st.List() {
		out = append(out, entry{ID: u.ID, Name: u.Name})
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"users": out})
}

// --- policy rows (workspace-admin only, D21) ---------------------------------

func (b *Broker) apiPolicyGet(w http.ResponseWriter, r *http.Request) {
	if !b.canManageUsers(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "policy is workspace-admin only (D21)"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var rows []users.PolicyRow
	if org := r.PathValue("org"); org != "" {
		o, ok := st.Org(org)
		if !ok {
			server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
			return
		}
		rows = o.Policy
	} else {
		rows = st.Policy()
	}
	if rows == nil {
		rows = []users.PolicyRow{}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"policy": rows})
}

func (b *Broker) apiPolicyPut(w http.ResponseWriter, r *http.Request) {
	if !b.canManageUsers(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "policy is workspace-admin only (D21)"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Policy []users.PolicyRow `json:"policy"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Policy == nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {policy: [{tiles, deny?, mayCall?}]} ([] clears)"})
		return
	}
	var err error
	if org := r.PathValue("org"); org != "" {
		err = st.SetOrgPolicy(org, body.Policy)
	} else {
		err = st.SetPolicy(body.Policy)
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// matchesTile mirrors users.matchTile for the ACL view's PATTERN rows (exact
// entries are classified separately above).
func matchesTile(pat, path string) bool {
	if pat == "*" {
		return true
	}
	if strings.HasSuffix(pat, "/*") {
		prefix := strings.TrimSuffix(pat, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return false
}
