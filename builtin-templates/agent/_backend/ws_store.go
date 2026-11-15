// ws_store.go — what the workspace agent keeps beside its conversations
// (docs/workspace-agent.md; D231), in the person's own database: each
// conversation's session (its handle at xbind, scope, model, the connection
// as last read, its plan), the requests it filed for its person's approval
// and what came of each, the tile agents it started, the briefs it wrote
// for them, the messages it holds behind a tile agent's question, and each
// notebook page's stamp (the orgs whose data it holds, the tile and
// revision it describes, its one line). Additive tables, made by
// schemaAdds; nothing here is read outside workspace mode.
package main

import (
	"database/sql"
	"encoding/json"
	"strings"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addWsSchema)
	runDeletedHooks = append(runDeletedHooks, wsRunDeleted)
}

const wsSchema = `
CREATE TABLE IF NOT EXISTS ws_sessions (
  run_id     INTEGER PRIMARY KEY,
  owner      TEXT    NOT NULL,
  main       INTEGER NOT NULL DEFAULT 0,
  handle     TEXT    NOT NULL DEFAULT '',
  model      TEXT    NOT NULL DEFAULT '',
  scope      TEXT    NOT NULL DEFAULT '', -- {orgs, tiles}; '': everything the rules allow
  conn       TEXT    NOT NULL DEFAULT '', -- the handle as xbind last answered it (connection, tainted)
  conn_ms    INTEGER NOT NULL DEFAULT 0,
  plan       TEXT    NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ws_sessions_main ON ws_sessions(owner) WHERE main=1;
-- what the agent asked its person to approve (xbind keeps the approval)
CREATE TABLE IF NOT EXISTS ws_requests (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER NOT NULL,
  kind       TEXT    NOT NULL,            -- call | session | template | changeset
  ref        TEXT    NOT NULL,            -- xbind's id for it
  call_id    TEXT    NOT NULL DEFAULT '', -- the tool call parked on it ('': none)
  tile       TEXT    NOT NULL DEFAULT '',
  title      TEXT    NOT NULL DEFAULT '', -- what it is, in the agent's words
  method     TEXT    NOT NULL DEFAULT '',
  path       TEXT    NOT NULL DEFAULT '',
  body       BLOB,
  ctype      TEXT    NOT NULL DEFAULT '',
  brief_id   INTEGER NOT NULL DEFAULT 0,
  state      TEXT    NOT NULL DEFAULT 'pending',
  outcome    TEXT    NOT NULL DEFAULT '', -- what came of it, as the conversation is told
  owed       INTEGER NOT NULL DEFAULT 0,  -- the outcome waits to be delivered as an update
  msg_id     INTEGER NOT NULL DEFAULT 0,
  created_ms INTEGER NOT NULL DEFAULT 0,
  decided_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ws_requests_run ON ws_requests(run_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ws_requests_ref ON ws_requests(kind, ref);
-- the tile agents the agent started
CREATE TABLE IF NOT EXISTS ws_agents (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER NOT NULL,
  session    TEXT    NOT NULL,            -- xbind's session id
  tile       TEXT    NOT NULL DEFAULT '',
  harness    TEXT    NOT NULL DEFAULT '',
  mode       TEXT    NOT NULL DEFAULT '',
  asks       INTEGER NOT NULL DEFAULT 1,
  brief_id   INTEGER NOT NULL DEFAULT 0,
  status     TEXT    NOT NULL DEFAULT 'starting',
  waiting    TEXT    NOT NULL DEFAULT '', -- what it waits for its person about ('': nothing)
  waits      INTEGER NOT NULL DEFAULT 0,
  last       TEXT    NOT NULL DEFAULT '',
  turn       INTEGER NOT NULL DEFAULT 0,
  told       TEXT    NOT NULL DEFAULT '', -- the state the conversation was last told
  owed       INTEGER NOT NULL DEFAULT 0,  -- a change waits to be delivered as an update
  wake       INTEGER NOT NULL DEFAULT 0,  -- … and asks for a wake
  started_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_ws_agents_session ON ws_agents(session);
CREATE INDEX IF NOT EXISTS idx_ws_agents_run ON ws_agents(run_id, id);
-- messages held behind a tile agent's question, in order
CREATE TABLE IF NOT EXISTS ws_held (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  agent_id   INTEGER NOT NULL,
  text       TEXT    NOT NULL,
  created_ms INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ws_briefs (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id      INTEGER NOT NULL,
  tile        TEXT    NOT NULL,
  goal        TEXT    NOT NULL DEFAULT '',
  done_when   TEXT    NOT NULL DEFAULT '',
  leave_alone TEXT    NOT NULL DEFAULT '',
  check_by    TEXT    NOT NULL DEFAULT '',
  known       TEXT    NOT NULL DEFAULT '',
  state       TEXT    NOT NULL DEFAULT 'draft', -- draft | asked | started | denied | replaced
  session     TEXT    NOT NULL DEFAULT '',
  edited      INTEGER NOT NULL DEFAULT 0,       -- its person changed it
  created_ms  INTEGER NOT NULL DEFAULT 0,
  updated_ms  INTEGER NOT NULL DEFAULT 0
);
-- a notebook page's stamp (the page itself: wiki_pages, coord_know.go)
CREATE TABLE IF NOT EXISTS ws_pages (
  store_id INTEGER NOT NULL,
  slug     TEXT    NOT NULL,
  orgs     TEXT    NOT NULL DEFAULT '[]', -- the orgs whose data it holds
  tile     TEXT    NOT NULL DEFAULT '',
  rev      TEXT    NOT NULL DEFAULT '',
  summary  TEXT    NOT NULL DEFAULT '',
  PRIMARY KEY (store_id, slug)
);
`

// addWsSchema makes the workspace agent's tables (schemaAdds: additive,
// idempotent).
func (d *DB) addWsSchema() error {
	_, err := d.q.Exec(wsSchema)
	return err
}

// wsRunDeleted (runDeletedHooks): a conversation is gone, and what it kept.
// Its handle at xbind is closed by the route that deletes it (ws_sessions.go).
func wsRunDeleted(t *DB, runID int64) error {
	_, _ = t.q.Exec(`DELETE FROM ws_held WHERE agent_id IN (SELECT id FROM ws_agents WHERE run_id=?)`, runID)
	for _, tbl := range []string{"ws_sessions", "ws_requests", "ws_agents", "ws_briefs"} {
		if _, err := t.q.Exec(`DELETE FROM `+tbl+` WHERE run_id=?`, runID); err != nil {
			return err
		}
	}
	return nil
}

// --- sessions ----------------------------------------------------------------------

// wsSession is a workspace conversation's own state.
type wsSession struct {
	Run     int64
	Owner   string
	Main    bool
	Handle  string
	Model   string
	Scope   *wsScope
	Conn    wsHandle // as last read
	ConnMs  int64
	Plan    string
	Created int64
}

const wsSessionCols = `run_id, owner, main, handle, model, scope, conn, conn_ms, plan, created_ms`

func scanWsSession(scan func(...any) error) (*wsSession, error) {
	var s wsSession
	var scope, conn string
	if err := scan(&s.Run, &s.Owner, &s.Main, &s.Handle, &s.Model, &scope, &conn, &s.ConnMs, &s.Plan, &s.Created); err != nil {
		return nil, err
	}
	if scope != "" {
		s.Scope = &wsScope{}
		_ = json.Unmarshal([]byte(scope), s.Scope)
	}
	if conn != "" {
		_ = json.Unmarshal([]byte(conn), &s.Conn)
	}
	return &s, nil
}

func (d *DB) wsSession(runID int64) (*wsSession, error) {
	return scanWsSession(d.q.QueryRow(`SELECT `+wsSessionCols+` FROM ws_sessions WHERE run_id=?`, runID).Scan)
}

// wsSessions are owner's sessions: Main first, then by creation.
func (d *DB) wsSessions(owner string) []*wsSession {
	rows, err := d.q.Query(`SELECT `+wsSessionCols+` FROM ws_sessions WHERE owner=? ORDER BY main DESC, run_id`, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*wsSession
	for rows.Next() {
		if s, err := scanWsSession(rows.Scan); err == nil {
			out = append(out, s)
		}
	}
	return out
}

func wsScopeJSON(sc *wsScope) string {
	if sc == nil {
		return ""
	}
	return mustJSON(sc)
}

// wsPutHandle records what xbind answered for a session's handle.
func (d *DB) wsPutHandle(runID int64, h wsHandle) error {
	_, err := d.q.Exec(`UPDATE ws_sessions SET handle=?, model=?, scope=?, conn=?, conn_ms=? WHERE run_id=?`,
		h.ID, h.Model, wsScopeJSON(h.Scope), mustJSON(h), nowMs(), runID)
	return err
}

// --- requests ----------------------------------------------------------------------

// The kinds of request the agent files.
const (
	wsReqCall      = "call"      // a tile's API, changed
	wsReqSession   = "session"   // a tile agent's start
	wsReqTemplate  = "template"  // a tile made from a template
	wsReqChangeSet = "changeset" // an admin batch
)

// wsReq is one request and what came of it.
type wsReq struct {
	ID      int64  `json:"id"`
	Run     int64  `json:"run"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"` // xbind's id: what the approval chrome is opened with
	CallID  string `json:"callId,omitempty"`
	Tile    string `json:"tile,omitempty"`
	Title   string `json:"title"`
	Method  string `json:"method,omitempty"`
	Path    string `json:"path,omitempty"`
	Body    []byte `json:"-"`
	CType   string `json:"-"`
	Brief   int64  `json:"brief,omitempty"`
	State   string `json:"state"`
	Outcome string `json:"outcome,omitempty"`
	Owed    bool   `json:"-"`
	Created int64  `json:"createdMs"`
	Decided int64  `json:"decidedMs,omitempty"`
}

// open: nothing came of it yet.
func (r *wsReq) open() bool {
	switch r.State {
	case "pending", "approved", "proposed", "applying":
		return true
	}
	return false
}

const wsReqCols = `id, run_id, kind, ref, call_id, tile, title, method, path, body, ctype, brief_id, state, outcome, owed, created_ms, decided_ms`

func scanWsReq(scan func(...any) error) (*wsReq, error) {
	var r wsReq
	err := scan(&r.ID, &r.Run, &r.Kind, &r.Ref, &r.CallID, &r.Tile, &r.Title, &r.Method, &r.Path, &r.Body, &r.CType, &r.Brief,
		&r.State, &r.Outcome, &r.Owed, &r.Created, &r.Decided)
	return &r, err
}

func (d *DB) wsReqs(where string, args ...any) []*wsReq {
	rows, err := d.q.Query(`SELECT `+wsReqCols+` FROM ws_requests `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*wsReq
	for rows.Next() {
		if r, err := scanWsReq(rows.Scan); err == nil {
			out = append(out, r)
		}
	}
	return out
}

func (d *DB) wsReq(id int64) (*wsReq, error) {
	return scanWsReq(d.q.QueryRow(`SELECT `+wsReqCols+` FROM ws_requests WHERE id=?`, id).Scan)
}

func (d *DB) wsAddReq(r *wsReq) error {
	r.Created = nowMs()
	return d.q.QueryRow(`INSERT INTO ws_requests (run_id, kind, ref, call_id, tile, title, method, path, body, ctype, brief_id, state, created_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		r.Run, r.Kind, r.Ref, r.CallID, r.Tile, r.Title, r.Method, r.Path, r.Body, r.CType, r.Brief, orStr(r.State, "pending"), r.Created).Scan(&r.ID)
}

// --- tile agents -------------------------------------------------------------------

// wsAgentRow is a tile agent the agent started, as last seen.
type wsAgentRow struct {
	ID      int64  `json:"id"`
	Run     int64  `json:"run"`
	Session string `json:"session"` // xbind's session id: what Open opens
	Tile    string `json:"tile"`
	Harness string `json:"harness"`
	Mode    string `json:"mode,omitempty"`
	Asks    bool   `json:"asks"`
	Brief   int64  `json:"brief,omitempty"`
	Status  string `json:"status"` // starting | running | idle | ended | failed | stopped | lost
	Waiting string `json:"waiting,omitempty"`
	Waits   bool   `json:"waits"`
	Last    string `json:"last,omitempty"`
	Turn    int    `json:"turn,omitempty"`
	Told    string `json:"-"`
	Owed    bool   `json:"-"`
	Wake    bool   `json:"-"`
	Started int64  `json:"startedMs"`
	Updated int64  `json:"updatedMs"`
}

// live: it may still change.
func (a *wsAgentRow) live() bool {
	switch a.Status {
	case "starting", "running", "idle":
		return true
	}
	return false
}

// wsWorking is the state of a tile agent that is starting or working: the
// one state that is never news to the conversation.
const wsWorking = "working"

// state is what the conversation is told of it: that it works, that it
// waits for its person (and for what), or how it stopped.
func (a *wsAgentRow) state() string {
	switch {
	case a.Waits && a.live():
		return "waiting:" + a.Waiting
	case a.Status == "starting" || a.Status == "running":
		return wsWorking
	}
	return a.Status
}

const wsAgentCols = `id, run_id, session, tile, harness, mode, asks, brief_id, status, waiting, waits, last, turn, told, owed, wake, started_ms, updated_ms`

func scanWsAgent(scan func(...any) error) (*wsAgentRow, error) {
	var a wsAgentRow
	err := scan(&a.ID, &a.Run, &a.Session, &a.Tile, &a.Harness, &a.Mode, &a.Asks, &a.Brief, &a.Status, &a.Waiting, &a.Waits, &a.Last, &a.Turn,
		&a.Told, &a.Owed, &a.Wake, &a.Started, &a.Updated)
	return &a, err
}

func (d *DB) wsAgents(where string, args ...any) []*wsAgentRow {
	rows, err := d.q.Query(`SELECT `+wsAgentCols+` FROM ws_agents `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*wsAgentRow
	for rows.Next() {
		if a, err := scanWsAgent(rows.Scan); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// wsAgentOf is run's tile agent named by its number (#3) or its xbind
// session id.
func (d *DB) wsAgentOf(runID int64, name string) (*wsAgentRow, error) {
	name = strings.TrimSpace(name)
	if n := toInt(strings.TrimPrefix(name, "#")); n > 0 {
		return scanWsAgent(d.q.QueryRow(`SELECT `+wsAgentCols+` FROM ws_agents WHERE run_id=? AND id=?`, runID, n).Scan)
	}
	return scanWsAgent(d.q.QueryRow(`SELECT `+wsAgentCols+` FROM ws_agents WHERE run_id=? AND session=?`, runID, name).Scan)
}

func (d *DB) wsHeld(agentID int64) (ids []int64, texts []string) {
	rows, err := d.q.Query(`SELECT id, text FROM ws_held WHERE agent_id=? ORDER BY id`, agentID)
	if err != nil {
		return nil, nil
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var s string
		if rows.Scan(&id, &s) == nil {
			ids, texts = append(ids, id), append(texts, s)
		}
	}
	return ids, texts
}

// --- briefs ------------------------------------------------------------------------

// wsBrief is what a tile agent will be told, as its person may edit it.
type wsBrief struct {
	ID         int64  `json:"id"`
	Run        int64  `json:"run"`
	Tile       string `json:"tile"`
	Goal       string `json:"goal"`
	DoneWhen   string `json:"doneWhen"`
	LeaveAlone string `json:"leaveAlone"`
	CheckBy    string `json:"checkBy"`
	Known      string `json:"known,omitempty"`
	State      string `json:"state"`
	Session    string `json:"session,omitempty"`
	Edited     bool   `json:"edited,omitempty"`
	Created    int64  `json:"createdMs"`
	Updated    int64  `json:"updatedMs"`
}

const wsBriefCols = `id, run_id, tile, goal, done_when, leave_alone, check_by, known, state, session, edited, created_ms, updated_ms`

func scanWsBrief(scan func(...any) error) (*wsBrief, error) {
	var b wsBrief
	err := scan(&b.ID, &b.Run, &b.Tile, &b.Goal, &b.DoneWhen, &b.LeaveAlone, &b.CheckBy, &b.Known, &b.State, &b.Session, &b.Edited, &b.Created, &b.Updated)
	return &b, err
}

func (d *DB) wsBrief(runID, id int64) (*wsBrief, error) {
	return scanWsBrief(d.q.QueryRow(`SELECT `+wsBriefCols+` FROM ws_briefs WHERE run_id=? AND id=?`, runID, id).Scan)
}

func (d *DB) wsBriefs(runID int64) []*wsBrief {
	rows, err := d.q.Query(`SELECT `+wsBriefCols+` FROM ws_briefs WHERE run_id=? ORDER BY id`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*wsBrief
	for rows.Next() {
		if b, err := scanWsBrief(rows.Scan); err == nil {
			out = append(out, b)
		}
	}
	return out
}

// --- notebook stamps ---------------------------------------------------------------

// wsStamp is a notebook page's stamp.
type wsStamp struct {
	Orgs    []string `json:"orgs,omitempty"`
	Tile    string   `json:"tile,omitempty"`
	Rev     string   `json:"rev,omitempty"`
	Summary string   `json:"summary,omitempty"`
}

func (d *DB) wsStamp(store int64, slug string) wsStamp {
	var s wsStamp
	var orgs string
	if d.q.QueryRow(`SELECT orgs, tile, rev, summary FROM ws_pages WHERE store_id=? AND slug=?`, store, slug).Scan(&orgs, &s.Tile, &s.Rev, &s.Summary) == nil {
		_ = json.Unmarshal([]byte(orgs), &s.Orgs)
	}
	return s
}

func (d *DB) wsPutStamp(store int64, slug string, s wsStamp) error {
	if s.Orgs == nil {
		s.Orgs = []string{}
	}
	_, err := d.q.Exec(`INSERT INTO ws_pages (store_id, slug, orgs, tile, rev, summary) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(store_id, slug) DO UPDATE SET orgs=excluded.orgs, tile=excluded.tile, rev=excluded.rev, summary=excluded.summary`,
		store, slug, mustJSON(s.Orgs), s.Tile, s.Rev, s.Summary)
	return err
}

// wsOne is the single integer q answers (0 when none).
func (d *DB) wsOne(q string, args ...any) int64 {
	var n sql.NullInt64
	_ = d.q.QueryRow(q, args...).Scan(&n)
	return n.Int64
}
