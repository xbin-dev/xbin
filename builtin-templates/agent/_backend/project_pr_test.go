package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// commitIn commits a file in a checkout (as the task's agent would).
func commitWork(t *testing.T, co, name, text string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(co, name), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, co, "add", name)
	gitRun(t, co, "commit", "-q", "-m", "work on "+name)
	return gitRun(t, co, "rev-parse", "HEAD")
}

// pullReqs are the pull requests opened at K's fake provider (their bodies).
func (fx *p2Fix) pullReqs() []scmPullReq {
	var out []scmPullReq
	for _, rq := range fx.scm.prov.Requests("POST /pulls") {
		var b scmPullReq
		if json.Unmarshal([]byte(rq.Body), &b) == nil {
			out = append(out, b)
		}
	}
	return out
}

// turn sends text to task run runID and waits for the turn after (the
// fake model's call count reaching calls) and the worker to go quiet.
func (fx *p2Fix) turn(t *testing.T, pid, runID int64, text string, calls int) {
	t.Helper()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", runID), map[string]any{"text": text}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return r != nil && resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) >= calls
	})
	waitJobsDone(t, fx.projFix, pid)
}

// policy.autoPR: a task that comes to rest with commits its default branch
// lacks has its branch pushed and a pull request opened (a draft, here),
// with a body that says which task it is and nothing of the conversation;
// one that has an open pull request isn't asked again; a cancelled turn
// asks nothing; with autoPR off nothing is opened.
func TestAutoPROnRest(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := readyTask(t, fx.projFix, map[string]any{"policy": map[string]any{"autoPR": "draft"}})
	if n := len(fx.pullReqs()); n != 0 {
		t.Fatalf("a pull request with nothing to push: %d", n)
	}
	co := filepath.Join(k.Dir, "web")
	head := commitWork(t, co, "fix.txt", "fixed\n")
	fx.turn(t, p.ID, runID, "commit done, secret plan: zebra-42", 2)
	reqs := fx.pullReqs()
	if len(reqs) != 1 {
		t.Fatalf("pull requests opened at rest: %+v (jobs %s)", reqs, jobsDump(fx.ag.db, p.ID))
	}
	rq := reqs[0]
	if rq.Repo != "acme/web" || rq.Head != k.Branch || rq.Base != "main" || !rq.Draft || rq.ClientID != fmt.Sprintf("agent:proj:%d:pr:1:web", p.ID) ||
		rq.Title != k.Title || !strings.Contains(rq.Body, "task 1 of the project Web") || strings.Contains(rq.Body, "zebra") {
		t.Fatalf("the pull request asked for: %+v", rq)
	}
	if got := gitRun(t, fx.odir, "rev-parse", "refs/heads/"+k.Branch); got != head {
		t.Fatalf("the branch on the origin: %s, want %s", got, head)
	}
	if got := gitRun(t, fx.odir, "rev-parse", "refs/heads/main"); got == head {
		t.Fatalf("the default branch was pushed")
	}
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	prs := k2.taskPRs()
	if k2.Phase != phasePR || len(prs) != 1 || prs[0].Number != 1 || prs[0].State != "open" || !prs[0].Draft {
		t.Fatalf("the task after its pull request: phase %s prs %+v", k2.Phase, prs)
	}
	var opened int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_events WHERE project_id=? AND kind=?`, p.ID, pevPROpened).Scan(&opened)
	if opened != 1 {
		t.Fatalf("pr.opened events: %d", opened)
	}
	// another rest with more commits: the open pull request isn't asked again
	commitWork(t, co, "more.txt", "more\n")
	fx.turn(t, p.ID, runID, "more", 3)
	if n := len(fx.pullReqs()); n != 1 {
		t.Fatalf("a second pull request for the same branch: %d", n)
	}
	if js := fx.ag.db.jobsWhere(`WHERE project_id=? AND kind=? AND created_ms>?`, p.ID, pjPR, k2.UpdatedMs); len(js) != 0 {
		t.Fatalf("auto-PR asked again with its pull request open: %s", jobsDump(fx.ag.db, p.ID))
	}

	// a turn that ends cancelled asks nothing; nor does autoPR off
	fx2 := newP2Fix(t)
	p2, k3, run3 := readyTask(t, fx2.projFix, nil)
	commitWork(t, filepath.Join(k3.Dir, "web"), "x.txt", "x\n")
	fx2.turn(t, p2.ID, run3, "go", 2)
	if js := fx2.ag.db.jobsWhere(`WHERE project_id=? AND kind=?`, p2.ID, pjPR); len(js) != 0 || len(fx2.pullReqs()) != 0 {
		t.Fatalf("autoPR off opened a pull request: %s", jobsDump(fx2.ag.db, p2.ID))
	}
	pol, _ := json.Marshal(map[string]any{"autoPR": "ready"})
	_, _ = fx2.ag.db.q.Exec(`UPDATE projects SET policy=? WHERE id=?`, string(pol), p2.ID)
	_, _ = fx2.ag.db.q.Exec(`UPDATE runs SET status=? WHERE id=?`, statusCanceled, run3)
	cur, _ := fx2.ag.db.getProject(p2.ID)
	kk, _ := fx2.ag.db.taskByN(p2.ID, 1)
	_ = fx2.ag.db.Tx(func(t2 *DB) error { projectRestedPR(t2, cur, kk); return nil })
	if js := fx2.ag.db.jobsWhere(`WHERE project_id=? AND kind=?`, p2.ID, pjPR); len(js) != 0 {
		t.Fatalf("a cancelled turn asked for a pull request: %s", jobsDump(fx2.ag.db, p2.ID))
	}
}

// The pr job, asked for by hand, twice: one push, one pull request — the
// second run finds it open and only pushes; a repeat of the provider's
// create (its clientId) answers the same one. A task whose branch is its
// repo's default branch is never pushed. The route: participants, a ready
// workspace, POST only.
func TestPRJobIdempotent(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := readyTask(t, fx.projFix, nil)
	co := filepath.Join(k.Dir, "web")
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task/pr", runID), nil); w.Code != 405 {
		t.Fatalf("GET …/task/pr (the UI's probe): %d", w.Code)
	}
	if w := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 404 && w.Code != 403 {
		t.Fatalf("bob opens alice's pull request: %d", w.Code)
	}
	// nothing to push yet: done, nothing opened
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 202 {
		t.Fatalf("POST …/task/pr: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	if n := len(fx.pullReqs()); n != 0 {
		t.Fatalf("a pull request with nothing to push: %d", n)
	}
	head := commitWork(t, co, "fix.txt", "fixed\n")
	for i := range 2 {
		w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{"title": "Fix it", "body": "Please look", "draft": false})
		if w.Code != 202 || !strings.Contains(w.Body.String(), `"kind":"pr"`) {
			t.Fatalf("POST …/task/pr #%d: %d %s", i, w.Code, w.Body)
		}
		waitJobsDone(t, fx.projFix, p.ID)
	}
	reqs := fx.pullReqs()
	if len(reqs) != 1 || reqs[0].Title != "Fix it" || reqs[0].Body != "Please look" || reqs[0].Draft {
		t.Fatalf("pull requests asked for: %+v (jobs %s)", reqs, jobsDump(fx.ag.db, p.ID))
	}
	if got := gitRun(t, fx.odir, "rev-parse", "refs/heads/"+k.Branch); got != head {
		t.Fatalf("the branch on the origin: %s", got)
	}
	// the job run again by itself: still one
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if _, err := fx.ag.db.queueJob(p.ID, k2.ID, "", pjPR, "", 0); err != nil {
		t.Fatal(err)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	k2, _ = fx.ag.db.taskByN(p.ID, 1)
	if n := len(fx.pullReqs()); n != 1 || len(k2.taskPRs()) != 1 || k2.Phase != phasePR {
		t.Fatalf("after the job again: %d asked, prs %+v phase %s", n, k2.taskPRs(), k2.Phase)
	}
	var opened int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_events WHERE project_id=? AND kind=?`, p.ID, pevPROpened).Scan(&opened)
	if opened != 1 {
		t.Fatalf("pr.opened events: %d", opened)
	}
	// the provider's own idempotency: the same clientId's create answers the open one
	pl, err := fx.scm2.PullCreate(t.Context(), reqs[0])
	if err != nil || pl.Number != k2.taskPRs()[0].Number {
		t.Fatalf("the create repeated: %+v %v", pl, err)
	}

	// a task on its repo's default branch: never pushed
	_, _ = fx.ag.db.q.Exec(`UPDATE project_tasks SET branch='main', phase='open', prs='[]' WHERE id=?`, k2.ID)
	gitRun(t, co, "checkout", "-q", "-B", "main")
	commitWork(t, co, "evil.txt", "straight to main\n")
	before := gitRun(t, fx.odir, "rev-parse", "refs/heads/main")
	if _, err := fx.ag.db.queueJob(p.ID, k2.ID, "", pjPR, "", 0); err != nil {
		t.Fatal(err)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	if got := gitRun(t, fx.odir, "rev-parse", "refs/heads/main"); got != before {
		t.Fatalf("the default branch was pushed")
	}
	if js := fx.ag.db.jobsWhere(`WHERE task_id=? AND kind=? ORDER BY id DESC LIMIT 1`, k2.ID, pjPR); len(js) != 1 || js[0].State != pjFailed ||
		!strings.Contains(js[0].Error, "default branch") {
		t.Fatalf("the pr job on the default branch: %s", jobsDump(fx.ag.db, p.ID))
	}
	// a workspace not ready: 409
	_, _ = fx.ag.db.q.Exec(`UPDATE project_tasks SET ws='preparing' WHERE id=?`, k2.ID)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 409 {
		t.Fatalf("a pull request from a workspace being prepared: %d %s", w.Code, w.Body)
	}
}
