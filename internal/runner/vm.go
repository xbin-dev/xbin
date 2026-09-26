package runner

import (
	"context"
	"errors"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/vm"
)

// VM backends (plans/vm-sandbox.md): a manifest with "vm" runs its backend
// in a Firecracker microVM — the same binds (FUSE over vsock), the same
// network policy (the relay, outside the VM), its sockets on a guest-local
// run dir bridged over vsock. Nothing about the proxy, the gateway or the
// logs changes; what can't cross (host networking, provider splices, GPUs,
// an env layer) is an error, never a silent namespace fallback.

// vmHealthTimeout covers a cold boot, a first rootfs image build and the
// backend's own start.
const vmHealthTimeout = 60 * time.Second

// vmRes is what a VM generation holds until it exits.
type vmRes struct {
	release  func()
	leafMiB  int
	stopping bool
}

type vmState struct {
	mu  sync.Mutex
	res map[string]vmRes // by the generation's listen socket
}

// wantsVM reports whether c's backend runs in a VM here.
func (r *Runner) wantsVM(c *registry.Component) bool {
	return r.Isolate && c.Manifest.VM.Enabled() && sandboxable(c.Manifest.Runtime)
}

func (r *Runner) healthFor(c *registry.Component) time.Duration {
	if !r.wantsVM(c) {
		return healthTimeout
	}
	if r.VM != nil && r.VM.Status().Emulated {
		return 3 * vmHealthTimeout // a software-emulated guest boots and starts slower
	}
	return vmHealthTimeout
}

// vmApply turns the backend's sandbox spec into a VM sandbox under the
// workspace policy (sizes clamped to it) and reserves its memory.
func (r *Runner) vmApply(c *registry.Component, spec *sandbox.Spec, dir, sock, gw string) error {
	if r.VM == nil {
		return errors.New(`this tile asks for a VM ("vm" in xbin.json), but VM sandboxes need isolation and KVM`)
	}
	p := r.VM.Policy()
	if !p.Backends {
		return errors.New(`this tile asks for a VM ("vm" in xbin.json), but an admin hasn't enabled VM backends (PUT /api/xbin/vm/policy)`)
	}
	if st := r.VM.Status(); !st.Available {
		return errors.New("this tile asks for a VM, but VM sandboxes can't run here: " + st.Reason)
	}
	if c.Manifest.Setup != "" {
		return errors.New(`"vm" can't be combined with "setup" yet — install the dependencies at start inside the VM (it is root), or drop "vm"`)
	}
	mem, cpus := p.MemMiB, p.VCPUs
	if o := c.Manifest.VM; o.MemMiB > 0 && o.MemMiB < mem {
		mem = o.MemMiB
	}
	if o := c.Manifest.VM; o.VCPUs > 0 && o.VCPUs < cpus {
		cpus = o.VCPUs
	}
	release, err := r.VM.Reserve(mem)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := r.VM.Apply(ctx, spec, vm.Options{
		VCPUs: cpus, MemMiB: mem, Hostname: hostname(c.Path),
		Local: []string{dir}, Listen: sock, Gateway: gw,
	}); err != nil {
		release()
		return err
	}
	r.vms.mu.Lock()
	if r.vms.res == nil {
		r.vms.res = map[string]vmRes{}
	}
	r.vms.res[sock] = vmRes{release: release, leafMiB: mem + r.VM.OverheadMiB()}
	r.vms.mu.Unlock()
	return nil
}

// vmLeafBytes is a VM generation's cgroup cap. Generations of one tile share
// its leaf, and blue/green briefly runs two, so the cap covers two VMs; each
// guest still can't use more than its own memory.
func (r *Runner) vmLeafBytes(sock string) (int64, bool) {
	r.vms.mu.Lock()
	defer r.vms.mu.Unlock()
	res, ok := r.vms.res[sock]
	return int64(2*res.leafMiB) << 20, ok
}

// vmRelease gives a generation's reservation back when it exits.
func (r *Runner) vmRelease(sock string) {
	r.vms.mu.Lock()
	res, ok := r.vms.res[sock]
	delete(r.vms.res, sock)
	r.vms.mu.Unlock()
	if ok {
		res.release()
	}
}

// stopFirst reports whether c's old generation must stop before the new one
// starts: a VM backend with file-backed resources (sqlite, filesystem) sees
// them through the VM file server, where two guests' caches — a WAL's shared memory above all —
// are not coherent with each other.
func (r *Runner) stopFirst(c *registry.Component) bool {
	if !r.wantsVM(c) || r.EnvForComponent == nil {
		return false
	}
	return len(resourceBinds(r.EnvForComponent(c), r.Root)) > 0
}

// hostname names a VM after its tile: lowercase letters, digits, dashes.
func hostname(comp string) string {
	var b strings.Builder
	for _, ch := range strings.ToLower(path.Base("/" + comp)) {
		switch {
		case ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9':
			b.WriteRune(ch)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
	}
	h := strings.Trim(b.String(), "-")
	if h == "" {
		return "xbin-vm"
	}
	if len(h) > 63 {
		h = h[:63]
	}
	return h
}
