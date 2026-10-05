package main

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWebhookHMAC(t *testing.T) {
	ee := newEvEnv(t)
	body := fixture(t, "push")
	old := ee.gh.hookSecretNow()
	if old == "" {
		t.Fatal("setup gave the hook no secret")
	}
	ok(t, ee.sendHook(ee.gH, ingress, "ping", "p-1", old, []byte(`{"zen":"Keep it logically awesome."}`)), 200)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-1", old, body), 202)
	// A wrong secret, no signature, a signature of another body, another
	// algorithm's prefix, a truncated or non-hex digest.
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-2", "not-the-secret", body), 401)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-3", "", body), 401)
	other := fixtureWith(t, "push", map[string]any{"forced": false})
	good := sign(old, body)
	for i, sig := range []string{sign(old, other), "sha1=" + strings.TrimPrefix(good, "sha256="), good[:len(good)-2], good[:len(good)-1] + "z", "sha256="} {
		ok(t, ee.sendHookSig(ee.gH, ingress, "push", fmt.Sprintf("d-3%d", i), sig, body), 401)
	}
	ok(t, ee.sendHookSig(ee.gH, ingress, "push", "d-4", strings.ToUpper(good[:7])+good[7:], body), 202) // GitHub's prefix, any case
	if n := ee.global.ev().counts.BadSig; n != 7 {
		t.Fatalf("bad signatures counted: %d", n)
	}
	// Rotated: the new secret, and for a day the previous one.
	ok(t, ee.call(ee.gH, ownerC, "POST", "/setup/app", map[string]any{"appId": ee.gh.appID, "clientId": ee.gh.clientID,
		"clientSecret": ee.gh.clientSecret, "privateKey": ee.gh.keyPEM, "webhookSecret": "rotated-webhook-secret-0123456789"}), 200)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-5", "rotated-webhook-secret-0123456789", body), 202)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-6", old, body), 202)
	ee.clock.advance(25 * time.Hour)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-7", old, body), 401)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-8", "rotated-webhook-secret-0123456789", body), 202)
	// Only ingress (or a manager testing) posts; a partition never serves it.
	refusal(t, ee.sendHook(ee.gH, agentC, "push", "d-9", old, body), 403, "not-allowed")
	ok(t, ee.sendHook(ee.gH, ownerC, "push", "d-10", "rotated-webhook-secret-0123456789", body), 202)
	refusal(t, ee.sendHook(ee.user("alice").routes(), ingress, "push", "d-11", old, body), 404, "not-found")
	// No secret at all: nothing is taken.
	_ = ee.global.vault.Delete(vaultHookSecret)
	refusal(t, ee.sendHook(ee.gH, ingress, "push", "d-12", old, body), 503, "setup")
}

// Paste keeps a new webhook secret before pointing GitHub at it: a
// delivery GitHub signs with it while the PATCH is still answering is
// taken. A PATCH GitHub refuses leaves the current secret current, and the
// previous one (from a rotation in the last day) accepted as before.
func TestWebhookSecretKeptBeforePatch(t *testing.T) {
	ee := newEvEnv(t)
	body := fixture(t, "push")
	old := ee.gh.hookSecretNow()
	const next = "rotated-webhook-secret-0123456789"
	var during int
	ee.gh.onHookPatch = func() { during = ee.sendHook(ee.gH, ingress, "push", "d-1", next, body).Code }
	paste := func(secret string) *httptest.ResponseRecorder {
		return ee.call(ee.gH, ownerC, "POST", "/setup/app", map[string]any{"appId": ee.gh.appID, "clientId": ee.gh.clientID,
			"clientSecret": ee.gh.clientSecret, "privateKey": ee.gh.keyPEM, "webhookSecret": secret})
	}
	ok(t, paste(next), 200)
	ee.gh.onHookPatch = nil
	if during != 202 {
		t.Fatalf("a delivery signed with the new secret during the PATCH: %d", during)
	}
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-2", old, body), 202) // the previous, for a day
	// GitHub refuses the next PATCH: it still signs with the current one.
	ee.gh.fail("PATCH /app/hook/config", 1, 500, nil, `{"message":"boom"}`)
	if r := paste("refused-webhook-secret-0123456789"); r.Code < 400 {
		t.Fatalf("a refused PATCH: %d", r.Code)
	}
	// …and the previous one, rotated out by the first Paste, is still
	// accepted for the rest of its day (deliveries in flight signed with it)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-2b", old, body), 202)
	ee.clock.advance(25 * time.Hour)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-3", next, body), 202)
	ok(t, ee.sendHook(ee.gH, ingress, "push", "d-4", "refused-webhook-secret-0123456789", body), 401)
}

func TestWebhookDedupe(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	sec := ee.gh.hookSecretNow()
	body := fixture(t, "push")
	r := ee.sendHook(ee.gH, ingress, "push", "dup-1", sec, body)
	if r.Code != 202 || !strings.Contains(r.Body.String(), `"queued":1`) {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	r = ee.sendHook(ee.gH, ingress, "push", "dup-1", sec, body)
	if r.Code != 202 || !strings.Contains(r.Body.String(), "duplicate") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	if n := len(ee.global.ev().out); n != 1 || ee.global.ev().counts.Duplicates != 1 {
		t.Fatalf("%d items, %+v", n, ee.global.ev().counts)
	}
	// Per installation: the same delivery id from another installation is
	// another delivery.
	ok(t, ee.sendHook(ee.gH, ingress, "push", "dup-1", sec, fixtureWith(t, "push", map[string]any{"installation.id": 101})), 202)
	if n := len(ee.global.ev().out); n != 2 {
		t.Fatalf("%d items", n)
	}
	// Each installation keeps its own 10 000 (one's traffic never evicts
	// another's), and nothing older than seven days.
	now := ee.clock.now().UnixMilli()
	for i := 0; i < seenPerInst+5; i++ {
		_ = ee.global.state.Put(seenKey(100, fmt.Sprintf("bulk-%05d", i)), now-int64(seenPerInst-i))
	}
	_ = ee.global.state.Put(seenKey(200, "old"), now-8*24*3600*1000)
	_ = ee.global.state.Put(seenKey(200, "new"), now)
	ee.global.pruneSeen(true)
	count := func(inst string) int {
		keys, _ := ee.global.state.List("seen/" + inst + "/")
		return len(keys)
	}
	if count("100") != seenPerInst || count("101") != 1 || count("200") != 1 || !ee.global.seen(200, "new") || ee.global.seen(100, "bulk-00000") ||
		!ee.global.seen(100, fmt.Sprintf("bulk-%05d", seenPerInst+4)) {
		t.Fatalf("100: %d, 101: %d, 200: %d", count("100"), count("101"), count("200"))
	}
}

// A public App can be installed by anyone: an account outside
// allowedAccounts is dropped before dedupe and normalisation.
func TestForeignAccountDropped(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	foreign := map[string]any{"repository.full_name": "evil/web", "repository.owner": map[string]any{"login": "evil"}, "installation.id": 666}
	r := ee.hook("push", fixtureWith(t, "push", foreign))
	if r.Code != 202 || !strings.Contains(r.Body.String(), "dropped") {
		t.Fatalf("%d %s", r.Code, r.Body)
	}
	// Naming acme's repo under a foreign installation's account changes nothing.
	ok(t, ee.hook("push", fixtureWith(t, "push", map[string]any{"installation.account": map[string]any{"login": "evil"}})), 202)
	keys, _ := ee.global.state.List("seen/")
	if c := ee.global.ev().counts; c.Foreign != 2 || len(ee.global.ev().out) != 0 || len(keys) != 0 {
		t.Fatalf("%+v, %d items, seen %v", c, len(ee.global.ev().out), keys)
	}
}

func TestEventsHealth(t *testing.T) {
	e := newEnv(t)
	if h := evHealth(e.global); h.Webhooks != "unknown" || h.Healthy || h.PollMinMs != 120000 {
		t.Fatalf("no App: %+v", h)
	}
	ee := &evEnv{env: e, agent: newFakeAgent(t), other: newFakeAgent(t)}
	ee.setup()
	ee.wire(e.global)
	health := func(c caller) eventsHealthInfo {
		var h helloResp
		decode(t, ee.call(ee.gH, c, "GET", "/scm/hello", nil), &h)
		return h.Events
	}
	alice := ee.user("alice").routes()
	inAlice := func() eventsHealthInfo {
		var h helloResp
		decode(t, ee.call(alice, personC("alice"), "GET", "/scm/hello", nil), &h)
		return h.Events
	}
	if h := health(agentC); h.Webhooks != "unknown" || h.Healthy {
		t.Fatalf("set up, nothing yet: %+v", h)
	}
	ok(t, ee.sendHook(ee.gH, ingress, "ping", "p-1", ee.gh.hookSecretNow(), []byte(`{}`)), 200)
	at := ee.clock.now().UnixMilli()
	if h := health(agentC); h.Webhooks != "active" || !h.Healthy || h.LastDeliveryAt != at {
		t.Fatalf("after a ping: %+v", h)
	}
	if h := inAlice(); h.Webhooks != "active" || !h.Healthy || h.LastDeliveryAt != at {
		t.Fatalf("a person's partition: %+v", h)
	}
	ok(t, ee.sendHook(ee.gH, ingress, "push", "x-1", "wrong", []byte(`{}`)), 401)
	if h := health(agentC); h.Webhooks != "active" || h.Healthy {
		t.Fatalf("a bad signature: %+v", h)
	}
	if h := inAlice(); h.Healthy {
		t.Fatalf("a person's partition: %+v", h)
	}
	ee.clock.advance(61 * time.Minute)
	if h := health(agentC); !h.Healthy {
		t.Fatalf("an hour on: %+v", h)
	}
	ee.clock.advance(24 * time.Hour)
	if h := health(agentC); h.Webhooks != "unknown" || h.Healthy {
		t.Fatalf("a day without: %+v", h)
	}
	// An App whose hook points nowhere: inactive.
	s, _ := newEnv(t).legacy()
	if h := evHealth(s); h.Webhooks != "inactive" || h.Healthy {
		t.Fatalf("no hook URL: %+v", h)
	}
	// The page shows it too.
	var pg struct {
		Events eventsHealthInfo `json:"events"`
	}
	decode(t, ee.call(ee.gH, ownerC, "GET", "/api/page", nil), &pg)
	if pg.Events.LastDeliveryAt != at {
		t.Fatalf("page: %+v", pg.Events)
	}
}

// A commit status and a check suite of one commit within five seconds
// make one checks.completed, with the worse conclusion and both halves.
func TestChecksMergeWithin5s(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "branches": []string{fxBranch}, "key": "task:1"})
	ok(t, ee.hook("check_suite", fixtureWith(t, "check_suite", map[string]any{"check_suite.conclusion": "success"})), 202)
	ee.clock.advance(2 * time.Second)
	ok(t, ee.hook("status", fixture(t, "status")), 202)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 0 {
		t.Fatalf("delivered before the window closed: %v", got)
	}
	ee.clock.advance(4 * time.Second)
	ee.deliver()
	got := ee.agent.take()
	if len(got) != 1 {
		t.Fatalf("%d events", len(got))
	}
	ev := got[0]
	runs, _ := get(ev, "data.checks.runs").([]any)
	if ev["conclusion"] != "failure" || get(ev, "data.checks.suite") != "77" || len(runs) != 1 || get(ev, "ref.pr") != float64(42) ||
		!hasSuffix(ev["topic"], "/acme/web/pull/42/checks.completed") || !hasSuffix(ev["url"], "/acme/web/pull/42/checks") {
		t.Fatalf("%v", ev)
	}
	// Past the window, another one is its own event.
	ee.clock.advance(10 * time.Second)
	ok(t, ee.hook("status", fixtureWith(t, "status", map[string]any{"context": "ci/other", "state": "success"})), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	if got := ee.agent.take(); len(got) != 1 || got[0]["conclusion"] != "success" || got[0]["eventId"] == ev["eventId"] {
		t.Fatalf("%v", got)
	}
	// The status first, then the suite: the merged event is the pull
	// request's — its topic and url too, not the status's branch and commit.
	sha2 := strings.Repeat("e", 40)
	ok(t, ee.hook("status", fixtureWith(t, "status", map[string]any{"sha": sha2, "branches": []any{map[string]any{"name": fxBranch, "commit": map[string]any{"sha": sha2}}}})), 202)
	ee.clock.advance(time.Second)
	ok(t, ee.hook("check_suite", fixtureWith(t, "check_suite", map[string]any{"check_suite.head_sha": sha2, "check_suite.conclusion": "success"})), 202)
	ee.clock.advance(5 * time.Second)
	ee.deliver()
	got = ee.agent.take()
	if len(got) != 1 || !hasSuffix(got[0]["topic"], "/acme/web/pull/42/checks.completed") || get(got[0], "ref.pr") != float64(42) ||
		!hasSuffix(got[0]["url"], "/acme/web/pull/42/checks") || got[0]["conclusion"] != "failure" {
		t.Fatalf("%v", got)
	}
}

// A status naming several branches reaches a subscription on the second
// as that branch's: its ref.branch, topic and summary say the one matched.
func TestChecksStatusBranchMatched(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web", "branches": []string{fxBranch}})
	both := []any{map[string]any{"name": "main", "commit": map[string]any{"sha": fxSHA}}, map[string]any{"name": fxBranch, "commit": map[string]any{"sha": fxSHA}}}
	ok(t, ee.hook("status", fixtureWith(t, "status", map[string]any{"branches": both})), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	got := ee.agent.take()
	if len(got) != 1 || get(got[0], "ref.branch") != fxBranch || !hasSuffix(got[0]["topic"], "/acme/web/branch/"+fxBranch+"/checks.completed") ||
		!strings.Contains(got[0]["summary"].(string), " on "+fxBranch+" ") {
		t.Fatalf("%v", got)
	}
}

// A held checks.completed a delivery pass has already taken is never
// merged into: the second half is its own event, not lost.
func TestChecksMergeInFlight(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ok(t, ee.hook("check_suite", fixtureWith(t, "check_suite", map[string]any{"check_suite.conclusion": "success"})), 202)
	// Four seconds on, the merge window is still open; the item is made due
	// (a held item first falls due as the window closes, so this is the
	// only way to reach the guard), and a delivery pass takes it and is
	// posting it…
	ee.clock.advance(4 * time.Second)
	h := ee.global.ev()
	h.mu.Lock()
	for _, it := range h.out {
		it.NextAt = ee.clock.now().UnixMilli()
	}
	h.mu.Unlock()
	posting, release := make(chan struct{}), make(chan struct{})
	post := h.post
	h.post = func(ctx context.Context, u string, body []byte) (int, error) {
		close(posting)
		<-release
		return post(ctx, u, body)
	}
	done := make(chan struct{})
	go func() { ee.deliver(); close(done) }()
	<-posting
	// …when the commit's failing status arrives.
	ok(t, ee.hook("status", fixture(t, "status")), 202)
	close(release)
	<-done
	h.post = post
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	got := ee.agent.take()
	if len(got) != 2 || got[0]["conclusion"] != "success" || got[1]["conclusion"] != "failure" {
		t.Fatalf("%d events: %v", len(got), got)
	}
}

// The suite's runs: GitHub's suite event lacks them, so they are read
// before the first attempt.
func TestChecksSuiteRuns(t *testing.T) {
	ee := newEvEnv(t)
	ee.subscribe(agentC, map[string]any{"repo": "acme/web"})
	ee.gh.mu.Lock()
	ee.gh.ci.checkRuns[fxSHA] = []map[string]any{
		{"id": 88001, "name": "test (ubuntu)", "status": "completed", "conclusion": "failure", "html_url": "https://github.com/acme/web/runs/88001", "check_suite": map[string]any{"id": 77}, "app": map[string]any{"slug": "github-actions"}},
		{"id": 88002, "name": "lint", "status": "completed", "conclusion": "success", "html_url": "https://github.com/acme/web/runs/88002", "check_suite": map[string]any{"id": 77}},
		{"id": 99, "name": "codecov", "status": "completed", "conclusion": "success", "check_suite": map[string]any{"id": 78}},
	}
	// A busy commit: another suite's hundred runs come first in the
	// commit's list — the suite's own are read from the suite.
	for i := range 100 {
		ee.gh.ci.checkRuns[fxSHA] = append([]map[string]any{{"id": 70000 + i, "name": "other", "status": "completed", "conclusion": "success", "check_suite": map[string]any{"id": 79}}}, ee.gh.ci.checkRuns[fxSHA]...)
	}
	ee.gh.mu.Unlock()
	ok(t, ee.hook("check_suite", fixture(t, "check_suite")), 202)
	ee.clock.advance(6 * time.Second)
	ee.deliver()
	got := ee.agent.take()
	if len(got) != 1 {
		t.Fatalf("%d", len(got))
	}
	runs, _ := get(got[0], "data.checks.runs").([]any)
	if len(runs) != 2 || get(runs[0].(map[string]any), "id") != "88001" || get(runs[1].(map[string]any), "conclusion") != "success" {
		t.Fatalf("%v", runs)
	}
}

// On start, what GitHub failed to deliver since the last delivery that
// arrived is redelivered — once, and never what was seen.
func TestHookCatchUp(t *testing.T) {
	ee := newEvEnv(t)
	ok(t, ee.hook("ping", []byte(`{}`)), 200)
	ee.global.markSeen(100, "seen-guid", ee.clock.now().UnixMilli())
	ee.clock.advance(time.Hour)
	at := func(d time.Duration) string { return ee.clock.now().Add(d).UTC().Format(time.RFC3339) }
	ee.gh.fail("GET /app/hook/deliveries", 1, 200, map[string]string{"Content-Type": "application/json"}, fmt.Sprintf(`[
		{"id": 1, "guid": "failed-guid", "delivered_at": %q, "status_code": 502, "installation_id": 100},
		{"id": 2, "guid": "ok-guid", "delivered_at": %q, "status_code": 202, "installation_id": 100},
		{"id": 3, "guid": "seen-guid", "delivered_at": %q, "status_code": 0, "installation_id": 100},
		{"id": 4, "guid": "old-guid", "delivered_at": %q, "status_code": 500, "installation_id": 100},
		{"id": 5, "guid": "re-guid", "delivered_at": %q, "status_code": 500, "redelivery": true, "installation_id": 100}]`,
		at(-10*time.Minute), at(-9*time.Minute), at(-8*time.Minute), at(-2*time.Hour), at(-7*time.Minute)))
	ee.gh.fail("POST /app/hook/deliveries/", 10, 202, nil, `{}`)
	n, err := ee.global.catchUp(t.Context())
	if err != nil || n != 1 || ee.gh.count("POST /app/hook/deliveries/1/attempts") != 1 || ee.gh.count("POST /app/hook/deliveries/4/attempts") != 0 {
		t.Fatalf("%d %v", n, err)
	}
}

// hasSuffix says whether a decoded field is a string ending in suffix.
func hasSuffix(v any, suffix string) bool {
	s, ok := v.(string)
	return ok && strings.HasSuffix(s, suffix)
}
