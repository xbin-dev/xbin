// normalize.go — GitHub's webhook deliveries as the scm contract's events
// (docs/scm.md §Delivery, event v1; API.md §7): which GitHub event becomes
// which kind and action, the ref an event concerns, its actor, its data, a
// topic and a summary in this provider's own words.
//
// A webhook body is attacker-controlled. Every field taken from it is
// checked (repos, logins, shas, branches, urls) or passed through as
// untrusted text — clipped, control characters dropped and token-shaped
// strings redacted — and nothing in it is interpreted.
package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Event kinds (docs/scm.md §Delivery). The last three are progress kinds: a
// subscription names them to get them.
const (
	kindPull     = "pull"
	kindChecks   = "checks"
	kindComment  = "comment"
	kindReview   = "review"
	kindPush     = "push"
	kindIssue    = "issue"
	kindWorkflow = "workflow"
	kindJob      = "job"
	kindCheck    = "check"
)

// kindActions is every kind and the actions it has.
var kindActions = map[string][]string{
	kindPull:     {"opened", "closed", "merged", "reopened", "synchronize", "ready", "draft", "edited"},
	kindChecks:   {"completed"},
	kindComment:  {"created", "edited"},
	kindReview:   {"submitted", "dismissed"},
	kindPush:     {"pushed"},
	kindIssue:    {"opened", "edited", "closed", "reopened", "labeled"},
	kindWorkflow: {"requested", "in_progress", "completed"},
	kindJob:      {"queued", "waiting", "in_progress", "completed"},
	kindCheck:    {"created", "in_progress", "completed", "rerequested"},
}

func progressKind(k string) bool { return k == kindWorkflow || k == kindJob || k == kindCheck }

// Text limits.
const (
	bodyMax  = 8 << 10 // comment and review bodies
	titleMax = 1 << 10 // titles, check names
)

// event is an event v1. for, forPid and subs are each consumer's own; the
// rest is the same for everyone it reaches.
type event struct {
	Protocol   int            `json:"protocol"`
	EventID    string         `json:"eventId"`
	For        string         `json:"for,omitempty"`
	ForPid     string         `json:"forPid,omitempty"`
	SCM        eventSource    `json:"scm"`
	Kind       string         `json:"kind"`
	Action     string         `json:"action"`
	Topic      string         `json:"topic"`
	Repo       string         `json:"repo"`
	Private    bool           `json:"private"`
	Ref        eventRef       `json:"ref"`
	Actor      eventActor     `json:"actor"`
	Conclusion string         `json:"conclusion,omitempty"`
	Summary    string         `json:"summary"`
	URL        string         `json:"url,omitempty"`
	At         int64          `json:"at"`
	Subs       []string       `json:"subs,omitempty"`
	Data       map[string]any `json:"data,omitempty"`

	// branches is every branch the event concerns (a commit status names
	// each branch whose head the commit is), for matching only.
	branches []string
	// unproven is a CI event's head branch while nothing in the body says
	// the run's head is this repo's and not a fork's (proveBranches settles
	// it: kept once proven, else dropped); runID is a workflow run's or a
	// job's run, runHead a workflow run's head repo (a job's run is looked
	// up by it).
	unproven string
	runID    int64
	runHead  string
}

type eventSource struct {
	Provider string `json:"provider"`
	Host     string `json:"host"`
}

type eventRef struct {
	Branch string `json:"branch,omitempty"`
	SHA    string `json:"sha,omitempty"`
	PR     int    `json:"pr,omitempty"`
	Issue  int    `json:"issue,omitempty"`
}

type eventActor struct {
	Login       string `json:"login"`
	Association string `json:"association"`
	Bot         bool   `json:"bot"`
	Self        bool   `json:"self"`
}

// The data of each kind.
type pullData struct {
	Number int     `json:"number"`
	Title  string  `json:"title"`
	State  string  `json:"state"`
	Draft  bool    `json:"draft"`
	Head   refSide `json:"head"` // repo: the head's own repo (a fork's differs)
	Base   struct {
		Ref string `json:"ref"`
	} `json:"base"`
	URL string `json:"url"`
}

type checksData struct {
	Suite   string      `json:"suite"`
	HeadSHA string      `json:"headSha"`
	Runs    []checksRun `json:"runs"`
}

type checksRun struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type commentData struct {
	ID   string `json:"id"`
	Body string `json:"body"`
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	URL  string `json:"url"`
}

type reviewData struct {
	ID    string `json:"id"`
	State string `json:"state"`
	Body  string `json:"body"`
	URL   string `json:"url"`
}

type pushData struct {
	Before  string `json:"before"`
	After   string `json:"after"`
	Commits int    `json:"commits"`
	Forced  bool   `json:"forced"`
}

type issueData struct {
	Number int      `json:"number"`
	Title  string   `json:"title"`
	State  string   `json:"state"`
	Labels []string `json:"labels"`
	URL    string   `json:"url"`
}

// workflowData is a workflowRuns entry of GET /scm/checks without its jobs.
type workflowData struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
	StartedAt  int64  `json:"startedAt"`
	UpdatedAt  int64  `json:"updatedAt"`
	Attempt    int    `json:"attempt"`
	HeadSHA    string `json:"headSha"`
	HeadBranch string `json:"headBranch"`
}

type jobData struct {
	RunID string `json:"runId"`
	Job   job    `json:"job"`
}

// normCtx is what normalising needs beyond the body.
type normCtx struct {
	provider string // this tile's path
	host     string
	web      string // GitHub's web address (https://github.com)
	botLogin string // <slug>[bot]: actor.self
	delivery string // X-GitHub-Delivery
	now      int64
}

// normalize turns one delivery (X-GitHub-Event ghEvent) into the events it
// yields — none for an action the contract has no event for, or a body that
// doesn't say which repo. Several get eventId suffixes ":<i>".
func normalize(ghEvent string, h *ghHook, nc normCtx) []*event {
	if h.Repository == nil || !validRepo(h.Repository.FullName) {
		return nil
	}
	var evs []*event
	b := func(kind, action string) *event {
		e := &event{Protocol: protocolVersion, SCM: eventSource{Provider: nc.provider, Host: nc.host}, Kind: kind,
			Action: action, Repo: h.Repository.FullName, Private: h.Repository.Private, At: nc.now, Data: map[string]any{}}
		e.Actor = nc.actorOf(h.Sender, "")
		evs = append(evs, e)
		return e
	}
	switch ghEvent {
	case "pull_request":
		nc.pull(h, b)
	case "issue_comment":
		if h.Issue == nil || h.Comment == nil || (h.Action != "created" && h.Action != "edited") {
			break
		}
		e := b(kindComment, h.Action)
		if len(h.Issue.PullRequest) > 0 && string(h.Issue.PullRequest) != "null" {
			e.Ref.PR = h.Issue.Number
		} else {
			e.Ref.Issue = h.Issue.Number
		}
		c := h.Comment
		e.Actor = nc.actorOf(h.Sender, c.Association)
		e.URL, e.At = cleanURL(c.HTMLURL), timeOr(c.UpdatedAt, nc.now)
		e.Data[kindComment] = commentData{ID: idStr(c.ID), Body: cleanText(c.Body, bodyMax), URL: e.URL}
	case "pull_request_review_comment":
		if h.PullRequest == nil || h.Comment == nil || (h.Action != "created" && h.Action != "edited") {
			break
		}
		e := b(kindComment, h.Action)
		e.Ref = nc.pullRef(h)
		if s := cleanSHA(h.Comment.CommitID); s != "" {
			e.Ref.SHA = s
		}
		c := h.Comment
		e.Actor = nc.actorOf(h.Sender, c.Association)
		e.URL, e.At = cleanURL(c.HTMLURL), timeOr(c.UpdatedAt, nc.now)
		e.Data[kindComment] = commentData{ID: idStr(c.ID), Body: cleanText(c.Body, bodyMax), Path: cleanName(c.Path, 1024),
			Line: max(c.Line, 0), URL: e.URL}
	case "pull_request_review":
		if h.PullRequest == nil || h.Review == nil || (h.Action != "submitted" && h.Action != "dismissed") {
			break
		}
		e := b(kindReview, h.Action)
		e.Ref = nc.pullRef(h)
		if s := cleanSHA(h.Review.CommitID); s != "" {
			e.Ref.SHA = s
		}
		rv := h.Review
		e.Actor = nc.actorOf(h.Sender, rv.Association)
		e.URL, e.At = cleanURL(rv.HTMLURL), timeOr(rv.SubmittedAt, nc.now)
		e.Data[kindReview] = reviewData{ID: idStr(rv.ID), State: cleanWord(strings.ToLower(rv.State)), Body: cleanText(rv.Body, bodyMax), URL: e.URL}
	case "push":
		br, ok := strings.CutPrefix(h.Ref, "refs/heads/")
		if !ok || cleanBranch(br) == "" { // tags aren't an event of the contract
			break
		}
		e := b(kindPush, "pushed")
		e.Ref = eventRef{Branch: cleanBranch(br), SHA: cleanSHA(h.After)}
		e.URL = cleanURL(h.Compare)
		e.Data[kindPush] = pushData{Before: cleanSHA(h.Before), After: cleanSHA(h.After), Commits: len(h.Commits), Forced: h.Forced}
	case "issues":
		if h.Issue == nil || !hasAction(kindIssue, h.Action) {
			break
		}
		e := b(kindIssue, h.Action)
		is := h.Issue
		e.Ref.Issue = is.Number
		e.Actor = nc.actorOf(h.Sender, "")
		e.URL, e.At = cleanURL(is.HTMLURL), timeOr(is.UpdatedAt, nc.now)
		labels := []string{}
		for i, l := range is.Labels {
			if i < 50 {
				labels = append(labels, cleanName(l.Name, 100))
			}
		}
		e.Data[kindIssue] = issueData{Number: is.Number, Title: cleanName(is.Title, titleMax), State: cleanWord(is.State), Labels: labels, URL: e.URL}
	case "check_suite":
		cs := h.CheckSuite
		if cs == nil || h.Action != "completed" {
			break
		}
		e := b(kindChecks, "completed")
		e.Ref = eventRef{SHA: cleanSHA(cs.HeadSHA), PR: firstPR(cs.PullRequests, h.Repository.ID)}
		ciBranch(e, cs.HeadBranch, cs.PullRequests, h.Repository.ID)
		e.Conclusion, e.At = cleanWord(cs.Conclusion), timeOr(cs.UpdatedAt, nc.now)
		e.URL = nc.checksURL(e)
		e.Data[kindChecks] = checksData{Suite: idStr(cs.ID), HeadSHA: e.Ref.SHA, Runs: []checksRun{}}
	case "status":
		nc.status(h, b)
	case "check_run":
		cr := h.CheckRun
		if cr == nil || !hasAction(kindCheck, h.Action) {
			break
		}
		c := checkOf(&cr.ghCheckRun)
		action := h.Action
		if action == "created" && c.Status == "in_progress" {
			action = "in_progress"
		}
		e := b(kindCheck, action)
		e.Ref = eventRef{SHA: cleanSHA(cr.HeadSHA), PR: firstPR(cr.PullRequests, h.Repository.ID)}
		if cr.CheckSuite != nil {
			ciBranch(e, cr.CheckSuite.HeadBranch, cr.PullRequests, h.Repository.ID)
			c.Suite = idStr(cr.CheckSuite.ID)
		}
		e.Conclusion, e.URL = c.Conclusion, c.URL
		e.At = max(c.CompletedAt, c.StartedAt, 0)
		if e.At == 0 {
			e.At = nc.now
		}
		e.Data[kindCheck] = c
	case "workflow_run":
		wr := h.WorkflowRun
		if wr == nil || !hasAction(kindWorkflow, h.Action) {
			break
		}
		e := b(kindWorkflow, h.Action)
		w := workflowOf(&wr.ghRun)
		e.Ref = eventRef{SHA: w.HeadSHA, PR: firstPR(wr.PullRequests, h.Repository.ID)}
		e.runID = wr.ID
		if wr.HeadRepository != nil && validRepo(wr.HeadRepository.FullName) {
			e.runHead = wr.HeadRepository.FullName
		}
		if strings.EqualFold(e.runHead, e.Repo) {
			e.Ref.Branch = w.HeadBranch // a fork's branch isn't this repo's (nor one GitHub didn't say whose it is)
		}
		e.Conclusion, e.URL, e.At = w.Conclusion, w.URL, timeOr(wr.UpdatedAt, nc.now)
		e.Data[kindWorkflow] = w
	case "workflow_job":
		wj := h.WorkflowJob
		if wj == nil || !hasAction(kindJob, h.Action) {
			break
		}
		e := b(kindJob, h.Action)
		j := jobOf(&wj.ghJob)
		e.Ref = eventRef{SHA: cleanSHA(wj.HeadSHA)}
		e.unproven, e.runID = cleanBranch(wj.HeadBranch), wj.RunID // a job doesn't say whose head its run's is
		e.Conclusion, e.URL = j.Conclusion, j.URL
		e.At = max(j.CompletedAt, j.StartedAt, 0)
		if e.At == 0 {
			e.At = nc.now
		}
		e.Data[kindJob] = jobData{RunID: idStr(wj.RunID), Job: j}
	}
	for i, e := range evs {
		e.EventID = "scm:" + nc.host + ":" + nc.delivery
		if len(evs) > 1 {
			e.EventID += ":" + strconv.Itoa(i)
		}
		if e.Ref.Branch != "" && !containsFold(e.branches, e.Ref.Branch) {
			e.branches = append([]string{e.Ref.Branch}, e.branches...)
		}
		e.Topic = topicOf(nc.host, e)
		e.Summary = summaryOf(e)
	}
	return evs
}

// pull is a pull_request delivery.
func (nc normCtx) pull(h *ghHook, b func(kind, action string) *event) {
	p := h.PullRequest
	if p == nil {
		return
	}
	action := h.Action
	switch action {
	case "closed":
		if p.Merged {
			action = "merged"
		}
	case "ready_for_review":
		action = "ready"
	case "converted_to_draft":
		action = "draft"
	}
	if !hasAction(kindPull, action) {
		return
	}
	e := b(kindPull, action)
	e.Ref = nc.pullRef(h)
	e.Actor = nc.actorOf(h.Sender, "")
	e.URL, e.At = cleanURL(p.HTMLURL), timeOr(p.UpdatedAt, nc.now)
	state := cleanWord(p.State)
	if p.Merged {
		state = "merged"
	}
	d := pullData{Number: p.Number, Title: cleanName(p.Title, titleMax), State: state, Draft: p.Draft, URL: e.URL,
		Head: refSide{Ref: cleanBranch(p.Head.Ref), SHA: cleanSHA(p.Head.SHA)}}
	if p.Head.Repo != nil && validRepo(p.Head.Repo.FullName) {
		d.Head.Repo = p.Head.Repo.FullName
	}
	d.Base.Ref = cleanBranch(p.Base.Ref)
	e.Data[kindPull] = d
}

// pullRef is the ref of an event about a pull request: its number, head
// sha and — when the head is in this repo, not a fork — its branch.
func (nc normCtx) pullRef(h *ghHook) eventRef {
	p := h.PullRequest
	r := eventRef{PR: max(p.Number, 0), SHA: cleanSHA(p.Head.SHA)}
	if p.Head.Repo == nil || strings.EqualFold(p.Head.Repo.FullName, h.Repository.FullName) {
		r.Branch = cleanBranch(p.Head.Ref)
	}
	return r
}

// status is a commit status: a final one (success, failure, error) is
// checks.completed; pending ones aren't an event.
func (nc normCtx) status(h *ghHook, b func(kind, action string) *event) {
	concl := map[string]string{"success": "success", "failure": "failure", "error": "failure"}[h.State]
	sha := cleanSHA(h.SHA)
	if concl == "" || sha == "" {
		return
	}
	e := b(kindChecks, "completed")
	e.Ref.SHA, e.Conclusion, e.At = sha, concl, timeOr(h.UpdatedAt, nc.now)
	for _, br := range h.Branches {
		if n := cleanBranch(br.Name); n != "" && strings.EqualFold(br.Commit.SHA, sha) && len(e.branches) < 20 {
			e.branches = append(e.branches, n)
		}
	}
	if len(e.branches) > 0 {
		e.Ref.Branch = e.branches[0]
	}
	e.URL = nc.checksURL(e)
	e.Data[kindChecks] = checksData{HeadSHA: sha, Runs: []checksRun{{ID: "status:" + cleanName(h.Context, 200),
		Name: cleanName(h.Context, 200), Conclusion: concl, URL: cleanURL(h.TargetURL)}}}
}

// checksURL is where a person sees a commit's checks.
func (nc normCtx) checksURL(e *event) string {
	if e.Ref.PR > 0 {
		return nc.web + "/" + e.Repo + "/pull/" + strconv.Itoa(e.Ref.PR) + "/checks"
	}
	if e.Ref.SHA != "" {
		return nc.web + "/" + e.Repo + "/commit/" + e.Ref.SHA + "/checks"
	}
	return ""
}

// actorOf is who did it: a GitHub login (checked), its association with
// the repo (one of the contract's five), whether it's a bot, and whether
// it's this App's own bot.
func (nc normCtx) actorOf(u *ghUser, assoc string) eventActor {
	a := eventActor{Association: association(assoc)}
	if u == nil {
		return a
	}
	a.Login = cleanLogin(u.Login)
	a.Bot = u.Type == "Bot" || strings.HasSuffix(a.Login, "[bot]")
	a.Self = a.Login != "" && nc.botLogin != "" && strings.EqualFold(a.Login, nc.botLogin)
	return a
}

// checkOf, workflowOf and jobOf are GET /scm/checks's entries from GitHub's
// objects (as checks.go builds them), every text clipped.
func checkOf(g *ghCheckRun) checkRun {
	c := checkRun{ID: idStr(g.ID), Name: cleanName(g.Name, titleMax), Status: runStatus(g.Status), Conclusion: cleanWord(g.Conclusion),
		URL: cleanURL(g.HTMLURL), DetailsURL: cleanURL(g.DetailsURL), Title: cleanText(g.Output.Title, checkTextMax),
		Summary: cleanText(g.Output.Summary, checkTextMax), Annotations: max(g.Output.AnnotationsCount, 0),
		StartedAt: ghTime(g.StartedAt), CompletedAt: ghTime(g.CompletedAt)}
	if c.Status == "waiting" {
		c.Status = "queued"
	}
	if g.App != nil {
		c.App = cleanName(g.App.Slug, 100)
	}
	if g.CheckSuite != nil {
		c.Suite = idStr(g.CheckSuite.ID)
	}
	if c.App == "github-actions" {
		c.Job = c.ID // an Actions job's check run has the job's id
	}
	return c
}

func workflowOf(g *ghRun) workflowData {
	return workflowData{ID: idStr(g.ID), Name: cleanName(g.Name, titleMax), Event: cleanWord(g.Event), Status: runStatus(g.Status),
		Conclusion: cleanWord(g.Conclusion), URL: cleanURL(g.HTMLURL), StartedAt: ghTime(g.StartedAt), UpdatedAt: ghTime(g.UpdatedAt),
		Attempt: max(g.Attempt, 1), HeadSHA: cleanSHA(g.HeadSHA), HeadBranch: cleanBranch(g.HeadBranch)}
}

func jobOf(g *ghJob) job {
	j := job{ID: idStr(g.ID), Name: cleanName(g.Name, titleMax), Status: runStatus(g.Status), Conclusion: cleanWord(g.Conclusion),
		URL: cleanURL(g.HTMLURL), StartedAt: ghTime(g.StartedAt), CompletedAt: ghTime(g.CompletedAt), Steps: []step{}}
	if j.Status != "queued" {
		j.Runner = cleanName(g.RunnerName, 200)
		if j.Runner == "" && len(g.Labels) > 0 {
			j.Runner = cleanName(g.Labels[0], 200)
		}
	}
	if k := strings.LastIndexByte(g.CheckRunURL, '/'); k >= 0 && k < len(g.CheckRunURL)-1 {
		if _, err := strconv.ParseInt(g.CheckRunURL[k+1:], 10, 64); err == nil {
			j.Check = g.CheckRunURL[k+1:]
		}
	}
	for i, st := range g.Steps {
		if i >= 200 {
			break
		}
		j.Steps = append(j.Steps, step{N: st.Number, Name: cleanName(st.Name, titleMax), Status: runStatus(st.Status),
			Conclusion: cleanWord(st.Conclusion), StartedAt: ghTime(st.StartedAt), CompletedAt: ghTime(st.CompletedAt)})
	}
	return j
}

// topicOf is the event's agent-inbox topic:
// scm/<host>/<owner>/<repo>/(pull/<n>|issue/<n>|branch/<ref>|repo)/<kind>.<action>.
func topicOf(host string, e *event) string {
	where := "repo"
	switch {
	case e.Ref.PR > 0:
		where = "pull/" + strconv.Itoa(e.Ref.PR)
	case e.Ref.Issue > 0:
		where = "issue/" + strconv.Itoa(e.Ref.Issue)
	case e.Ref.Branch != "":
		where = "branch/" + e.Ref.Branch
	}
	return "scm/" + host + "/" + e.Repo + "/" + where + "/" + e.Kind + "." + e.Action
}

// summaryOf is a sentence in this provider's own words: it names checked
// fields only (numbers, logins, branches, shas), never a title or a body.
func summaryOf(e *event) string {
	who := e.Actor.Login
	if who == "" {
		who = "someone"
	}
	on := ""
	if e.Ref.Branch != "" {
		on = " on " + e.Ref.Branch
	}
	what := "pull request #" + strconv.Itoa(e.Ref.PR)
	if e.Ref.PR == 0 {
		what = "issue #" + strconv.Itoa(e.Ref.Issue)
	}
	switch e.Kind {
	case kindPull:
		return fmt.Sprintf("pull request #%d %s by %s%s", e.Ref.PR, e.Action, who, on)
	case kindChecks:
		return fmt.Sprintf("checks %s%s (%s)", or(e.Conclusion, "done"), on, short(e.Ref.SHA))
	case kindComment:
		return fmt.Sprintf("comment %s on %s by %s", e.Action, what, who)
	case kindReview:
		st := ""
		if d, ok := e.Data[kindReview].(reviewData); ok && d.State != "" {
			st = " (" + d.State + ")"
		}
		return fmt.Sprintf("review %s%s on pull request #%d by %s", e.Action, st, e.Ref.PR, who)
	case kindPush:
		if d, ok := e.Data[kindPush].(pushData); ok && d.Forced {
			return fmt.Sprintf("force-push to %s by %s (%s)", e.Ref.Branch, who, short(e.Ref.SHA))
		} else if ok {
			return fmt.Sprintf("%d commit(s) pushed to %s by %s (%s)", d.Commits, e.Ref.Branch, who, short(e.Ref.SHA))
		}
	case kindIssue:
		return fmt.Sprintf("issue #%d %s by %s", e.Ref.Issue, e.Action, who)
	case kindWorkflow:
		return fmt.Sprintf("workflow run %s %s%s", e.Action, or(e.Conclusion, ""), on)
	case kindJob:
		return fmt.Sprintf("job %s %s%s", e.Action, or(e.Conclusion, ""), on)
	case kindCheck:
		return fmt.Sprintf("check run %s %s%s", e.Action, or(e.Conclusion, ""), on)
	}
	return e.Kind + " " + e.Action
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func short(sha string) string { return clip(sha, 7) }

func hasAction(kind, action string) bool {
	for _, a := range kindActions[kind] {
		if a == action {
			return true
		}
	}
	return false
}

// firstPR is the first pull request a CI event lists into this repo
// (GitHub may list another repo's that shares the head).
func firstPR(p []ghPullRef, repoID int64) int {
	for _, x := range p {
		if x.Number > 0 && x.Base.in(repoID) {
			return x.Number
		}
	}
	return 0
}

// ciBranch is a check suite's or a check run's head branch: this repo's
// when a pull request it lists has its head here on that branch, else
// unproven — a fork's run names the fork's branch, and GitHub lists no
// pull request for it — until proveBranches looks.
func ciBranch(e *event, head string, prs []ghPullRef, repoID int64) {
	br := cleanBranch(head)
	if br == "" {
		return
	}
	for _, x := range prs {
		if x.Head.in(repoID) && x.Head.Ref == br {
			e.Ref.Branch = br
			return
		}
	}
	e.unproven = br
}

func timeOr(s string, def int64) int64 {
	if t := ghTime(s); t > 0 {
		return t
	}
	return def
}

func containsFold(l []string, s string) bool {
	for _, x := range l {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}
