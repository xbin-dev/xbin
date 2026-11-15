package acp

// A session in a word and in a line: what a list of sessions, an inbox
// ("Needs you") and a notification say about one — the same on every
// surface, whoever hosts the session. Three things:
//
//   - the status classes every host asks about (Busy, TurnOver);
//   - Summary, the few numbers a list needs, and the one word for its state
//     (StateOf: starting, ready, working, login, failed, stopped);
//   - Need, what a session waits on a person for, as a heading and a
//     sentence.

import "strings"

// Busy reports whether a turn of the client's runs: running, waiting for a
// person's answer, or being cancelled.
func Busy(status string) bool {
	return status == StatusRunning || status == StatusWaiting || status == StatusCancelling
}

// TurnOver reports whether the session can take a prompt: it is idle, or
// its last turn failed.
func TurnOver(status string) bool { return status == StatusIdle || status == StatusError }

// A session's state in one word (StateOf).
const (
	StateStarting = "starting" // the agent is being started
	StateReady    = "ready"    // idle: it takes a prompt
	StateWorking  = "working"  // a turn runs (or waits on a person: Summary.Waiting says which)
	StateLogin    = "login"    // signed out: it needs a person to sign it in
	StateFailed   = "failed"   // the session, or its last turn, failed
	StateStopped  = "stopped"  // no agent runs
)

// StateOf is a session status as the word a list shows. signedOut: the
// agent says it is signed out (a status event's login, SessionState
// .AuthNeeded) — that is what a session at rest says first.
func StateOf(status string, signedOut bool) string {
	switch {
	case Busy(status):
		return StateWorking
	case status == StatusStarting:
		return StateStarting
	case signedOut && status != StatusExited && status != "":
		return StateLogin
	case status == StatusIdle:
		return StateReady
	case status == StatusError:
		return StateFailed
	}
	return StateStopped
}

// Summary is a session in a line — what changes it is what an inbox needs
// to hear about: its status, how many permission requests and questions
// wait for an answer, and how many turns it took (so a turn that ran and
// ended between two summaries still shows: idle → idle with a higher turn).
// Comparable: two summaries that are equal say the same.
type Summary struct {
	Status    string `json:"status"`    // starting | idle | running | waiting_permission | cancelling | error | exited
	Pending   int    `json:"pending"`   // unanswered permission requests
	Questions int    `json:"questions"` // unanswered questions (elicitation.request)
	Turn      uint64 `json:"turn"`      // prompts taken so far
}

// Busy reports whether a turn runs.
func (s Summary) Busy() bool { return Busy(s.Status) }

// Waiting reports whether the session waits on a person: its status says
// so, or a request is unanswered.
func (s Summary) Waiting() bool { return s.Status == StatusWaiting || s.Pending+s.Questions > 0 }

// What a session waits on a person for (Need.Kind).
const (
	NeedApproval = "approval" // a permission request
	NeedQuestion = "question" // a question (an elicitation)
	NeedLogin    = "login"    // the agent is signed out
)

// Need is what a session waits on a person for, in words.
type Need struct {
	Kind  string // NeedApproval | NeedQuestion | NeedLogin
	Agent string // the agent's name ("": "The agent")
	// What: an approval's call (its title, a command), a question's own
	// words. "" when the request says nothing.
	What string
}

func (n Need) agent() string {
	if n.Agent != "" {
		return n.Agent
	}
	return "The agent"
}

// Heading is the need in a few words, for a card's or a notification's
// title: "Permission needed", "Question", "Sign in to ‹agent›".
func (n Need) Heading() string {
	switch n.Kind {
	case NeedApproval:
		return "Permission needed"
	case NeedLogin:
		if n.Agent == "" {
			return "Sign in to the agent"
		}
		return "Sign in to " + n.Agent
	}
	return "Question"
}

// Text is the need as a sentence: "‹agent› wants to run ‹what› — approve
// or deny.", the question's own words (else "‹agent› is waiting for your
// answer."), "‹agent› needs you to sign in to it." — bounded (an approval's
// lead at 200 bytes, a question at 240), cut on a character with an
// ellipsis.
func (n Need) Text() string {
	switch n.Kind {
	case NeedApproval:
		what := n.What
		if what == "" {
			what = "a command"
		}
		return clipText(n.agent()+" wants to run "+what, 200) + " — approve or deny."
	case NeedLogin:
		return n.agent() + " needs you to sign in to it."
	}
	if n.What != "" {
		return clipText(n.What, 240)
	}
	return n.agent() + " is waiting for your answer."
}

// clipText cuts s to n bytes (on a character) with an ellipsis.
func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "") + "…"
}
