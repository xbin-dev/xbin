package registry

// partition.go — partitioned tiles in the registry (plans/partitions/01 §1-§5;
// PD-02, PD-03, PD-05, PD-28, PD-44). A tile's primary code may ask, with
// the manifest's "partition", for one backend per person ("user") plus an
// optional background "global" instance. The request (Q) is code kind: it
// comes from the code the primary runs, a pinned primary's checkpoint
// included (composeManifest). What routing obeys is the RECORDED mode (R),
// xbind state the broker keeps (broker/partitionmode.go), which follows Q
// on its own only while the tile holds no data; otherwise a change waits for
// a tile manager (pending). A rescan asks the mode store through the
// PartitionModes hook and keeps the answer on each component.
//
// Zero state: a tile whose code asks for nothing and that has no recorded
// mode is Unpartitioned and carries nothing — every component of every
// workspace that never used partitions.

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// PartitionSpec is a partition mode: user partitions, plus the global
// instance. The zero value is unpartitioned.
type PartitionSpec struct {
	User   bool `json:"user"`
	Global bool `json:"global"`
}

// IsZero reports whether s is unpartitioned.
func (s PartitionSpec) IsZero() bool { return !s.User && !s.Global }

// String names s as the switch surfaces say it.
func (s PartitionSpec) String() string {
	switch {
	case s.User && s.Global:
		return "user + global"
	case s.User:
		return "user"
	case s.Global:
		return "global"
	}
	return "unpartitioned"
}

// SpecOf is *p, the zero spec (unpartitioned) for nil.
func SpecOf(p *PartitionSpec) PartitionSpec {
	if p == nil {
		return PartitionSpec{}
	}
	return *p
}

// PartitionState is a tile's settled partition state (01 §2.1).
type PartitionState uint8

const (
	// PartitionUnpartitioned: the recorded mode has no user partitions and
	// the tile runs as one instance (today's).
	PartitionUnpartitioned PartitionState = iota
	// PartitionPartitioned: the recorded mode has user partitions.
	PartitionPartitioned
	// PartitionPending: the code asks for another mode on a tile that holds
	// data; nothing runs until a tile manager switches or keeps (PD-50).
	PartitionPending
	// PartitionInvalid: the code's request is invalid; nothing runs, and
	// nothing is recorded or deleted.
	PartitionInvalid
)

// String is the state's wire name.
func (s PartitionState) String() string {
	switch s {
	case PartitionPartitioned:
		return "partitioned"
	case PartitionPending:
		return "pending"
	case PartitionInvalid:
		return "invalid"
	}
	return "unpartitioned"
}

// Held reports whether the state keeps the tile's primary from running.
func (s PartitionState) Held() bool { return s == PartitionPending || s == PartitionInvalid }

// PartitionRequest is an open or declined request R → Q.
type PartitionRequest struct {
	Spec     *PartitionSpec // Q; nil = unpartitioned (the code dropped the key)
	Declined bool           // a tile manager kept R for exactly Q
	Since    time.Time
}

// PartitionAsk is what a rescan asks the mode store about one tile.
type PartitionAsk struct {
	Tile, Scope string
	// RootsScope: the tile roots its scope, whose data namespaces are then
	// the tile's (01 §2.2).
	RootsScope bool
	// Requested is Q, nil when the primary's code doesn't ask (or the tile
	// is a template, whose request waits for its instances).
	Requested *PartitionSpec
	// Invalid says why Q is invalid; "" when it is valid or absent.
	Invalid string
	// Unread says why the primary's code manifest can't be read — an
	// xbin.json that doesn't parse, a pinned checkpoint that isn't or can't
	// be prepared: Q is unknown, not absent. The store holds a tile it has a
	// record of (Invalid, nothing written) rather than read "no request": a
	// typo or a store fault is never a switch request. "" when it was read.
	Unread string
}

// PartitionMode is the mode store's answer for one tile.
type PartitionMode struct {
	State    PartitionState
	Recorded PartitionSpec     // R
	Request  *PartitionRequest // Q when it differs from R (pending or declined); nil otherwise
	// Err says why the store holds the tile Invalid when its request isn't
	// the reason: its code can't be read (PartitionAsk.Unread), or its mode
	// record can't be.
	Err string
	// Unknown: the mode record can't be read, so R is unknown — Recorded is
	// the zero spec and means nothing (the state is Invalid).
	Unknown bool
}

// partitionInfo is the settled partition state a component carries.
type partitionInfo struct {
	mode   PartitionMode
	asked  *PartitionSpec // Q as asked (nil: absent or invalid)
	unread string         // why the code's manifest can't be read (Rescan, composePinned)
	detail string         // an invalid request's specifics, for managers (PartitionErrDetail)
}

// PartitionState answers c's settled state, its recorded mode R and the
// request R → Q when Q differs (pending, or declined). A deployment view
// carries none of it: ask the registry's component.
func (c *Component) PartitionState() (PartitionState, PartitionSpec, *PartitionRequest) {
	m := c.partition.mode
	return m.State, m.Recorded, m.Request
}

// Partitioned answers R when it has user partitions and the tile isn't
// pending or invalid.
func (c *Component) Partitioned() (PartitionSpec, bool) {
	if c.partition.mode.State != PartitionPartitioned {
		return PartitionSpec{}, false
	}
	return c.partition.mode.Recorded, true
}

// PartitionRecordUnknown reports whether c's recorded mode can't be read
// (its record is unreadable; c is Invalid): R is unknown, so every refusal
// that R's user partitions would give applies — fail closed.
func (c *Component) PartitionRecordUnknown() bool { return c.partition.mode.Unknown }

// PartitionRequested is Q, what the primary's code asks: nil when it
// doesn't, or asks for something invalid (PartitionErr says what).
func (c *Component) PartitionRequested() *PartitionSpec { return c.partition.asked }

// PartitionShown reports whether c's partition state is anything to show:
// the code asks (validly or not), the recorded mode is partitioned, or a
// request is open or declined. False for every zero-state tile.
func (c *Component) PartitionShown() bool {
	m := c.partition.mode
	return m.State != PartitionUnpartitioned || m.Request != nil || c.PartitionErr != "" || c.partition.asked != nil
}

// PartitionErrDetail is why c's request is invalid with the specifics
// PartitionErr leaves out — the sibling tile that asks differently, the
// grant it holds — for managers and admins (bx doctor, the Partitions
// section); PartitionErr otherwise.
func (c *Component) PartitionErrDetail() string {
	if c.partition.detail != "" {
		return "partition: " + c.partition.detail
	}
	return c.PartitionErr
}

// PartitionedScope answers the recorded mode R of scope's root tile when R
// has user partitions: the broker's namespace choice (01 §5). The data
// layout follows R, not the running state, so a root that is paused
// (pending or invalid) still answers its R — a caller that must refuse
// during a pause checks the root's PartitionState().Held() as well; a root
// whose record can't be read is Invalid with R unknown and answers false
// here. The workspace scope and a scope no tile roots are never
// partitioned.
func (r *Registry) PartitionedScope(scope string) (PartitionSpec, bool) {
	if scope == "" {
		return PartitionSpec{}, false
	}
	c, ok := r.Component(scope)
	if !ok {
		return PartitionSpec{}, false
	}
	if rec := c.partition.mode.Recorded; rec.User {
		return rec, true
	}
	return PartitionSpec{}, false
}

// PartitionAsked reports whether any component asks for a partition mode,
// validly or not — whether a rescan with the mode store installed has
// anything to settle that the scan without it didn't.
func (r *Registry) PartitionAsked() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, c := range r.components {
		if c.partition.asked != nil || c.PartitionErr != "" {
			return true
		}
	}
	return false
}

// PartitionList is the manifest's "partition": a list of words. Any other
// JSON value parses (the manifest's other keys still apply) and is invalid
// (ValidatePartition): an unknown request fails closed.
type PartitionList []string

// partitionNotList stands in for a "partition" value that is no list of
// strings; no word can be it.
const partitionNotList = "\x00not a list"

// UnmarshalJSON accepts a list of strings; null is absent; anything else
// is kept as invalid.
func (p *PartitionList) UnmarshalJSON(b []byte) error {
	if strings.TrimSpace(string(b)) == "null" {
		*p = nil
		return nil
	}
	var words []string
	if err := json.Unmarshal(b, &words); err != nil {
		*p = PartitionList{partitionNotList}
		return nil
	}
	if words == nil {
		words = []string{} // [] is present, and invalid
	}
	*p = words
	return nil
}

// partitionNoteMax bounds partitionNote, in characters (PD-57).
const partitionNoteMax = 280

// Partition words (PD-02).
const (
	PartitionWordUser   = "user"
	PartitionWordGlobal = "global"
)

// ParsePartition reads a "partition" value: nil for absent, the spec for
// ["user"] or ["user","global"] (any order), an error for anything else —
// [], ["global"] alone, duplicates, unknown words (a later "org" included)
// or a value that isn't a list of strings.
func ParsePartition(p PartitionList) (*PartitionSpec, error) {
	if p == nil {
		return nil, nil
	}
	if len(p) == 0 {
		return nil, errors.New(`[] asks for nothing: use ["user"] or ["user", "global"], or drop the key`)
	}
	var s PartitionSpec
	for _, w := range p {
		switch {
		case w == partitionNotList:
			return nil, errors.New(`must be a list: ["user"] or ["user", "global"]`)
		case w == PartitionWordUser && !s.User:
			s.User = true
		case w == PartitionWordGlobal && !s.Global:
			s.Global = true
		case w == PartitionWordUser || w == PartitionWordGlobal:
			return nil, fmt.Errorf("%q is listed twice", w)
		default:
			return nil, fmt.Errorf("unknown word %q (this xbind knows \"user\" and \"global\"; an unknown word runs no backend)", w)
		}
	}
	if !s.User {
		return nil, errors.New(`["global"] alone partitions nothing: use ["user", "global"]`)
	}
	return &s, nil
}

// ValidatePartition checks m's partition request and the keys that ride on
// it: the spec (nil when m asks for nothing), or why the request is
// invalid. partitionNote is judged beside a request, partitionMail beside a
// request with "global" only (the doorbell rings the global instance; 01
// §1: without it the key is ignored), so a manifest without "partition" is
// never an error here.
func ValidatePartition(m Manifest) (*PartitionSpec, error) {
	q, err := ParsePartition(m.Partition)
	if err != nil || q == nil {
		return nil, err
	}
	if mail := m.PartitionMail; mail != "" && q.Global {
		u, perr := url.Parse(mail)
		if perr != nil || !strings.HasPrefix(mail, "/") || strings.HasPrefix(mail, "//") ||
			u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(mail, "?#") {
			return nil, fmt.Errorf("partitionMail %q must be an absolute path without a query", mail)
		}
	}
	if n := utf8.RuneCountInString(m.PartitionNote); n > partitionNoteMax {
		return nil, fmt.Errorf("partitionNote is %d characters, over %d", n, partitionNoteMax)
	}
	return q, nil
}

// PartitionRefusedTarget reports whether a partitioned tile may never hold
// target (PD-28, 01 §3 rule 5): governance (xbin, xbin:*), cap:sandboxes
// (tile sandboxes are keyed per tile and deployment), cap:net-admin and
// cap:containers (they widen every partition's sandbox).
func PartitionRefusedTarget(target string) bool {
	switch {
	case target == "xbin", strings.HasPrefix(target, "xbin:"),
		target == "cap:sandboxes", target == "cap:net-admin", target == "cap:containers":
		return true
	}
	return false
}

// partitionRefusal is a structural refusal (01 §3) whose specifics name
// more than the tile's own manifest and scope: Error is what every reader
// of the tile sees (the components row, the 409); detail names the sibling
// tile or the grant, for managers only (PartitionErrDetail, the log).
type partitionRefusal struct{ msg, detail string }

func (e *partitionRefusal) Error() string { return e.msg }

// partitionStructure applies 01 §3's structural rules to c, which asks for
// q: nil, or why the request is invalid. asked is every component's valid
// request (absent ones missing), grants the workspace grant table.
func partitionStructure(c *Component, q PartitionSpec, asked map[string]*PartitionSpec,
	comps map[string]*Component, scopes map[string]*ScopeManifest, grants []Grant) error {
	resPrefix := "res:" + c.Scope + "/"
	usesScope := false
	for _, u := range c.Manifest.Uses {
		if PartitionRefusedTarget(u.Target) {
			return fmt.Errorf("a partitioned tile can't use %s", u.Target)
		}
		usesScope = usesScope || c.Scope != "" && strings.HasPrefix(u.Target, resPrefix)
	}
	for _, g := range grants {
		if g.From != c.Path {
			continue
		}
		if PartitionRefusedTarget(g.Target) {
			return &partitionRefusal{
				msg:    "the tile holds a grant a partitioned tile can't hold (xbin, xbin:*, cap:sandboxes, cap:net-admin or cap:containers): revoke it first",
				detail: fmt.Sprintf("a partitioned tile can't hold %s (revoke the grant first)", g.Target)}
		}
		usesScope = usesScope || c.Scope != "" && strings.HasPrefix(g.Target, resPrefix)
	}
	switch {
	case usesScope && c.Scope != c.Path:
		return fmt.Errorf("the tile uses %s's resources but doesn't root that scope: a partitioned tile keeps its data in a scope it roots", c.Scope)
	case c.Manifest.Chrome:
		return errors.New("a chrome tile acts as the signed-in person and can't be partitioned")
	case c.Manifest.VM.Enabled():
		return errors.New("a vm backend can't be partitioned in this release")
	}
	if c.Scope == "" {
		return nil
	}
	for _, rel := range slices.Sorted(maps.Keys(comps)) {
		o := comps[rel]
		if rel == c.Path || o.Scope != c.Scope || o.IsTemplate() {
			continue
		}
		if oq := asked[rel]; oq == nil || *oq != q {
			return &partitionRefusal{
				msg: fmt.Sprintf("another tile in the same scope %s asks for a different partition mode: every tile of a partitioned scope asks alike", c.Scope),
				detail: fmt.Sprintf("%s, in the same scope %s, asks for %s: every tile of a partitioned scope asks alike",
					rel, c.Scope, SpecOf(oq))}
		}
	}
	if sm := scopes[c.Scope]; sm != nil {
		for _, name := range slices.Sorted(maps.Keys(sm.Resources)) {
			res := sm.Resources[name]
			switch {
			case !res.Shared.Valid():
				return fmt.Errorf("%s/scope.json: resource %s: shared must be true or \"read\"", c.Scope, name)
			case res.Shared == SharedRead && res.Type == "sqlite":
				return fmt.Errorf("%s/scope.json: resource %s: a sqlite resource can't be shared read-only (\"read\"): its readers write its journal", c.Scope, name)
			}
		}
	}
	return nil
}

// settlePartitions judges every component's partition request — the value,
// then the structural rules against the scan's other components, scopes and
// grants — and settles each through the mode store (PartitionModes). An
// invalid request, or a tile the store holds invalid (its code or its
// record can't be read), is c's PartitionErr and part of its ManifestErr. A
// component whose code manifest can't be read asks nothing it can be held
// to: its request is unknown (PartitionAsk.Unread), never absent. Called by
// Rescan before it publishes the scan, with no registry lock held (the hook
// may read the registry) and under the scan's serialization.
func (r *Registry) settlePartitions(comps map[string]*Component, scopes map[string]*ScopeManifest, ws WorkspaceManifest) {
	asked := map[string]*PartitionSpec{}
	errs := map[string]error{}
	for rel, c := range comps {
		if c.IsTemplate() || c.partition.unread != "" {
			continue // a template's request waits for its instances (01 §3 rule 7)
		}
		q, err := ValidatePartition(c.Manifest)
		switch {
		case err != nil:
			errs[rel] = err
		case q != nil:
			asked[rel] = q
		}
	}
	invalid := map[string]string{}
	for _, rel := range slices.Sorted(maps.Keys(comps)) {
		c := comps[rel]
		ask := PartitionAsk{Tile: rel, Scope: c.Scope, RootsScope: c.Scope != "" && c.Scope == rel, Unread: c.partition.unread}
		err := errs[rel]
		if q := asked[rel]; q != nil && err == nil {
			err = partitionStructure(c, *q, asked, comps, scopes, ws.Grants)
		}
		detail := ""
		if err != nil {
			ask.Invalid = err.Error()
			if pr := (*partitionRefusal)(nil); errors.As(err, &pr) {
				detail = pr.detail
			}
		} else {
			ask.Requested = asked[rel]
		}
		mode := r.partitionMode(ask)
		why := ask.Invalid
		if why == "" && mode.State == PartitionInvalid {
			why = mode.Err
		}
		if why != "" {
			c.PartitionErr = "partition: " + why
			if c.ManifestErr != "" {
				c.ManifestErr += "; "
			}
			c.ManifestErr += c.PartitionErr
			invalid[rel] = cmp.Or(detail, why)
		}
		c.partition = partitionInfo{mode: mode, asked: ask.Requested, unread: ask.Unread, detail: detail}
	}
	r.partScan.logInvalid(invalid)
}

// partitionMode asks the mode store about one tile. Without one (a registry
// no broker wired) nothing is recorded, so a tile that asks for a mode waits
// (pending) and one that asks for nothing, or whose request can't be read,
// is unpartitioned: fail closed.
func (r *Registry) partitionMode(ask PartitionAsk) PartitionMode {
	if r.PartitionModes != nil {
		return r.PartitionModes(ask)
	}
	switch {
	case ask.Invalid != "":
		return PartitionMode{State: PartitionInvalid}
	case ask.Requested != nil:
		return PartitionMode{State: PartitionPending, Request: &PartitionRequest{Spec: ask.Requested}}
	}
	return PartitionMode{}
}

// partitionScan serializes Rescan: every scan settles partition modes and
// the store records what it settles (history, an auto R := Q), so a slower,
// older scan must never record — or publish — after a newer one. It also
// remembers the invalid requests it logged.
type partitionScan struct {
	mu     sync.Mutex
	logged map[string]string // tile → why its request is invalid, as last logged
}

// serialize takes the scan lock and answers its release.
func (s *partitionScan) serialize() func() {
	s.mu.Lock()
	return s.mu.Unlock
}

// logInvalid warns once per tile whose partition request became invalid,
// or whose reason changed — the operator learns why its backend stopped,
// a pre-existing custom "partition" key of another shape included (docs/
// compat.md rule 7) — with the specifics readers don't see. Called under
// the scan lock.
func (s *partitionScan) logInvalid(now map[string]string) {
	for _, tile := range slices.Sorted(maps.Keys(now)) {
		if why := now[tile]; s.logged[tile] != why {
			slog.Warn("partitions: the tile's partition request is invalid; its backend doesn't run", "tile", tile, "why", why)
		}
	}
	s.logged = now
}

// SharedMode is a scope.json resource's "shared" (PD-05, PD-45): in a
// partitioned scope, one namespace for every partition (today's keys)
// instead of one per partition. true: every partition and the global
// instance read and write; "read": the global instance writes, user
// partitions read. It is ignored in a scope no tile partitions. Any other
// value is kept and invalid for a partitioned scope (fail closed).
type SharedMode string

const (
	SharedNone SharedMode = ""
	SharedAll  SharedMode = "true"
	SharedRead SharedMode = "read"
)

// Valid reports whether s is a value xbind knows.
func (s SharedMode) Valid() bool { return s == SharedNone || s == SharedAll || s == SharedRead }

// UnmarshalJSON accepts true, false, null and "read"; any other value is
// kept verbatim, as an invalid mode.
func (s *SharedMode) UnmarshalJSON(b []byte) error {
	switch t := strings.TrimSpace(string(b)); t {
	case "true":
		*s = SharedAll
	case "false", "null":
		*s = SharedNone
	case `"read"`:
		*s = SharedRead
	default:
		*s = SharedMode("?" + t)
	}
	return nil
}

// MarshalJSON writes s back as it was declared (a machine-managed workspace
// xbin.json keeps its resources' values).
func (s SharedMode) MarshalJSON() ([]byte, error) {
	switch s {
	case SharedAll:
		return []byte("true"), nil
	case SharedNone:
		return []byte("false"), nil
	case SharedRead:
		return []byte(`"read"`), nil
	}
	if raw := strings.TrimPrefix(string(s), "?"); json.Valid([]byte(raw)) {
		return []byte(raw), nil
	}
	return json.Marshal(string(s))
}
