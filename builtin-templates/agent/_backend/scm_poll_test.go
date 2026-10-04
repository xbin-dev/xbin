package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// The cadence (API.md §scm events and polling): without events every
// 2 min to 20 min, every 10 to 2 h, every 30 to 24 h, then a waking "lost
// track of CI"; with healthy events a first read at 30 min, then every 15.
func TestPollCadence(t *testing.T) {
	for _, c := range []struct {
		age     time.Duration
		healthy bool
		want    time.Duration
		stop    bool
	}{
		{0, false, 2 * time.Minute, false}, {19 * time.Minute, false, 2 * time.Minute, false},
		{20 * time.Minute, false, 10 * time.Minute, false}, {119 * time.Minute, false, 10 * time.Minute, false},
		{2 * time.Hour, false, 30 * time.Minute, false}, {23 * time.Hour, false, 30 * time.Minute, false},
		{24 * time.Hour, false, 0, true}, {0, true, 30 * time.Minute, false}, {10 * time.Minute, true, 20 * time.Minute, false},
		{30 * time.Minute, true, 15 * time.Minute, false}, {24 * time.Hour, true, 0, true},
	} {
		if got, stop := scmPollDelay(c.age, c.healthy); got != c.want || stop != c.stop {
			t.Errorf("age %s healthy %v: %s %v, want %s %v", c.age, c.healthy, got, stop, c.want, c.stop)
		}
	}

	// a task with an open PR and no events: CI pending all day
	fx := newEvFx(t, modeLegacy, "")
	t0 := fx.now()
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	fx.scm.SetChecks("acme/web", evSHA, &scmChecks{SHA: evSHA, State: "pending", Checks: []scmCheck{{ID: "1", Status: "in_progress", Suite: "7"}}, Statuses: []scmStatus{}})
	var reads []time.Duration // the checks row's reads, by the head's age
	for i := 0; i < 200; i++ {
		r := fx.pollRow(1, scmPollChecks)
		if r.Due == 0 {
			break
		}
		fx.setNow(r.Due)
		before := r.Step
		fx.pass()
		if fx.pollRow(1, scmPollChecks).Step == before+1 {
			reads = append(reads, time.Duration(fx.now()-t0)*time.Millisecond)
		}
	}
	var want []time.Duration
	for a := 2 * time.Minute; a <= 24*time.Hour; {
		want = append(want, a)
		d, stop := scmPollDelay(a, false)
		if stop {
			break
		}
		a += d
	}
	if fmt.Sprint(reads) != fmt.Sprint(want) || len(reads) != 64 {
		t.Fatalf("%d reads at %v\nwant %d at %v", len(reads), reads, len(want), want)
	}
	if k := fx.task(1); k.taskPRs()[0].Checks != "pending" {
		t.Fatalf("the PR's checks: %q", k.taskPRs()[0].Checks)
	}
	lost := fx.events(pevNote)
	if len(lost) != 1 || !lost[0].Wake || !strings.Contains(pevText(lost[0]), "lost track of CI") {
		t.Fatalf("the note after 24 h: %+v", lost)
	}
	if n := len(fx.scm.Requests("POST /poll")); n < 64 {
		t.Fatalf("%d POST /scm/poll for 64 reads", n)
	}

	t.Run("healthy events", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.api.healthy.Store(true)
		t0 := fx.now()
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.scm.SetChecks("acme/web", evSHA, &scmChecks{SHA: evSHA, State: "pending", Checks: []scmCheck{{ID: "1", Status: "in_progress"}}, Statuses: []scmStatus{}})
		fx.advance(2 * time.Minute)
		fx.pass()
		if r := fx.pollRow(1, scmPollChecks); r.Step != 0 || r.Due != t0+(30*time.Minute).Milliseconds() {
			t.Fatalf("with healthy events the first read is at 30 min: step %d, due +%s", r.Step, time.Duration(r.Due-t0)*time.Millisecond)
		}
		fx.setNow(t0 + (30 * time.Minute).Milliseconds())
		fx.pass()
		if r := fx.pollRow(1, scmPollChecks); r.Step != 1 || r.Due != fx.now()+(15*time.Minute).Milliseconds() {
			t.Fatalf("the safety read: step %d, next in %s", r.Step, time.Duration(r.Due-fx.now())*time.Millisecond)
		}
	})
	t.Run("a delivery for the repo makes it healthy", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		t0 := fx.now()
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.mustTake(fx.ev(scmKindWorkflow, "in_progress"))
		fx.advance(2 * time.Minute)
		fx.pass()
		if r := fx.pollRow(1, scmPollChecks); r.Step != 0 || r.Due != t0+(30*time.Minute).Milliseconds() {
			t.Fatalf("after a delivery: step %d, due +%s", r.Step, time.Duration(r.Due-t0)*time.Millisecond)
		}
	})
	t.Run("no CI here", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.scm.SetChecks("acme/web", evSHA, &scmChecks{SHA: evSHA, State: "none", Checks: []scmCheck{}, Statuses: []scmStatus{}})
		for i := 0; i < 50 && fx.pollRow(1, scmPollChecks).Due > 0; i++ {
			fx.setNow(fx.pollRow(1, scmPollChecks).Due)
			fx.pass()
		}
		if r := fx.pollRow(1, scmPollChecks); r.Due != 0 || len(fx.events(pevNote)) != 0 {
			t.Fatalf("a repo without CI: due %d, notes %+v", r.Due, fx.events(pevNote))
		}
	})
}

// A webhook and a poll describing the same fact act once, whichever comes
// first: a failing suite one input, a merge one event.
func TestPollWebhookSemanticDedupe(t *testing.T) {
	t.Run("poll first", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		failingChecks(fx, evSHA, "77", "88001")
		fx.advance(2 * time.Minute)
		fx.pass() // the poll reads the failure
		if len(fx.inputs()) != 1 {
			t.Fatalf("the poll's read: %+v", fx.inputs())
		}
		// the provider's answer changes (another check passed), the event comes late
		c := failingChecksValue(fx, evSHA)
		c.Checks = append(c.Checks, scmCheck{ID: "5", Name: "lint", Status: "completed", Conclusion: "success", Suite: "78"})
		fx.scm.SetChecks("acme/web", evSHA, c)
		fx.mustTake(fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" }))
		fx.advance(time.Minute)
		fx.pass()
		if in, evs := fx.inputs(), fx.events(pevCIFailed); len(in) != 1 || len(evs) != 1 || scmFixesToday(fx.task(1)) != 1 {
			t.Fatalf("after the event: %d inputs, %d ci.failed, %d fixes", len(in), len(evs), scmFixesToday(fx.task(1)))
		}
		// a second suite failing on the same head is a new fact
		c.Checks = append(c.Checks, scmCheck{ID: "6", Name: "e2e", Status: "completed", Conclusion: "failure", Suite: "79"})
		fx.scm.SetChecks("acme/web", evSHA, c)
		fx.mustTake(fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" }))
		fx.advance(time.Minute)
		fx.pass()
		if in := fx.inputs(); len(in) != 2 || !strings.Contains(in[1].Text, "e2e") || strings.Contains(in[1].Text, "TestLogin") {
			t.Fatalf("the second suite: %+v", in)
		}
	})
	t.Run("webhook first", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		failingChecks(fx, evSHA, "77", "88001")
		fx.mustTake(fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" }))
		fx.advance(time.Minute)
		fx.pass()
		c := failingChecksValue(fx, evSHA)
		c.Statuses = []scmStatus{{Context: "ci/other", State: "success"}}
		fx.scm.SetChecks("acme/web", evSHA, c) // a new answer: the poll reads it
		_, _ = fx.ag.db.q.Exec(`UPDATE scm_poll SET due_ms=? WHERE kind='checks'`, fx.now())
		fx.pass()
		if in := fx.inputs(); len(in) != 1 {
			t.Fatalf("the poll after the event: %+v", in)
		}
	})
	t.Run("a merge", func(t *testing.T) {
		fx := newEvFx(t, modeLegacy, "")
		fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
		fx.scm.AddPull("acme/web", &scmPull{Number: 42, State: "merged", Head: scmRef{Ref: evBranch, SHA: evSHA}})
		fx.advance(2 * time.Minute)
		fx.pass() // the poll sees it merged
		fx.mustTake(fx.ev(scmKindPull, "merged"))
		if k := fx.task(1); k.Phase != phaseMerged || len(fx.events(pevMerged)) != 1 || len(fx.jobs(pjCleanup)) != 1 {
			t.Fatalf("phase %s, merged events %d", k.Phase, len(fx.events(pevMerged)))
		}
	})
}

// failingChecksValue is the answer failingChecks set.
func failingChecksValue(fx *evFx, sha string) *scmChecks {
	failingChecks(fx, sha, "77", "88001")
	fx.scm.mu.Lock()
	defer fx.scm.mu.Unlock()
	c := *fx.scm.checks["acme/web@"+sha]
	return &c
}

// A person's partition at rest comes back for its next read (userWake).
func TestPollDueInUserWake(t *testing.T) {
	fx := newEvFx(t, modeUser, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	fx.pass() // the subscription posted: what is left is the read
	at := time.UnixMilli(fx.now())
	due := fx.pollRow(1, scmPollChecks).Due
	if w := fx.ag.db.userWake(at); w.runnable || w.wake != due/1000 {
		t.Fatalf("userWake %+v, want a wake at %d", w, due/1000)
	}
	fx.setNow(due)
	if w := fx.ag.db.userWake(time.UnixMilli(due)); !w.runnable {
		t.Fatalf("userWake at the read's time: %+v", w)
	}
	// an event's read sooner moves it
	fx.setNow(due - (2 * time.Minute).Milliseconds() + 1000)
	fx.mustTakePartition(fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" }))
	if w := fx.ag.db.userWake(time.UnixMilli(fx.now())); w.wake != (fx.now()+60_000)/1000 && !w.runnable {
		t.Fatalf("after the event: %+v", w)
	}
	// nothing to read: back only to post the subscription again (25 days)
	_, _ = fx.ag.db.q.Exec(`UPDATE scm_poll SET due_ms=0`)
	var repost int64
	_ = fx.ag.db.q.QueryRow(`SELECT next_ms FROM scm_subs WHERE state='live'`).Scan(&repost)
	if w := fx.ag.db.userWake(time.UnixMilli(fx.now())); w.runnable || repost < fx.now()+(24*24*time.Hour).Milliseconds() || w.wake != repost/1000 {
		t.Fatalf("with no read due: %+v (the subscription's next post at %d)", w, repost/1000)
	}
}

// An unpartitioned agent with nothing else to do leaves the way back for its
// next read: the `wake` job at its minute, `resume` once it is due — the
// idle reap must not end polling where no event comes.
func TestPollDueLeavesWakeUnpartitioned(t *testing.T) {
	var mu sync.Mutex
	var jobs []map[string]any
	fakeGateway(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "PUT" && r.URL.Path == "/api/xbin/cron/jobs" {
			var j map[string]any
			_ = json.NewDecoder(r.Body).Decode(&j)
			jobs = append(jobs, j)
		}
		w.WriteHeader(200)
	}))
	take := func() []map[string]any {
		mu.Lock()
		defer mu.Unlock()
		out := jobs
		jobs = nil
		return out
	}
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	fx := newEvFx(t, modeLegacy, "")
	k := fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	_, _ = fx.ag.db.q.Exec(`UPDATE runs SET status='waiting_input' WHERE id=?`, k.RunID)
	_, _ = fx.ag.db.q.Exec(`DELETE FROM project_jobs`)
	fx.pass()
	if fx.ag.db.hasWork() {
		t.Fatal("the fixture has other work")
	}
	ag := &Agent{db: fx.ag.db}
	due := fx.pollRow(1, scmPollChecks).Due
	ag.leaveWakeUp(fx.ag.db)
	if j := take(); len(j) != 1 || j[0]["name"] != "wake" || j[0]["schedule"] != wakeSchedule(due/1000) {
		t.Fatalf("with a read due at %d: %v", due/1000, j)
	}
	fx.setNow(due)
	ag.leaveWakeUp(fx.ag.db)
	if j := take(); len(j) != 1 || j[0]["name"] != "resume" {
		t.Fatalf("with a read due now: %v", j)
	}
	// nothing to read and no subscription: nothing left behind
	_, _ = fx.ag.db.q.Exec(`UPDATE scm_poll SET due_ms=0`)
	_, _ = fx.ag.db.q.Exec(`DELETE FROM scm_subs`)
	ag.leaveWakeUp(fx.ag.db)
	if j := take(); len(j) != 0 {
		t.Fatalf("with nothing due: %v", j)
	}
}

// mustTakePartition takes e in this person's partition as global's mail
// would hand it.
func (fx *evFx) mustTakePartition(e scmEvent) {
	fx.t.Helper()
	e.For, e.ForPid, e.SCM = "user:alice", alicePID, scmEventSource{Provider: "apps/scm-github", Host: "github.com"}
	if err := fx.ag.db.Tx(func(t *DB) error {
		if !scmSeenOnce(t, e.EventID) {
			return nil
		}
		return scmTake(t, &e)
	}); err != nil {
		fx.t.Fatal(err)
	}
}
