package broker

// partitionbus.go — a partitioned scope's bus (plans/partitions/02 §9,
// 04 §2). A bus of a partitioned scope that isn't shared is per partition,
// like its kv and blobs: every event published on it is stamped with the
// partition whose namespace it is in — "global" for the global instance's
// (today's keys), "user:<id>" for a person's — and reaches only subscribers
// acting in that partition on the scope. An unstamped event on it reaches
// no one there. A shared bus carries no stamp and reaches every reader, as
// today; a "read" one refuses people's partitions' publishes (readClamp,
// 403). A scope no tile partitions is untouched: no stamp, no filter.
//
// The stamp is events.Event.Partition, the identity plane's field (F2),
// reached through the two seams below, which partitionwire.go points at
// that field (TestBusPartitionStampWired pins it). Unwired, a partitioned
// scope's own bus carries nothing: a publish answers 503 and nothing on it
// is delivered, rather than reach every partition. /ws/events' admin pass
// never covers a stamped bus event (server.eventFilter, 02 §9 G2).

import (
	"net/http"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/util"
)

var (
	// stampBusPartition stamps ev with partition part ("global" |
	// "user:<id>"): `ev.Partition = part` once events.Event has F2's field.
	stampBusPartition func(ev *events.Event, part string)
	// busEventPartition is e's stamp, "" for none: `e.Partition`.
	busEventPartition func(e events.Event) string
	// publishPartitionPushSeam delivers a person's partition's bus event to
	// that partition's own push subscriptions (03 §D, 04 §2); from is the
	// tile whose credential published it (only its own subscriptions may
	// start the partition): partitionbussubs.go fills it. Unfilled, a
	// person's event reaches frames and sockets only: today's push
	// subscriptions are all the global instance's.
	publishPartitionPushSeam = func(b *Broker, ra reach, from, topic string, data any) {}
)

// sharedRes reports whether res is a shared resource of its scope ("shared":
// true or "read").
func sharedRes(res registry.Resource) bool {
	return res.Shared == registry.SharedAll || res.Shared == registry.SharedRead
}

// publishPartitioned publishes on ra's bus when it is a partitioned scope's
// own (not shared), stamped with the partition the publisher reached
// (ra.part): the global instance's event also reaches today's push
// subscriptions, a person's only their partition's (publishPartitionPushSeam),
// p being the publisher. It answers false, having written nothing, for any
// other bus — an unpartitioned scope's or a shared one, published as today
// by the caller.
func (b *Broker) publishPartitioned(w http.ResponseWriter, p auth.Principal, ra reach, topic string, data any) bool {
	if ra.part == "" || sharedRes(ra.res) {
		return false
	}
	if stampBusPartition == nil {
		server.WriteError(w, http.StatusServiceUnavailable, "bus events of partitioned tiles aren't available in this xbind yet", "/docs/partitions.md")
		return true
	}
	id := ra.rt.String()
	ev := events.Event{Type: "bus", Topic: id + "/" + topic, Data: data}
	counter := id
	if ra.dep != util.MainDeployment {
		ev.Deployment, counter = ra.dep, id+"\x00"+ra.dep
	}
	stampBusPartition(&ev, ra.part)
	b.Hub.Publish(ev)
	b.countBusEvent(counter)
	if ra.pkey == "" {
		b.bus.publishStamped(id, ra.dep, topic, data, partGlobalKey, "") // global's: today's subscriptions are all global's
	} else {
		publishPartitionPushSeam(b, ra, b.publisherTile(p), topic, data)
	}
	server.WriteOK(w)
	return true
}

// publisherTile is the tile p's credential belongs to — the tile itself,
// or the tile of an xbin.window sub-path — "" for a person's own
// credential, the root token and a delivery.
func (b *Broker) publisherTile(p auth.Principal) string {
	if p.Component == "" || isDelivery(p) {
		return ""
	}
	if c, _, ok := b.Reg.Resolve(p.Component); ok {
		return c.Path
	}
	return p.Component
}

// busPartitionReaches is busFilter's partition rule for bus rt (res) as p
// reaches it (own: its own scope): true for a scope no tile partitions; a
// partitioned scope's shared bus as the data plane reaches it (F10's rule
// for shared resources, W2): another tile's person's partition while that
// person can read the scope — never their consent — asked at every event,
// as a person's push subscription asks it; on a partitioned scope's own
// bus, only an event stamped with the partition p acts in there — an
// unstamped one reaches no one. A scope whose root's mode can't be read
// reaches no one (fail closed).
func (b *Broker) busPartitionReaches(p auth.Principal, rt resTarget, res registry.Resource, own bool, e events.Event) bool {
	root, isTile := b.Reg.Component(rt.Scope)
	switch {
	case rt.Scope == "" || !isTile:
		return true
	case root.PartitionRecordUnknown():
		return false
	}
	stamp := ""
	if busEventPartition != nil {
		stamp = busEventPartition(e)
	}
	_, partitioned := b.Reg.PartitionedScope(rt.Scope)
	if !partitioned || sharedRes(res) {
		// a person's event never reaches past their partition, even when
		// the scope stopped partitioning between its publish and now
		if strings.HasPrefix(stamp, "user:") {
			return false
		}
		if partitioned { // shared: the reach the data plane would allow (reachPartition)
			_, err := b.reachPartition(p, rt.Scope, own, true)
			return err == nil
		}
		return true
	}
	part, err := b.partitionOf(p, rt.Scope, own)
	return err == nil && stamp != "" && part == stamp
}
