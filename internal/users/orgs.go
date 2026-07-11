// Orgs & teams (plans/orgs.md, DECISIONS D19–D21): GitHub-shaped grouping
// layered on the flat user store. An org OWNS a path namespace positionally —
// the reserved `o` segment (`o/<org>/…` or `<seg>/o/<org>/…`) binds a tile to
// its org, so "whose tile is this" is readable off the path with no ownership
// table to drift. Teams live inside orgs and only ever WIDEN a member's
// access (union, exactly like a user's own tile entries); org policy rows are
// the runtime-plane ceiling on what the org's tiles may be granted (evaluated
// here in Ceiling, asked at the broker chokepoints).
//
// This file is the whole semantic core: everything above it (auth.Principal,
// the broker gates, the HTTP API) only asks questions answered here.
package users

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Policy deny kinds (D20): the capability classes a policy row may strip from
// the tiles it covers.
const (
	PolicyDenyNet      = "net"       // net interface bindings (internet/host/lan/provider)
	PolicyDenyGPU      = "gpu"       // gpu:* grants
	PolicyDenyXbinCaps = "xbin-caps" // the reserved xbin / xbin:* capability targets
)

var policyDenyKinds = map[string]bool{PolicyDenyNet: true, PolicyDenyGPU: true, PolicyDenyXbinCaps: true}

// PolicyRow is one pattern-keyed ceiling row (D20): for tiles covered by the
// Tiles pattern, Deny strips capability classes outright, and MayCall (when
// present) allow-lists grant targets (component paths / res:… ids). Rows
// compose restrictively across the workspace and org level: any deny wins,
// and every MayCall-bearing matching row must be satisfied.
type PolicyRow struct {
	Tiles   string   `json:"tiles"`
	Deny    []string `json:"deny,omitempty"`
	MayCall []string `json:"mayCall,omitempty"`
}

// Team is a named group of org members with union-only grants (D19): its
// Tiles/CanCreate patterns confer access exactly like a user's own entries,
// but only on paths inside the team's org (the evaluation clamp in Access).
type Team struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []string `json:"members,omitempty"`
	// Tiles maps path patterns to levels, like User.Tiles; entries take
	// effect only on paths whose OrgOf is this team's org.
	Tiles     map[string]string `json:"tiles,omitempty"`
	CanCreate []string          `json:"canCreate,omitempty"`
	// NewTiles is the level the team is auto-granted on a tile created "in
	// the team" (create-in-team, D19). Defaults to write.
	NewTiles string `json:"newTiles,omitempty"`
	// TermAPI/TermNet confer the D17 terminal-plane grants on members (union
	// with their user flags). Workspace-admin-set only — org admins cannot
	// change them (D21).
	TermAPI bool  `json:"termApi,omitempty"`
	TermNet bool  `json:"termNet,omitempty"`
	Created int64 `json:"created"`
}

// Org is one organization: members, delegated org-admins (D21), teams, an
// optional base permission every member gets on org tiles, and the policy
// ceiling for the org's tiles. Admins are implicitly members.
type Org struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Admins  []string `json:"admins,omitempty"`
	Members []string `json:"members,omitempty"`
	// BasePermission is ""|read|write — the floor every member holds on every
	// org tile (GitHub base permissions). Terminal is never implicit.
	BasePermission string      `json:"basePermission,omitempty"`
	Policy         []PolicyRow `json:"policy,omitempty"` // workspace-admin-managed (D21)
	Teams          []*Team     `json:"teams,omitempty"`
	Created        int64       `json:"created"`
}

// Team returns the team with id, or nil.
func (o *Org) Team(id string) *Team {
	for _, t := range o.Teams {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// effMember reports org membership including implicit admin membership.
func (o *Org) effMember(userID string) bool {
	return contains(o.Members, userID) || contains(o.Admins, userID)
}

// OrgOf reports the org a component path belongs to, bound positionally by
// the reserved `o` segment: `o/<org>/…` or `<seg>/o/<org>/…` (so
// `apps/o/sales/crm` → sales). Nothing else is org-owned.
func OrgOf(path string) (string, bool) {
	segs := strings.Split(path, "/")
	if len(segs) >= 2 && segs[0] == "o" && segs[1] != "" {
		return segs[1], true
	}
	if len(segs) >= 3 && segs[1] == "o" && segs[2] != "" {
		return segs[2], true
	}
	return "", false
}

// ValidateNewTilePath gates tile-CREATION paths (create/clone/import — never
// existing dirs): the segments `o` and `u` are reserved. `o` is valid only in
// the two org-marker positions and only naming an existing org (no namespace
// squatting before the org exists); `u` is reserved outright for future
// per-user tiles. `bx doctor` warns about pre-existing paths that would no
// longer validate.
func (s *Store) ValidateNewTilePath(path string) error {
	segs := strings.Split(path, "/")
	for i, seg := range segs {
		switch seg {
		case "u":
			return fmt.Errorf("path segment \"u\" is reserved (future per-user tiles)")
		case "o":
			if i > 1 {
				return fmt.Errorf("the org marker \"o\" is only valid as o/<org>/… or <dir>/o/<org>/…")
			}
			if i+1 >= len(segs) || segs[i+1] == "" {
				return fmt.Errorf("org marker \"o\" must be followed by an org id")
			}
			if _, ok := s.Org(segs[i+1]); !ok {
				return fmt.Errorf("no such org %q — create the org first", segs[i+1])
			}
			if i+1 == len(segs)-1 {
				// A tile AT the org container would block (or nest with)
				// every tile of the org — tiles live strictly below it.
				return fmt.Errorf("%s is org %q's container directory — create tiles under it (e.g. %s/<name>)", path, segs[i+1], path)
			}
		}
	}
	return nil
}

// ParseTeamRef splits the external "<org>/<team>" team reference.
func ParseTeamRef(ref string) (org, team string, err error) {
	org, team, ok := strings.Cut(ref, "/")
	if !ok || org == "" || team == "" || strings.Contains(team, "/") {
		return "", "", fmt.Errorf("team reference must be <org>/<team>, got %q", ref)
	}
	return normalizeID(org), normalizeID(team), nil
}

// reservedOrgIDs: `o`/`u` are the marker segments themselves; "workspace" is
// the UI/API label for the non-org plane.
var reservedOrgIDs = map[string]bool{"o": true, "u": true, "workspace": true}

// --- org / team CRUD -------------------------------------------------------

func (s *Store) Orgs() []Org {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Org, 0, len(s.orgs))
	for _, o := range s.orgs {
		out = append(out, *o)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Org(id string) (*Org, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	o, ok := s.orgs[normalizeID(id)]
	if !ok {
		return nil, false
	}
	c := *o
	return &c, true
}

// UpsertOrg creates or updates an org's own fields (name, admins, members,
// basePermission). Teams and Policy are managed by their own methods and
// survive an update. A member removed here is also stripped from the org's
// teams (GitHub semantics: leaving the org leaves its teams).
func (s *Store) UpsertOrg(o Org) (*Org, error) {
	o.ID = normalizeID(o.ID)
	if o.BasePermission != "" && o.BasePermission != LevelRead && o.BasePermission != LevelWrite {
		return nil, fmt.Errorf("basePermission must be empty, %q or %q (terminal is never org-wide)", LevelRead, LevelWrite)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing := s.orgs[o.ID]
	if existing == nil {
		if err := validID(o.ID); err != nil {
			return nil, err
		}
		if reservedOrgIDs[o.ID] {
			return nil, fmt.Errorf("org id %q is reserved", o.ID)
		}
	}
	var err error
	if o.Admins, err = s.normUsersLocked(o.Admins); err != nil {
		return nil, err
	}
	if o.Members, err = s.normUsersLocked(o.Members); err != nil {
		return nil, err
	}
	if strings.TrimSpace(o.Name) == "" {
		o.Name = o.ID
	}
	if existing != nil {
		o.Created = existing.Created
		o.Policy = existing.Policy
		o.Teams = teamsWithMembersOf(existing.Teams, &o)
	} else {
		o.Created = time.Now().Unix()
		o.Policy = nil
		o.Teams = nil
	}
	no := o
	s.orgs[no.ID] = &no
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := no
	return &c, nil
}

// teamsWithMembersOf rebuilds a team list keeping only members of org (COW —
// the input teams are never mutated).
func teamsWithMembersOf(teams []*Team, org *Org) []*Team {
	out := make([]*Team, 0, len(teams))
	for _, t := range teams {
		nt := *t
		nt.Members = filter(t.Members, org.effMember)
		out = append(out, &nt)
	}
	return out
}

func (s *Store) DeleteOrg(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.orgs, normalizeID(id))
	return s.persistLocked()
}

// UpsertTeam creates or updates a team inside an org. Members must be org
// members (admins count); tile levels and the NewTiles level are validated.
func (s *Store) UpsertTeam(orgID string, t Team) (*Team, error) {
	orgID, t.ID = normalizeID(orgID), normalizeID(t.ID)
	for pat, l := range t.Tiles {
		if levelRank(l) == 0 {
			return nil, fmt.Errorf("tiles[%q]: unknown level %q (want read|write|terminal)", pat, l)
		}
	}
	if t.NewTiles == "" {
		t.NewTiles = LevelWrite
	}
	if levelRank(t.NewTiles) == 0 {
		return nil, fmt.Errorf("newTiles: unknown level %q (want read|write|terminal)", t.NewTiles)
	}
	if t.Tiles == nil {
		t.Tiles = map[string]string{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[orgID]
	if org == nil {
		return nil, fmt.Errorf("no such org %q", orgID)
	}
	existing := org.Team(t.ID)
	if existing == nil {
		if err := validID(t.ID); err != nil {
			return nil, err
		}
	}
	var err error
	if t.Members, err = s.normUsersLocked(t.Members); err != nil {
		return nil, err
	}
	for _, m := range t.Members {
		if !org.effMember(m) {
			return nil, fmt.Errorf("user %q is not a member of org %q", m, orgID)
		}
	}
	if strings.TrimSpace(t.Name) == "" {
		t.Name = t.ID
	}
	if existing != nil {
		t.Created = existing.Created
	} else {
		t.Created = time.Now().Unix()
	}
	nt := t
	no := *org
	no.Teams = replaceTeam(org.Teams, &nt)
	s.orgs[orgID] = &no
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := nt
	return &c, nil
}

// replaceTeam returns a new sorted team list with t replacing/added (COW).
func replaceTeam(teams []*Team, t *Team) []*Team {
	out := make([]*Team, 0, len(teams)+1)
	for _, e := range teams {
		if e.ID != t.ID {
			out = append(out, e)
		}
	}
	out = append(out, t)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) DeleteTeam(orgID, teamID string) error {
	orgID, teamID = normalizeID(orgID), normalizeID(teamID)
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[orgID]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	no := *org
	no.Teams = make([]*Team, 0, len(org.Teams))
	for _, t := range org.Teams {
		if t.ID != teamID {
			no.Teams = append(no.Teams, t)
		}
	}
	s.orgs[orgID] = &no
	return s.persistLocked()
}

// GrantTileTeam raises a team's level on one tile (never lowers — same
// monotone rule as GrantTile). Used by create-in-team: the team gets its
// NewTiles level on the tile a member just created (D19).
func (s *Store) GrantTileTeam(orgID, teamID, path, level string) error {
	if levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q", level)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	t := org.Team(normalizeID(teamID))
	if t == nil {
		return fmt.Errorf("no such team %q in org %q", teamID, orgID)
	}
	if levelRank(t.Tiles[path]) >= levelRank(level) {
		return nil // already at or above
	}
	nt := *t
	nt.Tiles = make(map[string]string, len(t.Tiles)+1)
	for k, v := range t.Tiles {
		nt.Tiles[k] = v
	}
	nt.Tiles[path] = level
	no := *org
	no.Teams = replaceTeam(org.Teams, &nt)
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// --- policy rows ------------------------------------------------------------

func validatePolicy(rows []PolicyRow) error {
	for i, r := range rows {
		if strings.TrimSpace(r.Tiles) == "" {
			return fmt.Errorf("policy[%d]: tiles pattern required", i)
		}
		for _, d := range r.Deny {
			if !policyDenyKinds[d] {
				return fmt.Errorf("policy[%d]: unknown deny kind %q (want net|gpu|xbin-caps)", i, d)
			}
		}
		for _, m := range r.MayCall {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("policy[%d]: empty mayCall pattern", i)
			}
		}
	}
	return nil
}

// Policy returns the workspace-level ceiling rows.
func (s *Store) Policy() []PolicyRow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]PolicyRow(nil), s.policy...)
}

// SetPolicy replaces the workspace-level ceiling rows.
func (s *Store) SetPolicy(rows []PolicyRow) error {
	if err := validatePolicy(rows); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.policy = append([]PolicyRow(nil), rows...)
	return s.persistLocked()
}

// SetOrgPolicy replaces one org's ceiling rows (workspace-admin data, D21).
func (s *Store) SetOrgPolicy(orgID string, rows []PolicyRow) error {
	if err := validatePolicy(rows); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	no := *org
	no.Policy = append([]PolicyRow(nil), rows...)
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// --- Access: the one per-user resolver --------------------------------------

// accessTeam is the slice of a team one user gets: the team's grants plus the
// org clamp they evaluate under.
type accessTeam struct {
	org              string
	tiles            map[string]string
	canCreate        []string
	termAPI, termNet bool
}

// Access is a per-request snapshot of everything one user may do: their own
// entries unioned with their teams' (org-clamped), org base permissions, and
// implicit org-admin power (D19/D21). Reads shared COW maps — treat as
// read-only. Nil-safe: a nil *Access answers no to everything.
type Access struct {
	user       *User
	memberOrgs map[string]string // org id → basePermission ("" = none); admins included
	adminOrgs  map[string]bool
	teams      []accessTeam
}

// Access resolves the snapshot for one user ("" or unknown → nil, false).
func (s *Store) Access(id string) (*Access, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[normalizeID(id)]
	if !ok {
		return nil, false
	}
	uc := *u
	a := &Access{user: &uc, memberOrgs: map[string]string{}, adminOrgs: map[string]bool{}}
	for _, o := range s.orgs {
		if !o.effMember(uc.ID) {
			continue // eval clamp: stray team entries of non-members are inert
		}
		a.memberOrgs[o.ID] = o.BasePermission
		if contains(o.Admins, uc.ID) {
			a.adminOrgs[o.ID] = true
		}
		for _, t := range o.Teams {
			if contains(t.Members, uc.ID) {
				a.teams = append(a.teams, accessTeam{
					org: o.ID, tiles: t.Tiles, canCreate: t.CanCreate,
					termAPI: t.TermAPI, termNet: t.TermNet,
				})
			}
		}
	}
	return a, true
}

// TileLevel is the effective access level on one path: max over the user's
// direct entries, their teams' entries (only inside the team's org), the org
// base permission, and implicit org-admin terminal (D21) — workspace admins
// have terminal everywhere.
func (a *Access) TileLevel(path string) string {
	if a == nil {
		return ""
	}
	if a.user.IsAdmin() {
		return LevelTerminal
	}
	best := ""
	up := func(l string) {
		if levelRank(l) > levelRank(best) {
			best = l
		}
	}
	for pat, l := range a.user.Tiles {
		if matchTile(pat, path) {
			up(l)
		}
	}
	if org, ok := OrgOf(path); ok {
		if a.adminOrgs[org] {
			return LevelTerminal // org admin: full control of the org's tiles
		}
		if base, member := a.memberOrgs[org]; member && base != "" {
			up(base)
		}
		for _, t := range a.teams {
			if t.org != org {
				continue // the evaluation clamp: teams never reach outside their org
			}
			for pat, l := range t.tiles {
				if matchTile(pat, path) {
					up(l)
				}
			}
		}
	}
	return best
}

func (a *Access) CanReadTile(path string) bool {
	return levelRank(a.TileLevel(path)) >= levelRank(LevelRead)
}
func (a *Access) CanWriteTile(path string) bool {
	return levelRank(a.TileLevel(path)) >= levelRank(LevelWrite)
}
func (a *Access) CanTerminalTile(path string) bool {
	return levelRank(a.TileLevel(path)) >= levelRank(LevelTerminal)
}

// CanCreateTile: the user's own patterns, their teams' (org-clamped), or
// implicit org-admin create inside the org. Creating INSIDE an org
// additionally requires membership of that org (D19 amendment): a broad
// personal pattern like apps/* must not let a non-member inject tiles into
// apps/o/<org>/… — unlike read/write access, where workspace-admin-granted
// personal patterns deliberately stay global (the auditor case).
func (a *Access) CanCreateTile(path string) bool {
	if a == nil {
		return false
	}
	if a.user.IsAdmin() {
		return true
	}
	if org, ok := OrgOf(path); ok {
		if a.adminOrgs[org] {
			return true
		}
		if _, member := a.memberOrgs[org]; !member {
			return false // non-members never create in an org, whatever their patterns
		}
		for _, pat := range a.user.CanCreate {
			if matchTile(pat, path) {
				return true
			}
		}
		for _, t := range a.teams {
			if t.org != org {
				continue
			}
			for _, pat := range t.canCreate {
				if matchTile(pat, path) {
					return true
				}
			}
		}
		return false
	}
	for _, pat := range a.user.CanCreate {
		if matchTile(pat, path) {
			return true
		}
	}
	return false
}

// CanTerminal is the coarse "may open any terminal at all" pre-gate: any
// terminal-level entry (direct or team) or any org-adminship qualifies.
func (a *Access) CanTerminal() bool {
	if a == nil {
		return false
	}
	if a.user.CanTerminal() || len(a.adminOrgs) > 0 {
		return true
	}
	for _, t := range a.teams {
		for _, l := range t.tiles {
			if levelRank(l) >= levelRank(LevelTerminal) {
				return true
			}
		}
	}
	return false
}

// TermAPI / TermNet: the D17 terminal-plane grants, unioned across the user
// flag and every team the user is in.
func (a *Access) TermAPI() bool {
	if a == nil {
		return false
	}
	if a.user.IsAdmin() || a.user.TermAPI {
		return true
	}
	for _, t := range a.teams {
		if t.termAPI {
			return true
		}
	}
	return false
}
func (a *Access) TermNet() bool {
	if a == nil {
		return false
	}
	if a.user.IsAdmin() || a.user.TermNet {
		return true
	}
	for _, t := range a.teams {
		if t.termNet {
			return true
		}
	}
	return false
}

// IsAdminOrg reports delegated org-adminship (D21).
func (a *Access) IsAdminOrg(org string) bool { return a != nil && a.adminOrgs[org] }

// AdminOrgs lists the orgs this user administers, sorted.
func (a *Access) AdminOrgs() []string {
	if a == nil {
		return nil
	}
	out := make([]string, 0, len(a.adminOrgs))
	for o := range a.adminOrgs {
		out = append(out, o)
	}
	sort.Strings(out)
	return out
}

// --- Ceiling: the runtime-plane policy evaluation ---------------------------

// Ceiling is the composed policy ceiling over one tile: the workspace rows
// plus the tile's-org rows that cover it. It constrains ELEMENTS (what the
// tile may be granted) — human principals are never subject to it.
type Ceiling struct {
	rows []PolicyRow
}

// Ceiling collects the rows covering path (workspace + its org's).
func (s *Store) Ceiling(path string) Ceiling {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []PolicyRow
	for _, r := range s.policy {
		if matchTile(r.Tiles, path) {
			rows = append(rows, r)
		}
	}
	if org, ok := OrgOf(path); ok {
		if o := s.orgs[org]; o != nil {
			for _, r := range o.Policy {
				if matchTile(r.Tiles, path) {
					rows = append(rows, r)
				}
			}
		}
	}
	return Ceiling{rows: rows}
}

// DenyRow returns the first row denying kind (for error messages).
func (c Ceiling) DenyRow(kind string) (PolicyRow, bool) {
	for _, r := range c.rows {
		for _, d := range r.Deny {
			if d == kind {
				return r, true
			}
		}
	}
	return PolicyRow{}, false
}

// Denies reports whether any matching row strips the capability class.
func (c Ceiling) Denies(kind string) bool {
	_, ok := c.DenyRow(kind)
	return ok
}

// MayCallBlocker returns the first MayCall-bearing row whose allow-list does
// not cover target ("" ⇒ target allowed). Rows intersect: all must pass.
func (c Ceiling) MayCallBlocker(target string) (PolicyRow, bool) {
	for _, r := range c.rows {
		if len(r.MayCall) == 0 {
			continue
		}
		ok := false
		for _, pat := range r.MayCall {
			if matchTile(pat, target) {
				ok = true
				break
			}
		}
		if !ok {
			return r, true
		}
	}
	return PolicyRow{}, false
}

// MayCall reports whether every MayCall-bearing matching row covers target.
func (c Ceiling) MayCall(target string) bool {
	_, blocked := c.MayCallBlocker(target)
	return !blocked
}

// --- small helpers -----------------------------------------------------------

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}

func filter(list []string, keep func(string) bool) []string {
	out := make([]string, 0, len(list))
	for _, e := range list {
		if keep(e) {
			out = append(out, e)
		}
	}
	return out
}

// normUsersLocked normalizes, dedups, sorts and existence-checks a user-id
// list (caller holds s.mu).
func (s *Store) normUsersLocked(ids []string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = normalizeID(id)
		if id == "" || seen[id] {
			continue
		}
		if _, ok := s.byID[id]; !ok {
			return nil, fmt.Errorf("no such user %q", id)
		}
		seen[id] = true
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// --- per-tile ACL editing + membership views (the /access and whoami APIs) --

// SetUserTile sets — or with level "" removes — a user's EXACT tile entry.
// This is the per-tile ACL editor's write path: unlike the monotone
// GrantTile it can lower or clear (pattern entries are edited on the user
// object itself, not here).
func (s *Store) SetUserTile(id, path, level string) error {
	if level != "" && levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q (want read|write|terminal, or empty to remove)", level)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return fmt.Errorf("no such user %q", id)
	}
	nu := *u
	nu.Tiles = withEntry(u.Tiles, path, level)
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}

// SetTeamTile is SetUserTile for a team's exact entry.
func (s *Store) SetTeamTile(orgID, teamID, path, level string) error {
	if level != "" && levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q (want read|write|terminal, or empty to remove)", level)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	t := org.Team(normalizeID(teamID))
	if t == nil {
		return fmt.Errorf("no such team %q in org %q", teamID, orgID)
	}
	nt := *t
	nt.Tiles = withEntry(t.Tiles, path, level)
	no := *org
	no.Teams = replaceTeam(org.Teams, &nt)
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// withEntry returns a copy of m with path set to level ("" = removed).
func withEntry(m map[string]string, path, level string) map[string]string {
	out := make(map[string]string, len(m)+1)
	for k, v := range m {
		out[k] = v
	}
	if level == "" {
		delete(out, path)
	} else {
		out[path] = level
	}
	return out
}

// TeamInfo / OrgMembership are the whoami-facing membership views. CanCreate
// rides along in the SELF view only, so create-in-team pickers can pin the
// right path prefix.
type TeamInfo struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	CanCreate []string `json:"canCreate,omitempty"`
}
type OrgMembership struct {
	ID    string     `json:"id"`
	Name  string     `json:"name"`
	Admin bool       `json:"admin"`
	Teams []TeamInfo `json:"teams,omitempty"`
}

// UserOrgs lists the orgs a user belongs to (admins included), with the teams
// they're in — the self-service view whoami serves. Org ADMINS see all of
// their org's teams (they may act in any of them, e.g. create-in-team), with
// membership still deciding for plain members.
func (s *Store) UserOrgs(id string) []OrgMembership {
	id = normalizeID(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []OrgMembership
	for _, o := range s.orgs {
		if !o.effMember(id) {
			continue
		}
		admin := contains(o.Admins, id)
		m := OrgMembership{ID: o.ID, Name: o.Name, Admin: admin}
		for _, t := range o.Teams {
			if admin || contains(t.Members, id) {
				m.Teams = append(m.Teams, TeamInfo{ID: t.ID, Name: t.Name, CanCreate: t.CanCreate})
			}
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
