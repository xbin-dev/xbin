package acp

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

// What a host does with a message that arrives: prompt, steer or hold.
func TestQueueMessage(t *testing.T) {
	for _, x := range []struct {
		name      string
		q         QueueState
		interrupt bool
		want      string
	}{
		{"idle, nothing held: a prompt", QueueState{Status: StatusIdle, Steering: true}, false, MessagePrompt},
		{"the last turn failed: a prompt", QueueState{Status: StatusError}, false, MessagePrompt},
		{"idle, with an interrupt: nothing to interrupt — a prompt", QueueState{Status: StatusIdle}, true, MessagePrompt},
		{"idle, others held: behind them", QueueState{Status: StatusIdle, Held: 1, Steering: true}, false, MessageHold},
		{"a turn runs, the agent steers", QueueState{Status: StatusRunning, Steering: true}, false, MessageSteer},
		{"a turn waits on a person, the agent steers", QueueState{Status: StatusWaiting, Steering: true}, false, MessageSteer},
		{"a turn is being cancelled, the agent steers", QueueState{Status: StatusCancelling, Steering: true}, false, MessageSteer},
		{"a turn runs, no steering", QueueState{Status: StatusRunning}, false, MessageHold},
		{"a turn runs, others held: never past them", QueueState{Status: StatusRunning, Steering: true, Held: 2}, false, MessageHold},
		{"an interrupt is held, never steered", QueueState{Status: StatusRunning, Steering: true}, true, MessageHold},
		{"the agent's own turn runs: held (a steer would answer promptRequired)", QueueState{Status: StatusIdle, Detached: true, Steering: true}, false, MessageHold},
		{"the agent's own turn waits on a person: held", QueueState{Status: StatusWaiting, Detached: true, Steering: true}, false, MessageHold},
		{"starting: a prompt (it waits for the handshake)", QueueState{Status: StatusStarting}, false, MessagePrompt},
	} {
		if got := x.q.Message(x.interrupt); got != x.want {
			t.Errorf("%s: %s, want %s", x.name, got, x.want)
		}
	}
}

// When the held messages go: the turn is over, nothing else is on its way.
func TestQueueFlush(t *testing.T) {
	for _, x := range []struct {
		name string
		q    QueueState
		want bool
	}{
		{"idle", QueueState{Status: StatusIdle, Held: 1}, true},
		{"the turn failed", QueueState{Status: StatusError, Held: 3}, true},
		{"nothing held", QueueState{Status: StatusIdle}, false},
		{"a turn runs", QueueState{Status: StatusRunning, Held: 1}, false},
		{"a turn waits on a person", QueueState{Status: StatusWaiting, Held: 1}, false},
		{"a turn is being cancelled", QueueState{Status: StatusCancelling, Held: 1}, false},
		{"starting", QueueState{Status: StatusStarting, Held: 1}, false},
		{"exited", QueueState{Status: StatusExited, Held: 1}, false},
		{"a prompt is on its way", QueueState{Status: StatusIdle, Held: 1, Sending: true}, false},
		{"the agent's own turn runs, though every status says idle", QueueState{Status: StatusIdle, Held: 1, Detached: true}, false},
	} {
		if got := x.q.Flush(); got != x.want {
			t.Errorf("%s: flush %v, want %v", x.name, got, x.want)
		}
	}
}

func TestSteerOutcome(t *testing.T) {
	for _, x := range []struct {
		outcome  string
		err      error
		action   string
		detached bool
	}{
		{SteerInjected, nil, MessageDelivered, false},
		{SteerStartedNewTurn, nil, MessageDelivered, true},
		{SteerPromptRequired, nil, MessagePrompt, false},
		{"", ErrSteeringUnsupported, MessageHold, false},
		{"", fmt.Errorf("steer: %w", ErrSteeringUnsupported), MessageHold, false},
		{"", ErrEnded, MessageRefused, false},
		{"", errors.New("the pipe broke"), MessageRefused, false},
		{SteerInjected, errors.New("an error wins over an outcome"), MessageRefused, false},
		{"somethingNewer", nil, MessageRefused, false},
	} {
		if action, detached := SteerOutcome(x.outcome, x.err); action != x.action || detached != x.detached {
			t.Errorf("%q, %v: %s detached %v, want %s detached %v", x.outcome, x.err, action, detached, x.action, x.detached)
		}
	}
}

// A detached turn is over once the agent has been quiet for Quiet; every
// event of its puts that off.
func TestDetachedTurn(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	at := func(ms int) time.Time { return t0.Add(time.Duration(ms) * time.Millisecond) }
	var d DetachedTurn
	if d.Running() || d.Over(at(0)) || !d.Deadline().IsZero() || d.Event(at(0)) || d.End() {
		t.Fatal("the zero value follows nothing")
	}
	d.Begin(at(0))
	if !d.Running() || d.Over(at(4999)) || !d.Over(at(5000)) || !d.Deadline().Equal(at(5000)) {
		t.Fatalf("the default quiet is DetachedQuiet: deadline %v", d.Deadline())
	}
	d = DetachedTurn{Quiet: 300 * time.Millisecond}
	d.Begin(at(0))
	if !d.Event(at(200)) || d.Over(at(499)) || !d.Over(at(500)) || !d.Deadline().Equal(at(500)) {
		t.Fatalf("an event puts the end off: deadline %v", d.Deadline())
	}
	d.Begin(at(600)) // begun again while one runs: it goes on
	if !d.Running() || d.Over(at(899)) || !d.Over(at(900)) {
		t.Fatalf("begun again: deadline %v", d.Deadline())
	}
	if !d.End() || d.Running() || d.Over(at(5000)) || d.Event(at(5000)) || d.End() {
		t.Fatal("an ended turn is over for good")
	}
}
