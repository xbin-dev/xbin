// ci_watch.go — a CI watch's life (API.md §CI in the conversation): made or
// refreshed (at most ciMaxLive per conversation, the oldest pushed one
// ending first), its subscription at the provider (key ci:<id>), reading
// GET /scm/checks for it (conditional on its etag, one read per watch at a
// time, shared by everyone who asks meanwhile), a task's watches (from its
// refs, and lazily when its CI is asked for), ending (unwatched, the
// conversation deleted, a day after its branch went), and the background
// refresher — an ownerLoops entry that re-reads a watch with anything not
// completed (a failed one too, while other jobs run) that nobody's
// events moved: every minute for 20 minutes after its push, then at the
// polling cadence (10 minutes until 2 hours, 30 until a day), then not until
// someone looks. Reads are made as the home's identity at the provider: the
// project's for a task, else the person in their own partition, the bot
// elsewhere — never a token in a sandbox.
package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func init() {
	projectRefsHooks = append(projectRefsHooks, ciTaskRefs)
	runDeletedHooks = append(runDeletedHooks, ciRunDeleted)
	ownerLoops = append(ownerLoops, ciLoop)
}

// errCILimit: a conversation watches ciMaxLive branches, none of them a
// pushed one that could make room.
var errCILimit = errors.New("this conversation already watches CI for 10 branches: stop watching one first")

// ciKinds are what a watch subscribes to: what CI says, and what moves the
// branch (a push, its pull request).
var ciKinds = []string{scmKindChecks, scmKindPull, scmKindWorkflow, scmKindJob, scmKindCheck, scmKindPush}

// ciAs is the identity a watch is read as: a task's project's, else the
// person in their own partition, else the bot.
func ciAs(d *DB, w *ciWatch) string {
	if w.ProjectID != 0 {
		if p, err := d.getProject(w.ProjectID); err == nil {
			return projectAs(p)
		}
	}
	if userMode() {
		return scmAsPerson
	}
	return scmAsBot
}

// ciUpsert makes w live in t, or refreshes the live watch of the same
// conversation and branch (answered instead, created false). A new head
// starts the snapshot over. At the limit the oldest pushed watch ends;
// with none, errCILimit.
func ciUpsert(t *DB, w *ciWatch) (*ciWatch, bool, error) {
	live := t.ciLive(w.RootRun)
	for _, x := range live {
		if x.SCM != w.SCM || !strings.EqualFold(x.Repo, w.Repo) || x.Ref != w.Ref {
			continue
		}
		changed, wasGone := false, x.State == ciGone
		if w.Source == ciTask && x.Source != ciTask {
			x.Source, x.ProjectID, x.N, changed = ciTask, w.ProjectID, w.N, true
		}
		if w.Source == ciPushed && x.Source == ciPushed && w.RunID != 0 && x.RunID != w.RunID {
			x.RunID, changed = w.RunID, true
		}
		if w.PR > 0 && x.PR != w.PR {
			x.PR, changed = w.PR, true
		}
		if w.Host != "" && x.Host == "" {
			x.Host, changed = w.Host, true
		}
		if w.SHA != "" && x.SHA != w.SHA {
			x.moveTo(w.SHA) // a new head: the branch is there (not gone)
			changed = true
		}
		switch {
		case w.State == ciGone && x.State != ciGone:
			x.State, changed = ciGone, true
		case w.State != ciGone && x.State == ciGone && w.PR > 0:
			x.State, changed = ciNone, true // an open pull request of the branch: not gone
		}
		if changed && !(wasGone && x.State == ciGone) {
			x.UpdatedMs = nowMs() // a gone watch keeps when it went
		}
		if changed {
			if err := t.ciSave(x); err != nil {
				return nil, false, err
			}
			ciChanged(t, x.RootRun, x.ID)
		}
		return x, false, nil
	}
	if len(live) >= ciMaxLive {
		var oldest *ciWatch
		for _, x := range live {
			if x.Source == ciPushed && (oldest == nil || x.ID < oldest.ID) {
				oldest = x
			}
		}
		if oldest == nil {
			return nil, false, errCILimit
		}
		if err := ciEnd(t, oldest); err != nil {
			return nil, false, err
		}
	}
	if err := t.ciInsert(w); err != nil {
		return nil, false, err
	}
	ciChanged(t, w.RootRun, w.ID)
	return w, true, nil
}

// ciEnd ends w in t; its subscription goes after the commit.
func ciEnd(t *DB, w *ciWatch) error {
	w.EndedMs = nowMs()
	if _, err := t.q.Exec(`UPDATE ci_watch SET ended_ms=? WHERE id=?`, w.EndedMs, w.ID); err != nil {
		return err
	}
	ciChanged(t, w.RootRun, w.ID)
	scm, key := w.SCM, w.SubKey
	t.AfterCommit(func() { go ciUnsubscribe(scm, key) })
	return nil
}

// ciStarted: what follows a new watch, off every transaction — a pushed
// one's pull request looked up once, its subscription, its first read.
func ciStarted(t *DB, id int64) {
	d := ciBase(t)
	t.AfterCommit(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			ciSetUp(ctx, d, id)
			_ = ciRead(ctx, d, id)
		}()
	})
}

// ciRenewLater posts watch id's subscription again after the commit (its
// head moved): a subscription lapses 30 days after it was posted, and a
// branch still pushed to keeps its own alive this way.
func ciRenewLater(t *DB, id int64) {
	d := ciBase(t)
	t.AfterCommit(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			ciSetUp(ctx, d, id)
		}()
	})
}

// ciReadLater reads watch id after the commit, off the caller's path.
func ciReadLater(t *DB, id int64) {
	d := ciBase(t)
	t.AfterCommit(func() {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			_ = ciRead(ctx, d, id)
		}()
	})
}

// ciBase is the database t is a view of, outside any transaction: what
// work after the commit uses (never the package's agent, which tests swap).
func ciBase(t *DB) *DB { return &DB{sql: t.sql, q: t.sql, features: t.features} }

// ciSetUp looks a watch's open pull request up (when it has none) and
// subscribes to its branch's events at the provider (when it offers them).
func ciSetUp(ctx context.Context, d *DB, id int64) {
	w := d.ciWatchByID(id)
	if w == nil || w.EndedMs != 0 {
		return
	}
	api, err := scmFor(w.SCM)
	if err != nil {
		return
	}
	if w.PR == 0 && w.Ref != "" {
		if page, err := api.Pulls(ctx, scmQuery{Repo: w.Repo, Head: w.Ref, State: "open", As: ciAs(d, w), Limit: 5}); err == nil {
			for _, pl := range page.Items {
				if pl.Head.Ref == w.Ref && pl.Number > 0 {
					_ = d.Tx(func(t *DB) error {
						cur := t.ciWatchByID(id)
						if cur == nil || cur.PR != 0 {
							return nil
						}
						cur.PR = pl.Number
						if err := t.ciSave(cur); err != nil {
							return err
						}
						ciChanged(t, cur.RootRun, cur.ID)
						return nil
					})
					break
				}
			}
		}
	}
	if h, err := api.Hello(ctx); err != nil || !h.has(scmCapEvents) {
		return
	}
	if _, err := api.Subscribe(ctx, scmSubscription{Repo: w.Repo, Branches: []string{w.Ref}, Kinds: ciKinds, Key: w.SubKey}); err != nil {
		logf("ci watch %d: subscribing to %s %s: %v", w.ID, w.Repo, w.Ref, err)
	}
}

// ciUnsubscribe drops the subscription keyed key at provider scm.
func ciUnsubscribe(scm, key string) {
	if key == "" {
		return
	}
	api, err := scmFor(scm)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if h, err := api.Hello(ctx); err != nil || !h.has(scmCapEvents) {
		return
	}
	subs, err := api.Subscriptions(ctx)
	if err != nil {
		return
	}
	for _, s := range subs {
		if s.Key == key && s.ID != "" {
			_ = api.Unsubscribe(ctx, s.ID)
		}
	}
}

// --- reading ----------------------------------------------------------------------------

// ciReads is the read in flight per watch: one upstream read at a time,
// whoever else asks meanwhile waits for it.
var ciReads = struct {
	sync.Mutex
	m map[ciKey]chan struct{}
}{m: map[ciKey]chan struct{}{}}

// ciRead reads watch id's checks now — or, with one in flight, waits for
// that one (or ctx).
func ciRead(ctx context.Context, d *DB, id int64) error {
	key := ciKey{d.sql, id}
	ciReads.Lock()
	if ch := ciReads.m[key]; ch != nil {
		ciReads.Unlock()
		select {
		case <-ch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	ch := make(chan struct{})
	ciReads.m[key] = ch
	ciReads.Unlock()
	defer func() {
		ciReads.Lock()
		delete(ciReads.m, key)
		ciReads.Unlock()
		close(ch)
	}()
	ciReadCount.Add(1)
	return ciReadNow(ctx, d, id)
}

// ciReadCount counts upstream reads (tests).
var ciReadCount atomic.Int64

// ciReadNow is one conditional GET /scm/checks for a watch, recorded: the
// snapshot (a new head starts it over), its state and outcome — or the
// provider's refusal, kept on the watch (signin: the dock offers a sign-in).
func ciReadNow(ctx context.Context, d *DB, id int64) error {
	w := d.ciWatchByID(id)
	if w == nil || w.EndedMs != 0 {
		return nil
	}
	api, err := scmFor(w.SCM)
	var res *scmChecks
	if err == nil {
		ref, etag := orStr(w.Ref, w.SHA), ""
		if w.Snapshot != "" {
			etag = w.ETag
		}
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		res, err = api.Checks(cctx, w.Repo, ref, etag, ciAs(d, w))
		cancel()
	}
	if err != nil && ctx.Err() != nil {
		return err // the caller gave up: nothing learned
	}
	return d.Tx(func(t *DB) error {
		cur := t.ciWatchByID(id)
		if cur == nil || cur.EndedMs != 0 {
			return nil
		}
		now := nowMs()
		before := cur.Error + "|" + cur.Refusal
		cur.FetchedMs = now
		switch {
		case err != nil:
			cur.Error, cur.Refusal = ciText(err.Error(), 500), ""
			var se *scmError
			if errors.As(err, &se) {
				cur.Error, cur.Refusal = ciText(se.Message, 500), ciRefusal(se.Refusal)
			}
		case res == nil: // not modified
			cur.Error, cur.Refusal = "", ""
		default:
			cur.Error, cur.Refusal = "", ""
			if sha := ciSHA(res.SHA); sha != "" && sha != cur.SHA {
				if cur.SHA != "" {
					cur.moveTo(sha)
					cur.FetchedMs = now
					ciRenewLater(t, cur.ID)
				}
				cur.SHA = sha
			}
			cur.setChecks(res)
			cur.ETag = clip(res.ETag, 200)
			cur.touch(now)
		}
		if res == nil && err == nil && cur.UpdatedMs < now && cur.FetchedMs > cur.UpdatedMs {
			cur.touch(now) // nothing changed: its snapshot is as fresh as this read
		}
		if err := t.ciSave(cur); err != nil {
			return err
		}
		if res != nil || before != cur.Error+"|"+cur.Refusal {
			ciChanged(t, cur.RootRun, cur.ID)
		}
		return nil
	})
}

// ciHealthy: provider scm's webhooks arrive (its hello says so).
func ciHealthy(ctx context.Context, scm string) bool {
	api, err := scmFor(scm)
	if err != nil {
		return false
	}
	h, err := api.Hello(ctx)
	return err == nil && h.Events.Healthy
}

// ciStale: a read would tell more — the watch has anything not completed
// (or nothing yet: ciOpen, whatever its state — a failed job doesn't stop
// the others) and its snapshot is older than after (10 s with webhooks
// unhealthy, 30 s healthy).
func ciStale(w *ciWatch, after time.Duration) bool {
	if w.EndedMs != 0 || w.State == ciGone {
		return false
	}
	if w.FetchedMs == 0 {
		return true
	}
	if !ciOpen(w) {
		return false
	}
	return time.Since(time.UnixMilli(w.FetchedMs)) >= after
}

// ciFreshen reads root's watches that a read would tell more about (fresh:
// the dock's every-15-s ask; else only those never read), in parallel, and
// waits for them (or ctx).
func ciFreshen(ctx context.Context, d *DB, root int64, fresh bool) {
	var wg sync.WaitGroup
	healthy := map[string]bool{}
	for _, w := range d.ciLive(root) {
		after := time.Duration(1 << 62)
		if fresh {
			if _, ok := healthy[w.SCM]; !ok {
				healthy[w.SCM] = ciHealthy(ctx, w.SCM)
			}
			after = ciFreshUnhealthy
			if healthy[w.SCM] {
				after = ciFreshHealthy
			}
		}
		if !ciStale(w, after) {
			continue
		}
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_ = ciRead(ctx, d, id)
		}(w.ID)
	}
	wg.Wait()
}

// How old a snapshot may be before the dock's fresh read reads it again.
var (
	ciFreshUnhealthy = 10 * time.Second
	ciFreshHealthy   = 30 * time.Second
)

// --- a task's watches -------------------------------------------------------------------

// ciTaskRefs (projectRefsHooks): k's branch on the remote and its pull
// requests, as its refs job just recorded them — a watch per repo the
// branch was pushed to, with its open pull request; a merged or closed one
// makes the watch gone.
func ciTaskRefs(t *DB, p *Project, k *ProjectTask) {
	if p == nil || k == nil || k.RunID == 0 || k.Branch == "" {
		return
	}
	prs := k.taskPRs()
	for _, c := range t.checkouts(k.ID) {
		r, err := t.projectRepo(p.ID, c.Repo)
		if err != nil || r.Repo == "" {
			continue
		}
		w := &ciWatch{RootRun: k.RunID, RunID: k.RunID, ProjectID: p.ID, N: k.N, Source: ciTask, SCM: p.SCM, Host: p.Host,
			Repo: r.Repo, Ref: k.Branch, SHA: ciSHA(c.RemoteSHA)}
		gone := false
		for _, pr := range prs {
			if !strings.EqualFold(pr.Repo, r.Repo) {
				continue
			}
			switch pr.State {
			case "open":
				w.PR, gone = pr.Number, false
				if sha := ciSHA(pr.HeadSHA); sha != "" {
					w.SHA = sha
				}
			case "merged", "closed":
				if w.PR == 0 {
					w.PR, gone = pr.Number, true
				}
			}
		}
		if w.SHA == "" && w.PR == 0 {
			continue // not on the remote yet
		}
		if gone {
			w.State = ciGone
			if ciEndedGone(t, w) {
				continue // its CI was watched to the end already: not again
			}
		}
		x, created, err := ciUpsert(t, w)
		if err != nil {
			logf("project %d task %d: watching CI of %s: %v", p.ID, k.N, r.Repo, err)
			continue
		}
		if created {
			ciStarted(t, x.ID)
		} else if x.FetchedMs == 0 {
			ciReadLater(t, x.ID)
		}
	}
}

// ciEndedGone: w (gone, not yet made) would only repeat a task watch of
// the same conversation, repo, branch and head that went and ended — none
// of the same branch live.
func ciEndedGone(t *DB, w *ciWatch) bool {
	var live, ended int
	_ = t.q.QueryRow(`SELECT coalesce(sum(ended_ms=0), 0), coalesce(sum(ended_ms>0 AND sha=? AND source=?), 0)
		FROM ci_watch WHERE root_run=? AND scm=? AND lower(repo)=lower(?) AND ref=?`, w.SHA, ciTask, w.RootRun, w.SCM, w.Repo, w.Ref).Scan(&live, &ended)
	return live == 0 && ended > 0
}

// ciTaskLazy: GET /runs/{id}/ci on a task that never had a watch makes
// them from what its last refs check recorded. A closed or done task, or
// one whose watches ended (gone a day), is left as it is: its refs job
// makes a watch again when its branch moves — a visit never re-subscribes.
func ciTaskLazy(d *DB, root int64) {
	run, err := d.getRun(root)
	if err != nil || run.Origin != originProject {
		return
	}
	var n int
	if d.q.QueryRow(`SELECT count(*) FROM ci_watch WHERE root_run=? AND source=?`, root, ciTask).Scan(&n) != nil || n > 0 {
		return
	}
	p, k := d.projectOfRun(run)
	if p == nil || k == nil || k.Phase == phaseClosed || k.Phase == phaseDone {
		return
	}
	_ = d.Tx(func(t *DB) error {
		ciTaskRefs(t, p, k)
		return nil
	})
}

// ciRunDeleted (runDeletedHooks): a deleted conversation's watches go, rows
// and all (their snapshots were its CI); their subscriptions after the
// commit.
func ciRunDeleted(t *DB, id int64) error {
	ws := t.ciWatchesWhere(`WHERE root_run=?`, id)
	if len(ws) == 0 {
		return nil
	}
	if _, err := t.q.Exec(`DELETE FROM ci_watch WHERE root_run=?`, id); err != nil {
		return err
	}
	ciCacheInTx(t, id, nil)
	for _, w := range ws {
		if w.EndedMs == 0 {
			scm, key := w.SCM, w.SubKey
			t.AfterCommit(func() { go ciUnsubscribe(scm, key) })
		}
	}
	return nil
}

// --- the background refresher (ownerLoops) ----------------------------------------------

// ciTimes is the refresher's clock: how often it looks, how long an event
// for a watch keeps it from reading, the cadence after a push, when a gone
// watch ends and when an ended one's row goes. Swapped whole (tests).
type ciTimes struct {
	Tick, EventHold  time.Duration
	Cadence          []struct{ Until, Every time.Duration }
	GoneFor, KeptFor time.Duration
}

var ciClock atomic.Pointer[ciTimes]

func init() {
	ciClock.Store(&ciTimes{Tick: 15 * time.Second, EventHold: 2 * time.Minute,
		Cadence: []struct{ Until, Every time.Duration }{
			{20 * time.Minute, time.Minute},
			{2 * time.Hour, 10 * time.Minute},
			{24 * time.Hour, 30 * time.Minute},
		},
		GoneFor: 24 * time.Hour, KeptFor: 7 * 24 * time.Hour})
}

// ciEvented is when an event last moved each watch (memory: a restart only
// costs a read).
var ciEvented sync.Map // ciKey{database, watch id} → unix ms

// ciLoop is the refresher: an engine owner's loop, never holding the
// engine up or waking it, stopped with ctx.
func ciLoop(ctx context.Context, e *Engine) {
	if e == nil || e.db == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(ciClock.Load().Tick):
		}
		ciPass(ctx, e.db)
	}
}

// ciPass is one look: summaries a rolled-back transaction left are read
// again, gone watches a day old end, ended rows a week old go, and each
// watch with anything not completed (ciOpen — a failed one too, while
// other jobs run) that nobody's events moved lately and whose turn at the
// cadence came is read.
func ciPass(ctx context.Context, d *DB) {
	ciSettle(d)
	now, clk := time.Now(), ciClock.Load()
	for _, w := range d.ciWatchesWhere(`WHERE ended_ms=0 AND state=? AND updated_ms<?`, ciGone, now.Add(-clk.GoneFor).UnixMilli()) {
		_ = d.Tx(func(t *DB) error { return ciEnd(t, w) })
	}
	_, _ = d.q.Exec(`DELETE FROM ci_watch WHERE ended_ms>0 AND ended_ms<?`, now.Add(-clk.KeptFor).UnixMilli())
	for _, w := range d.ciWatchesWhere(`WHERE ended_ms=0 AND state<>? ORDER BY id`, ciGone) {
		if ctx.Err() != nil {
			return
		}
		if !ciOpen(w) {
			continue // everything completed: nothing a read would tell
		}
		if at, ok := ciEvented.Load(ciKey{d.sql, w.ID}); ok && now.Sub(time.UnixMilli(at.(int64))) < clk.EventHold {
			continue
		}
		every := time.Duration(0)
		age := now.Sub(time.UnixMilli(w.Since))
		for _, c := range clk.Cadence {
			if age < c.Until {
				every = c.Every
				break
			}
		}
		if every == 0 || (w.FetchedMs != 0 && now.Sub(time.UnixMilli(w.FetchedMs)) < every) {
			continue
		}
		_ = ciRead(ctx, d, w.ID)
	}
}

// ciRefusal keeps a provider's refusal word: lower-case letters and -.
func ciRefusal(s string) string {
	if len(s) > 40 {
		return ""
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r == '-') {
			return ""
		}
	}
	return s
}
