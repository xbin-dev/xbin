// projects_coord_prs.go — a task's pull requests, for the coordinator and
// for people (API.md §The coordinator, §Big tasks, upgrades and pull
// requests). task_link_pr and POST /projects/{pid}/tasks/{n}/link-pr link a
// pull request of one of the task's repos to it (linkTaskPR): read at once,
// recorded, the task in review — and ended as an event would end it when
// it is over (cleanup then trusts it only for the commits its head holds:
// dirtyIn). Refused: one another live task holds, a head that is the
// default branch or another live task's branch, a second open PR in the
// repo. The task pushes to its head only when it comes from that repo (not
// a fork) and the project's own login opened it (prPushable); else the link
// is Tracked: followed and shown, but its CI failures and review comments
// don't go to the task. task_refresh reads a task's open pull requests again
// now through the poll's own path (scmApplyPull: a merge no webhook brought
// ends the task), asks for a fresh read of their checks, re-tracks reads
// that had stopped (scmRetrackTask), and adopts what its checkouts on other
// branches opened (taskOffCheck). None of it merges, approves, pushes or
// comments; it is the facts the existing state derives from, read again.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, linkPRRoutes)
}

func linkPRRoutes() []routeDef {
	return []routeDef{
		{"POST /projects/{pid}/tasks/{n}/link-pr", needAny, projectNeed(lvParticipant, handleLinkPR)},
	}
}

// coordRefreshMax bounds task_refresh without a task (each may run a
// command in its sandbox); coordOffWait one task's check of its checkouts.
const (
	coordRefreshMax = 10
	coordOffWait    = 45 * time.Second
)

// coordPRSpecs are task_link_pr and task_refresh (coordToolSpecs).
func coordPRSpecs() []toolSpec {
	return []toolSpec{
		{Type: "function", Function: funcDef{Name: "task_link_pr",
			Description: "Link a pull request to one of this project's tasks — one its coding agent opened itself from another branch, say — so the task follows its CI, reviews and merge. " +
				"The task pushes to the pull request's branch only when it comes from that repo (not a fork) and the project's own identity opened it; otherwise the link only tracks it (its CI failures and reviews don't go to the task). " +
				"Refused: a pull request another open task holds, one from the default branch or another task's branch, and a second open one in a repo (a task holds one per repo). " +
				"A merged one may be linked: it ends the task. It reads the pull request at once; it never merges, approves, pushes or comments.",
			Parameters: obj([]string{"task", "pr"}, map[string]any{
				"task": coordIntProp("the task's number"),
				"repo": strProp("one of the task's repos (owner/name; default: the project's only repo)"),
				"pr":   coordIntProp("the pull request's number in repo"),
			})}},
		{Type: "function", Function: funcDef{Name: "task_refresh",
			Description: "Read tasks' pull requests again now and apply what they say — a merge or close no event brought, a moved head, draft or ready — then say each one's state and checks. " +
				"It also links a pull request a task's coding agent opened from another branch, when the task's checkouts can be reached. " +
				"Default: every open or in-review task with pull requests (at most 10); name a task to check one without any. It never merges, approves, pushes or comments.",
			Parameters: obj(nil, map[string]any{
				"task": coordIntProp("a task's number (default: every open or in-review task with pull requests)"),
			})}},
	}
}

// --- linking ------------------------------------------------------------------------------------

// linkResult is what linkTaskPR did.
type linkResult struct {
	PR      TaskPR `json:"pr"`
	Pushes  bool   `json:"pushes"`            // the task pushes to its head
	Why     string `json:"why,omitempty"`     // why the link only tracks it
	Already bool   `json:"already,omitempty"` // the task held it already
}

// linkTaskPR links pull request n of repo (one of task k's repos) to task
// k, by: read now at the provider, refused when another open or in-review
// task holds it; recorded (its head when the task may push there), the
// task in review, the refs hooks told, ended as an event would end it when
// it is over.
func (ag *Agent) linkTaskPR(ctx context.Context, p *Project, k *ProjectTask, repo ProjectRepo, n int, by string) (*linkResult, error) {
	over := func(k *ProjectTask) error {
		if k.RunID == 0 || (k.Phase != phaseOpen && k.Phase != phasePR) {
			return perr(409, "task #%d is %s: it takes no pull request", k.N, orStr(k.Phase, "over"))
		}
		return nil
	}
	if n <= 0 {
		return nil, perr(400, "pr: a pull request's number")
	}
	if err := over(k); err != nil {
		return nil, err
	}
	in := false
	for _, r := range scmTaskRepos(ag.db, p, k) {
		in = in || r.Slug == repo.Slug
	}
	if !in {
		return nil, perr(409, "%s isn't one of task #%d's repos", repo.Repo, k.N)
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, coordScmCall)
	pl, err := api.Pull(cctx, repo.Repo, n, projectAs(p))
	cancel()
	if err != nil {
		return nil, err
	}
	n = orInt(pl.Number, n)
	res := &linkResult{}
	err = ag.db.Tx(func(t *DB) error {
		cur, err := t.taskByID(k.ID)
		if err != nil {
			return err
		}
		if err := over(cur); err != nil {
			return err
		}
		if other := prHeldBy(t, p.ID, cur.ID, repo.Repo, n); other != 0 {
			return &projErr{code: 409, refusal: refusalBusy, msg: fmt.Sprintf("PR #%d on %s is task #%d's", n, repo.Repo, other)}
		}
		for _, h := range cur.taskPRs() {
			if strings.EqualFold(h.Repo, repo.Repo) && h.Number == n {
				res.Already, res.PR = true, h
			}
		}
		res.Pushes, res.Why = prPushable(t, p, cur, repo.Repo, pl)
		if res.Already && res.PR.State != "open" {
			return nil // over already: it stays as it ended
		}
		// its head, when a branch of this repo, may be neither the default
		// branch nor another live task's; and one open PR per repo
		sameRepo := pl.Head.Repo != "" && strings.EqualFold(pl.Head.Repo, repo.Repo)
		if sameRepo {
			if why := prHeadRefusal(t, p, cur, repo, n, pl.Head.Ref); why != "" {
				return &projErr{code: 409, refusal: refusalBusy, msg: why}
			}
		}
		if open := openPRIn(cur.taskPRs(), repo.Repo, n); open != nil && orStr(pl.State, "open") == "open" {
			return &projErr{code: 409, refusal: refusalBusy, msg: fmt.Sprintf("PR #%d is open for task #%d in %s: a task holds one open pull request per repo — close it, or let it merge, first",
				open.Number, cur.N, repo.Repo)}
		}
		m := TaskPR{Repo: repo.Repo, Number: n, URL: pl.URL, State: "open", Draft: pl.Draft, HeadSHA: strings.ToLower(pl.Head.SHA), Checks: "none",
			Tracked: !res.Pushes}
		switch {
		case sameRepo && pl.Head.Ref != "" && pl.Head.Ref != cur.Branch:
			m.Head = pl.Head.Ref
		case !sameRepo && pl.Head.Ref != "":
			m.Head = orStr(pl.Head.Repo, "?") + ":" + pl.Head.Ref // a fork's: never a branch of this repo (scmHeadPR)
		}
		if err := mergeTaskPRs(t, cur, []TaskPR{m}); err != nil {
			return err
		}
		if !res.Already {
			text := fmt.Sprintf("linked PR #%d on %s: ", n, repo.Repo)
			if res.Pushes {
				text += "the task pushes to " + scmLine(orStr(pl.Head.Ref, cur.Branch), 120)
			} else {
				text += "tracked only — " + res.Why
			}
			addProjectEvent(t, p.ID, cur.N, pevPROpened, map[string]any{"text": text, "url": pl.URL, "repo": repo.Repo, "pr": n, "by": by},
				false, fmt.Sprintf("pr.opened:%d:%s:%d", cur.N, repo.Repo, n))
		}
		for _, h := range projectRefsHooks {
			h(t, p, cur)
		}
		onTaskChange(t, p, cur, "prs")
		if pl.State == "merged" || pl.State == "closed" {
			scmPullEnded(t, p, cur, repo.Repo, n, pl.State == "merged")
		}
		for _, h := range cur.taskPRs() {
			if strings.EqualFold(h.Repo, repo.Repo) && h.Number == n {
				res.PR = h
			}
		}
		if res.PR.State == "open" && res.PR.HeadSHA != "" {
			scmNudge(t, p, cur, repo.Repo, scmPollChecks, 0, res.PR.HeadSHA, 0) // its checks read at the next pass
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

// handleLinkPR: POST /projects/{pid}/tasks/{n}/link-pr {repo?, pr} — a
// participant links a pull request to the task (linkTaskPR): 200 {task,
// pr, pushes, why?, already?}.
func handleLinkPR(w http.ResponseWriter, r *http.Request) {
	p, k, ok := taskOfPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Repo string `json:"repo"`
		PR   int    `json:"pr"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch {
	case p.State != projActive:
		xbin.WriteError(w, 409, "this project is "+p.State)
		return
	case p.Kind == projTeam:
		xbin.WriteError(w, 409, "a team project's definition has no tasks")
		return
	}
	rp, err := projAg().coordRepo(p, body.Repo)
	if err != nil {
		xbin.WriteError(w, 400, "repo: "+err.Error())
		return
	}
	res, err := projAg().linkTaskPR(r.Context(), p, k, rp, body.PR, callerOf(r).tag())
	if err != nil {
		writeProjErr(w, err)
		return
	}
	k, _ = projAg().db.taskByN(p.ID, k.N)
	xbin.WriteJSON(w, 200, map[string]any{"task": projAg().db.projTaskView(p, k), "pr": res.PR, "pushes": res.Pushes,
		"why": res.Why, "already": res.Already})
}

// coordLinkPR is task_link_pr.
func (ag *Agent) coordLinkPR(ctx context.Context, p *Project, args map[string]any) (string, error) {
	var a struct {
		Task int64  `json:"task"`
		N    int64  `json:"n"` // the plan's name for it
		Repo string `json:"repo"`
		PR   int    `json:"pr"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	if a.Task == 0 {
		a.Task = a.N
	}
	if a.PR <= 0 {
		return "", errors.New("pr: the pull request's number")
	}
	k, _, err := ag.db.projectTaskOf(p, a.Task)
	if err != nil {
		return "", err
	}
	r, err := ag.coordRepo(p, a.Repo)
	if err != nil {
		return "", err
	}
	if err := ag.coordStillOwner(); err != nil {
		return "", err
	}
	res, err := ag.linkTaskPR(ctx, p, k, r, a.PR, "the coordinator")
	if err != nil {
		return "", errors.New(coordErrWords(coordScmErr(err)))
	}
	var b strings.Builder
	switch {
	case res.Already && res.PR.State != "open":
		fmt.Fprintf(&b, "Task #%d holds PR #%d on %s already; it is %s.", k.N, res.PR.Number, r.Repo, res.PR.State)
	case res.Already:
		fmt.Fprintf(&b, "Task #%d holds PR #%d on %s already.", k.N, res.PR.Number, r.Repo)
	default:
		fmt.Fprintf(&b, "Linked PR #%d on %s to task #%d.", res.PR.Number, r.Repo, k.N)
	}
	if res.Pushes {
		fmt.Fprintf(&b, " The task pushes to its branch, %s.", coordPlain(taskBranchIn(mustTask(ag.db, k), r.Repo)))
	} else {
		fmt.Fprintf(&b, " The link only tracks it — the task doesn't push to it (%s), and its CI failures and review comments don't go to the task.", res.Why)
	}
	b.WriteString("\n" + ag.coordRefreshOne(ctx, p, k.N, false))
	return b.String(), nil
}

// mustTask is k as stored now (k itself when it can't be read).
func mustTask(d *DB, k *ProjectTask) *ProjectTask {
	if cur, err := d.taskByID(k.ID); err == nil {
		return cur
	}
	return k
}

// --- refreshing ------------------------------------------------------------------------------

// coordRefresh is task_refresh.
func (ag *Agent) coordRefresh(ctx context.Context, p *Project, args map[string]any) (string, error) {
	var a struct {
		Task int64 `json:"task"`
		N    int64 `json:"n"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	if a.Task == 0 {
		a.Task = a.N
	}
	if a.Task > 0 {
		if _, _, err := ag.db.projectTaskOf(p, a.Task); err != nil {
			return "", err
		}
		return ag.coordRefreshOne(ctx, p, a.Task, true), nil
	}
	ks, err := ag.db.tasksWhere(`WHERE project_id=? AND run_id<>0 AND phase IN (?, ?) ORDER BY updated_ms DESC, n DESC`, p.ID, phaseOpen, phasePR)
	if err != nil {
		return "", err
	}
	var parts []string
	more := 0
	for _, k := range ks {
		if len(k.taskPRs()) == 0 {
			continue
		}
		if len(parts) == coordRefreshMax {
			more++
			continue
		}
		parts = append(parts, ag.coordRefreshOne(ctx, p, k.N, true))
	}
	if len(parts) == 0 {
		return "no open task has a pull request — name a task (task) to check its checkouts for one its coding agent opened", nil
	}
	if more > 0 {
		parts = append(parts, fmt.Sprintf("(%d more task(s) with pull requests: name each)", more))
	}
	return strings.Join(parts, "\n"), nil
}

// coordRefreshOne refreshes task n (refreshTaskPRs, after adopting what
// its checkouts on other branches opened when off) and says where it
// stands: its state, then a line per pull request.
func (ag *Agent) coordRefreshOne(ctx context.Context, p *Project, n int64, off bool) string {
	k, err := ag.db.taskByN(p.ID, n)
	if err != nil {
		return fmt.Sprintf("#%d: no such task", n)
	}
	var notes []string
	if off && k.RunID != 0 && (k.Phase == phaseOpen || k.Phase == phasePR) {
		cctx, cancel := context.WithTimeout(ctx, coordOffWait)
		adopted, err := taskOffCheck(cctx, p, k)
		cancel()
		switch {
		case err != nil:
			notes = append(notes, "its checkouts weren't checked for other branches: "+clip(coordPlain(err.Error()), 200))
		case adopted > 0:
			notes = append(notes, fmt.Sprintf("linked %d pull request(s) its coding agent opened from another branch", adopted))
		}
		k = mustTask(ag.db, k)
	}
	notes = append(notes, ag.refreshTaskPRs(ctx, p, k)...)
	k = mustTask(ag.db, k)
	v := ag.db.projTaskView(p, k)
	var b strings.Builder
	fmt.Fprintf(&b, "#%d — %s", k.N, orStr(coordStateWords[v.State], v.State))
	prs := k.taskPRs()
	if len(prs) == 0 {
		b.WriteString(" · no pull request")
	}
	for _, pr := range prs {
		fmt.Fprintf(&b, "\n- PR #%d on %s: %s", pr.Number, pr.Repo, pr.State)
		if pr.State == "open" {
			if pr.Draft {
				b.WriteString(", draft")
			}
			fmt.Fprintf(&b, ", checks %s", orStr(pr.Checks, "none"))
		}
		if pr.HeadSHA != "" {
			fmt.Fprintf(&b, " · head %s", sha7(pr.HeadSHA))
		}
		if pr.Head != "" {
			fmt.Fprintf(&b, " on %s", coordPlain(pr.Head))
		}
	}
	for _, n := range notes {
		b.WriteString("\n  " + n)
	}
	return b.String()
}

// refreshTaskPRs reads task k's open pull requests now, each applied as
// the poll applies its read (scmApplyPull: merged or closed ends it, an
// open one's head and draft flag recorded), then re-tracks what stopped
// reading and asks for a fresh read of each open one's checks. Answers
// what couldn't be read.
func (ag *Agent) refreshTaskPRs(ctx context.Context, p *Project, k *ProjectTask) []string {
	var notes []string
	api, apiErr := scmFor(p.SCM)
	for _, pr := range k.taskPRs() {
		if pr.State != "open" || pr.Number == 0 {
			continue
		}
		if apiErr != nil {
			notes = append(notes, fmt.Sprintf("PR #%d on %s wasn't read: %v", pr.Number, pr.Repo, apiErr))
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, coordScmCall)
		pl, err := api.Pull(cctx, pr.Repo, pr.Number, projectAs(p))
		cancel()
		if err != nil {
			notes = append(notes, fmt.Sprintf("PR #%d on %s wasn't read: %s", pr.Number, pr.Repo, coordErrWords(coordScmErr(err))))
			continue
		}
		scmApplyPull(ag.db, &scmPollRow{Project: p.ID, N: k.N, Repo: pr.Repo,
			Item: scmPollRec{scmPollItem: scmPollItem{Kind: scmPollPull, Repo: pr.Repo, Number: pr.Number}}}, pl)
	}
	_ = ag.db.Tx(func(t *DB) error {
		cur, err := t.taskByID(k.ID)
		if err != nil || cur.RunID == 0 || cur.Phase != phasePR || p.State != projActive {
			return nil
		}
		scmRetrackTask(t, p, cur)
		for _, pr := range cur.taskPRs() {
			if pr.State == "open" && pr.HeadSHA != "" {
				scmNudge(t, p, cur, pr.Repo, scmPollChecks, 0, pr.HeadSHA, 0)
			}
		}
		return nil
	})
	return notes
}
