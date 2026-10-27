//go:build linux

package tilesbx

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/xbin-dev/xbin/internal/limits"
)

// capTape records what Deps.AtCap was told.
type capTape struct {
	mu   sync.Mutex
	hits []string // "<tile> <kind> <value> <detail>"
}

func (c *capTape) atCap(tile string, k limits.Kind, v int64, detail string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hits = append(c.hits, fmt.Sprintf("%s %s %d %s", tile, k, v, detail))
}

func (c *capTape) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.hits
	c.hits = nil
	return out
}

// grantTable is tile → its grant (memMiB, vcpus, diskGiB).
type grantTable struct {
	mu sync.Mutex
	m  map[string][3]int
}

func (g *grantTable) set(tile string, v [3]int) { g.mu.Lock(); g.m[tile] = v; g.mu.Unlock() }

// grantDeps wires a grant table and a tape into a fake runtime's Deps.
func grantDeps(grants *grantTable, tape *capTape) func(*Options) {
	return func(o *Options) {
		o.Deps.Granted = func(tile string) (int, int, int) {
			grants.mu.Lock()
			defer grants.mu.Unlock()
			g := grants.m[tile]
			return g[0], g[1], g[2]
		}
		o.Deps.AtCap = tape.atCap
	}
}

// covers D203 — a tile's grants raise its sandbox totals (perTile) and one
// sandbox's maximum (perSandbox max*) above the policy and its overrides,
// as a layer: the policy file is untouched, a grant below changes nothing,
// another tile keeps the policy's; the runtime's limits show the raise,
// and the next admission takes it.
func TestGrantedRaisesLimits(t *testing.T) {
	grants := &grantTable{m: map[string][3]int{}}
	fe := newFakeEnv(t, grantDeps(grants, &capTape{}))
	fe.putPolicy(`{"overrides":{"apps/mgr":{"perTile":{"max":16,"running":16,"memMiB":3072}}}}`, http.StatusOK)
	lim := func(tile string) Limits {
		fe.m.mu.Lock()
		defer fe.m.mu.Unlock()
		return fe.m.limitsFor(tile)
	}
	if l := lim("apps/mgr"); l.PerTile.MemMiB != 3072 || l.PerSandbox.MaxMemMiB != 8192 {
		t.Fatalf("no grant: %+v", l)
	}
	grants.set("apps/mgr", [3]int{12288, 12, 150})
	l := lim("apps/mgr")
	if l.PerTile.MemMiB != 12288 || l.PerSandbox.MaxMemMiB != 12288 || l.PerTile.VCPUs != 12 || l.PerSandbox.MaxVCPUs != 12 ||
		l.PerTile.DiskGiB != 150 || l.PerSandbox.MaxDiskGiB != 200 || l.PerTile.Running != 16 {
		t.Errorf("granted: %+v", l)
	}
	if o := fe.m.Policy().Overrides["apps/mgr"]; o == nil || o.PerTile.MemMiB != 3072 {
		t.Errorf("the grant reached the policy: %+v", o)
	}
	if l := lim("apps/other"); l.PerTile.MemMiB != 8192 || l.PerTile.VCPUs != 8 {
		t.Errorf("another tile: %+v", l)
	}
	grants.set("apps/mgr", [3]int{1024, 1, 1}) // below: nothing
	if l := lim("apps/mgr"); l.PerTile.MemMiB != 3072 || l.PerTile.VCPUs != 8 || l.PerTile.DiskGiB != 100 {
		t.Errorf("a grant below the policy: %+v", l)
	}
	fe.m.mu.Lock()
	rt := fe.m.runtime(fe.k)
	fe.m.mu.Unlock()
	if rt.Limits.MemMiB != 3072 {
		t.Errorf("the runtime's limits: %+v", rt.Limits)
	}

	// the admission: three 1 GiB sandboxes fill 3 GiB; granted 4 GiB, a fourth
	var names []string
	for i := 0; i < 4; i++ {
		body := ns(fmt.Sprintf("sb-%d", i))
		body["memMiB"] = 1024
		fe.create(body)
		names = append(names, body["name"].(string))
	}
	if ran, _ := fe.startAll(names); len(ran) != 3 {
		t.Fatalf("under 3 GiB: ran %v", ran)
	}
	grants.set("apps/mgr", [3]int{4096, 0, 0})
	if _, refused := fe.startAll(names); len(refused) != 0 {
		t.Fatalf("granted 4 GiB: refused %v", refused)
	}
	for _, n := range names {
		if fe.runOf(n) == nil {
			t.Errorf("granted 4 GiB: %s isn't running", n)
		}
	}
	fe.m.mu.Lock()
	rt = fe.m.runtime(fe.k)
	fe.m.mu.Unlock()
	if rt.Limits.MemMiB != 4096 || rt.Limits.PerSandbox.MaxMemMiB != 8192 {
		t.Errorf("the runtime's limits under the grant: %+v", rt.Limits)
	}
}

// covers D203 — a start the admission refuses at one of the tile's
// totals is a hit at that cap (memory, vCPUs, disk), told once the book is
// let go; a refusal at the running count (no kind) or the workspace's
// total isn't the tile's to raise.
func TestAtCapFromAdmission(t *testing.T) {
	tape := &capTape{}
	fe := newFakeEnv(t, grantDeps(&grantTable{m: map[string][3]int{}}, tape))
	fe.putPolicy(`{"overrides":{"apps/mgr":{"perTile":{"max":16,"running":2,"memMiB":2048,"vcpus":3}}}}`, http.StatusOK)
	mk := func(name string, mem, cpus int) {
		body := ns(name)
		body["memMiB"], body["vcpus"] = mem, cpus
		fe.create(body)
	}
	mk("a", 1024, 2)
	mk("b", 1536, 1)
	mk("c", 512, 2)
	mk("d", 256, 1)
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/b/start", nil), http.StatusTooManyRequests, RefLimit)
	fe.want(fe.do(mgr, "POST", "/sandboxes/c/start", nil), http.StatusTooManyRequests, RefLimit)
	want := []string{
		"apps/mgr sbxMemMiB 2048 a 1536 MiB sandbox's start refused: 1024 MiB held",
		"apps/mgr sbxVCPUs 3 a 2-vCPU sandbox's start refused: 2 vCPUs held",
	}
	if got := tape.take(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("hits:\n got %q\nwant %q", got, want)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/d/start", nil), http.StatusOK, "")
	mk("e", 256, 0)
	fe.want(fe.do(mgr, "POST", "/sandboxes/e/start", nil), http.StatusTooManyRequests, RefLimit) // running 2
	if got := tape.take(); len(got) != 0 {
		t.Errorf("a refusal at the running count: %q", got)
	}

	fe.m.StopAll("the test")
	fe.putPolicy(`{"perTile":{"diskGiB":1}}`, http.StatusOK)
	fe.m.mu.Lock()
	fe.m.boxLocked(fe.k, "a").diskBytes = 2 << 30
	fe.m.mu.Unlock()
	fe.want(fe.do(mgr, "POST", "/sandboxes/a/start", nil), http.StatusTooManyRequests, RefLimit)
	if got := tape.take(); len(got) != 1 || got[0] != "apps/mgr sbxDiskGiB 1 a sandbox's start refused: the tile's sandboxes use 2.0 GiB of disk" {
		t.Errorf("the disk cap: %q", got)
	}
}

// covers D203 — a sandbox the usage worker stops over the tile's disk
// total is a hit at sbxDiskGiB; one the OOM killer took (read from its
// leaf before the leaf goes) is a hit at sbxMemMiB, naming it, its size
// and the kills — unless a stop was asked of it.
func TestAtCapStopsAndOOM(t *testing.T) {
	tape := &capTape{}
	fe := newFakeEnv(t, grantDeps(&grantTable{m: map[string][3]int{"apps/mgr": {10240, 0, 0}}}, tape))
	fe.create(ns("sb-1"))
	fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
	fe.waitState("sb-1", StateRunning)
	fe.putPolicy(`{"perTile":{"diskGiB":1}}`, http.StatusOK)
	fe.m.mu.Lock()
	fe.m.boxLocked(fe.k, "sb-1").diskBytes = 3 << 30
	fe.m.mu.Unlock()
	fe.m.overDiskStop("apps/mgr")
	if in := fe.waitState("sb-1", StateStopped); !strings.Contains(in.StateDetail, "perTile.diskGiB") {
		t.Fatalf("the over-disk stop: %+v", in)
	}
	if got := tape.take(); len(got) != 1 || got[0] != "apps/mgr sbxDiskGiB 1 sandbox sb-1 stopped: the tile's sandboxes use 3.0 GiB" {
		t.Errorf("the over-disk hit: %q", got)
	}

	fe.putPolicy(`{"perTile":{"diskGiB":100}}`, http.StatusOK)
	fe.m.oomKills = func(string) int64 { return 2 }
	for _, asked := range []bool{false, true} {
		fe.want(fe.do(mgr, "POST", "/sandboxes/sb-1/start", nil), http.StatusOK, "")
		r := fe.runOf("sb-1")
		r.leaf = "sbx-test" // as a cgroup parent places it
		if asked {
			_ = fe.m.Stop(fe.k, "sb-1", "")
		} else {
			fe.l.last().die(ExitStatus{Code: -1, Signal: syscall.SIGKILL})
		}
		fe.waitState("sb-1", StateStopped)
		<-r.done
		got := tape.take()
		switch {
		case asked && len(got) != 0:
			t.Errorf("an asked stop with OOM kills is a hit: %q", got)
		case !asked && (len(got) != 1 || got[0] != "apps/mgr sbxMemMiB 10240 sandbox sb-1 (2048 MiB) was OOM-killed (2 processes)"):
			t.Errorf("the OOM hit: %q", got)
		}
	}
}
