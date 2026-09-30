package broker

// partitionledger.go — the per-person egress ledger (plans/partitions 06
// §6.1; S1, S10, PD-46): xbind counts, per user partition and per day, what
// left it for another tile, never any content:
//
//   - "edge": an allowed cross-tile partition call (Route) or data reach
//     (allowAt) into the same person's partition of another partitioned
//     tile;
//   - "provider": a call to a tile that isn't partitioned — through a global
//     bind or grant, or the person's own personal bind — by target;
//   - "bus" and "trigger": bus subscriptions by source and private triggers'
//     sources, for the planes that register them (ledgerCount).
//
// It lives in data/partitions/<TileKey>/<dep>/<pkey>/ledger.json (user
// partitions run on the primary only, PD-17), {schema: 1, tile, dep, user,
// uid, days: {"YYYY-MM-DD": {kind: {target: n}}}}, and keeps 90 days. It
// runs in both consent settings. Counting is in memory; a file is written
// when a day gains a row, at most once a minute otherwise, and when xbind
// stops (Broker.Close), so a busy edge costs no write per call.
//
// Who sees what (PD-46): the person their own rows; a tile's writers and
// managers per-target totals for the tile; admins per-target totals per
// person; and, for the Policies tab's turn-on confirmation and bx doctor,
// admins the edges between partitioned tiles with the number of people who
// used each (GET /partitions/edges).

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

// The ledger's kinds.
const (
	LedgerEdge     = "edge"
	LedgerProvider = "provider"
	LedgerBus      = "bus"
	LedgerTrigger  = "trigger"
)

const (
	ledgerFileName = "ledger.json"
	ledgerSchema   = 1
	ledgerKeepDays = 90
	ledgerSaveGap  = time.Minute // at most one write a minute per ledger for counts on existing rows
	ledgerDayFmt   = "2006-01-02"
)

var ledgerNow = time.Now // tests stand in

func init() {
	partitionEdgeSeam = func(b *Broker, userID, from, to string) {
		kind := LedgerProvider
		if _, partitioned, _ := b.tilePartitioning(to); partitioned {
			kind = LedgerEdge
		}
		b.ledgerCount(from, userID, kind, to)
	}
	registerWipeHook(wipeHook{name: "ledgers", wipe: wipeLedgersHook})
}

// ledgerDoc is one user partition's ledger file.
type ledgerDoc struct {
	Schema int                                    `json:"schema"`
	Tile   string                                 `json:"tile"`
	Dep    string                                 `json:"dep"`
	User   string                                 `json:"user"`
	UID    string                                 `json:"uid"`
	Days   map[string]map[string]map[string]int64 `json:"days"` // day → kind → target → count
}

// ledgerEntry is a ledger as this xbind counts it.
type ledgerEntry struct {
	doc    *ledgerDoc
	dirty  bool
	saved  time.Time
	broken bool // the file on disk can't be read by this xbind: counted in memory, never written over
}

type ledgerState struct {
	mu      sync.Mutex
	entries map[string]*ledgerEntry // file path → entry
}

var ledgerStates sync.Map // *Broker → *ledgerState

func (b *Broker) ledgers() *ledgerState {
	v, _ := ledgerStates.LoadOrStore(b, &ledgerState{entries: map[string]*ledgerEntry{}})
	return v.(*ledgerState)
}

// ledgerTileDir holds every ledger of tile.
func (b *Broker) ledgerTileDir(tile string) string {
	return filepath.Join(b.Reg.Root, "data", partitionsDir, util.TileKey(tile))
}

func (b *Broker) ledgerPath(tile, dep, pkey string) string {
	return filepath.Join(b.ledgerTileDir(tile), dep, pkey, ledgerFileName)
}

// readLedgerDoc reads a ledger file: nil without one; an error for a file
// this xbind can't read.
func readLedgerDoc(path string) (*ledgerDoc, error) {
	raw, err := os.ReadFile(path) // walk-ok: data/partitions is xbind's own; no sandbox sees it
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d ledgerDoc
	switch {
	case json.Unmarshal(raw, &d) != nil:
		return nil, errors.New("it doesn't parse")
	case d.Schema != ledgerSchema:
		return nil, fmt.Errorf("schema %d, this xbind reads %d", d.Schema, ledgerSchema)
	}
	if d.Days == nil {
		d.Days = map[string]map[string]map[string]int64{}
	}
	return &d, nil
}

// ledgerCount counts one kind of egress to target by person userID's
// partition of tile (on its primary). Nothing is counted while tile's
// mode switch runs (its ledgers are being deleted), nor for a person whose
// partition id can't be told.
func (b *Broker) ledgerCount(tile, userID, kind, target string) {
	if tile == "" || target == "" || b.switchHold(tile) != "" {
		return
	}
	uid, err := b.mintPartitionUID(userID)
	if err != nil {
		return
	}
	dep := b.primaryOf(tile)
	if !util.DeploymentNameOK(dep) {
		return
	}
	path := b.ledgerPath(tile, dep, util.PartitionKey(userID, uid))
	now := ledgerNow()
	day := now.UTC().Format(ledgerDayFmt)
	ls := b.ledgers()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	e := ls.entries[path]
	if e == nil {
		doc, err := readLedgerDoc(path)
		e = &ledgerEntry{doc: doc, saved: now}
		if err != nil {
			slog.Warn("partitions: a ledger this xbind can't read is kept as it is; counting in memory", "tile", tile, "err", err)
			e.broken, e.doc = true, nil
		}
		if e.doc == nil || e.doc.User != userID || e.doc.UID != uid || e.doc.Tile != tile {
			e.doc = &ledgerDoc{Schema: ledgerSchema, Tile: tile, Dep: dep, User: userID, UID: uid, Days: map[string]map[string]map[string]int64{}}
		}
		ls.entries[path] = e
	}
	kinds := e.doc.Days[day]
	if kinds == nil {
		kinds = map[string]map[string]int64{}
		e.doc.Days[day] = kinds
	}
	targets := kinds[kind]
	if targets == nil {
		targets = map[string]int64{}
		kinds[kind] = targets
	}
	_, had := targets[target]
	targets[target]++
	e.dirty = true
	if !had || now.Sub(e.saved) >= ledgerSaveGap {
		b.saveLedgerLocked(path, e, now)
	}
}

// saveLedgerLocked writes e (days beyond the retention dropped); the
// state's lock is held.
func (b *Broker) saveLedgerLocked(path string, e *ledgerEntry, now time.Time) {
	if e.broken {
		return
	}
	cut := now.UTC().AddDate(0, 0, -ledgerKeepDays).Format(ledgerDayFmt)
	for day := range e.doc.Days {
		if day < cut {
			delete(e.doc.Days, day)
		}
	}
	raw, err := json.Marshal(e.doc)
	if err == nil {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			err = fsutil.WriteFileAtomic(path, append(raw, '\n'), 0o600)
		}
	}
	if err != nil {
		slog.Warn("partitions: the egress ledger can't be saved", "tile", e.doc.Tile, "err", err)
		return
	}
	e.dirty, e.saved = false, now
}

// flushLedgers writes every ledger with counts not yet saved (Broker.Close).
func (b *Broker) flushLedgers() {
	v, ok := ledgerStates.Load(b)
	if !ok {
		return
	}
	ls := v.(*ledgerState)
	ls.mu.Lock()
	defer ls.mu.Unlock()
	now := ledgerNow()
	for path, e := range ls.entries {
		if e.dirty {
			b.saveLedgerLocked(path, e, now)
		}
	}
}

// ledgerDocs are every ledger: the counted ones as this xbind counts them,
// the others as their files say. A file this xbind can't read is skipped.
func (b *Broker) ledgerDocs() []ledgerDoc {
	files, _ := filepath.Glob(filepath.Join(b.Reg.Root, "data", partitionsDir, "*", "*", "u-*", ledgerFileName)) // walk-ok: data/partitions is xbind's own
	ls := b.ledgers()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	var out []ledgerDoc
	seen := map[string]bool{}
	for path, e := range ls.entries {
		seen[path] = true
		out = append(out, cloneLedger(e.doc))
	}
	for _, path := range files {
		if seen[path] {
			continue
		}
		if d, err := readLedgerDoc(path); err == nil && d != nil {
			out = append(out, *d)
		}
	}
	return out
}

func cloneLedger(d *ledgerDoc) ledgerDoc {
	c := *d
	c.Days = make(map[string]map[string]map[string]int64, len(d.Days))
	for day, kinds := range d.Days {
		ck := make(map[string]map[string]int64, len(kinds))
		for k, targets := range kinds {
			ck[k] = make(map[string]int64, len(targets))
			for t, n := range targets {
				ck[k][t] = n
			}
		}
		c.Days[day] = ck
	}
	return c
}

// ledgerRow is one day's count of one kind to one target.
type ledgerRow struct {
	Tile   string `json:"tile,omitempty"`
	User   string `json:"user,omitempty"`
	Day    string `json:"day,omitempty"`
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Count  int64  `json:"count"`
	People int    `json:"people,omitempty"`
}

// ledgerLive reports d as the ledger of its person's current incarnation:
// a recreated person never sees the old one's rows.
func (b *Broker) ledgerLive(d ledgerDoc) bool {
	return d.UID != "" && b.storedPartitionUID(d.User) == d.UID
}

// ledgerSince is the first day of a window of days days.
func ledgerSince(days int) string {
	return ledgerNow().UTC().AddDate(0, 0, -(days - 1)).Format(ledgerDayFmt)
}

// ledgerWindow reads ?days= (1..90, default def).
func ledgerWindow(r *http.Request, def int) int {
	n, err := strconv.Atoi(r.URL.Query().Get("days"))
	if err != nil || n < 1 || n > ledgerKeepDays {
		return def
	}
	return n
}

func (b *Broker) registerPartitionLedger(srv *server.Server) {
	srv.RegisterAPI("GET /partitions/ledger", b.apiPartitionLedger)
	srv.RegisterAPI("GET /partitions/edges", b.apiPartitionEdges)
}

// apiPartitionLedger — GET /partitions/ledger[?tile=][&days=] (PersonOnly):
// rows, the person's own days (of tile, when given); with tile, totals —
// per-target totals over every person, for the tile's writers, managers and
// admins; for admins, people — per-target totals per person (of tile, when
// given). Never a content.
func (b *Broker) apiPartitionLedger(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if !personOwn(w, p) {
		return
	}
	tile := strings.Trim(r.URL.Query().Get("tile"), "/")
	days := ledgerWindow(r, 30)
	admin := b.IsAdmin(p)
	if tile != "" && !admin && !p.CanWriteTile(tile) && !b.mayManageTile(p, tile) && !p.CanReadTile(tile) {
		server.WriteError(w, http.StatusNotFound, "no tile "+tile, consentsDocs)
		return
	}
	since := ledgerSince(days)
	rows := []ledgerRow{}
	totals := map[[2]string]*ledgerRow{}
	type personKey struct{ user, tile, kind, target string }
	people := map[personKey]*ledgerRow{}
	seen := map[[2]string]map[string]bool{}
	for _, d := range b.ledgerDocs() {
		if tile != "" && d.Tile != tile || !b.ledgerLive(d) {
			continue
		}
		for day, kinds := range d.Days {
			if day < since {
				continue
			}
			for kind, targets := range kinds {
				for target, n := range targets {
					if d.User == p.UserID {
						rows = append(rows, ledgerRow{Tile: d.Tile, Day: day, Kind: kind, Target: target, Count: n})
					}
					k := [2]string{kind, target}
					if totals[k] == nil {
						totals[k], seen[k] = &ledgerRow{Kind: kind, Target: target}, map[string]bool{}
					}
					totals[k].Count += n
					seen[k][d.User] = true
					pk := personKey{d.User, d.Tile, kind, target}
					if people[pk] == nil {
						people[pk] = &ledgerRow{User: d.User, Tile: d.Tile, Kind: kind, Target: target}
					}
					people[pk].Count += n
				}
			}
		}
	}
	slices.SortFunc(rows, func(a, c ledgerRow) int {
		return cmp.Or(strings.Compare(c.Day, a.Day), strings.Compare(a.Tile, c.Tile), strings.Compare(a.Kind, c.Kind), strings.Compare(a.Target, c.Target))
	})
	out := map[string]any{"days": days, "rows": rows}
	if tile != "" && (admin || p.CanWriteTile(tile) || b.mayManageTile(p, tile)) {
		list := []ledgerRow{}
		for k, t := range totals {
			t.People = len(seen[k])
			list = append(list, *t)
		}
		sortLedger(list)
		out["totals"] = list
	}
	if admin {
		list := []ledgerRow{}
		for _, t := range people {
			list = append(list, *t)
		}
		sortLedger(list)
		out["people"] = list
	}
	server.WriteJSON(w, http.StatusOK, out)
}

func sortLedger(list []ledgerRow) {
	slices.SortFunc(list, func(a, c ledgerRow) int {
		return cmp.Or(strings.Compare(a.User, c.User), strings.Compare(a.Tile, c.Tile), strings.Compare(a.Kind, c.Kind), strings.Compare(a.Target, c.Target))
	})
}

// partitionEdge is one edge between partitioned tiles as the Policies tab's
// turn-on confirmation and bx doctor show it.
type partitionEdge struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Granted   bool   `json:"granted"`   // a grant lets from reach people's data in to now
	People    int    `json:"people"`    // people whose partitions used it in the window
	Calls     int64  `json:"calls"`     // calls and reaches counted in the window
	Consented int    `json:"consented"` // people who allowed it (kept while the policy is off)
}

// partitionEdges are the granted edges between partitioned tiles and the
// ones the ledgers counted in the last days days.
func (b *Broker) partitionEdges(days int) []partitionEdge {
	edges := map[[2]string]*partitionEdge{}
	edge := func(from, to string) *partitionEdge {
		k := [2]string{from, to}
		if edges[k] == nil {
			edges[k] = &partitionEdge{From: from, To: to}
		}
		return edges[k]
	}
	var parted []string
	for _, c := range b.Reg.Components() {
		if _, ok, err := b.tilePartitioning(c.Path); ok && err == nil {
			parted = append(parted, c.Path)
		}
	}
	for _, z := range parted {
		for _, x := range parted {
			if z != x && b.edgeGranted(z, x) {
				edge(z, x).Granted = true
			}
		}
	}
	since := ledgerSince(days)
	users := map[[2]string]map[string]bool{}
	for _, d := range b.ledgerDocs() {
		if !b.ledgerLive(d) {
			continue
		}
		for day, kinds := range d.Days {
			if day < since {
				continue
			}
			for target, n := range kinds[LedgerEdge] {
				e := edge(d.Tile, target)
				e.Calls += n
				k := [2]string{d.Tile, target}
				if users[k] == nil {
					users[k] = map[string]bool{}
				}
				users[k][d.User] = true
			}
		}
	}
	for k, set := range users {
		edges[k].People = len(set)
	}
	b.countConsents(edges)
	out := make([]partitionEdge, 0, len(edges))
	for _, e := range edges {
		out = append(out, *e)
	}
	slices.SortFunc(out, func(a, c partitionEdge) int {
		return cmp.Or(strings.Compare(a.From, c.From), strings.Compare(a.To, c.To))
	})
	return out
}

// countConsents adds, to each edge, the live people who allowed it.
func (b *Broker) countConsents(edges map[[2]string]*partitionEdge) {
	entries, _ := os.ReadDir(b.consentsBase()) // walk-ok: data/partitions is xbind's own
	for _, en := range entries {
		uid, ok := strings.CutSuffix(en.Name(), ".json")
		if !ok {
			continue
		}
		doc, err := b.consentDocOf(uid)
		if err != nil || doc == nil || b.storedPartitionUID(doc.User) != uid {
			continue
		}
		for k := range doc.Edges {
			from, to, _ := strings.Cut(k, "→")
			if e := edges[[2]string{from, to}]; e != nil {
				e.Consented++
			}
		}
	}
}

// apiPartitionEdges — GET /partitions/edges[?days=30] (admin): the edges
// between partitioned tiles — granted, or used in the window — with how
// many people used and allowed each: what turning partitionConsent on
// would start asking about.
func (b *Broker) apiPartitionEdges(w http.ResponseWriter, r *http.Request) {
	if !b.IsAdmin(auth.PrincipalOf(r)) {
		server.WriteError(w, http.StatusForbidden, "admin only — needs the xbin:admin capability", "/docs/auth.md")
		return
	}
	days := ledgerWindow(r, 30)
	server.WriteJSON(w, http.StatusOK, map[string]any{"days": days,
		"policy": map[string]bool{"partitionConsent": b.Policies().PartitionConsent}, "edges": b.partitionEdges(days)})
}

// ---- the switch's wipe ----

// wipeLedgersHook is the "ledgers" store's part of a switch that deletes
// everything (01 §2.6): every person's ledger of the tile, counted or on
// disk. Removing or adding global keeps them (they are people's
// partitions'). A dry run counts nothing: ledgers are metadata.
func wipeLedgersHook(b *Broker, t wipeTarget, _ *wipeSummary) error {
	if t.Kind != wipeEverything || t.DryRun {
		return nil
	}
	dir := b.ledgerTileDir(t.Tile)
	ls := b.ledgers()
	ls.mu.Lock()
	defer ls.mu.Unlock()
	for path := range ls.entries {
		if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			delete(ls.entries, path)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "*", "u-*", ledgerFileName)) // walk-ok: data/partitions is xbind's own
	if err != nil {
		return err
	}
	var errs []error
	for _, f := range files {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
