package boot

import (
	"fmt"
	"log/slog"
	goruntime "runtime"
	"time"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/runner"
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
// An OOM kill in a live leaf is a hit at the tile's memory cap, sticky in
// the tile-limits store (D203: its alert offers raising it) — the poll and
// a generation's exit share one count, so a kill is one hit; memory
// pressure without a kill keeps today's passing warning, kind memory.
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
					switch {
					case h.OOMKills > 0:
						brk.RecordLimitHit(c.Path, limits.MemMiB, st.oomHit(c.Path, h.Deployment, h.Partition, h.OOMKills, h.LimitMiB, "while the backend kept running"))
					case h.Mem > lastMem[h.Leaf]:
						out = append(out, broker.Alert{Level: "warn", Kind: "memory", Tile: tile,
							Message: who + " is at its memory limit — it may be leaking or under-provisioned"})
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

// stepTileLimits wires the tile-limits plane (D203, plans/tile-limits.md
// §5, §6): the runtime's answers to the store (limitsources.go), and the
// store's decisions to the runtime — a tile's raised backend memory is its
// deployments' ceiling (and each person's partition's), a VM grant its
// guest's, an OOM kill at an exit a hit. The tile sandboxes' grants and
// hits are in their Deps (stepTileSandboxes: sandboxGrants). After the
// cgroup, VM and tile-sandbox steps, whose runtimes it reads; before the
// server serves the store's routes.
func (st *State) stepTileLimits() error {
	brk, dp, run := st.Broker, st.Deployments, st.Run
	brk.SetLimitSources(st.limitSources())
	brk.RefreshLimitAsks() // the manifests' asks, and the max clamp LimitGrant uses
	dp.TileCeiling = func(tile string) cgroup.Limits {
		l := dp.TileLimits
		if g := brk.LimitGrant(tile, limits.MemMiB) << 20; l.MemMax > 0 && g > l.MemMax {
			l.MemMax = g
		}
		return l
	}
	run.LimitHooks = runner.LimitHooks{
		OnOOM: func(e runner.OOMEvent) {
			brk.RecordLimitHit(e.Tile, limits.MemMiB, st.oomHit(e.Tile, e.Deployment, e.Partition, e.Kills, e.LimitMiB, ""))
		},
		VMGrant: func(tile string) (int, int) {
			return int(brk.LimitGrant(tile, limits.VMMemMiB)), int(brk.LimitGrant(tile, limits.VMVCPUs))
		},
	}
	return nil
}

// oomHit is a hit at tile's memory cap: the OOM killer took kills
// processes in deployment dep's leaf, or a person's partition's, capped at
// limitMiB (0: the tile's cap now). A primary's hit is the tile's; any
// other deployment's names it (admins only, D127h); a partition's says
// whose by Partition alone (03 §A.9).
func (st *State) oomHit(tile, dep string, partition bool, kills, limitMiB int64, how string) limits.Hit {
	if limitMiB <= 0 { // the leaf's memory.max unread: the tile's cap now
		limitMiB = max(st.Deployments.TileLimits.MemMax>>20, st.Broker.LimitGrant(tile, limits.MemMiB))
	}
	if dep == st.Deployments.Primary(tile) {
		dep = ""
	}
	detail := fmt.Sprintf("OOM-killed (%d %s)", kills, map[bool]string{true: "process", false: "processes"}[kills == 1])
	if how != "" {
		detail += " " + how
	}
	now := time.Now()
	return limits.Hit{At: now, Since: now, Count: 1, Value: limitMiB, Detail: detail, Deployment: dep, Partition: partition}
}

// sandboxGrants are the tile-sandbox runtime's half of the tile-limits
// plane (D203), its Deps' Granted and AtCap: a tile's grants raise its
// totals (tilesbx limitsFor, under its lock: the store never calls out
// under its own), and a total met is a hit.
func sandboxGrants(brk *broker.Broker) (granted func(string) (int, int, int), atCap func(string, limits.Kind, int64, string)) {
	granted = func(tile string) (int, int, int) {
		return int(brk.LimitGrant(tile, limits.SbxMemMiB)), int(brk.LimitGrant(tile, limits.SbxVCPUs)), int(brk.LimitGrant(tile, limits.SbxDiskGiB))
	}
	atCap = func(tile string, k limits.Kind, v int64, detail string) {
		now := time.Now()
		brk.RecordLimitHit(tile, k, limits.Hit{At: now, Since: now, Count: 1, Value: v, Detail: detail})
	}
	return granted, atCap
}
