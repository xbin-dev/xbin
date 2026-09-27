package runner

// limits.go — a tile's cgroup leaves at their caps, for the workspace's
// limit alerts. Today a tile has one flat leaf, comp-<CompKey>, which every
// generation shares; a tile that runs a deployment beyond main moves to a
// per-tile parent with a leaf per deployment (07-runtime §10.3), so the
// alerts ask for the tile's hits rather than for one leaf, and each hit
// names the deployment whose leaf it is (P25).

import "github.com/xbin-dev/xbin/internal/util"

// LimitHit is one leaf of a tile that hit its memory or pids cap: the
// cumulative counters cgroup keeps (memory.events "max", pids.events
// "max"), which the caller compares with the ones it saw last.
type LimitHit struct {
	// Deployment names the deployment whose leaf this is; "" for the flat
	// leaf, which zero-state and main-only tiles keep (P5).
	Deployment string
	// Leaf is the cgroup leaf's name, the key its counters are tracked by:
	// util.CompKey(tile) for the flat leaf.
	Leaf      string
	Mem, Pids int64
}

// AtLimitTile reports tile's leaves whose limit-hit counters cgroup can
// read; nil without cgroup accounting. A tile with the flat leaf answers it
// alone, exactly as Cgroup.AtLimit(util.CompKey(tile)) does.
func (r *Runner) AtLimitTile(tile string) []LimitHit {
	if r.Cgroup == nil {
		return nil
	}
	leaf := util.CompKey(tile)
	mem, pids, ok := r.Cgroup.AtLimit(leaf)
	if !ok {
		return nil
	}
	return []LimitHit{{Leaf: leaf, Mem: mem, Pids: pids}}
}
