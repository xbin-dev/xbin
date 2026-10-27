package runner

// covers D203 — the runtime half of tile limits (plans/tile-limits.md §5,
// §6): an exit reads its leaf's OOM kills before the leaf may go, tells
// them once (blue/green's shared leaf, the alerts' poll), names them in
// the backend log and in both crash watches' breaker words; ApplyLimits
// raises live leaves (never a VM's, never down) and restarts what the
// breaker stopped; a VM grant raises the guest's ceiling. Over fakeCgroup
// (leaf_deploy_test.go), hostWorld's real processes on the host, and the
// deploy rig's fake engine.

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/vm"
)

// oomTape records what OnOOM was told.
type oomTape struct {
	mu  sync.Mutex
	evs []OOMEvent
}

func (o *oomTape) on(e OOMEvent) { o.mu.Lock(); o.evs = append(o.evs, e); o.mu.Unlock() }

func (o *oomTape) take() []OOMEvent {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := o.evs
	o.evs = nil
	return out
}

const oomWords = "; it was OOM-killed at its 2 GiB memory limit — raise it (the tile's ⚙ → Limits, /xbin/limits, `bx limits raise apps/x memory`)"

// covers D203 — noteExit reads oom_kill before Remove (the counter goes with
// the leaf), writes the backend log's line, marks the leaf for the breaker,
// tells OnOOM (tile, deployment, partition, generation, kills, the leaf's
// cap) and forgets a removed leaf's count; no kills, no line and no event.
func TestNoteExitReadsOOMBeforeRemove(t *testing.T) {
	cg := newFakeCgroup(tileCaps)
	var tape oomTape
	r := &Runner{cgOps: cg, LimitHooks: LimitHooks{OnOOM: tape.on}}
	c := &registry.Component{Path: "apps/x"}

	r.joinLeaf("apps/x", "main", ckX, "a.sock", fakePid)
	cg.mu.Lock()
	cg.order, cg.kills[ckX], cg.usage[ckX] = nil, 2, cgroup.Usage{MemMax: 2 << 30}
	cg.mu.Unlock()
	cg.exit(ckX, fakePid)
	var log bytes.Buffer
	inst := &instance{gen: 4, leaf: ckX, sock: "a.sock"}
	r.noteExit(c, "main", inst, &log)

	if want := []string{"oom " + ckX, "remove " + ckX}; !reflect.DeepEqual(cg.order, want) {
		t.Errorf("the exit's cgroup calls %q, want the OOM kills read before the leaf goes: %q", cg.order, want)
	}
	if got := log.String(); got != "--- gen 4: OOM-killed at memory.max 2 GiB ---\n" {
		t.Errorf("the backend log: %q", got)
	}
	want := []OOMEvent{{Tile: "apps/x", Deployment: "main", Gen: 4, Kills: 2, LimitMiB: 2048}}
	if got := tape.take(); !reflect.DeepEqual(got, want) {
		t.Errorf("OnOOM: %+v, want %+v", got, want)
	}
	if cg.has(ckX) {
		t.Error("the leaf outlived its last generation")
	}
	r.oom.mu.Lock()
	_, kept := r.oom.seen[ckX]
	r.oom.mu.Unlock()
	if kept {
		t.Error("a removed leaf's count is still kept")
	}
	err := r.looped(errors.New("backend crash-looping (3 exits)"), "apps/x", inst)
	if got := err.Error(); got != "backend crash-looping (3 exits)"+oomWords {
		t.Errorf("the breaker's words: %q", got)
	}
	if !crashLooped(err) || crashLooped(errors.New("x")) {
		t.Error("crashLooped doesn't tell the breaker's error")
	}
	var pcl partCrashLoop
	if !errors.As(r.looped(partCrashLoop{errors.New("x")}, "apps/x", inst), &pcl) || !sticky(r.looped(partCrashLoop{errors.New("x")}, "apps/x", inst)) {
		t.Error("a person's partition's breaker error lost its type under the OOM words")
	}

	// a generation without kills: no line, no event, no OOM words
	r.joinLeaf("apps/x", "main", ckX, "b.sock", fakePid+1)
	cg.exit(ckX, fakePid+1)
	log.Reset()
	other := &instance{gen: 5, leaf: ckX, sock: "b.sock"}
	r.noteExit(c, "main", other, &log)
	if log.Len() != 0 || len(tape.take()) != 0 {
		t.Errorf("an exit without kills: log %q", log.String())
	}
	if got := r.looped(errors.New("loop"), "apps/x", &instance{leaf: "elsewhere"}).Error(); got != "loop" {
		t.Errorf("a loop in a leaf without kills: %q", got)
	}
	// without cgroups: nothing read, nothing told
	(&Runner{}).noteExit(c, "main", inst, &log)
}

// covers D203 — one count per leaf: blue/green's two generations share a
// leaf, so the second exit tells only the kills since the first; the
// alerts' poll (AtLimitTile) takes the kills of a live leaf, which the
// exit doesn't tell again; a leaf made fresh counts from its own start; a
// leaf the runner never saw joined (a leftover) doesn't report its past;
// a person's partition's exit says so; a VM's leaf is logged but neither
// told nor marked (its guest's size is a kind of its own).
func TestOOMSharedLeafDelta(t *testing.T) {
	cg := newFakeCgroup(tileCaps)
	var tape oomTape
	r := &Runner{cgOps: cg, LimitHooks: LimitHooks{OnOOM: tape.on}}
	c := &registry.Component{Path: "apps/x"}
	var log bytes.Buffer
	setKills := func(leaf string, n int64) {
		cg.mu.Lock()
		cg.kills[leaf], cg.usage[leaf] = n, cgroup.Usage{MemMax: 1 << 30}
		cg.mu.Unlock()
	}

	g1 := &instance{gen: 1, leaf: leafXdev, sock: "g1.sock"}
	g2 := &instance{gen: 2, leaf: leafXdev, sock: "g2.sock"}
	r.joinLeaf("apps/x", "dev", leafXdev, g1.sock, fakePid)
	setKills(leafXdev, 2)
	r.joinLeaf("apps/x", "dev", leafXdev, g2.sock, fakePid+1) // the leaf exists: its count stays where it was
	hits := r.AtLimitTile("apps/x")
	if len(hits) != 1 || hits[0].OOMKills != 2 || hits[0].LimitMiB != 1024 || hits[0].Deployment != "dev" {
		t.Fatalf("the poll: %+v, want dev's leaf with its 2 kills at 1 GiB", hits)
	}
	if again := r.AtLimitTile("apps/x"); again[0].OOMKills != 0 {
		t.Errorf("a second poll told the same kills: %+v", again)
	}
	cg.exit(leafXdev, fakePid)
	r.noteExit(c, "dev", g1, &log) // the poll took them
	if evs := tape.take(); len(evs) != 0 || log.Len() != 0 {
		t.Errorf("g1's exit told the poll's kills again: %+v %q", evs, log.String())
	}
	if !cg.has(leafXdev) {
		t.Fatal("g1's exit removed the leaf g2 is in")
	}
	setKills(leafXdev, 3)
	cg.exit(leafXdev, fakePid+1)
	r.noteExit(c, "dev", g2, &log)
	want := []OOMEvent{{Tile: "apps/x", Deployment: "dev", Gen: 2, Kills: 1, LimitMiB: 1024}}
	if got := tape.take(); !reflect.DeepEqual(got, want) {
		t.Errorf("g2's exit: %+v, want the one kill since g1's: %+v", got, want)
	}

	// the leaf made fresh again counts from its own start
	g3 := &instance{gen: 3, leaf: leafXdev, sock: "g3.sock"}
	r.joinLeaf("apps/x", "dev", leafXdev, g3.sock, fakePid+2)
	setKills(leafXdev, 1)
	cg.exit(leafXdev, fakePid+2)
	r.noteExit(c, "dev", g3, &log)
	if got := tape.take(); len(got) != 1 || got[0].Kills != 1 {
		t.Errorf("a fresh leaf's exit: %+v, want its one kill", got)
	}

	// a leftover leaf: its past isn't news, what follows is
	cg.join("add", leafXmn, fakePid+3, tileCaps, 0)
	setKills(leafXmn, 5)
	if h := r.AtLimitTile("apps/x"); len(h) != 1 || h[0].OOMKills != 0 {
		t.Errorf("a leftover leaf's first poll: %+v, want no kills", h)
	}
	setKills(leafXmn, 6)
	if h := r.AtLimitTile("apps/x"); len(h) != 1 || h[0].OOMKills != 1 {
		t.Errorf("a leftover leaf's next kill: %+v", h)
	}

	// a person's partition's exit
	pl := partitionLeaf("apps/x", "main", "u-0123456789abcdef0123456789abcdef")
	pv := partitionView(c, "user:alice", "u-0123456789abcdef0123456789abcdef")
	r.joinLeaf("apps/x", "main", pl, "p.sock", fakePid+4)
	setKills(pl, 1)
	cg.exit(pl, fakePid+4)
	r.noteExit(pv, "main", &instance{gen: 1, leaf: pl, sock: "p.sock"}, &log)
	if got := tape.take(); len(got) != 1 || !got[0].Partition || got[0].Tile != "apps/x" {
		t.Errorf("a person's partition's exit: %+v", got)
	}

	// a VM's leaf: logged, not told, not marked
	r.vms.res = map[string]vmRes{"vm.sock": {release: func() {}, leafMiB: 704, memMiB: 512}}
	vmLeaf := cgroup.DeploymentLeaf(ckX, "vm")
	vmInst := &instance{gen: 7, leaf: vmLeaf, sock: "vm.sock"}
	r.joinLeaf("apps/x", "vm", vmLeaf, vmInst.sock, fakePid+5)
	setKills(vmLeaf, 1)
	cg.exit(vmLeaf, fakePid+5)
	log.Reset()
	r.noteExit(c, "vm", vmInst, &log)
	if !strings.Contains(log.String(), "gen 7: OOM-killed") || len(tape.take()) != 0 {
		t.Errorf("a VM's exit: log %q", log.String())
	}
	if _, ok := r.oom.recent(vmLeaf, r.now()); ok {
		t.Error("a VM's kill was marked for the breaker")
	}
}

// covers D203 — the real exit path (startDeployment's wait goroutine) on
// the host: a backend the OOM killer took reads its leaf's kills before the
// leaf goes, the backend log says so, OnOOM hears it; three such exits in
// the window trip the breaker, whose words name the cap and how to raise
// it; ApplyLimits restarts what the breaker stopped, and the new generation
// joins with the raised cap.
func TestOOMExitAndBreakerOnTheHost(t *testing.T) {
	h := newHostWorld(t, 0)
	cg := newFakeCgroup(tileCaps)
	h.r.cgOps = cg
	var tape oomTape
	h.r.OnOOM = tape.on
	var mu sync.Mutex
	memMax := int64(2 << 30)
	h.r.LimitsFor = func(tile, dep string) cgroup.Limits {
		mu.Lock()
		defer mu.Unlock()
		return cgroup.Limits{MemMax: memMax, PidsMax: 512}
	}
	kill := func(gen int) {
		t.Helper()
		if got := h.ensure(); got != "g"+string(rune('0'+gen)) {
			t.Fatalf("ensure: %s, want g%d", got, gen)
		}
		inst := curOf(t, h.r, "apps/x", "main")
		cg.mu.Lock()
		cg.kills[ckX], cg.usage[ckX] = 1, cgroup.Usage{MemMax: memMax}
		cg.order = nil
		cg.mu.Unlock()
		if err := inst.cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		waitUntil(t, "the killed generation's exit", func() bool {
			s := h.r.existingState("apps/x")
			s.mu.Lock()
			defer s.mu.Unlock()
			return s.cur == nil
		})
		cg.mu.Lock()
		order := cg.order
		cg.mu.Unlock()
		if want := []string{"oom " + ckX, "remove " + ckX}; !reflect.DeepEqual(order, want) {
			t.Errorf("g%d's exit: %q, want %q", gen, order, want)
		}
	}
	for gen := 1; gen <= 3; gen++ {
		kill(gen)
	}
	if got := tape.take(); len(got) != 3 || got[2] != (OOMEvent{Tile: "apps/x", Deployment: "main", Gen: 3, Kills: 1, LimitMiB: 2048}) {
		t.Errorf("OnOOM: %+v", got)
	}
	b, _ := os.ReadFile(filepath.Join(h.root, ".xbin", "log", ckX+".log"))
	if !strings.Contains(string(b), "--- gen 3: OOM-killed at memory.max 2 GiB ---") {
		t.Errorf("the backend log:\n%s", b)
	}
	st := statusOf(h.r, "apps/x")
	if !strings.Contains(st, "backend crash-looping (3 exits)") || !strings.HasSuffix(st, oomWords) {
		t.Errorf("the breaker: %q, want the OOM words", st)
	}

	// raised: the breaker's state restarts, onto the new cap
	mu.Lock()
	memMax = 3 << 30
	mu.Unlock()
	cg.takeLog()
	h.r.ApplyLimits("apps/x", false)
	waitUntil(t, "g4 healthy", func() bool { return statusOf(h.r, "apps/x") == "healthy g4" })
	if !cg.logged("add " + ckX + " mem=3221225472 pids=512 weight=100") {
		t.Errorf("g4's join: %q, want the raised cap", cg.takeLog())
	}
}

// covers D203 — the deploy path's crash watch (watchGen) names an OOM kill
// in its breaker's words too.
func TestDeployBreakerNamesOOM(t *testing.T) {
	g := newDeployRig(t, goMan)
	g.ensure()
	if err := g.pause("c1"); err != nil {
		t.Fatal(err)
	}
	g.settle()
	g.takeAll()
	s := g.r.existingState(g.c.Path)
	s.mu.Lock()
	now := g.f.now()
	s.crashes = []time.Time{now, now}
	s.cur.leaf = "tile-x/d-main/backend"
	s.mu.Unlock()
	g.r.oom.mark("tile-x/d-main/backend", now, 2048)
	g.f.crash(g.c.Path, "main")
	g.settle()
	const loop = "backend crash-looping (3 exits); deploy a fixed checkpoint or restart it — see .xbin/log/apps~x-ebdae547.log"
	if a := g.ensure(); a != "error: "+loop+oomWords {
		t.Fatalf("ensure while crash-looping = %s", a)
	}
}

// covers D203 — ApplyLimits raises every live non-VM leaf of the tile (each
// deployment's, a person's partition's starting generation) to LimitsFor's
// cap, never a VM's leaf, never one already at or above it, never down;
// a lower cap waits for the next start; nothing restarts unless asked or a
// state is crash-looping.
func TestApplyLimitsResizesLiveLeaves(t *testing.T) {
	cg := newFakeCgroup(tileCaps)
	caps := map[string]int64{"main": 3 << 30, "dev": 3 << 30}
	r := &Runner{cgOps: cg, states: map[string]*state{}}
	r.LimitsFor = func(tile, dep string) cgroup.Limits { return cgroup.Limits{MemMax: caps[dep], PidsMax: 512} }
	add := func(leaf string, pid int, memMax int64) {
		cg.join("add", leaf, pid, tileCaps, 0)
		cg.usage[leaf] = cgroup.Usage{MemMax: memMax}
	}
	add(ckX, fakePid, 2<<30)
	add(leafXdev, fakePid+1, 1<<30)
	pl := partitionLeaf("apps/x", "main", "u-0123456789abcdef0123456789abcdef")
	add(pl, fakePid+2, 2<<30)
	r.states[stateKey("apps/x", "main")] = &state{comp: "apps/x", dep: "main", cur: &instance{leaf: ckX, sock: "m.sock"}}
	r.states[stateKey("apps/x", "dev")] = &state{comp: "apps/x", dep: "dev", cur: &instance{leaf: leafXdev, sock: "vm.sock"}}
	r.parts.states = map[string]*state{"p": {comp: "apps/x", dep: "main", pt: &partInfo{starting: &instance{leaf: pl, sock: "p.sock"}}}}
	r.states[stateKey("apps/y", "main")] = &state{comp: "apps/y", dep: "main", cur: &instance{leaf: "apps~y", sock: "y.sock"}}
	r.vms.res = map[string]vmRes{"vm.sock": {release: func() {}, leafMiB: 704, memMiB: 512}}
	cg.takeLog()

	r.ApplyLimits("apps/x", false)
	got := cg.takeLog()
	want := []string{"resize " + ckX + " mem=3221225472", "resize " + pl + " mem=3221225472"}
	if len(got) != 2 || !reflect.DeepEqual(map[string]bool{got[0]: true, got[1]: true}, map[string]bool{want[0]: true, want[1]: true}) {
		t.Errorf("ApplyLimits resized %q, want %q (never the VM's leaf, nor another tile's)", got, want)
	}

	r.ApplyLimits("apps/x", true) // at the cap already; no registry: no restart either
	caps["main"] = 1 << 30        // lower: at the next start
	r.ApplyLimits("apps/x", false)
	if got := cg.takeLog(); len(got) != 0 {
		t.Errorf("a leaf at its cap, or above a lower one, was resized: %q", got)
	}

	// a leaf without a cap is never cut; lifting a cap is a raise
	cg.usage[ckX] = cgroup.Usage{MemMax: -1}
	caps["main"] = 3 << 30
	r.ApplyLimits("apps/x", false)
	cg.usage[ckX] = cgroup.Usage{MemMax: 2 << 30}
	caps["main"] = 0
	r.ApplyLimits("apps/x", false)
	if got := cg.takeLog(); !reflect.DeepEqual(got, []string{"resize " + ckX + " mem=0", "resize " + pl + " mem=0"}) {
		t.Errorf("unlimited stays, a lift is a raise (main's and its partition's): %q", got)
	}
	(&Runner{states: map[string]*state{}}).ApplyLimits("apps/x", true) // no cgroups, no registry
}

// covers D203 — a VM backend's guest: the ceiling is the larger of the
// policy's size and the tile's grant; the manifest's "vm" sizes lower it,
// never raise it; no grant is today's.
func TestGuestSizeWithGrant(t *testing.T) {
	p := vm.Policy{MemMiB: 1024, VCPUs: 2}
	for _, c := range []struct {
		name      string
		o         *registry.VMOpt
		g         vmGrant
		mem, cpus int
	}{
		{"no ask, no grant", nil, vmGrant{}, 1024, 2},
		{"an ask above the policy, ungranted: clamped", &registry.VMOpt{MemMiB: 4096, VCPUs: 4}, vmGrant{}, 1024, 2},
		{"an ask granted", &registry.VMOpt{MemMiB: 4096, VCPUs: 4}, vmGrant{memMiB: 4096, vcpus: 4}, 4096, 4},
		{"an ask above its grant", &registry.VMOpt{MemMiB: 8192, VCPUs: 8}, vmGrant{memMiB: 4096, vcpus: 4}, 4096, 4},
		{"a raise without an ask", nil, vmGrant{memMiB: 2048, vcpus: 3}, 2048, 3},
		{"the manifest lowers below a grant", &registry.VMOpt{MemMiB: 512, VCPUs: 1}, vmGrant{memMiB: 4096, vcpus: 4}, 512, 1},
		{"a grant below the policy", nil, vmGrant{memMiB: 512, vcpus: 1}, 1024, 2},
	} {
		if mem, cpus := guestSize(p, c.o, c.g); mem != c.mem || cpus != c.cpus {
			t.Errorf("%s: %d MiB %d vCPUs, want %d %d", c.name, mem, cpus, c.mem, c.cpus)
		}
	}
	r := &Runner{}
	if g := r.vmGrant("apps/x"); g != (vmGrant{}) {
		t.Errorf("no hook: %+v", g)
	}
	r.VMGrant = func(tile string) (int, int) { return 2048, 4 }
	if g := r.vmGrant("apps/x"); g != (vmGrant{memMiB: 2048, vcpus: 4}) {
		t.Errorf("the hook's grant: %+v", g)
	}
}
