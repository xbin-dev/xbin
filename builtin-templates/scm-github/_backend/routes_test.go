package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestHelloShape(t *testing.T) {
	e := newEnv(t)
	var h helloResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/hello?protocol=1", nil), &h)
	if h.App.Configured || len(h.Notes) == 0 {
		t.Fatalf("unconfigured: %+v", h)
	}
	e.setup()
	r := e.call(e.gH, agentC, "GET", "/scm/hello?protocol=1", nil)
	ok(t, r, 200)
	decode(t, r, &h)
	if h.Protocol != 1 || fmt.Sprint(h.Protocols) != "[1]" || h.SCM.Name != "scm-github" || h.SCM.Kind != "github" ||
		h.Hosts[0] != "127.0.0.1" || !h.App.Configured || h.App.Slug != "acme-xbin" || !strings.HasSuffix(h.App.InstallURL, "/apps/acme-xbin/installations/new") ||
		h.Limits != (limitsInfo{100, 900, 3000, 100, 50}) || h.Events.PollMinMs != 120000 || h.Events.Webhooks != "unknown" {
		t.Fatalf("hello: %+v", h)
	}
	if got := strings.Join(h.Caps, ","); got != "credentials,repos,pulls,issues,checks,poll,partitions" {
		t.Fatalf("caps: %s", got)
	}
	// The body carries its etag and the header the same.
	var raw map[string]any
	decode(t, r, &raw)
	if raw["etag"] == nil || raw["etag"] != r.Header().Get("ETag") {
		t.Fatalf("etag %v / %q", raw["etag"], r.Header().Get("ETag"))
	}
	// checks.rerun once the App has actions: write — in a person's
	// partition only: global has no one to rerun as.
	e.gh.mu.Lock()
	e.gh.appPerms["actions"] = "write"
	e.gh.mu.Unlock()
	e.setup()
	decode(t, e.call(e.gH, agentC, "GET", "/scm/hello", nil), &h)
	if strings.Contains(strings.Join(h.Caps, ","), "checks.rerun") {
		t.Fatalf("global lists rerun: %v", h.Caps)
	}
	ua := e.user("alice").routes()
	decode(t, e.call(ua, personC("alice"), "GET", "/scm/hello", nil), &h)
	if !strings.Contains(strings.Join(h.Caps, ","), "checks.rerun") {
		t.Fatalf("no rerun cap: %v", h.Caps)
	}
	p := basePolicy()
	p.AllowRerun = false
	e.setPolicy(p)
	decode(t, e.call(ua, personC("alice"), "GET", "/scm/hello", nil), &h)
	if strings.Contains(strings.Join(h.Caps, ","), "checks.rerun") {
		t.Fatal("rerun cap with allowRerun off")
	}
	// The person's page reaches hello in their partition.
	ok(t, e.call(e.user("alice").routes(), pageC("alice"), "GET", "/scm/hello", nil), 200)
}

// A capability hello doesn't list answers 501 unsupported.
func TestUnlistedCapUnsupported(t *testing.T) {
	e := newEnv(t)
	e.setup()
	for _, rt := range [][2]string{{"POST", "/scm/subscriptions"}, {"GET", "/scm/subscriptions"}, {"DELETE", "/scm/subscriptions/s1"}, {"GET", "/scm/events"}} {
		refusal(t, e.call(e.gH, agentC, rt[0], rt[1], map[string]any{"repo": "acme/web"}), 501, "unsupported")
	}
	refusal(t, e.call(e.gH, nobodyC, "GET", "/scm/events", nil), 403, "not-allowed")
}

func TestHelloProtocolRefusal(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"2", "0", "x"} {
		x := refusal(t, e.call(e.gH, agentC, "GET", "/scm/hello?protocol="+p, nil), 400, "protocol")
		if fmt.Sprint(x.Protocols) != "[1]" {
			t.Fatalf("protocols %v", x.Protocols)
		}
	}
}

func TestUpstreamErrorMapping(t *testing.T) {
	e := newEnv(t)
	e.setup()
	get := func() *scmErr {
		r := e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil)
		var x scmErr
		json.Unmarshal(r.Body.Bytes(), &x)
		x.Status = r.Code
		return &x
	}
	ok(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil), 200) // tokens in place
	cases := []struct {
		status  int
		header  map[string]string
		body    string
		code    int
		refusal string
	}{
		{404, nil, `{"message":"Not Found"}`, 404, "not-found"},
		{403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": fmt.Sprint(e.clock.now().Add(time.Minute).Unix())}, `{"message":"API rate limit exceeded"}`, 429, "limit"},
		{403, map[string]string{"X-GitHub-SSO": "required; url=https://github.com/orgs/acme/sso?authorization_request=AB"}, `{"message":"Resource protected by organization SAML enforcement."}`, 403, "not-allowed"},
		{403, nil, `{"message":"Resource not accessible by integration"}`, 403, "not-allowed"},
		{429, map[string]string{"Retry-After": "30"}, `{"message":"secondary rate limit"}`, 429, "limit"},
		{422, nil, `{"message":"Validation Failed"}`, 400, "invalid"},
		{502, nil, `{"message":"Server Error"}`, 503, "unavailable"},
		{418, nil, `{"message":"teapot"}`, 502, "upstream"},
	}
	for _, c := range cases {
		e.gh.fail("GET /repos/acme/web", 1, c.status, c.header, c.body)
		e.global.gh.forget("inst:100")
		x := get()
		if x.Status != c.code || x.Refusal != c.refusal {
			t.Fatalf("%d → %d %s, want %d %s", c.status, x.Status, x.Refusal, c.code, c.refusal)
		}
		switch {
		case c.header["X-GitHub-SSO"] != "" && (x.SSO == nil || !strings.HasPrefix(x.SSO.URL, "https://github.com/orgs/acme/sso")):
			t.Fatalf("sso: %+v", x)
		case c.refusal == "limit" && x.RetryAfterMs <= 0:
			t.Fatalf("limit without retryAfterMs: %+v", x)
		case c.refusal == "upstream" && (x.Upstream == nil || x.Upstream.Status != 418):
			t.Fatalf("upstream: %+v", x)
		}
		if c.header["X-RateLimit-Remaining"] == "0" {
			// The identity's limit is spent: the next call isn't even made.
			n := e.gh.count("GET /repos/acme/web")
			if x := get(); x.Refusal != "limit" || e.gh.count("GET /repos/acme/web") != n {
				t.Fatal("called GitHub with the limit spent")
			}
			e.clock.advance(61 * time.Second)
		}
	}
}

func TestReposPagination(t *testing.T) {
	e := newEnv(t)
	e.setup()
	e.gh.mu.Lock()
	var names []string
	for i := 0; i < 45; i++ {
		names = append(names, fmt.Sprintf("r%02d", i))
	}
	e.gh.instRepos[100] = names
	e.gh.mu.Unlock()
	var all []repoInfo
	next := ""
	for i := 0; ; i++ {
		var pg page[repoInfo]
		decode(t, e.call(e.gH, agentC, "GET", "/scm/repos?limit=20&cursor="+next, nil), &pg)
		all = append(all, pg.Items...)
		if pg.Next == "" {
			break
		}
		next = pg.Next
		if i > 5 {
			t.Fatal("no end")
		}
	}
	if len(all) != 45 || all[0].Name != "r00" || all[44].Name != "r44" || all[0].Host != "127.0.0.1" || all[0].Permission != "admin" || all[0].CloneURL == "" {
		t.Fatalf("%d repos: %+v", len(all), all[0])
	}
	var pg page[repoInfo]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repos?q=R1", nil), &pg)
	if len(pg.Items) != 10 {
		t.Fatalf("q: %d", len(pg.Items))
	}
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repos?cursor=garbage", nil), 400, "invalid")
	// Cached five minutes: no new upstream list within.
	n := e.gh.count("GET /installation/repositories")
	e.call(e.gH, agentC, "GET", "/scm/repos", nil)
	if e.gh.count("GET /installation/repositories") != n {
		t.Fatal("not cached")
	}
	// A person's: their installations, the repos they can see.
	u := e.signIn("alice", "octocat").routes()
	e.gh.mu.Lock()
	e.gh.collab["acme/r01|octocat"] = "write"
	e.gh.mu.Unlock()
	decode(t, e.call(u, personC("alice"), "GET", "/scm/repos", nil), &pg)
	if len(pg.Items) != 1 || pg.Items[0].Name != "r01" || pg.Items[0].Permission != "write" {
		t.Fatalf("person's repos: %+v", pg.Items)
	}
	// One repo, with its default branch's protection.
	e.gh.mu.Lock()
	e.gh.instRepos[100] = append(e.gh.instRepos[100], "web")
	e.gh.mu.Unlock()
	var one repoInfo
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil), &one)
	if one.Protected == nil || !*one.Protected || one.DefaultBranch != "main" {
		t.Fatalf("repo: %+v", one)
	}
	// A person who can't bypass it: protected; an admin, who may: absent.
	one = repoInfo{}
	decode(t, e.call(u, personC("alice"), "GET", "/scm/repo?repo=acme/web", nil), &one)
	if one.Protected == nil || !*one.Protected {
		t.Fatalf("writer's repo: %+v", one)
	}
	e.gh.mu.Lock()
	e.gh.collab["acme/web|octocat"] = "admin"
	e.gh.mu.Unlock()
	one = repoInfo{}
	decode(t, e.call(u, personC("alice"), "GET", "/scm/repo?repo=acme/web", nil), &one)
	if one.Protected != nil || one.Permission != "admin" {
		t.Fatalf("admin's repo: %+v", one)
	}
}

func seedPulls(e *env) {
	e.gh.mu.Lock()
	defer e.gh.mu.Unlock()
	e.gh.ci.pulls["acme/web"] = []*fPull{
		{Number: 1, Title: "one", Head: "a", Base: "main", State: "open", User: "octocat", Mergeable: boolp(true)},
		{Number: 2, Title: "two", Head: "b", Base: "main", State: "closed", User: "octocat"},
		{Number: 3, Title: "three", Head: "c", Base: "main", State: "closed", Merged: true, User: "octocat"},
	}
}

func boolp(b bool) *bool { return &b }

func TestPullCreateIdempotent(t *testing.T) {
	e := newEnv(t)
	e.setup()
	req := map[string]any{"repo": "acme/web", "head": "feature", "title": "Fix login", "body": "untrusted <b>body</b>", "draft": true}
	r := e.call(e.gH, agentC, "POST", "/scm/pulls", req)
	ok(t, r, 201)
	var p pullInfo
	decode(t, r, &p)
	if p.Base.Ref != "main" || !p.Draft || p.Body != "untrusted <b>body</b>" || p.Head.Ref != "feature" || p.Existing || !p.Author.Self || !p.Author.Bot {
		t.Fatalf("created: %+v", p)
	}
	// GitHub's 422 "already exists": the open one, 200 existing.
	r = e.call(e.gH, agentC, "POST", "/scm/pulls", map[string]any{"repo": "acme/web", "head": "feature", "title": "again"})
	ok(t, r, 200)
	var p2 pullInfo
	decode(t, r, &p2)
	if !p2.Existing || p2.Number != p.Number {
		t.Fatalf("existing: %+v", p2)
	}
	// A clientId: the same request answers the same pull; another, 409.
	req2 := map[string]any{"repo": "acme/web", "head": "other", "title": "x", "clientId": "c1"}
	e.gh.mu.Lock()
	e.gh.ci.branches["acme/web|other"] = strings.Repeat("c", 40)
	e.gh.mu.Unlock()
	ok(t, e.call(e.gH, agentC, "POST", "/scm/pulls", req2), 201)
	r = e.call(e.gH, agentC, "POST", "/scm/pulls", req2)
	ok(t, r, 200)
	decode(t, r, &p2)
	if !p2.Existing {
		t.Fatal("clientId repeat not existing")
	}
	req2["title"] = "y"
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/pulls", req2), 409, "exists")
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/pulls", map[string]any{"repo": "acme/web", "title": "x"}), 400, "invalid")
	// Lists: state words.
	seedPulls(e)
	for state, want := range map[string]string{"open": "1", "closed": "2", "merged": "3", "all": "1,2,3"} {
		var pg page[pullInfo]
		decode(t, e.call(e.gH, agentC, "GET", "/scm/pulls?repo=acme/web&state="+state, nil), &pg)
		var got []string
		for _, x := range pg.Items {
			got = append(got, fmt.Sprint(x.Number))
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("%s: %v", state, got)
		}
	}
	// No merge: PATCH refuses state merged.
	refusal(t, e.call(e.gH, agentC, "PATCH", "/scm/pulls/1", map[string]any{"repo": "acme/web", "state": "merged"}), 400, "invalid")
	var pp pullInfo
	r = e.call(e.gH, agentC, "PATCH", "/scm/pulls/1", map[string]any{"repo": "acme/web", "title": "renamed", "state": "closed"})
	ok(t, r, 200)
	decode(t, r, &pp)
	if pp.Title != "renamed" || pp.State != "closed" {
		t.Fatalf("patched: %+v", pp)
	}
}

func TestMergeableUnknown(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedPulls(e)
	var p pullInfo
	decode(t, e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web", nil), &p)
	if p.Mergeable == nil || !*p.Mergeable || p.RetryAfterMs != 0 {
		t.Fatalf("computed: %+v", p)
	}
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"][0].Mergeable = nil
	e.gh.mu.Unlock()
	r := e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web", nil)
	if !strings.Contains(r.Body.String(), `"mergeable":null`) {
		t.Fatalf("mergeable not null: %s", r.Body)
	}
	decode(t, r, &p)
	if p.MergeableState != "unknown" || p.RetryAfterMs != 3000 {
		t.Fatalf("unknown: %+v", p)
	}
}

func TestReadyForReviewGraphQL(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedPulls(e)
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"][0].Draft = true
	e.gh.mu.Unlock()
	var p pullInfo
	decode(t, e.call(e.gH, agentC, "PATCH", "/scm/pulls/1", map[string]any{"repo": "acme/web", "draft": false}), &p)
	if p.Draft {
		t.Fatal("still a draft")
	}
	decode(t, e.call(e.gH, agentC, "PATCH", "/scm/pulls/1", map[string]any{"repo": "acme/web", "draft": true}), &p)
	if !p.Draft {
		t.Fatal("not a draft")
	}
	e.gh.mu.Lock()
	q := strings.Join(e.gh.ci.graphql, "\n")
	e.gh.mu.Unlock()
	if !strings.Contains(q, "markPullRequestReadyForReview") || !strings.Contains(q, "convertPullRequestToDraft") {
		t.Fatalf("mutations: %s", q)
	}
	// Already as asked: no mutation.
	n := e.gh.count("POST /graphql")
	e.call(e.gH, agentC, "PATCH", "/scm/pulls/1", map[string]any{"repo": "acme/web", "draft": true})
	if e.gh.count("POST /graphql") != n {
		t.Fatal("a mutation for nothing")
	}
}

func TestCommentsTimeline(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedPulls(e)
	at := func(m int) string { return e.clock.now().Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	user := map[string]any{"login": "hubot", "type": "User"}
	e.gh.mu.Lock()
	e.gh.ci.comments["acme/web|1"] = []map[string]any{{"id": 11, "user": user, "author_association": "FIRST_TIME_CONTRIBUTOR", "body": "c1", "html_url": "u", "created_at": at(1)},
		{"id": 12, "user": user, "author_association": "MEMBER", "body": "c3", "html_url": "u", "created_at": at(5)}}
	e.gh.ci.reviews["acme/web|1"] = []map[string]any{{"id": 21, "user": user, "author_association": "OWNER", "body": "lgtm", "state": "CHANGES_REQUESTED", "html_url": "u", "submitted_at": at(3)},
		{"id": 22, "user": user, "body": "", "state": "PENDING", "html_url": "u", "submitted_at": at(4)}}
	e.gh.ci.revComms["acme/web|1"] = []map[string]any{{"id": 31, "user": user, "author_association": "COLLABORATOR", "body": "nit", "path": "a.go", "line": 7, "html_url": "u", "created_at": at(2)}}
	e.gh.mu.Unlock()
	var pg page[comment]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/pulls/1/comments?repo=acme/web", nil), &pg)
	var got []string
	for _, c := range pg.Items {
		got = append(got, c.Kind+":"+c.Body+":"+c.State+":"+c.Author.Association)
	}
	want := "comment:c1::NONE,review-comment:nit::COLLABORATOR,review:lgtm:changes_requested:OWNER,comment:c3::MEMBER"
	if strings.Join(got, ",") != want {
		t.Fatalf("timeline:\n%s\nwant\n%s", strings.Join(got, ","), want)
	}
	if pg.Items[1].Path != "a.go" || pg.Items[1].Line != 7 {
		t.Fatalf("review comment: %+v", pg.Items[1])
	}
	since := e.clock.now().Add(3 * time.Minute).UnixMilli()
	pg = page[comment]{}
	decode(t, e.call(e.gH, agentC, "GET", fmt.Sprintf("/scm/pulls/1/comments?repo=acme/web&since=%d&limit=1", since), nil), &pg)
	if len(pg.Items) != 1 || pg.Items[0].Body != "lgtm" || pg.Next == "" {
		t.Fatalf("since: %+v", pg)
	}
	next := pg.Next
	pg = page[comment]{}
	decode(t, e.call(e.gH, agentC, "GET", fmt.Sprintf("/scm/pulls/1/comments?repo=acme/web&since=%d&limit=1&cursor=%s", since, next), nil), &pg)
	if len(pg.Items) != 1 || pg.Items[0].Body != "c3" || pg.Next != "" {
		t.Fatalf("page 2: %+v", pg)
	}
	r := e.call(e.gH, agentC, "POST", "/scm/pulls/1/comments", map[string]any{"repo": "acme/web", "body": "hello"})
	ok(t, r, 201)
	var c comment
	decode(t, r, &c)
	if c.Body != "hello" || c.Kind != "comment" || !c.Author.Self {
		t.Fatalf("posted: %+v", c)
	}
	// Issues: pull requests left out; one with its comments.
	e.gh.mu.Lock()
	e.gh.ci.issues["acme/web"] = []*fIssue{{Number: 5, Title: "Crash on login", Body: "b", State: "open", Labels: []string{"bug"}, User: "hubot", At: e.clock.now()},
		{Number: 1, Title: "a pull", State: "open", PR: true, User: "x", At: e.clock.now()}}
	e.gh.ci.comments["acme/web|5"] = []map[string]any{{"id": 51, "user": user, "body": "me too", "html_url": "u", "created_at": at(1)}}
	e.gh.mu.Unlock()
	var is page[issueInfo]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web", nil), &is)
	if len(is.Items) != 1 || is.Items[0].Number != 5 || is.Items[0].Labels[0] != "bug" {
		t.Fatalf("issues: %+v", is)
	}
	decode(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web&q=crash", nil), &is)
	if len(is.Items) != 1 {
		t.Fatalf("search: %+v", is)
	}
	var one issueInfo
	decode(t, e.call(e.gH, agentC, "GET", "/scm/issues/5?repo=acme/web&comments=1", nil), &one)
	if len(one.Comments) != 1 || one.Comments[0].Body != "me too" {
		t.Fatalf("issue: %+v", one)
	}
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/issues/1?repo=acme/web", nil), 404, "not-found")
}

func TestConditionalETag(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedPulls(e)
	r := e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web", nil)
	tag := r.Header().Get("ETag")
	if tag == "" {
		t.Fatal("no ETag")
	}
	r = e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web&ifNoneMatch="+tag, nil)
	if r.Code != 304 || r.Body.Len() != 0 {
		t.Fatalf("query validator: %d %s", r.Code, r.Body)
	}
	c := agentC
	rq := e.call(e.gH, c, "GET", "/scm/pulls/1?repo=acme/web", nil)
	_ = rq
	// The header works too, and upstream calls were conditional (304s).
	e.gh.mu.Lock()
	nm := e.gh.notMod
	e.gh.mu.Unlock()
	if nm == 0 {
		t.Fatal("GitHub never answered 304: upstream calls aren't conditional")
	}
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"][0].Title = "changed"
	e.gh.mu.Unlock()
	r = e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web&ifNoneMatch="+tag, nil)
	if r.Code != 200 || r.Header().Get("ETag") == tag {
		t.Fatalf("changed: %d", r.Code)
	}
}

func TestPollChangedOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedPulls(e)
	items := []map[string]any{
		{"id": "t1-pull", "kind": "pull", "repo": "acme/web", "number": 1},
		{"id": "t1-checks", "kind": "checks", "repo": "acme/web", "ref": "main"},
		{"id": "t9-pull", "kind": "pull", "repo": "acme/web", "number": 99},
		{"id": "bad", "kind": "nope", "repo": "acme/web"},
	}
	var res pollResp
	decode(t, e.call(e.gH, agentC, "POST", "/scm/poll", map[string]any{"items": items}), &res)
	if len(res.Items) != 4 || !res.Items[0].Changed || res.Items[0].ETag == "" || len(res.Items[0].Value) == 0 ||
		!res.Items[1].Changed || res.Items[2].Error == nil || res.Items[2].Error.Refusal != "not-found" || res.Items[3].Error.Refusal != "invalid" {
		t.Fatalf("first poll: %+v", res)
	}
	var v pullInfo
	json.Unmarshal(res.Items[0].Value, &v)
	if v.Number != 1 {
		t.Fatalf("value: %s", res.Items[0].Value)
	}
	items[0]["etag"], items[1]["etag"] = res.Items[0].ETag, res.Items[1].ETag
	res = pollResp{}
	decode(t, e.call(e.gH, agentC, "POST", "/scm/poll", map[string]any{"items": items[:2]}), &res)
	if res.Items[0].Changed || res.Items[1].Changed || res.Items[0].Value != nil {
		t.Fatalf("unchanged poll: %+v", res)
	}
	// The poll's etag is the route's.
	r := e.call(e.gH, agentC, "GET", "/scm/pulls/1?repo=acme/web", nil)
	if r.Header().Get("ETag") != res.Items[0].ETag {
		t.Fatal("poll and route etags differ")
	}
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"][0].Title = "changed"
	e.gh.mu.Unlock()
	res = pollResp{}
	decode(t, e.call(e.gH, agentC, "POST", "/scm/poll", map[string]any{"items": items[:2]}), &res)
	if !res.Items[0].Changed || res.Items[1].Changed {
		t.Fatalf("changed poll: %+v", res)
	}
	var many []map[string]any
	for i := 0; i < 51; i++ {
		many = append(many, items[0])
	}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/poll", map[string]any{"items": many}), 400, "invalid")
	// A rate limit in one item asks the consumer to wait.
	e.gh.fail("GET /repos/acme/web/pulls/1", 1, 429, map[string]string{"Retry-After": "20"}, `{"message":"slow"}`)
	e.global.gh.forget("inst:100")
	decode(t, e.call(e.gH, agentC, "POST", "/scm/poll", map[string]any{"items": items[:1]}), &res)
	if res.RetryAfterMs < 20000 {
		t.Fatalf("retryAfterMs %d", res.RetryAfterMs)
	}
}

// An issue search stays in the repo asked about: qualifiers in q are
// dropped (they'd OR in repos the policy refuses), and a hit from another
// repo is filtered out whatever GitHub answers.
func TestIssueSearchStaysInRepo(t *testing.T) {
	e := newEnv(t)
	e.setup()
	p := basePolicy()
	p.BotRepos = []string{"acme/web"}
	e.setPolicy(p)
	e.gh.mu.Lock()
	e.gh.ci.issues["acme/web"] = []*fIssue{{Number: 5, Title: "Crash on login", State: "open", User: "hubot", At: e.clock.now()}}
	e.gh.ci.issues["acme/api"] = []*fIssue{{Number: 9, Title: "Crash in secret billing", Body: "the secret", State: "open", User: "hubot", At: e.clock.now()}}
	e.gh.mu.Unlock()
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/api", nil), 403, "not-allowed")
	only5 := func(path string) {
		t.Helper()
		var is page[issueInfo]
		decode(t, e.call(e.gH, agentC, "GET", path, nil), &is)
		if len(is.Items) != 1 || is.Items[0].Number != 5 {
			t.Fatalf("%s: %+v", path, is.Items)
		}
	}
	for _, q := range []string{"repo:acme/api", "repo:acme/api+crash", "org:acme+crash", "crash+OR+repo:acme/api", "%22repo:acme/api%22+crash", "-repo:acme/web+crash", "(repo:acme/api)+crash"} {
		only5("/scm/issues?repo=acme/web&q=" + q)
	}
	e.gh.mu.Lock()
	for _, q := range e.gh.ci.searches {
		if strings.Count(q, "repo:") != 1 || strings.Contains(q, "org:") || strings.Contains(q, " OR ") {
			t.Errorf("a qualifier reached GitHub: %q", q)
		}
	}
	e.gh.ci.searchAll = true // a GitHub answering past the repo named
	e.gh.mu.Unlock()
	only5("/scm/issues?repo=acme/web&q=crash")
}

// GitHub's search limit is its own: spent, it blocks searches by that
// identity, not its other calls.
func TestRateLimitPerResource(t *testing.T) {
	e := newEnv(t)
	e.setup()
	ok(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web&q=crash", nil), 200)
	reset := fmt.Sprint(e.clock.now().Add(time.Minute).Unix())
	e.gh.fail("GET /search/issues", 1, 403, map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": reset, "X-RateLimit-Resource": "search"}, `{"message":"API rate limit exceeded"}`)
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web&q=crash", nil), 429, "limit")
	n := e.gh.count("GET /search/issues")
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web&q=crash", nil), 429, "limit")
	if e.gh.count("GET /search/issues") != n {
		t.Fatal("searched with the search limit spent")
	}
	ok(t, e.call(e.gH, agentC, "GET", "/scm/repo?repo=acme/web", nil), 200)
	ok(t, e.call(e.gH, agentC, "GET", "/scm/issues?repo=acme/web", nil), 200)
	for u, want := range map[string]string{"https://api.github.com/search/issues?q=x": "search", "https://h/api/graphql": "graphql", "https://api.github.com/repos/a/search": "core", "https://api.github.com/repos/a/b/issues?q=/search/": "core",
		"https://api.github.com/repos/acme/search/pulls": "core", "https://api.github.com/repos/search/web/pulls": "core",
		"https://ghe.example/api/v3/search/code?q=x": "search", "https://ghe.example/api/v3/repos/acme/search/pulls": "core",
		"https://api.github.com/repos/acme/graphql": "core", "https://api.github.com/graphql": "graphql"} {
		if got := rateResource(u); got != want {
			t.Fatalf("%s: %s, want %s", u, got, want)
		}
	}
	for base, want := range map[string]string{"https://api.github.com": "https://api.github.com/graphql", "https://ghe.example/api/v3": "https://ghe.example/api/graphql"} {
		if got := graphqlURL(base); got != want || rateResource(got) != "graphql" {
			t.Fatalf("%s: %s (%s), want %s", base, got, rateResource(got), want)
		}
	}
}

// conf "public": a different App's Device Flow is unknown until checked;
// the same App's stays.
func TestPublicDeviceFlowFollowsApp(t *testing.T) {
	e := newEnv(t)
	e.setup()
	s := e.global
	p := s.public()
	p.DeviceFlow = "on"
	if err := s.conf.Put("public", p); err != nil {
		t.Fatal(err)
	}
	gen := p.TokenGen
	if err := s.writePublic(0); err != nil || s.public().DeviceFlow != "on" || s.public().TokenGen != gen {
		t.Fatalf("same App: %+v %v", s.public(), err)
	}
	if err := s.writePublic(pubNewApp | pubTokens); err != nil || s.public().DeviceFlow != "unknown" || s.public().TokenGen <= gen {
		t.Fatalf("new App: %+v %v", s.public(), err)
	}
}
