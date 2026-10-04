// live_ci_test.go — live checks of CI in progress (S4) and of the writes
// the contract makes (a branch, a draft pull request, create idempotence,
// ready-for-review and back through GraphQL, close). See live_test.go for
// how they are run.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestLiveS4 dispatches the CI workflow (workflow_dispatch, with the PAT),
// waits for job `slow` to run, then asks for its log while it runs: GitHub's
// job-log endpoint raw (and wherever it redirects), the run's log archive,
// and the template's GET /scm/checks/jobs/{id}/log (409 in-progress wanted
// while no partial log exists).
func TestLiveS4(t *testing.T) {
	l := newLive(t)
	n := &liveNote{t: t}
	ctx := context.Background()
	read, err := l.global.instAuth(ctx, l.owner(), l.name(), "read")
	if err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-5 * time.Second)
	d := l.raw("POST", "/repos/"+l.c.repo+"/actions/workflows/ci.yml/dispatches", l.c.pat, false, map[string]string{"ref": "main"}, nil)
	n.add("workflow_dispatch (PAT): %d", d.Status)
	if d.Status != 204 && d.Status != 200 {
		t.Fatalf("dispatch: %d", d.Status)
	}
	var runID, jobID int64
	var jobURL string
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(5 * time.Second)
		if runID == 0 {
			rr := l.raw("GET", "/repos/"+l.c.repo+"/actions/runs?event=workflow_dispatch&per_page=5", read.bearer, false, nil, nil)
			var runs struct {
				Runs []struct {
					ID        int64  `json:"id"`
					CreatedAt string `json:"created_at"`
				} `json:"workflow_runs"`
			}
			_ = rr.json(&runs)
			for _, r := range runs.Runs {
				if at, _ := time.Parse(time.RFC3339, r.CreatedAt); at.After(since) {
					runID = r.ID
					break
				}
			}
			continue
		}
		jr := l.raw("GET", "/repos/"+l.c.repo+"/actions/runs/"+strconv.FormatInt(runID, 10)+"/jobs?filter=latest", read.bearer, false, nil, nil)
		var js struct {
			Jobs []struct {
				ID      int64  `json:"id"`
				Name    string `json:"name"`
				Status  string `json:"status"`
				HTMLURL string `json:"html_url"`
				Steps   []struct {
					Name   string `json:"name"`
					Status string `json:"status"`
				} `json:"steps"`
			} `json:"jobs"`
		}
		_ = jr.json(&js)
		for _, j := range js.Jobs {
			if j.Name != "slow" || j.Status != "in_progress" {
				continue
			}
			for _, st := range j.Steps {
				if st.Name == "Count for five minutes" && st.Status == "in_progress" {
					jobID, jobURL = j.ID, j.HTMLURL
				}
			}
		}
		if jobID != 0 {
			break
		}
	}
	if jobID == 0 {
		t.Fatalf("job slow didn't reach its counting step in time (run %d)", runID)
	}
	n.add("run %d, job slow %d in progress (%s)", runID, jobID, jobURL)
	time.Sleep(20 * time.Second) // some ticks printed

	probe := func(label string) {
		lg := l.raw("GET", "/repos/"+l.c.repo+"/actions/jobs/"+strconv.FormatInt(jobID, 10)+"/logs", read.bearer, false, nil, nil)
		loc := lg.Header.Get("Location")
		host := ""
		if u, err := url.Parse(loc); err == nil {
			host = u.Host
		}
		n.add("S4 %s: GET /actions/jobs/{id}/logs while running → %d (Location host %q) %s", label, lg.Status, host, clip(string(lg.Body), 200))
		if lg.Status == http.StatusFound && loc != "" {
			resp, err := http.Get(loc)
			if err == nil {
				b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
				resp.Body.Close()
				n.add("S4 %s: the redirect's target → %d, %d bytes, last line %q", label, resp.StatusCode, len(b), clip(lastLine(string(b)), 120))
			}
		}
	}
	probe("first")
	ar := l.raw("GET", "/repos/"+l.c.repo+"/actions/runs/"+strconv.FormatInt(runID, 10)+"/logs", read.bearer, false, nil, nil)
	n.add("S4: GET /actions/runs/{id}/logs (the archive) while running → %d", ar.Status)

	r := l.call(l.gH, agentC, "GET", "/scm/checks/jobs/"+strconv.FormatInt(jobID, 10)+"/log?repo="+url.QueryEscape(l.c.repo), nil)
	var e scmErr
	_ = json.Unmarshal(r.Body.Bytes(), &e)
	n.add("S4: GET /scm/checks/jobs/{id}/log while running → %d %s url=%s", r.Code, e.Refusal, e.URL)
	if r.Code != 409 || e.Refusal != refInProgress {
		t.Errorf("a running job's log: want 409 in-progress, got %d %s", r.Code, r.Body)
	}

	// The check run of the job while it runs, and the combined view.
	cr := l.raw("GET", "/repos/"+l.c.repo+"/check-runs/"+strconv.FormatInt(jobID, 10), read.bearer, false, nil, nil)
	var crm struct {
		Status string `json:"status"`
		Output struct {
			Title   *string `json:"title"`
			Summary *string `json:"summary"`
			Count   int     `json:"annotations_count"`
		} `json:"output"`
	}
	_ = cr.json(&crm)
	n.add("the job's check run (same id) while running: %d status=%s annotations=%d", cr.Status, crm.Status, crm.Output.Count)
	rc := l.call(l.gH, agentC, "GET", "/scm/checks?repo="+url.QueryEscape(l.c.repo)+"&ref=main", nil)
	var ck checksResp
	_ = json.Unmarshal(rc.Body.Bytes(), &ck)
	for _, wr := range ck.WorkflowRuns {
		if wr.ID != strconv.FormatInt(runID, 10) {
			continue
		}
		for _, j := range wr.Jobs {
			done := 0
			for _, s := range j.Steps {
				if s.Status == "completed" {
					done++
				}
			}
			n.add("GET /scm/checks main: run %s %s, job %s %s, steps %d/%d, runner %q, check %s", wr.ID, wr.Status, j.Name, j.Status, done, len(j.Steps), j.Runner, j.Check)
		}
	}
	n.add("GET /scm/checks main while running: state=%s counts=%+v", ck.State, ck.Counts)
	time.Sleep(15 * time.Second)
	probe("15 s later")
}

func lastLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			return s[i+1:]
		}
	}
	return s
}

// TestLiveWrites: a branch with one commit (a bot write token), a draft pull
// request through POST /scm/pulls, the same again (200 existing), the same
// clientId for something else (409), mergeable while GitHub computes it,
// ready for review and back to draft through GraphQL, then closed and the
// branch deleted.
func TestLiveWrites(t *testing.T) {
	l := newLive(t)
	n := &liveNote{t: t}
	_, tok := l.botToken("live:writes", "write", nil)
	branch := "live/" + time.Now().UTC().Format("20060102-150405")
	ref := l.raw("GET", "/repos/"+l.c.repo+"/git/ref/heads/main", tok, false, nil, nil)
	var rm struct {
		Object struct {
			SHA string `json:"sha"`
		} `json:"object"`
	}
	_ = ref.json(&rm)
	cr := l.raw("POST", "/repos/"+l.c.repo+"/git/refs", tok, false, map[string]string{"ref": "refs/heads/" + branch, "sha": rm.Object.SHA}, nil)
	if cr.Status != 201 {
		t.Fatalf("create branch: %d", cr.Status)
	}
	t.Cleanup(func() {
		d := l.raw("DELETE", "/repos/"+l.c.repo+"/git/refs/heads/"+url.PathEscape(branch), tok, false, nil, nil)
		t.Logf("branch %s deleted: %d", branch, d.Status)
	})
	put := l.raw("PUT", "/repos/"+l.c.repo+"/contents/live/"+url.PathEscape(branch[5:])+".txt", tok, false, map[string]string{
		"message": "live check [skip ci]", "content": base64.StdEncoding.EncodeToString([]byte("live " + branch + "\n")), "branch": branch}, nil)
	n.add("branch %s with one commit (bot write token): ref %d, contents %d", branch, cr.Status, put.Status)

	body := map[string]any{"repo": l.c.repo, "head": branch, "title": "live check " + branch, "body": "made by the live checks", "draft": true, "clientId": "live-" + branch}
	r := l.call(l.gH, agentC, "POST", "/scm/pulls", body)
	var p pullInfo
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	n.add("POST /scm/pulls draft: %d number=%d draft=%v mergeable=%v mergeableState=%s retryAfterMs=%d author=%+v", r.Code, p.Number, p.Draft, p.Mergeable, p.MergeableState, p.RetryAfterMs, p.Author)
	if r.Code != 201 || !p.Draft {
		wa, err := l.global.instAuth(context.Background(), l.owner(), l.name(), "write")
		if err != nil {
			t.Fatal(err)
		}
		raw := l.raw("POST", "/repos/"+l.c.repo+"/pulls", wa.bearer, false, map[string]any{"head": branch, "base": "main", "title": "live draft", "draft": true}, nil)
		n.add("raw POST /pulls with the tile's internal write token (pull_requests, issues: write; metadata: read): %d %s", raw.Status, clip(string(raw.Body), 400))
		raw = l.raw("POST", "/repos/"+l.c.repo+"/pulls", tok, false, map[string]any{"head": branch, "base": "main", "title": "live draft", "draft": true}, nil)
		n.add("raw POST /pulls draft:true: %d %s", raw.Status, clip(string(raw.Body), 400))
		if raw.Status == 201 {
			t.Fatalf("open a draft: %d %s", r.Code, r.Body)
		}
		delete(body, "draft")
		r = l.call(l.gH, agentC, "POST", "/scm/pulls", body)
		_ = json.Unmarshal(r.Body.Bytes(), &p)
		n.add("POST /scm/pulls (not a draft: GitHub refuses drafts here): %d number=%d draft=%v mergeable=%v mergeableState=%s retryAfterMs=%d author=%+v", r.Code, p.Number, p.Draft, p.Mergeable, p.MergeableState, p.RetryAfterMs, p.Author)
		if r.Code != 201 {
			t.Fatalf("open a pull: %d %s", r.Code, r.Body)
		}
	}
	num := strconv.Itoa(p.Number)
	t.Cleanup(func() {
		l.call(l.gH, agentC, "PATCH", "/scm/pulls/"+num, map[string]any{"repo": l.c.repo, "state": "closed"})
	})
	r = l.call(l.gH, agentC, "POST", "/scm/pulls", body)
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	n.add("POST /scm/pulls again, same clientId: %d existing=%v number=%d", r.Code, p.Existing, p.Number)
	delete(body, "clientId")
	r = l.call(l.gH, agentC, "POST", "/scm/pulls", body)
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	n.add("POST /scm/pulls again, no clientId (GitHub's 422 → find it): %d existing=%v number=%d", r.Code, p.Existing, p.Number)
	if r.Code != 200 || !p.Existing {
		t.Errorf("create idempotence: want 200 existing, got %d %s", r.Code, r.Body)
	}
	// GitHub's own 422 for it, raw.
	dup := l.raw("POST", "/repos/"+l.c.repo+"/pulls", tok, false, map[string]any{"head": branch, "base": "main", "title": "dup"}, nil)
	n.add("raw POST /pulls for a head already open: %d %s", dup.Status, clip(string(dup.Body), 300))
	body["clientId"], body["title"] = "live-"+branch, "another title"
	r = l.call(l.gH, agentC, "POST", "/scm/pulls", body)
	n.add("POST /scm/pulls same clientId, another title: %d", r.Code)

	for i := 0; i < 5; i++ {
		r = l.call(l.gH, agentC, "GET", "/scm/pulls/"+num+"?repo="+url.QueryEscape(l.c.repo), nil)
		p = pullInfo{}
		_ = json.Unmarshal(r.Body.Bytes(), &p)
		n.add("GET /scm/pulls/%s (%d): mergeable=%v mergeableState=%s retryAfterMs=%d", num, i, p.Mergeable, p.MergeableState, p.RetryAfterMs)
		if p.Mergeable != nil {
			break
		}
		time.Sleep(3 * time.Second)
	}

	// To draft and back (GraphQL). Ready first only if it opened as a draft.
	steps := []bool{true, false}
	if p.Draft {
		steps = []bool{false, true}
	}
	for _, d := range steps {
		r = l.call(l.gH, agentC, "PATCH", "/scm/pulls/"+num, map[string]any{"repo": l.c.repo, "draft": d, "title": "live check draft=" + strconv.FormatBool(d)})
		var e scmErr
		_ = json.Unmarshal(r.Body.Bytes(), &e)
		p = pullInfo{}
		_ = json.Unmarshal(r.Body.Bytes(), &p)
		n.add("PATCH draft:%v + title (GraphQL, the bot's write token): %d draft=%v title=%q %s", d, r.Code, p.Draft, p.Title, e.Message)
		if r.Code != 200 && !d {
			// Which installation token GraphQL takes for it, raw.
			var node struct {
				NodeID string `json:"node_id"`
			}
			l.raw("GET", "/repos/"+l.c.repo+"/pulls/"+num, tok, false, nil, nil).json(&node)
			q := map[string]any{"query": "mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }", "variables": map[string]any{"id": node.NodeID}}
			inst, _ := l.global.installation(context.Background(), l.owner(), l.name())
			for _, perms := range []map[string]string{
				{"pull_requests": "write", "contents": "read", "metadata": "read"},
				{"pull_requests": "write", "contents": "write", "metadata": "read"},
			} {
				m, err := l.global.mint(context.Background(), inst, []string{l.name()}, perms)
				if err != nil {
					t.Fatal(err)
				}
				gq := l.raw("POST", "/graphql", m.Token, false, q, nil)
				n.add("raw GraphQL markPullRequestReadyForReview with a token of %v: %d %s", perms, gq.Status, clip(string(gq.Body), 200))
				l.raw("DELETE", "/installation/token", m.Token, false, nil, nil)
				if gq.Status == 200 && !bytesContains(gq.Body, "errors") {
					break
				}
			}
		}
	}
	// GraphQL raw with the bot token: the answer's shape for a bad node id.
	gq := l.raw("POST", "/graphql", tok, false, map[string]any{"query": "mutation($id: ID!) { markPullRequestReadyForReview(input: {pullRequestId: $id}) { pullRequest { isDraft } } }",
		"variables": map[string]any{"id": "PR_doesnotexist"}}, nil)
	n.add("raw GraphQL with an unknown node id: %d %s", gq.Status, clip(string(gq.Body), 300))
	r = l.call(l.gH, agentC, "PATCH", "/scm/pulls/"+num, map[string]any{"repo": l.c.repo, "state": "closed"})
	_ = json.Unmarshal(r.Body.Bytes(), &p)
	n.add("PATCH state:closed: %d state=%s", r.Code, p.State)
	r = l.call(l.gH, agentC, "GET", "/scm/pulls?repo="+url.QueryEscape(l.c.repo)+"&state=closed&head="+url.QueryEscape(branch), nil)
	n.add("GET /scm/pulls state=closed head=%s: %d %s", branch, r.Code, clip(r.Body.String(), 200))
}

func bytesContains(b []byte, s string) bool { return strings.Contains(string(b), s) }
