package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// A task whose pull request passed CI waits for a person to review and
// merge it: it is in Needs you (reason review) and counts toward the
// project's need-you (counts.ready); failing or pending CI, a draft that
// isn't open any more, or someone who may not act in it, isn't.
func TestCIPassedNeedsReview(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "fix the header"})
	k := fx.waitWS(t, p.ID, 1, wsReady)
	// its first turn taken and over (a new task's run rests before the engine starts it)
	hwait(t, "the task's turn", func() bool {
		r, err := fx.ag.db.getRun(runID)
		return err == nil && len(fakeOf(fx.ag).callsFor(runID)) > 0 && resting(r.Status)
	})
	waitJobsDone(t, fx, p.ID) // the turn's end reads its refs and PRs (refs job): settled before the test sets its own
	setPR := func(state, checks string) {
		t.Helper()
		b, _ := json.Marshal([]TaskPR{{Repo: "acme/web", Number: 7, URL: "https://github.com/acme/web/pull/7", State: state, Checks: checks}})
		if err := fx.ag.db.setTask(k.ID, map[string]any{"phase": phasePR, "prs": string(b)}); err != nil {
			t.Fatal(err)
		}
	}
	needs := func(c caller, path string) []map[string]any {
		t.Helper()
		w := callAs(t, fx.mux, c, "GET", path, nil)
		var out struct {
			Items []map[string]any `json:"items"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return out.Items
	}
	review := func(items []map[string]any) bool {
		for _, it := range items {
			if r, _ := it["run"].(map[string]any); r != nil && fmt.Sprint(r["id"]) == fmt.Sprint(runID) {
				return it["reason"] == needReview
			}
		}
		return false
	}
	ready := func() int {
		w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d", p.ID), nil)
		var out struct {
			Project ProjectView `json:"project"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Project.Counts[countReady]
	}

	setPR("open", "pending")
	if review(needs(asAlice, "/needs")) || ready() != 0 {
		t.Fatal("CI still running: not yet")
	}
	setPR("open", "success")
	if !review(needs(asAlice, "/needs")) {
		kk, _ := fx.ag.db.taskByN(p.ID, 1)
		r, _ := fx.ag.db.getRun(runID)
		_, st, wf := taskDerived(kk, r.Status, taskCISummary(kk.RunID), false)
		t.Logf("task phase %s ws %s prs %s; run %s; derived %s/%s; ci %+v; jobs %s", kk.Phase, kk.WS, fmt.Sprint(kk.taskPRs()), r.Status, st, wf, taskCISummary(kk.RunID), jobsDump(fx.ag.db, p.ID))
		t.Fatalf("CI passed: in Needs you as a review: %v", needs(asAlice, "/needs"))
	}
	if !review(needs(asAlice, fmt.Sprintf("/projects/%d/needs", p.ID))) {
		t.Fatal("…and in the project's own")
	}
	if ready() != 1 {
		t.Fatalf("counts.ready: %d", ready())
	}
	if review(needs(asBob, "/needs")) {
		t.Fatal("bob may not act in alice's task")
	}
	n := 0
	for _, it := range needs(asAlice, "/needs") {
		if r, _ := it["run"].(map[string]any); r != nil && fmt.Sprint(r["id"]) == fmt.Sprint(runID) {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("the task is listed %d times", n)
	}
	setPR("open", "failure")
	if review(needs(asAlice, "/needs")) || ready() != 0 {
		t.Fatal("CI failed: no review")
	}
	setPR("merged", "success")
	if review(needs(asAlice, "/needs")) || ready() != 0 {
		t.Fatal("merged: nothing to review")
	}
}
