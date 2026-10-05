// project_team.go — team projects (API.md §Team projects): the tables
// (project_board at the global instance, project_board_out in a member's
// partition), a definition's security part and its hash, the board's push
// from a membership (taskChangedHooks → project_board_out → a PUT to the
// global instance through callGlobal, retried 10 s doubling to 10 min) and
// the owner loop that sends it, re-reads the definitions of memberships
// with open tasks every 10 min and, at the global instance, drops the rows
// of definitions that are gone.
//
// A team project is a definition at a partitioned agent's global instance
// (kind team: name, repos, policy, members, an optional seed sandbox — no
// tasks, no credential) and a membership in each member's own partition
// (kind membership, team_ref the definition's id) whose tasks, coordinator
// and credentials are a personal project's. The board at global holds one
// row per (member, task) — never a transcript; `run` is offered only to the
// member whose run it is. The routes are project_team_routes.go (global) and
// project_team_member.go (a person's partition).
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addTeamSchema)
	taskChangedHooks = append(taskChangedHooks, teamTaskChanged)
	ownerLoops = append(ownerLoops, teamLoop)
}

// boardSchema is T's tables (API.md §Team projects): project_board at the
// global instance, project_board_out in a person's partition. Both are made
// in every home (an empty table costs nothing, and a home's mode can't be
// told apart in a migration test); only the home that uses one writes it.
// A row's member_ref is the uid of the membership that sent it: a task
// number is a membership's own, so a member's new membership of the same
// definition (the old one deleted) numbers its tasks from 1 again.
const boardSchema = `
CREATE TABLE IF NOT EXISTS project_board (
  project_id INTEGER NOT NULL,
  member     TEXT    NOT NULL,
  member_pid TEXT    NOT NULL DEFAULT '',
  member_ref TEXT    NOT NULL DEFAULT '',
  n          INTEGER NOT NULL,
  title      TEXT    NOT NULL DEFAULT '',
  col        TEXT    NOT NULL DEFAULT '',
  state      TEXT    NOT NULL DEFAULT '',
  waiting    TEXT    NOT NULL DEFAULT '',
  branch     TEXT    NOT NULL DEFAULT '',
  prs        TEXT    NOT NULL DEFAULT '[]',
  ci         TEXT    NOT NULL DEFAULT '',
  run        INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  stale      INTEGER NOT NULL DEFAULT 0,
  hidden     INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, member, n)
);
CREATE INDEX IF NOT EXISTS idx_pboard_member ON project_board(member, member_pid);
CREATE TABLE IF NOT EXISTS project_board_out (
  project_id INTEGER NOT NULL,
  n          INTEGER NOT NULL,
  body       TEXT    NOT NULL DEFAULT '{}',
  tries      INTEGER NOT NULL DEFAULT 0,
  next_ms    INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (project_id, n)
);
`

// addTeamSchema makes T's tables (schemaAdds: after migrate(), never on
// team). Additive and idempotent.
func (d *DB) addTeamSchema() error {
	if _, err := d.q.Exec(boardSchema); err != nil {
		return fmt.Errorf("team projects schema: %w", err)
	}
	// a board made before member_ref (an error: the column is there)
	_, _ = d.q.Exec(`ALTER TABLE project_board ADD COLUMN member_ref TEXT NOT NULL DEFAULT ''`)
	return nil
}

// --- the definition's security part -----------------------------------------------------

// teamSecurityKeys are the policy keys a membership takes from its
// definition only once its member accepted them (API.md §Team projects):
// what runs beside the member's token or decides as whom. The other keys
// follow the definition at once.
var teamSecurityKeys = []string{"instructions", "checks", "prConventions", "taskClass", "engine", "harness", "as",
	"membersAsBot", "reviews", "autoPR", "autoLabel", "ci", "coordinator", "workflows", "protection", "branchPrefix"}

// teamSecurity is the canonical JSON of the security part of a policy and
// its repos — {policy: {the keys above, defaults filled}, repos: [{repo,
// setup}] by repo} with every object's keys sorted — the bytes whose
// SHA-256 is def_hash and that def_pending keeps.
func teamSecurity(policy json.RawMessage, repos []ProjectRepo) []byte {
	var all map[string]json.RawMessage
	_ = json.Unmarshal(policyView(policy), &all)
	pol := map[string]any{}
	for _, k := range teamSecurityKeys {
		if v, ok := all[k]; ok {
			pol[k] = canonValue(v)
		}
	}
	rs := []map[string]any{}
	for _, r := range repos {
		rs = append(rs, map[string]any{"repo": strings.ToLower(r.Repo), "setup": r.Setup})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i]["repo"].(string) < rs[j]["repo"].(string) })
	b, _ := json.Marshal(map[string]any{"policy": pol, "repos": rs})
	return b
}

// canonValue is a JSON value as a plain Go value (numbers kept as written),
// so marshalling it again sorts every object's keys.
func canonValue(raw json.RawMessage) any {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil
	}
	return v
}

// teamHash is the hash a member accepts: SHA-256 of the canonical bytes.
func teamHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// teamPolicyAccepted is the policy a membership runs: the definition's,
// with every security key as the membership has it (absent stays absent).
func teamPolicyAccepted(own, def json.RawMessage) json.RawMessage {
	var mine, theirs map[string]json.RawMessage
	_ = json.Unmarshal(policyView(own), &mine)
	if json.Unmarshal(orRaw(def), &theirs) != nil || theirs == nil {
		theirs = map[string]json.RawMessage{}
	}
	for _, k := range teamSecurityKeys {
		if v, ok := mine[k]; ok {
			theirs[k] = v
		} else {
			delete(theirs, k)
		}
	}
	b, _ := json.Marshal(theirs)
	return b
}

// teamPolicyAdopt is own with every security key taken from sec (the
// pending security part's policy object): what accepting adopts.
func teamPolicyAdopt(own json.RawMessage, sec map[string]json.RawMessage) json.RawMessage {
	var mine map[string]json.RawMessage
	if json.Unmarshal(orRaw(own), &mine) != nil || mine == nil {
		mine = map[string]json.RawMessage{}
	}
	for _, k := range teamSecurityKeys {
		if v, ok := sec[k]; ok {
			mine[k] = v
		} else {
			delete(mine, k)
		}
	}
	b, _ := json.Marshal(mine)
	return b
}

// --- the board's push (a person's partition) -----------------------------------------------

// teamOutRow is a project_board_out body: the row as it is PUT, with the
// definition's id beside it, so a row is still sent (a deleted task's)
// after its membership's row is gone, and the membership's uid, which the
// global instance keeps with the row (a deleted task hides only the row of
// the membership it was in). The global instance ignores team.
type teamOutRow struct {
	Team       int64  `json:"team"`
	Membership string `json:"membership"`
	BoardRow
}

// teamTaskChanged (taskChangedHooks): a task of a membership changed —
// its board row is written to project_board_out (the latest wins; a row
// waiting to be tried again keeps its wait) and the sender kicked after
// the commit. A deleted task is sent as state deleted; an archived
// membership sends nothing else.
func teamTaskChanged(t *DB, p *Project, k *ProjectTask, what string) {
	if !userMode() || p == nil || k == nil || p.Kind != projMembership || p.TeamRef == 0 {
		return
	}
	cur, err := t.taskByN(p.ID, k.N)
	if err != nil {
		cur = k
	}
	v := t.projTaskView(p, cur)
	row := BoardRow{N: cur.N, Title: v.Title, Column: v.Column, State: v.State, Waiting: v.WaitingFor, Branch: v.Branch,
		PRs: v.PRs, CI: v.CI, Run: cur.RunID, UpdatedMs: nowMs()}
	if what == "deleted" || cur.Phase == phaseDeleted || cur.RunID == 0 {
		row = BoardRow{N: cur.N, State: taskDeleted, PRs: []TaskPR{}, UpdatedMs: row.UpdatedMs}
	} else if p.State != projActive {
		return // an archived membership tells the board nothing more (only that a task went)
	}
	if row.PRs == nil {
		row.PRs = []TaskPR{}
	}
	body, _ := json.Marshal(teamOutRow{Team: p.TeamRef, Membership: p.UID, BoardRow: row})
	if _, err := t.q.Exec(`INSERT INTO project_board_out (project_id, n, body, tries, next_ms) VALUES (?, ?, ?, 0, 0)
		ON CONFLICT(project_id, n) DO UPDATE SET body=excluded.body`, p.ID, cur.N, string(body)); err != nil {
		logf("project %d: the board row of task %d: %v", p.ID, cur.N, err)
		return
	}
	t.AfterCommit(teamKick)
	if what == "created" { // a task about to start: the definition is read again (its removed repos, its name)
		pid := p.ID
		t.AfterCommit(func() {
			if ag := projAg(); ag != nil {
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := ag.teamSync(ctx, pid, false); err != nil && err != errTeamGone {
						logf("project %d: re-reading its team project: %v", pid, err)
					}
				}()
			}
		})
	}
}

// teamKickCh wakes the team loop (a row to send, a sync to make).
var teamKickCh = make(chan struct{}, 1)

func teamKick() {
	select {
	case teamKickCh <- struct{}{}:
	default:
	}
}

// teamBackoff is the first retry's wait (doubling to teamBackoffMax).
var (
	teamBackoff    = 10 * time.Second
	teamBackoffMax = 10 * time.Minute
)

// teamSyncEvery is how often a membership with open tasks re-reads its
// definition; teamSweepEvery how often the global instance drops the board
// rows of definitions that are gone.
var (
	teamSyncEvery  = 10 * time.Minute
	teamSweepEvery = 10 * time.Minute
)

// teamLoop (ownerLoops): in a person's partition, sends the board rows due
// and re-reads the definitions of memberships with open tasks; at the
// global instance, drops the rows of definitions that are gone. It never
// holds the engine (ownerLoops don't).
func teamLoop(ctx context.Context, e *Engine) {
	// the mode the engine started the loops under, never the process
	// global again: a test switches it under a running engine
	if e == nil || e.ag == nil || e.loopMode == modeLegacy {
		return
	}
	var lastSync, lastSweep time.Time
	for {
		wait := time.Minute
		switch e.loopMode {
		case modeUser:
			wait = min(wait, teamSendDue(ctx, e.ag))
			if time.Since(lastSync) >= teamSyncEvery {
				lastSync = time.Now()
				teamSyncOpen(ctx, e.ag)
			}
		case modeGlobal:
			if time.Since(lastSweep) >= teamSweepEvery {
				lastSweep = time.Now()
				teamSweep(e.ag.db)
			}
		}
		t := time.NewTimer(max(wait, 50*time.Millisecond))
		select {
		case <-ctx.Done():
		case <-t.C:
		case <-teamKickCh:
		}
		t.Stop()
		if ctx.Err() != nil {
			return
		}
	}
}

// teamOut is a row of project_board_out.
type teamOut struct {
	pid, n int64
	body   string
	tries  int
}

// teamSendDue sends the board rows due now and answers how long until the
// next one is (at most a minute).
func teamSendDue(ctx context.Context, ag *Agent) time.Duration {
	now := nowMs()
	rows, err := ag.db.q.Query(`SELECT project_id, n, body, tries FROM project_board_out WHERE next_ms<=? ORDER BY next_ms, project_id, n LIMIT 50`, now)
	if err != nil {
		return time.Minute
	}
	var due []teamOut
	for rows.Next() {
		var o teamOut
		if rows.Scan(&o.pid, &o.n, &o.body, &o.tries) == nil {
			due = append(due, o)
		}
	}
	rows.Close()
	left := map[int64]bool{} // memberships that left in this pass: their other rows are gone
	for _, o := range due {
		if ctx.Err() != nil {
			return time.Minute
		}
		if !left[o.pid] && teamSend(ctx, ag, o) {
			left[o.pid] = true
		}
	}
	var next int64
	_ = ag.db.q.QueryRow(`SELECT COALESCE(MIN(next_ms), 0) FROM project_board_out`).Scan(&next)
	if next == 0 {
		return time.Minute
	}
	return min(time.Duration(max(next-nowMs(), 0))*time.Millisecond, time.Minute)
}

// teamSend PUTs one board row to the global instance: done, it goes (unless
// a newer one was written meanwhile); 404 or 403 — no longer a member, or
// the definition is gone — the membership is archived (API.md §Team projects);
// 400, it can never be taken, it goes; anything else is tried again after
// teamBackoff doubling to teamBackoffMax. Answers whether the membership left.
func teamSend(ctx context.Context, ag *Agent, o teamOut) (left bool) {
	var row teamOutRow
	if json.Unmarshal([]byte(o.body), &row) != nil || row.Team <= 0 || row.Team >= partitionIDBase {
		_, _ = ag.db.q.Exec(`DELETE FROM project_board_out WHERE project_id=? AND n=? AND body=?`, o.pid, o.n, o.body)
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	res, err := callGlobal(cctx, http.MethodPut, fmt.Sprintf("/projects/%d/board/%d", row.Team, o.n), []byte(o.body), "application/json")
	cancel()
	switch {
	case err == nil && res.Status/100 == 2:
		_, _ = ag.db.q.Exec(`DELETE FROM project_board_out WHERE project_id=? AND n=? AND body=?`, o.pid, o.n, o.body)
	case err == nil && (res.Status == http.StatusNotFound || res.Status == http.StatusForbidden):
		ag.teamLeave(o.pid, res.Status)
		return true
	case err == nil && res.Status == http.StatusBadRequest:
		logf("project %d: the team board refused task %d's row: %s", o.pid, o.n, clip(string(res.Body), 300))
		_, _ = ag.db.q.Exec(`DELETE FROM project_board_out WHERE project_id=? AND n=? AND body=?`, o.pid, o.n, o.body)
	default:
		wait := teamBackoff << min(o.tries, 16)
		if wait > teamBackoffMax || wait <= 0 {
			wait = teamBackoffMax
		}
		// the wait holds for a row written meanwhile too: the global instance is still down
		_, _ = ag.db.q.Exec(`UPDATE project_board_out SET tries=tries+1, next_ms=? WHERE project_id=? AND n=?`,
			nowMs()+wait.Milliseconds(), o.pid, o.n)
	}
	return false
}

// teamSyncOpen re-reads the definition of every active membership with an
// open task (every 10 min while it has open tasks).
func teamSyncOpen(ctx context.Context, ag *Agent) {
	ids := scanIDs(ag.db.q.Query(`SELECT p.id FROM projects p WHERE p.kind=? AND p.state=? AND EXISTS
		(SELECT 1 FROM project_tasks k WHERE k.project_id=p.id AND k.phase IN ('open','pr') AND k.run_id<>0)`,
		projMembership, projActive))
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		if _, err := ag.teamSync(ctx, id, true); err != nil && err != errTeamGone {
			logf("project %d: re-reading its team project: %v", id, err)
		}
	}
}

// teamSweep (the global instance) drops the board rows of definitions that
// are gone or being deleted.
func teamSweep(d *DB) {
	if _, err := d.q.Exec(`DELETE FROM project_board WHERE project_id NOT IN
		(SELECT id FROM projects WHERE kind=? AND state<>?)`, projTeam, projDeleting); err != nil {
		logf("team board sweep: %v", err)
	}
}

// teamSynced is when each membership last re-read its definition (memory
// only): a page opening twice in a row reads it once.
var teamSynced = struct {
	sync.Mutex
	m map[int64]time.Time
}{m: map[int64]time.Time{}}

// teamSyncFresh says whether membership pid read its definition within d.
func teamSyncFresh(pid int64, d time.Duration) bool {
	teamSynced.Lock()
	defer teamSynced.Unlock()
	at, ok := teamSynced.m[pid]
	return ok && time.Since(at) < d
}

func teamSyncedNow(pid int64) {
	teamSynced.Lock()
	teamSynced.m[pid] = time.Now()
	teamSynced.Unlock()
}

// teamListWait is how long GET /memberships waits for its re-reads, all of
// them together.
var teamListWait = 5 * time.Second

// teamLocks serialize each membership's re-reads (teamLock).
var teamLocks = struct {
	sync.Mutex
	m map[int64]chan struct{}
}{m: map[int64]chan struct{}{}}

// teamLock takes membership pid's re-read lock (waiting at most until ctx
// ends) and answers its release.
func teamLock(ctx context.Context, pid int64) (func(), error) {
	teamLocks.Lock()
	ch := teamLocks.m[pid]
	if ch == nil {
		ch = make(chan struct{}, 1)
		teamLocks.m[pid] = ch
	}
	teamLocks.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
