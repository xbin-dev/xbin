// checks.go — CI on a commit (docs/scm.md §Checks): GitHub's check runs,
// commit statuses and Actions workflow runs with their jobs and steps,
// combined; a completed job's log from a byte offset; a check run's
// annotations; a person's rerun. Every text here a build printed is
// untrusted and passes through clipped.
package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	checkTextMax = 4 << 10 // a check's title and summary, an annotation's message
	logKeepMax   = 8 << 20 // a job log's tail kept after download
	logTailDef   = 65536
	logTailMax   = 1 << 20
)

type ghCheckRun struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	App  *struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	HTMLURL     string `json:"html_url"`
	DetailsURL  string `json:"details_url"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
	Output      struct {
		Title            string `json:"title"`
		Summary          string `json:"summary"`
		AnnotationsCount int    `json:"annotations_count"`
	} `json:"output"`
	CheckSuite *struct {
		ID int64 `json:"id"`
	} `json:"check_suite"`
}

type ghRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	HTMLURL    string `json:"html_url"`
	StartedAt  string `json:"run_started_at"`
	UpdatedAt  string `json:"updated_at"`
	Attempt    int    `json:"run_attempt"`
	HeadSHA    string `json:"head_sha"`
	HeadBranch string `json:"head_branch"`
}

type ghJob struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Status      string   `json:"status"`
	Conclusion  string   `json:"conclusion"`
	HTMLURL     string   `json:"html_url"`
	StartedAt   string   `json:"started_at"`
	CompletedAt string   `json:"completed_at"`
	RunnerName  string   `json:"runner_name"`
	Labels      []string `json:"labels"`
	CheckRunURL string   `json:"check_run_url"`
	Steps       []struct {
		Name        string `json:"name"`
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		Number      int    `json:"number"`
		StartedAt   string `json:"started_at"`
		CompletedAt string `json:"completed_at"`
	} `json:"steps"`
}

// runStatus maps GitHub's run and job statuses onto the contract's four.
func runStatus(s string) string {
	switch s {
	case "completed", "in_progress", "waiting":
		return s
	}
	return "queued" // queued, requested, pending
}

func idStr(n int64) string { return strconv.FormatInt(n, 10) }

func (s *srv) handleChecks(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	v, err := s.getChecks(r.Context(), c, q.Get("repo"), q.Get("ref"), q.Get("as"))
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, v)
}

var hexSHA = func(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *srv) getChecks(ctx context.Context, c who, repo, ref, as string) (*checksResp, error) {
	if ref == "" || len(ref) > 255 {
		return nil, refuse(refInvalid, "ref is a branch, pull/<n> or a commit sha")
	}
	var out *checksResp
	err := s.withAuth(ctx, c, as, repo, false, func(a ghAuth) error {
		sha, err := s.resolveSHA(ctx, a, repo, ref)
		if err != nil {
			return err
		}
		key := a.key + " " + strings.ToLower(repo) + " " + sha
		if v, ok := s.checksC.get(key, s.now(), 5*time.Second); ok {
			cp := *v.(*checksResp)
			cp.Ref = ref
			out = &cp
			return nil
		}
		if out, err = s.combined(ctx, a, repo, sha); err != nil {
			return err
		}
		s.checksC.put(key, out, s.now())
		cp := *out
		cp.Ref = ref
		out = &cp
		return nil
	})
	return out, err
}

// resolveSHA turns a branch or pull/<n> into the commit it points at.
func (s *srv) resolveSHA(ctx context.Context, a ghAuth, repo, ref string) (string, error) {
	if hexSHA(ref) {
		return ref, nil
	}
	if n, ok := strings.CutPrefix(ref, "pull/"); ok {
		if _, err := strconv.Atoi(n); err != nil {
			return "", refuse(refInvalid, "a pull ref is pull/<number>")
		}
		var g ghPull
		if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/pulls/"+n, nil, &g); err != nil {
			return "", err
		}
		return g.Head.SHA, nil
	}
	var cm struct {
		SHA string `json:"sha"`
	}
	if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/commits/"+pathEsc(ref), nil, &cm); err != nil {
		return "", err
	}
	if cm.SHA == "" {
		return "", refuse(refNotFound, "no commit at %s", clip(ref, 100))
	}
	return cm.SHA, nil
}

// combined reads everything CI reported on sha: at most 4 upstream calls
// at once, every one conditional.
func (s *srv) combined(ctx context.Context, a ghAuth, repo, sha string) (*checksResp, error) {
	base := s.repoBase(repo)
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	get := func(u string, out any, optional bool) {
		sem <- struct{}{}
		defer func() { <-sem }()
		_, err := s.gh.call(ctx, a, http.MethodGet, u, nil, out)
		if err != nil && !(optional && (isRefusal(err, refNotFound) || isRefusal(err, refNotAllowed))) {
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
		}
	}
	var checks struct {
		CheckRuns []ghCheckRun `json:"check_runs"`
	}
	var status struct {
		Statuses []struct {
			Context     string `json:"context"`
			State       string `json:"state"`
			TargetURL   string `json:"target_url"`
			Description string `json:"description"`
			UpdatedAt   string `json:"updated_at"`
		} `json:"statuses"`
	}
	var runs struct {
		WorkflowRuns []ghRun `json:"workflow_runs"`
	}
	for _, c := range []struct {
		u        string
		out      any
		optional bool
	}{
		{base + "/commits/" + sha + "/check-runs?per_page=100", &checks, false},
		{base + "/commits/" + sha + "/status?per_page=100", &status, false},
		{base + "/actions/runs?head_sha=" + sha + "&per_page=20", &runs, true},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			get(c.u, c.out, c.optional)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	if len(runs.WorkflowRuns) > 20 {
		runs.WorkflowRuns = runs.WorkflowRuns[:20]
	}
	jobs := make([][]ghJob, len(runs.WorkflowRuns))
	for i, run := range runs.WorkflowRuns {
		wg.Add(1)
		go func(i int, id int64) {
			defer wg.Done()
			var js struct {
				Jobs []ghJob `json:"jobs"`
			}
			get(base+"/actions/runs/"+idStr(id)+"/jobs?filter=latest&per_page=100", &js, true)
			jobs[i] = js.Jobs
		}(i, run.ID)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	out := &checksResp{SHA: sha, WorkflowRuns: []workflowRun{}, Checks: []checkRun{}, Statuses: []commitStatus{}}
	jobOfCheck := map[string]string{}
	anyPending := false
	for i, g := range runs.WorkflowRuns {
		wr := workflowRun{ID: idStr(g.ID), Name: g.Name, Event: g.Event, Status: runStatus(g.Status), Conclusion: g.Conclusion,
			URL: g.HTMLURL, StartedAt: ghTime(g.StartedAt), UpdatedAt: ghTime(g.UpdatedAt), Attempt: max(g.Attempt, 1),
			HeadSHA: g.HeadSHA, HeadBranch: g.HeadBranch, Jobs: []job{}}
		for n, gj := range jobs[i] {
			if n >= 100 {
				break
			}
			j := job{ID: idStr(gj.ID), Name: gj.Name, Status: runStatus(gj.Status), Conclusion: gj.Conclusion, URL: gj.HTMLURL,
				StartedAt: ghTime(gj.StartedAt), CompletedAt: ghTime(gj.CompletedAt), Steps: []step{}}
			if j.Status != "queued" {
				j.Runner = gj.RunnerName
				if j.Runner == "" && len(gj.Labels) > 0 {
					j.Runner = gj.Labels[0]
				}
			}
			if k := strings.LastIndexByte(gj.CheckRunURL, '/'); k >= 0 && k < len(gj.CheckRunURL)-1 {
				j.Check = gj.CheckRunURL[k+1:]
				jobOfCheck[j.Check] = j.ID
			}
			for _, st := range gj.Steps {
				j.Steps = append(j.Steps, step{N: st.Number, Name: st.Name, Status: runStatus(st.Status), Conclusion: st.Conclusion,
					StartedAt: ghTime(st.StartedAt), CompletedAt: ghTime(st.CompletedAt)})
			}
			if j.Status != "completed" {
				anyPending = true
			}
			wr.Jobs = append(wr.Jobs, j)
		}
		out.WorkflowRuns = append(out.WorkflowRuns, wr)
	}
	for n, g := range checks.CheckRuns {
		if n >= 100 {
			break
		}
		cr := checkRun{ID: idStr(g.ID), Name: g.Name, Status: runStatus(g.Status), Conclusion: g.Conclusion, URL: g.HTMLURL,
			DetailsURL: g.DetailsURL, Title: clip(g.Output.Title, checkTextMax), Summary: clip(g.Output.Summary, checkTextMax),
			Annotations: g.Output.AnnotationsCount, StartedAt: ghTime(g.StartedAt), CompletedAt: ghTime(g.CompletedAt), Job: jobOfCheck[idStr(g.ID)]}
		if cr.Status == "waiting" {
			cr.Status = "queued"
		}
		if g.App != nil {
			cr.App = g.App.Slug
		}
		if g.CheckSuite != nil {
			cr.Suite = idStr(g.CheckSuite.ID)
		}
		out.Checks = append(out.Checks, cr)
		bucket(&out.Counts, cr.Status, cr.Conclusion)
	}
	for _, st := range status.Statuses {
		out.Statuses = append(out.Statuses, commitStatus{Context: st.Context, State: st.State, URL: st.TargetURL,
			Description: clip(st.Description, 1024), UpdatedAt: ghTime(st.UpdatedAt)})
		switch st.State {
		case "success":
			bucket(&out.Counts, "completed", "success")
		case "failure", "error":
			bucket(&out.Counts, "completed", "failure")
		default:
			bucket(&out.Counts, "pending", "")
		}
	}
	switch {
	case out.Counts.Total == 0 && !anyPending:
		out.State = "none"
	case out.Counts.Pending > 0 || anyPending:
		out.State = "pending"
	case out.Counts.Failure > 0:
		out.State = "failure"
	default:
		out.State = "success"
	}
	return out, nil
}

// bucket counts one check or status in exactly one bucket.
func bucket(c *counts, status, conclusion string) {
	c.Total++
	if status != "completed" {
		c.Pending++
		return
	}
	switch conclusion {
	case "success":
		c.Success++
	case "failure", "timed_out", "action_required", "startup_failure":
		c.Failure++
	case "neutral":
		c.Neutral++
	case "skipped":
		c.Skipped++
	case "cancelled", "stale":
		c.Cancelled++
	default:
		c.Neutral++
	}
}

// logCache keeps a few completed jobs' logs (they never change): a viewer
// paging back doesn't download again.
type logEntry struct {
	data  []byte // the tail kept (≤ 8 MiB)
	total int64
	url   string
}

func (s *srv) handleJobLog(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		fail(w, refuse(refInvalid, "a job's id"))
		return
	}
	tail, since, until := int64(logTailDef), int64(0), int64(0)
	var perr error
	num := func(k string, dst *int64) {
		if v := q.Get(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				perr = refuse(refInvalid, "%s is a byte count", k)
			}
			*dst = n
		}
	}
	num("tailBytes", &tail)
	num("since", &since)
	num("until", &until)
	if perr != nil {
		fail(w, perr)
		return
	}
	tail = min(max(tail, 1), logTailMax)
	repo := q.Get("repo")
	ctx := r.Context()
	var out *jobLog
	err := s.withAuth(ctx, c, q.Get("as"), repo, false, func(a ghAuth) error {
		key := a.key + " " + strings.ToLower(repo) + " " + id
		var e *logEntry
		if v, ok := s.logsC.get(key, s.now(), 10*time.Minute); ok {
			e = v.(*logEntry)
		} else {
			var j ghJob
			if _, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/actions/jobs/"+id, nil, &j); err != nil {
				return err
			}
			if j.Status != "completed" {
				ie := refuse(refInProgress, "GitHub serves a job's log once the job has finished: follow it live on GitHub")
				ie.URL = j.HTMLURL
				return ie
			}
			data, total, err := s.downloadLog(ctx, a, s.repoBase(repo)+"/actions/jobs/"+id+"/logs")
			if err != nil {
				return err
			}
			e = &logEntry{data: data, total: total, url: j.HTMLURL}
			s.logsC.put(key, e, s.now())
		}
		out = sliceLog(id, e, tail, since, until)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, out)
}

// sliceLog cuts the bytes [max(since, end−tail), end) out of a log, the
// start moved forward to a line's start.
func sliceLog(id string, e *logEntry, tail, since, until int64) *jobLog {
	end := e.total
	if until > 0 && until < end {
		end = until
	}
	keptFrom := e.total - int64(len(e.data))
	if end < keptFrom { // until before what's kept: nothing of it is here
		end = keptFrom
	}
	start := max(since, end-tail, 0)
	if start < keptFrom {
		start = keptFrom
	}
	if start > end {
		start = end
	}
	at := func(off int64) byte { return e.data[off-keptFrom] }
	if start > 0 && start < end && (start == keptFrom || at(start-1) != '\n') {
		for start < end && at(start) != '\n' {
			start++
		}
		if start < end {
			start++
		}
	}
	return &jobLog{ID: id, Text: string(e.data[start-keptFrom : end-keptFrom]), Bytes: e.total, From: start,
		Complete: true, Truncated: start > 0, URL: e.url}
}

// downloadLog follows GitHub's redirect to the log's storage and keeps its
// last 8 MiB. Storage is another host (*.actions.githubusercontent.com,
// *.blob.core.windows.net): a narrowed net binding that leaves it out
// fails here, and the refusal names it.
func (s *srv) downloadLog(ctx context.Context, a ghAuth, u string) ([]byte, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, refuse(refInvalid, "a bad upstream address")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+a.bearer.Reveal())
	resp, err := s.gh.hc.Do(req)
	if err != nil {
		var ue *url.Error
		host := req.URL.Host
		if errors.As(err, &ue) {
			if p, perr := url.Parse(ue.URL); perr == nil {
				host = p.Host
			}
		}
		e := refuse(refUpstream, "the job's log couldn't be fetched from %s (is it in this tile's net binding?)", host)
		e.Upstream = &upstreamErr{Status: 0, Message: "unreachable: " + host}
		return nil, 0, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
			return nil, 0, refuse(refNotFound, "GitHub has no log for that job (any more)")
		}
		return nil, 0, ghError(&ghResp{Status: resp.StatusCode, Header: resp.Header, Body: b}, s.now())
	}
	ring := make([]byte, 0, 1<<20)
	var total int64
	buf := make([]byte, 64<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			total += int64(n)
			ring = append(ring, buf[:n]...)
			if len(ring) > logKeepMax {
				ring = append(ring[:0], ring[len(ring)-logKeepMax:]...)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return nil, 0, refuse(refUnavailable, "the job's log broke off")
		}
	}
	return ring, total, nil
}

func (s *srv) handleAnnotations(w http.ResponseWriter, r *http.Request, c who) {
	q := r.URL.Query()
	id := r.PathValue("id")
	if _, err := strconv.ParseInt(id, 10, 64); err != nil {
		fail(w, refuse(refInvalid, "a check run's id"))
		return
	}
	if l, err := strconv.Atoi(q.Get("limit")); err == nil && l > 50 {
		q.Set("limit", "50")
	}
	limit, pg, err := ghPaging(q)
	if err != nil {
		fail(w, err)
		return
	}
	limit = min(limit, 50)
	ctx := r.Context()
	repo := q.Get("repo")
	out := &page[annotation]{Items: []annotation{}}
	err = s.withAuth(ctx, c, q.Get("as"), repo, false, func(a ghAuth) error {
		var list []struct {
			Path      string `json:"path"`
			StartLine int    `json:"start_line"`
			EndLine   int    `json:"end_line"`
			Level     string `json:"annotation_level"`
			Title     string `json:"title"`
			Message   string `json:"message"`
		}
		rr, err := s.gh.call(ctx, a, http.MethodGet, s.repoBase(repo)+"/check-runs/"+id+"/annotations?per_page="+strconv.Itoa(limit)+"&page="+strconv.Itoa(pg), nil, &list)
		if err != nil {
			return err
		}
		for _, g := range list {
			lv := g.Level
			if lv != "warning" && lv != "failure" {
				lv = "notice"
			}
			out.Items = append(out.Items, annotation{Path: g.Path, StartLine: g.StartLine, EndLine: g.EndLine, Level: lv,
				Title: clip(g.Title, 1024), Message: clip(g.Message, checkTextMax)})
		}
		if nextLink(rr.Header) != "" {
			out.Next = cursorAt(pg + 1)
		}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeGET(w, r, out)
}

// handleRerun re-runs a workflow run as the asking person — never the bot
// (a rerun spends CI minutes and can deploy) — while the App has actions:
// write and the policy allows it.
func (s *srv) handleRerun(w http.ResponseWriter, r *http.Request, c who) {
	var req rerunReq
	if err := readBody(r, &req); err != nil {
		fail(w, err)
		return
	}
	if s.mode == modeUser && !s.rerunOffered() { // elsewhere: no person, 403 identity below
		fail(w, refuse(refUnsupported, "this scm-github doesn't offer reruns (the App needs actions: write — preset ci — and the policy allowRerun)"))
		return
	}
	if _, err := strconv.ParseInt(req.RunID, 10, 64); err != nil {
		fail(w, refuse(refInvalid, "runId is a workflow run's id"))
		return
	}
	as, err := s.resolveAs(c, req.As)
	if err == nil && as != asPerson {
		e := refuse(refIdentity, "a rerun is a person's own, never the bot's")
		e.Identities = []string{asPerson}
		err = e
	}
	if err != nil {
		fail(w, err)
		return
	}
	ctx := r.Context()
	var out rerunResp
	err = s.withAuth(ctx, c, asPerson, req.Repo, true, func(a ghAuth) error {
		u := s.repoBase(req.Repo) + "/actions/runs/" + req.RunID
		var run ghRun
		if _, err := s.gh.call(ctx, a, http.MethodGet, u, nil, &run); err != nil {
			return err
		}
		if run.Status != "completed" {
			return refuse(refExists, "that run is still going: re-run it once it has finished")
		}
		what := "/rerun"
		if req.FailedOnly {
			what = "/rerun-failed-jobs"
		}
		if _, err := s.gh.call(ctx, a, http.MethodPost, u+what, map[string]any{}, nil); err != nil {
			return err
		}
		out = rerunResp{RunID: req.RunID, Attempt: max(run.Attempt, 1) + 1}
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}
