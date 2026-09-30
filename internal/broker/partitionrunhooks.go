package broker

// partitionrunhooks.go — the identity plane's answers to the runner's
// people's-partitions hooks (plans/partitions/03 §A, 02 §1-§2, §9). Each
// has the signature of its runner.PartitionHooks field, so boot installs
// them as method values once the runner has the hooks
// (plans/partitions/records/F2.md, "Seams"):
//
//   - PartitionIdent → PartitionHooks.PartitionIdent;
//   - ShouldRunPartition → the identity half of
//     PartitionHooks.ShouldRunPartition (the data plane's
//     PartitionEncryptionHoldReason is the other; boot joins them);
//   - PublishPartitionState → PartitionHooks.PartitionEvent.

import (
	"fmt"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// PartitionIdent is the partition id (pkey) of partition part
// ("user:<id>") of tile and its person's uid, minting the uid at their
// first partition (PD-43). Anything but a live person's user partition is
// an error: the runner starts nothing.
func (b *Broker) PartitionIdent(tile, part string) (pkey, uid string, err error) {
	pt, err := util.ParsePartition(part)
	id, ok := pt.User()
	if err != nil || !ok {
		return "", "", fmt.Errorf("%s: %q is no person's partition", tile, part)
	}
	if err := b.personLive(id, tile); err != nil {
		return "", "", err
	}
	if uid, err = b.mintPartitionUID(id); err != nil {
		return "", "", err
	}
	return util.PartitionKey(id, uid), uid, nil
}

// ShouldRunPartition reports whether partition part of deployment dep of
// tile may run for the person's incarnation uid (the runner state's), as
// far as identity goes (PD-17, PD-20): tile runs user partitions (its
// recorded mode, not paused), dep is its primary, and part's person exists
// with that same uid, is enabled and can read tile.
func (b *Broker) ShouldRunPartition(tile, dep, part, uid string) bool {
	pt, err := util.ParsePartition(part)
	id, ok := pt.User()
	if err != nil || !ok || uid == "" || b.storedPartitionUID(id) != uid {
		return false
	}
	c, found := b.Reg.Component(tile)
	if !found {
		return false
	}
	if spec, on := c.Partitioned(); !on || !spec.User {
		return false
	}
	return b.isPrimary(tile, dep) && b.personLive(id, tile) == nil && !b.partitionDropping(tile, util.PartitionKey(id, uid)) // a reset (partitionops.go)
}

// PublishPartitionState publishes a runner event of user partition part of
// tile — typ build-start, build-ok, build-error or reload, text its detail
// — as the `partitions` event, op `state`, stamped with the partition, so
// /ws/events delivers it to that person's sockets and the partition's own
// principals only (02 §9); never tile-wide. The event type's full shape is
// the operations plane's (06 §6).
func (b *Broker) PublishPartitionState(tile, dep, part, typ, text string) {
	pt, err := util.ParsePartition(part)
	if err != nil || !pt.IsUser() || b.Hub == nil {
		return
	}
	data := map[string]any{"op": "state", "partition": part, "event": typ}
	if text != "" {
		data["text"] = text
	}
	b.Hub.Publish(events.Event{Type: "partitions", Component: tile, Partition: part, Data: data})
}
