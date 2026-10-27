package boot

import (
	"log/slog"
	goruntime "runtime"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/cgroup"
)

// The resource-limit steps: per-tile cgroup ceilings and the alerts raised
// when a tile hits them (docs/isolation.md §Resource limits).

// Per-component cgroup v2 limits + accounting (memory/CPU/pids), best-effort
// — active only when xbind's cgroup is delegated (systemd Delegate=yes / a
// container). A runaway tile then OOMs / throttles / can't fork *alone*.
func (st *State) stepCgroup() error {
	if cg := cgroup.New(); cg.Enabled() {
		memMax := parseBytes("XBIN_LIMIT_MEM", st.Cfg.LimitMem, 2<<30) // 2 GiB
		tile := cgroup.Limits{
			MemMax:    memMax,
			PidsMax:   int64(max(512, goruntime.NumCPU()*8)), // fork-bomb ceiling
			CPUWeight: 100,                                   // fair share; burst when idle
		}
		cg.SetLimits(tile)
		st.Deployments.TileLimits = tile // every deployment's ceiling (D127n)
		st.Run.Cgroup = cg
		st.Term.Cgroup = cg // restricted (non-admin) terminals get the same caps (D17d)
		slog.Info("cgroup v2 limits enabled", "memMax", memMax, "pidsMax", max(512, goruntime.NumCPU()*8))
	}
	return nil
}

// Fold cgroup at-limit events into the workspace alerts: a tile that keeps
// hitting its memory or pids cap surfaces in the admin console / shell.
// Delta-tracked so a one-off blip clears once the tile settles. It runs after
// the cgroup step (before, run.Cgroup was always nil and nothing was ever
// installed). Sessions with a leaf of their own — a VM's, a restricted
// user's — are watched too. Every leaf of a tile counts (runner.AtLimitTile):
// the flat one, or one per deployment; a non-primary deployment's hit names
// it and reaches admins only (D127h), as does a person's partition's, which
// names neither the person nor the partition (plans/partitions/03 §A.9).
func (st *State) stepLimitAlerts() error {
	run, reg, brk, dp := st.Run, st.Reg, st.Broker, st.Deployments
	if run.Cgroup != nil && run.Cgroup.Enabled() {
		lastMem, lastPids := map[string]int64{}, map[string]int64{}
		sessMem, sessPids := map[string]int64{}, map[string]int64{}
		brk.SetLimitAlerts(func() []broker.Alert {
			out := sessionLimitAlerts(run.LeafCgroup, st.Sbx, sessMem, sessPids)
			for _, c := range reg.Components() {
				for _, h := range run.AtLimitTile(c.Path) {
					who, tile := c.Path, c.Path
					switch {
					case h.Partition:
						who, tile = "a person's partition of "+c.Path, ""
					case h.Deployment != "" && h.Deployment != dp.Primary(c.Path):
						who, tile = c.Path+"'s deployment "+h.Deployment, ""
					}
					if h.Mem > lastMem[h.Leaf] {
						out = append(out, broker.Alert{Level: "warn", Kind: "oom", Tile: tile,
							Message: who + " hit its memory limit (was OOM-killed) — it may be leaking or under-provisioned"})
					}
					if h.Pids > lastPids[h.Leaf] {
						out = append(out, broker.Alert{Level: "warn", Kind: "pids", Tile: tile,
							Message: who + " hit its process (pids) limit — a runaway fork/spawn?"})
					}
					lastMem[h.Leaf], lastPids[h.Leaf] = h.Mem, h.Pids
				}
			}
			return out
		})
	}
	return nil
}
