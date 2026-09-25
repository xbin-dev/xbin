// actor.go — one look at a run (a pass) and the turn it may lead to.
//
// A pass reads the run and its undelivered inbox and decides, by status, what
// the run needs: nothing (it parks — a timer is armed if it waits for a
// time), a state change (an approval, a resolved wait), or a turn. Inputs are
// handled in priority order: cancel > interrupt > approve/deny > awaited
// subagents and deadlines > messages (the steer) > compaction > wake.
//
// A TURN is the model working: step after step (model call → tools) until it
// answers without calling a tool, parks (ask_user, yield, approval, waiting on
// subagents) or ends (finish, error, the step cap). Messages that arrive
// meanwhile are delivered at the next step boundary — after the tool results
// of the step in flight, before the next model call — which is the only place
// the transcript can take a user message without breaking the provider's
// "tool results immediately follow their calls" rule.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// pendingState is runs.pending: what a parked run is parked on.
type pendingState struct {
	Kind      string      `json:"kind"`                // approval | await | deps
	ToolCalls []toolCall  `json:"toolCalls,omitempty"` // approval: the parked calls
	Waits     []waitEntry `json:"waits,omitempty"`     // await: subagent_wait calls
}

// waitEntry is one subagent_wait call the run is parked on.
type waitEntry struct {
	TC       string  `json:"tc"`
	Links    []int64 `json:"links"`
	Need     string  `json:"need"` // all | any
	Deadline int64   `json:"deadline"`
}

func parsePending(s string) pendingState {
	var p pendingState
	if s != "" {
		_ = json.Unmarshal([]byte(s), &p)
	}
	return p
}

// turnState is what a turn carries between steps.
type turnState struct {
	run  *Run
	cfg  Config
	root int64
	mcp  []toolSpec
	own  map[string]bool // tools whose OWN schema has a summary field
	// waits registered by subagent_wait calls in the current step.
	waits []waitEntry
	// spawnedThisStep counts subagent_spawn calls in the current step (the cap).
	spawnedThisStep int
	lastLatency     time.Duration
	back            map[string]string // wire tool name → internal (this step)
}

// --- the pass ------------------------------------------------------------------

func (e *Engine) pass(a *actor) {
	run, err := e.db.getRun(a.id)
	if err != nil {
		e.disarmTimer(a.id)
		return // deleted
	}
	if run.Status == statusQueued || run.Status == statusBlocked ||
		(run.CancelReq != 0 && run.SettledAt == 0 && !resting(run.Status)) {
		if e.fenced(func(t *DB) error { return t.normalizeLegacy(run) }) != nil {
			return
		}
		if run, err = e.db.getRun(a.id); err != nil {
			return
		}
	}
	in := sortInbox(e.db.undelivered(run.ID))

	if len(in.cancel) > 0 {
		e.stopRun(run, in, statusCanceled)
		return
	}
	if len(in.interrupt) > 0 {
		e.stopRun(run, in, statusIdle)
		if run, err = e.db.getRun(a.id); err != nil {
			return
		}
		in = sortInbox(e.db.undelivered(run.ID))
	}
	// The owner's brake: nothing starts or continues while it is on. A human
	// message clears it before it is queued (resumeIfHalted), so a prompt is
	// never swallowed.
	if e.halted() {
		return
	}

	switch run.Status {
	case statusWaiting:
		p := parsePending(run.Pending)
		if p.Kind == "approval" {
			if len(in.approve) > 0 {
				e.turn(a, run, in.approve[len(in.approve)-1])
				return
			}
			if len(in.user) > 0 {
				// Replying instead of approving DENIES the parked calls — the
				// transcript must answer them before the reply is delivered.
				if e.denyParked(run, p, "(not executed: you replied instead of approving — ask again if you still need it)") {
					e.turn(a, run, nil)
				}
			}
			return
		}
		e.consumeStale(run, in.approve)
		if len(in.user) > 0 || len(in.wake) > 0 {
			if e.setRunning(run, in.wake) {
				e.turn(a, run, nil)
			}
		}
		return

	case statusAwait:
		if e.resolveAwait(run, len(in.user) > 0) {
			e.turn(a, run, nil)
		}
		return

	case statusSleep:
		if run.WakeAt <= e.unix() || len(in.user) > 0 || len(in.wake) > 0 || len(in.watch) > 0 ||
			(run.ParentID == 0 && e.db.hasNotices(run.ID)) {
			if e.setRunning(run, in.wake) {
				e.turn(a, run, nil)
			}
			return
		}
		e.armTimer(run.ID, run.WakeAt)
		return

	case statusRunning:
		e.consumeStale(run, in.approve)
		e.turn(a, run, nil)
		return
	}

	// Resting: idle, done, canceled, error.
	e.consumeStale(run, in.approve)
	wantTurn := len(in.user) > 0 || len(in.wake) > 0 || len(in.watch) > 0 ||
		(run.Status != statusError && run.ParentID == 0 && e.db.hasNotices(run.ID))
	if wantTurn {
		if e.startTurn(run, in.wake) {
			e.turn(a, run, nil)
		}
		return
	}
	if len(in.compact) > 0 {
		e.compactNow(run, in.compact)
	}
}

// consumeStale drops approve rows that no longer apply (the approval was
// already decided some other way).
func (e *Engine) consumeStale(run *Run, rows []*InboxRow) {
	if len(rows) == 0 {
		return
	}
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		return nil
	})
}

// setRunning moves a parked run back to running (a wake, an answer).
func (e *Engine) setRunning(run *Run, wakes []*InboxRow) bool {
	err := e.fenced(func(t *DB) error {
		for _, r := range wakes {
			t.consume(r.ID, 0)
		}
		if err := t.setStatus(run.ID, statusRunning, 0, run.Result, ""); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		return nil
	})
	if err == nil {
		e.disarmTimer(run.ID)
		run.Status = statusRunning
	}
	return err == nil
}

// startTurn opens a new turn on a resting run.
func (e *Engine) startTurn(run *Run, wakes []*InboxRow) bool {
	err := e.fenced(func(t *DB) error {
		for _, r := range wakes {
			t.consume(r.ID, 0)
		}
		if _, err := t.q.Exec(`UPDATE runs SET status=?, wake_at=0, pending='', turn_steps=0, turn_started=?,
			settled_at=0, outcome='', cancel_req=0, updated=? WHERE id=?`, statusRunning, e.unix(), e.unix(), run.ID); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		return nil
	})
	if err == nil {
		run.Status = statusRunning
		run.TurnSteps = 0
	}
	return err == nil
}

// stopRun applies an interrupt (→ idle) or a cancel (→ canceled): whatever
// the run was doing is abandoned, and every unsettled tool result says so.
func (e *Engine) stopRun(run *Run, in inboxSet, to string) {
	reason := "interrupted by the owner"
	rows := in.interrupt
	if to == statusCanceled {
		reason = "cancelled"
		rows = append(in.cancel, in.interrupt...)
		if len(in.cancel) > 0 && in.cancel[0].Body.Reason != "" {
			reason = in.cancel[0].Body.Reason
		}
	}
	wasActive := active(run.Status)
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		if !wasActive {
			return nil // nothing in flight; a resting run stays as it is
		}
		// Every unsettled result in the transcript — running tools, parked
		// approvals, waits — is answered now, so the transcript stays valid.
		e.settlePlaceholders(t, run, "("+reason+")")
		_, _ = t.q.Exec(`UPDATE links SET delivered=1 WHERE parent_id=? AND mode='fg' AND delivered=0`, run.ID)
		if err := t.setStatus(run.ID, to, 0, reason, ""); err != nil {
			return err
		}
		e.emitStep(t, run.RootID, t.journal(run.ID, "note", map[string]string{"text": reason}))
		if run.ParentID != 0 {
			outcome := outcomeCanceled
			if to == statusIdle {
				outcome = outcomeInterrupted
			}
			e.settleOwnLink(t, run, linkCanceled, outcome, reason)
		}
		e.finishWatchRound(t, run)
		e.emitRun(t, run.ID)
		return nil
	})
	e.disarmTimer(run.ID)
}

// settlePlaceholders answers every unsettled tool result of a run.
func (e *Engine) settlePlaceholders(t *DB, run *Run, text string) {
	rows, err := t.q.Query(`SELECT id, content FROM messages WHERE run_id=? AND role='tool'`, run.ID)
	if err != nil {
		return
	}
	type pr struct {
		id int64
		c  string
	}
	var ps []pr
	for rows.Next() {
		var p pr
		if rows.Scan(&p.id, &p.c) == nil && isPlaceholder(p.c) {
			ps = append(ps, p)
		}
	}
	rows.Close()
	for _, p := range ps {
		_ = t.rewriteMessage(run.ID, p.id, text)
		e.emitMessageID(t, rootOf(run), run.ID, p.id)
	}
}

// denyParked answers a parked approval's calls with a refusal and resumes.
func (e *Engine) denyParked(run *Run, p pendingState, text string) bool {
	err := e.fenced(func(t *DB) error {
		for _, tc := range p.ToolCalls {
			if ok, _ := t.casToolResult(run.ID, tc.ID, text); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, rootOf(run), run.ID, id)
				}
			}
		}
		e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": "pending tool call(s) denied"}))
		if err := t.setStatus(run.ID, statusRunning, 0, "", ""); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		return nil
	})
	if err == nil {
		run.Status, run.Pending = statusRunning, ""
	}
	return err == nil
}

// --- the turn ------------------------------------------------------------------

// turn runs steps until the run parks, ends or is stopped. approval, when set,
// is a verdict row for the parked approval: consumed (with pending cleared
// and the calls marked running, in one transaction — so an approved call runs
// at most once even across a crash) before anything else happens.
func (e *Engine) turn(a *actor, run *Run, approval *InboxRow) {
	ctx, cancel := context.WithCancelCause(e.base)
	e.setStepCancel(run.ID, cancel)
	defer func() {
		e.setStepCancel(run.ID, nil)
		cancel(nil)
	}()
	cfg, err := e.db.runConfig(run.ID)
	if err != nil {
		return
	}
	ts := &turnState{run: run, cfg: cfg, root: rootOf(run)}
	e.repairTranscript(run.ID)

	var approved []toolCall
	if approval != nil {
		p := parsePending(run.Pending)
		if !approval.Body.Approve {
			_ = e.fenced(func(t *DB) error { t.consume(approval.ID, 0); return nil })
			if !e.denyParked(run, p, "(denied by user)") {
				return
			}
		} else {
			err := e.fenced(func(t *DB) error {
				if !t.consume(approval.ID, 0) {
					return fmt.Errorf("approval already consumed")
				}
				for _, tc := range p.ToolCalls {
					_, _ = t.setToolPlaceholder(run.ID, tc.ID, toolRunning)
				}
				e.emitStep(t, ts.root, t.journal(run.ID, "note", map[string]string{"text": "tool call(s) approved"}))
				if err := t.setStatus(run.ID, statusRunning, 0, "", ""); err != nil {
					return err
				}
				e.emitRun(t, run.ID)
				return nil
			})
			if err != nil {
				return
			}
			approved = p.ToolCalls
		}
	}

	ts.mcp = e.ag.mcpTools(ctx, cfg)
	if ctx.Err() != nil {
		return
	}
	if len(approved) > 0 {
		if e.execTools(ctx, ts, approved, true) {
			return
		}
	}
	for {
		if run, err = e.db.getRun(run.ID); err != nil {
			return
		}
		ts.run = run
		if run.Status != statusRunning || ctx.Err() != nil {
			return
		}
		if e.controlQueued(run.ID) {
			return // the next pass applies it
		}
		if run.TurnSteps >= cfg.maxTurnSteps() {
			e.endTurn(ts, endCap, fmt.Sprintf("stopped after %d steps in one turn (maxTurnSteps) — send a message to continue", run.TurnSteps))
			return
		}
		if !e.deliverBoundary(ts) {
			return
		}
		if e.forceCompactQueued(run.ID) {
			e.maybeCompact(ctx, ts, true)
		} else {
			e.maybeCompact(ctx, ts, false)
		}
		if ctx.Err() != nil {
			return
		}
		reply, specsOK := e.modelStep(ctx, ts)
		if !specsOK {
			return
		}
		calls := e.uniqueCallIDs(run, reply.Msg.ToolCalls)
		if !e.recordAssistant(ts, reply, calls) {
			return
		}
		if len(calls) == 0 {
			if e.endTurnIfQuiet(ts, asString(reply.Msg.Content)) {
				return
			}
			continue // a message arrived while it was answering: answer that too
		}
		if e.execTools(ctx, ts, calls, false) {
			return
		}
	}
}

// uniqueCallIDs gives every call an id no other call in the run has. Some
// providers omit ids, and some (small local models) reuse "call_0" every
// step — either would make a result answer the wrong call. Ids are replayed
// from storage, so renaming here is safe.
func (e *Engine) uniqueCallIDs(run *Run, in []toolCall) []toolCall {
	calls := make([]toolCall, len(in))
	copy(calls, in)
	seen := map[string]bool{}
	for i := range calls {
		id := calls[i].ID
		taken := id == "" || seen[id]
		if !taken {
			var n int
			_ = e.db.q.QueryRow(`SELECT count(*) FROM messages WHERE run_id=? AND role='tool' AND tool_call_id=?`, run.ID, id).Scan(&n)
			taken = n > 0
		}
		if taken {
			id = fmt.Sprintf("call_%d_%d_%d_%s", run.ID, run.TurnSteps, i, e.gen)
		}
		seen[id] = true
		calls[i].ID = id
		if calls[i].Type == "" {
			calls[i].Type = "function"
		}
	}
	return calls
}

// controlQueued reports an interrupt or cancel waiting to be applied.
func (e *Engine) controlQueued(runID int64) bool {
	var n int
	_ = e.db.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0 AND kind IN ('interrupt','cancel')`, runID).Scan(&n)
	return n > 0
}

func (e *Engine) forceCompactQueued(runID int64) bool {
	var n int
	_ = e.db.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0 AND kind='compact'`, runID).Scan(&n)
	return n > 0
}

// modelStep assembles the context and makes one model call under the gate.
// false means the turn ended (the call failed or the step was cancelled).
func (e *Engine) modelStep(ctx context.Context, ts *turnState) (LLMReply, bool) {
	run, cfg := ts.run, ts.cfg
	msgs, err := e.ag.assembleContext(ctx, run, cfg)
	if err == nil {
		if verr := validateWire(msgs); verr != nil {
			logf("run #%d: transcript invalid before a model call (%v) — repairing", run.ID, verr)
			e.repairTranscript(run.ID)
			if msgs, err = e.ag.assembleContext(ctx, run, cfg); err == nil {
				if verr = validateWire(msgs); verr != nil {
					err = fmt.Errorf("the transcript cannot be sent to the model: %w", verr)
				}
			}
		}
	}
	if err != nil {
		e.failTurn(ctx, ts, "assemble context: "+err.Error())
		return LLMReply{}, false
	}
	specs, own := injectSummaries(toolSpecs(cfg, run.Depth, ts.mcp))
	ts.own = own
	msgs, specs, ts.back = wireNames(msgs, specs)
	release, err := e.gate.acquire(ctx, run.Depth == 0)
	if err != nil {
		e.failTurn(ctx, ts, err.Error())
		return LLMReply{}, false
	}
	model := visionModelFor(ctx, cfg, msgs)
	e.draftStart(ts, model)
	t0 := time.Now()
	reply, err := e.llm.Chat(ctx, LLMRequest{
		Run: run.ID, Purpose: "turn",
		Model: model, Msgs: msgs, Tools: specs, Stream: cfg.feature("streaming"),
		Wire: cfg.Wire, ReasoningEffort: cfg.ReasoningEffort,
	}, func(ev LLMEvent) { e.draftEvent(ts, ev) })
	release()
	e.draftEnd(ts)
	if err != nil {
		e.failTurn(ctx, ts, err.Error())
		return LLMReply{}, false
	}
	if reply.Model == "" {
		reply.Model = model
	}
	for i, c := range reply.Msg.ToolCalls {
		if n, ok := ts.back[c.Function.Name]; ok {
			reply.Msg.ToolCalls[i].Function.Name = n
		}
	}
	ts.lastLatency = time.Since(t0)
	return reply, true
}

// recordAssistant writes the model's message and a placeholder result for
// every call it made, in ONE transaction: the transcript is valid at every
// instant, including the one right after this commit.
func (e *Engine) recordAssistant(ts *turnState, reply LLMReply, calls []toolCall) bool {
	run := ts.run
	tcJSON := ""
	if len(calls) > 0 {
		b, _ := json.Marshal(calls)
		tcJSON = string(b)
	}
	meta := msgMeta{Reasoning: reply.Reasoning, ReasoningRaw: reply.ReasoningRaw, ReasoningMs: reply.ReasoningMs,
		Wire: reply.Wire, Model: reply.Model, Finish: reply.Finish}
	if reply.Usage.PromptTokens > 0 || reply.Usage.CompletionTokens > 0 {
		u := reply.Usage
		meta.Usage = &u
	}
	metaJSON, _ := json.Marshal(meta)
	err := e.fenced(func(t *DB) error {
		m := &Message{RunID: run.ID, Role: "assistant", Content: asString(reply.Msg.Content), ToolCalls: tcJSON, Meta: metaJSON}
		if _, err := t.addMessage(m); err != nil {
			return err
		}
		e.emitMessage(t, ts.root, m)
		for _, tc := range calls {
			pm := &Message{RunID: run.ID, Role: "tool", Name: tc.Function.Name, ToolCallID: tc.ID, Content: toolRunning}
			if _, err := t.addMessage(pm); err != nil {
				return err
			}
			e.emitMessage(t, ts.root, pm)
		}
		t.setPromptTokens(run.ID, reply.Usage.PromptTokens)
		t.addRunCost(run.ID, reply.Usage.PromptTokens, reply.Usage.CompletionTokens)
		if _, err := t.q.Exec(`UPDATE runs SET turn_steps=turn_steps+1 WHERE id=?`, run.ID); err != nil {
			return err
		}
		t.journal(run.ID, "llm_call", map[string]any{
			"model": reply.Model, "wire": reply.Wire, "latencyMs": ts.lastLatency.Milliseconds(),
			"promptTokens": reply.Usage.PromptTokens, "completionTokens": reply.Usage.CompletionTokens,
			"reasoningTokens": reply.Usage.ReasoningTokens, "toolCalls": len(calls), "finishReason": reply.Finish,
		})
		e.emitRun(t, run.ID)
		return nil
	})
	return err == nil
}

// failTurn ends a turn on an error — unless the step was cancelled on
// purpose (a handoff writes nothing; an interrupt or cancel is applied by
// the pass that consumes its row).
func (e *Engine) failTurn(ctx context.Context, ts *turnState, msg string) {
	if ctx.Err() != nil {
		return
	}
	e.endTurn(ts, endError, msg)
}

// Why a turn ended.
const (
	endAnswered = "answered"
	endFinished = "finished"
	endError    = "error"
	endCap      = "cap"
	endAsked    = "asked"
)

// endTurnIfQuiet ends a turn on a plain answer — unless a message or a
// subagent result arrived while the model was answering, in which case the
// turn goes on (false). Checked inside the ending transaction, so a message
// can never slip between "nothing queued" and "idle".
func (e *Engine) endTurnIfQuiet(ts *turnState, answer string) bool {
	quiet := false
	_ = e.fenced(func(t *DB) error {
		var n int
		_ = t.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND delivered_at=0 AND kind IN ('user','watch')`, ts.run.ID).Scan(&n)
		if n > 0 || t.hasNotices(ts.run.ID) {
			return nil
		}
		quiet = true
		return e.endTurnTx(t, ts, endAnswered, answer)
	})
	return quiet
}

// endTurn ends a turn unconditionally (finish, error, the cap).
func (e *Engine) endTurn(ts *turnState, why, result string) {
	_ = e.fenced(func(t *DB) error { return e.endTurnTx(t, ts, why, result) })
}

func (e *Engine) endTurnTx(t *DB, ts *turnState, why, result string) error {
	run := ts.run
	status, outcome, linkState := statusIdle, outcomeAnswered, linkDone
	switch why {
	case endFinished:
		status, outcome = statusDone, outcomeDone
	case endError:
		status, outcome, linkState = statusError, outcomeError, linkError
	case endCap:
		outcome = outcomeIncomplete
	}
	keep := run.Result
	if why != endAnswered {
		keep = result
	}
	if err := t.setStatus(run.ID, status, 0, keep, ""); err != nil {
		return err
	}
	switch why {
	case endError:
		e.emitStep(t, ts.root, t.journal(run.ID, "error", map[string]string{"error": result}))
	case endCap:
		e.emitStep(t, ts.root, t.journal(run.ID, "note", map[string]string{"text": result}))
	case endFinished:
		e.emitStep(t, ts.root, t.journal(run.ID, "finish", map[string]string{"result": result}))
	}
	// Interrupts that arrived for a turn that has now ended are moot.
	_, _ = t.q.Exec(`UPDATE inbox SET delivered_at=? WHERE run_id=? AND kind='interrupt' AND delivered_at=0`, now(), run.ID)
	if !e.finishWatchRound(t, run) && run.ParentID == 0 {
		t.bumpActivity(run.ID) // a discarded watcher round is not news
	}
	if run.ParentID != 0 {
		res := result
		if why == endAnswered && strings.TrimSpace(res) == "" {
			res = t.lastAssistant(run.ID)
		}
		e.settleOwnLink(t, run, linkState, outcome, res)
	}
	// Nothing below a run outlives the turn that was going to consume it —
	// except below a top-level run that errored: its subagents' results are
	// delivered when the owner resumes it.
	if run.ParentID != 0 || why == endFinished {
		e.ag.cancelBelow(t, run.ID, "its parent's turn ended")
	}
	e.emitRun(t, run.ID)
	if run.ParentID == 0 && run.OriginID != 0 && (run.Origin == "schedule" || run.Origin == "watcher") {
		status := map[string]string{endAnswered: "ok", endFinished: "done", endError: "error: " + clip(result, 200), endCap: "incomplete"}[why]
		_, _ = t.q.Exec(`UPDATE schedules SET last_status=? WHERE id=?`, status, run.OriginID)
	}
	if run.ParentID == 0 && run.Origin == "channel" {
		e.channelTurnEnd(t, run, why, result) // the reply, in this transaction (outbox.go)
	}
	if run.ParentID == 0 && run.TitleSrc == "clip" && (why == endAnswered || why == endFinished) {
		t.AfterCommit(func() { e.maybeTitle(run.ID) })
	}
	switch why {
	case endFinished:
		t.AfterCommit(func() { publishEvent(run.ID, "done") })
	case endError:
		t.AfterCommit(func() { publishEvent(run.ID, "error") })
	}
	return nil
}

// --- the step boundary ---------------------------------------------------------

// deliverBoundary moves what is waiting into the transcript, in one
// transaction: queued messages (in order, with their attachments), watcher
// rounds, and one notice for every background subagent that finished. False
// means the engine lost ownership.
func (e *Engine) deliverBoundary(ts *turnState) bool {
	run := ts.run
	var delivered []int64
	err := e.fenced(func(t *DB) error {
		rows := t.undelivered(run.ID)
		for _, r := range rows {
			switch r.Kind {
			case inboxUser, inboxWatch:
			default:
				continue
			}
			text := r.Body.Text
			if r.Kind == inboxWatch {
				r.Body.Mark = t.maxMessageSeq(run.ID)
				r.Body.Open = true
				t.setInboxBody(r.ID, r.Body)
			}
			files, _ := e.ag.checkAttachmentsTx(t, run.ID, r.Body.Files)
			if text == "" && len(files) > 0 {
				text = "(see attached)"
			}
			if r.Body.Source == "parent" {
				text = fmt.Sprintf("[message from your parent run #%d]\n%s", r.Body.From, text)
			}
			m := &Message{RunID: run.ID, Role: "user", Content: text + attachmentNote(files)}
			if b := r.Body; b.Sender != "" || (b.Source != "" && b.Source != "human" && b.Source != "parent") {
				meta := msgMeta{Sender: b.Sender, OriginID: b.OriginID, Label: b.Label}
				if b.Source != "human" {
					meta.Origin = b.Source
				}
				m.Meta, _ = json.Marshal(meta)
			}
			if _, err := t.addMessage(m); err != nil {
				return err
			}
			if err := e.ag.linkMessageFilesTx(t, run.ID, m.ID, files); err != nil {
				return err
			}
			if !t.consume(r.ID, m.ID) {
				return fmt.Errorf("inbox row %d consumed twice", r.ID)
			}
			delivered = append(delivered, r.ID)
			e.emitMessage(t, ts.root, m)
		}
		if n := e.deliverNotices(t, ts); n > 0 || len(delivered) > 0 {
			e.emitInbox(t, ts.root, run.ID)
		}
		return nil
	})
	for _, id := range delivered {
		e.delivered(id)
	}
	return err == nil
}

// --- compaction outside a turn -------------------------------------------------

// compactNow runs a requested compaction on a resting run.
func (e *Engine) compactNow(run *Run, _ []*InboxRow) {
	cfg, err := e.db.runConfig(run.ID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(e.base, 2*time.Minute)
	defer cancel()
	e.maybeCompact(ctx, &turnState{run: run, cfg: cfg, root: rootOf(run)}, true)
}

// --- watcher rounds ------------------------------------------------------------

// finishWatchRound closes an open watcher round when a turn ends: a round that
// reported no change (no state_changed) is rolled back, so history keeps only
// the rounds that mattered — unless a human spoke during it. True when the
// round was discarded.
func (e *Engine) finishWatchRound(t *DB, run *Run) bool {
	rows := t.inboxRows(`WHERE run_id=? AND kind='watch' AND delivered_at<>0 ORDER BY id DESC LIMIT 1`, run.ID)
	if len(rows) == 0 || !rows[0].Body.Open {
		return false
	}
	w := rows[0]
	w.Body.Open = false
	t.setInboxBody(w.ID, w.Body)
	if w.Body.Changed {
		return false
	}
	var human int
	_ = t.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind='user' AND delivered_at<>0 AND id>?`, run.ID, w.ID).Scan(&human)
	if human > 0 {
		return false
	}
	t.deleteMessagesAfter(run.ID, w.Body.Mark)
	e.emitStep(t, rootOf(run), t.journal(run.ID, "note", map[string]string{"text": "watcher: no change — round discarded"}))
	return true
}

// markWatchChanged is state_changed: keep the open round.
func (d *DB) markWatchChanged(runID int64) {
	rows := d.inboxRows(`WHERE run_id=? AND kind='watch' AND delivered_at<>0 ORDER BY id DESC LIMIT 1`, runID)
	if len(rows) == 1 && rows[0].Body.Open {
		rows[0].Body.Changed = true
		d.setInboxBody(rows[0].ID, rows[0].Body)
	}
}

// --- halt ------------------------------------------------------------------------

// halted reports the owner's global brake: while it is on, only a human
// message moves anything (and clears it).
func (e *Engine) halted() bool { return e.db.getSetting("halt") == "1" }

// resumeIfHalted clears the brake when a human explicitly asks for work: a
// halt that silently swallows prompts is indistinguishable from a broken
// agent.
func (ag *Agent) resumeIfHalted(runID int64) {
	if ag.db.getSetting("halt") != "1" {
		return
	}
	_ = ag.db.putSetting("halt", "")
	if runID != 0 {
		ag.db.journal(runID, "note", map[string]string{"text": "halt cleared: you sent a message, which resumes the agent"})
	}
	if ag.eng != nil {
		go ag.eng.recover()
	}
}
