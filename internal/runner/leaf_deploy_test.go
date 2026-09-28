package runner

// covers P5 P22 P25 T10 PO-11 — the cgroup half of the runner's deployment
// edges (07-runtime §10.3): TestCgroupLeafPerDeployment
// (= TestLeafPerDeployment), TestLimitAlertsNestedLeaves and
// TestStatsCountEveryDeployment, over fakeCgroup, a model of the cgroup
// tree in the manager's place (Runner.cgOps). The leaf names are
// hand-maintained literals: ckX is util.CompKey("apps/x").

import (
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/sbx"
)

// The per-tile layout of apps/x.
const (
	nodeX    = "tile-" + ckX + "/"
	leafXdev = "tile-" + ckX + "/d-dev/backend"
	leafXmn  = "tile-" + ckX + "/d-main/backend"
)

// fakeCgroup models the cgroup tree the runner asks for: a leaf exists from
// its first join until a Remove finds it without members (a real rmdir
// refuses a cgroup that holds processes), and the per-tile node exists
// while any leaf beneath it does. It logs every join and removal.
type fakeCgroup struct {
	mu     sync.Mutex
	caps   cgroup.Limits           // the caps installed on the manager (Add's)
	leaves map[string][]int        // leaf → its member pids
	log    []string                // "add <leaf> mem=… pids=… weight=…", "addmem <leaf> max=… …", "remove <leaf>"
	hits   map[string][2]int64     // leaf → memory.events max, pids.events max
	usage  map[string]cgroup.Usage // leaf → its accounting
}

func newFakeCgroup(caps cgroup.Limits) *fakeCgroup {
	return &fakeCgroup{caps: caps, leaves: map[string][]int{}, hits: map[string][2]int64{}, usage: map[string]cgroup.Usage{}}
}

func (f *fakeCgroup) Enabled() bool { return true }

func (f *fakeCgroup) Add(name string, pid int) { f.join("add", name, pid, f.caps, 0) }

func (f *fakeCgroup) AddLimited(name string, pid int, l cgroup.Limits) {
	f.join("add", name, pid, l, 0)
}

func (f *fakeCgroup) AddMem(name string, pid int, memMax int64) {
	f.join("addmem", name, pid, f.caps, memMax)
}

func (f *fakeCgroup) AddMemLimited(name string, pid int, l cgroup.Limits, memMax int64) {
	f.join("addmem", name, pid, l, memMax)
}

func (f *fakeCgroup) join(op, name string, pid int, l cgroup.Limits, memMax int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e := fmt.Sprintf("%s %s mem=%d pids=%d weight=%d", op, name, l.MemMax, l.PidsMax, l.NodeWeight)
	if op == "addmem" {
		e = fmt.Sprintf("%s %s max=%d pids=%d weight=%d", op, name, memMax, l.PidsMax, l.NodeWeight)
	}
	f.log = append(f.log, e)
	f.leaves[name] = append(f.leaves[name], pid)
}

// exit takes pid out of leaf, as its exit does.
func (f *fakeCgroup) exit(leaf string, pid int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.leaves[leaf] = slices.DeleteFunc(f.leaves[leaf], func(p int) bool { return p == pid })
}

// Remove removes a leaf without live members: a real process that exited
// has left it by itself, a made-up pid (past any pid_max) only by exit.
func (f *fakeCgroup) Remove(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, "remove "+name)
	pids, ok := f.leaves[name]
	if !ok {
		return
	}
	pids = slices.DeleteFunc(pids, func(p int) bool { return p < fakePid && !alivePid(p) })
	if f.leaves[name] = pids; len(pids) == 0 {
		delete(f.leaves, name)
	}
}

// fakePid and up are pids no process has (past the kernel's pid_max).
const fakePid = 1 << 23

func alivePid(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// logged reports whether the runner asked for entry since the last take.
func (f *fakeCgroup) logged(entry string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.log, entry)
}

// nested lists the leaves beneath node, sorted.
func (f *fakeCgroup) nested(node string) []string {
	var out []string
	for l := range f.leaves {
		if strings.HasPrefix(l, node) {
			out = append(out, l)
		}
	}
	sort.Strings(out)
	return out
}

func (f *fakeCgroup) Usage(name string) (cgroup.Usage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.HasSuffix(name, "/") {
		ls := f.nested(name)
		return f.sum(ls), len(ls) > 0
	}
	_, ok := f.leaves[name]
	return f.usage[name], ok
}

func (f *fakeCgroup) sum(leaves []string) cgroup.Usage {
	u := cgroup.Usage{MemMax: -1}
	for _, l := range leaves {
		u.MemCurrent += f.usage[l].MemCurrent
		u.CPUUsec += f.usage[l].CPUUsec
		u.PidsCurrent += f.usage[l].PidsCurrent
	}
	return u
}

func (f *fakeCgroup) AtLimit(name string) (int64, int64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.leaves[name]; !ok {
		return 0, 0, false
	}
	h := f.hits[name]
	return h[0], h[1], true
}

func (f *fakeCgroup) Procs(name string) ([]int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	pids, ok := f.leaves[name]
	return append([]int(nil), pids...), ok
}

// tileLeaves is the flat leaf, when it exists, then the nested ones.
func (f *fakeCgroup) tileLeaves(key string) []string {
	var out []string
	if _, ok := f.leaves[key]; ok {
		out = append(out, key)
	}
	return append(out, f.nested("tile-"+key+"/")...)
}

func (f *fakeCgroup) TileProcs(key string) ([]int, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ls := f.tileLeaves(key)
	var pids []int
	for _, l := range ls {
		pids = append(pids, f.leaves[l]...)
	}
	return pids, len(ls) > 0
}

func (f *fakeCgroup) TileUsage(key string) (cgroup.Usage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ls := f.tileLeaves(key)
	if len(ls) == 1 && ls[0] == key {
		return f.usage[key], true
	}
	return f.sum(ls), len(ls) > 0
}

func (f *fakeCgroup) TileLeaves(key string) []cgroup.Leaf {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []cgroup.Leaf
	for _, l := range f.tileLeaves(key) {
		lf := cgroup.Leaf{Name: l}
		if parts := strings.Split(l, "/"); len(parts) > 1 {
			lf.Deployment = strings.TrimPrefix(parts[1], "d-")
		}
		out = append(out, lf)
	}
	return out
}

// takeLog returns what the runner asked since the last take.
func (f *fakeCgroup) takeLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.log
	f.log = nil
	return out
}

func (f *fakeCgroup) has(leaf string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.leaves[leaf]
	return ok
}

// curOf is deployment dep of tile's current generation.
func curOf(t *testing.T, r *Runner, tile, dep string) *instance {
	t.Helper()
	s := r.existingStateOf(tile, dep)
	if s == nil {
		t.Fatalf("%s %s has no state", tile, dep)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		t.Fatalf("%s %s has no generation", tile, dep)
	}
	return s.cur
}

// tileCaps are the caps a manager installs: today's per-component ones.
var tileCaps = cgroup.Limits{MemMax: 2 << 30, PidsMax: 512}

// covers P5 P22 P25 T10 PO-11 — TestCgroupLeafPerDeployment
// (= TestLeafPerDeployment, 15-test-plan §3.2): a zero-state tile's
// generations join today's flat leaf with the installed caps, and a record
// alone changes nothing; the first non-main deployment creates
// tile-<CompKey>/d-<name>/backend under its own caps and the non-primary
// weight; main moves under the parent at its next generation, at the
// primary's weight, while its old one drains in the flat leaf, whose last
// exit removes it; an exiting generation removes only its own deployment's
// leaf; once the parent empties, main starts flat again. main's generations
// run through the shipped start on the host (hostWorld); dev's leaf is the
// runner's choice for it.
func TestCgroupLeafPerDeployment(t *testing.T) {
	if cgroup.TileNode(ckX) != nodeX || cgroup.DeploymentLeaf(ckX, "dev") != leafXdev {
		t.Fatalf("the per-tile layout's names: %q, %q", cgroup.TileNode(ckX), cgroup.DeploymentLeaf(ckX, "dev"))
	}
	h := newHostWorld(t, 0)
	cg := newFakeCgroup(tileCaps)
	h.r.cgOps = cg
	flat := ckX

	// A zero-state tile: today's call, the installed caps, the flat leaf.
	if got := h.ensure(); got != "g1" {
		t.Fatalf("ensure: %s", got)
	}
	g1 := curOf(t, h.r, "apps/x", "main")
	if g1.leaf != flat {
		t.Errorf("zero-state generation's leaf %q, want the flat %q", g1.leaf, flat)
	}
	want := []string{"add " + flat + " mem=2147483648 pids=512 weight=0"}
	if got := cg.takeLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("zero-state join:\n got %q\nwant %q", got, want)
	}

	// A record alone (LimitsFor installed, main alone) keeps the flat leaf.
	limits := map[string]cgroup.Limits{"main": tileCaps, "dev": {MemMax: 256 << 20, PidsMax: 64}}
	h.r.LimitsFor = func(tile, dep string) cgroup.Limits { return limits[dep] }
	h.r.Changed(h.c)
	waitUntil(t, "g2 healthy", func() bool { return statusOf(h.r, "apps/x") == "healthy g2" })
	if l := curOf(t, h.r, "apps/x", "main").leaf; l != flat {
		t.Errorf("main-only tile with a record: leaf %q, want the flat %q", l, flat)
	}
	waitUntil(t, "g1's exit", func() bool { return cg.logged("remove " + flat) })
	want = []string{"add " + flat + " mem=2147483648 pids=512 weight=100", "remove " + flat}
	if got := cg.takeLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("main-only swap:\n got %q\nwant %q", got, want)
	}
	if !cg.has(flat) {
		t.Error("g1's exit removed the flat leaf g2 is in")
	}

	// dev, the tile's first non-main deployment: its own leaf under the
	// parent, its own caps, the non-primary weight.
	h.mu.Lock()
	h.code["dev"] = Code{WorkTree: true}
	h.mu.Unlock()
	devLeaf := h.r.chooseLeaf("apps/x", "dev")
	if devLeaf != leafXdev {
		t.Fatalf("dev's leaf %q, want %q", devLeaf, leafXdev)
	}
	h.r.joinLeaf("apps/x", "dev", devLeaf, "no-vm.sock", fakePid)
	want = []string{"add " + leafXdev + " mem=268435456 pids=64 weight=50"}
	if got := cg.takeLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("dev's join:\n got %q\nwant %q", got, want)
	}

	// main moves in at its next generation; the old one drains flat.
	h.r.Changed(h.c)
	waitUntil(t, "g3 healthy", func() bool { return statusOf(h.r, "apps/x") == "healthy g3" })
	if l := curOf(t, h.r, "apps/x", "main").leaf; l != leafXmn {
		t.Errorf("main's generation after dev started: leaf %q, want %q", l, leafXmn)
	}
	waitUntil(t, "g2's exit", func() bool { return !cg.has(flat) })
	want = []string{"add " + leafXmn + " mem=2147483648 pids=512 weight=100", "remove " + flat}
	if got := cg.takeLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("main's move:\n got %q\nwant %q", got, want)
	}

	// dev exits: its leaf alone goes; main's next generation stays nested.
	cg.exit(devLeaf, fakePid)
	h.r.leaveLeaf(devLeaf)
	if cg.has(leafXdev) || !cg.has(leafXmn) {
		t.Errorf("dev's exit: dev leaf left %v, main's leaf kept %v", cg.has(leafXdev), cg.has(leafXmn))
	}
	if got := h.r.chooseLeaf("apps/x", "main"); got != leafXmn {
		t.Errorf("main with the parent still held: %q, want %q", got, leafXmn)
	}

	// main stops: the parent empties, and main starts flat again.
	h.r.Stop("apps/x")
	waitUntil(t, "main's nested leaf removed", func() bool { return !cg.has(leafXmn) })
	if got := h.r.chooseLeaf("apps/x", "main"); got != flat {
		t.Errorf("main once the parent emptied: %q, want the flat %q", got, flat)
	}

	// A VM generation's leaf holds two guests, under its deployment's caps.
	h.r.vms.mu.Lock()
	h.r.vms.res = map[string]vmRes{"vm.sock": {release: func() {}, leafMiB: 704, memMiB: 512}}
	h.r.vms.mu.Unlock()
	h.r.joinLeaf("apps/x", "dev", leafXdev, "vm.sock", fakePid+1)
	want = []string{"remove " + leafXdev, "remove " + leafXmn, "addmem " + leafXdev + " max=1476395008 pids=64 weight=50"}
	if got := cg.takeLog(); !reflect.DeepEqual(got, want) {
		t.Errorf("a VM join:\n got %q\nwant %q", got, want)
	}
}

// covers P5 P25 T10 — TestLimitAlertsNestedLeaves (15-test-plan §3.2):
// AtLimitTile checks the flat leaf of a zero-state or main-only tile
// exactly as Cgroup.AtLimit(CompKey) does, keyed by that leaf and naming no
// deployment; a tile running non-main deployments answers every leaf, the
// flat one while main's old generation drains there, each naming its
// deployment; without cgroup accounting or leaves it answers nothing.
func TestLimitAlertsNestedLeaves(t *testing.T) {
	r := &Runner{}
	if hits := r.AtLimitTile("apps/x"); hits != nil {
		t.Errorf("without cgroup: %+v", hits)
	}
	cg := newFakeCgroup(tileCaps)
	r.cgOps = cg
	if hits := r.AtLimitTile("apps/x"); hits != nil {
		t.Errorf("a tile without a leaf: %+v", hits)
	}

	cg.join("add", ckX, fakePid, tileCaps, 0)
	cg.hits[ckX] = [2]int64{3, 1}
	mem, pids, _ := cg.AtLimit(ckX)
	want := []LimitHit{{Leaf: ckX, Mem: mem, Pids: pids}}
	if hits := r.AtLimitTile("apps/x"); !reflect.DeepEqual(hits, want) {
		t.Errorf("the flat leaf: %+v, want %+v", hits, want)
	}

	cg.join("add", leafXdev, fakePid+1, tileCaps, 0)
	cg.join("add", leafXmn, fakePid+2, tileCaps, 0)
	cg.hits[leafXdev] = [2]int64{1, 0}
	cg.hits[leafXmn] = [2]int64{0, 2}
	want = []LimitHit{
		{Leaf: ckX, Mem: 3, Pids: 1},
		{Deployment: "dev", Leaf: leafXdev, Mem: 1},
		{Deployment: "main", Leaf: leafXmn, Pids: 2},
	}
	if hits := r.AtLimitTile("apps/x"); !reflect.DeepEqual(hits, want) {
		t.Errorf("nested leaves, main draining flat:\n got %+v\nwant %+v", hits, want)
	}

	cg.exit(ckX, fakePid)
	cg.Remove(ckX)
	if hits := r.AtLimitTile("apps/x"); len(hits) != 2 || hits[0].Deployment != "dev" || hits[1].Deployment != "main" {
		t.Errorf("after the drain: %+v, want dev's and main's leaves", hits)
	}
}

// covers P25 T10 — TestStatsCountEveryDeployment: a tile's stats are its
// whole tree's, its flat leaf and every leaf beneath its per-tile parent
// (TileProcs, TileUsage), with the per-tile totals of 07-runtime §10.3; a
// non-main deployment's backend row in the sandbox registry is sampled by
// its own leaf, and main's backend rows never by theirs (by their tile, as
// ever); without cgroups the tile sums every deployment's current
// generation's process tree, the primary's first.
func TestStatsCountEveryDeployment(t *testing.T) {
	f, _ := newDepFake(t, "apps/x")
	f.set("apps/x", "dev", "worktree")
	f.ensure("apps/x", "main")
	f.ensure("apps/x", "dev")
	f.settle()

	cg := newFakeCgroup(tileCaps)
	f.r.cgOps = cg
	me := os.Getpid()
	cg.join("add", ckX, me, tileCaps, 0)
	cg.join("add", leafXdev, me, tileCaps, 0)
	cg.usage[ckX] = cgroup.Usage{MemCurrent: 100 << 20, PidsCurrent: 3}
	cg.usage[leafXdev] = cgroup.Usage{MemCurrent: 30 << 20, PidsCurrent: 2}
	defer f.r.Sandboxes.Add(sbx.Entry{ID: "backend:" + ckX + ":g1", Kind: sbx.Backend, Tile: "apps/x", PID: me, Leaf: ckX})()
	defer f.r.Sandboxes.Add(sbx.Entry{ID: "backend+dev:" + ckX + ":g1", Kind: sbx.Backend, Tile: "apps/x", Deployment: "dev", PID: me, Leaf: leafXdev})()

	f.r.statsSample()
	f.r.stats.mu.Lock()
	tile := f.r.stats.series["apps/x"]
	devRow := f.r.stats.series[sandboxKey("backend+dev:"+ckX+":g1")]
	_, mainRow := f.r.stats.series[sandboxKey("backend:"+ckX+":g1")]
	f.r.stats.mu.Unlock()
	if len(tile) != 1 || tile[0].Mem != 130<<20 || tile[0].Pids != 5 {
		t.Errorf("the tile's point %+v, want its flat and nested leaves' totals (130 MiB, 5 pids)", tile)
	}
	if len(devRow) != 1 || devRow[0].Mem != 30<<20 || devRow[0].Pids != 2 {
		t.Errorf("dev's backend row %+v, want its own leaf's (30 MiB, 2 pids)", devRow)
	}
	if mainRow {
		t.Error("main's backend row is sampled by its entry; it is its tile's")
	}

	// Without cgroups: every deployment's current generation, the primary's first.
	for _, dep := range []string{"main", "dev"} {
		inst := curOf(t, f.r, "apps/x", dep)
		inst.cmd = &exec.Cmd{Process: &os.Process{Pid: map[string]int{"main": me, "dev": 1}[dep]}}
	}
	if got := f.r.backendPids()["apps/x"]; !reflect.DeepEqual(got, []int{me, 1}) {
		t.Errorf("backendPids = %v, want the primary's then dev's", got)
	}
}
