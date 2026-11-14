package acp

// Messages while a turn runs: what a host does with a message a person
// sends when the agent is busy — steer it into the running turn, hold it
// for the turn's end, or prompt with it — and the turn codex starts on its
// own. The host keeps the held messages wherever it keeps things (xbind: in
// memory, shown by its `queue` events; the agent template: its inbox rows)
// and does the sending; the decisions are here, the same for both:
//
//   - nothing runs and nothing waits: the message is a prompt;
//   - a turn runs: it is offered to the turn where the agent steers
//     (Client.Steer) — unless it interrupts the turn, or others are already
//     held (it never jumps them); else it is held;
//   - what the agent answers a steer is SteerOutcome's to read: injected →
//     delivered; promptRequired → the turn had ended, it is a prompt after
//     all; startedNewTurn → delivered, and the agent now runs a turn no
//     session/prompt answers (a detached turn);
//   - held messages go as a prompt once the turn is over — idle, or it
//     failed — and no detached turn runs;
//   - a detached turn (DetachedTurn) is followed as a running one, and is
//     over once the agent has gone quiet: no turn.end will ever say so. A
//     message meanwhile is held; an interrupt ends it in the host's view
//     only (the client has no prompt of its own to cancel).

import (
	"errors"
	"time"
)

// What a host does with a message (QueueState.Message, SteerOutcome).
const (
	MessagePrompt    = "prompt"    // send it with Client.Prompt: it starts a turn
	MessageSteer     = "steer"     // offer it to the running turn (Client.Steer), then read SteerOutcome
	MessageHold      = "hold"      // keep it for the turn's end
	MessageDelivered = "delivered" // SteerOutcome: the agent has it — nothing more to send
	MessageRefused   = "refused"   // SteerOutcome: the steer failed and the message is the host's again (see the error)
)

// QueueState is what a host knows of its session when a message arrives or
// the session moves.
type QueueState struct {
	Status   string // the session's status (StatusIdle, StatusRunning, …)
	Detached bool   // the agent runs a turn of its own (DetachedTurn.Running)
	Held     int    // messages already held for the turn's end
	Steering bool   // the agent takes messages mid-turn (SessionState.Steering)
	Sending  bool   // a prompt is being handed to the agent right now
}

// Busy reports whether a turn runs: the client's (running, waiting on an
// answer, being cancelled) or the agent's own.
func (q QueueState) Busy() bool { return Busy(q.Status) || q.Detached }

// Message is what to do with a message that arrives now: MessagePrompt,
// MessageSteer or MessageHold. interrupt: it is to cut the running turn
// short — it is held (never steered) and the host cancels the turn, whose
// end sends it.
func (q QueueState) Message(interrupt bool) string {
	switch {
	case !q.Busy() && q.Held == 0:
		return MessagePrompt
	case Busy(q.Status) && !q.Detached && q.Steering && !interrupt && q.Held == 0:
		return MessageSteer
	}
	return MessageHold
}

// Flush reports whether the held messages go as a prompt now: there are
// some, the turn is over (idle, or it failed), no detached turn runs and no
// prompt is on its way.
func (q QueueState) Flush() bool {
	return q.Held > 0 && TurnOver(q.Status) && !q.Detached && !q.Sending
}

// SteerOutcome reads Client.Steer's answer for a message: what to do with
// it next, and whether the agent started a turn of its own with it
// (detached: follow it with a DetachedTurn).
//
//   - SteerInjected: MessageDelivered.
//   - SteerStartedNewTurn: MessageDelivered, detached — the turn had just
//     ended and the agent (codex-acp) began another with the message; no
//     turn.end will report its end.
//   - SteerPromptRequired: MessagePrompt — no turn runs any more; a host
//     that cannot prompt right now holds it.
//   - ErrSteeringUnsupported: MessageHold.
//   - any other error: MessageRefused. The message did not join the turn as
//     far as the client knows; whether to hold it, report it or — when the
//     transport cannot say if the frame arrived — never send it again is
//     the host's call.
func SteerOutcome(outcome string, err error) (action string, detached bool) {
	switch {
	case errors.Is(err, ErrSteeringUnsupported):
		return MessageHold, false
	case err != nil:
		return MessageRefused, false
	}
	switch outcome {
	case SteerInjected:
		return MessageDelivered, false
	case SteerStartedNewTurn:
		return MessageDelivered, true
	case SteerPromptRequired:
		return MessagePrompt, false
	}
	return MessageRefused, false
}

// DetachedQuiet is how long a detached turn goes without an event before
// it is taken to be over.
const DetachedQuiet = 5 * time.Second

// DetachedTurn follows a turn the agent started on its own
// (SteerStartedNewTurn): the client runs no prompt, so no turn.end reports
// its end and every status the client restates says idle. The host treats
// the session as running from Begin, calls Event for each event the agent
// sends, and ends the turn (End) when it has been quiet for Quiet — Over
// says so; Deadline is when to look. It keeps no timer of its own. The zero
// value follows nothing; not safe for concurrent use.
type DetachedTurn struct {
	Quiet time.Duration // 0: DetachedQuiet

	on   bool
	last time.Time
}

func (d *DetachedTurn) quiet() time.Duration {
	if d.Quiet > 0 {
		return d.Quiet
	}
	return DetachedQuiet
}

// Begin starts following a turn (again: the one followed goes on).
func (d *DetachedTurn) Begin(now time.Time) { d.on, d.last = true, now }

// Running reports whether a detached turn is being followed.
func (d *DetachedTurn) Running() bool { return d.on }

// Event notes an event of the agent's: the turn goes on. False when no
// detached turn runs (nothing is noted).
func (d *DetachedTurn) Event(now time.Time) bool {
	if d.on {
		d.last = now
	}
	return d.on
}

// Deadline is when the turn is over if nothing more comes (zero: none runs).
func (d *DetachedTurn) Deadline() time.Time {
	if !d.on {
		return time.Time{}
	}
	return d.last.Add(d.quiet())
}

// Over reports whether the turn has been quiet long enough to be over.
func (d *DetachedTurn) Over(now time.Time) bool {
	return d.on && !now.Before(d.Deadline())
}

// End stops following the turn; false when none ran.
func (d *DetachedTurn) End() bool {
	was := d.on
	d.on = false
	return was
}
