// personal.go — the personal plane (plans/DECISIONS.md D88): what a user may
// do with the tiles they own personally (owner "user:<id>"), and two switches
// an admin can hold on any account.
//
// Orgs have had this since D26/D28/D54: permission sets (allowance, ceiling
// rows, terminal flags) and network sets (default egress, terminal reach).
// A personal tile had none of it — no default egress, and nobody but a
// workspace admin could approve or wire anything on it. Here a user carries:
//
//   - NoPersonalTiles — may not own tiles personally (org-only, for them);
//   - NoTerminal — capped at `write` on every tile: no shells, no agent
//     sessions, no backend logs;
//   - Sets / NetSets — permission and network sets for the tiles they own.
//
// The workspace PersonalDefaults {sets, netSets} are a LIVE layer, unioned
// with every user's own (the D54 union rule: nothing here ever narrows).
// The user's personal plane is defaults ∪ own:
//
//   - permission sets: their Policy rows join the ceiling of the user's
//     tiles, their Allow entries are what the owner may approve there
//     themselves, and their termApi/termNet flags reach the user (as an org's
//     sets reach its members);
//   - network sets: the union of their rules is the PERSONAL NETWORK — the
//     default egress of the user's tiles (the `personal` net ref), a terminal
//     scope on them, and `net:<rule>` allowance entries. Unlike an org's, it
//     is NOT a ceiling: a live default must never strand wiring a workspace
//     admin already made on a personal tile (cap those with a `deny net`
//     policy row instead).
package users

import (
	"fmt"
	"sort"
	"strings"
)

// PersonalDefaults is the workspace-live personal plane every non-admin user
// gets on top of their own sets (ws-admin; persisted as personalDefaults).
type PersonalDefaults struct {
	Sets    []string `json:"sets,omitempty"`
	NetSets []string `json:"netSets,omitempty"`
}

// PersonalPatch updates a user's personal plane; nil fields are untouched.
type PersonalPatch struct {
	NoPersonalTiles *bool
	NoTerminal      *bool
	Sets            *[]string
	NetSets         *[]string
}

// SetUserPersonal applies a patch to one user (ws-admin / xbin:users at the
// API). Set names must exist; lists are deduped and sorted.
func (s *Store) SetUserPersonal(id string, p PersonalPatch) (*User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.byID[normalizeID(id)]
	if u == nil {
		return nil, fmt.Errorf("no such user %q", id)
	}
	nu := *u
	if p.NoPersonalTiles != nil {
		nu.NoPersonalTiles = *p.NoPersonalTiles
	}
	if p.NoTerminal != nil {
		nu.NoTerminal = *p.NoTerminal
	}
	if p.Sets != nil {
		sets, err := s.normSetNamesLocked(*p.Sets, false)
		if err != nil {
			return nil, err
		}
		nu.Sets = sets
	}
	if p.NetSets != nil {
		sets, err := s.normSetNamesLocked(*p.NetSets, true)
		if err != nil {
			return nil, err
		}
		nu.NetSets = sets
	}
	s.byID[nu.ID] = &nu
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := nu
	return &c, nil
}

// normSetNamesLocked dedupes, sorts and existence-checks permission (net
// false) or network (net true) set names. Empty in → nil out.
func (s *Store) normSetNamesLocked(names []string, net bool) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = normalizeID(n)
		if n == "" || seen[n] {
			continue
		}
		if net {
			if _, ok := s.netSets[n]; !ok {
				return nil, fmt.Errorf("no such network set %q", n)
			}
		} else if _, ok := s.sets[n]; !ok {
			return nil, fmt.Errorf("no such permission set %q", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

// PersonalDefaults returns a copy of the workspace personal defaults.
func (s *Store) PersonalDefaults() PersonalDefaults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return PersonalDefaults{
		Sets:    append([]string(nil), s.personal.Sets...),
		NetSets: append([]string(nil), s.personal.NetSets...),
	}
}

// SetPersonalDefaults validates and replaces the workspace personal defaults.
func (s *Store) SetPersonalDefaults(d PersonalDefaults) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sets, err := s.normSetNamesLocked(d.Sets, false)
	if err != nil {
		return err
	}
	nets, err := s.normSetNamesLocked(d.NetSets, true)
	if err != nil {
		return err
	}
	s.personal = PersonalDefaults{Sets: sets, NetSets: nets}
	return s.persistLocked()
}

// union merges string lists, deduped + sorted.
func union(lists ...[]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, l := range lists {
		for _, e := range l {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	sort.Strings(out)
	return out
}

// personalLocked resolves a user's personal plane: the permission sets and
// network sets that apply (defaults ∪ own). Admins get nothing here — they
// bypass every gate this feeds.
func (s *Store) personalLocked(u *User) (sets, netSets []string) {
	if u == nil || u.IsAdmin() {
		return nil, nil
	}
	return union(s.personal.Sets, u.Sets), union(s.personal.NetSets, u.NetSets)
}

// Personal is one user's resolved personal plane (whoami, the users list,
// the broker's gates).
type Personal struct {
	Sets     []string `json:"sets"`     // permission sets (defaults ∪ own)
	NetSets  []string `json:"netSets"`  // network sets (defaults ∪ own)
	NetRules []string `json:"netRules"` // the personal network: union of NetSets' rules
	Allow    []string `json:"allow"`    // what the owner may approve on their tiles
}

// Personal resolves a user's personal plane (zero value for unknown/admin).
func (s *Store) Personal(uid string) Personal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u := s.byID[normalizeID(uid)]
	sets, nets := s.personalLocked(u)
	rules := s.netRulesLocked(nets)
	return Personal{Sets: sets, NetSets: nets, NetRules: rules, Allow: s.allowUnionLocked(sets, nil, rules)}
}

// PersonalNet is the owner's personal network for a user-owned tile: the
// network sets that make it up and their rules ("host" among them = host
// networking). Empty = the owner has none (no default egress, today's
// behaviour).
func (s *Store) PersonalNet(uid string) (sets, rules []string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, nets := s.personalLocked(s.byID[normalizeID(uid)])
	return nets, s.netRulesLocked(nets)
}

// netRulesLocked is the union of the named network sets' rules.
func (s *Store) netRulesLocked(names []string) []string {
	var lists [][]string
	for _, n := range names {
		if ns := s.netSets[n]; ns != nil {
			lists = append(lists, ns.Rules)
		}
	}
	return union(lists...)
}

// allowUnionLocked is an allowance: the named permission sets' entries, the
// extras, and a `net:<rule>` entry per network rule (network reach is an
// implicit allowance, D54) — deduped + sorted. Shared by orgs and users.
func (s *Store) allowUnionLocked(sets, extras, netRules []string) []string {
	lists := [][]string{extras}
	for _, n := range sets {
		if ps := s.sets[n]; ps != nil {
			lists = append(lists, ps.Allow)
		}
	}
	nets := make([]string, 0, len(netRules))
	for _, r := range netRules {
		nets = append(nets, "net:"+r)
	}
	return union(append(lists, nets)...)
}

// allowCovers matches a normalized approval target (+ role, "" for the
// binding plane) against allowance entries. The xbin floor holds here:
// even a hand-edited entry can't delegate workspace management.
func allowCovers(entries []string, target, role string) bool {
	if target == "xbin" || strings.HasPrefix(target, "xbin:") {
		return false
	}
	for _, e := range entries {
		pe, err := parseAllowEntry(e)
		if err != nil {
			continue // hand-edited junk never matches
		}
		if allowMatch(pe, target, role) {
			return true
		}
	}
	return false
}

// PersonalAllowanceCovers reports whether a user's personal allowance covers
// a target — may the owner approve it on their own tile themselves.
func (s *Store) PersonalAllowanceCovers(uid, target, role string) bool {
	return allowCovers(s.Personal(uid).Allow, target, role)
}

// personalPolicyLocked is the ceiling rows the owner's personal permission
// sets contribute to their tiles.
func (s *Store) personalPolicyLocked(uid string) []PolicyRow {
	sets, _ := s.personalLocked(s.byID[uid])
	var rows []PolicyRow
	for _, n := range sets {
		if ps := s.sets[n]; ps != nil {
			rows = append(rows, ps.Policy...)
		}
	}
	return rows
}

// personalTermFlagsLocked ORs the user's personal sets' terminal flags into
// the ones their orgs' sets conferred.
func (s *Store) personalTermFlagsLocked(u *User, api, net bool) (bool, bool) {
	sets, _ := s.personalLocked(u)
	for _, n := range sets {
		if ps := s.sets[n]; ps != nil {
			api, net = api || ps.TermAPI, net || ps.TermNet
		}
	}
	return api, net
}

// setHolderLocked names who references a permission (net false) or network
// (net true) set outside the orgs — a user, the personal defaults or the
// new-account seed — so deletes refuse instead of silently changing them.
func (s *Store) setHolderLocked(name string, net bool) string {
	pick := func(sets, nets []string) []string {
		if net {
			return nets
		}
		return sets
	}
	if contains(pick(s.personal.Sets, s.personal.NetSets), name) {
		return "the personal defaults"
	}
	if contains(pick(s.newUsers.Sets, s.newUsers.NetSets), name) {
		return "the new-account defaults"
	}
	ids := make([]string, 0, len(s.byID))
	for id := range s.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if u := s.byID[id]; contains(pick(u.Sets, u.NetSets), name) {
			return "user " + id
		}
	}
	return ""
}

// SetUsers lists who outside the orgs holds a set: "user:<id>" per user and
// "personal-defaults" / "new-accounts" for the workspace layers (the API's
// attachedTo, the restart fan-out).
func (s *Store) SetUsers(name string, net bool) []string {
	name = normalizeID(name)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	has := func(sets, nets []string) bool {
		if net {
			return contains(nets, name)
		}
		return contains(sets, name)
	}
	if has(s.personal.Sets, s.personal.NetSets) {
		out = append(out, "personal-defaults")
	}
	if has(s.newUsers.Sets, s.newUsers.NetSets) {
		out = append(out, "new-accounts")
	}
	for id, u := range s.byID {
		if has(u.Sets, u.NetSets) {
			out = append(out, OwnerKindUser+":"+id)
		}
	}
	sort.Strings(out)
	return out
}

// --- the two switches ---------------------------------------------------------

// NoTerminal reports the account-wide terminal switch (admins never).
func (a *Access) NoTerminal() bool {
	return a != nil && !a.user.IsAdmin() && a.user.NoTerminal
}

// NoPersonalTiles reports the per-user personal-ownership switch (admins
// never). The workspace tileCreation policy is the broker's to combine.
func (a *Access) NoPersonalTiles() bool {
	return a != nil && !a.user.IsAdmin() && a.user.NoPersonalTiles
}

// capLevel applies NoTerminal: terminal becomes write, nothing else moves.
func capLevel(noTerminal bool, l string) string {
	if noTerminal && l == LevelTerminal {
		return LevelWrite
	}
	return l
}

// TileLevel is the effective level on one path: the D31 resolution
// (tileLevel), capped at write for a NoTerminal account.
func (a *Access) TileLevel(path string) string {
	return capLevel(a.NoTerminal(), a.tileLevel(path))
}

// Explain lists the contributions to the level on path (explain), each capped
// like TileLevel so the first entry still IS the effective level.
func (a *Access) Explain(path string) []Contribution {
	out := a.explain(path)
	if a.NoTerminal() {
		for i := range out {
			if out[i].Level == LevelTerminal {
				out[i].Level = LevelWrite
				out[i].Source += " (no-terminal)"
			}
		}
	}
	return out
}
