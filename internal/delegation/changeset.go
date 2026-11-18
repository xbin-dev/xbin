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
	"context"
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
	ItemAsked    = "asked"    // it needs an admin, and its person asked one (ChangeSet.From on the admins' copies)
	// A plan's line that waits (ChangeItem.Wait): approved, and applied by
	// xbind itself once what it waits for is there — or never.
	ItemWaiting   = "waiting"   // approved; xbind applies it when its tile provides what was approved
	ItemLapsed    = "lapsed"    // it waited MaxWait and nothing came: never applied
	ItemCancelled = "cancelled" // it was waiting, and its person, their agent's end or a change to its tiles ended that: never applied
)

// MaxWait is how long a waiting line waits after its plan is applied.
const MaxWait = 24 * time.Hour

// WaitRole is the role a waiting binding is approved for: what a tool's
// caller needs of the tile that provides it, and nothing wider.
const WaitRole = "reader"

// PlanWait is what a waiting binding waits for, and is pinned to: Tile
// declaring a provide of Service, of Kind, granting Role — to be bound to
// Component's Slot. Anything else the tile declares is not what was
// approved.
type PlanWait struct {
	Component string `json:"component"`
	Slot      string `json:"slot"`
	Tile      string `json:"tile"`
	Kind      string `json:"kind"`
	Service   string `json:"service"`
	Role      string `json:"role"`
}

// ItemWait is a waiting line's state: PlanWait as approved; from the
// moment its plan is applied, Until (when it lapses), the two tiles'
// Owners and the slot's binding then (Was) — a change to either ends the
// wait —; and Held, xbind's words for why it is not applied though the
// tile now provides something (it declared something else than approved).
type ItemWait struct {
	PlanWait
	Until  time.Time `json:"until,omitempty"`
	Owners []string  `json:"owners,omitempty"`
	Was    string    `json:"was,omitempty"`
	Held   string    `json:"held,omitempty"`
	// Ids: the two tiles' own identities when the line was approved
	// (component, tile — as Owners): a tile deleted and made again at the
	// same path is another tile, and the line is not for it. "" where none
	// could be read.
	Ids []string `json:"ids,omitempty"`
}

type bindWaitKey struct{}

// WithBindWait marks ctx as the bind of a plan's line that waited: the
// binding is made for exactly w — its handler checks the provider once
// more against it, and records w.Role as the most the binding grants.
func WithBindWait(ctx context.Context, w PlanWait) context.Context {
	return context.WithValue(ctx, bindWaitKey{}, w)
}

// BindWait is the waiting line ctx's bind is made for (false: an ordinary bind).
func BindWait(ctx context.Context) (PlanWait, bool) {
	w, ok := ctx.Value(bindWaitKey{}).(PlanWait)
	return w, ok
}

// PlannedStart is an earlier agent.start line of the same set: a tile
// agent it starts in Tile may write what a later line waits for.
type PlannedStart struct {
	N    int
	Tile string
}

// WaitState is where a waiting line stands (PlanPreviewer.PreviewWait).
type WaitState string

const (
	WaitPending WaitState = "pending" // the tile provides nothing of the kind yet
	WaitReady   WaitState = "ready"   // it provides exactly what was approved
	WaitHeld    WaitState = "held"    // it provides something else: not bound, and said
	WaitGone    WaitState = "gone"    // a tile of the line is no more
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
	// MaxStartArgs bounds an agent.start line's arguments: they carry the
	// brief, which a single start request may carry too (64 KiB).
	MaxStartArgs = 68 << 10
	// MaxAsked bounds the requests one person has waiting with the admins
	// ("Ask an admin"), and MaxAskedOfOne the ones an admin has waiting
	// from other people.
	MaxAsked      = 3
	MaxAskedOfOne = 30
	MaxSetBytes   = 256 << 10
	KeepSets      = 30 * 24 * time.Hour
	// applyingStale: a set still "applying" this long after it was claimed
	// was interrupted (applying takes seconds).
	applyingStale = 10 * time.Minute
)

// The kinds a plan adds to the admin kinds (the broker's plan.go describes
// a creation; the server, which knows sessions, a start).
const (
	KindTileCreate = "tile.create"
	KindAgentStart = "agent.start"
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
	Line   string
	Before json.RawMessage
	After  json.RawMessage
	// BeforeText and AfterText are Before and After in words — xbind's own,
	// what a review draws ("not bound", "apps/dice").
	BeforeText, AfterText string
	Effects               []string
	Warnings              []string
	Blocked               string    // why the handler would refuse it; "" when it would apply
	Inverse               *ChangeOp // what undoes it, when something does
	Call                  AdminCall
	// Forbidden: the person themselves may not make this change (the
	// handler's 403): the preview then says the line and the refusal, and
	// nothing of the state behind it.
	Forbidden bool
	// Admin: applying it takes an admin's rights — it is not something the
	// person could do with their own hands and no admin (a plan's line).
	Admin bool
	// Tiles are the existing tiles the operation is ON: the ones whose
	// "agent" level and orgs' agent rules a plan's line is held to.
	Tiles []string
	// Creates is the tile a tile.create line would make: later lines of the
	// same set may name it.
	Creates *PlannedTile
	// Uses are the lines (their numbers) whose planned tiles this one names;
	// Deferred says it was described from those templates' manifests and is
	// checked for real once they exist.
	Uses     []int
	Deferred bool
	// Start is what an agent.start line starts, as a single start's sheet
	// shows it (the server's sessionPreview).
	Start json.RawMessage
	// UndoByHand: what undoing it takes when xbind can't (a created tile is
	// its person's to delete).
	UndoByHand string
	// Pin is what else the line was described from, hashed: a template's
	// manifest (a creation), the coding agent, mode, network and API access
	// a start resolves to. It moving between the proposal and the approval
	// makes the line stale, as its before and after moving does.
	Pin string
	// Later: the line would have to wait for a tile to provide what it
	// binds, and may not — it takes an admin's rights, and only a person's
	// own binding waits. xbind's words for what to do instead (propose it
	// again once the tile provides it); Blocked says the same.
	Later string
	// Power: Blocked is the power rule's refusal (the broker's
	// accessreach.go) — the tile, or code tied to it, holds power over the
	// workspace or the host: an admin makes this change by hand. The agent
	// reads one fixed sentence of it, never the holders.
	Power bool
	// Account: the account an access.set line resolved to — set only for a
	// person who sees accounts (D246). The server holds it to the person
	// whose agent proposed the line: their own access is never a line of
	// theirs, whoever describes it.
	Account string
	// Waits: the line can't be applied yet — the tile it binds to provides
	// nothing of the kind until a tile agent an earlier line starts has
	// written it. Approved, it waits (ItemWaiting) and xbind applies it by
	// itself once the tile provides exactly this.
	Waits *PlanWait
}

// PlannedTile is a tile an earlier tile.create line of the same set makes
// when it is applied: what xbind knows of it beforehand — its path, whose
// it will be, and its template's manifest (slots, provides).
type PlannedTile struct {
	N           int // the line that creates it
	Path        string
	Template    string
	Owner       string
	Manifest    json.RawMessage
	Partitioned bool
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
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`
	// BeforeText and AfterText say Before and After in words, written by
	// xbind: what a review shows (Before and After stay the data).
	BeforeText string   `json:"beforeText,omitempty"`
	AfterText  string   `json:"afterText,omitempty"`
	Effects    []string `json:"effects,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
	// Blocked: why this operation can't be applied at all (the handler's
	// refusal at the dry run). A blocked item is never applied.
	Blocked string `json:"blocked,omitempty"`
	// Power: Blocked is the power rule's refusal (ChangePreview.Power).
	Power bool `json:"power,omitempty"`
	// Basis is a hash of Before: at apply, an item whose state no longer
	// matches is stale and not applied.
	Basis string `json:"basis,omitempty"`
	// Inverse undoes the operation, when one exists.
	Inverse *ChangeOp `json:"inverse,omitempty"`
	Result  string    `json:"result,omitempty"`
	Error   string    `json:"error,omitempty"`

	// The fields below are a plan's (ChangeSet.Plan).

	// Admin: applying this line takes an admin's rights. A line without it
	// is something the person could do themselves, with no admin.
	Admin bool `json:"admin,omitempty"`
	// NeedsAdmin: an Admin line of a person who is no admin. They can't
	// apply it; they can ask an admin (…/ask). xbind says nothing more of
	// it than its line — its before and after are an admin's to read.
	NeedsAdmin bool `json:"needsAdmin,omitempty"`
	// Needs are the earlier lines this one depends on: tile.create lines
	// whose tile it names. It is applied only with them.
	Needs []int `json:"needs,omitempty"`
	// Deferred: described from the template of a tile an earlier line
	// creates; checked against the tile itself when it is applied.
	Deferred bool `json:"deferred,omitempty"`
	// Start is what an agent.start line starts: {tile, provider,
	// providerName, mode, asks, prompt, net, api, …}, as a start request's
	// preview.session.
	Start json.RawMessage `json:"start,omitempty"`
	// SigninUse: applying an agent.start line, its person ticked "Use my …
	// sign-in for this session" (the line's start offered it: signInId) —
	// the session carries the sign-in only then. SigninTrust: they ticked
	// its trust line too (start.signInTrust).
	SigninUse   bool `json:"signinUse,omitempty"`
	SigninTrust bool `json:"signinTrust,omitempty"`
	// Out is what applying it made: {path} for tile.create, {session} for
	// agent.start.
	Out json.RawMessage `json:"out,omitempty"`
	// UndoByHand: xbind's words for undoing it by hand, when it can't.
	UndoByHand string `json:"undoByHand,omitempty"`
	// Of: in a set an admin was asked to apply, the line's number in the
	// asker's set.
	Of int `json:"of,omitempty"`
	// Pin: ChangePreview.Pin as the line was described — what a deferred
	// line, which has no Basis to hold it, is held to beside its sentence.
	Pin string `json:"pin,omitempty"`
	// Wait: a line that waits for a tile to provide what it binds
	// (ChangePreview.Waits); with Result "waiting", where the wait stands.
	Wait *ItemWait `json:"wait,omitempty"`
	// Later: xbind's words for a line that may not wait (ChangePreview.Later):
	// propose it again once its tile provides what it binds. The agent
	// reads it — it says nothing the line's own arguments don't.
	Later string `json:"later,omitempty"`
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

	// Plan: proposed as a plan (POST /delegate/plans) — its lines may create
	// tiles and start tile agents, and are personal or an admin's
	// (ChangeItem.Admin). Session is the agent session (the conversation)
	// that proposed it: an agent.start line starts under it, held to its
	// taint.
	Plan    bool   `json:"plan,omitempty"`
	Session string `json:"session,omitempty"`
	// From: this set is a request to an admin (User) from that person, who
	// pressed "Ask an admin" on the lines of their own set AskOf that need
	// one. Every admin has a copy; AskID is the same on all of them, and
	// the first to decide ends the others (DecidedBy).
	From      string `json:"from,omitempty"`
	AskOf     string `json:"askOf,omitempty"`
	AskID     string `json:"askId,omitempty"`
	DecidedBy string `json:"decidedBy,omitempty"`
	// ByHand: for an undo set, what xbind can't undo, in its words (a
	// created tile is its person's to delete).
	ByHand []string `json:"byHand,omitempty"`
}

// lapsed turns cs's waiting lines whose time is up to ItemLapsed (cs.Items
// is copied before it is written), and reports whether any did.
func lapsed(cs *ChangeSet, now time.Time) bool {
	changed := false
	for i, it := range cs.Items {
		if it.Result != ItemWaiting || it.Wait == nil || it.Wait.Until.IsZero() || now.Before(it.Wait.Until) {
			continue
		}
		if !changed {
			cs.Items, changed = slices.Clone(cs.Items), true
		}
		cs.Items[i].Result = ItemLapsed
		cs.Items[i].Error = fmt.Sprintf("%s did not provide %s within %d hours of the plan being applied: nothing was bound", it.Wait.Tile, it.Wait.Service, int(MaxWait/time.Hour))
	}
	return changed
}

// Waiting reports whether a line of the set still waits.
func (c ChangeSet) Waiting() bool {
	return slices.ContainsFunc(c.Items, func(it ChangeItem) bool { return it.Result == ItemWaiting })
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
		if lapsed(&cs, now) { // a waiting line whose time is up: never applied
			_ = s.writeSet(p, cs)
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
		if max := map[bool]int{false: MaxItemArgs, true: MaxStartArgs}[it.Kind == KindAgentStart]; len(it.Args) > max {
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
	open, asked := 0, 0
	for _, e := range sets {
		switch {
		case !e.Open():
		case e.From != "":
			asked++ // other people's requests never crowd out the person's own agent
		default:
			open++
		}
	}
	if cs.From == "" && open >= MaxOpenSets || cs.From != "" && asked >= MaxAskedOfOne {
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
		if !cs.Open() || cs.From != "" { // a request from someone else isn't this person's agent's
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
