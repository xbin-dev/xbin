// project_dream.go — a task's dream round (API.md §Project wiki, "Dreaming";
// D222): when a task's work goes to review its context is still live, so
// it is asked — goals, not steps — to improve the project wiki for the next
// agent: what it verified, how to build and test, traps and decisions,
// never status. Once, the first time its turn rests with its PR open; and
// once more at its end (merged or closed) when it worked 3 turns or more
// since, and its conversation (a coding agent's session) is still there.
// Project policy dream (default on) turns it off.
//
// The round is a platform input (source dream) through the project's queue
// with hold_park: it never answers a person's pending question — it waits
// behind it (coordHoldPark, harness_pass.go's replyPrompt). A built-in task
// writes the wiki with its tools during the round (turn_by dream). A coding
// agent writes whole pages to <project dir>/.xbin/wiki-out/<slug>.md with
// the header of the copy it read; after its turn jobWikiIngest applies each
// with compare-and-swap against that version. A page that lost re-syncs the
// copy and goes back to the agent in one follow-up prompt with the
// conflict's words; after 2 rounds what is left is dropped, with a task
// event.
package main

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	pjWikiIngest    = "wikiingest"
	dreamEndTurns   = 3  // turns since the last dream that earn one at the end
	dreamRoundsMax  = 2  // ingest rounds (the first + one follow-up)
	dreamFilesMax   = 50 // page files read per round
	dreamFileMaxLen = wikiPageMax + 4<<10
)

func init() {
	projectJobKinds[pjWikiIngest] = jobWikiIngest
	turnEndHooks = append(turnEndHooks, dreamTurnEnd)
	projectEventHooks = append(projectEventHooks, dreamPROpened)
	taskChangedHooks = append(taskChangedHooks, dreamTaskChanged)
}

// dreamRow is a task's wiki_dreams row.
type dreamRow struct {
	Turns, Dreams, Round int
	PRDone, EndDone      bool
	Tokens, LastTotal    int64
}

func (d *DB) dreamOf(taskID int64) dreamRow {
	var r dreamRow
	var pr, end int
	_ = d.q.QueryRow(`SELECT turns, dreams, round, pr_done, end_done, tokens, last_total FROM wiki_dreams WHERE task_id=?`, taskID).
		Scan(&r.Turns, &r.Dreams, &r.Round, &pr, &end, &r.Tokens, &r.LastTotal)
	r.PRDone, r.EndDone = pr != 0, end != 0
	return r
}

func (d *DB) putDream(k *ProjectTask, r dreamRow) {
	_, _ = d.q.Exec(`INSERT INTO wiki_dreams (task_id, project_id, n, turns, dreams, round, pr_done, end_done, tokens, last_total, updated_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_id) DO UPDATE SET turns=excluded.turns, dreams=excluded.dreams,
		round=excluded.round, pr_done=excluded.pr_done, end_done=excluded.end_done, tokens=excluded.tokens, last_total=excluded.last_total,
		updated_ms=excluded.updated_ms`, k.ID, k.ProjectID, k.N, r.Turns, r.Dreams, r.Round, b2i(r.PRDone), b2i(r.EndDone), r.Tokens, r.LastTotal, nowMs())
}

// dreamTurnEnd (turnEndHooks): a task's turn ended. A dream round's adds
// its tokens and, for a coding agent, queues the ingest of its page files;
// any other counts a turn. Then a task resting with its PR open dreams, once.
func dreamTurnEnd(t *DB, run *Run, why, outcome, result string) {
	if run.ParentID != 0 || run.Origin != originProject || !t.features {
		return
	}
	k := t.taskByRun(run.ID)
	if k == nil {
		return
	}
	p, err := t.getProject(k.ProjectID)
	if err != nil {
		return
	}
	cur, err := t.getRun(run.ID)
	if err != nil {
		return
	}
	r := t.dreamOf(k.ID)
	total := int64(cur.PromptTokens + cur.CompletionTokens)
	if k.TurnBy == srcDream {
		if total > r.LastTotal {
			r.Tokens += total - r.LastTotal
		}
		r.Turns = 0
		if cur.Engine == engineHarness && p.State == projActive {
			_, _ = t.queueJob(p.ID, k.ID, "", pjWikiIngest, "", 0)
		}
	} else {
		r.Turns++
	}
	r.LastTotal = total
	t.putDream(k, r)
	if k.TurnBy != srcDream {
		dreamMaybe(t, p, k, cur, false)
	}
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

// dreamTaskChanged (taskChangedHooks): a task ended (merged or closed).
func dreamTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if what != "phase" || (k.Phase != phaseMerged && k.Phase != phaseClosed) || k.RunID == 0 || !t.features {
		return
	}
	if run, err := t.getRun(k.RunID); err == nil {
		dreamMaybe(t, p, k, run, true)
	}
}

// dreamMaybe queues task k's dream round when it is due: at rest with an
// open PR (the first time), or at its end after dreamEndTurns turns.
func dreamMaybe(t *DB, p *Project, k *ProjectTask, run *Run, end bool) {
	if !policyOf(p.Policy).Dream || p.State != projActive || p.Kind == projTeam || run.ParentID != 0 {
		return
	}
	r := t.dreamOf(k.ID)
	if end {
		if r.EndDone || r.Turns < dreamEndTurns || !dreamSessionLives(t, run) || k.WS != wsReady {
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
	t.putDream(k, r)
	addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": "its dream round: it adds what it learned to the project wiki"}, false, "")
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
Update the page a fact belongs on rather than adding a near-duplicate, and keep the index page a short map of the pages (at most 60 lines).
Never record task status, branches or pull requests, or anything that will be stale next week. If you learned nothing worth keeping, change nothing.
Don't change code or push in this round.
`)
	if harness {
		fmt.Fprintf(&b, "The wiki's pages are in %s (read-only; index.md is the map). To add or change a page, write the whole page to %s/<slug>.md (make the folder if it isn't there): "+
			"its first line is the header of the copy you read, `<!-- wiki: <slug> version <N> -->` — `version 0` for a new page — then `# Title`, then the page in Markdown "+
			"([[slug]] links to other pages, at most 16 KB). A slug is lowercase letters, digits and dashes.\n", wikiDir(p), wikiOutDir(p))
	} else {
		b.WriteString("Use wiki_list, wiki_search and wiki_read, then wiki_write or wiki_edit — read a page before you change it.\n")
	}
	b.WriteString("Answer with one line: what you changed in the wiki.")
	return b.String()
}

// --- a coding agent's pages --------------------------------------------------------------------

var wikiHeaderRe = regexp.MustCompile(`^<!--\s*wiki:\s*([A-Za-z0-9-]+)\s+version\s+(\d+)\s*-->\s*$`)

// dreamPage is one page file a dream round wrote.
type dreamPage struct {
	file    string
	slug    string
	base    int64
	title   string
	body    string
	sources []string
	err     string // why it can't be read as a page
}

// parseDreamPage reads a page file: its header (none: a new page named by
// the file), an optional # title, the body, a trailing sources comment.
func parseDreamPage(file, text string) dreamPage {
	pg := dreamPage{file: file, slug: strings.TrimSuffix(file, ".md")}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) {
		if m := wikiHeaderRe.FindStringSubmatch(strings.TrimSpace(lines[i])); m != nil {
			pg.slug = strings.ToLower(m[1])
			pg.base, _ = strconv.ParseInt(m[2], 10, 64)
			i++
		}
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) && strings.HasPrefix(lines[i], "# ") {
		pg.title = strings.TrimSpace(lines[i][2:])
		i++
	}
	rest := lines[i:]
	for j := len(rest) - 1; j >= 0; j-- {
		l := strings.TrimSpace(rest[j])
		if l == "" {
			continue
		}
		if s, ok := strings.CutPrefix(l, "<!-- sources:"); ok && strings.HasSuffix(s, "-->") {
			for _, x := range strings.Split(strings.TrimSuffix(s, "-->"), ";") {
				if x = strings.TrimSpace(x); x != "" {
					pg.sources = append(pg.sources, x)
				}
			}
			rest = rest[:j]
		}
		break
	}
	pg.body = strings.TrimSpace(strings.Join(rest, "\n"))
	if _, err := wikiSlug(pg.slug); err != nil {
		pg.err = err.Error()
	}
	return pg
}

// jobWikiIngest applies the page files task k's dream round wrote. Never
// fails the workspace: what can't be read is a note.
func jobWikiIngest(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	d := projAg().db
	if k == nil || p.Dir == "" {
		return doneJob("no task")
	}
	note := func(text string) {
		_ = d.Tx(func(t *DB) error {
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": clip(text, 1000)}, false, "")
			return nil
		})
	}
	ref := taskRef(p, k)
	s, err := openWsbx(ctx, p, ref)
	if err != nil {
		note("the dream round's wiki pages weren't read: " + err.Error())
		return doneJob("the sandbox: " + clip(err.Error(), 200))
	}
	out := wikiOutDir(p)
	list, err := s.conn.ListDir(ctx, s.id, out, 0)
	if err != nil {
		if sbxRefusal(err) == "not-found" {
			return doneJob("the dream round wrote no page")
		}
		note("the dream round's wiki pages weren't read: " + err.Error())
		return doneJob("listing its pages: " + clip(err.Error(), 200))
	}
	var names []string
	for _, e := range list.Entries {
		if e.Type == "file" && strings.HasSuffix(e.Name, ".md") {
			names = append(names, e.Name)
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return doneJob("the dream round wrote no page")
	}
	if len(names) > dreamFilesMax {
		names = names[:dreamFilesMax]
	}
	actor := wikiActor{By: fmt.Sprintf("task #%d (dream)", k.N), Run: k.RunID, Task: k.N}
	var wrote, lost []string
	var bad []string // "slug: why"
	for _, name := range names {
		path := out + "/" + name
		data, _, err := s.conn.ReadFile(ctx, s.id, path, 0, 0, dreamFileMaxLen)
		if err != nil {
			bad = append(bad, fmt.Sprintf("%s: unreadable (%v)", name, err))
			continue
		}
		pg := parseDreamPage(name, string(data))
		_ = s.conn.Remove(ctx, s.id, path, false) // read: the agent writes it again if it has to
		if pg.err != "" {
			bad = append(bad, fmt.Sprintf("%s: %s", name, pg.err))
			continue
		}
		base := pg.base
		pw, err := d.wikiPut(p.ID, actor, wikiChange{Slug: pg.slug, Title: pg.title, Body: pg.body, Sources: pg.sources, Base: &base, Note: "dream round"})
		switch {
		case err == nil:
			wrote = append(wrote, fmt.Sprintf("%s (v%d)", pw.Slug, pw.Version))
		case isWikiConflict(err):
			lost = append(lost, pg.slug)
		default:
			bad = append(bad, fmt.Sprintf("%s: %v", pg.slug, err))
		}
	}
	var round int
	_ = d.Tx(func(t *DB) error {
		r := t.dreamOf(k.ID)
		r.Round++
		round = r.Round
		t.putDream(k, r)
		return nil
	})
	if len(wrote) > 0 {
		note("the dream round wrote the wiki: " + strings.Join(wrote, ", "))
	}
	if len(lost)+len(bad) == 0 {
		return doneJob(fmt.Sprintf("wrote %d page(s)", len(wrote)))
	}
	if round >= dreamRoundsMax || k.RunID == 0 {
		note(fmt.Sprintf("the dream round's pages left out after %d rounds: %s", round, strings.Join(append(lost, bad...), "; ")))
		return doneJob(fmt.Sprintf("wrote %d page(s), dropped %d", len(wrote), len(lost)+len(bad)))
	}
	if len(lost) > 0 { // their copies, fresh: what the agent merges into
		if _, err := wikiSyncTo(ctx, p, ref); err == nil {
			d.setWikiSynced(ref, p.ID, d.wikiSeq(p.ID))
		}
	}
	text := dreamFollowUpFor(p, lost, bad)
	err = d.Tx(func(t *DB) error {
		_, err := queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcDream, HoldPark: true,
			Dedupe: fmt.Sprintf("dream:%d:again:%d:%d", k.N, j.ID, round)})
		return err
	})
	if err != nil {
		note("the dream round's follow-up wasn't sent: " + err.Error())
	}
	return doneJob(fmt.Sprintf("wrote %d page(s); %d sent back", len(wrote), len(lost)+len(bad)))
}

var dreamFollowUpFor = dreamFollowUp // (a var: tests)

// dreamFollowUp is the one prompt a round's lost and refused pages go back
// in: the conflict in the owner's words.
func dreamFollowUp(p *Project, lost, bad []string) string {
	var b strings.Builder
	b.WriteString("[dream round — from the platform, not a person]\nSome of your wiki pages weren't saved:\n")
	for _, s := range lost {
		fmt.Fprintf(&b, "- %s: %s\n", s, wikiConflict)
	}
	for _, s := range bad {
		fmt.Fprintf(&b, "- %s\n", s)
	}
	fmt.Fprintf(&b, "Read each again in %s/<slug>.md (just updated), merge your change into it, and write the whole page to %s/<slug>.md again with that copy's header. "+
		"Answer with one line.", wikiDir(p), wikiOutDir(p))
	return b.String()
}
