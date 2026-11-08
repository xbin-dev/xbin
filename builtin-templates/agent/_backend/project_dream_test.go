package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openPRs gives task k an open pull request and phase pr (in the store).
func openPRs(t *testing.T, d *DB, k *ProjectTask) *ProjectTask {
	t.Helper()
	b, _ := json.Marshal([]TaskPR{{Repo: "acme/web", Number: 7, State: "open", Checks: "none"}})
	if err := d.setTask(k.ID, map[string]any{"phase": phasePR, "prs": string(b)}); err != nil {
		t.Fatal(err)
	}
	cur, err := d.taskByID(k.ID)
	if err != nil {
		t.Fatal(err)
	}
	return cur
}

// dreamsQueued is the dream rounds waiting in pid's queue.
func dreamsQueued(d *DB, pid int64) []taskInput {
	var out []taskInput
	for _, in := range d.queuedInputs(pid, 0) {
		if in.Source == srcDream {
			out = append(out, in)
		}
	}
	return out
}

// dreamsIn is the dream rounds in run's inbox (delivered or not).
func dreamsIn(d *DB, runID int64) []*InboxRow {
	var out []*InboxRow
	for _, r := range d.inboxRows(`WHERE run_id=? AND kind IN ('user','hprompt')`, runID) {
		if r.Body.Source == srcDream {
			out = append(out, r)
		}
	}
	return out
}

// dreamTexts is every dream round of task run in pid: queued, then in its inbox.
func dreamTexts(d *DB, pid, runID int64) []string {
	var out []string
	for _, in := range dreamsQueued(d, pid) {
		out = append(out, in.Text)
	}
	for _, r := range dreamsIn(d, runID) {
		out = append(out, r.Body.Text)
	}
	return out
}

// When a task dreams: the first time its turn rests with its PR open (the
// PR opening while it rests counts: the pr.opened event), never twice — and
// not a task from before dreaming came in (no turn of it ended since); at
// its end only after 3 turns since; never with policy dream off.
func TestDreamTriggers(t *testing.T) {
	fx := newWikiFix(t)
	d := fx.ag.db
	stopEngineFirst(t, fx.ag)
	fx.ag.eng.Shutdown(2 * time.Second) // the store only: nothing delivers what is queued
	p, _ := d.getProject(fx.p.ID)
	k := openPRs(t, d, fx.k)
	_ = d.Tx(func(tx *DB) error { dreamOnPR(tx, p, k); return nil })
	if q := dreamTexts(d, p.ID, fx.task.ID); len(q) != 0 {
		t.Fatalf("a task with no turn since dreaming came in dreams: %q", q)
	}
	_ = d.Tx(func(tx *DB) error { tx.putDream(k, dreamRow{Turns: 1}); return nil })
	_ = d.Tx(func(tx *DB) error {
		addProjectEvent(tx, p.ID, k.N, pevPROpened, map[string]any{"text": "opened PR #7 on acme/web"}, false, "")
		return nil
	})
	q := dreamTexts(d, p.ID, fx.task.ID)
	if len(q) != 1 || !strings.Contains(q[0], "improve the project wiki") || !strings.Contains(q[0], "wiki_write") {
		t.Fatalf("the PR's dream round: %q", q)
	}
	_ = d.Tx(func(tx *DB) error { dreamOnPR(tx, p, k); dreamTurnEnd(tx, fx.task, turnAnswered, "", ""); return nil })
	if n := len(dreamTexts(d, p.ID, fx.task.ID)); n != 1 {
		t.Fatalf("dream rounds after a second rest: %d", n)
	}
	d.dropQueued(p.ID, 1)
	_, _ = d.q.Exec(`DELETE FROM inbox WHERE run_id=?`, fx.task.ID)
	// the end: two turns since aren't enough, three are
	_ = d.Tx(func(tx *DB) error {
		r := tx.dreamOf(k.ID)
		r.Turns = 2
		tx.putDream(k, r)
		k.Phase = phaseMerged
		dreamTaskChanged(tx, p, k, "phase")
		return nil
	})
	if n := len(dreamTexts(d, p.ID, fx.task.ID)); n != 0 {
		t.Fatalf("an end dream after 2 turns: %d", n)
	}
	_ = d.Tx(func(tx *DB) error {
		r := tx.dreamOf(k.ID)
		r.Turns = 3
		tx.putDream(k, r)
		dreamTaskChanged(tx, p, k, "phase")
		dreamTaskChanged(tx, p, k, "phase")
		return nil
	})
	if q := dreamTexts(d, p.ID, fx.task.ID); len(q) != 1 || !strings.Contains(q[0], "This task is over") {
		t.Fatalf("the end's dream round: %q", q)
	}
	if r := d.dreamOf(k.ID); r.Dreams != 2 || !r.PRDone || !r.EndDone {
		t.Fatalf("the dream row: %+v", r)
	}
	// policy dream off: none
	k2, _ := fx.addTask(t, fx.p, 2, "alice")
	_, _ = d.q.Exec(`UPDATE projects SET policy=? WHERE id=?`, `{"dream":false}`, p.ID)
	p, _ = d.getProject(p.ID)
	k2 = openPRs(t, d, k2)
	_ = d.Tx(func(tx *DB) error { tx.putDream(k2, dreamRow{Turns: 1}); dreamOnPR(tx, p, k2); return nil })
	if q := dreamTexts(d, p.ID, k2.RunID); len(q) != 0 {
		t.Fatalf("a dream round with policy dream off: %q", q)
	}
}

// The dream round never answers a person's pending question: queued while
// the task waits, it is delivered only after the person answered; one
// delivered just before the task parked goes back to the queue, held; a
// coding agent's pass never takes it for the reply to its park. Its
// answer isn't the task's latest answer.
func TestDreamHeldBehindQuestion(t *testing.T) {
	fx := newWikiFix(t)
	d, f := fx.ag.db, fakeOf(fx.ag)
	f.on(forRun(fx.task.ID, lastUser("please ask me")), callTools(tc("a1", "ask_user", `{"question":"which colour?"}`))).once()
	f.on(forRun(fx.task.ID, lastUser("the blue one")), say("painted it blue")).once()
	f.on(forRun(fx.task.ID, lastUser("[dream round")), say("wiki: nothing new")).once()
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", fx.task.ID), map[string]any{"text": "please ask me"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the task's question", func() bool { return statusOf(d, fx.task.ID) == statusWaiting })
	p, _ := d.getProject(fx.p.ID)
	k := openPRs(t, d, fx.k)
	_ = d.Tx(func(tx *DB) error {
		r := tx.dreamOf(k.ID)
		r.Turns = 3
		tx.putDream(k, r)
		k.Phase = phaseMerged
		_ = tx.setTask(k.ID, map[string]any{"phase": phaseMerged})
		dreamTaskChanged(tx, p, k, "phase")
		return nil
	})
	projectPump(p.ID)
	fx.ag.eng.Poke(fx.task.ID)
	time.Sleep(100 * time.Millisecond)
	if q := dreamsQueued(d, p.ID); len(q) != 1 || !q[0].HoldPark {
		t.Fatalf("the dream round isn't held: %+v", q)
	}
	if rows := dreamsIn(d, fx.task.ID); len(rows) != 0 {
		t.Fatalf("the dream round reached the waiting task: %+v", rows[0].Body)
	}
	if statusOf(d, fx.task.ID) != statusWaiting {
		t.Fatalf("the question was answered: %s", statusOf(d, fx.task.ID))
	}
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", fx.task.ID), map[string]any{"text": "the blue one"}); w.Code != 200 {
		t.Fatalf("the answer: %d %s", w.Code, w.Body)
	}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("queue %+v; inbox %d; status %s; calls %d", d.queuedInputs(fx.p.ID, 0), len(d.inboxRows(`WHERE run_id=?`, fx.task.ID)),
				statusOf(d, fx.task.ID), len(f.callsFor(fx.task.ID)))
			dumpRuns(t, d)
		}
	})
	waitFor(t, "the dream round's turn", func() bool {
		return len(f.callsFor(fx.task.ID)) >= 3 && !active(statusOf(d, fx.task.ID)) && len(dreamsQueued(d, p.ID)) == 0
	})
	text := fullText(d, fx.task.ID)
	if i, j := strings.Index(text, "the blue one"), strings.Index(text, "[dream round"); i < 0 || j < i {
		t.Fatalf("the dream round came before the answer:\n%s", text)
	}
	if k, _ := d.taskByID(fx.k.ID); k.Last != "painted it blue" || k.TurnBy != srcDream {
		t.Fatalf("the task's latest answer %q (turn by %s)", k.Last, k.TurnBy)
	}
	// delivered while it worked, then it parked: back to the queue, held
	raw, _ := json.Marshal(pendingState{Kind: "approval", ToolCalls: []toolCall{tc("c1", "bash", `{"command":"ls"}`)}, Park: newPark()})
	if err := d.Tx(func(tx *DB) error {
		if _, _, err := tx.enqueue(fx.task.ID, inboxUser, inboxBody{Text: "[dream round] again", Source: srcDream}, ""); err != nil {
			return err
		}
		return tx.setStatus(fx.task.ID, statusWaiting, 0, "approve", string(raw))
	}); err != nil {
		t.Fatal(err)
	}
	if q := dreamsQueued(d, p.ID); len(q) != 1 || !q[0].HoldPark || q[0].Text != "[dream round] again" {
		t.Fatalf("the dream round wasn't taken back: %+v", q)
	}
	// a coding agent's pass: a dream prompt is no reply to a park
	h := sortHarnessInbox([]*InboxRow{{ID: 1, Kind: inboxHPrompt, Body: inboxBody{Text: "dream", Source: srcDream}}})
	if len(h.prompt) != 1 || h.replyPrompt() != nil {
		t.Fatalf("a dream prompt answers a coding agent's park: %+v", h.replyPrompt())
	}
}

// A coding agent's wiki: before its prompt, the gate has the sandbox's
// copy written (.xbin/wiki/<slug>.md, read-only, each with its header);
// its dream round's pages (.xbin/wiki-out) are applied with compare-and-
// swap against the version in their header — a new page lands, one another
// writer changed comes back once with the conflict's words, the copy
// re-synced; after a second round what is left is dropped with a task event.
func TestDreamIngestHarness(t *testing.T) {
	fx, runID := harnessTaskFix(t, "fix the header", nil)
	d := fx.ag.db
	hwait(t, "the coding agent's first turn", func() bool {
		return turnOver(fx.ag, runID)() && strings.Contains(fullText(d, runID), "echo: ")
	})
	k := d.taskByRun(runID)
	p, _ := d.getProject(k.ProjectID)
	zero := int64(0)
	if _, err := d.wikiPut(p.ID, wikiActor{By: "alice"}, wikiChange{Slug: "notes", Title: "Notes", Body: "Build with make.", Base: &zero}); err != nil {
		t.Fatal(err)
	}
	one := int64(1)
	if _, err := d.wikiPut(p.ID, wikiActor{By: "bob"}, wikiChange{Slug: "notes", Body: "Build with make; test with make test.", Base: &one}); err != nil {
		t.Fatal(err)
	}
	// the coding agent's pages, as it would write them in its round (the
	// fake agent has no file system of its own here: no agent host)
	out := wikiOutDir(p)
	write := func(slug string, v int, body string) {
		t.Helper()
		if err := os.MkdirAll(out, 0o755); err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("<!-- wiki: %s version %d -->\n# %s\n\n%s\n", slug, v, strings.ToUpper(slug[:1])+slug[1:], body)
		if err := os.WriteFile(filepath.Join(out, slug+".md"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("notes", 1, "Build with make; lint with make lint.")
	write("fresh", 0, "A new page.\n\n<!-- sources: Makefile; ci.yml -->")
	oldFollow := dreamFollowUpFor
	var followUp string
	dreamFollowUpFor = func(p *Project, lost, bad []string) string {
		followUp = dreamFollowUp(p, lost, bad)
		write("notes", 1, "Still from version 1.")
		return followUp
	}
	t.Cleanup(func() { dreamFollowUpFor = oldFollow })
	k = openPRs(t, d, k)
	_ = d.Tx(func(tx *DB) error { dreamOnPR(tx, p, k); return nil })
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("jobs: %s", jobsDump(d, p.ID))
			for _, ev := range d.projectEvents(p.ID, 0, 200) {
				t.Logf("event %s #%d %s", ev.Kind, ev.N, ev.Body)
			}
			t.Logf("queue: %+v", d.queuedInputs(p.ID, 0))
			t.Logf("transcript: %s", clip(fullText(d, runID), 4000))
		}
	})
	hwait(t, "the dream round's pages applied, the follow-up's dropped", func() bool {
		for _, ev := range d.projectEvents(p.ID, 0, 200) {
			if strings.Contains(string(ev.Body), "left out after 2 rounds") {
				return turnOver(fx.ag, runID)()
			}
		}
		return false
	})
	fresh, err := d.wikiPage(p.ID, "fresh")
	if err != nil || fresh.Version != 1 || fresh.Body != "A new page." || fresh.UpdatedBy != "task #1 (dream)" || fresh.Title != "Fresh" ||
		strings.Join(fresh.Sources, "|") != "Makefile|ci.yml" {
		t.Fatalf("the new page: %+v %v", fresh, err)
	}
	if notes, _ := d.wikiPage(p.ID, "notes"); notes.Version != 2 || notes.UpdatedBy != "bob" {
		t.Fatalf("the page another changed was overwritten: %+v", notes)
	}
	if !strings.Contains(followUp, "notes: "+wikiConflict) {
		t.Fatalf("the follow-up lacks the conflict's words:\n%s", followUp)
	}
	text := fullText(d, runID)
	if strings.Count(text, "[dream round") != 2 || !strings.Contains(text, out+"/<slug>.md") || !strings.Contains(text, wikiConflict) {
		t.Fatalf("the dream round's prompts:\n%s", clip(text, 4000))
	}
	// the copy: every page, its header first, read-only; re-synced after the conflict
	b, err := os.ReadFile(filepath.Join(wikiDir(p), "notes.md"))
	if err != nil || !strings.HasPrefix(string(b), "<!-- wiki: notes version 2 -->\n# Notes\n") {
		t.Fatalf("the copy of notes: %v %q", err, b)
	}
	if b, err := os.ReadFile(filepath.Join(wikiDir(p), "fresh.md")); err != nil || !strings.HasPrefix(string(b), "<!-- wiki: fresh version 1 -->") {
		t.Fatalf("the copy of fresh: %v %q", err, b)
	}
	if st, err := os.Stat(filepath.Join(wikiDir(p), "notes.md")); err != nil || st.Mode().Perm()&0o222 != 0 {
		t.Fatalf("the copy is writable: %v %v", st.Mode(), err)
	}
	if d.wikiSynced(taskRef(p, k), p.ID) != d.wikiSeq(p.ID) {
		t.Fatal("the sync's watermark lags")
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Fatalf("wiki-out keeps files: %v", left)
	}
}
