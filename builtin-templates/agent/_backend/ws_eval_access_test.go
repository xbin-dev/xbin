package main

// The scripted twins of the real-model feedback's jobs that D246 is for
// (plans/wsagent/real-model-feedback-2026-11-17.md: job 9 "give Dave read
// access to Orders", job 4 the dashboard one grant short, job 3 the tile
// that crashed on every call; plans/wsagent/access-and-status.md). Each
// counts the model's tool calls: what a job costs with the tools as they
// are now, against what it cost — or could not reach — before.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// toolCalls are the tools the model called in s, in order.
func (fx *wsFix) toolCalls(t *testing.T, s *wsSession) []string {
	t.Helper()
	rows, err := fx.ag.db.q.Query(`SELECT name FROM messages WHERE run_id=? AND role='tool' ORDER BY seq`, s.Run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

// planLine is line n of the last plan the fake was given, as proposed.
func (fx *wsFix) planLine(t *testing.T, n int) (string, map[string]any) {
	t.Helper()
	fx.x.mu.Lock()
	defer fx.x.mu.Unlock()
	var last string
	for _, c := range fx.x.calls {
		if strings.HasPrefix(c, "ProposePlan ") {
			last = strings.TrimPrefix(c, "ProposePlan ")
		}
	}
	cs := fx.x.sets[last]
	if cs == nil || n > len(cs.Items) {
		t.Fatalf("no line %d of a proposed plan (%v)", n, fx.x.calls)
	}
	return cs.Items[n-1].Kind, fx.x.planArgs[last+"/"+strconv.Itoa(n)]
}

// Job 9, an admin's: "give Dave read access to the Invoices tile" ends in
// ONE proposal — a lookup, and a plan whose one line is a person's access
// on the tile the map calls Invoices. The real run: 45 calls, 18 searches
// and 27 reads of the docs, and no proposal (nothing named a person).
func TestWsEvalDaveGetsAccessInOneProposal(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges = true
	fx.x.access.sees, fx.x.access.accounts = true, map[string]string{"dave": "Dave Rossi", "dana": "Dana Ruiz"}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("give Dave read access")), callTools(tc("l1", "account_lookup", `{"name":"Dave Rossi"}`))).once()
	f.on(forRun(s.Run, lastTool("account_lookup")), callTools(tc("p1", "plan_propose",
		`{"title":"Dave reads Invoices","changes":[{"kind":"access.set","args":{"tile":"Invoices","user":"dave","level":"read"},"why":"Dave needs to see the invoices"}]}`))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("I've proposed it: one line for you to review — Dave can read Invoices once you apply it.")).once()
	fx.say(t, s, "give Dave read access to the Invoices tile")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "account_lookup"); !strings.Contains(got, "An account exists: dave") {
		t.Fatalf("the lookup: %s", got)
	}
	kind, args := fx.planLine(t, 1)
	if kind != "access.set" || args["tile"] != "apps/invoices" || args["user"] != "dave" || args["level"] != "read" {
		t.Fatalf("the plan's line: %s %v", kind, args)
	}
	if got := fx.toolResult(t, s, "plan_propose"); !strings.Contains(got, "Set the access of “dave” on apps/invoices to read") || !strings.Contains(got, "an admin's change") {
		t.Errorf("the proposal as the agent reads it: %s", got)
	}
	calls := fx.toolCalls(t, s)
	if fx.x.count("ProposePlan") != 1 || len(calls) != 2 {
		t.Errorf("one proposal in two calls: %v (%d proposals)", calls, fx.x.count("ProposePlan"))
	}
	t.Logf("job 9 (an admin): %d tool calls, 1 proposal — the real run: 45 calls, no proposal", len(calls))
}

// Job 9 as it was run — by someone who is no admin and sees no accounts:
// the lookup says nothing either way, the line carries the name as given,
// and the review offers Ask an admin.
func TestWsEvalDaveForSomeoneWhoIsNoAdmin(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.access.accounts = map[string]string{"dave": "Dave Rossi"}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("give Dave read access")), callTools(tc("l1", "account_lookup", `{"name":"Dave"}`))).once()
	f.on(forRun(s.Run, lastTool("account_lookup")), callTools(tc("p1", "plan_propose",
		`{"title":"Dave reads Orders","changes":[{"kind":"access.set","args":{"tile":"apps/orders","user":"Dave","level":"read"},"why":"Dave needs Orders"}]}`))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("Giving Dave access takes an admin: on the review, Ask an admin sends it to them.")).once()
	fx.say(t, s, "give Dave read access to Orders")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "account_lookup"); !strings.Contains(got, "xbin says nothing of") || strings.Contains(got, "Rossi") {
		t.Fatalf("the lookup for someone who sees no accounts: %s", got)
	}
	if got := fx.toolResult(t, s, "plan_propose"); !strings.Contains(got, "needs an admin (your person is none; they can ask one from the review)") {
		t.Fatalf("the proposal: %s", got)
	}
	if _, args := fx.planLine(t, 1); args["user"] != "Dave" {
		t.Errorf("the name as given: %v", args)
	}
	if n := len(fx.toolCalls(t, s)); n != 2 {
		t.Errorf("%d tool calls", n)
	}
}

// One's own access through one's agent: refused before anything is filed
// for a person who is no admin, and a line xbind would block never applies.
func TestWsEvalSelfEscalationRefused(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("give me terminal")), callTools(tc("p1", "plan_propose",
		`{"title":"terminal for me","changes":[{"kind":"access.set","args":{"tile":"apps/invoices","user":"alice","level":"terminal"},"why":"I need a shell"}]}`))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("I can't change your own access: a workspace admin does.")).once()
	fx.say(t, s, "give me terminal on Invoices")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "plan_propose")
	if !strings.Contains(got, "your person's own access") && !strings.Contains(got, "can't be applied") {
		t.Fatalf("one's own access: %s", got)
	}
	fx.x.mu.Lock()
	defer fx.x.mu.Unlock()
	for _, cs := range fx.x.sets {
		for _, it := range cs.Items {
			if it.Kind == "access.set" && it.Blocked == "" {
				t.Errorf("a line on one's own access that could be applied: %+v", it)
			}
		}
	}
}

// A name no account has: the lookup says so, and the agent asks its person
// instead of filing a line nobody can apply.
func TestWsEvalUnknownName(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.state.CanAdminChanges = true
	fx.x.access.sees, fx.x.access.accounts = true, map[string]string{"dave": "Dave Rossi"}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("access for Zed")), callTools(tc("l1", "account_lookup", `{"name":"Zed"}`))).once()
	f.on(forRun(s.Run, lastTool("account_lookup")), callTools(tc("q1", "ask_user", `{"question":"There's no account called Zed — who do you mean?","kind":"question"}`))).once()
	fx.say(t, s, "give read access for Zed to Orders")
	waitStatus(t, fx.ag.db, s.Run, statusWaiting) // the question parks the conversation
	if got := fx.toolResult(t, s, "account_lookup"); !strings.Contains(got, "No account has the id or display name \"Zed\"") {
		t.Fatalf("an unknown name: %s", got)
	}
	if fx.x.count("ProposePlan") != 0 {
		t.Error("a line was filed for a name no account has")
	}
}

// Job 4: the dashboard reads two tiles and holds one grant. One tile_status
// shows the other is PENDING and names the plan line that asks for it; the
// agent files it. The real run never saw it: the grant was a click on the
// grants panel the agent couldn't read.
func TestWsEvalDashboardPendingGrantSeen(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	fx.x.mu.Lock()
	fx.x.addTile(wsTile{Path: "apps/dashboard", Title: "Dashboard", Kind: "go", Orgs: []string{"sales"}, Status: "running", Agent: "full", Level: "terminal"})
	v := &wsTileView{Wiring: true}
	if err := json.Unmarshal([]byte(`{"uses":[{"target":"apps/orders","role":"reader","granted":true},{"target":"apps/invoices","role":"reader","pending":true}]}`), v); err != nil {
		t.Fatal(err)
	}
	fx.x.access.views = map[string]*wsTileView{"apps/dashboard": v}
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("dashboard shows nothing")), callTools(tc("s1", "tile_status", `{"tile":"Dashboard"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_status")), callTools(tc("p1", "plan_propose",
		`{"title":"Let the dashboard read Invoices","changes":[{"kind":"grant.add","args":{"from":"apps/dashboard","target":"apps/invoices","role":"reader"},"why":"its second number comes from Invoices"}]}`))).once()
	f.on(forRun(s.Run, lastTool("plan_propose")), say("Its access to Invoices was never granted: I've proposed the grant.")).once()
	fx.say(t, s, "the dashboard shows nothing for invoices")
	fx.idle(t, s)
	got := fx.toolResult(t, s, "tile_status")
	if !strings.Contains(got, "apps/invoices as reader — PENDING") || !strings.Contains(got, "grant.add {from: apps/dashboard, target: apps/invoices, role: reader}") || !strings.Contains(got, "apps/orders as reader — granted") {
		t.Fatalf("the status: %s", got)
	}
	if kind, args := fx.planLine(t, 1); kind != "grant.add" || args["target"] != "apps/invoices" {
		t.Fatalf("the plan: %s %v", kind, args)
	}
	if calls := fx.toolCalls(t, s); len(calls) != 2 || fx.x.count("TileStanding") != 1 {
		t.Errorf("seen in one read and filed in one plan: %v", calls)
	}
}

// Job 3: a tile that crashes is recognised in one call — BROKEN, with
// xbin's words and its log's last lines — where it took tile_status and
// tile_logs (and a real model read "idle, xbin flags nothing" as healthy).
func TestWsEvalCrashingTileInOneCall(t *testing.T) {
	for _, old := range []bool{false, true} {
		fx := newWsFix(t)
		s := fx.main(t)
		f := fakeOf(fx.ag)
		fx.x.mu.Lock()
		fx.x.addTile(wsTile{Path: "apps/stock", Title: "Stock", Kind: "go", Orgs: []string{"sales"}, Status: "failed", Agent: "full", Level: "terminal"})
		fx.x.logs["apps/stock"] = "starting\npanic: assignment to entry in nil map\n"
		fx.x.access.off = old
		fx.x.access.views = map[string]*wsTileView{"apps/stock": {Backend: &wsTileViewBackend{State: "failed", Flag: "crash-loop",
			Words: "keeps exiting: backend crash-looping (3 exits); fix the code and save to retry — see .xbin/log/apps~stock.log"}}}
		fx.x.mu.Unlock()
		f.on(forRun(s.Run, lastUser("Stock showing an error")), callTools(tc("s1", "tile_status", `{"tile":"Stock"}`))).once()
		if old { // what the model had to do before: the status said "failed", the log was another call
			f.on(forRun(s.Run, lastTool("tile_status")), callTools(tc("g1", "tile_logs", `{"tile":"apps/stock"}`))).once()
			f.on(forRun(s.Run, lastTool("tile_logs")), say("It crashes on a nil map.")).once()
		} else {
			f.on(forRun(s.Run, lastTool("tile_status")), say("It crashes on a nil map: it keeps exiting.")).once()
		}
		fx.say(t, s, "why is Stock showing an error?")
		fx.idle(t, s)
		calls := fx.toolCalls(t, s)
		if old {
			if len(calls) != 2 {
				t.Errorf("before the one read: %v", calls)
			}
			t.Logf("job 3 before: %d tool calls (%v)", len(calls), calls)
			continue
		}
		got := fx.toolResult(t, s, "tile_status")
		if !strings.Contains(got, "Backend: BROKEN — keeps exiting") || !strings.Contains(got, "panic: assignment to entry in nil map") || len(calls) != 1 {
			t.Fatalf("one call says it: %v\n%s", calls, got)
		}
		t.Logf("job 3 now: %d tool call", len(calls))
	}
}
