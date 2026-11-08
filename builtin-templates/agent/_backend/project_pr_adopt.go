// project_pr_adopt.go — a task's branch per repo, and pull requests that
// came to a task another way than its pr job (API.md §Big tasks, upgrades
// and pull requests, "Pull requests"). A task's branch in a repo is the
// head of its open pull request there that it pushes to (TaskPR.Head, not
// Tracked), else its own (taskBranchIn): the pr and refs jobs push and read
// that one, and the scm routing follows both. A checkout prScript or
// refsScript finds on another branch is looked up at the provider: the one
// OPEN pull request from that branch, in the same repo, opened by the
// project's own login, its head in the checkout's history and not already
// on the default branch, while the task has no other open PR in that repo
// and no other live task holds the PR or its branch — that one is adopted,
// the task in review; anything else gets a note saying why and that
// task_link_pr links one (linkTaskPR: the coordinator's tool and the
// people's route). Adopting never ends a task: a merged or closed PR is
// only named. A task holds at most one open PR per repo (its poll rows are
// per repo).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// taskBranchIn is task k's branch in repo (owner/name): the head of its
// open pull request there that it pushes to (not Tracked), else its own
// branch. Everything that matches a task's branch asks this.
func taskBranchIn(k *ProjectTask, repo string) string {
	for _, pr := range k.taskPRs() {
		if pr.Head != "" && pr.State == "open" && !pr.Tracked && strings.EqualFold(pr.Repo, repo) {
			return pr.Head
		}
	}
	return k.Branch
}

// taskOnBranch: branch is one of task k's in repo — its own, or the head of
// its open pull request there that it pushes to (a task linked to two
// branches follows both).
func taskOnBranch(k *ProjectTask, repo, branch string) bool {
	if branch == "" {
		return false
	}
	return branch == k.Branch || branch == taskBranchIn(k, repo)
}

// taskBranches are task k's branches in repo: its own, then a linked head.
func taskBranches(k *ProjectTask, repo string) []string {
	if b := taskBranchIn(k, repo); b != k.Branch {
		return []string{k.Branch, b}
	}
	return []string{k.Branch}
}

// openPRIn is the open pull request of prs in repo other than number
// except (nil: none) — a task holds at most one per repo.
func openPRIn(prs []TaskPR, repo string, except int) *TaskPR {
	for i, pr := range prs {
		if pr.State == "open" && pr.Number != except && strings.EqualFold(pr.Repo, repo) {
			return &prs[i]
		}
	}
	return nil
}

// mergeTaskPRs folds add into task k's pull requests — one it holds keeps
// its checks, and its head unless add names one — and writes them; the
// task goes to review (phase pr) when it was open and one added is open.
// k is updated to match.
func mergeTaskPRs(t *DB, k *ProjectTask, add []TaskPR) error {
	prs := k.taskPRs()
	open := false
	for _, m := range add {
		open = open || m.State == "open"
		replaced := false
		for x := range prs {
			if strings.EqualFold(prs[x].Repo, m.Repo) && prs[x].Number == m.Number {
				m.Checks, m.ChecksAt = orStr(prs[x].Checks, "none"), prs[x].ChecksAt
				m.Head = orStr(m.Head, prs[x].Head)
				prs[x], replaced = m, true
			}
		}
		if !replaced {
			prs = append(prs, m)
		}
	}
	b, _ := json.Marshal(prs)
	cols := map[string]any{"prs": string(b)}
	k.PRs = b
	if open && k.Phase == phaseOpen {
		cols["phase"], k.Phase = phasePR, phasePR
	}
	return t.setTask(k.ID, cols)
}

// prHeldBy is the number of the other task of project pid, open or in
// review, that holds pull request n of repo (0: none).
func prHeldBy(t *DB, pid, exceptID int64, repo string, n int) int64 {
	ks, _ := t.tasksWhere(`WHERE project_id=? AND id<>? AND run_id<>0 AND phase IN (?, ?) AND prs LIKE ?`,
		pid, exceptID, phaseOpen, phasePR, fmt.Sprintf(`%%"number":%d,%%`, n))
	for _, o := range ks {
		for _, pr := range o.taskPRs() {
			if strings.EqualFold(pr.Repo, repo) && pr.Number == n {
				return o.N
			}
		}
	}
	return 0
}

// branchHeldBy is the number of the other live task (open or in review) of
// project pid whose branch in repo branch is — its own, or a linked head it
// pushes to (0: none).
func branchHeldBy(t *DB, pid, exceptID int64, repo, branch string) int64 {
	ks, _ := t.tasksWhere(`WHERE project_id=? AND id<>? AND run_id<>0 AND phase IN (?, ?)`, pid, exceptID, phaseOpen, phasePR)
	for _, o := range ks {
		if taskOnBranch(o, repo, branch) {
			return o.N
		}
	}
	return 0
}

// prHeadRefusal says why pull request n's head branch in repo r can't be
// task k's ("" it can): the repo's default branch, or another live task's
// branch. Asked inside the transaction that records it.
func prHeadRefusal(t *DB, p *Project, k *ProjectTask, r ProjectRepo, n int, branch string) string {
	switch {
	case branch != "" && branch == r.DefaultBranch:
		return fmt.Sprintf("PR #%d's head is %s's default branch (%s), which a task never takes", n, r.Repo, branch)
	case branch != "" && branch != k.Branch:
		if other := branchHeldBy(t, p.ID, k.ID, r.Repo, branch); other != 0 {
			return fmt.Sprintf("PR #%d's head %s is task #%d's branch", n, scmLine(branch, 120), other)
		}
	}
	return ""
}

// prPushable says whether task k may push to pull request pl's head in
// repo: the PR comes from repo itself (not a fork) and the project's own
// login opened it. why says what it lacks.
func prPushable(t *DB, p *Project, k *ProjectTask, repo string, pl *scmPull) (bool, string) {
	own := scmOwnLogin(t, p, k)
	switch {
	case pl.Head.Repo == "":
		return false, "its head repository is unknown (a fork deleted?)"
	case !strings.EqualFold(pl.Head.Repo, repo):
		return false, fmt.Sprintf("it comes from the fork %s", scmLine(pl.Head.Repo, 120))
	case own == "":
		return false, "the project's own login isn't known yet (no credential was written)"
	case !strings.EqualFold(pl.Author.Login, own):
		return false, fmt.Sprintf("%s opened it, not the project's own login (%s)", scmLogin(pl.Author.Login), scmLogin(own))
	}
	return true, ""
}

// --- adopting what the coding agent opened ----------------------------------------------------

// prOff is a checkout found on a branch that isn't the task's there:
// prScript's or refsScript's OFF <i> <branch> <head>.
type prOff struct {
	co           ProjectCheckout
	repo         ProjectRepo
	branch, head string
}

// prAdopt is what an off checkout's branch came to: its pull request to
// adopt (pull), else why not — wake false for what nothing can be linked
// for (not on a branch) or may pass (the provider unreachable).
type prAdopt struct {
	prOff
	pull *scmPull
	why  string
	code string // the reason's word, for the note's dedupe
	wake bool
}

func (a *prAdopt) refuse(why, code string, wake bool) {
	a.pull, a.why, a.code, a.wake = nil, why, code, wake
}

// prOffLines are out's OFF lines, for cos (the script's checkouts, by its
// index).
func prOffLines(out string, cos []ProjectCheckout, bySlug map[string]ProjectRepo) []prOff {
	var offs []prOff
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) < 3 || f[0] != "OFF" {
			continue
		}
		i, err := strconv.Atoi(f[1])
		if err != nil || i < 0 || i >= len(cos) {
			continue
		}
		o := prOff{co: cos[i], repo: bySlug[cos[i].Repo], branch: clip(f[2], 255)}
		if len(f) >= 4 && f[3] != "-" {
			o.head = strings.ToLower(f[3])
		}
		offs = append(offs, o)
	}
	return offs
}

// shaShape is a commit id as a provider names one (the history checks pass
// it to git, so nothing else goes).
var shaShape = regexp.MustCompile(`^[0-9a-fA-F]{7,64}$`)

// headScript prints, for each checkout C_i and commit S_i, whether S_i is
// in its HEAD's history and whether its default branch on the remote
// (origin/DEF_i) holds it already: ANC <i> 1|0 1|0.
const headScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "C=\${C_$i}; S=\${S_$i}; DEF=\${DEF_$i}"
  a=0; b=0
  if git -C "$C" merge-base --is-ancestor "$S" HEAD 2>/dev/null; then a=1; fi
  if git -C "$C" merge-base --is-ancestor "$S" "refs/remotes/origin/$DEF" 2>/dev/null; then b=1; fi
  printf 'ANC %s %s %s\n' "$i" "$a" "$b"
  i=$((i+1))
done
`

// prAdoptCheck looks each off checkout's branch up at the provider (every
// pull request from it, any state) and decides (prAdoptFind), then reads
// each candidate's head in the checkout (s: the sandbox that holds it): its
// HEAD or in its history, and not on the default branch already.
func prAdoptCheck(ctx context.Context, d *DB, p *Project, k *ProjectTask, s *wsbx, api scmAPI, offs []prOff) []prAdopt {
	out := make([]prAdopt, len(offs))
	var need []int // adopted but for the history check
	for i, o := range offs {
		a := prAdopt{prOff: o, wake: true}
		switch {
		case o.branch == "" || o.branch == "-":
			a.refuse("it isn't on a branch (a detached HEAD)", "detached", false)
		case o.branch == o.repo.DefaultBranch:
			a.refuse("that is the repo's default branch", "default", true)
		default:
			a.pull, a.why, a.code, a.wake = prAdoptFind(ctx, d, p, k, api, o)
			if a.pull != nil {
				need = append(need, i)
			}
		}
		out[i] = a
	}
	if len(need) == 0 {
		return out
	}
	env := map[string]string{"N": strconv.Itoa(len(need))}
	for j, i := range need {
		env[fmt.Sprintf("C_%d", j)], env[fmt.Sprintf("S_%d", j)] = out[i].co.Path, out[i].pull.Head.SHA
		env[fmt.Sprintf("DEF_%d", j)] = out[i].repo.DefaultBranch
	}
	res, err := s.must(ctx, "reading the checkout's history", headScript, env, "", time.Minute)
	inHead, onDef := map[int]bool{}, map[int]bool{}
	for _, l := range strings.Split(res, "\n") {
		if f := strings.Fields(l); len(f) == 4 && f[0] == "ANC" {
			if j, err := strconv.Atoi(f[1]); err == nil {
				inHead[j], onDef[j] = f[2] == "1", f[3] == "1"
			}
		}
	}
	for j, i := range need {
		pl := out[i].pull
		switch {
		case err != nil:
			out[i].refuse("its history couldn't be read: "+clip(err.Error(), 200), "read", false)
		case !inHead[j]:
			out[i].refuse(fmt.Sprintf("PR #%d's head %s isn't in the checkout's history", pl.Number, sha7(pl.Head.SHA)), "history", true)
		case onDef[j]:
			out[i].refuse(fmt.Sprintf("PR #%d's head %s is on the default branch already: nothing of the task's is in it", pl.Number, sha7(pl.Head.SHA)), "on-default", true)
		}
	}
	return out
}

// prAdoptFind is the pull request off's branch may adopt (nil: why not):
// the one open PR from it — a merged or closed one only named — from the
// repo itself, by the project's own login, held by no other live task,
// while the task has no other open PR in that repo.
func prAdoptFind(ctx context.Context, d *DB, p *Project, k *ProjectTask, api scmAPI, o prOff) (*scmPull, string, string, bool) {
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	page, err := api.Pulls(cctx, scmQuery{Repo: o.repo.Repo, Head: o.branch, State: "all", As: projectAs(p)})
	cancel()
	if err != nil {
		return nil, "its pull requests couldn't be read: " + clip(err.Error(), 200), "read", false
	}
	var open []scmPull
	var over []string
	for _, pl := range page.Items {
		if pl.Head.Ref != "" && pl.Head.Ref != o.branch {
			continue
		}
		if orStr(pl.State, "open") == "open" {
			open = append(open, pl)
		} else {
			over = append(over, fmt.Sprintf("PR #%d (%s)", pl.Number, pl.State))
		}
	}
	switch {
	case len(open) == 0 && len(over) > 0:
		// adopting never ends a task: a person or the coordinator links it if it is the task's
		return nil, fmt.Sprintf("only %s comes from it, and an open one alone is adopted", strings.Join(over, ", ")), "over", true
	case len(open) == 0:
		return nil, "no pull request comes from it", "none", true
	case len(open) > 1:
		return nil, fmt.Sprintf("%d open pull requests come from it", len(open)), "many", true
	}
	pl := open[0]
	if ok, why := prPushable(d, p, k, o.repo.Repo, &pl); !ok {
		return nil, fmt.Sprintf("PR #%d: %s", pl.Number, why), "pushable", true
	}
	if pl.Head.SHA == "" || !shaShape.MatchString(pl.Head.SHA) {
		return nil, fmt.Sprintf("PR #%d has no head commit the agent can check", pl.Number), "history", true
	}
	if why, code := prAdoptHeld(d, p, k, o, pl.Number); why != "" {
		return nil, why, code, true
	}
	return &pl, "", "", true
}

// prAdoptHeld says why task k can't take PR n from off's branch now ("" it
// can): another live task holds the PR or its branch, or k has another
// open PR in that repo. Asked again inside the transaction that records it.
func prAdoptHeld(t *DB, p *Project, k *ProjectTask, o prOff, n int) (string, string) {
	if other := prHeldBy(t, p.ID, k.ID, o.repo.Repo, n); other != 0 {
		return fmt.Sprintf("PR #%d is task #%d's", n, other), "held"
	}
	if why := prHeadRefusal(t, p, k, o.repo, n, o.branch); why != "" {
		return why, "held"
	}
	if open := openPRIn(k.taskPRs(), o.repo.Repo, n); open != nil {
		return fmt.Sprintf("PR #%d is open for this task in %s: a task holds one open pull request per repo — close it, or let it merge, first",
			open.Number, o.repo.Repo), "second"
	}
	return "", ""
}

// prAdoptApply records ads in t for task k (as stored now): each pull
// request adopted — checked again here, as stored — with its head and a
// pr.opened event saying so, the task in review, the refs hooks told; each
// refusal as a note, once per branch and reason. Answers how many were
// adopted.
func prAdoptApply(t *DB, p *Project, k *ProjectTask, ads []prAdopt) (int, error) {
	var add []TaskPR
	for _, a := range ads {
		if a.pull != nil && orStr(a.pull.State, "open") != "open" { // adopting never ends a task
			a.refuse(fmt.Sprintf("PR #%d is %s, and an open one alone is adopted", a.pull.Number, a.pull.State), "over", true)
		}
		if a.pull != nil {
			if why, code := prAdoptHeld(t, p, k, a.prOff, a.pull.Number); why != "" {
				a.refuse(why, code, true)
			} else if held := openPRIn(k.taskPRs(), a.repo.Repo, 0); held != nil && held.Number == a.pull.Number && held.Head == a.branch && !held.Tracked {
				continue // adopted already
			}
		}
		if a.pull == nil {
			if a.why == "" {
				continue
			}
			text := fmt.Sprintf("pull request: %s's checkout is on %s, not the task's branch %s: %s — the coordinator (task_link_pr) or a person can link the task's pull request",
				a.repo.Repo, scmLine(a.branch, 120), taskBranchIn(k, a.repo.Repo), a.why)
			// once per branch and reason: the refs job looks after every turn
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": clip(text, 1000), "repo": a.repo.Repo, "branch": a.branch}, a.wake,
				fmt.Sprintf("off:%d:%s:%s:%s", k.N, strings.ToLower(a.repo.Repo), a.branch, a.code))
			continue
		}
		pl := a.pull
		m := TaskPR{Repo: a.repo.Repo, Number: pl.Number, URL: pl.URL, State: "open", Draft: pl.Draft,
			HeadSHA: strings.ToLower(pl.Head.SHA), Checks: "none", Head: a.branch}
		if err := mergeTaskPRs(t, k, []TaskPR{m}); err != nil { // one at a time: the next one's checks see it
			return 0, err
		}
		add = append(add, m)
		addProjectEvent(t, p.ID, k.N, pevPROpened, map[string]any{
			"text": fmt.Sprintf("linked PR #%d on %s: the coding agent opened it from %s", pl.Number, a.repo.Repo, scmLine(a.branch, 120)),
			"url":  pl.URL, "repo": a.repo.Repo, "pr": pl.Number, "head": a.branch}, false,
			fmt.Sprintf("pr.opened:%d:%s:%d", k.N, a.repo.Repo, pl.Number))
	}
	if len(add) == 0 {
		return 0, nil
	}
	for _, h := range projectRefsHooks {
		h(t, p, k)
	}
	onTaskChange(t, p, k, "prs")
	return len(add), nil
}

// prAdoptRun checks offs and records what came of it, in its own
// transaction (after the job's own: what it wrote is read again).
func prAdoptRun(ctx context.Context, p *Project, k *ProjectTask, s *wsbx, api scmAPI, offs []prOff) (int, error) {
	if len(offs) == 0 {
		return 0, nil
	}
	d := projAg().db
	ads := prAdoptCheck(ctx, d, p, k, s, api, offs)
	n := 0
	err := d.Tx(func(t *DB) error {
		cur, err := t.taskByID(k.ID)
		if err != nil || cur.RunID == 0 || (cur.Phase != phaseOpen && cur.Phase != phasePR) {
			return err
		}
		n, err = prAdoptApply(t, p, cur, ads)
		return err
	})
	return n, err
}

// taskOffCheck runs prScript without pushing over task k's checkouts and
// adopts what it finds on other branches (task_refresh). Answers how many
// were adopted.
func taskOffCheck(ctx context.Context, p *Project, k *ProjectTask) (int, error) {
	if k.WS != wsReady {
		return 0, fmt.Errorf("its workspace isn't ready (%s)", k.WS)
	}
	repos, err := taskRepos(p, k)
	if err != nil {
		return 0, err
	}
	bySlug := map[string]ProjectRepo{}
	for _, r := range repos {
		bySlug[r.Slug] = r
	}
	var cos []ProjectCheckout
	for _, c := range projAg().db.checkouts(k.ID) {
		if _, ok := bySlug[c.Repo]; ok && c.State != "removed" && c.State != "pending" {
			cos = append(cos, c)
		}
	}
	if len(cos) == 0 {
		return 0, nil
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return 0, err
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if err != nil {
		return 0, err
	}
	env := map[string]string{"N": strconv.Itoa(len(cos)), "GIT_TERMINAL_PROMPT": "0"}
	for i, c := range cos {
		r := bySlug[c.Repo]
		env[fmt.Sprintf("C_%d", i)], env[fmt.Sprintf("DEF_%d", i)] = c.Path, r.DefaultBranch
		env[fmt.Sprintf("PUSH_%d", i)], env[fmt.Sprintf("BR_%d", i)] = "0", taskBranchIn(k, r.Repo)
	}
	out, err := s.must(ctx, "reading the task's branches", prScript, env, "", time.Minute)
	if err != nil {
		return 0, err
	}
	return prAdoptRun(ctx, p, k, s, api, prOffLines(out, cos, bySlug))
}
