package cgroup

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeCgroupfs is a Manager over a plain directory that keeps the cgroup v2
// rules the per-tile layout depends on, so a wrong order fails as it would on
// the kernel: a controller's files exist in a cgroup only once its parent
// enables that controller in cgroup.subtree_control; a cgroup that enables
// controllers for its children can't take a process, nor one with processes
// enable them (no internal processes); cgroup.procs gains one pid per write;
// and rmdir refuses a cgroup with children or processes. Every write is
// logged, relative to the base.
type fakeCgroupfs struct {
	base   string
	writes []string // "<rel path>=<value>", in order
}

func newFakeCgroupfs(t *testing.T, caps Limits) (*Manager, *fakeCgroupfs) {
	t.Helper()
	f := &fakeCgroupfs{base: t.TempDir()}
	// xbind's base, as New leaves it: controllers on for its children.
	if err := os.WriteFile(filepath.Join(f.base, "cgroup.subtree_control"), []byte("cpu memory pids"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{base: f.base, enabled: true, limits: caps, write: f.write, rmdir: f.rmdir}
	return m, f
}

func (f *fakeCgroupfs) write(p, v string) error {
	rel, _ := filepath.Rel(f.base, p)
	f.writes = append(f.writes, filepath.ToSlash(rel)+"="+v)
	dir, file := filepath.Dir(p), filepath.Base(p)
	if _, err := os.Stat(dir); err != nil {
		return err
	}
	switch {
	case file == "cgroup.subtree_control":
		if len(pidsIn(dir)) > 0 {
			return syscall.EBUSY
		}
		on := strings.Fields(readStr(p))
		for _, tok := range strings.Fields(v) {
			c := strings.TrimPrefix(tok, "+")
			if !slices.Contains(strings.Fields(readStr(filepath.Join(filepath.Dir(dir), "cgroup.subtree_control"))), c) {
				return syscall.ENOENT // the whole line is refused
			}
			if !slices.Contains(on, c) {
				on = append(on, c)
			}
		}
		return os.WriteFile(p, []byte(strings.Join(on, " ")), 0o644)
	case file == "cgroup.procs":
		if strings.TrimSpace(readStr(filepath.Join(dir, "cgroup.subtree_control"))) != "" {
			return syscall.EBUSY
		}
		return os.WriteFile(p, []byte(readStr(p)+v+"\n"), 0o644)
	case strings.HasPrefix(file, "memory."), strings.HasPrefix(file, "pids."), strings.HasPrefix(file, "cpu."):
		ctrl, _, _ := strings.Cut(file, ".")
		if !slices.Contains(strings.Fields(readStr(filepath.Join(filepath.Dir(dir), "cgroup.subtree_control"))), ctrl) {
			return syscall.ENOENT
		}
	}
	return os.WriteFile(p, []byte(v), 0o644)
}

func (f *fakeCgroupfs) rmdir(p string) error {
	ents, err := os.ReadDir(p)
	if err != nil {
		return err
	}
	for _, e := range ents {
		if e.IsDir() {
			return syscall.EBUSY
		}
	}
	if len(pidsIn(p)) > 0 {
		return syscall.EBUSY
	}
	return os.RemoveAll(p)
}

// exit is the kernel dropping an exited process from its cgroup.
func (f *fakeCgroupfs) exit(t *testing.T, name string, pid int) {
	t.Helper()
	p := filepath.Join(f.dir(name), "cgroup.procs")
	var keep []string
	for _, n := range pidsIn(filepath.Dir(p)) {
		if n != pid {
			keep = append(keep, strconv.Itoa(n)+"\n")
		}
	}
	if err := os.WriteFile(p, []byte(strings.Join(keep, "")), 0o644); err != nil {
		t.Fatal(err)
	}
}

// dir is name's directory by the name rule.
func (f *fakeCgroupfs) dir(name string) string {
	if !strings.Contains(name, "/") {
		return filepath.Join(f.base, "comp-"+name)
	}
	return filepath.Join(f.base, strings.TrimSuffix(name, "/"))
}

// file reads one of name's files ("" when absent).
func (f *fakeCgroupfs) file(name, file string) string {
	return strings.TrimSpace(readStr(filepath.Join(f.dir(name), file)))
}

// set writes a counter the kernel keeps (the manager never writes these).
func (f *fakeCgroupfs) set(t *testing.T, name, file, val string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.dir(name), file), []byte(val), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (f *fakeCgroupfs) exists(name string) bool { return isDir(f.dir(name)) }

// topLevel lists the base's cgroups.
func (f *fakeCgroupfs) topLevel(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(f.base)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// writesUnder is the log's writes into name's directory (not beneath it).
func (f *fakeCgroupfs) writesUnder(name string) []string {
	rel, _ := filepath.Rel(f.base, f.dir(name))
	var out []string
	for _, w := range f.writes {
		if filepath.Dir(strings.SplitN(w, "=", 2)[0]) == filepath.ToSlash(rel) {
			out = append(out, w)
		}
	}
	return out
}

// firstWrite is the log index of the first write with prefix ("" matches
// nothing: -1).
func (f *fakeCgroupfs) firstWrite(prefix string) int {
	for i, w := range f.writes {
		if strings.HasPrefix(w, prefix) {
			return i
		}
	}
	return -1
}

func pidsIn(dir string) []int { return parsePids(readStr(filepath.Join(dir, "cgroup.procs"))) }

func sorted(p []int) []int { slices.Sort(p); return p }

// covers T10 T18 — the per-tile cgroup parent (07-runtime §10.3), on a
// directory that keeps cgroupfs's rules: a tile keeps its flat leaf while
// main runs alone, and readers create nothing; its first non-main deployment
// makes tile-<CompKey>/d-<name>/backend, each parent enabling its children's
// controllers before anything is made beneath it, the tile weighted as one
// flat leaf and the deployment node by its role, no memory or pids cap above
// the leaves, and no process in any node; main moves in at its next
// generation while the old one drains in the flat leaf, and the tile readers
// (TileProcs, TileUsage, TileLeaves, ProcsTree, hierarchical Usage and
// AtLimit on TileNode) see both; removing one leaf never removes another,
// nor a leaf a later generation joined, and the last one takes its empty
// parents, never the base; the boot sweep drops empty tile-* nodes and keeps
// live ones; malformed nested names touch nothing.
func TestNonPrimaryCgroupSubtree(t *testing.T) {
	caps := Limits{MemMax: 2 << 30, PidsMax: 512, CPUWeight: 100}
	const key = "apps~crm-1a2b3c4d"
	tile := "tile-" + key
	devLeaf, mainLeaf := DeploymentLeaf(key, "dev"), DeploymentLeaf(key, "main")

	t.Run("layout", func(t *testing.T) {
		m, f := newFakeCgroupfs(t, caps)

		// main alone: today's flat leaf, and the tile readers are its readers.
		m.Add(key, 101)
		f.set(t, key, "memory.current", "1000")
		if got := f.topLevel(t); !reflect.DeepEqual(got, []string{"comp-" + key}) {
			t.Fatalf("main alone made %v; want the flat leaf alone", got)
		}
		if got := m.TileLeaves(key); !reflect.DeepEqual(got, []Leaf{{Name: key}}) {
			t.Errorf("TileLeaves (flat) = %+v", got)
		}
		flatProcs, _ := m.Procs(key)
		if got, ok := m.TileProcs(key); !ok || !reflect.DeepEqual(got, flatProcs) {
			t.Errorf("TileProcs (flat) = %v %v; Procs = %v", got, ok, flatProcs)
		}
		flatUse, _ := m.Usage(key)
		if got, ok := m.TileUsage(key); !ok || got != flatUse {
			t.Errorf("TileUsage (flat) = %+v %v; Usage = %+v", got, ok, flatUse)
		}
		if got, ok := m.ProcsTree(key); !ok || !reflect.DeepEqual(got, []int{101}) {
			t.Errorf("ProcsTree of the flat leaf = %v %v; it is Procs", got, ok)
		}
		if f.exists(TileNode(key)) {
			t.Fatal("a reader made the per-tile parent")
		}

		// The first non-main deployment starts in the nested layout.
		dev := caps
		dev.MemMax, dev.NodeWeight = 512<<20, DeploymentWeight(false)
		m.AddWith(devLeaf, 201, dev)
		for _, step := range [][2]string{
			{tile + "/cgroup.subtree_control", tile + "/d-dev/"},
			{tile + "/d-dev/cgroup.subtree_control", tile + "/d-dev/backend/"},
		} {
			if a, b := f.firstWrite(step[0]), f.firstWrite(step[1]); a < 0 || b < 0 || a > b {
				t.Errorf("%s (write %d) must come before anything under %s (write %d): %v", step[0], a, step[1], b, f.writes)
			}
		}
		if w := f.writesUnder(devLeaf); len(w) == 0 || w[len(w)-1] != tile+"/d-dev/backend/cgroup.procs=201" {
			t.Errorf("the pid must join after the caps: %v", w)
		}
		for name, want := range map[string]map[string]string{
			TileNode(key):              {"cpu.weight": "100", "cgroup.subtree_control": "cpu memory pids"},
			DeploymentNode(key, "dev"): {"cpu.weight": "50", "cgroup.subtree_control": "cpu memory pids"},
			devLeaf: {"memory.max": "536870912", "memory.high": "469762048", "pids.max": "512",
				"cpu.weight": "100", "cgroup.procs": "201", "cgroup.subtree_control": ""},
		} {
			for file, v := range want {
				if got := f.file(name, file); got != v {
					t.Errorf("%s %s = %q; want %q", name, file, got, v)
				}
			}
		}
		for _, node := range []string{TileNode(key), DeploymentNode(key, "dev")} {
			for _, file := range []string{"memory.max", "memory.high", "pids.max", "cgroup.procs"} {
				if got := f.file(node, file); got != "" {
					t.Errorf("%s holds %s = %q; nodes carry no caps and no process", node, file, got)
				}
			}
		}
		if got := pidsIn(f.dir(key)); !reflect.DeepEqual(got, []int{101}) {
			t.Errorf("the flat leaf changed: %v", got)
		}

		// main moves in at its next generation; the old one drains flat.
		main := caps
		main.NodeWeight = DeploymentWeight(true)
		m.AddWith(mainLeaf, 102, main)
		if got := f.file(DeploymentNode(key, "main"), "cpu.weight"); got != "100" {
			t.Errorf("the primary's node weight = %q", got)
		}
		wantLeaves := []Leaf{{Name: key}, {Name: devLeaf, Deployment: "dev"}, {Name: mainLeaf, Deployment: "main"}}
		if got := m.TileLeaves(key); !reflect.DeepEqual(got, wantLeaves) {
			t.Errorf("TileLeaves (draining) = %+v; want %+v", got, wantLeaves)
		}
		if got, ok := m.TileProcs(key); !ok || !reflect.DeepEqual(sorted(got), []int{101, 102, 201}) {
			t.Errorf("TileProcs (draining) = %v %v", got, ok)
		}
		if got, ok := m.ProcsTree(TileNode(key)); !ok || !reflect.DeepEqual(sorted(got), []int{102, 201}) {
			t.Errorf("ProcsTree(tile node) = %v %v", got, ok)
		}
		if got, _ := m.Procs(TileNode(key)); len(got) != 0 {
			t.Errorf("the tile node holds processes: %v", got)
		}
		// The kernel keeps a node's counters hierarchical; the manager reads them.
		f.set(t, key, "pids.current", "1")
		f.set(t, key, "cpu.stat", "usage_usec 70\nuser_usec 50\n")
		f.set(t, TileNode(key), "memory.current", "3000")
		f.set(t, TileNode(key), "pids.current", "2")
		f.set(t, TileNode(key), "cpu.stat", "usage_usec 500\n")
		f.set(t, TileNode(key), "memory.events", "low 0\nhigh 4\nmax 2\noom 1\noom_kill 1\n")
		f.set(t, TileNode(key), "pids.events", "max 5\n")
		if got, ok := m.Usage(TileNode(key)); !ok || got != (Usage{MemCurrent: 3000, MemMax: -1, CPUUsec: 500, PidsCurrent: 2}) {
			t.Errorf("Usage(tile node) = %+v %v", got, ok)
		}
		if got, ok := m.TileUsage(key); !ok || got != (Usage{MemCurrent: 4000, MemMax: -1, CPUUsec: 570, PidsCurrent: 3}) {
			t.Errorf("TileUsage (draining) = %+v %v; want the flat leaf plus the parent", got, ok)
		}
		if mem, pids, ok := m.AtLimit(TileNode(key)); !ok || mem != 3 || pids != 5 {
			t.Errorf("AtLimit(tile node) = %d %d %v", mem, pids, ok)
		}
		if got, ok := m.Usage(devLeaf); !ok || got.MemMax != 512<<20 {
			t.Errorf("Usage(dev leaf) = %+v %v", got, ok)
		}

		// The old main generation exits: the flat leaf goes, the tree stays.
		f.exit(t, key, 101)
		m.Remove(key)
		if f.exists(key) || !f.exists(devLeaf) || !f.exists(mainLeaf) {
			t.Fatalf("after the drain: %v, tree %v", f.topLevel(t), m.TileLeaves(key))
		}
		if got, _ := m.TileUsage(key); got != (Usage{MemCurrent: 3000, MemMax: -1, CPUUsec: 500, PidsCurrent: 2}) {
			t.Errorf("TileUsage (nested) = %+v; want the parent's", got)
		}

		// Blue/green: a later generation joined, so the leaf stays.
		m.AddWith(devLeaf, 202, dev)
		f.exit(t, devLeaf, 201)
		m.Remove(devLeaf)
		if got := pidsIn(f.dir(devLeaf)); !reflect.DeepEqual(got, []int{202}) {
			t.Fatalf("an exit removed a leaf another generation had joined: %v", got)
		}
		// dev's last generation exits: its leaf and node go, main's stay.
		f.exit(t, devLeaf, 202)
		m.Remove(devLeaf)
		if f.exists(DeploymentNode(key, "dev")) {
			t.Error("dev's empty node was kept")
		}
		if got := pidsIn(f.dir(mainLeaf)); !reflect.DeepEqual(got, []int{102}) || !f.exists(TileNode(key)) {
			t.Fatalf("removing dev's leaf touched main's: %v", got)
		}
		m.Remove(devLeaf) // a second Remove is harmless
		if !f.exists(mainLeaf) {
			t.Fatal("a repeated Remove took another deployment's leaf")
		}
		// main's last exit takes the whole tree, never the base.
		f.exit(t, mainLeaf, 102)
		m.Remove(mainLeaf)
		if got := f.topLevel(t); len(got) != 0 {
			t.Errorf("after the last exit: %v", got)
		}
		if !isDir(f.base) {
			t.Fatal("Remove took the base")
		}
		if got := m.TileLeaves(key); got != nil {
			t.Errorf("TileLeaves (gone) = %+v", got)
		}
		if _, ok := m.TileUsage(key); ok {
			t.Error("TileUsage of a tile with no cgroup")
		}
		if _, ok := m.TileProcs(key); ok {
			t.Error("TileProcs of a tile with no cgroup")
		}
	})

	t.Run("vm", func(t *testing.T) {
		m, f := newFakeCgroupfs(t, caps)
		l := caps
		l.PidsMax, l.NodeWeight = 64, DeploymentWeight(false)
		m.AddMemWith(devLeaf, 301, l, 3<<30)
		for file, v := range map[string]string{"memory.max": "3221225472", "memory.high": "", "pids.max": "64", "cgroup.procs": "301"} {
			if got := f.file(devLeaf, file); got != v {
				t.Errorf("VM leaf %s = %q; want %q", file, got, v)
			}
		}
		if got := f.file(DeploymentNode(key, "dev"), "cpu.weight"); got != "50" {
			t.Errorf("VM deployment node weight = %q", got)
		}
	})

	t.Run("an exit racing a start", func(t *testing.T) {
		// The old generation's Remove fires while the new one is between
		// its leaf's mkdir and its cgroup.procs write: it must wait, then
		// find the new pid and keep the leaf and its parents.
		m, f := newFakeCgroupfs(t, caps)
		removed := make(chan struct{})
		write := f.write
		m.write = func(p, v string) error {
			if strings.HasSuffix(p, "/d-dev/backend/memory.max") {
				go func() { m.Remove(devLeaf); close(removed) }()
				select {
				case <-removed:
				case <-time.After(50 * time.Millisecond):
				}
			}
			return write(p, v)
		}
		m.AddWith(devLeaf, 501, caps)
		<-removed
		if got := pidsIn(f.dir(devLeaf)); !reflect.DeepEqual(got, []int{501}) {
			t.Errorf("a concurrent exit removed a starting generation's leaf: %v", got)
		}
	})

	t.Run("controllers without cpu", func(t *testing.T) {
		m, f := newFakeCgroupfs(t, caps)
		// The base's controllers: cpu isn't delegated.
		if err := os.WriteFile(filepath.Join(f.base, "cgroup.subtree_control"), []byte("memory pids"), 0o644); err != nil {
			t.Fatal(err)
		}
		m.AddWith(devLeaf, 401, caps)
		if got := f.file(TileNode(key), "cgroup.subtree_control"); got != "memory pids" {
			t.Errorf("tile node controllers = %q", got)
		}
		if got := f.file(devLeaf, "memory.max"); got != "2147483648" {
			t.Errorf("without cpu the leaf lost its memory cap: %q", got)
		}
		if got := f.file(devLeaf, "cgroup.procs"); got != "401" {
			t.Errorf("leaf procs = %q", got)
		}
	})

	t.Run("sweep", func(t *testing.T) {
		m, f := newFakeCgroupfs(t, caps)
		m.AddWith(DeploymentLeaf("a-11111111", "dev"), 1, caps) // a crashed run's, empty now
		f.exit(t, DeploymentLeaf("a-11111111", "dev"), 1)
		m.AddWith(DeploymentLeaf("b-22222222", "main"), 7, caps) // still running
		m.AddWith(DeploymentLeaf("b-22222222", "old"), 2, caps)  // exited
		f.exit(t, DeploymentLeaf("b-22222222", "old"), 2)
		if err := os.Mkdir(filepath.Join(f.base, "tile-c-33333333"), 0o755); err != nil { // a bare node
			t.Fatal(err)
		}
		m.Add("x-44444444", 9) // a flat leaf is not the sweep's, nor is init
		f.exit(t, "x-44444444", 9)
		for _, d := range []string{"init", "comp-y-55555555"} {
			if err := os.Mkdir(filepath.Join(f.base, d), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(f.base, "tile-file"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		m.sweep()
		want := []string{"comp-x-44444444", "comp-y-55555555", "init", "tile-b-22222222"}
		if got := f.topLevel(t); !reflect.DeepEqual(got, want) {
			t.Errorf("after the sweep: %v; want %v", got, want)
		}
		if got := m.TileLeaves("b-22222222"); !reflect.DeepEqual(got, []Leaf{{Name: DeploymentLeaf("b-22222222", "main"), Deployment: "main"}}) {
			t.Errorf("the live tile's leaves: %+v", got)
		}
		if got := pidsIn(f.dir(DeploymentLeaf("b-22222222", "main"))); !reflect.DeepEqual(got, []int{7}) {
			t.Errorf("the live leaf: %v", got)
		}
	})

	t.Run("names", func(t *testing.T) {
		m, f := newFakeCgroupfs(t, caps)
		m.AddWith(mainLeaf, 5, caps)
		before := f.topLevel(t)
		for _, bad := range []string{
			tile + "/../escape/backend", "/" + tile + "/d-x/backend", tile + "//backend", tile + "/./backend",
			tile + "/d-x/..", "comp-" + key + "/x", "init/x", "tile-/x", "../x/y", tile + "/d-x//",
		} {
			m.AddWith(bad, 6, caps)
			m.AddMemWith(bad, 6, caps, 1<<30)
			m.Remove(bad)
			if _, ok := m.Usage(bad); ok {
				t.Errorf("Usage(%q) answered", bad)
			}
			if _, ok := m.ProcsTree(bad); ok {
				t.Errorf("ProcsTree(%q) answered", bad)
			}
			if _, ok := m.Procs(bad); ok {
				t.Errorf("Procs(%q) answered", bad)
			}
			if _, _, ok := m.AtLimit(bad); ok {
				t.Errorf("AtLimit(%q) answered", bad)
			}
		}
		if got := f.topLevel(t); !reflect.DeepEqual(got, before) {
			t.Errorf("malformed names changed the base: %v → %v", before, got)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(f.base), "escape")); !errors.Is(err, os.ErrNotExist) {
			t.Error("a name escaped the base")
		}
		// The tile node never takes a process.
		m.AddWith(TileNode(key), 6, caps)
		if got := f.file(TileNode(key), "cgroup.procs"); got != "" {
			t.Errorf("the tile node took a process: %q", got)
		}
		if got := pidsIn(f.dir(mainLeaf)); !reflect.DeepEqual(got, []int{5}) {
			t.Errorf("main's leaf: %v", got)
		}
		// A tile whose path starts "tile-" keeps a flat leaf of its own.
		m.Add("tile-x-66666666", 8)
		if !isDir(filepath.Join(f.base, "comp-tile-x-66666666")) {
			t.Error("a flat name starting tile- left the flat mapping")
		}
	})
}

// covers P25 T10 SC-PRIMARY-FIRST — the primary's leaf keeps its caps
// whatever its non-primary siblings use. A main-only tile's flat leaf gets
// exactly today's writes: the installed per-component caps, then the pid (a
// VM's with its guest cap instead of the memory pair). In the per-tile layout
// the primary's leaf carries the tile's caps unless a manager lowered them,
// no memory or pids cap sits above the leaves, and inside a tile weighted as
// one flat leaf the primary's node outweighs a non-primary's. A sibling that
// exhausts its own leaf shows in its own counters only, and neither its
// restarts nor its caps ever lower the primary's; a reassignment re-weights
// both nodes at their next generations.
func TestPrimaryLeafKeepsCaps(t *testing.T) {
	caps := Limits{MemMax: 2 << 30, PidsMax: 512, CPUWeight: 100}
	const key = "apps~crm-1a2b3c4d"
	mainLeaf, devLeaf := DeploymentLeaf(key, "main"), DeploymentLeaf(key, "dev")
	m, f := newFakeCgroupfs(t, caps)

	// Main alone, flat: today's writes, byte for byte (P5).
	m.Add(key, 100)
	flat := "comp-" + key + "/"
	if got, want := f.writesUnder(key), []string{
		flat + "memory.max=2147483648", flat + "memory.high=1879048192", flat + "pids.max=512",
		flat + "cpu.weight=100", flat + "cgroup.procs=100",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("flat Add wrote %v; want %v", got, want)
	}
	m.AddMem("vm-77777777", 110, 3<<30)
	vm := "comp-vm-77777777/"
	if got, want := f.writesUnder("vm-77777777"), []string{
		vm + "pids.max=512", vm + "cpu.weight=100", vm + "memory.max=3221225472", vm + "cgroup.procs=110",
	}; !reflect.DeepEqual(got, want) {
		t.Errorf("flat AddMem wrote %v; want %v", got, want)
	}

	// Nested: main at the tile's caps, dev lowered by a tile manager.
	main := caps
	main.NodeWeight = DeploymentWeight(true)
	dev := Limits{MemMax: 256 << 20, PidsMax: 64, CPUWeight: 100, NodeWeight: DeploymentWeight(false)}
	m.AddWith(mainLeaf, 101, main)
	m.AddWith(devLeaf, 201, dev)
	wantMain := map[string]string{"memory.max": "2147483648", "memory.high": "1879048192", "pids.max": "512", "cpu.weight": "100"}
	checkMain := func(when string, want map[string]string) {
		t.Helper()
		for file, v := range want {
			if got := f.file(mainLeaf, file); got != v {
				t.Errorf("%s: the primary's %s = %q; want %q", when, file, got, v)
			}
		}
	}
	checkMain("beside dev", wantMain)
	for _, node := range []string{TileNode(key), DeploymentNode(key, "main"), DeploymentNode(key, "dev")} {
		for _, file := range []string{"memory.max", "memory.high", "pids.max"} {
			if got := f.file(node, file); got != "" {
				t.Errorf("%s caps %s = %q: a cap above the leaves is shared", node, file, got)
			}
		}
	}
	tileW, mainW, devW := f.file(TileNode(key), "cpu.weight"), f.file(DeploymentNode(key, "main"), "cpu.weight"), f.file(DeploymentNode(key, "dev"), "cpu.weight")
	if tileW != strconv.FormatInt(caps.CPUWeight, 10) {
		t.Errorf("tile weight %s; want one flat leaf's %d", tileW, caps.CPUWeight)
	}
	if mw, _ := strconv.Atoi(mainW); mainW != "100" || devW != "50" || mw <= 50 {
		t.Errorf("node weights: primary %s, non-primary %s; want 100 over 50", mainW, devW)
	}

	// dev exhausts its leaf: the kernel counts it there alone.
	f.set(t, devLeaf, "memory.current", strconv.Itoa(256<<20))
	f.set(t, devLeaf, "memory.events", "low 0\nhigh 12\nmax 40\noom 3\noom_kill 3\n")
	f.set(t, devLeaf, "pids.events", "max 9\n")
	if mem, pids, ok := m.AtLimit(devLeaf); !ok || mem != 43 || pids != 9 {
		t.Errorf("AtLimit(dev) = %d %d %v", mem, pids, ok)
	}
	if mem, pids, ok := m.AtLimit(mainLeaf); !ok || mem != 0 || pids != 0 {
		t.Errorf("AtLimit(main) = %d %d %v: dev's hits leaked into main's leaf", mem, pids, ok)
	}
	// dev restarts, then main: main's caps are the tile's still.
	m.AddWith(devLeaf, 202, dev)
	m.AddWith(mainLeaf, 102, main)
	checkMain("after dev ran out", wantMain)
	if u, ok := m.Usage(mainLeaf); !ok || u.MemMax != caps.MemMax {
		t.Errorf("Usage(main) = %+v %v", u, ok)
	}
	if got := f.file(devLeaf, "memory.max"); got != strconv.Itoa(256<<20) {
		t.Errorf("dev's own cap = %q", got)
	}

	// A manager lowers main's limits: its next generation takes them, and
	// dev's leaf keeps its own.
	lowered := main
	lowered.MemMax, lowered.PidsMax = 1<<30, 256
	m.AddWith(mainLeaf, 103, lowered)
	checkMain("lowered by a manager", map[string]string{"memory.max": "1073741824", "memory.high": "939524096", "pids.max": "256"})
	if got := f.file(devLeaf, "pids.max"); got != "64" {
		t.Errorf("dev's pids cap = %q", got)
	}

	// dev becomes the primary: both nodes re-weighted at their next
	// generations (a reassignment restarts both primaries).
	dev.NodeWeight, main.NodeWeight = DeploymentWeight(true), DeploymentWeight(false)
	m.AddWith(devLeaf, 203, dev)
	m.AddWith(mainLeaf, 104, main)
	if got, want := [2]string{f.file(DeploymentNode(key, "dev"), "cpu.weight"), f.file(DeploymentNode(key, "main"), "cpu.weight")}, [2]string{"100", "50"}; got != want {
		t.Errorf("after reassignment: dev %s, main %s; want %v", got[0], got[1], want)
	}
	checkMain("as a non-primary", wantMain)
	// A leaf without a node weight (a tile sandbox later) leaves its node's.
	m.AddWith(DeploymentNode(key, "dev")+"/sbx-a", 301, Limits{MemMax: 128 << 20})
	if got := f.file(DeploymentNode(key, "dev"), "cpu.weight"); got != "100" {
		t.Errorf("a leaf with no NodeWeight re-weighted its node: %s", got)
	}
}
