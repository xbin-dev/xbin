package term

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// nextStatus waits for the next published summary (8 s).
func (r *agentRig) nextStatus(t *testing.T) StatusChange {
	t.Helper()
	select {
	case st := <-r.status:
		return st
	case <-time.After(8 * time.Second):
		t.Fatal("no status change")
		return StatusChange{}
	}
}

// statusUntil collects summaries until one satisfies pred; none repeats the
// one before it (only changes are published).
func (r *agentRig) statusUntil(t *testing.T, prev *StatusChange, pred func(StatusChange) bool) []StatusChange {
	t.Helper()
	var seen []StatusChange
	for {
		st := r.nextStatus(t)
		if prev != nil && st == *prev {
			t.Fatalf("a summary was published twice: %+v (after %+v)", st, seen)
		}
		cp := st
		prev = &cp
		seen = append(seen, st)
		if pred(st) {
			return seen
		}
	}
}

func fmtStatuses(ss []StatusChange) string {
	var b strings.Builder
	for _, s := range ss {
		fmt.Fprintf(&b, "%s/p%d/q%d ", s.Status, s.Pending, s.Questions)
	}
	return b.String()
}

// The inbox's stream: an agent session publishes a summary (status, pending
// permissions, pending questions) on every change — running, waiting with a
// count, back to idle, and a final exited before the directory's close —
// never the same summary twice, and nothing for the events in between
// (deltas, tool calls, usage).
func TestAgentStatusChanges(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	info, code, err := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	id := info.ID
	seen := r.statusUntil(t, nil, func(s StatusChange) bool { return s.Status == agent.StatusIdle })
	last := seen[len(seen)-1]
	if last.ID != id || last.User != "owner" || last.Pending != 0 || last.Questions != 0 {
		t.Fatalf("idle summary: %+v", last)
	}

	// a permission: running → waiting (1 pending) → … → idle
	ctx := context.Background()
	if _, err := m.AgentPrompt(ctx, id, "perm"); err != nil {
		t.Fatal(err)
	}
	seen = r.statusUntil(t, &last, func(s StatusChange) bool { return s.Status == agent.StatusWaiting && s.Pending == 1 })
	last = seen[len(seen)-1]
	if last.Turn != 1 {
		t.Fatalf("the turn's summaries: %s (turn %d)", fmtStatuses(seen), last.Turn)
	}
	pend, _ := m.AgentPending(id)
	if len(pend) != 1 {
		t.Fatalf("pending: %+v", pend)
	}
	if err := m.AgentPermit(id, pend[0].PID, "", agent.AllowOnce, "owner"); err != nil {
		t.Fatal(err)
	}
	seen = r.statusUntil(t, &last, func(s StatusChange) bool { return s.Status == agent.StatusIdle })
	for _, s := range seen {
		if s.Pending != 0 {
			t.Fatalf("after the answer: %s", fmtStatuses(seen))
		}
	}
	last = seen[len(seen)-1]

	// a question counts separately
	if _, err := m.AgentPrompt(ctx, id, "ask me"); err != nil {
		t.Fatal(err)
	}
	seen = r.statusUntil(t, &last, func(s StatusChange) bool { return s.Status == agent.StatusWaiting && s.Questions == 1 })
	last = seen[len(seen)-1]
	if last.Pending != 0 {
		t.Fatalf("question summary: %s", fmtStatuses(seen))
	}
	qs, _ := m.AgentQuestions(id)
	if len(qs) != 1 {
		t.Fatalf("questions: %+v", qs)
	}
	if err := m.AgentElicit(id, qs[0].EID, "accept", json.RawMessage(`{"question_0":"SQLite"}`), "owner"); err != nil {
		t.Fatal(err)
	}
	seen = r.statusUntil(t, &last, func(s StatusChange) bool { return s.Status == agent.StatusIdle && s.Questions == 0 })
	last = seen[len(seen)-1]

	// a plain turn: at most running, then idle one turn on — its deltas,
	// usage and title publish nothing else
	if _, err := m.AgentPrompt(ctx, id, "burst"); err != nil {
		t.Fatal(err)
	}
	seen = r.statusUntil(t, &last, func(s StatusChange) bool { return s.Status == agent.StatusIdle })
	if got := fmtStatuses(seen); (got != "running/p0/q0 idle/p0/q0 " && got != "idle/p0/q0 ") || seen[len(seen)-1].Turn != last.Turn+1 {
		t.Fatalf("a plain turn: %s (turn %d after %d)", got, seen[len(seen)-1].Turn, last.Turn)
	}

	// the end: exited, then the directory's close
	m.Kill(id)
	r.statusUntil(t, nil, func(s StatusChange) bool { return s.Status == agent.StatusExited || s.Status == agent.StatusError })
	waitClose(t, r.change, "close:"+id)
	select {
	case st := <-r.status:
		t.Fatalf("a summary after the end: %+v", st)
	case <-time.After(100 * time.Millisecond):
	}
}

// A glance (the compact session read's turn and last line): the prompts an
// agent session took so far and the last line it wrote since its last
// prompt — kept as the log is written, never read back out of it.
func TestAgentGlance(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	info, code, err := m.OpenAgent(auth.Principal{Owner: true}, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || code != 200 {
		t.Fatalf("open: %d %v", code, err)
	}
	id := info.ID
	last := r.statusUntil(t, nil, func(s StatusChange) bool { return s.Status == agent.StatusIdle })
	if turn, line, ok := m.AgentGlance(id); !ok || turn != 0 || line != "" {
		t.Fatalf("before any prompt: turn %d, last %q, ok %v", turn, line, ok)
	}
	ctx := context.Background()
	for i, say := range []string{"hi", "first\n\nsecond"} {
		if _, err := m.AgentPrompt(ctx, id, say); err != nil {
			t.Fatal(err)
		}
		prev := last[len(last)-1]
		last = r.statusUntil(t, &prev, func(s StatusChange) bool { return s.Status == agent.StatusIdle && int(s.Turn) == i+1 })
		want := "echo: hi"
		if i == 1 {
			want = "second" // the last line that isn't blank; the first turn's text is not this turn's
		}
		if turn, line, ok := m.AgentGlance(id); !ok || int(turn) != i+1 || line != want {
			t.Fatalf("after turn %d: turn %d, last %q, ok %v; want %q", i+1, turn, line, ok, want)
		}
	}
	if _, _, ok := m.AgentGlance("no-such-session"); ok {
		t.Fatal("a glance at a session that isn't there")
	}

	// the line itself: the last one that isn't blank, its end when it is long, never half a rune
	for _, c := range []struct{ in, want string }{
		{"", ""}, {"\n \n", ""}, {"one\ntwo\n\n", "two"}, {"  padded  ", "padded"},
		{strings.Repeat("a", lastLineMax+50) + "é", "…" + strings.Repeat("a", lastLineMax-2) + "é"},
		{"ok\xff\xfe", "ok"},
	} {
		if got := lastLine([]byte(c.in)); got != c.want {
			t.Errorf("lastLine(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	// a failure says why, in place of what the agent wrote
	st := &agentState{}
	st.noteSaid(agent.Event{Type: agent.EvMessageDelta, Data: json.RawMessage(`{"role":"agent","text":"working on it"}`)})
	st.noteSaid(agent.Event{Type: agent.EvMessageDelta, Data: json.RawMessage(`{"role":"agent","text":"a subagent's","parent":"t1"}`)})
	if got := lastLine(st.said); got != "working on it" {
		t.Fatalf("with a subagent's text: %q", got)
	}
	st.noteSaid(agent.Event{Type: agent.EvStatus, Data: json.RawMessage(`{"status":"error","detail":"the agent exited: signal: killed"}`)})
	if got := lastLine(st.said); got != "the agent exited: signal: killed" {
		t.Fatalf("after a failure: %q", got)
	}
	st.noteSaid(agent.Event{Type: agent.EvMessageDelta, Data: json.RawMessage(`{"role":"agent","text":"` + strings.Repeat("x", 3*saidKeep) + `"}`)})
	if len(st.said) > 2*saidKeep {
		t.Fatalf("the kept tail is %d bytes", len(st.said))
	}
}
