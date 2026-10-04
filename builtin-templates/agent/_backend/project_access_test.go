package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"
)

// projBody is a POST /projects body over the fixture's sandbox.
func (fx *projFix) projBody(more map[string]any) map[string]any {
	body := map[string]any{"name": "Web", "scm": "apps/scm-github", "repos": []map[string]any{{"repo": "acme/web"}},
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", fx.box.ID)}}
	for k, v := range more {
		body[k] = v
	}
	return body
}

// insertTestProject writes a project straight to the store (no sandbox,
// no provider call), with acme/web as its repo.
func insertTestProject(t *testing.T, db *DB, owner string) *Project {
	t.Helper()
	p := &Project{Name: "Web", Slug: db.projectSlug("Web"), Kind: projPersonal, Owner: owner, Visibility: visPrivate,
		TeamRole: roleViewer, SCM: "apps/scm-github", Host: "github.com", CreatedBy: owner}
	if err := db.insertProject(p); err != nil {
		t.Fatal(err)
	}
	if err := db.insertRepo(&ProjectRepo{ProjectID: p.ID, Slug: "web", Repo: "acme/web", URL: "file:///nowhere", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	return p
}

// Who may do what with a project: the owner, a participant and a viewer
// member, view-as, an element, a manager who isn't in it, nobody — and,
// where a home's identity is the bot, who may name a repo (the bot rule);
// the kinds each home holds.
func TestProjectAccessMatrix(t *testing.T) {
	fx := newProjFix(t)
	// the bot rule, as it is by default: only managers name repos here
	scmBotAllowed = func(w who, _ string) bool { return w.manager() }
	if w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(nil)); w.Code != 403 || !strings.Contains(w.Body.String(), "bot rule") {
		t.Fatalf("a non-manager naming a repo for the bot: %d %s", w.Code, w.Body)
	}
	// the bot rule names alice for acme/*
	scmBotAllowed = func(w who, repo string) bool {
		ok, _ := path.Match("acme/*", repo)
		return w.manager() || (w.kind == whoUser && w.user == "alice" && ok)
	}
	if w := callAs(t, fx.mux, asBob, "POST", "/projects", fx.projBody(nil)); w.Code != 403 {
		t.Fatalf("bob, whom the rule doesn't name: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(map[string]any{"kind": "team"})); w.Code != 409 {
		t.Fatalf("kind team at an unpartitioned agent: %d %s", w.Code, w.Body)
	}
	p := fx.newProject(t, asAlice, map[string]any{"share": map[string]any{"members": []map[string]any{
		{"user": "carol", "role": roleParticipant}, {"user": "dave", "role": roleViewer}}}})
	type exp struct{ view, patch, task int }
	matrix := map[string]exp{
		"alice":         {200, 200, 201},
		"carol":         {200, 403, 201},
		"dave":          {200, 403, 403},
		"bob":           {404, 404, 404},
		"mgr":           {404, 404, 404},
		"view-as-alice": {404, 404, 404},
		"element":       {404, 404, 404},
		"system":        {200, 200, 201},
		"nobody":        {403, 403, 403},
		"cron":          {403, 403, 403},
	}
	for name, want := range matrix {
		c := allCallers[name]
		if got := callAs(t, fx.mux, c, "GET", fmt.Sprintf("/projects/%d", p.ID), nil).Code; got != want.view {
			t.Errorf("%s views the project: %d, want %d", name, got, want.view)
		}
		cur, _ := fx.ag.db.getProject(p.ID)
		if got := callAs(t, fx.mux, c, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": cur.Version, "name": "Web " + name}).Code; got != want.patch {
			t.Errorf("%s patches the project: %d, want %d", name, got, want.patch)
		}
		if got := callAs(t, fx.mux, c, "POST", fmt.Sprintf("/projects/%d/tasks", p.ID), map[string]any{"text": "x"}).Code; got != want.task {
			t.Errorf("%s makes a task: %d, want %d", name, got, want.task)
		}
	}
	// the list shows each what they may see
	var list struct {
		Items []ProjectView `json:"items"`
	}
	for name, n := range map[string]int{"alice": 1, "dave": 1, "bob": 0, "view-as-alice": 0} {
		w := callAs(t, fx.mux, allCallers[name], "GET", "/projects", nil)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Items) != n {
			t.Errorf("%s's list: %d %s", name, w.Code, w.Body)
		}
	}
	// the task copies the project's ACL: carol may talk in alice's task, dave read it
	k, _ := fx.ag.db.taskByN(p.ID, 1)
	if got := callAs(t, fx.mux, asDave, "GET", fmt.Sprintf("/runs/%d/task", k.RunID), nil).Code; got != 200 {
		t.Errorf("dave reads the task: %d", got)
	}
	if got := callAs(t, fx.mux, asBob, "GET", fmt.Sprintf("/runs/%d/task", k.RunID), nil).Code; got != 404 {
		t.Errorf("bob reads the task: %d", got)
	}
	// a member removed loses the task's conversation too
	if w := callAs(t, fx.mux, asDave, "DELETE", fmt.Sprintf("/projects/%d/members/dave", p.ID), nil); w.Code != 204 {
		t.Fatalf("dave leaves: %d %s", w.Code, w.Body)
	}
	if got := callAs(t, fx.mux, asDave, "GET", fmt.Sprintf("/runs/%d/task", k.RunID), nil).Code; got != 404 {
		t.Errorf("dave, gone, reads the task: %d", got)
	}
	if got := callAs(t, fx.mux, asCarol, "DELETE", fmt.Sprintf("/projects/%d/members/alice", p.ID), nil).Code; got != 403 {
		t.Errorf("carol removes someone else: %d", got)
	}
}

// At a partitioned agent's global instance a project is a team definition
// (kind team, shared); in a person's partition it is theirs alone.
func TestProjectKindsPerHome(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		setMode(t, modeGlobal, "")
		fx := newProjFix(t)
		if w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(nil)); w.Code != 409 {
			t.Fatalf("a personal project at global: %d %s", w.Code, w.Body)
		}
		team := fx.projBody(map[string]any{"kind": "team", "sandbox": nil})
		if w := callAs(t, fx.mux, asAlice, "POST", "/projects", team); w.Code != 409 {
			t.Fatalf("a team definition no one shares: %d %s", w.Code, w.Body)
		}
		team["share"] = map[string]any{"visibility": "team", "teamRole": "participant"}
		w := callAs(t, fx.mux, asAlice, "POST", "/projects", team)
		if w.Code != 201 || !strings.Contains(w.Body.String(), `"kind":"team"`) {
			t.Fatalf("a team definition: %d %s", w.Code, w.Body)
		}
		var out struct{ Project ProjectView }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		if w := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/projects/%d/tasks", out.Project.ID), map[string]any{"text": "x"}); w.Code != 409 {
			t.Fatalf("a task of a team definition: %d %s", w.Code, w.Body)
		}
	})
	t.Run("partition", func(t *testing.T) {
		setMode(t, modeUser, "alice")
		ag, mux := accessFixture(t)
		newP1SCM(t)
		body := map[string]any{"name": "Web", "scm": "apps/scm-github", "repos": []any{}, "sandbox": map[string]any{"ref": "apps/cs|x"},
			"share": map[string]any{"visibility": "team"}}
		if w := callAs(t, mux, asAlice, "POST", "/projects", body); w.Code != 409 {
			t.Fatalf("a shared project in a partition: %d %s", w.Code, w.Body)
		}
		p := insertTestProject(t, ag.db, "alice")
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/projects/%d/members", p.ID), map[string]any{"user": "bob"}); w.Code != 409 {
			t.Fatalf("a member in a partition: %d %s", w.Code, w.Body)
		}
		if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": 1, "visibility": "team"}); w.Code != 409 {
			t.Fatalf("team visibility in a partition: %d %s", w.Code, w.Body)
		}
	})
}

// A project's tasks never run in a class with internal reach — at
// creation, by a policy change, by a task's own class — nor in a class the
// caller may not use; a class edited after the task started (a private-lane
// one gaining internal) still gives the task no internal reach or MCP.
func TestTaskClassNeverInternal(t *testing.T) {
	fx := newProjFix(t)
	boxy := agentClass{ID: "boxy", Name: "Boxy", Toolsets: []string{tsSandbox, tsFiles}, Managers: classSet{All: true}, SandboxEgress: []string{"none"}}
	mgrOnly := agentClass{ID: "mgronly", Name: "Managers' coding", Toolsets: []string{tsSandbox, tsWeb, tsFiles}, Managers: classSet{All: true},
		SandboxEgress: []string{"internet"}, Who: "managers"}
	classStore.Store(newClassState(classSettings{Classes: []agentClass{boxy, mgrOnly}}))
	t.Cleanup(func() { classStore.Store(nil) })

	w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(map[string]any{"policy": map[string]any{"taskClass": "internal"}}))
	if w.Code != 409 || !strings.Contains(w.Body.String(), refusalClassInternal) {
		t.Fatalf("POST /projects with an internal task class: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", "/projects", fx.projBody(map[string]any{"policy": map[string]any{"taskClass": "mgronly"}})); w.Code != 403 {
		t.Fatalf("a managers-only class from a non-manager: %d %s", w.Code, w.Body)
	}
	p := fx.newProject(t, asAlice, nil)
	w = callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/projects/%d", p.ID), map[string]any{"version": p.Version, "policy": map[string]any{"taskClass": "internal"}})
	if w.Code != 409 || !strings.Contains(w.Body.String(), refusalClassInternal) {
		t.Fatalf("PATCH policy.taskClass internal: %d %s", w.Code, w.Body)
	}
	for class, code := range map[string]int{"internal": 409, "mgronly": 403} {
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/tasks", p.ID), map[string]any{"text": "x", "class": class}); w.Code != code {
			t.Errorf("a task in %s: %d %s", class, w.Code, w.Body)
		}
	}
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "x", "class": "boxy"})
	cfg, _ := fx.ag.db.runConfig(runID)
	if cfg.Toolset != "private" || cfg.Class != "boxy" {
		t.Fatalf("the task's class and lane: %q %q", cfg.Class, cfg.Toolset)
	}
	// the class is edited to add internal reach (and MCP): a private-lane
	// class keeps it through clampTo, but never in a project's run
	boxy.Toolsets = append(boxy.Toolsets, tsInternal)
	boxy.MCP = classSet{All: true}
	classStore.Store(newClassState(classSettings{Classes: []agentClass{boxy, mgrOnly}}))
	plain := cfg
	plain.Project = nil
	if cl := classOf(plain); !cl.has(tsInternal) {
		t.Fatal("the fixture: without project the edited class would have internal reach")
	}
	if cl := classOf(cfg); cl.has(tsInternal) || cl.MCP.All || len(cl.MCP.Names) > 0 {
		t.Fatalf("a task's class after the edit: %+v", cl)
	}
}

// Every read through the project's identity is held to its own repos: the
// issue picker, a batch from issues and a task's issue name one of them or
// get 400 — at a bot home and in a person's partition alike.
func TestTaskReposOfProjectOnly(t *testing.T) {
	check := func(t *testing.T, mux *http.ServeMux, p *Project) {
		for _, c := range []struct {
			method, path string
			body         any
		}{
			{"GET", fmt.Sprintf("/projects/%d/issues?repo=acme/secret", p.ID), nil},
			{"POST", fmt.Sprintf("/projects/%d/tasks/batch", p.ID), map[string]any{"issues": []map[string]any{{"repo": "acme/web", "number": 1}, {"repo": "acme/secret", "number": 2}}}},
			{"POST", fmt.Sprintf("/projects/%d/tasks", p.ID), map[string]any{"text": "x", "issue": map[string]any{"repo": "acme/secret", "number": 3}}},
		} {
			if w := callAs(t, mux, asAlice, c.method, c.path, c.body); w.Code != 400 {
				t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
			}
		}
	}
	t.Run("bot home", func(t *testing.T) {
		fx := newProjFix(t)
		fx.scm.addRepo("acme/secret", fx.origin, "main", nil)
		p := fx.newProject(t, asAlice, nil)
		pp, _ := fx.ag.db.getProject(p.ID)
		check(t, fx.mux, pp)
		fx.scm.addIssue("acme/web", scmIssue{Number: 7, Title: "Broken", Body: strings.Repeat("long ", 2000)})
		w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/issues?repo=acme/web", p.ID), nil)
		var page scmPage[scmIssue]
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &page) != nil || len(page.Items) != 1 || page.Items[0].Title != "Broken" ||
			len(page.Items[0].Body) > 2<<10+8 {
			t.Fatalf("the project's own issues (bodies clipped to 2 KiB): %d %s", w.Code, clip(w.Body.String(), 300))
		}
	})
	t.Run("partition", func(t *testing.T) {
		setMode(t, modeUser, "alice")
		ag, mux := accessFixture(t)
		newP1SCM(t)
		check(t, mux, insertTestProject(t, ag.db, "alice"))
	})
}

// A project's conversation is shared with its project and stays in its
// space: sharing it apart, publishing it and moving it are refused; its
// export stays.
func TestProjectRunBarred(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "x"})
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"PATCH", fmt.Sprintf("/runs/%d", runID), map[string]any{"visibility": "team"}},
		{"PATCH", fmt.Sprintf("/runs/%d", runID), map[string]any{"teamRole": "participant"}},
		{"POST", fmt.Sprintf("/runs/%d/members", runID), map[string]any{"user": "bob"}},
	} {
		if w := callAs(t, fx.mux, asAlice, c.method, c.path, c.body); w.Code != 409 || !strings.Contains(w.Body.String(), "task of project Web") {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	if w := callAs(t, fx.mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", runID), map[string]any{"title": "renamed"}); w.Code != 200 {
		t.Errorf("a rename stays allowed: %d %s", w.Code, w.Body)
	}
	if isChat(originProject) {
		t.Error("a project's run would move home")
	}
	// publish and the hosting move refuse it through the same check
	rec := httptest.NewRecorder()
	if !projectRunBarred(rec, runID) || rec.Code != 409 {
		t.Errorf("projectRunBarred: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	plain := runAs(t, fx.ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if projectRunBarred(rec, plain) {
		t.Error("a chat is barred")
	}
}

func TestProjectPublishBarredInPartition(t *testing.T) {
	setMode(t, modeUser, "alice")
	ag, mux := accessFixture(t)
	p := insertTestProject(t, ag.db, "alice")
	run, err := ag.startRunOpts(runOpts{Title: "t", Cfg: defaultConfig(), Hold: true, Stamp: runStamp{Owner: "alice", Visibility: visPrivate,
		TeamRole: roleViewer, Origin: originProject, OriginID: p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/publish", run.ID), map[string]any{"share": map[string]any{"visibility": "team"}})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "task of project") {
		t.Fatalf("publishing a project's run: %d %s", w.Code, w.Body)
	}
}
