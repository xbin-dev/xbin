package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// askSchema is Claude's AskUserQuestion form as the adapters build it: the
// questions are its choice fields, each with an "Other" box.
const askSchema = `{"type":"object","properties":{
	"question_0":{"type":"string","title":"Deploy","description":"Deploy the fix to staging now?","oneOf":[{"const":"Yes","title":"Yes"},{"const":"No","title":"No"}]},
	"question_0_custom":{"type":"string","title":"Other","_meta":{"_askUserQuestionCustomAnswer":{"questionId":"question_0","isCustomAnswer":true}}}}}`

// parkQuestion parks run on a coding agent's question asked at since (its
// result left as an earlier answer, as a coding agent's is).
func parkQuestion(t *testing.T, d *DB, run int64, since int64) {
	t.Helper()
	ps := pendingState{Kind: "question", Park: newPark(), Since: since,
		Harness: &hPark{EID: "e1", Message: "Please answer the following questions.", Schema: json.RawMessage(askSchema)}}
	raw, _ := json.Marshal(ps)
	if err := d.Tx(func(t *DB) error {
		return t.setStatus(run, statusWaiting, 0, "Done: the header is fixed.", string(raw))
	}); err != nil {
		t.Fatal(err)
	}
}

// The coordinator reads a task's question — quoted, with its age — not
// "waiting for a person": in task_list, task_status and task_message's
// answer, which says only a person may answer it and where they see it,
// and holds the message. The built-in engine's ask_user says its question
// (run.result) with the age of its ask step.
func TestCoordSeesQuestionAndAge(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	_, task := fx.addTask(t, fx.p, 1, "alice")
	parkQuestion(t, fx.ag.db, task.ID, time.Now().Add(-49*time.Hour).UnixMilli())
	want := "waiting for a person to answer: «Deploy the fix to staging now?» (asked 2d ago)"
	for _, tool := range []string{"task_list", "task_status"} {
		out, err := fx.tool(t, run, tool, map[string]any{})
		if err != nil || !strings.Contains(out, want) || strings.Contains(out, "the header is fixed") {
			t.Fatalf("%s: %v\n%s", tool, err, out)
		}
	}
	out, err := fx.tool(t, run, "task_message", map[string]any{"task": 1, "text": "yes, deploy it"})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"not delivered yet", "it waits for a person to answer: «Deploy the fix to staging now?» (asked 2d ago)",
		"Only a person may answer it", "you can't answer or decline it",
		"Needs you list", "project's page", "delivered once they have answered"} {
		if !strings.Contains(out, w) {
			t.Fatalf("task_message's answer lacks %q:\n%s", w, out)
		}
	}
	if q := fx.ag.db.queuedInputs(fx.p.ID, 1); len(q) != 1 || !q[0].HoldPark {
		t.Fatalf("the message isn't held: %+v", q)
	}
	if r := mustRun(t, fx.ag, task.ID); r.Status != statusWaiting || parsePending(r.Pending).Harness == nil {
		t.Fatalf("the question was answered: %s %s", r.Status, r.Pending)
	}

	// the built-in engine's ask_user: its question is the result, its age the ask step's
	_, t2 := fx.addTask(t, fx.p, 2, "alice")
	if err := fx.ag.db.Tx(func(t *DB) error {
		s := t.journal(t2.ID, "ask", map[string]string{"kind": "ask_user", "question": "Which branch?"})
		if _, err := t.q.Exec(`UPDATE steps SET created=? WHERE id=?`, time.Now().Add(-3*time.Hour).Unix(), s.ID); err != nil {
			return err
		}
		return t.setStatus(t2.ID, statusWaiting, 0, "Which branch should I **base** it on?", "")
	}); err != nil {
		t.Fatal(err)
	}
	out, _ = fx.tool(t, run, "task_status", map[string]any{"tasks": []int64{2}})
	if !strings.Contains(out, "waiting for a person to answer: «Which branch should I base it on?» (asked 3h ago)") {
		t.Fatalf("ask_user's words:\n%s", out)
	}
}

// A task whose turn ended — idle, its answer ready — is needs-you on the
// board, but the coordinator reads that its answer is ready and that a
// message starts its next turn, not that it waits for a person.
func TestIdleTaskWords(t *testing.T) {
	fx := newCoordFix(t)
	run := fx.coordinator(t, asAlice, fx.p.ID)
	k, task := fx.addTask(t, fx.p, 1, "alice")
	if err := fx.ag.db.Tx(func(t *DB) error { return t.setStatus(task.ID, statusIdle, 0, "", "") }); err != nil {
		t.Fatal(err)
	}
	if v := fx.ag.db.projTaskView(fx.p, k); v.State != taskNeedsYou || v.Column != colNeedsYou {
		t.Fatalf("the board moved: %s %s", v.Column, v.State)
	}
	out, err := fx.tool(t, run, "task_list", map[string]any{})
	if err != nil || !strings.Contains(out, "#1 Task 1 — its turn ended — its answer is ready (task_result); a message from you starts its next turn") ||
		strings.Contains(out, "waiting for a person") {
		t.Fatalf("task_list: %v\n%s", err, out)
	}
	out, _ = fx.tool(t, run, "task_message", map[string]any{"task": 1, "text": "now the footer"})
	if strings.Contains(out, "not delivered") {
		t.Fatalf("an idle task's message held: %s", out)
	}
}

// A question the coding agent withdraws ($/cancel_request) is settled like
// a withdrawn permission: the park clears, the turn goes on, and the
// coordinator's message held behind it is delivered — its turn follows.
func TestWithdrawnQuestionReleasesHeld(t *testing.T) {
	trigger := filepath.Join(t.TempDir(), "withdraw")
	fx, runID := harnessTaskFix(t, "ask-withdraw "+trigger, nil)
	defer func() {
		if t.Failed() {
			r, _ := fx.ag.db.getRun(runID)
			t.Logf("run: %s %s\n%s", r.Status, clip(r.Pending, 600), transcript(fx.ag.db, runID))
		}
	}()
	hwait(t, "the task's question", func() bool {
		r, err := fx.ag.db.getRun(runID)
		return err == nil && r.Status == statusWaiting && parsePending(r.Pending).Kind == "question"
	})
	pid := fx.ag.db.taskByRun(runID).ProjectID
	w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/coordinator", pid), map[string]any{})
	var co struct {
		Run Run `json:"run"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &co) != nil {
		t.Fatalf("the coordinator: %d %s", w.Code, w.Body)
	}
	coord := mustRun(t, fx.ag, co.Run.ID)
	cfg, _ := fx.ag.db.runConfig(coord.ID)
	out, err := fx.ag.runTool(context.Background(), coord, cfg, "task_message", map[string]any{"task": 1, "text": "echo after the question"})
	if err != nil || !strings.Contains(out, "«Deploy the fix to staging now?»") || !strings.Contains(out, "Only a person may answer it") {
		t.Fatalf("task_message: %v %s", err, out)
	}
	if q := fx.ag.db.queuedInputs(pid, 1); len(q) != 1 || !q[0].HoldPark {
		t.Fatalf("the message isn't held: %+v", q)
	}
	if err := os.WriteFile(trigger, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	hwait(t, "the held message's turn", func() bool {
		return turnOver(fx.ag, runID)() && strings.Contains(fullText(fx.ag.db, runID), "echo after the question")
	})
	text := fullText(fx.ag.db, runID)
	if i, j := strings.Index(text, "withdrew the question"), strings.Index(text, "echo after the question"); i < 0 || j < i {
		t.Fatalf("withdrawn, then the held message: %s", transcript(fx.ag.db, runID))
	}
	if q := fx.ag.db.queuedInputs(pid, 1); len(q) != 0 {
		t.Fatalf("still held: %+v", q)
	}
}

// After a restart, a parked question whose adapter exec is gone is settled
// and its turn ended: the successor's attach finds the exec gone (the pipe
// ends lost), never waits on it — the run doesn't stay waiting for an
// answer nothing can take.
func TestRestartParkedQuestionExecGone(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "ask")
	parkOf(t, ag, run.ID, "question")
	s := ag.eng.harnessOf(run.ID)
	tg, exec := s.pipe.t, s.pipe.ExecID()
	handOff(t, ag, run.ID)
	if err := tg.Conn.ExecDelete(context.Background(), tg.ID, exec); err != nil {
		t.Fatal(err)
	}
	hwait(t, "the exec gone", func() bool { return !execRuns(tg, exec) })
	b := successor(t, ag)
	hwait(t, "the question settled", func() bool {
		r := mustRun(t, ag, run.ID)
		return r.Status != statusWaiting && turnOver(ag, run.ID)()
	})
	r := mustRun(t, ag, run.ID)
	hs, _ := ag.db.harnessSession(run.ID)
	if r.Pending != "" || hs.Queue != "" || (hs.State != hsLost && hs.State != hsStopped) || b.reattaching(run.ID) {
		t.Fatalf("after the restart: run %s %q, session %s %q", r.Status, r.Pending, hs.State, hs.Error)
	}
	if rows := ag.db.placeholderRows(run.ID); len(rows) != 0 {
		t.Fatalf("a tool row still waits: %v\n%s", rows, transcript(ag.db, run.ID))
	}
	// and the conversation goes on: the next message starts a new adapter
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo again")
	})
}

// While a restart's attach is tried again (the sandbox manager doesn't
// answer), a task parked on a question reads "reconnecting to its coding
// agent", not "waiting for a person"; once the attach takes, its question
// again.
func TestRestartReconnectingWords(t *testing.T) {
	var down atomic.Bool
	wrap := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if down.Load() {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"restarting","refusal":"unavailable"}`))
				return
			}
			h.ServeHTTP(w, r)
		})
	}
	fx := newProjFixWith(t, wrap)
	fx.m.Caps = []string{"exec", "files", "tar"}
	argv := acptest.Command()
	fx.m.Harnesses = []fsbHarness{{ID: "fake", Title: "Fake agent (tests)", Argv: argv, Login: argv[0] + " acptest login"}}
	forgetHarnessProbes()
	t.Cleanup(func() { settleHarnesses(fx.ag.eng) })
	p := fx.newProject(t, asAlice, nil)
	_, runID := fx.newTask(t, asAlice, p.ID, map[string]any{"text": "ask-withdraw " + filepath.Join(t.TempDir(), "never"), "agent": map[string]any{"provider": "fake"}})
	hwait(t, "the task's question", func() bool {
		r, err := fx.ag.db.getRun(runID)
		return err == nil && r.Status == statusWaiting && parsePending(r.Pending).Kind == "question"
	})
	words := func() string {
		pv, _ := fx.ag.db.getProject(p.ID)
		k, _ := fx.ag.db.taskByN(p.ID, 1)
		return coordTaskLine(fx.ag.db.projTaskView(pv, k))
	}
	if w := words(); !strings.Contains(w, "waiting for a person to answer: «Deploy the fix to staging now?»") {
		t.Fatalf("before the restart: %s", w)
	}
	handOff(t, fx.ag, runID)
	down.Store(true)
	b := successor(t, fx.ag)
	hwait(t, "the attach tried again", func() bool { return b.reattaching(runID) })
	if w := words(); !strings.Contains(w, "reconnecting to its coding agent") || strings.Contains(w, "waiting for a person") {
		t.Fatalf("while reconnecting: %s", w)
	}
	down.Store(false)
	hwait(t, "the attach", func() bool { return b.harnessOf(runID) != nil && !b.reattaching(runID) })
	if w := words(); !strings.Contains(w, "waiting for a person to answer") {
		t.Fatalf("once attached: %s", w)
	}
}
