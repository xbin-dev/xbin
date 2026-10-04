// project_gate.go — the workspace gate and the other places the engine
// asks Projects something (API.md §The workspace): projectGate in
// Engine.pass parks a task's turn until its workspace is ready (and while
// its credential is being renewed); a task's built-in run takes a
// subagent's place at the model-call gate; a project run's class never has
// internal reach (projectClamp, in classOf); a coordinator's project events
// are delivered at its step boundaries and wake it; and the environment a
// task's commands get (projectEnv).
//
// The gate reads the database only — never a provider or a sandbox. A
// coordinator is never gated.
package main

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// projectGate (Engine.pass, after the inbox is read, before a coding
// agent's pass): false parks the task's run — status sleeping while its
// workspace is prepared (or its credential renewed), waiting_input when a
// person must act (a sign-in, a failed workspace, cleanup refused) — with
// pendingState {kind: "project", project}, its inbox left unconsumed. A
// park ends here once the workspace is ready (run is updated in place).
// Cancel and interrupt rows always pass; so does a turn already in flight.
func (e *Engine) projectGate(run *Run, rows []*InboxRow) bool {
	if hostedID(run.ID) || !e.db.features {
		return true
	}
	input := false
	for _, r := range rows {
		switch r.Kind {
		case inboxCancel, inboxInterrupt, inboxHStop:
			return true
		case inboxUser, inboxHPrompt, inboxWake, inboxWatch, inboxApprove, inboxHAnswer:
			input = true
		}
	}
	ref := e.db.projectRefOf(run) // an older build's rewrite loses Config.Project: given back here
	if !ref.isTask() {
		return true
	}
	k := e.db.taskByRun(run.ID)
	if k == nil {
		return true
	}
	p, err := e.db.getProject(k.ProjectID)
	if err != nil {
		return true
	}
	ps := parsePending(run.Pending)
	ours := ps.Kind == pendKindProject
	want, creds := "", false
	switch k.WS {
	case wsPending, wsQueued, wsPreparing:
		want = statusSleep
	case wsSignin, wsFailed, wsBlocked:
		want = statusWaiting
	case wsReady:
		if p.State == projActive && p.Kind != projTeam && scmCredsDue(e.db, p, k) {
			want, creds = statusSleep, true
		}
	}
	if want == "" {
		if ours {
			e.unparkTask(run)
		}
		return true
	}
	if !ours && (active(run.Status) || !input) {
		return true // a turn in flight goes on; nothing here would start one
	}
	if p.State != projActive {
		e.shelveTask(run, p, k) // archived: no workspace work goes on, so no park waits for it
		return false
	}
	park := ProjectPark{Project: p.ID, N: k.N, WS: k.WS, Detail: k.Error}
	if js := e.db.jobsWhere(`WHERE task_id=? AND state IN ('queued','running','waiting') ORDER BY id LIMIT 1`, k.ID); len(js) > 0 {
		park.Step = orStr(js[0].Step, js[0].Kind)
	}
	if creds {
		park.Step = "renewing the credentials"
	}
	if ours && run.Status == want && ps.Project != nil && ps.Project.WS == park.WS && ps.Project.Detail == park.Detail {
		return false // parked already, as it stands
	}
	pend, _ := json.Marshal(pendingState{Kind: pendKindProject, Project: &park})
	result := ""
	if want == statusWaiting {
		result = parkWords(p, k)
	}
	_ = e.fenced(func(t *DB) error {
		if err := t.setStatus(run.ID, want, 0, result, string(pend)); err != nil {
			return err
		}
		if !ours {
			e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": "waiting for the project's workspace: " + parkWords(p, k)}))
		}
		if creds && projectJobKinds[pjCreds] != nil {
			if _, err := t.queueJob(p.ID, k.ID, "", pjCreds, "", 0); err != nil {
				return err
			}
		}
		e.emitRun(t, run.ID)
		return nil
	})
	return false
}

// shelveTask: task k's turn would wait for a workspace an archived project
// doesn't prepare. Its inputs go back to the project's queue (the pump
// delivers them once the project is active again), a wake row is dropped,
// and the run rests — nothing of it keeps the engine (hasWork) or a
// person's partition (userWake) up. A message with files stays in the
// inbox (the queue holds text only).
func (e *Engine) shelveTask(run *Run, p *Project, k *ProjectTask) {
	err := e.fenced(func(t *DB) error {
		// newest first, each ahead of the whole queue: they were delivered
		// before anything still in it, and keep their own order
		for _, row := range t.inboxRows(`WHERE run_id=? AND delivered_at=0 AND kind IN (?, ?, ?) ORDER BY id DESC`, run.ID, inboxUser, inboxHPrompt, inboxWake) {
			if row.Kind == inboxWake {
				_, _ = t.q.Exec(`DELETE FROM inbox WHERE id=?`, row.ID)
				continue
			}
			if len(row.Body.Files) > 0 {
				continue
			}
			kind, src := "input", orStr(row.Body.Source, srcHuman)
			if row.ClientID == startClientID(p.ID, k.N) {
				kind = "start"
			}
			text := row.Body.Text
			if src == srcCoordinator {
				text = strings.TrimPrefix(text, coordFrame)
			}
			if _, err := t.q.Exec(`INSERT INTO project_queue (id, project_id, n, kind, text, source, sender, created)
				VALUES ((SELECT MIN(id) - 1 FROM project_queue), ?, ?, ?, ?, ?, ?, ?)`,
				p.ID, k.N, kind, text, src, row.Body.Sender, nowMs()); err != nil {
				return err
			}
			if _, err := t.q.Exec(`DELETE FROM inbox WHERE id=?`, row.ID); err != nil {
				return err
			}
		}
		if err := t.setStatus(run.ID, statusIdle, 0, "", ""); err != nil {
			return err
		}
		e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": "the project is " + p.State + ": this task's input waits in its queue"}))
		e.emitInbox(t, run.ID, run.ID)
		e.emitRun(t, run.ID)
		return nil
	})
	if err == nil {
		run.Status, run.Pending, run.Result, run.WakeAt = statusIdle, "", "", 0
	}
}

// gateHeldSQL (hasWork, userWake; inbox alias i): not a row of a task run
// the gate parked waiting for a person — a sign-in, a failed workspace, a
// refused cleanup. Its input waits for that person's act (which brings the
// process up by itself, and whose job the worker's terms count), so it is
// no work that keeps the engine or wakes a person's partition.
const gateHeldSQL = `i.run_id NOT IN (SELECT id FROM runs WHERE status='waiting_input' AND origin='project'
	AND json_extract(CASE WHEN json_valid(pending) THEN pending ELSE '{}' END, '$.kind')='project')`

// shelveTasks (archiving project pid): every task run is poked — the gate
// of one parked for its workspace, or with input waiting for it, shelves it.
func (ag *Agent) shelveTasks(pid int64) {
	e := projEng()
	if e == nil {
		return
	}
	for _, id := range scanIDs(ag.db.q.Query(`SELECT run_id FROM project_tasks WHERE project_id=? AND run_id<>0`, pid)) {
		e.Poke(id)
	}
}

// unparkTask ends the gate's park: the run rests, its inbox still holds
// what starts the turn (the pass goes on with run as it is now).
func (e *Engine) unparkTask(run *Run) {
	err := e.fenced(func(t *DB) error {
		if err := t.setStatus(run.ID, statusIdle, 0, "", ""); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		return nil
	})
	if err == nil {
		run.Status, run.Pending, run.Result, run.WakeAt = statusIdle, "", "", 0
	}
}

// parkWords says why a task waits for its workspace, for people (/needs
// shows it as the run's result).
func parkWords(p *Project, k *ProjectTask) string {
	name := "#" + strconv.FormatInt(k.N, 10) + " " + k.Title
	switch k.WS {
	case wsSignin:
		return name + " needs you: sign in to " + orStr(p.Host, "the scm provider")
	case wsFailed:
		return name + " needs you: its workspace failed — " + orStr(k.Error, "see the project's status")
	case wsBlocked:
		return name + " needs you: its cleanup was refused (work that isn't pushed)"
	case wsReady:
		return name + ": renewing its credentials"
	}
	return name + ": its workspace is being prepared"
}

// needsReason is a parked run's /needs reason for the gate's park.
func needsReason(ps pendingState) string {
	if ps.Kind == pendKindProject {
		return "project"
	}
	return ""
}

// --- the engine's other questions ------------------------------------------------------------

// modelGateTop: a model call of run takes the top-level class at the
// model-call gate — never a built-in task's (it runs among many, as a
// subagent does), whose role comes from projectRefOf.
func (e *Engine) modelGateTop(run *Run) bool {
	return run.Depth == 0 && !e.db.projectRefOf(run).isTask()
}

// projectClamp is classOf's last word on a project run (Config.Project set:
// a task, a coordinator and their subagents): never internal reach nor MCP
// servers, whatever its lane — a class edited after the task started
// included.
func projectClamp(cfg Config, c agentClass) agentClass {
	if cfg.Project == nil {
		return c
	}
	c.Toolsets = without(c.Toolsets, tsInternal)
	c.MCP = classSet{}
	return c
}

// projectWakes: an idle coordinator has project events that should wake it
// (the coordinator's part answers, projectWakeHook).
func projectWakes(d *DB, run *Run) bool {
	return run.ParentID == 0 && run.Origin == originProject && hooksOn(d, run.ID) && projectWakeHook(d, run)
}

// deliverProjectEvents writes a coordinator's undelivered project events as
// one message at a step boundary (deliverBoundary, beside the notices) and
// has them marked delivered with it. True when it wrote one.
func (e *Engine) deliverProjectEvents(t *DB, ts *turnState) bool {
	run := ts.run
	if run.ParentID != 0 || run.Origin != originProject || !hooksOn(t, run.ID) {
		return false
	}
	ref := t.projectRefOf(run)
	if !ref.isCoordinator() {
		return false
	}
	text, mark := projectDeliverHook(t, run)
	if text == "" || mark == nil {
		return false
	}
	m := &Message{RunID: run.ID, Role: "user", Content: text}
	m.Meta, _ = json.Marshal(msgMeta{Origin: "project", OriginID: ref.ID, Label: "project updates"})
	if _, err := t.addMessage(m); err != nil {
		return false
	}
	mark(m.ID)
	e.emitMessage(t, ts.root, m)
	return true
}

// projectPromptFor is the # Project block of run's system prompt: a task's
// brief (projectPrompt), a coordinator's (its part's), "" otherwise.
func (ag *Agent) projectPromptFor(run *Run, cfg Config) string {
	if run == nil || run.Origin != originProject || hostedID(run.ID) {
		return ""
	}
	ref := ag.db.projectRefOf(run)
	if ref == nil {
		return ""
	}
	p, err := ag.db.getProject(ref.ID)
	if err != nil {
		return ""
	}
	if ref.isCoordinator() {
		return projectCoordPrompt(p, run)
	}
	root := run
	if run.ParentID != 0 {
		if root, err = ag.db.getRun(rootOf(run)); err != nil {
			return ""
		}
	}
	k := ag.db.taskByRun(root.ID)
	if k == nil {
		return ""
	}
	return "\n\n" + projectPrompt(ag.db, p, k, run.ParentID != 0)
}

// --- a task's environment ---------------------------------------------------------------------

// projectEnv is what every command in a task's workspace gets — a bash
// job's, the coding agent's — over its own env: the task's directory,
// branch and ports, and the credentials' (scmProjectEnv: GH_CONFIG_DIR).
// home is the sandbox's home. nil for a run that isn't a project's task.
func projectEnv(run *Run, home string) map[string]string {
	if projAg() == nil || run == nil || run.Origin != originProject || hostedID(run.ID) {
		return nil
	}
	root := run
	if run.ParentID != 0 {
		var err error
		if root, err = projAg().db.getRun(rootOf(run)); err != nil {
			return nil
		}
	}
	p, k := projAg().db.projectOfRun(root)
	if k == nil {
		return nil
	}
	return taskEnv(p, k, home)
}

// taskEnv is task k's environment (projectEnv; the setup and the jobs).
func taskEnv(p *Project, k *ProjectTask, home string) map[string]string {
	pol := policyOf(p.Policy)
	ports := portsOf(pol, k.N)
	env := map[string]string{"TASK_DIR": k.Dir, "BRANCH": k.Branch,
		"TASK_PORT_BASE": strconv.Itoa(ports.Base), "TASK_PORT_SPAN": strconv.Itoa(ports.Span), "PORT": strconv.Itoa(ports.Base)}
	for key, v := range scmProjectEnv(p, home) {
		env[key] = v
	}
	return env
}

// withProjectEnv is env with run's project environment over it (the call
// sites: a bash job's exec, a coding agent's spawn).
func withProjectEnv(env map[string]string, run *Run, home string) map[string]string {
	extra := projectEnv(run, home)
	if len(extra) == 0 {
		return env
	}
	out := make(map[string]string, len(env)+len(extra))
	for k, v := range env {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

// taskEnvFile is .task-env: the same, for a person's terminal (". .task-env").
func taskEnvFile(env map[string]string) string {
	s := ""
	for _, k := range []string{"BRANCH", "TASK_PORT_BASE", "TASK_PORT_SPAN", "PORT", "GH_CONFIG_DIR"} {
		if v, ok := env[k]; ok {
			s += fmt.Sprintf("%s=%s\n", k, shellQuote(v))
		}
	}
	return s
}

// shellQuote is s as one sh word.
func shellQuote(s string) string {
	out := "'"
	for _, r := range s {
		if r == '\'' {
			out += `'\''`
		} else {
			out += string(r)
		}
	}
	return out + "'"
}
