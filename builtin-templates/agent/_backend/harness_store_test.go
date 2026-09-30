package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// An instance's database from before coding agents: its conversations (a
// coding one bound to a sandbox, its subagent, a quick ask), a stored class
// set and the global config, as the previous binary left them.
const oldHarnessRows = `
INSERT INTO settings (k, v) VALUES ('engine', '2'), ('asks_backfill', '1'), ('conv_epoch_ms', '1790000000000'),
 ('config', '{"model":"","models":{},"system":"You help.","tokenBudget":0,"maxIters":0,"toolTimeout":0,"subagents":true,"approve":false,"mcp":null,"features":null,"maxSpawn":20}'),
 ('classes', '{"classes":[{"id":"coding","name":"Coding","toolsets":["sandbox","web","files"],"mcp":[],"managers":"all","sandboxEgress":["none","internet"]},{"id":"ops","name":"Ops","toolsets":["files","sandbox"],"mcp":[],"managers":["apps/cs"],"sandboxEgress":["none"]}],"default":"coding"}');
INSERT INTO runs (id, title, status, config, created, updated, root_id, depth, owner, visibility, team_role, origin, title_src, activity_ms)
 VALUES (1, 'fix the build', 'idle', '{"system":"You help.","subagents":true,"toolset":"web","class":"coding","sandbox":{"ref":"apps/cs|sb-1","cwd":"/work","name":"api","manager":"Coding sandboxes","image":"base","tools":["git","go"],"egress":"internet","by":"alice","at":1790000000000},"attached":[{"ref":"apps/cs|sb-1","cwd":"/work","name":"api","egress":"internet"}]}',
  1790000000, 1790000100, 1, 0, 'alice', 'private', 'viewer', 'chat', 'auto', 1790000100000);
INSERT INTO runs (id, title, status, config, parent_id, created, updated, root_id, depth, detached, outcome, settled_at, owner, visibility, team_role, origin, title_src)
 VALUES (2, 'look into it', 'done', '{"system":"You help.","class":"coding","toolset":"web"}', 1, 1790000010, 1790000020, 1, 1, 1, 'answered', 1790000020, 'alice', 'private', 'viewer', 'chat', 'origin');
INSERT INTO runs (id, title, kind, status, config, created, updated, root_id, owner, visibility, team_role, origin, activity_ms)
 VALUES (3, 'quick one', 'quick', 'idle', '{"class":"internal","toolset":"private"}', 1790000200, 1790000200, 3, 'bob', 'private', 'viewer', 'chat', 1790000200000);
INSERT INTO messages (run_id, seq, role, content, created) VALUES (1, 0, 'user', 'the build is red', 1790000000),
 (1, 1, 'assistant', 'fixed it', 1790000050), (2, 0, 'user', 'look', 1790000010), (2, 1, 'assistant', 'looked', 1790000020),
 (3, 0, 'user', 'hi', 1790000200), (3, 1, 'assistant', 'hello', 1790000201);
INSERT INTO messages_fts (content, run_id, msg_id) SELECT content, run_id, id FROM messages;
INSERT INTO links (parent_id, child_id, tool_call_id, mode, state, outcome, result, created, settled)
 VALUES (1, 2, 'c1', 'bg', 'done', 'answered', 'looked', 1790000010, 1790000020);
INSERT INTO inbox (run_id, kind, body, created, delivered_at) VALUES (1, 'message', '{"text":"the build is red"}', 1790000000, 1790000001);
`

// seedOldDB writes a database as the previous binary left it: its schema
// (agentSchemaBeforeHarness) and rows, no migration of this one run.
func seedOldDB(t *testing.T, rows string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent.db")
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	for _, q := range []string{agentSchemaBeforeHarness, rows} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("seeding the old database: %v", err)
		}
	}
	var n int
	if raw.QueryRow(`SELECT count(*) FROM pragma_table_info('runs') WHERE name='engine'`).Scan(&n); n != 0 {
		t.Fatal("the old schema already has runs.engine")
	}
	return path
}

// The migration is additive: an existing instance's conversations, classes
// and config load as before (engine "", no harness, bindings without
// harnesses), the new tables work, a second boot changes nothing, and an
// old binary's insert (which names no engine) still lands.
func TestHarnessMigrationKeepsOldRows(t *testing.T) {
	path := seedOldDB(t, oldHarnessRows)
	db, err := openDB(path)
	if err != nil {
		t.Fatalf("migrating: %v", err)
	}
	for _, tbl := range []string{"harness_sessions", "harness_prefs", "harness_seen", "harness_options"} {
		var n int
		if err := db.sql.QueryRow(`SELECT count(*) FROM ` + tbl).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%s: %d %v", tbl, n, err)
		}
	}
	runs, err := db.queryRuns(`ORDER BY id`)
	if err != nil || len(runs) != 3 {
		t.Fatalf("runs: %v %v", runs, err)
	}
	for _, r := range runs {
		if r.Engine != "" {
			t.Errorf("run %d: engine %q", r.ID, r.Engine)
		}
		if s := runSummary(r); s["engine"] != "" {
			t.Errorf("run %d's summary: %v", r.ID, s["engine"])
		}
	}
	if r := runs[0]; r.Title != "fix the build" || r.Owner != "alice" || r.Visibility != visPrivate || r.RootID != 1 {
		t.Fatalf("the coding conversation: %+v", r)
	}
	if r := runs[1]; r.ParentID != 1 || r.Status != statusDone || r.Outcome != outcomeAnswered {
		t.Fatalf("its subagent: %+v", r)
	}
	cfg, err := db.runConfig(1)
	if err != nil || cfg.Class != classCoding || cfg.Engine != "" || cfg.Harness != nil || cfg.Sandbox == nil ||
		cfg.Sandbox.Ref != "apps/cs|sb-1" || cfg.Sandbox.Harnesses != nil || len(cfg.Sandbox.Tools) != 2 || len(cfg.Attached) != 1 {
		t.Fatalf("the stored config: %+v %v", cfg, err)
	}
	if msgs, _ := db.messages(1, false); len(msgs) != 2 || msgs[1].Content != "fixed it" {
		t.Fatalf("the transcript: %v", msgs)
	}
	if db.getSetting("engine") != engineVersion || db.getSetting("conv_epoch_ms") != "1790000000000" {
		t.Fatal("a one-time step ran again")
	}
	g := parseConfig(db.getSetting("config"))
	if g.System != "You help." || g.MaxSpawn != 20 || g.harnessIdle() != 15*60e9 || g.maxHarness() != 3 {
		t.Fatalf("the global config: %+v", g)
	}

	// the stored classes: the edited coding keeps what it was saved with,
	// the custom one allows no harness, and neither fails to load
	t.Cleanup(func() { classStore.Store(nil) })
	st := loadClasses(db)
	coding, _ := st.find(classCoding)
	ops, _ := st.find("ops")
	if coding.has(tsHarness) || coding.allowsHarness("claude") || ops.allowsHarness("claude") || st.def != classCoding {
		t.Fatalf("stored classes: %+v %+v", coding, ops)
	}
	if b, _ := json.Marshal(coding); !strings.Contains(string(b), `"harnesses":[]`) {
		t.Fatalf("a stored class's harnesses: %s", b)
	}

	// the new tables work on the migrated file
	if err := db.setHarnessMode("alice", "claude", hmAuto); err != nil || db.harnessMode("alice", "claude") != hmAuto {
		t.Fatalf("prefs: %v", err)
	}
	if err := db.putHarnessSession(&harnessSession{RunID: 1, RootID: 1, Ref: "apps/cs|sb-1", Provider: "claude"}); err != nil {
		t.Fatalf("a session: %v", err)
	}

	// an old binary during a blue/green overlap inserts only what it knows
	if _, err := db.sql.Exec(`INSERT INTO runs (title, status, config, parent_id, root_id, depth, detached, created, updated,
		owner, visibility, team_role, origin, origin_id, session_key, title_src, activity_ms)
		VALUES ('old binary', 'idle', '{}', 0, 0, 0, 0, 1, 1, 'bob', 'private', 'viewer', 'chat', 0, '', '', 1)`); err != nil {
		t.Fatalf("an old binary's insert: %v", err)
	}
	if r, err := db.getRun(4); err != nil || r.Engine != "" {
		t.Fatalf("the old binary's run: %+v %v", r, err)
	}
	db.sql.Close()

	// a second boot: nothing changes, nothing is lost
	db2, err := openDB(path)
	if err != nil {
		t.Fatalf("the second boot: %v", err)
	}
	defer db2.sql.Close()
	if s, _ := db2.harnessSession(1); s == nil || s.Provider != "claude" || db2.harnessMode("alice", "claude") != hmAuto {
		t.Fatalf("after a second boot: %+v", s)
	}
	if runs, _ := db2.queryRuns(`ORDER BY id`); len(runs) != 4 {
		t.Fatalf("runs after a second boot: %d", len(runs))
	}
}

// An old conversation serves as it did through the real routes — its view
// and run JSON carry engine "" and no harness.
func TestHarnessMigrationServesOldConversations(t *testing.T) {
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	db, err := openDB(seedOldDB(t, oldHarnessRows))
	if err != nil {
		t.Fatal(err)
	}
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	t.Cleanup(func() { classStore.Store(nil) })
	mux := http.NewServeMux()
	routes(mux)
	w := callAs(t, mux, asAlice, "GET", "/runs/1/view", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixed it") {
		t.Fatalf("the view: %d %s", w.Code, w.Body)
	}
	var v struct {
		Run map[string]any `json:"run"`
	}
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Run["engine"] != "" || v.Run["harness"] != nil {
		t.Fatalf("the view's run: %v", v.Run)
	}
	w = callAs(t, mux, asAlice, "GET", "/runs/1", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"engine":""`) || strings.Contains(w.Body.String(), `"harness":{`) {
		t.Fatalf("the run: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asBob, "GET", "/runs/1/view", nil); w.Code != 404 {
		t.Fatalf("bob sees alice's private conversation: %d", w.Code)
	}
}

// A session row round-trips whole; the settings default to approve; what a
// probe learns keeps what a session learned, and the other way round.
func TestHarnessStore(t *testing.T) {
	db := newTestDB(t)
	root, _ := db.createRunStamped("t", "{}", 0, statusIdle, runStamp{Owner: "alice", Engine: engineHarness})
	kid, _ := db.createRunStamped("k", "{}", root, statusIdle, runStamp{})
	hkid, _ := db.createRunStamped("h", "{}", root, statusIdle, runStamp{Engine: engineHarness})
	for id, want := range map[int64]string{root: engineHarness, kid: "", hkid: engineHarness} {
		if r, _ := db.getRun(id); r.Engine != want {
			t.Errorf("run %d: engine %q, want %q", id, r.Engine, want)
		}
	}
	if r, _ := db.getRun(hkid); r.Owner != "alice" || r.RootID != root {
		t.Fatalf("a harness child carries its root's stamp: %+v", r)
	}

	if s, err := db.harnessSession(root); s != nil || err != nil {
		t.Fatalf("no session yet: %+v %v", s, err)
	}
	in := &harnessSession{RunID: hkid, RootID: root, Ref: "apps/cs|sb-1", Cwd: "/work", Provider: "claude",
		Argv: []string{"claude-agent-acp"}, ExecID: "e1", ClientID: "agent:3:h:1", Gen: 2, State: hsLive, ACPSession: "s-1",
		Loadable: true, Steering: true, ReadOff: 1234, ErrOff: 56, PromptRPC: `"h3.2-7"`, PromptState: "sent", Turn: 4,
		Snapshot: `{"sessionId":"s-1"}`, Rules: `[]`, Plan: `{"entries":[]}`, Usage: `{"used":1}`, Counts: `{"tools":1}`,
		Login: `{}`, Queue: `[]`, Held: `{"text":"x"}`, Error: "", LastActiveMs: 99}
	if err := db.putHarnessSession(in); err != nil {
		t.Fatal(err)
	}
	out, err := db.harnessSession(hkid)
	if err != nil || out == nil {
		t.Fatal(err)
	}
	if a, b := jsonOf(in), jsonOf(out); a != b {
		t.Fatalf("round trip\n in %s\nout %s", a, b)
	}
	in.State, in.ReadOff, in.Gen = hsStopped, 2000, 3
	created := out.CreatedMs
	if err := db.putHarnessSession(in); err != nil {
		t.Fatal(err)
	}
	if out, _ = db.harnessSession(hkid); out.State != hsStopped || out.ReadOff != 2000 || out.Gen != 3 || out.CreatedMs != created {
		t.Fatalf("an update: %+v", out)
	}
	if err := db.deleteRun(root); err != nil {
		t.Fatal(err)
	}
	if s, _ := db.harnessSession(hkid); s != nil {
		t.Fatal("a deleted run's session stays")
	}

	if db.harnessMode("alice", "codex") != hmApprove || len(db.harnessModes("alice")) != 0 {
		t.Fatal("unset is approve, and not listed")
	}
	_ = db.setHarnessMode("alice", "codex", hmAuto)
	_ = db.setHarnessMode("alice", "codex", hmApprove)
	_ = db.setHarnessMode("bob", "claude", hmAuto)
	if m := db.harnessModes("alice"); len(m) != 1 || m["codex"] != hmApprove {
		t.Fatalf("alice's: %v", m)
	}

	yes, no := true, false
	_ = db.noteHarnessSeen("apps/cs|sb-1", "claude", nil, &no)  // a session: signed out
	_ = db.noteHarnessSeen("apps/cs|sb-1", "claude", &yes, nil) // a probe: installed
	_ = db.noteHarnessSeen("apps/cs|sb-2", "claude", &no, nil)
	seen := db.seenHarnesses("claude")
	if s := seen["apps/cs|sb-1"]; s.Installed == nil || !*s.Installed || s.SignedIn == nil || *s.SignedIn || s.At == 0 {
		t.Fatalf("sb-1: %s", jsonOf(s))
	}
	if b, _ := json.Marshal(seen["apps/cs|sb-2"]); strings.Contains(string(b), "signedIn") {
		t.Fatalf("unknown is left out: %s", b)
	}
	if !db.anyHarnessSeen() || len(db.seenHarnesses("codex")) != 0 {
		t.Fatal("seen")
	}

	if db.harnessOptions("claude") != nil {
		t.Fatal("no options before any session")
	}
	_ = db.setHarnessOptions("claude", json.RawMessage(`[{"id":"model","currentValue":"opus"}]`))
	if string(db.harnessOptions("claude")) != `[{"id":"model","currentValue":"opus"}]` {
		t.Fatalf("options: %s", db.harnessOptions("claude"))
	}
}

// The tile-wide knobs: harnessIdleMin (nil = 15, 0 = never) and maxHarness
// (0 = 3); PUT /config never keeps a conversation's engine or harness.
func TestHarnessConfigKnobs(t *testing.T) {
	zero, five := 0, 5
	for _, c := range []struct {
		cfg  Config
		idle string
		max  int
	}{
		{Config{}, "15m0s", 3},
		{Config{HarnessIdleMin: &zero, MaxHarness: 1}, "0s", 1},
		{Config{HarnessIdleMin: &five, MaxHarness: 99}, "5m0s", 16},
	} {
		if c.cfg.harnessIdle().String() != c.idle || c.cfg.maxHarness() != c.max {
			t.Errorf("%+v: %v %d", c.cfg, c.cfg.harnessIdle(), c.cfg.maxHarness())
		}
	}
	_, mux := accessFixture(t)
	w := callAs(t, mux, asMgr, "PUT", "/config", map[string]any{"harnessIdleMin": 0, "maxHarness": 2, "engine": "harness",
		"harness": map[string]string{"provider": "claude", "ref": "apps/cs|x"}})
	if w.Code != 200 {
		t.Fatalf("PUT /config: %d %s", w.Code, w.Body)
	}
	g := parseConfig(agent.db.getSetting("config"))
	if g.Engine != "" || g.Harness != nil || g.HarnessIdleMin == nil || *g.HarnessIdleMin != 0 || g.maxHarness() != 2 {
		t.Fatalf("the global config: %s", jsonOf(g))
	}
}

func jsonOf(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// A binary from before coding agents, rolled back to, ends at once any turn
// it starts on a coding agent's run: its turn() checks turn_steps against
// maxTurnSteps (whose ceiling is harnessTurnCap) before anything else, and
// the triggers keep a harness run's turn_steps there — from its creation,
// through a draft becoming one, through such a binary's own turn start
// (v0.3.64's startTurn zeroes turn_steps with its status) and its
// recover()'s running runs. A built-in run is untouched.
func TestHarnessTurnCap(t *testing.T) {
	db, err := openDB(seedOldDB(t, oldHarnessRows))
	if err != nil {
		t.Fatal(err)
	}
	defer db.sql.Close()
	steps := func(id int64) int {
		var n int
		if err := db.sql.QueryRow(`SELECT turn_steps FROM runs WHERE id=?`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	h, err := db.createRunStamped("coding", `{"engine":"harness"}`, 0, statusRunning, runStamp{Owner: "alice", Engine: engineHarness})
	if err != nil {
		t.Fatal(err)
	}
	if steps(h) != harnessTurnCap {
		t.Fatalf("a new coding agent's run: turn_steps %d", steps(h))
	}
	// v0.3.64's startTurn, verbatim
	oldStart := `UPDATE runs SET status=?, wake_at=0, pending='', turn_steps=0, turn_started=?,
			settled_at=0, outcome='', cancel_req=0, updated=? WHERE id=?`
	for _, id := range []int64{h, 1} {
		if _, err := db.sql.Exec(oldStart, statusRunning, 1, 1, id); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []Config{{}, {MaxTurnSteps: 100000}, {MaxIters: 100000}} {
		if steps(h) < c.maxTurnSteps() {
			t.Fatalf("an older binary's turn start: turn_steps %d, its cap %d", steps(h), c.maxTurnSteps())
		}
	}
	if steps(1) != 0 {
		t.Fatalf("a built-in run: turn_steps %d", steps(1))
	}
	// a draft that becomes a coding agent's (releaseDraft stamps engine)
	if _, err := db.sql.Exec(`UPDATE runs SET engine=? WHERE id=3`, engineHarness); err != nil || steps(3) != harnessTurnCap {
		t.Fatalf("a draft made a coding agent's: %d %v", steps(3), err)
	}
	if _, err := db.sql.Exec(`UPDATE runs SET turn_steps=turn_steps+1 WHERE id=?`, h); err != nil || steps(h) != harnessTurnCap+1 {
		t.Fatalf("an update above the cap stays: %d %v", steps(h), err)
	}
}
