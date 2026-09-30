// handoff_people.go — whose partitions have run, as a partitioned agent's
// global instance learns it. Mail never starts a person's partition that
// has never run, so a DM handed to someone who has never opened the agent
// waits in their inbox until they do — like any unread message: nothing
// answers the chat for it (90 §I12: a notification, if one is ever added,
// is the inbox's — a doorbell for whatever must know — not a reply to the
// sender). What global does with the fact is only the chat's typing status:
// `working` once the person's partition has run, `idle` before. The global
// instance can't ask xbind; it learns it from the partition itself: a
// person's partition mails `partition/hello` when it first starts (once per
// database), and any mail from it (a reply, usage totals) says the same.
//
// partition_people keeps per person one flag: their partition ran (its
// `noticed` column is no longer written). No times, no activity.
package main

import (
	"context"
	"strings"
	"time"
)

// topicHello: a person → global: their partition runs.
const topicHello = "partition/hello"

func init() {
	mailHandlers[topicHello] = handleHello
}

// partitionRan (the global instance): whether person's partition has run.
func (d *DB) partitionRan(person string) bool {
	var seen int
	_ = d.q.QueryRow(`SELECT seen FROM partition_people WHERE person=?`, person).Scan(&seen)
	return seen != 0
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
