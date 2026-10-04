// projects_coord_scm.go — the coordinator's read-only scm tools (API.md
// §The coordinator): scm_pr reads a pull request — its state and
// mergeability, its reviews and comments, its checks as one aggregate with
// the failing jobs, each its failing step and the end of its log — and
// scm_issues reads issues. Both read through the project's identity at its
// provider (projectAs: maybe the bot), so a repo must be one of the
// project's (a tool error otherwise, at every home): a prompt-injected
// coordinator never reads a repo nobody named for the project. Protocol 1
// has no merge route, and nothing here writes.
//
// What the provider says in words — titles, bodies, reviews, check output,
// logs — is clipped, redacted (projRedact: the token shapes and every live
// scm token) and framed as untrusted data (untrusted).
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// scm_pr's bounds: a failing job's log excerpt, all of them, the jobs read,
// a pull request's description, its reviews and comments.
const (
	coordLogEach    = 2 << 10
	coordLogAll     = 8 << 10
	coordLogJobs    = 4
	coordBodyMax    = 2 << 10
	coordReviewsMax = 4 << 10
)

// coordScmCall bounds one provider call of a tool.
const coordScmCall = 20 * time.Second

func (ag *Agent) coordPR(ctx context.Context, p *Project, args map[string]any) (string, error) {
	var a struct {
		Task   int64  `json:"task"`
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	var repo string
	var number int
	var others []string
	switch {
	case a.Task > 0 && (a.Repo != "" || a.Number > 0):
		return "", errors.New("name a task, or a repo and a number — not both")
	case a.Task > 0:
		k, _, err := ag.db.projectTaskOf(p, a.Task)
		if err != nil {
			return "", err
		}
		prs := k.taskPRs()
		if len(prs) == 0 {
			return fmt.Sprintf("task #%d has no pull request yet", k.N), nil
		}
		pick := prs[len(prs)-1]
		for _, pr := range prs {
			if pr.State == "open" {
				pick = pr
				break
			}
		}
		for _, pr := range prs {
			if pr.Repo != pick.Repo || pr.Number != pick.Number {
				others = append(others, fmt.Sprintf("%s #%d (%s)", pr.Repo, pr.Number, pr.State))
			}
		}
		repo, number = pick.Repo, pick.Number
	case a.Repo != "" && a.Number > 0:
		repo, number = a.Repo, a.Number
	default:
		return "", errors.New("name a task (its pull request), or a repo and a number")
	}
	if strings.TrimSpace(repo) == "" {
		return "", errors.New("repo: one of this project's repos (owner/name)")
	}
	r, err := ag.coordRepo(p, repo)
	if err != nil {
		return "", err
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return "", fmt.Errorf("the project's scm provider: %v", err)
	}
	as := projectAs(p)
	cctx, cancel := context.WithTimeout(ctx, coordScmCall)
	pr, err := api.Pull(cctx, r.Repo, number, as)
	cancel()
	if err != nil {
		return "", coordScmErr(err)
	}
	host := orStr(p.Host, "the scm provider")
	var b strings.Builder
	fmt.Fprintf(&b, "Pull request %s #%d — %s", r.Repo, pr.Number, pr.State)
	if pr.Draft {
		b.WriteString(", draft")
	}
	switch {
	case pr.Mergeable == nil:
		b.WriteString(", mergeability not known yet")
	case *pr.Mergeable:
		b.WriteString(", mergeable")
	default:
		b.WriteString(", not mergeable (conflicts or a blocked base)")
	}
	if pr.MergeableState != "" && pr.MergeableState != "unknown" {
		fmt.Fprintf(&b, " (%s)", coordPlain(pr.MergeableState))
	}
	fmt.Fprintf(&b, "\nhead %s @ %s → base %s · by %s · %s\n", coordPlain(pr.Head.Ref), shortSHA(pr.Head.SHA),
		coordPlain(pr.Base.Ref), coordPlain(pr.Author.Login), pr.URL)
	if len(others) > 0 {
		b.WriteString("The task's other pull requests: " + strings.Join(others, ", ") + " (scm_pr repo and number reads one)\n")
	}
	b.WriteString(untrusted(host, "the pull request's title and description",
		clip(pr.Title+"\n\n"+strings.TrimSpace(pr.Body), coordBodyMax)) + "\n")
	ag.coordPRReviews(ctx, &b, api, host, r.Repo, pr.Number, as)
	if pr.Head.SHA != "" {
		ag.coordPRChecks(ctx, &b, api, host, r.Repo, pr.Head.SHA, as)
	}
	b.WriteString("You can't merge, approve or comment: tell the person what you found.")
	return b.String(), nil
}

// coordPRReviews adds a pull request's reviews and comments: a count of each
// verdict, then the newest of them, framed.
func (ag *Agent) coordPRReviews(ctx context.Context, b *strings.Builder, api scmAPI, host, repo string, n int, as string) {
	cctx, cancel := context.WithTimeout(ctx, coordScmCall)
	page, err := api.Comments(cctx, repo, n, 0, as)
	cancel()
	if err != nil {
		fmt.Fprintf(b, "Reviews: can't be read now (%v)\n", coordPlain(coordScmErr(err).Error()))
		return
	}
	if page == nil || len(page.Items) == 0 {
		b.WriteString("Reviews: none yet\n")
		return
	}
	verdicts := map[string]int{}
	for _, c := range page.Items {
		if c.Kind == "review" && c.State != "" {
			verdicts[c.State]++
		}
	}
	var counts []string
	for _, s := range []string{"approved", "changes_requested", "commented", "dismissed"} {
		if verdicts[s] > 0 {
			counts = append(counts, fmt.Sprintf("%d %s", verdicts[s], strings.ReplaceAll(s, "_", " ")))
		}
	}
	fmt.Fprintf(b, "Reviews and comments: %d", len(page.Items))
	if len(counts) > 0 {
		b.WriteString(" (" + strings.Join(counts, ", ") + ")")
	}
	b.WriteString("\n")
	var lines []string
	size := 0
	for i := len(page.Items) - 1; i >= 0 && size < coordReviewsMax; i-- { // the newest first, within the budget
		c := page.Items[i]
		l := "- " + c.Kind + " by " + c.Author.Login
		if c.Author.Association != "" {
			l += " (" + c.Author.Association + ")"
		}
		if c.State != "" {
			l += " " + strings.ReplaceAll(c.State, "_", " ")
		}
		if c.Path != "" {
			l += fmt.Sprintf(" on %s:%d", c.Path, c.Line)
		}
		l += ": " + clip(strings.TrimSpace(c.Body), 600)
		size += len(l)
		lines = append(lines, l)
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	b.WriteString(untrusted(host, "reviews and comments on the pull request, oldest first", strings.Join(lines, "\n")) + "\n")
}

// coordPRChecks adds the head's CI: the aggregate, then each failing job with
// its failing step and the end of its log (≤ coordLogEach each, coordLogAll in
// all), framed.
func (ag *Agent) coordPRChecks(ctx context.Context, b *strings.Builder, api scmAPI, host, repo, sha, as string) {
	cctx, cancel := context.WithTimeout(ctx, coordScmCall)
	ch, err := api.Checks(cctx, repo, sha, "", as)
	cancel()
	if err != nil {
		fmt.Fprintf(b, "Checks: can't be read now (%v)\n", coordPlain(coordScmErr(err).Error()))
		return
	}
	if ch == nil {
		b.WriteString("Checks: none reported\n")
		return
	}
	s := coordCISummary(ch)
	fmt.Fprintf(b, "Checks on %s: %s — %d job(s): %d done, %d failed, %d running, %d queued", shortSHA(sha), s.State,
		s.Jobs.Total, s.Jobs.Done, s.Jobs.Failed, s.Jobs.Running, s.Jobs.Queued)
	if s.Current != "" {
		fmt.Fprintf(b, " (now: %s)", coordPlain(s.Current))
	}
	b.WriteString("\n")
	type failing struct {
		run, job, step, url, id string
	}
	var fails []failing
	for _, wr := range ch.WorkflowRuns {
		for _, j := range wr.Jobs {
			if !coordFailed(j.Conclusion) {
				continue
			}
			f := failing{run: wr.Name, job: j.Name, url: j.URL, id: j.ID}
			for _, st := range j.Steps {
				if coordFailed(st.Conclusion) {
					f.step = st.Name
					break
				}
			}
			fails = append(fails, f)
		}
	}
	var other []string
	for _, c := range ch.Checks {
		if c.Job == "" && coordFailed(c.Conclusion) {
			l := c.Name + ": " + c.Conclusion
			if t := strings.TrimSpace(c.Title + " " + c.Summary); t != "" {
				l += " — " + clip(t, 300)
			}
			other = append(other, l)
		}
	}
	for _, st := range ch.Statuses {
		if st.State == "failure" || st.State == "error" {
			other = append(other, st.Context+": "+st.State+orStr(" — "+clip(st.Description, 300), ""))
		}
	}
	if len(fails) == 0 && len(other) == 0 {
		return
	}
	var t strings.Builder
	all := 0
	for i, f := range fails {
		fmt.Fprintf(&t, "== %s › %s", f.run, f.job)
		if f.step != "" {
			fmt.Fprintf(&t, " — failed at step %q", f.step)
		}
		t.WriteString(" — " + f.url + "\n")
		if i >= coordLogJobs || all >= coordLogAll {
			continue
		}
		lctx, cancel := context.WithTimeout(ctx, coordScmCall)
		lg, err := api.JobLog(lctx, repo, f.id, coordLogEach, 0, 0, as)
		cancel()
		switch {
		case scmRefused(err, scmRefInProgress):
			t.WriteString("(its log comes when the job has ended)\n")
		case err != nil:
			t.WriteString("(its log can't be read now)\n")
		case lg != nil:
			x := strings.TrimSpace(cleanOutput([]byte(lg.Text)))
			if len(x) > coordLogEach {
				x = "…" + strings.ToValidUTF8(x[len(x)-coordLogEach:], "")
			}
			if all+len(x) > coordLogAll {
				x = "…" + strings.ToValidUTF8(x[len(x)-(coordLogAll-all):], "")
			}
			all += len(x)
			t.WriteString(x + "\n")
		}
	}
	for _, o := range other {
		t.WriteString("== " + o + "\n")
	}
	b.WriteString(untrusted(host, "the failing checks: their jobs, failing steps and the end of each log",
		strings.TrimRight(t.String(), "\n")) + "\n")
}

// coordFailed: a check or job conclusion that fails the commit.
func coordFailed(c string) bool {
	switch c {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	}
	return false
}

// coordCISummary is a commit's CI at a glance (the CISummary shape): every
// job of its workflow runs, and each check or status that reports none.
func coordCISummary(ch *scmChecks) CISummary {
	s := CISummary{State: orStr(ch.State, "none")}
	jobChecks := map[string]bool{}
	count := func(status, conclusion string) {
		s.Jobs.Total++
		switch {
		case status == "completed" || (status == "" && conclusion != ""):
			s.Jobs.Done++
			if coordFailed(conclusion) {
				s.Jobs.Failed++
			}
		case status == "in_progress":
			s.Jobs.Running++
		default:
			s.Jobs.Queued++
		}
	}
	for _, wr := range ch.WorkflowRuns {
		if s.StartedAt == 0 || (wr.StartedAt > 0 && wr.StartedAt < s.StartedAt) {
			s.StartedAt = wr.StartedAt
		}
		s.UpdatedAt = max(s.UpdatedAt, wr.UpdatedAt)
		if s.URL == "" {
			s.URL = wr.URL
		}
		for _, j := range wr.Jobs {
			if j.Check != "" {
				jobChecks[j.Check] = true
			}
			count(j.Status, j.Conclusion)
			if j.Status == "in_progress" && s.Current == "" {
				s.Current = j.Name
				for _, st := range j.Steps {
					if st.Status == "in_progress" {
						s.Current += " › " + st.Name
						break
					}
				}
			}
		}
	}
	for _, c := range ch.Checks {
		if c.Job != "" || jobChecks[c.ID] {
			continue
		}
		count(c.Status, c.Conclusion)
	}
	for _, st := range ch.Statuses {
		switch st.State {
		case "pending":
			count("queued", "")
		case "success":
			count("completed", "success")
		default:
			count("completed", "failure")
		}
	}
	return s
}

// coordScmErr is a provider's refusal in the model's words: a sign-in only
// the person can do, an app not installed, anything else.
func coordScmErr(err error) error {
	var se *scmError
	if !errors.As(err, &se) {
		return err
	}
	switch se.Refusal {
	case scmRefSignin:
		return errors.New("the project's scm provider needs its person to sign in first (the project's page offers it) — you can't do that for them")
	case scmRefNotFound:
		return errors.New("not found at the scm provider")
	case scmRefNotInstalled:
		return errors.New("the scm provider's app isn't installed on that account")
	}
	return fmt.Errorf("the scm provider: %s", clip(coordPlain(se.Error()), 300))
}

// --- scm_issues ---------------------------------------------------------------------------------

func (ag *Agent) coordIssues(ctx context.Context, p *Project, args map[string]any) (string, error) {
	var a struct {
		Repo    string   `json:"repo"`
		Numbers []int    `json:"numbers"`
		State   string   `json:"state"`
		Labels  []string `json:"labels"`
		Q       string   `json:"q"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Repo) == "" {
		return "", errors.New("repo: one of this project's repos (owner/name)")
	}
	r, err := ag.coordRepo(p, a.Repo)
	if err != nil {
		return "", err
	}
	if len(a.Numbers) > 10 {
		return "", errors.New("at most 10 issues at once")
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return "", fmt.Errorf("the project's scm provider: %v", err)
	}
	as, host := projectAs(p), orStr(p.Host, "the scm provider")
	if len(a.Numbers) > 0 {
		budget := inlineBudget(len(a.Numbers))
		parts := make([]string, 0, len(a.Numbers))
		for _, n := range a.Numbers {
			cctx, cancel := context.WithTimeout(ctx, coordScmCall)
			is, err := api.Issue(cctx, r.Repo, n, true, as)
			cancel()
			if err != nil {
				parts = append(parts, fmt.Sprintf("%s#%d: %v", r.Repo, n, coordScmErr(err)))
				continue
			}
			var t strings.Builder
			fmt.Fprintf(&t, "#%d [%s] %s", is.Number, is.State, is.Title)
			if len(is.Labels) > 0 {
				t.WriteString(" (" + strings.Join(is.Labels, ", ") + ")")
			}
			fmt.Fprintf(&t, " — by %s\n%s", is.Author.Login, strings.TrimSpace(is.Body))
			for _, c := range is.Comments {
				fmt.Fprintf(&t, "\n- %s", c.Author.Login)
				if c.Author.Association != "" {
					t.WriteString(" (" + c.Author.Association + ")")
				}
				t.WriteString(": " + clip(strings.TrimSpace(c.Body), 500))
			}
			parts = append(parts, fmt.Sprintf("%s#%d — %s\n%s", r.Repo, is.Number, is.URL,
				untrusted(host, "an issue and its comments", clip(t.String(), budget))))
		}
		return strings.Join(parts, "\n\n"), nil
	}
	state := orStr(a.State, "open")
	cctx, cancel := context.WithTimeout(ctx, coordScmCall)
	page, err := api.Issues(cctx, scmQuery{Repo: r.Repo, State: state, Labels: a.Labels, Q: strings.TrimSpace(a.Q), Limit: 20, As: as})
	cancel()
	if err != nil {
		return "", coordScmErr(err)
	}
	if page == nil || len(page.Items) == 0 {
		return fmt.Sprintf("no %s issues in %s match", state, r.Repo), nil
	}
	var t strings.Builder
	for _, is := range page.Items {
		fmt.Fprintf(&t, "#%d [%s] %s", is.Number, is.State, clip(is.Title, 200))
		if len(is.Labels) > 0 {
			t.WriteString(" (" + strings.Join(is.Labels, ", ") + ")")
		}
		t.WriteString("\n")
	}
	out := fmt.Sprintf("Issues of %s (%s; numbers read one in full):\n", r.Repo, state) +
		untrusted(host, "issue titles", strings.TrimRight(t.String(), "\n"))
	if page.Next != "" {
		out += "\n(more match: narrow with labels or words)"
	}
	return out, nil
}
