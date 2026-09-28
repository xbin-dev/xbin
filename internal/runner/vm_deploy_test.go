package runner

// covers D127q T10 SC-PRIMARY-FIRST — the VM books of the runner's deployment
// edges (07-runtime §10.2, §12): TestVMReserveOwnerIsTile and
// TestPrimaryFirstStopsNonPrimaryGuests. The runner books every guest to
// its tile, passes the headroom option for a non-primary view and the
// primary-first callback for the primary's, against a real vm.Manager's
// books (no VM runs: Reserve only counts).

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// vmBooks gives f's runner a VM manager whose policy admits maxVMs guests of
// 512 MiB, and writes apps/x's manifest, "vm" on or off, into the registry
// (the primary's code, 07-runtime §5.1).
func vmBooks(t *testing.T, f *depFake, maxVMs int, vmOn bool) (*vm.Manager, vm.Policy) {
	t.Helper()
	m := &vm.Manager{Root: t.TempDir()}
	if err := m.SetPolicy(vm.Policy{Backends: true, MemMiB: 512, MaxVMs: maxVMs}); err != nil {
		t.Fatal(err)
	}
	f.r.VM = m
	setVM(t, f, vmOn)
	return m, m.Policy()
}

func setVM(t *testing.T, f *depFake, on bool) {
	t.Helper()
	man := `{"runtime":"go"}`
	if on {
		man = `{"runtime":"go","vm":true}`
	}
	if err := os.WriteFile(filepath.Join(f.comps["apps/x"].Dir, "xbin.json"), []byte(man), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.r.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
}

// covers T10 D127q — TestVMReserveOwnerIsTile (15-test-plan §3.2): every
// deployment's VM backend reserves with the owner set to the tile, so
// UsedBy()[tile] sums them and no "<tile>+<name>" owner ever appears; a
// non-primary reservation leaves the primary's next guest free while the
// primary's code runs a VM, and nothing when it doesn't (07-runtime §10.2).
func TestVMReserveOwnerIsTile(t *testing.T) {
	f, _ := newDepFake(t, "apps/x")
	m, p := vmBooks(t, f, 4, true)
	c := f.comps["apps/x"]

	relMain, err := f.r.vmReserve(c, p, 512)
	if err != nil {
		t.Fatalf("the primary's guest: %v", err)
	}
	relDev, err := f.r.vmReserve(nonPrimary(c, "dev"), p, 512)
	if err != nil {
		t.Fatalf("dev's guest: %v", err)
	}
	relQA, err := f.r.vmReserve(nonPrimary(c, "qa"), p, 512)
	if err != nil {
		t.Fatalf("qa's guest: %v", err)
	}
	if got, want := m.UsedBy(), map[string]vm.Usage{"apps/x": {VMs: 3, MemMiB: 1536}}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedBy = %v, want every deployment's guest on the tile: %v", got, want)
	}
	for owner := range m.UsedBy() {
		if strings.Contains(owner, "+") {
			t.Errorf("a deployment became a budget owner: %q", owner)
		}
	}

	// A fourth guest fits the budget (4 × 512 MiB) but not beside the
	// primary's next one: a non-primary deployment's is refused.
	if _, err := f.r.vmReserve(nonPrimary(c, "dev"), p, 512); err == nil || !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("a non-primary guest that leaves no room for the primary's: %v, want a refusal", err)
	}
	// With no VM in the primary's code there is nothing to keep free.
	setVM(t, f, false)
	relDev2, err := f.r.vmReserve(nonPrimary(c, "dev"), p, 512)
	if err != nil {
		t.Errorf("a non-primary guest while the primary runs no VM: %v", err)
	} else {
		relDev2()
	}
	relMain()
	relDev()
	relQA()
	if got := m.UsedBy(); len(got) != 0 {
		t.Errorf("after every release: %v", got)
	}
}

// covers D127q T10 SC-PRIMARY-FIRST — when the VM budget can't admit the
// primary's next guest, the runner's primary-first callback stops the same
// tile's non-primary guests, which give their reservations back before the
// stop returns, and the primary's guest is admitted; a non-primary start is
// refused rather than preempting, and stops nothing; when the tile's
// non-primary guests can't make the room, nothing is stopped and the
// primary's guest is refused as without the option.
func TestPrimaryFirstStopsNonPrimaryGuests(t *testing.T) {
	f, _ := newDepFake(t, "apps/x")
	f.set("apps/x", "dev", "worktree")
	f.ensure("apps/x", "main")
	f.ensure("apps/x", "dev")
	f.settle()
	f.takeLog()
	m, p := vmBooks(t, f, 2, true)
	c := f.comps["apps/x"]

	// Both generations run in VMs, and a generation's exit gives its VM back
	// before its stop returns, as the shipped exit does.
	f.r.engine.stop = func(inst *instance, d time.Duration) { f.r.vmRelease(inst.sock); f.stop(inst, d) }
	for _, dep := range []string{"main", "dev"} {
		inst := curOf(t, f.r, "apps/x", dep)
		rel, err := m.Reserve("apps/x", 512)
		if err != nil {
			t.Fatal(err)
		}
		f.r.vms.mu.Lock()
		if f.r.vms.res == nil {
			f.r.vms.res = map[string]vmRes{}
		}
		f.r.vms.res[inst.sock] = vmRes{release: rel, memMiB: 512, leafMiB: 704}
		f.r.vms.mu.Unlock()
	}

	if _, err := f.r.vmReserve(nonPrimary(c, "dev"), p, 512); err == nil || !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("a non-primary guest past the budget: %v, want a refusal", err)
	}
	if got := f.takeLog(); len(got) != 0 {
		t.Errorf("a non-primary refusal stopped something: %q", got)
	}

	rel, err := f.r.vmReserve(c, p, 512) // the primary's blue/green guest
	if err != nil {
		t.Fatalf("the primary's guest: %v", err)
	}
	defer rel()
	if got, want := f.takeLog(), []string{"stop apps/x dev g1"}; !equalStrings(got, want) {
		t.Errorf("making room for the primary: %q, want %q", got, want)
	}
	if st := f.r.DeploymentStatus("apps/x", "dev"); st.Serving != "" {
		t.Errorf("dev still has its generation: %+v", st)
	}
	curOf(t, f.r, "apps/x", "main") // the primary's generation is untouched
	if got, want := m.UsedBy(), map[string]vm.Usage{"apps/x": {VMs: 2, MemMiB: 1024}}; !reflect.DeepEqual(got, want) {
		t.Errorf("UsedBy = %v, want %v", got, want)
	}

	// No non-primary guest is left to make room: the primary's is refused.
	if _, err := f.r.vmReserve(c, p, 512); err == nil || !errors.Is(err, sbx.ErrRefused) {
		t.Errorf("the primary's guest with no non-primary one to stop: %v, want a refusal", err)
	}
	if got := f.takeLog(); len(got) != 0 {
		t.Errorf("a refusal stopped something: %q", got)
	}
}
