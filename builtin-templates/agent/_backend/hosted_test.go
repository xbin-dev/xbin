package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// withTeam gives the process a team database as the global instance
// migrates it (setMode restores the previous one).
func withTeam(t *testing.T) *teamDB {
	t.Helper()
	tdb, err := migrateTeam(filepath.Join(t.TempDir(), "team.db"))
	if err != nil {
		t.Fatal(err)
	}
	teamStore.Store(tdb)
	t.Cleanup(func() { tdb.sql.Close() })
	return tdb
}

// quickWakes makes the global instance ring a host once, at once.
func quickWakes(t *testing.T) {
	old := wakeRetries
	wakeRetries = []time.Duration{0}
	t.Cleanup(func() { wakeRetries = old })
}

// fwdCapture stands in for the global instance the host's engine posts its
// run to.
type fwdCapture struct {
	mu  sync.Mutex
	evs []fwdEvent
}

func captureFwd(t *testing.T) *fwdCapture {
	c := &fwdCapture{}
	hostedFwd.mu.Lock()
	old := hostedFwd.post
	hostedFwd.post = func(_ context.Context, body []byte) error {
		var b struct{ Events []fwdEvent }
		_ = json.Unmarshal(body, &b)
		c.mu.Lock()
		c.evs = append(c.evs, b.Events...)
		c.mu.Unlock()
		return nil
	}
	hostedFwd.mu.Unlock()
	t.Cleanup(func() {
		time.Sleep(3 * fwdWindow) // the last batch
		hostedFwd.mu.Lock()
		hostedFwd.post = old
		hostedFwd.mu.Unlock()
	})
	return c
}

func (c *fwdCapture) count(root int64, typ string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, ev := range c.evs {
		if ev.Root == root && (typ == "" || ev.Type == typ) {
			n++
		}
	}
	return n
}

func TestAudienceBeyond(t *testing.T) {
	snap := audience{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Members: map[string]string{"bob": roleViewer}}
	for _, c := range []struct {
		now  audience
		want string
	}{
		{snap, ""},
		{audience{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Members: map[string]string{}}, ""}, // narrower
		{audience{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Members: map[string]string{"bob": roleViewer, "carol": roleViewer}}, "carol"},
		{audience{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Members: map[string]string{"bob": roleParticipant}}, "bob (to talk to it)"},
		{audience{Owner: "alice", Visibility: visTeam, TeamRole: roleViewer, Members: map[string]string{"bob": roleViewer}}, "the team"},
		{audience{Owner: "dave", Visibility: visPrivate, TeamRole: roleViewer, Members: map[string]string{"bob": roleViewer}}, "dave (the owner)"},
	} {
		if got := strings.Join(c.now.beyond(snap), ","); got != c.want {
			t.Errorf("%s beyond the snapshot: %q, want %q", c.now.key(), got, c.want)
		}
	}
	teamSnap := audience{Owner: "alice", Visibility: visTeam, TeamRole: roleViewer, Members: map[string]string{}}
	up := audience{Owner: "alice", Visibility: visTeam, TeamRole: roleParticipant, Members: map[string]string{}}
	if got := strings.Join(up.beyond(teamSnap), ","); got != "the team (to talk to it)" {
		t.Errorf("the team made participants: %q", got)
	}
}

// TestHostedAtGlobal: the global instance moves a shared conversation into
// team for its host (a participant), serves it to its members from there —
// the view, a member's message into team's inbox with a ring for the host
// and nothing driven at global, the host's live run fanned out to the
// members' streams (a non-host's post refused), no join links — lists it,
// rings the host when the audience widens, and gives it back as a plain
// shared conversation once hosting ended.
func TestHostedAtGlobal(t *testing.T) {
	ag, h := globalAgent(t)
	tdb := withTeam(t)
	mail := stubMail(t)
	quickWakes(t)
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"plan the trip","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &run)
	waitStatus(t, ag.db, run.ID, statusIdle)
	if _, err := ag.db.q.Exec(`INSERT INTO share_links (run_id, token_hash, role, created_by, created, expires) VALUES (?, 'x', 'viewer', 'alice', ?, 0)`, run.ID, now()); err != nil {
		t.Fatal(err)
	}

	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("dave", "read")), 404, nil)
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), ownerToken), 403, nil)
	var moved struct {
		Conversation, From int64
		Audience           audience
	}
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, run.ID), f5("alice", "read")), 200, &moved)
	if !hostedID(moved.Conversation) || moved.From != run.ID || moved.Audience.Owner != "alice" || moved.Audience.Members["bob"] != roleParticipant {
		t.Fatalf("moved: %+v", moved)
	}
	id := moved.Conversation
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", run.ID), "", f5("alice", "read")), 404, nil)
	var links int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM share_links WHERE run_id=?`, run.ID).Scan(&links)
	if links != 0 {
		t.Fatal("the moved conversation's join links survived")
	}
	// idempotent for its host; one host per conversation
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, id), f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", "/hosted", fmt.Sprintf(`{"conversation":%d}`, id), f5("bob", "read")), 409, nil)

	var v struct {
		Hosted   map[string]any
		Messages []Message
		Access   string
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", id), "", f5("bob", "read")), 200, &v)
	if v.Hosted["host"] != "alice" || v.Hosted["state"] != hostActive || v.Access != "participant" {
		t.Fatalf("bob's view of the hosted conversation: hosted %v, access %s", v.Hosted, v.Access)
	}
	if !strings.Contains(fmt.Sprint(v.Messages), "plan the trip") {
		t.Fatalf("the transcript didn't move: %v", v.Messages)
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", id), "", f5("dave", "read")), 404, nil)

	// a member writes: into team, the host is rung, nothing runs here
	calls := len(fakeOf(ag).calls)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", id), `{"text":"and the hotel?"}`, f5("bob", "read")), 200, nil)
	sent := mail.wait(t, 1)
	if sent[0].to != "user:alice" || sent[0].topic != topicHostedInput || !strings.Contains(string(sent[0].data), fmt.Sprint(id)) {
		t.Fatalf("the ring: %+v %s", sent[0], sent[0].data)
	}
	if n := len(tdb.runs.undelivered(id)); n != 1 {
		t.Fatalf("team's inbox holds %d inputs, want 1", n)
	}
	time.Sleep(50 * time.Millisecond)
	fakeOf(ag).mu.Lock()
	after := len(fakeOf(ag).calls)
	fakeOf(ag).mu.Unlock()
	if after != calls {
		t.Fatal("BUG: the global instance's engine drove a hosted conversation")
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/links", id), `{"role":"viewer"}`, f5("alice", "read")), 409, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/compact", id), `{}`, f5("alice", "read")), 409, nil)
	if !containsID(homeConvIDs(t, h, f5("bob", "read"), "mine"), id) || !containsID(homeConvIDs(t, h, f5("alice", "read"), "shared"), id) {
		t.Fatal("the hosted conversation isn't in its members' lists")
	}
	if containsID(homeConvIDs(t, h, f5("dave", "read"), "shared"), id) {
		t.Fatal("BUG: a stranger lists the hosted conversation")
	}

	// the host's run, live, to every member (90 §I4); only its host posts it
	bob := followAs(t, h, fmt.Sprintf("/stream?run=%d&deltas=1", id), f5("bob", "read"))
	waitFor(t, "bob's stream to say hello", func() bool { bob.mu.Lock(); defer bob.mu.Unlock(); return len(bob.evs) > 0 })
	post := func(who string, evs ...fwdEvent) map[string]int {
		b, _ := json.Marshal(map[string]any{"events": evs})
		var out map[string]int
		serveJSON(t, h, as("POST", "/hosted/events", string(b), f5(who, "read")), 200, &out)
		return out
	}
	text := func(s string) fwdEvent {
		return fwdEvent{Type: evText, Run: id, Root: id, Key: "text:" + itoa(id), Data: map[string]any{"text": s, "model": "m", "started": 5}}
	}
	if got := post("bob", text("forged by bob")); got["refused"] != 1 || got["published"] != 0 {
		t.Fatalf("bob's post of alice's run: %v", got)
	}
	if got := post("alice", fwdEvent{Type: evBye, Run: id, Root: id}, fwdEvent{Type: evRevoked, Run: id, Root: id}); got["refused"] != 2 {
		t.Fatalf("the host's post of a stream's own words: %v", got)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/approve", id), `{"approve":true}`, f5("bob", "read")), 400, nil) // nothing parked
	post("alice", text("Once"))
	waitFor(t, "bob's stream to carry the draft's start", func() bool { s, _ := bob.text(t, id); return s == "Once" })
	post("alice", text("Once upon a time"))
	waitFor(t, "bob's stream to carry the host's draft", func() bool { s, _ := bob.text(t, id); return s == "Once upon a time" })
	bob.mu.Lock()
	deltas := 0
	for _, ev := range bob.evs {
		if ev.Type == evText+".delta" && ev.Run == id {
			deltas++
		}
	}
	bob.mu.Unlock()
	if deltas == 0 {
		t.Fatal("the host's draft reached bob without a delta")
	}
	var mid struct{ Drafts []draft }
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", id), "", f5("bob", "read")), 200, &mid)
	if len(mid.Drafts) != 1 || mid.Drafts[0].Text != "Once upon a time" {
		t.Fatalf("a member connecting mid-answer: drafts %+v", mid.Drafts)
	}

	// a wider audience rings the host (whose engine pauses it)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/members", id), `{"user":"carol","role":"viewer"}`, f5("alice", "read")), 200, nil)
	sent = mail.wait(t, 2)
	if !strings.Contains(string(sent[1].data), `"signal":"audience"`) {
		t.Fatalf("the audience ring: %s", sent[1].data)
	}

	// continuing without the host: only once hosting ended
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", id), "", f5("bob", "read")), 409, nil)
	_ = tdb.runs.setTeamHostState(id, hostDropped, "declined", "")
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", id), "", f5("carol", "read")), 403, nil) // a viewer
	var back struct{ Conversation int64 }
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosted/%d/continue", id), "", f5("bob", "read")), 200, &back)
	if back.Conversation <= 0 || back.Conversation >= teamIDBase {
		t.Fatalf("continued as #%d", back.Conversation)
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", id), "", f5("bob", "read")), 404, nil)
	var bv struct{ Messages []Message }
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", back.Conversation), "", f5("bob", "read")), 200, &bv)
	if !strings.Contains(fmt.Sprint(bv.Messages), "plan the trip") {
		t.Fatalf("the continued conversation's transcript: %v", bv.Messages)
	}
	if a, _ := ag.aclOf(back.Conversation); a.owner != "alice" || a.members["bob"] != roleParticipant || a.members["carol"] != roleViewer {
		t.Fatalf("the continued conversation's audience: %+v", a)
	}
}

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

// hostedRun makes a conversation in team as the global instance's move
// would (owner, members, a first input waiting), noted as hosted by host.
func hostedRun(t *testing.T, tr *DB, owner string, members map[string]string, host, text string) int64 {
	t.Helper()
	cfg := defaultConfig()
	cfg.System = "test agent"
	cfg.Features = mergeFeatures(cfg.Features, map[string]bool{"streaming": false})
	raw, _ := json.Marshal(cfg)
	id, err := tr.createRunStamped(clip(text, 40), string(raw), 0, statusIdle, runStamp{Owner: owner, Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tr.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System}); err != nil {
		t.Fatal(err)
	}
	for u, r := range members {
		if _, err := tr.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, ?, ?, ?, 'invite', ?)`, id, u, r, owner, now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := tr.q.Exec(`INSERT INTO team_hosts (run_id, host, state, since, created) VALUES (?, ?, 'active', ?, ?)`, id, host, now(), now()); err != nil {
		t.Fatal(err)
	}
	queueTeam(t, tr, id, owner, text)
	return id
}

func queueTeam(t *testing.T, tr *DB, id int64, sender, text string) {
	t.Helper()
	if err := tr.Tx(func(d *DB) error {
		_, _, err := d.enqueue(id, inboxUser, inboxBody{Text: text, Source: "human", Sender: sender}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// hostAgent is alice's partition with team open and her host engine's
// pieces stubbed: its live posts are captured.
func hostAgent(t *testing.T) (*Agent, http.Handler, *teamDB, *fwdCapture) {
	t.Helper()
	ag, h := userAgent(t)
	if err := ag.db.addHostedSchema(); err != nil {
		t.Fatal(err)
	}
	tdb := withTeam(t)
	fwd := captureFwd(t)
	t.Cleanup(func() {
		if e := hostEngine.Load(); e != nil {
			e.Shutdown(2 * time.Second)
		}
		hostEngine.Store(nil)
	})
	return ag, h, tdb, fwd
}

func (ag *Agent) hostSnapshot(t *testing.T, tr *DB, id int64) {
	t.Helper()
	acl, err := tr.loadACL(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`INSERT INTO hosted (conversation, state, snapshot, resources, confirmed_at, created) VALUES (?, 'active', ?, '[]', ?, ?)`,
		id, audienceOf(acl).key(), now(), now()); err != nil {
		t.Fatal(err)
	}
}

func answered(tr *DB, id int64, text string) bool {
	msgs, _ := tr.messages(id, false)
	for _, m := range msgs {
		if m.Role == "assistant" && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// TestHostEngineDrivesOnlyItsOwn: a person's host engine drives the
// conversations its own hosted table lists — a team row naming them as host
// (a forged one) is never taken up, not even marked — under its own epoch;
// its run goes to the global instance live; a wider audience pauses it (the
// step not taken) until the host confirms what they were shown; taking the
// resources back drops it.
func TestHostEngineDrivesOnlyItsOwn(t *testing.T) {
	ag, h, tdb, fwd := hostAgent(t)
	tr := tdb.runs
	f := fakeOf(ag)
	f.on(lastUser("forged"), say("SHOULD NEVER RUN"))
	f.on(lastUser("legit"), say("hosted answer"))
	f.on(lastUser("again"), say("second answer"))
	f.on(lastUser("third"), say("SHOULD NOT RUN EITHER"))

	forged := hostedRun(t, tr, "alice", map[string]string{"bob": roleParticipant}, "user:alice", "forged: spend alice's resources")
	legit := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "legit question")
	ag.hostSnapshot(t, tr, legit)

	if e := ensureHostEngine(); e == nil {
		t.Fatal("no host engine")
	}
	waitFor(t, "the hosted conversation's answer", func() bool { return answered(tr, legit, "hosted answer") })
	time.Sleep(100 * time.Millisecond)
	if len(f.callsFor(forged)) != 0 || answered(tr, forged, "SHOULD") {
		t.Fatal("BUG: the host's engine drove a conversation its own table doesn't list")
	}
	if n := len(tr.undelivered(forged)); n != 1 {
		t.Fatalf("the forged conversation's input was taken (%d left)", n)
	}
	var lease string
	_ = tr.q.QueryRow(`SELECT lease_owner FROM runs WHERE id=?`, forged).Scan(&lease)
	if lease != "" {
		t.Fatalf("the forged conversation was marked driven: %q", lease)
	}
	if ep := tr.getSetting("engine_epoch." + hostKey()); ep != "1" || tr.getSetting("engine_epoch") != "" {
		t.Fatalf("the host's epoch: %q (the legacy key %q)", ep, tr.getSetting("engine_epoch"))
	}
	waitFor(t, "the run posted to the global instance", func() bool { return fwd.count(legit, evMessage) > 0 && fwd.count(legit, evRun) > 0 })
	if fwd.count(forged, "") != 0 {
		t.Fatal("the forged conversation's events went to the global instance")
	}

	// a wider audience: carol joins at global — paused, the input not taken,
	// the host told on their phone
	var pushMu sync.Mutex
	var pushes []xbin.UserNotification
	ag.needs = newNeedsPusher(func(_ context.Context, n xbin.UserNotification) error {
		pushMu.Lock()
		pushes = append(pushes, n)
		pushMu.Unlock()
		return nil
	})
	t.Cleanup(func() { ag.needs = nil })
	if _, err := tr.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, 'carol', 'viewer', 'bob', 'invite', ?)`, legit, now()); err != nil {
		t.Fatal(err)
	}
	queueTeam(t, tr, legit, "bob", "again, please")
	hostEngine.Load().Poke(legit)
	var row *hostedRow
	waitFor(t, "the conversation to pause", func() bool { row, _ = ag.db.hostedRow(legit); return row != nil && row.State == hostPaused })
	if strings.Join(row.Pending, ",") != "carol" {
		t.Fatalf("waiting for: %v", row.Pending)
	}
	th, _ := tr.teamHost(legit)
	if th.State != hostPaused || th.Reason != "confirm" {
		t.Fatalf("team's note of the pause: %+v", th)
	}
	if info := hostedInfo(th); fmt.Sprint(info["pending"]) != "[carol]" || info["pendingKey"] != row.PendingKey {
		t.Fatalf("the members' view of the pause: pending %v, key %v (the host's %s)", info["pending"], info["pendingKey"], row.PendingKey)
	}
	time.Sleep(100 * time.Millisecond)
	if answered(tr, legit, "second answer") {
		t.Fatal("BUG: a paused conversation ran for its wider audience")
	}
	waitFor(t, "the pause posted to the global instance", func() bool { return fwd.count(legit, "hosted") > 0 })
	waitFor(t, "the host told", func() bool { pushMu.Lock(); defer pushMu.Unlock(); return len(pushes) == 1 })
	pushMu.Lock()
	if p := pushes[0]; p.User != "alice" || p.Link != fmt.Sprintf("#c=%d", legit) || !strings.Contains(p.Body, "carol") {
		t.Fatalf("the push to the host: %+v", p)
	}
	pushMu.Unlock()

	// only the host confirms, and only what they were shown
	viewed := alicesFrame("read")
	viewed["X-XBin-Viewed-By"] = "admin"
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", legit), `{}`, viewed), 403, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", legit), `{"seen":"something else"}`, alicesFrame("read")), 409, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/hosting/%d/confirm", legit), fmt.Sprintf(`{"seen":%q}`, row.PendingKey), alicesFrame("read")), 200, nil)
	waitFor(t, "the answer after the confirmation", func() bool { return answered(tr, legit, "second answer") })

	// taking the resources back: dropped, nothing more runs
	var list struct{ Items []hostedRow }
	serveJSON(t, h, as("GET", "/hosting", "", alicesFrame("read")), 200, &list)
	if len(list.Items) != 1 || list.Items[0].Conv != legit || list.Items[0].State != hostActive {
		t.Fatalf("alice's hosting: %+v", list.Items)
	}
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/hosting/%d", legit), "", alicesFrame("read")), 200, nil)
	if th, _ := tr.teamHost(legit); th.State != hostDropped {
		t.Fatalf("team's note after the drop: %+v", th)
	}
	queueTeam(t, tr, legit, "bob", "third time")
	hostEngine.Load().Poke(legit)
	time.Sleep(150 * time.Millisecond)
	if answered(tr, legit, "SHOULD NOT") {
		t.Fatal("BUG: a dropped conversation still ran")
	}
	serveJSON(t, h, as("DELETE", fmt.Sprintf("/hosting/%d", forged), "", alicesFrame("read")), 404, nil)
}

// TestHostedAudienceMail: the global instance's ring after a member was
// added pauses an idle hosted conversation at once (nothing to run, so no
// pass would look); a ring from anyone but the global instance is ignored.
func TestHostedAudienceMail(t *testing.T) {
	ag, _, tdb, _ := hostAgent(t)
	tr := tdb.runs
	id := hostedRun(t, tr, "bob", map[string]string{"alice": roleParticipant}, "user:alice", "legit hello")
	ag.hostSnapshot(t, tr, id)
	if e := ensureHostEngine(); e == nil {
		t.Fatal("no host engine")
	}
	waitFor(t, "the first answer", func() bool { return answered(tr, id, "ok") })
	waitQuiet(t, &Agent{eng: hostEngine.Load()})
	if _, err := tr.q.Exec(`INSERT INTO run_members (run_id, user, role, added_by, via, created) VALUES (?, 'dave', 'viewer', 'bob', 'invite', ?)`, id, now()); err != nil {
		t.Fatal(err)
	}
	ring := func(from string) {
		data, _ := json.Marshal(hostedInput{Conversation: id, Signal: "audience"})
		if err := ag.db.Tx(func(d *DB) error {
			return handleHostedInputMail(context.Background(), d, mailItem{ID: "m-" + from, From: from, Topic: topicHostedInput, Data: data})
		}); err != nil {
			t.Fatal(err)
		}
	}
	ring("user:bob")
	time.Sleep(50 * time.Millisecond)
	if row, _ := ag.db.hostedRow(id); row.State != hostActive {
		t.Fatal("a ring from someone other than the global instance was taken")
	}
	ring("global")
	waitFor(t, "the idle conversation to pause", func() bool { row, _ := ag.db.hostedRow(id); return row.State == hostPaused })
}

// TestHostEngineFencing: two host engines of one person over team (a
// blue/green pair) fence each other by the person's own epoch; another
// person's takeover — or the legacy key — touches neither.
func TestHostEngineFencing(t *testing.T) {
	ag, _ := userAgent(t)
	tdb := withTeam(t)
	other, err := openTeamSQL(tdb.path) // the successor process's own handle
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	mk := func(db *DB, key string) *Engine {
		e := newEngine(db, &Agent{db: db, noGateway: true}, fakeOf(ag), "")
		e.epochKey = "engine_epoch." + key
		e.scope = func(int64) bool { return false }
		e.takeOver()
		t.Cleanup(func() { e.Shutdown(time.Second) })
		return e
	}
	nop := func(*DB) error { return nil }
	a1 := mk(tdb.runs, "u-alice")
	if err := a1.fenced(nop); err != nil {
		t.Fatalf("the first engine: %v", err)
	}
	a2 := mk(&DB{sql: other, q: other}, "u-alice")
	if err := a1.fenced(nop); err != errFenced {
		t.Fatalf("the predecessor wrote after its successor took over: %v", err)
	}
	if err := a2.fenced(nop); err != nil {
		t.Fatalf("the successor: %v", err)
	}
	b := mk(tdb.runs, "u-bob")
	if err := a2.fenced(nop); err != nil || b.fenced(nop) != nil {
		t.Fatal("one host's takeover fenced another's engine")
	}
	if tdb.runs.getSetting("engine_epoch.u-alice") != "2" || tdb.runs.getSetting("engine_epoch") != "" {
		t.Fatal("epochs: not per host")
	}
}

// TestCopyIn: "Add a copy of my …" — a person's partition sends copies of
// their own session files to a shared conversation (the originals stay);
// the global instance keeps them under from-<person>/ with a note, for
// participants only.
func TestCopyIn(t *testing.T) {
	t.Run("partition", func(t *testing.T) {
		ag, h := userAgent(t)
		var mine Run
		serveJSON(t, h, as("POST", "/ask", `{"text":"draft the plan"}`, alicesFrame("read")), 200, &mine)
		waitStatus(t, ag.db, mine.ID, statusIdle)
		if _, err := ag.db.replPutFile(mine.ID, "plan.md", "# the plan", 0); err != nil {
			t.Fatal(err)
		}
		var posted string
		g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
			posted = path + " " + string(body)
			return 200, `{"conversation":7,"files":["from-alice/plan.md"]}`
		})
		body := fmt.Sprintf(`{"conversation":7,"files":[{"run":%d,"path":"plan.md"}]}`, mine.ID)
		viewed := alicesFrame("read")
		viewed["X-XBin-Viewed-By"] = "admin"
		serveJSON(t, h, as("POST", "/copyin", body, viewed), 403, nil)
		serveJSON(t, h, as("POST", "/copyin", fmt.Sprintf(`{"conversation":%d,"files":[{"run":%d,"path":"plan.md"}]}`, teamIDBase+1, mine.ID), alicesFrame("read")), 409, nil)
		serveJSON(t, h, as("POST", "/copyin", fmt.Sprintf(`{"conversation":7,"files":[{"run":%d,"path":"nope.md"}]}`, mine.ID), alicesFrame("read")), 404, nil)
		if len(g.got()) != 0 {
			t.Fatal("a refused copy reached the global instance")
		}
		serveJSON(t, h, as("POST", "/copyin", body, alicesFrame("read")), 200, nil)
		if !strings.HasPrefix(posted, "/runs/7/copyin ") || !strings.Contains(posted, "# the plan") {
			t.Fatalf("posted: %s", posted)
		}
		if f, err := ag.db.replFile(mine.ID, "plan.md"); err != nil || f.Content != "# the plan" {
			t.Fatal("the original changed")
		}
	})
	t.Run("global", func(t *testing.T) {
		ag, h := globalAgent(t)
		var run Run
		serveJSON(t, h, as("POST", "/ask", `{"text":"our plan","share":{"members":[{"user":"bob","role":"participant"},{"user":"carol","role":"viewer"}]}}`, f5("alice", "read")), 200, &run)
		waitStatus(t, ag.db, run.ID, statusIdle)
		body := `{"files":[{"path":"notes/plan.md","content":"# the plan"}]}`
		serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/copyin", run.ID), body, f5("dave", "read")), 404, nil)
		serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/copyin", run.ID), body, f5("carol", "read")), 403, nil)
		var out struct{ Files []string }
		serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/copyin", run.ID), body, f5("alice", "read")), 200, &out)
		if len(out.Files) != 1 || out.Files[0] != "from-alice/plan.md" {
			t.Fatalf("copied as %v", out.Files)
		}
		var files []ReplFile
		serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/files", run.ID), "", f5("carol", "read")), 200, &files)
		if len(files) != 1 || files[0].Path != "from-alice/plan.md" {
			t.Fatalf("carol sees %+v", files)
		}
		steps, _ := ag.db.steps(run.ID)
		noted := false
		for _, s := range steps {
			noted = noted || strings.Contains(s.Detail, "alice added a copy of from-alice/plan.md")
		}
		if !noted {
			t.Fatal("no note of the copy")
		}
	})
}
