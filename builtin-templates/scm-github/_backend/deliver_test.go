package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOutboxRetryAndTick(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	if c := ee.cronLog(); len(c) != 1 || c[0] {
		t.Fatalf("at start, the cron is removed (nothing waits): %v", c)
	}
	ee.agent.answer(503, 500)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	if c := ee.cronLog(); len(c) != 2 || !c[1] {
		t.Fatalf("an item waits: the cron is registered: %v", c)
	}
	h := ee.global.ev()
	item := func() outItem {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, it := range h.out {
			return *it
		}
		return outItem{}
	}
	ee.deliver()
	if it := item(); it.Attempts != 1 || it.State != "pending" || it.NextAt != ee.clock.now().Add(10*time.Second).UnixMilli() {
		t.Fatalf("%+v", it)
	}
	ee.clock.advance(5 * time.Second)
	ee.deliver()
	ee.agent.mu.Lock()
	hits := ee.agent.hits
	ee.agent.mu.Unlock()
	if hits != 1 {
		t.Fatalf("retried early: %d", hits)
	}
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	if it := item(); it.Attempts != 2 || it.NextAt != ee.clock.now().Add(20*time.Second).UnixMilli() {
		t.Fatalf("doubling: %+v", it)
	}
	// The cron's tick delivers it; the outbox empty, the cron goes.
	ee.clock.advance(21 * time.Second)
	ok(t, ee.call(ee.gH, cronC, "POST", "/tick", nil), 204)
	if got := ee.agent.take(); len(got) != 1 || item().State != "delivered" {
		t.Fatalf("%v %+v", got, item())
	}
	if c := ee.cronLog(); len(c) != 3 || c[2] {
		t.Fatalf("nothing waits: the cron is removed: %v", c)
	}
	// The doubling stops at an hour.
	if retryDelay(1) != 10*time.Second || retryDelay(3) != 40*time.Second || retryDelay(12) != time.Hour || retryDelay(40) != time.Hour {
		t.Fatal(retryDelay(12))
	}
	// 404: dropped and counted.
	ee.agent.answer(404)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	if c := h.counts; c.NotFound != 1 || c.Delivered != 1 || len(h.out) != 1 {
		t.Fatalf("%+v, %d items", c, len(h.out))
	}
	// Never taken for a day: dropped and counted.
	ee.agent.answer(500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500, 500)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	for i := 0; i < 30; i++ {
		ee.deliver()
		ee.clock.advance(time.Hour)
	}
	ok(t, ee.call(ee.gH, cronC, "POST", "/tick", nil), 204)
	if c := h.counts; c.Expired != 1 || h.pending() {
		t.Fatalf("%+v", c)
	}
	// An unbound consumer is retried (the binding may come back).
	ee.subscribe(caller{from: "apps/unbound", role: "consumer"}, map[string]any{"repo": "acme/web"})
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	h.mu.Lock()
	var last string
	for _, it := range h.out {
		if it.Consumer == "apps/unbound" {
			last = it.Last
		}
	}
	h.mu.Unlock()
	if last != "unbound" {
		t.Fatalf("%q", last)
	}
	// Only xbind's cron (or the tile, the owner) ticks.
	refusal(t, ee.call(ee.gH, agentC, "POST", "/tick", nil), 403, "not-allowed")
}

// The outbox and subscriptions outlive a restart.
func TestOutboxSurvivesRestart(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ee.agent.answer(503)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	s2 := newSrv("global", tilePath, ee.global.state, ee.conf, ee.global.vault, ee.gh.srv.Client(), ee.clock.now)
	s2.defaultAPI, s2.defaultWeb = ee.gh.srv.URL, ee.gh.srv.URL
	ee.cron = nil
	ee.wire(s2)
	if c := ee.cronLog(); len(c) != 1 || !c[0] {
		t.Fatalf("an item waits after a restart: the cron is registered: %v", c)
	}
	ee.clock.advance(11 * time.Second)
	s2.ev().deliverDue(t.Context())
	if got := ee.agent.take(); len(got) != 1 || len(s2.ev().subs) != 1 {
		t.Fatalf("%v", got)
	}
}

func TestEventsCursor(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ee.subscribe(agentC, map[string]any{"repo": "acme/api"})
	ee.signIn("alice", "octocat")
	alice := ee.user("alice").routes()
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web"}), 201)
	start := ee.clock.now().UnixMilli()
	for i := 0; i < 4; i++ {
		ok(t, ee.hook("push", fixture(t, "push")), 202)
		ee.clock.advance(time.Second)
	}
	api := map[string]any{"repository.full_name": "acme/api", "repository.name": "api"}
	ok(t, ee.hook("push", fixtureWith(t, "push", api)), 202)
	ee.agent.answer(503) // one still due
	ee.deliver()
	type pg struct {
		Items []map[string]any `json:"items"`
		Next  string           `json:"next"`
		Etag  string           `json:"etag"`
	}
	var all []string
	cur := ""
	for i := 0; ; i++ {
		var p pg
		q := "/scm/events?limit=2"
		if cur != "" {
			q += "&cursor=" + cur
		}
		decode(t, ee.call(ee.gH, agentC, "GET", q, nil), &p)
		for _, e := range p.Items {
			if e["for"] != "global" {
				t.Fatalf("%v", e)
			}
			all = append(all, e["eventId"].(string))
		}
		if p.Next == "" {
			break
		}
		if len(p.Items) != 2 || i > 5 {
			t.Fatalf("page %d: %d items", i, len(p.Items))
		}
		cur = p.Next
	}
	// Five, oldest first: delivered or due alike.
	if len(all) != 5 || all[0] >= all[1] {
		t.Fatalf("%v", all)
	}
	var p pg
	decode(t, ee.call(ee.gH, agentC, "GET", "/scm/events?repo=acme/api", nil), &p)
	if len(p.Items) != 1 || p.Items[0]["repo"] != "acme/api" {
		t.Fatalf("%v", p.Items)
	}
	decode(t, ee.call(ee.gH, agentC, "GET", fmt.Sprintf("/scm/events?since=%d", start+2500), nil), &p)
	if len(p.Items) != 2 {
		t.Fatalf("since: %d", len(p.Items))
	}
	// Each consumer and person sees their own.
	decode(t, ee.call(ee.gH, agent2C, "GET", "/scm/events", nil), &p)
	if len(p.Items) != 0 {
		t.Fatalf("%v", p.Items)
	}
	decode(t, ee.call(alice, personC("alice"), "GET", "/scm/events?limit=100", nil), &p)
	if len(p.Items) != 4 || p.Items[0]["for"] != "user:alice" || p.Items[0]["forPid"] != "pid-alice" {
		t.Fatalf("alice: %d %v", len(p.Items), p.Items)
	}
	refusal(t, ee.call(ee.gH, agentC, "GET", "/scm/events?cursor=!!", nil), 400, "invalid")
	refusal(t, ee.call(ee.gH, agentC, "GET", "/scm/events?limit=101", nil), 400, "invalid")
	// Seven days on, gone.
	ee.clock.advance(8 * 24 * time.Hour)
	decode(t, ee.call(ee.gH, agentC, "GET", "/scm/events", nil), &p)
	if len(p.Items) != 0 {
		t.Fatalf("%d after a week", len(p.Items))
	}
}

func TestDeliveryRechecksAccess(t *testing.T) {
	ee := newEvEnv(t)
	ee.signIn("alice", "octocat")
	alice := ee.user("alice").routes()
	sub := map[string]any{"repo": "acme/web", "key": "task:1"}
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", sub), 201)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 1 || got[0]["for"] != "user:alice" || got[0]["private"] != true {
		t.Fatalf("%v", got)
	}
	setPerm := func(p string) {
		ee.gh.mu.Lock()
		ee.gh.collab["acme/web|octocat"] = p
		ee.gh.mu.Unlock()
	}
	// Access lost on GitHub: the hour's cache still says read…
	setPerm("none")
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 1 {
		t.Fatalf("cached: %d", len(got))
	}
	// …until a member webhook drops it: no further private-repo event, her
	// subscriptions on the repo deleted.
	var repo map[string]any
	_ = json.Unmarshal(fixtureWith(t, "push", nil), &repo)
	member, _ := json.Marshal(map[string]any{"action": "removed", "member": map[string]any{"login": "octocat"},
		"repository": repo["repository"], "installation": map[string]any{"id": 100}})
	ok(t, ee.hook("member", member), 202)
	if ee.global.state.Get(accessKey("octocat", "acme/web"), &accessRec{}) == nil {
		t.Fatal("the member event kept the cached access")
	}
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	h := ee.global.ev()
	if got := ee.agent.take(); len(got) != 0 || len(h.subs) != 0 || h.counts.AccessLost != 1 || len(h.out) != 2 {
		t.Fatalf("%d events, %d subs, %+v, %d items", len(got), len(h.subs), h.counts, len(h.out))
	}
	// A membership webhook (a team, an org) drops it too.
	setPerm("read")
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", sub), 201)
	setPerm("none")
	membership, _ := json.Marshal(map[string]any{"action": "removed", "scope": "team", "member": map[string]any{"login": "octocat"},
		"organization": map[string]any{"login": "acme"}, "installation": map[string]any{"id": 100}})
	ok(t, ee.hook("membership", membership), 202)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 0 || len(h.subs) != 0 || h.counts.AccessLost != 2 {
		t.Fatalf("membership: %d events, %d subs", len(got), len(h.subs))
	}
	// A public repo's events aren't checked again.
	setPerm("read")
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", sub), 201)
	setPerm("none")
	ok(t, ee.hook("organization", []byte(`{"action":"member_removed","membership":{"user":{"login":"octocat"}},"organization":{"login":"acme"},"installation":{"id":100}}`)), 202)
	ok(t, ee.hook("push", fixtureWith(t, "push", map[string]any{"repository.private": false})), 202)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 1 || got[0]["private"] != false {
		t.Fatalf("public: %v", got)
	}
	// Nothing in an event names a credential.
	for _, r := range ee.seen {
		if strings.Contains(r.body, ee.gh.clientSecret) || strings.Contains(r.body, ee.gh.hookSecretNow()) || strings.Contains(r.body, "ghs_") || strings.Contains(r.body, "ghu_") {
			t.Fatalf("%s %s: %s", r.method, r.path, r.body)
		}
	}
}

// GET /scm/events for a person re-checks a private repo's access as a
// delivery does: lost, its items aren't listed, the pending ones dropped
// and their subscriptions there deleted.
func TestEventsRechecksAccess(t *testing.T) {
	ee := newEvEnv(t)
	ee.signIn("alice", "octocat")
	alice := ee.user("alice").routes()
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web", "key": "task:1"}), 201)
	ee.agent.answer(500, 500, 500, 500)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ok(t, ee.hook("push", fixtureWith(t, "push", map[string]any{"repository.private": false})), 202)
	ee.deliver() // the consumer is failing: both stay pending
	list := func() []json.RawMessage {
		var p page[json.RawMessage]
		r := ee.call(alice, personC("alice"), "GET", "/scm/events", nil)
		ok(t, r, 200)
		decode(t, r, &p)
		return p.Items
	}
	if n := len(list()); n != 2 {
		t.Fatalf("%d listed", n)
	}
	ee.gh.mu.Lock()
	ee.gh.collab["acme/web|octocat"] = "none"
	ee.gh.mu.Unlock()
	ee.clock.advance(61 * time.Minute) // past the access cache
	items := list()
	h := ee.global.ev()
	if len(items) != 1 || !strings.Contains(string(items[0]), `"private":false`) || len(h.subs) != 0 || h.counts.AccessLost != 1 || len(h.out) != 1 {
		t.Fatalf("%d listed, %d subs, %+v, %d items", len(items), len(h.subs), h.counts, len(h.out))
	}
	// GitHub not answering refuses the listing rather than show it.
	ee.gh.mu.Lock()
	ee.gh.collab["acme/web|octocat"] = "read"
	ee.gh.mu.Unlock()
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web", "key": "task:1"}), 201)
	ee.agent.answer(500)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	ee.clock.advance(61 * time.Minute)
	ee.gh.fail("/collaborators/octocat/permission", 10, 502, nil, `{}`)
	refusal(t, ee.call(alice, personC("alice"), "GET", "/scm/events", nil), 503, "unavailable")
}

// One consumer failing for a day fills its own share of the outbox, never
// another consumer's room.
func TestOutboxPerConsumerCap(t *testing.T) {
	defer func(n int) { outboxPerConsumer = n }(outboxPerConsumer)
	outboxPerConsumer = 3
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ee.subscribe(agent2C, map[string]any{"repo": "acme/web"})
	ee.agent.answer(500, 500, 500, 500, 500, 500)
	for i := 0; i < 5; i++ {
		ok(t, ee.hook("push", fixture(t, "push")), 202)
		ee.deliver()
	}
	h := ee.global.ev()
	pend := 0
	for _, it := range h.out {
		if it.State == "pending" {
			pend++
			if it.Consumer != "apps/agent" {
				t.Fatalf("%+v", it)
			}
		}
	}
	if got := ee.other.take(); pend != 3 || len(got) != 5 || h.counts.Overflow != 2 {
		t.Fatalf("pending %d, the other consumer got %d, %+v", pend, len(got), h.counts)
	}
}
