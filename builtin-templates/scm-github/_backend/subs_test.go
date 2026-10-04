package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSubscriptionPersonChecks(t *testing.T) {
	ee := newEvEnv(t)
	alice := ee.user("alice").routes()
	sub := map[string]any{"repo": "acme/web", "branches": []string{fxBranch}, "prs": []int{42}, "kinds": []string{"pull", "checks.completed"}, "key": "task:1"}
	// Not signed in: a sign-in starts, as for a token.
	x := refusal(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", sub), 409, "signin")
	if x.Signin == nil || x.Signin.UserCode == "" {
		t.Fatalf("%+v", x)
	}
	ee.signIn("alice", "octocat")
	r := ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", sub)
	ok(t, r, 201)
	var v subView
	decode(t, r, &v)
	if v.For != "user:alice" || v.Repo != "acme/web" || v.Key != "task:1" || v.Expires != ee.clock.now().Add(30*24*time.Hour).UnixMilli() ||
		len(v.PRs) != 1 || strings.Join(v.Kinds, ",") != "pull,checks.completed" || !strings.HasPrefix(v.ID, "s_") {
		t.Fatalf("%+v", v)
	}
	stored := ee.global.ev().subs[v.ID]
	if stored.Person != "alice" || stored.PID != "pid-alice" || stored.Consumer != "apps/agent" {
		t.Fatalf("%+v", stored)
	}
	// The same key replaces it (200, the same id).
	r = ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web", "key": "task:1"})
	ok(t, r, 200)
	var v2 subView
	decode(t, r, &v2)
	if v2.ID != v.ID || len(v2.Branches) != 0 {
		t.Fatalf("%+v", v2)
	}
	// octocat can't read acme/ops; the App can't see acme/secret; other/web
	// isn't an account this tile serves.
	ee.gh.mu.Lock()
	ee.gh.instRepos[100] = append(ee.gh.instRepos[100], "ops")
	ee.gh.mu.Unlock()
	refusal(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/ops"}), 403, "not-allowed")
	refusal(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/secret"}), 404, "not-found")
	refusal(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "other/web"}), 403, "not-allowed")
	refusal(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web", "kinds": []string{"pull.exploded"}}), 400, "invalid")
	// The relay body is the person's own: from her frame, a consumer that
	// isn't bound here (or a person's own binding) is refused; her login is
	// global's, never the body's.
	ok(t, ee.call(ee.gH, relayC("alice", "write"), "POST", "/partition/subscriptions", map[string]any{"consumer": "apps/other-agent", "sub": map[string]any{"repo": "acme/web"}}), 201)
	for _, c := range []string{"apps/unbound", "apps/mine", ""} {
		refusal(t, ee.call(ee.gH, relayC("alice", "write"), "POST", "/partition/subscriptions", map[string]any{"consumer": c, "sub": map[string]any{"repo": "acme/web"}}), 400, "invalid")
	}
	refusal(t, ee.call(ee.gH, relayC("bob", "write"), "POST", "/partition/subscriptions", map[string]any{"consumer": "apps/agent", "login": "octocat", "sub": map[string]any{"repo": "acme/web"}}), 409, "signin")
	// A person's consumer never reaches global; an unpartitioned copy keeps
	// no one's sign-in.
	refusal(t, ee.call(ee.gH, personC("alice"), "POST", "/scm/subscriptions", sub), 403, "not-allowed")
	_, lh := newEnv(t).legacy()
	refusal(t, ee.call(lh, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web"}), 403, "identity")
	// Listing and deleting are hers alone.
	var list struct {
		Items []subView `json:"items"`
	}
	decode(t, ee.call(alice, personC("alice"), "GET", "/scm/subscriptions", nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID != v.ID {
		t.Fatalf("%+v", list)
	}
	ee.signIn("bob", "hubot")
	refusal(t, ee.call(ee.user("bob").routes(), personC("bob"), "DELETE", "/scm/subscriptions/"+v.ID, nil), 404, "not-found")
	// Another consumer in her partition neither lists nor deletes it (its
	// own, made through her frame above, it does).
	other := caller{from: "apps/other-agent", role: "consumer", partition: "user:alice", pid: "pid-alice"}
	decode(t, ee.call(alice, other, "GET", "/scm/subscriptions", nil), &list)
	if len(list.Items) != 1 || list.Items[0].ID == v.ID {
		t.Fatalf("%+v", list)
	}
	refusal(t, ee.call(alice, other, "DELETE", "/scm/subscriptions/"+v.ID, nil), 404, "not-found")
	refusal(t, ee.call(ee.gH, agentC, "DELETE", "/scm/subscriptions/"+v.ID, nil), 404, "not-found")
	ok(t, ee.call(alice, personC("alice"), "DELETE", "/scm/subscriptions/"+v.ID, nil), 204)
	decode(t, ee.call(alice, personC("alice"), "GET", "/scm/subscriptions", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("%+v", list)
	}
}

// People's subscriptions are capped per person across consumers and in
// all, below the total, so tiles' still fit.
func TestSubscriptionCaps(t *testing.T) {
	defer func(a, b int) { subsPerPerson, subsPeople = a, b }(subsPerPerson, subsPeople)
	subsPerPerson, subsPeople = 3, 4
	ee := newEvEnv(t)
	ee.signIn("alice", "octocat")
	ee.gh.mu.Lock()
	ee.gh.collab["acme/web|hubot"] = "read"
	ee.gh.mu.Unlock()
	ee.signIn("bob", "hubot")
	post := func(user, consumer, key string) *httptest.ResponseRecorder {
		return ee.call(ee.gH, relayC(user, "write"), "POST", "/partition/subscriptions", map[string]any{"consumer": consumer, "sub": map[string]any{"repo": "acme/web", "key": key}})
	}
	ok(t, post("alice", "apps/agent", "k1"), 201)
	ok(t, post("alice", "apps/agent", "k2"), 201)
	ok(t, post("alice", "apps/other-agent", "k3"), 201)
	refusal(t, post("alice", "apps/other-agent", "k4"), 429, "limit")
	ok(t, post("alice", "apps/agent", "k1"), 200) // a key's replacement always fits
	ok(t, post("bob", "apps/agent", "k1"), 201)
	refusal(t, post("bob", "apps/agent", "k2"), 429, "limit")
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "key": "tile"})
}

func TestSubscriptionTile(t *testing.T) {
	ee := newEvEnv(t)
	v := ee.subscribe(agentC, map[string]any{"repo": "acme/web", "key": "k"})
	if v.For != "global" {
		t.Fatalf("%+v", v)
	}
	if v2 := ee.subscribe(agentC, map[string]any{"repo": "acme/web", "key": "k", "issues": true}); v2.ID != v.ID || !v2.Issues {
		t.Fatalf("%+v", v2)
	}
	// The bot must see the repo; the policy's accounts and botRepos hold.
	refusal(t, ee.call(ee.gH, agentC, "POST", "/scm/subscriptions", map[string]any{"repo": "acme/secret"}), 404, "not-found")
	refusal(t, ee.call(ee.gH, agentC, "POST", "/scm/subscriptions", map[string]any{"repo": "other/web"}), 403, "not-allowed")
	p := basePolicy()
	p.BotRepos = []string{"acme/api"}
	ee.setPolicy(p)
	refusal(t, ee.call(ee.gH, agentC, "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web"}), 403, "not-allowed")
	// …and at delivery: the existing subscription no longer matches.
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	if n := len(ee.global.ev().out); n != 0 {
		t.Fatalf("%d queued outside botRepos", n)
	}
	ee.setPolicy(basePolicy())
	// Each consumer sees its own.
	var list struct {
		Items []subView `json:"items"`
	}
	decode(t, ee.call(ee.gH, agent2C, "GET", "/scm/subscriptions", nil), &list)
	if len(list.Items) != 0 {
		t.Fatalf("%+v", list)
	}
	refusal(t, ee.call(ee.gH, agent2C, "DELETE", "/scm/subscriptions/"+v.ID, nil), 404, "not-found")
	ok(t, ee.call(ee.gH, agentC, "DELETE", "/scm/subscriptions/"+v.ID, nil), 204)
	refusal(t, ee.call(ee.gH, nobodyC, "GET", "/scm/subscriptions", nil), 403, "not-allowed")
	// It lapses after 30 days unless posted again.
	v = ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ee.clock.advance(31 * 24 * time.Hour)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	if n := len(ee.global.ev().out); n != 0 {
		t.Fatalf("%d queued by a lapsed subscription", n)
	}
	ok(t, ee.call(ee.gH, cronC, "POST", "/tick", nil), 204)
	if ee.global.ev().subs[v.ID] != nil {
		t.Fatal("a lapsed subscription kept")
	}
}

func TestProgressKindsOptIn(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "key": "all"})
	ee.subscribe(agent2C, map[string]any{"repo": "acme/web", "kinds": []string{"job", "checks.completed"}, "key": "jobs"})
	ok(t, ee.hook("workflow_job", fixture(t, "workflow_job_in_progress")), 202)
	ok(t, ee.hook("check_run", fixture(t, "check_run")), 202)
	ok(t, ee.hook("workflow_run", fixture(t, "workflow_run")), 202)
	ok(t, ee.hook("pull_request", fixture(t, "pull_request_opened")), 202)
	ok(t, ee.hook("check_suite", fixture(t, "check_suite")), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	kinds := func(evs []map[string]any) string {
		var k []string
		for _, e := range evs {
			k = append(k, e["kind"].(string)+"."+e["action"].(string))
		}
		return strings.Join(k, ",")
	}
	if got := kinds(ee.agent.take()); got != "pull.opened,checks.completed" {
		t.Fatalf("every kind but progress: %s", got)
	}
	if got := kinds(ee.other.take()); got != "job.in_progress,checks.completed" {
		t.Fatalf("named kinds: %s", got)
	}
}

func TestOneDeliveryPerConsumer(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "key": "b"})
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "branches": []string{fxBranch}, "key": "a"})
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "branches": []string{"main"}, "key": "not-this"})
	ee.subscribe(agent2C, map[string]any{"repo": "acme/web", "key": "c"})
	ee.signIn("alice", "octocat")
	ok(t, ee.call(ee.user("alice").routes(), personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web", "prs": []int{42}, "key": "p"}), 201)
	ok(t, ee.hook("pull_request", fixture(t, "pull_request_synchronize")), 202)
	ee.deliver()
	got := ee.agent.take()
	if len(got) != 2 {
		t.Fatalf("%d events: %v", len(got), got)
	}
	byFor := map[string]map[string]any{}
	for _, e := range got {
		byFor[e["for"].(string)] = e
	}
	g, p := byFor["global"], byFor["user:alice"]
	if g == nil || p == nil || g["eventId"] != p["eventId"] || g["forPid"] != nil || p["forPid"] != "pid-alice" {
		t.Fatalf("%v", got)
	}
	if b, _ := json.Marshal(g["subs"]); string(b) != `["a","b"]` {
		t.Fatalf("subs: %s", b)
	}
	if b, _ := json.Marshal(p["subs"]); string(b) != `["p"]` {
		t.Fatalf("subs: %s", b)
	}
	if o := ee.other.take(); len(o) != 1 || o[0]["for"] != "global" {
		t.Fatalf("%v", o)
	}
}

// A person's identity going (Forget, or a new partition under the same
// id) takes their subscriptions and undelivered events with it.
func TestWipePersonDropsEvents(t *testing.T) {
	ee := newEvEnv(t)
	ee.signIn("alice", "octocat")
	alice := ee.user("alice").routes()
	ok(t, ee.call(alice, personC("alice"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web"}), 201)
	ee.agent.answer(503)
	ok(t, ee.hook("push", fixture(t, "push")), 202)
	ee.deliver()
	if n := len(ee.global.ev().out); n != 1 {
		t.Fatalf("%d", n)
	}
	// Re-created under the same id: another partition id.
	ee.pids["alice"] = "pid-alice-2"
	var list struct {
		Items []subView `json:"items"`
	}
	decode(t, ee.call(alice, personC("alice"), "GET", "/scm/subscriptions", nil), &list)
	if len(list.Items) != 0 || len(ee.global.ev().out) != 0 || len(ee.global.ev().subs) != 0 {
		t.Fatalf("%+v, %d items, %d subs", list, len(ee.global.ev().out), len(ee.global.ev().subs))
	}
	// Forget.
	carol := ee.signIn("carol", "octocat").routes()
	ok(t, ee.call(carol, personC("carol"), "POST", "/scm/subscriptions", map[string]any{"repo": "acme/web"}), 201)
	ok(t, ee.call(carol, pageC("carol"), "DELETE", "/scm/signin", nil), 204)
	if n := len(ee.global.ev().subs); n != 0 {
		t.Fatalf("%d subs after Forget", n)
	}
}
