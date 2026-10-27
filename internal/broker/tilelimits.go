package broker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/util"
)

// Tile resource limits (D203, docs/isolation.md §Resource limits): a tile's
// manifest asks for more memory, disk or sandbox/VM room than the default
// ("limits", "vm"), an approver approves or raises it, and the runtime
// applies max(default, grant). The decisions and the at-cap hits live in
// data/tile-limits.json (limits.File) — xbind-owned, never the tile's.
//
// The seams below are the contract between the store (this file and its
// siblings: tilelimits_api.go, tilelimits_fit.go, tilelimits_alerts.go,
// tilelimits_facts.go, tilelimits_rows.go) and the runtime that feeds and
// obeys it (boot's stepTileLimits: the runner, the VM and tile-sandbox
// runtimes, the disk monitor).
//
// The store: the file is read once (again on every read while it can't be
// — a hand fix takes effect), held as an immutable *limits.File replaced
// whole on each change (copy on write), so LimitGrant is one atomic load and
// a map lookup. A file that can't be read leaves every tile at its defaults
// (the conservative direction), is never overwritten (writes answer 500)
// and raises an admins' alert of kind tile-limits. Lock order: a caller may
// hold a runtime lock (tilesbx's) and take the store's mutex; the store
// never calls out while holding it — the runtime's answers are gathered
// before, its effects (Apply, the disk monitor's kick, events) run after.

// LimitSources are the runtime's answers the store needs, wired at boot
// (stepTileLimits). Every value is in the kind's unit. The disk kind
// (diskGiB) is the broker's own — the disk monitor's default quota, a
// scope's usage, the partition's room — and never asks these. A nil func is
// "unknown": the kind is not enforced, nothing is used, there is no room
// or maximum to judge by.
type LimitSources struct {
	// Default is kind k's default for tile, and whether the kind is
	// enforced there at all (memMiB without cgroup delegation is not; a VM
	// kind on a tile that runs no VM is not). tile "" asks the workspace's
	// default (GET /limits' defaults).
	Default func(tile string, k limits.Kind) (def int64, enforced bool)
	// Max is the most any tile may have of k (0 = no maximum).
	Max func(k limits.Kind) int64
	// Usage is what tile uses of k now (0 = unknown or none).
	Usage func(tile string, k limits.Kind) int64
	// Capacity is the workspace's room for k: Max, Total and Free. The
	// store fills Reserve and Promised (only the granted extra counts).
	Capacity func(k limits.Kind) limits.Capacity
	// Apply makes a decision take effect: raise a running backend's
	// memory.max, recompute a tile's sandbox book; restart restarts the
	// backend (a VM's size, a crash-looping tile). Called with no store
	// lock held.
	Apply func(tile string, k limits.Kind, restart bool)
	// Applied is what runs now for tile's kind k when it differs from what
	// the store grants — a lowered memory limit waits for the backend's next
	// start; 0 = the same, or unknown. Optional (GET /limits' "applied").
	Applied func(tile string, k limits.Kind) int64
}

const (
	tileLimitsRel = "data/tile-limits.json"
	hitSaveEvery  = 10 * time.Second // at most one write of hits per this
)

// tileLimitState is the broker's limit state: the runtime's sources and the
// store.
type tileLimitState struct {
	src LimitSources

	mu      sync.Mutex
	cur     atomic.Pointer[limits.File] // the decisions and hits; replaced whole, never changed in place
	read    bool                        // cur is the file as read (or found absent)
	bad     error                       // why the file can't be read: defaults apply, nothing is written
	asks    map[string]int64            // the pending asks RefreshLimitAsks last saw: "tile|kind" → value
	hitDue  bool                        // hits changed since the last save
	timer   *time.Timer                 // the coalesced save of hits
	timerN  uint64                      // which timer is s.timer's (a stale one's firing changes nothing)
	saved   time.Time                   // when hits were last saved
	touched map[string]bool             // tiles whose hits repeated since their last event
	closed  bool                        // the broker closed: hits stay in memory

	maxes atomic.Pointer[map[limits.Kind]int64] // Max per kind as the last refresh saw it (LimitGrant's clamp)

	capMu    sync.Mutex
	capAt    time.Time
	capCache map[limits.Kind]limits.Capacity // the runtime's Capacity, kept capacityTTL
}

// SetLimitSources wires the runtime's side (boot, before the server step).
func (b *Broker) SetLimitSources(s LimitSources) { b.tl.src = s }

// LimitGrant is what the decisions on key raise kind k to — the store's
// limits.Effective less the default's floor: 0 when nothing raises it. The
// runtime applies max(default, grant). key is a tile path, or for diskGiB
// the scope's data key (util.ScopeKey).
//
// It never calls out: one atomic load, the registry's read lock for the
// manifest's ask, and arithmetic. An approved ask counts up to what the
// manifest asks now, compared without the default — an ask at or below
// the default is under max(default, grant) anyway.
func (b *Broker) LimitGrant(key string, k limits.Kind) int64 {
	f := b.tl.cur.Load()
	if f == nil {
		f = b.tlDoc() // the first ask: read the file
	}
	e := entryIn(f, key, k)
	if e.Empty() {
		return 0
	}
	g := e.Raised(b.manifestAskOf(key, k), 0)
	if m := b.tlMax(k); m > 0 {
		g = min(g, m)
	}
	return g
}

// RecordLimitHit notes key meeting its kind-k cap: an OOM kill, a full
// quota, a sandbox refused or stopped at its tile's total. The hit is
// sticky — an alert with a raise action — until a decision on the kind or
// an approver's dismissal; repeated hits coalesce (Count, At). Safe to call
// from any goroutine, with any runtime lock held: it never calls out.
//
// h.Count is how many events this report carries (0 = one); At defaults to
// now. The hit is saved at most every hitSaveEvery, and a limits event is
// published for its tile — at once for a new hit, with the save for a
// repeat.
func (b *Broker) RecordLimitHit(key string, k limits.Kind, h limits.Hit) {
	if key == "" || !k.Valid() {
		return
	}
	if h.At.IsZero() {
		h.At = time.Now().UTC()
	}
	n := max(h.Count, 1)
	s := &b.tl
	s.mu.Lock()
	next := cloneFile(b.tlDocLocked())
	bk := bookIn(next, key, k)
	old, had := bk.Hits[k]
	if had {
		h.Since, h.Count = old.Since, old.Count+n
		if h.Detail == "" {
			h.Detail = old.Detail
		}
		if h.Value == 0 {
			h.Value = old.Value
		}
	} else {
		h.Since, h.Count = h.At, n
	}
	bk.Hits[k] = h
	s.cur.Store(next)
	tile := b.limitTileOf(key, k)
	if had {
		if s.touched == nil {
			s.touched = map[string]bool{}
		}
		s.touched[tile] = true
	}
	b.tlHitSaveLocked()
	s.mu.Unlock()
	if !had {
		go b.publishLimits(tile) // never under a caller's lock
	}
}

// ---- the file ----

func (b *Broker) tlPath() string {
	return filepath.Join(b.Reg.Root, filepath.FromSlash(tileLimitsRel))
}

// tlDoc is the store's current file (read on first use; again while it
// can't be read). Never nil.
func (b *Broker) tlDoc() *limits.File {
	b.tl.mu.Lock()
	defer b.tl.mu.Unlock()
	return b.tlDocLocked()
}

// tlDocLocked is tlDoc for a caller holding b.tl.mu.
func (b *Broker) tlDocLocked() *limits.File {
	s := &b.tl
	if s.read {
		return s.cur.Load()
	}
	doc := &limits.File{Schema: limits.FileSchema}
	bs, err := os.ReadFile(b.tlPath())
	var bad error
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		bad = hostless(err)
	case json.Unmarshal(bs, doc) != nil:
		bad = errors.New("it doesn't parse")
	case doc.Schema != limits.FileSchema:
		bad = fmt.Errorf("schema %d, this xbind reads %d", doc.Schema, limits.FileSchema)
	}
	mem := s.cur.Load() // hits recorded while the file couldn't be read
	if bad != nil {
		if s.bad == nil || s.bad.Error() != bad.Error() {
			slog.Error("tile limits: the file can't be read; every tile has its default limits until it is fixed or removed", "path", tileLimitsRel, "err", bad)
		}
		s.bad = bad
		if mem == nil {
			mem = &limits.File{Schema: limits.FileSchema}
			s.cur.Store(mem)
		}
		return mem
	}
	s.read, s.bad = true, nil
	s.cur.Store(doc)
	if mem != nil && mergeHits(doc, mem) {
		b.tlHitSaveLocked()
	}
	return doc
}

// tlUnreadable is why the file can't be read (nil: it can), after a retry.
func (b *Broker) tlUnreadable() error {
	b.tl.mu.Lock()
	defer b.tl.mu.Unlock()
	b.tlDocLocked()
	return b.tl.bad
}

// tlSaveLocked writes next and makes it current; the caller holds b.tl.mu.
// Hits ride along. mkdir: create data/ (a decision; a hit's save never
// recreates it under a removed workspace).
func (b *Broker) tlSaveLocked(next *limits.File, mkdir bool) error {
	s := &b.tl
	if !s.read {
		return fmt.Errorf("%s can't be read (%v): fix or remove it by hand", tileLimitsRel, s.bad)
	}
	prune(next)
	bs, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	write := fsutil.WriteFileAtomic
	if mkdir {
		write = fsutil.WriteFileAtomicIn
	}
	if err := write(b.tlPath(), append(bs, '\n'), 0o600); err != nil {
		slog.Warn("tile limits: can't save", "path", tileLimitsRel, "err", err)
		return fmt.Errorf("can't save %s: %w", tileLimitsRel, hostless(err))
	}
	s.cur.Store(next)
	s.hitDue = false
	return nil
}

// tlHitSaveLocked schedules the save of changed hits: as soon as the last
// save is hitSaveEvery old.
func (b *Broker) tlHitSaveLocked() {
	s := &b.tl
	s.hitDue = true
	if s.timer != nil || s.closed {
		return
	}
	s.timerN++
	n := s.timerN
	s.timer = time.AfterFunc(max(0, time.Until(s.saved.Add(hitSaveEvery))), func() { b.tlFlushHits(n) })
}

// tlFlushHits saves changed hits and publishes the limits event for the
// tiles whose hits repeated since: timer n's firing (0: now, by hand).
func (b *Broker) tlFlushHits(n uint64) {
	s := &b.tl
	s.mu.Lock()
	if n != 0 {
		if n != s.timerN || s.timer == nil {
			s.mu.Unlock()
			return // stale: a later timer, or a flush by hand, took over
		}
		s.timer = nil
	}
	if s.hitDue && s.read && !s.closed {
		if err := b.tlSaveLocked(s.cur.Load(), false); err == nil {
			s.saved = time.Now()
		}
	}
	touched := s.touched
	s.touched = nil
	s.mu.Unlock()
	for _, t := range sortedNames(touched) {
		b.publishLimits(t)
	}
}

// flushLimitHits saves hits not yet saved, at the broker's close; later hits
// stay in memory.
func (b *Broker) flushLimitHits() {
	s := &b.tl
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
	if s.hitDue && s.read && !s.closed {
		_ = b.tlSaveLocked(s.cur.Load(), false)
	}
	s.closed = true
}

// ---- the immutable file's helpers ----

// entryIn is key's kind-k decisions in f (disk: the scopes' book).
func entryIn(f *limits.File, key string, k limits.Kind) limits.Entry {
	if bk := booksOf(f, k)[key]; bk != nil {
		return bk.Limits[k]
	}
	return limits.Entry{}
}

// hitIn is key's kind-k hit in f, if any.
func hitIn(f *limits.File, key string, k limits.Kind) (limits.Hit, bool) {
	if bk := booksOf(f, k)[key]; bk != nil {
		h, ok := bk.Hits[k]
		return h, ok
	}
	return limits.Hit{}, false
}

// booksOf is f's books kind k is kept in: scopes for disk, tiles otherwise.
func booksOf(f *limits.File, k limits.Kind) map[string]*limits.Book {
	if f == nil {
		return nil
	}
	if k == limits.DiskGiB {
		return f.Scopes
	}
	return f.Tiles
}

// cloneFile is a copy of f to change: new top-level maps, the same books
// (bookIn copies the one it changes).
func cloneFile(f *limits.File) *limits.File {
	n := &limits.File{Schema: limits.FileSchema}
	if f != nil {
		n.Tiles, n.Scopes = maps.Clone(f.Tiles), maps.Clone(f.Scopes)
	}
	return n
}

// bookIn is a fresh copy of key's book in next (a clone), installed there,
// its maps allocated.
func bookIn(next *limits.File, key string, k limits.Kind) *limits.Book {
	books := &next.Tiles
	if k == limits.DiskGiB {
		books = &next.Scopes
	}
	if *books == nil {
		*books = map[string]*limits.Book{}
	}
	nb := &limits.Book{}
	if old := (*books)[key]; old != nil {
		nb.Limits, nb.Hits = maps.Clone(old.Limits), maps.Clone(old.Hits)
	}
	if nb.Limits == nil {
		nb.Limits = map[limits.Kind]limits.Entry{}
	}
	if nb.Hits == nil {
		nb.Hits = map[limits.Kind]limits.Hit{}
	}
	(*books)[key] = nb
	return nb
}

// prune drops empty entries and books before a save (next is the caller's
// fresh clone; books it shares with older files are never emptied here).
func prune(f *limits.File) {
	for _, books := range []map[string]*limits.Book{f.Tiles, f.Scopes} {
		for key, bk := range books {
			empty := len(bk.Hits) == 0
			for _, e := range bk.Limits {
				empty = empty && e.Empty()
			}
			if empty {
				delete(books, key)
			}
		}
	}
}

// mergeHits adds mem's hits to doc where doc has none or an older one;
// it reports whether doc changed (doc is fresh from the file, not shared).
func mergeHits(doc, mem *limits.File) bool {
	changed := false
	for _, k := range limits.Kinds {
		for key, bk := range booksOf(mem, k) {
			h, ok := bk.Hits[k]
			if !ok {
				continue
			}
			if old, had := hitIn(doc, key, k); had && !old.At.Before(h.At) {
				continue
			}
			bookIn(doc, key, k).Hits[k] = h
			changed = true
		}
	}
	return changed
}

// ---- the manifest's asks, the maximums ----

// manifestAskOf is what the manifest asks of kind k for key: the tile's,
// or for disk the manifest of the tile that roots the scope keyed key.
func (b *Broker) manifestAskOf(key string, k limits.Kind) int64 {
	tile := key
	if k == limits.DiskGiB {
		if tile = b.scopeRootOfKey(key); tile == "" {
			return 0
		}
	}
	c, ok := b.Reg.Component(tile)
	if !ok || c.IsTemplate() {
		return 0
	}
	return c.Manifest.LimitAsk(k)
}

// limitScopeKey is scope's data key in main: what the disk monitor measures
// and the store keeps the scope's disk decisions under ("workspace" for the
// workspace scope).
func limitScopeKey(scope string) string {
	k, _ := scopeKeys(scope, util.MainDeployment) // main's never fails
	return k.Quota
}

// scopeRootOfKey is the tile that roots the scope with data key key ("" for
// the workspace scope, or a scope no tile roots).
func (b *Broker) scopeRootOfKey(key string) string {
	if key == "workspace" || key == "" {
		return ""
	}
	p := strings.ReplaceAll(key, "~", "/")
	if c, ok := b.Reg.Component(p); ok && c.Scope == p {
		return p
	}
	return ""
}

// limitTileOf is the tile a key's kind-k state is shown on: the key, or for
// disk the scope's root tile ("" for none).
func (b *Broker) limitTileOf(key string, k limits.Kind) string {
	if k == limits.DiskGiB {
		return b.scopeRootOfKey(key)
	}
	return key
}

// tlMax is Max(k) as the last refresh saw it (0 = none known).
func (b *Broker) tlMax(k limits.Kind) int64 {
	if m := b.tl.maxes.Load(); m != nil {
		return (*m)[k]
	}
	return 0
}

// tlRefreshMaxes asks the runtime (and the disk monitor) for every kind's
// maximum, for LimitGrant's clamp. Never under the store's lock.
func (b *Broker) tlRefreshMaxes() {
	m := map[limits.Kind]int64{}
	for _, k := range limits.Kinds {
		m[k] = b.kindMax(k)
	}
	b.tl.maxes.Store(&m)
}

// ---- events, refresh ----

// publishLimits tells clients a tile's limits changed: a decision, a hit, a
// new manifest ask (no payload: they refetch).
func (b *Broker) publishLimits(tile string) {
	if b.Hub != nil {
		b.Hub.Publish(events.Event{Type: "limits", Component: tile})
	}
}

// RefreshLimitAsks notices manifest asks that appeared, changed or went
// away since the last call and publishes a limits event for each such tile
// (the watch loop calls it after each rescan; boot may call it once). It
// also refreshes the maximums LimitGrant clamps by.
func (b *Broker) RefreshLimitAsks() {
	b.tlRefreshMaxes()
	doc := b.tlDoc()
	now := map[string]int64{}
	for _, c := range b.Reg.Components() {
		if c.IsTemplate() {
			continue
		}
		tf := b.tileFacts(c)
		for _, k := range limits.Kinds {
			if kf, ok := tf.kinds[k]; ok && kf.pending(tf, entryIn(doc, tf.keyOf(k), k)) {
				now[c.Path+"|"+string(k)] = kf.ask
			}
		}
	}
	s := &b.tl
	s.mu.Lock()
	old := s.asks
	s.asks = now
	s.mu.Unlock()
	changed := map[string]bool{}
	for key, v := range now {
		if ov, ok := old[key]; !ok || ov != v {
			changed[strings.SplitN(key, "|", 2)[0]] = true
		}
	}
	for key := range old {
		if _, ok := now[key]; !ok {
			changed[strings.SplitN(key, "|", 2)[0]] = true
		}
	}
	for _, t := range sortedNames(changed) {
		b.publishLimits(t)
	}
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
