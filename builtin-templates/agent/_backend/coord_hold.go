// coord_hold.go — every coordinator's rule (coord_profile.go): it never
// answers a person's question or approval. What a coordinator started may
// stop and wait for a person — an approval, a question, a sign-in — and
// that park is the person's: the coordinator can't answer it, decline it
// or withdraw it. So a message a coordinator (or anything else that isn't
// a person: an event, a platform round) sends something that waits is
// held, in order, until the person answered — never delivered where it
// would be taken for the reply — and the coordinator is told what waits,
// quoted, how long, and who must answer.
//
// What a profile does with the rule: its message tool asks coordHolds
// before it delivers and holds what it may not (Projects: the queue's
// hold_park, project_queue.go); when what it started parks with such a
// message already on its way, the message goes back behind the park
// (Projects: coordHoldPark); and its tool answers in parkCoordWords, so
// the coordinator reads a question as the question and not as "waiting".
package main

// coordHolds: something in this run status waits for a person — a
// coordinator's message to it is held until they answered.
func coordHolds(status string) bool { return status == statusWaiting }

// coordHeldSource: an input from src is never a person's reply (a
// coordinator's message, an event's, a platform round's): it is held
// behind a park, and taken back when its run parks before taking it up.
func coordHeldSource(src string) bool {
	return src == srcCoordinator || src == srcEvent || src == srcDream
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
