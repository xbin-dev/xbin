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
//     lives), in their own partition only.
//
// The exec is always deleted once the sign-in is over (signed in, refused,
// ended); one per conversation, bounded at 15 minutes (the manager's own
// timeout too), dropped on a handoff (the successor knows none: the person
// starts over).
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
	run  int64
	by   string // who started it: the only one who drives it
	mint bool   // Remember: the provider's Mint, its token kept
	name string // the saved sign-in's name (mint)
	prov acp.Provider
	spec acp.Signin
	conn *sbxConn
	box  string // the sandbox's id at its manager
	exec string

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

// endGuided ends g: its exec deleted, the run's slot freed.
func (e *Engine) endGuided(g *hGuided) {
	e.mu.Lock()
	if e.guided[g.run] == g {
		delete(e.guided, g.run)
	}
	e.mu.Unlock()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	if g.timer != nil {
		g.timer.Stop()
	}
	g.out, g.st.Token = nil, "" // nothing of it outlives the sign-in
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), sbxCallTimeout)
	defer cancel()
	if err := g.conn.ExecDelete(ctx, g.box, g.exec); err != nil {
		if r := sbxRefusal(err); r != "not-found" && r != "lost" {
			logf("run #%d: ending its sign-in (exec %s): %v", g.run, g.exec, err)
		}
	}
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
	if spec.TTY {
		req.Rows, req.Cols = 50, gsCols
	}
	ex, err := conn.ExecStart(ctx, box.ID, req)
	if err != nil {
		return nil, &hAuthErr{502, fmt.Sprintf("couldn't start %s's sign-in in %s: %v", prov.Name, sbxLabel(box), err)}
	}
	g := &hGuided{run: run.ID, by: by, mint: mint, name: name, prov: prov, spec: spec, conn: conn, box: box.ID, exec: ex.ID, state: "running"}
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
		last := g.st.Last
		go e.endGuided(g)
		if g.state != "running" {
			hint := ""
			if spec.Fallback != "" {
				hint = " — this CLI may be too old for it: sign in in a terminal (" + spec.Fallback + ")"
			}
			return nil, &hAuthErr{502, fmt.Sprintf("%s's sign-in ended without a link: %s%s", prov.Name, orStr(last, "it printed nothing"), hint)}
		}
		return nil, &hAuthErr{504, prov.Name + " didn't print its sign-in link"}
	}
	return &gsAnswer{URL: g.st.URL, Paste: g.st.Code}, nil
}

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
// CLI's word on it.
func (e *Engine) guidedCode(ctx context.Context, run *Run, by, code string) (*gsAnswer, *gsResult, error) {
	g := e.guidedOf(run.ID)
	if g == nil || g.by != by {
		return nil, nil, &hAuthErr{409, "no sign-in of yours is under way here — start one"}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return nil, nil, &hAuthErr{409, "that sign-in is over — start one"}
	}
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
			res := &gsResult{token: st.Token}
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
			return nil, nil, &hAuthErr{502, st.Failed}
		case g.state != "running":
			go e.endGuided(g)
			if !g.mint && g.exit != nil && *g.exit == 0 {
				return &gsAnswer{Done: true}, &gsResult{}, nil
			}
			what := "it ended"
			if g.mint {
				what = "it ended without a token"
			}
			return nil, nil, &hAuthErr{502, fmt.Sprintf("%s's sign-in didn't finish: %s (%s)", g.prov.Name, orStr(st.Last, "no word from it"), what)}
		}
		if err := g.read(ctx, dl); err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
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
	if b.code == "" { // start it (or hand back the link of the one this person started)
		switch {
		case b.remember && credWhy(run, box) != "":
			xbin.WriteError(w, http.StatusConflict, "Remember works only in a sandbox of your own that no one else uses, "+
				"in your own conversation — "+credWhy(run, box))
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
	g := e.guidedOf(run.ID)
	_, res, err := e.guidedCode(r.Context(), run, sbxUserOf(c), code)
	if writeGuidedErr(w, err, name) {
		return
	}
	out := map[string]any{"ok": "true", "state": "ready"}
	if res.token != "" { // Remember: the token, straight into the person's vault — never answered, logged or stored here
		key, _ := prov.KeyFor(res.token)
		s, err := agent.db.saveSignin(c.user, prov.ID, g.name, key, res.token, time.Now())
		res.token = ""
		if err != nil {
			xbin.WriteError(w, http.StatusBadGateway, err.Error())
			return
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
type gsResult struct{ token string }

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
