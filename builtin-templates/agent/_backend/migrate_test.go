package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// legacyDB seeds every state the pre-engine dispatcher could leave behind,
// as an existing instance's database holds it on its first boot of the new
// engine.
type legacyIDs struct {
	blockedParent, doneKid, queuedKid   int64
	queued, idleAsked, cancelled, stray int64
	dupSeq                              int64
	chainParent, depA, depB             int64
}

func seedLegacy(t *testing.T, db *DB) legacyIDs {
	t.Helper()
	x := func(q string, args ...any) int64 {
		res, err := db.sql.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	run := func(title, status string, parent int64, detached int, extra string) int64 {
		cfg, _ := json.Marshal(Config{System: "legacy", Subagents: true, Features: map[string]bool{"streaming": false}})
		id := x(`INSERT INTO runs (title, status, config, parent_id, detached, created, updated) VALUES (?, ?, ?, ?, ?, 1, 1)`,
			title, status, string(cfg), parent, detached)
		if extra != "" {
			if _, err := db.sql.Exec(`UPDATE runs SET `+extra+` WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
		}
		return id
	}
	msg := func(run int64, seq int, role, content, tcid, calls string) {
		x(`INSERT INTO messages (run_id, seq, role, content, tool_call_id, tool_calls, created) VALUES (?, ?, ?, ?, ?, ?, 1)`,
			run, seq, role, content, tcid, calls)
	}
	var ids legacyIDs
	_, _ = db.sql.Exec(`DELETE FROM settings WHERE k='engine'`)

	// A parent parked on two foreground children: one settled but not yet
	// delivered, one still queued.
	calls, _ := json.Marshal([]toolCall{tc("s1", "spawn_subagent", `{"task":"one"}`), tc("s2", "spawn_subagent", `{"task":"two"}`)})
	pend, _ := json.Marshal(legacyPending{Kind: "await", ToolCalls: []toolCall{tc("s1", "spawn_subagent", ""), tc("s2", "spawn_subagent", "")}})
	ids.blockedParent = run("blocked parent", statusBlocked, 0, 0, "pending='"+string(pend)+"'")
	msg(ids.blockedParent, 0, "user", "split it", "", "")
	msg(ids.blockedParent, 1, "assistant", "", "", string(calls))
	msg(ids.blockedParent, 2, "tool", toolRunning, "s1", "")
	msg(ids.blockedParent, 3, "tool", toolRunning, "s2", "")
	ids.doneKid = run("kid one", statusIdle, ids.blockedParent, 1, "outcome='answered', settled_at=5, result='R1'")
	ids.queuedKid = run("kid two", statusQueued, ids.blockedParent, 1, "")
	msg(ids.queuedKid, 0, "system", "legacy"+subagentContract, "", "")
	msg(ids.queuedKid, 1, "user", "task two", "", "")
	x(`INSERT INTO run_deps (run_id, dep_id, state, tool_call_id, created) VALUES (?, ?, 'satisfied', 's1', 1)`, ids.blockedParent, ids.doneKid)
	x(`INSERT INTO run_deps (run_id, dep_id, state, tool_call_id, created) VALUES (?, ?, 'pending', 's2', 1)`, ids.blockedParent, ids.queuedKid)

	ids.queued = run("queued", statusQueued, 0, 0, "")
	msg(ids.queued, 0, "user", "hello?", "", "")
	ids.idleAsked = run("idle with a question", statusIdle, 0, 0, "")
	msg(ids.idleAsked, 0, "user", "unanswered?", "", "")
	ids.cancelled = run("cancel requested", statusRunning, 0, 0, "cancel_req=5")
	ids.stray = run("non-detached child", statusRunning, ids.idleAsked, 0, "")

	ids.dupSeq = run("dup seqs", statusIdle, 0, 0, "")
	msg(ids.dupSeq, 0, "user", "a", "", "")
	msg(ids.dupSeq, 0, "assistant", "b", "", "")

	// A background chain: B waits for A (already finished, not yet handed over).
	ids.chainParent = run("chain parent", statusIdle, 0, 0, "")
	msg(ids.chainParent, 0, "user", "chain", "", "")
	msg(ids.chainParent, 1, "assistant", "started", "", "")
	ids.depA = run("step a", statusIdle, ids.chainParent, 1, "outcome='done', settled_at=5, result='RA'")
	ids.depB = run("step b", statusBlocked, ids.chainParent, 1, "")
	msg(ids.depB, 0, "system", "legacy"+subagentContract, "", "")
	msg(ids.depB, 1, "user", "task b", "", "")
	x(`INSERT INTO run_deps (run_id, dep_id, state, delivered, created) VALUES (?, ?, 'satisfied', 1, 1)`, ids.chainParent, ids.depA)
	x(`INSERT INTO run_deps (run_id, dep_id, state, created) VALUES (?, ?, 'pending', 1)`, ids.chainParent, ids.depB)
	x(`INSERT INTO run_deps (run_id, dep_id, state, created) VALUES (?, ?, 'satisfied', 1)`, ids.depB, ids.depA)
	_, _ = db.sql.Exec(`UPDATE runs SET root_id=id WHERE parent_id=0`)
	_, _ = db.sql.Exec(`UPDATE runs SET root_id=parent_id, depth=1 WHERE parent_id<>0`)
	return ids
}

func dump(db *DB) string {
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, status, pending, wake_at FROM runs ORDER BY id`,
		`SELECT id, parent_id, child_id, tool_call_id, mode, state, delivered FROM links ORDER BY id`,
		`SELECT child_id, dep_link, dep_run FROM link_deps ORDER BY child_id, dep_link`,
		`SELECT run_id, seq, role FROM messages ORDER BY run_id, seq, id`,
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

func TestLegacyDatabaseMigratesAndCompletes(t *testing.T) {
	db := newTestDB(t)
	ids := seedLegacy(t, db)
	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	first := dump(db)
	if err := db.migrate(); err != nil {
		t.Fatal(err)
	}
	if second := dump(db); second != first {
		t.Fatalf("a second migration changed things:\n--- first\n%s\n--- second\n%s", first, second)
	}

	// The shapes it must have produced.
	for id, want := range map[int64]string{
		ids.blockedParent: statusAwait, ids.queuedKid: statusRunning, ids.queued: statusRunning,
		ids.idleAsked: statusRunning, ids.cancelled: statusCanceled, ids.stray: statusCanceled,
		ids.depB: statusAwait,
	} {
		if got := statusOf(db, id); got != want {
			t.Errorf("run #%d: %s, want %s", id, got, want)
		}
	}
	if n := len(db.queryLinks(`WHERE parent_id=? AND mode='fg'`, ids.blockedParent)); n != 2 {
		t.Fatalf("%d foreground links for the parked parent, want 2", n)
	}
	var seqs []int
	msgs, _ := db.messages(ids.dupSeq, false)
	for _, m := range msgs {
		seqs = append(seqs, m.Seq)
	}
	if fmt.Sprint(seqs) != "[0 1]" {
		t.Fatalf("duplicate seqs not renumbered: %v", seqs)
	}

	// Then the engine finishes every one of them.
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("task two"), say("R2"))
	f.on(lastUser("[results of the runs"), say("B used RA"))
	f.on(lastIs("tool", "R2"), say("joined R1 and R2"))
	f.on(lastUser("hello?"), say("hi"))
	f.on(lastUser("unanswered?"), say("answered"))
	ag.eng.recover()
	waitFor(t, "the parked parent to join", func() bool { return strings.Contains(fullText(db, ids.blockedParent), "joined R1 and R2") })
	if got := fullText(db, ids.blockedParent); !strings.Contains(got, "R1") || strings.Contains(got, toolRunning) {
		t.Fatalf("parent transcript: %s", got)
	}
	waitFor(t, "the queued run", func() bool { return strings.Contains(fullText(db, ids.queued), "hi") })
	waitFor(t, "the unanswered run", func() bool { return strings.Contains(fullText(db, ids.idleAsked), "answered") })
	waitFor(t, "the chained run", func() bool { return strings.Contains(fullText(db, ids.depB), "B used RA") })
	if !strings.Contains(fullText(db, ids.depB), "RA") {
		t.Fatal("the chained run never got its dependency's result")
	}
	assertTranscriptValid(t, db, ids.blockedParent)
}
