package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The projects store's tables a credential test reads (P1's, as the spec
// freezes them): members (the person gate), tasks, checkouts and jobs (the
// task's workspace state). Made here only for the tests; the projects
// store makes them in a build that has it.
const scmTestProjectTables = `
CREATE TABLE IF NOT EXISTS project_members (project_id INTEGER NOT NULL, user TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'participant',
  added_by TEXT NOT NULL DEFAULT '', created_ms INTEGER NOT NULL DEFAULT 0, PRIMARY KEY (project_id, user));
CREATE TABLE IF NOT EXISTS project_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL, n INTEGER NOT NULL,
  run_id INTEGER NOT NULL DEFAULT 0, title TEXT NOT NULL DEFAULT '', slug TEXT NOT NULL DEFAULT '', size TEXT NOT NULL DEFAULT 'small',
  branch TEXT NOT NULL DEFAULT '', issue TEXT NOT NULL DEFAULT '', repos TEXT NOT NULL DEFAULT '[]', sandbox_ref TEXT NOT NULL DEFAULT '',
  fork_made INTEGER NOT NULL DEFAULT 0, dir TEXT NOT NULL DEFAULT '', ports_base INTEGER NOT NULL DEFAULT 0, ws TEXT NOT NULL DEFAULT 'pending',
  phase TEXT NOT NULL DEFAULT 'open', prs TEXT NOT NULL DEFAULT '[]', turn_by TEXT NOT NULL DEFAULT '', last TEXT NOT NULL DEFAULT '',
  setup_tail TEXT NOT NULL DEFAULT '', ci_fixes TEXT NOT NULL DEFAULT '{}', error TEXT NOT NULL DEFAULT '', from_run INTEGER NOT NULL DEFAULT 0,
  created_by TEXT NOT NULL DEFAULT '', created_ms INTEGER NOT NULL DEFAULT 0, updated_ms INTEGER NOT NULL DEFAULT 0, cleaned_ms INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS project_checkouts (task_id INTEGER NOT NULL, repo_slug TEXT NOT NULL, path TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'worktree', state TEXT NOT NULL DEFAULT 'pending', setup_exit INTEGER, error TEXT NOT NULL DEFAULT '',
  remote_sha TEXT NOT NULL DEFAULT '', PRIMARY KEY (task_id, repo_slug));
CREATE TABLE IF NOT EXISTS project_jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, project_id INTEGER NOT NULL, task_id INTEGER NOT NULL DEFAULT 0,
  repo_slug TEXT NOT NULL DEFAULT '', kind TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'queued', step TEXT NOT NULL DEFAULT '',
  attempts INTEGER NOT NULL DEFAULT 0, next_ms INTEGER NOT NULL DEFAULT 0, exec_ref TEXT NOT NULL DEFAULT '', exec_id TEXT NOT NULL DEFAULT '',
  client_id TEXT NOT NULL DEFAULT '', out TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', by_user TEXT NOT NULL DEFAULT '',
  epoch INTEGER NOT NULL DEFAULT 0, created_ms INTEGER NOT NULL DEFAULT 0, updated_ms INTEGER NOT NULL DEFAULT 0);
`

// credFx is a project in a sandbox with an scm provider bound: alice's
// partition (or, global, the agent's global instance), a sandbox manager
// homing what it makes there, the fake provider, and the projects store's
// seams answering for one project.
type credFx struct {
	ag     *Agent
	h      http.Handler
	m      *sbxTestManager
	scm    *fakeSCM
	box    *sbxSandbox
	ref    string
	p      *Project
	repos  []ProjectRepo
	levels map[string]level
	global *atomic.Bool
}

// credFixture: in alice's partition (mode user), else the global instance.
func credFixture(t *testing.T, mode agentMode) *credFx {
	t.Helper()
	fx := &credFx{levels: map[string]level{}, global: &atomic.Bool{}}
	user := ""
	if mode == modeUser {
		user = "alice"
	}
	setMode(t, mode, user)
	kv := newMemKV()
	confIn = newConfReader(kv, nil)
	putConf(kv, "", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	confIn.refresh()
	fx.ag, fx.h = homeAgent(t)
	if _, err := fx.ag.db.q.Exec(scmTestProjectTables); err != nil {
		t.Fatal(err)
	}
	if mode == modeUser {
		pidMu.Lock()
		pidSeen = alicePID
		pidMu.Unlock()
		fx.m = bindSbxWith(t, partitionManager("alice", alicePID, fx.global), "apps/cs")["apps/cs"]
	} else {
		fx.m = bindSbx(t, "apps/cs")["apps/cs"]
	}
	fx.scm = newFakeSCM(t, "apps/scm-github")
	bindSCM(t, fx.scm)
	fx.scm.AddRepo("acme/web", true)
	fx.scm.SetSignedIn("octocat", 583231)
	fx.box = mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "work"})
	fx.ref = sandboxRef("apps/cs", fx.box.ID)
	kind := projPersonal
	pid := int64(1)
	if mode == modeUser {
		pid = partitionIDBase + 1
	}
	fx.p = &Project{ID: pid, UID: "k3x9qa", Name: "Web", Slug: "web", Kind: kind, Owner: "alice", Visibility: visPrivate,
		TeamRole: roleViewer, SCM: "apps/scm-github", Host: "github.com", SandboxRef: fx.ref, Dir: fx.box.Workdir + "/web",
		Policy: json.RawMessage(`{}`), State: projActive, Version: 1}
	if mode != modeUser {
		fx.p.Policy = json.RawMessage(`{"as":"bot"}`)
	}
	fx.repos = []ProjectRepo{{Slug: "web", Repo: "acme/web", Mode: repoBare, State: "ready", BasePath: fx.p.Dir + "/.repos/web.git"}}
	oldIn, oldRepos, oldLevel := projectsInSandbox, projectReposOf, projectLevelOf
	projectsInSandbox = func(ref string) []*Project {
		if ref == fx.p.SandboxRef {
			return []*Project{fx.p}
		}
		return nil
	}
	projectReposOf = func(pid int64) []ProjectRepo {
		if pid == fx.p.ID {
			return fx.repos
		}
		return nil
	}
	projectLevelOf = func(p *Project, user string) level {
		if user == p.Owner {
			return lvOwner
		}
		return fx.levels[user]
	}
	t.Cleanup(func() {
		projectsInSandbox, projectReposOf, projectLevelOf = oldIn, oldRepos, oldLevel
		scmLiveMu.Lock()
		scmLives, scmRetired = map[string]*scmLive{}, map[string]int64{}
		scmPriors.m = map[string]string{}
		scmRebuildLocked()
		scmLiveMu.Unlock()
		scmSigninMu.Lock()
		scmSignins = map[string]*scmSignin{}
		scmSigninMu.Unlock()
	})
	return fx
}

// hostPath is a sandbox path on the host (the fake manager's sandboxes are
// host directories, their paths the same inside and out).
func (fx *credFx) credFile(name string) string {
	return filepath.Join(fx.box.Home, ".config", "xbin-scm", fx.p.UID, name)
}

func (fx *credFx) ensure(t *testing.T) {
	t.Helper()
	if err := ensureCreds(context.Background(), fx.p, nil, "", scmMinLeft); err != nil {
		t.Fatalf("ensure: %v", err)
	}
}

// task makes task n of the project, its run in status st.
func (fx *credFx) task(t *testing.T, n int64, st string) int64 {
	t.Helper()
	run := newRun(t, fx.ag, Config{}, "task")
	waitQuiet(t, fx.ag)
	if err := fx.ag.db.setStatus(run, st, 0, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.ag.db.q.Exec(`INSERT INTO project_tasks (project_id, n, run_id, ws) VALUES (?, ?, ?, 'ready')`, fx.p.ID, n, run); err != nil {
		t.Fatal(err)
	}
	return run
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func credRowOf(t *testing.T, fx *credFx) scmCredRow {
	t.Helper()
	rows := fx.ag.db.scmCredsOf(fx.p.ID, fx.ref)
	if len(rows) != 1 {
		t.Fatalf("project_creds: %+v", rows)
	}
	return rows[0]
}

// The files are 0600 in 0700 directories outside every repo, git's
// credential format and gh's hosts.yml; the row holds metadata, never the
// token; GH_CONFIG_DIR points at the project's own gh directory.
func TestCredFilesMode0600(t *testing.T) {
	fx := credFixture(t, modeUser)
	fx.ensure(t)
	toks := fx.scm.Tokens()
	if len(toks) != 1 || toks[0].As != scmAsPerson || toks[0].Purpose != "proj:k3x9qa:"+fx.ref || toks[0].Repos[0] != "acme/web" {
		t.Fatalf("tokens: %+v", toks)
	}
	tok := toks[0].Value
	for _, f := range []string{"github.com.cred", "gh/hosts.yml"} {
		fi, err := os.Stat(fx.credFile(f))
		if err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v %v", f, fi.Mode(), err)
		}
	}
	for _, d := range []string{filepath.Dir(fx.credFile("x")), filepath.Dir(fx.credFile("gh/x")), filepath.Join(fx.box.Home, ".config", "xbin-scm")} {
		if fi, err := os.Stat(d); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("%s: %v %v", d, fi.Mode(), err)
		}
	}
	if got := readFile(t, fx.credFile("github.com.cred")); got != "username=x-access-token\npassword="+tok+"\n" {
		t.Fatalf("cred file: %q", got)
	}
	if got := readFile(t, fx.credFile("gh/hosts.yml")); got != "github.com:\n    oauth_token: '"+tok+"'\n    user: 'octocat'\n    git_protocol: https\n" {
		t.Fatalf("hosts.yml: %q", got)
	}
	if strings.HasPrefix(fx.credFile(""), fx.box.Workdir) {
		t.Fatal("credentials under the workdir")
	}
	row := credRowOf(t, fx)
	if row.State != credLive || row.Identity != scmAsPerson || row.Login != "octocat" || row.ForUser != "alice" || row.Expires == 0 || row.Refresh >= row.Expires {
		t.Fatalf("row: %+v", row)
	}
	var n int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_creds WHERE instr(purpose||login||why, ?) > 0`, tok).Scan(&n)
	if n != 0 {
		t.Fatal("the token is in project_creds")
	}
	env := scmProjectEnv(fx.p, fx.box.Home)
	if env["GH_CONFIG_DIR"] != filepath.Dir(fx.credFile("gh/x")) || len(env) != 1 {
		t.Fatalf("env: %v", env)
	}
	if got := readFile(t, fx.p.Dir+"/.xbin/env"); got != "GH_CONFIG_DIR="+env["GH_CONFIG_DIR"]+"\n" {
		t.Fatalf(".xbin/env: %q", got)
	}
	// a second ensure with time left mints nothing
	fx.ensure(t)
	if len(fx.scm.Requests("POST /token")) != 1 {
		t.Fatal("a live credential was minted again")
	}
}

// git's own `credential fill`, through the helper line the base repo's
// config holds, answers the token — and a global helper is reset.
func TestGitCredentialFill(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	fx := credFixture(t, modeUser)
	base := fx.repos[0].BasePath
	git := func(dir string, stdin string, args ...string) string {
		c := exec.Command("git", append([]string{"-C", dir}, args...)...) // exec-ok: a test, on the fake sandbox's host directory
		c.Env = append(os.Environ(), "HOME="+fx.box.Home, "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1")
		c.Stdin = strings.NewReader(stdin)
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	git(base, "", "init", "-q", "--bare")
	// a person's own helper in their global config must not answer for the project
	if err := os.WriteFile(filepath.Join(fx.box.Home, ".gitconfig"), []byte("[credential]\n\thelper = !echo password=WRONG #\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fx.ensure(t)
	tok := fx.scm.Tokens()[0].Value
	out := git(base, "protocol=https\nhost=github.com\npath=acme/web.git\n\n", "credential", "fill")
	if !strings.Contains(out, "username=x-access-token\n") || !strings.Contains(out, "password="+tok+"\n") || strings.Contains(out, "WRONG") {
		t.Fatalf("credential fill: %q", out)
	}
	if got := git(base, "", "config", "--get-all", "credential.https://github.com.helper"); !strings.HasPrefix(got, "\n!f()") {
		t.Fatalf("helpers: %q", got)
	}
	if got := strings.TrimSpace(git(base, "", "config", "user.email")); got != "583231+octocat@users.noreply.github.com" {
		t.Fatalf("user.email: %q", got)
	}
	// applied twice, the lines don't pile up
	fx.p.Policy = json.RawMessage(`{"workflows":true}`) // another token request
	fx.ensure(t)
	if got := git(base, "", "config", "--get-all", "credential.https://github.com.helper"); strings.Count(got, "\n") != 2 {
		t.Fatalf("helpers after a second write: %q", got)
	}
}

// The refresher re-mints a token at its refreshAfter (or 75 % of its life)
// and rewrites both files; the old value stays masked until it expires.
func TestRefreshRewritesFile(t *testing.T) {
	fx := credFixture(t, modeUser)
	fx.scm.NoCache = true
	fx.ensure(t)
	first := fx.scm.Tokens()[0].Value
	scmRefreshDue(context.Background())
	if len(fx.scm.Tokens()) != 1 {
		t.Fatal("a token not due was refreshed")
	}
	fx.task(t, 1, statusWaiting) // a task at work keeps it fresh (an idle project's lapses)
	scmLiveMu.Lock()
	for _, l := range scmLives {
		l.refresh = nowMs() - 1 // the provider's refreshAfter has come
	}
	scmLiveMu.Unlock()
	scmRefreshDue(context.Background())
	toks := fx.scm.Tokens()
	if len(toks) != 2 {
		t.Fatalf("tokens after the refresh: %d", len(toks))
	}
	if got := readFile(t, fx.credFile("github.com.cred")); !strings.Contains(got, toks[1].Value) {
		t.Fatalf("the file wasn't rewritten: %q", got)
	}
	if row := credRowOf(t, fx); row.Expires != toks[1].Expires {
		t.Fatalf("row: %+v", row)
	}
	if redactText("x "+first+" y") == "x "+first+" y" {
		t.Fatal("the replaced token is no longer masked")
	}
	// 75 % of a short life comes before refreshAfter
	scmLiveMu.Lock()
	for _, l := range scmLives {
		l.refresh, l.written, l.expires = nowMs()+time.Hour.Milliseconds(), nowMs()-30*60*1000, nowMs()+5*60*1000
	}
	scmLiveMu.Unlock()
	scmRefreshDue(context.Background())
	if len(fx.scm.Tokens()) != 3 {
		t.Fatal("a token at 75 % of its life wasn't refreshed")
	}
}

// scmCredsDue reads project_creds alone: no row, not live, another
// identity, or a refresh within 10 minutes is due; a project without repos
// (or archived) never is.
func TestCredsDue(t *testing.T) {
	fx := credFixture(t, modeUser)
	db := fx.ag.db
	if !scmCredsDue(db, fx.p, nil) {
		t.Fatal("no credential yet: due")
	}
	fx.ensure(t)
	if scmCredsDue(db, fx.p, nil) {
		t.Fatal("a fresh credential isn't due")
	}
	k := &ProjectTask{ProjectID: fx.p.ID, N: 3, SandboxRef: "apps/cs|fork-3"}
	if !scmCredsDue(db, fx.p, k) {
		t.Fatal("a task's own fork without one: due")
	}
	set := func(q string) {
		if _, err := db.q.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	set(`UPDATE project_creds SET refresh_ms = ` + itoa(nowMs()+5*60*1000))
	if !scmCredsDue(db, fx.p, nil) {
		t.Fatal("refresh within 10 min: due")
	}
	set(`UPDATE project_creds SET refresh_ms = ` + itoa(nowMs()+50*60*1000) + `, identity='bot'`)
	if !scmCredsDue(db, fx.p, nil) {
		t.Fatal("another identity: due")
	}
	set(`UPDATE project_creds SET identity='person', state='scrubbed'`)
	if !scmCredsDue(db, fx.p, nil) {
		t.Fatal("scrubbed: due")
	}
	set(`UPDATE project_creds SET state='live'`)
	if scmCredsDue(db, fx.p, nil) {
		t.Fatal("live again: not due")
	}
	fx.repos = nil
	set(`DELETE FROM project_creds`)
	if scmCredsDue(db, fx.p, nil) {
		t.Fatal("no repos: never due")
	}
	fx.repos = []ProjectRepo{{Slug: "web", Repo: "acme/web"}}
	fx.p.State = projArchived
	if scmCredsDue(db, fx.p, nil) {
		t.Fatal("archived: never due")
	}
}

// project_creds is made by openDB's feature schemas: twice changes
// nothing, and a database from before it gains it with its rows intact.
func TestSCMCredsSchemaMigratesTwice(t *testing.T) {
	path := seedOldDB(t, oldHarnessRows)
	for i := 0; i < 2; i++ {
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		if i == 0 {
			if err := db.scmPutCred(scmCredRow{PID: 7, Ref: "apps/cs|b", Host: "github.com", Identity: "bot", State: credLive}); err != nil {
				t.Fatal(err)
			}
		}
		rows := db.scmCredsOf(7, "apps/cs|b")
		if len(rows) != 1 || rows[0].Identity != "bot" {
			t.Fatalf("open %d: %+v", i, rows)
		}
		var runs int
		_ = db.q.QueryRow(`SELECT count(*) FROM runs`).Scan(&runs)
		if runs == 0 {
			t.Fatalf("open %d: the old database's runs are gone", i)
		}
		db.sql.Close()
	}
}
