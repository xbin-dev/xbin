package runner

// limits.go — a tile's cgroup leaves: which leaf a backend generation
// starts in, with which caps, and which leaves hit their caps, for the
// workspace's limit alerts. Today a tile has one flat leaf, comp-<CompKey>,
// which every generation shares; a tile that runs a deployment beyond main
// moves to a per-tile parent with a leaf per deployment (07-runtime §10.3),
// so a generation keeps the leaf it started in (instance.leaf), the alerts
// ask for the tile's hits rather than for one leaf, and each hit names the
// deployment whose leaf it is (D127q).

import (
	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/util"
)

// cgroupOps is what the runner asks of the cgroup manager: *cgroup.Manager,
// or a test's model of the tree (Runner.cgOps).
type cgroupOps interface {
	Enabled() bool
	Add(name string, pid int)
	AddLimited(name string, pid int, l cgroup.Limits)
	AddMem(name string, pid int, memMax int64)
	AddMemLimited(name string, pid int, l cgroup.Limits, memMax int64)
	Remove(name string)
	Usage(name string) (cgroup.Usage, bool)
	AtLimit(name string) (mem, pids int64, ok bool)
	Procs(name string) ([]int, bool)
	TileProcs(compKey string) ([]int, bool)
	TileUsage(compKey string) (cgroup.Usage, bool)
	TileLeaves(compKey string) []cgroup.Leaf
}

// cgroups is the runner's cgroup manager; nil without one.
func (r *Runner) cgroups() cgroupOps {
	if r.cgOps != nil {
		return r.cgOps
	}
	if r.Cgroup == nil {
		return nil // never a typed nil in the interface
	}
	return r.Cgroup
}

// chooseLeaf is the leaf a generation of deployment dep of tile starts in,
// chosen once, at its start (07-runtime §10.3). main keeps the flat leaf,
// util.CompKey(tile), while it runs alone: a zero-state or main-only tile
// never gets the per-tile parent, and a record alone changes nothing (D119c).
// Any other deployment starts in its own leaf under the parent, which its
// start creates; main moves in at its first generation after that, while
// its old one drains in the flat leaf. No live process ever moves, and a
// tile whose parent emptied starts main flat again.
func (r *Runner) chooseLeaf(tile, dep string) string {
	key, nested := util.CompKey(tile), false
	if cg := r.cgroups(); cg != nil && dep == util.MainDeployment {
		_, nested = cg.Usage(cgroup.TileNode(key))
	}
	return leafFor(key, dep, nested) // sbx.go: the one leaf rule
}

// joinLeaf puts pid, of generation sock of deployment dep of tile, into leaf
// with that deployment's caps: LimitsFor's, which default to the tile's and
// never exceed its ceilings (D127n), with the primary's node weighted first
// (D127q). A VM generation's leaf holds two guests (vmLeafBytes). Without
// LimitsFor a leaf takes the caps installed on the manager, today's calls.
func (r *Runner) joinLeaf(tile, dep, leaf, sock string, pid int) {
	cg := r.cgroups()
	if cg == nil {
		return
	}
	b, isVM := r.vmLeafBytes(sock)
	switch {
	case r.LimitsFor == nil && isVM:
		cg.AddMem(leaf, pid, b)
	case r.LimitsFor == nil:
		cg.Add(leaf, pid)
	case isVM:
		cg.AddMemLimited(leaf, pid, r.leafLimits(tile, dep), b)
	default:
		cg.AddLimited(leaf, pid, r.leafLimits(tile, dep))
	}
}

// leafLimits are deployment dep of tile's caps, and its node's CPU weight:
// the primary's above the others' (D127q). A flat leaf has no such node.
func (r *Runner) leafLimits(tile, dep string) cgroup.Limits {
	l := r.LimitsFor(tile, dep)
	l.NodeWeight = cgroup.DeploymentWeight(dep == r.primary(tile))
	return l
}

// leaveLeaf removes an exited generation's leaf: a flat one as always, a
// nested one only while no other generation of its deployment is in it, and
// then its empty parents; never another deployment's (cgroup.Remove).
func (r *Runner) leaveLeaf(leaf string) {
	if cg := r.cgroups(); cg != nil && leaf != "" {
		cg.Remove(leaf)
	}
}

// LimitHit is one leaf of a tile that hit its memory or pids cap: the
// cumulative counters cgroup keeps (memory.events "max", pids.events
// "max"), which the caller compares with the ones it saw last.
type LimitHit struct {
	// Deployment names the deployment whose leaf this is; "" for the flat
	// leaf, which zero-state and main-only tiles keep (D119c).
	Deployment string
	// Leaf is the cgroup leaf's name, the key its counters are tracked by:
	// util.CompKey(tile) for the flat leaf.
	Leaf      string
	Mem, Pids int64
}

// AtLimitTile reports tile's leaves whose limit-hit counters cgroup can
// read, the flat leaf first; nil without cgroup accounting. A tile with
// only the flat leaf answers it alone, exactly as
// Cgroup.AtLimit(util.CompKey(tile)) does. A tile with the per-tile parent
// answers each leaf beneath it too (and the flat one while main's old
// generation drains there), each naming its deployment (D127q).
func (r *Runner) AtLimitTile(tile string) []LimitHit {
	cg := r.cgroups()
	if cg == nil {
		return nil
	}
	var hits []LimitHit
	for _, l := range cg.TileLeaves(util.CompKey(tile)) {
		if mem, pids, ok := cg.AtLimit(l.Name); ok {
			hits = append(hits, LimitHit{Deployment: l.Deployment, Leaf: l.Name, Mem: mem, Pids: pids})
		}
	}
	return hits
}
