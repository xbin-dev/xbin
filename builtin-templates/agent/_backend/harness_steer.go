// harness_steer.go — a message while a coding agent's turn runs (D-harness
// §3.5): an adapter that steers (initialize's _meta.steering, harness
// .steering) takes it into the running turn (_session/steering with
// idleBehavior promptRequired); one that doesn't gets it as the next prompt
// once the turn ends. Also the ends of a turn AgTT asks for: an interrupt
// (session/cancel — an adapter that doesn't end its turn within
// hCancelGrace is stopped) and the turn codex starts on its own.
//
// codex's detached turn (A2): codex-acp ignores idleBehavior, so a steer
// that lands just after its turn ended starts a turn of its own
// (startedNewTurn) whose end no session/prompt answer reports. The policy:
// the run follows it as working — its events are applied as a turn's — and
// its turn ends once the adapter has gone quiet for hDetachedQuiet (no
// event), the draft flushed as the answer. A message meanwhile waits (a
// steer answers promptRequired while no prompt runs); an interrupt ends it
// in AgTT's view only (the client has no prompt of its own to cancel).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// hDetachedQuiet: a detached turn is over after this long without an event
// (a var: tests shorten it).
var hDetachedQuiet = 5 * time.Second

// hCancelGrace: an adapter that doesn't end its turn this long after
// session/cancel is stopped.
const hCancelGrace = 15 * time.Second

// steers: the adapter takes messages mid-turn.
func (s *hsess) steers() bool { return s.c != nil && s.c.State().Steering }

// harnessSteer gives the running turn row's message: injected → the user
// row is written now; promptRequired → it waits for the turn's end, which
// pokes the run; startedNewTurn → the user row, and the run follows the
// adapter's own turn. Any failure leaves it queued for the turn's end.
func (e *Engine) harnessSteer(ctx context.Context, run *Run, row *InboxRow) {
	s := e.harnessOf(run.ID)
	if s == nil || s.isHalted() || !s.steers() {
		return // no steering: it is the next prompt
	}
	if err := e.harnessRights(ctx, run, s); err != nil {
		return
	}
	files, err := e.ag.checkAttachments(run.ID, row.Body.Files)
	if err != nil {
		files = nil
	}
	text := promptText(row.Body.Text, files)
	s.flushDraft() // the text before it is a row of its own
	out, err := s.c.Steer(ctx, acp.Prompt{Text: text})
	switch {
	case err != nil:
		logf("run #%d: steering %s: %v — the message waits for the turn's end", run.ID, s.prov.Name, err)
		return
	case out == acp.SteerPromptRequired:
		return
	}
	_ = e.fenced(func(t *DB) error {
		if _, err := e.userRowTx(t, run, row); err != nil {
			return err
		}
		if out != acp.SteerStartedNewTurn {
			return nil
		}
		cur, err := t.getRun(run.ID)
		if err != nil || cur.Status == statusRunning || cur.Status == statusWaiting {
			return err
		}
		if _, err := t.q.Exec(`UPDATE runs SET status=?, pending='', turn_started=?, settled_at=0, outcome='', updated=? WHERE id=?`,
			statusRunning, e.unix(), e.unix(), run.ID); err != nil {
			return err
		}
		e.emitRun(t, run.ID)
		return nil
	})
	e.delivered(row.ID)
	if out == acp.SteerStartedNewTurn {
		s.followDetached()
	}
}

// userRowTx writes row as the run's user message — its files linked, the
// task ledger's entry, the events — and consumes the row.
func (e *Engine) userRowTx(t *DB, run *Run, row *InboxRow) (*Message, error) {
	files, _ := e.ag.checkAttachmentsTx(t, run.ID, row.Body.Files)
	m := &Message{RunID: run.ID, Role: "user", Content: promptText(row.Body.Text, files)}
	if b := row.Body; b.Sender != "" || (b.Source != "" && b.Source != "human") {
		meta := msgMeta{Sender: b.Sender, OriginID: b.OriginID, Label: b.Label}
		if b.Source != "human" {
			meta.Origin = b.Source
		}
		m.Meta, _ = json.Marshal(meta)
	}
	if _, err := t.addMessage(m); err != nil {
		return nil, err
	}
	if !t.consume(row.ID, m.ID) {
		return nil, fmt.Errorf("inbox row %d consumed twice", row.ID)
	}
	src, who := askSource(row.Body)
	if err := t.recordAsk(m, src, who); err != nil {
		return nil, err
	}
	if err := e.ag.linkMessageFilesTx(t, run.ID, m.ID, files); err != nil {
		return nil, err
	}
	e.emitMessage(t, rootOf(run), m)
	e.emitInbox(t, rootOf(run), run.ID)
	return m, nil
}

// --- the detached turn ------------------------------------------------------------------

// followDetached: the adapter runs a turn of its own (startedNewTurn).
func (s *hsess) followDetached() {
	s.mu.Lock()
	s.detached = true
	s.quietLocked()
	s.mu.Unlock()
	s.activity("thinking", "")
}

func (s *hsess) isDetached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}

// quietLocked (re)arms the detached turn's end (s.mu held).
func (s *hsess) quietLocked() {
	s.quietDue = false
	if s.quiet != nil {
		s.quiet.Stop()
	}
	if s.halted {
		return
	}
	s.quiet = time.AfterFunc(hDetachedQuiet, func() { // the pass ends it (serialized with prompts)
		s.mu.Lock()
		due := s.detached && !s.halted
		s.quietDue, s.quiet = due, nil
		s.mu.Unlock()
		if due {
			s.e.Poke(s.run)
		}
	})
}

// detachedQuiet: the detached turn went quiet — the pass ends it.
func (s *hsess) detachedQuiet() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.quietDue
}

// touchDetached: an event of the detached turn — it goes on.
func (s *hsess) touchDetached() {
	s.mu.Lock()
	if s.detached {
		s.quietLocked()
	}
	s.mu.Unlock()
}

// endDetached ends the detached turn (why: end_turn when it went quiet,
// cancelled for an interrupt): the draft flushed as the answer, the run's
// turn over unless a prompt of its own runs (its answer ends it).
func (s *hsess) endDetached(why string) {
	s.mu.Lock()
	if !s.detached || s.halted {
		s.mu.Unlock()
		return
	}
	s.detached, s.quietDue = false, false
	if s.quiet != nil {
		s.quiet.Stop()
		s.quiet = nil
	}
	s.mu.Unlock()
	rests := false
	_ = s.commit(nil, func(t *DB, hs *harnessSession) error {
		if err := s.flushAllTx(t); err != nil {
			return err
		}
		run, err := t.getRun(s.run)
		if err != nil || hs.PromptState != "" || run.Status != statusRunning {
			return err
		}
		rests = true
		return s.e.endHarnessTurnTx(t, s.run, why, "")
	})
	s.activity("idle", "")
	s.publishSummary()
	if rests {
		s.armIdle()
	}
}

// --- interrupt --------------------------------------------------------------------------

// harnessInterrupt is session/cancel for the turn in flight: the parked
// request settles "(interrupted)" at once, the adapter ends the turn
// (turn.end cancelled → idle, interrupted); one that doesn't within
// hCancelGrace is stopped (its turn ends with a note).
func (e *Engine) harnessInterrupt(ctx context.Context, run *Run, rows []*InboxRow) {
	e.consumeRows(rows)
	hs, _ := e.db.harnessSession(run.ID)
	s := e.harnessOf(run.ID)
	detached := s != nil && s.isDetached()
	parked := run.Status == statusWaiting && parsePending(run.Pending).Kind != "login"
	if hs == nil || (hs.PromptState == "" && !parked && !detached) {
		return
	}
	if s == nil {
		var err error
		if s, err = e.ensureHarness(ctx, run); err != nil {
			return
		}
	}
	_ = s.commit(nil, func(t *DB, cur *harnessSession) error {
		r, err := t.getRun(run.ID)
		if err != nil {
			return err
		}
		p := parsePending(r.Pending)
		if r.Status != statusWaiting || p.Harness == nil || p.Kind == "login" {
			return nil
		}
		s.settleParkTx(t, r, "(interrupted)")
		cur.Queue = ""
		if cur.PromptState == "" && !detached {
			return e.endHarnessTurnTx(t, r.ID, "cancelled", "")
		}
		if err := t.setStatus(r.ID, statusRunning, 0, r.Result, ""); err != nil {
			return err
		}
		e.emitRun(t, r.ID)
		return nil
	})
	if detached {
		s.endDetached("cancelled")
		return
	}
	if err := s.c.Cancel(); err != nil {
		logf("run #%d: session/cancel: %v", run.ID, err)
	}
	s.cancelGrace(hs.PromptRPC)
}

// cancelGrace stops the adapter when the turn rpc names is still in flight
// hCancelGrace after session/cancel.
func (s *hsess) cancelGrace(rpc string) {
	if rpc == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.halted {
		return
	}
	if s.graceT != nil {
		s.graceT.Stop()
	}
	s.graceT = time.AfterFunc(hCancelGrace, func() {
		hs, _ := s.e.db.harnessSession(s.run)
		if s.isHalted() || hs == nil || hs.Gen != s.gen || hs.PromptState == "" || idKey(hs.PromptRPC) != idKey(rpc) {
			return
		}
		s.endWith(fmt.Sprintf("%s didn't stop its turn when asked — it was stopped; send a message to go on", s.prov.Name), false)
		s.pipe.Kill()
	})
}
