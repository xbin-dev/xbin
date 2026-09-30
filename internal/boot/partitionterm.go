package boot

// partitionterm.go — terminals and agent sessions on partitioned tiles
// (plans/partitions/06 §1-§3): what boot wires between the broker and the
// terminal manager. The manager asks the broker a new session's partition
// (its env, working directory, layer and history), whether a session's tile
// is partitioned now (the admin gates, PD-09) and a person's partition id
// (the history routes); a mode switch stops the tile's sessions and wipes
// their people's layers and history through the manager; and a tile whose
// recorded mode gains or loses user partitions without a switch (an empty
// tile's manifest decides at once) ends the sessions opened under the other
// mode.

import (
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/registry"
)

// wirePartitionTerm installs the hooks. It runs after wirePartitionRunner,
// so the runner's partition-change hook stays the first (registry's order).
func (st *State) wirePartitionTerm(brk *broker.Broker) {
	tm := st.Term
	tm.SessionPartition = brk.TermPartition
	tm.TilePartitioned = brk.TermTilePartitioned
	tm.PersonPartitionKey = brk.PersonPartitionKey
	brk.SetPartitionTerminals(tm)
	st.Reg.OnPartitionChange(func(c *registry.Component, old, new registry.PartitionMode) {
		was, now := old.Recorded.User || old.Unknown, new.Recorded.User || new.Unknown
		if was != now {
			tm.PartitionModeChanged(c.Path, now)
		}
	})
}
