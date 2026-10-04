package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// deliverCI runs scmEventHooks for ev, as E's intake does (in its
// transaction).
func (fx *ciFix) deliverCI(t *testing.T, ev scmEvent) {
	t.Helper()
	if ev.SCM.Provider == "" {
		ev.SCM = scmEventSource{Provider: "apps/scm-github", Host: "github.com"}
	}
	if ev.Repo == "" {
		ev.Repo = "acme/web"
	}
	if err := fx.ag.db.Tx(func(t *DB) error {
		for _, h := range scmEventHooks {
			h(t, &ev)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func ciData(kind string, v any) map[string]json.RawMessage {
	b, _ := json.Marshal(v)
	return map[string]json.RawMessage{kind: b}
}

// Progress events patch the stored snapshot in place — a job (its steps as
// the host last reported them), a run (its jobs kept), a check — redacted
// as a read is, the summary's current step dropped until the next read, a
// `ci` event to the conversation; another head's progress is read instead;
// a push moves the head (and reads), a closed pull request or a deleted
// branch makes the watch gone; another repo's events touch nothing.
func TestCIProgressEventsPatchSnapshot(t *testing.T) {
	fx := newCIFix(t)
	root := fx.conv(t, "alice", true)
	fx.scm.SetChecks("acme/web", "feature", ciChecks(ciSHA1))
	w := fx.watchRow(t, root, "feature", ciSHA1, nil)
	fx.read(t, w.ID)
	sub, _, _ := fx.ag.eng.hub.subscribe(root, -1, who{kind: whoSystem})
	defer fx.ag.eng.hub.unsubscribe(sub)
	reads := ciReadCount.Load()

	tok := "ghs_" + strings.Repeat("B", 36)
	done := scmJob{ID: "88001", Name: "test (ubuntu)", Status: "completed", Conclusion: "failure", URL: "https://github.com/acme/web/actions/runs/7001/job/88001",
		Steps: []scmStep{{N: 1, Name: "Set up job", Status: "completed", Conclusion: "success"}, {N: 4, Name: "go test " + tok, Status: "completed", Conclusion: "failure"}}}
	fx.deliverCI(t, scmEvent{Kind: scmKindJob, Action: "completed", Ref: scmEventRef{Branch: "feature", SHA: ciSHA1},
		Data: ciData(scmKindJob, map[string]any{"runId": "7001", "job": done})})
	x := fx.ag.db.ciWatchByID(w.ID)
	c := x.checks()
	j := c.WorkflowRuns[0].Jobs[1]
	if j.Status != "completed" || j.Conclusion != "failure" || len(j.Steps) != 2 || strings.Contains(x.Snapshot, tok) || !strings.Contains(j.Steps[1].Name, "[redacted]") {
		t.Fatalf("the job patched: %+v (%s)", j, x.Snapshot)
	}
	if x.UpdatedMs <= 0 || x.State != ciFailure {
		t.Fatalf("after the job: %+v", x)
	}
	s := ciSummaryOf([]*ciWatch{x})
	if s.Jobs.Running != 0 || s.Jobs.Failed != 2 || s.Current != "" {
		t.Fatalf("the summary after the job: %+v", s)
	}
	// a run's own progress keeps its jobs
	fx.deliverCI(t, scmEvent{Kind: scmKindWorkflow, Action: "completed", Ref: scmEventRef{Branch: "feature", SHA: ciSHA1},
		Data: ciData(scmKindWorkflow, scmWorkflowRun{ID: "7001", Name: "ci", Status: "completed", Conclusion: "failure", Attempt: 1})})
	c = fx.ag.db.ciWatchByID(w.ID).checks()
	if c.WorkflowRuns[0].Status != "completed" || len(c.WorkflowRuns[0].Jobs) != 3 {
		t.Fatalf("the run patched: %+v", c.WorkflowRuns[0])
	}
	// a check of its own, new
	fx.deliverCI(t, scmEvent{Kind: scmKindCheck, Action: "created", Ref: scmEventRef{SHA: ciSHA1},
		Data: ciData(scmKindCheck, scmCheck{ID: "88200", Name: "docs", Status: "queued", DetailsURL: "javascript:x"})})
	c = fx.ag.db.ciWatchByID(w.ID).checks()
	if len(c.Checks) != 4 || c.Checks[3].ID != "88200" || c.Checks[3].DetailsURL != "" {
		t.Fatalf("the check added: %+v", c.Checks)
	}
	if ciReadCount.Load() != reads {
		t.Fatal("a progress event of the head read the provider")
	}
	ciWait(t, "the ci event", func() bool {
		for _, ev := range sub.drain() {
			if ev.Type == evCI && ev.Root == root {
				d, _ := ev.Data.(map[string]any)
				return d["watch"] == w.ID && d["state"] == ciFailure
			}
		}
		return false
	})
	// another head's progress: read, not patched
	fx.scm.SetChecks("acme/web", "feature", ciChecks(ciSHA2))
	fx.deliverCI(t, scmEvent{Kind: scmKindJob, Action: "queued", Ref: scmEventRef{Branch: "feature", SHA: ciSHA2},
		Data: ciData(scmKindJob, map[string]any{"runId": "9", "job": scmJob{ID: "1", Name: "x", Status: "queued"}})})
	ciWait(t, "the read of the new head", func() bool { return fx.ag.db.ciWatchByID(w.ID).SHA == ciSHA2 })
	if c := fx.ag.db.ciWatchByID(w.ID).checks(); c.WorkflowRuns[0].Jobs[1].Status != "in_progress" {
		t.Fatal("the new head's snapshot isn't the read's")
	}
	// a push moves the head: the snapshot starts over, then is read
	third := strings.Repeat("c", 40)
	fx.scm.SetChecks("acme/web", "feature", &scmChecks{SHA: third, Checks: []scmCheck{}, Statuses: []scmStatus{}})
	fx.deliverCI(t, scmEvent{Kind: scmKindPush, Action: "pushed", Ref: scmEventRef{Branch: "feature", SHA: third},
		Data: ciData(scmKindPush, map[string]any{"before": ciSHA2, "after": third})})
	if x := fx.ag.db.ciWatchByID(w.ID); x.SHA != third || x.State != ciNone {
		t.Fatalf("after the push: %s %s", x.SHA, x.State)
	}
	ciWait(t, "the pushed head read", func() bool { return fx.ag.db.ciWatchByID(w.ID).FetchedMs > 0 })
	ciWait(t, "its subscription posted again (it lapses 30 days after)", func() bool {
		for _, s := range fx.scm.Subscriptions() {
			if s.Key == w.SubKey {
				return true
			}
		}
		return false
	})
	// another repo's events: nothing
	before := fx.ag.db.ciWatchByID(w.ID)
	fx.deliverCI(t, scmEvent{Repo: "acme/other", Kind: scmKindPush, Ref: scmEventRef{Branch: "feature"}, Data: ciData(scmKindPush, map[string]any{"after": ciSHA1})})
	if x := fx.ag.db.ciWatchByID(w.ID); x.SHA != before.SHA {
		t.Fatal("another repo's push moved the watch")
	}
	// its pull request closed: gone (it ends a day later)
	fx.deliverCI(t, scmEvent{Kind: scmKindPull, Action: "closed", Ref: scmEventRef{Branch: "feature", PR: 9},
		Data: ciData(scmKindPull, map[string]any{"number": 9, "state": "closed", "head": map[string]string{"ref": "feature"}})})
	if x := fx.ag.db.ciWatchByID(w.ID); x.State != ciGone || x.PR != 9 {
		t.Fatalf("after the close: %s pr %d", x.State, x.PR)
	}
	if s := ciSummaryOf(fx.live(root)); s.State != ciNone {
		t.Fatalf("a gone watch counted: %+v", s)
	}
	// a second watch, its branch deleted: gone too
	w2 := fx.watchRow(t, root, "other", ciSHA1, nil)
	fx.deliverCI(t, scmEvent{Kind: scmKindPush, Action: "pushed", Ref: scmEventRef{Branch: "other"},
		Data: ciData(scmKindPush, map[string]any{"before": ciSHA1, "after": strings.Repeat("0", 40)})})
	if x := fx.ag.db.ciWatchByID(w2.ID); x.State != ciGone {
		t.Fatalf("a deleted branch: %s", x.State)
	}
	// a day later the refresher ends both
	old := time.Now().Add(-25 * time.Hour).UnixMilli()
	_, _ = fx.ag.db.q.Exec(`UPDATE ci_watch SET updated_ms=? WHERE root_run=?`, old, root)
	ciPass(t.Context(), fx.ag.db)
	if ws := fx.live(root); len(ws) != 0 {
		t.Fatalf("gone watches a day old: %s", ciDump(ws))
	}
}
