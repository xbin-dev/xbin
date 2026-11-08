package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// forkLogins are coding agents' $HOME logins (acp.Provider.LoginFiles, as
// the paths they come to with $HOME the sandbox's home).
var forkLogins = []string{".claude/.credentials.json", ".codex/auth.json", ".gemini/oauth_creds.json",
	".local/share/opencode/auth.json", ".fakeacp/credentials"}

// A snapshot fork carries the project's sandbox's home — a coding agent's
// login with it: the fork base holds it, as a manager's clone copies the
// whole sandbox — and the fork has it removed before anything runs there
// (copies of one refresh token log each other out). The project's own
// sandbox keeps its login.
func TestForkClearsCopiedLogin(t *testing.T) {
	fx := newP2Fix(t)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	for _, f := range forkLogins {
		path := filepath.Join(fx.box.Home, f)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"refreshToken":"the-project-sandbox's"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{"now": true}); w.Code != 202 {
		t.Fatalf("POST fork-base: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	pp, _ := d.getProject(p.ID)
	if pp.ForkSnap == "" {
		t.Fatalf("no fork base: %s", jobsDump(d, p.ID))
	}
	// the hypothesis, settled: the fork base holds every login (a fork is
	// a copy of it)
	snap := filepath.Join(fx.keep.root, ".snaps", fx.box.ID, pp.ForkSnap, "home")
	for _, f := range forkLogins {
		if _, err := os.Stat(filepath.Join(snap, f)); err != nil {
			t.Fatalf("the fork base lacks %s: %v", f, err)
		}
	}
	tv, _ := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	k := fx.waitWS(t, p.ID, tv.N, wsReady)
	_, forkID, _ := splitSandboxRef(k.SandboxRef)
	if !k.ForkMade || forkID == fx.box.ID {
		t.Fatalf("the big task's sandbox: %+v", k)
	}
	if e := forkEntryAt(d, forkKey(p.ID, tv.N)); e == nil || e.Mode != forkFromSnap {
		t.Fatalf("not a snapshot fork: %+v", e)
	}
	for _, f := range forkLogins {
		if _, err := os.Stat(fx.realDir(forkID, filepath.Join(fx.box.Home, f))); !os.IsNotExist(err) {
			t.Errorf("the fork kept the copied login %s: %v", f, err)
		}
		if _, err := os.Stat(filepath.Join(fx.box.Home, f)); err != nil {
			t.Errorf("the project's own sandbox lost its login %s: %v", f, err)
		}
	}
}

// forkBoxState sets fork id's state at the fake manager, once per id (a
// repeated create answers the same sandbox: made sees it again).
func forkBoxState(fx **p2Fix, state, detail string) func(string) {
	var mu sync.Mutex
	seen := map[string]bool{}
	return func(id string) {
		mu.Lock()
		defer mu.Unlock()
		if seen[id] || *fx == nil {
			return
		}
		seen[id] = true
		m := (*fx).m
		m.mu.Lock()
		if b := m.boxes[id]; b != nil {
			b.State, b.StateDetail = state, detail
		}
		m.mu.Unlock()
	}
}

// prepareOf is task k's latest prepare job.
func prepareOf(t *testing.T, d *DB, k *ProjectTask) *ProjectJob {
	t.Helper()
	js := d.jobsWhere(`WHERE task_id=? AND kind=? ORDER BY id DESC LIMIT 1`, k.ID, pjPrepare)
	if len(js) == 0 {
		t.Fatalf("no prepare job for task %d", k.N)
	}
	return js[0]
}

// A big task's fork still being made when the create answers — after an
// answer lost to the wait (the manager makes a VM cold for many minutes,
// and the same clientId answers the record still creating) — is waited
// for: prepare never starts a sandbox that is creating, spends no attempt
// on it, and the task stays preparing, saying so; once the manager has
// made it, the task goes on in it.
func TestForkStillCreatingWaits(t *testing.T) {
	fastBackoff(t)
	var fx *p2Fix
	sc := &slowCreates{lose: 1, match: `:fork:`, made: forkBoxState(&fx, "creating", "")}
	fx = newP2FixWith(t, sc.wrap)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	tv, run := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	var e *forkEntry
	hwait(t, "the prepare waiting on the fork being made", func() bool {
		k, _ := d.taskByN(p.ID, tv.N)
		e = forkEntryAt(d, forkKey(p.ID, tv.N))
		j := prepareOf(t, d, k)
		if j.State == pjFailed {
			t.Fatalf("the prepare failed: %s", jobsDump(d, p.ID))
		}
		return e != nil && e.Ref != "" && j.State == pjWaiting && j.Step == forkMaking
	})
	_, forkID, _ := splitSandboxRef(e.Ref)
	// looked at again (each look asks the create again), still waiting
	for i := 0; i < 3; i++ {
		before := len(fx.managerCalls("POST", "/sbx/sandboxes"))
		_, _ = d.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND kind=? AND state='waiting'`, p.ID, pjPrepare)
		kickProjectWorker()
		hwait(t, "another look", func() bool { return len(fx.managerCalls("POST", "/sbx/sandboxes")) > before })
	}
	hwait(t, "the look done", func() bool {
		k, _ := d.taskByN(p.ID, tv.N)
		return prepareOf(t, d, k).State == pjWaiting
	})
	k, _ := d.taskByN(p.ID, tv.N)
	if j := prepareOf(t, d, k); j.Attempts > 1 || j.Step != forkMaking {
		t.Fatalf("the prepare while the fork is made: attempts %d, step %q (%s)", j.Attempts, j.Step, jobsDump(d, p.ID))
	}
	if calls := fx.managerCalls("POST", "/sbx/sandboxes/"+forkID+"/start"); len(calls) != 0 {
		t.Fatalf("a sandbox still being made was started: %d calls", len(calls))
	}
	var tw TaskView
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", run), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tw) != nil {
		t.Fatalf("GET task: %d %s", w.Code, w.Body)
	}
	if tw.WS != wsPreparing || tw.State != taskPreparing || tw.Column != colQueued || tw.Step != forkMaking || tw.Fork {
		t.Fatalf("the task while its sandbox is made: ws %s state %s column %s step %q fork %v", tw.WS, tw.State, tw.Column, tw.Step, tw.Fork)
	}

	// made: the next look goes on in it
	fx.m.mu.Lock()
	fx.m.boxes[forkID].State = "running"
	fx.m.mu.Unlock()
	_, _ = d.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND kind=? AND state='waiting'`, p.ID, pjPrepare)
	kickProjectWorker()
	k = fx.waitWS(t, p.ID, tv.N, wsReady)
	if !k.ForkMade || k.SandboxRef != e.Ref {
		t.Fatalf("the big task's sandbox: %+v, the fork %s", k, e.Ref)
	}
	if n := len(fx.projectBoxes(p.UID)); n != 2 {
		t.Fatalf("%d sandboxes of the project, want its own and one fork", n)
	}
}

// A fork in state error at its manager fails the task's workspace at once
// with the manager's words; the fork is deleted, and Retry makes another.
func TestForkErrorStateFails(t *testing.T) {
	fastBackoff(t)
	var fx *p2Fix
	sc := &slowCreates{match: `:fork:`, made: forkBoxState(&fx, "error", "the VM didn't boot: no space left on the pool")}
	fx = newP2FixWith(t, sc.wrap)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	tv, run := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	k := fx.waitWS(t, p.ID, tv.N, wsFailed)
	if !strings.Contains(k.Error, "no space left on the pool") || !strings.Contains(k.Error, "Retry makes another") {
		t.Fatalf("the task's error: %q", k.Error)
	}
	if j := prepareOf(t, d, k); j.Attempts != 1 {
		t.Fatalf("an error at the manager was retried: %d attempts (%s)", j.Attempts, jobsDump(d, p.ID))
	}
	if n := len(fx.projectBoxes(p.UID)); n != 1 {
		t.Fatalf("the failed fork is still at its manager: %d sandboxes of the project", n)
	}
	e := forkEntryAt(d, forkKey(p.ID, tv.N))
	if e == nil || e.Ref != "" || !strings.HasSuffix(e.Client, ":again1") {
		t.Fatalf("the fork's entry after its failure: %+v", e)
	}
	var tw TaskView
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", run), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tw) != nil ||
		tw.State != taskFailed || tw.Column != colNeedsYou {
		t.Fatalf("GET task: %d %s", w.Code, w.Body)
	}
	sc.mu.Lock()
	sc.made = nil
	sc.mu.Unlock()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/task/retry", run), map[string]any{}); w.Code != 202 {
		t.Fatalf("retry: %d %s", w.Code, w.Body)
	}
	k = fx.waitWS(t, p.ID, tv.N, wsReady)
	if !k.ForkMade || k.SandboxRef == "" {
		t.Fatalf("after Retry: %+v", k)
	}
	if body := fx.forkCreate(t, fmt.Sprintf("agent:proj:%d:fork:%d:again1", p.ID, tv.N)); body == nil {
		t.Fatal("Retry didn't ask with a new clientId")
	}
}

// A big task's fork waiting for its person's sign-in to the provider (its
// own credential) is the workspace state signin, as a small task's is —
// never a failed attempt — and goes on once they have signed in.
func TestForkScmSigninState(t *testing.T) {
	var pending atomic.Bool
	pending.Store(true)
	var primary atomic.Value
	primary.Store("")
	old := scmEnsureCreds
	scmEnsureCreds = func(ctx context.Context, p *Project, k *ProjectTask, ref string, left time.Duration) error {
		if pending.Load() && k != nil && ref != "" && ref != primary.Load().(string) {
			return &scmError{Status: 409, Refusal: scmRefSignin, Message: "sign in to github.com"}
		}
		return old(ctx, p, k, ref, left)
	}
	fx := newP2Fix(t)
	t.Cleanup(func() { scmEnsureCreds = old }) // after the engine stops (cleanups run last first)
	stopEngineFirst(t, fx.ag)
	primary.Store(sandboxRef("apps/cs", fx.box.ID))
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	tv, run := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big"})
	k := fx.waitWS(t, p.ID, tv.N, wsSignin)
	hwait(t, "the prepare waiting", func() bool { return prepareOf(t, d, k).State == pjWaiting })
	if j := prepareOf(t, d, k); j.Attempts != 0 || j.Error != "" {
		t.Fatalf("a sign-in spent an attempt: %d %q", j.Attempts, j.Error)
	}
	var tw TaskView
	if w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/runs/%d/task", run), nil); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tw) != nil ||
		tw.State != taskSignin || tw.WaitingFor != "signin" || tw.Column != colNeedsYou {
		t.Fatalf("GET task while it waits for the sign-in: %d %s", w.Code, w.Body)
	}
	pending.Store(false)
	_, _ = d.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND state='waiting'`, p.ID)
	kickProjectWorker()
	k = fx.waitWS(t, p.ID, tv.N, wsReady)
	if !k.ForkMade {
		t.Fatalf("after the sign-in: %+v", k)
	}
}

// A big task a coding agent answers says before its first turn when the
// agent starts signed out in its own sandbox (its copied login removed)
// with no saved sign-in to go in — here an unpartitioned agent, which has
// none; in a person's partition: where to save one, or the gate's reason
// for leaving theirs out — and nothing when theirs goes in.
func TestForkSigninNote(t *testing.T) {
	fx := newP2Fix(t)
	argv := acptest.Command()
	fx.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(fx.ag.eng) })
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	d := fx.ag.db
	tv, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "a big one", "size": "big", "agent": map[string]any{"provider": "fake"}})
	k := fx.waitWS(t, p.ID, tv.N, wsReady)
	want := "Fake agent (tests) starts signed out in each big task's own sandbox and asks there: " + noSavedSignins
	said := false
	for _, ev := range d.projectEvents(p.ID, 0, 100) {
		said = said || ev.N == tv.N && strings.Contains(string(ev.Body), "starts signed out in each big task's own sandbox")
	}
	var notes string
	_ = d.q.QueryRow(`SELECT group_concat(detail, ' | ') FROM steps WHERE run_id=? AND kind='note'`, runID).Scan(&notes)
	if !said || !strings.Contains(notes, want) {
		t.Fatalf("the task didn't say its coding agent starts signed out (event %v): %s", said, notes)
	}

	// in alice's partition: the card's one-click path; one saved goes in
	// (nothing said); one the gate leaves out (a team project's shared fork)
	// says why
	setMode(t, modeUser, "alice")
	memVault(t)
	box := &sbxSandbox{Visibility: "private"}
	box.Owner.User = "alice"
	if note := forkSigninNote(d, k, k.SandboxRef, box); note != "Fake agent (tests) starts signed out in the task's own sandbox; when it asks, "+
		"“Sign in once for every big task” saves a long-lived sign-in (Fake agent (tests): claude setup-token) in your partition, "+
		"and every later big task starts signed in (or save one in "+savedSigninsPath+")" {
		t.Fatalf("no saved sign-in: %q", note)
	}
	if _, err := d.saveSignin("alice", "fake", "Personal", acp.Key{Env: "CLAUDE_CODE_OAUTH_TOKEN", Kind: "setup-token"}, "sk-ant-oat01-x", time.Time{}); err != nil {
		t.Fatal(err)
	}
	if note := forkSigninNote(d, k, k.SandboxRef, box); note != "" {
		t.Fatalf("a saved sign-in that goes in: %q", note)
	}
	box.Members = []string{"bob"}
	if note := forkSigninNote(d, k, k.SandboxRef, box); note != "Fake agent (tests) starts signed out in the task's own sandbox and will ask there: "+credProjectShared {
		t.Fatalf("a saved sign-in the gate leaves out (a team project's shared fork): %q", note)
	}
	// a saved sign-in that can't be used (refused): the one-click path replaces it
	box.Members = nil
	ss := d.signins("alice", "fake")[0]
	ss.RefusedAt = nowMs()
	if err := d.putSignin(ss); err != nil {
		t.Fatal(err)
	}
	if note := forkSigninNote(d, k, k.SandboxRef, box); !strings.HasPrefix(note, "your saved sign-in Personal isn't used: it was refused") ||
		!strings.Contains(note, "“Sign in once for every big task”") {
		t.Fatalf("a refused saved sign-in: %q", note)
	}
}
