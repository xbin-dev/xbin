// harness_store.go — what the agent keeps about coding agents (harnesses:
// Claude Code, Codex, Gemini CLI, OpenCode over ACP, run in a coding
// sandbox; API.md §Coding agents): runs.engine, a harness run's
// session, each person's Auto / Always approve setting, what the agent last
// learned about a sandbox's harnesses, and the config options each harness
// last reported.
//
// All of it is additive: an older binary ignores the column and the tables,
// a run from before has engine "" (the agent's own loop) and no session row.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const harnessSchemaSQL = `
CREATE TABLE IF NOT EXISTS harness_sessions (
  run_id INTEGER PRIMARY KEY,
  root_id INTEGER NOT NULL DEFAULT 0,
  ref TEXT NOT NULL DEFAULT '',
  cwd TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL DEFAULT '',
  argv TEXT NOT NULL DEFAULT '',
  exec_id TEXT NOT NULL DEFAULT '',
  client_id TEXT NOT NULL DEFAULT '',
  gen INTEGER NOT NULL DEFAULT 0,
  state TEXT NOT NULL DEFAULT 'none',
  acp_session TEXT NOT NULL DEFAULT '',
  loadable INTEGER NOT NULL DEFAULT 0,
  steering INTEGER NOT NULL DEFAULT 0,
  read_off INTEGER NOT NULL DEFAULT 0,
  err_off INTEGER NOT NULL DEFAULT 0,
  prompt_rpc TEXT NOT NULL DEFAULT '',
  prompt_state TEXT NOT NULL DEFAULT '',
  turn INTEGER NOT NULL DEFAULT 0,
  snapshot TEXT NOT NULL DEFAULT '',
  rules TEXT NOT NULL DEFAULT '',
  plan TEXT NOT NULL DEFAULT '',
  usage TEXT NOT NULL DEFAULT '',
  counts TEXT NOT NULL DEFAULT '',
  login TEXT NOT NULL DEFAULT '',
  queue TEXT NOT NULL DEFAULT '',
  held TEXT NOT NULL DEFAULT '',
  error TEXT NOT NULL DEFAULT '',
  last_active_ms INTEGER NOT NULL DEFAULT 0,
  created_ms INTEGER NOT NULL DEFAULT 0,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  title TEXT NOT NULL DEFAULT '',
  shared INTEGER NOT NULL DEFAULT 0,
  draft TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  started_ms INTEGER NOT NULL DEFAULT 0,
  start_mode TEXT NOT NULL DEFAULT '',
  turn_seq INTEGER NOT NULL DEFAULT 0,
  answers TEXT NOT NULL DEFAULT '',
  steer_row INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_harness_sessions_root ON harness_sessions(root_id);
CREATE INDEX IF NOT EXISTS idx_harness_sessions_state ON harness_sessions(state);
CREATE TABLE IF NOT EXISTS harness_prefs (
  user TEXT NOT NULL,
  provider TEXT NOT NULL,
  mode TEXT NOT NULL,
  updated_ms INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (user, provider)
);
CREATE TABLE IF NOT EXISTS harness_seen (
  ref TEXT NOT NULL,
  provider TEXT NOT NULL,
  installed INTEGER NOT NULL DEFAULT -1,
  signed_in INTEGER NOT NULL DEFAULT -1,
  at INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (ref, provider)
);
CREATE TABLE IF NOT EXISTS harness_options (
  provider TEXT PRIMARY KEY,
  options TEXT NOT NULL DEFAULT '',
  at INTEGER NOT NULL DEFAULT 0
);
`

// addHarnessSchema adds runs.engine and the harness tables. It runs before
// any migration step that reads runs through runCols (which names engine).
func (d *DB) addHarnessSchema() error {
	_, _ = d.q.Exec(`ALTER TABLE runs ADD COLUMN engine TEXT NOT NULL DEFAULT ''`) // fails harmlessly when there
	if _, err := d.q.Exec(harnessSchemaSQL); err != nil {
		return err
	}
	// added by the engine (A8) to a table an earlier build of this program
	// may have made without them; each fails harmlessly when there
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN title TEXT NOT NULL DEFAULT ''`)
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN shared INTEGER NOT NULL DEFAULT 0`)
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN draft TEXT NOT NULL DEFAULT ''`)
	// the routes and views (A9b): the harness's name as its manager
	// advertises it, when the current generation started
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN name TEXT NOT NULL DEFAULT ''`)
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN started_ms INTEGER NOT NULL DEFAULT 0`)
	// the fixes after the live check: the mode the adapter opened its first
	// session in by itself, where the current turn's rows begin
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN start_mode TEXT NOT NULL DEFAULT ''`)
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN turn_seq INTEGER NOT NULL DEFAULT 0`)
	// the engine's ordering fixes: the answers on their way to the adapter,
	// the message whose steer is on its way
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN answers TEXT NOT NULL DEFAULT ''`)
	_, _ = d.q.Exec(`ALTER TABLE harness_sessions ADD COLUMN steer_row INTEGER NOT NULL DEFAULT 0`)
	_, err := d.q.Exec(harnessTurnCapSQL)
	return err
}

// harnessTurnCap is what runs.turn_steps says of a coding agent's run: the
// most model calls a turn may take (maxTurnSteps' ceiling). This binary
// never reads it for one (the harness engine takes no model steps); it is
// for a binary from before coding agents, rolled back to: its pass would
// drive a harness run it finds running, parked and answered, or messaged
// with the built-in model loop over the acp:* transcript — and its turn()
// ends a turn at the step cap before anything else (no model call, no
// compaction). The triggers keep a harness run at the cap through any
// update, that binary's own turn start (turn_steps=0) included.
const harnessTurnCap = 500

var harnessTurnCapSQL = strings.ReplaceAll(`
CREATE TRIGGER IF NOT EXISTS harness_turn_cap_ins AFTER INSERT ON runs
  WHEN NEW.engine = 'harness' AND NEW.turn_steps < CAP
  BEGIN UPDATE runs SET turn_steps = CAP WHERE id = NEW.id; END;
CREATE TRIGGER IF NOT EXISTS harness_turn_cap_upd AFTER UPDATE OF status, engine, turn_steps ON runs
  WHEN NEW.engine = 'harness' AND NEW.turn_steps < CAP
  BEGIN UPDATE runs SET turn_steps = CAP WHERE id = NEW.id; END;
`, "CAP", strconv.Itoa(harnessTurnCap))

// --- the run's config ---------------------------------------------------------------

// HarnessConfig is a harness run's coding agent (Config.Harness): which
// one, in which sandbox, how it (re)starts.
type HarnessConfig struct {
	Provider string            `json:"provider"`          // a catalog id (GET /harnesses)
	Mode     string            `json:"mode,omitempty"`    // the provider mode to (re)start in
	Options  map[string]string `json:"options,omitempty"` // config options applied after session/new|load
	Ref      string            `json:"ref"`               // the sandbox, fixed for the conversation
	Cwd      string            `json:"cwd,omitempty"`
	By       string            `json:"by,omitempty"` // who started it: a user id, "el:<component>", ""
}

const engineHarness = "harness"

// harnessIdle is how long a coding agent with no turn is kept (0 = until
// its conversation stops it).
func (c Config) harnessIdle() time.Duration {
	switch {
	case c.HarnessIdleMin == nil:
		return 15 * time.Minute
	case *c.HarnessIdleMin <= 0:
		return 0
	case *c.HarnessIdleMin > 7*24*60:
		return 7 * 24 * time.Hour
	}
	return time.Duration(*c.HarnessIdleMin) * time.Minute
}

// maxHarness is how many coding agents may run at once in one tree.
func (c Config) maxHarness() int { return clampCfg(c.MaxHarness, 3, 16) }

// --- sessions -----------------------------------------------------------------------

// Storage states of a harness session (the API's harness.state maps them).
const (
	hsNone     = "none"
	hsStarting = "starting"
	hsLive     = "live"
	hsStopped  = "stopped"
	hsLost     = "lost"
	hsFailed   = "failed"
	hsLogin    = "login"
)

// harnessSession is a harness run's row: the adapter process in the sandbox
// and what the engine needs to reattach to it after a handoff. The JSON
// columns (Snapshot: an acp.SessionState; Rules, Plan, Usage, Counts, Login,
// Queue, Held, Draft) are kept as the engine wrote them. Title is the
// adapter's own session title; Shared, that others may use the sandbox (the
// privacy note); Draft, the text and thinking read but not yet flushed to a
// row as of ReadOff (harness_engine.go). Name is the harness's name for
// people as the sandbox's manager advertised it at the last spawn (a harness
// the sdk catalog doesn't know has only its manager's title); StartedMs,
// when the current generation was spawned. StartMode is the mode the
// adapter opened its first session in with no mode asked of it (a harness
// the catalog doesn't know: the one mode anyone may switch it back to —
// harnessModeOpen); TurnSeq, the seq of the current (or last) turn's first
// row — its answer is the text after it (endHarnessTurnTx). Answers are
// the answers to the adapter's requests on their way to it (a JSON list of
// hAnswer: harness_park.go), SteerRow the inbox row whose steer is
// (harness_steer.go) — each recorded before it goes, so a successor never
// loses the one nor sends the other twice.
type harnessSession struct {
	RunID, RootID      int64
	Ref, Cwd, Provider string
	Argv               []string
	ExecID, ClientID   string
	Gen                int
	State              string
	ACPSession         string
	Loadable, Steering bool
	ReadOff, ErrOff    int64
	PromptRPC          string
	PromptState        string // "" | "sending" | "sent"
	Turn               int64
	Snapshot, Rules    string
	Plan, Usage        string
	Counts, Login      string
	Queue, Held        string
	Error              string
	LastActiveMs       int64
	CreatedMs          int64
	UpdatedMs          int64
	Title              string
	Shared             bool
	Draft              string
	Name               string
	StartedMs          int64
	StartMode          string
	TurnSeq            int64
	Answers            string
	SteerRow           int64
}

const harnessSessionCols = `run_id, root_id, ref, cwd, provider, argv, exec_id, client_id, gen, state, acp_session, loadable,
  steering, read_off, err_off, prompt_rpc, prompt_state, turn, snapshot, rules, plan, usage, counts, login, queue, held,
  error, last_active_ms, created_ms, updated_ms, title, shared, draft, name, started_ms, start_mode, turn_seq, answers, steer_row`

// harnessSession is run's session row (nil: it has none).
func (d *DB) harnessSession(run int64) (*harnessSession, error) {
	s := &harnessSession{}
	var argv string
	var loadable, steering, shared int
	err := d.q.QueryRow(`SELECT `+harnessSessionCols+` FROM harness_sessions WHERE run_id=?`, run).Scan(
		&s.RunID, &s.RootID, &s.Ref, &s.Cwd, &s.Provider, &argv, &s.ExecID, &s.ClientID, &s.Gen, &s.State, &s.ACPSession,
		&loadable, &steering, &s.ReadOff, &s.ErrOff, &s.PromptRPC, &s.PromptState, &s.Turn, &s.Snapshot, &s.Rules,
		&s.Plan, &s.Usage, &s.Counts, &s.Login, &s.Queue, &s.Held, &s.Error, &s.LastActiveMs, &s.CreatedMs, &s.UpdatedMs,
		&s.Title, &shared, &s.Draft, &s.Name, &s.StartedMs, &s.StartMode, &s.TurnSeq, &s.Answers, &s.SteerRow)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Loadable, s.Steering, s.Shared = loadable != 0, steering != 0, shared != 0
	if argv != "" {
		_ = json.Unmarshal([]byte(argv), &s.Argv)
	}
	return s, nil
}

// putHarnessSession writes s whole (inserting it the first time); it stamps
// UpdatedMs, and CreatedMs on a new row.
func (d *DB) putHarnessSession(s *harnessSession) error {
	ms := nowMs()
	if s.CreatedMs == 0 {
		s.CreatedMs = ms
	}
	s.UpdatedMs = ms
	if s.State == "" {
		s.State = hsNone
	}
	argv := ""
	if s.Argv != nil {
		b, _ := json.Marshal(s.Argv)
		argv = string(b)
	}
	_, err := d.q.Exec(`INSERT INTO harness_sessions (`+harnessSessionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(run_id) DO UPDATE SET root_id=excluded.root_id, ref=excluded.ref, cwd=excluded.cwd,
		  provider=excluded.provider, argv=excluded.argv, exec_id=excluded.exec_id, client_id=excluded.client_id,
		  gen=excluded.gen, state=excluded.state, acp_session=excluded.acp_session, loadable=excluded.loadable,
		  steering=excluded.steering, read_off=excluded.read_off, err_off=excluded.err_off,
		  prompt_rpc=excluded.prompt_rpc, prompt_state=excluded.prompt_state, turn=excluded.turn,
		  snapshot=excluded.snapshot, rules=excluded.rules, plan=excluded.plan, usage=excluded.usage,
		  counts=excluded.counts, login=excluded.login, queue=excluded.queue, held=excluded.held,
		  error=excluded.error, last_active_ms=excluded.last_active_ms, updated_ms=excluded.updated_ms,
		  title=excluded.title, shared=excluded.shared, draft=excluded.draft, name=excluded.name,
		  started_ms=excluded.started_ms, start_mode=excluded.start_mode, turn_seq=excluded.turn_seq,
		  answers=excluded.answers, steer_row=excluded.steer_row`,
		s.RunID, s.RootID, s.Ref, s.Cwd, s.Provider, argv, s.ExecID, s.ClientID, s.Gen, s.State, s.ACPSession,
		b2i(s.Loadable), b2i(s.Steering), s.ReadOff, s.ErrOff, s.PromptRPC, s.PromptState, s.Turn, s.Snapshot, s.Rules,
		s.Plan, s.Usage, s.Counts, s.Login, s.Queue, s.Held, s.Error, s.LastActiveMs, s.CreatedMs, s.UpdatedMs,
		s.Title, b2i(s.Shared), s.Draft, s.Name, s.StartedMs, s.StartMode, s.TurnSeq, s.Answers, s.SteerRow)
	return err
}

// --- a person's setting (§4.3.12) ------------------------------------------------------

// The per-person setting for a harness: "auto" (the provider's auto-edit
// mode) or "approve" (ask before acting — also what unset means).
const (
	hmAuto    = "auto"
	hmApprove = "approve"
)

// harnessModes is user's settings by provider (set ones only).
func (d *DB) harnessModes(user string) map[string]string {
	out := map[string]string{}
	rows, err := d.q.Query(`SELECT provider, mode FROM harness_prefs WHERE user=? ORDER BY provider`, user)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p, m string
		if rows.Scan(&p, &m) == nil {
			out[p] = m
		}
	}
	return out
}

// harnessMode is user's setting for provider (unset: approve).
func (d *DB) harnessMode(user, provider string) string {
	var m string
	if d.q.QueryRow(`SELECT mode FROM harness_prefs WHERE user=? AND provider=?`, user, provider).Scan(&m) != nil || m == "" {
		return hmApprove
	}
	return m
}

func (d *DB) setHarnessMode(user, provider, mode string) error {
	_, err := d.q.Exec(`INSERT INTO harness_prefs (user, provider, mode, updated_ms) VALUES (?, ?, ?, ?)
		ON CONFLICT(user, provider) DO UPDATE SET mode=excluded.mode, updated_ms=excluded.updated_ms`,
		user, provider, mode, nowMs())
	return err
}

// --- what the agent learned about a sandbox -----------------------------------------------

// harnessSeen is what a probe or a session last learned about one harness in
// one sandbox. Installed and SignedIn are nil while unknown (a probe learns
// only whether it is installed; a session whether it is signed in).
type harnessSeen struct {
	Installed *bool `json:"installed,omitempty"`
	SignedIn  *bool `json:"signedIn,omitempty"`
	At        int64 `json:"at"`
}

func triState(v int) *bool {
	if v < 0 {
		return nil
	}
	b := v != 0
	return &b
}

func triInt(b *bool) int {
	if b == nil {
		return -1
	}
	return b2i(*b)
}

// seenHarnesses is everything learned about provider's harness: by sandbox
// ref.
func (d *DB) seenHarnesses(provider string) map[string]harnessSeen {
	out := map[string]harnessSeen{}
	rows, err := d.q.Query(`SELECT ref, installed, signed_in, at FROM harness_seen WHERE provider=?`, provider)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var ref string
		var inst, signed int
		var at int64
		if rows.Scan(&ref, &inst, &signed, &at) == nil {
			out[ref] = harnessSeen{Installed: triState(inst), SignedIn: triState(signed), At: at}
		}
	}
	return out
}

// anyHarnessSeen: anything was learned about any sandbox yet.
func (d *DB) anyHarnessSeen() bool {
	var n int
	return d.q.QueryRow(`SELECT 1 FROM harness_seen LIMIT 1`).Scan(&n) == nil
}

// noteHarnessSeen records what was learned about provider in ref: a nil
// field keeps what was known before.
func (d *DB) noteHarnessSeen(ref, provider string, installed, signedIn *bool) error {
	_, err := d.q.Exec(`INSERT INTO harness_seen (ref, provider, installed, signed_in, at) VALUES (?1, ?2, ?3, ?4, ?5)
		ON CONFLICT(ref, provider) DO UPDATE SET
		  installed=CASE WHEN ?3 < 0 THEN installed ELSE ?3 END,
		  signed_in=CASE WHEN ?4 < 0 THEN signed_in ELSE ?4 END, at=?5`,
		ref, provider, triInt(installed), triInt(signedIn), nowMs())
	return err
}

// --- the options a harness reported ---------------------------------------------------------

// harnessOptions is the config options provider's last session reported (nil:
// none yet) — the home picker's model list.
func (d *DB) harnessOptions(provider string) json.RawMessage {
	var raw string
	if d.q.QueryRow(`SELECT options FROM harness_options WHERE provider=?`, provider).Scan(&raw) != nil || raw == "" {
		return nil
	}
	if !json.Valid([]byte(raw)) {
		return nil
	}
	return json.RawMessage(raw)
}

func (d *DB) setHarnessOptions(provider string, options json.RawMessage) error {
	_, err := d.q.Exec(`INSERT INTO harness_options (provider, options, at) VALUES (?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET options=excluded.options, at=excluded.at`, provider, string(options), nowMs())
	return err
}
