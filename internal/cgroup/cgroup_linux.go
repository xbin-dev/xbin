//go:build linux

// Package cgroup attaches component backends to per-component cgroup v2 groups
// so xbind can report their memory/CPU/pids usage (plans/isolation.md). It is
// best-effort: it works when xbind's cgroup is delegated and writable (a
// systemd user service with Delegate=yes, or a container's own cgroup) and
// quietly disables itself otherwise.
//
// A tile's generations share one flat leaf, comp-<CompKey>, until the tile
// first runs a deployment beyond main; from then they start in the per-tile
// layout (07-runtime §10.3):
//
//	tile-<CompKey>/          the tile's share of the box (TileWeight); no process
//	  d-<deployment>/        primary first (DeploymentWeight, P25); no process
//	    backend              that deployment's generations, with its own caps (P22)
//
// Zero-state and main-only tiles keep the flat leaf, byte for byte (P5).
// Tile sandboxes, every deployment's, have their leaves in a sibling subtree
// of their own, comp-tilesbx-<ws8> (Parent, leaf_linux.go;
// plans/tile-sandbox-runtime.md §6.2): the sandboxes policy sizes it, and
// no backend's caps or per-tile counters take them in.
package cgroup

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const mount = "/sys/fs/cgroup"

// Usage is a snapshot of one cgroup's resource accounting.
type Usage struct {
	MemCurrent  int64 `json:"memCurrent"`
	MemMax      int64 `json:"memMax"` // -1 = unlimited ("max")
	CPUUsec     int64 `json:"cpuUsec"`
	PidsCurrent int64 `json:"pidsCurrent"`
}

// Limits are the per-component resource caps written to each leaf. A runaway
// tile then OOMs / is throttled / can't fork *alone* instead of taking the box
// down (plans/isolation.md). Zero fields are left at the cgroup default.
type Limits struct {
	MemMax    int64 // memory.max, bytes (0 = unlimited)
	PidsMax   int64 // pids.max (0 = unlimited)
	CPUWeight int64 // cpu.weight 1..10000 (0 = default 100). Fair share under
	//                  contention, full burst when idle — no hard cpu.max.
	// MemHigh is memory.high, bytes: 0 = ⅞ of MemMax, <0 = none (a VM holds
	// its guest's memory by design; reclaim below that only stalls it).
	MemHigh int64
	// CPUMax is a hard CPU cap (cpu.max): µs of CPU time per 100 ms period,
	// so 100000 = one CPU; 0 = none. Backends keep bursting on their weight;
	// a tile sandbox is held to the vCPUs it was sized with
	// (plans/tile-sandbox-runtime.md §6.2).
	CPUMax int64
	// NoSwap writes memory.swap.max 0: MemMax is then all the memory the
	// leaf gets — a tile sandbox's memMiB is a cap, not a floor under the
	// host's swap (plans/tile-sandbox-runtime.md §6.2).
	NoSwap bool

	// NodeWeight is the cpu.weight written on a nested leaf's parent, the
	// deployment node d-<name>: DeploymentWeight(primary) (P25). 0 leaves
	// that node's weight as it is (a new node starts at the kernel's 100). A
	// flat leaf has no such node and ignores it.
	NodeWeight int64
}

// The per-tile layout's CPU weights (07-runtime §10.3). There is no memory or
// pids cap above the leaves: no cap is shared by the primary and a
// non-primary deployment (P25).
const (
	// TileWeight is tile-<CompKey>'s: one flat leaf's share, so a tile's
	// deployments never enlarge its share of the box.
	TileWeight = 100
	// PrimaryWeight is the primary deployment's node's.
	PrimaryWeight = 100
	// NonPrimaryWeight is every other deployment's node's: under
	// contention a busy one takes at most a third of the tile's share.
	NonPrimaryWeight = 50
)

const (
	tilePrefix      = "tile-"
	deployPrefix    = "d-"
	nodeControllers = "+cpu +memory +pids"
	maxTreeDepth    = 8 // the layout is three deep; walks stop well past it
)

// TileNode names the per-tile parent of the tile whose CompKey is compKey.
// It ends in '/', so by the name rule it addresses the node itself, never a
// flat leaf: Usage and AtLimit read its counters, which are hierarchical, and
// ProcsTree every process beneath it.
func TileNode(compKey string) string { return tilePrefix + compKey + "/" }

// DeploymentNode names deployment dep's node under the tile's parent.
func DeploymentNode(compKey, dep string) string {
	return tilePrefix + compKey + "/" + deployPrefix + dep
}

// DeploymentLeaf names deployment dep's backend leaf in the per-tile layout;
// its generations (blue/green) share it.
func DeploymentLeaf(compKey, dep string) string { return DeploymentNode(compKey, dep) + "/backend" }

// DeploymentWeight is a deployment node's cpu.weight: primary first (P25).
func DeploymentWeight(primary bool) int64 {
	if primary {
		return PrimaryWeight
	}
	return NonPrimaryWeight
}

// Leaf is one of a tile's leaves (TileLeaves).
type Leaf struct {
	// Name is what Usage, AtLimit, Procs and Remove take: the CompKey for
	// the flat leaf, the nested path otherwise.
	Name string
	// Deployment is the deployment whose node holds the leaf; "" for the
	// flat leaf.
	Deployment string
}

// cpuPeriod is cpu.max's period, µs.
const cpuPeriod = 100000

// Manager owns xbind's delegated cgroup subtree and per-component leaves —
// or, made by Parent, one cgroup inside it and the leaves under that.
type Manager struct {
	base    string // the delegated base cgroup dir (a Parent's: its own dir)
	enabled bool
	limits  Limits
	parent  bool // made by Parent: SetLimits re-writes base's own limits

	// mu orders making a leaf (mkdir … cgroup.procs) against removing one,
	// so an exiting generation can never rmdir a leaf, or a parent, that a
	// starting one has made but not yet joined.
	mu sync.Mutex
	// write and rmdir are os.WriteFile and os.Remove; the tests observe
	// the order of writes, and give a plain directory cgroupfs's rmdir.
	write func(path, val string) error
	rmdir func(path string) error
}

// SetLimits installs the per-component caps applied to every leaf in Add.
// On a Parent's Manager it re-writes the parent's own memory.max and
// pids.max instead (a policy change; see Parent), and says when that failed.
func (m *Manager) SetLimits(l Limits) error {
	switch {
	case m == nil:
	case m.parent:
		if m.enabled {
			return writeParentLimits(m.base, l)
		}
	default:
		m.limits = l
	}
	return nil
}

// New sets up (if possible) a delegated subtree: xbind moves itself into a
// leaf so controllers can be enabled for sibling per-component groups. Returns
// a disabled Manager (safe no-op) when delegation isn't available. Once
// enabled it sweeps what a crashed xbind left of the per-tile layout.
func New() *Manager {
	m := &Manager{}
	rel, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return m
	}
	// "0::/path\n"
	line := strings.TrimSpace(string(rel))
	i := strings.Index(line, "::")
	if i < 0 {
		return m
	}
	m.base = filepath.Join(mount, line[i+2:])

	// Move xbind into an "init" leaf (cgroup v2 forbids enabling controllers on
	// a cgroup that has processes directly in it).
	initLeaf := filepath.Join(m.base, "init")
	if err := os.Mkdir(initLeaf, 0o755); err != nil && !os.IsExist(err) {
		return m // not writable / not delegated
	}
	if err := os.WriteFile(filepath.Join(initLeaf, "cgroup.procs"), []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		return m
	}
	// Enable the controllers we report on for children (ignore already-on).
	_ = os.WriteFile(filepath.Join(m.base, "cgroup.subtree_control"), []byte(nodeControllers), 0o644)
	// Verify a controller is actually available to children.
	if sc, err := os.ReadFile(filepath.Join(m.base, "cgroup.subtree_control")); err == nil && strings.Contains(string(sc), "memory") {
		m.enabled = true
		m.sweep()
	}
	return m
}

// Enabled reports whether cgroup accounting is active.
func (m *Manager) Enabled() bool { return m != nil && m.enabled }

// leaf is a flat name's directory, comp-<name> (Parent and its leaves).
func (m *Manager) leaf(name string) string { return filepath.Join(m.base, "comp-"+name) }

// path maps a name to its cgroup directory. A name holding '/' is a path
// under the base, used verbatim: the per-tile layout, whose first element
// must be a tile-<CompKey> node and none of whose elements may be empty, "."
// or ".." (one trailing '/' addresses a node, TileNode's form). Any other
// name keeps today's flat leaf comp-<name>. ok=false for a malformed nested
// name, which every operation treats as absent.
func (m *Manager) path(name string) (dir string, nested, ok bool) {
	if !strings.Contains(name, "/") {
		return filepath.Join(m.base, "comp-"+name), false, true
	}
	rel := strings.TrimSuffix(name, "/")
	parts := strings.Split(rel, "/")
	if len(parts[0]) <= len(tilePrefix) || !strings.HasPrefix(parts[0], tilePrefix) {
		return "", true, false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return "", true, false
		}
	}
	return filepath.Join(m.base, rel), true, true
}

func (m *Manager) writeFile(path, val string) error {
	if m.write != nil {
		return m.write(path, val)
	}
	return osWrite(path, val)
}

func (m *Manager) remove(dir string) error {
	if m.rmdir != nil {
		return m.rmdir(dir)
	}
	return os.Remove(dir)
}

func osWrite(path, val string) error { return os.WriteFile(path, []byte(val), 0o644) }

// Add creates a per-component leaf, applies the caps, and moves pid into it.
// Limits are written before the pid joins so they bind the process's whole
// lifetime. Safe no-op when disabled.
func (m *Manager) Add(name string, pid int) { m.AddLimited(name, pid, m.limits) }

// AddLimited is Add with l as the leaf's caps instead of the installed ones:
// a deployment's own limits, which default to the tile's (P22). Like Add, it
// joins a leaf that exists (a deployment's generations share theirs), where
// AddWith, for a tile sandbox's leaf of its own, refuses a leftover one. A
// nested name's parents are made first, each enabling the controllers for
// its children before anything is made beneath it: the tile node at
// TileWeight, the leaf's parent at l.NodeWeight. A node address (a trailing
// '/') is refused: the tile node never holds a process.
func (m *Manager) AddLimited(name string, pid int, l Limits) { m.add(name, pid, l, false, 0) }

// AddMem is Add with the leaf's memory.max set to memMax instead of the
// shared cap, and no soft ceiling: a VM sandbox (plans/vm-sandbox.md) holds
// its guest's memory by design, so its leaf is sized to guest + VMM overhead
// and reclaim throttling below that would only stall the guest.
func (m *Manager) AddMem(name string, pid int, memMax int64) {
	m.AddMemLimited(name, pid, m.limits, memMax)
}

// AddMemLimited is AddMem with l's pids cap and weights (l.MemMax gives way
// to memMax), as AddLimited is to Add.
func (m *Manager) AddMemLimited(name string, pid int, l Limits, memMax int64) {
	m.add(name, pid, l, true, memMax)
}

func (m *Manager) add(name string, pid int, l Limits, vm bool, memMax int64) {
	if !m.Enabled() {
		return
	}
	leaf, nested, ok := m.path(name)
	if !ok || strings.HasSuffix(name, "/") {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if nested {
		parent := filepath.Dir(leaf)
		if !m.makeParents(parent) {
			return
		}
		if l.NodeWeight > 0 {
			_ = m.writeFile(filepath.Join(parent, "cpu.weight"), strconv.FormatInt(l.NodeWeight, 10))
		}
	}
	if err := os.Mkdir(leaf, 0o755); err != nil && !os.IsExist(err) {
		return
	}
	if vm {
		// Sized to guest + VMM overhead, and no soft ceiling (AddMem).
		l.MemMax, l.MemHigh = max(memMax, 0), -1
	}
	writeLimitsTo(m.writeFile, leaf, l)
	_ = m.writeFile(filepath.Join(leaf, "cgroup.procs"), strconv.Itoa(pid))
}

// makeParents makes each node from the base down to dir, enabling the
// controllers for its children before the next is made; the tile node gets
// TileWeight. Every step is repeated on an existing node, so a node a crash
// left half-made is finished. false when a node can't be made.
func (m *Manager) makeParents(dir string) bool {
	rel, err := filepath.Rel(m.base, dir)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return false
	}
	node := m.base
	for i, p := range strings.Split(rel, string(filepath.Separator)) {
		node = filepath.Join(node, p)
		if err := os.Mkdir(node, 0o755); err != nil && !os.IsExist(err) {
			return false
		}
		m.enableControllers(node)
		if i == 0 {
			_ = m.writeFile(filepath.Join(node, "cpu.weight"), strconv.Itoa(TileWeight))
		}
	}
	return true
}

// enableControllers turns on cpu, memory and pids for node's children. The
// kernel refuses the whole line when one controller isn't delegated, so on
// failure each is asked for alone: memory and pids caps still bind without
// cpu, as they do on a flat leaf.
func (m *Manager) enableControllers(node string) {
	sc := filepath.Join(node, "cgroup.subtree_control")
	if m.writeFile(sc, nodeControllers) == nil {
		return
	}
	for _, c := range strings.Fields(nodeControllers) {
		_ = m.writeFile(sc, c)
	}
}

// writeLimits applies l to a leaf (best-effort per file — a missing controller
// just leaves that cap at the default).
func writeLimits(leaf string, l Limits) { writeLimitsTo(osWrite, leaf, l) }

func writeLimitsTo(write func(path, val string) error, leaf string, l Limits) {
	set := func(file, val string) { _ = write(filepath.Join(leaf, file), val) }
	if l.MemMax > 0 {
		set("memory.max", strconv.FormatInt(l.MemMax, 10))
	}
	// A soft ceiling a little under the hard one: reclaim pressure kicks in
	// before the OOM kill, so a gradual leak is throttled first.
	switch {
	case l.MemHigh > 0:
		set("memory.high", strconv.FormatInt(l.MemHigh, 10))
	case l.MemHigh == 0 && l.MemMax > 0:
		set("memory.high", strconv.FormatInt(l.MemMax-l.MemMax/8, 10))
	}
	if l.PidsMax > 0 {
		set("pids.max", strconv.FormatInt(l.PidsMax, 10))
	}
	if l.CPUWeight > 0 {
		set("cpu.weight", strconv.FormatInt(l.CPUWeight, 10)) // fair share; no cpu.max = burst when idle
	}
	if l.CPUMax > 0 {
		set("cpu.max", strconv.FormatInt(l.CPUMax, 10)+" "+strconv.Itoa(cpuPeriod))
	}
	if l.NoSwap {
		set("memory.swap.max", "0")
	}
}

// AtLimit reads a leaf's memory/pids limit-hit counters (memory.events "max"
// + "oom", pids.events "max") — nonzero means the component has bumped a cap,
// which the alert monitor surfaces. On a node (TileNode, DeploymentNode) the
// counters are hierarchical: every leaf beneath it counts. ok=false if
// disabled/absent.
func (m *Manager) AtLimit(name string) (mem, pids int64, ok bool) {
	if !m.Enabled() {
		return 0, 0, false
	}
	leaf, _, valid := m.path(name)
	if !valid {
		return 0, 0, false
	}
	if _, err := os.Stat(leaf); err != nil {
		return 0, 0, false
	}
	mem = eventCount(filepath.Join(leaf, "memory.events"), "max") + eventCount(filepath.Join(leaf, "memory.events"), "oom")
	pids = eventCount(filepath.Join(leaf, "pids.events"), "max")
	return mem, pids, true
}

// eventCount reads "<key> <n>" from a cgroup .events file.
func eventCount(path, key string) int64 {
	for _, ln := range strings.Split(readStr(path), "\n") {
		f := strings.Fields(ln)
		if len(f) == 2 && f[0] == key {
			n, _ := strconv.ParseInt(f[1], 10, 64)
			return n
		}
	}
	return 0
}

// Usage reads a component leaf's accounting; ok=false if disabled/absent. On
// a node (TileNode, DeploymentNode) memory.current, pids.current and cpu.stat
// are hierarchical, the totals of every leaf beneath it, and MemMax is the
// node's own (-1: v1 caps no node).
func (m *Manager) Usage(name string) (Usage, bool) {
	if !m.Enabled() {
		return Usage{}, false
	}
	leaf, _, valid := m.path(name)
	if !valid {
		return Usage{}, false
	}
	if _, err := os.Stat(leaf); err != nil {
		return Usage{}, false
	}
	u := Usage{MemMax: -1}
	u.MemCurrent = readInt(filepath.Join(leaf, "memory.current"))
	if s := strings.TrimSpace(readStr(filepath.Join(leaf, "memory.max"))); s != "" && s != "max" {
		u.MemMax, _ = strconv.ParseInt(s, 10, 64)
	}
	u.PidsCurrent = readInt(filepath.Join(leaf, "pids.current"))
	for _, ln := range strings.Split(readStr(filepath.Join(leaf, "cpu.stat")), "\n") {
		if v, ok := strings.CutPrefix(ln, "usage_usec "); ok {
			u.CPUUsec, _ = strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		}
	}
	return u, true
}

// Procs lists the pids currently in a leaf (the backend's whole process
// tree — cgroup membership is inherited). ok=false if disabled/absent. It
// names only the leaf's own members: a node's are ProcsTree's.
func (m *Manager) Procs(name string) (pids []int, ok bool) {
	if !m.Enabled() {
		return nil, false
	}
	leaf, _, valid := m.path(name)
	if !valid {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(leaf, "cgroup.procs"))
	if err != nil {
		return nil, false
	}
	return parsePids(string(b)), true
}

// ProcsTree lists the pids in a node and in every node beneath it:
// cgroup.procs names only a cgroup's own members, and in the per-tile layout
// the processes sit in the leaves. For a flat leaf it is Procs. ok=false if
// disabled/absent.
func (m *Manager) ProcsTree(name string) (pids []int, ok bool) {
	if !m.Enabled() {
		return nil, false
	}
	dir, _, valid := m.path(name)
	if !valid {
		return nil, false
	}
	if fi, err := os.Lstat(dir); err != nil || !fi.IsDir() {
		return nil, false
	}
	walkNodes(dir, 0, func(node string) { pids = append(pids, parsePids(readStr(filepath.Join(node, "cgroup.procs")))...) })
	return pids, true
}

// walkNodes calls fn on dir and on every cgroup beneath it, parents first.
// DirEntry.IsDir never follows a symlink.
func walkNodes(dir string, depth int, fn func(node string)) {
	fn(dir)
	if depth >= maxTreeDepth {
		return
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			walkNodes(filepath.Join(dir, e.Name()), depth+1, fn)
		}
	}
}

// TileProcs lists every pid of the tile whose CompKey is compKey: its flat
// leaf's and, where it has the per-tile parent, every leaf's beneath that —
// both while main's old generation drains in the flat leaf. For a tile
// without the parent it is Procs(compKey). ok=false when neither exists.
func (m *Manager) TileProcs(compKey string) (pids []int, ok bool) {
	flat, fok := m.Procs(compKey)
	tree, tok := m.ProcsTree(TileNode(compKey))
	if !tok {
		return flat, fok
	}
	return append(flat, tree...), true
}

// TileUsage is the tile's accounting: its per-tile parent's hierarchical
// counters where it has one, plus its flat leaf's while main's old generation
// drains there (MemMax -1 then: no one cap bounds both). For a tile without
// the parent it is Usage(compKey), so consumers keyed by the flat leaf read
// the new layout only for tiles that have it.
func (m *Manager) TileUsage(compKey string) (Usage, bool) {
	flat, fok := m.Usage(compKey)
	node, nok := m.Usage(TileNode(compKey))
	switch {
	case !nok:
		return flat, fok
	case !fok:
		return node, true
	}
	return Usage{MemCurrent: flat.MemCurrent + node.MemCurrent, MemMax: -1,
		CPUUsec: flat.CPUUsec + node.CPUUsec, PidsCurrent: flat.PidsCurrent + node.PidsCurrent}, true
}

// TileLeaves lists the leaves of the tile whose CompKey is compKey: its flat
// leaf when that exists, then each leaf beneath its per-tile parent, sorted,
// with the deployment whose node holds it. Each leaf keeps its own limit
// counters, so an at-limit hit names its deployment (P25). Nil when the tile
// has none.
func (m *Manager) TileLeaves(compKey string) []Leaf {
	if !m.Enabled() {
		return nil
	}
	var out []Leaf
	if dir, _, _ := m.path(compKey); isDir(dir) {
		out = append(out, Leaf{Name: compKey})
	}
	top, _, ok := m.path(TileNode(compKey))
	if !ok || !isDir(top) {
		return out
	}
	var nested []Leaf
	walkNodes(top, 0, func(node string) {
		if node == top || hasChildNode(node) {
			return
		}
		rel, err := filepath.Rel(m.base, node)
		if err != nil {
			return
		}
		rel = filepath.ToSlash(rel)
		l := Leaf{Name: rel}
		if parts := strings.Split(rel, "/"); len(parts) > 1 {
			if dep, ok := strings.CutPrefix(parts[1], deployPrefix); ok {
				l.Deployment = dep
			}
		}
		nested = append(nested, l)
	})
	sort.Slice(nested, func(i, j int) bool { return nested[i].Name < nested[j].Name })
	return append(out, nested...)
}

func isDir(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.IsDir()
}

func hasChildNode(dir string) bool {
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if e.IsDir() {
			return true
		}
	}
	return false
}

// Remove deletes a leaf after its process has exited (best-effort). A flat
// leaf is removed as it always was. A nested one is removed only while its
// cgroup.procs is empty — another generation of the same deployment may have
// joined it — and then each parent left without children or processes,
// bottom-up, never the base: removing one deployment's leaf never removes
// another's.
func (m *Manager) Remove(name string) {
	if !m.Enabled() {
		return
	}
	dir, nested, ok := m.path(name)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !nested {
		_ = m.remove(dir)
		return
	}
	for d := dir; strings.HasPrefix(d, m.base+string(filepath.Separator)); d = filepath.Dir(d) {
		if !m.removeEmpty(d) {
			return
		}
	}
}

// removeEmpty removes the cgroup dir when no process is in it (rmdir itself
// refuses one with children); true when dir is gone.
func (m *Manager) removeEmpty(dir string) bool {
	if len(parsePids(readStr(filepath.Join(dir, "cgroup.procs")))) > 0 {
		return false
	}
	err := m.remove(dir)
	return err == nil || errors.Is(err, fs.ErrNotExist)
}

// sweep removes what a crashed xbind left of the per-tile layout: in every
// tile-* subtree each node without processes or children, bottom-up, and so
// a whole tree that holds no process. A subtree that still holds processes
// is logged and kept with the nodes above them; the orphans are the
// sandboxes' orphan sweep's to kill (07-runtime §10.3). New runs it at boot.
func (m *Manager) sweep() {
	ents, _ := os.ReadDir(m.base)
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, e := range ents {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), tilePrefix) {
			continue
		}
		dir := filepath.Join(m.base, e.Name())
		if !m.pruneTree(dir, 0) {
			var n int
			walkNodes(dir, 0, func(node string) { n += len(parsePids(readStr(filepath.Join(node, "cgroup.procs")))) })
			slog.Warn("cgroup: kept a per-tile subtree that still holds processes from an earlier run", "node", e.Name(), "procs", n)
		}
	}
}

// pruneTree removes dir's empty nodes bottom-up, then dir itself if it is
// left empty; true when dir is gone.
func (m *Manager) pruneTree(dir string, depth int) bool {
	if depth < maxTreeDepth {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() {
				m.pruneTree(filepath.Join(dir, e.Name()), depth+1)
			}
		}
	}
	return m.removeEmpty(dir)
}

func parsePids(s string) (pids []int) {
	for _, ln := range strings.Split(s, "\n") {
		if n, err := strconv.Atoi(strings.TrimSpace(ln)); err == nil {
			pids = append(pids, n)
		}
	}
	return pids
}

func readStr(p string) string { b, _ := os.ReadFile(p); return string(b) }

func readInt(p string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(readStr(p)), 10, 64)
	return n
}
