package main

import (
	"fmt"
	"strings"
	"testing"
)

func dumpConv(db *DB) string {
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, owner, visibility, team_role, origin, origin_id, session_key, title_src, activity_ms FROM runs ORDER BY id`,
		`SELECT key, origin, origin_id, run_id FROM sessions ORDER BY key`,
		`SELECT id, last_run_id FROM schedules ORDER BY id`,
	} {
		rows, err := db.sql.Query(q)
		if err != nil {
			return err.Error()
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			fmt.Fprintln(&b, vals...)
		}
		rows.Close()
	}
	return b.String()
}

// Runs written before D83 — or by an old binary during a blue/green overlap,
// which inserts only the columns it knows — are classified by where they came
// from, stay visible to everyone (as they always were), and a second pass
// changes nothing.
func TestLegacyRunsAreClassified(t *testing.T) {
	db := newTestDB(t)
	x := func(q string, args ...any) int64 {
		res, err := db.sql.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	run := func(title, kind string, parent int64) int64 {
		id := x(`INSERT INTO runs (title, kind, status, config, parent_id, created, updated) VALUES (?, ?, 'idle', '{}', ?, 100, 100)`, title, kind, parent)
		if parent == 0 {
			x(`UPDATE runs SET root_id=id WHERE id=?`, id)
		} else {
			x(`UPDATE runs SET root_id=?, depth=1 WHERE id=?`, parent, id)
		}
		return id
	}
	msg := func(run int64, seq int, role, content string, created int64) {
		x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, ?, ?, ?, ?)`, run, seq, role, content, created)
	}
	quick := run("a quick question", "quick", 0)
	msg(quick, 0, "user", "q", 200)
	msg(quick, 1, "assistant", "a", 250)
	task := run("plan the quarter", "", 0)
	kid := run("research", "", task)
	sched := x(`INSERT INTO schedules (name, cron, goal, created) VALUES ('digest', '@daily', 'digest it', 1)`)
	fired := run("⏱ digest", "", 0)
	x(`INSERT INTO steps (run_id, seq, kind, detail, created) VALUES (?, 0, 'note', ?, 100)`, fired,
		fmt.Sprintf(`{"text":"started by schedule #%d (digest)"}`, sched))
	orphanFired := run("⏱ gone", "", 0)
	wrun := run("👁 invoices", "", 0)
	watch := x(`INSERT INTO schedules (name, cron, goal, watcher, run_id, created) VALUES ('invoices', '@hourly', 'watch', 1, ?, 1)`, wrun)
	// what those columns hold as an old binary leaves them
	x(`UPDATE runs SET owner='', visibility='team', team_role='participant', origin='', origin_id=0, session_key='', title_src='', activity_ms=0`)
	x(`UPDATE schedules SET last_run_id=0`)
	x(`DELETE FROM sessions`)

	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	first := dumpConv(db)
	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	if second := dumpConv(db); second != first {
		t.Fatalf("a second migration changed things:\n--- first\n%s\n--- second\n%s", first, second)
	}

	get := func(id int64) *Run {
		r, err := db.getRun(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	for id, want := range map[int64]string{quick: "chat", task: "chat", kid: "chat", fired: "schedule", orphanFired: "schedule", wrun: "watcher"} {
		r := get(id)
		if r.Origin != want {
			t.Errorf("run #%d %q: origin %q, want %q", id, r.Title, r.Origin, want)
		}
		if r.Owner != "" || r.Visibility != visTeam || r.TeamRole != roleParticipant {
			t.Errorf("run #%d: legacy runs stay team-visible (owner %q, %s/%s)", id, r.Owner, r.Visibility, r.TeamRole)
		}
	}
	if r := get(fired); r.OriginID != sched || r.SessionKey != fmt.Sprintf("sched:%d", sched) {
		t.Errorf("fired run: origin id %d key %q", r.OriginID, r.SessionKey)
	}
	if r := get(wrun); r.OriginID != watch || r.SessionKey != fmt.Sprintf("watch:%d", watch) {
		t.Errorf("watcher run: origin id %d key %q", r.OriginID, r.SessionKey)
	}
	if r := get(quick); r.ActivityMs != 250_000 || r.TitleSrc != "" {
		t.Errorf("quick ask: activity %d (want the last message, 250s) title_src %q (legacy titles are never auto-titled)", r.ActivityMs, r.TitleSrc)
	}
	if r := get(task); r.ActivityMs != 100_000 {
		t.Errorf("a run with no messages is as old as its creation: %d", r.ActivityMs)
	}
	if get(kid).ActivityMs != 0 {
		t.Error("only roots carry activity")
	}
	var sessRun, lastRun int64
	_ = db.sql.QueryRow(`SELECT run_id FROM sessions WHERE key=?`, fmt.Sprintf("watch:%d", watch)).Scan(&sessRun)
	_ = db.sql.QueryRow(`SELECT last_run_id FROM schedules WHERE id=?`, sched).Scan(&lastRun)
	if sessRun != wrun || lastRun != fired {
		t.Errorf("watcher session → %d (want %d); schedule's last run %d (want %d)", sessRun, wrun, lastRun, fired)
	}
	if db.getSetting("conv_epoch_ms") == "" {
		t.Error("the unread floor is recorded")
	}
}

// A run a person starts is theirs and private; one the API or a legacy caller
// starts keeps the old team-visible shape; a subagent carries its root's.
func TestNewRunsAreStamped(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	alice := who{kind: whoUser, user: "alice", level: "read"}
	r, err := ag.startRunOpts(runOpts{Title: "hi", Cfg: defaultConfig(), Text: "hello", Stamp: alice.stamp("chat"), Sender: "alice", Hold: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Owner != "alice" || r.Visibility != visPrivate || r.Origin != "chat" || r.ActivityMs == 0 {
		t.Fatalf("a person's run: %+v", r)
	}
	legacy, _ := ag.startRun("old", "", defaultConfig(), "x", true, "")
	if legacy.Owner != "" || legacy.Visibility != visTeam || legacy.TeamRole != roleParticipant {
		t.Fatalf("the legacy entry point keeps the team shape: %+v", legacy)
	}
	kidID, err := db.createRun("kid", "{}", r.ID)
	if err != nil {
		t.Fatal(err)
	}
	kid, _ := db.getRun(kidID)
	if kid.Owner != "alice" || kid.Visibility != visPrivate || kid.Origin != "chat" || kid.ActivityMs != 0 {
		t.Fatalf("a subagent carries its root's stamp: %+v", kid)
	}
}
