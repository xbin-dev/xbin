// project_pr.go — a task's pull requests (API.md §Big tasks, upgrades and
// pull requests): the pr job pushes the task's branch in every checkout
// with commits its default branch lacks — never the default branch itself,
// never anything but the task's own branch — and opens a pull request for
// each such repo that has no open one (the provider's POST /scm/pulls,
// clientId agent:proj:<pid>:pr:<n>:<slug>, so a repeat answers the same),
// records them in the task (phase pr) and tells the parts that follow its
// branch (projectRefsHooks). POST /runs/{id}/task/pr asks for it by hand;
// policy.autoPR asks for it when a task comes to rest (projectRested), for
// the repos that have no pull request yet.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	projectJobKinds[pjPR] = jobPR
	projectRested = projectRestedPR
	routeTables = append(routeTables, prRoutes)
	rerunMarks[pjPR] = prAgain
}

// prAgain marks a pr job asked for again while it ran (its client id): the
// worker runs it once more, with the newer ask (rerunMarks).
const prAgain = "again"

func prRoutes() []routeDef {
	return []routeDef{
		{"POST /runs/{id}/task/pr", needParticipant, handleTaskPR},
	}
}

// prAsk is what a pr job was asked for (the setting proj_pr:<pid>:<n>; the
// latest ask wins, a person's over auto-PR's).
type prAsk struct {
	Draft bool   `json:"draft"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Auto  bool   `json:"auto,omitempty"` // auto-PR: only repos without a pull request, and only after a turn that ended well
	By    string `json:"by,omitempty"`
}

func prKey(pid, n int64) string { return fmt.Sprintf("proj_pr:%d:%d", pid, n) }

func prAskOf(d *DB, pid, n int64) (prAsk, string) {
	raw := d.getSetting(prKey(pid, n))
	var a prAsk
	if raw == "" || json.Unmarshal([]byte(raw), &a) != nil {
		return prAsk{Auto: true}, raw
	}
	return a, raw
}

// queuePR records a and queues task k's pr job (the live one, if any, is
// answered; one running now runs once more, reading the newest ask).
func queuePR(t *DB, p *Project, k *ProjectTask, a prAsk) (*ProjectJob, error) {
	b, _ := json.Marshal(a)
	if err := t.putSetting(prKey(p.ID, k.N), string(b)); err != nil {
		return nil, err
	}
	j, err := t.queueJob(p.ID, k.ID, "", pjPR, a.By, 0)
	if err == nil && j.State == pjRunning {
		_, err = t.q.Exec(`UPDATE project_jobs SET client_id=? WHERE id=?`, prAgain, j.ID)
	}
	return j, err
}

// handleTaskPR: POST /runs/{id}/task/pr {draft?, title?, body?} — push the
// task's branch and open its pull requests (202 {job}; they arrive in
// TaskView.prs).
func handleTaskPR(w http.ResponseWriter, r *http.Request) {
	p, k, ok := actTask(w, r)
	if !ok {
		return
	}
	var o ActOpts
	if !decodeBody(w, r, &o) {
		return
	}
	switch {
	case p.State != projActive:
		xbin.WriteError(w, 409, "this project is "+p.State)
		return
	case k.Phase != phaseOpen && k.Phase != phasePR:
		xbin.WriteError(w, 409, "this task is "+k.Phase+": it opens no pull request")
		return
	case k.WS != wsReady:
		xbin.WriteError(w, 409, "this task's workspace isn't ready ("+k.WS+")")
		return
	case len(o.Title) > 256:
		xbin.WriteError(w, 400, "title: at most 256 characters")
		return
	case len(o.Body) > 32<<10:
		xbin.WriteError(w, 400, "body: at most 32 KiB")
		return
	}
	a := prAsk{Draft: policyOf(p.Policy).AutoPR == "draft", Title: strings.TrimSpace(o.Title), Body: strings.TrimSpace(o.Body), By: callerOf(r).tag()}
	if o.Draft != nil {
		a.Draft = *o.Draft
	}
	var job *ProjectJob
	err := projAg().db.Tx(func(t *DB) error {
		var err error
		job, err = queuePR(t, p, k, a)
		emitProject(t, p.ID, "job", 0)
		return err
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, 202, map[string]any{"job": job})
}

// projectRestedPR (projectRested, in the transaction that ended k's turn):
// with policy.autoPR, a task whose turn ended well (idle or done — not
// cancelled, not failed) and that has a repo without an open pull request
// gets a pr job — unless a person's ask waits already.
func projectRestedPR(t *DB, p *Project, k *ProjectTask) {
	pol := policyOf(p.Policy)
	if pol.AutoPR != "draft" && pol.AutoPR != "ready" || p.State != projActive || k.WS != wsReady ||
		(k.Phase != phaseOpen && k.Phase != phasePR) || k.RunID == 0 {
		return
	}
	var status string
	if t.q.QueryRow(`SELECT status FROM runs WHERE id=?`, k.RunID).Scan(&status) != nil || (status != statusIdle && status != statusDone) {
		return
	}
	open := map[string]bool{}
	for _, pr := range k.taskPRs() {
		if pr.State == "open" {
			open[strings.ToLower(pr.Repo)] = true
		}
	}
	missing := false
	for _, slug := range k.Repos {
		if r, err := t.projectRepo(p.ID, slug); err == nil && !open[strings.ToLower(r.Repo)] {
			missing = true
		}
	}
	if !missing {
		return
	}
	if a, raw := prAskOf(t, p.ID, k.N); raw != "" && !a.Auto && t.liveJob(p.ID, k.ID, "", pjPR) != nil {
		return // a person's ask waits: it does more than this would
	}
	if _, err := queuePR(t, p, k, prAsk{Draft: pol.AutoPR == "draft", Auto: true}); err != nil {
		logf("project %d task %d: queueing its pull request: %v", p.ID, k.N, err)
	}
}

// prScript pushes the task's branch BR from each checkout C_i whose
// PUSH_i is 1 and that is on BR with commits origin/DEF_i lacks, and
// prints what it found: AHEAD|PUSHED <i> <commits> <head>, or OFF <i>
// <branch> for a checkout on another branch.
const prScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "C=\${C_$i}; DEF=\${DEF_$i}; PUSH=\${PUSH_$i}"
  if [ -e "$C/.git" ]; then
    cur=$(git -C "$C" symbolic-ref -q --short HEAD || true)
    if [ "$cur" != "$BR" ]; then
      printf 'OFF %s %s\n' "$i" "${cur:--}"
    else
      ahead=$(git -C "$C" rev-list --count "refs/remotes/origin/$DEF..HEAD" 2>/dev/null || echo 0)
      head=$(git -C "$C" rev-parse HEAD)
      if [ "$ahead" -gt 0 ] && [ "$PUSH" = 1 ]; then
        git -C "$C" push -q origin "refs/heads/$BR:refs/heads/$BR"
        printf 'PUSHED %s %s %s\n' "$i" "$ahead" "$head"
      else
        printf 'AHEAD %s %s %s\n' "$i" "$ahead" "$head"
      fi
    fi
  fi
  i=$((i+1))
done
`

// prTransient: a pull request's create failed in a way a later try may
// not — the provider or its host unreachable, busy or rate-limited — not
// a refusal of the request itself.
func prTransient(err error) bool {
	var se *scmError
	if !errors.As(err, &se) {
		return true
	}
	switch se.Refusal {
	case scmRefLimit, scmRefUnavailable, scmRefUpstream:
		return true
	}
	return false
}

// prBody is a pull request's body when none was given: which task it is
// for, and the issue it closes when it is in that repo. Nothing of the
// conversation goes out.
func prBody(p *Project, k *ProjectTask, repo string) string {
	b := fmt.Sprintf("Opened for task %d of the project %s: %s.", k.N, p.Name, k.Title)
	if k.Issue != nil && k.Issue.Number > 0 {
		if strings.EqualFold(k.Issue.Repo, repo) {
			b += fmt.Sprintf("\n\nCloses #%d", k.Issue.Number)
		} else {
			b += fmt.Sprintf("\n\nFor %s#%d", k.Issue.Repo, k.Issue.Number)
		}
	}
	return b
}

// jobPR pushes the task's branch and opens its pull requests (prScript,
// then POST /scm/pulls per repo), and records them.
func jobPR(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	d := projAg().db
	if k == nil || k.RunID == 0 || (k.Phase != phaseOpen && k.Phase != phasePR) {
		return doneJob("the task is over")
	}
	if k.WS != wsReady {
		return doneJob("no workspace to push from")
	}
	if j.ClientID == prAgain {
		// this run reads the newest ask: an ask from here on marks it again
		// (rerunMarks), so it runs once more after this one
		j.ClientID = ""
		if _, err := d.q.Exec(`UPDATE project_jobs SET client_id='' WHERE id=? AND client_id=?`, j.ID, prAgain); err != nil {
			return jobOutcome{}, err
		}
	}
	ask, raw := prAskOf(d, p.ID, k.N)
	repos, err := taskRepos(p, k)
	if err != nil {
		return jobOutcome{}, err
	}
	bySlug := map[string]ProjectRepo{}
	for _, r := range repos {
		bySlug[r.Slug] = r
	}
	open := map[string]bool{}
	for _, pr := range k.taskPRs() {
		if pr.State == "open" {
			open[strings.ToLower(pr.Repo)] = true
		}
	}
	var cos []ProjectCheckout
	var notes []string
	for _, c := range d.checkouts(k.ID) {
		r, ok := bySlug[c.Repo]
		switch {
		case !ok || c.State == "removed" || c.State == "pending":
		case r.DefaultBranch == "" || r.DefaultBranch == k.Branch:
			notes = append(notes, fmt.Sprintf("%s: the task works on its default branch, which it never pushes", r.Repo))
		case ask.Auto && open[strings.ToLower(r.Repo)]:
		default:
			cos = append(cos, c)
		}
	}
	if len(cos) == 0 {
		if len(notes) > 0 {
			return jobOutcome{}, jobFail("%s", strings.Join(notes, "; "))
		}
		return doneJob("nothing to push")
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if err != nil {
		return jobOutcome{}, err
	}
	if signin, err := stepCreds(ctx, p, k, taskRef(p, k)); signin {
		if nowMs()-j.Created > signinWait.Milliseconds() {
			return jobOutcome{}, jobFail("the sign-in to %s wasn't finished: sign in, then open the pull request again", orStr(p.Host, "the scm provider"))
		}
		return waitJob(10000, "waiting for a sign-in")
	} else if err != nil {
		return jobOutcome{}, err
	}
	env := map[string]string{"N": strconv.Itoa(len(cos)), "BR": k.Branch, "GIT_TERMINAL_PROMPT": "0"}
	for i, c := range cos {
		env[fmt.Sprintf("C_%d", i)] = c.Path
		env[fmt.Sprintf("DEF_%d", i)] = bySlug[c.Repo].DefaultBranch
		env[fmt.Sprintf("PUSH_%d", i)] = "1"
	}
	out, err := s.must(ctx, "pushing the task's branch", prScript, env, "", 5*time.Minute)
	j.Out = clip(projRedact(out), 8<<10)
	if err != nil {
		return jobOutcome{}, err
	}
	type found struct {
		ahead  int
		head   string
		pushed bool
	}
	got := map[int]found{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) >= 3 && f[0] == "OFF" {
			if i, err := strconv.Atoi(f[1]); err == nil && i < len(cos) {
				notes = append(notes, fmt.Sprintf("%s: its checkout is on %s, not the task's branch", bySlug[cos[i].Repo].Repo, clip(f[2], 100)))
			}
		}
		if len(f) != 4 || (f[0] != "AHEAD" && f[0] != "PUSHED") {
			continue
		}
		i, err1 := strconv.Atoi(f[1])
		n, err2 := strconv.Atoi(f[2])
		if err1 == nil && err2 == nil && i < len(cos) {
			got[i] = found{ahead: n, head: f[3], pushed: f[0] == "PUSHED"}
		}
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return jobOutcome{}, err
	}
	title := clip(orStr(ask.Title, k.Title), 256)
	var made []TaskPR
	var retry error               // a create that may work if tried again (the push and the create repeat safely)
	kept := false                 // a create still failing at the last try: the ask stays
	pushed := map[string]string{} // slug → the head pushed
	for i, c := range cos {
		g, ok := got[i]
		if !ok || g.ahead == 0 {
			continue
		}
		r := bySlug[c.Repo]
		if g.pushed {
			pushed[c.Repo] = g.head
		}
		if open[strings.ToLower(r.Repo)] {
			continue // its pull request is open: the push updated it
		}
		body := projRedact(orStr(ask.Body, prBody(p, k, r.Repo)))
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		pl, err := api.PullCreate(cctx, scmPullReq{Repo: r.Repo, Head: k.Branch, Base: r.DefaultBranch, Title: title, Body: body,
			Draft: ask.Draft, ClientID: fmt.Sprintf("agent:proj:%d:pr:%d:%s", p.ID, k.N, r.Slug), As: projectAs(p)})
		cancel()
		if err != nil {
			if scmRefused(err, scmRefSignin) {
				return waitJob(10000, "waiting for a sign-in")
			}
			if made == nil && len(pushed) == 0 {
				return jobOutcome{}, err
			}
			if prTransient(err) {
				if j.Attempts+1 < projJobMaxAttempts {
					if retry == nil {
						retry = fmt.Errorf("opening the pull request on %s: %w", r.Repo, err)
					}
					continue // what was done is recorded; the job runs again
				}
				// the last try: the job ends done with a note, never failed
				// (a failed job fails the task's workspace); the ask stays,
				// so the next ask or turn tries again
				kept = true
				notes = append(notes, fmt.Sprintf("%s: %v (tried %d times; ask again to retry)", r.Repo, err, projJobMaxAttempts))
				continue
			}
			notes = append(notes, fmt.Sprintf("%s: %v", r.Repo, err))
			continue
		}
		made = append(made, TaskPR{Repo: r.Repo, Number: pl.Number, URL: pl.URL, State: orStr(pl.State, "open"), Draft: pl.Draft,
			HeadSHA: orStr(pl.Head.SHA, g.head), Checks: "none"})
	}
	err = d.Tx(func(t *DB) error {
		cur, err := t.taskByID(k.ID)
		if err != nil {
			return err
		}
		for slug, sha := range pushed {
			if _, err := t.q.Exec(`UPDATE project_checkouts SET remote_sha=? WHERE task_id=? AND repo_slug=?`, sha, k.ID, slug); err != nil {
				return err
			}
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": fmt.Sprintf("pushed %s to %s", shortSHA(sha), k.Branch),
				"sha": sha, "repo": bySlug[slug].Repo}, false, "")
		}
		if len(made) > 0 {
			prs := cur.taskPRs()
			for _, m := range made {
				replaced := false
				for x := range prs {
					if strings.EqualFold(prs[x].Repo, m.Repo) && prs[x].Number == m.Number {
						m.Checks, m.ChecksAt = orStr(prs[x].Checks, "none"), prs[x].ChecksAt
						prs[x], replaced = m, true
					}
				}
				if !replaced {
					prs = append(prs, m)
				}
				addProjectEvent(t, p.ID, k.N, pevPROpened, map[string]any{"text": fmt.Sprintf("opened PR #%d on %s", m.Number, m.Repo),
					"url": m.URL, "repo": m.Repo, "pr": m.Number}, false, fmt.Sprintf("pr.opened:%d:%s:%d", k.N, m.Repo, m.Number))
			}
			b, _ := json.Marshal(prs)
			cols := map[string]any{"prs": string(b)}
			cur.PRs = b
			if cur.Phase == phaseOpen {
				cols["phase"], cur.Phase = phasePR, phasePR
			}
			if err := t.setTask(k.ID, cols); err != nil {
				return err
			}
		}
		if len(made) > 0 || len(pushed) > 0 {
			for _, h := range projectRefsHooks {
				h(t, p, cur)
			}
			onTaskChange(t, p, cur, "prs")
		}
		if len(notes) > 0 {
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": clip("pull request: "+strings.Join(notes, "; "), 1000)}, false, "")
		}
		if retry == nil && !kept && t.getSetting(prKey(p.ID, k.N)) == raw { // asked again meanwhile, or tried again: the ask stays
			_, _ = t.q.Exec(`DELETE FROM settings WHERE k=?`, prKey(p.ID, k.N))
		}
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	if retry != nil {
		return jobOutcome{}, retry
	}
	switch {
	case len(made) > 0:
		return doneJob(fmt.Sprintf("opened %d pull request(s)", len(made)))
	case kept:
		return doneJob("pushed the task's branch; its pull request wasn't opened")
	case len(pushed) > 0:
		return doneJob("pushed the task's branch")
	}
	return doneJob("nothing to push")
}
