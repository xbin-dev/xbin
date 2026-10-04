// project_settings.go — the project routes besides creating and changing
// one (project_routes.go): members, repos, status, warm, the issue picker,
// the event feed, and the task routes (the task list, a new task, a batch
// from issues, cancel; a task conversation's GET /runs/{id}/task and its
// actions). API.md §Projects and tasks, §The workspace.
package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// --- members -------------------------------------------------------------------------------

func membersAnswer(p *Project) map[string]any {
	return map[string]any{"owner": p.Owner, "members": agent.db.projectMembers(p.ID)}
}

func handleProjectMembers(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	xbin.WriteJSON(w, 200, membersAnswer(p))
}

func handleAddProjectMember(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	var body projectMember
	if !decodeBody(w, r, &body) {
		return
	}
	body.Role = orStr(body.Role, roleParticipant)
	switch {
	case userMode():
		xbin.WriteError(w, 409, noShareWords)
		return
	case !userIDRe.MatchString(body.User):
		xbin.WriteError(w, 400, "user: a user id (the login name)")
		return
	case body.Role != roleViewer && body.Role != roleParticipant:
		xbin.WriteError(w, 400, "role: viewer or participant")
		return
	case body.User == p.Owner:
		xbin.WriteError(w, 400, body.User+" is the owner")
		return
	}
	var runs []int64
	err := agent.db.Tx(func(t *DB) error {
		var n int
		_ = t.q.QueryRow(`SELECT count(*) FROM project_members WHERE project_id=?`, p.ID).Scan(&n)
		if n >= maxShareMembers {
			return perr(400, "a project has at most %d members — share it with the team instead", maxShareMembers)
		}
		if _, err := t.q.Exec(`INSERT INTO project_members (project_id, user, role, added_by, created_ms) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(project_id, user) DO UPDATE SET role=excluded.role`, p.ID, body.User, body.Role, c.tag(), nowMs()); err != nil {
			return err
		}
		var err error
		runs, err = t.copyACLToTasks(p.ID)
		t.touchProject(p.ID)
		emitProject(t, p.ID, "project", 0)
		return err
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	agent.afterACLChange(p.ID, runs)
	xbin.WriteJSON(w, 200, membersAnswer(p))
}

// handleRemoveProjectMember: a member removes themselves; the owner removes
// anyone.
func handleRemoveProjectMember(w http.ResponseWriter, r *http.Request) {
	p, lv := projectOf(r)
	c := callerOf(r)
	user := r.PathValue("user")
	if lv < lvOwner && !(c.kind == whoUser && c.viewedBy == "" && c.user == user) {
		xbin.WriteError(w, 403, "only the project's owner removes someone else")
		return
	}
	var runs []int64
	err := agent.db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`DELETE FROM project_members WHERE project_id=? AND user=?`, p.ID, user); err != nil {
			return err
		}
		var err error
		runs, err = t.copyACLToTasks(p.ID)
		emitProject(t, p.ID, "project", 0)
		return err
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	agent.afterACLChange(p.ID, runs)
	w.WriteHeader(204)
}

// --- repos -------------------------------------------------------------------------------------

func handleAddRepo(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	var body newRepoReq
	if !decodeBody(w, r, &body) {
		return
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	var n int
	_ = agent.db.q.QueryRow(`SELECT count(*) FROM project_repos WHERE project_id=?`, p.ID).Scan(&n)
	if n >= 20 {
		xbin.WriteError(w, 400, "a project has at most 20 repos")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	repo, err := resolveRepo(ctx, c, api, p, body)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if _, ok := agent.db.repoNamed(p.ID, repo.Repo); ok {
		xbin.WriteError(w, 409, repo.Repo+" is one of this project's repos already")
		return
	}
	var jobs []*ProjectJob
	err = agent.db.Tx(func(t *DB) error {
		repo.ProjectID = p.ID
		repo.Slug = t.repoSlug(p.ID, repo.Repo, repo.Slug)
		_, _ = t.q.Exec(`DELETE FROM project_repos WHERE project_id=? AND lower(repo)=lower(?) AND state='removing'`, p.ID, repo.Repo)
		if err := t.insertRepo(repo); err != nil {
			return err
		}
		if p.Dir != "" {
			j, err := t.queueJob(p.ID, 0, repo.Slug, pjRepo, c.tag(), 0)
			if err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		t.touchProject(p.ID)
		emitProject(t, p.ID, "repo", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if jobs == nil {
		jobs = []*ProjectJob{}
	}
	got, _ := agent.db.projectRepo(p.ID, repo.Slug)
	xbin.WriteJSON(w, 201, map[string]any{"repo": got, "jobs": jobs})
}

func handlePatchRepo(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	slug := r.PathValue("slug")
	var body struct {
		Setup    *string `json:"setup"`
		Checkout *string `json:"checkout"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if _, err := agent.db.projectRepo(p.ID, slug); err != nil {
		xbin.WriteError(w, 404, "no such repo in this project")
		return
	}
	cols := []string{}
	args := []any{}
	if body.Setup != nil {
		if len(*body.Setup) > 64<<10 {
			xbin.WriteError(w, 400, "setup: at most 64 KiB")
			return
		}
		cols, args = append(cols, "setup=?"), append(args, *body.Setup)
	}
	if body.Checkout != nil {
		if *body.Checkout != coWorktree && *body.Checkout != coClone {
			xbin.WriteError(w, 400, "checkout: worktree or clone")
			return
		}
		cols, args = append(cols, "checkout=?"), append(args, *body.Checkout)
	}
	if len(cols) > 0 {
		err := agent.db.Tx(func(t *DB) error {
			if _, err := t.q.Exec(`UPDATE project_repos SET `+strings.Join(cols, ", ")+` WHERE project_id=? AND slug=?`,
				append(args, p.ID, slug)...); err != nil {
				return err
			}
			emitProject(t, p.ID, "repo", 0)
			return nil
		})
		if err != nil {
			writeProjErr(w, err)
			return
		}
	}
	got, _ := agent.db.projectRepo(p.ID, slug)
	xbin.WriteJSON(w, 200, map[string]any{"repo": got})
}

// handleRemoveRepo: the repo goes (its base repo stays in the sandbox; new
// tasks stop using it); 409 busy while open tasks work in it, unless force.
func handleRemoveRepo(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	slug := r.PathValue("slug")
	if _, err := agent.db.projectRepo(p.ID, slug); err != nil {
		xbin.WriteError(w, 404, "no such repo in this project")
		return
	}
	force := r.URL.Query().Get("force") == "1"
	var busy []int64
	ks, _ := agent.db.tasksWhere(`WHERE project_id=? AND phase IN ('open','pr') AND run_id<>0`, p.ID)
	for _, k := range ks {
		if hasStr(k.Repos, slug) && k.WS != wsCleaned {
			busy = append(busy, k.N)
		}
	}
	if len(busy) > 0 && !force {
		writeProjErr(w, &projErr{code: 409, refusal: refusalBusy, msg: fmt.Sprintf("open tasks work in %s: %v — close them, or remove it with force=1", slug, busy),
			extra: map[string]any{"tasks": busy}})
		return
	}
	err := agent.db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`DELETE FROM project_repos WHERE project_id=? AND slug=?`, p.ID, slug); err != nil {
			return err
		}
		_, _ = t.q.Exec(`UPDATE project_jobs SET state='failed', error='the repo was removed', updated_ms=?
			WHERE project_id=? AND repo_slug=? AND state IN ('queued','waiting')`, nowMs(), p.ID, slug)
		emitProject(t, p.ID, "repo", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	go scrubProject(p.ID, "", "repo-removed")
	w.WriteHeader(202)
}

// --- status, warm, issues, events ---------------------------------------------------------------

// projectStatusRead: the project's status was read lately (its page is open):
// its repos are fetched every fetchEveryMin meanwhile.
var projectStatusRead = struct {
	sync.Mutex
	m map[int64]int64
}{m: map[int64]int64{}}

func noteStatusRead(pid int64) {
	projectStatusRead.Lock()
	projectStatusRead.m[pid] = nowMs()
	projectStatusRead.Unlock()
}

// statusReadAt is when project pid's status was last read (0: not lately).
func statusReadAt(pid int64) int64 {
	projectStatusRead.Lock()
	defer projectStatusRead.Unlock()
	return projectStatusRead.m[pid]
}

func handleProjectStatus(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	noteStatusRead(p.ID)
	st := ProjectStatus{Repos: []StatusRepo{}, Creds: []StatusCred{}, Warnings: []ProjectWarning{}}
	st.Sandbox.Ref = p.SandboxRef
	if p.SandboxRef != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		if conn, id, err := sbxDialRef(p.SandboxRef, sbxUserOf(binderWho(p.Owner))); err == nil {
			if box, err := conn.Get(ctx, id); err == nil {
				st.Sandbox = StatusSandbox{Ref: p.SandboxRef, Name: box.Name, State: box.State, Workdir: box.Workdir, Shared: !sandboxPrivate(box)}
			} else {
				st.Sandbox.State = "unknown: " + err.Error()
			}
		}
		cancel()
	}
	repos, _ := agent.db.projectRepos(p.ID)
	for _, rp := range repos {
		st.Repos = append(st.Repos, StatusRepo{Slug: rp.Slug, State: rp.State, FetchedMs: rp.FetchedMs, Head: rp.Head, Protected: rp.Protected, Error: rp.Error})
		if rp.Protected != nil && !*rp.Protected {
			st.Warnings = append(st.Warnings, ProjectWarning{Kind: "unprotected", Repo: rp.Repo,
				Text: rp.Repo + "'s default branch has no protection: a token in the sandbox could push to it or merge through the API (policy.protection refuse makes this a refusal)"})
		}
	}
	st.Creds = agent.db.projectCredsView(p.ID)
	st.Jobs = []ProjectJob{}
	for _, j := range agent.db.jobsWhere(`WHERE project_id=? AND (state IN ('queued','running','waiting') OR id IN
		(SELECT id FROM project_jobs WHERE project_id=? AND state IN ('done','failed') ORDER BY id DESC LIMIT 20)) ORDER BY id DESC`, p.ID, p.ID) {
		st.Jobs = append(st.Jobs, *j)
	}
	pol := policyOf(p.Policy)
	st.Slots = ProjectSlots{Used: agent.db.slotsUsed(p.ID), Max: pol.MaxTasks}
	if pol.Engine != "harness" {
		// a built-in task takes a subagent's place at the model-call gate:
		// every slot but the last a top-level conversation may take
		if sub := max(gateLimit(parseConfig(agent.db.getSetting("config")))-1, 1); pol.MaxTasks > sub {
			st.Warnings = append(st.Warnings, ProjectWarning{Kind: "slots", Text: fmt.Sprintf(
				"policy.maxTasks (%d) is more than the model calls built-in tasks may make at once here (%d): they wait for each other", pol.MaxTasks, sub)})
		}
	}
	xbin.WriteJSON(w, 200, st)
}

// projectCredsView is the credential metadata K keeps (project_creds) —
// never a token; none before K's table is in.
func (d *DB) projectCredsView(pid int64) []StatusCred {
	out := []StatusCred{}
	rows, err := d.q.Query(`SELECT sandbox_ref, host, identity, login, state, expires_ms, why FROM project_creds WHERE project_id=?`, pid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c StatusCred
		if rows.Scan(&c.Sandbox, &c.Host, &c.Identity, &c.Login, &c.State, &c.ExpiresMs, &c.Why) == nil {
			out = append(out, c)
		}
	}
	return out
}

// handleWarmProject: start the sandbox, fetch, refresh credentials.
func handleWarmProject(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	if p.State != projActive {
		xbin.WriteError(w, 409, "this project is "+p.State)
		return
	}
	var jobs []*ProjectJob
	err := agent.db.Tx(func(t *DB) error {
		if p.SandboxRef != "" || t.getSetting(sbxNewKey(p.ID)) != "" {
			j, err := t.queueJob(p.ID, 0, "", pjSandbox, c.tag(), 0)
			if err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		if projectJobKinds[pjCreds] != nil && p.Kind != projTeam {
			if j, err := t.queueJob(p.ID, 0, "", pjCreds, c.tag(), 0); err == nil {
				jobs = append(jobs, j)
			}
		}
		repos, _ := t.projectRepos(p.ID)
		for _, rp := range repos {
			kind := pjFetch
			if rp.State != "ready" {
				kind = pjRepo
			}
			if p.Dir == "" {
				continue // the sandbox job queues the repos once it knows where
			}
			j, err := t.queueJob(p.ID, 0, rp.Slug, kind, c.tag(), 0)
			if err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if jobs == nil {
		jobs = []*ProjectJob{}
	}
	xbin.WriteJSON(w, 202, map[string]any{"jobs": jobs})
}

// handleProjectIssues: the issue picker — only the project's own repos,
// through the project's identity; bodies clipped (2 KiB) and redacted.
func handleProjectIssues(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	q := r.URL.Query()
	rp, ok := agent.db.repoNamed(p.ID, q.Get("repo"))
	if !ok {
		xbin.WriteError(w, 400, fmt.Sprintf("repo: %q isn't one of this project's repos", q.Get("repo")))
		return
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sq := scmQuery{Repo: rp.Repo, Q: q.Get("q"), State: orStr(q.Get("state"), "open"), Cursor: q.Get("cursor"), As: projectAs(p), Limit: 30}
	if l := q.Get("labels"); l != "" {
		sq.Labels = strings.Split(l, ",")
	}
	page, err := api.Issues(ctx, sq)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	for i := range page.Items {
		it := &page.Items[i]
		it.Title, it.Body, it.Comments = projRedact(it.Title), clip(projRedact(it.Body), 2<<10), nil
	}
	if page.Items == nil {
		page.Items = []scmIssue{}
	}
	xbin.WriteJSON(w, 200, page)
}

func handleProjectEvents(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	items := agent.db.projectEvents(p.ID, since, limit+1)
	next := ""
	if len(items) > limit {
		items = items[:limit]
		next = strconv.FormatInt(items[len(items)-1].ID, 10)
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

// --- tasks ---------------------------------------------------------------------------------------

func handleListTasks(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	q := r.URL.Query()
	where, args := `WHERE project_id=?`, []any{p.ID}
	if ph := q.Get("phase"); ph != "" {
		where += ` AND phase=?`
		args = append(args, ph)
	} else {
		where += ` AND phase<>'deleted'`
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		where += ` AND (title LIKE ? OR branch LIKE ?)`
		args = append(args, "%"+s+"%", "%"+s+"%")
	}
	if q.Get("mine") == "1" {
		where += ` AND created_by=?`
		args = append(args, c.tag())
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	off, _ := strconv.Atoi(q.Get("cursor"))
	ks, err := agent.db.tasksWhere(where+` ORDER BY updated_ms DESC, n DESC`, args...)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	col := q.Get("col")
	items := []TaskView{}
	skipped := 0
	next := ""
	for _, k := range ks {
		v := agent.db.projTaskView(p, k)
		if col != "" && v.Column != col {
			continue
		}
		if skipped < off {
			skipped++
			continue
		}
		if len(items) == limit {
			next = strconv.Itoa(off + limit)
			break
		}
		items = append(items, v)
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

func handleNewTask(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var s TaskSpec
	if !decodeBody(w, r, &s) {
		return
	}
	if haltBlocks(w, r, 0) {
		return
	}
	k, run, err := agent.createTask(r.Context(), callerOf(r), p, s)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, 201, map[string]any{"task": agent.db.projTaskView(p, k), "run": runAnswer(run)})
}

// handleTaskBatch: tasks from issues (≤ 20), each its own task; a repo
// that isn't the project's refuses the whole batch (400).
func handleTaskBatch(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var body struct {
		Issues []IssueRef `json:"issues"`
		Size   string     `json:"size"`
		Agent  *TaskAgent `json:"agent"`
		Text   string     `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if len(body.Issues) == 0 || len(body.Issues) > 20 {
		xbin.WriteError(w, 400, "issues: 1 to 20 of {repo, number}")
		return
	}
	for _, is := range body.Issues {
		if _, ok := agent.db.repoNamed(p.ID, is.Repo); !ok {
			xbin.WriteError(w, 400, fmt.Sprintf("issues: %q isn't one of this project's repos", is.Repo))
			return
		}
	}
	if haltBlocks(w, r, 0) {
		return
	}
	tasks := []TaskView{}
	errs := []map[string]any{}
	for _, is := range body.Issues {
		ir := is
		k, _, err := agent.createTask(r.Context(), callerOf(r), p, TaskSpec{Text: body.Text, Size: body.Size, Agent: body.Agent, Issue: &ir})
		if err != nil {
			errs = append(errs, map[string]any{"issue": is, "error": err.Error()})
			continue
		}
		tasks = append(tasks, agent.db.projTaskView(p, k))
	}
	xbin.WriteJSON(w, 201, map[string]any{"tasks": tasks, "errors": errs})
}

// taskOfPath is {n} of the route's project.
func taskOfPath(w http.ResponseWriter, r *http.Request) (*Project, *ProjectTask, bool) {
	p, _ := projectOf(r)
	n, _ := strconv.ParseInt(r.PathValue("n"), 10, 64)
	k, err := agent.db.taskByN(p.ID, n)
	if err != nil {
		xbin.WriteError(w, 404, "no such task in this project")
		return nil, nil, false
	}
	return p, k, true
}

func handleGetTask(w http.ResponseWriter, r *http.Request) {
	p, k, ok := taskOfPath(w, r)
	if ok {
		xbin.WriteJSON(w, 200, agent.db.taskAnswer(p, k, callerOf(r)))
	}
}

func handleCancelTask(w http.ResponseWriter, r *http.Request) {
	p, k, ok := taskOfPath(w, r)
	if !ok {
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if err := agent.cancelTask(p, k, callerOf(r), clip(body.Reason, 500)); err != nil {
		writeProjErr(w, err)
		return
	}
	k, _ = agent.db.taskByN(p.ID, k.N)
	xbin.WriteJSON(w, 200, agent.db.projTaskView(p, k))
}

// runTask is the task a /runs/{id}/task route's conversation is (404 for a
// run that isn't one).
func runTask(w http.ResponseWriter, r *http.Request) (*Project, *ProjectTask, bool) {
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return nil, nil, false
	}
	p, k := agent.db.projectOfRun(run)
	if k == nil {
		xbin.WriteError(w, 404, "this conversation is no project's task")
		return nil, nil, false
	}
	return p, k, true
}

func handleRunTask(w http.ResponseWriter, r *http.Request) {
	if p, k, ok := runTask(w, r); ok {
		xbin.WriteJSON(w, 200, agent.db.taskAnswer(p, k, callerOf(r)))
	}
}

// handleTaskAct: POST /runs/{id}/task/refresh | retry | close.
func handleTaskAct(act string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, k, ok := runTask(w, r)
		if !ok {
			return
		}
		c := callerOf(r)
		var o ActOpts
		if !decodeBody(w, r, &o) {
			return
		}
		if p.State != projActive {
			xbin.WriteError(w, 409, "this project is "+p.State)
			return
		}
		var err error
		switch act {
		case "refresh":
			err = agent.refreshTask(p, k, c)
		case "retry":
			err = agent.retryTask(p, k, c)
		case "close":
			if err = agent.closeTask(r.Context(), p, k, c, o); err == nil {
				k, _ = agent.db.taskByN(p.ID, k.N)
				xbin.WriteJSON(w, 200, agent.db.projTaskView(p, k))
				return
			}
		}
		if err != nil {
			writeProjErr(w, err)
			return
		}
		xbin.WriteJSON(w, 202, map[string]any{"ok": true})
	}
}

// handleTaskCleanup: the task's worktrees and branch go — refused (409
// dirty) while a checkout has uncommitted or unpushed work, unless force,
// which only the conversation's owner sends (the route's need).
func handleTaskCleanup(w http.ResponseWriter, r *http.Request) {
	p, k, ok := runTask(w, r)
	if !ok {
		return
	}
	var o ActOpts
	if !decodeBody(w, r, &o) {
		return
	}
	if k.WS == wsCleaned {
		xbin.WriteJSON(w, 202, map[string]any{"ok": true})
		return
	}
	if !o.Force && k.WS != wsPending {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		dirty, err := taskDirty(ctx, p, k)
		cancel()
		if err != nil {
			writeProjErr(w, err)
			return
		}
		if len(dirty) > 0 {
			writeProjErr(w, &projErr{code: 409, refusal: refusalDirty, msg: "this task has work that isn't pushed: push it, or clean up with force",
				extra: map[string]any{"repos": dirty}})
			return
		}
	}
	err := agent.db.Tx(func(t *DB) error {
		j, err := t.queueJob(p.ID, k.ID, "", pjCleanup, callerOf(r).tag(), 0)
		if err == nil && o.Force {
			_, err = t.q.Exec(`UPDATE project_jobs SET client_id='force' WHERE id=?`, j.ID)
		}
		return err
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, 202, map[string]any{"ok": true})
}
