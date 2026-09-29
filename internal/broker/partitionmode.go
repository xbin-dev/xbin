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
// once, and why each held tile's primary may not run.
type partitionModes struct {
	mu   sync.Mutex
	root string // the workspace
	recs map[string]*modeRecord
	held map[string]string    // tile → HoldReason text while pending or invalid
	now  func() time.Time     // time.Now; tests pin it
	stop func(tile string)    // stops the tile's primary (boot: the runner's); nil = nothing runs yet
	warn map[string]time.Time // tile → the last failed write, logged once
}

// partitionSlot is the Broker's handle on its mode store.
type partitionSlot struct{ parts *partitionModes }

func (pm *partitionModes) dir(tile string) string {
	return filepath.Join(pm.root, "data", partitionsDir, util.TileKey(tile))
}

// load reads every record under data/partitions. A record whose tile doesn't
// hash to its directory is skipped (never read as another tile's).
func (pm *partitionModes) load() {
	base := filepath.Join(pm.root, "data", partitionsDir)
	entries, err := os.ReadDir(base) // walk-ok: data/partitions is xbind's own; no sandbox sees it
	if err != nil {
		return // the zero state: nothing recorded
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(base, e.Name(), modeFile)) // walk-ok: xbind's own record
		if err != nil {
			continue
		}
		var rec modeRecord
		if err := json.Unmarshal(b, &rec); err != nil || rec.Schema != modeSchema || util.TileKey(rec.Tile) != e.Name() {
			slog.Warn("partitions: a mode record this xbind doesn't read is skipped", "dir", e.Name(), "err", err)
			continue
		}
		pm.recs[rec.Tile] = &rec
	}
}

// write stores rec as its tile's record.
func (pm *partitionModes) write(rec *modeRecord) error {
	dir := pm.dir(rec.Tile)
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
	pm := &partitionModes{root: b.Reg.Root, recs: map[string]*modeRecord{}, held: map[string]string{},
		now: time.Now, warn: map[string]time.Time{}}
	pm.load()
	b.parts = pm
	b.Reg.PartitionModes = b.settlePartition
	if len(pm.recs) > 0 || b.Reg.PartitionAsked() {
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
	b.parts.mu.Lock()
	b.parts.stop = stop
	b.parts.mu.Unlock()
}

// settlePartition is the registry's PartitionModes hook: the state table for
// one tile, its record written when it changes. A tile that has no record
// and asks for nothing answers at once, reading nothing.
func (b *Broker) settlePartition(ask registry.PartitionAsk) registry.PartitionMode {
	pm := b.parts
	pm.mu.Lock()
	defer pm.mu.Unlock()
	rec := pm.recs[ask.Tile]
	if rec == nil && ask.Requested == nil && ask.Invalid == "" {
		delete(pm.held, ask.Tile)
		return registry.PartitionMode{}
	}
	holds := func() bool {
		held, store := b.tileHoldsData(ask)
		if held {
			slog.Debug("partitions: the tile holds data", "tile", ask.Tile, "store", store)
		}
		return held
	}
	mode, next := decideMode(rec, ask, holds, pm.now())
	if next != nil {
		if err := pm.write(next); err != nil {
			if last := pm.warn[ask.Tile]; pm.now().Sub(last) > time.Minute {
				slog.Error("partitions: recording the mode failed", "tile", ask.Tile, "err", err)
				pm.warn[ask.Tile] = pm.now()
			}
			if next.recorded() != rec.recorded() {
				// An unrecorded auto can't take effect: R stays, and the
				// tile waits rather than run a mode xbind can't remember.
				mode = registry.PartitionMode{State: registry.PartitionPending, Recorded: rec.recorded(),
					Request: &registry.PartitionRequest{Spec: ask.Requested, Since: pm.now()}}
			}
		} else {
			if h, ok := newEntry(rec, next); ok {
				slog.Info("partitions: mode", "tile", ask.Tile, "op", h.Op, "from", registry.SpecOf(h.From).String(), "to", registry.SpecOf(h.To).String())
			}
			pm.recs[ask.Tile] = next
		}
	}
	pm.setHeld(ask.Tile, mode, ask.Invalid)
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
// and stops a primary that just became held. Called with pm.mu held.
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
// the runner's HoldReason and ShouldRunDeployment (for the primary) at boot.
func (b *Broker) PartitionHoldReason(tile string) string {
	pm := b.parts
	if pm == nil {
		return ""
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	return pm.held[tile]
}

// errModeStale is a decision whose from/to no longer match the tile.
var errModeStale = errors.New("the tile's partition mode or request changed since")

// recordDecision records a tile manager's act on tile's open request R → Q
// (F13a's routes judge who may act): "keep" declines Q (R runs again,
// nothing deleted); "switch" sets R := Q with the wiped summary, after the
// wipe. from and to must still be R and Q. The registry settles the new
// record at the next rescan, which the caller triggers.
func (b *Broker) recordDecision(tile, op string, from, to registry.PartitionSpec, by string, wiped map[string]int64) error {
	pm := b.parts
	if pm == nil {
		return errors.New("partitions: no mode store")
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	rec := pm.recs[tile]
	if rec == nil || rec.Request == nil || rec.recorded() != from || registry.SpecOf(rec.Request.Spec) != to {
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
	if err := pm.write(next); err != nil {
		return err
	}
	pm.recs[tile] = next
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
	if q := c.PartitionRequested(); r.User || q != nil && q.User {
		return fmt.Errorf("%s is partitioned (or asks to be): a partitioned tile can't hold %s — it would reach or widen every person's partition", g.From, g.Target)
	}
	return nil
}

// errNotExist reports a missing file or directory.
func errNotExist(err error) bool { return errors.Is(err, fs.ErrNotExist) }
