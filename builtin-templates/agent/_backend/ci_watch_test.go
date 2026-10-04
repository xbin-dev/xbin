package main

import (
	"context"
	"fmt"
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

// A task's CI asked for makes its watches once: a visit to a task whose
// watch went and ended (its pull request merged a day ago) makes none again
// and posts no subscription; a done or closed task never had one made.
func TestCITaskLazyOnce(t *testing.T) {
	fx := newCIFix(t)
	fx.scm.SetChecks("acme/web", "xbin/k3x9/1-fix-login", ciChecks(ciSHA1))
	_, k := fx.ciTask(t, ciSHA1, []TaskPR{{Repo: "acme/web", Number: 42, State: "merged", HeadSHA: ciSHA1}})
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", k.RunID), nil)
	ws := fx.live(k.RunID)
	if len(ws) != 1 || ws[0].State != ciGone {
		t.Fatalf("the merged task's watch: %s", ciDump(ws))
	}
	ciWait(t, "its setup", func() bool { return len(fx.scm.Requests("POST /subscriptions")) == 1 })
	if _, err := fx.ag.db.q.Exec(`UPDATE ci_watch SET updated_ms=? WHERE id=?`, time.Now().Add(-25*time.Hour).UnixMilli(), ws[0].ID); err != nil {
		t.Fatal(err)
	}
	ciPass(t.Context(), fx.ag.db) // a day after it went: ended
	if n := len(fx.live(k.RunID)); n != 0 {
		t.Fatalf("%d live after a day", n)
	}
	subs, reads := len(fx.scm.Requests("POST /subscriptions")), len(fx.scm.Requests("GET /checks"))
	for i := 0; i < 3; i++ {
		ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", k.RunID), nil)
	}
	// the refs job again, nothing moved: no new gone watch either
	p, _ := fx.ag.db.getProject(k.ProjectID)
	if err := fx.ag.db.Tx(func(t *DB) error { ciTaskRefs(t, p, k); return nil }); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if ws := fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, k.RunID); len(ws) != 1 {
		t.Fatalf("a finished task's visits made watches: %s", ciDump(ws))
	}
	if len(fx.scm.Requests("POST /subscriptions")) != subs || len(fx.scm.Requests("GET /checks")) != reads {
		t.Fatal("a finished task's visit subscribed or read")
	}
	// a done task with no watch: none made
	if _, err := fx.ag.db.q.Exec(`DELETE FROM ci_watch WHERE root_run=?`, k.RunID); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE project_tasks SET phase=? WHERE id=?`, phaseDone, k.ID); err != nil {
		t.Fatal(err)
	}
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", k.RunID), nil)
	if ws := fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, k.RunID); len(ws) != 0 {
		t.Fatalf("a done task's visit: %s", ciDump(ws))
	}
}
