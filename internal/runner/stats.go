package runner

// Live per-tile resource stats for the admin console's resources tab: CPU,
// memory, I/O rates and process counts, sampled every statsInterval with a
// short history for charts. Two collection sources:
//
//   - cgroup v2 leaves (comp-<key>) when delegation is on (systemd
//     Delegate=yes — the installed service): accurate whole-tree memory/CPU/
//     pids, regardless of sandbox sub-uids. A tile that runs deployments
//     beyond main has the per-tile parent tile-<key>/ with a leaf per
//     deployment (07-runtime §10.3): its pids are the whole tree's
//     (TileProcs, cgroup.procs isn't recursive) and its totals the parent's
//     hierarchical counters (TileUsage), with the flat leaf's while main's
//     old generation drains there. A non-main deployment's backend row in
//     the sandbox registry is also sampled by its own leaf;
//   - a /proc scan fallback (make dev, no delegation): the descendant trees
//     of the tile's backends (every deployment's current generation), summed.
//
// I/O rates always come from /proc/<pid>/io (rchar/wchar/syscr/syscw deltas —
// syscall-level I/O, which is what actually reflects a tile's file activity
// here: resource writes go through FUSE and tmpfs overlays, so block-level
// counters land on the daemons instead). Pids of other uids (container
// sub-uids) refuse /proc/<pid>/io — those trees undercount I/O; CPU/memory
// stay correct via the cgroup.
//
// The sampler is demand-driven: it starts on the first snapshot request and
// idles (skips collection) once nobody has asked for statsIdle.

import (
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

const (
	statsInterval = 2 * time.Second
	statsHistory  = 90 // ~3 minutes of points
	statsIdle     = 90 * time.Second
)

// StatsPoint is one sample of one tile.
type StatsPoint struct {
	T     int64   `json:"t"`   // unix millis
	CPU   float64 `json:"cpu"` // percent of one core (can exceed 100)
	Mem   int64   `json:"mem"` // bytes
	RBps  float64 `json:"rbps"`
	WBps  float64 `json:"wbps"`
	RIops float64 `json:"riops"`
	WIops float64 `json:"wiops"`
	Pids  int64   `json:"pids"`
}

// TileStats is the live picture of one tile: the latest point + history.
type TileStats struct {
	Path   string       `json:"path"`
	Owner  string       `json:"owner,omitempty"` // filled by the API layer
	Cur    StatsPoint   `json:"cur"`
	Series []StatsPoint `json:"series"`
}

// statsRaw carries the monotonic counters a rate needs deltas of.
type statsRaw struct {
	t                time.Time
	cpuUsec          int64
	rch, wch, sr, sw int64
}

type statsState struct {
	mu     sync.Mutex
	series map[string][]StatsPoint // by tile path; sandboxKey(id) for terminals and agents
	prev   map[string]statsRaw
	poll   atomic.Int64 // unix nano of the last snapshot request
	once   sync.Once
}

// sandboxKey is where a registry sandbox that isn't a backend (a terminal,
// an agent) keeps its series beside the tiles'.
func sandboxKey(id string) string { return "sbx:" + id }

func isSandboxKey(k string) bool { return strings.HasPrefix(k, "sbx:") }

// StatsSnapshot returns the current stats for every live tile, starting the
// sampler on first use. Sorted by path; series oldest-first.
func (r *Runner) StatsSnapshot() map[string]any {
	r.statsWanted()
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	tiles := make([]TileStats, 0, len(r.stats.series))
	for comp, ser := range r.stats.series {
		if len(ser) == 0 || isSandboxKey(comp) {
			continue
		}
		ts := TileStats{Path: comp, Cur: ser[len(ser)-1]}
		ts.Series = append([]StatsPoint(nil), ser...)
		tiles = append(tiles, ts)
	}
	sort.Slice(tiles, func(i, j int) bool { return tiles[i].Path < tiles[j].Path })
	return map[string]any{
		"cgroup":      r.Cgroup != nil && r.Cgroup.Enabled(),
		"intervalSec": statsInterval.Seconds(),
		"tiles":       tiles,
	}
}

// statsWanted marks the sampler as watched, starting it on first use.
func (r *Runner) statsWanted() {
	r.stats.poll.Store(time.Now().UnixNano())
	r.stats.once.Do(func() {
		r.statsSample() // prime immediately so the first paint has data
		go r.statsLoop()
	})
}

// SandboxStats is the latest point of every sandbox the sampler follows:
// terminals and agents by registry id, backends by tile (a tile's
// generations share its leaf, so blue/green is counted once). Like
// StatsSnapshot it keeps the sampler running while someone asks.
func (r *Runner) SandboxStats() (bySandbox, byTile map[string]StatsPoint, cgroup bool) {
	r.statsWanted()
	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	bySandbox, byTile = map[string]StatsPoint{}, map[string]StatsPoint{}
	for k, ser := range r.stats.series {
		if len(ser) == 0 {
			continue
		}
		if isSandboxKey(k) {
			bySandbox[strings.TrimPrefix(k, "sbx:")] = ser[len(ser)-1]
		} else {
			byTile[k] = ser[len(ser)-1]
		}
	}
	return bySandbox, byTile, r.Cgroup.Enabled()
}

func (r *Runner) statsLoop() {
	t := time.NewTicker(statsInterval)
	defer t.Stop()
	for range t.C {
		if time.Since(time.Unix(0, r.stats.poll.Load())) > statsIdle {
			continue // nobody is watching — don't churn /proc
		}
		r.statsSample()
	}
}

// statsSample collects one point for every tile with a live process tree.
func (r *Runner) statsSample() {
	now := time.Now()

	// Which pids belong to which tile? cgroup membership (whole tree, any
	// uid) when delegated; the backend's /proc descendant tree in dev.
	targets := map[string][]int{}
	leaves := map[string]string{} // target → its cgroup leaf, a tile's CompKey ("" = sum /proc)
	tileLeaf := map[string]bool{} // … which lives in the tile sandboxes' parent
	cgs := r.cgroups()
	cg := cgs != nil && cgs.Enabled()
	var children map[int][]int // one /proc scan per sample, when needed
	tree := func(root int) []int {
		if children == nil {
			children = procChildren()
		}
		return procDescendants(children, root)
	}
	if cg {
		for _, c := range r.Reg.Components() {
			key := util.CompKey(c.Path)
			if pids, ok := cgs.TileProcs(key); ok && len(pids) > 0 {
				targets[c.Path], leaves[c.Path] = pids, key
			}
		}
	} else {
		// Dev fallback: the tile's backends' descendant trees.
		for comp, roots := range r.backendPids() {
			for _, root := range roots {
				targets[comp] = append(targets[comp], tree(root)...)
			}
		}
	}
	// Terminals and agents (the registry's other sandboxes), and a non-main
	// deployment's backend generations: their own leaf (a VM's, a restricted
	// user's, the deployment's), else their process tree. main's backend
	// rows are counted by their tile above, as ever.
	for _, e := range r.Sandboxes.List(sbx.Filter{}) {
		if e.Kind == sbx.Backend && e.Deployment == "" {
			continue // counted by their tile above
		}
		k := sandboxKey(e.ID)
		if e.Leaf != "" {
			var pids []int
			var ok bool
			if e.Kind == sbx.Tile { // in the tile sandboxes' parent (LeafCgroup)
				pids, ok = r.LeafCgroup(e).Procs(e.Leaf)
			} else if cg {
				pids, ok = cgs.Procs(e.Leaf)
			}
			if ok && len(pids) > 0 {
				targets[k], leaves[k], tileLeaf[k] = pids, e.Leaf, e.Kind == sbx.Tile
				continue
			}
		}
		if e.PID > 0 {
			targets[k] = tree(e.PID)
		}
	}

	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	if r.stats.series == nil {
		r.stats.series = map[string][]StatsPoint{}
		r.stats.prev = map[string]statsRaw{}
	}

	live := map[string]bool{}
	for comp, pids := range targets {
		live[comp] = true
		raw := statsRaw{t: now}
		// I/O counters: always summed from /proc/<pid>/io over the tile's
		// pids (cgroup io.stat counts block I/O, which lands on the FUSE
		// daemons, not the tile — syscall-level rchar/wchar is the real
		// signal). Pids of other uids (container sub-uids) deny io: skipped.
		for _, pid := range pids {
			addProcIO(&raw, pid)
		}
		// CPU/mem/pids: from the cgroup leaf when available (exact,
		// uid-agnostic), else summed from the /proc tree.
		var mem, pidsN int64
		if leaf := leaves[comp]; leaf != "" {
			var u cgroup.Usage
			var ok bool
			switch {
			case tileLeaf[comp]: // a tile sandbox: its leaf in their parent
				u, ok = r.TileCgroup.Usage(leaf)
			case isSandboxKey(comp):
				u, ok = cgs.Usage(leaf)
			default: // a tile: its leaves, flat or nested
				u, ok = cgs.TileUsage(leaf)
			}
			if ok {
				raw.cpuUsec = u.CPUUsec
				mem = u.MemCurrent
				pidsN = u.PidsCurrent
			}
		} else {
			var cpuJiffies int64
			for _, pid := range pids {
				cpuJiffies += procCPUJiffies(pid)
				mem += procRSSBytes(pid)
			}
			raw.cpuUsec = cpuJiffies * 10000 // USER_HZ=100 → 1 jiffy = 10000µs
			pidsN = int64(len(pids))
		}

		pt := StatsPoint{T: now.UnixMilli(), Mem: mem, Pids: pidsN}
		if prev, ok := r.stats.prev[comp]; ok {
			if dt := now.Sub(prev.t).Seconds(); dt > 0 {
				perSec := func(cur, was int64) float64 {
					if cur < was { // counter reset (respawn) — skip this interval
						return 0
					}
					return float64(cur-was) / dt
				}
				// Rounded — these are display rates, full float precision
				// only bloats the JSON.
				pt.CPU = math.Round(perSec(raw.cpuUsec, prev.cpuUsec)/1e4*10) / 10 // µs/s → % of one core
				pt.RBps = math.Round(perSec(raw.rch, prev.rch))
				pt.WBps = math.Round(perSec(raw.wch, prev.wch))
				pt.RIops = math.Round(perSec(raw.sr, prev.sr)*10) / 10
				pt.WIops = math.Round(perSec(raw.sw, prev.sw)*10) / 10
			}
		}
		r.stats.prev[comp] = raw
		ser := append(r.stats.series[comp], pt)
		if len(ser) > statsHistory {
			ser = ser[len(ser)-statsHistory:]
		}
		r.stats.series[comp] = ser
	}

	// Drop tiles whose backend is gone so the table and memory don't grow
	// without bound.
	for comp := range r.stats.series {
		if !live[comp] {
			delete(r.stats.series, comp)
			delete(r.stats.prev, comp)
		}
	}
}

// backendPids maps each running tile to its backends' root pids: every
// deployment's current generation, the primary's first.
func (r *Runner) backendPids() map[string][]int {
	out := map[string][]int{}
	for _, s := range r.allStates("") {
		primary := s.dep == r.primary(s.comp) // asked outside the state's lock
		s.mu.Lock()
		if s.cur != nil && s.cur.cmd != nil && s.cur.cmd.Process != nil {
			pid := s.cur.cmd.Process.Pid
			if primary {
				out[s.comp] = append([]int{pid}, out[s.comp]...)
			} else {
				out[s.comp] = append(out[s.comp], pid)
			}
		}
		s.mu.Unlock()
	}
	return out
}

// --- /proc helpers ---------------------------------------------------------

// addProcIO adds a pid's /proc/<pid>/io counters into raw. Reading another
// uid's io is denied (EACCES) — that pid's I/O is simply not counted.
func addProcIO(raw *statsRaw, pid int) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/io")
	if err != nil {
		return
	}
	for _, ln := range strings.Split(string(data), "\n") {
		key, val, ok := strings.Cut(ln, ": ")
		if !ok {
			continue
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(val), 10, 64)
		switch key {
		case "rchar":
			raw.rch += n
		case "wchar":
			raw.wch += n
		case "syscr":
			raw.sr += n
		case "syscw":
			raw.sw += n
		}
	}
}

func procCPUJiffies(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0
	}
	f := statFields(string(data))
	if len(f) <= 14 {
		return 0
	}
	utime, _ := strconv.ParseInt(f[13], 10, 64)
	stime, _ := strconv.ParseInt(f[14], 10, 64)
	return utime + stime
}

func procRSSBytes(pid int) int64 {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/statm")
	if err != nil {
		return 0
	}
	f := strings.Fields(string(data))
	if len(f) < 2 {
		return 0
	}
	rssPages, _ := strconv.ParseInt(f[1], 10, 64)
	return rssPages * int64(os.Getpagesize())
}

// procChildren reads the whole process table once and returns ppid→children.
func procChildren() map[int][]int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	children := map[int][]int{}
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "stat"))
		if err != nil {
			continue
		}
		f := statFields(string(data))
		if len(f) > 3 {
			ppid, _ := strconv.Atoi(f[3])
			children[ppid] = append(children[ppid], pid)
		}
	}
	return children
}

// procDescendants returns root and all its transitive children (BFS).
func procDescendants(children map[int][]int, root int) []int {
	out := []int{root}
	for i := 0; i < len(out); i++ {
		out = append(out, children[out[i]]...)
	}
	return out
}

// LeafCgroup is where a registry row's leaf lives: a tile sandbox's in the
// tile sandboxes' parent (TileCgroup), every other in Cgroup.
func (r *Runner) LeafCgroup(e sbx.Entry) *cgroup.Manager {
	if e.Kind == sbx.Tile {
		return r.TileCgroup
	}
	return r.Cgroup
}
