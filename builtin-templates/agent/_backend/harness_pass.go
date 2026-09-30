// harness_pass.go — a harness run's pass (D-harness §3.2, §3.3): what its
// inbox asks of its coding agent, in priority order — cancel > interrupt >
// halt > answers (approve, hanswer) > wake/retry > prompts > compact. A
// prompt (an hprompt row) is delivered when no turn runs and nothing is
// parked: the user row (+ the task ledger) is written as it goes, and the
// turn is the adapter's until its turn.end (harness_map.go). The pass never
// waits for a turn: the actor exits and the turn's end pokes the run again.
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

	"github.com/xbin-dev/xbin/sdk/acp"
)

// The harness run's own inbox kinds (inbox.go has the rest).
const (
	inboxHPrompt = "hprompt" // a message for the coding agent: prompted, steered or queued
	inboxHAnswer = "hanswer" // an answer to its parked question (POST /runs/{id}/harness/answer)
	inboxHNote   = "hnote"   // a notice for a parent about its harness child (never starts a turn)
)

// hInbox is a harness run's pending rows by kind.
type hInbox struct {
	prompt, answer, approve, wake, interrupt, cancel, compact []*InboxRow
}

func sortHarnessInbox(rows []*InboxRow) hInbox {
	var h hInbox
	for _, r := range rows {
		switch r.Kind {
		case inboxHPrompt:
			h.prompt = append(h.prompt, r)
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

// harnessPass is pass() for a harness run.
func (e *Engine) harnessPass(run *Run, rows []*InboxRow) {
	ctx := e.base
	h := sortHarnessInbox(rows)
	if len(h.cancel) > 0 {
		e.harnessCancel(ctx, run, h)
		return
	}
	if len(h.interrupt) > 0 {
		e.harnessInterrupt(ctx, run, h.interrupt)
	}
	if e.halted() {
		return
	}
	if len(h.approve) > 0 {
		e.harnessApprove(ctx, run, h.approve)
	}
	hs, err := e.db.harnessSession(run.ID)
	if err != nil {
		return
	}
	if run, err = e.db.getRun(run.ID); err != nil {
		return
	}
	busy := run.Status == statusWaiting || (hs != nil && hs.PromptState != "")
	switch {
	case len(h.wake) > 0:
		e.harnessWake(ctx, run, hs, h.wake)
		return
	case !busy && len(h.prompt) > 0:
		e.harnessPrompt(ctx, run, h.nextPrompt())
		return
	}
	if len(h.compact) > 0 { // D-harness §4.2.11 sends /compact; until then, said and consumed
		e.consumeWithNote(run, h.compact, "a coding agent compacts its own context")
	}
	if e.harnessOf(run.ID) == nil && hs != nil && hs.ExecID != "" && (hs.State == hsLive || hs.State == hsStarting) {
		e.resumeHarness(ctx, run, hs) // a predecessor's adapter: keep reading it
	}
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
// pokes its run): attached when its session was open, else — it was still
// opening one — ended, for the next prompt to start a new generation.
func (e *Engine) resumeHarness(ctx context.Context, run *Run, hs *harnessSession) {
	var st acp.SessionState
	if json.Unmarshal([]byte(hs.Snapshot), &st) == nil && st.SessionID != "" {
		if _, err := e.ensureHarness(ctx, run); err != nil && ctx.Err() == nil {
			logf("run #%d: taking over its coding agent: %v", run.ID, err)
		}
		return
	}
	cfg, err := e.db.runConfig(run.ID)
	if err != nil || cfg.Harness == nil {
		return
	}
	e.dropExec(ctx, run, cfg, hs)
	_ = e.fenced(func(t *DB) error {
		cur, _ := t.harnessSession(run.ID)
		if cur == nil || cur.Gen != hs.Gen {
			return nil
		}
		cur.State = hsStopped
		return t.putHarnessSession(cur)
	})
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
// its files, their names (the files stay the conversation's; D-harness
// §3.6's resource links come with the sign-in and files work).
func promptText(text string, files []*ReplFile) string {
	if text == "" && len(files) > 0 {
		text = "(see attached)"
	}
	return text + attachmentNote(files)
}

// harnessPrompt delivers row: the session ensured, the user row written
// with the prompt marked on its way, then session/prompt. A session that
// can't be had fails the turn with why, the prompt kept for /resume.
func (e *Engine) harnessPrompt(ctx context.Context, run *Run, row *InboxRow) {
	s, serr := e.ensureHarness(ctx, run)
	if ctx.Err() != nil || errors.Is(serr, errHandoff) || errors.Is(serr, errFenced) {
		return
	}
	var m *Message
	ok := false
	ferr := e.fenced(func(t *DB) error {
		files, _ := e.ag.checkAttachmentsTx(t, run.ID, row.Body.Files)
		m = &Message{RunID: run.ID, Role: "user", Content: promptText(row.Body.Text, files)}
		if b := row.Body; b.Sender != "" || (b.Source != "" && b.Source != "human") {
			meta := msgMeta{Sender: b.Sender, OriginID: b.OriginID, Label: b.Label}
			if b.Source != "human" {
				meta.Origin = b.Source
			}
			m.Meta, _ = json.Marshal(meta)
		}
		if _, err := t.addMessage(m); err != nil {
			return err
		}
		if !t.consume(row.ID, m.ID) {
			return fmt.Errorf("inbox row %d consumed twice", row.ID)
		}
		src, who := askSource(row.Body)
		if err := t.recordAsk(m, src, who); err != nil {
			return err
		}
		if err := e.ag.linkMessageFilesTx(t, run.ID, m.ID, files); err != nil {
			return err
		}
		e.emitMessage(t, rootOf(run), m)
		e.emitInbox(t, rootOf(run), run.ID)
		hs, _ := t.harnessSession(run.ID)
		if hs == nil {
			hs = &harnessSession{RunID: run.ID, RootID: rootOf(run)}
		}
		if serr != nil { // no session: the turn fails, the prompt is kept for a retry
			held, _ := json.Marshal(heldPrompt{Text: m.Content, Sender: row.Body.Sender})
			hs.Held = string(held)
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
		return
	}
	e.sendPrompt(ctx, s, run, m.Content)
}

// sendPrompt starts the turn at the adapter; a prompt that can't go ends
// it with why.
func (e *Engine) sendPrompt(ctx context.Context, s *hsess, run *Run, text string) {
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
// sign-in again) and the held prompt sent again — without another user row.
func (e *Engine) harnessWake(ctx context.Context, run *Run, hs *harnessSession, rows []*InboxRow) {
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		return nil
	})
	for _, r := range rows {
		e.delivered(r.ID)
	}
	if run.Status == statusWaiting || (hs != nil && hs.PromptState != "") {
		return // a turn or a park is in force: nothing to resume
	}
	s, serr := e.ensureHarness(ctx, run)
	if ctx.Err() != nil || errors.Is(serr, errHandoff) || errors.Is(serr, errFenced) {
		return
	}
	var held heldPrompt
	if hs != nil && hs.Held != "" {
		_ = json.Unmarshal([]byte(hs.Held), &held)
	}
	sent := false
	_ = e.fenced(func(t *DB) error {
		cur, _ := t.harnessSession(run.ID)
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
	if sent {
		e.sendPrompt(ctx, s, run, held.Text)
	}
}

// --- stops -----------------------------------------------------------------------------

// harnessInterrupt is session/cancel for the turn in flight: the adapter
// ends it (turn.end cancelled → idle, interrupted).
func (e *Engine) harnessInterrupt(ctx context.Context, run *Run, rows []*InboxRow) {
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		return nil
	})
	hs, _ := e.db.harnessSession(run.ID)
	if hs == nil || (hs.PromptState == "" && run.Status != statusWaiting) {
		return
	}
	s := e.harnessOf(run.ID)
	if s == nil {
		var err error
		if s, err = e.ensureHarness(ctx, run); err != nil {
			return
		}
	}
	if err := s.c.Cancel(); err != nil {
		logf("run #%d: session/cancel: %v", run.ID, err)
	}
}

// harnessCancel is the durable stop: session/cancel, the adapter ended,
// the run canceled (a child's link settles canceled).
func (e *Engine) harnessCancel(ctx context.Context, run *Run, h hInbox) {
	reason := "cancelled"
	if h.cancel[0].Body.Reason != "" {
		reason = h.cancel[0].Body.Reason
	}
	if s := e.harnessOf(run.ID); s != nil {
		_ = s.c.Cancel()
		s.stop()
	} else if hs, _ := e.db.harnessSession(run.ID); hs != nil && hs.ExecID != "" && (hs.State == hsLive || hs.State == hsStarting) {
		if cfg, err := e.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
		}
	}
	s := &hsess{e: e, run: run.ID, root: rootOf(run)}
	_ = e.fenced(func(t *DB) error {
		for _, r := range append(h.cancel, h.interrupt...) {
			t.consume(r.ID, 0)
		}
		if hs, _ := t.harnessSession(run.ID); hs != nil {
			hs.PromptState, hs.PromptRPC, hs.Queue = "", "", ""
			if hs.State == hsLive || hs.State == hsStarting {
				hs.State = hsStopped
			}
			if err := t.putHarnessSession(hs); err != nil {
				return err
			}
		}
		if !active(run.Status) {
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
		e.emitRun(t, run.ID)
		return nil
	})
}

// harnessApprove answers the parked permission with the verdict that names
// it: approve picks the first allow (allow_once first), deny the first
// reject (else the cancelled outcome). The rest of the approve rules —
// explicit options, feedback, a message instead of an answer — are
// D-harness §4.2.9's.
func (e *Engine) harnessApprove(ctx context.Context, run *Run, rows []*InboxRow) {
	p := parsePending(run.Pending)
	var v *InboxRow
	if run.Status == statusWaiting && p.Kind == "approval" && p.Harness != nil {
		for i := len(rows) - 1; i >= 0 && v == nil; i-- {
			if rows[i].Body.Park == p.Park {
				v = rows[i]
			}
		}
	}
	_ = e.fenced(func(t *DB) error {
		for _, r := range rows {
			t.consume(r.ID, 0)
		}
		return nil
	})
	if v == nil {
		return
	}
	s, err := e.ensureHarness(ctx, run)
	if err != nil {
		return
	}
	res := &acp.Resolution{PID: p.Harness.PID, By: "user:" + v.Body.Sender, RPCID: json.RawMessage(p.Harness.RPCID)}
	opt := pickOption(p.Harness.Options, v.Body.Approve)
	if opt == "" {
		res.Cancel = true
	} else {
		res.OptionID = opt
	}
	if r, err := s.perms.Resolve(p.Harness.PID, opt, "", res.By); err == nil {
		res = r
	} else if opt != "" {
		logf("run #%d: the parked permission %s: %v", run.ID, p.Harness.PID, err)
	}
	if err := s.c.RespondPermission(res); err != nil {
		logf("run #%d: answering the permission: %v", run.ID, err)
	}
}

// pickOption is the option approve (or deny) chooses: allow_once, else any
// allow — never one that raises the session to an explicit mode; a denial
// is reject_once, else reject_always; "" = none (the cancelled outcome).
func pickOption(opts []hOption, approve bool) string {
	kinds := []string{acp.RejectOnce, acp.RejectAlways}
	if approve {
		kinds = []string{acp.AllowOnce, acp.AllowAlways}
	}
	for _, k := range kinds {
		for _, o := range opts {
			if o.Kind == k && !o.Explicit {
				return o.OptionID
			}
		}
	}
	return ""
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
// provider's mode, or a mode they named (an explicit one only from a
// person).
func harnessClass(ctx context.Context, c who, req *harnessReq, class, system, model string) (agentClass, error) {
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
		case m != nil && m.Explicit && c.kind != whoUser:
			return agentClass{}, &errClass{403, fmt.Sprintf("only a person can start %s in %s", name, m.Name)}
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
		if s := e.harnessOf(rid); s != nil {
			s.stop()
			continue
		}
		run, err := e.db.getRun(rid)
		if err != nil || run.Engine != engineHarness {
			continue
		}
		hs, _ := e.db.harnessSession(rid)
		if hs == nil || hs.ExecID == "" || (hs.State != hsLive && hs.State != hsStarting) {
			continue
		}
		if cfg, err := e.db.runConfig(rid); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs)
		}
	}
}
