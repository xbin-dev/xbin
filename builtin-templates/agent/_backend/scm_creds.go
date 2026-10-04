// scm_creds.go — a project's credential in its sandbox (API.md §scm
// providers and credentials): minted by the project's scm provider for the
// project's repos, written into the sandbox behind the gate (scm_gate.go),
// refreshed before it lapses, scrubbed when the sandbox is shared, stopped,
// archived or deleted, the project leaves it, or the person forgets their
// sign-in — and kept out of every row, log and event (harness_redact.go
// masks its shape and every live value).
//
//   - Where it goes: two files under the sandbox's home, outside every repo
//     and every worktree — H/.config/xbin-scm/<uid>/<host>.cred (git's
//     credential protocol, read by a helper line in each base repo's config)
//     and …/gh/hosts.yml (GH_CONFIG_DIR, so `gh` uses it and a person's own
//     `gh` login stays untouched). 0600 files in 0700 directories, written
//     as …tmp then renamed.
//   - What is kept: the token in memory only (scmLive); project_creds holds
//     its metadata — who, until when, the gate's last word — never the
//     value. A restart forgets the value; the next ensure mints again.
//   - Who calls: the project worker (scmEnsureCreds before every git step,
//     the creds job the workspace gate queues: scm_jobs.go), never the
//     engine's path — the gate reads scmCredsDue, which reads the database
//     alone.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// scmMinLeft is how long a credential must still run before a git step.
const scmMinLeft = 10 * time.Minute

// addSCMCredsSchema is project_creds: a credential's metadata, never the
// token (additive and idempotent; never on team).
func addSCMCredsSchema(d *DB) error {
	_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS project_creds (
  project_id  INTEGER NOT NULL,
  sandbox_ref TEXT    NOT NULL,
  host        TEXT    NOT NULL,
  identity    TEXT    NOT NULL DEFAULT '',
  login       TEXT    NOT NULL DEFAULT '',
  for_user    TEXT    NOT NULL DEFAULT '',
  purpose     TEXT    NOT NULL DEFAULT '',
  expires_ms  INTEGER NOT NULL DEFAULT 0,
  refresh_ms  INTEGER NOT NULL DEFAULT 0,
  written_ms  INTEGER NOT NULL DEFAULT 0,
  state       TEXT    NOT NULL DEFAULT 'live',
  why         TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (project_id, sandbox_ref, host)
)`)
	return err
}

func init() {
	schemaAdds = append(schemaAdds, addSCMCredsSchema)
	scmEnsureCreds = ensureCreds
	scmScrubCreds = scmScrub
	scmProjectEnv = scmEnvOf
	scmGitConfig = scmGitConfigOf
	scmRedact = func(s string) string { return redactText(s) } // the shapes and every live token (harness_redact.go)
	scmPendingSignin = scmSigninOf
	scmCredsDue = scmDue
}

// --- live tokens (memory only) --------------------------------------------------------

// scmLive is a token handed to a sandbox, as this process holds it.
type scmLive struct {
	token                     scmSecret
	host, purpose, identity   string
	login, forUser            string
	author                    scmAuthor
	expires, refresh, written int64 // unix ms
	nextTry                   int64 // the refresher looks again no sooner (unix ms)
	// scope is what it was minted for (scmScopeOf): repos and permissions
	// — a project whose repos or policy changed since gets a new one.
	scope string
}

// scmScopeOf is a token request's scope, comparable: its repos (sorted)
// and permissions.
func scmScopeOf(req scmTokenReq) string {
	perms := make([]string, 0, len(req.Permissions))
	for k, v := range req.Permissions {
		perms = append(perms, k+"="+v)
	}
	sort.Strings(perms)
	repos := append([]string(nil), req.Repos...)
	sort.Strings(repos)
	return req.Access + "|" + strings.Join(repos, ",") + "|" + strings.Join(perms, ",")
}

var (
	scmLiveMu sync.Mutex
	scmLives  = map[string]*scmLive{} // pid|sandboxRef|host
	// scmRetired: values no longer handed out, still masked until they
	// expire — output written late (a log tail, a job's last lines) may
	// still carry one.
	scmRetired = map[string]int64{}
	// scmSecrets is what every redactor masks besides the shapes: each
	// live and retired value (harness_redact.go reads it).
	scmSecrets atomic.Pointer[[][]byte]
	// scmKeyLocks serialise one credential's minting (ensure, refresh): one
	// token at a time per (project, sandbox, host).
	scmKeyLocks sync.Map
	// scmSandboxLocks serialise what goes into one sandbox and what changes
	// who can read it (scmHoldSandbox).
	scmSandboxLocks sync.Map
)

func scmLiveKey(pid int64, ref, host string) string {
	return fmt.Sprint(pid) + "|" + ref + "|" + host
}

func scmKeyLock(key string) *sync.Mutex {
	m, _ := scmKeyLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// scmHoldSandbox takes sandbox ref's lock and returns its release. Held
// across a credential's gate, its files and its row (ensureCreds), across
// every scrub there (the caller of scmScrubRow and scmScrubSandbox holds
// it), and across a share's PATCH and a stop's or archive's lifecycle call
// through the agent — so no credential is written into a sandbox between
// the scrub that finds nothing there and the change that shares it, stops
// it or archives it. Never held across a provider's call; never taken
// twice (not reentrant). Order: a credential's key lock, then this.
func scmHoldSandbox(ref string) func() {
	m, _ := scmSandboxLocks.LoadOrStore(ref, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

// scmLiveSecrets is every value the redactors mask exactly.
func scmLiveSecrets() [][]byte {
	if p := scmSecrets.Load(); p != nil {
		return *p
	}
	return nil
}

// scmMask adds a value to the exact set at once — before it is written
// anywhere, or anything could print it.
func scmMask(tok scmSecret, expires int64) {
	if len(tok.Reveal()) < 8 {
		return
	}
	scmLiveMu.Lock()
	scmRetired[tok.Reveal()] = max(expires, nowMs()+time.Hour.Milliseconds())
	scmRebuildLocked()
	scmLiveMu.Unlock()
}

// scmRebuildLocked publishes the exact set (scmLiveMu held): every live
// value, and each retired one until it expires.
func scmRebuildLocked() {
	now := nowMs()
	seen := map[string]bool{}
	var out [][]byte
	for _, l := range scmLives {
		if v := l.token.Reveal(); !seen[v] {
			seen[v] = true
			out = append(out, []byte(v))
		}
	}
	for v, exp := range scmRetired {
		if exp < now {
			delete(scmRetired, v)
		} else if !seen[v] {
			seen[v] = true
			out = append(out, []byte(v))
		}
	}
	// longest first: a value inside another is masked by the longer one
	sort.Slice(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	scmSecrets.Store(&out)
}

func scmLiveGet(key string) *scmLive {
	scmLiveMu.Lock()
	defer scmLiveMu.Unlock()
	return scmLives[key]
}

func scmLivePut(key string, l *scmLive) {
	scmLiveMu.Lock()
	if old := scmLives[key]; old != nil {
		scmRetired[old.token.Reveal()] = old.expires
	}
	scmLives[key] = l
	scmRebuildLocked()
	scmLiveMu.Unlock()
	scmKickRefresher() // its timer moves
}

// scmLiveDrop forgets key's token (it stays masked until it expires).
func scmLiveDrop(key string) {
	scmLiveMu.Lock()
	if old := scmLives[key]; old != nil {
		scmRetired[old.token.Reveal()] = old.expires
		delete(scmLives, key)
		scmRebuildLocked()
	}
	scmLiveMu.Unlock()
}

// --- pending sign-ins (memory only) ---------------------------------------------------

var (
	scmSigninMu sync.Mutex
	scmSignins  = map[string]*scmSignin{} // user \x00 provider
)

// scmNoteSignin keeps the sign-in the provider started for user (its 409
// signin): the workspace gate's park shows it to that person only.
func scmNoteSignin(user, provider string, s *scmSignin) {
	if s == nil || user == "" {
		return
	}
	cp := *s
	scmSigninMu.Lock()
	scmSignins[user+"\x00"+provider] = &cp
	scmSigninMu.Unlock()
}

func scmClearSignin(user, provider string) {
	scmSigninMu.Lock()
	delete(scmSignins, user+"\x00"+provider)
	scmSigninMu.Unlock()
}

func scmSigninOf(user, provider string) *scmSignin {
	scmSigninMu.Lock()
	defer scmSigninMu.Unlock()
	s := scmSignins[user+"\x00"+provider]
	if s == nil {
		return nil
	}
	if s.ExpiresAt > 0 && s.ExpiresAt < nowMs() {
		delete(scmSignins, user+"\x00"+provider)
		return nil
	}
	cp := *s
	return &cp
}

// --- the rows -------------------------------------------------------------------------

// scmCredRow is a project_creds row.
type scmCredRow struct {
	PID                                          int64
	Ref, Host, Identity, Login, ForUser, Purpose string
	Expires, Refresh, Written                    int64
	State, Why                                   string
}

const scmCredCols = `project_id, sandbox_ref, host, identity, login, for_user, purpose, expires_ms, refresh_ms, written_ms, state, why`

func scmScanCreds(rows *sql.Rows, err error) []scmCredRow {
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []scmCredRow
	for rows.Next() {
		var c scmCredRow
		if rows.Scan(&c.PID, &c.Ref, &c.Host, &c.Identity, &c.Login, &c.ForUser, &c.Purpose, &c.Expires, &c.Refresh, &c.Written, &c.State, &c.Why) == nil {
			out = append(out, c)
		}
	}
	return out
}

func (d *DB) scmPutCred(c scmCredRow) error {
	_, err := d.q.Exec(`INSERT INTO project_creds (`+scmCredCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(project_id, sandbox_ref, host) DO UPDATE SET identity=excluded.identity, login=excluded.login,
		for_user=excluded.for_user, purpose=excluded.purpose, expires_ms=excluded.expires_ms, refresh_ms=excluded.refresh_ms,
		written_ms=excluded.written_ms, state=excluded.state, why=excluded.why`,
		c.PID, c.Ref, c.Host, c.Identity, c.Login, c.ForUser, c.Purpose, c.Expires, c.Refresh, c.Written, c.State, c.Why)
	return err
}

// setCredState marks a row (blocked or scrubbed) with why, making one when
// there is none (the gate refused before anything was written).
func (d *DB) scmSetCredState(pid int64, ref, host, state, why string) error {
	_, err := d.q.Exec(`INSERT INTO project_creds (project_id, sandbox_ref, host, state, why) VALUES (?,?,?,?,?)
		ON CONFLICT(project_id, sandbox_ref, host) DO UPDATE SET state=excluded.state, why=excluded.why`, pid, ref, host, state, why)
	return err
}

// scmCredUnemptied: a scrub (why) couldn't empty the row's files — they
// may still hold the (revoked) value. The row stays live, so every scrub
// trigger selects it again (and a share stays refused), and due at once
// (refresh_ms 0), so the gate has it replaced or blocked before a turn.
func (d *DB) scmCredUnemptied(pid int64, ref, host, why string) error {
	_, err := d.q.Exec(`UPDATE project_creds SET state=?, why=?, refresh_ms=0 WHERE project_id=? AND sandbox_ref=? AND host=?`,
		credLive, why, pid, ref, host)
	return err
}

// credsOf is p's rows (ref "" = in every sandbox).
func (d *DB) scmCredsOf(pid int64, ref string) []scmCredRow {
	if ref == "" {
		return scmScanCreds(d.q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE project_id=? ORDER BY sandbox_ref, host`, pid))
	}
	return scmScanCreds(d.q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE project_id=? AND sandbox_ref=? ORDER BY host`, pid, ref))
}

// scmCredStatus is p's credentials as GET /projects/{pid}/status shows them:
// metadata, never a token.
func scmCredStatus(d *DB, pid int64) []StatusCred {
	out := []StatusCred{}
	for _, c := range d.scmCredsOf(pid, "") {
		out = append(out, StatusCred{Sandbox: c.Ref, Host: c.Host, Identity: c.Identity, Login: c.Login,
			State: c.State, ExpiresMs: c.Expires, Why: c.Why})
	}
	return out
}

// scmDue is the workspace gate's question (scmCredsDue), from the
// database alone and through t only (the gate holds the one connection in
// its transaction): does k's sandbox (p's, for no task) lack a live
// credential, or is it due within scmMinLeft? A project without repos needs
// none (ensure writes nothing for it, so asking would park its turns for
// good).
func scmDue(t *DB, p *Project, k *ProjectTask) bool {
	ref := scmCredRef(p, k)
	if t == nil || ref == "" || !scmActive(p) || !scmHasRepos(t, p.ID) {
		return false
	}
	rows := t.scmCredsOf(p.ID, ref)
	soon := nowMs() + scmMinLeft.Milliseconds()
	as := scmProjectAs(p)
	for _, c := range rows {
		if (p.Host == "" || c.Host == p.Host) && c.State == credLive && c.Identity == as && c.Refresh > soon && c.Expires > soon {
			return false
		}
	}
	return true
}

// scmHasRepos: project_repos (the projects store's, frozen DDL) lists a
// repo of pid a token would cover (scmRepoNames' rule), read through t. No
// table yet: none.
func scmHasRepos(t *DB, pid int64) bool {
	var n int
	err := t.q.QueryRow(`SELECT count(*) FROM project_repos WHERE project_id=? AND repo<>'' AND state<>'removing'`, pid).Scan(&n)
	return err == nil && n > 0
}

// --- paths, env and git config --------------------------------------------------------

var (
	scmUIDRe  = regexp.MustCompile(`^[a-z0-9]{1,32}$`)
	scmHostRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9.-]{0,252}(:[0-9]{1,5})?$`)
)

// scmCredDir is where p's credentials live in a sandbox whose home is home
// ("" when either can't be named safely).
func scmCredDir(home, uid string) string {
	if !scmHomeRe.MatchString(home) || !scmUIDRe.MatchString(uid) {
		return ""
	}
	return strings.TrimRight(home, "/") + "/.config/xbin-scm/" + uid
}

// scmEnvOf is what a project's execs get from its credentials
// (scmProjectEnv): GH_CONFIG_DIR, so `gh` uses the project's token and a
// person's own login stays untouched.
func scmEnvOf(p *Project, home string) map[string]string {
	if p == nil {
		return nil
	}
	dir := scmCredDir(home, p.UID)
	if dir == "" {
		return nil
	}
	return map[string]string{"GH_CONFIG_DIR": dir + "/gh"}
}

// scmGitConfigOf is the git config a base repo (or a clone-mode checkout)
// of p gets for host (scmGitConfig), in order. A key named again adds a
// value: the first occurrence replaces every value (git config
// --unset-all, then --add), later ones add — so the empty helper resets
// the helpers a global or system config names, and the project's own comes
// after it. user.name and user.email are the token's author, when this
// process holds one.
func scmGitConfigOf(p *Project, host, home string) [][2]string {
	if p == nil || !scmHostRe.MatchString(host) {
		return nil
	}
	dir := scmCredDir(home, p.UID)
	if dir == "" {
		return nil
	}
	key := "credential.https://" + host
	out := [][2]string{
		{key + ".helper", ""},
		{key + ".helper", "!f(){ test \"$1\" = get && cat '" + dir + "/" + host + ".cred'; }; f"},
		{key + ".useHttpPath", "false"},
	}
	if a := scmAuthorOf(p.ID, host); a.Name != "" && a.Email != "" && !strings.ContainsAny(a.Name+a.Email, "\n\r\x00") {
		out = append(out, [2]string{"user.name", a.Name}, [2]string{"user.email", a.Email})
	}
	return out
}

// scmAuthorOf is the author a live token of p for host says commits are by.
func scmAuthorOf(pid int64, host string) scmAuthor {
	scmLiveMu.Lock()
	defer scmLiveMu.Unlock()
	prefix := fmt.Sprint(pid) + "|"
	var best *scmLive
	for k, l := range scmLives {
		if strings.HasPrefix(k, prefix) && l.host == host && (best == nil || l.written > best.written) {
			best = l
		}
	}
	if best == nil {
		return scmAuthor{}
	}
	return best.author
}

// scmGitEnv is the git config of pairs as the env the config script reads
// (GIT_CFG_<i>_K / _V, _F=1 on a key's first occurrence).
func scmGitEnv(pairs [][2]string, env map[string]string) {
	seen := map[string]bool{}
	for i, kv := range pairs {
		env[fmt.Sprintf("GIT_CFG_%d_K", i)] = kv[0]
		env[fmt.Sprintf("GIT_CFG_%d_V", i)] = kv[1]
		if !seen[kv[0]] {
			seen[kv[0]] = true
			env[fmt.Sprintf("GIT_CFG_%d_F", i)] = "1"
		}
	}
}

// scmGitScript applies GIT_CFG_* to each existing repo GIT_DIR_<i> names —
// values come in env, never spliced into the script.
const scmGitScript = `
i=0
while :; do
  eval "b=\${GIT_DIR_$i-}"
  [ -n "$b" ] || break
  if [ -e "$b" ]; then
    j=0
    while :; do
      eval "k=\${GIT_CFG_${j}_K-}"
      [ -n "$k" ] || break
      eval "v=\${GIT_CFG_${j}_V-}"
      eval "f=\${GIT_CFG_${j}_F-}"
      if [ -n "$f" ]; then git -C "$b" config --unset-all "$k" || true; fi
      git -C "$b" config --add "$k" "$v"
      j=$((j+1))
    done
  fi
  i=$((i+1))
done
`

// scmRepoDirs is every git directory in ref that takes p's git config: the
// base repos and the clone-mode checkouts of p's tasks there.
func scmRepoDirs(p *Project, ref string) []string {
	var out []string
	for _, r := range projectReposOf(p.ID) {
		switch {
		case r.BasePath != "":
			out = append(out, r.BasePath)
		case p.Dir != "" && r.Slug != "":
			out = append(out, p.Dir+"/.repos/"+r.Slug+".git")
		}
	}
	if agent != nil && agent.db != nil {
		rows, err := agent.db.q.Query(`SELECT c.path FROM project_checkouts c JOIN project_tasks t ON c.task_id=t.id
			WHERE t.project_id=? AND c.mode='clone' AND c.state NOT IN ('removed','pending') AND c.path<>''
			AND (t.sandbox_ref=? OR (t.sandbox_ref='' AND ?=?))`, p.ID, ref, ref, p.SandboxRef)
		if err == nil { // no tasks table yet: no clones
			for rows.Next() {
				var s string
				if rows.Scan(&s) == nil {
					out = append(out, s)
				}
			}
			rows.Close()
		}
	}
	return out
}

// --- small helpers --------------------------------------------------------------------

// scmActive: p takes credentials (an archived or deleting project doesn't).
func scmActive(p *Project) bool { return p != nil && (p.State == "" || p.State == projActive) }

// credRefOf is the sandbox k works in (its fork), else p's.
func scmCredRef(p *Project, k *ProjectTask) string {
	if k != nil && k.SandboxRef != "" {
		return k.SandboxRef
	}
	if p == nil {
		return ""
	}
	return p.SandboxRef
}

// sbxUserOf is who the agent acts for at p's sandbox manager (Sbx-User).
func scmSbxUser(p *Project) string {
	if strings.HasPrefix(p.Owner, "el:") {
		return ""
	}
	return p.Owner
}

// scmPurpose is the token's purpose at the provider: one per project and
// sandbox (a fork gets its own).
func scmPurpose(p *Project, ref string) string { return "proj:" + p.UID + ":" + ref }

// scmRepoNames is p's repos as the provider names them.
func scmRepoNames(pid int64) []string {
	var out []string
	for _, r := range projectReposOf(pid) {
		if r.Repo != "" && r.State != "removing" {
			out = append(out, r.Repo)
		}
	}
	sort.Strings(out)
	return out
}

// scmGateError is the gate's refusal, in words for the task and its error.
type scmGateError struct{ Box, Why string }

func (e *scmGateError) Error() string {
	return fmt.Sprintf(scmWhyGateFailed, e.Box, scmWhyWords(e.Why))
}

// errSCMNoSandbox: the project has no sandbox yet.
var errSCMNoSandbox = errors.New("the project has no sandbox yet")
