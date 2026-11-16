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
	"sort"
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
  call_id    TEXT    NOT NULL DEFAULT '', -- the tool call that filed it ('': the page's Start, or older): where the page draws it
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
-- what xbind (or the session's own bounds) refused a tool call, in fields
CREATE TABLE IF NOT EXISTS ws_refusals (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  run_id     INTEGER NOT NULL,
  call_id    TEXT    NOT NULL DEFAULT '', -- the tool call it answered
  tool       TEXT    NOT NULL DEFAULT '',
  kind       TEXT    NOT NULL,            -- hidden | policy | cannot | paused | off | slow | taint | not-connected
  reason     TEXT    NOT NULL DEFAULT '', -- xbind's sentence
  tile       TEXT    NOT NULL DEFAULT '',
  created_ms INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_ws_refusals_run ON ws_refusals(run_id, id);
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
	if _, err := d.q.Exec(wsSchema); err != nil {
		return err
	}
	// columns added after the tables were first made (ALTER fails
	// harmlessly when the column is already there)
	for _, q := range []string{
		`ALTER TABLE ws_briefs ADD COLUMN harness TEXT NOT NULL DEFAULT ''`, // the person's pick among what the tile allows
		`ALTER TABLE ws_sessions ADD COLUMN allow TEXT NOT NULL DEFAULT ''`, // which stamped orgs allow the session's model, as last asked
		// a request's outcome in fields, for the page (the prose stays for the model)
		`ALTER TABLE ws_requests ADD COLUMN grant_scope TEXT NOT NULL DEFAULT ''`,      // once | call | tile
		`ALTER TABLE ws_requests ADD COLUMN grant_until_ms INTEGER NOT NULL DEFAULT 0`, // a standing approval's end (0: until taken back)
		`ALTER TABLE ws_requests ADD COLUMN decided_by TEXT NOT NULL DEFAULT ''`,       // person | agent | xbin
		`ALTER TABLE ws_requests ADD COLUMN decided_via TEXT NOT NULL DEFAULT ''`,      // session | app | device
		`ALTER TABLE ws_requests ADD COLUMN result_status INTEGER NOT NULL DEFAULT 0`,  // the call's HTTP status, when one ran
		`ALTER TABLE ws_requests ADD COLUMN result TEXT NOT NULL DEFAULT ''`,           // one line of what came of it
		`ALTER TABLE ws_requests ADD COLUMN expires_ms INTEGER NOT NULL DEFAULT 0`,     // when a pending request lapses
		`ALTER TABLE ws_briefs ADD COLUMN call_id TEXT NOT NULL DEFAULT ''`,            // the tile_agent_brief call that wrote it: where the page draws it
		// what started a tile agent: where the page sets its card
		`ALTER TABLE ws_agents ADD COLUMN request_id INTEGER NOT NULL DEFAULT 0`, // the request that started it (ws_requests.id)
		`ALTER TABLE ws_agents ADD COLUMN call_id TEXT NOT NULL DEFAULT ''`,      // the tile_agent_start call that filed that request
		// the conversation's own record of the orgs whose data it holds, as xbind
		// ever reported them: it outlives every handle, and a new handle is
		// tainted with it before anything runs on it (wsReopen)
		`ALTER TABLE ws_sessions ADD COLUMN tainted TEXT NOT NULL DEFAULT ''`,
		// why the conversation can't go on on its model ('': it can): xbind
		// refused a new handle on it what the conversation holds (wsHeld)
		`ALTER TABLE ws_sessions ADD COLUMN held TEXT NOT NULL DEFAULT ''`,
		// why xbind refused the messages held for a tile agent ('': none): told once, then cleared
		`ALTER TABLE ws_agents ADD COLUMN refused TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = d.q.Exec(q)
	}
	// the turn the conversation was last told of (wsSeen). Forward only,
	// once: when the column is new, a tile agent from before it has been
	// told of the turns it has had — or is owed its news already
	if _, err := d.q.Exec(`ALTER TABLE ws_agents ADD COLUMN told_turn INTEGER NOT NULL DEFAULT 0`); err == nil {
		_, _ = d.q.Exec(`UPDATE ws_agents SET told_turn = turn`)
	}
	// forward only, once per row: a session from before the column keeps the
	// taint its stored handle carried
	_, _ = d.q.Exec(`UPDATE ws_sessions SET tainted = json_extract(conn, '$.tainted')
		WHERE tainted = '' AND json_valid(conn) AND json_type(conn, '$.tainted') = 'array'`)
	return nil
}

// wsRunDeleted (runDeletedHooks): a conversation is gone, and what it kept.
// Its handle at xbind is closed by the route that deletes it (ws_sessions.go).
func wsRunDeleted(t *DB, runID int64) error {
	_, _ = t.q.Exec(`DELETE FROM ws_held WHERE agent_id IN (SELECT id FROM ws_agents WHERE run_id=?)`, runID)
	for _, tbl := range []string{"ws_sessions", "ws_requests", "ws_agents", "ws_briefs", "ws_refusals"} {
		if _, err := t.q.Exec(`DELETE FROM `+tbl+` WHERE run_id=?`, runID); err != nil {
			return err
		}
	}
	return nil
}

// --- sessions ----------------------------------------------------------------------

// wsSession is a workspace conversation's own state.
type wsSession struct {
	Run    int64
	Owner  string
	Main   bool
	Handle string
	Model  string
	Scope  *wsScope
	Conn   wsHandle // as last read
	ConnMs int64
	// Allow is which orgs (of the notebook's stamps) allow the session's
	// model, as xbind last answered: org → "" (allowed) or why not. An org
	// that isn't in it has not been asked: its pages are closed until it is.
	Allow   map[string]string
	Plan    string
	Created int64
	// Tainted is the orgs whose data the conversation holds — every org
	// xbind ever reported for any of its handles, and every notebook stamp it
	// was shown. It only grows; a handle is never used before xbind holds all
	// of it (wsReopen).
	Tainted []string
	// Held, when set, is why the conversation can't go on on its model: a new
	// handle could not be made to hold what it holds. Nothing runs on it —
	// no model call, no tool — until its person moves it to a model that
	// may, or xbind's rules allow it again.
	Held *wsHeld
}

// wsHeld is a conversation that can't go on on its model: the orgs xbind
// refused a new handle on Model, its reason, and the ways forward — the
// model the conversation was on, where that is another one, or a new
// session.
type wsHeld struct {
	Code   string   `json:"code"` // model_taint | org
	Model  string   `json:"model"`
	Orgs   []string `json:"orgs,omitempty"`
	Reason string   `json:"reason,omitempty"`
	Was    string   `json:"was,omitempty"` // the model it was on, when the session's model now names another (a role's)
	At     int64    `json:"atMs"`
}

const wsSessionCols = `run_id, owner, main, handle, model, scope, conn, conn_ms, plan, created_ms, allow, tainted, held`

func scanWsSession(scan func(...any) error) (*wsSession, error) {
	var s wsSession
	var scope, conn, allow, tainted, held string
	if err := scan(&s.Run, &s.Owner, &s.Main, &s.Handle, &s.Model, &scope, &conn, &s.ConnMs, &s.Plan, &s.Created, &allow, &tainted, &held); err != nil {
		return nil, err
	}
	if tainted != "" {
		_ = json.Unmarshal([]byte(tainted), &s.Tainted)
	}
	if held != "" {
		s.Held = &wsHeld{}
		if json.Unmarshal([]byte(held), s.Held) != nil {
			s.Held = &wsHeld{Code: "model_taint", Reason: "xbin could not be asked whether this model may hold what the conversation holds"}
		}
	}
	if allow != "" {
		_ = json.Unmarshal([]byte(allow), &s.Allow)
	}
	if scope != "" {
		s.Scope = &wsScope{}
		_ = json.Unmarshal([]byte(scope), s.Scope)
	}
	if conn != "" {
		_ = json.Unmarshal([]byte(conn), &s.Conn)
	}
	s.Tainted = wsUnion(s.Tainted, s.Conn.Tainted)
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

// wsPutHandle records what xbind answered for a session's handle, and adds
// the orgs it reports the conversation holds to the conversation's own
// record — which only grows: what it holds now, every org of every handle.
func (d *DB) wsPutHandle(runID int64, h wsHandle) ([]string, error) {
	var raw string
	var had []string
	if err := d.q.QueryRow(`SELECT tainted FROM ws_sessions WHERE run_id=?`, runID).Scan(&raw); err != nil {
		return nil, err
	}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &had)
	}
	all := wsUnion(had, h.Tainted)
	if all == nil {
		all = []string{}
	}
	_, err := d.q.Exec(`UPDATE ws_sessions SET handle=?, model=?, scope=?, conn=?, conn_ms=?, tainted=? WHERE run_id=?`,
		h.ID, h.Model, wsScopeJSON(h.Scope), mustJSON(h), nowMs(), mustJSON(all), runID)
	return all, err
}

// wsPutHeld records why the conversation can't go on (nil: it can again).
func (d *DB) wsPutHeld(runID int64, held *wsHeld) error {
	raw := ""
	if held != nil {
		raw = mustJSON(held)
	}
	_, err := d.q.Exec(`UPDATE ws_sessions SET held=? WHERE run_id=?`, raw, runID)
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
	Ref     string `json:"ref"`              // xbind's id: what the approval chrome is opened with
	CallID  string `json:"callId,omitempty"` // the tool call that filed it, for its life ("": the page's Start filed it)
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
	// The outcome in fields, for the page (Outcome is the same in the words
	// the model is told): how it was allowed — Grant once | call | tile, a
	// standing one until GrantUntil (0: until taken back) —, By whom it was
	// decided (person | agent | xbin) and Via what (session | app | device),
	// the HTTP status of the call that ran and one line of what came of it,
	// and when a pending request lapses.
	Grant        string `json:"grant,omitempty"`
	GrantUntil   int64  `json:"grantUntilMs,omitempty"`
	By           string `json:"by,omitempty"`
	Via          string `json:"via,omitempty"`
	ResultStatus int    `json:"resultStatus,omitempty"`
	Result       string `json:"result,omitempty"`
	Expires      int64  `json:"expiresMs,omitempty"`
	// A plan's row, for the page's card (handleWsCards fills them from the
	// row's body): Plan says it is one, Lines each line's outcome once it
	// is decided.
	Plan  bool         `json:"plan,omitempty"`
	Lines []wsPlanLine `json:"lines,omitempty"`
}

// open: nothing came of it yet.
func (r *wsReq) open() bool {
	switch r.State {
	case "pending", "approved", "proposed", "applying":
		return true
	}
	return false
}

const wsReqCols = `id, run_id, kind, ref, call_id, tile, title, method, path, body, ctype, brief_id, state, outcome, owed, created_ms, decided_ms,
	grant_scope, grant_until_ms, decided_by, decided_via, result_status, result, expires_ms`

func scanWsReq(scan func(...any) error) (*wsReq, error) {
	var r wsReq
	err := scan(&r.ID, &r.Run, &r.Kind, &r.Ref, &r.CallID, &r.Tile, &r.Title, &r.Method, &r.Path, &r.Body, &r.CType, &r.Brief,
		&r.State, &r.Outcome, &r.Owed, &r.Created, &r.Decided, &r.Grant, &r.GrantUntil, &r.By, &r.Via, &r.ResultStatus, &r.Result, &r.Expires)
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
	return d.q.QueryRow(`INSERT INTO ws_requests (run_id, kind, ref, call_id, tile, title, method, path, body, ctype, brief_id, state, created_ms, expires_ms)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		r.Run, r.Kind, r.Ref, r.CallID, r.Tile, r.Title, r.Method, r.Path, r.Body, r.CType, r.Brief, orStr(r.State, "pending"), r.Created, r.Expires).Scan(&r.ID)
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
	Request int64  `json:"request,omitempty"` // the request that started it (ws_requests.id; 0: a row from before the field)
	CallID  string `json:"callId,omitempty"`  // … and the tile_agent_start call that filed that request ("": the brief's Start did)
	Status  string `json:"status"`            // starting | running | idle | ended | failed | stopped | lost
	Waiting string `json:"waiting,omitempty"`
	Waits   bool   `json:"waits"`
	Last    string `json:"last,omitempty"`
	Turn    int    `json:"turn,omitempty"`
	Told    string `json:"-"`
	// ToldTurn is the turn the conversation was last told of: a turn that
	// begins and ends between two reads leaves the state as it was told, and
	// only this says it happened (wsSeen)
	ToldTurn int    `json:"-"`
	Refused  string `json:"-"` // why xbind refused the messages held for it, until the conversation is told
	Owed     bool   `json:"-"`
	Wake     bool   `json:"-"`
	Started  int64  `json:"startedMs"`
	Updated  int64  `json:"updatedMs"`
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

const wsAgentCols = `id, run_id, session, tile, harness, mode, asks, brief_id, request_id, call_id, status, waiting, waits, last, turn, told, owed, wake, started_ms, updated_ms, refused, told_turn`

func scanWsAgent(scan func(...any) error) (*wsAgentRow, error) {
	var a wsAgentRow
	err := scan(&a.ID, &a.Run, &a.Session, &a.Tile, &a.Harness, &a.Mode, &a.Asks, &a.Brief, &a.Request, &a.CallID, &a.Status, &a.Waiting, &a.Waits, &a.Last, &a.Turn,
		&a.Told, &a.Owed, &a.Wake, &a.Started, &a.Updated, &a.Refused, &a.ToldTurn)
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
	Harness    string `json:"harness,omitempty"` // the person's pick ("": what xbind resolves)
	State      string `json:"state"`
	Session    string `json:"session,omitempty"`
	Edited     bool   `json:"edited,omitempty"`
	CallID     string `json:"callId,omitempty"` // the tile_agent_brief call that wrote it
	Created    int64  `json:"createdMs"`
	Updated    int64  `json:"updatedMs"`
}

const wsBriefCols = `id, run_id, tile, goal, done_when, leave_alone, check_by, known, state, session, edited, created_ms, updated_ms, harness, call_id`

func scanWsBrief(scan func(...any) error) (*wsBrief, error) {
	var b wsBrief
	err := scan(&b.ID, &b.Run, &b.Tile, &b.Goal, &b.DoneWhen, &b.LeaveAlone, &b.CheckBy, &b.Known, &b.State, &b.Session, &b.Edited, &b.Created, &b.Updated, &b.Harness, &b.CallID)
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

// --- refusals ----------------------------------------------------------------------

// wsRefused is one refusal a tool call met, in fields: what kind, xbind's
// sentence for it, and the tile it was about.
type wsRefused struct {
	ID      int64  `json:"id"`
	CallID  string `json:"callId,omitempty"`
	Tool    string `json:"tool,omitempty"`
	Kind    string `json:"kind"`
	Reason  string `json:"reason,omitempty"`
	Tile    string `json:"tile,omitempty"`
	Created int64  `json:"createdMs"`
}

func (d *DB) wsRefusals(runID int64) []wsRefused {
	rows, err := d.q.Query(`SELECT id, call_id, tool, kind, reason, tile, created_ms FROM ws_refusals WHERE run_id=? ORDER BY id DESC LIMIT 50`, runID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []wsRefused{}
	for rows.Next() {
		var r wsRefused
		if rows.Scan(&r.ID, &r.CallID, &r.Tool, &r.Kind, &r.Reason, &r.Tile, &r.Created) == nil {
			out = append(out, r)
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
	sort.Strings(s.Orgs)
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
