// harness_life.go — how long a coding agent's adapter lives, and what
// ends it (D-harness §3.3):
//
//   - idle reclaim: a one-shot timer at last_active + harnessIdleMin (the
//     tile's config), armed only while the session is live with no turn
//     and no park — an idle adapter holds off its sandbox's own idle stop.
//     It pokes the run; the pass stops the adapter (state stopped) when the
//     session still rests. No tickers.
//   - the sandbox's use re-checked (sandboxUse: the binder's rights, the
//     class, taint; egress): before every prompt, steer and answer, and at
//     most once a minute on durable events; a refusal stops the session
//     (session/cancel, the adapter killed) and ends the turn with why.
//   - a detached sandbox (storeBinding) stops the sessions working in it —
//     an hstop row, which the pass applies like a refusal.
//   - output lost mid-turn (a ring gap): the turn's end or a request may be
//     among the lost bytes, so the adapter is stopped (state lost) and the
//     next prompt starts it again (session/load).
//   - a request the stdio pipe gave up on (its socket dropped before the
//     manager acknowledged it: harness_pipe.go) is never sent twice: its
//     call ends at once — a prompt fails "send it again".
//   - /compact is sent as the prompt "/compact" when the adapter
//     advertises it.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// inboxHStop stops a coding agent's adapter (Reason: why — its sandbox was
// detached, its binder's rights are gone). Older binaries ignore it.
const inboxHStop = "hstop"

// harnessIdleTest, when set, is the idle reclaim's delay (tests).
var harnessIdleTest time.Duration

// hRecheckEvery bounds the re-check of the sandbox's use on durable events.
const hRecheckEvery = time.Minute

// hsRunning: the storage state has an adapter process (a sign-in keeps
// the signed-out one).
func hsRunning(state string) bool {
	return state == hsLive || state == hsStarting || state == hsLogin
}

// hsExecMayRun: the stored exec may still run — an adapter's state; failed
// (a stop whose kill may not have got through: the manager didn't answer)
// or lost (cut off by a manager that stopped answering), which a cancel, a
// delete or a stop tries to end again. Not stopped: its end was seen, or
// AgTT's own kill was sent.
func hsExecMayRun(state string) bool {
	return hsRunning(state) || state == hsFailed || state == hsLost
}

// --- idle reclaim ------------------------------------------------------------------------

func (e *Engine) harnessIdleFor() time.Duration {
	if harnessIdleTest != 0 {
		return harnessIdleTest
	}
	return parseConfig(e.db.getSetting("config")).harnessIdle()
}

// armIdle (re)arms the reclaim: the session rests now.
func (s *hsess) armIdle() {
	d := s.e.harnessIdleFor()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idleT != nil {
		s.idleT.Stop()
		s.idleT = nil
	}
	s.reclaim = false
	if s.halted || d <= 0 {
		return
	}
	s.idleT = time.AfterFunc(d, func() {
		s.mu.Lock()
		s.idleT, s.reclaim = nil, !s.halted
		due := s.reclaim
		s.mu.Unlock()
		if due {
			s.e.Poke(s.run)
		}
	})
}

// disarmIdle: the session works (a prompt, a park).
func (s *hsess) disarmIdle() {
	s.mu.Lock()
	if s.idleT != nil {
		s.idleT.Stop()
		s.idleT = nil
	}
	s.reclaim = false
	s.mu.Unlock()
}

func (s *hsess) reclaimDue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaim
}

// harnessReclaim is the reclaim's pass: an adapter still resting is
// stopped (the next prompt starts one); a busy one is left — the timer is
// armed again when it rests.
func (e *Engine) harnessReclaim(run *Run, s *hsess, busy bool) {
	s.disarmIdle()
	if hs, _ := e.db.harnessSession(s.run); busy || hs == nil || hs.State != hsLive || hs.PromptState != "" || s.isDetached() {
		return // the pass serializes prompts: what rests now rests through the commit
	}
	if s.commit(nil, func(t *DB, hs *harnessSession) error {
		hs.State = hsStopped
		e.emitRun(t, s.run)
		return nil
	}) != nil {
		return
	}
	logf("run #%d: %s idle — stopped (the next message starts it again)", run.ID, s.prov.Name)
	s.stop()
}

// --- the sandbox's use ---------------------------------------------------------------------

// harnessRights re-checks that the conversation may still use the
// sandbox before a prompt, steer or answer reaches s's adapter (not again
// right after the start that checked it): a refusal stops the session and
// is returned (a *harnessFail); a manager that doesn't answer is not a
// refusal.
func (e *Engine) harnessRights(ctx context.Context, run *Run, s *hsess) error {
	s.mu.Lock()
	skip := s.fresh && time.Since(s.checked) < 2*time.Second
	s.fresh = false
	s.mu.Unlock()
	if skip {
		return nil
	}
	cfg, err := e.db.runConfig(run.ID)
	if err != nil {
		return nil
	}
	_, _, err = e.harnessUse(ctx, run, cfg)
	if !isHarnessFail(err) {
		s.mu.Lock()
		s.checked = time.Now()
		s.mu.Unlock()
		return nil
	}
	e.stopHarnessNow(ctx, run, err.Error())
	return err
}

// recheckSoon re-checks the sandbox's use in the background after a
// durable event, at most every hRecheckEvery: a refusal queues an hstop.
func (s *hsess) recheckSoon() {
	s.mu.Lock()
	if s.checking || s.halted || time.Since(s.checked) < hRecheckEvery {
		s.mu.Unlock()
		return
	}
	s.checking = true
	s.mu.Unlock()
	go func() {
		e := s.e
		var ferr error
		if run, err := e.db.getRun(s.run); err == nil {
			if cfg, err := e.db.runConfig(s.run); err == nil {
				ctx, cancel := context.WithTimeout(e.base, sbxCallTimeout)
				_, _, ferr = e.harnessUse(ctx, run, cfg)
				cancel()
			}
		}
		s.mu.Lock()
		s.checking, s.checked = false, time.Now()
		s.mu.Unlock()
		if isHarnessFail(ferr) && !s.isHalted() {
			if _, _, err := e.ag.queue(s.run, inboxHStop, inboxBody{Reason: ferr.Error()}, ""); err != nil {
				logf("run #%d: stopping %s: %v", s.run, s.prov.Name, err)
			}
		}
	}()
}

// harnessStop is hstop rows: the session stopped with the first's reason.
func (e *Engine) harnessStop(ctx context.Context, run *Run, rows []*InboxRow) {
	reason := orStr(rows[0].Body.Reason, "stopped")
	e.consumeRows(rows)
	e.stopHarnessNow(ctx, run, reason)
}

// stopHarnessNow stops run's session: session/cancel and the adapter
// killed, the session failed with why, a park settled and a turn in flight
// ended with it.
func (e *Engine) stopHarnessNow(ctx context.Context, run *Run, why string) {
	hs, _ := e.db.harnessSession(run.ID)
	if s := e.harnessOf(run.ID); s != nil {
		if s.c != nil {
			_ = s.c.Cancel()
		}
		s.stop()
	} else if hs != nil && hs.ExecID != "" && hsExecMayRun(hs.State) {
		if cfg, err := e.db.runConfig(run.ID); err == nil && cfg.Harness != nil {
			e.dropExec(ctx, run, cfg, hs) // rights-free: a refusal is why it stops
		}
	}
	ls := &hsess{e: e, run: run.ID, root: rootOf(run)}
	_ = e.fenced(func(t *DB) error {
		cur, _ := t.harnessSession(run.ID)
		if cur == nil {
			return nil
		}
		inFlight := cur.PromptState != ""
		cur.PromptState, cur.PromptRPC, cur.Queue, cur.Login = "", "", "", ""
		cur.State, cur.Error = hsFailed, why
		if err := t.putHarnessSession(cur); err != nil {
			return err
		}
		r, err := t.getRun(run.ID)
		if err != nil {
			return err
		}
		if !inFlight && r.Status != statusRunning && r.Status != statusWaiting {
			return nil
		}
		ls.settleParkTx(t, r, "(stopped)")
		return e.endHarnessTurnTx(t, run.ID, "error", why)
	})
}

// stopDetachedHarnesses, in storeBinding's transaction: the conversation's
// coding agents working in a sandbox it no longer has are stopped (an
// hstop each, applied by their passes once the change commits).
func (d *DB) stopDetachedHarnesses(root int64, before, after Config) {
	var all []int64
	for _, b := range bindingsOf(before) {
		if _, still := after.sandboxBinding(b.Ref); still {
			continue
		}
		ids := scanIDs(d.q.Query(`SELECT run_id FROM harness_sessions WHERE root_id=? AND ref=? AND state IN ('starting','live','login')`, root, b.Ref))
		why := fmt.Sprintf("its sandbox %s was detached from the conversation", orStr(b.Name, b.Ref))
		for _, id := range ids {
			if _, _, err := d.enqueue(id, inboxHStop, inboxBody{Reason: why}, ""); err == nil {
				all = append(all, id)
			}
		}
	}
	if len(all) > 0 && agent != nil && agent.eng != nil {
		e := agent.eng
		d.AfterCommit(func() {
			for _, id := range all {
				e.Poke(id)
			}
		})
	}
}

// --- lost output, dropped requests ----------------------------------------------------------

// hsStdout is the pipe's stdout as the client reads it, watched for gaps.
type hsStdout struct {
	io.Reader
	s *hsess
}

func (r *hsStdout) Read(b []byte) (int, error) {
	n, err := r.Reader.Read(b)
	var g *acp.Gap
	if errors.As(err, &g) {
		go r.s.onGap(g.Lost)
	}
	return n, err
}

// onGap: the ring lost output before it was read. Said; mid-turn (or
// parked) the turn's end or a request may be among it — the adapter is
// stopped, the session lost, and the next prompt starts it again.
func (s *hsess) onGap(lost int64) {
	if s.isHalted() {
		return
	}
	run, err := s.e.db.getRun(s.run)
	if err != nil {
		return
	}
	note := fmt.Sprintf("%d bytes of %s's output were lost", lost, s.prov.Name)
	s.e.note(run, note)
	hs, _ := s.e.db.harnessSession(s.run)
	if (hs != nil && hs.PromptState != "") || run.Status == statusWaiting || s.isDetached() {
		s.endWith(note+" mid-turn — it was stopped; send a message to go on", true)
		s.pipe.Kill()
	}
}

// endWith sets what ended() says when the adapter's end is AgTT's doing
// (lost: the session is lost, not stopped).
func (s *hsess) endWith(why string, lost bool) {
	s.mu.Lock()
	s.endWhy, s.endLost = why, lost
	s.mu.Unlock()
}

// dropped: the pipe gave up on a request (hpTarget.Dropped) — its call
// ends now (a prompt: its turn fails, "send it again").
func (s *hsess) dropped(id json.RawMessage, method string) {
	why := fmt.Sprintf("the connection to %s dropped while your message was on its way — send it again", s.prov.Name)
	if method != acp.MSessionPrompt {
		why = fmt.Sprintf("the connection to %s dropped during %s", s.prov.Name, method)
	}
	s.mu.Lock()
	s.abandoned[idKey(string(id))] = why
	c := s.c
	s.mu.Unlock()
	if c == nil || !c.Abandon(id, why) {
		s.mu.Lock()
		delete(s.abandoned, idKey(string(id)))
		s.mu.Unlock()
	}
}

// takeAbandoned is why the call under id was abandoned ("": it wasn't).
func (s *hsess) takeAbandoned(id json.RawMessage) string {
	if len(id) == 0 {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	k := idKey(string(id))
	why := s.abandoned[k]
	delete(s.abandoned, k)
	return why
}

// --- the prompt in flight -----------------------------------------------------------------

func (s *hsess) setInflight(p *heldPrompt) {
	s.mu.Lock()
	s.inflight = p
	s.mu.Unlock()
}

func (s *hsess) inflightPrompt() *heldPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inflight
}

// --- /compact -----------------------------------------------------------------------------

// harnessCompact sends /compact as a prompt when the adapter advertises
// it (the route refuses it otherwise; a row that got here anyway is said).
func (e *Engine) harnessCompact(ctx context.Context, run *Run, hs *harnessSession, rows []*InboxRow) {
	var st acp.SessionState
	if hs != nil {
		_ = json.Unmarshal([]byte(hs.Snapshot), &st)
	}
	has := false
	for _, c := range st.Commands {
		has = has || c.Name == "compact"
	}
	cfg, _ := e.db.runConfig(run.ID)
	name := "the coding agent"
	if cfg.Harness != nil {
		name = e.db.harnessRunName(run.ID, cfg.Harness.Provider)
	}
	if !has {
		e.consumeWithNote(run, rows, name+" has no /compact")
		return
	}
	e.consumeRows(rows[1:])
	row := *rows[0]
	row.Body.Text, row.Body.Files, row.Body.Source = "/compact", nil, orStr(row.Body.Source, "human")
	e.harnessPrompt(ctx, run, &row)
}
