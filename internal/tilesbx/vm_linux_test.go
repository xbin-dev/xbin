//go:build linux

package tilesbx

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sandbox/agentcore"
	"github.com/xbin-dev/xbin/internal/sandbox/vm/proto"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// fakeVMs is VM mode's VMs over a real vm.Manager's policy and books (its
// Reserve, TileSandbox's sub-budget, UsedTiles) with a fake Apply: the
// spec is turned into a resident VM's the way vm.Apply turns it, and the
// fake launcher runs it like any other.
type fakeVMs struct {
	*vm.Manager

	mu       sync.Mutex
	emulated bool
	applied  []vm.Options
	onApply  func() // runs inside Apply (a policy flipped mid-start)
	applyErr error  // Apply fails with it
}

func newFakeVMs(t *testing.T, p vm.Policy) *fakeVMs {
	f := &fakeVMs{Manager: &vm.Manager{Root: t.TempDir()}}
	if err := f.SetPolicy(p); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeVMs) Apply(_ context.Context, spec *sandbox.Spec, o vm.Options) error {
	f.mu.Lock()
	f.applied = append(f.applied, o)
	emulated, hook, failure := f.emulated, f.onApply, f.applyErr
	f.mu.Unlock()
	if hook != nil {
		hook()
	}
	if failure != nil {
		return failure
	}
	if !o.Resident || spec.Agent == nil {
		return errors.New("not a resident VM")
	}
	hs := &proto.HostSpec{Resident: true, MemMiB: o.MemMiB, VCPUs: o.VCPUs, Hostname: o.Hostname, Disk: o.Disk}
	if emulated {
		hs.QEMU = "/qemu"
	} else {
		hs.Firecracker = "/firecracker"
	}
	spec.VM, spec.Lower, spec.Upper, spec.Work = hs, nil, "", ""
	spec.Entry, spec.Argv = sandbox.VMDir+"/bin/bx", []string{"bx", "__vm-host", sandbox.VMSpecPath}
	return nil
}

func (f *fakeVMs) lastApplied() vm.Options {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.applied[len(f.applied)-1]
}

// vmModes is boot's Modes adapter over the fake: the policy's switches
// (vm.Manager.TileVMs reads the same two, and the host's probe).
type vmModes struct{ f *fakeVMs }

func (m vmModes) VM() (string, string) {
	p := m.f.Policy()
	m.f.mu.Lock()
	emulated := m.f.emulated
	m.f.mu.Unlock()
	switch {
	case !p.Tiles:
		return "", "an admin hasn't enabled VM tile sandboxes (vm policy: tiles)"
	case emulated && !p.TilesEmulated:
		return "", "VMs would run emulated here, and an admin hasn't allowed that (vm policy: tilesEmulated)"
	case emulated:
		return accelEmulate, ""
	}
	return accelKVM, ""
}

// newVMEnv is a fake runtime with VM mode on: a 4 GiB VM budget, 2 GiB of
// it for tile sandboxes. Its shims exit 129 on SIGHUP, as the real one.
func newVMEnv(t *testing.T) (*fakeEnv, *fakeVMs) {
	fv := newFakeVMs(t, vm.Policy{Tiles: true, BudgetMiB: 4096, TilesBudgetMiB: 2048})
	fe := newFakeEnv(t, func(o *Options) { o.Deps.VM, o.Deps.Modes = fv, vmModes{fv} })
	fe.l.onSignal = func(p *fakeProc, s os.Signal) {
		if s == syscall.SIGHUP {
			go p.die(ExitStatus{Code: 129})
		}
	}
	return fe, fv
}

func vmDef(name string, memMiB int) map[string]any {
	return map[string]any{"name": name, "mode": "vm", "memMiB": memMiB, "vcpus": 1, "diskGiB": 2}
}

// A VM start books the VM's memory against the tile sub-budget, makes its
// disk, and runs a resident VM's spec — the factory and the lock kept, no
// upper — in a leaf sized for a VM; a stop flushes, hangs the shim up and
// gives everything back.
func TestVMStartStop(t *testing.T) {
	fe, fv := newVMEnv(t)
	var syncs atomic.Int32
	fe.l.mod = func(o *agentcore.Options) { o.Sync = func() { syncs.Add(1) } }
	fe.create(vmDef("vm-1", 512))
	w := fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusOK, "")
	in := fe.info(w)
	if in.State != StateRunning || in.Accel != accelKVM || in.Mode != ModeVM || in.Users != "any" {
		t.Fatalf("started: %+v", in)
	}
	sp := fe.l.last().spec
	if sp.VM == nil || !sp.VM.Resident || sp.Agent == nil || sp.Lock == nil || !sp.NoFollow || sp.Net != "relay" ||
		sp.Hostname != "vm-1" || sp.FuseWatch || sp.Upper != "" {
		t.Fatalf("the spec: %+v (vm %+v)", sp, sp.VM)
	}
	d, _ := fe.m.defs.get(fe.k, "vm-1")
	cur, _ := fe.m.CurDir(fe.k, d)
	o := fv.lastApplied()
	if !o.Resident || o.MemMiB != 512 || o.VCPUs != 1 || o.Hostname != "vm-1" || o.Disk != filepath.Join(cur, "vm", "disk.img") ||
		o.Listen != "" || o.Gateway != "" || o.TTY {
		t.Fatalf("vm.Options: %+v", o)
	}
	var st syscall.Stat_t
	if err := syscall.Stat(o.Disk, &st); err != nil || st.Size != 2<<30 || st.Blocks > 8 {
		t.Fatalf("the disk: %+v %v", st, err)
	}
	for _, sub := range []string{"upper", "work"} {
		if _, err := os.Lstat(filepath.Join(cur, sub)); !os.IsNotExist(err) {
			t.Errorf("a VM's cur/ has %s: %v", sub, err)
		}
	}
	if d.Base != "b-test" {
		t.Fatalf("the pin: %q", d.Base)
	}
	if u := fv.UsedTiles(); u.VMs != 1 || u.MemMiB != 512 {
		t.Fatalf("the tile sub-budget holds %+v", u)
	}
	rows := fe.sbx.List(sbx.Filter{Kind: sbx.Tile})
	if len(rows) != 1 || rows[0].Mode != sbx.VM || rows[0].Accel != sbx.KVM || rows[0].MemMiB != 512 {
		t.Fatalf("registry: %+v", rows)
	}
	r := fe.runOf("vm-1")
	if got := r.ops.readyWait(r); got != 60*time.Second {
		t.Fatalf("ready wait %s", got)
	}
	if out, ex := execRun(t, r, []string{"sh", "-c", "echo in-vm"}); out != "in-vm\n" || ex.Code != 0 {
		t.Fatalf("exec: %q %+v", out, ex)
	}

	before := syncs.Load() // an exec's exit syncs too (no NoSync)
	start := time.Now()
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/stop", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.StateDetail != "" || in.Accel != "" {
		t.Fatalf("stopped: %+v", in)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("the stop took %s: the shim's exit on SIGHUP wasn't waited for", time.Since(start))
	}
	if syncs.Load() != before+1 { // the guest was flushed before the hang-up
		t.Fatalf("syncs: %d before the stop, %d after", before, syncs.Load())
	}
	p := fe.l.last()
	if len(p.signals) != 1 || p.signals[0] != syscall.SIGHUP {
		t.Fatalf("the shim was sent %v", p.signals)
	}
	if r.exit.Code != 129 {
		t.Fatalf("the shim ended %+v, not by its own exit on SIGHUP", r.exit)
	}
	assertTornDown(t, fe, r)
	if u := fv.UsedTiles(); u.VMs != 0 || u.MemMiB != 0 || fv.Used().VMs != 0 {
		t.Fatalf("the VM reservation after the stop: tiles %+v, all %+v", u, fv.Used())
	}
}

// A shim that doesn't exit when hung up is killed.
func TestVMStopKillsAShimThatStays(t *testing.T) {
	fe, _ := newVMEnv(t)
	fe.l.onSignal = nil // SIGHUP ignored
	defer func(d time.Duration) { vmHupWait = d }(vmHupWait)
	vmHupWait = 100 * time.Millisecond
	fe.create(vmDef("vm-1", 512))
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	r := fe.runOf("vm-1")
	w := fe.do(mgr, "POST", "/sandboxes/vm-1/stop", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.StateDetail != "" {
		t.Fatalf("stopped: %+v", in)
	}
	if r.exit.Signal != syscall.SIGKILL {
		t.Fatalf("the shim ended %+v", r.exit)
	}
	assertTornDown(t, fe, r)
}

// A VM's leaf (§6.2): guest memory plus the VMM's overhead (more when
// emulated), no memory.high, 512 pids, a vCPU more than its own.
func TestVMLeaf(t *testing.T) {
	d := &Def{MemMiB: 1024, VCPUs: 2}
	l := vmOps.leaf(d, Limits{}, accelKVM)
	if l.MemMax != int64(1024+vm.VMOverheadMiB)<<20 || l.MemHigh >= 0 || l.PidsMax != 512 || l.CPUMax != 300000 || l.CPUWeight != 100 || l.NoSwap {
		t.Fatalf("kvm leaf: %+v", l)
	}
	if l := vmOps.leaf(d, Limits{}, accelEmulate); l.MemMax != int64(1024+vm.EmulatedOverheadMiB)<<20 {
		t.Fatalf("emulated leaf: %+v", l)
	}
	if r := (&run{accel: accelEmulate}); vmOps.readyWait(r) != 180*time.Second {
		t.Fatalf("emulated ready wait: %s", vmOps.readyWait(r))
	}
}

// A diskGiB grow applies at the next start; the disk never shrinks.
func TestVMDiskGrows(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	disk := fv.lastApplied().Disk
	w := fe.do(mgr, "PATCH", "/sandboxes/vm-1", map[string]any{"diskGiB": 3})
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); !in.RestartNeeded || in.DiskGiB != 2 {
		t.Fatalf("patched while running: %+v", in)
	}
	if fi, _ := os.Stat(disk); fi.Size() != 2<<30 {
		t.Fatalf("the disk grew while it ran: %d", fi.Size())
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/stop", nil), http.StatusOK, "")
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	if fi, _ := os.Stat(disk); fi.Size() != 3<<30 || fv.lastApplied().Disk != disk {
		t.Fatalf("after the restart: %d at %s", fi.Size(), fv.lastApplied().Disk)
	}
	fe.want(fe.do(mgr, "PATCH", "/sandboxes/vm-1", map[string]any{"diskGiB": 1}), http.StatusBadRequest, RefInvalid)
}

// The tile sub-budget refuses a VM that would pass it: 429 limit, recorded
// as a refusal, and nothing of the start is left.
func TestVMSubBudget(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 1024))
	fe.create(vmDef("vm-2", 1536))
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	w := fe.do(mgr, "POST", "/sandboxes/vm-2/start", nil)
	fe.want(w, http.StatusTooManyRequests, RefLimit)
	if !strings.Contains(w.Body.String(), "tile sandboxes (2048 MiB)") {
		t.Fatalf("the refusal: %s", w.Body.String())
	}
	if in := fe.get("vm-2"); in.State != StateStopped || !strings.Contains(in.StateDetail, "2048 MiB") {
		t.Fatalf("vm-2: %+v", in)
	}
	f := fe.sbx.Failures(sbx.Filter{Kind: sbx.Tile})
	if len(f) != 1 || f[0].Stage != sbx.Refused || f[0].Mode != sbx.VM {
		t.Fatalf("failures: %+v", f)
	}
	if u := fv.UsedTiles(); u.VMs != 1 || u.MemMiB != 1024 || fe.held() != 1 || fe.l.count() != 1 {
		t.Fatalf("after the refusal: tiles %+v, books %d, launches %d", u, fe.held(), fe.l.count())
	}
	d, _ := fe.m.defs.get(fe.k, "vm-2")
	dir, _ := fe.m.StateDir(fe.k, d)
	lock, err := lockState(dir)
	if err != nil {
		t.Fatalf("the lock after the refusal: %v", err)
	}
	lock.Close()
}

// A start whose VM can't be made gives everything back — the VM
// reservation, the book, the lock; nothing launched: vm.Apply's refusal
// (VMs unavailable here) is 503 unavailable, its other failures leave the
// sandbox stopped with why, and so does a disk that isn't a plain file (a
// symlink in its place is refused, never followed).
func TestVMApplyFails(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	d, _ := fe.m.defs.get(fe.k, "vm-1")
	dir, _ := fe.m.StateDir(fe.k, d)
	nothingLeft := func(what string) {
		t.Helper()
		if fv.UsedTiles().VMs != 0 || fv.Used().VMs != 0 || fe.held() != 0 || fe.l.count() != 0 {
			t.Fatalf("%s left: VMs %+v (tiles %+v), books %d, launches %d", what, fv.Used(), fv.UsedTiles(), fe.held(), fe.l.count())
		}
		lock, err := lockState(dir)
		if err != nil {
			t.Fatalf("%s: the lock: %v", what, err)
		}
		lock.Close()
	}
	setErr := func(err error) {
		fv.mu.Lock()
		fv.applyErr = err
		fv.mu.Unlock()
	}

	setErr(sbx.Refuse(fmt.Errorf("%w: no /dev/kvm", vm.ErrUnavailable)))
	w := fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !strings.Contains(w.Body.String(), "no /dev/kvm") {
		t.Fatalf("the refusal: %s", w.Body.String())
	}
	nothingLeft("a refused VM")

	setErr(errors.New("a VM sandbox can't export /.xbin-vm"))
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || in.StateDetail != "its VM: a VM sandbox can't export /.xbin-vm" {
		t.Fatalf("a failed VM: %+v", in)
	}
	nothingLeft("a failed VM")

	setErr(nil)
	cur, _ := fe.m.CurDir(fe.k, d)
	disk := filepath.Join(cur, "vm", "disk.img")
	elsewhere := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.Remove(disk); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, disk); err != nil {
		t.Fatal(err)
	}
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateStopped || !strings.Contains(in.StateDetail, "is a symlink") {
		t.Fatalf("a symlinked disk: %+v", in)
	}
	if _, err := os.Lstat(elsewhere); !os.IsNotExist(err) {
		t.Fatalf("the disk's symlink was followed: %v", err)
	}
	nothingLeft("a symlinked disk")
}

// VM mode follows the VM policy: create is invalid and a start unavailable
// while tiles is off, or VMs would run emulated without tilesEmulated —
// with the reason, and never a namespace instead.
func TestVMGate(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	set := func(p vm.Policy) {
		t.Helper()
		if err := fv.SetPolicy(p); err != nil {
			t.Fatal(err)
		}
	}
	set(vm.Policy{Tiles: false, BudgetMiB: 4096})
	w := fe.do(mgr, "POST", "/sandboxes", vmDef("vm-2", 512))
	fe.want(w, http.StatusBadRequest, RefInvalid)
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !strings.Contains(w.Body.String(), "vm policy: tiles") {
		t.Fatalf("the refusal: %s", w.Body.String())
	}
	fv.mu.Lock()
	fv.emulated = true
	fv.mu.Unlock()
	set(vm.Policy{Tiles: true, BudgetMiB: 4096})
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusServiceUnavailable, RefUnavailable)
	if !strings.Contains(w.Body.String(), "tilesEmulated") {
		t.Fatalf("the refusal: %s", w.Body.String())
	}
	if fe.l.count() != 0 || fe.held() != 0 || fv.UsedTiles().VMs != 0 {
		t.Fatalf("a refused start left launches %d, books %d, VMs %+v", fe.l.count(), fe.held(), fv.UsedTiles())
	}
	set(vm.Policy{Tiles: true, TilesEmulated: true, BudgetMiB: 4096})
	w = fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusOK, "")
	if in := fe.info(w); in.State != StateRunning || in.Accel != accelEmulate {
		t.Fatalf("emulated: %+v", in)
	}
	if r := fe.runOf("vm-1"); r.ops.readyWait(r) != 3*vmReadyWait {
		t.Fatalf("emulated ready wait %s", r.ops.readyWait(r))
	}
	if rows := fe.sbx.List(sbx.Filter{Kind: sbx.Tile}); len(rows) != 1 || rows[0].Accel != sbx.Emulate {
		t.Fatalf("registry: %+v", rows)
	}
	for _, f := range fe.sbx.Failures(sbx.Filter{Kind: sbx.Tile}) {
		if f.Stage != sbx.Refused {
			t.Fatalf("a gate's refusal recorded as %+v", f)
		}
	}
	// no VM manager: VM mode can't run, whatever the policy says
	fe2 := newFakeEnv(t, func(o *Options) { o.Deps.Modes = vmModes{fv} })
	fe2.create(vmDef("vm-1", 512))
	w = fe2.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe2.want(w, http.StatusServiceUnavailable, RefUnavailable)
}

// A switch turned off while a VM starts stops the start once it's up: it
// never runs under a policy that forbids it.
func TestVMGateRecheckedAfterTheStart(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	fv.onApply = func() { _ = fv.SetPolicy(vm.Policy{BudgetMiB: 4096}) } // the admin, mid-start
	w := fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil)
	fe.want(w, http.StatusOK, "")
	in := fe.info(w)
	if in.State != StateStopped || !strings.Contains(in.StateDetail, "vm policy: tiles") {
		t.Fatalf("started under tiles off: %+v", in)
	}
	if fe.held() != 0 || fv.UsedTiles().VMs != 0 || len(fe.m.running(nil)) != 0 {
		t.Fatalf("left: books %d, VMs %+v", fe.held(), fv.UsedTiles())
	}
}

// Turning tiles off stops the running VM sandboxes (not the namespace
// ones); turning tilesEmulated off stops those running emulated; a change
// that turns neither off stops nothing.
func TestOnVMPolicy(t *testing.T) {
	fe, fv := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	fe.create(ns("ns-1"))
	for _, n := range []string{"vm-1", "ns-1"} {
		fe.want(fe.do(mgr, "POST", "/sandboxes/"+n+"/start", nil), http.StatusOK, "")
	}
	on := vm.Policy{Tiles: true, TilesEmulated: true, BudgetMiB: 4096}
	fe.m.OnVMPolicy(on, vm.Policy{Tiles: true, BudgetMiB: 4096}) // tilesEmulated off, but this VM runs on KVM
	fe.m.OnVMPolicy(on, on)
	time.Sleep(50 * time.Millisecond)
	if fe.get("vm-1").State != StateRunning {
		t.Fatalf("a KVM sandbox stopped for tilesEmulated: %+v", fe.get("vm-1"))
	}
	if err := fv.SetPolicy(vm.Policy{BudgetMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	fe.m.OnVMPolicy(on, vm.Policy{BudgetMiB: 4096})
	in := fe.waitState("vm-1", StateStopped)
	if !strings.Contains(in.StateDetail, "vm policy: tiles") {
		t.Fatalf("stateDetail %q", in.StateDetail)
	}
	if fe.get("ns-1").State != StateRunning {
		t.Fatal("a namespace sandbox stopped for the VM policy")
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusServiceUnavailable, RefUnavailable)

	// emulated: tilesEmulated off stops it
	fv.mu.Lock()
	fv.emulated = true
	fv.mu.Unlock()
	if err := fv.SetPolicy(on); err != nil {
		t.Fatal(err)
	}
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	if err := fv.SetPolicy(vm.Policy{Tiles: true, BudgetMiB: 4096}); err != nil {
		t.Fatal(err)
	}
	fe.m.OnVMPolicy(on, vm.Policy{Tiles: true, BudgetMiB: 4096})
	in = fe.waitState("vm-1", StateStopped)
	if !strings.Contains(in.StateDetail, "tilesEmulated") {
		t.Fatalf("stateDetail %q", in.StateDetail)
	}
	if fv.UsedTiles().VMs != 0 {
		t.Fatalf("the VM reservation: %+v", fv.UsedTiles())
	}
}

// A VM that dies ends its run with the tail of its console: what the shim
// quoted on its way out (exit 125), control characters dropped.
func TestVMDied(t *testing.T) {
	fe, _ := newVMEnv(t)
	fe.create(vmDef("vm-1", 512))
	fe.want(fe.do(mgr, "POST", "/sandboxes/vm-1/start", nil), http.StatusOK, "")
	p := fe.l.last()
	fmt.Fprintf(p.log, "vm sandbox: something earlier\n--- VM console ---\n[    0.1] booting\n[    2.5] \x1b[31mKernel panic\x1b[0m - not syncing\r\n\nvm sandbox: the VM exited\n")
	p.die(ExitStatus{Code: vmExitVMM})
	in := fe.waitState("vm-1", StateStopped)
	want := "the VM exited: [    0.1] booting\n[    2.5] [31mKernel panic[0m - not syncing"
	if in.StateDetail != want {
		t.Fatalf("stateDetail %q, want %q", in.StateDetail, want)
	}
	if f := fe.sbx.Failures(sbx.Filter{Kind: sbx.Tile}); len(f) != 1 || f[0].Stage != sbx.Exit || !strings.Contains(f[0].Error, "Kernel panic") {
		t.Fatalf("failures: %+v", f)
	}
}

func TestVMDiedReasons(t *testing.T) {
	long := strings.Repeat("x", 3000)
	for _, c := range []struct{ log, want string }{
		{"--- VM console ---\nlast words\nvm sandbox: the VM exited", "the VM exited: last words"},
		{"vm sandbox: guest agent: EOF", "the VM failed: guest agent: EOF"},
		{"--- VM console ---\na\nb\nvm sandbox: guest agent: EOF", "the VM exited: a\nb"}, // the VMM died: its console says why
		{"--- VM console ---\n\nvm sandbox: start firecracker: permission denied", "the VM failed: start firecracker: permission denied"},
		{"", "the VM exited"},
		{"--- VM console ---\n" + long + "\nvm sandbox: the VM exited", "the VM exited: " + long[:vmDetailMax]},
		{"--- VM console ---\n1\n2\n3\n4\n5\n6\n7\n8\n9\n10\nvm sandbox: the VM exited", "the VM exited: 3\n4\n5\n6\n7\n8\n9\n10"},
		// the 8-bit CSI (C1, as UTF-8) and friends are control characters too
		{"--- VM console ---\n\u009b2J\u0085a\u0090b\x1b[1mc\x7f\nvm sandbox: the VM exited", "the VM exited: 2Jab[1mc"},
	} {
		if got := vmDied(c.log); got != c.want {
			t.Errorf("vmDied(%q) = %q, want %q", c.log, got, c.want)
		}
	}
	// a start the VM never answered: the console, once
	r := &run{ops: vmOps, log: &logRing{}, exit: ExitStatus{Code: vmExitVMM}}
	fmt.Fprintf(r.log, "--- VM console ---\nno init found\nvm sandbox: the VM exited\n")
	m := &Manager{}
	if got := m.exitReason(r, 0); got != "the sandbox didn't start: the VM exited: no init found" {
		t.Fatalf("not started: %q", got)
	}
	// its shim's other ends, with the log's last line when it didn't start
	r = &run{ops: vmOps, log: &logRing{}, exit: ExitStatus{Code: 127}}
	fmt.Fprintf(r.log, "[sbx] bind /dev/kvm: permission denied\n")
	if got := m.exitReason(r, 0); got != "the sandbox didn't start: the sandbox's VM ended (its shim exited with code 127): [sbx] bind /dev/kvm: permission denied" {
		t.Fatalf("init failed: %q", got)
	}
	r = &run{ops: vmOps, log: &logRing{}, ready: true, exit: ExitStatus{Code: vmExitVMM}}
	if got := m.exitReason(r, 3); got != "out of memory: 3 processes were killed" {
		t.Fatalf("oom: %q", got)
	}
}
