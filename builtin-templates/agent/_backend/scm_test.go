package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The bound providers come from XBIN_IFACE_SCM (service "scm" only, one per
// name); hello negotiates protocol 1, is cached 60 s and a failure 10 s,
// and a provider without credentials or of another protocol is refused.
func TestScmHelloCache(t *testing.T) {
	f := newFakeSCM(t, "apps/scm-github")
	g := newFakeSCM(t, "apps/scm-github#eu")
	bindSCM(t, f, g)
	if b := scmBound(); len(b) != 2 || b[0] != "apps/scm-github" || b[1] != "apps/scm-github#eu" {
		t.Fatalf("bound: %v", b)
	}
	t.Setenv("XBIN_IFACE_SCM", `[{"provider":"apps/a","url":"http://x/"},{"provider":"apps/a","url":"http://dup"},
		{"provider":"apps/llm","url":"http://y","service":"openai"},{"provider":"apps/nourl","url":""}]`)
	if b := scmBound(); len(b) != 1 || b[0] != "apps/a" {
		t.Fatalf("filtered: %v", b)
	}
	bindSCM(t, f, g)
	if _, err := scmFor("apps/gone"); !scmRefused(err, scmRefNotFound) {
		t.Fatalf("unbound: %v", err)
	}
	if _, err := scmOnly(""); !scmRefused(err, scmRefInvalid) || !strings.Contains(err.Error(), "name one") {
		t.Fatalf("two bound, none named: %v", err)
	}
	api, _ := scmFor("apps/scm-github")
	ctx := context.Background()
	h, err := api.Hello(ctx)
	if err != nil || h.SCM.Title != "GitHub" || !h.has(scmCapCredentials) || h.Limits.ReposPerToken != 100 {
		t.Fatalf("hello: %+v %v", h, err)
	}
	_, _ = api.Hello(ctx)
	if n := len(f.Requests("GET /hello")); n != 1 {
		t.Fatalf("hello asked %d times", n)
	}
	if q := f.Requests("GET /hello")[0].Query; q != "protocol=1" {
		t.Fatalf("hello's query: %q", q)
	}
	// 60 s later it is asked again
	age := func(d time.Duration) {
		scmHelloMu.Lock()
		for k, e := range scmHelloCache {
			e.at = e.at.Add(-d)
			scmHelloCache[k] = e
		}
		scmHelloMu.Unlock()
	}
	age(61 * time.Second)
	_, _ = api.Hello(ctx)
	if n := len(f.Requests("GET /hello")); n != 2 {
		t.Fatalf("after 60 s: asked %d times", n)
	}
	// a failure is kept 10 s
	eu, _ := scmFor("apps/scm-github#eu")
	g.FailNext("GET /hello", &scmError{})
	if _, err := eu.Hello(ctx); !scmRefused(err, scmRefUnavailable) {
		t.Fatalf("a 5xx without a body: %v", err)
	}
	if _, err := eu.Hello(ctx); err == nil || len(g.Requests("GET /hello")) != 1 {
		t.Fatalf("the failure isn't kept: %v", err)
	}
	age(11 * time.Second)
	if _, err := eu.Hello(ctx); err != nil || len(g.Requests("GET /hello")) != 2 {
		t.Fatalf("after 10 s: %v", err)
	}
	// no credentials: refused
	forgetSCMHellos()
	f.Caps = []string{scmCapRepos}
	if _, err := api.Hello(ctx); !scmRefused(err, scmRefUnsupported) {
		t.Fatalf("no credentials: %v", err)
	}
}

// A provider's refusal comes back as *scmError: its status, refusal and
// payload; a status alone maps to the contract's refusal.
func TestScmRefusalDecode(t *testing.T) {
	f := newFakeSCM(t, "apps/scm-github")
	bindSCM(t, f)
	api, _ := scmFor("apps/scm-github")
	ctx := context.Background()
	f.Person = nil
	_, err := api.Token(ctx, scmTokenReq{Repos: []string{"acme/web"}, Access: "write", As: scmAsPerson})
	var se *scmError
	if !errors.As(err, &se) || se.Status != 409 || se.Refusal != scmRefSignin || se.Signin == nil || se.Signin.UserCode != "ABCD-1234" || se.Signin.PollID == "" {
		t.Fatalf("signin: %#v", err)
	}
	f.Identities = []string{scmAsPerson}
	_, err = api.Token(ctx, scmTokenReq{Repos: []string{"acme/web"}, Access: "write", As: scmAsBot})
	if !errors.As(err, &se) || se.Status != 403 || se.Refusal != scmRefIdentity || len(se.Identities) != 1 {
		t.Fatalf("identity: %#v", err)
	}
	f.FailNext("GET /repos", &scmError{Status: 409, Refusal: scmRefNotInstalled, Message: "not installed", Install: &scmInstall{URL: "https://x", Owner: "acme"}})
	_, err = api.Repos(ctx, scmQuery{})
	if !errors.As(err, &se) || se.Install == nil || se.Install.Owner != "acme" {
		t.Fatalf("not-installed: %#v", err)
	}
	f.FailNext("GET /repos", &scmError{Status: 429, Refusal: scmRefLimit, Message: "slow down", RetryAfterMs: 3000})
	if _, err = api.Repos(ctx, scmQuery{}); !errors.As(err, &se) || se.RetryAfterMs != 3000 {
		t.Fatalf("limit: %#v", err)
	}
	f.FailNext("GET /repos", &scmError{})
	if _, err = api.Repos(ctx, scmQuery{}); !errors.As(err, &se) || se.Status != 503 || se.Refusal != scmRefUnavailable {
		t.Fatalf("a bare 503: %#v", err)
	}
	f.SetSignedIn("octocat", 583231)
	f.FailNext("GET /repos", &scmError{Status: 502, Message: "GitHub said 500"})
	if _, err = api.Repos(ctx, scmQuery{}); !errors.As(err, &se) || se.Refusal != scmRefUpstream {
		t.Fatalf("a 502 without a refusal: %#v", err)
	}
	if _, err = api.Pull(ctx, "acme/web", 7, scmAsPerson); !scmRefused(err, scmRefNotFound) {
		t.Fatalf("not-found: %v", err)
	}
	// the hello's protocol refusal
	f.Caps = append(f.Caps, scmCapCredentials)
	srv := f.srv.URL
	c := &scmConn{E: scmEndpoint{Provider: "apps/odd", URL: srv + "/nope"}}
	if _, err := c.Hello(ctx); !scmRefused(err, scmRefNotFound) {
		t.Fatalf("a missing route: %v", err)
	}
	// an unreachable provider
	c = &scmConn{E: scmEndpoint{Provider: "apps/down", URL: "http://127.0.0.1:1"}}
	if _, err := c.Repos(ctx, scmQuery{}); !scmRefused(err, scmRefUnavailable) || !strings.Contains(err.Error(), "didn't answer") {
		t.Fatalf("unreachable: %v", err)
	}
}

// Every client call reaches its route with its parameters: pulls (an
// existing one for the head), conditional checks (304: nil), a running
// job's log (in-progress with the live log's url), poll, subscriptions,
// and revoking by value sends the value only in the body.
func TestScmClientRoutes(t *testing.T) {
	f := newFakeSCM(t, "apps/scm-github")
	bindSCM(t, f)
	f.AddRepo("acme/web", true)
	f.SetSignedIn("octocat", 583231)
	api, _ := scmFor("apps/scm-github")
	ctx := context.Background()
	p, err := api.PullCreate(ctx, scmPullReq{Repo: "acme/web", Head: "xbin/k/1-a", Title: "A", As: scmAsBot})
	if err != nil || p.Number != 1 || p.Existing {
		t.Fatalf("create: %+v %v", p, err)
	}
	if p, err = api.PullCreate(ctx, scmPullReq{Repo: "acme/web", Head: "xbin/k/1-a", Title: "A"}); err != nil || !p.Existing {
		t.Fatalf("again: %+v %v", p, err)
	}
	list, err := api.Pulls(ctx, scmQuery{Repo: "acme/web", Head: "xbin/k/1-a", State: "open"})
	if err != nil || len(list.Items) != 1 {
		t.Fatalf("list: %+v %v", list, err)
	}
	title := "B"
	if p, err = api.PullPatch(ctx, 1, scmPullPatch{Repo: "acme/web", Title: &title}); err != nil || p.Title != "B" {
		t.Fatalf("patch: %+v %v", p, err)
	}
	f.SetChecks("acme/web", "main", &scmChecks{SHA: "abc", State: "pending", Checks: []scmCheck{{ID: "1", Name: "test"}}, Statuses: []scmStatus{}})
	c, err := api.Checks(ctx, "acme/web", "main", "", "")
	if err != nil || c.State != "pending" || c.ETag == "" {
		t.Fatalf("checks: %+v %v", c, err)
	}
	if c2, err := api.Checks(ctx, "acme/web", "main", c.ETag, ""); err != nil || c2 != nil {
		t.Fatalf("not modified: %+v %v", c2, err)
	}
	f.SetJobLog("88", "one\ntwo\nthree\n", true)
	_, err = api.JobLog(ctx, "acme/web", "88", 0, 0, 0, "")
	var se *scmError
	if !errors.As(err, &se) || se.Refusal != scmRefInProgress || !strings.Contains(se.URL, "/job/88") {
		t.Fatalf("a running job's log: %#v", err)
	}
	f.SetJobLog("88", "one\ntwo\nthree\n", false)
	if l, err := api.JobLog(ctx, "acme/web", "88", 6, 0, 0, ""); err != nil || l.Text != "three\n" || !l.Truncated || l.From != 8 {
		t.Fatalf("log tail: %+v %v", l, err)
	}
	if q := f.Requests("GET /checks/jobs/88/log")[1].Query; !strings.Contains(q, "tailBytes=6") || strings.Contains(q, "since") {
		t.Fatalf("log query: %q", q)
	}
	if r, err := api.Rerun(ctx, scmRerunReq{Repo: "acme/web", RunID: "7", FailedOnly: true, As: scmAsBot}); !scmRefused(err, scmRefIdentity) {
		t.Fatalf("a bot's rerun: %+v %v", r, err)
	}
	pr, err := api.Poll(ctx, scmPollReq{Items: []scmPollItem{{ID: "a", Kind: "checks", Repo: "acme/web", Ref: "main", ETag: c.ETag}, {ID: "b", Kind: "pull", Repo: "acme/web", Number: 1}}})
	if err != nil || len(pr.Items) != 2 || pr.Items[0].Changed || !pr.Items[1].Changed || len(pr.Items[1].Value) == 0 {
		t.Fatalf("poll: %+v %v", pr, err)
	}
	s, err := api.Subscribe(ctx, scmSubscription{Repo: "acme/web", Branches: []string{"main"}, Key: "task:1"})
	if err != nil || s.ID == "" || s.For != "global" {
		t.Fatalf("subscribe: %+v %v", s, err)
	}
	if subs, err := api.Subscriptions(ctx); err != nil || len(subs) != 1 {
		t.Fatalf("subscriptions: %+v %v", subs, err)
	}
	if err := api.Unsubscribe(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	tok, err := api.Token(ctx, scmTokenReq{Repos: []string{"acme/web"}, Access: "write", As: scmAsBot, Purpose: "p1"})
	if err != nil || tok.Token.Empty() || tok.Identity.Kind != scmAsBot {
		t.Fatalf("token: %v %v", tok, err)
	}
	if strings.Contains(tok.String(), tok.Token.Reveal()) || strings.Contains(jsonOf(tok), tok.Token.Reveal()) {
		t.Fatal("a token shows")
	}
	if err := api.Revoke(ctx, scmRevokeReq{Token: tok.Token}); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	_ = json.Unmarshal([]byte(f.Requests("POST /token/revoke")[0].Body), &body)
	if body["token"] != tok.Token.Reveal() || len(body) != 1 {
		t.Fatalf("revoke's body: %v", body)
	}
	if err := api.Revoke(ctx, scmRevokeReq{Purpose: "p1"}); !scmRefused(err, scmRefNotFound) {
		t.Fatalf("nothing left for p1: %v", err)
	}
	if is, err := api.Issues(ctx, scmQuery{Repo: "acme/web", Labels: []string{"bug", "ui"}}); err != nil || is.Items == nil {
		t.Fatalf("issues: %+v %v", is, err)
	}
	if q := f.Requests("GET /issues")[0].Query; !strings.Contains(q, "labels=bug%2Cui") {
		t.Fatalf("issues' query: %q", q)
	}
}

// GET /projects/scm lists every bound provider as this home sees it — one
// that fails with why.
func TestScmProvidersRoute(t *testing.T) {
	fx := credFixture(t, modeUser)
	down := newFakeSCM(t, "apps/scm-down")
	bindSCM(t, fx.scm, down)
	down.FailNext("GET /hello", &scmError{Status: 503, Refusal: scmRefSetup, Message: "not set up yet"})
	var out struct {
		Providers []scmProviderView `json:"providers"`
	}
	got := callAs(t, fx.h.(*http.ServeMux), asAlice, "GET", "/projects/scm", nil)
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &out) != nil || len(out.Providers) != 2 {
		t.Fatalf("GET /projects/scm: %d %s", got.Code, got.Body)
	}
	a, b := out.Providers[0], out.Providers[1]
	if a.SCM != "apps/scm-github" || a.Title != "GitHub" || a.You == nil || a.You.Person.Login != "octocat" || a.Error != "" || len(a.Caps) == 0 {
		t.Fatalf("the provider: %+v", a)
	}
	if b.SCM != "apps/scm-down" || b.Refusal != scmRefSetup || !strings.Contains(b.Error, "not set up") {
		t.Fatalf("the failing one: %+v", b)
	}
}
