package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A fork base is taken only while the project's sandbox is quiet: a task at
// work there, a command, a busy coding agent each hold it off — the loop
// doesn't ask, the worker's own job gives up, a person's ask waits — and
// with now it is taken anyway. The credentials in the sandbox are emptied
// first; a newer one replaces (and deletes) the one before.
func TestSnapshotOnlyWhenQuiet(t *testing.T) {
	fx := newP2Fix(t)
	p, _, runID := readyTask(t, fx.projFix, nil)
	d := fx.ag.db
	ref := p.SandboxRef
	_, boxID, _ := splitSandboxRef(ref)
	ctx := context.Background()
	if why := d.sandboxBusyWhy(ref); why != "" {
		t.Fatalf("a ready project's sandbox isn't quiet: %s", why)
	}
	snaps := func() int { return len(fx.managerCalls("POST", "/sbx/sandboxes/"+boxID+"/snapshots")) }

	// busy, three ways: nothing is taken
	busy := []struct {
		name   string
		on     string
		off    string
		args   []any
		reason string
	}{
		{"a task at work", `UPDATE runs SET status='running' WHERE id=?`, `UPDATE runs SET status='idle' WHERE id=?`, []any{runID}, "task"},
		{"a command", `INSERT INTO sandbox_jobs (root_id, job, run_id, ref, command, state, created_ms) VALUES (999, 1, 999, ?, 'make', 'running', 1)`,
			`DELETE FROM sandbox_jobs WHERE root_id=999`, []any{ref}, "command"},
		{"a coding agent", `INSERT INTO harness_sessions (run_id, root_id, ref, state, prompt_state) VALUES (998, 998, ?, 'live', 'sent')`,
			`DELETE FROM harness_sessions WHERE run_id=998`, []any{ref}, "coding agent"},
	}
	for _, b := range busy {
		if _, err := d.q.Exec(b.on, b.args...); err != nil {
			t.Fatalf("%s: %v", b.name, err)
		}
		if why := d.sandboxBusyWhy(ref); !strings.Contains(why, b.reason) {
			t.Fatalf("%s: busy says %q", b.name, why)
		}
		before := len(d.jobsWhere(`WHERE project_id=? AND kind=?`, p.ID, pjSnapshot))
		forkBaseSweep(ctx)
		if js := d.jobsWhere(`WHERE project_id=? AND kind=?`, p.ID, pjSnapshot); len(js) != before {
			t.Fatalf("%s: the loop asked for a snapshot: %s", b.name, jobsDump(d, p.ID))
		}
		if b.name == "a task at work" {
			// the worker's own job gives up at once; a person's ask waits
			if _, err := d.queueJob(p.ID, 0, "", pjSnapshot, "", 0); err != nil {
				t.Fatal(err)
			}
			waitJobsDone(t, fx.projFix, p.ID)
			if js := d.jobsWhere(`WHERE project_id=? AND kind=? AND state='done'`, p.ID, pjSnapshot); len(js) != 1 || !strings.Contains(js[0].Step, "not taken") {
				t.Fatalf("the worker's snapshot job while busy: %s", jobsDump(d, p.ID))
			}
			// a person's ask while the loop's own job is queued makes it
			// theirs: it waits for quiet, from now
			loop, err := d.queueJob(p.ID, 0, "", pjSnapshot, "", time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{})
			var got struct{ Job ProjectJob }
			if w.Code != 202 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.Job.ID != loop.ID || got.Job.By != "alice" {
				t.Fatalf("POST fork-base over the loop's job %d: %d %s", loop.ID, w.Code, w.Body)
			}
			hwait(t, "the person's snapshot to wait for quiet", func() bool {
				js := d.jobsWhere(`WHERE project_id=? AND kind=? AND state='waiting'`, p.ID, pjSnapshot)
				return len(js) == 1 && strings.Contains(js[0].Step, "quiet")
			})
			if n := snaps(); n != 0 {
				t.Fatalf("a snapshot of a busy sandbox: %d", n)
			}
		}
		if _, err := d.q.Exec(b.off, b.args...); err != nil {
			t.Fatal(err)
		}
	}
	if why := d.sandboxBusyWhy(ref); why != "" {
		t.Fatalf("quiet again: %s", why)
	}
	// a person's terminal the agent doesn't track: its manager says
	conn, _ := sbxDial("apps/cs", "alice")
	ex, err := conn.ExecStart(ctx, boxID, sbxExecReq{Cmd: "sleep 30", Label: "alice's shell"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = d.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND state='waiting'`, p.ID)
	kickProjectWorker()
	hwait(t, "the person's snapshot to wait for the terminal", func() bool {
		js := d.jobsWhere(`WHERE project_id=? AND kind=? AND state='waiting' AND step LIKE '%alice''s shell%'`, p.ID, pjSnapshot)
		return len(js) == 1
	})
	if n := snaps(); n != 0 {
		t.Fatalf("a snapshot under a person's terminal: %d", n)
	}
	_ = conn.ExecDelete(ctx, boxID, ex.ID)

	// quiet: the person's waiting ask is taken, credentials emptied first
	_, _ = d.q.Exec(`UPDATE project_jobs SET next_ms=0 WHERE project_id=? AND state='waiting'`, p.ID)
	kickProjectWorker()
	waitJobsDone(t, fx.projFix, p.ID)
	cur, _ := d.getProject(p.ID)
	if cur.ForkSnap == "" || snaps() != 1 {
		t.Fatalf("no fork base once quiet: snap %q, %d snapshot calls; %s", cur.ForkSnap, snaps(), jobsDump(d, p.ID))
	}
	call := fx.managerCalls("POST", "/sbx/sandboxes/"+boxID+"/snapshots")[0]
	if want := fmt.Sprintf(`"clientId":"agent:proj:%d:snap:`, p.ID); !strings.Contains(call.Body, want) || !strings.Contains(call.Body, `"name":"fork base of `+p.Slug+`"`) {
		t.Fatalf("the snapshot's clientId and name (one per job, not the project's name, which may change): %s", call.Body)
	}
	var state, why string
	if err := d.q.QueryRow(`SELECT state, why FROM project_creds WHERE project_id=? AND sandbox_ref=?`, p.ID, ref).Scan(&state, &why); err != nil ||
		state != credScrubbed || why != scrubStop {
		t.Fatalf("the credential before the snapshot: %q %q %v", state, why, err)
	}

	// fresh: the loop asks for none
	forkBaseSweep(ctx)
	if js := d.jobsWhere(`WHERE project_id=? AND kind=? AND state IN ('queued','running','waiting')`, p.ID, pjSnapshot); len(js) != 0 {
		t.Fatalf("a fresh fork base asked for again: %s", jobsDump(d, p.ID))
	}

	// old and worked in since, under a terminal the agent doesn't know of:
	// the loop asks once; its job isn't taken; the loop doesn't ask again
	// within the hour (an idle coding agent's adapter may run for hours)
	day := time.Now().Add(-48 * time.Hour).UnixMilli()
	_, _ = d.q.Exec(`UPDATE projects SET fork_snap_ms=? WHERE id=?`, day, p.ID)
	_, _ = d.q.Exec(`UPDATE project_jobs SET updated_ms=? WHERE project_id=? AND kind=?`, day, p.ID, pjSnapshot)
	_, _ = d.q.Exec(`UPDATE project_tasks SET updated_ms=? WHERE project_id=?`, nowMs(), p.ID)
	ex, err = conn.ExecStart(ctx, boxID, sbxExecReq{Cmd: "sleep 30", Label: "an adapter"})
	if err != nil {
		t.Fatal(err)
	}
	before := len(d.jobsWhere(`WHERE project_id=? AND kind=?`, p.ID, pjSnapshot))
	for range 3 {
		forkBaseSweep(ctx)
		waitJobsDone(t, fx.projFix, p.ID)
	}
	if js := d.jobsWhere(`WHERE project_id=? AND kind=?`, p.ID, pjSnapshot); len(js) != before+1 || !strings.Contains(js[len(js)-1].Step, "not taken") {
		t.Fatalf("the loop under a terminal: %d jobs, was %d: %s", len(js), before, jobsDump(d, p.ID))
	}
	_ = conn.ExecDelete(ctx, boxID, ex.ID)
	_, _ = d.q.Exec(`UPDATE projects SET fork_snap_ms=? WHERE id=?`, nowMs(), p.ID)

	// now: taken while a task works, a new one, the old one deleted
	_, _ = d.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, runID)
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{"now": true}); w.Code != 202 {
		t.Fatalf("POST fork-base now: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	_, _ = d.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, runID)
	now, _ := d.getProject(p.ID)
	if now.ForkSnap == "" || now.ForkSnap == cur.ForkSnap || snaps() != 2 {
		t.Fatalf("now: snap %q (was %q), %d snapshot calls; %s", now.ForkSnap, cur.ForkSnap, snaps(), jobsDump(d, p.ID))
	}
	if n := len(fx.managerCalls("DELETE", "/snapshots/"+cur.ForkSnap)); n != 1 {
		t.Fatalf("the old fork base wasn't deleted (%d calls)", n)
	}

	// the owner and nobody else; a manager without snapshots: 409
	if w := callAs(t, fx.mux, asBob, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{}); w.Code != 404 {
		t.Fatalf("bob asks for a fork base: %d", w.Code)
	}
	fx.m.Caps = []string{"exec", "files", "tar", "stdio"}
	forgetHellos()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{}); w.Code != 409 || !strings.Contains(w.Body.String(), "unsupported") {
		t.Fatalf("a fork base where the manager can't: %d %s", w.Code, w.Body)
	}
}

// No credential reaches the sandbox between the scrub before a snapshot
// and the snapshot itself: a credential written meanwhile (a task's next
// git step, the workspace gate, a warm-up) waits until the manager has
// answered — so no snapshot, and no fork made of one, holds a live token.
func TestSnapshotHoldsCredentialsOut(t *testing.T) {
	fx := newP2Fix(t)
	pv, k, _ := readyTask(t, fx.projFix, nil)
	d := fx.ag.db
	p, err := d.getProject(pv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(scmLiveIn(p.SandboxRef)) == 0 {
		t.Fatalf("a ready task's sandbox holds no credential to scrub")
	}
	var sawLive []scmCredRow
	ensured := make(chan error, 1)
	fx.keep.mu.Lock()
	fx.keep.onSnap = func() {
		// the snapshot's create is at the manager: a credential is asked
		// for now, and given every chance to land before the manager answers
		go func() { ensured <- scmEnsureCreds(context.Background(), p, k, p.SandboxRef, 10*time.Minute) }()
		deadline := time.Now().Add(1500 * time.Millisecond)
		for time.Now().Before(deadline) && len(sawLive) == 0 {
			sawLive = scmLiveIn(p.SandboxRef)
			time.Sleep(20 * time.Millisecond)
		}
	}
	fx.keep.mu.Unlock()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{"now": true}); w.Code != 202 {
		t.Fatalf("POST fork-base: %d %s", w.Code, w.Body)
	}
	waitJobsDone(t, fx.projFix, p.ID)
	fx.keep.mu.Lock()
	fx.keep.onSnap = nil
	fx.keep.mu.Unlock()
	if cur, _ := d.getProject(p.ID); cur.ForkSnap == "" {
		t.Fatalf("no fork base: %s", jobsDump(d, p.ID))
	}
	if len(sawLive) > 0 {
		t.Fatalf("a credential was live in the sandbox while its snapshot was taken: %+v", sawLive)
	}
	select {
	case err := <-ensured:
		if err != nil {
			t.Fatalf("the credential asked for meanwhile: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatalf("the credential asked for meanwhile never came")
	}
	if len(scmLiveIn(p.SandboxRef)) == 0 {
		t.Fatalf("the credential asked for meanwhile wasn't written after the snapshot")
	}
}

// A fork base waits for the git steps in its sandbox, even taken now: a
// step running there (the worker's lock on it held) keeps the snapshot
// off — a person's ask waits — and nothing is taken. A step the worker let
// through just before the snapshot took the lock (claimed in the database,
// not yet marked busy) makes the snapshot give way, and the snapshot's
// release never clears a lock such a step holds.
func TestSnapshotWaitsForGitSteps(t *testing.T) {
	fx := newP2Fix(t)
	p, _, _ := readyTask(t, fx.projFix, nil)
	d := fx.ag.db
	ref := p.SandboxRef
	_, boxID, _ := splitSandboxRef(ref)
	snaps := func() int { return len(fx.managerCalls("POST", "/sbx/sandboxes/"+boxID+"/snapshots")) }
	waitJobsDone(t, fx.projFix, p.ID)
	projWorkers.Lock()
	w := projWorkers.m[projEng()]
	projWorkers.Unlock()
	if w == nil {
		t.Fatal("no project worker")
	}
	locked := func() bool {
		w.mu.Lock()
		defer w.mu.Unlock()
		return w.locked[ref]
	}

	// a git step runs there: the worker's lock on the sandbox is held
	w.mu.Lock()
	w.locked[ref] = true
	w.mu.Unlock()
	if wr := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{"now": true}); wr.Code != 202 {
		t.Fatalf("POST fork-base now: %d %s", wr.Code, wr.Body)
	}
	hwait(t, "the snapshot to wait for the git step", func() bool {
		js := d.jobsWhere(`WHERE project_id=? AND kind=? AND state='waiting'`, p.ID, pjSnapshot)
		return len(js) == 1 && strings.Contains(js[0].Step, "a workspace job runs in it")
	})
	if n := snaps(); n != 0 || !locked() {
		t.Fatalf("a snapshot over a git step: %d taken, the step's lock held %v", n, locked())
	}
	_, _ = d.q.Exec(`DELETE FROM project_jobs WHERE project_id=? AND kind=?`, p.ID, pjSnapshot)
	w.mu.Lock()
	delete(w.locked, ref)
	w.mu.Unlock()

	// a fetch the worker claimed just before the snapshot took the lock:
	// the snapshot gives way and leaves the lock to it
	fj, err := d.queueJob(p.ID, 0, "", pjFetch, "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.q.Exec(`UPDATE project_jobs SET state='running' WHERE id=?`, fj.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		w.mu.Lock()
		delete(w.busy, fj.ID)
		delete(w.locked, ref)
		w.mu.Unlock()
	})
	if release, ok := holdGitSteps(ref); ok || release != nil || locked() {
		t.Fatalf("the snapshot's hold with a git step claimed: ok %v, lock held %v", ok, locked())
	}
	// the worker marks it busy with the lock: a release leaves the lock
	w.mu.Lock()
	w.busy[fj.ID], w.locked[ref] = true, true
	w.mu.Unlock()
	releaseGitSteps(w, ref)
	if !locked() {
		t.Fatal("the snapshot's release cleared the lock a running git step holds")
	}
	// the step over: the hold is the snapshot's, and its release clears it
	w.mu.Lock()
	delete(w.busy, fj.ID)
	delete(w.locked, ref)
	w.mu.Unlock()
	_, _ = d.q.Exec(`UPDATE project_jobs SET state='done' WHERE id=?`, fj.ID)
	release, ok := holdGitSteps(ref)
	if !ok || !locked() {
		t.Fatalf("the snapshot's hold on a quiet sandbox: ok %v, lock held %v", ok, locked())
	}
	release()
	if locked() {
		t.Fatal("the snapshot's release left the lock")
	}
}
