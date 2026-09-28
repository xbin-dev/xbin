package deployments

// index.go — the records in memory. Boot reads data/deployments once; after
// that every question about a tile's record is a map lookup (D119d: a save
// costs no file read), and every change is written through before the index
// moves: validated, atomically (fsutil.WriteFileAtomic, D60), mode 0600,
// with the fields this xbind doesn't know kept.
//
// A record is bound to its tile (D119i): it lives at the tile's TileKey, names
// the tile's full path, and carries the owner ref the tile had when it was
// made. A record that doesn't bind is ignored (inert: the tile answers the
// zero state) until an admin clears it; the owner ref is compared with the
// tile's current one on every lookup. A record that can't be used — it
// can't be read, doesn't parse, breaks an invariant, names a checkpoint the
// store doesn't hold, or has a newer schema — holds its tile: it fails
// closed (06-security C7), never onto the work tree, and alone: every other
// tile boots.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/util"
)

// RecordState is what a tile's record means for it.
type RecordState int

const (
	// RecordNone: no record, the zero state (D119c).
	RecordNone RecordState = iota
	// RecordActive: a valid record bound to the tile; it governs the tile.
	RecordActive
	// RecordHeld: the record can't be used; the tile fails closed.
	RecordHeld
	// RecordInert: a record that isn't the tile's (D119i); ignored, so the
	// tile answers the zero state, and no change is committed over it.
	RecordInert
)

// Found is what the index knows of one tile's record.
type Found struct {
	State RecordState
	// Record is the record for RecordActive, and the synthesized zero state
	// for RecordNone and RecordInert; nil for RecordHeld. Read-only.
	Record *Record
	// Err says why, for RecordHeld (a *HeldError) and RecordInert (an
	// *InertError).
	Err error
}

// The conditions a caller tells apart with errors.Is.
var (
	ErrRecordHeld  = errors.New("deployment record held")
	ErrRecordInert = errors.New("deployment record isn't this tile's")
	ErrStaleSeq    = errors.New("deployment record changed")
)

// HeldError is a record that holds its tile.
type HeldError struct {
	Tile string
	Why  string
	// Schema is the newer schema that holds it, 0 otherwise.
	Schema int
}

func (e *HeldError) Error() string {
	if e.Schema > 0 { // 11-contract §1.14's text
		return fmt.Sprintf("%s's deployment record was written by a newer xbind (schema %d)", e.Tile, e.Schema)
	}
	return e.Tile + "'s deployment record can't be used: " + e.Why
}

func (e *HeldError) Is(target error) bool { return target == ErrRecordHeld }

// InertError is a record that doesn't bind to the tile it is read for.
type InertError struct{ Tile, Why string }

func (e *InertError) Error() string {
	return e.Tile + "'s deployment record doesn't belong to it (" + e.Why + "): an admin clears it"
}

func (e *InertError) Is(target error) bool { return target == ErrRecordInert }

// StaleSeqError is a compare-and-set that lost: the record moved on.
type StaleSeqError struct {
	Tile string
	Seq  int64
}

func (e *StaleSeqError) Error() string { // 11-contract §1.14's text
	return fmt.Sprintf("the deployments of %s changed (seq %d); reload and retry", e.Tile, e.Seq)
}

func (e *StaleSeqError) Is(target error) bool { return target == ErrStaleSeq }

// index holds every record of the workspace in memory.
type index struct {
	root string
	// ownerRef is the tile's current owner ref; nil answers "" for every
	// tile.
	ownerRef func(tile string) string
	// hasCheckpoint, when set before load, answers whether tile's store
	// holds a checkpoint tree; a record naming one it doesn't is held
	// (06-security T11). nil: not checked.
	hasCheckpoint func(tile, tree string) bool
	now           func() time.Time

	mu sync.RWMutex
	// tiles maps a tile path to the record file at its TileKey that names
	// it: the only files that can govern a tile.
	tiles map[string]*slot
	// held maps a TileKey to why its record file holds whatever tile has
	// that key, for a file that names no tile: unreadable, or not a record.
	held map[string]string
	// stray maps a TileKey to the tile its record file names instead: a
	// record under another tile's key, inert for every tile.
	stray map[string]string

	lmu   sync.Mutex
	locks map[string]*sync.Mutex // per-tile: commits, removals, owner rewrites

	// dmu keeps the records directory from going under a write. A write (a
	// record, a journal: any tile) holds it shared from creating its
	// directory to renaming its file in (writeIn); removing a directory once
	// it is empty holds it alone (prune). The per-tile locks can't: one
	// tile's opt-out would remove the directory another tile's opt-in had
	// just created, and the write fails (ENOENT).
	dmu sync.RWMutex
}

// slot is one tile's record file as loaded or last written. Slots are
// immutable: a change replaces the slot.
type slot struct {
	rec    *Record // nil when the file names the tile but doesn't decode
	held   string  // non-empty: why the record holds the tile
	schema int     // held by a newer schema: the record's
	// former is the owner ref before the last rewrite, still accepted until
	// the owner store reports the new one, so a transfer that rewrites the
	// record first never leaves it inert in between.
	former *string
}

func newIndex(root string, ownerRef func(string) string) *index {
	return &index{root: root, ownerRef: ownerRef, now: time.Now,
		tiles: map[string]*slot{}, held: map[string]string{}, stray: map[string]string{},
		locks: map[string]*sync.Mutex{}}
}

var recordName = regexp.MustCompile(`^[0-9a-f]{32}\.json$`)

// load reads every record under data/deployments. No directory means no
// record: the zero state, nothing read beyond the one open, nothing
// written. An error means no tile's record can be judged — every pinned
// primary would otherwise run its work tree — and stops xbind; a record
// that can't be used holds its own tile only.
func (x *index) load() error {
	dir := recordDir(x.root)
	d, err := fsutil.OpenBeneath(dir, "")
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case errors.Is(err, syscall.ENOTDIR):
		slog.Warn("deployments: data/deployments isn't a directory; no tile has a deployment record", "path", dir)
		return nil
	case err != nil:
		return fmt.Errorf("reading deployment records: %w", err)
	}
	ents, err := d.ReadDir(-1)
	d.Close()
	if err != nil {
		return fmt.Errorf("reading deployment records: %w", err)
	}
	for _, e := range ents {
		if e.IsDir() || !recordName.MatchString(e.Name()) {
			continue // registration directories, a write's temp file
		}
		key := strings.TrimSuffix(e.Name(), ".json")
		data, err := readRecordFile(dir, e.Name())
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			x.held[key] = "can't read it: " + err.Error()
			continue
		}
		x.admit(key, data)
	}
	x.warnUnapplied()
	return nil
}

// readRecordFile reads one record, never following it out of dir, never
// blocking on a FIFO, and never more than maxRecordBytes.
func readRecordFile(dir, name string) ([]byte, error) {
	f, err := fsutil.OpenBeneath(dir, name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxRecordBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxRecordBytes {
		return nil, fmt.Errorf("larger than %d bytes", maxRecordBytes)
	}
	return data, nil
}

// admit files one record file, read from key.json.
func (x *index) admit(key string, data []byte) {
	var head map[string]json.RawMessage
	if err := json.Unmarshal(data, &head); err != nil {
		x.held[key] = "not a deployment record: " + err.Error()
		return
	}
	var tile string
	if raw, ok := head["tile"]; !ok || json.Unmarshal(raw, &tile) != nil || tile == "" {
		x.held[key] = "not a deployment record: no tile path"
		return
	}
	if util.TileKey(tile) != key {
		x.stray[key] = tile
		return
	}
	r, err := decodeRecord(data)
	if err != nil {
		x.tiles[tile] = &slot{held: "it doesn't parse: " + err.Error()}
		return
	}
	s := &slot{rec: r}
	if err := r.validate(); err != nil {
		var newer errNewerSchema
		if errors.As(err, &newer) {
			s.schema = newer.schema
		}
		s.held = err.Error()
	} else if why := x.missingCheckpoint(r); why != "" {
		s.held = why
	}
	x.tiles[tile] = s
}

// missingCheckpoint names a checkpoint r names that the store doesn't hold.
func (x *index) missingCheckpoint(r *Record) string {
	if x.hasCheckpoint == nil {
		return ""
	}
	for _, name := range sortedKeys(r.Deployments) {
		if cp := r.Deployments[name].Checkpoint; cp != nil && !x.hasCheckpoint(r.Tile, *cp) {
			return fmt.Sprintf("%s's checkpoint %s isn't in the store", name, *cp)
		}
	}
	return ""
}

// warnUnapplied is the admin alert of 06-security T11: one line per record
// that holds its tile or is ignored.
func (x *index) warnUnapplied() {
	for key, why := range x.held {
		slog.Warn("deployments: a record holds its tile", "key", key, "why", why)
	}
	for key, tile := range x.stray {
		slog.Warn("deployments: a record under another tile's key is ignored", "key", key, "names", tile)
	}
	for _, tile := range sortedKeys(x.tiles) {
		switch f := x.get(tile); f.State {
		case RecordHeld, RecordInert:
			slog.Warn("deployments: "+f.Err.Error(), "tile", tile)
		}
	}
}

// owner is tile's current owner ref.
func (x *index) owner(tile string) string {
	if x.ownerRef == nil {
		return ""
	}
	return x.ownerRef(tile)
}

// lookup answers what tile's record means for it, the zero state
// synthesized for a tile it doesn't govern.
func (x *index) lookup(tile string) Found {
	f := x.get(tile)
	if f.State == RecordNone || f.State == RecordInert {
		f.Record = ZeroRecord(tile)
	}
	return f
}

// get is lookup without the synthesized zero state (Record is nil for
// RecordNone and RecordInert): an in-memory lookup, allocating nothing for a
// tile without a record, so the per-save path pays nothing (D119d). The owner
// ref is the owner store's, read now.
func (x *index) get(tile string) Found {
	x.mu.RLock()
	s := x.tiles[tile]
	var heldWhy, strayTile string
	if s == nil && (len(x.held) > 0 || len(x.stray) > 0) {
		key := util.TileKey(tile)
		heldWhy, strayTile = x.held[key], x.stray[key]
	}
	x.mu.RUnlock()

	switch {
	case s == nil && heldWhy != "":
		return Found{State: RecordHeld, Err: &HeldError{Tile: tile, Why: heldWhy}}
	case s == nil && strayTile != "":
		return Found{State: RecordInert, Err: &InertError{Tile: tile, Why: fmt.Sprintf("it names %s", strayTile)}}
	case s == nil:
		return Found{State: RecordNone}
	case s.rec == nil || s.schema > 0:
		// Nothing of it can be trusted, the owner ref included.
		return Found{State: RecordHeld, Err: &HeldError{Tile: tile, Why: s.held, Schema: s.schema}}
	}
	cur := x.owner(tile)
	switch {
	case s.rec.Owner == cur:
		if s.former != nil {
			x.settle(tile, s)
		}
	case s.former != nil && *s.former == cur:
		// A transfer rewrote the record and the owner store hasn't moved yet.
	default:
		return Found{State: RecordInert, Err: &InertError{Tile: tile, Why: fmt.Sprintf("it was made for owner %q, and the tile's owner is %q", s.rec.Owner, cur)}}
	}
	if s.held != "" {
		return Found{State: RecordHeld, Err: &HeldError{Tile: tile, Why: s.held}}
	}
	return Found{State: RecordActive, Record: s.rec}
}

// settle drops a slot's former owner once the owner store reports the new
// one.
func (x *index) settle(tile string, s *slot) {
	x.mu.Lock()
	if x.tiles[tile] == s {
		c := *s
		c.former = nil
		x.tiles[tile] = &c
	}
	x.mu.Unlock()
}

// lock takes tile's change lock.
func (x *index) lock(tile string) func() {
	x.lmu.Lock()
	m := x.locks[tile]
	if m == nil {
		m = &sync.Mutex{}
		x.locks[tile] = m
	}
	x.lmu.Unlock()
	m.Lock()
	return m.Unlock
}

// commit changes tile's record: change edits a copy of the current record
// (the zero state's, for a tile without one), and the result is validated,
// given the next seq and written through before the index moves. expect >=
// 0 is a compare-and-set on seq (0 for the zero state). A held record, or an
// inert one, takes no change: an admin clears it first. The first commit of
// a tile binds the record to it: its schema, the tile's current owner ref
// and a creation stamp. change may not edit the binding or seq.
func (x *index) commit(tile string, expect int64, change func(*Record) error) (*Record, error) {
	unlock := x.lock(tile)
	defer unlock()
	f := x.lookup(tile)
	switch f.State {
	case RecordHeld, RecordInert:
		return nil, f.Err
	}
	cur := f.Record
	if expect >= 0 && cur.Seq != expect {
		return nil, &StaleSeqError{Tile: tile, Seq: cur.Seq}
	}
	next := cur.Clone()
	if err := change(next); err != nil {
		return nil, err
	}
	if next.Schema != cur.Schema || next.Tile != cur.Tile || next.Owner != cur.Owner ||
		next.Created != cur.Created || next.Seq != cur.Seq {
		return nil, fmt.Errorf("%s: a change can't edit the record's binding or seq", tile)
	}
	if f.State == RecordNone {
		next.Schema, next.Owner = RecordSchema, x.owner(tile)
		next.Created = x.now().UTC().Format(time.RFC3339)
	}
	next.Seq = cur.Seq + 1
	if err := next.validate(); err != nil {
		return nil, fmt.Errorf("%s: the change would break the deployment record: %w", tile, err)
	}
	if err := x.write(tile, next); err != nil {
		return nil, err
	}
	x.put(tile, &slot{rec: next})
	return next, nil
}

// write puts r at tile's record path.
func (x *index) write(tile string, r *Record) error {
	data, err := r.encode()
	if err != nil {
		return fmt.Errorf("%s: encoding the deployment record: %w", tile, err)
	}
	if err := x.writeIn(recordPath(x.root, tile), data); err != nil {
		return fmt.Errorf("%s: writing the deployment record: %w", tile, err)
	}
	return nil
}

// writeIn writes data at path, a file under the records directory,
// atomically and mode 0600, creating its directories; none of them is
// removed until the file is in (dmu).
func (x *index) writeIn(path string, data []byte) error {
	x.dmu.RLock()
	defer x.dmu.RUnlock()
	return fsutil.WriteFileAtomicIn(path, data, 0o600)
}

// prune removes each of dirs that is empty, in order (a tile's directory,
// then the records directory), while no write is between creating a
// directory and renaming its file in (dmu).
func (x *index) prune(dirs ...string) {
	x.dmu.Lock()
	defer x.dmu.Unlock()
	for _, d := range dirs {
		_ = os.Remove(d) // only when empty
	}
}

func (x *index) put(tile string, s *slot) {
	x.mu.Lock()
	x.tiles[tile] = s
	x.mu.Unlock()
}

// remove deletes the record file at tile's key, whatever it holds, and
// returns the tile to the zero state: opting out (expect >= 0 compares seq
// first; only an active record has one), and a creation path resetting the
// path (expect < 0). No file: nothing to do. The records directory goes too
// once it is empty.
func (x *index) remove(tile string, expect int64) error {
	unlock := x.lock(tile)
	defer unlock()
	if expect >= 0 {
		f := x.lookup(tile)
		if f.State == RecordHeld || f.State == RecordInert {
			return f.Err
		}
		if f.Record.Seq != expect {
			return &StaleSeqError{Tile: tile, Seq: f.Record.Seq}
		}
	}
	if err := os.Remove(recordPath(x.root, tile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s: removing the deployment record: %w", tile, err)
	}
	x.prune(recordDir(x.root))
	key := util.TileKey(tile)
	x.mu.Lock()
	delete(x.tiles, tile)
	delete(x.held, key)
	delete(x.stray, key)
	x.mu.Unlock()
	return nil
}

// rewriteOwner moves tile's record to owner ref owner in the same step as a
// transfer (D39, D119i). It must run before the owner store moves: the record
// then keeps answering to the former owner until the store reports the new
// one, so no lookup in between finds it inert. Only a record bound to the
// tile now follows it: none, one held without a trustworthy owner (it
// doesn't parse, a newer schema), and an inert one stay as they are. A held
// record that parses keeps its fault and is only re-owned, so the transfer
// doesn't turn it into the zero state.
func (x *index) rewriteOwner(tile, owner string) error {
	unlock := x.lock(tile)
	defer unlock()
	x.mu.RLock()
	s := x.tiles[tile]
	x.mu.RUnlock()
	if s == nil || s.rec == nil || s.schema > 0 {
		return nil
	}
	if f := x.get(tile); f.State == RecordInert || f.State == RecordNone {
		if f.State == RecordInert && x.owner(tile) == owner {
			// Already inert, or the owner store moved first: either way the
			// record isn't adopted (that is an admin's act).
			slog.Warn("deployments: a transfer left the record inert; it must be rewritten before the owner store moves",
				"tile", tile, "record owner", s.rec.Owner, "owner", owner)
		}
		return nil
	}
	if s.rec.Owner == owner {
		return nil
	}
	next := s.rec.Clone()
	next.Owner = owner
	if s.held == "" {
		next.Seq++
	}
	if err := x.write(tile, next); err != nil {
		return err
	}
	former := x.owner(tile)
	x.put(tile, &slot{rec: next, held: s.held, former: &former})
	return nil
}

// fileExists reports whether a record file sits at tile's key.
func (x *index) fileExists(tile string) bool {
	_, err := os.Lstat(recordPath(x.root, tile))
	return err == nil
}

// storeDir is tile's checkpoint store (11-contract §10.3).
func storeDir(root, tile string) string {
	return filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+".git")
}

// viewDir is tile's view repository (05-model §3), derived from the store.
func viewDir(root, tile string) string {
	return filepath.Join(root, "data", "checkpoints", util.TileKey(tile)+".view.git")
}
