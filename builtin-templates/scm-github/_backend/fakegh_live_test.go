// fakegh_live_test.go — the fake GitHub held to real GitHub. Each case in
// testdata/github-live.json is an answer real GitHub gave the owner's test
// App (live_test.go, by hand); here the fake is asked the same question and
// must answer the same, so a test built on the fake can't drift from
// GitHub where this tile relies on it. Plus the tile's behaviour pinned on
// the answers that corrected it.
package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type liveCase struct {
	Name      string          `json:"name"`
	Call      string          `json:"call"`
	Status    int             `json:"status"`
	Then      int             `json:"then,omitempty"`      // a redirect's target
	Message   string          `json:"message,omitempty"`   // in the body
	ErrorType string          `json:"errorType,omitempty"` // GraphQL
	Keys      []string        `json:"keys,omitempty"`      // the answer has at least these
	Flags     map[string]bool `json:"flags,omitempty"`     // a repo's permissions
}

type fakeAns struct {
	status, then int
	body         []byte
}

// noRedirect sees a redirect itself (a job log's 302) instead of following it.
var noRedirect = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// fdo calls the fake: auth "jwt" (the App's), "basic" (client id and
// secret), "" or a bearer token.
func fdo(t *testing.T, e *env, method, path, auth string, body any, hdr map[string]string) (int, http.Header, []byte) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.gh.srv.URL+path, rd)
	switch auth {
	case "":
	case "jwt":
		j, err := signJWT(e.gh.key, e.gh.clientID, e.clock.now())
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+j)
	case "basic":
		req.SetBasicAuth(e.gh.clientID, e.gh.clientSecret)
	default:
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := noRedirect.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, b
}

// fakeInst mints an installation token at the fake (acme, every repo).
func fakeInst(e *env, perms map[string]string) string {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	return e.gh.newInstToken(100, nil, perms)
}

// fakeUser signs octocat in at the fake: the parent's access token.
func fakeUser(e *env) (access, refresh string) {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	m := e.gh.newUser("octocat")
	return m["access_token"].(string), m["refresh_token"].(string)
}

func fakeScoped(t *testing.T, e *env, parent string) (int, []byte) {
	st, _, b := fdo(t, e, "POST", "/applications/"+e.gh.clientID+"/token/scoped", "basic",
		map[string]any{"access_token": parent, "target": "acme", "repositories": []string{"web"}, "permissions": map[string]string{"contents": "read"}}, nil)
	return st, b
}

func readyMutation(id string) map[string]any {
	return map[string]any{"query": "mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }",
		"variables": map[string]any{"id": id}}
}

// ask puts one case's question to a fresh fake.
func ask(t *testing.T, name string) fakeAns {
	e := newEnv(t)
	cid := e.gh.clientID
	e.gh.mu.Lock()
	e.gh.collab["acme/web|octocat"] = "write"
	e.gh.ci.pulls["acme/web"] = []*fPull{{Number: 1, Title: "one", Head: "feature", HeadSHA: strings.Repeat("b", 40), Base: "main", State: "open", Draft: true, User: "octocat"}}
	e.gh.ci.jobByID["7"] = map[string]any{"id": 7, "status": "in_progress", "html_url": "h"}
	e.gh.ci.jobByID["8"] = map[string]any{"id": 8, "status": "completed", "html_url": "h"}
	e.gh.ci.logs["8"] = "a log\n"
	e.gh.mu.Unlock()
	made := "ghu_" + strings.Repeat("x", 36)
	var a fakeAns
	set := func(st int, b []byte) { a.status, a.body = st, b }
	switch name {
	case "hook-config-webhook-off":
		st, _, b := fdo(t, e, "GET", "/app/hook/config", "jwt", nil, nil)
		set(st, b)
	case "check-token-not-this-apps":
		st, _, b := fdo(t, e, "POST", "/applications/"+cid+"/token", "basic", map[string]string{"access_token": made}, nil)
		set(st, b)
	case "check-token-bearer-not-basic":
		acc, _ := fakeUser(e)
		st, _, b := fdo(t, e, "POST", "/applications/"+cid+"/token", acc, map[string]string{"access_token": acc}, nil)
		set(st, b)
	case "revoke-token-not-this-apps":
		st, _, b := fdo(t, e, "DELETE", "/applications/"+cid+"/token", "basic", map[string]string{"access_token": made}, nil)
		set(st, b)
	case "revoke-grant-not-this-apps":
		st, _, b := fdo(t, e, "DELETE", "/applications/"+cid+"/grant", "basic", map[string]string{"access_token": made}, nil)
		set(st, b)
	case "installation-token-revoke":
		tok := fakeInst(e, presetRead())
		st, _, b := fdo(t, e, "DELETE", "/installation/token", tok, nil, nil)
		set(st, b)
		a.then, _, _ = fdo(t, e, "GET", "/repos/acme/web", tok, nil, nil)
	case "installation-token-revoke-again":
		tok := fakeInst(e, presetRead())
		fdo(t, e, "DELETE", "/installation/token", tok, nil, nil)
		st, _, b := fdo(t, e, "DELETE", "/installation/token", tok, nil, nil)
		set(st, b)
	case "mint-answer":
		st, _, b := fdo(t, e, "POST", "/app/installations/100/access_tokens", "jwt", map[string]any{"repositories": []string{"web"}, "permissions": map[string]string{"metadata": "read"}}, nil)
		set(st, b)
	case "repo-permissions-installation-token":
		st, _, b := fdo(t, e, "GET", "/repos/acme/web", fakeInst(e, presetRead()), nil, nil)
		set(st, b)
	case "installation-repositories-permissions":
		st, _, b := fdo(t, e, "GET", "/installation/repositories", fakeInst(e, presetRead()), nil, nil)
		var w struct {
			Repos []json.RawMessage `json:"repositories"`
		}
		_ = json.Unmarshal(b, &w)
		if len(w.Repos) > 0 {
			b = w.Repos[0]
		}
		set(st, b)
	case "pull-create-without-contents":
		tok := fakeInst(e, map[string]string{"pull_requests": "write", "issues": "write", "metadata": "read"})
		st, _, b := fdo(t, e, "POST", "/repos/acme/web/pulls", tok, map[string]any{"head": "x", "base": "main", "title": "t"}, nil)
		set(st, b)
	case "pull-create-exists":
		tok := fakeInst(e, map[string]string{"pull_requests": "write", "contents": "read", "metadata": "read"})
		st, _, b := fdo(t, e, "POST", "/repos/acme/web/pulls", tok, map[string]any{"head": "feature", "base": "main", "title": "t"}, nil)
		set(st, b)
	case "graphql-ready-contents-read":
		tok := fakeInst(e, map[string]string{"pull_requests": "write", "contents": "read", "metadata": "read"})
		st, _, b := fdo(t, e, "POST", "/graphql", tok, readyMutation("PR_1"), nil)
		set(st, b)
	case "graphql-ready-contents-write":
		tok := fakeInst(e, map[string]string{"pull_requests": "write", "contents": "write", "metadata": "read"})
		st, _, b := fdo(t, e, "POST", "/graphql", tok, readyMutation("PR_1"), nil)
		set(st, b)
	case "graphql-unknown-node":
		tok := fakeInst(e, map[string]string{"pull_requests": "write", "contents": "write", "metadata": "read"})
		st, _, b := fdo(t, e, "POST", "/graphql", tok, readyMutation("PR_doesnotexist"), nil)
		set(st, b)
	case "job-log-running", "job-log-completed":
		id := map[string]string{"job-log-running": "7", "job-log-completed": "8"}[name]
		st, h, b := fdo(t, e, "GET", "/repos/acme/web/actions/jobs/"+id+"/logs", fakeInst(e, presetRead()), nil, nil)
		set(st, b)
		if loc := h.Get("Location"); loc != "" {
			resp, err := http.Get(loc)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			a.then = resp.StatusCode
		}
	case "conditional-get":
		tok := fakeInst(e, presetRead())
		_, h, _ := fdo(t, e, "GET", "/repos/acme/web/pulls/1", tok, nil, nil)
		st, _, b := fdo(t, e, "GET", "/repos/acme/web/pulls/1", tok, nil, map[string]string{"If-None-Match": h.Get("ETag")})
		set(st, b)
	default:
		t.Fatalf("testdata/github-live.json names a case this test doesn't ask: %s", name)
	}
	return a
}

// TestFakeMatchesLiveGitHub asks the fake every question of
// testdata/github-live.json and wants real GitHub's answer.
func TestFakeMatchesLiveGitHub(t *testing.T) {
	b, err := os.ReadFile("testdata/github-live.json")
	if err != nil {
		t.Fatal(err)
	}
	var fx struct {
		Cases []liveCase `json:"cases"`
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	if len(fx.Cases) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range fx.Cases {
		t.Run(c.Name, func(t *testing.T) {
			a := ask(t, c.Name)
			if a.status != c.Status {
				t.Errorf("%s: GitHub answered %d, the fake %d (%s)", c.Call, c.Status, a.status, clip(string(a.body), 200))
			}
			if c.Then != 0 && a.then != c.Then {
				t.Errorf("%s: then GitHub answered %d, the fake %d", c.Call, c.Then, a.then)
			}
			if c.Message != "" && !strings.Contains(string(a.body), c.Message) {
				t.Errorf("%s: GitHub's answer says %q, the fake's doesn't: %s", c.Call, c.Message, clip(string(a.body), 200))
			}
			var m map[string]any
			_ = json.Unmarshal(a.body, &m)
			if c.ErrorType != "" {
				errs, _ := m["errors"].([]any)
				got := ""
				if len(errs) > 0 {
					got, _ = errs[0].(map[string]any)["type"].(string)
				}
				if got != c.ErrorType {
					t.Errorf("%s: GitHub's GraphQL error %s, the fake's %q", c.Call, c.ErrorType, got)
				}
			} else if c.Name == "graphql-ready-contents-write" && m["errors"] != nil {
				t.Errorf("%s: GitHub answered no error, the fake %v", c.Call, m["errors"])
			}
			for _, k := range c.Keys {
				if _, ok := m[k]; !ok {
					t.Errorf("%s: GitHub's answer has %q, the fake's doesn't", c.Call, k)
				}
			}
			if c.Flags != nil {
				got := map[string]bool{}
				if p, ok := m["permissions"].(map[string]any); ok {
					for k, v := range p {
						got[k], _ = v.(bool)
					}
				}
				for k, v := range c.Flags {
					if got[k] != v {
						t.Errorf("%s: GitHub's permissions.%s %v, the fake's %v", c.Call, k, v, got[k])
					}
				}
			}
		})
	}
}

// A person's read whose parent token GitHub no longer takes (401 — the
// fake answered 404 before it was held to GitHub) refreshes the pair once
// and is answered.
func TestPersonReadRefreshesOn401(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"] = []*fPull{{Number: 1, Title: "one", Head: "feature", Base: "main", State: "open", User: "octocat"}}
	e.gh.mu.Unlock()
	s := e.signIn("alice", "octocat")
	u := s.routes()
	acc, _, _ := s.userTokens()
	e.gh.mu.Lock()
	e.gh.userTokens[acc.Token].revoked = true // dead at GitHub, alive in the vault
	e.gh.mu.Unlock()
	var p pullInfo
	decode(t, e.call(u, personC("alice"), "GET", "/scm/pulls/1?repo=acme/web", nil), &p)
	if p.Number != 1 {
		t.Fatalf("pull: %+v", p)
	}
	if a2, _, _ := s.userTokens(); a2.Token == acc.Token {
		t.Fatal("not refreshed")
	}
}

// Paste of an App whose webhook is off: GitHub has no hook config (404),
// which is no webhook, not a failure.
func TestPasteAppWebhookOff(t *testing.T) {
	e := newEnv(t)
	r := e.call(e.gH, ownerC, "POST", "/setup/app", map[string]any{"appId": e.gh.appID, "clientId": e.gh.clientID,
		"clientSecret": e.gh.clientSecret, "privateKey": e.gh.keyPEM})
	ok(t, r, 200)
	var out struct {
		Hook struct {
			URL    string `json:"url"`
			Active bool   `json:"active"`
		} `json:"hook"`
	}
	decode(t, r, &out)
	if out.Hook.Active || out.Hook.URL != "" || e.gh.count("PATCH /app/hook/config") != 0 {
		t.Fatalf("hook: %+v, PATCHes %d", out.Hook, e.gh.count("PATCH /app/hook/config"))
	}
}
