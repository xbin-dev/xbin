package delegation

// changeset.go — admin change sets: the store. A delegate never calls the
// admin API. The agent of an admin who opted in proposes a batch of admin
// operations from a closed list of kinds; xbind describes each one in its
// own words with a before/after it computed (a dry run of the real
// handler); the person approves all, some or none in their own session;
// xbind applies the approved items in order, as that person, through the
// handlers everyone uses (internal/server/changeset.go). This file keeps
// the records: <hex(user)>/changesets/<id>.json.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/util"
)

// Change-set states.
const (
	SetProposed  = "proposed"  // waiting for its person
	SetApplying  = "applying"  // approved items are being applied
	SetApplied   = "applied"   // every approved item applied
	SetPartial   = "partial"   // some approved items failed, went stale or were skipped
	SetRejected  = "rejected"  // the person rejected it whole
	SetExpired   = "expired"   // nobody decided in time
	SetWithdrawn = "withdrawn" // the agent, or the person, took it back
)

// Item results.
const (
	ItemApplied  = "applied"
	ItemFailed   = "failed"   // the handler refused it at apply
	ItemStale    = "stale"    // the state changed since the person saw its before/after
	ItemSkipped  = "skipped"  // an earlier item failed
	ItemRejected = "rejected" // the person left it out
	ItemBlocked  = "blocked"  // it could never be applied (the preview's refusal)
)

// Limits.
const (
	MaxItems    = 20
	MaxOpenSets = 10
	SetTTL      = 7 * 24 * time.Hour
	// A person's change sets are bounded on disk whatever their agent
	// does: at most MaxOpenSets waiting and MaxDecidedSets decided (the
	// oldest go first), each at most MaxSetBytes with MaxItemArgs of
	// arguments an operation, and a decided set is listed for KeepSets.
	// What was applied stays in the audit log for its 13 months.
	MaxDecidedSets = 50
	MaxItemArgs    = 4 << 10
	MaxSetBytes    = 256 << 10
	KeepSets       = 30 * 24 * time.Hour
	// applyingStale: a set still "applying" this long after it was claimed
	// was interrupted (applying takes seconds).
	applyingStale = 10 * time.Minute
)

// ChangeOp is one admin operation of a closed list of kinds.
type ChangeOp struct {
	Kind string          `json:"kind"`
	Args json.RawMessage `json:"args"`
}

// AdminCall is the admin API request that applies an operation: what xbind
// runs, as the approving person, through the handler everyone uses.
type AdminCall struct {
	Method string          `json:"method"`
	Path   string          `json:"path"` // below /api/xbin
	Body   json.RawMessage `json:"body,omitempty"`
}

// ChangePreview is xbind's description of one operation, computed without
// changing anything: the checks of the handler that would apply it, and
// the state it would change.
type ChangePreview struct {
	Line     string
	Before   json.RawMessage
	After    json.RawMessage
	Effects  []string
	Warnings []string
	Blocked  string    // why the handler would refuse it; "" when it would apply
	Inverse  *ChangeOp // what undoes it, when something does
	Call     AdminCall
}

// ChangeItem is one operation of a set, as xbind described it.
type ChangeItem struct {
	N int `json:"n"`
	ChangeOp
	// Why is the agent's own reason, shown as the agent's.
	Why string `json:"why,omitempty"`
	// Line is xbind's plain-language sentence for the operation, written
	// from its kind and arguments — never the agent's text.
	Line string `json:"line"`
	// Before and After are the state the operation changes, read by xbind
	// when the set was proposed; Effects what else applying it does (a
	// backend restarts, who gains or loses access); Warnings what the
	// handler would say.
	Before   json.RawMessage `json:"before,omitempty"`
	After    json.RawMessage `json:"after,omitempty"`
	Effects  []string        `json:"effects,omitempty"`
	Warnings []string        `json:"warnings,omitempty"`
	// Blocked: why this operation can't be applied at all (the handler's
	// refusal at the dry run). A blocked item is never applied.
	Blocked string `json:"blocked,omitempty"`
	// Basis is a hash of Before: at apply, an item whose state no longer
	// matches is stale and not applied.
	Basis string `json:"basis,omitempty"`
	// Inverse undoes the operation, when one exists.
	Inverse *ChangeOp `json:"inverse,omitempty"`
	Result  string    `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// ChangeSet is one proposal.
type ChangeSet struct {
	ID      string       `json:"id"`
	User    string       `json:"user"`
	Agent   string       `json:"agent,omitempty"` // "" for an undo set xbind made for the person
	Title   string       `json:"title"`
	UndoOf  string       `json:"undoOf,omitempty"`
	State   string       `json:"state"`
	Created time.Time    `json:"created"`
	Decided time.Time    `json:"decided,omitempty"`
	Expires time.Time    `json:"expires"`
	Items   []ChangeItem `json:"items"`
	// WithdrawnBy is "xbin" for a proposal xbind withdrew because its
	// person's agent stopped for good (WithdrawOpenSets).
	WithdrawnBy string `json:"withdrawnBy,omitempty"`
}

// Open reports a set still waiting for its person.
func (c ChangeSet) Open() bool { return c.State == SetProposed }

// Change-set refusals.
var (
	ErrNoChangeSet    = errors.New("no such change set")
	ErrSetNotOpen     = errors.New("the change set is no longer waiting for approval")
	ErrTooManySets    = fmt.Errorf("%d change sets are already waiting for this person", MaxOpenSets)
	ErrTooManyItems   = fmt.Errorf("a change set holds at most %d operations", MaxItems)
	ErrEmptyChangeSet = errors.New("a change set holds at least one operation")
	ErrSetTooLarge    = fmt.Errorf("the change set is too large: at most %d bytes of arguments an operation and %d KiB in all", MaxItemArgs, MaxSetBytes>>10)
)

func (s *Store) setsDir(user string) string { return filepath.Join(userDir(s.dir, user), "changesets") }

func idOK(id string) bool {
	return id != "" && len(id) <= 64 && !strings.ContainsAny(id, "/\\.") && !strings.ContainsRune(id, 0)
}

// cachedSet is a set's file as it was parsed, with the stat it was parsed
// at: a file is read again only when its size or time changed.
type cachedSet struct {
	cs    ChangeSet
	size  int64
	mtime time.Time
}

// lockSets returns user's state with its change-set lock held: the caller
// unlocks p.setsMu. It is a lock of its own — never the one State(),
// requests and approvals share, which the gate takes on every delegate
// call — so listing or applying sets can't hold a person's agent up.
func (s *Store) lockSets(user string) *person {
	s.mu.Lock()
	p := s.people[user]
	if p == nil {
		p = &person{}
		s.people[user] = p
	}
	s.mu.Unlock()
	p.setsMu.Lock()
	if p.sets == nil {
		p.sets = map[string]*cachedSet{}
	}
	return p
}

// writeSet writes cs and keeps what was written as its parsed form.
func (s *Store) writeSet(p *person, cs ChangeSet) error {
	path := filepath.Join(s.setsDir(cs.User), cs.ID+".json")
	if err := writeJSON(path, cs); err != nil {
		delete(p.sets, cs.ID+".json")
		return err
	}
	if fi, err := os.Stat(path); err == nil {
		p.sets[cs.ID+".json"] = &cachedSet{cs: cs, size: fi.Size(), mtime: fi.ModTime()}
	}
	return nil
}

func ended(cs ChangeSet) time.Time {
	if cs.Decided.IsZero() {
		return cs.Created
	}
	return cs.Decided
}

// setsLocked answers user's sets, oldest first — expiring, pruning and
// capping them. It lists the directory and stats each file; a file is
// parsed only when it is new or changed.
func (s *Store) setsLocked(p *person, user string) ([]ChangeSet, error) {
	dir := s.setsDir(user)
	ents, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		clear(p.sets)
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	now := s.now()
	seen := map[string]bool{}
	var out []ChangeSet
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		c := p.sets[name]
		if c == nil || c.size != fi.Size() || !c.mtime.Equal(fi.ModTime()) {
			var cs ChangeSet
			if fi.Size() > 2*MaxSetBytes { // nothing this store wrote
				continue
			}
			if ok, err := readJSON(filepath.Join(dir, name), &cs); !ok || err != nil || cs.User != user || cs.ID+".json" != name {
				continue // not a set of this person's: never shown, never applied
			}
			c = &cachedSet{cs: cs, size: fi.Size(), mtime: fi.ModTime()}
			p.sets[name] = c
		}
		seen[name] = true
		cs := c.cs
		switch {
		case cs.State == SetApplying && now.Sub(ended(cs)) > applyingStale:
			// xbind stopped while applying it: what ran is in the audit log
			// and in each item's result; the rest never ran
			cs.State = SetPartial
			cs.Items = slices.Clone(cs.Items)
			for i := range cs.Items {
				if cs.Items[i].Result == "" {
					cs.Items[i].Result, cs.Items[i].Error = ItemSkipped, "xbind stopped while this change set was being applied: your agent's log says what ran"
				}
			}
			_ = s.writeSet(p, cs)
		case cs.State == SetProposed && !now.Before(cs.Expires):
			cs.State, cs.Decided = SetExpired, now.UTC().Truncate(time.Second)
			_ = s.writeSet(p, cs)
		case cs.State != SetProposed && cs.State != SetApplying && now.Sub(ended(cs)) > KeepSets:
			_ = os.Remove(filepath.Join(dir, name))
			delete(p.sets, name)
			continue
		}
		out = append(out, cs)
	}
	for name := range p.sets {
		if !seen[name] {
			delete(p.sets, name)
		}
	}
	// the decided ones beyond the cap go, oldest first
	var decided []ChangeSet
	for _, cs := range out {
		if cs.State != SetProposed && cs.State != SetApplying {
			decided = append(decided, cs)
		}
	}
	if over := len(decided) - MaxDecidedSets; over > 0 {
		slices.SortFunc(decided, func(a, b ChangeSet) int { return ended(a).Compare(ended(b)) })
		gone := map[string]bool{}
		for _, cs := range decided[:over] {
			_ = os.Remove(filepath.Join(dir, cs.ID+".json"))
			delete(p.sets, cs.ID+".json")
			gone[cs.ID] = true
		}
		out = slices.DeleteFunc(out, func(cs ChangeSet) bool { return gone[cs.ID] })
	}
	slices.SortFunc(out, func(a, b ChangeSet) int {
		if c := a.Created.Compare(b.Created); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// NewChangeSet stores cs, proposed, for cs.User.
func (s *Store) NewChangeSet(cs ChangeSet) (ChangeSet, error) {
	if s == nil {
		return ChangeSet{}, ErrNoStore
	}
	switch {
	case len(cs.Items) == 0:
		return ChangeSet{}, ErrEmptyChangeSet
	case len(cs.Items) > MaxItems:
		return ChangeSet{}, ErrTooManyItems
	}
	for _, it := range cs.Items {
		if len(it.Args) > MaxItemArgs {
			return ChangeSet{}, ErrSetTooLarge
		}
	}
	if raw, err := json.Marshal(cs); err != nil || len(raw) > MaxSetBytes {
		return ChangeSet{}, ErrSetTooLarge
	}
	p := s.lockSets(cs.User)
	defer p.setsMu.Unlock()
	sets, err := s.setsLocked(p, cs.User)
	if err != nil {
		return ChangeSet{}, err
	}
	open := 0
	for _, e := range sets {
		if e.Open() {
			open++
		}
	}
	if open >= MaxOpenSets {
		return ChangeSet{}, ErrTooManySets
	}
	now := s.now().UTC().Truncate(time.Second)
	cs.ID, cs.State, cs.Created, cs.Decided, cs.Expires = util.RandomToken(12), SetProposed, now, time.Time{}, now.Add(SetTTL)
	for i := range cs.Items {
		cs.Items[i].N, cs.Items[i].Result, cs.Items[i].Error = i+1, "", ""
	}
	if len(cs.Title) > 200 {
		cs.Title = cs.Title[:200]
	}
	return cs, s.writeSet(p, cs)
}

// ChangeSets lists user's sets, oldest first.
func (s *Store) ChangeSets(user string) ([]ChangeSet, error) {
	if s == nil {
		return []ChangeSet{}, nil
	}
	p := s.lockSets(user)
	defer p.setsMu.Unlock()
	sets, err := s.setsLocked(p, user)
	if sets == nil {
		sets = []ChangeSet{}
	}
	return sets, err
}

// ChangeSet is user's set id.
func (s *Store) ChangeSet(user, id string) (ChangeSet, bool) {
	if s == nil || !idOK(id) {
		return ChangeSet{}, false
	}
	sets, _ := s.ChangeSets(user)
	for _, cs := range sets {
		if cs.ID == id {
			return cs, true
		}
	}
	return ChangeSet{}, false
}

// WithdrawOpenSets withdraws every proposed set of user (WithdrawnBy
// "xbin"), as WithdrawPending does their requests, and answers the ids.
func (s *Store) WithdrawOpenSets(user string) ([]string, error) {
	if s == nil {
		return nil, ErrNoStore
	}
	p := s.lockSets(user)
	defer p.setsMu.Unlock()
	sets, err := s.setsLocked(p, user)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC().Truncate(time.Second)
	var ids []string
	for _, cs := range sets {
		if !cs.Open() {
			continue
		}
		cs.State, cs.Decided, cs.WithdrawnBy = SetWithdrawn, now, WithdrawnByXbin
		if err := s.writeSet(p, cs); err != nil {
			return ids, err
		}
		ids = append(ids, cs.ID)
	}
	return ids, nil
}

// UpdateChangeSet applies change to user's set id and writes it. change's
// error leaves the set as it was.
func (s *Store) UpdateChangeSet(user, id string, change func(*ChangeSet) error) (ChangeSet, error) {
	if s == nil {
		return ChangeSet{}, ErrNoStore
	}
	if !idOK(id) {
		return ChangeSet{}, ErrNoChangeSet
	}
	p := s.lockSets(user)
	defer p.setsMu.Unlock()
	sets, err := s.setsLocked(p, user)
	if err != nil {
		return ChangeSet{}, err
	}
	i := slices.IndexFunc(sets, func(c ChangeSet) bool { return c.ID == id })
	if i < 0 {
		return ChangeSet{}, ErrNoChangeSet
	}
	cs := sets[i]
	if err := change(&cs); err != nil {
		return sets[i], err
	}
	if raw, err := json.Marshal(cs); err != nil || len(raw) > 2*MaxSetBytes { // results and errors added while applying: bounded too
		return sets[i], ErrSetTooLarge
	}
	return cs, s.writeSet(p, cs)
}
