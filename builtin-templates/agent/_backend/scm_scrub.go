// scm_scrub.go — taking a project's credential out of a sandbox, the
// worker's two job kinds (creds, scrub) and the refresher.
//
// Scrubbing empties both files (0600, zero bytes), revokes the purpose at
// the provider (best effort: the provider forgets it either way), drops
// the token from memory (still masked until it expires) and marks the row
// scrubbed with why — or, when the files couldn't be emptied, leaves it
// live and due (scmCredUnemptied), so the next trigger tries again. The triggers are one line each where the moment is:
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
	// the projects store's (a scrub job, scmScrubWhy)
	scrubRepoRemoved = "repo-removed"
	scrubLeft        = "left" // the sandbox left the project
)

// scmScrub empties p's credentials in ref ("" = every sandbox p wrote
// them to) and revokes them (scmScrubCreds). Every row is tried; the first
// error is answered.
func scmScrub(ctx context.Context, p *Project, ref, why string) error {
	d := scmDB()
	if p == nil || d == nil {
		return nil
	}
	var first error
	for _, c := range d.scmCredsOf(p.ID, ref) {
		if c.State != credLive {
			continue
		}
		release := scmHoldSandbox(c.Ref)
		err := scmScrubRow(ctx, p, c, why, nil)
		release()
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// scmScrubRow scrubs one row (box: the sandbox as already read; nil = read
// it now); the caller holds c.Ref's lock (scmHoldSandbox). A sandbox that
// is gone has nothing left to empty.
func scmScrubRow(ctx context.Context, p *Project, c scmCredRow, why string, box *sbxSandbox) error {
	key := scmLiveKey(c.PID, c.Ref, c.Host)
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
	scmRevoke(ctx, p, c) // whatever the files say: the value stops working
	scmLiveDrop(key)
	if werr != nil {
		// The files may still hold the value: the row stays live (what
		// scmLiveIn and scmScrub select, so the share stays refused and the
		// next scrub tries again), due at once (scmCredsDue: the revoked
		// value is replaced at the next turn, if the gate still allows it).
		if err := scmDB().scmCredUnemptied(c.PID, c.Ref, c.Host, why); err != nil {
			logf("project #%d: its credential in %s: %v", c.PID, c.Ref, err)
		}
		return fmt.Errorf("emptying the credential in %s: %w", c.Ref, werr)
	}
	if err := scmDB().scmSetCredState(c.PID, c.Ref, c.Host, credScrubbed, why); err != nil {
		return fmt.Errorf("emptying the credential in %s: %w", c.Ref, err)
	}
	return nil
}

// scmRevoke asks the provider to revoke what it handed out for c's purpose
// — p's provider, or every bound one when p's is unknown (a purpose names
// one project and sandbox: another provider answers not-found).
func scmRevoke(ctx context.Context, p *Project, c scmCredRow) {
	if c.Purpose == "" {
		return
	}
	providers := []string{p.SCM}
	if p.SCM == "" {
		providers = scmBound()
	}
	for _, name := range providers {
		api, err := scmFor(name)
		if err == nil {
			rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			err = api.Revoke(rctx, scmRevokeReq{Purpose: c.Purpose})
			cancel()
		}
		if err != nil && !scmRefused(err, scmRefNotFound) {
			logf("project #%d: revoking its credential for %s at %s: %v", c.PID, c.Ref, name, err)
		}
	}
}

// scmProjectFor is the project a row of ref belongs to: the projects
// store's (projectsInSandbox), else its row in projects (the frozen DDL),
// else what the row itself says (its purpose carries the project's uid) —
// enough to empty and revoke. provider: the one the caller knows ("" =
// the project's, else every bound one, scmRevoke).
func scmProjectFor(c scmCredRow, provider string) *Project {
	for _, p := range projectsInSandbox(c.Ref) {
		if p.ID == c.PID {
			return p
		}
	}
	p := &Project{ID: c.PID, SCM: provider, Owner: c.ForUser, Host: c.Host, SandboxRef: c.Ref, State: projActive}
	var uid, scm, owner string
	if d := scmDB(); d != nil && d.q.QueryRow(`SELECT uid, scm, owner FROM projects WHERE id=?`, c.PID).Scan(&uid, &scm, &owner) == nil {
		p.UID, p.Owner = uid, orStr(owner, p.Owner)
		if provider == "" {
			p.SCM = scm
		}
	}
	if rest, ok := strings.CutPrefix(c.Purpose, "proj:"); ok && p.UID == "" {
		p.UID, _, _ = strings.Cut(rest, ":")
	}
	return p
}

// scmLiveIn is every live row in sandbox ref.
func scmLiveIn(ref string) []scmCredRow {
	d := scmDB()
	if d == nil {
		return nil
	}
	return scmScanCreds(d.q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE sandbox_ref=? AND state='live'`, ref))
}

// scmScrubSandbox scrubs every project's credential in ref (a share
// through the agent; scmScrubOnAction), the caller holding ref's lock. The
// first error is answered.
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
// (handlePatchSandbox, holding ref's lock through the PATCH): false (the error written) refuses the share — a
// credential that couldn't be emptied must not become readable by others.
func scmScrubForShare(w http.ResponseWriter, ctx context.Context, ref, label string) bool {
	if err := scmScrubSandbox(ctx, ref, scrubShare); err != nil {
		xbin.WriteError(w, http.StatusBadGateway, label+" isn't shared: a project's credential there couldn't be emptied: "+err.Error()+" — try again")
		return false
	}
	return true
}

// scmScrubOnAction is the stop and archive trigger (handleSandboxAction):
// for those two it takes ref's lock, scrubs every credential there (best
// effort) and returns the lock's release, which the caller defers past the
// lifecycle call — no credential is written between the scrub and the
// stop. Any other action: nothing, and a release that does nothing.
func scmScrubOnAction(ctx context.Context, ref, action string) func() {
	if action != scrubStop && action != scrubArchive {
		return func() {}
	}
	release := scmHoldSandbox(ref)
	_ = scmScrubSandbox(ctx, ref, action)
	return release
}

// projectSandboxGone: sandbox ref was deleted through the agent — nothing
// is left to empty; what was handed out for it is revoked.
func projectSandboxGone(ref string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	defer scmHoldSandbox(ref)()
	for _, c := range scmLiveIn(ref) {
		p := scmProjectFor(c, "")
		scmRevoke(ctx, p, c)
		scmLiveDrop(scmLiveKey(c.PID, c.Ref, c.Host))
		if err := scmDB().scmSetCredState(c.PID, c.Ref, c.Host, credScrubbed, scrubDelete); err != nil {
			logf("project #%d: its credential in %s (deleted): %v", c.PID, ref, err)
		}
	}
}

// scmForgetScrub scrubs every person credential of this partition's person
// from provider — before the provider forgets the sign-in.
func scmForgetScrub(ctx context.Context, provider string) error {
	rows := scmScanCreds(scmDB().q.Query(`SELECT `+scmCredCols+` FROM project_creds WHERE state='live' AND identity='person' AND for_user=?`, runUser))
	var first error
	for _, c := range rows {
		p := scmProjectFor(c, provider)
		if p.SCM != provider {
			continue
		}
		release := scmHoldSandbox(c.Ref)
		err := scmScrubRow(ctx, p, c, scrubForget, nil)
		release()
		if err != nil && first == nil {
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
// of p's, for a project's own job), why from the job (scmScrubWhy).
func scmScrubJob(ctx context.Context, p *Project, k *ProjectTask, j *ProjectJob) (jobOutcome, error) {
	ref := ""
	if k != nil && k.ForkMade && k.SandboxRef != "" {
		ref = k.SandboxRef
	}
	if err := scmScrub(ctx, p, ref, scmScrubWhy(p, k, j)); err != nil {
		return jobOutcome{}, err
	}
	return jobOutcome{Done: true}, nil
}

// scmScrubWhy is the word a scrub job records (API.md §scm providers and
// credentials, "Scrubbing"'s why): the one its
// queuer put in the job's step, else what the job says — a repo's job is
// a repo removed; an archived or deleting project, archive or delete; a
// task's fork, delete (it goes next); else the sandbox left the project.
func scmScrubWhy(p *Project, k *ProjectTask, j *ProjectJob) string {
	if j != nil {
		switch j.Step {
		case scrubShare, scrubStop, scrubArchive, scrubDelete, scrubRepoRemoved, scrubForget, scrubLeft:
			return j.Step
		}
		if j.Repo != "" {
			return scrubRepoRemoved
		}
	}
	switch {
	case p.State == projArchived:
		return scrubArchive
	case p.State == projDeleting, k != nil && k.ForkMade:
		return scrubDelete
	}
	return scrubLeft
}

// --- the refresher --------------------------------------------------------------------

// scmRetryIdle is how long the refresher leaves a due token whose project
// has no task at work (or whose refresh failed) before it looks again.
var scmRetryIdle = 2 * time.Minute

// scmKick wakes the refresher: a token was handed out, its timer moves.
var scmKick = make(chan struct{}, 1)

func scmKickRefresher() {
	select {
	case scmKick <- struct{}{}:
	default:
	}
}

// scmDueAt is when l is due: the provider's refreshAfter, or 75 % of its
// life if sooner — not before its next try.
func scmDueAt(l *scmLive) int64 {
	return max(min(l.refresh, l.written+(l.expires-l.written)*3/4), l.nextTry)
}

// scmRefreshLoop re-mints, before it lapses, each token this process
// handed out (scmDueAt) while its project has a task at work — a long
// coding-agent turn keeps its partition up, so the push at its end still
// finds a live credential. An idle project's credential is left to lapse;
// the next turn's gate (scmCredsDue) has a fresh one written first. One
// timer, at the next token's instant (no ticker: the backend runs on
// events and timers at known instants).
func scmRefreshLoop(ctx context.Context, _ *Engine) {
	for {
		scmRefreshDue(ctx)
		var wait <-chan time.Time
		var t *time.Timer
		if next := scmNextDue(); next > 0 {
			t = time.NewTimer(time.Duration(max(next-nowMs(), 0)) * time.Millisecond)
			wait = t.C
		}
		select {
		case <-ctx.Done():
		case <-wait:
		case <-scmKick:
		}
		if t != nil {
			t.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// scmNextDue is the earliest instant a live token is due (0: none live).
func scmNextDue() int64 {
	scmLiveMu.Lock()
	defer scmLiveMu.Unlock()
	var next int64
	for _, l := range scmLives {
		if at := scmDueAt(l); next == 0 || at < next {
			next = at
		}
	}
	return next
}

// scmRefreshDue re-mints every token that is due now; one that lapsed is
// let go (still masked until it expires), and one it can't refresh now is
// looked at again after scmRetryIdle.
func scmRefreshDue(ctx context.Context) {
	type due struct {
		pid       int64
		ref, host string
	}
	var todo []due
	now := nowMs()
	scmLiveMu.Lock()
	for k, l := range scmLives {
		switch {
		case l.expires <= now:
			scmRetired[l.token.Reveal()] = l.expires
			delete(scmLives, k)
		case now >= scmDueAt(l):
			l.nextTry = now + scmRetryIdle.Milliseconds()
			pid, ref, host := splitLiveKey(k)
			todo = append(todo, due{pid, ref, host})
		}
	}
	scmRebuildLocked()
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
	err := scmDB().q.QueryRow(`SELECT count(*) FROM project_tasks t JOIN runs r ON r.id=t.run_id
		WHERE t.project_id=? AND t.run_id<>0 AND r.status IN ('running','awaiting','sleeping','waiting_input')`, pid).Scan(&n)
	return err != nil || n > 0
}
