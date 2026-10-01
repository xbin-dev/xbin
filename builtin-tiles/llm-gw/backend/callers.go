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
// Counting is in memory; one writer goroutine persists the latest snapshot
// to kv after calls ended (coalesced: a burst of calls is one write), so no
// call waits on kv. Who sees which rows is callers_view.go's; the optional
// fairness limit is fairness.go's.
package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sort"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

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
	Last        int64   `json:"last,omitempty"` // unix ms of the last call
}

func (r *callerRow) key() callerKey { return callerKey{r.From, r.Deployment, r.PartitionID} }

// person reports whether the row is a person's partition (not a global
// instance).
func (r *callerRow) person() bool { return r.PartitionID != "" }

var (
	callersMu sync.Mutex
	// callerRows: the persisted rows once loaded, plus the calls counted
	// since this process started (a failed load never drops them).
	callerRows    = map[callerKey]*callerRow{}
	callersLoaded bool  // the persisted rows are in callerRows
	callersDirty  bool  // callerRows has counts kv hasn't
	loadRetryAt   int64 // unix ms: a failed load is tried again after it

	persistMu   sync.Mutex // one kv write at a time, the latest snapshot last
	persistWake = make(chan struct{}, 1)
	persistOnce sync.Once

	// The kv the rows live in ("callers"); tests replace them.
	loadCallersKV  = func() ([]byte, error) { return kv.Get("callers") }
	storeCallersKV = func(b []byte) error { return kv.Put("callers", b) }
	nowMs          = func() int64 { return time.Now().UnixMilli() }
	persistRetry   = 2 * time.Second // after a failed load or write
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

// loadCallersLocked reads the persisted rows once, adding them to what was
// counted since the start. A missing key is no rows; any other kv error is
// tried again later — never "no rows", which the next write would persist
// over every counter. callersMu held.
func loadCallersLocked() {
	if callersLoaded || nowMs() < loadRetryAt {
		return
	}
	b, err := loadCallersKV()
	var rows []*callerRow
	switch {
	case errors.Is(err, xbin.ErrNotFound):
	case err != nil:
		loadRetryAt = nowMs() + persistRetry.Milliseconds()
		log.Printf("llm-gw: reading the per-caller counters: %v (trying again)", err)
		return
	case json.Unmarshal(b, &rows) != nil:
		log.Printf("llm-gw: the per-caller counters in kv are unreadable: starting them afresh")
		rows = nil
	}
	counted := make(map[callerKey]bool, len(callerRows)) // since the start: the people as they are now
	for k := range callerRows {
		counted[k] = true
	}
	for _, r := range rows {
		if r == nil || r.From == "" {
			continue
		}
		m := callerRows[r.key()]
		if m == nil {
			callerRows[r.key()] = r
			continue
		}
		m.Reqs += r.Reqs // counted since the start: the persisted add up
		m.TokIn += r.TokIn
		m.TokOut += r.TokOut
		m.Cost += r.Cost
		m.Last = max(m.Last, r.Last)
	}
	callersLoaded = true
	for _, current := range []bool{true, false} { // the rows counted since the start first
		for _, r := range sortedRowsLocked() {
			if callerRows[r.key()] == r && counted[r.key()] == current {
				supersedeLocked(r, current)
			}
		}
	}
	pruneLocked()
}

// supersedeLocked drops the rows of an earlier partition of the same person
// of the same calling tile: the display name "user:<id>" came with another
// partition id, so that person was deleted and recreated (or their
// partition reset) — the new one never inherits the old one's records, and
// the old ones aren't kept. A row this process counted (current) wins over
// any other; of rows read back from kv the most recently used one does —
// never by time alone for a current row: the old partition's last call and
// the new one's first can fall in the same millisecond.
func supersedeLocked(row *callerRow, current bool) {
	if !row.person() {
		return
	}
	for k, o := range callerRows {
		if o != row && o.person() && o.From == row.From && o.Deployment == row.Deployment &&
			o.Partition == row.Partition && (current || o.Last <= row.Last) {
			delete(callerRows, k)
		}
	}
}

// countCaller adds one finished call to c's row; the writer persists it.
func countCaller(c *callerRef, tokIn, tokOut int64, cost float64) {
	if c == nil {
		return
	}
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
	supersedeLocked(row, true)
	pruneLocked()
	callersDirty = true
	callersMu.Unlock()
	persistOnce.Do(func() { go persistLoop() })
	select {
	case persistWake <- struct{}{}:
	default: // a write is due already: it takes this call's count too
	}
}

// persistLoop is the one writer: each wake writes the latest snapshot.
func persistLoop() {
	for range persistWake {
		for !persistCallers() {
			time.Sleep(persistRetry)
		}
	}
}

// persistCallers writes the rows to kv when they changed. false: it
// couldn't (the load or the write failed) — try again later.
func persistCallers() bool {
	persistMu.Lock()
	defer persistMu.Unlock()
	callersMu.Lock()
	loadCallersLocked()
	if !callersDirty {
		callersMu.Unlock()
		return true
	}
	if !callersLoaded {
		callersMu.Unlock()
		return false
	}
	b, err := json.Marshal(sortedRowsLocked())
	callersDirty = false
	callersMu.Unlock()
	if err != nil {
		return true
	}
	if err := storeCallersKV(b); err != nil {
		log.Printf("llm-gw: writing the per-caller counters: %v (trying again)", err)
		callersMu.Lock()
		callersDirty = true
		callersMu.Unlock()
		return false
	}
	return true
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
