package server

import (
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/delegation"
)

// A person can't ALLOW anything while their agent is stopped — paused,
// switched off, held, not given to them: a ticket or a standing approval
// made then would be live the moment the agent is back (a stop voids only
// what was approved before it). Denying always works. A stop for good
// (switched off, no longer given one, disabled) withdraws what waits, and
// the agent's backend hears of each once; a pause or a hold keeps it
// pending, to decide after resuming.
func TestDecidingWhileTheAgentIsStopped(t *testing.T) {
	w := delegateWorld(t)
	bob, page := delegateCred("d-bob"), w.cookie("bob")
	var mu sync.Mutex
	told := map[string]int{} // kind/id → notices to the backend
	w.s.HolderNotify = func(_, user, kind, id string) {
		mu.Lock()
		defer mu.Unlock()
		if user == "bob" {
			told[kind+"/"+id]++
		}
	}
	notices := func(key string) int { mu.Lock(); defer mu.Unlock(); return told[key] }
	approve := func(id, body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+id+"/approve", body, page)
		return rec.Code, rec.Body.String()
	}
	state := func(id string) delegation.Request {
		t.Helper()
		r, ok := w.store.Request("bob", id)
		if !ok {
			t.Fatalf("no request %s", id)
		}
		return r
	}
	refused := func(what string, code int, body, refusal string) {
		t.Helper()
		if code != 409 || !strings.Contains(body, `"refusal":"`+refusal+`"`) || !strings.Contains(body, "denying it works now") {
			t.Errorf("%s: %d %s, want 409 refusal %s", what, code, body, refusal)
		}
	}

	// paused: Allow, Always and a standing approval are refused; Deny goes through; it waits
	r1 := w.ask(bob, "POST", "/api/apps/x/notes", `{"text":"hi"}`)
	r2 := w.ask(bob, "POST", "/api/apps/x/notes", `{"text":"no"}`)
	if _, err := w.store.SetState("bob", func(st *delegation.State) { st.Paused = true }); err != nil {
		t.Fatal(err)
	}
	code, body := approve(r1.ID, "")
	refused("allow once, paused", code, body, "paused")
	code, body = approve(r1.ID, `{"grant":"always","scope":"tile"}`)
	refused("allow always, paused", code, body, "paused")
	rec := w.do(w.full, "POST", "/api/xbin/delegate/approvals", `{"tile":"apps/x","grant":"always","scope":"tile"}`, page)
	refused("a standing approval, paused", rec.Code, rec.Body.String(), "paused")
	if aps, _ := w.store.Approvals("bob"); len(aps) != 0 {
		t.Fatalf("a standing approval was made while paused: %+v", aps)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r2.ID+"/deny", "", page); rec.Code != 200 {
		t.Fatalf("deny while paused: %d %s", rec.Code, rec.Body.String())
	}
	if st := state(r1.ID).State; st != delegation.Pending {
		t.Fatalf("paused: the request is %s, want it still pending", st)
	}
	// resumed: the same request is decided as usual
	if _, err := w.store.SetState("bob", func(st *delegation.State) { st.Paused = false }); err != nil {
		t.Fatal(err)
	}
	if code, body := approve(r1.ID, ""); code != 200 {
		t.Fatalf("allow after resuming: %d %s", code, body)
	}

	// held (a locked vault): refused, kept pending
	r3 := w.ask(bob, "POST", "/api/apps/x/notes", `{"text":"held"}`)
	w.s.AgentHolder = func() string { return dHolder }
	held := ""
	w.s.AgentHeld = func() (string, string) { return held, "" }
	w.s.SetDelegationGate(w.s.AgentHeldGate(DelegationGate(w.store, true, false)))
	held = "locked"
	code, body = approve(r3.ID, "")
	refused("allow, held", code, body, "held")
	if st := state(r3.ID).State; st != delegation.Pending {
		t.Fatalf("held: the request is %s, want it pending", st)
	}
	held = ""

	// switched off: refused while off; what waited is withdrawn by xbin, and stays so when it is on again
	asked3 := notices("request/" + r3.ID) // (filing it told the backend too)
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/workspace", `{"enabled":false}`, w.cookie("alice")); rec.Code != 200 {
		t.Fatalf("off: %d %s", rec.Code, rec.Body.String())
	}
	if r := state(r3.ID); r.State != delegation.Withdrawn || r.WithdrawnBy != delegation.WithdrawnByXbin {
		t.Fatalf("switched off: the request is %s (by %q), want withdrawn by xbin", r.State, r.WithdrawnBy)
	}
	if n := notices("request/"+r3.ID) - asked3; n != 1 {
		t.Errorf("the backend heard of the withdrawal %d times, want once", n)
	}
	if _, err := w.store.SetWorkspace(true, "test"); err != nil {
		t.Fatal(err)
	}
	if code, body := approve(r3.ID, ""); code != 409 || strings.Contains(body, `"refusal"`) {
		t.Fatalf("a withdrawn request, switched on again: %d %s (want no longer pending)", code, body)
	}
	r4 := w.ask(bob, "POST", "/api/apps/x/notes", `{"text":"off"}`)
	if _, err := w.store.SetWorkspace(false, "test"); err != nil {
		t.Fatal(err)
	}
	code, body = approve(r4.ID, "")
	refused("allow, switched off", code, body, "workspace-off")
	if _, err := w.store.SetWorkspace(true, "test"); err != nil {
		t.Fatal(err)
	}

	// no longer given an agent: refused, and the next sweep withdraws what waits
	w.s.SetDelegationGate(AgentGivenGate(DelegationGate(w.store, true, false), w.s.Auth.Users.Given))
	code, body = approve(r4.ID, "")
	refused("allow, no agent", code, body, "no-agent")
	asked4 := notices("request/" + r4.ID)
	w.s.SweepDelegation() // bob has no access entry for the agent
	if r := state(r4.ID); r.State != delegation.Withdrawn || r.WithdrawnBy != delegation.WithdrawnByXbin {
		t.Fatalf("no agent: the request is %s (by %q), want withdrawn by xbin", r.State, r.WithdrawnBy)
	}
	w.s.SweepDelegation() // a second sweep tells nobody again
	if n := notices("request/"+r4.ID) - asked4; n != 1 {
		t.Errorf("the backend heard of the withdrawal %d times, want once", n)
	}
}
