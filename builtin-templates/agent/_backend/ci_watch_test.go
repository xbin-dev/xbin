package main

import (
	"context"
	"testing"
	"time"
)

// The refresher (an ownerLoops entry) re-reads a pending watch nobody's
// events moved at the cadence after its push — not one an event moved
// lately, not a finished one, not one past the cadence (a day after its
// push: left until someone looks) — and stops with its context.
func TestCIBackgroundRefresh(t *testing.T) {
	fx := newCIFix(t)
	old := ciClock.Load()
	ciClock.Store(&ciTimes{Tick: 20 * time.Millisecond, EventHold: 400 * time.Millisecond,
		Cadence: []struct{ Until, Every time.Duration }{{time.Hour, 150 * time.Millisecond}, {24 * time.Hour, time.Hour}},
		GoneFor: old.GoneFor, KeptFor: old.KeptFor})
	t.Cleanup(func() { ciClock.Store(old) })
	root := fx.conv(t, "alice", true)
	pending := &scmChecks{SHA: ciSHA1, Checks: []scmCheck{{ID: "1", Name: "slow", Status: "in_progress"}}, Statuses: []scmStatus{}}
	for _, ref := range []string{"fresh", "older", "stale", "done"} {
		fx.scm.SetChecks("acme/web", ref, pending)
	}
	fresh := fx.watchRow(t, root, "fresh", ciSHA1, pending)
	older := fx.watchRow(t, root, "older", ciSHA1, pending)
	stale := fx.watchRow(t, root, "stale", ciSHA1, pending)
	done := fx.watchRow(t, root, "done", ciSHA1, &scmChecks{SHA: ciSHA1, Checks: []scmCheck{{ID: "2", Name: "ok", Status: "completed", Conclusion: "success"}}, Statuses: []scmStatus{}})
	set := func(id int64, since time.Duration) {
		at := time.Now().Add(-since).UnixMilli()
		if _, err := fx.ag.db.q.Exec(`UPDATE ci_watch SET since=?, fetched_ms=? WHERE id=?`, at, at, id); err != nil {
			t.Fatal(err)
		}
	}
	set(fresh.ID, time.Second)  // pushed a second ago: every 150 ms
	set(older.ID, 2*time.Hour)  // two hours: hourly, read 2 h ago… once
	set(stale.ID, 25*time.Hour) // past the cadence: left alone
	set(done.ID, time.Second)   // nothing pending
	reads := func(ref string) int {
		n := 0
		for _, r := range fx.scm.Requests("GET /checks") {
			if containsQuery(r.Query, "ref", ref) {
				n++
			}
		}
		return n
	}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { ciLoop(ctx, fx.ag.eng); close(stopped) }()
	ciWait(t, "three reads of the fresh watch", func() bool { return reads("fresh") >= 3 })
	if n := reads("older"); n != 1 {
		t.Fatalf("the older watch read %d times (want once: its hour had passed)", n)
	}
	if reads("stale") != 0 || reads("done") != 0 {
		t.Fatalf("read past the cadence (%d) or finished (%d)", reads("stale"), reads("done"))
	}
	// an event moved it just now: left alone while the hold lasts
	ciEvented.Store(ciKey{fx.ag.db.sql, fresh.ID}, nowMs())
	time.Sleep(30 * time.Millisecond)
	n := reads("fresh")
	time.Sleep(250 * time.Millisecond)
	if got := reads("fresh"); got > n+1 {
		t.Fatalf("read %d times while an event held it", got-n)
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("the refresher didn't stop with its context")
	}
	n = reads("fresh")
	time.Sleep(200 * time.Millisecond)
	if reads("fresh") != n {
		t.Fatal("read after it stopped")
	}
}

func containsQuery(raw, k, v string) bool {
	for _, kv := range splitAmp(raw) {
		if kv == k+"="+v {
			return true
		}
	}
	return false
}

func splitAmp(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == '&' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return out
}
