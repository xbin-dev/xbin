// sandbox_jobs.go — bash and the jobs it becomes (D115).
//
// Every bash call is an exec at the manager (docs/sandbox-manager.md
// §Background execs) and a numbered job of the conversation (sandbox_jobs,
// numbered per root). bash follows the exec with the output long-poll; at its
// timeout the command goes on as a job the model follows with bash_output
// and stops with bash_kill. The exec is named after the tool call (clientId
// agent:<run>:<call>), so starting it twice finds the one command.
//
// When a turn is interrupted or cancelled, the command's process group gets
// TERM, then KILL. When the backend hands over to a successor (a restart), it
// is left running: the successor's transcript repair answers the call with
// the job it became (lostResultText), and bash_output picks it up from the
// start — offsets resume across restarts.
package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxRunningJobs  = 8   // commands running at once in one conversation
	bashDefaultWait = 120 // seconds bash waits before the command goes on as a job
	bashMaxWait     = 3600
	jobWaitMax      = 600 // bash_output's wait_s
	outputPoll      = 30 * time.Second
	outputChunk     = 256 << 10
)

// killGrace is how long TERM gets before KILL.
var killGrace = 3 * time.Second

// bashEnv keeps commands from waiting on a terminal nobody is at, and their
// output free of colors and pagers.
var bashEnv = map[string]string{"TERM": "dumb", "NO_COLOR": "1", "PAGER": "cat", "GIT_TERMINAL_PROMPT": "0"}

const sandboxJobSchemaSQL = `
CREATE TABLE IF NOT EXISTS sandbox_jobs (
	root_id      INTEGER NOT NULL,
	job          INTEGER NOT NULL,
	run_id       INTEGER NOT NULL,
	tool_call_id TEXT NOT NULL DEFAULT '',
	ref          TEXT NOT NULL,
	exec_id      TEXT NOT NULL DEFAULT '',
	command      TEXT NOT NULL,
	cwd          TEXT NOT NULL DEFAULT '',
	state        TEXT NOT NULL DEFAULT 'starting',
	exit_code    INTEGER,
	read_off     INTEGER NOT NULL DEFAULT 0,
	fg           INTEGER NOT NULL DEFAULT 1,
	created_ms   INTEGER NOT NULL,
	ended_ms     INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (root_id, job)
);
CREATE INDEX IF NOT EXISTS idx_sandbox_jobs_call ON sandbox_jobs(run_id, tool_call_id);
`

func (d *DB) addSandboxJobSchema() error {
	_, err := d.q.Exec(sandboxJobSchemaSQL)
	return err
}

// sbxJob is one command a conversation ran in a sandbox.
type sbxJob struct {
	Root    int64
	Job     int
	Run     int64  // the run whose bash started it
	Call    string // its tool call
	Ref     string // the sandbox
	Exec    string // the manager's exec id ("" until it started)
	Command string
	Cwd     string
	State   string // starting | running | exited | killed | lost
	Exit    *int
	ReadOff int64 // the output read so far (bash_output continues here)
	FG      bool  // its bash call followed it to the end (false: it went on as a job)
	Created int64 // unix ms
	Ended   int64
}

func (j *sbxJob) running() bool { return j.State == "starting" || j.State == "running" }

// clientID names the exec after its tool call.
func (j *sbxJob) clientID() string {
	if j.Call == "" {
		return fmt.Sprintf("agent:%d:job-%d", j.Root, j.Job)
	}
	return fmt.Sprintf("agent:%d:%s", j.Run, j.Call)
}

const jobCols = `root_id, job, run_id, tool_call_id, ref, exec_id, command, cwd, state, exit_code, read_off, fg, created_ms, ended_ms`

func scanJob(sc interface{ Scan(...any) error }) (*sbxJob, error) {
	j := &sbxJob{}
	var exit sql.NullInt64
	var fg int
	if err := sc.Scan(&j.Root, &j.Job, &j.Run, &j.Call, &j.Ref, &j.Exec, &j.Command, &j.Cwd, &j.State, &exit,
		&j.ReadOff, &fg, &j.Created, &j.Ended); err != nil {
		return nil, err
	}
	if exit.Valid {
		c := int(exit.Int64)
		j.Exit = &c
	}
	j.FG = fg == 1
	return j, nil
}

func (d *DB) job(root int64, n int) (*sbxJob, error) {
	j, err := scanJob(d.q.QueryRow(`SELECT `+jobCols+` FROM sandbox_jobs WHERE root_id=? AND job=?`, root, n))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no job %d in this conversation — sandbox_info lists its jobs", n)
	}
	return j, err
}

// jobByCall is the job a bash call started (nil: none).
func (d *DB) jobByCall(run int64, call string) *sbxJob {
	if call == "" {
		return nil
	}
	j, err := scanJob(d.q.QueryRow(`SELECT `+jobCols+` FROM sandbox_jobs WHERE run_id=? AND tool_call_id=? ORDER BY job DESC LIMIT 1`, run, call))
	if err != nil {
		return nil
	}
	return j
}

// jobList is a conversation's jobs, newest first.
func (d *DB) jobList(root int64, onlyRunning bool, limit int) []*sbxJob {
	q := `SELECT ` + jobCols + ` FROM sandbox_jobs WHERE root_id=?`
	if onlyRunning {
		q += ` AND state IN ('starting','running')`
	}
	rows, err := d.q.Query(q+` ORDER BY job DESC LIMIT ?`, root, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*sbxJob
	for rows.Next() {
		if j, err := scanJob(rows); err == nil {
			out = append(out, j)
		}
	}
	return out
}

func (d *DB) newJob(j *sbxJob) error {
	j.State = "starting"
	return d.q.QueryRow(`INSERT INTO sandbox_jobs (root_id, job, run_id, tool_call_id, ref, command, cwd, state, fg, created_ms)
		SELECT ?1, COALESCE(MAX(job), 0) + 1, ?2, ?3, ?4, ?5, ?6, 'starting', ?7, ?8 FROM sandbox_jobs WHERE root_id=?1
		RETURNING job`, j.Root, j.Run, j.Call, j.Ref, j.Command, j.Cwd, b2i(j.FG), j.Created).Scan(&j.Job)
}

func (d *DB) dropJob(j *sbxJob) {
	_, _ = d.q.Exec(`DELETE FROM sandbox_jobs WHERE root_id=? AND job=?`, j.Root, j.Job)
}

func (d *DB) jobStarted(j *sbxJob, exec string) {
	j.Exec = exec
	if j.State == "starting" {
		j.State = "running"
	}
	_, _ = d.q.Exec(`UPDATE sandbox_jobs SET exec_id=?, state=CASE WHEN state='starting' THEN 'running' ELSE state END
		WHERE root_id=? AND job=?`, exec, j.Root, j.Job)
}

func (d *DB) jobRead(j *sbxJob, off int64) {
	j.ReadOff = off
	_, _ = d.q.Exec(`UPDATE sandbox_jobs SET read_off=? WHERE root_id=? AND job=?`, off, j.Root, j.Job)
}

func (d *DB) jobBackground(j *sbxJob) {
	j.FG = false
	_, _ = d.q.Exec(`UPDATE sandbox_jobs SET fg=0 WHERE root_id=? AND job=?`, j.Root, j.Job)
}

// jobEnded records how a job ended (once: the first word stands).
func (d *DB) jobEnded(j *sbxJob, state string, exit *int) {
	if !j.running() || state == "running" || state == "starting" {
		return
	}
	j.State, j.Exit, j.Ended = state, exit, nowMs()
	var code any
	if exit != nil {
		code = *exit
	}
	_, _ = d.q.Exec(`UPDATE sandbox_jobs SET state=?, exit_code=?, ended_ms=? WHERE root_id=? AND job=? AND state IN ('starting','running')`,
		state, code, j.Ended, j.Root, j.Job)
}

// lostResultText answers a call a restart cut off. A bash command went on in
// the sandbox as a job, so its answer says which and how to pick it up; any
// other tool gets the generic words.
func (d *DB) lostResultText(runID int64, call toolCall) string {
	if call.Function.Name != "bash" {
		return toolLostToRestart
	}
	j := d.jobByCall(runID, call.ID)
	if j == nil {
		return toolLostToRestart
	}
	d.jobBackground(j)
	return fmt.Sprintf("(no result: the backend restarted while this command ran. It went on in the sandbox as job %d — "+
		"bash_output {\"job\": %d} shows its output from the start and whether it has finished; bash_kill {\"job\": %d} stops it.)",
		j.Job, j.Job, j.Job)
}

// --- bash ----------------------------------------------------------------------

func (ag *Agent) toolBash(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	command := str(args["command"])
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("bash needs a command")
	}
	root := rootOf(run)
	use, err := ag.sandboxUse(ctx, root, cfg, "")
	if err != nil {
		return "", err
	}
	cwd, err := use.path(str(args["cwd"]))
	if err != nil {
		return "", err
	}
	wait := toInt(args["timeout_s"])
	if wait <= 0 {
		wait = bashDefaultWait
	}
	wait = min(wait, bashMaxWait)
	bg := args["background"] == true

	j := ag.db.jobByCall(run.ID, toolCallOf(ctx))
	if j != nil && j.Ref != use.Binding.Ref {
		j = nil // the conversation moved to another sandbox since
	}
	if j == nil {
		if err := ag.jobRoom(ctx, root, cfg); err != nil {
			return "", err
		}
		j = &sbxJob{Root: root, Run: run.ID, Call: toolCallOf(ctx), Ref: use.Binding.Ref, Command: command, Cwd: cwd, FG: !bg, Created: nowMs()}
		if err := ag.db.newJob(j); err != nil {
			return "", err
		}
	}
	ex, err := use.Conn.ExecStart(ctx, use.ID, sbxExecReq{Cmd: j.Command, Cwd: j.Cwd, Env: bashEnv,
		Label: fmt.Sprintf("agent · job %d", j.Job), ClientID: j.clientID()})
	if err != nil {
		switch {
		case ctx.Err() == nil:
			ag.db.dropJob(j) // it never started
			return "", err
		case context.Cause(ctx) == errHandoff:
			return "", context.Cause(ctx) // it may have started: the successor finds it by its clientId
		}
		// stopped while starting: it may have started all the same
		go ag.stopStarting(use, j, killGrace)
		return "", context.Cause(ctx)
	}
	ag.db.jobStarted(j, ex.ID)
	if bg {
		ag.db.jobBackground(j)
		return fmt.Sprintf("started job %d in %s: %s\n[bash_output {\"job\": %d} reads its output · bash_kill {\"job\": %d} stops it]",
			j.Job, j.Cwd, clip(j.Command, 200), j.Job, j.Job), nil
	}
	start := time.Now()
	r, err := readJob(ctx, use, j, 0, toolDeadline(ctx, time.Duration(wait)*time.Second))
	if err != nil {
		if ctx.Err() != nil {
			return "", ag.bashStopped(ctx, use, j)
		}
		if gone(err) {
			ag.db.jobEnded(j, "lost", nil)
			return withFooter(r.out.String(), fmt.Sprintf("lost · job %d — the sandbox no longer has this command (it restarted?)", j.Job)), nil
		}
		return "", err
	}
	ag.db.jobRead(j, r.since)
	took := fmtDur(time.Since(start))
	if r.state == "running" {
		ag.db.jobBackground(j)
		return withFooter(r.out.String(), fmt.Sprintf("still running after %s · job %d — bash_output {\"job\": %d} follows it, bash_kill {\"job\": %d} stops it",
			took, j.Job, j.Job, j.Job)), nil
	}
	ag.db.jobEnded(j, r.state, r.exit)
	return withFooter(r.out.String(), fmt.Sprintf("%s · %s · job %d", endWords(r.state, r.exit, r.signal), took, j.Job)), nil
}

// bashStopped: the turn stopped while bash followed its command. A handoff
// leaves it running (the successor's answer names the job); an interrupt or
// a cancel stops its process group — TERM now, KILL if it outlives the grace
// (without holding the turn up for it).
func (ag *Agent) bashStopped(ctx context.Context, use *sbxUse, j *sbxJob) error {
	cause := context.Cause(ctx)
	switch {
	case cause == errHandoff:
		return cause
	case errors.Is(cause, context.DeadlineExceeded):
		ag.db.jobBackground(j) // the tool's own timeout: it goes on as a job
		return cause
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*killGrace+sbxCallTimeout)
	if err := use.Conn.ExecSignal(sctx, use.ID, j.Exec, "TERM", true); err != nil {
		cancel()
		logf("job %d of #%d: TERM after an interrupt: %v", j.Job, j.Root, err)
		return cause
	}
	go func(grace time.Duration) {
		defer cancel()
		ex, err := awaitEnd(sctx, use, j.Exec, grace)
		if err == nil && ex.State == "running" {
			if err = use.Conn.ExecSignal(sctx, use.ID, j.Exec, "KILL", true); err == nil {
				ex, err = awaitEnd(sctx, use, j.Exec, grace)
			}
		}
		if err == nil && ex.State != "running" {
			ag.db.jobEnded(j, ex.State, ex.ExitCode)
		}
	}(killGrace)
	return cause
}

// stopStarting stops a command whose start was cut short, if it started.
func (ag *Agent) stopStarting(use *sbxUse, j *sbxJob, grace time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*grace+2*sbxCallTimeout)
	defer cancel()
	if err := ag.resolveExec(ctx, use, j); err != nil || j.Exec == "" {
		return
	}
	_, _ = ag.stopJob(ctx, use, j, "", grace)
}

// gone: the manager no longer has the exec.
func gone(err error) bool {
	r := sbxRefusal(err)
	return r == "lost" || r == "not-found"
}

// --- reading output ------------------------------------------------------------------

// jobOut is what reading a job's output found.
type jobOut struct {
	out    *shaper
	since  int64 // read up to here
	state  string
	exit   *int
	signal string
}

// readJob reads a job's output from since until it has caught up with a
// command that ended, or until the deadline (then it catches up without
// waiting, skipping what the tail can't hold).
func readJob(ctx context.Context, use *sbxUse, j *sbxJob, since int64, until time.Time) (*jobOut, error) {
	r := &jobOut{out: newShaper(), since: since, state: "running"}
	late := 0
	for {
		waitMs := 0
		if d := time.Until(until); d > 0 {
			waitMs = int(min(d, outputPoll) / time.Millisecond)
		}
		ch, err := use.Conn.ExecOutput(ctx, use.ID, j.Exec, r.since, outputChunk, waitMs, true)
		if err != nil {
			return r, err
		}
		data, derr := base64.StdEncoding.DecodeString(ch.Data)
		if derr != nil || ch.Encoding == "text" {
			data = []byte(ch.Data) // a manager that answered in text anyway
		}
		if ch.Start > r.since {
			r.out.skip(ch.Start - r.since) // the ring dropped them
		}
		r.out.write(data)
		r.since = max(r.since, ch.End)
		r.state, r.exit, r.signal = ch.State, ch.ExitCode, ch.Signal
		caught := r.since >= ch.Total
		if caught && ch.State != "running" {
			return r, nil
		}
		if !time.Now().Before(until) {
			if caught || late >= 4 {
				return r, nil
			}
			late++
		}
		if r.out.headFull() && ch.Total-r.since > int64(r.out.tailMax) {
			jump := ch.Total - int64(r.out.tailMax)
			r.out.skip(jump - r.since)
			r.since = jump
		}
	}
}

// awaitEnd waits up to d for an exec to end, and says where it stands.
func awaitEnd(ctx context.Context, use *sbxUse, eid string, d time.Duration) (*sbxExec, error) {
	until := time.Now().Add(d)
	for {
		ex, err := use.Conn.ExecGet(ctx, use.ID, eid)
		if err != nil || ex.State != "running" || !time.Now().Before(until) {
			return ex, err
		}
		// its stream moves or closes, or the wait runs out
		if _, err := use.Conn.ExecOutput(ctx, use.ID, eid, ex.Total, 1, int(time.Until(until)/time.Millisecond), false); err != nil {
			return ex, err
		}
	}
}

// stopJob signals a job's process group: signal alone when named, else TERM
// and — if it hasn't ended within grace — KILL. It says where the exec
// stands afterwards.
func (ag *Agent) stopJob(ctx context.Context, use *sbxUse, j *sbxJob, signal string, grace time.Duration) (*sbxExec, error) {
	sig := signal
	if sig == "" {
		sig = "TERM"
	}
	if err := use.Conn.ExecSignal(ctx, use.ID, j.Exec, sig, true); err != nil {
		return nil, err
	}
	ex, err := awaitEnd(ctx, use, j.Exec, grace)
	if err == nil && ex.State == "running" && signal == "" {
		if err = use.Conn.ExecSignal(ctx, use.ID, j.Exec, "KILL", true); err == nil {
			ex, err = awaitEnd(ctx, use, j.Exec, grace)
		}
	}
	if err == nil && ex.State != "running" {
		ag.db.jobEnded(j, ex.State, ex.ExitCode)
	}
	return ex, err
}

// resolveExec finds the exec of a job whose start a restart cut off, by its
// clientId; a job that never started is lost.
func (ag *Agent) resolveExec(ctx context.Context, use *sbxUse, j *sbxJob) error {
	if j.Exec != "" {
		return nil
	}
	execs, err := use.Conn.ExecList(ctx, use.ID)
	if err != nil {
		return err
	}
	for _, ex := range execs {
		if ex.ClientID == j.clientID() {
			ag.db.jobStarted(j, ex.ID)
			return nil
		}
	}
	if time.Since(time.UnixMilli(j.Created)) > sbxCallTimeout {
		ag.db.jobEnded(j, "lost", nil)
	}
	return nil
}

// --- bash_output, bash_kill -------------------------------------------------------

// jobUse is a job and the sandbox it runs in, checked as every tool call is.
func (ag *Agent) jobUse(ctx context.Context, run *Run, cfg Config, args map[string]any) (*sbxJob, *sbxUse, error) {
	root := rootOf(run)
	j, err := ag.db.job(root, toInt(args["job"]))
	if err != nil {
		return nil, nil, err
	}
	use, err := ag.sandboxUse(ctx, root, cfg, j.Ref)
	if err != nil {
		return nil, nil, fmt.Errorf("job %d ran in %s: %w", j.Job, j.Ref, err)
	}
	if err := ag.resolveExec(ctx, use, j); err != nil {
		return nil, nil, err
	}
	return j, use, nil
}

func (ag *Agent) toolBashOutput(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	j, use, err := ag.jobUse(ctx, run, cfg, args)
	if err != nil {
		return "", err
	}
	if j.Exec == "" {
		if j.State == "lost" {
			return fmt.Sprintf("job %d never started (the backend restarted before it did) — run it again with bash", j.Job), nil
		}
		return fmt.Sprintf("job %d is still starting — ask again in a moment", j.Job), nil
	}
	since := j.ReadOff
	if v, ok := args["offset"]; ok && v != nil {
		since = int64(max(0, toInt(v)))
	}
	wait := min(max(toInt(args["wait_s"]), 0), jobWaitMax)
	r, err := readJob(ctx, use, j, since, toolDeadline(ctx, time.Duration(wait)*time.Second))
	if err != nil {
		if gone(err) {
			ag.db.jobEnded(j, "lost", nil)
			return fmt.Sprintf("job %d is gone — its sandbox restarted, or its manager no longer keeps it", j.Job), nil
		}
		return "", err
	}
	ag.db.jobRead(j, r.since)
	out := r.out.String()
	if r.out.empty() {
		out = "(no new output)"
	}
	if r.state == "running" {
		return withFooter(out, fmt.Sprintf("running · %s so far · job %d · read to byte %d",
			fmtDur(time.Since(time.UnixMilli(j.Created))), j.Job, r.since)), nil
	}
	ag.db.jobEnded(j, r.state, r.exit)
	return withFooter(out, fmt.Sprintf("%s · %s · job %d", endWords(r.state, r.exit, r.signal),
		fmtDur(time.Duration(j.Ended-j.Created)*time.Millisecond), j.Job)), nil
}

func (ag *Agent) toolBashKill(ctx context.Context, run *Run, cfg Config, args map[string]any) (string, error) {
	sig := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(str(args["signal"]))), "SIG")
	switch sig {
	case "", "INT", "TERM", "KILL", "HUP":
	default:
		return "", fmt.Errorf("signal is INT, TERM, KILL or HUP")
	}
	j, use, err := ag.jobUse(ctx, run, cfg, args)
	if err != nil {
		return "", err
	}
	if !j.running() || j.Exec == "" {
		return fmt.Sprintf("job %d isn't running (%s)", j.Job, endWords(j.State, j.Exit, "")), nil
	}
	ex, err := ag.stopJob(ctx, use, j, sig, killGrace)
	switch {
	case gone(err):
		ag.db.jobEnded(j, "lost", nil)
		return fmt.Sprintf("job %d is gone — its sandbox restarted, or its manager no longer keeps it", j.Job), nil
	case err != nil:
		return "", err
	case ex.State == "running":
		return fmt.Sprintf("sent %s to job %d; it is still running — bash_kill {\"job\": %d, \"signal\": \"KILL\"} forces it", orStr(sig, "TERM, then KILL,"), j.Job, j.Job), nil
	}
	return fmt.Sprintf("job %d stopped (%s)", j.Job, endWords(ex.State, ex.ExitCode, ex.Signal)), nil
}

// jobRoom refuses a new command while the conversation already runs its
// limit — after asking the manager about the ones the table still thinks
// are running.
func (ag *Agent) jobRoom(ctx context.Context, root int64, cfg Config) error {
	running := ag.db.jobList(root, true, maxRunningJobs+1)
	if len(running) < maxRunningJobs {
		return nil
	}
	ag.refreshJobs(ctx, root, cfg, running)
	var still []string
	for _, j := range running {
		if j.running() {
			still = append(still, fmt.Sprint(j.Job))
		}
	}
	if len(still) < maxRunningJobs {
		return nil
	}
	return fmt.Errorf("%d commands are already running in this conversation (jobs %s) — wait for one (bash_output) or stop one (bash_kill) first",
		len(still), strings.Join(still, ", "))
}

// refreshJobs asks the managers where running jobs stand.
func (ag *Agent) refreshJobs(ctx context.Context, root int64, cfg Config, jobs []*sbxJob) {
	uses := map[string]*sbxUse{}
	for _, j := range jobs {
		if !j.running() {
			continue
		}
		use, ok := uses[j.Ref]
		if !ok {
			use, _ = ag.sandboxUse(ctx, root, cfg, j.Ref)
			uses[j.Ref] = use
		}
		if use == nil || ag.resolveExec(ctx, use, j) != nil || j.Exec == "" {
			continue
		}
		ex, err := use.Conn.ExecGet(ctx, use.ID, j.Exec)
		switch {
		case gone(err):
			ag.db.jobEnded(j, "lost", nil)
		case err == nil:
			ag.db.jobEnded(j, ex.State, ex.ExitCode)
		}
	}
}

// --- a deleted conversation's jobs ----------------------------------------------------

// killJobsTimeout bounds stopping what a deleted conversation left running.
var killJobsTimeout = 30 * time.Second

// leftJob is a job a delete leaves running, and the person its kill is sent
// for (who bound its sandbox; "" = the tile itself).
type leftJob struct {
	job  *sbxJob
	user string
}

// jobsLeftBy is what deleting runs would leave running in sandboxes: the
// running jobs of those that are conversations (jobs are numbered per root).
func (ag *Agent) jobsLeftBy(ids []int64) []leftJob {
	var out []leftJob
	for _, id := range ids {
		jobs := ag.db.jobList(id, true, 1000)
		if len(jobs) == 0 {
			continue
		}
		cfg, _ := ag.db.runConfig(id)
		for _, j := range jobs {
			user := ""
			if b, ok := cfg.sandboxBinding(j.Ref); ok {
				user = sbxUserOf(binderWho(b.By))
			}
			out = append(out, leftJob{j, user})
		}
	}
	return out
}

// killJobs ends the commands a deleted conversation left running: KILL to
// each one's process group, best effort and bounded — it runs after the
// delete has answered, and nothing is left to record how they ended.
// (Archiving a conversation leaves them running.)
func killJobs(jobs []leftJob) {
	ctx, cancel := context.WithTimeout(context.Background(), killJobsTimeout)
	defer cancel()
	for _, l := range jobs {
		j := l.job
		conn, id, err := sbxDialRef(j.Ref, l.user)
		if err != nil {
			continue
		}
		eid := j.Exec
		if eid == "" { // its start was cut short: find it by its clientId
			execs, err := conn.ExecList(ctx, id)
			if err != nil {
				logf("job %d of deleted #%d: %v", j.Job, j.Root, err)
				continue
			}
			for _, ex := range execs {
				if ex.ClientID == j.clientID() && ex.State == "running" {
					eid = ex.ID
				}
			}
			if eid == "" {
				continue
			}
		}
		if err := conn.ExecSignal(ctx, id, eid, "KILL", true); err != nil && !gone(err) {
			logf("job %d of deleted #%d: KILL: %v", j.Job, j.Root, err)
		}
	}
}
