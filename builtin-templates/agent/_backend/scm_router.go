// scm_router.go — what an scm event does to a project's task (API.md §scm
// events and polling, "Routing"). An event is matched through project_refs
// — the task's pull request, then its branch, then a head sha — to (project,
// task); issue events go to the projects that subscribed to the repo's
// issues. Then, per kind:
//
//   - checks.completed on the task's current head (else ignored: superseded)
//     schedules a read of the head's checks after policy.ci.delaySec — the
//     read (scm_poll.go, the same one polling makes) decides: failing jobs
//     with their logs as a task input, or green as pr.ready;
//   - review and comment on the task's PR schedule its timeline's read
//     after policy.reviews.batchSec (coalesced): trusted people's words are
//     forwarded to the task, anyone else's is a quiet event;
//   - pull.opened/reopened of the task's branch asks the task's refs job to
//     record the PR (projectRefsCheck); pull.merged/closed ends it (phase,
//     scrub, cleanup per policy, a waking event); synchronize moves its head;
//   - push to the task's branch moves its head and, by anyone but the task's
//     own identity, queues a note to the task;
//   - issue.opened/labeled: a quiet event, waking with policy.autoLabel.
//
// The provider's own app (actor.self) and the task's own identity (the login
// of its credential) are ignored but for the facts they carry: a head that
// moved, a PR merged or closed, CI's result on a commit (whoever pushed it).
// Every input to a task goes through project_queue (source event, held
// while its run waits for a person) and the pump.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// scmRefKinds: what project_refs maps to a task.
const (
	refBranch = "branch"
	refPR     = "pr"
	refSHA    = "sha"
)

// scmTaskRef is a task an event routes to.
type scmTaskRef struct{ pid, n int64 }

// scmMatch is the tasks ev routes to: by its PR, else its branch, else its
// sha — in this provider and repo.
func scmMatch(t *DB, ev *scmEvent) []scmTaskRef {
	repo := strings.ToLower(ev.Repo)
	try := func(kind, value string) []scmTaskRef {
		if value == "" {
			return nil
		}
		rows, err := t.q.Query(`SELECT DISTINCT project_id, n FROM project_refs WHERE scm=? AND repo=? AND kind=? AND value=?`,
			ev.SCM.Provider, repo, kind, value)
		if err != nil {
			return nil
		}
		defer rows.Close()
		var out []scmTaskRef
		for rows.Next() {
			var r scmTaskRef
			if rows.Scan(&r.pid, &r.n) == nil {
				out = append(out, r)
			}
		}
		return out
	}
	pr := ev.Ref.PR
	if pr == 0 && ev.Kind == scmKindPull {
		var d struct{ Number int }
		_ = json.Unmarshal(ev.Data[scmKindPull], &d)
		pr = d.Number
	}
	if pr > 0 {
		if m := try(refPR, strconv.Itoa(pr)); len(m) > 0 {
			return m
		}
	}
	if m := try(refBranch, ev.Ref.Branch); len(m) > 0 {
		return m
	}
	return try(refSHA, strings.ToLower(ev.Ref.SHA))
}

// scmRoute routes a (non-progress) event to the projects' tasks (in t).
func scmRoute(t *DB, ev *scmEvent) error {
	if ev.Kind == scmKindIssue {
		scmRouteIssue(t, ev)
		return nil
	}
	for _, m := range scmMatch(t, ev) {
		p, err := t.getProject(m.pid)
		if err != nil || p.State != projActive || p.SCM != ev.SCM.Provider {
			continue
		}
		k, err := t.taskByN(m.pid, m.n)
		if err != nil || k.RunID == 0 || k.Phase == phaseDeleted {
			continue
		}
		repo, ok := scmTaskRepo(t, p, k, ev.Repo)
		if !ok {
			continue
		}
		scmRouteTask(t, p, k, repo, ev)
	}
	return nil
}

// scmTaskRepo is the project repo (owner/name, as the project spells it)
// of task k that names repo.
func scmTaskRepo(t *DB, p *Project, k *ProjectTask, repo string) (string, bool) {
	for _, r := range scmTaskRepos(t, p, k) {
		if strings.EqualFold(r.Repo, repo) {
			return r.Repo, true
		}
	}
	return "", false
}

// scmTaskRepos are the project repos task k works in (its slugs; none
// named: every repo of the project).
func scmTaskRepos(t *DB, p *Project, k *ProjectTask) []ProjectRepo {
	all, _ := t.projectRepos(p.ID)
	if len(k.Repos) == 0 {
		return all
	}
	var out []ProjectRepo
	for _, r := range all {
		for _, s := range k.Repos {
			if r.Slug == s {
				out = append(out, r)
			}
		}
	}
	return out
}

// scmRouteTask is §11.3's table for one task.
func scmRouteTask(t *DB, p *Project, k *ProjectTask, repo string, ev *scmEvent) {
	own := scmOwnLogin(t, p, k)
	self := ev.Actor.Self || (own != "" && strings.EqualFold(ev.Actor.Login, own))
	pol := policyOf(p.Policy)
	switch ev.Kind {
	case scmKindChecks:
		if ev.Action != "completed" {
			return
		}
		head := scmHead(t, p, k, repo)
		if ev.Ref.SHA == "" || !strings.EqualFold(ev.Ref.SHA, head) {
			return // superseded (or not a commit of the task's)
		}
		scmNudge(t, p, k, repo, scmPollChecks, 0, head, time.Duration(pol.CI.DelaySec)*time.Second)
	case scmKindPull:
		var d struct {
			Pull struct {
				Number int    `json:"number"`
				Head   scmRef `json:"head"`
				Draft  *bool  `json:"draft"`
				URL    string `json:"url"`
			} `json:"pull"`
		}
		_ = json.Unmarshal(ev.Data[scmKindPull], &d.Pull)
		num := orInt(ev.Ref.PR, d.Pull.Number)
		sha := orStr(d.Pull.Head.SHA, ev.Ref.SHA)
		switch ev.Action {
		case "merged", "closed":
			scmPullEnded(t, p, k, repo, num, ev.Action == "merged")
		case "opened", "reopened":
			branch := orStr(d.Pull.Head.Ref, ev.Ref.Branch)
			if self || branch != k.Branch {
				return
			}
			held := false
			for _, pr := range k.taskPRs() {
				held = held || (strings.EqualFold(pr.Repo, repo) && pr.Number == num && pr.State == "open")
			}
			if !held {
				projectRefsCheck(t, p.ID, k.N)
			}
		case "synchronize":
			if sha != "" {
				scmSetHead(t, p, k, repo, num, sha)
			}
		case "ready", "draft":
			scmSetDraft(t, p, k, repo, num, ev.Action == "draft")
		}
	case scmKindPush:
		if ev.Ref.Branch != k.Branch || ev.Ref.SHA == "" {
			return
		}
		scmSetHead(t, p, k, repo, 0, ev.Ref.SHA)
		if self {
			return
		}
		who := scmLogin(ev.Actor.Login)
		text := fmt.Sprintf("[scm: %s pushed %s to %s — not this task]\nSomeone else pushed to %s: pull before pushing.",
			who, sha7(ev.Ref.SHA), k.Branch, k.Branch)
		_, _ = queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcEvent, HoldPark: true,
			Dedupe: "push:" + strconv.FormatInt(k.N, 10) + ":" + strings.ToLower(ev.Ref.SHA)})
		addProjectEvent(t, p.ID, k.N, pevPush, map[string]any{"text": fmt.Sprintf("%s pushed %s to %s", who, sha7(ev.Ref.SHA), k.Branch),
			"sha": ev.Ref.SHA, "repo": repo, "by": who}, false, "push:"+strconv.FormatInt(k.N, 10)+":"+strings.ToLower(ev.Ref.SHA))
	case scmKindReview, scmKindComment:
		if self || ev.Ref.PR == 0 || scmOpenPR(k, repo, ev.Ref.PR) == nil {
			return
		}
		scmNudge(t, p, k, repo, scmPollComments, ev.Ref.PR, "", time.Duration(pol.Reviews.BatchSec)*time.Second)
	}
}

// scmRouteIssue: an issue opened or labeled in a project's repo — the
// projects whose issue subscription it matched (else every active project
// of that repo watching for policy.autoLabel). A quiet event; one with
// autoLabel wakes the coordinator.
func scmRouteIssue(t *DB, ev *scmEvent) {
	if ev.Action != "opened" && ev.Action != "labeled" || ev.Actor.Self {
		return
	}
	var d struct {
		Number int      `json:"number"`
		Title  string   `json:"title"`
		Labels []string `json:"labels"`
		URL    string   `json:"url"`
	}
	_ = json.Unmarshal(ev.Data[scmKindIssue], &d)
	num := orInt(ev.Ref.Issue, d.Number)
	if num == 0 {
		return
	}
	var pids []int64
	for _, s := range ev.Subs {
		if rest, ok := strings.CutPrefix(s, "issues:"); ok {
			if id, err := strconv.ParseInt(strings.SplitN(rest, ":", 2)[0], 10, 64); err == nil {
				pids = append(pids, id)
			}
		}
	}
	if len(pids) == 0 {
		pids = scanIDs(t.q.Query(`SELECT DISTINCT p.id FROM projects p JOIN project_repos r ON r.project_id=p.id
			WHERE p.state='active' AND p.scm=? AND lower(r.repo)=?`, ev.SCM.Provider, strings.ToLower(ev.Repo)))
	}
	seen := map[int64]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		p, err := t.getProject(pid)
		if err != nil || p.State != projActive || p.SCM != ev.SCM.Provider || p.Kind == projTeam {
			continue
		}
		pol := policyOf(p.Policy)
		if _, ok := t.repoNamed(p.ID, ev.Repo); !ok || pol.AutoLabel == "" {
			continue
		}
		wake := false
		for _, l := range d.Labels {
			wake = wake || strings.EqualFold(l, pol.AutoLabel)
		}
		if ev.Action == "labeled" && !wake {
			continue
		}
		text := fmt.Sprintf("issue #%d %s on %s — its title (untrusted): %q", num, ev.Action, ev.Repo, scmLine(d.Title, 120))
		if wake {
			text += fmt.Sprintf(" — labeled %q", pol.AutoLabel)
		}
		addProjectEvent(t, p.ID, 0, pevIssue, map[string]any{"text": text, "repo": ev.Repo, "issue": num,
			"url": scmLine(orStr(d.URL, ev.URL), 300)}, wake, fmt.Sprintf("issue:%s:%d:%s:%t", strings.ToLower(ev.Repo), num, ev.Action, wake))
	}
}

// --- the task's state ---------------------------------------------------------------

// scmOwnLogin is the login task k's credential is (the identity its pushes
// and comments carry; "" unknown).
func scmOwnLogin(t *DB, p *Project, k *ProjectTask) string {
	var login string
	err := t.q.QueryRow(`SELECT login FROM project_creds WHERE project_id=? AND sandbox_ref=? AND login<>'' ORDER BY written_ms DESC LIMIT 1`,
		p.ID, scmCredRef(p, k)).Scan(&login)
	if err == sql.ErrNoRows {
		_ = t.q.QueryRow(`SELECT login FROM project_creds WHERE project_id=? AND login<>'' ORDER BY written_ms DESC LIMIT 1`, p.ID).Scan(&login)
	}
	return login
}

// scmOpenPR is task k's open PR number n (0: any) in repo (nil: none).
func scmOpenPR(k *ProjectTask, repo string, n int) *TaskPR {
	for _, pr := range k.taskPRs() {
		if strings.EqualFold(pr.Repo, repo) && pr.State == "open" && (n == 0 || pr.Number == n) {
			return &pr
		}
	}
	return nil
}

// scmHead is task k's current head in repo: the sha its checks row watches
// (set from its open PR's head, a push, or the branch it pushed), else its
// open PR's head, else its branch as its last refs check saw it.
func scmHead(t *DB, p *Project, k *ProjectTask, repo string) string {
	if it, ok := scmPollItemOf(t, p.ID, k.N, repo, scmPollChecks); ok && it.Ref != "" {
		return it.Ref
	}
	if pr := scmOpenPR(k, repo, 0); pr != nil && pr.HeadSHA != "" {
		return pr.HeadSHA
	}
	return scmPushedSHA(t, p, k, repo)
}

// scmPushedSHA is the task branch's sha on the remote in repo at its last
// refs check ("" none).
func scmPushedSHA(t *DB, p *Project, k *ProjectTask, repo string) string {
	r, ok := t.repoNamed(p.ID, repo)
	if !ok {
		return ""
	}
	for _, c := range t.checkouts(k.ID) {
		if c.Repo == r.Slug {
			return c.RemoteSHA
		}
	}
	return ""
}

// scmSetHead moves task k's head in repo to sha (a push, a synchronize):
// its open PR's head (pr 0: whichever is open) with its checks unknown
// again — and then projectRefsHooks run, as for any head that moved — and
// the sha its checks row watches.
func scmSetHead(t *DB, p *Project, k *ProjectTask, repo string, pr int, sha string) {
	sha = strings.ToLower(sha)
	prs := k.taskPRs()
	changed := false
	for i := range prs {
		if strings.EqualFold(prs[i].Repo, repo) && prs[i].State == "open" && (pr == 0 || prs[i].Number == pr) && !strings.EqualFold(prs[i].HeadSHA, sha) {
			prs[i].HeadSHA, prs[i].Checks, prs[i].ChecksAt = sha, "none", 0
			changed = true
		}
	}
	if !changed {
		if scmOpenPR(k, repo, 0) == nil {
			scmWatchHead(t, p, k, repo, sha) // no PR: the checks row follows the branch
		}
		return
	}
	b, _ := json.Marshal(prs)
	k.PRs = b
	_ = t.setTask(k.ID, map[string]any{"prs": string(b)})
	for _, h := range projectRefsHooks {
		h(t, p, k)
	}
	onTaskChange(t, p, k, "prs")
}

// scmSetDraft records a PR's draft flag (ready for review, or back to
// draft).
func scmSetDraft(t *DB, p *Project, k *ProjectTask, repo string, n int, draft bool) {
	prs := k.taskPRs()
	for i := range prs {
		if strings.EqualFold(prs[i].Repo, repo) && prs[i].Number == n && prs[i].Draft != draft {
			prs[i].Draft = draft
			b, _ := json.Marshal(prs)
			k.PRs = b
			_ = t.setTask(k.ID, map[string]any{"prs": string(b)})
			onTaskChange(t, p, k, "prs")
			return
		}
	}
}

// scmSetChecks records what CI says about task k's PR in repo on sha
// (none | pending | success | failure); false: no open PR there on sha.
func scmSetChecks(t *DB, p *Project, k *ProjectTask, repo, sha, state string) bool {
	prs := k.taskPRs()
	for i := range prs {
		if strings.EqualFold(prs[i].Repo, repo) && prs[i].State == "open" && strings.EqualFold(prs[i].HeadSHA, sha) {
			if prs[i].Checks != state {
				prs[i].Checks, prs[i].ChecksAt = state, scmClock()
				b, _ := json.Marshal(prs)
				k.PRs = b
				_ = t.setTask(k.ID, map[string]any{"prs": string(b)})
				onTaskChange(t, p, k, "prs")
			}
			return true
		}
	}
	return false
}

// scmPullEnded: task k's PR n in repo was merged (or closed). Its row says
// so; with none of its PRs open any more the task's phase is merged (one
// was) or closed, its credentials are scrubbed — its own sandbox's, or the
// project's when no other task of the project is open — and its workspace
// is cleaned up as policy.cleanup says; the coordinator is woken.
func scmPullEnded(t *DB, p *Project, k *ProjectTask, repo string, n int, merged bool) {
	state := "closed"
	if merged {
		state = "merged"
	}
	prs := k.taskPRs()
	found := false
	for i := range prs {
		if strings.EqualFold(prs[i].Repo, repo) && prs[i].Number == n {
			if prs[i].State == state || prs[i].State == "merged" {
				return // known already (a poll and an event, a repeat; a merged PR stays merged)
			}
			prs[i].State = state
			found = true
		}
	}
	if !found {
		return
	}
	open, anyMerged := false, false
	for _, pr := range prs {
		open = open || pr.State == "open"
		anyMerged = anyMerged || pr.State == "merged"
	}
	b, _ := json.Marshal(prs)
	k.PRs = b
	cols := map[string]any{"prs": string(b)}
	what := "prs"
	ended := !open && (k.Phase == phaseOpen || k.Phase == phasePR)
	if ended {
		k.Phase = phaseClosed
		if anyMerged {
			k.Phase = phaseMerged
		}
		cols["phase"], what = k.Phase, "phase"
	}
	_ = t.setTask(k.ID, cols)
	_, _ = t.q.Exec(`DELETE FROM scm_poll WHERE project_id=? AND n=? AND repo=?`, p.ID, k.N, repo)
	kind := pevClosed
	if merged {
		kind = pevMerged
	}
	addProjectEvent(t, p.ID, k.N, kind, map[string]any{"text": fmt.Sprintf("PR #%d on %s was %s", n, repo, state), "repo": repo, "pr": n},
		true, fmt.Sprintf("%s:%d:%s:%d", kind, k.N, strings.ToLower(repo), n))
	if ended {
		scmEndTaskWork(t, p, k, merged)
	}
	onTaskChange(t, p, k, what)
}

// scmEndTaskWork scrubs a finished task's credentials and queues its
// cleanup as policy.cleanup says.
func scmEndTaskWork(t *DB, p *Project, k *ProjectTask, merged bool) {
	switch {
	case k.ForkMade && k.SandboxRef != "":
		_, _ = t.queueJob(p.ID, k.ID, "", pjScrub, "", 0) // its own sandbox's
	default:
		var others int
		_ = t.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND id<>? AND run_id<>0 AND phase IN (?, ?)`,
			p.ID, k.ID, phaseOpen, phasePR).Scan(&others)
		if others == 0 {
			_, _ = t.queueJob(p.ID, 0, "", pjScrub, "", 0) // the project's: no open task needs it
		}
	}
	pol := policyOf(p.Policy)
	if (merged && pol.Cleanup.OnMerge) || (!merged && pol.Cleanup.OnClose) {
		if k.WS != wsPending && k.WS != wsCleaned && k.WS != wsCleaning {
			_, _ = t.queueJob(p.ID, k.ID, "", pjCleanup, "", 0)
		}
	}
}

// --- reviews -------------------------------------------------------------------------

// scmTrusted: whose review words reach a task (policy.reviews).
func scmTrusted(pol PolicyReviews, a scmActor) bool {
	switch pol.Forward {
	case "off":
		return false
	case "all":
		return true
	}
	for _, l := range pol.Allow {
		if strings.EqualFold(l, a.Login) {
			return true
		}
	}
	switch strings.ToUpper(a.Association) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// scmApplyComments takes a PR's timeline as read: each entry once (by its
// id and state) — the task's own and the provider's skipped, an approval or
// a dismissal not forwarded — trusted people's words to the task as one
// input (untrusted text, ≤ 8 KiB) with a quiet review event, anyone else's
// a quiet comment event. It answers the next read's since.
func scmApplyComments(d *DB, r *scmPollRow, pg *scmPage[scmComment]) (since int64) {
	since = r.Item.Since
	for _, c := range pg.Items {
		if c.CreatedAt >= since {
			since = c.CreatedAt + 1
		}
	}
	_ = d.Tx(func(t *DB) error {
		p, k, ok := scmLiveTask(t, r.Project, r.N)
		if !ok || scmOpenPR(k, r.Repo, r.Item.Number) == nil {
			return nil
		}
		pol := policyOf(p.Policy).Reviews
		own := scmOwnLogin(t, p, k)
		var fwd []scmComment
		ns := strconv.FormatInt(k.N, 10)
		for _, c := range pg.Items {
			if c.Author.Self || (own != "" && strings.EqualFold(own, c.Author.Login)) || strings.TrimSpace(c.Body) == "" {
				continue
			}
			if c.Kind == "review" && (c.State == "approved" || c.State == "dismissed") {
				continue
			}
			if !scmSeenOnce(t, scmSemKey(p.ID, k.N, "review", strconv.Itoa(r.Item.Number), c.ID, orStr(c.State, c.Kind))) {
				continue
			}
			if scmTrusted(pol, c.Author) {
				fwd = append(fwd, c)
				continue
			}
			why := "not forwarded"
			if pol.Forward == "off" {
				why = "not forwarded: reviews.forward is off"
			}
			addProjectEvent(t, p.ID, k.N, pevComment, map[string]any{
				"text": fmt.Sprintf("a %s by %s (%s) on PR #%d — %s", orStr(c.Kind, "comment"), scmLogin(c.Author.Login),
					orStr(strings.ToUpper(c.Author.Association), "NONE"), r.Item.Number, why),
				"repo": r.Repo, "pr": r.Item.Number, "by": scmLogin(c.Author.Login)}, false, "comment:"+ns+":"+c.ID)
		}
		if len(fwd) == 0 {
			return nil
		}
		var b strings.Builder
		for _, c := range fwd {
			fmt.Fprintf(&b, "%s (%s)", c.Author.Login, orStr(strings.ToUpper(c.Author.Association), "NONE"))
			switch {
			case c.Kind == "review" && c.State == "changes_requested":
				b.WriteString(" — review, changes requested")
			case c.Kind == "review":
				b.WriteString(" — review")
			case c.Path != "" && c.Line > 0:
				fmt.Fprintf(&b, " on %s:%d", c.Path, c.Line)
			case c.Path != "":
				fmt.Fprintf(&b, " on %s", c.Path)
			}
			b.WriteString(":\n")
			b.WriteString(strings.TrimSpace(c.Body))
			b.WriteString("\n\n")
		}
		what := fmt.Sprintf("review comments on PR #%d (%s)", r.Item.Number, r.Repo)
		text := fmt.Sprintf("[scm: %s — untrusted text]\n%s\nAddress them (or say why not), then push.", what,
			untrusted(p.Host, what, b.String()))
		_, _ = queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcEvent, HoldPark: true,
			Dedupe: fmt.Sprintf("review:%d:%d:%s", k.N, r.Item.Number, fwd[len(fwd)-1].ID)})
		addProjectEvent(t, p.ID, k.N, pevReview, map[string]any{
			"text": fmt.Sprintf("%d review comment(s) on PR #%d forwarded to the task", len(fwd), r.Item.Number),
			"repo": r.Repo, "pr": r.Item.Number}, false, fmt.Sprintf("review:%d:%d:%s", k.N, r.Item.Number, fwd[len(fwd)-1].ID))
		return nil
	})
	return since
}

// --- words ----------------------------------------------------------------------------

// scmLoginChars: what a host login is made of; anything else in one is
// replaced.
var scmLoginChars = regexp.MustCompile(`[^A-Za-z0-9._\[\]-]`)

// scmLogin is a login for our own words ("someone" when there is none).
func scmLogin(s string) string {
	s = clip(scmLoginChars.ReplaceAllString(s, "?"), 60)
	return orStr(s, "someone")
}

// scmLine is one line of untrusted scm text (a title) for an event's
// words: invisible characters out, on one line, the frame's markers
// defused, redacted, clipped.
func scmLine(s string, n int) string {
	s = invisibles.ReplaceAllString(s, "")
	s = strings.Join(strings.Fields(s), " ")
	s = frameMarkers.ReplaceAllString(s, "($1")
	return clip(projRedact(s), n)
}

// sha7 is a commit's short name.
func sha7(s string) string {
	if len(s) > 7 {
		return strings.ToLower(s[:7])
	}
	return strings.ToLower(s)
}
