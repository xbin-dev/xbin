package main

import (
	"context"
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
			if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/fork-base", p.ID), map[string]any{}); w.Code != 202 {
				t.Fatalf("POST fork-base: %d %s", w.Code, w.Body)
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
	if want := fmt.Sprintf(`"clientId":"agent:proj:%d:snap:%s"`, p.ID, time.Now().UTC().Format("20060102")); !strings.Contains(call.Body, want) {
		t.Fatalf("the snapshot's clientId: %s", call.Body)
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
