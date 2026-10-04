// project_sandbox.go — a big task's own sandbox (API.md §Big tasks,
// upgrades and pull requests): provisionFork, which prepare calls for a big
// task, clones the project's sandbox from its fork base (sandbox_snapshots.go)
// — or, without one or without the manager's clone, makes a fresh sandbox
// from the same image and clones the repos into it — labels it with the
// project, the task and the home, gives it the project sandbox's
// visibility and members, empties what the snapshot carried that isn't the
// task's (other tasks' checkouts, every credential), gives it a credential
// of its own (the gate judging the fork and the sandbox it came from) and
// fetches; and the fork job, which deletes it once the task's workspace is
// cleaned up (its credentials scrubbed first) unless policy bigTasks.keepFork.
//
// Every fork the project made is in a registry (the setting
// proj_fork:<pid>:<n>, what the create asked for and the sandbox it made),
// so a retry asks the manager for the same sandbox (its clientId) and a
// fork outlives neither its task's cleanup nor its project's deletion
// (forkSweep). A fork works at the project's paths: its manager must give
// it the same working directory (a clone of a sandbox does).
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
	provisionFork = provisionTaskFork
	projectJobKinds[pjFork] = jobFork
	taskChangedHooks = append(taskChangedHooks, forkTaskChanged)
}

// taskLabel is the label a task's fork carries (display and lookup only).
const taskLabel = "xbin.agent/task"

// Fork modes.
const (
	forkFromSnap = "snapshot" // cloned from the project's fork base
	forkFresh    = "fresh"    // a new sandbox of the same image, the repos cloned into it
)

// forkKeyPrefix starts every registry key.
const forkKeyPrefix = "proj_fork:"

func forkKey(pid, n int64) string { return fmt.Sprintf("%s%d:%d", forkKeyPrefix, pid, n) }

// forkEntry is a fork in the registry: how it was asked for (so a retry
// asks the same) and what was made.
type forkEntry struct {
	Mode    string `json:"mode"`           // forkFromSnap | forkFresh
	Snap    string `json:"snap,omitempty"` // the fork base it was cloned from
	Client  string `json:"client"`         // the create's clientId
	Ref     string `json:"ref,omitempty"`  // the sandbox made ("" before the create answered)
	User    string `json:"user,omitempty"` // whom the agent acts for at its manager (Sbx-User)
	Keep    bool   `json:"keep,omitempty"` // policy bigTasks.keepFork, as last seen
	Created int64  `json:"created"`
}

func putForkEntry(d *DB, pid, n int64, e *forkEntry) error {
	b, _ := json.Marshal(e)
	return d.putSetting(forkKey(pid, n), string(b))
}

func dropForkEntry(d *DB, pid, n int64) {
	_, _ = d.q.Exec(`DELETE FROM settings WHERE k=?`, forkKey(pid, n))
}

// provisionTaskFork (provisionFork) makes k's own sandbox and answers its
// ref. It is prepare's step: everything it does is found again on a retry
// (the registry, the create's clientId, scripts that look before they act).
func provisionTaskFork(ctx context.Context, p *Project, k *ProjectTask) (string, error) {
	d := projAg().db
	if p.SandboxRef == "" || p.Dir == "" {
		return "", errors.New("the project's sandbox isn't ready yet")
	}
	user := sbxUserOf(binderWho(p.Owner))
	conn, srcID, err := sbxDialRef(p.SandboxRef, user)
	if err != nil {
		return "", err
	}
	src, err := conn.Get(ctx, srcID)
	if err != nil {
		return "", err
	}
	pol := policyOf(p.Policy)
	ent := forkEntryAt(d, forkKey(p.ID, k.N))
	if ent == nil {
		ent = &forkEntry{Mode: forkFresh, User: user, Created: nowMs(), Client: fmt.Sprintf("agent:proj:%d:fork:%d", p.ID, k.N)}
		if pol.BigTasks.Mode != forkFresh && p.ForkSnap != "" && managerSnapCaps(ctx, p.SandboxRef) {
			ent.Mode, ent.Snap = forkFromSnap, p.ForkSnap
		}
	}
	ent.Keep = pol.BigTasks.KeepFork
	if err := putForkEntry(d, p.ID, k.N, ent); err != nil { // before the create: a retry asks the same
		return "", err
	}
	box, err := createFork(ctx, conn, src, p, k, ent)
	if err != nil && ent.Mode == forkFromSnap && (sbxRefusal(err) == "not-found" || sbxRefusal(err) == "unsupported") {
		// the fork base is gone (or the manager no longer clones): fresh
		logf("project %d task %d: forking from %s: %v — a fresh sandbox instead", p.ID, k.N, ent.Snap, err)
		_, _ = d.q.Exec(`UPDATE projects SET fork_snap='', fork_snap_ms=0 WHERE id=? AND fork_snap=?`, p.ID, ent.Snap)
		ent.Mode, ent.Snap, ent.Client = forkFresh, "", ent.Client+":fresh"
		if err := putForkEntry(d, p.ID, k.N, ent); err != nil {
			return "", err
		}
		box, err = createFork(ctx, conn, src, p, k, ent)
	}
	if err != nil {
		if managerRefused(err) && sbxRefusal(err) != "unavailable" && sbxRefusal(err) != "busy" {
			return "", jobFail("making the task's own sandbox: %v", err)
		}
		return "", err
	}
	ref := sandboxRef(conn.M.Provider, box.ID)
	if ent.Ref != ref {
		ent.Ref = ref
		if err := putForkEntry(d, p.ID, k.N, ent); err != nil {
			return "", err
		}
	}
	if box.State != "running" {
		if box, err = conn.Lifecycle(ctx, box.ID, "start", 120, false); err != nil {
			return "", err
		}
		if box.State != "running" {
			return "", fmt.Errorf("the task's own sandbox %s is %s, not running yet", box.Name, box.State)
		}
	}
	if box.Workdir+"/"+p.Slug != p.Dir {
		return "", jobFail("the task's own sandbox works in %s, not where the project's does (%s): its manager doesn't keep a clone's paths",
			box.Workdir, strings.TrimSuffix(p.Dir, "/"+p.Slug))
	}
	if !scmHomeRe.MatchString(box.Home) || strings.Contains(box.Home, "/../") || strings.HasSuffix(box.Home, "/..") {
		return "", jobFail("the task's own sandbox says an odd home (%q)", box.Home)
	}
	s := &wsbx{conn: conn, id: box.ID, ref: ref, box: box}
	repos, err := taskReposAll(p)
	if err != nil {
		return "", err
	}
	env := map[string]string{"H": box.Home, "P": p.Dir, "N": strconv.Itoa(len(repos))}
	for i, r := range repos {
		env[fmt.Sprintf("B_%d", i)] = basePath(p, r)
	}
	if _, err := s.must(ctx, "clearing the task's own sandbox", forkClearScript, env, "", 2*time.Minute); err != nil {
		return "", err
	}
	if err := forkCreds(ctx, p, k, ref); err != nil {
		return "", err
	}
	if ent.Mode == forkFresh {
		if err := cloneIntoFork(ctx, s, p, k, repos); err != nil {
			return "", err
		}
	} else {
		for _, r := range repos {
			if err := fetchRepo(ctx, s, p, r); err != nil {
				if r.Mode == repoAdopted { // a person's own clone: its remote may be theirs to reach (ssh)
					logf("project %d task %d: fetching %s in the fork: %v", p.ID, k.N, r.Repo, err)
					continue
				}
				return "", err
			}
		}
	}
	_ = d.Tx(func(t *DB) error {
		addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its own sandbox is ready (" + forkWords(ent) + ")"}, false, "")
		return nil
	})
	return ref, nil
}

func forkWords(e *forkEntry) string {
	if e.Mode == forkFromSnap {
		return "forked from the project's"
	}
	return "a fresh one"
}

// taskReposAll is every repo of p a fork holds (not one being removed).
func taskReposAll(p *Project) ([]ProjectRepo, error) {
	all, err := projAg().db.projectRepos(p.ID)
	if err != nil {
		return nil, err
	}
	var out []ProjectRepo
	for _, r := range all {
		if r.State != "removing" {
			out = append(out, r)
		}
	}
	return out, nil
}

// createFork asks k's fork of the manager (the same clientId answers the
// same sandbox): cloned from the fork base, or fresh from the project
// sandbox's image; its visibility and members the project sandbox's.
func createFork(ctx context.Context, conn *sbxConn, src *sbxSandbox, p *Project, k *ProjectTask, e *forkEntry) (*sbxSandbox, error) {
	start := true
	req := sbxCreate{Name: clip(p.Slug+"-"+strconv.FormatInt(k.N, 10), 64), Visibility: src.Visibility, Members: src.Members,
		Labels: map[string]string{projectLabel: p.UID, taskLabel: strconv.FormatInt(k.N, 10)}, Start: &start, ClientID: e.Client}
	if e.Mode == forkFromSnap {
		req.From = &sbxFrom{Sandbox: src.ID, Snapshot: e.Snap}
	} else {
		req.Image, req.Size, req.Egress = src.Image.ID, src.Size.ID, src.Egress
	}
	return conn.Create(ctx, req)
}

// forkClearScript empties a fork of what isn't its task's: the credentials
// the snapshot carried (every project's, never used here — the fork gets
// its own), the other tasks' checkouts and the worktrees git kept for them.
// Values in env: H the home, P the project's directory, B_i the bases.
const forkClearScript = `rm -rf -- "$H/.config/xbin-scm"
i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}"
  if [ -d "$B" ]; then git -C "$B" worktree prune; fi
  i=$((i+1))
done
if [ -d "$P/tasks" ]; then find "$P/tasks" -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +; fi
mkdir -p "$P/.repos" "$P/tasks" "$P/.xbin"
i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}"
  if [ -d "$B" ]; then git -C "$B" worktree prune; git -C "$B" worktree repair; fi
  i=$((i+1))
done
`

// forkCreds is the fork's own credential: the gate judges the fork (and
// that the sandbox it came from was never used by a hosted conversation).
// A sign-in pending is tried again (prepare backs off); the gate refusing
// fails the task with its words.
func forkCreds(ctx context.Context, p *Project, k *ProjectTask, ref string) error {
	err := scmEnsureCreds(ctx, p, k, ref, 10*time.Minute)
	var ge *scmGateError
	switch {
	case err == nil:
		return nil
	case scmRefused(err, scmRefSignin):
		return fmt.Errorf("the task's own sandbox needs a sign-in to %s first: sign in, then Retry if it fails", orStr(p.Host, "the scm provider"))
	case errors.As(err, &ge):
		return jobFail("%s", ge.Error())
	}
	return err
}

// cloneIntoFork clones each base into a fresh fork (the repo job's script,
// in a background exec found again by its clientId), waiting for each.
func cloneIntoFork(ctx context.Context, s *wsbx, p *Project, k *ProjectTask, repos []ProjectRepo) error {
	pol := policyOf(p.Policy)
	for _, r := range repos {
		env := map[string]string{"B": basePath(p, r), "U": r.URL, "DEF": r.DefaultBranch, "PFX": p.branchPrefix(pol)}
		gitCfgEnv(env, p, s.box.Home)
		for key, v := range bashEnv {
			env[key] = v
		}
		cid := fmt.Sprintf("agent:proj:%d:fork:%d:repo:%s", p.ID, k.N, r.Slug)
		ex, err := s.conn.ExecStart(ctx, s.id, sbxExecReq{Cmd: "set -eu\n" + repoScript, Env: env, TimeoutMs: int((30 * time.Minute).Milliseconds()),
			Label: "agent · task " + strconv.FormatInt(k.N, 10) + " · clone " + r.Repo, ClientID: cid})
		if err != nil {
			return err
		}
		for {
			running, code, tail, err := s.execState(ctx, ex.ID)
			if err != nil {
				return err
			}
			if !running {
				if code != 0 {
					return fmt.Errorf("cloning %s into the task's own sandbox failed (exit %d): %s", r.Repo, code, lastLines(clip(projRedact(tail), 8<<10), 6))
				}
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(2 * time.Second):
			}
		}
	}
	return nil
}

// --- deleting a fork --------------------------------------------------------------------

// forkTaskChanged (taskChangedHooks): a big task's workspace was cleaned
// up — its fork job deletes the fork.
func forkTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if what != "ws" || k.WS != wsCleaned || !k.ForkMade || p.Kind == projTeam {
		return
	}
	if _, err := t.queueJob(p.ID, k.ID, "", pjFork, "", 0); err != nil {
		logf("project %d task %d: queueing its fork's deletion: %v", p.ID, k.N, err)
	}
}

// jobFork deletes k's fork once its workspace is cleaned up — its
// credentials scrubbed first — unless policy bigTasks.keepFork.
func jobFork(ctx context.Context, p *Project, k *ProjectTask, _ *ProjectJob) (jobOutcome, error) {
	if k == nil {
		return doneJob("the task is gone")
	}
	d := projAg().db
	e := forkEntryAt(d, forkKey(p.ID, k.N))
	switch {
	case e == nil || e.Ref == "":
		return doneJob("no fork")
	case k.WS != wsCleaned:
		return doneJob("its workspace is in use")
	case policyOf(p.Policy).BigTasks.KeepFork:
		dropForkEntry(d, p.ID, k.N)
		return doneJob("kept (bigTasks.keepFork)")
	}
	if err := scmScrubCreds(ctx, p, e.Ref, scrubDelete); err != nil {
		return jobOutcome{}, err
	}
	if err := deleteFork(ctx, e); err != nil {
		return jobOutcome{}, err
	}
	dropForkEntry(d, p.ID, k.N)
	_ = d.Tx(func(t *DB) error {
		addProjectEvent(t, p.ID, k.N, pevWorkspace, map[string]any{"text": "its own sandbox was deleted"}, false, "")
		emitProject(t, p.ID, "task", k.N)
		return nil
	})
	return doneJob("deleted its own sandbox")
}

// deleteFork deletes a fork at its manager (gone already: done).
func deleteFork(ctx context.Context, e *forkEntry) error {
	conn, id, err := sbxDialRef(e.Ref, e.User)
	if err != nil {
		if sbxRefusal(err) == "unbound" {
			return nil // its manager is no longer bound: nothing the agent can delete
		}
		return err
	}
	if err := conn.Delete(ctx, id); err != nil && sbxRefusal(err) != "not-found" {
		return err
	}
	return nil
}

// forkSweep (the owner loop) deals with the forks the jobs missed: a
// cleaned task's fork whose job didn't run (an archived project's waits),
// and the forks of a project deleted since — its credentials were scrubbed
// with it — which are deleted unless they were to be kept.
func forkSweep(ctx context.Context) {
	d := projAg().db
	for _, key := range forkKeys(d) {
		if ctx.Err() != nil {
			return
		}
		var pid, n int64
		if _, err := fmt.Sscanf(strings.TrimPrefix(key, forkKeyPrefix), "%d:%d", &pid, &n); err != nil {
			continue
		}
		e := forkEntryAt(d, key)
		if e == nil {
			_, _ = d.q.Exec(`DELETE FROM settings WHERE k=?`, key)
			continue
		}
		p, err := d.getProject(pid)
		if err != nil && !errors.Is(err, errNoProject) {
			continue
		}
		if err != nil { // the project was deleted: its credentials with it
			if e.Ref != "" && !e.Keep {
				cctx, cancel := context.WithTimeout(ctx, time.Minute)
				err = deleteFork(cctx, e)
				cancel()
				if err != nil {
					logf("a deleted project's fork %s: %v", e.Ref, err)
					continue
				}
			}
			dropForkEntry(d, pid, n)
			continue
		}
		if p.State == projDeleting {
			continue // its deletion runs: the rows go, then this
		}
		if keep := policyOf(p.Policy).BigTasks.KeepFork; keep != e.Keep {
			e.Keep = keep
			_ = putForkEntry(d, pid, n, e)
		}
		k, err := d.taskByN(pid, n)
		if err == nil && k.WS == wsCleaned && k.ForkMade && p.State == projActive && d.liveJob(pid, k.ID, "", pjFork) == nil {
			_, _ = d.queueJob(pid, k.ID, "", pjFork, "", 0)
		}
	}
}
