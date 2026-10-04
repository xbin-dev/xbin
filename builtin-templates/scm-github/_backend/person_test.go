package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func personToken(t *testing.T, e *env, u http.Handler, body map[string]any) map[string]any {
	t.Helper()
	r := e.call(u, personC("alice"), "POST", "/scm/token", body)
	ok(t, r, 200)
	var m map[string]any
	decode(t, r, &m)
	return m
}

func (e *env) userTok(tok string) *fUserTok {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	return e.gh.userTokens[tok]
}

// A refresh is the partition's own (no client secret: a device-flow
// token), and GitHub's rotation retires the old pair.
func TestRefreshDirectRotates(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	acc0, ref0, _ := s.userTokens()
	first := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"})
	// Within the epoch: the cached scoped token, no refresh.
	e.clock.advance(5 * time.Hour)
	if again := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"}); again["token"] != first["token"] {
		t.Fatal("re-scoped within the epoch")
	}
	// Less than 15 minutes left in the epoch (8 h − 60 min): refresh.
	e.clock.advance(106 * time.Minute)
	next := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"})
	if next["token"] == first["token"] {
		t.Fatal("no new token after the epoch")
	}
	acc1, ref1, _ := s.userTokens()
	if acc1.Token == acc0.Token || ref1.Token == ref0.Token {
		t.Fatal("the pair didn't rotate")
	}
	if !e.userTok(acc0.Token).revoked {
		t.Fatal("the old access token still works at GitHub")
	}
	if rec := s.personRecord(); rec.Epoch != 2 {
		t.Fatalf("epoch %d", rec.Epoch)
	}
	if e.gh.count("POST /login/oauth/access_token") < 2 {
		t.Fatal("no refresh call")
	}
	// The fake refuses a refresh carrying a client secret: this one had none.
	e.gh.mu.Lock()
	_, oldStill := e.gh.refresh[ref0.Token]
	e.gh.mu.Unlock()
	if oldStill {
		t.Fatal("the old refresh token still works")
	}
	// A 401 on a read refreshes once and retries.
	e.gh.fail("GET /repos/acme/web/pulls/", 1, 401, nil, `{"message":"Bad credentials"}`)
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"] = []*fPull{{Number: 7, Title: "t", Head: "feature", Base: "main", State: "open", User: "octocat"}}
	e.gh.mu.Unlock()
	ok(t, e.call(u, personC("alice"), "GET", "/scm/pulls/7?repo=acme/web", nil), 200)
	if rec := s.personRecord(); rec.Epoch != 3 {
		t.Fatalf("no refresh on 401: epoch %d", rec.Epoch)
	}
}

// A person token expires at the epoch's end — its parent's expiry less an
// hour — so every token of the epoch rotates together; personTtlMin caps it.
func TestEpochCapsExpiry(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	rec := s.personRecord()
	m := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read"})
	exp := int64(m["expiresAt"].(float64))
	if want := time.UnixMilli(rec.ExpiresAt).Add(-time.Hour).UnixMilli(); exp != want {
		t.Fatalf("expiresAt %d, want %d (parent %d)", exp, want, rec.ExpiresAt)
	}
	if ra := int64(m["refreshAfter"].(float64)); ra != exp-int64(10*time.Minute/time.Millisecond) {
		t.Fatalf("refreshAfter %d", ra)
	}
	if m["author"].(map[string]any)["email"] != "583231+octocat@users.noreply.127.0.0.1" {
		t.Fatalf("author %v", m["author"])
	}
	p := basePolicy()
	p.PersonTTLMin = 30 // under maxTtlSec: a consumer's minTtlSec could go unmet
	refusal(t, e.call(e.gH, ownerC, "PUT", "/api/policy", p), 400, "invalid")
	p.PersonTTLMin = 55
	e.setPolicy(p)
	m = personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read", "purpose": "capped", "minTtlSec": 3000})
	if exp := int64(m["expiresAt"].(float64)); exp != e.clock.now().Add(55*time.Minute).UnixMilli() {
		t.Fatalf("personTtlMin: %d", exp)
	}
	if ra := int64(m["refreshAfter"].(float64)); ra != e.clock.now().Add(45*time.Minute).UnixMilli() {
		t.Fatalf("refreshAfter %d", ra)
	}
	// A value kept from before the floor is the floor.
	p.PersonTTLMin = 20
	if err := e.global.state.Put("policy", p); err != nil {
		t.Fatal(err)
	}
	m = personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read", "purpose": "old"})
	if exp := int64(m["expiresAt"].(float64)); exp != e.clock.now().Add(50*time.Minute).UnixMilli() {
		t.Fatalf("personTtlMin below the floor: %d", exp)
	}
}

// A refresh token GitHub no longer takes: the sign-in is cleared and a new
// one started.
func TestBadRefreshNeedsSignin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	e.gh.mu.Lock()
	e.gh.refresh = map[string]string{}
	e.gh.mu.Unlock()
	e.clock.advance(7 * time.Hour)
	x := refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 409, "signin")
	if x.Signin == nil {
		t.Fatal("no sign-in started")
	}
	if s.personRecord() != nil {
		t.Fatal("the sign-in is still there")
	}
	if _, _, err := s.userTokens(); err == nil {
		t.Fatal("the pair is still in the vault")
	}
}

// Scoping is global's: basic auth with the client secret, the target and
// repositories asked, permissions global computed.
func TestScopedViaGlobal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	m := personToken(t, e, s.routes(), map[string]any{"repos": []string{"acme/web"}, "access": "write", "permissions": map[string]string{"actions": "none"}})
	e.gh.mu.Lock()
	req := e.gh.scopeReq[len(e.gh.scopeReq)-1]
	e.gh.mu.Unlock()
	perms, _ := json.Marshal(req["permissions"])
	want := presetWrite()
	delete(want, "actions")
	wantJ, _ := json.Marshal(want)
	if req["target"] != "acme" || strings.Join(toStrings(req["repositories"]), ",") != "web" || string(perms) != string(wantJ) {
		t.Fatalf("scope request: %v", req)
	}
	tok := e.userTok(m["token"].(string))
	if tok == nil || !tok.scoped || tok.login != "octocat" {
		t.Fatalf("not a scoped token: %+v", tok)
	}
	// Global keeps nothing of the access token.
	acc, _, _ := s.userTokens()
	for _, k := range mustKeys(e.global.state.(*memKV)) {
		var raw json.RawMessage
		e.global.state.Get(k, &raw)
		if strings.Contains(string(raw), acc.Token) || strings.Contains(string(raw), m["token"].(string)) {
			t.Fatalf("global's %s holds a token", k)
		}
	}
}

func toStrings(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func mustKeys(k *memKV) []string {
	keys, _ := k.List("")
	return keys
}

// The relay is a person's own partition's only.
func TestRelayRefusesOthers(t *testing.T) {
	e := newEnv(t)
	e.setup()
	body := map[string]string{"accessToken": "ghu_x"}
	for _, c := range []caller{agentC, personC("alice"), ownerC, selfC, ingress, nobodyC, cronC} {
		for _, p := range []string{"/partition/identity", "/partition/scope", "/partition/revoke-token", "/partition/revoke-grant", "/partition/check-token", "/partition/bot-token"} {
			if r := e.call(e.gH, c, "POST", p, body); r.Code != 403 {
				t.Fatalf("%+v %s: %d", c, p, r.Code)
			}
		}
	}
	// Alice's partition can't scope bob's token: it isn't her registered sign-in.
	alice := e.signIn("alice", "octocat")
	bob := e.signIn("bob", "hubot")
	bobAcc, _, _ := bob.userTokens()
	x := refusal(t, e.call(e.gH, relayC("alice", "write"), "POST", "/partition/scope", map[string]any{
		"accessToken": bobAcc.Token, "owner": "acme", "repos": []string{"acme/web"}, "access": "read"}), 403, "identity")
	_ = x
	aliceAcc, _, _ := alice.userTokens()
	ok(t, e.call(e.gH, relayC("alice", "write"), "POST", "/partition/scope", map[string]any{
		"accessToken": aliceAcc.Token, "owner": "acme", "repos": []string{"acme/web"}, "access": "read"}), 200)
}

// Forget: the grant revoked at GitHub (every token of it), the sign-in
// cleared, global's identity dropped.
func TestForgetRevokesGrant(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	m := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read"})
	acc, _, _ := s.userTokens()
	// GitHub refuses the grant's revocation: Forget says so and keeps
	// everything, so it can be tried again.
	e.gh.fail("DELETE /applications/", 1, 500, nil, `{"message":"boom"}`)
	refusal(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 503, "unavailable")
	if a2, _, _ := s.userTokens(); a2.Token != acc.Token || s.personRecord() == nil || e.global.ident("alice") == nil {
		t.Fatal("a failed Forget cleared the sign-in")
	}
	if e.userTok(acc.Token).revoked {
		t.Fatal("the fake revoked it anyway: the test proves nothing")
	}
	// GitHub's 422 ("validation failed, or the endpoint has been spammed")
	// with the token still alive isn't "already gone": GitHub is asked,
	// and the sign-in kept.
	e.gh.fail("DELETE /applications/", 1, 422, nil, `{"message":"Validation Failed"}`)
	checks := e.gh.count("POST /applications/" + e.gh.clientID + "/token")
	refusal(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 503, "unavailable")
	if a2, _, _ := s.userTokens(); a2.Token != acc.Token || s.personRecord() == nil || e.global.ident("alice") == nil || e.userTok(acc.Token).revoked {
		t.Fatal("a 422 with the token alive cleared the sign-in")
	}
	if e.gh.count("POST /applications/"+e.gh.clientID+"/token") == checks {
		t.Fatal("a 422 didn't ask GitHub whether the token is alive")
	}
	// The check's own 422 (the same "spammed") isn't "GitHub no longer
	// knows it": only its 404 is.
	e.gh.fail("DELETE /applications/", 1, 422, nil, `{"message":"Validation Failed"}`)
	e.gh.fail("POST /applications/", 1, 422, nil, `{"message":"Validation Failed"}`)
	refusal(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 503, "unavailable")
	if a2, _, _ := s.userTokens(); a2.Token != acc.Token || s.personRecord() == nil || e.global.ident("alice") == nil || e.userTok(acc.Token).revoked {
		t.Fatal("a 422 from both the revoke and the check cleared the sign-in")
	}
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if !e.userTok(acc.Token).revoked || !e.userTok(m["token"].(string)).revoked {
		t.Fatal("tokens still work at GitHub")
	}
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("the identity is still kept")
	}
	var h helloResp
	decode(t, e.call(u, personC("alice"), "GET", "/scm/hello", nil), &h)
	if h.You.Person != nil {
		t.Fatal("hello still names the person")
	}
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 409, "signin")
	// A 422 for a token GitHub no longer knows (the fake's answer for one
	// it never issued) is gone enough: Forget clears the sign-in.
	s = e.signIn("alice", "octocat")
	u = s.routes()
	acc, _, _ = s.userTokens()
	e.gh.mu.Lock()
	delete(e.gh.userTokens, acc.Token)
	e.gh.mu.Unlock()
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("a token GitHub no longer knows kept the sign-in")
	}
}

// Forget after the access token expired (8 h) with the sign-in itself (the
// refresh token, ~6 months) alive: the pair is refreshed first and the
// grant revoked with the new token — not "already gone".
func TestForgetRefreshesExpiredToken(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	m := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read"})
	liveRefresh := func() int {
		e.gh.mu.Lock()
		defer e.gh.mu.Unlock()
		n := 0
		for _, l := range e.gh.refresh {
			if l == "octocat" {
				n++
			}
		}
		return n
	}
	e.clock.advance(9 * time.Hour)
	// A refresh GitHub fails (not bad_refresh_token) is answered, and
	// nothing is cleared.
	e.gh.fail("POST /login/oauth/access_token", 1, 503, nil, `boom`)
	if r := e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil); r.Code < 400 {
		t.Fatalf("a failed refresh: %d", r.Code)
	}
	if s.personRecord() == nil || e.global.ident("alice") == nil || liveRefresh() == 0 {
		t.Fatal("a failed refresh cleared the sign-in")
	}
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if n := liveRefresh(); n != 0 {
		t.Fatalf("the grant is still authorised at GitHub: %d live refresh tokens", n)
	}
	e.gh.mu.Lock()
	for tok, x := range e.gh.userTokens {
		if x.login == "octocat" && !x.revoked && e.clock.now().Before(x.exp) {
			e.gh.mu.Unlock()
			t.Fatalf("token %s… still works at GitHub", tok[:8])
		}
	}
	e.gh.mu.Unlock()
	if !e.userTok(m["token"].(string)).revoked {
		t.Fatal("the scoped token wasn't revoked with the grant")
	}
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("the identity is still kept")
	}
	// bad_refresh_token: nothing is left to revoke with — Forget clears.
	s = e.signIn("alice", "octocat")
	u = s.routes()
	e.clock.advance(9 * time.Hour)
	e.gh.mu.Lock()
	for rt, l := range e.gh.refresh {
		if l == "octocat" {
			delete(e.gh.refresh, rt)
		}
	}
	e.gh.mu.Unlock()
	ok(t, e.call(u, pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	if s.personRecord() != nil || e.global.ident("alice") != nil {
		t.Fatal("a sign-in GitHub no longer refreshes was kept")
	}
}

// Global learns a person's login from GitHub's answer for a token only
// this App issued — never from GET /user.
func TestIdentityRegistrationVerifiedAtGlobal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.signIn("alice", "octocat")
	if e.gh.count("POST /applications/"+e.gh.clientID+"/token") == 0 {
		t.Fatal("global didn't check the token with GitHub")
	}
	if id := e.global.ident("alice"); id == nil || id.Login != "octocat" || id.PID != "pid-alice" {
		t.Fatalf("ident: %+v", id)
	}
	// The relay names no login: the token decides. Alice registering a
	// token of hubot's makes her hubot — that's a sign-in she holds.
	e.gh.mu.Lock()
	tok := e.gh.newUser("hubot")["access_token"].(string)
	e.gh.mu.Unlock()
	var a account
	decode(t, e.call(e.gH, relayC("alice", "write"), "POST", "/partition/identity", map[string]any{"accessToken": tok, "login": "octocat"}), &a)
	if a.Login != "hubot" || e.global.ident("alice").Login != "hubot" {
		t.Fatalf("registered %+v", a)
	}
}

// A PAT or another App's token isn't a sign-in of this App: 403 identity.
func TestIdentityRequiresAppToken(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, tok := range []string{"ghp_personalAccessToken0123456789", "ghu_anotherAppsToken0123456789", ""} {
		r := e.call(e.gH, relayC("alice", "write"), "POST", "/partition/identity", map[string]string{"accessToken": tok})
		if tok == "" {
			refusal(t, r, 400, "invalid")
			continue
		}
		refusal(t, r, 403, "identity")
	}
	if e.global.ident("alice") != nil {
		t.Fatal("registered anyway")
	}
}

// The person's own frame can call the relay with any body: global checks
// every field against the policy itself.
func TestRelayRevalidatesPolicy(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	acc, _, _ := s.userTokens()
	scope := func(owner string, repos []string, access string, perms map[string]string) map[string]any {
		return map[string]any{"accessToken": acc.Token, "owner": owner, "repos": repos, "access": access, "permissions": perms}
	}
	frame := relayC("alice", "write")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("acme", []string{"acme/web"}, "write", map[string]string{"issues": "write"})), 400, "invalid")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("acme", []string{"acme/web"}, "write", map[string]string{"administration": "write"})), 400, "invalid")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("acme", []string{"acme/web"}, "write", map[string]string{"workflows": "write"})), 403, "not-allowed")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("evil", []string{"evil/x"}, "read", nil)), 403, "not-allowed")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("acme", []string{"evil/x"}, "read", nil)), 400, "invalid")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/scope", scope("acme", []string{"acme/web"}, "owner", nil)), 400, "invalid")
	p := basePolicy()
	p.BotForPeople, p.BotRepos = "on", []string{"acme/api"}
	e.setPolicy(p)
	refusal(t, e.call(e.gH, frame, "POST", "/partition/bot-token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	refusal(t, e.call(e.gH, frame, "POST", "/partition/bot-token", map[string]any{"repo": "acme/api", "access": "write", "permissions": map[string]string{"workflows": "write"}}), 403, "not-allowed")
	ok(t, e.call(e.gH, frame, "POST", "/partition/bot-token", map[string]any{"repo": "acme/api", "access": "read"}), 200)
}

// A bot token a person's partition got through the relay and revoked there
// is never handed out again (global mints a fresh one each time).
func TestRelayedBotTokenRevoked(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.BotForPeople = "on"
	e.setPolicy(p)
	u := e.signIn("alice", "octocat").routes()
	req := map[string]any{"repo": "acme/web", "access": "read", "as": "bot", "purpose": "x"}
	a := personToken(t, e, u, req)["token"].(string)
	if b := personToken(t, e, u, req)["token"].(string); b != a {
		t.Fatal("the partition didn't reuse its token")
	}
	ok(t, e.call(u, personC("alice"), "POST", "/scm/token/revoke", map[string]string{"token": a}), 204)
	e.gh.mu.Lock()
	revoked := e.gh.instTokens[a].revoked
	e.gh.mu.Unlock()
	if !revoked {
		t.Fatal("not revoked at GitHub")
	}
	if c := personToken(t, e, u, req)["token"].(string); c == a {
		t.Fatal("a revoked token handed out again")
	}
}

// A person deleted and re-created under the same id is another person: the
// first relay call from the new partition id wipes the old one's identity
// and everything kept for it.
func TestRelayNewPartitionWipesOld(t *testing.T) {
	e := newEnv(t)
	e.setup()
	var wiped []string
	old := wipePerson
	wipePerson = append(wipePerson, func(_ *srv, p string) { wiped = append(wiped, p) })
	t.Cleanup(func() { wipePerson = old })
	e.signIn("alice", "octocat")
	e.signIn("bob", "hubot")
	e.pids["alice"] = "pid-alice-2"
	c := relayC("alice", "write")
	c.pid = "pid-alice-2"
	ok(t, e.call(e.gH, c, "POST", "/partition/revoke-token", map[string]string{"accessToken": "ghu_whatever"}), 204)
	if e.global.ident("alice") != nil {
		t.Fatal("the old person's identity survived")
	}
	if strings.Join(wiped, ",") != "alice" || e.global.ident("bob") == nil {
		t.Fatalf("wiped %v", wiped)
	}
	// The new alice signs in and is registered under her partition id.
	delete(e.users, "alice")
	e.signIn("alice", "octocat")
	if id := e.global.ident("alice"); id == nil || id.PID != "pid-alice-2" {
		t.Fatalf("new ident %+v", id)
	}
}

// A refresh ends reuse of the old epoch's scoped tokens, not their record:
// one still outstanding is revoked by value, at GitHub.
func TestRevokeScopedAfterRefresh(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	first := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"})
	e.clock.advance(6*time.Hour + 50*time.Minute)
	if next := personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"}); next["token"] == first["token"] {
		t.Fatal("no refresh")
	}
	tok := first["token"].(string)
	if e.userTok(tok).revoked {
		t.Fatal("the fake revoked it already: the test proves nothing")
	}
	ok(t, e.call(u, personC("alice"), "POST", "/scm/token/revoke", map[string]string{"token": tok}), 204)
	if !e.userTok(tok).revoked {
		t.Fatal("the old epoch's token wasn't revoked")
	}
}

// A person's partition keeps tokens for reuse; global's revoke-all and a
// policy change reach that cache (conf "public" tokenGen), and every reuse
// re-checks the request against the policy conf carries.
func TestPartitionReuseFollowsGlobal(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.BotForPeople = "on"
	e.setPolicy(p)
	u := e.signIn("alice", "octocat").routes()
	bot := map[string]any{"repo": "acme/web", "access": "read", "as": "bot", "purpose": "x"}
	a := personToken(t, e, u, bot)["token"].(string)
	if personToken(t, e, u, bot)["token"].(string) != a {
		t.Fatal("the partition didn't reuse its token")
	}
	ok(t, e.call(e.gH, ownerC, "POST", "/api/revoke-all", nil), 200)
	e.gh.mu.Lock()
	revoked := e.gh.instTokens[a].revoked
	e.gh.mu.Unlock()
	if !revoked {
		t.Fatal("revoke-all missed the relayed token")
	}
	b := personToken(t, e, u, bot)["token"].(string)
	if b == a {
		t.Fatal("a revoked token handed out again after revoke-all")
	}
	// botRepos narrowed: the cached token for acme/web isn't handed out.
	p.BotRepos = []string{"acme/api"}
	e.setPolicy(p)
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", bot), 403, "not-allowed")
	// allowedAccounts narrowed: neither a bot nor a person token, cached or not.
	p.BotRepos = []string{"*/*"}
	e.setPolicy(p)
	personToken(t, e, u, bot)
	pers := map[string]any{"repo": "acme/web", "access": "read", "purpose": "y"}
	personToken(t, e, u, pers)
	p.AllowedAccounts = []string{"other"}
	e.setPolicy(p)
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", bot), 403, "not-allowed")
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", pers), 403, "not-allowed")
	// allowWorkflows off again: a cached workflows token isn't handed out.
	p.AllowedAccounts, p.AllowWorkflows = []string{"acme"}, true
	e.setPolicy(p)
	wf := map[string]any{"repo": "acme/web", "access": "write", "purpose": "w", "permissions": map[string]string{"workflows": "write"}}
	personToken(t, e, u, wf)
	p.AllowWorkflows = false
	e.setPolicy(p)
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", wf), 403, "not-allowed")
	// The generation alone ends reuse, even with the policy unchanged.
	c := personToken(t, e, u, pers)["token"].(string)
	if personToken(t, e, u, pers)["token"].(string) != c {
		t.Fatal("no reuse within a generation")
	}
	e.setPolicy(p)
	if personToken(t, e, u, pers)["token"].(string) == c {
		t.Fatal("a person token of the old generation handed out again")
	}
}

// A partition re-checks the policy conf "public" carries before reusing a
// token, on its own: a narrowed policy refuses a cached token without a
// relay, even with the generation unchanged (here conf is written
// directly; global always moves the generation too).
func TestPartitionRechecksPolicy(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.BotForPeople, p.AllowWorkflows = "on", true
	e.setPolicy(p)
	s := e.signIn("alice", "octocat")
	u := s.routes()
	bot := map[string]any{"repo": "acme/web", "access": "read", "as": "bot", "purpose": "x"}
	pers := map[string]any{"repo": "acme/web", "access": "read", "purpose": "y"}
	wf := map[string]any{"repo": "acme/web", "access": "write", "purpose": "w", "permissions": map[string]string{"workflows": "write"}}
	cached := map[string]string{}
	for k, req := range map[string]map[string]any{"bot": bot, "pers": pers, "wf": wf} {
		cached[k] = personToken(t, e, u, req)["token"].(string)
	}
	relays := 0
	orig := s.relayCall
	s.relayCall = func(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
		relays++
		return orig(ctx, method, path, body)
	}
	base := e.global.public()
	narrowed := func(f func(*publicPolicy)) {
		pub := base
		pub.Policy.AllowedAccounts = append([]string{}, base.Policy.AllowedAccounts...)
		pub.Policy.BotRepos = append([]string{}, base.Policy.BotRepos...)
		f(&pub.Policy)
		if err := e.conf.Put("public", pub); err != nil {
			t.Fatal(err)
		}
	}
	for _, c := range []struct {
		name string
		f    func(*publicPolicy)
		reqs []map[string]any
	}{
		{"botRepos", func(p *publicPolicy) { p.BotRepos = []string{"acme/api"} }, []map[string]any{bot}},
		{"allowedAccounts", func(p *publicPolicy) { p.AllowedAccounts = []string{"other"} }, []map[string]any{bot, pers}},
		{"allowWorkflows", func(p *publicPolicy) { p.AllowWorkflows = false }, []map[string]any{wf}},
	} {
		narrowed(c.f)
		for _, req := range c.reqs {
			refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", req), 403, "not-allowed")
		}
		if relays != 0 {
			t.Fatalf("%s: the partition relayed instead of refusing itself", c.name)
		}
	}
	// The same generation and policy again: every cached token is reused,
	// still without a relay (the refusals above were the partition's).
	narrowed(func(*publicPolicy) {})
	for k, req := range map[string]map[string]any{"bot": bot, "pers": pers, "wf": wf} {
		if personToken(t, e, u, req)["token"].(string) != cached[k] {
			t.Fatalf("%s: not reused", k)
		}
	}
	if relays != 0 {
		t.Fatal("reuse relayed")
	}
}

// A grant revoked elsewhere (the person revoking the App in their GitHub
// settings): GitHub answers its refresh token incorrect_client_credentials
// (live), not bad_refresh_token. Once GitHub's check says it no longer
// knows the access token, the sign-in is over — a token request asks for a
// sign-in, Forget clears. A token GitHub still knows, or a check that
// fails, is GitHub's refusal, nothing cleared.
func TestRevokedGrantEndsSignin(t *testing.T) {
	e := newEnv(t)
	e.setup()
	revokeElsewhere := func(s *srv) {
		acc, _, _ := s.userTokens()
		if st, _, _ := fdo(t, e, "DELETE", "/applications/"+e.gh.clientID+"/grant", "basic", map[string]string{"access_token": acc.Token}, nil); st != 204 {
			t.Fatalf("revoking the grant at the fake: %d", st)
		}
	}
	gone := func(s *srv, what string, atGlobal bool) {
		t.Helper()
		if s.personRecord() != nil || (atGlobal && e.global.ident("alice") != nil) {
			t.Fatalf("%s: the sign-in is still kept", what)
		}
		if _, _, err := s.userTokens(); err == nil {
			t.Fatalf("%s: the pair is still in the vault", what)
		}
	}
	kept := func(s *srv, what string) {
		t.Helper()
		if s.personRecord() == nil || e.global.ident("alice") == nil {
			t.Fatalf("%s: the sign-in was cleared", what)
		}
	}
	tokenReq := func(u http.Handler) *httptest.ResponseRecorder {
		return e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"})
	}
	icc := `{"error":"incorrect_client_credentials"}`

	// A token request once the access token has expired.
	s := e.signIn("alice", "octocat")
	revokeElsewhere(s)
	e.clock.advance(9 * time.Hour)
	if x := refusal(t, tokenReq(s.routes()), 409, "signin"); x.Signin == nil {
		t.Fatal("no sign-in started")
	}
	gone(s, "token request", false) // as bad_refresh_token: the partition's pair

	// Forget: nothing left to revoke with — 204, cleared.
	s = e.signIn("alice", "octocat")
	revokeElsewhere(s)
	e.clock.advance(9 * time.Hour)
	ok(t, e.call(s.routes(), pageC("alice"), "DELETE", "/scm/signin", nil), 204)
	gone(s, "Forget", true)

	// GitHub's check fails (500): unknown — refused, nothing cleared.
	s = e.signIn("alice", "octocat")
	revokeElsewhere(s)
	e.clock.advance(9 * time.Hour)
	e.gh.fail("POST /applications/", 1, 500, nil, `{"message":"boom"}`)
	refusal(t, e.call(s.routes(), pageC("alice"), "DELETE", "/scm/signin", nil), 502, "upstream")
	kept(s, "a failed check")
	ok(t, e.call(s.routes(), pageC("alice"), "DELETE", "/scm/signin", nil), 204)

	// incorrect_client_credentials for a token GitHub still knows: GitHub's
	// refusal, nothing cleared — a token request and Forget alike.
	s = e.signIn("alice", "octocat")
	e.clock.advance(7 * time.Hour)
	e.gh.fail("POST /login/oauth/access_token", 1, 200, nil, icc)
	refusal(t, tokenReq(s.routes()), 502, "upstream")
	kept(s, "token request, a token GitHub knows")
	e.clock.advance(59*time.Minute + 30*time.Second) // within Forget's minute
	e.gh.fail("POST /login/oauth/access_token", 1, 200, nil, icc)
	refusal(t, e.call(s.routes(), pageC("alice"), "DELETE", "/scm/signin", nil), 502, "upstream")
	kept(s, "Forget, a token GitHub knows")
}
