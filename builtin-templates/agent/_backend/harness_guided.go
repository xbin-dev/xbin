// harness_guided.go — the guided sign-in (D179; API.md "Coding agents" →
// "Signing in"): POST /runs/{id}/harness/authenticate {method: "guided"}
// runs the provider's own sign-in CLI (acp.Provider.Signin: Claude Code's
// `claude auth login --claudeai`) as a contract exec in the run's sandbox
// and reads what it prints with acp.Signin.Scan — the page to open and
// whether it asks for the code — so a person signs in with a link and a
// pasted code, never seeing a terminal:
//
//   - {method: "guided"} starts it (stdin open; a TTY one at 1000 columns,
//     so nothing wraps) and answers 202 {signin: {url, paste}} — to the
//     person who started it only: anyone else's ask is 409 while it waits;
//     theirs again answers it again;
//   - {method: "guided", code} writes the code and Enter (a newline over
//     pipes, CR on a terminal) and waits for the CLI's word: signed in (its
//     done line, or exit 0) → the run's Retry (an inbox wake: the adapter
//     starts again and reads the new sign-in, the held prompt resent);
//     "Invalid code" → 409, the CLI asking again; any other end → 502 with
//     the CLI's reason line;
//   - {remember: true, name?} runs the provider's Mint instead (`claude
//     setup-token`): the token it prints is scraped here and kept as the
//     person's saved sign-in (harness_creds.go) — it never reaches the page
//     — in a sandbox of their own that no one else uses only (the exec's
//     output is readable by everyone who may use the sandbox while it
//     lives), in their own partition only; the gate checked again before
//     the code goes in. The Mint runs clean: the image's CLI, an empty
//     environment, a throwaway HOME (mintArgv) — and its errors never show
//     the CLI's last line (a token in a format the scan doesn't know).
//
// The exec is always deleted once the sign-in is over (signed in, refused,
// ended, its sandbox shared through the agent); one per conversation,
// bounded at 15 minutes (the manager's own timeout too), dropped on a
// handoff (the successor knows none: the person starts over). It is
// recorded before it starts (harness_signin_execs) until its delete is
// done — retried, and swept by the next start.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
	"github.com/xbin-dev/xbin/sdk/acp"
)

const (
	gsFor      = 15 * time.Minute // a guided sign-in waits for its person at most this
	gsStartFor = 30 * time.Second // the CLI prints its link within this
	gsCodeFor  = 60 * time.Second // and answers a code within this
	gsOutMax   = 256 << 10        // what is kept of its output (the tail)
	gsCols     = 1000             // a TTY sign-in's width: one line per URL, per token
)

// hGuided is a guided sign-in under way on a run (this process's).
type hGuided struct {
	run    int64
	by     string // who started it: the only one who drives it
	mint   bool   // Remember: the provider's Mint, its token kept
	name   string // the saved sign-in's name (mint)
	prov   acp.Provider
	spec   acp.Signin
	conn   *sbxConn
	ref    string // the sandbox (provider:id)
	box    string // the sandbox's id at its manager
	exec   string
	client string // the exec's clientId (harness_signin_execs)

	mu     sync.Mutex // one request at a time drives it; the rest below under it
	off    int64
	out    []byte
	st     acp.SigninState
	state  string // the exec's: running | exited | killed | lost
	exit   *int
	timer  *time.Timer
	closed bool // over: the exec deleted (or being)
}

// guidedOf is run's guided sign-in under way here (nil: none).
func (e *Engine) guidedOf(run int64) *hGuided {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.guided[run]
}

// endGuided ends g: the run's slot freed, its exec deleted — retried in the
// background when the manager doesn't answer (its output may hold a minted
// token), and recorded until it is (harness_signin_execs: a later start
// sweeps what is left; the review's L6).
func (e *Engine) endGuided(g *hGuided) {
	if g.close(e) {
		_ = deleteSigninExec(g.conn, g.client, g.box, g.exec, true)
	}
}

// endGuidedNow is endGuided waiting for the delete (a share: no exec of a
// sign-in left in a sandbox others may read) — its error.
func (e *Engine) endGuidedNow(g *hGuided) error {
	if g.close(e) {
		return deleteSigninExec(g.conn, g.client, g.box, g.exec, true)
	}
	return nil
}

// close frees g's slot and drops what it read (false: closed already).
func (g *hGuided) close(e *Engine) bool {
	e.mu.Lock()
	if e.guided[g.run] == g {
		delete(e.guided, g.run)
		e.updateHoldLocked() // a person's partition: no longer held up for it
	}
	e.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return false
	}
	g.closed = true
	if g.timer != nil {
		g.timer.Stop()
	}
	g.out, g.st.Token = nil, "" // nothing of it outlives the sign-in
	return true
}

// signinExecRetry is how long the retries of a failed delete wait between
// tries (doubling, six of them; then the next start's sweep).
var signinExecRetry = time.Second

// noteSigninExec records a sign-in's exec before it starts (client: its
// clientId; the manager's id once known) — until it is deleted.
func noteSigninExec(client, ref, exec, user string) {
	if agent == nil || agent.db == nil {
		return
	}
	if _, err := agent.db.q.Exec(`INSERT INTO harness_signin_execs (client, ref, exec, user, created_ms) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(client) DO UPDATE SET exec=excluded.exec`, client, ref, exec, user, nowMs()); err != nil {
		logf("recording a sign-in's exec in %s: %v", ref, err)
	}
}

// deleteSigninExec deletes a sign-in's exec (exec "": the one of clientId
// client, if the manager started it) and forgets its record — retrying in
// the background, when retry, if the manager didn't take it. The first
// try's error.
func deleteSigninExec(conn *sbxConn, client, box, exec string, retry bool) error {
	try := func() error {
		ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
		defer cancel()
		ids := []string{exec}
		if exec == "" { // it may have started: the manager knows it by its clientId
			all, err := conn.ExecList(ctx, box)
			if err != nil && sbxRefusal(err) != "not-found" {
				return err
			}
			ids = nil
			for _, x := range all {
				if x.ClientID == client {
					ids = append(ids, x.ID)
				}
			}
		}
		for _, id := range ids {
			if err := conn.ExecDelete(ctx, box, id); err != nil {
				if r := sbxRefusal(err); r != "not-found" && r != "lost" {
					return err
				}
			}
		}
		if agent != nil && agent.db != nil {
			_, _ = agent.db.q.Exec(`DELETE FROM harness_signin_execs WHERE client=?`, client)
		}
		return nil
	}
	err := try()
	if err != nil && retry {
		logf("ending a sign-in (%s): %v — trying again", client, err)
		go func() {
			d := signinExecRetry
			for i := 0; i < 6; i++ {
				time.Sleep(d)
				if try() == nil {
					return
				}
				d *= 2
			}
			logf("ending a sign-in (%s): gave up — the next start sweeps it", client)
		}()
	}
	return err
}

// sweepSigninExecs deletes the sign-in execs an earlier process left (a
// crash, a manager that was down, a request gone before its exec started):
// as their person, at the engine's start (takeOver) — those recorded before
// it (before ms), never one this process is running.
func (e *Engine) sweepSigninExecs(before int64) {
	rows, err := e.db.q.Query(`SELECT client, ref, exec, user FROM harness_signin_execs WHERE created_ms < ?`, before)
	if err != nil {
		return
	}
	type left struct{ client, ref, exec, user string }
	var all []left
	for rows.Next() {
		var l left
		if rows.Scan(&l.client, &l.ref, &l.exec, &l.user) == nil {
			all = append(all, l)
		}
	}
	rows.Close()
	for _, l := range all {
		provider, id, ok := splitSandboxRef(l.ref)
		if !ok {
			_, _ = e.db.q.Exec(`DELETE FROM harness_signin_execs WHERE client=?`, l.client)
			continue
		}
		conn, err := sbxDial(provider, l.user)
		if err != nil {
			logf("sweeping a sign-in's exec (%s): %v", l.client, err)
			continue
		}
		if err := deleteSigninExec(conn, l.client, id, l.exec, false); err != nil {
			logf("sweeping a sign-in's exec (%s): %v", l.client, err)
		}
	}
}

// endGuidedIn ends every guided sign-in under way in sandbox ref, waiting
// for each exec's delete (a share: the error of the first that failed).
func (e *Engine) endGuidedIn(ref string) error {
	e.mu.Lock()
	var in []*hGuided
	for _, g := range e.guided {
		if g.ref == ref {
			in = append(in, g)
		}
	}
	e.mu.Unlock()
	var first error
	for _, g := range in {
		if err := e.endGuidedNow(g); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// dropGuided ends every guided sign-in here (a handoff: the successor
// knows none of them).
func (e *Engine) dropGuided() {
	e.mu.Lock()
	all := make([]*hGuided, 0, len(e.guided))
	for _, g := range e.guided {
		all = append(all, g)
	}
	e.mu.Unlock()
	for _, g := range all {
		e.endGuided(g)
	}
}

// gsAnswer is a guided sign-in's answer: the link and whether the CLI asks
// for the code (202), or signed in.
type gsAnswer struct {
	URL   string
	Paste bool
	Done  bool
}

// guidedSpec is the spec a guided sign-in of prov runs (mint: Remember).
func guidedSpec(prov acp.Provider, mint bool) (acp.Signin, string) {
	switch {
	case mint && prov.Mint != nil:
		return *prov.Mint, ""
	case mint:
		return acp.Signin{}, prov.Name + " can't make a sign-in to remember — paste a key or token in Coding-agent sign-ins instead"
	case prov.Signin != nil:
		return *prov.Signin, ""
	}
	return acp.Signin{}, prov.Name + " has no guided sign-in — use a terminal"
}

// guidedStart starts run's guided sign-in in its sandbox for by (or hands
// back the one by started): conn and box are the sandbox as the route
// found it for the person.
func (e *Engine) guidedStart(ctx context.Context, run *Run, prov acp.Provider, conn *sbxConn, box *sbxSandbox, cwd, by string,
	mint bool, name string) (*gsAnswer, error) {
	if g := e.guidedOf(run.ID); g != nil {
		if g.by != by || g.mint != mint {
			return nil, &hAuthErr{409, "a sign-in to " + prov.Name + " is already under way"}
		}
		g.mu.Lock()
		defer g.mu.Unlock()
		return &gsAnswer{URL: g.st.URL, Paste: g.st.Code}, nil
	}
	spec, why := guidedSpec(prov, mint)
	switch {
	case why != "":
		return nil, &hAuthErr{409, why}
	case !box.hasCap("exec"):
		return nil, &hAuthErr{409, sbxLabel(box) + " offers no commands — sign in in a terminal"}
	case spec.TTY && !box.hasCap("tty"):
		return nil, &hAuthErr{409, fmt.Sprintf("%s offers no terminals, which %s needs — paste a key or token instead", sbxLabel(box), spec.Command)}
	}
	req := sbxExecReq{Argv: spec.Argv, Cwd: cwd, Env: spec.Env, TTY: spec.TTY, Stdin: true, TimeoutMs: int(gsFor / time.Millisecond),
		Label: fmt.Sprintf("sign-in %s · #%d", prov.ID, rootOf(run)), ClientID: fmt.Sprintf("signin:%d:%d", run.ID, nowMs())}
	if mint { // the image's CLI, a clean environment, a throwaway HOME (the review's L12)
		req.Argv, req.Env = mintArgv(spec), nil
	}
	if spec.TTY {
		req.Rows, req.Cols = 50, gsCols
	}
	ref := sandboxRef(conn.M.Provider, box.ID)
	// recorded before it starts, and started on a context of its own: a
	// request gone mid-call never leaves an exec no one ends (N19, L6)
	noteSigninExec(req.ClientID, ref, "", conn.User)
	sctx, scancel := context.WithTimeout(context.Background(), sbxCallTimeout)
	ex, err := conn.ExecStart(sctx, box.ID, req)
	scancel()
	if err != nil {
		_ = deleteSigninExec(conn, req.ClientID, box.ID, "", true) // it may have started all the same
		return nil, &hAuthErr{502, fmt.Sprintf("couldn't start %s's sign-in in %s: %v", prov.Name, sbxLabel(box), err)}
	}
	noteSigninExec(req.ClientID, ref, ex.ID, conn.User)
	g := &hGuided{run: run.ID, by: by, mint: mint, name: name, prov: prov, spec: spec, conn: conn, ref: ref, box: box.ID,
		exec: ex.ID, client: req.ClientID, state: "running"}
	e.mu.Lock()
	if e.guided == nil {
		e.guided = map[int64]*hGuided{}
	}
	if e.closing || e.guided[run.ID] != nil {
		e.mu.Unlock()
		e.endGuided(g)
		return nil, &hAuthErr{409, "a sign-in to " + prov.Name + " is already under way"}
	}
	e.guided[run.ID] = g
	e.updateHoldLocked() // waiting for its person holds a person's partition up (harness_partition.go)
	e.mu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.timer = time.AfterFunc(gsFor, func() { e.endGuided(g) }) // the bound: never left waiting for nobody
	dl := time.Now().Add(gsStartFor)
	for g.st.URL == "" || (!g.st.Code && g.state == "running") {
		if g.state != "running" || time.Now().After(dl) || ctx.Err() != nil {
			break
		}
		if err := g.read(ctx, dl); err != nil {
			break
		}
	}
	if g.st.URL == "" {
		last := g.lastWords()
		go e.endGuided(g)
		if g.state != "running" {
			hint := ""
			if spec.Fallback != "" && !mint {
				hint = " — this CLI may be too old for it: sign in in a terminal (" + spec.Fallback + ")"
			}
			return nil, &hAuthErr{502, fmt.Sprintf("%s's sign-in ended without a link: %s%s", prov.Name, orStr(last, "it printed nothing"), hint)}
		}
		return nil, &hAuthErr{504, prov.Name + " didn't print its sign-in link"}
	}
	return &gsAnswer{URL: g.st.URL, Paste: g.st.Code}, nil
}

// lastWords is the CLI's last line as an error may show it (g.mu held): a
// mint's never — a token in a format the scan doesn't know would be that
// line (the review's L7) — and anything token-shaped masked.
func (g *hGuided) lastWords() string {
	if g.mint {
		return ""
	}
	return redactText(g.st.Last)
}

// failWords is the CLI's refusal line (its Fail marker's) as an error shows
// it: token-shaped strings masked.
func (g *hGuided) failWords() string {
	return redactText(g.st.Failed)
}

// mintPath is where a mint finds its CLI: the image's own directories (the
// rootfs's PATH), never the sandbox's — a shim in ~/.local/bin, ~/.bun/bin
// or a project's node_modules/.bin would see the token it prints. A
// variable for tests.
var mintPath = "/usr/local/go/bin:/usr/local/node/bin:/usr/local/bun/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// mintScript runs a mint clean ($1 the PATH, $2 the CLI, the rest its
// arguments): the CLI found on $1 only, run by its absolute path in an
// environment of PATH, HOME, TERM and LANG alone — no NODE_OPTIONS, no
// LD_PRELOAD, nothing a profile or the sandbox's environment set — and a
// throwaway HOME (none of the sandbox's settings, hooks or plugins), removed
// after. What it can't help: an image whose own directories were altered
// (API.md: a compromised image is out of scope).
const mintScript = `p=$1; shift
b=$(PATH=$p; command -v "$1") || { printf 'OAuth error: %%s is not installed in the image (%%s)\n' "$1" "$p"; exit 127; }
shift
h=$(mktemp -d) || exit 1
env -i PATH="$p" HOME="$h" TERM="${TERM:-xterm-256color}" LANG="${LANG:-C.UTF-8}" %s"$b" "$@"
c=$?
rm -rf -- "$h"
exit $c`

// mintArgv is spec's argv wrapped in mintScript (spec.Env passed through
// env -i, quoted).
func mintArgv(spec acp.Signin) []string {
	env := ""
	for _, k := range sortedKeys(spec.Env) {
		env += shQuote(k+"="+spec.Env[k]) + " "
	}
	return append([]string{"sh", "-c", fmt.Sprintf(mintScript, env), "mint", mintPath}, spec.Argv...)
}

// shQuote is s as one shell word.
func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// read takes the exec's output on (long-polling until more, its end, or
// dl) and reads it again (g.mu held).
func (g *hGuided) read(ctx context.Context, dl time.Time) error {
	wait := time.Until(dl)
	if wait <= 0 {
		return context.DeadlineExceeded
	}
	if wait > 25*time.Second {
		wait = 25 * time.Second
	}
	ch, err := g.conn.ExecOutput(ctx, g.box, g.exec, g.off, 64<<10, int(wait/time.Millisecond), true)
	if err != nil {
		return err
	}
	if b, err := base64.StdEncoding.DecodeString(ch.Data); err == nil && len(b) > 0 {
		g.out = append(g.out, b...)
		if len(g.out) > gsOutMax {
			g.out = append([]byte(nil), g.out[len(g.out)-gsOutMax:]...)
		}
	}
	g.off = ch.End
	g.state, g.exit = orStr(ch.State, g.state), ch.ExitCode
	g.st = g.spec.Scan(g.out)
	return nil
}

// guidedCode writes code to by's guided sign-in on run and waits for the
// CLI's word on it — on a context of its own, bounded by gsCodeFor: a
// request gone mid-way doesn't leave a minted token in the exec's output
// until the 15-minute bound (the review's L6); the caller still keeps it.
// ref is the conversation's sandbox now and gate credWhy for it as it is
// now: a mint whose sandbox was shared (or swapped) since it started ends
// here, before the code goes in (L5). The result names the sign-in (N17).
func (e *Engine) guidedCode(run *Run, by, code, ref, gate string) (*gsAnswer, *gsResult, error) {
	g := e.guidedOf(run.ID)
	if g == nil || g.by != by {
		return nil, nil, &hAuthErr{409, "no sign-in of yours is under way here — start one"}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, nil, &hAuthErr{409, "that sign-in is over — start one"}
	}
	if g.mint && (g.ref != ref || gate != "") {
		go e.endGuided(g)
		if g.ref != ref {
			gate = "this conversation's sandbox changed since it started"
		}
		return nil, nil, &hAuthErr{409, "that sign-in was ended: Remember works only in a sandbox of your own that no one else uses — " + gate}
	}
	ctx, cancel := context.WithTimeout(context.Background(), gsCodeFor+sbxSlack)
	defer cancel()
	invalid := g.st.Invalid
	enter := "\n"
	if g.spec.TTY {
		enter = "\r"
	}
	if err := g.conn.ExecStdin(ctx, g.box, g.exec, []byte(code+enter), false); err != nil {
		go e.endGuided(g)
		return nil, nil, &hAuthErr{502, fmt.Sprintf("couldn't hand %s the code: %v", g.prov.Name, err)}
	}
	dl := time.Now().Add(gsCodeFor)
	for {
		st := g.st
		switch {
		case g.mint && st.Token != "":
			res := &gsResult{token: st.Token, name: g.name}
			g.st.Token = ""
			go e.endGuided(g)
			return &gsAnswer{Done: true}, res, nil
		case !g.mint && st.Done:
			go e.endGuided(g)
			return &gsAnswer{Done: true}, &gsResult{}, nil
		case st.Invalid > invalid:
			return nil, nil, &hAuthErr{409, fmt.Sprintf("%s says that isn't the whole code — copy it again from the sign-in page and paste it", g.prov.Name)}
		case st.Failed != "":
			go e.endGuided(g)
			return nil, nil, &hAuthErr{502, g.failWords()}
		case g.state != "running":
			go e.endGuided(g)
			if !g.mint && g.exit != nil && *g.exit == 0 {
				return &gsAnswer{Done: true}, &gsResult{}, nil
			}
			what := "it ended"
			if g.mint {
				what = "it ended without a token"
			}
			return nil, nil, &hAuthErr{502, fmt.Sprintf("%s's sign-in didn't finish: %s (%s)", g.prov.Name, orStr(g.lastWords(), "no word from it"), what)}
		}
		if err := g.read(ctx, dl); err != nil {
			go e.endGuided(g)
			if errors.Is(err, context.DeadlineExceeded) {
				return nil, nil, &hAuthErr{504, g.prov.Name + " didn't answer the code"}
			}
			return nil, nil, &hAuthErr{502, fmt.Sprintf("reading %s's sign-in: %v", g.prov.Name, err)}
		}
	}
}

// guidedBody is POST …/authenticate's for method guided.
type guidedBody struct {
	code     string
	remember bool
	name     *string
	confirm  bool
}

// handleGuided is POST /runs/{id}/harness/authenticate {method: "guided"}
// (handleHarnessAuthenticate checked the instance, the caller and that the
// run waits for its sign-in):
//
//	{method: "guided", remember?, name?, confirm?} → 202 {ok, signin: {url, paste}}
//	{method: "guided", code} → 200 {ok, state: "ready", saved?: {…the saved sign-in, no secret}}
//	  | 409 (an invalid code: paste it again) | 502 {error: the CLI's reason} | 504
func handleGuided(w http.ResponseWriter, r *http.Request, run *Run, cfg Config, name string, b guidedBody, denied func(string) string) {
	c := callerOf(r)
	prov := harnessProvider(cfg.Harness.Provider, nil)
	if _, why := guidedSpec(prov, b.remember); why != "" {
		xbin.WriteError(w, http.StatusConflict, why)
		return
	}
	if b.remember {
		if why := signinsWhy(); why != "" {
			xbin.WriteError(w, http.StatusConflict, why)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	conn, _, box, err := personSandbox(ctx, c, cfg.Harness.Ref, func(b *sbxSandbox) string { return denied(sbxLabel(b)) })
	cancel()
	if err != nil {
		writeSbxErr(w, err)
		return
	}
	e := agent.eng
	ref := cfg.Harness.Ref
	gate := credWhy(run, ref, box)
	if b.code == "" { // start it (or hand back the link of the one this person started)
		switch {
		case b.remember && gate != "":
			xbin.WriteError(w, http.StatusConflict, "Remember works only in a sandbox of your own that no one else uses, "+
				"in your own conversation — "+gate)
			return
		case !b.remember && sandboxShared(box) && !b.confirm:
			xbin.WriteJSON(w, http.StatusConflict, map[string]any{"confirm": true,
				"error": fmt.Sprintf("anyone who may use %s acts as you with %s there — confirm to sign in", sbxLabel(box), name)})
			return
		}
		nm := ""
		if b.remember {
			if nm = pickName(b.name, c.user, prov.ID); nm == "" {
				xbin.WriteError(w, http.StatusBadRequest, fmt.Sprintf("name: up to %d characters, one line", signinNameMax))
				return
			}
		}
		cwd := orStr(cfg.Harness.Cwd, "")
		res, err := e.guidedStart(r.Context(), run, prov, conn, box, cwd, sbxUserOf(c), b.remember, nm)
		if writeGuidedErr(w, err, name) {
			return
		}
		xbin.WriteJSON(w, http.StatusAccepted, map[string]any{"ok": "true", "signin": map[string]any{"url": res.URL, "paste": res.Paste}})
		return
	}
	code := cleanCode(b.code)
	if code == "" {
		xbin.WriteError(w, http.StatusBadRequest, "code: the code the sign-in page showed")
		return
	}
	_, res, err := e.guidedCode(run, sbxUserOf(c), code, ref, gate)
	if writeGuidedErr(w, err, name) {
		return
	}
	out := map[string]any{"ok": "true", "state": "ready"}
	if res.token != "" { // Remember: the token, straight into the person's vault — never answered, logged or stored here
		key, _ := prov.KeyFor(res.token)
		s, err := agent.db.saveSignin(c.user, prov.ID, res.name, key, res.token, time.Now())
		res.token = ""
		if err != nil {
			xbin.WriteError(w, http.StatusBadGateway, err.Error())
			return
		}
		if s.replaced { // a sign-in of that name re-minted: what runs on the old token stops (N14)
			stopCredUsers(s, fmt.Sprintf("your saved sign-in %s was replaced — the next message starts it with the new one", s.Name), run.ID)
		}
		if !s.IsDefault { // this conversation picks it: its sandbox's own sign-in is still out
			_ = e.fenced(func(t *DB) error {
				cur, err := t.runConfig(run.ID)
				if err != nil || cur.Harness == nil {
					return err
				}
				cur.Harness.Signin = s.ID
				raw, _ := json.Marshal(cur)
				_, err = t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), run.ID)
				return err
			})
		}
		out["saved"] = s.view(time.Now())
	}
	// the retry: the adapter starts again and reads the new sign-in; the held prompt goes
	if _, _, err := agent.queue(run.ID, inboxWake, inboxBody{Reason: "signed in"}, ""); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, out)
}

// writeGuidedErr answers err (false: none).
func writeGuidedErr(w http.ResponseWriter, err error, name string) bool {
	var ae *hAuthErr
	switch {
	case err == nil:
		return false
	case errors.As(err, &ae):
		xbin.WriteError(w, ae.Code, ae.Msg)
	case errors.Is(err, context.Canceled):
		xbin.WriteError(w, http.StatusGatewayTimeout, "the request ended before "+name+" answered")
	default:
		xbin.WriteError(w, http.StatusGatewayTimeout, name+" didn't answer its sign-in")
	}
	return true
}

// gsResult is what a finished guided sign-in left: a minted token (a
// secret: kept in the vault at once, then dropped).
type gsResult struct{ token, name string }

// cleanCode is a pasted code as a CLI reads it: one line, nothing but what a
// code is made of (a paste may carry spaces, a newline, a control char).
func cleanCode(s string) string {
	return strings.Map(func(r rune) rune {
		if r <= ' ' || r == 0x7f {
			return -1
		}
		return r
	}, s)
}
