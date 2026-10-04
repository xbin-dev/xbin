package main

import (
	"encoding/json"
	"net/http"
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
	p.PersonTTLMin = 30
	e.setPolicy(p)
	m = personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "read", "purpose": "capped"})
	if exp := int64(m["expiresAt"].(float64)); exp != e.clock.now().Add(30*time.Minute).UnixMilli() {
		t.Fatalf("personTtlMin: %d", exp)
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
		for _, p := range []string{"/partition/identity", "/partition/scope", "/partition/revoke-token", "/partition/revoke-grant", "/partition/bot-token"} {
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
