package broker

// partitionmode.go — a tile's partition mode (plans/partitions/01 §2; PD-44,
// PD-49, PD-50, PD-51). The manifest's "partition" is a request (Q) the
// running code makes; the mode routing obeys is the RECORDED one (R), kept
// here, in data/partitions/<TileKey>/mode.json — xbind's own state, which no
// sandbox sees:
//
//	{"schema": 1, "tile": "apps/agent",
//	 "mode":     {"user": true, "global": true},       // R; absent = unpartitioned
//	 "request":  {"spec": {…} | null, "since": "…"},   // an open request R → Q
//	 "declined": {"spec": …, "by": "alice", "at": "…"}, // "keep the current mode" for exactly Q
//	 "history":  [{"op": "auto|request|switch|keep|withdrawn", "from", "to", "by", "at", "wiped"}]}
//
// The state table (01 §2.1), settled at every rescan through the registry's
// PartitionModes hook:
//
//	Q = R                                 → R's state (unpartitioned or partitioned)
//	Q ≠ R, the tile holds no data         → auto: R := Q at once, history "auto"
//	Q ≠ R, holds data                     → pending: nothing runs (PD-50)
//	Q ≠ R, holds data, declined names Q   → declined: R runs
//	Q invalid                             → invalid: nothing runs, nothing recorded
//
// Code that returns to R clears the request and the decline (history
// "withdrawn"); a different request Q′ opens a new one. Nothing here deletes
// data: switching and keeping are a tile manager's acts (F13a's routes),
// which record through recordDecision. No mode.json is ever written while R
// and Q are both absent, so a workspace that never uses partitions has no
// data/partitions directory (the zero state).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	partitionsDir     = "partitions" // under data/
	modeFile          = "mode.json"
	modeSchema        = 1
	modeHistoryMax    = 200 // the oldest entries go first
	modeOpAuto        = "auto"
	modeOpRequest     = "request"
	modeOpSwitch      = "switch"
	modeOpKeep        = "keep"
	modeOpWithdrawn   = "withdrawn"
	partitionHeldNote = "a manager of the tile must switch (deleting all its data) or keep the current mode"
)

// modeRecord is one tile's mode.json.
type modeRecord struct {
	Schema   int                     `json:"schema"`
	Tile     string                  `json:"tile"`
	Mode     *registry.PartitionSpec `json:"mode,omitempty"`
	Request  *modeRequest            `json:"request,omitempty"`
	Declined *modeDeclined           `json:"declined,omitempty"`
	History  []modeHistory           `json:"history,omitempty"`
}

type modeRequest struct {
	Spec  *registry.PartitionSpec `json:"spec"`
	Since time.Time               `json:"since"`
}

type modeDeclined struct {
	Spec *registry.PartitionSpec `json:"spec"`
	By   string                  `json:"by,omitempty"`
	At   time.Time               `json:"at"`
}

type modeHistory struct {
	Op    string                  `json:"op"`
	From  *registry.PartitionSpec `json:"from"`
	To    *registry.PartitionSpec `json:"to"`
	By    string                  `json:"by,omitempty"`
	At    time.Time               `json:"at"`
	Wiped map[string]int64        `json:"wiped,omitempty"`
}

// recorded is R.
func (r *modeRecord) recorded() registry.PartitionSpec {
	if r == nil {
		return registry.PartitionSpec{}
	}
	return registry.SpecOf(r.Mode)
}

// clone is a deep enough copy of r to change and write.
func (r *modeRecord) clone(tile string) *modeRecord {
	if r == nil {
		return &modeRecord{Schema: modeSchema, Tile: tile}
	}
	c := *r
	c.History = slices.Clone(r.History)
	return &c
}

// log appends one history entry, keeping the last modeHistoryMax.
func (r *modeRecord) log(h modeHistory) {
	r.History = append(r.History, h)
	if n := len(r.History) - modeHistoryMax; n > 0 {
		r.History = slices.Delete(r.History, 0, n)
	}
}

// specPtr is s as a record stores it: nil for unpartitioned.
func specPtr(s registry.PartitionSpec) *registry.PartitionSpec {
	if s.IsZero() {
		return nil
	}
	return &s
}

// stateOf is the state a tile running mode s is in.
func stateOf(s registry.PartitionSpec) registry.PartitionState {
	if s.User {
		return registry.PartitionPartitioned
	}
	return registry.PartitionUnpartitioned
}

// decideMode is the state table for one tile: rec is its record (nil when
// it has none), ask what its primary's code requests, holds whether it
// holds data — asked only when Q ≠ R and no decline covers Q. It answers the
// settled mode and the record to write, nil when nothing changes.
func decideMode(rec *modeRecord, ask registry.PartitionAsk, holds func() bool, now time.Time) (registry.PartitionMode, *modeRecord) {
	r := rec.recorded()
	if ask.Invalid != "" {
		return registry.PartitionMode{State: registry.PartitionInvalid, Recorded: r}, nil
	}
	q := registry.SpecOf(ask.Requested)
	settled := registry.PartitionMode{State: stateOf(r), Recorded: r}
	switch {
	case q == r && (rec == nil || rec.Request == nil && rec.Declined == nil):
		return settled, nil
	case q == r: // the code is back at R: the request and the decline go
		next, was := rec.clone(ask.Tile), (*registry.PartitionSpec)(nil)
		if next.Request != nil {
			was = next.Request.Spec
		} else {
			was = next.Declined.Spec
		}
		next.Request, next.Declined = nil, nil
		next.log(modeHistory{Op: modeOpWithdrawn, From: specPtr(r), To: was, At: now})
		return settled, next
	case rec != nil && rec.Declined != nil && registry.SpecOf(rec.Declined.Spec) == q:
		settled.Request = &registry.PartitionRequest{Spec: specPtr(q), Declined: true, Since: rec.Declined.At}
		if rec.Request == nil {
			return settled, nil
		}
		next := rec.clone(ask.Tile) // a request left beside the decline that covers it
		next.Request = nil
		return settled, next
	case !holds():
		next := rec.clone(ask.Tile)
		next.Mode, next.Request, next.Declined = specPtr(q), nil, nil
		next.log(modeHistory{Op: modeOpAuto, From: specPtr(r), To: specPtr(q), At: now})
		return registry.PartitionMode{State: stateOf(q), Recorded: q}, next
	}
	pending := registry.PartitionMode{State: registry.PartitionPending, Recorded: r,
		Request: &registry.PartitionRequest{Spec: specPtr(q), Since: now}}
	if rec != nil && rec.Request != nil && registry.SpecOf(rec.Request.Spec) == q {
		pending.Request.Since = rec.Request.Since
		return pending, nil
	}
	next := rec.clone(ask.Tile) // a new request (a different Q′ replaces one, and any decline)
	next.Request, next.Declined = &modeRequest{Spec: specPtr(q), Since: now}, nil
	next.log(modeHistory{Op: modeOpRequest, From: specPtr(r), To: specPtr(q), At: now})
	return pending, next
}

// partitionModes is the broker's mode store: every tile's record, loaded
// once, the records it couldn't read, and why each held tile's primary may
// not run.
//
// Locks: settleMu serializes each read-decide-write cycle (a settle, a
// decision) and is the only one held across I/O ("holds data" opens kv
// files and decrypts vaults; the record is written); mu guards the maps for
// a moment at a time; heldMu guards held and stop, which the runner and the
// proxy read on every call (PartitionHoldReason) without waiting for a
// settle.
type partitionModes struct {
	settleMu sync.Mutex

	mu         sync.Mutex
	root       string // the workspace
	recs       map[string]*modeRecord
	unread     map[string]string    // TileKey → why its mode.json can't be read: never decided on, never overwritten
	warn       map[string]time.Time // tile → the last failed write, logged once a minute
	sealedWait bool                 // a settle waits for the vault to unseal (resettleAfterUnseal)
	now        func() time.Time     // time.Now; tests pin it

	heldMu sync.RWMutex
	held   map[string]string // tile → HoldReason text while pending or invalid
	stop   func(tile string) // stops the tile's primary (boot: the runner's); nil = nothing runs yet
}

// partitionSlot is the Broker's handle on its mode store.
type partitionSlot struct{ parts *partitionModes }

func newPartitionModes(root string) *partitionModes {
	return &partitionModes{root: root, recs: map[string]*modeRecord{}, unread: map[string]string{},
		warn: map[string]time.Time{}, now: time.Now, held: map[string]string{}}
}

func (pm *partitionModes) dir(tile string) string {
	return filepath.Join(pm.root, "data", partitionsDir, util.TileKey(tile))
}

// errRecordUnread refuses writing a record over one this xbind couldn't
// read (a newer schema, a corrupt file, one that appeared after the load).
var errRecordUnread = errors.New("the partition mode record on disk can't be read by this xbind: it is kept as it is")

// load reads every record under data/partitions. A record this xbind can't
// read — it doesn't parse, has another schema (a newer xbind wrote it),
// names a tile that doesn't hash to its directory, or can't be read at all
// — is kept apart, by its directory (the tile's key): that tile is held
// invalid, and nothing is ever decided on or written over it, so a
// downgrade or a damaged file never runs a partitioned tile as one instance
// or destroys its recorded mode.
func (pm *partitionModes) load() {
	base := filepath.Join(pm.root, "data", partitionsDir)
	entries, err := os.ReadDir(base) // walk-ok: data/partitions is xbind's own; no sandbox sees it
	if err != nil {
		if !errNotExist(err) {
			slog.Error("partitions: the mode records can't be listed; tiles that ask for a mode wait", "err", err)
		}
		return // the zero state: nothing recorded
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, e.Name(), modeFile)) // walk-ok: xbind's own record
		if errNotExist(err) {
			continue
		}
		var rec modeRecord
		switch {
		case err != nil:
		case json.Unmarshal(b, &rec) != nil:
			err = errors.New("it doesn't parse")
		case rec.Schema != modeSchema:
			err = fmt.Errorf("schema %d, this xbind reads %d", rec.Schema, modeSchema)
		case util.TileKey(rec.Tile) != e.Name():
			err = errors.New("it names another tile than its directory's")
		}
		if err != nil {
			slog.Error("partitions: a mode record this xbind can't read; its tile is held and the record kept", "dir", e.Name(), "err", err)
			pm.unread[e.Name()] = err.Error()
			continue
		}
		pm.recs[rec.Tile] = &rec
	}
}

// write stores rec as its tile's record; loaded says whether the tile had
// a record this store read. A record is never written over one this xbind
// couldn't read, nor, for a tile it has none of, over a file that appeared
// since the load.
func (pm *partitionModes) write(rec *modeRecord, loaded bool) error {
	key, dir := util.TileKey(rec.Tile), pm.dir(rec.Tile)
	pm.mu.Lock()
	_, bad := pm.unread[key]
	pm.mu.Unlock()
	if bad {
		return errRecordUnread
	}
	if !loaded {
		if _, err := os.Lstat(filepath.Join(dir, modeFile)); !errNotExist(err) {
			why := "a record appeared that this xbind didn't load"
			if err != nil {
				why = "it can't be checked: " + err.Error()
			}
			pm.mu.Lock()
			pm.unread[key] = why
			pm.mu.Unlock()
			return errRecordUnread
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return fsutil.WriteFileAtomic(filepath.Join(dir, modeFile), append(b, '\n'), 0o600)
}

// initPartitionModes loads the mode store and installs it as the registry's
// PartitionModes hook. The registry's first scan had no store (every tile
// that asks waited, fail closed); it rescans only when there is something to
// settle, so a zero-state workspace reads one missing directory and nothing
// else.
func (b *Broker) initPartitionModes() {
	pm := newPartitionModes(b.Reg.Root)
	pm.load()
	b.parts = pm
	b.Reg.PartitionModes = b.settlePartition
	if len(pm.recs) > 0 || len(pm.unread) > 0 || b.Reg.PartitionAsked() {
		if err := b.Reg.Rescan(); err != nil {
			slog.Warn("partitions: rescan after loading the mode records", "err", err)
		}
	}
}

// SetPartitionStop installs how a tile's primary is stopped when it becomes
// pending or invalid (boot: the runner's StopDeployment of the primary).
func (b *Broker) SetPartitionStop(stop func(tile string)) {
	if b.parts == nil {
		return
	}
	b.parts.heldMu.Lock()
	b.parts.stop = stop
	b.parts.heldMu.Unlock()
}

// resettleAfterUnseal settles again once the vault unseals when a settle
// waited for it: a tile whose only "holds data" answer was a sealed vault
// was paused without a recorded request (tileHoldsData).
func (b *Broker) resettleAfterUnseal() {
	pm := b.parts
	if pm == nil {
		return
	}
	pm.mu.Lock()
	wait := pm.sealedWait
	pm.sealedWait = false
	pm.mu.Unlock()
	if wait {
		if err := b.Reg.Rescan(); err != nil {
			slog.Warn("partitions: rescan after the vault unsealed", "err", err)
		}
	}
}

// settlePartition is the registry's PartitionModes hook: the state table for
// one tile, its record written when it changes. A tile that has no record
// and asks for nothing answers at once, reading nothing. A tile whose record
// can't be read, or whose code's request can't be (PartitionAsk.Unread),
// is held invalid, and nothing is decided or written for it.
func (b *Broker) settlePartition(ask registry.PartitionAsk) registry.PartitionMode {
	pm := b.parts
	pm.settleMu.Lock()
	defer pm.settleMu.Unlock()
	pm.mu.Lock()
	rec, bad := pm.recs[ask.Tile], pm.unread[util.TileKey(ask.Tile)]
	pm.mu.Unlock()
	var mode registry.PartitionMode
	switch {
	case bad != "":
		mode = unreadRecord(bad)
	case rec == nil && ask.Requested == nil && ask.Invalid == "":
		pm.setHeld(ask.Tile, registry.PartitionMode{}, "") // the zero state, a code that can't be read included
		return registry.PartitionMode{}
	case ask.Unread != "":
		mode = registry.PartitionMode{State: registry.PartitionInvalid, Recorded: rec.recorded(),
			Err: fmt.Sprintf("the code's partition request can't be read (%s); the tile waits, in its recorded mode (%s), until it can be", ask.Unread, rec.recorded())}
	default:
		mode = pm.decide(b, rec, ask)
	}
	pm.setHeld(ask.Tile, mode, cmp.Or(ask.Invalid, mode.Err))
	return mode
}

// unreadRecord is the mode of a tile whose record can't be read: invalid,
// R unknown.
func unreadRecord(why string) registry.PartitionMode {
	return registry.PartitionMode{State: registry.PartitionInvalid, Unknown: true,
		Err: "its partition mode record can't be read (" + why + "); nothing is decided or recorded until an admin repairs it"}
}

// decide runs the state table for a tile whose record and request were
// read, and records what changed. "Holds data" is asked without mu held.
// Called under settleMu.
func (pm *partitionModes) decide(b *Broker, rec *modeRecord, ask registry.PartitionAsk) registry.PartitionMode {
	sealed := false
	holds := func() bool {
		if b.switchHold(ask.Tile) != "" {
			return true // a manager's switch is emptying it: never an auto record meanwhile
		}
		held, store, s := b.tileHoldsData(ask)
		if sealed = s; held {
			slog.Debug("partitions: the tile holds data", "tile", ask.Tile, "store", store, "sealed", s)
		}
		return held
	}
	mode, next := decideMode(rec, ask, holds, pm.now())
	if next != nil && sealed && mode.State == registry.PartitionPending {
		// A sealed vault is the only answer: pause, record nothing, and
		// settle again once it unseals (resettleAfterUnseal).
		pm.mu.Lock()
		pm.sealedWait = true
		pm.mu.Unlock()
		return mode
	}
	if next == nil {
		return mode
	}
	err := pm.write(next, rec != nil)
	if err == nil {
		if h, ok := newEntry(rec, next); ok {
			slog.Info("partitions: mode", "tile", ask.Tile, "op", h.Op, "from", registry.SpecOf(h.From).String(), "to", registry.SpecOf(h.To).String())
			go b.partitionModeChanged(ask.Tile, h) // frames reload, a request pushes to managers (partitionswitch.go)
		}
		pm.mu.Lock()
		pm.recs[ask.Tile] = next
		pm.mu.Unlock()
		return mode
	}
	if errors.Is(err, errRecordUnread) {
		pm.mu.Lock()
		why := pm.unread[util.TileKey(ask.Tile)]
		pm.mu.Unlock()
		return unreadRecord(why)
	}
	pm.mu.Lock()
	if last := pm.warn[ask.Tile]; pm.now().Sub(last) > time.Minute {
		slog.Error("partitions: recording the mode failed", "tile", ask.Tile, "err", err)
		pm.warn[ask.Tile] = pm.now()
	}
	pm.mu.Unlock()
	if next.recorded() != rec.recorded() {
		// An unrecorded auto can't take effect: R stays, and the tile waits
		// rather than run a mode xbind can't remember.
		mode = registry.PartitionMode{State: registry.PartitionPending, Recorded: rec.recorded(),
			Request: &registry.PartitionRequest{Spec: ask.Requested, Since: pm.now()}}
	}
	return mode
}

// newEntry is the history entry next added to rec, if it added one.
func newEntry(rec, next *modeRecord) (modeHistory, bool) {
	n := len(next.History)
	if n == 0 {
		return modeHistory{}, false
	}
	h := next.History[n-1]
	if rec != nil && len(rec.History) > 0 {
		if old := rec.History[len(rec.History)-1]; old.Op == h.Op && old.At.Equal(h.At) {
			return modeHistory{}, false
		}
	}
	return h, true
}

// setHeld records why tile's primary may not run in mode (none: it may),
// and stops a primary that just became held. invalid is why an Invalid
// tile is.
func (pm *partitionModes) setHeld(tile string, mode registry.PartitionMode, invalid string) {
	why := ""
	switch mode.State {
	case registry.PartitionPending:
		to := registry.PartitionSpec{}
		if mode.Request != nil {
			to = registry.SpecOf(mode.Request.Spec)
		}
		why = fmt.Sprintf("is paused: a partition mode switch is requested (%s → %s); %s", mode.Recorded, to, partitionHeldNote)
	case registry.PartitionInvalid:
		why = "can't run: partition: " + invalid
	}
	pm.heldMu.Lock()
	defer pm.heldMu.Unlock()
	was := pm.held[tile]
	if why == "" {
		delete(pm.held, tile)
		return
	}
	pm.held[tile] = why
	if was == "" && pm.stop != nil {
		go pm.stop(tile) // it may be running; nothing starts it again while held
	}
}

// PartitionHoldReason says why tile's primary may not run because of its
// partition mode — pending or invalid — or "" when it may. Composed into
// the runner's HoldReason and ShouldRunDeployment (for the primary) at boot;
// it never waits for a settle.
func (b *Broker) PartitionHoldReason(tile string) string {
	pm := b.parts
	if pm == nil {
		return ""
	}
	if why := b.switchStartHold(tile); why != "" { // a manager's switch is wiping it, or its scope (partitionswitch.go)
		return why
	}
	pm.heldMu.RLock()
	defer pm.heldMu.RUnlock()
	return pm.held[tile]
}

// partitionPaused reports whether a delivery to deployment dep ("" is
// main) of tile reaches a primary its partition mode holds: cron ticks are
// then missed and bus deliveries dropped, quietly, as for a disabled tile
// (01 §2.3).
func (b *Broker) partitionPaused(tile, dep string) bool {
	return b.PartitionHoldReason(tile) != "" && b.isPrimary(tile, dep)
}

// errModeStale is a decision whose from/to no longer match the tile.
var errModeStale = errors.New("the tile's partition mode or request changed since")

// recordDecision records a tile manager's act on tile's open request R → Q
// (F13a's routes judge who may act): "keep" declines Q (R runs again,
// nothing deleted); "switch" sets R := Q with the wiped summary, after the
// wipe — on an open request, or on a declined one (a manager may still
// switch after keeping). from and to must still be R and Q. The registry
// settles the new record at the next rescan, which the caller triggers.
func (b *Broker) recordDecision(tile, op string, from, to registry.PartitionSpec, by string, wiped map[string]int64) error {
	pm := b.parts
	if pm == nil {
		return errors.New("partitions: no mode store")
	}
	pm.settleMu.Lock()
	defer pm.settleMu.Unlock()
	pm.mu.Lock()
	rec, bad := pm.recs[tile], pm.unread[util.TileKey(tile)]
	pm.mu.Unlock()
	if bad != "" {
		return errRecordUnread
	}
	open := rec != nil && rec.Request != nil && registry.SpecOf(rec.Request.Spec) == to
	declined := op == modeOpSwitch && rec != nil && rec.Request == nil && rec.Declined != nil && registry.SpecOf(rec.Declined.Spec) == to
	if !open && !declined || rec.recorded() != from {
		return errModeStale
	}
	next, now := rec.clone(tile), pm.now()
	switch op {
	case modeOpKeep:
		next.Request, next.Declined = nil, &modeDeclined{Spec: specPtr(to), By: by, At: now}
	case modeOpSwitch:
		next.Mode, next.Request, next.Declined = specPtr(to), nil, nil
	default:
		return fmt.Errorf("partitions: unknown decision %q", op)
	}
	next.log(modeHistory{Op: op, From: specPtr(from), To: specPtr(to), By: by, At: now, Wiped: wiped})
	if err := pm.write(next, true); err != nil {
		return err
	}
	pm.mu.Lock()
	pm.recs[tile] = next
	pm.mu.Unlock()
	return nil
}

// modeRecordOf is tile's record as stored (nil: none), for the surfaces that
// show history (F13a/F7b).
func (b *Broker) modeRecordOf(tile string) *modeRecord {
	pm := b.parts
	if pm == nil {
		return nil
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	if rec := pm.recs[tile]; rec != nil {
		return rec.clone(tile)
	}
	return nil
}

// grantRefusal is why approving g is refused with 409: D127k's governance
// rule for tiles with non-primary deployments, then PD-28's for partitioned
// tiles — governance, cap:sandboxes, cap:net-admin and cap:containers would
// reach or widen every partition. A tile whose code asks for user
// partitions is refused as well: the grant would make its request invalid.
func (b *Broker) grantRefusal(g registry.Grant) error {
	if err := b.xbinGrantRefusal(g); err != nil {
		return err
	}
	if !registry.PartitionRefusedTarget(g.Target) {
		return nil
	}
	c, ok := b.Reg.Component(g.From)
	if !ok {
		return nil
	}
	_, r, _ := c.PartitionState()
	if q := c.PartitionRequested(); r.User || q != nil && q.User || c.PartitionRecordUnknown() {
		return fmt.Errorf("%s is partitioned (or asks to be): a partitioned tile can't hold %s — it would reach or widen every person's partition", g.From, g.Target)
	}
	return nil
}

// errNotExist reports a missing file or directory.
func errNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
