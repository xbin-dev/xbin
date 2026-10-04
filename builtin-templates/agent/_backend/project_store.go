// project_store.go — Projects' rows (API.md §Projects and tasks): the
// tables (projects, project_members, project_repos, project_tasks,
// project_checkouts, project_jobs, project_queue, project_events), made by
// addProjectSchema from schemaAdds — additive and idempotent, never on the
// team database; reading and writing them; names (uid, slugs, branches,
// ports); the policy with its defaults; a project's ACL (the rootACL shape,
// so level() is reused) and the copy of it on its task conversations; and
// projectRefOf, the one way any code learns a run's role in a project.
//
// The shapes are projects_types.go's; the hooks this part fills for the
// others (projectsInSandbox, projectReposOf, projectLevelOf) are set in
// init() below.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addProjectSchema)
	projectsInSandbox = func(ref string) []*Project {
		if projAg() == nil || ref == "" {
			return nil
		}
		ps, _ := projAg().db.projectsWhere(`WHERE (sandbox_ref=? OR id IN
			(SELECT project_id FROM project_tasks WHERE sandbox_ref=? AND run_id<>0))`, ref, ref)
		return ps
	}
	projectReposOf = func(pid int64) []ProjectRepo {
		if projAg() == nil {
			return nil
		}
		rs, _ := projAg().db.projectRepos(pid)
		return rs
	}
	projectLevelOf = func(p *Project, user string) level {
		if projAg() == nil || p == nil || user == "" {
			return lvNone
		}
		a, err := projAg().db.projectACL(p.ID)
		if err != nil {
			return lvNone
		}
		return a.level(who{kind: whoUser, user: user, level: "read"})
	}
}

// projectSchema is every Projects table (API.md §Projects and tasks). New
// columns are added with addColumn below, never by editing a CREATE.
const projectSchema = `
CREATE TABLE IF NOT EXISTS projects (
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  uid          TEXT    NOT NULL UNIQUE,
  name         TEXT    NOT NULL,
  slug         TEXT    NOT NULL,
  kind         TEXT    NOT NULL DEFAULT 'personal',
  team_ref     INTEGER NOT NULL DEFAULT 0,
  owner        TEXT    NOT NULL DEFAULT '',
  visibility   TEXT    NOT NULL DEFAULT 'private',
  team_role    TEXT    NOT NULL DEFAULT 'viewer',
  scm          TEXT    NOT NULL DEFAULT '',
  host         TEXT    NOT NULL DEFAULT '',
  sandbox_ref  TEXT    NOT NULL DEFAULT '',
  sandbox_made INTEGER NOT NULL DEFAULT 0,
  dir          TEXT    NOT NULL DEFAULT '',
  policy       TEXT    NOT NULL DEFAULT '{}',
  fork_snap    TEXT    NOT NULL DEFAULT '',
  fork_snap_ms INTEGER NOT NULL DEFAULT 0,
  state        TEXT    NOT NULL DEFAULT 'active',
  version      INTEGER NOT NULL DEFAULT 1,
  from_seed    INTEGER NOT NULL DEFAULT 0,
  def_hash     TEXT    NOT NULL DEFAULT '',
  def_pending  TEXT    NOT NULL DEFAULT '',
  created_by   TEXT    NOT NULL DEFAULT '',
  created_ms   INTEGER NOT NULL DEFAULT 0,
  updated_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_projects_slug ON projects(slug) WHERE state<>'deleting';
CREATE INDEX IF NOT EXISTS idx_projects_team ON projects(team_ref) WHERE team_ref<>0;
CREATE INDEX IF NOT EXISTS idx_projects_sbx ON projects(sandbox_ref) WHERE sandbox_ref<>'';

CREATE TABLE IF NOT EXISTS project_members (
  project_id INTEGER NOT NULL,
  user       TEXT    NOT NULL,
  role       TEXT    NOT NULL DEFAULT 'participant',
  added_by   TEXT    NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, user)
);

CREATE TABLE IF NOT EXISTS project_repos (
  project_id     INTEGER NOT NULL,
  slug           TEXT    NOT NULL,
  repo           TEXT    NOT NULL,
  url            TEXT    NOT NULL DEFAULT '',
  default_branch TEXT    NOT NULL DEFAULT '',
  base_path      TEXT    NOT NULL DEFAULT '',
  mode           TEXT    NOT NULL DEFAULT 'bare',
  checkout       TEXT    NOT NULL DEFAULT 'worktree',
  setup          TEXT    NOT NULL DEFAULT '',
  state          TEXT    NOT NULL DEFAULT 'pending',
  error          TEXT    NOT NULL DEFAULT '',
  fetched_ms     INTEGER NOT NULL DEFAULT 0,
  head           TEXT    NOT NULL DEFAULT '',
  protected      INTEGER NOT NULL DEFAULT -1,
  created_ms     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, slug)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_repos_repo ON project_repos(project_id, repo);

CREATE TABLE IF NOT EXISTS project_tasks (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id  INTEGER NOT NULL,
  n           INTEGER NOT NULL,
  run_id      INTEGER NOT NULL DEFAULT 0,
  title       TEXT    NOT NULL DEFAULT '',
  slug        TEXT    NOT NULL DEFAULT '',
  size        TEXT    NOT NULL DEFAULT 'small',
  branch      TEXT    NOT NULL DEFAULT '',
  issue       TEXT    NOT NULL DEFAULT '',
  repos       TEXT    NOT NULL DEFAULT '[]',
  sandbox_ref TEXT    NOT NULL DEFAULT '',
  fork_made   INTEGER NOT NULL DEFAULT 0,
  dir         TEXT    NOT NULL DEFAULT '',
  ports_base  INTEGER NOT NULL DEFAULT 0,
  ws          TEXT    NOT NULL DEFAULT 'pending',
  phase       TEXT    NOT NULL DEFAULT 'open',
  prs         TEXT    NOT NULL DEFAULT '[]',
  turn_by     TEXT    NOT NULL DEFAULT '',
  last        TEXT    NOT NULL DEFAULT '',
  setup_tail  TEXT    NOT NULL DEFAULT '',
  ci_fixes    TEXT    NOT NULL DEFAULT '{}',
  error       TEXT    NOT NULL DEFAULT '',
  from_run    INTEGER NOT NULL DEFAULT 0,
  created_by  TEXT    NOT NULL DEFAULT '',
  created_ms  INTEGER NOT NULL DEFAULT 0,
  updated_ms  INTEGER NOT NULL DEFAULT 0,
  cleaned_ms  INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ptasks_n ON project_tasks(project_id, n);
CREATE INDEX IF NOT EXISTS idx_ptasks_run ON project_tasks(run_id) WHERE run_id<>0;
CREATE INDEX IF NOT EXISTS idx_ptasks_phase ON project_tasks(project_id, phase);

CREATE TABLE IF NOT EXISTS project_checkouts (
  task_id    INTEGER NOT NULL,
  repo_slug  TEXT    NOT NULL,
  path       TEXT    NOT NULL DEFAULT '',
  mode       TEXT    NOT NULL DEFAULT 'worktree',
  state      TEXT    NOT NULL DEFAULT 'pending',
  setup_exit INTEGER,
  error      TEXT    NOT NULL DEFAULT '',
  remote_sha TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (task_id, repo_slug)
);

CREATE TABLE IF NOT EXISTS project_jobs (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  task_id    INTEGER NOT NULL DEFAULT 0,
  repo_slug  TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,
  state      TEXT    NOT NULL DEFAULT 'queued',
  step       TEXT    NOT NULL DEFAULT '',
  attempts   INTEGER NOT NULL DEFAULT 0,
  next_ms    INTEGER NOT NULL DEFAULT 0,
  exec_ref   TEXT    NOT NULL DEFAULT '',
  exec_id    TEXT    NOT NULL DEFAULT '',
  client_id  TEXT    NOT NULL DEFAULT '',
  out        TEXT    NOT NULL DEFAULT '',
  error      TEXT    NOT NULL DEFAULT '',
  by_user    TEXT    NOT NULL DEFAULT '',
  epoch      INTEGER NOT NULL DEFAULT 0,
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pjobs_live ON project_jobs(project_id, task_id, repo_slug, kind)
  WHERE state IN ('queued','running','waiting');
CREATE INDEX IF NOT EXISTS idx_pjobs_due ON project_jobs(state, next_ms);

CREATE TABLE IF NOT EXISTS project_queue (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  text       TEXT    NOT NULL DEFAULT '',
  source     TEXT    NOT NULL DEFAULT 'human',
  sender     TEXT    NOT NULL DEFAULT '',
  hold_park  INTEGER NOT NULL DEFAULT 0,
  dedupe     TEXT    NOT NULL DEFAULT '',
  created    INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pqueue_dedupe ON project_queue(project_id, dedupe) WHERE dedupe<>'';
CREATE INDEX IF NOT EXISTS idx_pqueue_order ON project_queue(project_id, id);

CREATE TABLE IF NOT EXISTS project_events (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL DEFAULT 0,
  kind       TEXT    NOT NULL,
  body       TEXT    NOT NULL DEFAULT '{}',
  wake       INTEGER NOT NULL DEFAULT 0,
  coord_user TEXT    NOT NULL DEFAULT '',
  delivered  INTEGER NOT NULL DEFAULT 0,
  msg_id     INTEGER NOT NULL DEFAULT 0,
  dedupe     TEXT    NOT NULL DEFAULT '',
  created    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_pevents_feed ON project_events(project_id, id);
CREATE INDEX IF NOT EXISTS idx_pevents_due ON project_events(coord_user, delivered, wake);
CREATE UNIQUE INDEX IF NOT EXISTS idx_pevents_dedupe ON project_events(project_id, dedupe) WHERE dedupe<>'';
`

// addProjectSchema makes Projects' tables (openDB → addFeatureSchemas, after
// migrate(); never on team). In a person's partition projects number from
// 2^40, as its conversations do (seedProjectIDs), so a project id says its
// home as a run id does.
func (d *DB) addProjectSchema() error {
	projACL.flush(0) // ids name another database's projects from now on
	if _, err := d.q.Exec(projectSchema); err != nil {
		return fmt.Errorf("projects schema: %w", err)
	}
	if userMode() {
		return d.seedProjectIDs()
	}
	return nil
}

// seedProjectIDs starts a person's partition's projects at 2^40 (the
// seedTriggerIDs pattern, trigger_registry.go).
func (d *DB) seedProjectIDs() error {
	if _, err := d.q.Exec(`INSERT INTO sqlite_sequence (name, seq) SELECT 'projects', ?
		WHERE NOT EXISTS (SELECT 1 FROM sqlite_sequence WHERE name='projects')`, partitionIDBase-1); err != nil {
		return err
	}
	_, err := d.q.Exec(`UPDATE sqlite_sequence SET seq=? WHERE name='projects' AND seq < ?`, partitionIDBase-1, partitionIDBase-1)
	return err
}

// --- projects ---------------------------------------------------------------------

const projectCols = `id, uid, name, slug, kind, team_ref, owner, visibility, team_role, scm, host, sandbox_ref,
	sandbox_made, dir, policy, fork_snap, fork_snap_ms, state, version, from_seed, def_hash, def_pending,
	created_by, created_ms, updated_ms`

func scanProject(scan func(dest ...any) error) (*Project, error) {
	p := &Project{}
	var made, seed int
	var policy, pending string
	if err := scan(&p.ID, &p.UID, &p.Name, &p.Slug, &p.Kind, &p.TeamRef, &p.Owner, &p.Visibility, &p.TeamRole,
		&p.SCM, &p.Host, &p.SandboxRef, &made, &p.Dir, &policy, &p.ForkSnap, &p.ForkSnapMs, &p.State, &p.Version,
		&seed, &p.DefHash, &pending, &p.CreatedBy, &p.CreatedMs, &p.UpdatedMs); err != nil {
		return nil, err
	}
	p.SandboxMade, p.FromSeed = made != 0, seed != 0
	p.Policy = json.RawMessage(orStr(policy, "{}"))
	if pending != "" {
		sum := sha256.Sum256([]byte(pending))
		p.DefPending = hex.EncodeToString(sum[:])
	}
	return p, nil
}

// errNoProject: no such project here.
var errNoProject = errors.New("no such project")

func (d *DB) getProject(id int64) (*Project, error) {
	p, err := scanProject(d.q.QueryRow(`SELECT `+projectCols+` FROM projects WHERE id=?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNoProject
	}
	return p, err
}

func (d *DB) projectsWhere(where string, args ...any) ([]*Project, error) {
	rows, err := d.q.Query(`SELECT `+projectCols+` FROM projects `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Project
	for rows.Next() {
		p, err := scanProject(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// insertProject writes a new project (its uid drawn here, unique).
func (d *DB) insertProject(p *Project) error {
	if len(p.Policy) == 0 {
		p.Policy = json.RawMessage("{}")
	}
	p.CreatedMs, p.UpdatedMs, p.Version = nowMs(), nowMs(), 1
	for range 8 {
		p.UID = randomUID()
		err := d.q.QueryRow(`INSERT INTO projects (uid, name, slug, kind, team_ref, owner, visibility, team_role, scm, host,
			sandbox_ref, sandbox_made, dir, policy, state, version, from_seed, def_hash, created_by, created_ms, updated_ms)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?) RETURNING id`,
			p.UID, p.Name, p.Slug, p.Kind, p.TeamRef, p.Owner, p.Visibility, p.TeamRole, p.SCM, p.Host,
			p.SandboxRef, b2i(p.SandboxMade), p.Dir, string(p.Policy), orStr(p.State, projActive), b2i(p.FromSeed),
			p.DefHash, p.CreatedBy, p.CreatedMs, p.UpdatedMs).Scan(&p.ID)
		if err == nil {
			return nil
		}
		if !strings.Contains(err.Error(), "projects.uid") {
			return err
		}
	}
	return errors.New("couldn't draw a free project uid")
}

// touchProject bumps a project's updated time (an activity the list sorts by).
func (d *DB) touchProject(pid int64) {
	_, _ = d.q.Exec(`UPDATE projects SET updated_ms=? WHERE id=?`, nowMs(), pid)
}

// --- names ---------------------------------------------------------------------------

const uidAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// randomUID is 6 × [a-z0-9] (projects.uid: branches, paths, labels).
func randomUID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	for i := range b {
		b[i] = uidAlphabet[int(b[i])%len(uidAlphabet)]
	}
	return string(b[:])
}

// slugOf is s lowercased, every run of other characters one "-", trimmed,
// at most max ("" when nothing is left).
func slugOf(s string, max int) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if len(out) > max {
		out = strings.TrimRight(out[:max], "-")
	}
	return out
}

// freeSlug is base, or base-2, base-3… — the first taken returns false.
func freeSlug(base string, max int, taken func(string) bool) string {
	if !taken(base) {
		return base
	}
	for i := 2; ; i++ {
		suf := "-" + strconv.Itoa(i)
		b := base
		if len(b)+len(suf) > max {
			b = strings.TrimRight(b[:max-len(suf)], "-")
		}
		if s := b + suf; !taken(s) {
			return s
		}
	}
}

// projectSlug is a free workspace directory name for a project called name.
func (d *DB) projectSlug(name string) string {
	return freeSlug(orStr(slugOf(name, 40), "project"), 40, func(s string) bool {
		var n int
		_ = d.q.QueryRow(`SELECT count(*) FROM projects WHERE slug=? AND state<>'deleting'`, s).Scan(&n)
		return n > 0
	})
}

// validRepo: owner/name, each part of the characters hosts allow.
func validRepo(repo string) bool {
	o, n, ok := strings.Cut(repo, "/")
	part := func(s string) bool {
		if s == "" || len(s) > 100 || s == "." || s == ".." {
			return false
		}
		for _, r := range s {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
				return false
			}
		}
		return true
	}
	return ok && part(o) && part(n)
}

// branchPrefix is the policy's prefix, or xbin/<uid> (API.md §The workspace).
func (p *Project) branchPrefix(pol ProjectPolicy) string {
	if pol.BranchPrefix != "" {
		return pol.BranchPrefix
	}
	return "xbin/" + p.UID
}

// validBranchPrefix: a ref path git takes, without a trailing slash.
func validBranchPrefix(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 60 || strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") || strings.Contains(s, "..") ||
		strings.Contains(s, "//") || strings.HasSuffix(s, ".lock") || strings.HasPrefix(s, "-") {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == '/') {
			return false
		}
	}
	return true
}

// portsOf is task n's ports (API.md §The workspace).
func portsOf(pol ProjectPolicy, n int64) TaskPorts {
	slots := max(pol.Ports.Slots, 1)
	return TaskPorts{Base: pol.Ports.Base + int((n-1)%int64(slots))*pol.Ports.Span, Span: pol.Ports.Span}
}

// --- policy ----------------------------------------------------------------------------

// policyOf is a project's policy with every default filled (an unknown or
// bad stored key falls back to its default rather than failing a read).
func policyOf(raw json.RawMessage) ProjectPolicy {
	pol := defaultProjectPolicy()
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &pol)
	}
	def := defaultProjectPolicy()
	if pol.MaxTasks < 1 || pol.MaxTasks > 16 {
		pol.MaxTasks = def.MaxTasks
	}
	if pol.MaxOpenTasks < 1 {
		pol.MaxOpenTasks = def.MaxOpenTasks
	}
	if pol.MaxCreatesDay < 1 {
		pol.MaxCreatesDay = def.MaxCreatesDay
	}
	if pol.SetupTimeout < 10 {
		pol.SetupTimeout = def.SetupTimeout
	}
	if pol.Ports.Span < 1 || pol.Ports.Slots < 1 || pol.Ports.Base < 1024 {
		pol.Ports = def.Ports
	}
	if pol.FetchEveryMin < 1 {
		pol.FetchEveryMin = def.FetchEveryMin
	}
	if pol.TaskClass == "" {
		pol.TaskClass = def.TaskClass
	}
	return pol
}

// policyView is the policy as GET /projects/{pid} shows it: every key, the
// defaults filled, and the keys this build doesn't know kept as stored.
func policyView(raw json.RawMessage) json.RawMessage {
	filled, _ := json.Marshal(policyOf(raw))
	var out, stored map[string]json.RawMessage
	_ = json.Unmarshal(filled, &out)
	if json.Unmarshal(raw, &stored) == nil {
		for k, v := range stored {
			if _, ok := out[k]; !ok {
				out[k] = v
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// mergePolicy applies a PATCH's policy object to the stored one: objects
// merge key by key, anything else replaces, null removes the key (back to
// its default).
func mergePolicy(stored, patch json.RawMessage) (json.RawMessage, error) {
	var a, b map[string]json.RawMessage
	if err := json.Unmarshal(orRaw(stored), &a); err != nil {
		a = map[string]json.RawMessage{}
	}
	if err := json.Unmarshal(patch, &b); err != nil {
		return nil, fmt.Errorf("policy: an object")
	}
	if a == nil {
		a = map[string]json.RawMessage{}
	}
	for k, v := range b {
		switch t := strings.TrimSpace(string(v)); {
		case t == "null":
			delete(a, k)
		case strings.HasPrefix(t, "{") && strings.HasPrefix(strings.TrimSpace(string(a[k])), "{"):
			m, err := mergePolicy(a[k], v)
			if err != nil {
				return nil, fmt.Errorf("policy.%s: %w", k, err)
			}
			a[k] = m
		default:
			a[k] = v
		}
	}
	return json.Marshal(a)
}

func orRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 {
		return json.RawMessage("{}")
	}
	return r
}

// checkPolicy says what is wrong with a policy a person sent ("" = nothing).
func checkPolicy(raw json.RawMessage) string {
	var probe ProjectPolicy
	if err := json.Unmarshal(raw, &probe); err != nil {
		return "policy: " + err.Error()
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	pol := policyOf(raw)
	in := func(v string, ok ...string) bool { return v == "" || hasStr(ok, v) }
	switch {
	case keys["maxTasks"] != nil && (probe.MaxTasks < 1 || probe.MaxTasks > 16):
		return "policy.maxTasks: 1 to 16"
	case !in(pol.Engine, "auto", "builtin", "harness"):
		return "policy.engine: auto, builtin or harness"
	case !in(pol.As, scmAsPerson, scmAsBot):
		return "policy.as: person or bot"
	case !in(pol.AutoPR, "off", "draft", "ready"):
		return "policy.autoPR: off, draft or ready"
	case !in(pol.Checkout, coWorktree, coClone):
		return "policy.checkout: worktree or clone"
	case !in(pol.Protection, "warn", "refuse"):
		return "policy.protection: warn or refuse"
	case !in(pol.Reviews.Forward, "trusted", "all", "off"):
		return "policy.reviews.forward: trusted, all or off"
	case !in(pol.BigTasks.Mode, "fork", "fresh"):
		return "policy.bigTasks.mode: fork or fresh"
	case !validBranchPrefix(pol.BranchPrefix):
		return "policy.branchPrefix: a branch path such as team/web (letters, digits, - _ . /)"
	case len(pol.Instructions) > 32<<10 || len(pol.PRConventions) > 8<<10 || len(pol.Checks) > 20:
		return "policy: instructions ≤ 32 KiB, prConventions ≤ 8 KiB, at most 20 checks"
	case keys["ports"] != nil && (probe.Ports.Base < 1024 || probe.Ports.Span < 1 || probe.Ports.Slots < 1 ||
		probe.Ports.Base+probe.Ports.Span*probe.Ports.Slots > 65535):
		return "policy.ports: base ≥ 1024, span and slots ≥ 1, ending below 65536"
	}
	return ""
}

// --- members, ACL ------------------------------------------------------------------------

// projectMember is a row of project_members.
type projectMember struct {
	User string `json:"user"`
	Role string `json:"role"`
}

func (d *DB) projectMembers(pid int64) []projectMember {
	out := []projectMember{}
	rows, err := d.q.Query(`SELECT user, role FROM project_members WHERE project_id=? ORDER BY user`, pid)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var m projectMember
		if rows.Scan(&m.User, &m.Role) == nil {
			out = append(out, m)
		}
	}
	return out
}

// projACL caches projects' ACLs (keyed by project id: apart from the runs'
// cache, whose ids are another space). Every change here flushes it.
var projACL aclCache

// loadProjectACL is a project's ACL in the rootACL shape (acl.go), so a
// caller's level on it is level().
func (d *DB) loadProjectACL(pid int64) (*rootACL, error) {
	a := &rootACL{root: pid, members: map[string]string{}, loaded: time.Now()}
	if err := d.q.QueryRow(`SELECT owner, visibility, team_role FROM projects WHERE id=?`, pid).
		Scan(&a.owner, &a.visibility, &a.teamRole); err != nil {
		return nil, err
	}
	for _, m := range d.projectMembers(pid) {
		a.members[m.User] = m.Role
	}
	return a, nil
}

// projectACL is the cached ACL of project pid.
func (d *DB) projectACL(pid int64) (*rootACL, error) {
	if a := projACL.get(pid); a != nil {
		return a, nil
	}
	a, err := d.loadProjectACL(pid)
	if err != nil {
		return nil, err
	}
	projACL.put(a)
	return a, nil
}

// projectLevel is w's level on project p (lvNone: they may not see it).
func (d *DB) projectLevel(w who, pid int64) level {
	a, err := d.projectACL(pid)
	if err != nil {
		return lvNone
	}
	return a.level(w)
}

// copyACLToTasks writes the project's sharing onto every task conversation
// it has (API.md §Projects and tasks; the precedent of triggers_admin.go): the run
// owner stays the task's creator; visibility, team role and members are the
// project's (writeTaskMembers). Coordinators stay private. The caller
// flushes and re-publishes (afterACLChange).
func (d *DB) copyACLToTasks(pid int64) ([]int64, error) {
	p, err := d.getProject(pid)
	if err != nil {
		return nil, err
	}
	type task struct {
		run     int64
		creator string
	}
	var tasks []task
	rows, err := d.q.Query(`SELECT run_id, created_by FROM project_tasks WHERE project_id=? AND run_id<>0`, pid)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k task
		if rows.Scan(&k.run, &k.creator) == nil {
			tasks = append(tasks, k)
		}
	}
	rows.Close()
	members := d.projectMembers(pid)
	runs := make([]int64, 0, len(tasks))
	for _, k := range tasks {
		if _, err := d.q.Exec(`UPDATE runs SET visibility=?, team_role=? WHERE id=?`, p.Visibility, p.TeamRole, k.run); err != nil {
			return nil, err
		}
		if _, err := d.q.Exec(`DELETE FROM run_members WHERE run_id=?`, k.run); err != nil {
			return nil, err
		}
		if err := d.writeTaskMembers(k.run, p, members, k.creator); err != nil {
			return nil, err
		}
		runs = append(runs, k.run)
	}
	return runs, nil
}

// writeTaskMembers gives task conversation runID the project's members and
// — when someone else created the task — the project's owner as a
// participant: the owner may do on every task what a participant may
// (API.md §Projects and tasks), and the task's sandbox is bound with the
// owner's authority (jobBind), which every tool call re-checks as a
// participant of the conversation (sandboxUse). The owner is never a
// member row of the project, so nothing else puts them there.
func (d *DB) writeTaskMembers(runID int64, p *Project, members []projectMember, creator string) error {
	for _, m := range members {
		if _, err := d.q.Exec(`INSERT OR REPLACE INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, ?)`,
			runID, m.User, m.Role, now()); err != nil {
			return err
		}
	}
	if p.Owner == "" || p.Owner == creator || strings.HasPrefix(p.Owner, "el:") {
		return nil // the owner's own task; a component's project has no one else's (shareClash)
	}
	_, err := d.q.Exec(`INSERT OR REPLACE INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, ?)`,
		runID, p.Owner, roleParticipant, now())
	return err
}

// shareClash is the refusal when project pid may not be shared as it now
// stands (nil when it may): a component's project with tasks is never
// shared — its tasks are bound with the component's authority, and a
// component takes part in no one else's conversation, so a person's task
// there could use no sandbox — and a shared project's sandbox is its own
// (sandboxShareClash). (409; in the caller's transaction, after the change.)
func (d *DB) shareClash(pid int64, ref string) error {
	var owner, kind string
	var shared int
	if err := d.q.QueryRow(`SELECT owner, kind, (visibility='team') + (SELECT count(*) FROM project_members WHERE project_id=projects.id)
		FROM projects WHERE id=?`, pid).Scan(&owner, &kind, &shared); err != nil {
		return err
	}
	if shared > 0 && strings.HasPrefix(owner, "el:") && kind != projTeam {
		return perr(409, "a component's project isn't shared: its tasks work with the component's authority, which no one else's task carries")
	}
	return d.sandboxShareClash(pid, ref)
}

// sandboxShareClash is the refusal when project pid may not keep its
// sandbox as its sharing now stands (nil when it may): a shared project —
// team-visible, or with members — has its sandbox to itself, and no
// project joins a sandbox a shared one holds. Whoever a project's task
// conversations let run commands in its sandbox reads all it holds,
// credentials another project there was given included; the credential
// gate judges a sandbox by its own users, not by who reaches it through a
// project. A project being deleted no longer counts. (409 sandbox-shared;
// in the caller's transaction, after the change, so it rolls back.)
func (d *DB) sandboxShareClash(pid int64, ref string) error {
	if ref == "" {
		return nil
	}
	shared := func(id int64) bool {
		var n int
		_ = d.q.QueryRow(`SELECT (visibility='team') + (SELECT count(*) FROM project_members WHERE project_id=projects.id)
			FROM projects WHERE id=?`, id).Scan(&n)
		return n > 0
	}
	others := scanIDs(d.q.Query(`SELECT id FROM projects WHERE sandbox_ref=? AND id<>? AND state<>? ORDER BY id`, ref, pid, projDeleting))
	if len(others) == 0 {
		return nil
	}
	name := func(id int64) string {
		var n string
		_ = d.q.QueryRow(`SELECT name FROM projects WHERE id=?`, id).Scan(&n)
		return n
	}
	why := ""
	if shared(pid) {
		why = fmt.Sprintf("the project's sandbox also holds the project %s: a shared project needs a sandbox of its own (its people could read what the other project keeps there)", name(others[0]))
	} else {
		for _, id := range others {
			if shared(id) {
				why = fmt.Sprintf("the sandbox holds the shared project %s, which needs it to itself (its people could read what this project keeps there)", name(id))
				break
			}
		}
	}
	if why == "" {
		return nil
	}
	return &projErr{code: 409, refusal: refusalSandboxShared, msg: why}
}

// afterACLChange flushes the caches a project's sharing change touched and
// tells the live streams (after the commit).
func (ag *Agent) afterACLChange(pid int64, runs []int64) {
	projACL.flush(pid)
	for _, id := range runs {
		ag.acl.flush(id)
		if e := projEng(); e != nil {
			if a, err := ag.aclOf(id); err == nil {
				e.hub.revalidate(id, a)
			}
			e.publishRun(id)
		}
	}
}
