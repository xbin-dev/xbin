// live_test.go — the template's own code against real GitHub: the owner's
// test App, its PAT and a device-flow sign-in (live_helpers_test.go loads
// them). Run by hand, never in CI — every test skips without XBIN_GH_LIVE=1:
//
//	cd builtin-templates/scm-github/_backend
//	XBIN_GH_LIVE=1 go test -count=1 -v -run 'TestLiveApp|TestLiveReads|TestLiveWrites' .
//	XBIN_GH_LIVE=1 go test -count=1 -v -timeout 5m -run TestLiveS4 .
//	XBIN_GH_LIVE=1 go test -count=1 -v -run 'TestLivePerson|TestLiveS2' .     # needs person.json
//	XBIN_GH_LIVE=1 XBIN_GH_LIVE_FORGET=1 go test -count=1 -v -run TestLiveForget .  # LAST: ends the sign-in
//
// The credentials live in ~/.config/xbin-test (XBIN_GH_LIVE_DIR elsewhere):
// github.env (GH_TEST_REPO, GH_APP_ID, GH_CLIENT_ID, GH_CLIENT_SECRET,
// GH_PAT), app.pem, person.json. The repo holds main, feature/hello with
// open pull request 1, issues 2–4 and a CI workflow (jobs pass, fail with a
// warning and an error annotation, slow ~5 min, workflow_dispatch on).
// Each answer a test found is logged as "ANSWER: …" (what the live-check record
// was written from); nothing logged carries a token.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveAppBot: the App's JWT, installation lookup, bot tokens minted
// down-scoped (repositories + permissions) and what they list, the identity
// check refusing a PAT, and S3 — whether DELETE /installation/token revokes
// a stateless ghs_ token.
func TestLiveAppBot(t *testing.T) {
	l := newLive(t)
	n := &liveNote{t: t}
	n.add("GET /app with the template's JWT (iss = client id): App %d, slug %s, owner %s, permissions %v", l.app.AppID, l.app.Slug, l.app.Owner, l.app.Permissions)
	n.add("bot user %s[bot] id %d (GET /users/<slug>[bot], anonymous)", l.app.Slug, l.app.BotID)

	// Hook config, read only (Paste would PATCH it).
	auth, err := l.global.appAuth()
	if err != nil {
		t.Fatal(err)
	}
	var hook map[string]any
	if _, err := l.global.gh.call(context.Background(), auth, "GET", liveAPI+"/app/hook/config", nil, &hook); err != nil {
		n.add("GET /app/hook/config: %v", err)
	} else {
		keys := []string{}
		for k := range hook {
			keys = append(keys, k)
		}
		n.add("GET /app/hook/config keys %v (url set: %v)", keys, hook["url"] != "" && hook["url"] != nil)
	}

	// Paste itself (POST /setup/app, no hookUrl) — only while the App has
	// no webhook config, so nothing at GitHub is changed.
	if hc := l.raw("GET", "/app/hook/config", auth.bearer, false, nil, nil); hc.Status == 404 {
		g2 := newSrv("global", tilePath, newMemKV(), newMemKV(), newMemVault(), l.hc, time.Now)
		r := l.call(g2.routes(), ownerC, "POST", "/setup/app", map[string]any{"appId": l.c.appID, "clientId": l.c.clientID,
			"clientSecret": l.c.clientSecret.Reveal(), "privateKey": l.c.pem.Reveal()})
		n.add("POST /setup/app (Paste, no hookUrl) for an App whose webhook is off: %d %s", r.Code, clip(r.Body.String(), 200))
		if r.Code != 200 {
			t.Errorf("Paste of an App with its webhook off: %d", r.Code)
		}
	}

	// Installation of the repo's owner.
	inst, err := l.global.installation(context.Background(), l.owner(), l.name())
	if err != nil {
		t.Fatalf("installation: %v", err)
	}
	n.add("GET /repos/{o}/{r}/installation: id %d", inst)
	if _, err := l.global.installation(context.Background(), "octocat", "hello-world"); !isRefusal(err, refNotInstalled) {
		t.Errorf("an account without the App: want not-installed, got %v", err)
	} else {
		n.add("an account without the App (octocat/hello-world): 409 not-installed")
	}

	// A read bot token, narrowed.
	tr, tok := l.botToken("live:read", "read", nil)
	n.add("POST /scm/token as bot (read): repos %v, permissions %v, expiresAt in %s, token %s (%d chars)", tr.Repos, tr.Permissions,
		time.Until(time.UnixMilli(tr.ExpiresAt)).Round(time.Minute), redact(tok.Reveal()), len(tok.Reveal()))
	if !strings.HasPrefix(tok.Reveal(), "ghs_") {
		t.Errorf("a bot token is ghs_…, got %s", redact(tok.Reveal()))
	}
	if tr.Permissions["contents"] != "read" || tr.Permissions["pull_requests"] != "read" {
		t.Errorf("read preset: %v", tr.Permissions)
	}
	listed := l.raw("GET", "/installation/repositories", tok, false, nil, nil)
	var lr struct {
		Total int `json:"total_count"`
		Repos []struct {
			FullName string `json:"full_name"`
		} `json:"repositories"`
	}
	_ = listed.json(&lr)
	n.add("GET /installation/repositories with it: %d, total_count %d, first %v", listed.Status, lr.Total, lr.Repos)

	// What the down-scoped token may not do: write contents.
	put := l.raw("PUT", "/repos/"+l.c.repo+"/contents/live-denied.txt", tok, false, map[string]string{"message": "denied", "content": "eA=="}, nil)
	n.add("PUT contents with the read token: %d (want 403)", put.Status)

	// A write token asking only for contents: GitHub's answer to narrowed permissions.
	tw, tokW := l.botToken("live:write", "write", map[string]string{"pull_requests": "read"})
	n.add("POST /scm/token write with pull_requests narrowed to read: permissions %v", tw.Permissions)
	_ = tokW

	// The minted token's own answer at GitHub (POST access_tokens, raw):
	// fields the template doesn't keep.
	jwt, _ := l.global.appAuth()
	mint := l.raw("POST", "/app/installations/"+strconv.FormatInt(inst, 10)+"/access_tokens", jwt.bearer, false,
		map[string]any{"repositories": []string{l.name()}, "permissions": map[string]string{"metadata": "read"}}, nil)
	var mm map[string]any
	_ = mint.json(&mm)
	mk := []string{}
	for k := range mm {
		mk = append(mk, k)
	}
	n.add("POST /app/installations/{id}/access_tokens: %d, keys %v, repository_selection %v", mint.Status, mk, mm["repository_selection"])
	minted := newSecret(mm["token"].(string))

	// S3: DELETE /installation/token on a stateless token, then use it.
	before := l.raw("GET", "/repos/"+l.c.repo, minted, false, nil, nil)
	del := l.raw("DELETE", "/installation/token", minted, false, nil, nil)
	after := l.raw("GET", "/repos/"+l.c.repo, minted, false, nil, nil)
	again := l.raw("DELETE", "/installation/token", minted, false, nil, nil)
	n.add("S3: GET with the ghs_ token %d; DELETE /installation/token %d; GET after %d; DELETE again %d", before.Status, del.Status, after.Status, again.Status)
	if after.Status != 401 {
		t.Errorf("S3: a revoked installation token still answers %d", after.Status)
	}

	// The template's own revoke path (POST /scm/token/revoke → DELETE /installation/token).
	r := l.call(l.gH, agentC, "POST", "/scm/token/revoke", map[string]string{"token": tok.Reveal()})
	ok(t, r, 204)
	gone := l.raw("GET", "/repos/"+l.c.repo, tok, false, nil, nil)
	n.add("POST /scm/token/revoke {token} → 204; the token at GitHub after: %d", gone.Status)
	if gone.Status != 401 {
		t.Errorf("the template's revoke left the token answering %d", gone.Status)
	}
	r = l.call(l.gH, ownerC, "POST", "/api/revoke-all", nil)
	n.add("POST /api/revoke-all: %d %s", r.Code, r.Body.String())
	if g := l.raw("GET", "/repos/"+l.c.repo, tokW, false, nil, nil); g.Status != 401 {
		t.Errorf("revoke-all left the write token answering %d", g.Status)
	}

	// The identity check refuses a PAT (only this App's tokens answer).
	pat := l.raw("POST", "/applications/"+l.c.clientID+"/token", secretString{}, true, map[string]string{"access_token": l.c.pat.Reveal()}, nil)
	n.add("POST /applications/{cid}/token with the PAT: %d %s", pat.Status, clip(string(pat.Body), 200))
	if _, _, err := l.global.checkUserToken(context.Background(), l.c.pat); !isRefusal(err, refIdentity) {
		t.Errorf("checkUserToken(PAT): want identity, got %v", err)
	}
	r = l.call(l.gH, relayC("live", "write"), "POST", "/partition/identity", map[string]string{"accessToken": l.c.pat.Reveal()})
	n.add("POST /partition/identity with the PAT: %d", r.Code)
	if r.Code != 403 {
		t.Errorf("the relay registered a PAT: %d", r.Code)
	}
	bogus := l.raw("POST", "/applications/"+l.c.clientID+"/token", secretString{}, true, map[string]string{"access_token": "ghu_" + strings.Repeat("x", 36)}, nil)
	n.add("POST /applications/{cid}/token with a made-up ghu_ token: %d", bogus.Status)
	wrongAuth := l.raw("POST", "/applications/"+l.c.clientID+"/token", l.c.pat, false, map[string]string{"access_token": l.c.pat.Reveal()}, nil)
	n.add("POST /applications/{cid}/token with a bearer PAT instead of basic auth: %d", wrongAuth.Status)
	// The revocation endpoints for tokens this App never issued.
	for _, what := range []string{"token", "grant"} {
		d := l.raw("DELETE", "/applications/"+l.c.clientID+"/"+what, secretString{}, true, map[string]string{"access_token": "ghu_" + strings.Repeat("y", 36)}, nil)
		n.add("DELETE /applications/{cid}/%s with a made-up ghu_ token: %d %s", what, d.Status, clip(string(d.Body), 160))
		d = l.raw("DELETE", "/applications/"+l.c.clientID+"/"+what, secretString{}, true, map[string]string{"access_token": l.c.pat.Reveal()}, nil)
		n.add("DELETE /applications/{cid}/%s with the PAT: %d %s", what, d.Status, clip(string(d.Body), 160))
	}
}

// TestLiveReads: the contract's reads as the bot, against what GitHub
// answers raw — repos, pulls (mergeable), the comment timeline, issues
// (search and the word-only q rule), checks combined, a completed job's
// log, annotations, conditional requests, rate-limit headers, pagination.
func TestLiveReads(t *testing.T) {
	l := newLive(t)
	n := &liveNote{t: t}
	repo := url.QueryEscape(l.c.repo)
	get := func(path string) (*rawRespRec, map[string]any) {
		r := l.call(l.gH, agentC, "GET", path, nil)
		var m map[string]any
		_ = json.Unmarshal(r.Body.Bytes(), &m)
		return &rawRespRec{r.Code, r.Header(), r.Body.Bytes()}, m
	}

	r, m := get("/scm/repos")
	n.add("GET /scm/repos as bot: %d, items %v", r.code, clip(jsonOf(m["items"]), 400))
	r, m = get("/scm/repo?repo=" + repo)
	n.add("GET /scm/repo: %d protected=%v permission=%v defaultBranch=%v", r.code, m["protected"], m["permission"], m["defaultBranch"])
	r, m = get("/scm/pulls?repo=" + repo + "&state=all")
	n.add("GET /scm/pulls state=all: %d, %d items", r.code, lenOf(m["items"]))
	r, m = get("/scm/pulls/1?repo=" + repo)
	n.add("GET /scm/pulls/1: %d mergeable=%v mergeableState=%v retryAfterMs=%v draft=%v head=%v author=%v", r.code, m["mergeable"], m["mergeableState"], m["retryAfterMs"], m["draft"], m["head"], m["author"])
	etag := r.hdr.Get("ETag")
	r2 := l.call(l.gH, agentC, "GET", "/scm/pulls/1?repo="+repo+"&ifNoneMatch="+url.QueryEscape(etag), nil)
	n.add("GET /scm/pulls/1 with ifNoneMatch: %d", r2.Code)
	if r2.Code != 304 {
		t.Errorf("conditional read: want 304, got %d", r2.Code)
	}

	// Comments: one of the bot's own, so the timeline has a comment.
	cr := l.call(l.gH, agentC, "POST", "/scm/pulls/1/comments", map[string]string{"repo": l.c.repo, "body": "live check " + time.Now().UTC().Format(time.RFC3339)})
	n.add("POST /scm/pulls/1/comments as bot: %d", cr.Code)
	// A review with one inline comment, as the owner (the PAT), so the
	// timeline has every kind.
	var pr1 struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	l.raw("GET", "/repos/"+l.c.repo+"/pulls/1", l.c.pat, false, nil, nil).json(&pr1)
	files := l.raw("GET", "/repos/"+l.c.repo+"/pulls/1/files", l.c.pat, false, nil, nil)
	var fl []struct {
		Filename string `json:"filename"`
	}
	_ = files.json(&fl)
	if len(fl) > 0 {
		rv := l.raw("POST", "/repos/"+l.c.repo+"/pulls/1/reviews", l.c.pat, false, map[string]any{"event": "COMMENT", "body": "live review",
			"commit_id": pr1.Head.SHA, "comments": []map[string]any{{"path": fl[0].Filename, "line": 1, "side": "RIGHT", "body": "live inline comment"}}}, nil)
		n.add("a COMMENT review with one inline comment (PAT): %d", rv.Status)
	}
	// A commit status on the pull request's head, from the App (statuses: write).
	if inst, err := l.global.installation(context.Background(), l.owner(), l.name()); err == nil {
		if m, err := l.global.mint(context.Background(), inst, []string{l.name()}, map[string]string{"statuses": "write"}); err == nil {
			st := l.raw("POST", "/repos/"+l.c.repo+"/statuses/"+pr1.Head.SHA, m.Token, false, map[string]string{"state": "success", "context": "live/check", "description": "set by the live checks"}, nil)
			n.add("POST /statuses/{sha} (App, statuses: write): %d", st.Status)
			l.raw("DELETE", "/installation/token", m.Token, false, nil, nil)
		}
	}
	r, m = get("/scm/pulls/1/comments?repo=" + repo)
	kinds := map[string]int{}
	if items, ok := m["items"].([]any); ok {
		for _, it := range items {
			if im, ok := it.(map[string]any); ok {
				kinds[str(im["kind"])]++
				if im["kind"] != "comment" {
					n.add("timeline %s: %s", im["kind"], clip(jsonOf(im), 400))
				}
			}
		}
	}
	n.add("GET /scm/pulls/1/comments: %d, %d items, kinds %v, last %v", r.code, lenOf(m["items"]), kinds, clip(jsonOf(last(m["items"])), 300))

	r, m = get("/scm/issues?repo=" + repo)
	n.add("GET /scm/issues: %d, numbers %v (pull requests left out)", r.code, field(m["items"], "number"))
	r, m = get("/scm/issues?repo=" + repo + "&q=" + url.QueryEscape("login"))
	n.add("GET /scm/issues q=login (search): %d, numbers %v", r.code, field(m["items"], "number"))
	r, m = get("/scm/issues?repo=" + repo + "&q=" + url.QueryEscape("repo:octocat/hello-world is:pr login"))
	n.add("GET /scm/issues q='repo:octocat/hello-world is:pr login' (qualifiers dropped): %d, numbers %v", r.code, field(m["items"], "number"))
	r, m = get("/scm/issues/2?repo=" + repo + "&comments=1")
	n.add("GET /scm/issues/2?comments=1: %d title=%v comments=%d", r.code, m["title"], lenOf(m["comments"]))
	r, _ = get("/scm/issues/1?repo=" + repo)
	n.add("GET /scm/issues/1 (a pull request): %d", r.code)

	// The search itself, raw: what GitHub answers for the template's q.
	jwtRead, err := l.global.instAuth(context.Background(), l.owner(), l.name(), "read")
	if err != nil {
		t.Fatal(err)
	}
	sr := l.raw("GET", "/search/issues?q="+url.QueryEscape("repo:"+l.c.repo+" is:issue login state:open"), jwtRead.bearer, false, nil, nil)
	var sm struct {
		Items []map[string]any `json:"items"`
	}
	_ = sr.json(&sm)
	if len(sm.Items) > 0 {
		n.add("raw search: %d, rate resource %s, repository_url %v", sr.Status, sr.Header.Get("X-RateLimit-Resource"), sm.Items[0]["repository_url"])
	} else {
		n.add("raw search: %d, rate resource %s, no items", sr.Status, sr.Header.Get("X-RateLimit-Resource"))
	}

	// The repo's permissions as the bot sees them, raw.
	for _, u := range []string{"/repos/" + l.c.repo, "/installation/repositories"} {
		pr := l.raw("GET", u, jwtRead.bearer, false, nil, nil)
		var one struct {
			Permissions map[string]bool `json:"permissions"`
			Repos       []struct {
				Permissions map[string]bool `json:"permissions"`
			} `json:"repositories"`
		}
		_ = pr.json(&one)
		if len(one.Repos) > 0 {
			one.Permissions = one.Repos[0].Permissions
		}
		n.add("raw GET %s as the bot: permissions %v (present: %v)", u, one.Permissions, one.Permissions != nil)
	}

	// Checks on main and on the pull request.
	for _, ref := range []string{"main", "pull/1", "feature/hello"} {
		r, m = get("/scm/checks?repo=" + repo + "&ref=" + url.QueryEscape(ref))
		n.add("GET /scm/checks ref=%s: %d state=%v counts=%v runs=%d checks=%d statuses=%v", ref, r.code, m["state"], m["counts"], lenOf(m["workflowRuns"]), lenOf(m["checks"]), clip(jsonOf(m["statuses"]), 300))
	}
	_, m = get("/scm/checks?repo=" + repo + "&ref=main")
	var cks checksResp
	_ = json.Unmarshal([]byte(jsonOf(m)), &cks)
	for _, c := range cks.Checks {
		if c.Status != "completed" {
			n.add("a check not completed on main: %s %s %s (job %s)", c.ID, c.Name, c.Status, c.Job)
		}
	}
	for _, wr := range cks.WorkflowRuns {
		for _, j := range wr.Jobs {
			if j.Status != "completed" {
				n.add("a job not completed on main: run %s (%s) job %s %s %s", wr.ID, wr.Status, j.ID, j.Name, j.Status)
			}
		}
	}
	var failCheck, doneJob, slowJob string
	for _, c := range cks.Checks {
		if c.Name == "fail" && failCheck == "" {
			failCheck = c.ID
		}
	}
	for _, wr := range cks.WorkflowRuns {
		for _, j := range wr.Jobs {
			if j.Status == "completed" && doneJob == "" && j.Name == "pass" {
				doneJob = j.ID
				n.add("a completed job %s: check %s, steps %d, runner %q, url %s", j.Name, j.Check, len(j.Steps), j.Runner, j.URL)
			}
			if j.Name == "slow" && slowJob == "" {
				slowJob = j.ID
			}
		}
	}
	if failCheck != "" {
		r, m = get("/scm/checks/runs/" + failCheck + "/annotations?repo=" + repo)
		n.add("GET annotations of check 'fail': %d %v", r.code, clip(jsonOf(m["items"]), 600))
	} else {
		n.add("no 'fail' check on main yet: annotations not read")
	}
	if doneJob != "" {
		r, m = get("/scm/checks/jobs/" + doneJob + "/log?repo=" + repo + "&tailBytes=300")
		n.add("GET job log (completed): %d bytes=%v from=%v complete=%v truncated=%v text=%q", r.code, m["bytes"], m["from"], m["complete"], m["truncated"], clip(redact(str(m["text"])), 300))
		lg := l.raw("GET", "/repos/"+l.c.repo+"/actions/jobs/"+doneJob+"/logs", jwtRead.bearer, false, nil, nil)
		loc, _ := url.Parse(lg.Header.Get("Location"))
		host := ""
		if loc != nil {
			host = loc.Host
		}
		n.add("raw job logs (completed): %d, Location host %s", lg.Status, host)
	}

	// Conditional requests and the rate limit, raw.
	p1 := l.raw("GET", "/repos/"+l.c.repo+"/pulls/1", jwtRead.bearer, false, nil, nil)
	et := p1.Header.Get("ETag")
	rem1 := p1.Header.Get("X-RateLimit-Remaining")
	p2 := l.raw("GET", "/repos/"+l.c.repo+"/pulls/1", jwtRead.bearer, false, nil, map[string]string{"If-None-Match": et})
	n.add("ETag %s; If-None-Match → %d; X-RateLimit-Remaining %s → %s (limit %s, resource %s, used %s)", et, p2.Status, rem1, p2.Header.Get("X-RateLimit-Remaining"),
		p1.Header.Get("X-RateLimit-Limit"), p1.Header.Get("X-RateLimit-Resource"), p2.Header.Get("X-RateLimit-Used"))
	rl := l.raw("GET", "/rate_limit", jwtRead.bearer, false, nil, nil)
	var rlm struct {
		Resources map[string]map[string]any `json:"resources"`
	}
	_ = rl.json(&rlm)
	keys := []string{}
	for k := range rlm.Resources {
		keys = append(keys, k)
	}
	n.add("GET /rate_limit (installation): %d, resources %v, core %v", rl.Status, keys, rlm.Resources["core"])

	// Pagination: Link with per_page=1.
	pg := l.raw("GET", "/repos/"+l.c.repo+"/issues?state=all&per_page=1", jwtRead.bearer, false, nil, nil)
	n.add("issues per_page=1: Link %s", pg.Header.Get("Link"))
	r, m = get("/scm/issues?repo=" + repo + "&limit=1")
	n.add("GET /scm/issues limit=1: %d, %d item, next=%v", r.code, lenOf(m["items"]), m["next"] != nil)
	if m["next"] != nil {
		r, m = get("/scm/issues?repo=" + repo + "&limit=1&cursor=" + url.QueryEscape(str(m["next"])))
		n.add("…its next page: %d, numbers %v", r.code, field(m["items"], "number"))
	}

	// Poll: the conditional GETs of a pull and checks.
	pr := l.call(l.gH, agentC, "POST", "/scm/poll", map[string]any{"items": []map[string]any{
		{"id": "p", "kind": "pull", "repo": l.c.repo, "number": 1, "etag": etag},
		{"id": "c", "kind": "checks", "repo": l.c.repo, "ref": "pull/1"},
	}})
	n.add("POST /scm/poll: %d %s", pr.Code, clip(pr.Body.String(), 300))
	var pa struct {
		Items []struct {
			ID      string `json:"id"`
			Changed bool   `json:"changed"`
			Etag    string `json:"etag"`
		} `json:"items"`
	}
	_ = json.Unmarshal(pr.Body.Bytes(), &pa)
	if len(pa.Items) == 2 {
		pr = l.call(l.gH, agentC, "POST", "/scm/poll", map[string]any{"items": []map[string]any{
			{"id": "p", "kind": "pull", "repo": l.c.repo, "number": 1, "etag": pa.Items[0].Etag},
			{"id": "c", "kind": "checks", "repo": l.c.repo, "ref": "pull/1", "etag": pa.Items[1].Etag},
		}})
		n.add("POST /scm/poll again with those etags: %d %s", pr.Code, clip(pr.Body.String(), 300))
	}
	_ = slowJob
}

type rawRespRec struct {
	code int
	hdr  http.Header
	body []byte
}

func jsonOf(v any) string { b, _ := json.Marshal(v); return string(b) }
func str(v any) string    { s, _ := v.(string); return s }
func lenOf(v any) int     { l, _ := v.([]any); return len(l) }
func last(v any) any {
	if l, _ := v.([]any); len(l) > 0 {
		return l[len(l)-1]
	}
	return nil
}
func field(v any, k string) []any {
	var out []any
	l, _ := v.([]any)
	for _, it := range l {
		if m, ok := it.(map[string]any); ok {
			out = append(out, m[k])
		}
	}
	return out
}
