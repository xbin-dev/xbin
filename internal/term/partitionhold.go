package term

// partitionhold.go — the gap between a stop and a wipe (plans/partitions/06
// §2, §6, §9). A partition's end (a reset, an orphan's purge, a deleted
// person's sweep) stops the partition's sessions, then deletes its person
// layer and agent history; a mode switch does the same for the whole tile.
// Neither may let a session open in between, which would mount the layer
// while it is removed, or write a fresh layer or history next to the wiped
// partition. So:
//
//   - HoldPartition refuses the partition's new sessions (409,
//     ErrPartitionEnding) until released — the switch's twin is the broker's
//     switch hold, refused in SessionPartition;
//   - every open that passed pickPartition counts as in flight until it
//     registers its session or fails (partOpened), and a stop waits for the
//     ones it matches (stopWhere), so none slips past it unseen.

import "sync"

// partHoldKey names a held partition: its id on one tile (a tile path), or
// on every tile ("").
type partHoldKey struct{ tile, pkey string }

// partState is the manager's holds and in-flight opens, under Manager.mu.
type partState struct {
	holds    map[partHoldKey]int
	inflight map[*openOpts]sessionPart
}

// HoldPartition refuses new sessions of partition id pkey — on tile, or on
// every tile when tile is "" — until release: the hold a partition's end
// takes before StopPartitionSessions and keeps through WipePartitionKey.
func (m *Manager) HoldPartition(tile, pkey string) (release func()) {
	k := partHoldKey{tile, pkey}
	m.mu.Lock()
	if m.parts.holds == nil {
		m.parts.holds = map[partHoldKey]int{}
	}
	m.parts.holds[k]++
	m.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.parts.holds[k]--; m.parts.holds[k] <= 0 {
				delete(m.parts.holds, k)
			}
			m.mu.Unlock()
		})
	}
}

// opening counts o's open as in flight, unless its partition is held
// (false: the open is refused). Atomic with the hold, so an open either
// counts before a hold — and the stop that follows waits for it — or is
// refused.
func (m *Manager) opening(o *openOpts) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sp := o.part; sp.key != "" && (m.parts.holds[partHoldKey{"", sp.key}] > 0 || m.parts.holds[partHoldKey{sp.tile, sp.key}] > 0) {
		return false
	}
	if m.parts.inflight == nil {
		m.parts.inflight = map[*openOpts]sessionPart{}
	}
	m.parts.inflight[o] = o.part
	return true
}

// partOpened ends o's open being in flight: its session registered (a stop
// sees it now), or the open failed. Every caller of a successful
// pickPartition calls it once the create returned; again is a no-op.
func (m *Manager) partOpened(o *openOpts) {
	m.mu.Lock()
	delete(m.parts.inflight, o)
	m.mu.Unlock()
}

// inFlight counts the opens in flight whose partition matches.
func (m *Manager) inFlight(match func(sessionPart) bool) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, sp := range m.parts.inflight {
		if match(sp) {
			n++
		}
	}
	return n
}
