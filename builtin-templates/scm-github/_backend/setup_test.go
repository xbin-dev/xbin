package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func startManifest(t *testing.T, e *env, c caller) (state string, m map[string]any) {
	t.Helper()
	r := e.call(e.gH, c, "POST", "/setup/manifest", map[string]any{"name": "Acme xbin", "org": "acme", "publicHost": "scm.example.com", "presets": []string{"ci"}})
	ok(t, r, 200)
	var out struct {
		PostURL  string         `json:"postUrl"`
		Manifest map[string]any `json:"manifest"`
		State    string         `json:"state"`
	}
	decode(t, r, &out)
	if !strings.HasPrefix(out.PostURL, e.gh.srv.URL+"/organizations/acme/settings/apps/new?state=") || len(out.State) != 43 {
		t.Fatalf("start: %+v", out)
	}
	return out.State, out.Manifest
}

func TestSetupManifestState(t *testing.T) {
	e := newEnv(t)
	state, m := startManifest(t, e, ownerC)
	perms := m["default_permissions"].(map[string]any)
	hook := m["hook_attributes"].(map[string]any)
	if perms["actions"] != "write" || perms["members"] != "read" || perms["administration"] != nil || perms["workflows"] != nil ||
		hook["url"] != "https://scm.example.com/hook/github" || m["redirect_url"] != "https://scm.example.com/setup/github" {
		t.Fatalf("manifest: %v", m)
	}
	e.gh.mu.Lock()
	e.gh.manifests["code1"] = true
	e.gh.mu.Unlock()
	land := func(code, st string) string {
		return "https://scm.example.com/setup/github?" + url.Values{"code": {code}, "state": {st}}.Encode()
	}
	// Another manager can't finish this one's flow.
	refusal(t, e.call(e.gH, relayC("bob", "write"), "POST", "/setup/manifest/code", map[string]string{"url": land("code1", state)}), 403, "not-allowed")
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/manifest/code", map[string]string{"url": land("code1", "forged")}), 404, "not-found")
	r := e.call(e.gH, ownerC, "POST", "/setup/manifest/code", map[string]string{"url": land("code1", state)})
	ok(t, r, 200)
	if a, _ := e.global.app(); a == nil || a.AppID != e.gh.appID || a.Slug != "acme-xbin" {
		t.Fatalf("app: %+v", a)
	}
	// Single use.
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/manifest/code", map[string]string{"url": land("code1", state)}), 404, "not-found")
	// An hour at most.
	state2, _ := startManifest(t, e, relayC("alice", "write"))
	e.clock.advance(61 * time.Minute)
	refusal(t, e.call(e.gH, relayC("alice", "write"), "POST", "/setup/manifest/code", map[string]string{"url": land("code2", state2)}), 404, "not-found")
	// Without a public host the App's hook waits, and GitHub sends the
	// manager to its App list.
	r = e.call(e.gH, ownerC, "POST", "/setup/manifest", map[string]any{"name": "x"})
	var out struct {
		Manifest map[string]any `json:"manifest"`
	}
	decode(t, r, &out)
	if out.Manifest["hook_attributes"].(map[string]any)["active"] != false || !strings.HasSuffix(out.Manifest["redirect_url"].(string), "/settings/apps") {
		t.Fatalf("no host: %v", out.Manifest)
	}
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/manifest", map[string]any{"name": "x", "presets": []string{"admin"}}), 400, "invalid")
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/manifest", map[string]any{"name": "x", "publicHost": "evil.com/x"}), 400, "invalid")
}

func TestSetupIngressCallback(t *testing.T) {
	e := newEnv(t)
	state, _ := startManifest(t, e, ownerC)
	e.gh.mu.Lock()
	e.gh.manifests["code9"] = true
	e.gh.mu.Unlock()
	refusal := func(c caller, code int) {
		t.Helper()
		r := e.call(e.gH, c, "GET", "/setup/github?code=code9&state="+state, nil)
		if r.Code != code || !strings.HasPrefix(r.Header().Get("Content-Type"), "text/html") {
			t.Fatalf("%+v: %d %s", c, r.Code, r.Body)
		}
	}
	refusal(agentC, 403)
	refusal(ingress, 200)
	if a, _ := e.global.app(); a == nil {
		t.Fatal("not set up")
	}
	refusal(ingress, 404) // spent
	// The secrets came from GitHub and went to the vault, never to an answer.
	if v, _ := e.global.vault.Get(vaultHookSecret); v.Reveal() != "fake-manifest-hook-secret" {
		t.Fatal("webhook secret not kept")
	}
	for _, s := range e.seen {
		if strings.Contains(s.body, "fake-manifest-hook-secret") || strings.Contains(s.body, e.gh.clientSecret) || strings.Contains(s.body, "PRIVATE KEY") {
			t.Fatalf("a secret in %s %s", s.method, s.path)
		}
	}
	// A manager loading the tile's own address at top level (way b) works too.
	state2, _ := startManifest(t, e, ownerC)
	e.gh.mu.Lock()
	e.gh.manifests["code10"] = true
	e.gh.mu.Unlock()
	if r := e.call(e.gH, ownerC, "GET", "/setup/github?code=code10&state="+state2, nil); r.Code != 200 {
		t.Fatalf("top level: %d", r.Code)
	}
}

func TestSetupPasteValidates(t *testing.T) {
	e := newEnv(t)
	paste := func(over map[string]any) map[string]any {
		b := map[string]any{"appId": e.gh.appID, "clientId": e.gh.clientID, "clientSecret": e.gh.clientSecret, "privateKey": e.gh.keyPEM,
			"hookUrl": "https://scm.example.com/hook/github", "webhookSecret": "whsec-1"}
		for k, v := range over {
			b[k] = v
		}
		return b
	}
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"appId": 1})), 400, "invalid")
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"privateKey": "garbage"})), 400, "invalid")
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	otherPEM := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(other)}))
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"privateKey": otherPEM})), 400, "invalid")
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"hookUrl": "http://plain"})), 400, "invalid")
	refusal(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"apiBase": "http://x", "webBase": "http://y"})), 400, "invalid")
	if a, _ := e.global.app(); a != nil {
		t.Fatal("stored a bad App")
	}
	r := e.call(e.gH, ownerC, "POST", "/setup/app", paste(nil))
	ok(t, r, 200)
	e.gh.mu.Lock()
	hu, hs := e.gh.hookURL, e.gh.hookSecret
	e.gh.mu.Unlock()
	if hu != "https://scm.example.com/hook/github" || hs != "whsec-1" {
		t.Fatalf("hook config: %q %q", hu, hs)
	}
	var got map[string]any
	decode(t, e.call(e.gH, ownerC, "GET", "/setup/app", nil), &got)
	app := got["app"].(map[string]any)
	if !strings.HasPrefix(app["keyFingerprint"].(string), "SHA256:") || got["configured"] != true || app["appId"].(float64) != float64(e.gh.appID) {
		t.Fatalf("GET /setup/app: %v", got)
	}
	// conf "public" for partitions: no secret, the public half.
	var pub publicConf
	e.conf.Get("public", &pub)
	if !pub.Configured || pub.ClientID != e.gh.clientID || pub.Slug != "acme-xbin" || pub.Policy.BotForPeople != "off" {
		t.Fatalf("public: %+v", pub)
	}
	// A second paste without a webhook secret keeps the one there; a new
	// one moves the old to -prev.
	ok(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"webhookSecret": nil})), 200)
	if v, _ := e.global.vault.Get(vaultHookSecret); v.Reveal() != "whsec-1" {
		t.Fatal("webhook secret replaced")
	}
	ok(t, e.call(e.gH, ownerC, "POST", "/setup/app", paste(map[string]any{"webhookSecret": "whsec-2"})), 200)
	if v, _ := e.global.vault.Get(vaultHookSecretP); v.Reveal() != "whsec-1" {
		t.Fatal("no previous secret kept")
	}
}

func TestSetupNeedsManager(t *testing.T) {
	e := newEnv(t)
	e.setup()
	readOnly := relayC("bob", "read")
	viewer := relayC("alice", "write")
	viewer.viewedBy = "admin"
	for _, c := range []caller{agentC, personC("alice"), readOnly, viewer, ingress, nobodyC, cronC} {
		for _, rt := range [][2]string{{"GET", "/setup/app"}, {"POST", "/setup/app"}, {"POST", "/setup/manifest"}, {"POST", "/setup/manifest/code"},
			{"POST", "/setup/check"}, {"GET", "/setup/installations"}, {"GET", "/api/policy"}, {"PUT", "/api/policy"}, {"POST", "/api/revoke-all"}} {
			if r := e.call(e.gH, c, rt[0], rt[1], map[string]any{}); r.Code != 403 {
				t.Fatalf("%+v %v: %d", c, rt, r.Code)
			}
		}
	}
	for _, c := range []caller{ownerC, selfC, relayC("alice", "write"), relayC("alice", "terminal")} {
		ok(t, e.call(e.gH, c, "GET", "/setup/app", nil), 200)
	}
	// The page says who manages.
	var pg map[string]any
	decode(t, e.call(e.gH, readOnly, "GET", "/api/page", nil), &pg)
	if pg["manager"] != false || pg["mode"] != "global" {
		t.Fatalf("page: %v", pg)
	}
	refusal(t, e.call(e.gH, agentC, "GET", "/api/page", nil), 403, "not-allowed")
}

// No secret reaches an answer (but the one route that hands out a token),
// a log line, a kv value or a %v of a struct holding one.
func TestNoSecretsInResponsesOrLogs(t *testing.T) {
	var logs bytes.Buffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	e := newEnv(t)
	e.setup()
	seedCI(e)
	getToken(t, e, agentC, map[string]any{"repo": "acme/web", "access": "write", "purpose": "p"})
	s := e.signIn("alice", "octocat")
	u := s.routes()
	personToken(t, e, u, map[string]any{"repo": "acme/web", "access": "write"})
	e.call(u, personC("alice"), "GET", "/scm/checks?repo=acme/web&ref=feature", nil)
	e.call(u, personC("alice"), "GET", "/scm/repos", nil)
	e.call(u, pageC("alice"), "GET", "/api/page", nil)
	e.call(e.gH, ownerC, "GET", "/setup/app", nil)
	e.call(e.gH, ownerC, "GET", "/api/policy", nil)
	e.call(u, pageC("alice"), "GET", "/scm/signin", nil)
	e.call(e.gH, agentC, "GET", "/scm/hello", nil)
	// a failure path: a refusal from GitHub carrying a request echo
	e.gh.fail("POST /applications", 1, 500, nil, `{"message":"boom"}`)
	e.call(u, personC("alice"), "POST", "/scm/token", map[string]any{"repo": "acme/web", "access": "read", "purpose": "x"})

	e.gh.mu.Lock()
	var secrets []string
	for k := range e.gh.instTokens {
		secrets = append(secrets, k)
	}
	for k, v := range e.gh.userTokens {
		if !v.scoped {
			secrets = append(secrets, k) // parents never leave the partition
		}
	}
	for k := range e.gh.refresh {
		secrets = append(secrets, k)
	}
	for k := range e.gh.devices {
		secrets = append(secrets, k)
	}
	e.gh.mu.Unlock()
	secrets = append(secrets, e.gh.clientSecret, "PRIVATE KEY", e.vaultOf(e.global, vaultHookSecret))
	for _, r := range e.seen {
		if r.path == "/scm/token" && r.code == 200 {
			continue // the one route that hands a token out
		}
		for _, sec := range secrets {
			if sec != "" && strings.Contains(r.body, sec) {
				t.Fatalf("%s %s answered a secret (%.8s…)", r.method, r.path, sec)
			}
		}
	}
	for _, sec := range secrets {
		if strings.Contains(logs.String(), sec) {
			t.Fatalf("a secret in the log")
		}
	}
	for name, kv := range map[string]*memKV{"global": e.global.state.(*memKV), "alice": s.state.(*memKV), "conf": e.conf} {
		for _, k := range mustKeys(kv) {
			var raw json.RawMessage
			kv.Get(k, &raw)
			for _, sec := range append(secrets, e.vaultOf(s, vaultUserAccess)) {
				if sec != "" && strings.Contains(string(raw), sec) {
					t.Fatalf("%s kv %s holds a secret", name, k)
				}
			}
		}
	}
	tr := &tokenResp{Token: newSecret("ghs_SECRETVALUE")}
	for _, f := range []string{"%v", "%+v", "%#v", "%s"} {
		if strings.Contains(fmt.Sprintf(f, tr), "SECRETVALUE") || strings.Contains(fmt.Sprintf(f, *tr), "SECRETVALUE") {
			t.Fatalf("%s shows the token", f)
		}
	}
	if b, _ := json.Marshal(tr); strings.Contains(string(b), "SECRETVALUE") {
		t.Fatal("json shows the token")
	}
	if !strings.Contains(string(tokenJSON(tr)), "SECRETVALUE") {
		t.Fatal("tokenJSON lost the token")
	}
}

func (e *env) vaultOf(s *srv, name string) string {
	v, err := s.vault.Get(name)
	if err != nil {
		return ""
	}
	return v.Reveal()
}

// The App's permissions are re-read from GitHub on Check and when an
// installation accepts new ones: what is offered from them follows a
// change made on GitHub after Paste.
func TestAppPermissionsRefreshed(t *testing.T) {
	ee := newEvEnv(t)
	if ee.global.public().Rerun {
		t.Fatal("rerun offered without actions: write")
	}
	ee.gh.mu.Lock()
	ee.gh.appPerms["actions"] = "write"
	ee.gh.mu.Unlock()
	ok(t, ee.call(ee.gH, ownerC, "POST", "/setup/check", nil), 200)
	if !ee.global.public().Rerun {
		t.Fatal("Check didn't pick up actions: write")
	}
	var h helloResp
	decode(t, ee.call(ee.user("alice").routes(), personC("alice"), "GET", "/scm/hello", nil), &h) // a partition reads it
	if !slices.Contains(h.Caps, capRerun) {
		t.Fatalf("caps %v", h.Caps)
	}
	ee.gh.mu.Lock()
	delete(ee.gh.appPerms, "actions")
	ee.gh.appPerms["contents"] = "read"
	ee.gh.mu.Unlock()
	ok(t, ee.hook("installation", fixtureWith(t, "installation", map[string]any{"action": "new_permissions_accepted"})), 202)
	if p := ee.global.public(); p.Rerun || p.BotContents != "read" {
		t.Fatalf("after new_permissions_accepted: rerun %v, contents %q", p.Rerun, p.BotContents)
	}
}
