// scm_ensure.go — minting a project's credential and writing it into a
// sandbox (scmEnsureCreds; scm_creds.go has the state, scm_gate.go the
// gate). Each write re-reads the sandbox from its manager and re-checks the
// gate; a refusal blocks the credential, scrubs what was there and fails
// the task with the gate's words. A provider's 409 signin keeps its device
// code for the person who must sign in and parks the task on it (ws
// signin) until the creds job sees the sign-in finish (scm_jobs.go).
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ensureCreds makes sure ref ("" = k's sandbox, else p's) holds a live
// credential for p with at least minLeft to run (scmEnsureCreds). It calls
// the sandbox manager and the provider: the worker's, never the engine's.
func ensureCreds(ctx context.Context, p *Project, k *ProjectTask, ref string, minLeft time.Duration) error {
	if p == nil {
		return errors.New("no project")
	}
	if !scmActive(p) {
		return fmt.Errorf("project %s is %s: it takes no credentials", p.Name, p.State)
	}
	if ref == "" {
		ref = scmCredRef(p, k)
	}
	if ref == "" {
		return errSCMNoSandbox
	}
	repos := scmRepoNames(p.ID)
	if len(repos) == 0 {
		return nil // nothing to push to yet
	}
	conn, id, err := sbxDialRef(ref, scmSbxUser(p))
	if err != nil {
		return err
	}
	box, err := conn.Get(ctx, id)
	if err != nil {
		return err
	}
	as := scmProjectAs(p)
	host := p.Host
	key := scmLiveKey(p.ID, ref, host)
	mu := scmKeyLock(key)
	mu.Lock()
	defer mu.Unlock()
	if why := scmCredWhy(p, as, ref, box); why != "" {
		scmBlock(ctx, p, k, ref, box, why)
		return &scmGateError{Box: sbxLabel(box), Why: why}
	}
	req := scmTokenReq{Repos: repos, Access: "write", As: as, Purpose: scmPurpose(p, ref)}
	if scmPolicyOf(p).Workflows {
		req.Permissions = map[string]string{"workflows": "write"}
	}
	if l := scmLiveGet(key); l != nil && l.identity == as && l.scope == scmScopeOf(req) &&
		time.Until(time.UnixMilli(l.expires)) >= minLeft && scmRowLive(p.ID, ref, l.host) {
		return nil // a repo added, or workflows turned on, since: minted again
	}
	api, err := scmFor(p.SCM)
	if err != nil {
		return err
	}
	tok, err := api.Token(ctx, req)
	if err != nil {
		var se *scmError
		if errors.As(err, &se) && se.Refusal == scmRefSignin {
			scmNoteSignin(p.Owner, p.SCM, se.Signin)
			scmTaskWS(p, k, ref, wsSignin, "sign in to "+scmTitle(ctx, api)+" to let this task push", wsPending, wsQueued, wsPreparing, wsReady)
		}
		return err
	}
	scmMask(tok.Token, tok.ExpiresAt) // masked from here on, whatever happens next
	if host == "" {
		host = tok.Host
	}
	if tok.Host != host || !scmHostRe.MatchString(host) {
		return fmt.Errorf("%s handed out a credential for %q, not for the project's host %q", p.SCM, tok.Host, host)
	}
	if v := tok.Token.Reveal(); strings.ContainsAny(v, " \t\r\n\x00") || strings.ContainsAny(tok.Username, "\r\n\x00") {
		return fmt.Errorf("%s handed out a credential this agent can't write into a file", p.SCM)
	}
	key = scmLiveKey(p.ID, ref, host)
	live := &scmLive{token: tok.Token, host: host, purpose: req.Purpose, identity: as, login: tok.Identity.Login,
		author: tok.Author, expires: tok.ExpiresAt, refresh: tok.RefreshAfter, written: nowMs(), scope: scmScopeOf(req)}
	if as == scmAsPerson {
		live.forUser = p.Owner
	}
	if live.refresh <= 0 || live.refresh > live.expires {
		live.refresh = live.expires - scmMinLeft.Milliseconds()
	}
	scmLivePut(key, live) // before the files: scmGitConfig reads its author
	if err := scmWriteFiles(ctx, conn, id, box, p, ref, live, tok.Username); err != nil {
		scmLiveDrop(key) // not there: the next ensure mints again (it stays masked)
		return fmt.Errorf("writing the credential into %s: %w", sbxLabel(box), err)
	}
	if err := agent.db.scmPutCred(scmCredRow{PID: p.ID, Ref: ref, Host: host, Identity: as, Login: live.login, ForUser: live.forUser,
		Purpose: live.purpose, Expires: live.expires, Refresh: live.refresh, Written: live.written, State: credLive}); err != nil {
		return err
	}
	if as == scmAsPerson {
		scmClearSignin(p.Owner, p.SCM)
	}
	scmCredsReady(p, k, ref)
	return nil
}

// scmRowLive: project_creds still says live (another process, or a scrub,
// hasn't changed it since this process wrote it).
func scmRowLive(pid int64, ref, host string) bool {
	for _, c := range agent.db.scmCredsOf(pid, ref) {
		if c.Host == host {
			return c.State == credLive
		}
	}
	return false
}

// scmTitle is how people know a provider: its hello's title, else its tile.
func scmTitle(ctx context.Context, api scmAPI) string {
	if h, err := api.Hello(ctx); err == nil && h.SCM.Title != "" {
		return h.SCM.Title
	}
	return api.Provider()
}

// --- the files ------------------------------------------------------------------------

// yamlQ is s as a single-quoted YAML scalar.
func scmYAMLQ(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// scmWriteFiles writes l's two files into box (…tmp, then renamed, in 0700
// directories) and the git config into each of p's repos there; and
// P/.xbin/env for people's terminals.
func scmWriteFiles(ctx context.Context, conn *sbxConn, id string, box *sbxSandbox, p *Project, ref string, l *scmLive, username string) error {
	dir := scmCredDir(box.Home, p.UID)
	if dir == "" {
		return errors.New(scmWhyWords(whyHome))
	}
	username = orStr(username, "x-access-token")
	v := l.token.Reveal()
	files := []struct{ path, body string }{
		{dir + "/" + l.host + ".cred.tmp", "username=" + username + "\npassword=" + v + "\n"},
		{dir + "/gh/hosts.yml.tmp", l.host + ":\n    oauth_token: " + scmYAMLQ(v) + "\n    user: " + scmYAMLQ(l.login) + "\n    git_protocol: https\n"},
	}
	for _, f := range files {
		if _, err := conn.WriteFile(ctx, id, f.path, strings.NewReader(f.body), sbxWrite{Mode: "0600", Mkdirs: true}); err != nil {
			return err
		}
	}
	env := map[string]string{"X": strings.TrimRight(box.Home, "/") + "/.config/xbin-scm", "D": dir, "F": l.host + ".cred"}
	for i, b := range scmRepoDirs(p, ref) {
		env[fmt.Sprintf("GIT_DIR_%d", i)] = b
	}
	scmGitEnv(scmGitConfigOf(p, l.host, box.Home), env)
	script := "set -eu\numask 077\nchmod 700 \"$X\" \"$D\" \"$D/gh\"\nmv -f \"$D/$F.tmp\" \"$D/$F\"\nmv -f \"$D/gh/hosts.yml.tmp\" \"$D/gh/hosts.yml\"\n" + scmGitScript
	if err := scmRun(ctx, conn, id, script, env); err != nil {
		return err
	}
	if p.Dir != "" && scmHomeRe.MatchString(p.Dir) {
		body := "GH_CONFIG_DIR=" + dir + "/gh\n"
		if _, err := conn.WriteFile(ctx, id, strings.TrimRight(p.Dir, "/")+"/.xbin/env", strings.NewReader(body), sbxWrite{Mode: "0644", Mkdirs: true}); err != nil {
			logf("project #%d: writing .xbin/env in %s: %v", p.ID, ref, err)
		}
	}
	return nil
}

// scmEmptyFiles empties p's two files for host in box (0600, zero bytes).
func scmEmptyFiles(ctx context.Context, conn *sbxConn, id string, box *sbxSandbox, p *Project, host string) error {
	dir := scmCredDir(box.Home, p.UID)
	if dir == "" || !scmHostRe.MatchString(host) {
		return nil // nothing can have been written there
	}
	for _, f := range []string{dir + "/" + host + ".cred", dir + "/gh/hosts.yml"} {
		if _, err := conn.WriteFile(ctx, id, f, strings.NewReader(""), sbxWrite{Mode: "0600", Mkdirs: true}); err != nil {
			return err
		}
	}
	// a write whose rename never ran left its …tmp behind
	for _, f := range []string{dir + "/" + host + ".cred.tmp", dir + "/gh/hosts.yml.tmp"} {
		if err := conn.Remove(ctx, id, f, false); err != nil && sbxRefusal(err) != "not-found" {
			return err
		}
	}
	return nil
}

// scmRun runs script in the sandbox (values in env), refusing a non-zero
// exit with its redacted output.
func scmRun(ctx context.Context, conn *sbxConn, id, script string, env map[string]string) error {
	res, err := conn.Run(ctx, id, sbxRunReq{Argv: []string{"sh", "-c", script}, Env: env, TimeoutMs: 30000, Merge: true, MaxOutput: 8192})
	if err != nil {
		return err
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		out := ""
		if res.Output != nil {
			out = res.Output.Head + res.Output.Tail
		}
		return fmt.Errorf("the sandbox's script failed: %s", clip(strings.TrimSpace(redactText(out)), 400))
	}
	return nil
}

// --- refusals and the task's state ----------------------------------------------------

// scmBlock is the gate refusing ref for p: whatever p wrote there is
// emptied and revoked, the row says blocked with why, and the task (every
// task of p working there, for no k) fails with the gate's words.
func scmBlock(ctx context.Context, p *Project, k *ProjectTask, ref string, box *sbxSandbox, why string) {
	unemptied := false
	for _, c := range agent.db.scmCredsOf(p.ID, ref) {
		if c.State == credLive {
			if err := scmScrubRow(ctx, p, c, why, box); err != nil {
				logf("project #%d: %v", p.ID, err)
				unemptied = true // the row stays live: the next scrub tries again
			}
		}
	}
	host := p.Host
	if host == "" {
		for _, c := range agent.db.scmCredsOf(p.ID, ref) {
			host = c.Host
		}
	}
	if !unemptied {
		if err := agent.db.scmSetCredState(p.ID, ref, orStr(host, "-"), credBlocked, why); err != nil {
			logf("project #%d: marking its credential in %s blocked: %v", p.ID, ref, err)
		}
	}
	label := ref
	if box != nil {
		label = sbxLabel(box)
	}
	scmTaskWS(p, k, ref, wsFailed, (&scmGateError{Box: label, Why: why}).Error(), wsPending, wsQueued, wsPreparing, wsSignin, wsReady)
}

// scmTaskWS moves k (nil: every task of p working in ref) to ws with errText
// — only from one of the states from — and says so (projectTaskChanged).
// The tasks' table is the projects store's; without it nothing happens.
func scmTaskWS(p *Project, k *ProjectTask, ref, ws, errText string, from ...string) {
	ns := scmTasksIn(p, k, ref, from)
	if len(ns) == 0 {
		return
	}
	err := agent.db.Tx(func(t *DB) error {
		for _, n := range ns {
			res, err := t.q.Exec(`UPDATE project_tasks SET ws=?, error=?, updated_ms=? WHERE project_id=? AND n=? AND ws=?`,
				ws, errText, nowMs(), p.ID, n.n, n.ws)
			if err != nil {
				return err
			}
			if rowsAffected(res) > 0 {
				scmPriorWS(p.ID, n.n, n.ws, ws)
				projectTaskChanged(t, p.ID, n.n, "ws")
			}
		}
		return nil
	})
	if err != nil {
		logf("project #%d: a task's workspace state: %v", p.ID, err)
	}
}

type scmTaskState struct {
	n      int64
	ws     string
	run    int64
	errTxt string
}

// scmTasksIn is k's state (nil: each task of p working in ref) when it is
// one of from (any, when from is empty).
func scmTasksIn(p *Project, k *ProjectTask, ref string, from []string) []scmTaskState {
	if agent == nil || agent.db == nil {
		return nil
	}
	q := `SELECT n, ws, run_id, error FROM project_tasks WHERE project_id=? AND (sandbox_ref=? OR (sandbox_ref='' AND ?=?))`
	args := []any{p.ID, ref, ref, p.SandboxRef}
	if k != nil {
		q, args = `SELECT n, ws, run_id, error FROM project_tasks WHERE project_id=? AND n=?`, []any{p.ID, k.N}
	}
	rows, err := agent.db.q.Query(q, args...)
	if err != nil {
		return nil // no tasks table yet
	}
	defer rows.Close()
	var out []scmTaskState
	for rows.Next() {
		var s scmTaskState
		if rows.Scan(&s.n, &s.ws, &s.run, &s.errTxt) != nil {
			continue
		}
		if len(from) == 0 || strings.Contains(" "+strings.Join(from, " ")+" ", " "+s.ws+" ") {
			out = append(out, s)
		}
	}
	return out
}

// scmPriors: the state a task was in before the credentials parked it
// (signin) or failed it — what it goes back to once they are written
// (memory: after a restart, scmRestoreWS works it out).
var scmPriors = struct {
	m map[string]string
}{m: map[string]string{}}

func scmPriorWS(pid, n int64, from, to string) {
	key := fmt.Sprint(pid, "|", n)
	scmLiveMu.Lock()
	defer scmLiveMu.Unlock()
	switch {
	case to == wsSignin || to == wsFailed:
		if from != wsSignin && from != wsFailed {
			scmPriors.m[key] = from
		}
	default:
		delete(scmPriors.m, key)
	}
}

// scmRestoreWS is what a task the credentials held goes back to: what it
// was, else ready when its workspace was bound (a bind job finished and
// none of the preparing ones is live), else preparing.
func scmRestoreWS(pid, n int64) string {
	scmLiveMu.Lock()
	ws := scmPriors.m[fmt.Sprint(pid, "|", n)]
	scmLiveMu.Unlock()
	if ws != "" {
		return ws
	}
	var bound, live int
	err := agent.db.q.QueryRow(`SELECT
		COALESCE(SUM(j.kind='bind' AND j.state='done'), 0),
		COALESCE(SUM(j.kind IN ('sandbox','repo','fork','prepare','setup','bind') AND j.state IN ('queued','running','waiting')), 0)
		FROM project_jobs j JOIN project_tasks t ON j.task_id=t.id WHERE t.project_id=? AND t.n=?`, pid, n).Scan(&bound, &live)
	if err == nil && bound > 0 && live == 0 {
		return wsReady
	}
	return wsPreparing
}

// scmCredsReady: a credential was written for k's sandbox (each task of p
// there, for no k) — a task the credentials held (signin, or failed by the
// gate) goes back to what it was, and a run the workspace gate parked
// takes its turn now (the gate looks again).
func scmCredsReady(p *Project, k *ProjectTask, ref string) {
	for _, s := range scmTasksIn(p, k, ref, nil) {
		if s.ws == wsSignin || (s.ws == wsFailed && strings.HasPrefix(s.errTxt, "credentials can't go into")) {
			back := scmRestoreWS(p.ID, s.n)
			err := agent.db.Tx(func(t *DB) error {
				res, err := t.q.Exec(`UPDATE project_tasks SET ws=?, error='', updated_ms=? WHERE project_id=? AND n=? AND ws=?`, back, nowMs(), p.ID, s.n, s.ws)
				if err == nil && rowsAffected(res) > 0 {
					scmPriorWS(p.ID, s.n, s.ws, back)
					projectTaskChanged(t, p.ID, s.n, "ws")
				}
				return err
			})
			if err != nil {
				logf("project #%d: task %d's workspace state: %v", p.ID, s.n, err)
			}
		}
		if s.run != 0 {
			if r, err := agent.db.getRun(s.run); err == nil && parsePending(r.Pending).Kind == pendKindProject {
				if _, _, err := agent.queue(s.run, inboxWake, inboxBody{Reason: "credentials ready"}, ""); err != nil {
					logf("run #%d: waking it (credentials ready): %v", s.run, err)
				}
			}
		}
	}
}
