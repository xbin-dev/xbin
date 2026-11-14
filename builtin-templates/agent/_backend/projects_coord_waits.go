// projects_coord_waits.go — what a task in the board's Needs you column
// waits for, in the coordinator's words (D3): a person's question or
// approval — quoted, with its age — is not the same as a task whose turn
// ended with its answer ready, though both are needs-you on the board.
// Only a person answers a question: task_message while one waits says so,
// and the message is held (HoldPark) until they have.
package main

import (
	"fmt"
)

// taskWait is what a task's run waits on (TaskView.wait): its park while it
// waits for a person, and whether its coding agent is being reconnected
// after a restart (an attach tried again, harness_pass.go).
type taskWait struct {
	park      *runPark
	reconnect bool
}

// taskWaitOf is run's taskWait (nil: nothing to say).
func (d *DB) taskWaitOf(run *Run) *taskWait {
	w := &taskWait{}
	if run.Status == statusWaiting && run.ParentID == 0 {
		pk := d.parkOf(run)
		w.park = &pk
	}
	if e := projEng(); e != nil && run.Engine == engineHarness && e.reattaching(run.ID) {
		w.reconnect = true
	}
	if w.park == nil && !w.reconnect {
		return nil
	}
	return w
}

// coordQuote is a park's text as the coordinator reads it: one line,
// redacted, its frames' markers defused, quoted.
func coordQuote(s string) string {
	return "«" + clip(coordPlain(projRedact(s)), 200) + "»"
}

// coordAsked is " (asked 2h ago)" ("" when the age isn't known).
func coordAsked(since int64) string {
	if since <= 0 {
		return ""
	}
	return " (asked " + ago(since) + ")"
}

// coordWaitWords is v's state in the coordinator's words when its board
// state alone would mislead ("": coordStateWords says it).
func coordWaitWords(v TaskView) string {
	w := v.wait
	switch {
	case w != nil && w.reconnect:
		return "reconnecting to its coding agent (after a restart)"
	case v.State != taskNeedsYou:
		return ""
	case w != nil && w.park != nil:
		return parkCoordWords(*w.park, "waiting for")
	case v.RunStatus != statusWaiting:
		return "its turn ended — its answer is ready (task_result); a message from you starts its next turn"
	}
	return ""
}

// parkCoordWords is a person's park in the coordinator's words, after
// verb ("waiting for", "waits for").
func parkCoordWords(pk runPark, verb string) string {
	age := coordAsked(pk.Since)
	switch pk.Kind {
	case needApproval:
		return verb + " a person to approve: " + coordQuote(pk.Text) + age
	case needLogin:
		return verb + " a person to sign in to its coding agent" + age
	case "project":
		return "its workspace " + verb + " a person: " + coordQuote(pk.Text) + age
	}
	if pk.Text == "" {
		return verb + " a person to answer its question" + age
	}
	return verb + " a person to answer: " + coordQuote(pk.Text) + age
}

// coordHeldWords is task_message's answer for a message held behind a
// person's park on task n's run: what waits, that only a person answers it
// and where they see it, and that the message follows their answer.
func (d *DB) coordHeldWords(n int64, run *Run) string {
	pk := d.parkOf(run)
	if pk.Kind == "project" {
		return fmt.Sprintf("queued for task #%d, not delivered yet: its workspace waits for a person — %s%s. "+
			"Your message is delivered once that is settled.", n, coordQuote(pk.Text), coordAsked(pk.Since))
	}
	return fmt.Sprintf("queued for task #%d, not delivered yet: it %s. Only a person may answer it — you can't answer "+
		"or decline it for them; it is on their Needs you list and on the project's page. Your message is delivered "+
		"once they have answered.", n, parkCoordWords(pk, "waits for"))
}
