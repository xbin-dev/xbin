package broker

// partitionwire.go — the partition planes wired to each other
// (plans/partitions/records/W1-wire.md). Each plane was built against the
// others through seams with fail-closed defaults; this file fills them, in
// one place:
//
//   - the data plane (F4: partitionns.go, partitionbus.go, diskpart.go) asks
//     the identity plane (F2: partitionroute.go) which partition a
//     principal acts in (addressedPartition — the one decision for calls and
//     data), a person's stored uid, and the one minting path
//     (mintPartitionUID), which adopts the uid the namespaces' ns.json and
//     the partitions' partition.json carry (adoptablePartitionUID);
//   - a partitioned scope's own bus stamps and reads events.Event.Partition;
//   - the runner's side (F3), which boot installs per broker
//     (SetPartitionRunner): which people's instances may bind their volumes
//     (the idle unmount), and the stops a mode switch makes before its wipe;
//   - the per-partition disk ceiling is the partitions API's
//     (POST /partitions/limits, PartitionBytes);
//   - the switch's wipe executor (F13a: partitionwipe.go) deletes people's
//     partition namespaces under the store name "holds data" asks
//     ("partition-namespaces");
//   - a new tile at a removed partitioned tile's path never inherits its
//     mode record or its people's data (pathLeftovers, F13b's open end);
//   - the identity plane asks the consent plane (F10: partitionconsent.go)
//     about a person's consent while partitionConsent is on, and counts
//     allowed cross-tile edges in the egress ledger (partitionledger.go);
//     both planes' records go last in a switch's wipe (metadata hooks),
//     then the partitions' identity records (F5: partitionrecords.go);
//   - a person's terminal opening on a partitioned tile (F7a:
//     partitionterm.go) writes their partition's identity record (F5)
//     (plans/partitions/records/W2-wire.md).

import (
	"cmp"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/util"
)

func init() {
	addressedPartitionSeam = (*Broker).addressedPartitionKey
	partitionUIDSeam = (*Broker).storedPartitionUID
	partitionMintUIDSeam = (*Broker).mintPartitionUID
	partitionAdoptUID = func(b *Broker, userID string, created time.Time) (string, bool) {
		uid := b.adoptablePartitionUID(userID, created.Unix()) // ns.json and partition.json
		return uid, uid != ""
	}
	stampBusPartition = func(ev *events.Event, part string) { ev.Partition = part }
	busEventPartition = func(e events.Event) string { return e.Partition }
	partitionRunningSeam = func(b *Broker, scope, dep, pkey string) bool { return b.partitionRunning(scope, pkey) }
	partitionDiskCeilingSeam = func(b *Broker, tile string) int64 { return b.PartitionBytes(tile) }
	registerWipeHook(wipeHook{name: "partition-namespaces", stop: stopPartitionInstances, wipe: wipePartitionNamespacesHook})
	// cross-tile edges (F10): consents asked at every call and reach while
	// partitionConsent is on, the egress ledger counting them, and both
	// planes' metadata wiped last on a switch
	partitionConsentHolds = (*Broker).consentOrAsk
	partitionEdgeSeam = (*Broker).ledgerEdge
	registerWipeHook(wipeHook{name: "consents", meta: true, wipe: wipeConsentsHook})
	registerWipeHook(wipeHook{name: "ledgers", meta: true, wipe: wipeLedgersHook})
	// F5's identity records last of all (W2): a record names whose a
	// partition's leftovers are (PD-43) — a data store's failure leaves it,
	// and a retry, a re-adoption and a record-driven sweep still find them —
	// and its wipe removes each person's directory whole, the ledger's file
	// in it included, once the ledgers' hook has stopped their writes
	registerWipeHook(wipeHook{name: "partition-records", meta: true, wipe: wipePartitionRecords})
	// F5 ↔ F7a (W2): a person's terminal or agent session opening on a
	// partitioned tile writes their partition's record, so their layer and
	// history, keyed by the partition id alone, have one
	termPartitionRecord = (*Broker).noteTermPartition
}

// noteTermPartition is termPartitionRecord (partitionterm.go): person
// user's partition of tile under incarnation uid gets its partition.json on
// the tile's primary — people's partitions run nowhere else (PD-17) —
// unless one exists; none while the tile is paused (nothing of it runs, and
// a switch's wipe may be deleting the records), as for a start. Best effort:
// a record that can't be written is logged, the session opens.
func (b *Broker) noteTermPartition(tile, user, uid string) {
	dep := cmp.Or(b.primaryOf(tile), util.MainDeployment)
	if user == "" || uid == "" || b.partitionPaused(tile, dep) {
		return
	}
	t := partTarget{tile: tile, dep: dep, pkey: util.PartitionKey(user, uid), part: util.UserPartition(user), user: user, uid: uid}
	if err := b.notePartition(t, false); err != nil {
		slog.Warn("partitions: a terminal's partition record", "tile", tile, "partition", t.part, "err", err)
	}
}

// partitionRunSlot is the runner's side of people's partitions as the
// broker asks it; boot installs it (SetPartitionRunner). Unset: every
// partition counts as running (nothing is unmounted for idleness) and a
// switch stops people's instances only through StopBackend.
type partitionRunSlot struct {
	mu      sync.RWMutex
	running func(scope, pkey string) bool // runner.PartitionRunning
	stop    func(tile string)             // runner.StopPartitions: tokens revoked, processes waited for
	revoke  func(tile string) int         // auth.RevokePartitionInstances
	one     func(tile, dep, part string)  // runner.StopPartition: one person's instance (SetPartitionInstanceStop)
}

// SetPartitionInstanceStop installs the runner's stop of one person's partition
// instance of a tile (runner.StopPartition: its token revoked, its
// processes waited for; the next request starts it again) — the one hook
// both planes that stop a single instance use (W2): a revoked consent stops
// the caller tile's instance of the person (F10, stopEdgeCaller), and a
// personal bind's change restarts the person's instance with its new env
// (F15, restartPartition). Boot installs it (wirePartitionRunner).
func (b *Broker) SetPartitionInstanceStop(stop func(tile, dep, part string)) {
	b.partRun.mu.Lock()
	defer b.partRun.mu.Unlock()
	b.partRun.one = stop
}

// SetPartitionEdgeStop is SetPartitionInstanceStop (F10's name, kept for callers).
func (b *Broker) SetPartitionEdgeStop(stop func(tile, dep, part string)) {
	b.SetPartitionInstanceStop(stop)
}

// SetPartitionRestart is SetPartitionInstanceStop (F15's name, kept for callers).
func (b *Broker) SetPartitionRestart(stop func(tile, dep, part string)) {
	b.SetPartitionInstanceStop(stop)
}

// PartitionInstanceStopWired reports whether boot installed the stop of one
// person's instance: without it a revoked consent stops nothing and a
// personal bind's change waits for the instance's next start (boot's
// guards, TestPartitionConsentWiring and TestPersonalBindRestartWired).
func (b *Broker) PartitionInstanceStopWired() bool {
	b.partRun.mu.RLock()
	defer b.partRun.mu.RUnlock()
	return b.partRun.one != nil
}

// PartitionEdgeStopWired is PartitionInstanceStopWired (F10's name).
func (b *Broker) PartitionEdgeStopWired() bool { return b.PartitionInstanceStopWired() }

// PartitionRestartWired is PartitionInstanceStopWired (F15's name).
func (b *Broker) PartitionRestartWired() bool { return b.PartitionInstanceStopWired() }

// stopPartitionInstance stops partition part of deployment dep of tile, if
// the runner's stop is installed.
func (b *Broker) stopPartitionInstance(tile, dep, part string) {
	b.partRun.mu.RLock()
	stop := b.partRun.one
	b.partRun.mu.RUnlock()
	if stop != nil {
		stop(tile, dep, part)
	}
}

// SetPartitionRunner installs the runner's side of people's partitions:
// running reports whether an instance of person pkey may be binding their
// namespaces of scope (starting, running or still stopping); stop stops
// every person's instance of a tile, waiting for them; revoke drops every
// instance token of a tile's people's partitions.
func (b *Broker) SetPartitionRunner(running func(scope, pkey string) bool, stop func(tile string), revoke func(tile string) int) {
	b.partRun.mu.Lock()
	defer b.partRun.mu.Unlock()
	b.partRun.running, b.partRun.stop, b.partRun.revoke = running, stop, revoke
}

// partitionRunning is F4's partitionRunningSeam: true unless the runner
// says no instance of pkey in scope may be binding its volumes.
func (b *Broker) partitionRunning(scope, pkey string) bool {
	b.partRun.mu.RLock()
	running := b.partRun.running
	b.partRun.mu.RUnlock()
	return running == nil || running(scope, pkey)
}

// stopPartitionInstances is a switch's stop step for people's partitions
// (01 §2.5, 02 §2): every person's instance of the tile — and, when it
// roots its scope, of every other tile of the scope, which reach its
// namespaces — stopped and waited for, and every instance token of their
// partitions revoked, before anything is wiped and before the new mode is
// published. runSwitch's StopBackend stops them too; this holds even
// where a stop hook isn't the runner's.
func stopPartitionInstances(b *Broker, t wipeTarget) {
	b.partRun.mu.RLock()
	stop, revoke := b.partRun.stop, b.partRun.revoke
	b.partRun.mu.RUnlock()
	tiles := []string{t.Tile}
	if t.RootsScope {
		for _, c := range b.Reg.Components() {
			if c.Scope == t.Scope && c.Path != t.Tile {
				tiles = append(tiles, c.Path)
			}
		}
	}
	for _, tile := range tiles {
		if stop != nil {
			stop(tile)
		}
		if revoke != nil {
			revoke(tile)
		}
	}
}

// partitionLeftovers is pathLeftovers' part for partitioned tiles: what a
// removed partitioned tile at path, or under it, left that a new tile there
// would inherit — its mode record (data/partitions/<TileKey>/mode.json:
// the fresh tile's first settle would find the old recorded mode, and an
// opt-out instance could go pending), a record there this xbind can't read
// (it would hold the new tile invalid), and its people's partition
// namespaces (a new owner would inherit them). Only tiles no longer
// registered count. A workspace no tile ever partitioned has none of these.
func (b *Broker) partitionLeftovers(path string, under func(string) bool) []string {
	gone := func(tile string) bool {
		_, registered := b.Reg.Component(tile)
		return under(tile) && !registered
	}
	var out []string
	if pm := b.parts; pm != nil {
		pm.mu.Lock()
		for tile := range pm.recs {
			if gone(tile) {
				out = append(out, "partition mode record of "+tile)
			}
		}
		if why, ok := pm.unread[util.TileKey(path)]; ok && gone(path) {
			out = append(out, "a partition mode record of "+path+" this xbind can't read ("+why+")")
		}
		pm.mu.Unlock()
	}
	people := map[string]int{}
	_ = b.eachPartitionNamespace("", func(id nsID) {
		if gone(id.scope) {
			people[id.scope]++
		}
	})
	for scope, n := range people {
		out = append(out, fmt.Sprintf("%s's people's partition data (%d namespace(s))", scope, n))
	}
	parts := map[string]int{}
	_ = b.eachPartitionRecord("", func(_ partitionDirOf, rec partitionRecord) {
		if gone(rec.Tile) {
			parts[rec.Tile]++
		}
	})
	for tile, n := range parts {
		out = append(out, fmt.Sprintf("%s's people's partition vaults and records (%d partition(s))", tile, n))
	}
	return append(out, b.mailLeftovers(gone)...) // its mail stores (partitionmail_bell.go)
}

// wipePartitionNamespacesHook is the "partition-namespaces" store's wipe:
// every person's partition namespace of the scope the tile roots, on a
// switch that deletes everything (wipeEverything); removing or adding
// "global" keeps people's partitions (owner ruling H1). A dry run counts.
// wipePartitionNamespaces holds each namespace itself (nsRemoving); the
// part: backup subkeys are the executor's to erase (erasedSubjects).
func wipePartitionNamespacesHook(b *Broker, t wipeTarget, sum *wipeSummary) error {
	if !t.RootsScope || t.Kind != wipeEverything {
		return nil
	}
	got, err := b.wipePartitionNamespaces(t.Tile, t.DryRun)
	sum.Namespaces += got.Namespaces
	sum.Partitions += got.Partitions
	sum.Bytes += got.Bytes
	for _, u := range got.People {
		sum.addPerson(u)
	}
	return err
}
