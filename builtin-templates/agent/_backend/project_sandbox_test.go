package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// forkCreate is the manager's create call with clientId cid (its body).
func (fx *p2Fix) forkCreate(t *testing.T, cid string) map[string]any {
	t.Helper()
	for _, c := range fx.managerCalls("POST", "/sbx/sandboxes") {
		var b map[string]any
		if json.Unmarshal([]byte(c.Body), &b) == nil && b["clientId"] == cid {
			return b
		}
	}
	t.Fatalf("no sandbox created with clientId %s", cid)
	return nil
}

// A big task gets its own sandbox, cloned from the fork base: the
// project's labels and the task's, the primary's visibility; in it the
// other tasks' checkouts are gone (and git forgot them), no credential
// came along — the fork has its own, minted for it — and the bases are
// fetched (a commit pushed after the snapshot is the task's base). Its
// cleanup deletes the fork, its credential scrubbed first.
func TestForkFlow(t *testing.T) {
	fx := newP2Fix(t)
	p, k1, _ := readyTask(t, fx.projFix, nil)
	d := fx.ag.db
	// another project's credential in the sandbox (one the scrub before the
	// snapshot doesn't know): no fork carries it
	other := filepath.Join(fx.box.Home, ".config", "xbin-scm", "zz9zz9")
	if err := os.MkdirAll(other, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(other, "github.com.cred"), []byte("protocol=https\npassword=someone-elses\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{"now": true}); w.Code != 202 {
		t.Fatalf("POST fork-base: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	pp, _ := d.getProject(p.ID)
	if pp.ForkSnap == "" {
		t.Fatalf("no fork base: %s", jobsDump(d, p.ID))
	}
	after := fx.pushOrigin(t, "after.txt", "pushed after the snapshot\n")

	tv, run2 := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	k2 := fx.waitWS(t, p.ID, tv.N, wsReady)
	if !k2.ForkMade || k2.SandboxRef == "" || k2.SandboxRef == pp.SandboxRef {
		t.Fatalf("the big task's sandbox: %+v", k2)
	}
	_, forkID, _ := splitSandboxRef(k2.SandboxRef)
	_, srcID, _ := splitSandboxRef(pp.SandboxRef)
	body := fx.forkCreate(t, fmt.Sprintf("agent:proj:%d:fork:%d", p.ID, tv.N))
	from, _ := body["from"].(map[string]any)
	labels, _ := body["labels"].(map[string]any)
	if from["sandbox"] != srcID || from["snapshot"] != pp.ForkSnap || labels[projectLabel] != pp.UID || labels[taskLabel] != fmt.Sprint(tv.N) ||
		body["visibility"] != fx.box.Visibility {
		t.Fatalf("the fork's create: %v", body)
	}
	if fb, ok := fx.m.Box(forkID); !ok || fb.Labels[taskLabel] != fmt.Sprint(tv.N) {
		t.Fatalf("the fork at its manager: %+v %v", fb, ok)
	}

	// in the fork: only this task's checkout; git forgot the others'
	ents, err := os.ReadDir(fx.realDir(forkID, pp.Dir+"/tasks"))
	if err != nil || len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), fmt.Sprintf("%d-", tv.N)) {
		t.Fatalf("the fork's tasks: %v %v", ents, err)
	}
	base := fx.realDir(forkID, pp.Dir+"/.repos/web.git")
	if wl := gitRun(t, base, "worktree", "list"); strings.Contains(wl, "/tasks/1-") || !strings.Contains(wl, fmt.Sprintf("/tasks/%d-", tv.N)) {
		t.Fatalf("the fork's base still knows task 1's worktree:\n%s", wl)
	}
	if st := gitRun(t, filepath.Join(k1.Dir, "web"), "status", "--porcelain"); st != "" || gitRun(t, filepath.Join(k1.Dir, "web"), "branch", "--show-current") != k1.Branch {
		t.Fatalf("task 1's checkout in the project's sandbox changed: %q", st)
	}

	// its own credential, none of the primary's
	purpose := "proj:" + pp.UID + ":" + k2.SandboxRef
	found := false
	for _, pu := range fx.tokenPurposes() {
		found = found || pu == purpose
	}
	if !found {
		t.Fatalf("no token minted for the fork (%s): %v", purpose, fx.tokenPurposes())
	}
	if _, err := os.Stat(fx.realDir(forkID, other)); !os.IsNotExist(err) {
		t.Fatalf("another project's credential came along into the fork: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("the project's own sandbox lost another project's credential: %v", err)
	}
	credRel := "/.config/xbin-scm/" + pp.UID + "/github.com.cred"
	mine, err1 := os.ReadFile(fx.realDir(forkID, fx.box.Home+credRel))
	theirs, err2 := os.ReadFile(fx.box.Home + credRel)
	if err1 != nil || len(mine) == 0 || (err2 == nil && string(mine) == string(theirs)) {
		t.Fatalf("the fork's credential file: %v %v (same as the primary's: %v)", err1, err2, err2 == nil && string(mine) == string(theirs))
	}

	// fetched: the commit pushed after the snapshot is under the task's branch
	co := fx.realDir(forkID, k2.Dir+"/web")
	if _, err := os.Stat(filepath.Join(co, "after.txt")); err != nil {
		t.Fatalf("the fork's checkout lacks the newer commit %s: %v", after, err)
	}
	cfg, _ := d.runConfig(run2)
	if cfg.Sandbox == nil || cfg.Sandbox.Ref != k2.SandboxRef || cfg.Sandbox.Cwd != k2.Dir+"/web" {
		t.Fatalf("the big task's binding: %+v", cfg.Sandbox)
	}
	var tvw TaskView
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", run2), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tvw) != nil || !tvw.Fork {
		t.Fatalf("GET task: %d %s", w.Code, w.Body)
	}

	// cleaned up: the fork goes, its credential scrubbed first
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", run2), map[string]any{}); w.Code != 202 {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	fx.waitWS(t, p.ID, tv.N, wsCleaned)
	waitJobsDone(t, fx.projFix, p.ID)
	if _, ok := fx.m.Box(forkID); ok {
		t.Fatalf("the fork outlived its task's cleanup: %s", jobsDump(d, p.ID))
	}
	var state string
	if err := d.q.QueryRow(`SELECT state FROM project_creds WHERE project_id=? AND sandbox_ref=?`, p.ID, k2.SandboxRef).Scan(&state); err != nil || state != credScrubbed {
		t.Fatalf("the fork's credential row: %q %v", state, err)
	}
	if d.getSetting(forkKey(p.ID, tv.N)) != "" {
		t.Fatalf("the fork is still in the registry")
	}
	if _, ok := fx.m.Box(fx.box.ID); !ok {
		t.Fatalf("the project's own sandbox went too")
	}
}

// Without a fork base (here: a manager that neither snapshots nor clones)
// a big task gets a fresh sandbox of the same image with the repos cloned
// into it; bigTasks.keepFork keeps it after the cleanup (its credential
// scrubbed: no task works there any more). A deleted project's fork goes
// when the loop next looks.
func TestForkFallbackFresh(t *testing.T) {
	fx := newP2Fix(t)
	fx.m.Caps = []string{"exec", "files", "tar", "stdio", "archive", "ports"}
	forgetHellos()
	p := fx.newProject(t, asAlice, map[string]any{"policy": map[string]any{"bigTasks": map[string]any{"keepFork": true}}})
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{}); w.Code != 409 {
		t.Fatalf("a fork base where the manager can't: %d %s", w.Code, w.Body)
	}
	tv, run := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	k := fx.waitWS(t, p.ID, tv.N, wsReady)
	pp, _ := d.getProject(p.ID)
	_, forkID, _ := splitSandboxRef(k.SandboxRef)
	if !k.ForkMade || forkID == fx.box.ID {
		t.Fatalf("the big task's sandbox: %+v", k)
	}
	body := fx.forkCreate(t, fmt.Sprintf("agent:proj:%d:fork:%d", p.ID, tv.N))
	if body["from"] != nil || body["image"] != "base" {
		t.Fatalf("a fresh fork's create: %v", body)
	}
	if out := gitRun(t, fx.realDir(forkID, pp.Dir+"/.repos/web.git"), "rev-parse", "--is-bare-repository"); out != "true" {
		t.Fatalf("the fresh fork's base: %s", out)
	}
	if _, err := os.Stat(fx.realDir(forkID, k.Dir+"/web/README.md")); err != nil {
		t.Fatalf("the fresh fork's checkout: %v", err)
	}
	if _, err := os.Stat(k.Dir); err == nil {
		t.Fatalf("the big task's checkout is in the project's own sandbox too")
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/cleanup", run), map[string]any{}); w.Code != 202 {
		t.Fatalf("cleanup: %d %s", w.Code, w.Body)
	}
	fx.waitWS(t, p.ID, tv.N, wsCleaned)
	waitJobsDone(t, fx.projFix, p.ID)
	if _, ok := fx.m.Box(forkID); !ok {
		t.Fatalf("keepFork: the fork was deleted: %s", jobsDump(d, p.ID))
	}
	if d.getSetting(forkKey(p.ID, tv.N)) != "" {
		t.Fatalf("a kept fork is still in the registry")
	}
	var kept string
	if err := d.q.QueryRow(`SELECT state FROM project_creds WHERE project_id=? AND sandbox_ref=?`, p.ID, k.SandboxRef).Scan(&kept); err != nil || kept != credScrubbed {
		t.Fatalf("a kept fork keeps its credential: %q %v", kept, err)
	}

	// a second big task's fork, then the project deleted: the loop deletes it
	_, err := d.q.Exec(`UPDATE projects SET policy=? WHERE id=?`, `{}`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	tv2, _ := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "another", "size": "big"})
	k2 := fx.waitWS(t, p.ID, tv2.N, wsReady)
	_, fork2, _ := splitSandboxRef(k2.SandboxRef)
	waitJobsDone(t, fx.projFix, p.ID)
	if w := callAs(t, fx.mux, asAlice, "DELETE", fmt.Sprintf("/projects/%d?sandbox=keep", p.ID), nil); w.Code != 202 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	hwait(t, "the project's deletion", func() bool { _, err := d.getProject(p.ID); return err != nil })
	if _, ok := fx.m.Box(fork2); !ok {
		t.Fatalf("the fork was gone before the loop looked")
	}
	forkSweep(t.Context())
	if _, ok := fx.m.Box(fork2); ok {
		t.Fatalf("a deleted project's fork outlived the loop")
	}
	if d.getSetting(forkKey(p.ID, tv2.N)) != "" {
		t.Fatalf("a deleted project's fork is still in the registry")
	}
	if _, ok := fx.m.Box(forkID); !ok {
		t.Fatalf("the kept fork went with the project")
	}
}

// failPrepare drives task n's prepare job, which fails in the fork after
// the fork was made and given its credential (the clone can't reach the
// origin), through its attempts to failed.
func (fx *p2Fix) failPrepare(t *testing.T, pid, n int64) *ProjectTask {
	t.Helper()
	hwait(t, "the big task's prepare to fail", func() bool {
		_, _ = fx.ag.db.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND kind=? AND state='queued'`, pid, pjPrepare)
		kickProjectWorker()
		k, _ := fx.ag.db.taskByN(pid, n)
		return k != nil && k.WS == wsFailed
	})
	k, _ := fx.ag.db.taskByN(pid, n)
	return k
}

// A big task whose prepare failed after its fork was made — the registry
// holds the fork, the task's row doesn't (fork_made unset), a live
// credential is in the fork — loses its fork when it is cleaned up, its
// credential scrubbed first; and so does one closed without a cleanup.
func TestForkOfFailedPrepareDeleted(t *testing.T) {
	fx := newP2Fix(t)
	fx.m.Caps = []string{"exec", "files", "tar", "stdio", "archive", "ports"} // fresh forks: the repos cloned in
	forgetHellos()
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	if _, err := d.q.Exec(`UPDATE project_repos SET url='file:///nowhere/web.git' WHERE project_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	for _, how := range []string{"cleanup", "close"} {
		tv, run := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one, " + how, "size": "big"})
		k := fx.failPrepare(t, p.ID, tv.N)
		e := forkEntryAt(d, forkKey(p.ID, tv.N))
		if k.ForkMade || e == nil || e.Ref == "" {
			t.Fatalf("%s: the failed task's fork: fork_made %v, registry %+v", how, k.ForkMade, e)
		}
		_, forkID, _ := splitSandboxRef(e.Ref)
		if _, ok := fx.m.Box(forkID); !ok {
			t.Fatalf("%s: the fork isn't at its manager", how)
		}
		if live := scmLiveIn(e.Ref); len(live) == 0 {
			t.Fatalf("%s: no live credential in the fork (the case this is about)", how)
		}
		if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/%s", run, how), map[string]any{}); w.Code != 202 && w.Code != 200 {
			t.Fatalf("%s: %d %s", how, w.Code, w.Body)
		}
		hwait(t, how+": the fork's deletion", func() bool {
			_, ok := fx.m.Box(forkID)
			return !ok && d.getSetting(forkKey(p.ID, tv.N)) == ""
		})
		waitJobsDone(t, fx.projFix, p.ID)
		var state string
		if err := d.q.QueryRow(`SELECT state FROM project_creds WHERE project_id=? AND sandbox_ref=?`, p.ID, e.Ref).Scan(&state); err != nil || state != credScrubbed {
			t.Fatalf("%s: the fork's credential row: %q %v", how, state, err)
		}
	}
	if _, ok := fx.m.Box(fx.box.ID); !ok {
		t.Fatalf("the project's own sandbox went too")
	}
}
