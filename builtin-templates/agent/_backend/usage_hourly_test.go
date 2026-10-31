package main

import (
	"testing"
	"time"
)

func usageAt(t *testing.T, d *DB, run, hour int64) (calls, prompt, completion, cost int64, ok bool) {
	t.Helper()
	err := d.q.QueryRow(`SELECT calls, prompt, completion, cost_micros FROM usage_hourly WHERE run_id=? AND hour=?`, run, hour).
		Scan(&calls, &prompt, &completion, &cost)
	return calls, prompt, completion, cost, err == nil
}

// fixUsageClock sets usage_hourly's clock to at (put back after the test).
func fixUsageClock(t *testing.T, at time.Time) {
	t.Helper()
	old := usageClock
	usageClock = func() time.Time { return at }
	t.Cleanup(func() { usageClock = old })
}

// usage_hourly: written beside addRunCost and by a coding agent's cost
// deltas, never where the feature tables aren't, gone with its run, and
// pruned past 8 days when the hour turns and when the table is opened.
func TestUsageHourly(t *testing.T) {
	ag, _ := accessFixture(t)
	d := ag.db
	run := runAs(t, ag, runStamp{Owner: "alice"}, false)
	now := time.Now()
	fixUsageClock(t, now) // the hour can't turn under the test
	h := hourOf(now)
	d.addRunCost(run, 100, 10)
	d.addRunCost(run, 50, 5)
	if calls, p, c, _, ok := usageAt(t, d, run, h); !ok || calls != 2 || p != 150 || c != 15 {
		t.Fatalf("this hour: calls %d prompt %d completion %d (%v)", calls, p, c, ok)
	}
	// a coding agent's cumulative cost: deltas; a new adapter counting from zero adds its cost whole;
	// another currency, or none said, adds nothing
	for _, step := range []struct{ prev, next string }{
		{"", `{"used":10,"size":100,"cost":{"amount":0.40,"currency":"USD"}}`},
		{`{"cost":{"amount":0.40,"currency":"USD"}}`, `{"cost":{"amount":1.00}}`},
		{`{"cost":{"amount":1.00}}`, `{"cost":{"amount":0.25}}`},
		{`{"cost":{"amount":0.25}}`, `{"cost":{"amount":9,"currency":"EUR"}}`},
		{`{"cost":{"amount":0.25}}`, `{"used":3}`},
		{`{"cost":{"amount":0.25}}`, `not json`},
	} {
		d.addHarnessCost(run, step.prev, step.next)
	}
	if calls, _, _, cost, _ := usageAt(t, d, run, h); calls != 2 || cost != 1_250_000 {
		t.Fatalf("cost: %d micros, calls %d (want 1 250 000, 2)", cost, calls)
	}
	// never on a handle without the feature tables (team's)
	bare := &DB{sql: d.sql, q: d.sql}
	bare.addRunCost(run, 1000, 1000)
	if _, p, _, _, _ := usageAt(t, d, run, h); p != 150 {
		t.Fatalf("a handle without the features wrote: prompt %d", p)
	}
	// pruning: the first write of a new hour drops what is past 8 days
	other := runAs(t, ag, runStamp{Owner: "alice"}, false)
	old, kept := h-9*24*3600, h-7*24*3600
	for _, hr := range []int64{old, kept} {
		uhExec(t, d, `INSERT INTO usage_hourly (run_id, hour, calls, prompt) VALUES (?, ?, 1, 1)`, other, hr)
	}
	usagePruned.Store(h) // pruned this hour already: nothing goes
	d.addRunCost(run, 1, 1)
	if _, _, _, _, ok := usageAt(t, d, other, old); !ok {
		t.Fatalf("pruned twice in an hour")
	}
	usagePruned.Store(h - 3600) // the hour turned
	d.addRunCost(run, 1, 1)
	if _, _, _, _, ok := usageAt(t, d, other, old); ok {
		t.Errorf("a 9-day-old hour outlived the hour's prune")
	}
	if _, _, _, _, ok := usageAt(t, d, other, kept); !ok {
		t.Errorf("a 7-day-old hour was pruned")
	}
	// …and opening the table prunes too
	uhExec(t, d, `INSERT INTO usage_hourly (run_id, hour, calls) VALUES (?, ?, 1)`, other, old)
	if err := d.addUsageHourlySchema(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, ok := usageAt(t, d, other, old); ok {
		t.Errorf("opening the table kept a 9-day-old hour")
	}
	// a deleted conversation's hours go with it
	if err := d.deleteRun(run); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM usage_hourly WHERE run_id=?`, run).Scan(&n)
	if n != 0 {
		t.Errorf("a deleted run's hours stayed: %d", n)
	}
}

func uhExec(t *testing.T, d *DB, q string, args ...any) {
	t.Helper()
	if _, err := d.q.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
