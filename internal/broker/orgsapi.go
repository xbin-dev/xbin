package broker

import (
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// Ownership / org management API (plans/ownership.md, D24–D28). Pure
// translation: decode → gate → users.Store method → encode; every rule lives
// in internal/users. Gates:
//
//   - workspace admin / xbin:users element (canManageUsers) — everything;
//   - a HUMAN org admin (session principal) — their org's members, shares and
//     owned-tile ACLs/transfers; NEVER the ws-security knobs (permission
//     sets, allowances, policy rows, org create/delete);
//   - a tile's USER-OWNER — that tile's ACL and transfers (D24).
//
// Frame principals act as their element and never inherit the driving user's
// org-adminship or ownership (plans/auth.md).

func (b *Broker) registerOrgs(srv *server.Server) {
	srv.RegisterAPI("GET /orgs", b.apiOrgsList)
	srv.RegisterAPI("POST /orgs", b.apiOrgCreate)
	srv.RegisterAPI("PATCH /orgs/{org}", b.apiOrgUpdate)
	srv.RegisterAPI("DELETE /orgs/{org}", b.apiOrgDelete)
	srv.RegisterAPI("GET /permission-sets", b.apiPermSetsList)
	srv.RegisterAPI("PUT /permission-sets/{name}", b.apiPermSetPut)
	srv.RegisterAPI("DELETE /permission-sets/{name}", b.apiPermSetDelete)
	srv.RegisterAPI("GET /owner", b.apiOwnerGet)
	srv.RegisterAPI("POST /owner", b.apiOwnerTransfer)
	srv.RegisterAPI("GET /access", b.apiAccessGet)
	srv.RegisterAPI("PUT /access", b.apiAccessPut)
	srv.RegisterAPI("GET /access-matrix", b.apiAccessMatrix)
	srv.RegisterAPI("GET /users-directory", b.apiUsersDirectory)
	srv.RegisterAPI("GET /policy", b.apiPolicyGet)
	srv.RegisterAPI("PUT /policy", b.apiPolicyPut)
	srv.RegisterAPI("GET /orgs/{org}/policy", b.apiPolicyGet)
	srv.RegisterAPI("PUT /orgs/{org}/policy", b.apiPolicyPut)
	srv.RegisterAPI("GET /defaults", b.apiDefaultsGet)
	srv.RegisterAPI("PUT /defaults", b.apiDefaultsPut)
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

// humanID is the acting human behind a session principal ("" for elements —
// ownership rights never ride a frame token).
func humanID(p auth.Principal) string {
	if p.Component != "" || p.User == nil {
		return ""
	}
	return p.User.ID
}

func (b *Broker) usersEvent() { b.Hub.Publish(events.Event{Type: "users"}) }

// orgView is the management-facing org shape: the org plus its resolved
// allowance (what its admins may self-approve, D26) and the tiles it owns.
type orgView struct {
	users.Org
	ResolvedAllow []string `json:"resolvedAllow"`
	OwnedTiles    []string `json:"ownedTiles"`
}

func (b *Broker) orgView(o users.Org) orgView {
	return orgView{
		Org:           o,
		ResolvedAllow: b.Users.ResolvedAllow(o.ID),
		OwnedTiles:    b.Users.OwnedBy(users.OwnerKindOrg + ":" + o.ID),
	}
}

// GET /orgs — the MANAGEMENT view: ws-admin sees all orgs, an org admin their
// orgs. (Members' self-service view is whoami's `orgs`.)
func (b *Broker) apiOrgsList(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	out := []orgView{}
	for _, o := range st.Orgs() {
		if b.canManageOrg(p, o.ID) {
			out = append(out, b.orgView(o))
		}
	}
	if len(out) == 0 && !b.canManageUsers(p) && humanID(p) == "" {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "org management is admin/org-admin only", "docs": "/docs/auth.md"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"orgs": out})
}

func (b *Broker) apiOrgCreate(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ID == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {id, name?}"})
		return
	}
	o, err := st.UpsertOrg(users.Org{ID: body.ID, Name: body.Name})
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, b.orgView(*o))
}

// PATCH /orgs/{org} — {name?, members?} by org admins; the security knobs
// {sets?, allow?} are ws-admin only (D26/D28: delegation is granted from
// above, never self-served).
func (b *Broker) apiOrgUpdate(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	org := r.PathValue("org")
	p := auth.PrincipalOf(r)
	if !b.canManageOrg(p, org) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "org management needs workspace admin or org admin", "docs": "/docs/auth.md"})
		return
	}
	cur, ok := st.Org(org)
	if !ok {
		server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
		return
	}
	var body struct {
		Name    *string        `json:"name"`
		Members []users.Member `json:"members"`
		Sets    []string       `json:"sets"`
		Allow   []string       `json:"allow"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	if (body.Sets != nil || body.Allow != nil) && !b.canManageUsers(p) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{
			"error": "permission sets and allowances are workspace-admin only — delegation is granted from above (D26)", "docs": "/docs/auth.md"})
		return
	}
	up := *cur
	if body.Name != nil {
		up.Name = *body.Name
	}
	if body.Members != nil {
		up.Members = body.Members
	}
	if _, err := st.UpsertOrg(up); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if body.Sets != nil {
		if err := st.SetOrgSets(org, body.Sets); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if body.Allow != nil {
		if err := st.SetOrgAllow(org, body.Allow); err != nil {
			server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	b.usersEvent()
	o, _ := st.Org(org)
	server.WriteJSON(w, http.StatusOK, b.orgView(*o))
}

func (b *Broker) apiOrgDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	if err := st.DeleteOrg(r.PathValue("org")); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- permission sets (D28, ws-admin only) -----------------------------------

func (b *Broker) apiPermSetsList(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	attached := map[string][]string{}
	for _, o := range st.Orgs() {
		for _, n := range o.Sets {
			attached[n] = append(attached[n], o.ID)
		}
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"sets": st.PermissionSets(), "attachedTo": attached})
}

func (b *Broker) apiPermSetPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body users.PermissionSet
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "bad body"})
		return
	}
	if err := st.UpsertPermissionSet(r.PathValue("name"), body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

func (b *Broker) apiPermSetDelete(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	if err := st.DeletePermissionSet(r.PathValue("name")); err != nil {
		server.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- ownership (D24) ---------------------------------------------------------

// mayManageTile: ws-admin, the tile's user-owner, or an org admin of the
// owning org — the principal set that edits a tile's ACL and transfers it.
func (b *Broker) mayManageTile(p auth.Principal, tile string) bool {
	if b.canManageUsers(p) {
		return true
	}
	uid := humanID(p)
	if uid == "" || b.Users == nil {
		return false
	}
	owner := b.Users.Owner(tile)
	if owner == users.OwnerKindUser+":"+uid {
		return true
	}
	if org, ok := strings.CutPrefix(owner, users.OwnerKindOrg+":"); ok {
		return p.Access.IsAdminOrg(org)
	}
	return false
}

func (b *Broker) apiOwnerGet(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	p := auth.PrincipalOf(r)
	if tile == "" || !p.CanReadTile(tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "no access to this tile"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]string{"tile": tile, "owner": st.Owner(tile)})
}

// POST /owner {tile, to} — transfer (D24). ws-admin: anywhere. A user-owner:
// to an org they belong to (user→user / →workspace is a ws-admin act). An
// org admin of the owning org: to another org they admin, or to a member of
// the org.
func (b *Broker) apiOwnerTransfer(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Tile string `json:"tile"`
		To   string `json:"to"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tile, to: \"user:<id>\"|\"org:<id>\"|\"\"}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	toKind, toID, err := users.ParseOwner(body.To)
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	p := auth.PrincipalOf(r)
	if !b.canManageUsers(p) {
		uid := humanID(p)
		allowed := false
		if uid != "" {
			owner := st.Owner(body.Tile)
			switch {
			case owner == users.OwnerKindUser+":"+uid:
				if toKind == users.OwnerKindOrg {
					for _, m := range st.UserOrgs(uid) {
						if m.ID == toID {
							allowed = true
							break
						}
					}
				}
			default:
				if org, isOrg := strings.CutPrefix(owner, users.OwnerKindOrg+":"); isOrg && p.Access.IsAdminOrg(org) {
					switch toKind {
					case users.OwnerKindOrg:
						allowed = p.Access.IsAdminOrg(toID) // org→org: adminship of both
					case users.OwnerKindUser:
						if o, found := st.Org(org); found {
							_, allowed = o.Member(toID) // org→user: a member of the org
						}
					}
				}
			}
		}
		if !allowed {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{
				"error": "not allowed: owners may transfer to their orgs; org admins within/between their orgs; anything else is workspace-admin", "docs": "/docs/auth.md"})
			return
		}
	}
	if err := st.SetOwner(body.Tile, body.To); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("owner transfer", "tile", body.Tile, "to", body.To)
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"tile": body.Tile, "owner": body.To})
}

// --- per-tile ACL (/access) --------------------------------------------------

type accessEntry struct {
	Kind   string `json:"kind"` // user | org
	ID     string `json:"id"`
	Level  string `json:"level"`
	Source string `json:"source"` // exact | pattern:<pat>
}

// GET /access?tile= — the tile's ACL: its owner plus every user/org entry
// that reaches it. Viewable by whoever may manage the tile.
func (b *Broker) apiAccessGet(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	if tile == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need ?tile="})
		return
	}
	p := auth.PrincipalOf(r)
	if !b.mayManageTile(p, tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "tile access is managed by its owner, the owning org's admins, or a workspace admin", "docs": "/docs/auth.md"})
		return
	}
	entries := []accessEntry{}
	for _, u := range st.List() {
		for pat, l := range u.Tiles {
			if !matchesTile(pat, tile) {
				continue
			}
			src := "exact"
			if pat != tile {
				src = "pattern:" + pat
			}
			entries = append(entries, accessEntry{Kind: "user", ID: u.ID, Level: l, Source: src})
		}
	}
	for _, o := range st.Orgs() {
		for pat, l := range o.Tiles {
			if !matchesTile(pat, tile) {
				continue
			}
			src := "exact"
			if pat != tile {
				src = "pattern:" + pat
			}
			entries = append(entries, accessEntry{Kind: "org", ID: o.ID, Level: l, Source: src})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Kind != entries[j].Kind {
			return entries[i].Kind < entries[j].Kind
		}
		return entries[i].ID < entries[j].ID
	})
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"tile": tile, "owner": st.Owner(tile), "entries": entries,
	})
}

// matchesTile mirrors users.matchTile for the API's provenance display.
func matchesTile(pat, path string) bool {
	if pat == "*" || pat == path {
		return true
	}
	if strings.HasSuffix(pat, "/*") {
		prefix := strings.TrimSuffix(pat, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return false
}

// PUT /access {tile, kind: user|org, id, level|""} — writes/clears an EXACT
// entry (user.Tiles / org.Tiles). Gated by mayManageTile (D24: sharing is an
// ownership right).
func (b *Broker) apiAccessPut(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Tile  string `json:"tile"`
		Kind  string `json:"kind"`
		ID    string `json:"id"`
		Level string `json:"level"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Tile == "" || body.ID == "" {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {tile, kind: user|org, id, level|\"\"}"})
		return
	}
	body.Tile = strings.Trim(body.Tile, "/")
	p := auth.PrincipalOf(r)
	if !b.mayManageTile(p, body.Tile) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "tile access is managed by its owner, the owning org's admins, or a workspace admin", "docs": "/docs/auth.md"})
		return
	}
	var err error
	switch body.Kind {
	case "user":
		err = st.SetUserTile(body.ID, body.Tile, body.Level)
	case "org":
		err = st.SetOrgTile(body.ID, body.Tile, body.Level)
	default:
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be user or org"})
		return
	}
	if err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	slog.Info("access set", "tile", body.Tile, "kind", body.Kind, "id", body.ID, "level", body.Level)
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}

// --- access matrix (ws-admin console) ---------------------------------------

// GET /access-matrix — users × components effective levels with provenance
// (users.Access.Explain) — the admin tile's access-map data.
func (b *Broker) apiAccessMatrix(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	comps := b.Reg.Components()
	paths := make([]string, 0, len(comps))
	for _, c := range comps {
		paths = append(paths, c.Path)
	}
	sort.Strings(paths)
	type cell struct {
		Level   string               `json:"level"`
		Explain []users.Contribution `json:"explain,omitempty"`
	}
	matrix := map[string]map[string]cell{}
	userList := []map[string]string{}
	for _, u := range st.List() {
		userList = append(userList, map[string]string{"id": u.ID, "name": u.Name, "role": u.Role})
		acc, ok := st.Access(u.ID)
		if !ok {
			continue
		}
		row := map[string]cell{}
		for _, path := range paths {
			if l := acc.TileLevel(path); l != "" {
				row[path] = cell{Level: l, Explain: acc.Explain(path)}
			} else if ex := acc.Explain(path); len(ex) > 0 && ex[0].Level == users.LevelNone {
				// An exact `none` EXCLUSION is a deliberate row — render it,
				// don't flatten it into "never granted" (D31 legibility).
				row[path] = cell{Level: users.LevelNone, Explain: ex}
			}
		}
		matrix[u.ID] = row
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{
		"users": userList, "components": paths, "matrix": matrix,
		"owners": st.Owners(),
	})
}

// GET /users-directory — minimal id+name listing so org admins can add
// members without the admin-only GET /users (plans/ownership.md).
func (b *Broker) apiUsersDirectory(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	if !b.canManageUsers(p) && !(p.Component == "" && len(p.Access.AdminOrgs()) > 0) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "the user directory needs admin, an org admin, or the xbin:users capability"})
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

// --- policy rows (workspace-admin only) --------------------------------------

func (b *Broker) apiPolicyGet(w http.ResponseWriter, r *http.Request) {
	st := b.usersStore(w)
	if st == nil {
		return
	}
	p := auth.PrincipalOf(r)
	if org := r.PathValue("org"); org != "" {
		// Org rows are READABLE by that org's admins too — they need to see
		// the ceilings their approvals can trip on (writes stay ws-admin).
		if !b.canManageOrg(p, org) {
			server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "org policy is readable by its admins; edits are workspace-admin only"})
			return
		}
		o, ok := st.Org(org)
		if !ok {
			server.WriteJSON(w, http.StatusNotFound, map[string]string{"error": "no such org"})
			return
		}
		server.WriteJSON(w, http.StatusOK, map[string]any{"policy": o.Policy})
		return
	}
	if !b.canManageUsers(p) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "policy is workspace-admin only"})
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"policy": st.Policy()})
}

func (b *Broker) apiPolicyPut(w http.ResponseWriter, r *http.Request) {
	if !b.canManageUsers(auth.PrincipalOf(r)) {
		server.WriteJSON(w, http.StatusForbidden, map[string]string{"error": "policy is workspace-admin only"})
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		Policy []users.PolicyRow `json:"policy"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {policy: [rows]}"})
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

// --- workspace defaults (D27, ws-admin) --------------------------------------

func (b *Broker) apiDefaultsGet(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	server.WriteJSON(w, http.StatusOK, map[string]any{"defaultTiles": st.DefaultTiles()})
}

func (b *Broker) apiDefaultsPut(w http.ResponseWriter, r *http.Request) {
	if !b.requireUsersCap(w, r) {
		return
	}
	st := b.usersStore(w)
	if st == nil {
		return
	}
	var body struct {
		DefaultTiles map[string]string `json:"defaultTiles"`
	}
	if err := decodeJSON(r, &body); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": "need {defaultTiles: {pattern: level}}"})
		return
	}
	if err := st.SetDefaultTiles(body.DefaultTiles); err != nil {
		server.WriteJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	b.usersEvent()
	server.WriteJSON(w, http.StatusOK, map[string]string{"ok": "true"})
}
