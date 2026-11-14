package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// loseSandbox drops ref at the fixture's manager as a manager that lost it
// would: no delete through the agent, the sandbox is simply not there.
func (fx *projFix) loseSandbox(t *testing.T, ref string) {
	t.Helper()
	_, id, _ := splitSandboxRef(ref)
	fx.m.mu.Lock()
	defer fx.m.mu.Unlock()
	if fx.m.boxes[id] == nil {
		t.Fatalf("no sandbox %s at the manager", id)
	}
	delete(fx.m.boxes, id)
}

func TestRecreateGoneSandbox(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, map[string]any{"sandbox": map[string]any{"new": map[string]any{"provider": "apps/cs"}}})
	fx.waitRepoReady(t, p.ID)
	tv, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "Fix the login page"})
	fx.waitWS(t, p.ID, 1, wsReady)
	// its first turn over and the refs fetch after it done before the
	// sandbox goes: a refs job that starts after would retry against the
	// gone sandbox, and the jobs would never go idle
	hwait(t, "the task's first turn", func() bool {
		r, err := fx.ag.db.getRun(runID)
		return err == nil && resting(r.Status) && strings.Contains(fullText(fx.ag.db, runID), "Fix the login page")
	})
	fx.waitJobsIdle(t, p.ID)
	before, _ := fx.ag.db.getProject(p.ID)
	old := before.SandboxRef

	fx.loseSandbox(t, old)
	w := callAs(t, fx.mux, asAlice, "GET", fmt.Sprintf("/projects/%d/status", p.ID), nil)
	var st ProjectStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || !st.Sandbox.Gone {
		t.Fatalf("the status should say the sandbox is gone: %d %s", w.Code, w.Body)
	}

	// only its owner recreates it
	if w := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{}); w.Code != 404 {
		t.Fatalf("bob: %d %s", w.Code, w.Body)
	}
	fx.waitJobsIdle(t, p.ID) // a job at work (a refs fetch) refuses it as busy for its own reason
	w = callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{})
	if w.Code != 202 {
		t.Fatalf("recreate: %d %s", w.Code, w.Body)
	}
	var out struct {
		Gone bool         `json:"gone"`
		Jobs []ProjectJob `json:"jobs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || !out.Gone || len(out.Jobs) != 2 || out.Jobs[0].Kind != pjSandbox || out.Jobs[1].Kind != pjPrepare {
		t.Fatalf("the answer: %s", w.Body)
	}

	// a new sandbox (a fresh clientId: the manager doesn't hand back the lost one), the repo cloned again, the task prepared there
	fx.waitRepoReady(t, p.ID)
	k := fx.waitWS(t, p.ID, 1, wsReady)
	after, _ := fx.ag.db.getProject(p.ID)
	if after.SandboxRef == "" || after.SandboxRef == old || !after.SandboxMade || k.SandboxRef != after.SandboxRef {
		t.Fatalf("the new sandbox: project %q (made %v), task %q, old %q", after.SandboxRef, after.SandboxMade, k.SandboxRef, old)
	}
	if got := gitRun(t, filepath.Join(k.Dir, "web"), "branch", "--show-current"); got != tv.Branch {
		t.Fatalf("the checkout's branch: %s", got)
	}
	if _, err := os.Stat(filepath.Join(k.Dir, "web", "README.md")); err != nil {
		t.Fatalf("the checkout: %v", err)
	}
	_, id, _ := splitSandboxRef(after.SandboxRef)
	conn, _ := sbxDial("apps/cs", "alice")
	if box, err := conn.Get(context.Background(), id); err != nil || box.Labels[projectLabel] != after.UID {
		t.Fatalf("the new sandbox at its manager: %+v %v", box, err)
	}
	if v := fx.ag.db.getSetting(sbxNewKey(p.ID)); v != "" {
		t.Fatalf("the spec kept after the sandbox was made: %s", v)
	}
	var notes int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM project_events WHERE project_id=? AND kind=? AND body LIKE '%recreated%'`, p.ID, pevNote).Scan(&notes)
	if notes != 1 {
		t.Fatalf("recreated notes: %d", notes)
	}

	// once more: another generation, another sandbox
	fx.loseSandbox(t, after.SandboxRef)
	fx.waitJobsIdle(t, p.ID) // a job at work (a refs fetch) refuses it as busy for its own reason
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{}); w.Code != 202 {
		t.Fatalf("recreate again: %d %s", w.Code, w.Body)
	}
	fx.waitWS(t, p.ID, 1, wsReady)
	third, _ := fx.ag.db.getProject(p.ID)
	if third.SandboxRef == after.SandboxRef || third.SandboxRef == old {
		t.Fatalf("the third sandbox: %s", third.SandboxRef)
	}
}

func TestRecreateLiveSandboxNeedsForce(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	old := sandboxRef("apps/cs", fx.box.ID)

	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{})
	if w.Code != 409 || !json.Valid(w.Body.Bytes()) {
		t.Fatalf("a live sandbox without force: %d %s", w.Code, w.Body)
	}
	var refused struct {
		Refusal string `json:"refusal"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refused)
	if refused.Refusal != "exists" {
		t.Fatalf("the refusal: %s", w.Body)
	}
	if q, _ := fx.ag.db.getProject(p.ID); q.SandboxRef != old {
		t.Fatalf("a refusal changed the project: %s", q.SandboxRef)
	}

	fx.waitJobsIdle(t, p.ID) // a job at work (a refs fetch) refuses it as busy for its own reason
	w = callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{"force": true})
	if w.Code != 202 {
		t.Fatalf("force: %d %s", w.Code, w.Body)
	}
	fx.waitRepoReady(t, p.ID)
	q, _ := fx.ag.db.getProject(p.ID)
	if q.SandboxRef == old || !q.SandboxMade {
		t.Fatalf("the new sandbox: %s (made %v)", q.SandboxRef, q.SandboxMade)
	}
	// the old one is left as it is
	conn, _ := sbxDial("apps/cs", "alice")
	if _, err := conn.Get(context.Background(), fx.box.ID); err != nil {
		t.Fatalf("the old sandbox: %v", err)
	}
}

// waitJobsIdle waits for project pid to have no job queued or at work: a
// recreate refuses while one is (busy), and the project queues its own (a
// refs fetch after a turn, a prepare after a sandbox).
func (fx *projFix) waitJobsIdle(t *testing.T, pid int64) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for len(fx.ag.db.jobsWhere(`WHERE project_id=? AND state IN ('queued','running')`, pid)) != 0 {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for the project's jobs:\n%s", jobsDump(fx.ag.db, pid))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A turn at work in a sandbox that is still there refuses a recreate, force
// or not: it would go on in the old sandbox while its task is prepared
// again in the new one and its credentials are revoked (unpushed work lost).
func TestRecreateRefusesATurnAtWork(t *testing.T) {
	fx := newProjFix(t)
	p := fx.newProject(t, asAlice, nil)
	fx.waitRepoReady(t, p.ID)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "Fix the login page"})
	fx.waitWS(t, p.ID, 1, wsReady)
	// its own turn over first: the engine writes the run's status while it
	// works, and would put back another over the one set here
	// its first turn over and the project's own jobs (a refs fetch after it)
	// done: the engine writes the run's status while it works, and a job at
	// work refuses a recreate as busy for its own reason
	hwait(t, "the task's first turn", func() bool {
		r, err := fx.ag.db.getRun(runID)
		return err == nil && resting(r.Status) && strings.Contains(fullText(fx.ag.db, runID), "Fix the login page")
	})
	fx.waitJobsIdle(t, p.ID)
	old := sandboxRef("apps/cs", fx.box.ID)
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET status=? WHERE id=?`, statusRunning, runID); err != nil {
		t.Fatal(err)
	}
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{"force": true})
	var refused struct {
		Refusal string `json:"refusal"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &refused)
	if w.Code != 409 || refused.Refusal != "busy" || !strings.Contains(w.Body.String(), "task #1") {
		t.Fatalf("a turn at work: %d %s", w.Code, w.Body)
	}
	if q, _ := fx.ag.db.getProject(p.ID); q.SandboxRef != old || fx.ag.db.getSetting(sbxNewKey(p.ID)) != "" {
		t.Fatalf("a refusal changed the project: %s", q.SandboxRef)
	}
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET status=? WHERE id=?`, statusDone, runID); err != nil {
		t.Fatal(err)
	}
	fx.waitJobsIdle(t, p.ID)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{"force": true}); w.Code != 202 {
		t.Fatalf("once the turn ended: %d %s", w.Code, w.Body)
	}
	// a second one at once is refused, not a second sandbox
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/sandbox/recreate", p.ID), map[string]any{"force": true}); w.Code != 409 {
		t.Fatalf("a second recreate at once: %d %s", w.Code, w.Body)
	}
	fx.waitRepoReady(t, p.ID)
}
