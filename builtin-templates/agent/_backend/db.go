// db.go — the agent's durable state, in an in-component sqlite file
// (XBIN_RES_DB). Everything the loop needs to resume after a crash or backend
// unload lives here: runs, the transcript, the visibility journal, and
// self-edited memory blocks. See plans/agent.md.
package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, no cgo
)

// Run status values.
const (
	statusIdle    = "idle"          // awaiting the next injected user message
	statusRunning = "running"       // being driven right now
	statusWaiting = "waiting_input" // parked on ask_user / tool approval
	statusSleep   = "sleeping"      // yielded until wake_at; heartbeat resumes
	statusDone    = "done"          // finished (finish tool)
	statusError   = "error"         // gave up after an error
)

type Run struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	WakeAt   int64  `json:"wakeAt"`   // unix seconds; 0 = none
	ParentID int64  `json:"parentId"` // subagent parent; 0 = top-level
	Summary  string `json:"summary"`  // running compaction summary
	Result   string `json:"result"`   // final result (finish) or ask question
	Pending  string `json:"pending"`  // json: a parked approval/ask, if any
	// LastPromptTokens is the provider-reported prompt size of the most recent
	// LLM call — the compaction trigger's ground truth (beats the estimate).
	LastPromptTokens int   `json:"lastPromptTokens"`
	Created          int64 `json:"created"`
	Updated          int64 `json:"updated"`
}

type Message struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"runId"`
	Seq        int    `json:"seq"`
	Role       string `json:"role"` // system|user|assistant|tool
	Content    string `json:"content"`
	Name       string `json:"name"`       // tool name (for role=tool)
	ToolCallID string `json:"toolCallId"` // links a tool result to its call
	ToolCalls  string `json:"toolCalls"`  // json array (assistant tool_calls)
	Tokens     int    `json:"tokens"`     // rough estimate
	Compacted  bool   `json:"compacted"`  // folded into Summary, out of window
	Created    int64  `json:"created"`
}

type Step struct {
	ID      int64  `json:"id"`
	RunID   int64  `json:"runId"`
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"` // llm_call|tool_call|tool_result|compaction|yield|ask|error|finish|note
	Detail  string `json:"detail"`
	Created int64  `json:"created"`
}

type DB struct{ sql *sql.DB }

func openDB(path string) (*DB, error) {
	sq, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// Serialize access: one connection avoids "database is locked" and keeps
	// the loop's read-modify-write steps consistent without explicit locking.
	sq.SetMaxOpenConns(1)
	d := &DB{sql: sq}
	if err := d.init(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *DB) init() error {
	_, err := d.sql.Exec(`
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
`)
	if err != nil {
		return err
	}
	// Best-effort migrations for instances created before a column existed
	// (ALTER fails harmlessly when the column is already present).
	_, _ = d.sql.Exec(`ALTER TABLE runs ADD COLUMN last_prompt_tokens INTEGER NOT NULL DEFAULT 0`)
	// Backfill the FTS index from any messages that predate it (one-time).
	var ftsN int
	_ = d.sql.QueryRow(`SELECT count(*) FROM messages_fts`).Scan(&ftsN)
	if ftsN == 0 {
		_, _ = d.sql.Exec(`INSERT INTO messages_fts(content, run_id, msg_id)
			SELECT content, run_id, id FROM messages WHERE content != ''`)
	}
	return nil
}

func now() int64 { return time.Now().Unix() }

// --- runs ---------------------------------------------------------------

func (d *DB) createRun(title, config string, parentID int64) (int64, error) {
	t := now()
	res, err := d.sql.Exec(
		`INSERT INTO runs (title, status, config, parent_id, created, updated) VALUES (?, ?, ?, ?, ?, ?)`,
		title, statusIdle, config, parentID, t, t)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (d *DB) getRun(id int64) (*Run, error) {
	r := &Run{}
	err := d.sql.QueryRow(
		`SELECT id, title, status, wake_at, parent_id, summary, result, pending, last_prompt_tokens, created, updated FROM runs WHERE id=?`, id).
		Scan(&r.ID, &r.Title, &r.Status, &r.WakeAt, &r.ParentID, &r.Summary, &r.Result, &r.Pending, &r.LastPromptTokens, &r.Created, &r.Updated)
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (d *DB) runConfig(id int64) (Config, error) {
	var raw string
	if err := d.sql.QueryRow(`SELECT config FROM runs WHERE id=?`, id).Scan(&raw); err != nil {
		return Config{}, err
	}
	return parseConfig(raw), nil
}

func (d *DB) listRuns() ([]*Run, error) {
	rows, err := d.sql.Query(
		`SELECT id, title, status, wake_at, parent_id, summary, result, pending, last_prompt_tokens, created, updated FROM runs ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r := &Run{}
		if err := rows.Scan(&r.ID, &r.Title, &r.Status, &r.WakeAt, &r.ParentID, &r.Summary, &r.Result, &r.Pending, &r.LastPromptTokens, &r.Created, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// setStatus updates a run's status and any of the optional parked fields.
func (d *DB) setStatus(id int64, status string, wakeAt int64, result, pending string) error {
	_, err := d.sql.Exec(
		`UPDATE runs SET status=?, wake_at=?, result=?, pending=?, updated=? WHERE id=?`,
		status, wakeAt, result, pending, now(), id)
	return err
}

func (d *DB) touchRun(id int64) {
	_, _ = d.sql.Exec(`UPDATE runs SET updated=? WHERE id=?`, now(), id)
}

func (d *DB) setSummary(id int64, summary string) error {
	_, err := d.sql.Exec(`UPDATE runs SET summary=?, updated=? WHERE id=?`, summary, now(), id)
	return err
}

// setPromptTokens records the provider-reported prompt size of the latest LLM
// call (compaction's ground-truth trigger). Best-effort; skips non-positive.
func (d *DB) setPromptTokens(id int64, n int) {
	if n <= 0 {
		return
	}
	_, _ = d.sql.Exec(`UPDATE runs SET last_prompt_tokens=? WHERE id=?`, n, id)
}

func (d *DB) deleteRun(id int64) error {
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM messages WHERE run_id=?`,
		`DELETE FROM messages_fts WHERE run_id=?`,
		`DELETE FROM steps WHERE run_id=?`,
		`DELETE FROM memory WHERE run_id=?`,
		`DELETE FROM runs WHERE id=?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// hasPending reports whether any run still needs the wake heartbeat — sleeping
// (a future/overdue wake) or left 'running' (a mid-drive/stalled run).
func (d *DB) hasPending() bool {
	var n int
	_ = d.sql.QueryRow(`SELECT count(*) FROM runs WHERE status IN ('sleeping','running')`).Scan(&n)
	return n > 0
}

// dueRuns returns runs the heartbeat should re-drive: sleeping ones whose
// wake_at has passed, plus any left 'running' (a crash mid-drive).
func (d *DB) dueRuns() ([]int64, error) {
	rows, err := d.sql.Query(
		`SELECT id FROM runs WHERE (status=? AND wake_at<=?) OR status=? ORDER BY id`,
		statusSleep, now(), statusRunning)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// --- messages -----------------------------------------------------------

func (d *DB) nextSeq(table, col string, runID int64) int {
	var n sql.NullInt64
	_ = d.sql.QueryRow(fmt.Sprintf(`SELECT MAX(seq) FROM %s WHERE run_id=?`, table), runID).Scan(&n)
	if n.Valid {
		return int(n.Int64) + 1
	}
	return 0
}

func (d *DB) addMessage(m *Message) (int64, error) {
	m.Seq = d.nextSeq("messages", "seq", m.RunID)
	m.Created = now()
	m.Tokens = estimateTokens(m.Content) + estimateTokens(m.ToolCalls)
	res, err := d.sql.Exec(
		`INSERT INTO messages (run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.RunID, m.Seq, m.Role, m.Content, m.Name, m.ToolCallID, m.ToolCalls, m.Tokens, b2i(m.Compacted), m.Created)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	if m.Content != "" {
		_, _ = d.sql.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`, m.Content, m.RunID, id)
	}
	d.touchRun(m.RunID)
	return id, nil
}

func (d *DB) messages(runID int64, onlyLive bool) ([]*Message, error) {
	q := `SELECT id, run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created FROM messages WHERE run_id=?`
	if onlyLive {
		q += ` AND compacted=0`
	}
	q += ` ORDER BY seq`
	rows, err := d.sql.Query(q, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m := &Message{}
		var comp int
		if err := rows.Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &m.Content, &m.Name, &m.ToolCallID, &m.ToolCalls, &m.Tokens, &comp, &m.Created); err != nil {
			return nil, err
		}
		m.Compacted = comp != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// searchMessages runs an FTS5 query over a run's WHOLE transcript — including
// turns compacted out of the live window — newest first. This is the agent's
// recall: detail folded into a summary is still retrievable.
func (d *DB) searchMessages(runID int64, query string, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	rows, err := d.sql.Query(
		`SELECT m.id, m.run_id, m.seq, m.role, m.content
		 FROM messages_fts
		 JOIN messages m ON m.id = messages_fts.msg_id
		 WHERE messages_fts.run_id = ? AND messages_fts MATCH ?
		 ORDER BY m.seq DESC LIMIT ?`, runID, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m := &Message{}
		if err := rows.Scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ftsQuery turns free text into a safe FTS5 MATCH expression: each whitespace
// token becomes a double-quoted literal (AND-ed), so arbitrary user input can't
// trip FTS5 syntax (unbalanced quotes, bare operators).
func ftsQuery(s string) string {
	var toks []string
	for _, f := range strings.Fields(s) {
		f = strings.ReplaceAll(f, `"`, "")
		if f != "" {
			toks = append(toks, `"`+f+`"`)
		}
	}
	return strings.Join(toks, " ")
}

// maxMessageSeq returns the highest message seq for a run, or -1 if none.
func (d *DB) maxMessageSeq(runID int64) int {
	var n sql.NullInt64
	_ = d.sql.QueryRow(`SELECT MAX(seq) FROM messages WHERE run_id=?`, runID).Scan(&n)
	if n.Valid {
		return int(n.Int64)
	}
	return -1
}

// deleteMessagesAfter removes a run's messages with seq > mark (and their FTS
// rows) — a watcher round's rollback when nothing changed.
func (d *DB) deleteMessagesAfter(runID int64, seq int) {
	_, _ = d.sql.Exec(`DELETE FROM messages_fts WHERE msg_id IN (SELECT id FROM messages WHERE run_id=? AND seq>?)`, runID, seq)
	_, _ = d.sql.Exec(`DELETE FROM messages WHERE run_id=? AND seq>?`, runID, seq)
}

func (d *DB) markCompacted(ids []int64) error {
	for _, id := range ids {
		if _, err := d.sql.Exec(`UPDATE messages SET compacted=1 WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// --- steps (the visibility journal) -------------------------------------

func (d *DB) journal(runID int64, kind string, detail any) {
	var raw string
	switch v := detail.(type) {
	case string:
		raw = v
	default:
		b, _ := json.Marshal(v)
		raw = string(b)
	}
	seq := d.nextSeq("steps", "seq", runID)
	_, _ = d.sql.Exec(`INSERT INTO steps (run_id, seq, kind, detail, created) VALUES (?, ?, ?, ?, ?)`,
		runID, seq, kind, raw, now())
	d.touchRun(runID)
}

func (d *DB) steps(runID int64) ([]*Step, error) {
	rows, err := d.sql.Query(`SELECT id, run_id, seq, kind, detail, created FROM steps WHERE run_id=? ORDER BY seq`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Step
	for rows.Next() {
		s := &Step{}
		if err := rows.Scan(&s.ID, &s.RunID, &s.Seq, &s.Kind, &s.Detail, &s.Created); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- memory blocks ------------------------------------------------------

func (d *DB) memorySet(runID int64, key, value string) error {
	_, err := d.sql.Exec(
		`INSERT INTO memory (run_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(run_id, key) DO UPDATE SET value=excluded.value`,
		runID, key, value)
	return err
}

func (d *DB) memoryDelete(runID int64, key string) error {
	_, err := d.sql.Exec(`DELETE FROM memory WHERE run_id=? AND key=?`, runID, key)
	return err
}

func (d *DB) memory(runID int64) (map[string]string, error) {
	rows, err := d.sql.Query(`SELECT key, value FROM memory WHERE run_id=? ORDER BY key`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		m[k] = v
	}
	return m, rows.Err()
}

// --- default settings ---------------------------------------------------

func (d *DB) getSetting(k string) string {
	var v string
	_ = d.sql.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	return v
}

func (d *DB) putSetting(k, v string) error {
	_, err := d.sql.Exec(
		`INSERT INTO settings (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
