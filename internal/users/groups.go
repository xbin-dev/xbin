// groups.go — IdP-group → membership sync and the single-membership store
// primitives (plans/DECISIONS.md D53).
//
// An org carries GroupRules (Org.SSOGroups); at every SSO sign-in the
// provider's groups for the user are reconciled against them: memberships
// the rules want are created or re-set with provenance Via "sso", synced
// memberships whose rule or group is gone are REMOVED, and manual rows are
// never touched (manual wins when both exist). Workspace admin can come
// from SSOConfig.AdminGroups the same way, guarded so a hand-promoted admin
// is never demoted by the IdP and the last enabled admin never loses the
// role. A provider that fails to return groups changes nothing — unknown is
// not empty — and the failure is recorded on the user for the admin UI.
package users

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Bounds keeping users.json small however many groups an IdP emits.
const (
	maxSSOGroupsPerUser = 500
	maxKnownSSOGroups   = 2000
	maxSSOSyncErrorLen  = 200
)

// ErrNotMember is RemoveOrgMember's "nothing to remove" (a 404 at the API).
var ErrNotMember = errors.New("not a member")

// MemberPatch is a partial single-membership edit: nil fields keep their
// value; Detach clears provenance (synced → manual).
type MemberPatch struct {
	Level     *string
	Create    *bool
	Admin     *bool
	Suspended *bool
	Detach    bool
}

// SetOrgMember upserts ONE membership (org admins and ws-admins; the API's
// PUT /orgs/{org}/members/{user}). Absent → a new manual row (level read
// unless given); present → the given fields overlay. Provenance is never
// set here — only cleared via Detach.
func (s *Store) SetOrgMember(orgID, userID string, p MemberPatch) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return Member{}, fmt.Errorf("no such org %q", orgID)
	}
	userID = normalizeID(userID)
	if _, ok := s.byID[userID]; !ok {
		return Member{}, fmt.Errorf("no such user %q", userID)
	}
	m, has := org.Member(userID)
	if !has {
		m = Member{ID: userID, Level: LevelRead}
	}
	if p.Level != nil {
		if levelRank(*p.Level) == 0 {
			return Member{}, fmt.Errorf("unknown level %q (want read|write|terminal)", *p.Level)
		}
		m.Level = *p.Level
	}
	if p.Create != nil {
		m.Create = *p.Create
	}
	if p.Admin != nil {
		m.Admin = *p.Admin
	}
	if p.Suspended != nil {
		m.Suspended = *p.Suspended
	}
	if p.Detach {
		m.Via, m.ViaGroups = "", nil
	}
	no := *org
	no.Members = replaceMember(org.Members, m)
	s.orgs[no.ID] = &no
	return m, s.persistLocked()
}

// RemoveOrgMember drops ONE membership (ErrNotMember when absent). A synced
// row comes back at the user's next sign-in while its rule stands — the API
// says so; the durable fix is the rule or the group.
func (s *Store) RemoveOrgMember(orgID, userID string) (Member, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return Member{}, fmt.Errorf("no such org %q", orgID)
	}
	userID = normalizeID(userID)
	m, has := org.Member(userID)
	if !has {
		return Member{}, ErrNotMember
	}
	no := *org
	no.Members = withoutMember(org.Members, userID)
	s.orgs[no.ID] = &no
	return m, s.persistLocked()
}

// SetOrgSSOGroups replaces an org's group rules (ws-admin). Groups are
// trimmed, non-empty and unique (case-folded); levels validate ("" → read).
func (s *Store) SetOrgSSOGroups(orgID string, rules []GroupRule) error {
	seen := map[string]bool{}
	out := make([]GroupRule, 0, len(rules))
	for _, r := range rules {
		r.Group = strings.TrimSpace(r.Group)
		if r.Group == "" {
			return fmt.Errorf("group rule: empty group")
		}
		key := strings.ToLower(r.Group)
		if seen[key] {
			return fmt.Errorf("group rule: %q listed twice", r.Group)
		}
		if r.Level == "" {
			r.Level = LevelRead
		}
		if levelRank(r.Level) == 0 {
			return fmt.Errorf("group rule %q: unknown level %q (want read|write|terminal)", r.Group, r.Level)
		}
		seen[key] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	no := *org
	no.SSOGroups = out
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// SSOGroupRulesExist reports whether any rule (org or workspace-admin) is
// configured — the sign-in flow asks the provider for groups (and requests
// the scopes that needs) only then.
func (s *Store) SSOGroupRulesExist() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.sso != nil && len(s.sso.AdminGroups) > 0 {
		return true
	}
	for _, o := range s.orgs {
		if len(o.SSOGroups) > 0 {
			return true
		}
	}
	return false
}

// KnownSSOGroups is the union of groups seen at users' last SSO sign-ins
// (case-folded, sorted) — the admin UI's datalist for writing rules.
func (s *Store) KnownSSOGroups() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	seen := map[string]string{}
	for _, u := range s.byID {
		for _, g := range u.SSOGroups {
			if _, ok := seen[strings.ToLower(g)]; !ok {
				seen[strings.ToLower(g)] = g
			}
		}
	}
	out := make([]string, 0, len(seen))
	for _, g := range seen {
		out = append(out, g)
	}
	sort.Strings(out)
	if len(out) > maxKnownSSOGroups {
		out = out[:maxKnownSSOGroups]
	}
	return out
}

// SyncChange is one membership the reconcile created or re-set.
type SyncChange struct {
	Org    string
	Level  string
	Create bool
	Admin  bool
	Groups []string // the rule groups that matched
}

// SyncReport is what one SSO sign-in's reconcile did — audited line by line
// by the login handler.
type SyncReport struct {
	User          string
	Groups        []string // normalized provider groups
	Added         []SyncChange
	Updated       []SyncChange
	Removed       []string // org ids
	SkippedManual []string // a rule matched but a manual row exists (manual wins)
	AdminGranted  bool
	AdminRevoked  bool
	AdminBlocked  string // "" | "last-admin": revoke wanted but refused
	Changed       bool   // something was persisted
}

// SyncSSOGroups reconciles one user's memberships and admin role against
// the groups the provider returned at sign-in (the algorithm in the file
// doc). recordGroups=false means the provider was not asked (no rules
// active) — memberships still reconcile against "no groups" (which only
// removes stale synced rows) but User.SSOGroups keeps its previous value.
// One critical section, one persist, and only when something changed: a
// routine login must not rewrite users.json.
func (s *Store) SyncSSOGroups(userID string, groups []string, recordGroups bool) (SyncReport, error) {
	groups = normGroupKeys(groups)
	if len(groups) > maxSSOGroupsPerUser {
		groups = groups[:maxSSOGroupsPerUser]
	}
	have := map[string]bool{}
	for _, g := range groups {
		have[strings.ToLower(g)] = true
	}
	matches := func(key string) bool { return have[strings.ToLower(strings.TrimSpace(key))] }

	s.mu.Lock()
	defer s.mu.Unlock()
	id := normalizeID(userID)
	u := s.byID[id]
	if u == nil {
		return SyncReport{}, fmt.Errorf("no such user %q", userID)
	}
	rep := SyncReport{User: id, Groups: groups}
	nu := *u

	// Desired memberships: the union of every matching rule per org.
	orgIDs := make([]string, 0, len(s.orgs))
	for oid := range s.orgs {
		orgIDs = append(orgIDs, oid)
	}
	sort.Strings(orgIDs)
	changedOrgs := map[string]*Org{}
	for _, oid := range orgIDs {
		o := s.orgs[oid]
		var want *SyncChange
		for _, r := range o.SSOGroups {
			if !matches(r.Group) {
				continue
			}
			if want == nil {
				want = &SyncChange{Org: oid, Level: LevelRead}
			}
			if levelRank(r.Level) > levelRank(want.Level) {
				want.Level = r.Level
			}
			want.Create = want.Create || r.Create
			want.Admin = want.Admin || r.Admin
			want.Groups = append(want.Groups, r.Group)
		}
		m, has := o.Member(id)
		switch {
		case has && m.Via != MemberViaSSO:
			if want != nil {
				rep.SkippedManual = append(rep.SkippedManual, oid)
			}
		case has && want == nil:
			no := *o
			no.Members = withoutMember(o.Members, id)
			changedOrgs[oid] = &no
			rep.Removed = append(rep.Removed, oid)
		case has:
			if m.Level != want.Level || m.Create != want.Create || m.Admin != want.Admin || !equalStrings(m.ViaGroups, want.Groups) {
				m.Level, m.Create, m.Admin, m.ViaGroups = want.Level, want.Create, want.Admin, want.Groups
				no := *o
				no.Members = replaceMember(o.Members, m) // Suspended survives a re-sync
				changedOrgs[oid] = &no
				rep.Updated = append(rep.Updated, *want)
			}
		case want != nil:
			no := *o
			no.Members = replaceMember(o.Members, Member{
				ID: id, Level: want.Level, Create: want.Create, Admin: want.Admin,
				Via: MemberViaSSO, ViaGroups: want.Groups,
			})
			changedOrgs[oid] = &no
			rep.Added = append(rep.Added, *want)
		}
	}

	// Workspace admin by rule, guarded.
	adminWanted := false
	if s.sso != nil {
		for _, g := range s.sso.AdminGroups {
			if matches(g) {
				adminWanted = true
				break
			}
		}
	}
	switch {
	case adminWanted && nu.Role != RoleAdmin:
		nu.Role, nu.RoleVia = RoleAdmin, MemberViaSSO
		rep.AdminGranted = true
	case !adminWanted && nu.Role == RoleAdmin && nu.RoleVia == MemberViaSSO:
		if s.otherEnabledAdminsLocked(id) == 0 {
			rep.AdminBlocked = "last-admin" // keep role + provenance; retried next sign-in
		} else {
			nu.Role, nu.RoleVia = RoleUser, ""
			rep.AdminRevoked = true
		}
	}
	if nu.Role != RoleAdmin {
		nu.RoleVia = ""
	}
	if recordGroups && !equalFoldStrings(groups, u.SSOGroups) { // a case-only difference is not a change worth a rewrite
		nu.SSOGroups = groups
	}
	nu.SSOSyncError = ""

	rep.Changed = len(rep.Added)+len(rep.Updated)+len(rep.Removed) > 0 ||
		rep.AdminGranted || rep.AdminRevoked ||
		nu.RoleVia != u.RoleVia || nu.SSOSyncError != u.SSOSyncError ||
		!equalStrings(nu.SSOGroups, u.SSOGroups)
	if !rep.Changed {
		return rep, nil
	}
	for oid, no := range changedOrgs {
		s.orgs[oid] = no
	}
	s.byID[id] = &nu
	return rep, s.persistLocked()
}

// RecordSSOSyncError notes a group-fetch failure at sign-in (truncated)
// without touching any membership.
func (s *Store) RecordSSOSyncError(userID, msg string) error {
	if len(msg) > maxSSOSyncErrorLen {
		msg = msg[:maxSSOSyncErrorLen]
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(userID)]
	if u == nil {
		return fmt.Errorf("no such user %q", userID)
	}
	if u.SSOSyncError == msg {
		return nil
	}
	nu := *u
	nu.SSOSyncError = msg
	s.byID[nu.ID] = &nu
	return s.persistLocked()
}

// otherEnabledAdminsLocked counts enabled admins other than id (the
// last-admin guard; caller holds s.mu).
func (s *Store) otherEnabledAdminsLocked(id string) int {
	n := 0
	for _, u := range s.byID {
		if u.ID != id && u.Role == RoleAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

// normGroupKeys trims, drops empties, dedupes case-insensitively (first
// spelling wins) and sorts a group list.
func normGroupKeys(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, g := range in {
		g = strings.TrimSpace(g)
		if g == "" || seen[strings.ToLower(g)] {
			continue
		}
		seen[strings.ToLower(g)] = true
		out = append(out, g)
	}
	sort.Strings(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

func replaceMember(members []Member, m Member) []Member {
	out := make([]Member, 0, len(members)+1)
	placed := false
	for _, x := range members {
		if x.ID == m.ID {
			out = append(out, m)
			placed = true
		} else {
			out = append(out, x)
		}
	}
	if !placed {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func withoutMember(members []Member, id string) []Member {
	out := make([]Member, 0, len(members))
	for _, x := range members {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalFoldStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}
