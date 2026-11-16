package term

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
)

// codex's detached turn (sdk/acp DetachedTurn; agentdetached.go), against
// the scripted agent's detach… move: a steer that lands just as the turn
// ended is answered startedNewTurn, and the agent runs a turn no prompt of
// the client's answers. The session must say it is running, hold a message
// sent meanwhile instead of prompting into the turn, and end the turn
// itself — on an interrupt, or once the agent has gone quiet.

func setDetachedQuiet(t *testing.T, d time.Duration) {
	t.Helper()
	old := detachedQuiet
	detachedQuiet = d
	t.Cleanup(func() { detachedQuiet = old })
}

// isDetachedEnd: xbind's turn.end of the agent's own turn.
func isDetachedEnd(why string) func(SessionEvent) bool {
	return func(e SessionEvent) bool {
		d := edata(e.Event)
		return e.Type == agent.EvTurnEnd && d["detached"] == true && d["stopReason"] == why
	}
}

// detach starts a turn of the agent's own playing script: the prompt's turn
// has ended, the steer was answered startedNewTurn.
func detach(t *testing.T, r *agentRig, ctx context.Context, id, script string) {
	t.Helper()
	if _, err := r.m.AgentPrompt(ctx, id, "detach"); err != nil {
		t.Fatal(err)
	}
	r.until(t, agentSays("waiting for a steer"))
	res, err := r.m.AgentQueue(ctx, id, agent.Prompt{Text: script}, false)
	if err != nil || res.Outcome != QueueSteered {
		t.Fatalf("the steer that starts the agent's own turn: %+v %v", res, err)
	}
}

func TestAgentDetachedTurnIsFollowedAndHoldsMessages(t *testing.T) {
	setDetachedQuiet(t, time.Hour) // this test ends the turn itself
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "--steer")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	detach(t, r, ctx, id, "stall here")
	// the prompt's turn ended (idle), then the agent's own began: running
	end := r.until(t, ofType(agent.EvTurnEnd))
	if d := edata(end.Event); d["turn"] != float64(1) || d["stopReason"] != "end_turn" || d["detached"] != nil {
		t.Fatalf("the prompt's own end: %s", end.Data)
	}
	// the session says running once the steer's answer is read; the agent's
	// first words in its turn come on the pump, before that or after
	for running, stalling := false, false; !running || !stalling; {
		e := r.until(t, func(e SessionEvent) bool { return isStatus(agent.StatusRunning)(e) || agentSays("stalling")(e) })
		running, stalling = running || e.Type == agent.EvStatus, stalling || e.Type != agent.EvStatus
	}
	if info, _ := m.Info(id); info.Status != agent.StatusRunning {
		t.Fatalf("the directory row while the agent's own turn runs: %q", info.Status)
	}
	// a prompt is refused as for any running turn; a message is held, not prompted into the turn
	if _, err := m.ReservePrompt(id); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("a prompt while the agent's own turn runs: %v", err)
	}
	held, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "after"}, false)
	if err != nil || held.Outcome != QueueHeld || held.ID == "" {
		t.Fatalf("a message while the agent's own turn runs: %+v %v", held, err)
	}
	if q := queueOf(r.until(t, ofType(EvQueue))); len(q.Items) != 1 || q.Items[0].ID != held.ID {
		t.Fatalf("the queue event: %+v", q)
	}
	// a status of the client's that says idle (it runs no prompt) reads running while the turn goes on
	if err := m.AgentSetOption(ctx, id, "model", "fake-fast"); err != nil {
		t.Fatal(err)
	}
	opt := r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvStatus && edata(e.Event)["options"] != nil })
	if st := edata(opt.Event)["status"]; st != agent.StatusRunning {
		t.Fatalf("a status restated during the agent's own turn says %q", st)
	}

	// an interrupt ends it here: its turn.end (no turn number: no prompt's), idle, and what was held goes in
	if err := m.AgentCancel(id); err != nil {
		t.Fatal(err)
	}
	e := r.until(t, isDetachedEnd("cancelled"))
	if _, has := edata(e.Event)["turn"]; has {
		t.Fatalf("the agent's own turn has no turn number: %s", e.Data)
	}
	r.until(t, isStatus(agent.StatusIdle))
	// What was held goes in as a prompt; the queue event saying so is logged
	// once the prompt is in, while the agent already answers it on its own
	// goroutine — so its echo, and even its turn's idle, may come before the
	// queue event or after it. Each is waited for, in whichever order.
	var sent *queueData
	echoed, idleAfter := false, false
	for sent == nil || !echoed || !idleAfter {
		e := r.until(t, func(e SessionEvent) bool {
			return e.Type == EvQueue && len(queueOf(e).Sent) > 0 || agentSays("echo: after")(e) || echoed && isStatus(agent.StatusIdle)(e)
		})
		switch {
		case e.Type == EvQueue:
			q := queueOf(e)
			sent = &q
		case agentSays("echo: after")(e):
			echoed = true
		default:
			idleAfter = true
		}
	}
	if len(sent.Sent) != 1 || sent.Sent[0] != held.ID || len(sent.Items) != 0 {
		t.Fatalf("the held message goes in at the turn's end: %+v", *sent)
	}
	if err := m.AgentCancel(id); err != nil { // nothing runs: a no-op, no second end
		t.Fatal(err)
	}
}

func TestAgentDetachedTurnEndsWhenTheAgentGoesQuiet(t *testing.T) {
	setDetachedQuiet(t, 150*time.Millisecond)
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "--steer")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// the agent's own turn asks for a permission: a turn that waits on a
	// person is not quiet, however long the answer takes
	detach(t, r, ctx, id, "perm please")
	req := r.until(t, ofType(agent.EvPermissionRequest))
	r.until(t, isStatus(agent.StatusWaiting))
	time.Sleep(4 * detachedQuiet)
	if info, _ := m.Info(id); info.Status != agent.StatusWaiting || info.Pending != 1 {
		t.Fatalf("waiting on a person, well past the quiet time: %q, %d pending", info.Status, info.Pending)
	}
	if err := m.AgentPermit(id, edata(req.Event)["pid"].(string), "once", "", "user:test"); err != nil {
		t.Fatal(err)
	}
	// answered: running again (the client, with no prompt of its own, says idle), then the script's last word
	r.until(t, isStatus(agent.StatusRunning))
	r.until(t, agentSays("listed"))
	// no turn.end comes from the agent: quiet for detachedQuiet, the turn is over
	r.until(t, isDetachedEnd("end_turn"))
	r.until(t, isStatus(agent.StatusIdle))
	if info, _ := m.Info(id); info.Status != agent.StatusIdle {
		t.Fatalf("after the quiet end: %q", info.Status)
	}
	// and the session takes a prompt again
	if res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "next"}, false); err != nil || res.Outcome != QueuePrompted {
		t.Fatalf("a message after the agent's own turn: %+v %v", res, err)
	}
	r.until(t, agentSays("echo: next"))
}
