package tilesbx

// vm.go — VM mode (plans/tile-sandbox-runtime.md §2.5, §6.1–§6.3, §7):
// a tile sandbox as a resident microVM. Its Spec is namespace mode's
// turned into a VM jail by vm.Apply with Options.Resident — the jail's
// shim takes the connection factory and routes every connection xbind
// makes through it to the guest's agent — with its state on its own sparse
// disk (cur/vm/disk.img, grown to diskGiB at each start) and its memory
// booked against the workspace's VM count and budget and the tile
// sub-budget (tilesBudgetMiB). The VM policy's tiles and tilesEmulated gate
// every start, and switching either off stops what runs (OnVMPolicy). A
// stop flushes the guest, then hangs up the shim (SIGHUP: it flushes again
// and exits 129) and kills it only if it doesn't; a VMM that dies ends the
// run with the tail of the guest's console in stateDetail.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/xbin-dev/xbin/internal/cgroup"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/vm"
)

// VMs is the VM manager as VM mode uses it (*vm.Manager).
type VMs interface {
	// Apply turns a namespace spec into a VM jail; Resident: the shim
	// serves spec.Agent, and there is no session 1. A refusal (VMs
	// unavailable here) is marked sbx.ErrRefused.
	Apply(ctx context.Context, spec *sandbox.Spec, o vm.Options) error
	// Reserve books a VM's memory against the workspace's VM count and
	// budget — and, with vm.TileSandbox(), the tile sub-budget — charged to
	// owner (the tile). A refusal is marked sbx.ErrRefused.
	Reserve(owner string, memMiB int, opts ...vm.ReserveOption) (release func(), err error)
}

// How a VM runs (Info.accel, the registry's Accel).
const (
	accelKVM     = string(sbx.KVM)     // Firecracker on KVM
	accelEmulate = string(sbx.Emulate) // QEMU's software emulation
)

const (
	vmReadyWait = 60 * time.Second // a VM's guest agent answers "ready" within (it boots first)
	vmApplyWait = 15 * time.Minute // vm.Apply may first build the base's VM image
	vmLeafPids  = 512              // the shim, the VMM, its file servers: nothing of the guest's
	vmExitVMM   = 125              // the shim's exit when the VM died (the console's tail on its stderr)
	emulateSlow = 3                // an emulated VM's waits are stretched (§14)
	vmConsole   = "--- VM console ---"
	vmDetailMax = 1024 // bytes of the console a stateDetail quotes
)

// vmHupWait is how long a stop gives a hung-up shim to flush the guest and
// exit (it waits for the guest at most 2 s, 12 s emulated) before killing
// it. A var: the tests shorten it.
var vmHupWait = 10 * time.Second

// vmOps is VM mode.
var vmOps = &modeOps{
	check:      (*Manager).vmGate,
	spec:       vmSpec,
	leaf:       vmLeaf,
	readyWait:  func(r *run) time.Duration { return slower(r, vmReadyWait) },
	stop:       vmStop,
	exitReason: vmExitReason,
}

// vmGate refuses a VM start while VM mode can't run tile sandboxes: the VM
// policy's tiles (and, where VMs are emulated, tilesEmulated) off, or the
// host unable to run VMs. Never a fallback to a namespace (D120).
func (m *Manager) vmGate() error {
	_, reason := m.vmMode()
	if reason == "" && m.deps.VM == nil {
		reason = "this xbind can't run VM tile sandboxes"
	}
	if reason != "" {
		return &Error{Refusal: RefUnavailable, Msg: "VM mode is unavailable: " + reason, RetryAfter: time.Minute}
	}
	return nil
}

// specAccel is how the spec's VM runs, as vm.Apply chose it ("" for a
// namespace sandbox's spec).
func specAccel(s *sandbox.Spec) string {
	switch {
	case s.VM == nil:
		return ""
	case s.VM.Emulated():
		return accelEmulate
	}
	return accelKVM
}

// slower stretches d for an emulated VM.
func slower(r *run, d time.Duration) time.Duration {
	if r.accel == accelEmulate {
		return emulateSlow * d
	}
	return d
}

// vmSpec is a VM sandbox's Spec (§7 step 5): its memory booked (the VM
// budget and the tile sub-budget; the release is the undo), its disk made
// or grown to diskGiB, and a namespace spec — the pinned base as the lower,
// the mounts as FUSE exports, the relay, the factory and the lock, its
// hostname — turned into a resident VM's jail by vm.Apply.
func vmSpec(m *Manager, k Key, d *Def, in specInput) (*sandbox.Spec, func(), error) {
	release, err := m.deps.VM.Reserve(k.Tile, d.MemMiB, vm.TileSandbox())
	if err != nil {
		return nil, nil, &Error{Refusal: RefLimit, Msg: err.Error(), RetryAfter: 30 * time.Second}
	}
	ok := false
	defer func() {
		if !ok {
			release()
		}
	}()
	disk, err := vm.EnsureDiskAt(in.Cur, int64(d.DiskGiB)<<30)
	if err != nil {
		return nil, nil, fmt.Errorf("its disk: %w", err)
	}
	spec := &sandbox.Spec{
		Lower:    []string{in.Lower},
		Binds:    append([]sandbox.Bind(nil), in.Binds...),
		Hostname: d.Name,
		HostUID:  os.Getuid(), HostGID: os.Getgid(),
		Net:      "relay",
		NoFollow: true,
		Agent:    in.Agent, Lock: in.Lock,
	}
	ctx, cancel := context.WithTimeout(context.Background(), vmApplyWait)
	defer cancel()
	err = m.deps.VM.Apply(ctx, spec, vm.Options{Resident: true, MemMiB: d.MemMiB, VCPUs: d.VCPUs, Disk: disk, Hostname: d.Name})
	switch {
	case errors.Is(err, sbx.ErrRefused):
		return nil, nil, &Error{Refusal: RefUnavailable, Msg: err.Error(), RetryAfter: time.Minute}
	case err != nil:
		return nil, nil, fmt.Errorf("its VM: %w", err)
	}
	ok = true
	return spec, release, nil
}

// vmLeaf is a VM sandbox's leaf (§6.2): its guest memory plus what the
// shim and the VMM hold past it (more under emulation: QEMU's translated
// code), no memory.high (a VM holds its guest's memory by design), a
// small pids cap (the guest's processes are the guest's), and one vCPU
// more than its own as a hard CPU cap — the VMM's threads beside the
// vCPUs'.
func vmLeaf(d *Def, _ Limits, accel string) cgroup.Limits {
	over := vm.VMOverheadMiB
	if accel == accelEmulate {
		over = vm.EmulatedOverheadMiB
	}
	return cgroup.Limits{
		MemMax:    int64(d.MemMiB+over) << 20,
		MemHigh:   -1,
		PidsMax:   vmLeafPids,
		CPUWeight: 100,
		CPUMax:    int64(d.VCPUs+1) * 100000,
	}
}

// vmStop is VM mode's stop (§7): the guest flushes (at most stopSync),
// then the shim is hung up — it flushes again and exits 129 — and killed
// when it hasn't exited within vmHupWait (stretched under emulation). A
// run already ending is only asked again.
func vmStop(m *Manager, r *run, why string) {
	r.mu.Lock()
	asked, agent := r.asked, r.agent
	r.mu.Unlock()
	if asked {
		m.end(r, why)
		return
	}
	if agent != nil {
		if err := agent.Sync(stopSync); err != nil {
			slog.Info("tile sandbox: stopping its VM without a sync", "tile", r.k.Tile, "sandbox", r.def.Name, "err", err)
		}
	}
	if !m.ask(r, why) {
		return // something else ended it meanwhile
	}
	if err := r.proc.Signal(syscall.SIGHUP); err == nil {
		t := time.NewTimer(slower(r, vmHupWait))
		defer t.Stop()
		select {
		case <-r.exited:
			return
		case <-t.C:
			slog.Warn("tile sandbox: its VM didn't hang up in time; killing it", "tile", r.k.Tile, "sandbox", r.def.Name)
		}
	}
	m.kill(r)
}

// vmExitReason is why a VM sandbox ended on its own: the VM died (the
// shim's 125) — with the tail of its console, or what the shim said — or
// its shim exited otherwise. Past memory.max it is the OOM killer's (the
// common reason).
func vmExitReason(r *run, oom int64) (string, bool) {
	switch {
	case oom > 0:
		return "", false
	case r.exit.Code == vmExitVMM:
		return vmDied(r.log.TailSince(r.logMark, 400)), true
	case r.exit.Signal > 0:
		return "the sandbox's VM ended (its shim was killed by " + sigName(r.exit.Signal) + ")", false
	case r.exit.Err != nil && r.exit.Code < 0:
		return "the sandbox's VM ended (" + r.exit.Err.Error() + ")", false
	}
	return fmt.Sprintf("the sandbox's VM ended (its shim exited with code %d)", r.exit.Code), false
}

// vmDied reads a dead VM's end from its run's log (host.fail's shape: the
// console's tail after vmConsole — written when the VMM exited — then
// "vm sandbox: <why>"): "the VM exited: <the console's last lines>", or,
// with no console quoted, "the VM failed: <why>" (a VMM that never ran, a
// guest agent that broke off). The guest's words are data: control
// characters are dropped and the quote is bounded.
func vmDied(log string) string {
	lines := strings.Split(log, "\n")
	msg := ""
	if n := len(lines); n > 0 {
		if s, ok := strings.CutPrefix(strings.TrimSpace(lines[n-1]), "vm sandbox: "); ok {
			msg, lines = s, lines[:n-1]
		}
	}
	var console []string
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) == vmConsole {
			console = lines[i+1:]
			break
		}
	}
	if tail := lastLines(console, 8, vmDetailMax); tail != "" {
		return "the VM exited: " + tail
	}
	if msg = lastLines([]string{msg}, 1, vmDetailMax); msg != "" && msg != "the VM exited" {
		return "the VM failed: " + msg
	}
	return "the VM exited"
}

// lastLines is the last n non-empty lines of lines, printable, joined, and
// at most max bytes (the end kept).
func lastLines(lines []string, n, max int) string {
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if l := strings.TrimSpace(printable(lines[i])); l != "" {
			out = append(out, l)
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	s := strings.Join(out, "\n")
	if len(s) > max {
		s = s[len(s)-max:]
		for len(s) > 0 && !utf8.RuneStart(s[0]) {
			s = s[1:]
		}
	}
	return s
}

// printable drops control characters — C0, DEL and C1 (a console's
// escapes, U+009B's too; a tty's \r) — and invalid UTF-8.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if r == utf8.RuneError || (unicode.IsControl(r) && r != '\t') {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
}

// OnVMPolicy is boot's hook for a change of the VM policy (PUT
// /vm/policy, the one place it changes; old is the stored policy it
// replaced): tiles switched off stops every running VM sandbox, and
// tilesEmulated switched off stops those running emulated. Their state is
// kept, and their next start is refused with the reason (vmGate). It
// returns at once; the stops run off the caller's goroutine.
func (m *Manager) OnVMPolicy(old, cur vm.Policy) {
	var why string
	var pick func(*run) bool
	switch {
	case old.Tiles && !cur.Tiles:
		why = "an admin switched VM tile sandboxes off (vm policy: tiles): stopped, state kept"
		pick = func(r *run) bool { return r.def.Mode == ModeVM }
	case old.TilesEmulated && !cur.TilesEmulated:
		why = "an admin stopped emulated VMs for tile sandboxes (vm policy: tilesEmulated): stopped, state kept"
		pick = func(r *run) bool { return r.def.Mode == ModeVM && r.accel == accelEmulate }
	default:
		return
	}
	var runs []*run
	for _, r := range m.running(nil) {
		if pick(r) {
			runs = append(runs, r)
		}
	}
	if len(runs) > 0 {
		slog.Info("tile sandboxes: the VM policy changed; stopping VM sandboxes", "count", len(runs), "why", why)
		go m.stopRuns(runs, why)
	}
}
