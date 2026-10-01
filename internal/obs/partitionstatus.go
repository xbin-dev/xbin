package obs

// partitionstatus.go — the status of a person's partition of a partitioned
// tile (plans/partitions/02 §8-§9; S12, C10). POST /tile-report from a user
// partition's credential (auth.Principal.Partition, stamped by the server's
// partition gate) is stored per partition, never becomes the tile card's
// status, and publishes its status event with Partition set, which the
// server delivers only to that person's sockets and the partition's own
// principals. GET /tile-report answers such a caller its partition's record
// for its own tile. The global instance keeps today's behaviour.
//
// A record is keyed by the partition's id (the pkey, PartitionID), not its
// wire key: a deleted and recreated person of the same id is another
// partition and never reads the first one's record (PD-43). A partition's
// own restart (its runner event, stamped with the partition) clears its
// record alone; a restart of the tile's code (its build-start) clears every
// partition's.

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

// partStatus is a partition's stored status, with the partition it tells.
type partStatus struct {
	part util.Partition
	rec  statusRec
}

// partKey is a partition's status key: the tile and the partition's id
// (without the PartitionID hook, the wire key).
func (o *Plane) partKey(tile string, part util.Partition) (string, error) {
	id := string(part)
	if o.PartitionID != nil {
		var err error
		if id, err = o.PartitionID(part); err != nil {
			return "", err
		}
	}
	return tile + "\x00" + id, nil
}

// setPartitionStatus stores (or, "ok" without a message, clears) part's
// status of comp and publishes it to that partition only; a transient one
// is published and not stored.
func (o *Plane) setPartitionStatus(comp string, part util.Partition, rec statusRec, transient bool) error {
	if !transient {
		key, err := o.partKey(comp, part)
		if err != nil {
			return err
		}
		o.statusMu.Lock()
		if o.partStatuses == nil {
			o.partStatuses = map[string]partStatus{}
		}
		if rec.Level == "ok" && rec.Message == "" {
			delete(o.partStatuses, key)
		} else {
			o.partStatuses[key] = partStatus{part: part, rec: rec}
		}
		o.statusMu.Unlock()
	}
	o.publishPartitionStatus(comp, part, rec, transient)
	return nil
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
	delete(out, comp)
	key, err := o.partKey(comp, part)
	if err != nil {
		return
	}
	o.statusMu.Lock()
	ps, ok := o.partStatuses[key]
	o.statusMu.Unlock()
	if ok {
		out[comp] = ps.rec
	}
}

// clearPartitionStatuses drops every partition's status of tile when its
// code restarts them all (build-start), telling each partition.
func (o *Plane) clearPartitionStatuses(tile string) {
	prefix := tile + "\x00"
	var parts []util.Partition
	o.statusMu.Lock()
	for key, ps := range o.partStatuses {
		if strings.HasPrefix(key, prefix) {
			parts = append(parts, ps.part)
			delete(o.partStatuses, key)
		}
	}
	o.statusMu.Unlock()
	for _, part := range parts {
		o.publishPartitionStatus(tile, part, statusRec{Level: "ok", TS: time.Now().Unix()}, false)
	}
}

// clearPartitionStatus drops part's status of tile when that partition's
// instance restarts, telling it; nothing else's clears.
func (o *Plane) clearPartitionStatus(tile string, part util.Partition) {
	key, err := o.partKey(tile, part)
	if err != nil {
		return
	}
	o.statusMu.Lock()
	_, had := o.partStatuses[key]
	delete(o.partStatuses, key)
	o.statusMu.Unlock()
	if had {
		o.publishPartitionStatus(tile, part, statusRec{Level: "ok", TS: time.Now().Unix()}, false)
	}
}

// partitionRestarted reports whether e, an event stamped with a user
// partition, says that partition's instance (re)starts: a build-start of
// its own, or its runner's `partitions` state event for a build-start or a
// reload.
func partitionRestarted(e events.Event) bool {
	if e.Type == "build-start" {
		return true
	}
	if e.Type != "partitions" {
		return false
	}
	var f struct{ Op, Event string }
	if b, err := json.Marshal(e.Data); err == nil {
		_ = json.Unmarshal(b, &f)
	}
	return f.Op == "state" && (f.Event == "build-start" || f.Event == "reload")
}
