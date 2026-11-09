package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	_ = d.Tx(func(tx *DB) error {
		dreamOnPR(tx, p, k)
		run, _ := tx.getRun(fx.task.ID)
		dreamTurnEnd(tx, p, k, run, turnAnswered)
		return nil
	})
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
	// the coding agent's pages, as it would write them in its round, in its
	// task's own folder (the fake agent has no file system of its own here:
	// no agent host). Written now, they carry a time within the round; one
	// left from before it, and another task's, are never this round's.
	out := wikiOutFor(p, k)
	writeIn := func(dir, slug string, v int, body string, at time.Time) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		text := fmt.Sprintf("<!-- wiki: %s version %d -->\n# %s\n\n%s\n", slug, v, strings.ToUpper(slug[:1])+slug[1:], body)
		f := filepath.Join(dir, slug+".md")
		if err := os.WriteFile(f, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(f, at, at); err != nil {
			t.Fatal(err)
		}
	}
	during := time.Now().Add(10 * time.Minute)
	write := func(slug string, v int, body string) { t.Helper(); writeIn(out, slug, v, body, during) }
	write("notes", 1, "Build with make; lint with make lint.")
	write("fresh", 0, "A new page.\n\n<!-- sources: Makefile; ci.yml -->")
	writeIn(out, "stale", 0, "Left from before the round.", time.Now().Add(-time.Hour))
	other := filepath.Join(wikiOutDir(p), "2")
	writeIn(other, "theirs", 0, "Task 2's page.", during)
	oldFollow := dreamFollowUpFor
	var followUp string
	var rejectedThen string
	dreamFollowUpFor = func(p *Project, k *ProjectTask, lost, bad []string) string {
		followUp = dreamFollowUp(p, k, lost, bad)
		if b, err := os.ReadFile(filepath.Join(out, "notes.md.rejected")); err == nil { // what the agent merges from
			rejectedThen = string(b)
		}
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
	if !strings.Contains(rejectedThen, "lint with make lint") || !strings.Contains(followUp, "notes.md.rejected — merge it into the fresh copy") {
		t.Fatalf("the rejected page wasn't kept for the follow-up: %q\n%s", rejectedThen, followUp)
	}
	if !strings.Contains(followUp, "notes: Another agent changed the contents of this article since you last read it (now version 2, by bob) — read the article again before editing.") {
		t.Fatalf("the follow-up lacks the conflict's words:\n%s", followUp)
	}
	text := fullText(d, runID)
	if strings.Count(text, "[dream round") != 2 || !strings.Contains(text, out+"/<slug>.md") || !strings.Contains(text, "since you last read it (now version 2, by bob)") {
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
	for _, slug := range []string{"stale", "theirs"} {
		if _, err := d.wikiPage(p.ID, slug); err == nil {
			t.Errorf("%s was ingested: not this task's round's", slug)
		}
	}
	if _, err := os.Stat(filepath.Join(other, "theirs.md")); err != nil {
		t.Fatalf("another task's page file was touched: %v", err)
	}
	if left, _ := os.ReadDir(out); len(left) != 0 {
		t.Fatalf("wiki-out keeps files: %v", left)
	}
}

// stillPump is the wiki fixture with its engine stopped: the queue and the
// pump alone, a turn played by hand.
func stillPump(t *testing.T) (*wikiFix, *Project) {
	t.Helper()
	fx := newWikiFix(t)
	stopEngineFirst(t, fx.ag)
	fx.ag.eng.Shutdown(2 * time.Second)
	_ = fx.ag.db.Tx(func(tx *DB) error { return tx.setStatus(fx.task.ID, statusIdle, 0, "", "") })
	p, _ := fx.ag.db.getProject(fx.p.ID)
	return fx, p
}

// undelivered is run's inbox inputs not taken up yet, by source.
func undelivered(d *DB, runID int64) []string {
	var out []string
	for _, r := range d.inboxRows(`WHERE run_id=? AND delivered_at=0 AND kind IN ('user','hprompt')`, runID) {
		out = append(out, r.Body.Source)
	}
	return out
}

// The review's case: a coordinator's message and a dream round queued
// together were delivered by one pump, one turn on both, turn_by "dream".
// Now the dream round is a turn of its own: the message goes first, the
// round only once nothing else is pending; while its turn runs, what is
// queued for the task waits; that the turn is the dream's is recorded when
// it is delivered — the wiki's write right and the turn's end key off it.
func TestDreamTurnAlone(t *testing.T) {
	fx, p := stillPump(t)
	d := fx.ag.db
	queue := func(src, text, dedupe string) {
		t.Helper()
		if err := d.Tx(func(tx *DB) error {
			_, err := queueTaskInput(tx, taskInput{Project: p.ID, N: 1, Kind: "input", Text: text, Source: src, HoldPark: true, Dedupe: dedupe})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	takeUp := func() {
		_, _ = d.q.Exec(`UPDATE inbox SET delivered_at=? WHERE run_id=? AND delivered_at=0`, nowMs(), fx.task.ID)
	}
	queue(srcCoordinator, "please fix the login bug", "")
	queue(srcDream, "[dream round — x", "dream:1:pr")
	projectPump(p.ID)
	if got := undelivered(d, fx.task.ID); strings.Join(got, ",") != srcCoordinator || len(dreamsQueued(d, p.ID)) != 1 {
		t.Fatalf("delivered together: %v (queued dreams %d)", got, len(dreamsQueued(d, p.ID)))
	}
	if k, _ := d.taskByN(p.ID, 1); k.TurnBy != srcCoordinator || d.dreamOf(k.ID).Active {
		t.Fatalf("the work turn: turn_by %q, dream active %v", k.TurnBy, d.dreamOf(k.ID).Active)
	}
	tcfg, _ := d.runConfig(fx.task.ID)
	if r, _ := fx.ag.wikiRoleOf(fx.task, tcfg); r == nil || r.write {
		t.Fatal("the work turn may write the wiki")
	}
	// the work turn: taken up, still running — the round waits
	takeUp()
	_ = d.Tx(func(tx *DB) error { return tx.setStatus(fx.task.ID, statusRunning, 0, "", "") })
	projectPump(p.ID)
	if len(undelivered(d, fx.task.ID)) != 0 {
		t.Fatal("the dream round reached a working run")
	}
	k, _ := d.taskByN(p.ID, 1)
	_ = d.Tx(func(tx *DB) error {
		_ = tx.setStatus(fx.task.ID, statusIdle, 0, "", "")
		cur, _ := tx.getRun(fx.task.ID)
		if dreamTurnEnd(tx, p, k, cur, turnError) {
			t.Error("the work turn ended as the dream's")
		}
		return nil
	})
	projectPump(p.ID)
	if got := undelivered(d, fx.task.ID); strings.Join(got, ",") != srcDream || !d.dreamOf(k.ID).Active {
		t.Fatalf("the round alone, recorded: %v active %v", got, d.dreamOf(k.ID).Active)
	}
	if r, _ := fx.ag.wikiRoleOf(fx.task, tcfg); r == nil || !r.write {
		t.Fatal("the dream turn may not write the wiki")
	}
	// its turn runs: a coordinator's message waits for its end
	takeUp()
	_ = d.Tx(func(tx *DB) error { return tx.setStatus(fx.task.ID, statusRunning, 0, "", "") })
	queue(srcCoordinator, "and then deploy", "")
	_ = d.Tx(func(tx *DB) error { return tx.setStatus(fx.task.ID, statusIdle, 0, "", "") })
	projectPump(p.ID)
	if got := undelivered(d, fx.task.ID); len(got) != 0 {
		t.Fatalf("delivered while the dream turn runs: %v", got)
	}
	_ = d.Tx(func(tx *DB) error {
		cur, _ := tx.getRun(fx.task.ID)
		if !dreamTurnEnd(tx, p, k, cur, turnAnswered) {
			t.Error("the dream turn's end wasn't the dream's")
		}
		return nil
	})
	projectPump(p.ID)
	if got := undelivered(d, fx.task.ID); strings.Join(got, ",") != srcCoordinator {
		t.Fatalf("after the dream turn: %v", got)
	}
	if r, _ := fx.ag.wikiRoleOf(fx.task, tcfg); r == nil || r.write {
		t.Fatal("the turn after the dream may write the wiki")
	}
}

// D198 and the dream: the work turn's verdict ("stopped part-way") is
// stored before the dream round goes, the round's start doesn't take it
// down, and the round's own turn isn't judged.
func TestDreamKeepsVerdict(t *testing.T) {
	fx := newCoordFix(t)
	if w := callAs(t, fx.mux, asMgr, "PUT", "/config", map[string]any{"models": map[string]any{"memory": "m-memory"}}); w.Code != 200 {
		t.Fatalf("config: %d %s", w.Code, w.Body)
	}
	_, _ = fx.ag.db.q.Exec(`UPDATE projects SET dir='/work/web' WHERE id=?`, fx.p.ID)
	k, task := fx.addTask(t, fx.p, 1, "alice")
	d, f := fx.ag.db, fakeOf(fx.ag)
	_, _ = d.q.Exec(`UPDATE runs SET config=json_set(config, '$.models.memory', 'm-memory') WHERE id=?`, task.ID) // its judge's model
	var asks []string
	var mu sync.Mutex
	f.on(func(r LLMRequest) bool { return r.Purpose == "judge" }, func(r LLMRequest) (LLMReply, error) {
		mu.Lock()
		asks = append(asks, asString(r.Msgs[1].Content))
		mu.Unlock()
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: `{"delivered": false, "reason": "it only made a plan"}`}}, nil
	}).block("judge")
	f.on(forRun(task.ID, lastUser("do the work")), say("Here is my plan; I'll start next.")).once()
	f.on(forRun(task.ID, lastUser("[dream round")), say("wiki: nothing new")).once()
	k = openPRs(t, d, k)
	_ = d.Tx(func(tx *DB) error { tx.putDream(k, dreamRow{}); return nil })
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", task.ID), map[string]any{"text": "do the work"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the work turn's end, the dream queued", func() bool { return !active(statusOf(d, task.ID)) && len(dreamsQueued(d, fx.p.ID)) == 1 })
	time.Sleep(100 * time.Millisecond)
	if len(dreamsQueued(d, fx.p.ID)) != 1 || len(dreamsIn(d, task.ID)) != 0 {
		t.Fatal("the dream round went before its work turn's verdict")
	}
	f.release("judge")
	hwait(t, "the dream round's turn", func() bool {
		return len(f.callsFor(task.ID)) >= 3 && !active(statusOf(d, task.ID)) && !d.dreamOf(k.ID).Active
	})
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(asks) != 1 || !strings.Contains(asks[0], "do the work") {
		t.Fatalf("the judge was asked %d time(s): %q", len(asks), asks)
	}
	if v := d.verdictOf(task.ID); v == nil || v.Delivered || v.Reason != "it only made a plan" {
		t.Fatalf("the work turn's verdict after the dream: %+v", v)
	}
}

// The end dream: only when the task merged — never when a person closed it
// — and it holds the task's cleanup back until its round is over.
func TestDreamEndHoldsCleanup(t *testing.T) {
	fx, p := stillPump(t)
	d := fx.ag.db
	k := openPRs(t, d, fx.k)
	_ = d.Tx(func(tx *DB) error { tx.putDream(k, dreamRow{Turns: 3, PRDone: true}); return nil })
	_ = d.Tx(func(tx *DB) error { scmPullEnded(tx, p, k, "acme/web", 7, true); return nil })
	cleanups := d.jobsWhere(`WHERE task_id=? AND kind=? AND state='queued'`, k.ID, pjCleanup)
	if len(cleanups) != 1 || cleanups[0].NextMs < nowMs()+(dreamCleanHold-time.Minute).Milliseconds() {
		t.Fatalf("the cleanup isn't held back for the end dream: %+v", cleanups)
	}
	if q := dreamTexts(d, p.ID, fx.task.ID); len(q) != 1 || !strings.Contains(q[0], "This task is over") {
		t.Fatalf("the end dream: %q", q)
	}
	k, _ = d.taskByID(k.ID)
	_ = d.Tx(func(tx *DB) error {
		cur, _ := tx.getRun(fx.task.ID)
		if !dreamTurnEnd(tx, p, k, cur, turnAnswered) {
			t.Error("its turn wasn't the dream's")
		}
		return nil
	})
	if cl := d.jobsWhere(`WHERE task_id=? AND kind=? AND state='queued'`, k.ID, pjCleanup); len(cl) != 1 || cl[0].NextMs > nowMs() {
		t.Fatalf("the cleanup still held after the round: %+v", cl)
	}
	// a person closes task 2: no end dream, its cleanup as it was
	k2, run2 := fx.addTask(t, fx.p, 2, "alice")
	k2 = openPRs(t, d, k2)
	_ = d.Tx(func(tx *DB) error { tx.putDream(k2, dreamRow{Turns: 5, PRDone: true}); return nil })
	if err := fx.ag.closeTask(context.Background(), p, k2, coordWho("alice"), ActOpts{Cleanup: true}); err != nil {
		t.Fatal(err)
	}
	if q := dreamTexts(d, p.ID, run2.ID); len(q) != 0 {
		t.Fatalf("a task a person closed dreams: %q", q)
	}
}
