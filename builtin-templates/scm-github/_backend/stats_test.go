package main

import (
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

var shaA = strings.Repeat("a", 40) // acme/web's main (newFakeCI)

// seedStats gives acme/web two open pull requests, one closed, three
// merged (two within seven days), three open issues and a closed one (and
// the open pull request GitHub's issue list carries), and a green check
// on main.
func seedStats(e *env) {
	now := e.clock.now()
	f := e.gh
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ci.pulls["acme/web"] = []*fPull{
		{Number: 1, Title: "one", Head: "feature", HeadSHA: shaB, Base: "main", State: "open", User: "octocat"},
		{Number: 2, Title: "two", Head: "b", Base: "main", State: "open", Draft: true, User: "octocat"},
		{Number: 3, Title: "three", Head: "c", Base: "main", State: "closed", User: "octocat"},
		{Number: 4, Title: "four", Head: "d", Base: "main", State: "closed", Merged: true, MergedAt: now.Add(-48 * time.Hour), User: "octocat"},
		{Number: 5, Title: "five", Head: "e", Base: "main", State: "closed", Merged: true, MergedAt: now.Add(-7*24*time.Hour + time.Minute), User: "octocat"},
		{Number: 6, Title: "six", Head: "f", Base: "main", State: "closed", Merged: true, MergedAt: now.Add(-8 * 24 * time.Hour), User: "octocat"},
	}
	f.ci.issues["acme/web"] = []*fIssue{
		{Number: 10, Title: "a", State: "open", User: "octocat", At: now},
		{Number: 11, Title: "b", State: "open", User: "octocat", At: now},
		{Number: 12, Title: "c", State: "open", User: "octocat", At: now},
		{Number: 13, Title: "d", State: "closed", User: "octocat", At: now},
		{Number: 1, Title: "one", State: "open", User: "octocat", PR: true, At: now},
	}
	f.ci.checkRuns[shaA] = []map[string]any{{"id": 91, "name": "test", "status": "completed", "conclusion": "success", "html_url": "u"}}
}

func (f *fakeGH) total() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.hits {
		n += c
	}
	return n
}

func keysOf(m map[string]any) string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return strings.Join(ks, ",")
}

func TestRepoStatsShape(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedStats(e)
	r := e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil)
	ok(t, r, 200)
	var raw map[string]any
	decode(t, r, &raw)
	if got := keysOf(raw); got != "at,defaultBranch,defaultBranchChecks,etag,mergedPulls,openIssues,openPulls,pushedAt,repo,url" {
		t.Fatalf("fields: %s", got)
	}
	if raw["etag"] != r.Header().Get("ETag") {
		t.Fatalf("etag %v / %q", raw["etag"], r.Header().Get("ETag"))
	}
	var st repoStats
	decode(t, r, &st)
	web := e.gh.srv.URL + "/acme/web"
	if st.Repo != "acme/web" || st.URL != web || st.DefaultBranch != "main" || st.At != e.clock.now().UnixMilli() ||
		st.PushedAt != time.Date(2026, 10, 3, 11, 0, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("repo: %+v", st)
	}
	// Pull requests aren't issues; a draft is open; merged counts the last
	// seven days only.
	if *st.OpenPulls != 2 || *st.OpenIssues != 3 || *st.MergedPulls != (mergedPulls{Days: 7, Count: 2}) {
		t.Fatalf("counts: %d %d %+v", *st.OpenPulls, *st.OpenIssues, *st.MergedPulls)
	}
	if *st.DefaultBranchChecks != (branchChecks{SHA: shaA, State: "success", URL: web + "/commit/" + shaA + "/checks"}) {
		t.Fatalf("checks: %+v", *st.DefaultBranchChecks)
	}
	// One GraphQL query counted; its search names the repo and the moment
	// seven days ago — no REST search spent.
	e.gh.mu.Lock()
	searches := slices.Clone(e.gh.ci.searches)
	e.gh.mu.Unlock()
	if strings.Join(searches, "|") != "repo:acme/web is:pr is:merged merged:>=2026-09-26T12:00:00+00:00" ||
		e.gh.count("POST /graphql") != 1 || e.gh.count("GET /search/issues") != 0 {
		t.Fatalf("searches %q, graphql %d", searches, e.gh.count("POST /graphql"))
	}
	// The default branch's checks are GET /scm/checks's on it.
	var c checksResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=main", nil), &c)
	if c.SHA != st.DefaultBranchChecks.SHA || c.State != st.DefaultBranchChecks.State {
		t.Fatalf("checks route %s %s", c.SHA, c.State)
	}
	// Conditional, as every GET.
	r2 := e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web&ifNoneMatch="+r.Header().Get("ETag"), nil)
	ok(t, r2, 304)
	// A bad repo is refused before GitHub hears of it.
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=nope", nil), 400, "invalid")
}

// A second read within five minutes makes no call to GitHub and answers
// the same `at`; after them, it reads again.
func TestRepoStatsCache(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedStats(e)
	var st repoStats
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil), &st)
	n, at := e.gh.total(), st.At
	e.gh.mu.Lock()
	e.gh.ci.pulls["acme/web"] = append(e.gh.ci.pulls["acme/web"], &fPull{Number: 7, Head: "g", Base: "main", State: "open", User: "octocat"})
	e.gh.mu.Unlock()
	e.clock.advance(5*time.Minute - time.Second)
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil), &st)
	if e.gh.total() != n || st.At != at || *st.OpenPulls != 2 {
		t.Fatalf("not cached: %d calls more, at %d→%d, %d open", e.gh.total()-n, at, st.At, *st.OpenPulls)
	}
	// Another repo is its own entry.
	ok(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/api", nil), 200)
	if e.gh.total() == n {
		t.Fatal("acme/api answered from acme/web's entry")
	}
	e.clock.advance(time.Second)
	n = e.gh.total()
	decode(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil), &st)
	if e.gh.total() == n || st.At == at || *st.OpenPulls != 3 {
		t.Fatalf("stale after 5 min: at %d, %d open", st.At, *st.OpenPulls)
	}
}

// A part GitHub fails is absent and the rest stands; such an answer is
// kept a minute, not five. The repo itself failing fails the read.
func TestRepoStatsPartial(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedStats(e)
	get := func(repo string) (map[string]any, repoStats) {
		t.Helper()
		r := e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo="+repo, nil)
		ok(t, r, 200)
		var raw map[string]any
		var st repoStats
		decode(t, r, &raw)
		decode(t, r, &st)
		return raw, st
	}
	// GraphQL down: no counts; the repo and its checks still.
	e.gh.fail("POST /graphql", 1, 502, nil, `{"message":"Server Error"}`)
	raw, _ := get("acme/web")
	if got := keysOf(raw); got != "at,defaultBranch,defaultBranchChecks,etag,pushedAt,repo,url" {
		t.Fatalf("graphql down: %s", got)
	}
	n := e.gh.total()
	e.clock.advance(59 * time.Second)
	get("acme/web")
	if e.gh.total() != n {
		t.Fatal("a partial answer not kept for its minute")
	}
	e.clock.advance(time.Second)
	_, st := get("acme/web")
	if e.gh.total() == n || st.OpenPulls == nil || *st.MergedPulls != (mergedPulls{7, 2}) {
		t.Fatalf("not read again after a minute: %+v", st)
	}
	// Complete now: kept the five minutes.
	n = e.gh.total()
	e.clock.advance(4 * time.Minute)
	get("acme/web")
	if e.gh.total() != n {
		t.Fatal("a complete answer not kept five minutes")
	}

	// One part of the query timing out leaves only it out. acme/api has no
	// pull requests or issues (0 is an answer, present) and no commit on
	// its default branch (no checks: GitHub's answer, not a failure).
	e.gh.mu.Lock()
	e.gh.ci.gqlFail = "merged"
	e.gh.mu.Unlock()
	raw, st = get("acme/api")
	if got := keysOf(raw); got != "at,defaultBranch,etag,openIssues,openPulls,pushedAt,repo,url" || *st.OpenPulls != 0 || *st.OpenIssues != 0 {
		t.Fatalf("merged timed out: %s %+v", got, st)
	}
	e.gh.mu.Lock()
	e.gh.ci.gqlFail = "issues"
	e.gh.mu.Unlock()
	e.clock.advance(time.Minute)
	raw, _ = get("acme/api")
	if got := keysOf(raw); got != "at,defaultBranch,etag,mergedPulls,openPulls,pushedAt,repo,url" {
		t.Fatalf("issues timed out: %s", got)
	}
	e.gh.mu.Lock()
	e.gh.ci.gqlFail = ""
	e.gh.mu.Unlock()
	e.clock.advance(time.Minute)
	raw, _ = get("acme/api")
	if got := keysOf(raw); got != "at,defaultBranch,etag,mergedPulls,openIssues,openPulls,pushedAt,repo,url" {
		t.Fatalf("acme/api whole: %s", got)
	}
	n = e.gh.total()
	e.clock.advance(4 * time.Minute)
	get("acme/api")
	if e.gh.total() != n {
		t.Fatal("no default-branch commit counted as a failure: not kept five minutes")
	}

	// The checks failing leave them out.
	e.clock.advance(5 * time.Minute)
	e.gh.fail("/check-runs", 1, 500, nil, `{"message":"Server Error"}`)
	raw, _ = get("acme/web")
	if got := keysOf(raw); got != "at,defaultBranch,etag,mergedPulls,openIssues,openPulls,pushedAt,repo,url" {
		t.Fatalf("checks down: %s", got)
	}

	// The repo itself: GitHub's refusal is the answer.
	e.clock.advance(5 * time.Minute)
	e.gh.fail("GET /repos/acme/web", 1, 404, nil, `{"message":"Not Found"}`)
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil), 404, "not-found")
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/nope", nil), 404, "not-found")
	e.gh.fail("GET /repos/acme/web", 1, 502, nil, `{"message":"Server Error"}`)
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/web", nil), 503, "unavailable")
}

// The identities and policy of every read; the cache is per identity.
func TestRepoStatsIdentities(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedStats(e)
	path := "/scm/repo/stats?repo=acme/web"
	// Global: the bot, for a tile; never a person; no consumer role, nothing.
	ok(t, e.call(e.gH, agentC, "GET", path, nil), 200)
	refusal(t, e.call(e.gH, agentC, "GET", path+"&as=person", nil), 403, "identity")
	refusal(t, e.call(e.gH, nobodyC, "GET", path, nil), 403, "not-allowed")
	// The policy is checked before the cache.
	p := basePolicy()
	p.BotRepos = []string{"acme/api"}
	e.setPolicy(p)
	refusal(t, e.call(e.gH, agentC, "GET", path, nil), 403, "not-allowed")
	ok(t, e.call(e.gH, agentC, "GET", "/scm/repo/stats?repo=acme/api", nil), 200)
	e.setPolicy(basePolicy())

	// A person's partition: the person — sign-in first; the bot refused
	// while botForPeople is off.
	u := e.user("alice").routes()
	refusal(t, e.call(u, personC("alice"), "GET", path, nil), 409, "signin")
	e.signIn("alice", "octocat")
	var st repoStats
	decode(t, e.call(u, personC("alice"), "GET", path, nil), &st)
	if *st.OpenPulls != 2 || *st.OpenIssues != 3 || st.DefaultBranchChecks == nil {
		t.Fatalf("octocat's view: %+v", st)
	}
	refusal(t, e.call(u, personC("alice"), "GET", path+"&as=bot", nil), 403, "identity")
	refusal(t, e.call(u, agentC, "GET", path, nil), 403, "not-allowed") // a tile at a person's partition

	// Under botForPeople on, the bot there is its own identity: not the
	// person's cached answer.
	p = basePolicy()
	p.BotForPeople = "on"
	e.setPolicy(p)
	n := e.gh.count("POST /graphql")
	ok(t, e.call(u, personC("alice"), "GET", path, nil), 200)
	if e.gh.count("POST /graphql") != n {
		t.Fatal("the person's answer wasn't kept")
	}
	ok(t, e.call(u, personC("alice"), "GET", path+"&as=bot", nil), 200)
	if e.gh.count("POST /graphql") != n+1 {
		t.Fatal("the bot answered from the person's entry")
	}

	// Someone who can't see the repo gets 404, whoever read it before.
	ub := e.signIn("bob", "hubot").routes()
	refusal(t, e.call(ub, personC("bob"), "GET", path, nil), 404, "not-found")

	// Unpartitioned: the bot for tiles; a person's consumer, no one.
	_, lh := e.legacy()
	ok(t, e.call(lh, agentC, "GET", path, nil), 200)
	refusal(t, e.call(lh, agentC, "GET", path+"&as=person", nil), 403, "identity")
	refusal(t, e.call(lh, personC("alice"), "GET", path, nil), 403, "identity")
}

// hello lists stats everywhere the route is served: global, a person's
// partition, an unpartitioned copy.
func TestRepoStatsCap(t *testing.T) {
	e := newEnv(t)
	e.setup()
	var h helloResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/hello?protocol=1", nil), &h)
	if !slices.Contains(h.Caps, capStats) {
		t.Fatalf("global caps %v", h.Caps)
	}
	decode(t, e.call(e.user("alice").routes(), personC("alice"), "GET", "/scm/hello", nil), &h)
	if !slices.Contains(h.Caps, capStats) {
		t.Fatalf("partition caps %v", h.Caps)
	}
	_, lh := e.legacy()
	decode(t, e.call(lh, agentC, "GET", "/scm/hello", nil), &h)
	if !slices.Contains(h.Caps, capStats) {
		t.Fatalf("legacy caps %v", h.Caps)
	}
}
