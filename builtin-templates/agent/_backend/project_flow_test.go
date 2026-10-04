package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
