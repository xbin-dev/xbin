package main

import (
	"fmt"
	"testing"
	"time"
)

// "Workspace agent finished" (ws_done.go, xbin D241): once per finished
// request — a turn that ends with nothing of it open, after the person
// wrote — never per turn, and never while a tile agent of it works.
func TestWsDonePushOncePerRequest(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	log := withPushes(fx.ag, 0)
	wsDoneTold.Lock()
	clear(wsDoneTold.last) // run ids repeat from one test's database to the next
	wsDoneTold.Unlock()
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("how are the invoices")), say("All 3 invoices are sent.")).once()
	fx.say(t, s, "how are the invoices")
	fx.idle(t, s)
	waitFor(t, "the finished push", func() bool { return len(log.all()) == 1 })
	n := log.all()[0]
	if n.Kind != "done" || n.Link != fmt.Sprintf("#c=%d", s.Run) || n.User != "alice" {
		t.Fatalf("the push: %+v", n)
	}
	// the same request looked at again (another turn end, a wake): no second push
	fx.ag.wsDoneCheck(s.Run)
	time.Sleep(30 * time.Millisecond)
	if len(log.all()) != 1 {
		t.Fatalf("pushed again for the same request: %+v", log.all())
	}
	// a tile agent of the conversation still works: not finished
	if _, err := fx.ag.db.q.Exec(`INSERT INTO ws_agents (run_id, session, tile, status) VALUES (?, 'tsess1', 'apps/orders', 'running')`, s.Run); err != nil {
		t.Fatal(err)
	}
	f.on(forRun(s.Run, lastUser("and the orders")), say("I started a tile agent in Orders.")).once()
	fx.say(t, s, "and the orders")
	fx.idle(t, s)
	time.Sleep(30 * time.Millisecond)
	if len(log.all()) != 1 {
		t.Fatalf("a push while a tile agent works: %+v", log.all())
	}
	// it ends; the turn that reports finishes the request
	if _, err := fx.ag.db.q.Exec(`UPDATE ws_agents SET status='idle' WHERE run_id=?`, s.Run); err != nil {
		t.Fatal(err)
	}
	fx.ag.wsDoneCheck(s.Run)
	if len(log.all()) != 2 {
		t.Fatalf("the second request's push: %+v", log.all())
	}
}
