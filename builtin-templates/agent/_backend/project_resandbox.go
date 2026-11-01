// project_resandbox.go — POST /projects/{pid}/sandbox/recreate (API.md
// §Projects and tasks, "Recreating the sandbox"): a new workspace sandbox
// for a project whose sandbox is gone — its manager lost it, or it was
// deleted outside the agent — or, with force, for one that is still there
// but that the owner wants to start over without (it is left as it is:
// never deleted; the project's credentials in it are emptied and revoked).
//
// The project forgets what lived in the old sandbox — its directory, its
// repos' clones (an adopted clone becomes a base of the project's own), its
// fork base, its tasks' checkouts — and its sandbox job makes the new one as
// a project's {new} sandbox is made. The create's clientId carries a
// generation (sbxGenKey), so a manager that dedupes creates answers a new
// sandbox, not the one it lost. Open tasks are prepared again there: their
// branches come back from the remote; what was never pushed went with the
// old sandbox. A big task's own sandbox (a fork) is its own and is left be.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, func() []routeDef {
		return []routeDef{
			{"POST /projects/{pid}/sandbox/recreate", needAny, projectNeed(lvOwner, handleRecreateSandbox)},
		}
	})
}

// sbxGenKey counts a project's recreated sandboxes: its sandbox job's
// clientId carries it (sbxClientID).
func sbxGenKey(pid int64) string { return "proj_sbx_gen:" + strconv.FormatInt(pid, 10) }

// sbxClientID is the clientId of the sandbox job's create: the first
// sandbox's as it always was, a recreated one's with its generation.
func sbxClientID(pid int64, attempts int, d *DB) string {
	if g, _ := strconv.Atoi(d.getSetting(sbxGenKey(pid))); g > 0 {
		return fmt.Sprintf("agent:proj:%d:sbx:r%d:%d", pid, g, attempts)
	}
	return fmt.Sprintf("agent:proj:%d:sbx:%d", pid, attempts)
}

// sandboxGone: ref's manager says there is no such sandbox (deleted, or
// lost on its side) — not a manager that can't be reached.
func sandboxGone(ctx context.Context, p *Project, ref string) (gone bool, err error) {
	conn, id, err := sbxDialRef(ref, sbxUserOf(binderWho(p.Owner)))
	if err == nil {
		_, err = conn.Get(ctx, id)
	}
	if err != nil && sbxRefusal(err) == "not-found" {
		return true, nil
	}
	return false, err
}

// pickEgress is what a project's new sandbox asks for when nothing was
// said: internet where the manager offers it (a project clones and
// fetches), else its first.
func pickEgress(offered []string) string {
	for _, e := range offered {
		if e == "internet" {
			return e
		}
	}
	if len(offered) > 0 {
		return offered[0]
	}
	return ""
}

// handleRecreateSandbox: POST /projects/{pid}/sandbox/recreate
// {new?: {provider, image?, size?, egress?}, force?}.
func handleRecreateSandbox(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	c := callerOf(r)
	var body struct {
		New   *sandboxNew `json:"new"`
		Force bool        `json:"force"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	switch {
	case p.Kind == projTeam:
		xbin.WriteError(w, http.StatusConflict, "a team project's definition has no workspace to recreate: each member's is their own")
		return
	case p.State != projActive:
		xbin.WriteError(w, http.StatusConflict, "this project is "+p.State)
		return
	case p.SandboxRef == "" && projAg().db.getSetting(sbxNewKey(p.ID)) != "":
		xbin.WriteError(w, http.StatusConflict, "the project's sandbox is being made")
		return
	}
	if haltBlocks(w, r, 0) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	old := p.SandboxRef
	gone := false
	if old != "" {
		var err error
		gone, err = sandboxGone(ctx, p, old)
		switch {
		case gone || body.Force:
		case err == nil:
			writeRefused(w, http.StatusConflict, "exists", fmt.Sprintf(
				"the sandbox %s is still there: send force to start over in a new one (the old one is left as it is)", old))
			return
		default:
			writeRefused(w, http.StatusBadGateway, "unavailable", fmt.Sprintf(
				"can't tell whether the sandbox %s is still there (%v): send force to start over anyway", old, err))
			return
		}
	}
	req := sandboxNew{}
	if body.New != nil {
		req = *body.New
	} else if old != "" {
		req.Provider, _, _ = splitSandboxRef(old)
	}
	m, ok := boundManager(req.Provider)
	if !ok {
		xbin.WriteError(w, 400, fmt.Sprintf("new.provider: %q isn't a sandbox manager bound to this agent", req.Provider))
		return
	}
	if req.Egress == "" {
		if h, err := managerHello(ctx, m); err == nil {
			req.Egress = pickEgress(h.Egress)
		}
	}
	if _, err := taskClassFor(c, &Project{SandboxRef: sandboxRef(req.Provider, "x")}, policyOf(p.Policy).TaskClass); err != nil {
		writeProjErr(w, err)
		return
	}
	if js := projAg().db.jobsWhere(`WHERE project_id=? AND state='running' AND kind<>?`, p.ID, pjFork); len(js) > 0 {
		writeRefused(w, http.StatusConflict, "busy", fmt.Sprintf("the project's %s job is at work: try again once it ends", js[0].Kind))
		return
	}
	if old != "" {
		// a gone sandbox has nothing to empty: its credentials are revoked
		if err := scmScrub(ctx, p, old, scrubLeft); err != nil {
			writeRefused(w, http.StatusBadGateway, "unavailable", "emptying the project's credentials from the old sandbox: "+err.Error())
			return
		}
	}
	var jobs []*ProjectJob
	err := projAg().db.Tx(func(t *DB) error {
		ms := nowMs()
		g, _ := strconv.Atoi(t.getSetting(sbxGenKey(p.ID)))
		if err := t.putSetting(sbxGenKey(p.ID), strconv.Itoa(g+1)); err != nil {
			return err
		}
		b, _ := json.Marshal(req)
		if err := t.putSetting(sbxNewKey(p.ID), string(b)); err != nil {
			return err
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE projects SET sandbox_ref='', sandbox_made=0, dir='', fork_snap='', fork_snap_ms=0, version=version+1, updated_ms=? WHERE id=?`, []any{ms, p.ID}},
			// the clones went with the sandbox; an adopted one comes back as a base of the project's own
			{`UPDATE project_repos SET state='pending', error='', fetched_ms=0, head='', mode=?, base_path='' WHERE project_id=?`, []any{repoBare, p.ID}},
			// what waited to act on the old sandbox
			{`UPDATE project_jobs SET state='failed', error='superseded: the sandbox was recreated', updated_ms=? WHERE project_id=? AND state IN ('queued','waiting') AND kind<>?`, []any{ms, p.ID, pjFork}},
			{`DELETE FROM project_creds WHERE project_id=? AND sandbox_ref=? AND state<>'live'`, []any{p.ID, old}},
		} {
			if _, err := t.q.Exec(q.sql, q.args...); err != nil {
				return err
			}
		}
		tasks, err := t.tasksWhere(`WHERE project_id=? AND fork_made=0 AND sandbox_ref IN (?, '')`, p.ID, old)
		if err != nil {
			return err
		}
		for _, k := range tasks {
			if _, err := t.q.Exec(`DELETE FROM project_checkouts WHERE task_id=?`, k.ID); err != nil {
				return err
			}
			if err := t.setTask(k.ID, map[string]any{"sandbox_ref": "", "dir": ""}); err != nil {
				return err
			}
			switch {
			case k.RunID != 0 && (k.Phase == phaseOpen || k.Phase == phasePR):
				j, err := t.queueJob(p.ID, k.ID, "", pjPrepare, c.tag(), 0)
				if err != nil {
					return err
				}
				jobs = append(jobs, j)
				setWS(t, p, k, wsQueued, "")
			case k.WS != wsPending && k.WS != wsCleaned:
				// over, and what it had went with the sandbox: nothing to clean up
				if err := t.setTask(k.ID, map[string]any{"cleaned_ms": ms}); err != nil {
					return err
				}
				setWS(t, p, k, wsCleaned, "")
			}
		}
		j, err := t.queueJob(p.ID, 0, "", pjSandbox, c.tag(), 0)
		if err != nil {
			return err
		}
		jobs = append([]*ProjectJob{j}, jobs...)
		text := "the sandbox is recreated"
		if old != "" {
			why := "it was replaced"
			if gone {
				why = "it was gone"
			}
			text = fmt.Sprintf("the sandbox is recreated: %s (%s)", old, why)
		}
		addProjectEvent(t, p.ID, 0, pevNote, map[string]any{"text": text, "by": c.tag()}, false, "")
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		writeProjErr(w, err)
		return
	}
	p, _ = projAg().db.getProject(p.ID)
	xbin.WriteJSON(w, http.StatusAccepted, map[string]any{"project": projAg().db.projectView(p, projAg().db.projectLevel(c, p.ID)), "jobs": jobs, "gone": gone})
}

// writeRefused answers a refusal the client tells apart by name (exists,
// unavailable, busy) with why.
func writeRefused(w http.ResponseWriter, code int, refusal, why string) {
	xbin.WriteJSON(w, code, map[string]any{"error": why, "refusal": refusal})
}
