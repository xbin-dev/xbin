// ci_store.go — CI in the conversation (API.md §CI in the conversation):
// the ci_watch table, a watch's stored snapshot (the provider's GET
// /scm/checks answer, cleaned: redacted, clipped, links kept only when they
// are http(s), at most 256 KiB), the aggregate a conversation's watches add
// up to (CISummary: the chip, TaskView.ci, a board row's ci), the views the
// routes answer, and the `ci` stream event (coalesced per conversation, like
// drafts: a client that falls behind sees only the latest).
//
// A watch is keyed by the conversation's root (root_run) and the branch it
// follows; run_id is the run whose push it came from (a coding agent child,
// say). Watches are made by a task's refs (ci_watch.go), at a run's turn end
// from what it pushed (ci_detect.go) and by hand (ci_routes.go); scm events
// keep them current (ci_events.go), reads fill the gaps (ci_watch.go).
//
// taskCISummary (projects_seams.go) reads memory only: its callers build a
// TaskView inside a transaction that holds the one connection, so the
// summaries are kept per database and root here, set whenever a watch
// changes and read again from the table when the database opens.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addCIWatchSchema)
	taskCISummary = func(runID int64) *CISummary {
		a := projAg()
		if a == nil || a.db == nil || runID == 0 {
			return nil
		}
		return ciCached(a.db, runID)
	}
}

// ciWatchSchema is §6.9's table, as it is (additive, idempotent).
const ciWatchSchema = `
CREATE TABLE IF NOT EXISTS ci_watch (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  root_run   INTEGER NOT NULL,
  run_id     INTEGER NOT NULL DEFAULT 0,
  project_id INTEGER NOT NULL DEFAULT 0,
  n          INTEGER NOT NULL DEFAULT 0,
  source     TEXT    NOT NULL,
  scm        TEXT    NOT NULL,
  host       TEXT    NOT NULL DEFAULT '',
  repo       TEXT    NOT NULL,
  ref        TEXT    NOT NULL DEFAULT '',
  pr         INTEGER NOT NULL DEFAULT 0,
  sha        TEXT    NOT NULL DEFAULT '',
  since      INTEGER NOT NULL DEFAULT 0,
  state      TEXT    NOT NULL DEFAULT 'none',
  snapshot   TEXT    NOT NULL DEFAULT '',
  etag       TEXT    NOT NULL DEFAULT '',
  sub_key    TEXT    NOT NULL DEFAULT '',
  by_user    TEXT    NOT NULL DEFAULT '',
  carded     TEXT    NOT NULL DEFAULT '',
  error      TEXT    NOT NULL DEFAULT '',
  refusal    TEXT    NOT NULL DEFAULT '',
  fetched_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  ended_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ciw_ref ON ci_watch(root_run, scm, repo, ref) WHERE ended_ms=0;
CREATE INDEX IF NOT EXISTS idx_ciw_match ON ci_watch(scm, repo, ref);
CREATE INDEX IF NOT EXISTS idx_ciw_sha ON ci_watch(scm, repo, sha);
`

// addCIWatchSchema makes ci_watch (schemaAdds: the agent's own database
// only, never team) and reads the conversations' summaries into memory.
func (d *DB) addCIWatchSchema() error {
	if _, err := d.q.Exec(ciWatchSchema); err != nil {
		return fmt.Errorf("ci_watch: %w", err)
	}
	ciPrime(d)
	return nil
}

// Watch states: a snapshot's (none, pending, success, failure), or gone —
// its branch was deleted or its pull request merged or closed (the watch
// ends a day later).
const (
	ciNone    = "none"
	ciPending = "pending"
	ciSuccess = "success"
	ciFailure = "failure"
	ciGone    = "gone"
)

// Bounds of what a watch keeps (the contract's own, and the table's).
const (
	ciSnapMax    = 256 << 10 // a stored snapshot
	ciTextMax    = 4 << 10   // a check's title or summary
	ciNameMax    = 300       // a name: a run's, a job's, a step's, a check's
	ciMaxRuns    = 20
	ciMaxJobs    = 100
	ciMaxSteps   = 200
	ciMaxChecks  = 100
	ciMaxStatus  = 100
	ciMaxLive    = 10 // live watches per conversation
	ciSubKeyPref = "ci:"
)

// ciWatch is one row of ci_watch.
type ciWatch struct {
	ID, RootRun, RunID, ProjectID, N int64
	Source, SCM, Host, Repo, Ref     string
	PR                               int
	SHA                              string
	Since                            int64
	State, Snapshot, ETag, SubKey    string
	ByUser, Carded, Error, Refusal   string
	FetchedMs, UpdatedMs, EndedMs    int64
}

const ciCols = `id, root_run, run_id, project_id, n, source, scm, host, repo, ref, pr, sha, since, state, snapshot, etag,
	sub_key, by_user, carded, error, refusal, fetched_ms, updated_ms, ended_ms`

func scanCIWatch(scan func(dest ...any) error) (*ciWatch, error) {
	w := &ciWatch{}
	err := scan(&w.ID, &w.RootRun, &w.RunID, &w.ProjectID, &w.N, &w.Source, &w.SCM, &w.Host, &w.Repo, &w.Ref, &w.PR, &w.SHA,
		&w.Since, &w.State, &w.Snapshot, &w.ETag, &w.SubKey, &w.ByUser, &w.Carded, &w.Error, &w.Refusal, &w.FetchedMs,
		&w.UpdatedMs, &w.EndedMs)
	return w, err
}

// ciWatchesWhere lists the rows matching where (an SQL tail, with args).
func (d *DB) ciWatchesWhere(where string, args ...any) []*ciWatch {
	rows, err := d.q.Query(`SELECT `+ciCols+` FROM ci_watch `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*ciWatch
	for rows.Next() {
		if w, err := scanCIWatch(rows.Scan); err == nil {
			out = append(out, w)
		}
	}
	return out
}

// ciLive is root's live watches, oldest first.
func (d *DB) ciLive(root int64) []*ciWatch {
	return d.ciWatchesWhere(`WHERE root_run=? AND ended_ms=0 ORDER BY id`, root)
}

// ciWatchByID is one watch (nil: none).
func (d *DB) ciWatchByID(id int64) *ciWatch {
	ws := d.ciWatchesWhere(`WHERE id=?`, id)
	if len(ws) == 0 {
		return nil
	}
	return ws[0]
}

// ciInsert adds w (its ID set).
func (d *DB) ciInsert(w *ciWatch) error {
	now := nowMs()
	if w.Since == 0 {
		w.Since = now
	}
	w.UpdatedMs = now
	w.State = orStr(w.State, ciNone)
	err := d.q.QueryRow(`INSERT INTO ci_watch (root_run, run_id, project_id, n, source, scm, host, repo, ref, pr, sha, since,
		state, by_user, updated_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		w.RootRun, w.RunID, w.ProjectID, w.N, w.Source, w.SCM, w.Host, w.Repo, w.Ref, w.PR, w.SHA, w.Since, w.State,
		w.ByUser, w.UpdatedMs).Scan(&w.ID)
	if err != nil {
		return err
	}
	w.SubKey = ciSubKeyPref + strconv.FormatInt(w.ID, 10)
	_, err = d.q.Exec(`UPDATE ci_watch SET sub_key=? WHERE id=?`, w.SubKey, w.ID)
	return err
}

// ciSave writes back what a watch's life changes (never its key).
func (d *DB) ciSave(w *ciWatch) error {
	_, err := d.q.Exec(`UPDATE ci_watch SET run_id=?, project_id=?, n=?, source=?, host=?, pr=?, sha=?, since=?, state=?,
		snapshot=?, etag=?, by_user=?, carded=?, error=?, refusal=?, fetched_ms=?, updated_ms=?, ended_ms=? WHERE id=?`,
		w.RunID, w.ProjectID, w.N, w.Source, w.Host, w.PR, w.SHA, w.Since, w.State, w.Snapshot, w.ETag, w.ByUser, w.Carded,
		w.Error, w.Refusal, w.FetchedMs, w.UpdatedMs, w.EndedMs, w.ID)
	return err
}

// --- the snapshot ------------------------------------------------------------------------

// checks is the stored snapshot (nil: none yet).
func (w *ciWatch) checks() *scmChecks {
	if w.Snapshot == "" {
		return nil
	}
	var c scmChecks
	if json.Unmarshal([]byte(w.Snapshot), &c) != nil {
		return nil
	}
	return &c
}

// setChecks stores c (cleaned), its state, and the outcome when final.
func (w *ciWatch) setChecks(c *scmChecks) {
	c = ciClean(c)
	b, _ := json.Marshal(c)
	w.Snapshot = string(b)
	if w.State != ciGone {
		w.State = c.State
	}
	w.card()
}

// card keeps the watch's final outcome — <sha>:<state> — once per outcome:
// the inline card's key, which every later answer repeats.
func (w *ciWatch) card() {
	if (w.State == ciSuccess || w.State == ciFailure) && w.SHA != "" {
		w.Carded = w.SHA + ":" + w.State
	}
}

// moveTo starts the watch over on a new head: no snapshot, nothing known.
func (w *ciWatch) moveTo(sha string) {
	w.SHA, w.Snapshot, w.ETag, w.Error, w.Refusal, w.FetchedMs = sha, "", "", "", "", 0
	if w.State != ciGone {
		w.State = ciNone
	}
	w.Since = nowMs()
}

// ciText is untrusted text as a page or a row keeps it: its invisible
// characters gone, redacted (scm tokens, the harness's shapes), clipped.
func ciText(s string, max int) string {
	return clip(projRedact(invisibles.ReplaceAllString(s, "")), max)
}

// ciLink keeps an http(s) link, nothing else (a javascript: or data: link
// from a build never reaches a page).
func ciLink(s string) string {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	u.User = nil
	return u.String()
}

// ciWord keeps a status or conclusion word: lower-case letters and _ only.
func ciWord(s string) string {
	if len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '_') {
			return ""
		}
	}
	return s
}

// ciClean is c as a watch keeps it: every text redacted and clipped, links
// http(s) only, lists bounded, state and counts computed again from what is
// kept (§4.9's rules), at most ciSnapMax bytes of JSON.
func ciClean(c *scmChecks) *scmChecks {
	out := &scmChecks{SHA: ciSHA(c.SHA), Ref: ciText(c.Ref, ciNameMax), ETag: c.ETag, Checks: []scmCheck{}, Statuses: []scmStatus{}}
	if c.WorkflowRuns != nil {
		out.WorkflowRuns = []scmWorkflowRun{}
	}
	for i, r := range c.WorkflowRuns {
		if i >= ciMaxRuns {
			break
		}
		out.WorkflowRuns = append(out.WorkflowRuns, ciCleanRun(r))
	}
	for i, k := range c.Checks {
		if i >= ciMaxChecks {
			break
		}
		out.Checks = append(out.Checks, ciCleanCheck(k))
	}
	for i, s := range c.Statuses {
		if i >= ciMaxStatus {
			break
		}
		out.Statuses = append(out.Statuses, scmStatus{Context: ciText(s.Context, ciNameMax), State: ciWord(s.State), URL: ciLink(s.URL),
			Description: ciText(s.Description, 1024), UpdatedAt: s.UpdatedAt})
	}
	ciRecount(out)
	ciShrink(out)
	return out
}

func ciCleanRun(r scmWorkflowRun) scmWorkflowRun {
	o := scmWorkflowRun{ID: ciID(r.ID), Name: ciText(r.Name, ciNameMax), Event: ciText(r.Event, 60), Status: ciWord(r.Status),
		Conclusion: ciWord(r.Conclusion), URL: ciLink(r.URL), StartedAt: r.StartedAt, UpdatedAt: r.UpdatedAt, Attempt: r.Attempt,
		HeadSHA: ciSHA(r.HeadSHA), HeadBranch: ciText(r.HeadBranch, ciNameMax)}
	if r.Jobs != nil {
		o.Jobs = []scmJob{}
	}
	for i, j := range r.Jobs {
		if i >= ciMaxJobs {
			break
		}
		o.Jobs = append(o.Jobs, ciCleanJob(j))
	}
	return o
}

func ciCleanJob(j scmJob) scmJob {
	o := scmJob{ID: ciID(j.ID), Name: ciText(j.Name, ciNameMax), Status: ciWord(j.Status), Conclusion: ciWord(j.Conclusion),
		URL: ciLink(j.URL), StartedAt: j.StartedAt, CompletedAt: j.CompletedAt, Runner: ciText(j.Runner, 120), Check: ciID(j.Check),
		Steps: []scmStep{}}
	for i, s := range j.Steps {
		if i >= ciMaxSteps {
			break
		}
		o.Steps = append(o.Steps, scmStep{N: s.N, Name: ciText(s.Name, ciNameMax), Status: ciWord(s.Status), Conclusion: ciWord(s.Conclusion),
			StartedAt: s.StartedAt, CompletedAt: s.CompletedAt})
	}
	return o
}

func ciCleanCheck(k scmCheck) scmCheck {
	return scmCheck{ID: ciID(k.ID), Name: ciText(k.Name, ciNameMax), App: ciText(k.App, 120), Status: ciWord(k.Status),
		Conclusion: ciWord(k.Conclusion), URL: ciLink(k.URL), DetailsURL: ciLink(k.DetailsURL), Title: ciText(k.Title, ciTextMax),
		Summary: ciText(k.Summary, ciTextMax), Annotations: max(k.Annotations, 0), StartedAt: k.StartedAt, CompletedAt: k.CompletedAt,
		Suite: ciID(k.Suite), Job: ciID(k.Job)}
}

// ciID keeps an id a provider gave: printable, no spaces or slashes, ≤ 100.
func ciID(s string) string {
	if len(s) > 100 {
		return ""
	}
	for _, r := range s {
		if r <= ' ' || r == '/' || r == '\\' || r == '?' || r == '#' || r == '%' || r > '~' {
			return ""
		}
	}
	return s
}

// ciSHA keeps a commit id: hex, ≤ 64.
func ciSHA(s string) string {
	if len(s) > 64 {
		return ""
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return ""
		}
	}
	return strings.ToLower(s)
}

// ciShrink cuts c to ciSnapMax bytes of JSON: check summaries first, then
// their titles, then the steps of completed jobs, then the tail of the
// checks.
func ciShrink(c *scmChecks) {
	size := func() int { b, _ := json.Marshal(c); return len(b) }
	if size() <= ciSnapMax {
		return
	}
	for i := range c.Checks {
		c.Checks[i].Summary = ""
	}
	if size() <= ciSnapMax {
		return
	}
	for i := range c.Checks {
		c.Checks[i].Title = ""
	}
	for i := range c.Statuses {
		c.Statuses[i].Description = ""
	}
	if size() <= ciSnapMax {
		return
	}
	for i := range c.WorkflowRuns {
		for j := range c.WorkflowRuns[i].Jobs {
			if c.WorkflowRuns[i].Jobs[j].Status == "completed" {
				c.WorkflowRuns[i].Jobs[j].Steps = []scmStep{}
			}
		}
	}
	for size() > ciSnapMax && len(c.Checks) > 0 {
		c.Checks = c.Checks[:len(c.Checks)/2]
	}
	for size() > ciSnapMax && len(c.WorkflowRuns) > 0 {
		c.WorkflowRuns = c.WorkflowRuns[:len(c.WorkflowRuns)/2]
	}
}

// ciFailed: a conclusion (or status state) that fails CI (§4.9).
func ciFailed(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "action_required", "startup_failure", "error":
		return true
	}
	return false
}

// ciRecount computes c's counts and state again over its checks and
// statuses, and its jobs' and runs' progress (§4.9: pending while any
// check, job or status isn't completed).
func ciRecount(c *scmChecks) {
	var n scmCounts
	pending, failed := false, false
	for _, k := range c.Checks {
		switch {
		case k.Status != "completed":
			n.Pending++
			pending = true
		case ciFailed(k.Conclusion):
			n.Failure++
			failed = true
		case k.Conclusion == "neutral":
			n.Neutral++
		case k.Conclusion == "skipped":
			n.Skipped++
		case k.Conclusion == "cancelled" || k.Conclusion == "stale":
			n.Cancelled++
		default:
			n.Success++
		}
	}
	for _, s := range c.Statuses {
		switch s.State {
		case "pending":
			n.Pending++
			pending = true
		case "failure", "error":
			n.Failure++
			failed = true
		default:
			n.Success++
		}
	}
	for _, r := range c.WorkflowRuns {
		pending = pending || r.Status != "completed"
		failed = failed || ciFailed(r.Conclusion)
		for _, j := range r.Jobs {
			pending = pending || j.Status != "completed"
		}
	}
	n.Total = n.Success + n.Failure + n.Pending + n.Neutral + n.Skipped + n.Cancelled
	c.Counts = n
	switch {
	case failed:
		c.State = ciFailure
	case pending:
		c.State = ciPending
	case n.Total == 0 && len(c.WorkflowRuns) == 0:
		c.State = ciNone
	default:
		c.State = ciSuccess
	}
}

// --- the aggregate ---------------------------------------------------------------------

// ciJobState sorts a job (or a check, a status) into the summary's counts.
func ciJobCount(j *CIJobs, status, conclusion string) {
	j.Total++
	switch status {
	case "completed":
		j.Done++
		if ciFailed(conclusion) {
			j.Failed++
		}
	case "in_progress":
		j.Running++
	default:
		j.Queued++
	}
}

// ciSummaryOf adds up a conversation's live watches (gone ones left out):
// nil when it has none.
func ciSummaryOf(ws []*ciWatch) *CISummary {
	var live []*ciWatch
	for _, w := range ws {
		if w.EndedMs == 0 {
			live = append(live, w)
		}
	}
	if len(live) == 0 {
		return nil
	}
	s := &CISummary{State: ciNone}
	seen := map[string]bool{}
	failURL, runURL, firstURL := "", "", ""
	for _, w := range live {
		if w.State == ciGone {
			continue
		}
		seen[w.State] = true
		s.UpdatedAt = max(s.UpdatedAt, w.UpdatedMs)
		c := w.checks()
		if c == nil {
			continue
		}
		fresh := w.FetchedMs >= w.UpdatedMs // not patched by an event since its last read
		jobs := map[string]bool{}
		for _, r := range c.WorkflowRuns {
			if r.StartedAt > 0 && (s.StartedAt == 0 || r.StartedAt < s.StartedAt) {
				s.StartedAt = r.StartedAt
			}
			firstURL = orStr(firstURL, r.URL)
			if ciFailed(r.Conclusion) && failURL == "" {
				failURL = r.URL
			}
			if r.Status != "completed" && runURL == "" {
				runURL = r.URL
			}
			for _, j := range r.Jobs {
				jobs[j.ID] = true
				ciJobCount(&s.Jobs, j.Status, j.Conclusion)
				if s.Current == "" && fresh && j.Status == "in_progress" {
					s.Current = ciCurrent(j)
				}
			}
		}
		for _, k := range c.Checks {
			if k.Job != "" && jobs[k.Job] {
				continue // shown as its job
			}
			ciJobCount(&s.Jobs, k.Status, k.Conclusion)
			if ciFailed(k.Conclusion) && failURL == "" {
				failURL = orStr(k.DetailsURL, k.URL)
			}
		}
		for _, st := range c.Statuses {
			status, concl := "in_progress", ""
			if st.State != "pending" {
				status, concl = "completed", st.State
			}
			ciJobCount(&s.Jobs, status, concl)
		}
	}
	switch {
	case seen[ciFailure]:
		s.State = ciFailure
	case seen[ciPending]:
		s.State = ciPending
	case seen[ciSuccess]:
		s.State = ciSuccess
	}
	s.URL = orStr(failURL, orStr(runURL, firstURL))
	return s
}

// ciCurrent is "job › step" for a running job (its step in progress, else
// the first not done), or the job alone.
func ciCurrent(j scmJob) string {
	for _, st := range j.Steps {
		if st.Status == "in_progress" {
			return j.Name + " › " + st.Name
		}
	}
	for _, st := range j.Steps {
		if st.Status != "completed" {
			return j.Name + " › " + st.Name
		}
	}
	return j.Name
}

// --- the summaries in memory (taskCISummary) ------------------------------------------------

type ciKey struct {
	db   *sql.DB
	root int64
}

var ciSums sync.Map // ciKey → *CISummary

func ciCached(d *DB, root int64) *CISummary {
	v, ok := ciSums.Load(ciKey{d.sql, root})
	if !ok {
		return nil
	}
	s, _ := v.(*CISummary)
	if s == nil {
		return nil
	}
	cp := *s
	return &cp
}

func ciCache(d *DB, root int64, s *CISummary) {
	if s == nil {
		ciSums.Delete(ciKey{d.sql, root})
		return
	}
	ciSums.Store(ciKey{d.sql, root}, s)
}

// ciPrime reads every conversation's summary when the database opens.
func ciPrime(d *DB) {
	roots := map[int64][]*ciWatch{}
	for _, w := range d.ciWatchesWhere(`WHERE ended_ms=0 ORDER BY id`) {
		roots[w.RootRun] = append(roots[w.RootRun], w)
	}
	for root, ws := range roots {
		ciCache(d, root, ciSummaryOf(ws))
	}
}

// --- telling ------------------------------------------------------------------------------

// ciChanged says root's CI changed (watch wid, 0: several): its summary in
// memory, the `ci` stream event to its viewers after the commit, and — a
// task's, when the summary moved — projectTaskChanged(…, "ci"), which the
// board hears.
func ciChanged(t *DB, root, wid int64) {
	ws := t.ciLive(root)
	sum := ciSummaryOf(ws)
	old := ciCached(t, root)
	ciCache(t, root, sum) // now: the board's TaskView in this transaction reads it
	data := map[string]any{"root": root, "watch": wid, "summary": sum}
	items := []map[string]any{}
	for _, w := range ws {
		items = append(items, map[string]any{"id": w.ID, "state": w.State, "outcome": w.Carded, "run": w.RunID, "repo": w.Repo, "ref": w.Ref})
		if w.ID == wid {
			data["state"], data["outcome"] = w.State, w.Carded
		}
	}
	data["watches"] = items
	if wid != 0 && data["state"] == nil {
		data["state"], data["outcome"] = "ended", ""
	}
	t.AfterCommit(func() { ciPublish(root, data) })
	if ciSameSummary(old, sum) {
		return
	}
	if k := t.taskByRun(root); k != nil {
		projectTaskChanged(t, k.ProjectID, k.N, "ci")
	}
}

func ciSameSummary(a, b *CISummary) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// ciPublish sends the `ci` event to root's viewers, coalesced per root.
func ciPublish(root int64, data map[string]any) {
	e := projEng()
	if e == nil || e.hub == nil {
		return
	}
	e.hub.publish(&Event{Type: evCI, Run: root, Root: root, Data: data, key: "ci:" + strconv.FormatInt(root, 10)})
}

// --- views --------------------------------------------------------------------------------

// ciView is w as the API shows it (kind: the provider's host family; its
// links are built for github only).
func ciView(w *ciWatch, kind string) CIWatchView {
	v := CIWatchView{ID: w.ID, Source: w.Source, Run: w.RunID, SCM: w.SCM, Host: w.Host, Repo: w.Repo, Ref: w.Ref, PR: w.PR,
		SHA: w.SHA, State: w.State, Since: w.Since, UpdatedMs: w.UpdatedMs, FetchedMs: w.FetchedMs, Error: w.Error,
		Refusal: w.Refusal, Outcome: w.Carded, Checks: w.checks()}
	if kind == "github" && w.Host != "" {
		base := "https://" + w.Host + "/" + w.Repo
		if w.PR > 0 {
			v.URLs.PR = base + "/pull/" + strconv.Itoa(w.PR)
			v.URLs.Checks = v.URLs.PR + "/checks"
		}
		if w.Ref != "" {
			v.URLs.Branch = base + "/tree/" + ciPathEscape(w.Ref)
		}
		if w.SHA != "" {
			v.URLs.Commit = base + "/commit/" + w.SHA
			if v.URLs.Checks == "" {
				v.URLs.Checks = v.URLs.Commit + "/checks"
			}
		}
	}
	return v
}

// ciPathEscape escapes a branch for a link, keeping its slashes.
func ciPathEscape(ref string) string {
	parts := strings.Split(ref, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// ciSortedByID orders watches by id (the dock's order: never re-sorted).
func ciSortedByID(ws []*ciWatch) {
	sort.Slice(ws, func(i, j int) bool { return ws[i].ID < ws[j].ID })
}
