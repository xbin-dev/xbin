package main

// ci_fakes_test.go — the CI view's test fixture: an agent with the fake
// sandbox manager (alice's sandbox, a host directory) and K's fake scm
// provider bound through the real scm client (scm_fake_test.go, extended
// here only through its levers), conversations bound to that sandbox, a
// task made row by row, and a CI snapshot with a run, its jobs and steps, a
// check of its own and a status.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

type ciFix struct {
	ag  *Agent
	mux *http.ServeMux
	scm *fakeSCM
	m   *sbxTestManager
	box *sbxSandbox
}

const ciSHA1 = "9fceb02d0ae598e95dc970b74767f19372d61af8"
const ciSHA2 = "1f2e3d4c5b6a79881726354453627180a9b8c7d6"

func newCIFix(t *testing.T) *ciFix {
	t.Helper()
	ag, mux := accessFixture(t)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Egress: "internet"})
	f := newFakeSCM(t, "apps/scm-github")
	bindSCM(t, f)
	f.AddRepo("acme/web", true)
	oldBot := scmBotAllowed
	scmBotAllowed = func(who, string) bool { return true } // the bot rule has its own test
	t.Cleanup(func() { scmBotAllowed = oldBot })
	return &ciFix{ag: ag, mux: mux, scm: f, m: m, box: box}
}

// conv is a conversation of owner's; bound: to the fixture's sandbox.
func (fx *ciFix) conv(t *testing.T, owner string, bound bool) int64 {
	t.Helper()
	id := runAs(t, fx.ag, runStamp{Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if bound {
		fx.bind(t, id, owner)
	}
	return id
}

func (fx *ciFix) bind(t *testing.T, id int64, by string) {
	t.Helper()
	b, _ := json.Marshal(SandboxBinding{Ref: sandboxRef("apps/cs", fx.box.ID), Cwd: fx.box.Workdir, By: by})
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET config=json_set(CASE WHEN json_valid(config) THEN config ELSE '{}' END, '$.sandbox', json(?)) WHERE id=?`,
		string(b), id); err != nil {
		t.Fatal(err)
	}
}

// ciChecks is CI on sha: a run "ci" with a job done, a job running its
// fourth step and one queued; codecov's check failing (with annotations);
// jenkins's status passing.
func ciChecks(sha string) *scmChecks {
	return &scmChecks{SHA: sha, Ref: "feature", State: "failure",
		WorkflowRuns: []scmWorkflowRun{{ID: "7001", Name: "ci", Event: "push", Status: "in_progress", URL: "https://github.com/acme/web/actions/runs/7001",
			StartedAt: 1789990000000, Attempt: 1, HeadSHA: sha, HeadBranch: "feature", Jobs: []scmJob{
				{ID: "88000", Name: "lint", Status: "completed", Conclusion: "success", URL: "https://github.com/acme/web/actions/runs/7001/job/88000",
					Check: "88000", Steps: []scmStep{{N: 1, Name: "Set up job", Status: "completed", Conclusion: "success"}}},
				{ID: "88001", Name: "test (ubuntu)", Status: "in_progress", URL: "https://github.com/acme/web/actions/runs/7001/job/88001", Check: "88001",
					Steps: []scmStep{{N: 1, Name: "Set up job", Status: "completed", Conclusion: "success"}, {N: 4, Name: "go test ./...", Status: "in_progress"}}},
				{ID: "88002", Name: "build", Status: "queued", Steps: []scmStep{}}}}},
		Checks: []scmCheck{
			{ID: "88000", Name: "lint", Status: "completed", Conclusion: "success", Job: "88000"},
			{ID: "88001", Name: "test (ubuntu)", Status: "in_progress", Job: "88001"},
			{ID: "88100", Name: "codecov/patch", App: "codecov", Status: "completed", Conclusion: "failure", URL: "https://github.com/acme/web/runs/88100",
				DetailsURL: "https://app.codecov.io/gh/acme/web", Title: "62% of diff hit", Summary: "see the report", Annotations: 2}},
		Statuses: []scmStatus{{Context: "ci/jenkins", State: "success", URL: "https://jenkins.example/12", Description: "Build #12 passed"}}}
}

// watchRow inserts a live watch as made by hand (a test's own row).
func (fx *ciFix) watchRow(t *testing.T, root int64, ref, sha string, c *scmChecks) *ciWatch {
	t.Helper()
	w := &ciWatch{RootRun: root, RunID: root, Source: ciManual, SCM: "apps/scm-github", Host: "github.com", Repo: "acme/web", Ref: ref, SHA: sha}
	if err := fx.ag.db.ciInsert(w); err != nil {
		t.Fatal(err)
	}
	if c != nil {
		w.setChecks(c)
		w.FetchedMs, w.UpdatedMs = nowMs(), nowMs()
		if err := fx.ag.db.ciSave(w); err != nil {
			t.Fatal(err)
		}
	}
	return w
}

// ciTask makes project "Web" (acme/web) and its task 1 row by row: a task
// run of alice's, its branch pushed at sha (its checkout's remote head).
func (fx *ciFix) ciTask(t *testing.T, sha string, prs []TaskPR) (*Project, *ProjectTask) {
	t.Helper()
	d := fx.ag.db
	p := &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer,
		SCM: "apps/scm-github", Host: "github.com", SandboxRef: sandboxRef("apps/cs", fx.box.ID), State: projActive}
	if err := d.insertProject(p); err != nil {
		t.Fatal(err)
	}
	if err := d.insertRepo(&ProjectRepo{ProjectID: p.ID, Slug: "web", Repo: "acme/web", URL: "https://github.com/acme/web.git", DefaultBranch: "main", State: "ready"}); err != nil {
		t.Fatal(err)
	}
	run := runAs(t, fx.ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: originProject, OriginID: p.ID}, false)
	b, _ := json.Marshal(prs)
	if _, err := d.q.Exec(`INSERT INTO project_tasks (project_id, n, run_id, title, slug, branch, ws, phase, prs, created_by, created_ms)
		VALUES (?, 1, ?, 'Fix the login loop', 'fix-login', 'xbin/k3x9/1-fix-login', 'ready', 'open', ?, 'alice', ?)`, p.ID, run, string(b), nowMs()); err != nil {
		t.Fatal(err)
	}
	k, err := d.taskByN(p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.putCheckout(ProjectCheckout{TaskID: k.ID, Repo: "web", Path: "/w/tasks/1-fix-login/web", Mode: coWorktree, State: "ready", RemoteSHA: sha}); err != nil {
		t.Fatal(err)
	}
	return p, k
}

// ciGET calls GET target as c and decodes the answer into out.
func ciGET(t *testing.T, mux *http.ServeMux, c caller, target string, out any) int {
	t.Helper()
	w := callAs(t, mux, c, "GET", target, nil)
	if out != nil && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("GET %s: %v: %s", target, err, w.Body)
		}
	}
	return w.Code
}

// ciGit runs git on the host for a fixture, with extra env.
func ciGit(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...) // exec-ok: a test fixture (a local origin and a clone in a host-directory sandbox)
	c.Dir = dir
	c.Env = append(append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null"), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// ciWait waits for cond (5 s).
func ciWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ciLiveOf is root's live watches (fresh from the table).
func (fx *ciFix) live(root int64) []*ciWatch { return fx.ag.db.ciLive(root) }

// ciReadSync reads a watch now (as the routes' reads do).
func (fx *ciFix) read(t *testing.T, id int64) {
	t.Helper()
	if err := ciRead(context.Background(), fx.ag.db, id); err != nil {
		t.Fatal(err)
	}
}

func ciDump(ws []*ciWatch) string {
	var b strings.Builder
	for _, w := range ws {
		fmt.Fprintf(&b, "\n  #%d root=%d run=%d %s %s %s@%s pr=%d state=%s ended=%d", w.ID, w.RootRun, w.RunID, w.Source, w.Repo, w.Ref, w.SHA, w.PR, w.State, w.EndedMs)
	}
	return b.String()
}
