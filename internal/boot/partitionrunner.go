package boot

// partitionrunner.go — the runner's side of partitioned tiles
// (plans/partitions/03 §A): what boot wires between the registry, the
// broker and the runner for people's partitions.
//
// The runner's PartitionHooks that give a person's partition its identity,
// liveness, env and data, token binding and event routing —
// PartitionIdent, ShouldRunPartition, PartitionEnv,
// RegisterPartitionInstance and PartitionEvent — belong to the identity and
// data planes and are wired where those are built. Until they are, every
// hook is nil and no person's partition starts (runner.ErrPartitionRefused):
// a partitioned tile then runs its global instance alone, if it declares
// one.

import "github.com/xbin-dev/xbin/internal/registry"

// wirePartitionRunner installs the running caps (an admin's or a tile
// manager's limits over the runner's memory-derived defaults) and the mode
// transitions: every rescan that changes a tile's partition state tells the
// runner before it is published, so instances the new state doesn't cover
// stop, their tokens revoked, first (01 §6, 02 §2).
func (st *State) wirePartitionRunner() {
	run, brk := st.Run, st.Broker
	run.PartitionCapsFor = brk.PartitionCaps
	brk.SetPartitionCapDefaults(run.DefaultPartitionCaps)
	st.Reg.OnPartitionChange(func(c *registry.Component, old, new registry.PartitionMode) {
		run.PartitionsChanged(c, runningSpec(old), runningSpec(new))
	})
}

// runningSpec is the partition spec a tile in mode m runs: its recorded mode
// while it is partitioned; none while unpartitioned, pending or invalid
// (nothing of the primary runs then, PD-50).
func runningSpec(m registry.PartitionMode) registry.PartitionSpec {
	if m.State != registry.PartitionPartitioned {
		return registry.PartitionSpec{}
	}
	return m.Recorded
}
