// ci_routes.go — the routes of CI in the conversation (API.md §CI in the
// conversation), on a conversation's root: its CI view (re-read where a read
// would tell more, with fresh=1), a job's log and a check's annotations —
// only of ids the watch's stored snapshot holds, never one a caller made up
// — watching a branch or pull request by hand and unwatching, and re-running
// a workflow run's failed jobs: a person's click only, in their own
// partition, as themselves (never the bot). The run view's `ci` key is
// runViewHooks' (ciRunView). Every text served here is the platform's
// untrusted output, ANSI codes stripped and redacted: the views draw it as
// plain text.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func init() {
	routeTables = append(routeTables, ciRoutes)
	runViewHooks = append(runViewHooks, ciRunView)
}

func ciRoutes() []routeDef {
	return []routeDef{
		{"GET /runs/{id}/ci", needViewer, handleCI},
		{"GET /runs/{id}/ci/jobs/{job}/log", needViewer, handleCILog},
		{"GET /runs/{id}/ci/checks/{check}/annotations", needViewer, handleCIAnnotations},
		{"POST /runs/{id}/ci/watch", needParticipant, handleCIWatch},
		{"DELETE /runs/{id}/ci/watch/{wid}", needParticipant, handleCIUnwatch},
		{"POST /runs/{id}/ci/rerun", needParticipant, handleCIRerun},
	}
}

// Log tails (bytes).
const (
	ciTailDefault = 65536
	ciTailMax     = 262144
)

// ciRoot is the route's conversation root (the error answered: 0).
func ciRoot(w http.ResponseWriter, r *http.Request) (*DB, int64) {
	d := projAg().db
	run, err := d.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, http.StatusNotFound, "no such run")
		return nil, 0
	}
	return d, rootOf(run)
}

// ciKind is a provider's host family (its hello's scm.kind; "" unknown).
func ciKind(ctx context.Context, scm string) string {
	api, err := scmFor(scm)
	if err != nil {
		return ""
	}
	h, err := api.Hello(ctx)
	if err != nil {
		return ""
	}
	return h.SCM.Kind
}

// ciCanRerun: c may re-run CI here — a person (not viewed as) taking part
// in the conversation, in their own partition (the only place they are
// themselves at the provider).
func ciCanRerun(c who, lv level) bool {
	return c.kind == whoUser && c.viewedBy == "" && userMode() && c.user == runUser && lv >= lvParticipant
}

// ciCanWatch: c takes part in root, an scm provider is bound, and root
// works in a sandbox (bound, a coding agent's, a coding agent child's) or
// is a project's task, or already watches something.
func ciCanWatch(d *DB, root int64, lv level) bool {
	if lv < lvParticipant || len(scmBound()) == 0 {
		return false
	}
	run, err := d.getRun(root)
	if err != nil {
		return false
	}
	if run.Origin == originProject {
		return true
	}
	if cfg, err := d.runConfig(root); err == nil && (cfg.Sandbox != nil || cfg.Harness != nil) {
		return true
	}
	var n int
	_ = d.q.QueryRow(`SELECT (SELECT count(*) FROM runs WHERE root_id=? AND engine=?) + (SELECT count(*) FROM ci_watch WHERE root_run=? AND ended_ms=0)`,
		root, engineHarness, root).Scan(&n)
	return n > 0
}

// ciRunView (runViewHooks): a conversation's answers carry ci — its
// summary (null: nothing watched) and whether the caller may watch more —
// so the chip is right the moment it opens.
func ciRunView(t *DB, c who, run *Run, v map[string]any) {
	root := rootOf(run)
	a, err := t.loadACL(root)
	lv := lvNone
	if err == nil {
		lv = a.level(c)
	}
	v["ci"] = map[string]any{"summary": ciSummaryOf(t.ciLive(root)), "canWatch": ciCanWatch(t, root, lv)}
}

// ciViewOf is GET /runs/{id}/ci's answer for a caller at lv.
func ciViewOf(ctx context.Context, d *DB, root int64, c who, lv level) CIView {
	ws := d.ciLive(root)
	ciSortedByID(ws)
	v := CIView{Root: root, Watches: []CIWatchView{}, CanWatch: ciCanWatch(d, root, lv)}
	if s := ciSummaryOf(ws); s != nil {
		v.Summary = *s
	} else {
		v.Summary = CISummary{State: ciNone}
	}
	kinds, live, rerun := map[string]string{}, true, false
	for _, w := range ws {
		if _, ok := kinds[w.SCM]; !ok {
			kinds[w.SCM] = ciKind(ctx, w.SCM)
			api, err := scmFor(w.SCM)
			var h *scmHello
			if err == nil {
				h, err = api.Hello(ctx)
			}
			live = live && err == nil && h.Events.Healthy
			rerun = rerun || (err == nil && h.has(scmCapRerun))
		}
		v.Watches = append(v.Watches, ciView(w, kinds[w.SCM]))
	}
	v.Live = live && len(ws) > 0
	v.CanRerun = rerun && ciCanRerun(c, lv)
	return v
}

// handleCI: GET /runs/{id}/ci?fresh= — the conversation's CI. A task with
// no watch yet gets its own first; fresh=1 re-reads what a read would tell
// more about, once per watch however many ask.
func handleCI(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	ciTaskLazy(d, root)
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	ciFreshen(ctx, d, root, r.URL.Query().Get("fresh") == "1")
	xbin.WriteJSON(w, http.StatusOK, ciViewOf(r.Context(), d, root, callerOf(r), levelOf(r)))
}

// ciWatchOf is the route's watch (?watch= or {wid}) of root, live or not
// (nil: answered 404).
func ciWatchOf(w http.ResponseWriter, d *DB, root int64, raw string) *ciWatch {
	id, _ := strconv.ParseInt(raw, 10, 64)
	x := d.ciWatchByID(id)
	if x == nil || x.RootRun != root {
		xbin.WriteError(w, http.StatusNotFound, "no such CI watch in this conversation")
		return nil
	}
	return x
}

// ciHasJob: job is one of the snapshot's jobs.
func ciHasJob(c *scmChecks, job string) bool {
	if c == nil || job == "" {
		return false
	}
	for _, r := range c.WorkflowRuns {
		for _, j := range r.Jobs {
			if j.ID == job {
				return true
			}
		}
	}
	return false
}

// ciHasCheck: check is one of the snapshot's check runs.
func ciHasCheck(c *scmChecks, check string) bool {
	if c == nil || check == "" {
		return false
	}
	for _, k := range c.Checks {
		if k.ID == check {
			return true
		}
	}
	return false
}

// ciRunOf is the snapshot's workflow run id (nil: none).
func ciRunOf(c *scmChecks, id string) *scmWorkflowRun {
	if c == nil || id == "" {
		return nil
	}
	for i := range c.WorkflowRuns {
		if c.WorkflowRuns[i].ID == id {
			return &c.WorkflowRuns[i]
		}
	}
	return nil
}

func qInt64(r *http.Request, k string) int64 {
	n, _ := strconv.ParseInt(r.URL.Query().Get(k), 10, 64)
	return max(n, 0)
}

// handleCILog: GET /runs/{id}/ci/jobs/{job}/log?watch=&tail=&since=&until=
func handleCILog(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	x := ciWatchOf(w, d, root, r.URL.Query().Get("watch"))
	if x == nil {
		return
	}
	job := r.PathValue("job")
	if !ciHasJob(x.checks(), job) {
		xbin.WriteError(w, http.StatusNotFound, "no such job in this watch's CI")
		return
	}
	tail := int(qInt64(r, "tail"))
	if tail <= 0 {
		tail = ciTailDefault
	}
	tail = min(tail, ciTailMax)
	api, err := scmFor(x.SCM)
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	lg, err := api.JobLog(r.Context(), x.Repo, job, tail, qInt64(r, "since"), qInt64(r, "until"), ciAs(d, x))
	if err != nil {
		var se *scmError
		if errors.As(err, &se) && se.Refusal == scmRefInProgress {
			xbin.WriteJSON(w, http.StatusConflict, map[string]any{"error": "the job is still running: its log is ready when it finishes",
				"refusal": scmRefInProgress, "url": ciLink(se.URL)})
			return
		}
		writeSCMErr(w, r, err)
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"text": ciLogText(lg.Text), "bytes": lg.Bytes, "from": lg.From,
		"complete": lg.Complete, "truncated": lg.Truncated, "url": ciLink(lg.URL)})
}

// ansiRe finds terminal escape sequences: CSI (colours, cursor moves), OSC
// (titles, links; ended by BEL or ST), and any other two-byte escape.
var ansiRe = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-Za-z]|\x1b[@-_]|\x{9b}[0-?]*[ -/]*[@-~]`)

// ciCtrlRe finds control characters a log line keeps none of (tab and
// newline stay).
var ciCtrlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f\x{80}-\x{9f}]`)

// stripANSI is s without its escape sequences and control characters; a
// carriage return ends the line (a progress bar's redraws are kept as
// lines).
func stripANSI(s string) string {
	s = ansiRe.ReplaceAllString(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return ciCtrlRe.ReplaceAllString(s, "")
}

// ciLogText is a log as it is served: stripped, its invisible characters
// gone, redacted (never clipped: the caller asked for the tail it got).
func ciLogText(s string) string {
	return projRedact(invisibles.ReplaceAllString(stripANSI(strings.ToValidUTF8(s, "")), ""))
}

// handleCIAnnotations: GET /runs/{id}/ci/checks/{check}/annotations?watch=&cursor=
func handleCIAnnotations(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	x := ciWatchOf(w, d, root, r.URL.Query().Get("watch"))
	if x == nil {
		return
	}
	check := r.PathValue("check")
	if !ciHasCheck(x.checks(), check) {
		xbin.WriteError(w, http.StatusNotFound, "no such check in this watch's CI")
		return
	}
	api, err := scmFor(x.SCM)
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	page, err := api.Annotations(r.Context(), x.Repo, check, r.URL.Query().Get("cursor"), ciAs(d, x))
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	items := []scmAnnotation{}
	for i, a := range page.Items {
		if i >= 50 {
			break
		}
		lvl := a.Level
		if lvl != "notice" && lvl != "warning" && lvl != "failure" {
			lvl = "notice"
		}
		items = append(items, scmAnnotation{Path: ciText(a.Path, 500), StartLine: max(a.StartLine, 0), EndLine: max(a.EndLine, 0),
			Level: lvl, Title: ciText(a.Title, ciNameMax), Message: ciText(stripANSI(a.Message), ciTextMax)})
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next": clip(page.Next, 500)})
}

// ciRefRe is a branch a person may name (as ciBranchRe), or a pull ref.
var ciRefRe = ciBranchRe

// handleCIWatch: POST /runs/{id}/ci/watch {scm?, repo, ref?, pr?} — watch a
// branch, or a pull request's head branch, by hand.
func handleCIWatch(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	var body struct {
		SCM  string `json:"scm"`
		Repo string `json:"repo"`
		Ref  string `json:"ref"`
		PR   int    `json:"pr"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	body.Repo, body.Ref = strings.TrimSpace(body.Repo), strings.TrimSpace(body.Ref)
	switch {
	case !validRepo(body.Repo):
		xbin.WriteError(w, http.StatusBadRequest, "repo: owner/name")
		return
	case body.Ref == "" && body.PR <= 0:
		xbin.WriteError(w, http.StatusBadRequest, "name a branch (ref) or a pull request (pr)")
		return
	case body.Ref != "" && (!ciRefRe.MatchString(body.Ref) || strings.Contains(body.Ref, "..")):
		xbin.WriteError(w, http.StatusBadRequest, "ref: a branch name")
		return
	}
	c := callerOf(r)
	if why := botRefusal(c, body.Repo); why != "" {
		xbin.WriteError(w, http.StatusForbidden, why)
		return
	}
	api, err := scmOnly(body.SCM)
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	nw := &ciWatch{RootRun: root, RunID: root, Source: ciManual, SCM: api.Provider(), Repo: body.Repo, Ref: body.Ref,
		PR: max(body.PR, 0), ByUser: sbxUserOf(c)}
	as := scmAsBot
	if userMode() {
		as = scmAsPerson
	}
	if h, err := api.Hello(r.Context()); err == nil && len(h.Hosts) == 1 {
		nw.Host = strings.ToLower(h.Hosts[0])
	}
	if body.PR > 0 {
		pl, err := api.Pull(r.Context(), body.Repo, body.PR, as)
		if err != nil {
			writeSCMErr(w, r, err)
			return
		}
		if body.Ref == "" {
			nw.Ref = pl.Head.Ref
		}
		nw.SHA = ciSHA(pl.Head.SHA)
		if nw.Ref == "" || !ciRefRe.MatchString(nw.Ref) || pl.Head.Ref != nw.Ref {
			xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("pull request #%d's head isn't branch %s", body.PR, nw.Ref))
			return
		}
		if pl.State == "merged" || pl.State == "closed" {
			nw.State = ciGone
		}
	}
	var x *ciWatch
	created := false
	err = d.Tx(func(t *DB) error {
		var err error
		x, created, err = ciUpsert(t, nw)
		if err == nil && created {
			ciStarted(t, x.ID)
		}
		return err
	})
	if errors.Is(err, errCILimit) {
		xbin.WriteJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "refusal": "limit"})
		return
	}
	if err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_ = ciRead(ctx, d, x.ID) // the first read (or the one under way), so the answer shows something
	if y := d.ciWatchByID(x.ID); y != nil {
		x = y
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	xbin.WriteJSON(w, status, ciView(x, ciKind(r.Context(), x.SCM)))
}

// handleCIUnwatch: DELETE /runs/{id}/ci/watch/{wid}. A task's own watch
// stays while the task does (409).
func handleCIUnwatch(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	x := ciWatchOf(w, d, root, r.PathValue("wid"))
	if x == nil {
		return
	}
	if x.Source == ciTask && x.EndedMs == 0 {
		xbin.WriteError(w, http.StatusConflict, "this is the task's own CI: it is watched while the task is open")
		return
	}
	if x.EndedMs == 0 {
		if err := d.Tx(func(t *DB) error { return ciEnd(t, x) }); err != nil {
			xbin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCIRerun: POST /runs/{id}/ci/rerun {watch, runId, failedOnly} — a
// person's own, in their own partition, as themselves; the run must be one
// in the watch's snapshot. Noted in the conversation's journal.
func handleCIRerun(w http.ResponseWriter, r *http.Request) {
	d, root := ciRoot(w, r)
	if d == nil {
		return
	}
	var body struct {
		Watch      json.Number `json:"watch"`
		RunID      string      `json:"runId"`
		FailedOnly bool        `json:"failedOnly"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	c := callerOf(r)
	if !ciCanRerun(c, levelOf(r)) {
		xbin.WriteJSON(w, http.StatusForbidden, map[string]any{"error": "re-running CI is a person's own, as themselves, from their own space",
			"refusal": scmRefIdentity})
		return
	}
	x := ciWatchOf(w, d, root, body.Watch.String())
	if x == nil {
		return
	}
	wr := ciRunOf(x.checks(), body.RunID)
	if wr == nil {
		xbin.WriteError(w, http.StatusNotFound, "no such workflow run in this watch's CI")
		return
	}
	api, err := scmFor(x.SCM)
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	if h, err := api.Hello(r.Context()); err != nil || !h.has(scmCapRerun) {
		xbin.WriteJSON(w, http.StatusNotImplemented, map[string]any{"error": "this scm provider doesn't re-run CI", "refusal": scmRefUnsupported})
		return
	}
	res, err := api.Rerun(r.Context(), scmRerunReq{Repo: x.Repo, RunID: wr.ID, FailedOnly: body.FailedOnly, As: scmAsPerson})
	if err != nil {
		writeSCMErr(w, r, err)
		return
	}
	what := "re-ran " + wr.Name
	if body.FailedOnly {
		what = "re-ran the failed jobs of " + wr.Name
	}
	note := fmt.Sprintf("%s %s (%s %s)", c.user, what, x.Repo, x.Ref)
	_ = d.Tx(func(t *DB) error {
		if e := projEng(); e != nil {
			e.emitStep(t, root, t.journal(root, "note", map[string]string{"text": ciText(note, 500)}))
		} else {
			t.journal(root, "note", map[string]string{"text": ciText(note, 500)})
		}
		ciReadLater(t, x.ID)
		return nil
	})
	xbin.WriteJSON(w, http.StatusAccepted, map[string]any{"runId": res.RunID, "attempt": res.Attempt})
}
