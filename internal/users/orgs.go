// Ownership, orgs & delegated approvals (plans/ownership.md, D24–D28).
//
// Every component may have an OWNER — a user or an org — recorded here (never
// in the workspace: a tile terminal must not be able to edit its own
// ownership). Ownership is what permissions hang off: a user-owner holds
// terminal on their tile and manages its ACL; an org-owned tile confers each
// member's org-wide level. Orgs are flat member lists (no teams); allowances
// (directly or via named permission sets) let workspace admins delegate
// grant/binding approval to org admins for org-owned tiles; policy-ceiling
// rows still cap what any tile may be granted (deny beats allow).
//
// This file is the whole semantic core: everything above it (auth.Principal,
// the broker gates, the HTTP API) only asks questions answered here.
package users

import (
	"fmt"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Policy deny kinds (D20): the capability classes a policy row may strip from
// the tiles it covers.
const (
	PolicyDenyNet      = "net"       // net interface bindings (internet/host/lan/provider)
	PolicyDenyGPU      = "gpu"       // gpu:* grants
	PolicyDenyXbinCaps = "xbin-caps" // the reserved xbin / xbin:* capability targets
	PolicyDenyIngress  = "ingress"   // exposed-endpoint bindings (public reachability, plans/ingress.md)
)

var policyDenyKinds = map[string]bool{
	PolicyDenyNet: true, PolicyDenyGPU: true, PolicyDenyXbinCaps: true, PolicyDenyIngress: true,
}

// PolicyRow is one pattern-keyed ceiling row (D20): for tiles covered by the
// Tiles pattern, Deny strips capability classes outright, and MayCall (when
// present) allow-lists grant targets (component paths / res:… ids). Rows
// compose restrictively across the workspace, permission-set and org level:
// any deny wins, and every MayCall-bearing matching row must be satisfied.
type PolicyRow struct {
	Tiles   string   `json:"tiles"`
	Deny    []string `json:"deny,omitempty"`
	MayCall []string `json:"mayCall,omitempty"`
}

// Member is one org membership (D25): Level is the org-wide access level on
// org-OWNED tiles; Create = may create new org-owned tiles; Admin = org
// management (members, org tiles' ACLs, transfers, exercising allowances).
// The UI presents presets — Admin (terminal+create+admin), Developer
// (terminal+create), Viewer (read) — over these three knobs.
//
// Suspended (D34) pauses the membership without losing it: a suspended
// member contributes NOTHING through this org — no level on org tiles, no
// org shares, no create, no adminship, no set-conferred term flags — but
// the row (with its knobs) stays for one-click reinstatement. Org admins
// set it (pausing a member of your org is org management); a full ACCOUNT
// disable is the ws-admin's User.Disabled.
type Member struct {
	ID        string `json:"id"`
	Level     string `json:"level"` // read|write|terminal
	Create    bool   `json:"create,omitempty"`
	Admin     bool   `json:"admin,omitempty"`
	Suspended bool   `json:"suspended,omitempty"`
}

// Org is one organization (D25): a flat member list, per-tile shares (tiles
// shared TO the org), attached permission sets + per-org allowance extras
// (D26/D28, ws-admin-managed), and the org's policy-ceiling rows applied to
// tiles the org OWNS.
type Org struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	Members []Member `json:"members,omitempty"`
	// Tiles maps path (or pattern) → level: tiles shared TO this org — every
	// member gets the entry's level (mirror of User.Tiles).
	Tiles map[string]string `json:"tiles,omitempty"`
	// Sets / Allow are the delegated-approval configuration (ws-admin only):
	// referenced permission sets plus per-org extra allowance entries.
	Sets    []string    `json:"sets,omitempty"`
	Allow   []string    `json:"allow,omitempty"`
	Policy  []PolicyRow `json:"policy,omitempty"` // ceiling rows for org-OWNED tiles
	Created int64       `json:"created"`
}

// Member returns the membership entry for a user, if any.
func (o *Org) Member(userID string) (Member, bool) {
	for _, m := range o.Members {
		if m.ID == userID {
			return m, true
		}
	}
	return Member{}, false
}

// PermissionSet is a named, reusable bundle of org permissions (D28),
// attached to orgs by reference: allowance entries, ceiling rows, and
// member terminal-plane flags. Workspace-admin managed.
type PermissionSet struct {
	Allow   []string    `json:"allow,omitempty"`
	Policy  []PolicyRow `json:"policy,omitempty"`
	TermAPI bool        `json:"termApi,omitempty"`
	TermNet bool        `json:"termNet,omitempty"`
	Created int64       `json:"created,omitempty"`
}

// --- ownership ---------------------------------------------------------------

// Owner refs are "user:<id>" or "org:<id>"; "" means workspace-owned.
const (
	OwnerKindUser = "user"
	OwnerKindOrg  = "org"
)

// ParseOwner splits an owner ref. "" is valid (workspace-owned).
func ParseOwner(ref string) (kind, id string, err error) {
	if ref == "" {
		return "", "", nil
	}
	kind, id, ok := strings.Cut(ref, ":")
	if !ok || id == "" || (kind != OwnerKindUser && kind != OwnerKindOrg) {
		return "", "", fmt.Errorf("owner must be user:<id> or org:<id>, got %q", ref)
	}
	return kind, normalizeID(id), nil
}

// Owner returns a component's owner ref ("" = workspace-owned).
func (s *Store) Owner(path string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.owners[path]
}

// OwnerOrg returns the org id when path is org-owned.
func (s *Store) OwnerOrg(path string) (string, bool) {
	k, id, _ := ParseOwner(s.Owner(path))
	if k == OwnerKindOrg {
		return id, true
	}
	return "", false
}

// SetOwner records (or with "" clears) a component's owner. The referenced
// user/org must exist. Transfer AUTHORIZATION is the broker's job — this is
// the storage primitive.
func (s *Store) SetOwner(path, ref string) error {
	kind, id, err := ParseOwner(ref)
	if err != nil {
		return err
	}
	path = strings.Trim(path, "/")
	if path == "" {
		return fmt.Errorf("component path required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch kind {
	case OwnerKindUser:
		if _, ok := s.byID[id]; !ok {
			return fmt.Errorf("no such user %q", id)
		}
	case OwnerKindOrg:
		if _, ok := s.orgs[id]; !ok {
			return fmt.Errorf("no such org %q", id)
		}
	}
	if s.owners == nil {
		s.owners = map[string]string{}
	}
	no := make(map[string]string, len(s.owners)+1)
	for k, v := range s.owners {
		no[k] = v
	}
	if ref == "" {
		delete(no, path)
	} else {
		no[path] = kind + ":" + id
	}
	s.owners = no
	return s.persistLocked()
}

// Owners returns a copy of the full ownership table (admin views, doctor).
func (s *Store) Owners() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.owners))
	for k, v := range s.owners {
		out[k] = v
	}
	return out
}

// OwnedBy lists paths owned by ref, sorted.
func (s *Store) OwnedBy(ref string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for p, o := range s.owners {
		if o == ref {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// ValidateNewTile gates creation: the owner ref must parse and exist. (The
// old positional o/u path reservations are gone — paths carry no org
// meaning, D24.)
func (s *Store) ValidateNewTile(ownerRef string) error {
	kind, id, err := ParseOwner(ownerRef)
	if err != nil {
		return err
	}
	if kind == "" {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if kind == OwnerKindUser {
		if _, ok := s.byID[id]; !ok {
			return fmt.Errorf("no such user %q", id)
		}
	} else if _, ok := s.orgs[id]; !ok {
		return fmt.Errorf("no such org %q — create the org first", id)
	}
	return nil
}

// reservedOrgIDs: "workspace" is the UI/API label for the non-org plane.
var reservedOrgIDs = map[string]bool{"workspace": true}

// --- org CRUD ----------------------------------------------------------------

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

// UpsertOrg creates or updates an org's name + members. The ws-admin-only
// fields (Sets/Allow/Policy) and shares survive an update — they have their
// own setters. Member ids must exist; levels validate; duplicates collapse
// (highest wins on conflict is NOT attempted — last entry wins, deduped).
func (s *Store) UpsertOrg(o Org) (*Org, error) {
	o.ID = normalizeID(o.ID)
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
	members, err := s.normMembersLocked(o.Members)
	if err != nil {
		return nil, err
	}
	o.Members = members
	if strings.TrimSpace(o.Name) == "" {
		o.Name = o.ID
	}
	if existing != nil {
		o.Created = existing.Created
		o.Tiles = existing.Tiles
		o.Sets = existing.Sets
		o.Allow = existing.Allow
		o.Policy = existing.Policy
	} else {
		o.Created = time.Now().Unix()
	}
	no := o
	s.orgs[no.ID] = &no
	if err := s.persistLocked(); err != nil {
		return nil, err
	}
	c := no
	return &c, nil
}

// DeleteOrg removes an org — refused while it still OWNS tiles (transfer
// first; explicit beats silently orphaning an org's property, D24).
func (s *Store) DeleteOrg(id string) error {
	id = normalizeID(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	ref := OwnerKindOrg + ":" + id
	var owned []string
	for p, o := range s.owners {
		if o == ref {
			owned = append(owned, p)
		}
	}
	if len(owned) > 0 {
		sort.Strings(owned)
		return fmt.Errorf("org %q still owns %d tile(s) (%s…) — transfer them first", id, len(owned), owned[0])
	}
	delete(s.orgs, id)
	return s.persistLocked()
}

// SetOrgSets attaches permission sets to an org by name (ws-admin, D28).
func (s *Store) SetOrgSets(orgID string, sets []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(sets))
	for _, n := range sets {
		n = normalizeID(n)
		if n == "" || seen[n] {
			continue
		}
		if _, ok := s.sets[n]; !ok {
			return fmt.Errorf("no such permission set %q", n)
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Strings(out)
	no := *org
	no.Sets = out
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// SetOrgAllow replaces an org's extra allowance entries (ws-admin, D26).
func (s *Store) SetOrgAllow(orgID string, allow []string) error {
	if err := ValidateAllow(allow); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	no := *org
	no.Allow = append([]string(nil), allow...)
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// SetOrgTile sets — or with level "" removes — a tile SHARE to an org (the
// per-tile ACL's org entries; mirror of SetUserTile).
func (s *Store) SetOrgTile(orgID, path, level string) error {
	if level != "" && levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q (want read|write|terminal, or empty to remove)", level)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return fmt.Errorf("no such org %q", orgID)
	}
	no := *org
	no.Tiles = withEntry(org.Tiles, path, level)
	s.orgs[no.ID] = &no
	return s.persistLocked()
}

// normMembersLocked validates + dedups a member list (caller holds s.mu).
func (s *Store) normMembersLocked(members []Member) ([]Member, error) {
	seen := map[string]bool{}
	out := make([]Member, 0, len(members))
	for _, m := range members {
		m.ID = normalizeID(m.ID)
		if m.ID == "" || seen[m.ID] {
			continue
		}
		if _, ok := s.byID[m.ID]; !ok {
			return nil, fmt.Errorf("no such user %q", m.ID)
		}
		if m.Level == "" {
			m.Level = LevelRead
		}
		if levelRank(m.Level) == 0 {
			return nil, fmt.Errorf("member %q: unknown level %q (want read|write|terminal)", m.ID, m.Level)
		}
		seen[m.ID] = true
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// --- permission sets (D28) ---------------------------------------------------

func (s *Store) PermissionSets() map[string]PermissionSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]PermissionSet, len(s.sets))
	for n, ps := range s.sets {
		out[n] = *ps
	}
	return out
}

// UpsertPermissionSet creates or replaces a named set (ws-admin only at the
// API). Allowance grammar + policy rows validate; the xbin floor applies.
func (s *Store) UpsertPermissionSet(name string, ps PermissionSet) error {
	name = normalizeID(name)
	if err := validID(name); err != nil {
		return fmt.Errorf("permission set name: %w", err)
	}
	if err := ValidateAllow(ps.Allow); err != nil {
		return err
	}
	if err := validatePolicy(ps.Policy); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sets == nil {
		s.sets = map[string]*PermissionSet{}
	}
	if old, ok := s.sets[name]; ok {
		ps.Created = old.Created
	} else {
		ps.Created = time.Now().Unix()
	}
	np := ps
	s.sets[name] = &np
	return s.persistLocked()
}

// DeletePermissionSet removes a set — refused while any org references it
// (detach first; explicit beats silently changing orgs' capabilities).
func (s *Store) DeletePermissionSet(name string) error {
	name = normalizeID(name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.orgs {
		if contains(o.Sets, name) {
			return fmt.Errorf("permission set %q is attached to org %q — detach it first", name, o.ID)
		}
	}
	delete(s.sets, name)
	return s.persistLocked()
}

// --- allowances (D26): grammar + evaluation ---------------------------------

// The allowance grammar (plans/ownership.md, D26/D32). Every entry parses to
// a class + class-specific pattern, so a typo can't silently over- OR
// under-delegate (write-time validation refuses entries that could never
// match a real approval target):
//
//	res:<glob>[@<role>]          resource grants, optionally capped at a role
//	gpu:<glob>                   gpu grants
//	cap:<glob>                   capability grants (cap:net-admin, cap:containers)
//	net:internet | net:host | net:lan:<glob> | net:provider:<tile-glob>
//	iface:<svc>[@<tile-glob>[#<inst-glob>]]
//	                             interface bindings — optionally pinned to a
//	                             provider tile and a provider INSTANCE, so
//	                             "the dev instance of the api, only" is
//	                             expressible (D32)
//	ingress:host:<glob> | ingress:zone:<glob> | ingress:listen:<port|lo-hi>
//	tile:<pattern>[@<role>]      grants targeting whole tiles, optionally
//	                             capped at a role (bare = any role — prefer
//	                             @reader for consume-only delegation)
type allowEntry struct {
	class    string // res|gpu|cap|net|iface|ingress:host|ingress:zone|ingress:listen|tile
	value    string // class-specific pattern
	role     string // res/tile @role cap ("" = any role)
	provider string // iface @provider tile-glob ("" = any provider)
	instance string // iface #instance glob ("" = any instance)
}

// allowRoleRank orders the conventional roles for @role caps (bus aliases
// normalize). Unknown roles rank 0 and only match exactly.
func allowRoleRank(r string) int {
	switch r {
	case "subscriber":
		r = "reader"
	case "publisher":
		r = "writer"
	}
	switch r {
	case "reader":
		return 1
	case "writer":
		return 2
	case "admin":
		return 3
	}
	return 0
}

// roleWithin reports whether a requested role fits under an entry's cap.
func roleWithin(requested, cap string) bool {
	if cap == "" {
		return true // uncapped entry: any role
	}
	if requested == "" {
		return false // capped entry never covers an unknown/absent role
	}
	rr, cr := allowRoleRank(requested), allowRoleRank(cap)
	if rr == 0 || cr == 0 {
		return requested == cap
	}
	return rr <= cr
}

// parseAllowEntry validates one entry against the grammar.
func parseAllowEntry(e string) (allowEntry, error) {
	e = strings.TrimSpace(e)
	if e == "" {
		return allowEntry{}, fmt.Errorf("empty entry")
	}
	if e == "xbin" || strings.HasPrefix(e, "xbin:") {
		return allowEntry{}, fmt.Errorf("%q is never delegable — xbin capability grants make their holder a workspace admin", e)
	}
	class, rest, ok := strings.Cut(e, ":")
	if !ok || rest == "" {
		return allowEntry{}, fmt.Errorf("%q — entries are res:/gpu:/cap:/net:/iface:/ingress:host:/ingress:zone:/ingress:listen:/tile: followed by a value", e)
	}
	out := allowEntry{class: class, value: rest}
	cutRole := func() {
		if v, r, has := strings.Cut(out.value, "@"); has {
			out.value, out.role = v, r
		}
	}
	switch class {
	case "res", "tile":
		cutRole()
		if out.value == "" {
			return allowEntry{}, fmt.Errorf("%q — missing pattern", e)
		}
		if out.role != "" && allowRoleRank(out.role) == 0 {
			// Manifest-declared custom roles are allowed, but must look like one.
			if strings.ContainsAny(out.role, ":/@#*") {
				return allowEntry{}, fmt.Errorf("%q — @%s is not a role", e, out.role)
			}
		}
	case "gpu":
		// any glob
	case "cap":
		if strings.HasPrefix(rest, "xbin") {
			return allowEntry{}, fmt.Errorf("%q — the xbin capability family is never delegable (and no cap: target spells it)", e)
		}
	case "net":
		switch {
		case rest == "internet" || rest == "host":
		case strings.HasPrefix(rest, "internet:") && len(rest) > 9: // D35: filtered-internet carve-outs
		case strings.HasPrefix(rest, "lan:") && len(rest) > 4:
		case strings.HasPrefix(rest, "provider:") && len(rest) > 9:
		default:
			return allowEntry{}, fmt.Errorf("%q — net entries are net:internet, net:internet:<host-glob|cidr>[:port], net:lan:<glob|cidr>, or net:provider:<tile-glob>", e)
		}
	case "iface":
		v := rest
		if svc, prov, has := strings.Cut(v, "@"); has {
			out.value = svc
			if p, inst, hasInst := strings.Cut(prov, "#"); hasInst {
				out.provider, out.instance = p, inst
			} else {
				out.provider = prov
			}
		}
		if out.value == "" || (strings.Contains(rest, "@") && out.provider == "") ||
			(strings.Contains(rest, "#") && out.instance == "") {
			return allowEntry{}, fmt.Errorf("%q — iface entries are iface:<service>[@<tile-glob>[#<instance-glob>]]", e)
		}
	case "ingress":
		kind, val, ok := strings.Cut(rest, ":")
		if !ok || val == "" || (kind != "host" && kind != "zone" && kind != "listen") {
			return allowEntry{}, fmt.Errorf("%q — ingress entries are ingress:host:<glob>, ingress:zone:<glob> or ingress:listen:<port|lo-hi>", e)
		}
		out.class, out.value = "ingress:"+kind, val
		if kind == "listen" {
			lo, hi, isRange := strings.Cut(val, "-")
			if !isRange {
				hi = lo
			}
			l, e1 := strconv.Atoi(strings.TrimSpace(lo))
			h, e2 := strconv.Atoi(strings.TrimSpace(hi))
			if e1 != nil || e2 != nil || l < 1 || h > 65535 || l > h {
				return allowEntry{}, fmt.Errorf("%q — listen wants a port or lo-hi range", val)
			}
		}
	default:
		return allowEntry{}, fmt.Errorf("%q — entries are res:/gpu:/cap:/net:/iface:/ingress:host:/ingress:zone:/ingress:listen:/tile: followed by a value", e)
	}
	return out, nil
}

// ValidateAllow checks allowance entries against the grammar and the xbin
// floor: the workspace-governance capability family (xbin, xbin:*) is never
// delegable — an element granted xbin@admin IS a workspace admin, so an org
// admin who could self-approve it would transitively be one too (D26).
func ValidateAllow(entries []string) error {
	for i, e := range entries {
		if _, err := parseAllowEntry(e); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
	}
	return nil
}

// ResolvedAllow is an org's effective allowance: the union of every attached
// permission set's entries plus the org's own extras, deduped + sorted.
func (s *Store) ResolvedAllow(orgID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	org := s.orgs[normalizeID(orgID)]
	if org == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(es []string) {
		for _, e := range es {
			if !seen[e] {
				seen[e] = true
				out = append(out, e)
			}
		}
	}
	for _, n := range org.Sets {
		if ps := s.sets[n]; ps != nil {
			add(ps.Allow)
		}
	}
	add(org.Allow)
	sort.Strings(out)
	return out
}

// AllowanceCovers reports whether an org's resolved allowance covers a
// normalized approval target at the requested role ("" for the roleless
// binding plane). The xbin floor is enforced here too (defense-in-depth:
// even a hand-edited entry can't delegate it).
func (s *Store) AllowanceCovers(orgID, target, role string) bool {
	if target == "xbin" || strings.HasPrefix(target, "xbin:") {
		return false
	}
	for _, e := range s.ResolvedAllow(orgID) {
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

// allowMatch matches one parsed allowance entry against a normalized target
// (+ requested role for the grant plane). Target forms per class:
//
//	tile entries      bare component paths (grant targets)
//	res/gpu/cap/net   the prefixed target strings (res:apps/x/db, cap:…)
//	iface entries     iface:<svc>@<provider>[#<instance>] (bindingTargets)
//	ingress entries   ingress:host:<h> / :zone:<z> / :listen:<port>
func allowMatch(e allowEntry, target, role string) bool {
	switch e.class {
	case "tile":
		if strings.Contains(target, ":") {
			return false // tile: entries cover bare component paths only
		}
		return matchTile(e.value, target) && roleWithin(role, e.role)
	case "res", "gpu", "cap":
		rest, ok := strings.CutPrefix(target, e.class+":")
		if !ok {
			return false
		}
		if e.class == "res" && !roleWithin(role, e.role) {
			return false
		}
		return globMatch(e.value, rest)
	case "net":
		rest, ok := strings.CutPrefix(target, "net:")
		return ok && netAllowMatch(e.value, rest)
	case "iface":
		rest, ok := strings.CutPrefix(target, "iface:")
		if !ok {
			return false
		}
		svc, prov, inst := rest, "", ""
		if s2, p, has := strings.Cut(rest, "@"); has {
			svc = s2
			if p2, i2, hasInst := strings.Cut(p, "#"); hasInst {
				prov, inst = p2, i2
			} else {
				prov = p
			}
		}
		if !globMatch(e.value, svc) {
			return false
		}
		if e.provider != "" && (prov == "" || !matchTile(e.provider, prov)) {
			return false
		}
		if e.instance != "" && (inst == "" || !globMatch(e.instance, inst)) {
			return false
		}
		return true
	case "ingress:host", "ingress:zone":
		rest, ok := strings.CutPrefix(target, e.class+":")
		return ok && globMatch(e.value, rest)
	case "ingress:listen":
		port, ok := strings.CutPrefix(target, "ingress:listen:")
		if !ok {
			return false
		}
		lo, hi, isRange := strings.Cut(e.value, "-")
		if !isRange {
			hi = lo
		}
		l, e1 := strconv.Atoi(strings.TrimSpace(lo))
		h, e2 := strconv.Atoi(strings.TrimSpace(hi))
		p, e3 := strconv.Atoi(strings.TrimSpace(port))
		return e1 == nil && e2 == nil && e3 == nil && p >= l && p <= h
	}
	return false
}

// netAllowMatch matches a net-class allowance value against a normalized
// net-class target value (both after "net:"). Beyond the plain glob, it
// understands the D35 carve-out semantics:
//
//   - `internet` (unfiltered) covers every filtered `internet:<spec>` target
//     — full egress subsumes any filter;
//   - `internet:<glob|cidr>[:port]` covers `internet:<spec>` by hostname
//     glob or CIDR CONTAINMENT (an org allowed 1.2.0.0/16 may approve
//     1.2.3.0/24 or 1.2.3.4 — carving a subnet out of a larger grant), with
//     an entry-pinned port matching only that port;
//   - `lan:<glob|cidr>` covers `lan:<cidr>` the same way (containment or
//     glob).
func netAllowMatch(entry, target string) bool {
	if es, ok := strings.CutPrefix(entry, "internet:"); ok {
		if ts, ok2 := strings.CutPrefix(target, "internet:"); ok2 {
			return netSpecCovers(es, ts)
		}
		return false
	}
	if entry == "internet" && (target == "internet" || strings.HasPrefix(target, "internet:")) {
		return true
	}
	if es, ok := strings.CutPrefix(entry, "lan:"); ok {
		if ts, ok2 := strings.CutPrefix(target, "lan:"); ok2 {
			return netSpecCovers(es, ts)
		}
		return false
	}
	return globMatch(entry, target)
}

// netSpecCovers: one allowance spec vs one concrete binding spec — CIDR
// containment when both are addresses, hostname glob otherwise; an entry
// port pins the target's port.
func netSpecCovers(entry, target string) bool {
	eHost, ePort := cutNetPort(entry)
	tHost, tPort := cutNetPort(target)
	if ePort != "" && ePort != tPort {
		return false
	}
	if epfx, ok := parsePrefixish(eHost); ok {
		tpfx, ok2 := parsePrefixish(tHost)
		return ok2 && epfx.Bits() <= tpfx.Bits() && epfx.Contains(tpfx.Addr())
	}
	eh, th := strings.ToLower(eHost), strings.ToLower(tHost)
	if globMatch(eh, th) {
		return true
	}
	// `*.x.y` also covers the apex `x.y` — unlike TLS wildcards, an
	// ALLOWANCE for a domain's subdomains obviously means the domain too
	// (the apex-mismatch footgun would push admins straight back to
	// unfiltered internet).
	if rest, ok := strings.CutPrefix(eh, "*."); ok && rest == th {
		return true
	}
	return false
}

// cutNetPort splits a trailing ":<digits>" port off a spec (IPv4/hostname
// forms; bracketed IPv6 keeps its brackets in the host half).
func cutNetPort(s string) (host, port string) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 {
		return s, ""
	}
	p := s[i+1:]
	if p == "" {
		return s, ""
	}
	for _, c := range p {
		if c < '0' || c > '9' {
			return s, ""
		}
	}
	return s[:i], p
}

// parsePrefixish parses a CIDR or bare address ("1.2.3.4" ⇒ /32).
func parsePrefixish(s string) (netip.Prefix, bool) {
	s = strings.Trim(s, "[]")
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		return p.Masked(), err == nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	return netip.PrefixFrom(a, a.BitLen()), true
}

// globMatch supports one '*' anywhere (exact match without it).
func globMatch(pat, s string) bool {
	i := strings.IndexByte(pat, '*')
	if i < 0 {
		return pat == s
	}
	pre, suf := pat[:i], pat[i+1:]
	return len(s) >= len(pre)+len(suf) && strings.HasPrefix(s, pre) && strings.HasSuffix(s, suf)
}

// --- policy rows -------------------------------------------------------------

func validatePolicy(rows []PolicyRow) error {
	for i, r := range rows {
		if strings.TrimSpace(r.Tiles) == "" {
			return fmt.Errorf("policy[%d]: tiles pattern required", i)
		}
		for _, d := range r.Deny {
			if !policyDenyKinds[d] {
				return fmt.Errorf("policy[%d]: unknown deny kind %q (want net|gpu|xbin-caps|ingress)", i, d)
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

// SetOrgPolicy replaces one org's ceiling rows (workspace-admin data).
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

// orgShare is the slice of one org a member sees: their membership plus the
// org's shared-tile entries.
type orgShare struct {
	id     string
	member Member
	tiles  map[string]string // shared TO the org (COW, read-only)
}

// Access is a per-request snapshot of everything one user may do: ownership-
// derived levels, org-wide member levels, org shares, their own entries, and
// the workspace defaults (D24/D25/D27). Reads shared COW maps — treat as
// read-only. Nil-safe: a nil *Access answers no to everything.
type Access struct {
	user         *User
	owners       map[string]string // path → owner ref (COW snapshot)
	orgs         []orgShare
	defaultTiles map[string]string
	setAPI       bool // termApi conferred by any attached set of any org (D28)
	setNet       bool
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
	a := &Access{user: &uc, owners: s.owners, defaultTiles: s.defaultTiles}
	for _, o := range s.orgs {
		m, ok := o.Member(uc.ID)
		if !ok || m.Suspended {
			continue // suspended membership confers nothing (D34)
		}
		a.orgs = append(a.orgs, orgShare{id: o.ID, member: m, tiles: o.Tiles})
		for _, n := range o.Sets {
			if ps := s.sets[n]; ps != nil {
				a.setAPI = a.setAPI || ps.TermAPI
				a.setNet = a.setNet || ps.TermNet
			}
		}
	}
	sort.Slice(a.orgs, func(i, j int) bool { return a.orgs[i].id < a.orgs[j].id })
	return a, true
}

// TileLevel is the effective access level on one path (D24/D25/D27/D31).
// Resolution, in order:
//
//  1. workspace admin / the tile's user-owner / an org admin of the owning
//     org ⇒ terminal;
//  2. an EXACT per-user entry is authoritative — it sets the level outright
//     (including `none` = explicit exclusion), overriding org level, shares,
//     patterns and defaults. Exact entries are the sanctioned per-tile
//     override/share device (written via /access, owner-gated);
//  3. otherwise levels union — but on an ORG-OWNED tile the user's access is
//     their standing in the org (member level + shares the owners sanctioned),
//     NOT the workspace plane: personal pattern entries and workspace
//     defaults do not reach org tiles (D31 — "your perms on an org tile are
//     your perms in the org").
func (a *Access) TileLevel(path string) string {
	if a == nil {
		return ""
	}
	if a.user.IsAdmin() {
		return LevelTerminal
	}
	owner := a.owners[path]
	if owner == OwnerKindUser+":"+a.user.ID {
		return LevelTerminal // your tile is yours (D24)
	}
	orgOwner, isOrgOwned := strings.CutPrefix(owner, OwnerKindOrg+":")
	if isOrgOwned {
		for _, os := range a.orgs {
			if os.id == orgOwner && os.member.Admin {
				return LevelTerminal // org admins run the org's tiles
			}
		}
	}
	if l, ok := a.user.Tiles[path]; ok { // exact entry: authoritative (D31)
		if l == LevelNone {
			return ""
		}
		return l
	}
	best := ""
	up := func(l string) {
		if levelRank(l) > levelRank(best) {
			best = l
		}
	}
	if isOrgOwned {
		for _, os := range a.orgs {
			if os.id == orgOwner {
				up(os.member.Level)
			}
		}
	}
	for _, os := range a.orgs { // org shares (owner-sanctioned per-tile/pattern)
		for pat, l := range os.tiles {
			if matchTile(pat, path) {
				up(l)
			}
		}
	}
	if !isOrgOwned { // the workspace plane stops at org boundaries (D31)
		for pat, l := range a.user.Tiles {
			if l != LevelNone && matchTile(pat, path) {
				up(l)
			}
		}
		for pat, l := range a.defaultTiles {
			if matchTile(pat, path) {
				up(l)
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

// CanCreateTile gates PERSONAL (user-owned) creation: the user's own
// CanCreate patterns (admins anywhere). Creating AS an org is CanCreateAs —
// the path no longer encodes the org (D24), so the create request names the
// owner instead.
func (a *Access) CanCreateTile(path string) bool {
	if a == nil {
		return false
	}
	if a.user.IsAdmin() {
		return true
	}
	for _, pat := range a.user.CanCreate {
		if matchTile(pat, path) {
			return true
		}
	}
	return false
}

// CanCreateAs reports whether this user may create tiles OWNED BY org (D25:
// the member's Create knob; org admins implicitly).
func (a *Access) CanCreateAs(org string) bool {
	if a == nil {
		return false
	}
	if a.user.IsAdmin() {
		return true
	}
	for _, os := range a.orgs {
		if os.id == org {
			return os.member.Create || os.member.Admin
		}
	}
	return false
}

// CanTerminal is the coarse "may open any terminal at all" pre-gate: any
// terminal-level source qualifies — own entries, an owned tile, org level,
// org-adminship, or a terminal-level share.
func (a *Access) CanTerminal() bool {
	if a == nil {
		return false
	}
	if a.user.CanTerminal() {
		return true
	}
	me := OwnerKindUser + ":" + a.user.ID
	for _, ref := range a.owners {
		if ref == me {
			return true
		}
	}
	for _, os := range a.orgs {
		if os.member.Admin || levelRank(os.member.Level) >= levelRank(LevelTerminal) {
			return true
		}
		for _, l := range os.tiles {
			if levelRank(l) >= levelRank(LevelTerminal) {
				return true
			}
		}
	}
	return false
}

// TermAPI / TermNet: the D17 terminal-plane grants — the user flag, or a
// flag conferred by a permission set attached to any of their orgs (D28).
func (a *Access) TermAPI() bool {
	if a == nil {
		return false
	}
	return a.user.IsAdmin() || a.user.TermAPI || a.setAPI
}
func (a *Access) TermNet() bool {
	if a == nil {
		return false
	}
	return a.user.IsAdmin() || a.user.TermNet || a.setNet
}

// IsAdminOrg reports org-adminship (the D25 member knob).
func (a *Access) IsAdminOrg(org string) bool {
	if a == nil {
		return false
	}
	for _, os := range a.orgs {
		if os.id == org {
			return os.member.Admin
		}
	}
	return false
}

// AdminOrgs lists the orgs this user administers, sorted.
func (a *Access) AdminOrgs() []string {
	if a == nil {
		return nil
	}
	var out []string
	for _, os := range a.orgs {
		if os.member.Admin {
			out = append(out, os.id)
		}
	}
	return out
}

// Owned lists the tiles this user owns directly, sorted.
func (a *Access) Owned() []string {
	if a == nil {
		return nil
	}
	me := OwnerKindUser + ":" + a.user.ID
	var out []string
	for p, ref := range a.owners {
		if ref == me {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// --- Ceiling: the runtime-plane policy evaluation ---------------------------

// Ceiling is the composed policy ceiling over one tile: the workspace rows
// plus — when the tile is org-owned — the org's rows and every attached
// permission set's rows that cover it (restrictive union, D20/D28). It
// constrains ELEMENTS (what the tile may be granted) — human principals are
// never subject to it.
type Ceiling struct {
	rows []PolicyRow
}

// Ceiling collects the rows covering path (workspace + owner org's + sets').
func (s *Store) Ceiling(path string) Ceiling {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var rows []PolicyRow
	add := func(rs []PolicyRow) {
		for _, r := range rs {
			if matchTile(r.Tiles, path) {
				rows = append(rows, r)
			}
		}
	}
	add(s.policy)
	if org, ok := strings.CutPrefix(s.owners[path], OwnerKindOrg+":"); ok {
		if o := s.orgs[org]; o != nil {
			for _, n := range o.Sets {
				if ps := s.sets[n]; ps != nil {
					add(ps.Policy)
				}
			}
			add(o.Policy)
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

// --- default tiles (D27) -----------------------------------------------------

// DefaultTiles returns the workspace-level pattern→level map every user gets.
func (s *Store) DefaultTiles() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]string, len(s.defaultTiles))
	for k, v := range s.defaultTiles {
		out[k] = v
	}
	return out
}

// SetDefaultTiles replaces the workspace defaults (ws-admin).
func (s *Store) SetDefaultTiles(m map[string]string) error {
	for pat, l := range m {
		if strings.TrimSpace(pat) == "" {
			return fmt.Errorf("defaultTiles: empty pattern")
		}
		if levelRank(l) == 0 {
			return fmt.Errorf("defaultTiles[%q]: unknown level %q (want read|write|terminal)", pat, l)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nm := make(map[string]string, len(m))
	for k, v := range m {
		nm[k] = v
	}
	s.defaultTiles = nm
	return s.persistLocked()
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

// --- per-tile ACL editing + membership views (the /access and whoami APIs) --

// SetUserTile sets — or with level "" removes — a user's EXACT tile entry.
// This is the per-tile ACL editor's write path: exact entries are
// AUTHORITATIVE for their tile (D31) — they can lower, raise, or with level
// `none` explicitly exclude (pattern entries are edited on the user object
// itself, not here).
func (s *Store) SetUserTile(id, path, level string) error {
	if level != "" && level != LevelNone && levelRank(level) == 0 {
		return fmt.Errorf("unknown level %q (want read|write|terminal, none to exclude, or empty to remove)", level)
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

// OrgMembership is the whoami-facing self-service view of one membership.
type OrgMembership struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Level  string `json:"level"`
	Create bool   `json:"create,omitempty"`
	Admin  bool   `json:"admin,omitempty"`
	// Suspended: the membership is paused (D34) — shown so the member knows
	// why the org's tiles are gone (rather than silently vanishing).
	Suspended bool `json:"suspended,omitempty"`
}

// UserOrgs lists the orgs a user belongs to with their role — whoami's
// `orgs` field, and what owner pickers build from. Suspended memberships
// are included, flagged (they confer nothing; Access skips them).
func (s *Store) UserOrgs(id string) []OrgMembership {
	id = normalizeID(id)
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []OrgMembership
	for _, o := range s.orgs {
		if m, ok := o.Member(id); ok {
			out = append(out, OrgMembership{ID: o.ID, Name: o.Name, Level: m.Level,
				Create: m.Create, Admin: m.Admin, Suspended: m.Suspended})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// --- Explain: the "why does X have Y on Z" resolver ---------------------------

// Contribution is one source feeding a user's effective level on a path —
// the raw material for access-map views. Source forms:
//
//	admin                workspace admin (terminal everywhere)
//	owner                the user owns the tile (D24)
//	org-admin:<org>      org admin of the owning org
//	exact                an exact per-user entry — authoritative (D31);
//	                     level `none` = explicit exclusion
//	org-member:<org>     the member's org-wide level on an org-owned tile
//	org-share:<org>:<pattern>  a tile shared to an org they belong to
//	direct:<pattern>     the user's own pattern entry (non-org tiles only)
//	default:<pattern>    the workspace defaultTiles entry (D27; non-org only)
type Contribution struct {
	Level  string `json:"level"`
	Source string `json:"source"`
}

// Explain lists the contributions that actually apply to the user's level on
// path under the D31 resolution, highest first. The effective level is the
// first entry's — with `none` meaning no access (TileLevel agrees by
// construction; TestExplainMatchesTileLevel pins that).
func (a *Access) Explain(path string) []Contribution {
	if a == nil {
		return nil
	}
	if a.user.IsAdmin() {
		return []Contribution{{Level: LevelTerminal, Source: "admin"}}
	}
	owner := a.owners[path]
	if owner == OwnerKindUser+":"+a.user.ID {
		return []Contribution{{Level: LevelTerminal, Source: "owner"}}
	}
	orgOwner, isOrgOwned := strings.CutPrefix(owner, OwnerKindOrg+":")
	if isOrgOwned {
		for _, os := range a.orgs {
			if os.id == orgOwner && os.member.Admin {
				return []Contribution{{Level: LevelTerminal, Source: "org-admin:" + orgOwner}}
			}
		}
	}
	if l, ok := a.user.Tiles[path]; ok { // authoritative — nothing else applies
		return []Contribution{{Level: l, Source: "exact"}}
	}
	var out []Contribution
	if isOrgOwned {
		for _, os := range a.orgs {
			if os.id == orgOwner {
				out = append(out, Contribution{Level: os.member.Level, Source: "org-member:" + orgOwner})
			}
		}
	}
	for _, os := range a.orgs {
		for pat, l := range os.tiles {
			if matchTile(pat, path) {
				out = append(out, Contribution{Level: l, Source: "org-share:" + os.id + ":" + pat})
			}
		}
	}
	if !isOrgOwned {
		for pat, l := range a.user.Tiles {
			if l != LevelNone && matchTile(pat, path) {
				out = append(out, Contribution{Level: l, Source: "direct:" + pat})
			}
		}
		for pat, l := range a.defaultTiles {
			if matchTile(pat, path) {
				out = append(out, Contribution{Level: l, Source: "default:" + pat})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if levelRank(out[i].Level) != levelRank(out[j].Level) {
			return levelRank(out[i].Level) > levelRank(out[j].Level)
		}
		return out[i].Source < out[j].Source
	})
	return out
}
