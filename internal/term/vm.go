package term

import (
	"context"
	"errors"
	"log/slog"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/vm"
)

// VM terminals (plans/vm-sandbox.md): the same sandbox spec a namespace
// terminal gets, turned by internal/vm into a Firecracker microVM (QEMU's
// emulation where KVM isn't usable) that runs the shell as root in its own
// kernel. The browser asks with ?vm=1 (the
// title-bar toggle); the admin's workspace policy has to allow it.

// VMStatus is what the terminal picker needs to show the VM toggle.
type VMStatus struct {
	Available bool   `json:"available"`          // a VM terminal would start
	Reason    string `json:"reason,omitempty"`   // why not
	Emulated  bool   `json:"emulated,omitempty"` // no KVM: software emulation, several times slower
	Note      string `json:"note,omitempty"`     // why emulated
	MemMiB    int    `json:"memMiB,omitempty"`
	VCPUs     int    `json:"vcpus,omitempty"`
}

// VMStatus reports whether VM terminals can open here, and why not.
func (m *Manager) VMStatus() VMStatus {
	if m.VM == nil {
		return VMStatus{Reason: "VM sandboxes need isolation (xbind --isolate)"}
	}
	p := m.VM.Policy()
	if !p.Terminals {
		return VMStatus{Reason: "an admin hasn't enabled VM terminals for this workspace"}
	}
	st := m.VM.Status()
	if !st.Available {
		return VMStatus{Reason: st.Reason}
	}
	return VMStatus{Available: true, Emulated: st.Emulated, Note: st.Note, MemMiB: p.MemMiB, VCPUs: p.VCPUs}
}

// vmRefusal is why a session with these options can't be a VM ("" = it can).
func (m *Manager) vmRefusal(o openOpts) string {
	if st := m.VMStatus(); !st.Available {
		return st.Reason
	}
	if o.netHost {
		return "host networking isn't available in a VM terminal — pick another network scope"
	}
	if o.gpu != "" && o.gpu != "none" {
		return "GPUs aren't available in VM terminals"
	}
	return ""
}

// applyVM turns spec into a VM sandbox under the workspace policy and
// reserves its memory; release gives the reservation back when the session
// ends. disk is the session's persistent disk image ("" = a fresh guest).
func (m *Manager) applyVM(spec *sandbox.Spec, rel string, o openOpts, disk string) (release func(), err error) {
	if why := m.vmRefusal(o); why != "" {
		return nil, errors.New(why)
	}
	p := m.VM.Policy()
	release, err = m.VM.Reserve(p.MemMiB)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := m.VM.Apply(ctx, spec, vm.Options{
		TTY:      o.kind != KindAgent,
		Disk:     disk,
		VCPUs:    p.VCPUs,
		MemMiB:   p.MemMiB,
		Hostname: vmHostname(rel),
	}); err != nil {
		release()
		return nil, err
	}
	return release, nil
}

// vmDisk is the persistent disk image in a tile's terminal layer (made and
// sized on demand), or "" when there is none to give.
func (m *Manager) vmDisk(layer string) string {
	if m.VM == nil {
		return ""
	}
	disk, err := m.VM.EnsureDisk(layer)
	if err != nil {
		slog.Warn("VM terminal disk (the session runs without one)", "layer", layer, "err", err)
		return ""
	}
	return disk
}

// vmLeafBytes is a VM session's cgroup leaf cap: guest memory + overhead.
func (m *Manager) vmLeafBytes() int64 {
	return int64(m.VM.Policy().MemMiB+m.VM.OverheadMiB()) << 20
}

// hangupVM ends a VM session gracefully: SIGHUP makes the shim have the
// guest flush its disk before the VM dies, then the usual SIGKILL follows
// (the pump sees the exit either way). false = not sent; kill outright.
func (s *Session) hangupVM() bool {
	p := s.cmd.Process
	if p.Signal(syscall.SIGHUP) != nil {
		return false
	}
	time.AfterFunc(3*time.Second, func() { _ = p.Kill() })
	if s.pty != nil {
		_ = s.pty.Close()
	}
	return true
}

// vmHostname names the guest after its tile: lowercase letters, digits and
// dashes, at most 63.
func vmHostname(rel string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(path.Base("/" + rel)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if h == "" {
		h = "xbin-vm"
	}
	if len(h) > 63 {
		h = h[:63]
	}
	return h
}
