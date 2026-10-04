package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

var projectTables = []string{"projects", "project_members", "project_repos", "project_tasks", "project_checkouts",
	"project_jobs", "project_queue", "project_events"}

// The schema is additive and idempotent: run twice on the same database it
// changes nothing and keeps what is there.
func TestProjectSchemaMigratesTwice(t *testing.T) {
	db := newTestDB(t)
	p := &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, SCM: "apps/scm-github"}
	if err := db.insertProject(p); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.addProjectSchema(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if err := db.addFeatureSchemas(); err != nil {
			t.Fatalf("feature schemas, run %d: %v", i, err)
		}
	}
	got, err := db.getProject(p.ID)
	if err != nil || got.UID != p.UID || got.Name != "Web" || string(got.Policy) != "{}" {
		t.Fatalf("the project after migrating twice: %+v %v", got, err)
	}
	for _, tbl := range projectTables {
		var n int
		if err := db.q.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil {
			t.Errorf("%s: %v", tbl, err)
		}
	}
}

// A database the previous binary left (no project tables) opens: its rows
// are kept, the project tables are made empty, and opening it again
// changes nothing.
func TestProjectSchemaOldDB(t *testing.T) {
	path := seedOldDB(t, oldHarnessRows)
	for i := 0; i < 2; i++ {
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		for _, tbl := range projectTables {
			var n int
			if err := db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
				t.Fatalf("open %d: %s: %d %v", i, tbl, n, err)
			}
		}
		runs, err := db.queryRuns(`ORDER BY id`)
		if err != nil || len(runs) != 3 || runs[0].Title != "fix the build" {
			t.Fatalf("open %d: the old runs: %v %v", i, runs, err)
		}
		if !db.features {
			t.Fatal("the feature flag is off after opening")
		}
		db.sql.Close()
	}
}

// In a person's partition projects number from 2^40, as its conversations
// do: a project id names its home.
func TestProjectIDsFrom2to40(t *testing.T) {
	setMode(t, modeUser, "alice")
	db := newTestDB(t)
	p := &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice"}
	if err := db.insertProject(p); err != nil {
		t.Fatal(err)
	}
	if p.ID < partitionIDBase {
		t.Fatalf("a partition's project id %d is below 2^40", p.ID)
	}
	if err := db.addProjectSchema(); err != nil { // again: the seed is idempotent
		t.Fatal(err)
	}
	q := &Project{Name: "Api", Slug: "api", Kind: projPersonal, Owner: "alice"}
	if err := db.insertProject(q); err != nil || q.ID != p.ID+1 {
		t.Fatalf("the next id: %d (%v)", q.ID, err)
	}
	setMode(t, modeLegacy, "")
	d2 := newTestDB(t)
	r := &Project{Name: "Web", Slug: "web", Kind: projPersonal}
	if err := d2.insertProject(r); err != nil || r.ID != 1 {
		t.Fatalf("unpartitioned, projects number from 1: %d %v", r.ID, err)
	}
}

// Project routes name the project {pid}, never {id} (the global
// instance's hostedRoute intercepts every {id} pattern); a route on a run
// stays /runs/{id}/….
func TestProjectRoutesUsePid(t *testing.T) {
	for _, rt := range projectRoutes() {
		_, path, _ := strings.Cut(rt.pattern, " ")
		switch {
		case strings.HasPrefix(path, "/projects"):
			if strings.Contains(path, "{id}") {
				t.Errorf("%s names {id}", rt.pattern)
			}
		case strings.HasPrefix(path, "/runs/{id}/task"):
			if rt.need < needViewer || rt.need > needOwner {
				t.Errorf("%s: a run route resolves the run's level (guard)", rt.pattern)
			}
		default:
			t.Errorf("%s: neither a project nor a task route", rt.pattern)
		}
	}
}

// A task is a run with origin project: never in the chat list, never moved
// home, but in the run list of those who may see it.
func TestTaskRunsNotChats(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "do it"})
	run, _ := fx.ag.db.getRun(runID)
	if run.Origin != originProject || run.OriginID != p.ID || run.Owner != "alice" || run.ParentID != 0 {
		t.Fatalf("the task's run: %+v", run)
	}
	if isChat(run.Origin) {
		t.Fatal("a task is a chat (it would move home)")
	}
	w := callAs(t, fx.mux, asAlice, "GET", "/conversations", nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d,`, runID)) {
		t.Fatalf("the chat list shows the task: %d %s", w.Code, w.Body)
	}
	w = callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d", runID), nil)
	var v map[string]any
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("GET /runs/{id}: %d %s", w.Code, w.Body)
	}
	pr, _ := v["project"].(map[string]any)
	if pr == nil || pr["role"] != projRoleTask || pr["name"] != "Web" || v["projectTask"] == nil {
		t.Fatalf("the run's project keys: %v", v["project"])
	}
}

// The policy: defaults filled, unknown keys kept, a PATCH merges objects
// key by key and null takes a key back to its default; bad values refused.
func TestProjectPolicyMerge(t *testing.T) {
	stored := json.RawMessage(`{"maxTasks":5,"ci":{"maxPerDay":2},"futureKey":{"x":1}}`)
	merged, err := mergePolicy(stored, json.RawMessage(`{"ci":{"delaySec":30},"maxTasks":null,"autoPR":"draft"}`))
	if err != nil {
		t.Fatal(err)
	}
	pol := policyOf(merged)
	if pol.MaxTasks != 3 || pol.CI.MaxPerDay != 2 || pol.CI.DelaySec != 30 || !pol.CI.AutoFix || pol.AutoPR != "draft" {
		t.Fatalf("merged: %s → %+v", merged, pol)
	}
	var view map[string]any
	_ = json.Unmarshal(policyView(merged), &view)
	if view["futureKey"] == nil || view["taskClass"] != "coding" || view["setupBlocking"] != true {
		t.Fatalf("the view: %v", view)
	}
	for _, bad := range []string{`{"maxTasks":17}`, `{"engine":"x"}`, `{"autoPR":"yes"}`, `{"branchPrefix":"../x"}`,
		`{"ports":{"base":80,"span":10,"slots":1}}`, `{"as":"root"}`} {
		if why := checkPolicy(json.RawMessage(bad)); why == "" {
			t.Errorf("%s was taken", bad)
		}
	}
	if why := checkPolicy(json.RawMessage(`{"maxTasks":16,"branchPrefix":"team/web"}`)); why != "" {
		t.Errorf("a good policy refused: %s", why)
	}
	if got := slugOf("  Fix: the LOGIN page!! ", 32); got != "fix-the-login-page" {
		t.Errorf("slug: %q", got)
	}
	if got := portsOf(defaultProjectPolicy(), 102); got.Base != 20010 || got.Span != 10 {
		t.Errorf("ports of task 102: %+v", got)
	}
}

// After an older build rewrote a task's and a coordinator's config without
// `project`, their role comes back through projectRefOf at once — the gate
// parks the task, the model-call gate counts it as a task — and the field
// is there again after that read.
func TestProjectRefRederivedAfterOldBinaryRewrite(t *testing.T) {
	ag, _ := accessFixture(t)
	db := ag.db
	p := &Project{Name: "Web", Slug: "web", Kind: projPersonal, Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, SCM: "apps/scm-github"}
	if err := db.insertProject(p); err != nil {
		t.Fatal(err)
	}
	cfg := defaultConfig()
	task, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true, Stamp: runStamp{Owner: "alice", Visibility: visPrivate,
		TeamRole: roleViewer, Origin: originProject, OriginID: p.ID}})
	if err != nil {
		t.Fatal(err)
	}
	coord, err := ag.startRunOpts(runOpts{Title: "c", Cfg: cfg, Hold: true, Stamp: runStamp{Owner: "alice", Visibility: visPrivate,
		TeamRole: roleViewer, Origin: originProject, OriginID: p.ID, SessionKey: coordSessionKey(p.ID, "alice")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.q.Exec(`INSERT INTO project_tasks (project_id, n, run_id, title, ws) VALUES (?, 1, ?, 't', 'preparing')`, p.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	// the stored configs have no project (what an older build's rewrite leaves)
	if c, _ := db.runConfig(task.ID); c.Project != nil {
		t.Fatal("the fixture's config already has project")
	}
	if !ag.eng.modelGateTop(&Run{ID: 999999, Depth: 0}) || ag.eng.modelGateTop(task) {
		t.Fatal("the model-call gate takes the task for a top-level run")
	}
	if ref := db.projectRefOf(coord); !ref.isCoordinator() || ref.ID != p.ID {
		t.Fatalf("the coordinator's role: %+v", ref)
	}
	for _, id := range []int64{task.ID, coord.ID} {
		c, _ := db.runConfig(id)
		if c.Project == nil || c.Project.ID != p.ID {
			t.Fatalf("run %d: the field wasn't written back: %+v", id, c.Project)
		}
	}
	tc, _ := db.runConfig(task.ID)
	if !tc.Project.isTask() || tc.Project.N != 1 || tc.System != cfg.System {
		t.Fatalf("the task's config after the write-back: %+v", tc)
	}
	// the gate parks it (its workspace is preparing) on a message
	if _, _, err := ag.queue(task.ID, inboxUser, inboxBody{Text: "go", Source: "human", Sender: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the gate's park", func() bool {
		r, _ := db.getRun(task.ID)
		return r.Status == statusSleep && parsePending(r.Pending).Kind == pendKindProject
	})
	if n := len(fakeOf(ag).callsFor(task.ID)); n != 0 {
		t.Fatalf("a parked task called the model %d times", n)
	}
}
