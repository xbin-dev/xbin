package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// project_pr_guard_test.go — what adopting and linking a task's pull
// requests must never do (the review of D221's D1/D2): end a live task with
// an old PR, clean up work no merged PR holds, give a task a second open PR
// in a repo, or make the default branch or another task's branch its own.

// askPR asks for task runID's pull request and waits for the jobs.
func (fx *p2Fix) askPR(t *testing.T, pid, runID int64) {
	t.Helper()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 202 {
		t.Fatalf("POST …/task/pr: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, pid)
}

// Only an open PR is adopted, and never one whose head the default branch
// already holds: an old merged PR from a branch name the coding agent
// happens to reuse doesn't end the live task (a note names it instead);
// nor is an open PR from a branch with nothing of its own adopted.
func TestAdoptOnlyOpenPRs(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := ownTask(t, fx)
	co := filepath.Join(k.Dir, "web")
	base := gitRun(t, co, "rev-parse", "HEAD") // main's commit: an old PR merged with a merge commit had it as its head
	gitRun(t, co, "checkout", "-q", "-b", "fix-typo")
	commitWork(t, co, "new.txt", "new work, not pushed\n")
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 12, URL: "https://github.com/acme/web/pull/12", Title: "an old fix from months ago",
		State: "merged", Head: scmRef{Ref: "fix-typo", SHA: base, Repo: "acme/web"}, Base: scmRef{Ref: "main"},
		Author: scmActor{Login: ownLogin, Association: "OWNER"}})
	fx.askPR(t, p.ID, runID)
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if k2.Phase != phaseOpen || len(k2.taskPRs()) != 0 || k2.WS != wsReady {
		t.Fatalf("an old merged PR was adopted: phase %s ws %s prs %+v", k2.Phase, k2.WS, k2.taskPRs())
	}
	if notes := prEvents(t, fx.ag.db, p.ID, pevNote, "is on fix-typo"); len(notes) != 1 || !strings.Contains(pevText(notes[0]), "PR #12") ||
		!strings.Contains(pevText(notes[0]), "merged") || !strings.Contains(pevText(notes[0]), "task_link_pr") {
		t.Fatalf("the note about #12: %+v", notes)
	}
	// an open PR whose head main already holds: nothing of the task's in it
	gitRun(t, co, "checkout", "-q", "-b", "xbin/bchzwo/empty", base)
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 13, URL: "https://github.com/acme/web/pull/13", State: "open",
		Head: scmRef{Ref: "xbin/bchzwo/empty", SHA: base, Repo: "acme/web"}, Author: scmActor{Login: ownLogin}})
	fx.askPR(t, p.ID, runID)
	if k2, _ = fx.ag.db.taskByN(p.ID, 1); k2.Phase != phaseOpen || len(k2.taskPRs()) != 0 {
		t.Fatalf("an open PR whose head is on main was adopted: phase %s prs %+v", k2.Phase, k2.taskPRs())
	}
	if notes := prEvents(t, fx.ag.db, p.ID, pevNote, "is on xbin/bchzwo/empty"); len(notes) != 1 || !strings.Contains(pevText(notes[0]), "default branch") {
		t.Fatalf("the note about #13: %+v", notes)
	}
}

// Cleanup counts a merged PR as holding the task's work only when the
// checkout's HEAD is its head or in its history: linking someone else's
// merged PR ends the task, but its unpushed commits keep the checkout
// (blocked: a person decides); the task's own merged PR holding HEAD lets
// cleanup go on.
func TestCleanupKeepsWorkPastMergedPR(t *testing.T) {
	t.Run("an unrelated merged PR", func(t *testing.T) {
		fx := newP2Fix(t)
		p, k, _ := ownTask(t, fx)
		co := filepath.Join(k.Dir, "web")
		commitWork(t, co, "mine.txt", "the task's own work, never pushed\n")
		fx.scm.prov.AddPull("acme/web", &scmPull{Number: 7, URL: "https://github.com/acme/web/pull/7", Title: "someone else's",
			State: "merged", Head: scmRef{Ref: "octo/other", SHA: "1234567890abcdef1234567890abcdef12345678", Repo: "acme/web"},
			Base: scmRef{Ref: "main"}, Author: scmActor{Login: "octo-dev", Association: "MEMBER"}})
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/1/link-pr", p.ID), map[string]any{"pr": 7}); w.Code != 200 {
			t.Fatalf("linking a merged PR (allowed): %d %s", w.Code, w.Body)
		}
		waitJobsDone(t, fx.projFix, p.ID)
		k2, _ := fx.ag.db.taskByN(p.ID, 1)
		if _, err := os.Stat(co); err != nil || k2.Phase != phaseMerged || k2.WS != wsBlocked {
			t.Fatalf("after linking someone else's merged PR: phase %s ws %s, checkout %v (jobs %s)", k2.Phase, k2.WS, err, jobsDump(fx.ag.db, p.ID))
		}
	})
	t.Run("the task's own merged PR", func(t *testing.T) {
		fx := newP2Fix(t)
		p, k, _ := ownTask(t, fx)
		co := filepath.Join(k.Dir, "web")
		head := commitWork(t, co, "mine.txt", "the task's own work\n")
		fx.scm.prov.AddPull("acme/web", &scmPull{Number: 8, URL: "https://github.com/acme/web/pull/8", State: "merged",
			Head: scmRef{Ref: k.Branch, SHA: head, Repo: "acme/web"}, Base: scmRef{Ref: "main"}, Author: scmActor{Login: ownLogin}})
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/1/link-pr", p.ID), map[string]any{"pr": 8}); w.Code != 200 {
			t.Fatalf("linking the task's merged PR: %d %s", w.Code, w.Body)
		}
		waitJobsDone(t, fx.projFix, p.ID)
		if k2, _ := fx.ag.db.taskByN(p.ID, 1); k2.Phase != phaseMerged || k2.WS != wsCleaned {
			t.Fatalf("after its own merged PR: phase %s ws %s (jobs %s)", k2.Phase, k2.WS, jobsDump(fx.ag.db, p.ID))
		}
	})
}

// A task holds at most one open PR per repo (the poll rows are per repo):
// linking a second is refused, naming the open one, and a checkout on
// another branch with an open PR isn't adopted while one is open.
func TestOneOpenPRPerRepo(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := ownTask(t, fx)
	co := filepath.Join(k.Dir, "web")
	commitWork(t, co, "own.txt", "the task's own work\n")
	fx.askPR(t, p.ID, runID) // its own PR, #1
	k1, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k1.taskPRs(); len(prs) != 1 || prs[0].State != "open" {
		t.Fatalf("the task's own PR: %+v", prs)
	}
	own := k1.taskPRs()[0].Number
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 58, URL: "https://github.com/acme/web/pull/58", State: "open",
		Head: scmRef{Ref: "xbin/bchzwo/another", SHA: evSHA, Repo: "acme/web"}, Author: scmActor{Login: ownLogin}})
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/1/link-pr", p.ID), map[string]any{"pr": 58})
	if w.Code != 409 || !strings.Contains(w.Body.String(), fmt.Sprintf("PR #%d", own)) {
		t.Fatalf("a second open PR linked: %d %s", w.Code, w.Body)
	}
	// the coding agent opens another from another branch: not adopted while #own is open
	gitRun(t, co, "checkout", "-q", "-b", adoptBranch)
	head := commitWork(t, co, "more.txt", "more\n")
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 59, URL: "https://github.com/acme/web/pull/59", State: "open",
		Head: scmRef{Ref: adoptBranch, SHA: head, Repo: "acme/web"}, Author: scmActor{Login: ownLogin}})
	fx.askPR(t, p.ID, runID)
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k2.taskPRs(); len(prs) != 1 || prs[0].Number != own {
		t.Fatalf("a second open PR in acme/web: %+v", prs)
	}
	if notes := prEvents(t, fx.ag.db, p.ID, pevNote, "is on "+adoptBranch); len(notes) != 1 || !strings.Contains(pevText(notes[0]), fmt.Sprintf("PR #%d is open", own)) {
		t.Fatalf("the note: %+v", notes)
	}
}

// A linked PR's head may be neither the repo's default branch nor another
// live task's branch: the task would push there, and events for it would
// route to the task.
func TestLinkHeadRefusals(t *testing.T) {
	fx := newP2Fix(t)
	p, _, _ := ownTask(t, fx)
	_, _ = fx.newTask(t, asAlice, p.ID, map[string]any{"text": "another"})
	k2 := fx.waitWS(t, p.ID, 2, wsReady)
	waitJobsDone(t, fx.projFix, p.ID)
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 9, URL: "https://github.com/acme/web/pull/9", Title: "release", State: "open",
		Head: scmRef{Ref: "main", SHA: evSHA, Repo: "acme/web"}, Base: scmRef{Ref: "release"}, Author: scmActor{Login: ownLogin}})
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 10, URL: "https://github.com/acme/web/pull/10", State: "open",
		Head: scmRef{Ref: k2.Branch, SHA: evSHA2, Repo: "acme/web"}, Author: scmActor{Login: ownLogin}})
	for _, c := range []struct {
		pr   int
		want string
	}{{9, "default branch"}, {10, "task #2's branch"}} {
		w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks/1/link-pr", p.ID), map[string]any{"pr": c.pr})
		if w.Code != 409 || !strings.Contains(w.Body.String(), c.want) {
			t.Fatalf("linking #%d: %d %s", c.pr, w.Code, w.Body)
		}
	}
	var routed int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_refs WHERE project_id=? AND n=1 AND kind=? AND value IN ('main', ?)`, p.ID, refBranch, k2.Branch).Scan(&routed)
	if k, _ := fx.ag.db.taskByN(p.ID, 1); len(k.taskPRs()) != 0 || routed != 0 {
		t.Fatalf("task 1 after the refusals: prs %+v, routed %d", k.taskPRs(), routed)
	}
}

// A tracked PR (another author's, a fork's) is followed and shown, but its
// CI failures and review comments never reach the task: information only.
func TestTrackedPRInformationOnly(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 42, HeadSHA: evSHA, Head: "feature-x", Tracked: true})
	failingChecks(fx, evSHA, "77", "88001")
	fx.mustTake(fx.ev(scmKindChecks, "completed", func(e *scmEvent) { e.Conclusion = "failure" }))
	fx.advance(time.Minute)
	fx.pass()
	k := fx.task(1)
	if in, evs := fx.inputs(), fx.events(pevCIFailed); len(in) != 0 || len(evs) != 1 || !strings.Contains(pevText(evs[0]), "tracked PR #42") ||
		k.taskPRs()[0].Checks != "failure" || scmFixesToday(k) != 0 {
		t.Fatalf("a tracked PR's failing CI: inputs %+v, events %+v, checks %q", in, evs, k.taskPRs()[0].Checks)
	}
	fx.scm.AddComment("acme/web", 42, scmComment{ID: "r1", Kind: "review", State: "changes_requested", Body: "rename the helper",
		Author: scmActor{Login: "octo-dev", Association: "MEMBER"}, CreatedAt: fx.now()})
	fx.mustTake(fx.ev(scmKindReview, "submitted"))
	fx.advance(3 * time.Minute)
	fx.pass()
	if in, c := fx.inputs(), fx.events(pevComment); len(in) != 0 || len(c) != 1 || !strings.Contains(pevText(c[0]), "the task only tracks this PR") {
		t.Fatalf("a tracked PR's review: inputs %+v, comment events %+v", in, c)
	}
	// its head is no branch of the task's
	if k := fx.task(1); taskBranchIn(k, "acme/web") != evBranch || taskOnBranch(k, "acme/web", "feature-x") {
		t.Fatalf("a tracked head became the task's: %s", taskBranchIn(k, "acme/web"))
	}
}

// A linked head is the task's branch only while its PR is open and the
// task's to push to.
func TestTaskBranchInOpenPushableOnly(t *testing.T) {
	k := &ProjectTask{Branch: evBranch}
	for _, c := range []struct {
		pr   TaskPR
		want string
	}{
		{TaskPR{Repo: "acme/web", Number: 58, State: "open", Head: adoptBranch}, adoptBranch},
		{TaskPR{Repo: "acme/web", Number: 58, State: "merged", Head: adoptBranch}, evBranch},
		{TaskPR{Repo: "acme/web", Number: 58, State: "closed", Head: adoptBranch}, evBranch},
		{TaskPR{Repo: "acme/web", Number: 59, State: "open", Head: "feature-x", Tracked: true}, evBranch},
		{TaskPR{Repo: "acme/api", Number: 60, State: "open", Head: adoptBranch}, evBranch},
	} {
		b, _ := json.Marshal([]TaskPR{c.pr})
		k.PRs = b
		if got := taskBranchIn(k, "acme/web"); got != c.want {
			t.Errorf("%+v: %s, want %s", c.pr, got, c.want)
		}
	}
}

// The refs job writes what it read onto the task as stored then: a PR that
// ended while it read (an event, a link) stays ended.
func TestRefsJobKeepsConcurrentEnd(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := ownTask(t, fx)
	commitWork(t, filepath.Join(k.Dir, "web"), "own.txt", "the task's own work\n")
	fx.askPR(t, p.ID, runID)
	k1, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k1.taskPRs(); len(prs) != 1 || prs[0].State != "open" {
		t.Fatalf("the task's PR: %+v", prs)
	}
	n := k1.taskPRs()[0].Number
	fx.scm.prov.mu.Lock()
	fx.scm.prov.pull("acme/web", n).Head.SHA = evSHA2 // a head the refs job records
	fx.scm.prov.mu.Unlock()
	var once sync.Once
	end := func() {
		once.Do(func() { // the merge event lands while the refs job holds its read
			_ = fx.ag.db.Tx(func(t *DB) error {
				pp, _ := t.getProject(p.ID)
				cur, err := t.taskByN(p.ID, 1)
				if err == nil {
					scmPullEnded(t, pp, cur, "acme/web", n, true)
				}
				return nil
			})
		})
	}
	p2PullsHook.Store(&end)
	t.Cleanup(func() { p2PullsHook.Store(nil) })
	if err := fx.ag.db.Tx(func(t *DB) error {
		j, err := t.queueJob(p.ID, k1.ID, "", pjRefs, "", 0)
		if err == nil {
			_, err = t.q.Exec(`UPDATE project_jobs SET client_id=? WHERE id=?`, refsReadPulls, j.ID)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	kickProjectWorker()
	waitJobsDone(t, fx.projFix, p.ID)
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k2.taskPRs(); k2.Phase != phaseMerged || len(prs) != 1 || prs[0].State != "merged" {
		t.Fatalf("after the refs job: phase %s prs %+v (jobs %s)", k2.Phase, prs, jobsDump(fx.ag.db, p.ID))
	}
}
