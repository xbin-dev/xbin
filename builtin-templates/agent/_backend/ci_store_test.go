package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ci_watch is additive and idempotent: made twice (and with every feature
// schema again) its rows stay; a database from before opens with the table
// empty and its old rows kept, twice; one live watch per conversation and
// branch, while ended ones pile up.
func TestCIWatchSchema(t *testing.T) {
	db := newTestDB(t)
	w := &ciWatch{RootRun: 7, RunID: 7, Source: ciManual, SCM: "apps/scm-github", Repo: "acme/web", Ref: "feature"}
	if err := db.ciInsert(w); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.addCIWatchSchema(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := db.addFeatureSchemas(); err != nil {
			t.Fatalf("feature schemas, run %d: %v", i, err)
		}
	}
	got := db.ciWatchByID(w.ID)
	if got == nil || got.Ref != "feature" || got.State != ciNone || got.SubKey != fmt.Sprintf("ci:%d", w.ID) {
		t.Fatalf("the watch after migrating twice: %+v", got)
	}
	dup := &ciWatch{RootRun: 7, Source: ciPushed, SCM: "apps/scm-github", Repo: "acme/web", Ref: "feature"}
	if err := db.ciInsert(dup); err == nil {
		t.Fatal("two live watches of one conversation's branch")
	}
	if _, err := db.q.Exec(`UPDATE ci_watch SET ended_ms=1 WHERE id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := db.ciInsert(&ciWatch{RootRun: 7, Source: ciPushed, SCM: "apps/scm-github", Repo: "acme/web", Ref: "feature"}); err != nil {
		t.Fatalf("a live watch beside an ended one: %v", err)
	}

	path := seedOldDB(t, oldHarnessRows)
	for i := 0; i < 2; i++ {
		d, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var n int
		if err := d.sql.QueryRow(`SELECT count(*) FROM ci_watch`).Scan(&n); err != nil || n != 0 {
			t.Fatalf("open %d: ci_watch %d %v", i, n, err)
		}
		runs, err := d.queryRuns(`ORDER BY id`)
		if err != nil || len(runs) != 3 {
			t.Fatalf("open %d: the old runs: %v %v", i, runs, err)
		}
		d.sql.Close()
	}
	// never on team: migrate() alone
	if team := newTestTeamDB(t); team.q.QueryRow(`SELECT count(*) FROM ci_watch`).Scan(new(int)) == nil {
		t.Fatal("team has ci_watch")
	}
}

// A conversation's watches add up: failure over pending over success over
// none (a gone watch left out); jobs counted once (a check reporting a job
// is that job), non-job checks and statuses as one each; the current job
// and step only from a snapshot no event patched since its read; the link
// to what failed first.
func TestCIAggregate(t *testing.T) {
	if ciSummaryOf(nil) != nil {
		t.Fatal("no watch: a summary")
	}
	mk := func(state string, c *scmChecks) *ciWatch {
		w := &ciWatch{ID: 1, State: state, FetchedMs: 100, UpdatedMs: 100, SHA: ciSHA1}
		if c != nil {
			w.setChecks(c)
		}
		return w
	}
	a := mk(ciNone, ciChecks(ciSHA1))
	if a.State != ciFailure {
		t.Fatalf("the snapshot's state computed again: %s", a.State)
	}
	s := ciSummaryOf([]*ciWatch{a})
	want := CIJobs{Total: 5, Done: 3, Failed: 1, Running: 1, Queued: 1} // lint, test, build, codecov, jenkins
	if s.State != ciFailure || s.Jobs != want || s.Current != "test (ubuntu) › go test ./..." || s.URL != "https://app.codecov.io/gh/acme/web" || s.StartedAt != 1789990000000 {
		t.Fatalf("summary: %+v", s)
	}
	a.UpdatedMs = 200 // an event patched it after its read: its steps may be stale
	if s := ciSummaryOf([]*ciWatch{a}); s.Current != "" {
		t.Fatalf("current from a patched snapshot: %q", s.Current)
	}
	green := &scmChecks{SHA: ciSHA2, Checks: []scmCheck{{ID: "1", Name: "ok", Status: "completed", Conclusion: "success"}}, Statuses: []scmStatus{}}
	b := mk(ciNone, green)
	pend := mk(ciNone, &scmChecks{SHA: ciSHA2, Checks: []scmCheck{{ID: "2", Name: "slow", Status: "queued"}}, Statuses: []scmStatus{}})
	gone := mk(ciNone, ciChecks(ciSHA1))
	gone.State = ciGone
	for _, c := range []struct {
		ws   []*ciWatch
		want string
	}{{[]*ciWatch{b}, ciSuccess}, {[]*ciWatch{b, pend}, ciPending}, {[]*ciWatch{b, pend, a}, ciFailure}, {[]*ciWatch{b, gone}, ciSuccess},
		{[]*ciWatch{mk(ciNone, nil)}, ciNone}} {
		if got := ciSummaryOf(c.ws); got.State != c.want {
			t.Errorf("state %s, want %s", got.State, c.want)
		}
	}
	// what a snapshot keeps: redacted, links http(s) only, bounded
	tok := "ghs_" + strings.Repeat("A", 36)
	dirty := ciChecks(ciSHA1)
	dirty.Checks[2].Summary = "token " + tok + " leaked"
	dirty.Checks[2].DetailsURL = "javascript:alert(1)"
	dirty.WorkflowRuns[0].Jobs[1].Steps[1].Name = "run ​with " + tok
	dirty.Checks[0].ID = "../../x"
	big := strings.Repeat("x", 5000)
	for i := 0; i < 90; i++ {
		dirty.Checks = append(dirty.Checks, scmCheck{ID: fmt.Sprint(9000 + i), Name: "c", Status: "completed", Conclusion: "neutral", Summary: big, Title: big})
	}
	c := ciClean(dirty)
	raw, _ := json.Marshal(c)
	switch {
	case strings.Contains(string(raw), tok):
		t.Error("a token in the snapshot")
	case strings.Contains(string(raw), "javascript:"):
		t.Error("a javascript: link kept")
	case strings.Contains(string(raw), "​"):
		t.Error("an invisible character kept")
	case c.Checks[0].ID != "":
		t.Errorf("a path-shaped id kept: %q", c.Checks[0].ID)
	case len(raw) > ciSnapMax:
		t.Errorf("snapshot %d bytes", len(raw))
	case len(c.Checks[3].Title) > ciTextMax+4:
		t.Errorf("a title of %d bytes", len(c.Checks[3].Title))
	}
}

// A task's watches are its TaskView.ci (memory only: TaskViews are built
// inside transactions): made from the task's refs (projectRefsHooks), read,
// the board told through projectTaskChanged("ci") when the summary moves;
// lazily by GET /runs/{id}/ci; gone with the task's pull request merged.
func TestTaskCISummary(t *testing.T) {
	fx := newCIFix(t)
	var mu sync.Mutex
	var told []string
	old := taskChangedHooks
	taskChangedHooks = append(append([]func(*DB, *Project, *ProjectTask, string){}, old...), func(_ *DB, _ *Project, k *ProjectTask, what string) {
		mu.Lock()
		told = append(told, fmt.Sprintf("%d:%s", k.N, what))
		mu.Unlock()
	})
	t.Cleanup(func() { taskChangedHooks = old })
	fx.scm.SetChecks("acme/web", "xbin/k3x9/1-fix-login", ciChecks(ciSHA1))
	p, k := fx.ciTask(t, ciSHA1, []TaskPR{{Repo: "acme/web", Number: 42, State: "open", HeadSHA: ciSHA1}})
	if taskCISummary(k.RunID) != nil {
		t.Fatal("a summary before any watch")
	}
	// lazily: the task's CI asked for
	var v CIView
	if code := ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", k.RunID), &v); code != 200 {
		t.Fatalf("GET ci: %d", code)
	}
	ws := fx.live(k.RunID)
	if len(ws) != 1 || ws[0].Source != ciTask || ws[0].PR != 42 || ws[0].ProjectID != p.ID || ws[0].N != 1 {
		t.Fatalf("the task's watch: %s", ciDump(ws))
	}
	ciWait(t, "the first read", func() bool { w := fx.ag.db.ciWatchByID(ws[0].ID); return w != nil && w.FetchedMs > 0 })
	ciWait(t, "the summary", func() bool { s := taskCISummary(k.RunID); return s != nil && s.State == ciFailure })
	if tv := fx.ag.db.projTaskView(p, k); tv.CI == nil || tv.CI.Jobs.Total != 5 {
		t.Fatalf("TaskView.ci: %+v", tv.CI)
	}
	mu.Lock()
	gotTold := strings.Join(told, ",")
	mu.Unlock()
	if !strings.Contains(gotTold, "1:ci") {
		t.Fatalf("the board wasn't told: %q", gotTold)
	}
	var tv TaskView
	if code := ciGET(t, fx.mux, asAlice, fmt.Sprintf("/projects/%d/tasks/1", p.ID), &tv); code != 200 || tv.CI == nil || tv.CI.State != ciFailure {
		t.Fatalf("GET task: %d %+v", code, tv.CI)
	}
	// the refs job again, the PR merged: gone, and out of the summary
	k.PRs, _ = json.Marshal([]TaskPR{{Repo: "acme/web", Number: 42, State: "merged", HeadSHA: ciSHA1}})
	if err := fx.ag.db.Tx(func(t *DB) error {
		for _, h := range projectRefsHooks {
			h(t, p, k)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ws := fx.live(k.RunID); len(ws) != 1 || ws[0].State != ciGone {
		t.Fatalf("after the merge: %s", ciDump(ws))
	}
	if s := taskCISummary(k.RunID); s == nil || s.State != ciNone {
		t.Fatalf("a gone watch's summary: %+v", s)
	}
	// deleting the conversation takes its watches
	if err := fx.ag.db.Tx(func(t *DB) error { return runDeleted(t, k.RunID) }); err != nil {
		t.Fatal(err)
	}
	if n := len(fx.ag.db.ciWatchesWhere(`WHERE root_run=?`, k.RunID)); n != 0 || taskCISummary(k.RunID) != nil {
		t.Fatalf("after the delete: %d rows, summary %+v", n, taskCISummary(k.RunID))
	}
}

// An outcome is kept once per <sha>:<state>: read again it stays the same
// key (one card); a new state or a new head is a new one; GET and the ci
// event both say it.
func TestCIOutcomeCardedOnce(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	fx.scm.SetChecks("acme/web", "feature", ciChecks(ciSHA1))
	w := fx.watchRow(t, root, "feature", ciSHA1, nil)
	fx.read(t, w.ID)
	got := fx.ag.db.ciWatchByID(w.ID)
	if got.Carded != ciSHA1+":failure" {
		t.Fatalf("carded %q", got.Carded)
	}
	fx.scm.SetChecks("acme/web", "feature", ciChecks(ciSHA1)) // the same answer: a 304 (its etag)
	fx.read(t, w.ID)
	if again := fx.ag.db.ciWatchByID(w.ID); again.Carded != got.Carded {
		t.Fatalf("carded moved on a repeat: %q", again.Carded)
	}
	green := &scmChecks{SHA: ciSHA1, State: "success", Checks: []scmCheck{{ID: "1", Name: "ok", Status: "completed", Conclusion: "success"}}, Statuses: []scmStatus{}}
	fx.scm.SetChecks("acme/web", "feature", green) // re-run, passed
	fx.read(t, w.ID)
	if x := fx.ag.db.ciWatchByID(w.ID); x.Carded != ciSHA1+":success" {
		t.Fatalf("after the re-run passed: %q", x.Carded)
	}
	pending := &scmChecks{SHA: ciSHA2, Checks: []scmCheck{{ID: "2", Name: "ok", Status: "in_progress"}}, Statuses: []scmStatus{}}
	fx.scm.SetChecks("acme/web", "feature", pending) // a new head, running
	fx.read(t, w.ID)
	x := fx.ag.db.ciWatchByID(w.ID)
	if x.SHA != ciSHA2 || x.State != ciPending || x.Carded != ciSHA1+":success" {
		t.Fatalf("a new head: sha %s state %s carded %q", x.SHA, x.State, x.Carded)
	}
	var v CIView
	ciGET(t, fx.mux, asAlice, fmt.Sprintf("/runs/%d/ci", root), &v)
	if len(v.Watches) != 1 || v.Watches[0].Outcome != ciSHA1+":success" {
		t.Fatalf("GET says: %+v", v.Watches)
	}
}

// A summary written inside a transaction that then rolls back (E's intake
// failing after scmEventHooks ran, say) doesn't stay in memory: the
// refresher's next pass reads that conversation again — and leaves alone a
// summary a later transaction committed.
func TestCISummaryRolledBack(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	w := fx.watchRow(t, root, "feature", ciSHA1, ciChecks(ciSHA1))
	if err := fx.ag.db.Tx(func(t *DB) error { ciChanged(t, root, w.ID); return nil }); err != nil {
		t.Fatal(err)
	}
	if s := ciCached(fx.ag.db, root); s == nil || s.State != ciFailure {
		t.Fatalf("the summary: %+v", s)
	}
	boom := errors.New("the intake failed")
	err := fx.ag.db.Tx(func(t *DB) error {
		x := t.ciWatchByID(w.ID)
		x.State = ciGone
		if err := t.ciSave(x); err != nil {
			return err
		}
		ciChanged(t, root, x.ID)
		if s := ciCached(t, root); s == nil || s.State != ciNone {
			return fmt.Errorf("inside the transaction: %+v", s)
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatal(err)
	}
	ciPass(t.Context(), fx.ag.db)
	if s := ciCached(fx.ag.db, root); s == nil || s.State != ciFailure {
		t.Fatalf("after the rollback: %+v", s)
	}
	// committed: kept as written
	if err := fx.ag.db.Tx(func(t *DB) error {
		x := t.ciWatchByID(w.ID)
		x.State = ciGone
		if err := t.ciSave(x); err != nil {
			return err
		}
		ciChanged(t, root, x.ID)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ciSettle(fx.ag.db)
	if s := ciCached(fx.ag.db, root); s == nil || s.State != ciNone {
		t.Fatalf("after a commit: %+v", s)
	}
}
