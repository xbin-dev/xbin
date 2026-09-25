// migrate.go — the schema and every migration an existing instance needs.
//
// Everything here is additive and idempotent: running it twice changes
// nothing the second time, and an older binary can still run against the
// migrated file (it ignores the new tables and statuses). The one-time steps
// of the engine rewrite are guarded by settings k='engine'; the conversions an
// OLD process could still produce during a blue/green overlap (queued runs,
// run_deps edges) are also reachable from adoptLegacy, which the engine runs
// again once the old process's leases have lapsed.
package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

const engineVersion = "2"

func (d *DB) migrate() error {
	if _, err := d.q.Exec(schemaSQL); err != nil {
		return err
	}
	// Columns added after instances existed (ALTER fails harmlessly when the
	// column is already there).
	for _, q := range []string{
		`ALTER TABLE runs ADD COLUMN last_prompt_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN kind TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE schedules ADD COLUMN toolset TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN root_id INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN depth INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN detached INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN outcome TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN settled_at INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN cancel_req INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN lease_owner TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN lease_until INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN llm_calls INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN prompt_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN completion_tokens INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE repl_files ADD COLUMN mime TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE repl_files ADD COLUMN blob TEXT NOT NULL DEFAULT ''`,
		// The engine rewrite (engine=2).
		`ALTER TABLE runs ADD COLUMN turn_steps INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN turn_started INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE messages ADD COLUMN meta TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = d.q.Exec(q)
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_runs_status ON runs(status, wake_at)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_parent ON runs(parent_id)`,
		`CREATE INDEX IF NOT EXISTS idx_runs_root ON runs(root_id)`,
	} {
		if _, err := d.q.Exec(q); err != nil {
			return err
		}
	}
	d.backfillGraph()
	if n := d.sweepOrphanRuns(); n > 0 {
		logf("removed %d orphaned subagent run(s) whose parent had been deleted", n)
	}
	// Backfill the FTS index from any messages that predate it (one-time).
	var ftsN int
	_ = d.q.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsN)
	if ftsN == 0 {
		_, _ = d.q.Exec(`INSERT INTO messages_fts(content, run_id, msg_id)
			SELECT content, run_id, id FROM messages WHERE content != ''`)
	}
	first := d.getSetting("engine") != engineVersion
	if first {
		d.renumberDuplicateSeqs() // M1
	}
	d.adoptLegacy(first) // M2–M4
	if first {
		if err := d.putSetting("engine", engineVersion); err != nil {
			return err
		}
	}
	return nil
}

const schemaSQL = `
CREATE TABLE IF NOT EXISTS runs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  title TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'idle',
  wake_at INTEGER NOT NULL DEFAULT 0,
  parent_id INTEGER NOT NULL DEFAULT 0,
  config TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  pending TEXT NOT NULL DEFAULT '',
  last_prompt_tokens INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  role TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  tool_call_id TEXT NOT NULL DEFAULT '',
  tool_calls TEXT NOT NULL DEFAULT '',
  tokens INTEGER NOT NULL DEFAULT 0,
  compacted INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_messages_run ON messages(run_id, seq);
CREATE VIRTUAL TABLE IF NOT EXISTS messages_fts USING fts5(
  content, run_id UNINDEXED, msg_id UNINDEXED, tokenize='porter'
);
CREATE TABLE IF NOT EXISTS steps (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  detail TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_steps_run ON steps(run_id, seq);
CREATE TABLE IF NOT EXISTS memory (
  run_id INTEGER NOT NULL,
  key TEXT NOT NULL,
  value TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (run_id, key)
);
CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS skills (
  name TEXT PRIMARY KEY,
  description TEXT NOT NULL DEFAULT '',
  content TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS schedules (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL DEFAULT '',
  cron TEXT NOT NULL DEFAULT '',
  goal TEXT NOT NULL DEFAULT '',
  system TEXT NOT NULL DEFAULT '',
  watcher INTEGER NOT NULL DEFAULT 0,
  enabled INTEGER NOT NULL DEFAULT 1,
  run_id INTEGER NOT NULL DEFAULT 0,
  last_run INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL
);
-- Session files: a per-run store the model writes with file_write/file_edit
-- and the render pane shows. sqlite rows, never host files (normReplPath).
CREATE TABLE IF NOT EXISTS repl_files (
  run_id INTEGER NOT NULL,
  path TEXT NOT NULL,
  content TEXT NOT NULL DEFAULT '',
  bytes INTEGER NOT NULL DEFAULT 0,
  version INTEGER NOT NULL DEFAULT 1,
  created INTEGER NOT NULL,
  updated INTEGER NOT NULL,
  PRIMARY KEY (run_id, path)
);
-- The REPL's replay log (repl.go build()): a statement row is 'running'
-- before execution and updated after, so one that killed the process is
-- marked 'killed' and never replayed.
CREATE TABLE IF NOT EXISTS repl_log (
  run_id INTEGER NOT NULL,
  seq INTEGER NOT NULL,
  kind TEXT NOT NULL DEFAULT 'eval',
  code TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'running',
  ms INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL,
  PRIMARY KEY (run_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_repl_log_run ON repl_log(run_id, seq);
-- Pre-engine dependency edges. Read-only now (adoptLegacy converts them); an
-- older binary sharing the file during a swap may still write them.
CREATE TABLE IF NOT EXISTS run_deps (
  run_id       INTEGER NOT NULL,
  dep_id       INTEGER NOT NULL,
  kind         TEXT NOT NULL DEFAULT 'await',
  state        TEXT NOT NULL DEFAULT 'pending',
  delivered    INTEGER NOT NULL DEFAULT 0,
  tool_call_id TEXT NOT NULL DEFAULT '',
  on_error     TEXT NOT NULL DEFAULT 'report',
  created      INTEGER NOT NULL,
  settled      INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (run_id, dep_id)
);
CREATE INDEX IF NOT EXISTS idx_deps_dep   ON run_deps(dep_id);
CREATE INDEX IF NOT EXISTS idx_deps_undel ON run_deps(run_id, delivered, state);
-- Per-tree lifetime spawn budget: a monotonic counter a single statement can
-- compare-and-swap.
CREATE TABLE IF NOT EXISTS run_trees (
  root_id   INTEGER PRIMARY KEY,
  spawned   INTEGER NOT NULL DEFAULT 0,
  max_spawn INTEGER NOT NULL DEFAULT 0,
  max_depth INTEGER NOT NULL DEFAULT 0,
  created   INTEGER NOT NULL
);
-- Which session files a user message carried (images are added at context
-- assembly, never stored in content).
CREATE TABLE IF NOT EXISTS message_files (
  msg_id INTEGER NOT NULL,
  run_id INTEGER NOT NULL,
  path   TEXT NOT NULL,
  PRIMARY KEY (msg_id, path)
);
CREATE INDEX IF NOT EXISTS idx_message_files_run ON message_files(run_id);
-- The inbox: every input to a run (a human message, an approval, an
-- interrupt, a cancel, a compaction or watcher request) is a row, consumed
-- exactly once by the engine at a step boundary (inbox.go).
CREATE TABLE IF NOT EXISTS inbox (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id INTEGER NOT NULL,
  kind TEXT NOT NULL,
  body TEXT NOT NULL DEFAULT '{}',
  client_id TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  delivered_at INTEGER NOT NULL DEFAULT 0,
  msg_id INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_inbox_run ON inbox(run_id, delivered_at, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_inbox_client ON inbox(run_id, client_id) WHERE client_id<>'';
-- A subagent link: one delegation from a parent to a child run. The child
-- writes state/outcome/result/settled (once, WHERE state='running'); the
-- parent writes mode/delivered/demoted_at. Never both (links.go).
CREATE TABLE IF NOT EXISTS links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  parent_id INTEGER NOT NULL,
  child_id INTEGER NOT NULL,
  tool_call_id TEXT NOT NULL DEFAULT '',
  mode TEXT NOT NULL DEFAULT 'fg',
  state TEXT NOT NULL DEFAULT 'running',
  outcome TEXT NOT NULL DEFAULT '',
  result TEXT NOT NULL DEFAULT '',
  deadline INTEGER NOT NULL DEFAULT 0,
  delivered INTEGER NOT NULL DEFAULT 0,
  demoted_at INTEGER NOT NULL DEFAULT 0,
  label TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL,
  settled INTEGER NOT NULL DEFAULT 0,
  legacy INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_links_parent ON links(parent_id, delivered, state);
CREATE INDEX IF NOT EXISTS idx_links_child ON links(child_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_links_open ON links(child_id) WHERE state='running';
-- after:[ids] — a child that starts only once these links have settled.
CREATE TABLE IF NOT EXISTS link_deps (
  child_id INTEGER NOT NULL,
  dep_link INTEGER NOT NULL,
  dep_run INTEGER NOT NULL,
  PRIMARY KEY (child_id, dep_link)
);
CREATE INDEX IF NOT EXISTS idx_link_deps_dep ON link_deps(dep_link);
`

// backfillGraph places rows created before root_id/depth existed. Idempotent
// by its own root_id=0 guard.
func (d *DB) backfillGraph() {
	_, _ = d.q.Exec(`UPDATE runs SET root_id=id WHERE root_id=0 AND parent_id=0`)
	for i := 0; i < 8; i++ {
		res, err := d.q.Exec(`UPDATE runs SET
			depth = 1 + COALESCE((SELECT p.depth FROM runs p WHERE p.id=runs.parent_id), 0),
			root_id = COALESCE((SELECT p.root_id FROM runs p WHERE p.id=runs.parent_id), id)
			WHERE root_id=0 AND parent_id<>0
			  AND EXISTS (SELECT 1 FROM runs p WHERE p.id=runs.parent_id AND p.root_id<>0)`)
		if err != nil || rowsAffected(res) == 0 {
			break
		}
	}
	_, _ = d.q.Exec(`UPDATE runs SET root_id=id WHERE root_id=0`)
}

// renumberDuplicateSeqs (M1): the old seq allocation (SELECT MAX, then INSERT)
// raced, and a duplicate seq makes transcript order ambiguous.
func (d *DB) renumberDuplicateSeqs() {
	for _, table := range []string{"messages", "steps"} {
		rows, err := d.q.Query(`SELECT DISTINCT run_id FROM ` + table + ` GROUP BY run_id, seq HAVING count(*) > 1`)
		if err != nil {
			continue
		}
		var runs []int64
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				runs = append(runs, id)
			}
		}
		rows.Close()
		for _, r := range runs {
			_ = d.Tx(func(t *DB) error {
				_, err := t.q.Exec(`UPDATE `+table+` SET seq = (
					SELECT n FROM (SELECT id, ROW_NUMBER() OVER (ORDER BY seq, id) - 1 AS n FROM `+table+` WHERE run_id=?1) x
					 WHERE x.id = `+table+`.id) WHERE run_id=?1`, r)
				return err
			})
		}
		if len(runs) > 0 {
			logf("renumbered duplicate %s seqs in %d run(s)", table, len(runs))
		}
	}
}

// legacyPending is the pre-engine `pending` shape.
type legacyPending struct {
	Kind      string     `json:"kind"`
	ToolCalls []toolCall `json:"toolCalls"`
}

// adoptLegacy converts pre-engine workflow state (M2–M4). first gates the
// steps that are only right once: after the upgrade, a resting run with a
// trailing user message is a legitimate state (an interrupted turn), not a
// dropped dispatch.
func (d *DB) adoptLegacy(first bool) {
	d.adoptLegacyLinks()
	d.adoptLegacyDeps()
	d.adoptLegacyStatuses(first)
}

// adoptLegacyLinks (M2): parent→child run_deps edges become links.
func (d *DB) adoptLegacyLinks() {
	type edge struct {
		parent, child              int64
		state, tc                  string
		delivered                  bool
		created, settled           int64
		pStatus, pPending          string
		cStatus, cOutcome, cResult string
		cSettled                   int64
		cTitle                     string
	}
	rows, err := d.q.Query(`
		SELECT d.run_id, d.dep_id, d.state, d.tool_call_id, d.delivered, d.created, d.settled,
		       p.status, p.pending, c.status, c.outcome, c.result, c.settled_at, c.title
		  FROM run_deps d JOIN runs c ON c.id=d.dep_id JOIN runs p ON p.id=d.run_id
		 WHERE c.parent_id = d.run_id AND d.kind='await'
		   AND NOT EXISTS (SELECT 1 FROM links l WHERE l.parent_id=d.run_id AND l.child_id=d.dep_id AND l.legacy=1)`)
	if err != nil {
		return
	}
	var edges []edge
	for rows.Next() {
		var e edge
		var del int
		if rows.Scan(&e.parent, &e.child, &e.state, &e.tc, &del, &e.created, &e.settled,
			&e.pStatus, &e.pPending, &e.cStatus, &e.cOutcome, &e.cResult, &e.cSettled, &e.cTitle) == nil {
			e.delivered = del != 0
			edges = append(edges, e)
		}
	}
	rows.Close()
	for _, e := range edges {
		mode := "bg"
		if e.tc != "" && e.pStatus == statusBlocked {
			var lp legacyPending
			if json.Unmarshal([]byte(e.pPending), &lp) == nil && lp.Kind == "await" {
				for _, c := range lp.ToolCalls {
					if c.ID == e.tc {
						mode = "fg"
					}
				}
			}
		}
		state, outcome, result := linkRunning, "", ""
		switch {
		case e.state != "pending":
			state, outcome = legacyEdgeState(e.state, e.cOutcome)
			result = e.cResult
		case e.cSettled != 0:
			state, outcome = legacyEdgeState("", e.cOutcome)
			result = e.cResult
		case resting(e.cStatus):
			outcome = outcomeAnswered
			state = linkDone
			switch e.cStatus {
			case statusCanceled:
				state, outcome = linkCanceled, outcomeCanceled
			case statusError:
				state, outcome = linkError, outcomeError
			}
			result = firstNonEmpty(e.cResult, d.lastAssistant(e.child))
		}
		settled := e.settled
		if state != linkRunning && settled == 0 {
			settled = now()
		}
		deadline := int64(0)
		if mode == "fg" && state == linkRunning {
			deadline = now() + 900
		}
		tc := e.tc
		if mode == "bg" {
			tc = "" // a bg link never rewrites a placeholder
		}
		// A child can have one running link; a second legacy edge for the same
		// child (impossible in practice) is simply skipped by the index.
		_, _ = d.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, state, outcome, result, deadline,
			delivered, label, created, settled, legacy) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,1)`,
			e.parent, e.child, tc, mode, state, outcome, result, deadline, b2i(e.delivered), e.cTitle, e.created, settled)
	}
}

func legacyEdgeState(edgeState, outcome string) (state, out string) {
	switch edgeState {
	case "failed", "timeout":
		return linkError, firstNonEmpty(outcome, outcomeError)
	case "canceled":
		return linkCanceled, outcomeCanceled
	case "satisfied":
		return linkDone, firstNonEmpty(outcome, outcomeDone)
	}
	switch outcome {
	case outcomeError:
		return linkError, outcomeError
	case outcomeCanceled:
		return linkCanceled, outcomeCanceled
	case "":
		return linkDone, outcomeDone
	}
	return linkDone, outcome
}

// adoptLegacyDeps (M3): sibling `after` edges become link_deps rows.
func (d *DB) adoptLegacyDeps() {
	rows, err := d.q.Query(`
		SELECT d.run_id, d.dep_id FROM run_deps d JOIN runs w ON w.id=d.run_id JOIN runs dep ON dep.id=d.dep_id
		 WHERE d.kind='await' AND d.delivered=0 AND dep.parent_id <> d.run_id
		   AND NOT EXISTS (SELECT 1 FROM link_deps ld WHERE ld.child_id=d.run_id AND ld.dep_run=d.dep_id)`)
	if err != nil {
		return
	}
	type pair struct{ waiter, dep int64 }
	var ps []pair
	for rows.Next() {
		var p pair
		if rows.Scan(&p.waiter, &p.dep) == nil {
			ps = append(ps, p)
		}
	}
	rows.Close()
	for _, p := range ps {
		var linkID int64
		if d.q.QueryRow(`SELECT id FROM links WHERE child_id=? ORDER BY id DESC LIMIT 1`, p.dep).Scan(&linkID) != nil {
			continue
		}
		_, _ = d.q.Exec(`INSERT OR IGNORE INTO link_deps (child_id, dep_link, dep_run) VALUES (?, ?, ?)`, p.waiter, linkID, p.dep)
	}
}

// adoptLegacyStatuses (M4) moves runs out of the statuses only the old
// dispatcher understood. The same normalisation runs per run when the engine
// first touches it (normalizeLegacy), so a row an old process writes after
// this pass is still handled.
func (d *DB) adoptLegacyStatuses(first bool) {
	runs, err := d.queryRuns(`WHERE status IN ('queued','blocked')
		OR (cancel_req<>0 AND settled_at=0 AND status NOT IN ('idle','done','error','canceled'))
		OR (parent_id<>0 AND detached=0 AND status NOT IN ('idle','done','error','canceled'))`)
	if err == nil {
		for _, r := range runs {
			_ = d.Tx(func(t *DB) error { return t.normalizeLegacy(r) })
		}
	}
	if !first {
		return
	}
	// An idle run whose last message is from the user was asked something that
	// was never answered — the signature of a dispatch the old code dropped.
	rows, err := d.q.Query(`
		SELECT r.id FROM runs r WHERE r.status='idle'
		   AND (SELECT m.role FROM messages m WHERE m.run_id=r.id ORDER BY m.seq DESC, m.id DESC LIMIT 1) = 'user'`)
	if err != nil {
		return
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		_ = d.setStatusOnly(id, statusRunning)
	}
}

// normalizeLegacy rewrites one run left in a pre-engine state. Called inside
// a transaction (at boot, and by the engine before it works on a run).
func (d *DB) normalizeLegacy(r *Run) error {
	switch {
	case r.CancelReq != 0 && r.SettledAt == 0 && !resting(r.Status):
		r.Status = statusCanceled
		return d.setStatus(r.ID, statusCanceled, 0, "cancelled", "")
	case r.ParentID != 0 && !r.Detached && !resting(r.Status):
		// A non-detached child only ever existed inside its parent's tool call;
		// resurrecting it would run work nobody is waiting for.
		r.Status = statusCanceled
		return d.setStatus(r.ID, statusCanceled, 0, "cancelled: pre-upgrade subagent", "")
	case r.Status == statusQueued:
		r.Status = statusRunning
		return d.setStatusOnly(r.ID, statusRunning)
	case r.Status == statusBlocked:
		var lp legacyPending
		if r.Pending != "" && json.Unmarshal([]byte(r.Pending), &lp) == nil && lp.Kind == "await" {
			// Parked on its children: the fg links (adopted from its edges)
			// describe what it waits for.
			var open int
			_ = d.q.QueryRow(`SELECT count(*) FROM links WHERE parent_id=? AND mode='fg' AND delivered=0`, r.ID).Scan(&open)
			st := statusAwait
			if open == 0 {
				st = statusRunning
			}
			r.Status, r.Pending = st, `{"kind":"await"}`
			return d.setStatus(r.ID, st, r.WakeAt, r.Result, r.Pending)
		}
		var deps int
		_ = d.q.QueryRow(`SELECT count(*) FROM link_deps WHERE child_id=?`, r.ID).Scan(&deps)
		if deps > 0 {
			r.Status, r.Pending = statusAwait, `{"kind":"deps"}`
			return d.setStatus(r.ID, statusAwait, 0, r.Result, r.Pending)
		}
		r.Status = statusRunning
		return d.setStatus(r.ID, statusRunning, 0, r.Result, "")
	}
	return nil
}

// legacyLeaseLive reports runs an OLD process still drives (its leases are
// renewed every iteration and last 30 s). The new engine leaves them alone
// and looks again when the latest lapses.
func (d *DB) legacyLeaseLive(engineGen string) (ids []int64, until int64) {
	rows, err := d.q.Query(`SELECT id, lease_until FROM runs
		WHERE lease_owner<>'' AND lease_owner<>? AND lease_owner NOT LIKE 'engine:%' AND lease_until > ?`, engineGen, now())
	if err != nil {
		return nil, 0
	}
	defer rows.Close()
	for rows.Next() {
		var id, u int64
		if rows.Scan(&id, &u) == nil {
			ids = append(ids, id)
			if u > until {
				until = u
			}
		}
	}
	return ids, until
}

// scanIDs collects one int64 column.
func scanIDs(rows *sql.Rows, err error) []int64 {
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

// jsonField extracts one string field from a JSON object (best-effort).
func jsonField(raw, key string) string {
	var m map[string]any
	if json.Unmarshal([]byte(raw), &m) != nil {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
}
