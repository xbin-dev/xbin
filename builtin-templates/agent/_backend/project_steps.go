// project_steps.go — the workspace's job kinds (API.md §The workspace):
// sandbox (find or create the workspace sandbox, label and start it, lay
// out <workdir>/<slug>), repo (a bare base repo per repo, cloned in a
// background exec), fetch, prepare (a task's worktrees on its branch, in
// one Run), setup (a repo's setup script, in the background) and bind (the
// task's conversation bound to its checkout). Each looks before it acts —
// running one again after a restart is a no-op where it already happened —
// and every value a script needs is passed in its environment, never
// spliced into it. Paths come from the sandbox's own workdir and home.
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
	projectJobKinds[pjSandbox] = jobSandbox
	projectJobKinds[pjRepo] = jobRepo
	projectJobKinds[pjFetch] = jobFetch
	projectJobKinds[pjPrepare] = jobPrepare
}

// projectLabel is the label a workspace sandbox carries — for display and
// lookup only: a label proves nothing, and no gate reads it.
const projectLabel = "xbin.agent/project"

// --- the sandbox, as the worker uses it --------------------------------------------------------

// wsbx is a sandbox the worker works in: its manager (called for the
// project's owner) and its description.
type wsbx struct {
	conn *sbxConn
	id   string
	ref  string
	box  *sbxSandbox
}

func openWsbx(ctx context.Context, p *Project, ref string) (*wsbx, error) {
	conn, id, err := sbxDialRef(ref, sbxUserOf(binderWho(p.Owner)))
	if err != nil {
		return nil, err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if box.Workdir == "" || !strings.HasPrefix(box.Workdir, "/") {
		return nil, jobFail("the sandbox %s says no working directory", box.Name)
	}
	return &wsbx{conn: conn, id: id, ref: ref, box: box}, nil
}

// gitLocked: git refused because another process holds one of its locks.
func gitLocked(out string) bool {
	return strings.Contains(out, ".lock': File exists") || strings.Contains(out, "index.lock") ||
		strings.Contains(out, "Unable to create") && strings.Contains(out, ".lock")
}

// run runs script (set -eu first) in the sandbox with env, merged output,
// within timeout; git's own lock refusals are tried again (2 s, 4 s … 60 s,
// six times) — a stale lock is never removed. Answers the output and the
// exit code.
func (s *wsbx) run(ctx context.Context, script string, env map[string]string, cwd string, timeout time.Duration) (string, int, error) {
	wait := 2 * time.Second
	for try := 0; ; try++ {
		res, err := s.conn.Run(ctx, s.id, sbxRunReq{Cmd: "set -eu\n" + script, Env: env, Cwd: cwd,
			TimeoutMs: int(timeout.Milliseconds()), Merge: true, MaxOutput: 64 << 10})
		if err != nil {
			return "", -1, err
		}
		out := ""
		if res.Output != nil {
			out = res.Output.Head + res.Output.Tail
		}
		code := -1
		if res.ExitCode != nil {
			code = *res.ExitCode
		}
		if res.TimedOut {
			return out, code, fmt.Errorf("timed out after %s: %s", timeout, clip(projRedact(out), 2000))
		}
		if code != 0 && gitLocked(out) && try < 6 {
			select {
			case <-ctx.Done():
				return out, code, ctx.Err()
			case <-time.After(wait):
			}
			wait = min(wait*2, 60*time.Second)
			continue
		}
		return out, code, nil
	}
}

// must is run's error for a non-zero exit too.
func (s *wsbx) must(ctx context.Context, what, script string, env map[string]string, cwd string, timeout time.Duration) (string, error) {
	out, code, err := s.run(ctx, script, env, cwd, timeout)
	if err == nil && code != 0 {
		err = fmt.Errorf("%s failed (exit %d): %s", what, code, clip(projRedact(strings.TrimSpace(out)), 2000))
	}
	return out, err
}

// execState is a background exec of a job: still running, or its exit and
// the tail of what it printed (8 KiB).
func (s *wsbx) execState(ctx context.Context, execID string) (running bool, code int, tail string, err error) {
	ex, err := s.conn.ExecGet(ctx, s.id, execID)
	if err != nil {
		return false, -1, "", err
	}
	if ex.State == "running" {
		return true, 0, "", nil
	}
	since := max(ex.Total-8<<10, 0)
	if ch, err := s.conn.ExecOutput(ctx, s.id, execID, since, 8<<10, 0, false); err == nil {
		tail = ch.Data
	}
	code = -1
	if ex.ExitCode != nil {
		code = *ex.ExitCode
	}
	return false, code, tail, nil
}

// --- what a job says --------------------------------------------------------------------------

// jobFailErr fails a job at once (no retry: retrying can't help).
type jobFailErr struct{ msg string }

func (e *jobFailErr) Error() string { return e.msg }

func jobFail(format string, args ...any) error { return &jobFailErr{fmt.Sprintf(format, args...)} }

func waitJob(ms int64, step string) (jobOutcome, error) {
	return jobOutcome{WaitMs: ms, Step: step}, nil
}

func doneJob(step string) (jobOutcome, error) { return jobOutcome{Done: true, Step: step}, nil }

// signinWait is how long a task's workspace waits for its person's sign-in
// (the provider's device code lasts about as long) before it fails.
var signinWait = 20 * time.Minute

// stepCreds is the credential's part before a git step: the project's
// token in ref, live for 10 more minutes (scmEnsureCreds; nothing before
// that part is in). A team definition's sandbox (a seed) never holds one.
// A sign-in pending: the task waits on it (signin true).
func stepCreds(ctx context.Context, p *Project, k *ProjectTask, ref string) (signin bool, err error) {
	if p.Kind == projTeam {
		return false, nil
	}
	err = scmEnsureCreds(ctx, p, k, ref, 10*time.Minute)
	if scmRefused(err, scmRefSignin) {
		return true, nil
	}
	return false, err
}

// --- sandbox ----------------------------------------------------------------------------------

// jobSandbox finds or creates the project's workspace sandbox, labels it,
// starts it, lays out <workdir>/<slug>/{.repos,tasks,.xbin}, records dir,
// and queues the repos' jobs.
func jobSandbox(ctx context.Context, p *Project, _ *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if p.SandboxRef == "" {
		raw := projAg().db.getSetting(sbxNewKey(p.ID))
		if raw == "" {
			return jobOutcome{}, jobFail("the project has no sandbox: name one (sandbox.ref) or ask for a new one")
		}
		var req sandboxNew
		_ = json.Unmarshal([]byte(raw), &req)
		conn, err := sbxDial(req.Provider, sbxUserOf(binderWho(p.Owner)))
		if err != nil {
			return jobOutcome{}, err
		}
		vis := visPrivate
		if p.Kind == projTeam {
			vis = p.Visibility // a team's seed is the team's to see
		}
		start := true
		box, err := conn.Create(ctx, sbxCreate{Name: p.Slug, Image: req.Image, Size: req.Size, Egress: req.Egress, Visibility: vis,
			Labels: withHomeLabel(map[string]string{projectLabel: p.UID}), Start: &start,
			ClientID: fmt.Sprintf("agent:proj:%d:sbx:%d", p.ID, j.Attempts)})
		if err != nil {
			if managerRefused(err) && sbxRefusal(err) != "unavailable" {
				return jobOutcome{}, jobFail("creating the sandbox: %v", err)
			}
			return jobOutcome{}, err
		}
		ref := sandboxRef(conn.M.Provider, box.ID)
		err = projAg().db.Tx(func(t *DB) error {
			if _, err := t.q.Exec(`UPDATE projects SET sandbox_ref=?, sandbox_made=1, updated_ms=? WHERE id=?`, ref, nowMs(), p.ID); err != nil {
				return err
			}
			_, _ = t.q.Exec(`UPDATE project_tasks SET sandbox_ref=? WHERE project_id=? AND sandbox_ref=''`, ref, p.ID)
			_, _ = t.q.Exec(`DELETE FROM settings WHERE k=?`, sbxNewKey(p.ID))
			emitProject(t, p.ID, "project", 0)
			return nil
		})
		if err != nil {
			return jobOutcome{}, err
		}
		p.SandboxRef, p.SandboxMade = ref, true
	}
	s, err := openWsbx(ctx, p, p.SandboxRef)
	if err != nil {
		return jobOutcome{}, err
	}
	if s.box.Labels[projectLabel] != p.UID {
		labels := map[string]string{}
		for k, v := range s.box.Labels {
			labels[k] = v
		}
		labels[projectLabel] = p.UID
		if _, err := s.conn.Patch(ctx, s.id, sbxPatch{Labels: &labels}); err != nil {
			logf("project %d: labelling its sandbox: %v", p.ID, err) // display only: not a reason to stop
		}
	}
	switch s.box.State {
	case "running":
	case "archived":
		if s.box, err = s.conn.Lifecycle(ctx, s.id, "thaw", 120, true); err != nil {
			return jobOutcome{}, err
		}
	default:
		if s.box, err = s.conn.Lifecycle(ctx, s.id, "start", 120, false); err != nil {
			return jobOutcome{}, err
		}
	}
	if s.box.State != "running" {
		return waitJob(3000, "starting the sandbox")
	}
	dir := s.box.Workdir + "/" + p.Slug
	if _, err := s.must(ctx, "laying out the workspace", `mkdir -p "$P/.repos" "$P/tasks" "$P/.xbin"`,
		map[string]string{"P": dir}, "", time.Minute); err != nil {
		return jobOutcome{}, err
	}
	err = projAg().db.Tx(func(t *DB) error {
		if p.Dir != dir {
			if _, err := t.q.Exec(`UPDATE projects SET dir=?, updated_ms=? WHERE id=?`, dir, nowMs(), p.ID); err != nil {
				return err
			}
			p.Dir = dir
		}
		ks, _ := t.tasksWhere(`WHERE project_id=? AND dir=''`, p.ID)
		for _, k := range ks {
			_ = t.setTask(k.ID, map[string]any{"dir": dir + "/tasks/" + strconv.FormatInt(k.N, 10) + "-" + k.Slug})
		}
		repos, _ := t.projectRepos(p.ID)
		for _, r := range repos {
			if r.Mode == repoBare && (r.State == "pending" || r.State == "failed") {
				if _, err := t.queueJob(p.ID, 0, r.Slug, pjRepo, j.By, 0); err != nil {
					return err
				}
			}
		}
		emitProject(t, p.ID, "project", 0)
		return nil
	})
	if err != nil {
		return jobOutcome{}, err
	}
	return doneJob("the sandbox is ready")
}

// --- repo, fetch ---------------------------------------------------------------------------------

// repoScript makes or repairs a bare base repo (API.md §The workspace;
// the credential lines come as GIT_CFG_<i>_K / _V pairs).
const repoScript = `[ -d "$B" ] || git init -q --bare "$B"
git -C "$B" remote get-url origin >/dev/null 2>&1 || git -C "$B" remote add origin "$U"
git -C "$B" remote set-url origin "$U"
if [ -z "$DEF" ]; then
  DEF=$(git ls-remote --symref "$U" HEAD | sed -n 's#^ref: refs/heads/\(.*\)	HEAD$#\1#p')
fi
[ -n "$DEF" ] || { echo "can't tell the default branch of $U" >&2; exit 3; }
git -C "$B" config --unset-all remote.origin.fetch || true
git -C "$B" config --add remote.origin.fetch "+refs/heads/$DEF:refs/remotes/origin/$DEF"
git -C "$B" config --add remote.origin.fetch "+refs/heads/$PFX/*:refs/remotes/origin/$PFX/*"
git -C "$B" config push.default current
git -C "$B" config push.autoSetupRemote true
git -C "$B" config core.logAllRefUpdates true
` + gitCfgLoop + `git -C "$B" fetch -q --prune --no-tags origin
echo "DEF=$DEF"
git -C "$B" rev-parse "refs/remotes/origin/$DEF"
`

// gitCfgLoop applies the credential lines (GIT_CFG_N pairs) to "$G" (else
// "$B"): a key
// met first is reset (--unset-all), then each value added — so a helper
// list can start with the empty reset line.
const gitCfgLoop = `G=${G:-$B}; i=0; prev=
while [ "$i" -lt "${GIT_CFG_N:-0}" ]; do
  eval "k=\${GIT_CFG_${i}_K}; v=\${GIT_CFG_${i}_V}"
  if [ "$k" != "$prev" ]; then git -C "$G" config --unset-all "$k" || true; fi
  git -C "$G" config --add "$k" "$v"
  prev=$k; i=$((i+1))
done
`

// gitCfgEnv is the credential lines for p's host as GIT_CFG_* env.
func gitCfgEnv(env map[string]string, p *Project, home string) {
	pairs := scmGitConfig(p, p.Host, home)
	env["GIT_CFG_N"] = strconv.Itoa(len(pairs))
	for i, kv := range pairs {
		env[fmt.Sprintf("GIT_CFG_%d_K", i)] = kv[0]
		env[fmt.Sprintf("GIT_CFG_%d_V", i)] = kv[1]
	}
}

// basePath is a repo's base: its bare repo in the project's directory, or
// an adopted clone's toplevel.
func basePath(p *Project, r ProjectRepo) string {
	if r.Mode == repoAdopted && r.BasePath != "" {
		return r.BasePath
	}
	return p.Dir + "/.repos/" + r.Slug + ".git"
}

// jobRepo clones a repo's base in a background exec (30 min), then records
// its head; with policy.protection refuse, an unprotected default branch
// fails it.
func jobRepo(ctx context.Context, p *Project, _ *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	r, err := projAg().db.projectRepo(p.ID, j.Repo)
	if err != nil {
		return jobOutcome{}, jobFail("the repo %s is no longer the project's", j.Repo)
	}
	if r.Mode == repoAdopted {
		return doneJob("adopted")
	}
	if p.Dir == "" || p.SandboxRef == "" {
		return waitJob(3000, "waiting for the sandbox")
	}
	pol := policyOf(p.Policy)
	if pol.Protection == "refuse" && r.Protected != nil && !*r.Protected {
		return jobOutcome{}, jobFail("%s's default branch has no protection, and the project's policy refuses such a repo (protection: refuse)", r.Repo)
	}
	s, err := openWsbx(ctx, p, p.SandboxRef)
	if err != nil {
		return jobOutcome{}, err
	}
	if signin, err := stepCreds(ctx, p, nil, p.SandboxRef); signin {
		return waitJob(10000, "waiting for a sign-in")
	} else if err != nil {
		return jobOutcome{}, err
	}
	if j.ExecID == "" {
		projAg().db.setRepoState(p.ID, r.Slug, "cloning", "")
		env := map[string]string{"B": basePath(p, r), "U": r.URL, "DEF": r.DefaultBranch, "PFX": p.branchPrefix(pol)}
		gitCfgEnv(env, p, s.box.Home)
		for k, v := range bashEnv {
			env[k] = v
		}
		j.ClientID = fmt.Sprintf("agent:proj:%d:repo:%s:%d", p.ID, r.Slug, j.Attempts)
		ex, err := s.conn.ExecStart(ctx, s.id, sbxExecReq{Cmd: "set -eu\n" + repoScript, Env: env, TimeoutMs: int((30 * time.Minute).Milliseconds()),
			Label: "agent · project " + p.Slug + " · clone " + r.Repo, ClientID: j.ClientID})
		if err != nil {
			return jobOutcome{}, err
		}
		j.ExecRef, j.ExecID = s.ref, ex.ID
		return waitJob(1000, "cloning "+r.Repo)
	}
	running, code, tail, err := s.execState(ctx, j.ExecID)
	if err != nil {
		if sbxRefusal(err) == "not-found" {
			j.ExecID = "" // the manager lost it: started again
			return waitJob(1000, "cloning "+r.Repo)
		}
		return jobOutcome{}, err
	}
	if running {
		return waitJob(2000, "cloning "+r.Repo)
	}
	j.Out = clip(projRedact(tail), 8<<10)
	j.ExecID = ""
	if code != 0 {
		return jobOutcome{}, fmt.Errorf("cloning %s failed (exit %d): %s", r.Repo, code, lastLines(j.Out, 6))
	}
	def, head := parseRepoOut(tail)
	_ = projAg().db.Tx(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE project_repos SET state='ready', error='', head=?, fetched_ms=?,
			default_branch=CASE WHEN default_branch='' THEN ? ELSE default_branch END WHERE project_id=? AND slug=?`,
			head, nowMs(), def, p.ID, r.Slug)
		emitProject(t, p.ID, "repo", 0)
		return err
	})
	return doneJob("cloned " + r.Repo)
}

// parseRepoOut reads the repo script's last words: DEF=<branch>, then the
// default branch's sha.
func parseRepoOut(out string) (def, head string) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, l := range lines {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "DEF="); ok {
			def = v
		}
	}
	if n := len(lines); n > 0 {
		head = strings.TrimSpace(lines[n-1])
	}
	if len(head) < 7 || strings.ContainsAny(head, " =") {
		head = ""
	}
	return def, head
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func (d *DB) setRepoState(pid int64, slug, state, errText string) {
	_ = d.Tx(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE project_repos SET state=?, error=? WHERE project_id=? AND slug=?`, state, errText, pid, slug)
		emitProject(t, pid, "repo", 0)
		return err
	})
}

// fetchScript fetches a base and prints its default branch's sha.
const fetchScript = `git -C "$B" fetch -q --prune --no-tags origin
git -C "$B" rev-parse "refs/remotes/origin/$DEF"
`

// fetchRepo fetches one base repo (120 s) and records it.
func fetchRepo(ctx context.Context, s *wsbx, p *Project, r ProjectRepo) error {
	out, err := s.must(ctx, "fetching "+r.Repo, fetchScript, map[string]string{"B": basePath(p, r), "DEF": r.DefaultBranch,
		"GIT_TERMINAL_PROMPT": "0"}, "", 120*time.Second)
	if err != nil {
		return err
	}
	head := strings.TrimSpace(lastLines(out, 1))
	_, err = projAg().db.q.Exec(`UPDATE project_repos SET head=?, fetched_ms=? WHERE project_id=? AND slug=?`, head, nowMs(), p.ID, r.Slug)
	return err
}

// jobFetch fetches one of the project's repos.
func jobFetch(ctx context.Context, p *Project, _ *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	r, err := projAg().db.projectRepo(p.ID, j.Repo)
	if err != nil || r.State != "ready" {
		return doneJob("nothing to fetch")
	}
	s, err := openWsbx(ctx, p, p.SandboxRef)
	if err != nil {
		return jobOutcome{}, err
	}
	if signin, err := stepCreds(ctx, p, nil, p.SandboxRef); signin {
		return waitJob(10000, "waiting for a sign-in")
	} else if err != nil {
		return jobOutcome{}, err
	}
	if err := fetchRepo(ctx, s, p, r); err != nil {
		return jobOutcome{}, err
	}
	emitProject(projAg().db, p.ID, "repo", 0)
	return doneJob("fetched " + r.Repo)
}

// --- prepare, setup, bind ---------------------------------------------------------------------

// prepareScript adds a task's checkout of each repo (N of them: B_i, C_i,
// DEF_i, MODE_i, U_i) on the branch BR — a worktree of the base, or (MODE
// clone) a clone borrowing the base's objects (a bare base's, or an
// adopted working clone's .git's) — and prints each one's HEAD.
// A checkout already there is kept as it is.
const prepareScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}; C=\${C_$i}; DEF=\${DEF_$i}; MODE=\${MODE_$i}; U=\${U_$i}"
  if [ "$MODE" = clone ]; then
    if [ ! -e "$C/.git" ]; then
      mkdir -p "$C"
      git init -q "$C"
      mkdir -p "$C/.git/objects/info"
      O=$(cd "$B" && cd "$(git rev-parse --git-common-dir)" && pwd)/objects
      printf '%s\n' "$O" > "$C/.git/objects/info/alternates"
      git -C "$C" fetch -q "$B" "+refs/remotes/origin/*:refs/remotes/origin/*"
      if git -C "$C" show-ref -q --verify "refs/remotes/origin/$BR"; then
        git -C "$C" checkout -q -b "$BR" "origin/$BR"
      else
        git -C "$C" checkout -q -b "$BR" "origin/$DEF"
      fi
      git -C "$C" remote add origin "$U"
      git -C "$C" config push.default current
      git -C "$C" config push.autoSetupRemote true
      git -C "$C" config core.logAllRefUpdates true
      G=$C
      ` + gitCfgLoop + `      unset G
      git -C "$B" config gc.auto 0
    fi
  else
    git -C "$B" worktree prune
    if [ ! -e "$C/.git" ]; then
      mkdir -p "$(dirname "$C")"
      if git -C "$B" show-ref -q --verify "refs/heads/$BR"; then
        git -C "$B" worktree add -q "$C" "$BR"
      elif git -C "$B" show-ref -q --verify "refs/remotes/origin/$BR"; then
        git -C "$B" worktree add -q --track -b "$BR" "$C" "origin/$BR"
      else
        git -C "$B" worktree add -q --no-track -b "$BR" "$C" "origin/$DEF"
      fi
    fi
  fi
  printf 'HEAD %s %s\n' "$i" "$(git -C "$C" rev-parse HEAD)"
  i=$((i+1))
done
`

// branchTakenScript prints "taken" when any base (B_i) already has BR on
// the remote: a branch that isn't this task's (a new task's first prepare).
const branchTakenScript = `i=0
while [ "$i" -lt "$N" ]; do
  eval "B=\${B_$i}"
  if git -C "$B" show-ref -q --verify "refs/remotes/origin/$BR"; then echo taken; fi
  i=$((i+1))
done
`

// taskRepos is a task's repos, in its order, and the ref it works in.
func taskRepos(p *Project, k *ProjectTask) ([]ProjectRepo, error) {
	all, err := projAg().db.projectRepos(p.ID)
	if err != nil {
		return nil, err
	}
	var out []ProjectRepo
	for _, slug := range k.Repos {
		for _, r := range all {
			if r.Slug == slug {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// taskRef is the sandbox a task works in: its fork, else the project's.
func taskRef(p *Project, k *ProjectTask) string { return orStr(k.SandboxRef, p.SandboxRef) }

// jobPrepare makes a task's checkouts once the project's sandbox and the
// task's repos are ready: a fork first for a big task (provisionFork; this
// build without forks works in the project's sandbox), fresh credentials, a
// fetch when the last is older than 2 min, a free branch, the checkouts in
// one Run, .task-env; then the setup jobs and bind.
func jobPrepare(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if k == nil || k.RunID == 0 || k.Phase != phaseOpen && k.Phase != phasePR {
		return doneJob("the task is over")
	}
	if p.SandboxRef == "" || p.Dir == "" {
		if js := projAg().db.jobsWhere(`WHERE project_id=? AND task_id=0 AND kind=? ORDER BY id DESC LIMIT 1`, p.ID, pjSandbox); len(js) > 0 && js[0].State == pjFailed {
			return jobOutcome{}, jobFail("the project's sandbox failed: %s", js[0].Error)
		}
		_ = projAg().db.Tx(func(t *DB) error { setWS(t, p, k, wsQueued, ""); return nil })
		return waitJob(2000, "waiting for the project's sandbox")
	}
	repos, err := taskRepos(p, k)
	if err != nil {
		return jobOutcome{}, err
	}
	if len(repos) == 0 {
		return jobOutcome{}, jobFail("the task works in no repo of the project")
	}
	for _, r := range repos {
		switch r.State {
		case "ready":
		case "failed":
			return jobOutcome{}, jobFail("the repo %s failed: %s", r.Repo, r.Error)
		default:
			_ = projAg().db.Tx(func(t *DB) error {
				setWS(t, p, k, wsQueued, "")
				if t.liveJob(p.ID, 0, r.Slug, pjRepo) == nil && r.Mode == repoBare {
					_, err := t.queueJob(p.ID, 0, r.Slug, pjRepo, j.By, 0)
					return err
				}
				return nil
			})
			return waitJob(2000, "waiting for "+r.Repo)
		}
	}
	// A sign-in the task waits for holds until the credential is there
	// (stepCreds, below): this step looks again every 10 s and whenever
	// another job of the project ends, and each look must not move the
	// task to preparing and back — its run would leave the park that
	// shows the person the device code, and come back to it.
	if k.WS != wsSignin {
		_ = projAg().db.Tx(func(t *DB) error { setWS(t, p, k, wsPreparing, ""); return nil })
	}
	if k.Size == sizeBig && !k.ForkMade {
		ref, err := provisionFork(ctx, p, k)
		switch {
		case err == nil && ref != "":
			k.SandboxRef, k.ForkMade = ref, true
			if err := projAg().db.setTask(k.ID, map[string]any{"sandbox_ref": ref, "fork_made": 1}); err != nil {
				return jobOutcome{}, err
			}
			// the rest is claimed again under the fork's lock (lockKey)
			return jobOutcome{Step: "its own sandbox is made"}, nil
		case err != nil && !errors.Is(err, errNotInBuild):
			return jobOutcome{}, err
		}
	}
	ref := taskRef(p, k)
	s, err := openWsbx(ctx, p, ref)
	if err != nil {
		return jobOutcome{}, err
	}
	if signin, err := stepCreds(ctx, p, k, ref); signin {
		if nowMs()-j.Created > signinWait.Milliseconds() {
			return jobOutcome{}, jobFail("the sign-in to %s wasn't finished: sign in, then Retry", orStr(p.Host, "the scm provider"))
		}
		_ = projAg().db.Tx(func(t *DB) error { setWS(t, p, k, wsSignin, ""); return nil })
		if projectJobKinds[pjCreds] != nil {
			_, _ = projAg().db.queueJob(p.ID, k.ID, "", pjCreds, j.By, 0)
		}
		return waitJob(10000, "waiting for a sign-in")
	} else if err != nil {
		return jobOutcome{}, err
	}
	if k.WS == wsSignin {
		_ = projAg().db.Tx(func(t *DB) error { setWS(t, p, k, wsPreparing, ""); return nil })
	}
	for _, r := range repos {
		if r.Mode == repoBare && nowMs()-r.FetchedMs > 2*60*1000 {
			if err := fetchRepo(ctx, s, p, r); err != nil {
				return jobOutcome{}, err
			}
		}
	}
	dir := p.Dir + "/tasks/" + strconv.FormatInt(k.N, 10) + "-" + k.Slug
	if k.Dir != dir {
		k.Dir = dir
		if err := projAg().db.setTask(k.ID, map[string]any{"dir": dir}); err != nil {
			return jobOutcome{}, err
		}
	}
	env := map[string]string{"N": strconv.Itoa(len(repos)), "GIT_TERMINAL_PROMPT": "0"}
	for i, r := range repos {
		env[fmt.Sprintf("B_%d", i)] = basePath(p, r)
	}
	if len(projAg().db.checkouts(k.ID)) == 0 {
		base := k.Branch
		for try := 2; ; try++ {
			env["BR"] = k.Branch
			out, err := s.must(ctx, "checking the branch", branchTakenScript, env, "", time.Minute)
			if err != nil {
				return jobOutcome{}, err
			}
			if !strings.Contains(out, "taken") {
				break
			}
			if try > 20 {
				return jobOutcome{}, jobFail("no free branch near %s on the remote", base)
			}
			k.Branch = fmt.Sprintf("%s-%d", base, try)
		}
		if k.Branch != base {
			if err := projAg().db.setTask(k.ID, map[string]any{"branch": k.Branch}); err != nil {
				return jobOutcome{}, err
			}
		}
	}
	env["BR"] = k.Branch
	gitCfgEnv(env, p, s.box.Home)
	for i, r := range repos {
		mode := coWorktree
		if r.Checkout == coClone {
			mode = coClone
		}
		env[fmt.Sprintf("C_%d", i)] = dir + "/" + r.Slug
		env[fmt.Sprintf("DEF_%d", i)] = r.DefaultBranch
		env[fmt.Sprintf("MODE_%d", i)] = mode
		env[fmt.Sprintf("U_%d", i)] = r.URL
	}
	if _, err := s.must(ctx, "preparing the checkouts", prepareScript, env, "", 5*time.Minute); err != nil {
		return jobOutcome{}, err
	}
	tenv := taskEnv(p, k, s.box.Home)
	if _, err := s.conn.WriteFile(ctx, s.id, dir+"/.task-env", strings.NewReader(taskEnvFile(tenv)), sbxWrite{Mode: "0644", Mkdirs: true}); err != nil {
		return jobOutcome{}, err
	}
	if gh := tenv["GH_CONFIG_DIR"]; gh != "" {
		_, _ = s.conn.WriteFile(ctx, s.id, p.Dir+"/.xbin/env", strings.NewReader("GH_CONFIG_DIR="+shellQuote(gh)+"\n"), sbxWrite{Mode: "0644", Mkdirs: true})
	}
	err = projAg().db.Tx(func(t *DB) error {
		have := map[string]ProjectCheckout{}
		for _, c := range t.checkouts(k.ID) {
			have[c.Repo] = c
		}
		for _, r := range repos {
			c, ok := have[r.Slug]
			if !ok || c.State == "pending" || c.State == "removed" || c.State == "failed" {
				mode := coWorktree
				if r.Checkout == coClone {
					mode = coClone
				}
				c = ProjectCheckout{TaskID: k.ID, Repo: r.Slug, Path: dir + "/" + r.Slug, Mode: mode, State: "added"}
				if err := t.putCheckout(c); err != nil {
					return err
				}
			}
			if r.Setup != "" && c.SetupExit == nil {
				if _, err := t.queueJob(p.ID, k.ID, r.Slug, pjSetup, j.By, 0); err != nil {
					return err
				}
			}
		}
		_, err := t.queueJob(p.ID, k.ID, "", pjBind, j.By, 0)
		onTaskChange(t, p, k, "ws")
		return err
	})
	if err != nil {
		return jobOutcome{}, err
	}
	return doneJob("the checkouts are ready")
}
