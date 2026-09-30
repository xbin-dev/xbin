package boot

// partitionrunner.go — the runner's side of partitioned tiles
// (plans/partitions/03 §A): what boot wires between the registry, the
// broker and the runner for people's partitions.
//
// The runner's PartitionHooks give a person's partition its identity and
// liveness (the identity plane's PartitionIdent and ShouldRunPartition,
// with the data plane's encryption hold), its env and data (the data
// plane's PartitionEnv), its token's binding (registerPartitionInstance)
// and its events' routing (PublishPartitionState). While any start hook is
// nil no person's partition starts (runner.ErrPartitionRefused).

import "github.com/xbin-dev/xbin/internal/registry"

// wirePartitionRunner installs the people's-partitions hooks, the running
// caps (an admin's or a tile manager's limits over the runner's
// memory-derived defaults), the broker's view of the runner (which
// people's instances may bind their volumes; a switch's stops) and the mode
// transitions: every rescan that changes a tile's partition state tells the
// runner before it is published, so instances the new state doesn't cover
// stop, their tokens revoked, first (01 §6, 02 §2); then the tile's
// people's-partition tokens are revoked wholesale, whoever registered them.
func (st *State) wirePartitionRunner() {
	run, brk := st.Run, st.Broker
	run.PartitionIdent = brk.PartitionIdent
	run.ShouldRunPartition = func(tile, dep, part, uid string) bool {
		return brk.ShouldRunPartition(tile, dep, part, uid) && brk.PartitionEncryptionHoldReason(tile, dep, part) == ""
	}
	run.PartitionEnv = brk.PartitionEnv // XBIN_PARTITION is the runner's own
	run.RegisterPartitionInstance = st.registerPartitionInstance
	run.PartitionEvent = brk.PublishPartitionState
	run.PartitionCapsFor = brk.PartitionCaps
	brk.SetPartitionCapDefaults(run.DefaultPartitionCaps)
	brk.SetPartitionRunner(run.PartitionRunning, run.StopPartitions, st.Auth.RevokePartitionInstances)
	st.Reg.OnPartitionChange(func(c *registry.Component, old, new registry.PartitionMode) {
		run.PartitionsChanged(c, runningSpec(old), runningSpec(new))
	})
	st.Reg.OnPartitionChange(func(c *registry.Component, _, new registry.PartitionMode) {
		if !runningSpec(new).User {
			st.Auth.RevokePartitionInstances(c.Path) // 02 §2: none authenticates past the change
		}
	})

	// a person's personal binds changed: their instance restarts (05 §3;
	// TestPersonalBindRestartWired)
	brk.SetPartitionRestart(run.StopPartition)
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
