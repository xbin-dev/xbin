package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// The workspace for real: a project over a sandbox of the fake manager (a
// host directory that isn't /work), its repo cloned from a local bare
// origin into a bare base, a task's worktree on its branch, .task-env, the
// conversation bound to the checkout, and its first turn run.
func TestWorktreeFlow(t *testing.T) {
	fx := newProjFix(t)
	if fx.box.Workdir == "/work" || !strings.HasPrefix(fx.box.Workdir, os.TempDir()) {
		t.Fatalf("the fixture's workdir should be odd: %s", fx.box.Workdir)
	}
	p := fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": "echo set up > .setup-ran"}}})
	if p.Kind != projPersonal || p.Owner != "alice" || len(p.UID) != 6 || p.Slug != "web" || p.Level != "owner" {
		t.Fatalf("the project: %+v", p)
	}
	fx.waitRepoReady(t, p.ID)
	p2, _ := fx.ag.db.getProject(p.ID)
	if want := fx.box.Workdir + "/web"; p2.Dir != want {
		t.Fatalf("dir %q, want %q", p2.Dir, want)
	}
	base := filepath.Join(p2.Dir, ".repos", "web.git")
	if out := gitRun(t, base, "rev-parse", "--is-bare-repository"); out != "true" {
		t.Fatalf("the base isn't bare: %s", out)
	}
	if got := gitRun(t, base, "config", "core.logAllRefUpdates"); got != "true" {
		t.Fatalf("logAllRefUpdates: %s", got)
	}
	tv, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "Fix the login page"})
	if tv.N != 1 || tv.Title != "Fix the login page" || tv.Branch != "xbin/"+p.UID+"/1-fix-the-login-page" || runID == 0 {
		t.Fatalf("the task: %+v run %d", tv, runID)
	}
	k := fx.waitWS(t, p.ID, 1, wsReady)
	co := filepath.Join(k.Dir, "web")
	if got := gitRun(t, co, "branch", "--show-current"); got != tv.Branch {
		t.Fatalf("the checkout's branch: %s", got)
	}
	if _, err := os.Stat(filepath.Join(co, "README.md")); err != nil {
		t.Fatalf("the checkout: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(co, ".setup-ran")); err != nil || !strings.Contains(string(b), "set up") {
		t.Fatalf("the setup didn't run in the checkout: %v %q", err, b)
	}
	env, err := os.ReadFile(filepath.Join(k.Dir, ".task-env"))
	if err != nil || !strings.Contains(string(env), "BRANCH='"+tv.Branch+"'") || !strings.Contains(string(env), "PORT='20000'") {
		t.Fatalf(".task-env: %v %s", err, env)
	}
	cfg, _ := fx.ag.db.runConfig(runID)
	if cfg.Sandbox == nil || cfg.Sandbox.Ref != sandboxRef("apps/cs", fx.box.ID) || cfg.Sandbox.Cwd != co {
		t.Fatalf("the binding: %+v", cfg.Sandbox)
	}
	if !cfg.Project.isTask() || cfg.Project.ID != p.ID || cfg.Project.N != 1 {
		t.Fatalf("Config.Project: %+v", cfg.Project)
	}
	waitFor(t, "the task's first turn", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return r != nil && resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) > 0
	})
	call := fakeOf(fx.ag).callsFor(runID)[0]
	sys := asString(call.Msgs[0].Content)
	for _, want := range []string{"# Project", "task #1 of the project \"Web\"", tv.Branch, "AGENTS.md", "never merge"} {
		if !strings.Contains(sys, want) {
			t.Errorf("the system prompt lacks %q:\n%s", want, sys)
		}
	}
	var view map[string]any
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", runID), nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &view) != nil || view["ws"] != wsReady {
		t.Fatalf("GET /runs/{id}/task: %d %s", w.Code, w.Body)
	}
}

// readyTask is a project with task 1 prepared and its first turn over.
func readyTask(t *testing.T, fx *projFix, more map[string]any) (ProjectView, *ProjectTask, int64) {
	t.Helper()
	p := fx.newProject(t, asAlice, more)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "do it"})
	k := fx.waitWS(t, p.ID, 1, wsReady)
	waitFor(t, "the first turn", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) == 1
	})
	waitFor(t, "the worker going quiet", func() bool {
		return len(fx.ag.db.jobsWhere(`WHERE project_id=? AND state IN ('queued','running','waiting')`, p.ID)) == 0
	})
	return p, k, runID
}

// waitJobsDone waits until project pid has no live job.
func waitJobsDone(t *testing.T, fx *projFix, pid int64) {
	t.Helper()
	hwait(t, "the project's jobs", func() bool {
		return len(fx.ag.db.jobsWhere(`WHERE project_id=? AND state IN ('queued','running','waiting')`, pid)) == 0
	})
}

// Preparing a task again changes nothing: the checkout and the work in it
// stay, the branch is the same.
func TestPrepareIdempotent(t *testing.T) {
	fx := newProjFix(t)
	p, k, _ := readyTask(t, fx, nil)
	co := filepath.Join(k.Dir, "web")
	if err := os.WriteFile(filepath.Join(co, "work.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	head := gitRun(t, co, "rev-parse", "HEAD")
	if _, err := fx.ag.db.queueJob(p.ID, k.ID, "", pjPrepare, "", 0); err != nil {
		t.Fatal(err)
	}
	waitJobsDone(t, fx, p.ID)
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if k2.WS != wsReady || k2.Branch != k.Branch {
		t.Fatalf("after preparing again: ws %s branch %s", k2.WS, k2.Branch)
	}
	if b, err := os.ReadFile(filepath.Join(co, "work.txt")); err != nil || string(b) != "mine" {
		t.Fatalf("the work in the checkout: %v %q", err, b)
	}
	if got := gitRun(t, co, "rev-parse", "HEAD"); got != head {
		t.Fatalf("HEAD moved: %s → %s", head, got)
	}
	if js := fx.ag.db.jobsWhere(`WHERE project_id=? AND state='failed'`, p.ID); len(js) != 0 {
		t.Fatalf("failed jobs: %s", jobsDump(fx.ag.db, p.ID))
	}
}

// A job a predecessor engine claimed is queued again at takeover and run
// by the successor; the predecessor can no longer write.
func TestTakeoverResumesJobs(t *testing.T) {
	fx := newProjFix(t)
	p, _, _ := readyTask(t, fx, nil)
	a := fx.ag.eng
	a.mu.Lock()
	old := a.epoch
	a.mu.Unlock()
	// a fetch a's worker had claimed when it went away
	res, err := fx.ag.db.q.Exec(`INSERT INTO project_jobs (project_id, task_id, repo_slug, kind, state, epoch, created_ms, updated_ms)
		VALUES (?, 0, 'web', 'fetch', 'running', ?, ?, ?)`, p.ID, old, nowMs(), nowMs())
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	b := successor(t, fx.ag)
	hwait(t, "the successor to run the job", func() bool {
		j, err := fx.ag.db.getJob(id)
		return err == nil && j.State == pjDone
	})
	j, _ := fx.ag.db.getJob(id)
	b.mu.Lock()
	cur := b.epoch
	b.mu.Unlock()
	if j.Epoch != cur || cur == old {
		t.Fatalf("the job's epoch %d, the successor's %d (the predecessor's %d)", j.Epoch, cur, old)
	}
	if err := a.fenced(func(*DB) error { return nil }); err != errFenced {
		t.Fatalf("the predecessor still writes: %v", err)
	}
}

// commitIn makes a commit in a checkout.
func commitIn(t *testing.T, dir, file, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", file)
	gitRun(t, dir, "commit", "-q", "-m", "change "+file)
}

// Cleanup refuses a checkout with uncommitted or unpushed work; once it is
// pushed, the worktree and the branch go and the conversation stays.
func TestCleanupRefusesUnpushed(t *testing.T) {
	fx := newProjFix(t)
	p, k, runID := readyTask(t, fx, nil)
	co := filepath.Join(k.Dir, "web")
	cleanup := func(body map[string]any) (int, string) {
		w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", runID), body)
		return w.Code, w.Body.String()
	}
	if err := os.WriteFile(filepath.Join(co, "wip.txt"), []byte("wip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := cleanup(nil); code != 409 || !strings.Contains(body, `"refusal":"dirty"`) || !strings.Contains(body, `"dirty":1`) {
		t.Fatalf("uncommitted work: %d %s", code, body)
	}
	gitRun(t, co, "add", "wip.txt")
	gitRun(t, co, "commit", "-q", "-m", "wip")
	if code, body := cleanup(nil); code != 409 || !strings.Contains(body, `"unpushed":1`) {
		t.Fatalf("unpushed work: %d %s", code, body)
	}
	gitRun(t, co, "push", "-q")
	if code, body := cleanup(nil); code != 202 {
		t.Fatalf("pushed: %d %s", code, body)
	}
	fx.waitWS(t, p.ID, 1, wsCleaned)
	if _, err := os.Stat(co); !os.IsNotExist(err) {
		t.Fatalf("the worktree is still there: %v", err)
	}
	if _, err := os.Stat(k.Dir); !os.IsNotExist(err) {
		t.Fatalf("the task directory is still there: %v", err)
	}
	base := filepath.Join(fx.box.Workdir, "web", ".repos", "web.git")
	if out := gitRun(t, base, "branch", "--list", k.Branch); out != "" {
		t.Fatalf("the branch is still in the base: %q", out)
	}
	if _, err := fx.ag.db.getRun(runID); err != nil {
		t.Fatal("the conversation went with the workspace")
	}
	// the worker's own cleanup refuses as the route does: ws blocked
	p2 := fx.newProject(t, asAlice, map[string]any{"name": "Two"})
	_, run2 := fx.newTask(t, asAlice, p2.ID, map[string]any{"text": "two"})
	k2 := fx.waitWS(t, p2.ID, 1, wsReady)
	commitIn(t, filepath.Join(k2.Dir, "web"), "x.txt", "x")
	_ = run2
	if _, err := fx.ag.db.queueJob(p2.ID, k2.ID, "", pjCleanup, "", 0); err != nil {
		t.Fatal(err)
	}
	k2 = fx.waitWS(t, p2.ID, 1, wsBlocked)
	if !strings.Contains(k2.Error, `"unpushed":1`) {
		t.Fatalf("blocked with: %s", k2.Error)
	}
}

// Only the conversation's owner forces a cleanup over unpushed work.
func TestCleanupForceOwnerOnly(t *testing.T) {
	fx := newProjFix(t)
	p, k, runID := readyTask(t, fx, map[string]any{"share": map[string]any{"members": []map[string]any{{"user": "carol", "role": "participant"}}}})
	co := filepath.Join(k.Dir, "web")
	commitIn(t, co, "mine.txt", "unpushed")
	if w := callAs(t, fx.mux, asCarol, "POST", fmt.Sprintf("/runs/%d/task/cleanup", runID), map[string]any{"force": true}); w.Code != 403 {
		t.Fatalf("a participant forcing: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asCarol, "POST", fmt.Sprintf("/runs/%d/task/close", runID), map[string]any{}); w.Code != 200 {
		t.Fatalf("a participant closes the task: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", runID), map[string]any{"force": true}); w.Code != 202 {
		t.Fatalf("the owner forcing: %d %s", w.Code, w.Body)
	}
	fx.waitWS(t, p.ID, 1, wsCleaned)
	if _, err := os.Stat(co); !os.IsNotExist(err) {
		t.Fatalf("the forced cleanup left the worktree: %v", err)
	}

	// forced while a cleanup that isn't runs: that one done (refused),
	// the forced one runs after it — the mark isn't written over
	_, run2 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "two"})
	k2 := fx.waitWS(t, p.ID, 2, wsReady)
	commitIn(t, filepath.Join(k2.Dir, "web"), "x.txt", "x")
	_ = fx.ag.db.putSetting("halt", "1") // the worker claims nothing meanwhile
	j, err := fx.ag.db.queueJob(p.ID, k2.ID, "", pjCleanup, "alice", 0)
	if err != nil {
		t.Fatal(err)
	}
	fx.ag.eng.mu.Lock()
	epoch := fx.ag.eng.epoch
	fx.ag.eng.mu.Unlock()
	_, _ = fx.ag.db.q.Exec(`UPDATE project_jobs SET state='running', epoch=? WHERE id=?`, epoch, j.ID)
	j.Epoch = epoch
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", run2), map[string]any{"force": true}); w.Code != 202 {
		t.Fatalf("forcing: %d %s", w.Code, w.Body)
	}
	projWorkers.Lock()
	wk := projWorkers.m[fx.ag.eng]
	projWorkers.Unlock()
	pp, _ := fx.ag.db.getProject(p.ID)
	out, _ := doneJob("refused: work that isn't pushed")
	wk.finish(pp, k2, j, out, nil)
	if js := fx.ag.db.jobsWhere(`WHERE id=?`, j.ID); len(js) != 1 || js[0].State != pjQueued || js[0].ClientID != cleanupForce {
		t.Fatalf("the forced cleanup after the running one: %s", jobsDump(fx.ag.db, p.ID))
	}
	_ = fx.ag.db.putSetting("halt", "")
	kickProjectWorker()
	fx.waitWS(t, p.ID, 2, wsCleaned)
}

// Deleting a task's conversation marks the task deleted and cleans its
// workspace up.
func TestDeleteRunMarksTask(t *testing.T) {
	fx := newProjFix(t)
	p, k, runID := readyTask(t, fx, nil)
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d", runID), nil); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	k2 := fx.waitWS(t, p.ID, 1, wsCleaned)
	if k2.RunID != 0 || k2.Phase != phaseDeleted {
		t.Fatalf("the task: run %d phase %s", k2.RunID, k2.Phase)
	}
	if _, err := os.Stat(filepath.Join(k.Dir, "web")); !os.IsNotExist(err) {
		t.Fatalf("the worktree stayed: %v", err)
	}
	var tv TaskView
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/tasks/1", p.ID), nil)
	if json.Unmarshal(w.Body.Bytes(), &tv) != nil || tv.State != taskDeleted || tv.Column != colDone {
		t.Fatalf("the deleted task's view: %s", w.Body)
	}
}

// What a task pushes itself is noticed: its branch on the remote, then a
// moved head, each run projectRefsHooks once; a PR opened in a later turn
// without a push, and one opened outside any turn (projectRefsCheck), are
// recorded in prs.
func TestRefsJobFiresHooks(t *testing.T) {
	refs := &hookLog{}
	old := projectRefsHooks
	projectRefsHooks = append(append([]func(*DB, *Project, *ProjectTask){}, old...), func(_ *DB, _ *Project, k *ProjectTask) {
		refs.add(fmt.Sprintf("%d %s", k.N, k.Phase))
	})
	t.Cleanup(func() { projectRefsHooks = old }) // registered first: runs after the engine stops
	fx := newProjFix(t)
	p, k, runID := readyTask(t, fx, nil)
	co := filepath.Join(k.Dir, "web")
	turn := func(text string) {
		t.Helper()
		n := len(fakeOf(fx.ag).callsFor(runID))
		send(t, fx.ag, runID, text)
		waitFor(t, "the turn", func() bool {
			r, _ := fx.ag.db.getRun(runID)
			return resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) > n
		})
		waitJobsDone(t, fx, p.ID)
	}
	if n := len(refs.all()); n != 0 {
		t.Fatalf("hooks before any push: %v", refs.all())
	}
	commitIn(t, co, "a.txt", "a")
	gitRun(t, co, "push", "-q")
	turn("pushed")
	if got := refs.all(); len(got) != 1 {
		t.Fatalf("the first push: hooks %v", got)
	}
	cos := fx.ag.db.checkouts(k.ID)
	if len(cos) != 1 || cos[0].RemoteSHA != gitRun(t, co, "rev-parse", "HEAD") {
		t.Fatalf("the remote sha: %+v", cos)
	}
	turn("nothing new")
	if got := refs.all(); len(got) != 1 {
		t.Fatalf("a turn without a push: hooks %v", got)
	}
	commitIn(t, co, "b.txt", "b")
	gitRun(t, co, "push", "-q")
	turn("pushed again")
	if got := refs.all(); len(got) != 2 {
		t.Fatalf("the moved head: hooks %v", got)
	}
	// gh pr create in a later turn: no push, a PR now open
	fx.scm.addPull("acme/web", scmPull{Number: 42, URL: "https://github.com/acme/web/pull/42", State: "open",
		Head: scmRef{Ref: k.Branch, SHA: gitRun(t, co, "rev-parse", "HEAD")}})
	turn("opened a PR")
	if got := refs.all(); len(got) != 3 || got[2] != "1 pr" {
		t.Fatalf("the PR: hooks %v", got)
	}
	k2, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k2.taskPRs(); len(prs) != 1 || prs[0].Number != 42 || prs[0].State != "open" || k2.Phase != phasePR {
		t.Fatalf("prs %s phase %s", k2.PRs, k2.Phase)
	}
	// another PR for the branch (against another base), opened outside any
	// turn: the scm event asks for the refs check, which reads the PRs
	fx.scm.addPull("acme/web", scmPull{Number: 43, URL: "https://github.com/acme/web/pull/43", State: "open",
		Head: scmRef{Ref: k.Branch, SHA: gitRun(t, co, "rev-parse", "HEAD")}})
	_ = fx.ag.db.Tx(func(t2 *DB) error { projectRefsCheck(t2, p.ID, 1); return nil })
	waitJobsDone(t, fx, p.ID)
	k3, _ := fx.ag.db.taskByN(p.ID, 1)
	if prs := k3.taskPRs(); len(prs) != 2 || prs[1].Number != 43 {
		t.Fatalf("the PR opened outside a turn: %s", k3.PRs)
	}
	if got := refs.all(); len(got) != 4 {
		t.Fatalf("hooks after it: %v", got)
	}
	opened := 0
	for _, e := range fx.ag.db.projectEvents(p.ID, 0, 200) {
		if e.Kind == pevPROpened {
			opened++
		}
	}
	if opened != 2 {
		t.Fatalf("pr.opened events: %d", opened)
	}
}

// A task a coding agent answers: its first prompt starts with the brief,
// it works in the checkout, and its commands see the task's environment.
func TestHarnessTask(t *testing.T) {
	fx := newProjFix(t)
	fx.m.Caps = []string{"exec", "files", "tar"}
	argv := acptest.Command()
	fx.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(fx.ag.eng) })
	p := fx.newProject(t, asAlice, nil)
	tv, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "fix the header", "agent": map[string]any{"provider": "fake"}})
	if run, _ := fx.ag.db.getRun(runID); run.Engine != engineHarness {
		t.Fatalf("the task's engine: %q", run.Engine)
	}
	k := fx.waitWS(t, p.ID, 1, wsReady)
	hwait(t, "the coding agent's first turn", func() bool {
		return turnOver(fx.ag, runID)() && strings.Contains(fullText(fx.ag.db, runID), "fix the header")
	})
	text := fullText(fx.ag.db, runID)
	if i, j := strings.Index(text, "# Project"), strings.Index(text, "fix the header"); i < 0 || j < i {
		t.Fatalf("the first prompt doesn't start with the brief: %s", clip(text, 600))
	}
	co := filepath.Join(k.Dir, "web")
	if hs, _ := fx.ag.db.harnessSession(runID); hs == nil || hs.Cwd != co {
		t.Fatalf("the coding agent's cwd: %+v", hs)
	}
	for name, want := range map[string]string{"BRANCH": tv.Branch, "PORT": "20000", "TASK_DIR": k.Dir} {
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", runID), map[string]any{"text": "printenv " + name}); w.Code != 200 {
			t.Fatal(w.Body)
		}
		hwait(t, "printenv "+name, func() bool {
			return turnOver(fx.ag, runID)() && strings.Contains(fullText(fx.ag.db, runID), "printenv: "+want)
		})
	}

	// a start that waited for a slot past its workspace's readiness gets
	// the brief too: one slot, task 1 holding it (it waits for a person)
	cur, _ := fx.ag.db.getProject(p.ID)
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version,
		"policy": map[string]any{"maxTasks": 1}}); w.Code != 200 {
		t.Fatalf("maxTasks 1: %d %s", w.Code, w.Body)
	}
	if err := fx.ag.db.setStatus(runID, statusWaiting, 0, "which one?", ""); err != nil {
		t.Fatal(err)
	}
	_, run2 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "fix the footer", "agent": map[string]any{"provider": "fake"}})
	fx.waitWS(t, p.ID, 2, wsReady)
	if startDelivered(fx, p.ID, 2) {
		t.Fatal("task 2 started without a slot")
	}
	if err := fx.ag.db.setStatus(runID, statusIdle, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	hwait(t, "task 2's first turn", func() bool {
		return turnOver(fx.ag, run2)() && strings.Contains(fullText(fx.ag.db, run2), "fix the footer")
	})
	text = fullText(fx.ag.db, run2)
	if i, j := strings.Index(text, "# Project"), strings.Index(text, "fix the footer"); i < 0 || j < i {
		t.Fatalf("a queued start's first prompt doesn't start with the brief: %s", clip(text, 600))
	}
}
