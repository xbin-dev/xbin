// project_rows.go — Projects' rows besides the projects themselves
// (project_store.go): a project's repos, its tasks and their checkouts, the
// worker's jobs, and projectRefOf — the one way any code learns a run's
// role in a project, deriving Config.Project again when an older build
// dropped it.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// --- repos -----------------------------------------------------------------------------

const repoCols = `project_id, slug, repo, url, default_branch, base_path, mode, checkout, setup, state, error,
	fetched_ms, head, protected, created_ms`

func scanRepo(scan func(dest ...any) error) (ProjectRepo, error) {
	var r ProjectRepo
	var prot int
	err := scan(&r.ProjectID, &r.Slug, &r.Repo, &r.URL, &r.DefaultBranch, &r.BasePath, &r.Mode, &r.Checkout, &r.Setup,
		&r.State, &r.Error, &r.FetchedMs, &r.Head, &prot, &r.CreatedMs)
	if prot >= 0 {
		b := prot == 1
		r.Protected = &b
	}
	return r, err
}

func (d *DB) projectRepos(pid int64) ([]ProjectRepo, error) {
	rows, err := d.q.Query(`SELECT `+repoCols+` FROM project_repos WHERE project_id=? ORDER BY created_ms, slug`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjectRepo{}
	for rows.Next() {
		r, err := scanRepo(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) projectRepo(pid int64, slug string) (ProjectRepo, error) {
	r, err := scanRepo(d.q.QueryRow(`SELECT `+repoCols+` FROM project_repos WHERE project_id=? AND slug=?`, pid, slug).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errNoProject
	}
	return r, err
}

// repoNamed is the project's repo owner/name (false: not one of its).
func (d *DB) repoNamed(pid int64, repo string) (ProjectRepo, bool) {
	r, err := scanRepo(d.q.QueryRow(`SELECT `+repoCols+` FROM project_repos WHERE project_id=? AND lower(repo)=lower(?)
		AND state<>'removing'`, pid, repo).Scan)
	return r, err == nil
}

// repoSlug is a free slug in project pid for repo owner/name.
func (d *DB) repoSlug(pid int64, repo, want string) string {
	_, name, _ := strings.Cut(repo, "/")
	base := orStr(slugOf(want, 40), orStr(slugOf(name, 40), "repo"))
	return freeSlug(base, 40, func(s string) bool {
		var n int
		_ = d.q.QueryRow(`SELECT count(*) FROM project_repos WHERE project_id=? AND slug=?`, pid, s).Scan(&n)
		return n > 0
	})
}

func (d *DB) insertRepo(r *ProjectRepo) error {
	prot := -1
	if r.Protected != nil {
		prot = b2i(*r.Protected)
	}
	r.CreatedMs = nowMs()
	_, err := d.q.Exec(`INSERT INTO project_repos (project_id, slug, repo, url, default_branch, base_path, mode, checkout,
		setup, state, protected, created_ms) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Slug, r.Repo, r.URL, r.DefaultBranch, r.BasePath, orStr(r.Mode, repoBare),
		orStr(r.Checkout, coWorktree), r.Setup, orStr(r.State, "pending"), prot, r.CreatedMs)
	return err
}

// --- tasks -----------------------------------------------------------------------------

const taskCols = `id, project_id, n, run_id, title, slug, size, branch, issue, repos, sandbox_ref, fork_made, dir,
	ports_base, ws, phase, prs, turn_by, last, setup_tail, ci_fixes, error, from_run, created_by, created_ms,
	updated_ms, cleaned_ms`

func scanTask(scan func(dest ...any) error) (*ProjectTask, error) {
	k := &ProjectTask{}
	var issue, repos, prs, fixes string
	var fork int
	if err := scan(&k.ID, &k.ProjectID, &k.N, &k.RunID, &k.Title, &k.Slug, &k.Size, &k.Branch, &issue, &repos,
		&k.SandboxRef, &fork, &k.Dir, &k.PortsBase, &k.WS, &k.Phase, &prs, &k.TurnBy, &k.Last, &k.SetupTail,
		&fixes, &k.Error, &k.FromRun, &k.CreatedBy, &k.CreatedMs, &k.UpdatedMs, &k.CleanedMs); err != nil {
		return nil, err
	}
	k.ForkMade = fork != 0
	if issue != "" {
		var ir IssueRef
		if json.Unmarshal([]byte(issue), &ir) == nil {
			k.Issue = &ir
		}
	}
	_ = json.Unmarshal([]byte(repos), &k.Repos)
	if k.Repos == nil {
		k.Repos = []string{}
	}
	k.PRs = json.RawMessage(orStr(prs, "[]"))
	k.CIFixes = json.RawMessage(orStr(fixes, "{}"))
	return k, nil
}

func (d *DB) tasksWhere(where string, args ...any) ([]*ProjectTask, error) {
	rows, err := d.q.Query(`SELECT `+taskCols+` FROM project_tasks `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ProjectTask
	for rows.Next() {
		k, err := scanTask(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (d *DB) taskWhere(where string, args ...any) (*ProjectTask, error) {
	k, err := scanTask(d.q.QueryRow(`SELECT `+taskCols+` FROM project_tasks `+where, args...).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoProject
	}
	return k, err
}

// taskByN is task n of project pid.
func (d *DB) taskByN(pid, n int64) (*ProjectTask, error) {
	return d.taskWhere(`WHERE project_id=? AND n=?`, pid, n)
}

// taskByRun is the task whose conversation run is (nil: none).
func (d *DB) taskByRun(runID int64) *ProjectTask {
	if runID == 0 {
		return nil
	}
	k, err := d.taskWhere(`WHERE run_id=?`, runID)
	if err != nil {
		return nil
	}
	return k
}

func (d *DB) taskByID(id int64) (*ProjectTask, error) { return d.taskWhere(`WHERE id=?`, id) }

// taskPRs is a task's PRs as a list.
func (k *ProjectTask) taskPRs() []TaskPR {
	out := []TaskPR{}
	_ = json.Unmarshal(k.PRs, &out)
	return out
}

// setTask writes the named columns of a task (and updated_ms).
func (d *DB) setTask(id int64, cols map[string]any) error {
	if len(cols) == 0 {
		return nil
	}
	var sets []string
	var args []any
	for c, v := range cols {
		sets = append(sets, c+"=?")
		args = append(args, v)
	}
	args = append(args, nowMs(), id)
	_, err := d.q.Exec(`UPDATE project_tasks SET `+strings.Join(sets, ", ")+`, updated_ms=? WHERE id=?`, args...)
	return err
}

// checkouts are a task's rows of project_checkouts.
func (d *DB) checkouts(taskID int64) []ProjectCheckout {
	out := []ProjectCheckout{}
	rows, err := d.q.Query(`SELECT task_id, repo_slug, path, mode, state, setup_exit, error, remote_sha
		FROM project_checkouts WHERE task_id=? ORDER BY repo_slug`, taskID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c ProjectCheckout
		var exit sql.NullInt64
		if rows.Scan(&c.TaskID, &c.Repo, &c.Path, &c.Mode, &c.State, &exit, &c.Error, &c.RemoteSHA) == nil {
			if exit.Valid {
				x := int(exit.Int64)
				c.SetupExit = &x
			}
			out = append(out, c)
		}
	}
	return out
}

func (d *DB) putCheckout(c ProjectCheckout) error {
	var exit any
	if c.SetupExit != nil {
		exit = *c.SetupExit
	}
	_, err := d.q.Exec(`INSERT INTO project_checkouts (task_id, repo_slug, path, mode, state, setup_exit, error, remote_sha)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(task_id, repo_slug) DO UPDATE SET path=excluded.path, mode=excluded.mode,
		state=excluded.state, setup_exit=excluded.setup_exit, error=excluded.error, remote_sha=excluded.remote_sha`,
		c.TaskID, c.Repo, c.Path, c.Mode, c.State, exit, c.Error, c.RemoteSHA)
	return err
}

// --- the role of a run ---------------------------------------------------------------------

// projectRefOf is run's role in its project: Config.Project when the run
// has it, else — for a run with origin project only — the role derived from
// what is authoritative (project_tasks.run_id for a task, the session key
// proj:<pid>:coord:<user> for a coordinator) and written back to the run's
// config, which an older build may have dropped (API.md §Projects and tasks,
// "Rolling back"). nil: the run plays no role in a project. Every reader of
// a project run's role goes through here, never the stored field alone.
func (d *DB) projectRefOf(run *Run) *ProjectRef {
	if run == nil || run.Origin != originProject || hostedID(run.ID) || !d.features {
		return nil
	}
	cfg, err := d.runConfig(run.ID)
	if err != nil {
		return nil
	}
	if cfg.Project != nil {
		return cfg.Project
	}
	var ref *ProjectRef
	switch {
	case run.ParentID != 0:
		if root, err := d.getRun(rootOf(run)); err == nil && root.ID != run.ID {
			ref = d.projectRefOf(root)
		}
	case strings.HasPrefix(run.SessionKey, "proj:") && strings.Contains(run.SessionKey, ":coord:"):
		pid, _ := strconv.ParseInt(strings.SplitN(strings.TrimPrefix(run.SessionKey, "proj:"), ":", 2)[0], 10, 64)
		if pid > 0 {
			ref = &ProjectRef{ID: pid, Role: projRoleCoordinator}
		}
	default:
		if k := d.taskByRun(run.ID); k != nil {
			ref = &ProjectRef{ID: k.ProjectID, Role: projRoleTask, N: k.N}
		}
	}
	if ref != nil {
		b, _ := json.Marshal(ref)
		_, _ = d.q.Exec(`UPDATE runs SET config=json_set(CASE WHEN json_valid(config) THEN config ELSE '{}' END, '$.project', json(?))
			WHERE id=?`, string(b), run.ID)
	}
	return ref
}

// projectOfRun is the project and task of a task run (nil, nil otherwise).
func (d *DB) projectOfRun(run *Run) (*Project, *ProjectTask) {
	ref := d.projectRefOf(run)
	if !ref.isTask() || run.ParentID != 0 {
		return nil, nil
	}
	k := d.taskByRun(run.ID)
	if k == nil {
		return nil, nil
	}
	p, err := d.getProject(k.ProjectID)
	if err != nil {
		return nil, nil
	}
	return p, k
}

// --- jobs ----------------------------------------------------------------------------------

const pjobCols = `id, project_id, task_id, repo_slug, kind, state, step, attempts, next_ms, exec_ref, exec_id,
	client_id, out, error, by_user, epoch, created_ms, updated_ms`

func scanPJob(scan func(dest ...any) error) (*ProjectJob, error) {
	j := &ProjectJob{}
	err := scan(&j.ID, &j.Project, &j.Task, &j.Repo, &j.Kind, &j.State, &j.Step, &j.Attempts, &j.NextMs, &j.ExecRef,
		&j.ExecID, &j.ClientID, &j.Out, &j.Error, &j.By, &j.Epoch, &j.Created, &j.Updated)
	return j, err
}

func (d *DB) jobsWhere(where string, args ...any) []*ProjectJob {
	rows, err := d.q.Query(`SELECT `+pjobCols+` FROM project_jobs `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*ProjectJob
	for rows.Next() {
		if j, err := scanPJob(rows.Scan); err == nil {
			out = append(out, j)
		}
	}
	return out
}

func (d *DB) getJob(id int64) (*ProjectJob, error) {
	j, err := scanPJob(d.q.QueryRow(`SELECT `+pjobCols+` FROM project_jobs WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoProject
	}
	return j, err
}

// queueJob adds a job unless one of the same (project, task, repo, kind)
// is live already (the live-job index): that one is answered. after delays
// its first run.
func (d *DB) queueJob(pid, taskID int64, repo, kind, by string, after time.Duration) (*ProjectJob, error) {
	ms := nowMs()
	var id int64
	err := d.q.QueryRow(`INSERT INTO project_jobs (project_id, task_id, repo_slug, kind, state, next_ms, by_user, created_ms, updated_ms)
		VALUES (?, ?, ?, ?, 'queued', ?, ?, ?, ?) ON CONFLICT DO NOTHING RETURNING id`,
		pid, taskID, repo, kind, ms+after.Milliseconds(), by, ms, ms).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		err = d.q.QueryRow(`SELECT id FROM project_jobs WHERE project_id=? AND task_id=? AND repo_slug=? AND kind=?
			AND state IN ('queued','running','waiting')`, pid, taskID, repo, kind).Scan(&id)
	}
	if err != nil {
		return nil, err
	}
	d.AfterCommit(kickProjectWorker)
	return d.getJob(id)
}

// liveJob is the live job of that kind (nil: none).
func (d *DB) liveJob(pid, taskID int64, repo, kind string) *ProjectJob {
	js := d.jobsWhere(`WHERE project_id=? AND task_id=? AND repo_slug=? AND kind=? AND state IN ('queued','running','waiting')`,
		pid, taskID, repo, kind)
	if len(js) == 0 {
		return nil
	}
	return js[0]
}
