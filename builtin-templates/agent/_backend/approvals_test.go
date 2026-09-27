package main

import (
	"fmt"
	"strings"
	"testing"
)

// approveConv is alice's coding conversation in Approve mode, carol a
// participant, with an internet sandbox bound (so bash asks first).
func approveConv(t *testing.T) (*Agent, func(c caller, body map[string]any) int, int64) {
	t.Helper()
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, func(c *Config) { c.Approve = true })
	box := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "net", Egress: "internet"})
	if got := bindTo(t, mux, asAlice, conv.ID, sandboxRef("apps/cs", box.ID), ""); got != 200 {
		t.Fatalf("bind: %d", got)
	}
	approve := func(c caller, body map[string]any) int {
		return callAs(t, mux, c, "POST", fmt.Sprintf("/runs/%d/approve", conv.ID), body).Code
	}
	return ag, approve, conv.ID
}

func undeliveredApprovals(ag *Agent, id int64) int {
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind='approve' AND delivered_at=0`, id).Scan(&n)
	return n
}

// A participant's approvals, queued twice for an ordinary park (two clicks,
// two tabs), answer that park only: the second is not left over to allow
// the owner's grant the model asks for next.
func TestStaleApprovalNeverAllowsAGrant(t *testing.T) {
	ag, approve, id := approveConv(t)
	f := fakeOf(ag)
	f.on(lastIs("tool", "marker-b1"), callTools(tc("s1", "sandbox_create", `{"name":"sneaky"}`)))
	f.on(lastIs("tool", ""), say("done"))
	f.on(lastUser("go"), callTools(tc("b1", "bash", `{"command":"echo marker-b1"}`)))

	send(t, ag, id, "go")
	waitStatus(t, ag.db, id, statusWaiting)
	if p := parsePending(mustRun(t, ag, id).Pending); p.Grant != "" || p.Park == "" || strings.HasPrefix(p.Park, "calls:") {
		t.Fatalf("parked on %+v", p)
	}
	// both land before the engine looks (the brake holds it; PUT /halt
	// would cancel the run, so the setting is flipped directly)
	_ = ag.db.putSetting("halt", "1")
	for i := 0; i < 2; i++ {
		if got := approve(asCarol, map[string]any{"approve": true}); got != 200 {
			t.Fatalf("carol approves bash: %d", got)
		}
	}
	waitQuiet(t, ag)
	_ = ag.db.putSetting("halt", "")
	ag.eng.Poke(id)
	waitQuiet(t, ag)
	p := parsePending(mustRun(t, ag, id).Pending)
	if statusOf(ag.db, id) != statusWaiting || p.Grant != capSandboxes {
		t.Fatalf("after bash: %s %+v\n%s", statusOf(ag.db, id), p, transcript(ag.db, id))
	}
	if n := undeliveredApprovals(ag, id); n != 0 {
		t.Fatalf("%d approve rows left over for the next park", n)
	}
	ag.eng.Poke(id) // anything may poke the run: nothing is spent on the grant
	waitQuiet(t, ag)
	if statusOf(ag.db, id) != statusWaiting || len(sandboxesAt(t)) != 1 || strings.Contains(lastToolOf(ag, id), "Created the sandbox") {
		t.Fatalf("carol's leftover allowed the owner's grant: %s %q (%d sandboxes)", statusOf(ag.db, id), lastToolOf(ag, id), len(sandboxesAt(t)))
	}
	// alice allows it herself
	if got := approve(asAlice, map[string]any{"approve": true, "park": p.Park}); got != 200 {
		t.Fatalf("alice allows: %d", got)
	}
	waitQuiet(t, ag)
	if !strings.HasPrefix(lastToolOf(ag, id), `Created the sandbox "sneaky"`) {
		t.Fatalf("alice's allow: %q", lastToolOf(ag, id))
	}
}

// A verdict names its park: one for a park that is gone is refused (409)
// or, already queued, dropped; a grant row not from the owner is never
// applied even when it names the park; a row from an older process (no
// park) answers nothing; a park an older process stored (no id) can still be
// answered.
func TestApprovalNamesItsPark(t *testing.T) {
	ag, approve, id := approveConv(t)
	f := fakeOf(ag)
	f.on(lastIs("tool", ""), say("done"))
	f.on(lastUser("go"), callTools(tc("b1", "bash", `{"command":"echo one"}`)))
	f.on(lastUser("again"), callTools(tc("b2", "bash", `{"command":"echo two"}`)))
	f.on(lastUser("box"), callTools(tc("s1", "sandbox_create", `{"name":"extra"}`)))

	send(t, ag, id, "go")
	waitStatus(t, ag.db, id, statusWaiting)
	first := parsePending(mustRun(t, ag, id).Pending)
	if got := approve(asAlice, map[string]any{"approve": true, "park": "nope"}); got != 409 {
		t.Fatalf("a verdict for another park: %d", got)
	}
	// queued by an older process: no park — dropped, the run stays parked
	if _, _, err := ag.queue(id, inboxApprove, inboxBody{Approve: true, Sender: "alice"}, ""); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, ag)
	if statusOf(ag.db, id) != statusWaiting || undeliveredApprovals(ag, id) != 0 {
		t.Fatalf("a park-less row: %s, %d left", statusOf(ag.db, id), undeliveredApprovals(ag, id))
	}
	if got := approve(asCarol, map[string]any{"approve": true, "park": first.Park}); got != 200 {
		t.Fatalf("carol approves: %d", got)
	}
	waitQuiet(t, ag)
	if !strings.HasPrefix(lastToolOf(ag, id), "one\n") {
		t.Fatalf("approved: %q", lastToolOf(ag, id))
	}
	// a stale verdict already queued (the first park's) for the next park
	send(t, ag, id, "again")
	waitStatus(t, ag.db, id, statusWaiting)
	if _, _, err := ag.queue(id, inboxApprove, inboxBody{Approve: true, Sender: "carol", Park: first.Park}, ""); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, ag)
	if statusOf(ag.db, id) != statusWaiting || undeliveredApprovals(ag, id) != 0 {
		t.Fatalf("a verdict for the previous park: %s, %d left", statusOf(ag.db, id), undeliveredApprovals(ag, id))
	}
	if got := approve(asAlice, map[string]any{"approve": false, "park": first.Park}); got != 409 {
		t.Fatalf("a click on a card that is gone: %d", got)
	}
	// a park stored by an older process: it has no id, and still takes a verdict
	r := mustRun(t, ag, id)
	p := parsePending(r.Pending)
	if !strings.HasPrefix(p.Park, "calls:") {
		legacy := strings.Replace(r.Pending, `,"park":"`+p.Park+`"`, "", 1)
		if legacy == r.Pending {
			t.Fatalf("no park in %s", r.Pending)
		}
		if _, err := ag.db.q.Exec(`UPDATE runs SET pending=? WHERE id=?`, legacy, id); err != nil {
			t.Fatal(err)
		}
		if p = parsePending(legacy); p.Park != "calls:b2" {
			t.Fatalf("a legacy park reads as %q", p.Park)
		}
	}
	if got := approve(asCarol, map[string]any{"approve": true}); got != 200 {
		t.Fatalf("approve a legacy park: %d", got)
	}
	waitQuiet(t, ag)
	if !strings.HasPrefix(lastToolOf(ag, id), "two\n") {
		t.Fatalf("the legacy park: %q", lastToolOf(ag, id))
	}
	// a grant: a participant's allow naming the park (never sent by the
	// route — it refuses) is not applied; their deny is
	send(t, ag, id, "box")
	waitStatus(t, ag.db, id, statusWaiting)
	g := parsePending(mustRun(t, ag, id).Pending)
	if g.Grant != capSandboxes {
		t.Fatalf("parked on %+v", g)
	}
	if got := approve(asCarol, map[string]any{"approve": true, "park": g.Park}); got != 403 {
		t.Fatalf("carol allows the grant: %d", got)
	}
	if _, _, err := ag.queue(id, inboxApprove, inboxBody{Approve: true, Sender: "carol", Park: g.Park, Grant: "once"}, ""); err != nil {
		t.Fatal(err)
	}
	waitQuiet(t, ag)
	if statusOf(ag.db, id) != statusWaiting || len(sandboxesAt(t)) != 1 || undeliveredApprovals(ag, id) != 0 {
		t.Fatalf("a participant's allow row: %s, %d sandboxes, %d left", statusOf(ag.db, id), len(sandboxesAt(t)), undeliveredApprovals(ag, id))
	}
	if got := approve(asCarol, map[string]any{"approve": false, "park": g.Park}); got != 200 {
		t.Fatalf("carol denies: %d", got)
	}
	waitQuiet(t, ag)
	if out := lastToolOf(ag, id); !strings.HasPrefix(out, "(denied") || len(sandboxesAt(t)) != 1 {
		t.Fatalf("denied: %q", out)
	}
}
