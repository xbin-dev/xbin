package obs

// partitionstatus.go — the status of a person's partition of a partitioned
// tile (plans/partitions/02 §8-§9; S12, C10). POST /tile-report from a user
// partition's credential (auth.Principal.Partition, stamped by the server's
// partition gate) is stored per partition, never becomes the tile card's
// status, and publishes its status event with Partition set, which the
// server delivers only to that person's sockets and the partition's own
// principals. GET /tile-report answers such a caller its partition's record
// for its own tile. The global instance keeps today's behaviour.

import (
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// partKey is a partition's status key: the tile and the partition key.
func partKey(tile string, part util.Partition) string { return tile + "\x00" + string(part) }

// setPartitionStatus stores (or, "ok" without a message, clears) part's
// status of comp and publishes it to that partition only; a transient one
// is published and not stored.
func (o *Plane) setPartitionStatus(comp string, part util.Partition, rec statusRec, transient bool) {
	if !transient {
		o.statusMu.Lock()
		if o.partStatuses == nil {
			o.partStatuses = map[string]statusRec{}
		}
		if rec.Level == "ok" && rec.Message == "" {
			delete(o.partStatuses, partKey(comp, part))
		} else {
			o.partStatuses[partKey(comp, part)] = rec
		}
		o.statusMu.Unlock()
	}
	o.publishPartitionStatus(comp, part, rec, transient)
}

func (o *Plane) publishPartitionStatus(comp string, part util.Partition, rec statusRec, transient bool) {
	data := map[string]any{"level": rec.Level, "message": rec.Message, "ts": rec.TS}
	if transient {
		data["transient"] = true
	}
	o.Hub.Publish(events.Event{Type: "status", Component: comp, Partition: string(part), Data: data})
}

// partitionStatuses is GET /tile-report's answer for part's principal of
// comp: out with comp's entry replaced by the partition's own record (or
// dropped when it has none) — a partition sees its own state, never
// another's.
func (o *Plane) partitionStatuses(out map[string]statusRec, comp string, part util.Partition) {
	o.statusMu.Lock()
	rec, ok := o.partStatuses[partKey(comp, part)]
	o.statusMu.Unlock()
	delete(out, comp)
	if ok {
		out[comp] = rec
	}
}

// clearPartitionStatuses drops every partition's status of tile when its
// code restarts them all (build-start), telling each partition.
func (o *Plane) clearPartitionStatuses(tile string) {
	prefix := tile + "\x00"
	var parts []util.Partition
	o.statusMu.Lock()
	for key := range o.partStatuses {
		if part, ok := strings.CutPrefix(key, prefix); ok {
			parts = append(parts, util.Partition(part))
			delete(o.partStatuses, key)
		}
	}
	o.statusMu.Unlock()
	for _, part := range parts {
		o.publishPartitionStatus(tile, part, statusRec{Level: "ok", TS: time.Now().Unix()}, false)
	}
}
