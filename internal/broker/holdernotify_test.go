package broker

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
)

// xbind's notices to a holder's backend: to the person's partition of the
// holder, on the path its table entry names; coalesced; sent again until a
// delivery is answered 2xx; and carrying kinds and ids — nothing else.
func TestHolderNotices(t *testing.T) {
	w := partRouteWS(t)
	b := w.b
	const holder = "apps/q"
	t.Cleanup(auth.TestSealDelegate(holder))
	t.Cleanup(auth.TestDelegateNotify(holder, "/agent/notices"))
	old := holderNotifyBackoff
	holderNotifyBackoff = []time.Duration{5 * time.Millisecond}
	t.Cleanup(func() { holderNotifyBackoff = old })

	type delivery struct {
		p          auth.Principal
		comp, path string
		body       string
		code       int
	}
	var mu sync.Mutex
	var got []delivery
	failing := 2 // the holder is down for the first two deliveries
	okd := make(chan struct{}, 16)
	ms := b.mail()
	ms.bellMu.Lock()
	ms.dispatch = func(_ context.Context, p auth.Principal, comp, path string, body []byte) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		code := 200
		if len(got) < failing {
			code = 503
		}
		got = append(got, delivery{p, comp, path, string(body), code})
		if code == 200 {
			okd <- struct{}{}
		}
		return code, ""
	}
	ms.bellMu.Unlock()

	b.NotifyHolder(holder, "alice", "request", "r1")
	b.NotifyHolder(holder, "alice", "request", "r1") // the same event: once
	b.NotifyHolder(holder, "alice", "state", "")
	select {
	case <-okd:
	case <-time.After(20 * time.Second):
		t.Fatalf("no delivery was answered in 20 s (%d made)", len(got))
	}
	mu.Lock()
	ds := append([]delivery(nil), got...)
	mu.Unlock()
	if len(ds) < failing+1 {
		t.Fatalf("%d deliveries: the events weren't sent again after %d failures", len(ds), failing)
	}
	last := ds[len(ds)-1]
	if last.comp != holder || last.path != "/agent/notices" || last.p.Component != partitionMailPrincipal || last.p.Role != "writer" || string(last.p.Partition) != "user:alice" || last.p.UserID != "" {
		t.Errorf("the delivery: to %s%s as %+v", last.comp, last.path, last.p)
	}
	// what was sent again is what failed: at least once — and each event once a delivery
	for i, d := range ds {
		var n struct {
			Partition string
			Events    []HolderEvent
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(d.body), &n) != nil || json.Unmarshal([]byte(d.body), &raw) != nil {
			t.Fatalf("delivery %d isn't a notice: %s", i, d.body)
		}
		if len(raw) != 2 || n.Partition != "user:alice" { // ids and kinds only: nothing else has a place in it
			t.Errorf("delivery %d carries more than a partition and events: %s", i, d.body)
		}
		seen := map[HolderEvent]int{}
		for _, ev := range n.Events {
			seen[ev]++
			if (ev != HolderEvent{Kind: "request", ID: "r1"} && ev != HolderEvent{Kind: "state"}) {
				t.Errorf("delivery %d has an event nobody queued: %+v", i, ev)
			}
		}
		if seen[HolderEvent{Kind: "request", ID: "r1"}] != 1 {
			t.Errorf("delivery %d: request r1 %d times, want once: %s", i, seen[HolderEvent{Kind: "request", ID: "r1"}], d.body)
		}
	}
	if !strings.Contains(last.body, `{"kind":"state"}`) {
		t.Errorf("the answered delivery lacks the state event: %s", last.body)
	}
	// answered: nothing waits, nothing is sent again
	deadline := time.Now().Add(10 * time.Second)
	for {
		n := b.holderNotes()
		n.mu.Lock()
		left := len(n.queues)
		n.mu.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d queues still hold events after an answered delivery", left)
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	n := len(got)
	mu.Unlock()
	// nobody but a holder with a notice path is told anything
	b.NotifyHolder("apps/x", "alice", "request", "r2") // no holder
	restore := auth.TestSealDelegate("apps/pg")        // a holder, no path
	b.NotifyHolder("apps/pg", "alice", "request", "r3")
	restore()
	b.Close()
	b.NotifyHolder(holder, "alice", "request", "r4") // after Close: nothing
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Errorf("%d more deliveries after the queue emptied: %+v", len(got)-n, got[n:])
	}
}

// The full connection view of an agent session — names of blocked tiles
// included — is read by the page of the holder it belongs to, for its
// driver, and by nobody else.
func TestConnectionViewForTheHoldersPage(t *testing.T) {
	fx := agentFixture(t)
	ps := fx.principals()
	const holder = "apps/notes"
	t.Cleanup(auth.TestSealDelegate(holder))
	if code, body := fx.agents(ps["dave"], "PUT", "/orgs/finance/agents", `{"models":"private"}`, map[string]string{"org": "finance"}); code != 200 {
		t.Fatalf("finance: %d %s", code, body)
	}
	fx.b.AgentHandle = func(user, h, id string) ([]string, []string, bool, string, bool) {
		if user == "dave" && h == holder && id == "s1" {
			return nil, nil, false, "hosted/big", true
		}
		return nil, nil, false, "", false
	}
	// the orgs a person can see (for a session's allows and taint): the
	// workspace, their own, one a tile they read is under — nothing else
	for org, want := range map[string]bool{agentpolicy.WorkspaceID: true, "finance": true, "nosuch": false, "": false} {
		if got := fx.b.AgentOrgVisible(ps["dave"], org); got != want {
			t.Errorf("dave sees org %q: %v, want %v", org, got, want)
		}
	}
	dave := ps["dave"]
	page := auth.Principal{Component: holder, Via: "frame", UserID: "dave", User: dave.User, Access: dave.Access, Gen: "s.1"}
	read := func(p auth.Principal, q string) (int, string) {
		rec := call(t, fx.b.apiAgentsConnection, p, "GET", "/agents/connection?"+q, "", nil)
		return rec.Code, rec.Body.String()
	}
	code, body := read(page, "session=s1")
	var view agentpolicy.View
	_ = json.Unmarshal([]byte(body), &view)
	blocked := false
	for _, ts := range view.Tiles {
		blocked = blocked || ts.Tile == "apps/ledger" && ts.State == agentpolicy.Blocked && strings.Contains(ts.Reason, "private models only")
	}
	if code != 200 || !blocked {
		t.Fatalf("the holder's page, for its driver: %d %s", code, body)
	}
	if code, _ := read(page, "session=nosuch"); code != 404 {
		t.Errorf("a session that isn't the driver's: %d", code)
	}
	other, dev, viewAs := page, page, page
	other.Component, dev.Deployment, viewAs.Impersonator = "apps/email", "dev", "root2"
	agent := dave
	agent.Via, agent.Agent = "delegate", holder
	for name, p := range map[string]auth.Principal{"dave himself": dave, "another tile's frame": other, "the holder's dev deployment": dev, "view-as": viewAs,
		"the agent": agent, "the holder's backend": {Component: holder, Via: "instance"}} {
		if code, body := read(p, "session=s1"); code == 200 {
			t.Errorf("%s reads the session's full view: %s", name, body)
		}
	}
}
