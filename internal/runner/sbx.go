package runner

// sbx.go — the runner's side of the sandbox registry (internal/sbx, D112):
// one entry per backend generation while its process lives — blue/green
// lists two — and what the sandbox layer under a backend refused or failed
// at. A backend's own crashes and build errors aren't the sandbox's: they
// stay in its state and log.
//
// Entries and failures follow the name rule (P17): main's are exactly what
// they were before tile deployments, even on a tile with a record; every
// other deployment's carry its name in their ID and in Deployment, under
// the tile's path (P13).

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/xbin-dev/xbin/internal/cgroup"
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

// sbxAdd lists a started generation until the returned remove, in the
// cgroup leaf its deployment's generation starts in (genLeaf). start lists
// its generation with sbxAddLeaf and the leaf it placed it in.
func (r *Runner) sbxAdd(c *registry.Component, gen int, sock string, pid int) func() {
	return r.sbxAddLeaf(c, gen, sock, pid, r.genLeaf(c, r.viewDeployment(c)))
}

// sbxAddLeaf lists generation gen of the deployment view c describes, whose
// process runs in cgroup leaf leaf ("" = no cgroup accounting), until the
// returned remove.
func (r *Runner) sbxAddLeaf(c *registry.Component, gen int, sock string, pid int, leaf string) func() {
	if r.Sandboxes == nil {
		return func() {}
	}
	dep := r.sbxDeployment(c)
	e := sbx.Entry{ID: sbxID(c.Path, dep, gen), Kind: sbx.Backend, Tile: c.Path, Deployment: dep,
		Mode: r.modeOf(c, sock), PID: pid, Gen: gen, Leaf: leaf}
	if v, ok := r.vmInfo(sock); ok {
		e.MemMiB, e.VCPUs, e.Accel = v.MemMiB, v.VCPUs, sbx.KVM
		if v.Emulated {
			e.Accel = sbx.Emulate
		}
	}
	return r.Sandboxes.Add(e)
}

// sbxDeployment is what an entry or failure for the deployment view c
// describes records as its Deployment: the name, except main's, which is
// never recorded (P5, P17).
func (r *Runner) sbxDeployment(c *registry.Component) string {
	if dep := r.viewDeployment(c); dep != util.MainDeployment {
		return dep
	}
	return ""
}

// sbxID is a backend generation's registry ID: backend:<CompKey>:g<gen> for
// main, as before deployments; backend+<name>:<CompKey>:g<gen> for another
// deployment's (dep, never "main"). The prefixes differ, so two deployments
// that reach one generation number never share an ID, and a name holds no
// ':' (07-runtime §10.1).
func sbxID(tile, dep string, gen int) string {
	if dep == "" {
		return fmt.Sprintf("backend:%s:g%d", util.CompKey(tile), gen)
	}
	return fmt.Sprintf("backend+%s:%s:g%d", dep, util.CompKey(tile), gen)
}

// genLeaf is the cgroup leaf a new generation of deployment dep of c starts
// in (07-runtime §10.3), as the registry lists it: chooseLeaf's (limits.go),
// "" without cgroup accounting.
func (r *Runner) genLeaf(c *registry.Component, dep string) string {
	return r.listedLeaf(r.chooseLeaf(c.Path, dep))
}

// listedLeaf is leaf as a registry entry records it: "" without cgroup
// accounting, where no generation is placed in one.
func (r *Runner) listedLeaf(leaf string) string {
	if cg := r.cgroups(); cg == nil || !cg.Enabled() {
		return ""
	}
	return leaf
}

// leafFor is the leaf rule: the flat <CompKey> while main runs alone, so
// zero-state and main-only tiles keep today's leaf (P5); dep's backend leaf
// under the tile's parent for every other deployment, and for main once the
// tile has the parent (nested), from its next generation on.
func leafFor(key, dep string, nested bool) string {
	if dep == util.MainDeployment && !nested {
		return key
	}
	return cgroup.DeploymentLeaf(key, dep)
}

// sbxFail records a sandbox that couldn't be set up or started for the
// deployment view c.
func (r *Runner) sbxFail(c *registry.Component, stage sbx.Stage, err error) {
	r.Sandboxes.Fail(sbx.Failure{Kind: sbx.Backend, Tile: c.Path, Deployment: r.sbxDeployment(c),
		Mode: r.intendedMode(c), Stage: sbx.StageOf(err, stage), Error: err.Error()})
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
	logFile := deploymentLog(c.Path, r.viewDeployment(c)) // main's: .xbin/log/<CompKey>.log, as before
	switch {
	case code == 125 && mode == sbx.VM:
		r.sbxFail(c, sbx.Exit, fmt.Errorf("the VM exited (125) — see %s", logFile))
	case code == 127 && time.Since(started) < 10*time.Second:
		r.sbxFail(c, sbx.Exit, fmt.Errorf("the sandbox couldn't start the backend (127) — see %s", logFile))
	}
}

// GenUsage reads what each group of backend generations in es holds, by
// key(e): the cgroup leaf they share, read once, where it has accounting;
// else the sum of each generation's process tree (/proc, scanned at most
// once), CPU in µs. Keys that nothing answers are absent.
func (r *Runner) GenUsage(es []sbx.Entry, key func(sbx.Entry) string) map[string]cgroup.Usage {
	out := map[string]cgroup.Usage{}
	byLeaf := map[string]bool{}
	var children map[int][]int
	for _, e := range es {
		k := key(e)
		if byLeaf[k] {
			continue
		}
		if e.Leaf != "" {
			if u, ok := r.Cgroup.Usage(e.Leaf); ok {
				out[k], byLeaf[k] = u, true
				continue
			}
		}
		if e.PID <= 0 {
			continue
		}
		if children == nil {
			children = procChildren()
		}
		u := out[k]
		u.MemMax = -1
		for _, pid := range procDescendants(children, e.PID) {
			u.CPUUsec += procCPUJiffies(pid) * 10000 // USER_HZ=100
			u.MemCurrent += procRSSBytes(pid)
			u.PidsCurrent++
		}
		out[k] = u
	}
	return out
}

// errExited is a backend that exited before it became healthy.
var errExited = errors.New("the backend exited before it listened")
