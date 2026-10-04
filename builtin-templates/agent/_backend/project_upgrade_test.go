package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// upFix is a conversation of alice's with the fixture's sandbox bound and
// two clones in it: web (an https origin with a token in it, on a branch,
// one file changed) and api (an ssh origin).
type upFix struct {
	*p2Fix
	run      int64
	web, api string
}

const upToken = "ghp_UPGRADEtokenUPGRADEtokenUPGRADEtoken12"

func newUpFix(t *testing.T) *upFix {
	t.Helper()
	fx := newP2Fix(t)
	fx.scm.addRepo("acme/api", "file:///nowhere/api.git", "main", nil)
	run := codingRunAs(t, fx.ag, alicePrivate, false)
	if code := bindTo(t, fx.mux, asAlice, run, sandboxRef("apps/cs", fx.box.ID), ""); code != 200 {
		t.Fatalf("binding the sandbox: %d", code)
	}
	web := filepath.Join(fx.box.Workdir, "web")
	gitRun(t, fx.box.Workdir, "clone", "-q", fx.odir, web)
	gitRun(t, web, "remote", "set-url", "origin", "https://x-access-token:"+upToken+"@github.com/acme/web.git?x=1#frag")
	gitRun(t, web, "checkout", "-q", "-b", "feature")
	if err := os.WriteFile(filepath.Join(web, "README.md"), []byte("# changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	api := filepath.Join(fx.box.Workdir, "api")
	gitRun(t, fx.box.Workdir, "init", "-q", "-b", "main", api)
	gitRun(t, api, "remote", "add", "origin", "git@github.com:acme/api.git")
	return &upFix{p2Fix: fx, run: run, web: web, api: api}
}

func (fx *upFix) upgrade(t *testing.T, c caller, body map[string]any) (int, string) {
	t.Helper()
	b := map[string]any{"name": "Site", "scm": "apps/scm-github", "repos": []map[string]any{{"path": fx.web, "repo": "acme/web"}}, "branch": "new"}
	for k, v := range body {
		b[k] = v
	}
	w := callAs(t, fx.mux, c, "POST", fmt.Sprintf("/runs/%d/project", fx.run), b)
	return w.Code, w.Body.String()
}

// Detect finds the clones where the conversation works, each with its
// origin (the token, query and fragment dropped — said only as
// hasCredentials), host, repo, provider, default and current branch and
// changed files; an ssh origin is marked.
func TestUpgradeDetect(t *testing.T) {
	fx := newUpFix(t)
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/project/detect", fx.run), nil)
	if w.Code != 200 {
		t.Fatalf("detect: %d %s", w.Code, w.Body)
	}
	if strings.Contains(w.Body.String(), upToken) || strings.Contains(w.Body.String(), "x-access-token") || strings.Contains(w.Body.String(), "frag") {
		t.Fatalf("detect answers the remote's credential: %s", w.Body)
	}
	var out struct {
		Sandbox    string       `json:"sandbox"`
		Cwd        string       `json:"cwd"`
		Candidates []detectCand `json:"candidates"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	by := map[string]detectCand{}
	for _, c := range out.Candidates {
		by[c.Path] = c
	}
	web, api := by[fx.web], by[fx.api]
	if out.Sandbox != sandboxRef("apps/cs", fx.box.ID) || out.Cwd != fx.box.Workdir || len(out.Candidates) != 2 {
		t.Fatalf("detect: %s", w.Body)
	}
	if web.Remote != "https://github.com/acme/web.git" || web.Host != "github.com" || web.Repo != "acme/web" || web.SCM != "apps/scm-github" ||
		web.DefaultBranch != "main" || web.Branch != "feature" || web.Dirty != 1 || web.SSH || !web.HasCredentials {
		t.Fatalf("the https clone: %+v", web)
	}
	if api.Remote != "ssh://github.com/acme/api.git" || api.Repo != "acme/api" || !api.SSH || api.HasCredentials || api.SCM != "apps/scm-github" {
		t.Fatalf("the ssh clone: %+v", api)
	}
	// cwd inside a clone: just that clone
	_, _ = fx.ag.db.q.Exec(`UPDATE runs SET config=json_set(config, '$.sandbox.cwd', ?) WHERE id=?`, fx.web+"/sub", fx.run)
	_ = os.MkdirAll(fx.web+"/sub", 0o755)
	w = callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/project/detect", fx.run), nil)
	if w.Code != 200 || strings.Count(w.Body.String(), `"path"`) != 1 || !strings.Contains(w.Body.String(), `"path":"`+fx.web+`"`) {
		t.Fatalf("detect from inside a clone: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asBob, "GET", fmt.Sprintf("/runs/%d/project/detect", fx.run), nil); w.Code != 404 && w.Code != 403 {
		t.Fatalf("bob detects in alice's conversation: %d", w.Code)
	}
	bare := codingRunAs(t, fx.ag, alicePrivate, false)
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/project/detect", bare), nil); w.Code != 409 {
		t.Fatalf("detect without a sandbox: %d %s", w.Code, w.Body)
	}
}

// The conversation becomes task 1 of a new project: origin project and
// Config.Project, its clone the repo's base (adopted, ready) and its
// checkout (main), on a new branch, its origin switched to the provider's
// https URL (the token gone); the sandbox labelled, a credential written
// for the clone; listed as the project's task; and its next turn is a
// task's. Named after its clone, the project's directory is a new one
// beside it — never the clone, which would hold the project's layout and
// its later tasks' worktrees. A second upgrade under way is refused.
func TestUpgradeAdoptsTask1(t *testing.T) {
	fx := newUpFix(t)
	upgrading.Store(fx.run, true)
	if code, body := fx.upgrade(t, asAlice, nil); code != 409 || !strings.Contains(body, `"refusal":"busy"`) {
		t.Fatalf("an upgrade while one is under way: %d %s", code, body)
	}
	upgrading.Delete(fx.run)
	code, body := fx.upgrade(t, asAlice, map[string]any{"name": "web", "switchHttps": []string{fx.web}})
	if code != 201 {
		t.Fatalf("upgrade: %d %s", code, body)
	}
	var out struct {
		Project ProjectView `json:"project"`
		Task    TaskView    `json:"task"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	p, tv := out.Project, out.Task
	want := "xbin/" + p.UID + "/1-t"
	if p.Name != "web" || p.Owner != "alice" || tv.N != 1 || tv.Run != fx.run || tv.WS != wsReady || tv.Branch != want ||
		len(tv.Checkouts) != 1 || tv.Checkouts[0].Mode != coMain || tv.Checkouts[0].Path != fx.web {
		t.Fatalf("the project and its task 1: %s", body)
	}
	if len(p.Repos) != 1 || p.Repos[0].Mode != repoAdopted || p.Repos[0].BasePath != fx.web || p.Repos[0].State != "ready" || p.Repos[0].Repo != "acme/web" {
		t.Fatalf("the adopted repo: %+v", p.Repos)
	}
	run, _ := fx.ag.db.getRun(fx.run)
	cfg, _ := fx.ag.db.runConfig(fx.run)
	if run.Origin != originProject || run.OriginID != p.ID || !cfg.Project.isTask() || cfg.Project.ID != p.ID || cfg.Project.N != 1 {
		t.Fatalf("the conversation: origin %s/%d project %+v", run.Origin, run.OriginID, cfg.Project)
	}
	if br := gitRun(t, fx.web, "branch", "--show-current"); br != want {
		t.Fatalf("the clone's branch: %s", br)
	}
	if u := gitRun(t, fx.web, "remote", "get-url", "origin"); u != fx.origin {
		t.Fatalf("the clone's origin: %s", u)
	}
	if b, _ := os.ReadFile(filepath.Join(fx.web, "README.md")); string(b) != "# changed\n" {
		t.Fatalf("the clone's work was touched: %q", b)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	if pp, _ := fx.ag.db.getProject(p.ID); pp.Dir != fx.box.Workdir+"/web-2" || pathsClash(pp.Dir, fx.web) || pathsClash(pp.Dir, fx.api) {
		t.Fatalf("the project's directory: %s (the clones %s, %s)", pp.Dir, fx.web, fx.api)
	}
	for _, d := range []string{".repos", "tasks", ".xbin"} {
		if _, err := os.Stat(filepath.Join(fx.web, d)); err == nil {
			t.Fatalf("the project's layout went into the clone: %s", d)
		}
	}
	if b, ok := fx.m.Box(fx.box.ID); !ok || b.Labels[projectLabel] != p.UID {
		t.Fatalf("the sandbox's label: %+v", b.Labels)
	}
	if h := gitRun(t, fx.web, "config", "--get-all", "credential.https://github.com.helper"); !strings.Contains(h, ".config/xbin-scm/"+p.UID) {
		t.Fatalf("the clone's credential helper: %q (jobs %s)", h, jobsDump(fx.ag.db, p.ID))
	}
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/tasks", p.ID), nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), fmt.Sprintf(`"run":%d`, fx.run)) {
		t.Fatalf("the project's tasks: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, fx.mux, asAlice, "GET", "/conversations", nil); strings.Contains(w.Body.String(), fmt.Sprintf(`"id":%d,`, fx.run)) {
		t.Fatalf("task 1 is still among the chats: %s", w.Body)
	}
	fx.turn(t, p.ID, fx.run, "carry on", 1)
	calls := fakeOf(fx.ag).callsFor(fx.run)
	if sys := asString(calls[len(calls)-1].Msgs[0].Content); !strings.Contains(sys, "# Project") || !strings.Contains(sys, want) {
		t.Fatalf("task 1's turn isn't a task's:\n%s", sys)
	}
	// once a project's, never again
	if code, body := fx.upgrade(t, asAlice, nil); code != 409 {
		t.Fatalf("a task made a project again: %d %s", code, body)
	}
}

// The project's directory is never an entry already in the working
// directory: a clone the upgrade doesn't adopt (api) or a person's own
// folder. Named after the clone it doesn't adopt, the project's directory is
// api-2, and nothing of the layout lands in api.
func TestUpgradeNamedAfterOtherClone(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"a", "b", ".c", "..d"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-c", verifyScript)
	cmd.Env = append(os.Environ(), "N=0", "W="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the verify script: %v %s", err, out)
	}
	if ents := parseEnts(string(out)); len(ents) != 4 || !ents["a"] || !ents["b"] || !ents[".c"] || !ents["..d"] {
		t.Fatalf("the working directory's entries: %v (%q)", ents, out)
	}

	fx := newUpFix(t)
	code, body := fx.upgrade(t, asAlice, map[string]any{"name": "api"})
	if code != 201 {
		t.Fatalf("upgrade: %d %s", code, body)
	}
	var res struct {
		Project ProjectView `json:"project"`
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	waitJobsDone(t, fx.projFix, res.Project.ID)
	if p, _ := fx.ag.db.getProject(res.Project.ID); p.Dir != fx.box.Workdir+"/api-2" {
		t.Fatalf("the project's directory: %s (the clone not adopted is %s)", p.Dir, fx.api)
	}
	for _, d := range []string{".repos", "tasks", ".xbin"} {
		if _, err := os.Stat(filepath.Join(fx.api, d)); err == nil {
			t.Fatalf("the project's layout went into the clone not adopted: %s", d)
		}
	}
}

// What an upgrade refuses: a repo the bot rule refuses at a bot home (403);
// a partitioned agent's global (409); a conversation whose class — or the
// policy's taskClass — has internal reach (409 class-internal), or that has
// held internal data, or one the caller may not use (403); a branch kept
// that is the default one, a shared or busy conversation, someone else's,
// an origin that isn't the repo named — and none leaves a project behind.
func TestUpgradeRefusals(t *testing.T) {
	t.Run("global", func(t *testing.T) {
		setMode(t, modeGlobal, "")
		fx := newUpFix(t)
		if code, body := fx.upgrade(t, asAlice, nil); code != 409 {
			t.Fatalf("an upgrade at global: %d %s", code, body)
		}
		if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/project/detect", fx.run), nil); w.Code != 409 {
			t.Fatalf("detect at global: %d", w.Code)
		}
	})
	fx := newUpFix(t)
	mgrOnly := agentClass{ID: "mgronly", Name: "Managers' coding", Toolsets: []string{tsSandbox, tsWeb, tsFiles}, Managers: classSet{All: true},
		SandboxEgress: []string{"internet"}, Who: "managers"}
	inty := agentClass{ID: "inty", Name: "Inside", Toolsets: []string{tsSandbox, tsFiles, tsInternal}, Managers: classSet{All: true}, SandboxEgress: []string{"internet"}}
	classStore.Store(newClassState(classSettings{Classes: []agentClass{mgrOnly, inty}}))
	t.Cleanup(func() { classStore.Store(nil) })
	setCfg := func(expr string, args ...any) {
		t.Helper()
		if _, err := fx.ag.db.q.Exec(`UPDATE runs SET config=`+expr+` WHERE id=?`, append(args, fx.run)...); err != nil {
			t.Fatal(err)
		}
	}
	original, _ := fx.ag.db.runConfig(fx.run)
	restore := func() {
		b, _ := json.Marshal(original)
		setCfg(`?`, string(b))
		_, _ = fx.ag.db.q.Exec(`UPDATE runs SET status='idle', visibility='private' WHERE id=?`, fx.run)
	}
	cases := []struct {
		name    string
		prep    func()
		c       caller
		body    map[string]any
		code    int
		refusal string
	}{
		{"the bot rule", func() { scmBotAllowed = func(w who, _ string) bool { return w.manager() } }, asAlice, nil, 403, ""},
		{"a class with internal reach", func() { setCfg(`json_remove(json_set(config, '$.class', 'inty'), '$.toolset')`) }, asAlice, nil, 409, refusalClassInternal},
		{"internal data held", func() { setCfg(`json_set(config, '$.heldInternal', json('true'))`) }, asAlice, nil, 409, refusalClassInternal},
		{"a managers' class", func() { setCfg(`json_remove(json_set(config, '$.class', 'mgronly'), '$.toolset')`) }, asAlice, nil, 403, ""},
		{"an internal taskClass", nil, asAlice, map[string]any{"policy": map[string]any{"taskClass": "internal"}}, 409, refusalClassInternal},
		{"a managers' taskClass", nil, asAlice, map[string]any{"policy": map[string]any{"taskClass": "mgronly"}}, 403, ""},
		{"the default branch kept", func() { gitRun(t, fx.web, "checkout", "-q", "main") }, asAlice, map[string]any{"branch": "keep"}, 409, ""},
		{"a shared conversation", func() { _, _ = fx.ag.db.q.Exec(`UPDATE runs SET visibility='team' WHERE id=?`, fx.run) }, asAlice, nil, 409, ""},
		{"a busy conversation", func() { _, _ = fx.ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, fx.run) }, asAlice, nil, 409, refusalBusy},
		{"someone else", nil, asBob, nil, 404, ""},
		{"an origin that isn't the repo", nil, asAlice, map[string]any{"repos": []map[string]any{{"path": fx.web, "repo": "acme/api"}}}, 400, ""},
		{"a path that isn't a clone's top", nil, asAlice, map[string]any{"repos": []map[string]any{{"path": fx.box.Workdir, "repo": "acme/web"}}}, 400, ""},
		{"an upgrade under way", func() { upgrading.Store(fx.run, true) }, asAlice, nil, 409, refusalBusy},
		{"a clone holding the working directory", func() {
			gitRun(t, fx.box.Workdir, "init", "-q", "-b", "main")
			gitRun(t, fx.box.Workdir, "remote", "add", "origin", "https://github.com/acme/web.git")
		}, asAlice, map[string]any{"repos": []map[string]any{{"path": fx.box.Workdir, "repo": "acme/web"}}}, 409, ""},
	}
	oldBot := scmBotAllowed
	for _, tc := range cases {
		scmBotAllowed = oldBot
		upgrading.Delete(fx.run)
		restore()
		gitRun(t, fx.web, "checkout", "-q", "feature")
		fx.ag.acl.flush(fx.run)
		if tc.prep != nil {
			tc.prep()
		}
		fx.ag.acl.flush(fx.run)
		code, body := fx.upgrade(t, tc.c, tc.body)
		t.Logf("%s: %d %s", tc.name, code, body)
		if code != tc.code && !(tc.code == 404 && code == 403) {
			t.Errorf("%s: %d %s", tc.name, code, body)
		}
		if tc.refusal != "" && !strings.Contains(body, `"refusal":"`+tc.refusal+`"`) {
			t.Errorf("%s: no refusal %s: %s", tc.name, tc.refusal, body)
		}
		var n int
		_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
		if n != 0 {
			t.Fatalf("%s left a project behind", tc.name)
		}
		if run, _ := fx.ag.db.getRun(fx.run); run.Origin == originProject {
			t.Fatalf("%s: the conversation became a task", tc.name)
		}
	}
	scmBotAllowed = oldBot
	upgrading.Delete(fx.run)
	_ = os.RemoveAll(filepath.Join(fx.box.Workdir, ".git"))
	restore()
	gitRun(t, fx.web, "checkout", "-q", "feature")
	code, body := fx.upgrade(t, asAlice, map[string]any{"branch": "keep"})
	if code != 201 || !strings.Contains(body, `"branch":"feature"`) {
		t.Fatalf("an upgrade keeping its branch: %d %s", code, body)
	}
	var out struct{ Project ProjectView }
	_ = json.Unmarshal([]byte(body), &out)
	waitJobsDone(t, fx.projFix, out.Project.ID)
}

// Cleaning up an upgraded task never removes its clone: the checkout is
// kept (mode main), the clone and its work stay, forced or not.
func TestUpgradeNeverRemovesMain(t *testing.T) {
	fx := newUpFix(t)
	code, body := fx.upgrade(t, asAlice, nil)
	if code != 201 {
		t.Fatalf("upgrade: %d %s", code, body)
	}
	var out struct{ Project ProjectView }
	_ = json.Unmarshal([]byte(body), &out)
	waitJobsDone(t, fx.projFix, out.Project.ID)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", fx.run), map[string]any{"force": true}); w.Code != 202 {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	k := fx.waitWS(t, out.Project.ID, 1, wsCleaned)
	waitJobsDone(t, fx.projFix, out.Project.ID)
	if b, err := os.ReadFile(filepath.Join(fx.web, "README.md")); err != nil || string(b) != "# changed\n" {
		t.Fatalf("the clone after cleanup: %v %q", err, b)
	}
	if gitRun(t, fx.web, "rev-parse", "--is-inside-work-tree") != "true" {
		t.Fatalf("the clone is no longer a clone")
	}
	cos := fx.ag.db.checkouts(k.ID)
	if len(cos) != 1 || cos[0].State != "kept" || cos[0].Mode != coMain {
		t.Fatalf("the checkout after cleanup: %+v", cos)
	}
}
