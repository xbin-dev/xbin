// project_routes.go — Projects' routes (API.md §Projects and tasks, §The
// workspace), mounted from routeTables through the chain every route takes.
// A project is named {pid} (never {id}: at the global instance hostedRoute
// intercepts every {id} pattern); guard doesn't resolve it, so each project
// route's handler is wrapped in projectNeed, which loads the project and
// the caller's level on its ACL (404 below viewer, 403 below the need).
//
// Where a home's identity at the scm provider is the bot — a partitioned
// agent's global instance, an unpartitioned agent — naming a repo takes the
// agent's manager or its scm bot rule (scmBotAllowed); every later read is
// held to the project's own repos.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() { routeTables = append(routeTables, projectRoutes) }

func projectRoutes() []routeDef {
	return []routeDef{
		{"GET /projects", needAny, handleListProjects},
		{"POST /projects", needStart, handleNewProject},
		{"GET /projects/{pid}", needAny, projectNeed(lvViewer, handleGetProject)},
		{"PATCH /projects/{pid}", needAny, projectNeed(lvOwner, handlePatchProject)},
		{"DELETE /projects/{pid}", needAny, projectNeed(lvOwner, handleDeleteProject)},
		{"GET /projects/{pid}/members", needAny, projectNeed(lvViewer, handleProjectMembers)},
		{"POST /projects/{pid}/members", needAny, projectNeed(lvOwner, handleAddProjectMember)},
		{"DELETE /projects/{pid}/members/{user}", needAny, projectNeed(lvViewer, handleRemoveProjectMember)},
		{"POST /projects/{pid}/repos", needAny, projectNeed(lvOwner, handleAddRepo)},
		{"PATCH /projects/{pid}/repos/{slug}", needAny, projectNeed(lvOwner, handlePatchRepo)},
		{"DELETE /projects/{pid}/repos/{slug}", needAny, projectNeed(lvOwner, handleRemoveRepo)},
		{"GET /projects/{pid}/status", needAny, projectNeed(lvViewer, handleProjectStatus)},
		{"POST /projects/{pid}/warm", needAny, projectNeed(lvParticipant, handleWarmProject)},
		{"GET /projects/{pid}/issues", needAny, projectNeed(lvViewer, handleProjectIssues)},
		{"GET /projects/{pid}/tasks", needAny, projectNeed(lvViewer, handleListTasks)},
		{"POST /projects/{pid}/tasks", needAny, projectNeed(lvParticipant, handleNewTask)},
		{"POST /projects/{pid}/tasks/batch", needAny, projectNeed(lvParticipant, handleTaskBatch)},
		{"GET /projects/{pid}/tasks/{n}", needAny, projectNeed(lvViewer, handleGetTask)},
		{"POST /projects/{pid}/tasks/{n}/cancel", needAny, projectNeed(lvParticipant, handleCancelTask)},
		{"GET /projects/{pid}/events", needAny, projectNeed(lvViewer, handleProjectEvents)},
		{"GET /runs/{id}/task", needViewer, handleRunTask},
		{"POST /runs/{id}/task/refresh", needParticipant, handleTaskAct("refresh")},
		{"POST /runs/{id}/task/retry", needParticipant, handleTaskAct("retry")},
		{"POST /runs/{id}/task/close", needParticipant, handleTaskAct("close")},
		{"POST /runs/{id}/task/cleanup", needOwner, handleTaskCleanup},
	}
}

// projCtx carries projectNeed's resolution to the handler.
type projCtx struct {
	p  *Project
	lv level
}

type projCtxKey struct{}

// projectNeed wraps a {pid} route: the project, and the caller at lv or
// above on its ACL (404 when they may not see it, 403 below lv). A project
// being deleted answers only reads.
func projectNeed(lv level, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pid, _ := strconv.ParseInt(r.PathValue("pid"), 10, 64)
		p, err := projAg().db.getProject(pid)
		if err != nil {
			xbin.WriteError(w, 404, "no such project")
			return
		}
		c := callerOf(r)
		have := projAg().db.projectLevel(c, pid)
		switch {
		case have < lvViewer:
			xbin.WriteError(w, 404, "no such project")
			return
		case have < lv:
			xbin.WriteError(w, 403, fmt.Sprintf("you are a %s of this project; that needs %s", have, lv))
			return
		case p.State == projDeleting && r.Method != http.MethodGet:
			xbin.WriteError(w, 409, "this project is being deleted")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), projCtxKey{}, projCtx{p: p, lv: have})))
	}
}

func projectOf(r *http.Request) (*Project, level) {
	pc, _ := r.Context().Value(projCtxKey{}).(projCtx)
	return pc.p, pc.lv
}

// botHome: this home's identity at an scm provider is the bot (the global
// instance, an unpartitioned agent): naming a repo takes scmBotAllowed.
func botHome() bool { return !userMode() }

// botRefusal says why w may not name repo for the bot here ("" = may).
func botRefusal(w who, repo string) string {
	if !botHome() || scmBotAllowed(w, repo) {
		return ""
	}
	return fmt.Sprintf("naming %s for the bot takes a manager, or the agent's scm bot rule", repo)
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(v); err != nil && err != io.EOF {
		xbin.WriteError(w, 400, "bad JSON: "+err.Error())
		return false
	}
	return true
}

// --- the project view -----------------------------------------------------------------------

// projectView is GET /projects/{pid}'s answer for a caller at level lv.
func (d *DB) projectView(p *Project, lv level) ProjectView {
	v := ProjectView{Project: *p, Level: lv.String(), Counts: map[string]int{colQueued: 0, colWorking: 0, colNeedsYou: 0, colPR: 0, colDone: 0}}
	v.Policy = policyView(p.Policy)
	v.Repos, _ = d.projectRepos(p.ID)
	if v.Repos == nil {
		v.Repos = []ProjectRepo{}
	}
	queued := d.queuedInputs(p.ID, 0)
	ks, _ := d.tasksWhere(`WHERE project_id=? AND phase<>'deleted'`, p.ID)
	for _, k := range ks {
		status := ""
		if k.RunID != 0 {
			_ = d.q.QueryRow(`SELECT status FROM runs WHERE id=?`, k.RunID).Scan(&status)
		}
		col, _, _ := taskDerived(k, status, taskCISummary(k.RunID), queueWaits(queued, k.N))
		v.Counts[col]++
	}
	v.Slots = ProjectSlots{Used: d.slotsUsed(p.ID), Max: policyOf(p.Policy).MaxTasks}
	return v
}

// projectWhere is the SQL form of "w may see the project" over projects p.
func projectWhere(w who) (string, []any) {
	switch w.kind {
	case whoSystem:
		return "1=1", nil
	case whoElement:
		return "p.owner=?", []any{"el:" + w.el}
	case whoUser:
		if w.viewedBy != "" {
			return "p.visibility='team'", nil
		}
		q := "(p.owner=? OR p.visibility='team' OR EXISTS (SELECT 1 FROM project_members m WHERE m.project_id=p.id AND m.user=?))"
		args := []any{w.user, w.user}
		if w.manager() {
			q = "(p.owner='' OR " + q[1:]
		}
		return q, args
	}
	return "0=1", nil
}

func handleListProjects(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	where, args := projectWhere(c)
	q := r.URL.Query()
	if st := q.Get("state"); st != "" {
		where += " AND p.state=?"
		args = append(args, st)
	} else {
		where += " AND p.state<>'deleting'"
	}
	if k := q.Get("kind"); k != "" {
		where += " AND p.kind=?"
		args = append(args, k)
	}
	off, _ := strconv.Atoi(q.Get("cursor"))
	const page = 50
	ps, err := projAg().db.projectsWhere(`p WHERE `+where+` ORDER BY p.updated_ms DESC, p.id DESC LIMIT ? OFFSET ?`,
		append(args, page+1, max(off, 0))...)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	next := ""
	if len(ps) > page {
		ps, next = ps[:page], strconv.Itoa(off+page)
	}
	items := []ProjectView{}
	for _, p := range ps {
		items = append(items, projAg().db.projectView(p, projAg().db.projectLevel(c, p.ID)))
	}
	xbin.WriteJSON(w, 200, map[string]any{"items": items, "next": next})
}

func handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, lv := projectOf(r)
	xbin.WriteJSON(w, 200, map[string]any{"project": projAg().db.projectView(p, lv)})
}

// --- creating a project -------------------------------------------------------------------

// newRepoReq is one repo a request names.
type newRepoReq struct {
	Repo  string `json:"repo"`
	Slug  string `json:"slug"`
	Setup string `json:"setup"`
}

// sandboxNew is a workspace sandbox to create (POST /projects sandbox.new).
type sandboxNew struct {
	Provider string `json:"provider"`
	Image    string `json:"image,omitempty"`
	Size     string `json:"size,omitempty"`
	Egress   string `json:"egress,omitempty"`
}

type newProjectBody struct {
	Name    string       `json:"name"`
	SCM     string       `json:"scm"`
	Repos   []newRepoReq `json:"repos"`
	Sandbox *struct {
		Ref string      `json:"ref"`
		New *sandboxNew `json:"new"`
	} `json:"sandbox"`
	Policy json.RawMessage `json:"policy"`
	Share  *shareSpec      `json:"share"`
	Kind   string          `json:"kind"`
}

// sbxNewKey is the setting a project's sandbox to create waits in until
// its sandbox job made it.
func sbxNewKey(pid int64) string { return "proj_sbx_new:" + strconv.FormatInt(pid, 10) }

func handleNewProject(w http.ResponseWriter, r *http.Request) {
	c := callerOf(r)
	var body newProjectBody
	if !decodeBody(w, r, &body) {
		return
	}
	body.Name = strings.TrimSpace(body.Name)
	kind := orStr(body.Kind, projPersonal)
	switch {
	case body.Name == "" || len([]rune(body.Name)) > 80:
		xbin.WriteError(w, 400, "need {name} (at most 80 characters)")
		return
	case kind != projPersonal && kind != projTeam:
		xbin.WriteError(w, 400, `kind: "team" (or none)`)
		return
	case globalMode() && kind != projTeam:
		xbin.WriteError(w, 409, "the agent's shared space keeps team projects only: send kind \"team\" (with share), or create a personal project in your own space")
		return
	case !globalMode() && kind == projTeam:
		xbin.WriteError(w, 409, "a team project is made in the agent's shared space (?xbin-partition=global)")
		return
	case userMode() && body.Share.shared():
		xbin.WriteError(w, 409, "a project in your own space is yours alone: it can't be shared from here — make a team project in the shared space")
		return
	case globalMode() && c.kind == whoUser && !body.Share.shared():
		xbin.WriteError(w, 409, "the agent's shared space keeps shared projects: say who shares this one (share: the team, or people)")
		return
	case len(body.Repos) > 20:
		xbin.WriteError(w, 400, "repos: at most 20")
		return
	}
	if err := body.Share.check(c.tag()); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if len(body.Policy) == 0 {
		body.Policy = json.RawMessage("{}")
	}
	if why := checkPolicy(body.Policy); why != "" {
		xbin.WriteError(w, 400, why)
		return
	}
	if haltBlocks(w, r, 0) {
		return
	}
	api, err := scmFor(body.SCM)
	if err != nil {
		xbin.WriteError(w, 400, fmt.Sprintf("scm: %q isn't an scm provider bound to this agent (GET /projects/scm lists them)", body.SCM))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	p := &Project{Name: body.Name, Kind: kind, Owner: c.tag(), Visibility: visPrivate, TeamRole: roleViewer,
		SCM: api.Provider(), Policy: body.Policy, State: projActive, CreatedBy: c.tag()}
	if body.Share != nil && body.Share.Visibility == visTeam {
		st := runStamp{}
		body.Share.stamp(&st)
		p.Visibility, p.TeamRole = st.Visibility, st.TeamRole
	}
	var sbxNewReq *sandboxNew
	switch {
	case body.Sandbox != nil && body.Sandbox.Ref != "":
		if err := checkProjectSandbox(ctx, c, body.Sandbox.Ref); err != nil {
			writeProjErr(w, err)
			return
		}
		p.SandboxRef = body.Sandbox.Ref
	case body.Sandbox != nil && body.Sandbox.New != nil:
		if _, ok := boundManager(body.Sandbox.New.Provider); !ok {
			xbin.WriteError(w, 400, fmt.Sprintf("sandbox.new.provider: %q isn't a sandbox manager bound to this agent", body.Sandbox.New.Provider))
			return
		}
		sbxNewReq = body.Sandbox.New
	case kind != projTeam:
		xbin.WriteError(w, 400, "need {sandbox: {ref} or {new: {provider}}}: the project's workspace")
		return
	}
	pol := policyOf(p.Policy)
	provider := ""
	if p.SandboxRef != "" {
		provider, _, _ = splitSandboxRef(p.SandboxRef)
	} else if sbxNewReq != nil {
		provider = sbxNewReq.Provider
	}
	probe := &Project{SandboxRef: sandboxRef(provider, "x")}
	if provider == "" {
		probe = nil
	}
	if _, err := taskClassFor(c, probe, pol.TaskClass); err != nil {
		writeProjErr(w, err)
		return
	}
	hello, err := api.Hello(ctx)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if len(hello.Hosts) > 0 {
		p.Host = hello.Hosts[0]
	}
	var repos []ProjectRepo
	for _, rq := range body.Repos {
		repo, err := resolveRepo(ctx, c, api, p, rq)
		if err != nil {
			writeProjErr(w, err)
			return
		}
		for _, have := range repos {
			if strings.EqualFold(have.Repo, repo.Repo) {
				xbin.WriteError(w, 400, fmt.Sprintf("repos: %s twice", repo.Repo))
				return
			}
		}
		repos = append(repos, *repo)
	}
	var jobs []*ProjectJob
	err = projAg().db.Tx(func(t *DB) error {
		p.Slug = t.projectSlug(p.Name)
		if err := t.insertProject(p); err != nil {
			return err
		}
		if body.Share != nil {
			for _, m := range body.Share.Members {
				if _, err := t.q.Exec(`INSERT OR REPLACE INTO project_members (project_id, user, role, added_by, created_ms)
					VALUES (?, ?, ?, ?, ?)`, p.ID, m.User, orStr(m.Role, roleParticipant), c.tag(), nowMs()); err != nil {
					return err
				}
			}
		}
		for i := range repos {
			repos[i].ProjectID = p.ID
			repos[i].Slug = t.repoSlug(p.ID, repos[i].Repo, repos[i].Slug)
			if err := t.insertRepo(&repos[i]); err != nil {
				return err
			}
		}
		if sbxNewReq != nil {
			b, _ := json.Marshal(sbxNewReq)
			if err := t.putSetting(sbxNewKey(p.ID), string(b)); err != nil {
				return err
			}
		}
		if p.SandboxRef != "" || sbxNewReq != nil {
			j, err := t.queueJob(p.ID, 0, "", pjSandbox, c.tag(), 0)
			if err != nil {
				return err
			}
			jobs = append(jobs, j)
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": "project created", "by": c.tag()}, false, "")
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if jobs == nil {
		jobs = []*ProjectJob{}
	}
	p, _ = projAg().db.getProject(p.ID)
	xbin.WriteJSON(w, 201, map[string]any{"project": projAg().db.projectView(p, projAg().db.projectLevel(c, p.ID)), "jobs": jobs})
}

// checkProjectSandbox: w may make sandbox ref a project's workspace — it is
// theirs to use (in a person's partition, homed there) and offers commands
// and files.
func checkProjectSandbox(ctx context.Context, w who, ref string) error {
	conn, id, err := sbxDialRef(ref, sbxUserOf(w))
	if err != nil {
		return err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return err
	}
	if why := partitionBoxRefusal(box); why != "" {
		return perr(403, "%s", why)
	}
	if !sandboxAccess(w, box).Use {
		return perr(403, "you may not use this sandbox (%s) — its owner can add you as a member", box.Name)
	}
	if !box.hasCap("exec") || !box.hasCap("files") {
		return perr(409, "this sandbox offers no commands or files")
	}
	return nil
}

// resolveRepo checks a repo a request names — its shape, the bot rule at a
// bot home, and that the provider can see it as the project — and answers
// its row (not yet stored). A repo the provider can't see is 400 with its
// refusal.
func resolveRepo(ctx context.Context, w who, api scmAPI, p *Project, rq newRepoReq) (*ProjectRepo, error) {
	rq.Repo = strings.TrimSpace(strings.TrimSuffix(rq.Repo, ".git"))
	if !validRepo(rq.Repo) {
		return nil, perr(400, "repos: %q isn't owner/name", rq.Repo)
	}
	if rq.Slug != "" && slugOf(rq.Slug, 40) != rq.Slug {
		return nil, perr(400, "repos: slug %q: [a-z0-9-], at most 40", rq.Slug)
	}
	if len(rq.Setup) > 64<<10 {
		return nil, perr(400, "repos: a setup script is at most 64 KiB")
	}
	if why := botRefusal(w, rq.Repo); why != "" {
		return nil, perr(403, "%s", why)
	}
	info, err := api.Repo(ctx, rq.Repo, projectAs(p))
	if err != nil {
		var se *scmError
		if errors.As(err, &se) && se.Refusal == scmRefNotFound {
			cp := *se
			cp.Status = 400
			cp.Message = fmt.Sprintf("%s: the scm provider can't see it (%s)", rq.Repo, se.Message)
			return nil, &cp
		}
		return nil, err
	}
	repo := &ProjectRepo{Repo: orStr(info.Owner+"/"+info.Name, rq.Repo), URL: info.CloneURL, DefaultBranch: info.DefaultBranch,
		Protected: info.Protected, Setup: rq.Setup, Slug: rq.Slug, Mode: repoBare, Checkout: policyOf(p.Policy).Checkout}
	if info.Owner == "" || info.Name == "" {
		repo.Repo = rq.Repo
	}
	if repo.URL == "" {
		return nil, perr(502, "the scm provider gave no clone URL for %s", rq.Repo)
	}
	return repo, nil
}

// --- changing a project ---------------------------------------------------------------------------

type patchProjectBody struct {
	Version    int64           `json:"version"`
	Name       *string         `json:"name"`
	Policy     json.RawMessage `json:"policy"`
	Visibility *string         `json:"visibility"`
	TeamRole   *string         `json:"teamRole"`
	State      *string         `json:"state"`
}

func handlePatchProject(w http.ResponseWriter, r *http.Request) {
	p, lv := projectOf(r)
	c := callerOf(r)
	var body patchProjectBody
	if !decodeBody(w, r, &body) {
		return
	}
	if body.Version != p.Version {
		xbin.WriteJSON(w, 412, map[string]any{"error": "the project changed since you read it: read it again", "version": p.Version})
		return
	}
	if sharesInPartition(body.Visibility, body.TeamRole) {
		xbin.WriteError(w, 409, "a project in your own space is yours alone: it can't be shared from here")
		return
	}
	cols := map[string]any{}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if n == "" || len([]rune(n)) > 80 {
			xbin.WriteError(w, 400, "name: 1 to 80 characters")
			return
		}
		cols["name"] = n
	}
	if len(body.Policy) > 0 && string(body.Policy) != "null" {
		merged, err := mergePolicy(p.Policy, body.Policy)
		if err != nil {
			xbin.WriteError(w, 400, err.Error())
			return
		}
		if why := checkPolicy(merged); why != "" {
			xbin.WriteError(w, 400, why)
			return
		}
		if np, op := policyOf(merged), policyOf(p.Policy); np.TaskClass != op.TaskClass {
			if _, err := taskClassFor(c, p, np.TaskClass); err != nil {
				writeProjErr(w, err)
				return
			}
		}
		cols["policy"] = string(merged)
	}
	if body.Visibility != nil {
		if *body.Visibility != visPrivate && *body.Visibility != visTeam {
			xbin.WriteError(w, 400, "visibility: private or team")
			return
		}
		cols["visibility"] = *body.Visibility
	}
	if body.TeamRole != nil {
		if *body.TeamRole != roleViewer && *body.TeamRole != roleParticipant {
			xbin.WriteError(w, 400, "teamRole: viewer or participant")
			return
		}
		cols["team_role"] = *body.TeamRole
	}
	archive := false
	if body.State != nil {
		switch *body.State {
		case projActive, projArchived:
			archive = *body.State == projArchived && p.State != projArchived
			cols["state"] = *body.State
		default:
			xbin.WriteError(w, 400, `state: "active" or "archived"`)
			return
		}
	}
	var runs []int64
	sharing := body.Visibility != nil || body.TeamRole != nil
	err := projAg().db.Tx(func(t *DB) error {
		var sets []string
		var args []any
		for k, v := range cols {
			sets = append(sets, k+"=?")
			args = append(args, v)
		}
		sets = append(sets, "version=version+1", "updated_ms=?")
		args = append(args, nowMs(), p.ID, p.Version)
		res, err := t.q.Exec(`UPDATE projects SET `+strings.Join(sets, ", ")+` WHERE id=? AND version=?`, args...)
		if err != nil {
			return err
		}
		if rowsAffected(res) != 1 {
			return &projErr{code: 412, msg: "the project changed since you read it: read it again"}
		}
		if sharing {
			if runs, err = t.copyACLToTasks(p.ID); err != nil {
				return err
			}
		}
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if sharing {
		projAg().afterACLChange(p.ID, runs)
	}
	if archive {
		go scrubProject(p.ID, "", "archive")
	}
	go projectPump(p.ID)
	np, _ := projAg().db.getProject(p.ID)
	xbin.WriteJSON(w, 200, map[string]any{"project": projAg().db.projectView(np, lv)})
}

// scrubProject scrubs a project's credentials (ref "": every sandbox),
// off any request's context.
func scrubProject(pid int64, ref, why string) {
	p, err := projAg().db.getProject(pid)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := scmScrubCreds(ctx, p, ref, why); err != nil {
		logf("project %d: scrubbing its credentials (%s): %v", pid, why, err)
	}
}

// handleDeleteProject: DELETE /projects/{pid}?sandbox=keep|delete — the
// project goes to deleting; the worker scrubs, cleans every task up,
// deletes the tasks' conversations and (sandbox=delete, a sandbox it made)
// its sandbox; the rows go when that is done.
func handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	sbx := orStr(r.URL.Query().Get("sandbox"), "keep")
	if sbx != "keep" && sbx != "delete" {
		xbin.WriteError(w, 400, "sandbox: keep or delete")
		return
	}
	if p.State == projDeleting {
		xbin.WriteJSON(w, 202, map[string]any{"state": projDeleting})
		return
	}
	err := projAg().db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`UPDATE projects SET state=?, version=version+1, updated_ms=? WHERE id=?`, projDeleting, nowMs(), p.ID); err != nil {
			return err
		}
		if sbx == "delete" && p.SandboxMade {
			if err := t.putSetting("proj_sbx_delete:"+strconv.FormatInt(p.ID, 10), "1"); err != nil {
				return err
			}
		}
		_, _ = t.q.Exec(`DELETE FROM project_queue WHERE project_id=?`, p.ID)
		_, _ = t.q.Exec(`UPDATE project_jobs SET state='failed', error='the project is being deleted', updated_ms=?
			WHERE project_id=? AND state IN ('queued','waiting') AND kind<>?`, nowMs(), p.ID, pjCleanup)
		if _, err := t.queueJob(p.ID, 0, "", pjCleanup, c.tag(), 0); err != nil {
			return err
		}
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	xbin.WriteJSON(w, 202, map[string]any{"state": projDeleting})
}
