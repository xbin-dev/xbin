package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// coordLinkFix is the coordinator's fixture with the project's own login
// known (its credential row).
func coordLinkFix(t *testing.T) (*coordFix, *Run) {
	t.Helper()
	fx := newCoordFix(t)
	if _, err := fx.ag.db.q.Exec(`INSERT INTO project_creds (project_id, sandbox_ref, host, identity, login, written_ms) VALUES (?, '', 'github.com', 'person', ?, ?)`,
		fx.p.ID, ownLogin, nowMs()); err != nil {
		t.Fatal(err)
	}
	return fx, fx.coordinator(t, asAlice, fx.p.ID)
}

// task_link_pr: the own login's PR from the same repo is linked with its
// head (the task pushes there, both branches route); another task's PR is
// refused; another author's or a fork's is tracked only, and the result
// says so. The people's route answers the same.
func TestLinkPRTool(t *testing.T) {
	fx, run := coordLinkFix(t)
	k1, _ := fx.addTask(t, fx.p, 1, "alice")
	fx.addTask(t, fx.p, 2, "alice")
	fx.scm.AddPull("acme/web", &scmPull{Number: 58, State: "open", URL: "https://github.com/acme/web/pull/58",
		Head: scmRef{Ref: adoptBranch, SHA: evSHA, Repo: "acme/web"}, Base: scmRef{Ref: "main"}, Author: scmActor{Login: ownLogin, Association: "OWNER"}})
	fx.scm.AddPull("acme/web", &scmPull{Number: 59, State: "open", Head: scmRef{Ref: "feature-x", SHA: evSHA2, Repo: "acme/web"},
		Author: scmActor{Login: "octo-dev", Association: "MEMBER"}})
	fx.scm.AddPull("acme/web", &scmPull{Number: 60, State: "open", Head: scmRef{Ref: "patch-1", SHA: evSHA3, Repo: "magik6k/web"},
		Author: scmActor{Login: ownLogin}})

	out, err := fx.tool(t, run, "task_link_pr", map[string]any{"task": 1, "repo": "acme/web", "pr": 58})
	if err != nil || !strings.Contains(out, "Linked PR #58 on acme/web to task #1.") || !strings.Contains(out, "pushes to its branch, "+adoptBranch) ||
		!strings.Contains(out, "- PR #58 on acme/web: open") {
		t.Fatalf("task_link_pr #58: %v\n%s", err, out)
	}
	k, _ := fx.ag.db.taskByN(fx.p.ID, 1)
	if prs := k.taskPRs(); k.Phase != phasePR || len(prs) != 1 || prs[0].Head != adoptBranch || taskBranchIn(k, "acme/web") != adoptBranch {
		t.Fatalf("task 1 after the link: phase %s prs %+v", k.Phase, k.taskPRs())
	}
	var routed int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_refs WHERE project_id=? AND n=1 AND kind=? AND value IN (?, ?)`,
		fx.p.ID, refBranch, k1.Branch, adoptBranch).Scan(&routed)
	if routed != 2 {
		t.Fatalf("task 1's branches routed: %d", routed)
	}
	if evs := prEvents(t, fx.ag.db, fx.p.ID, pevPROpened, "linked PR #58 on acme/web: the task pushes to "+adoptBranch); len(evs) != 1 {
		t.Fatalf("the link's event: %+v", fx.ag.db.projectEvents(fx.p.ID, 0, 50))
	}
	// held by task 1: refused for task 2
	if _, err := fx.tool(t, run, "task_link_pr", map[string]any{"task": 2, "pr": 58}); err == nil || !strings.Contains(err.Error(), "is task #1's") {
		t.Fatalf("another task's PR: %v", err)
	}
	// another author's: tracked only
	out, err = fx.tool(t, run, "task_link_pr", map[string]any{"task": 2, "pr": 59})
	if err != nil || !strings.Contains(out, "only tracks it") || !strings.Contains(out, "octo-dev opened it") {
		t.Fatalf("task_link_pr #59: %v\n%s", err, out)
	}
	if k, _ := fx.ag.db.taskByN(fx.p.ID, 2); len(k.taskPRs()) != 1 || k.taskPRs()[0].Head != "" || taskBranchIn(k, "acme/web") != k.Branch {
		t.Fatalf("task 2 tracks #59: %+v", k.taskPRs())
	}
	// the people's route: a fork's, tracked only; another task's, 409
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/2/link-pr", fx.p.ID), map[string]any{"pr": 60})
	var got struct {
		PR     TaskPR `json:"pr"`
		Pushes bool   `json:"pushes"`
		Why    string `json:"why"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Pushes || got.PR.Number != 60 || !strings.Contains(got.Why, "fork") {
		t.Fatalf("POST …/link-pr #60: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/2/link-pr", fx.p.ID), map[string]any{"pr": 58}); w.Code != 409 {
		t.Fatalf("POST …/link-pr another task's: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/2/link-pr", fx.p.ID), map[string]any{"pr": 999}); w.Code != 404 {
		t.Fatalf("POST …/link-pr of no such PR: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/projects/%d/tasks/2/link-pr", fx.p.ID), map[string]any{"pr": 60}); w.Code != 404 && w.Code != 403 {
		t.Fatalf("POST …/link-pr by someone outside the project: %d", w.Code)
	}
}

// task_refresh reads a task's PRs now: one merged with no webhook ends the
// task as the event would (phase merged, a merged event); asked again,
// nothing more happens. Without a task it covers every one with PRs.
func TestTaskRefreshAppliesMerge(t *testing.T) {
	fx, run := coordLinkFix(t)
	k1, _ := fx.addTask(t, fx.p, 1, "alice")
	k2, _ := fx.addTask(t, fx.p, 2, "alice")
	fx.addTask(t, fx.p, 3, "alice") // no PR
	for _, c := range []struct {
		k  *ProjectTask
		pr TaskPR
	}{{k1, TaskPR{Repo: "acme/web", Number: 57, State: "open", HeadSHA: evSHA, Checks: "pending"}},
		{k2, TaskPR{Repo: "acme/web", Number: 58, State: "open", HeadSHA: evSHA2, Checks: "none"}}} {
		b, _ := json.Marshal([]TaskPR{c.pr})
		_ = fx.ag.db.setTask(c.k.ID, map[string]any{"prs": string(b), "phase": phasePR})
	}
	fx.scm.AddPull("acme/web", &scmPull{Number: 57, State: "merged", Head: scmRef{Ref: k1.Branch, SHA: evSHA, Repo: "acme/web"}})
	fx.scm.AddPull("acme/web", &scmPull{Number: 58, State: "open", Draft: true, Head: scmRef{Ref: k2.Branch, SHA: evSHA3, Repo: "acme/web"}})

	out, err := fx.tool(t, run, "task_refresh", map[string]any{"task": 1})
	if err != nil || !strings.Contains(out, "#1 — merged") || !strings.Contains(out, "- PR #57 on acme/web: merged") {
		t.Fatalf("task_refresh 1: %v\n%s", err, out)
	}
	if k, _ := fx.ag.db.taskByN(fx.p.ID, 1); k.Phase != phaseMerged || k.taskPRs()[0].State != "merged" {
		t.Fatalf("task 1: %s %+v", k.Phase, k.taskPRs())
	}
	if evs := fx.ag.db.projectEvents(fx.p.ID, 0, 100); len(prEvents(t, fx.ag.db, fx.p.ID, pevMerged, "PR #57")) != 1 {
		t.Fatalf("the merged event: %+v", evs)
	}
	if _, err := fx.tool(t, run, "task_refresh", map[string]any{"task": 1}); err != nil || len(prEvents(t, fx.ag.db, fx.p.ID, pevMerged, "PR #57")) != 1 {
		t.Fatalf("refreshed again: %v", err)
	}
	// every open task with PRs: task 2's head and draft flag recorded; task 3 has none
	out, err = fx.tool(t, run, "task_refresh", map[string]any{})
	if err != nil || !strings.Contains(out, "- PR #58 on acme/web: open, draft, checks none · head "+sha7(evSHA3)) || strings.Contains(out, "#3") ||
		strings.Contains(out, "#1 ") {
		t.Fatalf("task_refresh: %v\n%s", err, out)
	}
	if pr := fx.ag.db.mustTaskN(t, fx.p.ID, 2).taskPRs()[0]; pr.HeadSHA != evSHA3 || !pr.Draft {
		t.Fatalf("task 2's PR: %+v", pr)
	}
}

// mustTaskN is task n of pid.
func (d *DB) mustTaskN(t *testing.T, pid, n int64) *ProjectTask {
	t.Helper()
	k, err := d.taskByN(pid, n)
	if err != nil {
		t.Fatal(err)
	}
	return k
}
