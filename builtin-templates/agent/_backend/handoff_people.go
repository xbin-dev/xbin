// handoff_people.go — whose partitions have run, as a partitioned agent's
// global instance learns it, for the first-DM notice (plans: 09 §3). Mail
// never starts a person's partition that has never run, so a DM handed to
// someone who has never opened the agent waits in their inbox until they
// do. The global instance can't ask xbind; it learns it from the partition
// itself: a person's partition mails `partition/hello` when it first
// starts (once per database), and any mail from it (a reply, usage totals)
// says the same. Until then a linked DM gets a short notice — the first
// one only — and the chat isn't shown the agent working.
//
// partition_people keeps per person only two flags: their partition ran,
// they were told. No times, no activity.
package main

import (
	"context"
	"strings"
	"time"
)

// topicHello: a person → global: their partition runs.
const topicHello = "partition/hello"

// firstDMNotice answers a linked person's DM while their partition has never
// run.
const firstDMNotice = "This chat account is linked to your own space in this agent, which hasn't started yet: open the agent once " +
	"(in xbin) to receive your messages there. Your message waits for it — up to 7 days — and is answered then."

func init() {
	mailHandlers[topicHello] = handleHello
}

// partitionRan (the global instance, in handDM's transaction): whether
// person's partition has run. When it hasn't, the chat is told why nothing
// answers yet — the first time only.
func (d *DB) partitionRan(person string, chID int64, key, addr string) bool {
	var seen, noticed int
	_ = d.q.QueryRow(`SELECT seen, noticed FROM partition_people WHERE person=?`, person).Scan(&seen, &noticed)
	if seen != 0 {
		return true
	}
	if noticed == 0 {
		d.outboxAdd(chID, key, 0, "notice", addr, firstDMNotice)
		_, _ = d.q.Exec(`INSERT INTO partition_people (person, noticed) VALUES (?, 1) ON CONFLICT(person) DO UPDATE SET noticed=1`, person)
	}
	return false
}

// markRan records that person's partition runs (the global instance: it
// mailed us).
func (d *DB) markRan(person string) {
	_, _ = d.q.Exec(`INSERT INTO partition_people (person, seen) VALUES (?, 1) ON CONFLICT(person) DO UPDATE SET seen=1`, person)
}

// handleHello (the global instance): a person's partition started.
func handleHello(_ context.Context, t *DB, it mailItem) error {
	person, ok := strings.CutPrefix(it.From, "user:")
	if !globalMode() || !ok || person == "" {
		logf("partition/hello %s from %q: only a person's partition says hello to the global instance — ignored", it.ID, it.From)
		return nil
	}
	t.markRan(person)
	return nil
}

// sayHello (a person's partition, at start): tells the global instance this
// partition runs — once per database; a failure is tried at the next start.
func (ag *Agent) sayHello(ctx context.Context) {
	if !userMode() || ag.db.getSetting("hello_sent") != "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := sendMail(cctx, "global", topicHello, map[string]any{}, ""); err != nil {
		logf("partition/hello: %v (tried again at the next start)", err)
		return
	}
	_ = ag.db.putSetting("hello_sent", "1")
}
