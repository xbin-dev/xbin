package boot

// limitsources.go — the runtime's answers to the tile-limits store (D203,
// plans/tile-limits.md §2, §5, §6; broker.LimitSources): each kind's
// default, maximum, a tile's usage, the workspace's room, and what a
// decision does. Every kind but diskGiB, which is the broker's own (its
// disk monitor). The store may ask a default under its own lock, and the
// tile-sandbox runtime asks the store under its own (Granted), so nothing
// here that a default reads takes the runtime's lock.

import (
	"bufio"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// limitSources are the runtime's answers.
func (st *State) limitSources() broker.LimitSources {
	room := &roomCache{read: st.limitRoom}
	return broker.LimitSources{
		Default:  st.limitDefault,
		Max:      st.limitMax,
		Usage:    st.limitUsage,
		Capacity: room.get,
		Apply:    st.applyLimit,
		Applied:  st.limitApplied,
	}
}

// limitDefault is kind k's default for tile and whether it is enforced
// there: memMiB the per-tile cap (XBIN_LIMIT_MEM), on a backend that isn't
// a VM's, with cgroup delegation; the VM kinds the VM policy's sizes, on a
// VM backend that can run; the sandbox kinds the sandboxes policy's
// totals for the tile, without a grant, where tile sandboxes can run. For
// tile "" it is the workspace's default, enforced where the runtime runs.
func (st *State) limitDefault(tile string, k limits.Kind) (int64, bool) {
	c, ok := st.Reg.Component(tile)
	backend := ok && c.HasBackend()
	vmTile := backend && st.Cfg.Isolate && c.Manifest.VM.Enabled()
	if tile == "" { // the workspace's: any backend of its kind
		backend, vmTile = k == limits.MemMiB, k == limits.VMMemMiB || k == limits.VMVCPUs
		vmTile = vmTile && st.Cfg.Isolate
	}
	switch k {
	case limits.MemMiB:
		return st.Deployments.TileLimits.MemMax >> 20, backend && !vmTile && st.Run.Cgroup.Enabled()
	case limits.VMMemMiB, limits.VMVCPUs:
		p := st.VM.Policy() // nil-safe: the defaults
		on := vmTile && st.VM != nil && p.Backends
		if k == limits.VMVCPUs {
			return int64(p.VCPUs), on
		}
		return int64(p.MemMiB), on
	case limits.SbxMemMiB, limits.SbxVCPUs, limits.SbxDiskGiB:
		if st.TileSbx == nil {
			return 0, false
		}
		pt := st.TileSbx.PolicyPerTile(tile)
		return int64(map[limits.Kind]int{limits.SbxMemMiB: pt.MemMiB, limits.SbxVCPUs: pt.VCPUs, limits.SbxDiskGiB: pt.DiskGiB}[k]), st.TileSbx.Isolated()
	}
	return 0, false
}

// limitMax is the most any tile may have of k (0 = none): memMiB the
// host's memory (MemTotal ∧ xbind's cgroup ancestors); vmMemMiB the VM
// memory budget, vmVCPUs min(32, the CPUs); the sandbox kinds every tile
// sandbox's memory together and the policy's per-sandbox bounds, disk
// also the partition less its reserve.
func (st *State) limitMax(k limits.Kind) int64 {
	switch k {
	case limits.MemMiB:
		return runner.HostMemory() >> 20
	case limits.VMMemMiB:
		return int64(st.VM.Policy().BudgetMiB)
	case limits.VMVCPUs:
		return int64(min(32, goruntime.NumCPU()))
	case limits.SbxMemMiB, limits.SbxVCPUs, limits.SbxDiskGiB:
		if st.TileSbx == nil {
			return 0
		}
		mem, cpus, disk := st.TileSbx.GrantMax()
		switch k {
		case limits.SbxMemMiB:
			return int64(mem)
		case limits.SbxVCPUs:
			return int64(cpus)
		}
		if _, total := statfsGiB(st.WS); total > 0 {
			return min(int64(disk), total-int64(float64(total)*limits.ReserveFraction))
		}
		return int64(disk)
	}
	return 0
}

// limitUsage is what tile uses of k now (0 = unknown or none): its
// backend's memory as the stats sampler last read it, its VM guests'
// reservation, its sandboxes' booked memory and vCPUs and measured disk.
func (st *State) limitUsage(tile string, k limits.Kind) int64 {
	switch k {
	case limits.MemMiB:
		return st.Run.TileMem(tile) >> 20
	case limits.VMMemMiB:
		return int64(st.VM.UsedBy()[tile].MemMiB) // nil-safe
	case limits.VMVCPUs: // not booked: its running VM backends' guests
		var n int64
		for _, e := range st.Sbx.List(sbx.Filter{Tile: tile, Kind: sbx.Backend}) {
			if e.Mode == sbx.VM {
				n += int64(e.VCPUs)
			}
		}
		return n
	case limits.SbxMemMiB, limits.SbxVCPUs, limits.SbxDiskGiB:
		if st.TileSbx == nil {
			return 0
		}
		mem, cpus, disk := st.TileSbx.TileHeld(tile)
		return map[limits.Kind]int64{limits.SbxMemMiB: int64(mem), limits.SbxVCPUs: int64(cpus), limits.SbxDiskGiB: (disk + 1<<30 - 1) >> 30}[k]
	}
	return 0
}

// limitRoom is the workspace's room for k now (the store fills Reserve and
// Promised): memory the host's, free as MemAvailable within xbind's
// cgroup ancestors' headroom; the VM budget and what is reserved of it;
// every tile sandbox's memory together and what is booked of it; CPUs;
// the workspace partition for sandbox disk.
func (st *State) limitRoom(k limits.Kind) limits.Capacity {
	c := limits.Capacity{Kind: k, Max: st.limitMax(k)}
	switch k {
	case limits.MemMiB:
		c.Total, c.Free = runner.HostMemory()>>20, memFree()>>20
	case limits.VMMemMiB:
		if st.VM != nil {
			c.Total = int64(st.VM.Policy().BudgetMiB)
			c.Free = max(0, c.Total-int64(st.VM.Used().MemMiB))
		}
	case limits.VMVCPUs, limits.SbxVCPUs:
		c.Total = int64(goruntime.NumCPU())
	case limits.SbxMemMiB:
		if st.TileSbx != nil {
			used, capMiB := st.TileSbx.TotalBook()
			c.Total, c.Free = int64(capMiB), max(0, int64(capMiB-used))
		}
	case limits.SbxDiskGiB:
		c.Free, c.Total = statfsGiB(st.WS)
	}
	return c
}

// applyLimit makes a decision on tile's kind k take effect: a backend's
// memory raised live (a lower one at its next start), a crash-looping tile
// or restart restarted onto it; a VM's size at its next start (restart:
// now, which stops the guest first); the sandbox kinds at the next
// admission, which reads the grant (tilesbx limitsFor).
func (st *State) applyLimit(tile string, k limits.Kind, restart bool) {
	switch k {
	case limits.MemMiB, limits.VMMemMiB, limits.VMVCPUs:
		st.Run.ApplyLimits(tile, restart)
	}
}

// limitApplied is what tile runs now of k when it differs from what its
// next start gets (0: the same, or unknown): a lowered memory waiting for
// that start, a VM's size waiting for a restart.
func (st *State) limitApplied(tile string, k limits.Kind) int64 {
	switch k {
	case limits.MemMiB:
		return st.Run.AppliedMem(tile)
	case limits.VMMemMiB, limits.VMVCPUs:
		mem, cpus := st.Run.AppliedVM(tile)
		if k == limits.VMVCPUs {
			return int64(cpus)
		}
		return int64(mem)
	}
	return 0
}

// roomCache keeps each kind's room for roomTTL: the shell's prompts and
// the limits page ask it per row.
type roomCache struct {
	read func(limits.Kind) limits.Capacity
	mu   sync.Mutex
	at   map[limits.Kind]time.Time
	c    map[limits.Kind]limits.Capacity
}

const roomTTL = 5 * time.Second

func (r *roomCache) get(k limits.Kind) limits.Capacity {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at == nil {
		r.at, r.c = map[limits.Kind]time.Time{}, map[limits.Kind]limits.Capacity{}
	}
	if at, ok := r.at[k]; ok && time.Since(at) < roomTTL {
		return r.c[k]
	}
	c := r.read(k)
	r.at[k], r.c[k] = time.Now(), c
	return c
}

// memFree is the memory a raise can still come from, bytes: MemAvailable,
// within the headroom (memory.max − memory.current) of every cgroup from
// xbind's own up to the root that caps it; 0 when meminfo can't be read.
func memFree() int64 {
	free := memAvailable("/proc/meminfo")
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return free
	}
	const root = "/sys/fs/cgroup"
	for _, line := range strings.Split(string(b), "\n") {
		rel, ok := strings.CutPrefix(line, "0::")
		if !ok {
			continue
		}
		for dir := filepath.Join(root, filepath.FromSlash(rel)); len(dir) > len(root); dir = filepath.Dir(dir) {
			lim, err1 := strconv.ParseInt(strings.TrimSpace(readFileStr(filepath.Join(dir, "memory.max"))), 10, 64)
			cur, err2 := strconv.ParseInt(strings.TrimSpace(readFileStr(filepath.Join(dir, "memory.current"))), 10, 64)
			if err1 == nil && err2 == nil {
				free = min(free, max(0, lim-cur))
			}
		}
	}
	return free
}

// memAvailable is meminfo's MemAvailable in bytes; 0 when unread.
func memAvailable(meminfo string) int64 {
	f, err := os.Open(meminfo)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if rest, ok := strings.CutPrefix(sc.Text(), "MemAvailable:"); ok {
			kb, _ := strconv.ParseInt(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(rest), "kB")), 10, 64)
			return kb << 10
		}
	}
	return 0
}

func readFileStr(p string) string { b, _ := os.ReadFile(p); return string(b) }

// statfsGiB is the free and total GiB of the partition holding dir.
func statfsGiB(dir string) (free, total int64) {
	var s syscall.Statfs_t
	if syscall.Statfs(dir, &s) != nil {
		return 0, 0
	}
	return int64(s.Bavail) * int64(s.Bsize) >> 30, int64(s.Blocks) * int64(s.Bsize) >> 30
}
