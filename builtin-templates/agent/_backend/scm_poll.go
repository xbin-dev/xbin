// scm_poll.go — the reads that stand in for scm events, and the reads an
// event asks for (API.md §scm events and polling, "Polling"). A row of
// scm_poll is one conditional read of one task's repo: its head's checks
// (kind checks), its open PR (pull) or its PR's timeline (comments).
//
//   - Rows are made by the refs hook (scm_subs.go) while a task has an open
//     PR, and by an event that wants a read (scmNudge: checks.completed on
//     the head after policy.ci.delaySec, a review or comment after
//     policy.reviews.batchSec — coalesced: a read already due keeps its
//     time). A read an event asked for is made whatever the cadence says.
//   - Cadence while no event came for the head: every 2 min for the first
//     20 min, every 10 min until 2 h, every 30 min until 24 h — then the
//     row stops, checks with a waking note "lost track of CI — check
//     manually". With healthy events (the provider's hello says so, the repo
//     delivered within 30 min, or one came for the head) only a safety read
//     every 15 min once the head has waited 30 min. Never sooner than the
//     provider's events.pollMinMs. Checks that are done (success, failure)
//     stop the row until the head moves; checks that nobody reports (state
//     none for 30 min: a repo without CI) stop it quietly.
//   - One pass reads every due row: one POST /scm/poll per provider and
//     identity (at most its limits.pollItems items; the rest wait for the
//     next pass), or each route's own read where the provider has no poll.
//     A changed answer is handled as the event would be, with the semantic
//     dedupe (scm_seen "sem:…" keys): failing CI as one task input per
//     failing suite on a head (capped per task per day), green CI as
//     pr.ready, a merged or closed PR, a moved head, reviews to forward.
//   - The pass runs in an ownerLoops entry (scmLoop) beside the project
//     worker — it never holds the engine up — at the next row's time, on a
//     kick, and at least every 10 min. A person's partition at rest comes
//     back for the next row's time (userWake, resume_mode.go); the global
//     instance and an unpartitioned agent poll while they run, and a
//     delivery or a POST /tick that starts them makes the pass at once.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// Poll kinds (scm_poll.kind; scmPollItem.kind).
const (
	scmPollChecks   = "checks"
	scmPollPull     = "pull"
	scmPollComments = "comments"
)

func init() {
	ownerLoops = append(ownerLoops, scmLoop)
}

// The cadence (vars: the tests shorten nothing, they move scmClock).
var (
	scmPollFirst    = 2 * time.Minute  // the first read of a new head
	scmPollSafety   = 15 * time.Minute // with healthy events: the safety read
	scmPollPatience = 30 * time.Minute // with healthy events: no read before the head waited this long
	scmPollGiveUp   = 24 * time.Hour   // then stop
	scmPollNoCI     = 30 * time.Minute // checks still none after this: no CI here
)

// scmPollDelay is when to read a row again: age is how long its head has
// waited, healthy whether events arrive. stop: no more reads.
func scmPollDelay(age time.Duration, healthy bool) (time.Duration, bool) {
	switch {
	case age >= scmPollGiveUp:
		return 0, true
	case healthy && age < scmPollPatience:
		return scmPollPatience - age, false
	case healthy:
		return scmPollSafety, false
	case age < 20*time.Minute:
		return 2 * time.Minute, false
	case age < 2*time.Hour:
		return 10 * time.Minute, false
	}
	return 30 * time.Minute, false
}

// scmPollRec is scm_poll.item: the read, and what the last read of checks
// said.
type scmPollRec struct {
	scmPollItem
	State string `json:"state,omitempty"`
}

// scmPollRow is a row of scm_poll (with its project's provider and identity).
type scmPollRow struct {
	Project, N   int64
	Repo, Kind   string
	Item         scmPollRec
	raw          string // item as read (the guard of the update after a read)
	ETag         string
	Due, Step    int64
	PendingSince int64
	Last         int64
	Nudge        bool
	SCM, As      string
	healthy      bool
}

func (r *scmPollRow) id() string {
	return fmt.Sprintf("%d:%d:%s:%s", r.Project, r.N, r.Repo, r.Kind)
}

const scmPollCols = `project_id, n, repo, kind, item, etag, due_ms, step, pending_since, last_ms, nudge`

func scanPollRow(scan func(dest ...any) error) (*scmPollRow, error) {
	r := &scmPollRow{}
	var nudge int
	if err := scan(&r.Project, &r.N, &r.Repo, &r.Kind, &r.raw, &r.ETag, &r.Due, &r.Step, &r.PendingSince, &r.Last, &nudge); err != nil {
		return nil, err
	}
	r.Nudge = nudge != 0
	_ = json.Unmarshal([]byte(r.raw), &r.Item)
	return r, nil
}

// scmPollRowOf is task n's row of that kind in repo.
func scmPollRowOf(t *DB, pid, n int64, repo, kind string) (*scmPollRow, bool) {
	r, err := scanPollRow(t.q.QueryRow(`SELECT `+scmPollCols+` FROM scm_poll WHERE project_id=? AND n=? AND repo=? AND kind=?`,
		pid, n, repo, kind).Scan)
	return r, err == nil
}

// scmPollItemOf is that row's read.
func scmPollItemOf(t *DB, pid, n int64, repo, kind string) (scmPollRec, bool) {
	if r, ok := scmPollRowOf(t, pid, n, repo, kind); ok {
		return r.Item, true
	}
	return scmPollRec{}, false
}

func scmItemJSON(it scmPollRec) string {
	b, _ := json.Marshal(it)
	return string(b)
}

// scmPutPoll writes a row whole.
func scmPutPoll(t *DB, pid, n int64, repo, kind string, it scmPollRec, etag string, due, step, since int64, nudge bool) {
	_, _ = t.q.Exec(`INSERT INTO scm_poll (`+scmPollCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(project_id, n, repo, kind) DO UPDATE SET item=excluded.item, etag=excluded.etag, due_ms=excluded.due_ms,
		step=excluded.step, pending_since=excluded.pending_since, nudge=excluded.nudge`,
		pid, n, repo, kind, scmItemJSON(it), etag, due, step, since, b2i(nudge))
}

// --- what makes and moves rows ----------------------------------------------------------

// scmPollTrack keeps task k's rows of repo as its refs say (the refs hook):
// the checks row watches head (a new head starts over: read at once while
// a PR is open, else only when an event asks); the pull and comments rows
// exist while it has an open PR.
func scmPollTrack(t *DB, p *Project, k *ProjectTask, repo string, open *TaskPR, head string) {
	now := scmClock()
	first := now + scmPollFirst.Milliseconds()
	moved := false
	if head != "" {
		head = strings.ToLower(head)
		due := int64(0)
		if open != nil {
			due = first
		}
		row, ok := scmPollRowOf(t, p.ID, k.N, repo, scmPollChecks)
		switch {
		case !ok:
			scmPutPoll(t, p.ID, k.N, repo, scmPollChecks, scmPollRec{scmPollItem: scmPollItem{Kind: scmPollChecks, Repo: repo, Ref: head}}, "", due, 0, now, false)
		case !strings.EqualFold(row.Item.Ref, head):
			moved = true
			scmPutPoll(t, p.ID, k.N, repo, scmPollChecks, scmPollRec{scmPollItem: scmPollItem{Kind: scmPollChecks, Repo: repo, Ref: head}}, "", due, 0, now, false)
			_, _ = t.q.Exec(`UPDATE scm_poll SET last_ms=0 WHERE project_id=? AND n=? AND repo=? AND kind=?`, p.ID, k.N, repo, scmPollChecks)
		case open != nil && row.Due == 0 && row.Last == 0: // watched before its PR opened, never read
			_, _ = t.q.Exec(`UPDATE scm_poll SET due_ms=?, pending_since=? WHERE project_id=? AND n=? AND repo=? AND kind=?`,
				first, now, p.ID, k.N, repo, scmPollChecks)
		}
	}
	if open == nil {
		_, _ = t.q.Exec(`DELETE FROM scm_poll WHERE project_id=? AND n=? AND repo=? AND kind IN (?, ?)`, p.ID, k.N, repo, scmPollPull, scmPollComments)
		t.AfterCommit(scmLoopPoke)
		return
	}
	for _, kind := range []string{scmPollPull, scmPollComments} {
		row, ok := scmPollRowOf(t, p.ID, k.N, repo, kind)
		switch {
		case !ok || row.Item.Number != open.Number:
			it := scmPollRec{scmPollItem: scmPollItem{Kind: kind, Repo: repo, Number: open.Number}}
			if kind == scmPollComments {
				it.Since = now
			}
			scmPutPoll(t, p.ID, k.N, repo, kind, it, "", first, 0, now, false)
		case moved: // new work on the PR: the cadence starts over
			due := row.Due
			if due == 0 || due > first {
				due = first
			}
			_, _ = t.q.Exec(`UPDATE scm_poll SET due_ms=?, step=0, pending_since=? WHERE project_id=? AND n=? AND repo=? AND kind=?`,
				due, now, p.ID, k.N, repo, kind)
		}
	}
	t.AfterCommit(scmLoopPoke)
}

// scmWatchHead points task k's checks row in repo at sha (a push to a
// branch with no open PR): read only when an event asks.
func scmWatchHead(t *DB, p *Project, k *ProjectTask, repo, sha string) {
	scmPollTrack(t, p, k, repo, nil, sha)
}

// scmNudge asks for a read of task k's row of that kind in repo within
// delay (an event: checks completed on head sha, a review or comment on PR
// pr). A read already asked for keeps its earlier time (coalesced).
func scmNudge(t *DB, p *Project, k *ProjectTask, repo, kind string, pr int, sha string, delay time.Duration) {
	now := scmClock()
	due := now + delay.Milliseconds()
	row, ok := scmPollRowOf(t, p.ID, k.N, repo, kind)
	if !ok {
		it := scmPollRec{scmPollItem: scmPollItem{Kind: kind, Repo: repo, Ref: strings.ToLower(sha), Number: pr}}
		if kind == scmPollComments {
			it.Since = now - (10 * time.Minute).Milliseconds() // the comment that asked is recent
		}
		scmPutPoll(t, p.ID, k.N, repo, kind, it, "", due, 0, now, true)
		t.AfterCommit(scmLoopPoke)
		return
	}
	it, etag, step, since := row.Item, row.ETag, row.Step, row.PendingSince
	if kind == scmPollChecks && sha != "" && !strings.EqualFold(it.Ref, sha) {
		it.Ref, it.State, etag, step, since = strings.ToLower(sha), "", "", 0, now
	}
	if kind != scmPollChecks && pr != 0 && it.Number != pr {
		it.Number, etag, step, since = pr, "", 0, now
	}
	if row.Nudge && row.Due > 0 && row.Due <= due {
		due = row.Due
	}
	scmPutPoll(t, p.ID, k.N, repo, kind, it, etag, due, step, since, true)
	t.AfterCommit(scmLoopPoke)
}

// --- the pass ---------------------------------------------------------------------------

// scmDuePolls are the rows due now of active projects.
func scmDuePolls(d *DB, now int64) []*scmPollRow {
	rows, err := d.q.Query(`SELECT s.project_id, s.n, s.repo, s.kind, s.item, s.etag, s.due_ms, s.step, s.pending_since, s.last_ms, s.nudge
		FROM scm_poll s JOIN projects p ON p.id=s.project_id WHERE s.due_ms>0 AND s.due_ms<=? AND p.state='active'
		ORDER BY s.due_ms LIMIT 500`, now)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*scmPollRow
	for rows.Next() {
		if r, err := scanPollRow(rows.Scan); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// scmPollPass reads every due row (see the file's comment).
func scmPollPass(ctx context.Context, d *DB) {
	now := scmClock()
	type key struct{ scm, as string }
	groups := map[key][]*scmPollRow{}
	var order []key
	for _, r := range scmDuePolls(d, now) {
		p, err := d.getProject(r.Project)
		if err != nil {
			continue
		}
		r.SCM, r.As = p.SCM, projectAs(p)
		kk := key{r.SCM, r.As}
		if _, ok := groups[kk]; !ok {
			order = append(order, kk)
		}
		groups[kk] = append(groups[kk], r)
	}
	for _, kk := range order {
		if ctx.Err() != nil {
			return
		}
		rs := groups[kk]
		api, err := scmFor(kk.scm)
		if err != nil {
			for _, r := range rs {
				scmPollLater(d, r, 10*time.Minute)
			}
			continue
		}
		hctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		hello, _ := api.Hello(hctx)
		cancel()
		limit, minMs := 50, int64(0)
		if hello != nil {
			if hello.Limits.PollItems > 0 {
				limit = hello.Limits.PollItems
			}
			minMs = hello.Events.PollMinMs
		}
		var batch []*scmPollRow
		for _, r := range rs {
			r.healthy = scmHealthy(d, hello, r)
			if !r.Nudge && r.healthy && time.Duration(now-r.PendingSince)*time.Millisecond < scmPollPatience {
				scmPollAt(d, r, r.PendingSince+scmPollPatience.Milliseconds()) // events arrive: no read yet
				continue
			}
			if len(batch) < limit {
				batch = append(batch, r)
			}
		}
		if len(batch) == 0 {
			continue
		}
		results, retry, err := scmPollRead(ctx, api, hello, kk.as, batch)
		if err != nil {
			logf("scm poll at %s: %v", kk.scm, err)
			for _, r := range batch {
				scmPollAfter(d, r, nil, "", false, minMs)
			}
			continue
		}
		for _, r := range batch {
			scmPolled(ctx, d, api, r, results[r.id()], minMs)
		}
		if retry > 0 {
			for _, r := range rs {
				if !slices.Contains(batch, r) {
					scmPollLater(d, r, time.Duration(retry)*time.Millisecond)
				}
			}
		}
	}
}

// scmHealthy: events arrive for r — the provider says its webhooks are
// healthy, its repo delivered within 30 min, or (checks) one came for its
// head.
func scmHealthy(d *DB, h *scmHello, r *scmPollRow) bool {
	if h != nil && h.Events.Healthy {
		return true
	}
	if at := scmSeenAt(d, scmDeliveryKey(r.SCM, r.Repo, "")); at > 0 && scmClock()-at < (30*time.Minute).Milliseconds() {
		return true
	}
	return r.Kind == scmPollChecks && r.Item.Ref != "" && scmSeenAt(d, scmDeliveryKey(r.SCM, r.Repo, r.Item.Ref)) > 0
}

// scmPollRead makes the reads of batch: one POST /scm/poll, or each
// route's own read where the provider has no poll. Answers by item id, and
// the provider's retryAfterMs.
func scmPollRead(ctx context.Context, api scmAPI, h *scmHello, as string, batch []*scmPollRow) (map[string]*scmPollResult, int64, error) {
	out := map[string]*scmPollResult{}
	if h == nil || h.has(scmCapPoll) {
		req := scmPollReq{As: as}
		for _, r := range batch {
			it := r.Item.scmPollItem
			it.ID, it.ETag = r.id(), r.ETag
			req.Items = append(req.Items, it)
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		resp, err := api.Poll(cctx, req)
		cancel()
		if err == nil {
			for i := range resp.Items {
				out[resp.Items[i].ID] = &resp.Items[i]
			}
			return out, resp.RetryAfterMs, nil
		}
		if !scmRefused(err, scmRefUnsupported) {
			return nil, 0, err
		}
	}
	for _, r := range batch {
		res := scmReadItem(ctx, api, as, r)
		out[r.id()] = &res
	}
	return out, 0, nil
}

// scmReadItem is one row's read through its own route (a provider with no
// poll).
func scmReadItem(ctx context.Context, api scmAPI, as string, r *scmPollRow) scmPollResult {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res := scmPollResult{ID: r.id()}
	var v any
	var etag string
	var err error
	switch r.Kind {
	case scmPollChecks:
		var c *scmChecks
		if c, err = api.Checks(cctx, r.Repo, r.Item.Ref, r.ETag, as); err == nil && c == nil {
			return res // not modified
		} else if c != nil {
			v, etag = c, c.ETag
		}
	case scmPollPull:
		var pl *scmPull
		if pl, err = api.Pull(cctx, r.Repo, r.Item.Number, as); pl != nil {
			v, etag = pl, pl.ETag
		}
	case scmPollComments:
		var pg *scmPage[scmComment]
		if pg, err = api.Comments(cctx, r.Repo, r.Item.Number, r.Item.Since, as); pg != nil {
			v, etag = pg, pg.ETag
		}
	}
	if err != nil {
		var se *scmError
		if e, ok := err.(*scmError); ok {
			se = e
		} else {
			se = &scmError{Refusal: scmRefUnavailable, Message: err.Error()}
		}
		res.Error = se
		return res
	}
	b, _ := json.Marshal(v)
	if etag == "" || etag != r.ETag {
		res.Changed, res.Value = true, b
	}
	res.ETag = etag
	return res
}

// scmPolled handles one row's answer and schedules its next read.
func scmPolled(ctx context.Context, d *DB, api scmAPI, r *scmPollRow, res *scmPollResult, minMs int64) {
	switch {
	case res == nil:
		scmPollAfter(d, r, nil, "", false, minMs)
		return
	case res.Error != nil:
		if res.Error.Refusal != scmRefNotFound {
			logf("scm poll %s: %v", r.id(), res.Error)
		}
		scmPollAfter(d, r, nil, "", false, minMs)
		return
	case !res.Changed:
		scmPollAfter(d, r, &res.ETag, "", false, minMs)
		return
	}
	var since int64
	final := false
	state := ""
	switch r.Kind {
	case scmPollChecks:
		var c scmChecks
		if json.Unmarshal(res.Value, &c) != nil {
			scmPollAfter(d, r, nil, "", false, minMs)
			return
		}
		state, final = scmApplyChecks(ctx, d, api, r, &c)
	case scmPollPull:
		var pl scmPull
		if json.Unmarshal(res.Value, &pl) == nil {
			final = scmApplyPull(d, r, &pl)
		}
	case scmPollComments:
		var pg scmPage[scmComment]
		if json.Unmarshal(res.Value, &pg) == nil {
			since = scmApplyComments(d, r, &pg)
		}
	}
	if since > r.Item.Since {
		r.Item.Since = since
	}
	if state != "" {
		r.Item.State = state
	}
	scmPollAfter(d, r, &res.ETag, state, final, minMs)
}

// scmPollAfter records a read of r (etag nil: none came) and when the next
// one is due — none once final, or once the cadence gives up (checks: with
// a waking note). The update is skipped when the row changed meanwhile (a
// new head, an event's read): that one stands.
func scmPollAfter(d *DB, r *scmPollRow, etag *string, state string, final bool, minMs int64) {
	now := scmClock()
	age := time.Duration(now-r.PendingSince) * time.Millisecond
	due := int64(0)
	stop := final
	lost := false
	if !stop {
		var delay time.Duration
		delay, stop = scmPollDelay(age, r.healthy)
		lost = stop && r.Kind == scmPollChecks && r.Item.State != scmCINone
		if r.Kind == scmPollChecks && r.Item.State == scmCINone && age >= scmPollNoCI {
			stop = true // nobody reports CI here
		}
		if !stop {
			due = now + max(delay.Milliseconds(), minMs)
		}
	}
	et := r.ETag
	if etag != nil {
		et = *etag
	}
	_ = d.Tx(func(t *DB) error {
		res, err := t.q.Exec(`UPDATE scm_poll SET item=?, etag=?, due_ms=?, step=step+1, last_ms=?, nudge=0
			WHERE project_id=? AND n=? AND repo=? AND kind=? AND item=? AND due_ms=?`,
			scmItemJSON(r.Item), et, due, now, r.Project, r.N, r.Repo, r.Kind, r.raw, r.Due)
		if err != nil || rowsAffected(res) == 0 || !lost {
			return err
		}
		if k, err := t.taskByN(r.Project, r.N); err == nil && k.RunID != 0 && (k.Phase == phaseOpen || k.Phase == phasePR) {
			addProjectEvent(t, r.Project, r.N, pevNote, map[string]any{
				"text": fmt.Sprintf("lost track of CI on %s@%s — check manually", k.Branch, sha7(r.Item.Ref)), "repo": r.Repo, "sha": r.Item.Ref},
				true, fmt.Sprintf("lost:%d:%s:%s", r.N, strings.ToLower(r.Repo), r.Item.Ref))
		}
		return nil
	})
}

// scmPollAt moves r's next read to at, reading nothing.
func scmPollAt(d *DB, r *scmPollRow, at int64) {
	_, _ = d.q.Exec(`UPDATE scm_poll SET due_ms=? WHERE project_id=? AND n=? AND repo=? AND kind=? AND item=? AND due_ms=?`,
		at, r.Project, r.N, r.Repo, r.Kind, r.raw, r.Due)
}

// scmPollLater moves r's next read on by wait.
func scmPollLater(d *DB, r *scmPollRow, wait time.Duration) {
	scmPollAt(d, r, scmClock()+wait.Milliseconds())
}

// --- the loop ---------------------------------------------------------------------------

// scmLoopKick wakes the loop (a row or a subscription to look at now).
var scmLoopKick = make(chan struct{}, 1)

// scmLoopOff stops the loop's passes (the tests make them by hand).
var scmLoopOff atomic.Bool

func scmLoopPoke() {
	select {
	case scmLoopKick <- struct{}{}:
	default:
	}
}

// scmLoop is E's ownerLoops entry: subscriptions and reads, at the next
// one's time, on a kick, at least every 10 min.
func scmLoop(ctx context.Context, e *Engine) {
	var pruned time.Time
	for {
		next := int64(0)
		if !scmLoopOff.Load() && e.db.features {
			if time.Since(pruned) > time.Hour {
				scmPruneSeen(e.db)
				pruned = time.Now()
			}
			scmPass(ctx, e.db)
			next = e.db.scmNextMs()
		}
		wait := 10 * time.Minute
		if next > 0 {
			wait = min(wait, max(time.Duration(next-scmClock())*time.Millisecond, time.Second))
		}
		tm := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			tm.Stop()
			return
		case <-tm.C:
		case <-scmLoopKick:
			tm.Stop()
		}
	}
}

// scmPass is one pass: the issue subscriptions the projects' policies want,
// the subscriptions due, the reads due.
func scmPass(ctx context.Context, d *DB) {
	scmIssueSubsSync(d)
	scmSubsPass(ctx, d)
	scmPollPass(ctx, d)
}

// scmNextMs is when the next read or subscription is due (unix ms; 0:
// none).
func (d *DB) scmNextMs() int64 {
	var a, b sql.NullInt64
	_ = d.q.QueryRow(`SELECT MIN(s.due_ms) FROM scm_poll s JOIN projects p ON p.id=s.project_id WHERE s.due_ms>0 AND p.state='active'`).Scan(&a)
	_ = d.q.QueryRow(`SELECT MIN(next_ms) FROM scm_subs WHERE state IN (?, ?, ?)`, subPost, subLive, subDrop).Scan(&b)
	switch {
	case a.Valid && b.Valid:
		return max(min(a.Int64, b.Int64), 1)
	case a.Valid:
		return max(a.Int64, 1)
	case b.Valid:
		return max(b.Int64, 1)
	}
	return 0
}

// scmWakeAt (userWake, a person's partition at rest) is when a read or a
// subscription is next due, in unix seconds (0: none).
func (d *DB) scmWakeAt() int64 {
	if !d.features {
		return 0
	}
	if ms := d.scmNextMs(); ms > 0 {
		return max(ms/1000, 1)
	}
	return 0
}

// --- what a read says --------------------------------------------------------------------

// scmCINone is checks' state when nothing reported on the commit.
const scmCINone = "none"

// scmLiveTask is r's task and project if events may still change it.
func scmLiveTask(t *DB, pid, n int64) (*Project, *ProjectTask, bool) {
	p, err := t.getProject(pid)
	if err != nil || p.State != projActive {
		return nil, nil, false
	}
	k, err := t.taskByN(pid, n)
	if err != nil || k.RunID == 0 || (k.Phase != phaseOpen && k.Phase != phasePR) {
		return nil, nil, false
	}
	return p, k, true
}

// scmApplyPull: the task's PR as read — merged or closed ends it; an open
// one's head and draft flag are recorded. final: the PR is over.
func scmApplyPull(d *DB, r *scmPollRow, pl *scmPull) (final bool) {
	_ = d.Tx(func(t *DB) error {
		p, k, ok := scmLiveTask(t, r.Project, r.N)
		if !ok {
			final = true
			return nil
		}
		n := orInt(pl.Number, r.Item.Number)
		switch pl.State {
		case "merged", "closed":
			scmPullEnded(t, p, k, r.Repo, n, pl.State == "merged")
			final = true
		default:
			if pl.Head.SHA != "" {
				scmSetHead(t, p, k, r.Repo, n, pl.Head.SHA)
			}
			scmSetDraft(t, p, k, r.Repo, n, pl.Draft)
		}
		return nil
	})
	return final
}

// scmSemKey is a semantic dedupe key (§11.4): the same fact from an event
// and a poll is acted on once.
func scmSemKey(pid, n int64, kind, ref, sub, state string) string {
	return fmt.Sprintf("sem:%d:%d:%s:%s:%s:%s", pid, n, kind, strings.ToLower(ref), sub, state)
}
