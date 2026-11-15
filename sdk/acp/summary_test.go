package acp

import (
	"strings"
	"testing"
)

func TestStatusClasses(t *testing.T) {
	for _, x := range []struct {
		status         string
		busy, turnOver bool
		state, signed  string // signed: the state when the agent says it is signed out
	}{
		{StatusStarting, false, false, StateStarting, StateStarting},
		{StatusIdle, false, true, StateReady, StateLogin},
		{StatusRunning, true, false, StateWorking, StateWorking},
		{StatusWaiting, true, false, StateWorking, StateWorking},
		{StatusCancelling, true, false, StateWorking, StateWorking},
		{StatusError, false, true, StateFailed, StateLogin},
		{StatusExited, false, false, StateStopped, StateStopped},
		{"", false, false, StateStopped, StateStopped},
		{"something-newer", false, false, StateStopped, StateLogin},
	} {
		if Busy(x.status) != x.busy || TurnOver(x.status) != x.turnOver {
			t.Errorf("%q: busy %v, turn over %v", x.status, Busy(x.status), TurnOver(x.status))
		}
		if got := StateOf(x.status, false); got != x.state {
			t.Errorf("%q: state %q, want %q", x.status, got, x.state)
		}
		if got := StateOf(x.status, true); got != x.signed {
			t.Errorf("%q signed out: state %q, want %q", x.status, got, x.signed)
		}
	}
}

func TestSummary(t *testing.T) {
	for _, x := range []struct {
		s             Summary
		busy, waiting bool
	}{
		{Summary{Status: StatusIdle, Turn: 3}, false, false},
		{Summary{Status: StatusRunning}, true, false},
		{Summary{Status: StatusWaiting}, true, true},
		{Summary{Status: StatusRunning, Pending: 1}, true, true},   // the status that follows a request not yet seen
		{Summary{Status: StatusRunning, Questions: 2}, true, true}, // a question
		{Summary{Status: StatusCancelling}, true, false},
		{Summary{Status: StatusExited}, false, false},
	} {
		if x.s.Busy() != x.busy || x.s.Waiting() != x.waiting {
			t.Errorf("%+v: busy %v, waiting %v", x.s, x.s.Busy(), x.s.Waiting())
		}
	}
	if (Summary{Status: StatusIdle, Turn: 1}) == (Summary{Status: StatusIdle, Turn: 2}) {
		t.Error("a turn that ran between two summaries must show")
	}
}

func TestNeedWords(t *testing.T) {
	for _, x := range []struct {
		n             Need
		heading, text string
	}{
		{Need{Kind: NeedApproval, Agent: "Codex", What: "go test ./..."}, "Permission needed", "Codex wants to run go test ./... — approve or deny."},
		{Need{Kind: NeedApproval}, "Permission needed", "The agent wants to run a command — approve or deny."},
		{Need{Kind: NeedQuestion, Agent: "Claude Code", What: "Which branch?"}, "Question", "Which branch?"},
		{Need{Kind: NeedQuestion, Agent: "Claude Code"}, "Question", "Claude Code is waiting for your answer."},
		{Need{Kind: NeedQuestion}, "Question", "The agent is waiting for your answer."},
		{Need{Kind: NeedLogin, Agent: "Codex"}, "Sign in to Codex", "Codex needs you to sign in to it."},
		{Need{Kind: NeedLogin}, "Sign in to the agent", "The agent needs you to sign in to it."},
		{Need{}, "Question", "The agent is waiting for your answer."},
	} {
		if x.n.Heading() != x.heading || x.n.Text() != x.text {
			t.Errorf("%+v: %q / %q, want %q / %q", x.n, x.n.Heading(), x.n.Text(), x.heading, x.text)
		}
	}
	long := Need{Kind: NeedApproval, Agent: "A", What: strings.Repeat("é", 300)}.Text()
	if !strings.HasSuffix(long, "… — approve or deny.") || len(long) > 200+len("… — approve or deny.") || strings.ContainsRune(long, '�') {
		t.Errorf("a long approval is cut on a character: %d bytes, %q", len(long), long[len(long)-30:])
	}
	if q := (Need{Kind: NeedQuestion, What: strings.Repeat("x", 500)}).Text(); len(q) != 240+len("…") {
		t.Errorf("a long question: %d bytes", len(q))
	}
}
