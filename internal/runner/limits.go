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
	"log/slog"
	"strings"

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
	OOMKills(name string) int64                // read before Remove: the counter goes with the leaf (oom.go)
	Resize(name string, l cgroup.Limits) error // a live leaf's memory caps (ApplyLimits)
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
	_, existed := cg.Usage(leaf)
	defer func() { r.oom.joined(leaf, cg.OOMKills(leaf), !existed) }() // oom.go: its kills count from here
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
	Leaf string
	// Partition: the leaf is a person's partition's (tile-<key>/p-<hash>/,
	// partitionLeaf) — whose it is stays out of alerts (admins see "a
	// person's partition"; plans/partitions/03 §A.9).
	Partition bool
	Mem, Pids int64
	// OOMKills is how many processes the OOM killer took in the leaf
	// since the runner last looked — at an exit or at this poll, one count
	// (oom.go), so a kill is told once; LimitMiB is the leaf's memory.max
	// (0: none, or unread). AtLimitTile reads it, and takes it.
	OOMKills, LimitMiB int64
}

// AtLimitTile reports tile's leaves whose limit-hit counters cgroup can
// read, the flat leaf first, with their OOM kills since the runner last
// looked (taken: the next call, or the leaf's exit, counts from here); nil
// without cgroup accounting. A tile with
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
	node := cgroup.TileNode(util.CompKey(tile))
	for _, l := range cg.TileLeaves(util.CompKey(tile)) {
		if mem, pids, ok := cg.AtLimit(l.Name); ok {
			rest, nested := strings.CutPrefix(l.Name, node)
			kills, limitMiB := r.oomOf(cg, tile, l.Name)
			hits = append(hits, LimitHit{Deployment: l.Deployment, Leaf: l.Name, Partition: nested && strings.HasPrefix(rest, "p-"), Mem: mem, Pids: pids,
				OOMKills: kills, LimitMiB: limitMiB})
		}
	}
	return hits
}

// ApplyLimits makes a decision on tile's limits take effect (D203,
// plans/tile-limits.md §5), once LimitsFor answers it: every running
// generation's leaf that isn't a VM's — every deployment's, every person's
// partition's — has its memory caps raised live (Resize). A lower cap than
// a live leaf has waits for its next start: a decision never OOM-kills. A
// state the crash watch's breaker stopped, or restart (a VM's size, which
// applies at a start; "restart now"), restarts the tile's generations as a
// change does (ChangedTile), and each new one joins with the new caps. A
// generation still starting joins with the caps of the moment it joined.
func (r *Runner) ApplyLimits(tile string, restart bool) {
	cg := r.cgroups()
	looped := false
	for _, s := range append(r.allStates(tile), r.partStates(tile)...) {
		s.mu.Lock()
		dep, insts := s.dep, []*instance{s.cur}
		if s.pt != nil {
			insts = append(insts, s.pt.starting)
		}
		looped = looped || s.cur == nil && crashLooped(s.lastErr)
		s.mu.Unlock()
		for _, inst := range insts {
			if inst != nil && cg != nil && r.LimitsFor != nil {
				r.raiseLeaf(cg, tile, dep, inst)
			}
		}
	}
	if !looped && !restart || r.Reg == nil {
		return
	}
	if c, ok := r.Reg.Component(tile); ok {
		r.ChangedTile(c)
	}
}

// raiseLeaf raises generation inst's leaf to deployment dep of tile's caps
// now, when they are above what it has; never a VM's leaf (sized to its
// guests, vmLeafBytes) and never down.
func (r *Runner) raiseLeaf(cg cgroupOps, tile, dep string, inst *instance) {
	if _, isVM := r.vmLeafBytes(inst.sock); isVM || inst.leaf == "" {
		return
	}
	u, ok := cg.Usage(inst.leaf)
	if !ok {
		return
	}
	l := r.leafLimits(tile, dep)
	switch want, has := l.MemMax, u.MemMax; {
	case has < 0, want > 0 && want <= has: // unlimited now, or already at least that
		return
	}
	if err := cg.Resize(inst.leaf, l); err != nil {
		slog.Warn("tile limits: raising a running backend's memory", "component", tile, "deployment", dep, "leaf", inst.leaf, "err", err)
	}
}

// HostMemory is the memory this host gives xbind, bytes: MemTotal, or less
// where its own cgroup or one above it caps it (a container's limit) — the
// most memory a tile's backend may be granted (D203).
func HostMemory() int64 { return hostMemoryOnce() }

// TileMem is tile's backend memory as the stats sampler last read it
// (bytes; 0 when it hasn't): a tile's usage beside its memory limit
// (D203). Asking keeps the sampler running.
func (r *Runner) TileMem(tile string) int64 {
	r.statsWanted()
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	if ser := r.stats.series[tile]; len(ser) > 0 {
		return ser[len(ser)-1].Mem
	}
	return 0
}

// AppliedMem is the memory cap tile's primary runs with now, MiB, when it
// differs from what its next start gets (LimitsFor): a lower cap waiting
// for that start, since ApplyLimits never cuts a live leaf (D203). 0 when
// the same, or unknown: nothing running, no cgroups, a VM, no cap. It asks
// LimitsFor, so the tile-limits store's lock must not be held.
func (r *Runner) AppliedMem(tile string) int64 {
	cg, s := r.cgroups(), r.existingState(tile)
	if cg == nil || s == nil || r.LimitsFor == nil {
		return 0
	}
	s.mu.Lock()
	inst, dep := s.cur, s.dep
	s.mu.Unlock()
	if inst == nil || inst.leaf == "" {
		return 0
	}
	if _, isVM := r.vmLeafBytes(inst.sock); isVM {
		return 0
	}
	u, ok := cg.Usage(inst.leaf)
	if !ok || u.MemMax <= 0 || u.MemMax == r.leafLimits(tile, dep).MemMax {
		return 0
	}
	return u.MemMax >> 20
}

// AppliedVM is the size tile's primary's running VM has, when it differs
// from what its next start gets (guestSize with the tile's grant): a size
// that waits for a restart (D203). 0 for each that is the same, or
// unknown. It asks VMGrant: the store's lock must not be held.
func (r *Runner) AppliedVM(tile string) (memMiB, vcpus int) {
	s := r.existingState(tile)
	if s == nil || r.VM == nil || r.Reg == nil {
		return 0, 0
	}
	s.mu.Lock()
	inst := s.cur
	s.mu.Unlock()
	c, ok := r.Reg.Component(tile)
	if inst == nil || !ok {
		return 0, 0
	}
	v, ok := r.vmInfo(inst.sock)
	if !ok {
		return 0, 0
	}
	mem, cpus := guestSize(r.VM.Policy(), c.Manifest.VM, r.vmGrant(tile))
	if v.MemMiB != mem {
		memMiB = v.MemMiB
	}
	if v.VCPUs != cpus {
		vcpus = v.VCPUs
	}
	return memMiB, vcpus
}
