package runner

// Live per-tile resource stats for the admin console's resources tab: CPU,
// memory, I/O rates and process counts, sampled every statsInterval with a
// short history for charts. Two collection sources:
//
//   - cgroup v2 leaves (comp-<key>) when delegation is on (systemd
//     Delegate=yes — the installed service): accurate whole-tree memory/CPU/
//     pids, regardless of sandbox sub-uids;
//   - a /proc scan fallback (make dev, no delegation): the backend pid's
//     descendant tree, summed.
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
	series map[string][]StatsPoint
	prev   map[string]statsRaw
	poll   atomic.Int64 // unix nano of the last snapshot request
	once   sync.Once
}

// StatsSnapshot returns the current stats for every live tile, starting the
// sampler on first use. Sorted by path; series oldest-first.
func (r *Runner) StatsSnapshot() map[string]any {
	r.stats.poll.Store(time.Now().UnixNano())
	r.stats.once.Do(func() {
		r.statsSample() // prime immediately so the first paint has data
		go r.statsLoop()
	})

	r.stats.mu.Lock()
	defer r.stats.mu.Unlock()
	tiles := make([]TileStats, 0, len(r.stats.series))
	for comp, ser := range r.stats.series {
		if len(ser) == 0 {
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
	cg := r.Cgroup != nil && r.Cgroup.Enabled()
	if cg {
		for _, c := range r.Reg.Components() {
			if pids, ok := r.Cgroup.Procs(util.CompKey(c.Path)); ok && len(pids) > 0 {
				targets[c.Path] = pids
			}
		}
	} else if roots := r.backendPids(); len(roots) > 0 {
		// Dev fallback: one /proc scan, then each backend's descendant tree.
		children := procChildren()
		for comp, root := range roots {
			targets[comp] = procDescendants(children, root)
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
		if cg {
			if u, ok := r.Cgroup.Usage(util.CompKey(comp)); ok {
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

// backendPids maps each running component to its backend's root pid.
func (r *Runner) backendPids() map[string]int {
	r.mu.Lock()
	states := make([]*state, 0, len(r.states))
	for _, s := range r.states {
		states = append(states, s)
	}
	r.mu.Unlock()
	out := map[string]int{}
	for _, s := range states {
		s.mu.Lock()
		if s.cur != nil && s.cur.cmd != nil && s.cur.cmd.Process != nil {
			out[s.comp] = s.cur.cmd.Process.Pid
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