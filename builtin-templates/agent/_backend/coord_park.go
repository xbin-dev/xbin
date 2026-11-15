// coord_park.go — a coordinator's tool call that waits for a decision made
// outside this engine (coord_profile.go): a person approving a request in
// another system's own chrome, where this backend can ask and read and
// never answer. The call's result stays a placeholder ("(waiting for …)")
// and its conversation parks, waiting for its person, until one of three
// things happens:
//
//   - the decision arrives: the profile settles the call with what came of
//     it (coordSettle) and the conversation goes on from that result;
//   - the person writes instead: every call still parked is answered with
//     the profile's words for "still open" (its Unpark), and the outcome
//     reaches the coordinator later as an update (coord_wake.go);
//   - the turn is stopped (an interrupt, a cancel): the placeholders are
//     answered as every other unsettled call's, and a later coordSettle
//     reports that it settled nothing — again the profile delivers the
//     outcome as an update.
//
// So a profile that parks keeps the request's row itself, with the call it
// answers; the kit keeps the turn honest: a parked call is a legitimate
// placeholder while its park stands (pendingState.ToolCalls, which
// repairTranscript leaves alone), and never one a later turn could take for
// a lost tool.
package main

import (
	"encoding/json"
	"errors"
)

// pendKindCoord is runs.pending's kind for a coordinator parked on calls
// that wait outside (pendingState.ToolCalls: the calls).
const pendKindCoord = "coord"

// coordPark makes tc wait: its placeholder says what for ("your person to
// approve the request in xbin"), and the step parks once its other calls
// ran (parkIfWaiting). Only for a coordinator's own call, in its turn.
func (e *Engine) coordPark(ts *turnState, tc toolCall, waitsFor string) error {
	if coordOriginOf(ts.run) == nil {
		return errors.New("only a coordinator's call waits outside its turn")
	}
	err := e.fenced(func(t *DB) error {
		ok, err := t.setToolPlaceholder(ts.run.ID, tc.ID, toolWaitingPrefix+waitsFor+"…)")
		if err != nil {
			return err
		}
		if !ok {
			return errors.New("the call was answered already")
		}
		if id, _, err := t.toolResultRow(ts.run.ID, tc.ID); err == nil {
			e.emitMessageID(t, ts.root, ts.run.ID, id)
		}
		return nil
	})
	if err == nil {
		ts.parked = append(ts.parked, tc)
	}
	return err
}

// parkCoord parks run on ts.parked (parkIfWaiting): waiting for its person,
// its result line saying what waits. true: parked.
func (e *Engine) parkCoord(ts *turnState) bool {
	run := ts.run
	words := "waiting for a decision"
	if p := coordOriginOf(run); p != nil && p.ParkWords != nil {
		words = p.ParkWords(e.db, run, ts.parked)
	}
	pend, _ := json.Marshal(pendingState{Kind: pendKindCoord, ToolCalls: ts.parked, Since: nowMs()})
	err := e.fenced(func(t *DB) error {
		t.bumpActivity(run.ID)
		if err := t.setStatus(run.ID, statusWaiting, 0, words, string(pend)); err != nil {
			return err
		}
		e.emitStep(t, ts.root, t.journal(run.ID, "ask", map[string]any{"kind": pendKindCoord, "tools": toolNames(ts.parked)}))
		e.emitRun(t, run.ID)
		return nil
	})
	if err != nil {
		return false
	}
	// a decision may have landed between the call and the park: look again
	e.Poke(run.ID)
	return true
}

// coordSettle answers the parked call callID of run with content — what
// came of what it waited for. True: the placeholder took it, and when it
// was the last call the park stood on, the conversation goes on. False:
// the call was answered some other way (the person wrote, the turn was
// stopped) — the profile then owes the outcome as an update.
func (e *Engine) coordSettle(runID int64, callID, content string) bool {
	took := false
	_ = e.fenced(func(t *DB) error {
		run, err := t.getRun(runID)
		if err != nil {
			return err
		}
		ok, err := t.casToolResult(runID, callID, capToolResult(content))
		if err != nil || !ok {
			return err
		}
		took = true
		if id, _, err := t.toolResultRow(runID, callID); err == nil {
			e.emitMessageID(t, rootOf(run), runID, id)
		}
		p := parsePending(run.Pending)
		if run.Status != statusWaiting || p.Kind != pendKindCoord {
			return nil // settled before the step parked: parkIfWaiting finds nothing left
		}
		left := p.ToolCalls[:0:0]
		for _, tc := range p.ToolCalls {
			if tc.ID != callID {
				left = append(left, tc)
			}
		}
		if len(left) > 0 {
			p.ToolCalls = left
			return t.setStatus(runID, statusWaiting, 0, run.Result, mustJSON(p))
		}
		// the last one: the next look at the run finds nothing parked and goes
		// on (pass). No inbox row says so — the transcript does — so a look
		// that raced this commit leaves nothing behind to answer twice.
		t.AfterCommit(func() { e.Poke(runID) })
		return nil
	})
	return took
}

// coordParked is the calls of run's park that are still placeholders (a
// decision that landed while the step was parking leaves none).
func coordParked(d *DB, run *Run, calls []toolCall) []toolCall {
	var open []toolCall
	for _, tc := range calls {
		if _, cur, err := d.toolResultRow(run.ID, tc.ID); err == nil && isPlaceholder(cur) {
			open = append(open, tc)
		}
	}
	return open
}

// coordUnpark ends run's park because its person wrote instead of
// deciding: each call still parked is answered in the profile's words (its
// Unpark, which also notes that the outcome is now owed as an update), and
// the conversation is running again. True: it is.
func (e *Engine) coordUnpark(run *Run, p pendingState) bool {
	prof := coordOriginOf(run)
	err := e.fenced(func(t *DB) error {
		for _, tc := range coordParked(t, run, p.ToolCalls) {
			text := "(still waiting — its outcome will reach you as an update)"
			if prof != nil && prof.Unpark != nil {
				text = prof.Unpark(t, run, tc)
			}
			if ok, _ := t.casToolResult(run.ID, tc.ID, text); ok {
				if id, _, err := t.toolResultRow(run.ID, tc.ID); err == nil {
					e.emitMessageID(t, rootOf(run), run.ID, id)
				}
			}
		}
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
