// scm_scrub.go — taking a project's credential out of a sandbox, the
// worker's two job kinds (creds, scrub) and the refresher.
//
// Scrubbing empties both files (0600, zero bytes), revokes the purpose at
// the provider (best effort: the provider forgets it either way), drops
// the token from memory (still masked until it expires) and marks the row
// scrubbed with why. The triggers are one line each where the moment is:
// a sandbox shared, stopped, archived or deleted through the agent
// (sandbox_routes.go), Forget (scm_routes.go), the projects store's own
// (archive, delete, a repo removed, the sandbox leaving), a fork deleted —
// and the gate refusing a sandbox that held one (scm_ensure.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Why a credential was scrubbed (project_creds.why).
const (
	scrubShare   = "share"
	scrubStop    = "stop"
	scrubArchive = "archive"
	scrubDelete  = "delete"
	scrubForget  = "forget"
)

// scmScrub empties p's credentials in ref ("" = every sandbox p wrote
// them to) and revokes them (scmScrubCreds). Every row is tried; the first
// error is answered.
func scmScrub(ctx context.Context, p *Project, ref, why string) error {
	if p == nil || agent == nil || agent.db == nil {
		return nil
	}
	var first error
	for _, c := range agent.db.scmCredsOf(p.ID, ref) {
		if c.State != credLive {
			continue
		}
		if err := scmScrubRow(ctx, p, c, why, nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// scmScrubRow scrubs one row (box: the sandbox as already read; nil = read
// it now). A sandbox that is gone has nothing left to empty.
func scmScrubRow(ctx context.Context, p *Project, c scmCredRow, why string, box *sbxSandbox) error {
	key := scmLiveKey(c.PID, c.Ref, c.Host)
	if box == nil { // the gate's block holds the lock and passes the box
		mu := scmKeyLock(key)
		mu.Lock()
		defer mu.Unlock()
	}
	var werr error
	conn, id, err := sbxDialRef(c.Ref, scmSbxUser(p))
	if err == nil && box == nil {
		box, err = conn.Get(ctx, id)
	}
	switch {
	case err == nil:
		werr = scmEmptyFiles(ctx, conn, id, box, p, c.Host)
	case sbxRefusal(err) != "not-found":
		werr = err
	}
	if werr != nil && sbxRefusal(werr) == "not-found" {
		werr = nil
	}
	scmRevoke(ctx, p, c)
	scmLiveDrop(key)
	if err := agent.db.scmSetCredState(c.PID, c.Ref, c.Host, credScrubbed, why); err != nil && werr == nil {
		werr = err
	}
	if werr != nil {
		return fmt.Errorf("emptying the credential in %s: %w", c.Ref, werr)
	}
	return nil
}

// scmRevoke asks the provider to revoke what it handed out for c's purpose.
func scmRevoke(ctx context.Context, p *Project, c scmCredRow) {
	if c.Purpose == "" || p.SCM == "" {
		return
	}
	api, err := scmFor(p.SCM)
	if err == nil {
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err = api.Revoke(rctx, scmRevokeReq{Purpose: c.Purpose})
		cancel()
	}
	if err != nil && !scmRefused(err, scmRefNotFound) {
		logf("project #%d: revoking its credential for %s at %s: %v", c.PID, c.Ref, p.SCM, err)
	}
}

// scmProjectFor is the project a row of ref belongs to: the projects
// store's (projectsInSandbox), else what the row itself says (its purpose
// carries the project's uid) — enough to empty and revoke.
func scmProjectFor(c scmCredRow, provider string) *Project {
	for _, p := range projectsInSandbox(c.Ref) {
		if p.ID == c.PID {
			return p
		}
	}
	uid := ""
	if rest, ok := strings.CutPrefix(c.Purpose, "proj:"); ok {
		uid, _, _ = strings.Cut(rest, ":")
	}
	return &Project{ID: c.PID, UID: uid, SCM: provider, Owner: c.ForUser, SandboxRef: c.Ref, State: projActive}
}

// scmLiveIn is every live row in sandbox ref.
func scmLiveIn(ref string) []scmCredRow {
	if agent == nil || agent.db == nil {
		return nil
	}
	return scmScanCreds(agent.db.q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE sandbox_ref=? AND state='live'`, ref))
}

// scmScrubSandbox scrubs every project's credential in ref (a stop or an
// archive through the agent; a share). The first error is answered.
func scmScrubSandbox(ctx context.Context, ref, why string) error {
	var first error
	for _, c := range scmLiveIn(ref) {
		if err := scmScrubRow(ctx, scmProjectFor(c, ""), c, why, nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// scmScrubForShare scrubs ref before the PATCH that shares it goes out
// (handlePatchSandbox): false (the error written) refuses the share — a
// credential that couldn't be emptied must not become readable by others.
func scmScrubForShare(w http.ResponseWriter, ctx context.Context, ref, label string) bool {
	if err := scmScrubSandbox(ctx, ref, scrubShare); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, label+" isn't shared: a project's credential there couldn't be emptied: "+err.Error()+" — try again")
		return false
	}
	return true
}

// projectSandboxGone: sandbox ref was deleted through the agent — nothing
// is left to empty; what was handed out for it is revoked.
func projectSandboxGone(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, c := range scmLiveIn(ref) {
		p := scmProjectFor(c, "")
		scmRevoke(ctx, p, c)
		scmLiveDrop(scmLiveKey(c.PID, c.Ref, c.Host))
		if err := agent.db.scmSetCredState(c.PID, c.Ref, c.Host, credScrubbed, scrubDelete); err != nil {
			logf("project #%d: its credential in %s (deleted): %v", c.PID, ref, err)
		}
	}
}

// scmForgetScrub scrubs every person credential of this partition's person
// from provider — before the provider forgets the sign-in.
func scmForgetScrub(ctx context.Context, provider string) error {
	rows := scmScanCreds(agent.db.q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE state='live' AND identity='person' AND for_user=?`, runUser))
	var first error
	for _, c := range rows {
		p := scmProjectFor(c, provider)
		if p.SCM != provider {
			continue
		}
		if err := scmScrubRow(ctx, p, c, scrubForget, nil); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// --- the worker's jobs ----------------------------------------------------------------

// scmSigninWait is how long the creds job waits on a sign-in.
const scmSigninWait = 15 * time.Minute

func init() {
	projectJobKinds[pjCreds] = scmCredsJob
	projectJobKinds[pjScrub] = scmScrubJob
	ownerLoops = append(ownerLoops, scmRefreshLoop)
}

// scmCredsJob is the creds job: a credential for k's sandbox (p's, for a
// project's own job). A sign-in under way: the job waits, polling the
// provider at its interval, for at most 15 minutes.
func scmCredsJob(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	if s := scmSigninOf(p.Owner, p.SCM); s != nil && s.PollID != "" && scmProjectAs(p) == scmAsPerson {
		api, err := scmFor(p.SCM)
		if err != nil {
			return jobOutcome{}, err
		}
		st, err := api.SigninPoll(ctx, s.PollID)
		switch {
		case err != nil && !scmRefused(err, scmRefNotFound):
			return jobOutcome{}, err
		case err != nil || st.State == "done":
			scmClearSignin(p.Owner, p.SCM) // done, or a poll the provider forgot: ask again
		case st.State == "pending":
			if over := scmSigninOver(j); over != nil {
				return jobOutcome{}, over
			}
			return jobOutcome{WaitMs: max(s.IntervalMs, st.RetryAfterMs, 2000), Step: "waiting for a sign-in to " + scmTitle(ctx, api)}, nil
		default:
			scmClearSignin(p.Owner, p.SCM)
			return jobOutcome{}, fmt.Errorf("the sign-in to %s ended: %s", scmTitle(ctx, api), orStr(st.Error, st.State))
		}
	}
	err := ensureCreds(ctx, p, k, "", scmMinLeft)
	var se *scmError
	switch {
	case err == nil:
		return jobOutcome{Done: true}, nil
	case errors.As(err, &se) && se.Refusal == scmRefSignin:
		if over := scmSigninOver(j); over != nil {
			return jobOutcome{}, over
		}
		wait := int64(5000)
		if se.Signin != nil && se.Signin.IntervalMs > 0 {
			wait = se.Signin.IntervalMs
		}
		return jobOutcome{WaitMs: wait, Step: "waiting for a sign-in"}, nil
	}
	return jobOutcome{}, err
}

// scmSigninOver: the job has waited on a sign-in longer than it may.
func scmSigninOver(j *ProjectJob) error {
	if j != nil && j.Created > 0 && nowMs()-j.Created > scmSigninWait.Milliseconds() {
		return errors.New("the sign-in wasn't finished within 15 minutes — sign in, then retry the task")
	}
	return nil
}

// scmScrubJob is the scrub job: p's credentials out of k's fork (every sandbox
// of p's, for a project's own job), why from the project's state.
func scmScrubJob(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	ref, why := "", "scrub"
	if k != nil && k.ForkMade && k.SandboxRef != "" {
		ref = k.SandboxRef
	}
	switch p.State {
	case projArchived:
		why = scrubArchive
	case projDeleting:
		why = scrubDelete
	}
	if err := scmScrub(ctx, p, ref, why); err != nil {
		return jobOutcome{}, err
	}
	return jobOutcome{Done: true}, nil
}

// --- the refresher --------------------------------------------------------------------

// scmRefreshEvery is how often the refresher looks.
var scmRefreshEvery = 30 * time.Second

// scmRefreshLoop re-mints, before it lapses, each token this process
// handed out (at the provider's refreshAfter, or 75 % of its life if
// sooner) while its project has a task at work — a long coding-agent turn
// keeps its partition up, so the push at its end still finds a live
// credential. An idle project's credential is left to lapse; the next
// turn's gate (scmCredsDue) has a fresh one written first.
func scmRefreshLoop(ctx context.Context, _ *Engine) {
	t := time.NewTicker(scmRefreshEvery)
	defer t.Stop()
	for {
		scmRefreshDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// scmRefreshDue re-mints every token that is due now.
func scmRefreshDue(ctx context.Context) {
	type due struct {
		pid       int64
		ref, host string
	}
	var todo []due
	now := nowMs()
	scmLiveMu.Lock()
	for k, l := range scmLives {
		if now >= min(l.refresh, l.written+(l.expires-l.written)*3/4) {
			pid, ref, host := splitLiveKey(k)
			todo = append(todo, due{pid, ref, host})
		}
	}
	scmLiveMu.Unlock()
	for _, d := range todo {
		if ctx.Err() != nil {
			return
		}
		var p *Project
		for _, x := range projectsInSandbox(d.ref) {
			if x.ID == d.pid {
				p = x
			}
		}
		if p == nil || !scmProjectBusy(p.ID) {
			continue
		}
		key := scmLiveKey(d.pid, d.ref, d.host)
		l := scmLiveGet(key)
		if l == nil {
			continue
		}
		// a minLeft past its expiry makes ensure mint again
		left := time.Until(time.UnixMilli(l.expires)) + time.Minute
		if err := ensureCreds(ctx, p, nil, d.ref, left); err != nil {
			logf("project #%d: refreshing its credential in %s: %v", d.pid, d.ref, err)
		}
	}
}

// splitLiveKey takes a scmLiveKey apart: project, sandbox, host.
func splitLiveKey(k string) (int64, string, string) {
	first, last := strings.IndexByte(k, '|'), strings.LastIndexByte(k, '|')
	if first < 0 || last <= first {
		return 0, "", ""
	}
	pid, _ := strconv.ParseInt(k[:first], 10, 64)
	return pid, k[first+1 : last], k[last+1:]
}

// scmProjectBusy: a task run of project pid is at work (running, awaiting,
// sleeping or waiting for a person). A store that can't say: yes.
func scmProjectBusy(pid int64) bool {
	var n int
	err := agent.db.q.QueryRow(`SELECT count(*) FROM project_tasks t JOIN runs r ON r.id=t.run_id
		WHERE t.project_id=? AND t.run_id<>0 AND r.status IN ('running','awaiting','sleeping','waiting_input')`, pid).Scan(&n)
	return err != nil || n > 0
}
