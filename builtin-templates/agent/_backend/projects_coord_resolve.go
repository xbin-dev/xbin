// projects_coord_resolve.go — the coordinator (API.md §The coordinator): a
// person's conversation that creates and steers a project's tasks. One per
// person per project, made when they first ask for it (POST
// /projects/{pid}/coordinator): a run with origin project, its session key
// proj:<pid>:coord:<user>, owned by the person, private, Config.Project
// {id, role: coordinator}, in the built-in web class (the web lane — it
// steers tasks that have egress, so it never holds internal reach), its
// web, schedule and skill-writing tools denied unless the policy says
// otherwise.
//
// The resolver is the coordinator's boundary: coordinatorOf says whether a
// run is a project's live coordinator (depth 0, its role read through
// projectRefOf, its session key naming its project and owner, the project
// active, the owner still a participant, no internal reach), and
// projectTaskOf finds a task by its project-local number — never a run id,
// never a ParentID walk — refusing one whose run has internal reach anyway.
// The tools (projects_coord_tools.go) go through both on every call.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, coordRoutes)
	runStatusHooks = append(runStatusHooks, coordHoldPark)
}

func coordRoutes() []routeDef {
	return []routeDef{
		{"POST /projects/{pid}/coordinator", needAny, projectNeed(lvParticipant, handleCoordinator)},
		{"GET /projects/{pid}/needs", needAny, projectNeed(lvViewer, handleProjectNeeds)},
	}
}

// coordDeny are the tools a coordinator never has; coordWebTools only while
// its project's policy.coordinator.web is off.
var (
	coordDeny     = []string{"schedule", "unschedule", "skill_manage"}
	coordWebTools = []string{"web_search", "web_fetch"}
)

// coordKeyOf is the project and the person a coordinator's session key
// names (ok false: it names none).
func coordKeyOf(key string) (pid int64, user string, ok bool) {
	rest, found := strings.CutPrefix(key, "proj:")
	if !found {
		return 0, "", false
	}
	id, user, found := strings.Cut(rest, ":coord:")
	if !found || user == "" {
		return 0, "", false
	}
	pid, err := strconv.ParseInt(id, 10, 64)
	if err != nil || pid <= 0 {
		return 0, "", false
	}
	return pid, user, true
}

// coordRunOf is the id of user's coordinator of project pid (0: none yet).
func (d *DB) coordRunOf(pid int64, user string) int64 {
	var id int64
	_ = d.q.QueryRow(`SELECT id FROM runs WHERE session_key=? AND origin=? AND origin_id=? AND owner=? AND parent_id=0
		ORDER BY id LIMIT 1`, coordSessionKey(pid, user), originProject, pid, user).Scan(&id)
	return id
}

// coordWho is the person a coordinator acts for, as a caller: their own
// tile level isn't known outside a request, so it is "read" — a class only
// managers may use is refused to a coordinator's tasks (fail closed).
func coordWho(user string) who { return who{kind: whoUser, user: user, level: "read"} }

// errCoordClassInternal: the web class has internal reach (an edit made it
// so) — no coordinator is made in it, and none acts while it has.
var errCoordClassInternal = errors.New("the coordinator's class (the built-in web class) has internal reach: a coordinator steers tasks that reach outside, so it never holds internal reach — a manager should take internal reach off that class")

// coordinatorOf is run's project when run is a live coordinator of it, at
// depth 0 (its subagents get no project tools): its role read through
// projectRefOf (never the stored field alone), its session key naming that
// project and the run's owner, the project active, the owner still a
// participant of it, and its class without internal reach (the firewall
// rule, checked again on every call). The error is what a tool answers.
func (ag *Agent) coordinatorOf(t *DB, run *Run, cfg Config) (*Project, error) {
	if run == nil || run.Depth != 0 || run.ParentID != 0 || run.Origin != originProject || hostedID(run.ID) {
		return nil, errors.New("the project tools are a project coordinator's own")
	}
	ref := t.projectRefOf(run)
	if !ref.isCoordinator() {
		return nil, errors.New("the project tools are a project coordinator's own")
	}
	pid, user, ok := coordKeyOf(run.SessionKey)
	if !ok || pid != ref.ID || pid != run.OriginID || user != run.Owner {
		return nil, errors.New("this conversation isn't a project's coordinator")
	}
	p, err := t.getProject(pid)
	if err != nil {
		return nil, errors.New("this coordinator's project is gone")
	}
	switch {
	case p.State != projActive:
		return nil, fmt.Errorf("the project %s is %s: its coordinator acts on nothing until it is active again", p.Name, p.State)
	case p.Kind == projTeam:
		return nil, errors.New("a team project's definition has no tasks: its members' coordinators work in their own spaces")
	case t.projectLevel(coordWho(user), pid) < lvParticipant:
		return nil, fmt.Errorf("%s no longer takes part in the project %s: their coordinator acts on nothing", user, p.Name)
	case classOf(cfg).has(tsInternal):
		return nil, errCoordClassInternal
	}
	return p, nil
}

// projectTaskOf is task n of p and its run (nil once its conversation was
// deleted) — by the project-local number alone. A task whose run has
// internal reach anyway (a run an older build made, a class edited in the
// database) is refused, as Engine.node refuses a run in another lane.
func (d *DB) projectTaskOf(p *Project, n int64) (*ProjectTask, *Run, error) {
	if n <= 0 {
		return nil, nil, errors.New("name a task by its number in this project (task_list lists them)")
	}
	k, err := d.taskByN(p.ID, n)
	if err != nil {
		return nil, nil, fmt.Errorf("there is no task #%d in this project (task_list lists them)", n)
	}
	if k.RunID == 0 {
		return k, nil, nil
	}
	run, err := d.getRun(k.RunID)
	if err != nil {
		return k, nil, nil
	}
	if run.ParentID != 0 || run.Origin != originProject || run.OriginID != p.ID {
		return nil, nil, fmt.Errorf("task #%d's conversation isn't this project's", n)
	}
	cfg, err := d.runConfig(run.ID)
	if err != nil {
		return nil, nil, fmt.Errorf("task #%d: %v", n, err)
	}
	if coordRawInternal(cfg) {
		return nil, nil, fmt.Errorf("task #%d is in a different capability lane (its class has internal reach): the coordinator can't reach it", n)
	}
	return k, run, nil
}

// coordRawInternal: cfg's class as stored — before clampTo and projectClamp,
// which hold a project run's class only while its config says it is one —
// has internal reach (a config with no class: its lane's built-in).
func coordRawInternal(cfg Config) bool {
	st := currentClasses()
	c, ok := st.find(cfg.Class)
	if !ok {
		c, _ = st.find(laneClass(cfg.Toolset))
	}
	return c.has(tsInternal)
}

// --- making one --------------------------------------------------------------------------

// ensureCoordinator is w's coordinator of p, made now when they have none
// (made: true). In one transaction, so two first uses make one; the
// project events written before it are marked delivered — it starts from
// the project as it stands (its # Project block, task_list), not from a
// backlog it never asked for.
func (ag *Agent) ensureCoordinator(w who, p *Project) (run *Run, made bool, err error) {
	switch {
	case w.kind != whoUser || w.viewedBy != "":
		return nil, false, perr(403, "a coordinator is a person's own: ask from your own account")
	case p.Kind == projTeam:
		return nil, false, perr(409, "a team project's definition has no tasks and no coordinator: work on it from your own space (its membership)")
	case p.State != projActive:
		return nil, false, perr(409, "this project is %s: its coordinator starts when it is active", p.State)
	}
	cls, ok := currentClasses().find(classWeb)
	if !ok || cls.has(tsInternal) {
		return nil, false, &projErr{code: 409, refusal: refusalClassInternal, msg: errCoordClassInternal.Error()}
	}
	pol := policyOf(p.Policy)
	cfg := parseConfig(ag.db.getSetting("config"))
	cfg.setClass(cls, false)
	cfg.Project = &ProjectRef{ID: p.ID, Role: projRoleCoordinator}
	cfg.Deny = append(append([]string(nil), cfg.Deny...), coordDeny...)
	if !pol.Coordinator.Web {
		cfg.Deny = append(cfg.Deny, coordWebTools...)
	}
	if m := strings.TrimSpace(pol.Coordinator.Model); m != "" && validPick(m) {
		cfg.Pick = m
	}
	var id int64
	err = ag.db.Tx(func(t *DB) error {
		if id = t.coordRunOf(p.ID, w.user); id != 0 {
			return nil
		}
		var err error
		id, err = ag.startRunTx(t, runOpts{Title: "Coordinator · " + p.Name, Cfg: cfg, Hold: true, Stamp: runStamp{
			Owner: w.user, Visibility: visPrivate, TeamRole: roleViewer, Origin: originProject, OriginID: p.ID,
			SessionKey: coordSessionKey(p.ID, w.user), TitleSrc: "origin"}})
		if err != nil {
			return err
		}
		made = true
		_, err = t.q.Exec(`UPDATE project_events SET delivered=? WHERE project_id=? AND coord_user=? AND delivered=0`,
			nowMs(), p.ID, w.user)
		return err
	})
	if err != nil {
		return nil, false, err
	}
	run, err = ag.db.getRun(id)
	return run, made, err
}

// handleCoordinator is POST /projects/{pid}/coordinator {text?}: the
// caller's coordinator of the project, made on first use; text is queued
// to it as their message.
func handleCoordinator(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	var body struct {
		Text string `json:"text"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Text = strings.TrimSpace(body.Text)
	if len(body.Text) > 64<<10 {
		xbin.WriteError(w, 400, "text: at most 64 KiB")
		return
	}
	if body.Text != "" && haltBlocks(w, r, 0) {
		return
	}
	c := callerOf(r)
	ag := projAg()
	run, _, err := ag.ensureCoordinator(c, p)
	if err != nil {
		writeProjErr(w, err)
		return
	}
	if body.Text != "" {
		if _, _, err := ag.queue(run.ID, inboxUser, inboxBody{Text: body.Text, Source: "human", Sender: c.user}, ""); err != nil {
			xbin.WriteError(w, 500, err.Error())
			return
		}
		ag.db.bumpActivity(run.ID)
		if cur, err := ag.db.getRun(run.ID); err == nil {
			run = cur
		}
	}
	xbin.WriteJSON(w, 200, map[string]any{"run": runAnswer(run)})
}

// --- never answering a park ------------------------------------------------------------------

// coordHoldPark (runStatusHooks): a task's run started waiting for a person
// (an approval, ask_user, a coding agent's question or sign-in) while a
// coordinator's or an scm event's input was already in its inbox, not yet
// taken up — the pump delivered it while the run worked. Left there, the
// next pass would take it for the person's reply, and a reply to a parked
// built-in run denies its parked calls. It goes back to the head of the
// project's queue, held until the run stops waiting (hold_park), in its own
// order. The gate's own parks (kind project) keep their inbox: they wait
// for the workspace, not for an answer.
func coordHoldPark(t *DB, runID int64, status string) {
	if status != statusWaiting {
		return
	}
	k := t.taskByRun(runID)
	if k == nil {
		return
	}
	run, err := t.getRun(runID)
	if err != nil || run.ParentID != 0 || parsePending(run.Pending).Kind == pendKindProject {
		return
	}
	moved := false
	for _, row := range t.inboxRows(`WHERE run_id=? AND delivered_at=0 AND kind IN (?, ?) ORDER BY id DESC`, runID, inboxUser, inboxHPrompt) {
		src := row.Body.Source
		if (src != srcCoordinator && src != srcEvent) || len(row.Body.Files) > 0 || row.ClientID == startClientID(k.ProjectID, k.N) {
			continue
		}
		text := row.Body.Text
		if src == srcCoordinator {
			text = strings.TrimPrefix(text, coordFrame)
		}
		if _, err := t.q.Exec(`INSERT INTO project_queue (id, project_id, n, kind, text, source, sender, hold_park, created)
			VALUES ((SELECT MIN(id) - 1 FROM project_queue), ?, ?, 'input', ?, ?, ?, 1, ?)`,
			k.ProjectID, k.N, text, src, row.Body.Sender, nowMs()); err != nil {
			logf("task run #%d: holding back an input while it waits for a person: %v", runID, err)
			return
		}
		if _, err := t.q.Exec(`DELETE FROM inbox WHERE id=?`, row.ID); err != nil {
			return
		}
		moved = true
	}
	if moved {
		if e := projEng(); e != nil {
			e.emitInbox(t, runID, runID)
		}
	}
}
