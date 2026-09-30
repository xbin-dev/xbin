// harness_engine.go — the coding agents this process drives (D-harness §3.3):
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

	mu      sync.Mutex
	halted  bool    // let go (a handoff, a stop): nothing more is applied or written
	draft   hsDraft // text and thinking read, not yet flushed to a row
	calls   map[string]*hcall
	dirty   map[string]bool // calls whose in-memory tool row isn't published yet
	timer   *time.Timer     // the ≤ 250 ms upsert of dirty tool rows
	act     hActivity
	lastSum string // the harness event last published

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
}

// newHsess is a session of run at generation gen, started (or attached)
// under epoch.
func newHsess(e *Engine, run *Run, gen int, epoch int64, prov acp.Provider) *hsess {
	return &hsess{e: e, run: run.ID, root: rootOf(run), parent: run.ParentID, gen: gen, epoch: epoch, prov: prov,
		perms: acp.NewPermissions(), done: make(chan struct{}), calls: map[string]*hcall{}, dirty: map[string]bool{},
		act: hActivity{Kind: "idle", At: nowMs()}, abandoned: map[string]string{}, checked: time.Now(), fresh: true}
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
	var cur int64
	_ = e.db.q.QueryRow(`SELECT CAST(v AS INTEGER) FROM settings WHERE k='engine_epoch'`).Scan(&cur)
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
		var st acp.SessionState
		if json.Unmarshal([]byte(hs.Snapshot), &st) == nil && st.SessionID != "" {
			s, err := e.attachHarness(ctx, run, cfg, hs, st)
			if err == nil || !isHarnessFail(err) {
				return s, err
			}
			return nil, e.failHarness(run, err)
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
		hs.ReadOff, hs.ErrOff, hs.Draft = 0, 0, ""
		hs.Shared = sandboxShared(u.Box)
		hs.LastActiveMs = nowMs()
		return t.putHarnessSession(hs)
	})
	if err != nil {
		return nil, err
	}
	tg := harnessTarget(u)
	tg.Guard, tg.Dropped = s.guard, s.dropped
	pipe, err := startHarnessPipe(ctx, tg, hpSpawn{Run: run.ID, Root: rootOf(run), Gen: gen, Provider: prov.ID,
		Argv: prov.Argv, Cwd: cwd, Env: prov.Env})
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
	s.c = acp.NewWith(acp.ClientOptions{Caps: harnessCaps(), IDPrefix: fmt.Sprintf("h%d.%d", run.ID, gen), AwaitLogin: true})
	go s.consume()
	opts := map[string]string{}
	for k, v := range h.Options {
		if k != "mode" {
			opts[k] = v
		}
	}
	acfg := acp.Config{Provider: prov, Mode: h.Mode, Options: opts, ResumeID: resume, Cwd: cwd, Argv: prov.Argv,
		Perms: s.perms, Log: s.log, Spawn: func(context.Context, acp.Config) (*acp.Process, error) { return s.process(), nil }}
	if err := s.c.Start(ctx, acfg); err != nil {
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
		if wasLogin || st.AuthNeeded {
			return s.loginTx(t, hs, nil)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if !wasLogin && !st.AuthNeeded {
		signed := true
		_ = e.db.noteHarnessSeen(h.Ref, prov.ID, &signed, &signed)
	}
	s.publishSummary()
	s.armIdle()
	return s, nil
}

func isAuthErr(err error) bool {
	var re *acp.Error
	return errors.As(err, &re) && re.Code == acp.CodeAuthRequired
}

// sandboxShared: others may use the sandbox (team visibility, members or
// shares) — the privacy note (§4.3.2 sandbox.shared).
func sandboxShared(b *sbxSandbox) bool {
	if b == nil {
		return false
	}
	sh := strings.TrimSpace(string(b.Shares))
	return b.Visibility == "team" || len(b.Members) > 0 || b.Shared || (sh != "" && sh != "null" && sh != "[]" && sh != "{}")
}

// dropExec ends an adapter a predecessor left half-started (best effort).
func (e *Engine) dropExec(ctx context.Context, run *Run, cfg Config, hs *harnessSession) {
	u, _, err := e.harnessUse(ctx, run, cfg)
	if err != nil {
		return
	}
	attachHarnessPipe(ctx, harnessTarget(u), hs.ExecID, hs.ReadOff, hs.ErrOff).Kill()
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
// than the offset) and the permissions still pending restored.
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
	s.c = acp.NewWith(acp.ClientOptions{Caps: harnessCaps(), AwaitLogin: true, Attach: &st,
		IDPrefix: fmt.Sprintf("h%d.%de%da%d", run.ID, hs.Gen, epoch, seq)})
	go s.consume()
	acfg := acp.Config{Provider: prov, Mode: cfg.Harness.Mode, Cwd: hs.Cwd, Argv: prov.Argv, Perms: s.perms, Log: s.log,
		Spawn: func(context.Context, acp.Config) (*acp.Process, error) { return s.process(), nil }}
	if err := s.c.Start(ctx, acfg); err != nil {
		s.stop()
		return nil, err
	}
	if !s.draft.empty() {
		s.showDraft()
	}
	s.publishSummary()
	if hs.PromptState == "" && run.Status != statusWaiting && run.Status != statusRunning && hs.State == hsLive {
		s.armIdle()
	}
	return s, nil
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
	p.Stdout = &hsStdout{Reader: p.Stdout, s: s}
	return p
}

type hsStdin struct {
	io.WriteCloser
	s *hsess
}

func (w *hsStdin) Write(b []byte) (int, error) {
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

func (s *hsess) log(line string) { logf("run #%d: %s: %s", s.run, s.prov.Name, line) }

// consume applies the session's events in order until the client's stream
// ends — then the adapter is gone (unless this process let it go).
func (s *hsess) consume() {
	defer close(s.done)
	for ev := range s.c.Events() {
		if s.isHalted() {
			continue // drained, not applied: the successor reads it again
		}
		s.touchDetached()
		s.apply(ev)
		if ev.Type != acp.EvMessageDelta && ev.Type != acp.EvThoughtDelta {
			s.recheckSoon()
		}
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
		hs.State, hs.PromptState, hs.PromptRPC, hs.Queue, hs.Login = hsStopped, "", "", "", ""
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
// through ev (its frame; nil or 0: the one before stays), the stderr
// offset, the snapshot, the unflushed draft and the in-memory tool rows —
// in one fenced transaction. Nothing is written once the session was let go
// or a newer generation owns the row.
func (s *hsess) commit(ev *acp.Event, fn func(t *DB, hs *harnessSession) error) error {
	var snap []byte
	if s.c != nil {
		snap, _ = json.Marshal(s.c.State())
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
		res := t.lastAssistant(run.ID)
		if status == statusError {
			res = msg
		}
		e.settleOwnLink(t, run, linkState, outcome, res)
	}
	e.emitRun(t, run.ID)
	if status == statusError {
		t.AfterCommit(func() { publishEvent(run.ID, "error") })
	}
	t.AfterCommit(func() { e.Poke(run.ID) }) // what is queued goes next
	return nil
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

// harnessHasWork: a prompt is on its way or running (hasWork).
const harnessWorkSQL = `(SELECT count(*) FROM harness_sessions h JOIN runs r ON r.id=h.run_id
	WHERE h.prompt_state<>'' AND r.engine='harness')`
