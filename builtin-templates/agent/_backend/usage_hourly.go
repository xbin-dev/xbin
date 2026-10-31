// usage_hourly.go — what each conversation spent, hour by hour (D214): its
// model calls and tokens, written beside addRunCost's lifetime counters
// (db.go), and the deltas of a coding agent's reported cumulative cost
// (harness_map.go onStatus), on the run that spent them. A project's
// overview (project_overview.go) charts them per class of run.
//
// Kept 8 days: older rows go when the hour turns (the first write of a new
// hour prunes) and when the table is opened — no ticker (D81). Spend from
// before this table is not in it. An older build ignores the table.
package main

import (
	"encoding/json"
	"math"
	"strings"
	"sync/atomic"
	"time"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addUsageHourlySchema)
	runDeletedHooks = append(runDeletedHooks, usageRunDeleted)
}

// usageKeep is how long hourly usage is kept.
const usageKeep = 8 * 24 * time.Hour

const usageHourlySchema = `
CREATE TABLE IF NOT EXISTS usage_hourly (
  run_id      INTEGER NOT NULL,
  hour        INTEGER NOT NULL,
  calls       INTEGER NOT NULL DEFAULT 0,
  prompt      INTEGER NOT NULL DEFAULT 0,
  completion  INTEGER NOT NULL DEFAULT 0,
  cost_micros INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, hour)
);
CREATE INDEX IF NOT EXISTS idx_usage_hourly_hour ON usage_hourly(hour);
`

// addUsageHourlySchema makes usage_hourly (schemaAdds: the agent's own
// database, never team) and drops what is past keeping.
func (d *DB) addUsageHourlySchema() error {
	if _, err := d.q.Exec(usageHourlySchema); err != nil {
		return err
	}
	d.pruneUsage(usageClock())
	return nil
}

// hourOf is the start of t's hour (UTC), in unix seconds — usage_hourly.hour.
func hourOf(t time.Time) int64 { return t.Unix() - t.Unix()%3600 }

// usagePruned is the hour usage_hourly was last pruned in (one prune an
// hour); usageClock is the hour's clock (tests fix it).
var (
	usagePruned atomic.Int64
	usageClock  = time.Now
)

// addUsageHour adds to run id's row of the current hour. Only where the
// feature tables are (never team, never a hosted conversation's handle).
func (d *DB) addUsageHour(id int64, calls, prompt, completion int, costMicros int64) {
	if d == nil || !d.features || hostedID(id) || id == 0 {
		return
	}
	now := usageClock()
	h := hourOf(now)
	if _, err := d.q.Exec(`INSERT INTO usage_hourly (run_id, hour, calls, prompt, completion, cost_micros) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id, hour) DO UPDATE SET calls=calls+excluded.calls, prompt=prompt+excluded.prompt,
		  completion=completion+excluded.completion, cost_micros=cost_micros+excluded.cost_micros`,
		id, h, calls, max(prompt, 0), max(completion, 0), max(costMicros, 0)); err != nil {
		return
	}
	if usagePruned.Swap(h) != h {
		d.pruneUsage(now)
	}
}

// pruneUsage drops the hours past usageKeep before now.
func (d *DB) pruneUsage(now time.Time) {
	_, _ = d.q.Exec(`DELETE FROM usage_hourly WHERE hour < ?`, hourOf(now.Add(-usageKeep)))
}

// usageRunDeleted (runDeletedHooks): a deleted conversation's hours go with it.
func usageRunDeleted(t *DB, runID int64) error {
	_, _ = t.q.Exec(`DELETE FROM usage_hourly WHERE run_id=?`, runID)
	return nil
}

// --- a coding agent's cost --------------------------------------------------------------

// usageCostMicros is a usage update's cumulative cost in millionths of a
// dollar (ok false: it says none, or in another currency).
func usageCostMicros(raw string) (int64, bool) {
	if raw == "" {
		return 0, false
	}
	var u struct {
		Cost *struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	if json.Unmarshal([]byte(raw), &u) != nil || u.Cost == nil || u.Cost.Amount < 0 || math.IsNaN(u.Cost.Amount) || math.IsInf(u.Cost.Amount, 0) {
		return 0, false
	}
	if c := strings.TrimSpace(u.Cost.Currency); c != "" && !strings.EqualFold(c, "USD") {
		return 0, false
	}
	return int64(math.Round(u.Cost.Amount * 1e6)), true
}

// addHarnessCost records what a coding agent's session spent since its last
// usage update (prev, as stored): the cumulative cost's delta — or, when
// the cost went down (a new adapter counts from zero), the new cost whole.
func (d *DB) addHarnessCost(runID int64, prev, next string) {
	n, ok := usageCostMicros(next)
	if !ok {
		return
	}
	p, _ := usageCostMicros(prev)
	delta := n - p
	if n < p {
		delta = n
	}
	if delta > 0 {
		d.addUsageHour(runID, 0, 0, 0, delta)
	}
}

// --- reading --------------------------------------------------------------------------------

// usageRow is one run's hour.
type usageRow struct {
	Run, Root, Hour    int64
	Tokens, CostMicros int64
}

// projectUsage is every hour since `since` (unix s) of project pid's
// conversations — its tasks, its coordinators and everything under them
// (a subagent carries its root's origin).
func (d *DB) projectUsage(pid, since int64) []usageRow {
	rows, err := d.q.Query(`SELECT u.run_id, r.root_id, u.hour, u.prompt + u.completion, u.cost_micros
		FROM usage_hourly u JOIN runs r ON r.id=u.run_id
		WHERE u.hour >= ? AND r.origin=? AND r.origin_id=?`, since, originProject, pid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []usageRow
	for rows.Next() {
		var u usageRow
		if rows.Scan(&u.Run, &u.Root, &u.Hour, &u.Tokens, &u.CostMicros) == nil {
			if u.Root == 0 {
				u.Root = u.Run
			}
			out = append(out, u)
		}
	}
	return out
}
