// scm_ci.go — what a read of a task's head's checks does (API.md §scm
// events and polling, "CI"): the read an event asked for
// (checks.completed, after policy.ci.delaySec) or a poll made.
//
//   - failure: each failing suite on the head is acted on once (scm_seen
//     "sem:…:checks:<sha>:<suite>:failure", whichever of an event or a poll
//     read it first). With policy.ci.autoFix the task gets one input: up to
//     3 failing jobs, each with its failing step and the last 120 lines of
//     its log (a 409 in-progress: the steps alone), ANSI stripped, redacted,
//     ≤ policy.ci.logBytes in all, framed as untrusted — queued (dedupe
//     ci:<n>:<sha>:<suite>) with a quiet ci.failed event; at most
//     policy.ci.maxPerDay such inputs per task per day (project_tasks
//     .ci_fixes), past which a waking ci.stuck event instead. Without
//     autoFix: the ci.failed event only.
//   - success, with the task's PR open on that head: the PR's checks are
//     green — the task awaits review — and a waking pr.ready event.
//   - pending: the PR's checks say so; the row reads again.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// scmFailed: a check's (or a job's) conclusion that fails CI.
func scmFailed(conclusion string) bool {
	switch conclusion {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	}
	return false
}

// scmFail is one failing suite of a commit: its failing jobs, the failing
// checks no job reports, its failing statuses.
type scmFail struct {
	suite    string
	jobs     []scmJob
	checks   []scmCheck
	statuses []scmStatus
}

// scmFailures groups c's failures by suite (a check's suite; a job's is
// its check's, else its run's; a status is its own), in the order they
// come.
func scmFailures(c *scmChecks) []*scmFail {
	var out []*scmFail
	by := map[string]*scmFail{}
	get := func(s string) *scmFail {
		f := by[s]
		if f == nil {
			f = &scmFail{suite: s}
			by[s] = f
			out = append(out, f)
		}
		return f
	}
	suiteOf := map[string]string{}
	for _, ch := range c.Checks {
		suiteOf[ch.ID] = ch.Suite
	}
	byJob := map[string]bool{}
	for _, run := range c.WorkflowRuns {
		for _, j := range run.Jobs {
			if !scmFailed(j.Conclusion) {
				continue
			}
			s := suiteOf[j.Check]
			if s == "" {
				s = "run:" + run.ID
			}
			f := get(s)
			f.jobs = append(f.jobs, j)
			byJob[j.ID] = true
		}
	}
	for _, ch := range c.Checks {
		if !scmFailed(ch.Conclusion) || (ch.Job != "" && byJob[ch.Job]) {
			continue
		}
		s := ch.Suite
		if s == "" {
			s = "check:" + ch.ID
		}
		f := get(s)
		f.checks = append(f.checks, ch)
	}
	for _, st := range c.Statuses {
		if st.State == "failure" || st.State == "error" {
			f := get("status:" + st.Context)
			f.statuses = append(f.statuses, st)
		}
	}
	return out
}

// scmCIFixes is project_tasks.ci_fixes: CI-fix inputs a task got today.
type scmCIFixes struct {
	Day string `json:"day"`
	N   int    `json:"n"`
}

func scmToday() string { return time.UnixMilli(scmClock()).UTC().Format("2006-01-02") }

// scmFixesToday is how many CI-fix inputs task k got today.
func scmFixesToday(k *ProjectTask) int {
	var f scmCIFixes
	_ = json.Unmarshal(k.CIFixes, &f)
	if f.Day != scmToday() {
		return 0
	}
	return f.N
}

// scmApplyChecks acts on a read of task r's head's checks (see the file's
// comment). It answers the state read and whether the row is done with
// this head.
func scmApplyChecks(ctx context.Context, d *DB, api scmAPI, r *scmPollRow, c *scmChecks) (string, bool) {
	p, k, ok := scmLiveTask(d, r.Project, r.N)
	if !ok {
		return c.State, true
	}
	sha := strings.ToLower(orStr(c.SHA, r.Item.Ref))
	if !strings.EqualFold(sha, scmHead(d, p, k, r.Repo)) {
		return c.State, true // superseded: the row follows the new head
	}
	pol := policyOf(p.Policy).CI
	switch c.State {
	case "failure":
		var fresh []*scmFail
		for _, f := range scmFailures(c) {
			if !scmSeen(d, scmSemKey(p.ID, k.N, scmKindChecks, sha, f.suite, "failure")) {
				fresh = append(fresh, f)
			}
		}
		if len(fresh) == 0 {
			return c.State, true
		}
		body := ""
		if pol.AutoFix && scmFixesToday(k) < pol.MaxPerDay {
			body = scmCIBody(ctx, api, p, r.Repo, r.As, fresh, pol.LogBytes)
		}
		_ = d.Tx(func(t *DB) error {
			p, k, ok := scmLiveTask(t, r.Project, r.N)
			if !ok || !strings.EqualFold(sha, scmHead(t, p, k, r.Repo)) {
				return nil
			}
			var won []*scmFail
			for _, f := range fresh {
				if scmSeenOnce(t, scmSemKey(p.ID, k.N, scmKindChecks, sha, f.suite, "failure")) {
					won = append(won, f)
				}
			}
			if len(won) == 0 {
				return nil
			}
			scmCIFailed(t, p, k, r.Repo, sha, won, body)
			return nil
		})
		return c.State, true
	case "success":
		_ = d.Tx(func(t *DB) error {
			p, k, ok := scmLiveTask(t, r.Project, r.N)
			pr := (*TaskPR)(nil)
			if ok {
				pr = scmOpenPR(k, r.Repo, 0)
			}
			if pr == nil || !strings.EqualFold(pr.HeadSHA, sha) || !scmSeenOnce(t, scmSemKey(p.ID, k.N, scmKindChecks, sha, "all", "success")) {
				return nil
			}
			scmSetChecks(t, p, k, r.Repo, sha, "success")
			addProjectEvent(t, p.ID, k.N, pevPRReady, map[string]any{
				"text": fmt.Sprintf("CI is green on PR #%d (%s@%s): it awaits review", pr.Number, k.Branch, sha7(sha)),
				"repo": r.Repo, "pr": pr.Number, "sha": sha, "url": pr.URL}, true, fmt.Sprintf("pr.ready:%d:%s:%d:%s", k.N, strings.ToLower(r.Repo), pr.Number, sha))
			return nil
		})
		return c.State, true
	case "pending":
		_ = d.Tx(func(t *DB) error {
			if p, k, ok := scmLiveTask(t, r.Project, r.N); ok {
				scmSetChecks(t, p, k, r.Repo, sha, "pending")
			}
			return nil
		})
	}
	return orStr(c.State, scmCINone), false
}

// scmCIFailed is a failing CI on task k's head (in t): the PR's checks,
// and the task told (autoFix, under the day's cap), or the events alone.
func scmCIFailed(t *DB, p *Project, k *ProjectTask, repo, sha string, fails []*scmFail, body string) {
	pol := policyOf(p.Policy).CI
	scmSetChecks(t, p, k, repo, sha, "failure")
	suite := fails[0].suite
	ns := strconv.FormatInt(k.N, 10)
	var names []string
	for _, f := range fails {
		for _, j := range f.jobs {
			names = append(names, j.Name)
		}
		for _, ch := range f.checks {
			names = append(names, ch.Name)
		}
		for _, st := range f.statuses {
			names = append(names, st.Context)
		}
	}
	failed := scmLine(strings.Join(names, ", "), 300)
	ev := map[string]any{"text": fmt.Sprintf("CI failed on %s@%s — failing (untrusted names): %s", k.Branch, sha7(sha), failed),
		"repo": repo, "sha": sha, "suite": suite}
	switch {
	case !pol.AutoFix:
		addProjectEvent(t, p.ID, k.N, pevCIFailed, ev, false, "ci.failed:"+ns+":"+sha+":"+suite)
	case scmFixesToday(k) >= pol.MaxPerDay:
		ev["text"] = fmt.Sprintf("CI failed again on %s@%s — the task had its %d CI fixes today; it needs a person", k.Branch, sha7(sha), pol.MaxPerDay)
		addProjectEvent(t, p.ID, k.N, pevCIStuck, ev, true, "ci.stuck:"+ns+":"+sha)
	default:
		if body == "" { // the cap moved while the logs were read: the names alone
			body = "failing: " + strings.Join(names, ", ")
		}
		fixes := scmCIFixes{Day: scmToday(), N: scmFixesToday(k) + 1}
		b, _ := json.Marshal(fixes)
		k.CIFixes = b
		_ = t.setTask(k.ID, map[string]any{"ci_fixes": string(b)})
		text := fmt.Sprintf("[scm: CI failed on %s@%s — untrusted output]\n%s\n— fix it and push.", k.Branch, sha7(sha),
			untrusted(p.Host, "CI output of "+k.Branch+"@"+sha7(sha), body))
		_, _ = queueTaskInput(t, taskInput{Project: p.ID, N: k.N, Kind: "input", Text: text, Source: srcEvent, HoldPark: true,
			Dedupe: "ci:" + ns + ":" + sha + ":" + suite})
		addProjectEvent(t, p.ID, k.N, pevCIFailed, ev, false, "ci.failed:"+ns+":"+sha+":"+suite)
	}
}

// scmCIBody is what failed, for the task: up to 3 failing jobs — each its
// failing step and its log's last 120 lines (ANSI stripped, redacted) —
// then failing checks no job reports (title and summary) and statuses;
// ≤ budget bytes. Untrusted: the caller frames it.
func scmCIBody(ctx context.Context, api scmAPI, p *Project, repo, as string, fails []*scmFail, budget int) string {
	budget = max(budget, 1024)
	var items int
	for _, f := range fails {
		items += len(f.jobs) + len(f.checks)
	}
	per := budget / max(1, min(items, 3))
	var b strings.Builder
	n := 0
	for _, f := range fails {
		for _, j := range f.jobs {
			if n == 3 {
				break
			}
			n++
			fmt.Fprintf(&b, "• job %q failed", j.Name)
			for _, s := range j.Steps {
				if scmFailed(s.Conclusion) {
					fmt.Fprintf(&b, " at step %d %q", s.N, s.Name)
					break
				}
			}
			if j.URL != "" {
				fmt.Fprintf(&b, " (%s)", j.URL)
			}
			b.WriteString("\n")
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			lg, err := api.JobLog(cctx, repo, j.ID, 32<<10, 0, 0, as)
			cancel()
			switch {
			case scmRefused(err, scmRefInProgress):
				b.WriteString("(its log can be read once the job has ended)\n")
			case err != nil || lg == nil:
				b.WriteString("(its log couldn't be read)\n")
			default:
				tail := lastLines(projRedact(ansiRE.ReplaceAllString(lg.Text, "")), 120)
				if len(tail) > per {
					tail = "…" + scmLastBytes(tail, per)
				}
				b.WriteString(tail)
				b.WriteString("\n")
			}
		}
		for _, ch := range f.checks {
			if n == 3 {
				break
			}
			n++
			fmt.Fprintf(&b, "• check %q (%s): %s\n%s\n", ch.Name, ch.Conclusion, ch.Title, clip(projRedact(ch.Summary), per))
		}
		for _, st := range f.statuses {
			fmt.Fprintf(&b, "• status %q: %s (%s)\n", st.Context, st.Description, st.State)
		}
	}
	return clip(b.String(), budget)
}

// scmLastBytes is s cut to its last n bytes (on a rune boundary).
func scmLastBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[len(s)-n:], "")
}
