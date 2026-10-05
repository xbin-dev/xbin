package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// p1SCM is the scm provider of Projects' own tests: repos, issues and pull
// requests in memory as the test sets them (the reads Projects' logic is
// about), swapped in through scmFor/scmBound — and its credentials (token,
// revoke, sign-in, forget) are the scm client's own against the fake
// provider (scm_fake_test.go), so a project's credential goes through the
// real gate, minting, files and redaction like any other.
type p1SCM struct {
	prov   *fakeSCM // the provider behind the credentials (its tokens, its sign-in)
	creds  scmAPI   // the scm client, bound to it
	mu     sync.Mutex
	name   string
	hosts  []string
	repos  map[string]*scmRepo
	issues map[string]map[int]*scmIssue
	pulls  map[string][]scmPull
	calls  []string
	refuse error // every read answers it (a sign-in, say)
}

func newP1SCM(t *testing.T) *p1SCM {
	f := &p1SCM{name: "apps/scm-github", hosts: []string{"github.com"}, repos: map[string]*scmRepo{},
		issues: map[string]map[int]*scmIssue{}, pulls: map[string][]scmPull{}}
	f.prov = newFakeSCM(t, f.name)
	bindSCM(t, f.prov)
	scmForgetState(t)
	creds, err := scmFor(f.name)
	if err != nil {
		t.Fatalf("the scm client for the fake provider: %v", err)
	}
	f.creds = creds
	oldFor, oldBound := scmFor, scmBound
	scmFor = func(provider string) (scmAPI, error) {
		if provider != f.name {
			return nil, errScmUnbound
		}
		return f, nil
	}
	scmBound = func() []string { return []string{f.name} }
	t.Cleanup(func() { ciBG.Wait(); scmFor, scmBound = oldFor, oldBound })
	return f
}

func (f *p1SCM) note(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}

func (f *p1SCM) addRepo(repo, url, def string, protected *bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	o, n, _ := strings.Cut(repo, "/")
	f.repos[strings.ToLower(repo)] = &scmRepo{Host: "github.com", Owner: o, Name: n, CloneURL: url, DefaultBranch: def, Protected: protected}
}

func (f *p1SCM) addIssue(repo string, is scmIssue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m := f.issues[strings.ToLower(repo)]
	if m == nil {
		m = map[int]*scmIssue{}
		f.issues[strings.ToLower(repo)] = m
	}
	m[is.Number] = &is
}

func (f *p1SCM) addPull(repo string, p scmPull) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulls[strings.ToLower(repo)] = append(f.pulls[strings.ToLower(repo)], p)
}

func (f *p1SCM) Provider() string { return f.name }
func (f *p1SCM) Hello(context.Context) (*scmHello, error) {
	return &scmHello{Protocol: 1, Hosts: f.hosts, Identities: []string{scmAsPerson, scmAsBot},
		You: scmYou{Identities: []string{scmAsPerson, scmAsBot}, Default: scmAsBot}}, nil
}
func (f *p1SCM) unsupported() error {
	return &scmError{Status: 501, Refusal: scmRefUnsupported, Message: "not in the test's fake"}
}
func (f *p1SCM) Token(ctx context.Context, req scmTokenReq) (*scmToken, error) {
	return f.creds.Token(ctx, req)
}
func (f *p1SCM) Revoke(ctx context.Context, req scmRevokeReq) error { return f.creds.Revoke(ctx, req) }
func (f *p1SCM) Signin(ctx context.Context) (*scmSigninState, error) {
	return f.creds.Signin(ctx)
}
func (f *p1SCM) SigninState(ctx context.Context) (*scmSigninState, error) {
	return f.creds.SigninState(ctx)
}
func (f *p1SCM) SigninPoll(ctx context.Context, id string) (*scmSigninState, error) {
	return f.creds.SigninPoll(ctx, id)
}
func (f *p1SCM) Forget(ctx context.Context) error { return f.creds.Forget(ctx) }
func (f *p1SCM) Repos(context.Context, scmQuery) (*scmPage[scmRepo], error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Repo(_ context.Context, repo, as string) (*scmRepo, error) {
	f.note("repo " + repo + " as " + as)
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.repos[strings.ToLower(repo)]
	if r == nil {
		return nil, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such repo"}
	}
	cp := *r
	return &cp, nil
}
func (f *p1SCM) PullCreate(context.Context, scmPullReq) (*scmPull, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Pulls(_ context.Context, q scmQuery) (*scmPage[scmPull], error) {
	f.note("pulls " + q.Repo + " head " + q.Head)
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &scmPage[scmPull]{Items: []scmPull{}}
	for _, p := range f.pulls[strings.ToLower(q.Repo)] {
		if (q.Head == "" || p.Head.Ref == q.Head) && (q.State == "" || q.State == "all" || p.State == q.State) {
			out.Items = append(out.Items, p)
		}
	}
	return out, nil
}
func (f *p1SCM) Pull(context.Context, string, int, string) (*scmPull, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) PullPatch(_ context.Context, n int, p scmPullPatch) (*scmPull, error) {
	f.note(fmt.Sprintf("pull-patch %s %d", p.Repo, n))
	return nil, nil
}
func (f *p1SCM) Comments(context.Context, string, int, int64, string) (*scmPage[scmComment], error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Checks(context.Context, string, string, string, string) (*scmChecks, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) JobLog(context.Context, string, string, int, int64, int64, string) (*scmJobLog, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Annotations(context.Context, string, string, string, string) (*scmPage[scmAnnotation], error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Rerun(context.Context, scmRerunReq) (*scmRerun, error) { return nil, f.unsupported() }
func (f *p1SCM) Issues(_ context.Context, q scmQuery) (*scmPage[scmIssue], error) {
	f.note("issues " + q.Repo)
	f.mu.Lock()
	defer f.mu.Unlock()
	out := &scmPage[scmIssue]{Items: []scmIssue{}}
	for _, is := range f.issues[strings.ToLower(q.Repo)] {
		out.Items = append(out.Items, *is)
	}
	return out, nil
}
func (f *p1SCM) Issue(_ context.Context, repo string, n int, _ bool, as string) (*scmIssue, error) {
	f.note(fmt.Sprintf("issue %s#%d as %s", repo, n, as))
	if f.refuse != nil {
		return nil, f.refuse
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if is := f.issues[strings.ToLower(repo)][n]; is != nil {
		cp := *is
		return &cp, nil
	}
	return nil, &scmError{Status: 404, Refusal: scmRefNotFound, Message: "no such issue"}
}
func (f *p1SCM) Poll(context.Context, scmPollReq) (*scmPollResp, error) { return nil, f.unsupported() }
func (f *p1SCM) Subscribe(context.Context, scmSubscription) (*scmSubscription, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Subscriptions(context.Context) ([]scmSubscription, error) {
	return nil, f.unsupported()
}
func (f *p1SCM) Unsubscribe(context.Context, string) error { return nil }

// --- git fixtures -----------------------------------------------------------------------------

// gitRun runs git on the host for a fixture (the fake manager's sandboxes
// are host directories too).
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...) // exec-ok: a test fixture (a local origin)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=t@example.com", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// bareOrigin makes a bare repo with one commit on main (its file:// URL).
func bareOrigin(t *testing.T) (url, dir string) {
	t.Helper()
	root := t.TempDir()
	dir = filepath.Join(root, "origin.git")
	gitRun(t, root, "init", "-q", "--bare", "-b", "main", dir)
	work := filepath.Join(root, "seed")
	gitRun(t, root, "clone", "-q", dir, work)
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("# web\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "commit", "-q", "-m", "first")
	gitRun(t, work, "push", "-q", "origin", "HEAD:main")
	return "file://" + dir, dir
}

// --- the project fixture ----------------------------------------------------------------------

// projFix is an agent with a fake sandbox manager (alice's sandbox there),
// the in-memory scm provider with acme/web at a local bare origin, and the
// scm bot rule letting anyone name repos (the bot rule has its own test).
type projFix struct {
	ag     *Agent
	mux    *http.ServeMux
	m      *sbxTestManager
	scm    *p1SCM
	box    *sbxSandbox
	origin string
	odir   string
}

func newProjFix(t *testing.T) *projFix {
	t.Helper()
	ag, mux := accessFixture(t)
	m := bindSbx(t, "apps/cs")["apps/cs"]
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Egress: "internet"})
	f := newP1SCM(t)
	url, dir := bareOrigin(t)
	f.addRepo("acme/web", url, "main", nil)
	oldBot := scmBotAllowed
	scmBotAllowed = func(who, string) bool { return true }
	oldDelay := projStreamDelay.Load()
	projStreamDelay.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { scmBotAllowed = oldBot; projStreamDelay.Store(oldDelay) })
	stopEngineFirst(t, ag)
	return &projFix{ag: ag, mux: mux, m: m, scm: f, box: box, origin: url, odir: dir}
}

// stopEngineFirst stops ag's engine — its actors, project worker, jobs and
// owner loops joined — before the cleanups registered so far put the seams
// they read back (cleanups run last-first; newTestAgent's own stop comes
// after them, too late). Call it once the fixture's seams are swapped.
func stopEngineFirst(t *testing.T, ag *Agent) {
	t.Helper()
	t.Cleanup(func() { ag.eng.Shutdown(2 * time.Second) })
}

// newProject creates a project as c over the fixture's sandbox; the body
// may add or override keys.
func (fx *projFix) newProject(t *testing.T, c caller, more map[string]any) ProjectView {
	t.Helper()
	body := map[string]any{"name": "Web", "scm": "apps/scm-github", "repos": []map[string]any{{"repo": "acme/web"}},
		"sandbox": map[string]any{"ref": sandboxRef("apps/cs", fx.box.ID)}}
	for k, v := range more {
		body[k] = v
	}
	w := callAs(t, fx.mux, c, "POST", "/projects", body)
	if w.Code != 201 {
		t.Fatalf("POST /projects: %d %s", w.Code, w.Body)
	}
	var out struct {
		Project ProjectView `json:"project"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Project
}

// newTask creates a task in project pid as c.
func (fx *projFix) newTask(t *testing.T, c caller, pid int64, spec map[string]any) (TaskView, int64) {
	t.Helper()
	w := callAs(t, fx.mux, c, "POST", fmt.Sprintf("/projects/%d/tasks", pid), spec)
	if w.Code != 201 {
		t.Fatalf("POST tasks: %d %s", w.Code, w.Body)
	}
	var out struct {
		Task TaskView `json:"task"`
		Run  Run      `json:"run"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Task, out.Run.ID
}

// waitRepoReady waits for every repo of pid to be cloned.
func (fx *projFix) waitRepoReady(t *testing.T, pid int64) {
	t.Helper()
	hwait(t, "the project's repos to be ready", func() bool {
		rs, _ := fx.ag.db.projectRepos(pid)
		for _, r := range rs {
			if r.State == "failed" {
				t.Fatalf("repo %s failed: %s (jobs %s)", r.Repo, r.Error, jobsDump(fx.ag.db, pid))
			}
			if r.State != "ready" {
				return false
			}
		}
		return len(rs) > 0
	})
}

// waitWS waits for task n's workspace state.
func (fx *projFix) waitWS(t *testing.T, pid, n int64, ws string) *ProjectTask {
	t.Helper()
	var k *ProjectTask
	hwait(t, "task workspace "+ws, func() bool {
		k, _ = fx.ag.db.taskByN(pid, n)
		if k != nil && k.WS == wsFailed && ws != wsFailed {
			t.Fatalf("the task's workspace failed: %s (jobs %s)", k.Error, jobsDump(fx.ag.db, pid))
		}
		return k != nil && k.WS == ws
	})
	return k
}

func jobsDump(d *DB, pid int64) string {
	var b strings.Builder
	for _, j := range d.jobsWhere(`WHERE project_id=? ORDER BY id`, pid) {
		fmt.Fprintf(&b, "\n  #%d %s task=%d repo=%s %s step=%q err=%q out=%q", j.ID, j.Kind, j.Task, j.Repo, j.State, j.Step, j.Error, clip(j.Out, 300))
	}
	return b.String()
}
