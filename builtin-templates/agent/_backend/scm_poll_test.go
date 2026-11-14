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
// track of CI" — while the open PR's pull and comments rows go on every
// 6 h; with healthy events a first read at 30 min, then every 15.
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
	for _, kind := range []string{scmPollPull, scmPollComments} {
		if r := fx.pollRow(1, kind); r == nil || r.Due == 0 || r.Due-r.Last != scmPollBackoff.Milliseconds() {
			t.Fatalf("the open PR's %s row after a day: %+v", kind, r)
		}
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

// An event that asks for a read while one of the same row is in flight
// (the row already due, so the nudge keeps its time) wins over that read's
// result: the row stays due for the read the event asked for.
func TestPollNudgeDuringRead(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA})
	checks := func() scmEvent {
		return fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" })
	}
	fx.mustTake(checks())
	due := fx.pollRow(1, scmPollChecks).Due
	fx.setNow(due + 1000)
	inFlight := fx.pollRow(1, scmPollChecks) // the pass read this row
	fx.mustTake(checks())                    // another suite finished meanwhile
	if r := fx.pollRow(1, scmPollChecks); r.Due != due || !r.Nudge {
		t.Fatalf("the second nudge: due %d (want %d, coalesced), nudge %v", r.Due, due, r.Nudge)
	}
	scmPollAfter(fx.ag.db, inFlight, nil, "failure", true, 0) // the read's final result
	if r := fx.pollRow(1, scmPollChecks); r.Due != due || !r.Nudge {
		t.Fatalf("after the stale read: due %d, nudge %v — the event's read was lost", r.Due, r.Nudge)
	}
	// with no nudge meanwhile the read's result stands
	again := fx.pollRow(1, scmPollChecks)
	scmPollAfter(fx.ag.db, again, nil, "failure", true, 0)
	if r := fx.pollRow(1, scmPollChecks); r.Due != 0 || r.Nudge {
		t.Fatalf("after a read with no nudge meanwhile: due %d, nudge %v", r.Due, r.Nudge)
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

// An open PR's pull row backs off to every 6 h past a day and never stops:
// at 61 h it is still read (the merge then seen, no webhook needed); a
// checks row still gives up at 24 h with its lost note.
func TestPollOpenPRBacksOffNeverStops(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	t0 := fx.now()
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 57, HeadSHA: evSHA})
	open := &scmPull{Number: 57, State: "open", Head: scmRef{Ref: evBranch, SHA: evSHA, Repo: "acme/web"}}
	fx.scm.AddPull("acme/web", open)
	fx.scm.SetChecks("acme/web", evSHA, &scmChecks{SHA: evSHA, State: "pending", Checks: []scmCheck{{ID: "1", Status: "in_progress"}}, Statuses: []scmStatus{}})
	var reads []time.Duration
	for i := 0; i < 300; i++ {
		r := fx.pollRow(1, scmPollPull)
		if r == nil || r.Due == 0 || r.Due > t0+(61*time.Hour).Milliseconds() {
			break
		}
		fx.setNow(r.Due)
		fx.pass()
		reads = append(reads, time.Duration(fx.now()-t0)*time.Millisecond)
	}
	r := fx.pollRow(1, scmPollPull)
	if r == nil || r.Due == 0 {
		t.Fatalf("the open PR's pull row stopped: %+v", r)
	}
	var late []time.Duration
	for i := 1; i < len(reads); i++ {
		if reads[i-1] >= 24*time.Hour {
			late = append(late, reads[i]-reads[i-1])
		}
	}
	if len(late) < 5 {
		t.Fatalf("reads past a day: %v (all %v)", late, reads)
	}
	for _, d := range late {
		if d != 6*time.Hour {
			t.Fatalf("past a day the pull row reads every %v, want 6h (%v)", d, late)
		}
	}
	if c := fx.pollRow(1, scmPollComments); c == nil || c.Due == 0 {
		t.Fatalf("the open PR's comments row stopped: %+v", c)
	}
	if c := fx.pollRow(1, scmPollChecks); c.Due != 0 {
		t.Fatalf("the checks row still reads past a day: due %d", c.Due)
	}
	if lost := fx.events(pevNote); len(lost) != 1 || !lost[0].Wake || !strings.Contains(pevText(lost[0]), "lost track of CI") {
		t.Fatalf("the lost note: %+v", lost)
	}
	// merged at 61 h with no webhook: the next read sees it
	fx.scm.mu.Lock()
	open.State = "merged"
	fx.scm.mu.Unlock()
	fx.setNow(r.Due)
	fx.pass()
	if k := fx.task(1); k.Phase != phaseMerged || k.taskPRs()[0].State != "merged" || len(fx.events(pevMerged)) != 1 {
		t.Fatalf("after the merge: phase %s prs %+v", k.Phase, k.taskPRs())
	}
	if r := fx.pollRow(1, scmPollPull); r != nil {
		t.Fatalf("a merged PR is still read: %+v", r)
	}
}

// What an older build left — an open PR's pull row stopped after a day,
// or none made — is re-tracked when the loop starts (and daily): due again,
// the comments row's since kept; the merge it missed is then seen.
func TestBootRetracksOrphanedPRs(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, "xbin/bchzwo/7-fix-55-tile-agent-can-read-globa", evSHA, TaskPR{Number: 57, HeadSHA: evSHA})
	fx.addTask(2, "xbin/bchzwo/8-other", evSHA2, TaskPR{Number: 60, HeadSHA: evSHA2})
	fx.addTask(3, "xbin/bchzwo/9-quiet", evSHA3, TaskPR{Number: 61, HeadSHA: evSHA3}) // reading: left alone
	d := fx.ag.db
	since := fx.pollRow(1, scmPollComments).Item.Since
	_, _ = d.q.Exec(`UPDATE scm_poll SET due_ms=0 WHERE project_id=? AND n=1 AND kind IN (?, ?)`, fx.p.ID, scmPollPull, scmPollComments)
	_, _ = d.q.Exec(`DELETE FROM scm_poll WHERE project_id=? AND n=2 AND kind IN (?, ?)`, fx.p.ID, scmPollPull, scmPollComments)
	due3 := fx.pollRow(3, scmPollPull).Due
	fx.advance(61 * time.Hour)
	fx.scm.AddPull("acme/web", &scmPull{Number: 57, State: "merged", Head: scmRef{Ref: "xbin/bchzwo/7-fix-55-tile-agent-can-read-globa", SHA: evSHA}})
	fx.scm.AddPull("acme/web", &scmPull{Number: 60, State: "open", Head: scmRef{Ref: "xbin/bchzwo/8-other", SHA: evSHA2}})
	fx.scm.AddPull("acme/web", &scmPull{Number: 61, State: "open", Head: scmRef{Ref: "xbin/bchzwo/9-quiet", SHA: evSHA3}})
	if n := scmRetrack(d); n != 2 {
		t.Fatalf("re-tracked %d task(s), want 2", n)
	}
	r1, r2 := fx.pollRow(1, scmPollPull), fx.pollRow(2, scmPollPull)
	if r1 == nil || r1.Due == 0 || r2 == nil || r2.Due == 0 || r2.Item.Number != 60 {
		t.Fatalf("after re-tracking: task 1 %+v, task 2 %+v", r1, r2)
	}
	if c := fx.pollRow(1, scmPollComments); c.Due == 0 || c.Item.Since != since {
		t.Fatalf("task 1's comments row: due %d since %d (was %d)", c.Due, c.Item.Since, since)
	}
	if r3 := fx.pollRow(3, scmPollPull); r3.Due != due3 {
		t.Fatalf("a reading row was moved: %d → %d", due3, r3.Due)
	}
	if n := scmRetrack(d); n != 0 {
		t.Fatalf("re-tracked again: %d", n)
	}
	fx.setNow(max(r1.Due, r2.Due))
	fx.pass()
	if k := fx.task(1); k.Phase != phaseMerged {
		t.Fatalf("task 1 after the read: %s %+v", k.Phase, k.taskPRs())
	}
	if k := fx.task(2); k.Phase != phasePR {
		t.Fatalf("task 2 after the read: %s", k.Phase)
	}
}
