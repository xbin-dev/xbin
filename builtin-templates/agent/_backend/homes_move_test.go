package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// moveAgent is the global instance with the tables a partitioned start makes.
func moveAgent(t *testing.T) (*Agent, http.Handler) {
	t.Helper()
	ag, h := globalAgent(t)
	for _, f := range []func() error{ag.db.addHandoffSchema, ag.db.addMoveSchema} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	return ag, h
}

func moveRow(ag *Agent, root int64) (state string, to int64) {
	_ = ag.db.q.QueryRow(`SELECT state, to_id FROM conv_moves WHERE root=?`, root).Scan(&state, &to)
	return
}

// TestUnshareMovesAtGlobal (90 §I10): at the global instance a person's
// conversation that stops being shared — its last member removed, or made
// private with nobody in it — starts moving to its owner's own partition:
// a conv/move mail to them, its links revoked, every change refused while
// it moves (reads go on). Only the owner drives it: the export (refused while
// it works), done (it goes from here, its event and tombstone saying where),
// idempotently. A conversation still shared, or not a person's, stays.
func TestUnshareMovesAtGlobal(t *testing.T) {
	ag, h := moveAgent(t)
	mail := stubMail(t)
	var x Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"plan it","share":{"members":[{"user":"bob"}]}}`, f5("alice", "read")), 200, &x)
	waitStatus(t, ag.db, x.ID, statusIdle)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/links", x.ID), `{"role":"viewer"}`, f5("alice", "read")), 200, nil)
	// made private while bob is in it: shared still — it stays
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x.ID), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
	if st, _ := moveRow(ag, x.ID); st != "" || mail.count() != 0 {
		t.Fatalf("a conversation bob is in started moving: %q, %d mails", st, mail.count())
	}
	alice := followAs(t, h, "/stream", f5("alice", "read"))
	// bob leaves — the last one: it moves to alice's own space
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/runs/%d/members/bob", x.ID), "", f5("bob", "read")), 200, nil)
	if st, _ := moveRow(ag, x.ID); st != "asked" {
		t.Fatalf("un-shared, not moving: %q", st)
	}
	sent := mail.wait(t, 1)
	if sent[0].to != "user:alice" || sent[0].topic != topicMove || string(sent[0].data) != fmt.Sprintf(`{"run":%d}`, x.ID) {
		t.Fatalf("the move's mail: %+v %s", sent[0], sent[0].data)
	}
	var revoked int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM share_links WHERE run_id=? AND revoked=0`, x.ID).Scan(&revoked)
	if revoked != 0 {
		t.Fatal("a moving conversation's join link still works")
	}
	// while it moves: read, yes; change, no — for everyone
	path := fmt.Sprintf("/runs/%d", x.ID)
	serveJSON(t, h, as("GET", path+"/view", "", f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", path+"/read", "", f5("alice", "read")), 200, nil)
	for _, c := range []struct{ method, path, body string }{
		{"POST", path + "/message", `{"text":"more"}`},
		{"PATCH", path, `{"title":"renamed"}`},
		{"PATCH", path, `{"visibility":"team"}`},
		{"POST", path + "/members", `{"user":"carol"}`},
		{"DELETE", path, ""},
	} {
		serveJSON(t, h, as(c.method, c.path, c.body, f5("alice", "read")), 409, nil)
	}
	serveJSON(t, h, as("POST", path+"/message", `{"text":"more"}`, ownerToken), 409, nil)
	// only its owner learns of it
	var m convMove
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d", x.ID), "", f5("alice", "read")), 200, &m)
	if m.State != "asked" || m.Root != x.ID {
		t.Fatalf("GET /moves: %+v", m)
	}
	for _, p := range []string{"", "/export"} {
		serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d%s", x.ID, p), "", f5("bob", "read")), 404, nil)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), fmt.Sprintf(`{"to":%d}`, partitionIDBase+3), f5("bob", "read")), 404, nil)
	// working: not read yet
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='running' WHERE id=?`, x.ID)
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d/export", x.ID), "", f5("alice", "read")), 409, nil)
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, x.ID)
	var b convBundle
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d/export", x.ID), "", f5("alice", "read")), 200, &b)
	if !hasMsg(&b, "user", "plan it") || b.Owner != "alice" {
		t.Fatalf("the export: %+v", b)
	}
	// done: it goes from here, and says where
	to := partitionIDBase + 3
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), `{"to":5}`, f5("alice", "read")), 400, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), fmt.Sprintf(`{"to":%d}`, to), f5("alice", "read")), 200, &m)
	if _, err := ag.db.getRun(x.ID); err == nil || m.State != "moved" || m.To != to {
		t.Fatalf("done: still here (%v), %+v", err, m)
	}
	serveJSON(t, h, as("GET", path+"/view", "", f5("alice", "read")), 404, nil)
	waitFor(t, "the deleted event saying where it went", func() bool {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		for _, ev := range alice.evs {
			if d, ok := ev.Data.(map[string]any); ok && ev.Run == x.ID && d["deleted"] == true && d["movedTo"] == float64(to) {
				return true
			}
		}
		return false
	})
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d", x.ID), "", f5("alice", "read")), 200, &m)
	if m.State != "moved" || m.To != to {
		t.Fatalf("the tombstone: %+v", m)
	}
	// done again (its answer was lost): the same; another copy: no
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), fmt.Sprintf(`{"to":%d}`, to), f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), fmt.Sprintf(`{"to":%d}`, to+1), f5("alice", "read")), 409, nil)

	// a team conversation made private (nobody in it) moves too; given up,
	// it stays here and takes changes again
	var y Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"for the team","share":{"visibility":"team"}}`, f5("alice", "read")), 200, &y)
	waitStatus(t, ag.db, y.ID, statusIdle)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", y.ID), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
	if st, _ := moveRow(ag, y.ID); st != "asked" {
		t.Fatalf("a team conversation made private: %q", st)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/abandon", y.ID), `{"why":"too large"}`, f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", y.ID), `{"title":"kept here"}`, f5("alice", "read")), 200, nil)
	// …and a done after that is refused: the conversation is still here
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", y.ID), fmt.Sprintf(`{"to":%d}`, to+2), f5("alice", "read")), 409, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", y.ID), fmt.Sprintf(`{"to":%d}`, to+2), f5("bob", "read")), 404, nil) // not his to ask about
	// a conversation gone with no move on record: the partition's copy is the only one
	serveJSON(t, h, as("POST", "/moves/999/done", fmt.Sprintf(`{"to":%d}`, to+4), f5("alice", "read")), 200, nil)

	// the owner token's conversation isn't a person's: it stays
	var own Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"the owner's"}`, ownerToken), 200, &own)
	waitStatus(t, ag.db, own.ID, statusIdle)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", own.ID), `{"visibility":"private"}`, ownerToken), 200, nil)
	if st, _ := moveRow(ag, own.ID); st != "" {
		t.Fatalf("the owner token's conversation moves: %q", st)
	}
	quiet(t, ag)
}

// TestUnshareMoveMailRefused: a move whose mail xbind refuses for good (the
// person is gone) is given up — the conversation stays, and takes changes.
func TestUnshareMoveMailRefused(t *testing.T) {
	ag, h := moveAgent(t)
	mail := stubMail(t)
	mail.mu.Lock()
	mail.fail = fmt.Errorf("%w: HTTP 404", errMailRefused)
	mail.mu.Unlock()
	var x Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"hi","share":{"visibility":"team"}}`, f5("alice", "read")), 200, &x)
	waitStatus(t, ag.db, x.ID, statusIdle)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x.ID), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
	waitFor(t, "the move given up", func() bool { st, _ := moveRow(ag, x.ID); return st == "" })
	waitMailIdle(t)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x.ID), `{"title":"still here"}`, f5("alice", "read")), 200, nil)
	quiet(t, ag)
}

// TestUnshareUnpartitioned: unpartitioned nothing moves (the legacy golden).
func TestUnshareUnpartitioned(t *testing.T) {
	setMode(t, modeLegacy, "")
	ag, h := homeAgent(t)
	_ = ag.db.putSetting("config", mustJSON(defaultConfig()))
	_ = ag.db.addMoveSchema() // a no-op unpartitioned
	var x Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"visibility":"team"}}`, alicesFrame("read")), 200, &x)
	waitStatus(t, ag.db, x.ID, statusIdle)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x.ID), `{"visibility":"private"}`, alicesFrame("read")), 200, nil)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", x.ID), `{"title":"mine"}`, alicesFrame("read")), 200, nil)
	var n int
	if err := ag.db.q.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name IN ('conv_moves','moves_in')`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("move tables unpartitioned: %d %v", n, err)
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d", x.ID), "", alicesFrame("read")), 404, nil)
	quiet(t, ag)
}

// moveGlobal stands in for the global instance a partition moves from.
type moveGlobal struct {
	mu      sync.Mutex
	export  func() (int, string)
	raw     map[string]string
	done    func(to int64) (int, string, error)
	calls   []string
	gate    chan struct{} // done waits on it, when set
	entered chan struct{}
}

func stubMoveGlobal(t *testing.T, g *moveGlobal) {
	t.Helper()
	oldExport, oldCall := exportAtGlobal, callGlobal
	exportAtGlobal = func(_ context.Context, path string) (gwResp, error) {
		g.mu.Lock()
		g.calls = append(g.calls, "GET "+path)
		exp, raw := g.export, g.raw
		g.mu.Unlock()
		if strings.HasSuffix(path, "/export") {
			st, body := exp()
			return gwResp{Status: st, Type: "application/json", Body: []byte(body)}, nil
		}
		for p, data := range raw {
			if strings.HasSuffix(path, "/raw?path="+p) {
				return gwResp{Status: 200, Type: "application/octet-stream", Body: []byte(data)}, nil
			}
		}
		return gwResp{Status: 404, Body: []byte(`{"error":"no"}`)}, nil
	}
	callGlobal = func(_ context.Context, method, path string, body []byte, _ string) (gwResp, error) {
		g.mu.Lock()
		g.calls = append(g.calls, method+" "+path+" "+string(body))
		done, gate, entered := g.done, g.gate, g.entered
		g.mu.Unlock()
		if strings.HasSuffix(path, "/done") {
			if entered != nil {
				entered <- struct{}{}
			}
			if gate != nil {
				<-gate
			}
			var b struct{ To int64 }
			_ = json.Unmarshal(body, &b)
			st, out, err := done(b.To)
			return gwResp{Status: st, Type: "application/json", Body: []byte(out)}, err
		}
		return gwResp{Status: 200, Type: "application/json", Body: []byte(`{}`)}, nil
	}
	t.Cleanup(func() {
		waitFor(t, "the mover to rest", func() bool { mover.mu.Lock(); defer mover.mu.Unlock(); return !mover.running })
		mover.mu.Lock()
		if mover.timer != nil {
			mover.timer.Stop()
			mover.timer = nil
		}
		mover.mu.Unlock()
		exportAtGlobal, callGlobal = oldExport, oldCall
	})
}

func movesIn(ag *Agent, from int64) (state string, to int64, tries int) {
	_ = ag.db.q.QueryRow(`SELECT state, to_id, tries FROM moves_in WHERE from_id=?`, from).Scan(&state, &to, &tries)
	return
}

func listedMine(t *testing.T, h http.Handler, id int64) bool {
	for _, x := range homeConvIDs(t, h, alicesFrame("read"), "mine") {
		if x == id {
			return true
		}
	}
	return false
}

// TestMoveIntoPartition (90 §I10): alice's partition takes a conversation
// the global instance moves to her — its transcript, its files (one past the
// bundle's cap read on its own, on the message that carried it) — hidden
// until global let it go, then listed: never in two lists, nothing lost. A
// mail delivered twice moves it once; a crash after the import makes it
// again (the half-made copy goes); a done that fails is tried again; a move
// given up at global drops the copy; one too large is given up there.
func TestMoveIntoPartition(t *testing.T) {
	ag, h := userAgent(t)
	for _, f := range []func() error{ag.db.addHandoffSchema, ag.db.addMoveSchema} {
		if err := f(); err != nil {
			t.Fatal(err)
		}
	}
	bundle := convBundle{Version: bundleVersion, Title: "plan it", Owner: "alice", Messages: []bundleMsg{
		{Role: "user", Content: "plan it", Sender: "alice", Files: []string{"a.md", "big.bin"}},
		{Role: "assistant", Content: "planned"},
		{Role: "user", Content: "and bob?", Sender: "bob"},
	}, Files: []bundleFile{{Path: "a.md", Content: "# a"}}, Left: []string{"big.bin"}}
	bj, _ := json.Marshal(bundle)
	g := &moveGlobal{
		export: func() (int, string) { return 200, string(bj) },
		raw:    map[string]string{"big.bin": "\x00\x01big"},
		done:   func(to int64) (int, string, error) { return 200, `{"state":"moved"}`, nil },
		gate:   make(chan struct{}),
	}
	g.entered = make(chan struct{}, 1)
	stubMoveGlobal(t, g)
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "user:bob", Topic: topicMove, Data: json.RawMessage(`{"run":5}`), At: time.Now()}, // only global moves one here
		{ID: "002", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":5}`), At: time.Now()},
		{ID: "003", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":5}`), At: time.Now()}, // mailed twice
	}}
	useMail(t, fm)
	if c, err := ag.pullMail(context.Background()); err != nil || c.Handled != 3 {
		t.Fatalf("the mailbox: %+v %v", c, err)
	}
	if !ag.db.movesWait() {
		t.Fatal("a move under way isn't work that wakes the partition")
	}
	<-g.entered // imported, and asking global to let it go: hidden here
	st, y, _ := movesIn(ag, 5)
	if st != "arrived" || y < partitionIDBase || listedMine(t, h, y) {
		t.Fatalf("before global let it go: %s %d, listed %v", st, y, listedMine(t, h, y))
	}
	close(g.gate)
	waitFor(t, "the move done", func() bool { st, _, _ := movesIn(ag, 5); return st == "done" })
	if !listedMine(t, h, y) || ag.db.movesWait() {
		t.Fatal("moved, but not listed")
	}
	r, _ := ag.db.getRun(y)
	if r.Owner != "alice" || r.Visibility != visPrivate || r.Origin != "chat" || r.SessionKey != "" {
		t.Fatalf("the moved conversation: %+v", r)
	}
	if !strings.Contains(transcript(ag.db, y), "and bob?") {
		t.Fatalf("its transcript: %s", transcript(ag.db, y))
	}
	if f, err := ag.db.replFile(y, "a.md"); err != nil || f.Content != "# a" {
		t.Fatalf("its file: %v %+v", err, f)
	}
	files, _ := ag.db.replFiles(y)
	carried := ag.db.messageFiles(y)
	var big string
	for _, f := range files {
		if strings.HasPrefix(f.Path, "big") {
			big = f.Path
		}
	}
	var msgFiles []string
	for _, ps := range carried {
		msgFiles = append(msgFiles, ps...)
	}
	if big == "" || !strings.Contains(strings.Join(msgFiles, ","), big) || !strings.Contains(strings.Join(msgFiles, ","), "a.md") {
		t.Fatalf("the file past the cap: %+v, carried %v", files, carried)
	}
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE parent_id=0 AND title='plan it'`).Scan(&n)
	if n != 1 {
		t.Fatalf("moved %d times", n)
	}

	// a crash after an import (a hidden copy, still "asked"): made again, once
	g.mu.Lock()
	g.gate = nil
	g.mu.Unlock()
	st6 := who{kind: whoUser, user: "alice"}.stamp("chat")
	st6.Origin, st6.SessionKey = heldOrigin, moveKey(6)
	half, _ := ag.db.createRunStamped("half", "{}", 0, statusIdle, st6)
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (6, ?)`, now())
	// …and the first done fails (the network): tried again
	fails := 1
	g.mu.Lock()
	g.done = func(to int64) (int, string, error) {
		if fails > 0 {
			fails--
			return 0, "", errors.New("connection reset")
		}
		return 200, `{}`, nil
	}
	g.mu.Unlock()
	kickMoves()
	<-g.entered
	waitFor(t, "the first done to fail", func() bool { st, _, tries := movesIn(ag, 6); return st == "arrived" && tries == 1 })
	_, y6, _ := movesIn(ag, 6)
	if _, err := ag.db.getRun(half); err == nil || listedMine(t, h, y6) {
		t.Fatal("the half-made copy stayed, or the new one shows before global let it go")
	}
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET next_try=0 WHERE from_id=6`)
	kickMoves()
	<-g.entered
	waitFor(t, "move 6 done", func() bool { st, _, _ := movesIn(ag, 6); return st == "done" })
	if !listedMine(t, h, y6) {
		t.Fatal("move 6 isn't listed")
	}

	// given up at global meanwhile: the copy goes
	g.mu.Lock()
	g.done = func(int64) (int, string, error) { return 409, `{"error":"no such move"}`, nil }
	g.mu.Unlock()
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (7, ?)`, now())
	kickMoves()
	<-g.entered
	waitFor(t, "move 7 dropped", func() bool { st, _, _ := movesIn(ag, 7); return st == "dropped" })
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE session_key=?`, moveKey(7)).Scan(&n)
	if n != 0 {
		t.Fatal("a dropped move left its copy")
	}

	// busy at global: later; too large: given up there
	g.mu.Lock()
	g.export = func() (int, string) { return 409, `{"error":"busy"}` }
	g.mu.Unlock()
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (8, ?)`, now())
	kickMoves()
	waitFor(t, "move 8 waiting", func() bool { st, _, tries := movesIn(ag, 8); return st == "asked" && tries == 1 })
	g.mu.Lock()
	g.export = func() (int, string) { return 413, `{"error":"too large"}` }
	g.mu.Unlock()
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET next_try=0 WHERE from_id=8`)
	kickMoves()
	waitFor(t, "move 8 dropped", func() bool { st, _, _ := movesIn(ag, 8); return st == "dropped" })
	g.mu.Lock()
	abandoned := strings.Contains(strings.Join(g.calls, "\n"), "POST /moves/8/abandon")
	g.mu.Unlock()
	if !abandoned {
		t.Fatalf("too large, not given up at global: %v", g.calls)
	}
	// un-shared once more, it is asked again
	fm.items = append(fm.items, mailItem{ID: "004", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":8}`), At: time.Now()})
	g.mu.Lock()
	g.export = func() (int, string) { return 404, `{"error":"no such move"}` }
	g.mu.Unlock()
	if _, err := ag.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "move 8 asked again, then dropped (gone at global)", func() bool {
		st, _, tries := movesIn(ag, 8)
		return st == "dropped" && tries == 0
	})
	quiet(t, ag)
}
