package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
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

// moveKeyOf is the key root's move was mailed with.
func moveKeyOf(ag *Agent, root int64) (key string) {
	_ = ag.db.q.QueryRow(`SELECT key FROM conv_moves WHERE root=?`, root).Scan(&key)
	return
}

// exportMove is alice's partition reading move id with its mailed key (want: its status).
func exportMove(t *testing.T, h http.Handler, ag *Agent, id int64, want int) moveExport {
	t.Helper()
	var x moveExport
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d/export?key=%s", id, moveKeyOf(ag, id)), "", f5("alice", "read")), want, &x)
	return x
}

// doneMove is POST /moves/{id}/done {to, ticket, key} as hdr.
func doneMove(t *testing.T, h http.Handler, id, to int64, ticket, key string, hdr map[string]string, want int) convMove {
	t.Helper()
	var m convMove
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", id), fmt.Sprintf(`{"to":%d,"ticket":%q,"key":%q}`, to, ticket, key), hdr), want, &m)
	return m
}

// TestUnshareMovesAtGlobal (90 §I10): at the global instance a person's
// chat that stops being shared — its last member removed, or made private
// with nobody in it — starts moving to its owner's own partition: a
// conv/move mail to them, its links revoked, every change refused while it
// moves but stopping it (reads go on). Only the owner's partition drives it:
// the export (a ticket), done with that ticket (it goes from here, its event
// and tombstone saying where), idempotently; the owner's page gets 403 on
// those, anyone else's partition learns nothing. A conversation still
// shared, or not a person's, stays.
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
	var mi moveItem
	key := moveKeyOf(ag, x.ID)
	if sent[0].to != "user:alice" || sent[0].topic != topicMove || json.Unmarshal(sent[0].data, &mi) != nil || mi.Run != x.ID || mi.Key == "" || mi.Key != key {
		t.Fatalf("the move's mail: %+v %s", sent[0], sent[0].data)
	}
	var revoked int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM share_links WHERE run_id=? AND revoked=0`, x.ID).Scan(&revoked)
	if revoked != 0 {
		t.Fatal("a moving conversation's join link still works")
	}
	// while it moves: read, yes; change, no — for everyone; stop it, yes
	path := fmt.Sprintf("/runs/%d", x.ID)
	serveJSON(t, h, as("GET", path+"/view", "", f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", path+"/read", "", f5("alice", "read")), 200, nil)
	for _, c := range []struct{ method, path, body string }{
		{"POST", path + "/message", `{"text":"more"}`},
		{"PATCH", path, `{"title":"renamed"}`},
		{"PATCH", path, `{"visibility":"team"}`},
		{"POST", path + "/members", `{"user":"carol"}`},
		{"POST", path + "/resume", ""},
		{"DELETE", path, ""},
	} {
		serveJSON(t, h, as(c.method, c.path, c.body, f5("alice", "read")), 409, nil)
	}
	serveJSON(t, h, as("POST", path+"/message", `{"text":"more"}`, ownerToken), 409, nil)
	serveJSON(t, h, as("POST", path+"/cancel", "", f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", path+"/interrupt", "", f5("alice", "read")), 200, nil)
	// only its owner learns of it; only their partition drives it
	var m convMove
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d", x.ID), "", alicesFrame("read")), 200, &m)
	if m.State != "asked" || m.Root != x.ID {
		t.Fatalf("GET /moves: %+v", m)
	}
	// alice's page or terminals (stamped as her partition, without the mailed key), the owner token: 403
	for _, hdr := range []map[string]string{alicesFrame("read"), f5("alice", "read"), ownerToken} {
		for _, c := range []struct{ method, path string }{{"GET", "/export"}, {"POST", "/done"}, {"POST", "/abandon"}} {
			serveJSON(t, h, as(c.method, fmt.Sprintf("/moves/%d%s", x.ID, c.path), fmt.Sprintf(`{"to":%d}`, partitionIDBase+3), hdr), 403, nil)
		}
	}
	for _, id := range []int64{x.ID, 99999} { // bob's partition: the same answers for alice's move and for no move at all
		for _, p := range []string{"", "/export?key=" + key} {
			serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d%s", id, p), "", f5("bob", "read")), 404, nil)
		}
		serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/abandon", id), fmt.Sprintf(`{"key":%q}`, key), f5("bob", "read")), 404, nil)
		if m := doneMove(t, h, id, partitionIDBase+3, "", key, f5("bob", "read"), 200); m.State != "gone" {
			t.Fatalf("bob's done on %d: %+v", id, m)
		}
	}
	if st, _ := moveRow(ag, x.ID); st != "asked" {
		t.Fatalf("bob's calls changed alice's move: %q", st)
	}
	// working (a turn, a wait, input not taken yet): not read yet
	for _, s := range []string{statusRunning, statusSleep, statusWaiting, statusAwait, statusQueued} {
		_, _ = ag.db.q.Exec(`UPDATE runs SET status=? WHERE id=?`, s, x.ID)
		exportMove(t, h, ag, x.ID, 409)
	}
	_, _ = ag.db.q.Exec(`UPDATE runs SET status='idle' WHERE id=?`, x.ID)
	res, _ := ag.db.q.Exec(`INSERT INTO inbox (run_id, kind, body, created) VALUES (?, 'user', '{"text":"queued"}', ?)`, x.ID, now())
	qid, _ := res.LastInsertId()
	exportMove(t, h, ag, x.ID, 409)
	_, _ = ag.db.q.Exec(`DELETE FROM inbox WHERE id=?`, qid)
	b := exportMove(t, h, ag, x.ID, 200)
	if !hasMsg(&b.convBundle, "user", "plan it") || b.Owner != "alice" || b.Ticket == "" {
		t.Fatalf("the export: %+v", b)
	}
	to := partitionIDBase + 3
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/done", x.ID), `{"to":5}`, f5("alice", "read")), 400, nil)
	doneMove(t, h, x.ID, to, "not-the-ticket", key, f5("alice", "read"), 412)
	doneMove(t, h, x.ID, to, b.Ticket, "not-the-key", f5("alice", "read"), 403)
	// done: it goes from here, and says where
	if m = doneMove(t, h, x.ID, to, b.Ticket, key, f5("alice", "read"), 200); m.State != "moved" || m.To != to {
		t.Fatalf("done: %+v", m)
	}
	if _, err := ag.db.getRun(x.ID); err == nil {
		t.Fatal("moved, but still here")
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
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d", x.ID), "", alicesFrame("read")), 200, &m)
	if m.State != "moved" || m.To != to {
		t.Fatalf("the tombstone: %+v", m)
	}
	// done again (its answer was lost): the same; another copy: no
	doneMove(t, h, x.ID, to, b.Ticket, key, f5("alice", "read"), 200)
	doneMove(t, h, x.ID, to+1, b.Ticket, key, f5("alice", "read"), 409)
	serveJSON(t, h, as("GET", fmt.Sprintf("/moves/%d/export?key=%s", x.ID, key), "", f5("alice", "read")), 404, nil) // moved: nothing to read

	// a team conversation made private (nobody in it) moves too; given up,
	// it stays here and takes changes again
	var y Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"for the team","share":{"visibility":"team"}}`, f5("alice", "read")), 200, &y)
	waitStatus(t, ag.db, y.ID, statusIdle)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", y.ID), `{"visibility":"private"}`, f5("alice", "read")), 200, nil)
	if st, _ := moveRow(ag, y.ID); st != "asked" {
		t.Fatalf("a team conversation made private: %q", st)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/moves/%d/abandon", y.ID), fmt.Sprintf(`{"why":"too large","key":%q}`, moveKeyOf(ag, y.ID)), f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("PATCH", fmt.Sprintf("/runs/%d", y.ID), `{"title":"kept here"}`, f5("alice", "read")), 200, nil)
	// …and a done after that is refused: the conversation is still here
	doneMove(t, h, y.ID, to+2, "", "", f5("alice", "read"), 409)
	doneMove(t, h, y.ID, to+2, "", "", f5("bob", "read"), 200) // bob learns nothing: the same as for no move
	// a conversation gone with no move on record: the partition's copy is the only one
	if m := doneMove(t, h, 999, to+4, "", "", f5("alice", "read"), 200); m.State != "gone" {
		t.Fatalf("done with nothing on record: %+v", m)
	}

	// the owner token's conversation isn't a person's: it stays
	var own Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"the owner's","share":{"visibility":"team"}}`, ownerToken), 200, &own)
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
	export  func(path string) (int, string)
	move    func(path string) (int, string) // GET /moves/{id}
	raw     map[string]string
	done    func(to int64, ticket string) (int, string, error)
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
		exp, mv, raw := g.export, g.move, g.raw
		g.mu.Unlock()
		if strings.Contains(path, "/export") {
			st, body := exp(path)
			return gwResp{Status: st, Type: "application/json", Body: []byte(body)}, nil
		}
		if strings.HasPrefix(path, "/moves/") && mv != nil {
			st, body := mv(path)
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
			var b struct {
				To     int64
				Ticket string
			}
			_ = json.Unmarshal(body, &b)
			st, out, err := done(b.To, b.Ticket)
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

func (g *moveGlobal) called(what string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return strings.Contains(strings.Join(g.calls, "\n"), what)
}

func (g *moveGlobal) set(f func(g *moveGlobal)) {
	g.mu.Lock()
	defer g.mu.Unlock()
	f(g)
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

func exportJSON(x moveExport) string {
	b, _ := json.Marshal(x)
	return string(b)
}

// TestMoveIntoPartition (90 §I10): alice's partition takes a conversation
// the global instance moves to her — its transcript, its files (one past the
// bundle's cap read on its own, on the message that carried it), its notes,
// its schedule (reporting into the new id), her pin — hidden until global
// let it go (with the export's ticket), then listed: never in two lists,
// nothing lost. A mail delivered twice moves it once; a crash after the
// import makes it again (the half-made copy goes); a done that fails is
// tried again; a move given up at global drops the copy; one too large is
// given up there.
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
	x := moveExport{convBundle: bundle, Ticket: "t1", Memory: map[string]string{"plan": "ship friday"}, PinnedAt: 1234,
		Schedules: []*Schedule{{ID: 9, Name: "standup", Cron: "0 9 * * *", Goal: "ask me how it went", Owner: "alice", Visibility: visTeam,
			Mode: modeConversation, TargetRun: 5, CreatedByRun: 5, Enabled: true}},
		Behind: []string{"its sandboxes (they are the shared space's)"}}
	var tickets []string
	g := &moveGlobal{
		export: func(string) (int, string) { return 200, exportJSON(x) },
		raw:    map[string]string{"big.bin": "\x00\x01big"},
		done: func(to int64, ticket string) (int, string, error) {
			tickets = append(tickets, ticket)
			return 200, `{"state":"moved"}`, nil
		},
		gate: make(chan struct{}),
	}
	g.entered = make(chan struct{}, 1)
	stubMoveGlobal(t, g)
	fm := &fakeMail{items: []mailItem{
		{ID: "001", From: "user:bob", Topic: topicMove, Data: json.RawMessage(`{"run":5}`), At: time.Now()}, // only global moves one here
		{ID: "002", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":5,"key":"k5"}`), At: time.Now()},
		{ID: "003", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":5,"key":"k5"}`), At: time.Now()}, // mailed twice
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
	var early int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM schedules WHERE target_run=?`, y).Scan(&early)
	close(g.gate)
	waitFor(t, "the move done", func() bool { st, _, _ := movesIn(ag, 5); return st == "done" })
	if !listedMine(t, h, y) || ag.db.movesWait() || early != 0 {
		t.Fatalf("moved, but not listed (or its schedule came before: %d)", early)
	}
	g.mu.Lock()
	if len(tickets) != 1 || tickets[0] != "t1" {
		t.Errorf("done's tickets: %v", tickets)
	}
	g.mu.Unlock()
	if !g.called("GET /moves/5/export?key=k5") || !g.called(`"key":"k5"`) {
		t.Error("the mailed key wasn't what the partition drove the move with")
	}
	r, _ := ag.db.getRun(y)
	if r.Owner != "alice" || r.Visibility != visPrivate || r.Origin != "chat" || r.SessionKey != "" {
		t.Fatalf("the moved conversation: %+v", r)
	}
	if tr := transcript(ag.db, y); !strings.Contains(tr, "and bob?") {
		t.Fatalf("its transcript: %s", tr)
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
	if mem, _ := ag.db.memory(y); mem["plan"] != "ship friday" {
		t.Fatalf("its notes: %v", mem)
	}
	scheds, _ := ag.db.listSchedules()
	if len(scheds) != 1 || scheds[0].TargetRun != y || scheds[0].CreatedByRun != y || scheds[0].Owner != "alice" ||
		scheds[0].Visibility != visPrivate || scheds[0].Mode != modeConversation || !scheds[0].Enabled || scheds[0].Goal != "ask me how it went" {
		t.Fatalf("its schedule here: %+v", scheds)
	}
	if s := ag.db.userStates("alice", []int64{y})[y]; s.PinnedAt != 1234 {
		t.Fatalf("alice's pin: %+v", s)
	}
	var note string
	_ = ag.db.q.QueryRow(`SELECT detail FROM steps WHERE run_id=? AND kind='note'`, y).Scan(&note)
	if !strings.Contains(note, "Not moved with it: its sandboxes") || !strings.Contains(note, "schedule reporting into it came along") {
		t.Fatalf("the note: %s", note)
	}
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE parent_id=0 AND title='plan it'`).Scan(&n)
	if n != 1 {
		t.Fatalf("moved %d times", n)
	}
	x.Schedules, x.Memory, x.PinnedAt, x.Behind = nil, nil, 0, nil

	// a crash after an import (a hidden copy, still "asked"): made again, once
	g.set(func(g *moveGlobal) { g.gate = nil })
	st6 := who{kind: whoUser, user: "alice"}.stamp("chat")
	st6.Origin, st6.SessionKey = heldOrigin, moveKey(6)
	half, _ := ag.db.createRunStamped("half", "{}", 0, statusIdle, st6)
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (6, ?)`, now())
	// …and the first done fails (the network): tried again
	fails := 1
	g.set(func(g *moveGlobal) {
		g.done = func(int64, string) (int, string, error) {
			if fails > 0 {
				fails--
				return 0, "", errors.New("connection reset")
			}
			return 200, `{}`, nil
		}
	})
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

	// changed at global since it was read (412): its copy goes, it is read again
	var reads int
	g.set(func(g *moveGlobal) {
		g.export = func(string) (int, string) {
			reads++
			x.Ticket = fmt.Sprintf("t10-%d", reads)
			return 200, exportJSON(x)
		}
		g.done = func(_ int64, ticket string) (int, string, error) {
			if ticket == "t10-1" {
				return 412, `{"error":"read it again: it changed since it was read"}`, nil
			}
			return 200, `{}`, nil
		}
	})
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (10, ?)`, now())
	kickMoves()
	<-g.entered
	waitFor(t, "move 10 read again", func() bool { st, to, tries := movesIn(ag, 10); return st == "asked" && to == 0 && tries == 1 })
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET next_try=0 WHERE from_id=10`)
	kickMoves()
	<-g.entered
	waitFor(t, "move 10 done", func() bool { st, _, _ := movesIn(ag, 10); return st == "done" })
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE parent_id=0 AND (session_key=? OR id=(SELECT to_id FROM moves_in WHERE from_id=10))`, moveKey(10)).Scan(&n)
	if n != 1 || reads != 2 {
		t.Fatalf("move 10: %d copies after %d reads", n, reads)
	}

	// given up at global meanwhile: the copy goes
	g.set(func(g *moveGlobal) {
		g.export = func(string) (int, string) { return 200, exportJSON(x) }
		g.done = func(int64, string) (int, string, error) { return 409, `{"error":"no such move"}`, nil }
	})
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (7, ?)`, now())
	kickMoves()
	<-g.entered
	waitFor(t, "move 7 dropped", func() bool { st, _, _ := movesIn(ag, 7); return st == "dropped" })
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE session_key=?`, moveKey(7)).Scan(&n)
	if n != 0 {
		t.Fatal("a dropped move left its copy")
	}

	// not importable here (nothing ever said in it): given up at global, not tried for days
	empty := moveExport{convBundle: convBundle{Version: bundleVersion, Title: "never sent", Owner: "alice"}, Ticket: "t11"}
	g.set(func(g *moveGlobal) { g.export = func(string) (int, string) { return 200, exportJSON(empty) } })
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (11, ?)`, now())
	kickMoves()
	waitFor(t, "move 11 dropped", func() bool { st, _, _ := movesIn(ag, 11); return st == "dropped" })
	if !g.called("POST /moves/11/abandon") {
		t.Fatal("a conversation this home can't take wasn't given up at global")
	}

	// the export answers 404, but the move stands at global (a route not
	// there, say): later, not dropped
	g.set(func(g *moveGlobal) {
		g.export = func(string) (int, string) { return 404, `404 page not found` }
		g.move = func(string) (int, string) { return 200, `{"run":12,"state":"asked"}` }
	})
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (12, ?)`, now())
	kickMoves()
	waitFor(t, "move 12 waiting", func() bool { st, _, tries := movesIn(ag, 12); return st == "asked" && tries == 1 })
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET state='dropped' WHERE from_id=12`)

	// busy at global: later; too large: given up there
	g.set(func(g *moveGlobal) {
		g.export = func(string) (int, string) { return 409, `{"error":"busy"}` }
		g.move = nil
	})
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (8, ?)`, now())
	kickMoves()
	waitFor(t, "move 8 waiting", func() bool { st, _, tries := movesIn(ag, 8); return st == "asked" && tries == 1 })
	g.set(func(g *moveGlobal) { g.export = func(string) (int, string) { return 413, `{"error":"too large"}` } })
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET next_try=0 WHERE from_id=8`)
	kickMoves()
	waitFor(t, "move 8 dropped", func() bool { st, _, _ := movesIn(ag, 8); return st == "dropped" })
	if !g.called("POST /moves/8/abandon") {
		t.Fatal("too large, not given up at global")
	}
	// un-shared once more, it is asked again; gone at global (the move's own 404): dropped
	fm.items = append(fm.items, mailItem{ID: "004", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":8}`), At: time.Now()})
	g.set(func(g *moveGlobal) { g.export = func(string) (int, string) { return 404, `{"error":"no such move"}` } })
	if _, err := ag.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "move 8 asked again, then dropped (gone at global)", func() bool {
		st, _, tries := movesIn(ag, 8)
		return st == "dropped" && tries == 0
	})
	g.mu.Lock()
	asked := slices.Contains(g.calls, "GET /moves/8")
	g.mu.Unlock()
	if !asked {
		t.Fatal("the export's 404 wasn't checked with the move itself")
	}
	// asked anew at global (a new key) while this side still tries the old one: the new key is taken
	_, _ = ag.db.q.Exec(`INSERT INTO moves_in (from_id, created, key, next_try) VALUES (13, ?, 'old', ?)`, now(), now()+3600)
	fm.items = append(fm.items, mailItem{ID: "005", From: "global", Topic: topicMove, Data: json.RawMessage(`{"run":13,"key":"new"}`), At: time.Now()})
	if _, err := ag.pullMail(context.Background()); err != nil {
		t.Fatal(err)
	}
	var k13 string
	_ = ag.db.q.QueryRow(`SELECT key FROM moves_in WHERE from_id=13`).Scan(&k13)
	if k13 != "new" {
		t.Fatalf("the move asked anew kept the old key: %q", k13)
	}
	_, _ = ag.db.q.Exec(`UPDATE moves_in SET state='dropped' WHERE from_id=13`)
	quiet(t, ag)
}
