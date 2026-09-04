// defaults.go — what a NEW account starts with, and the workspace's
// tile-creation policy (plans/DECISIONS.md D52).
//
// Both are workspace-level provisioning policy, persisted in users.json next
// to defaultTiles (D27). defaultTiles is a live baseline evaluated on every
// access check; NewUserDefaults is a one-shot seed COPIED onto an account at
// creation — SSO JIT provisioning (sso.go), the users API and bx — so an
// admin doesn't hand-edit every SSO-provisioned row, and so "everyone from
// corp.com lands in org corp as a developer" is one setting. Rows stay
// independently editable afterwards.
package users

import (
	"fmt"
	"sort"
	"strings"
)

// NewUserDefaults seeds an account created after it was set. Anything the
// creating admin specifies is ADDED on top (a union): default tiles + the
// request's tiles (request wins per path), default create patterns + the
// request's, terminal-plane flags OR'd, default orgs joined unless the user
// is already a member. Role is never part of this — JIT accounts are always
// `user` and an admin promotes deliberately (D52).
type NewUserDefaults struct {
	Tiles     map[string]string `json:"tiles,omitempty"`     // path|pattern → read|write|terminal
	CanCreate []string          `json:"canCreate,omitempty"` // create patterns
	TermAPI   bool              `json:"termApi,omitempty"`
	TermNet   bool              `json:"termNet,omitempty"`
	Orgs      []OrgDefault      `json:"orgs,omitempty"` // memberships to join
}

// OrgDefault is one default membership. Org admin is deliberately NOT a
// knob here: an auto-provisioned account must never manage an org.
type OrgDefault struct {
	Org    string `json:"org"`
	Level  string `json:"level"` // read|write|terminal ("" = read)
	Create bool   `json:"create,omitempty"`
}

// Tile-creation policy (D52): who may create tiles outside an organisation.
//
//	any       (default) non-admins create user-owned tiles by default and
//	          org-owned ones where they hold Create
//	org-only  non-admins may ONLY create org-owned tiles: a personal owner
//	          is refused and an unspecified owner resolves to the one org
//	          where they hold Create (several → they must choose; none →
//	          refused). Workspace admins are unaffected — workspace-owned
//	          creation is already an admin-only act.
const (
	TileCreationAny     = "any"
	TileCreationOrgOnly = "org-only"
)

// NewUserDefaults returns a copy of the current seed (zero value = none).
func (s *Store) NewUserDefaults() NewUserDefaults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return copyDefaults(s.newUsers)
}

// SetNewUserDefaults validates and replaces the seed (ws-admin). Default
// orgs must exist; levels validate; patterns dedupe.
func (s *Store) SetNewUserDefaults(d NewUserDefaults) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	nd, err := s.normDefaultsLocked(d)
	if err != nil {
		return err
	}
	s.newUsers = nd
	return s.persistLocked()
}

func (s *Store) normDefaultsLocked(d NewUserDefaults) (NewUserDefaults, error) {
	out := NewUserDefaults{TermAPI: d.TermAPI, TermNet: d.TermNet}
	for pat, l := range d.Tiles {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			return out, fmt.Errorf("newUsers.tiles: empty pattern")
		}
		if levelRank(l) == 0 && l != LevelNone { // none = a seeded explicit exclusion (D31)
			return out, fmt.Errorf("newUsers.tiles[%q]: unknown level %q (want read|write|terminal|none)", pat, l)
		}
		if out.Tiles == nil {
			out.Tiles = map[string]string{}
		}
		out.Tiles[pat] = l
	}
	seen := map[string]bool{}
	for _, p := range d.CanCreate {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out.CanCreate = append(out.CanCreate, p)
	}
	seenOrg := map[string]bool{}
	for _, od := range d.Orgs {
		od.Org = normalizeID(od.Org)
		if od.Org == "" || seenOrg[od.Org] {
			continue
		}
		if _, ok := s.orgs[od.Org]; !ok {
			return out, fmt.Errorf("newUsers.orgs: no such org %q — create the org first", od.Org)
		}
		if od.Level == "" {
			od.Level = LevelRead
		}
		if levelRank(od.Level) == 0 {
			return out, fmt.Errorf("newUsers.orgs[%s]: unknown level %q (want read|write|terminal)", od.Org, od.Level)
		}
		seenOrg[od.Org] = true
		out.Orgs = append(out.Orgs, od)
	}
	sort.Slice(out.Orgs, func(i, j int) bool { return out.Orgs[i].Org < out.Orgs[j].Org })
	return out, nil
}

func copyDefaults(d NewUserDefaults) NewUserDefaults {
	out := NewUserDefaults{TermAPI: d.TermAPI, TermNet: d.TermNet}
	if len(d.Tiles) > 0 {
		out.Tiles = make(map[string]string, len(d.Tiles))
		for k, v := range d.Tiles {
			out.Tiles[k] = v
		}
	}
	out.CanCreate = append([]string(nil), d.CanCreate...)
	out.Orgs = append([]OrgDefault(nil), d.Orgs...)
	return out
}

// seedNewUserLocked applies the defaults onto a user row being created
// (union semantics — see NewUserDefaults). Org joins are separate
// (joinDefaultOrgsLocked) because the row must exist in byID first.
func (s *Store) seedNewUserLocked(u *User) {
	d := s.newUsers
	if len(d.Tiles) > 0 {
		if u.Tiles == nil {
			u.Tiles = map[string]string{}
		}
		for pat, l := range d.Tiles {
			if _, set := u.Tiles[pat]; !set {
				u.Tiles[pat] = l
			}
		}
	}
	for _, p := range d.CanCreate {
		if !contains(u.CanCreate, p) {
			u.CanCreate = append(u.CanCreate, p)
		}
	}
	u.TermAPI = u.TermAPI || d.TermAPI
	u.TermNet = u.TermNet || d.TermNet
}

// joinDefaultOrgsLocked adds the default memberships for a user that now
// exists in byID. A default org deleted since the setting was saved is
// skipped (creation must not fail on stale policy — the admin UI shows the
// dangling entry); an existing membership is left untouched.
func (s *Store) joinDefaultOrgsLocked(id string) {
	for _, od := range s.newUsers.Orgs {
		if _, ok := s.orgs[od.Org]; !ok {
			continue
		}
		_ = s.addOrgMemberLocked(od.Org, Member{ID: id, Level: od.Level, Create: od.Create})
	}
}

// AddOrgMember adds ONE membership (no-op when the user is already a member
// — existing knobs are kept; PATCH /orgs members edits them). UpsertOrg stays
// the whole-list editor; this is the single-row path the new-account seed
// and `bx org member add` need.
func (s *Store) AddOrgMember(orgID string, m Member) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.addOrgMemberLocked(orgID, m); err != nil {
		return err
	}
	return s.persistLocked()
}

func (s *Store) addOrgMemberLocked(orgID string, m Member) error {
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	m.ID = normalizeID(m.ID)
	if _, ok := s.byID[m.ID]; !ok {
		return fmt.Errorf("no such user %q", m.ID)
	}
	if m.Level == "" {
		m.Level = LevelRead
	}
	if levelRank(m.Level) == 0 {
		return fmt.Errorf("member %q: unknown level %q (want read|write|terminal)", m.ID, m.Level)
	}
	if _, already := org.Member(m.ID); already {
		return nil
	}
	no := *org
	no.Members = append(append([]Member(nil), org.Members...), m)
	sort.Slice(no.Members, func(i, j int) bool { return no.Members[i].ID < no.Members[j].ID })
	s.orgs[no.ID] = &no
	return nil
}

// TileCreation reports the workspace's tile-creation policy ("any" when unset).
func (s *Store) TileCreation() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.tileCreation == "" {
		return TileCreationAny
	}
	return s.tileCreation
}

// SetTileCreation sets the policy (ws-admin): any | org-only.
func (s *Store) SetTileCreation(v string) error {
	switch v {
	case "", TileCreationAny:
		v = ""
	case TileCreationOrgOnly:
	default:
		return fmt.Errorf("tileCreation must be any or org-only")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tileCreation = v
	return s.persistLocked()
}
