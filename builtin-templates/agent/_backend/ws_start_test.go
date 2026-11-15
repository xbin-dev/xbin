package main

import (
	"fmt"
	"strings"
	"testing"
)

// A tile agent's start follows xbind's dry run of it, both ways: where the
// person's preference lets starts through no request is filed and Start on
// the brief's card is the one confirmation; where xbind says one is needed
// — at the tool, at the click, or at the call itself — a request is filed
// for the chrome to open, and nothing starts before the person allows it.
func TestWsStartFollowsTheDryRun(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	base := fmt.Sprintf("/ws/sessions/%d", s.Run)
	set := func(ask bool, dry *bool) { fx.x.mu.Lock(); fx.x.startAsk, fx.x.startDry = ask, dry; fx.x.mu.Unlock() }
	no := false
	type answer struct {
		Request string
		Started bool
		ID      int64
	}
	brief := func(n int, what string) {
		t.Helper()
		f.on(forRun(s.Run, lastUser(what)), callTools(tc(fmt.Sprintf("b%d", n), "tile_agent_brief", `{"tile":"apps/orders","goal":"`+what+`","done_when":"it works"}`),
			tc(fmt.Sprintf("s%d", n), "tile_agent_start", fmt.Sprintf(`{"brief":%d}`, n)))).once()
		fx.say(t, s, what)
		fx.idle(t, s)
	}

	// the preference lets it through: nothing asked; the card's Start starts it
	set(false, nil)
	brief(1, "export orders as csv")
	if n := fx.x.count("Ask "); n != 0 {
		t.Fatalf("a request was filed where xbind needs none: %v", fx.x.calls)
	}
	if got := fx.toolResult(t, s, "tile_agent_start"); !strings.Contains(got, "pressing Start there is their go-ahead") {
		t.Fatalf("the start the preference lets through: %q", got)
	}
	var a answer
	a = answer{}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/1/start", nil), 200, &a)
	if !a.Started || a.Request != "" || fx.x.count("Ask ") != 0 || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 1 {
		t.Fatalf("Start where none is needed: %+v %v", a, fx.x.calls)
	}

	// filed while it let starts through; the person changed it before pressing Start: xbind asks
	fx.rest(t, s) // the start's outcome reaches the conversation in a turn of its own
	brief(2, "export orders as pdf")
	set(true, nil)
	a = answer{}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/2/start", nil), 200, &a)
	req := fx.x.pending(t)
	if a.Started || a.Request != req.ID || req.Kind != "session" || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 1 {
		t.Fatalf("Start where xbind now asks: %+v (pending %+v)", a, req.wsRequest)
	}
	a = answer{}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/2/start", nil), 200, &a) // pressed again: the same request
	if a.Request != req.ID || fx.x.count("Ask ") != 1 {
		t.Fatalf("Start pressed again: %+v %v", a, fx.x.calls)
	}
	fx.x.decide(req.ID, "approved")
	fx.notify(t)
	fx.rest(t, s)
	if n := len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)); n != 2 {
		t.Fatalf("%d tile agents after the approval", n)
	}

	// the dry run said none is needed, the call itself says one is: filed, not failed
	set(true, &no)
	brief(3, "export orders as xlsx")
	a = answer{}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/3/start", nil), 200, &a)
	req = fx.x.pending(t)
	if a.Started || a.Request != req.ID || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 2 {
		t.Fatalf("a start xbind refused for want of an approval: %+v", a)
	}
	if r, _ := fx.ag.db.wsReq(a.ID); r.State != "pending" || r.Ref != req.ID || r.Outcome != "" {
		t.Fatalf("its row: %+v", r)
	}

	// xbind asks from the start: the tool files the request
	set(true, nil)
	brief(4, "export orders as json")
	if got := fx.toolResult(t, s, "tile_agent_start"); strings.Contains(got, "pressing Start there is their go-ahead") || fx.x.count("Ask ") != 3 {
		t.Fatalf("a start xbind asks for: %q (%v)", got, fx.x.calls)
	}
}
