// db.go — the agent's durable state, in an in-component sqlite file
// (XBIN_RES_DB). Everything the engine needs to resume after a crash, a save
// or a backend unload lives here: runs, the transcript, the visibility
// journal, memory blocks, the inbox and the subagent links. The schema and
// its migrations are in migrate.go. See API.md.
//
// Two rules keep this correct with ONE connection (SetMaxOpenConns(1)):
//
//   - every statement goes through d.q, which is the database for a plain DB
//     and the transaction for a view handed out by Tx — calling d.sql while a
//     transaction holds the only connection would deadlock;
//   - work that must happen only if the write happened (a poke, an event)
//     is registered with AfterCommit, never run inline.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go sqlite driver, no cgo
)

// Run status values.
const (
	statusIdle     = "idle"          // awaiting the next human message
	statusRunning  = "running"       // the engine owes it work right now
	statusWaiting  = "waiting_input" // parked on ask_user / tool approval
	statusAwait    = "awaiting"      // parked on its subagents / dependencies
	statusSleep    = "sleeping"      // yielded until wake_at
	statusDone     = "done"          // finished (finish tool); resumable
	statusError    = "error"         // a turn failed; resumable
	statusCanceled = "canceled"      // stopped by a human; resumable
	// Legacy values written by the pre-engine dispatcher. Read-only now: a run
	// found in one is normalised when the engine first touches it (migrate.go).
	statusQueued  = "queued"
	statusBlocked = "blocked"
)

// resting reports statuses a run sits in until something is sent to it.
func resting(s string) bool {
	switch s {
	case statusIdle, statusDone, statusCanceled, statusError:
		return true
	}
	return false
}

type Run struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Kind     string `json:"kind"` // "" (task) | "quick" (a quick ask)
	Status   string `json:"status"`
	WakeAt   int64  `json:"wakeAt"`   // unix seconds; 0 = none
	ParentID int64  `json:"parentId"` // subagent parent; 0 = top-level
	Summary  string `json:"summary"`  // running compaction summary
	Result   string `json:"result"`   // final result (finish) or ask question
	Pending  string `json:"pending"`  // json: a parked approval/await, if any
	// LastPromptTokens is the provider-reported prompt size of the most recent
	// LLM call — the compaction trigger's ground truth (beats the estimate).
	LastPromptTokens int `json:"lastPromptTokens"`
	// Last is the latest assistant answer snippet (list responses only — for
	// the home view's quick-ask cards, where a plain reply leaves result "").
	Last string `json:"last,omitempty"`
	// RootID is the tree this run belongs to (itself when top-level), Depth its
	// distance from the root. Denormalized and immutable.
	RootID int64 `json:"rootId"`
	Depth  int   `json:"depth"`
	// Detached, Outcome, SettledAt and CancelReq are the pre-engine workflow
	// columns. Kept for readers (the tree view, old tiles); the engine decides
	// nothing from them — subagent state lives in links (links.go).
	Detached  bool   `json:"detached"`
	Outcome   string `json:"outcome,omitempty"`
	SettledAt int64  `json:"settledAt,omitempty"`
	CancelReq int64  `json:"cancelReq,omitempty"`
	// Denormalized cost, incremented where the LLM call is recorded.
	LLMCalls         int `json:"llmCalls"`
	PromptTokens     int `json:"promptTokens"`
	CompletionTokens int `json:"completionTokens"`
	// TurnSteps counts model calls in the current turn (the step cap);
	// TurnStarted is when the turn began (unix seconds).
	TurnSteps   int   `json:"turnSteps"`
	TurnStarted int64 `json:"turnStarted"`
	Created     int64 `json:"created"`
	Updated     int64 `json:"updated"`
	// Who may see it and where it came from (D83, migrate_conv.go). Set on the
	// root; a subagent carries a copy for display — access is always decided
	// on the root.
	Owner      string `json:"owner"`      // user id | "el:<path>" | "" (legacy/system)
	Visibility string `json:"visibility"` // private | team
	TeamRole   string `json:"teamRole"`   // what team visibility grants: viewer | participant
	Origin     string `json:"origin"`     // chat | api | schedule | watcher | channel | trigger ("" legacy)
	OriginID   int64  `json:"originId"`   // the automation's row id
	SessionKey string `json:"sessionKey"` // "sched:3", "watch:1", "chan:…" — "" for plain chats
	TitleSrc   string `json:"titleSrc"`   // clip | auto | user | origin ("" legacy: never auto-titled)
	ActivityMs int64  `json:"activityMs"` // last meaningful activity (unix ms), roots only
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
	// Meta is the assistant message's msgMeta as stored (reasoning, usage,
	// wire). Raw on purpose: views strip what they must not carry.
	Meta json.RawMessage `json:"meta,omitempty"`
}

type Step struct {
	ID      int64  `json:"id"`
	RunID   int64  `json:"runId"`
	Seq     int    `json:"seq"`
	Kind    string `json:"kind"` // llm_call|compaction|yield|ask|error|finish|note|link|…
	Detail  string `json:"detail"`
	Created int64  `json:"created"`
}

// queryer is what *sql.DB and *sql.Tx have in common.
type queryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

type DB struct {
	sql *sql.DB
	q   queryer
	tx  *txState // non-nil for a view handed out by Tx
}

type txState struct{ after []func() }

// dsn adds what the sqlite driver needs to share a file with another process
// (a blue/green swap briefly runs two): wait for the other writer rather than
// failing with SQLITE_BUSY, and take the write lock at BEGIN so a transaction
// never has to upgrade mid-way. modernc only honours _pragma and _txlock.
// Rollback journal, not WAL: the file lives on an encrypted FUSE mount.
func dsn(path string) string {
	if path == "" || strings.HasPrefix(path, ":memory:") || strings.Contains(path, "?") {
		return path
	}
	return path + "?_pragma=busy_timeout(5000)&_txlock=immediate"
}

func openDB(path string) (*DB, error) {
	sq, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}
	// One connection: the engine's read-modify-write steps are transactions,
	// and a second connection in this process would only add lock contention.
	sq.SetMaxOpenConns(1)
	d := &DB{sql: sq, q: sq}
	if err := d.migrate(); err != nil {
		return nil, err
	}
	return d, nil
}

// Tx runs fn in one transaction. Nested calls join the outer one. Functions
// registered with AfterCommit run once the commit succeeded, in order.
func (d *DB) Tx(fn func(t *DB) error) error {
	if d.tx != nil {
		return fn(d)
	}
	tx, err := d.sql.Begin()
	if err != nil {
		return err
	}
	st := &txState{}
	t := &DB{sql: d.sql, q: tx, tx: st}
	if err := fn(t); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, f := range st.after {
		f()
	}
	return nil
}

// AfterCommit defers f until the enclosing transaction commits (at once when
// there is none). A rolled-back transaction drops it.
func (d *DB) AfterCommit(f func()) {
	if d.tx != nil {
		d.tx.after = append(d.tx.after, f)
		return
	}
	f()
}

func now() int64 { return time.Now().Unix() }

// --- runs ---------------------------------------------------------------

// createRun inserts a run and places it in the graph: root_id and depth are
// derived from the parent here, so no caller can place a run inconsistently.
func (d *DB) createRun(title, config string, parentID int64) (int64, error) {
	return d.createRunStatus(title, config, parentID, statusIdle)
}

func (d *DB) createRunStatus(title, config string, parentID int64, status string) (int64, error) {
	return d.createRunStamped(title, config, parentID, status, runStamp{})
}

// runStamp is who a new top-level run belongs to and where it came from. The
// zero value is a legacy/system run: no owner, visible to the whole team.
type runStamp struct {
	Owner, Visibility, TeamRole string
	Origin                      string
	OriginID                    int64
	SessionKey, TitleSrc        string
}

func (d *DB) createRunStamped(title, config string, parentID int64, status string, st runStamp) (int64, error) {
	t := now()
	rootID, depth := int64(0), 0
	if parentID != 0 {
		if p, err := d.getRun(parentID); err == nil {
			rootID, depth = p.RootID, p.Depth+1
			if rootID == 0 {
				rootID = p.ID
			}
			// A subagent carries its root's stamp (for display; access is
			// decided on the root).
			st = runStamp{Owner: p.Owner, Visibility: p.Visibility, TeamRole: p.TeamRole,
				Origin: p.Origin, OriginID: p.OriginID, TitleSrc: "origin"}
		}
	}
	if st.Visibility == "" {
		st.Visibility = visTeam
	}
	if st.TeamRole == "" {
		st.TeamRole = roleParticipant
	}
	activity := int64(0)
	if parentID == 0 {
		activity = time.Now().UnixMilli()
	}
	var id int64
	err := d.q.QueryRow(
		`INSERT INTO runs (title, status, config, parent_id, root_id, depth, detached, created, updated,
		   owner, visibility, team_role, origin, origin_id, session_key, title_src, activity_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		title, status, config, parentID, rootID, depth, b2i(parentID != 0), t, t,
		st.Owner, st.Visibility, st.TeamRole, st.Origin, st.OriginID, st.SessionKey, st.TitleSrc, activity).Scan(&id)
	if err != nil {
		return 0, err
	}
	if rootID == 0 { // top-level: it is its own root
		if _, err := d.q.Exec(`UPDATE runs SET root_id=? WHERE id=?`, id, id); err != nil {
			return 0, err
		}
	}
	return id, nil
}

const runCols = `id, title, kind, status, wake_at, parent_id, summary, result, pending, last_prompt_tokens, created, updated, root_id, depth, detached, outcome, settled_at, cancel_req, llm_calls, prompt_tokens, completion_tokens, turn_steps, turn_started, owner, visibility, team_role, origin, origin_id, session_key, title_src, activity_ms`

func scanRun(scan func(dest ...any) error) (*Run, error) {
	r := &Run{}
	var detached int
	if err := scan(&r.ID, &r.Title, &r.Kind, &r.Status, &r.WakeAt, &r.ParentID, &r.Summary, &r.Result, &r.Pending,
		&r.LastPromptTokens, &r.Created, &r.Updated, &r.RootID, &r.Depth, &detached, &r.Outcome, &r.SettledAt,
		&r.CancelReq, &r.LLMCalls, &r.PromptTokens, &r.CompletionTokens, &r.TurnSteps, &r.TurnStarted,
		&r.Owner, &r.Visibility, &r.TeamRole, &r.Origin, &r.OriginID, &r.SessionKey, &r.TitleSrc, &r.ActivityMs); err != nil {
		return nil, err
	}
	r.Detached = detached != 0
	return r, nil
}

func (d *DB) getRun(id int64) (*Run, error) {
	return scanRun(d.q.QueryRow(`SELECT `+runCols+` FROM runs WHERE id=?`, id).Scan)
}

func (d *DB) queryRuns(where string, args ...any) ([]*Run, error) {
	rows, err := d.q.Query(`SELECT `+runCols+` FROM runs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (d *DB) runConfig(id int64) (Config, error) {
	var raw string
	if err := d.q.QueryRow(`SELECT config FROM runs WHERE id=?`, id).Scan(&raw); err != nil {
		return Config{}, err
	}
	return parseConfig(raw), nil
}

func (d *DB) listRuns() ([]*Run, error) { return d.queryRuns(`ORDER BY id DESC`) }

// rootRuns lists top-level runs only — the sidebar's source, so no subagent
// can ever surface there however the client filters.
func (d *DB) rootRuns() ([]*Run, error) { return d.queryRuns(`WHERE parent_id=0 ORDER BY id DESC`) }

// treeRuns returns every run in a tree, oldest first. root_id is denormalized
// precisely so this is an index scan rather than a recursive walk.
func (d *DB) treeRuns(rootID int64) ([]*Run, error) {
	return d.queryRuns(`WHERE root_id=? ORDER BY created, id`, rootID)
}

// lastAssistantByRun returns each run's most recent non-empty assistant
// message (one query; used to decorate the runs list for the home view).
func (d *DB) lastAssistantByRun() map[int64]string {
	out := map[int64]string{}
	rows, err := d.q.Query(`SELECT run_id, content FROM messages
		WHERE id IN (SELECT MAX(id) FROM messages WHERE role='assistant' AND content!='' GROUP BY run_id)`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var c string
		if rows.Scan(&id, &c) == nil {
			out[id] = c
		}
	}
	return out
}

// lastAssistant is one run's latest non-empty assistant text.
func (d *DB) lastAssistant(runID int64) string {
	var c string
	_ = d.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND role='assistant' AND content!=''
		ORDER BY seq DESC LIMIT 1`, runID).Scan(&c)
	return c
}

// setRunKind tags a run ("quick" = a quick ask; "" = ordinary task).
func (d *DB) setRunKind(id int64, kind string) {
	_, _ = d.q.Exec(`UPDATE runs SET kind=? WHERE id=?`, kind, id)
}

// setStatus updates a run's status and any of the optional parked fields.
func (d *DB) setStatus(id int64, status string, wakeAt int64, result, pending string) error {
	_, err := d.q.Exec(
		`UPDATE runs SET status=?, wake_at=?, result=?, pending=?, updated=? WHERE id=?`,
		status, wakeAt, result, pending, now(), id)
	return err
}

// setStatusOnly changes the status, keeping result/pending/wake_at.
func (d *DB) setStatusOnly(id int64, status string) error {
	_, err := d.q.Exec(`UPDATE runs SET status=?, updated=? WHERE id=?`, status, now(), id)
	return err
}

func (d *DB) touchRun(id int64) {
	_, _ = d.q.Exec(`UPDATE runs SET updated=? WHERE id=?`, now(), id)
}

func (d *DB) setSummary(id int64, summary string) error {
	_, err := d.q.Exec(`UPDATE runs SET summary=?, updated=? WHERE id=?`, summary, now(), id)
	return err
}

// addRunCost accumulates what a run has spent (denormalized: the tile shows
// cost continuously).
func (d *DB) addRunCost(id int64, prompt, completion int) {
	_, _ = d.q.Exec(
		`UPDATE runs SET llm_calls=llm_calls+1, prompt_tokens=prompt_tokens+?, completion_tokens=completion_tokens+? WHERE id=?`,
		prompt, completion, id)
}

// setPromptTokens records the provider-reported prompt size of the latest LLM
// call (compaction's ground-truth trigger). Best-effort; skips non-positive.
func (d *DB) setPromptTokens(id int64, n int) {
	if n <= 0 {
		return
	}
	_, _ = d.q.Exec(`UPDATE runs SET last_prompt_tokens=? WHERE id=?`, n, id)
}

// deleteRun removes a run AND everything it spawned. Subagent runs are only
// meaningful as part of their parent's work, so leaving them behind orphans
// rows that nothing will ever read or clean up.
func (d *DB) deleteRun(id int64) error {
	kids, err := d.descendants(id)
	if err != nil {
		return err
	}
	return d.Tx(func(t *DB) error {
		for i := len(kids) - 1; i >= 0; i-- { // deepest first
			if err := t.deleteOneRun(kids[i]); err != nil {
				return err
			}
		}
		return t.deleteOneRun(id)
	})
}

func (d *DB) deleteOneRun(id int64) error {
	return d.Tx(func(t *DB) error {
		for _, q := range []string{
			`DELETE FROM messages WHERE run_id=?`,
			`DELETE FROM messages_fts WHERE run_id=?`,
			`DELETE FROM steps WHERE run_id=?`,
			`DELETE FROM memory WHERE run_id=?`,
			`DELETE FROM repl_files WHERE run_id=?`,
			`DELETE FROM repl_log WHERE run_id=?`,
			`DELETE FROM message_files WHERE run_id=?`,
			`DELETE FROM run_deps WHERE run_id=?1 OR dep_id=?1`,
			`DELETE FROM run_trees WHERE root_id=?`,
			`DELETE FROM inbox WHERE run_id=?`,
			`DELETE FROM link_deps WHERE child_id=?1 OR dep_run=?1`,
			`DELETE FROM links WHERE parent_id=?1 OR child_id=?1`,
			`DELETE FROM runs WHERE id=?`,
		} {
			if _, err := t.q.Exec(q, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// sweepOrphanRuns deletes subagent runs whose parent row no longer exists —
// left behind by deletes from before the cascade existed. Logged, not silent.
func (d *DB) sweepOrphanRuns() int {
	rows, err := d.q.Query(
		`SELECT id FROM runs WHERE parent_id<>0
		   AND NOT EXISTS (SELECT 1 FROM runs p WHERE p.id = runs.parent_id)`)
	if err != nil {
		return 0
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if d.deleteRun(id) == nil {
			n++
		}
	}
	return n
}

// descendants returns every run below id, parents before children.
func (d *DB) descendants(id int64) ([]int64, error) {
	rows, err := d.q.Query(`
		WITH RECURSIVE sub(id, lvl) AS (
		  SELECT id, 1 FROM runs WHERE parent_id=?
		  UNION ALL
		  SELECT r.id, sub.lvl+1 FROM runs r JOIN sub ON r.parent_id = sub.id
		) SELECT id FROM sub ORDER BY lvl, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var n int64
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// --- messages -----------------------------------------------------------

// addMessage appends to a run's transcript. The seq is allocated inside the
// INSERT itself, so two writers can never draw the same one (the old
// SELECT MAX then INSERT could).
func (d *DB) addMessage(m *Message) (int64, error) {
	m.Created = now()
	m.Tokens = estimateTokens(m.Content) + estimateTokens(m.ToolCalls)
	meta := ""
	if len(m.Meta) > 0 {
		meta = string(m.Meta)
	}
	var id int64
	err := d.q.QueryRow(
		`INSERT INTO messages (run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created, meta)
		 SELECT ?1, COALESCE(MAX(seq), -1) + 1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10 FROM messages WHERE run_id=?1
		 RETURNING id, seq`,
		m.RunID, m.Role, m.Content, m.Name, m.ToolCallID, m.ToolCalls, m.Tokens, b2i(m.Compacted), m.Created, meta).
		Scan(&id, &m.Seq)
	if err != nil {
		return 0, err
	}
	m.ID = id
	if m.Content != "" {
		_, _ = d.q.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`, m.Content, m.RunID, id)
	}
	d.touchRun(m.RunID)
	return id, nil
}

// Tool-result placeholders. A result that is still one of these has not been
// settled, and only then may a late writer replace it (casToolResult) — a
// tool from an interrupted step or a subagent result arriving after its wait
// was demoted must never overwrite what the model has already seen.
const (
	toolRunning          = "(running…)"
	toolAwaitingApproval = "(awaiting your approval)"
	toolWaitingPrefix    = "(waiting for "
	toolLostToRestart    = "(no result: the backend restarted while this tool was running)"
)

func isPlaceholder(s string) bool {
	return s == toolRunning || s == toolAwaitingApproval || strings.HasPrefix(s, toolWaitingPrefix)
}

// toolResultRow finds the result row answering a tool call.
func (d *DB) toolResultRow(runID int64, toolCallID string) (id int64, content string, err error) {
	err = d.q.QueryRow(
		`SELECT id, content FROM messages WHERE run_id=? AND role='tool' AND tool_call_id=? ORDER BY seq DESC LIMIT 1`,
		runID, toolCallID).Scan(&id, &content)
	return
}

func (d *DB) rewriteMessage(runID, id int64, content string) error {
	if _, err := d.q.Exec(`UPDATE messages SET content=?, tokens=? WHERE id=?`, content, estimateTokens(content), id); err != nil {
		return err
	}
	// Keep the FTS row in step, or recall would search the placeholder text.
	if res, err := d.q.Exec(`UPDATE messages_fts SET content=? WHERE msg_id=?`, content, id); err != nil || rowsAffected(res) == 0 {
		_, _ = d.q.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`, content, runID, id)
	}
	d.touchRun(runID)
	return nil
}

// updateToolResult rewrites a tool result in place, unconditionally. In place
// rather than appended because seq is the transcript's order: results stay in
// CALL order however the tools finished.
func (d *DB) updateToolResult(runID int64, toolCallID, content string) error {
	id, _, err := d.toolResultRow(runID, toolCallID)
	if err != nil {
		return err
	}
	return d.rewriteMessage(runID, id, content)
}

// casToolResult settles a tool result only while it is still a placeholder;
// false means it was already settled (or there is no row).
func (d *DB) casToolResult(runID int64, toolCallID, content string) (bool, error) {
	id, cur, err := d.toolResultRow(runID, toolCallID)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !isPlaceholder(cur) {
		return false, nil
	}
	return true, d.rewriteMessage(runID, id, content)
}

// setToolPlaceholder moves an unsettled result to another placeholder state
// (running → awaiting approval, running → waiting for #N).
func (d *DB) setToolPlaceholder(runID int64, toolCallID, placeholder string) (bool, error) {
	return d.casToolResult(runID, toolCallID, placeholder)
}

func (d *DB) messageByID(id int64) (*Message, error) {
	rows, err := d.q.Query(`SELECT `+msgCols+` FROM messages WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, sql.ErrNoRows
	}
	return scanMessage(rows.Scan)
}

// insertMessagesAfter splices messages in directly after seq `afterSeq`,
// shifting every later message down to make room (repair uses it).
func (d *DB) insertMessagesAfter(runID int64, afterSeq int, msgs []*Message) error {
	if len(msgs) == 0 {
		return nil
	}
	return d.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`UPDATE messages SET seq = seq + ? WHERE run_id=? AND seq > ?`,
			len(msgs), runID, afterSeq); err != nil {
			return err
		}
		ts := now()
		for i, m := range msgs {
			var id int64
			err := t.q.QueryRow(
				`INSERT INTO messages (run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created)
				 VALUES (?, ?, ?, ?, ?, ?, '', ?, 0, ?) RETURNING id`,
				runID, afterSeq+1+i, m.Role, m.Content, m.Name, m.ToolCallID, estimateTokens(m.Content), ts).Scan(&id)
			if err != nil {
				return err
			}
			m.ID = id
			if m.Content != "" {
				if _, err := t.q.Exec(`INSERT INTO messages_fts(content, run_id, msg_id) VALUES (?, ?, ?)`,
					m.Content, runID, id); err != nil {
					return err
				}
			}
		}
		t.touchRun(runID)
		return nil
	})
}

// setSeqs rewrites the order of a run's messages: ids listed get seq 0..n-1
// (repair's reordering; one transaction).
func (d *DB) setSeqs(runID int64, ids []int64) error {
	return d.Tx(func(t *DB) error {
		// Two passes through negative seqs so no unique-ish ordering is violated
		// halfway (seq is not unique, but readers order by it).
		for i, id := range ids {
			if _, err := t.q.Exec(`UPDATE messages SET seq=? WHERE id=? AND run_id=?`, -1-i, id, runID); err != nil {
				return err
			}
		}
		_, err := t.q.Exec(`UPDATE messages SET seq = -seq - 1 WHERE run_id=? AND seq < 0`, runID)
		return err
	})
}

const msgCols = `id, run_id, seq, role, content, name, tool_call_id, tool_calls, tokens, compacted, created, meta`

func scanMessage(scan func(dest ...any) error) (*Message, error) {
	m := &Message{}
	var comp int
	var meta string
	if err := scan(&m.ID, &m.RunID, &m.Seq, &m.Role, &m.Content, &m.Name, &m.ToolCallID, &m.ToolCalls, &m.Tokens, &comp, &m.Created, &meta); err != nil {
		return nil, err
	}
	m.Compacted = comp != 0
	if meta != "" && json.Valid([]byte(meta)) {
		m.Meta = json.RawMessage(meta)
	}
	return m, nil
}

func (d *DB) messages(runID int64, onlyLive bool) ([]*Message, error) {
	q := `SELECT ` + msgCols + ` FROM messages WHERE run_id=?`
	if onlyLive {
		q += ` AND compacted=0`
	}
	q += ` ORDER BY seq, id`
	rows, err := d.q.Query(q, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// searchMessages runs an FTS5 query over a run's WHOLE transcript — including
// turns compacted out of the live window — newest first (recall).
func (d *DB) searchMessages(runID int64, query string, limit int) ([]*Message, error) {
	if limit <= 0 || limit > 20 {
		limit = 8
	}
	rows, err := d.q.Query(
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
// token becomes a double-quoted literal (AND-ed).
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
	_ = d.q.QueryRow(`SELECT MAX(seq) FROM messages WHERE run_id=?`, runID).Scan(&n)
	if n.Valid {
		return int(n.Int64)
	}
	return -1
}

// deleteMessagesAfter removes a run's messages with seq > mark (and their FTS
// rows) — a watcher round's rollback when nothing changed.
func (d *DB) deleteMessagesAfter(runID int64, seq int) {
	_, _ = d.q.Exec(`DELETE FROM messages_fts WHERE msg_id IN (SELECT id FROM messages WHERE run_id=? AND seq>?)`, runID, seq)
	_, _ = d.q.Exec(`DELETE FROM message_files WHERE msg_id IN (SELECT id FROM messages WHERE run_id=? AND seq>?)`, runID, seq)
	_, _ = d.q.Exec(`DELETE FROM messages WHERE run_id=? AND seq>?`, runID, seq)
}

func (d *DB) markCompacted(ids []int64) error {
	for _, id := range ids {
		if _, err := d.q.Exec(`UPDATE messages SET compacted=1 WHERE id=?`, id); err != nil {
			return err
		}
	}
	return nil
}

// --- steps (the visibility journal) -------------------------------------

// journal appends a step. Its seq is allocated in the INSERT, like messages.
func (d *DB) journal(runID int64, kind string, detail any) *Step {
	var raw string
	switch v := detail.(type) {
	case string:
		raw = v
	default:
		b, _ := json.Marshal(v)
		raw = string(b)
	}
	s := &Step{RunID: runID, Kind: kind, Detail: raw, Created: now()}
	if err := d.q.QueryRow(
		`INSERT INTO steps (run_id, seq, kind, detail, created)
		 SELECT ?1, COALESCE(MAX(seq), -1) + 1, ?2, ?3, ?4 FROM steps WHERE run_id=?1 RETURNING id, seq`,
		runID, kind, raw, s.Created).Scan(&s.ID, &s.Seq); err != nil {
		return nil
	}
	d.touchRun(runID)
	return s
}

func (d *DB) steps(runID int64) ([]*Step, error) {
	rows, err := d.q.Query(`SELECT id, run_id, seq, kind, detail, created FROM steps WHERE run_id=? ORDER BY seq, id`, runID)
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
	_, err := d.q.Exec(
		`INSERT INTO memory (run_id, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(run_id, key) DO UPDATE SET value=excluded.value`,
		runID, key, value)
	return err
}

func (d *DB) memoryDelete(runID int64, key string) error {
	_, err := d.q.Exec(`DELETE FROM memory WHERE run_id=? AND key=?`, runID, key)
	return err
}

func (d *DB) memory(runID int64) (map[string]string, error) {
	rows, err := d.q.Query(`SELECT key, value FROM memory WHERE run_id=? ORDER BY key`, runID)
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

// --- settings -----------------------------------------------------------

func (d *DB) getSetting(k string) string {
	var v string
	_ = d.q.QueryRow(`SELECT v FROM settings WHERE k=?`, k).Scan(&v)
	return v
}

func (d *DB) putSetting(k, v string) error {
	_, err := d.q.Exec(
		`INSERT INTO settings (k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v=excluded.v`, k, v)
	return err
}

// --- spawn budget -------------------------------------------------------

// ensureTree creates a root's budget row, snapshotting the limit in force when
// the workflow started so changing the default cannot move the goalposts.
func (d *DB) ensureTree(rootID int64, maxSpawn int) {
	_, _ = d.q.Exec(
		`INSERT OR IGNORE INTO run_trees (root_id, spawned, max_spawn, max_depth, created) VALUES (?, 0, ?, 0, ?)`,
		rootID, maxSpawn, now())
}

// reserveSpawn takes one unit of a tree's LIFETIME budget (a monotonic
// counter, compare-and-swapped in one statement), or reports it is spent.
func (d *DB) reserveSpawn(rootID int64, fallbackMax int) (ok bool, spawned, max int) {
	d.ensureTree(rootID, fallbackMax)
	res, err := d.q.Exec(`UPDATE run_trees SET spawned = spawned + 1 WHERE root_id=? AND spawned < max_spawn`, rootID)
	_ = d.q.QueryRow(`SELECT spawned, max_spawn FROM run_trees WHERE root_id=?`, rootID).Scan(&spawned, &max)
	if err != nil {
		return false, spawned, max
	}
	return rowsAffected(res) == 1, spawned, max
}

// --- helpers --------------------------------------------------------------

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func rowsAffected(res sql.Result) int64 {
	if res == nil {
		return 0
	}
	n, _ := res.RowsAffected()
	return n
}

// errGone is a run deleted out from under whoever was working on it.
var errGone = errors.New("run is gone")

func logf(format string, args ...any) { log.Printf("agent: "+format, args...) }

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf("%q", err.Error())
	}
	return string(b)
}
