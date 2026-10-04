// project_tasks.go — a project's tasks (API.md §Projects and tasks): making
// one (its conversation — a run with origin project, Config.Project set,
// the project's sharing copied — its number, branch, ports, its start in
// the queue and its workspace's first job), what a task looks like
// (TaskView: the derived board column, state and whom it waits for), and
// the actions on it (cancel, close, retry, refresh, cleanup).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// projErr is a refusal a project route answers: {error, refusal?, …extra}.
type projErr struct {
	code    int
	msg     string
	refusal string
	extra   map[string]any
}

func (e *projErr) Error() string { return e.msg }

func perr(code int, format string, args ...any) *projErr {
	return &projErr{code: code, msg: fmt.Sprintf(format, args...)}
}

// refusalBarred, refusalLimit, refusalBusy, refusalDirty: the agent's own
// refusals on project routes (API.md §Projects and tasks).
const (
	refusalBarred = "barred"
	refusalLimit  = "limit"
	refusalBusy   = "busy"
	refusalDirty  = "dirty"
)

// --- making a task ------------------------------------------------------------------------

// taskClassFor checks the class a task of p would run in, for w: it exists,
// w may use it, it never has internal reach (409 class-internal: every task
// reads scm text, and the coordinator that steers it is in the web lane),
// and it may work in p's sandbox.
func taskClassFor(w who, p *Project, name string) (agentClass, error) {
	st := currentClasses()
	cls, ok := st.find(strings.TrimSpace(name))
	switch {
	case !ok:
		return cls, perr(400, "class: no class %q (GET /classes lists them)", name)
	case cls.has(tsInternal):
		return cls, &projErr{code: 409, refusal: refusalClassInternal, msg: fmt.Sprintf(
			"the %s class has internal reach: a project's tasks read text from the scm provider (issues, reviews, CI logs) and never run with internal reach", orStr(cls.Name, cls.ID))}
	case !cls.usableBy(w):
		return cls, perr(403, "the %s class is for the agent's managers", orStr(cls.Name, cls.ID))
	case !cls.has(tsSandbox):
		return cls, perr(403, "the %s class has no sandbox toolset: a task works in the project's sandbox", orStr(cls.Name, cls.ID))
	}
	if p != nil && p.SandboxRef != "" {
		if provider, _, ok := splitSandboxRef(p.SandboxRef); ok && !cls.allowsManager(provider) {
			return cls, perr(403, "the %s class doesn't allow sandboxes from %s, where this project works", orStr(cls.Name, cls.ID), provider)
		}
	}
	return cls, nil
}

// lastHarness is the coding agent w last started a conversation with ("":
// none) — policy.engine auto's pick.
func (d *DB) lastHarness(w who) string {
	if w.kind != whoUser {
		return ""
	}
	var raw string
	if d.q.QueryRow(`SELECT config FROM runs WHERE owner=? AND engine='harness' AND parent_id=0 ORDER BY id DESC LIMIT 1`, w.user).Scan(&raw) != nil {
		return ""
	}
	if c := parseConfig(raw); c.Harness != nil {
		return c.Harness.Provider
	}
	return ""
}

// taskEngine picks who answers a new task: a coding agent where one may
// run (not at the global instance, the class allows it, the sandbox's image
// has it), else the built-in agent. A coding agent asked for by name that
// can't run is refused (409 barred); policy.engine's pick falls back.
func taskEngine(ctx context.Context, w who, p *Project, pol ProjectPolicy, cls agentClass, s TaskSpec) (*harnessReq, error) {
	req, named := (*harnessReq)(nil), s.Agent != nil
	switch {
	case named:
		req = &harnessReq{Provider: s.Agent.Provider, Mode: s.Agent.Mode}
	case pol.Engine == "builtin":
		return nil, nil
	case pol.Engine == "harness" && pol.Harness != "":
		req = &harnessReq{Provider: pol.Harness}
	case pol.Engine == "auto" || pol.Engine == "harness":
		prov := orStr(pol.Harness, projAg().db.lastHarness(w))
		if prov == "" {
			return nil, nil
		}
		req = &harnessReq{Provider: prov}
	default:
		return nil, nil
	}
	fail := func(code int, msg string) (*harnessReq, error) {
		if named {
			return nil, &projErr{code: code, refusal: refusalBarred, msg: msg}
		}
		return nil, nil
	}
	if why := harnessBarred(nil); why != "" {
		return fail(409, why)
	}
	if !cls.allowsHarness(req.Provider) {
		return fail(409, fmt.Sprintf("the %s class doesn't allow the coding agent %s", orStr(cls.Name, cls.ID), req.Provider))
	}
	if _, err := harnessClass(ctx, w, req, cls.ID, "", ""); err != nil {
		if e, ok := err.(*errClass); ok {
			return fail(e.code, e.msg)
		}
		return fail(409, err.Error())
	}
	if p.SandboxRef != "" {
		if provider, id, ok := splitSandboxRef(p.SandboxRef); ok {
			if m, ok := boundManager(provider); ok {
				cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				if h, err := managerHello(cctx, m); err == nil && h.advertises() {
					if box, err := (&sbxConn{M: m, User: sbxUserOf(binderWho(p.Owner))}).Get(cctx, id); err == nil &&
						!hasStr(h.imageHarnesses(box.Image.ID), req.Provider) {
						return fail(409, fmt.Sprintf("the project's sandbox (%s) has no %s", box.Name, req.Provider))
					}
				}
			}
		}
	}
	return req, nil
}

// createTask makes a task of p for w: its conversation (held: its start goes
// through the queue), its row, its start, its first workspace job. In one
// transaction; the pump and the worker run after it.
func (ag *Agent) createTask(ctx context.Context, w who, p *Project, s TaskSpec) (*ProjectTask, *Run, error) {
	switch {
	case p.Kind == projTeam:
		return nil, nil, perr(409, "a team project's definition has no tasks: work on it from your own space (its membership)")
	case p.State != projActive:
		return nil, nil, perr(409, "this project is %s: it takes no new tasks", p.State)
	case strings.TrimSpace(s.Text) == "" && s.Issue == nil:
		return nil, nil, perr(400, "need {text}, the task's brief (or an issue)")
	case s.Size != "" && s.Size != sizeSmall && s.Size != sizeBig:
		return nil, nil, perr(400, "size: small or big")
	case len(s.Text) > 64<<10:
		return nil, nil, perr(400, "text: at most 64 KiB")
	case !validPick(s.Model):
		return nil, nil, perr(400, "model: a model id from GET /models (up to 200 characters)")
	}
	pol := policyOf(p.Policy)
	repos, err := ag.db.projectRepos(p.ID)
	if err != nil {
		return nil, nil, err
	}
	var use []string
	if len(s.Repos) == 0 {
		for _, r := range repos {
			if r.State != "removing" {
				use = append(use, r.Slug)
			}
		}
	} else {
		for _, slug := range s.Repos {
			ok := false
			for _, r := range repos {
				ok = ok || (r.Slug == slug && r.State != "removing")
			}
			if !ok {
				return nil, nil, perr(400, "repos: %q isn't one of this project's repos", slug)
			}
			if !hasStr(use, slug) {
				use = append(use, slug)
			}
		}
	}
	var issue *scmIssue
	if s.Issue != nil {
		r, ok := ag.db.repoNamed(p.ID, s.Issue.Repo)
		if !ok || s.Issue.Number <= 0 {
			return nil, nil, perr(400, "issue.repo: %q isn't one of this project's repos", s.Issue.Repo)
		}
		s.Issue.Repo = r.Repo
		if issue, err = projectIssue(ctx, p, r.Repo, s.Issue.Number); err != nil {
			return nil, nil, err
		}
		s.Issue.Title, s.Issue.URL = clip(projRedact(issue.Title), 200), issue.URL
		if len(s.Repos) == 0 {
			use = []string{r.Slug}
		}
	}
	cls, err := taskClassFor(w, p, orStr(s.Class, pol.TaskClass))
	if err != nil {
		return nil, nil, err
	}
	hreq, err := taskEngine(ctx, w, p, pol, cls, s)
	if err != nil {
		return nil, nil, err
	}
	if s.From != 0 {
		var open, today int
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND from_run<>0 AND phase IN ('open','pr')`, p.ID).Scan(&open)
		_ = ag.db.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND from_run<>0 AND created_ms>?`,
			p.ID, nowMs()-24*3600*1000).Scan(&today)
		switch {
		case open >= pol.MaxOpenTasks:
			return nil, nil, &projErr{code: 429, refusal: refusalLimit, msg: fmt.Sprintf("this project has %d open tasks a coordinator made (policy.maxOpenTasks)", open)}
		case today >= pol.MaxCreatesDay:
			return nil, nil, &projErr{code: 429, refusal: refusalLimit, msg: fmt.Sprintf("coordinators made %d tasks in this project in the last 24 hours (policy.maxTaskCreatesPerDay)", today)}
		}
	}
	title := strings.TrimSpace(s.Title)
	if title == "" && s.Issue != nil {
		title = s.Issue.Title
	}
	if title == "" {
		title = firstLine(s.Text)
	}
	title = clipRunes(orStr(title, "task"), 80)
	cfg := parseConfig(ag.db.getSetting("config"))
	cfg.setClass(cls, false)
	if hreq == nil && s.Model != "" {
		cfg.Pick = s.Model
	}
	var k *ProjectTask
	var runID int64
	err = ag.db.Tx(func(t *DB) error {
		var n int64
		if err := t.q.QueryRow(`SELECT COALESCE(MAX(n), 0) + 1 FROM project_tasks WHERE project_id=?`, p.ID).Scan(&n); err != nil {
			return err
		}
		slug := orStr(slugOf(title, 32), "task")
		k = &ProjectTask{ProjectID: p.ID, N: n, Title: title, Slug: slug, Size: orStr(s.Size, sizeSmall),
			Branch: p.branchPrefix(pol) + "/" + fmt.Sprintf("%d-%s", n, slug), Issue: s.Issue, Repos: use,
			SandboxRef: p.SandboxRef, PortsBase: portsOf(pol, n).Base, WS: wsPending, Phase: phaseOpen,
			TurnBy: srcHuman, FromRun: s.From, CreatedBy: w.tag(), CreatedMs: nowMs(), UpdatedMs: nowMs()}
		if s.From != 0 {
			k.TurnBy = srcCoordinator
		}
		if p.Dir != "" {
			k.Dir = p.Dir + "/tasks/" + fmt.Sprintf("%d-%s", n, slug)
		}
		rc := cfg
		rc.Project = &ProjectRef{ID: p.ID, Role: projRoleTask, N: n}
		if hreq != nil {
			rc.Engine = engineHarness
			rc.Harness = &HarnessConfig{Provider: hreq.Provider, Mode: hreq.Mode, Options: hreq.Options,
				Ref: k.SandboxRef, Cwd: k.cwd(), By: w.tag()}
		}
		var err error
		runID, err = ag.startRunTx(t, runOpts{Title: title, Cfg: rc, Hold: true, Stamp: runStamp{Owner: w.tag(),
			Visibility: p.Visibility, TeamRole: p.TeamRole, Origin: originProject, OriginID: p.ID, TitleSrc: "origin"}})
		if err != nil {
			return err
		}
		k.RunID = runID
		for _, m := range t.projectMembers(p.ID) {
			if _, err := t.q.Exec(`INSERT OR REPLACE INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, ?)`,
				runID, m.User, m.Role, now()); err != nil {
				return err
			}
		}
		issueJSON := ""
		if s.Issue != nil {
			b, _ := json.Marshal(s.Issue)
			issueJSON = string(b)
		}
		reposJSON, _ := json.Marshal(use)
		if err := t.q.QueryRow(`INSERT INTO project_tasks (project_id, n, run_id, title, slug, size, branch, issue, repos,
			sandbox_ref, dir, ports_base, ws, phase, turn_by, from_run, created_by, created_ms, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			p.ID, n, runID, k.Title, k.Slug, k.Size, k.Branch, issueJSON, string(reposJSON), k.SandboxRef, k.Dir,
			k.PortsBase, k.WS, k.Phase, k.TurnBy, k.FromRun, k.CreatedBy, k.CreatedMs, k.UpdatedMs).Scan(&k.ID); err != nil {
			return err
		}
		k.PRs, k.CIFixes = json.RawMessage("[]"), json.RawMessage("{}")
		text := strings.TrimSpace(s.Text)
		if issue != nil {
			text = strings.TrimSpace(frameIssue(p.Host, issue) + "\n\n" + text)
		}
		src, sender := srcHuman, w.user
		if s.From != 0 {
			src, sender = srcCoordinator, ""
		}
		if _, err := queueTaskInput(t, taskInput{Project: p.ID, N: n, Kind: "start", Text: text, Source: src, Sender: sender}); err != nil {
			return err
		}
		if _, err := t.queueJob(p.ID, k.ID, "", pjPrepare, w.tag(), 0); err != nil {
			return err
		}
		addProjectEvent(t, p.ID, n, pevTaskCreated, map[string]any{"text": "created: " + title, "by": w.tag()}, false, "")
		t.touchProject(p.ID)
		onTaskChange(t, p, k, "created")
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	run, err := ag.db.getRun(runID)
	return k, run, err
}

// cwd is where a task works: its checkout for a one-repo task, else its
// task directory (each repo's AGENTS.md / CLAUDE.md at the root).
func (k *ProjectTask) cwd() string {
	if k.Dir == "" {
		return ""
	}
	if len(k.Repos) == 1 {
		return k.Dir + "/" + k.Repos[0]
	}
	return k.Dir
}

// projectIssue reads an issue of p's through the project's identity.
func projectIssue(ctx context.Context, p *Project, repo string, n int) (*scmIssue, error) {
	api, err := scmFor(p.SCM)
	if err != nil {
		return nil, err
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return api.Issue(cctx, repo, n, false, projectAs(p))
}

// projectAs is the identity a project acts as at its provider: policy.as,
// else the person in a person's partition, else the bot (API.md §Projects and tasks).
func projectAs(p *Project) string {
	if as := policyOf(p.Policy).As; as != "" {
		return as
	}
	if userMode() && (p.Kind == projPersonal || p.Kind == projMembership) {
		return scmAsPerson
	}
	return scmAsBot
}

// --- what a task looks like --------------------------------------------------------------

// taskDerived is the board column, the task state and whom it waits for,
// from its row, its run's status, its CI and whether input waits in the queue.
func taskDerived(k *ProjectTask, status string, ci *CISummary, queued bool) (col, state, waiting string) {
	switch k.Phase {
	case phaseDeleted:
		return colDone, taskDeleted, ""
	case phaseMerged:
		return colDone, taskMerged, ""
	case phaseClosed:
		return colDone, taskClosed, ""
	case phaseDone:
		return colDone, taskDone, ""
	}
	switch k.WS {
	case wsPending, wsQueued:
		if queued && (status == statusIdle || status == "") {
			return colQueued, taskQueued, "slot"
		}
		return colQueued, taskQueued, ""
	case wsPreparing:
		return colQueued, taskPreparing, ""
	case wsSignin:
		return colNeedsYou, taskSignin, "signin"
	case wsFailed:
		return colNeedsYou, taskFailed, "you"
	case wsBlocked:
		return colNeedsYou, taskBlocked, "you"
	}
	switch status {
	case statusWaiting:
		return colNeedsYou, taskNeedsYou, "you"
	case statusRunning, statusQueued, statusBlocked, statusAwait, statusSleep:
		return colWorking, taskWorking, ""
	case statusCanceled:
		return colDone, taskCancelled, ""
	case statusError:
		return colNeedsYou, taskFailed, "you"
	}
	if k.Phase == phasePR {
		checks := "none"
		if ci != nil {
			checks = ci.State
		} else {
			for _, pr := range k.taskPRs() {
				if pr.State == "open" && pr.Checks != "" {
					checks = pr.Checks
				}
			}
		}
		switch checks {
		case "failure":
			return colPR, taskCIFailed, "you"
		case "pending":
			return colPR, taskCI, "ci"
		}
		return colPR, taskAwaitingReview, "review"
	}
	if queued {
		return colQueued, taskQueued, "slot"
	}
	return colNeedsYou, taskNeedsYou, "you" // its turn ended: its answer waits for a person (or the coordinator)
}

// projTaskView is task k as the API shows it.
func (d *DB) projTaskView(p *Project, k *ProjectTask) TaskView {
	v := TaskView{Project: k.ProjectID, N: k.N, Run: k.RunID, Title: k.Title, Size: k.Size, Branch: k.Branch,
		Issue: k.Issue, Repos: k.Repos, WS: k.WS, Phase: k.Phase, SandboxRef: k.SandboxRef, Fork: k.ForkMade,
		Dir: k.Dir, Checkouts: d.checkouts(k.ID), PRs: k.taskPRs(), TurnBy: k.TurnBy, Error: k.Error, Last: k.Last,
		CreatedBy: k.CreatedBy, CreatedMs: k.CreatedMs, UpdatedMs: k.UpdatedMs}
	if v.Repos == nil {
		v.Repos = []string{}
	}
	if p != nil && k.PortsBase > 0 {
		v.Ports = &TaskPorts{Base: k.PortsBase, Span: policyOf(p.Policy).Ports.Span}
	}
	if k.RunID != 0 {
		if run, err := d.getRun(k.RunID); err == nil {
			v.RunStatus = run.Status
			if run.Engine == engineHarness {
				v.Engine = engineHarness
				if cfg, err := d.runConfig(run.ID); err == nil && cfg.Harness != nil {
					v.Harness = cfg.Harness.Provider
				}
			}
		}
		v.CI = taskCISummary(k.RunID)
	}
	if js := d.jobsWhere(`WHERE task_id=? AND state IN ('queued','running','waiting') ORDER BY id LIMIT 1`, k.ID); len(js) > 0 {
		v.Step = orStr(js[0].Step, js[0].Kind)
	}
	v.Column, v.State, v.WaitingFor = taskDerived(k, v.RunStatus, v.CI, queueWaits(d.queuedInputs(k.ProjectID, k.N), k.N))
	return v
}

// taskParkFor is the park a task's run is in (nil: none), with the device
// code of a pending sign-in only for the person who must sign in — the
// project's owner, whose identity the project uses (never a stream event,
// never another viewer).
func taskParkFor(run *Run, p *Project, w who) *ProjectPark {
	ps := parsePending(run.Pending)
	if ps.Kind != pendKindProject || ps.Project == nil {
		return nil
	}
	pk := *ps.Project
	pk.Signin = nil
	if pk.WS == wsSignin && w.kind == whoUser && w.viewedBy == "" && w.user == p.Owner && run.Owner == w.user {
		pk.Signin = scmPendingSignin(w.user, p.SCM)
	}
	return &pk
}

// taskAnswer is GET /runs/{id}/task's answer: the TaskView, and the run's
// workspace park (park) when the gate holds it.
func (d *DB) taskAnswer(p *Project, k *ProjectTask, w who) map[string]any {
	b, _ := json.Marshal(d.projTaskView(p, k))
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if k.RunID != 0 {
		if run, err := d.getRun(k.RunID); err == nil {
			if pk := taskParkFor(run, p, w); pk != nil {
				out["park"] = pk
			}
		}
	}
	return out
}

// projectRunView (runViewHooks): a project run's answers carry project
// {id, name, n, role}; a task's also projectTask, and its park's sign-in for
// the person who must sign in.
func projectRunView(t *DB, w who, run *Run, v map[string]any) {
	ref := t.projectRefOf(run)
	if ref == nil {
		return
	}
	p, err := t.getProject(ref.ID)
	if err != nil {
		return
	}
	v["project"] = map[string]any{"id": p.ID, "name": p.Name, "n": ref.N, "role": ref.Role}
	if !ref.isTask() || run.ParentID != 0 {
		return
	}
	k := t.taskByRun(run.ID)
	if k == nil {
		return
	}
	v["projectTask"] = t.taskAnswer(p, k, w)
	if pk := taskParkFor(run, p, w); pk != nil && pk.Signin != nil {
		if sum, ok := v["run"].(map[string]any); ok {
			if ps, ok := sum["pendingState"].(pendingState); ok {
				ps.Project = pk
				sum["pendingState"] = ps
			}
		}
	}
}

// --- acting on a task -------------------------------------------------------------------------

// cancelTask stops a task's run and everything it started, and drops its
// queued inputs; its conversation, worktrees and branch stay.
func (ag *Agent) cancelTask(p *Project, k *ProjectTask, by who, reason string) error {
	return ag.db.Tx(func(t *DB) error {
		if k.RunID != 0 {
			ag.cancelRuns(t, k.RunID, true, orStr(reason, "cancelled"))
		}
		t.dropQueued(p.ID, k.N)
		t.dropUntaken(k.RunID)
		addProjectEvent(t, p.ID, k.N, pevTaskCancel, map[string]any{"text": "cancelled" + orStr(": "+reason, ""), "by": by.tag()}, false, "")
		onTaskChange(t, p, k, "state")
		return nil
	})
}

// closeTask closes a task (phase closed): stops it, drops its queue, and
// with cleanup queues its workspace's cleanup; closePRs closes its open
// pull requests at the provider (best effort, as the project).
func (ag *Agent) closeTask(ctx context.Context, p *Project, k *ProjectTask, by who, o ActOpts) error {
	if o.ClosePRs {
		if api, err := scmFor(p.SCM); err == nil {
			for _, pr := range k.taskPRs() {
				if pr.State == "open" {
					cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
					closed := "closed"
					_, _ = api.PullPatch(cctx, pr.Number, scmPullPatch{Repo: pr.Repo, State: &closed, As: projectAs(p)})
					cancel()
				}
			}
		}
	}
	return ag.db.Tx(func(t *DB) error {
		if k.RunID != 0 {
			ag.cancelRuns(t, k.RunID, true, orStr(o.Reason, "the task was closed"))
		}
		t.dropQueued(p.ID, k.N)
		t.dropUntaken(k.RunID)
		k.Phase = phaseClosed
		if err := t.setTask(k.ID, map[string]any{"phase": phaseClosed}); err != nil {
			return err
		}
		if o.Cleanup && k.WS != wsPending && k.WS != wsCleaned {
			if _, err := t.queueJob(p.ID, k.ID, "", pjCleanup, by.tag(), 0); err != nil {
				return err
			}
		}
		addProjectEvent(t, p.ID, k.N, pevClosed, map[string]any{"text": "closed", "by": by.tag()}, false, "")
		onTaskChange(t, p, k, "phase")
		return nil
	})
}

// retryTask queues a task's failed workspace jobs again (and the project's
// own failed ones it waits for).
func (ag *Agent) retryTask(p *Project, k *ProjectTask, by who) error {
	return ag.db.Tx(func(t *DB) error {
		n := 0
		for _, j := range t.jobsWhere(`WHERE project_id=? AND (task_id=? OR task_id=0) AND state='failed'
			AND id IN (SELECT MAX(id) FROM project_jobs WHERE project_id=? AND (task_id=? OR task_id=0) GROUP BY task_id, repo_slug, kind)`,
			p.ID, k.ID, p.ID, k.ID) {
			if t.liveJob(j.Project, j.Task, j.Repo, j.Kind) != nil {
				continue
			}
			if _, err := t.queueJob(j.Project, j.Task, j.Repo, j.Kind, by.tag(), 0); err != nil {
				return err
			}
			n++
		}
		if k.WS == wsFailed || k.WS == wsSignin {
			if _, err := t.queueJob(p.ID, k.ID, "", pjPrepare, by.tag(), 0); err != nil {
				return err
			}
			setWS(t, p, k, wsQueued, "")
		}
		if k.WS == wsBlocked {
			setWS(t, p, k, wsReady, "")
		}
		return nil
	})
}

// refreshTask: fresh credentials, a fetch of its repos, its pushed branch
// and PRs read again.
func (ag *Agent) refreshTask(p *Project, k *ProjectTask, by who) error {
	return ag.db.Tx(func(t *DB) error {
		if projectJobKinds[pjCreds] != nil {
			if _, err := t.queueJob(p.ID, k.ID, "", pjCreds, by.tag(), 0); err != nil {
				return err
			}
		}
		for _, slug := range k.Repos {
			if _, err := t.queueJob(p.ID, 0, slug, pjFetch, by.tag(), 0); err != nil {
				return err
			}
		}
		if k.WS == wsReady {
			_, err := t.queueJob(p.ID, k.ID, "", pjRefs, by.tag(), 0)
			return err
		}
		return nil
	})
}

// writeProjErr answers err: a projErr, a provider's refusal passed through
// (its status and payload), a sandbox manager's, or a 500.
func writeProjErr(w http.ResponseWriter, err error) {
	var pe *projErr
	var se *scmError
	var be *bindError
	var sbe *sbxError
	switch {
	case errors.As(err, &pe):
		body := map[string]any{"error": pe.msg}
		if pe.refusal != "" {
			body["refusal"] = pe.refusal
		}
		for k, v := range pe.extra {
			body[k] = v
		}
		xbin.WriteJSON(w, pe.code, body)
	case errors.As(err, &se):
		code := se.Status
		if code == 0 {
			code = 502
		}
		b, _ := json.Marshal(se)
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		xbin.WriteJSON(w, code, body)
	case errors.As(err, &be), errors.As(err, &sbe):
		writeSbxErr(w, err)
	default:
		xbin.WriteError(w, 500, err.Error())
	}
}

// projectRunBarred answers 409 for a project's conversation (origin
// project) on a route that would share it apart from its project or take
// it to another home — publish, copy, hosting, a move, its own sharing or
// members — and says whether it did. GET /runs/{id}/export stays.
func projectRunBarred(w http.ResponseWriter, root int64) bool {
	run, err := projAg().db.getRun(root)
	if err != nil || run.Origin != originProject {
		return false
	}
	name := "its project"
	if p, err := projAg().db.getProject(run.OriginID); err == nil {
		name = "project " + p.Name
	}
	xbin.WriteJSON(w, http.StatusConflict, map[string]any{"refusal": refusalBarred, "error": fmt.Sprintf(
		"this conversation is a task of %s: its sharing is the project's, and it stays in its project's space", name)})
	return true
}
