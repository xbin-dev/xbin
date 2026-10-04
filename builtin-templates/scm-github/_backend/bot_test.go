package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func getToken(t *testing.T, e *env, c caller, body map[string]any) (string, map[string]any) {
	t.Helper()
	r := e.call(e.gH, c, "POST", "/scm/token", body)
	ok(t, r, 200)
	var m map[string]any
	decode(t, r, &m)
	return m["token"].(string), m
}

func lastMint(e *env) map[string]any {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	return e.gh.mintReqs[len(e.gh.mintReqs)-1]
}

// A bot token names the asked repos and the preset's permissions only —
// and GitHub (the fake) holds it to them.
func TestBotTokenDownScoped(t *testing.T) {
	e := newEnv(t)
	e.setup()
	tok, body := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "p"})
	m := lastMint(e)
	if repos, _ := json.Marshal(m["repositories"]); string(repos) != `["web"]` {
		t.Fatalf("minted for %s", repos)
	}
	perms, _ := json.Marshal(m["permissions"])
	want, _ := json.Marshal(presetRead())
	if string(perms) != string(want) {
		t.Fatalf("permissions %s, want %s", perms, want)
	}
	if body["username"] != "x-access-token" || body["host"] == "" || body["identity"].(map[string]any)["login"] != "acme-xbin[bot]" ||
		!strings.Contains(body["author"].(map[string]any)["email"].(string), "777+acme-xbin[bot]@users.noreply.") {
		t.Fatalf("answer: %v", body)
	}
	if exp, ra := body["expiresAt"].(float64), body["refreshAfter"].(float64); exp-ra != float64(10*time.Minute/time.Millisecond) {
		t.Fatalf("refreshAfter isn't 10 min before expiry: %v %v", exp, ra)
	}
	// The token reads acme/web, not acme/api.
	for repo, code := range map[string]int{"acme/web": 200, "acme/api": 404} {
		r, _ := http.NewRequest("GET", e.gh.srv.URL+"/repos/"+repo, nil)
		r.Header.Set("Authorization", "Bearer "+tok)
		resp, err := e.gh.srv.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != code {
			t.Fatalf("%s: %d", repo, resp.StatusCode)
		}
	}
	// write: contents and pull requests write, the rest read; never administration.
	getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "write"})
	pm := lastMint(e)["permissions"].(map[string]any)
	if pm["contents"] != "write" || pm["pull_requests"] != "write" || pm["issues"] != "read" || pm["administration"] != nil || pm["workflows"] != nil {
		t.Fatalf("write preset: %v", pm)
	}
}

// Reused per (consumer, purpose, repos, permissions) while 15 min (or the
// consumer's minimum) are left; never across consumers.
func TestBotTokenCacheMarginPerConsumer(t *testing.T) {
	e := newEnv(t)
	e.setup()
	req := map[string]any{"repos": []string{"acme/web", "acme/api"}, "access": "read", "purpose": "proj:1"}
	a, _ := getToken(t, e, agentC, req)
	b, _ := getToken(t, e, agentC, map[string]any{"repos": []string{"acme/api", "acme/web"}, "access": "read", "purpose": "proj:1"})
	if a != b {
		t.Fatal("same request, another token")
	}
	c, _ := getToken(t, e, agent2C, req)
	d, _ := getToken(t, e, personCAtGlobalKey(), req)
	if c == a || d == a || d == c {
		t.Fatal("a token shared between consumers")
	}
	other, _ := getToken(t, e, agentC, map[string]any{"repos": []string{"acme/web", "acme/api"}, "access": "read", "purpose": "proj:2"})
	if other == a {
		t.Fatal("a token shared between purposes")
	}
	e.clock.advance(44 * time.Minute) // 16 min left
	if x, _ := getToken(t, e, agentC, req); x != a {
		t.Fatal("renewed with 16 min left")
	}
	e.clock.advance(2 * time.Minute) // 14 min left
	if x, _ := getToken(t, e, agentC, req); x == a {
		t.Fatal("reused with 14 min left")
	}
	// A consumer's minimum: 30 min.
	req["minTtlSec"] = 1800
	f, _ := getToken(t, e, agentC, req)
	e.clock.advance(31 * time.Minute)
	if g, _ := getToken(t, e, agentC, req); g == f {
		t.Fatal("reused under the consumer's minTtlSec")
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read", "minTtlSec": 60}), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read", "minTtlSec": 7200}), 400, "invalid")
}

// a tile's non-primary deployment is another consumer.
func personCAtGlobalKey() caller {
	c := agentC
	c.deployment = "staging"
	return c
}

func TestReposSpanOwners(t *testing.T) {
	e := newEnv(t)
	e.setup()
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repos": []string{"acme/web", "other/x"}, "access": "read"}), 400, "invalid")
	var many []string
	for i := 0; i < 101; i++ {
		many = append(many, "acme/r"+strings.Repeat("x", i%3)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repos": many, "access": "read"}), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "not a repo", "access": "read"}), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "admin"}), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"access": "read"}), 400, "invalid")
}

func TestNotInstalled(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.AllowedAccounts = []string{"acme", "other"}
	e.setPolicy(p)
	x := refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "other/x", "access": "read"}), 409, "not-installed")
	if x.Install == nil || x.Install.Owner != "other" || !strings.HasSuffix(x.Install.URL, "/apps/acme-xbin/installations/new") {
		t.Fatalf("install: %+v", x.Install)
	}
	// Installed now: the cache of the refusal doesn't stick.
	e.gh.mu.Lock()
	e.gh.installs["other"] = 200
	e.gh.instRepos[200] = []string{"x"}
	e.gh.mu.Unlock()
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "other/x", "access": "read"}), 200)
	// Reads say so too.
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/nope", nil), 409, "not-installed")
}

// permissions may only narrow the preset; workflows only under the policy.
func TestPermissionsNarrowOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	ask := func(access string, perms map[string]string) map[string]any {
		return map[string]any{"repo": "acme/web", "access": access, "permissions": perms}
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", ask("read", map[string]string{"contents": "write"})), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", ask("write", map[string]string{"administration": "write"})), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", ask("write", map[string]string{"issues": "write"})), 400, "invalid")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", ask("write", map[string]string{"workflows": "write"})), 403, "not-allowed")
	_, body := getToken(t, e, agentC, ask("write", map[string]string{"contents": "read", "actions": "none"}))
	pm := lastMint(e)["permissions"].(map[string]any)
	if pm["contents"] != "read" || pm["actions"] != nil || pm["pull_requests"] != "write" {
		t.Fatalf("narrowed: %v (answer %v)", pm, body["permissions"])
	}
	p := basePolicy()
	p.AllowWorkflows = true
	e.setPolicy(p)
	e.gh.mu.Lock()
	e.gh.appPerms["workflows"] = "write"
	e.gh.mu.Unlock()
	getToken(t, e, agentC, ask("write", map[string]string{"workflows": "write"}))
	if lastMint(e)["permissions"].(map[string]any)["workflows"] != "write" {
		t.Fatal("workflows not asked for")
	}
}

func TestRevokeByValueAndPurpose(t *testing.T) {
	e := newEnv(t)
	e.setup()
	a, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "p1"})
	b, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/api", "access": "read", "purpose": "p1"})
	c, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "p2"})
	revoked := func(tok string) bool {
		e.gh.mu.Lock()
		defer e.gh.mu.Unlock()
		return e.gh.instTokens[tok].revoked
	}
	// Only the consumer it was given to revokes it by value.
	refusal(t, e.call(e.gH, agent2C, "POST", "/scm/token/revoke", map[string]string{"token": a}), 404, "not-found")
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"token": a}), 204)
	if !revoked(a) || revoked(b) {
		t.Fatal("by value: wrong tokens revoked")
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"token": a}), 404, "not-found")
	refusal(t, e.call(e.gH, agent2C, "POST", "/scm/token/revoke", map[string]string{"purpose": "p1"}), 404, "not-found")
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"purpose": "p1"}), 204)
	if !revoked(b) || revoked(c) {
		t.Fatal("by purpose: wrong tokens revoked")
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"purpose": "p1"}), 404, "not-found")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"token": "ghs_unknown"}), 404, "not-found")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{}), 400, "invalid")
	// A fresh request after a revoke mints anew.
	if x, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "p1"}); x == a {
		t.Fatal("a revoked token handed out again")
	}
	// Revoke all: every cached bot token, managers only.
	refusal(t, e.call(e.gH, agentC, "POST", "/api/revoke-all", nil), 403, "not-allowed")
	ok(t, e.call(e.gH, ownerC, "POST", "/api/revoke-all", nil), 200)
	if !revoked(c) {
		t.Fatal("revoke-all left a token")
	}
}

// GitHub's stateless installation tokens are ~520 characters: nothing
// assumes a length.
func TestLongStatelessTokens(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.gh.mu.Lock()
	e.gh.longTokens = true
	e.gh.mu.Unlock()
	a, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "long"})
	if len(a) != 520 {
		t.Fatalf("len %d", len(a))
	}
	if b, _ := getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "read", "purpose": "long"}); b != a {
		t.Fatal("not cached")
	}
	// Reads with long internal tokens work too.
	ok(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil), 200)
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"token": a}), 204)
}

// botRepos limits what the bot names, for tokens and reads alike.
func TestBotReposPolicy(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.BotRepos = []string{"acme/a*"}
	e.setPolicy(p)
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	ok(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/api", "access": "read"}), 200)
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil), 403, "not-allowed")
	var pg page[repoInfo]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repos", nil), &pg)
	if len(pg.Items) != 1 || pg.Items[0].Name != "api" {
		t.Fatalf("repos: %+v", pg.Items)
	}
	// The relay applies it to a person's bot tokens too.
	p.BotForPeople = "on"
	e.setPolicy(p)
	refusal(t, e.call(e.gH, relayC("alice", "write"), "POST", "/partition/bot-token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	refusal(t, e.call(e.gH, ownerC, "PUT", "/api/policy", policy{BotForPeople: "on", BotRepos: []string{"[bad"}}), 400, "invalid")
}

// allowedAccounts: setup writes the App's own account; others are refused;
// an empty list refuses everything; installations elsewhere are foreign.
func TestAllowedAccounts(t *testing.T) {
	e := newEnv(t)
	e.setup()
	var p policy
	decode(t, e.call(e.gH, ownerC, "GET", "/api/policy", nil), &p)
	if strings.Join(p.AllowedAccounts, ",") != "acme" || p.BotForPeople != "off" || !p.AllowRerun || strings.Join(p.BotRepos, ",") != "*/*" {
		t.Fatalf("defaults after setup: %+v", p)
	}
	e.gh.mu.Lock()
	e.gh.installs["evil"] = 300
	e.gh.instRepos[300] = []string{"x"}
	e.gh.mu.Unlock()
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "evil/x", "access": "read"}), 403, "not-allowed")
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/pulls?repo=evil/x", nil), 403, "not-allowed")
	var inst struct {
		Items   []map[string]any `json:"items"`
		Foreign int              `json:"foreign"`
	}
	decode(t, e.call(e.gH, ownerC, "GET", "/setup/installations", nil), &inst)
	if inst.Foreign != 1 || len(inst.Items) != 2 {
		t.Fatalf("installations: %+v", inst)
	}
	var pg page[repoInfo]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repos", nil), &pg)
	for _, r := range pg.Items {
		if r.Owner == "evil" {
			t.Fatal("a foreign installation's repo listed")
		}
	}
	p.AllowedAccounts = []string{}
	e.setPolicy(p)
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
	// A person's partition reads keep to them too (conf mirrors the list).
	u := e.signIn("alice", "octocat").routes()
	refusal(t, e.call(u, personC("alice"), "GET", "/scm/repo?repo=acme/web", nil), 403, "not-allowed")
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read"}), 403, "not-allowed")
}
