package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The wake rule (coord_wake.go, ws_notify.go): a tile agent's news wakes
// an idle conversation at once — never after a window — and what is
// coalesced is what arrives together. Every part runs inside a window of
// ten minutes: a wake that waited for it would never come.

// The first report after an approval. The approval's outcome wakes the
// conversation (its person's act); the tile agent that then ends its turn
// wakes it again at once. This is the case that used to wait out a minute.
func TestWsWakeFirstNewsIsNotHeldBack(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	coordWakeEvery.Store(int64(10 * time.Minute))
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("start it")), callTools(
		tc("b1", "tile_agent_brief", `{"tile":"apps/orders","goal":"List what is in the tile","done_when":"the files are listed"}`),
		tc("s1", "tile_agent_start", `{"brief":1}`))).once()
	f.on(forRun(s.Run, lastTool("tile_agent_start")), say("Waiting for you to allow the start.")).once()
	f.on(forRun(s.Run, lastUser("started in apps/orders")), say("It has started.")).once()
	f.on(forRun(s.Run, lastUser("ended its turn")), say("It has reported.")).once()
	fx.say(t, s, "start it")
	fx.idle(t, s)

	fx.x.decide(fx.x.pending(t).ID, "approved")
	fx.notify(t)
	waitFor(t, "the approval's wake", func() bool { return fx.lastAssistant(t, s) == "It has started." })
	fx.idle(t, s)
	rows := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if len(rows) != 1 {
		t.Fatalf("the tile agents: %+v", rows)
	}
	// the card's place: the request that started it, and the call that filed that request
	if req := fx.ag.db.wsReqs(`WHERE run_id=?`, s.Run); len(req) != 1 || rows[0].Request != req[0].ID || rows[0].CallID != "s1" || req[0].CallID != "s1" {
		t.Fatalf("the tile agent's row names request %d, call %q; the request: %+v", rows[0].Request, rows[0].CallID, req)
	}
	var cards struct {
		Agents []struct {
			ID, Request int64
			CallID      string
		}
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/cards", s.Run), nil), 200, &cards)
	if len(cards.Agents) != 1 || cards.Agents[0].Request != rows[0].Request || cards.Agents[0].CallID != "s1" {
		t.Fatalf("the cards' tile agent: %+v, want request %d and callId s1", cards.Agents, rows[0].Request)
	}

	// seconds after that wake, the tile agent ends its turn
	fx.x.setAgent(rows[0].Session, func(a *wsAgent) { a.Status, a.Last = "idle", "Listed 3 files." })
	told := time.Now()
	fx.notify(t)
	waitFor(t, "the tile agent's wake, at once", func() bool { return fx.lastAssistant(t, s) == "It has reported." })
	if d := time.Since(told); d > 3*time.Second {
		t.Fatalf("the tile agent's ended turn woke the conversation after %s", d)
	}
	fx.idle(t, s)
	if up := fx.updates(t, s); len(up) != 2 || !strings.Contains(up[1], "tile agent #1 in apps/orders (Claude Code) ended its turn") {
		t.Fatalf("the updates: %q", up)
	}
	if n := len(f.callsFor(s.Run)); n != 4 {
		t.Fatalf("%d model calls, want 4 (the ask's two steps, and a wake each for the start and the report)", n)
	}
}

// What changes together is told together: three tile agents found changed
// by one pass are one wake and one message.
func TestWsWakeOnePassIsOneWake(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	coordWakeEvery.Store(int64(10 * time.Minute))
	var sids []string
	for _, tile := range []string{"apps/invoices", "apps/orders", "apps/calendar"} {
		_, sid := fx.seedAgent(t, s, tile)
		sids = append(sids, sid)
	}
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("All three have reported."))
	for _, sid := range sids {
		fx.x.setAgent(sid, func(a *wsAgent) { a.Status = "idle" })
	}
	fx.notify(t)
	fx.rest(t, s)
	waitFor(t, "the wake", func() bool { return fx.lastAssistant(t, s) == "All three have reported." })
	fx.idle(t, s)
	if up := fx.updates(t, s); len(up) != 1 || strings.Count(up[0], "ended its turn") != 3 {
		t.Fatalf("three changes found by one pass: %d updates:\n%s", len(up), strings.Join(up, "\n---\n"))
	}
	if n := len(f.callsFor(s.Run)); n != 1 {
		t.Fatalf("%d model calls for one pass's changes, want 1", n)
	}
}

// A storm is spaced, and only a storm: wsWakeBurst wakes inside one window
// each come at once; the next waits for the oldest of them to leave it —
// the kit's timer brings it then — and a decision of its person's still
// wakes it meanwhile.
func TestWsWakeStormIsSpaced(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	coordWakeEvery.Store(int64(10 * time.Minute))
	_, sid := fx.seedAgent(t, s, "apps/orders")
	f := fakeOf(fx.ag)
	// a request of the conversation's waits for its person all along
	fx.x.api["POST /api/apps/invoices/send"] = func(body []byte) (int, string) { return 200, `{"sent":true}` }
	f.on(forRun(s.Run, lastUser("send it")), callTools(tc("k1", "tile_call", `{"tile":"apps/invoices","method":"POST","path":"/send","body":"{}","why":"Send it"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_call")), say("It waits for your go-ahead.")).once()
	f.on(forRun(s.Run, lastUser("[workspace updates")), say("Noted."))
	fx.say(t, s, "send it")
	waitFor(t, "the request", func() bool { return fx.lastAssistant(t, s) == "It waits for your go-ahead." })
	fx.idle(t, s)
	asked := len(f.callsFor(s.Run))

	turn := func(n int) { // the tile agent works, then ends another turn
		t.Helper()
		fx.x.setAgent(sid, func(a *wsAgent) { a.Status = "running" })
		fx.notify(t)
		fx.x.setAgent(sid, func(a *wsAgent) { a.Status, a.Turn = "idle", n+1 })
		fx.notify(t)
	}
	for i := 1; i <= wsWakeBurst; i++ {
		turn(i)
		waitFor(t, fmt.Sprintf("wake %d of the burst, at once", i), func() bool { return len(f.callsFor(s.Run)) == asked+i })
		fx.rest(t, s)
	}
	// one more inside the window: owed, not woken for — a timer is armed for when the window lets it through
	turn(wsWakeBurst + 1)
	waitFor(t, "the kit's timer", func() bool {
		coordWaker.Lock()
		defer coordWaker.Unlock()
		return coordWaker.timer[s.Run] != nil
	})
	run, _ := fx.ag.db.getRun(s.Run)
	if !wsOwes(fx.ag.db, run) || len(f.callsFor(s.Run)) != asked+wsWakeBurst || statusOf(fx.ag.db, s.Run) != statusIdle {
		t.Fatalf("past the burst: owed %v, %d wakes (want %d), %s", wsOwes(fx.ag.db, run), len(f.callsFor(s.Run))-asked, wsWakeBurst, statusOf(fx.ag.db, s.Run))
	}
	// its person's decision is not held by that: it wakes the conversation now, and the news rides along
	before := len(fx.updates(t, s))
	fx.x.decide(fx.x.pending(t).ID, "approved")
	fx.notify(t)
	waitFor(t, "the decision's wake", func() bool { return len(fx.updates(t, s)) == before+1 })
	fx.rest(t, s)
	if up := fx.updates(t, s); !strings.Contains(up[before], "approved by your person") || !strings.Contains(up[before], "tile agent #1 in apps/orders (Claude Code) ended its turn") {
		t.Fatalf("the decision's update: %q", up[before])
	}
	if n := len(f.callsFor(s.Run)) - asked; n != wsWakeBurst+1 {
		t.Fatalf("%d wakes, want the burst's %d and the decision's", n, wsWakeBurst)
	}
}

// The kit's count, by itself: a profile's Limits.Wakes wakes inside one
// window, the next when the oldest left it; one wake where the profile
// names none (the projects coordinator's rule, unchanged).
func TestCoordWakeAdmitBurst(t *testing.T) {
	old := coordWakeEvery.Load()
	t.Cleanup(func() { coordWakeEvery.Store(old); coordWakerReset() })
	coordWakerReset()
	coordWakeEvery.Store(int64(10 * time.Minute))
	const a, b = int64(910001), int64(910002)
	for i := 0; i < 3; i++ {
		if !coordWakeAdmit(a, 3) {
			t.Fatalf("wake %d of a burst of 3 was held", i+1)
		}
	}
	if coordWakeAdmit(a, 3) {
		t.Fatal("a fourth wake inside the window of a burst of 3")
	}
	coordWaker.Lock()
	armed := coordWaker.timer[a] != nil
	coordWaker.Unlock()
	if !armed {
		t.Fatal("a held wake armed no timer")
	}
	if !coordWakeAdmit(b, 0) || coordWakeAdmit(b, 0) {
		t.Fatal("a profile that names no burst takes one wake a window")
	}
	coordForget(a) // its person's act: the count starts again
	if !coordWakeAdmit(a, 3) {
		t.Fatal("a wake after the count was forgotten")
	}
	// the window slides: a wake leaves it coordWakeEvery after it was taken
	coordWakerReset()
	coordWakeEvery.Store(int64(40 * time.Millisecond))
	first := time.Now()
	if !coordWakeAdmit(a, 2) || !coordWakeAdmit(a, 2) || coordWakeAdmit(a, 2) {
		t.Fatal("a burst of 2")
	}
	waitFor(t, "the oldest wake to leave the window", func() bool { return coordWakeAdmit(a, 2) })
	if d := time.Since(first); d < 40*time.Millisecond {
		t.Fatalf("a third wake %s after the first, inside the window", d)
	}
}

// A tile agent's turn that begins and ends between two reads (D238's G12):
// both reads say "idle", and only the turn's
// number moved. It is news all the same — the conversation is told, once.
// The same for a first turn that was over before the first read after the
// start (a row that was "idle" from the moment it was made).
func TestWsWakeTurnEndedBetweenTwoReads(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	coordWakeEvery.Store(int64(10 * time.Minute))
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("ended its turn")), say("Read its report."))

	row, id := fx.seedAgent(t, s, "apps/orders")
	fx.x.setAgent(id, func(a *wsAgent) { a.Status, a.Last, a.Turn = "idle", "First report.", 1 })
	fx.notify(t)
	fx.rest(t, s)
	if up := fx.updates(t, s); len(up) != 1 || !strings.Contains(up[0], "ended its turn") {
		t.Fatalf("the first turn's end: %q", up)
	}

	// a second turn — its person's message in the tile, or the agent's own —
	// starts and ends before anything reads the session again
	fx.x.setAgent(id, func(a *wsAgent) { a.Last, a.Turn = "Second report.", 2 })
	fx.notify(t)
	fx.rest(t, s)
	up := fx.updates(t, s)
	if len(up) != 2 || !strings.Contains(up[1], "ended its turn") {
		t.Fatalf("a turn that ended between two reads was never told: %q", up)
	}
	// told once: reading it again says nothing new
	fx.notify(t)
	fx.rest(t, s)
	if up := fx.updates(t, s); len(up) != 2 {
		t.Fatalf("the same turn told twice: %q", up)
	}
	if cur, _ := fx.ag.db.wsAgentOf(s.Run, row.Session); cur.ToldTurn != 2 || cur.Owed {
		t.Fatalf("the row after the telling: toldTurn %d owed %v", cur.ToldTurn, cur.Owed)
	}

	// a session that read "idle" when it was started, whose first turn was
	// over before the first read
	fx.x.mu.Lock()
	b := &wsAgent{ID: fx.x.id("s"), Tile: "apps/orders", Provider: "claude", Name: "Claude Code", Mode: "default", Asks: true, Status: "idle", Origin: "agent", Mine: true}
	fx.x.agents[b.ID] = b
	fx.x.mu.Unlock()
	fx.ag.wsAddAgent(s, &wsReq{Run: s.Run, Tile: "apps/orders"}, *b)
	fx.x.setAgent(b.ID, func(a *wsAgent) { a.Last, a.Turn = "Done at once.", 1 })
	fx.notify(t)
	fx.rest(t, s)
	if up := fx.updates(t, s); len(up) != 3 || !strings.Contains(up[2], "tile agent #2") || !strings.Contains(up[2], "ended its turn") {
		t.Fatalf("a first turn that ended before the first read was never told: %q", up)
	}
}

// told_turn is added to a database from before it, forward only: a tile
// agent that was there has been told of the turns it had (so the upgrade
// wakes nobody), and a later open leaves the column as it is.
func TestWsToldTurnFromThePreviousSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	d, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.q.Exec(`ALTER TABLE ws_agents DROP COLUMN told_turn`); err != nil { // as the previous schema had it
		t.Fatal(err)
	}
	if _, err := d.q.Exec(`INSERT INTO ws_agents (run_id, session, status, turn, told) VALUES (1, 's1', 'idle', 3, 'idle'), (1, 's2', 'running', 1, 'working')`); err != nil {
		t.Fatal(err)
	}
	_ = d.sql.Close()
	if d, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	row, err := d.wsAgentOf(1, "s1")
	if err != nil || row.ToldTurn != 3 || row.Told != "idle" || row.Owed {
		t.Fatalf("the row after the upgrade: %+v %v", row, err)
	}
	if _, err := d.q.Exec(`UPDATE ws_agents SET turn = 5 WHERE session = 's1'`); err != nil { // a turn it hasn't been told of yet
		t.Fatal(err)
	}
	_ = d.sql.Close()
	if d, err = openDB(path); err != nil {
		t.Fatal(err)
	}
	defer d.sql.Close()
	if row, _ := d.wsAgentOf(1, "s1"); row.ToldTurn != 3 || row.Turn != 5 {
		t.Fatalf("a second open rewrote the column: told %d of turn %d", row.ToldTurn, row.Turn)
	}
}
