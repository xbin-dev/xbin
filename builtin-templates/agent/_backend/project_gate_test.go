package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A setup script that waits for a file the test makes: the workspace stays
// preparing until the test says so.
func holdSetup(fx *projFix) (script, release string) {
	release = filepath.Join(fx.box.Home, "go-on")
	return fmt.Sprintf("while [ ! -e %q ]; do sleep 0.05; done; echo prepared", release), release
}

// The gate parks a task's turn while its workspace is prepared — status
// sleeping, pendingState {kind: project}, its message left in the inbox,
// no model call — and lets it run once the workspace is ready.
func TestGateParksAndResumes(t *testing.T) {
	fx := newProjFix(t)
	script, release := holdSetup(fx)
	p := fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": script}}})
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "do the thing"})
	waitFor(t, "the gate's park", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		ps := parsePending(r.Pending)
		return r.Status == statusSleep && ps.Kind == pendKindProject && ps.Project != nil && ps.Project.N == 1
	})
	time.Sleep(50 * time.Millisecond)
	if n := len(fakeOf(fx.ag).callsFor(runID)); n != 0 {
		t.Fatalf("a parked task called the model %d times", n)
	}
	if rows := fx.ag.db.undelivered(runID); len(rows) == 0 || rows[0].Body.Text != "do the thing" {
		t.Fatalf("the start isn't waiting in the inbox: %+v", rows)
	}
	// a person's own message waits too
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", runID), map[string]any{"text": "and this"}); w.Code != 200 {
		t.Fatalf("a message to a parked task: %d %s", w.Code, w.Body)
	}
	if err := os.WriteFile(release, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fx.waitWS(t, p.ID, 1, wsReady)
	waitFor(t, "the turn after the park", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return r != nil && resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) > 0
	})
	r, _ := fx.ag.db.getRun(runID)
	if parsePending(r.Pending).Kind != "" {
		t.Fatalf("still parked: %s", r.Pending)
	}
	msgs, _ := fx.ag.db.messages(runID, false)
	var users []string
	for _, m := range msgs {
		if m.Role == "user" {
			users = append(users, m.Content)
		}
	}
	if len(users) != 2 || users[0] != "do the thing" || users[1] != "and this" {
		t.Fatalf("what reached the model: %q", users)
	}
}

// A cancel always passes the gate: a parked task stops.
func TestGateLetsCancelThrough(t *testing.T) {
	fx := newProjFix(t)
	script, _ := holdSetup(fx)
	p := fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": script}}})
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "do the thing"})
	waitStatus(t, fx.ag.db, runID, statusSleep)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/cancel", runID), nil); w.Code != 200 {
		t.Fatalf("cancel: %d %s", w.Code, w.Body)
	}
	waitStatus(t, fx.ag.db, runID, statusCanceled)
	if n := len(fakeOf(fx.ag).callsFor(runID)); n != 0 {
		t.Fatalf("the model was called %d times", n)
	}
	var tv TaskView
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/tasks/1", p.ID), nil)
	if json.Unmarshal(w.Body.Bytes(), &tv) != nil || tv.RunStatus != statusCanceled {
		t.Fatalf("the task: %s", w.Body)
	}
}

// A built-in task takes a subagent's place at the model-call gate — never a
// person's last slot; a coordinator and an ordinary conversation are
// top-level.
func TestTaskClassAtModelGate(t *testing.T) {
	ag, _ := accessFixture(t)
	p := insertTestProject(t, ag.db, "alice")
	mk := func(st runStamp, cfg Config) *Run {
		r, err := ag.startRunOpts(runOpts{Title: "x", Cfg: cfg, Hold: true, Stamp: st})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	tc := defaultConfig()
	tc.Project = &ProjectRef{ID: p.ID, Role: projRoleTask, N: 1}
	task := mk(runStamp{Owner: "alice", Origin: originProject, OriginID: p.ID}, tc)
	cc := defaultConfig()
	cc.Project = &ProjectRef{ID: p.ID, Role: projRoleCoordinator}
	coord := mk(runStamp{Owner: "alice", Origin: originProject, OriginID: p.ID, SessionKey: coordSessionKey(p.ID, "alice")}, cc)
	chat := mk(runStamp{Owner: "alice", Origin: "chat"}, defaultConfig())
	kid, _ := ag.db.createRun("kid", "{}", chat.ID)
	kidRun, _ := ag.db.getRun(kid)
	for _, c := range []struct {
		name string
		run  *Run
		top  bool
	}{{"task", task, false}, {"coordinator", coord, true}, {"chat", chat, true}, {"subagent", kidRun, false}} {
		if got := ag.eng.modelGateTop(c.run); got != c.top {
			t.Errorf("%s: top-level at the model-call gate = %v, want %v", c.name, got, c.top)
		}
	}
}

// A sign-in the provider asks for parks the task (waiting for a person,
// /needs reason project) until it is done; then the workspace is made.
func TestSigninParksTask(t *testing.T) {
	var signin atomic.Bool
	signin.Store(true)
	old := scmEnsureCreds
	scmEnsureCreds = func(_ context.Context, _ *Project, k *ProjectTask, _ string, _ time.Duration) error {
		if k != nil && signin.Load() {
			return &scmError{Status: 409, Refusal: scmRefSignin, Message: "sign in to GitHub"}
		}
		return nil
	}
	t.Cleanup(func() { scmEnsureCreds = old }) // registered first: runs after the engine stops
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "go"})
	fx.waitWS(t, p.ID, 1, wsSignin)
	waitFor(t, "the sign-in park", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		ps := parsePending(r.Pending)
		return r.Status == statusWaiting && ps.Kind == pendKindProject && ps.Project.WS == wsSignin
	})
	w := callAs(t, fx.mux, asAlice, "GET", "/needs", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"reason":"project"`) {
		t.Fatalf("/needs: %d %s", w.Code, w.Body)
	}
	r, _ := fx.ag.db.getRun(runID)
	if !strings.Contains(r.Result, "sign in to github.com") {
		t.Fatalf("the park's words: %q", r.Result)
	}
	// its start waits in the inbox, yet is no work of the engine's: the
	// person's sign-in ends the park (hasWork, userWake count the job)
	var all, work int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM inbox i WHERE i.run_id=? AND i.delivered_at=0`, runID).Scan(&all)
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM inbox i WHERE i.run_id=? AND i.delivered_at=0 AND `+fx.ag.db.gateHeld(), runID).Scan(&work)
	if all == 0 || work != 0 {
		t.Fatalf("the parked start: %d in the inbox, %d counted as work", all, work)
	}
	signin.Store(false)
	_, _ = fx.ag.db.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE state='waiting'`)
	kickProjectWorker()
	fx.waitWS(t, p.ID, 1, wsReady)
	waitFor(t, "the turn after the sign-in", func() bool { return len(fakeOf(fx.ag).callsFor(runID)) > 0 })
}

// The device code is in the view of the person who must sign in, and in no
// one else's — nor in a stream event.
func TestDeviceCodeOnlyToRequester(t *testing.T) {
	old, oldP := scmEnsureCreds, scmPendingSignin
	scmEnsureCreds = func(_ context.Context, _ *Project, k *ProjectTask, _ string, _ time.Duration) error {
		if k != nil {
			return &scmError{Status: 409, Refusal: scmRefSignin, Message: "sign in"}
		}
		return nil
	}
	scmPendingSignin = func(user, scm string) *scmSignin {
		if user == "alice" && scm == "apps/scm-github" {
			return &scmSignin{URL: "https://github.com/login/device", UserCode: "WDJB-MJHT"}
		}
		return nil
	}
	t.Cleanup(func() { scmEnsureCreds, scmPendingSignin = old, oldP })
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, map[string]any{"share": map[string]any{"members": []map[string]any{{"user": "carol", "role": "participant"}}}})
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "go"})
	waitStatus(t, fx.ag.db, runID, statusWaiting)
	for _, path := range []string{"/runs/%d/view", "/runs/%d/task", "/runs/%d"} {
		u := fmt.Sprintf(path, runID)
		if w := callAs(t, fx.mux, asAlice, "GET", u, nil); w.Code != 200 || !strings.Contains(w.Body.String(), "WDJB-MJHT") {
			t.Errorf("alice's %s lacks her code: %d %s", u, w.Code, clip(w.Body.String(), 400))
		}
		if w := callAs(t, fx.mux, asCarol, "GET", u, nil); w.Code != 200 || strings.Contains(w.Body.String(), "WDJB-MJHT") {
			t.Errorf("carol's %s: %d (the code shown: %v)", u, w.Code, strings.Contains(w.Body.String(), "WDJB-MJHT"))
		}
	}
	r, _ := fx.ag.db.getRun(runID)
	if b, _ := json.Marshal(runSummary(r)); strings.Contains(string(b), "WDJB") || strings.Contains(r.Pending, "WDJB") {
		t.Fatal("the stored park or the run event carries the code")
	}
}

// A task's brief is the same bytes from one turn to the next (the prompt
// cache stays warm); a failed setup's output is in it, framed untrusted.
func TestProjectPromptStable(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, map[string]any{"repos": []map[string]any{{"repo": "acme/web", "setup": "echo boom-from-setup; exit 3"}},
		"policy": map[string]any{"instructions": "Use tabs.", "checks": []string{"go test ./..."}}})
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "first"})
	waitFor(t, "the first turn", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) == 1
	})
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", runID), map[string]any{"text": "second"}); w.Code != 200 {
		t.Fatal(w.Body)
	}
	waitFor(t, "the second turn", func() bool {
		r, _ := fx.ag.db.getRun(runID)
		return resting(r.Status) && len(fakeOf(fx.ag).callsFor(runID)) == 2
	})
	calls := fakeOf(fx.ag).callsFor(runID)
	a, b := asString(calls[0].Msgs[0].Content), asString(calls[1].Msgs[0].Content)
	if a != b {
		t.Fatalf("the system prompt changed between turns:\n%s\n---\n%s", a, b)
	}
	for _, want := range []string{"Use tabs.", "`go test ./...`", "The setup script failed for web (exit 3)", "[untrusted", "boom-from-setup"} {
		if !strings.Contains(a, want) {
			t.Errorf("the brief lacks %q:\n%s", want, a)
		}
	}
}
