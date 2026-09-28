package vm

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
)

// fakeTile stands in for the runner's side of one tile's VM books: its
// deployments' guests, each started with the option the runner passes (the
// primary PrimaryFirst, the others NonPrimary), and the stop callback that
// ends its non-primary guests when the primary needs their room.
type fakeTile struct {
	t    *testing.T
	m    *Manager
	path string

	mu     sync.Mutex
	guests map[string][]func() // releases, by deployment
	sizes  map[string][]int
	shorts []Usage // what each stop call was asked to free
}

func newFakeTile(t *testing.T, m *Manager, path string) *fakeTile {
	return &fakeTile{t: t, m: m, path: path, guests: map[string][]func(){}, sizes: map[string][]int{}}
}

// start reserves one guest of mem for dep; primaryMiB is what a non-primary
// start keeps free. The reservation runs under a deadline: Reserve holding
// its books while it calls stop would deadlock on the stop's releases.
func (f *fakeTile) start(dep string, mem, primaryMiB int, extra ...ReserveOption) error {
	opt := NonPrimary(primaryMiB)
	if dep == "main" {
		opt = PrimaryFirst(f.stopNonPrimary)
	}
	var rel func()
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		rel, err = f.m.Reserve(f.path, mem, append([]ReserveOption{opt}, extra...)...)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		f.t.Fatal("Reserve never returned: it holds its books while it calls stop")
	}
	if err == nil {
		f.mu.Lock()
		f.guests[dep] = append(f.guests[dep], rel)
		f.sizes[dep] = append(f.sizes[dep], mem)
		f.mu.Unlock()
	}
	return err
}

// exit ends dep's oldest guest (a crash, or the old generation of a swap).
func (f *fakeTile) exit(dep string) {
	f.mu.Lock()
	rel := f.guests[dep][0]
	f.guests[dep], f.sizes[dep] = f.guests[dep][1:], f.sizes[dep][1:]
	f.mu.Unlock()
	rel()
}

func (f *fakeTile) running(dep string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.guests[dep])
}

func (f *fakeTile) stops() []Usage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Usage(nil), f.shorts...)
}

// stopNonPrimary is the runner's callback: it stops every non-primary guest
// of the tile if together they hold short, and returns once they are
// released.
func (f *fakeTile) stopNonPrimary(short Usage) bool {
	_ = f.m.Used() // the books aren't held while stop runs
	f.mu.Lock()
	f.shorts = append(f.shorts, short)
	var have Usage
	var rels []func()
	for dep, sizes := range f.sizes {
		if dep == "main" {
			continue
		}
		for i, mem := range sizes {
			have.VMs++
			have.MemMiB += mem
			rels = append(rels, f.guests[dep][i])
		}
	}
	if have.VMs < short.VMs || have.MemMiB < short.MemMiB {
		f.mu.Unlock()
		return false
	}
	for dep := range f.guests {
		if dep != "main" {
			delete(f.guests, dep)
			delete(f.sizes, dep)
		}
	}
	f.mu.Unlock()
	for _, rel := range rels {
		rel()
	}
	return true
}

func newBooks(t *testing.T, p Policy) *Manager {
	t.Helper()
	m := &Manager{Root: t.TempDir()}
	if err := m.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	return m
}

// covers T10 — the VM books charge every deployment's reservation to the
// tile: UsedBy()[tile] sums the primary's and the non-primary deployments'
// guests, no "<tile>+<name>" owner (nor any other) ever appears, the served
// usedBy JSON keeps its shape, and each release takes its own guest off the
// tile's books.
func TestReserveChargesTileForEveryDeployment(t *testing.T) {
	m := newBooks(t, Policy{Backends: true, MemMiB: 1024, MaxVMs: 8, BudgetMiB: 8192})
	a := newFakeTile(t, m, "apps/a")
	if err := a.start("main", 1024, 0); err != nil {
		t.Fatal(err)
	}
	if err := a.start("dev", 512, 1024); err != nil {
		t.Fatal(err)
	}
	if err := a.start("staging", 256, 1024); err != nil {
		t.Fatal(err)
	}
	if err := a.start("dev", 512, 1024); err != nil { // dev's blue/green pair
		t.Fatal(err)
	}
	rb, err := m.Reserve("apps/b", 256) // another tile, no options: a VM terminal
	if err != nil {
		t.Fatal(err)
	}

	by := m.UsedBy()
	if len(by) != 2 || by["apps/a"] != (Usage{4, 2304}) || by["apps/b"] != (Usage{1, 256}) {
		t.Fatalf("usedBy: %+v", by)
	}
	for owner := range by {
		if strings.ContainsAny(owner, "+:") {
			t.Fatalf("a deployment became a budget owner: %q", owner)
		}
	}
	if m.Used() != (Usage{5, 2560}) {
		t.Fatalf("used: %+v", m.Used())
	}
	b, _ := json.Marshal(by)
	if string(b) != `{"apps/a":{"vms":4,"memMiB":2304},"apps/b":{"vms":1,"memMiB":256}}` {
		t.Fatalf("usedBy JSON: %s", b)
	}

	a.exit("dev")
	if got := m.UsedBy()["apps/a"]; got != (Usage{3, 1792}) {
		t.Fatalf("after dev's old generation: %+v", got)
	}
	a.exit("staging")
	a.exit("dev")
	if got := m.UsedBy()["apps/a"]; got != (Usage{1, 1024}) {
		t.Fatalf("after the non-primary guests: %+v", got)
	}
	a.exit("main")
	rb()
	if len(m.UsedBy()) != 0 || m.Used() != (Usage{}) {
		t.Fatalf("not all released: %+v %+v", m.UsedBy(), m.Used())
	}
	if len(a.stops()) != 0 {
		t.Fatalf("stop called with room to spare: %+v", a.stops())
	}
}

// covers P25 SC-PRIMARY-FIRST T10 — a non-primary deployment never starves
// its primary: its VM reservation leaves the primary's guest (its memory and
// one VM) free, so with the budget filled by the tile's non-primary guests up
// to that headroom a primary crash still restarts and a swap still fits,
// while one more non-primary start is refused as sbx.Refused (the D112
// failure ring's "refused"). When the budget can't admit a primary start,
// the runner's callback is asked, with the shortfall and without the books
// held, to stop the tile's non-primary guests, and the primary is admitted;
// a callback that can't make room leaves the refusal unchanged. A
// non-primary start never calls it, and a primary that runs no VM keeps
// nothing free.
func TestNonPrimaryVMCannotStarvePrimary(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		m := newBooks(t, Policy{Backends: true, MemMiB: 1024, MaxVMs: 8, BudgetMiB: 4096})
		a := newFakeTile(t, m, "apps/a")
		if err := a.start("main", 1024, 0); err != nil {
			t.Fatal(err)
		}
		for i := range 2 { // up to the headroom: 3072 held, 1024 free
			if err := a.start("dev", 1024, 1024); err != nil {
				t.Fatalf("dev guest %d: %v", i, err)
			}
		}
		err := a.start("dev", 1024, 1024)
		if !errors.Is(err, sbx.ErrRefused) || sbx.StageOf(err, sbx.Start) != sbx.Refused ||
			!strings.Contains(err.Error(), "VM memory budget (4096 MiB) has no room left for a non-primary deployment: 1024 MiB stays free for the tile's primary") {
			t.Fatalf("a non-primary start into the headroom: %v", err)
		}
		reg := sbx.New()
		reg.Fail(sbx.Failure{Kind: sbx.Backend, Tile: "apps/a", Deployment: "dev", Mode: sbx.VM, Stage: sbx.StageOf(err, sbx.Start), Error: err.Error()})
		if f := reg.Failures(sbx.Filter{Tile: "apps/a", Deployment: "dev"}); len(f) != 1 || f[0].Stage != sbx.Refused {
			t.Fatalf("failure ring: %+v", f)
		}

		// A primary crash restarts; a blue/green swap fits beside it.
		a.exit("main")
		if err := a.start("main", 1024, 0); err != nil {
			t.Fatalf("primary restart: %v", err)
		}
		if err := a.start("main", 1024, 0); err != nil {
			t.Fatalf("primary swap: %v", err)
		}
		a.exit("main")
		if len(a.stops()) != 0 || a.running("dev") != 2 {
			t.Fatalf("stopped non-primary guests with room to spare: %+v", a.stops())
		}

		// Another tile takes the headroom: the primary's next swap stops the
		// tile's non-primary guests and is admitted.
		rb, err := m.Reserve("apps/b", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.start("main", 1024, 0); err != nil {
			t.Fatalf("primary swap over a full budget: %v", err)
		}
		if s := a.stops(); len(s) != 1 || s[0] != (Usage{0, 1024}) {
			t.Fatalf("stop calls: %+v", s)
		}
		if a.running("dev") != 0 || m.UsedBy()["apps/a"] != (Usage{2, 2048}) || m.Used() != (Usage{3, 3072}) {
			t.Fatalf("after the stop: dev %d, %+v %+v", a.running("dev"), m.UsedBy(), m.Used())
		}

		// A non-primary start never preempts, even handed a callback.
		never := PrimaryFirst(func(Usage) bool { t.Error("a non-primary start called stop"); return true })
		if err := a.start("dev", 1024, 1024, never); !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "stays free for the tile's primary") {
			t.Fatalf("non-primary into the headroom: %v", err)
		}
		rb2, err := m.Reserve("apps/b", 1024)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.start("dev", 256, 1024, never); !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM memory budget (4096 MiB) is spent") {
			t.Fatalf("non-primary over a full budget: %v", err)
		}

		// Nothing left to stop: the primary's refusal is the plain one.
		_, plain := m.Reserve("apps/a", 1024)
		if err := a.start("main", 1024, 0); err == nil || err.Error() != plain.Error() || !errors.Is(err, sbx.ErrRefused) {
			t.Fatalf("primary with nothing to stop: %v (plain: %v)", err, plain)
		}
		if s := a.stops(); len(s) != 2 || s[1] != (Usage{0, 1024}) || m.Used() != (Usage{4, 4096}) {
			t.Fatalf("stop calls %+v, used %+v", s, m.Used())
		}
		rb()
		rb2()
	})

	t.Run("count", func(t *testing.T) {
		m := newBooks(t, Policy{Backends: true, MemMiB: 256, MaxVMs: 3, BudgetMiB: 1 << 20})
		a := newFakeTile(t, m, "apps/a")
		if err := a.start("main", 256, 0); err != nil {
			t.Fatal(err)
		}
		if err := a.start("dev", 256, 256); err != nil {
			t.Fatal(err)
		}
		err := a.start("dev", 256, 256)
		if !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM limit (3 running) has no room left for a non-primary deployment: one VM stays free for the tile's primary") {
			t.Fatalf("a non-primary start into the last VM: %v", err)
		}
		if _, err := m.Reserve("apps/b", 256); err != nil { // another tile takes the last VM
			t.Fatal(err)
		}
		if err := a.start("main", 256, 0); err != nil {
			t.Fatalf("primary swap at the VM limit: %v", err)
		}
		if s := a.stops(); len(s) != 1 || s[0] != (Usage{1, 0}) || a.running("dev") != 0 || a.running("main") != 2 {
			t.Fatalf("stop calls %+v, dev %d, main %d", s, a.running("dev"), a.running("main"))
		}
	})

	t.Run("primary runs no VM", func(t *testing.T) {
		m := newBooks(t, Policy{Backends: true, MemMiB: 1024, MaxVMs: 2, BudgetMiB: 2048})
		a := newFakeTile(t, m, "apps/a")
		for i := range 2 {
			if err := a.start("dev", 1024, 0); err != nil {
				t.Fatalf("dev guest %d: %v", i, err)
			}
		}
		if err := a.start("dev", 1024, 0); !errors.Is(err, sbx.ErrRefused) || !strings.Contains(err.Error(), "VM limit (2 running) is reached") {
			t.Fatalf("past the limit: %v", err)
		}
	})
}
