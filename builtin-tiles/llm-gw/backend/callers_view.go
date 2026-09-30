// callers_view.go — what GET /stats says about calls from partitioned tiles
// to whoever asks (API.md "Usage by partition"; as docs/partitions.md has it
// for xbind's own metadata: a person sees their own rows, a tile's writers
// and managers totals, per-person rows are an admin's — never content or
// activity over time):
//
//   - the owner token, and this tile's own principals with no person behind
//     them (its page opened with the owner token; its backend): every row —
//     a person's partition's with its counters only, never its last use or
//     calls in flight — and the totals;
//   - a manager of this tile (write or terminal access; a tile granted
//     admin here, with no person behind its call): the global instances'
//     rows, their own partitions' rows, and per calling tile its people's
//     partitions together (callerTotals: how many, requests, tokens, cost);
//   - anyone else who opens the page: their own partitions' rows;
//   - an admin viewing the workspace as someone: nothing.
//
// A workspace admin signed in as themselves reads as a manager: xbind tells
// a tile the person's level on it, not whether they are an admin.
package main

import (
	"net/http"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// selfPath is this tile's path (XBIN_COMPONENT); tests set it.
var selfPath = xbin.Self()

// callerStat is a row as GET /stats shows it.
type callerStat struct {
	callerRow
	Active  int `json:"active,omitempty"`
	Waiting int `json:"waiting,omitempty"`
}

// callerTotal is a calling tile's people's partitions together.
type callerTotal struct {
	From       string  `json:"from"`
	Deployment string  `json:"deployment,omitempty"`
	Partitions int     `json:"partitions"` // people's partitions with a row
	Reqs       int64   `json:"reqs"`
	TokIn      int64   `json:"tokIn"`
	TokOut     int64   `json:"tokOut"`
	Cost       float64 `json:"cost"`
}

// callersView is GET /stats' part about partitioned callers for one viewer.
type callersView struct {
	rows   []callerStat
	totals []callerTotal
	manage bool // may change the fairness limit
}

// viewFor is what r's caller may see.
func viewFor(r *http.Request) callersView {
	c := xbin.Caller(r)
	if c.ViewedBy != "" {
		return callersView{}
	}
	every := c.User == "" && (c.Owner || selfPath != "" && c.From == selfPath)
	manager := c.UserCanWrite()
	mine := func(row *callerRow) bool { return c.User != "" && row.Partition == "user:"+c.User }
	callersMu.Lock()
	defer callersMu.Unlock()
	loadCallersLocked()
	rows := sortedRowsLocked()
	var totals []callerTotal
	if manager { // rows come by tile, then deployment: one total per run
		for _, row := range rows {
			if !row.person() {
				continue
			}
			if n := len(totals); n == 0 || totals[n-1].From != row.From || totals[n-1].Deployment != row.Deployment {
				totals = append(totals, callerTotal{From: row.From, Deployment: row.Deployment})
			}
			t := &totals[len(totals)-1]
			t.Partitions++
			t.Reqs += row.Reqs
			t.TokIn += row.TokIn
			t.TokOut += row.TokOut
			t.Cost += row.Cost
		}
	}
	for k, g := range gates { // a caller whose first call hasn't ended yet
		if callerRows[k] == nil {
			rows = append(rows, &callerRow{From: k.From, Deployment: k.Deployment, PartitionID: k.PartitionID, Partition: g.part})
		}
	}
	sortRows(rows)
	out := callersView{totals: totals, manage: manager}
	for _, row := range rows {
		own := mine(row)
		switch {
		case own, every, manager && !row.person():
		default:
			continue
		}
		s := callerStat{callerRow: *row}
		if row.person() && !own { // someone else's: counters only
			if row.Reqs == 0 {
				continue // not a call ended yet: nothing but that it is live
			}
			s.Last = 0
		} else if g := gates[row.key()]; g != nil {
			s.Active, s.Waiting = g.n, len(g.waiters)
		}
		out.rows = append(out.rows, s)
	}
	return out
}
