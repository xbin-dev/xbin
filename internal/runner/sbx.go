package runner

// sbx.go — the runner's side of the sandbox registry (internal/sbx, D112):
// one entry per backend generation while its process lives — blue/green
// lists two — and what the sandbox layer under a backend refused or failed
// at. A backend's own crashes and build errors aren't the sandbox's: they
// stay in its state and log.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sbx"
	"github.com/xbin-dev/xbin/internal/util"
)

// BackendVM is what a VM backend generation holds.
type BackendVM struct {
	MemMiB   int  `json:"memMiB"`
	VCPUs    int  `json:"vcpus"`
	Emulated bool `json:"emulated,omitempty"`
}

// vmInfo is the VM a generation (by its socket) runs in, if any.
func (r *Runner) vmInfo(sock string) (BackendVM, bool) {
	r.vms.mu.Lock()
	defer r.vms.mu.Unlock()
	res, ok := r.vms.res[sock]
	return BackendVM{MemMiB: res.memMiB, VCPUs: res.vcpus, Emulated: res.emulated}, ok
}

// intendedMode is how c's backend is isolated when it starts here.
func (r *Runner) intendedMode(c *registry.Component) sbx.Mode {
	switch {
	case r.wantsVM(c):
		return sbx.VM
	case r.Isolate && sandboxable(c.Manifest.Runtime):
		return sbx.Namespace
	}
	return sbx.Host
}

// modeOf is how a running generation is isolated.
func (r *Runner) modeOf(c *registry.Component, sock string) sbx.Mode {
	if _, ok := r.vmInfo(sock); ok {
		return sbx.VM
	}
	if r.Isolate && sandboxable(c.Manifest.Runtime) {
		return sbx.Namespace
	}
	return sbx.Host
}

// sbxAdd lists a started generation until the returned remove.
func (r *Runner) sbxAdd(c *registry.Component, gen int, sock string, pid int) func() {
	if r.Sandboxes == nil {
		return func() {}
	}
	e := sbx.Entry{ID: fmt.Sprintf("backend:%s:g%d", util.CompKey(c.Path), gen), Kind: sbx.Backend, Tile: c.Path,
		Mode: r.modeOf(c, sock), PID: pid, Gen: gen}
	if r.Cgroup.Enabled() {
		e.Leaf = util.CompKey(c.Path)
	}
	if v, ok := r.vmInfo(sock); ok {
		e.MemMiB, e.VCPUs, e.Accel = v.MemMiB, v.VCPUs, sbx.KVM
		if v.Emulated {
			e.Accel = sbx.Emulate
		}
	}
	return r.Sandboxes.Add(e)
}

// sbxFail records a sandbox that couldn't be set up or started for c.
func (r *Runner) sbxFail(c *registry.Component, stage sbx.Stage, err error) {
	r.Sandboxes.Fail(sbx.Failure{Kind: sbx.Backend, Tile: c.Path, Mode: r.intendedMode(c),
		Stage: sbx.StageOf(err, stage), Error: err.Error()})
}

// sbxExited records a generation whose sandbox layer died: the VM shim's
// failure exit (125) at any time, or the sandbox init's (127) at start.
func (r *Runner) sbxExited(c *registry.Component, mode sbx.Mode, ps *os.ProcessState, started time.Time) {
	if ps == nil || mode == sbx.Host {
		return
	}
	code := ps.ExitCode()
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return // stopped (drain, restart) or killed: not the sandbox's failure
	}
	switch {
	case code == 125 && mode == sbx.VM:
		r.sbxFail(c, sbx.Exit, fmt.Errorf("the VM exited (125) — see .xbin/log/%s.log", util.CompKey(c.Path)))
	case code == 127 && time.Since(started) < 10*time.Second:
		r.sbxFail(c, sbx.Exit, fmt.Errorf("the sandbox couldn't start the backend (127) — see .xbin/log/%s.log", util.CompKey(c.Path)))
	}
}

// errExited is a backend that exited before it became healthy.
var errExited = errors.New("the backend exited before it listened")
