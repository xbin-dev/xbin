// scm_handoff.go — an scm event for a person, in a partitioned agent
// (API.md §scm events and polling; /docs/partitions.md §Partition mail).
// The provider delivers every event to the agent's global instance; one
// `for: user:<id>` belongs to that person's partition, where their
// projects, tasks and subscriptions live:
//
//   - global (after its transport dedupe) records a handoff of kind "scm"
//     — the event as it came, with the provider the caller was — in the
//     transaction that took it (queueHandoff, as an event for a person's
//     private trigger is: trigger_registry.go handEvent), and mails it as
//     `handoff/scm` after the commit (handoff_send.go). Global keeps nothing
//     of it once mailed. The mail names the provider as its source.
//   - the person's partition (handleSCMHandoff, registered for the topic)
//     takes it only from global, only for its own person, and only when the
//     event's forPid is this partition's id: a person deleted and made again
//     under the same id is another person, with another partition id, and
//     never gets the old one's events (dropped, counted). It dedupes on the
//     eventId again and handles it as any home does (scmTake).
package main

import (
	"context"
	"encoding/json"
	"strings"
)

// scmHandoffKind is handoffs.kind of an scm event on its way to a person.
const scmHandoffKind = "scm"

func init() {
	mailHandlers[topicSCM] = handleSCMHandoff
}

// scmHandOn queues ev for person's partition (global, in the transaction
// that took it); it is mailed after the commit.
func scmHandOn(t *DB, person string, ev *scmEvent) error {
	src := strings.SplitN(ev.SCM.Provider, "#", 2)[0]
	if err := t.queueHandoff(scmHandoffKind, person, newHandoffID(), ev, func(q *handoffRow) { q.source = src }); err != nil {
		return err
	}
	t.AfterCommit(kickHandoffs)
	return nil
}

// handleSCMHandoff takes an scm event global handed this person (in the
// transaction that records the mail item as seen).
func handleSCMHandoff(_ context.Context, t *DB, it mailItem) error {
	if !userMode() || it.From != "global" {
		logf("handoff/scm %s from %q: only the global instance hands a person an scm event — refused", it.ID, it.From)
		scmDrop("not-global")
		return nil
	}
	var ev scmEvent
	if err := json.Unmarshal(it.Data, &ev); err != nil || ev.EventID == "" || ev.Kind == "" || ev.Protocol != scmProtocol {
		logf("handoff/scm %s: malformed — dropped", it.ID)
		scmDrop("malformed")
		return nil
	}
	if ev.For != partitionKey() {
		logf("handoff/scm %s: for %q, not this partition (%s) — dropped", it.ID, ev.For, partitionKey())
		scmDrop("not-ours")
		return nil
	}
	if pid := partitionID(); pid == "" || ev.ForPid != pid {
		// fail closed: an event with no partition id, or one made for an
		// earlier person of the same id, is never this person's
		logf("handoff/scm %s: for partition %q, this is %q — dropped", it.ID, ev.ForPid, pid)
		scmDrop("for-pid")
		return nil
	}
	bound := false
	for _, b := range scmBound() {
		bound = bound || b == ev.SCM.Provider
	}
	if !bound {
		logf("handoff/scm %s: %s isn't bound here — dropped", it.ID, ev.SCM.Provider)
		scmDrop("not-bound")
		return nil
	}
	if !t.features || !scmSeenOnce(t, ev.EventID) {
		return nil
	}
	return scmTake(t, &ev)
}
