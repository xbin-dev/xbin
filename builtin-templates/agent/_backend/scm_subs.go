// scm_subs.go — what an scm provider is asked to send this home, and what
// routes it here (API.md §scm events and polling, "Subscriptions").
//
//   - The refs hook (projectRefsHooks: the task's refs job saw its branch on
//     the remote, a PR open or change; an event moved its head) writes the
//     task's project_refs — its branch, each PR's number and head, the
//     branch's pushed sha, per repo — and keeps, per task and repo once the
//     branch is on the remote or a PR exists, one subscription, key
//     task:<pid>:<n>:<repo slug>: {repo, branches: [its branch], prs: [its
//     PRs], kinds: pull, checks, comment, review, push and the progress kinds
//     workflow, job, check}. It also keeps the task's poll rows (scm_poll.go).
//   - A project whose policy names an autoLabel keeps one issue subscription
//     per repo, key issues:<pid>:<repo slug>: {repo, issues: true, kinds:
//     [issue]}. Team definitions subscribe to nothing.
//   - scm_subs holds each subscription this home wants, and the loop
//     (scm_poll.go scmLoop) posts it (POST /scm/subscriptions — the same key
//     replaces it there), posts it again at 25 days (a subscription lapses at
//     30), and deletes it once it isn't wanted: the task cleaned up or its
//     conversation deleted, the task no longer open when it is due again, an
//     issue subscription whose project or label is gone. A provider without
//     events keeps none (polling stands in); its row looks again after 6 h.
//
// A subscription made in a person's partition reaches that person's
// partition of a partitioned provider, which marks it for them (for:
// user:<id>); at the global instance or an unpartitioned agent, global.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

func init() {
	projectRefsHooks = append(projectRefsHooks, scmRefsChanged)
	taskChangedHooks = append(taskChangedHooks, scmTaskChanged)
	// the worker's kinds of §14.2: the loop does this work (it never holds
	// the engine), so a queued one only wakes it
	projectJobKinds[pjPoll] = scmKickJob
	projectJobKinds[pjSubscribe] = scmKickJob
}

// scm_subs states.
const (
	subPost = "post" // to post (or post again)
	subLive = "live" // posted; next_ms is when to post it again
	subDrop = "drop" // to delete at the provider, then here
)

// scmSubRepost is when a subscription is posted again (it lapses at 30
// days); scmSubIdle how long a provider that keeps none is left.
var (
	scmSubRepost = 25 * 24 * time.Hour
	scmSubIdle   = 6 * time.Hour
)

// scmTaskKinds is what a task's subscription asks for.
var scmTaskKinds = []string{scmKindPull, scmKindChecks, scmKindComment, scmKindReview, scmKindPush, scmKindWorkflow, scmKindJob, scmKindCheck}

func scmTaskSubKey(pid, n int64, slug string) string {
	return fmt.Sprintf("task:%d:%d:%s", pid, n, slug)
}

func scmIssueSubKey(pid int64, slug string) string {
	return fmt.Sprintf("issues:%d:%s", pid, slug)
}

// scmKickJob is a poll or subscribe job: the loop's work, which it wakes.
func scmKickJob(context.Context, *Project, *ProjectTask, *ProjectJob) (jobOutcome, error) {
	scmLoopPoke()
	return doneJob("the scm loop looks now")
}

// --- the refs hook --------------------------------------------------------------------

// scmRefsChanged (projectRefsHooks): task k's refs changed — its routing
// rows, its subscriptions, its poll rows.
func scmRefsChanged(t *DB, p *Project, k *ProjectTask) {
	if p == nil || k == nil || p.Kind == projTeam || k.RunID == 0 {
		return
	}
	now := scmClock()
	_, _ = t.q.Exec(`DELETE FROM project_refs WHERE project_id=? AND n=?`, p.ID, k.N)
	prs := k.taskPRs()
	cos := t.checkouts(k.ID)
	for _, r := range scmTaskRepos(t, p, k) {
		lr := strings.ToLower(r.Repo)
		add := func(kind, value string) {
			if value != "" {
				_, _ = t.q.Exec(`INSERT OR IGNORE INTO project_refs (scm, repo, kind, value, project_id, n, created) VALUES (?, ?, ?, ?, ?, ?, ?)`,
					p.SCM, lr, kind, value, p.ID, k.N, now)
			}
		}
		pushed := ""
		for _, c := range cos {
			if c.Repo == r.Slug {
				pushed = strings.ToLower(c.RemoteSHA)
			}
		}
		add(refBranch, k.Branch)
		add(refSHA, pushed)
		var nums []int
		var open *TaskPR
		for i, pr := range prs {
			if !strings.EqualFold(pr.Repo, r.Repo) || pr.Number == 0 {
				continue
			}
			add(refPR, strconv.Itoa(pr.Number))
			add(refSHA, strings.ToLower(pr.HeadSHA))
			nums = append(nums, pr.Number)
			if pr.State == "open" {
				open = &prs[i]
			}
		}
		if pushed != "" || len(nums) > 0 {
			slices.Sort(nums)
			scmWantSub(t, scmTaskSubKey(p.ID, k.N, r.Slug), p, k.N, scmSubscription{Repo: r.Repo, Branches: []string{k.Branch}, PRs: nums,
				Kinds: scmTaskKinds, Key: scmTaskSubKey(p.ID, k.N, r.Slug)})
		}
		head := pushed
		if open != nil && open.HeadSHA != "" {
			head = open.HeadSHA
		}
		scmPollTrack(t, p, k, r.Repo, open, head)
	}
}

// scmWantSub records that this home wants subscription s under key: posted
// (again) when it differs from what was posted.
func scmWantSub(t *DB, key string, p *Project, n int64, s scmSubscription) {
	body, _ := json.Marshal(s)
	var old, state string
	err := t.q.QueryRow(`SELECT body, state FROM scm_subs WHERE key=?`, key).Scan(&old, &state)
	if err == nil && old == string(body) && state != subDrop {
		return
	}
	_, _ = t.q.Exec(`INSERT INTO scm_subs (key, scm, repo, project_id, n, body, state, next_ms, tries, error) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, '')
		ON CONFLICT(key) DO UPDATE SET scm=excluded.scm, repo=excluded.repo, body=excluded.body, state=excluded.state, next_ms=0, tries=0, error=''`,
		key, p.SCM, s.Repo, p.ID, n, string(body), subPost)
	t.AfterCommit(scmLoopPoke)
}

// scmDropSubs marks the subscriptions of task n (n 0: every one of project
// pid) to be deleted.
func scmDropSubs(t *DB, pid, n int64) {
	q := `UPDATE scm_subs SET state=?, next_ms=0, tries=0 WHERE project_id=? AND state<>?`
	args := []any{subDrop, pid, subDrop}
	if n > 0 {
		q += ` AND n=?`
		args = append(args, n)
	}
	if res, err := t.q.Exec(q, args...); err == nil && rowsAffected(res) > 0 {
		t.AfterCommit(scmLoopPoke)
	}
}

// scmTaskChanged (taskChangedHooks): a task that is over reads nothing
// more; one cleaned up or deleted is routed nothing more and keeps no
// subscription.
func scmTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if p == nil || k == nil {
		return
	}
	switch what {
	case "phase", "ws", "deleted":
	default:
		return
	}
	if k.Phase != phaseOpen && k.Phase != phasePR {
		_, _ = t.q.Exec(`DELETE FROM scm_poll WHERE project_id=? AND n=?`, p.ID, k.N)
	}
	if k.Phase == phaseDeleted || k.RunID == 0 || k.WS == wsCleaned {
		_, _ = t.q.Exec(`DELETE FROM project_refs WHERE project_id=? AND n=?`, p.ID, k.N)
		scmDropSubs(t, p.ID, k.N)
	}
}

// --- issue subscriptions ------------------------------------------------------------------

// scmIssueSubsSync keeps the issue subscriptions the projects' policies
// want (autoLabel set, a project active and not a team definition), and
// drops the ones no longer wanted.
func scmIssueSubsSync(d *DB) {
	want := map[string]bool{}
	ps, _ := d.projectsWhere(`WHERE state='active' AND kind<>?`, projTeam)
	_ = d.Tx(func(t *DB) error {
		for _, p := range ps {
			if policyOf(p.Policy).AutoLabel == "" {
				continue
			}
			repos, _ := t.projectRepos(p.ID)
			for _, r := range repos {
				key := scmIssueSubKey(p.ID, r.Slug)
				want[key] = true
				scmWantSub(t, key, p, 0, scmSubscription{Repo: r.Repo, Issues: true, Kinds: []string{scmKindIssue}, Key: key})
			}
		}
		rows, err := t.q.Query(`SELECT key FROM scm_subs WHERE key LIKE 'issues:%' AND state<>?`, subDrop)
		if err != nil {
			return nil
		}
		var drop []string
		for rows.Next() {
			var k string
			if rows.Scan(&k) == nil && !want[k] {
				drop = append(drop, k)
			}
		}
		rows.Close()
		for _, k := range drop {
			_, _ = t.q.Exec(`UPDATE scm_subs SET state=?, next_ms=0, tries=0 WHERE key=?`, subDrop, k)
		}
		if len(drop) > 0 {
			t.AfterCommit(scmLoopPoke)
		}
		return nil
	})
}

// --- the subscriptions pass --------------------------------------------------------------

// scmSubRow is a row of scm_subs.
type scmSubRow struct {
	Key, SCM, Repo, Body, SubID, State string
	Project, N                         int64
	Tries                              int
}

// scmSubWanted: a task's subscription is still wanted when it is due (the
// task open, its project active); an issue one, while its project is
// active.
func scmSubWanted(d *DB, s scmSubRow) bool {
	p, err := d.getProject(s.Project)
	if err != nil || p.State != projActive {
		return false
	}
	if strings.HasPrefix(s.Key, "issues:") {
		return policyOf(p.Policy).AutoLabel != ""
	}
	k, err := d.taskByN(s.Project, s.N)
	return err == nil && k.RunID != 0 && (k.Phase == phaseOpen || k.Phase == phasePR) && k.WS != wsCleaned
}

// scmSubsPass posts, posts again and deletes the subscriptions due.
func scmSubsPass(ctx context.Context, d *DB) {
	now := scmClock()
	rows, err := d.q.Query(`SELECT key, scm, repo, body, sub_id, state, project_id, n, tries FROM scm_subs
		WHERE state IN (?, ?, ?) AND next_ms<=? ORDER BY next_ms LIMIT 100`, subPost, subLive, subDrop, now)
	if err != nil {
		return
	}
	var due []scmSubRow
	for rows.Next() {
		var s scmSubRow
		if rows.Scan(&s.Key, &s.SCM, &s.Repo, &s.Body, &s.SubID, &s.State, &s.Project, &s.N, &s.Tries) == nil {
			due = append(due, s)
		}
	}
	rows.Close()
	for _, s := range due {
		if ctx.Err() != nil {
			return
		}
		if s.State != subDrop && !scmSubWanted(d, s) {
			s.State = subDrop
		}
		api, err := scmFor(s.SCM)
		if s.State == subDrop {
			if s.SubID != "" && err == nil {
				cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = api.Unsubscribe(cctx, s.SubID)
				cancel()
				if err != nil && !scmRefused(err, scmRefNotFound) {
					scmSubRetry(d, s, err)
					continue
				}
			}
			_, _ = d.q.Exec(`DELETE FROM scm_subs WHERE key=? AND body=? AND sub_id=?`, s.Key, s.Body, s.SubID)
			continue
		}
		if err != nil {
			scmSubRetry(d, s, err)
			continue
		}
		var body scmSubscription
		_ = json.Unmarshal([]byte(s.Body), &body)
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		got, err := api.Subscribe(cctx, body)
		cancel()
		if err != nil {
			scmSubRetry(d, s, err)
			continue
		}
		now := scmClock()
		_, _ = d.q.Exec(`UPDATE scm_subs SET state=?, sub_id=?, posted_ms=?, expires_ms=?, next_ms=?, tries=0, error=''
			WHERE key=? AND body=? AND state IN (?, ?)`, subLive, got.ID, now, got.Expires, now+scmSubRepost.Milliseconds(),
			s.Key, s.Body, subPost, subLive)
	}
}

// scmSubRetry backs a subscription's try off: 1 min doubling to 1 h; a
// provider that keeps no subscriptions (501), 6 h.
func scmSubRetry(d *DB, s scmSubRow, err error) {
	wait := time.Minute << min(s.Tries, 6)
	wait = min(wait, time.Hour)
	if scmRefused(err, scmRefUnsupported) {
		wait = scmSubIdle
	} else {
		logf("scm subscription %s at %s: %v", s.Key, s.SCM, err)
	}
	_, _ = d.q.Exec(`UPDATE scm_subs SET tries=tries+1, error=?, next_ms=? WHERE key=? AND state=?`,
		clip(projRedact(err.Error()), 400), scmClock()+wait.Milliseconds(), s.Key, s.State)
}
