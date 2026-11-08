package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// The fixtures look like the real case (2026-11): PR #58's head, which the
// coding agent opened itself with gh from a branch not its task's, and the
// project acting as its owner's own account.
const (
	adoptBranch = "xbin/bchzwo/review-agent-template-series-17"
	ownLogin    = "magik6k"
)

// prEvents are project pid's events of kind whose text holds want.
func prEvents(t *testing.T, d *DB, pid int64, kind, want string) []ProjectEvent {
	t.Helper()
	var out []ProjectEvent
	for _, ev := range d.projectEvents(pid, 0, 500) {
		if ev.Kind == kind && strings.Contains(pevText(ev), want) {
			out = append(out, ev)
		}
	}
	return out
}

// ownTask is a ready task of a project whose credential's login is
// ownLogin (as the project acting as its owner's own account records it;
// a person's sign-in goes only into their own space, which this fixture
// isn't, so the row says so).
func ownTask(t *testing.T, fx *p2Fix) (ProjectView, *ProjectTask, int64) {
	t.Helper()
	p, k, runID := readyTask(t, fx.projFix, nil)
	if _, err := fx.ag.db.q.Exec(`UPDATE project_creds SET login=? WHERE project_id=?`, ownLogin, p.ID); err != nil {
		t.Fatal(err)
	}
	cur, _ := fx.ag.db.getProject(p.ID)
	if own := scmOwnLogin(fx.ag.db, cur, k); own != ownLogin {
		t.Fatalf("the project's own login: %q", own)
	}
	return p, k, runID
}

// The coding agent opened its PR itself from another branch, as the
// project's own login, and kept committing after: the pr job finds its
// checkout off the task's branch, adopts the PR (its head recorded, the
// task in review, an event saying so), routes both branches — and the next
// pr job pushes the adopted branch, opening nothing.
func TestPROffBranchAdopted(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := ownTask(t, fx)
	co := filepath.Join(k.Dir, "web")
	gitRun(t, co, "checkout", "-q", "-b", adoptBranch)
	prHead := commitWork(t, co, "review.txt", "reviewed\n")
	fx.scm.prov.AddPull("acme/web", &scmPull{Number: 58, URL: "https://github.com/acme/web/pull/58", Title: "Review the agent template",
		State: "open", Head: scmRef{Ref: adoptBranch, SHA: prHead, Repo: "acme/web"}, Base: scmRef{Ref: "main"},
		Author: scmActor{Login: ownLogin, Association: "OWNER"}})
	head := commitWork(t, co, "more.txt", "more\n") // HEAD past the PR's head: the ancestry check
	ask := func() {
		t.Helper()
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 202 {
			t.Fatalf("POST …/task/pr: %d %s", w.Code, w.Body)
		}
		waitJobsDone(t, fx.projFix, p.ID)
	}
	ask()
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	prs := k2.taskPRs()
	if k2.Phase != phasePR || len(prs) != 1 || prs[0].Number != 58 || prs[0].Head != adoptBranch || prs[0].HeadSHA != prHead ||
		prs[0].State != "open" || prs[0].URL != "https://github.com/acme/web/pull/58" {
		t.Fatalf("after the pr job: phase %s, prs %+v (jobs %s)", k2.Phase, prs, jobsDump(fx.ag.db, p.ID))
	}
	if n := len(fx.pullReqs()); n != 0 {
		t.Fatalf("%d pull request(s) opened besides the agent's", n)
	}
	if evs := prEvents(t, fx.ag.db, p.ID, pevPROpened, "linked PR #58 on acme/web: the coding agent opened it from "+adoptBranch); len(evs) != 1 {
		t.Fatalf("the adoption's event: %+v", fx.ag.db.projectEvents(p.ID, 0, 100))
	}
	if got := taskBranchIn(k2, "acme/web"); got != adoptBranch {
		t.Fatalf("the task's branch in acme/web: %s", got)
	}
	var routed int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_refs WHERE project_id=? AND n=1 AND kind=? AND value IN (?, ?)`,
		p.ID, refBranch, k.Branch, adoptBranch).Scan(&routed)
	if routed != 2 {
		t.Fatalf("the branches routed to the task: %d, want both", routed)
	}
	// the next pr job pushes the adopted branch — the task's here now — and opens nothing
	ask()
	if got := gitRun(t, fx.odir, "rev-parse", "refs/heads/"+adoptBranch); got != head {
		t.Fatalf("the adopted branch on the origin: %s, want %s", got, head)
	}
	if n := len(fx.pullReqs()); n != 0 {
		t.Fatalf("a pull request opened for an adopted branch: %d", n)
	}
	if evs := prEvents(t, fx.ag.db, p.ID, pevPROpened, "linked PR #58"); len(evs) != 1 {
		t.Fatalf("the adoption said again: %d", len(evs))
	}
}

// A checkout off the task's branch whose PR fails a rule is never adopted,
// and a waking note names the branch, the reason and task_link_pr — once
// per branch and reason: another author's PR, one from a fork, one whose
// head isn't in the checkout's history.
func TestPROffBranchForeignNotAdopted(t *testing.T) {
	fx := newP2Fix(t)
	p, k, runID := ownTask(t, fx)
	co := filepath.Join(k.Dir, "web")
	ask := func() {
		t.Helper()
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/pr", runID), map[string]any{}); w.Code != 202 {
			t.Fatalf("POST …/task/pr: %d %s", w.Code, w.Body)
		}
		waitJobsDone(t, fx.projFix, p.ID)
	}
	for i, c := range []struct {
		branch, why string
		pull        func(head string) *scmPull
	}{
		{"xbin/bchzwo/someone-elses", "octo-dev opened it, not the project's own login", func(head string) *scmPull {
			return &scmPull{Number: 61, State: "open", Head: scmRef{Ref: "xbin/bchzwo/someone-elses", SHA: head, Repo: "acme/web"},
				Author: scmActor{Login: "octo-dev", Association: "MEMBER"}}
		}},
		{"xbin/bchzwo/from-a-fork", "it comes from the fork magik6k/web", func(head string) *scmPull {
			return &scmPull{Number: 62, State: "open", Head: scmRef{Ref: "xbin/bchzwo/from-a-fork", SHA: head, Repo: "magik6k/web"},
				Author: scmActor{Login: ownLogin}}
		}},
		{"xbin/bchzwo/elsewhere", "isn't in the checkout's history", func(string) *scmPull {
			return &scmPull{Number: 63, State: "open", Head: scmRef{Ref: "xbin/bchzwo/elsewhere", SHA: "1234567890abcdef1234567890abcdef12345678", Repo: "acme/web"},
				Author: scmActor{Login: ownLogin}}
		}},
	} {
		gitRun(t, co, "checkout", "-q", "-b", c.branch)
		fx.scm.prov.AddPull("acme/web", c.pull(commitWork(t, co, fmt.Sprintf("w%d.txt", i), "work\n")))
		ask()
		k2, _ := fx.ag.db.taskByN(p.ID, 1)
		if len(k2.taskPRs()) != 0 || k2.Phase != phaseOpen {
			t.Fatalf("%s: adopted: phase %s prs %+v", c.branch, k2.Phase, k2.taskPRs())
		}
		notes := prEvents(t, fx.ag.db, p.ID, pevNote, "is on "+c.branch+", not the task's branch "+k.Branch)
		if len(notes) != 1 || !notes[0].Wake || !strings.Contains(pevText(notes[0]), c.why) || !strings.Contains(pevText(notes[0]), "task_link_pr") {
			t.Fatalf("%s: the note: %+v", c.branch, notes)
		}
	}
	ask() // asked again: the same refusal isn't said twice
	if notes := prEvents(t, fx.ag.db, p.ID, pevNote, "is on xbin/bchzwo/elsewhere"); len(notes) != 1 {
		t.Fatalf("the refusal repeated: %d", len(notes))
	}
}

// A pull.opened or synchronize webhook, or a push, for an adopted head
// routes to its task — beside the task's own branch, which still routes.
func TestPullOpenedLinkedHead(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	fx.addTask(1, evBranch, evSHA, TaskPR{Number: 58, HeadSHA: evSHA, Head: adoptBranch})
	pull := func(n int, branch, sha string) func(*scmEvent) {
		return func(e *scmEvent) {
			e.Ref = scmEventRef{Branch: branch, SHA: sha, PR: n}
			e.Data = map[string]json.RawMessage{"pull": json.RawMessage(fmt.Sprintf(`{"number":%d,"head":{"ref":%q,"sha":%q}}`, n, branch, sha))}
		}
	}
	// another PR opened from the adopted head (found by its branch)
	fx.mustTake(fx.ev(scmKindPull, "opened", pull(64, adoptBranch, evSHA2)))
	if js := fx.jobs(pjRefs); len(js) != 1 || js[0].ClientID != refsReadPulls {
		t.Fatalf("the refs check for the adopted head: %+v", js)
	}
	// one from a branch that isn't the task's: nothing
	fx.mustTake(fx.ev(scmKindPull, "opened", pull(65, "someone/else", evSHA2)))
	if js := fx.jobs(pjRefs); len(js) != 1 {
		t.Fatalf("a PR from another branch asked for a refs check: %+v", js)
	}
	// synchronize of the adopted PR moves its head
	fx.mustTake(fx.ev(scmKindPull, "synchronize", pull(58, adoptBranch, evSHA2)))
	if pr := fx.task(1).taskPRs()[0]; pr.HeadSHA != evSHA2 || pr.Head != adoptBranch {
		t.Fatalf("after synchronize: %+v", pr)
	}
	// someone else's push to the adopted head moves it and tells the task
	fx.mustTake(fx.ev(scmKindPush, "pushed", func(e *scmEvent) { e.Ref = scmEventRef{Branch: adoptBranch, SHA: evSHA3} }))
	if pr := fx.task(1).taskPRs()[0]; pr.HeadSHA != evSHA3 {
		t.Fatalf("after the push: %+v", pr)
	}
	if in := fx.inputs(); len(in) != 1 || !strings.Contains(in[0].Text, "pushed "+sha7(evSHA3)+" to "+adoptBranch) {
		t.Fatalf("the push's note: %+v", in)
	}
	// a push to the task's own branch (no PR from it) moves no PR's head
	fx.mustTake(fx.ev(scmKindPush, "pushed", func(e *scmEvent) {
		e.Ref = scmEventRef{Branch: evBranch, SHA: evSHA}
		e.Actor = scmActor{Login: evBot}
	}))
	if pr := fx.task(1).taskPRs()[0]; pr.HeadSHA != evSHA3 {
		t.Fatalf("a push to the task's own branch moved the adopted PR's head: %+v", pr)
	}
}

// The brief tells the task to stay on its branch and that the platform
// opens its PR — when its turn ends with auto-PR, when a person asks
// without — never to run gh pr create or switch branches; a linked head
// is named on its repo's line; the words are the same from turn to turn.
func TestTaskBriefStaysOnBranch(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	k := fx.addTask(1, evBranch, evSHA)
	brief := projectPrompt(fx.ag.db, fx.p, k, false)
	for _, want := range []string{"Branch: " + evBranch + " — stay on it", "Never run `gh pr create`", "never switch to or create another branch",
		"opens its pull request from it when a person asks"} {
		if !strings.Contains(brief, want) {
			t.Fatalf("the brief says no %q:\n%s", want, brief)
		}
	}
	if again := projectPrompt(fx.ag.db, fx.p, k, false); again != brief {
		t.Fatalf("the brief changed between turns:\n%s\n---\n%s", brief, again)
	}
	fx.p.Policy = json.RawMessage(`{"autoPR":"draft"}`)
	if b := projectPrompt(fx.ag.db, fx.p, k, false); !strings.Contains(b, "When your turn ends the platform opens its pull request from it.") {
		t.Fatalf("with auto-PR:\n%s", b)
	}
	k = fx.addTask(2, "xbin/k3x9qa/2-other", evSHA2, TaskPR{Number: 58, HeadSHA: evSHA2, Head: adoptBranch})
	if b := projectPrompt(fx.ag.db, fx.p, k, false); !strings.Contains(b, "the task's branch here: "+adoptBranch) {
		t.Fatalf("a linked head isn't named:\n%s", b)
	}
}

// Adopting never ends a task: a merged PR handed to prAdoptApply (only an
// open one ever is) is a note, the task open.
func TestPROffBranchMergedNeverEndsTask(t *testing.T) {
	fx := newEvFx(t, modeLegacy, "")
	k := fx.addTask(1, "xbin/bchzwo/7-fix-55-tile-agent-can-read-globa", "")
	r, _ := fx.ag.db.repoNamed(fx.p.ID, "acme/web")
	merged := &scmPull{Number: 58, State: "merged", URL: "https://github.com/acme/web/pull/58",
		Head: scmRef{Ref: adoptBranch, SHA: evSHA, Repo: "acme/web"}, Author: scmActor{Login: evBot}}
	adopt := func() int {
		n := 0
		if err := fx.ag.db.Tx(func(t *DB) error {
			cur, err := t.taskByN(fx.p.ID, 1)
			if err != nil {
				return err
			}
			n, err = prAdoptApply(t, fx.p, cur, []prAdopt{{prOff: prOff{repo: r, branch: adoptBranch, head: evSHA}, pull: merged}})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := adopt(); n != 0 {
		t.Fatalf("adopted %d", n)
	}
	if k = fx.task(1); k.Phase != phaseOpen || len(k.taskPRs()) != 0 || len(fx.events(pevMerged)) != 0 || len(fx.events(pevPROpened)) != 0 {
		t.Fatalf("after a merged PR: phase %s prs %+v events %+v", k.Phase, k.taskPRs(), fx.events(""))
	}
	if notes := fx.events(pevNote); len(notes) != 1 || !strings.Contains(pevText(notes[0]), "PR #58 is merged") {
		t.Fatalf("the note: %+v", notes)
	}
}
