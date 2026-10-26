package term

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// Messages while a turn runs (D197): held for the turn's end, steered into
// it, interrupting it, taken back, dropped at the session's end — against
// the scripted agent (hack/fakeacp; --steer serves _session/steering).

// openFake opens a fake-agent session (args: the fake's flags) and waits
// for it to be idle.
func openFake(t *testing.T, r *agentRig, args string) string {
	t.Helper()
	if args != "" {
		t.Setenv(agent.FakeEnv, fakeBin+" "+args)
	}
	info, code, err := r.m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil {
		t.Fatalf("open: %d %v", code, err)
	}
	t.Cleanup(func() { endFake(t, r, info.ID) })
	r.until(t, isStatus(agent.StatusIdle))
	return info.ID
}

// endFake kills the session and waits for its teardown — its history is
// written under the rig's root, which the test's TempDir cleanup removes.
func endFake(t *testing.T, r *agentRig, id string) {
	t.Helper()
	_, st, err := r.m.agentOf(id)
	if err != nil {
		return // out of the map already: its history is written (saveHistory, then onExit)
	}
	r.m.Kill(id)
	select {
	case <-st.gone:
	case <-time.After(10 * time.Second):
		t.Errorf("session %s: not torn down 10 s after its kill", id)
	}
}

func isStatus(st string) func(SessionEvent) bool {
	return func(e SessionEvent) bool { return e.Type == agent.EvStatus && edata(e.Event)["status"] == st }
}

// agentSays: an agent message.delta whose text has s.
func agentSays(s string) func(SessionEvent) bool {
	return func(e SessionEvent) bool {
		d := edata(e.Event)
		txt, _ := d["text"].(string)
		return e.Type == agent.EvMessageDelta && d["role"] == "agent" && strings.Contains(txt, s)
	}
}

func queueOf(e SessionEvent) queueData {
	var d queueData
	_ = json.Unmarshal(e.Data, &d)
	return d
}

func TestAgentQueueHeldUntilTheTurnEnds(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// idle: a message is a prompt
	res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "hi"}, false)
	if err != nil || res.Outcome != QueuePrompted || res.Turn != 1 {
		t.Fatalf("idle: %+v %v", res, err)
	}
	r.until(t, agentSays("echo: hi"))
	r.until(t, isStatus(agent.StatusIdle))

	// busy, no steering: held, in order, and the tail page says so
	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	a, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "first"}, false)
	if err != nil || a.Outcome != QueueHeld || a.ID == "" {
		t.Fatalf("busy: %+v %v", a, err)
	}
	b, _ := m.AgentQueue(ctx, id, agent.Prompt{Text: "second"}, false)
	q := queueOf(r.until(t, func(e SessionEvent) bool { return e.Type == EvQueue && len(queueOf(e).Items) == 2 }))
	if q.Items[0].Text != "first" || q.Items[1].ID != b.ID {
		t.Fatalf("queue event: %+v", q)
	}
	page, _ := m.AgentPage(id, 0, 50)
	var held []Queued
	if json.Unmarshal(page.State.Queue, &held) != nil || len(held) != 2 {
		t.Fatalf("the tail page's state: %s", page.State.Queue)
	}
	// a plain prompt is still refused while the turn runs (older clients)
	if _, err := m.AgentPrompt(ctx, id, "x"); !errors.Is(err, agent.ErrBusy) {
		t.Fatalf("prompt while busy: %v", err)
	}

	// the turn ends: both go in as one prompt, the queue says they went
	r.until(t, func(e SessionEvent) bool { return e.Type == agent.EvTurnEnd })
	sent := r.until(t, func(e SessionEvent) bool { return e.Type == EvQueue && len(queueOf(e).Sent) > 0 })
	if d := queueOf(sent); len(d.Items) != 0 || len(d.Sent) != 2 || d.Sent[0] != a.ID {
		t.Fatalf("sent: %+v", d)
	}
	r.until(t, agentSays("echo: first\n\nsecond"))
	if page, _ := m.AgentPage(id, 0, 50); len(page.State.Queue) != 0 {
		t.Fatalf("nothing held now: %s", page.State.Queue)
	}
}

func TestAgentQueueSteersIntoTheTurn(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "--steer")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := m.AgentPrompt(ctx, id, "steer me"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "look at the tests too"}, false)
	if err != nil || res.Outcome != QueueSteered {
		t.Fatalf("steer: %+v %v", res, err)
	}
	echo := r.until(t, func(e SessionEvent) bool {
		return e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "user" && edata(e.Event)["steered"] == true
	})
	if edata(echo.Event)["text"] != "look at the tests too" {
		t.Fatalf("steered echo: %v", edata(echo.Event))
	}
	r.until(t, agentSays("steers: look at the tests too"))
	r.until(t, isStatus(agent.StatusIdle))

	// idle again: a message is a prompt (the steer route is not asked)
	if res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "next"}, false); err != nil || res.Outcome != QueuePrompted {
		t.Fatalf("idle with steering: %+v %v", res, err)
	}
}

func TestAgentQueueInterrupt(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "--steer") // interrupt never steers: it ends the turn, then sends
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "stop, do this"}, true)
	if err != nil || res.Outcome != QueueHeld {
		t.Fatalf("interrupt: %+v %v", res, err)
	}
	end := r.until(t, ofType(agent.EvTurnEnd))
	if edata(end.Event)["stopReason"] != "cancelled" {
		t.Fatalf("the running turn ends cancelled: %v", edata(end.Event))
	}
	r.until(t, agentSays("echo: stop, do this"))
}

func TestAgentUnqueue(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	long := strings.Repeat("y", 500)
	a, _ := m.AgentQueue(ctx, id, agent.Prompt{Text: long}, false)
	b, _ := m.AgentQueue(ctx, id, agent.Prompt{Text: "b"}, false)
	ev := r.until(t, func(e SessionEvent) bool { return e.Type == EvQueue && len(queueOf(e).Items) == 2 })
	if got := queueOf(ev).Items[0].Text; len([]rune(got)) != queueEventText+1 || !strings.HasSuffix(got, "…") {
		t.Fatalf("the event carries a preview: %d runes", len([]rune(got)))
	}
	if _, err := m.AgentUnqueue(id, "nope"); !errors.Is(err, ErrNoQueued) {
		t.Fatalf("unknown id: %v", err)
	}
	got, err := m.AgentUnqueue(id, b.ID)
	if err != nil || len(got) != 1 || got[0].Text != "b" {
		t.Fatalf("one: %+v %v", got, err)
	}
	got, err = m.AgentUnqueue(id, "")
	if err != nil || len(got) != 1 || got[0].ID != a.ID || got[0].Text != long {
		t.Fatalf("all, whole: %+v %v", got, err)
	}
	// nothing held: the turn ends and nothing follows it
	r.until(t, ofType(agent.EvTurnEnd))
	r.until(t, isStatus(agent.StatusIdle))
	time.Sleep(300 * time.Millisecond)
	for {
		select {
		case e := <-r.events:
			if e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "user" {
				t.Fatalf("a taken-back message went in: %v", edata(e.Event))
			}
			continue
		default:
		}
		break
	}
}

func TestAgentQueueLimits(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	release, err := m.ReserveQueue(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.ReserveQueue(id); !errors.Is(err, ErrQueueTaken) {
		t.Fatalf("a second message being taken: %v", err)
	}
	release()

	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	for i := 0; i < maxQueued; i++ {
		if res, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "m"}, false); err != nil || res.Outcome != QueueHeld {
			t.Fatalf("%d: %+v %v", i, res, err)
		}
	}
	if _, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "one too many"}, false); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("full: %v", err)
	}
}

func TestAgentQueueDroppedWhenTheSessionEnds(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	id := openFake(t, r, "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := m.AgentPrompt(ctx, id, "slow"); err != nil {
		t.Fatal(err)
	}
	r.until(t, isStatus(agent.StatusRunning))
	if _, err := m.AgentQueue(ctx, id, agent.Prompt{Text: "later"}, false); err != nil {
		t.Fatal(err)
	}
	endFake(t, r, id)
	d := queueOf(r.until(t, func(e SessionEvent) bool { return e.Type == EvQueue && queueOf(e).Dropped > 0 }))
	if d.Dropped != 1 || len(d.Items) != 0 || d.Reason == "" {
		t.Fatalf("dropped: %+v", d)
	}
	n := r.until(t, ofType(EvNotice))
	if txt, _ := edata(n.Event)["text"].(string); !strings.Contains(txt, "1 queued message was not sent") {
		t.Fatalf("notice: %q", txt)
	}
}
