// project_dream.go — a task's dream round (API.md §Project wiki, "Dreaming";
// D222): when a task's work goes to review its context is still live, so
// it is asked — goals, not steps — to improve the project wiki for the next
// agent: what it verified, how to build and test, traps and decisions,
// never status. Once, the first time its turn rests with its PR open; and
// once more when it merges, if it worked 3 turns or more since and its
// conversation (a coding agent's session) is still there — never when a
// person closed or stopped it. Project policy dream (default on) turns it
// off.
//
// The round is a platform input (source dream) through the project's queue
// with hold_park, and a turn of its own: the pump delivers it only to a
// resting run with nothing else pending, after its last work turn's verdict
// is in (D198's judge: wiki_dreams.judge_ms); what was queued after it, and
// anything queued while it runs, waits for its end (dreamHolds). Its
// delivery records that the turn taking it up is the dream's
// (wiki_dreams.active) — the write right of a built-in task's wiki tools,
// the ingest of a coding agent's pages and the judge's skip key off that,
// never off turn_by. It never answers a person's pending question: it
// waits behind it (coordHoldPark, harness_pass.go's replyPrompt). A
// person's own message goes straight in as ever; the dream turn then just
// ends with it.
//
// A coding agent writes whole pages to its task's own folder,
// <project dir>/.xbin/wiki-out/<n>/<slug>.md (small tasks share the
// project's sandbox), with the header of the copy it read; after its turn
// jobWikiIngest (project_dream_ingest.go) applies those written since the
// round began with compare-and-swap against that version. The end dream
// holds the task's cleanup back (at most dreamCleanHold) until its pages
// are in.
package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	pjWikiIngest    = "wikiingest"
	dreamEndTurns   = 3  // turns since the last dream that earn one at the end
	dreamRoundsMax  = 2  // ingest rounds (the first + one follow-up)
	dreamFilesMax   = 50 // page files read per round
	dreamFileMaxLen = wikiPageMax + 4<<10
)

// dreamJudgeWait is how long a dream waits for its work turn's verdict
// before going anyway (a judgement a restart cut off); dreamCleanHold how
// long the end dream holds the task's cleanup back at most. Vars: tests.
var (
	dreamJudgeWait = judgeTimeout + 30*time.Second
	dreamCleanHold = 30 * time.Minute
)

func init() {
	projectJobKinds[pjWikiIngest] = jobWikiIngest
	projectEventHooks = append(projectEventHooks, dreamPROpened)
	taskChangedHooks = append(taskChangedHooks, dreamTaskChanged)
	judgeDone = dreamJudged
}

// dreamRow is a task's wiki_dreams row.
type dreamRow struct {
	Turns, Dreams, Round         int
	PRDone, EndDone              bool
	Tokens, LastTotal            int64
	Active, LastDream, HoldClean bool
	StartedMs, JudgeMs           int64
}

func (d *DB) dreamOf(taskID int64) dreamRow {
	var r dreamRow
	var pr, end, act, last, hold int
	_ = d.q.QueryRow(`SELECT turns, dreams, round, pr_done, end_done, tokens, last_total, active, started_ms, last_dream, judge_ms, hold_clean
		FROM wiki_dreams WHERE task_id=?`, taskID).
		Scan(&r.Turns, &r.Dreams, &r.Round, &pr, &end, &r.Tokens, &r.LastTotal, &act, &r.StartedMs, &last, &r.JudgeMs, &hold)
	r.PRDone, r.EndDone, r.Active, r.LastDream, r.HoldClean = pr != 0, end != 0, act != 0, last != 0, hold != 0
	return r
}

func (d *DB) putDream(k *ProjectTask, r dreamRow) {
	_, _ = d.q.Exec(`INSERT INTO wiki_dreams (task_id, project_id, n, turns, dreams, round, pr_done, end_done, tokens, last_total,
		active, started_ms, last_dream, judge_ms, hold_clean, updated_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(task_id) DO UPDATE SET turns=excluded.turns, dreams=excluded.dreams, round=excluded.round, pr_done=excluded.pr_done,
		end_done=excluded.end_done, tokens=excluded.tokens, last_total=excluded.last_total, active=excluded.active,
		started_ms=excluded.started_ms, last_dream=excluded.last_dream, judge_ms=excluded.judge_ms, hold_clean=excluded.hold_clean,
		updated_ms=excluded.updated_ms`, k.ID, k.ProjectID, k.N, r.Turns, r.Dreams, r.Round, b2i(r.PRDone), b2i(r.EndDone), r.Tokens,
		r.LastTotal, b2i(r.Active), r.StartedMs, b2i(r.LastDream), r.JudgeMs, b2i(r.HoldClean), nowMs())
}

// dreamTurnEnd (projectTurnEnd, in the transaction that ends task k's
// turn): whether the turn was the dream's. A dream turn adds its tokens,
// and a coding agent's queues the ingest of its pages (a built-in task's
// end dream lets its cleanup go now); a work turn counts, its verdict is
// due when it answered, and a task resting with its PR open dreams, once.
func dreamTurnEnd(t *DB, p *Project, k *ProjectTask, cur *Run, why string) bool {
	if !t.features {
		return false
	}
	r := t.dreamOf(k.ID)
	total := int64(cur.PromptTokens + cur.CompletionTokens)
	dream := r.Active
	if dream {
		if total > r.LastTotal {
			r.Tokens += total - r.LastTotal
		}
		r.Turns, r.Active, r.LastDream, r.JudgeMs = 0, false, true, 0
		if cur.Engine == engineHarness && p.State == projActive {
			_, _ = t.queueJob(p.ID, k.ID, "", pjWikiIngest, "", 0)
		} else if r.HoldClean {
			r.HoldClean = false
			dreamLetClean(t, k)
		}
	} else {
		r.Turns++
		r.LastDream, r.JudgeMs = false, 0
		if why == turnAnswered {
			r.JudgeMs = nowMs() // judgeTurn judges it after this commit: the dream waits for its verdict
		}
	}
	r.LastTotal = total
	t.putDream(k, r)
	if !dream {
		dreamMaybe(t, p, k, cur, false)
	}
	return dream
}

// dreamJudged (judge.go's judgeDone): run's judgement is over (stored or
// not) — a dream waiting for it may go.
func dreamJudged(runID int64) {
	ag := projAg()
	if ag == nil || !ag.db.features {
		return
	}
	var pid int64
	_ = ag.db.Tx(func(t *DB) error {
		k := t.taskByRun(runID)
		if k == nil {
			return nil
		}
		if res, err := t.q.Exec(`UPDATE wiki_dreams SET judge_ms=0 WHERE task_id=? AND judge_ms<>0`, k.ID); err == nil && rowsAffected(res) > 0 {
			pid = k.ProjectID
		}
		return nil
	})
	if pid != 0 {
		projectPump(pid)
	}
}

// dreamTurnJustEnded (judgeTurn): run's turn that just ended was its dream
// round — not judged: the verdict stays its work turn's.
func dreamTurnJustEnded(t *DB, runID int64) bool {
	var v int
	_ = t.q.QueryRow(`SELECT w.last_dream FROM wiki_dreams w JOIN project_tasks k ON k.id=w.task_id WHERE k.run_id=?`, runID).Scan(&v)
	return v != 0
}

// dreamRunning (judgeStatusChanged): run's turn taking up its dream round
// starts — its work turn's verdict stays.
func dreamRunning(t *DB, runID int64) bool {
	var v int
	_ = t.q.QueryRow(`SELECT w.active FROM wiki_dreams w JOIN project_tasks k ON k.id=w.task_id WHERE k.run_id=?`, runID).Scan(&v)
	return v != 0
}

// dreamHolds (projectPump, for each queued input in order): whether in
// waits. A dream round goes alone — to a resting run with nothing pending
// and its work turn's verdict in — and what comes after it waits behind
// it; while its turn runs nothing else of the task is delivered. held
// carries the tasks held so far in this pump.
func dreamHolds(t *DB, k *ProjectTask, run *Run, in taskInput, held map[int64]bool) bool {
	if held[k.N] {
		return true
	}
	r := t.dreamOf(k.ID)
	if r.Active && dreamStale(t, run, r) {
		r.Active = false // its input was taken back or dropped before a turn took it up (a cancel, a close)
		t.putDream(k, r)
	}
	if r.Active {
		held[k.N] = true
		return true
	}
	if in.Source != srcDream {
		return false
	}
	judging := r.JudgeMs > 0 && nowMs()-r.JudgeMs < dreamJudgeWait.Milliseconds()
	if !resting(run.Status) || t.inputPending(run.ID) || judging {
		held[k.N] = true
		return true
	}
	return false
}

// dreamStale: an active dream whose input no turn has, and no turn takes
// up — the run rests and its inbox holds no dream round.
func dreamStale(t *DB, run *Run, r dreamRow) bool {
	if !resting(run.Status) || nowMs()-r.StartedMs < 5000 {
		return false
	}
	var n int
	_ = t.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0 AND kind IN (?, ?)
		AND json_extract(CASE WHEN json_valid(body) THEN body ELSE '{}' END, '$.source')=?`, run.ID, inboxUser, inboxHPrompt, srcDream).Scan(&n)
	return n == 0
}

// inputPending: run's inbox holds an input it hasn't taken up.
func (d *DB) inputPending(runID int64) bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0 AND kind IN (?, ?, ?)`, runID, inboxUser, inboxHPrompt, inboxWake).Scan(&n)
	return n > 0
}

// dreamStarted (projectPump): task k's dream round was delivered — the
// turn that takes it up is the dream's.
func dreamStarted(t *DB, k *ProjectTask) {
	r := t.dreamOf(k.ID)
	r.Active, r.StartedMs, r.LastDream = true, nowMs(), false
	t.putDream(k, r)
}

// dreamPROpened (projectEventHooks): a pull request opened while the task
// rests — the usual way: auto-PR after its turn. The event is written
// before the task's row says phase pr: it looks after the commit.
func dreamPROpened(t *DB, p *Project, ev *ProjectEvent) {
	if ev.Kind != pevPROpened || ev.N <= 0 || !t.features {
		return
	}
	pid, n := p.ID, ev.N
	t.AfterCommit(func() {
		ag := projAg()
		if ag == nil {
			return
		}
		_ = ag.db.Tx(func(t *DB) error {
			p, err := t.getProject(pid)
			if err != nil {
				return nil
			}
			if k, err := t.taskByN(pid, n); err == nil {
				dreamOnPR(t, p, k)
			}
			return nil
		})
	})
}

// dreamOnPR: task k's PR is open — it dreams if its turn rests.
func dreamOnPR(t *DB, p *Project, k *ProjectTask) {
	if k.RunID == 0 {
		return
	}
	if run, err := t.getRun(k.RunID); err == nil {
		dreamMaybe(t, p, k, run, false)
	}
}

// dreamTaskChanged (taskChangedHooks): a task merged — its end dream. A
// task a person closed (or whose PR closed unmerged) has none.
func dreamTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if what != "phase" || k.Phase != phaseMerged || k.RunID == 0 || !t.features {
		return
	}
	if run, err := t.getRun(k.RunID); err == nil {
		dreamMaybe(t, p, k, run, true)
	}
}

// dreamMaybe queues task k's dream round when it is due: at rest with an
// open PR (the first time), or at its merge after dreamEndTurns turns —
// then holding its cleanup back until the round's pages are in.
func dreamMaybe(t *DB, p *Project, k *ProjectTask, run *Run, end bool) {
	if !policyOf(p.Policy).Dream || p.State != projActive || p.Kind == projTeam || run.ParentID != 0 {
		return
	}
	r := t.dreamOf(k.ID)
	if end {
		if r.EndDone || r.Turns < dreamEndTurns || !dreamSessionLives(t, run) || k.WS != wsReady || run.Status == statusCanceled {
			return
		}
		r.EndDone = true
	} else {
		// a turn of it ended since dreaming came in (its row counts them):
		// a task from before has none, and dreams when it works again
		if r.PRDone || r.Turns == 0 || k.Phase != phasePR || !resting(run.Status) || run.Status == statusCanceled || !openPR(k) {
			return
		}
		r.PRDone = true
	}
	r.Dreams++
	r.Round = 0
	which := "pr"
	if end {
		which = "end"
	}
	text := dreamPromptFor(p, k, run.Engine == engineHarness, end)
	if _, err := queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcDream, HoldPark: true,
		Dedupe: fmt.Sprintf("dream:%d:%s", k.N, which)}); err != nil {
		logf("project %d task %d: queueing its dream round: %v", p.ID, k.N, err)
		return
	}
	if end { // the merge queued its cleanup just before: it waits for the round (dreamLetClean), at most dreamCleanHold
		if res, err := t.q.Exec(`UPDATE project_jobs SET next_ms=? WHERE task_id=? AND kind=? AND state='queued'`,
			nowMs()+dreamCleanHold.Milliseconds(), k.ID, pjCleanup); err == nil && rowsAffected(res) > 0 {
			r.HoldClean = true
		}
	}
	t.putDream(k, r)
	addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": "its dream round: it adds what it learned to the project wiki"}, false, "")
}

// dreamLetClean: the end dream is over — the task's cleanup it held back
// goes now.
func dreamLetClean(t *DB, k *ProjectTask) {
	if res, err := t.q.Exec(`UPDATE project_jobs SET next_ms=? WHERE task_id=? AND kind=? AND state='queued'`, nowMs(), k.ID, pjCleanup); err == nil && rowsAffected(res) > 0 {
		t.AfterCommit(kickProjectWorker)
	}
}

// dreamSessionLives: the run's conversation can still take a turn — a
// built-in run's always, a coding agent's while its session is kept.
func dreamSessionLives(t *DB, run *Run) bool {
	if run.Engine != engineHarness {
		return true
	}
	hs, err := t.harnessSession(run.ID)
	return err == nil && hs != nil && hs.ACPSession != ""
}

func openPR(k *ProjectTask) bool {
	for _, pr := range k.taskPRs() {
		if pr.State == "open" {
			return true
		}
	}
	return false
}

// wikiOutFor is where task k's coding agent writes its dream round's pages:
// a folder of its own (small tasks share the project's sandbox).
func wikiOutFor(p *Project, k *ProjectTask) string {
	return wikiOutDir(p) + "/" + strconv.FormatInt(k.N, 10)
}

// dreamPromptFor is the round's prompt (a var: tests drive the fake coding
// agent with it).
var dreamPromptFor = dreamPrompt

// dreamPrompt steers goals, not steps (the article's advice): what a good
// wiki holds for the next agent, and what it never holds.
func dreamPrompt(p *Project, k *ProjectTask, harness, end bool) string {
	var b strings.Builder
	b.WriteString("[dream round — from the platform, not a person]\n")
	if end {
		b.WriteString("This task is over. ")
	} else {
		b.WriteString("Your work on this task goes to review now. ")
	}
	b.WriteString(`Before you stop, improve the project wiki for the next agent who works on this project.
Keep what you verified while working: facts with their file paths, how to build, test and run things, traps you hit and how you got past them, decisions and why. Quote identifiers, commands and error messages verbatim.
Update the page a fact belongs on rather than adding a near-duplicate. The index page (slug ` + "`index`" + `) is a short map of the pages (at most 60 lines): add a line to ` + "`index`" + ` only for a new page.
Never record task status, branches or pull requests, or anything that will be stale next week. If you learned nothing worth keeping, change nothing.
Just before you write a page, read it again: if another agent already recorded the fact, change nothing.
Don't mark what you add with your task number or name: the history records who wrote what.
` + wikiProvenance + `
Don't change code or push in this round.
`)
	if harness {
		fmt.Fprintf(&b, "The wiki's pages are in %s (read-only; index.md is the map). To add or change a page, write the whole page to %s/<slug>.md (make the folder if it isn't there): "+
			"its first line is the header of the copy you read, `<!-- wiki: <slug> version <N> -->` — `version 0` for a new page — then `# Title`, then the page in Markdown without the title again "+
			"([[slug]] links to other pages, at most 16 KB). A slug is lowercase letters, digits and dashes.\n", wikiDir(p), wikiOutFor(p, k))
	} else {
		b.WriteString("Use wiki_list, wiki_search and wiki_read, then wiki_write or wiki_edit — read a page before you change it.\n")
	}
	b.WriteString("Answer with one line: what you changed in the wiki.")
	return b.String()
}
