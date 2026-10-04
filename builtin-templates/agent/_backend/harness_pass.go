// harness_pass.go — a harness run's pass (D147 §3.2, §3.3): what its
// inbox asks of its coding agent, in priority order — cancel > stop >
// interrupt > halt > answers (approve, hanswer: harness_answer.go) >
// wake/retry > prompts > compact > the idle reclaim (harness_life.go). A
// prompt (an hprompt row) is delivered when no turn runs and nothing is
// parked: the user row (+ the task ledger) is written as it goes, and the
// turn is the adapter's until its turn.end (harness_map.go); one sent while
// a park waits rejects it first, one during a turn is steered or waits
// (harness_steer.go). The pass never waits for a turn: the actor exits and
// the turn's end pokes the run again.
//
// Also the ways in: POST /ask and POST /runs with `harness` (§4.2.3) — the
// run is created with engine "harness" and its first message as an hprompt —
// and a message to a harness run (POST /runs/{id}/message, /answer) queued
// as an hprompt. Older binaries ignore the new inbox kinds (sortInbox), so a
// harness run is never driven by an older process's model loop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// The harness run's own inbox kinds (inbox.go has the rest).
const (
	inboxHPrompt = "hprompt" // a message for the coding agent: prompted, steered or queued
	inboxHAnswer = "hanswer" // an answer to its parked question (POST /runs/{id}/harness/answer)
	inboxHNote   = "hnote"   // a notice for a parent about its harness child (never starts a turn)
)

// hInbox is a harness run's pending rows by kind. A `user` row (an older
// binary's message, a parent's) is a prompt too; the ones no person or
// parent wrote (a schedule's, a watcher's, /learn) are stray: a coding
// agent's conversation isn't driven by them (D147 §3.3).
type hInbox struct {
	prompt, answer, approve, wake, interrupt, cancel, compact, stop, swtch, stray []*InboxRow
}

func sortHarnessInbox(rows []*InboxRow) hInbox {
	var h hInbox
	for _, r := range rows {
		switch r.Kind {
		case inboxHPrompt:
			h.prompt = append(h.prompt, r)
		case inboxUser:
			switch r.Body.Source {
			case "", "human", "parent":
				h.prompt = append(h.prompt, r)
			default:
				h.stray = append(h.stray, r)
			}
		case inboxWatch:
			h.stray = append(h.stray, r)
		case inboxHAnswer:
			h.answer = append(h.answer, r)
		case inboxApprove:
			h.approve = append(h.approve, r)
		case inboxWake:
			h.wake = append(h.wake, r)
		case inboxInterrupt:
			h.interrupt = append(h.interrupt, r)
		case inboxCancel:
			h.cancel = append(h.cancel, r)
		case inboxCompact:
			h.compact = append(h.compact, r)
		case inboxHStop:
			h.stop = append(h.stop, r)
		case inboxHSwitch:
			h.swtch = append(h.swtch, r)
		}
	}
	return h
}

// nextPrompt is the prompt that goes next: one sent with interrupt (the
// caller's own jumps the queue), else the oldest.
func (h hInbox) nextPrompt() *InboxRow {
	for _, r := range h.prompt {
		if r.Body.Jump {
			return r
		}
	}
	if len(h.prompt) > 0 {
		return h.prompt[0]
	}
	return nil
}

// replyPrompt is the prompt that answers a park by replying: nextPrompt's
// choice among the ones a parent agent didn't send.
func (h hInbox) replyPrompt() *InboxRow {
	var own hInbox
	for _, r := range h.prompt {
		if r.Body.Source != "parent" {
			own.prompt = append(own.prompt, r)
		}
	}
	return own.nextPrompt()
}

// harnessPass is pass() for a harness run: cancel > stop > interrupt >
// halt > answers (approve, hanswer) > wake > prompts (a message while a
// park waits rejects it first; one during a turn is steered, else waits)
// > compact > the idle reclaim.
func (e *Engine) harnessPass(run *Run, rows []*InboxRow) {
	ctx := e.base
	if hs, _ := e.db.harnessSession(run.ID); hs != nil && hs.SteerRow != 0 {
		// a steer still marked on its way as a pass starts (passes are
		// serial: none of this process's is sending it) was let go without
		// knowing whether the turn took it — a predecessor's, cut off by
		// the handoff. Never sent again.
		e.steerUnsure(run, hs.SteerRow, "the backend was replaced while it was on its way")
		kept := rows[:0:0]
		for _, r := range rows {
			if r.ID != hs.SteerRow {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	h := sortHarnessInbox(rows)
	if len(h.cancel) > 0 {
		e.harnessCancel(ctx, run, h)
		return
	}
	if len(h.stop) > 0 {
		e.harnessStop(ctx, run, h.stop)
	}
	if len(h.swtch) > 0 && e.harnessSwitch(ctx, run, h.swtch) { // another sign-in picked (harness_creds.go)
		if run, _ = e.db.getRun(run.ID); run == nil {
			return
		}
	}
	if len(h.interrupt) > 0 {
		e.harnessInterrupt(ctx, run, h.interrupt)
	}
	if e.halted() {
		// a person's partition reads it from conf: a known halt cancels
		// the run, one not read yet parks it with a look again (brake.go);
		// elsewhere the run waits, as ever
		if !userMode() {
			return
		}
		if r, err := e.db.getRun(run.ID); err == nil {
			run = r
		}
		e.onBrake(run)
		e.harnessIdleUnderBrake(ctx, run.ID) // an idle one's reclaim moves no work: it goes on (harness_partition.go)
		return
	}
	if len(h.stray) > 0 {
		e.consumeWithNote(run, h.stray, "a coding agent's conversation takes messages from people — "+
			orStr(h.stray[0].Body.Source, h.stray[0].Kind)+" doesn't drive it")
	}
	if len(h.approve) > 0 || len(h.answer) > 0 {
		if r, err := e.db.getRun(run.ID); err == nil {
			run = r
		}
		if len(h.approve) > 0 {
			e.harnessApprove(ctx, run, h.approve)
		}
		if len(h.answer) > 0 {
			e.harnessAnswer(ctx, run, h.answer)
		}
	}
	hs, err := e.db.harnessSession(run.ID)
	if err != nil {
		return
	}
	if run, err = e.db.getRun(run.ID); err != nil {
		return
	}
	s := e.harnessOf(run.ID)
	if s != nil && s.detachedQuiet() { // codex's own turn went quiet: over (harness_steer.go)
		s.endDetached("end_turn")
		if run, err = e.db.getRun(run.ID); err != nil {
			return
		}
		hs, _ = e.db.harnessSession(run.ID)
	}
	parked := run.Status == statusWaiting
	turn := (hs != nil && hs.PromptState != "") || (s != nil && s.isDetached()) || detachedLeft(run, hs, s)
	switch {
	case len(h.wake) > 0:
		e.harnessWake(ctx, run, hs, h.wake)
		return
	case len(h.prompt) > 0 && parked:
		// a person's reply answers the park first; the parent agent's waits
		// for them — the parent model never answers a child's permission
		// (D147 §4.4) — and the pass goes on below: a successor still
		// takes a predecessor's adapter over
		if p := h.replyPrompt(); p != nil {
			e.harnessReplyToPark(ctx, run, p)
			return
		}
	case len(h.prompt) > 0 && turn && s != nil:
		// a turn being interrupted isn't steered: the message sent with the
		// interrupt (and any other) is the next prompt, at its end (§3.5)
		if p := h.nextPrompt(); !p.Body.Jump && len(h.interrupt) == 0 {
			e.harnessSteer(ctx, run, p)
		}
		return
	case len(h.prompt) > 0 && !turn:
		e.harnessPrompt(ctx, run, h.nextPrompt())
		return
	} // a turn a predecessor left running: attached below, its end pokes the run
	if len(h.compact) > 0 && !parked && !turn {
		e.harnessCompact(ctx, run, hs, h.compact)
		return
	}
	if s != nil && s.reclaimDue() {
		e.harnessReclaim(run, s, parked || turn)
		return
	}
	if s == nil && hs != nil && hs.ExecID != "" && hsRunning(hs.State) {
		e.resumeHarness(ctx, run, hs) // a predecessor's adapter: keep reading it
	}
}

// detachedLeft: a predecessor's adapter runs a turn of its own (codex's
// detached turn: detachedStored) that no session here follows yet —
// resumeHarness attaches it, and follows it.
func detachedLeft(run *Run, hs *harnessSession, s *hsess) bool {
	return s == nil && detachedStored(run, hs) && hs.ExecID != "" && hsRunning(hs.State)
}

// consumeWithNote consumes rows with a journal note.
func (e *Engine) consumeWithNote(run *Run, rows []*InboxRow, text string) {
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": text}))
		return nil
	})
	for _, r := range rows {
		e.delivered(r.ID)
	}
}

// resumeHarness takes over a live adapter a predecessor drove (recover()
// pokes its run): attached when its session was open (or it waits on its
// sign-in without one: attachable), else — it was still opening one —
// ended, for the next prompt to start a new generation. An attach its
// manager didn't answer (it restarts too) is tried again by a one-shot
// timer, backing off (hAttachRetry): until it takes, the exec turns out
// gone (the pipe ends Lost, the turn with it) or the rights are refused
// (stopHarnessNow).
func (e *Engine) resumeHarness(ctx context.Context, run *Run, hs *harnessSession) {
	if _, ok := attachable(hs); ok {
		_, err := e.ensureHarnessAt(ctx, run, true)
		switch {
		case err == nil, ctx.Err() != nil, errors.Is(err, errHandoff), errors.Is(err, errFenced):
		case isHarnessFail(err): // stopped (ensureHarnessAt): nothing to try again
			e.resetAttachRetry(run.ID)
			logf("run #%d: taking over its coding agent: %v", run.ID, err)
		default:
			d := e.retryAttach(run.ID)
			logf("run #%d: taking over its coding agent: %v — trying again in %s", run.ID, err, d)
		}
		return
	}
	cfg, err := e.db.runConfig(run.ID)
	if err != nil || cfg.Harness == nil {
		return
	}
	if hs.State == hsLogin { // a sign-in park with no session to take over: left, as a Retry leaves it
		e.leaveLogin(ctx, run, hs)
		return
	}
	e.dropExec(ctx, run, cfg, hs)
	_ = e.fenced(func(t *DB) error {
		cur, _ := t.harnessSession(run.ID)
		if cur == nil || cur.Gen != hs.Gen {
			return nil
		}
		cur.State = hsStopped
		sending := cur.PromptState != "" // never sent by a session that hadn't opened (a wake counts it: harness_partition.go)
		cur.PromptState, cur.PromptRPC = "", ""
		if err := t.putHarnessSession(cur); err != nil || !sending {
			return err
		}
		return e.endHarnessTurnTx(t, run.ID, "error", "the backend was replaced while your message was on its way to "+
			t.harnessRunName(run.ID, cfg.Harness.Provider)+" — send it again")
	})
}

// hAttachRetry is a takeover's first wait before it tries an unanswered
// attach again, doubling to hAttachRetryMax (vars: tests).
var hAttachRetry, hAttachRetryMax = 2 * time.Second, time.Minute

// retryAttach arms run's timer for the next attach (the pass reaches
// resumeHarness again) and says when.
func (e *Engine) retryAttach(run int64) time.Duration {
	e.mu.Lock()
	if e.hretry == nil {
		e.hretry = map[int64]int{}
	}
	n := e.hretry[run]
	e.hretry[run] = n + 1
	e.mu.Unlock()
	d := min(hAttachRetry<<min(n, 10), hAttachRetryMax)
	e.armTimer(run, e.unix()+int64((d+time.Second-1)/time.Second))
	return d
}

// resetAttachRetry: run's adapter is taken over (or stopped) — a later
// unanswered attach waits hAttachRetry again.
func (e *Engine) resetAttachRetry(run int64) {
	e.mu.Lock()
	delete(e.hretry, run)
	e.mu.Unlock()
}

// --- prompts ----------------------------------------------------------------------------

// heldPrompt is a prompt kept for a retry (harness_sessions.held): it was
// shown as a user row, and didn't reach the adapter.
type heldPrompt struct {
	Text   string   `json:"text"`
	Files  []string `json:"files,omitempty"`
	Sender string   `json:"sender,omitempty"`
}

// promptText is what the adapter is sent for a message: its text and, for
// its files, their names (the files stay the conversation's; D147
// §3.6's resource links come with the sign-in and files work).
func promptText(text string, files []*ReplFile) string {
	if text == "" && len(files) > 0 {
		text = "(see attached)"
	}
	return text + attachmentNote(files)
}

// harnessPrompt delivers row: the session ensured (and the sandbox's use
// re-checked), the user row written with the prompt marked on its way,
// then session/prompt. A session that can't be had fails the turn with
// why, the prompt kept for /resume; an adapter that isn't signed in parks
// the run on its sign-in with the prompt held (§4.3.4 login).
func (e *Engine) harnessPrompt(ctx context.Context, run *Run, row *InboxRow) {
	s, serr := e.ensureHarness(ctx, run)
	if ctx.Err() != nil || errors.Is(serr, errHandoff) || errors.Is(serr, errFenced) {
		return
	}
	if serr == nil {
		serr = e.harnessRights(ctx, run, s)
	}
	var m *Message
	ok := false
	ferr := e.fenced(func(t *DB) error {
		var err error
		if m, err = e.userRowTx(t, run, row); err != nil {
			return err
		}
		hs, _ := t.harnessSession(run.ID)
		if hs == nil {
			hs = &harnessSession{RunID: run.ID, RootID: rootOf(run)}
		}
		hs.TurnSeq = int64(m.Seq) // the turn's answer is what comes after it
		held := &heldPrompt{Text: m.Content, Sender: row.Body.Sender}
		if errors.Is(serr, errHarnessLogin) { // signed out: the sign-in first, the prompt held
			if err := s.loginTx(t, hs, held); err != nil {
				return err
			}
			return t.putHarnessSession(hs)
		}
		if serr != nil { // no session: the turn fails, the prompt is kept for a retry
			b, _ := json.Marshal(held)
			hs.Held = string(b)
			if err := t.putHarnessSession(hs); err != nil {
				return err
			}
			return e.endHarnessTurnTx(t, run.ID, "error", serr.Error())
		}
		hs.PromptState, hs.PromptRPC, hs.Held = "sending", "", ""
		if err := t.putHarnessSession(hs); err != nil {
			return err
		}
		if _, err := t.q.Exec(`UPDATE runs SET status=?, pending='', turn_started=?, settled_at=0, outcome='', updated=? WHERE id=?`,
			statusRunning, e.unix(), e.unix(), run.ID); err != nil {
			return err
		}
		if run.ParentID == 0 {
			t.bumpActivity(run.ID)
		}
		e.emitRun(t, run.ID)
		ok = true
		return nil
	})
	e.delivered(row.ID)
	if ferr != nil || !ok {
		if errors.Is(serr, errHarnessLogin) && s != nil {
			s.publishSummary()
		}
		return
	}
	e.sendPrompt(ctx, s, run, m.Content)
}

// sendPrompt starts the turn at the adapter; a prompt that can't go ends
// it with why.
func (e *Engine) sendPrompt(ctx context.Context, s *hsess, run *Run, text string) {
	s.toWork(true) // at work: it keeps a person's partition up (harness_partition.go)
	s.setInflight(&heldPrompt{Text: text})
	s.activity("thinking", "")
	err := s.c.Prompt(ctx, acp.Prompt{Text: text})
	if err == nil || ctx.Err() != nil || s.isHalted() {
		return
	}
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
		if hs.PromptState == "" {
			return nil
		}
		hs.PromptState, hs.PromptRPC = "", ""
		return e.endHarnessTurnTx(t, run.ID, "error", fmt.Sprintf("couldn't send the message to %s: %v", s.prov.Name, err))
	})
	s.activity("idle", "")
}

// harnessWake is /resume: a fresh adapter when none is live (it reads its
// sign-in again — one parked on its sign-in is ended first) and the held
// prompt sent again, without another user row.
func (e *Engine) harnessWake(ctx context.Context, run *Run, hs *harnessSession, rows []*InboxRow) {
	e.consumeRows(rows)
	if hs != nil && (hs.State == hsLogin || parsePending(run.Pending).Kind == "login") {
		e.leaveLogin(ctx, run, hs)
		var err error
		if run, err = e.db.getRun(run.ID); err != nil {
			return
		}
		hs, _ = e.db.harnessSession(run.ID)
	}
	if run.Status == statusWaiting || (hs != nil && hs.PromptState != "") {
		return // a turn or a park is in force: nothing to resume
	}
	s, serr := e.ensureHarness(ctx, run)
	if ctx.Err() != nil || errors.Is(serr, errHandoff) || errors.Is(serr, errFenced) {
		return
	}
	if serr == nil {
		serr = e.harnessRights(ctx, run, s)
	}
	var held heldPrompt
	if hs != nil && hs.Held != "" {
		_ = json.Unmarshal([]byte(hs.Held), &held)
	}
	sent := false
	_ = e.fenced(func(t *DB) error {
		cur, _ := t.harnessSession(run.ID)
		if errors.Is(serr, errHarnessLogin) && cur != nil {
			if err := s.loginTx(t, cur, nil); err != nil {
				return err
			}
			return t.putHarnessSession(cur)
		}
		if serr != nil {
			if run.Status != statusError {
				return e.endHarnessTurnTx(t, run.ID, "error", serr.Error())
			}
			e.emitStep(t, rootOf(run), t.journal(run.ID, "error", map[string]string{"error": serr.Error()}))
			return nil
		}
		if held.Text == "" || cur == nil {
			if run.Status == statusError {
				if err := t.setStatus(run.ID, statusIdle, 0, "", ""); err != nil {
					return err
				}
				e.emitRun(t, run.ID)
			}
			return nil
		}
		cur.PromptState, cur.PromptRPC, cur.Held = "sending", "", ""
		if err := t.putHarnessSession(cur); err != nil {
			return err
		}
		if err := t.setStatus(run.ID, statusRunning, 0, "", ""); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		sent = true
		return nil
	})
	if errors.Is(serr, errHarnessLogin) && s != nil {
		s.publishSummary()
	}
	if sent {
		e.sendPrompt(ctx, s, run, held.Text)
	}
}

// --- stops -----------------------------------------------------------------------------

// harnessProviderOf is run id's coding agent's id ("" for a built-in run).
func harnessProviderOf(d *DB, id int64) string {
	if cfg, err := d.runConfig(id); err == nil && cfg.Harness != nil {
		return cfg.Harness.Provider
	}
	return ""
}

// harnessAdapterUp: r is a coding agent's run whose adapter runs (or is
// starting, or waits for a sign-in) — a cancel has it to stop even while
// the conversation rests (§4.2.11).
func (d *DB) harnessAdapterUp(r *Run) bool {
	if r.Engine != engineHarness {
		return false
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM harness_sessions WHERE run_id=? AND exec_id<>'' AND state IN ('starting','live','login')`, r.ID).Scan(&n)
	return n > 0
}

// harnessCancel is the durable stop: session/cancel, the adapter ended,
// the run canceled (a child's link settles canceled). A run that rests (no
// turn, no park: an idle adapter) keeps its status — there is no turn to
// cancel — and only its adapter stops (state stopped, as the idle reclaim
// leaves it; the next message starts a new one).
func (e *Engine) harnessCancel(ctx context.Context, run *Run, h hInbox) {
	reason := "cancelled"
	if h.cancel[0].Body.Reason != "" {
		reason = h.cancel[0].Body.Reason
	}
	stopped := false
	if s := e.harnessOf(run.ID); s != nil {
		_ = s.c.Cancel()
		s.stop()
		stopped = true
	} else if hs, _ := e.db.harnessSession(run.ID); hs != nil && hs.ExecID != "" && hsExecMayRun(hs.State) {
		if cfg, err := e.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
			stopped = hsRunning(hs.State)
		}
	}
	s := &hsess{e: e, run: run.ID, root: rootOf(run)}
	_ = e.fenced(func(t *DB) error {
		for _, r := range append(h.cancel, h.interrupt...) {
			t.consume(r.ID, 0)
		}
		if hs, _ := t.harnessSession(run.ID); hs != nil {
			hs.PromptState, hs.PromptRPC, hs.Queue, hs.Login = "", "", "", ""
			if hsRunning(hs.State) {
				hs.State = hsStopped
			}
			if err := t.putHarnessSession(hs); err != nil {
				return err
			}
		}
		if !active(run.Status) {
			if stopped {
				e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": fmt.Sprintf(
					"%s stopped (%s) — the next message starts it again", t.harnessRunName(run.ID, harnessProviderOf(t, run.ID)), reason)}))
				e.emitRun(t, run.ID)
			}
			return nil
		}
		s.settleParkTx(t, run, "("+reason+")")
		if err := t.setStatus(run.ID, statusCanceled, 0, reason, ""); err != nil {
			return err
		}
		e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": reason}))
		if run.ParentID != 0 {
			e.settleOwnLink(t, run, linkCanceled, outcomeCanceled, reason)
		}
		runTurnEnd(t, run, turnCanceled, outcomeCanceled, reason) // turnEndHooks (project_events.go)
		e.emitRun(t, run.ID)
		return nil
	})
}

// --- the ways in -----------------------------------------------------------------------

// harnessReq is a new conversation's `harness` (§4.2.3).
type harnessReq struct {
	Provider string            `json:"provider"`
	Mode     string            `json:"mode"`
	Options  map[string]string `json:"options"`
}

// harnessClass checks a new harness conversation's request and picks its
// class: the one named (it must allow the harness), else the caller's
// default when it does, else the first class they may use that does. It
// also resolves req.Mode — the caller's own setting (§4.3.12) mapped to the
// provider's mode, or a mode they named: one the catalog doesn't know to be
// safe (an explicit one, one it doesn't list, any of a harness it lacks)
// only from a person — the conversation's owner-to-be (default-deny).
func harnessClass(ctx context.Context, c who, req *harnessReq, class, system, model string) (agentClass, error) {
	if globalMode() { // a person's own conversations only (harness_partition.go)
		return agentClass{}, &errClass{http.StatusConflict, harnessNotAtGlobal}
	}
	st := currentClasses()
	req.Provider = strings.TrimSpace(req.Provider)
	prov, ok := knownHarness(ctx, req.Provider)
	switch {
	case req.Provider == "" || !ok:
		return agentClass{}, &errClass{400, fmt.Sprintf("harness.provider: no coding agent %q (GET /harnesses lists them)", req.Provider)}
	case strings.TrimSpace(system) != "":
		return agentClass{}, &errClass{400, "system: a coding agent keeps its own instructions — system is for the built-in agent"}
	case strings.TrimSpace(model) != "":
		return agentClass{}, &errClass{400, "model: a coding agent's model is harness.options.model"}
	}
	name := prov.Name
	if req.Mode != "" {
		var m *acp.Mode
		var ids []string
		for i := range prov.Modes {
			ids = append(ids, prov.Modes[i].ID)
			if prov.Modes[i].ID == req.Mode {
				m = &prov.Modes[i]
			}
		}
		switch {
		case m == nil && len(prov.Modes) > 0:
			return agentClass{}, &errClass{400, "harness.mode: one of " + strings.Join(ids, ", ")}
		case !prov.Safe(req.Mode) && (c.kind != whoUser || c.viewedBy != ""): // a person (not an admin viewing as one)
			mname := req.Mode
			if m != nil {
				mname = m.Name
			}
			return agentClass{}, &errClass{403, fmt.Sprintf("only a person can start %s in %s", name, mname)}
		}
	} else {
		req.Mode = prov.ApproveMode
		if c.kind == whoUser && agent.db.harnessMode(c.user, prov.ID) == hmAuto && prov.AutoMode != "" {
			req.Mode = prov.AutoMode
		}
	}
	var catalogOpts []acp.ConfigOption
	_ = json.Unmarshal(agent.db.harnessOptions(prov.ID), &catalogOpts)
	for k := range req.Options {
		mode := k == "mode"
		for _, o := range catalogOpts {
			mode = mode || (o.ID == k && o.Category == "mode")
		}
		if mode {
			return agentClass{}, &errClass{400, "harness.options: the mode is harness.mode"}
		}
	}
	if id := strings.TrimSpace(class); id != "" {
		cls, ok := st.find(id)
		switch {
		case !ok:
			return cls, &errClass{400, fmt.Sprintf("class: no class %q (GET /classes lists them)", id)}
		case !cls.usableBy(c):
			return cls, &errClass{403, fmt.Sprintf("the %s class is for the agent's managers", cls.Name)}
		case !cls.allowsHarness(prov.ID):
			return cls, &errClass{400, fmt.Sprintf("class: the %s class doesn't allow %s", cls.Name, name)}
		}
		return cls, nil
	}
	if def := st.defaultFor(c); def.allowsHarness(prov.ID) {
		return def, nil
	}
	for _, cls := range st.list {
		if cls.usableBy(c) && cls.allowsHarness(prov.ID) {
			return cls, nil
		}
	}
	return agentClass{}, &errClass{403, fmt.Sprintf("no class you may use allows %s", name)}
}

// harnessApply makes cfg a harness conversation's: its sandbox (bound by
// askSandbox) must have the harness and reach out; a running one is probed
// (cached) and refused when the harness is missing. False once it answered.
func harnessApply(w http.ResponseWriter, r *http.Request, cfg *Config, req *harnessReq) bool {
	ctx := r.Context()
	c := callerOf(r)
	prov, _ := knownHarness(ctx, req.Provider)
	if cfg.Sandbox == nil {
		writeHarnessErr(w, 400, fmt.Sprintf("a coding agent needs a sandbox: sandbox {ref, cwd?} whose image has %s", prov.Name))
		return false
	}
	b := *cfg.Sandbox
	name := orStr(b.Name, b.Ref)
	provider, _, _ := splitSandboxRef(b.Ref)
	if m, ok := boundManager(provider); ok {
		if h, err := managerHello(ctx, m); err == nil && h.advertises() && !hasStr(h.imageHarnesses(b.Image), prov.ID) {
			writeHarnessErr(w, 409, fmt.Sprintf("%s's image doesn't have %s", name, prov.Name))
			return false
		} else if err == nil {
			probeHarnesses(ctx, c, b.Ref, []hcManager{{m: m, hello: h}})
		}
	}
	if b.Egress == "none" {
		writeHarnessErr(w, 409, fmt.Sprintf("%s must reach its provider — %s's egress is none", prov.Name, name))
		return false
	}
	if seen := agent.db.seenHarnesses(prov.ID)[b.Ref]; seen.Installed != nil && !*seen.Installed {
		writeHarnessErr(w, 409, fmt.Sprintf("%s doesn't have %s (%s not found)", name, prov.Name, strings.Join(prov.Bins, ", ")))
		return false
	}
	cfg.Engine = engineHarness
	cfg.Harness = &HarnessConfig{Provider: prov.ID, Mode: req.Mode, Options: req.Options, Ref: b.Ref, Cwd: b.Cwd, By: c.tag()}
	return true
}

func writeHarnessErr(w http.ResponseWriter, code int, msg string) {
	writeClassErr(w, &errClass{code, msg})
}

// interruptHarness queues an interrupt of a coding agent's running turn (a
// message sent with interrupt: true).
func (ag *Agent) interruptHarness(run *Run) {
	if !active(run.Status) {
		return
	}
	if _, _, err := ag.queue(run.ID, inboxInterrupt, inboxBody{Reason: "interrupted by the owner"}, ""); err != nil {
		logf("run #%d: queueing an interrupt: %v", run.ID, err)
	}
}

// endHarnesses stops the coding agents of run id and every run below it
// (a conversation being deleted): the ones this process drives closed,
// another's adapter ended at its manager — before their rows go.
func (e *Engine) endHarnesses(ctx context.Context, id int64) {
	ids := []int64{id}
	if kids, err := e.db.descendants(id); err == nil {
		ids = append(ids, kids...)
	}
	for _, rid := range ids {
		e.endHarness(ctx, rid)
	}
}

// endHarness stops run rid's coding agent and stores it stopped, under its
// start lock: no pass takes it over again meanwhile.
func (e *Engine) endHarness(ctx context.Context, rid int64) {
	if run, err := e.db.getRun(rid); err != nil || run.Engine != engineHarness {
		return // a run's engine never changes: no start lock for a built-in run's delete
	}
	mu := e.harnessLock(rid)
	mu.Lock()
	defer mu.Unlock()
	e.resetAttachRetry(rid)
	run, err := e.db.getRun(rid)
	if err != nil {
		return
	}
	hs, _ := e.db.harnessSession(rid)
	if s := e.harnessOf(rid); s != nil {
		s.stop()
	} else if hs != nil && hs.ExecID != "" && hsExecMayRun(hs.State) {
		if cfg, err := e.db.runConfig(rid); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
		}
	}
	_ = e.fenced(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE harness_sessions SET state=?, prompt_state='', prompt_rpc='', updated_ms=? WHERE run_id=? AND state IN ('starting','live','login')`,
			hsStopped, nowMs(), rid)
		return err
	})
}
