package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

var shaB = strings.Repeat("b", 40)

// seedCI puts a workflow run with two jobs, four check runs and two
// statuses on the feature branch's head.
func seedCI(e *env) {
	f := e.gh
	t0 := e.clock.now().Add(-10 * time.Minute)
	ts := func(m int) string { return t0.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	api := f.srv.URL + "/repos/acme/web"
	f.mu.Lock()
	defer f.mu.Unlock()
	run := map[string]any{"id": 7001, "name": "ci", "event": "push", "status": "in_progress", "conclusion": nil, "html_url": f.srv.URL + "/acme/web/actions/runs/7001",
		"run_started_at": ts(0), "updated_at": ts(1), "run_attempt": 1, "head_sha": shaB, "head_branch": "feature"}
	j1 := map[string]any{"id": 88001, "name": "test (ubuntu)", "status": "in_progress", "conclusion": nil, "html_url": f.srv.URL + "/acme/web/actions/runs/7001/job/88001",
		"started_at": ts(1), "completed_at": nil, "runner_name": "", "labels": []string{"ubuntu-24.04"}, "check_run_url": api + "/check-runs/88001",
		"steps": []map[string]any{{"name": "Set up job", "status": "completed", "conclusion": "success", "number": 1, "started_at": ts(1), "completed_at": ts(2)},
			{"name": "go test ./...", "status": "in_progress", "conclusion": nil, "number": 4, "started_at": ts(3)}}}
	j2 := map[string]any{"id": 88002, "name": "lint", "status": "completed", "conclusion": "failure", "html_url": f.srv.URL + "/acme/web/actions/runs/7001/job/88002",
		"started_at": ts(1), "completed_at": ts(4), "runner_name": "GitHub Actions 2", "labels": []string{"ubuntu-latest"}, "check_run_url": api + "/check-runs/88002", "steps": []map[string]any{}}
	f.ci.runs[shaB] = []map[string]any{run}
	f.ci.runByID["7001"] = run
	f.ci.jobs["7001"] = []map[string]any{j1, j2}
	f.ci.jobByID["88001"], f.ci.jobByID["88002"] = j1, j2
	gha := map[string]any{"slug": "github-actions"}
	f.ci.checkRuns[shaB] = []map[string]any{
		{"id": 88001, "name": "test (ubuntu)", "app": gha, "status": "in_progress", "html_url": f.srv.URL + "/acme/web/runs/88001", "details_url": "d1", "started_at": ts(1), "check_suite": map[string]any{"id": 77}},
		{"id": 88002, "name": "lint", "app": gha, "status": "completed", "conclusion": "failure", "html_url": "u2", "started_at": ts(1), "completed_at": ts(4), "check_suite": map[string]any{"id": 77}},
		{"id": 88100, "name": "codecov/patch", "app": map[string]any{"slug": "codecov"}, "status": "completed", "conclusion": "failure", "html_url": "u3", "details_url": "https://app.codecov.io/x",
			"output": map[string]any{"title": "62% of diff hit (target 80%)", "summary": strings.Repeat("s", 5000), "annotations_count": 3}, "check_suite": map[string]any{"id": 78}},
		{"id": 88200, "name": "docs", "status": "completed", "conclusion": "neutral", "html_url": "u4"},
	}
	f.ci.statuses[shaB] = []map[string]any{{"context": "ci/jenkins", "state": "success", "target_url": "https://jenkins", "description": "Build #12 passed", "updated_at": ts(2)},
		{"context": "deploy", "state": "pending"}}
	f.ci.pulls["acme/web"] = []*fPull{{Number: 1, Title: "x", Head: "feature", HeadSHA: shaB, Base: "main", State: "open", User: "octocat"}}
}

func TestChecksCombined(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedCI(e)
	var c checksResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=feature", nil), &c)
	if c.SHA != shaB || c.Ref != "feature" || c.State != "pending" {
		t.Fatalf("head: %s %s %s", c.SHA, c.Ref, c.State)
	}
	if c.Counts != (counts{Total: 6, Success: 1, Failure: 2, Pending: 2, Neutral: 1}) {
		t.Fatalf("counts: %+v", c.Counts)
	}
	if len(c.WorkflowRuns) != 1 {
		t.Fatalf("runs: %+v", c.WorkflowRuns)
	}
	wr := c.WorkflowRuns[0]
	if wr.ID != "7001" || wr.Status != "in_progress" || wr.Conclusion != "" || wr.Attempt != 1 || wr.HeadBranch != "feature" || len(wr.Jobs) != 2 {
		t.Fatalf("run: %+v", wr)
	}
	j := wr.Jobs[0]
	if j.ID != "88001" || j.Check != "88001" || j.Runner != "ubuntu-24.04" || len(j.Steps) != 2 || j.Steps[1].N != 4 || j.Steps[1].Status != "in_progress" || j.CompletedAt != 0 {
		t.Fatalf("job: %+v", j)
	}
	if wr.Jobs[1].Runner != "GitHub Actions 2" || wr.Jobs[1].Conclusion != "failure" {
		t.Fatalf("job 2: %+v", wr.Jobs[1])
	}
	byID := map[string]checkRun{}
	for _, x := range c.Checks {
		byID[x.ID] = x
	}
	if byID["88001"].Job != "88001" || byID["88001"].Suite != "77" || byID["88001"].App != "github-actions" || byID["88100"].Job != "" ||
		byID["88100"].Annotations != 3 || len(byID["88100"].Summary) != 4096 || byID["88100"].DetailsURL != "https://app.codecov.io/x" {
		t.Fatalf("checks: %+v", byID)
	}
	if len(c.Statuses) != 2 || c.Statuses[0].Context != "ci/jenkins" || c.Statuses[0].Description != "Build #12 passed" {
		t.Fatalf("statuses: %+v", c.Statuses)
	}
	// The same through pull/<n> and the sha; cached five seconds.
	n := e.gh.count("GET /repos/acme/web/commits/" + shaB + "/check-runs")
	var c2 checksResp
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=pull/1", nil), &c2)
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref="+shaB, nil), &c2)
	if c2.SHA != shaB || e.gh.count("GET /repos/acme/web/commits/"+shaB+"/check-runs") != n {
		t.Fatal("not cached for 5 s")
	}
	// Everything done: failure; all green: success; nothing: none.
	e.clock.advance(6 * time.Second)
	e.gh.mu.Lock()
	for _, cr := range e.gh.ci.checkRuns[shaB] {
		cr["status"] = "completed"
		if cr["conclusion"] == nil {
			cr["conclusion"] = "success"
		}
	}
	e.gh.ci.jobByID["88001"]["status"], e.gh.ci.jobByID["88001"]["conclusion"] = "completed", "success"
	e.gh.ci.statuses[shaB] = e.gh.ci.statuses[shaB][:1]
	e.gh.mu.Unlock()
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=feature", nil), &c)
	if c.State != "failure" || c.Counts.Pending != 0 {
		t.Fatalf("done: %s %+v", c.State, c.Counts)
	}
	e.clock.advance(6 * time.Second)
	e.gh.mu.Lock()
	for _, cr := range e.gh.ci.checkRuns[shaB] {
		cr["conclusion"] = "success"
	}
	e.gh.mu.Unlock()
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=feature", nil), &c)
	if c.State != "success" {
		t.Fatalf("green: %s %+v", c.State, c.Counts)
	}
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web&ref=main", nil), &c)
	if c.State != "none" || c.Counts.Total != 0 || c.WorkflowRuns == nil || c.Checks == nil || c.Statuses == nil {
		t.Fatalf("nothing: %+v", c)
	}
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/checks?repo=acme/web", nil), 400, "invalid")
}

func TestJobLogCompletedOnly(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedCI(e)
	var b strings.Builder
	for i := 0; i < 10000; i++ {
		fmt.Fprintf(&b, "\x1b[32mline %05d\x1b[0m\n", i) // 20 bytes a line
	}
	text := b.String()
	e.gh.mu.Lock()
	e.gh.ci.logs["88002"] = text
	e.gh.mu.Unlock()
	x := refusal(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88001/log?repo=acme/web", nil), 409, "in-progress")
	if !strings.HasSuffix(x.URL, "/acme/web/actions/runs/7001/job/88001") {
		t.Fatalf("url: %q", x.URL)
	}
	var l jobLog
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88002/log?repo=acme/web", nil), &l)
	total := int64(len(text))
	if l.Bytes != total || !l.Complete || !l.Truncated || l.From <= total-65536 || l.From%20 != 0 || !strings.HasPrefix(l.Text, "\x1b[32mline") ||
		int64(len(l.Text)) != total-l.From || !strings.HasSuffix(l.Text, "line 09999\x1b[0m\n") || l.ID != "88002" {
		t.Fatalf("tail: bytes %d from %d len %d %q…", l.Bytes, l.From, len(l.Text), l.Text[:30])
	}
	// Paging back: until the last start.
	var l2 jobLog
	decode(t, e.call(e.gH, agentC, "GET", fmt.Sprintf("/scm/checks/jobs/88002/log?repo=acme/web&until=%d&tailBytes=200", l.From), nil), &l2)
	if l2.From != l.From-200 || len(l2.Text) != 200 || !strings.HasSuffix(text[:l.From], l2.Text) {
		t.Fatalf("page back: from %d len %d", l2.From, len(l2.Text))
	}
	// since: from a byte, cut forward to a line's start.
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88002/log?repo=acme/web&since=5&tailBytes=1048576", nil), &l2)
	if l2.From != 20 || !l2.Truncated || int64(len(l2.Text)) != total-20 {
		t.Fatalf("since: from %d", l2.From)
	}
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88002/log?repo=acme/web&since=0&tailBytes=1048576", nil), &l2)
	if l2.From != 0 || l2.Truncated || int64(len(l2.Text)) != total {
		t.Fatalf("whole: from %d trunc %v", l2.From, l2.Truncated)
	}
	// Downloaded once (completed logs don't change).
	if n := e.gh.count("GET /_blob/logs/88002"); n != 1 {
		t.Fatalf("downloaded %d times", n)
	}
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/99999/log?repo=acme/web", nil), 404, "not-found")
	refusal(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88002/log?repo=acme/web&since=-1", nil), 400, "invalid")
	// The log's host unreachable (a narrowed net binding): 502 naming it.
	e.gh.mu.Lock()
	e.gh.ci.jobByID["88003"] = map[string]any{"id": 88003, "status": "completed", "html_url": "h"}
	e.gh.ci.logs["88003"] = "REDIRECT:http://127.0.0.1:1/blob"
	e.gh.mu.Unlock()
	x = refusal(t, e.call(e.gH, agentC, "GET", "/scm/checks/jobs/88003/log?repo=acme/web", nil), 502, "upstream")
	if !strings.Contains(x.Message, "127.0.0.1:1") || strings.Contains(x.Message, "sig=") {
		t.Fatalf("egress: %q", x.Message)
	}
}

// sliceLog keeps to the 8 MiB kept of a bigger log.
func TestJobLogKeepsTail(t *testing.T) {
	e := &logEntry{data: []byte("xx\nline a\nline b\n"), total: 1000}
	l := sliceLog("1", e, 1<<20, 0, 0)
	if l.From != 1000-int64(len(e.data))+3 || l.Text != "line a\nline b\n" || !l.Truncated {
		t.Fatalf("%+v", l)
	}
	keptFrom := 1000 - int64(len(e.data))
	// until before what's kept: nothing of it is here — empty, from where
	// the kept part starts, truncated (never a panic).
	for _, until := range []int64{5, keptFrom - 1, keptFrom} {
		l = sliceLog("1", e, 65536, 0, until)
		if l.Text != "" || l.From != keptFrom || !l.Truncated || l.Bytes != 1000 {
			t.Fatalf("until %d: %+v", until, l)
		}
	}
	// since past the end (or past until): empty at the end.
	for _, c := range [][2]int64{{2000, 0}, {999, 995}} {
		l = sliceLog("1", e, 65536, c[0], c[1])
		if l.Text != "" {
			t.Fatalf("since %d until %d: %+v", c[0], c[1], l)
		}
	}
	// A window inside what's kept.
	l = sliceLog("1", e, 65536, keptFrom+3, keptFrom+10)
	if l.Text != "line a\n" || l.From != keptFrom+3 {
		t.Fatalf("window: %+v", l)
	}
}

func TestAnnotations(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedCI(e)
	e.gh.mu.Lock()
	e.gh.ci.annots["88100"] = []map[string]any{
		{"path": "a.go", "start_line": 1, "end_line": 2, "annotation_level": "notice", "title": "t", "message": "m1"},
		{"path": "b.go", "start_line": 3, "end_line": 3, "annotation_level": "warning", "message": strings.Repeat("w", 5000)},
		{"path": "c.go", "start_line": 9, "end_line": 9, "annotation_level": "failure", "message": "m3"},
	}
	e.gh.mu.Unlock()
	var pg page[annotation]
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/runs/88100/annotations?repo=acme/web&limit=2", nil), &pg)
	if len(pg.Items) != 2 || pg.Items[0].Level != "notice" || pg.Items[1].Level != "warning" || len(pg.Items[1].Message) != 4096 || pg.Next == "" {
		t.Fatalf("page 1: %+v", pg)
	}
	next := pg.Next
	pg = page[annotation]{}
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/runs/88100/annotations?repo=acme/web&limit=2&cursor="+next, nil), &pg)
	if len(pg.Items) != 1 || pg.Items[0].Level != "failure" || pg.Items[0].Path != "c.go" || pg.Next != "" {
		t.Fatalf("page 2: %+v", pg)
	}
	pg = page[annotation]{}
	decode(t, e.call(e.gH, agentC, "GET", "/scm/checks/runs/88100/annotations?repo=acme/web&limit=500", nil), &pg)
	if len(pg.Items) != 3 {
		t.Fatalf("limit clamp: %d", len(pg.Items))
	}
}

// A rerun: offered only with the App's actions: write and allowRerun; a
// person's own, never the bot's; refused while the run goes.
func TestRerunCapAndIdentity(t *testing.T) {
	e := newEnv(t)
	e.setup()
	seedCI(e)
	body := map[string]any{"repo": "acme/web", "runId": "7001", "failedOnly": true}
	refusal(t, e.call(e.gH, agentC, "POST", "/scm/checks/rerun", body), 403, "identity")
	e.gh.mu.Lock()
	e.gh.appPerms["actions"] = "write"
	e.gh.mu.Unlock()
	e.setup()
	x := refusal(t, e.call(e.gH, agentC, "POST", "/scm/checks/rerun", body), 403, "identity")
	if strings.Join(x.Identities, ",") != "person" {
		t.Fatalf("identities %v", x.Identities)
	}
	u := e.signIn("alice", "octocat").routes()
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/checks/rerun", map[string]any{"repo": "acme/web", "runId": "7001", "as": "bot"}), 403, "identity")
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/checks/rerun", body), 409, "exists")
	e.gh.mu.Lock()
	e.gh.ci.runByID["7001"]["status"] = "completed"
	e.gh.mu.Unlock()
	r := e.call(u, personC("alice"), "POST", "/scm/checks/rerun", body)
	ok(t, r, 202)
	var rr rerunResp
	decode(t, r, &rr)
	if rr.RunID != "7001" || rr.Attempt != 2 {
		t.Fatalf("rerun: %+v", rr)
	}
	e.gh.mu.Lock()
	got := strings.Join(e.gh.ci.reruns, ";")
	e.gh.mu.Unlock()
	if got != "rerun-failed-jobs 7001 user" {
		t.Fatalf("reruns: %s", got)
	}
	p := basePolicy()
	p.AllowRerun = false
	e.setPolicy(p)
	refusal(t, e.call(u, personC("alice"), "POST", "/scm/checks/rerun", body), 501, "unsupported")
}
