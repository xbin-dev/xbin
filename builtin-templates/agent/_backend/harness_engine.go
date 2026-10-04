// harness_engine.go — the coding agents this process drives (D147 §3.3):
// one adapter process per harness run in its sandbox (harness_pipe.go), an
// acp.Client (sdk/acp) over it, and ONE consumer goroutine per session that
// applies the client's events in order (harness_map.go). The pass (actor.go →
// harness_pass.go) decides what a run needs; the session is how it reaches
// the adapter.
//
//   - ensure (every prompt): the sandbox is still the conversation's to use
//     (sandboxUse: binder rights, class, taint) and reaches out (egress ≠
//     none), the harness is installed (a probe when not known), then a new
//     generation is spawned and started — session/load when the adapter can
//     reopen the conversation's earlier session.
//   - attach (after a handoff): the successor reads the running adapter's
//     output on from the offset its predecessor committed, rebuilding the
//     client from the stored snapshot without a handshake (ClientOptions.Attach)
//     — the prompt in flight still ends its turn, the pending permissions
//     can still be answered.
//   - consistency: a durable event (all but message and thought chunks and
//     terminal output deltas) commits its rows, read_off, err_off, the
//     snapshot and the unflushed draft in ONE fenced transaction; the rest
//     changes memory only and is stored with the next durable event.
//     Replaying from read_off rebuilds exactly what was not stored.
//   - a handoff (BeginShutdown) lets every adapter go (the pipe's Detach —
//     never a kill, never Client.Close); stdin writes check the engine
//     epoch first (hpTarget.Guard), so nothing reaches an adapter from a
//     process that no longer owns it.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

var (
	errHarnessStale = errors.New("a newer generation of this coding agent owns its session")
	errHarnessGone  = errors.New("this coding agent's session is no longer driven here")
)

// hsess is one coding agent's session this process drives.
type hsess struct {
	e      *Engine
	run    int64
	root   int64
	parent int64 // a subagent's parent (its turn ends settle its link)
	gen    int
	epoch  int64 // the engine epoch it was started (or attached) under
	prov   acp.Provider
	c      *acp.Client
	pipe   *harnessPipe
	perms  *acp.Permissions
	done   chan struct{} // the consumer ended
	// applyMu is held while the consumer applies an event, and by a
	// durable step from anywhere else (commit with no event): such a step
	// stores the draft as of `applied`, never with an event half applied.
	applyMu sync.Mutex

	mu      sync.Mutex
	applied int64   // the offset of the last event the consumer applied
	halted  bool    // let go (a handoff, a stop): nothing more is applied or written
	draft   hsDraft // text and thinking read, not yet flushed to a row
	calls   map[string]*hcall
	dirty   map[string]bool // calls whose in-memory tool row isn't published yet
	timer   *time.Timer     // the ≤ 250 ms upsert of dirty tool rows
	act     hActivity
	lastSum string // the harness event last published

	reuseMu sync.Mutex
	reuse   map[string]int // an adapter's call id used again for a new call → its count (sid)

	inflight  *heldPrompt       // the prompt of the turn in flight (a sign-in holds it)
	detached  bool              // the adapter runs a turn of its own (harness_steer.go)
	quiet     *time.Timer       // ends a detached turn once the adapter goes quiet
	quietDue  bool              // … it went quiet: the pass ends it
	idleT     *time.Timer       // the idle reclaim (harness_life.go)
	reclaim   bool              // the idle reclaim is due: the pass stops the adapter
	graceT    *time.Timer       // stops an adapter that doesn't end its turn after session/cancel
	checked   time.Time         // the sandbox's use was last re-checked
	fresh     bool              // just started (or attached): that checked it
	checking  bool              // a re-check is on its way
	authing   *hAuth            // a sign-in AgTT started (harness_login.go)
	abandoned map[string]string // request ids the pipe gave up on → why
	endWhy    string            // what ended() says when the end is AgTT's doing
	endLost   bool              // … and the session is lost, not stopped
	newSess   bool              // spawned to open a new session (not session/load): its first mode is the adapter's own
	steerOut  json.RawMessage   // the request id of the steer frame last put on stdin (harness_steer.go)
	braked    bool              // a halt conf says is on was seen at an event (harness_partition.go brakeSoon)
	work      uint64            // how many times it went to work (toWork): a rest after a turn's end checks it (harness_partition.go)
	// cred is the saved sign-in the adapter started with (its id; ""
	// none: harness_creds.go). While one is in, only the adapter's own
	// refusal of a prompt or a session says it is signed out (authRefused,
	// set by the client's AuthHint for a -32000) — claude-agent-acp's
	// status probe calls an env token's sign-in "none". Atomic: a start
	// that finds the CLI signed in on its own drops it while the consumer
	// reads it (credID).
	cred        atomic.Pointer[string]
	authRefused atomic.Bool
	// secret is cred's value, held for this generation only: redacted from
	// all the adapter prints (red, harness_redact.go) and handed to an
	// adapter that takes keys only through authenticate. Never stored.
	secret    string
	red       atomic.Pointer[redactor]
	scrubLeft string // a key file that couldn't be removed after the handover: the cred's id (harness_sessions.scrub)

	// rest: no turn at work — idle, or parked on a person. In a person's
	// partition only a working session keeps the hold, or one AgTT is
	// signing in (signing: a sign-in it started, awaiting the adapter's
	// answer) (harness_partition.go).
	rest    atomic.Bool
	signing atomic.Bool
}

// newHsess is a session of run at generation gen, started (or attached)
// under epoch.
func newHsess(e *Engine, run *Run, gen int, epoch int64, prov acp.Provider) *hsess {
	s := &hsess{e: e, run: run.ID, root: rootOf(run), parent: run.ParentID, gen: gen, epoch: epoch, prov: prov,
		perms: acp.NewPermissions(), done: make(chan struct{}), calls: map[string]*hcall{}, dirty: map[string]bool{},
		act: hActivity{Kind: "idle", At: nowMs()}, abandoned: map[string]string{}, checked: time.Now(), fresh: true,
		reuse: e.db.harnessReuse(run.ID, gen)}
	s.setSecret("")
	return s
}

// credID is the saved sign-in the adapter started with ("" none).
func (s *hsess) credID() string {
	if p := s.cred.Load(); p != nil {
		return *p
	}
	return ""
}

func (s *hsess) setCred(id string) { s.cred.Store(&id) }

// setSecret is the generation's saved sign-in's secret ("" none: the token
// shape alone is redacted).
func (s *hsess) setSecret(secret string) {
	s.secret = secret
	s.red.Store(newRedactor(secret))
}

// redactor is what the adapter's output is redacted with.
func (s *hsess) redactor() *redactor {
	if r := s.red.Load(); r != nil {
		return r
	}
	return newRedactor()
}

// hsDraft is the unflushed draft: the run's own text and thinking, and a
// harness-internal subagent's by the stored id of the call it runs under.
type hsDraft struct {
	Text    string              `json:"text,omitempty"`
	Think   string              `json:"think,omitempty"`
	Started int64               `json:"started,omitempty"` // ms
	Subs    map[string]*hsDraft `json:"subs,omitempty"`
}

func (d *hsDraft) empty() bool { return d.Text == "" && d.Think == "" && len(d.Subs) == 0 }

// hActivity is what the agent is doing now (§4.3.2 activity).
type hActivity struct {
	Kind  string `json:"kind"`
	Title string `json:"title,omitempty"`
	At    int64  `json:"at"`
}

// harnessCaps is what AgTT advertises (§6.1): no files or terminals of the
// client's (the adapter works in the sandbox itself), the tool-call
// extensions it renders, form questions, url questions (a device-code
// sign-in) and the exact argv of a terminal sign-in.
func harnessCaps() *acp.ClientCapabilities {
	return &acp.ClientCapabilities{
		Meta: map[string]any{"terminal_output": true, "terminal_output_delta": true, "subagent-transcript": true,
			"terminal-auth": true},
		Elicitation: &acp.ElicitationCaps{Form: &struct{}{}, URL: &struct{}{}},
	}
}

// --- the registry ---------------------------------------------------------------

// harnessOf is the session of run this process drives (nil: none).
func (e *Engine) harnessOf(run int64) *hsess {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.harness[run]
}

// adopt registers s; false when the engine is going away (s is let go).
func (e *Engine) adopt(s *hsess) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closing || !e.owned {
		return false
	}
	if old := e.harness[s.run]; old != nil && old != s {
		old.halt()
	}
	e.harness[s.run] = s
	e.updateHoldLocked()
	return true
}

// forget unregisters s (when it is still the run's).
func (e *Engine) forget(s *hsess) {
	e.mu.Lock()
	if e.harness[s.run] == s {
		delete(e.harness, s.run)
		e.updateHoldLocked()
	}
	e.mu.Unlock()
}

// stopRestingCreds stops, as a person's partition shuts down, each adapter
// of this process that rests (up, no turn, nothing asked of the person) with
// a saved sign-in in its environment, waiting for the manager's kill:
// nothing holding a secret runs on while the partition is stopped — xbind
// gives a partition no hook before it purges a deleted person, whose
// partition then never starts again to stop it (the review's L13). The next
// message starts it again (session/load). One at work is let go to the
// successor as any other (API.md: the residual).
func (e *Engine) stopRestingCreds() {
	if !userMode() {
		return
	}
	e.mu.Lock()
	all := make([]*hsess, 0, len(e.harness))
	for _, s := range e.harness {
		all = append(all, s)
	}
	e.mu.Unlock()
	for _, s := range all {
		if s.pipe == nil {
			continue
		}
		stopped := false
		_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
			run, err := t.getRun(s.run)
			if err != nil || hs.Cred == "" || hs.State != hsLive || hs.PromptState != "" ||
				run.Status == statusWaiting || run.Status == statusRunning {
				return err
			}
			hs.State, stopped = hsStopped, true
			e.emitRun(t, s.run)
			return nil
		})
		if !stopped {
			continue
		}
		s.halt()
		e.forget(s)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := s.pipe.KillNow(ctx)
		cancel()
		if err != nil {
			logf("run #%d: stopping %s (a saved sign-in in its environment) as the partition stops: %v", s.run, s.prov.Name, err)
		} else {
			logf("run #%d: %s stopped with the partition (a saved sign-in in its environment) — the next message starts it again", s.run, s.prov.Name)
		}
	}
}

// letHarnessesGo is the handoff: every adapter is let go untouched — its
// pipe detached, its consumer halted — for the successor to attach to.
func (e *Engine) letHarnessesGo() {
	e.mu.Lock()
	all := make([]*hsess, 0, len(e.harness))
	for _, s := range e.harness {
		all = append(all, s)
	}
	e.mu.Unlock()
	for _, s := range all {
		s.halt()
		s.pipe.Detach()
	}
	e.dropGuided() // a guided sign-in lives in the process that started it: the person starts over
}

func (s *hsess) halt() {
	s.mu.Lock()
	s.halted = true
	for _, t := range []**time.Timer{&s.timer, &s.quiet, &s.idleT, &s.graceT} {
		if *t != nil {
			(*t).Stop()
			*t = nil
		}
	}
	s.reclaim = false
	s.mu.Unlock()
}

func (s *hsess) isHalted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.halted
}

// guard is the pipe's check before every stdin write: this process still
// owns the session (the engine epoch it started under is the database's).
func (s *hsess) guard() error {
	e := s.e
	e.mu.Lock()
	closing, owned, ep := e.closing, e.owned, e.epoch
	e.mu.Unlock()
	switch {
	case closing:
		return errHandoff
	case !owned || ep != s.epoch:
		return errFenced
	case s.isHalted():
		return errHarnessGone
	}
	cur, err := e.epochNow() // a read that fails refuses this write, and is no takeover (epoch.go)
	if err != nil {
		return err
	}
	if cur != ep {
		return errFenced
	}
	return nil
}

// --- ensure: a live session for a prompt ---------------------------------------------

// harnessFail is why a session couldn't start, for people (the run's error).
type harnessFail struct{ msg string }

func (f *harnessFail) Error() string { return f.msg }

// ensureHarness returns run's live session: the one this process drives, an
// adapter a predecessor left running (attached), or a new generation. An
// error says why none could be had (a *harnessFail is the harness's own
// failure — the session is stored failed; anything else, like a handoff,
// leaves it as it was).
func (e *Engine) ensureHarness(ctx context.Context, run *Run) (*hsess, error) {
	return e.ensureHarnessAt(ctx, run, false)
}

// ensureHarnessAt is ensureHarness; attachOnly takes over a running adapter
// and starts none (nil, nil: none runs).
func (e *Engine) ensureHarnessAt(ctx context.Context, run *Run, attachOnly bool) (*hsess, error) {
	mu := e.harnessLock(run.ID) // the pass, a sign-in's route, a delete: one at a time
	mu.Lock()
	defer mu.Unlock()
	if s := e.harnessOf(run.ID); s != nil && !s.isHalted() {
		select {
		case <-s.done:
		default:
			return s, nil
		}
		e.forget(s)
	}
	cfg, err := e.db.runConfig(run.ID)
	if err != nil {
		return nil, err
	}
	if cfg.Harness == nil {
		return nil, &harnessFail{"this conversation names no coding agent"}
	}
	hs, err := e.db.harnessSession(run.ID)
	if err != nil {
		return nil, err
	}
	if attachOnly && (hs == nil || hs.ExecID == "" || !hsRunning(hs.State)) {
		return nil, nil
	}
	if hs != nil && hs.ExecID != "" && hsRunning(hs.State) {
		if st, ok := attachable(hs); ok {
			s, err := e.attachHarness(ctx, run, cfg, hs, st)
			if err == nil || !isHarnessFail(err) {
				return s, err
			}
			// the conversation may no longer drive it: stopped as a
			// refusal mid-turn is (§3.3) — never left to work on unread
			e.stopHarnessNow(ctx, run, err.Error())
			return nil, err
		}
		// the predecessor was still opening the session (initialize,
		// session/new or a session/load replay): start over
		e.dropExec(ctx, run, cfg, hs)
	}
	s, err := e.spawnHarness(ctx, run, cfg, hs)
	if isHarnessFail(err) {
		return nil, e.failHarness(run, err)
	}
	return s, err
}

// harnessLock is run's start lock (ensureHarness).
func (e *Engine) harnessLock(run int64) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hlocks == nil {
		e.hlocks = map[int64]*sync.Mutex{}
	}
	mu := e.hlocks[run]
	if mu == nil {
		mu = &sync.Mutex{}
		e.hlocks[run] = mu
	}
	return mu
}

func isHarnessFail(err error) bool {
	var f *harnessFail
	return errors.As(err, &f)
}

// attachable is the stored snapshot of a running adapter a successor
// takes over: one with its session open, or one parked on its sign-in
// that refused a session signed out (codex: AwaitLogin — authenticate
// opens the session through it). false: it was still opening one.
func attachable(hs *harnessSession) (acp.SessionState, bool) {
	var st acp.SessionState
	if json.Unmarshal([]byte(hs.Snapshot), &st) != nil {
		return st, false
	}
	return st, st.SessionID != "" || (hs.State == hsLogin && st.AuthNeeded)
}

// failHarness stores the session failed with err's words.
func (e *Engine) failHarness(run *Run, err error) error {
	_ = e.fenced(func(t *DB) error {
		hs, _ := t.harnessSession(run.ID)
		if hs == nil {
			hs = &harnessSession{RunID: run.ID, RootID: rootOf(run)}
		}
		hs.State, hs.Error, hs.PromptState, hs.PromptRPC = hsFailed, err.Error(), "", ""
		return t.putHarnessSession(hs)
	})
	return err
}

// harnessUse is the sandbox a harness run works in, checked as every
// sandbox tool checks it (sandboxUse), plus what a coding agent needs of it.
func (e *Engine) harnessUse(ctx context.Context, run *Run, cfg Config) (*sbxUse, acp.Provider, error) {
	if why := harnessBarred(run); why != "" { // the global instance, a hosted conversation (harness_partition.go)
		return nil, acp.Provider{}, &harnessFail{why}
	}
	h := cfg.Harness
	b, ok := cfg.sandboxBinding(h.Ref)
	if !ok {
		return nil, acp.Provider{}, &harnessFail{"this coding agent's sandbox is no longer attached to the conversation"}
	}
	u, err := e.ag.sandboxUse(ctx, rootOf(run), cfg, h.Ref)
	if err != nil {
		var se *sbxError
		if errors.As(err, &se) && se.Refusal != "unavailable" && se.Refusal != "unreachable" {
			return nil, acp.Provider{}, &harnessFail{err.Error()}
		}
		return nil, acp.Provider{}, err
	}
	name := orStr(u.Box.Name, orStr(b.Name, b.Ref))
	prov, ok := harnessIn(u, h.Provider)
	if !ok {
		return nil, prov, &harnessFail{fmt.Sprintf("%s's image doesn't have %s", name, harnessName(h.Provider))}
	}
	if u.Box.effectiveEgress() == "none" {
		return nil, prov, &harnessFail{fmt.Sprintf("%s must reach its provider — %s's egress is none", prov.Name, name)}
	}
	return u, prov, nil
}

// harnessIn is the provider id as the sandbox's manager has it: its
// image's advertisement, or — a manager whose hello predates the field —
// the catalog's.
func harnessIn(u *sbxUse, id string) (acp.Provider, bool) {
	if u.Hello.advertises() {
		for _, e := range u.Hello.imageHarnessEntries(u.Box.Image.ID) {
			if e.ID == id {
				return harnessProvider(id, &e), true
			}
		}
		return acp.Provider{ID: id, Name: harnessName(id)}, false
	}
	if _, ok := acp.Lookup(id); ok {
		return harnessProvider(id, nil), true
	}
	return acp.Provider{ID: id, Name: id}, false
}

// harnessRunName is run's coding agent's name for people: as its manager
// advertised it at the last spawn (harness_sessions.name — the one name a
// harness the sdk catalog lacks has), else harnessName.
func (d *DB) harnessRunName(runID int64, provider string) string {
	var n string
	if d.q.QueryRow(`SELECT name FROM harness_sessions WHERE run_id=?`, runID).Scan(&n) == nil && n != "" {
		return n
	}
	return harnessName(provider)
}

// harnessName is a provider's name for people ("Claude Code").
func harnessName(id string) string {
	if p, ok := acp.Lookup(id); ok {
		return p.Name
	}
	if id == fakeHarness {
		return acp.Fake(nil).Name
	}
	return id
}

// installed is what the agent knows about prov in ref, probing a running
// sandbox when it doesn't know yet: false only when it knows it is not.
func (e *Engine) installed(ctx context.Context, u *sbxUse, ref string, prov acp.Provider, binder who) (bool, string) {
	seen := e.db.seenHarnesses(prov.ID)[ref]
	if seen.Installed == nil && u.Box.State == "running" {
		probeHarnesses(ctx, binder, ref, []hcManager{{m: u.Conn.M, hello: u.Hello}})
		seen = e.db.seenHarnesses(prov.ID)[ref]
	}
	if seen.Installed != nil && !*seen.Installed {
		return false, strings.Join(prov.Bins, ", ")
	}
	return true, ""
}

// spawnHarness starts a new generation: the adapter in the sandbox, the
// client's handshake (session/load of the earlier session when it can).
func (e *Engine) spawnHarness(ctx context.Context, run *Run, cfg Config, hs *harnessSession) (*hsess, error) {
	u, prov, err := e.harnessUse(ctx, run, cfg)
	if err != nil {
		return nil, err
	}
	h := cfg.Harness
	name := orStr(u.Box.Name, h.Ref)
	b, _ := cfg.sandboxBinding(h.Ref)
	if ok, bins := e.installed(ctx, u, h.Ref, prov, binderWho(b.By)); !ok {
		return nil, &harnessFail{fmt.Sprintf("%s doesn't have %s (%s not found)", name, prov.Name, bins)}
	}
	if hs == nil {
		hs = &harnessSession{RunID: run.ID, RootID: rootOf(run)}
	}
	// a saved sign-in of the person's, through the gate (harness_creds.go):
	// its secret in the adapter's environment only — never stored
	cred, credNote := e.db.credPick(run, cfg, h.Ref, u.Box)
	env, secret, err := credEnv(prov.Env, cred)
	if err != nil {
		return nil, &harnessFail{err.Error()}
	}
	env = withProjectEnv(env, run, u.Box.Home) // a project task's ports, branch and credentials' env (project_gate.go)
	credID := ""
	if cred != nil {
		credID = cred.ID
	}
	// a key the previous generation handed a CLI that keeps it in a file
	// (codex): gone before this one starts, or it would start as that account
	if err := e.scrubPrevious(ctx, u, prov, hs, credID); err != nil {
		return nil, &harnessFail{fmt.Sprintf("%s kept the key of its last start in %s, and it couldn't be removed (%v) — "+
			"try again; it doesn't start as the wrong account", prov.Name, name, err)}
	}
	wasLogin := hs.State == hsLogin // started to sign in: it stays parked on it
	gen := hs.Gen + 1
	cwd := orStr(h.Cwd, u.Cwd)
	resume := ""
	if hs.Loadable && hs.ACPSession != "" {
		resume = hs.ACPSession
	}
	e.mu.Lock()
	epoch := e.epoch
	e.mu.Unlock()
	s := newHsess(e, run, gen, epoch, prov)
	s.newSess = resume == ""
	if cred != nil {
		s.setCred(cred.ID)
	}
	s.setSecret(secret)  // redacted from all it prints (harness_redact.go)
	var rules []acp.Rule // what "allow always" answers remember in this conversation
	if json.Unmarshal([]byte(hs.Rules), &rules) == nil {
		s.perms.SetRules(rules)
	}
	err = e.fenced(func(t *DB) error {
		cur, err := t.harnessSession(run.ID)
		if err != nil {
			return err
		}
		if cur != nil && cur.Gen != hs.Gen {
			return errHarnessStale
		}
		if cur != nil {
			hs = cur
		}
		hs.Gen, hs.State, hs.Error = gen, hsStarting, ""
		hs.Ref, hs.Cwd, hs.Provider, hs.Argv = h.Ref, cwd, prov.ID, prov.Argv
		hs.ExecID, hs.ClientID = "", harnessClientID(run.ID, gen)
		hs.ReadOff, hs.ErrOff, hs.Draft, hs.Answers = 0, 0, "", "" // a new adapter: no request of the old one's to answer
		hs.Shared, hs.Name = sandboxShared(u.Box), prov.Name
		hs.LastActiveMs, hs.StartedMs = nowMs(), nowMs()
		hs.Cred = s.credID() // which saved sign-in, never its secret
		hs.Scrub = ""        // scrubPrevious removed it
		if credNote != "" {
			s.e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": credNote}))
		}
		t.harnessUsageTx() // a person's partition's usage totals (harness_partition.go)
		return t.putHarnessSession(hs)
	})
	if err != nil {
		return nil, err
	}
	tg := harnessTarget(u)
	tg.Guard, tg.Dropped = s.guard, s.dropped
	pipe, err := startHarnessPipe(ctx, tg, hpSpawn{Run: run.ID, Root: rootOf(run), Gen: gen, Provider: prov.ID,
		Argv: prov.Argv, Cwd: cwd, Env: env})
	env = nil
	if err != nil {
		return nil, &harnessFail{fmt.Sprintf("couldn't start %s in %s: %v", prov.Name, name, err)}
	}
	s.pipe = pipe
	if err := s.commit(nil, func(t *DB, hs *harnessSession) error { hs.ExecID = pipe.ExecID(); return nil }); err != nil {
		pipe.Detach()
		return nil, err
	}
	if !e.adopt(s) {
		pipe.Detach()
		return nil, errHandoff
	}
	s.c = acp.NewWith(acp.ClientOptions{Caps: harnessCaps(), IDPrefix: fmt.Sprintf("h%d.%d", run.ID, gen), AwaitLogin: true,
		AuthHint: s.authHint})
	go s.consume()
	opts := map[string]string{}
	for k, v := range h.Options {
		if k != "mode" {
			opts[k] = v
		}
	}
	acfg := acp.Config{Provider: prov, Mode: h.Mode, Options: opts, SkipModeOptions: true, ResumeID: resume, Cwd: cwd,
		Argv: prov.Argv, Perms: s.perms, Log: s.log, Spawn: func(context.Context, acp.Config) (*acp.Process, error) { return s.process(), nil }}
	err = s.c.Start(ctx, acfg)
	handed := false
	if sid, _ := s.c.Session(); err != nil && isAuthErr(err) && sid == "" && s.c.State().AuthNeeded && cred != nil && s.credAuthenticate(ctx, cred) {
		err, handed = nil, true // a saved API key the adapter takes only through authenticate (codex, gemini): its session is open now
	}
	if handed && prov.AuthFile != "" {
		// codex wrote it to its auth.json: out at once — it keeps the key in
		// memory; a failure leaves it for the next start, a share or Forget
		if serr := scrubKeyFile(ctx, u.Conn, u.ID, prov, secret); serr != nil {
			logf("run #%d: %v — removed at the next start", run.ID, serr)
			s.scrubLeft = cred.ID
		}
	}
	if err == nil && cred != nil && prov.AuthFile != "" && !handed {
		// a CLI whose key goes only through authenticate came up signed in on
		// its own (the sandbox's own `codex login`): that sign-in is what it
		// uses — never overwritten, never named as the saved one
		s.setCred("")
		cred = nil
		s.setSecret("")
		e.note(run, fmt.Sprintf("%s is signed in in this sandbox on its own — it uses that sign-in, not your saved one "+
			"(sign it out there, `codex logout`, to use the saved one)", prov.Name))
	}
	if err != nil {
		if ctx.Err() != nil || s.isHalted() {
			return nil, errHandoff
		}
		if resume != "" && !errors.Is(err, context.DeadlineExceeded) && !isAuthErr(err) {
			// the earlier session can't be reopened (the adapter lost it):
			// a fresh one, with a note — the conversation's rows stay
			s.stop()
			e.note(run, fmt.Sprintf("%s couldn't reopen its earlier session (%v) — it starts a new one", prov.Name, err))
			_ = e.fenced(func(t *DB) error {
				cur, _ := t.harnessSession(run.ID)
				if cur != nil {
					cur.Loadable, cur.ACPSession = false, ""
					return t.putHarnessSession(cur)
				}
				return nil
			})
			hs, _ := e.db.harnessSession(run.ID)
			return e.spawnHarness(ctx, run, cfg, hs)
		}
		if sid, _ := s.c.Session(); isAuthErr(err) && sid == "" && s.c.State().AuthNeeded {
			// it refused a session signed out (codex): it stays up for a
			// sign-in through it (harness_login.go)
			if cerr := s.commit(nil, func(t *DB, hs *harnessSession) error {
				hs.Steering = s.c.State().Steering
				s.refuseCredTx(t, hs, err.Error()) // it refused the saved sign-in it was started with
				return s.loginTx(t, hs, nil)
			}); cerr != nil {
				s.stop()
				return nil, cerr
			}
			return s, errHarnessLogin
		}
		s.stop()
		return nil, &harnessFail{fmt.Sprintf("%s didn't start in %s: %v", prov.Name, name, err)}
	}
	sid, loadable := s.c.Session()
	st := s.c.State()
	err = s.commit(nil, func(t *DB, hs *harnessSession) error {
		hs.State, hs.ACPSession, hs.Loadable, hs.Steering = hsLive, sid, loadable, st.Steering
		hs.Cred, hs.Scrub = s.credID(), s.scrubLeft
		if resume == "" {
			noteStartMode(hs, h.Mode, st)
		}
		if err := s.dropModeOptionsTx(t, run, st); err != nil {
			return err
		}
		if wasLogin || s.signedOutNow(st) {
			return s.loginTx(t, hs, nil)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !wasLogin && !s.signedOutNow(st) {
		signed := true
		_ = e.db.noteHarnessSeen(h.Ref, prov.ID, &signed, &signed)
	}
	s.publishSummary()
	s.armIdle()
	return s, nil
}

// noteStartMode records the mode a fresh session opened in when none was
// asked of it (the adapter's own — harnessModeOpen), once.
func noteStartMode(hs *harnessSession, asked string, st acp.SessionState) {
	if hs.StartMode == "" && asked == "" {
		hs.StartMode = currentMode(st)
	}
}

// dropModeOptionsTx: a stored config option the live adapter reports as its
// option of category mode was not set (SkipModeOptions — the mode is
// Harness.Mode's, and its owner-only rule, §4.2.3): it leaves the
// conversation's options, with a note.
func (s *hsess) dropModeOptionsTx(t *DB, run *Run, st acp.SessionState) error {
	cfg, err := t.runConfig(run.ID)
	if err != nil || cfg.Harness == nil {
		return err
	}
	var dropped []string
	for _, o := range st.Options {
		if _, ok := cfg.Harness.Options[o.ID]; ok && o.Category == "mode" {
			delete(cfg.Harness.Options, o.ID)
			dropped = append(dropped, o.ID)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	raw, _ := json.Marshal(cfg)
	if _, err := t.q.Exec(`UPDATE runs SET config=? WHERE id=?`, string(raw), run.ID); err != nil {
		return err
	}
	s.e.emitStep(t, s.root, t.journal(run.ID, "note", map[string]string{"text": fmt.Sprintf(
		"%s's option %s is its mode — not set as an option (the mode picker switches it)", s.prov.Name, strings.Join(dropped, ", "))}))
	return nil
}

func isAuthErr(err error) bool {
	var re *acp.Error
	return errors.As(err, &re) && re.Code == acp.CodeAuthRequired
}

// sandboxShared: others may use the sandbox — anything but private (team
// visibility, a visibility this agent doesn't know), members or shares —
// the privacy note (§4.3.2 sandbox.shared).
func sandboxShared(b *sbxSandbox) bool {
	return b != nil && !sandboxPrivate(b)
}

// sandboxPrivate: nobody else may use b — an allow-list (the review's L9):
// visibility "" or "private", not seen through a share, no members, no
// shares. Anything else, a visibility a newer manager adds included, is
// shared.
func sandboxPrivate(b *sbxSandbox) bool {
	if b == nil {
		return false
	}
	sh := strings.TrimSpace(string(b.Shares))
	return (b.Visibility == "" || b.Visibility == "private") && !b.Shared && len(b.Members) == 0 &&
		(sh == "" || sh == "null" || sh == "[]" || sh == "{}")
}

// dropExec ends the adapter hs names that this process doesn't drive (a
// predecessor's, half-started or refused; best effort, in the background:
// Kill). It needs no rights: a refusal is exactly when an adapter must
// stop — its manager is called for the person who bound the sandbox (who
// started it; the harness's starter once the binding is gone) over the
// exec's routes.
func (e *Engine) dropExec(ctx context.Context, run *Run, cfg Config, hs *harnessSession) {
	if hs == nil || hs.ExecID == "" {
		return
	}
	ref, by := hs.Ref, ""
	if cfg.Harness != nil {
		ref, by = orStr(ref, cfg.Harness.Ref), cfg.Harness.By
	}
	if b, ok := cfg.sandboxBinding(ref); ok && b.By != "" {
		by = b.By
	}
	provider, id, ok := splitSandboxRef(ref)
	if !ok {
		return
	}
	conn, err := sbxDial(provider, sbxUserOf(binderWho(by)))
	if err != nil {
		logf("run #%d: stopping its coding agent (exec %s): %v", run.ID, hs.ExecID, err)
		return
	}
	attachHarnessPipe(ctx, hpTarget{Conn: conn, ID: id}, hs.ExecID, hs.ReadOff, hs.ErrOff).Kill()
}

// --- attach: taking over a running adapter ---------------------------------------

// attachSeq numbers this process's attaches: with the epoch, a request id
// prefix no earlier client of the same adapter used.
var attachSeq struct {
	sync.Mutex
	n int
}

// attachHarness takes over the adapter a predecessor drove: its output
// read on from read_off, the client rebuilt from the stored snapshot — with
// the prompt in flight as the record says (A2: the snapshot may be newer
// than the offset), the permissions still pending restored and of its
// questions the ones the record knows (knownQuestions) — then the answers
// the predecessor had on their way sent again (hAnswer).
func (e *Engine) attachHarness(ctx context.Context, run *Run, cfg Config, hs *harnessSession, st acp.SessionState) (*hsess, error) {
	u, prov, err := e.harnessUse(ctx, run, cfg)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	epoch := e.epoch
	e.mu.Unlock()
	attachSeq.Lock()
	attachSeq.n++
	seq := attachSeq.n
	attachSeq.Unlock()
	s := newHsess(e, run, hs.Gen, epoch, prov)
	s.setCred(hs.Cred)
	s.scrubLeft = hs.Scrub
	if why := s.credStillFits(run, hs.Ref, u.Box); why != "" { // the sandbox was shared meanwhile: not with that sign-in in it
		return nil, &harnessFail{why}
	}
	if sg := e.db.signin(hs.Cred); sg != nil { // what to redact from the adapter's output from here on
		s.setSecret(credSecret(sg))
	}
	switch hs.PromptState {
	case "sent":
		st.PromptRPC, st.Turn = json.RawMessage(hs.PromptRPC), uint64(hs.Turn)
		s.act = hActivity{Kind: "thinking", At: nowMs()}
	case "sending":
		// it may never have reached the adapter: it fails (at most once,
		// never sent twice) — the person sends it again
		st.PromptRPC = nil
		err := e.fenced(func(t *DB) error {
			cur, _ := t.harnessSession(run.ID)
			if cur == nil || cur.Gen != hs.Gen {
				return errHarnessStale
			}
			cur.PromptState, cur.PromptRPC = "", ""
			if err := t.putHarnessSession(cur); err != nil {
				return err
			}
			return e.endHarnessTurnTx(t, run.ID, "error",
				"the backend was replaced while your message was on its way to "+prov.Name+" — send it again")
		})
		if err != nil {
			return nil, err
		}
	default:
		st.PromptRPC = nil
	}
	p := parsePending(run.Pending)
	for _, pd := range harnessPendings(p, hs.Queue) {
		s.perms.Restore(pd.Pending, pd.RPC)
	}
	answers := answersOf(hs.Answers, hs.Gen) // on their way when the predecessor let go: answered again below
	device := hs.State == hsLogin && loginDevice(hs.Login)
	st.Elicitations = knownQuestions(st.Elicitations, p, hs.Queue, answers, device)
	var rules []acp.Rule
	if json.Unmarshal([]byte(hs.Rules), &rules) == nil {
		s.perms.SetRules(rules)
	}
	if hs.Draft != "" {
		_ = json.Unmarshal([]byte(hs.Draft), &s.draft)
	}
	tg := harnessTarget(u)
	tg.Guard, tg.Dropped = s.guard, s.dropped
	s.pipe = attachHarnessPipe(ctx, tg, hs.ExecID, hs.ReadOff, hs.ErrOff)
	if !e.adopt(s) {
		s.pipe.Detach()
		return nil, errHandoff
	}
	s.c = acp.NewWith(acp.ClientOptions{Caps: harnessCaps(), AwaitLogin: true, Attach: &st, AuthHint: s.authHint,
		IDPrefix: fmt.Sprintf("h%d.%de%da%d", run.ID, hs.Gen, epoch, seq)})
	go s.consume()
	acfg := acp.Config{Provider: prov, Mode: cfg.Harness.Mode, Cwd: hs.Cwd, Argv: prov.Argv, Perms: s.perms, Log: s.log,
		Spawn: func(context.Context, acp.Config) (*acp.Process, error) { return s.process(), nil }}
	if err := s.c.Start(ctx, acfg); err != nil {
		s.stop()
		return nil, err
	}
	e.resetAttachRetry(run.ID)
	if !s.draft.empty() {
		s.showDraft()
	}
	if len(answers) > 0 {
		go s.reanswer(answers)
	}
	if device {
		// the device code shown: the adapter's URL question, accepted by
		// the predecessor as it showed it (its accept perhaps lost with the
		// handoff: accepted again — the adapter ignores a second answer).
		// Its elicitation/complete wakes the run (onURLComplete). None
		// restored: a code nobody here waits on is taken away — the person
		// signs in again, or Retries.
		urls := 0
		for _, q := range st.Elicitations {
			if q.Mode != "url" {
				continue
			}
			urls++
			if !q.Accepted {
				go func(eid string) { _ = s.c.RespondElicitation(eid, "accept", nil, "agtt") }(q.EID)
			}
		}
		if urls == 0 {
			_ = s.commit(nil, func(t *DB, cur *harnessSession) error { return s.setDeviceTx(t, cur, nil) })
		}
	}
	s.publishSummary()
	switch {
	case detachedStored(run, hs):
		// codex's detached turn (harness_steer.go), which only the
		// predecessor's memory followed: followed again, it ends once the
		// adapter is quiet, or interrupted
		s.followDetached()
	case hs.PromptState == "" && run.Status != statusWaiting && run.Status != statusRunning && hs.State == hsLive:
		if userMode() && hs.LastActiveMs > 0 {
			// a person's partition: its predecessor stopped with it resting,
			// and the wake-up it left came back for this reclaim
			// (harness_partition.go) — due at its last activity plus the
			// idle time, not a whole idle time from now
			s.armIdleFrom(time.UnixMilli(hs.LastActiveMs), nil)
		} else {
			s.armIdle()
		}
	case run.Status == statusWaiting:
		s.toRest() // parked on a person: their answer moves it (harness_partition.go)
	case userMode():
		e.brakeLook() // a turn taken over mid-way: the halt looked at while it works (harness_partition.go)
	}
	return s, nil
}

// knownQuestions is what of a snapshot's questions a successor restores:
// the ones the record knows — the question park's, the queued ones', one
// whose answer is on its way (hAnswer: added when the snapshot no longer
// has it, to be answered again), and a sign-in's url one (its device code)
// when the login shows the code (device) or it was accepted. The snapshot
// is the client's state as of a commit, and its read loop files a question
// before the consumer applies it — so a snapshot may hold one whose frame
// is past read_off. That one is dropped: read again, it parks then (a url
// one is declined: no sign-in of this process's asked for it); restored,
// the client would take its frame for one already filed and ask nobody,
// the adapter waiting for an answer no one is asked for.
func knownQuestions(list []acp.ElicitationState, p pendingState, queue string, answers []hAnswer, device bool) []acp.ElicitationState {
	known := map[string]bool{}
	if p.Kind == "question" && p.Harness != nil && p.Harness.EID != "" {
		known[p.Harness.EID] = true
	}
	var q []hQueued
	_ = json.Unmarshal([]byte(queue), &q)
	for _, it := range q {
		if it.Kind == "question" && it.Perm != nil && it.Perm.EID != "" {
			known[it.Perm.EID] = true
		}
	}
	answered := map[string]*hPark{}
	for _, a := range answers {
		if a.Kind == "question" && a.Park != nil && a.Park.EID != "" {
			known[a.Park.EID], answered[a.Park.EID] = true, a.Park
		}
	}
	var out []acp.ElicitationState
	for _, e := range list {
		if known[e.EID] || (e.Mode == "url" && (e.Accepted || device)) {
			out = append(out, e)
			delete(answered, e.EID)
		}
	}
	for eid, h := range answered {
		q := acp.Elicitation{EID: eid, Message: h.Message, Schema: h.Schema}
		if h.CallID != "" {
			q.ToolCallID = acpCallID(h.CallID)
		}
		out = append(out, acp.ElicitationState{Elicitation: q, RPCID: json.RawMessage(h.RPCID)})
	}
	return out
}

// loginDevice: the stored login shows a device code.
func loginDevice(raw string) bool {
	var l hLogin
	return raw != "" && json.Unmarshal([]byte(raw), &l) == nil && l.Device != nil
}

// harnessPending is one permission request a session waits on, as stored:
// the park's (pendingState.harness) and those queued behind it.
type harnessPending struct {
	acp.Pending
	RPC json.RawMessage
}

// harnessPendings are the permissions to restore after a handoff.
func harnessPendings(p pendingState, queue string) []harnessPending {
	var out []harnessPending
	if p.Harness != nil && p.Harness.PID != "" && p.Kind == "approval" {
		out = append(out, p.Harness.pending())
	}
	var q []hQueued
	if json.Unmarshal([]byte(queue), &q) == nil {
		for _, it := range q {
			if it.Perm != nil && it.Perm.PID != "" {
				out = append(out, it.Perm.pending())
			}
		}
	}
	return out
}

// --- the process and the consumer ---------------------------------------------------

// process is the pipe as the client starts it, its stdin watched for the
// prompt frame: the moment it has gone, the record says so ("sent").
func (s *hsess) process() *acp.Process {
	p := s.pipe.Process()
	p.Stdin = &hsStdin{WriteCloser: p.Stdin, s: s}
	p.Stdout = &hsStdout{Reader: newRedactReader(p.Stdout, s.redactor), s: s} // a saved sign-in never reaches a row (harness_redact.go)
	return p
}

type hsStdin struct {
	io.WriteCloser
	s *hsess
}

func (w *hsStdin) Write(b []byte) (int, error) {
	if bytes.Contains(b, []byte(`"`+acp.MSessionSteering+`"`)) { // before it goes: once tried, it may have reached the adapter
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(b, &m) == nil && m.Method == acp.MSessionSteering && len(m.ID) > 0 {
			w.s.steerGoing(m.ID)
		}
	}
	n, err := w.WriteCloser.Write(b)
	if err == nil && bytes.Contains(b, []byte(`"`+acp.MSessionPrompt+`"`)) {
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(b, &m) == nil && m.Method == acp.MSessionPrompt && len(m.ID) > 0 {
			w.s.promptSent(m.ID)
		}
	}
	return n, err
}

// promptSent records that the prompt reached the adapter: its request id
// and turn, so a successor adopts it (a handoff mid-turn).
func (s *hsess) promptSent(id json.RawMessage) {
	turn := s.c.State().Turn
	_ = s.e.fenced(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE harness_sessions SET prompt_state='sent', prompt_rpc=?, turn=?, updated_ms=?
			WHERE run_id=? AND gen=? AND prompt_state='sending'`, string(id), int64(turn), nowMs(), s.run, s.gen)
		return err
	})
}

func (s *hsess) log(line string) {
	logf("run #%d: %s: %s", s.run, s.prov.Name, s.redactor().text(line))
}

// consume applies the session's events in order until the client's stream
// ends — then the adapter is gone (unless this process let it go).
func (s *hsess) consume() {
	defer close(s.done)
	for ev := range s.c.Events() {
		if s.isHalted() {
			continue // drained, not applied: the successor reads it again
		}
		s.applyMu.Lock()
		s.touchDetached()
		s.apply(ev)
		if ev.Wire != nil && ev.Wire.Off > 0 {
			s.mu.Lock()
			s.applied = max(s.applied, ev.Wire.Off)
			s.mu.Unlock()
		}
		s.applyMu.Unlock()
		if ev.Type != acp.EvMessageDelta && ev.Type != acp.EvThoughtDelta {
			s.recheckSoon()
		}
		s.brakeSoon() // a person's partition: a halt read from conf, at any event (harness_partition.go)
	}
	s.ended()
}

// stop ends the session this process drives: the adapter closed (its
// stdin, then TERM, then DELETE), nothing more applied.
func (s *hsess) stop() {
	s.halt()
	s.e.forget(s)
	if s.c != nil {
		_ = s.c.Close()
	} else if s.pipe != nil {
		s.pipe.Kill()
	}
}

// ended: the adapter's output ended — it exited, was killed, or the exec
// is gone (lost). A turn in flight ends with a note; the next prompt
// starts a new generation (with session/load when it can).
func (s *hsess) ended() {
	s.e.forget(s)
	perr := s.pipe.Err()
	if s.isHalted() || errors.Is(perr, errPipeDetached) || errors.Is(perr, errPipeReplaced) {
		return // let go: another process (or a stop) owns what follows
	}
	s.mu.Lock()
	endWhy, endLost := s.endWhy, s.endLost
	s.mu.Unlock()
	lost := s.pipe.Lost() || perr != nil || endLost // a manager that stopped answering: cut off too
	why := fmt.Sprintf("%s stopped", s.prov.Name)
	switch ex := s.pipe.Exit(); {
	case perr != nil:
		why = fmt.Sprintf("%s was cut off (%v)", s.prov.Name, perr)
	case lost:
		why = fmt.Sprintf("%s was cut off — its sandbox stopped or restarted", s.prov.Name)
	case ex.Code != nil && *ex.Code != 0:
		why = fmt.Sprintf("%s exited %d", s.prov.Name, *ex.Code)
	case ex.Signal != "":
		why = fmt.Sprintf("%s was killed (%s)", s.prov.Name, ex.Signal)
	}
	msg := why + " during the turn — send a message to go on"
	if endWhy != "" {
		why, msg = endWhy, endWhy
	}
	s.flushDraft()
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
		inFlight := hs.PromptState != ""
		hs.State, hs.PromptState, hs.PromptRPC, hs.Queue, hs.Login, hs.Answers = hsStopped, "", "", "", "", ""
		if lost {
			hs.State, hs.Error = hsLost, why
		}
		run, err := t.getRun(s.run)
		if err != nil {
			return err
		}
		s.settleParkTx(t, run, "(interrupted)")
		if inFlight || run.Status == statusRunning || run.Status == statusWaiting {
			return s.e.endHarnessTurnTx(t, s.run, "error", msg)
		}
		s.e.emitRun(t, s.run)
		return nil
	})
	s.publishSummary()
}

// --- durable commits ------------------------------------------------------------------

// commit is one durable step of the session: fn's writes, the offset read
// through ev (its frame; nil, or an event of no frame: the last event
// applied — what the draft and the tool rows stored are as of), the stderr
// offset, the snapshot, the unflushed draft and the in-memory tool rows —
// in one fenced transaction. Nothing is written once the session was let go
// or a newer generation owns the row. ev is the consumer's (it applies it);
// with none, the step is from elsewhere (the pass, a sign-in) and waits for
// the event being applied — so a successor that reads on from the offset
// stored never gets again what this step flushed (a steer's draft).
func (s *hsess) commit(ev *acp.Event, fn func(t *DB, hs *harnessSession) error) error {
	if ev == nil {
		s.applyMu.Lock()
		defer s.applyMu.Unlock()
	}
	var snap []byte
	if s.c != nil {
		snap, _ = json.Marshal(storedState(s.c.State())) // never a device code (harness_login.go)
	}
	s.mu.Lock()
	rows := s.dirtyRowsLocked()
	s.mu.Unlock()
	err := s.e.fenced(func(t *DB) error {
		if s.isHalted() {
			return errHarnessGone
		}
		hs, err := t.harnessSession(s.run)
		if err != nil {
			return err
		}
		if hs == nil || hs.Gen != s.gen {
			return errHarnessStale
		}
		for _, m := range rows {
			if err := t.putHarnessRow(m); err != nil {
				return err
			}
		}
		if fn != nil {
			if err := fn(t, hs); err != nil {
				return err
			}
		}
		if ev != nil && ev.Wire != nil && ev.Wire.Off > 0 {
			hs.ReadOff = ev.Wire.Off
		} else {
			s.mu.Lock()
			hs.ReadOff = max(hs.ReadOff, s.applied)
			s.mu.Unlock()
		}
		if s.pipe != nil {
			hs.ErrOff = max(hs.ErrOff, s.pipe.ErrOff())
		}
		if snap != nil {
			hs.Snapshot = string(snap)
		}
		if rules := s.perms.Rules(); len(rules) > 0 { // "allow always" answers, for a successor
			b, _ := json.Marshal(rules)
			hs.Rules = string(b)
		}
		s.mu.Lock() // what fn flushed is a row now: the draft as it stands after it
		hs.Draft = ""
		if !s.draft.empty() {
			b, _ := json.Marshal(s.draft)
			hs.Draft = string(b)
		}
		s.mu.Unlock()
		hs.LastActiveMs = nowMs()
		return t.putHarnessSession(hs)
	})
	if err != nil && !errors.Is(err, errHarnessGone) {
		logf("run #%d: %s: a durable step failed: %v", s.run, s.prov.Name, err)
	}
	if err == nil {
		s.mu.Lock()
		for _, m := range rows {
			delete(s.dirty, m.call)
		}
		s.mu.Unlock()
	}
	return err
}

// note journals a note on run (outside any other write).
func (e *Engine) note(run *Run, text string) {
	_ = e.fenced(func(t *DB) error {
		e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": text}))
		return nil
	})
}

// --- the turn's end -----------------------------------------------------------------

// endHarnessTurnTx ends a harness run's turn: why is end_turn and the ACP
// stop reasons (max_tokens, max_turn_requests, refusal, cancelled), or
// "error" with msg. The run rests (idle, or error), a subagent's link
// settles with the turn's last text, and the run is poked for what is
// queued.
func (e *Engine) endHarnessTurnTx(t *DB, runID int64, why, msg string) error {
	run, err := t.getRun(runID)
	if err != nil {
		return err
	}
	status, outcome, linkState := statusIdle, outcomeAnswered, linkDone
	note := ""
	switch why {
	case "end_turn", "":
	case "cancelled":
		outcome, linkState = outcomeInterrupted, linkCanceled
	case "max_tokens":
		outcome, note = outcomeIncomplete, "the coding agent stopped: its answer hit the token limit"
	case "max_turn_requests":
		outcome, note = outcomeIncomplete, "the coding agent stopped: it hit its limit of requests in one turn"
	case "refusal":
		outcome, note = outcomeIncomplete, "the coding agent refused to go on"
	default:
		status, outcome, linkState = statusError, outcomeError, linkError
		if msg == "" {
			msg = "the coding agent's turn failed"
		}
	}
	result := run.Result
	if status == statusError {
		result = msg
	}
	if err := t.setStatus(run.ID, status, 0, result, ""); err != nil {
		return err
	}
	root := rootOf(run)
	switch {
	case status == statusError:
		e.emitStep(t, root, t.journal(run.ID, "error", map[string]string{"error": msg}))
	case note != "":
		e.emitStep(t, root, t.journal(run.ID, "note", map[string]string{"text": note}))
	}
	if hs, _ := t.harnessSession(run.ID); hs != nil && hs.Plan != "" {
		var plan any
		if json.Unmarshal([]byte(hs.Plan), &plan) == nil {
			t.journal(run.ID, "plan", plan) // the turn's last plan (not shown in the chat)
		}
	}
	if run.ParentID == 0 {
		t.bumpActivity(run.ID)
	} else {
		res := t.harnessTurnText(run.ID)
		if status == statusError {
			res = msg
		}
		e.settleOwnLink(t, run, linkState, outcome, res)
	}
	runTurnEnd(t, run, harnessTurnWhy(why), outcome, orStr(msg, t.harnessTurnText(run.ID))) // turnEndHooks (project_events.go)
	e.emitRun(t, run.ID)
	if status == statusError {
		t.AfterCommit(func() { publishEvent(run.ID, "error") })
	}
	t.AfterCommit(func() { e.Poke(run.ID) }) // what is queued goes next
	return nil
}

// harnessTurnText is the answer of run's current (or last) turn: its newest
// text after the turn's first row (harness_sessions.turn_seq) — "(no
// answer)" when the turn wrote none, never an earlier turn's.
func (d *DB) harnessTurnText(runID int64) string {
	var from int64
	_ = d.q.QueryRow(`SELECT turn_seq FROM harness_sessions WHERE run_id=?`, runID).Scan(&from)
	var c string
	_ = d.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND role='assistant' AND content!='' AND seq>?
		ORDER BY seq DESC LIMIT 1`, runID, from).Scan(&c)
	if c == "" {
		return "(no answer)"
	}
	return c
}

// --- summaries and the harness event ---------------------------------------------------

// publishSummary sends the `harness` event (§4.3.3) when the summary
// changed since the last one sent.
func (s *hsess) publishSummary() {
	if s.isHalted() {
		return
	}
	run, err := s.e.db.getRun(s.run)
	if err != nil {
		return
	}
	sum := harnessSummary(s.e.db, run)
	if sum == nil {
		return
	}
	b, _ := json.Marshal(sum)
	s.mu.Lock()
	same := string(b) == s.lastSum
	s.lastSum = string(b)
	s.mu.Unlock()
	if !same {
		s.e.hub.publish(&Event{Type: evHarness, Run: s.run, Root: s.root, key: "harness:" + itoa(s.run), Data: json.RawMessage(b)})
	}
}

// activity sets what the agent is doing (published when it changes).
func (s *hsess) activity(kind, title string) {
	s.mu.Lock()
	changed := s.act.Kind != kind || s.act.Title != title
	if changed {
		s.act = hActivity{Kind: kind, Title: title, At: nowMs()}
	}
	s.mu.Unlock()
	if changed {
		s.publishSummary()
	}
}

func (s *hsess) activityNow() hActivity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.act
}

// harnessRunIDs are the harness runs recover() pokes: a session starting or
// live (joined against runs, so the row of a run an older binary deleted is
// ignored).
const harnessRecoverSQL = `SELECT h.run_id FROM harness_sessions h JOIN runs r ON r.id=h.run_id
	WHERE h.state IN ('starting','live','login') AND r.engine='harness'`

// harnessHasWork: a prompt is on its way or running, or an adapter runs at
// all (hasWork) — an idle one too: a process that exits with no successor
// leaves the resume job, and the next one attaches it and re-arms its idle
// reclaim (a blue/green successor clears the job as it takes over). A
// process can't tell a handoff from a last exit, so it never stops one
// itself (BeginShutdown).
const harnessWorkSQL = `(SELECT count(*) FROM harness_sessions h JOIN runs r ON r.id=h.run_id
	WHERE r.engine='harness' AND (h.prompt_state<>'' OR (h.exec_id<>'' AND h.state IN ('starting','live','login'))))`
