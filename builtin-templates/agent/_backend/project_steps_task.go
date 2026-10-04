// project_steps_task.go — a task's own job kinds after prepare
// (project_steps.go has the workspace's): setup (a repo's setup script, in
// the background), bind (the conversation bound to its checkout: the
// workspace is ready and the parked turn goes on), refs (what the task
// pushed, and its pull requests) and cleanup (its worktrees and branch, or
// a project being deleted). API.md §The workspace.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() {
	projectJobKinds[pjSetup] = jobSetup
	projectJobKinds[pjBind] = jobBind
	projectJobKinds[pjRefs] = jobRefs
	projectJobKinds[pjCleanup] = jobCleanup
}

// jobSetup runs a repo's setup script for a task in the background (its
// checkout the cwd, the task's environment, setupTimeoutSec); keeps the
// redacted tail. A failure doesn't stop the task: the tail goes into its
// brief.
func jobSetup(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if k == nil || k.RunID == 0 {
		return doneJob("the task is gone")
	}
	r, err := agent.db.projectRepo(p.ID, j.Repo)
	if err != nil || strings.TrimSpace(r.Setup) == "" {
		return doneJob("no setup")
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if err != nil {
		return jobOutcome{}, err
	}
	c := k.Dir + "/" + r.Slug
	if j.ExecID == "" {
		path := p.Dir + "/.xbin/setup-" + r.Slug + ".sh"
		if _, err := s.conn.WriteFile(ctx, s.id, path, strings.NewReader(r.Setup), sbxWrite{Mode: "0755", Mkdirs: true}); err != nil {
			return jobOutcome{}, err
		}
		env := taskEnv(p, k, s.box.Home)
		for key, v := range bashEnv {
			env[key] = v
		}
		env["REPO"], env["REPO_DIR"], env["AGENT_SETUP"] = r.Repo, c, path
		j.ClientID = fmt.Sprintf("agent:proj:%d:setup:%d:%s", p.ID, k.N, r.Slug)
		ex, err := s.conn.ExecStart(ctx, s.id, sbxExecReq{Cmd: `sh "$AGENT_SETUP"`, Cwd: c, Env: env,
			TimeoutMs: policyOf(p.Policy).SetupTimeout * 1000, Label: "agent · task " + strconv.FormatInt(k.N, 10) + " · setup " + r.Slug,
			ClientID: j.ClientID})
		if err != nil {
			return jobOutcome{}, err
		}
		j.ExecRef, j.ExecID = s.ref, ex.ID
		_ = agent.db.putCheckoutState(k.ID, r.Slug, "setup", nil, "")
		return waitJob(1000, "running "+r.Slug+"'s setup")
	}
	running, code, tail, err := s.execState(ctx, j.ExecID)
	if err != nil {
		return jobOutcome{}, err
	}
	if running {
		return waitJob(2000, "running "+r.Slug+"'s setup")
	}
	j.Out = clip(projRedact(tail), 8<<10)
	errText := ""
	if code != 0 {
		errText = fmt.Sprintf("the setup script exited %d", code)
		tailText := clip(projRedact(lastLines(tail, 60)), 8<<10)
		_ = agent.db.Tx(func(t *DB) error {
			cur, err := t.taskByID(k.ID)
			if err != nil {
				return err
			}
			st := strings.TrimSpace(cur.SetupTail + "\n" + r.Slug + ":\n" + tailText)
			if len(st) > 8<<10 {
				st = st[len(st)-8<<10:]
			}
			return t.setTask(k.ID, map[string]any{"setup_tail": st})
		})
	}
	_ = agent.db.putCheckoutState(k.ID, r.Slug, "ready", &code, errText)
	return doneJob(fmt.Sprintf("%s's setup exited %d", r.Slug, code))
}

// putCheckoutState sets a checkout's state (and its setup's exit).
func (d *DB) putCheckoutState(taskID int64, repo, state string, exit *int, errText string) error {
	if exit == nil {
		_, err := d.q.Exec(`UPDATE project_checkouts SET state=?, error=? WHERE task_id=? AND repo_slug=?`, state, errText, taskID, repo)
		return err
	}
	_, err := d.q.Exec(`UPDATE project_checkouts SET state=?, setup_exit=?, error=? WHERE task_id=? AND repo_slug=?`, state, *exit, errText, taskID, repo)
	return err
}

// startClientID is the client id of a task's start in its run's inbox: a
// coding agent's start is given the brief once the workspace is ready.
func startClientID(pid, n int64) string { return fmt.Sprintf("proj-start:%d:%d", pid, n) }

// jobBind binds the task's conversation to its checkout — the sandbox
// binding, and a coding agent's sandbox and cwd — once its setups are done
// (setupBlocking); the workspace is ready, and the parked turn goes on.
func jobBind(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if k == nil || k.RunID == 0 {
		return doneJob("the task is gone")
	}
	if policyOf(p.Policy).SetupBlocking && len(agent.db.jobsWhere(`WHERE task_id=? AND kind=? AND state IN ('queued','running','waiting')`, k.ID, pjSetup)) > 0 {
		return waitJob(2000, "waiting for the setup")
	}
	cfg, err := agent.db.runConfig(k.RunID)
	if err != nil {
		return doneJob("the task's conversation is gone")
	}
	ref, cwd := taskRef(p, k), k.cwd()
	b, err := prepareBinding(ctx, binderWho(k.CreatedBy), cfg, sandboxPick{Ref: ref, Cwd: cwd})
	if err != nil {
		var be *bindError
		if errors.As(err, &be) {
			return jobOutcome{}, jobFail("binding the task's sandbox: %s", be.msg)
		}
		return jobOutcome{}, err
	}
	var poke bool
	err = agent.db.Tx(func(t *DB) error {
		if err := storeBinding(t, k.RunID, func(c *Config) error {
			if err := attachSandbox(c, b); err != nil {
				return err
			}
			if c.Harness != nil {
				c.Harness.Ref, c.Harness.Cwd = ref, cwd
			}
			if c.Project == nil {
				c.Project = &ProjectRef{ID: p.ID, Role: projRoleTask, N: k.N}
			}
			return nil
		}); err != nil {
			return err
		}
		_, _ = t.q.Exec(`UPDATE project_checkouts SET state='ready' WHERE task_id=? AND state IN ('added','setup')`, k.ID)
		run, err := t.getRun(k.RunID)
		if err != nil {
			return err
		}
		if run.Engine == engineHarness {
			for _, row := range t.inboxRows(`WHERE run_id=? AND client_id=? AND delivered_at=0`, run.ID, startClientID(p.ID, k.N)) {
				cur, _ := t.taskByID(k.ID)
				row.Body.Text = harnessBrief(t, p, orTask(cur, k), row.Body.Text)
				t.setInboxBody(row.ID, row.Body)
			}
		} else if parsePending(run.Pending).Kind == pendKindProject {
			if _, _, err := t.enqueue(run.ID, inboxWake, inboxBody{Source: "project"}, ""); err != nil {
				return err
			}
		}
		setWS(t, p, k, wsReady, "")
		addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its workspace is ready on " + k.Branch}, false, "")
		poke = true
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	if poke && agent.eng != nil {
		agent.eng.Poke(k.RunID)
	}
	return doneJob("bound")
}

func orTask(a, b *ProjectTask) *ProjectTask {
	if a != nil {
		return a
	}
	return b
}

// --- refs: what the task pushed -----------------------------------------------------------------

// refsScript prints, for each checkout C_i, the task branch's sha on the
// remote as its base last saw it ("-": not there) — a push updates it.
const refsScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "C=\${C_$i}"
  sha=-
  if [ -e "$C/.git" ]; then sha=$(git -C "$C" rev-parse -q --verify "refs/remotes/origin/$BR" 2>/dev/null || echo -); fi
  printf 'REF %s %s\n' "$i" "$sha"
  i=$((i+1))
done
`

// refsReadPulls marks a refs job an scm event asked for (projectRefsCheck):
// it reads the branch's pull requests even when prs holds an open one.
const refsReadPulls = "read-pulls"

// jobRefs (after each of a task's turns, and when an scm event says a PR
// opened): the task's branch on the remote and its open pull requests,
// recorded; when either changed, projectRefsHooks run (subscriptions, CI
// watches) in the same transaction.
func jobRefs(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if k == nil || k.RunID == 0 || k.WS != wsReady {
		return doneJob("no workspace to look at")
	}
	cos := agent.db.checkouts(k.ID)
	repos, err := taskRepos(p, k)
	if err != nil || len(cos) == 0 {
		return doneJob("no checkouts")
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if err != nil {
		return jobOutcome{}, err
	}
	env := map[string]string{"N": strconv.Itoa(len(cos)), "BR": k.Branch}
	for i, c := range cos {
		env[fmt.Sprintf("C_%d", i)] = c.Path
	}
	out, err := s.must(ctx, "reading the task's branch", refsScript, env, "", time.Minute)
	if err != nil {
		return jobOutcome{}, err
	}
	shas := map[int]string{}
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) == 3 && f[0] == "REF" {
			if i, err := strconv.Atoi(f[1]); err == nil && f[2] != "-" {
				shas[i] = f[2]
			}
		}
	}
	repoOf := map[string]ProjectRepo{}
	for _, r := range repos {
		repoOf[r.Slug] = r
	}
	prs := k.taskPRs()
	changed, moved := false, map[string]string{}
	var opened []TaskPR
	api, apiErr := scmFor(p.SCM)
	for i, c := range cos {
		sha := shas[i]
		r, ok := repoOf[c.Repo]
		if !ok {
			continue
		}
		if sha != "" && sha != c.RemoteSHA {
			moved[c.Repo] = sha
			changed = true
		}
		open := false
		for _, pr := range prs {
			open = open || (strings.EqualFold(pr.Repo, r.Repo) && pr.State == "open")
		}
		if sha == "" || (moved[c.Repo] == "" && open && j.ClientID != refsReadPulls) || apiErr != nil {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		page, err := api.Pulls(cctx, scmQuery{Repo: r.Repo, Head: k.Branch, State: "open", As: projectAs(p)})
		cancel()
		if err != nil {
			logf("project %d task %d: reading its pull requests: %v", p.ID, k.N, err)
			continue
		}
		for _, pl := range page.Items {
			upd := TaskPR{Repo: r.Repo, Number: pl.Number, URL: pl.URL, State: orStr(pl.State, "open"), Draft: pl.Draft, HeadSHA: pl.Head.SHA}
			found := false
			for x := range prs {
				if strings.EqualFold(prs[x].Repo, r.Repo) && prs[x].Number == pl.Number {
					found = true
					if prs[x].State != upd.State || prs[x].HeadSHA != upd.HeadSHA || prs[x].Draft != upd.Draft || prs[x].URL != upd.URL {
						upd.Checks, upd.ChecksAt = prs[x].Checks, prs[x].ChecksAt
						prs[x] = upd
						changed = true
					}
				}
			}
			if !found {
				upd.Checks = "none"
				prs = append(prs, upd)
				opened = append(opened, upd)
				changed = true
			}
		}
	}
	if !changed {
		return doneJob("nothing new")
	}
	err = agent.db.Tx(func(t *DB) error {
		for slug, sha := range moved {
			if _, err := t.q.Exec(`UPDATE project_checkouts SET remote_sha=? WHERE task_id=? AND repo_slug=?`, sha, k.ID, slug); err != nil {
				return err
			}
		}
		cur, err := t.taskByID(k.ID)
		if err != nil {
			return err
		}
		b, _ := json.Marshal(prs)
		cols := map[string]any{"prs": string(b)}
		cur.PRs = b
		for _, pr := range prs {
			if pr.State == "open" && cur.Phase == phaseOpen {
				cols["phase"], cur.Phase = phasePR, phasePR
			}
		}
		if err := t.setTask(k.ID, cols); err != nil {
			return err
		}
		for _, pr := range opened {
			addProjectEvent(t, p.ID, k.N, pevPROpened, map[string]any{"text": fmt.Sprintf("opened PR #%d on %s", pr.Number, pr.Repo),
				"url": pr.URL, "repo": pr.Repo, "pr": pr.Number}, false, fmt.Sprintf("pr.opened:%d:%s:%d", k.N, pr.Repo, pr.Number))
		}
		for slug, sha := range moved {
			addProjectEvent(t, p.ID, k.N, pevNote, map[string]any{"text": fmt.Sprintf("pushed %s to %s", shortSHA(sha), k.Branch),
				"sha": sha, "repo": repoOf[slug].Repo}, false, "")
		}
		for _, h := range projectRefsHooks {
			h(t, p, cur)
		}
		onTaskChange(t, p, cur, "prs")
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	return doneJob("recorded the branch and its pull requests")
}

// --- cleanup --------------------------------------------------------------------------------------

// dirtyScript reports, per checkout C_i (DEF_i its default branch), its
// uncommitted files and the commits its upstream (else the default branch
// on the remote) lacks.
const dirtyScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "C=\${C_$i}; DEF=\${DEF_$i}"
  if [ -e "$C/.git" ]; then
    d=$(git -C "$C" status --porcelain | wc -l)
    if git -C "$C" rev-parse -q --verify "@{u}" >/dev/null 2>&1; then
      u=$(git -C "$C" log --oneline "@{u}..HEAD" | wc -l)
    else
      u=$(git -C "$C" log --oneline "origin/$DEF..HEAD" 2>/dev/null | wc -l)
    fi
    printf 'DIRTY %s %s %s\n' "$i" "$d" "$u"
  fi
  i=$((i+1))
done
`

// taskDirty is the checkouts of k with uncommitted or unpushed work
// ({slug, dirty, unpushed}); a repo whose PR merged counts as pushed.
func taskDirty(ctx context.Context, p *Project, k *ProjectTask) ([]map[string]any, error) {
	cos := agent.db.checkouts(k.ID)
	if len(cos) == 0 {
		return nil, nil
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if err != nil {
		return nil, err
	}
	return dirtyIn(ctx, s, p, k, cos)
}

func dirtyIn(ctx context.Context, s *wsbx, p *Project, k *ProjectTask, cos []ProjectCheckout) ([]map[string]any, error) {
	repos, _ := agent.db.projectRepos(p.ID)
	byslug := map[string]ProjectRepo{}
	for _, r := range repos {
		byslug[r.Slug] = r
	}
	merged := map[string]bool{}
	for _, pr := range k.taskPRs() {
		if pr.State == "merged" {
			merged[strings.ToLower(pr.Repo)] = true
		}
	}
	env := map[string]string{"N": strconv.Itoa(len(cos))}
	for i, c := range cos {
		env[fmt.Sprintf("C_%d", i)] = c.Path
		env[fmt.Sprintf("DEF_%d", i)] = byslug[c.Repo].DefaultBranch
	}
	out, err := s.must(ctx, "checking the checkouts", dirtyScript, env, "", time.Minute)
	if err != nil {
		return nil, err
	}
	var dirty []map[string]any
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 4 || f[0] != "DIRTY" {
			continue
		}
		i, _ := strconv.Atoi(f[1])
		d, _ := strconv.Atoi(f[2])
		u, _ := strconv.Atoi(f[3])
		if i >= len(cos) || cos[i].Mode == coMain {
			continue
		}
		if merged[strings.ToLower(byslug[cos[i].Repo].Repo)] {
			u = 0
		}
		if d > 0 || u > 0 {
			dirty = append(dirty, map[string]any{"slug": cos[i].Repo, "dirty": d, "unpushed": u})
		}
	}
	return dirty, nil
}

// cleanupScript removes a task's checkouts (W_i: worktree of B_i on BR, or a
// clone; MODE main is never touched), then its directory once no checkout
// is left in it.
const cleanupScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}; C=\${C_$i}; MODE=\${MODE_$i}"
  case "$MODE" in
    worktree)
      if [ -e "$C" ]; then git -C "$B" worktree remove --force "$C"; fi
      git -C "$B" worktree prune
      git -C "$B" branch -D "$BR" >/dev/null 2>&1 || true ;;
    clone) rm -rf -- "$C" ;;
  esac
  i=$((i+1))
done
if [ -d "$TASK_DIR" ] && [ -z "$(find "$TASK_DIR" -mindepth 1 -maxdepth 1 -type d)" ]; then rm -rf -- "$TASK_DIR"; fi
`

// jobCleanup removes a task's worktrees and branch — refused (ws blocked)
// while a checkout has uncommitted or unpushed work, unless the job was
// forced (by the conversation's owner). A project-level cleanup is the
// project being deleted.
func jobCleanup(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if k == nil {
		return deleteProjectStep(ctx, p, j)
	}
	if k.WS == wsCleaned {
		return doneJob("cleaned already")
	}
	cos := agent.db.checkouts(k.ID)
	var live []ProjectCheckout
	for _, c := range cos {
		if c.State != "removed" {
			live = append(live, c)
		}
	}
	if len(live) == 0 {
		_ = agent.db.Tx(func(t *DB) error {
			setWS(t, p, k, wsCleaned, "")
			return t.setTask(k.ID, map[string]any{"cleaned_ms": nowMs()})
		})
		return doneJob("nothing to remove")
	}
	s, err := openWsbx(ctx, p, taskRef(p, k))
	if sbxRefusal(err) == "not-found" {
		_ = agent.db.Tx(func(t *DB) error {
			_, _ = t.q.Exec(`UPDATE project_checkouts SET state='removed' WHERE task_id=?`, k.ID)
			setWS(t, p, k, wsCleaned, "")
			return t.setTask(k.ID, map[string]any{"cleaned_ms": nowMs()})
		})
		return doneJob("the sandbox is gone")
	}
	if err != nil {
		return jobOutcome{}, err
	}
	if j.ClientID != "force" {
		dirty, err := dirtyIn(ctx, s, p, k, live)
		if err != nil {
			return jobOutcome{}, err
		}
		if len(dirty) > 0 {
			b, _ := json.Marshal(map[string]any{"repos": dirty})
			_ = agent.db.Tx(func(t *DB) error {
				setWS(t, p, k, wsBlocked, string(b))
				addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its cleanup was refused: work that isn't pushed", "repos": dirty}, true, "")
				return nil
			})
			return doneJob("refused: work that isn't pushed")
		}
	}
	_ = agent.db.Tx(func(t *DB) error { setWS(t, p, k, wsCleaning, ""); return nil })
	repos, _ := agent.db.projectRepos(p.ID)
	byslug := map[string]ProjectRepo{}
	for _, r := range repos {
		byslug[r.Slug] = r
	}
	env := map[string]string{"N": strconv.Itoa(len(live)), "BR": k.Branch, "TASK_DIR": k.Dir}
	for i, c := range live {
		r, ok := byslug[c.Repo]
		b := p.Dir + "/.repos/" + c.Repo + ".git"
		if ok {
			b = basePath(p, r)
		}
		env[fmt.Sprintf("B_%d", i)], env[fmt.Sprintf("C_%d", i)], env[fmt.Sprintf("MODE_%d", i)] = b, c.Path, c.Mode
	}
	if _, err := s.must(ctx, "removing the checkouts", cleanupScript, env, "", 2*time.Minute); err != nil {
		return jobOutcome{}, err
	}
	_ = agent.db.Tx(func(t *DB) error {
		_, _ = t.q.Exec(`UPDATE project_checkouts SET state='removed' WHERE task_id=? AND mode<>?`, k.ID, coMain)
		_, _ = t.q.Exec(`UPDATE project_checkouts SET state='kept' WHERE task_id=? AND mode=?`, k.ID, coMain)
		setWS(t, p, k, wsCleaned, "")
		addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its worktrees and branch were cleaned up"}, false, "")
		return t.setTask(k.ID, map[string]any{"cleaned_ms": nowMs()})
	})
	return doneJob("cleaned up")
}

// deleteProjectStep takes a project being deleted down: its credentials
// scrubbed, its tasks' workspaces cleaned up (unless its sandbox goes too),
// its conversations deleted, its sandbox deleted when it made it and was
// asked to, then its rows.
func deleteProjectStep(ctx context.Context, p *Project, j *ProjectJob) (jobOutcome, error) {
	if p.State != projDeleting {
		return doneJob("not being deleted")
	}
	if err := scmScrubCreds(ctx, p, "", "delete"); err != nil {
		return jobOutcome{}, err
	}
	key := "proj_sbx_delete:" + strconv.FormatInt(p.ID, 10)
	dropSbx := agent.db.getSetting(key) == "1" && p.SandboxMade && p.SandboxRef != ""
	ks, _ := agent.db.tasksWhere(`WHERE project_id=?`, p.ID)
	if !dropSbx {
		waiting := false
		for _, k := range ks {
			switch {
			case agent.db.liveJob(p.ID, k.ID, "", pjCleanup) != nil:
				waiting = true
			case k.WS != wsCleaned && k.WS != wsPending && k.WS != wsBlocked && len(agent.db.checkouts(k.ID)) > 0 &&
				len(agent.db.jobsWhere(`WHERE task_id=? AND kind=? AND state='failed'`, k.ID, pjCleanup)) == 0:
				if _, err := agent.db.queueJob(p.ID, k.ID, "", pjCleanup, j.By, 0); err != nil {
					return jobOutcome{}, err
				}
				waiting = true
			}
		}
		if waiting {
			return waitJob(2000, "cleaning the tasks up")
		}
	}
	runs := scanIDs(agent.db.q.Query(`SELECT id FROM runs WHERE parent_id=0 AND origin='project' AND origin_id=?`, p.ID))
	for _, id := range runs {
		if agent.eng != nil {
			agent.eng.endHarnesses(ctx, id)
		}
		if err := deleteConversation(id); err != nil {
			return jobOutcome{}, err
		}
	}
	if dropSbx {
		if conn, id, err := sbxDialRef(p.SandboxRef, sbxUserOf(binderWho(p.Owner))); err == nil {
			if err := conn.Delete(ctx, id); err != nil && sbxRefusal(err) != "not-found" {
				return jobOutcome{}, err
			}
		}
	}
	acl, _ := agent.db.loadProjectACL(p.ID)
	err := agent.db.Tx(func(t *DB) error {
		for _, q := range []string{
			`DELETE FROM project_checkouts WHERE task_id IN (SELECT id FROM project_tasks WHERE project_id=?)`,
			`DELETE FROM project_tasks WHERE project_id=?`,
			`DELETE FROM project_repos WHERE project_id=?`,
			`DELETE FROM project_members WHERE project_id=?`,
			`DELETE FROM project_queue WHERE project_id=?`,
			`DELETE FROM project_events WHERE project_id=?`,
			`DELETE FROM project_jobs WHERE project_id=? AND state<>'running'`,
			`DELETE FROM projects WHERE id=?`,
		} {
			if _, err := t.q.Exec(q, p.ID); err != nil {
				return err
			}
		}
		_, _ = t.q.Exec(`DELETE FROM settings WHERE key IN (?, ?)`, key, sbxNewKey(p.ID))
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	projACL.flush(p.ID)
	if acl != nil {
		go publishProject(p.ID, "deleted", 0, acl)
	}
	return doneJob("deleted")
}
