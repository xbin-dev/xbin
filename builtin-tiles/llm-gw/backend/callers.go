// callers.go — usage per caller of a partitioned tile (API.md "Usage by
// partition"; docs/partitions.md "Providers: calls from partitioned tiles").
//
// A call from a partitioned tile carries X-XBin-Partition ("user:<id>" or
// "global") and, from a person's partition, X-XBin-Partition-Id (the stable
// key of that person's partition). Such calls are counted per caller —
// (X-XBin-From, X-XBin-Deployment, X-XBin-Partition-Id), a partition's id ""
// being the tile's global instance — beside the per-backend counters:
// requests, tokens and cost, metadata only (never a prompt or a model). A
// call from a tile that isn't partitioned carries neither header and is
// counted per backend only, as before: nothing here changes for it.
//
// The optional fairness limit (partitionLimit, off by default) caps how many
// calls one person's partition of a calling tile may have in flight; more
// wait their turn, in order, until one ends or the caller goes away. The
// global instance and tiles that aren't partitioned are never held.
package main

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// maxPartitionLimit bounds partitionLimit.
const maxPartitionLimit = 64

// maxCallerRows bounds the persisted rows: past it the least recently seen
// row goes (a deleted person's partition id is never used again).
const maxCallerRows = 1000

// callerKey is who a counted call comes from.
type callerKey struct{ From, Deployment, PartitionID string }

// callerRow is one caller's persisted counters.
type callerRow struct {
	From       string `json:"from"`
	Deployment string `json:"deployment,omitempty"`
	// Partition is the display name the caller's last call carried:
	// "user:<id>" or "global". Rows are keyed on PartitionID.
	Partition   string  `json:"partition"`
	PartitionID string  `json:"partitionId,omitempty"`
	Reqs        int64   `json:"reqs"`
	TokIn       int64   `json:"tokIn"`
	TokOut      int64   `json:"tokOut"`
	Cost        float64 `json:"cost"`
	Last        int64   `json:"last"` // unix ms of the last call
}

func (r *callerRow) key() callerKey { return callerKey{r.From, r.Deployment, r.PartitionID} }

// gate is a caller's calls in flight and those waiting for a slot.
type gate struct {
	part    string // the caller's display name, for GET /stats
	n       int
	waiters []chan struct{}
}

var (
	callersMu  sync.Mutex
	callerRows map[callerKey]*callerRow // nil until loaded from kv
	gates      = map[callerKey]*gate{}
	limitNow   int // the partitionLimit the last call saw (for releases)

	persistMu sync.Mutex // one kv write at a time, the latest snapshot last

	// The kv the rows live in ("callers"); tests replace them.
	loadCallersKV  = func() ([]byte, error) { return kv.Get("callers") }
	storeCallersKV = func(b []byte) error { return kv.Put("callers", b) }
	nowMs          = func() int64 { return time.Now().UnixMilli() }
)

// callerRef is a counted call: its key and display name. nil for a call
// that isn't counted per caller.
type callerRef struct {
	key  callerKey
	part string
}

// callerOf is r's caller when it comes from a partitioned tile, else nil.
func callerOf(r *http.Request) *callerRef {
	c := xbin.Caller(r)
	if c.Partition == "" || c.From == "" {
		return nil
	}
	return &callerRef{key: callerKey{c.From, c.Deployment, c.PartitionID}, part: c.Partition}
}

// limited: the fairness limit applies to the caller — a person's partition.
func (c *callerRef) limited() bool { return c != nil && c.key.PartitionID != "" }

// loadCallersLocked reads the rows from kv once. callersMu held.
func loadCallersLocked() {
	if callerRows != nil {
		return
	}
	callerRows = map[callerKey]*callerRow{}
	b, err := loadCallersKV()
	if err != nil {
		return
	}
	var rows []*callerRow
	if json.Unmarshal(b, &rows) != nil {
		return
	}
	for _, r := range rows {
		if r != nil && r.From != "" {
			callerRows[r.key()] = r
		}
	}
}

// admitCaller counts a call of c in flight, first waiting for a slot when
// the fairness limit holds c's partition. It answers the release to defer,
// and false when r's caller went away while waiting (nothing to answer).
func admitCaller(r *http.Request, c *callerRef, limit int) (release func(), ok bool) {
	if c == nil {
		return func() {}, true
	}
	callersMu.Lock()
	limitNow = limit
	g := gates[c.key]
	if g == nil {
		g = &gate{part: c.part}
		gates[c.key] = g
	}
	if !c.limited() || limit <= 0 || g.n < limit {
		g.n++
		callersMu.Unlock()
		return func() { releaseCaller(c.key) }, true
	}
	ch := make(chan struct{})
	g.waiters = append(g.waiters, ch)
	callersMu.Unlock()
	select {
	case <-ch: // a release handed its slot on (g.n already counts it)
		return func() { releaseCaller(c.key) }, true
	case <-r.Context().Done():
		callersMu.Lock()
		for i, w := range g.waiters {
			if w == ch {
				g.waiters = append(g.waiters[:i], g.waiters[i+1:]...)
				callersMu.Unlock()
				return nil, false
			}
		}
		callersMu.Unlock()
		releaseCaller(c.key) // handed a slot as it left: pass it on
		return nil, false
	}
}

// releaseCaller ends a call in flight and hands free slots to the waiters.
func releaseCaller(k callerKey) {
	callersMu.Lock()
	defer callersMu.Unlock()
	g := gates[k]
	if g == nil {
		return
	}
	g.n--
	pumpLocked(g)
	if g.n <= 0 && len(g.waiters) == 0 {
		delete(gates, k)
	}
}

// pumpLocked admits waiters while the limit (as last seen) allows.
func pumpLocked(g *gate) {
	for len(g.waiters) > 0 && (limitNow <= 0 || g.n < limitNow) {
		g.n++
		close(g.waiters[0])
		g.waiters = g.waiters[1:]
	}
}

// setPartitionLimit applies a changed limit to calls already waiting.
func setPartitionLimit(limit int) {
	callersMu.Lock()
	defer callersMu.Unlock()
	limitNow = limit
	for _, g := range gates {
		pumpLocked(g)
	}
}

// countCaller adds one finished call to c's row and persists the rows.
func countCaller(c *callerRef, tokIn, tokOut int64, cost float64) {
	if c == nil {
		return
	}
	persistMu.Lock()
	defer persistMu.Unlock()
	callersMu.Lock()
	loadCallersLocked()
	row := callerRows[c.key]
	if row == nil {
		row = &callerRow{From: c.key.From, Deployment: c.key.Deployment, PartitionID: c.key.PartitionID}
		callerRows[c.key] = row
	}
	row.Partition = c.part
	row.Reqs++
	row.TokIn += tokIn
	row.TokOut += tokOut
	row.Cost += cost
	row.Last = nowMs()
	pruneLocked()
	b, err := json.Marshal(sortedRowsLocked())
	callersMu.Unlock()
	if err == nil {
		_ = storeCallersKV(b)
	}
}

// pruneLocked drops the least recently seen rows past maxCallerRows.
func pruneLocked() {
	if len(callerRows) <= maxCallerRows {
		return
	}
	rows := sortedRowsLocked()
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Last > rows[j].Last })
	for _, r := range rows[maxCallerRows:] {
		delete(callerRows, r.key())
	}
}

// sortedRowsLocked is every row by caller, then partition.
func sortedRowsLocked() []*callerRow {
	rows := make([]*callerRow, 0, len(callerRows))
	for _, r := range callerRows {
		rows = append(rows, r)
	}
	sortRows(rows)
	return rows
}

// sortRows orders rows by caller, then partition.
func sortRows(rows []*callerRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.Deployment != b.Deployment {
			return a.Deployment < b.Deployment
		}
		return a.Partition+"\x00"+a.PartitionID < b.Partition+"\x00"+b.PartitionID
	})
}

// callerStat is a row as GET /stats shows it.
type callerStat struct {
	callerRow
	Active  int `json:"active"`
	Waiting int `json:"waiting"`
}

// callersFor is the rows r may see: every row for a manager of this tile
// (write or terminal access, the owner token), only their own partitions'
// for anyone else who opens its page, none while an admin views the
// workspace as someone — per-person usage is the tile's managers'.
func callersFor(r *http.Request) []callerStat {
	c := xbin.Caller(r)
	if c.ViewedBy != "" {
		return nil
	}
	all := c.UserCanWrite()
	callersMu.Lock()
	defer callersMu.Unlock()
	loadCallersLocked()
	rows := sortedRowsLocked()
	for k, g := range gates { // a caller whose first call hasn't ended yet
		if callerRows[k] == nil {
			rows = append(rows, &callerRow{From: k.From, Deployment: k.Deployment, PartitionID: k.PartitionID, Partition: g.part})
		}
	}
	sortRows(rows)
	var out []callerStat
	for _, row := range rows {
		if !all && row.Partition != "user:"+c.User {
			continue
		}
		s := callerStat{callerRow: *row}
		if g := gates[row.key()]; g != nil {
			s.Active, s.Waiting = g.n, len(g.waiters)
		}
		out = append(out, s)
	}
	return out
}

// partitionLimitIn is a PUT /config body's partitionLimit, checked: nil
// when absent, else the limit or why it can't be set.
func partitionLimitIn(r *http.Request, raw json.RawMessage) (*int, string, int) {
	if len(raw) == 0 || strings.TrimSpace(string(raw)) == "null" {
		return nil, "", 0
	}
	if !xbin.Caller(r).UserCanWrite() {
		return nil, "the fairness limit needs write access to this tile", http.StatusForbidden
	}
	var n int
	if json.Unmarshal(raw, &n) != nil || n < 0 || n > maxPartitionLimit {
		return nil, "partitionLimit is a whole number from 0 (off) to 64", http.StatusBadRequest
	}
	return &n, "", 0
}
