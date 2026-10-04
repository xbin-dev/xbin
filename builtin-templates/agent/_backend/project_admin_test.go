package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Deleting a project: its tasks' workspaces cleaned, their conversations
// deleted, its credentials scrubbed, its rows gone — the sandbox kept, or
// deleted when the project made it and that was asked.
func TestProjectDelete(t *testing.T) {
	scrubbed := &hookLog{}
	old := scmScrubCreds
	scmScrubCreds = func(_ context.Context, p *Project, ref, why string) error {
		scrubbed.add(fmt.Sprintf("%d %s", p.ID, why))
		return nil
	}
	t.Cleanup(func() { scmScrubCreds = old })
	fx := newProjFix(t)
	p, k, runID := readyTask(t, fx, nil)
	if w := callAs(t, fx.mux, asCarol, "DELETE", fmt.Sprintf("/projects/%d", p.ID), nil); w.Code != 404 {
		t.Fatalf("someone else deletes it: %d", w.Code)
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/projects/%d?sandbox=keep", p.ID), nil); w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	hwait(t, "the project's rows to go", func() bool {
		_, err := fx.ag.db.getProject(p.ID)
		return err == errNoProject
	})
	if _, err := fx.ag.db.getRun(runID); err == nil {
		t.Fatal("the task's conversation stayed")
	}
	if _, err := os.Stat(filepath.Join(k.Dir, "web")); !os.IsNotExist(err) {
		t.Fatalf("the worktree stayed: %v", err)
	}
	if scrubbed.count(fmt.Sprintf("%d delete", p.ID)) == 0 {
		t.Fatalf("no scrub: %v", scrubbed.all())
	}
	if _, err := os.Stat(fx.box.Workdir); err != nil {
		t.Fatalf("the kept sandbox: %v", err)
	}
	for _, tbl := range []string{"project_tasks", "project_repos", "project_jobs", "project_events", "project_checkouts"} {
		var n int
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n)
		if n != 0 {
			t.Errorf("%s keeps %d rows", tbl, n)
		}
	}

	// a project that made its sandbox deletes it with sandbox=delete
	q := fx.newProject(t, asAlice, map[string]any{"name": "Fresh", "sandbox": map[string]any{"new": map[string]any{"provider": "apps/cs"}}})
	fx.waitRepoReady(t, q.ID)
	qp, _ := fx.ag.db.getProject(q.ID)
	if !qp.SandboxMade || qp.SandboxRef == "" {
		t.Fatalf("the made sandbox: %+v", qp)
	}
	_, id, _ := splitSandboxRef(qp.SandboxRef)
	conn, _ := sbxDial("apps/cs", "alice")
	box, err := conn.Get(context.Background(), id)
	if err != nil || box.Labels[projectLabel] != qp.UID || box.Visibility != visPrivate {
		t.Fatalf("the made sandbox at its manager: %+v %v", box, err)
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/projects/%d?sandbox=delete", q.ID), nil); w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	hwait(t, "the made sandbox deleted", func() bool {
		_, err := conn.Get(context.Background(), id)
		return sbxRefusal(err) == "not-found"
	})
}

// Archiving stops a project's pump and scrubs its credentials; the rest
// stays, and it comes back active.
func TestProjectArchive(t *testing.T) {
	scrubbed := &hookLog{}
	old := scmScrubCreds
	scmScrubCreds = func(_ context.Context, p *Project, ref, why string) error {
		scrubbed.add(why)
		return nil
	}
	t.Cleanup(func() { scmScrubCreds = old })
	fx := newProjFix(t)
	p := heldProject(t, fx, 1)
	_, r1 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "one"})
	waitStatus(t, fx.ag.db, r1, statusSleep)
	fx.newTask(t, asAlice, p.ID, map[string]any{"text": "two"})
	cur, _ := fx.ag.db.getProject(p.ID)
	w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version, "state": "archived"})
	if w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	hwait(t, "the scrub", func() bool { return scrubbed.count("archive") == 1 })
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version, "name": "stale"}); w.Code != 412 {
		t.Fatalf("a stale version: %d %s", w.Code, w.Body)
	}
	_ = fx.ag.db.setStatus(r1, statusIdle, 0, "", "")
	projectPump(p.ID)
	if startDelivered(fx, p.ID, 2) {
		t.Fatal("an archived project's pump started a task")
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks", p.ID), map[string]any{"text": "three"}); w.Code != 409 {
		t.Fatalf("a task in an archived project: %d %s", w.Code, w.Body)
	}
	cur, _ = fx.ag.db.getProject(p.ID)
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version, "state": "active"}); w.Code != 200 {
		t.Fatalf("unarchive: %d %s", w.Code, w.Body)
	}
	waitFor(t, "task 2's start once active", func() bool { return startDelivered(fx, p.ID, 2) })
}

// Repos come and go: one added is cloned; one open tasks use is removed
// only with force; the status shows the repos, the jobs, the slots and a
// warning for an unprotected default branch.
func TestProjectReposAndStatus(t *testing.T) {
	fx := newProjFix(t)
	no := false
	fx.scm.addRepo("acme/api", fx.origin, "main", &no)
	p, _, _ := readyTask(t, fx, nil)
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/repos", p.ID), map[string]any{"repo": "acme/api", "setup": "true"})
	if w.Code != 201 || !strings.Contains(w.Body.String(), `"slug":"api"`) {
		t.Fatalf("add a repo: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asCarol, "POST", fmt.Sprintf("/projects/%d/repos", p.ID), map[string]any{"repo": "acme/api"}); w.Code != 404 {
		t.Fatalf("someone else adds a repo: %d", w.Code)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/repos", p.ID), map[string]any{"repo": "acme/nope"}); w.Code != 400 || !strings.Contains(w.Body.String(), "not-found") {
		t.Fatalf("a repo the provider can't see: %d %s", w.Code, w.Body)
	}
	fx.waitRepoReady(t, p.ID)
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d/repos/api", p.ID), map[string]any{"setup": "make deps", "checkout": "clone"}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"checkout":"clone"`) {
		t.Fatalf("patch a repo: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/projects/%d/repos/web", p.ID), nil); w.Code != 409 || !strings.Contains(w.Body.String(), `"refusal":"busy"`) {
		t.Fatalf("removing a repo task 1 works in: %d %s", w.Code, w.Body)
	}
	var st ProjectStatus
	w = callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/status", p.ID), nil)
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	if st.Sandbox.State != "running" || st.Sandbox.Workdir != fx.box.Workdir || len(st.Repos) != 2 || st.Slots.Max != 3 || len(st.Jobs) == 0 {
		t.Fatalf("the status: %+v", st)
	}
	warned := false
	for _, wn := range st.Warnings {
		warned = warned || (wn.Kind == "unprotected" && wn.Repo == "acme/api")
	}
	if !warned {
		t.Fatalf("no warning for acme/api's unprotected branch: %+v", st.Warnings)
	}
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/projects/%d/repos/web?force=1", p.ID), nil); w.Code != 202 {
		t.Fatalf("forced: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/warm", p.ID), nil); w.Code != 202 || !strings.Contains(w.Body.String(), `"kind":"fetch"`) {
		t.Fatalf("warm: %d %s", w.Code, w.Body)
	}
	// policy protection refuse: a repo with an unprotected default branch fails
	cur, _ := fx.ag.db.getProject(p.ID)
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version, "policy": map[string]any{"protection": "refuse"}}); w.Code != 200 {
		t.Fatalf("policy: %d %s", w.Code, w.Body)
	}
	fx.scm.addRepo("acme/open", fx.origin, "main", &no)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/repos", p.ID), map[string]any{"repo": "acme/open"}); w.Code != 201 {
		t.Fatalf("add acme/open: %d %s", w.Code, w.Body)
	}
	hwait(t, "acme/open refused", func() bool {
		r, err := fx.ag.db.projectRepo(p.ID, "open")
		return err == nil && r.State == "failed" && strings.Contains(r.Error, "protection")
	})
}
